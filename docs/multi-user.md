# Multi-User Security

Command Guard 0.7.0 keeps the built-in `admin` account and adds regular users with explicit Command and RunBook grants. Usernames are lowercase and immutable; `admin` is reserved case-insensitively.

## Account lifecycle

Administrators create users in **Users & Security** with a temporary password. On first sign-in the user must choose a 12–128 character password and enroll a TOTP authenticator before a JWT is issued. Passwords are stored only as bcrypt hashes. Password changes, administrator password resets, TOTP resets, disabling, and deletion invalidate existing sessions. The built-in admin login remains compatible with clients that omit `username`.

Regular users see only Overview, assigned Commands, assigned RunBooks, filtered Logs, and My Account. Command and RunBook grants are independent: a RunBook grant allows that workflow and its step logs, but does not allow its Commands to run independently.

## User configuration

Users are normally managed through the UI/API. Their persisted shape is:

```json
{
  "username": "operator.one",
  "enabled": true,
  "password_hash": "$2a$...",
  "force_password_change": false,
  "totp_secret": "BASE32...",
  "token_version": 3,
  "command_ids": ["system-summary"],
  "runbook_ids": ["basic-health-check"],
  "http_request_ids": ["healthcheck-api"],
  "allowed_ips": ["10.20.0.0/16", "2001:db8::/48"],
  "allowed_weekdays": ["monday", "tuesday", "wednesday", "thursday", "friday"],
  "allowed_time_start": "08:30",
  "allowed_time_end": "18:00",
  "temporary_any_ip_until": "2026-07-14T18:30:00+05:30"
}
```

Grants are independent: `command_ids`, `runbook_ids`, `http_request_ids` (saved HTTP requests, see [http-requests.md](http-requests.md)) and `terminal_hosts` each unlock only their own page, execution endpoint, shared-schedule editing and log access.

Defaults are `allowed_ips: ["*"]`, every weekday, and an all-day time window. `*` cannot be combined with another IP rule. Time windows use server time, include the start, exclude the end, and cannot be equal or cross midnight. Temporary any-IP access bypasses only the IP rule and expires at its exact RFC3339 timestamp.

## Reverse proxies

Forwarding headers are ignored unless the socket peer matches `trusted_proxies`. Entries are exact IPs or CIDRs; the default is an empty list. The supplied loopback deployment uses:

```json
"trusted_proxies": ["127.0.0.1", "::1"]
```

Only add addresses of proxies that overwrite or safely append forwarding headers. Malformed forwarding chains are ignored.

## APIs

- `POST /api/auth/login` (username + password + `totp_code` in one request), `POST /api/auth/verify-2fa` (authenticator enrollment only), and `POST /api/auth/complete-onboarding`
  - All login failures return the same `"Invalid login credentials or OTP"`; 3 consecutive failures per IP+username lock it for 30s, escalating to 2m, 10m, 30m, and 10 failures from one IP in 5 minutes lock that IP for 5 minutes (HTTP 429 + `Retry-After`)
- `GET/POST /api/users`, `GET/PUT/DELETE /api/users/{username}`
- `POST /api/users/{username}/enable|disable|reset-password|reset-totp`
- `POST /api/account/password`
- `GET /api/user/commands`, `GET /api/user/runbooks`
- `PUT /api/user/commands/{id}/schedule`, `PUT /api/user/runbooks/{id}/schedule`
- `GET /api/security/audit?page=1&page_size=50&filter=...`

User schedule endpoints accept only `cron_schedule`, with a valid five-field expression or an empty string. Schedules are shared configuration and hot-reload immediately.

## Auditing and logs

Security events are appended to `logs/audit/YYYY-MM-DD.jsonl`. Entries include timestamp, actor, role, source IP, action, target, outcome, and sanitized details. Credentials, hashes, TOTP values, tokens, command bodies/output, and sensitive configuration are excluded. `audit_retention_days` defaults to 90.

Command logs include `invoked_by` (`admin`, username, `scheduler`, or `api-token`) and `trigger` (`manual`, `schedule`, `runbook`, or `api`). Regular-user log access is enforced by current grants on listing, inline content, direct access, and download.
