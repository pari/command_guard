# CommandGuard: guide for AI coding agents and contributors

CommandGuard is an open-source (MIT), self-hosted operations console written in Go. It runs commands on remote hosts over SSH (Linux/Unix) or WinRM (Windows) and adds RunBooks, saved HTTP requests, persistent browser terminals, Disk Monitor, VNC remote desktops, multi-user access control with TOTP 2FA, and a web admin UI.

This file is the architecture map and rulebook for anyone changing the code, human or AI. User-facing documentation lives in `README.md` and `docs/`.

## Development rules (read first)

### When in doubt, ask; don't code
If a request seems technically wrong, contradicts the architecture, or mixes up concepts (for example top-level config parameters vs user-defined `variables`, the wrong endpoint or the wrong file):
1. Stop and explain why the request may not be right.
2. Describe the distinction that matters.
3. Ask for confirmation before writing code. Do not implement something you believe is wrong, even when asked.

### Never remove existing functionality
- Do not remove or simplify UI components (for example replacing a modal with `alert()`), form fields, configuration options, validation or error handling.
- Do not remove features as a side effect of another change.
- If something must change, explain what and why, and wait for confirmation first.

### This is a public open-source repository
- **Never commit secrets or private data**: no real `config.json`, PEM keys, `test_env.json`, passwords, tokens, internal host names, IP addresses, e-mail addresses or customer names. `.gitignore` covers the usual files; check `git status` before committing.
- **Use neutral example data** in code, tests and docs: `example.com` / `example.org` domains, the documentation IP ranges `192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24` (or RFC 1918 ranges), and generic names like `web-01`, `db-01`, `deploy`.
- **Keep the docs in step with the code.** A behaviour change updates the matching file in `docs/` and this file; a new feature also gets a line in `README.md`.
- **Security fixes and reports** follow `SECURITY.md`. If a change removes one of the known limitations listed there, update that list.
- The project is MIT licensed. Vendored third-party code keeps its own licence; list any new bundled library in `THIRD_PARTY_NOTICES.md` and put licence texts that must travel with it in `ui/licenses/`.

## Features at a glance
- **Commands**: saved shell commands bound to a Host; run from the UI, by API with a bearer token, or by cron. Output size limit, time limit, skip-if-running, email and webhook notifications, local or S3 log storage.
- **RunBooks**: ordered steps (Commands or HTTP requests) with delays, retries, timeouts and stop/continue-on-failure, live progress and a consolidated result; see `docs/runbooks.md`.
- **HTTP Requests**: saved API calls with auth, retries, expected status and assertions; see `docs/http-requests.md`.
- **Persistent Terminals**: browser SSH terminals (xterm.js) that survive logout and `cguard` restarts via the `cg_sessiond` companion daemon, with image/file drop and Agent Sessions presets; see `docs/terminals.md`.
- **Disk Monitor**: free-space checks over SSH with thresholds, alert emails with cooldown and a daily summary; see `docs/disk-monitor.md`.
- **Remote Desktops (VNC)**: noVNC in the browser, direct or through an SSH tunnel; the server authenticates to the VNC server so passwords never reach the browser; see `docs/vnc.md`.
- **Users & Security**: built-in `admin` plus regular users with explicit grants (commands, RunBooks, HTTP requests, terminal hosts, remote desktops), IP / weekday / time-of-day sign-in policies, temporary any-IP access, login lockout and a security audit log; see `docs/multi-user.md`.
- **Overview & Dashboard**: day view of expected vs observed scheduled runs, with an optional daily overview email.
- **Operations**: bastion (jump) hosts, hot config reload, encrypted backup and restore, PEM key management, global variables, SMTP settings, `--validate` for CI.

## Repository layout

| Path | Responsibility |
|---|---|
| `main.go` | Entry point and flags, `Config` types, `ConfigCache` hot reload, variable substitution, `--validate`, `ExecutionManager` locking, SSH/WinRM executors (`SSHExecutor`, bastion dialing), command handler, cron scheduler, log files, S3 log upload/download, webhooks, email queue, cleanup and graceful shutdown, route registration |
| `config_api.go` | `/api/config/*` admin API (hosts, commands, SMTP, variables, logs listing/deletion, S3 test) and `mutateConfig`, the only safe way to write `config.json` |
| `auth.go`, `users_api.go`, `login_limiter.go`, `audit.go` | Sign-in, TOTP, JWT, middleware (`AuthMiddleware`, `AdminMiddleware`), users and grants, account password, brute-force lockout, security audit log (`logs/audit/`) |
| `auth_prod.go` | Build-tag stub (`!dev`); the `dev_mode` bypass itself is implemented in `auth.go` |
| `runbooks.go` | RunBook config API, async runner, progress and history |
| `http_requests.go` | Saved HTTP requests: config API, runner, cron, logging |
| `overview.go`, `overview_email.go` | Scheduled-jobs overview API and the daily overview email |
| `backup_api.go`, `restore_gate.go` | Encrypted backup export/inspect/restore (scrypt + AES-GCM) and the gate that pauses jobs during a restore |
| `pemfiles_api.go` | PEM key upload/list/delete under `pem_files/` |
| `log_access.go`, `log_chunk.go`, `log_interrupt.go` | Per-user log access rules, ranged log reads, marking logs interrupted by a restart |
| `smtp_transport.go` | SMTP sending (TLS/SSL/none) |
| `disk_monitor*.go` | Disk Monitor config/API, df runner, state, scheduler, alerts, email bodies |
| `terminals*.go`, `sessiond/` | Terminal REST/WebSocket bridge, Agent Sessions, image/file upload; `sessiond/` is the `cg_sessiond` daemon |
| `vnc.go`, `vnc_rfb.go`, `vnc_sessions.go` | VNC config/API, RFB handshake and filter, runtime sessions |
| `version.go` | `AppVersion` (stamped into `config.json` as `version`) |
| `ui/` | Static web UI (jQuery + Bootstrap, one component per page in `ui/js/components/`), vendored libraries, `ui/licenses/` |
| `docs/` | User documentation and README screenshots |
| `Dockerfile`, `docker-entrypoint.sh` | Container image; all state lives in the `/app/data` volume |
| `config.json.sample`, `test_config.json`, `test_env.example.json` | Sample configuration and test fixtures (no real credentials) |

### Runtime files
All runtime paths are relative to the working directory, which is `instance/` for local runs (built by `compile_and_run.sh`) and `/app/data` in Docker. Everything there is git-ignored: `config.json`, `pem_files/`, `logs/` (execution logs, `logs/audit/`, `logs/disk_monitor/state.json`), `mail_queue/`, `run/` (`sessiond.sock`, terminal order/meta/personal presets, VNC thumbnails). The server also serves `./ui/` (in `instance/` a copy refreshed on every build) and reads `./email.tpl` (copied once, so local customisations survive).

## Build, run and flags

The main binary is `cguard`; the terminal daemon `cg_sessiond` must sit in the same folder. Local builds go into the self-contained `instance/` folder, and the server runs from inside it.

```bash
./compile_and_run.sh                 # build into instance/, refresh instance/ui, start from instance/
cd instance
./cguard --validate                  # check config and exit
./cguard --config /path/to/other.json
./cguard --version
```

`compile_and_run.sh` builds both binaries into `instance/`, replaces `instance/ui/` with a fresh copy of `ui/`, copies `email.tpl` and creates `config.json` from the sample only when missing, stops a running `cguard` (`pkill -x cguard`) and starts the new one. It deliberately leaves `cg_sessiond` running so open terminals survive. `instance/` holds real config, keys and logs: never delete it as build output. The binary was called `shexec_go` before version 0.7.0's open-source release; `.gitignore` still ignores that name for older deploy scripts that build with `-o shexec_go`. Docker: `docker build -t commandguard .` then run with a volume on `/app/data`; see `README.md`.

First start: when `admin_password` is empty, the sign-in page asks for a new admin password (12–128 characters) and enrolls TOTP. A single CTRL+C shuts down gracefully (10 s grace period).

### --validate checks
JSON syntax, duplicate command IDs and host names, references (Command hosts, RunBook steps, bastions), missing PEM files, cron expressions, output size formats, negative timeouts, undefined `${{ENV}}` / `{{config}}` variables, the HTTP request, user/security, terminals and Agent Session, Disk Monitor and VNC blocks. It also warns about risky settings: dangerous commands without a bearer token, email without SMTP, incomplete S3 settings, skipped TLS verification, unencrypted direct VNC. Warnings do not fail validation.

## Configuration

Top-level keys of `config.json` (see `type Config` in `main.go`):

| Key | Meaning |
|---|---|
| `version` | Written by the app (`AppVersion`) |
| `port`, `bind_address`, `access_url` | Listener (restart needed for port/bind) and the public URL used in email links |
| `cron_enabled` | `"Y"` / `"N"`, global cron switch (Disk Monitor has its own scheduler) |
| `log_retention_days`, `audit_retention_days` | Local log and audit retention (defaults 3 and 90) |
| `dev_mode` | Development bypasses; must be `false` outside development |
| `admin_password`, `totp_secret`, `jwt_secret` | Built-in admin credentials and JWT signing key |
| `trusted_proxies` | Proxy IPs/CIDRs whose forwarding headers are trusted |
| `users` | Regular users and their grants/policies (bcrypt hashes) |
| `variables` | Config variables for `{{name}}` substitution |
| `smtp` | Mail settings |
| `scheduled_jobs_overview_email` | Daily overview email (`enabled`, `recipient`, `hour`, `minute`, `period`, `subject`) |
| `s3` | S3 credentials/bucket/prefix for `log_storage: "s3"` |
| `terminals`, `disk_monitor`, `vnc` | Feature settings blocks |
| `hosts` | Map of host ID → SSH/WinRM connection |
| `commands`, `runbooks`, `http_requests`, `vnc_connections` | Saved jobs and remote desktops |

Abbreviated example:

```json
{
    "port": 8080,
    "bind_address": "127.0.0.1",
    "access_url": "https://commandguard.example.com",
    "cron_enabled": "Y",
    "dev_mode": false,
    "trusted_proxies": ["127.0.0.1", "::1"],
    "variables": { "admin_email": "ops@example.com", "default_max_output": "2MB" },
    "smtp": {
        "host": "smtp.example.com", "port": 587, "username": "mailer", "password": "${{SMTP_PASSWORD}}",
        "from_email": "noreply@example.com", "from_name": "CommandGuard",
        "encryption": "tls", "enabled": true, "timeout": 30, "retries": 3, "skip_cert_verify": false
    },
    "hosts": {
        "web-01": { "connection_type": "ssh", "host": "192.0.2.10", "port": 22, "username": "deploy", "pem_file": "./pem_files/deploy.pem" },
        "win-01": { "connection_type": "winrm", "host": "192.0.2.20", "port": 5986, "username": "Administrator", "password": "${{WIN_PASSWORD}}", "winrm_https": true, "winrm_insecure": true },
        "db-01":  { "connection_type": "ssh", "host": "10.0.1.20", "port": 22, "username": "deploy", "pem_file": "./pem_files/deploy.pem", "bastion_host_ref": "web-01" }
    },
    "commands": [{
        "cmd_id": "system-summary", "host_ref": "web-01", "command": "uptime && df -h /",
        "description": "Uptime and disk", "cron_schedule": "*/15 * * * *", "bearer_token": "long-random-token",
        "notify_webhook": "https://hooks.example.com/commandguard", "max_output_size": "{{default_max_output}}",
        "timeout_seconds": 300, "log_storage": "local", "skip_if_running": true,
        "notify_email": { "to": ["{{admin_email}}"], "subject": "{{status}} - {{cmd_id}} {{host}}",
                          "on_success": false, "on_failure": true, "include_output": true, "max_output_lines": 100 }
    }]
}
```

## Configuration Hot-Reload
- `ConfigCache.loadIfChanged` (`main.go`) runs before every command/RunBook/HTTP-request execution and after every save. It first compares the file's mtime and size with those recorded at the last successful load and skips the read entirely when they match (a file modified within the last 2 s is never trusted and is always hashed); otherwise it hashes the file and reloads only when the SHA-256 differs
- All config changes applied immediately except port and bind_address
- Cron jobs automatically updated on config change
- Variable substitutions processed during reload

### Writing config.json from Go code
**ALWAYS** go through `(*ConfigAPI).mutateConfig(func(cfg map[string]interface{}) error)` in `config_api.go`. It holds `configFileMutex` across the read, your callback and the atomic write (`.tmp` + rename, typed `Config` round-trip, `SetEscapeHTML(false)`), then reloads the cache. Reading the file in a handler and calling `saveConfig` afterwards is **not** safe: two concurrent saves silently lose one edit (`config_concurrency_test.go` proves this).
- Return `httpErr(status, message)` from the callback to abort without writing and answer with that status; `sendConfigMutationError(w, err, fallback)` maps it (anything else becomes a 500 with `fallback`)
- The auth manager's `updateConfigSecrets` (admin credentials, JWT secret, users) and the user schedule endpoint use the same path
- Disk Monitor handlers build a candidate block from a pre-loaded context; `saveDiskMonitor` saves it through `mutateConfig` and answers 409 if the `disk_monitor` block changed on disk in the meantime
- `saveConfig(map)` alone is only for callers that already own a complete document (tests, restore)

## Variable Substitution

The configuration supports two types of variable substitution that happen at config load time, while preserving bash variables for runtime:

### Config Variables ({{var_name}})
- Define variables in the `variables` section of config.json
- Reference them anywhere using `{{variable_name}}` syntax
- Supports strings, numbers, booleans, and arrays
- Substituted when config is loaded/reloaded
- Example: `"to": ["{{admin_email}}"]`

### Environment Variables (${{VAR_NAME}})
- Reference system environment variables using `${{VAR_NAME}}` syntax
- Note the double curly braces: `${{}}` not `${}`
- Useful for sensitive data like passwords and API keys
- Substituted when config is loaded/reloaded
- Example: `"password": "${{SMTP_PASSWORD}}"`

### Bash Variables (${var} and $var)
- Standard bash variable syntax `${var}` and `$var` are left unchanged
- Handled by bash at command runtime, not at config load time
- Allows complex bash scripts with arrays, conditionals, loops
- Examples: `${!folders[@]}`, `${arr[$i]}`, `${VAR:-default}`, `$folder`

### Processing Order
1. Environment variables are substituted first (${{VAR}})
2. Config variables are substituted second ({{var}})
3. Validation ensures no undefined config-time variables remain
4. Bash variables (${var}) are passed through untouched
5. All substitution happens during config load/reload

### Example Usage
```json
{
    "variables": {
        "admin_email": "admin@company.com",
        "default_timeout": 30,
        "backup_bucket": "my-s3-bucket"
    },
    "commands": [{
        "max_output_size": "{{default_max_output}}",
        "command": "for file in *.txt; do aws s3 cp $file s3://{{backup_bucket}}/; done",
        "notify_email": {
            "to": ["{{admin_email}}"]
        }
    }],
    "smtp": {
        "password": "${{SMTP_PASSWORD}}"
    }
}
```

**After substitution**, the command becomes:
```bash
for file in *.txt; do aws s3 cp $file s3://my-s3-bucket/; done
```
Note that `{{backup_bucket}}` was replaced at config load, but `$file` remains for bash to handle.

### Variable Syntax Summary

The system uses distinct syntax for each variable type to prevent conflicts:

| Variable Type | Syntax | Processed When | Example |
|--------------|--------|----------------|---------|
| Config variables | `{{variable_name}}` | Config load/reload | `{{backup_bucket}}` |
| Environment variables | `${{ENV_VAR}}` | Config load/reload | `${{SMTP_PASSWORD}}` |
| Bash variables | `${var}` or `$var` | Command runtime | `${filename}`, `$i`, `${arr[@]}` |

**Why these syntaxes?**
- `${{VAR}}` for environment variables avoids conflicts with bash `${var}` syntax
- This allows complex bash scripts with arrays, loops, and conditionals to work seamlessly
- Clear separation between config-time substitution and runtime bash expansion

## API overview

All `/api/*` endpoints except sign-in and the bearer-token job endpoints need `Authorization: Bearer <JWT>`. `AdminMiddleware` endpoints need the admin role; user endpoints check the caller's grants.

### Authentication
- `POST /api/auth/login`: single step. Body `{"username": "", "password": "...", "totp_code": "123456"}` (blank username = built-in `admin`). Returns a 24-hour JWT as `token`. Every failure (unknown user, wrong password or TOTP, access policy) gives the same 401 `"Invalid login credentials or OTP"`. Accounts without an authenticator yet (first admin setup, new users, forced password change) get a `temp_token` + QR code after the password is verified.
- `POST /api/auth/verify-2fa`: completes authenticator **enrollment** only (`temp_token` + `totp_code`). The temp token is valid for 5 minutes, only from the IP that obtained it, and is discarded once a lockout triggers.
- `POST /api/auth/complete-onboarding` (alias `/api/auth/onboarding`): forced password change + enrollment for new users.
- `GET /api/auth/validate`, `POST /api/auth/logout` (stateless; the client drops the token). Password changes, resets, TOTP resets, disabling and deletion bump the user's `token_version`, which invalidates existing JWTs.

### Login Brute-Force Protection
Implemented in `login_limiter.go` (in-memory; resets on restart). Applies to `/api/auth/login`, `/api/auth/verify-2fa` and `/api/auth/complete-onboarding`.
- **IP + username**: 3 consecutive failures lock that pair for 30s, then 2m, 10m, 30m (capped) on repeated lockouts. A successful login or 1h without failures resets it.
- **Per IP (any username)**: 10 failures within 5 minutes lock the IP for 5 minutes.
- Locked requests get HTTP 429 with `Retry-After` and are rejected before credentials are checked; `denied`/`locked` events go to the security audit log.
- Client IP comes from `getClientIP`; behind a reverse proxy, set `trusted_proxies` or every client shares the proxy IP.

### Running jobs
- `GET /api/commands/{cmd_id}`: runs a command and returns output, exit code, duration, `trace_id` and `is_output_truncated`. **Authorization**: a JWT (admin, or a user granted the command) or the command's own `bearer_token`. Calls without a JWT are refused when the command has no `bearer_token` (`main.go`, after `ClaimsFromRequest`). The admin UI must not send the command's bearer token; it would overwrite the JWT in the `Authorization` header.
- `POST /api/runbooks/{runbook_id}/runs`: starts an async RunBook and returns a run ID; `GET /api/runbooks/...` reads progress and history.
- `GET|POST /api/http-requests/{request_id}`: sends a saved HTTP request; JWT (admin or `http_request_ids` grant) or the request's `bearer_token`. HTTP 200 whenever a response was received (failed checks give `exit_code: 1`), 500 when none, 409 when skipped by `skip_if_running`.

### Logs and status
- `GET /api/logs` (list), `GET /api/logs/{trace_id}` (raw text), `/api/logs/{trace_id}/content` (JSON, optional `?bytes=N&before=X` window, see `log_chunk.go`), `/api/logs/{trace_id}/download`. All need a JWT; `canAccessLogFile` (`log_access.go`) limits users to logs of jobs they are granted, and disk-check logs to the admin. Deleting logs (`/api/logs/delete/{file}`, `/api/logs/clear-old`) is admin only.
- `GET /api/status` (admin): uptime, request counts, active requests, locks, cron jobs, scheduled HTTP requests, config hash.
- `GET /api/overview/scheduled-jobs`: day view for the Overview page.
- `GET /api/security/audit?page=&page_size=&filter=` (admin).

### Configuration (admin)
| Endpoint | Purpose |
|---|---|
| `GET/PUT /api/config` | Whole config (secrets redacted on read; `vnc_connections` preserved from disk on write) |
| `/api/config/hosts[/{id}]`, `/api/config/commands[/{id}]` | Hosts and commands CRUD |
| `/api/config/runbooks[/{id}]`, `/api/config/http-requests[/{id}]` | RunBooks and HTTP requests CRUD |
| `/api/config/disk-monitor[...]`, `/api/disk-monitor/...` | Disk Monitor settings/volumes and runtime actions |
| `/api/config/vnc-connections[...]`, `/api/vnc/...` | VNC connections, access mapping, test; runtime tickets and WebSocket |
| `/api/config/smtp`, `/api/config/smtp/test-email` | SMTP settings and test |
| `/api/config/variables` | Global variables |
| `/api/config/s3/test` | S3 connectivity test |
| `/api/config/scheduled-jobs-overview-email` | Daily overview email settings |
| `/api/config/backups/export|inspect|restore` | Encrypted backup and restore |
| `/api/pemfiles[/{name}]` | PEM key files |
| `/api/users[/{username}[/enable|disable|reset-password|reset-totp]]` | User management |
| `/api/terminals[...]` | Terminals (admin and granted users) |

### Regular-user endpoints
`GET /api/user/commands|runbooks|http-requests`, `PUT /api/user/{commands|runbooks|http-requests}/{id}/schedule` (only `cron_schedule`), `POST /api/account/password`.

## Authentication and security

- **Sign-in**: username, password and authenticator code on one form (`/login.html`); errors never reveal which part was wrong. JWTs are HMAC-SHA256, valid 24 hours, stored by the UI in `localStorage` as `auth_token`, and stateless apart from `token_version`.
- **Passwords**: regular users' passwords are bcrypt hashes (12–128 characters). The built-in admin password is stored as given in `admin_password` and compared in constant time.
- **Access policies** (`users[]`): `allowed_ips` (`*` or IPs/CIDRs), `allowed_weekdays`, `allowed_time_start`/`allowed_time_end` (server time, start inclusive, end exclusive, no midnight crossing), `temporary_any_ip_until`. See `docs/multi-user.md`.
- **Client IP** comes from `getClientIP`, which trusts forwarding headers only from `trusted_proxies`.
- **Security audit log**: `writeSecurityAudit` (`audit.go`) writes sign-ins, lockouts, grant and configuration changes, terminal and VNC events under `logs/audit/`; secrets are stripped by `sanitizeAuditDetails`. Retention: `audit_retention_days`.
- **Development mode**: with `"dev_mode": true`, password `abcdefgh` and TOTP `123456` are accepted (see `auth.go`). Default `false`; never enable it outside development.
- **Known limitations** (SSH host keys not verified, plaintext secrets in `config.json`, JWT in `localStorage`, no built-in TLS, single admin, in-memory lockout state) are documented in `SECURITY.md`. Keep that list accurate.

## Logging
- Console shows rotating stats (uptime, web requests, cron requests).
- One file per execution: `logs/YYYY_MM_DD_HHMMSS_cmdid_traceid_log.txt`, with a header (trace ID, timestamps, command, host, remote IP, user agent, exit code, error, `job_type`, RunBook fields) followed by the full output. The log always holds the full output, even when the API response is truncated.
- `log_storage: "s3"` uploads the log to the configured bucket and leaves a small pointer file locally; log reads fetch it back transparently.
- While a job runs its header says `error : job running` / `exitcode : -1`. On startup `markInterruptedLogs` (`log_interrupt.go`) rewrites such logs left by a previous process to exit code 1 with an "Interrupted: …" error.
- Local logs older than `log_retention_days` are deleted by the cleanup routine.

## Notifications

### Webhooks
When `notify_webhook` is set the server POSTs (asynchronously) `output` (respecting `max_output_size`), `error_msg`, `exitcode`, `trace_id`, `request_received_at`, `cmd_id`, `host`, `command`, `remote_ip`, `agent` (`cron-scheduler` for cron runs) and `exec_time`.

### Email
- Global `smtp` block (TLS/SSL, Gmail/Office 365/custom); per-job `notify_email` with `to`, `cc`, `subject` (placeholders `{{status}}`, `{{cmd_id}}`, `{{host}}`, `{{trace_id}}`), `on_success`, `on_failure`, `include_output`, `max_output_lines`.
- Body template: `email.tpl`. Messages are queued in `mail_queue/`; SMTP errors go to `logs/errors_smtp.log`. Emails link to the full log via `access_url` (the reader must sign in).
- Daily **Scheduled Jobs Overview** email (`overview_email.go`): one recipient, send time, period `previous_day` or `past_24_hours`, subject template (default `Scheduled Jobs Overview — {{window_label}}`).

## Output Size Limiting
- Configure per-command using `max_output_size` field
- **Default: 2MB when not specified** (prevents OOM by default)
- Supported units: B, KB/K, MB/M, GB/G
- Examples: "100KB", "2MB", "1.5GB"
- When limit is exceeded, output is truncated with "[OUTPUT TRUNCATED - EXCEEDED MAX SIZE]"
- Log files always receive full output (only API response is limited)
- Prevents OOM for commands with large outputs

## Command Time Limit
- Configure per-command using `timeout_seconds` ("Timeout (seconds)" in the Add/Edit Command modal); empty or 0 = no limit
- Applies to every way a Command runs: cron, API/"Run" button, and RunBook steps (`runCommandWithTimeout` in `main.go` wraps `executeRemoteCommandContext`)
- When it fires the app closes the SSH/WinRM session and records exit code 1 with the error `Timed out: the command was stopped after … because it reached its time limit of N seconds …` (`describeTimeout("command", …)` in `runbooks.go`); the remote process may keep running on the host if it ignores the closed session
- Inside a RunBook the Command limit, the step `timeout_seconds` and the RunBook overall `timeout_seconds` all apply; whichever is shortest fires and the message says which one
- `--validate` rejects negative values

## RunBooks
RunBooks are the only multi-command orchestration feature. Commands remain atomic and RunBooks reference them as ordered steps.

- Configure RunBooks on the RunBooks page or in the top-level `runbooks` array.
- Steps support delay-before, retries, retry delay, timeout, and stop/continue-on-failure.
- RunBooks can run manually, by API, or on cron schedules and expose live progress/history.
- Command-level notifications still run for steps; RunBooks may also send a consolidated email/webhook.
- Legacy `reports`, `next_cmd_id`, `next_on_success_only`, and `chain_delay_seconds` are rejected.

See `docs/runbooks.md` for the schema, optional fields, execution behavior, and APIs.

## HTTP Requests
Saved API calls stored in the top-level `http_requests` array (`http_requests.go`). Each entry has `request_id` (shares the ID namespace with `cmd_id`), `method`, `url`, ordered `query_params` / `headers` (`{key, value, enabled}`), `auth` (none/basic/bearer/api_key), `body` (none/json/text/xml/form), `timeout_seconds`, `follow_redirects`, `max_redirects`, `skip_tls_verify`, `retry_count` + `retry_delay_seconds` (retries only on transport errors and 5xx), `max_output_size`, `expected_status` (`2xx`, `200,201`, `200-299`), `assertions` (`body_contains`, `json_path_equals`, `max_response_ms`), plus the same `cron_schedule`, `bearer_token`, `notify_webhook`, `notify_email`, `log_storage` and `skip_if_running` as Commands.

- One shared runner (`runHTTPRequestJob`) serves the API handler, cron (`executeScheduledHTTPRequest`, cron key `http:<id>`) and RunBook steps (`request_id` instead of `cmd_id`).
- Logs reuse `createCommandLogFile`, so naming, listing, download, S3 pointer files and retention are unchanged. The header keeps `cmd_id` = request ID and adds `job_type : http_request`, `request_id`, `method`, `url`, `http_status`, `response_ms`, `response_bytes`, `attempts`; after `output:` come the masked request dump, response headers, full body and the check results.
- Users may be granted `http_request_ids` (run, reschedule via `/api/user/http-requests/{id}/schedule`, read logs). `/api/status` lists `scheduled_http_requests`; the Overview uses job type `http_request`.
- Frontend: `ui/js/components/httprequests.js` (admin page), `UserHttpRequests` in `userops.js`, step selector in `runbooks.js`, Type filter in `logs.js`.

See `docs/http-requests.md` for the full field reference, log format and API.

## Disk Monitor
Free-space monitoring over SSH, stored in the top-level `disk_monitor` block (`disk_monitor.go` config/API, `disk_monitor_check.go` df runner, `disk_monitor_state.go` runtime state, `disk_monitor_scheduler.go` tick loop, `disk_monitor_alerts.go` alert engine, `disk_monitor_email.go` text/HTML bodies).

- Volumes reference the shared `hosts` map by `host_ref` (SSH only). Settings: `enabled`, `send_alert_once_every` (default 4h), `daily_summary_enabled`/`daily_summary_time` (08:00 local), `default_check_interval` (1h), default threshold (5 GB), `max_concurrent_host_checks` (10), `email_bar_mode`, `email_subject` (template, default `[Disk Monitor] {{summary}}`; placeholders `{{summary}}`, `{{type}}`, `{{tripped}}`, `{{failed}}`, `{{ok}}`, `{{volumes}}`, `{{hosts}}`, `{{app_host}}`, also whitelisted in `substituteVariables`), `recipients`. Durations are strings (`30m`, `1h`, `2d`).
- Scheduler ticks every 30 s independently of `cron_enabled`; due volumes are grouped into one `df -PB1 '<mount>'…` per host (`sudo -n` optional). Each batch writes a normal log file (`cmd_id : diskcheck-<host_ref>`, `job_type : disk_check`, `remote_ip/agent : disk-monitor`, extra header keys `host_ref`, `mounts`, `use_sudo`, `disk_results`) covered by `log_retention_days`; disk logs are admin-only (`canAccessLogFile`) and ignored by the Overview.
- Runtime state (status OK/TRIPPED/UNKNOWN, last reading, 24-reading history, cooldowns, daily-summary marker) is persisted to `logs/disk_monitor/state.json` (debounced, flushed in `performCleanup`), never to config.json.
- Trip rule `free < threshold` (MB/GB absolute or % of total). Alert engine (60 s + wake after each batch) sends one consolidated text+HTML email via `sendSMTPMessage` when an enabled volume is TRIPPED/UNKNOWN past its cooldown; disabled volumes never alert. SMTP is not required for the config to load; the page shows a banner instead.
- Frontend: `ui/js/components/diskmonitor.js` (admin page with dashboard, Detect volumes, Test host, settings card), `API.diskMonitor` in `api.js`, Type filter "Disk check" in `logs.js`, host in-use counts in `hosts.js`, summary row in `status.js`.

See `docs/disk-monitor.md` for the field reference, log format, API and alert rules.

## Persistent Terminals
Interactive SSH terminals in the browser that keep running after logout and after `cguard` restarts.

- Sessions live in **`cg_sessiond`** (source in `./sessiond`, built into `instance/` by `compile_and_run.sh`), which `cguard` auto-starts on `run/sessiond.sock`. It is a separate process in its own session, so `pkill -x cguard` in `compile_and_run.sh` (exact process-name match) leaves open sessions alive. Restart it only to pick up a new daemon build, by sending SIGTERM to this installation's `cg_sessiond` PID; this ends all sessions. Never use `pkill -f cg_sessiond`: it also matches daemons of other installations on the same machine.
- `terminals.go` holds the REST + WebSocket bridge (`/api/terminals…`); the frame protocol constants there must match `sessiond/main.go`.
- SSH hosts only. admin: all hosts; regular users: hosts in their `terminal_hosts` grant (Users & Security). Users see only their own sessions.
- Session names live in the daemon. Clicking a tab's bright dot renames it (`PUT /api/terminals/{id}` → daemon op `rename`, added in `cg_sessiond` 1.1.0; an older running daemon answers "unknown op", returned as HTTP 501 asking for a daemon restart).
- Optional `terminals` config block: `enabled`, `max_sessions_per_user` (1 to 200, default 10, also set in App Settings > General Settings), `scrollback_bytes`, `idle_kill_hours`, `images_enabled`, `image_retention_days`.
- Waiting alert: while any tab's green dot blinks (`.terminal-session.attached.waiting`), `updateTitleFlash()` in `terminals.js` flashes the window title. It ticks from the same-origin `ui/js/title-flash-worker.js` (hidden pages' own timers are slowed to about once a minute; the CSP blocks `blob:` workers), falling back to a page timer.
- Image paste: pasting or dropping an image on a session (`bindImageInput` in `terminals.js`) calls `POST /api/terminals/{id}/images` (`terminals_images.go`), which streams it over a new `SSHExecutor` connection (bastion included; exec of `mktemp`/`cat`/`mv`, not SFTP) to a 0600 `/tmp/cg-paste-*` on the host; the browser then pastes the path without Enter. Ctrl+V deliberately still sends `^V`. `cg_sessiond` is not involved. Other dropped files (any type, up to 100 MB, not folders) go to `POST /api/terminals/{id}/files?name=…` by the same path and land at `/tmp/cg-paste-<random>-<name>`; `terminalSafeFileName` reduces the name to characters that need no quoting, and pasting stays image-only. `terminals.images_enabled` turns off both; `terminals.image_retention_days` (1 to 365, default 7, also in App Settings > General Settings) sets how long they stay.
- Font size: `Aa` popover in the Sessions header or Ctrl+Shift+= / - / 0; 8–32 px, default 14, one size for all sessions, saved per browser in `localStorage` (`terminalFontSize`). `fitActive()` applies it, so hidden tabs change when shown (fonts cannot be measured in a hidden pane).
- Each user's tab order (drag or Ctrl+Shift+←/→) is stored by `cguard`, not the daemon, in `run/terminal_order.json` (`PUT /api/terminals/order`, returned as `order` by `GET /api/terminals`); only ids of sessions the user can see are kept.
- Frontend: `ui/js/components/terminals.js` with vendored `ui/js/xterm*.js` + `ui/css/xterm.css`.
- **Agent Sessions** (`terminals_agent_sessions.go`): saved launch presets (host + folder + optional coding agent + app name + icon). The robot button in the Sessions header or Ctrl+Shift+L opens a searchable palette; launching creates a normal session named `Name`, `Name_02`, … (lowest free number among running sessions) and the browser types `cd '<folder>' && clear && <agent> <args>` (shell-only presets: `cd '<folder>' && clear`) once `replay_end` has enabled input (`pendingInput` in `terminals.js`). Shared presets live in `terminals.agent_sessions` in config.json (admin only, written via `mutateConfig`, validated by `validateAgentSessionsConfig` on load/`--validate`, host delete refused with 409 while referenced); personal presets per user in `run/terminal_agent_sessions.json`. The icon of a running session comes from `run/terminal_meta.json` (session id → meta, written by `POST /api/terminals` when the body has `meta`, returned as `meta` on each session, pruned when the daemon no longer lists the id). The daemon is unchanged. API: `GET/POST /api/terminals/agent-sessions`, `PUT/DELETE /api/terminals/agent-sessions/{id}` (`scope` = `shared`|`personal`). Audit: `terminal_agent_session_create|update|delete`.

See `docs/terminals.md` for behaviour, configuration and API details.

## Remote Desktops (VNC)
Top-level `vnc` settings block and `vnc_connections` array; user grants in `users[].vnc_access` (`[{vnc_id, level: control|view}]`).

- `vnc.go` config/validation/admin API (`/api/config/vnc-connections`, `.../{id}/access`, `.../{id}/test`), `vnc_rfb.go` RFB handshake + auth (None, VNC DES, Apple ARD, Tight, VeNCrypt Plain/X509*) and the viewer message filter, `vnc_sessions.go` runtime `/api/vnc` (one-time IP-bound tickets, WebSocket bridge, registry, 15 s enforcement loop, idle disconnect, thumbnails, audit).
- Passwords never reach the browser: the proxy authenticates, then offers noVNC security type None. View-only and clipboard-off are enforced server-side by dropping viewer messages; the ExtendedMouseButtons pseudo-encoding is always stripped so pointer messages keep a fixed size.
- `PUT /api/config` preserves `vnc_connections` from disk (manage them only through their API); `GET /api/config` redacts their passwords. Hosts used as `tunnel_host_ref` cannot be deleted (409) or changed to WinRM (400); `hosts.js` counts them (`getHostVNCCount`).
- No daemon: the remote desktop persists on the VNC server; closing the tab or restarting `cguard` only drops the viewer, which reconnects by itself.
- Frontend: `ui/js/components/remotedesktops.js` (page + admin Manage view), `ui/js/vnc-viewer.js` (ES module, imported on demand), `ui/vnc.html` + `ui/js/vnc-popout.js` (pop-out), noVNC 1.7.0 vendored in `ui/js/novnc-1.7.0/` (versioned by folder name; MPL-2.0, `ui/licenses/novnc-LICENSE.txt`). Grants also editable in Users & Security (Remote desktops tab with level selector); settings in App Settings.

See `docs/vnc.md` for fields, auth types per server, security model and API.

## Host configuration

### Bastion (jump) hosts
An SSH host may set `bastion_host_ref` to the ID of another SSH Host. The app logs into the bastion with the bastion's own credentials, opens a TCP forward on that connection to the target's `host:port`, and runs the target's SSH handshake over it (pure Go, no OpenSSH/ProxyCommand). This applies everywhere a Host is dialed: Commands/RunBooks (`SSHExecutor.Connect`), Disk Monitor (`runDiskDf`) and persistent terminals (`cg_sessiond` ≥ 1.2.0; an older running daemon answers 501 until restarted).
- **Single hop only**: the bastion must not have a `bastion_host_ref` itself. Both hosts must be SSH; the bastion needs a password or PEM key. The bastion stays usable as a normal host.
- Validation lives in `checkBastionRef`/`resolveBastionHosts` (`main.go`): run by `loadIfChanged` (which also attaches the resolved copy as `Host.Bastion`, `json:"-"`, never persisted), `--validate` (`bastionRefProblems`), restore (`validateRestoredConfig`) and the host create/update/delete APIs (`validateHostsBastions`, HTTP 400). A Host whose `BastionHostRef` is set but `Bastion` is nil never dials directly: `Connect`, `runDiskDf` and the terminal create handler fail loudly instead.
- Deleting a host that other hosts use as bastion is refused (HTTP 409, and the UI locks the ID / disables Delete: `getHostBastionDependents` in `hosts.js`). `PUT /api/config/hosts/{id}` keeps the stored `bastion_host_ref` when the key is absent; send `""` to go back to a direct connection.
- The bastion must allow TCP forwarding (`AllowTcpForwarding yes` / `PermitOpen`); refusals and unreachable targets are reported with the bastion's name in the error.

### Host protection in the UI
A host that is in use cannot be renamed or deleted from the Hosts page. `hosts.js` counts its users with `getHostCommandCount`, `getHostRunBookCount`, `getHostDiskVolumeCount`, `getHostAgentSessionCount`, `getHostVNCCount` and `getHostBastionDependents`.
- **In use**: the Host ID field is read-only with a warning such as "Host ID is locked - referenced by N command(s), N RunBook(s), …", and the Delete button is disabled with a tooltip. `deleteHost()` re-checks before deleting.
- **Not in use**: the ID can be changed (create new + delete old) and Delete asks for confirmation.
- The backend enforces the same rules: deleting a host used by Disk Monitor volumes, Agent Sessions, VNC tunnels or as a bastion returns HTTP 409.

## Password Field Handling in Host Configuration

### Important: Preserving Passwords During Host Updates
When editing host configurations through the API or web UI, special care must be taken to preserve sensitive fields like passwords:

1. **Backend Behavior**: The `HandleUpdateHost` function in `config_api.go` now preserves existing `password` and `pem_password` fields when they are not included in the update request. This prevents accidental deletion of passwords when updating other host properties.

2. **Frontend Behavior**: The hosts.js component should NOT send the password field when editing a host unless a new password is explicitly entered. An empty password field in the edit form means "keep existing password", not "remove password".

3. **Security Consideration**: Passwords are never sent back to the frontend when fetching host configurations. The edit form shows a placeholder "••••••• (unchanged)" to indicate an existing password is present.

4. **Implementation Details**:
   - When creating a new host: Password field is sent if provided
   - When editing an existing host: Password field is only sent if a new password is entered
   - Backend preserves existing password if not included in update request
   - This pattern prevents passwords from being wiped during routine host edits

## Invariants to keep when changing the code
- **Everything the server writes is owner-only**: files `privateFileMode` (0600), folders `privateDirMode` (0700), both defined in `main.go`. That covers `config.json` (every save and restore), PEM keys, mail queue items (they embed the SMTP password), execution logs, the audit trail, RunBook history, Disk Monitor state and the `run/` files. Never write runtime data with 0644/0755. `hardenFilePermissions` runs at startup and tightens `config.json`, `logs/`, `pem_files/`, `mail_queue/` and `run/` left open by older versions. The only exception is `email.tpl` (a template, 0644).
- **A save never leaves an unloadable config on disk.** `saveConfigLocked` reloads after writing; if the loader rejects the new file and the previous one was valid, the previous file is restored and the caller gets HTTP 400 with the loader's reason (for example a command whose `host_ref` does not exist, or a host whose PEM file is missing).
- **Enrollment sessions are bound to the account state.** `tempAuthSession.TokenVersion` must match the user's current `token_version` when enrollment or onboarding completes, so a password/TOTP reset, disable or delete invalidates a pending session. Admin enrollment only completes while no authenticator is enrolled.
- **Imported backups are untrusted until decrypted.** `decryptBackup` caps the scrypt parameters and checks salt and nonce sizes before deriving a key.
- **Secrets never appear in API responses**: host create/update answers go through `hostResponse`, VNC connections through `redactVNCConnection`, HTTP requests through `redactHTTPRequest`.
- **SSH connects use `dialSSH`** (in `main.go` and again in `sessiond/main.go`), which puts a deadline on the handshake as well as the TCP connect.
- **Shared buffers and run snapshots are copied or locked**: `LimitedBuffer` serialises stdout/stderr writes; `cloneRunBookRun` gives API responses their own copy of a run.
- **Toast messages are plain text.** `Utils.showSuccess` / `Utils.showError` escape their argument because Notyf renders HTML; do not pre-escape and do not pass markup.

## Important JSON Handling Guidelines

### Preventing HTML Escaping in JSON
When writing or modifying any JSON marshalling code in Go, **ALWAYS** disable HTML escaping to prevent characters like `<`, `>`, and `&` from being escaped as Unicode sequences (`\u003c`, `\u003e`, `\u0026`).

#### Correct approach:
```go
// Use json.Encoder with SetEscapeHTML(false)
var buf bytes.Buffer
encoder := json.NewEncoder(&buf)
encoder.SetEscapeHTML(false)  // CRITICAL: Disable HTML escaping
encoder.SetIndent("", "\t")    // Optional: for pretty printing
encoder.Encode(data)
```

#### Incorrect approach (DO NOT USE):
```go
// This will escape HTML characters
jsonData, _ := json.Marshal(data)
jsonData, _ := json.MarshalIndent(data, "", "\t")
```

This is crucial for maintaining clean config.json files that:
- Are human-readable
- Don't create unnecessary git diffs  
- Preserve special characters in fields like email addresses (e.g., `"SSH Executor <user@example.com>"`)

## Frontend conventions

- Plain JavaScript with jQuery and Bootstrap 5; each page is an object in `ui/js/components/` registered in `ui/js/router.js` (admin routes, plus a reduced set for regular users in `configureForRole`). API calls go through `ui/js/api.js`; shared helpers in `ui/js/utils.js`.
- Searchable dropdowns: `Utils.searchableSelect()` / `syncSearchableSelect()` / `setSelectValue()` / `destroySearchableSelect()` (Tom Select).
- Escape any user- or config-supplied text before inserting it as HTML.
- No CDN: all libraries are vendored under `ui/` (the CSP blocks other origins and `blob:` workers).

### Cache busting
Every CSS/JS include carries a version query (`?v=N`). When you change a file, bump its number where it is loaded (`ui/index.html`, `ui/login.html`, `ui/vnc.html`, or the importing JS file noted below) and update this table.

Current versions:
- `/css/app.css`: v=57 in `index.html` and `vnc.html`; `login.html` still loads v=13 (bump it there too when login styling matters)
- `/js/utils.js` 6, `/js/api.js` 26, `/js/auth.js` 2, `/js/router.js` 14, `/js/app.js` 5
- `/js/components/`: overview 6, dashboard 7, status 11, hosts 18, commands 32, runbooks 14, httprequests 1, logs 8, pemfiles 7, backuprestore 3, variables 1, smtp 6, appsettings 7, userops 3, account 1, userssecurity 5, terminals 28, diskmonitor 2, remotedesktops 4
- `/js/title-flash-worker.js` v=1: loaded by `new Worker()` in `terminals.js`; bump it there
- `/js/vnc-viewer.js` v=3: imported by `remotedesktops.js` and `vnc-popout.js`; bump it there. `/js/vnc-popout.js` v=3 in `vnc.html`
- noVNC is versioned by its folder `novnc-1.7.0/` (its internal relative imports carry no query string)
- Package-versioned vendor files: xterm.js `6.0.0` (addons fit `0.11.0`, web-links `0.12.0`, webgl `0.19.0`, `xterm.css?v=6.0.0`), Tom Select `2.4.3`, Tabulator `6.5.0`, ECharts `6.0.0`, dhtmlxGantt `10.0.0`
- Other vendor files (Bootstrap, jQuery, DataTables, Prism, Notyf, SweetAlert2, Day.js, QRCode): v=1

## Testing

```bash
go vet ./...
go test ./...                 # whole suite
go test -run TestName -v .    # one test
go test -race ./...           # race detector
./run_tests.sh [--coverage|--all|--integration|--verbose|--benchmark]
```

- Most tests are self-contained: they start the app in-process with `NewTestServer(t)` (`test_utils.go`) on a temporary directory and use `DoRequest`, `DoAuthRequest`, `DoCommandRequest`, `UpdateConfig`, `GetConfig`, `AssertStatus`, `AssertJSONResponse`, `AssertSuccess`/`AssertError` and `WaitForCondition`. Feature tests use fakes (for example a fake RFB server in `vnc_test.go`, a fake daemon in `terminals_test.go`, local HTTP servers in `http_requests_test.go`).
- Tests that execute real remote commands read SSH/WinRM test servers from a git-ignored `test_env.json` (template: `test_env.example.json`, setup: `docs/test-setup.md`, or `./setup_test_env.sh`). Never point them at production machines.
- Without `test_env.json` those tests skip (`DoCommandRequest` in `test_utils.go`), each after a short wait for the unreachable placeholder host, so `go test ./...` passes on a fresh clone and takes a few minutes.
- New tests: use subtests (`t.Run`), `defer ts.Close()` and `defer resp.Body.Close()`, cover failure cases, and keep tests independent of each other and of the network.

## Dependencies
- `golang.org/x/crypto` (SSH, bcrypt, scrypt), `github.com/robfig/cron/v3`, `github.com/golang-jwt/jwt/v5`, `github.com/pquerna/otp`, `github.com/masterzen/winrm`, `github.com/gorilla/websocket`, `github.com/aws/aws-sdk-go-v2` (S3), `gopkg.in/gomail.v2`.
- `go.mod` requires Go 1.26 (needed by the current `golang.org/x/crypto`); the Dockerfile builds with Go 1.27. Keep `x/crypto` current: its SSH client fixes matter here. Licences are listed in `THIRD_PARTY_NOTICES.md`.

## Operational notes
- `port` and `bind_address` changes need a restart; everything else hot-reloads.
- Execution locks are cleaned up after 5 minutes of inactivity; `skip_if_running` skips instead of waiting.
- Times in logs use the server's local timezone.
- The UI redirects to the login page when the JWT expires.
- Restarting `cg_sessiond` ends every open terminal session; only do it to load a new daemon build, and stop it by PID, not with `pkill -f`.

## Before you open a pull request
1. `gofmt -l .` prints nothing, `go vet ./...` passes, and the tests you touched pass.
2. `./instance/cguard --validate --config config.json.sample` passes.
3. CSS/JS changes bump `?v=` and the table above.
4. Docs in `docs/`, `README.md`, `SECURITY.md` and this file match the new behaviour.
5. `git status` shows no secrets, real hostnames, logs or local config.
