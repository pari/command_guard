package main

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestIntegrationFullWorkflow tests a complete workflow scenario
// TestIntegrationRunBookDefinition covers the clean RunBook workflow contract.
func TestIntegrationRunBookDefinition(t *testing.T) {
	commands := map[string]bool{"workflow-backup": true, "workflow-transfer": true, "workflow-cleanup": true}
	runBook := RunBook{RunBookID: "workflow-backup", Steps: []RunBookStep{
		{StepID: "backup", CmdID: "workflow-backup", OnFailure: "stop"},
		{StepID: "transfer", CmdID: "workflow-transfer", DelayBeforeSeconds: 2, RetryCount: 1},
		{StepID: "cleanup", CmdID: "workflow-cleanup", OnFailure: "continue"},
	}}
	if err := validateRunBooks([]RunBook{runBook}, commands, nil); err != nil {
		t.Fatalf("RunBook definition should be valid: %v", err)
	}
}

func TestPEMFileManagement(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	pemContent := `-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEAtest...
-----END RSA PRIVATE KEY-----`

	t.Run("UploadPEMFile", func(t *testing.T) {
		// Create a test PEM file
		pemPath := filepath.Join(ts.TempDir, "test.pem")
		if err := ioutil.WriteFile(pemPath, []byte(pemContent), 0600); err != nil {
			t.Fatalf("Failed to create test PEM file: %v", err)
		}

		// Simulate file upload (would need multipart form in real implementation)
		pemDir := filepath.Join(ts.TempDir, "pem_files")
		destPath := filepath.Join(pemDir, "test-key.pem")

		// Copy file to pem_files directory
		input, err := ioutil.ReadFile(pemPath)
		if err != nil {
			t.Fatalf("Failed to read PEM file: %v", err)
		}

		if err := ioutil.WriteFile(destPath, input, 0600); err != nil {
			t.Fatalf("Failed to write PEM file: %v", err)
		}

		// Verify file exists
		if _, err := os.Stat(destPath); os.IsNotExist(err) {
			t.Error("PEM file was not created")
		}
	})

	t.Run("CreateHostWithPEMFile", func(t *testing.T) {
		hostData := map[string]interface{}{
			"name":            "pem-host",
			"connection_type": "ssh",
			"host":            "pemhost.test",
			"port":            22,
			"username":        "pemuser",
			"pem_file":        filepath.Join(ts.TempDir, "pem_files", "test-key.pem"),
		}

		resp := ts.DoAuthRequest(t, "POST", "/api/config/hosts", hostData)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusCreated, resp.StatusCode)
		body := AssertJSONResponse(t, resp)
		AssertSuccess(t, body)
	})

	t.Run("UpdateHostPEMFile", func(t *testing.T) {
		// First create another PEM file
		pemPath2 := filepath.Join(ts.TempDir, "pem_files", "test-key2.pem")
		if err := ioutil.WriteFile(pemPath2, []byte(pemContent), 0600); err != nil {
			t.Fatalf("Failed to create second PEM file: %v", err)
		}

		// Update host to use new PEM file
		hostData := map[string]interface{}{
			"connection_type": "ssh",
			"host":            "pemhost.test",
			"port":            22,
			"username":        "pemuser",
			"pem_file":        filepath.Join(ts.TempDir, "pem_files", "test-key2.pem"),
		}

		resp := ts.DoAuthRequest(t, "PUT", "/api/config/hosts/pem-host", hostData)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)
	})
}

// TestEmailNotifications tests email notification configuration
func TestEmailNotifications(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	t.Run("ConfigureEmailNotification", func(t *testing.T) {
		cmdData := map[string]interface{}{
			"cmd_id":      "email-test-cmd",
			"host_ref":    "test-ssh-host",
			"command":     "echo 'test email'",
			"description": "Command with email notification",
			"notify_email": map[string]interface{}{
				"to":               []string{"admin@test.com"},
				"cc":               []string{"manager@test.com"},
				"subject":          "{{status}} - {{cmd_id}} on {{host}}",
				"on_success":       true,
				"on_failure":       true,
				"include_output":   true,
				"max_output_lines": 50,
			},
		}

		resp := ts.DoAuthRequest(t, "POST", "/api/config/commands", cmdData)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusCreated, resp.StatusCode)
		body := AssertJSONResponse(t, resp)
		AssertSuccess(t, body)

		// Execute command to trigger email
		resp = ts.DoCommandRequest(t, "email-test-cmd", "")
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)

		// Check if mail queue directory has files (email would be queued)
		mailQueueDir := filepath.Join(ts.TempDir, "mail_queue")
		os.MkdirAll(mailQueueDir, 0755) // Ensure directory exists

		// In real implementation, check for queued emails
		// This is a placeholder for email verification
	})
}

// TestWebhookNotifications tests webhook notification functionality
func TestWebhookNotifications(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	// Start a mock webhook server
	webhookReceived := false
	webhookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webhookReceived = true

		if err := r.ParseForm(); err != nil {
			t.Errorf("Failed to parse webhook form: %v", err)
		}

		// Verify webhook payload
		if r.Form.Get("cmd_id") != "webhook-test-cmd" {
			t.Error("Webhook payload missing cmd_id")
		}
		if r.Form.Get("trace_id") == "" {
			t.Error("Webhook payload missing trace_id")
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer webhookServer.Close()

	t.Run("ConfigureWebhookNotification", func(t *testing.T) {
		cmdData := map[string]interface{}{
			"cmd_id":         "webhook-test-cmd",
			"host_ref":       "test-ssh-host",
			"command":        "echo 'webhook test'",
			"description":    "Command with webhook",
			"notify_webhook": webhookServer.URL,
		}

		resp := ts.DoAuthRequest(t, "POST", "/api/config/commands", cmdData)
		defer resp.Body.Close()

		AssertStatus(t, http.StatusCreated, resp.StatusCode)

		// Execute command to trigger webhook
		resp = ts.DoCommandRequest(t, "webhook-test-cmd", "")
		defer resp.Body.Close()

		AssertStatus(t, http.StatusOK, resp.StatusCode)

		// Wait for webhook to be called
		time.Sleep(2 * time.Second)

		if !webhookReceived {
			t.Error("Webhook was not called")
		}
	})
}

// TestConfigValidation tests configuration validation
func TestConfigValidation(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	t.Run("InvalidCronExpression", func(t *testing.T) {
		cmdData := map[string]interface{}{
			"cmd_id":        "invalid-cron-cmd",
			"host_ref":      "test-ssh-host",
			"command":       "echo test",
			"description":   "Command with invalid cron",
			"cron_schedule": "invalid cron expression",
		}

		// This should still succeed in creation but validation would catch it
		resp := ts.DoAuthRequest(t, "POST", "/api/config/commands", cmdData)
		defer resp.Body.Close()

		// The API might accept it, but validation should fail
		// In real implementation, you'd run validation separately
	})

	t.Run("InvalidMaxOutputSize", func(t *testing.T) {
		cmdData := map[string]interface{}{
			"cmd_id":          "invalid-size-cmd",
			"host_ref":        "test-ssh-host",
			"command":         "echo test",
			"description":     "Command with invalid max size",
			"max_output_size": "invalid-size",
		}

		resp := ts.DoAuthRequest(t, "POST", "/api/config/commands", cmdData)
		defer resp.Body.Close()

		// Check if validation catches this
	})

	t.Run("DuplicateCommandID", func(t *testing.T) {
		// Try to create a command with existing ID
		cmdData := map[string]interface{}{
			"cmd_id":      "test-echo", // Already exists
			"host_ref":    "test-ssh-host",
			"command":     "echo duplicate",
			"description": "Duplicate command",
		}

		resp := ts.DoAuthRequest(t, "POST", "/api/config/commands", cmdData)
		defer resp.Body.Close()

		// Should fail or overwrite depending on implementation
	})
}

// Helper function
func contains(s, substr string) bool {
	return len(substr) > 0 && len(s) >= len(substr) &&
		(s == substr || s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
