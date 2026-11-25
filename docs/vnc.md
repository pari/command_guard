# Remote Desktops (VNC)

VNC desktops in the browser, for the admin and for users the admin maps to them.
The server (`cguard`) connects and signs in to the VNC server itself and then
hands the authenticated stream to the browser viewer (noVNC 1.7.0, vendored in
`ui/js/novnc-1.7.0/`). **VNC passwords never reach the browser.**

```
browser (noVNC) ──WebSocket /api/vnc/ws──▶ cguard ──TCP or SSH tunnel──▶ VNC server
                  security "None"             does the real auth (VNC / ARD / VeNCrypt / Tight)
```

## Files

| File | Purpose |
|------|---------|
| `vnc.go` | Config types, validation, admin API `/api/config/vnc-connections`, access mapping |
| `vnc_rfb.go` | RFB client handshake + auth, fake "None" handshake to the browser, viewer message filter |
| `vnc_sessions.go` | Runtime API `/api/vnc`, tickets, WebSocket bridge, session registry, enforcement, thumbnails |
| `vnc_test.go` | Fake RFB server tests (auth types, filter, API, WebSocket end to end) |
| `ui/js/components/remotedesktops.js` | Remote Desktops page (gallery, tabs, admin Manage view + editor) |
| `ui/js/vnc-viewer.js` | ES module viewer shared by the page and the pop-out |
| `ui/vnc.html`, `ui/js/vnc-popout.js` | Pop-out window |

## Configuration

```json
"vnc": { "enabled": true, "idle_disconnect_minutes": 60, "max_sessions_per_user": 5 },
"vnc_connections": [
    {
        "vnc_id": "design-mac",
        "name": "Design iMac",
        "description": "Studio",
        "tunnel_host_ref": "linux-host",
        "host": "127.0.0.1",
        "port": 5900,
        "auth_type": "auto",
        "username": "designer",
        "password": "${{MAC_PASSWORD}}",
        "tls_skip_verify": false,
        "allow_clipboard": true
    }
]
```

| Field | Notes |
|-------|-------|
| `vnc.enabled` | Default true. Turning it off disconnects every open viewer within ~15 s |
| `vnc.idle_disconnect_minutes` | Default 60, `0` = never. Counted from keyboard/mouse input and the viewer's activity ping |
| `vnc.max_sessions_per_user` | Default 5 (1–50). Open viewers per account |
| `vnc_id` | 1–64 chars: letters, digits, `-`, `_`, `.`. Cannot be changed |
| `tunnel_host_ref` | Optional SSH Host to tunnel through (its bastion is followed). `host` is then as seen from that host, default `127.0.0.1` |
| `host` / `port` | VNC server; port default 5900 |
| `auth_type` | `auto` (default), `none`, `vnc`, `ard`, `vencrypt` |
| `username` | Only for `ard` (macOS) and VeNCrypt Plain/X509Plain |
| `password` | Never returned by the API; blank on update = keep; `clear_password: true` removes it; `${{ENV}}` works |
| `tls_skip_verify` | VeNCrypt X509: accept self-signed certificates |
| `allow_clipboard` | Default true |

`--validate`, hot reload and restore check the block; `--validate` warns about direct
(non-tunnelled) connections with unencrypted auth types.

User grants live on the user: `"vnc_access": [{"vnc_id": "design-mac", "level": "control"}]`
(`control` or `view`; an unspecified level becomes `view`). Admin always has control.

### Auth types

| Server | Setting |
|--------|---------|
| macOS Screen Sharing | `ard` (or `auto` with a username): macOS user + password, Apple ARD (type 30). Enable System Settings › General › Sharing › Screen Sharing |
| TigerVNC / x11vnc / TightVNC (Linux) | `vnc` (VNC password, first 8 chars count) or `vencrypt` (X509None/X509Vnc/X509Plain, Plain) |
| TightVNC / UltraVNC (Windows) | `vnc` |
| RealVNC Server | `vnc`, with RealVNC set to Authentication "VNC password" and Encryption "Prefer off" / "Always off". Its native RSA-AES auth is not supported |

`auto` picks, in order: None, ARD (if a username is set), VNC password, Tight, VeNCrypt.
VeNCrypt anonymous-TLS subtypes (TLSNone/TLSVnc/TLSPlain) need anonymous DH, which Go's
TLS lacks; use X509 types or an SSH tunnel.

## Security model

- **Tickets**: `POST /api/vnc/connections/{id}/sessions` connects and authenticates first
  (errors come back as JSON), then returns a one-time ticket valid 30 s and bound to the
  caller's IP. `GET /api/vnc/ws?ticket=…` consumes it. No JWT in the WebSocket URL.
- **Shared**: ClientInit always requests a shared session, so viewers and the console coexist.
- **Viewer → server filter** (`vncClientFilter`): every message is decoded; unknown types,
  oversized clipboard (10 MB) or fence payloads close the session.
  - view-only: KeyEvent, PointerEvent, ClientCutText, SetDesktopSize, QEMU key and XVP are dropped
  - clipboard off: ClientCutText dropped and the ExtendedClipboard pseudo-encoding stripped
  - the ExtendedMouseButtons pseudo-encoding is always stripped so pointer events stay 6 bytes
- **Server → viewer** is passed through unparsed (lengths depend on encodings), so
  remote→browser clipboard with `allow_clipboard: false` is suppressed by the viewer only.
- **Enforcement** every 15 s (`VNCAPI.enforce`): a session ends when Remote Desktops are
  disabled, the connection is deleted or its target/credentials change (a rename does not),
  the JWT expires, the user is disabled or their token revoked, the allowed IP/time window no
  longer matches, the grant is removed or reduced to view-only, or the viewer is idle.
- **Audit** (`logs/audit`): `vnc_connect` (success/failed), `vnc_disconnect` (reason,
  duration, bytes, client IP), `vnc_kick`, `vnc_ticket_ip_mismatch`,
  `vnc_connection_create|update|delete|test`, `vnc_access_update`.

## API

Admin (`AdminMiddleware`):

| Method | Path | |
|--------|------|-|
| GET/POST | `/api/config/vnc-connections` | List (with `access`) / create |
| GET/PUT/DELETE | `/api/config/vnc-connections/{id}` | Delete refused (409) while granted to a user |
| GET/PUT | `/api/config/vnc-connections/{id}/access` | `{"access":[{"username","level"}]}` replaces this connection's grants |
| POST | `/api/config/vnc-connections/{id}/test` | Connect + sign in; optional body = unsaved form values |

`PUT /api/config` preserves `vnc_connections` from disk (they are managed only by the API
above); `GET /api/config` redacts their passwords. Hosts used as `tunnel_host_ref` cannot be
deleted (409) or turned into WinRM hosts (400).

Runtime (JWT; 403 when `vnc.enabled` is false):

| Method | Path | |
|--------|------|-|
| GET | `/api/vnc/connections` | Connections the caller may use, level, viewers, `thumbnail_at` (admin also gets `target`) |
| POST | `/api/vnc/connections/{id}/sessions` | Body `{"view_only": bool}` → `session_id`, `ticket`, `ws_path`, … |
| GET/PUT | `/api/vnc/connections/{id}/thumbnail` | Last-seen JPEG (≤400 KB) as a data URL / upload from the viewer |
| GET | `/api/vnc/sessions` | Open sessions (admin: all; user: own) |
| GET/DELETE | `/api/vnc/sessions/{sid}` | Status and `end_reason` (kept 15 min) / disconnect (admin on another user = kick) |
| POST | `/api/vnc/sessions/{sid}/ping` | Viewer activity (idle timer) |
| GET | `/api/vnc/ws?ticket=` | WebSocket, binary RFB |

## Behaviour

- Closing a tab, leaving the page, logging out or restarting `cguard` only drops the
  viewer; the desktop keeps running. Open tabs are remembered per browser
  (`localStorage.remoteDesktopTabs`) and reopen on return.
- The viewer reconnects by itself (2, 4, 8, 15, 30, 30 s) after network loss or a restart;
  after a revocation, kick, change or idle timeout it shows the reason and a Reconnect button.
- Thumbnails: the viewer uploads a 360 px JPEG 5 s after connecting, every 5 min and when
  closed; stored in `run/vnc_thumbnails/<id>.jpg`.
- Toolbar: special keys (Ctrl+Alt+Del, Win/Cmd, Alt+Tab, …), clipboard panel, quality
  (low/medium/high), fit vs 1:1, full screen, pop-out window, disconnect.
- Cursor: noVNC draws the cursor shape the server sends. Servers that send none (macOS
  Screen Sharing) get the local arrow instead while the remote cursor is empty or fully
  transparent (`hookCursor` in `vnc-viewer.js`, CSS `.rd-local-cursor`).
