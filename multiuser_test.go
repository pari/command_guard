package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestIPAllowedRules(t *testing.T) {
	tests := []struct {
		name, ip string
		rules    []string
		want     bool
	}{
		{"wildcard", "203.0.113.4", []string{"*"}, true},
		{"wildcard combined rejected", "203.0.113.4", []string{"*", "203.0.113.4"}, false},
		{"exact ipv4", "203.0.113.4", []string{"203.0.113.4"}, true},
		{"ipv4 cidr", "203.0.113.4", []string{"203.0.113.0/24"}, true},
		{"ipv4 outside", "198.51.100.2", []string{"203.0.113.0/24"}, false},
		{"exact ipv6", "2001:db8::10", []string{"2001:db8::10"}, true},
		{"ipv6 cidr", "2001:db8::10", []string{"2001:db8::/48"}, true},
		{"invalid client", "not-an-ip", []string{"*"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ipAllowed(tt.ip, tt.rules); got != tt.want {
				t.Fatalf("ipAllowed()=%v want %v", got, tt.want)
			}
		})
	}
}

func TestRegularUserLoginAndTokenRevocation(t *testing.T) {
	password := "initial-password-123"
	hash, _ := hashPassword(password)
	secret := "JBSWY3DPEHPK3PXP"
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"port":8080}`), 0644); err != nil {
		t.Fatal(err)
	}
	am := NewAuthManager(&AuthConfig{JWTSecret: "test-secret", Users: []User{{Username: "operator", Enabled: true, PasswordHash: hash, TOTPSecret: secret, TokenVersion: 1}}}, path)
	code, _ := totp.GenerateCode(secret, time.Now())
	loginBody, _ := json.Marshal(LoginRequest{Username: "Operator", Password: password, TOTPCode: code})
	loginReq := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(loginBody))
	loginReq.RemoteAddr = "203.0.113.8:1234"
	loginRecorder := httptest.NewRecorder()
	am.HandleLogin(loginRecorder, loginReq)
	if loginRecorder.Code != 200 {
		t.Fatalf("login status=%d body=%s", loginRecorder.Code, loginRecorder.Body.String())
	}
	var verified TwoFAResponse
	_ = json.Unmarshal(loginRecorder.Body.Bytes(), &verified)
	if verified.Token == "" {
		t.Fatalf("single-step login did not issue a token: %s", loginRecorder.Body.String())
	}
	protected := am.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	request := httptest.NewRequest("GET", "/protected", nil)
	request.RemoteAddr = "203.0.113.8:1234"
	request.Header.Set("Authorization", "Bearer "+verified.Token)
	response := httptest.NewRecorder()
	protected(response, request)
	if response.Code != 204 {
		t.Fatalf("valid token rejected: %d", response.Code)
	}
	am.mu.Lock()
	user := am.users["operator"]
	user.TokenVersion++
	am.users["operator"] = user
	am.mu.Unlock()
	response = httptest.NewRecorder()
	protected(response, request)
	if response.Code != 401 {
		t.Fatalf("revoked token status=%d", response.Code)
	}
}

func TestForcedOnboardingSetsPasswordAndTOTP(t *testing.T) {
	temporary := "temporary-pass-123"
	hash, _ := hashPassword(temporary)
	path := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(path, []byte(`{"port":8080}`), 0644)
	am := NewAuthManager(&AuthConfig{JWTSecret: "test-secret", Users: []User{{Username: "new-user", Enabled: true, PasswordHash: hash, ForcePasswordChange: true, TokenVersion: 1}}}, path)
	body, _ := json.Marshal(LoginRequest{Username: "new-user", Password: temporary})
	req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
	req.RemoteAddr = "192.0.2.1:1"
	rec := httptest.NewRecorder()
	am.HandleLogin(rec, req)
	var login LoginResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	if !login.NeedsPasswordChange || !login.NeedsTOTPSetup {
		t.Fatalf("onboarding not required: %s", rec.Body.String())
	}
	session, ok := am.tempSession(login.TempToken)
	if !ok || session.PendingTOTPSecret == "" {
		t.Fatal("missing pending TOTP enrollment")
	}
	code, _ := totp.GenerateCode(session.PendingTOTPSecret, time.Now())
	complete, _ := json.Marshal(OnboardingRequest{TempToken: login.TempToken, NewPassword: "replacement-pass-456", TOTPCode: code})
	completeReq := httptest.NewRequest("POST", "/api/auth/complete-onboarding", bytes.NewReader(complete))
	completeReq.RemoteAddr = "192.0.2.1:1"
	completeRec := httptest.NewRecorder()
	am.HandleCompleteOnboarding(completeRec, completeReq)
	if completeRec.Code != 200 {
		t.Fatalf("onboarding status=%d body=%s", completeRec.Code, completeRec.Body.String())
	}
	am.mu.RLock()
	user := am.users["new-user"]
	am.mu.RUnlock()
	if user.ForcePasswordChange || user.TOTPSecret == "" || !verifyPassword(user.PasswordHash, "replacement-pass-456") {
		t.Fatal("onboarding state not persisted")
	}
}

func TestUserPolicyTimeAndTemporaryIP(t *testing.T) {
	location := time.FixedZone("server", 19800)
	base := time.Date(2026, time.July, 13, 9, 0, 0, 0, location) // Monday
	user := normalizeUser(User{Enabled: true, AllowedIPs: []string{"10.0.0.0/8"}, AllowedWeekdays: []string{"monday"}, AllowedTimeStart: "09:00", AllowedTimeEnd: "17:00"})
	if err := evaluateUserPolicy(user, "10.1.2.3", base); err != nil {
		t.Fatalf("start boundary rejected: %v", err)
	}
	if err := evaluateUserPolicy(user, "10.1.2.3", base.Add(8*time.Hour)); err == nil {
		t.Fatal("end boundary accepted")
	}
	if err := evaluateUserPolicy(user, "10.1.2.3", base.AddDate(0, 0, 1)); err == nil {
		t.Fatal("disallowed weekday accepted")
	}
	user.TemporaryAnyIPUntil = base.Add(time.Hour).Format(time.RFC3339)
	if err := evaluateUserPolicy(user, "203.0.113.9", base); err != nil {
		t.Fatalf("temporary any-IP rejected: %v", err)
	}
	if err := evaluateUserPolicy(user, "203.0.113.9", base.Add(time.Hour)); err == nil {
		t.Fatal("temporary any-IP accepted at exact expiry")
	}
}

func TestTrustedProxyClientIP(t *testing.T) {
	original := authManager
	defer func() { authManager = original }()
	authManager = &AuthManager{users: map[string]User{}, trustedProxies: []string{"127.0.0.1"}}
	req := httptest.NewRequest("GET", "http://example.test/", nil)
	req.RemoteAddr = "198.51.100.8:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := getClientIP(req); got != "198.51.100.8" {
		t.Fatalf("untrusted peer spoofed client IP: %q", got)
	}
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.8, 127.0.0.1")
	if got := getClientIP(req); got != "198.51.100.8" {
		t.Fatalf("trusted chain client IP=%q", got)
	}
	req.Header.Set("X-Forwarded-For", "bad-value, 127.0.0.1")
	if got := getClientIP(req); got != "127.0.0.1" {
		t.Fatalf("malformed chain should fall back to peer, got %q", got)
	}
}

func TestPasswordHashSupportsPlanLength(t *testing.T) {
	for _, length := range []int{12, 72, 128} {
		password := string(make([]byte, length))
		passwordBytes := []byte(password)
		for i := range passwordBytes {
			passwordBytes[i] = 'a'
		}
		password = string(passwordBytes)
		hash, err := hashPassword(password)
		if err != nil {
			t.Fatalf("length %d rejected: %v", length, err)
		}
		if !verifyPassword(hash, password) {
			t.Fatalf("length %d did not verify", length)
		}
	}
	if _, err := hashPassword("too-short"); err == nil {
		t.Fatal("short password accepted")
	}
}

func TestRegularUserLogGrantAccess(t *testing.T) {
	original := authManager
	defer func() { authManager = original }()
	authManager = &AuthManager{users: map[string]User{"viewer": normalizeUser(User{Username: "viewer", Enabled: true, CommandIDs: []string{"allowed-command"}, RunBookIDs: []string{"allowed-book"}})}}
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	request := httptest.NewRequest("GET", "/api/logs", nil)
	request = withClaims(request, &Claims{Username: "viewer", Role: RoleUser})
	if !canAccessLogFile(request, write("command.txt", "cmd_id : allowed-command\noutput:\n")) {
		t.Fatal("Command grant did not expose legacy Command log")
	}
	if !canAccessLogFile(request, write("runbook.txt", "runbook_id : allowed-book\ncmd_id : hidden-step\noutput:\n")) {
		t.Fatal("RunBook grant did not expose step log")
	}
	if canAccessLogFile(request, write("denied.txt", "cmd_id : hidden-command\noutput:\n")) {
		t.Fatal("unassigned log was exposed")
	}
}
