package main

import (
	"bytes"
	"fmt"
	"html"
	"net/mail"
	"regexp"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
	"gopkg.in/gomail.v2"
)

const (
	scheduledJobsOverviewPeriodPreviousDay     = "previous_day"
	scheduledJobsOverviewPeriodPast24Hours     = "past_24_hours"
	scheduledJobsOverviewEmailDefaultSubject   = "Scheduled Jobs Overview — {{window_label}}"
	scheduledJobsOverviewEmailMaxSubjectLength = 200
	overviewEmailTimelineBucketCount           = 96
	overviewEmailTimelineBucketDuration        = 15 * time.Minute
)

var scheduledJobsOverviewEmailSubjectPlaceholderPattern = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

var scheduledJobsOverviewEmailSubjectPlaceholders = map[string]struct{}{
	"window_label": {},
	"period":       {},
	"window_start": {},
	"window_end":   {},
}

type ScheduledJobsOverviewEmailConfig struct {
	Enabled   bool   `json:"enabled"`
	Recipient string `json:"recipient"`
	Hour      int    `json:"hour"`
	Minute    int    `json:"minute"`
	Period    string `json:"period"`
	Subject   string `json:"subject"`
}

type ScheduledJobsOverviewEmailSettingsResponse struct {
	Settings  ScheduledJobsOverviewEmailConfig `json:"settings"`
	SMTPReady bool                             `json:"smtp_ready"`
}

var scheduledJobsOverviewEmailEntryID cron.EntryID

func defaultScheduledJobsOverviewEmailConfig() ScheduledJobsOverviewEmailConfig {
	return ScheduledJobsOverviewEmailConfig{
		Hour:    8,
		Minute:  0,
		Period:  scheduledJobsOverviewPeriodPreviousDay,
		Subject: scheduledJobsOverviewEmailDefaultSubject,
	}
}

func normalizeScheduledJobsOverviewEmailConfig(config *ScheduledJobsOverviewEmailConfig) ScheduledJobsOverviewEmailConfig {
	normalized := defaultScheduledJobsOverviewEmailConfig()
	if config == nil {
		return normalized
	}
	normalized = *config
	normalized.Recipient = strings.TrimSpace(normalized.Recipient)
	normalized.Period = strings.TrimSpace(strings.ToLower(normalized.Period))
	normalized.Subject = strings.TrimSpace(normalized.Subject)
	if normalized.Period == "" {
		normalized.Period = scheduledJobsOverviewPeriodPreviousDay
	}
	if normalized.Subject == "" {
		normalized.Subject = scheduledJobsOverviewEmailDefaultSubject
	}
	return normalized
}

func scheduledJobsOverviewSMTPReady(config *SMTPConfig) bool {
	if config == nil || !config.isEnabled() {
		return false
	}
	normalized, err := normalizeSMTPConfig(config, false)
	return err == nil && validateSMTPConfig(normalized) == nil
}

func validateScheduledJobsOverviewEmailConfig(config ScheduledJobsOverviewEmailConfig, smtp *SMTPConfig, requireSMTP bool, configVariables map[string]interface{}) error {
	if config.Hour < 0 || config.Hour > 23 {
		return fmt.Errorf("delivery hour must be between 0 and 23")
	}
	if config.Minute < 0 || config.Minute > 59 {
		return fmt.Errorf("delivery minute must be between 0 and 59")
	}
	if config.Period != scheduledJobsOverviewPeriodPreviousDay && config.Period != scheduledJobsOverviewPeriodPast24Hours {
		return fmt.Errorf("period must be one of: previous_day, past_24_hours")
	}
	if err := validateScheduledJobsOverviewEmailSubject(config.Subject, configVariables); err != nil {
		return err
	}
	if config.Recipient != "" {
		address, err := mail.ParseAddress(config.Recipient)
		if err != nil || address.Address != config.Recipient || address.Name != "" {
			return fmt.Errorf("a valid recipient email address is required")
		}
	}
	if !config.Enabled {
		return nil
	}
	if config.Recipient == "" {
		return fmt.Errorf("recipient email address is required when daily delivery is enabled")
	}
	if requireSMTP && !scheduledJobsOverviewSMTPReady(smtp) {
		return fmt.Errorf("SMTP must be configured and enabled before daily overview email can be enabled")
	}
	return nil
}

func validateScheduledJobsOverviewEmailSubject(subject string, configVariables map[string]interface{}) error {
	if strings.ContainsAny(subject, "\r\n") {
		return fmt.Errorf("email subject must not contain line breaks")
	}
	if utf8.RuneCountInString(subject) > scheduledJobsOverviewEmailMaxSubjectLength {
		return fmt.Errorf("email subject must not exceed %d characters", scheduledJobsOverviewEmailMaxSubjectLength)
	}
	for _, match := range scheduledJobsOverviewEmailSubjectPlaceholderPattern.FindAllStringSubmatch(subject, -1) {
		placeholder := match[1]
		if _, allowed := scheduledJobsOverviewEmailSubjectPlaceholders[placeholder]; allowed {
			continue
		}
		if _, isConfigVariable := configVariables[placeholder]; isConfigVariable {
			continue
		}
		return fmt.Errorf("unsupported email subject placeholder %q", match[0])
	}
	return nil
}

func scheduledJobsOverviewEmailSettingsResponse(config *Config) ScheduledJobsOverviewEmailSettingsResponse {
	response := ScheduledJobsOverviewEmailSettingsResponse{Settings: defaultScheduledJobsOverviewEmailConfig()}
	if config == nil {
		return response
	}
	response.Settings = normalizeScheduledJobsOverviewEmailConfig(config.ScheduledJobsOverviewEmail)
	response.SMTPReady = scheduledJobsOverviewSMTPReady(config.SMTP)
	return response
}

func scheduleScheduledJobsOverviewEmail(config *Config) {
	if config == nil || cronScheduler == nil {
		return
	}
	settings := normalizeScheduledJobsOverviewEmailConfig(config.ScheduledJobsOverviewEmail)
	if !settings.Enabled {
		return
	}
	if err := validateScheduledJobsOverviewEmailConfig(settings, config.SMTP, false, config.Variables); err != nil {
		logSMTPError("Daily Scheduled Jobs Overview email is not scheduled: " + err.Error())
		return
	}

	spec := fmt.Sprintf("%d %d * * *", settings.Minute, settings.Hour)
	entryID, err := cronScheduler.AddFunc(spec, queueScheduledJobsOverviewEmail)
	if err != nil {
		logSMTPError(fmt.Sprintf("Failed to schedule daily Scheduled Jobs Overview email with schedule %q: %v", spec, err))
		return
	}
	scheduledJobsOverviewEmailEntryID = entryID
	fmt.Printf("[CRON] Scheduled daily Scheduled Jobs Overview email to %s at %02d:%02d\n", settings.Recipient, settings.Hour, settings.Minute)
}

func queueScheduledJobsOverviewEmail() {
	appShutdown.RunAsync("daily Scheduled Jobs Overview email", func() {
		if err := sendScheduledJobsOverviewEmail(time.Now(), "logs"); err != nil {
			logSMTPError("Failed to send daily Scheduled Jobs Overview email: " + err.Error())
		}
	})
}

func sendScheduledJobsOverviewEmail(now time.Time, logsDir string) error {
	config := cache.GetConfig()
	if config == nil {
		return fmt.Errorf("configuration is not loaded")
	}
	settings := normalizeScheduledJobsOverviewEmailConfig(config.ScheduledJobsOverviewEmail)
	if !settings.Enabled {
		return nil
	}
	if err := validateScheduledJobsOverviewEmailConfig(settings, config.SMTP, true, config.Variables); err != nil {
		return err
	}

	windowStart, windowEnd, windowLabel := scheduledJobsOverviewEmailWindow(settings.Period, now)
	overview, err := buildScheduledJobsOverviewRange(config, windowStart, windowEnd, now, logsDir)
	if err != nil {
		return fmt.Errorf("build overview: %w", err)
	}
	htmlBody, textBody, subject, err := renderScheduledJobsOverviewEmail(config, overview, windowStart, windowEnd, windowLabel)
	if err != nil {
		return fmt.Errorf("render overview email: %w", err)
	}

	message := gomail.NewMessage()
	message.SetHeader("To", settings.Recipient)
	message.SetHeader("Subject", subject)
	message.SetBody("text/plain", textBody)
	message.AddAlternative("text/html", htmlBody)
	if err := sendSMTPMessage(config.SMTP, message); err != nil {
		return err
	}
	fmt.Printf("[EMAIL] Sent daily Scheduled Jobs Overview to %s for %s\n", settings.Recipient, windowLabel)
	return nil
}

func scheduledJobsOverviewEmailWindow(period string, now time.Time) (time.Time, time.Time, string) {
	if period == scheduledJobsOverviewPeriodPast24Hours {
		end := now.Truncate(time.Minute)
		start := end.Add(-24 * time.Hour)
		return start, end, fmt.Sprintf("Past 24 hours · %s to %s", start.Format("2006-01-02 15:04"), end.Format("2006-01-02 15:04"))
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	start := today.AddDate(0, 0, -1)
	return start, today, fmt.Sprintf("Previous day · %s", start.Format("2006-01-02"))
}

type overviewEmailKPI struct {
	Label  string
	Value  string
	Detail string
	Color  string
}

type overviewEmailTimelineSegment struct {
	Color string
	Width int
}

type overviewEmailTimelineCell struct {
	Title    string
	Segments []overviewEmailTimelineSegment
}

type overviewEmailTimelineRow struct {
	JobID string
	Type  string
	Cells []overviewEmailTimelineCell
}

type overviewEmailHourlyGroup struct {
	Label    string
	Expected int
	Observed int
}

type overviewEmailOutcome struct {
	Label string
	Value int
	Color string
}

type scheduledJobsOverviewEmailView struct {
	WindowLabel     string
	WindowRange     string
	Timezone        string
	GeneratedAt     string
	ProjectionNote  string
	AccessURL       string
	KPIs            []overviewEmailKPI
	TimelineHeaders []string
	TimelineRows    []overviewEmailTimelineRow
	HourlyGroups    []overviewEmailHourlyGroup
	Legend          []overviewEmailOutcome
	Outcomes        []overviewEmailOutcome
}

var overviewEmailStatusOrder = []string{
	"failed", "long_run", "running", "skipped", "success", "no_record", "upcoming", "disabled", "history_unavailable",
}

var overviewEmailStatusLabels = map[string]string{
	"success":             "Success",
	"failed":              "Failed",
	"long_run":            "Long run",
	"running":             "Running",
	"skipped":             "Skipped",
	"no_record":           "No record",
	"upcoming":            "Upcoming",
	"disabled":            "Disabled",
	"history_unavailable": "History unavailable",
}

var overviewEmailStatusColors = map[string]string{
	"success":             "#4caf50",
	"failed":              "#ef4444",
	"long_run":            "#f59e0b",
	"running":             "#2563eb",
	"skipped":             "#8b5cf6",
	"no_record":           "#9ca3af",
	"upcoming":            "#93c5fd",
	"disabled":            "#64748b",
	"history_unavailable": "#d1d5db",
}

func renderScheduledJobsOverviewEmail(config *Config, overview *ScheduledJobsOverview, windowStart, windowEnd time.Time, windowLabel string) (string, string, string, error) {
	view := buildScheduledJobsOverviewEmailView(config, overview, windowStart, windowEnd, windowLabel)
	var htmlBody bytes.Buffer
	if err := scheduledJobsOverviewEmailTemplate.Execute(&htmlBody, view); err != nil {
		return "", "", "", err
	}

	var textBody strings.Builder
	fmt.Fprintf(&textBody, "Scheduled Jobs Overview\n%s\n%s\n\n", view.WindowLabel, view.Timezone)
	for _, kpi := range view.KPIs {
		fmt.Fprintf(&textBody, "%s: %s (%s)\n", kpi.Label, kpi.Value, kpi.Detail)
	}
	textBody.WriteString("\nOutcomes:\n")
	for _, outcome := range view.Outcomes {
		fmt.Fprintf(&textBody, "- %s: %d\n", outcome.Label, outcome.Value)
	}
	textBody.WriteString("\nJobs:\n")
	for _, row := range view.TimelineRows {
		fmt.Fprintf(&textBody, "- %s (%s)\n", row.JobID, row.Type)
	}
	if view.AccessURL != "" {
		fmt.Fprintf(&textBody, "\nOpen Scheduled Jobs Overview: %s\n", view.AccessURL)
	}
	textBody.WriteString("\nExpected runs are projected from the current cron configuration.\n")

	settings := normalizeScheduledJobsOverviewEmailConfig(config.ScheduledJobsOverviewEmail)
	subject, err := renderScheduledJobsOverviewEmailSubject(settings.Subject, settings.Period, windowStart, windowEnd, windowLabel)
	if err != nil {
		return "", "", "", err
	}
	return htmlBody.String(), textBody.String(), subject, nil
}

func renderScheduledJobsOverviewEmailSubject(subjectTemplate, period string, windowStart, windowEnd time.Time, windowLabel string) (string, error) {
	periodLabel := "Previous day"
	if period == scheduledJobsOverviewPeriodPast24Hours {
		periodLabel = "Past 24 hours"
	}
	subject := strings.NewReplacer(
		"{{window_label}}", windowLabel,
		"{{period}}", periodLabel,
		"{{window_start}}", windowStart.Format("2006-01-02 15:04"),
		"{{window_end}}", windowEnd.Format("2006-01-02 15:04"),
	).Replace(subjectTemplate)
	if strings.ContainsAny(subject, "\r\n") {
		return "", fmt.Errorf("rendered email subject must not contain line breaks")
	}
	if utf8.RuneCountInString(subject) > scheduledJobsOverviewEmailMaxSubjectLength {
		return "", fmt.Errorf("rendered email subject must not exceed %d characters", scheduledJobsOverviewEmailMaxSubjectLength)
	}
	if match := scheduledJobsOverviewEmailSubjectPlaceholderPattern.FindString(subject); match != "" {
		return "", fmt.Errorf("unsupported email subject placeholder %q", match)
	}
	return subject, nil
}

func buildScheduledJobsOverviewEmailView(config *Config, overview *ScheduledJobsOverview, windowStart, windowEnd time.Time, windowLabel string) scheduledJobsOverviewEmailView {
	zoneName, zoneOffset := windowStart.Zone()
	timezone := formatOverviewEmailTimezone(zoneName, zoneOffset)
	summary := overview.Summary
	view := scheduledJobsOverviewEmailView{
		WindowLabel:    windowLabel,
		WindowRange:    fmt.Sprintf("%s — %s", windowStart.Format("2006-01-02 15:04"), windowEnd.Format("2006-01-02 15:04")),
		Timezone:       timezone,
		GeneratedAt:    overview.GeneratedAt,
		ProjectionNote: "Expected runs use cron expressions from the current configuration.",
		KPIs: []overviewEmailKPI{
			{Label: "Observed runs", Value: formatOverviewEmailNumber(summary.ObservedRuns), Detail: fmt.Sprintf("%s expected", formatOverviewEmailNumber(summary.ExpectedRuns)), Color: "#2f73c9"},
			{Label: "Failed runs", Value: formatOverviewEmailNumber(summary.FailedRuns), Detail: "Completed failures", Color: "#dc3545"},
			{Label: "Average runtime", Value: formatOverviewEmailDuration(summary.AverageRuntimeMS), Detail: "Completed observed runs", Color: "#0f83a6"},
			{Label: "Longest run", Value: formatOverviewEmailDuration(summary.LongestRuntimeMS), Detail: firstNonEmpty(summary.LongestRuntimeJob, "No completed runs"), Color: "#c47700"},
		},
		Legend: []overviewEmailOutcome{
			{Label: "Success", Color: overviewEmailStatusColors["success"]},
			{Label: "Failed", Color: overviewEmailStatusColors["failed"]},
			{Label: "Long run", Color: overviewEmailStatusColors["long_run"]},
			{Label: "Running", Color: overviewEmailStatusColors["running"]},
			{Label: "No record", Color: overviewEmailStatusColors["no_record"]},
			{Label: "Upcoming", Color: overviewEmailStatusColors["upcoming"]},
		},
	}
	if config != nil && strings.TrimSpace(config.AccessURL) != "" {
		view.AccessURL = strings.TrimRight(strings.TrimSpace(config.AccessURL), "/") + "/#overview"
	}
	for hour := 0; hour < 24; hour += 2 {
		view.TimelineHeaders = append(view.TimelineHeaders, windowStart.Add(time.Duration(hour)*time.Hour).Format("15:04"))
	}
	for _, job := range overview.Jobs {
		view.TimelineRows = append(view.TimelineRows, buildOverviewEmailTimelineRow(job, windowStart, windowEnd))
	}
	for hour := 0; hour < 24; hour += 2 {
		group := overviewEmailHourlyGroup{Label: windowStart.Add(time.Duration(hour) * time.Hour).Format("15:04")}
		for offset := 0; offset < 2 && hour+offset < len(overview.Hourly); offset++ {
			group.Expected += overview.Hourly[hour+offset].Expected
			group.Observed += overview.Hourly[hour+offset].Observed
		}
		view.HourlyGroups = append(view.HourlyGroups, group)
	}
	view.Outcomes = buildOverviewEmailOutcomes(summary)
	return view
}

func buildOverviewEmailTimelineRow(job OverviewJob, windowStart, windowEnd time.Time) overviewEmailTimelineRow {
	counts := make([]map[string]int, overviewEmailTimelineBucketCount)
	for index := range counts {
		counts[index] = make(map[string]int)
	}
	for _, occurrence := range job.Occurrences {
		startValue := occurrence.ActualStartAt
		if startValue == "" {
			startValue = occurrence.ScheduledAt
		}
		start, err := time.Parse(time.RFC3339, startValue)
		if err != nil {
			continue
		}
		end := start
		if occurrence.DurationMS > 0 {
			end = start.Add(time.Duration(occurrence.DurationMS) * time.Millisecond)
		}
		firstBucket := overviewEmailTimelineBucketIndex(start, windowStart, windowEnd)
		if firstBucket < 0 {
			continue
		}
		lastBucket := firstBucket
		if end.After(start) {
			lastInstant := end.Add(-time.Nanosecond)
			if bucket := overviewEmailTimelineBucketIndex(lastInstant, windowStart, windowEnd); bucket >= 0 {
				lastBucket = bucket
			} else if !lastInstant.Before(windowEnd) {
				lastBucket = overviewEmailTimelineBucketCount - 1
			}
		}
		status := occurrence.Status
		if _, exists := overviewEmailStatusLabels[status]; !exists {
			status = "history_unavailable"
		}
		for bucket := firstBucket; bucket <= lastBucket && bucket < len(counts); bucket++ {
			counts[bucket][status]++
		}
	}

	row := overviewEmailTimelineRow{JobID: job.JobID, Type: map[string]string{"runbook": "RunBook", httpRequestJobType: "HTTP Request"}[job.Type]}
	if row.Type == "" {
		row.Type = "Command"
	}
	for _, bucketCounts := range counts {
		cell := overviewEmailTimelineCell{}
		var titleParts []string
		for _, status := range overviewEmailStatusOrder {
			count := bucketCounts[status]
			if count == 0 {
				continue
			}
			titleParts = append(titleParts, fmt.Sprintf("%s: %d", overviewEmailStatusLabels[status], count))
		}
		segmentCount := len(titleParts)
		if segmentCount > 0 {
			width := 100 / segmentCount
			added := 0
			for _, status := range overviewEmailStatusOrder {
				if bucketCounts[status] == 0 {
					continue
				}
				segmentWidth := width
				added++
				if added == segmentCount {
					segmentWidth = 100 - width*(segmentCount-1)
				}
				cell.Segments = append(cell.Segments, overviewEmailTimelineSegment{Color: overviewEmailStatusColors[status], Width: segmentWidth})
			}
			cell.Title = strings.Join(titleParts, " · ")
		}
		row.Cells = append(row.Cells, cell)
	}
	return row
}

func overviewEmailTimelineBucketIndex(value, windowStart, windowEnd time.Time) int {
	if value.Before(windowStart) || !value.Before(windowEnd) {
		return -1
	}
	bucket := int(value.Sub(windowStart) / overviewEmailTimelineBucketDuration)
	if bucket < 0 || bucket >= overviewEmailTimelineBucketCount {
		return -1
	}
	return bucket
}

func buildOverviewEmailOutcomes(summary OverviewSummary) []overviewEmailOutcome {
	values := []overviewEmailOutcome{
		{Label: "Success", Value: summary.SuccessRuns - summary.LongRuns, Color: overviewEmailStatusColors["success"]},
		{Label: "Long run", Value: summary.LongRuns, Color: overviewEmailStatusColors["long_run"]},
		{Label: "Failed", Value: summary.FailedRuns, Color: overviewEmailStatusColors["failed"]},
		{Label: "Running", Value: summary.RunningRuns, Color: overviewEmailStatusColors["running"]},
		{Label: "Skipped", Value: summary.SkippedRuns, Color: overviewEmailStatusColors["skipped"]},
		{Label: "No record", Value: summary.NoRecordRuns, Color: overviewEmailStatusColors["no_record"]},
		{Label: "Upcoming", Value: summary.UpcomingRuns, Color: overviewEmailStatusColors["upcoming"]},
		{Label: "Disabled", Value: summary.DisabledRuns, Color: overviewEmailStatusColors["disabled"]},
		{Label: "Unavailable", Value: summary.UnavailableRuns, Color: overviewEmailStatusColors["history_unavailable"]},
	}
	filtered := values[:0]
	for _, value := range values {
		if value.Value > 0 {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func formatOverviewEmailTimezone(name string, offsetSeconds int) string {
	sign := "+"
	if offsetSeconds < 0 {
		sign = "-"
		offsetSeconds = -offsetSeconds
	}
	return fmt.Sprintf("%s (UTC%s%02d:%02d)", firstNonEmpty(name, "Server"), sign, offsetSeconds/3600, (offsetSeconds%3600)/60)
}

func formatOverviewEmailDuration(milliseconds int64) string {
	if milliseconds <= 0 {
		return "—"
	}
	duration := time.Duration(milliseconds) * time.Millisecond
	if duration < time.Second {
		return fmt.Sprintf("%dms", milliseconds)
	}
	if duration < time.Minute {
		return fmt.Sprintf("%.1fs", duration.Seconds())
	}
	if duration < time.Hour {
		return fmt.Sprintf("%dm %ds", int(duration.Minutes()), int(duration.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", int(duration.Hours()), int(duration.Minutes())%60)
}

func formatOverviewEmailNumber(value int) string {
	text := fmt.Sprintf("%d", value)
	if len(text) <= 3 {
		return text
	}
	var groups []string
	for len(text) > 3 {
		groups = append(groups, text[len(text)-3:])
		text = text[:len(text)-3]
	}
	groups = append(groups, text)
	for left, right := 0, len(groups)-1; left < right; left, right = left+1, right-1 {
		groups[left], groups[right] = groups[right], groups[left]
	}
	return strings.Join(groups, ",")
}

var scheduledJobsOverviewEmailTemplate = template.Must(template.New("scheduled-jobs-overview-email").Funcs(template.FuncMap{
	"escape": html.EscapeString,
}).Parse(`<!doctype html>
<html><body style="margin:0;padding:0;background:#f4f6f8;color:#202938;font-family:Arial,Helvetica,sans-serif">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="background:#f4f6f8"><tr><td align="center" style="padding:18px 8px">
<table role="presentation" width="920" cellspacing="0" cellpadding="0" border="0" style="width:100%;max-width:920px;background:#ffffff;border:1px solid #dfe4ea;border-radius:10px">
<tr><td style="padding:18px 20px;border-bottom:1px solid #e8ebef"><div style="font-size:22px;font-weight:700;color:#202938">Scheduled Jobs Overview</div><div style="margin-top:5px;font-size:13px;color:#687386">{{escape .WindowLabel}}</div><div style="margin-top:3px;font-size:11px;color:#8a93a0">{{escape .WindowRange}} · {{escape .Timezone}}</div></td></tr>
<tr><td style="padding:14px 14px 5px"><table role="presentation" width="100%" cellspacing="6" cellpadding="0" border="0"><tr>{{range .KPIs}}<td width="25%" valign="top" style="padding:12px;border:1px solid #e3e7ec;border-radius:8px"><div style="font-size:10px;font-weight:700;text-transform:uppercase;color:#687386">{{escape .Label}}</div><div style="margin-top:4px;font-size:22px;font-weight:700;color:{{.Color}}">{{escape .Value}}</div><div style="margin-top:3px;font-size:10px;color:#7a8493">{{escape .Detail}}</div></td>{{end}}</tr></table></td></tr>
<tr><td style="padding:8px 20px 10px"><div style="font-size:14px;font-weight:700">Job timeline</div><div style="margin-top:5px;font-size:10px;color:#687386">{{range $index, $status := .Legend}}{{if $index}} · {{end}}<span style="display:inline-block;width:8px;height:8px;background:{{$status.Color}}"></span> {{escape $status.Label}}{{end}}</div></td></tr>
<tr><td style="padding:0 14px 14px;overflow-x:auto"><table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="table-layout:fixed;border-collapse:collapse;border:1px solid #dfe4ea"><tr><th width="150" align="left" style="padding:7px;background:#fbfcfd;border:1px solid #dfe4ea;font-size:10px">Job</th>{{range .TimelineHeaders}}<th colspan="8" align="left" style="padding:7px 2px;background:#fbfcfd;border:1px solid #dfe4ea;font-size:8px;color:#465163">{{escape .}}</th>{{end}}</tr>{{range .TimelineRows}}<tr><td style="overflow:hidden;padding:7px;border:1px solid #edf0f3;font-size:10px;font-weight:600;white-space:nowrap;text-overflow:ellipsis">{{escape .JobID}}<div style="font-size:8px;font-weight:400;color:#7a8493">{{escape .Type}}</div></td>{{range .Cells}}<td title="{{escape .Title}}" style="height:24px;padding:0;border:1px solid #edf0f3;background:#fff">{{if .Segments}}<table role="presentation" width="100%" height="14" cellspacing="0" cellpadding="0" border="0"><tr>{{range .Segments}}<td width="{{.Width}}%" style="height:14px;background:{{.Color}}"></td>{{end}}</tr></table>{{end}}</td>{{end}}</tr>{{else}}<tr><td colspan="97" style="padding:24px;text-align:center;color:#7a8493;font-size:12px">No scheduled jobs in this window.</td></tr>{{end}}</table></td></tr>
<tr><td style="padding:4px 20px 8px"><div style="font-size:14px;font-weight:700">Expected / observed by time</div></td></tr>
<tr><td style="padding:0 14px 14px"><table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="table-layout:fixed;border-collapse:collapse"><tr>{{range .HourlyGroups}}<td align="center" style="padding:6px 2px;border:1px solid #edf0f3;font-size:8px"><div style="color:#687386">{{escape .Label}}</div><div style="margin-top:3px;font-size:10px"><span style="color:#9badc8">{{.Expected}}</span> / <strong style="color:#2f73c9">{{.Observed}}</strong></div></td>{{end}}</tr></table></td></tr>
<tr><td style="padding:4px 20px 8px"><div style="font-size:14px;font-weight:700">Outcome breakdown</div></td></tr>
<tr><td style="padding:0 20px 16px">{{range .Outcomes}}<span style="display:inline-block;margin:0 5px 5px 0;padding:5px 8px;border-radius:12px;background:#f5f6f8;font-size:10px"><span style="display:inline-block;width:8px;height:8px;background:{{.Color}}"></span> {{escape .Label}}: <strong>{{.Value}}</strong></span>{{else}}<span style="font-size:11px;color:#7a8493">No occurrences.</span>{{end}}</td></tr>
{{if .AccessURL}}<tr><td align="center" style="padding:4px 20px 18px"><a href="{{escape .AccessURL}}" style="display:inline-block;padding:9px 14px;border-radius:6px;background:#2f73c9;color:#ffffff;text-decoration:none;font-size:12px;font-weight:700">Open Scheduled Jobs Overview</a></td></tr>{{end}}
<tr><td style="padding:12px 20px;border-top:1px solid #e8ebef;background:#fbfcfd;color:#7a8493;font-size:9px">{{escape .ProjectionNote}} Generated {{escape .GeneratedAt}}.</td></tr>
</table></td></tr></table></body></html>`))
