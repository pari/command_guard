// Server Status Component
const Status = {
    // Selected top tab: 'server' (default), 'requests' or 'runbooks'. Whichever tab is
    // selected reloads every 2 s; opening a tab fetches fresh data right away.
    activeTab: 'server',
    // Selected "Upcoming" tab: 'jobs' (default), 'runbooks' or 'http'. Not live-reloaded.
    upcomingTab: 'jobs',
    livePollTimer: null,
    livePollInFlight: false,
    LIVE_POLL_INTERVAL_MS: 2000,

    async init() {
        this.activeTab = 'server';
        this.upcomingTab = 'jobs';
        this.stopLivePolling();
        this.render();
        this.bindEvents();
        await this.loadStatus();
    },

    render() {
        $('#mainContent').html(`
            <div class="status-container">
                <div class="page-header mb-3 px-3">
                    <h5 class="mb-0 d-flex align-items-center">
                        <i class="fas fa-info-circle me-2"></i> Server Status
                    </h5>
                    <button class="btn btn-outline-primary btn-sm" type="button" onclick="Status.refresh()">
                        <i class="fas fa-sync-alt me-1"></i> Refresh
                    </button>
                </div>
                <div id="statusContent" class="px-3">
                    <div class="text-center py-5 text-muted">
                        <i class="fas fa-spinner fa-spin fa-2x text-primary"></i>
                        <p class="mt-3 mb-0">Loading server status...</p>
                    </div>
                </div>
            </div>
        `);
    },

    // Tab switches: load the newly selected tab immediately, then keep reloading every 2 s.
    bindEvents() {
        $('#mainContent').off('.statusTabs').on('shown.bs.tab.statusTabs', '[data-status-tab]', (event) => {
            this.activeTab = event.currentTarget.dataset.statusTab || 'server';
            this.startLivePolling();
        }).on('shown.bs.tab.statusTabs', '[data-upcoming-tab]', (event) => {
            // The Upcoming tabs only switch view; they refresh with the page / Refresh button.
            this.upcomingTab = event.currentTarget.dataset.upcomingTab || 'jobs';
        });
    },

    async refresh() {
        await this.loadStatus();
    },

    async loadStatus() {
        try {
            const response = await API.ops.getStatus();

            if (!response.success || !response.data) {
                throw new Error('Status request was unsuccessful');
            }

            $('#statusContent').html(this.renderStatus(response.data));
            this.setLiveUpdatedTime();
            this.scheduleLivePolling();
        } catch (error) {
            console.error('Error fetching status:', error);
            $('#statusContent').html(`
                <div class="alert alert-danger mb-0" role="alert">
                    <i class="fas fa-exclamation-triangle me-2"></i>
                    Failed to fetch server status. Please try again.
                </div>
            `);
            Utils.showError('Failed to fetch server status');
        }
    },

    startLivePolling() {
        this.stopLivePolling();
        this.pollActive();
        this.livePollTimer = setInterval(() => this.pollActive(), this.LIVE_POLL_INTERVAL_MS);
    },

    // Keep reloading every 2 s without an extra immediate fetch (used right after a full load).
    scheduleLivePolling() {
        this.stopLivePolling();
        this.livePollTimer = setInterval(() => this.pollActive(), this.LIVE_POLL_INTERVAL_MS);
    },

    stopLivePolling() {
        if (this.livePollTimer) {
            clearInterval(this.livePollTimer);
            this.livePollTimer = null;
        }
    },

    // Reload only the live parts (active lists, tab counts, Server Information) in place.
    async pollActive() {
        if (this.livePollInFlight || document.hidden || !document.getElementById('statusLiveTabs')) {
            return;
        }
        this.livePollInFlight = true;
        try {
            const response = await API.ops.getStatus();
            if (!response.success || !response.data) {
                throw new Error('Status request was unsuccessful');
            }
            this.updateLiveSections(response.data);
        } catch (error) {
            console.error('Error reloading active status:', error);
            $('.status-live-updated').html('<span class="text-danger"><i class="fas fa-exclamation-triangle me-1"></i>Could not reload, retrying…</span>');
        } finally {
            this.livePollInFlight = false;
        }
    },

    updateLiveSections(data) {
        const activeRequests = data.active_requests || [];
        const activeRunBooks = data.active_runbooks || [];
        $('#statusServerInfoBody').html(this.renderServerInfo(data));
        $('#statusActiveRequestsBody').html(this.renderActiveRequests(activeRequests));
        $('#statusActiveRunBooksBody').html(this.renderActiveRunBooks(activeRunBooks));
        $('#statusActiveRequestsCount').text(activeRequests.length);
        $('#statusActiveRunBooksCount').text(activeRunBooks.length);
        this.setLiveUpdatedTime();
    },

    setLiveUpdatedTime() {
        const now = new Date();
        const pad = (n) => String(n).padStart(2, '0');
        const time = `${pad(now.getHours())}:${pad(now.getMinutes())}:${pad(now.getSeconds())}`;
        $('.status-live-updated').html(`<i class="fas fa-circle text-success me-1" style="font-size:0.5rem;vertical-align:middle"></i>Live · reloads every 2 seconds while this tab is open · updated ${time}`);
    },

    renderStatus(data) {
        const activeRequests = data.active_requests || [];
        const activeRunBooks = data.active_runbooks || [];
        const scheduledJobs = data.scheduled_jobs || [];
        const scheduledRunBooks = data.scheduled_runbooks || [];
        const scheduledHTTPRequests = data.scheduled_http_requests || [];
        const tab = this.activeTab;
        const tabButton = (key, target, icon, label, countId, count) => `
            <li class="nav-item" role="presentation">
                <button class="nav-link ${tab === key ? 'active' : ''}" type="button" role="tab" data-bs-toggle="tab" data-bs-target="#${target}" data-status-tab="${key}" aria-controls="${target}" aria-selected="${tab === key}">
                    <i class="fas ${icon} me-1"></i>${label}${countId ? ` <span class="badge rounded-pill bg-secondary ms-1" id="${countId}">${count}</span>` : ''}
                </button>
            </li>`;
        const pane = (key, id) => `tab-pane fade ${tab === key ? 'show active' : ''}" id="${id}" role="tabpanel`;
        const upcoming = this.upcomingTab;
        const upcomingTabButton = (key, target, icon, label, count) => `
            <li class="nav-item" role="presentation">
                <button class="nav-link ${upcoming === key ? 'active' : ''}" type="button" role="tab" data-bs-toggle="tab" data-bs-target="#${target}" data-upcoming-tab="${key}" aria-controls="${target}" aria-selected="${upcoming === key}">
                    <i class="fas ${icon} me-1"></i>${label} <span class="badge rounded-pill bg-secondary ms-1">${count}</span>
                </button>
            </li>`;
        const upcomingPane = (key, id) => `tab-pane fade ${upcoming === key ? 'show active' : ''}" id="${id}" role="tabpanel`;

        return `
            <div class="row g-3">
                <div class="col-12">
                    <ul class="nav nav-tabs mb-3" id="statusLiveTabs" role="tablist">
                        ${tabButton('server', 'statusTabServer', 'fa-server', 'Server Information')}
                        ${tabButton('requests', 'statusTabRequests', 'fa-tasks', 'Active Requests', 'statusActiveRequestsCount', activeRequests.length)}
                        ${tabButton('runbooks', 'statusTabRunBooks', 'fa-book-open', 'Active RunBooks', 'statusActiveRunBooksCount', activeRunBooks.length)}
                    </ul>
                    <div class="tab-content">
                        <div class="${pane('server', 'statusTabServer')}">
                            <div class="card">
                                <div class="card-body p-0">
                                    <div id="statusServerInfoBody">${this.renderServerInfo(data)}</div>
                                    <div class="small text-muted px-3 py-2 status-live-updated"></div>
                                </div>
                            </div>
                        </div>
                        <div class="${pane('requests', 'statusTabRequests')}">
                            <div class="card">
                                <div class="card-body">
                                    <div id="statusActiveRequestsBody">${this.renderActiveRequests(activeRequests)}</div>
                                    <div class="small text-muted mt-2 status-live-updated"></div>
                                </div>
                            </div>
                        </div>
                        <div class="${pane('runbooks', 'statusTabRunBooks')}">
                            <div class="card">
                                <div class="card-body">
                                    <div id="statusActiveRunBooksBody">${this.renderActiveRunBooks(activeRunBooks)}</div>
                                    <div class="small text-muted mt-2 status-live-updated"></div>
                                </div>
                            </div>
                        </div>
                    </div>
                </div>
                <div class="col-12">
                    <h5 class="mt-3 mb-2 d-flex align-items-center" id="statusUpcomingHeading"><i class="fas fa-calendar-alt me-2"></i>Upcoming</h5>
                    <ul class="nav nav-tabs mb-3" id="statusUpcomingTabs" role="tablist" aria-labelledby="statusUpcomingHeading">
                        ${upcomingTabButton('jobs', 'statusUpcomingJobs', 'fa-clock', 'Scheduled Jobs', scheduledJobs.length)}
                        ${upcomingTabButton('runbooks', 'statusUpcomingRunBooks', 'fa-book', 'Scheduled RunBooks', scheduledRunBooks.length)}
                        ${upcomingTabButton('http', 'statusUpcomingHttp', 'fa-globe', 'Scheduled HTTP Requests', scheduledHTTPRequests.length)}
                    </ul>
                    <div class="tab-content">
                        <div class="${upcomingPane('jobs', 'statusUpcomingJobs')}">
                            <div class="card">
                                <div class="card-body p-0">${this.renderScheduleTable(scheduledJobs, 'Command ID', data.current_time)}</div>
                            </div>
                        </div>
                        <div class="${upcomingPane('runbooks', 'statusUpcomingRunBooks')}">
                            <div class="card">
                                <div class="card-body p-0">${this.renderScheduleTable(scheduledRunBooks, 'RunBook ID', data.current_time)}</div>
                            </div>
                        </div>
                        <div class="${upcomingPane('http', 'statusUpcomingHttp')}">
                            <div class="card">
                                <div class="card-body p-0">${this.renderScheduleTable(scheduledHTTPRequests, 'Request ID', data.current_time)}</div>
                            </div>
                        </div>
                    </div>
                </div>
            </div>
        `;
    },

    renderServerInfo(data) {
        const activeRequests = data.active_requests || [];
        return `
            <table class="table table-sm mb-0">
                <tbody>
                    <tr><td><strong>Version:</strong></td><td>${data.version || 'N/A'}</td></tr>
                    <tr><td><strong>Uptime:</strong></td><td>${data.server_uptime || 'N/A'}</td></tr>
                    <tr><td><strong>Total Requests:</strong></td><td>${data.total_requests_handled || 0}</td></tr>
                    <tr><td><strong>Active Requests:</strong></td><td>${activeRequests.length}</td></tr>
                    <tr><td><strong>Cron Enabled:</strong></td><td><span class="badge ${data.cron_enabled ? 'bg-success' : 'bg-secondary'}">${data.cron_enabled ? 'Yes' : 'No'}</span></td></tr>
                    <tr><td><strong>Disk Monitor:</strong></td><td>${this.renderDiskMonitorSummary(data.disk_monitor)}</td></tr>
                    <tr><td><strong>Current Time:</strong></td><td>${data.current_time || 'N/A'}</td></tr>
                </tbody>
            </table>
        `;
    },

    renderDiskMonitorSummary(dm) {
        if (!dm || !dm.configured) return '<span class="text-muted">Not configured</span> <a href="#diskmonitor" data-page="diskmonitor">open</a>';
        const badge = dm.enabled ? '<span class="badge bg-success">Enabled</span>' : '<span class="badge bg-secondary">Disabled</span>';
        const parts = [`${dm.volumes} volume(s)`, `${dm.ok} OK`];
        if (dm.tripped) parts.push(`<span class="text-danger">${dm.tripped} tripped</span>`);
        if (dm.unknown) parts.push(`<span class="text-warning">${dm.unknown} failed</span>`);
        if (dm.disabled) parts.push(`${dm.disabled} disabled`);
        if ((dm.in_flight_hosts || []).length) parts.push(`checking ${dm.in_flight_hosts.map(h => Utils.escapeHtml(h)).join(', ')}`);
        if (dm.next_due) parts.push(`next due ${Utils.escapeHtml(dm.next_due)}`);
        if (!dm.smtp_ready) parts.push('<span class="text-warning">email not ready</span>');
        return `${badge} ${parts.join(' · ')} <a href="#diskmonitor" data-page="diskmonitor">open</a>`;
    },

    renderActiveRequests(requests) {
        if (requests.length === 0) {
            return '<p class="text-muted mb-0">No active requests</p>';
        }

        return `<div class="list-group">${requests.map(request => `
            <div class="list-group-item">
                <div class="d-flex justify-content-between align-items-center gap-3">
                    <span>${Utils.escapeHtml(request.cmd_id || 'Unknown')}</span>
                    <small class="text-muted">${Utils.escapeHtml(request.running_for || request.started_at || 'Running')}</small>
                </div>
            </div>
        `).join('')}</div>`;
    },

    renderActiveRunBooks(runBooks) {
        if (runBooks.length === 0) {
            return '<p class="text-muted mb-0">No active RunBooks</p>';
        }

        return `<div class="list-group">${runBooks.map(run => `<div class="list-group-item d-flex justify-content-between"><span>${Utils.escapeHtml(run.runbook_id || '')}</span><span class="badge bg-primary">Step ${run.current_step} of ${run.total_steps}</span></div>`).join('')}</div>`;
    },

    // Next Run label: "Today 05:33 PM" / "Tomorrow 01:05 AM" for the server's today and
    // tomorrow, otherwise "YYYY-MM-DD hh:mm AM/PM" (e.g. "2026-10-19 04:00 PM"). Both inputs are server-local
    // strings ("YYYY-MM-DD HH:mm:ss[.SSS]"), parsed without timezone conversion.
    formatNextRun(nextRun, serverNow) {
        if (!nextRun) return 'N/A';
        const match = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})/.exec(String(nextRun));
        if (!match) return String(nextRun);
        const [, year, month, day, hourText, minute] = match;
        const runDate = `${year}-${month}-${day}`;
        const nowMatch = /^(\d{4})-(\d{2})-(\d{2})/.exec(String(serverNow || ''));
        const today = nowMatch ? `${nowMatch[1]}-${nowMatch[2]}-${nowMatch[3]}` : dayjs().format('YYYY-MM-DD');
        const [ty, tm, td] = today.split('-').map(Number);
        const tomorrow = new Date(Date.UTC(ty, tm - 1, td + 1)).toISOString().slice(0, 10);
        const hour = Number(hourText);
        const clock = `${String(hour % 12 === 0 ? 12 : hour % 12).padStart(2, '0')}:${minute} ${hour < 12 ? 'AM' : 'PM'}`;
        if (runDate === today) return `Today ${clock}`;
        if (runDate === tomorrow) return `Tomorrow ${clock}`;
        return `${runDate} ${clock}`;
    },

    renderScheduleTable(items, idLabel, serverNow) {
        if (items.length === 0) {
            return '<p class="text-muted p-3 mb-0">No scheduled items</p>';
        }

        return `
            <div class="table-responsive">
                <table class="table table-sm mb-0">
                    <thead><tr><th>${idLabel}</th><th>Schedule</th><th>Next Run</th></tr></thead>
                    <tbody>${items.map(item => `
                        <tr>
                            <td>${item.cmd_id || 'N/A'}</td>
                            <td><code>${item.cron_schedule || 'N/A'}</code></td>
                            <td${item.next_run ? ` title="${Utils.escapeHtml(item.next_run)}"` : ''}>${Utils.escapeHtml(this.formatNextRun(item.next_run, serverNow))}</td>
                        </tr>
                    `).join('')}</tbody>
                </table>
            </div>
        `;
    },

    // Called by the Router when leaving the Status page.
    cleanup() {
        this.stopLivePolling();
        this.livePollInFlight = false;
        $('#mainContent').off('.statusTabs');
    }
};

window.Status = Status;
