package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// ---------------------------------------------------------------------------
// df parsing and command building
// ---------------------------------------------------------------------------

const sampleDfOutput = `Filesystem      1B-blocks        Used   Available Capacity Mounted on
/dev/root     53660876800 41598001152 12028493824      78% /
/dev/sdb1    107374182400 106971000000   403182400     100% /data
tmpfs          4139925504           0  4139925504       0% /dev/shm
/dev/sdc1     21474836480  1073741824 20401094656       5% /mnt/my disk
`

func TestParseDfOutput(t *testing.T) {
	rows := parseDfOutput(sampleDfOutput)
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(rows))
	}
	root := rows["/"]
	if root == nil || root.TotalBytes != 53660876800 || root.UsedBytes != 41598001152 || root.FreeBytes != 12028493824 || root.Filesystem != "/dev/root" {
		t.Fatalf("unexpected root row: %+v", root)
	}
	if rows["/mnt/my disk"] == nil {
		t.Fatalf("mount path with a space was not parsed: %v", rows)
	}
}

func TestBuildDfCommandQuotesAndSudo(t *testing.T) {
	cmd := buildDfCommand(false, []string{"/", "/it's"})
	if cmd != `df -PB1 '/' '/it'\''s'` {
		t.Fatalf("unexpected command: %s", cmd)
	}
	if got := buildDfCommand(true, nil); got != "sudo -n df -PB1" {
		t.Fatalf("unexpected sudo command: %s", got)
	}
}

func TestIsRealDisk(t *testing.T) {
	cases := map[string]bool{"/dev/sda1": true, "tmpfs": false, "tmpfs-xyz": false, "devtmpfs": false, "udev": false, "none": false, "overlay": false, "nfs:/x": true}
	for fs, want := range cases {
		if got := isRealDisk(fs, 1024); got != want {
			t.Errorf("isRealDisk(%q) = %v, want %v", fs, got, want)
		}
	}
	if isRealDisk("/dev/sda1", 0) {
		t.Errorf("zero-size filesystem must not be a real disk")
	}
}

func TestExtractMountError(t *testing.T) {
	stderr := "df: '/missing': No such file or directory\ndf: '/other': Permission denied\n"
	if got := extractMountError(stderr, "/missing"); got != "df: '/missing': No such file or directory" {
		t.Fatalf("unexpected error line: %q", got)
	}
}

// ---------------------------------------------------------------------------
// Durations, thresholds, formatting
// ---------------------------------------------------------------------------

func TestParseAndFormatDiskDuration(t *testing.T) {
	cases := map[string]time.Duration{"30m": 30 * time.Minute, "1h": time.Hour, "1h30m": 90 * time.Minute, "2d": 48 * time.Hour, "1.5d": 36 * time.Hour}
	for in, want := range cases {
		got, err := parseDiskDuration(in)
		if err != nil || got != want {
			t.Errorf("parseDiskDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "-1h", "0m", "5"} {
		if _, err := parseDiskDuration(bad); err == nil {
			t.Errorf("parseDiskDuration(%q) should fail", bad)
		}
	}
	for d, want := range map[time.Duration]string{48 * time.Hour: "2d", time.Hour: "1h", 90 * time.Minute: "1h30m", 45 * time.Second: "45s"} {
		if got := formatDiskDuration(d); got != want {
			t.Errorf("formatDiskDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestDiskThresholdBytes(t *testing.T) {
	if got := diskThresholdBytes(5, "GB", 0); got != 5*1024*1024*1024 {
		t.Errorf("GB threshold = %d", got)
	}
	if got := diskThresholdBytes(500, "MB", 0); got != 500*1024*1024 {
		t.Errorf("MB threshold = %d", got)
	}
	if got := diskThresholdBytes(10, "%", 1000); got != 100 {
		t.Errorf("percent threshold = %d", got)
	}
	if got := humanBytes1024(12028493824); got != "11.2 GB" {
		t.Errorf("humanBytes1024 = %q", got)
	}
}

// ---------------------------------------------------------------------------
// Config normalisation and validation
// ---------------------------------------------------------------------------

func diskTestHosts() map[string]Host {
	return map[string]Host{
		"linux": {ConnectionType: "ssh", Host: "10.0.0.1", Port: 22, Username: "u", Password: "p"},
		"win":   {ConnectionType: "winrm", Host: "10.0.0.2", Port: 5986, Username: "a", Password: "p"},
	}
}

func TestNormalizeDiskMonitorConfigDefaults(t *testing.T) {
	cfg := normalizeDiskMonitorConfig(&DiskMonitorConfig{Volumes: []DiskVolume{{HostRef: " linux ", MountPath: "/data/"}}})
	if !cfg.isEnabled() || cfg.SendAlertOnceEvery != "4h" || cfg.DailySummaryTime != "08:00" || cfg.DefaultCheckInterval != "1h" || cfg.DefaultThresholdUnit != "GB" || cfg.MaxConcurrentHostChecks != 10 || cfg.EmailBarMode != "free" || cfg.EmailSubject != diskMonitorDefaultEmailSubject {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	v := cfg.Volumes[0]
	if v.VolumeID == "" || v.HostRef != "linux" || v.MountPath != "/data" || v.CheckInterval != "1h" || v.ThresholdValue != 5 || v.ThresholdUnit != "GB" || !v.isEnabled() {
		t.Fatalf("volume not normalised: %+v", v)
	}
	if normalizeDiskMountPath("/") != "/" {
		t.Fatalf("root path must stay /")
	}
	if err := validateDiskMonitorConfig(cfg, diskTestHosts()); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

func TestValidateDiskMonitorConfigRejects(t *testing.T) {
	hosts := diskTestHosts()
	mk := func(mutate func(*DiskMonitorConfig)) DiskMonitorConfig {
		cfg := DiskMonitorConfig{Volumes: []DiskVolume{{VolumeID: "v1", HostRef: "linux", MountPath: "/"}}}
		mutate(&cfg)
		return normalizeDiskMonitorConfig(&cfg)
	}
	cases := map[string]DiskMonitorConfig{
		"winrm host":        mk(func(c *DiskMonitorConfig) { c.Volumes[0].HostRef = "win" }),
		"missing host":      mk(func(c *DiskMonitorConfig) { c.Volumes[0].HostRef = "nope" }),
		"relative mount":    mk(func(c *DiskMonitorConfig) { c.Volumes[0].MountPath = "data" }),
		"template in mount": mk(func(c *DiskMonitorConfig) { c.Volumes[0].MountPath = "/{{x}}" }),
		"bad percent":       mk(func(c *DiskMonitorConfig) { c.Volumes[0].ThresholdValue = 150; c.Volumes[0].ThresholdUnit = "%" }),
		"bad unit":          mk(func(c *DiskMonitorConfig) { c.Volumes[0].ThresholdUnit = "TB" }),
		"short interval":    mk(func(c *DiskMonitorConfig) { c.Volumes[0].CheckInterval = "10s" }),
		"bad summary time":  mk(func(c *DiskMonitorConfig) { c.DailySummaryTime = "25:00" }),
		"bad recipient":     mk(func(c *DiskMonitorConfig) { c.Recipients.To = []string{"not-an-email"} }),
		"bad bar mode":      mk(func(c *DiskMonitorConfig) { c.EmailBarMode = "both" }),
		"too concurrent":    mk(func(c *DiskMonitorConfig) { c.MaxConcurrentHostChecks = 500 }),
		"bad volume id":     mk(func(c *DiskMonitorConfig) { c.Volumes[0].VolumeID = "bad id!" }),
		"subject newline":   mk(func(c *DiskMonitorConfig) { c.EmailSubject = "[DM] {{summary}}\nBcc: x@example.com" }),
		"subject too long":  mk(func(c *DiskMonitorConfig) { c.EmailSubject = strings.Repeat("x", diskMonitorMaxEmailSubjectLen+1) }),
		"duplicate mount": mk(func(c *DiskMonitorConfig) {
			c.Volumes = append(c.Volumes, DiskVolume{VolumeID: "v2", HostRef: "linux", MountPath: "/"})
		}),
		"duplicate id": mk(func(c *DiskMonitorConfig) {
			c.Volumes = append(c.Volumes, DiskVolume{VolumeID: "v1", HostRef: "linux", MountPath: "/x"})
		}),
	}
	for name, cfg := range cases {
		if err := validateDiskMonitorConfig(cfg, hosts); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

// ---------------------------------------------------------------------------
// State machine
// ---------------------------------------------------------------------------

func withTempDiskState(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := diskMonitorStatePath
	diskMonitorStatePath = filepath.Join(dir, "state.json")
	diskStateStore = &diskStateStoreType{state: diskMonitorState{Version: diskMonitorStateVersion, Volumes: map[string]*diskVolumeState{}}}
	t.Cleanup(func() {
		diskStateStore.flush()
		diskMonitorStatePath = old
		diskStateStore = &diskStateStoreType{state: diskMonitorState{Version: diskMonitorStateVersion, Volumes: map[string]*diskVolumeState{}}}
	})
}

func TestApplyResultStateMachine(t *testing.T) {
	withTempDiskState(t)
	engine := newDiskMonitorEngine()
	settings := normalizeDiskMonitorConfig(&DiskMonitorConfig{})
	vol := DiskVolume{VolumeID: "v1", HostRef: "linux", MountPath: "/", CheckInterval: "1h", ThresholdValue: 1, ThresholdUnit: "GB"}
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.Local)
	gb := uint64(1024 * 1024 * 1024)

	// OK reading
	label := engine.applyResult(vol, settings, diskMountResult{MountPath: "/", Result: &dfResult{TotalBytes: 10 * gb, FreeBytes: 5 * gb, UsedBytes: 5 * gb}}, now, "t1", false)
	st, _ := diskStateStore.volume("v1")
	if label != "OK" || st.Status != diskStatusOK || st.NextDue != now.Add(time.Hour) || len(st.History) != 1 || st.LastTraceID != "t1" {
		t.Fatalf("after OK: label=%s state=%+v", label, st)
	}
	// Trip (free < 1 GB)
	label = engine.applyResult(vol, settings, diskMountResult{MountPath: "/", Result: &dfResult{TotalBytes: 10 * gb, FreeBytes: gb / 2}}, now.Add(time.Hour), "t2", false)
	st, _ = diskStateStore.volume("v1")
	if label != "TRIPPED" || st.Status != diskStatusTripped || st.TrippedSince != now.Add(time.Hour) || st.PendingRecovery {
		t.Fatalf("after trip: label=%s state=%+v", label, st)
	}
	// Still tripped: TrippedSince unchanged
	engine.applyResult(vol, settings, diskMountResult{MountPath: "/", Result: &dfResult{TotalBytes: 10 * gb, FreeBytes: gb / 4}}, now.Add(2*time.Hour), "t3", false)
	st, _ = diskStateStore.volume("v1")
	if st.TrippedSince != now.Add(time.Hour) {
		t.Fatalf("TrippedSince must not move while tripped: %v", st.TrippedSince)
	}
	// Recovery
	label = engine.applyResult(vol, settings, diskMountResult{MountPath: "/", Result: &dfResult{TotalBytes: 10 * gb, FreeBytes: 8 * gb}}, now.Add(3*time.Hour), "t4", false)
	st, _ = diskStateStore.volume("v1")
	if label != "OK" || st.Status != diskStatusOK || !st.PendingRecovery || !st.TrippedSince.IsZero() {
		t.Fatalf("after recovery: label=%s state=%+v", label, st)
	}
	// Check failure
	label = engine.applyResult(vol, settings, diskMountResult{MountPath: "/", Err: fmt.Errorf("dial: timeout")}, now.Add(4*time.Hour), "t5", false)
	st, _ = diskStateStore.volume("v1")
	if label != "FAILED" || st.Status != diskStatusUnknown || st.LastError != "dial: timeout" || st.TrippedSince != now.Add(4*time.Hour) {
		t.Fatalf("after failure: label=%s state=%+v", label, st)
	}
	// Interrupted by shutdown keeps status and makes it due now
	engine.applyResult(vol, settings, diskMountResult{MountPath: "/", Err: context.Canceled}, now.Add(5*time.Hour), "t6", true)
	st, _ = diskStateStore.volume("v1")
	if st.Status != diskStatusUnknown || st.NextDue != now.Add(5*time.Hour) {
		t.Fatalf("interrupted check changed state: %+v", st)
	}
	// History capped at 24
	for i := 0; i < 40; i++ {
		engine.applyResult(vol, settings, diskMountResult{MountPath: "/", Result: &dfResult{TotalBytes: 10 * gb, FreeBytes: 8 * gb}}, now.Add(time.Duration(10+i)*time.Hour), "t", false)
	}
	st, _ = diskStateStore.volume("v1")
	if len(st.History) != diskMonitorHistorySize {
		t.Fatalf("history length = %d, want %d", len(st.History), diskMonitorHistorySize)
	}

	// Persistence round trip
	diskStateStore.flush()
	loadDiskMonitorState()
	st, ok := diskStateStore.volume("v1")
	if !ok || st.Status != diskStatusOK || len(st.History) != diskMonitorHistorySize {
		t.Fatalf("state did not survive reload: ok=%v %+v", ok, st)
	}
	if _, err := os.Stat(diskMonitorStatePath); err != nil {
		t.Fatalf("state file missing: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Emails
// ---------------------------------------------------------------------------

func diskSampleFleet() diskFleetView {
	gb := uint64(1024 * 1024 * 1024)
	mb := uint64(1024 * 1024)
	checked := time.Date(2026, 4, 9, 7, 23, 0, 0, time.UTC)
	mk := func(host, addr, mount, status string, free, total uint64) diskEmailRow {
		return diskEmailRow{HostRef: host, HostAddr: addr, MountPath: mount,
			Volume: DiskVolume{VolumeID: host + mount, HostRef: host, MountPath: mount, ThresholdValue: 500, ThresholdUnit: "MB"},
			State:  diskVolumeState{Status: status, LastFreeBytes: free, LastTotalBytes: total, LastCheckedAt: checked}}
	}
	cfg := &Config{Hosts: map[string]Host{"bastion": {Host: "bastion.example.com"}, "db": {Host: "db.example.com"}}}
	settings := normalizeDiskMonitorConfig(&DiskMonitorConfig{Volumes: []DiskVolume{
		{VolumeID: "bastion/boot", HostRef: "bastion", MountPath: "/boot", ThresholdValue: 500, ThresholdUnit: "MB"},
		{VolumeID: "bastion/", HostRef: "bastion", MountPath: "/", ThresholdValue: 500, ThresholdUnit: "MB"},
		{VolumeID: "db/pg", HostRef: "db", MountPath: "/var/lib/postgresql", ThresholdValue: 500, ThresholdUnit: "MB"},
	}})
	state := diskMonitorState{Volumes: map[string]*diskVolumeState{
		"bastion/boot": {Status: diskStatusOK, LastFreeBytes: 712 * mb, LastTotalBytes: gb, LastCheckedAt: checked},
		"bastion/":     {Status: diskStatusOK, LastFreeBytes: 4 * gb, LastTotalBytes: 20 * gb, LastCheckedAt: checked},
		"db/pg":        {Status: diskStatusTripped, LastFreeBytes: 100 * mb, LastTotalBytes: 50 * gb, LastCheckedAt: checked, TrippedSince: time.Date(2026, 4, 9, 6, 0, 0, 0, time.UTC)},
	}}
	_ = mk
	return buildDiskFleetView(cfg, settings, state)
}

func mustContainDisk(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Errorf("expected body to contain %q", want)
	}
}

func TestBuildDiskDailySummary(t *testing.T) {
	fv := diskSampleFleet()
	subject, text, html := buildDiskDailySummary(fv, time.Date(2026, 4, 9, 8, 0, 0, 0, time.UTC), diskBarModeFree, diskMonitorDefaultEmailSubject)
	if subject != "[Disk Monitor] Daily summary: 2 volumes OK, 1 alerting" {
		t.Errorf("unexpected subject %q", subject)
	}
	mustContainDisk(t, text, "Host: bastion (bastion.example.com)")
	mustContainDisk(t, text, "/boot")
	for _, want := range []string{"<table", "bastion", "bastion.example.com", "/boot", "/var/lib/postgresql", "TRIPPED", "OK", "Daily summary", diskColorOK, diskColorTripped} {
		mustContainDisk(t, html, want)
	}
}

func TestBuildDiskAlertEmail(t *testing.T) {
	fv := diskSampleFleet()
	subject, text, html := buildDiskAlertEmail(fv, time.Date(2026, 4, 9, 8, 0, 0, 0, time.UTC), diskBarModeFree, diskMonitorDefaultEmailSubject)
	if subject != "[Disk Monitor] CRITICAL: 1 volumes affected on 1 host(s)" {
		t.Errorf("unexpected subject %q", subject)
	}
	mustContainDisk(t, text, "== Below threshold ==")
	mustContainDisk(t, text, "(0% free)")
	mustContainDisk(t, html, "Below threshold")
	mustContainDisk(t, html, "/var/lib/postgresql")
	mustContainDisk(t, html, "db.example.com")
	if strings.Contains(html, "/boot") {
		t.Errorf("alert email must not list OK volumes")
	}
}

func TestBuildDiskAlertEmailEmptyAndRecovered(t *testing.T) {
	if s, b, h := buildDiskAlertEmail(diskFleetView{}, time.Now(), diskBarModeFree, diskMonitorDefaultEmailSubject); s != "" || b != "" || h != "" {
		t.Errorf("expected empty output for empty fleet")
	}
	fv := diskSampleFleet()
	fv.Tripped = nil
	fv.Recovered = []diskEmailRow{fv.OK[0]}
	s, _, _ := buildDiskAlertEmail(fv, time.Now(), diskBarModeUsed, "")
	if s != "[Disk Monitor] 1 volume(s) recovered" {
		t.Errorf("unexpected recovered subject %q", s)
	}
}

func TestDiskEmailCustomSubject(t *testing.T) {
	fv := diskSampleFleet()
	now := time.Date(2026, 4, 9, 8, 0, 0, 0, time.UTC)
	tmpl := "[PROD] {{type}}: {{tripped}} low, {{failed}} failed, {{ok}}/{{volumes}} OK on {{hosts}} host(s) - {{summary}}"

	s, _, _ := buildDiskAlertEmail(fv, now, diskBarModeFree, tmpl)
	if want := "[PROD] Alert: 1 low, 0 failed, 2/3 OK on 1 host(s) - CRITICAL: 1 volumes affected on 1 host(s)"; s != want {
		t.Errorf("alert subject = %q, want %q", s, want)
	}
	s, _, _ = buildDiskDailySummary(fv, now, diskBarModeFree, tmpl)
	if want := "[PROD] Daily summary: 1 low, 0 failed, 2/3 OK on 2 host(s) - Daily summary: 2 volumes OK, 1 alerting"; s != want {
		t.Errorf("summary subject = %q, want %q", s, want)
	}
	s, body, _ := buildDiskTestEmail(fv, now, diskBarModeFree, "Disk check {{type}} from {{app_host}}")
	host, _ := os.Hostname()
	if want := "Disk check Test email from " + host; s != want {
		t.Errorf("test subject = %q, want %q", s, want)
	}
	mustContainDisk(t, body, "This is a test email")

	// Blank template or blank result falls back to the default.
	if s, _, _ = buildDiskTestEmail(fv, now, diskBarModeFree, "   "); s != "[Disk Monitor] Test email" {
		t.Errorf("blank template subject = %q", s)
	}
}

func TestDiskEmailSubjectPlaceholders(t *testing.T) {
	vars := map[string]interface{}{"env_name": "prod"}
	if err := validateDiskEmailSubjectPlaceholders("[{{env_name}}] {{summary}} {{type}} {{tripped}} {{failed}} {{ok}} {{volumes}} {{hosts}} {{app_host}}", vars); err != nil {
		t.Errorf("expected valid placeholders, got %v", err)
	}
	if err := validateDiskEmailSubjectPlaceholders("[DM] {{bogus}}", vars); err == nil {
		t.Errorf("expected unknown placeholder to be rejected")
	}
	// Config load must leave the runtime placeholders alone.
	out, err := substituteVariables(`{"disk_monitor":{"email_subject":"[{{env_name}}] {{summary}} on {{hosts}} host(s) {{type}} {{tripped}} {{failed}} {{ok}} {{volumes}} {{app_host}}"}}`, vars)
	if err != nil {
		t.Fatalf("substituteVariables rejected disk placeholders: %v", err)
	}
	if !strings.Contains(out, "[prod] {{summary}} on {{hosts}} host(s)") {
		t.Errorf("unexpected substitution result %s", out)
	}
}

func TestDiskEmailHTMLEscaping(t *testing.T) {
	cfg := &Config{Hosts: map[string]Host{"<script>alert(1)</script>": {Host: "evil.example.com"}}}
	settings := normalizeDiskMonitorConfig(&DiskMonitorConfig{Volumes: []DiskVolume{{VolumeID: "x", HostRef: "<script>alert(1)</script>", MountPath: "/data"}}})
	fv := buildDiskFleetView(cfg, settings, diskMonitorState{Volumes: map[string]*diskVolumeState{"x": {Status: diskStatusOK, LastFreeBytes: 1, LastTotalBytes: 2}}})
	_, _, html := buildDiskDailySummary(fv, time.Now(), diskBarModeFree, "")
	if strings.Contains(html, "<script>alert(1)</script>") || !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("host name was not escaped in HTML")
	}
}

func TestDiskFleetViewExcludesDisabled(t *testing.T) {
	off := false
	cfg := &Config{Hosts: map[string]Host{"linux": {Host: "h"}, "win": {ConnectionType: "winrm", Host: "w"}}}
	settings := normalizeDiskMonitorConfig(&DiskMonitorConfig{Volumes: []DiskVolume{
		{VolumeID: "a", HostRef: "linux", MountPath: "/"},
		{VolumeID: "b", HostRef: "linux", MountPath: "/off", Enabled: &off},
	}})
	state := diskMonitorState{Volumes: map[string]*diskVolumeState{"a": {Status: diskStatusTripped}, "b": {Status: diskStatusTripped}}}
	fv := buildDiskFleetView(cfg, settings, state)
	if len(fv.Tripped) != 1 || fv.Tripped[0].Volume.VolumeID != "a" {
		t.Fatalf("disabled volume must be excluded: %+v", fv.Tripped)
	}
}

func TestDiskDailySummaryTarget(t *testing.T) {
	ref := time.Date(2026, 10, 20, 13, 0, 0, 0, time.Local)
	target, err := diskDailySummaryTarget("08:30", ref)
	if err != nil || target.Hour() != 8 || target.Minute() != 30 || target.Day() != 20 {
		t.Fatalf("unexpected target %v (%v)", target, err)
	}
}

// ---------------------------------------------------------------------------
// End-to-end batch against the in-process SSH test server
// ---------------------------------------------------------------------------

func dfSessionHandler(stdout string, exitStatus uint32) func(ssh.Channel, <-chan *ssh.Request) {
	return func(channel ssh.Channel, requests <-chan *ssh.Request) {
		defer channel.Close()
		for req := range requests {
			if req.Type == "exec" {
				req.Reply(true, nil)
				channel.Write([]byte(stdout))
				channel.Stderr().Write([]byte("df: '/missing': No such file or directory\n"))
				status := make([]byte, 4)
				status[3] = byte(exitStatus)
				channel.SendRequest("exit-status", false, status)
				return
			}
			req.Reply(false, nil)
		}
	}
}

func startDiskTestSSHServer(t *testing.T) Host {
	t.Helper()
	hostSigner, _ := newSSHTestSigner(t)
	config := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
		if c.User() == "monitor" && string(pass) == "secret" {
			return nil, nil
		}
		return nil, fmt.Errorf("denied")
	}}
	config.AddHostKey(hostSigner)
	addr, port := startSSHTestServerWithHandler(t, config, dfSessionHandler(sampleDfOutput, 1))
	return Host{ConnectionType: "ssh", Host: addr, Port: port, Username: "monitor", Password: "secret"}
}

func TestCheckDiskMountsOverSSH(t *testing.T) {
	host := startDiskTestSSHServer(t)
	ctx, cancel := diskCheckContext(context.Background())
	defer cancel()
	run, results := checkDiskMounts(ctx, host, false, []string{"/", "/data", "/missing"})
	if run.ConnErr != nil {
		t.Fatalf("connection failed: %v", run.ConnErr)
	}
	if results[0].Err != nil || results[0].Result.FreeBytes != 12028493824 {
		t.Fatalf("root result: %+v", results[0])
	}
	if results[1].Err != nil || results[1].Result.FreeBytes != 403182400 {
		t.Fatalf("data result: %+v", results[1])
	}
	if results[2].Err == nil || !strings.Contains(results[2].Err.Error(), "No such file") {
		t.Fatalf("missing mount should carry the stderr line: %+v", results[2])
	}
	_, mounts, err := listDiskMounts(ctx, host, false)
	if err != nil || len(mounts) != 4 || mounts[0].MountPath != "/" {
		t.Fatalf("listDiskMounts: %v %+v", err, mounts)
	}
}

func TestRunBatchWritesLogAndState(t *testing.T) {
	withTempDiskState(t)
	ts := NewTestServer(t)
	defer ts.Close()
	host := startDiskTestSSHServer(t)
	engine := newDiskMonitorEngine()
	settings := normalizeDiskMonitorConfig(&DiskMonitorConfig{Volumes: []DiskVolume{
		{VolumeID: "root", HostRef: "test-ssh-server", MountPath: "/", ThresholdValue: 1, ThresholdUnit: "GB"},
		{VolumeID: "data", HostRef: "test-ssh-server", MountPath: "/data", ThresholdValue: 1, ThresholdUnit: "GB"},
	}})
	batch := diskHostBatch{HostRef: "test-ssh-server", Host: host, Volumes: settings.Volumes}
	outcome, err := engine.runBatch(context.Background(), batch, diskBatchParams{Trigger: "test", InvokedBy: "tester", RemoteIP: diskMonitorRemoteIP, Agent: "test"}, settings)
	if err != nil {
		t.Fatalf("runBatch: %v", err)
	}
	if !outcome.OK || len(outcome.Results) != 2 || outcome.Results[0].Status != "OK" || outcome.Results[1].Status != "TRIPPED" {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	content, readErr := os.ReadFile(outcome.Filename)
	if readErr != nil {
		t.Fatalf("log file missing: %v", readErr)
	}
	text := string(content)
	for _, want := range []string{"job_type : disk_check", "host_ref : test-ssh-server", "disk_results : 1 ok, 1 tripped, 0 failed", "exitcode : 0", "--- results ---", "TRIPPED  /data"} {
		if !strings.Contains(text, want) {
			t.Errorf("log missing %q:\n%s", want, text)
		}
	}
	fields := logMetadata(outcome.Filename)
	if fields["job_type"] != diskCheckJobType || fields["cmd_id"] != "diskcheck-test-ssh-server" || fields["exitcode"] != "0" {
		t.Errorf("log metadata not parsed: %v", fields)
	}
	if st, ok := diskStateStore.volume("data"); !ok || st.Status != diskStatusTripped {
		t.Errorf("state not updated: %+v", st)
	}
	if engine.hostLocks[diskHostKey("test-ssh-server", false)].TryLock() == false {
		t.Errorf("host lock not released")
	} else {
		engine.hostLocks[diskHostKey("test-ssh-server", false)].Unlock()
	}
	if len(engine.inFlightHosts()) != 0 {
		t.Errorf("in-flight not cleared")
	}
}

// ---------------------------------------------------------------------------
// REST API
// ---------------------------------------------------------------------------

func TestDiskMonitorAPI(t *testing.T) {
	withTempDiskState(t)
	ts := NewTestServer(t)
	defer ts.Close()

	// Settings default view
	resp := ts.DoAuthRequest(t, "GET", "/api/config/disk-monitor", nil)
	AssertStatus(t, http.StatusOK, resp.StatusCode)
	body := AssertJSONResponse(t, resp)
	resp.Body.Close()
	AssertSuccess(t, body)
	data := body["data"].(map[string]interface{})
	if data["send_alert_once_every"] != "4h" || data["smtp_ready"] != false {
		t.Fatalf("unexpected default settings: %v", data)
	}

	// Create volume on a WinRM host → 400
	resp = ts.DoAuthRequest(t, "POST", "/api/config/disk-monitor/volumes", map[string]interface{}{"host_ref": "test-winrm-server", "mount_path": "/", "threshold_value": 5, "threshold_unit": "GB"})
	AssertStatus(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()

	// Create on SSH host
	resp = ts.DoAuthRequest(t, "POST", "/api/config/disk-monitor/volumes", map[string]interface{}{"host_ref": "test-ssh-server", "mount_path": "/data/", "threshold_value": 5, "threshold_unit": "GB"})
	AssertStatus(t, http.StatusCreated, resp.StatusCode)
	body = AssertJSONResponse(t, resp)
	resp.Body.Close()
	created := body["data"].(map[string]interface{})
	volumeID, _ := created["volume_id"].(string)
	if volumeID == "" || created["mount_path"] != "/data" || created["check_interval"] != "1h" {
		t.Fatalf("unexpected created volume: %v", created)
	}

	// Duplicate mount → 409
	resp = ts.DoAuthRequest(t, "POST", "/api/config/disk-monitor/volumes", map[string]interface{}{"host_ref": "test-ssh-server", "mount_path": "/data", "threshold_value": 5, "threshold_unit": "GB"})
	AssertStatus(t, http.StatusConflict, resp.StatusCode)
	resp.Body.Close()

	// Bulk create: one new, one duplicate
	resp = ts.DoAuthRequest(t, "POST", "/api/config/disk-monitor/volumes/bulk", map[string]interface{}{"host_ref": "test-ssh-server", "volumes": []map[string]interface{}{{"mount_path": "/", "threshold_value": 10, "threshold_unit": "%"}, {"mount_path": "/data", "threshold_value": 1, "threshold_unit": "GB"}}})
	AssertStatus(t, http.StatusCreated, resp.StatusCode)
	body = AssertJSONResponse(t, resp)
	resp.Body.Close()
	bulk := body["data"].(map[string]interface{})
	if len(bulk["created"].([]interface{})) != 1 || len(bulk["skipped"].([]interface{})) != 1 {
		t.Fatalf("unexpected bulk result: %v", bulk)
	}

	// Config persisted and reloaded
	cfg := cache.GetConfig()
	if cfg.DiskMonitor == nil || len(cfg.DiskMonitor.Volumes) != 2 {
		t.Fatalf("disk_monitor block not persisted: %+v", cfg.DiskMonitor)
	}

	// Update settings (volumes preserved)
	resp = ts.DoAuthRequest(t, "PUT", "/api/config/disk-monitor", map[string]interface{}{"enabled": false, "send_alert_once_every": "2h", "recipients": map[string]interface{}{"to": []string{"ops@example.com"}}, "daily_summary_time": "07:15"})
	AssertStatus(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	cfg = cache.GetConfig()
	if cfg.DiskMonitor.isEnabled() || cfg.DiskMonitor.SendAlertOnceEvery != "2h" || len(cfg.DiskMonitor.Volumes) != 2 || cfg.DiskMonitor.Recipients.To[0] != "ops@example.com" {
		t.Fatalf("settings update lost data: %+v", cfg.DiskMonitor)
	}
	if cfg.DiskMonitor.EmailSubject != diskMonitorDefaultEmailSubject {
		t.Fatalf("omitted email_subject should keep the default, got %q", cfg.DiskMonitor.EmailSubject)
	}

	// Custom email subject: runtime placeholders and config variables are accepted.
	resp = ts.DoAuthRequest(t, "PUT", "/api/config/disk-monitor", map[string]interface{}{"enabled": false, "send_alert_once_every": "2h", "recipients": map[string]interface{}{"to": []string{"ops@example.com"}}, "email_subject": "[{{test_var}}] {{summary}} on {{hosts}} host(s)"})
	AssertStatus(t, http.StatusOK, resp.StatusCode)
	body = AssertJSONResponse(t, resp)
	resp.Body.Close()
	if got := body["data"].(map[string]interface{})["email_subject"]; got != "[{{test_var}}] {{summary}} on {{hosts}} host(s)" {
		t.Fatalf("email_subject not returned: %v", got)
	}
	if cfg = cache.GetConfig(); cfg.DiskMonitor.EmailSubject != "[test_value] {{summary}} on {{hosts}} host(s)" {
		t.Fatalf("email_subject not loaded with config variable substituted: %q", cfg.DiskMonitor.EmailSubject)
	}
	resp = ts.DoAuthRequest(t, "PUT", "/api/config/disk-monitor", map[string]interface{}{"email_subject": "[DM] {{nope}}"})
	AssertStatus(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()

	// Host delete blocked
	resp = ts.DoAuthRequest(t, "DELETE", "/api/config/hosts/test-ssh-server", nil)
	AssertStatus(t, http.StatusConflict, resp.StatusCode)
	resp.Body.Close()

	// State endpoint
	resp = ts.DoAuthRequest(t, "GET", "/api/disk-monitor/state", nil)
	AssertStatus(t, http.StatusOK, resp.StatusCode)
	body = AssertJSONResponse(t, resp)
	resp.Body.Close()
	state := body["data"].(map[string]interface{})
	if state["enabled"] != false || len(state["volumes"].([]interface{})) != 2 {
		t.Fatalf("unexpected state view: %v", state)
	}

	// Check now while disabled → 409
	resp = ts.DoAuthRequest(t, "POST", "/api/disk-monitor/volumes/"+volumeID+"/check", nil)
	AssertStatus(t, http.StatusConflict, resp.StatusCode)
	resp.Body.Close()

	// Update volume
	resp = ts.DoAuthRequest(t, "PUT", "/api/config/disk-monitor/volumes/"+volumeID, map[string]interface{}{"host_ref": "test-ssh-server", "mount_path": "/data", "threshold_value": 20, "threshold_unit": "%", "check_interval": "30m", "enabled": false})
	AssertStatus(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	v, _ := cache.GetConfig().DiskMonitor.findVolume(volumeID)
	if v.ThresholdUnit != "%" || v.CheckInterval != "30m" || v.isEnabled() {
		t.Fatalf("volume update not applied: %+v", v)
	}

	// Delete volume
	resp = ts.DoAuthRequest(t, "DELETE", "/api/config/disk-monitor/volumes/"+volumeID, nil)
	AssertStatus(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	if len(cache.GetConfig().DiskMonitor.Volumes) != 1 {
		t.Fatalf("volume not deleted")
	}

	// Test email without SMTP → 400
	resp = ts.DoAuthRequest(t, "POST", "/api/disk-monitor/test-email", nil)
	AssertStatus(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()

	// Unauthenticated → 401
	resp = ts.DoRequest(t, "GET", "/api/disk-monitor/state", nil, nil)
	AssertStatus(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

func TestValidateConfigRejectsDiskMonitorOnWinRM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	config := `{"port": 8080, "log_retention_days": 3, "dev_mode": true, "hosts": {"w": {"connection_type": "winrm", "host": "h", "username": "u", "password": "p"}}, "commands": [], "disk_monitor": {"volumes": [{"volume_id": "v", "host_ref": "w", "mount_path": "/"}]}}`
	if err := os.WriteFile(path, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateConfig(path); err == nil {
		t.Fatalf("expected validation to fail for a WinRM host_ref")
	}
	okConfig := strings.Replace(config, `"connection_type": "winrm", `, ``, 1)
	if err := os.WriteFile(path, []byte(okConfig), 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateConfig(path); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}
