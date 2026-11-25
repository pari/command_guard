package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/gomail.v2"
)

type RunBook struct {
	RunBookID      string                    `json:"runbook_id"`
	Description    string                    `json:"description,omitempty"`
	Steps          []RunBookStep             `json:"steps"`
	CronSchedule   string                    `json:"cron_schedule,omitempty"`
	TimeoutSeconds int                       `json:"timeout_seconds,omitempty"`
	SummaryFormat  string                    `json:"summary_format,omitempty"`
	BearerToken    string                    `json:"bearer_token,omitempty"`
	NotifyWebhook  string                    `json:"notify_webhook,omitempty"`
	NotifyEmail    *RunBookEmailNotification `json:"notify_email,omitempty"`
}

type RunBookStep struct {
	StepID             string `json:"step_id"`
	CmdID              string `json:"cmd_id,omitempty"`
	RequestID          string `json:"request_id,omitempty"` // HTTP request step (exactly one of cmd_id / request_id)
	Label              string `json:"label,omitempty"`
	OnFailure          string `json:"on_failure,omitempty"`
	DelayBeforeSeconds int    `json:"delay_before_seconds,omitempty"`
	RetryCount         int    `json:"retry_count,omitempty"`
	RetryDelaySeconds  int    `json:"retry_delay_seconds,omitempty"`
	TimeoutSeconds     int    `json:"timeout_seconds,omitempty"`
}

type RunBookEmailNotification struct {
	To             []string `json:"to"`
	CC             []string `json:"cc,omitempty"`
	Subject        string   `json:"subject,omitempty"`
	OnSuccess      *bool    `json:"on_success,omitempty"`
	OnFailure      *bool    `json:"on_failure,omitempty"`
	IncludeOutput  *bool    `json:"include_output,omitempty"`
	MaxOutputLines int      `json:"max_output_lines,omitempty"`
}

type RunBookAttemptResult struct {
	Attempt         int       `json:"attempt"`
	TraceID         string    `json:"trace_id,omitempty"`
	Status          string    `json:"status"`
	StartedAt       time.Time `json:"started_at"`
	EndedAt         time.Time `json:"ended_at"`
	Duration        string    `json:"duration"`
	Output          string    `json:"output,omitempty"`
	Error           string    `json:"error,omitempty"`
	OutputTruncated bool      `json:"output_truncated,omitempty"`
}

type RunBookStepResult struct {
	StepID    string                 `json:"step_id"`
	CmdID     string                 `json:"cmd_id,omitempty"`
	RequestID string                 `json:"request_id,omitempty"`
	Label     string                 `json:"label,omitempty"`
	Status    string                 `json:"status"`
	StartedAt *time.Time             `json:"started_at,omitempty"`
	EndedAt   *time.Time             `json:"ended_at,omitempty"`
	Duration  string                 `json:"duration,omitempty"`
	Attempts  []RunBookAttemptResult `json:"attempts,omitempty"`
	TraceID   string                 `json:"trace_id,omitempty"`
	Output    string                 `json:"output,omitempty"`
	Error     string                 `json:"error,omitempty"`
}

type RunBookRun struct {
	RunID         string              `json:"run_id"`
	RunBookID     string              `json:"runbook_id"`
	Description   string              `json:"description,omitempty"`
	Status        string              `json:"status"`
	Trigger       string              `json:"trigger"`
	InvokedBy     string              `json:"invoked_by"`
	StartedAt     time.Time           `json:"started_at"`
	EndedAt       *time.Time          `json:"ended_at,omitempty"`
	Duration      string              `json:"duration,omitempty"`
	CurrentStep   int                 `json:"current_step"`
	TotalSteps    int                 `json:"total_steps"`
	SuccessCount  int                 `json:"success_count"`
	FailureCount  int                 `json:"failure_count"`
	SkippedCount  int                 `json:"skipped_count"`
	StopRequested bool                `json:"stop_requested,omitempty"`
	Error         string              `json:"error,omitempty"`
	Steps         []RunBookStepResult `json:"steps"`
}

type RunBookManager struct {
	sync.RWMutex
	runs   map[string]*RunBookRun
	active map[string]string
	stops  map[string]chan struct{}
}

type runBookSnapshot struct {
	runBook  RunBook
	commands map[string]*CommandConfig
	requests map[string]*HTTPRequest
}

// stepTarget names what a step runs: its Command ID or its HTTP request ID.
func (s RunBookStep) stepTarget() string {
	if s.RequestID != "" {
		return s.RequestID
	}
	return s.CmdID
}

var runBookManager = newRunBookManager()

func newRunBookManager() *RunBookManager {
	m := &RunBookManager{
		runs:   make(map[string]*RunBookRun),
		active: make(map[string]string),
		stops:  make(map[string]chan struct{}),
	}
	m.loadHistory()
	return m
}

func runBookHistoryDir() string { return filepath.Join("logs", "runbooks") }

func (m *RunBookManager) loadHistory() {
	_ = os.MkdirAll(runBookHistoryDir(), privateDirMode)
	entries, err := ioutil.ReadDir(runBookHistoryDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := ioutil.ReadFile(filepath.Join(runBookHistoryDir(), entry.Name()))
		if err != nil {
			continue
		}
		var run RunBookRun
		if json.Unmarshal(data, &run) != nil || run.RunID == "" {
			continue
		}
		if run.Status == "queued" || run.Status == "running" || run.Status == "stopping" {
			now := time.Now()
			run.Status = "interrupted"
			run.EndedAt = &now
			run.Duration = now.Sub(run.StartedAt).Round(time.Millisecond).String()
			run.Error = "Server restarted before the RunBook completed"
		}
		m.runs[run.RunID] = &run
		m.persistLocked(&run)
	}
}

func (m *RunBookManager) persistLocked(run *RunBookRun) {
	_ = os.MkdirAll(runBookHistoryDir(), privateDirMode)
	data, err := json.MarshalIndent(run, "", "\t")
	if err != nil {
		return
	}
	target := filepath.Join(runBookHistoryDir(), run.RunID+".json")
	tmp := target + ".tmp"
	if ioutil.WriteFile(tmp, append(data, '\n'), privateFileMode) == nil {
		_ = os.Rename(tmp, target)
	}
}

func cloneRunBook(value RunBook) RunBook {
	data, _ := json.Marshal(value)
	var clone RunBook
	_ = json.Unmarshal(data, &clone)
	return clone
}

func findRunBook(runBookID string) (*RunBook, error) {
	if err := cache.loadIfChanged(); err != nil {
		return nil, err
	}
	cache.RLock()
	defer cache.RUnlock()
	if cache.config == nil {
		return nil, fmt.Errorf("configuration not loaded")
	}
	for _, runBook := range cache.config.RunBooks {
		if runBook.RunBookID == runBookID {
			copy := cloneRunBook(runBook)
			return &copy, nil
		}
	}
	return nil, fmt.Errorf("RunBook '%s' not found", runBookID)
}

func buildRunBookSnapshot(runBook *RunBook) (*runBookSnapshot, error) {
	snapshot := &runBookSnapshot{runBook: cloneRunBook(*runBook), commands: make(map[string]*CommandConfig), requests: make(map[string]*HTTPRequest)}
	for _, step := range runBook.Steps {
		if step.RequestID != "" {
			if _, exists := snapshot.requests[step.RequestID]; exists {
				continue
			}
			req, err := findHTTPRequest(step.RequestID)
			if err != nil {
				return nil, err
			}
			snapshot.requests[step.RequestID] = req
			continue
		}
		if _, exists := snapshot.commands[step.CmdID]; exists {
			continue
		}
		cmd, err := cache.findCommandConfig(step.CmdID)
		if err != nil {
			return nil, err
		}
		copy := *cmd
		snapshot.commands[step.CmdID] = &copy
	}
	return snapshot, nil
}

func (m *RunBookManager) Start(runBookID, trigger, remoteIP, agent string) (*RunBookRun, error) {
	invokedBy := "api-token"
	if trigger == "cron" || trigger == "schedule" {
		invokedBy = "scheduler"
	}
	return m.StartAs(runBookID, trigger, remoteIP, agent, invokedBy)
}

func (m *RunBookManager) StartAs(runBookID, trigger, remoteIP, agent, invokedBy string) (*RunBookRun, error) {
	if err := configRestoreGate.BeginWork(); err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			configRestoreGate.EndWork()
		}
	}()

	runBook, err := findRunBook(runBookID)
	if err != nil {
		return nil, err
	}
	snapshot, err := buildRunBookSnapshot(runBook)
	if err != nil {
		return nil, err
	}

	m.Lock()
	if activeID := m.active[runBookID]; activeID != "" {
		m.Unlock()
		return nil, fmt.Errorf("RunBook '%s' is already running", runBookID)
	}
	runID := generateTraceID()
	run := &RunBookRun{
		RunID: runID, RunBookID: runBookID, Description: runBook.Description,
		Status: "queued", Trigger: trigger, InvokedBy: invokedBy, StartedAt: time.Now(), TotalSteps: len(runBook.Steps),
		Steps: make([]RunBookStepResult, len(runBook.Steps)),
	}
	for i, step := range runBook.Steps {
		run.Steps[i] = RunBookStepResult{StepID: step.StepID, CmdID: step.CmdID, RequestID: step.RequestID, Label: step.Label, Status: "pending"}
	}
	stop := make(chan struct{})
	m.runs[runID] = run
	m.active[runBookID] = runID
	m.stops[runID] = stop
	m.persistLocked(run)
	copy := cloneRunBookRun(run)
	m.Unlock()

	go m.execute(snapshot, runID, stop, remoteIP, agent, invokedBy)
	started = true
	return &copy, nil
}

func (m *RunBookManager) RecordSkipped(runBook RunBook, trigger, reason string) {
	now := time.Now()
	invokedBy := "api-token"
	if trigger == "cron" || trigger == "schedule" {
		invokedBy = "scheduler"
	}
	run := &RunBookRun{RunID: generateTraceID(), RunBookID: runBook.RunBookID, Description: runBook.Description, Status: "skipped", Trigger: trigger, InvokedBy: invokedBy, StartedAt: now, EndedAt: &now, Duration: "0s", TotalSteps: len(runBook.Steps), SkippedCount: len(runBook.Steps), Error: reason, Steps: make([]RunBookStepResult, len(runBook.Steps))}
	for i, step := range runBook.Steps {
		run.Steps[i] = RunBookStepResult{StepID: step.StepID, CmdID: step.CmdID, RequestID: step.RequestID, Label: step.Label, Status: "skipped"}
	}
	m.Lock()
	m.runs[run.RunID] = run
	m.persistLocked(run)
	m.Unlock()
}

func (m *RunBookManager) Get(runID string) (*RunBookRun, bool) {
	m.RLock()
	defer m.RUnlock()
	run, ok := m.runs[runID]
	if !ok {
		return nil, false
	}
	copy := cloneRunBookRun(run)
	return &copy, true
}

// cloneRunBookRun returns an independent copy of run, including its Steps and
// their nested attempts. Callers encode the result after releasing the manager
// lock while the executor keeps updating the original. The caller holds the lock.
func cloneRunBookRun(run *RunBookRun) RunBookRun {
	var copy RunBookRun
	data, err := json.Marshal(run)
	if err == nil {
		err = json.Unmarshal(data, &copy)
	}
	if err != nil {
		copy = *run
		copy.Steps = append([]RunBookStepResult(nil), run.Steps...)
	}
	return copy
}

func (m *RunBookManager) List(runBookID string) []RunBookRun {
	m.RLock()
	defer m.RUnlock()
	result := make([]RunBookRun, 0)
	for _, run := range m.runs {
		if run.RunBookID == runBookID {
			result = append(result, cloneRunBookRun(run))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt.After(result[j].StartedAt) })
	return result
}

func (m *RunBookManager) IsActive(runBookID string) bool {
	m.RLock()
	defer m.RUnlock()
	return m.active[runBookID] != ""
}

func (m *RunBookManager) ActiveRuns() []RunBookRun {
	m.RLock()
	defer m.RUnlock()
	result := make([]RunBookRun, 0, len(m.active))
	for _, runID := range m.active {
		if run := m.runs[runID]; run != nil {
			data, _ := json.Marshal(run)
			var copy RunBookRun
			_ = json.Unmarshal(data, &copy)
			result = append(result, copy)
		}
	}
	return result
}

func (m *RunBookManager) PruneBefore(cutoff time.Time) int {
	m.Lock()
	defer m.Unlock()
	deleted := 0
	for runID, run := range m.runs {
		if run.StartedAt.Before(cutoff) && m.active[run.RunBookID] != runID {
			delete(m.runs, runID)
			_ = os.Remove(filepath.Join(runBookHistoryDir(), runID+".json"))
			deleted++
		}
	}
	return deleted
}

func (m *RunBookManager) RequestStop(runID string) bool {
	m.Lock()
	defer m.Unlock()
	run, ok := m.runs[runID]
	if !ok || (run.Status != "running" && run.Status != "queued") {
		return false
	}
	if !run.StopRequested {
		run.StopRequested = true
		run.Status = "stopping"
		close(m.stops[runID])
		m.persistLocked(run)
	}
	return true
}

func boolDefaultTrue(value *bool) bool { return value == nil || *value }

func (m *RunBookManager) mutate(runID string, fn func(*RunBookRun)) {
	m.Lock()
	defer m.Unlock()
	if run := m.runs[runID]; run != nil {
		fn(run)
		m.persistLocked(run)
	}
}

// describeTimeout explains a timeout in plain language: what was stopped, after how
// long, which limit was hit and where to change it. kind is "step", "runbook", "http"
// or "command" (the Command's own timeout_seconds from the Edit Command dialog).
func describeTimeout(kind string, limitSeconds int, elapsed time.Duration) string {
	elapsedText := elapsed.Round(time.Second).String()
	limitText := (time.Duration(limitSeconds) * time.Second).String()
	switch kind {
	case "command":
		return fmt.Sprintf("Timed out: the command was stopped after %s because it reached its time limit of %d seconds (%s). Raise \"Timeout (seconds)\" in the Edit Command dialog, or make the command finish faster.", elapsedText, limitSeconds, limitText)
	case "runbook":
		return fmt.Sprintf("Timed out: this step was stopped after %s because the whole RunBook reached its overall time limit of %d seconds (%s). Raise \"Overall timeout\" in the RunBook editor, or make the steps finish faster.", elapsedText, limitSeconds, limitText)
	case "http":
		return fmt.Sprintf("Timed out: no complete response within %d seconds (%s). Raise \"Timeout (seconds)\" on the HTTP request, or check that the server is reachable and responding.", limitSeconds, limitText)
	case "cancelled":
		return fmt.Sprintf("Timed out: this run was stopped after %s because the time limit for this run was reached.", elapsedText)
	default:
		return fmt.Sprintf("Timed out: this step was stopped after %s because it reached its time limit of %d seconds (%s). Raise \"Timeout\" for this step in the RunBook editor, or make the command finish faster.", elapsedText, limitSeconds, limitText)
	}
}

// runBookTimeoutMessage is the whole-RunBook timeout explanation shown on the run.
func runBookTimeoutMessage(limitSeconds int) string {
	return fmt.Sprintf("Timed out: the RunBook was stopped because it reached its overall time limit of %d seconds (%s). Raise \"Overall timeout\" in the RunBook editor.", limitSeconds, (time.Duration(limitSeconds) * time.Second).String())
}

// runBookStepLimits tells an attempt which limits apply so a timeout can be explained.
type runBookStepLimits struct {
	runBookCtx            context.Context
	runBookTimeoutSeconds int
	step                  RunBookStep
}

// timeoutMessage picks the step or the RunBook wording depending on which limit fired.
func (l runBookStepLimits) timeoutMessage(elapsed time.Duration) string {
	if l.runBookCtx != nil && l.runBookCtx.Err() != nil && l.runBookTimeoutSeconds > 0 {
		return describeTimeout("runbook", l.runBookTimeoutSeconds, elapsed)
	}
	if l.step.TimeoutSeconds > 0 {
		return describeTimeout("step", l.step.TimeoutSeconds, elapsed)
	}
	return describeTimeout("cancelled", 0, elapsed)
}

func waitRunBookDelay(ctx context.Context, stop <-chan struct{}, seconds int) bool {
	if seconds <= 0 {
		return true
	}
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	case <-stop:
		return false
	}
}

func (m *RunBookManager) execute(snapshot *runBookSnapshot, runID string, stop <-chan struct{}, remoteIP, agent, invokedBy string) {
	defer configRestoreGate.EndWork()

	if !appShutdown.BeginWork() {
		m.finish(runID, "interrupted", "Server is shutting down")
		return
	}
	defer appShutdown.DoneWork()

	ctx := context.Background()
	cancel := func() {}
	if snapshot.runBook.TimeoutSeconds > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(snapshot.runBook.TimeoutSeconds)*time.Second)
	}
	defer cancel()
	m.mutate(runID, func(run *RunBookRun) { run.Status = "running" })

	hasFailure := false
	for index, step := range snapshot.runBook.Steps {
		select {
		case <-ctx.Done():
			m.finishWithNotifications(snapshot.runBook, runID, "timed_out", runBookTimeoutMessage(snapshot.runBook.TimeoutSeconds))
			return
		case <-stop:
			m.finishWithNotifications(snapshot.runBook, runID, "stopped", "Stopped by user")
			return
		default:
		}
		if !waitRunBookDelay(ctx, stop, step.DelayBeforeSeconds) {
			select {
			case <-ctx.Done():
				m.finishWithNotifications(snapshot.runBook, runID, "timed_out", runBookTimeoutMessage(snapshot.runBook.TimeoutSeconds))
			default:
				m.finishWithNotifications(snapshot.runBook, runID, "stopped", "Stopped by user")
			}
			return
		}

		started := time.Now()
		m.mutate(runID, func(run *RunBookRun) {
			run.CurrentStep = index + 1
			run.Steps[index].Status = "running"
			run.Steps[index].StartedAt = &started
		})

		var final RunBookAttemptResult
		for attempt := 1; attempt <= step.RetryCount+1; attempt++ {
			if attempt > 1 {
				m.mutate(runID, func(run *RunBookRun) { run.Steps[index].Status = "retrying" })
				if !waitRunBookDelay(ctx, stop, step.RetryDelaySeconds) {
					break
				}
			}
			attemptCtx := ctx
			attemptCancel := func() {}
			if step.TimeoutSeconds > 0 {
				attemptCtx, attemptCancel = context.WithTimeout(ctx, time.Duration(step.TimeoutSeconds)*time.Second)
			}
			limits := runBookStepLimits{runBookCtx: ctx, runBookTimeoutSeconds: snapshot.runBook.TimeoutSeconds, step: step}
			if step.RequestID != "" {
				final = executeRunBookHTTPAttempt(attemptCtx, snapshot.requests[step.RequestID], snapshot.runBook.RunBookID, runID, step, index, attempt, remoteIP, agent, invokedBy, limits)
			} else {
				final = executeRunBookCommandAttempt(attemptCtx, snapshot.commands[step.CmdID], snapshot.runBook.RunBookID, runID, step, index, attempt, remoteIP, agent, invokedBy, limits)
			}
			attemptCancel()
			m.mutate(runID, func(run *RunBookRun) {
				run.Steps[index].Attempts = append(run.Steps[index].Attempts, final)
				run.Steps[index].Status = final.Status
			})
			if final.Status == "succeeded" || final.Status == "skipped" {
				break
			}
		}

		ended := time.Now()
		m.mutate(runID, func(run *RunBookRun) {
			result := &run.Steps[index]
			result.EndedAt = &ended
			result.Duration = ended.Sub(started).Round(time.Millisecond).String()
			result.TraceID, result.Output, result.Error = final.TraceID, final.Output, final.Error
			if final.Status == "succeeded" {
				run.SuccessCount++
			} else if final.Status == "skipped" {
				run.SkippedCount++
				run.FailureCount++
			} else {
				run.FailureCount++
			}
		})
		if step.RequestID == "" {
			// HTTP request steps send their own notifications inside runHTTPRequestJob.
			sendRunBookCommandNotification(snapshot.commands[step.CmdID], final, remoteIP, agent)
		}

		if final.Status != "succeeded" {
			hasFailure = true
			if step.OnFailure == "" || step.OnFailure == "stop" {
				m.finishWithNotifications(snapshot.runBook, runID, "failed", fmt.Sprintf("Step '%s' failed", step.StepID))
				return
			}
		}

		select {
		case <-stop:
			m.finishWithNotifications(snapshot.runBook, runID, "stopped", "Stopped by user")
			return
		default:
		}
	}
	status := "succeeded"
	if hasFailure {
		status = "failed"
	}
	m.finishWithNotifications(snapshot.runBook, runID, status, "")
}

func (m *RunBookManager) finish(runID, status, message string) {
	m.Lock()
	run := m.runs[runID]
	if run == nil {
		m.Unlock()
		return
	}
	now := time.Now()
	run.Status, run.EndedAt, run.Duration, run.Error = status, &now, now.Sub(run.StartedAt).Round(time.Millisecond).String(), message
	for i := range run.Steps {
		if run.Steps[i].Status == "pending" {
			run.Steps[i].Status = "stopped"
		}
	}
	delete(m.active, run.RunBookID)
	delete(m.stops, runID)
	m.persistLocked(run)
	m.Unlock()
}

func (m *RunBookManager) finishWithNotifications(runBook RunBook, runID, status, message string) {
	m.finish(runID, status, message)
	if run, ok := m.Get(runID); ok {
		sendRunBookNotifications(runBook, run)
	}
}

func executeRemoteCommandContext(ctx context.Context, cmdConfig *CommandConfig, logFile *os.File) (string, bool, error) {
	maxOutputSize, err := parseSize(cmdConfig.Command.MaxOutputSize)
	if err != nil {
		return "", false, fmt.Errorf("invalid max_output_size: %v", err)
	}
	executor, err := createExecutor(cmdConfig.Host)
	if err != nil {
		return "", false, fmt.Errorf("failed to create executor: %v", err)
	}
	defer executor.Close()
	if err := executor.Connect(); err != nil {
		return "", false, fmt.Errorf("failed to connect: %v", err)
	}
	type result struct {
		output    string
		truncated bool
		err       error
	}
	done := make(chan result, 1)
	go func() {
		output, truncated, runErr := executor.Execute(cmdConfig.Command.Command, logFile, maxOutputSize)
		done <- result{output, truncated, runErr}
	}()
	select {
	case result := <-done:
		return result.output, result.truncated, result.err
	case <-ctx.Done():
		_ = executor.Close()
		result := <-done
		return result.output, result.truncated, ctx.Err()
	}
}

func executeRunBookCommandAttempt(ctx context.Context, cmdConfig *CommandConfig, runBookID, runID string, step RunBookStep, index, attempt int, remoteIP, agent, invokedBy string, limits runBookStepLimits) RunBookAttemptResult {
	started := time.Now()
	result := RunBookAttemptResult{Attempt: attempt, Status: "failed", StartedAt: started}
	if cmdConfig == nil {
		result.Error = "Command configuration not found"
		result.EndedAt = time.Now()
		return result
	}
	cmdID := cmdConfig.Command.CmdID
	traceID := generateTraceID()
	result.TraceID = traceID
	logFile, logFilename, err := createCommandLogFile(cmdID, traceID)
	if err != nil {
		result.Error = err.Error()
		result.EndedAt = time.Now()
		return result
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "               trace_id : %s\n", traceID)
	fmt.Fprintf(logFile, "             runbook_id : %s\n", runBookID)
	fmt.Fprintf(logFile, "                  run_id : %s\n", runID)
	fmt.Fprintf(logFile, "                 step_id : %s\n", step.StepID)
	fmt.Fprintf(logFile, "              step_index : %d\n", index+1)
	fmt.Fprintf(logFile, "                 attempt : %d\n", attempt)
	fmt.Fprintf(logFile, "    request_received_at : %s\n", started.Format("2006-01-02 15:04:05.000"))
	fmt.Fprintf(logFile, "                 cmd_id : %s\n", cmdID)
	fmt.Fprintf(logFile, "                   host : %s\n", cmdConfig.Host.Host)
	fmt.Fprintf(logFile, "              remote_ip : %s\n", remoteIP)
	fmt.Fprintf(logFile, "                  agent : %s\n", agent)
	fmt.Fprintf(logFile, "             invoked_by : %s\n", invokedBy)
	fmt.Fprintf(logFile, "                trigger : runbook\n")

	if cmdConfig.Command.SkipIfRunning && execManager.isCommandActive(cmdID) {
		writeRunningLogStatus(logFile)
		err = fmt.Errorf("command already running and skip_if_running is enabled")
		updateExecutionLogStatus(logFile, logFilename, completedExecutionLogStatus(0, false, err, 1))
		finalizeCommandLog(cmdConfig, logFile, logFilename)
		result.Status, result.Error, result.EndedAt, result.Duration = "skipped", err.Error(), time.Now(), "0s"
		return result
	}

	cmdLock := execManager.getLock(cmdID)
	cmdLock.Lock()
	defer cmdLock.Unlock()
	activeReq := &ActiveRequest{TraceID: traceID, CmdID: cmdID, Host: cmdConfig.Host.Host, Command: cmdConfig.Command.Command, StartTime: started, RemoteIP: "runbook:" + runBookID}
	execManager.startExecution(activeReq)
	defer execManager.endExecution(cmdID, traceID)
	writeRunningLogStatus(logFile)
	output, truncated, runErr, commandTimedOut := runCommandWithTimeout(ctx, cmdConfig, logFile)
	ended := time.Now()
	duration := ended.Sub(started)
	exitCode := 0
	timedOut := false
	if runErr != nil {
		exitCode = 1
		if commandTimedOut {
			// The Command's own timeout_seconds fired first; runErr already explains it.
			timedOut = true
		} else if ctx.Err() != nil {
			// Replace Go's "context deadline exceeded" with a plain-language explanation
			// in the run result, the log file and the notifications alike.
			timedOut = true
			runErr = errors.New(limits.timeoutMessage(duration))
		}
	}
	updateExecutionLogStatus(logFile, logFilename, completedExecutionLogStatus(duration, truncated, runErr, exitCode))
	finalizeCommandLog(cmdConfig, logFile, logFilename)
	result.Output, result.OutputTruncated, result.EndedAt, result.Duration = output, truncated, ended, duration.Round(time.Millisecond).String()
	if runErr != nil {
		result.Error = runErr.Error()
		if timedOut {
			result.Status = "timed_out"
		}
	} else {
		result.Status = "succeeded"
	}
	return result
}

func sendRunBookCommandNotification(cmdConfig *CommandConfig, result RunBookAttemptResult, remoteIP, agent string) {
	if cmdConfig == nil || result.TraceID == "" || result.Status == "skipped" {
		return
	}
	exitCode := 0
	if result.Status != "succeeded" {
		exitCode = 1
	}
	data := WebhookData{Output: result.Output, ErrorMsg: result.Error, ExitCode: exitCode, TraceID: result.TraceID, RequestReceivedAt: result.StartedAt.Format("2006-01-02 15:04:05.000"), CmdID: cmdConfig.Command.CmdID, Host: cmdConfig.Host.Host, Command: cmdConfig.Command.Command, RemoteIP: remoteIP, Agent: agent, ExecTime: result.Duration}
	if cmdConfig.Command.NotifyWebhook != "" {
		appShutdown.RunAsync("runbook step webhook "+result.TraceID, func() { sendWebhookNotification(cmdConfig.Command.NotifyWebhook, data) })
	}
	if cmdConfig.Command.NotifyEmail != nil {
		cache.RLock()
		smtp, cfg := cache.config.SMTP, cache.config
		cache.RUnlock()
		if smtp != nil {
			email := prepareEmailData(data, cfg, result.OutputTruncated)
			email.IncludeOutput = cmdConfig.Command.NotifyEmail.IncludeOutput
			queueEmail(email, cmdConfig.Command.NotifyEmail, smtp)
		}
	}
}

func formatRunBookSummary(run *RunBookRun, format string, includeOutput bool, maxLines int) string {
	if format == "json" {
		data, _ := json.MarshalIndent(run, "", "  ")
		return string(data)
	}
	var text bytes.Buffer
	fmt.Fprintf(&text, "RunBook: %s\nRun ID: %s\nStatus: %s\nDuration: %s\nSteps: %d succeeded, %d failed, %d skipped\n", run.RunBookID, run.RunID, strings.ToUpper(run.Status), run.Duration, run.SuccessCount, run.FailureCount, run.SkippedCount)
	for i, step := range run.Steps {
		target := step.CmdID
		if step.RequestID != "" {
			target = "HTTP " + step.RequestID
		}
		fmt.Fprintf(&text, "\n%d. %s (%s): %s\n", i+1, step.Label, target, strings.ToUpper(step.Status))
		if step.Error != "" {
			fmt.Fprintf(&text, "Error: %s\n", step.Error)
		}
		if includeOutput && step.Output != "" {
			fmt.Fprintf(&text, "Output:\n%s\n", truncateOutputByLines(step.Output, maxLines))
		}
	}
	if format == "markdown" {
		return "# RunBook " + run.RunBookID + "\n\n```text\n" + text.String() + "```\n"
	}
	if format == "html" {
		return "<html><body><h1>RunBook " + html.EscapeString(run.RunBookID) + "</h1><pre>" + html.EscapeString(text.String()) + "</pre></body></html>"
	}
	return text.String()
}

func sendRunBookNotifications(runBook RunBook, run *RunBookRun) {
	if runBook.NotifyWebhook != "" {
		payload, _ := json.Marshal(run)
		appShutdown.RunAsync("runbook webhook "+run.RunID, func() {
			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Post(runBook.NotifyWebhook, "application/json", bytes.NewReader(payload))
			if err == nil {
				resp.Body.Close()
			}
		})
	}
	mail := runBook.NotifyEmail
	if mail == nil {
		return
	}
	success := run.Status == "succeeded"
	if success && !boolDefaultTrue(mail.OnSuccess) {
		return
	}
	if !success && !boolDefaultTrue(mail.OnFailure) {
		return
	}
	cache.RLock()
	smtp := cache.config.SMTP
	cache.RUnlock()
	if smtp == nil || !smtp.isEnabled() || len(mail.To) == 0 {
		return
	}
	format := runBook.SummaryFormat
	if format == "" {
		format = "html"
	}
	includeOutput := boolDefaultTrue(mail.IncludeOutput)
	body := formatRunBookSummary(run, format, includeOutput, mail.MaxOutputLines)
	subject := mail.Subject
	if subject == "" {
		subject = "{{status}} - {{runbook_id}} ({{run_id}})"
	}
	subject = strings.NewReplacer("{{status}}", strings.ToUpper(run.Status), "{{runbook_id}}", run.RunBookID, "{{run_id}}", run.RunID).Replace(subject)
	appShutdown.RunAsync("runbook email "+run.RunID, func() {
		message := gomail.NewMessage()
		message.SetHeader("To", mail.To...)
		if len(mail.CC) > 0 {
			message.SetHeader("Cc", mail.CC...)
		}
		message.SetHeader("Subject", subject)
		contentType := "text/plain"
		if format == "html" {
			contentType = "text/html"
		}
		message.SetBody(contentType, body)
		if err := sendSMTPMessage(smtp, message); err != nil {
			logSMTPError(fmt.Sprintf("Failed to send RunBook email for %s: %v", run.RunBookID, err))
		}
	})
}

func validateRunBooks(runBooks []RunBook, commandIDs, httpRequestIDs map[string]bool) error {
	seen := make(map[string]bool)
	for _, runBook := range runBooks {
		if runBook.RunBookID == "" || !isValidCommandID(runBook.RunBookID) {
			return fmt.Errorf("RunBook ID '%s' must contain only letters, numbers, and hyphens", runBook.RunBookID)
		}
		if seen[runBook.RunBookID] {
			return fmt.Errorf("duplicate RunBook ID '%s'", runBook.RunBookID)
		}
		seen[runBook.RunBookID] = true
		if len(runBook.Steps) == 0 {
			return fmt.Errorf("RunBook '%s' must contain at least one step", runBook.RunBookID)
		}
		stepIDs := make(map[string]bool)
		for index, step := range runBook.Steps {
			if step.StepID == "" || !isValidCommandID(step.StepID) {
				return fmt.Errorf("RunBook '%s' has invalid step_id '%s'", runBook.RunBookID, step.StepID)
			}
			if stepIDs[step.StepID] {
				return fmt.Errorf("RunBook '%s' has duplicate step_id '%s'", runBook.RunBookID, step.StepID)
			}
			stepIDs[step.StepID] = true
			if (step.CmdID == "") == (step.RequestID == "") {
				return fmt.Errorf("RunBook '%s' step '%s' must reference exactly one of cmd_id or request_id", runBook.RunBookID, step.StepID)
			}
			if step.RequestID != "" {
				if !httpRequestIDs[step.RequestID] {
					return fmt.Errorf("RunBook '%s' step '%s' references missing HTTP request '%s'", runBook.RunBookID, step.StepID, step.RequestID)
				}
			} else if !commandIDs[step.CmdID] {
				return fmt.Errorf("RunBook '%s' step '%s' references missing command '%s'", runBook.RunBookID, step.StepID, step.CmdID)
			}
			if step.OnFailure != "" && step.OnFailure != "stop" && step.OnFailure != "continue" {
				return fmt.Errorf("RunBook '%s' step '%s' has invalid on_failure", runBook.RunBookID, step.StepID)
			}
			if index == 0 && step.DelayBeforeSeconds != 0 {
				return fmt.Errorf("RunBook '%s' first step cannot have delay_before_seconds", runBook.RunBookID)
			}
			if step.DelayBeforeSeconds < 0 || step.RetryCount < 0 || step.RetryDelaySeconds < 0 || step.TimeoutSeconds < 0 {
				return fmt.Errorf("RunBook '%s' step '%s' contains a negative timing or retry value", runBook.RunBookID, step.StepID)
			}
		}
		if runBook.TimeoutSeconds < 0 {
			return fmt.Errorf("RunBook '%s' timeout_seconds cannot be negative", runBook.RunBookID)
		}
		if runBook.CronSchedule != "" {
			parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			if _, err := parser.Parse(runBook.CronSchedule); err != nil {
				return fmt.Errorf("RunBook '%s' has invalid cron_schedule: %v", runBook.RunBookID, err)
			}
		}
		format := runBook.SummaryFormat
		if format != "" && format != "html" && format != "text" && format != "markdown" && format != "json" {
			return fmt.Errorf("RunBook '%s' has invalid summary_format '%s'", runBook.RunBookID, format)
		}
		if runBook.NotifyEmail != nil && len(runBook.NotifyEmail.To) == 0 {
			return fmt.Errorf("RunBook '%s' email notification requires at least one recipient", runBook.RunBookID)
		}
	}
	return nil
}

func isValidCommandID(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func validateNoLegacyWorkflowFields(data []byte) error {
	var raw map[string]interface{}
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	if _, exists := raw["reports"]; exists {
		return fmt.Errorf("legacy 'reports' configuration is no longer supported; define 'runbooks' instead")
	}
	if commands, ok := raw["commands"].([]interface{}); ok {
		for _, value := range commands {
			command, _ := value.(map[string]interface{})
			for _, field := range []string{"next_cmd_id", "next_on_success_only", "chain_delay_seconds"} {
				if _, exists := command[field]; exists {
					return fmt.Errorf("legacy command field '%s' is no longer supported; configure sequencing in a RunBook", field)
				}
			}
		}
	}
	return nil
}

func runBookAuth(r *http.Request, runBook *RunBook) (*Claims, bool, bool) {
	header := r.Header.Get("Authorization")
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return nil, false, false
	}
	if authManager != nil {
		if claims, err := authManager.authenticateRequest(r); err == nil {
			if claims.Role == RoleUser && !authManager.userHasRunBook(claims.Username, runBook.RunBookID) {
				return nil, false, false
			}
			return claims, true, false
		}
	}
	api := runBook.BearerToken != "" && subtle.ConstantTimeCompare([]byte(parts[1]), []byte(runBook.BearerToken)) == 1
	return nil, api, api
}

func adminJWTOK(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" || authManager == nil {
		return false
	}
	claims, err := authManager.authenticateRequest(r)
	return err == nil && claims.Role == RoleAdmin
}

func runBookExecutionHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/runbooks/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[1] != "runs" {
		sendJSONResponse(w, http.StatusNotFound, APIResponse{Success: false, Error: "Invalid RunBook path"})
		return
	}
	runBook, err := findRunBook(parts[0])
	if err != nil {
		sendJSONResponse(w, http.StatusNotFound, APIResponse{Success: false, Error: err.Error()})
		return
	}
	claims, authorized, apiToken := runBookAuth(r, runBook)
	if !authorized {
		sendJSONResponse(w, http.StatusUnauthorized, APIResponse{Success: false, Error: "Invalid or missing RunBook authorization"})
		return
	}
	if claims != nil {
		r = withClaims(r, claims)
	}

	if len(parts) == 2 && r.Method == http.MethodPost {
		trigger := "api"
		if r.Header.Get("X-RunBook-Trigger") == "ui" {
			trigger = "manual"
		}
		invokedBy := "api-token"
		if claims != nil {
			invokedBy = claims.Username
		}
		if apiToken {
			trigger = "api"
		}
		run, err := runBookManager.StartAs(runBook.RunBookID, trigger, getClientIP(r), r.UserAgent(), invokedBy)
		if err != nil {
			code := http.StatusBadRequest
			if strings.Contains(err.Error(), "already running") {
				code = http.StatusConflict
			}
			sendJSONResponse(w, code, APIResponse{Success: false, Error: err.Error()})
			return
		}
		sendJSONResponse(w, http.StatusAccepted, APIResponse{Success: true, Data: run})
		return
	}
	if len(parts) == 2 && r.Method == http.MethodGet {
		if apiToken {
			sendJSONResponse(w, http.StatusUnauthorized, APIResponse{Success: false, Error: "RunBook history requires user authentication"})
			return
		}
		sendJSONResponse(w, http.StatusOK, APIResponse{Success: true, Data: runBookManager.List(runBook.RunBookID)})
		return
	}
	if len(parts) >= 3 {
		run, ok := runBookManager.Get(parts[2])
		if !ok || run.RunBookID != runBook.RunBookID {
			sendJSONResponse(w, http.StatusNotFound, APIResponse{Success: false, Error: "Run not found"})
			return
		}
		if len(parts) == 3 && r.Method == http.MethodGet {
			sendJSONResponse(w, http.StatusOK, APIResponse{Success: true, Data: run})
			return
		}
		if len(parts) == 4 && parts[3] == "stop" && r.Method == http.MethodPost {
			if !runBookManager.RequestStop(run.RunID) {
				sendJSONResponse(w, http.StatusConflict, APIResponse{Success: false, Error: "Run is not active"})
				return
			}
			sendJSONResponse(w, http.StatusAccepted, APIResponse{Success: true, Data: map[string]string{"status": "stopping"}})
			return
		}
		if len(parts) == 4 && parts[3] == "download" && r.Method == http.MethodGet {
			format := r.URL.Query().Get("format")
			if format == "" {
				format = runBook.SummaryFormat
			}
			if format == "" {
				format = "html"
			}
			contentType := "text/plain"
			if format == "html" {
				contentType = "text/html"
			} else if format == "json" {
				contentType = "application/json"
			}
			w.Header().Set("Content-Type", contentType)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(formatRunBookSummary(run, format, true, 0)))
			return
		}
	}
	sendJSONResponse(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Error: "Method not allowed"})
}

func executeScheduledRunBook(runBookID string) {
	atomic.AddInt64(&totalCronRequests, 1)
	_, err := runBookManager.StartAs(runBookID, "schedule", "cron-scheduler", "cron-scheduler", "scheduler")
	if err != nil {
		if runBook, findErr := findRunBook(runBookID); findErr == nil && strings.Contains(err.Error(), "already running") {
			runBookManager.RecordSkipped(*runBook, "schedule", err.Error())
		}
		fmt.Printf("[RUNBOOK] Scheduled RunBook %s skipped: %v\n", runBookID, err)
	}
}

// runBookIndex returns the position of runBookID in runBooks, or -1.
func runBookIndex(runBooks []RunBook, runBookID string) int {
	for i := range runBooks {
		if runBooks[i].RunBookID == runBookID {
			return i
		}
	}
	return -1
}

func loadConfigMap(path string) (map[string]interface{}, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return config, nil
}

func decodeRunBooks(value interface{}) ([]RunBook, error) {
	if value == nil {
		return []RunBook{}, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var runBooks []RunBook
	if err := json.Unmarshal(data, &runBooks); err != nil {
		return nil, err
	}
	return runBooks, nil
}

func assignMissingRunBookStepIDs(runBook *RunBook) {
	taken := make(map[string]bool, len(runBook.Steps))
	for _, step := range runBook.Steps {
		if step.StepID != "" {
			taken[step.StepID] = true
		}
	}
	for index := range runBook.Steps {
		if runBook.Steps[index].StepID != "" {
			continue
		}
		for {
			candidate := "s-" + generateTraceID()[:8]
			if !taken[candidate] {
				runBook.Steps[index].StepID = candidate
				taken[candidate] = true
				break
			}
		}
	}
}

func commandIDSetFromConfig(config map[string]interface{}) map[string]bool {
	result := make(map[string]bool)
	commands, _ := config["commands"].([]interface{})
	for _, value := range commands {
		command, _ := value.(map[string]interface{})
		if id, ok := command["cmd_id"].(string); ok {
			result[id] = true
		}
	}
	return result
}

func (ca *ConfigAPI) HandleRunBooks(w http.ResponseWriter, r *http.Request) {
	config, err := loadConfigMap(ca.configPath)
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to read configuration")
		return
	}
	runBooks, err := decodeRunBooks(config["runbooks"])
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Invalid RunBook configuration")
		return
	}

	if r.URL.Path == "/api/config/runbooks" {
		switch r.Method {
		case http.MethodGet:
			sendConfigJSONSuccess(w, http.StatusOK, runBooks, "RunBooks retrieved successfully")
		case http.MethodPost:
			var runBook RunBook
			if json.NewDecoder(r.Body).Decode(&runBook) != nil {
				sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
				return
			}
			err := ca.mutateConfig(func(config map[string]interface{}) error {
				runBooks, err := decodeRunBooks(config["runbooks"])
				if err != nil {
					return httpErr(http.StatusInternalServerError, "Invalid RunBook configuration")
				}
				for _, existing := range runBooks {
					if existing.RunBookID == runBook.RunBookID {
						return httpErr(http.StatusConflict, "RunBook already exists")
					}
				}
				assignMissingRunBookStepIDs(&runBook)
				runBooks = append(runBooks, runBook)
				if err := validateRunBooks(runBooks, commandIDSetFromConfig(config), httpRequestIDSetFromConfig(config)); err != nil {
					return httpErr(http.StatusBadRequest, err.Error())
				}
				config["runbooks"] = runBooks
				return nil
			})
			if err != nil {
				sendConfigMutationError(w, err, "Failed to save RunBook")
				return
			}
			sendConfigJSONSuccess(w, http.StatusCreated, runBook, "RunBook created successfully")
		default:
			sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

	runBookID := strings.TrimPrefix(r.URL.Path, "/api/config/runbooks/")
	if runBookID == "" || strings.Contains(runBookID, "/") {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid RunBook ID")
		return
	}
	if runBookIndex(runBooks, runBookID) < 0 {
		sendConfigJSONError(w, http.StatusNotFound, "RunBook not found")
		return
	}

	switch r.Method {
	case http.MethodPut:
		var runBook RunBook
		if json.NewDecoder(r.Body).Decode(&runBook) != nil {
			sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		if runBook.RunBookID != runBookID {
			sendConfigJSONError(w, http.StatusBadRequest, "RunBook ID cannot be changed")
			return
		}
		assignMissingRunBookStepIDs(&runBook)
		err := ca.mutateConfig(func(config map[string]interface{}) error {
			runBooks, err := decodeRunBooks(config["runbooks"])
			if err != nil {
				return httpErr(http.StatusInternalServerError, "Invalid RunBook configuration")
			}
			index := runBookIndex(runBooks, runBookID)
			if index < 0 {
				return httpErr(http.StatusNotFound, "RunBook not found")
			}
			runBooks[index] = runBook
			if err := validateRunBooks(runBooks, commandIDSetFromConfig(config), httpRequestIDSetFromConfig(config)); err != nil {
				return httpErr(http.StatusBadRequest, err.Error())
			}
			config["runbooks"] = runBooks
			return nil
		})
		if err != nil {
			sendConfigMutationError(w, err, "Failed to save RunBook")
			return
		}
		sendConfigJSONSuccess(w, http.StatusOK, runBook, "RunBook updated successfully")
	case http.MethodDelete:
		if runBookManager.IsActive(runBookID) {
			sendConfigJSONError(w, http.StatusConflict, "Cannot delete an active RunBook")
			return
		}
		err := ca.mutateConfig(func(config map[string]interface{}) error {
			if users := usersGranting(config["users"], "runbook", runBookID); len(users) > 0 {
				return httpErr(http.StatusConflict, fmt.Sprintf("Cannot delete RunBook '%s' because it is granted to user '%s'", runBookID, users[0]))
			}
			runBooks, err := decodeRunBooks(config["runbooks"])
			if err != nil {
				return httpErr(http.StatusInternalServerError, "Invalid RunBook configuration")
			}
			index := runBookIndex(runBooks, runBookID)
			if index < 0 {
				return httpErr(http.StatusNotFound, "RunBook not found")
			}
			config["runbooks"] = append(runBooks[:index], runBooks[index+1:]...)
			return nil
		})
		if err != nil {
			sendConfigMutationError(w, err, "Failed to delete RunBook")
			return
		}
		sendConfigJSONSuccess(w, http.StatusOK, nil, "RunBook deleted successfully")
	default:
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}
