package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/gomail.v2"
)

// Helper functions for JSON responses to match original signature
func sendConfigJSONError(w http.ResponseWriter, statusCode int, message string) {
	response := APIResponse{
		Success: false,
		Error:   message,
	}
	sendJSONResponse(w, statusCode, response)
}

func sendConfigJSONSuccess(w http.ResponseWriter, statusCode int, data interface{}, message string) {
	response := APIResponse{
		Success: true,
		Data:    data,
		Error:   message,
	}
	sendJSONResponse(w, statusCode, response)
}

var logSMTPTestFailure = logSMTPError

// validateCommandID validates that a command ID only contains alphanumeric characters and hyphens
// Returns true if valid, false if invalid (contains underscores or other invalid characters)
func validateCommandID(cmdID string) bool {
	if cmdID == "" {
		return false
	}

	// Check each character - only allow letters, numbers, and hyphens
	for _, char := range cmdID {
		if !((char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '-') {
			return false
		}
	}

	return true
}

// ConfigAPI handles configuration management endpoints
type ConfigAPI struct {
	configPath  string
	authManager *AuthManager
}

// NewConfigAPI creates a new configuration API handler
func NewConfigAPI(configPath string, authManager *AuthManager) *ConfigAPI {
	return &ConfigAPI{
		configPath:  configPath,
		authManager: authManager,
	}
}

// HandleGetConfig returns the current configuration (sanitized)
func (ca *ConfigAPI) HandleGetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Read config directly from file to ensure fresh data
	configData, err := ioutil.ReadFile(ca.configPath)
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to read configuration file")
		return
	}

	// Parse the config
	var config Config
	if err := json.Unmarshal(configData, &config); err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to parse configuration")
		return
	}

	// Create sanitized copy (remove passwords)
	sanitized := ca.sanitizeConfig(&config)

	sendConfigJSONSuccess(w, http.StatusOK, sanitized, "Configuration retrieved successfully")
}

// HandleUpdateConfig handles updating the entire configuration
func (ca *ConfigAPI) HandleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Parse request body
	var newConfig map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&newConfig); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid JSON in request body")
		return
	}

	// A failed reload after saving is only logged, so a bad value written here would
	// block hot reload of every other setting.
	if rawTerminals, ok := newConfig["terminals"]; ok {
		var terminals TerminalsConfig
		data, _ := json.Marshal(rawTerminals)
		if err := json.Unmarshal(data, &terminals); err != nil {
			sendConfigJSONError(w, http.StatusBadRequest, "Invalid terminals settings")
			return
		}
		if err := validateTerminalsConfig(&terminals); err != nil {
			sendConfigJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// The existing config is read under the config lock so that sensitive fields
	// (GET /api/config strips passwords via sanitizeConfig, so they won't be in the
	// incoming request) are copied from the file as it is at the moment of saving.
	err := ca.mutateConfig(func(existingConfig map[string]interface{}) error {
		// Preserve all authentication-only fields. They are intentionally absent
		// from GET /api/config and are managed through dedicated security APIs.
		// vnc_connections is managed only by /api/config/vnc-connections (its
		// passwords are redacted from GET /api/config).
		for _, field := range []string{"admin_password", "totp_secret", "jwt_secret", "users", "vnc_connections"} {
			if val, ok := existingConfig[field]; ok {
				newConfig[field] = val
			} else {
				delete(newConfig, field)
			}
		}

		// Preserve host passwords
		if newHosts, ok := newConfig["hosts"].(map[string]interface{}); ok {
			if existingHosts, ok := existingConfig["hosts"].(map[string]interface{}); ok {
				for hostName, existingHostRaw := range existingHosts {
					existingHost, ok := existingHostRaw.(map[string]interface{})
					if !ok {
						continue
					}
					newHost, ok := newHosts[hostName].(map[string]interface{})
					if !ok {
						continue
					}
					for _, field := range []string{"password", "pem_password", "ssh_password"} {
						if _, has := newHost[field]; !has {
							if val, ok := existingHost[field]; ok {
								newHost[field] = val
							}
						}
					}
					// A host key recorded since the page loaded its copy must not be
					// lost; it is forgotten only through the host's own API.
					if _, has := newHost["host_key"]; !has && !hostAddressChanged(existingHost, newHost) {
						if val, ok := existingHost["host_key"]; ok {
							newHost["host_key"] = val
						}
					}
				}
			}
		}

		// Preserve SMTP password
		if newSMTP, ok := newConfig["smtp"].(map[string]interface{}); ok {
			if existingSMTP, ok := existingConfig["smtp"].(map[string]interface{}); ok {
				if _, has := newSMTP["password"]; !has {
					if val, ok := existingSMTP["password"]; ok {
						newSMTP["password"] = val
					}
				}
			}
		}

		// Preserve S3 secret access key
		if newS3, ok := newConfig["s3"].(map[string]interface{}); ok {
			if existingS3, ok := existingConfig["s3"].(map[string]interface{}); ok {
				if _, has := newS3["secret_access_key"]; !has {
					if val, ok := existingS3["secret_access_key"]; ok {
						newS3["secret_access_key"] = val
					}
				}
			}
		}

		// Replace the whole document with the merged request
		for key := range existingConfig {
			delete(existingConfig, key)
		}
		for key, value := range newConfig {
			existingConfig[key] = value
		}
		return validateVNCRaw(existingConfig)
	})
	if err != nil {
		sendConfigMutationError(w, err, "Failed to save configuration")
		return
	}

	sendConfigJSONSuccess(w, http.StatusOK, map[string]bool{"updated": true}, "Configuration updated successfully")
}

// sanitizeConfig removes sensitive information from config
func (ca *ConfigAPI) sanitizeConfig(config *Config) map[string]interface{} {
	// Convert to map for manipulation
	jsonData, _ := json.Marshal(config)
	var result map[string]interface{}
	json.Unmarshal(jsonData, &result)

	// Remove sensitive fields
	if hosts, ok := result["hosts"].(map[string]interface{}); ok {
		for _, host := range hosts {
			if h, ok := host.(map[string]interface{}); ok {
				delete(h, "password")
				delete(h, "pem_password")
				delete(h, "ssh_password")
				// The host key is public; add its fingerprint for display
				if hostKey, _ := h["host_key"].(string); hostKey != "" {
					if keyType, fingerprint, ok := hostKeyInfo(hostKey); ok {
						h["host_key_type"] = keyType
						h["host_key_fingerprint"] = fingerprint
					}
				}
			}
		}
	}

	if smtp, ok := result["smtp"].(map[string]interface{}); ok {
		password, _ := smtp["password"].(string)
		delete(smtp, "password")
		smtp["has_password"] = password != ""
		smtp["password_length"] = utf8.RuneCountInString(password)
	}

	if s3Config, ok := result["s3"].(map[string]interface{}); ok {
		secretValue, hasSecret := s3Config["secret_access_key"].(string)
		hasSecret = hasSecret && strings.TrimSpace(secretValue) != ""
		delete(s3Config, "secret_access_key")
		s3Config["has_secret_access_key"] = hasSecret
		if hasSecret {
			s3Config["secret_access_key_length"] = utf8.RuneCountInString(secretValue)
		} else {
			s3Config["secret_access_key_length"] = 0
		}
	}

	if conns, ok := result["vnc_connections"].([]interface{}); ok {
		for _, value := range conns {
			if c, ok := value.(map[string]interface{}); ok {
				password, _ := c["password"].(string)
				delete(c, "password")
				c["has_password"] = password != ""
			}
		}
	}

	// Remove JWT secret from API responses
	delete(result, "jwt_secret")
	delete(result, "admin_password")
	delete(result, "totp_secret")
	if users, ok := result["users"].([]interface{}); ok {
		for _, value := range users {
			if user, ok := value.(map[string]interface{}); ok {
				delete(user, "password_hash")
				delete(user, "totp_secret")
			}
		}
	}

	// Note: Bearer tokens are now preserved for authenticated admin users
	// The authentication middleware ensures only admins can access this endpoint
	// so it's safe to include bearer tokens in the response

	// Note: Bearer tokens are preserved for authenticated admin users
	// The authentication middleware ensures only admins can access this endpoint

	return result
}

// HandleGetHosts returns all hosts
func (ca *ConfigAPI) HandleGetHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	config := cache.GetConfig()
	if config == nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Configuration not loaded")
		return
	}

	// Convert hosts map to array with names
	hosts := make([]map[string]interface{}, 0)
	for name, host := range config.Hosts {
		hostData := map[string]interface{}{
			"name":             name,
			"connection_type":  host.ConnectionType,
			"host":             host.Host,
			"port":             host.Port,
			"username":         host.Username,
			"has_password":     host.Password != "",
			"pem_file":         host.PemFile,
			"has_pem_password": host.PemPwd != "",
			"bastion_host_ref": host.BastionHostRef,
		}
		if keyType, fingerprint, ok := hostKeyInfo(host.HostKey); ok {
			hostData["host_key"] = strings.TrimSpace(host.HostKey)
			hostData["host_key_type"] = keyType
			hostData["host_key_fingerprint"] = fingerprint
		}

		if host.ConnectionType == "winrm" {
			hostData["winrm_https"] = host.WinRMHTTPS
			hostData["winrm_insecure"] = host.WinRMInsecure
			hostData["winrm_cacert"] = host.WinRMCACert
		}

		hosts = append(hosts, hostData)
	}

	sendConfigJSONSuccess(w, http.StatusOK, hosts, "Hosts retrieved successfully")
}

// HandleCreateHost creates a new host
func (ca *ConfigAPI) HandleCreateHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var hostData map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&hostData); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	name, ok := hostData["name"].(string)
	if !ok || name == "" {
		sendConfigJSONError(w, http.StatusBadRequest, "Host name is required")
		return
	}

	// Remove name from hostData as it's the key
	delete(hostData, "name")

	err := ca.mutateConfig(func(config map[string]interface{}) error {
		// Get or create hosts section
		hosts, ok := config["hosts"].(map[string]interface{})
		if !ok {
			hosts = make(map[string]interface{})
			config["hosts"] = hosts
		}

		// Check if host already exists
		if _, exists := hosts[name]; exists {
			return httpErr(http.StatusConflict, "Host already exists")
		}

		if _, err := applyHostKeyField(nil, hostData); err != nil {
			return err
		}

		// Add the new host
		hosts[name] = hostData
		return validateHostsBastions(config)
	})
	if err != nil {
		sendConfigMutationError(w, err, "Failed to save configuration")
		return
	}

	sendConfigJSONSuccess(w, http.StatusCreated, hostResponse(hostData), "Host created successfully")
}

// HandleUpdateHost updates an existing host
func (ca *ConfigAPI) HandleUpdateHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Extract host name from URL
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid URL")
		return
	}
	hostName := parts[4]

	var hostData map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&hostData); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	hostKeyChanged := false
	err := ca.mutateConfig(func(config map[string]interface{}) error {
		// Get hosts section
		hosts, ok := config["hosts"].(map[string]interface{})
		if !ok {
			return httpErr(http.StatusNotFound, "No hosts configured")
		}

		// Check if host exists
		existingHost, exists := hosts[hostName].(map[string]interface{})
		if !exists {
			return httpErr(http.StatusNotFound, "Host not found")
		}

		// Preserve password if not provided in update
		if _, hasPassword := hostData["password"]; !hasPassword {
			if existingPassword, ok := existingHost["password"]; ok {
				hostData["password"] = existingPassword
			}
		}

		// Preserve pem_password if not provided in update
		if _, hasPemPassword := hostData["pem_password"]; !hasPemPassword {
			if existingPemPassword, ok := existingHost["pem_password"]; ok {
				hostData["pem_password"] = existingPemPassword
			}
		}

		// Preserve the bastion reference when the request leaves the key out
		// (send "" to switch the host back to a direct connection)
		if _, hasBastion := hostData["bastion_host_ref"]; !hasBastion {
			if existingBastion, ok := existingHost["bastion_host_ref"]; ok {
				hostData["bastion_host_ref"] = existingBastion
			}
		}

		// Keep the stored host key when the field is left out and the address is
		// unchanged; "" forgets it (see applyHostKeyField)
		changed, err := applyHostKeyField(existingHost, hostData)
		if err != nil {
			return err
		}
		hostKeyChanged = changed

		// Update the host
		hosts[hostName] = hostData
		if err := validateHostsBastions(config); err != nil {
			return err
		}
		return validateVNCRaw(config)
	})
	if err != nil {
		sendConfigMutationError(w, err, "Failed to save configuration")
		return
	}

	if hostKeyChanged {
		actor, role := currentActor(r)
		details := map[string]interface{}{"key": "none (forgotten)"}
		if keyType, fingerprint, ok := hostKeyInfo(fmt.Sprint(hostData["host_key"])); ok {
			details["key"] = keyType + " " + fingerprint
		}
		writeSecurityAudit(r, actor, role, "ssh_host_key_changed", hostName, "success", details)
	}

	sendConfigJSONSuccess(w, http.StatusOK, hostResponse(hostData), "Host updated successfully")
}

// HandleDeleteHost deletes a host
func (ca *ConfigAPI) HandleDeleteHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Extract host name from URL
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid URL")
		return
	}
	hostName := parts[4]

	err := ca.mutateConfig(func(config map[string]interface{}) error {
		// Get hosts section
		hosts, ok := config["hosts"].(map[string]interface{})
		if !ok {
			return httpErr(http.StatusNotFound, "No hosts configured")
		}

		// Check if host exists
		if _, exists := hosts[hostName]; !exists {
			return httpErr(http.StatusNotFound, "Host not found")
		}
		if users := usersGranting(config["users"], "terminal", hostName); len(users) > 0 {
			return httpErr(http.StatusConflict, fmt.Sprintf("Cannot delete host '%s' because it is granted as a terminal host to user '%s'", hostName, users[0]))
		}
		if n := diskMonitorHostUsage(config, hostName); n > 0 {
			return httpErr(http.StatusConflict, fmt.Sprintf("Cannot delete host '%s' because %d disk monitor volume(s) reference it", hostName, n))
		}
		if n := agentSessionHostUsage(config, hostName); n > 0 {
			return httpErr(http.StatusConflict, fmt.Sprintf("Cannot delete host '%s' because %d shared Agent Session(s) on the Terminals page use it", hostName, n))
		}
		if dependents := bastionDependents(decodeHostsMap(config["hosts"]), hostName); len(dependents) > 0 {
			return httpErr(http.StatusConflict, fmt.Sprintf("Cannot delete host '%s' because host '%s' uses it as bastion host", hostName, dependents[0]))
		}
		if conns := vncTunnelUsage(config, hostName); len(conns) > 0 {
			return httpErr(http.StatusConflict, fmt.Sprintf("Cannot delete host '%s' because Remote Desktop connection '%s' tunnels through it", hostName, conns[0]))
		}

		// Delete the host
		delete(hosts, hostName)
		return validateHostsBastions(config)
	})
	if err != nil {
		sendConfigMutationError(w, err, "Failed to save configuration")
		return
	}

	sendConfigJSONSuccess(w, http.StatusOK, nil, "Host deleted successfully")
}

// HandleGetCommands returns all commands
func (ca *ConfigAPI) HandleGetCommands(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	config := cache.GetConfig()
	if config == nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Configuration not loaded")
		return
	}

	// Sanitize commands (remove bearer tokens)
	commands := make([]map[string]interface{}, len(config.Commands))
	for i, cmd := range config.Commands {
		cmdData := map[string]interface{}{
			"cmd_id":           cmd.CmdID,
			"host_ref":         cmd.HostRef,
			"command":          cmd.Command,
			"description":      cmd.Description,
			"cron_schedule":    cmd.CronSchedule,
			"has_bearer_token": cmd.BearerToken != "",
			"max_output_size":  cmd.MaxOutputSize,
			"notify_webhook":   cmd.NotifyWebhook,
			"notify_email":     cmd.NotifyEmail,
			"log_storage":      cmd.LogStorage,
			"skip_if_running":  cmd.SkipIfRunning,
			"timeout_seconds":  cmd.TimeoutSeconds,
		}
		commands[i] = cmdData
	}

	sendConfigJSONSuccess(w, http.StatusOK, commands, "Commands retrieved successfully")
}

// HandleCreateCommand creates a new command
func (ca *ConfigAPI) HandleCreateCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var cmdData map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&cmdData); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	cmdID, ok := cmdData["cmd_id"].(string)
	if !ok || cmdID == "" {
		sendConfigJSONError(w, http.StatusBadRequest, "Command ID is required")
		return
	}

	// Validate command ID format (no underscores, only alphanumeric and hyphens)
	if !validateCommandID(cmdID) {
		sendConfigJSONError(w, http.StatusBadRequest, "Command ID can only contain letters, numbers, and hyphens. Underscores and other special characters are not allowed.")
		return
	}

	err := ca.mutateConfig(func(config map[string]interface{}) error {
		// Get or create commands array
		commands, ok := config["commands"].([]interface{})
		if !ok {
			commands = make([]interface{}, 0)
		}

		// Check if command already exists
		for _, cmd := range commands {
			if c, ok := cmd.(map[string]interface{}); ok {
				if id, ok := c["cmd_id"].(string); ok && id == cmdID {
					return httpErr(http.StatusConflict, "Command already exists")
				}
			}
		}

		// Add the new command
		config["commands"] = append(commands, cmdData)
		return nil
	})
	if err != nil {
		sendConfigMutationError(w, err, "Failed to save configuration")
		return
	}

	sendConfigJSONSuccess(w, http.StatusCreated, cmdData, "Command created successfully")
}

// HandleUpdateCommand updates an existing command
func (ca *ConfigAPI) HandleUpdateCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Extract command ID from URL
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid URL")
		return
	}
	cmdID := parts[4]

	var cmdData map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&cmdData); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Validate command ID in the request body (if provided)
	if newCmdID, ok := cmdData["cmd_id"].(string); ok && newCmdID != "" {
		if !validateCommandID(newCmdID) {
			sendConfigJSONError(w, http.StatusBadRequest, "Command ID can only contain letters, numbers, and hyphens. Underscores and other special characters are not allowed.")
			return
		}
	}

	err := ca.mutateConfig(func(config map[string]interface{}) error {
		// Get commands array
		commands, ok := config["commands"].([]interface{})
		if !ok {
			return httpErr(http.StatusNotFound, "No commands configured")
		}

		// Find and update the command
		for i, cmd := range commands {
			if c, ok := cmd.(map[string]interface{}); ok {
				if id, ok := c["cmd_id"].(string); ok && id == cmdID {
					cmdData["cmd_id"] = cmdID // Ensure cmd_id is not changed
					commands[i] = cmdData
					config["commands"] = commands
					return nil
				}
			}
		}
		return httpErr(http.StatusNotFound, "Command not found")
	})
	if err != nil {
		sendConfigMutationError(w, err, "Failed to save configuration")
		return
	}

	sendConfigJSONSuccess(w, http.StatusOK, cmdData, "Command updated successfully")
}

// HandleDeleteCommand deletes a command
func (ca *ConfigAPI) HandleDeleteCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Extract command ID from URL
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid URL")
		return
	}
	cmdID := parts[4]

	err := ca.mutateConfig(func(config map[string]interface{}) error {
		// Get commands array
		commands, ok := config["commands"].([]interface{})
		if !ok {
			return httpErr(http.StatusNotFound, "No commands configured")
		}
		runBooks, _ := decodeRunBooks(config["runbooks"])
		for _, runBook := range runBooks {
			for _, step := range runBook.Steps {
				if step.CmdID == cmdID {
					return httpErr(http.StatusConflict, fmt.Sprintf("Cannot delete command '%s' because RunBook '%s' uses it", cmdID, runBook.RunBookID))
				}
			}
		}
		if users := usersGranting(config["users"], "command", cmdID); len(users) > 0 {
			return httpErr(http.StatusConflict, fmt.Sprintf("Cannot delete command '%s' because it is granted to user '%s'", cmdID, users[0]))
		}

		// Find and remove the command
		newCommands := make([]interface{}, 0)
		found := false
		for _, cmd := range commands {
			if c, ok := cmd.(map[string]interface{}); ok {
				if id, ok := c["cmd_id"].(string); ok && id == cmdID {
					found = true
					continue
				}
			}
			newCommands = append(newCommands, cmd)
		}
		if !found {
			return httpErr(http.StatusNotFound, "Command not found")
		}
		config["commands"] = newCommands
		return nil
	})
	if err != nil {
		sendConfigMutationError(w, err, "Failed to save configuration")
		return
	}

	sendConfigJSONSuccess(w, http.StatusOK, nil, "Command deleted successfully")
}

// HandleGetLogs returns list of log files
func (ca *ConfigAPI) HandleGetLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Parse optional limit parameter (0 = all)
	limit := 0
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	logsDir := "logs"
	files, err := ioutil.ReadDir(logsDir)
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to read logs directory")
		return
	}

	// Filter to .txt files only
	type logFile struct {
		info os.FileInfo
	}
	var txtFiles []logFile
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".txt") {
			txtFiles = append(txtFiles, logFile{info: file})
		}
	}
	// Sort by modification time (newest first) BEFORE reading file contents
	sort.Slice(txtFiles, func(i, j int) bool {
		return txtFiles[i].info.ModTime().After(txtFiles[j].info.ModTime())
	})

	logs := make([]map[string]interface{}, 0, len(txtFiles))
	for _, lf := range txtFiles {
		if limit > 0 && len(logs) >= limit {
			break
		}
		name := lf.info.Name()

		// Parse filename: YYYY_MM_DD_HHMMSS_cmdid_traceid_log.txt
		// Split known prefix (4 parts: YYYY, MM, DD, HHMMSS) and work backwards from _log.txt
		var cmdID, traceID string
		base := strings.TrimSuffix(name, ".txt") // remove .txt
		base = strings.TrimSuffix(base, "_log")  // remove _log
		parts := strings.SplitN(base, "_", 5)    // split into at most 5: YYYY, MM, DD, HHMMSS, rest
		if len(parts) >= 5 {
			rest := parts[4] // everything after HHMMSS_
			// rest = "cmdid_traceid" — trace_id is the last segment (16 hex chars)
			lastUnderscore := strings.LastIndex(rest, "_")
			if lastUnderscore > 0 {
				cmdID = rest[:lastUnderscore]
				traceID = rest[lastUnderscore+1:]
			} else {
				cmdID = rest
			}
		}

		// Read only the first 20 lines to extract host and exitcode
		host := "Unknown"
		status := "Unknown"
		logPath := filepath.Join(logsDir, name)
		if !canAccessLogFile(r, logPath) {
			continue
		}
		metadata := logMetadata(logPath)
		if metadata["host"] != "" {
			host = metadata["host"]
		}
		switch metadata["exitcode"] {
		case "0":
			status = "Success"
		case "-1":
			status = "Running"
		case "":
			status = "Unknown"
		default:
			status = "Failed"
		}

		logInfo := map[string]interface{}{
			"filename":   name,
			"size":       lf.info.Size(),
			"modified":   lf.info.ModTime().Format(time.RFC3339),
			"cmd_id":     cmdID,
			"trace_id":   traceID,
			"host":       host,
			"status":     status,
			"runbook_id": metadata["runbook_id"],
			"invoked_by": metadata["invoked_by"],
			"trigger":    metadata["trigger"],
			"job_type":   metadata["job_type"],
			"request_id": metadata["request_id"],
		}
		logs = append(logs, logInfo)
	}

	sendConfigJSONSuccess(w, http.StatusOK, logs, "Logs retrieved successfully")
}

// HandleDeleteLog deletes a single log file
func (ca *ConfigAPI) HandleDeleteLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Extract filename from URL path: /api/logs/delete/{filename}
	path := r.URL.Path
	prefix := "/api/logs/delete/"
	if !strings.HasPrefix(path, prefix) {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid path")
		return
	}
	filename := path[len(prefix):]
	if filename == "" {
		sendConfigJSONError(w, http.StatusBadRequest, "Filename is required")
		return
	}

	// Security: ensure filename doesn't contain path traversal
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid filename")
		return
	}

	// Ensure it's a .txt file in the logs directory
	if !strings.HasSuffix(filename, ".txt") {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid log file")
		return
	}

	logPath := filepath.Join("logs", filename)

	// Check if file exists
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		sendConfigJSONError(w, http.StatusNotFound, "Log file not found")
		return
	}

	// Delete the file
	if err := os.Remove(logPath); err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to delete log file: "+err.Error())
		return
	}

	sendConfigJSONSuccess(w, http.StatusOK, nil, "Log file deleted successfully")
}

// HandleClearOldLogs deletes log files older than specified days
func (ca *ConfigAPI) HandleClearOldLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Get days parameter from query string
	daysStr := r.URL.Query().Get("days")
	if daysStr == "" {
		sendConfigJSONError(w, http.StatusBadRequest, "Days parameter is required")
		return
	}

	days, err := strconv.Atoi(daysStr)
	if err != nil || days < 1 {
		sendConfigJSONError(w, http.StatusBadRequest, "Days must be a positive integer")
		return
	}

	logsDir := "logs"
	files, err := ioutil.ReadDir(logsDir)
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to read logs directory")
		return
	}

	cutoffTime := time.Now().AddDate(0, 0, -days)
	deletedCount := 0

	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".txt") {
			if file.ModTime().Before(cutoffTime) {
				logPath := filepath.Join(logsDir, file.Name())
				if err := os.Remove(logPath); err == nil {
					deletedCount++
				}
			}
		}
	}
	deletedCount += runBookManager.PruneBefore(cutoffTime)

	result := map[string]interface{}{
		"deleted_count":   deletedCount,
		"older_than_days": days,
	}
	sendConfigJSONSuccess(w, http.StatusOK, result, fmt.Sprintf("Deleted %d log files older than %d days", deletedCount, days))
}

// HandleExecuteCommand executes a command
func (ca *ConfigAPI) HandleExecuteCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Extract command ID from URL
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid URL")
		return
	}
	cmdID := parts[4]

	// Create a synthetic request to the existing handler
	// We'll need to modify this to call the actual execution function
	config := cache.GetConfig()
	if config == nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Configuration not loaded")
		return
	}

	// Find the command
	var targetCmd *Command
	for _, cmd := range config.Commands {
		if cmd.CmdID == cmdID {
			targetCmd = &cmd
			break
		}
	}

	if targetCmd == nil {
		sendConfigJSONError(w, http.StatusNotFound, "Command not found")
		return
	}

	// Execute the command asynchronously and return job ID
	jobID := generateTraceID()

	go func() {
		// This will be replaced with actual execution logic
		// For now, we'll just return the job ID
	}()

	data := map[string]interface{}{
		"job_id": jobID,
		"status": "started",
	}

	sendConfigJSONSuccess(w, http.StatusAccepted, data, "Command execution started")
}

// HandleGetVariables returns global variables
func (ca *ConfigAPI) HandleGetVariables(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	config := cache.GetConfig()
	if config == nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Configuration not loaded")
		return
	}

	sendConfigJSONSuccess(w, http.StatusOK, config.Variables, "Variables retrieved successfully")
}

// HandleUpdateVariables updates global variables
func (ca *ConfigAPI) HandleUpdateVariables(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var variables map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&variables); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	err := ca.mutateConfig(func(config map[string]interface{}) error {
		config["variables"] = variables
		return nil
	})
	if err != nil {
		sendConfigMutationError(w, err, "Failed to save configuration")
		return
	}

	sendConfigJSONSuccess(w, http.StatusOK, variables, "Variables updated successfully")
}

// HandleGetSMTP returns SMTP configuration
func (ca *ConfigAPI) HandleGetSMTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	configData, err := ioutil.ReadFile(ca.configPath)
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to read configuration")
		return
	}
	var config Config
	if err := json.Unmarshal(configData, &config); err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to parse configuration")
		return
	}

	sendConfigJSONSuccess(w, http.StatusOK, sanitizedSMTPData(config.SMTP, config.SMTP == nil), "SMTP configuration retrieved successfully")
}

// HandleUpdateSMTP updates SMTP configuration
func (ca *ConfigAPI) HandleUpdateSMTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var smtpConfig SMTPConfig
	if err := json.NewDecoder(r.Body).Decode(&smtpConfig); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	var normalized *SMTPConfig
	err := ca.mutateConfig(func(config map[string]interface{}) error {
		// Preserve the existing password when the client intentionally omits it.
		if existingSMTP, ok := config["smtp"].(map[string]interface{}); ok {
			if smtpConfig.Password == "" {
				if existingPassword, ok := existingSMTP["password"].(string); ok {
					smtpConfig.Password = existingPassword
				}
			}
		}

		var err error
		normalized, err = normalizeSMTPConfig(&smtpConfig, true)
		if err != nil {
			return httpErr(http.StatusBadRequest, err.Error())
		}
		if err := validateSMTPConfig(normalized); err != nil {
			return httpErr(http.StatusBadRequest, err.Error())
		}

		config["smtp"] = normalized
		return nil
	})
	if err != nil {
		sendConfigMutationError(w, err, "Failed to save configuration")
		return
	}

	sendConfigJSONSuccess(w, http.StatusOK, sanitizedSMTPData(normalized, false), "SMTP configuration updated successfully")
}

// HandleSendSMTPTestEmail sends a real email using the saved SMTP configuration.
func (ca *ConfigAPI) HandleSendSMTPTestEmail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var request struct {
		To string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	recipient := strings.TrimSpace(request.To)
	address, err := mail.ParseAddress(recipient)
	if err != nil || address.Address != recipient || address.Name != "" {
		sendConfigJSONError(w, http.StatusBadRequest, "A valid test email address is required")
		return
	}

	if err := cache.loadIfChanged(); err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to load SMTP configuration")
		return
	}
	config := cache.GetConfig()
	if config == nil || config.SMTP == nil {
		sendConfigJSONError(w, http.StatusBadRequest, "SMTP configuration is not set")
		return
	}
	if !config.SMTP.isEnabled() {
		sendConfigJSONError(w, http.StatusBadRequest, "SMTP is disabled")
		return
	}
	normalized, err := normalizeSMTPConfig(config.SMTP, false)
	if err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateSMTPConfig(normalized); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	sentAt := time.Now()
	const subject = "Command Guard Test Email"
	message := gomail.NewMessage()
	message.SetHeader("To", recipient)
	message.SetHeader("Subject", subject)
	message.SetBody("text/plain", fmt.Sprintf(
		"This is a test email from Command Guard.\n\nYour SMTP settings are working correctly.\n\nSent at: %s\n",
		sentAt.Format(time.RFC3339),
	))
	if err := sendSMTPMessage(normalized, message); err != nil {
		logSMTPTestFailure(fmt.Sprintf("Failed to send SMTP test email to %s: %v", recipient, err))
		sendConfigJSONError(w, http.StatusBadGateway, "Failed to send test email: "+err.Error())
		return
	}

	fromEmail, fromName, _ := normalized.senderParts()
	from := (&mail.Address{Name: fromName, Address: fromEmail}).String()
	sendConfigJSONSuccess(w, http.StatusOK, map[string]interface{}{
		"to":      recipient,
		"from":    from,
		"subject": subject,
		"sent_at": sentAt.Format(time.RFC3339),
	}, "Test email sent successfully")
}

// HandleTestS3 tests S3 settings without saving them.
func (ca *ConfigAPI) HandleTestS3(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var s3Data map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&s3Data); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	configData, err := ioutil.ReadFile(ca.configPath)
	if err == nil {
		var existingConfig map[string]interface{}
		if json.Unmarshal(configData, &existingConfig) == nil {
			if existingS3, ok := existingConfig["s3"].(map[string]interface{}); ok {
				secretValue, hasIncomingSecret := s3Data["secret_access_key"].(string)
				if !hasIncomingSecret || strings.TrimSpace(secretValue) == "" {
					if existingSecret, ok := existingS3["secret_access_key"]; ok {
						s3Data["secret_access_key"] = existingSecret
					}
				}
			}
		}
	}

	var s3Config S3Config
	jsonData, err := json.Marshal(s3Data)
	if err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid S3 settings")
		return
	}
	if err := json.Unmarshal(jsonData, &s3Config); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid S3 settings")
		return
	}

	if err := testS3Config(r.Context(), &s3Config); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "S3 test failed: "+err.Error())
		return
	}

	sendConfigJSONSuccess(w, http.StatusOK, map[string]bool{"connected": true}, "S3 settings tested successfully")
}

// HandleScheduledJobsOverviewEmail manages the global daily overview email setting.
func (ca *ConfigAPI) HandleScheduledJobsOverviewEmail(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if err := cache.loadIfChanged(); err != nil {
			sendConfigJSONError(w, http.StatusInternalServerError, "Failed to load Scheduled Jobs Overview email settings")
			return
		}
		config := cache.GetConfig()
		if config == nil {
			sendConfigJSONError(w, http.StatusInternalServerError, "Configuration is not loaded")
			return
		}
		sendConfigJSONSuccess(w, http.StatusOK, scheduledJobsOverviewEmailSettingsResponse(config), "Scheduled Jobs Overview email settings retrieved successfully")
	case http.MethodPut:
		var requested ScheduledJobsOverviewEmailConfig
		if err := json.NewDecoder(r.Body).Decode(&requested); err != nil {
			sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		requested = normalizeScheduledJobsOverviewEmailConfig(&requested)
		if err := cache.loadIfChanged(); err != nil {
			sendConfigJSONError(w, http.StatusInternalServerError, "Failed to load SMTP configuration")
			return
		}
		currentConfig := cache.GetConfig()
		if currentConfig == nil {
			sendConfigJSONError(w, http.StatusInternalServerError, "Configuration is not loaded")
			return
		}
		if err := validateScheduledJobsOverviewEmailConfig(requested, currentConfig.SMTP, true, currentConfig.Variables); err != nil {
			sendConfigJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		err := ca.mutateConfig(func(rawConfig map[string]interface{}) error {
			rawConfig["scheduled_jobs_overview_email"] = requested
			return nil
		})
		if err != nil {
			sendConfigMutationError(w, err, "Failed to save Scheduled Jobs Overview email settings")
			return
		}
		savedConfig := cache.GetConfig()
		sendConfigJSONSuccess(w, http.StatusOK, scheduledJobsOverviewEmailSettingsResponse(savedConfig), "Scheduled Jobs Overview email settings updated successfully")
	default:
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// validateHostsBastions checks every bastion_host_ref in the raw hosts map and
// returns a 400 configHTTPError describing the first problem, so a bad reference is
// rejected before it reaches config.json.
func validateHostsBastions(config map[string]interface{}) error {
	if err := resolveBastionHosts(decodeHostsMap(config["hosts"])); err != nil {
		return httpErr(http.StatusBadRequest, err.Error())
	}
	return nil
}

// configHTTPError lets a mutateConfig callback abort the save with a specific
// HTTP status and message instead of a generic 500.
type configHTTPError struct {
	Status  int
	Message string
}

func (e *configHTTPError) Error() string { return e.Message }

// httpErr builds a configHTTPError for use inside mutateConfig callbacks.
func httpErr(status int, message string) error {
	return &configHTTPError{Status: status, Message: message}
}

// sendConfigMutationError writes the HTTP error for a failed mutateConfig call:
// the callback's own status/message when it returned a configHTTPError, and
// otherwise a 500 with the given fallback message (read, encode or write failure).
func sendConfigMutationError(w http.ResponseWriter, err error, fallback string) {
	var httpError *configHTTPError
	if errors.As(err, &httpError) {
		sendConfigJSONError(w, httpError.Status, httpError.Message)
		return
	}
	sendConfigJSONError(w, http.StatusInternalServerError, fallback)
}

// mutateConfig is the single read-modify-write path for config.json. It holds
// configFileMutex across the read, the callback and the save so that two
// concurrent edits (two admin tabs, a user schedule change during a command
// edit, a TOTP enrolment during a host edit, ...) can never overwrite each
// other. fn receives the raw on-disk document and edits it in place; returning
// an error aborts without writing and the error is returned unchanged.
func (ca *ConfigAPI) mutateConfig(fn func(cfg map[string]interface{}) error) error {
	configFileMutex.Lock()
	defer configFileMutex.Unlock()

	cfg, err := loadConfigMap(ca.configPath)
	if err != nil {
		return err
	}
	if err := fn(cfg); err != nil {
		return err
	}
	return ca.saveConfigLocked(cfg)
}

// saveConfig saves the configuration to file with ordered fields. Prefer
// mutateConfig for anything that first reads the file; saveConfig alone is only
// safe when the caller already holds a complete document of its own.
func (ca *ConfigAPI) saveConfig(configMap map[string]interface{}) error {
	configFileMutex.Lock()
	defer configFileMutex.Unlock()
	return ca.saveConfigLocked(configMap)
}

// saveConfigLocked writes configMap to disk. The caller must hold configFileMutex.
func (ca *ConfigAPI) saveConfigLocked(configMap map[string]interface{}) error {
	// Convert the map to JSON first
	mapJSON, err := json.Marshal(configMap)
	if err != nil {
		return err
	}

	// Parse into a fresh Config struct to maintain field order
	// This ensures deletions are properly reflected
	var config Config
	if err := json.Unmarshal(mapJSON, &config); err != nil {
		return err
	}
	config.Version = AppVersion

	// Marshal the Config struct with proper formatting
	// The struct tags will maintain the original field order
	// Use a buffer and encoder to disable HTML escaping
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false) // Disable HTML escaping to prevent < > from becoming \u003c \u003e
	encoder.SetIndent("", "\t")

	if err := encoder.Encode(config); err != nil {
		return err
	}

	// Get the JSON data (Encode adds a trailing newline, so we keep it)
	jsonData := buf.Bytes()

	// Remember the current file so a rejected save can be undone. It is only
	// restored when it was itself loadable; otherwise a save must still be able
	// to repair a config that is already broken.
	previous, prevErr := ioutil.ReadFile(ca.configPath)
	previousValid := prevErr == nil && cache.loadIfChanged() == nil

	// Write to temporary file first (owner-only: the file holds credentials)
	tempFile := ca.configPath + ".tmp"
	if err := ioutil.WriteFile(tempFile, jsonData, 0600); err != nil {
		return err
	}
	if err := os.Chmod(tempFile, 0600); err != nil { // WriteFile keeps the mode of a leftover .tmp
		os.Remove(tempFile)
		return err
	}

	// Rename to actual config file
	if err := os.Rename(tempFile, ca.configPath); err != nil {
		return err
	}

	// Trigger config reload to update cron schedules and other cached state
	// This ensures the scheduledJobs map is updated immediately after config changes
	if err := cache.loadIfChanged(); err != nil {
		if previousValid {
			// The loader refuses the new file (for example a command that points at
			// a missing host). Leaving it on disk would make every later execution
			// fail on reload, so put the previous file back and report the reason.
			if werr := ioutil.WriteFile(tempFile, previous, 0600); werr == nil {
				if rerr := os.Rename(tempFile, ca.configPath); rerr == nil {
					_ = cache.loadIfChanged()
					return httpErr(http.StatusBadRequest, fmt.Sprintf("Configuration was not saved because it is invalid: %v", err))
				}
			}
		}
		// Log the error but don't fail the save operation
		fmt.Printf("[CONFIG] Warning: Failed to reload config after save: %v\n", err)
	}

	return nil
}

// hostResponse returns hostData for an API response without its secrets. The
// host list never returns passwords; create/update answers must not either.
func hostResponse(hostData map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(hostData)+2)
	for key, value := range hostData {
		if key == "password" || key == "pem_password" {
			continue
		}
		out[key] = value
	}
	password, _ := hostData["password"].(string)
	pemPassword, _ := hostData["pem_password"].(string)
	out["has_password"] = password != ""
	out["has_pem_password"] = pemPassword != ""
	if hostKey, _ := hostData["host_key"].(string); hostKey != "" {
		if keyType, fingerprint, ok := hostKeyInfo(hostKey); ok {
			out["host_key_type"] = keyType
			out["host_key_fingerprint"] = fingerprint
		}
	}
	return out
}

// marshalOrderedJSON marshals a map in a specific order without HTML escaping
func marshalOrderedJSON(config map[string]interface{}) ([]byte, error) {
	// Define the complete field order for the config
	fieldOrder := []string{
		"version", "port", "bind_address", "access_url", "cron_enabled", "log_retention_days", "audit_retention_days", "dev_mode",
		"admin_password", "totp_secret", "jwt_secret", "trusted_proxies", "ssh_host_key_checking", "users", "variables", "smtp", "scheduled_jobs_overview_email", "s3",
		"terminals", "disk_monitor", "vnc", "hosts", "commands", "runbooks", "http_requests", "vnc_connections",
	}

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "\t")

	// Build ordered map
	orderedMap := make(map[string]interface{})

	// Add fields in the specified order
	for _, field := range fieldOrder {
		if value, exists := config[field]; exists {
			orderedMap[field] = value
		}
	}

	// Add any remaining fields
	for key, value := range config {
		if _, exists := orderedMap[key]; !exists {
			orderedMap[key] = value
		}
	}

	// Now we need to manually build the JSON to maintain order
	var result bytes.Buffer
	result.WriteString("{\n")

	writtenFields := 0

	// Write fields in the specified order
	for _, field := range fieldOrder {
		if value, exists := orderedMap[field]; exists {
			if writtenFields > 0 {
				result.WriteString(",\n")
			}

			// Marshal field name (this won't have HTML escaping issues)
			result.WriteString("\t\"")
			result.WriteString(field)
			result.WriteString("\": ")

			// Marshal the value without HTML escaping
			var valueBuf bytes.Buffer
			valueEncoder := json.NewEncoder(&valueBuf)
			valueEncoder.SetEscapeHTML(false)
			if err := valueEncoder.Encode(value); err != nil {
				return nil, err
			}

			// Remove the trailing newline from Encode
			valueBytes := bytes.TrimRight(valueBuf.Bytes(), "\n")

			// Add indentation for multi-line values
			lines := bytes.Split(valueBytes, []byte("\n"))
			for i, line := range lines {
				if i > 0 {
					result.WriteString("\n\t")
				}
				result.Write(line)
			}

			writtenFields++
			delete(orderedMap, field)
		}
	}

	// Write any remaining fields
	for key, value := range orderedMap {
		if writtenFields > 0 {
			result.WriteString(",\n")
		}

		result.WriteString("\t\"")
		result.WriteString(key)
		result.WriteString("\": ")

		// Marshal the value without HTML escaping
		var valueBuf bytes.Buffer
		valueEncoder := json.NewEncoder(&valueBuf)
		valueEncoder.SetEscapeHTML(false)
		if err := valueEncoder.Encode(value); err != nil {
			return nil, err
		}

		// Remove the trailing newline
		valueBytes := bytes.TrimRight(valueBuf.Bytes(), "\n")

		// Add indentation
		lines := bytes.Split(valueBytes, []byte("\n"))
		for i, line := range lines {
			if i > 0 {
				result.WriteString("\n\t")
			}
			result.Write(line)
		}

		writtenFields++
	}

	result.WriteString("\n}")

	// Pretty format the result
	var prettyBuf bytes.Buffer
	if err := json.Indent(&prettyBuf, result.Bytes(), "", "\t"); err != nil {
		// If indenting fails, return the original
		return result.Bytes(), nil
	}

	return prettyBuf.Bytes(), nil
}

// HandleLogContent returns a complete command log.
func (ca *ConfigAPI) HandleLogContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Extract trace ID from URL path: /api/logs/{traceId}/content
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid URL")
		return
	}
	traceID := parts[3] // traceId is at index 3

	// Validate trace_id format (must be exactly 16 lowercase hex characters)
	if !isValidTraceID(traceID) {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid trace ID format")
		return
	}

	// Find the log file
	logsDir := "logs"
	files, err := ioutil.ReadDir(logsDir)
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to read logs directory")
		return
	}

	var logFile string
	for _, file := range files {
		if strings.Contains(file.Name(), traceID) {
			logFile = filepath.Join(logsDir, file.Name())
			break
		}
	}

	if logFile == "" {
		sendConfigJSONError(w, http.StatusNotFound, "Log file not found")
		return
	}
	if !canAccessLogFile(r, logFile) {
		sendConfigJSONError(w, http.StatusForbidden, "Log access denied")
		return
	}
	if isLogChunkRequest(r.URL.Query()) {
		serveLogChunk(w, r, traceID, logFile)
		return
	}

	content, storage, s3URI, err := resolveStoredLogContent(r.Context(), logFile)
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to read log content: "+err.Error())
		return
	}

	// Parse cmd_id and host from log content
	contentStr := string(content)
	fields := parseLogKeyValueFields(contentStr)
	cmdID := fields["cmd_id"]
	host := fields["host"]

	// Get file info for size and modification time
	fileInfo, err := os.Stat(logFile)
	var fileSize int64
	var modTime time.Time

	if err == nil && fileInfo != nil {
		fileSize = int64(len(content))
		modTime = fileInfo.ModTime()
	} else {
		// If we can't get file info, use the content length as size
		fileSize = int64(len(content))
		modTime = time.Now() // Use current time as fallback
	}

	data := map[string]interface{}{
		"trace_id": traceID,
		"content":  contentStr,
		"filename": filepath.Base(logFile),
		"cmd_id":   cmdID,
		"host":     host,
		"job_type": fields["job_type"],
		"size":     fileSize,
		"modified": modTime,
		"storage":  storage,
		"s3_uri":   s3URI,
	}

	sendConfigJSONSuccess(w, http.StatusOK, data, "Log content retrieved successfully")
}

// HandleLogDownload downloads a log file
func (ca *ConfigAPI) HandleLogDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract trace ID from URL path: /api/logs/{traceId}/download
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}
	traceID := parts[3] // traceId is at index 3

	// Validate trace_id format (must be exactly 16 lowercase hex characters)
	if !isValidTraceID(traceID) {
		http.Error(w, "Invalid trace ID format", http.StatusBadRequest)
		return
	}

	// Find the log file
	logsDir := "logs"
	files, err := ioutil.ReadDir(logsDir)
	if err != nil {
		http.Error(w, "Failed to read logs directory", http.StatusInternalServerError)
		return
	}

	var logFile string
	var fileName string
	for _, file := range files {
		if strings.Contains(file.Name(), traceID) {
			fileName = file.Name()
			logFile = filepath.Join(logsDir, fileName)
			break
		}
	}

	if logFile == "" {
		http.Error(w, "Log file not found", http.StatusNotFound)
		return
	}
	if !canAccessLogFile(r, logFile) {
		http.Error(w, "Log access denied", http.StatusForbidden)
		return
	}

	content, storage, s3URI, err := resolveStoredLogContent(r.Context(), logFile)
	if err != nil {
		http.Error(w, "Failed to read log content: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if storage == "s3" && s3URI != "" {
		if _, key, err := parseS3URI(s3URI); err == nil && filepath.Base(key) != "" {
			fileName = filepath.Base(key)
		}
	}

	// Set headers for file download
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fileName))
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))

	if _, err := w.Write(content); err != nil {
		fmt.Printf("Error streaming log download: %v\n", err)
	}
}
