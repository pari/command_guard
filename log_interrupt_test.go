package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeInterruptTestLog(t *testing.T, dir, name, receivedAt, exitcode, errValue string) string {
	t.Helper()
	content := "               trace_id : abc123\n" +
		"    request_received_at : " + receivedAt + "\n" +
		"                 cmd_id : long-job\n" +
		"              remote_ip : cron-scheduler\n" +
		"              exec_time : 0s\n" +
		"                  error : " + errValue + "\n" +
		"               exitcode : " + exitcode + "\n" +
		"                 output:\n" +
		"line one\nline two\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMarkInterruptedLogs(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 10, 17, 13, 22, 0, 0, time.Local)

	stale := writeInterruptTestLog(t, dir, "2026_10_17_091000_long-job_aaa_log.txt", "2026-10-17 09:10:00.002", "-1", "job running")
	lastOutput := time.Date(2026, 10, 17, 10, 30, 43, 0, time.Local)
	if err := os.Chtimes(stale, lastOutput, lastOutput); err != nil {
		t.Fatal(err)
	}
	fresh := writeInterruptTestLog(t, dir, "2026_10_17_132300_long-job_bbb_log.txt", "2026-10-17 13:23:00.000", "-1", "job running")
	done := writeInterruptTestLog(t, dir, "2026_10_17_080000_long-job_ccc_log.txt", "2026-10-17 08:00:00.000", "0", "none")
	freshBefore, _ := os.ReadFile(fresh)
	doneBefore, _ := os.ReadFile(done)

	if fixed := markInterruptedLogs(dir, start); fixed != 1 {
		t.Fatalf("fixed = %d, want 1", fixed)
	}

	fields, err := readOverviewLogHeader(stale)
	if err != nil {
		t.Fatal(err)
	}
	if fields["exitcode"] != "1" {
		t.Fatalf("exitcode = %q, want 1", fields["exitcode"])
	}
	if !strings.HasPrefix(fields["error"], "Interrupted:") || !strings.Contains(fields["error"], "2026-10-17 10:30:43") {
		t.Fatalf("unexpected error field %q", fields["error"])
	}
	if fields["exec_time"] != "4842.998s" {
		t.Fatalf("exec_time = %q, want 4842.998s", fields["exec_time"])
	}
	content, _ := os.ReadFile(stale)
	if !strings.HasSuffix(string(content), "output:\nline one\nline two\n") || strings.Contains(string(content), "job running") {
		t.Fatalf("output body not preserved or running status left behind:\n%s", content)
	}

	if after, _ := os.ReadFile(fresh); string(after) != string(freshBefore) {
		t.Fatal("log started after the process start was modified")
	}
	if after, _ := os.ReadFile(done); string(after) != string(doneBefore) {
		t.Fatal("completed log was modified")
	}

	// A second sweep finds nothing left to fix.
	if fixed := markInterruptedLogs(dir, start); fixed != 0 {
		t.Fatalf("second sweep fixed = %d, want 0", fixed)
	}
}
