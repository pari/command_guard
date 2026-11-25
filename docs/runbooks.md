# RunBooks

For role-based access, Command and RunBook grants are independent. A user granted a RunBook may execute it, edit its shared schedule, view its consolidated history, and read its generated step logs. That grant does not allow standalone execution of the referenced Commands. See [multi-user.md](multi-user.md).

RunBooks execute saved Commands and saved HTTP requests in a defined order. Commands and HTTP requests remain atomic: their host/URL, command text or request definition, output limit, log storage, lock behavior, and notifications are reused by every RunBook step.

## Configuration

```json
{
  "runbooks": [{
    "runbook_id": "backup-and-verify",
    "description": "Create, copy, and verify a backup",
    "steps": [
      { "step_id": "backup", "cmd_id": "backup-db" },
      {
        "step_id": "copy",
        "cmd_id": "copy-backup",
        "delay_before_seconds": 5,
        "retry_count": 1,
        "retry_delay_seconds": 10
      },
      {
        "step_id": "verify",
        "cmd_id": "verify-backup",
        "on_failure": "continue",
        "timeout_seconds": 300
      },
      {
        "step_id": "notify-api",
        "request_id": "backup-webhook",
        "retry_count": 2,
        "retry_delay_seconds": 10
      }
    ],
    "cron_schedule": "0 2 * * *",
    "timeout_seconds": 1800,
    "summary_format": "html"
  }]
}
```

Required RunBook fields are `runbook_id` and a non-empty `steps` array. Every step requires exactly one of an existing `cmd_id` (Command) or an existing `request_id` (HTTP request, see [http-requests.md](http-requests.md)). The configuration API accepts `step_id` as optional on create and update; when omitted, the server generates and persists a compact unique internal ID. Existing or explicitly supplied step IDs are preserved. Directly authored `config.json` RunBook definitions should continue to include a unique `step_id` for each step.

Optional RunBook fields are `description`, `cron_schedule`, `timeout_seconds`, `summary_format`, `bearer_token`, `notify_webhook`, and `notify_email`. Supported summary formats are `html`, `text`, `markdown`, and `json`; the default is HTML.

Optional step fields are `label`, `on_failure` (`stop` or `continue`), `delay_before_seconds`, `retry_count`, `retry_delay_seconds`, and `timeout_seconds`. Defaults are stop on failure, no delay, no retries, and no step-specific timeout (`0` means no step-specific limit). A step that runs past `timeout_seconds` is stopped and reported as "Timed out: this step was stopped after … because it reached its time limit of N seconds"; a step stopped by the RunBook's overall `timeout_seconds` says so instead. The same wording is written to the step's log file and sent in notifications. A Command that has its own `timeout_seconds` (Edit Command dialog) keeps that limit inside a RunBook too: the shortest of the Command, step and overall limits fires first, and the message names which one ("the command was stopped … Raise \"Timeout (seconds)\" in the Edit Command dialog").

Only one run of a particular RunBook may be active. A manual/API overlap returns HTTP 409; a scheduled overlap is stored as a skipped run. RunBook definitions and referenced Commands are snapshotted when a run starts.

## API

Configuration endpoints require an admin JWT:

- `GET /api/config/runbooks`
- `POST /api/config/runbooks`
- `PUT /api/config/runbooks/{runbook_id}`
- `DELETE /api/config/runbooks/{runbook_id}`

Execution and individual-run endpoints accept an admin JWT or the RunBook bearer token. History listing requires an admin JWT:

- `POST /api/runbooks/{runbook_id}/runs`
- `GET /api/runbooks/{runbook_id}/runs`
- `GET /api/runbooks/{runbook_id}/runs/{run_id}`
- `POST /api/runbooks/{runbook_id}/runs/{run_id}/stop`
- `GET /api/runbooks/{runbook_id}/runs/{run_id}/download?format=html`

Starting a RunBook returns HTTP 202 and a `run_id`. Poll the run endpoint until it reaches `succeeded`, `failed`, `timed_out`, `stopped`, `skipped`, or `interrupted`.

Run history is stored under `logs/runbooks`. Each Command or HTTP request attempt also creates its normal full log with `runbook_id`, `run_id`, `step_id`, `step_index`, and `attempt` metadata.

In the run view each finished step has one **Log** panel, which replaced the separate "Output" and "View log" items, plus a **Download** button. The panel reads the step's log a window at a time through `GET /api/logs/{trace_id}/content?bytes=N&before=X`:
- `bytes` is the window size, 1 KB–1 MB.
- `before` is the end byte offset; leave it out to read the tail.
- The response adds `offset`, `end` and `total_size`.
- Windows start on a line boundary.
- Local logs are read with ranged reads, so a large file is never loaded whole.
- Without these parameters the endpoint returns the whole log as before.

## Clean break

The old top-level `reports` array and Command fields `next_cmd_id`, `next_on_success_only`, and `chain_delay_seconds` are rejected by configuration validation. Define all multi-command execution in `runbooks`.
