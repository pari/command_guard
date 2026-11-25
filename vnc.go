package main

/*
Remote Desktops (VNC): configuration, validation, admin CRUD and access mapping.

  - vnc.go          config types, validation, /api/config/vnc-connections (admin)
  - vnc_rfb.go      the RFB (VNC protocol) side: dialing, server handshake + auth,
                    the fake "no auth" handshake towards noVNC, client message filter
  - vnc_sessions.go /api/vnc runtime: tickets, WebSocket bridge, live registry,
                    policy enforcement, idle disconnect, thumbnails, audit

VNC passwords never reach the browser: cguard authenticates to the VNC server
itself and then presents the authenticated stream to noVNC as security type None.
See docs/vnc.md.
*/

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// VNCSettings is the optional top-level "vnc" block in config.json.
type VNCSettings struct {
	Enabled               *bool `json:"enabled,omitempty"`                 // Defaults to true when omitted
	IdleDisconnectMinutes *int  `json:"idle_disconnect_minutes,omitempty"` // Default 60; 0 = never
	MaxSessionsPerUser    int   `json:"max_sessions_per_user,omitempty"`   // Open viewers per account (default 5)
}

// VNCConnection is one entry of the top-level "vnc_connections" array.
type VNCConnection struct {
	VNCID         string `json:"vnc_id"`
	Name          string `json:"name,omitempty"`
	Description   string `json:"description,omitempty"`
	TunnelHostRef string `json:"tunnel_host_ref,omitempty"` // Optional SSH Host to tunnel through (its bastion is followed)
	Host          string `json:"host,omitempty"`            // VNC server address; with a tunnel, as seen from the tunnel host (default 127.0.0.1)
	Port          int    `json:"port,omitempty"`            // Default 5900
	AuthType      string `json:"auth_type,omitempty"`       // auto (default) | none | vnc | ard | vencrypt
	Username      string `json:"username,omitempty"`        // ARD (macOS) and VeNCrypt Plain only
	Password      string `json:"password,omitempty"`
	TLSSkipVerify bool   `json:"tls_skip_verify,omitempty"` // VeNCrypt X509: accept any server certificate
	// AllowClipboard defaults to true. When false the proxy drops clipboard
	// messages from the browser and the viewer ignores remote clipboard updates.
	AllowClipboard *bool `json:"allow_clipboard,omitempty"`
}

// VNCGrant is one entry of a user's "vnc_access" list.
type VNCGrant struct {
	VNCID string `json:"vnc_id"`
	Level string `json:"level"` // control | view
}

const (
	vncLevelControl = "control"
	vncLevelView    = "view"

	vncDefaultPort               = 5900
	vncDefaultIdleMinutes        = 60
	vncDefaultMaxSessionsPerUser = 5
	vncMaxSessionsPerUserLimit   = 50
	vncMaxIdleMinutes            = 7 * 24 * 60
	vncMaxIDLength               = 64
	vncMaxNameRunes              = 80
	vncMaxDescriptionRunes       = 500
)

var vncAuthTypes = map[string]bool{"auto": true, "none": true, "vnc": true, "ard": true, "vencrypt": true}

func (c VNCConnection) displayName() string {
	if strings.TrimSpace(c.Name) != "" {
		return strings.TrimSpace(c.Name)
	}
	return c.VNCID
}

func (c VNCConnection) authType() string {
	t := strings.ToLower(strings.TrimSpace(c.AuthType))
	if t == "" {
		return "auto"
	}
	return t
}

func (c VNCConnection) port() int {
	if c.Port == 0 {
		return vncDefaultPort
	}
	return c.Port
}

func (c VNCConnection) targetHost() string {
	h := strings.TrimSpace(c.Host)
	if h == "" && c.TunnelHostRef != "" {
		return "127.0.0.1"
	}
	return h
}

func (c VNCConnection) clipboardAllowed() bool { return c.AllowClipboard == nil || *c.AllowClipboard }

// fingerprint changes whenever something that affects where or how the proxy
// connects changes; open viewers of a connection whose fingerprint changed are
// disconnected by the enforcement loop.
func (c VNCConnection) fingerprint() string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00%s\x00%s\x00%s\x00%t", c.TunnelHostRef, c.targetHost(), c.port(), c.authType(), c.Username, c.Password, c.TLSSkipVerify)
	return hex.EncodeToString(h.Sum(nil))
}

func normalizeVNCConnection(c *VNCConnection) {
	c.VNCID = strings.TrimSpace(c.VNCID)
	c.Name = strings.TrimSpace(c.Name)
	c.Description = strings.TrimSpace(c.Description)
	c.TunnelHostRef = strings.TrimSpace(c.TunnelHostRef)
	c.Host = strings.TrimSpace(c.Host)
	c.AuthType = strings.ToLower(strings.TrimSpace(c.AuthType))
	if c.AuthType == "auto" {
		c.AuthType = ""
	}
	c.Username = strings.TrimSpace(c.Username)
}

func normalizeVNCLevel(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "control", "full":
		return vncLevelControl
	case "view", "view-only", "view_only", "viewonly", "":
		return vncLevelView // least privilege when unspecified
	}
	return level
}

// normalizeVNCGrants lower-cases levels, drops empty IDs, keeps one grant per
// connection (the higher level wins) and sorts by ID.
func normalizeVNCGrants(grants []VNCGrant) []VNCGrant {
	byID := map[string]string{}
	for _, g := range grants {
		id := strings.TrimSpace(g.VNCID)
		if id == "" {
			continue
		}
		level := normalizeVNCLevel(g.Level)
		if byID[id] == vncLevelControl {
			continue
		}
		byID[id] = level
	}
	if len(byID) == 0 {
		return nil
	}
	result := make([]VNCGrant, 0, len(byID))
	for id, level := range byID {
		result = append(result, VNCGrant{VNCID: id, Level: level})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].VNCID < result[j].VNCID })
	return result
}

func validVNCID(id string) bool {
	if id == "" || len(id) > vncMaxIDLength {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func validateVNCSettings(s *VNCSettings) error {
	if s == nil {
		return nil
	}
	if s.IdleDisconnectMinutes != nil && (*s.IdleDisconnectMinutes < 0 || *s.IdleDisconnectMinutes > vncMaxIdleMinutes) {
		return fmt.Errorf("vnc: idle_disconnect_minutes must be from 0 (never) to %d", vncMaxIdleMinutes)
	}
	if s.MaxSessionsPerUser < 0 || s.MaxSessionsPerUser > vncMaxSessionsPerUserLimit {
		return fmt.Errorf("vnc: max_sessions_per_user must be from 1 to %d", vncMaxSessionsPerUserLimit)
	}
	return nil
}

// validateVNCConnections checks the vnc_connections array against the hosts map.
func validateVNCConnections(conns []VNCConnection, hosts map[string]Host) error {
	seen := map[string]bool{}
	for i := range conns {
		c := conns[i]
		normalizeVNCConnection(&c)
		if !validVNCID(c.VNCID) {
			return fmt.Errorf("vnc connection #%d: vnc_id %q is invalid (1-%d characters: letters, numbers, '-', '_' and '.')", i+1, c.VNCID, vncMaxIDLength)
		}
		if seen[c.VNCID] {
			return fmt.Errorf("duplicate vnc connection id %q", c.VNCID)
		}
		seen[c.VNCID] = true
		if utf8.RuneCountInString(c.Name) > vncMaxNameRunes {
			return fmt.Errorf("vnc connection %q: name is longer than %d characters", c.VNCID, vncMaxNameRunes)
		}
		if utf8.RuneCountInString(c.Description) > vncMaxDescriptionRunes {
			return fmt.Errorf("vnc connection %q: description is longer than %d characters", c.VNCID, vncMaxDescriptionRunes)
		}
		if c.TunnelHostRef != "" {
			host, ok := hosts[c.TunnelHostRef]
			if !ok {
				return fmt.Errorf("vnc connection %q references non-existent tunnel host %q", c.VNCID, c.TunnelHostRef)
			}
			if !isSSHHost(host) {
				return fmt.Errorf("vnc connection %q: tunnel host %q must be an SSH host", c.VNCID, c.TunnelHostRef)
			}
		} else if c.Host == "" {
			return fmt.Errorf("vnc connection %q: host is required when no tunnel host is set", c.VNCID)
		}
		if strings.ContainsAny(c.Host, " /\\\t\r\n") {
			return fmt.Errorf("vnc connection %q: host %q is not a valid address", c.VNCID, c.Host)
		}
		if c.Port < 0 || c.Port > 65535 {
			return fmt.Errorf("vnc connection %q: port must be from 1 to 65535", c.VNCID)
		}
		if !vncAuthTypes[c.authType()] {
			return fmt.Errorf("vnc connection %q: auth_type %q is invalid (auto, none, vnc, ard, vencrypt)", c.VNCID, c.AuthType)
		}
		switch c.authType() {
		case "vnc":
			if c.Password == "" {
				return fmt.Errorf("vnc connection %q: auth_type vnc requires a password", c.VNCID)
			}
		case "ard":
			if c.Username == "" || c.Password == "" {
				return fmt.Errorf("vnc connection %q: auth_type ard (macOS) requires a username and password", c.VNCID)
			}
		}
	}
	return nil
}

func vncConnectionIDSet(conns []VNCConnection) map[string]bool {
	result := make(map[string]bool, len(conns))
	for _, c := range conns {
		result[strings.TrimSpace(c.VNCID)] = true
	}
	return result
}

// validateVNCGrants checks one user's vnc_access entries.
func validateVNCGrants(username string, grants []VNCGrant, vncIDs map[string]bool) error {
	for _, g := range grants {
		if g.Level != vncLevelControl && g.Level != vncLevelView {
			return fmt.Errorf("user %q has invalid VNC access level %q for %q (control or view)", username, g.Level, g.VNCID)
		}
		if !vncIDs[g.VNCID] {
			return fmt.Errorf("user %q references missing VNC connection %q", username, g.VNCID)
		}
	}
	return nil
}

func findVNCConnection(cfg *Config, id string) (VNCConnection, bool) {
	if cfg == nil {
		return VNCConnection{}, false
	}
	for _, c := range cfg.VNCConnections {
		if c.VNCID == id {
			normalizeVNCConnection(&c)
			return c, true
		}
	}
	return VNCConnection{}, false
}

type vncSettings struct {
	enabled    bool
	idle       time.Duration // 0 = never
	maxPerUser int
}

func currentVNCSettings() vncSettings {
	s := vncSettings{enabled: true, idle: vncDefaultIdleMinutes * time.Minute, maxPerUser: vncDefaultMaxSessionsPerUser}
	cfg := cache.GetConfig()
	if cfg == nil || cfg.VNC == nil {
		return s
	}
	if cfg.VNC.Enabled != nil {
		s.enabled = *cfg.VNC.Enabled
	}
	if cfg.VNC.IdleDisconnectMinutes != nil {
		s.idle = time.Duration(*cfg.VNC.IdleDisconnectMinutes) * time.Minute
	}
	if cfg.VNC.MaxSessionsPerUser > 0 {
		s.maxPerUser = cfg.VNC.MaxSessionsPerUser
	}
	return s
}

// userVNCLevel returns the caller's access level for a connection ("" = none).
func (am *AuthManager) userVNCLevel(username, vncID string) string {
	am.mu.RLock()
	user, ok := am.users[canonicalUsername(username)]
	am.mu.RUnlock()
	if !ok {
		return ""
	}
	for _, g := range user.VNCAccess {
		if g.VNCID == vncID {
			return g.Level
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Raw config helpers (used inside mutateConfig callbacks)
// ---------------------------------------------------------------------------

func decodeVNCConnections(value interface{}) ([]VNCConnection, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var conns []VNCConnection
	if err := json.Unmarshal(data, &conns); err != nil {
		return nil, err
	}
	return conns, nil
}

func decodeUsers(value interface{}) ([]User, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var users []User
	if err := json.Unmarshal(data, &users); err != nil {
		return nil, err
	}
	return users, nil
}

// vncTunnelUsage returns the IDs of VNC connections that tunnel through hostID.
func vncTunnelUsage(config map[string]interface{}, hostID string) []string {
	conns, _ := decodeVNCConnections(config["vnc_connections"])
	result := []string{}
	for _, c := range conns {
		if strings.TrimSpace(c.TunnelHostRef) == hostID {
			result = append(result, c.VNCID)
		}
	}
	return result
}

// validateVNCRaw validates the vnc block, the vnc_connections array and every
// user's vnc_access of a raw config document; mutateConfig callbacks call it so a
// bad combination is rejected with 400 instead of breaking hot reload.
func validateVNCRaw(config map[string]interface{}) error {
	conns, err := decodeVNCConnections(config["vnc_connections"])
	if err != nil {
		return httpErr(http.StatusBadRequest, "Invalid vnc_connections: "+err.Error())
	}
	if rawSettings, ok := config["vnc"]; ok && rawSettings != nil {
		var settings VNCSettings
		data, _ := json.Marshal(rawSettings)
		if err := json.Unmarshal(data, &settings); err != nil {
			return httpErr(http.StatusBadRequest, "Invalid vnc settings")
		}
		if err := validateVNCSettings(&settings); err != nil {
			return httpErr(http.StatusBadRequest, err.Error())
		}
	}
	if err := validateVNCConnections(conns, decodeHostsMap(config["hosts"])); err != nil {
		return httpErr(http.StatusBadRequest, err.Error())
	}
	users, err := decodeUsers(config["users"])
	if err != nil {
		return httpErr(http.StatusInternalServerError, "Invalid users configuration")
	}
	ids := vncConnectionIDSet(conns)
	for _, u := range users {
		u = normalizeUser(u)
		if err := validateVNCGrants(u.Username, u.VNCAccess, ids); err != nil {
			return httpErr(http.StatusBadRequest, err.Error())
		}
	}
	return nil
}

func vncConnectionIndex(conns []VNCConnection, id string) int {
	for i, c := range conns {
		if strings.TrimSpace(c.VNCID) == id {
			return i
		}
	}
	return -1
}

// redactVNCConnection is the admin view of a connection: the password is replaced
// by has_password / password_length.
func redactVNCConnection(c VNCConnection) map[string]interface{} {
	normalizeVNCConnection(&c)
	return map[string]interface{}{
		"vnc_id":          c.VNCID,
		"name":            c.Name,
		"description":     c.Description,
		"tunnel_host_ref": c.TunnelHostRef,
		"host":            c.Host,
		"port":            c.port(),
		"auth_type":       c.authType(),
		"username":        c.Username,
		"has_password":    c.Password != "",
		"password_length": utf8.RuneCountInString(c.Password),
		"tls_skip_verify": c.TLSSkipVerify,
		"allow_clipboard": c.clipboardAllowed(),
	}
}

// vncAccessList returns who may use a connection, from a users list.
func vncAccessList(users []User, vncID string) []map[string]interface{} {
	result := []map[string]interface{}{}
	for _, u := range users {
		u = normalizeUser(u)
		for _, g := range u.VNCAccess {
			if g.VNCID == vncID {
				result = append(result, map[string]interface{}{"username": u.Username, "level": g.Level, "enabled": u.Enabled})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i]["username"].(string) < result[j]["username"].(string) })
	return result
}

// ---------------------------------------------------------------------------
// Admin API: /api/config/vnc-connections
// ---------------------------------------------------------------------------

type vncConnectionRequest struct {
	VNCConnection
	// ClearPassword removes a stored password (an empty password field otherwise
	// means "keep the stored one").
	ClearPassword bool `json:"clear_password,omitempty"`
}

func preserveVNCSecrets(incoming *vncConnectionRequest, existing VNCConnection) {
	if incoming.ClearPassword {
		incoming.Password = ""
		return
	}
	if incoming.Password == "" || isMaskedSecret(incoming.Password) {
		incoming.Password = existing.Password
	}
}

// isMaskedSecret recognises the bullet placeholder the UI shows for stored secrets.
func isMaskedSecret(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && strings.Trim(value, "•*") == ""
}

// HandleVNCConnections serves /api/config/vnc-connections[/{id}[/access|/test]] (admin only).
func (ca *ConfigAPI) HandleVNCConnections(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/config/vnc-connections"), "/")
	parts := []string{}
	if path != "" {
		parts = strings.Split(path, "/")
	}
	actor, role := currentActor(r)

	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			config, err := loadConfigMap(ca.configPath)
			if err != nil {
				sendConfigJSONError(w, http.StatusInternalServerError, "Failed to read configuration")
				return
			}
			conns, err := decodeVNCConnections(config["vnc_connections"])
			if err != nil {
				sendConfigJSONError(w, http.StatusInternalServerError, "Invalid vnc_connections configuration")
				return
			}
			users, _ := decodeUsers(config["users"])
			views := make([]map[string]interface{}, 0, len(conns))
			for _, c := range conns {
				view := redactVNCConnection(c)
				view["access"] = vncAccessList(users, c.VNCID)
				views = append(views, view)
			}
			sendConfigJSONSuccess(w, http.StatusOK, views, "VNC connections retrieved successfully")
		case http.MethodPost:
			var req vncConnectionRequest
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req) != nil {
				sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
				return
			}
			normalizeVNCConnection(&req.VNCConnection)
			if req.ClearPassword || isMaskedSecret(req.Password) {
				req.Password = ""
			}
			err := ca.mutateConfig(func(config map[string]interface{}) error {
				conns, err := decodeVNCConnections(config["vnc_connections"])
				if err != nil {
					return httpErr(http.StatusInternalServerError, "Invalid vnc_connections configuration")
				}
				if vncConnectionIndex(conns, req.VNCID) >= 0 {
					return httpErr(http.StatusConflict, fmt.Sprintf("VNC connection '%s' already exists", req.VNCID))
				}
				config["vnc_connections"] = append(conns, req.VNCConnection)
				return validateVNCRaw(config)
			})
			if err != nil {
				sendConfigMutationError(w, err, "Failed to save VNC connection")
				return
			}
			writeSecurityAudit(r, actor, role, "vnc_connection_create", req.VNCID, "success", vncAuditDetails(req.VNCConnection))
			sendConfigJSONSuccess(w, http.StatusCreated, redactVNCConnection(req.VNCConnection), "VNC connection created successfully")
		default:
			sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

	id := parts[0]
	if !validVNCID(id) || len(parts) > 2 {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid VNC connection ID")
		return
	}
	if len(parts) == 2 {
		switch {
		case parts[1] == "access" && r.Method == http.MethodGet:
			ca.handleVNCAccessGet(w, id)
		case parts[1] == "access" && r.Method == http.MethodPut:
			ca.handleVNCAccessPut(w, r, id)
		case parts[1] == "test" && r.Method == http.MethodPost:
			ca.handleVNCTest(w, r, id)
		case parts[1] == "access" || parts[1] == "test":
			sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		default:
			sendConfigJSONError(w, http.StatusNotFound, "Endpoint not found")
		}
		return
	}

	switch r.Method {
	case http.MethodGet:
		config, err := loadConfigMap(ca.configPath)
		if err != nil {
			sendConfigJSONError(w, http.StatusInternalServerError, "Failed to read configuration")
			return
		}
		conns, _ := decodeVNCConnections(config["vnc_connections"])
		index := vncConnectionIndex(conns, id)
		if index < 0 {
			sendConfigJSONError(w, http.StatusNotFound, "VNC connection not found")
			return
		}
		users, _ := decodeUsers(config["users"])
		view := redactVNCConnection(conns[index])
		view["access"] = vncAccessList(users, id)
		sendConfigJSONSuccess(w, http.StatusOK, view, "VNC connection retrieved successfully")
	case http.MethodPut:
		var req vncConnectionRequest
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req) != nil {
			sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		normalizeVNCConnection(&req.VNCConnection)
		if req.VNCID == "" {
			req.VNCID = id
		}
		if req.VNCID != id {
			sendConfigJSONError(w, http.StatusBadRequest, "VNC connection ID cannot be changed")
			return
		}
		err := ca.mutateConfig(func(config map[string]interface{}) error {
			conns, err := decodeVNCConnections(config["vnc_connections"])
			if err != nil {
				return httpErr(http.StatusInternalServerError, "Invalid vnc_connections configuration")
			}
			index := vncConnectionIndex(conns, id)
			if index < 0 {
				return httpErr(http.StatusNotFound, "VNC connection not found")
			}
			preserveVNCSecrets(&req, conns[index])
			conns[index] = req.VNCConnection
			config["vnc_connections"] = conns
			return validateVNCRaw(config)
		})
		if err != nil {
			sendConfigMutationError(w, err, "Failed to save VNC connection")
			return
		}
		writeSecurityAudit(r, actor, role, "vnc_connection_update", id, "success", vncAuditDetails(req.VNCConnection))
		sendConfigJSONSuccess(w, http.StatusOK, redactVNCConnection(req.VNCConnection), "VNC connection updated successfully")
	case http.MethodDelete:
		err := ca.mutateConfig(func(config map[string]interface{}) error {
			conns, err := decodeVNCConnections(config["vnc_connections"])
			if err != nil {
				return httpErr(http.StatusInternalServerError, "Invalid vnc_connections configuration")
			}
			index := vncConnectionIndex(conns, id)
			if index < 0 {
				return httpErr(http.StatusNotFound, "VNC connection not found")
			}
			if users := usersGranting(config["users"], "vnc", id); len(users) > 0 {
				return httpErr(http.StatusConflict, fmt.Sprintf("Cannot delete VNC connection '%s' because it is granted to user '%s'. Remove the access first.", id, users[0]))
			}
			conns = append(conns[:index], conns[index+1:]...)
			if len(conns) == 0 {
				delete(config, "vnc_connections")
			} else {
				config["vnc_connections"] = conns
			}
			return nil
		})
		if err != nil {
			sendConfigMutationError(w, err, "Failed to delete VNC connection")
			return
		}
		removeVNCThumbnail(id)
		writeSecurityAudit(r, actor, role, "vnc_connection_delete", id, "success", nil)
		sendConfigJSONSuccess(w, http.StatusOK, nil, "VNC connection deleted successfully")
	default:
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func vncAuditDetails(c VNCConnection) map[string]interface{} {
	return map[string]interface{}{
		"tunnel_host_ref": c.TunnelHostRef, "host": c.targetHost(), "port": c.port(),
		"auth_type": c.authType(), "allow_clipboard": c.clipboardAllowed(),
	}
}

func (ca *ConfigAPI) handleVNCAccessGet(w http.ResponseWriter, id string) {
	config, err := loadConfigMap(ca.configPath)
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to read configuration")
		return
	}
	conns, _ := decodeVNCConnections(config["vnc_connections"])
	if vncConnectionIndex(conns, id) < 0 {
		sendConfigJSONError(w, http.StatusNotFound, "VNC connection not found")
		return
	}
	users, _ := decodeUsers(config["users"])
	all := make([]string, 0, len(users))
	for _, u := range users {
		all = append(all, canonicalUsername(u.Username))
	}
	sort.Strings(all)
	sendConfigJSONSuccess(w, http.StatusOK, map[string]interface{}{"access": vncAccessList(users, id), "users": all}, "VNC access retrieved")
}

// handleVNCAccessPut replaces the list of users who may use one connection
// (body {"access": [{"username": "alice", "level": "control"}]}). Users left out
// lose their grant for this connection; their other grants are untouched.
func (ca *ConfigAPI) handleVNCAccessPut(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Access []struct {
			Username string `json:"username"`
			Level    string `json:"level"`
		} `json:"access"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 256*1024)).Decode(&req) != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	wanted := map[string]string{}
	for _, a := range req.Access {
		name := canonicalUsername(a.Username)
		if name == "" || name == RoleAdmin {
			sendConfigJSONError(w, http.StatusBadRequest, "Invalid username in access list (admin always has access)")
			return
		}
		level := normalizeVNCLevel(a.Level)
		if level != vncLevelControl && level != vncLevelView {
			sendConfigJSONError(w, http.StatusBadRequest, fmt.Sprintf("Invalid access level %q for user %q (control or view)", a.Level, name))
			return
		}
		wanted[name] = level
	}
	var result []map[string]interface{}
	err := ca.mutateConfig(func(config map[string]interface{}) error {
		conns, err := decodeVNCConnections(config["vnc_connections"])
		if err != nil {
			return httpErr(http.StatusInternalServerError, "Invalid vnc_connections configuration")
		}
		if vncConnectionIndex(conns, id) < 0 {
			return httpErr(http.StatusNotFound, "VNC connection not found")
		}
		users, err := decodeUsers(config["users"])
		if err != nil {
			return httpErr(http.StatusInternalServerError, "Invalid users configuration")
		}
		known := map[string]bool{}
		for i := range users {
			name := canonicalUsername(users[i].Username)
			known[name] = true
			grants := make([]VNCGrant, 0, len(users[i].VNCAccess)+1)
			for _, g := range users[i].VNCAccess {
				if g.VNCID != id {
					grants = append(grants, g)
				}
			}
			if level, ok := wanted[name]; ok {
				grants = append(grants, VNCGrant{VNCID: id, Level: level})
			}
			users[i].VNCAccess = normalizeVNCGrants(grants)
		}
		for name := range wanted {
			if !known[name] {
				return httpErr(http.StatusBadRequest, fmt.Sprintf("User %q does not exist", name))
			}
		}
		config["users"] = users
		result = vncAccessList(users, id)
		return validateVNCRaw(config)
	})
	if err != nil {
		sendConfigMutationError(w, err, "Failed to save VNC access")
		return
	}
	actor, role := currentActor(r)
	writeSecurityAudit(r, actor, role, "vnc_access_update", id, "success", map[string]interface{}{"access": result})
	sendConfigJSONSuccess(w, http.StatusOK, map[string]interface{}{"access": result}, "VNC access saved")
}

// handleVNCTest connects, authenticates and reads the desktop name and size of a
// saved connection, or of an unsaved one sent in the body (empty password = the
// stored password of the connection with that ID).
func (ca *ConfigAPI) handleVNCTest(w http.ResponseWriter, r *http.Request, id string) {
	cfg := cache.GetConfig()
	if cfg == nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Configuration unavailable")
		return
	}
	conn, exists := findVNCConnection(cfg, id)
	var body vncConnectionRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
	}
	if body.Host != "" || body.TunnelHostRef != "" {
		normalizeVNCConnection(&body.VNCConnection)
		body.VNCID = id
		if exists {
			preserveVNCSecrets(&body, conn)
		} else if isMaskedSecret(body.Password) {
			body.Password = ""
		}
		conn = body.VNCConnection
	} else if !exists {
		sendConfigJSONError(w, http.StatusNotFound, "VNC connection not found")
		return
	}
	if err := validateVNCConnections([]VNCConnection{conn}, cfg.Hosts); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	started := time.Now()
	server, info, err := dialVNCServer(conn, cfg.Hosts, vncDialTimeout)
	actor, role := currentActor(r)
	if err != nil {
		writeSecurityAudit(r, actor, role, "vnc_connection_test", id, "failed", map[string]interface{}{"error": err.Error()})
		sendConfigJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	server.Close()
	writeSecurityAudit(r, actor, role, "vnc_connection_test", id, "success", nil)
	sendConfigJSONSuccess(w, http.StatusOK, map[string]interface{}{
		"desktop_name": info.Name, "width": info.Width, "height": info.Height,
		"protocol": info.Protocol, "security": info.SecurityName, "elapsed_ms": time.Since(started).Milliseconds(),
	}, "Connected and authenticated")
}
