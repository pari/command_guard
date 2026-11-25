package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentSessionValidation(t *testing.T) {
	hosts := map[string]Host{
		"web-1": {ConnectionType: "ssh", Host: "10.0.0.1", Username: "u", Password: "p"},
		"win-1": {ConnectionType: "winrm", Host: "10.0.0.2", Username: "u", Password: "p"},
	}
	good := func() AgentSession {
		return AgentSession{ID: "abc123", Name: "Diary", HostRef: "web-1", Folder: "/ssd/www/diary", Agent: "claude", Icon: &AgentSessionIcon{Type: "fa", Value: "fa-book", Color: "#4cd964"}}
	}

	t.Run("Valid", func(t *testing.T) {
		p := good()
		p.Name = "  Diary "
		if err := normalizeAgentSession(&p); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Name != "Diary" {
			t.Fatalf("name not trimmed: %q", p.Name)
		}
		if err := validateAgentSessionsList([]AgentSession{p}, hosts, "t"); err != nil {
			t.Fatalf("unexpected list error: %v", err)
		}
	})

	t.Run("ShellOnlyAndEmojiAreFine", func(t *testing.T) {
		p := good()
		p.Agent = ""
		p.Icon = &AgentSessionIcon{Type: "emoji", Value: "📓", Color: "#ffffff"}
		if err := normalizeAgentSession(&p); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Icon.Color != "" {
			t.Fatalf("emoji icons must not keep a colour")
		}
	})

	bad := map[string]func(p *AgentSession){
		"empty name":        func(p *AgentSession) { p.Name = "" },
		"long name":         func(p *AgentSession) { p.Name = strings.Repeat("x", agentSessionMaxNameRunes+1) },
		"control char name": func(p *AgentSession) { p.Name = "a\tb" },
		"empty folder":      func(p *AgentSession) { p.Folder = "" },
		"newline in folder": func(p *AgentSession) { p.Folder = "/tmp\nrm -rf /" },
		"newline in args":   func(p *AgentSession) { p.Args = "--x\nrm" },
		"empty host":        func(p *AgentSession) { p.HostRef = "" },
		"bad id":            func(p *AgentSession) { p.ID = "has space" },
		"bad fa class":      func(p *AgentSession) { p.Icon = &AgentSessionIcon{Type: "fa", Value: "book"} },
		"bad colour":        func(p *AgentSession) { p.Icon = &AgentSessionIcon{Type: "fa", Value: "fa-book", Color: "red"} },
		"bad icon type":     func(p *AgentSession) { p.Icon = &AgentSessionIcon{Type: "svg", Value: "x"} },
		"html in emoji":     func(p *AgentSession) { p.Icon = &AgentSessionIcon{Type: "emoji", Value: "<b>"} },
	}
	for name, mutate := range bad {
		t.Run("Rejects "+name, func(t *testing.T) {
			p := good()
			mutate(&p)
			if err := normalizeAgentSession(&p); err == nil {
				t.Fatalf("expected an error")
			}
		})
	}

	t.Run("ListChecks", func(t *testing.T) {
		a, b := good(), good()
		if err := validateAgentSessionsList([]AgentSession{a, b}, hosts, "t"); err == nil || !strings.Contains(err.Error(), "duplicate id") {
			t.Fatalf("expected duplicate id error, got %v", err)
		}
		b.ID = "other"
		b.HostRef = "missing"
		if err := validateAgentSessionsList([]AgentSession{a, b}, hosts, "t"); err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("expected missing host error, got %v", err)
		}
		b.HostRef = "win-1"
		if err := validateAgentSessionsList([]AgentSession{a, b}, hosts, "t"); err == nil || !strings.Contains(err.Error(), "not an SSH host") {
			t.Fatalf("expected SSH host error, got %v", err)
		}
		b.ID = ""
		b.HostRef = "web-1"
		if err := validateAgentSessionsList([]AgentSession{a, b}, hosts, "t"); err == nil || !strings.Contains(err.Error(), "id is required") {
			t.Fatalf("expected id required error, got %v", err)
		}
	})

	t.Run("ConfigLevel", func(t *testing.T) {
		if err := validateAgentSessionsConfig(nil); err != nil {
			t.Fatal(err)
		}
		cfg := &Config{Hosts: hosts, Terminals: &TerminalsConfig{AgentSessions: []AgentSession{good()}}}
		if err := validateAgentSessionsConfig(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		cfg.Terminals.AgentSessions[0].HostRef = "nope"
		if err := validateAgentSessionsConfig(cfg); err == nil {
			t.Fatal("expected error for unknown host")
		}
	})

	t.Run("HostUsage", func(t *testing.T) {
		raw := map[string]interface{}{"terminals": map[string]interface{}{"agent_sessions": []interface{}{
			map[string]interface{}{"host_ref": "web-1"}, map[string]interface{}{"host_ref": "web-1"}, map[string]interface{}{"host_ref": "x"},
		}}}
		if n := agentSessionHostUsage(raw, "web-1"); n != 2 {
			t.Fatalf("expected 2, got %d", n)
		}
		if n := agentSessionHostUsage(map[string]interface{}{}, "web-1"); n != 0 {
			t.Fatalf("expected 0, got %d", n)
		}
	})
}

func agentSessionsBody(t *testing.T, resp *http.Response) map[string]interface{} {
	t.Helper()
	defer resp.Body.Close()
	body := AssertJSONResponse(t, resp)
	data, _ := body["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("response has no data: %v", body)
	}
	return data
}

func presetNames(list interface{}) []string {
	items, _ := list.([]interface{})
	names := []string{}
	for _, item := range items {
		entry, _ := item.(map[string]interface{})
		names = append(names, entry["name"].(string))
	}
	return names
}

func TestAgentSessionsAPI(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()
	sshHost, winrmHost := terminalTestHosts(t)

	t.Run("EmptyAtStart", func(t *testing.T) {
		data := agentSessionsBody(t, ts.DoAuthRequest(t, "GET", "/api/terminals/agent-sessions", nil))
		if len(presetNames(data["shared"])) != 0 || len(presetNames(data["personal"])) != 0 {
			t.Fatalf("expected no presets, got %v", data)
		}
		if data["can_share"] != true {
			t.Fatalf("admin must be allowed to share: %v", data["can_share"])
		}
	})

	t.Run("BodyValidation", func(t *testing.T) {
		cases := []map[string]interface{}{
			{"scope": "team", "name": "x", "host_ref": sshHost, "folder": "/tmp"},
			{"scope": "personal", "name": "", "host_ref": sshHost, "folder": "/tmp"},
			{"scope": "personal", "name": "x", "host_ref": sshHost, "folder": ""},
			{"scope": "personal", "name": "x", "host_ref": sshHost, "folder": "/tmp", "icon": map[string]interface{}{"type": "fa", "value": "nope"}},
		}
		for i, body := range cases {
			resp := ts.DoAuthRequest(t, "POST", "/api/terminals/agent-sessions", body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("case %d: expected 400, got %d", i, resp.StatusCode)
			}
			resp.Body.Close()
		}
		resp := ts.DoAuthRequest(t, "POST", "/api/terminals/agent-sessions", map[string]interface{}{"scope": "personal", "name": "x", "host_ref": "does-not-exist", "folder": "/tmp"})
		AssertStatus(t, http.StatusNotFound, resp.StatusCode)
		resp.Body.Close()
		if winrmHost != "" {
			resp = ts.DoAuthRequest(t, "POST", "/api/terminals/agent-sessions", map[string]interface{}{"scope": "shared", "name": "x", "host_ref": winrmHost, "folder": "/tmp"})
			AssertStatus(t, http.StatusBadRequest, resp.StatusCode)
			resp.Body.Close()
		}
	})

	var sharedID, personalID string
	t.Run("AdminCreatesSharedAndPersonal", func(t *testing.T) {
		data := agentSessionsBody(t, ts.DoAuthRequest(t, "POST", "/api/terminals/agent-sessions", map[string]interface{}{
			"scope": "shared", "name": "Diary", "host_ref": sshHost, "folder": "/ssd/www/diary", "agent": "claude", "args": "--resume",
			"icon": map[string]interface{}{"type": "fa", "value": "fa-book", "color": "#4cd964"},
		}))
		preset, _ := data["preset"].(map[string]interface{})
		sharedID, _ = preset["id"].(string)
		if sharedID == "" || data["scope"] != "shared" {
			t.Fatalf("shared preset not created: %v", data)
		}
		cfg := cache.GetConfig()
		if cfg.Terminals == nil || len(cfg.Terminals.AgentSessions) != 1 || cfg.Terminals.AgentSessions[0].ID != sharedID {
			t.Fatalf("shared preset not in the loaded config: %+v", cfg.Terminals)
		}
		raw, err := os.ReadFile(cache.configFile)
		if err != nil || !strings.Contains(string(raw), `"agent_sessions"`) {
			t.Fatalf("config.json does not contain agent_sessions: %v", err)
		}

		data = agentSessionsBody(t, ts.DoAuthRequest(t, "POST", "/api/terminals/agent-sessions", map[string]interface{}{
			"scope": "personal", "name": "scratch", "host_ref": sshHost, "folder": "/home/me/scratch", "icon": map[string]interface{}{"type": "emoji", "value": "🧪"},
		}))
		preset, _ = data["preset"].(map[string]interface{})
		personalID, _ = preset["id"].(string)
		if personalID == "" {
			t.Fatalf("personal preset not created: %v", data)
		}

		data = agentSessionsBody(t, ts.DoAuthRequest(t, "GET", "/api/terminals/agent-sessions", nil))
		if got := presetNames(data["shared"]); len(got) != 1 || got[0] != "Diary" {
			t.Fatalf("shared list wrong: %v", got)
		}
		if got := presetNames(data["personal"]); len(got) != 1 || got[0] != "scratch" {
			t.Fatalf("personal list wrong: %v", got)
		}
	})

	t.Run("UpdateAndNotFound", func(t *testing.T) {
		data := agentSessionsBody(t, ts.DoAuthRequest(t, "PUT", "/api/terminals/agent-sessions/"+sharedID, map[string]interface{}{
			"scope": "shared", "name": "Diary_sh", "host_ref": sshHost, "folder": "/ssd/www/diary",
		}))
		preset, _ := data["preset"].(map[string]interface{})
		if preset["name"] != "Diary_sh" || preset["id"] != sharedID {
			t.Fatalf("update failed: %v", data)
		}
		if cfg := cache.GetConfig(); cfg.Terminals.AgentSessions[0].Name != "Diary_sh" || cfg.Terminals.AgentSessions[0].Agent != "" {
			t.Fatalf("update not persisted: %+v", cfg.Terminals.AgentSessions[0])
		}
		resp := ts.DoAuthRequest(t, "PUT", "/api/terminals/agent-sessions/nope", map[string]interface{}{"scope": "personal", "name": "x", "host_ref": sshHost, "folder": "/tmp"})
		AssertStatus(t, http.StatusNotFound, resp.StatusCode)
		resp.Body.Close()
		// A personal id is not found in the shared scope.
		resp = ts.DoAuthRequest(t, "PUT", "/api/terminals/agent-sessions/"+personalID, map[string]interface{}{"scope": "shared", "name": "x", "host_ref": sshHost, "folder": "/tmp"})
		AssertStatus(t, http.StatusNotFound, resp.StatusCode)
		resp.Body.Close()
	})

	t.Run("HostDeleteIsRefusedWhileShared", func(t *testing.T) {
		resp := ts.DoAuthRequest(t, "DELETE", "/api/config/hosts/"+sshHost, nil)
		defer resp.Body.Close()
		AssertStatus(t, http.StatusConflict, resp.StatusCode)
		body := AssertJSONResponse(t, resp)
		if msg, _ := body["error"].(string); !strings.Contains(msg, "Agent Session") {
			t.Fatalf("expected the Agent Session reason, got %q", msg)
		}
	})

	t.Run("RegularUser", func(t *testing.T) {
		resp := ts.DoAuthRequest(t, "POST", "/api/users", map[string]interface{}{"username": "agent.user", "enabled": true, "temporary_password": "temporary-pass-123"})
		AssertStatus(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
		token, err := authManager.generateJWTFor("agent.user", RoleUser, 1)
		if err != nil {
			t.Fatal(err)
		}

		// No grant: the shared preset is hidden, personal presets on the host are refused.
		data := agentSessionsBody(t, terminalRequestAs(t, ts, token, "GET", "/api/terminals/agent-sessions", nil))
		if len(presetNames(data["shared"])) != 0 || data["can_share"] != false {
			t.Fatalf("user without grant must see no shared presets: %v", data)
		}
		resp = terminalRequestAs(t, ts, token, "POST", "/api/terminals/agent-sessions", map[string]interface{}{"scope": "personal", "name": "mine", "host_ref": sshHost, "folder": "/tmp"})
		AssertStatus(t, http.StatusForbidden, resp.StatusCode)
		resp.Body.Close()
		resp = terminalRequestAs(t, ts, token, "POST", "/api/terminals/agent-sessions", map[string]interface{}{"scope": "shared", "name": "mine", "host_ref": sshHost, "folder": "/tmp"})
		AssertStatus(t, http.StatusForbidden, resp.StatusCode)
		resp.Body.Close()

		// With the grant: shared preset visible, own personal preset allowed, admin's personal one invisible.
		resp = ts.DoAuthRequest(t, "PUT", "/api/users/agent.user", map[string]interface{}{"enabled": true, "terminal_hosts": []string{sshHost}})
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
		data = agentSessionsBody(t, terminalRequestAs(t, ts, token, "POST", "/api/terminals/agent-sessions", map[string]interface{}{"scope": "personal", "name": "mine", "host_ref": sshHost, "folder": "/tmp"}))
		preset, _ := data["preset"].(map[string]interface{})
		mineID, _ := preset["id"].(string)
		data = agentSessionsBody(t, terminalRequestAs(t, ts, token, "GET", "/api/terminals/agent-sessions", nil))
		if got := presetNames(data["shared"]); len(got) != 1 || got[0] != "Diary_sh" {
			t.Fatalf("granted user should see the shared preset: %v", got)
		}
		if got := presetNames(data["personal"]); len(got) != 1 || got[0] != "mine" {
			t.Fatalf("user should see only their own personal preset: %v", got)
		}
		// Shared deletes stay admin-only; the user may delete their own.
		resp = terminalRequestAs(t, ts, token, "DELETE", "/api/terminals/agent-sessions/"+sharedID+"?scope=shared", nil)
		AssertStatus(t, http.StatusForbidden, resp.StatusCode)
		resp.Body.Close()
		resp = terminalRequestAs(t, ts, token, "DELETE", "/api/terminals/agent-sessions/"+mineID+"?scope=personal", nil)
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
		// Admin's personal preset is not reachable by the user.
		resp = terminalRequestAs(t, ts, token, "DELETE", "/api/terminals/agent-sessions/"+personalID+"?scope=personal", nil)
		AssertStatus(t, http.StatusNotFound, resp.StatusCode)
		resp.Body.Close()
	})

	t.Run("Delete", func(t *testing.T) {
		resp := ts.DoAuthRequest(t, "DELETE", "/api/terminals/agent-sessions/"+sharedID+"?scope=shared", nil)
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
		resp = ts.DoAuthRequest(t, "DELETE", "/api/terminals/agent-sessions/"+personalID, nil)
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
		if cfg := cache.GetConfig(); cfg.Terminals != nil && len(cfg.Terminals.AgentSessions) != 0 {
			t.Fatalf("shared preset still in config: %+v", cfg.Terminals.AgentSessions)
		}
		data := agentSessionsBody(t, ts.DoAuthRequest(t, "GET", "/api/terminals/agent-sessions", nil))
		if len(presetNames(data["shared"])) != 0 || len(presetNames(data["personal"])) != 0 {
			t.Fatalf("expected empty lists, got %v", data)
		}
		resp = ts.DoAuthRequest(t, "DELETE", "/api/terminals/agent-sessions/"+sharedID+"?scope=shared", nil)
		AssertStatus(t, http.StatusNotFound, resp.StatusCode)
		resp.Body.Close()
	})

	t.Run("CreateTerminalValidatesMeta", func(t *testing.T) {
		// Bad meta is rejected before the daemon is contacted (which is absent in tests).
		resp := ts.DoAuthRequest(t, "POST", "/api/terminals", map[string]interface{}{"host_ref": sshHost, "meta": map[string]interface{}{"icon": map[string]interface{}{"type": "fa", "value": "x y"}}})
		AssertStatus(t, http.StatusBadRequest, resp.StatusCode)
		resp.Body.Close()
		resp = ts.DoAuthRequest(t, "POST", "/api/terminals", map[string]interface{}{"host_ref": sshHost, "meta": map[string]interface{}{"name": "Diary", "icon": map[string]interface{}{"type": "emoji", "value": "📓"}}})
		AssertStatus(t, http.StatusServiceUnavailable, resp.StatusCode)
		resp.Body.Close()
	})
}

func TestTerminalMetaSidecar(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()
	admin := &Claims{Username: "admin", Role: RoleAdmin}
	api := NewTerminalsAPI(authManager)
	api.socketPath, _ = fakeSessiond(t, false) // lists one running session "abc"
	dir := t.TempDir()
	api.orderPath = filepath.Join(dir, "terminal_order.json")
	api.metaPath = filepath.Join(dir, "terminal_meta.json")

	icon := &AgentSessionIcon{Type: "fa", Value: "fa-book", Color: "#4cd964"}
	api.storeTerminalMeta("abc", TerminalSessionMeta{PresetID: "p1", Name: "Diary", Agent: "claude", Folder: "/ssd/www/diary", Icon: icon})
	api.storeTerminalMeta("gone", TerminalSessionMeta{Name: "Old"})

	rec := httptest.NewRecorder()
	api.handleList(rec, httptest.NewRequest("GET", "/api/terminals", nil), admin, currentTerminalSettings())
	AssertStatus(t, http.StatusOK, rec.Code)
	var body struct {
		Data struct {
			Sessions []TerminalSession `json:"sessions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data.Sessions) != 1 || body.Data.Sessions[0].Meta == nil || body.Data.Sessions[0].Meta.Name != "Diary" || body.Data.Sessions[0].Meta.Icon.Value != "fa-book" {
		t.Fatalf("meta not attached to the listed session: %+v", body.Data.Sessions)
	}

	// Metadata of sessions the daemon no longer lists is pruned.
	api.metaMu.Lock()
	all := api.readTerminalMetaLocked()
	api.metaMu.Unlock()
	if _, ok := all["gone"]; ok {
		t.Fatalf("stale meta was not pruned: %v", all)
	}
	if _, ok := all["abc"]; !ok {
		t.Fatalf("live meta was pruned: %v", all)
	}

	// Personal presets round-trip through their sidecar file.
	api.agentPath = filepath.Join(dir, "terminal_agent_sessions.json")
	err := api.mutatePersonalAgentSessions("bob", func(list []AgentSession) ([]AgentSession, error) {
		return append(list, AgentSession{ID: "x1", Name: "A", HostRef: "web-1", Folder: "/a"}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := api.personalAgentSessions("bob"); len(got) != 1 || got[0].Name != "A" {
		t.Fatalf("personal preset not stored: %v", got)
	}
	if got := api.personalAgentSessions("alice"); len(got) != 0 {
		t.Fatalf("alice must have no presets: %v", got)
	}
	raw, _ := os.ReadFile(api.agentPath)
	if !strings.Contains(string(raw), `"bob"`) {
		t.Fatalf("sidecar file not written: %s", raw)
	}
}
