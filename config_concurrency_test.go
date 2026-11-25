package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

// TestConcurrentConfigSaves fires many config mutations at once (command edits,
// a host edit, a variables update and a user creation) and checks that none of
// them is lost. Before mutateConfig every handler read config.json outside the
// write lock, so parallel saves silently overwrote each other.
func TestConcurrentConfigSaves(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	before := ts.GetConfig(t)
	commands, _ := before["commands"].([]interface{})
	if len(commands) < 2 {
		t.Fatalf("test config needs at least two commands, found %d", len(commands))
	}

	var wg sync.WaitGroup
	errs := make(chan string, len(commands)+3)
	expectDesc := make(map[string]string)
	for i, raw := range commands {
		cmd, _ := raw.(map[string]interface{})
		cmdID, _ := cmd["cmd_id"].(string)
		body := make(map[string]interface{}, len(cmd))
		for k, v := range cmd {
			body[k] = v
		}
		desc := fmt.Sprintf("concurrent edit %d", i)
		body["description"] = desc
		expectDesc[cmdID] = desc
		wg.Add(1)
		go func(cmdID string, body map[string]interface{}) {
			defer wg.Done()
			resp := ts.DoAuthRequest(t, "PUT", "/api/config/commands/"+cmdID, body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Sprintf("update %s: status %d", cmdID, resp.StatusCode)
			}
		}(cmdID, body)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		resp := ts.DoAuthRequest(t, "PUT", "/api/config/variables", map[string]interface{}{"concurrent_marker": "yes"})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			errs <- fmt.Sprintf("variables: status %d", resp.StatusCode)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		resp := ts.DoAuthRequest(t, "POST", "/api/config/hosts", map[string]interface{}{
			"name": "concurrent-host", "connection_type": "ssh", "host": "127.0.0.1", "port": 22, "username": "u", "password": "p",
		})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			errs <- fmt.Sprintf("host create: status %d", resp.StatusCode)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		resp := ts.DoAuthRequest(t, "POST", "/api/users", map[string]interface{}{
			"username": "concurrent-user", "enabled": true, "temporary_password": "TemporaryPassw0rd!",
		})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			errs <- fmt.Sprintf("user create: status %d", resp.StatusCode)
		}
	}()

	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}

	after := ts.GetConfig(t)
	afterCommands, _ := after["commands"].([]interface{})
	seen := 0
	for _, raw := range afterCommands {
		cmd, _ := raw.(map[string]interface{})
		cmdID, _ := cmd["cmd_id"].(string)
		if want, ok := expectDesc[cmdID]; ok {
			seen++
			if got, _ := cmd["description"].(string); got != want {
				t.Errorf("command %s: description %q, want %q (edit lost)", cmdID, got, want)
			}
		}
	}
	if seen != len(expectDesc) {
		t.Errorf("%d of %d commands survived the concurrent saves", seen, len(expectDesc))
	}
	if vars, _ := after["variables"].(map[string]interface{}); vars["concurrent_marker"] != "yes" {
		t.Errorf("variables update lost: %v", after["variables"])
	}
	if hosts, _ := after["hosts"].(map[string]interface{}); hosts["concurrent-host"] == nil {
		t.Errorf("host create lost")
	}
	foundUser := false
	if users, _ := after["users"].([]interface{}); users != nil {
		for _, raw := range users {
			u, _ := raw.(map[string]interface{})
			if u["username"] == "concurrent-user" {
				foundUser = true
			}
		}
	}
	if !foundUser {
		t.Errorf("user create lost")
	}
}

// TestLoadIfChangedStatShortcut checks the mtime+size shortcut in loadIfChanged:
// an unchanged stat skips reading the file, a changed mtime falls back to the
// hash, and a file modified within the last two seconds is never trusted.
func TestLoadIfChangedStatShortcut(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	write := func(marker string, modTime time.Time) {
		t.Helper()
		// same byte length for every marker so only mtime distinguishes the files
		body := fmt.Sprintf(`{"port":18080,"access_url":"http://%s.test","hosts":{},"commands":[]}`, marker)
		if err := os.WriteFile(configPath, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(configPath, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}
	accessURL := func() string {
		t.Helper()
		if err := cache.loadIfChanged(); err != nil {
			t.Fatalf("loadIfChanged: %v", err)
		}
		return cache.GetConfig().AccessURL
	}

	oldCache, oldCron, oldJobs, oldAuth := cache, cronScheduler, scheduledJobs, authManager
	cache = &ConfigCache{configFile: configPath}
	cronScheduler = cron.New()
	scheduledJobs = make(map[string]cron.EntryID)
	authManager = nil
	t.Cleanup(func() {
		cronScheduler.Stop()
		cache, cronScheduler, scheduledJobs, authManager = oldCache, oldCron, oldJobs, oldAuth
	})

	old := time.Now().Add(-10 * time.Second).Truncate(time.Second)
	write("a", old)
	if got := accessURL(); got != "http://a.test" {
		t.Fatalf("initial load: %q", got)
	}

	// Same size and mtime: the shortcut must skip the read, so the stale "a" stays.
	write("b", old)
	if got := accessURL(); got != "http://a.test" {
		t.Fatalf("stat shortcut did not skip the reload: %q", got)
	}

	// Different mtime (still older than 2 s): hash path detects the new content.
	if err := os.Chtimes(configPath, old.Add(time.Second), old.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := accessURL(); got != "http://b.test" {
		t.Fatalf("changed mtime was not reloaded: %q", got)
	}

	// A file modified "now" is never trusted by the shortcut: same size and
	// mtime as the loaded one, but within 2 s, so it is hashed and picked up.
	now := time.Now().Truncate(time.Second)
	write("c", now)
	if got := accessURL(); got != "http://c.test" {
		t.Fatalf("fresh file not reloaded: %q", got)
	}
	write("d", now)
	if got := accessURL(); got != "http://d.test" {
		t.Fatalf("recent same-second edit was skipped: %q", got)
	}
}
