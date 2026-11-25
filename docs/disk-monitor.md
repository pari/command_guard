# Disk Monitor

Disk Monitor watches free space on the SSH hosts you already have on the Hosts page. It runs `df` over SSH on a per-volume interval, keeps the last readings for a trend line, emails one consolidated alert when a volume drops below its threshold, and sends a daily summary. It replaces the former stand-alone `sshDiskMon` service.

## How it works

1. Every volume is a mount path on a host (`host_ref` is a key of the shared `hosts` map; SSH hosts only, WinRM hosts are rejected). Hosts that connect through a bastion (`bastion_host_ref`) are checked through the bastion; each hop gets the 10 s dial timeout inside the 30 s total per batch.
2. A scheduler ticks every 30 seconds. Volumes whose next check is due (or that were never checked, or that you clicked "Check now" on) are grouped per host and checked in **one SSH session per host**: `df -PB1 '/mount1' '/mount2' …` (with a `sudo -n` prefix when `use_sudo` is set).
3. Each host batch writes **one log file** in `logs/` with `job_type : disk_check`, exactly like Commands and HTTP Requests. It shows on the Logs page (Type filter "Disk check"), is admin-only, and is deleted by the normal `log_retention_days` retention.
4. A volume **trips** when the `Available` bytes reported by `df` are below the threshold (`MB`, `GB`, or `%` of the filesystem size). A connection error, a timeout or a missing mount marks it **check failed** (`UNKNOWN`). When a tripped or failed volume reports fine again it is marked **recovered** and reported in the next email.
5. The alert engine runs every minute and right after every batch. If at least one enabled volume is tripped or failed **and** its cooldown (`send_alert_once_every`, default 4h) has elapsed, one email is sent listing every current problem plus pending recoveries. Only the volumes that were eligible get their cooldown reset, so a newly tripped volume is reported immediately even while others are cooling down.
6. A daily summary of every enabled volume is sent at `daily_summary_time` (server local time). The "already sent today" marker is persisted, so a restart cannot send it twice; if the app was down for more than 6 hours past the time, that day's summary is skipped.
7. Runtime state (status, last reading, 24 readings of history, cooldowns, daily marker) lives in `logs/disk_monitor/state.json`, written a couple of seconds after each change and flushed on shutdown. It is **not** part of config.json, so checks never rewrite the config file. Backups do not include it; it rebuilds itself on the next check.

Timeouts per host check: 10 s to connect, 15 s for `df`, 30 s overall. Hosts are never checked concurrently with themselves; `max_concurrent_host_checks` caps parallel hosts (a batch over the cap simply stays due and is retried on the next tick).

Only GNU `df` output (`-PB1`) is supported, i.e. Linux targets.

## Configuration

Top-level `disk_monitor` block in `config.json` (edit from the Disk Monitor page; all keys optional except `volumes[].host_ref` and `volumes[].mount_path`):

```json
"disk_monitor": {
	"enabled": true,
	"send_alert_once_every": "4h",
	"daily_summary_enabled": true,
	"daily_summary_time": "08:00",
	"default_check_interval": "1h",
	"default_threshold_value": 5,
	"default_threshold_unit": "GB",
	"max_concurrent_host_checks": 10,
	"email_bar_mode": "free",
	"email_subject": "[Disk Monitor] {{summary}}",
	"recipients": { "to": ["ops@example.com"], "cc": [] },
	"volumes": [
		{
			"volume_id": "dv-20261020-141501",
			"host_ref": "bastion",
			"mount_path": "/",
			"description": "root filesystem",
			"check_interval": "1h",
			"threshold_value": 5,
			"threshold_unit": "GB",
			"use_sudo": false,
			"enabled": true
		}
	]
}
```

| Field | Meaning | Rules / default |
|---|---|---|
| `enabled` | Master switch for checks, alerts and summary | default `true` |
| `send_alert_once_every` | Per-volume alert cooldown | duration `1m`–`30d`, default `4h` |
| `daily_summary_enabled` / `daily_summary_time` | Daily summary email | `HH:MM` 24-hour local time, default `08:00` |
| `default_check_interval` | Interval for volumes without their own | duration `1m`–`7d`, default `1h` |
| `default_threshold_value` / `default_threshold_unit` | Prefill for new volumes | `MB`/`GB` > 0, `%` in (0,100); default `5 GB` |
| `max_concurrent_host_checks` | Parallel host checks | `1`–`100`, default `10` |
| `email_bar_mode` | Progress bar in emails shows `free` or `used` | default `free` |
| `email_subject` | Subject template for alert, daily summary and test emails | one line, max 200 chars, placeholders below plus config variables; default `[Disk Monitor] {{summary}}` (empty = default) |
| `recipients.to` / `recipients.cc` | Alert and summary recipients | valid email addresses; `to` is required for emails to be sent |
| `volumes[].volume_id` | Stable ID | letters, digits, `-`, `_`; generated from host and mount (`dv-<host>-<hash>`) when omitted, so hand-written volumes keep a stable ID |
| `volumes[].host_ref` | Key in `hosts` | must exist and be an SSH host |
| `volumes[].mount_path` | Exact mount point | absolute, trailing `/` trimmed, no line breaks, no `{{`/`}}`/`${{` |
| `volumes[].check_interval` | Per-volume interval | duration `1m`–`7d`; empty = default |
| `volumes[].threshold_value` / `threshold_unit` | Trip line | `MB`, `GB` or `%` |
| `volumes[].use_sudo` | Run `sudo -n df` | default `false` |
| `volumes[].enabled` | Disabled volumes are not checked, never alert and are left out of the summary | default `true` |

Durations are written as `30m`, `1h`, `1h30m` or `2d`. Emails use the global SMTP settings (SMTP page). SMTP is **not** required for the config to load: when SMTP or recipients are missing, the page shows a warning banner, `--validate` prints a warning, and alerts fire as soon as SMTP is configured (cooldowns are not consumed while emails cannot be sent).

Hosts referenced by a volume cannot be deleted (HTTP 409) and their Host ID is locked on the Hosts page, like hosts used by Commands.

## Using the page

Admin-only, under **Monitoring → Disk Monitor**.

- **Dashboard table**: host, mount, free/total, threshold, % free bar, status (OK / TRIPPED / CHECK FAILED / NEVER CHECKED / DISABLED), last check, next check, 24-reading trend line, actions (Check now, View last log, Edit, Delete). A failed check shows its error under the row. The table refreshes every 15 seconds while the tab is visible.
- **Detect volumes**: pick a host, optionally `sudo -n`, Scan. Real disks that are not monitored yet are pre-ticked; `tmpfs`, `devtmpfs`, `udev`, `none`, `overlay` and zero-size filesystems are listed but unticked. Set threshold and unit per row and "Add selected". No log file is written for a scan.
- **Add volume / Edit**: host, mount path, threshold, interval, description, sudo, enabled.
- **Test host**: runs `df` for the host's volumes right now (writes a log with trigger `test`), or a plain `df` when the host has no volumes yet.
- **Settings card**: enabled, cooldown, daily summary, recipients, email subject, defaults, concurrency, email bar mode, **Send test email** (sends the current fleet status to the recipients).

## Log file

`logs/YYYY_MM_DD_HHMMSS_diskcheck-<host_ref>_<trace>_log.txt`

```
               trace_id : 9f1c2a7b3d4e5f60
    request_received_at : 2026-10-20 14:15:01.004
                 cmd_id : diskcheck-bastion
               job_type : disk_check
               host_ref : bastion
                   host : bastion.example.com
                 mounts : /, /data
               use_sudo : no
              remote_ip : disk-monitor
                  agent : disk-monitor
             invoked_by : disk-monitor
                trigger : schedule
           disk_results : 1 ok, 1 tripped, 0 failed
              exec_time : 1.212s
                  error : none
               exitcode : 0
                 output:
--- command ---
df -PB1 '/' '/data'
--- stdout ---
Filesystem      1B-blocks        Used   Available Capacity Mounted on
/dev/root     53660876800 41598001152 12028493824      78% /
/dev/sdb1    107374182400 106971000000   403182400     100% /data
--- stderr ---
(empty)
--- results ---
OK       /                        free 11.2 GB of 50.0 GB (22.4% free)   threshold 5 GB (5.0 GB)
TRIPPED  /data                    free 384.5 MB of 100.0 GB (0.4% free)  threshold 1 GB (1.0 GB)   tripped since 2026-10-20 14:15
```

`trigger` is `schedule`, `manual` or `test`; `invoked_by` is the admin username for manual runs. `exitcode 0` means every requested mount produced a reading (a tripped volume is a monitoring state, not a check failure); `exitcode 1` means a connection error, a timeout or a missing mount, with the first problem in `error`. A batch cut short by a shutdown gets `error : Interrupted by shutdown` and the volumes are re-checked right after restart.

## API (admin JWT)

Configuration (persisted to config.json, written to the security audit log):

| Method | Path | Notes |
|---|---|---|
| GET | `/api/config/disk-monitor` | settings (no volumes) + `smtp_ready` + list of SSH hosts |
| PUT | `/api/config/disk-monitor` | settings; volumes are kept as they are |
| GET / POST | `/api/config/disk-monitor/volumes` | list / create (`volume_id` optional; 409 on duplicate id or host+mount) |
| POST | `/api/config/disk-monitor/volumes/bulk` | `{host_ref, use_sudo, volumes:[{mount_path, threshold_value, threshold_unit, check_interval?}]}` → `{created, skipped}` |
| GET / PUT / DELETE | `/api/config/disk-monitor/volumes/{id}` | one volume; DELETE also drops its runtime state |

Runtime:

| Method | Path | Notes |
|---|---|---|
| GET | `/api/disk-monitor/state` | dashboard payload: banners, summary counts, per-volume config + state + history |
| GET | `/api/disk-monitor/volumes/{id}/history` | last 24 readings |
| POST | `/api/disk-monitor/volumes/{id}/check` | mark due; runs within seconds (202) |
| POST | `/api/disk-monitor/hosts/{host_ref}/check` | mark all enabled volumes on the host due |
| POST | `/api/disk-monitor/hosts/{host_ref}/test` | synchronous batch (`trigger : test`), returns per-mount results; 409 if the host is being checked; 400 for WinRM |
| POST | `/api/disk-monitor/hosts/{host_ref}/detect` | `{use_sudo}` → every mount with `already_monitored` / `precheck` flags; no log file |
| POST | `/api/disk-monitor/test-email` | sends the summary layout to the recipients; 400 when SMTP or recipients are missing |

`/api/status` carries a `disk_monitor` summary (configured, enabled, counts, in-flight hosts, next due, SMTP readiness, last email error).

```bash
JWT=...
curl -s -X POST -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  localhost:8080/api/config/disk-monitor/volumes \
  -d '{"host_ref":"bastion","mount_path":"/","threshold_value":5,"threshold_unit":"GB"}'
curl -s -X POST -H "Authorization: Bearer $JWT" localhost:8080/api/disk-monitor/hosts/bastion/detect -d '{"use_sudo":false}'
curl -s -H "Authorization: Bearer $JWT" localhost:8080/api/disk-monitor/state | jq .summary
```

## Alerts and daily summary

- Subjects come from the `email_subject` template (default `[Disk Monitor] {{summary}}`), so by default they read `[Disk Monitor] CRITICAL: N volumes affected on M host(s)`, `[Disk Monitor] N volume(s) recovered` (recoveries only), `[Disk Monitor] Daily summary: N volumes OK, M alerting`, `[Disk Monitor] Test email`. Placeholders (`renderDiskEmailSubject` in `disk_monitor_email.go`):

  | Placeholder | Value |
  |---|---|
  | `{{summary}}` | the generated text above, without the `[Disk Monitor]` prefix |
  | `{{type}}` | `Alert`, `Recovered`, `Daily summary` or `Test email` |
  | `{{tripped}}` / `{{failed}}` / `{{ok}}` / `{{volumes}}` | counts of below-threshold, check-failed, OK and all enabled volumes |
  | `{{hosts}}` | hosts with problem volumes (alert), recovered volumes (recovery) or all enabled volumes (summary/test) |
  | `{{app_host}}` | hostname of the server running Command Guard |

  Config variables (`{{my_var}}`) are substituted when the config loads. Any other `{{name}}` is rejected on save (HTTP 400). New placeholders must also be added to the template-variable whitelist in `substituteVariables` (`main.go`), or the config load fails. A blank rendered subject falls back to the default.
- Bodies are sent as text plus an HTML alternative (inline-styled cards per host and mount with a free/used bar), sections "Below threshold", "Check failed" and "Recovered".
- Recoveries alone never trigger an email; they ride along with the next alert or the daily summary.
- Email failures are written to `logs/errors_smtp.log` and shown as a red banner on the page; cooldowns are only advanced after a successful send.

## Validation

`./cguard --validate` reports the same rules as the page (ranges, units, SSH-only hosts, duplicate mounts, recipients) under `disk_monitor:` and warns when enabled volumes exist without SMTP or recipients. The same validation runs on hot reload and on backup restore; an invalid block keeps the previous config active.
