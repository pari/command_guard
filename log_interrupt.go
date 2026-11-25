package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// markInterruptedLogs closes out execution logs that a previous process left in
// the "job running" state (exitcode -1). That happens when cguard stops or
// crashes while a long job runs: shutdown waits only shutdownGracePeriod, so the
// final status is never written and the Overview, Logs page and daily email would
// otherwise show the run as running forever. Only logs started before startedBefore
// are touched, so jobs the new process starts during the sweep are left alone.
func markInterruptedLogs(logsDir string, startedBefore time.Time) int {
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		fmt.Printf("[LOG] Could not scan %s for interrupted runs: %v\n", logsDir, err)
		return 0
	}

	fixed := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, "_log.txt") {
			continue
		}
		path := filepath.Join(logsDir, name)
		fields, err := readOverviewLogHeader(path)
		if err != nil || fields["exitcode"] != "-1" {
			continue
		}
		startedAt, err := parseOverviewLogTime(fields["request_received_at"], time.Local)
		if err != nil || !startedAt.Before(startedBefore) {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		lastOutput := info.ModTime()
		duration := lastOutput.Sub(startedAt)
		if duration < 0 {
			duration = 0
		}
		status := executionLogStatus{
			ExecTime: fmt.Sprintf("%.3fs", duration.Seconds()),
			Error: fmt.Sprintf("Interrupted: Command Guard stopped while this job was running, so its result was never recorded (last output at %s). The remote process may still be running on the host.",
				lastOutput.Format("2006-01-02 15:04:05")),
			ExitCode: 1,
		}
		if err := rewriteExecutionLogStatus(path, status); err != nil {
			fmt.Printf("[LOG] Failed to mark interrupted run %s: %v\n", name, err)
			continue
		}
		fixed++
		fmt.Printf("[LOG] Marked interrupted run %s (started %s, last output %s)\n",
			name, startedAt.Format("2006-01-02 15:04:05"), lastOutput.Format("2006-01-02 15:04:05"))
	}
	return fixed
}
