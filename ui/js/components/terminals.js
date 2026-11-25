// Persistent SSH Terminals page.
// Sessions live in the cg_sessiond daemon on the server; this page only attaches
// to them over a WebSocket. Closing a tab or logging out detaches without ending
// the remote shell. A session ends when the shell exits (exit / Ctrl+D / logout)
// or when it is killed from the list.
const Terminals = {
    sessions: [],
    hosts: [],
    open: {},        // session id -> { term, fit, ws, pane, connected, ended }
    activeId: null,
    refreshTimer: null,
    resizeHandler: null,
    keyHandler: null,
    headerObserver: null,
    attentionHandler: null,
    sidebarWidth: null,      // Sessions list width in px set by dragging the divider; null = CSS default
    defaultSidebarWidth: 230,
    fontSize: 14,            // terminal font size in px, the same for all sessions (saved in this browser)
    defaultFontSize: 14,
    minFontSize: 8,
    maxFontSize: 32,
    fontPopoverCloser: null, // closes the font size popover on a click outside it, while it is open
    fontToastTimer: null,
    titleFlash: null,        // while a green dot blinks: { worker, timer, base, alert, showingAlert }
    endedSeen: {},           // session id -> when this page first saw it ended (ms)
    endedTimer: null,        // re-renders the list when the next ended session is due to disappear
    endedLingerMs: 5000,     // how long an ended session stays in the list
    order: [],               // session ids in the user's own tab order (saved on the server)
    orderSaving: 0,          // order saves in flight; a poll meanwhile must not undo a fresh move
    dragId: null,            // session tab being dragged
    renderPending: false,    // a list refresh that waited for the drag to finish
    // Agent Sessions: saved launch presets (host + folder + optional coding agent + icon), see docs/terminals.md.
    agentSessions: { shared: [], personal: [], canShare: false },
    agentRecent: [],         // preset ids, most recently launched first (kept in this browser only)
    palette: { filtered: [], sel: 0 },
    agentForm: { editing: null, icon: null, pickerTab: 'fa' },
    launching: false,
    agentKnown: ['claude', 'codex', 'gemini', 'aider', 'opencode'],
    agentIcons: ['fa-book', 'fa-robot', 'fa-brain', 'fa-shield-halved', 'fa-cart-shopping', 'fa-database', 'fa-chart-line', 'fa-globe', 'fa-rocket', 'fa-bug', 'fa-code', 'fa-server', 'fa-cloud', 'fa-envelope', 'fa-mobile-screen', 'fa-gamepad', 'fa-film', 'fa-music', 'fa-camera', 'fa-heart', 'fa-star', 'fa-flask', 'fa-wrench', 'fa-truck', 'fa-building', 'fa-graduation-cap', 'fa-money-bill', 'fa-calendar', 'fa-folder-open', 'fa-terminal', 'fa-house', 'fa-briefcase', 'fa-newspaper', 'fa-pen-nib', 'fa-palette', 'fa-gear', 'fa-key', 'fa-lock', 'fa-bolt', 'fa-fire', 'fa-leaf', 'fa-seedling', 'fa-cube', 'fa-layer-group', 'fa-sitemap', 'fa-diagram-project', 'fa-microchip', 'fa-network-wired'],
    agentColors: ['#4cd964', '#6ea8fe', '#aab4ff', '#ff9f43', '#ffd43b', '#ff6b6b', '#c084fc', '#b8b8b8'],
    agentEmoji: ['📓', '📔', '📘', '📗', '📙', '📚', '🧪', '🧬', '🔬', '🧰', '🛠️', '⚙️', '🛒', '🛍️', '💳', '🏦', '📊', '📈', '📉', '🗂️', '📁', '🗃️', '🌐', '🛰️', '📡', '🔗', '🧭', '🗺️', '🏠', '🏢', '🏭', '🏗️', '🚀', '✈️', '🚗', '🚚', '🤖', '🧠', '👾', '🎮', '🎬', '🎵', '📷', '🎨', '✏️', '📝', '💬', '📧', '🔒', '🔑', '🛡️', '🔥', '⚡', '💧', '🌱', '🍀', '🐙', '🐳', '🐧', '🦀', '🐍', '☕', '🍕', '❤️'],

    esc(value) { return $('<div>').text(value == null ? '' : String(value)).html(); },

    async init() {
        this.fontSize = this.loadFontSize();
        this.render();
        this.initResizer();
        let timer = null;
        this.resizeHandler = () => { clearTimeout(timer); timer = setTimeout(() => this.sizeWorkspace(), 120); };
        window.addEventListener('resize', this.resizeHandler);
        // The header changes height when the New Terminal form is toggled or the description
        // re-wraps (window or nav-sidebar width), so the workspace is resized with it.
        if (window.ResizeObserver) {
            this.headerObserver = new ResizeObserver(() => this.resizeHandler());
            this.headerObserver.observe($('.terminals-header')[0]);
        }
        this.sizeWorkspace();
        this.keyHandler = (ev) => {
            if (this.isSwitchKey(ev)) { ev.preventDefault(); this.cycleTab(ev.key === 'ArrowDown' ? 1 : -1); }
            else if (this.isMoveKey(ev)) { ev.preventDefault(); this.moveActive(ev.key === 'ArrowRight' ? 1 : -1); }
            else if (this.isPaletteKey(ev)) { ev.preventDefault(); this.togglePalette(); }
            else if (this.isFontKey(ev)) { ev.preventDefault(); this.handleFontKey(ev); }
            else if (ev.key === 'Escape' && this.fontPopoverCloser) { ev.preventDefault(); this.toggleFontPopover(false); }
        };
        try { this.agentRecent = JSON.parse(localStorage.getItem('terminalAgentRecent') || '[]'); } catch (_) { this.agentRecent = []; }
        if (!Array.isArray(this.agentRecent)) this.agentRecent = [];
        document.addEventListener('keydown', this.keyHandler);
        // A file dropped just outside a terminal would make the browser open it and leave the app.
        this.fileDropGuard = (e) => {
            if (!this.hasDraggedFiles(e)) return;
            e.preventDefault();
            if (e.type === 'dragover' && !(e.target.closest && e.target.closest('.terminal-pane'))) e.dataTransfer.dropEffect = 'none';
        };
        document.addEventListener('dragover', this.fileDropGuard);
        document.addEventListener('drop', this.fileDropGuard);
        await this.load();
        // Who is attached where changes from other windows, so the session list is polled often
        // (sessions only; the host list comes with load()). Hidden pages skip the poll.
        this.refreshTimer = setInterval(() => { if (document.visibilityState !== 'hidden') this.loadSessions(); }, 10000);
        // Refresh the list (who is attached where) as soon as this window gets attention again,
        // instead of waiting for the next poll.
        let lastRefresh = 0;
        this.attentionHandler = () => {
            if (document.visibilityState !== 'visible' || Date.now() - lastRefresh < 3000) return;
            lastRefresh = Date.now();
            this.loadSessions();
        };
        document.addEventListener('visibilitychange', this.attentionHandler);
        window.addEventListener('focus', this.attentionHandler);
    },

    cleanup() {
        clearInterval(this.refreshTimer);
        clearTimeout(this.endedTimer);
        this.endedTimer = null;
        clearTimeout(this.fontToastTimer);
        this.toggleFontPopover(false, false);
        window.removeEventListener('resize', this.resizeHandler);
        if (this.headerObserver) { this.headerObserver.disconnect(); this.headerObserver = null; }
        document.removeEventListener('keydown', this.keyHandler);
        document.removeEventListener('dragover', this.fileDropGuard);
        document.removeEventListener('drop', this.fileDropGuard);
        document.removeEventListener('visibilitychange', this.attentionHandler);
        window.removeEventListener('focus', this.attentionHandler);
        Object.keys(this.open).forEach(id => this.closeTab(id, true));
        this.stopTitleFlash();
        this.disposeTooltips();
        ['#terminalAgentPalette', '#terminalAgentManage'].forEach(id => {
            const el = document.querySelector(id);
            const modal = el && bootstrap.Modal.getInstance(el);
            if (modal) modal.hide();
        });
        $('body').removeClass('terminal-fullscreen terminal-resizing');
        this.activeId = null;
    },

    // Layout: a dark workspace with every session as a vertical tab on the left (green dot =
    // attached in this window, click it to rename, blinking while Claude Code / Codex waits mid-task for an answer;
    // orange = attached elsewhere; grey ring = not attached; hover for details, × to detach, skull to kill)
    // and the terminal on the right. The host/name form is hidden until the + in the Sessions header is clicked.
    render() {
        $('#mainContent').html(`
        <div class="terminals-page">
            <div class="page-header terminals-header">
                <div>
                    <h2><i class="fas fa-terminal me-2"></i>Terminals</h2>
                    <p class="text-muted mb-0 small">Persistent SSH sessions that keep running after you log out. Resume them from anywhere; they end only when the remote shell exits or you kill them. Dots: green = attached in this window (click it to rename the session; blinking = Claude Code or Codex is waiting for your answer to continue), orange = attached in another window, grey ring = running but not attached. Hover a session for its details. Switch tabs with Ctrl+Shift+&uarr;/&darr;. Drag tabs (or Ctrl+Shift+&larr;/&rarr;) to reorder them. The <i class="fas fa-robot"></i> button (or Ctrl+Shift+L) launches an <strong>Agent Session</strong>: a saved host + folder + coding agent that opens ready to work. The <strong>Aa</strong> button (or Ctrl+Shift+= / - / 0) changes the font size. Paste an image or drop any file on a session to copy it to the host and paste its path, ready for Claude Code.</p>
                </div>
                <div class="terminals-new terminals-new-hidden" id="terminalNewForm">
                    <select id="terminalHost" class="form-select form-select-sm" title="SSH host"></select>
                    <input id="terminalName" class="form-control form-control-sm" placeholder="Name (optional)" maxlength="40">
                    <button class="btn btn-primary btn-sm text-nowrap" id="terminalCreateBtn" onclick="Terminals.create()"><i class="fas fa-plus me-1"></i>New Terminal</button>
                </div>
            </div>
            <div class="terminal-workspace" id="terminalWorkspace">
                <aside class="terminal-sidebar" aria-label="Terminal sessions">
                    <div class="terminal-sidebar-head">
                        <span>Sessions</span>
                        <span class="terminal-sidebar-tools">
                            <button class="terminal-tool terminal-tool-font" type="button" id="terminalFontBtn" title="Terminal font size (Ctrl+Shift+= / - / 0)" aria-label="Terminal font size" aria-haspopup="dialog" aria-controls="terminalFontPopover" aria-expanded="false" onclick="Terminals.toggleFontPopover()">Aa</button>
                            <button class="terminal-tool terminal-tool-agent" type="button" id="terminalAgentBtn" title="Launch Agent Session (Ctrl+Shift+L)" aria-label="Launch Agent Session" onclick="Terminals.openPalette()"><i class="fas fa-robot"></i></button>
                            <button class="terminal-tool terminal-tool-new" type="button" id="terminalQuickNew" title="Show/hide the New Terminal form" aria-controls="terminalNewForm" aria-expanded="false" onclick="Terminals.toggleNewForm()"><i class="fas fa-plus"></i></button>
                            <button class="terminal-tool" type="button" id="terminalFullscreenBtn" title="Toggle full screen" onclick="Terminals.toggleFullscreen()"><i class="fas fa-expand"></i></button>
                        </span>
                    </div>
                    <div class="terminal-session-list" id="terminalSessionList"><div class="terminal-session-note"><i class="fas fa-spinner fa-spin"></i></div></div>
                </aside>
                <div class="terminal-resizer" id="terminalResizer" role="separator" aria-orientation="vertical" aria-label="Resize sessions list" aria-valuemin="150" tabindex="0" title="Drag to resize · double-click to reset"></div>
                <div class="terminal-panes" id="terminalPanes">
                    <div class="terminal-empty" id="terminalEmpty">Loading sessions…</div>
                    <div class="terminal-font-toast d-none" id="terminalFontToast" role="status"></div>
                </div>
                <div class="terminal-font-popover d-none" id="terminalFontPopover" role="dialog" aria-label="Terminal font size">
                    <div class="terminal-font-title">Terminal font size</div>
                    <div class="terminal-font-stepper">
                        <button class="terminal-font-step" type="button" id="terminalFontDown" aria-label="Smaller font" onclick="Terminals.setFontSize(Terminals.fontSize - 1)"><i class="fas fa-minus"></i></button>
                        <span class="terminal-font-value" id="terminalFontValue"></span>
                        <button class="terminal-font-step" type="button" id="terminalFontUp" aria-label="Larger font" onclick="Terminals.setFontSize(Terminals.fontSize + 1)"><i class="fas fa-plus"></i></button>
                    </div>
                    <input class="form-range terminal-font-range" type="range" id="terminalFontRange" min="${this.minFontSize}" max="${this.maxFontSize}" step="1" aria-label="Font size in pixels">
                    <button class="btn btn-sm btn-outline-light w-100" type="button" id="terminalFontReset" onclick="Terminals.setFontSize(Terminals.defaultFontSize)">Reset to ${this.defaultFontSize}</button>
                </div>
            </div>
        </div>`);
        // Dragging the slider fires many events; each size change refits and resizes the remote
        // terminal, so at most one is applied per frame.
        let fontFrame = null;
        $('#terminalFontRange').on('input', (e) => {
            const value = parseInt(e.target.value, 10);
            if (fontFrame) cancelAnimationFrame(fontFrame);
            fontFrame = requestAnimationFrame(() => { fontFrame = null; this.setFontSize(value); });
        });
        // Enter in the Name field creates the terminal, like clicking New Terminal.
        $('#terminalName').on('keydown', (e) => {
            if (e.key !== 'Enter' || e.isComposing || e.originalEvent?.isComposing) return;
            e.preventDefault();
            if (!$('#terminalCreateBtn').prop('disabled')) this.create();
        });
        $('#terminalSessionList')
            .on('click', '.terminal-session', (e) => {
                const item = e.currentTarget;
                const id = item.dataset.sessionId;
                const tip = bootstrap.Tooltip.getInstance(item);
                if (tip) tip.hide();
                if ($(e.target).closest('.terminal-session-dot').length && item.classList.contains('attached')) this.rename(id);
                else if ($(e.target).closest('.terminal-session-btn.detach').length) this.closeTab(id);
                else if ($(e.target).closest('.terminal-session-btn.kill').length) this.kill(id);
                else this.selectSession(id);
            })
            .on('keydown', '.terminal-session', (e) => {
                if (e.target !== e.currentTarget || (e.key !== 'Enter' && e.key !== ' ')) return;
                e.preventDefault();
                this.selectSession(e.currentTarget.dataset.sessionId);
            });
        this.initDrag();
    },

    async load(quiet = false) {
        try {
            const [hosts, list, agents] = await Promise.all([API.terminals.hosts(), API.terminals.list(), API.terminals.agentSessions.list().catch(() => null)]);
            this.hosts = hosts.data || [];
            this.sessions = (list.data && list.data.sessions) || [];
            this.takeOrder(list.data);
            if (agents && agents.data) this.takeAgentSessions(agents.data);
            this.renderHosts();
            this.renderSessions();
        } catch (error) {
            if (!quiet) Utils.showError(error.message || 'Failed to load terminals');
        }
    },

    // Quiet refresh of the session list only (polling, focus, takeover).
    async loadSessions() {
        try {
            const list = await API.terminals.list();
            this.sessions = (list.data && list.data.sessions) || [];
            this.takeOrder(list.data);
            this.renderSessions();
        } catch (_) { /* the next poll retries */ }
    },

    renderHosts() {
        const select = $('#terminalHost');
        const current = select.val();
        if (!this.hosts.length) {
            select.html('<option value="">No SSH hosts available</option>');
            $('#terminalCreateBtn').prop('disabled', true);
            return;
        }
        select.html(this.hosts.map(h => `<option value="${this.esc(h.host_id)}">${this.esc(h.host_id)} (${this.esc(h.username)}@${this.esc(h.host)}${h.bastion_host_ref ? ` via ${this.esc(h.bastion_host_ref)}` : ''})</option>`).join(''));
        if (current && this.hosts.some(h => h.host_id === current)) select.val(current);
        $('#terminalCreateBtn').prop('disabled', false);
    },

    formatTime(value) {
        if (!value || value.startsWith('0001-')) return '—';
        return new Date(value).toLocaleString();
    },

    // When an ended session leaves the list (ms): 5 s after it ended by the daemon's clock or after this
    // page first saw it ended, whichever comes first, so a skewed browser clock can't keep it around.
    // The daemon itself keeps ended sessions for 2 minutes.
    endedExpiresAt(s) {
        const exitedAt = Date.parse(s.exited_at || '') || 0;
        const seen = this.endedSeen[s.id] || Date.now();
        return Math.min(exitedAt, seen) + this.endedLingerMs;
    },

    // Sessions in tab order (oldest first), plus any session open here that the last list did not include yet.
    // Ended sessions not open here drop out once they are due; an open one is closed by its own timer.
    orderedSessions() {
        const now = Date.now();
        // The user's own order first, then everything else (new sessions) oldest first.
        const rank = new Map(this.order.map((id, i) => [id, i]));
        const byRank = (s) => (rank.has(s.id) ? rank.get(s.id) : Infinity);
        const sessions = this.sessions.filter(s => s.status !== 'exited' || this.open[s.id] || this.endedExpiresAt(s) > now)
            .sort((a, b) => (byRank(a) - byRank(b)) || String(a.created_at || '').localeCompare(String(b.created_at || '')));
        Object.keys(this.open).forEach(id => {
            if (!sessions.some(s => s.id === id)) sessions.push(this.open[id].session || { id, name: id.slice(0, 8), host_ref: '' });
        });
        return sessions;
    },

    tabEl(id) {
        return $('#terminalSessionList .terminal-session').filter((_, el) => el.dataset.sessionId === id);
    },

    sessionStatus(s) {
        const entry = this.open[s.id];
        if (s.status === 'exited') return `Ended${s.exit_reason ? ` · ${s.exit_reason}` : ''}`;
        if (entry && entry.connected) return entry.waiting ? 'Attached in this window · agent waiting for your answer' : 'Attached in this window';
        if (entry && entry.takenOver && s.attached) return 'Taken over by another window or tab';
        if (entry) return 'Open in this window, not connected (the remote session is still running)';
        if (s.attached) return 'Attached in another window';
        return 'Running (not attached)';
    },

    // Tooltip with everything the old sessions table showed. Every value is escaped.
    sessionTooltip(s) {
        const entry = this.open[s.id];
        const rows = [
            ['Session', s.id ? s.id.slice(0, 12) : ''],
            ['Host', s.host_ref],
            ['Agent session', s.meta ? [s.meta.name, s.meta.agent || 'shell', s.meta.folder].filter(Boolean).join(' · ') : ''],
            ['Via bastion', s.bastion || ''],
            ['Login', s.username || s.host ? `${s.username || ''}@${s.host || ''}` : ''],
            Auth.isAdmin() ? ['Owner', s.owner] : null,
            ['Created', this.formatTime(s.created_at)],
            ['Last attached', this.formatTime(s.last_attached_at)],
            ['Status', this.sessionStatus(s)]
        ].filter(row => row && row[1]);
        let hint = '';
        if (s.status !== 'exited') hint = entry ? (s.id === this.activeId ? '' : 'Click to show') : 'Click to resume';
        return `<div class="terminal-tip"><div class="terminal-tip-title">${this.esc(s.name || s.host_ref || s.id)}</div>${rows.map(([label, value]) => `<div class="terminal-tip-row"><span>${this.esc(label)}</span><span>${this.esc(value)}</span></div>`).join('')}${hint ? `<div class="terminal-tip-hint">${hint}</div>` : ''}</div>`;
    },

    // Dot colour: 'here' (green, attached in this window), 'elsewhere' (orange, attached in another
    // window or tab), 'idle' (grey ring, running but not attached anywhere), '' for ended sessions.
    dotState(s) {
        if (s.status === 'exited') return '';
        const entry = this.open[s.id];
        if (entry && entry.connected) return 'here';
        // The server's flag decides, also for a tab here that was taken over: once the other
        // window lets go, the session is no longer attached anywhere.
        if (s.attached) return 'elsewhere';
        return 'idle';
    },

    // A question Claude Code / Codex asks in the middle of a task (permission, AskUserQuestion, plan
    // approval, command/edit approval) replaces the input box with a list whose selected entry is
    // "❯ 1. Yes" / "› 1. Yes, just this once" and a footer such as "Enter to select · ↑/↓ to navigate
    // · Esc to cancel" or "Press ⏎ to confirm or esc to cancel". Checked against Claude Code 2.1.312
    // and Codex 0.159.2 screens.
    agentChoice: /^[\s│]*[❯›]\s*\d+\.\s/,
    agentDialogFooter: /enter to select|to navigate|esc to cancel|to confirm or esc|enter to confirm/i,
    agentBusy: /esc to interrupt/i,
    // The idle input box: in Claude Code a "❯" line right below a full-width rule, in Codex the
    // "100% context left · ? for shortcuts" footer in the last two lines.
    agentIdleFooter: /context left|\? for shortcuts/i,
    agentRule: /^\s*[─━]{20,}\s*$/,
    agentPrompt: /^\s*❯(\s|$)/,

    // True only while Claude Code or Codex is blocked mid-task on a question for the user. The idle
    // prompt after a finished task does not count: there the input box is at the bottom of the screen.
    agentWaiting(term) {
        const buf = term.buffer.active;
        const lines = [];
        for (let y = buf.baseY; y < buf.baseY + term.rows; y++) {
            const line = buf.getLine(y);
            const text = line ? line.translateToString(true) : '';
            if (text.trim()) lines.push(text);
        }
        if (!lines.length || /[$#]\s*$/.test(lines[lines.length - 1])) return false;
        const tail = lines.slice(-15);
        if (tail.some(l => this.agentBusy.test(l))) return false;
        if (lines.slice(-2).some(l => this.agentIdleFooter.test(l))) return false;
        const bottom = lines.length - 8;
        for (let i = Math.max(1, bottom); i < lines.length; i++) {
            if (this.agentPrompt.test(lines[i]) && this.agentRule.test(lines[i - 1])) return false;
        }
        return tail.some(l => this.agentChoice.test(l) || this.agentDialogFooter.test(l));
    },

    // Throttled (not debounced) so a check still runs every 400 ms while output keeps streaming.
    scheduleWaitCheck(entry) {
        if (entry.waitTimer) return;
        entry.waitTimer = setTimeout(() => { entry.waitTimer = null; this.checkWaiting(entry); }, 400);
    },

    checkWaiting(entry) {
        if (!this.open[entry.id]) return;
        let waiting = false;
        try { waiting = this.agentWaiting(entry.term); } catch (_) { /* disposed */ }
        if (waiting === entry.waiting) return;
        entry.waiting = waiting;
        const tab = this.tabEl(entry.id).toggleClass('waiting', waiting);
        if (tab.hasClass('attached')) tab.find('.terminal-session-dot').attr('title', this.dotTitle('here', entry));
        this.updateTitleFlash();
    },

    // The window title flashes exactly while a green dot blinks, so an agent waiting for an answer
    // is noticed from another browser tab. Reads the tabs the blink CSS applies to, so the two cannot disagree.
    updateTitleFlash() {
        const waiting = $('#terminalSessionList .terminal-session.attached.waiting');
        if (!waiting.length) { this.stopTitleFlash(); return; }
        const alert = waiting.length === 1
            ? `🟢 Waiting: ${waiting.find('.terminal-session-name').text()}`
            : `🟢 ${waiting.length} agents waiting`;
        if (!this.titleFlash) this.startTitleFlash(alert);
        else if (this.titleFlash.alert !== alert) {
            this.titleFlash.alert = alert;
            if (this.titleFlash.showingAlert) document.title = alert;
        }
    },

    startTitleFlash(alert) {
        const flash = { worker: null, timer: null, base: document.title, alert, showingAlert: true };
        this.titleFlash = flash;
        document.title = alert;
        const tick = () => {
            if (this.titleFlash !== flash) return;
            flash.showingAlert = !flash.showingAlert;
            document.title = flash.showingAlert ? flash.alert : flash.base;
        };
        // A page timer is only the fallback: browsers slow it to about once a minute in a tab
        // that has been hidden for a while, which is when the flashing matters most.
        const useTimer = () => {
            if (flash.worker) { flash.worker.terminate(); flash.worker = null; }
            if (this.titleFlash === flash && !flash.timer) flash.timer = setInterval(tick, 1000);
        };
        try {
            flash.worker = new Worker('/js/title-flash-worker.js?v=1');
            flash.worker.onmessage = tick;
            flash.worker.onerror = useTimer;
        } catch (_) { useTimer(); }
    },

    // Restores the title only if it still shows the alert: when leaving the page, the router has
    // already set the next page's title.
    stopTitleFlash() {
        const flash = this.titleFlash;
        if (!flash) return;
        this.titleFlash = null;
        if (flash.worker) flash.worker.terminate();
        clearInterval(flash.timer);
        if (document.title === flash.alert) document.title = flash.base;
    },

    dotTitle(dot, entry) {
        if (dot === 'here' && entry && entry.waiting) return 'Agent is waiting for your answer · click to rename';
        return { here: 'Attached in this window · click to rename', elsewhere: 'Attached in another window or tab · opening it here takes it over', idle: 'Running, not attached anywhere' }[dot] || '';
    },

    // The cached list says nothing about this window's own detach until the next poll; clear the
    // flag so a tab we just let go of doesn't show as attached elsewhere.
    markDetached(id) {
        const session = this.sessions.find(s => s.id === id);
        if (session) session.attached = false;
    },

    disposeTooltips() {
        document.querySelectorAll('#terminalSessionList .terminal-session').forEach(el => {
            const tip = bootstrap.Tooltip.getInstance(el);
            if (tip) tip.dispose();
        });
    },

    // Remember when each ended session was first seen and re-render when the next one is due to
    // leave the list (polling every 10 s alone would keep it up to 15 s).
    trackEnded() {
        const now = Date.now();
        const ended = this.sessions.filter(s => s.status === 'exited');
        const seen = {};
        ended.forEach(s => { seen[s.id] = this.endedSeen[s.id] || now; });
        this.endedSeen = seen;
        clearTimeout(this.endedTimer);
        this.endedTimer = null;
        const due = ended.filter(s => !this.open[s.id]).map(s => this.endedExpiresAt(s)).filter(t => t > now);
        if (due.length) this.endedTimer = setTimeout(() => { this.endedTimer = null; this.renderSessions(); }, Math.min(...due) - now + 50);
    },

    // Server order from a list response, unless a move of ours is still being saved.
    takeOrder(data) {
        if (!this.orderSaving) this.order = (data && data.order) || [];
    },

    // Use ids (the full tab order) from now on, show it and save it for this user.
    applyOrder(ids) {
        const same = ids.length === this.order.length && ids.every((id, i) => id === this.order[i]);
        this.order = ids;
        this.renderSessions();
        if (same) return;
        this.orderSaving++;
        API.terminals.saveOrder(ids)
            .catch(error => Utils.showError(error.message || 'Failed to save the terminal order'))
            .finally(() => { this.orderSaving--; });
    },

    // Ctrl+Shift+Left/Right: move the active tab one place.
    moveActive(dir) {
        const ids = this.orderedSessions().map(s => s.id);
        const i = ids.indexOf(this.activeId);
        const j = i + dir;
        if (i < 0 || j < 0 || j >= ids.length) return;
        [ids[i], ids[j]] = [ids[j], ids[i]];
        this.applyOrder(ids);
    },

    // Drag a tab up or down the list. A line shows where it will land.
    initDrag() {
        const list = $('#terminalSessionList');
        const clearMarks = () => list.find('.drop-before, .drop-after').removeClass('drop-before drop-after');
        list.on('dragstart', '.terminal-session', (e) => {
            this.dragId = e.currentTarget.dataset.sessionId;
            const tip = bootstrap.Tooltip.getInstance(e.currentTarget);
            if (tip) tip.hide();
            const dt = e.originalEvent.dataTransfer;
            dt.effectAllowed = 'move';
            dt.setData('text/plain', this.dragId);
            e.currentTarget.classList.add('dragging');
        }).on('dragover', '.terminal-session', (e) => {
            if (!this.dragId) return;
            e.preventDefault();
            e.originalEvent.dataTransfer.dropEffect = 'move';
            const el = e.currentTarget;
            const rect = el.getBoundingClientRect();
            const after = e.originalEvent.clientY > rect.top + rect.height / 2;
            clearMarks();
            if (el.dataset.sessionId !== this.dragId) el.classList.add(after ? 'drop-after' : 'drop-before');
        }).on('drop', '.terminal-session', (e) => {
            if (!this.dragId) return;
            e.preventDefault();
            const el = e.currentTarget;
            const dragged = this.dragId;
            const after = el.classList.contains('drop-after');
            this.dragId = null;
            clearMarks();
            if (el.dataset.sessionId === dragged) { this.endDrag(); return; }
            const ids = this.orderedSessions().map(s => s.id).filter(id => id !== dragged);
            const at = ids.indexOf(el.dataset.sessionId);
            if (at < 0) { this.endDrag(); return; }
            ids.splice(after ? at + 1 : at, 0, dragged);
            this.renderPending = false;
            this.applyOrder(ids);
        }).on('dragend', '.terminal-session', () => {
            clearMarks();
            this.endDrag();
        });
    },

    endDrag() {
        this.dragId = null;
        $('#terminalSessionList .dragging').removeClass('dragging');
        if (this.renderPending) { this.renderPending = false; this.renderSessions(); }
    },

    renderSessions() {
        const list = $('#terminalSessionList');
        if (!list.length) return;
        // Rebuilding the list mid-drag would destroy the tab being dragged; refresh once it is dropped.
        if (this.dragId) { this.renderPending = true; return; }
        this.disposeTooltips();
        this.trackEnded();
        const sessions = this.orderedSessions();
        if (!sessions.length) {
            list.html('<div class="terminal-session-note">No sessions yet.</div>');
        } else {
            list.html(sessions.map(s => {
                const entry = this.open[s.id];
                const ended = s.status === 'exited';
                const dot = this.dotState(s);
                const classes = ['terminal-session', entry ? 'open' : '', dot === 'here' ? 'attached' : dot, dot === 'here' && entry.waiting ? 'waiting' : '', s.id === this.activeId ? 'active' : '', ended ? 'ended' : ''].filter(Boolean).join(' ');
                const dotTitle = this.dotTitle(dot, entry);
                const detach = entry ? '<button type="button" class="terminal-session-btn detach" aria-label="Detach (session keeps running)">&times;</button>' : '';
                const kill = ended ? '' : '<button type="button" class="terminal-session-btn kill" aria-label="Kill session (hangs up the remote shell)"><i class="fas fa-skull-crossbones"></i></button>';
                return `<div class="${classes}" data-session-id="${this.esc(s.id)}" role="button" tabindex="0" draggable="true">
                    ${this.sessionIconHtml(s.meta)}<span class="terminal-session-name">${this.esc(s.name || s.host_ref || s.id.slice(0, 8))}</span>
                    <span class="terminal-session-actions">${detach}${kill}</span>
                    <span class="terminal-session-dot" title="${dotTitle}"></span>
                </div>`;
            }).join(''));
            const byId = Object.fromEntries(sessions.map(s => [s.id, s]));
            list.find('.terminal-session').each((_, el) => {
                new bootstrap.Tooltip(el, { title: this.sessionTooltip(byId[el.dataset.sessionId]), html: true, placement: 'right', trigger: 'hover', container: 'body', customClass: 'terminal-tooltip', animation: false });
            });
            list.find('.terminal-session-btn.detach').attr('title', 'Detach (session keeps running)');
            list.find('.terminal-session-btn.kill').attr('title', 'Kill session (hangs up the remote shell)');
        }
        this.updateEmpty();
        this.updateTitleFlash();
    },

    updateEmpty() {
        if (Object.keys(this.open).length) { $('#terminalEmpty').hide(); return; }
        const live = this.sessions.some(s => s.status !== 'exited');
        $('#terminalEmpty').text(live ? 'Select a session on the left to resume it.' : 'No sessions yet. Click + to pick a host and open a New Terminal.').show();
    },

    // Tab click: show a session open in this window, or resume one that is not attached here.
    selectSession(id) {
        if (this.open[id]) { this.activate(id); return; }
        const session = this.sessions.find(s => s.id === id);
        if (session && session.status === 'exited') return;
        this.openSession(id);
    },

    currentSize() {
        const active = this.open[this.activeId];
        if (active) return { cols: active.term.cols, rows: active.term.rows };
        return { cols: 120, rows: 30 };
    },

    async create() {
        const hostRef = $('#terminalHost').val();
        if (!hostRef) { Utils.showError('Select an SSH host first'); return; }
        const name = $('#terminalName').val().trim();
        $('#terminalHost, #terminalName, #terminalCreateBtn').prop('disabled', true);
        $('#terminalCreateBtn').html('<i class="fas fa-spinner fa-spin me-1"></i>Connecting…');
        try {
            const id = await this.createSession({ host_ref: hostRef, name });
            $('#terminalName').val('');
            this.openSession(id);
        } catch (error) {
            Utils.showError(error.message || 'Failed to create terminal');
        } finally {
            $('#terminalHost, #terminalName, #terminalCreateBtn').prop('disabled', false);
            $('#terminalCreateBtn').html('<i class="fas fa-plus me-1"></i>New Terminal');
        }
    },

    // Creates a session with the active terminal's size (or 120x30) and refreshes the list; returns the new id.
    // fields: host_ref, name and, for Agent Sessions, meta (preset id, name, agent, folder, icon).
    async createSession(fields) {
        const size = this.currentSize();
        const response = await API.terminals.create({ ...fields, cols: size.cols, rows: size.rows });
        await this.load(true);
        return response.data.id;
    },

    // pendingInput (optional): bytes typed into the shell as soon as the replay has finished and
    // input is enabled, used by Agent Sessions for "cd '<folder>' && clear && <agent>\r".
    openSession(id, pendingInput = null) {
        if (this.open[id]) { this.activate(id); return; }
        const session = this.sessions.find(s => s.id === id) || { id, name: id.slice(0, 8), host_ref: '' };
        $('#terminalEmpty').hide();

        const pane = $(`<div class="terminal-pane"><div class="terminal-banner d-none"></div><div class="terminal-host"></div></div>`).appendTo('#terminalPanes');
        // Make this pane the visible one BEFORE xterm.js opens into it. Inactive panes are
        // display:none; a terminal opened in a hidden element measures a 0x0 font and canvas
        // and sometimes never recovers (blank screen with a blinking cursor, keystrokes shown
        // nowhere) until it is recreated.
        this.activeId = id;
        $('#terminalSessionList .terminal-session').removeClass('active');
        $('#terminalPanes .terminal-pane').removeClass('active');
        this.tabEl(id).addClass('active');
        pane.addClass('active');

        const term = new Terminal({
            cursorBlink: true,
            fontSize: this.fontSize,
            fontFamily: 'Menlo, Consolas, "DejaVu Sans Mono", "Liberation Mono", monospace',
            scrollback: 5000,
            allowProposedApi: true,
            theme: { background: '#1e1e1e' }
        });
        const fit = new FitAddon.FitAddon();
        term.loadAddon(fit);
        try { term.loadAddon(new WebLinksAddon.WebLinksAddon()); } catch (_) { /* optional */ }
        term.open(pane.find('.terminal-host')[0]);
        try { fit.fit(); } catch (_) { /* refitted in activate() */ }
        // Ctrl+Shift+Up/Down switches terminal tabs and Ctrl+Shift+Left/Right moves the active one (handled by the document keydown listener
        // in init) and must not be forwarded to the remote shell.
        term.attachCustomKeyEventHandler(ev => !this.isSwitchKey(ev) && !this.isMoveKey(ev) && !this.isPaletteKey(ev) && !this.isFontKey(ev));
        // The WebGL renderer is loaded only once the terminal is visible and sized.
        try {
            const webgl = new WebglAddon.WebglAddon();
            webgl.onContextLoss(() => { webgl.dispose(); this.repaint(term); });
            term.loadAddon(webgl);
        } catch (_) { /* fall back to the DOM renderer */ }

        const entry = { id, session, term, fit, pane, ws: null, connected: false, ended: false, replaying: false, replayTimer: null, replayBytes: 0, encoder: new TextEncoder(), waiting: false, waitTimer: null, pendingInput };
        this.open[id] = entry;
        this.bindImageInput(entry);
        term.onWriteParsed(() => this.scheduleWaitCheck(entry));

        // While the daemon replays scrollback, xterm.js answers the terminal queries it
        // contains (DA, cursor position, colours, DECRQM). Those answers must not reach the
        // remote shell: they would be typed at the prompt as "0;276;0c10;rgb:ffff/..." junk.
        term.onData(data => {
            if (entry.replaying) return;
            if (entry.ws && entry.ws.readyState === WebSocket.OPEN) entry.ws.send(entry.encoder.encode(data));
        });
        term.onBinary(data => {
            if (entry.replaying) return;
            if (!entry.ws || entry.ws.readyState !== WebSocket.OPEN) return;
            const bytes = new Uint8Array(data.length);
            for (let i = 0; i < data.length; i++) bytes[i] = data.charCodeAt(i) & 255;
            entry.ws.send(bytes);
        });
        term.onResize(({ cols, rows }) => this.sendResize(entry, cols, rows));

        this.activate(id);
        this.connect(entry);
        this.renderSessions();
    },

    // Images pasted or dropped on a session are copied to its host by POST /api/terminals/{id}/images,
    // and other dropped files by .../files. The returned path is then pasted into the session,
    // without Enter, for the agent to read.
    imageTypes: ['image/png', 'image/jpeg', 'image/gif', 'image/webp'],
    imageMaxBytes: 20 * 1024 * 1024,
    fileMaxBytes: 100 * 1024 * 1024,

    hasDraggedFiles(e) {
        return !!e.dataTransfer && Array.from(e.dataTransfer.types || []).includes('Files');
    },

    bindImageInput(entry) {
        // Capture phase: must run before xterm.js's own paste listener on its textarea.
        entry.pane.find('.terminal-host')[0].addEventListener('paste', e => this.handleImagePaste(entry, e), true);
        const pane = entry.pane[0];
        pane.addEventListener('dragover', (e) => {
            if (!this.hasDraggedFiles(e)) return;
            e.preventDefault();
            e.dataTransfer.dropEffect = 'copy';
            pane.classList.add('terminal-drop-target');
        });
        pane.addEventListener('dragleave', (e) => {
            if (!pane.contains(e.relatedTarget)) pane.classList.remove('terminal-drop-target');
        });
        pane.addEventListener('drop', (e) => {
            pane.classList.remove('terminal-drop-target');
            if (!this.hasDraggedFiles(e)) return;
            e.preventDefault();
            this.uploadFiles(entry, this.droppedFiles(e.dataTransfer), true);
        });
    },

    // A folder looks like a file with no type, and reading it would fail with a confusing network
    // error. Items can only be read during the drop event.
    droppedFiles(dataTransfer) {
        const items = Array.from(dataTransfer.items || []).filter(i => i.kind === 'file');
        if (!items.length) return Array.from(dataTransfer.files || []);
        const files = [];
        items.forEach((item) => {
            const fsEntry = item.webkitGetAsEntry ? item.webkitGetAsEntry() : null;
            if (fsEntry && fsEntry.isDirectory) {
                Utils.showError(`"${fsEntry.name}" was not uploaded: folders cannot be copied. Zip it first and drop the zip.`);
                return;
            }
            const file = item.getAsFile();
            if (file) files.push(file);
        });
        return files;
    },

    // Text wins when the clipboard holds both (Excel cells also carry a picture of the cells),
    // and only an image stops the event, so text paste behaves exactly as before.
    handleImagePaste(entry, e) {
        const data = e.clipboardData;
        if (!data || data.getData('text/plain')) return;
        const items = Array.from(data.items || []);
        const images = items.filter(i => i.kind === 'file' && i.type.startsWith('image/')).map(i => i.getAsFile()).filter(Boolean);
        if (images.length) {
            e.preventDefault();
            e.stopImmediatePropagation();
            this.uploadFiles(entry, images);
        } else if (!items.length) {
            this.readClipboardImage(entry);
        }
    },

    // "Paste as plain text" (Ctrl+Shift+V in Chrome) delivers an empty event even with an image
    // on the clipboard. Reading the clipboard directly needs HTTPS and the browser's permission.
    async readClipboardImage(entry) {
        const hint = 'Could not read an image from the clipboard here. Use right-click > Paste, or drag the file onto the terminal.';
        if (!navigator.clipboard || !navigator.clipboard.read) { Utils.showError(hint); return; }
        try {
            const images = [];
            for (const item of await navigator.clipboard.read()) {
                const type = item.types.find(t => t.startsWith('image/'));
                if (type) images.push(await item.getType(type));
            }
            if (images.length) this.uploadFiles(entry, images);
        } catch (_) {
            Utils.showError(hint);
        }
    },

    canTypeInto(entry) {
        return this.open[entry.id] === entry && !entry.ended && !entry.takenOver && !entry.replaying && !!entry.ws && entry.ws.readyState === WebSocket.OPEN;
    },

    // Keeps entry from the start, so paths reach the session the file was added to even after
    // a tab switch. One queue per session keeps paths in the order the files were added.
    // Paste accepts images only. A drop accepts anything, and what isn't an image goes to /files.
    uploadFiles(entry, files, anyType = false) {
        const label = f => f.name ? `"${this.esc(f.name)}"` : 'The image';
        const isImage = f => this.imageTypes.includes(f.type);
        const asFile = f => anyType && !isImage(f);
        const uploads = files.filter((file) => {
            if (!anyType && file.type && !isImage(file)) {
                Utils.showError(`${label(file)} was not uploaded: only PNG, JPEG, GIF and WebP images can be pasted`);
                return false;
            }
            const limit = asFile(file) ? this.fileMaxBytes : this.imageMaxBytes;
            if (file.size > limit) {
                Utils.showError(`${label(file)} was not uploaded: it is larger than the ${limit / (1024 * 1024)} MB limit`);
                return false;
            }
            if (!file.size) {
                Utils.showError(`${label(file)} was not uploaded: it is empty`);
                return false;
            }
            return true;
        });
        if (!uploads.length) return;
        if (!this.canTypeInto(entry)) {
            Utils.showError('The file was not uploaded because this session is not connected');
            return;
        }
        entry.filesUploading = (entry.filesUploading || 0) + uploads.length;
        this.showUploadIndicator(entry);
        entry.imageQueue = (entry.imageQueue || Promise.resolve()).then(async () => {
            for (const file of uploads) {
                await this.uploadFile(entry, file, label(file), asFile(file));
                entry.filesUploading--;
                this.showUploadIndicator(entry);
            }
        });
    },

    async uploadFile(entry, file, label, asFile) {
        try {
            const { data } = await (asFile ? API.terminals.uploadFile(entry.id, file) : API.terminals.uploadImage(entry.id, file));
            if (this.canTypeInto(entry)) {
                entry.term.paste(`${data.path} `);
                Utils.showSuccess(`${asFile ? 'File' : 'Image'} copied to ${data.host_ref}`);
            } else {
                // Swal's text option: shown as plain text.
                Utils.showErrorDetails(`${asFile ? 'File' : 'Image'} copied, but not pasted`, `This session is no longer connected, so the path was not typed. The ${asFile ? 'file' : 'image'} is on ${data.host_ref} at ${data.path}`);
            }
        } catch (error) {
            Utils.showError(`${label} was not uploaded: ${error.message || 'upload failed'}`);
        }
    },

    showUploadIndicator(entry) {
        let el = entry.pane.find('.terminal-upload');
        if (!entry.filesUploading) { el.remove(); return; }
        if (!el.length) el = $('<div class="terminal-upload" role="status"></div>').appendTo(entry.pane);
        el.html(`<i class="fas fa-spinner fa-spin me-2"></i>${entry.filesUploading > 1 ? `Uploading ${entry.filesUploading} files…` : 'Uploading file…'}`);
    },

    connect(entry) {
        this.banner(entry, null);
        entry.takenOver = false;
        if (entry.ws) { try { entry.ws.onclose = null; entry.ws.close(); } catch (_) { /* ignore */ } }
        const proto = location.protocol === 'https:' ? 'wss' : 'ws';
        // The JWT travels in the WebSocket subprotocol list because browsers cannot set headers here.
        const ws = new WebSocket(`${proto}://${location.host}/api/terminals/${encodeURIComponent(entry.id)}/ws`, ['cg-jwt', API.getToken()]);
        ws.binaryType = 'arraybuffer';
        entry.ws = ws;
        entry.ended = false;
        entry.replayBytes = 0;
        this.setReplaying(entry, true);

        ws.onopen = () => {
            entry.connected = true;
            this.renderSessions();
            entry.fit.fit();
            this.sendResize(entry, entry.term.cols, entry.term.rows);
            entry.term.focus();
        };
        ws.onmessage = (event) => {
            if (typeof event.data === 'string') {
                let msg = {};
                try { msg = JSON.parse(event.data); } catch (_) { return; }
                if (msg.type === 'replay_end') {
                    // Writes are processed asynchronously: wait until everything queued before
                    // this marker has been parsed (and its replies swallowed) before re-enabling input.
                    entry.term.write('', () => {
                        this.setReplaying(entry, false);
                        this.repaint(entry.term);
                        console.debug(`[terminals] ${entry.id.slice(0, 8)} resumed: replay ${entry.replayBytes} bytes, replay_end received, ${entry.term.cols}x${entry.term.rows}`);
                    });
                } else if (msg.type === 'exit') {
                    entry.ended = true;
                    entry.term.write(`\r\n\x1b[33m[Session ended: ${msg.reason || 'shell exited'}]\x1b[0m\r\n`);
                    this.banner(entry, 'This session has ended.', 'Close tab', () => this.closeTab(entry.id));
                    // The tab leaves the list on its own after a few seconds (Close tab does it at once).
                    clearTimeout(entry.endTimer);
                    entry.endTimer = setTimeout(() => this.closeTab(entry.id), this.endedLingerMs);
                    this.load(true);
                } else if (msg.type === 'takeover') {
                    entry.ended = true;
                    entry.takenOver = true;
                    this.loadSessions();
                    this.banner(entry, 'This session was resumed from another window or tab.', 'Take it back', () => this.connect(entry));
                } else if (msg.type === 'error') {
                    entry.ended = true;
                    this.banner(entry, msg.message || 'Terminal error', 'Retry', () => this.connect(entry));
                }
                return;
            }
            if (entry.replaying) {
                entry.replayBytes += event.data.byteLength;
                // Keep input disabled until 5 s after the LAST replay chunk, not the first: a
                // large replay may still be parsing (and answering terminal queries) when the
                // original timer fires, and those answers would be typed into the remote shell.
                this.setReplaying(entry, true);
            }
            entry.term.write(new Uint8Array(event.data));
        };
        ws.onclose = () => {
            if (entry.connected && !entry.takenOver) this.markDetached(entry.id);
            entry.connected = false;
            this.renderSessions();
            this.setReplaying(entry, false);
            if (!entry.ended) this.banner(entry, 'Disconnected from the server. The remote session is still running.', 'Reconnect', () => this.connect(entry));
        };
        ws.onerror = () => { /* onclose follows and shows the banner */ };
    },

    setReplaying(entry, on) {
        entry.replaying = on;
        if (entry.replayTimer) { clearTimeout(entry.replayTimer); entry.replayTimer = null; }
        // Safety net for a cg_sessiond build that predates the replay_end marker: never leave
        // the terminal without input for longer than this.
        if (on) entry.replayTimer = setTimeout(() => this.setReplaying(entry, false), 5000);
        else this.flushPendingInput(entry);
    },

    // Types the Agent Session launch command once input is enabled on a freshly created
    // session. Sent once; if the connection is not open at that moment it waits for the next connect.
    flushPendingInput(entry) {
        const input = entry.pendingInput;
        if (!input || !entry.ws || entry.ws.readyState !== WebSocket.OPEN) return; // kept for the next (re)connect
        entry.pendingInput = null;
        entry.ws.send(entry.encoder.encode(input));
    },

    sendResize(entry, cols, rows) {
        if (entry.ws && entry.ws.readyState === WebSocket.OPEN && cols > 0 && rows > 0) {
            entry.ws.send(JSON.stringify({ type: 'resize', cols, rows }));
        }
    },

    banner(entry, text, label, action) {
        const el = entry.pane.find('.terminal-banner');
        el.off('click');
        if (!text) { el.addClass('d-none').empty(); return; }
        el.removeClass('d-none').html(`<span><i class="fas fa-info-circle me-2"></i>${this.esc(text)}</span>${label ? `<button type="button" class="btn btn-sm btn-dark">${this.esc(label)}</button>` : ''}`);
        if (action) el.find('button').on('click', action);
    },

    activate(id) {
        const entry = this.open[id];
        if (!entry) return;
        this.activeId = id;
        $('#terminalSessionList .terminal-session').removeClass('active');
        $('#terminalPanes .terminal-pane').removeClass('active');
        this.tabEl(id).addClass('active');
        entry.pane.addClass('active');
        setTimeout(() => { this.fitActive(); this.repaint(entry.term); entry.term.focus(); }, 30);
    },

    // Full redraw of every row; a no-op for a healthy terminal, but it brings back a canvas
    // that went stale while its pane was hidden or after a lost WebGL context.
    repaint(term) {
        try { term.refresh(0, term.rows - 1); } catch (_) { /* disposed */ }
    },

    // Ctrl+Shift+Up/Down: switch to the previous/next tab (the tab list is vertical).
    isSwitchKey(ev) {
        return ev.type === 'keydown' && ev.ctrlKey && ev.shiftKey && !ev.altKey && !ev.metaKey
            && (ev.key === 'ArrowUp' || ev.key === 'ArrowDown');
    },

    // Ctrl+Shift+Left/Right: move the active tab up/down in the list.
    isMoveKey(ev) {
        return ev.type === 'keydown' && ev.ctrlKey && ev.shiftKey && !ev.altKey && !ev.metaKey
            && (ev.key === 'ArrowLeft' || ev.key === 'ArrowRight');
    },

    // Ctrl+Shift+L opens/closes the Agent Session palette (also while a terminal has focus).
    isPaletteKey(ev) {
        return ev.type === 'keydown' && ev.ctrlKey && ev.shiftKey && !ev.altKey && !ev.metaKey && (ev.key === 'L' || ev.key === 'l');
    },

    // Ctrl+Shift+= / - / 0: larger, smaller, default font size (also while a terminal has focus).
    // Physical key codes, because Shift and the keyboard layout change ev.key.
    isFontKey(ev) {
        return ev.type === 'keydown' && ev.ctrlKey && ev.shiftKey && !ev.altKey && !ev.metaKey
            && ['Equal', 'Minus', 'Digit0', 'NumpadAdd', 'NumpadSubtract', 'Numpad0'].includes(ev.code);
    },

    handleFontKey(ev) {
        if (ev.code === 'Equal' || ev.code === 'NumpadAdd') this.setFontSize(this.fontSize + 1);
        else if (ev.code === 'Minus' || ev.code === 'NumpadSubtract') this.setFontSize(this.fontSize - 1);
        else this.setFontSize(this.defaultFontSize);
    },

    // The saved size, or the default when it is missing or out of range.
    loadFontSize() {
        let saved = NaN;
        try { saved = parseInt(localStorage.getItem('terminalFontSize'), 10); } catch (_) { /* optional */ }
        return saved >= this.minFontSize && saved <= this.maxFontSize ? saved : this.defaultFontSize;
    },

    // Changes the size for all sessions and remembers it in this browser. Only the visible terminal
    // changes right away: fonts cannot be measured in a hidden pane, so fitActive() applies the
    // new size to the others when they are shown.
    setFontSize(px) {
        const size = Math.min(this.maxFontSize, Math.max(this.minFontSize, Math.round(px)));
        this.fontSize = size;
        try {
            if (size === this.defaultFontSize) localStorage.removeItem('terminalFontSize');
            else localStorage.setItem('terminalFontSize', String(size));
        } catch (_) { /* optional */ }
        this.fitActive();
        const entry = this.open[this.activeId];
        if (entry) this.repaint(entry.term);
        this.syncFontPopover();
        this.showFontToast();
    },

    syncFontPopover() {
        $('#terminalFontValue').text(`${this.fontSize} px`);
        $('#terminalFontRange').val(this.fontSize);
        $('#terminalFontDown').prop('disabled', this.fontSize <= this.minFontSize);
        $('#terminalFontUp').prop('disabled', this.fontSize >= this.maxFontSize);
        $('#terminalFontReset').prop('disabled', this.fontSize === this.defaultFontSize);
    },

    // Brief "16px · 142×38" note over the terminal, so the effect on columns and rows is visible.
    showFontToast() {
        const entry = this.open[this.activeId];
        const toast = $('#terminalFontToast');
        toast.text(entry ? `${this.fontSize}px · ${entry.term.cols}×${entry.term.rows}` : `${this.fontSize}px`).removeClass('d-none');
        clearTimeout(this.fontToastTimer);
        this.fontToastTimer = setTimeout(() => toast.addClass('d-none'), 1500);
    },

    // Aa in the Sessions header. The popover lives inside the workspace so it also shows in full
    // screen; it is placed under the button and kept inside the workspace, which clips its content.
    toggleFontPopover(force, refocus = true) {
        const popover = $('#terminalFontPopover');
        const show = typeof force === 'boolean' ? force : popover.hasClass('d-none');
        if (this.fontPopoverCloser) {
            document.removeEventListener('pointerdown', this.fontPopoverCloser, true);
            this.fontPopoverCloser = null;
        }
        popover.toggleClass('d-none', !show);
        $('#terminalFontBtn').toggleClass('open', show).attr('aria-expanded', String(show));
        if (!show) {
            const entry = this.open[this.activeId];
            if (refocus && entry) entry.term.focus();
            return;
        }
        this.syncFontPopover();
        const area = document.getElementById('terminalWorkspace').getBoundingClientRect();
        const btn = document.getElementById('terminalFontBtn').getBoundingClientRect();
        const left = Math.max(4, Math.min(btn.left - area.left, area.width - popover.outerWidth() - 4));
        popover.css({ left: `${left}px`, top: `${btn.bottom - area.top + 4}px` });
        // Capture phase: xterm.js stops some events on their way up. A click on the Aa button is left
        // to its own toggle, and the place clicked keeps the focus.
        this.fontPopoverCloser = (ev) => {
            if (ev.target.closest('#terminalFontPopover, #terminalFontBtn')) return;
            this.toggleFontPopover(false, false);
        };
        document.addEventListener('pointerdown', this.fontPopoverCloser, true);
        popover.find('.terminal-font-range').trigger('focus');
    },

    cycleTab(dir) {
        const ids = this.orderedSessions().map(s => s.id).filter(id => this.open[id]);
        if (ids.length < 2) return;
        const i = Math.max(0, ids.indexOf(this.activeId));
        this.activate(ids[(i + dir + ids.length) % ids.length]);
    },

    // Give the workspace all the height left below it in the window (16px = #mainContainer bottom padding).
    sizeWorkspace() {
        const workspace = document.getElementById('terminalWorkspace');
        if (!workspace) return;
        const top = workspace.getBoundingClientRect().top + window.scrollY;
        workspace.style.setProperty('--terminal-workspace-height', `${Math.floor(window.innerHeight - top - 16)}px`);
        if (this.sidebarWidth) this.setSidebarWidth(this.sidebarWidth);
        this.fitActive();
    },

    // Width of the Sessions list, kept between 150px and min(600px, workspace - 320px) so the
    // terminal always keeps room. save=true remembers it in this browser.
    setSidebarWidth(px, save = false) {
        const workspace = document.getElementById('terminalWorkspace');
        if (!workspace) return;
        const max = Math.max(150, Math.min(600, workspace.clientWidth - 320));
        const width = Math.round(Math.min(max, Math.max(150, px)));
        this.sidebarWidth = width;
        workspace.style.setProperty('--terminal-sidebar-width', `${width}px`);
        $('#terminalResizer').attr({ 'aria-valuenow': width, 'aria-valuemax': max });
        if (save) { try { localStorage.setItem('terminalSidebarWidth', String(width)); } catch (_) { /* optional */ } }
    },

    // The divider between the Sessions list and the terminal: drag (or ←/→ when focused) to
    // resize the list, double-click to go back to the default width.
    initResizer() {
        let saved = null;
        try { saved = parseInt(localStorage.getItem('terminalSidebarWidth'), 10); } catch (_) { /* optional */ }
        if (saved > 0) this.setSidebarWidth(saved);
        const resizer = document.getElementById('terminalResizer');
        const sidebar = document.querySelector('#terminalWorkspace .terminal-sidebar');
        let frame = null;
        let pending = null;
        const apply = () => { frame = null; this.setSidebarWidth(pending); this.fitActive(); };
        const finish = (ev) => {
            if (!resizer.hasPointerCapture(ev.pointerId)) return;
            resizer.releasePointerCapture(ev.pointerId);
            if (frame) { cancelAnimationFrame(frame); apply(); }
            $('body').removeClass('terminal-resizing');
            this.setSidebarWidth(this.sidebarWidth, true);
            this.fitActive();
        };
        resizer.addEventListener('pointerdown', (ev) => {
            if (ev.button !== 0) return;
            ev.preventDefault();
            resizer.setPointerCapture(ev.pointerId);
            $('body').addClass('terminal-resizing');
        });
        resizer.addEventListener('pointermove', (ev) => {
            if (!resizer.hasPointerCapture(ev.pointerId)) return;
            pending = ev.clientX - sidebar.getBoundingClientRect().left;
            if (!frame) frame = requestAnimationFrame(apply);
        });
        resizer.addEventListener('pointerup', finish);
        resizer.addEventListener('pointercancel', finish);
        resizer.addEventListener('dblclick', () => {
            this.setSidebarWidth(this.defaultSidebarWidth);
            try { localStorage.removeItem('terminalSidebarWidth'); } catch (_) { /* optional */ }
            this.fitActive();
        });
        resizer.addEventListener('keydown', (ev) => {
            if (ev.key !== 'ArrowLeft' && ev.key !== 'ArrowRight') return;
            ev.preventDefault();
            const current = this.sidebarWidth || sidebar.getBoundingClientRect().width;
            this.setSidebarWidth(current + (ev.key === 'ArrowRight' ? 16 : -16), true);
            this.fitActive();
        });
    },

    // + in the Sessions header: show or hide the host/name form. The full-screen workspace
    // covers the form, so opening it leaves full screen first.
    toggleNewForm(force) {
        const form = $('#terminalNewForm');
        const show = typeof force === 'boolean' ? force : form.hasClass('terminals-new-hidden');
        if (show && $('#terminalWorkspace').hasClass('fullscreen')) this.toggleFullscreen();
        form.toggleClass('terminals-new-hidden', !show);
        $('#terminalQuickNew').toggleClass('open', show).attr('aria-expanded', String(show));
        this.sizeWorkspace();
        if (show) $('#terminalHost').trigger('focus');
        else if (this.open[this.activeId]) this.open[this.activeId].term.focus();
    },

    fitActive() {
        const entry = this.open[this.activeId];
        if (!entry || !entry.pane.hasClass('active')) return;
        try {
            if (entry.term.options.fontSize !== this.fontSize) entry.term.options.fontSize = this.fontSize;
            entry.fit.fit();
        } catch (_) { /* pane may be hidden */ }
    },

    // Detach from a session; the remote shell keeps running.
    closeTab(id, silent = false) {
        const entry = this.open[id];
        if (!entry) return;
        if (entry.connected) this.markDetached(id);
        clearTimeout(entry.waitTimer);
        clearTimeout(entry.endTimer);
        if (entry.ws) { try { entry.ws.onclose = null; entry.ws.close(); } catch (_) { /* ignore */ } }
        try { entry.term.dispose(); } catch (_) { /* ignore */ }
        entry.pane.remove();
        delete this.open[id];
        if (this.activeId === id) {
            const remaining = Object.keys(this.open);
            this.activeId = null;
            if (remaining.length) this.activate(remaining[remaining.length - 1]);
        }
        if (!silent) { this.renderSessions(); this.load(true); }
        else this.updateEmpty();
    },

    async kill(id) {
        const session = this.sessions.find(s => s.id === id);
        const result = await Swal.fire({
            title: 'Kill this session?',
            html: `<p>${this.esc(session ? (session.name || session.host_ref) : id)}</p><p class="text-muted small mb-0">The remote shell and anything running inside it (vim, claude, builds…) receive a hangup and are terminated. Use <code>exit</code> inside the terminal to end it cleanly instead.</p>`,
            icon: 'warning',
            showCancelButton: true,
            confirmButtonColor: '#dc3545',
            confirmButtonText: 'Kill session'
        });
        if (!result.isConfirmed) return;
        try {
            await API.terminals.kill(id);
            this.closeTab(id, true);
            await this.load(true);
            Utils.showSuccess('Session killed');
        } catch (error) {
            Utils.showError(error.message || 'Failed to kill session');
        }
    },

    // Rename dialog opened from the bright dot. An empty name restores the host name.
    async rename(id) {
        const session = this.sessions.find(s => s.id === id) || (this.open[id] && this.open[id].session);
        if (!session) return;
        const current = session.name || session.host_ref || '';
        const result = await Swal.fire({
            title: 'Rename terminal',
            input: 'text',
            inputValue: current,
            inputAttributes: { maxlength: 40, 'aria-label': 'Terminal name' },
            inputPlaceholder: session.host_ref || 'Name',
            html: '<p class="text-muted small mb-0">Leave empty to use the host name.</p>',
            showCancelButton: true,
            confirmButtonText: 'Save',
            didOpen: () => { const input = Swal.getInput(); if (input) input.select(); }
        });
        if (!result.isConfirmed) {
            if (this.open[id]) this.open[id].term.focus();
            return;
        }
        const name = String(result.value || '').trim();
        if (name === current) return;
        try {
            const response = await API.terminals.rename(id, name);
            const renamed = (response.data && response.data.name) || name;
            session.name = renamed;
            if (this.open[id] && this.open[id].session) this.open[id].session.name = renamed;
            this.renderSessions();
            await this.load(true);
            Utils.showSuccess('Terminal renamed');
        } catch (error) {
            Utils.showError(error.message || 'Failed to rename terminal');
        }
        if (this.open[id]) this.open[id].term.focus();
    },

    // =====================================================================================
    // Agent Sessions: launch palette, preset manager, icons
    // =====================================================================================

    takeAgentSessions(data) {
        const tag = (list, scope) => (Array.isArray(list) ? list : []).map(p => ({ ...p, scope }));
        this.agentSessions = { shared: tag(data.shared, 'shared'), personal: tag(data.personal, 'personal'), canShare: !!data.can_share };
        if ($('#terminalAgentPalette').hasClass('show')) this.renderPalette();
        if ($('#terminalAgentManage').hasClass('show') && $('#taManageForm').hasClass('d-none')) this.renderManage();
    },

    allAgentSessions() { return [...this.agentSessions.shared, ...this.agentSessions.personal]; },

    agentPresetById(id) { return this.allAgentSessions().find(p => p.id === id) || null; },

    // Icon markup (escaped). FA class names are whitelisted by pattern before they reach a class attribute.
    iconHtml(icon) {
        if (!icon || !icon.value) return '';
        if (icon.type === 'emoji') return `<span class="terminal-emoji">${this.esc(icon.value)}</span>`;
        if (icon.type === 'fa' && /^fa-[a-z0-9-]+$/.test(icon.value)) return `<i class="fas ${icon.value}"></i>`;
        return '';
    },

    iconStyle(icon) {
        return icon && icon.type === 'fa' && /^#[0-9a-fA-F]{6}$/.test(icon.color || '') ? ` style="color:${icon.color}"` : '';
    },

    // The 14px icon in front of a tab name; nothing (no box) for sessions without preset metadata.
    sessionIconHtml(meta) {
        const html = meta && this.iconHtml(meta.icon);
        return html ? `<span class="terminal-session-icon"${this.iconStyle(meta.icon)}>${html}</span>` : '';
    },

    // Single-quote for POSIX shells: ' -> '\'' ; nothing else is special inside single quotes.
    shellQuote(value) { return `'${String(value).replace(/'/g, `'\\''`)}'`; },

    // The command typed into the new shell: cd into the folder, clear the screen, then the agent (if any).
    launchCommand(preset) {
        const run = [preset.agent, preset.args].map(v => String(v || '').trim()).filter(Boolean).join(' ');
        const q = this.shellQuote(preset.folder);
        return run ? `cd ${q} && clear && ${run}` : `cd ${q} && clear`;
    },

    // "Diary" when free, otherwise the lowest free "Diary_02", "Diary_03", ... among the running sessions.
    nextSessionName(base) {
        const escaped = base.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
        const re = new RegExp(`^${escaped}(?:_(\\d+))?$`, 'i');
        const used = new Set();
        this.sessions.filter(s => s.status !== 'exited').forEach(s => {
            const m = re.exec(s.name || '');
            if (m) used.add(m[1] ? parseInt(m[1], 10) : 1);
        });
        if (!used.has(1)) return base;
        let n = 2;
        while (used.has(n)) n++;
        return `${base}_${String(n).padStart(2, '0')}`;
    },

    rememberRecent(id) {
        this.agentRecent = [id, ...this.agentRecent.filter(x => x !== id)].slice(0, 30);
        try { localStorage.setItem('terminalAgentRecent', JSON.stringify(this.agentRecent)); } catch (_) { /* optional */ }
    },

    async launchAgentSession(preset) {
        if (!preset || this.launching) return;
        this.launching = true;
        this.closePalette();
        const name = this.nextSessionName(preset.name);
        const meta = { preset_id: preset.id, name: preset.name, agent: preset.agent || '', folder: preset.folder, icon: preset.icon || null };
        try {
            const id = await this.createSession({ host_ref: preset.host_ref, name, meta });
            this.rememberRecent(preset.id);
            this.openSession(id, `${this.launchCommand(preset)}\r`);
            Utils.showSuccess(`${name} launched`);
        } catch (error) {
            Utils.showError(error.message || 'Failed to launch Agent Session');
        } finally {
            this.launching = false;
        }
    },

    // ---- Launch palette --------------------------------------------------------------

    ensurePaletteModal() {
        if ($('#terminalAgentPalette').length) return;
        $('body').append(`
        <div class="modal fade terminal-agent-modal" id="terminalAgentPalette" tabindex="-1" aria-labelledby="taPaletteTitle" aria-hidden="true">
            <div class="modal-dialog ta-palette-dialog">
                <div class="modal-content ta-content">
                    <div class="modal-header ta-head">
                        <h5 class="modal-title ta-title" id="taPaletteTitle"><i class="fas fa-robot"></i>Launch Agent Session</h5>
                        <button type="button" class="btn-close btn-close-white" data-bs-dismiss="modal" aria-label="Close"></button>
                    </div>
                    <div class="ta-search"><i class="fas fa-search"></i><input id="taPaletteSearch" type="search" placeholder="Type to filter… name, agent, folder or host" autocomplete="off" spellcheck="false" aria-label="Filter Agent Sessions"></div>
                    <div class="ta-plist" id="taPaletteList" role="listbox" aria-label="Agent Sessions"></div>
                    <div class="modal-footer ta-foot">
                        <div class="ta-hint"><span><kbd>↑</kbd><kbd>↓</kbd> move</span><span><kbd>Enter</kbd> launch</span><span><kbd>Esc</kbd> close</span></div>
                        <div class="ta-acts">
                            <button type="button" class="btn btn-sm ta-btn" id="taPaletteManage"><i class="fas fa-gear me-1"></i>Manage</button>
                            <button type="button" class="btn btn-sm ta-btn ta-btn-new" id="taPaletteBlank"><i class="fas fa-plus me-1"></i>Blank terminal</button>
                        </div>
                    </div>
                </div>
            </div>
        </div>`);
        const modal = $('#terminalAgentPalette');
        modal.on('shown.bs.modal', () => { $('#taPaletteSearch').trigger('focus'); });
        modal.on('hidden.bs.modal', () => { if (this.open[this.activeId] && !$('#terminalAgentManage').hasClass('show')) this.open[this.activeId].term.focus(); });
        $('#taPaletteSearch').on('input', () => { this.palette.sel = 0; this.renderPalette(); });
        modal.on('keydown', (e) => {
            if (e.target.closest('.ta-foot')) return; // footer buttons keep their own Enter/Space
            if (e.key === 'ArrowDown') { e.preventDefault(); this.palette.sel = Math.min(this.palette.filtered.length - 1, this.palette.sel + 1); this.renderPalette(); }
            else if (e.key === 'ArrowUp') { e.preventDefault(); this.palette.sel = Math.max(0, this.palette.sel - 1); this.renderPalette(); }
            else if (e.key === 'Enter') { e.preventDefault(); this.launchAgentSession(this.palette.filtered[this.palette.sel]); }
        });
        $('#taPaletteList')
            .on('click', '[data-edit]', (e) => { e.stopPropagation(); const preset = this.agentPresetById(e.currentTarget.dataset.edit); this.openManage(); if (preset) this.openAgentForm(preset); })
            .on('click', '.ta-row', (e) => this.launchAgentSession(this.palette.filtered[+e.currentTarget.dataset.i]))
            .on('mousemove', '.ta-row', (e) => { const i = +e.currentTarget.dataset.i; if (i !== this.palette.sel) { this.palette.sel = i; this.markPaletteSelection(); } })
            .on('click', '#taPaletteEmptyManage', () => this.openManage(true));
        $('#taPaletteManage').on('click', () => this.openManage());
        $('#taPaletteBlank').on('click', () => { this.closePalette(); this.toggleNewForm(true); });
    },

    openPalette() {
        this.ensurePaletteModal();
        const manage = document.getElementById('terminalAgentManage');
        if (manage && manage.classList.contains('show')) bootstrap.Modal.getInstance(manage).hide();
        $('#taPaletteSearch').val('');
        this.palette.sel = 0;
        this.renderPalette();
        bootstrap.Modal.getOrCreateInstance(document.getElementById('terminalAgentPalette')).show();
        // Refresh presets in the background (another window may have changed them).
        API.terminals.agentSessions.list().then(r => { if (r && r.data) this.takeAgentSessions(r.data); }).catch(() => { /* keep the current list */ });
    },

    closePalette() {
        const el = document.getElementById('terminalAgentPalette');
        const modal = el && bootstrap.Modal.getInstance(el);
        if (modal) modal.hide();
    },

    togglePalette() {
        if ($('#terminalAgentPalette').hasClass('show')) this.closePalette();
        else this.openPalette();
    },

    // Recently launched first (this browser), then by name.
    sortedAgentSessions() {
        const rank = (p) => { const i = this.agentRecent.indexOf(p.id); return i < 0 ? Number.MAX_SAFE_INTEGER : i; };
        return this.allAgentSessions().sort((a, b) => (rank(a) - rank(b)) || a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
    },

    agentMatches(p, words) {
        const hay = [p.name, p.agent || 'shell', p.args, p.folder, p.host_ref, p.scope === 'personal' ? 'mine personal' : 'shared'].join(' ').toLowerCase();
        return words.every(w => hay.includes(w));
    },

    // Wraps every occurrence of the search words in <mark>, escaping the text piecewise so a
    // match can never land inside an HTML entity.
    highlight(text, words) {
        text = String(text == null ? '' : text);
        const lower = text.toLowerCase();
        if (!words || !words.length || lower.length !== text.length) return this.esc(text);
        const marks = new Array(text.length).fill(false);
        words.forEach(w => {
            if (!w) return;
            let i = 0;
            while ((i = lower.indexOf(w, i)) !== -1) { for (let k = i; k < i + w.length; k++) marks[k] = true; i += w.length; }
        });
        let out = '', run = '', on = false;
        for (let i = 0; i < text.length; i++) {
            if (marks[i] !== on) { out += on ? `<mark>${this.esc(run)}</mark>` : this.esc(run); run = ''; on = marks[i]; }
            run += text[i];
        }
        return out + (on ? `<mark>${this.esc(run)}</mark>` : this.esc(run));
    },

    agentBadge(p, words) {
        const agent = (p.agent || '').trim() || 'shell';
        const cls = agent === 'shell' ? 'shell' : (this.agentKnown.includes(agent) ? agent : 'custom');
        return `<span class="ta-badge ta-badge-${cls}" title="${this.esc(p.agent ? this.launchCommand(p) : 'Shell only: cd into the folder')}">${words ? this.highlight(agent, words) : this.esc(agent)}</span>`;
    },

    renderPalette() {
        const list = $('#taPaletteList');
        if (!list.length) return;
        const words = String($('#taPaletteSearch').val() || '').toLowerCase().split(/\s+/).filter(Boolean);
        this.palette.filtered = this.sortedAgentSessions().filter(p => this.agentMatches(p, words));
        if (this.palette.sel >= this.palette.filtered.length) this.palette.sel = Math.max(0, this.palette.filtered.length - 1);
        if (!this.allAgentSessions().length) {
            list.html(`<div class="ta-empty"><div>No Agent Sessions yet.</div><div class="text-muted small">Save a host, folder and coding agent once, then launch it with two keystrokes.</div><button type="button" class="btn btn-sm ta-btn-primary" id="taPaletteEmptyManage"><i class="fas fa-plus me-1"></i>Create the first one</button></div>`);
            return;
        }
        if (!this.palette.filtered.length) {
            list.html(`<div class="ta-empty">Nothing matches “${this.esc($('#taPaletteSearch').val())}”.</div>`);
            return;
        }
        const both = this.palette.filtered.some(p => p.scope === 'shared') && this.palette.filtered.some(p => p.scope === 'personal');
        let html = '';
        let lastScope = null;
        this.palette.filtered.forEach((p, i) => {
            if (both && p.scope !== lastScope) { html += `<div class="ta-group">${p.scope === 'shared' ? 'Shared' : 'Personal'}</div>`; lastScope = p.scope; }
            const editable = p.scope === 'personal' || this.agentSessions.canShare;
            html += `<div class="ta-row${i === this.palette.sel ? ' sel' : ''}" data-i="${i}" role="option" aria-selected="${i === this.palette.sel}">
                <span class="ta-icon"${this.iconStyle(p.icon)}>${this.iconHtml(p.icon)}</span>
                <span class="ta-name" title="${this.esc(p.name)}">${this.highlight(p.name, words)}</span>
                ${this.agentBadge(p, words)}
                <span class="ta-host" title="${this.esc(p.host_ref)}">${this.highlight(p.host_ref, words)}</span>
                <span class="ta-folder" title="${this.esc(p.folder)}"><span>${this.highlight(p.folder, words)}</span></span>
                <span class="ta-scope">${p.scope === 'personal' ? '<span class="ta-tag ta-tag-mine">mine</span>' : ''}</span>
                ${editable ? `<button type="button" class="ta-edit" title="Edit preset" aria-label="Edit ${this.esc(p.name)}" data-edit="${this.esc(p.id)}"><i class="fas fa-pen"></i></button>` : '<span></span>'}
            </div>`;
        });
        list.html(html);
        const sel = list.find('.ta-row.sel')[0];
        if (sel && sel.scrollIntoView) sel.scrollIntoView({ block: 'nearest' });
    },

    markPaletteSelection() {
        $('#taPaletteList .ta-row').each((_, el) => {
            const on = +el.dataset.i === this.palette.sel;
            el.classList.toggle('sel', on);
            el.setAttribute('aria-selected', String(on));
        });
    },

    // ---- Manage dialog (list + add/edit form + icon picker) ---------------------------------

    ensureManageModal() {
        if ($('#terminalAgentManage').length) return;
        $('body').append(`
        <div class="modal fade terminal-agent-modal" id="terminalAgentManage" tabindex="-1" aria-labelledby="taManageTitle" aria-hidden="true">
            <div class="modal-dialog modal-lg modal-dialog-scrollable ta-manage-dialog">
                <div class="modal-content ta-content">
                    <div class="modal-header ta-head">
                        <h5 class="modal-title ta-title" id="taManageTitle"><i class="fas fa-gear"></i><span id="taManageHeading">Agent Sessions</span></h5>
                        <button type="button" class="btn-close btn-close-white" data-bs-dismiss="modal" aria-label="Close"></button>
                    </div>
                    <div class="modal-body p-0">
                        <div id="taManageList">
                            <div class="ta-toolbar">
                                <button type="button" class="btn btn-sm ta-btn-primary" id="taAddBtn"><i class="fas fa-plus me-1"></i>Add</button>
                                <div class="ta-search ta-search-inline"><i class="fas fa-search"></i><input id="taManageSearch" type="search" placeholder="Search presets" autocomplete="off" aria-label="Search presets"></div>
                            </div>
                            <div class="ta-table-wrap"><table class="ta-table"><thead><tr><th class="ta-col-icon"></th><th class="ta-col-name">App name</th><th class="ta-col-agent">Agent</th><th class="ta-col-host">Host</th><th>Folder</th><th class="ta-col-scope">Scope</th><th class="ta-col-acts"></th></tr></thead><tbody id="taManageRows"></tbody></table></div>
                        </div>
                        <div id="taManageForm" class="d-none">
                            <div class="ta-form">
                                <label class="ta-l">Scope <b>*</b></label>
                                <div class="ta-radios" id="taScopeRow">
                                    <label><input type="radio" name="taScope" id="taScopePersonal" value="personal" checked> Personal (only me)</label>
                                    <label id="taScopeSharedLabel"><input type="radio" name="taScope" id="taScopeShared" value="shared"> Shared (everyone with the host)</label>
                                </div>
                                <div class="ta-help" id="taScopeHelp"></div>

                                <label class="ta-l" for="tafName">App name <b>*</b></label>
                                <input class="form-control form-control-sm ta-in ta-mono" id="tafName" maxlength="30" placeholder="Diary" autocomplete="off">
                                <div class="ta-help" id="tafNameHelp">Tab names: Diary, Diary_02, Diary_03 …</div>

                                <label class="ta-l" for="tafHost">Host <b>*</b></label>
                                <select class="form-select form-select-sm ta-in" id="tafHost"></select>

                                <label class="ta-l" for="tafFolder">Folder path <b>*</b></label>
                                <input class="form-control form-control-sm ta-in ta-mono" id="tafFolder" placeholder="/ssd/www/diary" spellcheck="false" autocomplete="off">
                                <div class="ta-help ta-help-err d-none" id="tafFolderHelp">Folder path is required.</div>

                                <label class="ta-l" for="tafAgent">Coding agent</label>
                                <div class="ta-row2">
                                    <select class="form-select form-select-sm ta-in" id="tafAgent">
                                        <option value="">None (shell only)</option>
                                        <option value="claude">claude</option>
                                        <option value="codex">codex</option>
                                        <option value="gemini">gemini</option>
                                        <option value="aider">aider</option>
                                        <option value="opencode">opencode</option>
                                        <option value="__custom">Custom command…</option>
                                    </select>
                                    <input class="form-control form-control-sm ta-in ta-mono d-none" id="tafAgentCustom" placeholder="my-agent" spellcheck="false" autocomplete="off" maxlength="200">
                                </div>

                                <label class="ta-l" for="tafArgs">Extra arguments</label>
                                <input class="form-control form-control-sm ta-in ta-mono" id="tafArgs" placeholder="--resume" spellcheck="false" autocomplete="off" maxlength="500">

                                <label class="ta-l">Icon</label>
                                <div><button type="button" class="ta-iconbtn" id="taIconBtn" aria-expanded="false" aria-controls="taIconPicker"><span class="ta-icon" id="taIconBtnPreview"></span><span id="taIconBtnText">Choose…</span><i class="fas fa-chevron-down ta-dim"></i></button></div>

                                <div class="ta-picker d-none" id="taIconPicker">
                                    <div class="ta-tabs"><button type="button" class="on" data-tab="fa">Icons</button><button type="button" data-tab="emoji">Emoji</button></div>
                                    <div id="taTabFa">
                                        <div class="ta-igrid" id="taFaGrid"></div>
                                        <div class="ta-swatches" id="taSwatches"></div>
                                    </div>
                                    <div id="taTabEmoji" class="d-none">
                                        <div class="ta-igrid" id="taEmojiGrid"></div>
                                        <div class="ta-efree"><span>Or paste any emoji</span><input class="form-control form-control-sm ta-in" id="taEmojiFree" maxlength="8" aria-label="Custom emoji"></div>
                                    </div>
                                </div>

                                <div class="ta-preview">
                                    <span class="ta-cap">Preview</span>
                                    <div class="ta-tabdemo"><span class="terminal-session-icon" id="taPvIcon"></span><span class="terminal-session-name" id="taPvName">Diary</span><span class="terminal-session-dot"></span></div>
                                    <span class="ta-cap">Runs after login</span>
                                    <code id="taPvCmd"></code>
                                </div>
                            </div>
                            <div class="ta-form-foot">
                                <button type="button" class="btn btn-sm ta-btn" id="taFormCancel">Cancel</button>
                                <button type="button" class="btn btn-sm ta-btn-primary" id="taFormSave"><i class="fas fa-check me-1"></i>Save</button>
                            </div>
                        </div>
                    </div>
                </div>
            </div>
        </div>`);
        const modal = $('#terminalAgentManage');
        modal.on('hidden.bs.modal', () => { if (this.open[this.activeId] && !$('#terminalAgentPalette').hasClass('show')) this.open[this.activeId].term.focus(); });
        $('#taManageSearch').on('input', () => this.renderManage());
        $('#taAddBtn').on('click', () => this.openAgentForm(null));
        $('#taManageRows').on('click', '[data-act]', async (e) => {
            const btn = e.currentTarget;
            const preset = this.agentPresetById(btn.closest('tr').dataset.id);
            if (!preset) return;
            if (btn.dataset.act === 'launch') { bootstrap.Modal.getInstance(modal[0]).hide(); this.launchAgentSession(preset); }
            else if (btn.dataset.act === 'edit') this.openAgentForm(preset);
            else if (btn.dataset.act === 'del') this.deleteAgentSession(preset);
        });
        $('#taFormCancel').on('click', () => this.showManageList());
        $('#taFormSave').on('click', () => this.saveAgentForm());
        $('#tafName, #tafFolder, #tafArgs, #tafAgentCustom').on('input', () => this.updateAgentPreview());
        $('#tafHost').on('change', () => this.updateAgentPreview());
        $('#tafAgent').on('change', () => {
            const custom = $('#tafAgent').val() === '__custom';
            $('#tafAgentCustom').toggleClass('d-none', !custom);
            if (custom) $('#tafAgentCustom').trigger('focus');
            this.updateAgentPreview();
        });
        $('#tafName, #tafFolder, #tafArgs, #tafAgentCustom').on('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); this.saveAgentForm(); } });
        $('#taIconBtn').on('click', () => {
            const picker = $('#taIconPicker');
            picker.toggleClass('d-none');
            $('#taIconBtn').attr('aria-expanded', String(!picker.hasClass('d-none')));
        });
        $('#taIconPicker')
            .on('click', '[data-tab]', (e) => { this.agentForm.pickerTab = e.currentTarget.dataset.tab; this.renderIconPicker(); })
            .on('click', '[data-fa]', (e) => { const keep = this.agentForm.icon && this.agentForm.icon.type === 'fa' ? this.agentForm.icon.color : this.agentColors[0]; this.agentForm.icon = { type: 'fa', value: e.currentTarget.dataset.fa, color: keep }; this.renderIconPicker(); this.updateAgentPreview(); })
            .on('click', '[data-color]', (e) => { if (!this.agentForm.icon || this.agentForm.icon.type !== 'fa') this.agentForm.icon = { type: 'fa', value: 'fa-book' }; this.agentForm.icon.color = e.currentTarget.dataset.color; this.renderIconPicker(); this.updateAgentPreview(); })
            .on('click', '[data-emoji]', (e) => { this.agentForm.icon = { type: 'emoji', value: e.currentTarget.dataset.emoji }; this.renderIconPicker(); this.updateAgentPreview(); });
        $('#taEmojiFree').on('input', () => {
            const v = String($('#taEmojiFree').val() || '').trim();
            if (!v) return;
            let g = v;
            try { g = Array.from(new Intl.Segmenter().segment(v))[0].segment; } catch (_) { g = [...v][0]; }
            this.agentForm.icon = { type: 'emoji', value: g };
            this.renderIconPicker();
            this.updateAgentPreview();
        });
    },

    openManage(startWithForm = false) {
        this.ensureManageModal();
        this.closePalette();
        this.showManageList();
        bootstrap.Modal.getOrCreateInstance(document.getElementById('terminalAgentManage')).show();
        if (startWithForm) this.openAgentForm(null);
    },

    showManageList() {
        $('#taManageForm').addClass('d-none');
        $('#taManageList').removeClass('d-none');
        $('.ta-manage-dialog').addClass('ta-manage-wide');
        $('#taManageHeading').text('Agent Sessions');
        this.agentForm.editing = null;
        this.renderManage();
    },

    renderManage() {
        const rows = $('#taManageRows');
        if (!rows.length) return;
        const words = String($('#taManageSearch').val() || '').toLowerCase().split(/\s+/).filter(Boolean);
        const presets = this.allAgentSessions()
            .filter(p => this.agentMatches(p, words))
            .sort((a, b) => a.scope.localeCompare(b.scope) || a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
        if (!presets.length) {
            rows.html(`<tr><td colspan="7" class="ta-empty">${this.allAgentSessions().length ? 'No presets match.' : 'No Agent Sessions yet. Click Add to save your first one.'}</td></tr>`);
            return;
        }
        rows.html(presets.map(p => {
            const editable = p.scope === 'personal' || this.agentSessions.canShare;
            return `<tr data-id="${this.esc(p.id)}">
                <td><span class="ta-icon"${this.iconStyle(p.icon)}>${this.iconHtml(p.icon)}</span></td>
                <td class="ta-mono" title="${this.esc(p.name)}">${this.esc(p.name)}</td>
                <td>${this.agentBadge(p)}${p.args ? `<span class="ta-args ta-mono" title="${this.esc(p.args)}">${this.esc(p.args)}</span>` : ''}</td>
                <td class="ta-col-host" title="${this.esc(p.host_ref)}">${this.esc(p.host_ref)}</td>
                <td class="ta-folder-cell ta-mono" title="${this.esc(p.folder)}">${this.esc(p.folder)}</td>
                <td class="ta-col-scope"><span class="ta-tag${p.scope === 'personal' ? ' ta-tag-mine' : ''}">${p.scope === 'personal' ? 'mine' : 'shared'}</span></td>
                <td class="ta-acts-cell">
                    <button type="button" class="ta-ibtn" data-act="launch" title="Launch" aria-label="Launch ${this.esc(p.name)}"><i class="fas fa-play"></i></button>
                    ${editable ? `<button type="button" class="ta-ibtn" data-act="edit" title="Edit" aria-label="Edit ${this.esc(p.name)}"><i class="fas fa-pen"></i></button>
                    <button type="button" class="ta-ibtn ta-ibtn-del" data-act="del" title="Delete" aria-label="Delete ${this.esc(p.name)}"><i class="fas fa-trash"></i></button>` : ''}
                </td>
            </tr>`;
        }).join(''));
    },

    openAgentForm(preset) {
        this.ensureManageModal();
        this.agentForm.editing = preset || null;
        $('#taManageList').addClass('d-none');
        $('#taManageForm').removeClass('d-none');
        $('.ta-manage-dialog').removeClass('ta-manage-wide'); // the form keeps the narrower width
        $('#taManageHeading').text(preset ? `Edit “${preset.name}”` : 'New Agent Session');
        // Scope: admin may share; everyone else only has personal presets. Scope cannot change on edit.
        const canShare = this.agentSessions.canShare;
        $('#taScopeSharedLabel').toggleClass('d-none', !canShare);
        const scope = preset ? preset.scope : 'personal';
        $(scope === 'shared' ? '#taScopeShared' : '#taScopePersonal').prop('checked', true);
        $('#taScopePersonal, #taScopeShared').prop('disabled', !!preset);
        $('#taScopeHelp').text(preset ? 'The scope of an existing preset cannot be changed; create a new one instead.' : (canShare ? 'Shared presets are stored in config.json and offered to every user who may open terminals on the host.' : ''));
        $('#tafName').val(preset ? preset.name : '');
        const hostSelect = $('#tafHost');
        hostSelect.html(this.hosts.map(h => `<option value="${this.esc(h.host_id)}">${this.esc(h.host_id)} (${this.esc(h.username)}@${this.esc(h.host)}${h.bastion_host_ref ? ` via ${this.esc(h.bastion_host_ref)}` : ''})</option>`).join(''));
        if (preset && !this.hosts.some(h => h.host_id === preset.host_ref)) hostSelect.append(`<option value="${this.esc(preset.host_ref)}">${this.esc(preset.host_ref)}</option>`);
        hostSelect.val(preset ? preset.host_ref : (this.hosts[0] ? this.hosts[0].host_id : ''));
        $('#tafFolder').val(preset ? preset.folder : '');
        $('#tafFolderHelp').addClass('d-none');
        const agent = preset ? (preset.agent || '') : 'claude';
        if (!agent || this.agentKnown.includes(agent)) { $('#tafAgent').val(agent); $('#tafAgentCustom').addClass('d-none').val(''); }
        else { $('#tafAgent').val('__custom'); $('#tafAgentCustom').removeClass('d-none').val(agent); }
        $('#tafArgs').val(preset ? (preset.args || '') : '');
        this.agentForm.icon = preset && preset.icon ? { ...preset.icon } : { type: 'fa', value: 'fa-book', color: this.agentColors[0] };
        this.agentForm.pickerTab = this.agentForm.icon.type === 'emoji' ? 'emoji' : 'fa';
        $('#taIconPicker').addClass('d-none');
        $('#taIconBtn').attr('aria-expanded', 'false');
        $('#taEmojiFree').val('');
        $('#tafNameHelp').removeClass('ta-help-err');
        this.renderIconPicker();
        this.updateAgentPreview();
        setTimeout(() => $('#tafName').trigger('focus'), 50);
    },

    agentFormValue() {
        return $('#tafAgent').val() === '__custom' ? String($('#tafAgentCustom').val() || '').trim() : $('#tafAgent').val();
    },

    updateAgentPreview() {
        const name = String($('#tafName').val() || '').trim() || 'Diary';
        $('#tafNameHelp').removeClass('ta-help-err').text(`Tab names: ${name}, ${name}_02, ${name}_03 …`);
        $('#taPvName').text(name);
        const icon = this.agentForm.icon;
        $('#taPvIcon, #taIconBtnPreview').html(this.iconHtml(icon)).css('color', icon && icon.type === 'fa' ? icon.color : '');
        $('#taIconBtnText').text(icon && icon.type === 'fa' ? icon.value.replace(/^fa-/, '') : 'emoji');
        const host = this.hosts.find(h => h.host_id === $('#tafHost').val());
        const cmd = this.launchCommand({ folder: String($('#tafFolder').val() || '').trim() || '/path/to/app', agent: this.agentFormValue(), args: String($('#tafArgs').val() || '').trim() });
        $('#taPvCmd').html(`<span class="ta-prompt">${this.esc(host ? host.username : 'user')}@${this.esc($('#tafHost').val() || 'host')}</span>:~$ ${this.esc(cmd)}`);
    },

    renderIconPicker() {
        const icon = this.agentForm.icon || {};
        const tab = this.agentForm.pickerTab;
        $('#taIconPicker .ta-tabs button').each((_, b) => b.classList.toggle('on', b.dataset.tab === tab));
        $('#taTabFa').toggleClass('d-none', tab !== 'fa');
        $('#taTabEmoji').toggleClass('d-none', tab !== 'emoji');
        const faColor = icon.type === 'fa' && icon.color ? icon.color : '#e6e6e6';
        $('#taFaGrid').html(this.agentIcons.map(ic => `<button type="button" data-fa="${ic}" class="${icon.type === 'fa' && icon.value === ic ? 'on' : ''}" title="${ic.replace(/^fa-/, '')}" style="color:${faColor}"><i class="fas ${ic}"></i></button>`).join(''));
        $('#taSwatches').html('<span>Colour</span>' + this.agentColors.map(c => `<button type="button" data-color="${c}" class="${icon.type === 'fa' && icon.color === c ? 'on' : ''}" style="background:${c}" title="${c}" aria-label="Colour ${c}"></button>`).join(''));
        $('#taEmojiGrid').html(this.agentEmoji.map(e => `<button type="button" class="ta-emoji${icon.type === 'emoji' && icon.value === e ? ' on' : ''}" data-emoji="${e}">${e}</button>`).join(''));
    },

    async saveAgentForm() {
        const name = String($('#tafName').val() || '').trim();
        const folder = String($('#tafFolder').val() || '').trim();
        if (!name) { $('#tafNameHelp').addClass('ta-help-err').text('App name is required (max 30 characters).'); $('#tafName').trigger('focus'); return; }
        if (!folder) { $('#tafFolderHelp').removeClass('d-none'); $('#tafFolder').trigger('focus'); return; }
        const editing = this.agentForm.editing;
        const scope = editing ? editing.scope : ($('#taScopeShared').is(':checked') && this.agentSessions.canShare ? 'shared' : 'personal');
        const data = { scope, name, host_ref: $('#tafHost').val(), folder, agent: this.agentFormValue(), args: String($('#tafArgs').val() || '').trim(), icon: this.agentForm.icon };
        $('#taFormSave').prop('disabled', true);
        try {
            if (editing) await API.terminals.agentSessions.update(editing.id, data);
            else await API.terminals.agentSessions.create(data);
            const list = await API.terminals.agentSessions.list();
            this.takeAgentSessions(list.data || {});
            Utils.showSuccess(editing ? `${name} updated` : `${name} added`);
            this.showManageList();
        } catch (error) {
            Utils.showError(error.message || 'Failed to save Agent Session');
        } finally {
            $('#taFormSave').prop('disabled', false);
        }
    },

    async deleteAgentSession(preset) {
        const ok = await Utils.confirm('Delete Agent Session?', `"${preset.name}" (${preset.scope === 'shared' ? 'shared' : 'personal'}) will be removed. Terminals already running from it keep their icon.`, 'Delete');
        if (!ok) return;
        try {
            await API.terminals.agentSessions.remove(preset.id, preset.scope);
            const list = await API.terminals.agentSessions.list();
            this.takeAgentSessions(list.data || {});
            this.renderManage();
            Utils.showSuccess(`${preset.name} deleted`);
        } catch (error) {
            Utils.showError(error.message || 'Failed to delete Agent Session');
        }
    },

    toggleFullscreen() {
        $('#terminalWorkspace').toggleClass('fullscreen');
        $('body').toggleClass('terminal-fullscreen');
        const icon = $('#terminalFullscreenBtn i');
        icon.toggleClass('fa-expand', !$('#terminalWorkspace').hasClass('fullscreen')).toggleClass('fa-compress', $('#terminalWorkspace').hasClass('fullscreen'));
        setTimeout(() => this.fitActive(), 60);
    }
};
window.Terminals = Terminals;
