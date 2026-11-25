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

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter() (*loginLimiter, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)}
	l := newLoginLimiter()
	l.now = clock.now
	return l, clock
}

func failTimes(l *loginLimiter, ip, user string, n int) time.Duration {
	var locked time.Duration
	for i := 0; i < n; i++ {
		locked = l.fail(ip, user)
	}
	return locked
}

func TestLoginLimiterEscalatingPairLock(t *testing.T) {
	l, clock := newTestLimiter()
	if got := failTimes(l, "198.51.100.1", "alice", 2); got != 0 {
		t.Fatalf("locked after 2 failures: %v", got)
	}
	if _, locked := l.check("198.51.100.1", "alice"); locked {
		t.Fatal("locked before third failure")
	}
	if got := l.fail("198.51.100.1", "alice"); got != 30*time.Second {
		t.Fatalf("third failure lock=%v want 30s", got)
	}
	for i, want := range []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		if i > 0 {
			if got := failTimes(l, "198.51.100.1", "alice", 3); got != want {
				t.Fatalf("lock %d=%v want %v", i, got, want)
			}
		}
		if wait, locked := l.check("198.51.100.1", "alice"); !locked || wait != want {
			t.Fatalf("lock %d: locked=%v wait=%v want %v", i, locked, wait, want)
		}
		if _, other := l.check("198.51.100.1", "bob"); other {
			t.Fatal("lock leaked to another username")
		}
		if _, other := l.check("198.51.100.2", "alice"); other {
			t.Fatal("lock leaked to another IP")
		}
		clock.advance(want)
		if _, locked := l.check("198.51.100.1", "alice"); locked {
			t.Fatalf("still locked after %v", want)
		}
		// Let the per-IP window expire so only the pair escalation is measured.
		clock.advance(loginIPWindow)
	}
}

func TestLoginLimiterSuccessResetsEscalation(t *testing.T) {
	l, clock := newTestLimiter()
	failTimes(l, "198.51.100.1", "alice", 3)
	clock.advance(30 * time.Second)
	l.success("198.51.100.1", "alice")
	if got := failTimes(l, "198.51.100.1", "alice", 3); got != 30*time.Second {
		t.Fatalf("lock after success reset=%v want 30s", got)
	}
}

func TestLoginLimiterQuietPeriodResetsEscalation(t *testing.T) {
	l, clock := newTestLimiter()
	failTimes(l, "198.51.100.1", "alice", 3)
	clock.advance(loginPairResetAfter)
	if got := failTimes(l, "198.51.100.1", "alice", 3); got != 30*time.Second {
		t.Fatalf("lock after quiet period=%v want 30s", got)
	}
}

func TestLoginLimiterPerIPCapAcrossUsernames(t *testing.T) {
	l, clock := newTestLimiter()
	users := []string{"u0", "u1", "u2", "u3", "u4", "u5", "u6", "u7", "u8", "u9"}
	for i, user := range users {
		locked := l.fail("198.51.100.9", user)
		if i < len(users)-1 && locked != 0 {
			t.Fatalf("IP locked after %d failures", i+1)
		}
		if i == len(users)-1 && locked != loginIPLockDuration {
			t.Fatalf("IP lock=%v want %v", locked, loginIPLockDuration)
		}
	}
	if wait, locked := l.check("198.51.100.9", "someone-else"); !locked || wait != loginIPLockDuration {
		t.Fatalf("new username not blocked by IP lock: locked=%v wait=%v", locked, wait)
	}
	if _, locked := l.check("198.51.100.10", "u0"); locked {
		t.Fatal("IP lock leaked to another IP")
	}
	clock.advance(loginIPLockDuration)
	if _, locked := l.check("198.51.100.9", "someone-else"); locked {
		t.Fatal("IP still locked after lock duration")
	}
}

func TestLoginLimiterIPWindowExpires(t *testing.T) {
	l, clock := newTestLimiter()
	for i := 0; i < loginIPMaxFailures-1; i++ {
		l.fail("198.51.100.9", "user"+string(rune('a'+i)))
	}
	clock.advance(loginIPWindow)
	if got := l.fail("198.51.100.9", "late"); got != 0 {
		t.Fatalf("old failures still counted: lock=%v", got)
	}
}

const limiterTestIP = "203.0.113.50"

func newLimiterTestAuthManager(t *testing.T, users []User, admin AuthConfig) *AuthManager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"port":8080}`), 0644); err != nil {
		t.Fatal(err)
	}
	admin.JWTSecret = "test-secret"
	admin.Users = users
	return NewAuthManager(&admin, path)
}

func postAuth(t *testing.T, handler http.HandlerFunc, path string, body interface{}, ip string) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, bytes.NewReader(payload))
	req.RemoteAddr = ip + ":4321"
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestSingleStepLoginGenericErrorsAndLockout(t *testing.T) {
	password := "operator-password-123"
	hash, _ := hashPassword(password)
	secret := "JBSWY3DPEHPK3PXP"
	am := newLimiterTestAuthManager(t, []User{{Username: "operator", Enabled: true, PasswordHash: hash, TOTPSecret: secret, AllowedIPs: []string{"*"}}}, AuthConfig{})
	code, _ := totp.GenerateCode(secret, time.Now())

	attempts := []LoginRequest{
		{Username: "nobody", Password: password, TOTPCode: code},           // unknown username
		{Username: "operator", Password: "wrong-password", TOTPCode: code}, // wrong password
		{Username: "operator", Password: password, TOTPCode: "000000"},     // wrong TOTP
	}
	var firstBody string
	for i, attempt := range attempts {
		rec := postAuth(t, am.HandleLogin, "/api/auth/login", attempt, limiterTestIP)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
		if i == 0 {
			firstBody = rec.Body.String()
		} else if rec.Body.String() != firstBody {
			t.Fatalf("responses differ: %q vs %q", firstBody, rec.Body.String())
		}
	}

	// operator has two failures from this IP; the third locks the pair.
	rec := postAuth(t, am.HandleLogin, "/api/auth/login", attempts[2], limiterTestIP)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("expected lock on third failure: %d %q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
	// Correct credentials are refused while locked.
	rec = postAuth(t, am.HandleLogin, "/api/auth/login", LoginRequest{Username: "operator", Password: password, TOTPCode: code}, limiterTestIP)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("correct credentials accepted during lock: %d %s", rec.Code, rec.Body.String())
	}
	// Other IPs are unaffected.
	rec = postAuth(t, am.HandleLogin, "/api/auth/login", LoginRequest{Username: "operator", Password: password, TOTPCode: code}, "203.0.113.52")
	if rec.Code != http.StatusOK {
		t.Fatalf("login from other IP failed: %d %s", rec.Code, rec.Body.String())
	}
	var resp TwoFAResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Token == "" {
		t.Fatalf("no token issued: %s", rec.Body.String())
	}
}

func TestAdminSingleStepLogin(t *testing.T) {
	secret := "JBSWY3DPEHPK3PXP"
	am := newLimiterTestAuthManager(t, nil, AuthConfig{AdminPassword: "admin-password-123", TOTPSecret: secret})
	code, _ := totp.GenerateCode(secret, time.Now())
	rec := postAuth(t, am.HandleLogin, "/api/auth/login", LoginRequest{Password: "admin-password-123"}, limiterTestIP)
	if rec.Code != http.StatusUnauthorized || bytes.Contains(rec.Body.Bytes(), []byte("temp_token")) {
		t.Fatalf("password-only admin login must fail generically: %d %s", rec.Code, rec.Body.String())
	}
	rec = postAuth(t, am.HandleLogin, "/api/auth/login", LoginRequest{Password: "admin-password-123", TOTPCode: code}, limiterTestIP)
	var resp TwoFAResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &resp) != nil || resp.Token == "" {
		t.Fatalf("admin login failed: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminEnrollmentWhenTOTPMissing(t *testing.T) {
	am := newLimiterTestAuthManager(t, nil, AuthConfig{AdminPassword: "admin-password-123"})
	rec := postAuth(t, am.HandleLogin, "/api/auth/login", LoginRequest{Password: "admin-password-123"}, limiterTestIP)
	var login LoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil || !login.NeedsTOTPSetup || login.TempToken == "" || login.QRCode == "" {
		t.Fatalf("expected admin TOTP enrollment: %s", rec.Body.String())
	}
	session, _ := am.tempSession(login.TempToken)
	code, _ := totp.GenerateCode(session.PendingTOTPSecret, time.Now())
	rec = postAuth(t, am.HandleVerify2FA, "/api/auth/verify-2fa", TwoFARequest{TempToken: login.TempToken, TOTPCode: code}, limiterTestIP)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin enrollment failed: %d %s", rec.Code, rec.Body.String())
	}
	am.mu.RLock()
	saved := am.totpSecret
	am.mu.RUnlock()
	if saved != session.PendingTOTPSecret {
		t.Fatal("admin TOTP secret not saved")
	}
}

func TestEnrollmentTokenLockedAndBoundToIP(t *testing.T) {
	password := "fresh-user-password-1"
	hash, _ := hashPassword(password)
	am := newLimiterTestAuthManager(t, []User{{Username: "fresh", Enabled: true, PasswordHash: hash, AllowedIPs: []string{"*"}}}, AuthConfig{})
	rec := postAuth(t, am.HandleLogin, "/api/auth/login", LoginRequest{Username: "fresh", Password: password}, limiterTestIP)
	var login LoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil || !login.NeedsTOTPSetup || login.TempToken == "" {
		t.Fatalf("expected enrollment: %s", rec.Body.String())
	}
	session, _ := am.tempSession(login.TempToken)
	code, _ := totp.GenerateCode(session.PendingTOTPSecret, time.Now())

	rec = postAuth(t, am.HandleVerify2FA, "/api/auth/verify-2fa", TwoFARequest{TempToken: login.TempToken, TOTPCode: code}, "203.0.113.99")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("temp token accepted from another IP: %d", rec.Code)
	}
	for i := 0; i < 2; i++ {
		rec = postAuth(t, am.HandleVerify2FA, "/api/auth/verify-2fa", TwoFARequest{TempToken: login.TempToken, TOTPCode: "000000"}, limiterTestIP)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong code %d status=%d", i, rec.Code)
		}
	}
	rec = postAuth(t, am.HandleVerify2FA, "/api/auth/verify-2fa", TwoFARequest{TempToken: login.TempToken, TOTPCode: "000000"}, limiterTestIP)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected lock on third wrong code: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := am.tempSession(login.TempToken); ok {
		t.Fatal("temp token still valid after lockout")
	}
}
