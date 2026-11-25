package main

// df runner and parser for the Disk Monitor (port of sshDiskMon's sshcheck).
//
// A check opens one SSH connection to the host (reusing SSHExecutor.Connect so
// PEM keys, passwords and the friendly auth errors are shared with Commands),
// runs a single `df -PB1 '<mount>' ...` and parses the POSIX output. stdout and
// stderr are kept apart so a missing mount's error line can be reported.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	diskMonitorDialTimeout    = 10 * time.Second
	diskMonitorCommandTimeout = 15 * time.Second
	diskMonitorTotalTimeout   = 30 * time.Second
	diskMonitorOutputLimit    = 1 << 20 // 1 MB per stream
)

var errDiskCommandTimeout = errors.New("command timeout: df did not finish within 15s")

// dfResult is the parsed `df -PB1` row for one mount path.
type dfResult struct {
	Filesystem string
	TotalBytes uint64
	UsedBytes  uint64
	FreeBytes  uint64
}

// diskMountResult is the per-mount outcome of a check.
type diskMountResult struct {
	MountPath string
	Result    *dfResult
	Err       error
}

// diskMountInfo is one row of a discovery run (`df -PB1` with no arguments).
type diskMountInfo struct {
	MountPath  string
	Filesystem string
	TotalBytes uint64
	UsedBytes  uint64
	FreeBytes  uint64
}

// diskDfRun is the raw material written to the batch log file.
type diskDfRun struct {
	Command  string
	Stdout   string
	Stderr   string
	ConnErr  error // connection / session failure: nothing ran
	RunErr   error // df exited non-zero, timed out or was cancelled
	Duration time.Duration
}

func diskCheckContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, diskMonitorTotalTimeout)
}

// buildDfCommand returns the single shell line that reports every mount.
func buildDfCommand(sudo bool, mounts []string) string {
	var sb strings.Builder
	if sudo {
		sb.WriteString("sudo -n ")
	}
	sb.WriteString("df -PB1")
	for _, m := range mounts {
		sb.WriteByte(' ')
		sb.WriteString(shellQuoteSingle(m))
	}
	return sb.String()
}

func shellQuoteSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// parseDfOutput turns `df -PB1` output into rows keyed by mount path.
//
//	Filesystem      1B-blocks         Used    Available Capacity Mounted on
//	/dev/sda1     53660876800  41598001152  12028493824      78% /
func parseDfOutput(out string) map[string]*dfResult {
	results := make(map[string]*dfResult)
	for i, line := range strings.Split(out, "\n") {
		if i == 0 {
			continue // header
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		mount := strings.Join(fields[5:], " ") // "Mounted on" may contain spaces
		total, errT := strconv.ParseUint(fields[1], 10, 64)
		used, errU := strconv.ParseUint(fields[2], 10, 64)
		free, errF := strconv.ParseUint(fields[3], 10, 64)
		if errT != nil || errU != nil || errF != nil {
			continue
		}
		results[mount] = &dfResult{Filesystem: fields[0], TotalBytes: total, UsedBytes: used, FreeBytes: free}
	}
	return results
}

// extractMountError finds the stderr line that mentions the mount path.
func extractMountError(stderr, mount string) string {
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && strings.Contains(line, mount) {
			return line
		}
	}
	return strings.TrimSpace(stderr)
}

// isRealDisk filters pseudo filesystems out of the detect-volumes list.
func isRealDisk(filesystem string, total uint64) bool {
	if total == 0 {
		return false
	}
	fs := strings.ToLower(strings.TrimSpace(filesystem))
	switch fs {
	case "tmpfs", "devtmpfs", "udev", "none", "overlay":
		return false
	}
	return !strings.HasPrefix(fs, "tmpfs")
}

// runDiskDf connects to the host and runs one df command, honouring ctx and the
// dial/command timeouts. stdout and stderr are captured separately.
func runDiskDf(ctx context.Context, host Host, command string) diskDfRun {
	run := diskDfRun{Command: command}
	started := time.Now()
	defer func() { run.Duration = time.Since(started) }()

	normalizeHost(&host)
	if host.BastionHostRef != "" && host.Bastion == nil {
		run.ConnErr = fmt.Errorf("host is configured to connect via bastion host %q but the bastion was not resolved (configuration not loaded)", host.BastionHostRef)
		return run
	}
	executor := &SSHExecutor{host: host, connectTimeout: diskMonitorDialTimeout}

	connDone := make(chan error, 1)
	go func() {
		err := executor.Connect()
		connDone <- err
		if err == nil {
			// If the caller gave up while we were connecting, do not leak the client.
			select {
			case <-ctx.Done():
				executor.Close()
			default:
			}
		}
	}()
	select {
	case err := <-connDone:
		if err != nil {
			run.ConnErr = err
			return run
		}
	case <-ctx.Done():
		run.ConnErr = fmt.Errorf("connection to %s timed out: %w", host.Host, ctx.Err())
		return run
	}
	defer executor.Close()

	session, err := executor.client.NewSession()
	if err != nil {
		run.ConnErr = fmt.Errorf("failed to create SSH session: %w", err)
		return run
	}
	defer session.Close()

	stdout := NewLimitedBuffer(diskMonitorOutputLimit)
	stderr := NewLimitedBuffer(diskMonitorOutputLimit)
	session.Stdout = stdout
	session.Stderr = stderr

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	select {
	case err := <-done:
		run.RunErr = err
	case <-time.After(diskMonitorCommandTimeout):
		_ = session.Signal(ssh.SIGKILL)
		run.RunErr = errDiskCommandTimeout
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		run.RunErr = ctx.Err()
	}
	run.Stdout = stdout.String()
	run.Stderr = stderr.String()
	return run
}

// checkDiskMounts runs df for the given mounts and returns one result per mount.
func checkDiskMounts(ctx context.Context, host Host, sudo bool, mounts []string) (diskDfRun, []diskMountResult) {
	results := make([]diskMountResult, len(mounts))
	for i, m := range mounts {
		results[i] = diskMountResult{MountPath: m}
	}
	run := runDiskDf(ctx, host, buildDfCommand(sudo, mounts))

	if run.ConnErr != nil {
		for i := range results {
			results[i].Err = run.ConnErr
		}
		return run, results
	}
	if run.RunErr != nil && (errors.Is(run.RunErr, errDiskCommandTimeout) || errors.Is(run.RunErr, context.Canceled) || errors.Is(run.RunErr, context.DeadlineExceeded)) {
		for i := range results {
			results[i].Err = run.RunErr
		}
		return run, results
	}

	// df exits non-zero when any path is missing; parse what it did print.
	parsed := parseDfOutput(run.Stdout)
	for i, m := range mounts {
		if r, ok := parsed[m]; ok {
			results[i].Result = r
			continue
		}
		msg := extractMountError(run.Stderr, m)
		if msg == "" {
			msg = "mount not present in df output"
			if run.RunErr != nil {
				msg = fmt.Sprintf("%s (%v)", msg, run.RunErr)
			}
		}
		results[i].Err = errors.New(msg)
	}
	return run, results
}

// listDiskMounts runs `df -PB1` without arguments to enumerate every mount.
func listDiskMounts(ctx context.Context, host Host, sudo bool) (diskDfRun, []diskMountInfo, error) {
	run := runDiskDf(ctx, host, buildDfCommand(sudo, nil))
	if run.ConnErr != nil {
		return run, nil, run.ConnErr
	}
	if run.RunErr != nil && strings.TrimSpace(run.Stdout) == "" {
		msg := strings.TrimSpace(run.Stderr)
		if msg == "" {
			msg = run.RunErr.Error()
		}
		return run, nil, errors.New(msg)
	}
	parsed := parseDfOutput(run.Stdout)
	out := make([]diskMountInfo, 0, len(parsed))
	for mount, r := range parsed {
		out = append(out, diskMountInfo{MountPath: mount, Filesystem: r.Filesystem, TotalBytes: r.TotalBytes, UsedBytes: r.UsedBytes, FreeBytes: r.FreeBytes})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MountPath < out[j].MountPath })
	return run, out, nil
}
