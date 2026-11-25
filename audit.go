package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SecurityAuditEntry struct {
	Timestamp string                 `json:"timestamp"`
	Actor     string                 `json:"actor"`
	Role      string                 `json:"role"`
	SourceIP  string                 `json:"source_ip"`
	Action    string                 `json:"action"`
	Target    string                 `json:"target,omitempty"`
	Outcome   string                 `json:"outcome"`
	Details   map[string]interface{} `json:"details,omitempty"`
}

var securityAuditMutex sync.Mutex

func securityAuditDir() string { return filepath.Join("logs", "audit") }

func writeSecurityAudit(r *http.Request, actor, role, action, target, outcome string, details map[string]interface{}) {
	entry := SecurityAuditEntry{Timestamp: time.Now().Format(time.RFC3339Nano), Actor: actor, Role: role, Action: action, Target: target, Outcome: outcome, Details: sanitizeAuditDetails(details)}
	if r != nil {
		entry.SourceIP = getClientIP(r)
	}
	if entry.Actor == "" {
		entry.Actor = "unknown"
	}
	if entry.Role == "" {
		entry.Role = "unknown"
	}
	securityAuditMutex.Lock()
	defer securityAuditMutex.Unlock()
	_ = os.MkdirAll(securityAuditDir(), privateDirMode)
	file, err := os.OpenFile(filepath.Join(securityAuditDir(), time.Now().Format("2006-01-02")+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, privateFileMode)
	if err != nil {
		return
	}
	defer file.Close()
	enc := json.NewEncoder(file)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(entry)
}

func sanitizeAuditDetails(details map[string]interface{}) map[string]interface{} {
	if len(details) == 0 {
		return nil
	}
	result := make(map[string]interface{})
	for key, value := range details {
		lower := strings.ToLower(key)
		blocked := false
		for _, word := range []string{"password", "secret", "token", "output", "command", "credential", "hash"} {
			if strings.Contains(lower, word) {
				blocked = true
				break
			}
		}
		if !blocked {
			result[key] = value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func cleanupSecurityAudits(retentionDays int) {
	if retentionDays <= 0 {
		retentionDays = 90
	}
	entries, err := os.ReadDir(securityAuditDir())
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		date, err := time.ParseInLocation("2006-01-02", strings.TrimSuffix(entry.Name(), ".jsonl"), time.Local)
		if err == nil && date.Before(cutoff) {
			_ = os.Remove(filepath.Join(securityAuditDir(), entry.Name()))
		}
	}
}

func startSecurityAuditCleanupRoutine() {
	cleanup := func() {
		cfg := cache.GetConfig()
		days := 90
		if cfg != nil && cfg.AuditRetentionDays > 0 {
			days = cfg.AuditRetentionDays
		}
		cleanupSecurityAudits(days)
	}
	cleanup()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		cleanup()
	}
}

func (am *AuthManager) HandleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendJSONError(w, 405, "Method not allowed")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	filter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("filter")))
	files, _ := os.ReadDir(securityAuditDir())
	sort.Slice(files, func(i, j int) bool { return files[i].Name() > files[j].Name() })
	entries := make([]SecurityAuditEntry, 0)
	for _, item := range files {
		if item.IsDir() || !strings.HasSuffix(item.Name(), ".jsonl") {
			continue
		}
		file, err := os.Open(filepath.Join(securityAuditDir(), item.Name()))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		day := make([]SecurityAuditEntry, 0)
		for scanner.Scan() {
			var entry SecurityAuditEntry
			if json.Unmarshal(scanner.Bytes(), &entry) == nil {
				text := strings.ToLower(entry.Actor + " " + entry.Role + " " + entry.Action + " " + entry.Target + " " + entry.Outcome + " " + entry.SourceIP)
				if filter == "" || strings.Contains(text, filter) {
					day = append(day, entry)
				}
			}
		}
		file.Close()
		for i := len(day) - 1; i >= 0; i-- {
			entries = append(entries, day[i])
		}
	}
	total := len(entries)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	sendJSONSuccess(w, 200, map[string]interface{}{"items": entries[start:end], "page": page, "page_size": pageSize, "total": total}, "Audit entries retrieved")
}
