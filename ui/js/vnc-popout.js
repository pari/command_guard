// Pop-out window for one remote desktop (vnc.html?id=<vnc_id>[&view=1]).
// Uses the same sign-in as the main app (token in localStorage).
import { VncViewer, api, esc } from './vnc-viewer.js?v=3';

const root = document.getElementById('rdPopout');
const params = new URLSearchParams(location.search);
const id = params.get('id') || '';

function message(html) {
    root.innerHTML = `<div class="rd-popout-message">${html}</div>`;
}

async function start() {
    if (!localStorage.getItem('auth_token')) {
        message('You are not signed in. <a href="/login.html">Sign in</a> and open the desktop again.');
        return;
    }
    let conn;
    try {
        const list = (await api('/vnc/connections')).data?.connections || [];
        conn = list.find(c => c.vnc_id === id);
    } catch (error) {
        if (error.status === 401) { message('Your sign-in has expired. <a href="/login.html">Sign in</a> and open the desktop again.'); return; }
        message(esc(error.message || 'Failed to load the remote desktop'));
        return;
    }
    if (!conn) {
        message('This remote desktop does not exist or is not shared with you.');
        return;
    }
    document.title = `${conn.name} - Remote Desktop`;
    const viewer = new VncViewer(root, {
        vncId: conn.vnc_id, name: conn.name, level: conn.level, viewOnly: params.get('view') === '1',
        clipboard: conn.clipboard, canPopout: false,
        onClose: async (v) => { await v.destroy(); window.close(); message('Disconnected. You can close this window.'); },
        onState: (v, state) => { document.title = `${state === 'connected' ? '' : '○ '}${conn.name} - Remote Desktop`; }
    });
    viewer.setViewers(conn.viewers);
    viewer.connect();
    // Keep the "Also connected" list fresh
    setInterval(async () => {
        try {
            const list = (await api('/vnc/connections')).data?.connections || [];
            const current = list.find(c => c.vnc_id === id);
            if (current) viewer.setViewers(current.viewers);
        } catch (_) { /* next poll */ }
    }, 15000);
    // Closing the window disconnects right away instead of waiting for the server to notice
    window.addEventListener('pagehide', () => {
        if (viewer.sessionId) {
            fetch(`/api/vnc/sessions/${encodeURIComponent(viewer.sessionId)}`, { method: 'DELETE', keepalive: true, headers: { Authorization: `Bearer ${localStorage.getItem('auth_token')}` } }).catch(() => {});
        }
    });
}

start();
