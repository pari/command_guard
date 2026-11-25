package main

/*
Persistent browser SSH terminals.

The interactive SSH sessions themselves live in a separate companion process,
cg_sessiond (see ./sessiond), so they survive restarts of cguard. This file
contains everything on the cguard side:

  - authorization (admin: every SSH host; users: hosts in their terminal_hosts grant)
  - the REST endpoints under /api/terminals used by the Terminals page
  - the WebSocket endpoint that bridges the browser (xterm.js) to the daemon
  - auto-starting the daemon over its Unix socket when it is not running

Only SSH hosts are supported; WinRM cannot provide an interactive PTY.
*/

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// TerminalsConfig is the optional top-level "terminals" block in config.json.
type TerminalsConfig struct {
	Enabled            *bool  `json:"enabled,omitempty"`               // Defaults to true when omitted
	MaxSessionsPerUser int    `json:"max_sessions_per_user,omitempty"` // Running sessions per account (default 10)
	ScrollbackBytes    string `json:"scrollback_bytes,omitempty"`      // Replayed on resume, e.g. "2MB" (default)
	IdleKillHours      int    `json:"idle_kill_hours,omitempty"`       // Kill sessions nobody is attached to for N hours (0 = never)
	ImagesEnabled      *bool  `json:"images_enabled,omitempty"`        // Image paste/drop into sessions (terminals_images.go); defaults to true
	ImageRetentionDays int    `json:"image_retention_days,omitempty"`  // Days pasted images stay on the host (default 7)

	AgentSessions []AgentSession `json:"agent_sessions,omitempty"` // Shared launch presets (terminals_agent_sessions.go)
}

const (
	terminalSocketPath    = "run/sessiond.sock"
	terminalDaemonBinary  = "cg_sessiond"
	terminalDaemonLog     = "logs/sessiond.log"
	terminalWSSubprotocol = "cg-jwt"
	terminalMaxNameRunes  = 40
	terminalOrderPath     = "run/terminal_order.json" // per-user tab order of the Sessions list
	terminalMaxOrderIDs   = 200

	terminalDefaultMaxSessionsPerUser = 10
	terminalMaxSessionsPerUserLimit   = terminalMaxOrderIDs // More tabs than this could not keep their saved order

	wsWriteWait  = 15 * time.Second
	wsPongWait   = 90 * time.Second
	wsPingPeriod = 30 * time.Second
)

// Frame types on the daemon stream. Must match sessiond/main.go.
const (
	frameData      byte = 0
	frameResize    byte = 1
	frameDetach    byte = 2
	frameExit      byte = 3
	frameTakeover  byte = 4
	frameReplayEnd byte = 5 // daemon finished replaying scrollback
)

const sessiondMaxFrame = 1 << 20

// terminalDaemonAutoStart lets tests disable spawning cg_sessiond.
var terminalDaemonAutoStart = true

// sessiondBastion carries the bastion (jump host) credentials for a create request.
// Mirrors bastionParams in sessiond/main.go.
type sessiondBastion struct {
	HostRef     string `json:"host_ref,omitempty"`
	Host        string `json:"host"`
	Port        int    `json:"port,omitempty"`
	Username    string `json:"username"`
	Password    string `json:"password,omitempty"`
	PEM         string `json:"pem,omitempty"`
	PEMPassword string `json:"pem_password,omitempty"`
}

// sessiondMinBastionVersion is the first cg_sessiond release that understands the
// bastion field of a create request. Older daemons would silently ignore it and
// dial the target directly, so create refuses with 501 until the daemon is restarted.
const sessiondMinBastionVersion = "1.2.0"

type sessiondRequest struct {
	Op              string `json:"op"`
	ID              string `json:"id,omitempty"`
	Owner           string `json:"owner,omitempty"`
	Name            string `json:"name,omitempty"`
	HostRef         string `json:"host_ref,omitempty"`
	Host            string `json:"host,omitempty"`
	Port            int    `json:"port,omitempty"`
	Username        string `json:"username,omitempty"`
	Password        string `json:"password,omitempty"`
	PEM             string `json:"pem,omitempty"`
	PEMPassword     string `json:"pem_password,omitempty"`
	Term            string `json:"term,omitempty"`
	Cols            int    `json:"cols,omitempty"`
	Rows            int    `json:"rows,omitempty"`
	ScrollbackBytes int    `json:"scrollback_bytes,omitempty"`
	IdleKillSeconds int    `json:"idle_kill_seconds,omitempty"`

	Bastion *sessiondBastion `json:"bastion,omitempty"` // create only; cg_sessiond >= 1.2.0
}

// TerminalSession mirrors the daemon's session description. It never carries credentials.
type TerminalSession struct {
	ID             string    `json:"id"`
	Owner          string    `json:"owner"`
	HostRef        string    `json:"host_ref"`
	Name           string    `json:"name"`
	Host           string    `json:"host"`
	Username       string    `json:"username"`
	Bastion        string    `json:"bastion,omitempty"` // host_ref of the bastion the session goes through
	CreatedAt      time.Time `json:"created_at"`
	LastAttachedAt time.Time `json:"last_attached_at"`
	LastDetachedAt time.Time `json:"last_detached_at"`
	Attached       bool      `json:"attached"`
	Cols           int       `json:"cols"`
	Rows           int       `json:"rows"`
	Status         string    `json:"status"`
	ExitCode       int       `json:"exit_code"`
	ExitReason     string    `json:"exit_reason,omitempty"`
	ExitedAt       time.Time `json:"exited_at,omitempty"`

	// Meta is added by cguard from run/terminal_meta.json for sessions created from an
	// Agent Session preset (icon, preset name); the daemon never sees it.
	Meta *TerminalSessionMeta `json:"meta,omitempty"`
}

type sessiondResponse struct {
	OK       bool              `json:"ok"`
	Error    string            `json:"error,omitempty"`
	Session  *TerminalSession  `json:"session,omitempty"`
	Sessions []TerminalSession `json:"sessions,omitempty"`
	Version  string            `json:"version,omitempty"`
	PID      int               `json:"pid,omitempty"`
	Started  string            `json:"started_at,omitempty"`
}

type terminalSettings struct {
	enabled    bool
	maxPerUser int
	scrollback int
	idleKill   time.Duration

	imagesEnabled  bool
	imageRetention time.Duration // How long pasted images stay on the host
}

// validateTerminalsConfig checks the optional terminals block (used by --validate and hot reload).
func validateTerminalsConfig(cfg *TerminalsConfig) error {
	if cfg == nil {
		return nil
	}
	if cfg.MaxSessionsPerUser < 0 || cfg.IdleKillHours < 0 {
		return errors.New("terminals: max_sessions_per_user and idle_kill_hours must not be negative")
	}
	if cfg.MaxSessionsPerUser > terminalMaxSessionsPerUserLimit {
		return fmt.Errorf("terminals: max_sessions_per_user must not be above %d", terminalMaxSessionsPerUserLimit)
	}
	if cfg.ImageRetentionDays < 0 || cfg.ImageRetentionDays > terminalImageRetentionMaxDays {
		return fmt.Errorf("terminals: image_retention_days must be from 1 to %d", terminalImageRetentionMaxDays)
	}
	if strings.TrimSpace(cfg.ScrollbackBytes) != "" {
		n, err := parseSize(cfg.ScrollbackBytes)
		if err != nil || n <= 0 {
			return fmt.Errorf("terminals: invalid scrollback_bytes %q (use e.g. \"2MB\")", cfg.ScrollbackBytes)
		}
	}
	return nil
}

func currentTerminalSettings() terminalSettings {
	s := terminalSettings{enabled: true, maxPerUser: terminalDefaultMaxSessionsPerUser, scrollback: 2 * 1024 * 1024, imagesEnabled: true, imageRetention: terminalImageRetentionDefault}
	cfg := cache.GetConfig()
	if cfg == nil || cfg.Terminals == nil {
		return s
	}
	t := cfg.Terminals
	if t.Enabled != nil {
		s.enabled = *t.Enabled
	}
	if t.MaxSessionsPerUser > 0 {
		s.maxPerUser = t.MaxSessionsPerUser
	}
	if strings.TrimSpace(t.ScrollbackBytes) != "" {
		if n, err := parseSize(t.ScrollbackBytes); err == nil && n > 0 {
			s.scrollback = int(n)
		}
	}
	if t.IdleKillHours > 0 {
		s.idleKill = time.Duration(t.IdleKillHours) * time.Hour
	}
	if t.ImagesEnabled != nil {
		s.imagesEnabled = *t.ImagesEnabled
	}
	if t.ImageRetentionDays > 0 {
		s.imageRetention = time.Duration(t.ImageRetentionDays) * 24 * time.Hour
	}
	return s
}

// TerminalsAPI serves /api/terminals and talks to cg_sessiond.
type TerminalsAPI struct {
	auth       *AuthManager
	config     *ConfigAPI // needed to write shared Agent Sessions to config.json (nil = not available)
	socketPath string
	orderPath  string     // per-user Sessions-list order, see handleSaveOrder
	orderMu    sync.Mutex // guards the order file
	agentPath  string     // per-user personal Agent Sessions (terminals_agent_sessions.go)
	agentMu    sync.Mutex // guards the agent sessions file
	metaPath   string     // session id -> preset metadata
	metaMu     sync.Mutex // guards the meta file
	startMu    sync.Mutex
	upgrader   websocket.Upgrader
}

func NewTerminalsAPI(auth *AuthManager) *TerminalsAPI {
	return &TerminalsAPI{
		auth:       auth,
		socketPath: terminalSocketPath,
		orderPath:  terminalOrderPath,
		agentPath:  terminalAgentSessionsPath,
		metaPath:   terminalMetaPath,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  32 * 1024,
			WriteBufferSize: 32 * 1024,
			Subprotocols:    []string{terminalWSSubprotocol},
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
}

// ---------------------------------------------------------------------------
// Daemon client
// ---------------------------------------------------------------------------

func (t *TerminalsAPI) dial() (net.Conn, error) {
	return net.DialTimeout("unix", t.socketPath, 2*time.Second)
}

// dialEnsured connects to the daemon, starting it first if necessary.
func (t *TerminalsAPI) dialEnsured() (net.Conn, error) {
	if conn, err := t.dial(); err == nil {
		return conn, nil
	}
	if !terminalDaemonAutoStart {
		return nil, errors.New("terminal session daemon (cg_sessiond) is not running")
	}
	t.startMu.Lock()
	defer t.startMu.Unlock()
	if conn, err := t.dial(); err == nil { // started by a concurrent request
		return conn, nil
	}
	if err := t.startDaemon(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := t.dial(); err == nil {
			return conn, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, errors.New("terminal session daemon did not start in time; see " + terminalDaemonLog)
}

func (t *TerminalsAPI) startDaemon() error {
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), terminalDaemonBinary))
	}
	if abs, err := filepath.Abs(terminalDaemonBinary); err == nil {
		candidates = append(candidates, abs)
	}
	binary := ""
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			binary = candidate
			break
		}
	}
	if binary == "" {
		return errors.New("cg_sessiond binary not found next to cguard; build both into the same folder (./compile_and_run.sh does this)")
	}
	_ = os.MkdirAll(filepath.Dir(t.socketPath), 0700)
	_ = os.MkdirAll(filepath.Dir(terminalDaemonLog), privateDirMode)
	logFile, err := os.OpenFile(terminalDaemonLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, privateFileMode)
	if err != nil {
		return fmt.Errorf("failed to open %s: %v", terminalDaemonLog, err)
	}
	defer logFile.Close()

	cmd := exec.Command(binary, "--socket", t.socketPath)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	// New session: the daemon is not in our process group, so Ctrl+C, a restart of
	// cguard or `pkill -x cguard` leave it (and every open terminal) alive.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start cg_sessiond: %v", err)
	}
	go func() { _ = cmd.Wait() }()
	log.Printf("Started terminal session daemon %s (pid %d)", binary, cmd.Process.Pid)
	return nil
}

// sendSessiondRequest writes one request line and reads the JSON response line.
// For "attach" the returned reader continues with the framed stream.
func sendSessiondRequest(conn net.Conn, req sessiondRequest) (*sessiondResponse, *bufio.Reader, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(req); err != nil {
		return nil, nil, err
	}
	// Creating a session may spend up to 30s dialing the remote host.
	_ = conn.SetDeadline(time.Now().Add(45 * time.Second))
	if _, err := conn.Write(buf.Bytes()); err != nil {
		return nil, nil, fmt.Errorf("daemon write failed: %v", err)
	}
	reader := bufio.NewReaderSize(conn, 64*1024)
	line, err := reader.ReadBytes('\n')
	_ = conn.SetDeadline(time.Time{})
	if err != nil {
		return nil, nil, fmt.Errorf("daemon read failed: %v", err)
	}
	var resp sessiondResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, nil, fmt.Errorf("invalid daemon response: %v", err)
	}
	if !resp.OK {
		return &resp, reader, errors.New(resp.Error)
	}
	return &resp, reader, nil
}

func (t *TerminalsAPI) call(req sessiondRequest) (*sessiondResponse, error) {
	conn, err := t.dialEnsured()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	resp, _, err := sendSessiondRequest(conn, req)
	return resp, err
}

func (t *TerminalsAPI) listSessions() ([]TerminalSession, *sessiondResponse, error) {
	resp, err := t.call(sessiondRequest{Op: "list"})
	if err != nil {
		return nil, nil, err
	}
	return resp.Sessions, resp, nil
}

func writeSessiondFrame(w io.Writer, kind byte, payload []byte) error {
	for {
		chunk := payload
		if len(chunk) > sessiondMaxFrame {
			chunk = payload[:sessiondMaxFrame]
		}
		header := make([]byte, 5)
		header[0] = kind
		binary.BigEndian.PutUint32(header[1:], uint32(len(chunk)))
		if _, err := w.Write(header); err != nil {
			return err
		}
		if len(chunk) > 0 {
			if _, err := w.Write(chunk); err != nil {
				return err
			}
		}
		payload = payload[len(chunk):]
		if len(payload) == 0 {
			return nil
		}
	}
}

func readSessiondFrame(r *bufio.Reader) (byte, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > sessiondMaxFrame {
		return 0, nil, fmt.Errorf("daemon frame too large: %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

// ---------------------------------------------------------------------------
// Authorization helpers
// ---------------------------------------------------------------------------

type terminalHost struct {
	HostID         string `json:"host_id"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	BastionHostRef string `json:"bastion_host_ref,omitempty"`
}

// compareVersions compares two dotted numeric versions ("1.2.0"); missing parts count as 0.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var na, nb int
		if i < len(pa) {
			na, _ = strconv.Atoi(strings.TrimSpace(pa[i]))
		}
		if i < len(pb) {
			nb, _ = strconv.Atoi(strings.TrimSpace(pb[i]))
		}
		if na != nb {
			if na < nb {
				return -1
			}
			return 1
		}
	}
	return 0
}

func isSSHHost(host Host) bool {
	return host.ConnectionType == "" || strings.EqualFold(host.ConnectionType, "ssh")
}

// allowedTerminalHosts lists the SSH hosts the caller may open terminals on.
func (t *TerminalsAPI) allowedTerminalHosts(claims *Claims) []terminalHost {
	result := make([]terminalHost, 0)
	cfg := cache.GetConfig()
	if cfg == nil {
		return result
	}
	for id, host := range cfg.Hosts {
		if !isSSHHost(host) {
			continue
		}
		if claims.Role != RoleAdmin && !t.auth.userHasTerminalHost(claims.Username, id) {
			continue
		}
		normalizeHost(&host)
		result = append(result, terminalHost{HostID: id, Host: host.Host, Port: host.Port, Username: host.Username, BastionHostRef: host.BastionHostRef})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].HostID < result[j].HostID })
	return result
}

type terminalHTTPError struct {
	status  int
	message string
}

func (e *terminalHTTPError) Error() string { return e.message }

// resolveTerminalHost checks that hostRef exists, is SSH, and is permitted for the caller.
func (t *TerminalsAPI) resolveTerminalHost(claims *Claims, hostRef string) (Host, *terminalHTTPError) {
	cfg := cache.GetConfig()
	if cfg == nil {
		return Host{}, &terminalHTTPError{500, "Configuration unavailable"}
	}
	host, ok := cfg.Hosts[hostRef]
	if !ok {
		return Host{}, &terminalHTTPError{404, "Host not found"}
	}
	if !isSSHHost(host) {
		return Host{}, &terminalHTTPError{400, "Persistent terminals require an SSH host; WinRM hosts are not supported"}
	}
	if claims.Role != RoleAdmin && !t.auth.userHasTerminalHost(claims.Username, hostRef) {
		return Host{}, &terminalHTTPError{403, "Terminal access to this host is not granted"}
	}
	normalizeHost(&host)
	return host, nil
}

func (t *TerminalsAPI) canAccessSession(claims *Claims, session TerminalSession) bool {
	if claims.Role == RoleAdmin {
		return true
	}
	return session.Owner == claims.Username && t.auth.userHasTerminalHost(claims.Username, session.HostRef)
}

func (t *TerminalsAPI) findSession(id string) (*TerminalSession, error) {
	sessions, _, err := t.listSessions()
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		if sessions[i].ID == id {
			return &sessions[i], nil
		}
	}
	return nil, nil
}

func sanitizeTerminalName(name string) string {
	name = strings.TrimSpace(name)
	runes := make([]rune, 0, len(name))
	for _, r := range name {
		if r < 32 || r == 127 {
			continue
		}
		runes = append(runes, r)
		if len(runes) >= terminalMaxNameRunes {
			break
		}
	}
	return string(runes)
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

// Handle routes everything under /api/terminals. The WebSocket attach endpoint
// authenticates on its own because browsers cannot send an Authorization header
// on WebSocket upgrades; everything else goes through the regular JWT middleware.
func (t *TerminalsAPI) Handle(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/terminals"), "/")
	parts := []string{}
	if path != "" {
		parts = strings.Split(path, "/")
	}
	if len(parts) == 2 && parts[1] == "ws" {
		t.handleWebSocket(w, r, parts[0])
		return
	}
	t.auth.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := ClaimsFromRequest(r)
		settings := currentTerminalSettings()
		if !settings.enabled {
			sendJSONError(w, http.StatusForbidden, "Persistent terminals are disabled in configuration (terminals.enabled)")
			return
		}
		switch {
		case len(parts) == 0 && r.Method == http.MethodGet:
			t.handleList(w, r, claims, settings)
		case len(parts) == 0 && r.Method == http.MethodPost:
			t.handleCreate(w, r, claims, settings)
		case len(parts) == 1 && parts[0] == "order" && r.Method == http.MethodPut:
			t.handleSaveOrder(w, r, claims)
		case len(parts) == 1 && parts[0] == "agent-sessions" && r.Method == http.MethodGet:
			t.handleAgentSessionsList(w, r, claims)
		case len(parts) == 1 && parts[0] == "agent-sessions" && r.Method == http.MethodPost:
			t.handleAgentSessionCreate(w, r, claims)
		case len(parts) == 1 && parts[0] == "agent-sessions":
			sendJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		case len(parts) == 2 && parts[0] == "agent-sessions" && (r.Method == http.MethodPut || r.Method == http.MethodPatch):
			t.handleAgentSessionUpdate(w, r, claims, parts[1])
		case len(parts) == 2 && parts[0] == "agent-sessions" && r.Method == http.MethodDelete:
			t.handleAgentSessionDelete(w, r, claims, parts[1])
		case len(parts) == 2 && parts[0] == "agent-sessions":
			sendJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		case len(parts) == 2 && parts[1] == "images" && parts[0] != "agent-sessions" && r.Method == http.MethodPost:
			t.handleImageUpload(w, r, claims, settings, parts[0])
		case len(parts) == 2 && parts[1] == "files" && parts[0] != "agent-sessions" && r.Method == http.MethodPost:
			t.handleFileUpload(w, r, claims, settings, parts[0])
		case len(parts) == 1 && parts[0] == "hosts" && r.Method == http.MethodGet:
			sendJSONSuccess(w, http.StatusOK, t.allowedTerminalHosts(claims), "Terminal hosts retrieved")
		case len(parts) == 1 && r.Method == http.MethodGet:
			t.handleGet(w, r, claims, parts[0])
		case len(parts) == 1 && r.Method == http.MethodDelete:
			t.handleKill(w, r, claims, parts[0])
		case len(parts) == 1 && (r.Method == http.MethodPatch || r.Method == http.MethodPut):
			t.handleRename(w, r, claims, parts[0])
		case len(parts) == 0 || len(parts) == 1:
			sendJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		default:
			sendJSONError(w, http.StatusNotFound, "Endpoint not found")
		}
	})(w, r)
}

func (t *TerminalsAPI) handleList(w http.ResponseWriter, r *http.Request, claims *Claims, settings terminalSettings) {
	sessions, resp, err := t.listSessions()
	if err != nil {
		sendJSONError(w, http.StatusServiceUnavailable, "Terminal daemon unavailable: "+err.Error())
		return
	}
	// Icons of sessions started from an Agent Session preset; the complete daemon list
	// also lets stale metadata be pruned.
	t.attachTerminalMeta(sessions, true)
	visible := visibleTerminalSessions(sessions, claims)
	sendJSONSuccess(w, http.StatusOK, map[string]interface{}{
		"sessions": visible,
		"order":    filterTerminalOrder(t.loadTerminalOrders()[claims.Username], visible),
		"daemon":   map[string]interface{}{"version": resp.Version, "pid": resp.PID, "started_at": resp.Started},
		"limits":   map[string]interface{}{"max_sessions_per_user": settings.maxPerUser, "idle_kill_hours": int(settings.idleKill.Hours())},
	}, "Terminal sessions retrieved")
}

// visibleTerminalSessions returns the sessions the caller may list: admin all, others their own.
func visibleTerminalSessions(sessions []TerminalSession, claims *Claims) []TerminalSession {
	visible := make([]TerminalSession, 0, len(sessions))
	for _, session := range sessions {
		if claims.Role == RoleAdmin || session.Owner == claims.Username {
			visible = append(visible, session)
		}
	}
	return visible
}

// filterTerminalOrder keeps the ids of sessions in the list, without duplicates, so ended or
// foreign sessions never appear in (or get stored as) a user's tab order.
func filterTerminalOrder(ids []string, sessions []TerminalSession) []string {
	known := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		known[session.ID] = true
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if known[id] && len(out) < terminalMaxOrderIDs {
			out = append(out, id)
			known[id] = false
		}
	}
	return out
}

// loadTerminalOrders reads the per-user tab orders; a missing or unreadable file means none.
func (t *TerminalsAPI) loadTerminalOrders() map[string][]string {
	t.orderMu.Lock()
	defer t.orderMu.Unlock()
	return t.readTerminalOrdersLocked()
}

func (t *TerminalsAPI) readTerminalOrdersLocked() map[string][]string {
	orders := map[string][]string{}
	data, err := os.ReadFile(t.orderPath)
	if err != nil {
		return orders
	}
	if err := json.Unmarshal(data, &orders); err != nil {
		log.Printf("terminals: ignoring unreadable %s: %v", t.orderPath, err)
		return map[string][]string{}
	}
	return orders
}

// handleSaveOrder stores the caller's order of the Sessions list (PUT /api/terminals/order,
// body {"ids": [...]}). Only ids of sessions the caller can currently see are kept.
func (t *TerminalsAPI) handleSaveOrder(w http.ResponseWriter, r *http.Request, claims *Claims) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
		sendJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	sessions, _, err := t.listSessions()
	if err != nil {
		sendJSONError(w, http.StatusServiceUnavailable, "Terminal daemon unavailable: "+err.Error())
		return
	}
	ids := filterTerminalOrder(req.IDs, visibleTerminalSessions(sessions, claims))

	t.orderMu.Lock()
	defer t.orderMu.Unlock()
	orders := t.readTerminalOrdersLocked()
	if len(ids) == 0 {
		delete(orders, claims.Username)
	} else {
		orders[claims.Username] = ids
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "\t")
	if err := encoder.Encode(orders); err == nil {
		_ = os.MkdirAll(filepath.Dir(t.orderPath), 0700)
		tmp := t.orderPath + ".tmp"
		if err = os.WriteFile(tmp, buf.Bytes(), 0600); err == nil {
			err = os.Rename(tmp, t.orderPath)
		}
		if err != nil {
			sendJSONError(w, http.StatusInternalServerError, "Failed to save terminal order: "+err.Error())
			return
		}
	}
	sendJSONSuccess(w, http.StatusOK, map[string]interface{}{"order": ids}, "Terminal order saved")
}

func (t *TerminalsAPI) handleGet(w http.ResponseWriter, r *http.Request, claims *Claims, id string) {
	session, err := t.findSession(id)
	if err != nil {
		sendJSONError(w, http.StatusServiceUnavailable, "Terminal daemon unavailable: "+err.Error())
		return
	}
	if session == nil || !t.canAccessSession(claims, *session) {
		sendJSONError(w, http.StatusNotFound, "Terminal session not found")
		return
	}
	one := []TerminalSession{*session}
	t.attachTerminalMeta(one, false)
	sendJSONSuccess(w, http.StatusOK, one[0], "Terminal session retrieved")
}

func (t *TerminalsAPI) handleCreate(w http.ResponseWriter, r *http.Request, claims *Claims, settings terminalSettings) {
	var req struct {
		HostRef string               `json:"host_ref"`
		Name    string               `json:"name"`
		Cols    int                  `json:"cols"`
		Rows    int                  `json:"rows"`
		Meta    *TerminalSessionMeta `json:"meta"` // set when launched from an Agent Session preset
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req) != nil {
		sendJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	req.HostRef = strings.TrimSpace(req.HostRef)
	if req.HostRef == "" {
		sendJSONError(w, http.StatusBadRequest, "host_ref is required")
		return
	}
	if err := validateTerminalSessionMeta(req.Meta); err != nil {
		sendJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	host, herr := t.resolveTerminalHost(claims, req.HostRef)
	if herr != nil {
		sendJSONError(w, herr.status, herr.message)
		return
	}
	if host.Password == "" && host.PemFile == "" {
		sendJSONError(w, http.StatusBadRequest, "Host has no authentication method (neither password nor PEM key configured)")
		return
	}
	if host.BastionHostRef != "" && host.Bastion == nil {
		sendJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Host is configured to connect via bastion host %q but the bastion was not resolved (configuration not loaded)", host.BastionHostRef))
		return
	}

	// Enforce the per-account session limit against the daemon's live list.
	sessions, _, err := t.listSessions()
	if err != nil {
		sendJSONError(w, http.StatusServiceUnavailable, "Terminal daemon unavailable: "+err.Error())
		return
	}
	running := 0
	for _, session := range sessions {
		if session.Owner == claims.Username && session.Status == "running" {
			running++
		}
	}
	if running >= settings.maxPerUser {
		sendJSONError(w, http.StatusConflict, fmt.Sprintf("Session limit reached (%d running). Close or kill a session first.", running))
		return
	}

	create := sessiondRequest{
		Op: "create", Owner: claims.Username, Name: sanitizeTerminalName(req.Name), HostRef: req.HostRef,
		Host: host.Host, Port: host.Port, Username: host.Username, Password: host.Password, PEMPassword: host.PemPwd,
		Term: "xterm-256color", Cols: req.Cols, Rows: req.Rows,
		ScrollbackBytes: settings.scrollback, IdleKillSeconds: int(settings.idleKill.Seconds()),
	}
	if host.PemFile != "" {
		pemBytes, err := ioutil.ReadFile(host.PemFile)
		if err != nil {
			sendJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to read PEM file %s: %v", host.PemFile, err))
			return
		}
		create.PEM = string(pemBytes)
	}
	if host.Bastion != nil {
		// The daemon must understand the bastion field; an older build would ignore
		// it and connect to the target directly.
		ping, err := t.call(sessiondRequest{Op: "ping"})
		if err != nil {
			sendJSONError(w, http.StatusServiceUnavailable, "Terminal daemon unavailable: "+err.Error())
			return
		}
		if compareVersions(ping.Version, sessiondMinBastionVersion) < 0 {
			sendJSONError(w, http.StatusNotImplemented, fmt.Sprintf("The running terminal daemon (cg_sessiond %s) cannot open terminals through a bastion host. Restart it by stopping this installation's cg_sessiond process, for example kill -TERM <pid> (this ends all open terminals), to load the current build.", ping.Version))
			return
		}
		bastion := *host.Bastion
		normalizeHost(&bastion)
		create.Bastion = &sessiondBastion{
			HostRef: host.BastionHostRef, Host: bastion.Host, Port: bastion.Port,
			Username: bastion.Username, Password: bastion.Password, PEMPassword: bastion.PemPwd,
		}
		if bastion.PemFile != "" {
			pemBytes, err := ioutil.ReadFile(bastion.PemFile)
			if err != nil {
				sendJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to read bastion PEM file %s: %v", bastion.PemFile, err))
				return
			}
			create.Bastion.PEM = string(pemBytes)
		}
	}

	resp, err := t.call(create)
	if err != nil {
		writeSecurityAudit(r, claims.Username, claims.Role, "terminal_create", req.HostRef, "failed", map[string]interface{}{"host_ref": req.HostRef})
		sendJSONError(w, http.StatusBadGateway, "Failed to open terminal: "+err.Error())
		return
	}
	auditDetails := map[string]interface{}{"host_ref": req.HostRef, "session_id": resp.Session.ID}
	if req.Meta != nil {
		t.storeTerminalMeta(resp.Session.ID, *req.Meta)
		resp.Session.Meta = req.Meta
		auditDetails["agent_session"] = req.Meta.Name
		auditDetails["preset_id"] = req.Meta.PresetID
	}
	writeSecurityAudit(r, claims.Username, claims.Role, "terminal_create", req.HostRef, "success", auditDetails)
	sendJSONSuccess(w, http.StatusCreated, resp.Session, "Terminal session created")
}

func (t *TerminalsAPI) handleKill(w http.ResponseWriter, r *http.Request, claims *Claims, id string) {
	session, err := t.findSession(id)
	if err != nil {
		sendJSONError(w, http.StatusServiceUnavailable, "Terminal daemon unavailable: "+err.Error())
		return
	}
	if session == nil || !t.canAccessSession(claims, *session) {
		sendJSONError(w, http.StatusNotFound, "Terminal session not found")
		return
	}
	if _, err := t.call(sessiondRequest{Op: "kill", ID: id}); err != nil {
		sendJSONError(w, http.StatusBadGateway, "Failed to kill session: "+err.Error())
		return
	}
	writeSecurityAudit(r, claims.Username, claims.Role, "terminal_kill", session.HostRef, "success", map[string]interface{}{"host_ref": session.HostRef, "session_id": id, "owner": session.Owner})
	sendJSONSuccess(w, http.StatusOK, nil, "Terminal session killed")
}

// handleRename changes a session's display name (PUT or PATCH /api/terminals/{id} {"name": ...}).
// An empty name restores the host ID. Needs cg_sessiond 1.1.0 or later.
func (t *TerminalsAPI) handleRename(w http.ResponseWriter, r *http.Request, claims *Claims, id string) {
	var req struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		sendJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	session, err := t.findSession(id)
	if err != nil {
		sendJSONError(w, http.StatusServiceUnavailable, "Terminal daemon unavailable: "+err.Error())
		return
	}
	if session == nil || !t.canAccessSession(claims, *session) {
		sendJSONError(w, http.StatusNotFound, "Terminal session not found")
		return
	}
	name := sanitizeTerminalName(req.Name)
	resp, err := t.call(sessiondRequest{Op: "rename", ID: id, Name: name})
	if err != nil {
		if err.Error() == "unknown op" {
			sendJSONError(w, http.StatusNotImplemented, "The running terminal daemon (cg_sessiond) is an older build that cannot rename sessions. Restart it by stopping this installation's cg_sessiond process, for example kill -TERM <pid> (this ends all open terminals), to enable renaming.")
			return
		}
		sendJSONError(w, http.StatusBadGateway, "Failed to rename session: "+err.Error())
		return
	}
	writeSecurityAudit(r, claims.Username, claims.Role, "terminal_rename", session.HostRef, "success", map[string]interface{}{"host_ref": session.HostRef, "session_id": id, "owner": session.Owner, "old_name": session.Name, "new_name": resp.Session.Name})
	sendJSONSuccess(w, http.StatusOK, resp.Session, "Terminal session renamed")
}

// ---------------------------------------------------------------------------
// WebSocket bridge: browser <-> cguard <-> cg_sessiond
// ---------------------------------------------------------------------------

// handleWebSocket attaches the browser to a session. Protocol towards the browser:
//
//	browser -> server: binary frames = keystrokes; text frames = JSON control {"type":"resize","cols":..,"rows":..}
//	server -> browser: binary frames = terminal output; text frames = {"type":"replay_end"|"exit"|"takeover"|"error", ...}
func (t *TerminalsAPI) handleWebSocket(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		sendJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	// The JWT arrives as a second entry in the WebSocket subprotocol list
	// (["cg-jwt", <token>]) or, as a fallback, in the token query parameter.
	token := ""
	for _, proto := range websocket.Subprotocols(r) {
		if proto != terminalWSSubprotocol {
			token = proto
		}
	}
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		sendJSONError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	authReq := r.Clone(r.Context())
	authReq.Header = r.Header.Clone()
	authReq.Header.Set("Authorization", "Bearer "+token)
	claims, err := t.auth.authenticateRequest(authReq)
	if err != nil {
		sendJSONError(w, http.StatusUnauthorized, "Invalid or expired authentication")
		return
	}
	if !currentTerminalSettings().enabled {
		sendJSONError(w, http.StatusForbidden, "Persistent terminals are disabled in configuration")
		return
	}
	session, err := t.findSession(id)
	if err != nil {
		sendJSONError(w, http.StatusServiceUnavailable, "Terminal daemon unavailable: "+err.Error())
		return
	}
	if session == nil || !t.canAccessSession(claims, *session) {
		sendJSONError(w, http.StatusNotFound, "Terminal session not found")
		return
	}

	cols, rows := 0, 0
	fmt.Sscanf(r.URL.Query().Get("cols"), "%d", &cols)
	fmt.Sscanf(r.URL.Query().Get("rows"), "%d", &rows)

	// Attach to the daemon before upgrading so failures are still plain HTTP errors.
	daemonConn, err := t.dialEnsured()
	if err != nil {
		sendJSONError(w, http.StatusServiceUnavailable, "Terminal daemon unavailable: "+err.Error())
		return
	}
	_, reader, err := sendSessiondRequest(daemonConn, sessiondRequest{Op: "attach", ID: id, Cols: cols, Rows: rows})
	if err != nil {
		daemonConn.Close()
		sendJSONError(w, http.StatusConflict, "Cannot attach: "+err.Error())
		return
	}

	ws, err := t.upgrader.Upgrade(w, r, nil)
	if err != nil {
		_ = writeSessiondFrame(daemonConn, frameDetach, nil)
		daemonConn.Close()
		return // Upgrade already wrote the HTTP error
	}
	// The HTTP server's WriteTimeout must not apply to the long-lived hijacked connection.
	_ = ws.NetConn().SetDeadline(time.Time{})
	attachedAt := time.Now()
	writeSecurityAudit(r, claims.Username, claims.Role, "terminal_attach", session.HostRef, "success", map[string]interface{}{"host_ref": session.HostRef, "session_id": id})

	done := make(chan struct{})
	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			close(done)
			daemonConn.Close()
			ws.Close()
		})
	}
	var wsWriteMu sync.Mutex
	writeWS := func(messageType int, data []byte) error {
		wsWriteMu.Lock()
		defer wsWriteMu.Unlock()
		_ = ws.SetWriteDeadline(time.Now().Add(wsWriteWait))
		return ws.WriteMessage(messageType, data)
	}

	// daemon -> browser
	go func() {
		defer closeAll()
		for {
			kind, payload, err := readSessiondFrame(reader)
			if err != nil {
				return
			}
			switch kind {
			case frameData:
				if err := writeWS(websocket.BinaryMessage, payload); err != nil {
					return
				}
			case frameExit:
				msg := map[string]interface{}{"type": "exit"}
				var details map[string]interface{}
				if json.Unmarshal(payload, &details) == nil {
					for key, value := range details {
						msg[key] = value
					}
				}
				data, _ := json.Marshal(msg)
				_ = writeWS(websocket.TextMessage, data)
				return
			case frameTakeover:
				_ = writeWS(websocket.TextMessage, []byte(`{"type":"takeover"}`))
				return
			case frameReplayEnd:
				if err := writeWS(websocket.TextMessage, []byte(`{"type":"replay_end"}`)); err != nil {
					return
				}
			}
		}
	}()

	// keepalive pings; browsers answer automatically
	go func() {
		ticker := time.NewTicker(wsPingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				wsWriteMu.Lock()
				err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait))
				wsWriteMu.Unlock()
				if err != nil {
					closeAll()
					return
				}
			}
		}
	}()

	// browser -> daemon (this goroutine is the only writer to daemonConn)
	ws.SetReadLimit(sessiondMaxFrame)
	_ = ws.SetReadDeadline(time.Now().Add(wsPongWait))
	ws.SetPongHandler(func(string) error { return ws.SetReadDeadline(time.Now().Add(wsPongWait)) })
	for {
		messageType, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		_ = ws.SetReadDeadline(time.Now().Add(wsPongWait))
		switch messageType {
		case websocket.BinaryMessage:
			if err := writeSessiondFrame(daemonConn, frameData, data); err != nil {
				closeAll()
			}
		case websocket.TextMessage:
			var control struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if json.Unmarshal(data, &control) == nil && control.Type == "resize" && control.Cols > 0 && control.Rows > 0 {
				payload, _ := json.Marshal(map[string]int{"cols": control.Cols, "rows": control.Rows})
				if err := writeSessiondFrame(daemonConn, frameResize, payload); err != nil {
					closeAll()
				}
			}
		}
		// closeAll() closes ws, which makes the next ReadMessage fail and ends the loop.
	}
	_ = writeSessiondFrame(daemonConn, frameDetach, nil)
	closeAll()
	writeSecurityAudit(r, claims.Username, claims.Role, "terminal_detach", session.HostRef, "success", map[string]interface{}{"host_ref": session.HostRef, "session_id": id, "attached_seconds": int(time.Since(attachedAt).Seconds())})
}
