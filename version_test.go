package main

import (
	"encoding/json"
	"io/ioutil"
	"path/filepath"
	"strings"
	"testing"
)

func testConfigForVersionStamp(version string) map[string]interface{} {
	config := map[string]interface{}{
		"port":               8080,
		"bind_address":       "127.0.0.1",
		"access_url":         "http://localhost:8080",
		"cron_enabled":       "N",
		"log_retention_days": 3,
		"dev_mode":           false,
		"admin_password":     "testpassword123",
		"totp_secret":        "JBSWY3DPEHPK3PXP",
		"smtp": map[string]interface{}{
			"host":     "smtp.example.com",
			"port":     587,
			"username": "user@example.com",
			"password": "secret",
			"from":     "Notifier <user@example.com>",
			"tls":      true,
		},
		"hosts": map[string]interface{}{
			"test-host": map[string]interface{}{
				"connection_type": "ssh",
				"host":            "localhost",
				"port":            22,
				"username":        "testuser",
				"password":        "testpass",
			},
		},
		"commands": []interface{}{},
		"runbooks": []interface{}{},
	}
	if version != "" {
		config["version"] = version
	}
	return config
}

func readConfigMapForTest(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := ioutil.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("failed to parse config: %v", err)
	}
	return config
}

func TestSaveConfigStampsAppVersion(t *testing.T) {
	originalConfigFile := cache.configFile
	originalConfig := cache.config
	originalHash := cache.lastHash
	t.Cleanup(func() {
		cache.Lock()
		cache.configFile = originalConfigFile
		cache.config = originalConfig
		cache.lastHash = originalHash
		cache.Unlock()
	})

	tests := []struct {
		name    string
		version string
	}{
		{name: "adds missing version"},
		{name: "overwrites stale version", version: "0.1.0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.json")
			ca := NewConfigAPI(configPath, nil)

			cache.Lock()
			cache.configFile = configPath
			cache.config = nil
			cache.lastHash = ""
			cache.Unlock()

			if err := ca.saveConfig(testConfigForVersionStamp(tt.version)); err != nil {
				t.Fatalf("saveConfig failed: %v", err)
			}

			config := readConfigMapForTest(t, configPath)
			if config["version"] != AppVersion {
				t.Fatalf("expected version %q, got %#v", AppVersion, config["version"])
			}

			data, err := ioutil.ReadFile(configPath)
			if err != nil {
				t.Fatalf("failed to read raw config: %v", err)
			}
			raw := string(data)
			if !strings.Contains(raw, "Notifier <user@example.com>") {
				t.Fatalf("expected SMTP from address to remain unescaped, got: %s", raw)
			}
			if strings.Contains(raw, "\\u003c") || strings.Contains(raw, "\\u003e") || strings.Contains(raw, "\\u0026") {
				t.Fatalf("config JSON contains HTML-escaped characters: %s", raw)
			}
		})
	}
}

func TestSaveAuthConfigStampsAppVersion(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	initialConfig := []byte(`{
	"version": "0.1.0",
	"port": 8080
}
`)
	if err := ioutil.WriteFile(configPath, initialConfig, 0644); err != nil {
		t.Fatalf("failed to write initial config: %v", err)
	}

	am := &AuthManager{
		adminPassword: "new-password",
		totpSecret:    "JBSWY3DPEHPK3PXP",
		jwtSecret:     []byte("test-jwt-secret"),
		configPath:    configPath,
	}
	if err := am.saveAuthConfig(); err != nil {
		t.Fatalf("saveAuthConfig failed: %v", err)
	}

	config := readConfigMapForTest(t, configPath)
	if config["version"] != AppVersion {
		t.Fatalf("expected version %q, got %#v", AppVersion, config["version"])
	}
}
