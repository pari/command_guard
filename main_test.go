package main

import (
	"io/ioutil"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestAuthenticationFlow tests the complete authentication flow
func TestAuthenticationFlow(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	t.Run("Login_WithValidCredentialsAndTOTP", func(t *testing.T) {
		loginReq := map[string]string{"password": "testpassword123", "totp_code": "123456"}
		resp := ts.DoRequest(t, "POST", "/api/auth/login", loginReq, nil)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)

		body := AssertJSONResponse(t, resp)
		if _, ok := body["token"]; !ok {
			t.Error("Expected JWT token in response")
		}
		if _, ok := body["temp_token"]; ok {
			t.Error("Enrolled account must not receive a temp_token")
		}
	})

	t.Run("Login_WithInvalidPassword", func(t *testing.T) {
		loginReq := map[string]string{"password": "wrongpassword", "totp_code": "123456"}
		resp := ts.DoRequest(t, "POST", "/api/auth/login", loginReq, nil)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("Login_WithoutTOTP", func(t *testing.T) {
		loginReq := map[string]string{"password": "testpassword123"}
		resp := ts.DoRequest(t, "POST", "/api/auth/login", loginReq, nil)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusUnauthorized, resp.StatusCode)
		body := AssertJSONResponse(t, resp)
		if body["error"] != genericLoginError {
			t.Errorf("Expected generic login error, got %v", body["error"])
		}
	})

	t.Run("ValidateToken", func(t *testing.T) {
		resp := ts.DoAuthRequest(t, "GET", "/api/auth/validate", nil)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)

		body := AssertJSONResponse(t, resp)
		AssertSuccess(t, body)
	})

	t.Run("AccessProtectedEndpoint_WithoutToken", func(t *testing.T) {
		resp := ts.DoRequest(t, "GET", "/api/status", nil, nil)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("AccessProtectedEndpoint_WithToken", func(t *testing.T) {
		resp := ts.DoAuthRequest(t, "GET", "/api/status", nil)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)
	})
}

// TestCommandExecution tests command execution APIs
func TestCommandExecution(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	t.Run("ExecuteCommand_WithValidToken", func(t *testing.T) {
		resp := ts.DoCommandRequest(t, "test-echo", "test-token-123")
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)

		body := AssertJSONResponse(t, resp)
		if body["trace_id"] == nil {
			t.Error("Expected trace_id in response")
		}
	})

	t.Run("ExecuteCommand_WithInvalidToken", func(t *testing.T) {
		resp := ts.DoCommandRequest(t, "test-echo", "wrong-token")
		defer resp.Body.Close()

		AssertStatus(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("ExecuteCommand_NonExistent", func(t *testing.T) {
		resp := ts.DoCommandRequest(t, "non-existent-cmd", "")
		defer resp.Body.Close()

		AssertStatus(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("ExecuteCommand_OutputTruncation", func(t *testing.T) {
		// Add a command with small max_output_size
		config := ts.GetConfig(t)
		commands := config["commands"].([]interface{})
		commands = append(commands, map[string]interface{}{
			"cmd_id":          "test-large-output",
			"host_ref":        "test-ssh-host",
			"command":         "for i in {1..1000}; do echo \"Line $i\"; done",
			"max_output_size": "100B",
		})
		config["commands"] = commands
		ts.UpdateConfig(t, config)

		resp := ts.DoCommandRequest(t, "test-large-output", "")
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)

		body := AssertJSONResponse(t, resp)
		if truncated, ok := body["is_output_truncated"].(float64); !ok || truncated != 1 {
			t.Error("Expected output to be truncated")
		}
	})
}

// TestRunBookValidation checks the ordered workflow schema without remote execution.
func TestRunBookValidation(t *testing.T) {
	commandIDs := map[string]bool{"first": true, "second": true}
	runBooks := []RunBook{{RunBookID: "test-runbook", Steps: []RunBookStep{{StepID: "one", CmdID: "first"}, {StepID: "two", CmdID: "second", OnFailure: "continue"}}}}
	if err := validateRunBooks(runBooks, commandIDs, nil); err != nil {
		t.Fatalf("valid RunBook rejected: %v", err)
	}
	runBooks[0].Steps[1].CmdID = "missing"
	if err := validateRunBooks(runBooks, commandIDs, nil); err == nil {
		t.Fatal("expected missing Command reference to fail")
	}

	generated := RunBook{RunBookID: "generated-steps", Steps: []RunBookStep{
		{StepID: "existing", CmdID: "first"},
		{CmdID: "second"},
		{CmdID: "first"},
	}}
	assignMissingRunBookStepIDs(&generated)
	if generated.Steps[0].StepID != "existing" {
		t.Fatal("existing step ID was changed")
	}
	if generated.Steps[1].StepID == generated.Steps[2].StepID {
		t.Fatal("generated step IDs must be unique")
	}
	for _, step := range generated.Steps[1:] {
		if len(step.StepID) != 10 || !strings.HasPrefix(step.StepID, "s-") || !isValidCommandID(step.StepID) {
			t.Fatalf("generated step ID has unexpected format: %q", step.StepID)
		}
	}
	if err := validateRunBooks([]RunBook{generated}, commandIDs, nil); err != nil {
		t.Fatalf("RunBook with generated step IDs rejected: %v", err)
	}

	generated.Steps[2].StepID = generated.Steps[1].StepID
	if err := validateRunBooks([]RunBook{generated}, commandIDs, nil); err == nil {
		t.Fatal("expected duplicate generated step ID to fail validation")
	}
}

// TestConfigManagement tests configuration management APIs
func TestConfigManagement(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	t.Run("GetConfig", func(t *testing.T) {
		resp := ts.DoAuthRequest(t, "GET", "/api/config", nil)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)

		body := AssertJSONResponse(t, resp)
		AssertSuccess(t, body)

		if data, ok := body["data"].(map[string]interface{}); ok {
			if data["hosts"] == nil || data["commands"] == nil {
				t.Error("Expected hosts and commands in config")
			}
		} else {
			t.Error("Expected data in response")
		}
	})

	t.Run("CreateHost", func(t *testing.T) {
		hostData := map[string]interface{}{
			"name":            "new-test-host",
			"connection_type": "ssh",
			"host":            "newhost.test",
			"port":            22,
			"username":        "newuser",
			"password":        "newpass",
		}

		resp := ts.DoAuthRequest(t, "POST", "/api/config/hosts", hostData)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusCreated, resp.StatusCode)

		body := AssertJSONResponse(t, resp)
		AssertSuccess(t, body)
	})

	t.Run("UpdateHost", func(t *testing.T) {
		hostData := map[string]interface{}{
			"connection_type": "ssh",
			"host":            "updated.test",
			"port":            2222,
			"username":        "updateduser",
		}

		resp := ts.DoAuthRequest(t, "PUT", "/api/config/hosts/test-ssh-host", hostData)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)

		body := AssertJSONResponse(t, resp)
		AssertSuccess(t, body)

		// Verify password was preserved
		config := ts.GetConfig(t)
		hosts := config["hosts"].(map[string]interface{})
		host := hosts["test-ssh-host"].(map[string]interface{})
		if host["password"] != "testpass" {
			t.Error("Password was not preserved during update")
		}
	})

	t.Run("DeleteHost", func(t *testing.T) {
		// First create a host to delete
		hostData := map[string]interface{}{
			"name":            "host-to-delete",
			"connection_type": "ssh",
			"host":            "delete.test",
			"port":            22,
			"username":        "user",
			"password":        "pass",
		}
		resp := ts.DoAuthRequest(t, "POST", "/api/config/hosts", hostData)
		resp.Body.Close()

		// Now delete it
		resp = ts.DoAuthRequest(t, "DELETE", "/api/config/hosts/host-to-delete", nil)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)

		body := AssertJSONResponse(t, resp)
		AssertSuccess(t, body)
	})

	t.Run("CreateCommand", func(t *testing.T) {
		cmdData := map[string]interface{}{
			"cmd_id":      "new-test-cmd",
			"host_ref":    "test-ssh-host",
			"command":     "ls -la",
			"description": "New test command",
		}

		resp := ts.DoAuthRequest(t, "POST", "/api/config/commands", cmdData)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusCreated, resp.StatusCode)

		body := AssertJSONResponse(t, resp)
		AssertSuccess(t, body)
	})

	t.Run("CreateCommandWithInvalidID", func(t *testing.T) {
		// Test with underscore (should fail)
		cmdData := map[string]interface{}{
			"cmd_id":      "invalid_cmd_id",
			"host_ref":    "test-ssh-host",
			"command":     "ls -la",
			"description": "Test command with invalid ID",
		}

		resp := ts.DoAuthRequest(t, "POST", "/api/config/commands", cmdData)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusBadRequest, resp.StatusCode)

		body := AssertJSONResponse(t, resp)
		AssertError(t, body)

		// Verify error message mentions underscores
		if errorMsg, ok := body["error"].(string); ok {
			if !strings.Contains(errorMsg, "Underscores") && !strings.Contains(errorMsg, "underscores") {
				t.Errorf("Expected error message about underscores, got: %s", errorMsg)
			}
		}

		// Test with other special characters (should also fail)
		cmdData["cmd_id"] = "invalid@cmd"
		resp = ts.DoAuthRequest(t, "POST", "/api/config/commands", cmdData)
		resp.Body.Close()
		AssertStatus(t, http.StatusBadRequest, resp.StatusCode)

		// Test with valid ID with hyphens (should succeed)
		cmdData["cmd_id"] = "valid-cmd-123"
		resp = ts.DoAuthRequest(t, "POST", "/api/config/commands", cmdData)
		defer resp.Body.Close()
		AssertStatus(t, http.StatusCreated, resp.StatusCode)
	})

	t.Run("UpdateCommand", func(t *testing.T) {
		cmdData := map[string]interface{}{
			"host_ref":    "test-ssh-host",
			"command":     "echo 'updated'",
			"description": "Updated test command",
		}

		resp := ts.DoAuthRequest(t, "PUT", "/api/config/commands/test-echo", cmdData)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)

		body := AssertJSONResponse(t, resp)
		AssertSuccess(t, body)
	})

	t.Run("DeleteCommand", func(t *testing.T) {
		// First create a command to delete
		cmdData := map[string]interface{}{
			"cmd_id":      "cmd-to-delete",
			"host_ref":    "test-ssh-host",
			"command":     "echo delete",
			"description": "Command to delete",
		}
		resp := ts.DoAuthRequest(t, "POST", "/api/config/commands", cmdData)
		resp.Body.Close()

		// Now delete it
		resp = ts.DoAuthRequest(t, "DELETE", "/api/config/commands/cmd-to-delete", nil)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)

		body := AssertJSONResponse(t, resp)
		AssertSuccess(t, body)
	})
}

// TestScheduledTasks tests cron scheduled tasks
func TestScheduledTasks(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	t.Run("ScheduledCommand", func(t *testing.T) {
		// Enable cron
		config := ts.GetConfig(t)
		config["cron_enabled"] = "Y"

		// Add a command that runs every second for testing
		commands := config["commands"].([]interface{})
		commands = append(commands, map[string]interface{}{
			"cmd_id":        "test-cron-cmd",
			"host_ref":      "test-ssh-host",
			"command":       "date",
			"cron_schedule": "* * * * *",
		})
		config["commands"] = commands
		ts.UpdateConfig(t, config)

		WaitForCondition(t, func() bool {
			scheduledJobsMutex.RLock()
			defer scheduledJobsMutex.RUnlock()
			_, ok := scheduledJobs["test-cron-cmd"]
			return ok
		}, 2*time.Second, "scheduled Command registration")
	})

	t.Run("ScheduledRunBook", func(t *testing.T) {
		config := ts.GetConfig(t)
		config["cron_enabled"] = "Y"

		runBooks := config["runbooks"].([]interface{})
		runBooks = append(runBooks, map[string]interface{}{
			"runbook_id":    "test-cron-runbook",
			"description":   "Test cron RunBook",
			"cron_schedule": "* * * * *",
			"steps": []interface{}{
				map[string]interface{}{
					"step_id": "echo",
					"cmd_id":  "test-echo",
				},
			},
		})
		config["runbooks"] = runBooks
		ts.UpdateConfig(t, config)

		// Configuration reload validates and registers the scheduled RunBook.
	})
}

// TestStatusEndpoint tests the status monitoring endpoint
func TestStatusEndpoint(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	t.Run("GetStatus", func(t *testing.T) {
		resp := ts.DoAuthRequest(t, "GET", "/api/status", nil)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)

		body := AssertJSONResponse(t, resp)
		data, _ := body["data"].(map[string]interface{})

		// Check for expected fields
		for _, field := range []string{"version", "server_uptime", "total_requests_handled", "active_requests", "locked_commands", "cron_enabled", "config_last_updated"} {
			if data[field] == nil {
				t.Errorf("Expected field '%s' in status response", field)
			}
		}
	})
}

// TestLogRetrieval tests log retrieval endpoints
func TestLogRetrieval(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	t.Run("ListLogs", func(t *testing.T) {
		// Execute a command to generate a log
		resp := ts.DoCommandRequest(t, "test-echo", "test-token-123")
		respBody := AssertJSONResponse(t, resp)
		resp.Body.Close()

		traceID := respBody["trace_id"].(string)

		// List logs
		resp = ts.DoAuthRequest(t, "GET", "/api/logs", nil)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)

		body := AssertJSONResponse(t, resp)
		AssertSuccess(t, body)

		if logs, ok := body["data"].([]interface{}); ok {
			found := false
			for _, log := range logs {
				if logMap, ok := log.(map[string]interface{}); ok {
					if logMap["trace_id"] == traceID {
						found = true
						break
					}
				}
			}
			if !found {
				t.Error("Expected to find log entry for executed command")
			}
		}
	})

	t.Run("GetLogByTraceID", func(t *testing.T) {
		// Execute a command to generate a log
		resp := ts.DoCommandRequest(t, "test-echo", "test-token-123")
		respBody := AssertJSONResponse(t, resp)
		resp.Body.Close()

		traceID := respBody["trace_id"].(string)

		// Get log by trace ID
		resp = ts.DoAuthRequest(t, "GET", "/api/logs/"+traceID, nil)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)

		// Log endpoint returns plain text, not JSON
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("Failed to read log body: %v", err)
		}

		if !strings.Contains(string(body), traceID) {
			t.Error("Log content doesn't contain trace ID")
		}
	})
}

// Multi-command execution is covered by RunBook validation and integration tests.

// TestVariableSubstitution tests variable substitution in config
func TestVariableSubstitution(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	t.Run("ConfigVariables", func(t *testing.T) {
		config := ts.GetConfig(t)
		config["variables"] = map[string]interface{}{
			"test_host": "variable-host.test",
			"test_port": 2222,
		}

		// Add command using variables
		commands := config["commands"].([]interface{})
		commands = append(commands, map[string]interface{}{
			"cmd_id":      "var-test-cmd",
			"host_ref":    "test-ssh-host",
			"command":     "echo '{{test_host}}:{{test_port}}'",
			"description": "Test variable substitution",
		})
		config["commands"] = commands
		ts.UpdateConfig(t, config)

		// Execute command
		resp := ts.DoCommandRequest(t, "var-test-cmd", "")
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)
	})
}

// TestErrorHandling tests various error conditions
func TestErrorHandling(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	t.Run("InvalidJSON", func(t *testing.T) {
		headers := map[string]string{
			"Authorization": "Bearer " + ts.AdminToken,
			"Content-Type":  "application/json",
		}
		resp := ts.DoRequest(t, "POST", "/api/config/hosts", strings.NewReader("invalid json"), headers)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("DuplicateHostID", func(t *testing.T) {
		hostData := map[string]interface{}{
			"name":            "test-ssh-host", // Already exists
			"connection_type": "ssh",
			"host":            "duplicate.test",
			"port":            22,
			"username":        "user",
			"password":        "pass",
		}

		resp := ts.DoAuthRequest(t, "POST", "/api/config/hosts", hostData)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusConflict, resp.StatusCode)
	})

	t.Run("InvalidHostReference", func(t *testing.T) {
		cmdData := map[string]interface{}{
			"cmd_id":      "invalid-host-ref",
			"host_ref":    "non-existent-host",
			"command":     "echo test",
			"description": "Command with invalid host",
		}

		resp := ts.DoAuthRequest(t, "POST", "/api/config/commands", cmdData)
		defer resp.Body.Close()

		// A command that points at a missing host is refused: saving it would make
		// the loader reject the whole config and break every other command.
		AssertStatus(t, http.StatusBadRequest, resp.StatusCode)

		check := ts.DoAuthRequest(t, "GET", "/api/config/commands", nil)
		defer check.Body.Close()
		raw, _ := ioutil.ReadAll(check.Body)
		if strings.Contains(string(raw), "invalid-host-ref") {
			t.Fatalf("rejected command was persisted: %s", raw)
		}
	})
}
