# CommandGuard User Manual

> Command Guard 0.7.0 adds regular users, explicit Command/RunBook grants, sign-in policies, filtered operational views, and security auditing. See [multi-user.md](multi-user.md) for account lifecycle, policy semantics, APIs, and trusted-proxy configuration.

## Table of Contents

1. [Overview](#overview)
2. [Installation and Setup](#installation-and-setup)
3. [Authentication and Security](#authentication-and-security)
4. [Web Administration Interface](#web-administration-interface)
5. [API Reference](#api-reference)
6. [Host Management](#host-management)
7. [Command Configuration](#command-configuration)
8. [RunBooks](#runbooks)
9. [Scheduling and Automation](#scheduling-and-automation)
10. [Notification Systems](#notification-systems)
11. [Variable Substitution](#variable-substitution)
12. [Logging and Monitoring](#logging-and-monitoring)
13. [PEM File Management](#pem-file-management)
14. [Configuration Management](#configuration-management)
15. [Command Line Interface](#command-line-interface)
16. [Troubleshooting](#troubleshooting)
17. [Best Practices](#best-practices)
18. [Advanced Features](#advanced-features)

---

## Overview

SSH Executor is a powerful, enterprise-grade web service that provides centralized remote command execution via HTTP APIs. It supports both SSH (Linux/Unix) and WinRM (Windows) protocols, offering a comprehensive solution for infrastructure automation, monitoring, and management.

### Key Features

- **Cross-Platform Remote Execution**: Execute commands on Linux/Unix systems via SSH and Windows systems via WinRM
- **Web-Based Admin UI**: Full-featured administrative interface with 2FA authentication
- **Dual Authentication System**: Admin UI with password + TOTP 2FA, API endpoints with bearer tokens
- **Hot Configuration Reload**: Changes applied without service restart
- **Advanced Scheduling**: Cron-based scheduling for Commands and RunBooks
- **Notification Systems**: Email and webhook notifications with customizable templates
- **RunBooks**: Ordered multi-command workflows with retries, delays, and consolidated results
- **Output Management**: Configurable output size limits to prevent memory exhaustion
- **Comprehensive Logging**: Individual log files for each execution with trace IDs
- **Variable Substitution**: Support for configuration and environment variables
- **PEM Key Management**: Secure storage and management of SSH keys
- **Execution Locking**: Prevents concurrent execution of the same command
- **Skip If Running**: Option to skip command execution if already in progress

### Architecture Components

1. **HTTP Server**: RESTful API server with CORS support
2. **Authentication Manager**: JWT-based session management with 2FA
3. **Configuration Cache**: File-hash based hot-reload system
4. **Execution Manager**: Concurrent execution control and locking
5. **Cron Scheduler**: Automated task scheduling
6. **Notification Queue**: Reliable email and webhook delivery
7. **Remote Executors**: SSH and WinRM protocol implementations

---

## Installation and Setup

### System Requirements

- **Operating System**: Linux (recommended), macOS, or Windows
- **Go Runtime**: Version 1.26 or higher (if building from source)
- **Network Access**: 
  - SSH port 22 (or custom) to target Linux/Unix hosts
  - WinRM ports 5985/5986 to target Windows hosts
  - SMTP server access for email notifications
- **Storage**: Minimum 100MB for logs and queue files

### Installation

Build from source or run the Docker image as described in the [Quick start](../README.md#quick-start) and [Docker](../README.md#docker) sections of the README.

### Directory Structure

```
cguard/
├── cguard           # Main executable
├── config.json         # Configuration file
├── email.tpl          # Email notification template
├── ui/                # Web UI assets
│   ├── index.html     # Admin interface
│   ├── login.html     # Login page
│   ├── css/           # Stylesheets
│   └── js/            # JavaScript files
├── logs/              # Execution logs
│   ├── YYYY_MM_DD_*.txt  # Individual execution logs
│   └── errors_smtp.log    # SMTP error log
├── mail_queue/        # Email queue directory
└── pem_files/         # SSH key storage
```

---

## Authentication and Security

### Two-Factor Authentication System

SSH Executor implements a robust authentication system with multiple layers:

#### 1. Admin Authentication (Web UI)

**Password + TOTP 2FA Flow:**

1. **Single Sign-In Form**: Username (blank for `admin`), password and authenticator code are entered together at `/login.html`
2. **Generic Errors**: Any failure shows "Invalid login credentials or OTP", without revealing which field was wrong
3. **JWT Token Issuance**: 24-hour session token created upon successful verification
4. **Session Management**: Token stored in browser localStorage

**Brute-Force Lockout:**
- 3 consecutive failures for the same IP + username lock sign-in for 30 seconds; repeated lockouts escalate to 2, 10 and 30 minutes. A successful sign-in or an hour without failures resets this.
- 10 failures from one IP (any usernames) within 5 minutes lock that IP for 5 minutes.
- While locked the server answers HTTP 429 "Too many failed attempts. Try again in N seconds."

**First-Time Setup:**
```bash
# On first login, the system will:
1. Generate a TOTP secret automatically
2. Display QR code for authenticator app pairing
3. Save credentials to config.json
```

**Compatible Authenticator Apps:**
- Google Authenticator
- Microsoft Authenticator
- Authy
- 1Password
- Any RFC 6238 compliant TOTP app

#### 2. API Authentication (Commands/Reports)

**Bearer Token System:**
- Each command/report can have its own bearer token
- Tokens configured per-command in config.json
- Required for external API access (scripts, CI/CD)
- Admin UI access bypasses bearer tokens (uses JWT instead)

### Security Configuration

```json
{
  "dev_mode": false,
  "admin_password": "your-secure-password",
  "totp_secret": "BASE32ENCODEDSTRING",
  "commands": [
    {
      "cmd_id": "secure-command",
      "bearer_token": "unique-token-here",
      "host_ref": "prod-server",
      "command": "sensitive-operation.sh"
    }
  ]
}
```

#### Development Mode

**Configuration Field:** `dev_mode` (boolean, defaults to `false`)

When `"dev_mode": true` is set in config.json, authentication bypasses are enabled for development convenience:

**Enabled Bypasses:**
- **Password Bypass**: Password `abcdefgh` is accepted for admin login
- **2FA Bypass**: TOTP code `123456` is accepted for 2FA verification

**⚠️ IMPORTANT SECURITY WARNINGS:**
- Development mode is **DISABLED by default** (`false`)
- If `dev_mode` is not specified in config.json, it defaults to `false`
- **NEVER enable dev_mode in production environments**
- These bypasses work independently - you can use the real password with `123456` or `abcdefgh` with real TOTP
- Development mode setting can be changed via hot-reload (no restart required)

**Example Configuration:**
```json
{
  "dev_mode": true,          // Enable for development ONLY
  "admin_password": "letmein",
  "totp_secret": "JBSWY3DPEHPK3PXP"
}
```

**Development Login Flow:**
```bash
# Login with development password and development TOTP code in one request
curl -X POST http://localhost:8080/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"password": "abcdefgh", "totp_code": "123456"}'
```

### Security Best Practices

1. **Disable Development Mode**: Ensure `"dev_mode": false` in production (or omit the field entirely)
2. **Strong Passwords**: Use minimum 12 characters with mixed case, numbers, and symbols
3. **Unique Bearer Tokens**: Generate unique tokens for each command
4. **HTTPS in Production**: Always use TLS/SSL encryption
5. **Network Segmentation**: Restrict access to management networks
6. **Regular Token Rotation**: Update bearer tokens periodically
7. **Audit Logging**: Monitor logs for unauthorized access attempts

---

## Web Administration Interface

### Scheduled Jobs Overview

Click any run on the timeline (or a row in the table view) to open it: Commands and HTTP requests open the Log Details popup, RunBooks open a run-details popup with a Log panel per step. Slots that never ran (upcoming, no record) are not clickable.

The Overview page is the default admin landing page and presents the selected server-local day as a read-only timeline:

- Expected cron occurrences for both Commands and RunBooks
- Matched cron-triggered outcomes, runtime, failures, long runs, and running jobs
- Hourly expected-versus-observed activity and an outcome breakdown
- A sortable and filterable table containing the same occurrence data
- Date navigation, manual refresh, and automatic one-minute refresh while viewing today
- An administrator-only daily email schedule with one recipient, a customizable subject, server-local delivery time, and Previous day or Past 24 hours reporting periods

Historical projections use the cron expressions in the current configuration. Execution results are available only within the configured log-retention window; manual/API command runs and individual RunBook step logs are intentionally excluded.

Daily Overview emails use the global SMTP settings and contain an email-safe version of the KPI cards, job timeline, hourly activity, and outcome summary. The subject supports `{{window_label}}`, `{{period}}`, `{{window_start}}`, and `{{window_end}}`; the default is `Scheduled Jobs Overview — {{window_label}}`. Window timestamps use server-local time. This reporting schedule remains active when Command and RunBook cron execution is globally disabled.

### Dashboard

The dashboard provides real-time system status:

- **Server Statistics**: Uptime, request counts, active executions
- **Recent Activity**: Latest command executions and their status
- **System Health**: Cron status, configuration state, active locks

### Server Status

The Status page (Monitoring → Status) shows three tabs at the top:

- **Server Information** (shown by default): version, uptime, request counts, cron state and server time
- **Active Requests**: commands running right now and how long they have been running
- **Active RunBooks**: RunBooks running right now and which step they are on

Whichever tab is open reloads every 2 seconds, starting with Server Information when the page opens; switching tabs loads fresh data right away. Reloading pauses while the browser tab is in the background and stops when you leave the Status page. Each tab label shows the current count. Below them, under the **Upcoming** heading, a second set of tabs (Scheduled Jobs, Scheduled RunBooks, Scheduled HTTP Requests, each showing its count) lists every cron job sorted by next run, soonest first. Next Run reads like "Today 05:33 PM" or "Tomorrow 01:05 AM" for runs on the server's current or next day, and "2026-10-19 04:00 PM" for later dates; hover over it to see the full timestamp. Scheduled Jobs is shown by default. These tabs are not live-reloaded; they refresh when the page loads or when you click Refresh.

### Navigation Structure

```
Dashboard
├── Hosts          # SSH/WinRM host management
├── Commands       # Command configuration
├── RunBooks       # Ordered multi-command workflows
├── Logs           # Execution history
├── PEM Files      # SSH key management
└── Settings
    ├── Variables  # Configuration variables
    └── SMTP       # Email settings
```

### Host Management Interface

**Features:**
- Add/Edit/Delete SSH and WinRM hosts
- Test connectivity to hosts
- Password and PEM key authentication
- Host protection (prevents deletion when in use)
- Duplicate host detection

**Host Configuration Fields:**
- **Connection Type**: SSH or WinRM
- **Host**: IP address or hostname
- **Port**: Connection port (default: 22 for SSH, 5986 for WinRM)
- **Username**: Authentication username
- **Password**: Authentication password (optional with PEM)
- **PEM File**: Path to SSH private key
- **PEM Password**: Password for encrypted keys

### Command Management Interface

**Features:**
- Create and manage executable commands
- Configure authentication tokens
- Set up notifications (email/webhook)
- Reference Commands from RunBooks for multi-command workflows
- Configure output limits
- Schedule with cron expressions
- Row menu (⋮) per command: **Edit**, **Duplicate**, **Logs** (opens the Logs page filtered to that command) and **Delete**

**Command Fields:**
- **Command ID**: Unique identifier
- **Host Reference**: Target host for execution
- **Command**: Shell command or script
- **Description**: Human-readable description
- **Cron Schedule**: Optional scheduling expression
- **Bearer Token**: API authentication token
- **Max Output Size**: Prevent memory exhaustion
- **Timeout (seconds)**: Stop waiting after this many seconds and report the run as timed out (empty = no limit)
- **Skip If Running**: Skip execution if command already in progress
- **Notifications**: Email and webhook configuration
- **RunBook References**: Commands in use are protected from deletion

### RunBooks Interface

**Features:**
- Build ordered workflows from saved Commands
- Configure retries, delays, timeouts, and stop/continue-on-failure (a Command's own Timeout also applies inside a step)
- Schedule complete RunBooks
- Watch live execution progress and retained history
- Configure consolidated email/webhook summaries

**Step logs in the run view:** each finished step (on the RunBooks page and in the Overview run popup) has a **Log** button and a **Download** button.
- **Log** opens the step's log file inline, showing the last 128 KB (the end of a log is where errors and the exit code are). The status line says how much is shown, e.g. "Showing the last 1,204 lines · 128 KB of 3.4 MB".
- **Load earlier** adds the previous 128 KB each time, up to 2 MB in the page. Beyond that, use Download.
- **Download** saves the complete log file, whatever its size.
- The copy icon (tooltip "Copy to Clipboard") at the right of the status line copies the text shown in the panel. For a large log that is only the loaded part; use Download for the whole file.
- Failed and timed-out steps open their log automatically. An open log stays open, and keeps its scroll position, while a running RunBook refreshes.
- If the log file has been deleted (log retention), the output saved with the run is shown instead. That copy may be cut at the Command's max output size.

See [runbooks.md](runbooks.md) for every required and optional field.

### HTTP Requests Interface

**Features:**
- Save API calls with method, URL, query parameters, headers, auth (basic / bearer / API key) and JSON, text, XML or form bodies
- Timeouts, redirects, TLS verification toggle, retries on network errors and 5xx responses, output size limit
- Success rules: expected status codes (`2xx`, `200,201`, `200-299`) and assertions (body contains, JSON path equals, max response time)
- Run from the page, schedule with cron, trigger externally with a bearer token, or use as a RunBook step
- One log per run with the request, the response headers and the full response body (Recent Logs tab, Logs page)

See [http-requests.md](http-requests.md) for the complete field reference.

### Disk Monitor Interface

Admin-only page under **Monitoring → Disk Monitor**.

**Features:**
- Dashboard of every monitored mount: host, mount, free of total, threshold, % free bar, status (OK / TRIPPED / CHECK FAILED / NEVER CHECKED / DISABLED), last and next check, a 24-reading trend line and per-row actions (Check now, View last log, Edit, Delete); refreshes every 15 seconds
- **Detect volumes**: run `df` on a host and add the real disks in one go (pseudo filesystems such as tmpfs are listed but unticked)
- **Test host**: run `df` for a host immediately and see the per-mount result; writes a log file
- **Settings**: enable/disable, alert cooldown, daily summary time, recipients, default interval and threshold, concurrency, email bar mode, Send test email
- Warning banners when the monitor is disabled, when SMTP or recipients are missing, and when the last email failed

See [disk-monitor.md](disk-monitor.md) for the configuration fields, log format, API and alert rules.

---

## API Reference

### Authentication Endpoints

#### POST /api/auth/login
Verifies username, password and TOTP code in a single request.

**Request:**
```json
{
  "username": "",
  "password": "admin-password",
  "totp_code": "123456"
}
```
A blank `username` means the built-in `admin` account.

**Response (success):**
```json
{
  "success": true,
  "token": "jwt-token-valid-24-hours"
}
```

**Response (any failure, HTTP 401):**
```json
{
  "success": false,
  "error": "Invalid login credentials or OTP"
}
```

After repeated failures the endpoint returns HTTP 429 with a `Retry-After` header (see Brute-Force Lockout above).

Accounts that have not enrolled an authenticator yet (first-time admin setup, new users, forced password change) receive `temp_token`, `needs_totp_setup`/`needs_password_change` and a `qr_code` once the password is verified, and finish enrollment with `/api/auth/verify-2fa` or `/api/auth/complete-onboarding`.

#### POST /api/auth/verify-2fa
Completes authenticator enrollment and issues a JWT token. Only valid for a `temp_token` issued for enrollment, and only from the same IP.

**Request:**
```json
{
  "temp_token": "token-from-login",
  "totp_code": "123456"
}
```

**Response:**
```json
{
  "success": true,
  "token": "jwt-token-valid-24-hours"
}
```

#### GET /api/auth/validate
Validates current JWT token.

**Headers:**
```
Authorization: Bearer <jwt-token>
```

**Response:**
```json
{
  "success": true,
  "valid": true,
  "expires_at": "2024-01-01T12:00:00Z"
}
```

### Command Execution Endpoints

#### GET /api/commands/{cmd_id}
Executes a configured command.

**Headers (if bearer token configured):**
```
Authorization: Bearer <command-bearer-token>
```

**Response:**
```json
{
  "success": true,
  "data": {
    "cmd_id": "backup-db",
    "host": "prod-server",
    "command": "pg_dump database",
    "output": "Database backup completed",
    "exec_time": "2.456s",
    "trace_id": "a1b2c3d4e5f6",
    "is_output_truncated": 0,
    "exit_code": 0
  }
}
```

### RunBook Execution Endpoints

#### POST /api/runbooks/{runbook_id}/runs
Starts an asynchronous RunBook and returns HTTP 202 with a `run_id`.

Use `GET /api/runbooks/{runbook_id}/runs/{run_id}` for live progress and the consolidated result. See [runbooks.md](runbooks.md) for history, stop, download, and authentication details.

### Monitoring Endpoints

#### GET /api/overview/scheduled-jobs
Returns the Day-view schedule projection and matched cron execution history for Commands and RunBooks. The optional `date` query parameter uses `YYYY-MM-DD`; when omitted, the server's current local date is used.

**Headers:**
```
Authorization: Bearer <jwt-token>
```

**Example:**
```text
GET /api/overview/scheduled-jobs?date=2026-07-12
```

The response includes server timezone and retention metadata, jobs with nested occurrences, daily totals, and 24 hourly aggregation buckets.

#### GET/PUT /api/config/scheduled-jobs-overview-email
Reads or updates the administrator-managed daily Overview email schedule. The setting contains `enabled`, `recipient`, `hour`, `minute`, and a `period` of `previous_day` or `past_24_hours`. Enabling delivery requires valid, enabled SMTP settings.

#### GET /api/status
Returns comprehensive server status.

**Headers:**
```
Authorization: Bearer <jwt-token>
```

**Response:**
```json
{
  "success": true,
  "data": {
    "server_uptime": "5d 3h 25m 10s",
    "total_requests_handled": 1523,
    "active_requests": [
      {
        "trace_id": "abc123",
        "cmd_id": "backup-db",
        "host": "prod-server",
        "running_for": "5s",
        "started_at": "2024-01-01T10:00:00Z"
      }
    ],
    "locked_commands": {
      "backup-db": 1
    },
    "cron_enabled": true,
    "scheduled_jobs": [
      {
        "cmd_id": "daily-backup",
        "cron_schedule": "0 2 * * *",
        "next_run": "2024-01-02T02:00:00Z"
      }
    ],
    "config_last_updated": "2024-01-01T08:00:00Z"
  }
}
```

#### GET /api/logs
Lists recent execution logs.

**Headers:**
```
Authorization: Bearer <jwt-token>
```

**Query Parameters:**
- `limit`: Maximum number of logs (default: 100)
- `offset`: Skip first N logs
- `cmd_id`: Filter by command ID
- `host`: Filter by host

**Response:**
```json
{
  "success": true,
  "data": [
    {
      "filename": "2024_01_01_100000_backup-db_abc123_log.txt",
      "cmd_id": "backup-db",
      "trace_id": "abc123",
      "timestamp": "2024-01-01T10:00:00Z",
      "size": 2048,
      "host": "prod-server"
    }
  ]
}
```

#### GET /api/logs/{trace_id}
Downloads complete log file for an execution.

**Response:**
```
Content-Type: text/plain
Content-Disposition: attachment; filename="log.txt"

[Log file contents]
```

### Configuration Management Endpoints

#### GET /api/config
Returns current configuration (sensitive data redacted).

**Headers:**
```
Authorization: Bearer <jwt-token>
```

#### PUT /api/config
Updates entire configuration.

**Headers:**
```
Authorization: Bearer <jwt-token>
Content-Type: application/json
```

**Request:**
```json
{
  "port": 8080,
  "hosts": {...},
  "commands": [...]
}
```

#### GET /api/config/hosts
Lists all configured hosts.

#### POST /api/config/hosts
Adds a new host.

**Request:**
```json
{
  "host_id": "new-server",
  "connection_type": "ssh",
  "host": "192.168.1.100",
  "port": 22,
  "username": "admin",
  "password": "secret"
}
```

#### PUT /api/config/hosts/{host_id}
Updates an existing host.

#### DELETE /api/config/hosts/{host_id}
Deletes a host (if not in use).

#### GET /api/config/commands
Lists all configured commands.

#### POST /api/config/commands
Adds a new command.

#### PUT /api/config/commands/{cmd_id}
Updates an existing command.

#### DELETE /api/config/commands/{cmd_id}
Deletes a command.

#### GET/POST /api/config/runbooks
Lists or creates RunBooks.

#### PUT/DELETE /api/config/runbooks/{runbook_id}
Updates or deletes a RunBook.

#### GET /api/config/variables
Returns configuration variables.

#### PUT /api/config/variables
Updates configuration variables.

#### GET /api/config/smtp
Returns the SMTP configuration using `from_email`, `from_name`, `encryption`,
`enabled`, `timeout`, `retries`, and `skip_cert_verify`. The password is never
returned; `has_password` and `password_length` describe the saved secret.

#### PUT /api/config/smtp
Updates SMTP settings. Omitting or leaving `password` empty preserves the saved
password. `encryption` accepts `none`, `tls` (STARTTLS), or `ssl` (implicit TLS).

#### POST /api/config/smtp/test-email
Sends a real test email using the saved SMTP configuration. Requires an admin
JWT and a JSON body containing the recipient: `{"to":"admin@example.com"}`.
The response confirms the recipient, sender, subject, and send timestamp; SMTP
delivery failures are returned to the UI and logged in `logs/errors_smtp.log`.

### PEM File Management Endpoints

#### GET /api/pemfiles
Lists all stored PEM files.

**Response:**
```json
{
  "success": true,
  "data": [
    {
      "name": "prod-server.pem",
      "size": 1679,
      "modified": "2024-01-01T10:00:00Z"
    }
  ]
}
```

#### POST /api/pemfiles
Uploads a new PEM file.

**Request:**
```
Content-Type: multipart/form-data

file: [PEM file content]
```

#### GET /api/pemfiles/{filename}
Downloads a PEM file.

#### DELETE /api/pemfiles/{filename}
Deletes a PEM file.

#### PUT /api/pemfiles/{filename}/rename
Renames a PEM file.

**Request:**
```json
{
  "new_name": "renamed-key.pem"
}
```

---

## Host Management

### SSH Host Configuration

SSH hosts support multiple authentication methods:

#### Password Authentication
```json
{
  "linux-server": {
    "connection_type": "ssh",
    "host": "192.168.1.100",
    "port": 22,
    "username": "admin",
    "password": "secure-password"
  }
}
```

#### PEM Key Authentication
```json
{
  "secure-server": {
    "connection_type": "ssh",
    "host": "prod.example.com",
    "port": 22,
    "username": "deploy",
    "pem_file": "./pem_files/deploy-key.pem"
  }
}
```

#### Encrypted PEM Key
```json
{
  "encrypted-server": {
    "connection_type": "ssh",
    "host": "secure.example.com",
    "port": 22,
    "username": "admin",
    "pem_file": "./pem_files/encrypted.pem",
    "pem_password": "pem-encryption-password"
  }
}
```

#### Connecting through a bastion (jump host)
Hosts that are only reachable from a bastion can reference another SSH host with `bastion_host_ref`. The app first logs into the bastion with the bastion's own credentials, then opens the target's SSH connection through it (a TCP forward on the bastion connection, like OpenSSH `ProxyJump`). No OpenSSH client is needed on the server running Command Guard.

```json
{
  "bastion": {
    "connection_type": "ssh",
    "host": "bastion.example.com",
    "port": 22,
    "username": "jump",
    "pem_file": "./pem_files/bastion.pem"
  },
  "db-private": {
    "connection_type": "ssh",
    "host": "10.0.1.20",
    "port": 22,
    "username": "postgres",
    "password": "db-password",
    "bastion_host_ref": "bastion"
  }
}
```

- In the Hosts page the Add/Edit Host dialog has a **Connect via bastion host** dropdown (SSH hosts only). The hosts list shows *via bastion* under the address.
- **Single hop only**: the bastion itself cannot use a bastion. Both hosts must be SSH hosts and the bastion needs a password or PEM key. Saving a host with an invalid reference is rejected; `--validate` reports the same problems.
- Commands, RunBooks, Disk Monitor checks and persistent terminals all go through the bastion automatically. Terminals need the `cg_sessiond` daemon version 1.2.0 or newer; if an older daemon is still running, opening a terminal on such a host shows a message asking to restart the daemon (which ends open terminal sessions).
- The bastion's SSH server must allow TCP forwarding (`AllowTcpForwarding yes`, and `PermitOpen` must include the target). If it refuses, the error names the bastion and the sshd option to check.
- A host that other hosts use as bastion cannot be deleted or renamed until those references are removed.

### WinRM Host Configuration

Windows Remote Management configuration:

#### Basic Authentication (HTTP)
```json
{
  "windows-dev": {
    "connection_type": "winrm",
    "host": "win-dev.local",
    "port": 5985,
    "username": "Administrator",
    "password": "admin-password",
    "winrm_https": false
  }
}
```

#### Secure HTTPS Connection
```json
{
  "windows-prod": {
    "connection_type": "winrm",
    "host": "win-prod.example.com",
    "port": 5986,
    "username": "Administrator",
    "password": "secure-password",
    "winrm_https": true,
    "winrm_insecure": true
  }
}
```

#### With CA Certificate
```json
{
  "windows-secure": {
    "connection_type": "winrm",
    "host": "secure.windows.com",
    "port": 5986,
    "username": "admin",
    "password": "password",
    "winrm_https": true,
    "winrm_cacert": "./certs/ca.crt"
  }
}
```

### Host Protection Features

The system prevents accidental misconfiguration:

1. **Usage Detection**: Hosts referenced by commands, RunBooks, Disk Monitor volumes or as bastion by other hosts cannot have their ID changed
2. **Deletion Protection**: Hosts in use cannot be deleted (including hosts used as bastion)
3. **Duplicate Prevention**: Host IDs must be unique
4. **Connection Testing**: Verify connectivity before saving

---

## Command Configuration

### Basic Command Structure

```json
{
  "cmd_id": "unique-identifier",
  "host_ref": "target-host-id",
  "command": "shell command to execute",
  "description": "Human-readable description"
}
```

### Advanced Command Features

#### Scheduled Execution
```json
{
  "cmd_id": "daily-backup",
  "host_ref": "prod-db",
  "command": "/scripts/backup.sh",
  "cron_schedule": "0 2 * * *"  // 2 AM daily
}
```

#### Authenticated Access
```json
{
  "cmd_id": "sensitive-operation",
  "host_ref": "secure-server",
  "command": "sudo /usr/local/bin/critical-task",
  "bearer_token": "secret-token-12345"
}
```

#### Output Size Limiting
```json
{
  "cmd_id": "large-output-command",
  "host_ref": "log-server",
  "command": "tail -n 10000 /var/log/application.log",
  "max_output_size": "5MB"  // Supports B, KB, MB, GB
}
```

#### Execution Time Limit

Stop waiting for a command that runs too long, however it was started (cron, API, the Run button or a RunBook step):

```json
{
  "cmd_id": "nightly-report",
  "host_ref": "report-server",
  "command": "/scripts/build-report.sh",
  "timeout_seconds": 600  // 10 minutes; empty or 0 = no limit
}
```

**Behavior:**
- Set it in the Add/Edit Command dialog under **Scheduling & Security → Timeout (seconds)**
- When the limit is reached the run is recorded with exit code 1 and the error `Timed out: the command was stopped after 10m0s because it reached its time limit of 600 seconds (10m0s). Raise "Timeout (seconds)" in the Edit Command dialog, or make the command finish faster.` The same text appears in the API response, the log file, the Logs page, the Overview and in email/webhook notifications
- The SSH/WinRM session is closed; a process that ignores the closed session may keep running on the remote host
- Inside a RunBook the Command limit, the step timeout and the RunBook's overall timeout all apply; whichever is shortest wins and the message says which one fired
- Commands with a time limit show an hourglass icon in the Commands list

#### Skip If Already Running

Prevent overlapping executions of scheduled or triggered commands:

```json
{
  "cmd_id": "long-running-backup",
  "host_ref": "backup-server",
  "command": "/scripts/full-backup.sh",
  "cron_schedule": "*/5 * * * *",
  "skip_if_running": true
}
```

**Behavior:**
- **Default (false)**: New execution requests wait for the current execution to complete
- **When enabled (true)**: New execution requests are skipped immediately if the command is already running

**Use Cases:**
- Long-running cron jobs that might overlap with the next scheduled run
- Commands that should never have concurrent executions
- Preventing resource contention on the remote host

**Response when skipped (API):**
```json
{
  "success": false,
  "error": "Command 'long-running-backup' is already running and skip_if_running is enabled",
  "data": {
    "trace_id": "abc123",
    "skipped": true,
    "reason": "skip_if_running"
  }
}
```
HTTP Status: **409 Conflict**

### Multi-command workflows

Commands are atomic. Configure all ordered multi-command execution on the **RunBooks** page. See [runbooks.md](runbooks.md) for fields, execution behavior, and APIs.

### Multi-Line Scripts

Support for inline scripts with shebang:

```json
{
  "cmd_id": "complex-script",
  "host_ref": "app-server",
  "command": "#!/bin/bash\nset -e\necho 'Starting deployment'\ncd /app\ngit pull\nnpm install\nnpm run build\necho 'Deployment complete'"
}
```

---

## RunBooks

RunBooks replace both Reports and Command Chaining. They execute saved Commands and HTTP requests as ordered steps and provide scheduling, retries, delays, stop/continue-on-failure, live progress, retained history, and consolidated notifications.

See [runbooks.md](runbooks.md) for the complete schema and API.

## HTTP Requests

Saved HTTP requests (API calls) live in the top-level `http_requests` array:

```json
{
  "request_id": "healthcheck-api",
  "method": "GET",
  "url": "https://api.example.com/health",
  "headers": [{ "key": "Accept", "value": "application/json" }],
  "auth": { "type": "bearer", "token": "${{API_TOKEN}}" },
  "expected_status": "2xx",
  "assertions": [{ "type": "json_path_equals", "path": "status", "value": "ok" }],
  "cron_schedule": "*/5 * * * *",
  "bearer_token": "external-trigger-token"
}
```

Run one with `GET|POST /api/http-requests/{request_id}` (JWT or the request's bearer token). Every run writes a single log file containing the request, the response headers and the full response body; it is listed, viewed, downloaded, uploaded to S3 and cleaned up exactly like Command logs.

See [http-requests.md](http-requests.md) for all fields, authentication and body types, assertions, the log format and the configuration API.

## Disk Monitor

Free-space monitoring lives in the top-level `disk_monitor` block. Each volume is a mount path on one of your SSH hosts:

```json
"disk_monitor": {
  "send_alert_once_every": "4h",
  "daily_summary_time": "08:00",
  "recipients": { "to": ["ops@example.com"] },
  "volumes": [
    { "host_ref": "bastion", "mount_path": "/", "check_interval": "1h", "threshold_value": 5, "threshold_unit": "GB" },
    { "host_ref": "bastion", "mount_path": "/data", "threshold_value": 10, "threshold_unit": "%", "use_sudo": true }
  ]
}
```

Every host check runs one `df -PB1` over SSH and writes one log file (`job_type : disk_check`, Type filter "Disk check" on the Logs page). A volume trips when the available bytes fall below the threshold; one consolidated email (text + HTML) is sent per cooldown period listing every tripped or failing volume and any recoveries, and a daily summary goes out at the configured local time. Emails use the global SMTP settings; without SMTP or recipients the page shows a warning and nothing is sent.

See [disk-monitor.md](disk-monitor.md) for the full field reference, the log format, the API and the alert rules.

## Scheduling and Automation

### Cron Expression Format

Standard Unix cron format with 5 fields:

```
┌───────────── minute (0-59)
│ ┌───────────── hour (0-23)
│ │ ┌───────────── day of month (1-31)
│ │ │ ┌───────────── month (1-12)
│ │ │ │ ┌───────────── day of week (0-6, Sunday=0)
│ │ │ │ │
* * * * *
```

### Common Schedule Examples

| Schedule | Expression | Description |
|----------|------------|-------------|
| Every minute | `* * * * *` | Runs every minute |
| Every hour | `0 * * * *` | Runs at minute 0 of every hour |
| Daily at 2 AM | `0 2 * * *` | Runs at 2:00 AM every day |
| Weekly on Monday | `0 0 * * 1` | Runs at midnight on Mondays |
| Monthly on 1st | `0 0 1 * *` | Runs at midnight on the 1st |
| Every 5 minutes | `*/5 * * * *` | Runs every 5 minutes |
| Business hours | `0 9-17 * * 1-5` | Every hour 9 AM-5 PM weekdays |

### Enabling/Disabling Cron

Global control in config.json:
```json
{
  "cron_enabled": "Y"  // Set to "N" to disable all schedules
}
```

### Scheduled Command Example

```json
{
  "cmd_id": "cleanup-logs",
  "host_ref": "app-server",
  "command": "find /var/log -mtime +30 -delete",
  "cron_schedule": "0 3 * * 0",  // 3 AM every Sunday
  "description": "Weekly log cleanup"
}
```

### Scheduled RunBook Example

```json
{
  "runbook_id": "weekly-summary",
  "description": "Weekly infrastructure summary",
  "cron_schedule": "0 9 * * 1",  // 9 AM every Monday
  "steps": [
    {"step_id": "servers", "cmd_id": "server-stats"},
    {"step_id": "database", "cmd_id": "database-stats"},
    {"step_id": "application", "cmd_id": "application-stats"}
  ],
  "notify_email": {
    "to": ["team@example.com"],
    "subject": "Weekly Infrastructure RunBook"
  }
}
```

The server generates and maintains each step's internal `step_id` automatically when a RunBook is saved in the web UI. It is intentionally hidden from the editor. When authoring `config.json` directly, include a unique `step_id` as shown above.

---

## Notification Systems

### Email Notifications

#### SMTP Configuration

```json
{
  "smtp": {
    "host": "smtp.gmail.com",
    "port": 587,
    "username": "notifications@example.com",
    "password": "app-specific-password",
    "from_email": "noreply@example.com",
    "from_name": "SSH Executor",
    "encryption": "tls",
    "enabled": true,
    "timeout": 30,
    "retries": 3,
    "skip_cert_verify": false
  }
}
```

#### Command-Level Email Configuration

```json
{
  "cmd_id": "critical-task",
  "notify_email": {
    "to": ["admin@example.com", "ops@example.com"],
    "cc": ["manager@example.com"],
    "subject": "{{status}} - {{cmd_id}} on {{host}}",
    "on_success": true,
    "on_failure": true,
    "include_output": true,
    "max_output_lines": 100
  }
}
```

#### Email Template Variables

Available in subject line:
- `{{status}}`: SUCCESS or FAILURE
- `{{cmd_id}}`: Command identifier
- `{{host}}`: Target host
- `{{trace_id}}`: Execution trace ID

#### Email Template Customization

Edit `email.tpl` to customize email body:
```html
<!DOCTYPE html>
<html>
<head>
    <style>
        .header { background: #2c3e50; color: white; padding: 20px; }
        .content { padding: 20px; }
        .footer { background: #ecf0f1; padding: 10px; }
    </style>
</head>
<body>
    <div class="header">
        <h2>{{.Subject}}</h2>
    </div>
    <div class="content">
        <p><strong>Command:</strong> {{.CmdID}}</p>
        <p><strong>Host:</strong> {{.Host}}</p>
        <p><strong>Status:</strong> {{.Status}}</p>
        <pre>{{.Output}}</pre>
    </div>
</body>
</html>
```

### Webhook Notifications

#### Webhook Configuration

```json
{
  "cmd_id": "deployment",
  "notify_webhook": "https://hooks.slack.com/services/T00/B00/XXX"
}
```

#### Webhook Payload

POST request with JSON payload:
```json
{
  "output": "Command output here",
  "error_msg": "",
  "exitcode": 0,
  "trace_id": "abc123def456",
  "request_received_at": "2024-01-01T10:00:00Z",
  "cmd_id": "deployment",
  "host": "prod-server",
  "command": "deploy.sh",
  "remote_ip": "192.168.1.100",
  "agent": "curl/7.68.0",
  "exec_time": "5.234s"
}
```

#### Slack Integration Example

```json
{
  "notify_webhook": "https://hooks.slack.com/services/YOUR/WEBHOOK/URL"
}
```

The webhook automatically formats for Slack compatibility.

#### Custom Webhook Integration

```python
# Example webhook receiver
from flask import Flask, request

app = Flask(__name__)

@app.route('/webhook', methods=['POST'])
def handle_webhook():
    data = request.json
    if data['exitcode'] == 0:
        print(f"Success: {data['cmd_id']} on {data['host']}")
    else:
        print(f"Failure: {data['error_msg']}")
    return '', 200
```

---

## Variable Substitution

### Configuration Variables

Define reusable variables in config.json:

```json
{
  "variables": {
    "admin_email": "admin@example.com",
    "backup_bucket": "s3://my-backups",
    "max_retries": 3,
    "environments": ["dev", "staging", "prod"],
    "default_timeout": 300
  }
}
```

### Using Configuration Variables

Reference with `{{variable_name}}` syntax:

```json
{
  "commands": [
    {
      "cmd_id": "backup-to-s3",
      "command": "aws s3 cp backup.tar.gz {{backup_bucket}}/",
      "notify_email": {
        "to": ["{{admin_email}}"]
      }
    }
  ]
}
```

### Environment Variables

Reference system environment variables with `${{VAR_NAME}}` (note the double curly braces):

```json
{
  "smtp": {
    "password": "${{SMTP_PASSWORD}}"
  },
  "hosts": {
    "prod": {
      "password": "${{PROD_SSH_PASSWORD}}"
    }
  }
}
```

**Important**: Use `${{VAR}}` for environment variables in config.json. Standard bash syntax `${VAR}` is preserved for command scripts.

### Variable Processing Order

1. Environment variables (`${{VAR}}`) are substituted first (at config load)
2. Configuration variables (`{{var}}`) are substituted second (at config load)
3. Bash variables (`${var}`, `$var`) are passed through untouched (handled at runtime)
4. Validation ensures no undefined config-time variables remain

### Complex Variable Examples

```json
{
  "variables": {
    "base_path": "/opt/application",
    "log_level": "info",
    "notification_emails": ["admin@example.com", "ops@example.com"]
  },
  "commands": [
    {
      "cmd_id": "app-restart",
      "command": "cd {{base_path}} && ./restart.sh --log-level={{log_level}}",
      "notify_email": {
        "to": "{{notification_emails}}"
      }
    }
  ]
}
```

### Real-World Variable Usage Examples

#### Example 1: Database Backup with All Variable Types

This example demonstrates using config variables, environment variables, and bash variables together:

```json
{
  "variables": {
    "backup_directory": "/mnt/backups",
    "retention_days": 7
  },
  "commands": [{
    "cmd_id": "daily-db-backup",
    "host_ref": "database-server",
    "command": "TIMESTAMP=$(date +%Y%m%d_%H%M%S) && mysqldump -h ${{DB_HOST}} -u ${{DB_USER}} -p${{DB_PASSWORD}} ${DATABASE_NAME} | gzip > {{backup_directory}}/${DATABASE_NAME}_${TIMESTAMP}.sql.gz && find {{backup_directory}} -name '*.sql.gz' -mtime +{{retention_days}} -delete",
    "description": "Automated database backup with cleanup"
  }]
}
```

**Variable types used**:
- **Environment variables** (substituted at config load): `${{DB_HOST}}`, `${{DB_USER}}`, `${{DB_PASSWORD}}`
- **Config variables** (substituted at config load): `{{backup_directory}}`, `{{retention_days}}`
- **Bash variables** (handled at runtime): `${TIMESTAMP}`, `${DATABASE_NAME}`

**Command after substitution** (assuming env vars: DB_HOST=localhost, DB_USER=admin, DB_PASSWORD=secret):
```bash
TIMESTAMP=$(date +%Y%m%d_%H%M%S) && mysqldump -h localhost -u admin -psecret ${DATABASE_NAME} | gzip > /mnt/backups/${DATABASE_NAME}_${TIMESTAMP}.sql.gz && find /mnt/backups -name '*.sql.gz' -mtime +7 -delete
```

#### Example 2: Multi-Repository Git Sync with Bash Arrays

This demonstrates complex bash syntax that now works correctly:

```json
{
  "variables": {
    "projects_root": "/var/www"
  },
  "commands": [{
    "cmd_id": "sync-git-repos",
    "host_ref": "web-server",
    "command": "declare -A repos; repos['{{projects_root}}/frontend']='main'; repos['{{projects_root}}/backend']='develop'; repos['{{projects_root}}/api']='main'; for repo in \"${!repos[@]}\"; do echo \"Processing: $repo\"; cd \"$repo\" || continue; git fetch --all; git checkout ${repos[$repo]}; git pull origin ${repos[$repo]}; git status; done",
    "description": "Sync multiple git repositories to their respective branches"
  }]
}
```

**After substitution**:
```bash
declare -A repos; repos['/var/www/frontend']='main'; repos['/var/www/backend']='develop'; repos['/var/www/api']='main'; for repo in "${!repos[@]}"; do echo "Processing: $repo"; cd "$repo" || continue; git fetch --all; git checkout ${repos[$repo]}; git pull origin ${repos[$repo]}; git status; done
```

**Note**: All bash syntax (`${!repos[@]}`, `${repos[$repo]}`, `$repo`) works perfectly!

#### Example 3: Environment-Specific Deployment

```json
{
  "variables": {
    "environment": "production",
    "app_name": "myapp"
  },
  "hosts": {
    "deploy-target": {
      "connection_type": "ssh",
      "host": "${{DEPLOY_HOST}}",
      "username": "${{DEPLOY_USER}}",
      "password": "${{DEPLOY_PASSWORD}}"
    }
  },
  "commands": [{
    "cmd_id": "deploy-application",
    "host_ref": "deploy-target",
    "command": "export APP_ENV={{environment}} APP_NAME={{app_name}} && cd /opt/{{app_name}} && ./deploy.sh --config ${APP_ENV}_config.yaml --version ${{DEPLOY_VERSION}}",
    "description": "Deploy application with environment-specific configuration"
  }]
}
```

**Setup**:
```bash
export DEPLOY_HOST="prod.example.com"
export DEPLOY_USER="deployer"
export DEPLOY_PASSWORD="secure-password"
export DEPLOY_VERSION="v2.1.0"
```

**Command after substitution**:
```bash
export APP_ENV=production APP_NAME=myapp && cd /opt/myapp && ./deploy.sh --config ${APP_ENV}_config.yaml --version v2.1.0
```

At runtime, bash expands `${APP_ENV}` to `production`, resulting in: `./deploy.sh --config production_config.yaml --version v2.1.0`

#### Example 4: Secure Secrets Management

Best practice for handling sensitive data:

```json
{
  "smtp": {
    "host": "smtp.gmail.com",
    "port": 587,
    "username": "${{SMTP_USERNAME}}",
    "password": "${{SMTP_PASSWORD}}",
    "from_email": "alerts@company.com",
    "from_name": "Alerts",
    "encryption": "tls"
  },
  "hosts": {
    "production": {
      "connection_type": "ssh",
      "host": "${{PROD_SSH_HOST}}",
      "username": "${{PROD_SSH_USER}}",
      "pem_file": "/secure/keys/prod.pem",
      "pem_password": "${{PROD_PEM_PASSWORD}}"
    }
  },
  "commands": [{
    "cmd_id": "s3-backup",
    "host_ref": "production",
    "command": "AWS_ACCESS_KEY_ID=${{AWS_KEY}} AWS_SECRET_ACCESS_KEY=${{AWS_SECRET}} aws s3 sync /data/ s3://${{S3_BUCKET}}/backups/",
    "description": "Backup to S3 with credentials from environment"
  }]
}
```

**Security Benefits**:
- Credentials never stored in config.json
- Environment variables can be set via systemd, docker, or shell
- Easy rotation without modifying configuration
- No accidental commits of secrets to version control

### Variable Syntax Quick Reference

The system uses three distinct variable syntaxes to prevent conflicts:

| Variable Type | Syntax | When Processed | Example Usage |
|--------------|--------|----------------|---------------|
| **Config variables** | `{{variable_name}}` | Config load/reload | `{{backup_bucket}}`, `{{admin_email}}` |
| **Environment variables** | `${{ENV_VAR}}` | Config load/reload | `${{SMTP_PASSWORD}}`, `${{DB_HOST}}` |
| **Bash variables** | `${var}` or `$var` | Command runtime | `${filename}`, `$i`, `${arr[@]}` |

**Why these different syntaxes?**
- **`${{VAR}}`** for environment variables uses double curly braces to avoid conflicts with bash's `${var}` syntax
- This design allows complex bash scripts with arrays, loops, and conditionals to work seamlessly
- Clear separation between config-time substitution and runtime bash variable expansion

**Example showing all three types**:
```json
{
  "variables": {
    "backup_dir": "/mnt/backups"
  },
  "commands": [{
    "command": "for file in *.sql; do mysqldump -h ${{DB_HOST}} $file | gzip > {{backup_dir}}/${file}.gz; done"
  }]
}
```
After substitution: `for file in *.sql; do mysqldump -h prod-db.internal $file | gzip > /mnt/backups/${file}.gz; done`
- `${{DB_HOST}}` → replaced with environment variable value
- `{{backup_dir}}` → replaced with config variable value
- `${file}` → left for bash to expand at runtime

---

## Logging and Monitoring

### Log File Structure

Each execution creates a detailed log file:

**Filename Format:**
```
logs/YYYY_MM_DD_HHMMSS_cmdid_traceid_log.txt
```

**Example:**
```
logs/2024_01_01_143022_backup-db_a1b2c3d4_log.txt
```

### Log File Contents

```
================================================================================
Trace ID: a1b2c3d4e5f6
Request received at: 2024-01-01 14:30:22
Command ID: backup-db
Host: prod-server (192.168.1.100)
Command: pg_dump production > /backups/prod.sql
Remote IP: 10.0.0.50
User Agent: curl/7.68.0
Previous Trace ID: xyz789 (for chained commands)
================================================================================

Command Output:
---------------
Database backup started...
Dumping schema...
Dumping data...
Backup completed successfully.
Size: 1.2GB

================================================================================
Execution completed at: 2024-01-01 14:30:27
Execution time: 5.234s
Exit Code: 0
Output truncated: No
================================================================================
```

### Runs Interrupted by a Restart
While a job runs, its log says `error : job running` / `exitcode : -1`, and the Overview, Logs page and daily email show it as **Running**. If Command Guard is stopped or restarted while the job is still running (shutdown waits only 10 seconds), the final result is never written. On the next start, Command Guard finds these logs and marks them **Interrupted** (exit code 1, shown as Failed). The error message gives the time of the job's last output and warns that the remote process may still be running on the host. Set a **Timeout (seconds)** on long jobs, so a stalled job is stopped instead of hanging.

### Log Rotation and Management

```bash
# Clean up old logs (30+ days)
find logs/ -name "*.txt" -mtime +30 -delete

# Archive logs
tar -czf logs_archive_$(date +%Y%m).tar.gz logs/*.txt

# Search logs by command ID
grep -l "Command ID: backup-db" logs/*.txt

# Find failed executions
grep -l "Exit Code: 1" logs/*.txt
```

### SMTP Error Logging

Email delivery failures logged to:
```
logs/errors_smtp.log
```

Format:
```
2024-01-01 10:00:00 - Failed to send email: dial tcp smtp.gmail.com:587: timeout
2024-01-01 10:05:00 - SMTP authentication failed: 535 Authentication failed
```

### Monitoring Best Practices

1. **Log Retention**: Implement rotation policy
2. **Disk Space**: Monitor logs directory size
3. **Error Monitoring**: Watch errors_smtp.log
4. **Performance Tracking**: Analyze execution times
5. **Audit Trail**: Preserve logs for compliance

---

## PEM File Management

### PEM File Storage

SSH keys are securely stored in `./pem_files/` directory:

```bash
pem_files/
├── production.pem
├── staging.pem
├── dev-team.pem
└── deploy-key.pem
```

### Uploading PEM Files

#### Via Web UI
1. Navigate to Settings → PEM Files
2. Click "Upload PEM File"
3. Select file and upload
4. File automatically saved with .pem extension

#### Create from Pasted RSA Key
1. Navigate to Settings → PEM Files
2. Click "Create PEM file"
3. Enter the file name
4. Paste the full `-----BEGIN RSA PRIVATE KEY-----` block into the text area
5. Click "Create"

The pasted key is saved through the same PEM upload path, so duplicate-name checks, admin authentication, storage location, and file permissions are the same as uploaded files.

#### Via API
```bash
curl -X POST \
  -H "Authorization: Bearer <jwt-token>" \
  -F "file=@/path/to/key.pem" \
  http://localhost:8080/api/pemfiles
```

### Managing PEM Files

#### List Files
```bash
curl -H "Authorization: Bearer <jwt-token>" \
  http://localhost:8080/api/pemfiles
```

#### Download File
```bash
curl -H "Authorization: Bearer <jwt-token>" \
  http://localhost:8080/api/pemfiles/production.pem \
  -o production.pem
```

#### Delete File
```bash
curl -X DELETE \
  -H "Authorization: Bearer <jwt-token>" \
  http://localhost:8080/api/pemfiles/old-key.pem
```

### PEM File Security

1. **File Permissions**: Set to 600 (read/write owner only)
2. **Encrypted Keys**: Support for password-protected PEM files
3. **Access Control**: Only admin users can manage PEM files
4. **Path Validation**: Prevents directory traversal attacks

### Using PEM Files in Configuration

```json
{
  "hosts": {
    "secure-server": {
      "connection_type": "ssh",
      "host": "secure.example.com",
      "username": "deploy",
      "pem_file": "./pem_files/deploy-key.pem",
      "pem_password": "optional-encryption-password"
    }
  }
}
```

---

## Configuration Management

### Configuration File Structure

```json
{
  "port": 8080,
  "bind_address": "0.0.0.0",
  "access_url": "https://executor.example.com",
  "cron_enabled": "Y",
  "admin_password": "secure-password",
  "totp_secret": "BASE32SECRET",
  "variables": {},
  "smtp": {},
  "hosts": {},
  "commands": [],
  "runbooks": []
}
```

### Hot Configuration Reload

The system automatically detects configuration changes:

1. **File Hash Monitoring**: SHA256 hash checked every few seconds
2. **Automatic Reload**: Changes applied without restart
3. **Validation**: New configuration validated before applying
4. **Exclusions**: Port and bind_address require restart

### Configuration Backup

```bash
# Backup current configuration
cp config.json config.backup.$(date +%Y%m%d).json

# Version control
git add config.json
git commit -m "Configuration update"
```

### Configuration Validation

```bash
# Validate before applying
./cguard --validate

# Validate custom config file
./cguard --validate --config=/etc/shexec/prod.json
```

### Validation Checks

The validator performs comprehensive checks:

1. **JSON Syntax**: Valid JSON structure
2. **Required Fields**: All mandatory fields present
3. **Duplicate IDs**: No duplicate cmd_id or host_id
4. **References**: Valid `host_ref` and RunBook `cmd_id` references
5. **File Paths**: PEM files exist and readable
6. **Cron Expressions**: Valid cron syntax
7. **Output Sizes**: Valid size format (B, KB, MB, GB)
8. **Variables**: All referenced variables defined
9. **Security**: Dangerous commands have authentication

### Environment-Specific Configuration

```bash
# Development
./cguard --config=config.dev.json

# Staging
./cguard --config=config.staging.json

# Production
./cguard --config=config.prod.json
```

---

## Command Line Interface

Run these commands from the folder that holds `config.json`, `ui/` and the binary: `instance/` when you use `compile_and_run.sh`, `/app/data` in Docker.

### Basic Usage

```bash
./cguard [OPTIONS]
```

### Available Options

| Option | Description | Default |
|--------|-------------|---------|
| `--validate` | Validate configuration and exit | - |
| `--config` | Path to configuration file | config.json |
| `--help` | Show help message | - |
| `--version` | Show version information | - |

### Examples

#### Standard Operation
```bash
# Start with default config
./cguard

# Start with custom config
./cguard --config=/etc/shexec/production.json
```

#### Configuration Validation
```bash
# Validate default config
./cguard --validate

# Validate specific config
./cguard --validate --config=test-config.json

# Validate with environment variables
SMTP_PASSWORD=secret ./cguard --validate
```

### Exit Codes

| Code | Description |
|------|-------------|
| 0 | Success |
| 1 | Configuration validation failed |
| 2 | Configuration file not found |
| 3 | Port binding failed |
| 4 | Initialization error |

### Signal Handling

| Signal | Action |
|--------|--------|
| SIGINT (Ctrl+C) | Graceful shutdown |
| SIGTERM | Graceful shutdown |
| SIGHUP | Reload configuration (future) |

---

## Troubleshooting

### Common Issues and Solutions

#### Authentication Issues

**Problem**: Cannot login to admin interface
```bash
# Solution 1: Reset admin password
# Edit config.json and remove admin_password and totp_secret
# Restart service and set up new credentials

# Solution 2: Check browser console for errors
# Clear browser cache and cookies
```

**Problem**: 2FA code not accepted
```bash
# Check system time synchronization
timedatectl status

# Ensure time is synchronized
sudo timedatectl set-ntp true
```

#### Connection Issues

**Problem**: Cannot connect to SSH host
```bash
# Test SSH connectivity manually
ssh user@host -p 22

# Check firewall rules
sudo iptables -L | grep 22

# Verify SSH service
systemctl status sshd
```

**Problem**: WinRM connection fails
```powershell
# On Windows host, enable WinRM
Enable-PSRemoting -Force

# Configure WinRM for HTTPS
New-SelfSignedCertificate -DnsName "hostname" -CertStoreLocation Cert:\LocalMachine\My

# Set WinRM to use HTTPS
winrm quickconfig -transport:https
```

#### Performance Issues

**Problem**: Commands timing out
```json
{
  "commands": [{
    "max_output_size": "10MB",  // Increase if needed
    "timeout_seconds": 300      // Add timeout
  }]
}
```

**Problem**: High memory usage
```bash
# Check log file sizes
du -sh logs/

# Clean old logs
find logs/ -mtime +30 -delete

# Limit output size in commands
"max_output_size": "1MB"
```

#### Email Notification Issues

**Problem**: Emails not sending
```bash
# Check SMTP error log
tail -f logs/errors_smtp.log

# Test SMTP connection
telnet smtp.gmail.com 587

# Verify credentials
# For Gmail, use app-specific password
```

#### Cron Job Issues

**Problem**: Scheduled commands not running
```bash
# Check cron is enabled
grep "cron_enabled" config.json

# Verify cron expression
# Use online cron expression validator

# Check server logs for cron execution
grep "cron-scheduler" logs/*.txt
```

### Debug Mode

Enable verbose logging (future feature):
```json
{
  "debug_mode": true,
  "log_level": "debug"
}
```

### Health Checks

#### System Health Check Script
```bash
#!/bin/bash
# health_check.sh

# Check service is running
curl -f http://localhost:8080/api/status || exit 1

# Check disk space
df -h | grep -E "logs|mail_queue"

# Check recent executions
find logs/ -mmin -60 -name "*.txt" | wc -l
```

---

## Best Practices

### Security Best Practices

1. **Use HTTPS in Production**
   ```nginx
   server {
       listen 443 ssl;
       ssl_certificate /etc/ssl/certs/executor.crt;
       ssl_certificate_key /etc/ssl/private/executor.key;
       
       location / {
           proxy_pass http://localhost:8080;
           proxy_set_header X-Real-IP $remote_addr;
       }
   }
   ```

2. **Implement Network Segmentation**
   - Run service in DMZ or management network
   - Use firewall rules to restrict access
   - Implement VPN for remote access

3. **Regular Security Updates**
   ```bash
   # Update dependencies
   go get -u ./...
   go mod tidy
   
   # Rebuild and restart (builds into instance/)
   ./compile_and_run.sh
   ```

4. **Audit Logging**
   - Preserve all execution logs
   - Ship logs to SIEM system
   - Monitor for suspicious patterns

### Operational Best Practices

1. **High Availability Setup**
   ```yaml
   # docker-compose-ha.yml
   version: '3.8'
   services:
     executor1:
       image: ssh-executor
       volumes:
         - shared-config:/app/config
         - shared-logs:/app/logs
     
     executor2:
       image: ssh-executor
       volumes:
         - shared-config:/app/config
         - shared-logs:/app/logs
   ```

2. **Monitoring and Alerting**
   ```yaml
   # prometheus.yml
   scrape_configs:
     - job_name: 'ssh-executor'
       static_configs:
         - targets: ['localhost:8080']
       metrics_path: '/metrics'
   ```

3. **Backup Strategy**
   ```bash
   #!/bin/bash
   # Daily backup script
   BACKUP_DIR="/backups/ssh-executor"
   DATE=$(date +%Y%m%d)
   
   tar -czf $BACKUP_DIR/config_$DATE.tar.gz config.json pem_files/
   tar -czf $BACKUP_DIR/logs_$DATE.tar.gz logs/
   
   # Retain 30 days
   find $BACKUP_DIR -mtime +30 -delete
   ```

### Performance Optimization

1. **Command Output Management**
   - Set appropriate `max_output_size` limits
   - Use `head` or `tail` in commands when possible
   - Stream large outputs to files on remote host

2. **Connection Pooling**
   - Reuse SSH connections when possible
   - Configure SSH ControlMaster in ssh_config

3. **Resource Limits**
   ```bash
   # systemd service limits
   [Service]
   LimitNOFILE=65536
   LimitNPROC=4096
   MemoryLimit=2G
   ```

### Development Best Practices

1. **Testing Commands**
   ```json
   {
     "cmd_id": "test-command",
     "host_ref": "dev-server",
     "command": "echo 'test' && exit 0",
     "description": "Safe test command"
   }
   ```

2. **Gradual Rollout**
   - Test on development environment
   - Deploy to staging with limited scope
   - Monitor for 24 hours before production

3. **Documentation**
   - Document all custom commands
   - Maintain runbooks for common tasks
   - Keep configuration changelog

---

## Advanced Features

### Custom Scripts and Heredoc Support

The system automatically translates shebang scripts to heredoc format:

```json
{
  "command": "#!/bin/bash\nset -e\necho 'Multi-line script'\nexit 0"
}
```

Becomes:
```bash
bash <<'EOF'
set -e
echo 'Multi-line script'
exit 0
EOF
```

### Execution Locking

Prevents concurrent execution of the same command:

- **Per-Command Locks**: Each cmd_id has its own lock
- **Automatic Cleanup**: Locks released after 5 minutes of inactivity
- **Queue Behavior**: Second request waits for first to complete

### Skip If Already Running

An alternative to the default queue behavior for commands that should never overlap:

#### When to Use

| Scenario | Recommendation |
|----------|----------------|
| Short commands with frequent scheduling | Use default (queue) |
| Long-running backups scheduled every N minutes | Use `skip_if_running: true` |
| Commands that must complete before next run | Use `skip_if_running: true` |
| Critical commands that should always run | Use default (queue) |

#### Behavior by Trigger Type

| Trigger | Default Behavior | With skip_if_running |
|---------|-----------------|---------------------|
| **Web API** | Waits for lock, then executes | Returns 409 Conflict immediately |
| **Cron Schedule** | Waits for lock, then executes | Silently skips, logs to console |
| **RunBook Step** | Waits for lock, then executes | Records a skipped step and applies its failure policy |

#### Console Log Messages

When a command is skipped, you'll see messages like:
```
[CRON] Skipping backup-db - already running (skip_if_running enabled)
[RUNBOOK] Step backup-db skipped - already running
```

#### Example: Preventing Backup Overlap

```json
{
  "cmd_id": "nightly-backup",
  "host_ref": "database-server",
  "command": "/scripts/full-backup.sh",
  "description": "Full database backup (takes ~4 hours)",
  "cron_schedule": "0 * * * *",
  "skip_if_running": true,
  "notify_email": {
    "to": ["admin@example.com"],
    "on_failure": true
  }
}
```

In this example:
- The backup runs hourly via cron
- If the backup takes longer than 1 hour, the next scheduled run is skipped
- No pile-up of backup processes occurs
- Failures still trigger email notifications

### Output Truncation

Intelligent output management:

1. **API Response**: Truncated to configured limit
2. **Log Files**: Always contain complete output
3. **Truncation Indicator**: `is_output_truncated` field in response
4. **Download Full Output**: Use `/api/logs/{trace_id}` endpoint

### RunBook Workflow Examples

Backup, copy, restore, and conditional deployment sequences are configured as ordered RunBook steps. Use `on_failure: "stop"` for deployment gates and `delay_before_seconds` for waits between Commands. See [runbooks.md](runbooks.md).

### Dynamic Configuration with Variables

#### Environment-Based Configuration
```bash
# Set environment variables
export DB_HOST="prod-db.example.com"
export DB_PASSWORD="secure-password"

# Reference in config.json (note: use ${{VAR}} for env vars)
{
  "commands": [{
    "command": "mysqldump -h ${{DB_HOST}} -p${{DB_PASSWORD}} > /mnt/backups/dump.sql"
  }]
}

# After substitution, becomes:
# mysqldump -h prod-db.example.com -psecure-password > /mnt/backups/dump.sql
```

#### Mixing Config-Time and Runtime Variables
```json
{
  "variables": {
    "backup_dir": "/mnt/backups"
  },
  "commands": [{
    "cmd_id": "backup-all-dbs",
    "command": "for db in db1 db2 db3; do mysqldump -h ${{DB_HOST}} $db > {{backup_dir}}/${db}.sql; done"
  }]
}
```
After substitution: `for db in db1 db2 db3; do mysqldump -h prod-db.example.com $db > /mnt/backups/${db}.sql; done`

Note: `${db}` is a bash variable handled at runtime, while `${{DB_HOST}}` and `{{backup_dir}}` are substituted at config load.

#### Template-Based Commands
```json
{
  "variables": {
    "services": ["nginx", "mysql", "redis"],
    "restart_command": "systemctl restart"
  },
  "commands": [
    {
      "cmd_id": "restart-services",
      "command": "for svc in {{services}}; do {{restart_command}} $svc; done"
    }
  ]
}
```

### Integration Examples

#### CI/CD Pipeline Integration
```yaml
# .gitlab-ci.yml
deploy:
  script:
    - |
      curl -H "Authorization: Bearer $DEPLOY_TOKEN" \
        "https://executor.example.com/api/commands/deploy-app"
```

#### Monitoring Integration
```python
# nagios_check.py
import requests
import sys

response = requests.get(
    "https://executor.example.com/api/commands/health-check",
    headers={"Authorization": f"Bearer {TOKEN}"}
)

if response.json()["data"]["exit_code"] == 0:
    print("OK - Health check passed")
    sys.exit(0)
else:
    print("CRITICAL - Health check failed")
    sys.exit(2)
```

#### Slack Bot Integration
```javascript
// slack-bot.js
const { App } = require('@slack/bolt');

app.command('/execute', async ({ command, ack, respond }) => {
    await ack();
    
    const response = await fetch(`${EXECUTOR_URL}/api/commands/${command.text}`, {
        headers: { 'Authorization': `Bearer ${TOKEN}` }
    });
    
    const result = await response.json();
    await respond({
        text: `Command executed: ${result.data.output}`
    });
});
```

### Custom Email Templates

Create sophisticated HTML email templates:

```html
<!-- email.tpl -->
<!DOCTYPE html>
<html>
<head>
    <style>
        .success { color: green; }
        .failure { color: red; }
        .output { 
            background: #f4f4f4; 
            padding: 10px; 
            border-radius: 5px;
            font-family: monospace;
        }
    </style>
</head>
<body>
    <h2 class="{{.StatusClass}}">{{.Status}}</h2>
    <table>
        <tr><td>Command:</td><td>{{.CmdID}}</td></tr>
        <tr><td>Host:</td><td>{{.Host}}</td></tr>
        <tr><td>Duration:</td><td>{{.ExecTime}}</td></tr>
        <tr><td>Trace ID:</td><td>{{.TraceID}}</td></tr>
    </table>
    
    <h3>Output:</h3>
    <div class="output">{{.Output}}</div>
    
    <p>
        <a href="{{.AccessURL}}/api/logs/{{.TraceID}}">
            View Full Log
        </a>
    </p>
</body>
</html>
```

---

## Appendix

### Port Requirements

| Service | Default Port | Protocol | Direction |
|---------|-------------|----------|-----------|
| HTTP API | 8080 | TCP | Inbound |
| SSH | 22 | TCP | Outbound |
| WinRM HTTP | 5985 | TCP | Outbound |
| WinRM HTTPS | 5986 | TCP | Outbound |
| SMTP | 25/587/465 | TCP | Outbound |

### File Permissions

| Directory/File | Recommended | Description |
|----------------|-------------|-------------|
| cguard | 755 | Executable |
| config.json | 600 | Configuration |
| pem_files/ | 700 | PEM storage |
| logs/ | 755 | Log files |
| mail_queue/ | 755 | Email queue |

### Resource Requirements

| Component | Minimum | Recommended |
|-----------|---------|-------------|
| CPU | 1 core | 2+ cores |
| Memory | 512 MB | 2 GB |
| Disk | 1 GB | 10 GB |
| Network | 10 Mbps | 100 Mbps |

### Support and Resources

- **Documentation**: This manual
- **Configuration Reference**: CLAUDE.md
- **Source Code**: Available on GitHub
- **Issue Tracking**: GitHub Issues
- **Community Support**: Discord/Slack channel

### Version History

| Version | Date | Changes |
|---------|------|---------|
| 1.0.0 | 2024-01 | Initial release |
| 1.1.0 | 2024-02 | Added WinRM support |
| 1.2.0 | 2024-03 | Added 2FA authentication |
| 1.3.0 | 2024-04 | Added report system |
| 1.4.0 | 2024-05 | Added PEM file management |
| 1.5.0 | 2026-01 | Added "Skip If Already Running" feature for commands |

---

## Quick Reference Card

### Common API Calls

```bash
# Login and get JWT token (password + current TOTP code in one request)
JWT=$(curl -s -X POST http://localhost:8080/api/auth/login \
  -d '{"password":"admin","totp_code":"123456"}' | jq -r .token)

# Execute command
curl -H "Authorization: Bearer $JWT" \
  http://localhost:8080/api/commands/backup-db

# Get status
curl -H "Authorization: Bearer $JWT" \
  http://localhost:8080/api/status

# Start RunBook
curl -X POST -H "Authorization: Bearer $JWT" \
  http://localhost:8080/api/runbooks/daily-health/runs

# Send a saved HTTP request
curl -X POST -H "Authorization: Bearer $JWT" \
  http://localhost:8080/api/http-requests/healthcheck-api
```

### Configuration Quick Start

```json
{
  "port": 8080,
  "hosts": {
    "server1": {
      "connection_type": "ssh",
      "host": "192.168.1.10",
      "username": "admin",
      "password": "password"
    }
  },
  "commands": [
    {
      "cmd_id": "hello",
      "host_ref": "server1",
      "command": "echo 'Hello World'"
    },
    {
      "cmd_id": "long-task",
      "host_ref": "server1",
      "command": "/scripts/backup.sh",
      "cron_schedule": "0 * * * *",
      "skip_if_running": true
    }
  ]
}
```

### Cron Quick Reference

```
# Every minute:        * * * * *
# Every hour:          0 * * * *
# Every day at 2 AM:   0 2 * * *
# Every Monday:        0 0 * * 1
# Every 15 minutes:    */15 * * * *
```

---

*End of User Manual - Version 2.0*
