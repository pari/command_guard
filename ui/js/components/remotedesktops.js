// Remote Desktops page (VNC). Lists the desktops the signed-in account may use,
// opens them as tabs (viewer: /js/vnc-viewer.js, an ES module loaded on demand)
// and, for the admin, manages connections and who may use them. See docs/vnc.md.
// Leaving the page or closing a tab only disconnects the viewer: the desktop keeps
// running on the VNC server. Open tabs are remembered in this browser and reopen
// when you come back.
const RemoteDesktops = {
    connections: [],
    settings: {},
    viewerModule: null,
    viewers: {},          // vnc_id -> VncViewer
    tabOrder: [],         // open vnc_ids in tab order
    active: 'gallery',    // 'gallery' | vnc_id
    view: 'desktops',     // 'desktops' | 'manage' (admin)
    pollTimer: null,
    thumbs: {},           // vnc_id -> { at, src }
    disabled: false,
    admin: { connections: [], hosts: [], users: [], sessions: [], editing: null, access: {} },
    resizeHandler: null,
    STORAGE_KEY: 'remoteDesktopTabs',

    esc(value) { return Utils.escapeHtml(value == null ? '' : String(value)); },

    async init() {
        this.viewers = {};
        this.tabOrder = [];
        this.active = 'gallery';
        this.view = 'desktops';
        this.render();
        this.resizeHandler = () => this.sizeWorkspace();
        window.addEventListener('resize', this.resizeHandler);
        this.sizeWorkspace();
        try {
            this.viewerModule = await import('/js/vnc-viewer.js?v=3');
        } catch (error) {
            console.error('Failed to load the VNC viewer', error);
            $('#rdGallery').html('<div class="rd-empty text-danger">The remote desktop viewer could not be loaded. Reload the page.</div>');
            return;
        }
        await this.load();
        this.restoreTabs();
        this.pollTimer = setInterval(() => this.load(true), 10000);
    },

    render() {
        const admin = Auth.isAdmin();
        $('#mainContent').html(`
        <div class="rd-page">
            <div class="page-header rd-header">
                <div>
                    <h2><i class="fas fa-desktop me-2"></i>Remote Desktops</h2>
                    <p class="text-muted mb-0 small">VNC desktops in your browser. The server signs in to the desktop for you, so no VNC password is needed here.
                    Closing a tab or leaving this page only disconnects your viewer; the desktop keeps running. Desktops marked <strong>View only</strong> show the screen but do not accept your keyboard, mouse or clipboard.
                    <span id="rdIdleNote"></span></p>
                </div>
                ${admin ? `<div class="btn-group rd-view-switch" role="group" aria-label="Page view">
                    <button class="btn btn-sm btn-outline-primary active" type="button" data-view="desktops"><i class="fas fa-desktop me-1"></i>Desktops</button>
                    <button class="btn btn-sm btn-outline-primary" type="button" data-view="manage"><i class="fas fa-cog me-1"></i>Manage connections</button>
                </div>` : ''}
            </div>
            <div id="rdDesktopsView">
                <div class="rd-tabs" id="rdTabs" role="tablist"></div>
                <div class="rd-workspace" id="rdWorkspace">
                    <div class="rd-gallery" id="rdGallery"><div class="rd-empty"><i class="fas fa-spinner fa-spin"></i></div></div>
                </div>
            </div>
            ${admin ? '<div id="rdManageView" class="d-none"></div>' : ''}
        </div>
        ${admin ? this.editorModalHtml() : ''}`);
        $('#rdTabs').on('click', '.rd-tab', (e) => {
            const id = e.currentTarget.dataset.id;
            if ($(e.target).closest('.rd-tab-close').length) this.closeTab(id);
            else this.select(id);
        });
        $('#rdGallery').on('click', '[data-open]', (e) => {
            const btn = e.currentTarget;
            this.open(btn.dataset.open, btn.dataset.viewOnly === '1');
        });
        $('.rd-view-switch').on('click', 'button', (e) => this.setView(e.currentTarget.dataset.view));
        if (admin) this.bindEditor();
    },

    sizeWorkspace() {
        const ws = document.getElementById('rdWorkspace');
        if (!ws) return;
        const top = ws.getBoundingClientRect().top + window.scrollY;
        ws.style.setProperty('--rd-workspace-height', `${Math.max(360, Math.floor(window.innerHeight - top - 16))}px`);
    },

    async load(quiet = false) {
        try {
            const response = await API.vnc.list();
            this.disabled = false;
            this.connections = response.data?.connections || [];
            this.settings = response.data?.settings || {};
        } catch (error) {
            if (error.status === 403) {
                this.disabled = true;
                this.connections = [];
            } else if (!quiet) {
                Utils.showError(error.message || 'Failed to load remote desktops');
            }
        }
        const idle = this.settings.idle_disconnect_minutes;
        $('#rdIdleNote').text(this.disabled ? '' : (idle ? `Viewers are disconnected after ${idle} minutes without keyboard or mouse use.` : 'Idle viewers are not disconnected.'));
        this.renderGallery();
        this.renderTabs();
        Object.entries(this.viewers).forEach(([id, viewer]) => {
            const conn = this.connections.find(c => c.vnc_id === id);
            viewer.setViewers(conn ? conn.viewers : []);
        });
        if (this.view === 'manage') this.loadManage(true);
    },

    // ---- Gallery ----

    renderGallery() {
        const gallery = $('#rdGallery');
        if (this.disabled) {
            gallery.html(`<div class="rd-empty"><i class="fas fa-power-off fa-2x mb-2"></i><div>Remote Desktops are turned off.</div>${Auth.isAdmin() ? '<div class="small text-muted">Turn them on in App Settings.</div>' : ''}</div>`);
            return;
        }
        if (!this.connections.length) {
            gallery.html(`<div class="rd-empty"><i class="fas fa-desktop fa-2x mb-2"></i><div>No remote desktops ${Auth.isAdmin() ? 'are configured yet.' : 'have been shared with you.'}</div>${Auth.isAdmin() ? '<button class="btn btn-sm btn-primary mt-2" type="button" onclick="RemoteDesktops.setView(\'manage\'); RemoteDesktops.openEditor()"><i class="fas fa-plus me-1"></i>Add a VNC connection</button>' : ''}</div>`);
            return;
        }
        gallery.html(`<div class="rd-cards">${this.connections.map(c => this.cardHtml(c)).join('')}</div>`);
        this.connections.forEach(c => this.loadThumb(c));
    },

    cardHtml(c) {
        const open = !!this.viewers[c.vnc_id];
        const others = (c.viewers || []).filter(v => !v.mine);
        const names = [...new Set(others.map(v => v.username))];
        const seen = c.thumbnail_at ? `Last seen ${Utils.formatRelativeTime(c.thumbnail_at)}` : 'Not viewed yet';
        const control = c.level === 'control';
        return `<div class="rd-card ${open ? 'open' : ''}" data-id="${this.esc(c.vnc_id)}">
            <button class="rd-thumb" type="button" data-open="${this.esc(c.vnc_id)}" data-view-only="${control ? '0' : '1'}" title="${open ? 'Show' : 'Connect'}">
                <img alt="" class="${this.thumbs[c.vnc_id] ? '' : 'd-none'}" src="${this.thumbs[c.vnc_id] ? this.thumbs[c.vnc_id].src : ''}">
                <span class="rd-thumb-placeholder ${this.thumbs[c.vnc_id] ? 'd-none' : ''}"><i class="fas fa-desktop"></i></span>
                ${open ? '<span class="rd-thumb-open">Open</span>' : ''}
            </button>
            <div class="rd-card-body">
                <div class="rd-card-title">${this.esc(c.name)}${control ? '' : ' <span class="badge rd-badge-view">View only</span>'}</div>
                ${c.description ? `<div class="rd-card-desc">${this.esc(c.description)}</div>` : ''}
                ${c.target ? `<div class="rd-card-meta"><i class="fas fa-network-wired me-1"></i>${this.esc(c.target)}</div>` : ''}
                <div class="rd-card-meta">${this.esc(seen)}${names.length ? ` · <i class="fas fa-users ms-1 me-1"></i>${this.esc(names.join(', '))}` : ''}</div>
                <div class="rd-card-actions">
                    <button class="btn btn-sm btn-primary" type="button" data-open="${this.esc(c.vnc_id)}" data-view-only="${control ? '0' : '1'}"><i class="fas ${open ? 'fa-eye' : 'fa-plug'} me-1"></i>${open ? 'Show' : 'Connect'}</button>
                    ${control && !open ? `<button class="btn btn-sm btn-outline-secondary" type="button" data-open="${this.esc(c.vnc_id)}" data-view-only="1" title="Watch without sending keyboard or mouse input"><i class="fas fa-eye me-1"></i>View only</button>` : ''}
                </div>
            </div>
        </div>`;
    },

    async loadThumb(c) {
        if (!c.thumbnail_at) return;
        const cached = this.thumbs[c.vnc_id];
        if (cached && cached.at === c.thumbnail_at) return;
        try {
            const response = await API.vnc.thumbnail(c.vnc_id);
            this.thumbs[c.vnc_id] = { at: c.thumbnail_at, src: response.data.data_url };
            const card = $(`#rdGallery .rd-card[data-id="${CSS.escape(c.vnc_id)}"]`);
            card.find('img').attr('src', response.data.data_url).removeClass('d-none');
            card.find('.rd-thumb-placeholder').addClass('d-none');
        } catch (_) { /* no thumbnail */ }
    },

    // ---- Tabs ----

    renderTabs() {
        const tabs = [`<button class="rd-tab ${this.active === 'gallery' ? 'active' : ''}" type="button" role="tab" data-id="gallery"><i class="fas fa-th-large me-1"></i>All desktops</button>`];
        this.tabOrder.forEach(id => {
            const viewer = this.viewers[id];
            if (!viewer) return;
            const state = viewer.state || 'connecting';
            tabs.push(`<span class="rd-tab ${this.active === id ? 'active' : ''}" role="tab" tabindex="0" data-id="${this.esc(id)}" title="${this.esc(viewer.name)}">
                <span class="rd-tab-dot ${state}"></span><span class="rd-tab-name">${this.esc(viewer.name)}</span>${viewer.viewOnly ? ' <i class="fas fa-eye rd-tab-eye" title="View only"></i>' : ''}
                <button class="rd-tab-close" type="button" aria-label="Disconnect ${this.esc(viewer.name)}" title="Disconnect"><i class="fas fa-times"></i></button>
            </span>`);
        });
        $('#rdTabs').html(tabs.join(''));
    },

    open(id, viewOnly = false) {
        if (this.viewers[id]) { this.select(id); return; }
        const conn = this.connections.find(c => c.vnc_id === id);
        if (!conn || !this.viewerModule) return;
        const pane = $(`<div class="rd-pane" data-id="${this.esc(id)}"></div>`).appendTo('#rdWorkspace')[0];
        const viewer = new this.viewerModule.VncViewer(pane, {
            vncId: id, name: conn.name, level: conn.level, viewOnly, clipboard: conn.clipboard, canPopout: true,
            onPopout: (v) => this.popout(v),
            onClose: (v) => this.closeTab(v.vncId),
            onState: () => this.renderTabs(),
            onAuthLost: () => { window.location.href = '/login.html'; }
        });
        viewer.setViewers(conn.viewers);
        this.viewers[id] = viewer;
        this.tabOrder.push(id);
        this.saveTabs();
        this.select(id);
        viewer.connect();
        this.renderGallery();
    },

    select(id) {
        if (id !== 'gallery' && !this.viewers[id]) id = 'gallery';
        this.active = id;
        $('#rdGallery').toggleClass('d-none', id !== 'gallery');
        $('#rdWorkspace .rd-pane').each((_, el) => { el.classList.toggle('d-none', el.dataset.id !== id); });
        this.renderTabs();
        if (id !== 'gallery') setTimeout(() => this.viewers[id] && this.viewers[id].focus(), 0);
    },

    async closeTab(id) {
        const viewer = this.viewers[id];
        if (!viewer) return;
        delete this.viewers[id];
        this.tabOrder = this.tabOrder.filter(x => x !== id);
        this.saveTabs();
        const index = this.tabOrder.length ? this.tabOrder[this.tabOrder.length - 1] : 'gallery';
        this.select(this.active === id ? index : this.active);
        await viewer.destroy();
        $(`#rdWorkspace .rd-pane[data-id="${CSS.escape(id)}"]`).remove();
        this.load(true);
    },

    async popout(viewer) {
        const id = viewer.vncId;
        const url = `/vnc.html?id=${encodeURIComponent(id)}${viewer.viewOnly ? '&view=1' : ''}`;
        const win = window.open(url, `rd_${id}`, 'popup=yes,width=1280,height=860');
        if (!win) { Utils.showError('The browser blocked the pop-up window. Allow pop-ups for this site and try again.'); return; }
        await this.closeTab(id);
    },

    saveTabs() {
        const tabs = this.tabOrder.map(id => ({ id, view: !!(this.viewers[id] && this.viewers[id].viewOnly) }));
        try { localStorage.setItem(this.STORAGE_KEY, JSON.stringify(tabs)); } catch (_) { /* optional */ }
    },

    restoreTabs() {
        let tabs = [];
        try { tabs = JSON.parse(localStorage.getItem(this.STORAGE_KEY) || '[]'); } catch (_) { tabs = []; }
        if (!Array.isArray(tabs)) return;
        const max = this.settings.max_sessions_per_user || 5;
        tabs.slice(0, max).forEach(t => { if (t && this.connections.some(c => c.vnc_id === t.id)) this.open(t.id, !!t.view); });
        this.saveTabs();
        if (this.tabOrder.length) this.select(this.tabOrder[0]);
    },

    setView(view) {
        if (!Auth.isAdmin()) return;
        this.view = view;
        $('.rd-view-switch button').each((_, el) => el.classList.toggle('active', el.dataset.view === view));
        $('#rdDesktopsView').toggleClass('d-none', view !== 'desktops');
        $('#rdManageView').toggleClass('d-none', view !== 'manage');
        if (view === 'manage') this.loadManage();
        else { this.sizeWorkspace(); this.load(true); }
    },

    // Leaving the page disconnects the viewers (the desktops keep running); the tabs are
    // remembered and reopen next time.
    async cleanup() {
        clearInterval(this.pollTimer);
        this.pollTimer = null;
        window.removeEventListener('resize', this.resizeHandler);
        const viewers = Object.values(this.viewers);
        this.viewers = {};
        await Promise.all(viewers.map(v => v.destroy().catch(() => {})));
        $('#rdEditorModal').each((_, el) => { bootstrap.Modal.getInstance(el)?.hide(); });
        $('#rdEditorModal').remove();
        $('.modal-backdrop').remove();
    },

    // =====================================================================
    // Admin: manage connections
    // =====================================================================

    async loadManage(quiet = false) {
        try {
            const [conns, sessions] = await Promise.all([API.vnc.admin.list(), API.vnc.sessions().catch(() => ({ data: [] }))]);
            this.admin.connections = conns.data || [];
            this.admin.sessions = sessions.data || [];
        } catch (error) {
            if (!quiet) Utils.showError(error.message || 'Failed to load VNC connections');
        }
        this.renderManage();
    },

    renderManage() {
        const authLabel = { auto: 'Auto', none: 'None', vnc: 'VNC password', ard: 'macOS (ARD)', vencrypt: 'VeNCrypt' };
        const rows = this.admin.connections.map(c => {
            const target = c.tunnel_host_ref
                ? `${this.esc(c.host || '127.0.0.1')}:${c.port} <span class="text-muted">via</span> ${this.esc(c.tunnel_host_ref)}`
                : `${this.esc(c.host)}:${c.port}`;
            const access = (c.access || []).map(a => `<span class="badge ${a.level === 'control' ? 'bg-primary' : 'bg-secondary'} me-1" title="${a.level === 'control' ? 'Control' : 'View only'}">${this.esc(a.username)}${a.level === 'view' ? ' <i class="fas fa-eye"></i>' : ''}</span>`).join('') || '<span class="text-muted small">Admin only</span>';
            const live = this.admin.sessions.filter(s => s.vnc_id === c.vnc_id).length;
            return `<tr>
                <td><strong>${this.esc(c.name || c.vnc_id)}</strong><div class="small text-muted font-monospace">${this.esc(c.vnc_id)}</div></td>
                <td class="small">${target}</td>
                <td class="small">${authLabel[c.auth_type] || this.esc(c.auth_type)}${c.has_password ? ' <i class="fas fa-key text-muted" title="Password stored"></i>' : ''}${c.allow_clipboard ? '' : ' <i class="fas fa-clipboard text-muted" title="Clipboard off" style="opacity:.5"></i>'}</td>
                <td>${access}</td>
                <td>${live ? `<span class="badge bg-success">${live}</span>` : '<span class="text-muted">0</span>'}</td>
                <td class="text-end text-nowrap">
                    <button class="btn btn-sm btn-outline-secondary" type="button" onclick="RemoteDesktops.testConnection('${this.esc(c.vnc_id)}')" title="Connect and sign in to check the settings"><i class="fas fa-plug"></i></button>
                    <button class="btn btn-sm btn-outline-primary" type="button" onclick="RemoteDesktops.openEditor('${this.esc(c.vnc_id)}')"><i class="fas fa-edit me-1"></i>Edit</button>
                    <button class="btn btn-sm btn-outline-danger" type="button" onclick="RemoteDesktops.deleteConnection('${this.esc(c.vnc_id)}')" ${(c.access || []).length ? 'disabled title="Remove user access first (Edit > Access)"' : 'title="Delete"'}><i class="fas fa-trash"></i></button>
                </td>
            </tr>`;
        }).join('');
        const sessions = this.admin.sessions.map(s => `<tr>
            <td>${this.esc(s.name || s.vnc_id)}</td>
            <td>${this.esc(s.username)}${s.role === 'admin' ? ' <span class="badge bg-dark">admin</span>' : ''}</td>
            <td>${s.level === 'view' ? '<span class="badge bg-secondary">View only</span>' : '<span class="badge bg-primary">Control</span>'}</td>
            <td class="small">${this.esc(s.client_ip || '')}</td>
            <td class="small">${s.attached_at ? this.esc(dayjs(s.attached_at).fromNow(true)) : this.esc(s.status)}</td>
            <td class="small">${s.last_activity ? this.esc(Utils.formatRelativeTime(s.last_activity)) : ''}</td>
            <td class="text-end"><button class="btn btn-sm btn-outline-danger" type="button" onclick="RemoteDesktops.kick('${this.esc(s.session_id)}', '${this.esc(s.username)}')"><i class="fas fa-user-slash me-1"></i>Disconnect</button></td>
        </tr>`).join('');
        $('#rdManageView').html(`
            <div class="d-flex justify-content-between align-items-center mb-2">
                <h5 class="mb-0">VNC connections</h5>
                <button class="btn btn-primary btn-sm" type="button" onclick="RemoteDesktops.openEditor()"><i class="fas fa-plus me-1"></i>Add connection</button>
            </div>
            <div class="card mb-4"><div class="table-responsive"><table class="table table-hover mb-0 align-middle">
                <thead><tr><th>Connection</th><th>Target</th><th>Sign-in</th><th>Who has access</th><th>Open now</th><th></th></tr></thead>
                <tbody>${rows || '<tr><td colspan="6" class="text-center py-4 text-muted">No VNC connections yet. Add one to get started.</td></tr>'}</tbody>
            </table></div></div>
            <h5 class="mb-2">Open viewers</h5>
            <div class="card"><div class="table-responsive"><table class="table table-sm mb-0 align-middle">
                <thead><tr><th>Desktop</th><th>User</th><th>Access</th><th>From</th><th>Connected for</th><th>Last activity</th><th></th></tr></thead>
                <tbody>${sessions || '<tr><td colspan="7" class="text-center py-3 text-muted">Nobody is connected.</td></tr>'}</tbody>
            </table></div></div>`);
    },

    async kick(sid, username) {
        const result = await Swal.fire({ icon: 'warning', title: 'Disconnect viewer?', text: `${username} will be disconnected from this desktop. The desktop itself keeps running.`, showCancelButton: true, confirmButtonText: 'Disconnect', confirmButtonColor: '#dc3545' });
        if (!result.isConfirmed) return;
        try {
            await API.vnc.disconnect(sid);
            Utils.showSuccess('Viewer disconnected');
        } catch (error) {
            Utils.showError(error.message || 'Failed to disconnect');
        }
        this.loadManage(true);
    },

    async testConnection(id, data) {
        Utils.showLoading();
        try {
            const response = await API.vnc.admin.test(id, data);
            const r = response.data;
            Swal.fire({ icon: 'success', title: 'Connected', html: `Signed in to <strong>${this.esc(r.desktop_name || id)}</strong><br><span class="small text-muted">${r.width}×${r.height} · RFB ${this.esc(r.protocol)} · ${this.esc(r.security)} · ${r.elapsed_ms} ms</span>` });
        } catch (error) {
            Swal.fire({ icon: 'error', title: 'Connection failed', text: error.message || 'Test failed' });
        } finally {
            Utils.hideLoading();
        }
    },

    async deleteConnection(id) {
        const result = await Swal.fire({ icon: 'warning', title: `Delete ${id}?`, text: 'Open viewers of this desktop are disconnected. The VNC server itself is not changed.', showCancelButton: true, confirmButtonText: 'Delete', confirmButtonColor: '#dc3545' });
        if (!result.isConfirmed) return;
        try {
            await API.vnc.admin.remove(id);
            Utils.showSuccess('VNC connection deleted');
            if (this.viewers[id]) await this.closeTab(id);
        } catch (error) {
            Utils.showError(error.message || 'Failed to delete');
        }
        this.loadManage(true);
        this.load(true);
    },

    // ---- Connection editor (modal with Connection and Access tabs) ----

    editorModalHtml() {
        return `
        <div class="modal fade" id="rdEditorModal" tabindex="-1" aria-labelledby="rdEditorTitle" aria-hidden="true">
            <div class="modal-dialog modal-lg modal-dialog-scrollable">
                <div class="modal-content">
                    <div class="modal-header">
                        <h5 class="modal-title" id="rdEditorTitle">VNC connection</h5>
                        <button type="button" class="btn-close" data-bs-dismiss="modal" aria-label="Close"></button>
                    </div>
                    <div class="modal-body">
                        <ul class="nav nav-tabs mb-3" role="tablist">
                            <li class="nav-item"><button class="nav-link active" type="button" data-rd-tab="connection">Connection</button></li>
                            <li class="nav-item"><button class="nav-link" type="button" data-rd-tab="access">Access <span class="badge rounded-pill bg-light text-muted" id="rdAccessCount">0</span></button></li>
                        </ul>
                        <div data-rd-section="connection">
                            <div class="row g-3">
                                <div class="col-md-5">
                                    <label class="form-label" for="rdId">ID</label>
                                    <input class="form-control" id="rdId" maxlength="64" placeholder="design-imac" autocomplete="off">
                                    <div class="form-text" id="rdIdHelp">Letters, numbers, '-', '_' and '.'. Can't be changed later.</div>
                                </div>
                                <div class="col-md-7">
                                    <label class="form-label" for="rdName">Name</label>
                                    <input class="form-control" id="rdName" maxlength="80" placeholder="Design iMac">
                                </div>
                                <div class="col-12">
                                    <label class="form-label" for="rdDescription">Description</label>
                                    <input class="form-control" id="rdDescription" maxlength="500" placeholder="Optional, shown on the desktop card">
                                </div>
                                <div class="col-12">
                                    <label class="form-label d-block">How to reach the VNC server</label>
                                    <div class="form-check form-check-inline">
                                        <input class="form-check-input" type="radio" name="rdRoute" id="rdRouteTunnel" value="tunnel">
                                        <label class="form-check-label" for="rdRouteTunnel">Through an SSH host <span class="text-muted small">(recommended: encrypted, VNC can stay on localhost)</span></label>
                                    </div>
                                    <div class="form-check form-check-inline">
                                        <input class="form-check-input" type="radio" name="rdRoute" id="rdRouteDirect" value="direct">
                                        <label class="form-check-label" for="rdRouteDirect">Directly from this server</label>
                                    </div>
                                </div>
                                <div class="col-md-5 rd-tunnel-only">
                                    <label class="form-label" for="rdTunnelHost">SSH host</label>
                                    <select class="form-select" id="rdTunnelHost"></select>
                                    <div class="form-text">Its bastion host, if any, is used too.</div>
                                </div>
                                <div class="col-md-5">
                                    <label class="form-label" for="rdHost">VNC server address</label>
                                    <input class="form-control" id="rdHost" placeholder="192.168.1.20" autocomplete="off">
                                    <div class="form-text" id="rdHostHelp"></div>
                                </div>
                                <div class="col-md-2">
                                    <label class="form-label" for="rdPort">Port</label>
                                    <input class="form-control" id="rdPort" type="number" min="1" max="65535" placeholder="5900">
                                </div>
                                <div class="col-md-5">
                                    <label class="form-label" for="rdAuthType">Sign-in method</label>
                                    <select class="form-select" id="rdAuthType">
                                        <option value="auto">Automatic</option>
                                        <option value="vnc">VNC password (Linux, Windows, RealVNC)</option>
                                        <option value="ard">macOS Screen Sharing (user + password)</option>
                                        <option value="vencrypt">VeNCrypt (TigerVNC X509 / Plain)</option>
                                        <option value="none">No password</option>
                                    </select>
                                </div>
                                <div class="col-md-7"><div class="form-text mt-md-4 pt-md-2" id="rdAuthHelp"></div></div>
                                <div class="col-md-5 rd-user-field">
                                    <label class="form-label" for="rdUsername">Username</label>
                                    <input class="form-control" id="rdUsername" autocomplete="off">
                                </div>
                                <div class="col-md-7 rd-pass-field">
                                    <label class="form-label" for="rdPassword">Password</label>
                                    <input class="form-control" id="rdPassword" type="password" autocomplete="new-password">
                                    <div class="form-check mt-1 d-none" id="rdClearPasswordWrap">
                                        <input class="form-check-input" type="checkbox" id="rdClearPassword">
                                        <label class="form-check-label small" for="rdClearPassword">Remove the stored password</label>
                                    </div>
                                    <div class="form-text">Stored on the server only; never sent to browsers. <code>\${{ENV_VAR}}</code> works too.</div>
                                </div>
                                <div class="col-md-6 rd-tls-field">
                                    <div class="form-check form-switch">
                                        <input class="form-check-input" type="checkbox" id="rdTlsSkip">
                                        <label class="form-check-label" for="rdTlsSkip">Skip TLS certificate verification (VeNCrypt X509 with a self-signed certificate)</label>
                                    </div>
                                </div>
                                <div class="col-md-6">
                                    <div class="form-check form-switch">
                                        <input class="form-check-input" type="checkbox" id="rdClipboard">
                                        <label class="form-check-label" for="rdClipboard">Allow clipboard copy and paste</label>
                                    </div>
                                </div>
                            </div>
                        </div>
                        <div data-rd-section="access" hidden>
                            <p class="small text-muted">Choose who can use this desktop. <strong>Control</strong> can type, use the mouse and the clipboard; <strong>View only</strong> sees the screen and nothing they do is sent. Admin always has control. Removing access disconnects that user within about 15 seconds.</p>
                            <input class="form-control form-control-sm mb-2" id="rdAccessFilter" placeholder="Filter users…">
                            <div class="rd-access-list" id="rdAccessList"></div>
                        </div>
                        <div class="alert alert-danger d-none mt-3 mb-0" id="rdEditorError"></div>
                    </div>
                    <div class="modal-footer">
                        <button type="button" class="btn btn-outline-secondary me-auto" id="rdEditorTest"><i class="fas fa-plug me-1"></i>Test connection</button>
                        <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Cancel</button>
                        <button type="button" class="btn btn-primary" id="rdEditorSave"><i class="fas fa-check me-1"></i>Save</button>
                    </div>
                </div>
            </div>
        </div>`;
    },

    bindEditor() {
        const modal = $('#rdEditorModal');
        modal.on('click', '[data-rd-tab]', (e) => this.showEditorTab(e.currentTarget.dataset.rdTab));
        modal.on('change', 'input[name="rdRoute"], #rdAuthType', () => this.syncEditor());
        modal.on('change', '#rdClearPassword', (e) => $('#rdPassword').prop('disabled', e.target.checked));
        modal.on('input', '#rdAccessFilter', () => this.renderAccessList());
        modal.on('change', '.rd-access-level', (e) => {
            const name = e.target.dataset.user;
            if (e.target.value) this.admin.access[name] = e.target.value; else delete this.admin.access[name];
            $('#rdAccessCount').text(Object.keys(this.admin.access).length);
        });
        $('#rdEditorSave').on('click', () => this.saveEditor());
        $('#rdEditorTest').on('click', () => {
            const data = this.editorData();
            this.testConnection(this.admin.editing || data.vnc_id || 'new', data);
        });
    },

    showEditorTab(tab) {
        $('#rdEditorModal [data-rd-tab]').each((_, el) => el.classList.toggle('active', el.dataset.rdTab === tab));
        $('#rdEditorModal [data-rd-section]').each((_, el) => { el.hidden = el.dataset.rdSection !== tab; });
    },

    syncEditor() {
        const tunnel = $('#rdRouteTunnel').is(':checked');
        const auth = $('#rdAuthType').val();
        $('.rd-tunnel-only').toggleClass('d-none', !tunnel);
        $('#rdHost').attr('placeholder', tunnel ? '127.0.0.1' : '192.168.1.20');
        $('#rdHostHelp').text(tunnel ? 'As seen from the SSH host. Blank = 127.0.0.1 (VNC on the SSH host itself).' : 'Must be reachable from the server running this app.');
        $('.rd-user-field').toggleClass('d-none', !['auto', 'ard', 'vencrypt'].includes(auth));
        $('.rd-pass-field').toggleClass('d-none', auth === 'none');
        $('.rd-tls-field').toggleClass('d-none', !['auto', 'vencrypt'].includes(auth));
        const help = {
            auto: 'Uses what the server offers: no password, macOS sign-in (when a username is set), VNC password, then VeNCrypt.',
            vnc: 'Classic VNC password (only the first 8 characters count). RealVNC Server: set Authentication to "VNC password" and Encryption to "Prefer off".',
            ard: 'macOS: turn on System Settings > General > Sharing > Screen Sharing and allow this macOS user.',
            vencrypt: 'TigerVNC with -SecurityTypes X509Vnc/X509Plain/Plain. Anonymous-TLS types (TLSVnc) are not supported; use an SSH tunnel instead.',
            none: 'Only for servers without a password, ideally reached through an SSH tunnel.'
        };
        $('#rdAuthHelp').text(help[auth] || '');
    },

    async openEditor(id = null) {
        Utils.showLoading();
        try {
            const [hosts, users] = await Promise.all([API.config.getHosts(), API.users.list()]);
            this.admin.hosts = (hosts.data || []).filter(h => (h.connection_type || 'ssh').toLowerCase() === 'ssh').sort((a, b) => a.name.localeCompare(b.name));
            this.admin.users = (users.data?.users || []).map(u => ({ username: u.username, enabled: u.enabled })).sort((a, b) => a.username.localeCompare(b.username));
        } catch (error) {
            Utils.hideLoading();
            Utils.showError(error.message || 'Failed to load hosts and users');
            return;
        }
        Utils.hideLoading();
        const conn = id ? this.admin.connections.find(c => c.vnc_id === id) : null;
        if (id && !conn) { Utils.showError('Connection not found'); return; }
        this.admin.editing = id;
        this.admin.access = {};
        (conn?.access || []).forEach(a => { this.admin.access[a.username] = a.level; });
        $('#rdEditorTitle').text(id ? `Edit remote desktop · ${conn.name || id}` : 'Add remote desktop');
        $('#rdId').val(conn?.vnc_id || '').prop('readonly', !!id);
        $('#rdIdHelp').text(id ? 'The ID can\'t be changed.' : 'Letters, numbers, \'-\', \'_\' and \'.\'. Can\'t be changed later.');
        $('#rdName').val(conn?.name || '');
        $('#rdDescription').val(conn?.description || '');
        $('#rdTunnelHost').html(this.admin.hosts.map(h => `<option value="${this.esc(h.name)}">${this.esc(h.name)} (${this.esc(h.username || '')}@${this.esc(h.host || '')})</option>`).join('') || '<option value="">No SSH hosts configured</option>');
        const tunnel = conn ? !!conn.tunnel_host_ref : this.admin.hosts.length > 0;
        $(tunnel ? '#rdRouteTunnel' : '#rdRouteDirect').prop('checked', true);
        if (conn?.tunnel_host_ref) $('#rdTunnelHost').val(conn.tunnel_host_ref);
        $('#rdHost').val(conn?.host || '');
        $('#rdPort').val(conn?.port && conn.port !== 5900 ? conn.port : '');
        $('#rdAuthType').val(conn?.auth_type || 'auto');
        $('#rdUsername').val(conn?.username || '');
        $('#rdPassword').val('').prop('disabled', false).attr('placeholder', conn?.has_password ? '••••••• (unchanged)' : '');
        $('#rdClearPassword').prop('checked', false);
        $('#rdClearPasswordWrap').toggleClass('d-none', !conn?.has_password);
        $('#rdTlsSkip').prop('checked', !!conn?.tls_skip_verify);
        $('#rdClipboard').prop('checked', conn ? conn.allow_clipboard !== false : true);
        $('#rdAccessFilter').val('');
        $('#rdAccessCount').text(Object.keys(this.admin.access).length);
        $('#rdEditorError').addClass('d-none').text('');
        this.syncEditor();
        this.renderAccessList();
        this.showEditorTab('connection');
        bootstrap.Modal.getOrCreateInstance(document.getElementById('rdEditorModal')).show();
    },

    renderAccessList() {
        const query = String($('#rdAccessFilter').val() || '').trim().toLowerCase();
        const users = this.admin.users.filter(u => !query || u.username.includes(query));
        if (!this.admin.users.length) {
            $('#rdAccessList').html('<div class="text-muted small py-3 text-center">No regular users yet. Create them on the Users &amp; Security page.</div>');
            return;
        }
        $('#rdAccessList').html(users.map(u => {
            const level = this.admin.access[u.username] || '';
            return `<div class="rd-access-row">
                <span><i class="fas fa-user me-2 text-muted"></i>${this.esc(u.username)}${u.enabled ? '' : ' <span class="badge bg-secondary">disabled</span>'}</span>
                <select class="form-select form-select-sm rd-access-level" data-user="${this.esc(u.username)}" aria-label="Access for ${this.esc(u.username)}">
                    <option value="" ${level === '' ? 'selected' : ''}>No access</option>
                    <option value="view" ${level === 'view' ? 'selected' : ''}>View only</option>
                    <option value="control" ${level === 'control' ? 'selected' : ''}>Control</option>
                </select>
            </div>`;
        }).join('') || '<div class="text-muted small py-2">Nobody matches the filter.</div>');
    },

    editorData() {
        const tunnel = $('#rdRouteTunnel').is(':checked');
        const auth = $('#rdAuthType').val();
        const port = parseInt($('#rdPort').val(), 10);
        const data = {
            vnc_id: $('#rdId').val().trim(),
            name: $('#rdName').val().trim(),
            description: $('#rdDescription').val().trim(),
            tunnel_host_ref: tunnel ? ($('#rdTunnelHost').val() || '') : '',
            host: $('#rdHost').val().trim(),
            port: Number.isFinite(port) ? port : 0,
            auth_type: auth,
            username: ['auto', 'ard', 'vencrypt'].includes(auth) ? $('#rdUsername').val().trim() : '',
            tls_skip_verify: ['auto', 'vencrypt'].includes(auth) && $('#rdTlsSkip').is(':checked'),
            allow_clipboard: $('#rdClipboard').is(':checked')
        };
        if ($('#rdClearPassword').is(':checked')) data.clear_password = true;
        else if ($('#rdPassword').val()) data.password = $('#rdPassword').val();
        return data;
    },

    editorProblem(data) {
        if (!data.vnc_id) return 'Enter an ID.';
        if (!/^[A-Za-z0-9._-]{1,64}$/.test(data.vnc_id)) return 'The ID may contain only letters, numbers, \'-\', \'_\' and \'.\'.';
        if (!this.admin.editing && this.admin.connections.some(c => c.vnc_id === data.vnc_id)) return `A connection called ${data.vnc_id} already exists.`;
        if (data.tunnel_host_ref === '' && $('#rdRouteTunnel').is(':checked')) return 'Pick the SSH host to tunnel through, or choose Directly.';
        if (!data.tunnel_host_ref && !data.host) return 'Enter the VNC server address.';
        if (data.port && (data.port < 1 || data.port > 65535)) return 'The port must be from 1 to 65535.';
        const conn = this.admin.connections.find(c => c.vnc_id === this.admin.editing);
        const hasPassword = data.password || (conn?.has_password && !data.clear_password);
        if (data.auth_type === 'ard' && (!data.username || !hasPassword)) return 'macOS Screen Sharing needs the macOS username and password.';
        if (data.auth_type === 'vnc' && !hasPassword) return 'Enter the VNC password.';
        return '';
    },

    async saveEditor() {
        const data = this.editorData();
        const problem = this.editorProblem(data);
        if (problem) {
            $('#rdEditorError').removeClass('d-none').text(problem);
            this.showEditorTab('connection');
            return;
        }
        const access = Object.entries(this.admin.access).map(([username, level]) => ({ username, level }));
        const btn = $('#rdEditorSave').prop('disabled', true);
        try {
            if (this.admin.editing) await API.vnc.admin.update(this.admin.editing, data);
            else await API.vnc.admin.create(data);
            await API.vnc.admin.saveAccess(data.vnc_id, access);
            bootstrap.Modal.getInstance(document.getElementById('rdEditorModal'))?.hide();
            Utils.showSuccess(this.admin.editing ? 'Remote desktop saved' : 'Remote desktop added');
            this.loadManage(true);
            this.load(true);
        } catch (error) {
            $('#rdEditorError').removeClass('d-none').text(error.message || 'Failed to save');
            // A new connection that saved before the access list failed is now an edit
            if (!this.admin.editing && error.message && !/already exists/.test(error.message)) {
                try { await this.loadManage(true); if (this.admin.connections.some(c => c.vnc_id === data.vnc_id)) { this.admin.editing = data.vnc_id; $('#rdId').prop('readonly', true); } } catch (_) { /* ignore */ }
            }
        } finally {
            btn.prop('disabled', false);
        }
    }
};
window.RemoteDesktops = RemoteDesktops;
