package main

/*
Remote Desktops runtime: /api/vnc.

  POST /api/vnc/connections/{id}/sessions   connect + authenticate to the VNC server
                                             and return a one-time ticket (30 s, bound
                                             to the caller's IP)
  GET  /api/vnc/ws?ticket=...                WebSocket for noVNC (binary RFB)

Every open viewer is a vncSession in an in-memory registry. A loop re-checks each
one every vncEnforceInterval and disconnects it when the user's access is gone
(account disabled, token revoked, grant removed or reduced, allowed time window or
IP no longer matching, JWT expired), the connection was deleted or changed,
Remote Desktops were disabled, or the viewer was idle too long. Ended sessions are
kept for a while so the browser can ask why it was disconnected.
*/

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	vncTicketTTL         = 30 * time.Second
	vncEndedRetention    = 15 * time.Minute
	vncMaxThumbnailBytes = 400 * 1024
	vncWSReadBuffer      = 64 * 1024
)

var (
	vncEnforceInterval = 15 * time.Second
	vncThumbnailDir    = filepath.Join("run", "vnc_thumbnails")
)

type vncSession struct {
	ID           string
	VNCID        string
	Name         string // connection display name at connect time
	Username     string
	Role         string
	TokenVersion int
	TokenExpiry  time.Time
	ViewOnly     bool
	Fingerprint  string
	ClientIP     string
	UserAgent    string
	DesktopName  string
	Width        int
	Height       int
	Security     string

	CreatedAt  time.Time
	AttachedAt time.Time
	EndedAt    time.Time
	Status     string // pending | active | ended
	EndReason  string

	ticket        string
	ticketExpires time.Time
	server        net.Conn
	serverInit    []byte
	ws            *websocket.Conn
	lastActivity  atomic.Int64 // unix nanos
	bytesToViewer atomic.Int64
	bytesToServer atomic.Int64
	clipboard     atomic.Bool
	endOnce       sync.Once
}

func (s *vncSession) level() string {
	if s.ViewOnly {
		return vncLevelView
	}
	return vncLevelControl
}

func (s *vncSession) touch() { s.lastActivity.Store(time.Now().UnixNano()) }

// VNCAPI serves /api/vnc and owns the session registry.
type VNCAPI struct {
	auth     *AuthManager
	mu       sync.Mutex
	sessions map[string]*vncSession
	tickets  map[string]string // ticket -> session id
	upgrader websocket.Upgrader
	stop     chan struct{}
}

func NewVNCAPI(auth *AuthManager) *VNCAPI {
	api := &VNCAPI{
		auth:     auth,
		sessions: map[string]*vncSession{},
		tickets:  map[string]string{},
		stop:     make(chan struct{}),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  vncWSReadBuffer,
			WriteBufferSize: vncWSReadBuffer,
			Subprotocols:    []string{"binary"},
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true
				}
				if u, err := url.Parse(origin); err == nil && strings.EqualFold(u.Host, r.Host) {
					return true
				}
				return isAllowedOrigin(origin)
			},
		},
	}
	go api.enforceLoop()
	return api
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

func (v *VNCAPI) Handle(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/vnc"), "/")
	parts := []string{}
	if path != "" {
		parts = strings.Split(path, "/")
	}
	if len(parts) == 1 && parts[0] == "ws" {
		v.handleWebSocket(w, r)
		return
	}
	v.auth.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := ClaimsFromRequest(r)
		settings := currentVNCSettings()
		if !settings.enabled {
			sendJSONError(w, http.StatusForbidden, "Remote Desktops are disabled in configuration (vnc.enabled)")
			return
		}
		switch {
		case len(parts) == 1 && parts[0] == "connections" && r.Method == http.MethodGet:
			v.handleList(w, claims, settings)
		case len(parts) == 3 && parts[0] == "connections" && parts[2] == "sessions" && r.Method == http.MethodPost:
			v.handleCreate(w, r, claims, settings, parts[1])
		case len(parts) == 3 && parts[0] == "connections" && parts[2] == "thumbnail" && r.Method == http.MethodGet:
			v.handleThumbnailGet(w, claims, parts[1])
		case len(parts) == 3 && parts[0] == "connections" && parts[2] == "thumbnail" && r.Method == http.MethodPut:
			v.handleThumbnailPut(w, r, claims, parts[1])
		case len(parts) == 1 && parts[0] == "sessions" && r.Method == http.MethodGet:
			v.handleSessions(w, claims)
		case len(parts) == 2 && parts[0] == "sessions" && r.Method == http.MethodGet:
			v.handleSessionGet(w, claims, parts[1])
		case len(parts) == 2 && parts[0] == "sessions" && r.Method == http.MethodDelete:
			v.handleSessionDelete(w, r, claims, parts[1])
		case len(parts) == 3 && parts[0] == "sessions" && parts[2] == "ping" && r.Method == http.MethodPost:
			v.handlePing(w, claims, parts[1])
		case len(parts) >= 1 && (parts[0] == "connections" || parts[0] == "sessions"):
			sendJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		default:
			sendJSONError(w, http.StatusNotFound, "Endpoint not found")
		}
	})(w, r)
}

// callerLevel is the caller's access level for a connection: admin always control.
func (v *VNCAPI) callerLevel(claims *Claims, vncID string) string {
	if claims.Role == RoleAdmin {
		return vncLevelControl
	}
	return v.auth.userVNCLevel(claims.Username, vncID)
}

func (v *VNCAPI) canSeeSession(claims *Claims, s *vncSession) bool {
	return claims.Role == RoleAdmin || s.Username == claims.Username
}

// ---------------------------------------------------------------------------
// Views
// ---------------------------------------------------------------------------

func (v *VNCAPI) sessionView(s *vncSession, claims *Claims) map[string]interface{} {
	view := map[string]interface{}{
		"session_id": s.ID, "vnc_id": s.VNCID, "name": s.Name, "username": s.Username, "role": s.Role,
		"level": s.level(), "status": s.Status, "created_at": s.CreatedAt, "desktop_name": s.DesktopName,
		"width": s.Width, "height": s.Height, "mine": s.Username == claims.Username,
	}
	if !s.AttachedAt.IsZero() {
		view["attached_at"] = s.AttachedAt
	}
	if s.Status == "ended" {
		view["ended_at"] = s.EndedAt
		view["end_reason"] = s.EndReason
	}
	if claims.Role == RoleAdmin {
		view["client_ip"] = s.ClientIP
		view["security"] = s.Security
		view["bytes_to_viewer"] = s.bytesToViewer.Load()
		view["bytes_from_viewer"] = s.bytesToServer.Load()
		view["last_activity"] = time.Unix(0, s.lastActivity.Load())
	}
	return view
}

func vncTargetSummary(c VNCConnection) string {
	target := net.JoinHostPort(c.targetHost(), fmt.Sprint(c.port()))
	if c.TunnelHostRef != "" {
		target += " via SSH host " + c.TunnelHostRef
	}
	return target
}

func (v *VNCAPI) handleList(w http.ResponseWriter, claims *Claims, settings vncSettings) {
	cfg := cache.GetConfig()
	result := []map[string]interface{}{}
	if cfg != nil {
		v.mu.Lock()
		viewers := map[string][]map[string]interface{}{}
		for _, s := range v.sessions {
			if s.Status == "ended" {
				continue
			}
			entry := map[string]interface{}{"username": s.Username, "level": s.level(), "since": s.CreatedAt, "status": s.Status, "mine": s.Username == claims.Username}
			if v.canSeeSession(claims, s) {
				entry["session_id"] = s.ID
			}
			viewers[s.VNCID] = append(viewers[s.VNCID], entry)
		}
		v.mu.Unlock()
		for _, c := range cfg.VNCConnections {
			normalizeVNCConnection(&c)
			level := v.callerLevel(claims, c.VNCID)
			if level == "" {
				continue
			}
			list := viewers[c.VNCID]
			if list == nil {
				list = []map[string]interface{}{}
			}
			sort.Slice(list, func(i, j int) bool { return list[i]["since"].(time.Time).Before(list[j]["since"].(time.Time)) })
			view := map[string]interface{}{
				"vnc_id": c.VNCID, "name": c.displayName(), "description": c.Description, "level": level,
				"clipboard": c.clipboardAllowed(), "viewers": list,
			}
			if info, err := os.Stat(vncThumbnailPath(c.VNCID)); err == nil {
				view["thumbnail_at"] = info.ModTime()
			}
			if claims.Role == RoleAdmin {
				view["target"] = vncTargetSummary(c)
				view["auth_type"] = c.authType()
			}
			result = append(result, view)
		}
	}
	sendJSONSuccess(w, http.StatusOK, map[string]interface{}{
		"connections": result,
		"settings":    map[string]interface{}{"idle_disconnect_minutes": int(settings.idle.Minutes()), "max_sessions_per_user": settings.maxPerUser},
	}, "VNC connections retrieved")
}

func (v *VNCAPI) handleSessions(w http.ResponseWriter, claims *Claims) {
	v.mu.Lock()
	list := []map[string]interface{}{}
	for _, s := range v.sessions {
		if s.Status != "ended" && v.canSeeSession(claims, s) {
			list = append(list, v.sessionView(s, claims))
		}
	}
	v.mu.Unlock()
	sort.Slice(list, func(i, j int) bool {
		return list[i]["created_at"].(time.Time).Before(list[j]["created_at"].(time.Time))
	})
	sendJSONSuccess(w, http.StatusOK, list, "VNC sessions retrieved")
}

func (v *VNCAPI) lookup(claims *Claims, sid string) *vncSession {
	v.mu.Lock()
	defer v.mu.Unlock()
	s := v.sessions[sid]
	if s == nil || !v.canSeeSession(claims, s) {
		return nil
	}
	return s
}

func (v *VNCAPI) handleSessionGet(w http.ResponseWriter, claims *Claims, sid string) {
	s := v.lookup(claims, sid)
	if s == nil {
		sendJSONError(w, http.StatusNotFound, "VNC session not found")
		return
	}
	v.mu.Lock()
	view := v.sessionView(s, claims)
	v.mu.Unlock()
	sendJSONSuccess(w, http.StatusOK, view, "VNC session retrieved")
}

func (v *VNCAPI) handleSessionDelete(w http.ResponseWriter, r *http.Request, claims *Claims, sid string) {
	s := v.lookup(claims, sid)
	if s == nil {
		sendJSONError(w, http.StatusNotFound, "VNC session not found")
		return
	}
	if s.Username != claims.Username {
		writeSecurityAudit(r, claims.Username, claims.Role, "vnc_kick", s.VNCID, "success", map[string]interface{}{"session_id": s.ID, "user": s.Username})
		v.end(s, "Disconnected by the administrator")
	} else {
		v.end(s, "Disconnected")
	}
	sendJSONSuccess(w, http.StatusOK, nil, "VNC session closed")
}

func (v *VNCAPI) handlePing(w http.ResponseWriter, claims *Claims, sid string) {
	s := v.lookup(claims, sid)
	if s == nil || s.Username != claims.Username {
		sendJSONError(w, http.StatusNotFound, "VNC session not found")
		return
	}
	s.touch()
	sendJSONSuccess(w, http.StatusOK, nil, "OK")
}

// ---------------------------------------------------------------------------
// Connect
// ---------------------------------------------------------------------------

func (v *VNCAPI) handleCreate(w http.ResponseWriter, r *http.Request, claims *Claims, settings vncSettings, vncID string) {
	cfg := cache.GetConfig()
	conn, ok := findVNCConnection(cfg, vncID)
	level := ""
	if ok {
		level = v.callerLevel(claims, vncID)
	}
	if !ok || level == "" {
		sendJSONError(w, http.StatusNotFound, "VNC connection not found")
		return
	}
	var req struct {
		ViewOnly bool `json:"view_only"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			sendJSONError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
	}
	viewOnly := level == vncLevelView || req.ViewOnly

	v.mu.Lock()
	open := 0
	for _, s := range v.sessions {
		if s.Username == claims.Username && s.Status != "ended" {
			open++
		}
	}
	v.mu.Unlock()
	if open >= settings.maxPerUser {
		sendJSONError(w, http.StatusConflict, fmt.Sprintf("Remote desktop limit reached (%d open). Close a desktop first.", open))
		return
	}

	server, info, err := dialVNCServer(conn, cfg.Hosts, vncDialTimeout)
	if err != nil {
		writeSecurityAudit(r, claims.Username, claims.Role, "vnc_connect", vncID, "failed", map[string]interface{}{"level": vncLevelFor(viewOnly), "error": err.Error()})
		sendJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	now := time.Now()
	s := &vncSession{
		ID: randomToken(12), VNCID: vncID, Name: conn.displayName(), Username: claims.Username, Role: claims.Role,
		TokenVersion: claims.TokenVersion, ViewOnly: viewOnly, Fingerprint: conn.fingerprint(),
		ClientIP: getClientIP(r), UserAgent: r.UserAgent(), DesktopName: info.Name, Width: info.Width, Height: info.Height,
		Security: info.SecurityName, CreatedAt: now, Status: "pending",
		ticket: randomToken(24), ticketExpires: now.Add(vncTicketTTL), server: server, serverInit: info.ServerInit,
	}
	if claims.ExpiresAt != nil {
		s.TokenExpiry = claims.ExpiresAt.Time
	}
	s.clipboard.Store(conn.clipboardAllowed() && !viewOnly)
	s.touch()
	v.mu.Lock()
	v.sessions[s.ID] = s
	v.tickets[s.ticket] = s.ID
	v.mu.Unlock()
	time.AfterFunc(vncTicketTTL, func() {
		v.mu.Lock()
		pending := s.Status == "pending"
		v.mu.Unlock()
		if pending {
			v.end(s, "The viewer did not connect in time")
		}
	})

	writeSecurityAudit(r, claims.Username, claims.Role, "vnc_connect", vncID, "success", map[string]interface{}{
		"session_id": s.ID, "level": s.level(), "desktop_name": info.Name, "security": info.SecurityName,
	})
	sendJSONSuccess(w, http.StatusCreated, map[string]interface{}{
		"session_id": s.ID, "ticket": s.ticket, "ws_path": "/api/vnc/ws?ticket=" + s.ticket,
		"level": s.level(), "view_only": viewOnly, "clipboard": s.clipboard.Load(),
		"desktop_name": info.Name, "width": info.Width, "height": info.Height, "name": s.Name,
	}, "VNC session created")
}

func vncLevelFor(viewOnly bool) string {
	if viewOnly {
		return vncLevelView
	}
	return vncLevelControl
}

// ---------------------------------------------------------------------------
// WebSocket bridge
// ---------------------------------------------------------------------------

// wsBinaryReader turns the binary messages of a WebSocket into a byte stream.
type wsBinaryReader struct {
	conn  *websocket.Conn
	cur   io.Reader
	onMsg func(n int)
}

func (r *wsBinaryReader) Read(p []byte) (int, error) {
	for {
		if r.cur == nil {
			mt, reader, err := r.conn.NextReader()
			if err != nil {
				return 0, err
			}
			_ = r.conn.SetReadDeadline(time.Now().Add(wsPongWait))
			if mt != websocket.BinaryMessage {
				_, _ = io.Copy(io.Discard, reader)
				continue
			}
			r.cur = reader
		}
		n, err := r.cur.Read(p)
		if n > 0 && r.onMsg != nil {
			r.onMsg(n)
		}
		if err == io.EOF {
			r.cur = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

type wsBinaryWriter struct{ conn *websocket.Conn }

func (w wsBinaryWriter) Write(p []byte) (int, error) {
	_ = w.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	if err := w.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

type countingWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

func (v *VNCAPI) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	ticket := r.URL.Query().Get("ticket")
	v.mu.Lock()
	sid := v.tickets[ticket]
	delete(v.tickets, ticket) // one-time
	s := v.sessions[sid]
	valid := ticket != "" && s != nil && s.Status == "pending" && time.Now().Before(s.ticketExpires)
	v.mu.Unlock()
	if !valid {
		sendJSONError(w, http.StatusUnauthorized, "Invalid or expired VNC ticket")
		return
	}
	if ip := getClientIP(r); ip != s.ClientIP {
		writeSecurityAudit(r, s.Username, s.Role, "vnc_ticket_ip_mismatch", s.VNCID, "denied", map[string]interface{}{"session_id": s.ID, "expected_ip": s.ClientIP})
		v.end(s, "The viewer connected from a different address")
		sendJSONError(w, http.StatusForbidden, "VNC ticket was issued to a different address")
		return
	}
	ws, err := v.upgrader.Upgrade(w, r, nil)
	if err != nil {
		v.end(s, "The viewer could not open its connection")
		return
	}
	v.mu.Lock()
	if s.Status != "pending" { // ended in the meantime
		v.mu.Unlock()
		ws.Close()
		return
	}
	s.Status = "active"
	s.AttachedAt = time.Now()
	s.ws = ws
	v.mu.Unlock()
	s.touch()

	ws.SetReadLimit(vncMaxCutText + 1<<20)
	_ = ws.SetReadDeadline(time.Now().Add(wsPongWait))
	ws.SetPongHandler(func(string) error { return ws.SetReadDeadline(time.Now().Add(wsPongWait)) })

	reader := &wsBinaryReader{conn: ws}
	writer := wsBinaryWriter{conn: ws}
	if err := rfbServeBrowserHandshake(reader, countingWriter{writer, &s.bytesToViewer}, s.serverInit); err != nil {
		v.end(s, "Viewer handshake failed: "+err.Error())
		return
	}

	// server -> viewer
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := s.server.Read(buf)
			if n > 0 {
				if _, werr := writer.Write(buf[:n]); werr != nil {
					v.end(s, "The viewer connection was lost")
					return
				}
				s.bytesToViewer.Add(int64(n))
			}
			if err != nil {
				v.end(s, "The VNC server closed the connection")
				return
			}
		}
	}()
	// keepalive
	go func() {
		ticker := time.NewTicker(wsPingPeriod)
		defer ticker.Stop()
		for range ticker.C {
			v.mu.Lock()
			ended := s.Status == "ended"
			v.mu.Unlock()
			if ended {
				return
			}
			if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)); err != nil {
				return
			}
		}
	}()

	filter := &vncClientFilter{viewOnly: s.ViewOnly, clipboard: s.clipboard.Load, onInput: s.touch}
	err = filter.run(reader, countingWriter{s.server, &s.bytesToServer})
	switch {
	case errors.Is(err, errVNCUnexpectedMessage):
		log.Printf("vnc: closing session %s (%s/%s): %v", s.ID, s.Username, s.VNCID, err)
		v.end(s, "Closed: "+err.Error())
	case websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway):
		v.end(s, "Disconnected")
	default:
		v.end(s, "The viewer connection was closed")
	}
}

// end closes a session once and records why; safe to call from anywhere.
func (v *VNCAPI) end(s *vncSession, reason string) {
	s.endOnce.Do(func() {
		v.mu.Lock()
		wasActive := s.Status == "active"
		s.Status = "ended"
		s.EndReason = reason
		s.EndedAt = time.Now()
		delete(v.tickets, s.ticket)
		ws := s.ws
		v.mu.Unlock()
		if s.server != nil {
			s.server.Close()
		}
		if ws != nil {
			msg := reason
			if len(msg) > 120 {
				msg = msg[:120]
			}
			_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4000, msg), time.Now().Add(time.Second))
			ws.Close()
		}
		details := map[string]interface{}{
			"session_id": s.ID, "level": s.level(), "reason": reason, "client_ip": s.ClientIP,
			"bytes_to_viewer": s.bytesToViewer.Load(), "bytes_from_viewer": s.bytesToServer.Load(),
		}
		if wasActive {
			details["duration_seconds"] = int(time.Since(s.AttachedAt).Seconds())
		}
		writeSecurityAudit(nil, s.Username, s.Role, "vnc_disconnect", s.VNCID, "success", details)
	})
}

// ---------------------------------------------------------------------------
// Enforcement
// ---------------------------------------------------------------------------

func (v *VNCAPI) enforceLoop() {
	ticker := time.NewTicker(vncEnforceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-v.stop:
			return
		case <-ticker.C:
			v.enforce(time.Now())
		}
	}
}

// enforce disconnects every session that is no longer allowed and prunes old ended ones.
func (v *VNCAPI) enforce(now time.Time) {
	settings := currentVNCSettings()
	cfg := cache.GetConfig()
	v.mu.Lock()
	open := []*vncSession{}
	active := map[*vncSession]bool{}
	for id, s := range v.sessions {
		if s.Status == "ended" {
			if now.Sub(s.EndedAt) > vncEndedRetention {
				delete(v.sessions, id)
			}
			continue
		}
		open = append(open, s)
		active[s] = s.Status == "active"
	}
	v.mu.Unlock()
	for _, s := range open {
		if reason := v.revocationReason(s, active[s], settings, cfg, now); reason != "" {
			v.end(s, reason)
		}
	}
}

func (v *VNCAPI) revocationReason(s *vncSession, active bool, settings vncSettings, cfg *Config, now time.Time) string {
	if !settings.enabled {
		return "Remote Desktops were disabled by the administrator"
	}
	conn, ok := findVNCConnection(cfg, s.VNCID)
	if !ok {
		return "This remote desktop connection was deleted"
	}
	if conn.fingerprint() != s.Fingerprint {
		return "The connection settings were changed by the administrator; reconnect to continue"
	}
	s.clipboard.Store(conn.clipboardAllowed() && !s.ViewOnly)
	if !s.TokenExpiry.IsZero() && now.After(s.TokenExpiry) {
		return "Your login session expired; sign in again"
	}
	if s.Role != RoleAdmin {
		v.auth.mu.RLock()
		user, exists := v.auth.users[canonicalUsername(s.Username)]
		v.auth.mu.RUnlock()
		if !exists || !user.Enabled || user.TokenVersion != s.TokenVersion {
			return "Your account was disabled or your sign-in was revoked"
		}
		if err := evaluateUserPolicy(user, s.ClientIP, now); err != nil {
			return "Your account is not allowed to connect at this time or from this address"
		}
		level := v.auth.userVNCLevel(s.Username, s.VNCID)
		if level == "" {
			return "Your access to this remote desktop was removed"
		}
		if level == vncLevelView && !s.ViewOnly {
			return "Your access to this remote desktop was changed to view-only; reconnect to continue"
		}
	}
	if settings.idle > 0 && active {
		if idle := now.Sub(time.Unix(0, s.lastActivity.Load())); idle > settings.idle {
			return fmt.Sprintf("Disconnected after %d minutes without activity", int(settings.idle.Minutes()))
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Thumbnails (last-seen snapshot taken by the browser)
// ---------------------------------------------------------------------------

func vncThumbnailPath(vncID string) string {
	return filepath.Join(vncThumbnailDir, vncID+".jpg")
}

func removeVNCThumbnail(vncID string) {
	if validVNCID(vncID) {
		_ = os.Remove(vncThumbnailPath(vncID))
	}
}

func (v *VNCAPI) handleThumbnailGet(w http.ResponseWriter, claims *Claims, vncID string) {
	if !validVNCID(vncID) || v.callerLevel(claims, vncID) == "" {
		sendJSONError(w, http.StatusNotFound, "Thumbnail not found")
		return
	}
	path := vncThumbnailPath(vncID)
	info, err := os.Stat(path)
	if err != nil {
		sendJSONError(w, http.StatusNotFound, "Thumbnail not found")
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		sendJSONError(w, http.StatusNotFound, "Thumbnail not found")
		return
	}
	sendJSONSuccess(w, http.StatusOK, map[string]interface{}{
		"data_url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data), "updated_at": info.ModTime(),
	}, "Thumbnail retrieved")
}

func (v *VNCAPI) handleThumbnailPut(w http.ResponseWriter, r *http.Request, claims *Claims, vncID string) {
	if !validVNCID(vncID) || v.callerLevel(claims, vncID) == "" {
		sendJSONError(w, http.StatusNotFound, "VNC connection not found")
		return
	}
	if _, ok := findVNCConnection(cache.GetConfig(), vncID); !ok {
		sendJSONError(w, http.StatusNotFound, "VNC connection not found")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, vncMaxThumbnailBytes+1))
	if err != nil || len(data) == 0 {
		sendJSONError(w, http.StatusBadRequest, "Empty thumbnail")
		return
	}
	if len(data) > vncMaxThumbnailBytes {
		sendJSONError(w, http.StatusRequestEntityTooLarge, "Thumbnail too large")
		return
	}
	if !bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}) {
		sendJSONError(w, http.StatusBadRequest, "Thumbnail must be a JPEG image")
		return
	}
	if err := os.MkdirAll(vncThumbnailDir, 0700); err != nil {
		sendJSONError(w, http.StatusInternalServerError, "Failed to save thumbnail")
		return
	}
	tmp := vncThumbnailPath(vncID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err == nil {
		err = os.Rename(tmp, vncThumbnailPath(vncID))
		if err != nil {
			sendJSONError(w, http.StatusInternalServerError, "Failed to save thumbnail")
			return
		}
	} else {
		sendJSONError(w, http.StatusInternalServerError, "Failed to save thumbnail")
		return
	}
	sendJSONSuccess(w, http.StatusOK, map[string]interface{}{"updated_at": time.Now()}, "Thumbnail saved")
}
