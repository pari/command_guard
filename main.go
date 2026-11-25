/*
SSH Command Executor - Remote Command Execution Service

This service provides a secure HTTP API for executing SSH commands on remote hosts with
extensive features for automation, monitoring, and notification.

ARCHITECTURE OVERVIEW:
  - HTTP Server: Handles REST API requests on configurable port/interface
  - SSH Client: Executes commands on remote hosts using password or PEM key auth
  - Config Manager: Hot-reloads configuration without service restart
  - Cron Scheduler: Runs commands on schedule using cron expressions
  - Notification System: Sends webhooks and emails on command completion
  - Execution Manager: Prevents concurrent execution of same command
  - Logging System: Creates detailed log files for each execution

KEY COMPONENTS:
  - ConfigCache: Thread-safe configuration management with file hash checking
  - ExecutionManager: Tracks active requests and implements execution locking
  - LimitedBuffer: Memory-safe output capture with configurable size limits
  - Email Queue: File-based queue system for reliable email delivery
  - Variable Substitution: Supports {{config_vars}} and ${{ENV_VARS}}
	- RunBook Manager: Executes and tracks ordered multi-command workflows

API ENDPOINTS:
  - GET /commands/{cmd_id}: Execute a command (may require Bearer token)
	- POST /api/runbooks/{runbook_id}/runs: Start an asynchronous RunBook
  - GET /status: Server status and statistics
  - GET /logs/{trace_id}: Download complete execution log

CONFIGURATION:
  - config.json: Main configuration file (hot-reloaded on changes)
  - email.tpl: Email notification template
  - logs/: Directory for execution logs
  - mail_queue/: Directory for queued email notifications

SECURITY FEATURES:
  - Bearer token authentication per command
  - SSH authentication via password or PEM keys
  - Execution locking prevents resource exhaustion
  - Output size limiting prevents memory exhaustion

OPERATIONAL FEATURES:
  - Hot configuration reload (except port/bind_address)
	- Sequential RunBooks with retries, delays, and failure policies
  - Comprehensive logging with trace IDs
  - Clean console output with rotating statistics
  - Automatic cron job management

USAGE:
  1. Run ./compile_and_run.sh: it builds into instance/ and starts the server from there
  2. Configure hosts and commands in instance/config.json (or in the web UI)
  3. Access API: curl -H 'Authorization: Bearer <token>' http://localhost:8080/api/commands/command-id
  4. Monitor: tail -f instance/logs/*.txt

For detailed documentation see CLAUDE.md and docs/user-manual.md
*/

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/masterzen/winrm"
	"github.com/robfig/cron/v3"
	"golang.org/x/crypto/ssh"
	"gopkg.in/gomail.v2"
)

// RemoteExecutor interface for executing commands on remote hosts
type RemoteExecutor interface {
	Connect() error
	Execute(command string, logFile *os.File, maxOutputSize int64) (string, bool, error)
	Close() error
	GetHost() string
}

// translateScriptToHeredoc converts a script with shebang to heredoc format for SSH execution
// Supports #!/bin/bash and #!/bin/sh
// Example: "#!/bin/bash\necho hello\necho world" becomes "bash <<'EOF'\necho hello\necho world\nEOF"
func translateScriptToHeredoc(command string) string {
	// Only process if it starts with shebang
	if !strings.HasPrefix(command, "#!") {
		return command
	}

	// Split into lines
	lines := strings.Split(command, "\n")
	if len(lines) < 1 {
		return command
	}

	// Extract shebang and determine interpreter
	shebang := lines[0]
	var interpreter string

	switch {
	case strings.HasPrefix(shebang, "#!/bin/bash"):
		interpreter = "bash"
	case strings.HasPrefix(shebang, "#!/bin/sh"):
		interpreter = "sh"
	default:
		// Unsupported shebang, return as-is
		return command
	}

	// Build the heredoc command
	var scriptBody string
	if len(lines) > 1 {
		scriptBody = strings.Join(lines[1:], "\n")
	}

	// Format as heredoc
	heredocCommand := fmt.Sprintf("%s <<'EOF'\n%s\nEOF", interpreter, scriptBody)

	return heredocCommand
}

// SSHExecutor implements RemoteExecutor for SSH connections
type SSHExecutor struct {
	host           Host
	client         *ssh.Client
	bastionClient  *ssh.Client   // connection to the bastion host when host.Bastion is set
	connectTimeout time.Duration // 0 = default 30s (Disk Monitor uses a shorter dial timeout)
}

type sshAuthAttemptState struct {
	publicKeyOffered  bool
	passwordAttempted bool
}

// WinRMExecutor implements RemoteExecutor for WinRM connections
type WinRMExecutor struct {
	host   Host
	client *winrm.Client
	shell  *winrm.Shell
}

// Host defines connection parameters for a remote host (SSH or WinRM)
// Supports multiple connection types and authentication methods
type Host struct {
	// Connection type: "ssh" (default) or "winrm"
	ConnectionType string `json:"connection_type,omitempty"`

	// Common fields
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username"`
	Password string `json:"password,omitempty"`

	// SSH-specific fields
	PemFile string `json:"pem_file,omitempty"`     // Path to PEM key file
	PemPwd  string `json:"pem_password,omitempty"` // Password for encrypted PEM
	// HostKey is the host's public key in authorized_keys form ("ssh-ed25519 AAAA…").
	// It is recorded on the first connection (see ssh_hostkeys.go) or set by an admin;
	// a server presenting another key is refused.
	HostKey string `json:"host_key,omitempty"`
	// ID is the host's key in the hosts map, filled by resolveBastionHosts at config
	// load and never written to config.json.
	ID string `json:"-"`

	// Bastion (jump host): ID of another SSH Host that is logged into first; this
	// host is then reached through a TCP forward opened on that connection.
	// Single hop only: the bastion itself may not have a bastion_host_ref.
	BastionHostRef string `json:"bastion_host_ref,omitempty"`
	// Bastion is the resolved bastion host (copy of hosts[BastionHostRef]). It is
	// filled by resolveBastionHosts at config load and never written to config.json.
	Bastion *Host `json:"-"`

	// WinRM-specific fields
	WinRMHTTPS    bool   `json:"winrm_https,omitempty"`    // Use HTTPS (default: false)
	WinRMInsecure bool   `json:"winrm_insecure,omitempty"` // Skip cert verification
	WinRMCACert   string `json:"winrm_cacert,omitempty"`   // Path to CA certificate
}

// Command defines one atomic command that can be executed on a remote host.
type Command struct {
	CmdID          string             `json:"cmd_id"`                    // Unique identifier for the command
	HostRef        string             `json:"host_ref"`                  // Reference to host in hosts map
	Command        string             `json:"command"`                   // Shell command to execute
	Description    string             `json:"description"`               // Human-readable description
	CronSchedule   string             `json:"cron_schedule,omitempty"`   // Cron expression for scheduling
	BearerToken    string             `json:"bearer_token,omitempty"`    // Required bearer token for auth
	NotifyWebhook  string             `json:"notify_webhook,omitempty"`  // Webhook URL for notifications
	NotifyEmail    *EmailNotification `json:"notify_email,omitempty"`    // Email notification settings
	MaxOutputSize  string             `json:"max_output_size,omitempty"` // Max output size (e.g., "2MB")
	LogStorage     string             `json:"log_storage,omitempty"`     // "local" (default) or "s3"
	SkipIfRunning  bool               `json:"skip_if_running,omitempty"` // Skip if already running
	TimeoutSeconds int                `json:"timeout_seconds,omitempty"` // Stop waiting after N seconds (0 = no limit)
}

type SMTPConfig struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	FromEmail      string `json:"from_email,omitempty"`
	FromName       string `json:"from_name,omitempty"`
	Encryption     string `json:"encryption,omitempty"`
	Enabled        *bool  `json:"enabled,omitempty"`
	Timeout        int    `json:"timeout,omitempty"`
	Retries        int    `json:"retries,omitempty"`
	SkipCertVerify bool   `json:"skip_cert_verify,omitempty"`

	// From and TLS are retained for reading legacy configs and queued emails.
	// New saves use FromEmail, FromName, and Encryption instead.
	From string `json:"from,omitempty"`
	TLS  bool   `json:"tls,omitempty"`
}

type S3Config struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix,omitempty"`
}

type EmailNotification struct {
	To             []string `json:"to"`
	CC             []string `json:"cc,omitempty"`
	Subject        string   `json:"subject"`
	OnSuccess      bool     `json:"on_success"`
	OnFailure      bool     `json:"on_failure"`
	IncludeOutput  bool     `json:"include_output"`
	MaxOutputLines int      `json:"max_output_lines"`
}

type Config struct {
	// Application settings (strict order, always at top of config.json)
	Version            string   `json:"version,omitempty"`
	Port               int      `json:"port"`
	BindAddress        string   `json:"bind_address,omitempty"` // IP to bind to, defaults to "0.0.0.0"
	AccessURL          string   `json:"access_url,omitempty"`
	CronEnabled        string   `json:"cron_enabled,omitempty"`          // "Y" or "N", defaults to "Y"
	LogRetentionDays   int      `json:"log_retention_days"`              // Auto-delete local logs older than N days (default: 3)
	AuditRetentionDays int      `json:"audit_retention_days,omitempty"`  // Auto-delete security audit files older than N days (default: 90)
	DevMode            bool     `json:"dev_mode"`                        // Development mode (enables auth bypasses), defaults to false
	AdminPassword      string   `json:"admin_password,omitempty"`        // Admin UI password (stored after first setup)
	TOTPSecret         string   `json:"totp_secret,omitempty"`           // 2FA TOTP secret (stored after first setup)
	JWTSecret          string   `json:"jwt_secret,omitempty"`            // JWT signing secret (auto-generated and persisted)
	TrustedProxies     []string `json:"trusted_proxies,omitempty"`       // Socket peers allowed to supply forwarding headers
	SSHHostKeyChecking string   `json:"ssh_host_key_checking,omitempty"` // tofu (default), strict or off; see ssh_hostkeys.go
	Users              []User   `json:"users,omitempty"`                 // Role-based regular user accounts
	// Data sections (always at bottom of config.json)
	Variables                  map[string]interface{}            `json:"variables,omitempty"` // User-defined variables
	SMTP                       *SMTPConfig                       `json:"smtp,omitempty"`
	ScheduledJobsOverviewEmail *ScheduledJobsOverviewEmailConfig `json:"scheduled_jobs_overview_email,omitempty"`
	S3                         *S3Config                         `json:"s3,omitempty"`           // Optional AWS S3 log upload settings
	Terminals                  *TerminalsConfig                  `json:"terminals,omitempty"`    // Optional persistent browser terminal settings
	DiskMonitor                *DiskMonitorConfig                `json:"disk_monitor,omitempty"` // Optional disk free-space monitoring (see docs/disk-monitor.md)
	VNC                        *VNCSettings                      `json:"vnc,omitempty"`          // Optional Remote Desktops settings (see docs/vnc.md)
	Hosts                      map[string]Host                   `json:"hosts"`
	Commands                   []Command                         `json:"commands"`
	RunBooks                   []RunBook                         `json:"runbooks,omitempty"`
	HTTPRequests               []HTTPRequest                     `json:"http_requests,omitempty"`   // Scheduled HTTP requests / API calls
	VNCConnections             []VNCConnection                   `json:"vnc_connections,omitempty"` // Remote Desktops (docs/vnc.md)
}

type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

type CommandOutput struct {
	CmdID             string `json:"cmd_id"`
	Host              string `json:"host"`
	Command           string `json:"command"`
	Description       string `json:"description"`
	Output            string `json:"output"`
	ExecTime          string `json:"exec_time"`
	TraceID           string `json:"trace_id"`
	IsOutputTruncated int    `json:"is_output_truncated"`
	ExitCode          int    `json:"exit_code"`
}

type StatusResponse struct {
	Version               string                `json:"version"`
	ServerUptime          string                `json:"server_uptime"`
	TotalRequests         int                   `json:"total_requests_handled"`
	ActiveRequests        []ActiveRequestStatus `json:"active_requests"`
	LockedCommands        map[string]int        `json:"locked_commands"`
	CronEnabled           bool                  `json:"cron_enabled"`
	ScheduledJobs         []ScheduledJobStatus  `json:"scheduled_jobs"`
	ScheduledRunBooks     []ScheduledJobStatus  `json:"scheduled_runbooks"`
	ScheduledHTTPRequests []ScheduledJobStatus  `json:"scheduled_http_requests"`
	ActiveRunBooks        []RunBookRun          `json:"active_runbooks"`
	DiskMonitor           *DiskMonitorStatus    `json:"disk_monitor,omitempty"`
	ConfigLastUpdated     string                `json:"config_last_updated"`
	CurrentTime           string                `json:"current_time"`
}

type ScheduledJobStatus struct {
	CmdID        string `json:"cmd_id"`
	CronSchedule string `json:"cron_schedule"`
	NextRun      string `json:"next_run"`
}

// sortScheduledJobStatuses orders jobs by next run, soonest first, so the Status page
// lists the most immediate run at the top. Equal times are ordered by ID so the list
// does not reshuffle between refreshes. Jobs without a next run (empty or the zero
// time "0001-01-01 …") go last. NextRun uses "2006-01-02 15:04:05", which sorts
// correctly as a string.
func sortScheduledJobStatuses(jobs []ScheduledJobStatus) {
	hasNextRun := func(job ScheduledJobStatus) bool {
		return job.NextRun != "" && !strings.HasPrefix(job.NextRun, "0001-")
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		a, b := jobs[i], jobs[j]
		if hasNextRun(a) != hasNextRun(b) {
			return hasNextRun(a)
		}
		if a.NextRun != b.NextRun {
			return a.NextRun < b.NextRun
		}
		return a.CmdID < b.CmdID
	})
}

type ActiveRequestStatus struct {
	TraceID    string `json:"trace_id"`
	CmdID      string `json:"cmd_id"`
	Host       string `json:"host"`
	Command    string `json:"command"`
	RemoteIP   string `json:"remote_ip"`
	RunningFor string `json:"running_for"`
	StartedAt  string `json:"started_at"`
}

type ConfigCache struct {
	sync.RWMutex
	config     *Config
	lastHash   string
	configFile string
	// lastModTime/lastSize describe the file the current config was loaded
	// from. loadIfChanged skips reading and hashing the file while they still
	// match, which is the common case before every command run.
	lastModTime time.Time
	lastSize    int64
}

// configStatUnchanged reports whether the config file on disk still has the
// modification time and size recorded at the last successful load. A file
// modified within the last two seconds is never trusted, so an edit made in the
// same second as the previous load (same size, same mtime) is still hashed.
func (c *ConfigCache) configStatUnchanged() bool {
	info, err := os.Stat(c.configFile)
	if err != nil {
		return false
	}
	c.RLock()
	defer c.RUnlock()
	if c.config == nil || c.lastModTime.IsZero() {
		return false
	}
	if info.Size() != c.lastSize || !info.ModTime().Equal(c.lastModTime) {
		return false
	}
	return time.Since(info.ModTime()) > 2*time.Second
}

// GetConfig returns the current configuration safely
func (c *ConfigCache) GetConfig() *Config {
	c.RLock()
	defer c.RUnlock()
	return c.config
}

type ActiveRequest struct {
	TraceID   string    `json:"trace_id"`
	CmdID     string    `json:"cmd_id"`
	Host      string    `json:"host"`
	Command   string    `json:"command"`
	StartTime time.Time `json:"start_time"`
	RemoteIP  string    `json:"remote_ip"`
}

type ExecutionManager struct {
	sync.RWMutex
	locks          map[string]*sync.Mutex
	lastUsed       map[string]time.Time
	activeCount    map[string]int
	activeRequests map[string]*ActiveRequest
}

func (em *ExecutionManager) getLock(cmdID string) *sync.Mutex {
	em.RLock()
	if lock, exists := em.locks[cmdID]; exists {
		em.RUnlock()
		return lock
	}
	em.RUnlock()

	em.Lock()
	defer em.Unlock()

	// Double-check after acquiring write lock
	if lock, exists := em.locks[cmdID]; exists {
		return lock
	}

	lock := &sync.Mutex{}
	em.locks[cmdID] = lock
	em.lastUsed[cmdID] = time.Now()
	em.activeCount[cmdID] = 0
	return lock
}

func (em *ExecutionManager) startExecution(request *ActiveRequest) {
	em.Lock()
	em.activeCount[request.CmdID]++
	em.lastUsed[request.CmdID] = time.Now()
	em.activeRequests[request.TraceID] = request
	em.Unlock()
}

func (em *ExecutionManager) endExecution(cmdID, traceID string) {
	em.Lock()
	if count, exists := em.activeCount[cmdID]; exists && count > 0 {
		em.activeCount[cmdID]--
	}
	em.lastUsed[cmdID] = time.Now()
	delete(em.activeRequests, traceID)
	em.Unlock()
}

func (em *ExecutionManager) getActiveRequests() []ActiveRequest {
	em.RLock()
	defer em.RUnlock()

	requests := make([]ActiveRequest, 0, len(em.activeRequests))
	for _, req := range em.activeRequests {
		requests = append(requests, *req)
	}
	return requests
}

// isCommandActive checks if a command is currently being executed
func (em *ExecutionManager) isCommandActive(cmdID string) bool {
	em.RLock()
	defer em.RUnlock()
	count, exists := em.activeCount[cmdID]
	return exists && count > 0
}

func (em *ExecutionManager) cleanup() {
	em.Lock()
	defer em.Unlock()

	now := time.Now()
	for cmdID, lastUsed := range em.lastUsed {
		// Remove locks that haven't been used for 5 minutes and have no active executions
		if now.Sub(lastUsed) > 5*time.Minute && em.activeCount[cmdID] == 0 {
			delete(em.locks, cmdID)
			delete(em.lastUsed, cmdID)
			delete(em.activeCount, cmdID)
		}
	}
}

func (em *ExecutionManager) startCleanupRoutine() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				em.cleanup()
			case <-appShutdown.Done():
				return
			}
		}
	}()
}

// cleanOldLogs deletes local log files older than the configured retention period
func cleanOldLogs() {
	cfg := cache.GetConfig()
	days := cfg.LogRetentionDays
	if days <= 0 {
		days = 3 // default: 3 days
	}

	logsDir := "logs"
	files, err := ioutil.ReadDir(logsDir)
	if err != nil {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -days)
	deleted := 0
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".txt") {
			if file.ModTime().Before(cutoff) {
				if err := os.Remove(filepath.Join(logsDir, file.Name())); err == nil {
					deleted++
				}
			}
		}
	}
	deleted += runBookManager.PruneBefore(cutoff)
	if deleted > 0 {
		fmt.Printf("\nLocal log cleanup: deleted %d log files older than %d days\n", deleted, days)
	}
}

// startLogCleanupRoutine runs log cleanup on startup and then every hour
func startLogCleanupRoutine() {
	cleanOldLogs() // run immediately on startup
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cleanOldLogs()
		case <-appShutdown.Done():
			return
		}
	}
}

// Console display goroutine
func displayConsoleStats() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			uptime := time.Since(serverStartTime)
			hours := int(uptime.Hours())
			minutes := int(uptime.Minutes()) % 60
			seconds := int(uptime.Seconds()) % 60

			// Get port and bind address from config
			cfg := cache.GetConfig()
			bindAddr := cfg.BindAddress
			if bindAddr == "" {
				bindAddr = "0.0.0.0"
			}

			// Clear previous lines and display stats
			fmt.Printf("\r\033[K") // Clear line
			fmt.Printf("Uptime: %02d:%02d:%02d | %s:%d | Web requests: %d | Cron requests: %d",
				hours, minutes, seconds,
				bindAddr, cfg.Port,
				atomic.LoadInt64(&totalWebRequests),
				atomic.LoadInt64(&totalCronRequests))
		case <-appShutdown.Done():
			return
		}
	}
}

// Everything the server writes may hold credentials or command output (the
// config, keys, queued mail, logs, audit trail, runtime state), so it is created
// readable by the service account only.
const (
	privateFileMode os.FileMode = 0600
	privateDirMode  os.FileMode = 0700
)

// hardenFilePermissions fixes the modes of files and folders created before the
// server wrote them owner-only. Closing a directory to other users protects the
// files inside it, so the (possibly very many) log files are not touched one by one.
func hardenFilePermissions(configPath string) {
	chmod := func(path string, mode os.FileMode) {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() == mode {
			return
		}
		if err := os.Chmod(path, mode); err != nil {
			fmt.Printf("[SECURITY] Warning: could not set %s to %04o: %v\n", path, mode, err)
		}
	}
	chmod(configPath, privateFileMode)
	for _, dir := range []string{"logs", "pem_files", "mail_queue", "run"} {
		chmod(dir, privateDirMode)
	}
	for _, dir := range []string{"pem_files", "mail_queue"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.Type().IsRegular() {
				chmod(filepath.Join(dir, entry.Name()), privateFileMode)
			}
		}
	}
}

// Create log file for each command execution
func createCommandLogFile(cmdID, traceID string) (*os.File, string, error) {
	// Create logs directory if it doesn't exist
	if err := os.MkdirAll("logs", privateDirMode); err != nil {
		return nil, "", err
	}

	// Create filename with timestamp and cmd_id
	timestamp := time.Now().Format("2006_01_02_150405")
	filename := fmt.Sprintf("logs/%s_%s_%s_log.txt", timestamp, cmdID, traceID)

	file, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, privateFileMode)
	if err != nil {
		return nil, "", err
	}

	return file, filename, nil
}

func commandUsesS3LogStorage(cmd Command) bool {
	return strings.EqualFold(strings.TrimSpace(cmd.LogStorage), "s3")
}

func validateS3Config(s3Config *S3Config) error {
	if s3Config == nil {
		return fmt.Errorf("S3 configuration is not set")
	}
	if strings.TrimSpace(s3Config.AccessKeyID) == "" {
		return fmt.Errorf("S3 access key ID is required")
	}
	if strings.TrimSpace(s3Config.SecretAccessKey) == "" {
		return fmt.Errorf("S3 secret access key is required")
	}
	if strings.TrimSpace(s3Config.Region) == "" {
		return fmt.Errorf("S3 region is required")
	}
	if strings.TrimSpace(s3Config.Bucket) == "" {
		return fmt.Errorf("S3 bucket is required")
	}
	return nil
}

func newS3Client(ctx context.Context, s3Config *S3Config) (*awss3.Client, error) {
	if err := validateS3Config(s3Config); err != nil {
		return nil, err
	}

	cfg, err := awsconfig.LoadDefaultConfig(
		ctx,
		awsconfig.WithRegion(strings.TrimSpace(s3Config.Region)),
		awsconfig.WithCredentialsProvider(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{
				AccessKeyID:     strings.TrimSpace(s3Config.AccessKeyID),
				SecretAccessKey: strings.TrimSpace(s3Config.SecretAccessKey),
				Source:          "commandguard-s3-config",
			}, nil
		})),
	)
	if err != nil {
		return nil, err
	}

	return awss3.NewFromConfig(cfg), nil
}

func buildS3LogObjectKey(s3Config *S3Config, logFilename string) string {
	now := time.Now()
	keyParts := []string{
		strings.Trim(now.Format("2006"), "/"),
		strings.Trim(now.Format("01"), "/"),
		strings.Trim(now.Format("02"), "/"),
		filepath.Base(logFilename),
	}

	if s3Config != nil && strings.TrimSpace(s3Config.Prefix) != "" {
		keyParts = append([]string{strings.Trim(strings.TrimSpace(s3Config.Prefix), "/")}, keyParts...)
	}

	return strings.Join(keyParts, "/")
}

func uploadLogFileToS3(ctx context.Context, s3Config *S3Config, logFilename string) (string, error) {
	client, err := newS3Client(ctx, s3Config)
	if err != nil {
		return "", err
	}

	file, err := os.Open(logFilename)
	if err != nil {
		return "", err
	}
	defer file.Close()

	objectKey := buildS3LogObjectKey(s3Config, logFilename)
	_, err = client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:      aws.String(strings.TrimSpace(s3Config.Bucket)),
		Key:         aws.String(objectKey),
		Body:        file,
		ContentType: aws.String("text/plain"),
	})
	if err != nil {
		return "", err
	}

	return objectKey, nil
}

type s3LogPointer struct {
	URI    string
	Bucket string
	Key    string
}

// logStatusField is an extra key/value written with the final status (HTTP requests
// use it for http_status, response_ms, response_bytes and attempts).
type logStatusField struct {
	Key   string
	Value string
}

type executionLogStatus struct {
	ExecTime        string
	Error           string
	ExitCode        int
	OutputTruncated bool
	Extra           []logStatusField
}

func formatS3URI(bucket, key string) string {
	return fmt.Sprintf("s3://%s/%s", strings.TrimSpace(bucket), strings.TrimLeft(strings.TrimSpace(key), "/"))
}

func isKnownLogFieldKey(key string) bool {
	switch key {
	case "trace_id",
		"prev_trace_id",
		"parent_trace_id",
		"report_id",
		"runbook_id",
		"run_id",
		"step_id",
		"step_index",
		"attempt",
		"request_received_at",
		"cmd_id",
		"host",
		"remote_ip",
		"agent",
		"invoked_by",
		"trigger",
		"execution_skipped",
		"log_storage",
		"job_type",
		"request_id",
		"method",
		"url",
		"http_status",
		"response_ms",
		"response_bytes",
		"attempts",
		"host_ref",
		"mounts",
		"use_sudo",
		"disk_results",
		"output",
		"exec_time",
		"output_truncated",
		"error",
		"exitcode":
		return true
	default:
		return false
	}
}

func shouldUseLastLogFieldValue(key string) bool {
	switch key {
	case "exec_time", "output_truncated", "error", "exitcode", "http_status", "response_ms", "response_bytes", "attempts", "disk_results":
		return true
	default:
		return false
	}
}

func parseLogKeyValueFields(content string) map[string]string {
	fields := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if key == "" || !isKnownLogFieldKey(key) {
			continue
		}
		if _, exists := fields[key]; exists && !shouldUseLastLogFieldValue(key) {
			continue
		}
		fields[key] = strings.TrimSpace(parts[1])
		if key == "output" {
			break
		}
	}
	return fields
}

func writeLogField(writer io.Writer, key, value string) {
	fmt.Fprintf(writer, "%23s : %s\n", key, value)
}

func writeLogOutputMarker(writer io.Writer) {
	fmt.Fprintf(writer, "%23s:\n", "output")
}

func writeRunningLogStatus(logFile *os.File) {
	writeLogField(logFile, "exec_time", "0s")
	writeLogField(logFile, "error", "job running")
	writeLogField(logFile, "exitcode", "-1")
	writeLogOutputMarker(logFile)
}

func writeFinalLogStatus(writer io.Writer, status executionLogStatus) {
	errorValue := strings.TrimSpace(status.Error)
	if errorValue == "" {
		errorValue = "none"
	}

	for _, field := range status.Extra {
		writeLogField(writer, field.Key, field.Value)
	}
	writeLogField(writer, "exec_time", status.ExecTime)
	writeLogField(writer, "error", errorValue)
	writeLogField(writer, "exitcode", strconv.Itoa(status.ExitCode))
	if status.OutputTruncated {
		writeLogField(writer, "output_truncated", "YES (exceeded max_output_size)")
	}
}

func isFinalStatusLogField(key string) bool {
	switch key {
	case "exec_time", "output_truncated", "error", "exitcode", "http_status", "response_ms", "response_bytes", "attempts", "disk_results":
		return true
	default:
		return false
	}
}

func findOutputMarkerLine(content []byte) (int, int, bool) {
	lineStart := 0
	for lineStart <= len(content) {
		nextNewlineOffset := bytes.IndexByte(content[lineStart:], '\n')
		lineEnd := len(content)
		contentStart := len(content)
		if nextNewlineOffset >= 0 {
			lineEnd = lineStart + nextNewlineOffset
			contentStart = lineEnd + 1
		}

		line := content[lineStart:lineEnd]
		line = bytes.TrimSuffix(line, []byte("\r"))
		parts := bytes.SplitN(line, []byte(":"), 2)
		if len(parts) == 2 && strings.TrimSpace(string(parts[0])) == "output" {
			return lineStart, contentStart, true
		}

		if nextNewlineOffset < 0 {
			break
		}
		lineStart = contentStart
	}

	return 0, 0, false
}

func writeLogPrefixWithoutFinalStatus(builder *strings.Builder, prefix string) {
	for _, line := range strings.Split(prefix, "\n") {
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && isFinalStatusLogField(strings.TrimSpace(parts[0])) {
			continue
		}
		builder.WriteString(line)
		builder.WriteByte('\n')
	}
}

func rewriteExecutionLogStatus(logFilename string, status executionLogStatus) error {
	content, err := os.ReadFile(logFilename)
	if err != nil {
		return err
	}

	outputLineStart, outputContentStart, found := findOutputMarkerLine(content)
	if !found {
		return fmt.Errorf("output marker not found")
	}

	prefix := string(content[:outputLineStart])
	outputBody := content[outputContentStart:]

	var builder strings.Builder
	writeLogPrefixWithoutFinalStatus(&builder, prefix)
	writeFinalLogStatus(&builder, status)
	writeLogOutputMarker(&builder)
	builder.Write(outputBody)

	dir := filepath.Dir(logFilename)
	base := filepath.Base(logFilename)
	tempFile, err := os.CreateTemp(dir, "."+base+".status-*")
	if err != nil {
		return err
	}
	tempName := tempFile.Name()
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			os.Remove(tempName)
		}
	}()

	if _, err := tempFile.WriteString(builder.String()); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Chmod(privateFileMode); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Sync(); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}

	if err := os.Rename(tempName, logFilename); err != nil {
		return err
	}
	cleanupTemp = false
	return nil
}

func updateExecutionLogStatus(logFile *os.File, logFilename string, status executionLogStatus) {
	if logFile != nil {
		if err := logFile.Sync(); err != nil {
			fmt.Printf("[LOG] Failed to sync log file %s before status update: %v\n", logFilename, err)
		}
	}

	if err := rewriteExecutionLogStatus(logFilename, status); err != nil {
		fmt.Printf("[LOG] Failed to update log status for %s: %v\n", logFilename, err)
	}
}

func completedExecutionLogStatus(execDuration time.Duration, isTruncated bool, err error, exitCode int) executionLogStatus {
	errorValue := "none"
	if err != nil {
		errorValue = err.Error()
	}

	return executionLogStatus{
		ExecTime:        fmt.Sprintf("%.3fs", execDuration.Seconds()),
		Error:           errorValue,
		ExitCode:        exitCode,
		OutputTruncated: isTruncated,
	}
}

func stripLogValueQuotes(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'') {
			return strings.TrimSpace(value[1 : len(value)-1])
		}
	}
	return value
}

func parseS3URI(uri string) (string, string, error) {
	uri = stripLogValueQuotes(uri)
	if !strings.HasPrefix(uri, "s3://") {
		return "", "", fmt.Errorf("S3 URI must start with s3://")
	}

	rest := strings.TrimPrefix(uri, "s3://")
	slashIndex := strings.Index(rest, "/")
	if slashIndex <= 0 || slashIndex == len(rest)-1 {
		return "", "", fmt.Errorf("S3 URI must include bucket and object key")
	}

	bucket := strings.TrimSpace(rest[:slashIndex])
	key := strings.TrimLeft(strings.TrimSpace(rest[slashIndex+1:]), "/")
	if bucket == "" || key == "" {
		return "", "", fmt.Errorf("S3 URI must include bucket and object key")
	}

	return bucket, key, nil
}

func detectS3PointerLog(content []byte) (*s3LogPointer, bool, error) {
	fields := parseLogKeyValueFields(string(content))
	if !strings.EqualFold(stripLogValueQuotes(fields["log_storage"]), "s3") {
		return nil, false, nil
	}

	uri := stripLogValueQuotes(fields["output"])
	if uri == "" {
		return nil, true, fmt.Errorf("S3 pointer log is missing output URI")
	}

	bucket, key, err := parseS3URI(uri)
	if err != nil {
		return nil, true, err
	}

	return &s3LogPointer{
		URI:    formatS3URI(bucket, key),
		Bucket: bucket,
		Key:    key,
	}, true, nil
}

func downloadLogObjectFromS3(ctx context.Context, s3Config *S3Config, bucket, key string) ([]byte, error) {
	if err := validateS3Config(s3Config); err != nil {
		return nil, err
	}

	configBucket := strings.TrimSpace(s3Config.Bucket)
	if bucket != configBucket {
		return nil, fmt.Errorf("S3 pointer bucket %q does not match configured bucket %q", bucket, configBucket)
	}

	client, err := newS3Client(ctx, s3Config)
	if err != nil {
		return nil, err
	}

	object, err := client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer object.Body.Close()

	return io.ReadAll(object.Body)
}

func resolveStoredLogContent(ctx context.Context, logFilePath string) ([]byte, string, string, error) {
	localContent, err := os.ReadFile(logFilePath)
	if err != nil {
		return nil, "", "", err
	}

	pointer, isPointer, err := detectS3PointerLog(localContent)
	if err != nil {
		return nil, "", "", err
	}
	if !isPointer {
		return localContent, "local", "", nil
	}

	cfg := cache.GetConfig()
	if cfg == nil || cfg.S3 == nil {
		return nil, "", "", fmt.Errorf("S3 configuration is not set")
	}

	content, err := downloadLogObjectFromS3(ctx, cfg.S3, pointer.Bucket, pointer.Key)
	if err != nil {
		return nil, "", "", err
	}

	return content, "s3", pointer.URI, nil
}

func writePointerLogLine(builder *strings.Builder, fields map[string]string, key string) {
	if value, ok := fields[key]; ok && strings.TrimSpace(value) != "" {
		writeLogField(builder, key, value)
	}
}

func writeS3PointerLogFile(logFilename string, fullContent []byte, bucket, objectKey string) error {
	fields := parseLogKeyValueFields(string(fullContent))
	s3URI := formatS3URI(bucket, objectKey)

	var builder strings.Builder
	for _, key := range []string{
		"trace_id",
		"prev_trace_id",
		"parent_trace_id",
		"report_id",
		"runbook_id",
		"run_id",
		"step_id",
		"step_index",
		"attempt",
		"request_received_at",
		"cmd_id",
		"host",
		"remote_ip",
		"agent",
		"invoked_by",
		"trigger",
		"execution_skipped",
		"job_type",
		"request_id",
		"method",
		"url",
	} {
		writePointerLogLine(&builder, fields, key)
	}

	writeLogField(&builder, "log_storage", "s3")

	for _, key := range []string{
		"http_status",
		"response_ms",
		"response_bytes",
		"attempts",
		"exec_time",
		"error",
		"exitcode",
		"output_truncated",
	} {
		writePointerLogLine(&builder, fields, key)
	}
	writeLogField(&builder, "output", s3URI)

	dir := filepath.Dir(logFilename)
	base := filepath.Base(logFilename)
	tempFile, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tempName := tempFile.Name()
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			os.Remove(tempName)
		}
	}()

	if _, err := tempFile.WriteString(builder.String()); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Chmod(privateFileMode); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Sync(); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}

	if err := os.Rename(tempName, logFilename); err != nil {
		return err
	}
	cleanupTemp = false
	return nil
}

func testS3Config(ctx context.Context, s3Config *S3Config) error {
	client, err := newS3Client(ctx, s3Config)
	if err != nil {
		return err
	}

	bucket := strings.TrimSpace(s3Config.Bucket)
	if _, err := client.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(bucket)}); err != nil {
		return fmt.Errorf("HeadBucket failed: %v", err)
	}

	testKey := buildS3LogObjectKey(s3Config, fmt.Sprintf("s3_test_%s.txt", generateTraceID()))
	testBody := strings.NewReader("Command Guard S3 settings test\n")
	if _, err := client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(testKey),
		Body:        testBody,
		ContentType: aws.String("text/plain"),
	}); err != nil {
		return fmt.Errorf("PutObject failed: %v", err)
	}

	if _, err := client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(testKey),
	}); err != nil {
		return fmt.Errorf("DeleteObject failed after upload test: %v", err)
	}

	return nil
}

func logS3Error(message string) {
	os.MkdirAll("logs", privateDirMode)
	logFile, err := os.OpenFile("logs/errors_s3.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, privateFileMode)
	if err != nil {
		fmt.Printf("[S3 ERROR] %s\n", message)
		return
	}
	defer logFile.Close()

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(logFile, "[%s] %s\n", timestamp, message)
}

// finalizeCommandLog closes a Command run's log and applies its log_storage policy.
// The shared implementation lives in finalizeExecutionLog (http_requests.go).
func finalizeCommandLog(cmdConfig *CommandConfig, logFile *os.File, logFilename string) {
	logStorage := ""
	if cmdConfig != nil {
		logStorage = cmdConfig.Command.LogStorage
	}
	finalizeExecutionLog(logStorage, logFile, logFilename)
}

func generateTraceID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// isValidTraceID checks that a trace ID is exactly 16 lowercase hex characters
func isValidTraceID(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, ch := range s {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return false
		}
	}
	return true
}

func getClientIP(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		peer = host
	}
	peerIP := net.ParseIP(strings.TrimSpace(peer))
	if peerIP == nil || authManager == nil || !authManager.isTrustedProxy(peerIP) {
		return strings.TrimSpace(peer)
	}

	// A trusted proxy may append a forwarding chain. Reject the complete header
	// when any element is malformed, then select the closest untrusted hop while
	// walking from the socket peer toward the original client.
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		chain := make([]net.IP, 0, len(parts))
		for _, part := range parts {
			ip := net.ParseIP(strings.TrimSpace(part))
			if ip == nil {
				return strings.TrimSpace(peer)
			}
			chain = append(chain, ip)
		}
		for index := len(chain) - 1; index >= 0; index-- {
			if !authManager.isTrustedProxy(chain[index]) {
				return chain[index].String()
			}
		}
		if len(chain) > 0 {
			return chain[0].String()
		}
	}
	if value := strings.TrimSpace(r.Header.Get("X-Real-IP")); value != "" {
		if ip := net.ParseIP(value); ip != nil {
			return ip.String()
		}
	}
	return peerIP.String()
}

// Global Components - These manage the core functionality of the service

// cache manages configuration with hot-reload capability
// Monitors config.json for changes and reloads without restart
var cache = &ConfigCache{
	configFile: "config.json",
}

// execManager prevents concurrent execution of same command
// Tracks active requests and implements per-command locking
var execManager = &ExecutionManager{
	locks:          make(map[string]*sync.Mutex),
	lastUsed:       make(map[string]time.Time),
	activeCount:    make(map[string]int),
	activeRequests: make(map[string]*ActiveRequest),
}

// Service statistics and monitoring
var serverStartTime time.Time
var totalWebRequests int64  // Counter for HTTP API requests
var totalCronRequests int64 // Counter for cron-triggered executions

// Cron scheduler for automatic command execution
var cronScheduler *cron.Cron
var scheduledJobs map[string]cron.EntryID
var scheduledJobsMutex sync.RWMutex

var authManager *AuthManager // Global auth manager instance

const shutdownGracePeriod = 10 * time.Second

type shutdownCoordinator struct {
	shuttingDown atomic.Bool
	workMu       sync.Mutex
	wg           sync.WaitGroup
	ctx          context.Context
	cancel       context.CancelFunc
	deadline     time.Time
}

func newShutdownCoordinator() *shutdownCoordinator {
	ctx, cancel := context.WithCancel(context.Background())
	return &shutdownCoordinator{
		ctx:    ctx,
		cancel: cancel,
	}
}

func (sc *shutdownCoordinator) BeginWork() bool {
	if sc.shuttingDown.Load() {
		return false
	}

	sc.workMu.Lock()
	defer sc.workMu.Unlock()

	if sc.shuttingDown.Load() {
		return false
	}

	sc.wg.Add(1)
	return true
}

func (sc *shutdownCoordinator) DoneWork() {
	sc.wg.Done()
}

func (sc *shutdownCoordinator) RunAsync(name string, fn func()) bool {
	if !sc.BeginWork() {
		if name != "" {
			fmt.Printf("[SHUTDOWN] Skipping %s because shutdown is in progress\n", name)
		}
		return false
	}

	go func() {
		defer sc.DoneWork()
		fn()
	}()
	return true
}

func (sc *shutdownCoordinator) BeginShutdown() bool {
	sc.workMu.Lock()
	defer sc.workMu.Unlock()

	if sc.shuttingDown.Load() {
		return false
	}

	sc.deadline = time.Now().Add(shutdownGracePeriod)
	sc.shuttingDown.Store(true)
	sc.cancel()
	return true
}

func (sc *shutdownCoordinator) IsShuttingDown() bool {
	return sc.shuttingDown.Load()
}

func (sc *shutdownCoordinator) Done() <-chan struct{} {
	return sc.ctx.Done()
}

func (sc *shutdownCoordinator) contextUntilDeadline() (context.Context, context.CancelFunc) {
	sc.workMu.Lock()
	deadline := sc.deadline
	shuttingDown := sc.shuttingDown.Load()
	sc.workMu.Unlock()

	if !shuttingDown || deadline.IsZero() {
		return context.Background(), func() {}
	}

	return context.WithDeadline(context.Background(), deadline)
}

func (sc *shutdownCoordinator) waitForWork(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		sc.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

var appShutdown = newShutdownCoordinator()

func calculateFileHash(filename string) (string, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// normalizeHost sets default values for host configuration
func normalizeHost(host *Host) {
	// Set default connection type
	if host.ConnectionType == "" {
		host.ConnectionType = "ssh" // Default to SSH
	}

	// Set default ports based on connection type
	if host.Port == 0 {
		if strings.ToLower(host.ConnectionType) == "winrm" {
			if host.WinRMHTTPS {
				host.Port = 5986
			} else {
				host.Port = 5985
			}
		} else {
			host.Port = 22 // SSH default
		}
	}
	if host.Bastion != nil {
		normalizeHost(host.Bastion)
	}
}

// checkBastionRef validates host's bastion_host_ref against hosts and returns the
// normalized bastion to attach, or nil when the host connects directly.
func checkBastionRef(name string, host Host, hosts map[string]Host) (*Host, error) {
	ref := strings.TrimSpace(host.BastionHostRef)
	if ref == "" {
		return nil, nil
	}
	if ref == name {
		return nil, fmt.Errorf("host '%s' cannot use itself as its bastion host", name)
	}
	bastion, ok := hosts[ref]
	if !ok {
		return nil, fmt.Errorf("host '%s' references non-existent bastion host '%s'", name, ref)
	}
	if !isSSHHost(host) {
		return nil, fmt.Errorf("host '%s' is a %s host; only SSH hosts can connect via a bastion host", name, host.ConnectionType)
	}
	if !isSSHHost(bastion) {
		return nil, fmt.Errorf("host '%s' references bastion host '%s', which is a %s host; a bastion must be an SSH host", name, ref, bastion.ConnectionType)
	}
	if other := strings.TrimSpace(bastion.BastionHostRef); other != "" {
		return nil, fmt.Errorf("host '%s' references bastion host '%s', which itself connects via bastion '%s'; only a single hop is supported", name, ref, other)
	}
	if bastion.Password == "" && bastion.PemFile == "" {
		return nil, fmt.Errorf("bastion host '%s' (used by host '%s') has no authentication method (neither password nor PEM key)", ref, name)
	}
	bastion.Bastion = nil
	bastion.ID = ref
	normalizeHost(&bastion)
	return &bastion, nil
}

// resolveBastionHosts validates every bastion_host_ref in hosts and attaches the
// resolved bastion (Host.Bastion) to each referencing host, in place. Every code
// path that copies a Host out of the loaded config therefore carries the bastion
// with it. It returns the first problem found.
func resolveBastionHosts(hosts map[string]Host) error {
	for name, host := range hosts {
		host.BastionHostRef = strings.TrimSpace(host.BastionHostRef)
		bastion, err := checkBastionRef(name, host, hosts)
		if err != nil {
			return err
		}
		host.Bastion = bastion
		host.ID = name
		hosts[name] = host
	}
	return nil
}

// bastionRefProblems returns every bastion_host_ref problem in hosts (for --validate).
func bastionRefProblems(hosts map[string]Host) []string {
	names := make([]string, 0, len(hosts))
	for name := range hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	problems := make([]string, 0)
	for _, name := range names {
		if _, err := checkBastionRef(name, hosts[name], hosts); err != nil {
			problems = append(problems, err.Error())
		}
	}
	return problems
}

// bastionDependents lists the hosts that use hostID as their bastion host.
func bastionDependents(hosts map[string]Host, hostID string) []string {
	dependents := make([]string, 0)
	for name, host := range hosts {
		if strings.TrimSpace(host.BastionHostRef) == hostID {
			dependents = append(dependents, name)
		}
	}
	sort.Strings(dependents)
	return dependents
}

// createExecutor creates the appropriate executor based on host configuration
func createExecutor(host Host) (RemoteExecutor, error) {
	normalizeHost(&host)

	switch strings.ToLower(host.ConnectionType) {
	case "winrm":
		return &WinRMExecutor{host: host}, nil
	case "ssh", "":
		return &SSHExecutor{host: host}, nil
	default:
		return nil, fmt.Errorf("unsupported connection type: %s", host.ConnectionType)
	}
}

// sshClientConfig builds the SSH client configuration for one host (one hop).
// The returned state records which auth methods the server actually exercised so
// that explainSSHConnectError can describe a failure precisely.
func sshClientConfig(host Host, connectTimeout time.Duration) (*ssh.ClientConfig, *sshAuthAttemptState, error) {
	if host.Password == "" && host.PemFile == "" {
		return nil, nil, fmt.Errorf("no authentication method available (neither password nor PEM key configured)")
	}

	// Build auth methods based on available credentials
	authMethods := []ssh.AuthMethod{}
	authState := &sshAuthAttemptState{}

	// Try PEM key authentication first if available
	if host.PemFile != "" {
		pemSigner, err := loadPEMSigner(host.PemFile, host.PemPwd)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load PEM key from %s: %w", host.PemFile, err)
		}
		authMethods = append(authMethods, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			authState.publicKeyOffered = true
			return []ssh.Signer{pemSigner}, nil
		}))
	} else if host.Password != "" {
		// A callback with no signers records whether the server offered public-key
		// authentication without sending a key or opening a second connection.
		authMethods = append(authMethods, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			authState.publicKeyOffered = true
			return nil, nil
		}))
	}

	// Add password authentication if available
	if host.Password != "" {
		password := host.Password
		authMethods = append(authMethods, ssh.PasswordCallback(func() (string, error) {
			authState.passwordAttempted = true
			return password, nil
		}))
	}

	hostKeyCallback, hostKeyAlgorithms, err := hostKeyVerifier(host)
	if err != nil {
		return nil, nil, err
	}

	return &ssh.ClientConfig{
		User:              host.Username,
		Auth:              authMethods,
		HostKeyCallback:   hostKeyCallback,
		HostKeyAlgorithms: hostKeyAlgorithms,
		Timeout:           connectTimeout,
	}, authState, nil
}

// dialSSH is ssh.Dial with a deadline that also covers the SSH handshake.
// ClientConfig.Timeout only bounds the TCP connect, so a peer that accepts the
// connection and then stays silent would otherwise hold the caller (and the
// command's execution lock) forever.
func dialSSH(address string, cfg *ssh.ClientConfig, timeout time.Duration) (*ssh.Client, error) {
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return nil, err
	}
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	ncc, chans, reqs, err := ssh.NewClientConn(conn, address, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(ncc, chans, reqs), nil
}

// bastionHopError reports which stage of the second hop (through the bastion) failed.
type bastionHopError struct {
	stage string // forward | handshake | timeout
	err   error
}

func (e *bastionHopError) Error() string { return e.err.Error() }
func (e *bastionHopError) Unwrap() error { return e.err }

// dialThroughBastion opens a TCP forward to address on the bastion connection and
// runs the SSH handshake for the target over it. ClientConfig.Timeout only covers a
// direct TCP dial and the forwarded connection does not support deadlines, so both
// steps run under one timer; on expiry the forwarded connection is closed to
// unblock the handshake (the caller closes the bastion client).
func dialThroughBastion(bastionClient *ssh.Client, address string, cfg *ssh.ClientConfig, timeout time.Duration) (*ssh.Client, error) {
	type hopResult struct {
		client *ssh.Client
		err    *bastionHopError
	}
	results := make(chan hopResult, 1)
	conns := make(chan interface{ Close() error }, 1)
	go func() {
		conn, err := bastionClient.Dial("tcp", address)
		if err != nil {
			results <- hopResult{err: &bastionHopError{stage: "forward", err: err}}
			return
		}
		conns <- conn
		// NewClientConn closes conn itself when the handshake fails.
		ncc, chans, reqs, err := ssh.NewClientConn(conn, address, cfg)
		if err != nil {
			results <- hopResult{err: &bastionHopError{stage: "handshake", err: err}}
			return
		}
		results <- hopResult{client: ssh.NewClient(ncc, chans, reqs)}
	}()
	select {
	case r := <-results:
		if r.err != nil {
			return nil, r.err
		}
		return r.client, nil
	case <-time.After(timeout):
		select {
		case conn := <-conns:
			conn.Close()
		default:
		}
		return nil, &bastionHopError{stage: "timeout", err: fmt.Errorf("no SSH response from %s within %s", address, timeout)}
	}
}

// explainBastionHopError turns a second-hop failure into an operator-readable message
// that names the bastion and says whether the forward, the handshake or the time
// limit failed.
func explainBastionHopError(host Host, address string, authState sshAuthAttemptState, err error) error {
	ref := host.BastionHostRef
	hop, ok := err.(*bastionHopError)
	if !ok {
		return fmt.Errorf("via bastion host %q: %w", ref, err)
	}
	switch hop.stage {
	case "forward":
		text := hop.err.Error()
		if strings.Contains(text, "administratively prohibited") {
			return fmt.Errorf("bastion host %q refuses to forward connections to %s (check AllowTcpForwarding / PermitOpen in the bastion's sshd_config): %v", ref, address, hop.err)
		}
		if strings.Contains(text, "connect failed") {
			return fmt.Errorf("bastion host %q could not reach %s (is the host reachable from the bastion?): %v", ref, address, hop.err)
		}
		return fmt.Errorf("bastion host %q could not open a connection to %s: %v", ref, address, hop.err)
	case "timeout":
		return fmt.Errorf("connecting to %s via bastion host %q timed out: %v", address, ref, hop.err)
	default:
		return fmt.Errorf("via bastion host %q: %w", ref, explainSSHConnectError(host, address, authState, hop.err))
	}
}

// SSHExecutor implementation
func (e *SSHExecutor) Connect() error {
	if e.host.BastionHostRef != "" && e.host.Bastion == nil {
		// Never silently bypass the bastion: a Host built outside the loaded config
		// (or before resolveBastionHosts ran) must fail here rather than dial directly.
		return fmt.Errorf("host is configured to connect via bastion host %q but the bastion was not resolved (configuration not loaded)", e.host.BastionHostRef)
	}

	connectTimeout := 30 * time.Second
	if e.connectTimeout > 0 {
		connectTimeout = e.connectTimeout
	}
	sshConfig, authState, err := sshClientConfig(e.host, connectTimeout)
	if err != nil {
		return err
	}
	address := fmt.Sprintf("%s:%d", e.host.Host, e.host.Port)

	if e.host.Bastion == nil {
		client, err := dialSSH(address, sshConfig, connectTimeout)
		if err != nil {
			return explainSSHConnectError(e.host, address, *authState, err)
		}
		e.client = client
		return nil
	}

	// Hop 1: the bastion host
	bastion := *e.host.Bastion
	normalizeHost(&bastion)
	bastionConfig, bastionState, err := sshClientConfig(bastion, connectTimeout)
	if err != nil {
		return fmt.Errorf("bastion host %q: %w", e.host.BastionHostRef, err)
	}
	bastionAddress := fmt.Sprintf("%s:%d", bastion.Host, bastion.Port)
	bastionClient, err := dialSSH(bastionAddress, bastionConfig, connectTimeout)
	if err != nil {
		return fmt.Errorf("failed to connect to bastion host %q (%s): %w", e.host.BastionHostRef, bastionAddress, explainSSHConnectError(bastion, bastionAddress, *bastionState, err))
	}

	// Hop 2: the target, through a TCP forward on the bastion connection
	client, err := dialThroughBastion(bastionClient, address, sshConfig, connectTimeout)
	if err != nil {
		bastionClient.Close()
		return explainBastionHopError(e.host, address, *authState, err)
	}
	e.bastionClient = bastionClient
	e.client = client
	return nil
}

func explainSSHConnectError(host Host, address string, authState sshAuthAttemptState, cause error) error {
	if explained := explainHostKeyFailure(host, cause); explained != cause {
		return fmt.Errorf("failed to connect to SSH server: %w", explained)
	}
	if host.Password != "" && authState.publicKeyOffered && !authState.passwordAttempted {
		if host.PemFile == "" {
			return fmt.Errorf(
				"failed to connect to SSH server: SSH authentication failed for user %q at %s: "+
					"the server offered public-key authentication but did not offer password authentication at this stage. "+
					"This host is configured with a password and no PEM key. Configure a PEM key authorized for this SSH account, "+
					"or enable password authentication on the SSH server. Original SSH error: %w",
				host.Username, address, cause,
			)
		}

		return fmt.Errorf(
			"failed to connect to SSH server: SSH authentication failed for user %q at %s: "+
				"the server offered public-key authentication, but the configured PEM key did not authenticate and "+
				"password authentication was not offered at this stage. Verify that the PEM key is authorized for this SSH account, "+
				"or enable password authentication on the SSH server. Original SSH error: %w",
			host.Username, address, cause,
		)
	}

	return fmt.Errorf("failed to connect to SSH server: %w", cause)
}

func (e *SSHExecutor) Execute(command string, logFile *os.File, maxOutputSize int64) (string, bool, error) {
	if e.client == nil {
		return "", false, fmt.Errorf("not connected")
	}

	// Check if command is a script (starts with shebang) and translate it for SSH
	if strings.HasPrefix(command, "#!") {
		command = translateScriptToHeredoc(command)
	}

	session, err := e.client.NewSession()
	if err != nil {
		return "", false, fmt.Errorf("failed to create SSH session: %v", err)
	}
	defer session.Close()

	// Use LimitedBuffer to prevent OOM
	allOutput := NewLimitedBuffer(maxOutputSize)

	// Create a writer that writes to both log file and limited buffer
	multiWriter := io.MultiWriter(logFile, allOutput)

	session.Stdout = multiWriter
	session.Stderr = multiWriter

	err = session.Run(command)
	if err != nil {
		return allOutput.String(), allOutput.overflow, fmt.Errorf("command exited with error: %v", err)
	}

	return allOutput.String(), allOutput.overflow, nil
}

func (e *SSHExecutor) Close() error {
	var err error
	if e.client != nil {
		err = e.client.Close()
	}
	if e.bastionClient != nil {
		// Closed after the target: closing the bastion first would make the target
		// connection fail with a misleading EOF.
		if closeErr := e.bastionClient.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}

func (e *SSHExecutor) GetHost() string {
	return e.host.Host
}

// WinRMExecutor implementation
func (e *WinRMExecutor) Connect() error {
	// Determine endpoint URL
	var endpoint *winrm.Endpoint

	if e.host.WinRMHTTPS {
		endpoint = &winrm.Endpoint{
			Host:     e.host.Host,
			Port:     e.host.Port,
			HTTPS:    true,
			Insecure: e.host.WinRMInsecure,
		}

		// Set CA cert if provided
		if e.host.WinRMCACert != "" {
			cacert, err := ioutil.ReadFile(e.host.WinRMCACert)
			if err != nil {
				return fmt.Errorf("failed to read CA certificate: %v", err)
			}
			endpoint.CACert = cacert
		}
	} else {
		endpoint = &winrm.Endpoint{
			Host: e.host.Host,
			Port: e.host.Port,
		}
	}

	// Create WinRM client with Basic authentication
	params := winrm.DefaultParameters
	params.Timeout = "PT30S" // 30 second timeout

	client, err := winrm.NewClientWithParameters(endpoint, e.host.Username, e.host.Password, params)
	if err != nil {
		return fmt.Errorf("failed to create WinRM client: %v", err)
	}

	e.client = client

	// Create a shell for command execution
	shell, err := client.CreateShell()
	if err != nil {
		return fmt.Errorf("failed to create WinRM shell: %v", err)
	}
	e.shell = shell

	return nil
}

func (e *WinRMExecutor) Execute(command string, logFile *os.File, maxOutputSize int64) (string, bool, error) {
	if e.shell == nil {
		return "", false, fmt.Errorf("not connected")
	}

	// Use LimitedBuffer to prevent OOM
	allOutput := NewLimitedBuffer(maxOutputSize)

	// Create a writer that writes to both log file and limited buffer
	multiWriter := io.MultiWriter(logFile, allOutput)

	// Execute command
	cmd, err := e.shell.Execute(command)
	if err != nil {
		return "", false, fmt.Errorf("failed to execute command: %v", err)
	}

	// Read stdout and stderr with WaitGroup to prevent goroutine leaks
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(multiWriter, cmd.Stdout) }()
	go func() { defer wg.Done(); io.Copy(multiWriter, cmd.Stderr) }()

	// Wait for command to complete, then wait for I/O goroutines to finish
	cmd.Wait()
	wg.Wait()

	// Check exit code
	if cmd.ExitCode() != 0 {
		return allOutput.String(), allOutput.overflow, fmt.Errorf("command exited with code %d", cmd.ExitCode())
	}

	return allOutput.String(), allOutput.overflow, nil
}

func (e *WinRMExecutor) Close() error {
	if e.shell != nil {
		e.shell.Close()
	}
	// WinRM client doesn't have a Close method
	return nil
}

func (e *WinRMExecutor) GetHost() string {
	return e.host.Host
}

// substituteVariables performs both environment and config variable substitution
// Processing order: ${{ENV_VARS}} first, then {{config_vars}}
// Returns error if any undefined variables remain after substitution
func substituteVariables(jsonStr string, variables map[string]interface{}) (string, error) {
	result := jsonStr

	// First, substitute environment variables ${{VAR_NAME}}
	// New syntax to avoid conflicts with bash ${var} syntax
	envPattern := regexp.MustCompile(`\$\{\{([^}]+)\}\}`)
	result = envPattern.ReplaceAllStringFunc(result, func(match string) string {
		varName := match[3 : len(match)-2] // Remove ${{ and }}
		if value := os.Getenv(varName); value != "" {
			fmt.Printf("[CONFIG] Substituting environment variable ${{%s}}\n", varName)
			return value
		}
		// Return original if env var not found - will be caught in validation
		return match
	})

	// Then, substitute config variables {{var_name}}
	if variables != nil {
		configPattern := regexp.MustCompile(`\{\{([^}]+)\}\}`)
		substitutionCount := 0

		result = configPattern.ReplaceAllStringFunc(result, func(match string) string {
			varName := match[2 : len(match)-2] // Remove {{ and }}
			if value, exists := variables[varName]; exists {
				substitutionCount++
				// Convert value to string representation
				switch v := value.(type) {
				case string:
					return v
				case float64:
					// JSON numbers come as float64
					if v == float64(int(v)) {
						return fmt.Sprintf("%d", int(v))
					}
					return fmt.Sprintf("%g", v)
				case bool:
					return fmt.Sprintf("%v", v)
				case []interface{}:
					// For arrays, return JSON representation
					bytes, _ := json.Marshal(v)
					return string(bytes)
				default:
					return fmt.Sprintf("%v", v)
				}
			}
			// Return original if variable not found - will be caught in validation
			return match
		})

		if substitutionCount > 0 {
			fmt.Printf("[CONFIG] Substituted %d config variable(s)\n", substitutionCount)
		}
	}

	// Validate that no undefined variables remain (excluding email template variables)
	// Email templates use {{var}} syntax but those are template variables, not config variables
	if matches := envPattern.FindAllString(result, -1); len(matches) > 0 {
		return "", fmt.Errorf("undefined environment variables: %v", matches)
	}

	// For config variables, exclude common email template variables
	configVarPattern := regexp.MustCompile(`\{\{([^}]+)\}\}`)
	remainingMatches := configVarPattern.FindAllStringSubmatch(result, -1)
	var undefinedVars []string

	for _, match := range remainingMatches {
		if len(match) > 1 {
			varName := match[1]
			// Skip template variables commonly used in email subjects
			templateVars := []string{"status", "cmd_id", "host", "trace_id", "runbook_id", "run_id", "request_id", "Status", "TraceID",
				"PrevTraceID", "CmdID", "Host", "Command", "ExecTime", "RequestReceivedAt",
				"RemoteIP", "Agent", "AccessURL", "Output", "ErrorMsg", "ExitCode", "Error",
				"OutputTruncated", "IncludeOutput", "window_label", "period", "window_start", "window_end",
				// disk_monitor.email_subject placeholders (diskEmailSubjectPlaceholders)
				"summary", "type", "tripped", "failed", "ok", "volumes", "hosts", "app_host"}
			isTemplateVar := false
			for _, tv := range templateVars {
				if varName == tv {
					isTemplateVar = true
					break
				}
			}
			if !isTemplateVar {
				undefinedVars = append(undefinedVars, match[0])
			}
		}
	}

	if len(undefinedVars) > 0 {
		return "", fmt.Errorf("undefined config variables: %v", undefinedVars)
	}

	return result, nil
}

// loadIfChanged checks if config file has changed and reloads if necessary
// Uses file hash to detect changes, performs variable substitution, and updates cron jobs
// This enables hot-reload without service restart (except for port/bind_address changes)
func (c *ConfigCache) loadIfChanged() error {
	if c.configStatUnchanged() {
		return nil // same mtime and size as the loaded config: skip the read + hash
	}
	currentHash, err := calculateFileHash(c.configFile)
	if err != nil {
		return fmt.Errorf("failed to calculate file hash: %v", err)
	}
	currentStat, statErr := os.Stat(c.configFile)

	c.RLock()
	if c.lastHash == currentHash {
		c.RUnlock()
		return nil // Config unchanged, no need to log
	}
	c.RUnlock()

	data, err := ioutil.ReadFile(c.configFile)
	if err != nil {
		return fmt.Errorf("failed to read config file: %v", err)
	}
	if err := validateNoLegacyWorkflowFields(data); err != nil {
		return err
	}

	// First pass: parse to get variables
	var tempConfig struct {
		Variables map[string]interface{} `json:"variables"`
	}
	if err := json.Unmarshal(data, &tempConfig); err != nil {
		return fmt.Errorf("failed to parse variables from config: %v", err)
	}

	// Perform variable substitutions on the raw JSON string
	jsonStr := string(data)
	substitutedJSON, err := substituteVariables(jsonStr, tempConfig.Variables)
	if err != nil {
		return fmt.Errorf("variable substitution failed: %v", err)
	}

	// Second pass: parse the substituted JSON
	var newConfig Config
	err = json.Unmarshal([]byte(substitutedJSON), &newConfig)
	if err != nil {
		return fmt.Errorf("failed to parse substituted config: %v", err)
	}
	if newConfig.ScheduledJobsOverviewEmail != nil {
		normalized := normalizeScheduledJobsOverviewEmailConfig(newConfig.ScheduledJobsOverviewEmail)
		if err := validateScheduledJobsOverviewEmailConfig(normalized, newConfig.SMTP, false, newConfig.Variables); err != nil {
			return fmt.Errorf("invalid Scheduled Jobs Overview email configuration: %v", err)
		}
		newConfig.ScheduledJobsOverviewEmail = &normalized
	}

	// Validate critical configuration elements that could cause runtime failures
	for hostName, host := range newConfig.Hosts {
		// Normalize host for validation
		normalizeHost(&host)

		if strings.ToLower(host.ConnectionType) == "winrm" {
			// WinRM validation
			if host.Username == "" || host.Password == "" {
				return fmt.Errorf("host '%s' (WinRM) requires username and password", hostName)
			}

			// Check CA cert file if specified
			if host.WinRMCACert != "" {
				if _, err := os.Stat(host.WinRMCACert); os.IsNotExist(err) {
					return fmt.Errorf("host '%s' references non-existent CA certificate: %s", hostName, host.WinRMCACert)
				}
			}
		} else {
			// SSH validation
			// Check PEM files exist for hosts that use them
			if host.PemFile != "" {
				if _, err := os.Stat(host.PemFile); os.IsNotExist(err) {
					return fmt.Errorf("host '%s' references non-existent PEM file: %s", hostName, host.PemFile)
				}
			}

			// Validate that host has at least one auth method
			if host.Password == "" && host.PemFile == "" {
				return fmt.Errorf("host '%s' has no authentication method (neither password nor PEM key)", hostName)
			}
		}
	}
	if problems, _ := hostKeyConfigProblems(&newConfig); len(problems) > 0 {
		return errors.New(problems[0])
	}
	// Validate bastion references and attach the resolved bastion to each host
	if err := resolveBastionHosts(newConfig.Hosts); err != nil {
		return err
	}

	// Validate command references
	cmdIDs := make(map[string]bool)
	hasEmailNotifications := false
	for _, cmd := range newConfig.Commands {
		cmdIDs[cmd.CmdID] = true
		if cmd.LogStorage != "" &&
			!strings.EqualFold(cmd.LogStorage, "local") &&
			!strings.EqualFold(cmd.LogStorage, "s3") {
			return fmt.Errorf("command '%s' has invalid log_storage '%s' (valid: local, s3)", cmd.CmdID, cmd.LogStorage)
		}
		if cmd.NotifyEmail != nil {
			hasEmailNotifications = true
		}
	}

	requestIDs, err := validateHTTPRequests(newConfig.HTTPRequests, cmdIDs)
	if err != nil {
		return err
	}
	for _, req := range newConfig.HTTPRequests {
		if req.NotifyEmail != nil {
			hasEmailNotifications = true
		}
	}

	if err := validateRunBooks(newConfig.RunBooks, cmdIDs, requestIDs); err != nil {
		return err
	}
	if err := validateVNCSettings(newConfig.VNC); err != nil {
		return err
	}
	if err := validateVNCConnections(newConfig.VNCConnections, newConfig.Hosts); err != nil {
		return err
	}
	if err := validateSecurityConfig(newConfig.Users, newConfig.TrustedProxies, newConfig.Commands, newConfig.RunBooks, newConfig.HTTPRequests, newConfig.Hosts, newConfig.VNCConnections); err != nil {
		return err
	}
	if err := validateTerminalsConfig(newConfig.Terminals); err != nil {
		return err
	}
	if err := validateAgentSessionsConfig(&newConfig); err != nil {
		return err
	}
	if newConfig.DiskMonitor != nil {
		normalizedDisk := normalizeDiskMonitorConfig(newConfig.DiskMonitor)
		if err := validateDiskMonitorConfig(normalizedDisk, newConfig.Hosts); err != nil {
			return fmt.Errorf("invalid disk_monitor configuration: %v", err)
		}
		newConfig.DiskMonitor = &normalizedDisk
	}

	for _, cmd := range newConfig.Commands {
		// Check if host_ref exists
		if _, exists := newConfig.Hosts[cmd.HostRef]; !exists {
			return fmt.Errorf("command '%s' references non-existent host '%s'", cmd.CmdID, cmd.HostRef)
		}

	}

	// Check if email.tpl exists when email notifications are configured
	if hasEmailNotifications && newConfig.SMTP != nil {
		if _, err := os.Stat("email.tpl"); os.IsNotExist(err) {
			return fmt.Errorf("email notifications are configured but email.tpl template file is missing")
		}
	}

	c.Lock()
	c.config = &newConfig
	c.lastHash = currentHash
	if statErr == nil {
		c.lastModTime = currentStat.ModTime()
		c.lastSize = currentStat.Size()
	} else {
		c.lastModTime = time.Time{}
		c.lastSize = 0
	}
	c.Unlock()

	// Update auth manager with new credentials if changed
	if authManager != nil {
		authManager.UpdateCredentials(newConfig.AdminPassword, newConfig.TOTPSecret, newConfig.DevMode, newConfig.Users, newConfig.TrustedProxies)
	}

	// Update cron jobs when config changes
	updateCronJobs(&newConfig)

	return nil
}

func updateCronJobs(config *Config) {
	scheduledJobsMutex.Lock()
	defer scheduledJobsMutex.Unlock()

	// Remove all existing jobs first
	for cmdID, entryID := range scheduledJobs {
		cronScheduler.Remove(entryID)
		// Only log if we're actually removing jobs
		if config.CronEnabled != "N" {
			fmt.Printf("[CRON] Removed scheduled job for command: %s\n", cmdID)
		}
	}
	scheduledJobs = make(map[string]cron.EntryID)
	if scheduledJobsOverviewEmailEntryID != 0 {
		cronScheduler.Remove(scheduledJobsOverviewEmailEntryID)
		scheduledJobsOverviewEmailEntryID = 0
	}

	if appShutdown.IsShuttingDown() {
		fmt.Println("[CRON] Skipping cron job update because shutdown is in progress")
		return
	}

	if cronScheduler == nil {
		return
	}

	// The daily overview email is a reporting task and remains active even when
	// Command and RunBook cron execution is globally disabled.
	scheduleScheduledJobsOverviewEmail(config)

	// Check if cron is enabled (defaults to "Y" if not specified)
	cronEnabled := config.CronEnabled != "N"

	if !cronEnabled {
		fmt.Println("[CRON] Cron scheduling is DISABLED (cron_enabled: N)")
		return
	}

	fmt.Println("[CRON] Cron scheduling is ENABLED - Setting up scheduled jobs...")

	// Add new jobs based on config
	jobCount := 0

	// Schedule commands
	for _, cmd := range config.Commands {
		if cmd.CronSchedule != "" {
			// Create a closure to capture the cmd_id
			cmdID := cmd.CmdID
			entryID, err := cronScheduler.AddFunc(cmd.CronSchedule, func() {
				executeScheduledCommand(cmdID)
			})

			if err != nil {
				fmt.Printf("[CRON] Failed to schedule command %s with schedule '%s': %v\n",
					cmd.CmdID, cmd.CronSchedule, err)
			} else {
				scheduledJobs[cmd.CmdID] = entryID
				fmt.Printf("[CRON] Scheduled command %s (%s) with schedule: %s\n",
					cmd.CmdID, cmd.Description, cmd.CronSchedule)
				jobCount++
			}
		}
	}

	// Schedule RunBooks.
	for _, runBook := range config.RunBooks {
		if runBook.CronSchedule == "" {
			continue
		}
		runBookID := runBook.RunBookID
		entryID, err := cronScheduler.AddFunc(runBook.CronSchedule, func() {
			executeScheduledRunBook(runBookID)
		})
		if err != nil {
			fmt.Printf("[CRON] Failed to schedule RunBook %s with schedule '%s': %v\n", runBook.RunBookID, runBook.CronSchedule, err)
		} else {
			scheduledJobs["runbook:"+runBook.RunBookID] = entryID
			fmt.Printf("[CRON] Scheduled RunBook %s (%s) with schedule: %s\n", runBook.RunBookID, runBook.Description, runBook.CronSchedule)
			jobCount++
		}
	}

	// Schedule HTTP requests.
	for _, req := range config.HTTPRequests {
		if req.CronSchedule == "" {
			continue
		}
		requestID := req.RequestID
		entryID, err := cronScheduler.AddFunc(req.CronSchedule, func() {
			executeScheduledHTTPRequest(requestID)
		})
		if err != nil {
			fmt.Printf("[CRON] Failed to schedule HTTP request %s with schedule '%s': %v\n", req.RequestID, req.CronSchedule, err)
		} else {
			scheduledJobs["http:"+req.RequestID] = entryID
			fmt.Printf("[CRON] Scheduled HTTP request %s (%s) with schedule: %s\n", req.RequestID, req.Description, req.CronSchedule)
			jobCount++
		}
	}

	if jobCount > 0 {
		fmt.Printf("[CRON] Total %d scheduled jobs active\n", jobCount)
	} else {
		fmt.Println("[CRON] No commands, RunBooks or HTTP requests with cron_schedule found")
	}
}

type CommandConfig struct {
	Command Command
	Host    Host
}

type WebhookData struct {
	Output            string `json:"output"`
	ErrorMsg          string `json:"error_msg"`
	ExitCode          int    `json:"exitcode"`
	TraceID           string `json:"trace_id"`
	RequestReceivedAt string `json:"request_received_at"`
	CmdID             string `json:"cmd_id"`
	Host              string `json:"host"`
	Command           string `json:"command"`
	RemoteIP          string `json:"remote_ip"`
	Agent             string `json:"agent"`
	ExecTime          string `json:"exec_time"`
	HTTPStatus        int    `json:"http_status,omitempty"` // HTTP requests only
}

func (c *ConfigCache) findCommandConfig(cmdID string) (*CommandConfig, error) {
	err := c.loadIfChanged()
	if err != nil {
		return nil, err
	}

	c.RLock()
	defer c.RUnlock()

	for _, cmd := range c.config.Commands {
		if cmd.CmdID == cmdID {
			host, exists := c.config.Hosts[cmd.HostRef]
			if !exists {
				return nil, fmt.Errorf("host '%s' not found for command ID: %s", cmd.HostRef, cmdID)
			}
			return &CommandConfig{
				Command: cmd,
				Host:    host,
			}, nil
		}
	}
	return nil, fmt.Errorf("command not found for ID: %s", cmdID)
}

func loadPEMSigner(pemPath string, pemPassword string) (ssh.Signer, error) {
	pemBytes, err := ioutil.ReadFile(pemPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read PEM file: %v", err)
	}

	var signer ssh.Signer
	if pemPassword != "" {
		// PEM is encrypted, decrypt it first
		signer, err = ssh.ParsePrivateKeyWithPassphrase(pemBytes, []byte(pemPassword))
		if err != nil {
			return nil, fmt.Errorf("failed to parse encrypted PEM key: %v", err)
		}
	} else {
		// PEM is not encrypted
		signer, err = ssh.ParsePrivateKey(pemBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse PEM key: %v", err)
		}
	}

	return signer, nil
}

// LimitedBuffer captures output up to maxSize. stdout and stderr are copied into
// the same buffer from separate goroutines, so writes are serialised.
type LimitedBuffer struct {
	mu       sync.Mutex
	buf      *bytes.Buffer
	maxSize  int64
	curSize  int64
	overflow bool
}

func NewLimitedBuffer(maxSize int64) *LimitedBuffer {
	return &LimitedBuffer{
		buf:     &bytes.Buffer{},
		maxSize: maxSize,
		curSize: 0,
	}
}

func (lb *LimitedBuffer) Write(p []byte) (n int, err error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	writeSize := int64(len(p))
	if lb.curSize+writeSize > lb.maxSize {
		// Would exceed limit
		remaining := lb.maxSize - lb.curSize
		if remaining > 0 {
			// Write what we can
			lb.buf.Write(p[:remaining])
			lb.curSize = lb.maxSize
		}
		lb.overflow = true
		return len(p), nil // Pretend we wrote it all to avoid SSH errors
	}

	n, err = lb.buf.Write(p)
	lb.curSize += int64(n)
	return n, err
}

func (lb *LimitedBuffer) String() string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if lb.overflow {
		return lb.buf.String() + "\n[OUTPUT TRUNCATED - EXCEEDED MAX SIZE]"
	}
	return lb.buf.String()
}

func parseSize(sizeStr string) (int64, error) {
	if sizeStr == "" {
		// Default to 2MB when not specified
		return 2 * 1024 * 1024, nil
	}

	sizeStr = strings.TrimSpace(strings.ToUpper(sizeStr))

	// Extract numeric part and unit
	var numStr string
	var unit string

	for i, ch := range sizeStr {
		if ch >= '0' && ch <= '9' || ch == '.' {
			numStr += string(ch)
		} else {
			unit = sizeStr[i:]
			break
		}
	}

	if numStr == "" {
		return 0, fmt.Errorf("invalid size format: %s", sizeStr)
	}

	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number in size: %s", sizeStr)
	}

	// Convert to bytes based on unit
	var multiplier float64
	switch strings.TrimSpace(unit) {
	case "B", "":
		multiplier = 1
	case "KB", "K":
		multiplier = 1024
	case "MB", "M":
		multiplier = 1024 * 1024
	case "GB", "G":
		multiplier = 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown size unit: %s", unit)
	}

	return int64(num * multiplier), nil
}

func executeScheduledCommand(cmdID string) {
	if !appShutdown.BeginWork() {
		fmt.Printf("[CRON] Skipping command %s because shutdown is in progress\n", cmdID)
		return
	}
	defer appShutdown.DoneWork()
	if err := configRestoreGate.BeginWork(); err != nil {
		fmt.Printf("[CRON] Skipping command %s: %v\n", cmdID, err)
		return
	}
	defer configRestoreGate.EndWork()

	// Increment cron counter
	atomic.AddInt64(&totalCronRequests, 1)

	// Generate trace ID for scheduled execution
	traceID := generateTraceID()
	requestStart := time.Now()

	// Check skip_if_running before acquiring lock
	cmdConfig, err := cache.findCommandConfig(cmdID)
	if err != nil {
		return
	}
	if cmdConfig.Command.SkipIfRunning && execManager.isCommandActive(cmdID) {
		fmt.Printf("[CRON] Skipping %s - already running (skip_if_running enabled)\n", cmdID)
		return
	}

	// Get the lock for this cmd_id
	cmdLock := execManager.getLock(cmdID)
	cmdLock.Lock()

	if appShutdown.IsShuttingDown() {
		cmdLock.Unlock()
		fmt.Printf("[CRON] Skipping %s because shutdown started before execution\n", cmdID)
		return
	}

	// Re-fetch config after acquiring lock (may have changed)
	cmdConfig, err = cache.findCommandConfig(cmdID)
	if err != nil {
		cmdLock.Unlock()
		return
	}

	// Create log file
	logFile, logFilename, err := createCommandLogFile(cmdID, traceID)
	if err != nil {
		cmdLock.Unlock()
		return
	}
	defer logFile.Close()

	// Write log header
	fmt.Fprintf(logFile, "               trace_id : %s\n", traceID)
	fmt.Fprintf(logFile, "    request_received_at : %s\n", requestStart.Format("2006-01-02 15:04:05.000"))
	fmt.Fprintf(logFile, "                 cmd_id : %s\n", cmdID)
	fmt.Fprintf(logFile, "                   host : %s\n", cmdConfig.Host.Host)
	fmt.Fprintf(logFile, "              remote_ip : cron-scheduler\n")
	fmt.Fprintf(logFile, "                  agent : cron-scheduler\n")
	fmt.Fprintf(logFile, "             invoked_by : scheduler\n")
	fmt.Fprintf(logFile, "                trigger : schedule\n")
	writeRunningLogStatus(logFile)

	// Track execution
	activeReq := &ActiveRequest{
		TraceID:   traceID,
		CmdID:     cmdID,
		Host:      cmdConfig.Host.Host,
		Command:   cmdConfig.Command.Command,
		StartTime: requestStart,
		RemoteIP:  "cron-scheduler",
	}
	execManager.startExecution(activeReq)
	defer func() {
		execManager.endExecution(cmdID, traceID)
		cmdLock.Unlock()
	}()

	startTime := time.Now()
	output, isTruncated, err, _ := runCommandWithTimeout(context.Background(), cmdConfig, logFile)
	execDuration := time.Since(startTime)

	// Send webhook notification
	var exitCode int
	var errorMsg string
	if err != nil {
		exitCode = 1
		errorMsg = err.Error()
	} else {
		exitCode = 0
		errorMsg = ""
	}
	updateExecutionLogStatus(logFile, logFilename, completedExecutionLogStatus(execDuration, isTruncated, err, exitCode))

	// Send webhook notification if configured
	if cmdConfig.Command.NotifyWebhook != "" {
		webhookData := WebhookData{
			Output:            output, // Already truncated by executeSSHCommand
			ErrorMsg:          errorMsg,
			ExitCode:          exitCode,
			TraceID:           traceID,
			RequestReceivedAt: requestStart.Format("2006-01-02 15:04:05.000"),
			CmdID:             cmdID,
			Host:              cmdConfig.Host.Host,
			Command:           cmdConfig.Command.Command,
			RemoteIP:          "cron-scheduler",
			Agent:             "cron-scheduler",
			ExecTime:          fmt.Sprintf("%.3fs", execDuration.Seconds()),
		}
		appShutdown.RunAsync("webhook notification "+traceID, func() {
			sendWebhookNotification(cmdConfig.Command.NotifyWebhook, webhookData)
		})
	}

	// Send email notification if configured
	if cmdConfig.Command.NotifyEmail != nil {
		cache.RLock()
		smtp := cache.config.SMTP
		cache.RUnlock()

		if smtp != nil {
			webhookData := WebhookData{
				Output:            output,
				ErrorMsg:          errorMsg,
				ExitCode:          exitCode,
				TraceID:           traceID,
				RequestReceivedAt: requestStart.Format("2006-01-02 15:04:05.000"),
				CmdID:             cmdID,
				Host:              cmdConfig.Host.Host,
				Command:           cmdConfig.Command.Command,
				RemoteIP:          "cron-scheduler",
				Agent:             "cron-scheduler",
				ExecTime:          fmt.Sprintf("%.3fs", execDuration.Seconds()),
			}
			emailData := prepareEmailData(webhookData, cache.config, isTruncated)
			emailData.IncludeOutput = cmdConfig.Command.NotifyEmail.IncludeOutput
			queueEmail(emailData, cmdConfig.Command.NotifyEmail, smtp)
		}
	}

	finalizeCommandLog(cmdConfig, logFile, logFilename)

}

// executeRemoteCommand selects SSH or WinRM for one atomic Command execution.
func executeRemoteCommand(cmdConfig *CommandConfig, logFile *os.File) (string, bool, error) {
	// Parse max output size if specified
	maxOutputSize, err := parseSize(cmdConfig.Command.MaxOutputSize)
	if err != nil {
		return "", false, fmt.Errorf("invalid max_output_size: %v", err)
	}

	// Create the appropriate executor based on host configuration
	executor, err := createExecutor(cmdConfig.Host)
	if err != nil {
		return "", false, fmt.Errorf("failed to create executor: %v", err)
	}
	defer executor.Close()

	// Connect to the remote host
	err = executor.Connect()
	if err != nil {
		return "", false, fmt.Errorf("failed to connect: %v", err)
	}

	// Execute the command
	output, truncated, err := executor.Execute(cmdConfig.Command.Command, logFile, maxOutputSize)
	return output, truncated, err
}

// executeSSHCommand is kept for backward compatibility, redirects to executeRemoteCommand
func executeSSHCommand(cmdConfig *CommandConfig, logFile *os.File) (string, bool, error) {
	return executeRemoteCommand(cmdConfig, logFile)
}

// runCommandWithTimeout runs one Command under parent and, when the Command has its own
// timeout_seconds, under that limit as well. When the Command's own limit fires while
// parent is still alive, the returned error carries the plain-language explanation and
// timedOut is true; a cancellation coming from parent is passed through untouched so the
// caller (e.g. a RunBook step) can explain it with its own wording.
func runCommandWithTimeout(parent context.Context, cmdConfig *CommandConfig, logFile *os.File) (output string, truncated bool, err error, timedOut bool) {
	ctx := parent
	limit := cmdConfig.Command.TimeoutSeconds
	if limit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, time.Duration(limit)*time.Second)
		defer cancel()
	}
	started := time.Now()
	output, truncated, err = executeRemoteCommandContext(ctx, cmdConfig, logFile)
	if err != nil && limit > 0 && ctx.Err() != nil && parent.Err() == nil {
		timedOut = true
		err = errors.New(describeTimeout("command", limit, time.Since(started)))
	}
	return output, truncated, err, timedOut
}

// EmailData represents the data for email template
type EmailData struct {
	Status            string
	TraceID           string
	PrevTraceID       string
	CmdID             string
	Host              string
	Command           string
	ExecTime          string
	RequestReceivedAt string
	RemoteIP          string
	Agent             string
	AccessURL         string
	IncludeOutput     bool
	Output            string
	OutputTruncated   bool
	Error             bool
	ErrorMsg          string
	ExitCode          int
}

// MailQueueItem represents an email to be sent
type MailQueueItem struct {
	ID        string             `json:"id"`
	Timestamp time.Time          `json:"timestamp"`
	EmailData EmailData          `json:"email_data"`
	Config    *EmailNotification `json:"config"`
	SMTP      *SMTPConfig        `json:"smtp"`
}

func truncateOutputByLines(output string, maxLines int) string {
	if maxLines <= 0 {
		return output
	}

	lines := strings.Split(output, "\n")
	if len(lines) <= maxLines {
		return output
	}

	truncated := strings.Join(lines[:maxLines], "\n")
	return truncated + fmt.Sprintf("\n... [Output truncated - showing first %d lines]", maxLines)
}

func replaceTemplateVars(template string, data EmailData) string {
	// Simple string replacement for email subject
	result := strings.ReplaceAll(template, "{{status}}", data.Status)
	result = strings.ReplaceAll(result, "{{cmd_id}}", data.CmdID)
	result = strings.ReplaceAll(result, "{{request_id}}", data.CmdID)
	result = strings.ReplaceAll(result, "{{host}}", data.Host)
	result = strings.ReplaceAll(result, "{{trace_id}}", data.TraceID)
	return result
}

func generateEmailBody(data EmailData) (string, error) {
	// Read the email template
	templateContent, err := ioutil.ReadFile("email.tpl")
	if err != nil {
		return "", fmt.Errorf("failed to read email.tpl: %v", err)
	}

	// Parse and execute template
	tmpl, err := template.New("email").Parse(string(templateContent))
	if err != nil {
		return "", fmt.Errorf("failed to parse email template: %v", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute email template: %v", err)
	}

	return buf.String(), nil
}

func queueEmail(emailData EmailData, config *EmailNotification, smtp *SMTPConfig) {
	if smtp == nil || config == nil || !smtp.isEnabled() {
		return // No email configuration
	}

	// Check if we should send this email based on success/failure
	if emailData.Error && !config.OnFailure {
		return // Don't send on failure
	}
	if !emailData.Error && !config.OnSuccess {
		return // Don't send on success
	}

	// Create mail queue directory if it doesn't exist
	if err := os.MkdirAll("mail_queue", privateDirMode); err != nil {
		logSMTPError(fmt.Sprintf("Failed to create mail_queue directory: %v", err))
		return
	}

	// Create queue item
	item := MailQueueItem{
		ID:        generateTraceID(),
		Timestamp: time.Now(),
		EmailData: emailData,
		Config:    config,
		SMTP:      smtp,
	}

	// Save to queue
	queueFile := fmt.Sprintf("mail_queue/%s_%s.json", time.Now().Format("20060102_150405"), item.ID)
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		logSMTPError(fmt.Sprintf("Failed to marshal email queue item: %v", err))
		return
	}

	// The queue item embeds the SMTP settings, password included: owner-only.
	if err := ioutil.WriteFile(queueFile, data, privateFileMode); err != nil {
		logSMTPError(fmt.Sprintf("Failed to write email queue file: %v", err))
		return
	}

	// Process the email asynchronously
	appShutdown.RunAsync("email queue "+item.ID, func() {
		processEmailQueue(queueFile)
	})
}

func processEmailQueue(queueFile string) {
	// Read queue item
	data, err := ioutil.ReadFile(queueFile)
	if err != nil {
		logSMTPError(fmt.Sprintf("Failed to read queue file %s: %v", queueFile, err))
		return
	}

	var item MailQueueItem
	if err := json.Unmarshal(data, &item); err != nil {
		logSMTPError(fmt.Sprintf("Failed to unmarshal queue item: %v", err))
		os.Remove(queueFile) // Remove corrupt file
		return
	}

	// Send the email
	if err := sendEmail(item); err != nil {
		logSMTPError(fmt.Sprintf("Failed to send email for trace_id %s: %v", item.EmailData.TraceID, err))
	}

	// Remove queue file after processing (success or failure)
	os.Remove(queueFile)
}

func sendEmail(item MailQueueItem) error {
	// Prepare email data
	emailData := item.EmailData

	// Truncate output if needed
	if item.Config.IncludeOutput && item.Config.MaxOutputLines > 0 {
		emailData.Output = truncateOutputByLines(emailData.Output, item.Config.MaxOutputLines)
	}

	// Generate email body
	body, err := generateEmailBody(emailData)
	if err != nil {
		return err
	}

	// Prepare subject with template variables
	subject := replaceTemplateVars(item.Config.Subject, emailData)

	// Create email message
	m := gomail.NewMessage()
	m.SetHeader("To", item.Config.To...)
	if len(item.Config.CC) > 0 {
		m.SetHeader("Cc", item.Config.CC...)
	}
	m.SetHeader("Subject", subject)
	m.SetBody("text/plain", body)

	if err := sendSMTPMessage(item.SMTP, m); err != nil {
		return err
	}

	fmt.Printf("[EMAIL] Successfully sent email for trace_id: %s to %v\n",
		emailData.TraceID, item.Config.To)
	return nil
}

func logSMTPError(message string) {
	// Create logs directory if it doesn't exist
	os.MkdirAll("logs", privateDirMode)

	// Open or create the SMTP error log file
	logFile, err := os.OpenFile("logs/errors_smtp.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, privateFileMode)
	if err != nil {
		fmt.Printf("[SMTP ERROR] Failed to open log file: %v\n", err)
		return
	}
	defer logFile.Close()

	// Write the error with timestamp
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	fmt.Fprintf(logFile, "[%s] %s\n", timestamp, message)
}

func prepareEmailData(webhookData WebhookData, config *Config, isTruncated bool) EmailData {
	status := "SUCCESS"
	if webhookData.ExitCode != 0 {
		status = "FAILURE"
	}

	accessURL := ""
	if config != nil {
		accessURL = config.AccessURL
	}

	return EmailData{
		Status:            status,
		TraceID:           webhookData.TraceID,
		PrevTraceID:       "", // Will be set by caller if available
		CmdID:             webhookData.CmdID,
		Host:              webhookData.Host,
		Command:           webhookData.Command,
		ExecTime:          webhookData.ExecTime,
		RequestReceivedAt: webhookData.RequestReceivedAt,
		RemoteIP:          webhookData.RemoteIP,
		Agent:             webhookData.Agent,
		AccessURL:         accessURL,
		IncludeOutput:     true, // Will be controlled by email config
		Output:            webhookData.Output,
		OutputTruncated:   isTruncated,
		Error:             webhookData.ExitCode != 0,
		ErrorMsg:          webhookData.ErrorMsg,
		ExitCode:          webhookData.ExitCode,
	}
}

func sendWebhookNotification(webhookURL string, data WebhookData) {
	if webhookURL == "" {
		return
	}

	formData := url.Values{}
	formData.Set("output", data.Output)
	formData.Set("error_msg", data.ErrorMsg)
	formData.Set("exitcode", fmt.Sprintf("%d", data.ExitCode))
	formData.Set("trace_id", data.TraceID)
	formData.Set("request_received_at", data.RequestReceivedAt)
	formData.Set("cmd_id", data.CmdID)
	formData.Set("host", data.Host)
	formData.Set("command", data.Command)
	formData.Set("remote_ip", data.RemoteIP)
	formData.Set("agent", data.Agent)
	formData.Set("exec_time", data.ExecTime)
	if data.HTTPStatus != 0 {
		formData.Set("http_status", fmt.Sprintf("%d", data.HTTPStatus))
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.PostForm(webhookURL, formData)
	if err != nil {
		fmt.Printf("[WEBHOOK] Failed to send notification to %s: %v\n", webhookURL, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Printf("[WEBHOOK] Successfully sent notification to %s (status: %d)\n", webhookURL, resp.StatusCode)
	} else {
		fmt.Printf("[WEBHOOK] Webhook returned non-success status %d for %s\n", resp.StatusCode, webhookURL)
	}
}

// isAllowedOrigin checks if the given origin is permitted for CORS.
// Allowed origins are: the configured access_url and the server's own address.
func isAllowedOrigin(origin string) bool {
	if origin == "" {
		return false
	}

	// Build list of allowed origins from config
	cache.RLock()
	accessURL := ""
	port := 8080
	bindAddress := "0.0.0.0"
	if cache.config != nil {
		accessURL = cache.config.AccessURL
		if cache.config.Port > 0 {
			port = cache.config.Port
		}
		if cache.config.BindAddress != "" {
			bindAddress = cache.config.BindAddress
		}
	}
	cache.RUnlock()

	// Normalize origin (remove trailing slash)
	origin = strings.TrimRight(origin, "/")

	// Check against access_url (the configured public URL)
	if accessURL != "" {
		if origin == strings.TrimRight(accessURL, "/") {
			return true
		}
	}

	// Check against server's own address variants (for direct access)
	serverPort := fmt.Sprintf("%d", port)
	localOrigins := []string{
		fmt.Sprintf("http://localhost:%s", serverPort),
		fmt.Sprintf("https://localhost:%s", serverPort),
		fmt.Sprintf("http://127.0.0.1:%s", serverPort),
		fmt.Sprintf("https://127.0.0.1:%s", serverPort),
	}
	if bindAddress != "0.0.0.0" && bindAddress != "" {
		localOrigins = append(localOrigins,
			fmt.Sprintf("http://%s:%s", bindAddress, serverPort),
			fmt.Sprintf("https://%s:%s", bindAddress, serverPort),
		)
	}

	for _, allowed := range localOrigins {
		if origin == allowed {
			return true
		}
	}

	return false
}

// securityHeadersMiddleware wraps an http.Handler to add security headers and handle CORS
// on every response. This centralizes security headers instead of duplicating them in each handler.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Security headers
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'")

		// CORS: Only set headers when an Origin header is present (cross-origin browser requests).
		// Non-browser clients (curl, scripts, cron) don't send Origin and don't need CORS headers.
		origin := r.Header.Get("Origin")
		if origin != "" {
			if isAllowedOrigin(origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			}

			// Handle CORS preflight
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusOK)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func shutdownMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if appShutdown.IsShuttingDown() {
			w.Header().Set("Connection", "close")
			sendJSONResponse(w, http.StatusServiceUnavailable, APIResponse{
				Success: false,
				Error:   "Server is shutting down",
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

func sendJSONResponse(w http.ResponseWriter, status int, response APIResponse) {
	w.Header().Set("Content-Type", "application/json")

	w.WriteHeader(status)
	json.NewEncoder(w).Encode(response)
}

// statusHandler provides server health and statistics endpoint
// Returns current server status, active requests, cron jobs, and configuration info
// Useful for monitoring and debugging the service
func statusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendJSONResponse(w, http.StatusMethodNotAllowed, APIResponse{
			Success: false,
			Error:   "Method not allowed. Only GET is supported",
		})
		return
	}

	// Get active requests
	activeReqs := execManager.getActiveRequests()
	activeRequestStatuses := make([]ActiveRequestStatus, 0, len(activeReqs))

	now := time.Now()
	for _, req := range activeReqs {
		activeRequestStatuses = append(activeRequestStatuses, ActiveRequestStatus{
			TraceID:    req.TraceID,
			CmdID:      req.CmdID,
			Host:       req.Host,
			Command:    req.Command,
			RemoteIP:   req.RemoteIP,
			RunningFor: fmt.Sprintf("%.1fs", now.Sub(req.StartTime).Seconds()),
			StartedAt:  req.StartTime.Format("2006-01-02 15:04:05.000"),
		})
	}

	// Get locked commands count
	execManager.RLock()
	lockedCmds := make(map[string]int)
	for cmdID, count := range execManager.activeCount {
		if count > 0 {
			lockedCmds[cmdID] = count
		}
	}
	execManager.RUnlock()

	// Get scheduled jobs info
	scheduledJobsMutex.RLock()
	scheduledJobStatuses := make([]ScheduledJobStatus, 0)
	scheduledRunBookStatuses := make([]ScheduledJobStatus, 0)
	scheduledHTTPRequestStatuses := make([]ScheduledJobStatus, 0)

	for cmdID, entryID := range scheduledJobs {
		entry := cronScheduler.Entry(entryID)

		if strings.HasPrefix(cmdID, "runbook:") {
			runBookID := strings.TrimPrefix(cmdID, "runbook:")
			cache.RLock()
			if cache.config != nil {
				for _, runBook := range cache.config.RunBooks {
					if runBook.RunBookID == runBookID {
						scheduledRunBookStatuses = append(scheduledRunBookStatuses, ScheduledJobStatus{
							CmdID:        runBookID,
							CronSchedule: runBook.CronSchedule,
							NextRun:      entry.Next.Format("2006-01-02 15:04:05"),
						})
						break
					}
				}
			}
			cache.RUnlock()
		} else if strings.HasPrefix(cmdID, "http:") {
			requestID := strings.TrimPrefix(cmdID, "http:")
			cache.RLock()
			if cache.config != nil {
				for _, req := range cache.config.HTTPRequests {
					if req.RequestID == requestID {
						scheduledHTTPRequestStatuses = append(scheduledHTTPRequestStatuses, ScheduledJobStatus{
							CmdID:        requestID,
							CronSchedule: req.CronSchedule,
							NextRun:      entry.Next.Format("2006-01-02 15:04:05"),
						})
						break
					}
				}
			}
			cache.RUnlock()
		} else {
			// Regular command - look up cron_schedule from config
			cache.RLock()
			cronSchedule := ""
			if cache.config != nil {
				for _, cmd := range cache.config.Commands {
					if cmd.CmdID == cmdID {
						cronSchedule = cmd.CronSchedule
						break
					}
				}
			}
			cache.RUnlock()

			scheduledJobStatuses = append(scheduledJobStatuses, ScheduledJobStatus{
				CmdID:        cmdID,
				CronSchedule: cronSchedule,
				NextRun:      entry.Next.Format("2006-01-02 15:04:05"),
			})
		}
	}
	scheduledJobsMutex.RUnlock()
	sortScheduledJobStatuses(scheduledJobStatuses)
	sortScheduledJobStatuses(scheduledRunBookStatuses)
	sortScheduledJobStatuses(scheduledHTTPRequestStatuses)

	// Get config last updated time and cron status
	cache.RLock()
	configHash := cache.lastHash
	cronEnabled := cache.config != nil && cache.config.CronEnabled != "N"
	cache.RUnlock()

	status := StatusResponse{
		Version:               AppVersion,
		ServerUptime:          fmt.Sprintf("%.1f hours", time.Since(serverStartTime).Hours()),
		TotalRequests:         int(atomic.LoadInt64(&totalWebRequests) + atomic.LoadInt64(&totalCronRequests)),
		ActiveRequests:        activeRequestStatuses,
		LockedCommands:        lockedCmds,
		CronEnabled:           cronEnabled,
		ScheduledJobs:         scheduledJobStatuses,
		ScheduledRunBooks:     scheduledRunBookStatuses,
		ScheduledHTTPRequests: scheduledHTTPRequestStatuses,
		ActiveRunBooks:        runBookManager.ActiveRuns(),
		DiskMonitor:           diskMonitorStatusSummary(),
		ConfigLastUpdated:     configHash[:8] + "...",
		CurrentTime:           time.Now().Format("2006-01-02 15:04:05.000"),
	}

	sendJSONResponse(w, http.StatusOK, APIResponse{
		Success: true,
		Data:    status,
	})
}

func logsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendJSONResponse(w, http.StatusMethodNotAllowed, APIResponse{
			Success: false,
			Error:   "Method not allowed. Only GET is supported",
		})
		return
	}

	// Extract trace_id from path
	path := r.URL.Path
	var traceID string
	switch {
	case strings.HasPrefix(path, "/api/logs/"):
		traceID = strings.TrimPrefix(path, "/api/logs/")
	case strings.HasPrefix(path, "/logs/"):
		traceID = strings.TrimPrefix(path, "/logs/")
	default:
		sendJSONResponse(w, http.StatusNotFound, APIResponse{
			Success: false,
			Error:   "Invalid path. Use /logs/{trace_id}",
		})
		return
	}

	traceID = strings.Trim(traceID, "/")
	if traceID == "" || strings.Contains(traceID, "/") {
		sendJSONResponse(w, http.StatusBadRequest, APIResponse{
			Success: false,
			Error:   "Missing or invalid trace ID",
		})
		return
	}

	// Validate trace_id format (must be exactly 16 lowercase hex characters)
	if !isValidTraceID(traceID) {
		sendJSONResponse(w, http.StatusBadRequest, APIResponse{
			Success: false,
			Error:   "Invalid trace ID format",
		})
		return
	}

	// Find the log file with this trace_id
	pattern := fmt.Sprintf("logs/*_%s_log.txt", traceID)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		sendJSONResponse(w, http.StatusInternalServerError, APIResponse{
			Success: false,
			Error:   "Error searching for log file",
		})
		return
	}

	if len(matches) == 0 {
		sendJSONResponse(w, http.StatusNotFound, APIResponse{
			Success: false,
			Error:   fmt.Sprintf("Log file not found for trace_id: %s", traceID),
		})
		return
	}

	// Use the first match (there should only be one)
	logFilePath := matches[0]
	if !canAccessLogFile(r, logFilePath) {
		sendJSONResponse(w, http.StatusForbidden, APIResponse{Success: false, Error: "Log access denied"})
		return
	}

	content, storage, s3URI, err := resolveStoredLogContent(r.Context(), logFilePath)
	if err != nil {
		sendJSONResponse(w, http.StatusInternalServerError, APIResponse{
			Success: false,
			Error:   "Failed to read log content",
		})
		return
	}

	fileName := filepath.Base(logFilePath)
	if storage == "s3" && s3URI != "" {
		if _, key, err := parseS3URI(s3URI); err == nil && filepath.Base(key) != "" {
			fileName = filepath.Base(key)
		}
	}

	// Set headers for file download
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", fileName))

	// Stream the file content directly to the response
	if _, err := w.Write(content); err != nil {
		// Can't send JSON error here as headers are already sent
		// Just log the error
		fmt.Printf("Error streaming log file: %v\n", err)
	}
}

// execHandler executes one atomic Command and sends its configured notifications.
func execHandler(w http.ResponseWriter, r *http.Request) {
	// Increment request counter
	atomic.AddInt64(&totalWebRequests, 1)

	// Generate trace ID and capture request details
	traceID := generateTraceID()
	clientIP := getClientIP(r)
	userAgent := r.Header.Get("User-Agent")
	requestStart := time.Now()

	if r.Method != http.MethodGet {
		sendJSONResponse(w, http.StatusMethodNotAllowed, APIResponse{
			Success: false,
			Error:   "Method not allowed. Only GET is supported",
		})
		return
	}

	path := r.URL.Path
	// Handle both /commands/ and /api/commands/ prefixes
	var cmdID string
	if strings.HasPrefix(path, "/api/commands/") {
		cmdID = strings.TrimPrefix(path, "/api/commands/")
	} else if strings.HasPrefix(path, "/commands/") {
		cmdID = strings.TrimPrefix(path, "/commands/")
	} else {
		sendJSONResponse(w, http.StatusNotFound, APIResponse{
			Success: false,
			Error:   "Invalid path. Use /api/commands/{cmd_id}",
		})
		return
	}
	if cmdID == "" {
		sendJSONResponse(w, http.StatusBadRequest, APIResponse{
			Success: false,
			Error:   "Missing command ID",
		})
		return
	}

	// First check if command exists and if it requires authentication
	cmdConfig, err := cache.findCommandConfig(cmdID)
	if err != nil {
		sendJSONResponse(w, http.StatusNotFound, APIResponse{
			Success: false,
			Error:   fmt.Sprintf("Configuration not found for ID: %s", cmdID),
		})
		return
	}

	claims, jwtAuthenticated := ClaimsFromRequest(r)
	// JWT identities are authorized by the command middleware. All non-JWT API
	// calls must satisfy the command's configured bearer token when one exists.
	if !jwtAuthenticated {
		authHeader := r.Header.Get("Authorization")
		expectedAuth := "Bearer " + cmdConfig.Command.BearerToken
		if cmdConfig.Command.BearerToken == "" || subtle.ConstantTimeCompare([]byte(authHeader), []byte(expectedAuth)) != 1 {
			sendJSONResponse(w, http.StatusUnauthorized, APIResponse{Success: false, Error: "Unauthorized: Invalid or missing bearer token"})
			return
		}
	}
	invokedBy, trigger := "api-token", "api"
	if jwtAuthenticated {
		invokedBy, trigger = claims.Username, "manual"
	}

	if !appShutdown.BeginWork() {
		sendJSONResponse(w, http.StatusServiceUnavailable, APIResponse{
			Success: false,
			Error:   "Server is shutting down",
		})
		return
	}
	defer appShutdown.DoneWork()

	if err := configRestoreGate.BeginWork(); err != nil {
		sendJSONResponse(w, http.StatusConflict, APIResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}
	defer configRestoreGate.EndWork()

	// Create log file
	logFile, logFilename, err := createCommandLogFile(cmdID, traceID)
	if err != nil {
		sendJSONResponse(w, http.StatusInternalServerError, APIResponse{
			Success: false,
			Error:   "Failed to create log file",
		})
		return
	}
	defer logFile.Close()

	// Write log header
	fmt.Fprintf(logFile, "               trace_id : %s\n", traceID)
	fmt.Fprintf(logFile, "    request_received_at : %s\n", requestStart.Format("2006-01-02 15:04:05.000"))
	fmt.Fprintf(logFile, "                 cmd_id : %s\n", cmdID)
	fmt.Fprintf(logFile, "                   host : %s\n", cmdConfig.Host.Host)
	fmt.Fprintf(logFile, "              remote_ip : %s\n", clientIP)
	fmt.Fprintf(logFile, "                  agent : %s\n", userAgent)
	fmt.Fprintf(logFile, "             invoked_by : %s\n", invokedBy)
	fmt.Fprintf(logFile, "                trigger : %s\n", trigger)

	// Check if skip_if_running is enabled and command is already active
	if cmdConfig.Command.SkipIfRunning && execManager.isCommandActive(cmdID) {
		fmt.Fprintf(logFile, "      execution_skipped : YES (skip_if_running enabled, command already active)\n")
		writeRunningLogStatus(logFile)
		updateExecutionLogStatus(logFile, logFilename, executionLogStatus{
			ExecTime: "0s",
			Error:    "command already running and skip_if_running is enabled",
			ExitCode: 1,
		})
		finalizeCommandLog(cmdConfig, logFile, logFilename)
		sendJSONResponse(w, http.StatusConflict, APIResponse{
			Success: false,
			Error:   fmt.Sprintf("Command '%s' is already running and skip_if_running is enabled", cmdID),
			Data: map[string]interface{}{
				"trace_id": traceID,
				"skipped":  true,
				"reason":   "skip_if_running",
			},
		})
		return
	}

	// Get the lock for this cmd_id
	cmdLock := execManager.getLock(cmdID)
	cmdLock.Lock()

	if appShutdown.IsShuttingDown() {
		cmdLock.Unlock()
		fmt.Fprintf(logFile, "      execution_skipped : YES (shutdown in progress)\n")
		writeRunningLogStatus(logFile)
		updateExecutionLogStatus(logFile, logFilename, executionLogStatus{
			ExecTime: "0s",
			Error:    "server is shutting down",
			ExitCode: 1,
		})
		finalizeCommandLog(cmdConfig, logFile, logFilename)
		sendJSONResponse(w, http.StatusServiceUnavailable, APIResponse{
			Success: false,
			Error:   "Server is shutting down",
			Data: map[string]interface{}{
				"trace_id": traceID,
				"skipped":  true,
				"reason":   "shutdown",
			},
		})
		return
	}

	// Track execution with all details
	activeReq := &ActiveRequest{
		TraceID:   traceID,
		CmdID:     cmdID,
		Host:      cmdConfig.Host.Host,
		Command:   cmdConfig.Command.Command,
		StartTime: requestStart,
		RemoteIP:  clientIP,
	}
	execManager.startExecution(activeReq)
	defer func() {
		execManager.endExecution(cmdID, traceID)
		cmdLock.Unlock()
	}()

	writeRunningLogStatus(logFile)

	startTime := time.Now()
	output, isTruncated, err, _ := runCommandWithTimeout(context.Background(), cmdConfig, logFile)
	execDuration := time.Since(startTime)

	// Prepare webhook notification data
	var exitCode int
	var errorMsg string
	if err != nil {
		exitCode = 1
		errorMsg = err.Error()
		updateExecutionLogStatus(logFile, logFilename, completedExecutionLogStatus(execDuration, isTruncated, err, exitCode))

		// Prepare webhook data
		webhookData := WebhookData{
			Output:            output, // Already truncated by executeSSHCommand
			ErrorMsg:          errorMsg,
			ExitCode:          exitCode,
			TraceID:           traceID,
			RequestReceivedAt: requestStart.Format("2006-01-02 15:04:05.000"),
			CmdID:             cmdID,
			Host:              cmdConfig.Host.Host,
			Command:           cmdConfig.Command.Command,
			RemoteIP:          clientIP,
			Agent:             userAgent,
			ExecTime:          fmt.Sprintf("%.3fs", execDuration.Seconds()),
		}

		// Send webhook notification for failure
		if cmdConfig.Command.NotifyWebhook != "" {
			appShutdown.RunAsync("webhook notification "+traceID, func() {
				sendWebhookNotification(cmdConfig.Command.NotifyWebhook, webhookData)
			})
		}

		// Send email notification for failure if configured
		if cmdConfig.Command.NotifyEmail != nil {
			cache.RLock()
			smtp := cache.config.SMTP
			cache.RUnlock()

			if smtp != nil {
				emailData := prepareEmailData(webhookData, cache.config, isTruncated)
				emailData.IncludeOutput = cmdConfig.Command.NotifyEmail.IncludeOutput
				queueEmail(emailData, cmdConfig.Command.NotifyEmail, smtp)
			}
		}

		finalizeCommandLog(cmdConfig, logFile, logFilename)

		sendJSONResponse(w, http.StatusInternalServerError, APIResponse{
			Success: false,
			Error:   fmt.Sprintf("Failed to execute command: %v", err),
		})
		return
	}

	exitCode = 0
	errorMsg = ""
	updateExecutionLogStatus(logFile, logFilename, completedExecutionLogStatus(execDuration, isTruncated, err, exitCode))

	// Send webhook notification for success
	if cmdConfig.Command.NotifyWebhook != "" {
		webhookData := WebhookData{
			Output:            output, // Already truncated by executeSSHCommand
			ErrorMsg:          errorMsg,
			ExitCode:          exitCode,
			TraceID:           traceID,
			RequestReceivedAt: requestStart.Format("2006-01-02 15:04:05.000"),
			CmdID:             cmdID,
			Host:              cmdConfig.Host.Host,
			Command:           cmdConfig.Command.Command,
			RemoteIP:          clientIP,
			Agent:             userAgent,
			ExecTime:          fmt.Sprintf("%.3fs", execDuration.Seconds()),
		}
		appShutdown.RunAsync("webhook notification "+traceID, func() {
			sendWebhookNotification(cmdConfig.Command.NotifyWebhook, webhookData)
		})
	}

	// Send email notification for success if configured
	if cmdConfig.Command.NotifyEmail != nil {
		cache.RLock()
		smtp := cache.config.SMTP
		cache.RUnlock()

		if smtp != nil {
			webhookData := WebhookData{
				Output:            output,
				ErrorMsg:          errorMsg,
				ExitCode:          exitCode,
				TraceID:           traceID,
				RequestReceivedAt: requestStart.Format("2006-01-02 15:04:05.000"),
				CmdID:             cmdID,
				Host:              cmdConfig.Host.Host,
				Command:           cmdConfig.Command.Command,
				RemoteIP:          clientIP,
				Agent:             userAgent,
				ExecTime:          fmt.Sprintf("%.3fs", execDuration.Seconds()),
			}
			emailData := prepareEmailData(webhookData, cache.config, isTruncated)
			emailData.IncludeOutput = cmdConfig.Command.NotifyEmail.IncludeOutput
			queueEmail(emailData, cmdConfig.Command.NotifyEmail, smtp)
		}
	}

	finalizeCommandLog(cmdConfig, logFile, logFilename)

	// Convert boolean to int for JSON response
	truncatedInt := 0
	if isTruncated {
		truncatedInt = 1
	}

	cmdOutput := CommandOutput{
		CmdID:             cmdID,
		Host:              cmdConfig.Host.Host,
		Command:           cmdConfig.Command.Command,
		Description:       cmdConfig.Command.Description,
		Output:            output,
		ExecTime:          fmt.Sprintf("%.3fs", execDuration.Seconds()),
		TraceID:           traceID,
		IsOutputTruncated: truncatedInt,
		ExitCode:          exitCode,
	}

	sendJSONResponse(w, http.StatusOK, APIResponse{
		Success: true,
		Data:    cmdOutput,
	})
}

// validateConfig performs comprehensive configuration validation
// Checks for duplicate IDs, valid syntax, missing variables, and configuration errors
func validateConfig(configFile string) error {
	fmt.Println("Validating configuration file:", configFile)
	fmt.Println("=" + strings.Repeat("=", 50))

	// Check if file exists
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		return fmt.Errorf("configuration file not found: %s", configFile)
	}

	// Read the raw config file
	data, err := ioutil.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to read config file: %v", err)
	}
	if err := validateNoLegacyWorkflowFields(data); err != nil {
		return err
	}

	// First pass: parse to get variables
	var tempConfig struct {
		Variables map[string]interface{} `json:"variables"`
	}
	if err := json.Unmarshal(data, &tempConfig); err != nil {
		return fmt.Errorf("invalid JSON syntax: %v", err)
	}

	// Perform variable substitutions
	jsonStr := string(data)
	substitutedJSON, err := substituteVariables(jsonStr, tempConfig.Variables)
	if err != nil {
		return fmt.Errorf("variable substitution failed: %v", err)
	}

	// Second pass: parse the substituted JSON
	var config Config
	if err := json.Unmarshal([]byte(substitutedJSON), &config); err != nil {
		return fmt.Errorf("failed to parse configuration after substitution: %v", err)
	}

	// Validation results
	var errors []string
	var warnings []string

	// Check for duplicate command IDs
	cmdIDs := make(map[string]int)
	for i, cmd := range config.Commands {
		if prevIndex, exists := cmdIDs[cmd.CmdID]; exists {
			errors = append(errors, fmt.Sprintf("Duplicate cmd_id '%s' found at indices %d and %d", cmd.CmdID, prevIndex, i))
		}
		cmdIDs[cmd.CmdID] = i
	}

	// Check for duplicate host names in the raw JSON
	// Look for pattern: "hostname" : { ... "ssh_host" ...
	// We need to find all occurrences in the hosts section
	hostsStart := strings.Index(jsonStr, `"hosts"`)
	if hostsStart >= 0 {
		// Find the hosts section
		hostsEnd := hostsStart
		braceCount := 0
		inHosts := false
		for i := hostsStart; i < len(jsonStr); i++ {
			if jsonStr[i] == '{' {
				braceCount++
				if braceCount == 1 {
					inHosts = true
				}
			} else if jsonStr[i] == '}' {
				braceCount--
				if braceCount == 0 && inHosts {
					hostsEnd = i
					break
				}
			}
		}

		if hostsEnd > hostsStart {
			hostsSection := jsonStr[hostsStart:hostsEnd]
			// Now find all host definitions in this section
			hostPattern := regexp.MustCompile(`"([^"]+)"\s*:\s*\{`)
			matches := hostPattern.FindAllStringSubmatch(hostsSection, -1)
			hostNames := make(map[string]int)
			for _, match := range matches {
				if len(match) > 1 && match[1] != "hosts" {
					hostName := match[1]
					hostNames[hostName]++
					if hostNames[hostName] > 1 {
						warnings = append(warnings, fmt.Sprintf("Duplicate host name '%s' detected (last definition will be used)", hostName))
					}
				}
			}
		}
	}

	// Validate command references
	hasS3LogStorageCommands := false
	for _, cmd := range config.Commands {
		// Check if host_ref exists
		if _, exists := config.Hosts[cmd.HostRef]; !exists {
			errors = append(errors, fmt.Sprintf("Command '%s' references non-existent host '%s'", cmd.CmdID, cmd.HostRef))
		}

		if cmd.LogStorage != "" &&
			!strings.EqualFold(cmd.LogStorage, "local") &&
			!strings.EqualFold(cmd.LogStorage, "s3") {
			errors = append(errors, fmt.Sprintf("Command '%s' has invalid log_storage '%s' (valid: local, s3)", cmd.CmdID, cmd.LogStorage))
		}
		if commandUsesS3LogStorage(cmd) {
			hasS3LogStorageCommands = true
		}

		// Validate cron schedule if specified
		if cmd.CronSchedule != "" {
			parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			if _, err := parser.Parse(cmd.CronSchedule); err != nil {
				errors = append(errors, fmt.Sprintf("Command '%s' has invalid cron schedule '%s': %v", cmd.CmdID, cmd.CronSchedule, err))
			}
		}

		// Validate max_output_size if specified
		if cmd.MaxOutputSize != "" {
			if _, err := parseSize(cmd.MaxOutputSize); err != nil {
				errors = append(errors, fmt.Sprintf("Command '%s' has invalid max_output_size '%s': %v", cmd.CmdID, cmd.MaxOutputSize, err))
			}
		}

		// Validate timeout_seconds if specified (0 or empty = no limit)
		if cmd.TimeoutSeconds < 0 {
			errors = append(errors, fmt.Sprintf("Command '%s' timeout_seconds cannot be negative (got %d)", cmd.CmdID, cmd.TimeoutSeconds))
		}

		// Check for bearer token on sensitive commands
		if cmd.Command != "" && strings.Contains(strings.ToLower(cmd.Command), "rm -rf") && cmd.BearerToken == "" {
			warnings = append(warnings, fmt.Sprintf("Command '%s' contains potentially dangerous operation without bearer token protection", cmd.CmdID))
		}
	}

	// Validate hosts
	for hostName, host := range config.Hosts {
		// Normalize host for validation
		normalizeHost(&host)

		if strings.ToLower(host.ConnectionType) == "winrm" {
			// WinRM validation
			if host.Username == "" || host.Password == "" {
				errors = append(errors, fmt.Sprintf("Host '%s' (WinRM) requires username and password", hostName))
			}

			// Check CA cert file if specified
			if host.WinRMCACert != "" {
				if _, err := os.Stat(host.WinRMCACert); os.IsNotExist(err) {
					errors = append(errors, fmt.Sprintf("Host '%s' references non-existent CA certificate: %s", hostName, host.WinRMCACert))
				}
			}

			// Validate port
			if host.Port <= 0 || host.Port > 65535 {
				errors = append(errors, fmt.Sprintf("Host '%s' has invalid WinRM port: %d", hostName, host.Port))
			}
		} else {
			// SSH validation
			// Check authentication method
			if host.Password == "" && host.PemFile == "" {
				errors = append(errors, fmt.Sprintf("Host '%s' has no authentication method (neither password nor PEM key)", hostName))
			}

			// Check if PEM file exists (if specified)
			if host.PemFile != "" {
				if _, err := os.Stat(host.PemFile); os.IsNotExist(err) {
					errors = append(errors, fmt.Sprintf("Host '%s' references non-existent PEM file: %s", hostName, host.PemFile))
				}
			}

			// Validate port
			if host.Port <= 0 || host.Port > 65535 {
				errors = append(errors, fmt.Sprintf("Host '%s' has invalid SSH port: %d", hostName, host.Port))
			}
		}
	}
	errors = append(errors, bastionRefProblems(config.Hosts)...)
	hostKeyProblems, hostKeyWarnings := hostKeyConfigProblems(&config)
	errors = append(errors, hostKeyProblems...)
	warnings = append(warnings, hostKeyWarnings...)

	// Validate SMTP configuration if present
	if config.SMTP != nil {
		if config.SMTP.Host == "" {
			errors = append(errors, "SMTP configuration present but host is empty")
		}
		if config.SMTP.Port <= 0 || config.SMTP.Port > 65535 {
			errors = append(errors, fmt.Sprintf("Invalid SMTP port: %d", config.SMTP.Port))
		}
		if config.SMTP.isEnabled() {
			normalized, err := normalizeSMTPConfig(config.SMTP, false)
			if err != nil {
				errors = append(errors, err.Error())
			} else if err := validateSMTPConfig(normalized); err != nil {
				errors = append(errors, err.Error())
			}
		}
	}
	if config.ScheduledJobsOverviewEmail != nil {
		normalized := normalizeScheduledJobsOverviewEmailConfig(config.ScheduledJobsOverviewEmail)
		if err := validateScheduledJobsOverviewEmailConfig(normalized, config.SMTP, false, config.Variables); err != nil {
			errors = append(errors, "Invalid Scheduled Jobs Overview email configuration: "+err.Error())
		} else if normalized.Enabled && !scheduledJobsOverviewSMTPReady(config.SMTP) {
			warnings = append(warnings, "Scheduled Jobs Overview email is enabled but SMTP is unavailable or disabled")
		}
	}

	// Validate global settings
	if config.Port <= 0 || config.Port > 65535 {
		errors = append(errors, fmt.Sprintf("Invalid service port: %d", config.Port))
	}

	// Check for email notifications and validate email.tpl exists
	hasEmailNotifications := false
	for _, cmd := range config.Commands {
		if cmd.NotifyEmail != nil {
			hasEmailNotifications = true
			if config.SMTP == nil {
				warnings = append(warnings, fmt.Sprintf("Command '%s' has email notification configured but no SMTP settings", cmd.CmdID))
				break
			}
		}

		if config.S3 != nil {
			if config.S3.AccessKeyID == "" {
				warnings = append(warnings, "S3 configuration present but access_key_id is empty")
			}
			if config.S3.SecretAccessKey == "" {
				warnings = append(warnings, "S3 configuration present but secret_access_key is empty")
			}
			if config.S3.Region == "" {
				warnings = append(warnings, "S3 configuration present but region is empty")
			}
			if config.S3.Bucket == "" {
				warnings = append(warnings, "S3 configuration present but bucket is empty")
			}
		} else if hasS3LogStorageCommands {
			warnings = append(warnings, "At least one command uses S3 log storage but S3 settings are not configured")
		}
	}

	commandSet := make(map[string]bool, len(cmdIDs))
	for cmdID := range cmdIDs {
		commandSet[cmdID] = true
	}
	requestSet, err := validateHTTPRequests(config.HTTPRequests, commandSet)
	if err != nil {
		errors = append(errors, err.Error())
		requestSet = httpRequestIDSet(config.HTTPRequests)
	}
	for _, req := range config.HTTPRequests {
		if req.NotifyEmail != nil {
			hasEmailNotifications = true
			if config.SMTP == nil {
				warnings = append(warnings, fmt.Sprintf("HTTP request '%s' has email notification configured but no SMTP settings", req.RequestID))
			}
		}
		if req.SkipTLSVerify {
			warnings = append(warnings, fmt.Sprintf("HTTP request '%s' skips TLS certificate verification", req.RequestID))
		}
	}
	if hasEmailNotifications && config.SMTP != nil {
		if _, err := os.Stat("email.tpl"); os.IsNotExist(err) {
			errors = append(errors, "Email notifications are configured but email.tpl template file is missing")
		}
	}
	if err := validateRunBooks(config.RunBooks, commandSet, requestSet); err != nil {
		errors = append(errors, err.Error())
	}
	if err := validateVNCSettings(config.VNC); err != nil {
		errors = append(errors, err.Error())
	}
	if err := validateVNCConnections(config.VNCConnections, config.Hosts); err != nil {
		errors = append(errors, err.Error())
	}
	for _, c := range config.VNCConnections {
		if c.TunnelHostRef == "" && (c.AuthType == "" || strings.EqualFold(c.AuthType, "auto") || strings.EqualFold(c.AuthType, "vnc") || strings.EqualFold(c.AuthType, "none")) {
			warnings = append(warnings, fmt.Sprintf("VNC connection '%s' connects directly (no SSH tunnel); classic VNC traffic is not encrypted between this server and the VNC server", c.VNCID))
		}
	}
	if err := validateSecurityConfig(config.Users, config.TrustedProxies, config.Commands, config.RunBooks, config.HTTPRequests, config.Hosts, config.VNCConnections); err != nil {
		errors = append(errors, err.Error())
	}
	if err := validateTerminalsConfig(config.Terminals); err != nil {
		errors = append(errors, err.Error())
	}
	if err := validateAgentSessionsConfig(&config); err != nil {
		errors = append(errors, err.Error())
	}
	diskVolumeCount := 0
	if config.DiskMonitor != nil {
		normalizedDisk := normalizeDiskMonitorConfig(config.DiskMonitor)
		if err := validateDiskMonitorConfig(normalizedDisk, config.Hosts); err != nil {
			errors = append(errors, "disk_monitor: "+err.Error())
		}
		for _, v := range normalizedDisk.Volumes {
			if v.isEnabled() {
				diskVolumeCount++
			}
		}
		if diskVolumeCount > 0 && normalizedDisk.isEnabled() {
			if config.SMTP == nil || !config.SMTP.isEnabled() {
				warnings = append(warnings, "disk_monitor has enabled volumes but SMTP is not configured; alerts will not be emailed")
			} else if len(normalizedDisk.Recipients.To) == 0 {
				warnings = append(warnings, "disk_monitor has enabled volumes but no recipients; alerts will not be emailed")
			}
		}
	}
	for _, runBook := range config.RunBooks {
		if runBook.CronSchedule != "" {
			parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			if _, err := parser.Parse(runBook.CronSchedule); err != nil {
				errors = append(errors, fmt.Sprintf("RunBook '%s' has invalid cron schedule '%s': %v", runBook.RunBookID, runBook.CronSchedule, err))
			}
		}
		if runBook.NotifyEmail != nil && config.SMTP == nil {
			warnings = append(warnings, fmt.Sprintf("RunBook '%s' has email notification configured but no SMTP settings", runBook.RunBookID))
		}
	}
	// Print results
	fmt.Println("\n📋 Configuration Summary:")
	fmt.Printf("  • Hosts defined: %d\n", len(config.Hosts))
	fmt.Printf("  • Commands defined: %d\n", len(config.Commands))
	fmt.Printf("  • RunBooks defined: %d\n", len(config.RunBooks))
	fmt.Printf("  • HTTP requests defined: %d\n", len(config.HTTPRequests))
	fmt.Printf("  • Users defined: %d\n", len(config.Users))

	cronCount := 0
	for _, cmd := range config.Commands {
		if cmd.CronSchedule != "" {
			cronCount++
		}
	}
	fmt.Printf("  • Scheduled commands: %d\n", cronCount)

	runBookCronCount := 0
	for _, runBook := range config.RunBooks {
		if runBook.CronSchedule != "" {
			runBookCronCount++
		}
	}
	fmt.Printf("  • Scheduled RunBooks: %d\n", runBookCronCount)

	httpCronCount := 0
	for _, req := range config.HTTPRequests {
		if req.CronSchedule != "" {
			httpCronCount++
		}
	}
	fmt.Printf("  • Scheduled HTTP requests: %d\n", httpCronCount)
	fmt.Printf("  • Disk monitor volumes (enabled): %d\n", diskVolumeCount)
	fmt.Printf("  • Port: %d\n", config.Port)
	fmt.Printf("  • Bind address: %s\n", config.BindAddress)

	if len(errors) > 0 {
		fmt.Println("\n❌ Validation Errors:")
		for _, err := range errors {
			fmt.Printf("  • %s\n", err)
		}
	}

	if len(warnings) > 0 {
		fmt.Println("\n⚠️  Warnings:")
		for _, warn := range warnings {
			fmt.Printf("  • %s\n", warn)
		}
	}

	if len(errors) == 0 {
		fmt.Println("\n✅ Configuration is valid!")
		if len(warnings) > 0 {
			fmt.Println("   (with warnings - review above)")
		}
		return nil
	}

	return fmt.Errorf("configuration validation failed with %d error(s)", len(errors))
}

// setupGracefulShutdown handles CTRL+C/SIGTERM for graceful shutdown.
func setupGracefulShutdown(server *http.Server) <-chan struct{} {
	done := make(chan struct{})
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	var cleanupOnce sync.Once

	go func() {
		for sig := range c {
			if sig == os.Interrupt || sig == syscall.SIGTERM {
				if !appShutdown.BeginShutdown() {
					fmt.Println("\nForced shutdown requested. Exiting now.")
					os.Exit(1)
				}

				cleanupOnce.Do(func() {
					go func() {
						fmt.Println("\n🛑 Shutting down gracefully...")

						// Show active request count
						activeReqs := execManager.getActiveRequestCount()
						if activeReqs > 0 {
							fmt.Printf("   Active requests: %d\n", activeReqs)
						}

						// Perform cleanup
						performCleanup(server)

						fmt.Println("✓ Shutdown complete. Goodbye!")
						signal.Stop(c)
						close(done)
						return
					}()
				})
			}
		}
	}()

	return done
}

// performCleanup handles cleanup tasks during shutdown
func performCleanup(server *http.Server) {
	shutdownCtx, cancel := appShutdown.contextUntilDeadline()
	defer cancel()

	fmt.Println("   • Stopping cron scheduler...")
	cronDone := make(chan struct{})
	if cronScheduler != nil {
		cronCtx := cronScheduler.Stop()
		go func() {
			<-cronCtx.Done()
			close(cronDone)
		}()
	} else {
		close(cronDone)
	}

	fmt.Println("   • Stopping HTTP server...")
	serverDone := make(chan error, 1)
	if server != nil {
		go func() {
			serverDone <- server.Shutdown(shutdownCtx)
		}()
	} else {
		serverDone <- nil
	}

	fmt.Println("   • Waiting for active work to complete...")
	workDone := make(chan bool, 1)
	go func() {
		workDone <- appShutdown.waitForWork(shutdownCtx)
	}()

	waitingForCron := true
	waitingForServer := true
	waitingForWork := true

	for waitingForCron || waitingForServer || waitingForWork {
		select {
		case <-shutdownCtx.Done():
			fmt.Println("   ⚠️  Timeout waiting for graceful shutdown. Force closing...")
			return
		case <-cronDone:
			if waitingForCron {
				fmt.Println("   • Cron scheduler stopped")
				waitingForCron = false
				cronDone = nil
			}
		case err := <-serverDone:
			if waitingForServer {
				if err != nil && err != http.ErrServerClosed {
					fmt.Printf("   ⚠️  HTTP shutdown error: %v\n", err)
				} else {
					fmt.Println("   • HTTP server stopped")
				}
				waitingForServer = false
				serverDone = nil
			}
		case completed := <-workDone:
			if waitingForWork {
				if completed {
					fmt.Println("   • All active work completed")
				} else {
					fmt.Println("   ⚠️  Active work did not finish before timeout")
				}
				flushDiskMonitorState()
				waitingForWork = false
				workDone = nil
			}
		}
	}
}

// getActiveRequestCount returns the number of active requests
func (em *ExecutionManager) getActiveRequestCount() int {
	em.RLock()
	defer em.RUnlock()

	count := 0
	for _, activeCount := range em.activeCount {
		count += activeCount
	}

	return count
}

// main is the entry point of the SSH Command Executor service
// Initializes all components and starts the HTTP server
func main() {
	// Parse command line flags
	validateFlag := flag.Bool("validate", false, "Validate configuration and exit")
	versionFlag := flag.Bool("version", false, "Show version information and exit")
	configFile := flag.String("config", "config.json", "Path to configuration file")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("cguard version %s\n", AppVersion)
		os.Exit(0)
	}

	// If validate flag is set, just validate and exit
	if *validateFlag {
		if err := validateConfig(*configFile); err != nil {
			fmt.Printf("\n❌ Validation failed: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Update cache config file if custom path provided
	if *configFile != "config.json" {
		cache.configFile = *configFile
	}

	// Set server start time for uptime tracking
	serverStartTime = time.Now()

	fmt.Println("Starting CommandGuard")

	// Create logs directory
	if err := os.MkdirAll("logs", privateDirMode); err != nil {
		log.Fatalf("Failed to create logs directory: %v", err)
	}

	// Tighten what an older version (or a manual copy) left group/world-readable
	hardenFilePermissions(*configFile)

	// Close out logs a previous process left as "job running" (stopped or crashed mid-run)
	go markInterruptedLogs("logs", serverStartTime)

	// Initialize cron scheduler
	cronScheduler = cron.New()
	scheduledJobs = make(map[string]cron.EntryID)
	cronScheduler.Start()

	// Test initial config load (this will also setup cron jobs)
	err := cache.loadIfChanged()
	if err != nil {
		log.Fatalf("Failed to load initial configuration: %v", err)
	}

	// Show cron status
	cache.RLock()
	if cache.config != nil && cache.config.CronEnabled == "N" {
		fmt.Println("Cron scheduler initialized but DISABLED (cron_enabled: N)")
	} else {
		fmt.Println("Cron scheduler started and ENABLED")
	}
	cache.RUnlock()

	// Start the cleanup routine
	execManager.startCleanupRoutine()

	// Start automatic log cleanup routine
	go startLogCleanupRoutine()
	go startSecurityAuditCleanupRoutine()

	// Disk Monitor: restore runtime state, then run the checker and alert loops
	loadDiskMonitorState()
	go startDiskMonitorScheduler()
	go startDiskMonitorAlertEngine()

	// Initialize authentication manager with saved credentials from config
	config := cache.GetConfig() // Get current config
	// JWT secret priority: config.json > JWT_SECRET env var > auto-generate
	jwtSecret := config.JWTSecret
	if jwtSecret == "" {
		jwtSecret = os.Getenv("JWT_SECRET")
	}
	authConfig := &AuthConfig{
		AdminPassword:  config.AdminPassword, // Load saved admin password
		TOTPSecret:     config.TOTPSecret,    // Load saved TOTP secret
		JWTSecret:      jwtSecret,
		DevMode:        config.DevMode, // Load dev_mode flag (defaults to false)
		Users:          config.Users,
		TrustedProxies: config.TrustedProxies,
	}
	authManager = NewAuthManager(authConfig, *configFile)

	// Initialize configuration API
	configAPI := NewConfigAPI(*configFile, authManager)
	backupAPI := NewBackupAPI(*configFile)

	// Register authentication endpoints (no auth required)
	http.HandleFunc("/api/auth/login", authManager.HandleLogin)
	http.HandleFunc("/api/auth/verify-2fa", authManager.HandleVerify2FA)
	http.HandleFunc("/api/auth/complete-onboarding", authManager.HandleCompleteOnboarding)
	http.HandleFunc("/api/auth/onboarding", authManager.HandleCompleteOnboarding)
	http.HandleFunc("/api/auth/validate", authManager.HandleValidate)
	http.HandleFunc("/api/auth/logout", authManager.HandleLogout)

	// Register API endpoints (auth required)
	http.HandleFunc("/api/commands/", authManager.CommandMiddleware(execHandler))
	http.HandleFunc("/api/http-requests/", authManager.HTTPRequestMiddleware(httpRequestExecHandler))
	http.HandleFunc("/api/status", authManager.AdminMiddleware(statusHandler))
	http.HandleFunc("/api/overview/scheduled-jobs", authManager.AuthMiddleware(overviewHandler))
	http.HandleFunc("/api/runbooks/", runBookExecutionHandler)
	http.HandleFunc("/api/config/s3/test", authManager.AdminMiddleware(configAPI.HandleTestS3))
	http.HandleFunc("/api/config/backups/export", authManager.AdminMiddleware(backupAPI.HandleExport))
	http.HandleFunc("/api/config/backups/inspect", authManager.AdminMiddleware(backupAPI.HandleInspect))
	http.HandleFunc("/api/config/backups/restore", authManager.AdminMiddleware(backupAPI.HandleRestore))
	http.HandleFunc("/api/users", authManager.AdminMiddleware(authManager.HandleUsers))
	http.HandleFunc("/api/users/", authManager.AdminMiddleware(authManager.HandleUsers))
	http.HandleFunc("/api/security/audit", authManager.AdminMiddleware(authManager.HandleAudit))
	http.HandleFunc("/api/account/password", authManager.AuthMiddleware(authManager.HandleAccountPassword))
	http.HandleFunc("/api/user/commands", authManager.AuthMiddleware(authManager.HandleAssignedCommands))
	http.HandleFunc("/api/user/runbooks", authManager.AuthMiddleware(authManager.HandleAssignedRunBooks))
	http.HandleFunc("/api/user/commands/", authManager.AuthMiddleware(authManager.HandleUserSchedule))
	http.HandleFunc("/api/user/runbooks/", authManager.AuthMiddleware(authManager.HandleUserSchedule))
	http.HandleFunc("/api/user/http-requests", authManager.AuthMiddleware(authManager.HandleAssignedHTTPRequests))
	http.HandleFunc("/api/user/http-requests/", authManager.AuthMiddleware(authManager.HandleUserSchedule))

	// Persistent browser terminals (JWT auth; WebSocket attach authenticates itself)
	terminalsAPI := NewTerminalsAPI(authManager)
	terminalsAPI.config = configAPI // shared Agent Sessions are written to config.json via mutateConfig
	http.HandleFunc("/api/terminals", terminalsAPI.Handle)
	http.HandleFunc("/api/terminals/", terminalsAPI.Handle)

	// Remote Desktops (VNC): runtime API + WebSocket (ticket auth), admin config API
	vncAPI := NewVNCAPI(authManager)
	http.HandleFunc("/api/vnc", vncAPI.Handle)
	http.HandleFunc("/api/vnc/", vncAPI.Handle)
	http.HandleFunc("/api/config/vnc-connections", authManager.AdminMiddleware(configAPI.HandleVNCConnections))
	http.HandleFunc("/api/config/vnc-connections/", authManager.AdminMiddleware(configAPI.HandleVNCConnections))

	// Configuration management endpoints (auth required)
	http.HandleFunc("/api/config", authManager.AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			configAPI.HandleGetConfig(w, r)
		case http.MethodPut:
			configAPI.HandleUpdateConfig(w, r)
		default:
			sendJSONResponse(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Error: "Method not allowed"})
		}
	}))
	http.HandleFunc("/api/config/hosts", authManager.AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			configAPI.HandleGetHosts(w, r)
		case http.MethodPost:
			configAPI.HandleCreateHost(w, r)
		default:
			sendJSONResponse(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Error: "Method not allowed"})
		}
	}))
	http.HandleFunc("/api/config/hosts/", authManager.AdminMiddleware(configAPI.HandleHostItem))
	http.HandleFunc("/api/config/commands", authManager.AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			configAPI.HandleGetCommands(w, r)
		case http.MethodPost:
			configAPI.HandleCreateCommand(w, r)
		default:
			sendJSONResponse(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Error: "Method not allowed"})
		}
	}))
	http.HandleFunc("/api/config/commands/", authManager.AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			configAPI.HandleUpdateCommand(w, r)
		case http.MethodDelete:
			configAPI.HandleDeleteCommand(w, r)
		case http.MethodPost:
			if strings.HasSuffix(r.URL.Path, "/execute") {
				configAPI.HandleExecuteCommand(w, r)
			} else {
				sendJSONResponse(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Error: "Method not allowed"})
			}
		default:
			sendJSONResponse(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Error: "Method not allowed"})
		}
	}))
	http.HandleFunc("/api/config/runbooks", authManager.AdminMiddleware(configAPI.HandleRunBooks))
	http.HandleFunc("/api/config/runbooks/", authManager.AdminMiddleware(configAPI.HandleRunBooks))
	http.HandleFunc("/api/config/http-requests", authManager.AdminMiddleware(configAPI.HandleHTTPRequests))
	http.HandleFunc("/api/config/http-requests/", authManager.AdminMiddleware(configAPI.HandleHTTPRequests))
	http.HandleFunc("/api/config/variables", authManager.AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			configAPI.HandleGetVariables(w, r)
		case http.MethodPut:
			configAPI.HandleUpdateVariables(w, r)
		default:
			sendJSONResponse(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Error: "Method not allowed"})
		}
	}))
	http.HandleFunc("/api/config/smtp", authManager.AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			configAPI.HandleGetSMTP(w, r)
		case http.MethodPut:
			configAPI.HandleUpdateSMTP(w, r)
		default:
			sendJSONResponse(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Error: "Method not allowed"})
		}
	}))
	http.HandleFunc("/api/config/smtp/test-email", authManager.AdminMiddleware(configAPI.HandleSendSMTPTestEmail))
	http.HandleFunc("/api/config/scheduled-jobs-overview-email", authManager.AdminMiddleware(configAPI.HandleScheduledJobsOverviewEmail))
	// Disk Monitor endpoints (admin only)
	http.HandleFunc("/api/config/disk-monitor", authManager.AdminMiddleware(configAPI.HandleDiskMonitorConfig))
	http.HandleFunc("/api/config/disk-monitor/", authManager.AdminMiddleware(configAPI.HandleDiskMonitorConfig))
	http.HandleFunc("/api/disk-monitor", authManager.AdminMiddleware(handleDiskMonitorOps))
	http.HandleFunc("/api/disk-monitor/", authManager.AdminMiddleware(handleDiskMonitorOps))
	// Logs endpoints
	http.HandleFunc("/api/logs", authManager.AuthMiddleware(configAPI.HandleGetLogs))
	http.HandleFunc("/api/logs/clear-old", authManager.AdminMiddleware(configAPI.HandleClearOldLogs))
	http.HandleFunc("/api/logs/delete/", authManager.AdminMiddleware(configAPI.HandleDeleteLog))
	http.HandleFunc("/api/logs/", authManager.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Handle the original /api/logs/{trace_id} format for backward compatibility
		if !strings.Contains(path[len("/api/logs/"):], "/") {
			// This is /api/logs/{trace_id} - serve the log file directly
			logsHandler(w, r)
		} else if strings.HasSuffix(path, "/content") {
			configAPI.HandleLogContent(w, r)
		} else if strings.HasSuffix(path, "/download") {
			configAPI.HandleLogDownload(w, r)
		} else {
			sendJSONResponse(w, http.StatusNotFound, APIResponse{Success: false, Error: "Endpoint not found"})
		}
	}))

	// PEM Files endpoints
	pemFilesAPI := NewPemFilesAPI()
	http.HandleFunc("/api/pemfiles", authManager.AdminMiddleware(pemFilesAPI.HandlePemFiles))
	http.HandleFunc("/api/pemfiles/", authManager.AdminMiddleware(pemFilesAPI.HandlePemFiles))

	// Serve static files from ./ui/ directory
	fileServer := http.FileServer(http.Dir("./ui/"))
	http.Handle("/", fileServer)

	// Get port and bind address from config
	port := 8080
	bindAddress := "0.0.0.0" // Default to all interfaces
	cache.RLock()
	if cache.config != nil {
		if cache.config.Port > 0 {
			port = cache.config.Port
		}
		if cache.config.BindAddress != "" {
			bindAddress = cache.config.BindAddress
		}
	}
	cache.RUnlock()

	listenAddr := fmt.Sprintf("%s:%d", bindAddress, port)
	fmt.Printf("Server starting on %s\n", listenAddr)
	fmt.Println("\nWeb UI available at: http://" + listenAddr)
	fmt.Println("\nAPI Endpoints:")
	fmt.Println("  POST /api/auth/login           - Admin login")
	fmt.Println("  POST /api/auth/verify-2fa      - 2FA verification")
	fmt.Println("  GET  /api/commands/{cmd_id}    - Execute command")
	fmt.Println("  GET  /api/http-requests/{id}   - Execute HTTP request")
	fmt.Println("  POST /api/runbooks/{id}/runs   - Start RunBook")
	fmt.Println("  GET  /api/status               - Server status")
	fmt.Println("  GET  /api/overview/scheduled-jobs - Day scheduled jobs overview")
	fmt.Println("  GET  /api/logs/{trace_id}      - Download log file")
	fmt.Println("  GET  /api/terminals            - Persistent SSH terminals (via cg_sessiond)")
	fmt.Println("  GET  /api/disk-monitor/state   - Disk Monitor dashboard state")
	fmt.Println("  GET  /api/config/*             - Configuration management")
	fmt.Println("\nLogs written to: logs/")
	fmt.Println("\nPress Ctrl+C to stop (10 second grace period)")

	// Start console display
	go displayConsoleStats()

	server := &http.Server{
		Addr:           listenAddr,
		Handler:        securityHeadersMiddleware(shutdownMiddleware(http.DefaultServeMux)),
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   120 * time.Second, // Allow longer for slow SSH command responses
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1MB
	}

	shutdownDone := setupGracefulShutdown(server)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	if appShutdown.IsShuttingDown() {
		<-shutdownDone
	}
}
