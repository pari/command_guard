# Security policy

CommandGuard stores credentials for your servers and can run commands on them. Please take security reports seriously, and so will we.

## Reporting a vulnerability

**Do not open a public issue for a security problem.**

Use GitHub's private reporting: on the repository page go to **Security → Report a vulnerability**. Include the version (`./cguard --version`), what you did, what happened and what you expected.

You should get an acknowledgement within a few days. Please allow reasonable time for a fix before publishing details.

## Supported versions

Only the latest release receives security fixes.

## Known limitations

These are design limits of the current version, listed so you can decide whether CommandGuard fits your environment. Fixes are planned; contributions are welcome.

| Limitation | What it means for you |
|---|---|
| **A host's key is trusted on the first connection** | By default (`ssh_host_key_checking: "tofu"`) the first SSH connection to a host records the key it is shown, as OpenSSH does when you answer "yes"; an attacker on the network path at that moment could have their key recorded. Later connections are refused if the key differs. To close the gap, compare the recorded fingerprint on the Hosts page with the server's own (`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`), or set `"strict"` and approve each key before use. Hosts that existed before version 0.8.0 are recorded on their next connection. WinRM hosts are not covered: use `winrm_https` with certificate verification. SSH host certificates are not supported. |
| **Secrets are stored in plain text in `config.json`** | The admin password, host passwords, SMTP and VNC passwords, bearer tokens, TOTP secrets and the JWT signing key are in the config file. The server writes it, and the keys, logs, audit trail and queued mail, readable by the service account only (and tightens older installations at startup); add disk encryption, or keep secrets out of the file with `${{ENV_VAR}}` substitution. Passwords of regular users are stored only as bcrypt hashes. |
| **The first visitor sets the admin password** | Until an admin password exists, the sign-in page lets whoever opens it choose one. Do the first-run setup while the server is reachable only from localhost, or set `admin_password` in `config.json` before starting. |
| **The session token is kept in browser `localStorage`** | A cross-site scripting bug in the UI would expose the admin session. Keep CommandGuard on its own origin (do not serve other apps from the same host name). |
| **No built-in TLS** | The server speaks plain HTTP. Put it behind a reverse proxy that terminates TLS, and list that proxy in `trusted_proxies` so client IPs, IP allow-lists and login lockouts work correctly. |
| **Single built-in `admin` account** | There is one administrator; other accounts are regular users with explicit grants. There is no SSO/SAML/OIDC. |
| **Login lockout state is in memory** | Brute-force counters reset when the server restarts. |
| **A stopped command may keep running on the host** | When a time limit fires, the SSH/WinRM session is closed; a remote process that ignores the closed session continues. |

## Hardening checklist

- Bind to `127.0.0.1` and publish only through a TLS reverse proxy.
- Keep `dev_mode` set to `false`. Development mode accepts a fixed password and TOTP code.
- Give every externally callable command, RunBook and HTTP request a long random `bearer_token`.
- Use dedicated, least-privilege accounts on your hosts rather than `root`.
- Restrict users with `allowed_ips`, weekdays and time windows where you can.
- Review **Users & Security → Security audit** regularly.
- Take encrypted backups from **Backup & Restore** and store the passphrase separately.
