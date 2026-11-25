// Disk Monitor Component: free-space checks over SSH (df) on shared Hosts, threshold alerts and daily summary.
const DiskMonitor = {
    UNITS: ['GB', 'MB', '%'],
    DEFAULT_SUBJECT: '[Disk Monitor] {{summary}}',
    state: null,
    settings: null,
    hosts: [],
    filters: { search: '', host: 'all', status: 'all' },
    pollTimer: null,
    refreshing: false,
    editingId: null,
    detect: { hostRef: '', rows: [], useSudo: false },

    escape(value) {
        return String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#039;' })[c]);
    },

    async init() {
        this.state = null;
        this.settings = null;
        this.hosts = [];
        this.filters = { search: '', host: 'all', status: 'all' };
        this.editingId = null;
        this.render();
        await this.loadData();
        this.startPolling();
    },

    render() {
        $('#mainContent').html(`
            <div class="commands-page diskmon-page">
                <div class="commands-page-header">
                    <div>
                        <h1>Disk Monitor</h1>
                        <p>Free-space checks over SSH on your Hosts, with threshold alerts and a daily summary by email.</p>
                    </div>
                    <div class="d-flex flex-wrap gap-2">
                        <button class="btn btn-outline-secondary" type="button" onclick="DiskMonitor.showTestModal()"><i class="fas fa-plug me-2"></i>Test host</button>
                        <button class="btn btn-outline-primary" type="button" onclick="DiskMonitor.showDetectModal()"><i class="fas fa-search me-2"></i>Detect volumes</button>
                        <button class="btn btn-primary commands-add-button" type="button" onclick="DiskMonitor.showAddModal()"><i class="fas fa-plus me-2"></i>Add volume</button>
                    </div>
                </div>
                <div id="diskMonBanners"></div>
                <div id="diskMonSummary" class="commands-summary" aria-label="Disk monitor summary"></div>
                <div class="commands-toolbar diskmon-toolbar" aria-label="Disk monitor filters">
                    <div class="commands-search-wrap"><i class="fas fa-search" aria-hidden="true"></i><input type="search" class="form-control" id="diskMonSearch" placeholder="Search host or mount..." aria-label="Search volumes" oninput="DiskMonitor.updateFilter('search', this.value)"></div>
                    <div class="commands-filter-field"><label for="diskMonHostFilter">Host</label><select class="form-select" id="diskMonHostFilter" onchange="DiskMonitor.updateFilter('host', this.value)"><option value="all">All hosts</option></select></div>
                    <div class="commands-filter-field"><label for="diskMonStatusFilter">Status</label><select class="form-select" id="diskMonStatusFilter" onchange="DiskMonitor.updateFilter('status', this.value)"><option value="all">All statuses</option><option value="OK">OK</option><option value="TRIPPED">Tripped</option><option value="UNKNOWN">Check failed</option><option value="never">Never checked</option><option value="disabled">Disabled</option></select></div>
                    <button class="btn commands-clear-button" type="button" onclick="DiskMonitor.clearFilters()"><i class="fas fa-filter me-2"></i>Clear filters</button>
                </div>
                <div id="diskMonTable"><div class="text-center py-5"><i class="fas fa-spinner fa-spin fa-2x text-primary"></i><p class="mt-3 text-muted">Loading disk monitor...</p></div></div>
                <div class="small text-muted mb-3" id="diskMonRefreshNote"></div>
                <div class="card mb-4" id="diskMonSettingsCard">
                    <div class="card-header d-flex justify-content-between align-items-center" role="button" data-bs-toggle="collapse" data-bs-target="#diskMonSettingsBody" aria-expanded="false">
                        <span><i class="fas fa-sliders-h me-2"></i>Settings</span>
                        <i class="fas fa-chevron-down small"></i>
                    </div>
                    <div class="collapse" id="diskMonSettingsBody"><div class="card-body" id="diskMonSettingsForm"></div></div>
                </div>
            </div>
            ${this.renderVolumeModal()}
            ${this.renderDetectModal()}
            ${this.renderTestModal()}
        `);
    },

    hostOptions(selected) {
        return this.hosts.map(h => `<option value="${this.escape(h.host_id)}" ${h.host_id === selected ? 'selected' : ''}>${this.escape(h.host_id)} (${this.escape(h.host)})</option>`).join('');
    },

    renderVolumeModal() {
        return `
            <div class="modal fade" id="diskVolumeModal" tabindex="-1">
                <div class="modal-dialog modal-lg">
                    <div class="modal-content">
                        <div class="modal-header"><h5 class="modal-title" id="diskVolumeModalTitle">Add volume</h5><button type="button" class="btn-close" data-bs-dismiss="modal"></button></div>
                        <div class="modal-body">
                            <form id="diskVolumeForm" onsubmit="return false">
                                <div class="row">
                                    <div class="col-md-6 mb-3"><label class="form-label">Host *</label><select class="form-select" id="diskVolHost"></select><div class="form-text">SSH hosts from the Hosts page. WinRM hosts cannot be monitored.</div></div>
                                    <div class="col-md-6 mb-3"><label class="form-label">Mount path *</label><input type="text" class="form-control font-monospace" id="diskVolMount" placeholder="/data"><div class="form-text">Exact mount point as shown by <code>df</code> (a sub-directory of a mount is reported as failed).</div></div>
                                </div>
                                <div class="row">
                                    <div class="col-md-4 mb-3"><label class="form-label">Threshold *</label><div class="input-group"><input type="number" class="form-control" id="diskVolThreshold" min="0" step="any"><select class="form-select" id="diskVolUnit" style="max-width:6rem">${this.UNITS.map(u => `<option value="${u}">${u}</option>`).join('')}</select></div><div class="form-text">Alert when free space drops below this.</div></div>
                                    <div class="col-md-4 mb-3"><label class="form-label">Check interval</label><input type="text" class="form-control" id="diskVolInterval" placeholder="1h"><div class="form-text">30m, 1h, 1h30m, 2d (1m – 7d). Empty = default.</div></div>
                                    <div class="col-md-4 mb-3"><label class="form-label">Description</label><input type="text" class="form-control" id="diskVolDescription" placeholder="Optional note"></div>
                                </div>
                                <div class="row">
                                    <div class="col-md-4 mb-2"><div class="form-check"><input class="form-check-input" type="checkbox" id="diskVolSudo"><label class="form-check-label" for="diskVolSudo">Run df with <code>sudo -n</code></label></div></div>
                                    <div class="col-md-4 mb-2"><div class="form-check form-switch"><input class="form-check-input" type="checkbox" id="diskVolEnabled" checked><label class="form-check-label" for="diskVolEnabled">Enabled</label></div></div>
                                </div>
                                <div class="small text-muted" id="diskVolIdNote"></div>
                            </form>
                        </div>
                        <div class="modal-footer">
                            <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Cancel</button>
                            <button type="button" class="btn btn-primary" id="btnDiskVolSave" onclick="DiskMonitor.saveVolume()"><i class="fas fa-save me-1"></i>Save volume</button>
                        </div>
                    </div>
                </div>
            </div>`;
    },

    renderDetectModal() {
        return `
            <div class="modal fade" id="diskDetectModal" tabindex="-1">
                <div class="modal-dialog modal-xl">
                    <div class="modal-content">
                        <div class="modal-header"><h5 class="modal-title">Detect volumes</h5><button type="button" class="btn-close" data-bs-dismiss="modal"></button></div>
                        <div class="modal-body">
                            <div class="row align-items-end mb-3">
                                <div class="col-md-5"><label class="form-label">Host</label><select class="form-select" id="diskDetectHost"></select></div>
                                <div class="col-md-3"><div class="form-check mb-2"><input class="form-check-input" type="checkbox" id="diskDetectSudo"><label class="form-check-label" for="diskDetectSudo">Use <code>sudo -n</code></label></div></div>
                                <div class="col-md-4 text-md-end"><button type="button" class="btn btn-primary" id="btnDiskDetectScan" onclick="DiskMonitor.runDetect()"><i class="fas fa-search me-1"></i>Scan</button></div>
                            </div>
                            <div id="diskDetectResults"><p class="text-muted mb-0">Choose a host and click Scan. Real disks that are not monitored yet are pre-selected; tmpfs, overlay and other pseudo filesystems are left unchecked.</p></div>
                        </div>
                        <div class="modal-footer">
                            <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Close</button>
                            <button type="button" class="btn btn-success" id="btnDiskDetectAdd" onclick="DiskMonitor.bulkAdd()" disabled><i class="fas fa-plus me-1"></i>Add selected</button>
                        </div>
                    </div>
                </div>
            </div>`;
    },

    renderTestModal() {
        return `
            <div class="modal fade" id="diskTestModal" tabindex="-1">
                <div class="modal-dialog modal-lg">
                    <div class="modal-content">
                        <div class="modal-header"><h5 class="modal-title">Test host connection</h5><button type="button" class="btn-close" data-bs-dismiss="modal"></button></div>
                        <div class="modal-body">
                            <div class="row align-items-end mb-3">
                                <div class="col-md-7"><label class="form-label">Host</label><select class="form-select" id="diskTestHost"></select></div>
                                <div class="col-md-5 text-md-end"><button type="button" class="btn btn-primary" id="btnDiskTestRun" onclick="DiskMonitor.runTest()"><i class="fas fa-plug me-1"></i>Run df now</button></div>
                            </div>
                            <div class="form-text mb-3">Runs <code>df</code> for the host's configured volumes right away (or a plain <code>df</code> when none are configured), updates their status and writes a log file.</div>
                            <div id="diskTestResults"></div>
                        </div>
                        <div class="modal-footer"><button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Close</button></div>
                    </div>
                </div>
            </div>`;
    },

    // ----- Data -----

    async loadData() {
        try {
            const [state, settings] = await Promise.all([API.diskMonitor.getState(), API.diskMonitor.getSettings()]);
            this.state = state.data || {};
            this.settings = settings.data || {};
            this.hosts = this.settings.hosts || [];
            this.renderAll();
        } catch (error) {
            console.error('Disk monitor load error:', error);
            Utils.showError(error.message || 'Failed to load disk monitor');
            $('#diskMonTable').html(`<div class="alert alert-danger">${this.escape(error.message || 'Failed to load disk monitor')}</div>`);
        }
    },

    async refreshState() {
        if (this.refreshing) return;
        this.refreshing = true;
        try {
            const state = await API.diskMonitor.getState();
            this.state = state.data || {};
            this.renderBanners();
            this.renderSummary();
            this.renderTable();
        } catch (error) {
            console.warn('Disk monitor refresh failed:', error.message);
        } finally {
            this.refreshing = false;
        }
    },

    renderAll() {
        this.renderBanners();
        this.renderSummary();
        this.renderHostFilter();
        this.renderTable();
        this.renderSettingsForm();
    },

    startPolling() {
        this.stopPolling();
        this.pollTimer = setInterval(() => {
            if (document.hidden || $('.modal.show').length) return;
            this.refreshState();
        }, 15000);
    },

    stopPolling() {
        if (this.pollTimer) { clearInterval(this.pollTimer); this.pollTimer = null; }
    },

    // ----- Banners / summary -----

    renderBanners() {
        const s = this.state || {};
        const banners = [];
        if (!s.enabled) {
            banners.push(`<div class="alert alert-secondary d-flex align-items-center gap-2 py-2"><i class="fas fa-pause-circle"></i><span>The disk monitor is <strong>disabled</strong> in Settings. Checks and alerts are paused.</span></div>`);
        }
        if (s.enabled && !s.smtp_ready) {
            const reason = (this.settings && this.settings.recipients && (this.settings.recipients.to || []).length) ? 'SMTP is not configured or disabled.' : 'No alert recipients are configured.';
            banners.push(`<div class="alert alert-warning d-flex align-items-center gap-2 py-2"><i class="fas fa-envelope-open-text"></i><span>Alerts are not being emailed: ${reason} Set up <a href="#smtp" data-page="smtp">SMTP Settings</a> and recipients in the Settings card below.</span></div>`);
        }
        if (s.last_email_error) {
            banners.push(`<div class="alert alert-danger d-flex align-items-center gap-2 py-2"><i class="fas fa-exclamation-triangle"></i><span>${this.escape(s.last_email_error)}${s.last_email_error_at ? ` <span class="text-muted">(${this.escape(s.last_email_error_at)})</span>` : ''}</span></div>`);
        }
        $('#diskMonBanners').html(banners.join(''));
    },

    renderSummary() {
        const sum = (this.state && this.state.summary) || {};
        const volumes = (this.state && this.state.volumes) || [];
        const metrics = [
            ['fa-hdd', 'Volumes', volumes.length, 'primary'],
            ['fa-check-circle', 'OK', sum.ok || 0, 'success'],
            ['fa-exclamation-circle', 'Tripped', sum.tripped || 0, sum.tripped ? 'danger' : 'primary'],
            ['fa-unlink', 'Check failed', sum.unknown || 0, sum.unknown ? 'warning' : 'primary'],
            ['fa-pause', 'Disabled', sum.disabled || 0, 'primary'],
            ['fa-hourglass-half', 'Never checked', sum.never || 0, 'primary']
        ];
        $('#diskMonSummary').html(metrics.map(([icon, label, value, tone]) => `<div class="commands-summary-item"><span class="commands-summary-icon commands-summary-icon-${tone}"><i class="fas ${icon}"></i></span><span><small>${label}</small><strong>${value}</strong></span></div>`).join(''));
        const parts = [];
        if (this.state && this.state.next_due) parts.push(`Next check due ${this.escape(this.state.next_due)}`);
        if (this.state && this.state.last_check_at) parts.push(`last check ${this.escape(this.state.last_check_at)}`);
        if (this.state && this.state.last_email_sent_at) parts.push(`last email ${this.escape(this.state.last_email_sent_at)}`);
        if (this.state && (this.state.in_flight_hosts || []).length) parts.push(`checking now: ${this.state.in_flight_hosts.map(h => this.escape(h)).join(', ')}`);
        $('#diskMonRefreshNote').html(`<i class="fas fa-sync-alt me-1"></i>Refreshes every 15 seconds while this tab is open${parts.length ? ' · ' + parts.join(' · ') : ''}`);
    },

    renderHostFilter() {
        const select = $('#diskMonHostFilter');
        const current = this.filters.host;
        const refs = [...new Set(((this.state && this.state.volumes) || []).map(v => v.host_ref))].sort((a, b) => a.localeCompare(b));
        select.html(`<option value="all">All hosts</option>${refs.map(r => `<option value="${this.escape(r)}">${this.escape(r)}</option>`).join('')}`);
        select.val(refs.includes(current) ? current : 'all');
    },

    updateFilter(name, value) {
        this.filters[name] = name === 'search' ? String(value || '').trim().toLowerCase() : value;
        this.renderTable();
    },

    clearFilters() {
        this.filters = { search: '', host: 'all', status: 'all' };
        $('#diskMonSearch').val('');
        $('#diskMonHostFilter').val('all');
        $('#diskMonStatusFilter').val('all');
        this.renderTable();
    },

    // ----- Table -----

    statusBadge(v) {
        switch (v.status) {
            case 'OK': return '<span class="badge bg-success">OK</span>';
            case 'TRIPPED': return '<span class="badge bg-danger">TRIPPED</span>';
            case 'UNKNOWN': return '<span class="badge bg-warning text-dark">CHECK FAILED</span>';
            case 'disabled': return '<span class="badge bg-secondary">DISABLED</span>';
            default: return '<span class="badge bg-light text-dark border">NEVER CHECKED</span>';
        }
    },

    sparklineSVG(history) {
        const w = 120, h = 24, pad = 2;
        if (!history || !history.length) return `<svg width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" class="diskmon-spark"></svg>`;
        const innerH = h - 2 * pad, innerW = w - 2 * pad;
        const step = innerW / Math.max(history.length - 1, 1);
        const points = history.map((r, i) => {
            const pct = Math.max(0, Math.min(100, Number(r.pct_free) || 0));
            return `${(pad + i * step).toFixed(1)},${(pad + (1 - pct / 100) * innerH).toFixed(1)}`;
        }).join(' ');
        const last = Number(history[history.length - 1].pct_free) || 0;
        const color = last < 20 ? '#dc2626' : last < 40 ? '#f59e0b' : '#16a34a';
        const title = `${history.length} reading(s), latest ${last.toFixed(1)}% free`;
        return `<svg width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" class="diskmon-spark" role="img" aria-label="${title}"><title>${title}</title><polyline fill="none" stroke="${color}" stroke-width="1.5" points="${points}"/></svg>`;
    },

    percentBar(v) {
        if (!v.has_reading) return '<span class="text-muted">—</span>';
        const pct = Math.max(0, Math.min(100, Number(v.percent_free) || 0));
        const tone = v.status === 'TRIPPED' ? 'bg-danger' : pct < 20 ? 'bg-warning' : 'bg-success';
        return `<div class="diskmon-pct"><div class="progress" title="${pct.toFixed(1)}% free"><div class="progress-bar ${tone}" style="width:${pct.toFixed(1)}%"></div></div><small>${pct.toFixed(1)}% free</small></div>`;
    },

    relative(value) {
        if (!value) return '<span class="text-muted">never</span>';
        return `<span title="${this.escape(value)}">${this.escape(Utils.formatRelativeTime(value))}</span>`;
    },

    getFiltered() {
        const { search, host, status } = this.filters;
        return ((this.state && this.state.volumes) || []).filter(v => {
            const matchesSearch = !search || [v.host_ref, v.host_address, v.mount_path, v.description].some(x => String(x || '').toLowerCase().includes(search));
            const matchesHost = host === 'all' || v.host_ref === host;
            const matchesStatus = status === 'all' || v.status === status;
            return matchesSearch && matchesHost && matchesStatus;
        });
    },

    renderTable() {
        const container = $('#diskMonTable');
        const all = (this.state && this.state.volumes) || [];
        if (!all.length) {
            container.html(`<div class="commands-empty-state"><i class="fas fa-hdd fa-3x text-muted mb-3"></i><p class="text-muted">No volumes are monitored yet.</p><div class="d-flex gap-2 justify-content-center"><button class="btn btn-outline-primary" onclick="DiskMonitor.showDetectModal()"><i class="fas fa-search me-1"></i> Detect volumes on a host</button><button class="btn btn-primary" onclick="DiskMonitor.showAddModal()"><i class="fas fa-plus me-1"></i> Add a volume</button></div></div>`);
            return;
        }
        const rows = this.getFiltered();
        if (!rows.length) {
            container.html('<div class="commands-table-card p-4 text-center text-muted">No volumes match the current filters.</div>');
            return;
        }
        const html = rows.map(v => {
            const id = this.escape(v.volume_id);
            const hostCell = `<div class="fw-semibold">${this.escape(v.host_ref)}${v.use_sudo ? ' <i class="fas fa-user-shield text-muted" title="df runs with sudo -n"></i>' : ''}</div><small class="text-muted">${v.host_missing ? '<span class="text-danger">host missing</span>' : this.escape(v.host_address)}</small>`;
            const free = v.has_reading ? `<div>${this.escape(Utils.formatFileSize(v.last_free_bytes))}</div><small class="text-muted">of ${this.escape(Utils.formatFileSize(v.last_total_bytes))}</small>` : '<span class="text-muted">—</span>';
            const next = v.enabled ? (v.in_flight ? '<span class="text-primary"><i class="fas fa-spinner fa-spin me-1"></i>checking</span>' : this.relative(v.next_due)) : '<span class="text-muted">paused</span>';
            const actions = `<div class="btn-group btn-group-sm" role="group">
                <button class="btn btn-outline-primary" type="button" title="Check now" ${v.enabled ? '' : 'disabled'} onclick="DiskMonitor.checkNow('${id}')"><i class="fas fa-play"></i></button>
                <button class="btn btn-outline-secondary" type="button" title="View last log" ${v.last_trace_id ? '' : 'disabled'} onclick="DiskMonitor.viewLog('${this.escape(v.last_trace_id || '')}')"><i class="fas fa-file-alt"></i></button>
                <button class="btn btn-outline-secondary" type="button" title="Edit" onclick="DiskMonitor.edit('${id}')"><i class="fas fa-edit"></i></button>
                <button class="btn btn-outline-danger" type="button" title="Delete" onclick="DiskMonitor.remove('${id}')"><i class="fas fa-trash"></i></button>
            </div>`;
            const errorRow = v.last_error && v.status !== 'OK' ? `<tr class="diskmon-error-row"><td colspan="10"><i class="fas fa-exclamation-triangle text-warning me-1"></i>${this.escape(v.last_error)}</td></tr>` : '';
            return `<tr class="diskmon-row is-${this.escape((v.status || '').toLowerCase())}">
                <td>${hostCell}</td>
                <td><span class="diskmon-mount">${this.escape(v.mount_path)}</span>${v.description ? `<br><small class="text-muted">${this.escape(v.description)}</small>` : ''}</td>
                <td>${free}</td>
                <td>${this.escape(v.threshold_label)}${v.threshold_bytes ? `<br><small class="text-muted">${this.escape(Utils.formatFileSize(v.threshold_bytes))}</small>` : ''}</td>
                <td>${this.percentBar(v)}</td>
                <td>${this.statusBadge(v)}${v.pending_recovery ? '<br><small class="text-success">recovered, not yet reported</small>' : ''}</td>
                <td>${this.relative(v.last_checked_at)}</td>
                <td>${next}<br><small class="text-muted">every ${this.escape(v.check_interval)}</small></td>
                <td>${this.sparklineSVG(v.history)}</td>
                <td>${actions}</td>
            </tr>${errorRow}`;
        }).join('');
        container.html(`<div class="commands-table-card"><div class="commands-table-scroll"><table class="commands-table diskmon-table">
            <thead><tr><th>Host</th><th>Mount</th><th>Free</th><th>Threshold</th><th>% free</th><th>Status</th><th>Last check</th><th>Next check</th><th>Trend (last 24)</th><th>Actions</th></tr></thead>
            <tbody>${html}</tbody></table></div></div>`);
    },

    // ----- Settings -----

    renderSettingsForm() {
        const s = this.settings || {};
        const rec = s.recipients || {};
        $('#diskMonSettingsForm').html(`
            <form id="diskMonSettings" onsubmit="return false">
                <div class="row">
                    <div class="col-md-3 mb-3"><div class="form-check form-switch mt-4"><input class="form-check-input" type="checkbox" id="diskSetEnabled" ${s.enabled !== false ? 'checked' : ''}><label class="form-check-label" for="diskSetEnabled"><strong>Monitoring enabled</strong></label></div></div>
                    <div class="col-md-3 mb-3"><label class="form-label">Alert once every</label><input type="text" class="form-control" id="diskSetAlertEvery" value="${this.escape(s.send_alert_once_every || '4h')}"><div class="form-text">Cooldown per volume (1m – 30d), e.g. 4h.</div></div>
                    <div class="col-md-3 mb-3"><label class="form-label">Daily summary time</label><input type="time" class="form-control" id="diskSetSummaryTime" value="${this.escape(s.daily_summary_time || '08:00')}"><div class="form-text">Server local time.</div></div>
                    <div class="col-md-3 mb-3"><div class="form-check form-switch mt-4"><input class="form-check-input" type="checkbox" id="diskSetSummaryEnabled" ${s.daily_summary_enabled !== false ? 'checked' : ''}><label class="form-check-label" for="diskSetSummaryEnabled">Send daily summary</label></div></div>
                </div>
                <div class="row">
                    <div class="col-md-6 mb-3"><label class="form-label">Recipients (To)</label><input type="text" class="form-control" id="diskSetTo" value="${this.escape((rec.to || []).join(', '))}" placeholder="ops@example.com, admin@example.com"><div class="form-text">Comma-separated. Required for alerts and the daily summary.</div></div>
                    <div class="col-md-6 mb-3"><label class="form-label">Recipients (CC)</label><input type="text" class="form-control" id="diskSetCc" value="${this.escape((rec.cc || []).join(', '))}" placeholder="Optional"></div>
                </div>
                <div class="row">
                    <div class="col-md-12 mb-3"><label class="form-label" for="diskSetSubject">Email subject</label><input type="text" class="form-control" id="diskSetSubject" maxlength="200" value="${this.escape(s.email_subject || this.DEFAULT_SUBJECT)}" placeholder="${this.escape(this.DEFAULT_SUBJECT)}"><div class="form-text">Used for alerts, the daily summary and test emails. Placeholders: <code>{{summary}}</code> (e.g. "CRITICAL: 3 volumes affected on 2 host(s)"), <code>{{type}}</code> (Alert, Recovered, Daily summary, Test email), <code>{{tripped}}</code>, <code>{{failed}}</code>, <code>{{ok}}</code>, <code>{{volumes}}</code>, <code>{{hosts}}</code>, <code>{{app_host}}</code> and your config variables. Empty = <code>${this.escape(this.DEFAULT_SUBJECT)}</code>.</div></div>
                </div>
                <div class="row">
                    <div class="col-md-3 mb-3"><label class="form-label">Default check interval</label><input type="text" class="form-control" id="diskSetInterval" value="${this.escape(s.default_check_interval || '1h')}"></div>
                    <div class="col-md-3 mb-3"><label class="form-label">Default threshold</label><div class="input-group"><input type="number" class="form-control" id="diskSetThreshold" min="0" step="any" value="${this.escape(s.default_threshold_value ?? 5)}"><select class="form-select" id="diskSetUnit" style="max-width:6rem">${this.UNITS.map(u => `<option value="${u}" ${u === (s.default_threshold_unit || 'GB') ? 'selected' : ''}>${u}</option>`).join('')}</select></div></div>
                    <div class="col-md-3 mb-3"><label class="form-label">Max concurrent host checks</label><input type="number" class="form-control" id="diskSetConcurrency" min="1" max="100" value="${this.escape(s.max_concurrent_host_checks || 10)}"></div>
                    <div class="col-md-3 mb-3"><label class="form-label">Email bar shows</label><select class="form-select" id="diskSetBarMode"><option value="free" ${s.email_bar_mode !== 'used' ? 'selected' : ''}>% free</option><option value="used" ${s.email_bar_mode === 'used' ? 'selected' : ''}>% used</option></select></div>
                </div>
                <div class="d-flex flex-wrap gap-2 align-items-center">
                    <button type="button" class="btn btn-primary" id="btnDiskSetSave" onclick="DiskMonitor.saveSettings()"><i class="fas fa-save me-1"></i>Save settings</button>
                    <button type="button" class="btn btn-outline-secondary" id="btnDiskSetTestEmail" onclick="DiskMonitor.sendTestEmail()" ${s.smtp_ready ? '' : 'disabled'} title="${s.smtp_ready ? 'Send the current fleet status to the recipients' : 'Configure SMTP and recipients first'}"><i class="fas fa-paper-plane me-1"></i>Send test email</button>
                    <span class="small text-muted">SMTP: ${s.smtp_ready ? '<span class="text-success">ready</span>' : '<span class="text-warning">not ready</span>'} · Alert checks run every minute; volumes are checked on their own interval.</span>
                </div>
            </form>
        `);
    },

    collectSettings() {
        const list = sel => $(sel).val().split(',').map(x => x.trim()).filter(Boolean);
        const to = list('#diskSetTo');
        const cc = list('#diskSetCc');
        for (const addr of [...to, ...cc]) {
            if (!Utils.isValidEmail(addr)) { Utils.showError(`Invalid email address: ${addr}`); return null; }
        }
        const threshold = Number($('#diskSetThreshold').val());
        if (!(threshold > 0)) { Utils.showError('Default threshold must be greater than 0'); return null; }
        return {
            enabled: $('#diskSetEnabled').is(':checked'),
            send_alert_once_every: $('#diskSetAlertEvery').val().trim() || '4h',
            daily_summary_enabled: $('#diskSetSummaryEnabled').is(':checked'),
            daily_summary_time: $('#diskSetSummaryTime').val() || '08:00',
            default_check_interval: $('#diskSetInterval').val().trim() || '1h',
            default_threshold_value: threshold,
            default_threshold_unit: $('#diskSetUnit').val(),
            max_concurrent_host_checks: Number($('#diskSetConcurrency').val()) || 10,
            email_bar_mode: $('#diskSetBarMode').val(),
            email_subject: $('#diskSetSubject').val().trim() || this.DEFAULT_SUBJECT,
            recipients: { to, cc }
        };
    },

    async saveSettings() {
        const data = this.collectSettings();
        if (!data) return;
        try {
            Utils.showLoading();
            const response = await API.diskMonitor.saveSettings(data);
            this.settings = response.data || this.settings;
            this.hosts = this.settings.hosts || this.hosts;
            Utils.showSuccess('Disk monitor settings saved');
            await this.refreshState();
            this.renderSettingsForm();
        } catch (error) {
            Utils.showError(error.message || 'Failed to save settings');
        } finally {
            Utils.hideLoading();
        }
    },

    async sendTestEmail() {
        try {
            $('#btnDiskSetTestEmail').prop('disabled', true);
            await API.diskMonitor.testEmail();
            Utils.showSuccess('Test email sent');
            await this.refreshState();
        } catch (error) {
            Utils.showError(error.message || 'Test email failed');
        } finally {
            $('#btnDiskSetTestEmail').prop('disabled', !(this.settings && this.settings.smtp_ready));
        }
    },

    // ----- Volume add / edit / delete -----

    showAddModal() {
        if (!this.hosts.length) { Utils.showError('Add an SSH host on the Hosts page first'); return; }
        this.editingId = null;
        const s = this.settings || {};
        $('#diskVolumeModalTitle').text('Add volume');
        $('#diskVolHost').html(this.hostOptions(this.filters.host !== 'all' ? this.filters.host : '')).prop('disabled', false);
        $('#diskVolMount').val('');
        $('#diskVolThreshold').val(s.default_threshold_value ?? 5);
        $('#diskVolUnit').val(s.default_threshold_unit || 'GB');
        $('#diskVolInterval').val('').attr('placeholder', s.default_check_interval || '1h');
        $('#diskVolDescription').val('');
        $('#diskVolSudo').prop('checked', false);
        $('#diskVolEnabled').prop('checked', true);
        $('#diskVolIdNote').text('');
        $('#diskVolumeModal').modal('show');
    },

    edit(id) {
        const v = ((this.state && this.state.volumes) || []).find(x => x.volume_id === id);
        if (!v) return;
        this.editingId = id;
        $('#diskVolumeModalTitle').text(`Edit volume ${v.mount_path} on ${v.host_ref}`);
        $('#diskVolHost').html(this.hostOptions(v.host_ref) + (this.hosts.some(h => h.host_id === v.host_ref) ? '' : `<option value="${this.escape(v.host_ref)}" selected>${this.escape(v.host_ref)} (missing)</option>`));
        $('#diskVolMount').val(v.mount_path);
        $('#diskVolThreshold').val(v.threshold_value);
        $('#diskVolUnit').val(v.threshold_unit);
        $('#diskVolInterval').val(v.check_interval || '');
        $('#diskVolDescription').val(v.description || '');
        $('#diskVolSudo').prop('checked', !!v.use_sudo);
        $('#diskVolEnabled').prop('checked', !!v.enabled);
        $('#diskVolIdNote').text(`Volume ID: ${v.volume_id}`);
        $('#diskVolumeModal').modal('show');
    },

    collectVolume() {
        const mount = $('#diskVolMount').val().trim();
        if (!mount.startsWith('/')) { Utils.showError('Mount path must be an absolute path starting with /'); return null; }
        const threshold = Number($('#diskVolThreshold').val());
        if (!(threshold > 0)) { Utils.showError('Threshold must be greater than 0'); return null; }
        const unit = $('#diskVolUnit').val();
        if (unit === '%' && threshold >= 100) { Utils.showError('Percent threshold must be below 100'); return null; }
        return {
            host_ref: $('#diskVolHost').val(),
            mount_path: mount,
            description: $('#diskVolDescription').val().trim(),
            check_interval: $('#diskVolInterval').val().trim(),
            threshold_value: threshold,
            threshold_unit: unit,
            use_sudo: $('#diskVolSudo').is(':checked'),
            enabled: $('#diskVolEnabled').is(':checked')
        };
    },

    async saveVolume() {
        const data = this.collectVolume();
        if (!data) return;
        try {
            Utils.showLoading();
            $('#btnDiskVolSave').prop('disabled', true);
            if (this.editingId) await API.diskMonitor.updateVolume(this.editingId, data);
            else await API.diskMonitor.createVolume(data);
            Utils.showSuccess(this.editingId ? 'Volume updated' : 'Volume added; first check runs within a few seconds');
            $('#diskVolumeModal').modal('hide');
            await this.loadData();
        } catch (error) {
            Utils.showError(error.message || 'Failed to save volume');
        } finally {
            $('#btnDiskVolSave').prop('disabled', false);
            Utils.hideLoading();
        }
    },

    async remove(id) {
        const v = ((this.state && this.state.volumes) || []).find(x => x.volume_id === id);
        const label = v ? `${v.mount_path} on ${v.host_ref}` : id;
        if (!await Utils.confirm('Delete volume', `Stop monitoring ${label}? Its history will be discarded.`, 'Delete')) return;
        try {
            await API.diskMonitor.deleteVolume(id);
            Utils.showSuccess('Volume deleted');
            await this.loadData();
        } catch (error) {
            Utils.showError(error.message || 'Failed to delete volume');
        }
    },

    async checkNow(id) {
        try {
            await API.diskMonitor.checkVolume(id);
            Utils.showSuccess('Check queued');
            setTimeout(() => this.refreshState(), 3000);
            setTimeout(() => this.refreshState(), 12000);
        } catch (error) {
            Utils.showError(error.message || 'Failed to queue check');
        }
    },

    viewLog(traceId) {
        if (!traceId) return;
        if (window.Logs && typeof Logs.viewLog === 'function') Logs.viewLog(traceId);
        else window.open(`/api/logs/${encodeURIComponent(traceId)}`, '_blank');
    },

    // ----- Detect volumes -----

    showDetectModal() {
        if (!this.hosts.length) { Utils.showError('Add an SSH host on the Hosts page first'); return; }
        $('#diskDetectHost').html(this.hostOptions(this.filters.host !== 'all' ? this.filters.host : ''));
        $('#diskDetectSudo').prop('checked', false);
        $('#diskDetectResults').html('<p class="text-muted mb-0">Choose a host and click Scan. Real disks that are not monitored yet are pre-selected; tmpfs, overlay and other pseudo filesystems are left unchecked.</p>');
        $('#btnDiskDetectAdd').prop('disabled', true);
        this.detect = { hostRef: '', rows: [], useSudo: false };
        $('#diskDetectModal').modal('show');
    },

    async runDetect() {
        const hostRef = $('#diskDetectHost').val();
        const useSudo = $('#diskDetectSudo').is(':checked');
        const results = $('#diskDetectResults');
        results.html('<div class="text-center py-4"><i class="fas fa-spinner fa-spin fa-2x text-primary"></i><p class="mt-2 text-muted">Running df on the host...</p></div>');
        $('#btnDiskDetectScan').prop('disabled', true);
        $('#btnDiskDetectAdd').prop('disabled', true);
        try {
            const response = await API.diskMonitor.detect(hostRef, useSudo);
            const data = response.data || {};
            this.detect = { hostRef, rows: data.rows || [], useSudo, defaults: data };
            if (!this.detect.rows.length) { results.html('<div class="alert alert-warning mb-0">df returned no mounts.</div>'); return; }
            const unit = data.default_threshold_unit || 'GB';
            const rows = this.detect.rows.map((r, i) => `
                <tr class="${r.already_monitored ? 'table-secondary text-muted' : ''}">
                    <td><input class="form-check-input" type="checkbox" id="diskDetectRow${i}" ${r.precheck ? 'checked' : ''} ${r.already_monitored ? 'disabled' : ''} onchange="DiskMonitor.updateDetectAddButton()"></td>
                    <td class="font-monospace small">${this.escape(r.filesystem)}</td>
                    <td>${this.escape(Utils.formatFileSize(r.total_bytes))}</td>
                    <td>${this.escape(Utils.formatFileSize(r.used_bytes))}</td>
                    <td>${this.escape(Utils.formatFileSize(r.free_bytes))}</td>
                    <td>${Math.round(r.pct_used)}%</td>
                    <td class="diskmon-mount">${this.escape(r.mount_path)}${r.already_monitored ? ' <span class="badge bg-secondary ms-1">monitored</span>' : ''}${!r.real_disk && !r.already_monitored ? ' <span class="badge bg-light text-muted border ms-1">pseudo fs</span>' : ''}</td>
                    <td><input type="number" class="form-control form-control-sm" id="diskDetectThr${i}" min="0" step="any" value="${this.escape(data.default_threshold_value ?? 5)}" ${r.already_monitored ? 'disabled' : ''} style="min-width:5rem"></td>
                    <td><select class="form-select form-select-sm" id="diskDetectUnit${i}" ${r.already_monitored ? 'disabled' : ''}>${this.UNITS.map(u => `<option value="${u}" ${u === unit ? 'selected' : ''}>${u}</option>`).join('')}</select></td>
                </tr>`).join('');
            results.html(`<div class="table-responsive"><table class="table table-sm align-middle mb-0">
                <thead><tr><th>Add</th><th>Filesystem</th><th>Size</th><th>Used</th><th>Avail</th><th>Use%</th><th>Mounted on</th><th>Threshold</th><th>Unit</th></tr></thead>
                <tbody>${rows}</tbody></table></div>
                <div class="form-text mt-2">New volumes use the default check interval (${this.escape(data.default_check_interval || '1h')})${useSudo ? ' and run df with sudo -n' : ''}.</div>`);
            this.updateDetectAddButton();
        } catch (error) {
            results.html(`<div class="alert alert-danger mb-0"><i class="fas fa-exclamation-triangle me-1"></i>${this.escape(error.message || 'Scan failed')}</div>`);
        } finally {
            $('#btnDiskDetectScan').prop('disabled', false);
        }
    },

    updateDetectAddButton() {
        const count = this.detect.rows.filter((r, i) => !r.already_monitored && $(`#diskDetectRow${i}`).is(':checked')).length;
        $('#btnDiskDetectAdd').prop('disabled', count === 0).html(`<i class="fas fa-plus me-1"></i>Add ${count ? count + ' selected' : 'selected'}`);
    },

    async bulkAdd() {
        const volumes = [];
        this.detect.rows.forEach((r, i) => {
            if (r.already_monitored || !$(`#diskDetectRow${i}`).is(':checked')) return;
            volumes.push({ mount_path: r.mount_path, threshold_value: Number($(`#diskDetectThr${i}`).val()), threshold_unit: $(`#diskDetectUnit${i}`).val() });
        });
        if (!volumes.length) return;
        try {
            Utils.showLoading();
            const response = await API.diskMonitor.bulkCreate({ host_ref: this.detect.hostRef, use_sudo: this.detect.useSudo, volumes });
            const data = response.data || {};
            const skipped = data.skipped || [];
            Utils.showSuccess(response.error || `${(data.created || []).length} volume(s) added`);
            if (skipped.length) Utils.showErrorDetails('Some mounts were skipped', skipped.map(s => `${s.mount_path}: ${s.reason}`).join('\n'));
            $('#diskDetectModal').modal('hide');
            await this.loadData();
        } catch (error) {
            Utils.showError(error.message || 'Failed to add volumes');
        } finally {
            Utils.hideLoading();
        }
    },

    // ----- Test host -----

    showTestModal(hostRef) {
        if (!this.hosts.length) { Utils.showError('Add an SSH host on the Hosts page first'); return; }
        $('#diskTestHost').html(this.hostOptions(hostRef || (this.filters.host !== 'all' ? this.filters.host : '')));
        $('#diskTestResults').html('');
        $('#diskTestModal').modal('show');
    },

    async runTest() {
        const hostRef = $('#diskTestHost').val();
        const out = $('#diskTestResults');
        out.html('<div class="text-center py-3"><i class="fas fa-spinner fa-spin fa-2x text-primary"></i><p class="mt-2 text-muted">Connecting and running df...</p></div>');
        $('#btnDiskTestRun').prop('disabled', true);
        try {
            const response = await API.diskMonitor.testHost(hostRef);
            const data = response.data || {};
            const rows = (data.results || []).map(r => `<tr><td class="diskmon-mount">${this.escape(r.mount_path)}</td><td>${r.error ? '<span class="badge bg-warning text-dark">FAILED</span>' : r.status === 'TRIPPED' ? '<span class="badge bg-danger">TRIPPED</span>' : '<span class="badge bg-success">OK</span>'}</td><td>${r.error ? `<span class="text-danger small">${this.escape(r.error)}</span>` : `${this.escape(Utils.formatFileSize(r.free_bytes))} free of ${this.escape(Utils.formatFileSize(r.total_bytes))}`}</td></tr>`).join('');
            out.html(`<div class="alert ${data.ok ? 'alert-success' : 'alert-warning'} py-2">${this.escape(response.error || '')} <span class="text-muted">(${data.duration_ms || 0} ms)</span>${data.trace_id ? ` · <a href="#" onclick="DiskMonitor.viewLog('${this.escape(data.trace_id)}');return false;">view log</a>` : ''}</div>
                <div class="table-responsive"><table class="table table-sm mb-0"><thead><tr><th>Mount</th><th>Status</th><th>Result</th></tr></thead><tbody>${rows || '<tr><td colspan="3" class="text-muted">No mounts reported</td></tr>'}</tbody></table></div>`);
            this.refreshState();
        } catch (error) {
            out.html(`<div class="alert alert-danger mb-0"><i class="fas fa-exclamation-triangle me-1"></i>${this.escape(error.message || 'Test failed')}</div>`);
        } finally {
            $('#btnDiskTestRun').prop('disabled', false);
        }
    },

    cleanup() {
        this.stopPolling();
        $('#diskVolumeModal, #diskDetectModal, #diskTestModal').modal('hide');
    }
};

window.DiskMonitor = DiskMonitor;
