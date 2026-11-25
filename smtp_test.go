package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/gomail.v2"
)

func preserveConfigCacheForTest(t *testing.T, configPath string) {
	t.Helper()
	cache.Lock()
	originalFile := cache.configFile
	originalConfig := cache.config
	originalHash := cache.lastHash
	cache.configFile = configPath
	cache.config = nil
	cache.lastHash = ""
	cache.Unlock()
	t.Cleanup(func() {
		cache.Lock()
		cache.configFile = originalFile
		cache.config = originalConfig
		cache.lastHash = originalHash
		cache.Unlock()
	})
}

func writeSMTPTestConfig(t *testing.T, configPath string, smtpConfig map[string]interface{}) {
	t.Helper()
	config := testConfigForVersionStamp("")
	if smtpConfig == nil {
		delete(config, "smtp")
	} else {
		config["smtp"] = smtpConfig
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("failed to marshal test config: %v", err)
	}
	if err := ioutil.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}
}

func decodeSMTPResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var response struct {
		Success bool                   `json:"success"`
		Data    map[string]interface{} `json:"data"`
		Error   string                 `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !response.Success {
		t.Fatalf("request failed: %s", response.Error)
	}
	return response.Data
}

func TestSMTPConfigRoundTripAndPasswordPreservation(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	writeSMTPTestConfig(t, configPath, map[string]interface{}{
		"host": "old.example.com", "port": 587, "username": "old", "password": "smtp-secret",
		"from": "Old Sender <old@example.com>", "tls": true,
	})
	preserveConfigCacheForTest(t, configPath)
	api := NewConfigAPI(configPath, nil)

	payload := []byte(`{
		"host":"smtp.example.com","port":465,"username":"",
		"from_email":"alerts@example.com","from_name":"Command Guard",
		"encryption":"ssl","enabled":false,"timeout":45,"retries":0,
		"skip_cert_verify":true
	}`)
	request := httptest.NewRequest(http.MethodPut, "/api/config/smtp", bytes.NewReader(payload))
	recorder := httptest.NewRecorder()
	api.HandleUpdateSMTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	responseData := decodeSMTPResponse(t, recorder)
	if responseData["from_email"] != "alerts@example.com" || responseData["from_name"] != "Command Guard" {
		t.Fatalf("unexpected sender response: %#v", responseData)
	}
	if responseData["encryption"] != "ssl" || responseData["enabled"] != false || responseData["retries"] != float64(0) {
		t.Fatalf("unexpected SMTP settings response: %#v", responseData)
	}
	if responseData["has_password"] != true {
		t.Fatal("expected existing password metadata to be preserved")
	}

	stored := readConfigMapForTest(t, configPath)["smtp"].(map[string]interface{})
	if stored["password"] != "smtp-secret" {
		t.Fatal("existing SMTP password was not preserved")
	}
	if _, exists := stored["from"]; exists {
		t.Fatal("legacy from field was not removed during canonical save")
	}
	if _, exists := stored["tls"]; exists {
		t.Fatal("legacy tls field was not removed during canonical save")
	}

	request = httptest.NewRequest(http.MethodGet, "/api/config/smtp", nil)
	recorder = httptest.NewRecorder()
	api.HandleGetSMTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	reloaded := decodeSMTPResponse(t, recorder)
	for _, field := range []string{"from_email", "from_name", "encryption", "enabled", "timeout", "retries", "skip_cert_verify"} {
		if reloaded[field] != responseData[field] {
			t.Fatalf("field %s did not round trip: saved=%#v loaded=%#v", field, responseData[field], reloaded[field])
		}
	}
}

func TestGetSMTPLegacyAndMissingDefaults(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		writeSMTPTestConfig(t, configPath, map[string]interface{}{
			"host": "smtp.example.com", "port": 587,
			"from": "Legacy Sender <legacy@example.com>", "tls": true,
		})
		api := NewConfigAPI(configPath, nil)
		recorder := httptest.NewRecorder()
		api.HandleGetSMTP(recorder, httptest.NewRequest(http.MethodGet, "/api/config/smtp", nil))
		data := decodeSMTPResponse(t, recorder)
		if data["from_email"] != "legacy@example.com" || data["from_name"] != "Legacy Sender" || data["encryption"] != "tls" {
			t.Fatalf("legacy SMTP config was not normalized: %#v", data)
		}
		if data["enabled"] != true || data["timeout"] != float64(30) || data["retries"] != float64(0) {
			t.Fatalf("legacy defaults changed unexpectedly: %#v", data)
		}
	})

	t.Run("missing", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		writeSMTPTestConfig(t, configPath, nil)
		api := NewConfigAPI(configPath, nil)
		recorder := httptest.NewRecorder()
		api.HandleGetSMTP(recorder, httptest.NewRequest(http.MethodGet, "/api/config/smtp", nil))
		data := decodeSMTPResponse(t, recorder)
		if data["enabled"] != true || data["timeout"] != float64(30) || data["retries"] != float64(3) || data["encryption"] != "none" {
			t.Fatalf("unexpected new SMTP defaults: %#v", data)
		}
	})
}

func TestSMTPValidation(t *testing.T) {
	valid := &SMTPConfig{
		Host: "smtp.example.com", Port: 587, FromEmail: "alerts@example.com",
		Encryption: "tls", Enabled: boolPointer(true), Timeout: 30, Retries: 3,
	}
	if err := validateSMTPConfig(valid); err != nil {
		t.Fatalf("valid SMTP config rejected: %v", err)
	}
	invalid := *valid
	invalid.Encryption = "automatic"
	if err := validateSMTPConfig(&invalid); err == nil {
		t.Fatal("invalid encryption was accepted")
	}
	invalid = *valid
	invalid.FromEmail = "not-an-email"
	if err := validateSMTPConfig(&invalid); err == nil {
		t.Fatal("invalid from email was accepted")
	}
}

func TestSendSMTPMessageHeadersRetriesAndDisabled(t *testing.T) {
	originalAttempt := smtpSendAttempt
	originalWait := waitForSMTPRetry
	t.Cleanup(func() {
		smtpSendAttempt = originalAttempt
		waitForSMTPRetry = originalWait
	})
	waitForSMTPRetry = func(time.Duration) error { return nil }

	config := &SMTPConfig{
		Host: "smtp.example.com", Port: 587, FromEmail: "alerts@example.com", FromName: "Command Guard",
		Encryption: "tls", Enabled: boolPointer(true), Timeout: 30, Retries: 2,
	}
	message := gomail.NewMessage()
	message.SetHeader("To", "recipient@example.com")
	message.SetHeader("Subject", "test")
	message.SetBody("text/plain", "body")
	attempts := 0
	smtpSendAttempt = func(_ *SMTPConfig, sent *gomail.Message) error {
		attempts++
		from, err := mail.ParseAddress(sent.GetHeader("From")[0])
		if err != nil || from.Address != "alerts@example.com" || from.Name != "Command Guard" {
			t.Fatalf("unexpected From header: %#v (%v)", sent.GetHeader("From"), err)
		}
		if attempts < 3 {
			return &textproto.Error{Code: 451, Msg: "temporary failure"}
		}
		return nil
	}
	if err := sendSMTPMessage(config, message); err != nil {
		t.Fatalf("transient SMTP failure was not retried successfully: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempt count = %d, want 3", attempts)
	}

	attempts = 0
	smtpSendAttempt = func(_ *SMTPConfig, _ *gomail.Message) error {
		attempts++
		return &textproto.Error{Code: 550, Msg: "permanent failure"}
	}
	if err := sendSMTPMessage(config, message); err == nil {
		t.Fatal("permanent SMTP failure was ignored")
	}
	if attempts != 1 {
		t.Fatalf("permanent failure attempts = %d, want 1", attempts)
	}

	disabled := *config
	disabled.Enabled = boolPointer(false)
	attempts = 0
	smtpSendAttempt = func(_ *SMTPConfig, _ *gomail.Message) error {
		attempts++
		return errors.New("should not be called")
	}
	if err := sendSMTPMessage(&disabled, message); err == nil {
		t.Fatal("disabled SMTP unexpectedly sent a message")
	}
	if attempts != 0 {
		t.Fatalf("disabled SMTP made %d send attempts", attempts)
	}
}

func canonicalSMTPTestConfig(enabled bool) map[string]interface{} {
	return map[string]interface{}{
		"host":             "smtp.example.com",
		"port":             587,
		"username":         "smtp-user",
		"password":         "smtp-password",
		"from_email":       "alerts@example.com",
		"from_name":        "Command Guard",
		"encryption":       "tls",
		"enabled":          enabled,
		"timeout":          30,
		"retries":          0,
		"skip_cert_verify": false,
	}
}

func TestSendSMTPTestEmailHandlerSuccess(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	writeSMTPTestConfig(t, configPath, canonicalSMTPTestConfig(true))
	preserveConfigCacheForTest(t, configPath)

	originalAttempt := smtpSendAttempt
	t.Cleanup(func() { smtpSendAttempt = originalAttempt })
	sendAttempts := 0
	smtpSendAttempt = func(config *SMTPConfig, message *gomail.Message) error {
		sendAttempts++
		if config.Host != "smtp.example.com" || config.Encryption != "tls" {
			t.Fatalf("handler did not use saved SMTP config: %#v", config)
		}
		if got := message.GetHeader("To"); len(got) != 1 || got[0] != "recipient@example.com" {
			t.Fatalf("unexpected To header: %#v", got)
		}
		if got := message.GetHeader("Subject"); len(got) != 1 || got[0] != "Command Guard Test Email" {
			t.Fatalf("unexpected Subject header: %#v", got)
		}
		from, err := mail.ParseAddress(message.GetHeader("From")[0])
		if err != nil || from.Address != "alerts@example.com" || from.Name != "Command Guard" {
			t.Fatalf("unexpected From header: %#v (%v)", message.GetHeader("From"), err)
		}
		var rendered bytes.Buffer
		if _, err := message.WriteTo(&rendered); err != nil {
			t.Fatalf("failed to render test message: %v", err)
		}
		if !bytes.Contains(rendered.Bytes(), []byte("Your SMTP settings are working correctly.")) {
			t.Fatalf("test message body missing confirmation text: %s", rendered.String())
		}
		return nil
	}

	api := NewConfigAPI(configPath, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/config/smtp/test-email", bytes.NewBufferString(`{"to":"recipient@example.com"}`))
	recorder := httptest.NewRecorder()
	api.HandleSendSMTPTestEmail(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	data := decodeSMTPResponse(t, recorder)
	if data["to"] != "recipient@example.com" || data["subject"] != "Command Guard Test Email" || data["sent_at"] == "" {
		t.Fatalf("unexpected success response: %#v", data)
	}
	if sendAttempts != 1 {
		t.Fatalf("send attempts = %d, want 1", sendAttempts)
	}
}

func TestSendSMTPTestEmailHandlerErrors(t *testing.T) {
	t.Run("method", func(t *testing.T) {
		api := NewConfigAPI("unused.json", nil)
		recorder := httptest.NewRecorder()
		api.HandleSendSMTPTestEmail(recorder, httptest.NewRequest(http.MethodGet, "/api/config/smtp/test-email", nil))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("invalid recipient", func(t *testing.T) {
		api := NewConfigAPI("unused.json", nil)
		recorder := httptest.NewRecorder()
		api.HandleSendSMTPTestEmail(recorder, httptest.NewRequest(http.MethodPost, "/api/config/smtp/test-email", bytes.NewBufferString(`{"to":"not-an-email"}`)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		writeSMTPTestConfig(t, configPath, canonicalSMTPTestConfig(false))
		preserveConfigCacheForTest(t, configPath)
		api := NewConfigAPI(configPath, nil)
		recorder := httptest.NewRecorder()
		api.HandleSendSMTPTestEmail(recorder, httptest.NewRequest(http.MethodPost, "/api/config/smtp/test-email", bytes.NewBufferString(`{"to":"recipient@example.com"}`)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("missing configuration", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		writeSMTPTestConfig(t, configPath, nil)
		preserveConfigCacheForTest(t, configPath)
		api := NewConfigAPI(configPath, nil)
		recorder := httptest.NewRecorder()
		api.HandleSendSMTPTestEmail(recorder, httptest.NewRequest(http.MethodPost, "/api/config/smtp/test-email", bytes.NewBufferString(`{"to":"recipient@example.com"}`)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("transport failure", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		writeSMTPTestConfig(t, configPath, canonicalSMTPTestConfig(true))
		preserveConfigCacheForTest(t, configPath)

		originalAttempt := smtpSendAttempt
		originalLogger := logSMTPTestFailure
		t.Cleanup(func() {
			smtpSendAttempt = originalAttempt
			logSMTPTestFailure = originalLogger
		})
		smtpSendAttempt = func(_ *SMTPConfig, _ *gomail.Message) error {
			return &textproto.Error{Code: 550, Msg: "mailbox rejected"}
		}
		var logged string
		logSMTPTestFailure = func(message string) { logged = message }

		api := NewConfigAPI(configPath, nil)
		recorder := httptest.NewRecorder()
		api.HandleSendSMTPTestEmail(recorder, httptest.NewRequest(http.MethodPost, "/api/config/smtp/test-email", bytes.NewBufferString(`{"to":"recipient@example.com"}`)))
		if recorder.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if logged == "" {
			t.Fatal("transport failure was not logged")
		}
	})
}
