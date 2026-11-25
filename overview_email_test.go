package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

func TestBuildScheduledJobsOverviewRangeAcrossMidnight(t *testing.T) {
	logsDir := t.TempDir()
	location := time.UTC
	windowStart := time.Date(2038, time.January, 2, 18, 30, 0, 0, location)
	windowEnd := windowStart.Add(24 * time.Hour)
	now := windowEnd.Add(time.Minute)

	writeOverviewTestLog(t, logsDir, "2038-01-02 19:15:00.000", "hourly", "0000000000000300", "cron-scheduler", "", "2s", "0")
	writeOverviewTestLog(t, logsDir, "2038-01-03 07:15:00.000", "hourly", "0000000000000301", "cron-scheduler", "", "3s", "1")

	config := &Config{
		CronEnabled:      "Y",
		LogRetentionDays: 3,
		Commands:         []Command{{CmdID: "hourly", CronSchedule: "15 * * * *"}},
	}
	overview, err := buildScheduledJobsOverviewRange(config, windowStart, windowEnd, now, logsDir)
	if err != nil {
		t.Fatalf("build rolling overview: %v", err)
	}
	if overview.Summary.ExpectedRuns != 24 {
		t.Fatalf("expected 24 projected runs, got %d", overview.Summary.ExpectedRuns)
	}
	if overview.Summary.ObservedRuns != 2 || overview.Summary.FailedRuns != 1 {
		t.Fatalf("expected two cross-date observations with one failure, got %#v", overview.Summary)
	}
	if overview.Hourly[0].Expected != 1 || overview.Hourly[12].Observed != 1 {
		t.Fatalf("unexpected rolling hourly buckets: first=%#v thirteenth=%#v", overview.Hourly[0], overview.Hourly[12])
	}
}

func TestScheduledJobsOverviewEmailWindows(t *testing.T) {
	location := time.FixedZone("IST", 5*60*60+30*60)
	now := time.Date(2039, time.March, 8, 9, 45, 0, 0, location)

	start, end, _ := scheduledJobsOverviewEmailWindow(scheduledJobsOverviewPeriodPreviousDay, now)
	if start.Format(time.RFC3339) != "2039-03-07T00:00:00+05:30" || end.Format(time.RFC3339) != "2039-03-08T00:00:00+05:30" {
		t.Fatalf("unexpected previous-day window: %s to %s", start, end)
	}

	start, end, _ = scheduledJobsOverviewEmailWindow(scheduledJobsOverviewPeriodPast24Hours, now)
	if !end.Equal(now) || end.Sub(start) != 24*time.Hour {
		t.Fatalf("unexpected rolling window: %s to %s", start, end)
	}
}

func TestScheduledJobsOverviewEmailConfigValidation(t *testing.T) {
	enabled := true
	smtp := &SMTPConfig{Host: "smtp.example.com", Port: 587, FromEmail: "sender@example.com", Enabled: &enabled, Timeout: 30}
	valid := ScheduledJobsOverviewEmailConfig{Enabled: true, Recipient: "recipient@example.com", Hour: 7, Minute: 5, Period: scheduledJobsOverviewPeriodPreviousDay, Subject: scheduledJobsOverviewEmailDefaultSubject}
	if err := validateScheduledJobsOverviewEmailConfig(valid, smtp, true, nil); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}

	tests := []ScheduledJobsOverviewEmailConfig{
		{Enabled: true, Recipient: "not-an-email", Hour: 7, Minute: 5, Period: scheduledJobsOverviewPeriodPreviousDay, Subject: scheduledJobsOverviewEmailDefaultSubject},
		{Enabled: true, Recipient: "recipient@example.com", Hour: 24, Minute: 5, Period: scheduledJobsOverviewPeriodPreviousDay, Subject: scheduledJobsOverviewEmailDefaultSubject},
		{Enabled: true, Recipient: "recipient@example.com", Hour: 7, Minute: 60, Period: scheduledJobsOverviewPeriodPreviousDay, Subject: scheduledJobsOverviewEmailDefaultSubject},
		{Enabled: true, Recipient: "recipient@example.com", Hour: 7, Minute: 5, Period: "weekly", Subject: scheduledJobsOverviewEmailDefaultSubject},
		{Enabled: true, Recipient: "recipient@example.com", Hour: 7, Minute: 5, Period: scheduledJobsOverviewPeriodPreviousDay, Subject: "Overview {{unknown}}"},
		{Enabled: true, Recipient: "recipient@example.com", Hour: 7, Minute: 5, Period: scheduledJobsOverviewPeriodPreviousDay, Subject: "Overview\r\nBcc: attacker@example.com"},
		{Enabled: true, Recipient: "recipient@example.com", Hour: 7, Minute: 5, Period: scheduledJobsOverviewPeriodPreviousDay, Subject: strings.Repeat("x", scheduledJobsOverviewEmailMaxSubjectLength+1)},
	}
	for _, settings := range tests {
		if err := validateScheduledJobsOverviewEmailConfig(settings, smtp, true, nil); err == nil {
			t.Fatalf("invalid settings accepted: %#v", settings)
		}
	}
	if err := validateScheduledJobsOverviewEmailConfig(valid, nil, true, nil); err == nil {
		t.Fatal("enabled settings should require ready SMTP")
	}
}

func TestScheduledJobsOverviewEmailSubjectDefaultsAndPlaceholders(t *testing.T) {
	normalized := normalizeScheduledJobsOverviewEmailConfig(&ScheduledJobsOverviewEmailConfig{Period: scheduledJobsOverviewPeriodPreviousDay})
	if normalized.Subject != scheduledJobsOverviewEmailDefaultSubject {
		t.Fatalf("blank subject did not receive the default: %q", normalized.Subject)
	}

	location := time.FixedZone("IST", 5*60*60+30*60)
	windowStart := time.Date(2040, time.June, 10, 8, 15, 0, 0, location)
	windowEnd := windowStart.Add(24 * time.Hour)
	windowLabel := "Past 24 hours · 2040-06-10 08:15 to 2040-06-11 08:15"
	subject, err := renderScheduledJobsOverviewEmailSubject(
		"{{period}} | {{window_start}} | {{window_end}} | {{window_label}}",
		scheduledJobsOverviewPeriodPast24Hours,
		windowStart,
		windowEnd,
		windowLabel,
	)
	if err != nil {
		t.Fatalf("render subject: %v", err)
	}
	expected := "Past 24 hours | 2040-06-10 08:15 | 2040-06-11 08:15 | " + windowLabel
	if subject != expected {
		t.Fatalf("unexpected rendered subject:\nwant %q\n got %q", expected, subject)
	}

	defaultSubject, err := renderScheduledJobsOverviewEmailSubject(
		scheduledJobsOverviewEmailDefaultSubject,
		scheduledJobsOverviewPeriodPreviousDay,
		windowStart,
		windowEnd,
		"Previous day · 2040-06-10",
	)
	if err != nil {
		t.Fatalf("render default subject: %v", err)
	}
	if defaultSubject != "Scheduled Jobs Overview — Previous day · 2040-06-10" {
		t.Fatalf("default subject changed unexpectedly: %q", defaultSubject)
	}
}

func TestScheduledJobsOverviewEmailSubjectAllowsKnownConfigVariables(t *testing.T) {
	settings := defaultScheduledJobsOverviewEmailConfig()
	settings.Subject = "{{report_name}} — {{window_label}}"
	variables := map[string]interface{}{"report_name": "Operations"}
	if err := validateScheduledJobsOverviewEmailConfig(settings, nil, false, variables); err != nil {
		t.Fatalf("known config variable was rejected: %v", err)
	}

	jsonText := `{"subject":"{{report_name}} — {{window_label}}"}`
	substituted, err := substituteVariables(jsonText, variables)
	if err != nil {
		t.Fatalf("substitute subject variables: %v", err)
	}
	if substituted != `{"subject":"Operations — {{window_label}}"}` {
		t.Fatalf("unexpected subject substitution: %s", substituted)
	}
}

func TestRenderScheduledJobsOverviewEmailEscapesDynamicValues(t *testing.T) {
	windowStart := time.Date(2040, time.June, 10, 0, 0, 0, 0, time.UTC)
	windowEnd := windowStart.Add(24 * time.Hour)
	overview := &ScheduledJobsOverview{
		GeneratedAt: windowEnd.Format(time.RFC3339),
		Jobs: []OverviewJob{{
			JobID: "<job&>",
			Type:  "command",
			Occurrences: []OverviewOccurrence{{
				ScheduledAt:            windowStart.Add(time.Hour).Format(time.RFC3339),
				ScheduledOffsetSeconds: 3600,
				Status:                 "success",
			}},
		}},
		Summary: OverviewSummary{ExpectedRuns: 1, ObservedRuns: 1, SuccessRuns: 1},
		Hourly:  make([]OverviewHourlyBucket, 24),
	}
	config := &Config{AccessURL: "https://example.test/app?a=1&b=2"}
	htmlBody, textBody, subject, err := renderScheduledJobsOverviewEmail(config, overview, windowStart, windowEnd, "Previous day · 2040-06-10")
	if err != nil {
		t.Fatalf("render email: %v", err)
	}
	if strings.Contains(htmlBody, "<job&>") || !strings.Contains(htmlBody, "&lt;job&amp;&gt;") {
		t.Fatalf("job ID was not safely escaped: %s", htmlBody)
	}
	if !strings.Contains(htmlBody, "a=1&amp;b=2") {
		t.Fatal("access URL was not safely escaped")
	}
	if !strings.Contains(textBody, "<job&>") || !strings.Contains(subject, "Scheduled Jobs Overview") {
		t.Fatal("plain-text fallback or subject is incomplete")
	}
}

func TestOverviewEmailSchedulerIgnoresCommandCronSwitch(t *testing.T) {
	oldCron := cronScheduler
	oldJobs := scheduledJobs
	oldEntryID := scheduledJobsOverviewEmailEntryID
	cronScheduler = cron.New()
	scheduledJobs = make(map[string]cron.EntryID)
	scheduledJobsOverviewEmailEntryID = 0
	t.Cleanup(func() {
		cronScheduler.Stop()
		cronScheduler = oldCron
		scheduledJobs = oldJobs
		scheduledJobsOverviewEmailEntryID = oldEntryID
	})

	enabled := true
	config := &Config{
		CronEnabled: "N",
		SMTP:        &SMTPConfig{Host: "smtp.example.com", Port: 587, FromEmail: "sender@example.com", Enabled: &enabled, Timeout: 30},
		ScheduledJobsOverviewEmail: &ScheduledJobsOverviewEmailConfig{
			Enabled: true, Recipient: "recipient@example.com", Hour: 6, Minute: 30, Period: scheduledJobsOverviewPeriodPast24Hours,
		},
	}
	updateCronJobs(config)
	if scheduledJobsOverviewEmailEntryID == 0 {
		t.Fatal("overview email was not scheduled while Command/RunBook cron was disabled")
	}
	if len(scheduledJobs) != 0 {
		t.Fatalf("unexpected Command/RunBook schedules: %#v", scheduledJobs)
	}
}

func TestScheduledJobsOverviewEmailAPIPersistsSettings(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	configMap := testConfigForVersionStamp(AppVersion)
	var configBuffer bytes.Buffer
	encoder := json.NewEncoder(&configBuffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "\t")
	if err := encoder.Encode(configMap); err != nil {
		t.Fatalf("encode config: %v", err)
	}
	if err := os.WriteFile(configPath, configBuffer.Bytes(), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	oldCache := cache
	oldCron := cronScheduler
	oldJobs := scheduledJobs
	oldEntryID := scheduledJobsOverviewEmailEntryID
	oldAuthManager := authManager
	cache = &ConfigCache{configFile: configPath}
	cronScheduler = cron.New()
	scheduledJobs = make(map[string]cron.EntryID)
	scheduledJobsOverviewEmailEntryID = 0
	authManager = nil
	t.Cleanup(func() {
		cronScheduler.Stop()
		cache = oldCache
		cronScheduler = oldCron
		scheduledJobs = oldJobs
		scheduledJobsOverviewEmailEntryID = oldEntryID
		authManager = oldAuthManager
	})
	if err := cache.loadIfChanged(); err != nil {
		t.Fatalf("load config: %v", err)
	}

	payload := []byte(`{"enabled":true,"recipient":"recipient@example.com","hour":6,"minute":45,"period":"past_24_hours","subject":"Operations — {{window_label}}"}`)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/config/scheduled-jobs-overview-email", bytes.NewReader(payload))
	NewConfigAPI(configPath, nil).HandleScheduledJobsOverviewEmail(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected update status %d: %s", recorder.Code, recorder.Body.String())
	}

	savedData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	var saved Config
	if err := json.Unmarshal(savedData, &saved); err != nil {
		t.Fatalf("parse saved config: %v", err)
	}
	if saved.ScheduledJobsOverviewEmail == nil || !saved.ScheduledJobsOverviewEmail.Enabled || saved.ScheduledJobsOverviewEmail.Minute != 45 || saved.ScheduledJobsOverviewEmail.Subject != "Operations — {{window_label}}" {
		t.Fatalf("settings were not persisted: %#v", saved.ScheduledJobsOverviewEmail)
	}
	if scheduledJobsOverviewEmailEntryID == 0 {
		t.Fatal("hot reload did not register the daily overview email")
	}
}
