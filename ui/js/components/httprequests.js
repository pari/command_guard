// HTTP Requests Component: saved API calls (Postman-style) that run manually, by API, by cron or as RunBook steps.
const HttpRequests = {
    METHODS: ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'],
    MASK: '••••',
    items: [],
    runBooks: [],
    editingId: null,
    listState: { search: '', method: 'all', schedule: 'all', sortKey: 'request_id', sortDirection: 'asc', page: 1, pageSize: 10 },
    activeActionsMenu: null,
    form: { queryParams: [], headers: [], formFields: [], assertions: [], hasAuthSecret: false },

    escape(value) {
        return String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#039;' })[c]);
    },

    generateId() {
        const now = new Date();
        const pad = n => String(n).padStart(2, '0');
        const base = `req-${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}-${pad(now.getHours())}${pad(now.getMinutes())}${pad(now.getSeconds())}`;
        let candidate = base;
        let suffix = 1;
        while (this.items.some(item => item.request_id === candidate)) candidate = `${base}-${suffix++}`;
        return candidate;
    },

    async init() {
        this.items = [];
        this.runBooks = [];
        this.editingId = null;
        this.listState = { search: '', method: 'all', schedule: 'all', sortKey: 'request_id', sortDirection: 'asc', page: 1, pageSize: 10 };
        this.closeRowActionsMenu();
        this.render();
        await this.loadData();
    },

    render() {
        $('#mainContent').html(`
            <div class="commands-page httprequests-page">
                <div class="commands-page-header">
                    <div>
                        <h1>HTTP Requests</h1>
                        <p>Save, schedule and monitor API calls with headers, auth, bodies, retries and response checks.</p>
                    </div>
                    <button class="btn btn-primary commands-add-button" onclick="HttpRequests.showAddModal()"><i class="fas fa-plus me-2"></i>New request</button>
                </div>
                <div class="commands-toolbar httprequests-toolbar" aria-label="HTTP request filters">
                    <div class="commands-search-wrap"><i class="fas fa-search" aria-hidden="true"></i><input type="search" class="form-control" id="httpRequestsSearch" placeholder="Search requests..." aria-label="Search HTTP requests" oninput="HttpRequests.updateSearch(this.value)"></div>
                    <div class="commands-filter-field"><label for="httpRequestsMethodFilter">Method</label><select class="form-select" id="httpRequestsMethodFilter" onchange="HttpRequests.updateFilter('method', this.value)"><option value="all">All methods</option>${this.METHODS.map(m => `<option value="${m}">${m}</option>`).join('')}</select></div>
                    <div class="commands-filter-field"><label for="httpRequestsScheduleFilter">Schedule</label><select class="form-select" id="httpRequestsScheduleFilter" onchange="HttpRequests.updateFilter('schedule', this.value)"><option value="all">All schedules</option><option value="scheduled">Scheduled</option><option value="manual">Manual / No schedule</option><option value="daily">Daily</option><option value="weekly">Weekly</option><option value="monthly">Monthly</option><option value="custom">Custom cron</option></select></div>
                    <button class="btn commands-clear-button" type="button" onclick="HttpRequests.clearFilters()"><i class="fas fa-filter me-2"></i>Clear filters</button>
                </div>
                <div id="httpRequestsSummary" class="commands-summary" aria-label="HTTP requests summary"></div>
                <div id="httpRequestsListContainer"><div class="text-center py-5"><i class="fas fa-spinner fa-spin fa-2x text-primary"></i><p class="mt-3 text-muted">Loading HTTP requests...</p></div></div>
            </div>
            ${this.renderEditModal()}
            ${this.renderRunModal()}
        `);
    },

    renderEditModal() {
        return `
            <div class="modal fade" id="httpRequestModal" tabindex="-1">
                <div class="modal-dialog modal-xl">
                    <div class="modal-content">
                        <div class="modal-header"><h5 class="modal-title" id="httpRequestModalTitle">Add HTTP Request</h5><button type="button" class="btn-close" data-bs-dismiss="modal"></button></div>
                        <div class="modal-body">
                            <form id="httpRequestForm" onsubmit="return false">
                                <ul class="nav nav-tabs mb-3" role="tablist">
                                    <li class="nav-item" role="presentation"><button class="nav-link active" id="httpreq-request-tab" data-bs-toggle="tab" data-bs-target="#httpreq-request" type="button" role="tab"><i class="fas fa-globe me-2"></i>Request</button></li>
                                    <li class="nav-item" role="presentation"><button class="nav-link" id="httpreq-auth-tab" data-bs-toggle="tab" data-bs-target="#httpreq-auth" type="button" role="tab"><i class="fas fa-key me-2"></i>Auth</button></li>
                                    <li class="nav-item" role="presentation"><button class="nav-link" id="httpreq-settings-tab" data-bs-toggle="tab" data-bs-target="#httpreq-settings" type="button" role="tab"><i class="fas fa-check-double me-2"></i>Settings &amp; Checks</button></li>
                                    <li class="nav-item" role="presentation"><button class="nav-link" id="httpreq-schedule-tab" data-bs-toggle="tab" data-bs-target="#httpreq-schedule" type="button" role="tab"><i class="fas fa-clock me-2"></i>Scheduling &amp; Security</button></li>
                                    <li class="nav-item" role="presentation"><button class="nav-link" id="httpreq-notify-tab" data-bs-toggle="tab" data-bs-target="#httpreq-notify" type="button" role="tab"><i class="fas fa-bell me-2"></i>Notifications</button></li>
                                    <li class="nav-item" role="presentation"><button class="nav-link" id="recentlogs-tab" data-bs-toggle="tab" data-bs-target="#httpreq-logs" type="button" role="tab"><i class="fas fa-file-alt me-2"></i>Recent Logs</button></li>
                                </ul>
                                <div class="tab-content">
                                    <div class="tab-pane fade show active" id="httpreq-request" role="tabpanel">
                                        <div class="row">
                                            <div class="col-md-5 mb-3"><label class="form-label">Request ID *</label><input type="text" class="form-control" id="httpReqId" required pattern="[a-zA-Z0-9-]+" title="Only letters, numbers and hyphens"><div class="form-text" id="httpReqIdHelp">Unique ID shared with Command IDs - cannot be changed after creation</div></div>
                                            <div class="col-md-7 mb-3"><label class="form-label">Description</label><input type="text" class="form-control" id="httpReqDescription" placeholder="What does this request do?"></div>
                                        </div>
                                        <div class="row">
                                            <div class="col-md-3 mb-3"><label class="form-label">Method *</label><select class="form-select font-monospace" id="httpReqMethod">${this.METHODS.map(m => `<option value="${m}">${m}</option>`).join('')}</select></div>
                                            <div class="col-md-9 mb-3"><label class="form-label">URL *</label><input type="text" class="form-control font-monospace" id="httpReqUrl" required placeholder="https://api.example.com/v1/health"><div class="form-text">Absolute http(s) URL. Config variables <code>{{name}}</code> and <code>\${{ENV}}</code> are substituted at load time.</div></div>
                                        </div>
                                        <div class="row">
                                            <div class="col-lg-6 mb-3">
                                                <div class="d-flex justify-content-between align-items-center mb-1"><label class="form-label mb-0">Query parameters</label><button type="button" class="btn btn-sm btn-outline-primary" onclick="HttpRequests.addRow('queryParams')"><i class="fas fa-plus me-1"></i>Add</button></div>
                                                <div id="httpReqQueryParams"></div>
                                            </div>
                                            <div class="col-lg-6 mb-3">
                                                <div class="d-flex justify-content-between align-items-center mb-1"><label class="form-label mb-0">Headers</label><button type="button" class="btn btn-sm btn-outline-primary" onclick="HttpRequests.addRow('headers')"><i class="fas fa-plus me-1"></i>Add</button></div>
                                                <div id="httpReqHeaders"></div>
                                                <div class="form-text">Authorization, Cookie and API-key header values are stored but shown masked (${this.MASK}); leave the mask in place to keep the saved value.</div>
                                            </div>
                                        </div>
                                        <div class="row">
                                            <div class="col-md-3 mb-3"><label class="form-label">Body</label><select class="form-select" id="httpReqBodyType" onchange="HttpRequests.onBodyTypeChange()"><option value="none">None</option><option value="json">JSON</option><option value="text">Text</option><option value="xml">XML</option><option value="form">Form (urlencoded)</option></select></div>
                                            <div class="col-md-9 mb-3">
                                                <div id="httpReqBodyRaw" style="display:none"><label class="form-label">Body content</label><textarea class="form-control font-monospace" id="httpReqBodyContent" rows="7" placeholder='{"key": "value"}'></textarea><div class="form-text">Content-Type is set automatically unless you add a Content-Type header.</div></div>
                                                <div id="httpReqBodyForm" style="display:none"><div class="d-flex justify-content-between align-items-center mb-1"><label class="form-label mb-0">Form fields</label><button type="button" class="btn btn-sm btn-outline-primary" onclick="HttpRequests.addRow('formFields')"><i class="fas fa-plus me-1"></i>Add</button></div><div id="httpReqFormFields"></div></div>
                                            </div>
                                        </div>
                                        <div class="row"><div class="col-md-4 mb-3"><label class="form-label">Log Storage</label><select class="form-select" id="httpReqLogStorage"><option value="local">Local</option><option value="s3">S3</option></select><div class="form-text">S3 uploads the completed log file and removes the local copy after upload succeeds</div></div></div>
                                    </div>
                                    <div class="tab-pane fade" id="httpreq-auth" role="tabpanel">
                                        <div class="row">
                                            <div class="col-md-4 mb-3"><label class="form-label">Auth type</label><select class="form-select" id="httpReqAuthType" onchange="HttpRequests.onAuthTypeChange()"><option value="none">None</option><option value="basic">Basic</option><option value="bearer">Bearer token</option><option value="api_key">API key</option></select></div>
                                        </div>
                                        <div id="httpReqAuthBasic" class="row" style="display:none">
                                            <div class="col-md-6 mb-3"><label class="form-label">Username *</label><input type="text" class="form-control" id="httpReqAuthUsername" autocomplete="off"></div>
                                            <div class="col-md-6 mb-3"><label class="form-label">Password</label><input type="password" class="form-control" id="httpReqAuthPassword" autocomplete="new-password"><div class="form-text httpreq-secret-help"></div></div>
                                        </div>
                                        <div id="httpReqAuthBearer" class="row" style="display:none">
                                            <div class="col-md-8 mb-3"><label class="form-label">Token *</label><input type="password" class="form-control" id="httpReqAuthToken" autocomplete="new-password"><div class="form-text httpreq-secret-help"></div></div>
                                            <div class="col-md-4 mb-3"><label class="form-label">Prefix</label><input type="text" class="form-control" id="httpReqAuthPrefix" placeholder="Bearer"><div class="form-text">Sent as <code>Authorization: &lt;prefix&gt; &lt;token&gt;</code></div></div>
                                        </div>
                                        <div id="httpReqAuthApiKey" class="row" style="display:none">
                                            <div class="col-md-4 mb-3"><label class="form-label">Key name *</label><input type="text" class="form-control" id="httpReqAuthKey" placeholder="X-API-Key"></div>
                                            <div class="col-md-5 mb-3"><label class="form-label">Value *</label><input type="password" class="form-control" id="httpReqAuthValue" autocomplete="new-password"><div class="form-text httpreq-secret-help"></div></div>
                                            <div class="col-md-3 mb-3"><label class="form-label">Add to</label><select class="form-select" id="httpReqAuthAddTo"><option value="header">Header</option><option value="query">Query string</option></select></div>
                                        </div>
                                        <div class="alert alert-info small mb-0">Secrets are never returned by the API. When editing, leave a secret field empty to keep the stored value. Changing the auth type requires entering the new secret.</div>
                                    </div>
                                    <div class="tab-pane fade" id="httpreq-settings" role="tabpanel">
                                        <div class="row">
                                            <div class="col-md-3 mb-3"><label class="form-label">Timeout (seconds)</label><input type="number" class="form-control" id="httpReqTimeout" min="1" max="600" placeholder="30"></div>
                                            <div class="col-md-3 mb-3"><label class="form-label">Retries</label><input type="number" class="form-control" id="httpReqRetryCount" min="0" max="10" placeholder="0"><div class="form-text">Retried on network errors and 5xx responses</div></div>
                                            <div class="col-md-3 mb-3"><label class="form-label">Retry delay (seconds)</label><input type="number" class="form-control" id="httpReqRetryDelay" min="0" placeholder="0"></div>
                                            <div class="col-md-3 mb-3"><label class="form-label">Max Output Size</label><input type="text" class="form-control" id="httpReqMaxOutput" placeholder="2MB"><div class="form-text">Response body kept in memory / API (log keeps full body)</div></div>
                                        </div>
                                        <div class="row">
                                            <div class="col-md-3 mb-3"><div class="form-check mt-4"><input class="form-check-input" type="checkbox" id="httpReqFollowRedirects" checked><label class="form-check-label" for="httpReqFollowRedirects">Follow redirects</label></div></div>
                                            <div class="col-md-3 mb-3"><label class="form-label">Max redirects</label><input type="number" class="form-control" id="httpReqMaxRedirects" min="1" max="50" placeholder="10"></div>
                                            <div class="col-md-3 mb-3"><div class="form-check mt-4"><input class="form-check-input" type="checkbox" id="httpReqSkipTls"><label class="form-check-label" for="httpReqSkipTls">Skip TLS certificate verification</label></div></div>
                                            <div class="col-md-3 mb-3"><label class="form-label">Expected status</label><input type="text" class="form-control font-monospace" id="httpReqExpectedStatus" placeholder="2xx"><div class="form-text">e.g. <code>2xx</code>, <code>200,201</code>, <code>200-299,404</code></div></div>
                                        </div>
                                        <div class="d-flex justify-content-between align-items-center mb-1"><label class="form-label mb-0">Assertions</label><button type="button" class="btn btn-sm btn-outline-primary" onclick="HttpRequests.addRow('assertions')"><i class="fas fa-plus me-1"></i>Add</button></div>
                                        <div id="httpReqAssertions"></div>
                                        <div class="form-text">Any failed check marks the run as failed (exit code 1). JSON paths use dots: <code>data.items.0.id</code>.</div>
                                    </div>
                                    <div class="tab-pane fade" id="httpreq-schedule" role="tabpanel">
                                        <div class="row">
                                            <div class="col-md-6 mb-3"><label class="form-label">Schedule (Cron)</label><input type="text" class="form-control font-monospace" id="httpReqSchedule" placeholder="e.g., */5 * * * *"><div class="form-text">Leave empty to disable scheduling</div></div>
                                            <div class="col-md-6 mb-3"><label class="form-label">Bearer Token</label><input type="text" class="form-control" id="httpReqBearerToken" placeholder="Optional security token"><div class="form-text">Required for external API execution via <code>/api/http-requests/{id}</code></div></div>
                                        </div>
                                        <div class="row">
                                            <div class="col-md-6 mb-3"><label class="form-label">Webhook URL</label><input type="url" class="form-control" id="httpReqWebhook" placeholder="https://example.com/webhook"><div class="form-text">POST notification after execution (includes http_status)</div></div>
                                            <div class="col-md-6 mb-3"><div class="form-check mt-4"><input class="form-check-input" type="checkbox" id="httpReqSkipIfRunning"><label class="form-check-label" for="httpReqSkipIfRunning"><strong>Skip if already running</strong></label><div class="form-text">New runs are skipped while a previous run of this request is still in progress.</div></div></div>
                                        </div>
                                        <div class="alert alert-info"><h6 class="alert-heading">Common Cron Patterns</h6><div class="small"><code>*/5 * * * *</code> - Every 5 minutes<br><code>0 * * * *</code> - Every hour<br><code>0 0 * * *</code> - Daily at midnight<br><code>0 2 * * *</code> - Daily at 2 AM<br><code>0 0 * * 1</code> - Weekly on Monday<br><code>0 0 1 * *</code> - Monthly on 1st</div></div>
                                    </div>
                                    <div class="tab-pane fade" id="httpreq-notify" role="tabpanel">
                                        <h6 class="mb-3">Email Notification Settings</h6>
                                        <div class="row">
                                            <div class="col-md-6 mb-3"><label class="form-label">To (comma-separated)</label><input type="text" class="form-control" id="httpReqEmailTo" placeholder="email1@example.com, email2@example.com"><div class="form-text">Leave empty to disable email notifications</div></div>
                                            <div class="col-md-6 mb-3"><label class="form-label">Subject</label><input type="text" class="form-control" id="httpReqEmailSubject" placeholder="{{status}} - {{request_id}} ({{host}})"><div class="form-text">Variables: {{status}}, {{request_id}}, {{host}}, {{trace_id}}</div></div>
                                        </div>
                                        <div class="row mb-3">
                                            <div class="col-md-4"><div class="form-check"><input class="form-check-input" type="checkbox" id="httpReqEmailOnSuccess" checked><label class="form-check-label" for="httpReqEmailOnSuccess">Send on Success</label></div></div>
                                            <div class="col-md-4"><div class="form-check"><input class="form-check-input" type="checkbox" id="httpReqEmailOnFailure" checked><label class="form-check-label" for="httpReqEmailOnFailure">Send on Failure</label></div></div>
                                            <div class="col-md-4"><div class="form-check"><input class="form-check-input" type="checkbox" id="httpReqEmailIncludeOutput" checked><label class="form-check-label" for="httpReqEmailIncludeOutput">Include Response Body</label></div></div>
                                        </div>
                                        <div class="row"><div class="col-md-6 mb-3"><label class="form-label">Max Output Lines</label><input type="number" class="form-control" id="httpReqEmailMaxOutputLines" min="0" placeholder="e.g., 100"></div></div>
                                    </div>
                                    <div class="tab-pane fade" id="httpreq-logs" role="tabpanel"><div id="recentLogsContainer"></div></div>
                                </div>
                            </form>
                        </div>
                        <div class="modal-footer">
                            <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Cancel</button>
                            <button type="button" class="btn btn-success" id="btnHttpReqSaveAndRun" onclick="HttpRequests.save(true)"><i class="fas fa-play"></i> Save &amp; Run</button>
                            <button type="button" class="btn btn-primary" id="btnHttpReqSave" onclick="HttpRequests.save(false)"><i class="fas fa-save"></i> Save Request</button>
                        </div>
                    </div>
                </div>
            </div>`;
    },

    renderRunModal() {
        return `
            <div class="modal fade" id="httpRequestRunModal" tabindex="-1">
                <div class="modal-dialog modal-xl">
                    <div class="modal-content">
                        <div class="modal-header"><h5 class="modal-title">HTTP Request Result</h5><button type="button" class="btn-close" data-bs-dismiss="modal"></button></div>
                        <div class="modal-body"><div id="httpRequestRunOutput"></div></div>
                        <div class="modal-footer"><button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Close</button></div>
                    </div>
                </div>
            </div>`;
    },

    async loadData() {
        try {
            const [requests, config] = await Promise.all([API.httpRequests.list(), API.config.get()]);
            this.items = requests.data || [];
            this.runBooks = config.data?.runbooks || [];
            this.renderList();
        } catch (error) {
            console.error('HTTP requests load error:', error);
            Utils.showError(error.message || 'Failed to load HTTP requests');
        }
    },

    getRunBookCount(id) {
        return this.runBooks.filter(runBook => (runBook.steps || []).some(step => step.request_id === id)).length;
    },

    updateSearch(value) { this.listState.search = value.trim().toLowerCase(); this.listState.page = 1; this.renderList(); },
    updateFilter(name, value) { if (['method', 'schedule'].includes(name)) { this.listState[name] = value; this.listState.page = 1; this.renderList(); } },
    clearFilters() {
        Object.assign(this.listState, { search: '', method: 'all', schedule: 'all', page: 1 });
        $('#httpRequestsSearch').val('');
        $('#httpRequestsMethodFilter').val('all');
        $('#httpRequestsScheduleFilter').val('all');
        this.renderList();
    },

    getScheduleType(schedule) { return Commands.getScheduleType(schedule); },

    getFiltered() {
        const { search, method, schedule } = this.listState;
        const filtered = this.items.filter(item => {
            const matchesSearch = !search || [item.request_id, item.description, item.url, item.method].some(v => String(v || '').toLowerCase().includes(search));
            const matchesMethod = method === 'all' || (item.method || 'GET').toUpperCase() === method;
            const type = this.getScheduleType(item.cron_schedule);
            const matchesSchedule = schedule === 'all' || (schedule === 'scheduled' ? type !== 'manual' : type === schedule);
            return matchesSearch && matchesMethod && matchesSchedule;
        });
        const direction = this.listState.sortDirection === 'asc' ? 1 : -1;
        return filtered.sort((a, b) => {
            const key = this.listState.sortKey;
            const aValue = key === 'runbooks' ? this.getRunBookCount(a.request_id) : (a[key] || '');
            const bValue = key === 'runbooks' ? this.getRunBookCount(b.request_id) : (b[key] || '');
            if (typeof aValue === 'number') return (aValue - bValue) * direction;
            return String(aValue).localeCompare(String(bValue)) * direction;
        });
    },

    sort(key) {
        if (this.listState.sortKey === key) this.listState.sortDirection = this.listState.sortDirection === 'asc' ? 'desc' : 'asc';
        else { this.listState.sortKey = key; this.listState.sortDirection = 'asc'; }
        this.listState.page = 1;
        this.renderList();
    },
    sortIcon(key) {
        if (this.listState.sortKey !== key) return '<i class="fas fa-sort"></i>';
        return `<i class="fas fa-sort-${this.listState.sortDirection === 'asc' ? 'up' : 'down'}"></i>`;
    },
    changePage(page) {
        const pageCount = Math.max(1, Math.ceil(this.getFiltered().length / this.listState.pageSize));
        this.listState.page = Math.min(Math.max(1, page), pageCount);
        this.renderList();
    },
    changePageSize(value) { this.listState.pageSize = Number(value) || 10; this.listState.page = 1; this.renderList(); },

    renderSummary() {
        const scheduled = this.items.filter(item => this.getScheduleType(item.cron_schedule) !== 'manual').length;
        const withChecks = this.items.filter(item => (item.assertions || []).length || (item.expected_status && item.expected_status !== '2xx')).length;
        const hosts = new Set(this.items.map(item => { try { return new URL(item.url).host; } catch (_) { return ''; } }).filter(Boolean)).size;
        const metrics = [
            ['fa-globe', 'Total requests', this.items.length, 'primary'],
            ['fa-clock', 'Scheduled', scheduled, 'primary'],
            ['fa-hand-pointer', 'Manual', this.items.length - scheduled, 'primary'],
            ['fa-book', 'RunBook references', this.items.reduce((sum, item) => sum + this.getRunBookCount(item.request_id), 0), 'primary'],
            ['fa-check-double', 'With checks', withChecks, 'primary'],
            ['fa-server', 'Target hosts', hosts, 'success']
        ];
        $('#httpRequestsSummary').html(metrics.map(([icon, label, value, tone]) => `<div class="commands-summary-item"><span class="commands-summary-icon commands-summary-icon-${tone}"><i class="fas ${icon}"></i></span><span><small>${label}</small><strong>${value}</strong></span></div>`).join(''));
    },

    methodBadge(method) {
        const m = String(method || 'GET').toUpperCase();
        return `<span class="http-method-badge is-${m.toLowerCase()}">${this.escape(m)}</span>`;
    },

    renderList() {
        this.closeRowActionsMenu();
        this.renderSummary();
        const container = $('#httpRequestsListContainer');
        if (!this.items.length) {
            container.html(`<div class="commands-empty-state"><i class="fas fa-globe fa-3x text-muted mb-3"></i><p class="text-muted">No HTTP requests configured yet.</p><button class="btn btn-primary" onclick="HttpRequests.showAddModal()"><i class="fas fa-plus me-1"></i> Add Your First Request</button></div>`);
            return;
        }
        const filtered = this.getFiltered();
        const pageCount = Math.max(1, Math.ceil(filtered.length / this.listState.pageSize));
        if (this.listState.page > pageCount) this.listState.page = pageCount;
        const start = (this.listState.page - 1) * this.listState.pageSize;
        const pageItems = filtered.slice(start, start + this.listState.pageSize);
        let lastPage = 0;
        const pagination = Array.from({ length: pageCount }, (_, i) => i + 1)
            .filter(page => page === 1 || page === pageCount || Math.abs(page - this.listState.page) <= 1)
            .map(page => { const gap = lastPage && page - lastPage > 1 ? '<span class="commands-page-gap">…</span>' : ''; lastPage = page; return `${gap}<button class="commands-page-number ${page === this.listState.page ? 'active' : ''}" onclick="HttpRequests.changePage(${page})">${page}</button>`; }).join('');

        const rows = pageItems.map(item => {
            const id = this.escape(item.request_id);
            const runBookCount = this.getRunBookCount(item.request_id);
            const icons = [
                item.has_bearer_token || item.bearer_token ? '<i class="fas fa-key" title="Bearer token protected"></i>' : '',
                item.auth && item.auth.type && item.auth.type !== 'none' ? `<i class="fas fa-user-lock" title="Auth: ${this.escape(item.auth.type)}"></i>` : '',
                item.notify_webhook ? '<i class="fas fa-globe" title="Webhook configured"></i>' : '',
                item.notify_email ? '<i class="fas fa-envelope" title="Email notification configured"></i>' : '',
                (item.assertions || []).length ? `<i class="fas fa-check-double" title="${(item.assertions || []).length} assertion(s)"></i>` : '',
                item.log_storage === 's3' ? '<i class="fas fa-cloud-upload-alt" title="Logs uploaded to S3"></i>' : ''
            ].filter(Boolean).join('');
            return `<tr>
                <td><button type="button" class="commands-command-name d-block border-0 bg-transparent p-0 text-start" data-request-id="${id}" onclick="HttpRequests.edit(this.dataset.requestId)" title="Edit ${id}">${id}</button>${icons ? `<div class="commands-feature-icons">${icons}</div>` : ''}</td>
                <td><span class="commands-description-text">${this.escape(item.description || 'No description')}</span></td>
                <td>${this.methodBadge(item.method)}</td>
                <td><span class="http-url-text" title="${this.escape(item.url)}">${this.escape(item.url)}</span></td>
                <td>${item.cron_schedule ? `<span class="commands-schedule-badge"><i class="far fa-clock"></i>${this.escape(Commands.formatCronSchedule(item.cron_schedule))}</span>` : '<span class="commands-schedule-badge is-manual">None</span>'}</td>
                <td class="commands-number-cell">${runBookCount}</td>
                <td><span class="commands-status-badge ${item.cron_schedule ? 'is-scheduled' : 'is-manual'}"><span></span>${item.cron_schedule ? 'Scheduled' : 'Manual'}</span></td>
                <td><div class="commands-row-actions">
                    <button class="btn commands-run-button" type="button" onclick="HttpRequests.run('${id}')"><i class="fas fa-play"></i><span>Run</span></button>
                    <button class="btn commands-more-button" type="button" data-request-id="${id}" aria-expanded="false" aria-haspopup="menu" aria-label="Actions for ${id}" onclick="HttpRequests.toggleRowActions(this, this.dataset.requestId, event)"><i class="fas fa-ellipsis-v"></i></button>
                </div></td>
            </tr>`;
        }).join('');

        container.html(`
            <div class="commands-table-card"><div class="commands-table-scroll"><table class="commands-table httprequests-table">
                <thead><tr>
                    <th><button onclick="HttpRequests.sort('request_id')">Request ${this.sortIcon('request_id')}</button></th>
                    <th><button onclick="HttpRequests.sort('description')">Description ${this.sortIcon('description')}</button></th>
                    <th><button onclick="HttpRequests.sort('method')">Method ${this.sortIcon('method')}</button></th>
                    <th><button onclick="HttpRequests.sort('url')">URL ${this.sortIcon('url')}</button></th>
                    <th><button onclick="HttpRequests.sort('cron_schedule')">Schedule ${this.sortIcon('cron_schedule')}</button></th>
                    <th><button onclick="HttpRequests.sort('runbooks')">RunBooks ${this.sortIcon('runbooks')}</button></th>
                    <th>Status</th><th>Actions</th>
                </tr></thead>
                <tbody>${rows || '<tr><td colspan="8"><div class="commands-no-results"><i class="fas fa-search"></i><strong>No requests found</strong><span>Try changing or clearing the filters.</span></div></td></tr>'}</tbody>
            </table></div>
            <div class="commands-table-footer">
                <div><span>${filtered.length ? `Showing ${start + 1} to ${Math.min(start + this.listState.pageSize, filtered.length)} of ${filtered.length}` : 'Showing 0 requests'}</span></div>
                <div class="commands-pagination">
                    <button class="commands-page-arrow" onclick="HttpRequests.changePage(${this.listState.page - 1})" ${this.listState.page === 1 ? 'disabled' : ''} aria-label="Previous page"><i class="fas fa-chevron-left"></i></button>
                    ${pagination}
                    <button class="commands-page-arrow" onclick="HttpRequests.changePage(${this.listState.page + 1})" ${this.listState.page === pageCount ? 'disabled' : ''} aria-label="Next page"><i class="fas fa-chevron-right"></i></button>
                    <select class="form-select commands-page-size" onchange="HttpRequests.changePageSize(this.value)" aria-label="Rows per page">${[10, 25, 50].map(size => `<option value="${size}" ${size === this.listState.pageSize ? 'selected' : ''}>${size} / page</option>`).join('')}</select>
                </div>
            </div></div>`);
    },

    toggleRowActions(trigger, id, event) {
        event.preventDefault();
        event.stopPropagation();
        if (this.activeActionsMenu && this.activeActionsMenu.trigger === trigger) { this.closeRowActionsMenu(); return; }
        this.closeRowActionsMenu();
        const runBookCount = this.getRunBookCount(id);
        const menu = document.createElement('div');
        menu.className = 'dropdown-menu show commands-actions-menu commands-actions-menu-portal';
        menu.setAttribute('role', 'menu');
        const addItem = (icon, label, handler, className = 'dropdown-item') => {
            const button = document.createElement('button');
            button.type = 'button';
            button.className = className;
            button.innerHTML = `<i class="fas ${icon}"></i><span>${label}</span>`;
            if (handler) button.addEventListener('click', () => { this.closeRowActionsMenu(); handler(); });
            menu.appendChild(button);
            return button;
        };
        addItem('fa-edit', 'Edit', () => this.edit(id));
        addItem('fa-copy', 'Duplicate', () => this.duplicate(id));
        const divider = document.createElement('div');
        divider.className = 'dropdown-divider';
        menu.appendChild(divider);
        if (runBookCount > 0) {
            const button = addItem('fa-trash', 'Delete', null, 'dropdown-item text-muted');
            button.disabled = true;
            button.title = `Used by ${runBookCount} RunBook(s)`;
            const status = document.createElement('span');
            status.className = 'ms-auto small';
            status.textContent = 'In use';
            button.appendChild(status);
        } else {
            addItem('fa-trash', 'Delete', () => this.remove(id), 'dropdown-item text-danger');
        }
        const closeOnOutsideClick = e => { if (!menu.contains(e.target) && e.target !== trigger) this.closeRowActionsMenu(); };
        const closeOnEscape = e => { if (e.key === 'Escape') { this.closeRowActionsMenu(); trigger.focus(); } };
        const closeOnViewportChange = () => this.closeRowActionsMenu();
        this.activeActionsMenu = { element: menu, trigger, closeOnOutsideClick, closeOnEscape, closeOnViewportChange };
        trigger.setAttribute('aria-expanded', 'true');
        document.body.appendChild(menu);
        menu.style.visibility = 'hidden';
        const triggerRect = trigger.getBoundingClientRect();
        const menuRect = menu.getBoundingClientRect();
        const left = Math.min(Math.max(8, triggerRect.right - menuRect.width), window.innerWidth - menuRect.width - 8);
        const top = menuRect.height <= window.innerHeight - triggerRect.bottom - 8 ? triggerRect.bottom + 4 : Math.max(8, triggerRect.top - menuRect.height - 4);
        menu.style.left = `${left}px`;
        menu.style.top = `${top}px`;
        menu.style.visibility = 'visible';
        document.addEventListener('click', closeOnOutsideClick);
        document.addEventListener('keydown', closeOnEscape);
        document.addEventListener('scroll', closeOnViewportChange, true);
        window.addEventListener('resize', closeOnViewportChange);
    },

    closeRowActionsMenu() {
        if (!this.activeActionsMenu) return;
        const { element, trigger, closeOnOutsideClick, closeOnEscape, closeOnViewportChange } = this.activeActionsMenu;
        this.activeActionsMenu = null;
        document.removeEventListener('click', closeOnOutsideClick);
        document.removeEventListener('keydown', closeOnEscape);
        document.removeEventListener('scroll', closeOnViewportChange, true);
        window.removeEventListener('resize', closeOnViewportChange);
        trigger.setAttribute('aria-expanded', 'false');
        element.remove();
    },

    // ----- Form -----

    showAddModal() {
        this.editingId = null;
        this.fillForm({ request_id: this.generateId(), method: 'GET', follow_redirects: true });
        $('#httpRequestModalTitle').text('Add HTTP Request');
        $('#httpReqId').prop('readonly', false);
        $('#httpReqIdHelp').text('Unique ID shared with Command IDs - cannot be changed after creation (letters, numbers and hyphens only)');
        this.setupRecentLogsTab(null);
        this.openModal();
    },

    edit(id) {
        const item = this.items.find(entry => entry.request_id === id);
        if (!item) return;
        this.editingId = id;
        this.fillForm(item);
        $('#httpRequestModalTitle').text('Edit HTTP Request');
        $('#httpReqId').prop('readonly', true);
        $('#httpReqIdHelp').text('Request ID cannot be changed after creation');
        this.setupRecentLogsTab(id);
        this.openModal();
    },

    openModal() {
        $('#httpreq-request-tab').tab('show');
        $('#httpReqId').off('input.validation').on('input.validation', function () {
            const cleaned = $(this).val().replace(/[^a-zA-Z0-9-]/g, '');
            if (cleaned !== $(this).val()) $(this).val(cleaned);
        });
        $('#httpRequestForm').off('input.saverun change.saverun').on('input.saverun change.saverun', () => {
            $('#btnHttpReqSaveAndRun, #btnHttpReqSave').prop('disabled', false);
        });
        $('#btnHttpReqSaveAndRun, #btnHttpReqSave').prop('disabled', false);
        $('#httpRequestModal').modal('show');
    },

    fillForm(item) {
        const form = $('#httpRequestForm')[0];
        form.reset();
        this.form = {
            queryParams: (item.query_params || []).map(row => ({ ...row })),
            headers: (item.headers || []).map(row => ({ ...row })),
            formFields: (item.body?.fields || []).map(row => ({ ...row })),
            assertions: (item.assertions || []).map(row => ({ ...row })),
            hasAuthSecret: !!item.auth?.has_secret
        };
        $('#httpReqId').val(item.request_id || '');
        $('#httpReqDescription').val(item.description || '');
        $('#httpReqMethod').val((item.method || 'GET').toUpperCase());
        $('#httpReqUrl').val(item.url || '');
        $('#httpReqBodyType').val(item.body?.type || 'none');
        $('#httpReqBodyContent').val(item.body?.content || '');
        $('#httpReqLogStorage').val(item.log_storage === 's3' ? 's3' : 'local');
        const auth = item.auth || {};
        $('#httpReqAuthType').val(auth.type || 'none');
        $('#httpReqAuthUsername').val(auth.username || '');
        $('#httpReqAuthPassword').val('');
        $('#httpReqAuthToken').val('');
        $('#httpReqAuthPrefix').val(auth.prefix || '');
        $('#httpReqAuthKey').val(auth.key || '');
        $('#httpReqAuthValue').val('');
        $('#httpReqAuthAddTo').val(auth.add_to || 'header');
        $('.httpreq-secret-help').text(this.form.hasAuthSecret ? 'A secret is stored. Leave empty to keep it.' : '');
        $('#httpReqAuthPassword, #httpReqAuthToken, #httpReqAuthValue').attr('placeholder', this.form.hasAuthSecret ? '••••••• (unchanged)' : '');
        $('#httpReqTimeout').val(item.timeout_seconds || '');
        $('#httpReqRetryCount').val(item.retry_count || '');
        $('#httpReqRetryDelay').val(item.retry_delay_seconds || '');
        $('#httpReqMaxOutput').val(item.max_output_size || '');
        $('#httpReqFollowRedirects').prop('checked', item.follow_redirects !== false);
        $('#httpReqMaxRedirects').val(item.max_redirects || '');
        $('#httpReqSkipTls').prop('checked', !!item.skip_tls_verify);
        $('#httpReqExpectedStatus').val(item.expected_status || '');
        $('#httpReqSchedule').val(item.cron_schedule || '');
        $('#httpReqBearerToken').val(item.bearer_token || '');
        $('#httpReqWebhook').val(item.notify_webhook || '');
        $('#httpReqSkipIfRunning').prop('checked', !!item.skip_if_running);
        const mail = item.notify_email;
        $('#httpReqEmailTo').val(mail ? (mail.to || []).join(', ') : '');
        $('#httpReqEmailSubject').val(mail?.subject || '');
        $('#httpReqEmailOnSuccess').prop('checked', mail ? !!mail.on_success : true);
        $('#httpReqEmailOnFailure').prop('checked', mail ? !!mail.on_failure : true);
        $('#httpReqEmailIncludeOutput').prop('checked', mail ? !!mail.include_output : true);
        $('#httpReqEmailMaxOutputLines').val(mail?.max_output_lines || '');
        this.renderRows('queryParams');
        this.renderRows('headers');
        this.renderRows('formFields');
        this.renderRows('assertions');
        this.onAuthTypeChange();
        this.onBodyTypeChange();
    },

    onAuthTypeChange() {
        const type = $('#httpReqAuthType').val();
        $('#httpReqAuthBasic').toggle(type === 'basic');
        $('#httpReqAuthBearer').toggle(type === 'bearer');
        $('#httpReqAuthApiKey').toggle(type === 'api_key');
    },

    onBodyTypeChange() {
        const type = $('#httpReqBodyType').val();
        $('#httpReqBodyRaw').toggle(['json', 'text', 'xml'].includes(type));
        $('#httpReqBodyForm').toggle(type === 'form');
    },

    rowsContainer(kind) {
        return { queryParams: '#httpReqQueryParams', headers: '#httpReqHeaders', formFields: '#httpReqFormFields', assertions: '#httpReqAssertions' }[kind];
    },

    addRow(kind) {
        this.form[kind].push(kind === 'assertions' ? { type: 'body_contains', path: '', value: '' } : { key: '', value: '', enabled: true });
        this.renderRows(kind);
        $(`${this.rowsContainer(kind)} tr:last input`).first().trigger('focus');
        $('#btnHttpReqSaveAndRun, #btnHttpReqSave').prop('disabled', false);
    },
    removeRow(kind, index) { this.form[kind].splice(index, 1); this.renderRows(kind); $('#btnHttpReqSaveAndRun, #btnHttpReqSave').prop('disabled', false); },
    updateRow(kind, index, field, value) {
        const row = this.form[kind][index];
        if (!row) return;
        row[field] = value;
        if (kind === 'assertions' && field === 'type') this.renderRows(kind);
    },

    renderRows(kind) {
        const rows = this.form[kind];
        const container = $(this.rowsContainer(kind));
        if (!rows.length) {
            container.html(`<div class="http-kv-empty text-muted small">${kind === 'assertions' ? 'No assertions. The expected status check always applies.' : 'None'}</div>`);
            return;
        }
        if (kind === 'assertions') {
            container.html(`<table class="table table-sm http-kv-table mb-1"><thead><tr><th style="width:190px">Type</th><th>Path</th><th>Value</th><th style="width:40px"></th></tr></thead><tbody>${rows.map((row, i) => `
                <tr>
                    <td><select class="form-select form-select-sm" onchange="HttpRequests.updateRow('assertions', ${i}, 'type', this.value)">${[['body_contains', 'Body contains'], ['json_path_equals', 'JSON path equals'], ['max_response_ms', 'Max response ms']].map(([v, l]) => `<option value="${v}" ${row.type === v ? 'selected' : ''}>${l}</option>`).join('')}</select></td>
                    <td><input class="form-control form-control-sm font-monospace" value="${this.escape(row.path || '')}" placeholder="data.items.0.id" ${row.type === 'json_path_equals' ? '' : 'disabled'} oninput="HttpRequests.updateRow('assertions', ${i}, 'path', this.value)"></td>
                    <td><input class="form-control form-control-sm font-monospace" value="${this.escape(row.value || '')}" placeholder="${row.type === 'max_response_ms' ? '1500' : 'expected value'}" oninput="HttpRequests.updateRow('assertions', ${i}, 'value', this.value)"></td>
                    <td class="text-end"><button type="button" class="btn btn-sm btn-link text-danger p-0" title="Remove" onclick="HttpRequests.removeRow('assertions', ${i})"><i class="fas fa-times"></i></button></td>
                </tr>`).join('')}</tbody></table>`);
            return;
        }
        container.html(`<table class="table table-sm http-kv-table mb-1"><thead><tr><th style="width:34px" title="Enabled"></th><th>Key</th><th>Value</th><th style="width:40px"></th></tr></thead><tbody>${rows.map((row, i) => `
            <tr class="${row.enabled === false ? 'http-kv-disabled' : ''}">
                <td><input class="form-check-input" type="checkbox" ${row.enabled === false ? '' : 'checked'} title="Enabled" onchange="HttpRequests.updateRow('${kind}', ${i}, 'enabled', this.checked)"></td>
                <td><input class="form-control form-control-sm font-monospace" value="${this.escape(row.key || '')}" placeholder="key" oninput="HttpRequests.updateRow('${kind}', ${i}, 'key', this.value)"></td>
                <td><input class="form-control form-control-sm font-monospace" value="${this.escape(row.value || '')}" placeholder="value" ${row.masked ? `title="Stored value is hidden; leave ${this.MASK} to keep it"` : ''} oninput="HttpRequests.updateRow('${kind}', ${i}, 'value', this.value)"></td>
                <td class="text-end"><button type="button" class="btn btn-sm btn-link text-danger p-0" title="Remove" onclick="HttpRequests.removeRow('${kind}', ${i})"><i class="fas fa-times"></i></button></td>
            </tr>`).join('')}</tbody></table>`);
    },

    collect() {
        const form = $('#httpRequestForm')[0];
        if (!form.checkValidity()) { form.reportValidity(); return null; }
        const text = selector => $(selector).val().trim();
        const number = selector => { const n = Number($(selector).val()); return Number.isFinite(n) && n > 0 ? n : undefined; };
        const rows = kind => this.form[kind].filter(row => (row.key || '').trim()).map(row => ({ key: row.key.trim(), value: row.value || '', ...(row.enabled === false ? { enabled: false } : {}) }));
        const data = { request_id: text('#httpReqId'), description: text('#httpReqDescription'), method: $('#httpReqMethod').val(), url: text('#httpReqUrl') };
        if (!/^https?:\/\//i.test(data.url) && !/^\{\{/.test(data.url) && !/^\$\{\{/.test(data.url)) { Utils.showError('URL must start with http:// or https://'); $('#httpreq-request-tab').tab('show'); return null; }
        const queryParams = rows('queryParams'); if (queryParams.length) data.query_params = queryParams;
        const headers = rows('headers'); if (headers.length) data.headers = headers;
        const bodyType = $('#httpReqBodyType').val();
        if (['json', 'text', 'xml'].includes(bodyType)) {
            const content = $('#httpReqBodyContent').val();
            if (bodyType === 'json' && content.trim()) { try { JSON.parse(content); } catch (_) { Utils.showError('Body is not valid JSON'); $('#httpreq-request-tab').tab('show'); return null; } }
            data.body = { type: bodyType, content };
        } else if (bodyType === 'form') {
            data.body = { type: 'form', fields: rows('formFields') };
        }
        const authType = $('#httpReqAuthType').val();
        if (authType !== 'none') {
            const auth = { type: authType };
            const secret = selector => { const value = $(selector).val(); return value === '' ? undefined : value; };
            if (authType === 'basic') { auth.username = text('#httpReqAuthUsername'); auth.password = secret('#httpReqAuthPassword'); }
            if (authType === 'bearer') { auth.token = secret('#httpReqAuthToken'); auth.prefix = text('#httpReqAuthPrefix') || undefined; }
            if (authType === 'api_key') { auth.key = text('#httpReqAuthKey'); auth.value = secret('#httpReqAuthValue'); auth.add_to = $('#httpReqAuthAddTo').val(); }
            data.auth = auth;
        }
        data.timeout_seconds = number('#httpReqTimeout');
        data.retry_count = number('#httpReqRetryCount');
        data.retry_delay_seconds = number('#httpReqRetryDelay');
        data.max_output_size = text('#httpReqMaxOutput') || undefined;
        if (!$('#httpReqFollowRedirects').is(':checked')) data.follow_redirects = false;
        data.max_redirects = number('#httpReqMaxRedirects');
        if ($('#httpReqSkipTls').is(':checked')) data.skip_tls_verify = true;
        data.expected_status = text('#httpReqExpectedStatus') || undefined;
        const assertions = this.form.assertions.filter(row => (row.value || '').trim() || (row.path || '').trim()).map(row => ({ type: row.type, path: row.type === 'json_path_equals' ? (row.path || '').trim() : undefined, value: (row.value || '').trim() }));
        if (assertions.length) data.assertions = assertions;
        data.cron_schedule = text('#httpReqSchedule') || undefined;
        data.bearer_token = text('#httpReqBearerToken') || undefined;
        data.notify_webhook = text('#httpReqWebhook') || undefined;
        if ($('#httpReqSkipIfRunning').is(':checked')) data.skip_if_running = true;
        data.log_storage = $('#httpReqLogStorage').val() === 's3' ? 's3' : undefined;
        const to = text('#httpReqEmailTo').split(',').map(v => v.trim()).filter(Boolean);
        if (to.length) {
            data.notify_email = { to, subject: text('#httpReqEmailSubject') || '{{status}} - {{request_id}} ({{host}})', on_success: $('#httpReqEmailOnSuccess').is(':checked'), on_failure: $('#httpReqEmailOnFailure').is(':checked'), include_output: $('#httpReqEmailIncludeOutput').is(':checked'), max_output_lines: Number($('#httpReqEmailMaxOutputLines').val()) || 0 };
        }
        return data;
    },

    async save(runAfter) {
        const data = this.collect();
        if (!data) return;
        try {
            Utils.showLoading();
            const response = this.editingId ? await API.httpRequests.update(this.editingId, data) : await API.httpRequests.create(data);
            if (!response.success) throw new Error(response.error || 'Save failed');
            Utils.showSuccess(this.editingId ? 'HTTP request updated' : 'HTTP request created');
            this.editingId = data.request_id;
            $('#btnHttpReqSaveAndRun, #btnHttpReqSave').prop('disabled', true);
            await this.loadData();
            if (runAfter) {
                $('#httpRequestModal').modal('hide');
                await this.run(data.request_id);
            } else {
                $('#httpRequestModal').modal('hide');
            }
        } catch (error) {
            const message = error.message || 'Failed to save HTTP request';
            Utils.showError(/already exists|conflicts/i.test(message) ? `${message}. Choose a different Request ID.` : message);
        } finally {
            Utils.hideLoading();
        }
    },

    async remove(id) {
        if (this.getRunBookCount(id) > 0) { Utils.showError('This request is used by a RunBook and cannot be deleted'); return; }
        if (!await Utils.confirm('Delete HTTP request', `Delete HTTP request "${id}"?`, 'Delete')) return;
        try {
            await API.httpRequests.remove(id);
            Utils.showSuccess('HTTP request deleted');
            await this.loadData();
        } catch (error) {
            Utils.showError(error.message || 'Failed to delete HTTP request');
        }
    },

    duplicate(id) {
        const item = this.items.find(entry => entry.request_id === id);
        if (!item) return;
        const copy = JSON.parse(JSON.stringify(item));
        let candidate = `${id}-copy`;
        let suffix = 2;
        while (this.items.some(entry => entry.request_id === candidate)) candidate = `${id}-copy-${suffix++}`;
        copy.request_id = candidate;
        delete copy.cron_schedule;
        if (copy.auth) copy.auth.has_secret = false; // secrets are never returned, the copy needs new ones
        copy.headers = (copy.headers || []).map(row => row.masked ? { ...row, value: '', masked: false } : row);
        this.editingId = null;
        this.fillForm(copy);
        $('#httpRequestModalTitle').text('Duplicate HTTP Request');
        $('#httpReqId').prop('readonly', false);
        $('#httpReqIdHelp').text('Secrets (auth, masked headers) are not copied - enter them again before saving.');
        this.setupRecentLogsTab(null);
        this.openModal();
    },

    setupRecentLogsTab(id) {
        $('#recentLogsContainer').html(`<div class="text-center py-4 text-muted"><i class="fas fa-file-alt fa-2x mb-2"></i><p>${id ? 'Click this tab to load recent logs.' : 'Save the request first to see execution logs.'}</p></div>`);
        $('#recentlogs-tab').off('shown.bs.tab');
        if (id) {
            let loaded = false;
            $('#recentlogs-tab').on('shown.bs.tab', () => { if (!loaded) { loaded = true; Commands.loadRecentLogs(id); } });
        }
    },

    // ----- Execution -----

    async run(id) {
        const output = $('#httpRequestRunOutput');
        output.html(`<div class="text-center py-4"><i class="fas fa-spinner fa-spin fa-2x text-primary"></i><p class="mt-3">Sending request: <strong>${this.escape(id)}</strong>...</p></div>`);
        $('#httpRequestRunModal').modal('show');
        try {
            const response = await API.httpRequests.execute(id);
            output.html(this.renderRunResult(response.data || {}, !response.success, response.error));
        } catch (error) {
            const data = error.data || {};
            output.html(data.trace_id ? this.renderRunResult(data, true, error.message) : `<div class="alert alert-danger"><i class="fas fa-exclamation-triangle"></i> <strong>Failed to execute request</strong><p class="mb-0 mt-2">${this.escape(error.message || 'Unknown error occurred')}</p></div>`);
        }
    },

    renderRunResult(data, failed, errorMessage) {
        const success = !failed && data.exit_code === 0;
        const checks = data.checks || [];
        const headers = data.response_headers || {};
        const contentType = (headers['Content-Type'] || headers['content-type'] || [''])[0] || '';
        let body = data.output || '';
        let bodyHtml = this.escape(body);
        if (body && /json/i.test(contentType)) {
            try { body = JSON.stringify(JSON.parse(body), null, 2); } catch (_) { /* keep raw */ }
            try { bodyHtml = Utils.highlightCode(body, 'json'); } catch (_) { bodyHtml = this.escape(body); }
        }
        const headerRows = Object.keys(headers).sort().map(key => (headers[key] || []).map(value => `<tr><td class="fw-semibold">${this.escape(key)}</td><td class="font-monospace">${this.escape(value)}</td></tr>`).join('')).join('');
        return `
            <div class="alert ${success ? 'alert-success' : 'alert-danger'} mb-3">
                <div class="row">
                    <div class="col-md-6">
                        <strong><i class="fas fa-globe"></i> Request:</strong> ${this.escape(data.request_id || '')}<br>
                        <strong><i class="fas fa-paper-plane"></i> Call:</strong> ${this.methodBadge(data.method)} <span class="font-monospace small">${this.escape(data.url || '')}</span><br>
                        <strong><i class="fas ${success ? 'fa-check-circle' : 'fa-times-circle'}"></i> Status:</strong> ${success ? 'Success' : 'Failed'} ${data.http_status ? `<span class="badge ${data.http_status < 400 ? 'bg-success' : 'bg-danger'} ms-1">HTTP ${data.http_status}${data.status_text ? ' ' + this.escape(data.status_text.replace(/^\d+\s*/, '')) : ''}</span>` : '<span class="badge bg-secondary ms-1">No response</span>'}
                    </div>
                    <div class="col-md-6">
                        <strong><i class="fas fa-clock"></i> Time:</strong> ${this.escape(data.exec_time || 'N/A')}${data.response_ms ? ` (${data.response_ms} ms)` : ''}<br>
                        <strong><i class="fas fa-redo"></i> Attempts:</strong> ${data.attempts || 0} &nbsp; <strong><i class="fas fa-database"></i> Body:</strong> ${Utils.formatFileSize(data.response_bytes || 0)}${data.is_output_truncated ? ' <span class="badge bg-warning text-dark">Truncated</span>' : ''}<br>
                        <strong><i class="fas fa-fingerprint"></i> Trace ID:</strong> <code>${this.escape(data.trace_id || 'N/A')}</code>
                    </div>
                </div>
            </div>
            ${errorMessage || data.error ? `<div class="mb-3"><h6 class="mb-2 text-danger"><i class="fas fa-exclamation-triangle"></i> Error:</h6><pre class="bg-danger bg-opacity-10 text-danger p-3 rounded">${this.escape(errorMessage || data.error)}</pre></div>` : ''}
            ${checks.length ? `<div class="mb-3"><h6 class="mb-2"><i class="fas fa-check-double"></i> Checks:</h6><ul class="list-group http-checks-list">${checks.map(check => `<li class="list-group-item d-flex align-items-center gap-2"><span class="badge ${check.passed ? 'bg-success' : 'bg-danger'}">${check.passed ? 'PASS' : 'FAIL'}</span><strong>${this.escape(check.name)}</strong><span class="text-muted">${this.escape(check.detail)}</span></li>`).join('')}</ul></div>` : ''}
            ${headerRows ? `<details class="mb-3"><summary class="fw-semibold"><i class="fas fa-list"></i> Response headers (${Object.keys(headers).length})</summary><div class="table-responsive mt-2"><table class="table table-sm http-response-headers mb-0"><tbody>${headerRows}</tbody></table></div></details>` : ''}
            <div class="mb-3"><h6 class="mb-2"><i class="fas fa-file-code"></i> Response body:</h6><pre class="bg-dark text-light p-3 rounded http-response-body">${bodyHtml || '<span class="text-muted">(empty)</span>'}</pre></div>
            ${data.trace_id ? `<div class="d-flex align-items-center gap-3 small text-muted"><span><i class="fas fa-info-circle"></i> Full log (request, response headers and body): <code>/api/logs/${this.escape(data.trace_id)}</code></span><button type="button" class="btn btn-sm btn-outline-secondary" onclick="HttpRequests.toggleFullLog('${this.escape(data.trace_id)}')"><i class="fas fa-file-alt me-1"></i>View full log</button></div><div id="httpRequestFullLog" class="mt-2" style="display:none"></div>` : ''}`;
    },

    async toggleFullLog(traceId) {
        const container = $('#httpRequestFullLog');
        if (container.is(':visible')) { container.slideUp(150); return; }
        container.html('<div class="text-center py-2"><i class="fas fa-spinner fa-spin"></i> Loading log...</div>').slideDown(150);
        try {
            const response = await API.ops.getLogContent(traceId);
            container.html(`<pre class="bg-dark text-light p-2 rounded small http-response-body mb-0">${this.escape(response.data?.content || 'No content')}</pre>`);
        } catch (error) {
            container.html(`<div class="text-danger small">Error loading log: ${this.escape(error.message)}</div>`);
        }
    },

    cleanup() { this.closeRowActionsMenu(); }
};

window.HttpRequests = HttpRequests;
