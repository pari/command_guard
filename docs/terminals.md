# Persistent Terminals

Command Guard can open interactive SSH terminals in the browser that **keep running after you log out, close the browser, or restart `cguard`**. Open a session, start `vim`, `htop`, Claude Code or a long build, walk away, and resume the same session hours later from another machine. A session ends only when the remote shell exits (`exit`, `Ctrl+D`, `logout`) or when it is killed from the Terminals page. No `tmux` or `screen` is needed on the remote host.

## How it works

```
Browser (xterm.js) ──WebSocket──> cguard ──Unix socket──> cg_sessiond ──SSH + PTY──> remote host
```

- **`cg_sessiond`** is a small companion process built from `./sessiond`. It owns the SSH connections and PTYs. `cguard` starts it automatically the first time a terminal is used (listening on `run/sessiond.sock`, mode 0600) and logs its output to `logs/sessiond.log`. Because it runs in its own process session, restarting `cguard` (for example with `./compile_and_run.sh`) does not touch open terminals.
- The daemon knows nothing about `config.json`. `cguard` resolves the host credentials and passes them when a session is created, enforces all authorization, and simply bridges the browser's WebSocket to the daemon.
- Recent output (default 2 MB per session) is kept in memory and replayed when you resume; the terminal size is then nudged so full-screen programs (vim, htop, Claude Code, …) repaint.
- Only one browser view is attached to a session at a time. Resuming from a second window takes over and the first one shows a "resumed elsewhere" notice with a button to take it back.

Sessions are lost when `cg_sessiond` itself stops (a server reboot, `pkill cg_sessiond`), when the network path to the remote host breaks for longer than the SSH keepalive tolerates, or when the remote host closes the connection. In those cases the remote processes receive a hangup exactly as with a plain `ssh` session.

## Who can use it

- **admin** can open terminals on every SSH host and sees/kills every session.
- **Regular users** need the host in their `terminal_hosts` grant (Users & Security → user → *Terminal hosts*). They see and resume only their own sessions. Revoking the grant blocks new attaches immediately; existing attached views are not cut off.
- The usual per-user IP / weekday / time-window policy applies to every list, create, resume and kill request.
- **WinRM hosts are not supported** (no interactive PTY). Windows hosts with OpenSSH Server can be added as `connection_type: "ssh"` hosts instead.
- **Bastion hosts** work: a host with `bastion_host_ref` is reached by logging into the bastion first and opening the target's SSH connection through it; the daemon keeps both connections for the life of the session and the tab tooltip shows "Via bastion". This needs `cg_sessiond` **1.2.0 or newer**; with an older daemon still running, creating such a terminal answers HTTP 501 with a message asking to restart the daemon (stop this installation's `cg_sessiond` by its PID, which ends every open session). Direct hosts keep working with the old daemon.
- A host that is granted as a terminal host to a user cannot be deleted until the grant is removed (same protection Commands and RunBooks have).

Security audit (`logs/audit/`) records `terminal_create`, `terminal_attach`, `terminal_detach`, `terminal_rename` (with old and new name), `terminal_kill` and the `terminal_agent_session_*` preset changes with the host ID and session ID. Keystrokes and screen output are never logged.

## Configuration

Everything is optional. Without a `terminals` block the feature is enabled with the defaults shown here.

```json
"terminals": {
    "enabled": true,
    "max_sessions_per_user": 10,
    "scrollback_bytes": "2MB",
    "idle_kill_hours": 0,
    "images_enabled": true,
    "image_retention_days": 7
}
```

| Field | Meaning |
|---|---|
| `enabled` | `false` hides the feature and rejects all terminal API calls. |
| `max_sessions_per_user` | Running sessions each account may have at once, admins included: 1 to 200, default 10. Admins can also set it in App Settings > General Settings. It applies at once; lowering it does not end running sessions, but new ones are refused until the count is below the limit. |
| `scrollback_bytes` | Output kept per session for replay on resume (`KB`, `MB`, `GB` units like `max_output_size`). |
| `idle_kill_hours` | Kill sessions that nobody has been attached to for this many hours. `0` never kills idle sessions. |
| `images_enabled` | `false` refuses pasting images and dropping images or files into sessions. |
| `image_retention_days` | Days uploaded images and files stay in `/tmp` on the host: 1 to 365, default 7. Admins can also set it in App Settings > General Settings. |

The block is validated by `./cguard --validate` and on hot reload.

## Using the Terminals page

1. Open **Terminals** in the sidebar and click the yellow **+** in the Sessions header to show the host/name form (it is hidden by default so the terminal gets the whole pane). Pick an SSH host, optionally give the session a name, click **New Terminal** (or press Enter in the name field). Click the + (now an ×) again to hide the form.
2. Work normally. Every session (yours, or everyone's for admin) is a vertical tab on the left of the terminal:
   - The dot shows where a session is attached: **green** (glowing) = in this window (it **blinks** while Claude Code or Codex in that terminal is blocked mid-task on a question for you (permission prompt, AskUserQuestion, plan approval, Codex command/edit approval) but not at the idle prompt after a finished task; detected from the screen text by `agentWaiting` in `terminals.js`; only green dots can blink because other sessions have no screen in this browser), **orange** = in another window, tab or device (opening it here takes it over and the other window gets a "Take it back" banner), **grey ring** = running but not attached anywhere. Ended sessions have no dot. The list refreshes every 10 s while the page is visible and whenever the window regains focus. Click the green dot to rename the session (prefilled with the current name; leave it empty to go back to the host name).
   - While any dot blinks, the browser window title flashes once a second between `🟢 Waiting: <session name>` (`🟢 3 agents waiting` for several) and the normal title, so you notice it from another browser tab. It stops as soon as no dot is blinking. The timing comes from `ui/js/title-flash-worker.js` because browsers slow a hidden page's own repeating timers to about once a minute; the app's CSP blocks `blob:` workers, so it must stay a same-origin file.
   - Hover a tab for its details: session id, host, login, owner (admin), created, last attached and status.
   - On hover, × *detaches* the tab (the shell keeps running) and the skull kills the session.
   - An ended session (exit, Ctrl+D, kill, idle timeout) shows its end message and disappears from the list after about 5 s, in every window (the daemon still keeps it for 2 minutes, but the page no longer shows it).
   - Drag the line between the tab list and the terminal to make the list wider or narrower (or focus it and use ←/→); double-click it to go back to the default width. The width is remembered in this browser.
   - The **Aa** button in the tab list header opens the font size popover (stepper, slider, Reset). **Ctrl+Shift+=** / **Ctrl+Shift+-** make the text larger or smaller and **Ctrl+Shift+0** goes back to the default, also while a terminal has focus; on the Terminals page they replace the browser's page zoom for that key combination. The range is 8 to 32 px (default 14). One size applies to all sessions and is saved per browser (`localStorage` key `terminalFontSize`), so a laptop and a large monitor can differ. A size change refits the visible terminal and resizes the remote side; other open tabs pick it up when you switch to them.
   - The **+** in the tab list shows/hides the New Terminal form (from full screen it leaves full screen first). The expand button gives a full-screen terminal. The terminal area always fills the height left in the window.
3. Log out, come back later and click a session's tab to resume it. Ctrl+Shift+↑/↓ switches between the attached tabs.
   - Drag a tab up or down to reorder the list (a line shows where it will land), or move the active tab with Ctrl+Shift+←/→. The order is saved for your account on the server, so every browser and device shows the same order. New sessions appear at the bottom, and Ctrl+Shift+↑/↓ follows your order.
4. Type `exit` (or `Ctrl+D`) to end a session cleanly. **Kill** sends a hangup to the remote shell and everything running inside it.

## Pasting images and dropping files

An agent in a session runs on the remote host and cannot see your browser's clipboard or your computer's disk, so CommandGuard copies the image or file there for it. Paste an image into a session (Cmd+V on a Mac, right-click > Paste elsewhere) or drop any files onto the terminal, for example from Finder. A notice at the top of the session shows while the copy runs. Once it finishes, the file's path is pasted into that session without pressing Enter, so you can add your text and send it. Claude Code reads an image or document from a path.

- Ctrl+V is not used for this: it still sends `^V` to the session (vim block mode, shell literal-next). Ctrl+Shift+V is Chrome's "paste as plain text", which carries no image, so the page reads the clipboard directly instead; that needs HTTPS and the browser's permission.
- If the clipboard holds text as well as an image (cells copied from Excel), the text is pasted, as usual.
- **Images:** PNG, JPEG, GIF and WebP up to 20 MB go to `/tmp/cg-paste-<random>.<ext>` on the host.
- **Other files** (drop only) of any type up to 100 MB go to `/tmp/cg-paste-<random>-<name>`, keeping the name and extension. Characters other than letters, digits, `.`, `_` and `-` become `_`, so the path needs no quoting, and names are cut to 100 characters. Folders and empty files are refused; zip a folder first. Other files in the same drop still upload.
- Either way, the file is readable only by the session's login, and nothing is stored on the CommandGuard server. If `/tmp` on the host is held in memory (tmpfs), large files use RAM until they are removed. An upload stops after 15 minutes.
- The copy uses a new SSH connection from `cguard` with the Host's credentials, through its bastion if it has one. It needs only `sh`, `mktemp`, `cat` and `mv` on the host, not SFTP.
- Files older than `image_retention_days` (default 7) are removed the next time an image or file is copied to that host by that login. Until then they stay, so a shorter period takes effect at the next upload.
- **The path is on the session's Host.** After `ssh` to another machine or entering a container inside the session, the file is not there. The toast names the Host that received it.
- If the Host's address or username has been edited since the session started, the copy is refused (HTTP 409) rather than sent to a different machine or login. Start a new session.
- A reverse proxy in front of CommandGuard must allow bodies this large (nginx defaults to 1 MB: `client_max_body_size 100m;`).

## Agent Sessions

Most terminals start the same way: pick a host, `cd` into an app folder, start a coding agent. An **Agent Session** saves that once (host + folder + optional coding agent + app name + icon) so it can be launched with two keystrokes.

- Click the <i>robot</i> button next to the yellow **+** in the Sessions header (or press **Ctrl+Shift+L**, also while a terminal has focus) to open the launch palette. Type a few letters to filter by name, agent, folder or host; the first match is pre-selected, so "di" + Enter launches *Diary*. ↑/↓ move, Enter launches, Esc closes. Recently launched presets float to the top (remembered per browser). Click the pencil on a row to edit it, **Manage** to see the full list, **Blank terminal** to open the plain host/name form instead.
- Launching creates a normal session named after the preset: `Diary` when free, otherwise the lowest free `Diary_02`, `Diary_03`, … among the running sessions you can see. The tab shows the preset's icon (Font Awesome icon with a colour, or an emoji) before the name, in every window, and the tooltip lists the preset, agent and folder. Renaming the session keeps the icon; deleting the preset later does not change running tabs.
- As soon as the shell is ready the browser types `cd '<folder>' && clear && <agent> <args>` (or `cd '<folder>' && clear` for a shell-only preset such as `Diary_sh`) and presses Enter. The folder is single-quoted for the remote shell; the screen is cleared so the agent starts on a clean terminal, and if the `cd` fails nothing else runs. The command is visible in the terminal like anything you typed yourself, and nothing is logged.
- **Manage** lists the presets with search, launch, edit and delete, and **Add** opens the form: scope, app name (max 30 characters, becomes the tab name), host (the SSH hosts you may open terminals on), folder path, coding agent (`None (shell only)`, `claude`, `codex`, `gemini`, `aider`, `opencode` or a custom command), extra arguments, and the icon picker (48 icons with 8 colours, or an emoji grid plus a box to paste any emoji). A live preview shows the tab and the exact command.
- **Shared** presets (admin only) are stored in `terminals.agent_sessions` in `config.json`, so they are hot-reloaded, validated by `--validate`, included in backups and offered to every user whose `terminal_hosts` grant includes the preset's host. A host used by a shared preset cannot be deleted (HTTP 409; the Hosts page locks it like Commands do). **Personal** presets belong to one account (admin included) and are kept by `cguard` in `run/terminal_agent_sessions.json`; they are offered only while the host is still granted. The scope of a preset cannot be changed afterwards.
- Security audit records `terminal_agent_session_create`, `terminal_agent_session_update` and `terminal_agent_session_delete` with scope, preset id, name, host, folder and agent, and `terminal_create` carries the preset name when a session was launched from one.

```json
"terminals": {
    "agent_sessions": [
        { "id": "7f3a9c2b1d4e", "name": "Diary", "host_ref": "node1", "folder": "/ssd/www/diary", "agent": "claude", "args": "--resume",
          "icon": { "type": "fa", "value": "fa-book", "color": "#4cd964" } },
        { "id": "0b1c2d3e4f5a", "name": "Diary_sh", "host_ref": "node1", "folder": "/ssd/www/diary",
          "icon": { "type": "emoji", "value": "📓" } }
    ]
}
```

| Field | Meaning |
|---|---|
| `id` | Unique within the list (letters, digits, `-`, `_`; generated by the UI). |
| `name` | Tab base name, 1–30 characters, no control characters. |
| `host_ref` | An SSH host from `hosts`. |
| `folder` | Directory to `cd` into on the host (quoted for the shell at launch). |
| `agent` | Command started after the `cd`; omit or leave empty for a shell-only preset. |
| `args` | Extra arguments appended to `agent`. |
| `icon` | `{"type":"fa","value":"fa-book","color":"#rrggbb"}` or `{"type":"emoji","value":"📓"}`; optional. |

`cg_sessiond` is not involved: sessions carry no metadata in the daemon, so the icon shown on a running tab comes from `run/terminal_meta.json` (session id → preset metadata), written when a session is created from a preset and pruned when the daemon no longer lists the session. A daemon restart is therefore never needed for this feature.

## API

All endpoints require the regular JWT (`Authorization: Bearer …`).

| Method & path | Purpose |
|---|---|
| `GET /api/terminals/hosts` | SSH hosts the caller may use (`host_id`, `host`, `port`, `username`). |
| `GET /api/terminals` | Sessions visible to the caller plus daemon info, limits and `order` (the caller's saved tab order: session ids, only those still listed). |
| `PUT /api/terminals/order` | Save the caller's tab order: `{"ids": ["<session id>", …]}`. Ids of sessions the caller cannot see, ended ones and duplicates are dropped (max 200). Stored per user by `cguard` in `run/terminal_order.json`, so it is the same in every browser and survives restarts of both processes. Returns `{"order": [...]}`. |
| `POST /api/terminals` | Create a session: `{"host_ref": "...", "name": "...", "cols": 120, "rows": 30}`. Returns the session (`id`, …). An optional `meta` object (`preset_id`, `name`, `agent`, `folder`, `icon`) marks a session launched from an Agent Session; it is stored by `cguard` and returned as `meta` on the session in every later list. |
| `GET /api/terminals/agent-sessions` | `{"shared": [...], "personal": [...], "can_share": bool}`: the presets the caller may launch (shared ones only for hosts the caller may use; personal ones are the caller's own). |
| `POST /api/terminals/agent-sessions` | Create a preset: `{"scope": "shared"\|"personal", "name", "host_ref", "folder", "agent", "args", "icon"}`. `shared` needs admin and writes `config.json`; `personal` writes `run/terminal_agent_sessions.json`. The host must be usable by the caller. Returns `{"scope", "preset"}` (201). |
| `PUT /api/terminals/agent-sessions/{id}` | Update a preset (same body; the scope names the list the id is looked up in and cannot move a preset between lists). |
| `DELETE /api/terminals/agent-sessions/{id}?scope=shared\|personal` | Delete a preset (`scope` defaults to `personal`). |
| `GET /api/terminals/{id}` | One session. |
| `PUT /api/terminals/{id}` (or `PATCH`) | Rename a session: `{"name": "..."}` (max 40 characters, control characters dropped; empty = host ID). Returns the updated session. Needs `cg_sessiond` 1.1.0+; an older running daemon gives HTTP 501 asking for a daemon restart. |
| `DELETE /api/terminals/{id}` | Kill a session. |
| `POST /api/terminals/{id}/images` | Copy an image to the session's host. The raw image is the body (max 20 MB; type decided from its bytes). Returns `{"path", "host_ref", "host", "size"}` (201). Audit: `terminal_image_upload`. |
| `POST /api/terminals/{id}/files?name=<name>` | Copy any file to the session's host. The raw file is the body (max 100 MB). Returns the same fields (201). Audit: `terminal_file_upload`. |
| `GET /api/terminals/{id}/ws` | WebSocket attach. The JWT is sent as the second WebSocket subprotocol (`["cg-jwt", "<token>"]`); `?token=` is accepted as a fallback. Binary frames carry keystrokes/output, text frames carry JSON control messages (`{"type":"resize","cols":…,"rows":…}` from the browser; `{"type":"replay_end"|"exit"|"takeover"|"error"}` from the server; `replay_end` marks the end of the scrollback replay, and the browser discards the replies xterm.js generates to terminal queries contained in the replay until it arrives). |

Session object fields: `id`, `owner`, `host_ref`, `name`, `host`, `username`, `created_at`, `last_attached_at`, `last_detached_at`, `attached`, `cols`, `rows`, `status` (`running` or `exited`), `exit_code`, `exit_reason`, and `meta` (only for sessions launched from an Agent Session: `preset_id`, `name`, `agent`, `folder`, `icon`).

## Operations

- Both binaries must sit in the same folder: `compile_and_run.sh` builds them into `instance/` and starts `cguard` from there.
- `compile_and_run.sh` deliberately does **not** restart `cg_sessiond`. To run a newly built daemon, stop this installation's daemon by its PID, e.g. `kill -TERM <pid>` (this ends every open session; do not use `pkill -f cg_sessiond`, which also matches other installations on the machine; it shuts down cleanly and `cguard` starts the new binary on the next terminal request).
- Daemon log: `logs/sessiond.log`. Socket: `run/sessiond.sock` (both ignored by git).
- **Host keys**: before a session is created, `cguard` checks the host's key (and the bastion's) exactly as for Commands, recording it on first use; see "SSH host key verification" in the [user manual](user-manual.md). It then passes the stored key to the daemon, which pins it for the session's own connection (`cg_sessiond` 1.3.0 or newer). An older daemon that is still running ignores the pinned key, but the check by `cguard` still happens first, so no daemon restart is needed after an upgrade.
