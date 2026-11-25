package main

// Disk Monitor scheduler: a 30-second tick loop that groups due volumes by host,
// runs one `df` per host (one log file per batch) and applies the results to
// the runtime state. It runs independently of cron_enabled and is governed by
// the disk_monitor.enabled flag, which is re-read on every tick so hot reloads
// take effect immediately.

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	diskMonitorTick           = 30 * time.Second
	diskMonitorCoalesceWindow = 30 * time.Second
	diskMonitorRemoteIP       = "disk-monitor"
)

var (
	errDiskHostBusy       = errors.New("this host is already being checked; try again in a moment")
	errDiskConcurrencyCap = errors.New("max_concurrent_host_checks reached; the check stays due and is retried on the next tick")
)

type diskHostBatch struct {
	HostRef string
	Host    Host
	UseSudo bool
	Volumes []DiskVolume
}

type diskBatchParams struct {
	Trigger   string // schedule | manual | test
	InvokedBy string
	RemoteIP  string
	Agent     string
}

type diskBatchResult struct {
	VolumeID  string
	MountPath string
	Status    string // OK | TRIPPED | FAILED
	Result    *dfResult
	Err       error
}

type diskBatchOutcome struct {
	TraceID  string
	Filename string
	OK       bool
	Duration time.Duration
	Results  []diskBatchResult
}

type diskMonitorEngine struct {
	mu        sync.Mutex
	hostLocks map[string]*sync.Mutex
	inFlight  map[string]time.Time
	manual    map[string]bool
	kick      chan struct{}
	wakeAlert chan struct{}
}

var diskMonitor = newDiskMonitorEngine()

func newDiskMonitorEngine() *diskMonitorEngine {
	return &diskMonitorEngine{
		hostLocks: make(map[string]*sync.Mutex),
		inFlight:  make(map[string]time.Time),
		manual:    make(map[string]bool),
		kick:      make(chan struct{}, 1),
		wakeAlert: make(chan struct{}, 1),
	}
}

func diskHostKey(hostRef string, sudo bool) string {
	if sudo {
		return hostRef + "|sudo"
	}
	return hostRef + "|nosudo"
}

// MarkDue flags a volume for a check on the next tick ("Check now").
func (e *diskMonitorEngine) MarkDue(volumeID string) {
	e.mu.Lock()
	e.manual[volumeID] = true
	e.mu.Unlock()
}

// Kick wakes the scheduler loop so manual checks start within a second.
func (e *diskMonitorEngine) Kick() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// RemoveVolume forgets a deleted volume's manual flag and runtime state.
func (e *diskMonitorEngine) RemoveVolume(volumeID string) {
	e.mu.Lock()
	delete(e.manual, volumeID)
	e.mu.Unlock()
	diskStateStore.removeVolume(volumeID)
}

func (e *diskMonitorEngine) inFlightHosts() map[string]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]bool, len(e.inFlight))
	for key := range e.inFlight {
		out[key] = true
	}
	return out
}

func (e *diskMonitorEngine) wake() {
	select {
	case e.wakeAlert <- struct{}{}:
	default:
	}
}

// startDiskMonitorScheduler runs the tick loop until shutdown.
func startDiskMonitorScheduler() {
	e := diskMonitor
	e.tick(time.Now())
	ticker := time.NewTicker(diskMonitorTick)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			e.tick(time.Now())
		case <-e.kick:
			e.tick(time.Now())
		case <-appShutdown.Done():
			return
		}
	}
}

// dueBatches groups every enabled volume that is due into one batch per
// (host, sudo) pair.
func (e *diskMonitorEngine) dueBatches(cfg *Config, settings DiskMonitorConfig, now time.Time) []diskHostBatch {
	state := diskStateStore.snapshot()
	e.mu.Lock()
	manual := make(map[string]bool, len(e.manual))
	for id := range e.manual {
		manual[id] = true
	}
	e.mu.Unlock()

	batches := make(map[string]*diskHostBatch)
	order := make([]string, 0)
	for _, v := range settings.Volumes {
		if !v.isEnabled() {
			continue
		}
		host, ok := cfg.Hosts[v.HostRef]
		if !ok || !isSSHHost(host) {
			continue
		}
		due := manual[v.VolumeID]
		if !due {
			st, seen := state.Volumes[v.VolumeID]
			if !seen || st.NextDue.IsZero() || !now.Before(st.NextDue.Add(-diskMonitorCoalesceWindow)) {
				due = true
			}
		}
		if !due {
			continue
		}
		key := diskHostKey(v.HostRef, v.UseSudo)
		batch, exists := batches[key]
		if !exists {
			batch = &diskHostBatch{HostRef: v.HostRef, Host: host, UseSudo: v.UseSudo}
			batches[key] = batch
			order = append(order, key)
		}
		batch.Volumes = append(batch.Volumes, v)
	}
	sort.Strings(order)
	out := make([]diskHostBatch, 0, len(order))
	for _, key := range order {
		out = append(out, *batches[key])
	}
	return out
}

func (e *diskMonitorEngine) tick(now time.Time) {
	if appShutdown.IsShuttingDown() {
		return
	}
	settings, present := currentDiskMonitorSettings()
	if !present {
		return
	}
	pruneDiskMonitorState(settings)
	if !settings.isEnabled() {
		return
	}
	cfg := cache.GetConfig()
	if cfg == nil {
		return
	}
	for _, batch := range e.dueBatches(cfg, settings, now) {
		batch := batch
		appShutdown.RunAsync("disk check "+batch.HostRef, func() {
			params := diskBatchParams{Trigger: "schedule", InvokedBy: diskMonitorRemoteIP, RemoteIP: diskMonitorRemoteIP, Agent: diskMonitorRemoteIP}
			if _, err := e.runBatch(appShutdown.ctx, batch, params, settings); err != nil && !errors.Is(err, errDiskHostBusy) && !errors.Is(err, errDiskConcurrencyCap) {
				fmt.Printf("[DISK] %s: %v\n", batch.HostRef, err)
			}
		})
	}
}

// RunHostNow runs a synchronous batch (used by the host "Test" endpoint).
func (e *diskMonitorEngine) RunHostNow(hostRef string, host Host, useSudo bool, volumes []DiskVolume, trigger, invokedBy string) (*diskBatchOutcome, error) {
	if !appShutdown.BeginWork() {
		return nil, errors.New("server is shutting down")
	}
	defer appShutdown.DoneWork()
	settings, _ := currentDiskMonitorSettings()
	batch := diskHostBatch{HostRef: hostRef, Host: host, UseSudo: useSudo, Volumes: volumes}
	params := diskBatchParams{Trigger: trigger, InvokedBy: invokedBy, RemoteIP: diskMonitorRemoteIP, Agent: "admin-ui"}
	return e.runBatch(appShutdown.ctx, batch, params, settings)
}

// runBatch performs one host check: lock, cap, log file, df, state update.
func (e *diskMonitorEngine) runBatch(parent context.Context, batch diskHostBatch, params diskBatchParams, settings DiskMonitorConfig) (outcome *diskBatchOutcome, err error) {
	key := diskHostKey(batch.HostRef, batch.UseSudo)

	e.mu.Lock()
	lock, ok := e.hostLocks[key]
	if !ok {
		lock = &sync.Mutex{}
		e.hostLocks[key] = lock
	}
	if !lock.TryLock() {
		e.mu.Unlock()
		return nil, errDiskHostBusy
	}
	if len(e.inFlight) >= settings.maxConcurrent() {
		lock.Unlock()
		e.mu.Unlock()
		return nil, errDiskConcurrencyCap
	}
	e.inFlight[key] = time.Now()
	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		delete(e.inFlight, key)
		e.mu.Unlock()
		lock.Unlock()
		if r := recover(); r != nil {
			fmt.Printf("[DISK] panic while checking %s: %v\n%s\n", batch.HostRef, r, debug.Stack())
			outcome, err = nil, fmt.Errorf("internal error while checking %s: %v", batch.HostRef, r)
		}
	}()

	if gateErr := configRestoreGate.BeginWork(); gateErr != nil {
		return nil, gateErr
	}
	defer configRestoreGate.EndWork()
	if params.Trigger == "schedule" {
		atomic.AddInt64(&totalCronRequests, 1)
	}

	mounts := make([]string, 0, len(batch.Volumes))
	for _, v := range batch.Volumes {
		mounts = append(mounts, v.MountPath)
	}
	traceID := generateTraceID()
	started := time.Now()
	cmdID := "diskcheck-" + batch.HostRef
	logFile, logFilename, logErr := createCommandLogFile(cmdID, traceID)
	if logErr != nil {
		return nil, fmt.Errorf("failed to create log file: %v", logErr)
	}
	sudoText := "no"
	if batch.UseSudo {
		sudoText = "yes"
	}
	writeLogField(logFile, "trace_id", traceID)
	writeLogField(logFile, "request_received_at", started.Format("2006-01-02 15:04:05.000"))
	writeLogField(logFile, "cmd_id", cmdID)
	writeLogField(logFile, "job_type", diskCheckJobType)
	writeLogField(logFile, "host_ref", batch.HostRef)
	writeLogField(logFile, "host", batch.Host.Host)
	writeLogField(logFile, "mounts", strings.Join(mounts, ", "))
	writeLogField(logFile, "use_sudo", sudoText)
	writeLogField(logFile, "remote_ip", params.RemoteIP)
	writeLogField(logFile, "agent", params.Agent)
	writeLogField(logFile, "invoked_by", params.InvokedBy)
	writeLogField(logFile, "trigger", params.Trigger)
	writeRunningLogStatus(logFile)

	checkCtx, cancel := diskCheckContext(parent)
	run, results := checkDiskMounts(checkCtx, batch.Host, batch.UseSudo, mounts)
	cancel()
	interrupted := parent != nil && parent.Err() != nil

	// Output section: raw df exchange followed by the per-mount verdicts.
	fmt.Fprintf(logFile, "--- command ---\n%s\n", run.Command)
	fmt.Fprintf(logFile, "--- stdout ---\n%s", orEmptyMarker(run.Stdout))
	fmt.Fprintf(logFile, "--- stderr ---\n%s", orEmptyMarker(run.Stderr))
	fmt.Fprintln(logFile, "--- results ---")

	now := time.Now()
	outcome = &diskBatchOutcome{TraceID: traceID, Filename: logFilename, OK: true, Duration: run.Duration}
	counts := map[string]int{}
	var firstErr error
	for i, v := range batch.Volumes {
		res := results[i]
		status := e.applyResult(v, settings, res, now, traceID, interrupted)
		counts[status]++
		row := diskBatchResult{VolumeID: v.VolumeID, MountPath: v.MountPath, Status: status, Result: res.Result, Err: res.Err}
		outcome.Results = append(outcome.Results, row)
		if res.Err != nil {
			outcome.OK = false
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %v", v.MountPath, res.Err)
			}
			fmt.Fprintf(logFile, "%-8s %-24s %v\n", status, v.MountPath, res.Err)
			continue
		}
		thresholdBytes := diskThresholdBytes(v.ThresholdValue, v.ThresholdUnit, res.Result.TotalBytes)
		line := fmt.Sprintf("%-8s %-24s free %s of %s (%.1f%% free)   threshold %s (%s)",
			status, v.MountPath, humanBytes1024(res.Result.FreeBytes), humanBytes1024(res.Result.TotalBytes),
			diskPercentFree(res.Result.FreeBytes, res.Result.TotalBytes), formatDiskThreshold(v.ThresholdValue, v.ThresholdUnit), humanBytes1024(thresholdBytes))
		if status == diskStatusTripped {
			if st, ok := diskStateStore.volume(v.VolumeID); ok && !st.TrippedSince.IsZero() {
				line += "   tripped since " + st.TrippedSince.Format("2006-01-02 15:04")
			}
		}
		fmt.Fprintln(logFile, line)
	}
	exitCode := 0
	var logErrValue error
	switch {
	case interrupted:
		exitCode = 1
		logErrValue = errors.New("Interrupted by shutdown")
	case run.ConnErr != nil:
		exitCode = 1
		logErrValue = run.ConnErr
	case firstErr != nil:
		exitCode = 1
		logErrValue = firstErr
	}
	resultSummary := fmt.Sprintf("%d ok, %d tripped, %d failed", counts[diskStatusOK], counts[diskStatusTripped], counts["FAILED"])
	status := completedExecutionLogStatus(run.Duration, false, logErrValue, exitCode)
	status.Extra = []logStatusField{{Key: "disk_results", Value: resultSummary}}
	updateExecutionLogStatus(logFile, logFilename, status)
	finalizeExecutionLog("", logFile, logFilename)

	fmt.Printf("[DISK] %s (%s): %s in %.1fs\n", batch.HostRef, params.Trigger, resultSummary, run.Duration.Seconds())
	e.wake()
	return outcome, nil
}

func orEmptyMarker(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(empty)\n"
	}
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}

// applyResult updates one volume's runtime state after a check and returns the
// status label used in the log (OK / TRIPPED / FAILED).
func (e *diskMonitorEngine) applyResult(v DiskVolume, settings DiskMonitorConfig, res diskMountResult, now time.Time, traceID string, interrupted bool) string {
	e.mu.Lock()
	delete(e.manual, v.VolumeID)
	e.mu.Unlock()

	label := diskStatusOK
	diskStateStore.update(func(st *diskMonitorState) {
		vs := st.volumeState(v.VolumeID)
		vs.LastTraceID = traceID
		if interrupted {
			// We stopped, the host did not fail: keep the previous status and
			// make the volume due again straight after restart.
			vs.NextDue = now
			label = "FAILED"
			return
		}
		vs.LastCheckedAt = now
		vs.NextDue = now.Add(diskVolumeInterval(v, settings))

		if res.Err != nil {
			prev := vs.Status
			vs.Status = diskStatusUnknown
			vs.LastError = res.Err.Error()
			if prev == diskStatusOK || prev == "" {
				vs.TrippedSince = now
			}
			label = "FAILED"
			return
		}

		vs.LastError = ""
		vs.LastFreeBytes = res.Result.FreeBytes
		vs.LastUsedBytes = res.Result.UsedBytes
		vs.LastTotalBytes = res.Result.TotalBytes
		vs.History = append(vs.History, diskReading{At: now, FreeBytes: res.Result.FreeBytes, TotalBytes: res.Result.TotalBytes})
		if len(vs.History) > diskMonitorHistorySize {
			vs.History = vs.History[len(vs.History)-diskMonitorHistorySize:]
		}

		threshold := diskThresholdBytes(v.ThresholdValue, v.ThresholdUnit, res.Result.TotalBytes)
		prev := vs.Status
		if res.Result.FreeBytes < threshold {
			vs.Status = diskStatusTripped
			if prev != diskStatusTripped {
				vs.TrippedSince = now
			}
			label = diskStatusTripped
		} else {
			vs.Status = diskStatusOK
			if prev == diskStatusTripped || prev == diskStatusUnknown {
				vs.PendingRecovery = true
			}
			vs.TrippedSince = time.Time{}
			label = diskStatusOK
		}
	})
	return label
}

// flushDiskMonitorState is called from performCleanup so the last batch is kept.
func flushDiskMonitorState() {
	diskStateStore.flush()
}
