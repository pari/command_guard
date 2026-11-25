package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

const overviewNoRecordGrace = 2 * time.Minute

type OverviewTimezone struct {
	Name          string `json:"name"`
	OffsetSeconds int    `json:"offset_seconds"`
}

type OverviewOccurrence struct {
	ID                     string `json:"id"`
	Expected               bool   `json:"expected"`
	UnmatchedActual        bool   `json:"unmatched_actual,omitempty"`
	ScheduledAt            string `json:"scheduled_at"`
	ScheduledOffsetSeconds int    `json:"scheduled_offset_seconds"`
	ActualStartAt          string `json:"actual_start_at,omitempty"`
	ActualOffsetSeconds    *int   `json:"actual_offset_seconds,omitempty"`
	EndedAt                string `json:"ended_at,omitempty"`
	DurationMS             int64  `json:"duration_ms"`
	Status                 string `json:"status"`
	TraceID                string `json:"trace_id,omitempty"`
	RunID                  string `json:"run_id,omitempty"`
}

type OverviewJob struct {
	JobKey       string               `json:"job_key"`
	JobID        string               `json:"job_id"`
	Type         string               `json:"type"`
	Description  string               `json:"description,omitempty"`
	CronSchedule string               `json:"cron_schedule,omitempty"`
	Configured   bool                 `json:"configured"`
	Occurrences  []OverviewOccurrence `json:"occurrences"`
}

type OverviewSummary struct {
	ExpectedRuns      int    `json:"expected_runs"`
	ObservedRuns      int    `json:"observed_runs"`
	SuccessRuns       int    `json:"success_runs"`
	FailedRuns        int    `json:"failed_runs"`
	LongRuns          int    `json:"long_runs"`
	RunningRuns       int    `json:"running_runs"`
	SkippedRuns       int    `json:"skipped_runs"`
	NoRecordRuns      int    `json:"no_record_runs"`
	UpcomingRuns      int    `json:"upcoming_runs"`
	DisabledRuns      int    `json:"disabled_runs"`
	UnavailableRuns   int    `json:"history_unavailable_runs"`
	AverageRuntimeMS  int64  `json:"average_runtime_ms"`
	LongestRuntimeMS  int64  `json:"longest_runtime_ms"`
	LongestRuntimeJob string `json:"longest_runtime_job,omitempty"`
}

type OverviewHourlyBucket struct {
	Hour             int   `json:"hour"`
	Expected         int   `json:"expected"`
	Observed         int   `json:"observed"`
	Success          int   `json:"success"`
	Failed           int   `json:"failed"`
	NoRecord         int   `json:"no_record"`
	Upcoming         int   `json:"upcoming"`
	AverageRuntimeMS int64 `json:"average_runtime_ms"`
	runtimeTotalMS   int64
	runtimeCount     int
}

type ScheduledJobsOverview struct {
	Date                 string                 `json:"date"`
	ServerToday          string                 `json:"server_today"`
	DayStart             string                 `json:"day_start"`
	DayEnd               string                 `json:"day_end"`
	Timezone             OverviewTimezone       `json:"timezone"`
	GeneratedAt          string                 `json:"generated_at"`
	CronEnabled          bool                   `json:"cron_enabled"`
	LogRetentionDays     int                    `json:"log_retention_days"`
	HistoryAvailableFrom string                 `json:"history_available_from"`
	ProjectionBasis      string                 `json:"projection_basis"`
	Jobs                 []OverviewJob          `json:"jobs"`
	Summary              OverviewSummary        `json:"summary"`
	Hourly               []OverviewHourlyBucket `json:"hourly"`
	Warnings             []string               `json:"warnings,omitempty"`
}

type overviewActualRun struct {
	JobKey   string
	JobID    string
	Type     string
	Start    time.Time
	End      time.Time
	Duration time.Duration
	Status   string
	TraceID  string
	RunID    string
}

type overviewJobDefinition struct {
	JobKey       string
	JobID        string
	Type         string
	Description  string
	CronSchedule string
	Configured   bool
}

func overviewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendJSONResponse(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Error: "Method not allowed. Only GET is supported"})
		return
	}

	if err := cache.loadIfChanged(); err != nil {
		sendJSONResponse(w, http.StatusInternalServerError, APIResponse{Success: false, Error: "Failed to load configuration"})
		return
	}

	now := time.Now()
	location := now.Location()
	selected := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	if value := strings.TrimSpace(r.URL.Query().Get("date")); value != "" {
		parsed, err := time.ParseInLocation("2006-01-02", value, location)
		if err != nil {
			sendJSONResponse(w, http.StatusBadRequest, APIResponse{Success: false, Error: "date must use YYYY-MM-DD format"})
			return
		}
		selected = parsed
	}

	cfg := cache.GetConfig()
	if cfg == nil {
		sendJSONResponse(w, http.StatusInternalServerError, APIResponse{Success: false, Error: "Configuration is not loaded"})
		return
	}

	overview, err := buildScheduledJobsOverview(cfg, selected, now, "logs")
	if err != nil {
		sendJSONResponse(w, http.StatusInternalServerError, APIResponse{Success: false, Error: "Failed to build scheduled jobs overview"})
		return
	}
	if claims, ok := ClaimsFromRequest(r); ok && claims.Role == RoleUser {
		filtered := make([]OverviewJob, 0, len(overview.Jobs))
		for _, job := range overview.Jobs {
			allowed := (job.Type == "command" && authManager.userHasCommand(claims.Username, job.JobID)) || (job.Type == "runbook" && authManager.userHasRunBook(claims.Username, job.JobID)) || (job.Type == httpRequestJobType && authManager.userHasHTTPRequest(claims.Username, job.JobID))
			if allowed {
				filtered = append(filtered, job)
			}
		}
		overview.Jobs = filtered
		overview.Summary = OverviewSummary{}
		overview.Hourly = make([]OverviewHourlyBucket, 24)
		for hour := range overview.Hourly {
			overview.Hourly[hour].Hour = hour
		}
		calculateOverviewAggregates(overview, selected, selected.AddDate(0, 0, 1))
	}

	sendJSONResponse(w, http.StatusOK, APIResponse{Success: true, Data: overview})
}

func buildScheduledJobsOverview(cfg *Config, selectedDay, now time.Time, logsDir string) (*ScheduledJobsOverview, error) {
	location := now.Location()
	dayStart := time.Date(selectedDay.Year(), selectedDay.Month(), selectedDay.Day(), 0, 0, 0, 0, location)
	dayEnd := dayStart.AddDate(0, 0, 1)
	return buildScheduledJobsOverviewRange(cfg, dayStart, dayEnd, now, logsDir)
}

func buildScheduledJobsOverviewRange(cfg *Config, windowStart, windowEnd, now time.Time, logsDir string) (*ScheduledJobsOverview, error) {
	location := now.Location()
	windowStart = windowStart.In(location)
	windowEnd = windowEnd.In(location)
	if !windowEnd.After(windowStart) {
		return nil, fmt.Errorf("overview window end must be after its start")
	}
	serverToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	retentionDays := cfg.LogRetentionDays
	if retentionDays <= 0 {
		retentionDays = 3
	}
	retentionCutoff := now.AddDate(0, 0, -retentionDays)
	zoneName, zoneOffset := windowStart.Zone()
	cronEnabled := cfg.CronEnabled != "N"

	definitions := make([]overviewJobDefinition, 0)
	definitionByKey := make(map[string]overviewJobDefinition)
	for _, command := range cfg.Commands {
		if strings.TrimSpace(command.CronSchedule) == "" {
			continue
		}
		definition := overviewJobDefinition{
			JobKey:       "command:" + command.CmdID,
			JobID:        command.CmdID,
			Type:         "command",
			Description:  command.Description,
			CronSchedule: command.CronSchedule,
			Configured:   true,
		}
		definitions = append(definitions, definition)
		definitionByKey[definition.JobKey] = definition
	}
	for _, runBook := range cfg.RunBooks {
		if strings.TrimSpace(runBook.CronSchedule) == "" {
			continue
		}
		definition := overviewJobDefinition{
			JobKey:       "runbook:" + runBook.RunBookID,
			JobID:        runBook.RunBookID,
			Type:         "runbook",
			Description:  runBook.Description,
			CronSchedule: runBook.CronSchedule,
			Configured:   true,
		}
		definitions = append(definitions, definition)
		definitionByKey[definition.JobKey] = definition
	}
	for _, req := range cfg.HTTPRequests {
		if strings.TrimSpace(req.CronSchedule) == "" {
			continue
		}
		definition := overviewJobDefinition{
			JobKey:       "http:" + req.RequestID,
			JobID:        req.RequestID,
			Type:         httpRequestJobType,
			Description:  req.Description,
			CronSchedule: req.CronSchedule,
			Configured:   true,
		}
		definitions = append(definitions, definition)
		definitionByKey[definition.JobKey] = definition
	}

	actualRuns, err := loadOverviewActualRuns(logsDir, windowStart, windowEnd, location, now)
	if err != nil {
		return nil, err
	}
	actualRuns = append(actualRuns, loadOverviewRunBookRuns(windowStart, windowEnd, now)...)
	sort.Slice(actualRuns, func(i, j int) bool { return actualRuns[i].Start.Before(actualRuns[j].Start) })

	actualByMinute := make(map[string]map[string][]*overviewActualRun)
	for _, actual := range actualRuns {
		if actualByMinute[actual.JobKey] == nil {
			actualByMinute[actual.JobKey] = make(map[string][]*overviewActualRun)
		}
		minuteKey := actual.Start.In(location).Format("2006-01-02 15:04")
		actualByMinute[actual.JobKey][minuteKey] = append(actualByMinute[actual.JobKey][minuteKey], actual)
	}

	usedActual := make(map[*overviewActualRun]bool)
	jobs := make([]OverviewJob, 0, len(definitions))
	warnings := make([]string, 0)
	for _, definition := range definitions {
		if actualByMinute[definition.JobKey] == nil {
			actualByMinute[definition.JobKey] = make(map[string][]*overviewActualRun)
		}
		job := OverviewJob{
			JobKey:       definition.JobKey,
			JobID:        definition.JobID,
			Type:         definition.Type,
			Description:  definition.Description,
			CronSchedule: definition.CronSchedule,
			Configured:   true,
			Occurrences:  make([]OverviewOccurrence, 0),
		}

		schedule, parseErr := overviewCronParser().Parse(definition.CronSchedule)
		if parseErr != nil {
			warnings = append(warnings, fmt.Sprintf("%s '%s' has an invalid cron schedule", definition.Type, definition.JobID))
			jobs = append(jobs, job)
			continue
		}

		for scheduledAt := schedule.Next(windowStart.Add(-time.Nanosecond)); scheduledAt.Before(windowEnd); scheduledAt = schedule.Next(scheduledAt) {
			if scheduledAt.Before(windowStart) {
				continue
			}
			minuteKey := scheduledAt.In(location).Format("2006-01-02 15:04")
			var actual *overviewActualRun
			matches := actualByMinute[definition.JobKey][minuteKey]
			for len(matches) > 0 {
				candidate := matches[0]
				matches = matches[1:]
				if !usedActual[candidate] {
					actual = candidate
					break
				}
			}
			actualByMinute[definition.JobKey][minuteKey] = matches

			occurrence := newOverviewOccurrence(definition.JobKey, scheduledAt, windowStart, actual, true, false, cronEnabled, retentionCutoff, now)
			if actual != nil {
				usedActual[actual] = true
			}
			job.Occurrences = append(job.Occurrences, occurrence)
		}
		jobs = append(jobs, job)
	}

	jobIndex := make(map[string]int)
	for index := range jobs {
		jobIndex[jobs[index].JobKey] = index
	}
	actualOnlyDefinitions := make(map[string]overviewJobDefinition)
	for _, actual := range actualRuns {
		if usedActual[actual] {
			continue
		}
		index, exists := jobIndex[actual.JobKey]
		if !exists {
			definition, configured := definitionByKey[actual.JobKey]
			if !configured {
				definition = overviewJobDefinition{JobKey: actual.JobKey, JobID: actual.JobID, Type: actual.Type, Configured: false}
			}
			actualOnlyDefinitions[actual.JobKey] = definition
			continue
		}
		occurrence := newOverviewOccurrence(actual.JobKey, actual.Start, windowStart, actual, false, true, cronEnabled, retentionCutoff, now)
		jobs[index].Occurrences = append(jobs[index].Occurrences, occurrence)
		usedActual[actual] = true
	}

	actualOnlyKeys := make([]string, 0, len(actualOnlyDefinitions))
	for key := range actualOnlyDefinitions {
		actualOnlyKeys = append(actualOnlyKeys, key)
	}
	sort.Strings(actualOnlyKeys)
	for _, key := range actualOnlyKeys {
		definition := actualOnlyDefinitions[key]
		job := OverviewJob{
			JobKey:       definition.JobKey,
			JobID:        definition.JobID,
			Type:         definition.Type,
			Description:  definition.Description,
			CronSchedule: definition.CronSchedule,
			Configured:   definition.Configured,
			Occurrences:  make([]OverviewOccurrence, 0),
		}
		for _, actual := range actualRuns {
			if actual.JobKey != key || usedActual[actual] {
				continue
			}
			job.Occurrences = append(job.Occurrences, newOverviewOccurrence(key, actual.Start, windowStart, actual, false, true, cronEnabled, retentionCutoff, now))
			usedActual[actual] = true
		}
		jobIndex[key] = len(jobs)
		jobs = append(jobs, job)
	}

	for index := range jobs {
		sort.Slice(jobs[index].Occurrences, func(i, j int) bool {
			return jobs[index].Occurrences[i].ScheduledAt < jobs[index].Occurrences[j].ScheduledAt
		})
		classifyOverviewLongRuns(&jobs[index])
	}

	overview := &ScheduledJobsOverview{
		Date:                 windowStart.Format("2006-01-02"),
		ServerToday:          serverToday.Format("2006-01-02"),
		DayStart:             windowStart.Format(time.RFC3339),
		DayEnd:               windowEnd.Format(time.RFC3339),
		Timezone:             OverviewTimezone{Name: zoneName, OffsetSeconds: zoneOffset},
		GeneratedAt:          now.Format(time.RFC3339),
		CronEnabled:          cronEnabled,
		LogRetentionDays:     retentionDays,
		HistoryAvailableFrom: retentionCutoff.Format(time.RFC3339),
		ProjectionBasis:      "current_config",
		Jobs:                 jobs,
		Hourly:               make([]OverviewHourlyBucket, 24),
		Warnings:             warnings,
	}
	for hour := range overview.Hourly {
		overview.Hourly[hour].Hour = windowStart.Add(time.Duration(hour) * time.Hour).In(location).Hour()
	}
	calculateOverviewAggregates(overview, windowStart, windowEnd)
	return overview, nil
}

func overviewCronParser() cron.Parser {
	return cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
}

func newOverviewOccurrence(jobKey string, scheduledAt, dayStart time.Time, actual *overviewActualRun, expected, unmatched, cronEnabled bool, retentionCutoff, now time.Time) OverviewOccurrence {
	status := "upcoming"
	if scheduledAt.Before(retentionCutoff) {
		status = "history_unavailable"
	} else if !cronEnabled && scheduledAt.After(now.Add(-overviewNoRecordGrace)) {
		status = "disabled"
	} else if scheduledAt.Before(now.Add(-overviewNoRecordGrace)) {
		status = "no_record"
	}

	occurrence := OverviewOccurrence{
		ID:                     fmt.Sprintf("%s:%d", jobKey, scheduledAt.UnixNano()),
		Expected:               expected,
		UnmatchedActual:        unmatched,
		ScheduledAt:            scheduledAt.Format(time.RFC3339),
		ScheduledOffsetSeconds: int(scheduledAt.Sub(dayStart).Seconds()),
		Status:                 status,
	}
	if actual == nil {
		return occurrence
	}

	actualOffset := int(actual.Start.Sub(dayStart).Seconds())
	occurrence.ActualStartAt = actual.Start.Format(time.RFC3339)
	occurrence.ActualOffsetSeconds = &actualOffset
	occurrence.DurationMS = actual.Duration.Milliseconds()
	occurrence.Status = actual.Status
	occurrence.TraceID = actual.TraceID
	occurrence.RunID = actual.RunID
	occurrence.ID = fmt.Sprintf("%s:%s", jobKey, firstNonEmpty(actual.TraceID, actual.RunID, strconv.FormatInt(actual.Start.UnixNano(), 10)))
	if !actual.End.IsZero() {
		occurrence.EndedAt = actual.End.Format(time.RFC3339)
	}
	return occurrence
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func classifyOverviewLongRuns(job *OverviewJob) {
	var total int64
	count := 0
	for _, occurrence := range job.Occurrences {
		if occurrence.Status == "success" && occurrence.DurationMS > 0 {
			total += occurrence.DurationMS
			count++
		}
	}
	if count < 3 {
		return
	}
	average := total / int64(count)
	for index := range job.Occurrences {
		occurrence := &job.Occurrences[index]
		if occurrence.Status == "success" && occurrence.DurationMS > average*2 {
			occurrence.Status = "long_run"
		}
	}
}

func calculateOverviewAggregates(overview *ScheduledJobsOverview, windowStart, windowEnd time.Time) {
	var runtimeTotal int64
	runtimeCount := 0
	for _, job := range overview.Jobs {
		for _, occurrence := range job.Occurrences {
			if occurrence.Expected {
				overview.Summary.ExpectedRuns++
				if scheduledAt, err := time.Parse(time.RFC3339, occurrence.ScheduledAt); err == nil {
					if bucket := overviewHourlyBucketIndex(scheduledAt, windowStart, windowEnd); bucket >= 0 {
						overview.Hourly[bucket].Expected++
					}
				}
			}

			observed := occurrence.ActualStartAt != ""
			observedHour := -1
			if observed {
				overview.Summary.ObservedRuns++
				if actualAt, err := time.Parse(time.RFC3339, occurrence.ActualStartAt); err == nil {
					observedHour = overviewHourlyBucketIndex(actualAt, windowStart, windowEnd)
					if observedHour >= 0 {
						overview.Hourly[observedHour].Observed++
					}
				}
			}

			switch occurrence.Status {
			case "success":
				overview.Summary.SuccessRuns++
				if observedHour >= 0 {
					overview.Hourly[observedHour].Success++
				}
			case "long_run":
				overview.Summary.SuccessRuns++
				overview.Summary.LongRuns++
				if observedHour >= 0 {
					overview.Hourly[observedHour].Success++
				}
			case "failed":
				overview.Summary.FailedRuns++
				if observedHour >= 0 {
					overview.Hourly[observedHour].Failed++
				}
			case "running":
				overview.Summary.RunningRuns++
			case "skipped":
				overview.Summary.SkippedRuns++
			case "no_record":
				overview.Summary.NoRecordRuns++
				if scheduledAt, err := time.Parse(time.RFC3339, occurrence.ScheduledAt); err == nil {
					if bucket := overviewHourlyBucketIndex(scheduledAt, windowStart, windowEnd); bucket >= 0 {
						overview.Hourly[bucket].NoRecord++
					}
				}
			case "upcoming":
				overview.Summary.UpcomingRuns++
				if scheduledAt, err := time.Parse(time.RFC3339, occurrence.ScheduledAt); err == nil {
					if bucket := overviewHourlyBucketIndex(scheduledAt, windowStart, windowEnd); bucket >= 0 {
						overview.Hourly[bucket].Upcoming++
					}
				}
			case "disabled":
				overview.Summary.DisabledRuns++
			case "history_unavailable":
				overview.Summary.UnavailableRuns++
			}

			if observed && occurrence.DurationMS > 0 && occurrence.Status != "running" && occurrence.Status != "skipped" {
				runtimeTotal += occurrence.DurationMS
				runtimeCount++
				if observedHour >= 0 {
					overview.Hourly[observedHour].runtimeTotalMS += occurrence.DurationMS
					overview.Hourly[observedHour].runtimeCount++
				}
				if occurrence.DurationMS > overview.Summary.LongestRuntimeMS {
					overview.Summary.LongestRuntimeMS = occurrence.DurationMS
					overview.Summary.LongestRuntimeJob = job.JobID
				}
			}
		}
	}
	if runtimeCount > 0 {
		overview.Summary.AverageRuntimeMS = runtimeTotal / int64(runtimeCount)
	}
	for index := range overview.Hourly {
		bucket := &overview.Hourly[index]
		if bucket.runtimeCount > 0 {
			bucket.AverageRuntimeMS = bucket.runtimeTotalMS / int64(bucket.runtimeCount)
		}
	}
}

func overviewHourlyBucketIndex(value, windowStart, windowEnd time.Time) int {
	if value.Before(windowStart) || !value.Before(windowEnd) {
		return -1
	}
	bucket := int(value.Sub(windowStart) / time.Hour)
	if bucket < 0 || bucket >= 24 {
		return -1
	}
	return bucket
}

func loadOverviewActualRuns(logsDir string, dayStart, dayEnd time.Time, location *time.Location, now time.Time) ([]*overviewActualRun, error) {
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*overviewActualRun{}, nil
		}
		return nil, err
	}

	prefixes := overviewLogDatePrefixes(dayStart, dayEnd, location)
	runs := make([]*overviewActualRun, 0)
	for _, entry := range entries {
		if entry.IsDir() || !hasOverviewLogDatePrefix(entry.Name(), prefixes) || !strings.HasSuffix(entry.Name(), "_log.txt") {
			continue
		}
		fields, err := readOverviewLogHeader(filepath.Join(logsDir, entry.Name()))
		if err != nil || fields["runbook_id"] != "" || fields["job_type"] == diskCheckJobType {
			continue
		}
		if fields["remote_ip"] != "cron-scheduler" && fields["agent"] != "cron-scheduler" {
			continue
		}
		startedAt, err := parseOverviewLogTime(fields["request_received_at"], location)
		if err != nil || startedAt.Before(dayStart) || !startedAt.Before(dayEnd) {
			continue
		}
		cmdID := fields["cmd_id"]
		if cmdID == "" {
			continue
		}

		duration, _ := time.ParseDuration(fields["exec_time"])
		status := "failed"
		switch fields["exitcode"] {
		case "0":
			status = "success"
		case "-1":
			status = "running"
			duration = now.Sub(startedAt)
			if duration < 0 {
				duration = 0
			}
		}
		endedAt := startedAt.Add(duration)
		if status == "running" {
			endedAt = time.Time{}
		}
		jobKey, jobType := "command:"+cmdID, "command"
		if fields["job_type"] == httpRequestJobType {
			jobKey, jobType = "http:"+cmdID, httpRequestJobType
		}
		runs = append(runs, &overviewActualRun{
			JobKey:   jobKey,
			JobID:    cmdID,
			Type:     jobType,
			Start:    startedAt,
			End:      endedAt,
			Duration: duration,
			Status:   status,
			TraceID:  fields["trace_id"],
		})
	}
	return runs, nil
}

func overviewLogDatePrefixes(windowStart, windowEnd time.Time, location *time.Location) map[string]struct{} {
	prefixes := make(map[string]struct{})
	firstDay := time.Date(windowStart.In(location).Year(), windowStart.In(location).Month(), windowStart.In(location).Day(), 0, 0, 0, 0, location)
	lastInstant := windowEnd.Add(-time.Nanosecond).In(location)
	lastDay := time.Date(lastInstant.Year(), lastInstant.Month(), lastInstant.Day(), 0, 0, 0, 0, location)
	for day := firstDay; !day.After(lastDay); day = day.AddDate(0, 0, 1) {
		prefixes[day.Format("2006_01_02")+"_"] = struct{}{}
	}
	return prefixes
}

func hasOverviewLogDatePrefix(name string, prefixes map[string]struct{}) bool {
	if len(name) < len("2006_01_02_") {
		return false
	}
	_, exists := prefixes[name[:len("2006_01_02_")]]
	return exists
}

func readOverviewLogHeader(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	fields := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if key == "output" {
			break
		}
		if !isKnownLogFieldKey(key) {
			continue
		}
		if _, exists := fields[key]; exists && !shouldUseLastLogFieldValue(key) {
			continue
		}
		fields[key] = strings.TrimSpace(parts[1])
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return fields, nil
}

func parseOverviewLogTime(value string, location *time.Location) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05.000", "2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		var parsed time.Time
		var err error
		if layout == time.RFC3339Nano || layout == time.RFC3339 {
			parsed, err = time.Parse(layout, value)
		} else {
			parsed, err = time.ParseInLocation(layout, value, location)
		}
		if err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid log timestamp %q", value)
}

func loadOverviewRunBookRuns(dayStart, dayEnd, now time.Time) []*overviewActualRun {
	runBookManager.RLock()
	runs := make([]RunBookRun, 0, len(runBookManager.runs))
	for _, run := range runBookManager.runs {
		if run != nil {
			runs = append(runs, *run)
		}
	}
	runBookManager.RUnlock()

	result := make([]*overviewActualRun, 0)
	for _, run := range runs {
		if (run.Trigger != "cron" && run.Trigger != "schedule") || run.StartedAt.Before(dayStart) || !run.StartedAt.Before(dayEnd) {
			continue
		}
		status := "failed"
		switch run.Status {
		case "succeeded":
			status = "success"
		case "queued", "running", "stopping":
			status = "running"
		case "skipped":
			status = "skipped"
		}
		duration, _ := time.ParseDuration(run.Duration)
		if status == "running" {
			duration = now.Sub(run.StartedAt)
			if duration < 0 {
				duration = 0
			}
		}
		endedAt := time.Time{}
		if run.EndedAt != nil {
			endedAt = *run.EndedAt
		} else if status != "running" {
			endedAt = run.StartedAt.Add(duration)
		}
		result = append(result, &overviewActualRun{
			JobKey:   "runbook:" + run.RunBookID,
			JobID:    run.RunBookID,
			Type:     "runbook",
			Start:    run.StartedAt,
			End:      endedAt,
			Duration: duration,
			Status:   status,
			RunID:    run.RunID,
		})
	}
	return result
}
