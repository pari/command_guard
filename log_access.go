package main

import (
	"bufio"
	"net/http"
	"os"
	"strings"
)

func logMetadata(path string) map[string]string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	fields := make(map[string]string)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if key == "output" {
			break
		}
		if isKnownLogFieldKey(key) {
			fields[key] = strings.TrimSpace(parts[1])
		}
	}
	return fields
}

func canAccessLogFile(r *http.Request, path string) bool {
	claims, ok := ClaimsFromRequest(r)
	if !ok {
		return false
	}
	if claims.Role == RoleAdmin {
		return true
	}
	fields := logMetadata(path)
	if fields == nil {
		return false
	}
	if fields["job_type"] == diskCheckJobType {
		return false // disk checks are admin-only
	}
	if fields["job_type"] == httpRequestJobType {
		if authManager.userHasHTTPRequest(claims.Username, fields["cmd_id"]) {
			return true
		}
	} else if authManager.userHasCommand(claims.Username, fields["cmd_id"]) {
		return true
	}
	return fields["runbook_id"] != "" && authManager.userHasRunBook(claims.Username, fields["runbook_id"])
}
