package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// terminalTestHosts picks one SSH host and (if present) one WinRM host from the test config.
func terminalTestHosts(t *testing.T) (string, string) {
	t.Helper()
	sshHost, winrmHost := "", ""
	cfg := cache.GetConfig()
	for id, host := range cfg.Hosts {
		if isSSHHost(host) && (sshHost == "" || id < sshHost) {
			sshHost = id
		}
		if strings.EqualFold(host.ConnectionType, "winrm") && winrmHost == "" {
			winrmHost = id
		}
	}
	if sshHost == "" {
		t.Skip("test config has no SSH host")
	}
	return sshHost, winrmHost
}

func terminalRequestAs(t *testing.T, ts *TestServer, token, method, path string, body interface{}) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, ts.BaseURL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func terminalHostIDs(t *testing.T, resp *http.Response) map[string]bool {
	t.Helper()
	defer resp.Body.Close()
	AssertStatus(t, http.StatusOK, resp.StatusCode)
	body := AssertJSONResponse(t, resp)
	AssertSuccess(t, body)
	hosts, _ := body["data"].([]interface{})
	seen := map[string]bool{}
	for _, item := range hosts {
		entry, _ := item.(map[string]interface{})
		seen[entry["host_id"].(string)] = true
	}
	return seen
}

func TestTerminalsAuthorization(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()
	sshHost, winrmHost := terminalTestHosts(t)

	t.Run("AdminIsOfferedOnlySSHHosts", func(t *testing.T) {
		seen := terminalHostIDs(t, ts.DoAuthRequest(t, "GET", "/api/terminals/hosts", nil))
		if !seen[sshHost] {
			t.Fatalf("SSH host %q missing from %v", sshHost, seen)
		}
		if winrmHost != "" && seen[winrmHost] {
			t.Fatalf("WinRM host %q must not be offered for terminals", winrmHost)
		}
	})

	t.Run("CreateValidation", func(t *testing.T) {
		resp := ts.DoAuthRequest(t, "POST", "/api/terminals", map[string]interface{}{"name": "no host"})
		AssertStatus(t, http.StatusBadRequest, resp.StatusCode)
		resp.Body.Close()

		resp = ts.DoAuthRequest(t, "POST", "/api/terminals", map[string]interface{}{"host_ref": "does-not-exist"})
		AssertStatus(t, http.StatusNotFound, resp.StatusCode)
		resp.Body.Close()

		if winrmHost != "" {
			resp = ts.DoAuthRequest(t, "POST", "/api/terminals", map[string]interface{}{"host_ref": winrmHost})
			AssertStatus(t, http.StatusBadRequest, resp.StatusCode)
			resp.Body.Close()
		}

		// Valid host: authorization passes, but the daemon is unreachable in tests.
		resp = ts.DoAuthRequest(t, "POST", "/api/terminals", map[string]interface{}{"host_ref": sshHost})
		AssertStatus(t, http.StatusServiceUnavailable, resp.StatusCode)
		resp.Body.Close()
	})

	t.Run("UserGrants", func(t *testing.T) {
		resp := ts.DoAuthRequest(t, "POST", "/api/users", map[string]interface{}{"username": "term.user", "enabled": true, "temporary_password": "temporary-pass-123"})
		AssertStatus(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
		token, err := authManager.generateJWTFor("term.user", RoleUser, 1)
		if err != nil {
			t.Fatalf("generate user token: %v", err)
		}

		// No grant yet: nothing offered, create refused before any daemon contact.
		if seen := terminalHostIDs(t, terminalRequestAs(t, ts, token, "GET", "/api/terminals/hosts", nil)); len(seen) != 0 {
			t.Fatalf("user without grants should see no hosts, got %v", seen)
		}
		resp = terminalRequestAs(t, ts, token, "POST", "/api/terminals", map[string]interface{}{"host_ref": sshHost})
		AssertStatus(t, http.StatusForbidden, resp.StatusCode)
		resp.Body.Close()

		// Grants must reference existing SSH hosts.
		resp = ts.DoAuthRequest(t, "PUT", "/api/users/term.user", map[string]interface{}{"enabled": true, "terminal_hosts": []string{"missing-host"}})
		AssertStatus(t, http.StatusBadRequest, resp.StatusCode)
		resp.Body.Close()
		if winrmHost != "" {
			resp = ts.DoAuthRequest(t, "PUT", "/api/users/term.user", map[string]interface{}{"enabled": true, "terminal_hosts": []string{winrmHost}})
			AssertStatus(t, http.StatusBadRequest, resp.StatusCode)
			resp.Body.Close()
		}

		resp = ts.DoAuthRequest(t, "PUT", "/api/users/term.user", map[string]interface{}{"enabled": true, "terminal_hosts": []string{sshHost}})
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
		resp = ts.DoAuthRequest(t, "GET", "/api/users/term.user", nil)
		body := AssertJSONResponse(t, resp)
		resp.Body.Close()
		data, _ := body["data"].(map[string]interface{})
		grants, _ := data["terminal_hosts"].([]interface{})
		if len(grants) != 1 || grants[0] != sshHost {
			t.Fatalf("terminal_hosts not persisted: %v", data["terminal_hosts"])
		}

		// With the grant the host is offered and create proceeds to the daemon step.
		if seen := terminalHostIDs(t, terminalRequestAs(t, ts, token, "GET", "/api/terminals/hosts", nil)); !seen[sshHost] {
			t.Fatalf("granted host %q not offered: %v", sshHost, seen)
		}
		resp = terminalRequestAs(t, ts, token, "POST", "/api/terminals", map[string]interface{}{"host_ref": sshHost})
		AssertStatus(t, http.StatusServiceUnavailable, resp.StatusCode)
		resp.Body.Close()

		// A host granted as a terminal host cannot be deleted.
		resp = ts.DoAuthRequest(t, "DELETE", "/api/config/hosts/"+sshHost, nil)
		AssertStatus(t, http.StatusConflict, resp.StatusCode)
		resp.Body.Close()

		// Revoking the grant takes effect immediately.
		resp = ts.DoAuthRequest(t, "PUT", "/api/users/term.user", map[string]interface{}{"enabled": true, "terminal_hosts": []string{}})
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
		resp = terminalRequestAs(t, ts, token, "POST", "/api/terminals", map[string]interface{}{"host_ref": sshHost})
		AssertStatus(t, http.StatusForbidden, resp.StatusCode)
		resp.Body.Close()
	})

	t.Run("RenameValidation", func(t *testing.T) {
		resp := ts.DoAuthRequest(t, "PATCH", "/api/terminals/abc", "not an object")
		AssertStatus(t, http.StatusBadRequest, resp.StatusCode)
		resp.Body.Close()

		// Valid body: the session lookup needs the daemon, which is absent in tests.
		resp = ts.DoAuthRequest(t, "PATCH", "/api/terminals/abc", map[string]interface{}{"name": "deploy"})
		AssertStatus(t, http.StatusServiceUnavailable, resp.StatusCode)
		resp.Body.Close()
	})

	t.Run("WebSocketRequiresToken", func(t *testing.T) {
		resp, err := http.Get(ts.BaseURL + "/api/terminals/abc/ws")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		AssertStatus(t, http.StatusUnauthorized, resp.StatusCode)
		resp.Body.Close()
	})
}

func TestValidateTerminalsConfig(t *testing.T) {
	if err := validateTerminalsConfig(nil); err != nil {
		t.Fatalf("nil block must be valid: %v", err)
	}
	if err := validateTerminalsConfig(&TerminalsConfig{ScrollbackBytes: "2MB", MaxSessionsPerUser: 3, IdleKillHours: 12}); err != nil {
		t.Fatalf("valid block rejected: %v", err)
	}
	if err := validateTerminalsConfig(&TerminalsConfig{ScrollbackBytes: "lots"}); err == nil {
		t.Fatal("invalid scrollback_bytes accepted")
	}
	if err := validateTerminalsConfig(&TerminalsConfig{IdleKillHours: -1}); err == nil {
		t.Fatal("negative idle_kill_hours accepted")
	}
}

func TestSanitizeTerminalName(t *testing.T) {
	if got := sanitizeTerminalName("  build\x1b[31m box \n"); got != "build[31m box" {
		t.Fatalf("control characters not stripped: %q", got)
	}
	long := strings.Repeat("x", 100)
	if got := sanitizeTerminalName(long); len([]rune(got)) != terminalMaxNameRunes {
		t.Fatalf("name not truncated: %d", len([]rune(got)))
	}
}

// fakeSessiond answers list with one session and rename either like cg_sessiond 1.1.0
// or, when legacy is set, like a 1.0.0 build that does not know the op.
func fakeSessiond(t *testing.T, legacy bool) (string, *[]sessiondRequest) {
	t.Helper()
	dir, err := os.MkdirTemp("", "cgsd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	received := &[]sessiondRequest{}
	session := TerminalSession{ID: "abc", Owner: "admin", HostRef: "web-1", Name: "web-1", Status: "running"}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadBytes('\n')
			var req sessiondRequest
			_ = json.Unmarshal(line, &req)
			*received = append(*received, req)
			resp := sessiondResponse{OK: true}
			switch {
			case req.Op == "list":
				resp.Sessions = []TerminalSession{session}
			case req.Op == "rename" && legacy:
				resp = sessiondResponse{OK: false, Error: "unknown op"}
			case req.Op == "rename":
				session.Name = req.Name
				resp.Session = &session
			}
			out, _ := json.Marshal(resp)
			conn.Write(append(out, '\n'))
			conn.Close()
		}
	}()
	return path, received
}

func TestTerminalRename(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()
	admin := &Claims{Username: "admin", Role: RoleAdmin}
	rename := func(api *TerminalsAPI, id, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		api.handleRename(rec, httptest.NewRequest("PATCH", "/api/terminals/"+id, strings.NewReader(body)), admin, id)
		return rec
	}

	t.Run("SanitizedNameReachesDaemon", func(t *testing.T) {
		api := NewTerminalsAPI(authManager)
		var received *[]sessiondRequest
		api.socketPath, received = fakeSessiond(t, false)
		rec := rename(api, "abc", `{"name":"  deploy\u0007 shell  "}`)
		AssertStatus(t, http.StatusOK, rec.Code)
		var body struct {
			Data TerminalSession `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body.Data.Name != "deploy shell" {
			t.Fatalf("expected renamed session in response, got %+v", body.Data)
		}
		last := (*received)[len(*received)-1]
		if last.Op != "rename" || last.ID != "abc" || last.Name != "deploy shell" {
			t.Fatalf("unexpected daemon request %+v", last)
		}
	})

	t.Run("UnknownSession", func(t *testing.T) {
		api := NewTerminalsAPI(authManager)
		api.socketPath, _ = fakeSessiond(t, false)
		AssertStatus(t, http.StatusNotFound, rename(api, "nope", `{"name":"x"}`).Code)
	})

	t.Run("OldDaemonExplainsRestart", func(t *testing.T) {
		api := NewTerminalsAPI(authManager)
		api.socketPath, _ = fakeSessiond(t, true)
		rec := rename(api, "abc", `{"name":"x"}`)
		AssertStatus(t, http.StatusNotImplemented, rec.Code)
		if !strings.Contains(rec.Body.String(), "stopping this installation's cg_sessiond process") {
			t.Fatalf("expected restart hint, got %s", rec.Body.String())
		}
	})
}

func TestTerminalOrder(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()
	admin := &Claims{Username: "admin", Role: RoleAdmin}
	newAPI := func() (*TerminalsAPI, *[]sessiondRequest) {
		api := NewTerminalsAPI(authManager)
		var received *[]sessiondRequest
		api.socketPath, received = fakeSessiond(t, false)
		api.orderPath = filepath.Join(t.TempDir(), "terminal_order.json")
		return api, received
	}
	listOrder := func(api *TerminalsAPI) []string {
		rec := httptest.NewRecorder()
		api.handleList(rec, httptest.NewRequest("GET", "/api/terminals", nil), admin, currentTerminalSettings())
		AssertStatus(t, http.StatusOK, rec.Code)
		var body struct {
			Data struct {
				Order []string `json:"order"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Data.Order
	}

	t.Run("KeepsOnlyVisibleIDsOnce", func(t *testing.T) {
		api, _ := newAPI()
		rec := httptest.NewRecorder()
		api.handleSaveOrder(rec, httptest.NewRequest("PUT", "/api/terminals/order", strings.NewReader(`{"ids":["zzz","abc","abc"]}`)), admin)
		AssertStatus(t, http.StatusOK, rec.Code)
		if got := listOrder(api); len(got) != 1 || got[0] != "abc" {
			t.Fatalf("expected order [abc], got %v", got)
		}
	})

	t.Run("NoSavedOrderIsEmpty", func(t *testing.T) {
		api, _ := newAPI()
		if got := listOrder(api); len(got) != 0 {
			t.Fatalf("expected no order, got %v", got)
		}
	})

	t.Run("OtherUsersSessionsAreNotStored", func(t *testing.T) {
		api, _ := newAPI()
		user := &Claims{Username: "bob", Role: "user"}
		rec := httptest.NewRecorder()
		api.handleSaveOrder(rec, httptest.NewRequest("PUT", "/api/terminals/order", strings.NewReader(`{"ids":["abc"]}`)), user)
		AssertStatus(t, http.StatusOK, rec.Code)
		if orders := api.loadTerminalOrders(); len(orders["bob"]) != 0 {
			t.Fatalf("bob must not store admin's session, got %v", orders)
		}
	})

	t.Run("RouteIsNotARename", func(t *testing.T) {
		api, received := newAPI()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("PUT", "/api/terminals/order", strings.NewReader(`{"ids":["abc"]}`))
		req.Header.Set("Authorization", "Bearer "+ts.AdminToken)
		api.Handle(rec, req)
		AssertStatus(t, http.StatusOK, rec.Code)
		for _, r := range *received {
			if r.Op == "rename" {
				t.Fatalf("PUT /api/terminals/order was sent to the daemon as a rename")
			}
		}
	})
}

func TestTerminalSessionLimit(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	t.Run("Validation", func(t *testing.T) {
		if err := validateTerminalsConfig(&TerminalsConfig{MaxSessionsPerUser: terminalMaxSessionsPerUserLimit}); err != nil {
			t.Fatalf("the limit itself must be accepted: %v", err)
		}
		for _, value := range []int{terminalMaxSessionsPerUserLimit + 1, -1} {
			if err := validateTerminalsConfig(&TerminalsConfig{MaxSessionsPerUser: value}); err == nil {
				t.Fatalf("%d must be refused", value)
			}
		}
	})

	t.Run("ImageRetentionValidation", func(t *testing.T) {
		if err := validateTerminalsConfig(&TerminalsConfig{ImageRetentionDays: terminalImageRetentionMaxDays}); err != nil {
			t.Fatalf("the limit itself must be accepted: %v", err)
		}
		for _, value := range []int{terminalImageRetentionMaxDays + 1, -1} {
			if err := validateTerminalsConfig(&TerminalsConfig{ImageRetentionDays: value}); err == nil {
				t.Fatalf("%d must be refused", value)
			}
		}
	})

	getConfig := func(t *testing.T) map[string]interface{} {
		resp := ts.DoAuthRequest(t, "GET", "/api/config", nil)
		defer resp.Body.Close()
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		var body struct {
			Data map[string]interface{} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.Data
	}

	t.Run("SavingKeepsOtherTerminalKeysAndAppliesAtOnce", func(t *testing.T) {
		config := getConfig(t)
		disabled := false
		config["terminals"] = map[string]interface{}{"max_sessions_per_user": 37, "idle_kill_hours": 5, "images_enabled": disabled, "image_retention_days": 30}
		resp := ts.DoAuthRequest(t, "PUT", "/api/config", config)
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()

		settings := currentTerminalSettings()
		if settings.maxPerUser != 37 || settings.imagesEnabled || settings.idleKill.Hours() != 5 || settings.imageRetention != 30*24*time.Hour {
			t.Fatalf("settings after save = %+v", settings)
		}
		saved := getConfig(t)["terminals"].(map[string]interface{})
		if saved["max_sessions_per_user"] != float64(37) || saved["idle_kill_hours"] != float64(5) || saved["images_enabled"] != false {
			t.Fatalf("terminals block after save = %v", saved)
		}

		// Cleared in the form: both go back to their defaults.
		config["terminals"] = map[string]interface{}{"idle_kill_hours": 5}
		resp = ts.DoAuthRequest(t, "PUT", "/api/config", config)
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
		settings = currentTerminalSettings()
		if settings.maxPerUser != terminalDefaultMaxSessionsPerUser || settings.imageRetention != terminalImageRetentionDefault {
			t.Fatalf("settings after clearing = %+v", settings)
		}
	})

	t.Run("BadValueIsRefusedAndNotWritten", func(t *testing.T) {
		before, err := os.ReadFile(cache.configFile)
		if err != nil {
			t.Fatal(err)
		}
		config := getConfig(t)
		config["terminals"] = map[string]interface{}{"max_sessions_per_user": terminalMaxSessionsPerUserLimit + 1}
		resp := ts.DoAuthRequest(t, "PUT", "/api/config", config)
		AssertStatus(t, http.StatusBadRequest, resp.StatusCode)
		resp.Body.Close()
		after, err := os.ReadFile(cache.configFile)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("a refused value changed config.json")
		}
	})

	t.Run("CreateIsRefusedAtTheLimit", func(t *testing.T) {
		sshHost, _ := terminalTestHosts(t)
		api := NewTerminalsAPI(authManager)
		api.socketPath, _ = fakeSessiond(t, false) // Lists one running session owned by admin
		settings := currentTerminalSettings()
		settings.maxPerUser = 1
		admin := &Claims{Username: "admin", Role: RoleAdmin}
		rec := httptest.NewRecorder()
		api.handleCreate(rec, httptest.NewRequest("POST", "/api/terminals", strings.NewReader(`{"host_ref":"`+sshHost+`"}`)), admin, settings)
		AssertStatus(t, http.StatusConflict, rec.Code)
		if !strings.Contains(rec.Body.String(), "Session limit reached") {
			t.Fatalf("body = %s", rec.Body.String())
		}
	})
}
