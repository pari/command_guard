# HTTP Requests

Command Guard can save **HTTP requests / API calls** (in the spirit of Postman, Bruno or Insomnia) next to its SSH/WinRM Commands. A saved request can be run from the web UI, triggered from the outside with its own bearer token, scheduled with cron, used as a step inside a RunBook, and granted to regular users. Every run writes **one log file** that contains the request that was sent, the response headers and the full response body; that file has the same name scheme and lifecycle as every other execution log (Logs page, download, S3 upload, retention cleanup).

## How it works

```
cron / UI / API / RunBook step ──> runHTTPRequestJob ──> net/http client ──> target URL
                                        │
                                        ├── logs/YYYY_MM_DD_HHMMSS_<request_id>_<trace>_log.txt
                                        ├── webhook / email notification (same as Commands)
                                        └── exit code 0 (all checks passed) or 1
```

- Requests are stored in the top-level `http_requests` array of `config.json` and are hot-reloaded like everything else.
- A run is **successful (exit code 0)** when the response status matches `expected_status` (default `2xx`) and every assertion passes. A transport error (DNS, connect, TLS, timeout), an unexpected status or a failed assertion gives **exit code 1** and triggers the `on_failure` notifications.
- **Retries**: `retry_count` extra attempts are made after a transport error or a `5xx` response, waiting `retry_delay_seconds` in between. A `4xx` response or a failed assertion is never retried. Every attempt is written to the log file.
- `max_output_size` (default 2MB) limits the response body kept in memory, returned by the API and shown in the UI. The log file always receives the full body, exactly like Command output.
- The request ID shares the ID namespace with Command IDs: a `request_id` may not equal any `cmd_id`. Log file names, execution locks, user grants and the Logs page all key off the ID.
- Only one run of a given request executes at a time (same lock as Commands); `skip_if_running` makes overlapping runs skip instead of queue.

## Configuration

```json
"http_requests": [
  {
    "request_id": "healthcheck-api",
    "description": "Ping the production API health endpoint",
    "method": "GET",
    "url": "https://api.example.com/health",
    "query_params": [{ "key": "verbose", "value": "1" }],
    "headers": [{ "key": "Accept", "value": "application/json" }],
    "auth": { "type": "bearer", "token": "${{API_TOKEN}}" },
    "body": { "type": "json", "content": "{\"ping\": true}" },
    "timeout_seconds": 30,
    "follow_redirects": true,
    "max_redirects": 10,
    "skip_tls_verify": false,
    "retry_count": 2,
    "retry_delay_seconds": 5,
    "max_output_size": "2MB",
    "expected_status": "2xx",
    "assertions": [
      { "type": "body_contains", "value": "\"status\":\"ok\"" },
      { "type": "json_path_equals", "path": "data.status", "value": "ok" },
      { "type": "max_response_ms", "value": "1500" }
    ],
    "cron_schedule": "*/5 * * * *",
    "bearer_token": "CHANGE_ME_TO_A_LONG_RANDOM_TOKEN",
    "notify_webhook": "https://hooks.example.com/commandguard",
    "notify_email": { "to": ["ops@example.com"], "subject": "{{status}} - {{request_id}}", "on_success": false, "on_failure": true, "include_output": true, "max_output_lines": 100 },
    "log_storage": "local",
    "skip_if_running": true
  }
]
```

### Fields

| Field | Required | Meaning |
|---|---|---|
| `request_id` | yes | Unique ID (letters, numbers, hyphens). Must not collide with a `cmd_id`. Immutable after creation. |
| `description` | no | Free text shown in lists. |
| `method` | yes | `GET`, `POST`, `PUT`, `PATCH`, `DELETE`, `HEAD` or `OPTIONS`. |
| `url` | yes | Absolute `http://` or `https://` URL. May contain its own query string. |
| `query_params` | no | Ordered list of `{key, value, enabled}`; appended to the URL query. `enabled` defaults to `true`. |
| `headers` | no | Ordered list of `{key, value, enabled}`. A `Content-Type` header overrides the automatic one; a `Host` header overrides the Host. |
| `auth` | no | See *Authentication*. |
| `body` | no | See *Bodies*. |
| `timeout_seconds` | no | Whole-request timeout. Default 30, maximum 600. A run that exceeds it fails with "Timed out: no complete response within N seconds …". |
| `follow_redirects` | no | Default `true`. With `false` a `3xx` response is returned as the result. |
| `max_redirects` | no | Default 10, maximum 50. |
| `skip_tls_verify` | no | `true` disables certificate verification (`--validate` warns about it). |
| `retry_count` | no | Extra attempts after a transport error or `5xx`. Default 0, maximum 10. |
| `retry_delay_seconds` | no | Pause between attempts. |
| `max_output_size` | no | Body kept in memory / API / UI (`B`, `KB`, `MB`, `GB`). Default 2MB. |
| `expected_status` | no | Comma list of codes, ranges or classes: `200`, `200,201`, `2xx`, `200-299,404`. Default `2xx`. |
| `assertions` | no | See *Assertions*. |
| `cron_schedule` | no | Five-field cron expression, server timezone. |
| `bearer_token` | no | Token external callers must send to `/api/http-requests/{id}`. Without it the request can only be run with a JWT (UI, granted user) or by cron / RunBooks. |
| `notify_webhook` | no | Same form-encoded webhook as Commands, plus an `http_status` field. |
| `notify_email` | no | Same email settings as Commands. Subject placeholders: `{{status}}`, `{{request_id}}`, `{{host}}`, `{{trace_id}}`. |
| `log_storage` | no | `local` (default) or `s3`. |
| `skip_if_running` | no | Skip a new run while the previous one is still executing. |

### Authentication

| `auth.type` | Fields | Sent as |
|---|---|---|
| `none` (or no `auth`) | – | nothing |
| `basic` | `username`, `password` | `Authorization: Basic …` |
| `bearer` | `token`, optional `prefix` (default `Bearer`) | `Authorization: <prefix> <token>` |
| `api_key` | `key`, `value`, `add_to` (`header` default, or `query`) | header `<key>: <value>` or query parameter `?<key>=<value>` |

Secrets (`password`, `token`, `value`) are **never returned** by the configuration API or shown in the UI; the UI shows a placeholder and keeps the stored value when the field is left empty. Changing the auth type requires entering the new secret. In log files `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `X-API-Key`, `X-Auth-Token` values and an `api_key` query parameter are masked.

### Bodies

| `body.type` | Fields | Default `Content-Type` |
|---|---|---|
| `none` (or no `body`) | – | – |
| `json` | `content` (must be valid JSON) | `application/json` |
| `text` | `content` | `text/plain; charset=utf-8` |
| `xml` | `content` | `application/xml` |
| `form` | `fields` list of `{key, value, enabled}` | `application/x-www-form-urlencoded` |

Multipart / file uploads are not supported in this version.

### Assertions

| `type` | Fields | Passes when |
|---|---|---|
| `body_contains` | `value` | the response body (as kept in memory) contains the text |
| `json_path_equals` | `path`, `value` | the body is JSON and the value at the dotted path equals `value` (compared as text: `7`, `true`, `null`, `ok`). Paths: `data.items.0.id`, `items[0].id`, `$.a.b` |
| `max_response_ms` | `value` | the whole response (headers + body) arrived within that many milliseconds |

The status check (`expected_status`) always runs first. Results of every check are written to the log and returned by the API.

### Variables

`{{name}}` config variables and `${{ENV}}` environment variables are substituted in **every** field of a request (URL, headers, query parameters, auth secrets, bodies) when the configuration loads, exactly like Commands. A request body that must contain a literal `{{…}}` has to come from a config variable. The configuration API validates requests after substitution, so an unknown variable is rejected at save time.

## Using the HTTP Requests page

1. Open **HTTP Requests** in the sidebar and click **New request**.
2. **Request** tab: ID, method, URL, query parameters, headers, body and log storage.
3. **Auth** tab: none / basic / bearer / API key.
4. **Settings & Checks** tab: timeout, retries, redirects, TLS verification, output limit, expected status and assertions.
5. **Scheduling & Security** tab: cron schedule, external bearer token, webhook, skip-if-running.
6. **Notifications** tab: email settings. **Recent Logs** tab: the last runs of this request.
7. **Save & Run** sends the request immediately and opens the result: HTTP status, timing, attempts, every check, the response headers, the (pretty-printed) body and a link to the full log.

The row menu offers **Edit**, **Duplicate** (secrets are not copied) and **Delete**. A request used by a RunBook step or granted to a user cannot be deleted until that reference is removed.

## Log file

```
               trace_id : 842c5a729e9bf63c
    request_received_at : 2026-10-13 20:28:24.062
                 cmd_id : healthcheck-api
               job_type : http_request
             request_id : healthcheck-api
                 method : GET
                    url : https://api.example.com/health
                   host : api.example.com
              remote_ip : 127.0.0.1
                  agent : Mozilla/5.0 …
             invoked_by : admin
                trigger : manual
            http_status : 200
            response_ms : 143
         response_bytes : 27
               attempts : 1
              exec_time : 0.145s
                  error : none
               exitcode : 0
                 output:
--- request (attempt 1) ---
GET /health?verbose=1 HTTP/1.1
Host: api.example.com
Accept: application/json
Authorization: Bearer ****
User-Agent: CommandGuard/0.7.0
--- response headers (attempt 1) ---
HTTP/1.1 200 OK
Content-Type: application/json
--- response body (attempt 1) ---
{"status":"ok"}
--- checks ---
PASS status: HTTP 200 matches expected 2xx
PASS body_contains #1: body contains "\"status\":\"ok\""
```

`cmd_id` is kept equal to the request ID so that every existing log tool (Logs page, grants, overview, S3 pointer files) works unchanged; `job_type : http_request` tells them apart. RunBook step runs additionally carry `runbook_id`, `run_id`, `step_id`, `step_index` and `attempt`. Logs are deleted by the same `log_retention_days` cleanup as Command logs.

## API

Execution (admin JWT, a JWT of a user holding the grant, **or** the request's `bearer_token`):

```bash
curl -X POST -H "Authorization: Bearer $JWT"   http://localhost:8080/api/http-requests/healthcheck-api
curl        -H "Authorization: Bearer <bearer_token>" http://localhost:8080/api/http-requests/healthcheck-api
```

Both `GET` and `POST` are accepted. The response is `{"success": true, "data": {...}}` with `request_id`, `method`, `url`, `host`, `http_status`, `status_text`, `response_headers`, `output` (body, size-limited), `response_bytes`, `response_ms`, `exec_time`, `trace_id`, `is_output_truncated`, `exit_code`, `error`, `attempts` and `checks[]` (`name`, `passed`, `detail`). HTTP 200 is returned whenever the request was sent, even when checks failed (`exit_code: 1`). A transport failure (no response at all) returns HTTP 500 with the same payload; `skip_if_running` returns 409.

Configuration (admin JWT):

- `GET /api/config/http-requests` – list (auth secrets removed, secret header values masked as `••••`, `has_bearer_token`)
- `POST /api/config/http-requests` – create
- `GET|PUT|DELETE /api/config/http-requests/{request_id}` – read / update / delete. On update an empty or masked secret keeps the stored value. Delete is refused (409) while the request is running, referenced by a RunBook step, or granted to a user.

Every create / update / delete is written to the security audit log (`http_request_create`, `http_request_update`, `http_request_delete`).

Regular users: `GET /api/user/http-requests` lists granted requests; `PUT /api/user/http-requests/{id}/schedule` edits the shared cron schedule. Scheduled requests appear in `GET /api/status` under `scheduled_http_requests` and in the Scheduled Jobs Overview with type `http_request`.

## RunBook steps

A RunBook step references either a Command (`cmd_id`) or an HTTP request (`request_id`), never both:

```json
{ "step_id": "warm-cache", "request_id": "healthcheck-api", "retry_count": 1, "timeout_seconds": 20 }
```

Step retries, delays, timeouts and `on_failure` work the same way as for Command steps. The step's own webhook / email notifications are sent by the request itself. See [runbooks.md](runbooks.md).

## User grants

`http_request_ids` on a user (Users & Security → *Assigned HTTP Requests*) allows that user to run the request from the HTTP Requests page or the API, edit its shared schedule, and read its logs. Command and RunBook grants do not include HTTP requests. See [multi-user.md](multi-user.md).

## Validation

`./cguard --validate` (and every hot reload / backup restore) checks IDs, methods, URLs, auth and body types, numeric bounds, `expected_status` syntax, assertions, cron schedules, `max_output_size` and `log_storage`, and warns about `skip_tls_verify` and email notifications without SMTP.
