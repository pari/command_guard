package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

// TestServer wraps the HTTP server for testing
type TestServer struct {
	Server         *httptest.Server
	ConfigPath     string
	TempDir        string
	BaseURL        string
	AdminToken     string // JWT token for authenticated requests
	HasRealServers bool
}

// NewTestServer creates a new test server with real SSH/WinRM servers
// testVNCAPI is the Remote Desktops API of the most recent test server (vnc_test.go).
var testVNCAPI *VNCAPI

func NewTestServer(t *testing.T) *TestServer {
	// Check if we should use custom test config or the default one
	var configPath string
	tempDir := t.TempDir()
	var useExistingConfig bool

	// Check for test_env.json to load real server details
	if _, err := os.Stat("test_env.json"); err == nil {
		// Use real test environment config
		configPath = filepath.Join(tempDir, "test_config_real.json")
		if err := createRealTestConfig(configPath); err != nil {
			t.Fatalf("Failed to create real test config: %v", err)
		}
		useExistingConfig = true
	} else {
		// Fall back to the provided test_config.json if it exists
		if _, err := os.Stat("test_config.json"); err == nil {
			sourceConfig := "test_config.json"
			configPath = filepath.Join(tempDir, "test_config.json")
			data, readErr := os.ReadFile(sourceConfig)
			if readErr != nil {
				t.Fatalf("Failed to read test config: %v", readErr)
			}
			if writeErr := os.WriteFile(configPath, data, 0644); writeErr != nil {
				t.Fatalf("Failed to isolate test config: %v", writeErr)
			}
			useExistingConfig = true
			log.Println("Using test_config.json - Update SSH/WinRM server details for your environment")
		} else {
			t.Fatalf("No test configuration found. Copy test_env.example.json to test_env.json and configure your test servers")
		}
	}

	// Create temp directory for test artifacts
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Create logs directory
	logsDir := filepath.Join(tempDir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("Failed to create logs dir: %v", err)
	}

	// Create PEM files directory
	pemDir := filepath.Join(tempDir, "pem_files")
	if err := os.MkdirAll(pemDir, 0755); err != nil {
		t.Fatalf("Failed to create pem_files dir: %v", err)
	}

	// Create mail queue directory
	mailQueueDir := filepath.Join(tempDir, "mail_queue")
	if err := os.MkdirAll(mailQueueDir, 0755); err != nil {
		t.Fatalf("Failed to create mail_queue dir: %v", err)
	}

	// Initialize the application components
	initializeTestApp(configPath, tempDir)

	// Create HTTP test server
	mux := setupTestRoutes()
	server := httptest.NewServer(mux)

	ts := &TestServer{
		Server:         server,
		ConfigPath:     configPath,
		TempDir:        tempDir,
		BaseURL:        server.URL,
		HasRealServers: useExistingConfig && filepath.Base(configPath) == "test_config_real.json",
	}

	// Authenticate and get JWT token
	ts.authenticate(t)

	// Log test server info
	t.Logf("Test server started at: %s", ts.BaseURL)
	t.Logf("Using config: %s", configPath)
	if !useExistingConfig {
		t.Logf("Using mock servers - configure test_env.json for real servers")
	}

	return ts
}

// authenticate signs in with password and TOTP in a single request to get a JWT token
func (ts *TestServer) authenticate(t *testing.T) {
	loginReq := map[string]string{"password": "testpassword123", "totp_code": "123456"}
	resp := ts.DoRequest(t, "POST", "/api/auth/login", loginReq, nil)
	defer resp.Body.Close()

	var loginResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
		t.Fatalf("Failed to decode login response: %v", err)
	}

	token, ok := loginResp["token"].(string)
	if !ok {
		t.Fatalf("No token in login response: %v", loginResp)
	}

	ts.AdminToken = token
}

// Close cleans up the test server
func (ts *TestServer) Close() {
	ts.Server.Close()
}

// DoRequest performs an HTTP request to the test server
func (ts *TestServer) DoRequest(t *testing.T, method, path string, body interface{}, headers map[string]string) *http.Response {
	var bodyReader io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("Failed to marshal request body: %v", err)
		}
		bodyReader = bytes.NewReader(jsonData)
	}

	req, err := http.NewRequest(method, ts.BaseURL+path, bodyReader)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	// Set default headers
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// Set custom headers
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	return resp
}

// DoAuthRequest performs an authenticated request with JWT token
func (ts *TestServer) DoAuthRequest(t *testing.T, method, path string, body interface{}) *http.Response {
	headers := map[string]string{
		"Authorization": "Bearer " + ts.AdminToken,
	}
	return ts.DoRequest(t, method, path, body, headers)
}

// DoCommandRequest performs a command execution request
func (ts *TestServer) DoCommandRequest(t *testing.T, cmdID string, bearerToken string) *http.Response {
	headers := make(map[string]string)
	if bearerToken != "" {
		headers["Authorization"] = "Bearer " + bearerToken
	} else if ts.AdminToken != "" {
		headers["Authorization"] = "Bearer " + ts.AdminToken
	}
	if !ts.HasRealServers {
		// The placeholder hosts in test_config.json are unreachable: the request
		// either fails fast (HTTP 500) or hangs until the SSH connect times out.
		// Both mean "no test servers", which is a skip, not a failure.
		req, err := http.NewRequest("GET", ts.BaseURL+"/api/commands/"+cmdID, nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			t.Skip("remote execution requires test_env.json with reachable SSH/WinRM test servers")
		}
		if resp.StatusCode == http.StatusInternalServerError {
			resp.Body.Close()
			t.Skip("remote execution requires test_env.json with reachable SSH/WinRM test servers")
		}
		return resp
	}
	resp := ts.DoRequest(t, "GET", "/api/commands/"+cmdID, nil, headers)
	if !ts.HasRealServers && resp.StatusCode == http.StatusInternalServerError {
		resp.Body.Close()
		t.Skip("remote execution requires test_env.json with reachable SSH/WinRM test servers")
	}
	return resp
}

// UpdateConfig updates the test configuration
func (ts *TestServer) UpdateConfig(t *testing.T, config map[string]interface{}) {
	configData, err := json.MarshalIndent(config, "", "\t")
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	if err := ioutil.WriteFile(ts.ConfigPath, configData, 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	if err := cache.loadIfChanged(); err != nil {
		t.Fatalf("Failed to hot reload test config: %v", err)
	}

	// Give time for hot-reload
	time.Sleep(100 * time.Millisecond)
}

// GetConfig reads the current configuration
func (ts *TestServer) GetConfig(t *testing.T) map[string]interface{} {
	data, err := ioutil.ReadFile(ts.ConfigPath)
	if err != nil {
		t.Fatalf("Failed to read config: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("Failed to unmarshal config: %v", err)
	}

	return config
}

// AssertStatus checks if response has expected status code
func AssertStatus(t *testing.T, expected, actual int) {
	if expected != actual {
		t.Errorf("Expected status %d, got %d", expected, actual)
	}
}

// AssertJSONResponse decodes and returns JSON response
func AssertJSONResponse(t *testing.T, resp *http.Response) map[string]interface{} {
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to unmarshal response: %v\nBody: %s", err, string(body))
	}

	return result
}

// AssertSuccess checks if API response indicates success
func AssertSuccess(t *testing.T, response map[string]interface{}) {
	success, ok := response["success"].(bool)
	if !ok || !success {
		t.Errorf("Expected success response, got: %v", response)
	}
}

// AssertError checks if API response indicates error
func AssertError(t *testing.T, response map[string]interface{}) {
	success, ok := response["success"].(bool)
	if !ok || success {
		t.Errorf("Expected error response, got: %v", response)
	}
}

// createRealTestConfig creates test config from test_env.json
func createRealTestConfig(outputPath string) error {
	// Read test_env.json
	envData, err := ioutil.ReadFile("test_env.json")
	if err != nil {
		return fmt.Errorf("failed to read test_env.json: %v", err)
	}

	var testEnv map[string]interface{}
	if err := json.Unmarshal(envData, &testEnv); err != nil {
		return fmt.Errorf("failed to parse test_env.json: %v", err)
	}

	// Read base test_config.json as template
	configData, err := ioutil.ReadFile("test_config.json")
	if err != nil {
		return fmt.Errorf("failed to read test_config.json: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(configData, &config); err != nil {
		return fmt.Errorf("failed to parse test_config.json: %v", err)
	}

	// Update hosts with real server details from test_env.json
	if servers, ok := testEnv["test_servers"].(map[string]interface{}); ok {
		hosts := config["hosts"].(map[string]interface{})

		// Update SSH server details
		if sshServer, ok := servers["ssh"].(map[string]interface{}); ok {
			if sshHost, ok := hosts["test-ssh-server"].(map[string]interface{}); ok {
				sshHost["host"] = sshServer["host"]
				sshHost["port"] = sshServer["port"]
				sshHost["username"] = sshServer["username"]
				sshHost["password"] = sshServer["password"]
			}

			// Update SSH key-based host
			if sshHostKey, ok := hosts["test-ssh-server-key"].(map[string]interface{}); ok {
				sshHostKey["host"] = sshServer["host"]
				sshHostKey["port"] = sshServer["port"]
				sshHostKey["username"] = sshServer["username"]
				if pemPath, ok := sshServer["pem_file_path"].(string); ok && pemPath != "" {
					sshHostKey["pem_file"] = pemPath
				}
			}
		}

		// Update WinRM server details
		if winrmServer, ok := servers["winrm"].(map[string]interface{}); ok {
			if winrmHost, ok := hosts["test-winrm-server"].(map[string]interface{}); ok {
				winrmHost["host"] = winrmServer["host"]
				winrmHost["port"] = winrmServer["port"]
				winrmHost["username"] = winrmServer["username"]
				winrmHost["password"] = winrmServer["password"]
			}
		}
	}

	// Update SMTP if provided
	if smtp, ok := testEnv["smtp"].(map[string]interface{}); ok {
		if smtpConfig, ok := config["smtp"].(map[string]interface{}); ok {
			if host, ok := smtp["host"].(string); ok && host != "" {
				smtpConfig["host"] = host
				smtpConfig["port"] = smtp["port"]
				smtpConfig["username"] = smtp["username"]
				smtpConfig["password"] = smtp["password"]
			}
		}
	}

	// Write the updated config
	outputData, err := json.MarshalIndent(config, "", "\t")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %v", err)
	}

	if err := ioutil.WriteFile(outputPath, outputData, 0644); err != nil {
		return fmt.Errorf("failed to write config: %v", err)
	}

	return nil
}

// createTestConfig creates a basic test configuration (deprecated - use test_config.json)
func createTestConfig() map[string]interface{} {
	return map[string]interface{}{
		"port":           8080,
		"bind_address":   "127.0.0.1",
		"access_url":     "http://localhost:8080",
		"cron_enabled":   "N", // Disable cron for most tests
		"admin_password": "testpassword123",
		"totp_secret":    "JBSWY3DPEHPK3PXP", // Test secret
		"variables": map[string]interface{}{
			"test_var": "test_value",
		},
		"smtp": map[string]interface{}{
			"host":     "smtp.test.com",
			"port":     587,
			"username": "test@test.com",
			"password": "testpass",
			"from":     "Test <test@test.com>",
			"tls":      true,
		},
		"hosts": map[string]interface{}{
			"test-ssh-host": map[string]interface{}{
				"connection_type": "ssh",
				"host":            "localhost",
				"port":            22,
				"username":        "testuser",
				"password":        "testpass",
			},
			"test-winrm-host": map[string]interface{}{
				"connection_type": "winrm",
				"host":            "winserver.test",
				"port":            5986,
				"username":        "Administrator",
				"password":        "testpass",
				"winrm_https":     true,
				"winrm_insecure":  true,
			},
		},
		"commands": []interface{}{
			map[string]interface{}{
				"cmd_id":          "test-echo",
				"host_ref":        "test-ssh-host",
				"command":         "echo 'test'",
				"description":     "Test echo command",
				"bearer_token":    "test-token-123",
				"max_output_size": "1MB",
			},
			map[string]interface{}{
				"cmd_id":        "test-scheduled",
				"host_ref":      "test-ssh-host",
				"command":       "date",
				"description":   "Test scheduled command",
				"cron_schedule": "*/5 * * * *",
			},
		},
		"runbooks": []interface{}{
			map[string]interface{}{
				"runbook_id":   "test-runbook",
				"description":  "Test RunBook",
				"bearer_token": "runbook-token-123",
				"steps": []interface{}{
					map[string]interface{}{
						"step_id": "echo",
						"cmd_id":  "test-echo",
					},
				},
			},
		},
	}
}

// initializeTestApp initializes the application for testing
func initializeTestApp(configPath, tempDir string) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		panic(err)
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		panic(err)
	}
	// The historical API suite uses test-ssh-host while the checked-in real-server
	// template calls the same host test-ssh-server. Keep both labels in each
	// isolated copy, and point the optional key host at an existing placeholder so
	// hot reload can exercise API mutations without requiring private test keys.
	if _, ok := config.Hosts["test-ssh-host"]; !ok {
		if host, exists := config.Hosts["test-ssh-server"]; exists {
			host.Password = "testpass"
			config.Hosts["test-ssh-host"] = host
		}
	}
	if host, ok := config.Hosts["test-ssh-server-key"]; ok {
		placeholder := filepath.Join(tempDir, "test-key.pem")
		_ = os.WriteFile(placeholder, []byte("test-only-placeholder"), 0600)
		host.PemFile = placeholder
		config.Hosts["test-ssh-server-key"] = host
	}
	var configBuffer bytes.Buffer
	encoder := json.NewEncoder(&configBuffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "\t")
	if err := encoder.Encode(config); err != nil {
		panic(err)
	}
	if err := os.WriteFile(configPath, configBuffer.Bytes(), 0644); err != nil {
		panic(err)
	}
	hash, err := calculateFileHash(configPath)
	if err != nil {
		panic(err)
	}
	// Mirror loadIfChanged: attach resolved bastions so API tests exercise the real path.
	if err := resolveBastionHosts(config.Hosts); err != nil {
		panic(err)
	}
	cache.Lock()
	cache.configFile = configPath
	cache.config = &config
	cache.lastHash = hash
	cache.Unlock()
	if cronScheduler != nil {
		cronScheduler.Stop()
	}
	cronScheduler = cron.New()
	scheduledJobs = make(map[string]cron.EntryID)
	scheduledJobsOverviewEmailEntryID = 0
	cronScheduler.Start()
	updateCronJobs(&config)
	authManager = NewAuthManager(&AuthConfig{AdminPassword: config.AdminPassword, TOTPSecret: config.TOTPSecret, JWTSecret: "test-jwt-secret-for-http-suite", DevMode: true, Users: config.Users, TrustedProxies: config.TrustedProxies}, configPath)
	serverStartTime = time.Now()
}

// setupTestRoutes sets up HTTP routes for testing
func setupTestRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	configAPI := NewConfigAPI(cache.configFile, authManager)
	mux.HandleFunc("/api/auth/login", authManager.HandleLogin)
	mux.HandleFunc("/api/auth/verify-2fa", authManager.HandleVerify2FA)
	mux.HandleFunc("/api/auth/complete-onboarding", authManager.HandleCompleteOnboarding)
	mux.HandleFunc("/api/auth/validate", authManager.HandleValidate)
	mux.HandleFunc("/api/status", authManager.AdminMiddleware(statusHandler))
	mux.HandleFunc("/api/commands/", authManager.CommandMiddleware(execHandler))
	mux.HandleFunc("/api/http-requests/", authManager.HTTPRequestMiddleware(httpRequestExecHandler))
	mux.HandleFunc("/api/runbooks/", runBookExecutionHandler)
	mux.HandleFunc("/api/overview/scheduled-jobs", authManager.AuthMiddleware(overviewHandler))
	mux.HandleFunc("/api/users", authManager.AdminMiddleware(authManager.HandleUsers))
	mux.HandleFunc("/api/users/", authManager.AdminMiddleware(authManager.HandleUsers))
	mux.HandleFunc("/api/account/password", authManager.AuthMiddleware(authManager.HandleAccountPassword))
	mux.HandleFunc("/api/user/commands", authManager.AuthMiddleware(authManager.HandleAssignedCommands))
	mux.HandleFunc("/api/user/runbooks", authManager.AuthMiddleware(authManager.HandleAssignedRunBooks))
	mux.HandleFunc("/api/user/commands/", authManager.AuthMiddleware(authManager.HandleUserSchedule))
	mux.HandleFunc("/api/user/runbooks/", authManager.AuthMiddleware(authManager.HandleUserSchedule))
	mux.HandleFunc("/api/user/http-requests", authManager.AuthMiddleware(authManager.HandleAssignedHTTPRequests))
	mux.HandleFunc("/api/user/http-requests/", authManager.AuthMiddleware(authManager.HandleUserSchedule))
	// Tests must never reach a real cg_sessiond (it would open real SSH sessions):
	// point at a socket that does not exist and never auto-start the daemon.
	terminalsAPI := NewTerminalsAPI(authManager)
	terminalsAPI.config = configAPI
	terminalsAPI.socketPath = filepath.Join(os.TempDir(), "cg_sessiond_test_absent.sock")
	terminalsAPI.agentPath = filepath.Join(filepath.Dir(cache.configFile), "terminal_agent_sessions.json")
	terminalsAPI.metaPath = filepath.Join(filepath.Dir(cache.configFile), "terminal_meta.json")
	terminalDaemonAutoStart = false
	mux.HandleFunc("/api/terminals", terminalsAPI.Handle)
	mux.HandleFunc("/api/terminals/", terminalsAPI.Handle)
	if testVNCAPI != nil {
		close(testVNCAPI.stop)
	}
	testVNCAPI = NewVNCAPI(authManager)
	mux.HandleFunc("/api/vnc", testVNCAPI.Handle)
	mux.HandleFunc("/api/vnc/", testVNCAPI.Handle)
	mux.HandleFunc("/api/config/vnc-connections", authManager.AdminMiddleware(configAPI.HandleVNCConnections))
	mux.HandleFunc("/api/config/vnc-connections/", authManager.AdminMiddleware(configAPI.HandleVNCConnections))
	mux.HandleFunc("/api/config", authManager.AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			configAPI.HandleGetConfig(w, r)
		} else if r.Method == http.MethodPut {
			configAPI.HandleUpdateConfig(w, r)
		} else {
			sendJSONError(w, 405, "Method not allowed")
		}
	}))
	mux.HandleFunc("/api/config/hosts", authManager.AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			configAPI.HandleGetHosts(w, r)
		} else if r.Method == http.MethodPost {
			configAPI.HandleCreateHost(w, r)
		} else {
			sendJSONError(w, 405, "Method not allowed")
		}
	}))
	mux.HandleFunc("/api/config/hosts/", authManager.AdminMiddleware(configAPI.HandleHostItem))
	mux.HandleFunc("/api/config/commands", authManager.AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			configAPI.HandleGetCommands(w, r)
		} else if r.Method == http.MethodPost {
			configAPI.HandleCreateCommand(w, r)
		} else {
			sendJSONError(w, 405, "Method not allowed")
		}
	}))
	mux.HandleFunc("/api/config/commands/", authManager.AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			configAPI.HandleUpdateCommand(w, r)
		} else if r.Method == http.MethodDelete {
			configAPI.HandleDeleteCommand(w, r)
		} else {
			sendJSONError(w, 405, "Method not allowed")
		}
	}))
	mux.HandleFunc("/api/config/runbooks", authManager.AdminMiddleware(configAPI.HandleRunBooks))
	mux.HandleFunc("/api/config/runbooks/", authManager.AdminMiddleware(configAPI.HandleRunBooks))
	mux.HandleFunc("/api/config/http-requests", authManager.AdminMiddleware(configAPI.HandleHTTPRequests))
	mux.HandleFunc("/api/config/http-requests/", authManager.AdminMiddleware(configAPI.HandleHTTPRequests))
	mux.HandleFunc("/api/config/disk-monitor", authManager.AdminMiddleware(configAPI.HandleDiskMonitorConfig))
	mux.HandleFunc("/api/config/disk-monitor/", authManager.AdminMiddleware(configAPI.HandleDiskMonitorConfig))
	mux.HandleFunc("/api/disk-monitor", authManager.AdminMiddleware(handleDiskMonitorOps))
	mux.HandleFunc("/api/disk-monitor/", authManager.AdminMiddleware(handleDiskMonitorOps))
	mux.HandleFunc("/api/config/variables", authManager.AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			configAPI.HandleGetVariables(w, r)
		} else if r.Method == http.MethodPut {
			configAPI.HandleUpdateVariables(w, r)
		} else {
			sendJSONError(w, 405, "Method not allowed")
		}
	}))
	mux.HandleFunc("/api/config/smtp", authManager.AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			configAPI.HandleGetSMTP(w, r)
		} else if r.Method == http.MethodPut {
			configAPI.HandleUpdateSMTP(w, r)
		} else {
			sendJSONError(w, 405, "Method not allowed")
		}
	}))
	mux.HandleFunc("/api/config/smtp/test-email", authManager.AdminMiddleware(configAPI.HandleSendSMTPTestEmail))
	mux.HandleFunc("/api/config/scheduled-jobs-overview-email", authManager.AdminMiddleware(configAPI.HandleScheduledJobsOverviewEmail))
	mux.HandleFunc("/api/logs", authManager.AuthMiddleware(configAPI.HandleGetLogs))
	mux.HandleFunc("/api/logs/clear-old", authManager.AdminMiddleware(configAPI.HandleClearOldLogs))
	mux.HandleFunc("/api/logs/delete/", authManager.AdminMiddleware(configAPI.HandleDeleteLog))
	mux.HandleFunc("/api/logs/", authManager.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			configAPI.HandleLogContent(w, r)
		} else if strings.HasSuffix(r.URL.Path, "/download") {
			configAPI.HandleLogDownload(w, r)
		} else {
			logsHandler(w, r)
		}
	}))
	pemAPI := NewPemFilesAPI()
	mux.HandleFunc("/api/pemfiles", authManager.AdminMiddleware(pemAPI.HandlePemFiles))
	mux.HandleFunc("/api/pemfiles/", authManager.AdminMiddleware(pemAPI.HandlePemFiles))
	return mux
}

// WaitForCondition waits for a condition to be true with timeout
func WaitForCondition(t *testing.T, condition func() bool, timeout time.Duration, message string) {
	start := time.Now()
	for {
		if condition() {
			return
		}
		if time.Since(start) > timeout {
			t.Fatalf("Timeout waiting for: %s", message)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
