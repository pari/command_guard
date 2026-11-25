package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeOverviewTestLog(t *testing.T, dir, timestamp, cmdID, traceID, source, runBookID, duration, exitCode string) {
	t.Helper()
	parsed, err := time.Parse("2006-01-02 15:04:05.000", timestamp)
	if err != nil {
		t.Fatalf("parse test timestamp: %v", err)
	}
	name := fmt.Sprintf("%s_%s_%s_log.txt", parsed.Format("2006_01_02_150405"), cmdID, traceID)
	content := fmt.Sprintf("               trace_id : %s\n    request_received_at : %s\n                 cmd_id : %s\n                   host : test-host\n              remote_ip : %s\n                  agent : %s\n", traceID, timestamp, cmdID, source, source)
	if runBookID != "" {
		content += fmt.Sprintf("             runbook_id : %s\n", runBookID)
	}
	content += fmt.Sprintf("              exec_time : %s\n                  error : none\n               exitcode : %s\n                 output:\n               exitcode : 99\n", duration, exitCode)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write test log: %v", err)
	}
}

func TestBuildScheduledJobsOverviewMatchesCronLogs(t *testing.T) {
	logsDir := t.TempDir()
	location := time.FixedZone("IST", 5*60*60+30*60)
	selected := time.Date(2035, time.January, 2, 0, 0, 0, 0, location)
	now := time.Date(2035, time.January, 2, 12, 0, 0, 0, location)

	for index, minute := range []int{0, 15, 30, 45} {
		duration := "1s"
		if minute == 45 {
			duration = "10s"
		}
		writeOverviewTestLog(t, logsDir, fmt.Sprintf("2035-01-02 10:%02d:00.100", minute), "batch", fmt.Sprintf("%016x", index+1), "cron-scheduler", "", duration, "0")
	}
	writeOverviewTestLog(t, logsDir, "2035-01-02 11:00:00.100", "step-command", "0000000000000100", "cron-scheduler", "nightly-runbook", "2s", "0")
	writeOverviewTestLog(t, logsDir, "2035-01-02 11:00:00.200", "step-command", "0000000000000101", "browser-user", "", "2s", "0")

	cfg := &Config{
		CronEnabled:      "Y",
		LogRetentionDays: 3,
		Commands: []Command{
			{CmdID: "batch", CronSchedule: "0,15,30,45 10 * * *"},
			{CmdID: "step-command", CronSchedule: "0 11 * * *"},
		},
	}
	overview, err := buildScheduledJobsOverview(cfg, selected, now, logsDir)
	if err != nil {
		t.Fatalf("build overview: %v", err)
	}

	if overview.Summary.ExpectedRuns != 5 {
		t.Fatalf("expected 5 projected runs, got %d", overview.Summary.ExpectedRuns)
	}
	if overview.Summary.ObservedRuns != 4 {
		t.Fatalf("expected 4 cron runs after exclusions, got %d", overview.Summary.ObservedRuns)
	}
	if overview.Summary.LongRuns != 1 {
		t.Fatalf("expected one long run, got %d", overview.Summary.LongRuns)
	}
	if overview.Summary.NoRecordRuns != 1 {
		t.Fatalf("expected the RunBook step-only occurrence to have no record, got %d", overview.Summary.NoRecordRuns)
	}
	if got := overview.Jobs[0].Occurrences[3].Status; got != "long_run" {
		t.Fatalf("expected final batch occurrence to be long_run, got %q", got)
	}
}

func TestBuildScheduledJobsOverviewPreservesUnmatchedActual(t *testing.T) {
	logsDir := t.TempDir()
	location := time.UTC
	selected := time.Date(2036, time.March, 5, 0, 0, 0, 0, location)
	now := time.Date(2036, time.March, 5, 12, 0, 0, 0, location)
	writeOverviewTestLog(t, logsDir, "2036-03-05 10:05:00.000", "changed-job", "0000000000000200", "cron-scheduler", "", "3s", "0")

	cfg := &Config{
		CronEnabled:      "Y",
		LogRetentionDays: 3,
		Commands:         []Command{{CmdID: "changed-job", CronSchedule: "0 10 * * *"}},
	}
	overview, err := buildScheduledJobsOverview(cfg, selected, now, logsDir)
	if err != nil {
		t.Fatalf("build overview: %v", err)
	}
	if len(overview.Jobs) != 1 || len(overview.Jobs[0].Occurrences) != 2 {
		t.Fatalf("expected projected and unmatched actual occurrences, got %#v", overview.Jobs)
	}
	if !overview.Jobs[0].Occurrences[1].UnmatchedActual {
		t.Fatal("expected the observed 10:05 run to be marked unmatched_actual")
	}
	if overview.Summary.ExpectedRuns != 1 || overview.Summary.ObservedRuns != 1 {
		t.Fatalf("unexpected summary: %#v", overview.Summary)
	}
}

func TestBuildScheduledJobsOverviewRetentionAndDisabledStates(t *testing.T) {
	logsDir := t.TempDir()
	location := time.UTC
	now := time.Date(2037, time.April, 10, 12, 0, 0, 0, location)
	cfg := &Config{
		CronEnabled:      "N",
		LogRetentionDays: 3,
		Commands:         []Command{{CmdID: "daily", CronSchedule: "0 18 * * *"}},
	}

	today, err := buildScheduledJobsOverview(cfg, now, now, logsDir)
	if err != nil {
		t.Fatalf("build today overview: %v", err)
	}
	if got := today.Jobs[0].Occurrences[0].Status; got != "disabled" {
		t.Fatalf("expected future occurrence to be disabled, got %q", got)
	}

	oldDay := time.Date(2037, time.April, 1, 0, 0, 0, 0, location)
	old, err := buildScheduledJobsOverview(cfg, oldDay, now, logsDir)
	if err != nil {
		t.Fatalf("build historical overview: %v", err)
	}
	if got := old.Jobs[0].Occurrences[0].Status; got != "history_unavailable" {
		t.Fatalf("expected retained history to be unavailable, got %q", got)
	}
}

func TestReadOverviewLogHeaderStopsAtOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	content := "trace_id : abcdef0123456789\nexitcode : 0\noutput:\nexitcode : 99\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	fields, err := readOverviewLogHeader(path)
	if err != nil {
		t.Fatalf("read header: %v", err)
	}
	if fields["exitcode"] != "0" {
		t.Fatalf("expected header exitcode, got %q", fields["exitcode"])
	}
}
