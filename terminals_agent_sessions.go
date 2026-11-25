package main

/*
Agent Sessions: saved launch presets for the Terminals page.

A preset is host + folder + optional coding agent + display name + icon. Launching
one creates an ordinary terminal; the browser then types
"cd '<folder>' && <agent> <args>" once the shell is up, and the tab is named
"<name>", "<name>_02", ... with the preset's icon in front.

Two kinds of presets exist:
  - shared:   terminals.agent_sessions in config.json, edited by admins, offered to
              every user who may open terminals on the preset's host
  - personal: per-user lists in run/terminal_agent_sessions.json (outside config.json,
              like the per-user tab order), editable by their owner

Sessions themselves carry no metadata in cg_sessiond, so the icon shown on a
running session's tab comes from run/terminal_meta.json (session id -> meta),
written when a session is created from a preset and pruned when the daemon no
longer lists the session. The daemon is never changed for this feature: a
restart of cg_sessiond would end every open terminal.
*/

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	terminalAgentSessionsPath  = "run/terminal_agent_sessions.json" // personal presets, keyed by username
	terminalMetaPath           = "run/terminal_meta.json"           // session id -> preset metadata (icon, agent, folder)
	agentSessionMaxNameRunes   = 30
	agentSessionMaxFolderRunes = 512
	agentSessionMaxAgentRunes  = 200
	agentSessionMaxArgsRunes   = 500
	agentSessionMaxIconRunes   = 16
	agentSessionMaxPerUser     = 200
	agentSessionScopeShared    = "shared"
	agentSessionScopePersonal  = "personal"
)

var (
	agentSessionIDPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	agentSessionFAPattern    = regexp.MustCompile(`^fa-[a-z0-9-]{1,48}$`)
	agentSessionColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// AgentSessionIcon is the small icon shown before the tab name: a Font Awesome
// solid icon with a colour, or a single emoji.
type AgentSessionIcon struct {
	Type  string `json:"type"`            // "fa" or "emoji"
	Value string `json:"value"`           // "fa-book" or "📓"
	Color string `json:"color,omitempty"` // "#rrggbb"; fa icons only
}

// AgentSession is one saved launch preset (shared in config.json or personal in the sidecar).
type AgentSession struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`            // tab base name, e.g. "Diary"
	HostRef string            `json:"host_ref"`        // SSH host to connect to
	Folder  string            `json:"folder"`          // cd target on the host
	Agent   string            `json:"agent,omitempty"` // command started after cd; empty = shell only
	Args    string            `json:"args,omitempty"`  // extra arguments appended to the agent command
	Icon    *AgentSessionIcon `json:"icon,omitempty"`
}

// TerminalSessionMeta is what the browser sends with a create request made from a
// preset and what the list endpoint returns on the session, so every window shows
// the same icon. It is a copy: deleting the preset does not change running tabs.
type TerminalSessionMeta struct {
	PresetID string            `json:"preset_id,omitempty"`
	Name     string            `json:"name,omitempty"`
	Agent    string            `json:"agent,omitempty"`
	Folder   string            `json:"folder,omitempty"`
	Icon     *AgentSessionIcon `json:"icon,omitempty"`
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func hasControlChars(s string) bool {
	for _, r := range s {
		if r < 32 || r == 127 {
			return true
		}
	}
	return false
}

func validateAgentSessionIcon(icon *AgentSessionIcon) error {
	if icon == nil {
		return nil
	}
	switch icon.Type {
	case "fa":
		if !agentSessionFAPattern.MatchString(icon.Value) {
			return fmt.Errorf("icon value %q is not a Font Awesome class (expected e.g. \"fa-book\")", icon.Value)
		}
		if icon.Color != "" && !agentSessionColorPattern.MatchString(icon.Color) {
			return fmt.Errorf("icon color %q must be #rrggbb", icon.Color)
		}
	case "emoji":
		if icon.Value == "" || utf8.RuneCountInString(icon.Value) > agentSessionMaxIconRunes || hasControlChars(icon.Value) || strings.ContainsAny(icon.Value, "<>&\"'") {
			return errors.New("emoji icon must be a single emoji")
		}
		icon.Color = ""
	default:
		return fmt.Errorf("icon type %q must be \"fa\" or \"emoji\"", icon.Type)
	}
	return nil
}

// normalizeAgentSession trims the text fields and validates everything except the host
// (checked by the caller against the host map or the caller's grants).
func normalizeAgentSession(p *AgentSession) error {
	p.ID = strings.TrimSpace(p.ID)
	p.Name = strings.TrimSpace(p.Name)
	p.HostRef = strings.TrimSpace(p.HostRef)
	p.Folder = strings.TrimSpace(p.Folder)
	p.Agent = strings.TrimSpace(p.Agent)
	p.Args = strings.TrimSpace(p.Args)
	if p.ID != "" && !agentSessionIDPattern.MatchString(p.ID) {
		return fmt.Errorf("id %q may only contain letters, digits, '-' and '_' (max 64)", p.ID)
	}
	if p.Name == "" {
		return errors.New("name is required")
	}
	if utf8.RuneCountInString(p.Name) > agentSessionMaxNameRunes || hasControlChars(p.Name) {
		return fmt.Errorf("name %q must be 1-%d characters without control characters", p.Name, agentSessionMaxNameRunes)
	}
	if p.HostRef == "" {
		return errors.New("host_ref is required")
	}
	if p.Folder == "" {
		return errors.New("folder is required")
	}
	if utf8.RuneCountInString(p.Folder) > agentSessionMaxFolderRunes || hasControlChars(p.Folder) {
		return fmt.Errorf("folder must be at most %d characters without control characters", agentSessionMaxFolderRunes)
	}
	if utf8.RuneCountInString(p.Agent) > agentSessionMaxAgentRunes || hasControlChars(p.Agent) {
		return fmt.Errorf("agent must be at most %d characters without control characters", agentSessionMaxAgentRunes)
	}
	if utf8.RuneCountInString(p.Args) > agentSessionMaxArgsRunes || hasControlChars(p.Args) {
		return fmt.Errorf("args must be at most %d characters without control characters", agentSessionMaxArgsRunes)
	}
	if p.Icon != nil && p.Icon.Type == "" && p.Icon.Value == "" {
		p.Icon = nil
	}
	return validateAgentSessionIcon(p.Icon)
}

// validateAgentSessionsList checks a whole list: every entry valid, ids unique, hosts
// existing SSH hosts. label names the list in messages ("terminals.agent_sessions").
func validateAgentSessionsList(list []AgentSession, hosts map[string]Host, label string) error {
	seen := map[string]bool{}
	for i := range list {
		p := &list[i]
		if err := normalizeAgentSession(p); err != nil {
			return fmt.Errorf("%s[%d]: %v", label, i, err)
		}
		if p.ID == "" {
			return fmt.Errorf("%s[%d]: id is required", label, i)
		}
		if seen[p.ID] {
			return fmt.Errorf("%s: duplicate id %q", label, p.ID)
		}
		seen[p.ID] = true
		host, ok := hosts[p.HostRef]
		if !ok {
			return fmt.Errorf("%s[%d] (%s): host_ref %q does not exist", label, i, p.Name, p.HostRef)
		}
		if !isSSHHost(host) {
			return fmt.Errorf("%s[%d] (%s): host %q is not an SSH host", label, i, p.Name, p.HostRef)
		}
	}
	return nil
}

// validateAgentSessionsConfig validates the shared presets in the terminals block
// (used by --validate, hot reload and restore).
func validateAgentSessionsConfig(cfg *Config) error {
	if cfg == nil || cfg.Terminals == nil || len(cfg.Terminals.AgentSessions) == 0 {
		return nil
	}
	return validateAgentSessionsList(cfg.Terminals.AgentSessions, cfg.Hosts, "terminals.agent_sessions")
}

// agentSessionHostUsage counts shared presets on the raw config document that use hostName
// (host delete protection in HandleDeleteHost).
func agentSessionHostUsage(config map[string]interface{}, hostName string) int {
	terminals, _ := config["terminals"].(map[string]interface{})
	list, _ := terminals["agent_sessions"].([]interface{})
	n := 0
	for _, item := range list {
		entry, _ := item.(map[string]interface{})
		if ref, _ := entry["host_ref"].(string); ref == hostName {
			n++
		}
	}
	return n
}

// validateTerminalSessionMeta checks the optional meta a browser sends when creating a
// session from a preset.
func validateTerminalSessionMeta(meta *TerminalSessionMeta) error {
	if meta == nil {
		return nil
	}
	meta.PresetID = strings.TrimSpace(meta.PresetID)
	meta.Name = strings.TrimSpace(meta.Name)
	meta.Agent = strings.TrimSpace(meta.Agent)
	meta.Folder = strings.TrimSpace(meta.Folder)
	if meta.PresetID != "" && !agentSessionIDPattern.MatchString(meta.PresetID) {
		return errors.New("meta.preset_id is invalid")
	}
	if utf8.RuneCountInString(meta.Name) > agentSessionMaxNameRunes || hasControlChars(meta.Name) {
		return errors.New("meta.name is too long")
	}
	if utf8.RuneCountInString(meta.Agent) > agentSessionMaxAgentRunes || hasControlChars(meta.Agent) {
		return errors.New("meta.agent is too long")
	}
	if utf8.RuneCountInString(meta.Folder) > agentSessionMaxFolderRunes || hasControlChars(meta.Folder) {
		return errors.New("meta.folder is too long")
	}
	if meta.Icon != nil && meta.Icon.Type == "" && meta.Icon.Value == "" {
		meta.Icon = nil
	}
	if err := validateAgentSessionIcon(meta.Icon); err != nil {
		return fmt.Errorf("meta.%v", err)
	}
	return nil
}

func newAgentSessionID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("as%d", os.Getpid())
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Sidecar files (same pattern as run/terminal_order.json: mutex, atomic write, 0600)
// ---------------------------------------------------------------------------

func writeSidecarJSON(path string, value interface{}) error {
	data, err := marshalJSONNoEscape(value)
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func marshalJSONNoEscape(value interface{}) ([]byte, error) {
	var buf strings.Builder
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "\t")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// readPersonalAgentSessionsLocked reads every user's personal presets. Caller holds agentMu.
func (t *TerminalsAPI) readPersonalAgentSessionsLocked() map[string][]AgentSession {
	out := map[string][]AgentSession{}
	data, err := os.ReadFile(t.agentPath)
	if err != nil {
		return out
	}
	if err := json.Unmarshal(data, &out); err != nil {
		log.Printf("terminals: ignoring unreadable %s: %v", t.agentPath, err)
		return map[string][]AgentSession{}
	}
	return out
}

func (t *TerminalsAPI) personalAgentSessions(username string) []AgentSession {
	t.agentMu.Lock()
	defer t.agentMu.Unlock()
	return t.readPersonalAgentSessionsLocked()[username]
}

// mutatePersonalAgentSessions edits one user's personal list under the lock and writes it back.
func (t *TerminalsAPI) mutatePersonalAgentSessions(username string, fn func(list []AgentSession) ([]AgentSession, error)) error {
	t.agentMu.Lock()
	defer t.agentMu.Unlock()
	all := t.readPersonalAgentSessionsLocked()
	list, err := fn(all[username])
	if err != nil {
		return err
	}
	if len(list) == 0 {
		delete(all, username)
	} else {
		all[username] = list
	}
	return writeSidecarJSON(t.agentPath, all)
}

func (t *TerminalsAPI) readTerminalMetaLocked() map[string]TerminalSessionMeta {
	out := map[string]TerminalSessionMeta{}
	data, err := os.ReadFile(t.metaPath)
	if err != nil {
		return out
	}
	if err := json.Unmarshal(data, &out); err != nil {
		log.Printf("terminals: ignoring unreadable %s: %v", t.metaPath, err)
		return map[string]TerminalSessionMeta{}
	}
	return out
}

// storeTerminalMeta remembers the preset metadata of a freshly created session.
func (t *TerminalsAPI) storeTerminalMeta(sessionID string, meta TerminalSessionMeta) {
	t.metaMu.Lock()
	defer t.metaMu.Unlock()
	all := t.readTerminalMetaLocked()
	all[sessionID] = meta
	if err := writeSidecarJSON(t.metaPath, all); err != nil {
		log.Printf("terminals: failed to save %s: %v", t.metaPath, err)
	}
}

// attachTerminalMeta sets Meta on every session that was created from a preset and drops
// metadata of sessions the daemon no longer knows (ended and expired, or daemon restarted).
// prune must only be true for a complete list from the daemon.
func (t *TerminalsAPI) attachTerminalMeta(sessions []TerminalSession, prune bool) {
	t.metaMu.Lock()
	defer t.metaMu.Unlock()
	all := t.readTerminalMetaLocked()
	if len(all) == 0 {
		return
	}
	known := make(map[string]bool, len(sessions))
	for i := range sessions {
		known[sessions[i].ID] = true
		if meta, ok := all[sessions[i].ID]; ok {
			m := meta
			sessions[i].Meta = &m
		}
	}
	if !prune {
		return
	}
	changed := false
	for id := range all {
		if !known[id] {
			delete(all, id)
			changed = true
		}
	}
	if changed {
		if err := writeSidecarJSON(t.metaPath, all); err != nil {
			log.Printf("terminals: failed to save %s: %v", t.metaPath, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Listing
// ---------------------------------------------------------------------------

// presetUsable reports whether the caller may launch the preset: the host must still
// exist, be SSH and be allowed for the caller.
func (t *TerminalsAPI) presetUsable(claims *Claims, p AgentSession) bool {
	_, herr := t.resolveTerminalHost(claims, p.HostRef)
	return herr == nil
}

func sortAgentSessions(list []AgentSession) {
	sort.SliceStable(list, func(i, j int) bool {
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
}

// sharedAgentSessions returns the config presets (a copy).
func sharedAgentSessions() []AgentSession {
	cfg := cache.GetConfig()
	if cfg == nil || cfg.Terminals == nil {
		return nil
	}
	out := make([]AgentSession, len(cfg.Terminals.AgentSessions))
	copy(out, cfg.Terminals.AgentSessions)
	return out
}

// visibleAgentSessions returns the shared and personal presets the caller can launch.
func (t *TerminalsAPI) visibleAgentSessions(claims *Claims) (shared, personal []AgentSession) {
	shared = make([]AgentSession, 0)
	for _, p := range sharedAgentSessions() {
		if t.presetUsable(claims, p) {
			shared = append(shared, p)
		}
	}
	personal = make([]AgentSession, 0)
	for _, p := range t.personalAgentSessions(claims.Username) {
		if t.presetUsable(claims, p) {
			personal = append(personal, p)
		}
	}
	sortAgentSessions(shared)
	sortAgentSessions(personal)
	return shared, personal
}

// ---------------------------------------------------------------------------
// HTTP handlers: /api/terminals/agent-sessions[/{id}]
// ---------------------------------------------------------------------------

type agentSessionRequest struct {
	Scope string `json:"scope"`
	AgentSession
}

func (t *TerminalsAPI) handleAgentSessionsList(w http.ResponseWriter, r *http.Request, claims *Claims) {
	shared, personal := t.visibleAgentSessions(claims)
	sendJSONSuccess(w, http.StatusOK, map[string]interface{}{
		"shared":       shared,
		"personal":     personal,
		"can_share":    claims.Role == RoleAdmin,
		"max_per_user": agentSessionMaxPerUser,
	}, "Agent sessions retrieved")
}

func decodeAgentSessionRequest(w http.ResponseWriter, r *http.Request) (*agentSessionRequest, bool) {
	var req agentSessionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		sendJSONError(w, http.StatusBadRequest, "Invalid request body")
		return nil, false
	}
	req.Scope = strings.ToLower(strings.TrimSpace(req.Scope))
	if req.Scope == "" {
		req.Scope = agentSessionScopePersonal
	}
	if req.Scope != agentSessionScopeShared && req.Scope != agentSessionScopePersonal {
		sendJSONError(w, http.StatusBadRequest, "scope must be \"shared\" or \"personal\"")
		return nil, false
	}
	return &req, true
}

// checkAgentSessionWrite validates the preset body and the caller's right to use the host
// (and, for shared presets, the admin role). Returns the HTTP status to answer on failure.
func (t *TerminalsAPI) checkAgentSessionWrite(claims *Claims, req *agentSessionRequest) (int, string) {
	if req.Scope == agentSessionScopeShared && claims.Role != RoleAdmin {
		return http.StatusForbidden, "Only admin can manage shared Agent Sessions"
	}
	if req.Scope == agentSessionScopeShared && t.config == nil {
		return http.StatusInternalServerError, "Configuration API unavailable"
	}
	if err := normalizeAgentSession(&req.AgentSession); err != nil {
		return http.StatusBadRequest, err.Error()
	}
	if _, herr := t.resolveTerminalHost(claims, req.HostRef); herr != nil {
		return herr.status, herr.message
	}
	return 0, ""
}

func agentSessionAuditDetails(scope string, p AgentSession) map[string]interface{} {
	return map[string]interface{}{"scope": scope, "preset_id": p.ID, "name": p.Name, "host_ref": p.HostRef, "folder": p.Folder, "agent": p.Agent}
}

// mutateSharedAgentSessions edits terminals.agent_sessions in config.json through mutateConfig.
// fn gets the current typed list and returns the new one.
func (t *TerminalsAPI) mutateSharedAgentSessions(fn func(list []AgentSession) ([]AgentSession, error)) error {
	return t.config.mutateConfig(func(cfg map[string]interface{}) error {
		terminals, _ := cfg["terminals"].(map[string]interface{})
		if terminals == nil {
			terminals = map[string]interface{}{}
		}
		var current []AgentSession
		if raw, ok := terminals["agent_sessions"]; ok && raw != nil {
			data, err := json.Marshal(raw)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &current); err != nil {
				return httpErr(http.StatusInternalServerError, "terminals.agent_sessions in config.json is malformed")
			}
		}
		next, err := fn(current)
		if err != nil {
			return err
		}
		hosts := decodeHostsMap(cfg["hosts"])
		if err := validateAgentSessionsList(next, hosts, "terminals.agent_sessions"); err != nil {
			return httpErr(http.StatusBadRequest, err.Error())
		}
		if len(next) == 0 {
			delete(terminals, "agent_sessions")
		} else {
			data, err := json.Marshal(next)
			if err != nil {
				return err
			}
			var generic []interface{}
			if err := json.Unmarshal(data, &generic); err != nil {
				return err
			}
			terminals["agent_sessions"] = generic
		}
		if len(terminals) == 0 {
			delete(cfg, "terminals")
		} else {
			cfg["terminals"] = terminals
		}
		return nil
	})
}

func (t *TerminalsAPI) handleAgentSessionCreate(w http.ResponseWriter, r *http.Request, claims *Claims) {
	req, ok := decodeAgentSessionRequest(w, r)
	if !ok {
		return
	}
	req.ID = ""
	if status, msg := t.checkAgentSessionWrite(claims, req); status != 0 {
		sendJSONError(w, status, msg)
		return
	}
	preset := req.AgentSession
	preset.ID = newAgentSessionID()
	add := func(list []AgentSession) ([]AgentSession, error) {
		if len(list) >= agentSessionMaxPerUser {
			return nil, httpErr(http.StatusConflict, fmt.Sprintf("Limit of %d Agent Sessions reached", agentSessionMaxPerUser))
		}
		for _, existing := range list {
			if existing.ID == preset.ID {
				preset.ID = newAgentSessionID()
			}
		}
		return append(list, preset), nil
	}
	var err error
	if req.Scope == agentSessionScopeShared {
		err = t.mutateSharedAgentSessions(add)
	} else {
		err = t.mutatePersonalAgentSessions(claims.Username, add)
	}
	if err != nil {
		sendAgentSessionError(w, err, "Failed to save Agent Session")
		return
	}
	writeSecurityAudit(r, claims.Username, claims.Role, "terminal_agent_session_create", preset.HostRef, "success", agentSessionAuditDetails(req.Scope, preset))
	sendJSONSuccess(w, http.StatusCreated, map[string]interface{}{"scope": req.Scope, "preset": preset}, "Agent Session created")
}

func (t *TerminalsAPI) handleAgentSessionUpdate(w http.ResponseWriter, r *http.Request, claims *Claims, id string) {
	req, ok := decodeAgentSessionRequest(w, r)
	if !ok {
		return
	}
	req.ID = id
	if status, msg := t.checkAgentSessionWrite(claims, req); status != 0 {
		sendJSONError(w, status, msg)
		return
	}
	preset := req.AgentSession
	var old AgentSession
	replace := func(list []AgentSession) ([]AgentSession, error) {
		for i := range list {
			if list[i].ID == id {
				old = list[i]
				list[i] = preset
				return list, nil
			}
		}
		return nil, httpErr(http.StatusNotFound, "Agent Session not found")
	}
	var err error
	if req.Scope == agentSessionScopeShared {
		err = t.mutateSharedAgentSessions(replace)
	} else {
		err = t.mutatePersonalAgentSessions(claims.Username, replace)
	}
	if err != nil {
		sendAgentSessionError(w, err, "Failed to save Agent Session")
		return
	}
	details := agentSessionAuditDetails(req.Scope, preset)
	details["old_name"] = old.Name
	writeSecurityAudit(r, claims.Username, claims.Role, "terminal_agent_session_update", preset.HostRef, "success", details)
	sendJSONSuccess(w, http.StatusOK, map[string]interface{}{"scope": req.Scope, "preset": preset}, "Agent Session updated")
}

func (t *TerminalsAPI) handleAgentSessionDelete(w http.ResponseWriter, r *http.Request, claims *Claims, id string) {
	scope := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope")))
	if scope == "" {
		scope = agentSessionScopePersonal
	}
	if scope != agentSessionScopeShared && scope != agentSessionScopePersonal {
		sendJSONError(w, http.StatusBadRequest, "scope must be \"shared\" or \"personal\"")
		return
	}
	if scope == agentSessionScopeShared && claims.Role != RoleAdmin {
		sendJSONError(w, http.StatusForbidden, "Only admin can manage shared Agent Sessions")
		return
	}
	if scope == agentSessionScopeShared && t.config == nil {
		sendJSONError(w, http.StatusInternalServerError, "Configuration API unavailable")
		return
	}
	var removed AgentSession
	remove := func(list []AgentSession) ([]AgentSession, error) {
		for i := range list {
			if list[i].ID == id {
				removed = list[i]
				return append(list[:i:i], list[i+1:]...), nil
			}
		}
		return nil, httpErr(http.StatusNotFound, "Agent Session not found")
	}
	var err error
	if scope == agentSessionScopeShared {
		err = t.mutateSharedAgentSessions(remove)
	} else {
		err = t.mutatePersonalAgentSessions(claims.Username, remove)
	}
	if err != nil {
		sendAgentSessionError(w, err, "Failed to delete Agent Session")
		return
	}
	writeSecurityAudit(r, claims.Username, claims.Role, "terminal_agent_session_delete", removed.HostRef, "success", agentSessionAuditDetails(scope, removed))
	sendJSONSuccess(w, http.StatusOK, nil, "Agent Session deleted")
}

// sendAgentSessionError maps a configHTTPError (from httpErr) to its status, anything else to 500.
func sendAgentSessionError(w http.ResponseWriter, err error, fallback string) {
	var httpError *configHTTPError
	if errors.As(err, &httpError) {
		sendJSONError(w, httpError.Status, httpError.Message)
		return
	}
	log.Printf("terminals: agent session write failed: %v", err)
	sendJSONError(w, http.StatusInternalServerError, fallback+": "+err.Error())
}

// readAgentSessionBody is used by tests to decode a response payload.
func readAgentSessionBody(r io.Reader) (map[string]interface{}, error) {
	var body map[string]interface{}
	err := json.NewDecoder(r).Decode(&body)
	return body, err
}
