// Remote Desktops viewer (ES module), shared by the Remote Desktops page and the
// pop-out window (vnc.html). It asks the server for a one-time ticket, then lets
// noVNC speak RFB over /api/vnc/ws. The server has already authenticated to the
// VNC server, so noVNC sees security type "None" and never gets a password.
// View-only and "no clipboard" are enforced by the server; the UI just matches it.
import RFB from './novnc-1.7.0/core/rfb.js';

const THUMB_WIDTH = 360;
const THUMB_EVERY_MS = 5 * 60 * 1000;
const PING_EVERY_MS = 60 * 1000;
const RETRY_DELAYS = [2, 4, 8, 15, 30, 30];
// End reasons (vnc_sessions.go) after which reconnecting automatically makes sense
const TRANSIENT_REASONS = ['The viewer connection was lost', 'The viewer connection was closed', 'The VNC server closed the connection', 'The viewer could not open its connection'];

const KEYS = {
    ctrlAltDel: { label: 'Ctrl+Alt+Del' },
    win: { label: 'Windows / Command key', seq: [[0xffeb, 'MetaLeft']] },
    altTab: { label: 'Alt+Tab', chord: [[0xffe9, 'AltLeft'], [0xff09, 'Tab']] },
    ctrlEsc: { label: 'Ctrl+Esc', chord: [[0xffe3, 'ControlLeft'], [0xff1b, 'Escape']] },
    altF4: { label: 'Alt+F4', chord: [[0xffe9, 'AltLeft'], [0xffc1, 'F4']] },
    esc: { label: 'Esc', seq: [[0xff1b, 'Escape']] },
    printScreen: { label: 'Print Screen', seq: [[0xff61, 'PrintScreen']] }
};

const QUALITY = { low: { quality: 2, compression: 6, label: 'Low (slow networks)' }, medium: { quality: 6, compression: 2, label: 'Medium' }, high: { quality: 9, compression: 0, label: 'High (LAN)' } };

export function esc(value) {
    return String(value == null ? '' : value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

export class ApiError extends Error {
    constructor(message, status) { super(message); this.status = status; }
}

// Minimal API client (the pop-out window does not load api.js)
export async function api(path, options = {}) {
    const token = localStorage.getItem('auth_token');
    const headers = { Accept: 'application/json', ...(options.body && !(options.body instanceof Blob) ? { 'Content-Type': 'application/json' } : {}), ...(options.headers || {}) };
    if (token) headers.Authorization = `Bearer ${token}`;
    let response;
    try {
        response = await fetch(`/api${path}`, { ...options, headers });
    } catch (error) {
        throw new ApiError('The server could not be reached', 0);
    }
    let data = {};
    try { data = await response.json(); } catch (_) { /* empty body */ }
    if (!response.ok) throw new ApiError(data.error || `HTTP ${response.status}`, response.status);
    return data;
}

function prefs() {
    try { return JSON.parse(localStorage.getItem('vncViewerPrefs') || '{}') || {}; } catch (_) { return {}; }
}
function savePrefs(update) {
    try { localStorage.setItem('vncViewerPrefs', JSON.stringify({ ...prefs(), ...update })); } catch (_) { /* optional */ }
}

export class VncViewer {
    // options: { vncId, name, level ('control'|'view'), viewOnly, clipboard, canPopout,
    //            onPopout(viewer), onClose(viewer), onState(viewer, state), onAuthLost() }
    constructor(container, options) {
        this.container = container;
        this.opts = options;
        this.vncId = options.vncId;
        this.name = options.name || options.vncId;
        this.level = options.level === 'control' ? 'control' : 'view';
        this.viewOnly = this.level === 'view' || !!options.viewOnly;
        this.clipboardAllowed = options.clipboard !== false && !this.viewOnly;
        this.sessionId = null;
        this.rfb = null;
        this.state = 'idle';
        this.closing = false;
        this.retry = 0;
        this.retryTimer = null;
        this.countdownTimer = null;
        this.activity = false;
        const p = prefs();
        this.scaled = p.scaled !== false;
        this.quality = QUALITY[p.quality] ? p.quality : 'medium';
        this.build();
        this.pingTimer = setInterval(() => this.sendPing(), PING_EVERY_MS);
        this.thumbTimer = setInterval(() => this.captureThumbnail(), THUMB_EVERY_MS);
        this.fsHandler = () => this.syncFullscreenButton();
        document.addEventListener('fullscreenchange', this.fsHandler);
    }

    build() {
        const control = !this.viewOnly;
        const keyItems = Object.entries(KEYS).map(([k, v]) => `<li><button class="dropdown-item" type="button" data-key="${k}">${esc(v.label)}</button></li>`).join('');
        const qualityItems = Object.entries(QUALITY).map(([k, v]) => `<li><button class="dropdown-item rd-quality-item" type="button" data-quality="${k}">${esc(v.label)}</button></li>`).join('');
        this.container.innerHTML = `
        <div class="rd-viewer">
            <div class="rd-toolbar">
                <span class="rd-status-dot" aria-hidden="true"></span>
                <span class="rd-title">${esc(this.name)}</span>
                ${this.viewOnly ? '<span class="badge rd-badge-view" title="Keyboard, mouse and clipboard input are not sent">View only</span>' : ''}
                <span class="rd-status-text" aria-live="polite"></span>
                <span class="rd-viewers" title="Others connected to this desktop"></span>
                <span class="rd-toolbar-spacer"></span>
                ${control ? `<div class="dropdown">
                    <button class="rd-tool" type="button" data-bs-toggle="dropdown" aria-expanded="false" title="Send special keys"><i class="fas fa-keyboard"></i></button>
                    <ul class="dropdown-menu dropdown-menu-end rd-menu">${keyItems}</ul>
                </div>` : ''}
                ${this.clipboardAllowed ? '<button class="rd-tool rd-clip-toggle" type="button" title="Clipboard"><i class="fas fa-clipboard"></i></button>' : ''}
                <div class="dropdown">
                    <button class="rd-tool" type="button" data-bs-toggle="dropdown" aria-expanded="false" title="Picture quality"><i class="fas fa-sliders-h"></i></button>
                    <ul class="dropdown-menu dropdown-menu-end rd-menu">${qualityItems}</ul>
                </div>
                <button class="rd-tool rd-scale-toggle" type="button"></button>
                <button class="rd-tool rd-fullscreen" type="button" title="Full screen"><i class="fas fa-expand"></i></button>
                ${this.opts.canPopout ? '<button class="rd-tool rd-popout" type="button" title="Open in its own window"><i class="fas fa-up-right-from-square"></i></button>' : ''}
                <button class="rd-tool rd-close" type="button" title="Disconnect (the desktop keeps running)"><i class="fas fa-times"></i></button>
            </div>
            <div class="rd-clipboard d-none">
                <textarea class="form-control form-control-sm rd-clip-text" rows="3" placeholder="Text copied on the remote desktop appears here. Type or paste text and press Send to put it on the remote clipboard."></textarea>
                <div class="rd-clip-actions">
                    <button class="btn btn-sm btn-primary rd-clip-send" type="button"><i class="fas fa-arrow-right me-1"></i>Send to remote</button>
                    <button class="btn btn-sm btn-outline-light rd-clip-paste" type="button" title="Read your computer's clipboard and send it"><i class="fas fa-paste me-1"></i>Send my clipboard</button>
                    <button class="btn btn-sm btn-outline-light rd-clip-copy" type="button" title="Copy the text above to your computer's clipboard"><i class="fas fa-copy me-1"></i>Copy here</button>
                </div>
            </div>
            <div class="rd-screen ${this.scaled ? 'scaled' : 'native'}"></div>
            <div class="rd-overlay d-none" role="status">
                <div class="rd-overlay-box">
                    <div class="rd-overlay-text"></div>
                    <div class="rd-overlay-actions">
                        <button class="btn btn-sm btn-primary rd-reconnect" type="button"><i class="fas fa-rotate-right me-1"></i>Reconnect</button>
                        <button class="btn btn-sm btn-outline-light rd-overlay-close" type="button">Close</button>
                    </div>
                </div>
            </div>
        </div>`;
        const $ = sel => this.container.querySelector(sel);
        this.el = {
            root: $('.rd-viewer'), screen: $('.rd-screen'), overlay: $('.rd-overlay'), overlayText: $('.rd-overlay-text'),
            status: $('.rd-status-text'), viewers: $('.rd-viewers'), scale: $('.rd-scale-toggle'), fullscreen: $('.rd-fullscreen'),
            clip: $('.rd-clipboard'), clipText: $('.rd-clip-text'), reconnect: $('.rd-reconnect')
        };
        this.syncScaleButton();
        this.syncQualityMenu();
        this.container.addEventListener('click', (e) => this.onClick(e));
        ['pointermove', 'pointerdown', 'keydown', 'wheel', 'touchstart'].forEach(type => this.el.screen.addEventListener(type, () => { this.activity = true; }, { passive: true, capture: true }));
    }

    onClick(e) {
        const target = e.target.closest('button');
        if (!target || !this.container.contains(target)) return;
        if (target.dataset.key) this.sendKeys(target.dataset.key);
        else if (target.dataset.quality) this.setQuality(target.dataset.quality);
        else if (target.classList.contains('rd-scale-toggle')) this.toggleScale();
        else if (target.classList.contains('rd-fullscreen')) this.toggleFullscreen();
        else if (target.classList.contains('rd-popout')) this.opts.onPopout && this.opts.onPopout(this);
        else if (target.classList.contains('rd-close') || target.classList.contains('rd-overlay-close')) this.opts.onClose ? this.opts.onClose(this) : this.destroy();
        else if (target.classList.contains('rd-reconnect')) { this.retry = 0; this.connect(); }
        else if (target.classList.contains('rd-clip-toggle')) { this.el.clip.classList.toggle('d-none'); target.classList.toggle('active'); }
        else if (target.classList.contains('rd-clip-send')) this.sendClipboard(this.el.clipText.value);
        else if (target.classList.contains('rd-clip-paste')) this.pasteLocalClipboard();
        else if (target.classList.contains('rd-clip-copy')) this.copyToLocal(this.el.clipText.value);
    }

    setState(state, text) {
        this.state = state;
        this.el.root.dataset.state = state;
        this.el.status.textContent = text || '';
        if (this.opts.onState) this.opts.onState(this, state);
    }

    showOverlay(text, { reconnect = true } = {}) {
        this.el.overlayText.textContent = text;
        this.el.reconnect.classList.toggle('d-none', !reconnect);
        this.el.overlay.classList.remove('d-none');
    }

    hideOverlay() { this.el.overlay.classList.add('d-none'); }

    async connect() {
        clearTimeout(this.retryTimer);
        clearInterval(this.countdownTimer);
        if (this.closing) return;
        this.hideOverlay();
        this.setState('connecting', 'Connecting…');
        let ticket;
        try {
            ticket = (await api(`/vnc/connections/${encodeURIComponent(this.vncId)}/sessions`, { method: 'POST', body: JSON.stringify({ view_only: this.viewOnly }) })).data;
        } catch (error) {
            if (error.status === 401) { this.handleAuthLost(); return; }
            // 502 = the VNC server could not be reached or refused us: worth retrying a few times only if it is a network problem
            const transient = error.status === 0 || error.status === 502 && /timed out|cannot connect|could not reach|closed the connection/i.test(error.message);
            this.ended(error.message, transient);
            return;
        }
        if (this.closing) { api(`/vnc/sessions/${ticket.session_id}`, { method: 'DELETE' }).catch(() => {}); return; }
        this.sessionId = ticket.session_id;
        this.clipboardAllowed = !!ticket.clipboard;
        const url = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}${ticket.ws_path}`;
        this.el.screen.innerHTML = '';
        const rfb = new RFB(this.el.screen, url, { shared: true });
        this.rfb = rfb;
        rfb.viewOnly = this.viewOnly;
        rfb.scaleViewport = this.scaled;
        rfb.clipViewport = false;
        rfb.resizeSession = false;
        rfb.focusOnClick = !this.viewOnly;
        rfb.background = '#1e1e1e';
        this.hookCursor(rfb);
        this.applyQuality();
        rfb.addEventListener('connect', () => {
            if (rfb !== this.rfb) return;
            this.retry = 0;
            this.setState('connected', ticket.desktop_name && ticket.desktop_name !== this.name ? ticket.desktop_name : '');
            setTimeout(() => this.captureThumbnail(), 5000);
            if (!this.viewOnly) rfb.focus();
        });
        rfb.addEventListener('disconnect', (e) => { if (rfb === this.rfb) this.onDisconnect(e.detail && e.detail.clean); });
        rfb.addEventListener('clipboard', (e) => this.onRemoteClipboard(e.detail.text));
        rfb.addEventListener('desktopname', (e) => { if (this.state === 'connected' && e.detail.name !== this.name) this.el.status.textContent = e.detail.name; });
    }

    // noVNC hides the local pointer and draws the cursor shape the VNC server sends.
    // Some servers (macOS Screen Sharing) never send one and do not draw it into the
    // picture either, so no cursor is visible at all. While the remote cursor is
    // empty or fully transparent, show the local arrow instead (.rd-local-cursor in
    // app.css). This hooks noVNC 1.7.0's private _updateCursor (vendored, pinned by
    // folder name); re-check it when upgrading noVNC.
    hookCursor(rfb) {
        const screen = this.el.screen;
        const sync = (rgba) => {
            let visible = false;
            if (rgba) {
                for (let i = 3; i < rgba.length; i += 4) {
                    if (rgba[i]) { visible = true; break; }
                }
            }
            screen.classList.toggle('rd-local-cursor', !visible);
        };
        sync(null); // no shape received yet
        if (typeof rfb._updateCursor !== 'function') return;
        const original = rfb._updateCursor.bind(rfb);
        rfb._updateCursor = (rgba, ...rest) => {
            sync(rgba);
            return original(rgba, ...rest);
        };
    }

    async onDisconnect() {
        const sid = this.sessionId;
        this.rfb = null;
        if (this.closing) return;
        // Ask the server why (it keeps ended sessions for a while). No answer usually means
        // cguard restarted or the network dropped: reconnect.
        let reason = '';
        try {
            const session = (await api(`/vnc/sessions/${encodeURIComponent(sid)}`)).data;
            reason = session.end_reason || '';
        } catch (error) {
            if (error.status === 401) { this.handleAuthLost(); return; }
        }
        this.ended(reason || 'The connection was lost', !reason || TRANSIENT_REASONS.includes(reason));
    }

    ended(reason, transient) {
        if (this.closing) return;
        if (transient && this.retry < RETRY_DELAYS.length) {
            let left = RETRY_DELAYS[this.retry++];
            this.setState('reconnecting', 'Reconnecting…');
            this.showOverlay(`${reason}. Reconnecting in ${left}s…`);
            this.countdownTimer = setInterval(() => {
                left -= 1;
                if (left > 0) this.el.overlayText.textContent = `${reason}. Reconnecting in ${left}s…`;
            }, 1000);
            this.retryTimer = setTimeout(() => this.connect(), left * 1000);
            return;
        }
        this.setState('ended', 'Disconnected');
        this.showOverlay(reason);
    }

    handleAuthLost() {
        this.setState('ended', 'Signed out');
        this.showOverlay('Your sign-in has expired. Sign in again to reconnect.', { reconnect: false });
        if (this.opts.onAuthLost) this.opts.onAuthLost();
    }

    setViewers(list) {
        const others = (list || []).filter(v => !v.mine);
        const names = [...new Set(others.map(v => v.username + (v.level === 'view' ? ' (view)' : '')))];
        this.el.viewers.textContent = names.length ? `Also connected: ${names.join(', ')}` : '';
        this.el.viewers.classList.toggle('d-none', !names.length);
    }

    sendKeys(name) {
        if (!this.rfb || this.viewOnly) return;
        const k = KEYS[name];
        if (name === 'ctrlAltDel') this.rfb.sendCtrlAltDel();
        else if (k.seq) k.seq.forEach(([sym, code]) => this.rfb.sendKey(sym, code));
        else if (k.chord) {
            k.chord.forEach(([sym, code]) => this.rfb.sendKey(sym, code, true));
            k.chord.slice().reverse().forEach(([sym, code]) => this.rfb.sendKey(sym, code, false));
        }
        this.activity = true;
        this.rfb.focus();
    }

    sendClipboard(text) {
        if (!this.rfb || !this.clipboardAllowed || !text) return;
        this.rfb.clipboardPasteFrom(text);
        this.activity = true;
        this.flashStatus('Sent to the remote clipboard');
    }

    async pasteLocalClipboard() {
        try {
            const text = await navigator.clipboard.readText();
            if (!text) { this.flashStatus('Your clipboard is empty'); return; }
            this.el.clipText.value = text;
            this.sendClipboard(text);
        } catch (_) {
            this.flashStatus('The browser did not allow reading your clipboard; paste into the box instead');
        }
    }

    async copyToLocal(text) {
        try { await navigator.clipboard.writeText(text || ''); this.flashStatus('Copied to your clipboard'); } catch (_) { this.flashStatus('The browser did not allow writing to your clipboard'); }
    }

    onRemoteClipboard(text) {
        if (!this.clipboardAllowed) return; // the administrator turned the clipboard off for this desktop
        this.el.clipText.value = text;
        if (document.hasFocus()) navigator.clipboard?.writeText(text).catch(() => {});
    }

    flashStatus(text) {
        if (!this.flashTimer) this.statusBeforeFlash = this.el.status.textContent;
        this.el.status.textContent = text;
        clearTimeout(this.flashTimer);
        this.flashTimer = setTimeout(() => {
            this.flashTimer = null;
            if (this.el.status.textContent === text) this.el.status.textContent = this.statusBeforeFlash || '';
        }, 2500);
    }

    toggleScale() {
        this.scaled = !this.scaled;
        savePrefs({ scaled: this.scaled });
        if (this.rfb) this.rfb.scaleViewport = this.scaled;
        this.el.screen.classList.toggle('scaled', this.scaled);
        this.el.screen.classList.toggle('native', !this.scaled);
        this.syncScaleButton();
    }

    syncScaleButton() {
        this.el.scale.innerHTML = this.scaled ? '<i class="fas fa-compress"></i>' : '<span class="rd-tool-text">1:1</span>';
        this.el.scale.title = this.scaled ? 'Scaled to fit. Click for actual size (scroll to pan)' : 'Actual size. Click to scale to fit';
    }

    setQuality(key) {
        this.quality = key;
        savePrefs({ quality: key });
        this.applyQuality();
        this.syncQualityMenu();
    }

    applyQuality() {
        if (!this.rfb) return;
        this.rfb.qualityLevel = QUALITY[this.quality].quality;
        this.rfb.compressionLevel = QUALITY[this.quality].compression;
    }

    syncQualityMenu() {
        this.container.querySelectorAll('.rd-quality-item').forEach(item => item.classList.toggle('active', item.dataset.quality === this.quality));
    }

    toggleFullscreen() {
        if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
        else this.el.root.requestFullscreen?.().catch(() => {});
    }

    syncFullscreenButton() {
        const on = document.fullscreenElement === this.el.root;
        this.el.fullscreen.innerHTML = `<i class="fas ${on ? 'fa-compress-arrows-alt' : 'fa-expand'}"></i>`;
        this.el.fullscreen.title = on ? 'Leave full screen' : 'Full screen';
        if (on && this.rfb && !this.viewOnly) this.rfb.focus();
    }

    // Keeps the server's idle timer fresh while someone is actually using this desktop
    sendPing() {
        if (!this.activity || this.state !== 'connected' || !this.sessionId) return;
        this.activity = false;
        api(`/vnc/sessions/${encodeURIComponent(this.sessionId)}/ping`, { method: 'POST' }).catch(() => {});
    }

    // Last-seen snapshot for the Remote Desktops list
    async captureThumbnail() {
        if (!this.rfb || this.state !== 'connected') return;
        try {
            const image = this.rfb.getImageData();
            if (!image || !image.width || !image.height) return;
            const bitmap = await createImageBitmap(image);
            const width = Math.min(THUMB_WIDTH, image.width);
            const height = Math.max(1, Math.round(width * image.height / image.width));
            const canvas = document.createElement('canvas');
            canvas.width = width;
            canvas.height = height;
            canvas.getContext('2d').drawImage(bitmap, 0, 0, width, height);
            bitmap.close?.();
            const blob = await new Promise(resolve => canvas.toBlob(resolve, 'image/jpeg', 0.72));
            if (blob) await api(`/vnc/connections/${encodeURIComponent(this.vncId)}/thumbnail`, { method: 'PUT', body: blob, headers: { 'Content-Type': 'image/jpeg' } });
        } catch (_) { /* thumbnails are best effort */ }
    }

    focus() { if (this.rfb && !this.viewOnly) this.rfb.focus(); }

    // Disconnects the viewer; the remote desktop keeps running.
    async destroy({ snapshot = true } = {}) {
        if (this.closing) return;
        if (snapshot) await this.captureThumbnail();
        this.closing = true;
        clearTimeout(this.retryTimer);
        clearInterval(this.countdownTimer);
        clearInterval(this.pingTimer);
        clearInterval(this.thumbTimer);
        clearTimeout(this.flashTimer);
        document.removeEventListener('fullscreenchange', this.fsHandler);
        if (document.fullscreenElement === this.el.root) document.exitFullscreen().catch(() => {});
        const sid = this.sessionId;
        if (this.rfb) { try { this.rfb.disconnect(); } catch (_) { /* already closed */ } }
        this.rfb = null;
        if (sid) api(`/vnc/sessions/${encodeURIComponent(sid)}`, { method: 'DELETE' }).catch(() => {});
        this.container.innerHTML = '';
    }
}
