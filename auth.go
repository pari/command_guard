package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

type UserTimeWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// User is persisted in the top-level users array. Username is canonical lowercase
// and immutable after creation. Secret fields are never returned by user/config APIs.
type User struct {
	Username            string          `json:"username"`
	Enabled             bool            `json:"enabled"`
	PasswordHash        string          `json:"password_hash"`
	ForcePasswordChange bool            `json:"force_password_change,omitempty"`
	TOTPSecret          string          `json:"totp_secret,omitempty"`
	TokenVersion        int             `json:"token_version,omitempty"`
	CommandIDs          []string        `json:"command_ids,omitempty"`
	RunBookIDs          []string        `json:"runbook_ids,omitempty"`
	HTTPRequestIDs      []string        `json:"http_request_ids,omitempty"` // HTTP request IDs the user may run, reschedule and read logs for
	TerminalHosts       []string        `json:"terminal_hosts,omitempty"`   // SSH host IDs the user may open persistent terminals on
	VNCAccess           []VNCGrant      `json:"vnc_access,omitempty"`       // Remote Desktops (VNC connections) and level (control|view)
	AllowedIPs          []string        `json:"allowed_ips,omitempty"`
	AllowedWeekdays     []string        `json:"allowed_weekdays,omitempty"`
	AllowedTimeStart    string          `json:"allowed_time_start,omitempty"`
	AllowedTimeEnd      string          `json:"allowed_time_end,omitempty"`
	DailyTimeWindow     *UserTimeWindow `json:"daily_time_window,omitempty"`
	TemporaryAnyIPUntil string          `json:"temporary_any_ip_until,omitempty"`
}

type AuthConfig struct {
	AdminPassword  string   `json:"admin_password"`
	TOTPSecret     string   `json:"totp_secret"`
	JWTSecret      string   `json:"jwt_secret"`
	DevMode        bool     `json:"dev_mode"`
	Users          []User   `json:"users,omitempty"`
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
}

type tempAuthSession struct {
	Username          string
	Role              string
	ExpiresAt         time.Time
	PendingTOTPSecret string
	NeedsPassword     bool
	SourceIP          string
	// TokenVersion is the user's token_version when the session was created. A
	// password reset, TOTP reset, disable or another completed enrollment bumps
	// it, which makes this session stale.
	TokenVersion int
}

type AuthManager struct {
	mu             sync.RWMutex
	adminPassword  string
	totpSecret     string
	jwtSecret      []byte
	devMode        bool
	users          map[string]User
	trustedProxies []string
	tempSessions   map[string]tempAuthSession
	limiter        *loginLimiter
	configPath     string
}

type Claims struct {
	Username     string `json:"username"`
	Role         string `json:"role"`
	TokenVersion int    `json:"token_version"`
	jwt.RegisteredClaims
}

type authContextKey struct{}

func ClaimsFromRequest(r *http.Request) (*Claims, bool) {
	claims, ok := r.Context().Value(authContextKey{}).(*Claims)
	return claims, ok && claims != nil
}

func withClaims(r *http.Request, claims *Claims) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authContextKey{}, claims))
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTPCode string `json:"totp_code"`
}

type LoginResponse struct {
	Success             bool   `json:"success"`
	TempToken           string `json:"temp_token,omitempty"`
	NeedsSetup          bool   `json:"needs_setup,omitempty"`
	NeedsPasswordChange bool   `json:"needs_password_change,omitempty"`
	NeedsTOTPSetup      bool   `json:"needs_totp_setup,omitempty"`
	QRCode              string `json:"qr_code,omitempty"`
	Message             string `json:"message,omitempty"`
}

type TwoFARequest struct {
	TempToken string `json:"temp_token"`
	TOTPCode  string `json:"totp_code"`
}

type TwoFAResponse struct {
	Success bool   `json:"success"`
	Token   string `json:"token,omitempty"`
	Message string `json:"message,omitempty"`
}

type OnboardingRequest struct {
	TempToken   string `json:"temp_token"`
	NewPassword string `json:"new_password"`
	TOTPCode    string `json:"totp_code"`
}

func NewAuthManager(config *AuthConfig, configPath string) *AuthManager {
	am := &AuthManager{
		adminPassword:  config.AdminPassword,
		totpSecret:     config.TOTPSecret,
		devMode:        config.DevMode,
		users:          make(map[string]User),
		trustedProxies: append([]string(nil), config.TrustedProxies...),
		tempSessions:   make(map[string]tempAuthSession),
		limiter:        newLoginLimiter(),
		configPath:     configPath,
	}
	am.replaceUsers(config.Users)
	if config.JWTSecret == "" {
		secret := make([]byte, 32)
		_, _ = rand.Read(secret)
		am.jwtSecret = secret
		am.persistJWTSecret()
	} else if decoded, err := base64.StdEncoding.DecodeString(config.JWTSecret); err == nil && len(decoded) > 0 {
		am.jwtSecret = decoded
	} else {
		am.jwtSecret = []byte(config.JWTSecret)
	}
	go am.cleanupTempTokens()
	go am.limiter.cleanup()
	return am
}

func canonicalUsername(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func (am *AuthManager) replaceUsers(users []User) {
	if am.users == nil {
		am.users = make(map[string]User)
	}
	for key := range am.users {
		delete(am.users, key)
	}
	for _, raw := range users {
		u := normalizeUser(raw)
		if u.Username != "" && u.Username != RoleAdmin {
			am.users[u.Username] = u
		}
	}
}

func normalizeUser(user User) User {
	user.Username = canonicalUsername(user.Username)
	if user.TokenVersion < 1 {
		user.TokenVersion = 1
	}
	if len(user.AllowedIPs) == 0 {
		user.AllowedIPs = []string{"*"}
	}
	if len(user.AllowedWeekdays) == 0 {
		user.AllowedWeekdays = allWeekdays()
	}
	user.CommandIDs = uniqueSorted(user.CommandIDs)
	user.RunBookIDs = uniqueSorted(user.RunBookIDs)
	user.HTTPRequestIDs = uniqueSorted(user.HTTPRequestIDs)
	user.TerminalHosts = uniqueSorted(user.TerminalHosts)
	user.VNCAccess = normalizeVNCGrants(user.VNCAccess)
	return user
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func allWeekdays() []string {
	return []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}
}

func validatePassword(password string) error {
	length := utf8.RuneCountInString(password)
	if length < 12 || length > 128 {
		return errors.New("password must be 12 to 128 characters")
	}
	return nil
}

func bcryptMaterial(password string) []byte {
	sum := sha256.Sum256([]byte(password))
	return []byte(base64.RawStdEncoding.EncodeToString(sum[:]))
}

func hashPassword(password string) (string, error) {
	if err := validatePassword(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword(bcryptMaterial(password), bcrypt.DefaultCost)
	return string(hash), err
}

func verifyPassword(hash, password string) bool {
	return hash != "" && bcrypt.CompareHashAndPassword([]byte(hash), bcryptMaterial(password)) == nil
}

func (am *AuthManager) cleanupTempTokens() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		am.mu.Lock()
		now := time.Now()
		for token, session := range am.tempSessions {
			if !now.Before(session.ExpiresAt) {
				delete(am.tempSessions, token)
			}
		}
		am.mu.Unlock()
	}
}

func (am *AuthManager) createTempSession(session tempAuthSession) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	session.ExpiresAt = time.Now().Add(5 * time.Minute)
	am.mu.Lock()
	am.tempSessions[token] = session
	am.mu.Unlock()
	return token
}

func (am *AuthManager) tempSession(token string) (tempAuthSession, bool) {
	am.mu.RLock()
	session, ok := am.tempSessions[token]
	am.mu.RUnlock()
	return session, ok && time.Now().Before(session.ExpiresAt)
}

func (am *AuthManager) consumeTempSession(token string) {
	am.mu.Lock()
	delete(am.tempSessions, token)
	am.mu.Unlock()
}

func generateTOTPSecret(username string) (string, string, error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Command Guard", AccountName: username})
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

func (am *AuthManager) generateJWTFor(username, role string, tokenVersion int) (string, error) {
	now := time.Now()
	claims := &Claims{Username: username, Role: role, TokenVersion: tokenVersion, RegisteredClaims: jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour)), IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now),
	}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(am.jwtSecret)
}

func (am *AuthManager) generateJWT() (string, error) {
	return am.generateJWTFor(RoleAdmin, RoleAdmin, 0)
}

func (am *AuthManager) validateJWTSignature(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return am.jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}
	if claims.Username == "" {
		claims.Username = RoleAdmin
	}
	if claims.Role == "" {
		claims.Role = RoleAdmin
	}
	return claims, nil
}

// validateJWT preserves the existing internal signature. Request-aware validation is
// performed by authenticateRequest so IP/day/time policy is checked on every request.
func (am *AuthManager) validateJWT(tokenString string) (*Claims, error) {
	claims, err := am.validateJWTSignature(tokenString)
	if err != nil {
		return nil, err
	}
	if claims.Role == RoleAdmin {
		return claims, nil
	}
	am.mu.RLock()
	user, ok := am.users[canonicalUsername(claims.Username)]
	am.mu.RUnlock()
	if !ok || !user.Enabled || user.TokenVersion != claims.TokenVersion {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

func bearerToken(r *http.Request) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func (am *AuthManager) authenticateRequest(r *http.Request) (*Claims, error) {
	token := bearerToken(r)
	if token == "" {
		return nil, errors.New("missing token")
	}
	claims, err := am.validateJWT(token)
	if err != nil {
		return nil, err
	}
	if claims.Role == RoleUser {
		am.mu.RLock()
		user := am.users[canonicalUsername(claims.Username)]
		am.mu.RUnlock()
		if err := evaluateUserPolicy(user, getClientIP(r), time.Now()); err != nil {
			return nil, errors.New("access denied")
		}
	}
	return claims, nil
}

func (am *AuthManager) AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, err := am.authenticateRequest(r)
		if err != nil {
			sendJSONError(w, http.StatusUnauthorized, "Invalid or expired authentication")
			return
		}
		next(w, withClaims(r, claims))
	}
}

func (am *AuthManager) AdminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return am.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := ClaimsFromRequest(r)
		if claims.Role != RoleAdmin {
			sendJSONError(w, http.StatusForbidden, "Administrator access required")
			return
		}
		next(w, r)
	})
}

// executionMiddleware accepts a valid JWT or allows the handler to perform the
// configured per-item API-token check. Regular JWTs must hold the current grant
// for the item named by the path segment after prefix.
func (am *AuthManager) executionMiddleware(prefix string, hasGrant func(username, id string) bool, denied string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			next(w, r)
			return
		}
		claims, err := am.authenticateRequest(r)
		if err != nil {
			next(w, r)
			return
		}
		if claims.Role == RoleUser {
			id := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
			if !hasGrant(claims.Username, id) {
				sendJSONError(w, http.StatusForbidden, denied)
				return
			}
		}
		next(w, withClaims(r, claims))
	}
}

// CommandMiddleware guards /api/commands/{cmd_id}.
func (am *AuthManager) CommandMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return am.executionMiddleware("/api/commands/", am.userHasCommand, "Command access denied", next)
}

// HTTPRequestMiddleware guards /api/http-requests/{request_id}.
func (am *AuthManager) HTTPRequestMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return am.executionMiddleware("/api/http-requests/", am.userHasHTTPRequest, "HTTP request access denied", next)
}

func (am *AuthManager) userHasCommand(username, cmdID string) bool {
	am.mu.RLock()
	user, ok := am.users[canonicalUsername(username)]
	am.mu.RUnlock()
	return ok && containsString(user.CommandIDs, cmdID)
}

func (am *AuthManager) userHasRunBook(username, runBookID string) bool {
	am.mu.RLock()
	user, ok := am.users[canonicalUsername(username)]
	am.mu.RUnlock()
	return ok && containsString(user.RunBookIDs, runBookID)
}

func (am *AuthManager) userHasHTTPRequest(username, requestID string) bool {
	am.mu.RLock()
	user, ok := am.users[canonicalUsername(username)]
	am.mu.RUnlock()
	return ok && containsString(user.HTTPRequestIDs, requestID)
}

func (am *AuthManager) userHasTerminalHost(username, hostRef string) bool {
	am.mu.RLock()
	user, ok := am.users[canonicalUsername(username)]
	am.mu.RUnlock()
	return ok && containsString(user.TerminalHosts, hostRef)
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (am *AuthManager) isTrustedProxy(ip net.IP) bool {
	am.mu.RLock()
	rules := append([]string(nil), am.trustedProxies...)
	am.mu.RUnlock()
	for _, rule := range rules {
		if exact := net.ParseIP(strings.TrimSpace(rule)); exact != nil && exact.Equal(ip) {
			return true
		}
		if _, network, err := net.ParseCIDR(strings.TrimSpace(rule)); err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

// dummyPasswordHash lets unknown usernames pay the same bcrypt cost as real
// ones, so response time does not reveal whether an account exists.
var (
	dummyPasswordHashOnce sync.Once
	dummyPasswordHash     string
)

func equalizeLoginTiming(password string) {
	dummyPasswordHashOnce.Do(func() {
		hash, _ := bcrypt.GenerateFromPassword(bcryptMaterial("cg-login-timing-equalizer"), bcrypt.DefaultCost)
		dummyPasswordHash = string(hash)
	})
	verifyPassword(dummyPasswordHash, password)
}

func (am *AuthManager) totpAccepted(code, secret string) bool {
	am.mu.RLock()
	bypass := am.devMode && code == "123456"
	am.mu.RUnlock()
	return bypass || (secret != "" && totp.Validate(strings.TrimSpace(code), secret))
}

// recordLoginFailure audits a failed sign-in step and returns the lockout it
// triggered, if any.
func (am *AuthManager) recordLoginFailure(r *http.Request, sourceIP, username, role string) time.Duration {
	writeSecurityAudit(r, username, role, "login", username, "denied", nil)
	lockedFor := am.limiter.fail(sourceIP, username)
	if lockedFor > 0 {
		writeSecurityAudit(r, username, role, "login", username, "locked", map[string]interface{}{"source_ip": sourceIP, "locked_seconds": int(lockedFor.Seconds())})
	}
	return lockedFor
}

// rejectLogin answers every credential failure identically so the caller cannot
// tell whether the username, password or TOTP code was wrong.
func (am *AuthManager) rejectLogin(w http.ResponseWriter, r *http.Request, sourceIP, username, role string) {
	if lockedFor := am.recordLoginFailure(r, sourceIP, username, role); lockedFor > 0 {
		sendLoginLocked(w, lockedFor)
		return
	}
	sendJSONError(w, http.StatusUnauthorized, genericLoginError)
}

func (am *AuthManager) issueLoginToken(w http.ResponseWriter, r *http.Request, sourceIP, username, role string, tokenVersion int) {
	token, err := am.generateJWTFor(username, role, tokenVersion)
	if err != nil {
		sendJSONError(w, 500, "Failed to generate token")
		return
	}
	am.limiter.success(sourceIP, username)
	writeSecurityAudit(r, username, role, "login", username, "success", nil)
	writeJSON(w, 200, TwoFAResponse{Success: true, Token: token, Message: "Authentication successful"})
}

// HandleLogin verifies username, password and TOTP code in a single request.
// Accounts that have not enrolled an authenticator yet continue to enrollment
// once their password is verified.
func (am *AuthManager) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req LoginRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		sendJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	username := canonicalUsername(req.Username)
	if username == "" {
		username = RoleAdmin
	}
	sourceIP := getClientIP(r)
	if wait, locked := am.limiter.check(sourceIP, username); locked {
		sendLoginLocked(w, wait)
		return
	}
	if username == RoleAdmin {
		am.handleAdminLogin(w, r, req, sourceIP)
		return
	}
	am.mu.RLock()
	user, exists := am.users[username]
	devMode := am.devMode
	am.mu.RUnlock()
	passwordOK := false
	if exists {
		passwordOK = verifyPassword(user.PasswordHash, req.Password) || (devMode && req.Password == "abcdefgh")
	} else {
		equalizeLoginTiming(req.Password)
	}
	needsTOTP := strings.TrimSpace(user.TOTPSecret) == ""
	needsEnrollment := user.ForcePasswordChange || needsTOTP
	if !passwordOK || !user.Enabled || evaluateUserPolicy(user, sourceIP, time.Now()) != nil || (!needsEnrollment && !am.totpAccepted(req.TOTPCode, user.TOTPSecret)) {
		am.rejectLogin(w, r, sourceIP, username, RoleUser)
		return
	}
	if !needsEnrollment {
		am.issueLoginToken(w, r, sourceIP, username, RoleUser, user.TokenVersion)
		return
	}
	session := tempAuthSession{Username: username, Role: RoleUser, NeedsPassword: user.ForcePasswordChange, SourceIP: sourceIP, TokenVersion: user.TokenVersion}
	response := LoginResponse{Success: true, NeedsPasswordChange: user.ForcePasswordChange, NeedsTOTPSetup: needsTOTP, Message: "Password verified. Complete two-factor enrollment."}
	if needsTOTP {
		secret, url, err := generateTOTPSecret(username)
		if err != nil {
			sendJSONError(w, http.StatusInternalServerError, "Failed to prepare two-factor enrollment")
			return
		}
		session.PendingTOTPSecret = secret
		response.QRCode, _ = generateQRCodeDataURL(url)
	}
	response.TempToken = am.createTempSession(session)
	writeJSON(w, http.StatusOK, response)
}

func (am *AuthManager) handleAdminLogin(w http.ResponseWriter, r *http.Request, req LoginRequest, sourceIP string) {
	password := req.Password
	am.mu.RLock()
	current, secret, devMode := am.adminPassword, am.totpSecret, am.devMode
	am.mu.RUnlock()
	if current == "" {
		if err := validatePassword(password); err != nil {
			sendJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		secret, url, err := generateTOTPSecret(RoleAdmin)
		if err != nil {
			sendJSONError(w, 500, "Failed to generate TOTP secret")
			return
		}
		am.mu.Lock()
		am.adminPassword, am.totpSecret = password, secret
		am.mu.Unlock()
		if err := am.saveAuthConfig(); err != nil {
			sendJSONError(w, 500, "Failed to save configuration")
			return
		}
		qr, _ := generateQRCodeDataURL(url)
		writeJSON(w, http.StatusOK, LoginResponse{Success: true, NeedsSetup: true, QRCode: qr, Message: "Initial setup complete. Scan the two-factor QR code."})
		return
	}
	passwordOK := subtle.ConstantTimeCompare([]byte(password), []byte(current)) == 1 || (devMode && password == "abcdefgh")
	needsEnrollment := strings.TrimSpace(secret) == ""
	if !passwordOK || (!needsEnrollment && !am.totpAccepted(req.TOTPCode, secret)) {
		am.rejectLogin(w, r, sourceIP, RoleAdmin, RoleAdmin)
		return
	}
	if !needsEnrollment {
		am.issueLoginToken(w, r, sourceIP, RoleAdmin, RoleAdmin, 0)
		return
	}
	// A password exists but no authenticator was ever enrolled: enroll one
	// before issuing a session.
	pending, url, err := generateTOTPSecret(RoleAdmin)
	if err != nil {
		sendJSONError(w, http.StatusInternalServerError, "Failed to prepare two-factor enrollment")
		return
	}
	qr, _ := generateQRCodeDataURL(url)
	token := am.createTempSession(tempAuthSession{Username: RoleAdmin, Role: RoleAdmin, SourceIP: sourceIP, PendingTOTPSecret: pending})
	writeJSON(w, http.StatusOK, LoginResponse{Success: true, TempToken: token, NeedsTOTPSetup: true, QRCode: qr, Message: "Password verified. Complete two-factor enrollment."})
}

// HandleVerify2FA completes authenticator enrollment for an account whose
// password was verified by HandleLogin. Enrolled accounts sign in with a single
// HandleLogin request instead.
func (am *AuthManager) HandleVerify2FA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSONError(w, 405, "Method not allowed")
		return
	}
	var req TwoFARequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		sendJSONError(w, 400, "Invalid request body")
		return
	}
	sourceIP := getClientIP(r)
	session, ok := am.tempSession(req.TempToken)
	if !ok || session.PendingTOTPSecret == "" || session.SourceIP != sourceIP {
		sendJSONError(w, 401, "Invalid or expired temporary token")
		return
	}
	if session.NeedsPassword {
		sendJSONError(w, 409, "Password change is required before two-factor verification")
		return
	}
	if wait, locked := am.limiter.check(sourceIP, session.Username); locked {
		am.consumeTempSession(req.TempToken)
		sendLoginLocked(w, wait)
		return
	}
	if !am.totpAccepted(req.TOTPCode, session.PendingTOTPSecret) {
		if lockedFor := am.recordLoginFailure(r, sourceIP, session.Username, session.Role); lockedFor > 0 {
			am.consumeTempSession(req.TempToken)
			sendLoginLocked(w, lockedFor)
			return
		}
		sendJSONError(w, 401, "Invalid two-factor code")
		return
	}
	if session.Role == RoleAdmin {
		am.mu.Lock()
		previous := am.totpSecret
		if strings.TrimSpace(previous) != "" {
			// An authenticator was enrolled after this session started.
			am.mu.Unlock()
			am.consumeTempSession(req.TempToken)
			sendJSONError(w, 401, "Invalid or expired temporary token")
			return
		}
		am.totpSecret = session.PendingTOTPSecret
		am.mu.Unlock()
		if err := am.saveAuthConfig(); err != nil {
			am.mu.Lock()
			am.totpSecret = previous
			am.mu.Unlock()
			sendJSONError(w, 500, "Failed to save two-factor enrollment")
			return
		}
	} else {
		am.mu.Lock()
		user, exists := am.users[session.Username]
		if !exists || !user.Enabled || user.TokenVersion != session.TokenVersion {
			// Deleted, disabled or changed (password/TOTP reset) since the session started.
			am.mu.Unlock()
			am.consumeTempSession(req.TempToken)
			sendJSONError(w, 401, "Invalid or expired temporary token")
			return
		}
		previous := user
		user.TOTPSecret = session.PendingTOTPSecret
		user.TokenVersion++
		am.users[session.Username] = user
		am.mu.Unlock()
		if err := am.persistUsers(); err != nil {
			am.mu.Lock()
			am.users[session.Username] = previous
			am.mu.Unlock()
			sendJSONError(w, 500, "Failed to save two-factor enrollment")
			return
		}
	}
	am.consumeTempSession(req.TempToken)
	version := 0
	if session.Role == RoleUser {
		am.mu.RLock()
		version = am.users[session.Username].TokenVersion
		am.mu.RUnlock()
	}
	am.issueLoginToken(w, r, sourceIP, session.Username, session.Role, version)
}

func (am *AuthManager) HandleCompleteOnboarding(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSONError(w, 405, "Method not allowed")
		return
	}
	var req OnboardingRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req) != nil {
		sendJSONError(w, 400, "Invalid request body")
		return
	}
	sourceIP := getClientIP(r)
	session, ok := am.tempSession(req.TempToken)
	if !ok || session.Role != RoleUser || !session.NeedsPassword || session.SourceIP != sourceIP {
		sendJSONError(w, 401, "Invalid or expired onboarding session")
		return
	}
	if wait, locked := am.limiter.check(sourceIP, session.Username); locked {
		am.consumeTempSession(req.TempToken)
		sendLoginLocked(w, wait)
		return
	}
	if err := validatePassword(req.NewPassword); err != nil {
		sendJSONError(w, 400, err.Error())
		return
	}
	if session.PendingTOTPSecret == "" || (!am.devMode && !totp.Validate(req.TOTPCode, session.PendingTOTPSecret)) {
		if lockedFor := am.recordLoginFailure(r, sourceIP, session.Username, RoleUser); lockedFor > 0 {
			am.consumeTempSession(req.TempToken)
			sendLoginLocked(w, lockedFor)
			return
		}
		sendJSONError(w, 401, "Invalid two-factor code")
		return
	}
	hash, err := hashPassword(req.NewPassword)
	if err != nil {
		sendJSONError(w, 500, "Failed to secure password")
		return
	}
	am.mu.Lock()
	user, exists := am.users[session.Username]
	// A stale session (account deleted, disabled or reset since it started) must
	// not overwrite the current credentials.
	exists = exists && user.Enabled && user.TokenVersion == session.TokenVersion
	previous := user
	if exists {
		user.PasswordHash = hash
		user.ForcePasswordChange = false
		user.TOTPSecret = session.PendingTOTPSecret
		user.TokenVersion++
		am.users[session.Username] = user
	}
	am.mu.Unlock()
	if !exists {
		am.consumeTempSession(req.TempToken)
		sendJSONError(w, 401, "Invalid onboarding session")
		return
	}
	if err := am.persistUsers(); err != nil {
		am.mu.Lock()
		am.users[session.Username] = previous
		am.mu.Unlock()
		sendJSONError(w, 500, "Failed to save onboarding changes")
		return
	}
	am.consumeTempSession(req.TempToken)
	am.limiter.success(sourceIP, session.Username)
	token, _ := am.generateJWTFor(user.Username, RoleUser, user.TokenVersion)
	writeSecurityAudit(r, user.Username, RoleUser, "onboarding_complete", user.Username, "success", nil)
	writeJSON(w, 200, TwoFAResponse{Success: true, Token: token, Message: "Onboarding complete"})
}

func (am *AuthManager) HandleValidate(w http.ResponseWriter, r *http.Request) {
	claims, err := am.authenticateRequest(r)
	if err != nil {
		sendJSONSuccess(w, 200, map[string]interface{}{"valid": false}, "Invalid or expired token")
		return
	}
	data := map[string]interface{}{"valid": true, "username": claims.Username, "role": claims.Role, "expires_at": claims.ExpiresAt.Time.Format(time.RFC3339), "server_timezone": time.Now().Location().String()}
	sendJSONSuccess(w, 200, data, "Token is valid")
}

func (am *AuthManager) HandleLogout(w http.ResponseWriter, r *http.Request) {
	sendJSONSuccess(w, 200, nil, "Logged out successfully")
}

func (am *AuthManager) UpdateCredentials(adminPassword, totpSecret string, devMode bool, users []User, trustedProxies []string) {
	am.mu.Lock()
	defer am.mu.Unlock()
	if adminPassword != "" {
		am.adminPassword = adminPassword
	}
	if totpSecret != "" {
		am.totpSecret = totpSecret
	}
	am.devMode = devMode
	am.trustedProxies = append([]string(nil), trustedProxies...)
	am.replaceUsers(users)
}

func (am *AuthManager) RestoreCredentials(config AuthConfig) {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.adminPassword, am.totpSecret, am.devMode = config.AdminPassword, config.TOTPSecret, config.DevMode
	am.trustedProxies = append([]string(nil), config.TrustedProxies...)
	am.replaceUsers(config.Users)
	if config.JWTSecret != "" {
		if decoded, err := base64.StdEncoding.DecodeString(config.JWTSecret); err == nil {
			am.jwtSecret = decoded
		} else {
			am.jwtSecret = []byte(config.JWTSecret)
		}
	}
	am.tempSessions = make(map[string]tempAuthSession)
}

func (am *AuthManager) usersSnapshot() []User {
	am.mu.RLock()
	defer am.mu.RUnlock()
	result := make([]User, 0, len(am.users))
	for _, user := range am.users {
		result = append(result, user)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Username < result[j].Username })
	return result
}

func (am *AuthManager) persistJWTSecret() {
	_ = am.updateConfigSecrets(func(raw map[string]interface{}) { raw["jwt_secret"] = base64.StdEncoding.EncodeToString(am.jwtSecret) })
}
func (am *AuthManager) saveAuthConfig() error {
	return am.updateConfigSecrets(func(raw map[string]interface{}) {
		raw["admin_password"] = am.adminPassword
		raw["totp_secret"] = am.totpSecret
		raw["jwt_secret"] = base64.StdEncoding.EncodeToString(am.jwtSecret)
	})
}
func (am *AuthManager) persistUsers() error {
	users := am.usersSnapshot()
	return am.updateConfigSecrets(func(raw map[string]interface{}) { raw["users"] = users })
}

// updateConfigSecrets persists auth-owned fields (admin credentials, JWT secret,
// users) through the same locked read-modify-write path as every other config
// writer, so a credential change can neither lose nor be lost to a concurrent
// admin edit, and the in-memory config is reloaded immediately afterwards.
func (am *AuthManager) updateConfigSecrets(update func(map[string]interface{})) error {
	return NewConfigAPI(am.configPath, am).mutateConfig(func(raw map[string]interface{}) error {
		update(raw)
		return nil
	})
}

func generateQRCodeDataURL(url string) (string, error) {
	qr, err := otp.NewKeyFromURL(url)
	if err != nil {
		return "", err
	}
	img, err := qr.Image(256, 256)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	buf.WriteString("data:image/png;base64,")
	encoder := base64.NewEncoder(base64.StdEncoding, &buf)
	if err := png.Encode(encoder, img); err != nil {
		return "", err
	}
	_ = encoder.Close()
	return buf.String(), nil
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(value)
}
func sendJSONError(w http.ResponseWriter, statusCode int, message string) {
	sendJSONResponse(w, statusCode, APIResponse{Success: false, Error: message})
}
func sendJSONSuccess(w http.ResponseWriter, statusCode int, data interface{}, message string) {
	sendJSONResponse(w, statusCode, APIResponse{Success: true, Data: data, Error: message})
}

func evaluateUserPolicy(user User, clientIP string, now time.Time) error {
	if !user.Enabled {
		return errors.New("disabled")
	}
	temporary := false
	if value := strings.TrimSpace(user.TemporaryAnyIPUntil); value != "" {
		if expiry, err := time.Parse(time.RFC3339, value); err == nil && now.Before(expiry) {
			temporary = true
		}
	}
	if !temporary && !ipAllowed(clientIP, user.AllowedIPs) {
		return errors.New("ip denied")
	}
	if !weekdayAllowed(now.Weekday(), user.AllowedWeekdays) {
		return errors.New("weekday denied")
	}
	start, end := user.AllowedTimeStart, user.AllowedTimeEnd
	if user.DailyTimeWindow != nil {
		start, end = user.DailyTimeWindow.Start, user.DailyTimeWindow.End
	}
	if strings.TrimSpace(start) != "" || strings.TrimSpace(end) != "" {
		startMinutes, err1 := parseClock(start)
		endMinutes, err2 := parseClock(end)
		if err1 != nil || err2 != nil {
			return errors.New("time denied")
		}
		minute := now.Hour()*60 + now.Minute()
		if minute < startMinutes || minute >= endMinutes {
			return errors.New("time denied")
		}
	}
	return nil
}

func ipAllowed(value string, rules []string) bool {
	if len(rules) == 0 {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return false
	}
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule == "*" {
			return len(rules) == 1
		}
		if networkIP := net.ParseIP(rule); networkIP != nil && networkIP.Equal(ip) {
			return true
		}
		if _, network, err := net.ParseCIDR(rule); err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func weekdayAllowed(day time.Weekday, values []string) bool {
	if len(values) == 0 {
		return true
	}
	wanted := strings.ToLower(day.String())
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == wanted || value == wanted[:3] {
			return true
		}
	}
	return false
}

func parseClock(value string) (int, error) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}

func validateUser(user User, commandIDs, runBookIDs, httpRequestIDs, hostIDs, vncIDs map[string]bool) error {
	user = normalizeUser(user)
	if user.Username == "" || user.Username == RoleAdmin {
		return errors.New("username is required and admin is reserved")
	}
	for _, r := range user.Username {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' && r != '.' {
			return errors.New("username may contain lowercase letters, numbers, dots, and hyphens")
		}
	}
	if user.PasswordHash == "" {
		return errors.New("password hash is required")
	}
	if len(user.AllowedIPs) == 0 {
		user.AllowedIPs = []string{"*"}
	}
	for _, rule := range user.AllowedIPs {
		if rule == "*" {
			if len(user.AllowedIPs) != 1 {
				return errors.New("* must be the only allowed IP entry")
			}
			continue
		}
		if net.ParseIP(rule) == nil {
			if _, _, err := net.ParseCIDR(rule); err != nil {
				return fmt.Errorf("invalid allowed IP or CIDR %q", rule)
			}
		}
	}
	if len(user.AllowedWeekdays) > 0 {
		for _, value := range user.AllowedWeekdays {
			valid := false
			for _, day := range allWeekdays() {
				lower := strings.ToLower(value)
				if lower == day || lower == day[:3] {
					valid = true
				}
			}
			if !valid {
				return fmt.Errorf("invalid weekday %q", value)
			}
		}
	}
	start, end := user.AllowedTimeStart, user.AllowedTimeEnd
	if user.DailyTimeWindow != nil {
		start, end = user.DailyTimeWindow.Start, user.DailyTimeWindow.End
	}
	if (start == "") != (end == "") {
		return errors.New("both allowed time start and end are required")
	}
	if start != "" {
		a, e1 := parseClock(start)
		b, e2 := parseClock(end)
		if e1 != nil || e2 != nil || a >= b {
			return errors.New("time window must be valid, non-equal, and not overnight")
		}
	}
	if user.TemporaryAnyIPUntil != "" {
		if _, err := time.Parse(time.RFC3339, user.TemporaryAnyIPUntil); err != nil {
			return errors.New("temporary any-IP expiry must be an RFC3339 timestamp")
		}
	}
	for _, id := range user.CommandIDs {
		if !commandIDs[id] {
			return fmt.Errorf("user %q references missing command %q", user.Username, id)
		}
	}
	for _, id := range user.RunBookIDs {
		if !runBookIDs[id] {
			return fmt.Errorf("user %q references missing RunBook %q", user.Username, id)
		}
	}
	for _, id := range user.HTTPRequestIDs {
		if !httpRequestIDs[id] {
			return fmt.Errorf("user %q references missing HTTP request %q", user.Username, id)
		}
	}
	for _, id := range user.TerminalHosts {
		if !hostIDs[id] {
			return fmt.Errorf("user %q references missing SSH host %q for terminals", user.Username, id)
		}
	}
	return validateVNCGrants(user.Username, user.VNCAccess, vncIDs)
}

// sshHostIDs returns the IDs of hosts that can back a persistent terminal (SSH only).
func sshHostIDs(hosts map[string]Host) map[string]bool {
	result := make(map[string]bool, len(hosts))
	for id, host := range hosts {
		if host.ConnectionType == "" || strings.EqualFold(host.ConnectionType, "ssh") {
			result[id] = true
		}
	}
	return result
}

func validateSecurityConfig(users []User, trustedProxies []string, commands []Command, runBooks []RunBook, httpRequests []HTTPRequest, hosts map[string]Host, vncConnections []VNCConnection) error {
	commandIDs, runBookIDs := make(map[string]bool), make(map[string]bool)
	for _, command := range commands {
		commandIDs[command.CmdID] = true
	}
	for _, runBook := range runBooks {
		runBookIDs[runBook.RunBookID] = true
	}
	httpRequestIDs := httpRequestIDSet(httpRequests)
	hostIDs := sshHostIDs(hosts)
	vncIDs := vncConnectionIDSet(vncConnections)
	seen := make(map[string]bool)
	for _, user := range users {
		user = normalizeUser(user)
		if seen[user.Username] {
			return fmt.Errorf("duplicate user %q", user.Username)
		}
		seen[user.Username] = true
		if err := validateUser(user, commandIDs, runBookIDs, httpRequestIDs, hostIDs, vncIDs); err != nil {
			return err
		}
	}
	for _, rule := range trustedProxies {
		rule = strings.TrimSpace(rule)
		if rule == "*" || rule == "" {
			return fmt.Errorf("invalid trusted proxy %q", rule)
		}
		if net.ParseIP(rule) == nil {
			if _, _, err := net.ParseCIDR(rule); err != nil {
				return fmt.Errorf("invalid trusted proxy IP or CIDR %q", rule)
			}
		}
	}
	return nil
}
