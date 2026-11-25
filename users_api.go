package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

type userMutationRequest struct {
	User
	TemporaryPassword string `json:"temporary_password"`
}

func sanitizedUser(user User) map[string]interface{} {
	return map[string]interface{}{
		"username": user.Username, "enabled": user.Enabled, "force_password_change": user.ForcePasswordChange,
		"totp_enrolled": user.TOTPSecret != "", "token_version": user.TokenVersion,
		"command_ids": user.CommandIDs, "runbook_ids": user.RunBookIDs, "http_request_ids": user.HTTPRequestIDs, "terminal_hosts": user.TerminalHosts,
		"vnc_access":  user.VNCAccess,
		"allowed_ips": user.AllowedIPs, "allowed_weekdays": user.AllowedWeekdays,
		"allowed_time_start": user.AllowedTimeStart, "allowed_time_end": user.AllowedTimeEnd,
		"daily_time_window": user.DailyTimeWindow, "temporary_any_ip_until": user.TemporaryAnyIPUntil,
	}
}

func currentActor(r *http.Request) (string, string) {
	if claims, ok := ClaimsFromRequest(r); ok {
		return claims.Username, claims.Role
	}
	return "unknown", "unknown"
}

func configIDSets() (map[string]bool, map[string]bool, map[string]bool, map[string]bool, map[string]bool) {
	commands, runBooks, httpRequests, hosts, vncs := make(map[string]bool), make(map[string]bool), make(map[string]bool), make(map[string]bool), make(map[string]bool)
	if cfg := cache.GetConfig(); cfg != nil {
		for _, command := range cfg.Commands {
			commands[command.CmdID] = true
		}
		for _, runBook := range cfg.RunBooks {
			runBooks[runBook.RunBookID] = true
		}
		httpRequests = httpRequestIDSet(cfg.HTTPRequests)
		hosts = sshHostIDs(cfg.Hosts)
		vncs = vncConnectionIDSet(cfg.VNCConnections)
	}
	return commands, runBooks, httpRequests, hosts, vncs
}

func usersGranting(value interface{}, kind, id string) []string {
	data, _ := json.Marshal(value)
	var users []User
	_ = json.Unmarshal(data, &users)
	result := make([]string, 0)
	for _, user := range users {
		grants := user.CommandIDs
		switch kind {
		case "runbook":
			grants = user.RunBookIDs
		case "http_request":
			grants = user.HTTPRequestIDs
		case "terminal":
			grants = user.TerminalHosts
		case "vnc":
			grants = nil
			for _, g := range user.VNCAccess {
				grants = append(grants, g.VNCID)
			}
		}
		if containsString(grants, id) {
			result = append(result, user.Username)
		}
	}
	return result
}

func (am *AuthManager) HandleUsers(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/users"), "/")
	if path == "" {
		switch r.Method {
		case http.MethodGet:
			users := am.usersSnapshot()
			result := make([]map[string]interface{}, 0, len(users))
			for _, user := range users {
				result = append(result, sanitizedUser(user))
			}
			sendJSONSuccess(w, 200, map[string]interface{}{"users": result, "server_timezone": nowTimezone()}, "Users retrieved")
		case http.MethodPost:
			var req userMutationRequest
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req) != nil {
				sendJSONError(w, 400, "Invalid request body")
				return
			}
			req.User.Username = canonicalUsername(req.User.Username)
			if req.User.Username == RoleAdmin {
				sendJSONError(w, 400, "The admin username is reserved")
				return
			}
			hash, err := hashPassword(req.TemporaryPassword)
			if err != nil {
				sendJSONError(w, 400, err.Error())
				return
			}
			req.User.PasswordHash = hash
			req.User.ForcePasswordChange = true
			req.User.TokenVersion = 1
			req.User.TOTPSecret = ""
			req.User = normalizeUser(req.User)
			commands, runBooks, httpRequests, hosts, vncs := configIDSets()
			if err := validateUser(req.User, commands, runBooks, httpRequests, hosts, vncs); err != nil {
				sendJSONError(w, 400, err.Error())
				return
			}
			am.mu.Lock()
			if _, exists := am.users[req.User.Username]; exists {
				am.mu.Unlock()
				sendJSONError(w, 409, "User already exists")
				return
			}
			am.users[req.User.Username] = req.User
			am.mu.Unlock()
			if err := am.persistUsers(); err != nil {
				am.mu.Lock()
				delete(am.users, req.User.Username)
				am.mu.Unlock()
				sendJSONError(w, 500, "Failed to save user")
				return
			}
			actor, role := currentActor(r)
			writeSecurityAudit(r, actor, role, "user_create", req.User.Username, "success", map[string]interface{}{"command_ids": req.User.CommandIDs, "runbook_ids": req.User.RunBookIDs, "http_request_ids": req.User.HTTPRequestIDs, "terminal_hosts": req.User.TerminalHosts, "vnc_access": req.User.VNCAccess})
			sendJSONSuccess(w, 201, sanitizedUser(req.User), "User created")
		default:
			sendJSONError(w, 405, "Method not allowed")
		}
		return
	}
	parts := strings.Split(path, "/")
	username := canonicalUsername(parts[0])
	if username == "" || username == RoleAdmin {
		sendJSONError(w, 400, "Invalid username")
		return
	}
	am.mu.RLock()
	existing, exists := am.users[username]
	am.mu.RUnlock()
	if !exists {
		sendJSONError(w, 404, "User not found")
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	actor, role := currentActor(r)
	if action != "" {
		if r.Method != http.MethodPost {
			sendJSONError(w, 405, "Method not allowed")
			return
		}
		switch action {
		case "enable", "disable":
			enabled := action == "enable"
			am.mu.Lock()
			user := am.users[username]
			if user.Enabled != enabled {
				user.Enabled = enabled
				user.TokenVersion++
			}
			am.users[username] = user
			am.mu.Unlock()
			if err := am.persistUsers(); err != nil {
				am.mu.Lock()
				am.users[username] = existing
				am.mu.Unlock()
				sendJSONError(w, 500, "Failed to save user")
				return
			}
			writeSecurityAudit(r, actor, role, "user_"+action, username, "success", nil)
			sendJSONSuccess(w, 200, sanitizedUser(user), "User updated")
		case "reset-password", "password-reset":
			var body struct {
				TemporaryPassword string `json:"temporary_password"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
				sendJSONError(w, 400, "Invalid request body")
				return
			}
			hash, err := hashPassword(body.TemporaryPassword)
			if err != nil {
				sendJSONError(w, 400, err.Error())
				return
			}
			am.mu.Lock()
			user := am.users[username]
			user.PasswordHash = hash
			user.ForcePasswordChange = true
			user.TOTPSecret = ""
			user.TokenVersion++
			am.users[username] = user
			am.mu.Unlock()
			if err := am.persistUsers(); err != nil {
				am.mu.Lock()
				am.users[username] = existing
				am.mu.Unlock()
				sendJSONError(w, 500, "Failed to save password reset")
				return
			}
			writeSecurityAudit(r, actor, role, "password_reset", username, "success", nil)
			sendJSONSuccess(w, 200, sanitizedUser(user), "Password reset; onboarding is required")
		case "reset-totp", "totp-reset":
			am.mu.Lock()
			user := am.users[username]
			user.TOTPSecret = ""
			user.TokenVersion++
			am.users[username] = user
			am.mu.Unlock()
			if err := am.persistUsers(); err != nil {
				am.mu.Lock()
				am.users[username] = existing
				am.mu.Unlock()
				sendJSONError(w, 500, "Failed to save TOTP reset")
				return
			}
			writeSecurityAudit(r, actor, role, "totp_reset", username, "success", nil)
			sendJSONSuccess(w, 200, sanitizedUser(user), "Two-factor enrollment reset")
		default:
			sendJSONError(w, 404, "Unknown user action")
		}
		return
	}
	switch r.Method {
	case http.MethodGet:
		sendJSONSuccess(w, 200, sanitizedUser(existing), "User retrieved")
	case http.MethodPut:
		var req User
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req) != nil {
			sendJSONError(w, 400, "Invalid request body")
			return
		}
		if req.Username != "" && canonicalUsername(req.Username) != username {
			sendJSONError(w, 400, "Username is immutable")
			return
		}
		req.Username = username
		req.PasswordHash = existing.PasswordHash
		req.TOTPSecret = existing.TOTPSecret
		req.TokenVersion = existing.TokenVersion
		req.ForcePasswordChange = existing.ForcePasswordChange
		req = normalizeUser(req)
		commands, runBooks, httpRequests, hosts, vncs := configIDSets()
		if err := validateUser(req, commands, runBooks, httpRequests, hosts, vncs); err != nil {
			sendJSONError(w, 400, err.Error())
			return
		}
		if existing.Enabled != req.Enabled {
			req.TokenVersion++
		}
		am.mu.Lock()
		am.users[username] = req
		am.mu.Unlock()
		if err := am.persistUsers(); err != nil {
			am.mu.Lock()
			am.users[username] = existing
			am.mu.Unlock()
			sendJSONError(w, 500, "Failed to save user")
			return
		}
		writeSecurityAudit(r, actor, role, "user_update", username, "success", map[string]interface{}{"command_ids": req.CommandIDs, "runbook_ids": req.RunBookIDs, "http_request_ids": req.HTTPRequestIDs, "terminal_hosts": req.TerminalHosts, "vnc_access": req.VNCAccess})
		sendJSONSuccess(w, 200, sanitizedUser(req), "User updated")
	case http.MethodDelete:
		am.mu.Lock()
		delete(am.users, username)
		am.mu.Unlock()
		if err := am.persistUsers(); err != nil {
			am.mu.Lock()
			am.users[username] = existing
			am.mu.Unlock()
			sendJSONError(w, 500, "Failed to delete user")
			return
		}
		writeSecurityAudit(r, actor, role, "user_delete", username, "success", nil)
		sendJSONSuccess(w, 200, nil, "User deleted")
	default:
		sendJSONError(w, 405, "Method not allowed")
	}
}

func nowTimezone() string { return timeNow().Location().String() }

var timeNow = func() time.Time { return time.Now() }

func (am *AuthManager) HandleAccountPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		sendJSONError(w, 405, "Method not allowed")
		return
	}
	claims, ok := ClaimsFromRequest(r)
	if !ok || claims.Role != RoleUser {
		sendJSONError(w, 400, "Regular user account required")
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		sendJSONError(w, 400, "Invalid request body")
		return
	}
	am.mu.RLock()
	user, exists := am.users[claims.Username]
	am.mu.RUnlock()
	if !exists || !verifyPassword(user.PasswordHash, req.CurrentPassword) {
		sendJSONError(w, 401, "Current password is invalid")
		return
	}
	previous := user
	hash, err := hashPassword(req.NewPassword)
	if err != nil {
		sendJSONError(w, 400, err.Error())
		return
	}
	am.mu.Lock()
	user = am.users[claims.Username]
	user.PasswordHash = hash
	user.ForcePasswordChange = false
	user.TokenVersion++
	am.users[claims.Username] = user
	am.mu.Unlock()
	if err := am.persistUsers(); err != nil {
		am.mu.Lock()
		am.users[claims.Username] = previous
		am.mu.Unlock()
		sendJSONError(w, 500, "Failed to save password")
		return
	}
	writeSecurityAudit(r, claims.Username, claims.Role, "password_change", claims.Username, "success", nil)
	sendJSONSuccess(w, 200, nil, "Password changed; sign in again")
}

func (am *AuthManager) HandleAssignedCommands(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromRequest(r)
	if !ok {
		sendJSONError(w, 401, "Authentication required")
		return
	}
	if r.Method != http.MethodGet {
		sendJSONError(w, 405, "Method not allowed")
		return
	}
	cfg := cache.GetConfig()
	if cfg == nil {
		sendJSONError(w, 500, "Configuration unavailable")
		return
	}
	result := make([]map[string]interface{}, 0)
	for _, cmd := range cfg.Commands {
		if claims.Role == RoleAdmin || am.userHasCommand(claims.Username, cmd.CmdID) {
			result = append(result, map[string]interface{}{"cmd_id": cmd.CmdID, "description": cmd.Description, "host_ref": cmd.HostRef, "cron_schedule": cmd.CronSchedule, "execution_available": true})
		}
	}
	sendJSONSuccess(w, 200, result, "Commands retrieved")
}

func (am *AuthManager) HandleAssignedRunBooks(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromRequest(r)
	if !ok {
		sendJSONError(w, 401, "Authentication required")
		return
	}
	if r.Method != http.MethodGet {
		sendJSONError(w, 405, "Method not allowed")
		return
	}
	cfg := cache.GetConfig()
	if cfg == nil {
		sendJSONError(w, 500, "Configuration unavailable")
		return
	}
	result := make([]map[string]interface{}, 0)
	for _, rb := range cfg.RunBooks {
		if claims.Role == RoleAdmin || am.userHasRunBook(claims.Username, rb.RunBookID) {
			steps := make([]map[string]string, 0, len(rb.Steps))
			for _, step := range rb.Steps {
				steps = append(steps, map[string]string{"step_id": step.StepID, "cmd_id": step.CmdID, "request_id": step.RequestID, "label": step.Label})
			}
			result = append(result, map[string]interface{}{"runbook_id": rb.RunBookID, "description": rb.Description, "cron_schedule": rb.CronSchedule, "steps": steps, "execution_available": true})
		}
	}
	sendJSONSuccess(w, 200, result, "RunBooks retrieved")
}

// HandleAssignedHTTPRequests lists the HTTP requests the caller may run (GET /api/user/http-requests).
func (am *AuthManager) HandleAssignedHTTPRequests(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromRequest(r)
	if !ok {
		sendJSONError(w, 401, "Authentication required")
		return
	}
	if r.Method != http.MethodGet {
		sendJSONError(w, 405, "Method not allowed")
		return
	}
	cfg := cache.GetConfig()
	if cfg == nil {
		sendJSONError(w, 500, "Configuration unavailable")
		return
	}
	result := make([]map[string]interface{}, 0)
	for _, req := range cfg.HTTPRequests {
		if claims.Role == RoleAdmin || am.userHasHTTPRequest(claims.Username, req.RequestID) {
			result = append(result, map[string]interface{}{"request_id": req.RequestID, "description": req.Description, "method": req.effectiveMethod(), "url": req.URL, "cron_schedule": req.CronSchedule, "execution_available": true})
		}
	}
	sendJSONSuccess(w, 200, result, "HTTP requests retrieved")
}

type scheduleRequest struct {
	CronSchedule string `json:"cron_schedule"`
}

func validateFiveFieldCron(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	_, err := parser.Parse(value)
	return err
}

func (am *AuthManager) HandleUserSchedule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		sendJSONError(w, 405, "Method not allowed")
		return
	}
	claims, ok := ClaimsFromRequest(r)
	if !ok {
		sendJSONError(w, 401, "Authentication required")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/user/"), "/")
	parts := strings.Split(path, "/")
	scheduleKinds := map[string][2]string{"commands": {"commands", "cmd_id"}, "runbooks": {"runbooks", "runbook_id"}, "http-requests": {"http_requests", "request_id"}}
	if len(parts) != 3 || parts[2] != "schedule" {
		sendJSONError(w, 404, "Invalid schedule path")
		return
	}
	kind, id := parts[0], parts[1]
	keys, known := scheduleKinds[kind]
	if !known {
		sendJSONError(w, 404, "Invalid schedule path")
		return
	}
	if claims.Role == RoleUser && ((kind == "commands" && !am.userHasCommand(claims.Username, id)) || (kind == "runbooks" && !am.userHasRunBook(claims.Username, id)) || (kind == "http-requests" && !am.userHasHTTPRequest(claims.Username, id))) {
		sendJSONError(w, 403, "Schedule access denied")
		return
	}
	var req scheduleRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		sendJSONError(w, 400, "Invalid request body")
		return
	}
	req.CronSchedule = strings.TrimSpace(req.CronSchedule)
	if err := validateFiveFieldCron(req.CronSchedule); err != nil {
		sendJSONError(w, 400, "Invalid five-field cron schedule")
		return
	}
	err := NewConfigAPI(am.configPath, am).mutateConfig(func(raw map[string]interface{}) error {
		items, _ := raw[keys[0]].([]interface{})
		for _, value := range items {
			item, _ := value.(map[string]interface{})
			if item[keys[1]] == id {
				if req.CronSchedule == "" {
					delete(item, "cron_schedule")
				} else {
					item["cron_schedule"] = req.CronSchedule
				}
				return nil
			}
		}
		return httpErr(404, "Scheduled item not found")
	})
	if err != nil {
		var httpError *configHTTPError
		if errors.As(err, &httpError) {
			sendJSONError(w, httpError.Status, httpError.Message)
			return
		}
		sendJSONError(w, 500, "Failed to save schedule")
		return
	}
	writeSecurityAudit(r, claims.Username, claims.Role, "schedule_change", kind+":"+id, "success", map[string]interface{}{"cron_schedule": req.CronSchedule})
	sendJSONSuccess(w, 200, map[string]string{"cron_schedule": req.CronSchedule}, fmt.Sprintf("%s schedule updated", strings.TrimSuffix(strings.ReplaceAll(kind, "-", " "), "s")))
}

func passwordEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
