package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/robfig/cron/v3"
)

const testBackupPassword = "correct horse battery staple"

func TestBackupEncryptDecryptRoundTrip(t *testing.T) {
	plain := []byte("backup payload")
	encrypted, err := encryptBackup(plain, testBackupPassword)
	if err != nil {
		t.Fatalf("encryptBackup returned error: %v", err)
	}
	if bytes.Contains(encrypted, plain) {
		t.Fatalf("encrypted backup contains plaintext payload")
	}
	decrypted, err := decryptBackup(encrypted, testBackupPassword)
	if err != nil {
		t.Fatalf("decryptBackup returned error: %v", err)
	}
	if !bytes.Equal(decrypted, plain) {
		t.Fatalf("decrypted payload mismatch")
	}
	if _, err := decryptBackup(encrypted, "wrong password value"); err == nil {
		t.Fatalf("decryptBackup accepted wrong password")
	}
}

func TestBackupRejectsNewerAppVersion(t *testing.T) {
	payload := validTestBackupPayload(t)
	payload.Manifest.AppVersion = "999.0.0"
	payload.Manifest.Files["config.json"] = fileInfo(payload.Config)

	api := NewBackupAPI("config.json")
	if err := api.validatePayload(payload, true); err == nil {
		t.Fatalf("validatePayload accepted backup from newer app version")
	}
}

func TestDecodeBackupZipRejectsUnsafePath(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../config.json")
	if err != nil {
		t.Fatalf("failed to create zip entry: %v", err)
	}
	if _, err := w.Write([]byte("{}")); err != nil {
		t.Fatalf("failed to write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("failed to close zip: %v", err)
	}
	if _, err := decodeBackupZip(buf.Bytes()); err == nil {
		t.Fatalf("decodeBackupZip accepted unsafe path")
	}
}

func TestBackupRestoreAppliesExactPEMSnapshot(t *testing.T) {
	tempDir := t.TempDir()
	restoreCwd := chdirForTest(t, tempDir)
	defer restoreCwd()

	if err := os.MkdirAll("pem_files", 0755); err != nil {
		t.Fatalf("failed to create pem dir: %v", err)
	}
	writeTestFile(t, "config.json", []byte(testConfigJSON("oldpass", "old.pem")))
	writeTestFile(t, filepath.Join("pem_files", "old.pem"), []byte("old-key"))
	writeTestFile(t, filepath.Join("pem_files", "delete.pem"), []byte("delete-key"))
	writeTestFile(t, "email.tpl", []byte("old template"))

	oldCache := cache
	oldCron := cronScheduler
	oldScheduledJobs := scheduledJobs
	oldOverviewEmailEntryID := scheduledJobsOverviewEmailEntryID
	oldAuthManager := authManager
	defer func() {
		cache = oldCache
		cronScheduler = oldCron
		scheduledJobs = oldScheduledJobs
		scheduledJobsOverviewEmailEntryID = oldOverviewEmailEntryID
		authManager = oldAuthManager
	}()

	cache = &ConfigCache{configFile: "config.json"}
	cronScheduler = cron.New()
	scheduledJobs = make(map[string]cron.EntryID)
	scheduledJobsOverviewEmailEntryID = 0
	authManager = NewAuthManager(&AuthConfig{
		AdminPassword: "oldpass",
		TOTPSecret:    "OLDSECRET",
		JWTSecret:     "oldjwt",
		DevMode:       false,
	}, "config.json")
	if err := cache.loadIfChanged(); err != nil {
		t.Fatalf("initial cache load failed: %v", err)
	}

	payload := validTestBackupPayload(t)
	payload.Config = []byte(testConfigJSON("newpass", "new.pem"))
	payload.EmailTpl = []byte("new template")
	payload.PEMFiles = map[string][]byte{"new.pem": []byte("new-key")}
	payload.Manifest.Counts.PEMFiles = 1
	payload.Manifest.Files = map[string]backupFileInfo{
		"config.json":       fileInfo(payload.Config),
		"email.tpl":         fileInfo(payload.EmailTpl),
		"pem_files/new.pem": fileInfo(payload.PEMFiles["new.pem"]),
	}

	api := NewBackupAPI("config.json")
	if err := api.validatePayload(payload, true); err != nil {
		t.Fatalf("validatePayload returned error: %v", err)
	}
	if err := api.restorePayload(payload); err != nil {
		t.Fatalf("restorePayload returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join("pem_files", "old.pem")); !os.IsNotExist(err) {
		t.Fatalf("old PEM file was not removed")
	}
	if _, err := os.Stat(filepath.Join("pem_files", "delete.pem")); !os.IsNotExist(err) {
		t.Fatalf("extra PEM file was not removed")
	}
	newPEM, err := ioutil.ReadFile(filepath.Join("pem_files", "new.pem"))
	if err != nil {
		t.Fatalf("new PEM file missing: %v", err)
	}
	if string(newPEM) != "new-key" {
		t.Fatalf("new PEM content mismatch")
	}
	email, err := ioutil.ReadFile("email.tpl")
	if err != nil {
		t.Fatalf("email.tpl missing: %v", err)
	}
	if string(email) != "new template" {
		t.Fatalf("email.tpl content mismatch")
	}

	var restored Config
	data, err := ioutil.ReadFile("config.json")
	if err != nil {
		t.Fatalf("restored config missing: %v", err)
	}
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("failed to parse restored config: %v", err)
	}
	if restored.Version != AppVersion {
		t.Fatalf("restored config version = %q, want %q", restored.Version, AppVersion)
	}
	if restored.AdminPassword != "newpass" {
		t.Fatalf("restored admin password mismatch")
	}
}

func validTestBackupPayload(t *testing.T) *backupPayload {
	t.Helper()
	config := []byte(testConfigJSON("testpass", "key.pem"))
	payload := &backupPayload{
		Config:   config,
		EmailTpl: []byte("template"),
		HasEmail: true,
		PEMFiles: map[string][]byte{
			"key.pem": []byte("test-key"),
		},
		Manifest: backupManifest{
			Format:        backupFormatName,
			FormatVersion: backupFormatVersion,
			AppVersion:    AppVersion,
			CreatedAt:     "2026-07-14T00:00:00Z",
			Files:         make(map[string]backupFileInfo),
			Counts:        backupCounts{Hosts: 1, Commands: 1, RunBooks: 0, PEMFiles: 1},
		},
	}
	payload.Manifest.Files["config.json"] = fileInfo(payload.Config)
	payload.Manifest.Files["email.tpl"] = fileInfo(payload.EmailTpl)
	for name, data := range payload.PEMFiles {
		payload.Manifest.Files["pem_files/"+name] = fileInfo(data)
	}
	return payload
}

func testConfigJSON(adminPassword, pemName string) string {
	return `{
	"version": "0.0.1",
	"port": 8080,
	"bind_address": "127.0.0.1",
	"cron_enabled": "N",
	"log_retention_days": 3,
	"dev_mode": false,
	"admin_password": "` + adminPassword + `",
	"totp_secret": "JBSWY3DPEHPK3PXP",
	"jwt_secret": "jwt-secret",
	"hosts": {
		"local": {
			"connection_type": "ssh",
			"host": "127.0.0.1",
			"port": 22,
			"username": "tester",
			"pem_file": "./pem_files/` + pemName + `"
		}
	},
	"commands": [
		{
			"cmd_id": "test-command",
			"host_ref": "local",
			"command": "echo ok",
			"description": "Test command"
		}
	]
}`
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := ioutil.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}

func chdirForTest(t *testing.T, dir string) func() {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	return func() {
		if err := os.Chdir(old); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}
}

func TestSortedKeys(t *testing.T) {
	values := map[string][]byte{"b.pem": nil, "a.pem": nil}
	got := sortedKeys(values)
	if !sort.StringsAreSorted(got) {
		t.Fatalf("sortedKeys returned unsorted keys: %v", got)
	}
}
