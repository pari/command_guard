const RunBooks = {
    runBooks: [],
    commands: [],
    httpRequests: [],
    runs: {},
    editingId: null,
    steps: [],
    pollTimer: null,
    listState: { search: '', schedule: 'all', status: 'all' },
    activeActionsMenu: null,
    stepDrag: null,

    async init() {
        this.clearPoll();
        this.closeRowActionsMenu();
        this.renderShell();
        await this.loadData();
    },

    renderShell() {
        $('#mainContent').html(`
            <div class="commands-page runbooks-page">
                <div id="runBooksView"></div>
            </div>
        `);
        this.renderListShell();
    },

    async loadData() {
        try {
            const response = await API.config.get();
            if (!response.success) throw new Error('Failed to load configuration');
            this.runBooks = response.data.runbooks || [];
            this.commands = response.data.commands || [];
            try { const requests = await API.httpRequests.list(); this.httpRequests = requests.data || []; } catch (_) { this.httpRequests = []; }
            await Promise.all(this.runBooks.map(async runBook => {
                try {
                    const history = await API.ops.getRunBookRuns(runBook.runbook_id);
                    this.runs[runBook.runbook_id] = history.data || [];
                } catch (_) {
                    this.runs[runBook.runbook_id] = [];
                }
            }));
            this.renderList();
        } catch (error) {
            console.error('RunBooks load error:', error);
            Utils.showError('Failed to load RunBooks');
        }
    },

    renderListShell() {
        $('#runBooksView').html(`
            <div class="commands-page-header"><div><h1>RunBooks</h1><p>Build and monitor ordered workflows of Commands and HTTP requests.</p></div>
                <button class="btn btn-primary commands-add-button" onclick="RunBooks.create()"><i class="fas fa-plus me-2"></i>New RunBook</button></div>
            <div class="commands-toolbar runbooks-toolbar" aria-label="RunBook filters">
                <div class="commands-search-wrap"><i class="fas fa-search" aria-hidden="true"></i><input type="search" class="form-control" id="runBooksSearch" placeholder="Search RunBooks..." aria-label="Search RunBooks" value="${this.escape(this.listState.search)}" oninput="RunBooks.setFilter('search', this.value)"></div>
                <div class="commands-filter-field"><label for="runBooksScheduleFilter">Schedule</label><select class="form-select" id="runBooksScheduleFilter" onchange="RunBooks.setFilter('schedule', this.value)"><option value="all">All schedules</option><option value="scheduled" ${this.listState.schedule === 'scheduled' ? 'selected' : ''}>Scheduled</option><option value="manual" ${this.listState.schedule === 'manual' ? 'selected' : ''}>Manual</option></select></div>
                <div class="commands-filter-field"><label for="runBooksStatusFilter">Last status</label><select class="form-select" id="runBooksStatusFilter" onchange="RunBooks.setFilter('status', this.value)"><option value="all">All statuses</option><option value="running" ${this.listState.status === 'running' ? 'selected' : ''}>Running</option><option value="succeeded" ${this.listState.status === 'succeeded' ? 'selected' : ''}>Succeeded</option><option value="failed" ${this.listState.status === 'failed' ? 'selected' : ''}>Failed</option><option value="never run" ${this.listState.status === 'never run' ? 'selected' : ''}>Never run</option></select></div>
                <button class="btn commands-clear-button" type="button" onclick="RunBooks.clearFilters()"><i class="fas fa-filter me-2"></i>Clear filters</button>
            </div>
            <div id="runBooksSummary" class="commands-summary" aria-label="RunBooks summary"></div>
            <div id="runBooksListContainer">
                <div class="text-center py-5">
                    <i class="fas fa-spinner fa-spin fa-2x text-primary"></i>
                    <p class="mt-3 text-muted">Loading RunBooks...</p>
                </div>
            </div>
        `);
    },

    renderList() {
        this.clearPoll();
        this.closeRowActionsMenu();
        if (!$('#runBooksListContainer').length) {
            this.renderListShell();
        }
        this.renderRunBooksSummary();
        this.renderRunBooksTable();
    },

    renderRunBooksSummary() {
        const scheduled = this.runBooks.filter(item => item.cron_schedule).length;
        const running = this.runBooks.filter(item => this.latestRun(item.runbook_id)?.status === 'running' || this.latestRun(item.runbook_id)?.status === 'stopping').length;
        const failed = this.runBooks.filter(item => ['failed', 'timed_out', 'interrupted'].includes(this.latestRun(item.runbook_id)?.status)).length;
        $('#runBooksSummary').html(`
            ${this.metric('fa-book', 'Total RunBooks', this.runBooks.length)}
            ${this.metric('fa-clock', 'Scheduled', scheduled)}
            ${this.metric('fa-hand-pointer', 'Manual', this.runBooks.length - scheduled)}
            ${this.metric('fa-spinner', 'Running', running)}
            ${this.metric('fa-exclamation-circle', 'Last run failed', failed)}
            ${this.metric('fa-list-ol', 'Total steps', this.runBooks.reduce((sum, item) => sum + (item.steps || []).length, 0))}
        `);
    },

    renderRunBooksTable() {
        const filtered = this.getFiltered();
        const rows = filtered.map(runBook => {
            const latest = this.latestRun(runBook.runbook_id);
            const preview = (runBook.steps || []).slice(0, 3).map(step => `<span class="badge bg-light text-dark me-1">${step.request_id ? '<i class="fas fa-globe me-1" title="HTTP request"></i>' : ''}${this.escape(this.stepTarget(step))}</span>`).join('');
            const stepCount = (runBook.steps || []).length;
            return `<tr>
                <td><button type="button" class="commands-command-name d-block border-0 bg-transparent p-0 text-start" data-runbook-id="${this.escape(runBook.runbook_id)}" onclick="RunBooks.edit(this.dataset.runbookId)" aria-label="Edit ${this.escape(runBook.runbook_id)}" title="Edit ${this.escape(runBook.runbook_id)}">${this.escape(runBook.runbook_id)}</button><small class="text-muted">${this.escape(runBook.description || 'No description')}</small></td>
                <td><div class="runbook-step-preview">${preview}${stepCount > 3 ? `<span class="badge bg-secondary">+${stepCount - 3}</span>` : ''}</div><small class="text-muted">${stepCount} step${stepCount === 1 ? '' : 's'}</small></td>
                <td>${runBook.cron_schedule ? `<span class="commands-schedule-badge"><i class="far fa-clock"></i>${this.escape(runBook.cron_schedule)}</span>` : '<span class="commands-schedule-badge is-manual">Manual</span>'}</td>
                <td>${this.statusBadge(latest?.status || 'never run')}</td>
                <td><small>${latest ? Utils.formatRelativeTime(latest.started_at) : '—'}</small></td>
                <td><div class="commands-row-actions">
                    <button class="btn commands-run-button" type="button" onclick="RunBooks.start('${this.escape(runBook.runbook_id)}')"><i class="fas fa-play"></i><span>Run</span></button>
                    <button class="btn commands-more-button" type="button" data-runbook-id="${this.escape(runBook.runbook_id)}"
                            aria-expanded="false" aria-haspopup="menu" aria-label="Actions for ${this.escape(runBook.runbook_id)}"
                            onclick="RunBooks.toggleRowActions(this, this.dataset.runbookId, event)">
                        <i class="fas fa-ellipsis-v"></i>
                    </button>
                </div></td>
            </tr>`;
        }).join('');

        $('#runBooksListContainer').html(`
            <div class="commands-table-card"><div class="commands-table-scroll"><table class="commands-table runbooks-table">
                <thead><tr><th>RunBook</th><th>Steps</th><th>Schedule</th><th>Last status</th><th>Last run</th><th>Actions</th></tr></thead>
                <tbody>${rows || `<tr><td colspan="6"><div class="commands-no-results"><i class="fas fa-book"></i><strong>${this.runBooks.length ? 'No RunBooks found' : 'No RunBooks configured'}</strong><span>${this.runBooks.length ? 'Change or clear the filters.' : 'Create a RunBook to execute Commands in order.'}</span></div></td></tr>`}</tbody>
            </table></div></div>
        `);
    },

    metric(icon, label, value) {
        return `<div class="commands-summary-item"><span class="commands-summary-icon"><i class="fas ${icon}"></i></span><span><small>${label}</small><strong>${value}</strong></span></div>`;
    },

    latestRun(id) { return (this.runs[id] || [])[0]; },
    getFiltered() {
        const search = this.listState.search.toLowerCase();
        return this.runBooks.filter(item => {
            const latest = this.latestRun(item.runbook_id);
            const matchSearch = !search || [item.runbook_id, item.description, ...(item.steps || []).map(step => this.stepTarget(step))].some(value => String(value || '').toLowerCase().includes(search));
            const matchSchedule = this.listState.schedule === 'all' || (this.listState.schedule === 'scheduled' ? !!item.cron_schedule : !item.cron_schedule);
            const status = latest?.status || 'never run';
            const matchStatus = this.listState.status === 'all' || status === this.listState.status || (this.listState.status === 'failed' && ['failed', 'timed_out', 'interrupted'].includes(status));
            return matchSearch && matchSchedule && matchStatus;
        });
    },
    setFilter(name, value) {
        if (!Object.prototype.hasOwnProperty.call(this.listState, name)) return;
        this.listState[name] = name === 'search' ? String(value || '').trim().toLowerCase() : value;
        this.renderList();
    },
    clearFilters() {
        this.listState = { search: '', schedule: 'all', status: 'all' };
        $('#runBooksSearch').val('');
        $('#runBooksScheduleFilter').val('all');
        $('#runBooksStatusFilter').val('all');
        this.renderList();
    },

    toggleRowActions(trigger, runBookId, event) {
        event.preventDefault();
        event.stopPropagation();

        if (this.activeActionsMenu && this.activeActionsMenu.trigger === trigger) {
            this.closeRowActionsMenu();
            return;
        }

        this.closeRowActionsMenu();

        const menu = document.createElement('div');
        menu.className = 'dropdown-menu show commands-actions-menu commands-actions-menu-portal';
        menu.setAttribute('role', 'menu');
        menu.setAttribute('aria-label', `Actions for ${runBookId}`);

        const editButton = document.createElement('button');
        editButton.className = 'dropdown-item';
        editButton.type = 'button';
        editButton.innerHTML = '<i class="fas fa-edit"></i><span>Edit</span>';
        editButton.addEventListener('click', () => {
            this.closeRowActionsMenu();
            this.edit(runBookId);
        });
        menu.appendChild(editButton);

        const duplicateButton = document.createElement('button');
        duplicateButton.className = 'dropdown-item';
        duplicateButton.type = 'button';
        duplicateButton.innerHTML = '<i class="fas fa-copy"></i><span>Duplicate</span>';
        duplicateButton.addEventListener('click', () => {
            this.closeRowActionsMenu();
            this.duplicate(runBookId);
        });
        menu.appendChild(duplicateButton);

        const historyButton = document.createElement('button');
        historyButton.className = 'dropdown-item';
        historyButton.type = 'button';
        historyButton.innerHTML = '<i class="fas fa-history"></i><span>History</span>';
        historyButton.addEventListener('click', () => {
            this.closeRowActionsMenu();
            this.showHistory(runBookId);
        });
        menu.appendChild(historyButton);

        const divider = document.createElement('div');
        divider.className = 'dropdown-divider';
        menu.appendChild(divider);

        const deleteButton = document.createElement('button');
        deleteButton.className = 'dropdown-item text-danger';
        deleteButton.type = 'button';
        deleteButton.innerHTML = '<i class="fas fa-trash"></i><span>Delete</span>';
        deleteButton.addEventListener('click', () => {
            this.closeRowActionsMenu();
            this.remove(runBookId);
        });
        menu.appendChild(deleteButton);

        const closeOnOutsideClick = clickEvent => {
            if (!menu.contains(clickEvent.target) && clickEvent.target !== trigger) {
                this.closeRowActionsMenu();
            }
        };
        const closeOnEscape = keyEvent => {
            if (keyEvent.key === 'Escape') {
                this.closeRowActionsMenu();
                trigger.focus();
            }
        };
        const closeOnViewportChange = () => this.closeRowActionsMenu();

        this.activeActionsMenu = {
            element: menu,
            trigger,
            closeOnOutsideClick,
            closeOnEscape,
            closeOnViewportChange
        };
        trigger.setAttribute('aria-expanded', 'true');
        document.body.appendChild(menu);
        this.positionRowActionsMenu();

        document.addEventListener('click', closeOnOutsideClick);
        document.addEventListener('keydown', closeOnEscape);
        document.addEventListener('scroll', closeOnViewportChange, true);
        window.addEventListener('resize', closeOnViewportChange);
    },

    positionRowActionsMenu() {
        if (!this.activeActionsMenu) return;

        const { element, trigger } = this.activeActionsMenu;
        const viewportGap = 8;
        const menuGap = 4;
        element.style.visibility = 'hidden';

        const triggerRect = trigger.getBoundingClientRect();
        const menuRect = element.getBoundingClientRect();
        const left = Math.min(
            Math.max(viewportGap, triggerRect.right - menuRect.width),
            window.innerWidth - menuRect.width - viewportGap
        );
        const spaceBelow = window.innerHeight - triggerRect.bottom - viewportGap;
        const top = menuRect.height <= spaceBelow
            ? triggerRect.bottom + menuGap
            : Math.max(viewportGap, triggerRect.top - menuRect.height - menuGap);

        element.style.left = `${left}px`;
        element.style.top = `${top}px`;
        element.style.visibility = 'visible';
    },

    closeRowActionsMenu() {
        if (!this.activeActionsMenu) return;

        const {
            element,
            trigger,
            closeOnOutsideClick,
            closeOnEscape,
            closeOnViewportChange
        } = this.activeActionsMenu;
        this.activeActionsMenu = null;
        document.removeEventListener('click', closeOnOutsideClick);
        document.removeEventListener('keydown', closeOnEscape);
        document.removeEventListener('scroll', closeOnViewportChange, true);
        window.removeEventListener('resize', closeOnViewportChange);
        trigger.setAttribute('aria-expanded', 'false');
        element.remove();
    },

    create() {
        this.editingId = null;
        this.steps = [this.newStep()];
        this.renderBuilder({ runbook_id: '', description: '', summary_format: 'html' });
    },

    edit(id) {
        const item = this.runBooks.find(runBook => runBook.runbook_id === id);
        if (!item) return;
        this.editingId = id;
        this.steps = JSON.parse(JSON.stringify(item.steps || []));
        this.renderBuilder(item);
    },

    duplicate(id) {
        const item = this.runBooks.find(runBook => runBook.runbook_id === id);
        if (!item) return;
        const copy = JSON.parse(JSON.stringify(item));
        copy.runbook_id = `${id}-copy`;
        this.editingId = null;
        this.steps = copy.steps.map(step => ({ ...step, step_id: undefined }));
        this.renderBuilder(copy);
    },

    newStep() {
        return { cmd_id: this.commands[0]?.cmd_id || '', on_failure: 'stop' };
    },
    stepTarget(step) { return step.request_id || step.cmd_id || ''; },
    stepSelectValue(step) { return step.request_id ? `http:${step.request_id}` : (step.cmd_id ? `cmd:${step.cmd_id}` : ''); },
    setStepTarget(index, value) {
        const step = this.steps[index];
        if (value.startsWith('http:')) { step.request_id = value.slice(5); delete step.cmd_id; }
        else { step.cmd_id = value.startsWith('cmd:') ? value.slice(4) : value; delete step.request_id; }
    },

    renderBuilder(item) {
        const mail = item.notify_email || {};
        $('#runBooksView').html(`
            <div class="runbook-builder-header"><div><button class="btn btn-link ps-0" onclick="RunBooks.renderList()"><i class="fas fa-arrow-left me-2"></i>RunBooks</button><h1>${this.editingId ? 'Edit RunBook' : 'New RunBook'}</h1></div></div>
            <form id="runBookForm" onsubmit="return false">
                <div class="card mb-3"><div class="card-header"><i class="fas fa-info-circle me-2"></i>Basics</div><div class="card-body row g-3">
                    <div class="col-md-5"><label class="form-label">RunBook ID *</label><input id="runBookId" class="form-control" required pattern="[A-Za-z0-9-]+" value="${this.escape(item.runbook_id || '')}" ${this.editingId ? 'readonly' : ''}><div class="form-text">Immutable; letters, numbers, and hyphens only.</div></div>
                    <div class="col-md-7"><label class="form-label">Description</label><input id="runBookDescription" class="form-control" value="${this.escape(item.description || '')}" placeholder="What does this RunBook accomplish?"></div>
                </div></div>
                <div class="card mb-3"><div class="card-header d-flex justify-content-between align-items-center"><span><i class="fas fa-list-ol me-2"></i>Steps</span><button type="button" class="btn btn-sm btn-primary" onclick="RunBooks.addStep()"><i class="fas fa-plus me-1"></i>Add step</button></div><div class="card-body runbook-steps-card-body"><div id="runBookSteps"></div></div></div>
                <div class="row g-3 mb-3"><div class="col-lg-6"><div class="card h-100"><div class="card-header"><i class="fas fa-clock me-2"></i>Schedule & Security</div><div class="card-body">
                    <label class="form-label">Cron schedule</label><input id="runBookCron" class="form-control mb-3" value="${this.escape(item.cron_schedule || '')}" placeholder="0 2 * * *"><div class="form-text mb-3">Optional; empty means manual/API only. Uses server timezone.</div>
                    <label class="form-label">Overall timeout (seconds)</label><input id="runBookTimeout" type="number" min="0" class="form-control mb-3" value="${item.timeout_seconds || ''}" placeholder="No limit">
                    <label class="form-label">Bearer token</label><input id="runBookToken" class="form-control" value="${this.escape(item.bearer_token || '')}" placeholder="Optional external API token">
                </div></div></div><div class="col-lg-6"><div class="card h-100"><div class="card-header"><i class="fas fa-bell me-2"></i>Summary & Notifications</div><div class="card-body">
                    <label class="form-label">Summary format</label><select id="runBookFormat" class="form-select mb-3">${['html','text','markdown','json'].map(format => `<option ${format === (item.summary_format || 'html') ? 'selected' : ''}>${format}</option>`).join('')}</select>
                    <label class="form-label">Webhook URL</label><input id="runBookWebhook" class="form-control mb-3" value="${this.escape(item.notify_webhook || '')}" placeholder="Optional">
                    <div class="form-check form-switch mb-3"><input id="runBookEmailEnabled" class="form-check-input" type="checkbox" ${item.notify_email ? 'checked' : ''} onchange="$('#runBookEmailFields').toggle(this.checked)"><label class="form-check-label">Send consolidated email</label></div>
                    <div id="runBookEmailFields" style="display:${item.notify_email ? 'block' : 'none'}">
                        <label class="form-label">To *</label><input id="runBookEmailTo" class="form-control mb-2" value="${this.escape((mail.to || []).join(', '))}" placeholder="admin@example.com">
                        <label class="form-label">CC</label><input id="runBookEmailCc" class="form-control mb-2" value="${this.escape((mail.cc || []).join(', '))}">
                        <label class="form-label">Subject</label><input id="runBookEmailSubject" class="form-control mb-2" value="${this.escape(mail.subject || '{{status}} - {{runbook_id}} ({{run_id}})')}">
                        <div class="d-flex flex-wrap gap-3"><label><input id="runBookEmailSuccess" type="checkbox" ${mail.on_success !== false ? 'checked' : ''}> On success</label><label><input id="runBookEmailFailure" type="checkbox" ${mail.on_failure !== false ? 'checked' : ''}> On failure</label><label><input id="runBookEmailOutput" type="checkbox" ${mail.include_output !== false ? 'checked' : ''}> Include output</label></div>
                        <label class="form-label mt-2">Maximum output lines</label><input id="runBookEmailLines" type="number" min="0" class="form-control" value="${mail.max_output_lines || ''}" placeholder="Unlimited">
                    </div>
                </div></div></div></div>
                <div class="runbook-builder-actions"><button type="button" class="btn btn-secondary" onclick="RunBooks.renderList()">Cancel</button><button type="button" class="btn btn-outline-primary" onclick="RunBooks.save(false)">Save</button><button type="button" class="btn btn-primary" onclick="RunBooks.save(true)"><i class="fas fa-play me-2"></i>Save & Run</button></div>
            </form>
        `);
        this.renderSteps();
    },

    renderSteps() {
        this.stepDrag = null;
        const rows = this.steps.map((step, index) => `
            <tr class="runbook-step-row" data-index="${index}">
                <td class="runbook-step-reorder-cell" data-label="Reorder">
                    <button type="button" class="runbook-step-drag-handle" aria-label="Drag step ${index + 1} to reorder" title="Drag to reorder"
                        onpointerdown="RunBooks.startStepPointerDrag(event, ${index})" onpointermove="RunBooks.moveStepPointerDrag(event)" onpointerup="RunBooks.endStepPointerDrag(event)" onpointercancel="RunBooks.cancelStepPointerDrag(event)" onlostpointercapture="RunBooks.cancelStepPointerDrag(event)">
                        <i class="fas fa-grip-vertical" aria-hidden="true"></i>
                    </button>
                </td>
                <td data-label="Step target"><select class="form-select" aria-label="Command or HTTP request" required onchange="RunBooks.setStepTarget(${index}, this.value)"><option value="">Select command or request...</option><optgroup label="Commands">${this.commands.map(command => `<option value="cmd:${this.escape(command.cmd_id)}" ${this.stepSelectValue(step) === `cmd:${command.cmd_id}` ? 'selected' : ''}>${this.escape(command.cmd_id)} — ${this.escape(command.host_ref)}</option>`).join('')}</optgroup>${this.httpRequests.length ? `<optgroup label="HTTP Requests">${this.httpRequests.map(request => `<option value="http:${this.escape(request.request_id)}" ${this.stepSelectValue(step) === `http:${request.request_id}` ? 'selected' : ''}>${this.escape(request.request_id)} — ${this.escape((request.method || 'GET').toUpperCase())} ${this.escape(request.url || '')}</option>`).join('')}</optgroup>` : ''}</select></td>
                <td data-label="Label"><input class="form-control" aria-label="Label" value="${this.escape(step.label || '')}" oninput="RunBooks.updateStep(${index}, 'label', this.value)"></td>
                <td data-label="On Failure"><select class="form-select" aria-label="On failure" onchange="RunBooks.updateStep(${index}, 'on_failure', this.value)"><option value="stop" ${step.on_failure !== 'continue' ? 'selected' : ''}>Stop RunBook</option><option value="continue" ${step.on_failure === 'continue' ? 'selected' : ''}>Continue</option></select></td>
                <td data-label="Delay Before"><div class="input-group"><input type="number" min="0" class="form-control" aria-label="Delay before in seconds" value="${index ? (step.delay_before_seconds || 0) : 0}" ${index ? '' : 'disabled'} onchange="RunBooks.updateStep(${index}, 'delay_before_seconds', Number(this.value))"><span class="input-group-text">s</span></div></td>
                <td data-label="Retries"><select class="form-select" aria-label="Retry count" onchange="RunBooks.updateStep(${index}, 'retry_count', Number(this.value))">${[0, 1, 2, 3, 4, 5].map(count => `<option value="${count}" ${this.normalizeRetryCount(step.retry_count) === count ? 'selected' : ''}>${count === 0 ? '0 — No retries' : count}</option>`).join('')}</select></td>
                <td data-label="Retry Delay"><div class="input-group"><input type="number" min="0" class="form-control" aria-label="Retry delay in seconds" value="${step.retry_delay_seconds || 0}" onchange="RunBooks.updateStep(${index}, 'retry_delay_seconds', Number(this.value))"><span class="input-group-text">s</span></div></td>
                <td data-label="Timeout"><div class="input-group"><input type="number" min="0" class="form-control" aria-label="Step timeout in seconds" value="${step.timeout_seconds || ''}" placeholder="No limit" onchange="RunBooks.updateStep(${index}, 'timeout_seconds', Number(this.value))"><span class="input-group-text">s</span></div></td>
                <td class="runbook-step-actions" data-label="Actions"><button type="button" class="btn btn-sm" aria-label="Move step ${index + 1} up" title="Move up" onclick="RunBooks.moveStep(${index}, -1)" ${index === 0 ? 'disabled' : ''}><i class="fas fa-arrow-up" aria-hidden="true"></i></button><button type="button" class="btn btn-sm" aria-label="Move step ${index + 1} down" title="Move down" onclick="RunBooks.moveStep(${index}, 1)" ${index === this.steps.length - 1 ? 'disabled' : ''}><i class="fas fa-arrow-down" aria-hidden="true"></i></button><button type="button" class="btn btn-sm" aria-label="Copy step ${index + 1}" title="Copy step" onclick="RunBooks.copyStep(${index})"><i class="fas fa-copy" aria-hidden="true"></i></button><button type="button" class="btn btn-sm text-danger" aria-label="Delete step ${index + 1}" title="Delete step" onclick="RunBooks.removeStep(${index})" ${this.steps.length === 1 ? 'disabled' : ''}><i class="fas fa-trash" aria-hidden="true"></i></button></td>
            </tr>`).join('');
        $('#runBookSteps').html(`<div class="runbook-steps-table-wrap"><table class="runbook-steps-table"><thead><tr><th scope="col"><span class="visually-hidden">Reorder</span></th><th scope="col">Command / HTTP request</th><th scope="col">Label</th><th scope="col">On Failure</th><th scope="col">Delay Before</th><th scope="col">Retries</th><th scope="col">Retry Delay</th><th scope="col">Timeout</th><th scope="col">Actions</th></tr></thead><tbody>${rows}</tbody></table></div>`);
    },
    updateStep(index, key, value) { this.steps[index][key] = value; },
    normalizeRetryCount(value) { const count = Number(value || 0); return Number.isInteger(count) && count >= 0 && count <= 5 ? count : 0; },
    addStep() { this.steps.push(this.newStep()); this.renderSteps(); },
    removeStep(index) { if (this.steps.length > 1) { this.steps.splice(index, 1); this.steps[0].delay_before_seconds = 0; this.renderSteps(); } },
    copyStep(index) { const copy = { ...this.steps[index], step_id: undefined }; this.steps.splice(index + 1, 0, copy); this.renderSteps(); },
    moveStep(index, direction) {
        const target = index + direction;
        if (target < 0 || target >= this.steps.length) return;
        [this.steps[index], this.steps[target]] = [this.steps[target], this.steps[index]];
        this.steps[0].delay_before_seconds = 0;
        this.renderSteps();
    },
    reorderStep(sourceIndex, targetIndex, insertAfter) {
        if (sourceIndex < 0 || sourceIndex >= this.steps.length || targetIndex < 0 || targetIndex >= this.steps.length || sourceIndex === targetIndex) return;
        const [step] = this.steps.splice(sourceIndex, 1);
        let insertionIndex = targetIndex + (insertAfter ? 1 : 0);
        if (sourceIndex < insertionIndex) insertionIndex -= 1;
        this.steps.splice(insertionIndex, 0, step);
        this.steps[0].delay_before_seconds = 0;
        this.renderSteps();
    },
    startStepPointerDrag(event, index) {
        if (event.button !== 0) return;
        event.preventDefault();
        this.clearStepDrag();
        this.stepDrag = {
            sourceIndex: index,
            targetIndex: index,
            insertAfter: false,
            pointerId: event.pointerId,
            startX: event.clientX,
            startY: event.clientY,
            dragging: false
        };
        event.currentTarget.setPointerCapture(event.pointerId);
    },
    moveStepPointerDrag(event) {
        if (!this.stepDrag || this.stepDrag.pointerId !== event.pointerId) return;
        event.preventDefault();
        if (!this.stepDrag.dragging) {
            const moved = Math.hypot(event.clientX - this.stepDrag.startX, event.clientY - this.stepDrag.startY);
            if (moved < 6) return;
            this.stepDrag.dragging = true;
            event.currentTarget.closest('.runbook-step-row')?.classList.add('is-dragging');
        }
        const row = document.elementFromPoint(event.clientX, event.clientY)?.closest('.runbook-step-row');
        if (!row) return;
        this.setStepDropTarget(row, Number(row.dataset.index), event.clientY);
    },
    endStepPointerDrag(event) {
        if (!this.stepDrag || this.stepDrag.pointerId !== event.pointerId) return;
        event.preventDefault();
        const { sourceIndex, targetIndex, insertAfter, dragging } = this.stepDrag;
        this.clearStepDrag();
        if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
        if (dragging) this.reorderStep(sourceIndex, targetIndex, insertAfter);
    },
    cancelStepPointerDrag(event) {
        if (this.stepDrag && this.stepDrag.pointerId !== event.pointerId) return;
        if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
        this.clearStepDrag();
    },
    setStepDropTarget(row, index, clientY) {
        if (!this.stepDrag || Number.isNaN(index)) return;
        const insertAfter = clientY > row.getBoundingClientRect().top + (row.getBoundingClientRect().height / 2);
        this.clearStepDropIndicators();
        this.stepDrag.targetIndex = index;
        this.stepDrag.insertAfter = insertAfter;
        if (index !== this.stepDrag.sourceIndex) row.classList.add(insertAfter ? 'is-drop-after' : 'is-drop-before');
    },
    clearStepDropIndicators() {
        document.querySelectorAll('.runbook-step-row.is-drop-before, .runbook-step-row.is-drop-after').forEach(row => row.classList.remove('is-drop-before', 'is-drop-after'));
    },
    clearStepDrag() {
        this.clearStepDropIndicators();
        document.querySelectorAll('.runbook-step-row.is-dragging').forEach(row => row.classList.remove('is-dragging'));
        this.stepDrag = null;
    },

    collect() {
        const id = $('#runBookId').val().trim();
        const form = $('#runBookForm')[0];
        if (!form.checkValidity()) { form.reportValidity(); return null; }
        if (!this.steps.length || this.steps.some(step => !step.cmd_id && !step.request_id)) { Utils.showError('Every step requires a Command or an HTTP request'); return null; }
        const data = { runbook_id: id, steps: this.steps.map((step, index) => {
            const collectedStep = {
                cmd_id: step.request_id ? undefined : step.cmd_id, request_id: step.request_id || undefined, label: step.label?.trim() || undefined,
                on_failure: step.on_failure === 'continue' ? 'continue' : undefined,
                delay_before_seconds: index && step.delay_before_seconds > 0 ? step.delay_before_seconds : undefined,
                retry_count: this.normalizeRetryCount(step.retry_count) || undefined,
                retry_delay_seconds: step.retry_delay_seconds > 0 ? step.retry_delay_seconds : undefined,
                timeout_seconds: step.timeout_seconds > 0 ? step.timeout_seconds : undefined
            };
            if (typeof step.step_id === 'string' && step.step_id.trim()) collectedStep.step_id = step.step_id.trim();
            return collectedStep;
        }) };
        const optional = (key, value) => { if (value) data[key] = value; };
        optional('description', $('#runBookDescription').val().trim()); optional('cron_schedule', $('#runBookCron').val().trim()); optional('bearer_token', $('#runBookToken').val().trim()); optional('notify_webhook', $('#runBookWebhook').val().trim());
        const timeout = Number($('#runBookTimeout').val()); if (timeout > 0) data.timeout_seconds = timeout;
        data.summary_format = $('#runBookFormat').val();
        if ($('#runBookEmailEnabled').is(':checked')) {
            const to = $('#runBookEmailTo').val().split(',').map(value => value.trim()).filter(Boolean);
            if (!to.length) { Utils.showError('RunBook email requires at least one To recipient'); return null; }
            data.notify_email = { to, cc: $('#runBookEmailCc').val().split(',').map(value => value.trim()).filter(Boolean), subject: $('#runBookEmailSubject').val().trim(), on_success: $('#runBookEmailSuccess').is(':checked'), on_failure: $('#runBookEmailFailure').is(':checked'), include_output: $('#runBookEmailOutput').is(':checked') };
            const lines = Number($('#runBookEmailLines').val()); if (lines > 0) data.notify_email.max_output_lines = lines;
        }
        return data;
    },

    async save(runAfter) {
        const data = this.collect(); if (!data) return;
        try {
            Utils.showLoading();
            const response = this.editingId ? await API.config.updateRunBook(this.editingId, data) : await API.config.createRunBook(data);
            if (!response.success) throw new Error(response.error || 'Save failed');
            Utils.showSuccess('RunBook saved successfully');
            await this.loadData();
            if (runAfter) await this.start(data.runbook_id);
        } catch (error) { Utils.showError(error.message || 'Failed to save RunBook'); } finally { Utils.hideLoading(); }
    },

    async remove(id) {
        if (!await Utils.confirm('Delete RunBook', `Delete RunBook "${id}"?`, 'Delete')) return;
        try { await API.config.deleteRunBook(id); Utils.showSuccess('RunBook deleted'); await this.loadData(); } catch (error) { Utils.showError(error.message || 'Failed to delete RunBook'); }
    },

    async start(id) {
        try {
            const response = await API.ops.startRunBook(id);
            this.showRun(id, response.data.run_id);
        } catch (error) { Utils.showError(error.message || 'Failed to start RunBook'); }
    },

    async showRun(id, runId) {
        this.clearPoll();
        this.resetStepLogs();
        const render = async () => {
            try {
                const response = await API.ops.getRunBookRun(id, runId); const run = response.data;
                const terminal = this.isTerminalRunStatus(run.status);
                const view = document.getElementById('runBooksView');
                this.captureStepLogScroll(view);
                $('#runBooksView').html(`<div class="runbook-run-header"><button class="btn btn-link ps-0" onclick="RunBooks.loadData()"><i class="fas fa-arrow-left me-2"></i>RunBooks</button><div class="d-flex justify-content-between align-items-start"><div><h1>${this.escape(id)}</h1><p>Run ${this.escape(run.run_id)}</p></div>${this.statusBadge(run.status)}</div>${run.error ? `<div class="alert alert-danger mt-2 mb-0">${this.escape(run.error)}</div>` : ''}</div>
                    ${this.runDetailsHtml(id, run, { allowEdit: true })}
                    <div class="runbook-builder-actions"><button class="btn btn-secondary" onclick="RunBooks.loadData()">Back</button>${!terminal ? `<button class="btn btn-danger" onclick="RunBooks.stop('${this.escape(id)}','${this.escape(runId)}')">Stop after current step</button>` : `<button class="btn btn-primary" onclick="RunBooks.start('${this.escape(id)}')"><i class="fas fa-redo me-2"></i>Run again</button>`}</div>`);
                this.afterRunRender(view, run);
                if (terminal) { this.clearPoll(); await this.refreshHistory(id); }
            } catch (error) { this.clearPoll(); Utils.showError('Failed to refresh RunBook progress'); }
        };
        await render(); this.pollTimer = setInterval(render, 1000);
    },
    isTerminalRunStatus(status) { return ['succeeded','failed','timed_out','stopped','skipped','interrupted'].includes(status); },

    // Progress card + step timeline for one run. Shared by the RunBooks page and the Overview run popup.
    // options.allowEdit: show the "Change the time limit" link (needs the RunBooks page). Each step with a log gets a Log panel.
    runDetailsHtml(id, run, options = {}) {
        const allowEdit = options.allowEdit !== false;
        const steps = run.steps || [];
        const stepsHtml = steps.map((step, index) => `<div class="runbook-run-step is-${step.status}"><div class="runbook-step-number">${index + 1}</div><div class="flex-grow-1"><div class="d-flex justify-content-between"><strong>${this.escape(step.label || this.stepTarget(step))}</strong>${this.statusBadge(step.status)}</div><small class="text-muted">${step.request_id ? '<i class="fas fa-globe me-1" title="HTTP request"></i>' : ''}${this.escape(this.stepTarget(step))} ${step.duration ? `• ${this.escape(step.duration)}` : ''}</small>${step.error ? `<div class="alert alert-danger mt-2 mb-0">${this.escape(step.error)}${allowEdit && step.status === 'timed_out' ? `<div class="mt-2"><a href="#" class="alert-link" onclick="RunBooks.edit('${this.escape(id)}'); return false;"><i class="fas fa-edit me-1"></i>Change the time limit</a></div>` : ''}</div>` : ''}${this.stepLogHtml(step)}</div></div>`).join('');
        return `<div class="card mb-3"><div class="card-body"><div class="progress mb-2"><div class="progress-bar" style="width:${run.total_steps ? Math.round((run.current_step / run.total_steps) * 100) : 0}%"></div></div><div class="d-flex justify-content-between"><span>Step ${run.current_step} of ${run.total_steps}</span><span>${this.escape(run.duration || 'Running')}</span></div></div></div>
                    <div class="runbook-timeline">${stepsHtml}</div>`;
    },
    // ---- Step Log panel: one per finished step, replacing the old "Output" + "View log" pair. ----
    // Only a window of the log file is fetched (the tail first, then earlier chunks on request), so
    // multi-MB logs never land in the page whole; Download fetches the complete file.
    STEP_LOG_CHUNK_BYTES: 128 * 1024,
    STEP_LOG_MAX_LOADED_BYTES: 2 * 1024 * 1024,
    stepLogs: {},            // trace_id -> panel state, kept across the 1-2 s run refreshes
    stepLogAutoOpened: {},   // trace_id -> true once a failed step's panel was opened automatically

    resetStepLogs() { this.stepLogs = {}; this.stepLogAutoOpened = {}; },

    stepLogHtml(step) {
        if (!step.trace_id) {
            // No log file for this step: keep showing the output saved with the run.
            return step.output ? `<details class="mt-2"><summary>Output</summary><pre class="code-output mt-2">${this.escape(step.output)}</pre></details>` : '';
        }
        const state = this.stepLogs[step.trace_id] || (this.stepLogs[step.trace_id] = { open: false });
        state.savedOutput = step.output || '';
        return `<div class="runbook-step-logbox" data-trace-id="${this.escape(step.trace_id)}">${this.stepLogInnerHtml(step.trace_id)}</div>`;
    },

    stepLogInnerHtml(traceId) {
        const state = this.stepLogs[traceId] || { open: false };
        const id = this.escape(traceId);
        const bar = `<div class="runbook-step-log-bar"><button type="button" class="btn btn-sm btn-outline-secondary" onclick="RunBooks.toggleStepLog('${id}')" aria-expanded="${state.open ? 'true' : 'false'}" title="${state.open ? 'Hide' : 'Show'} the log of this step"><i class="fas fa-chevron-${state.open ? 'down' : 'right'} me-1"></i>Log</button><button type="button" class="btn btn-sm btn-outline-primary" onclick="RunBooks.downloadStepLog('${id}', this)" title="Download the complete log file"><i class="fas fa-download me-1"></i>Download</button></div>`;
        if (!state.open) return bar;
        const copy = `<button type="button" class="copy-icon-btn" onclick="RunBooks.copyStepLog('${id}', this)" title="Copy to Clipboard" aria-label="Copy to Clipboard"><i class="far fa-copy"></i></button>`;
        let body;
        if (state.error) {
            body = state.savedOutput
                ? `<div class="runbook-step-log-status is-warning"><span><i class="fas fa-exclamation-triangle me-1"></i>Log file not available (${this.escape(state.error)}) — showing the output saved with the run (may be cut at the command's max output size)</span>${copy}</div><pre class="runbook-step-log-text">${this.escape(state.savedOutput)}</pre>`
                : `<div class="runbook-step-log-status is-error"><span><i class="fas fa-exclamation-circle me-1"></i>Could not load the log: ${this.escape(state.error)}</span><a href="#" onclick="RunBooks.loadStepLog('${id}', false); return false;">Try again</a></div>`;
        } else if (!state.loaded) {
            body = `<div class="runbook-step-log-status"><span><i class="fas fa-spinner fa-spin me-1"></i>Loading log…</span></div>`;
        } else {
            const loadedBytes = state.end - state.offset;
            const whole = state.offset === 0 && state.end === state.total;
            const summary = whole
                ? `Whole log · ${state.lines.toLocaleString()} lines · ${Utils.formatFileSize(state.total)}`
                : `Showing the last ${state.lines.toLocaleString()} lines · ${Utils.formatFileSize(loadedBytes)} of ${Utils.formatFileSize(state.total)}`;
            let more = '';
            if (state.offset > 0 && loadedBytes >= this.STEP_LOG_MAX_LOADED_BYTES) {
                more = `<span class="text-muted">Only the last ${Utils.formatFileSize(this.STEP_LOG_MAX_LOADED_BYTES)} is shown here. Download the log to see the rest.</span>`;
            } else if (state.offset > 0) {
                more = `<button type="button" class="btn btn-sm btn-link p-0" onclick="RunBooks.loadStepLog('${id}', true)" ${state.loading ? 'disabled' : ''}>${state.loading ? '<i class="fas fa-spinner fa-spin me-1"></i>Loading…' : '<i class="fas fa-arrow-up me-1"></i>Load earlier'}</button>`;
            }
            body = `<div class="runbook-step-log-status"><span>${summary}</span><span class="runbook-step-log-actions">${more}${copy}</span></div><pre class="runbook-step-log-text">${this.escape(state.text)}</pre>`;
        }
        return `${bar}<div class="runbook-step-log">${body}</div>`;
    },

    // Redraw one step's panel in place (both the RunBooks page and the Overview popup), keeping its scroll.
    refreshStepLog(traceId) {
        document.querySelectorAll('.runbook-step-logbox').forEach(box => {
            if (box.dataset.traceId !== traceId) return;
            const oldPre = box.querySelector('.runbook-step-log-text');
            const oldTop = oldPre ? oldPre.scrollTop : 0;
            const oldHeight = oldPre ? oldPre.scrollHeight : 0;
            box.innerHTML = this.stepLogInnerHtml(traceId);
            this.restoreStepLogScroll(box, traceId, oldPre ? { top: oldTop, height: oldHeight } : null);
        });
        const state = this.stepLogs[traceId];
        if (state) { state.scrollToBottom = false; state.keepPosition = false; }
    },

    restoreStepLogScroll(box, traceId, previous) {
        const state = this.stepLogs[traceId];
        const pre = box.querySelector('.runbook-step-log-text');
        if (!state || !pre) return;
        if (state.scrollToBottom) pre.scrollTop = pre.scrollHeight;
        else if (state.keepPosition && previous) pre.scrollTop = previous.top + (pre.scrollHeight - previous.height);
        else if (previous) pre.scrollTop = previous.top;
        else if (typeof state.scrollTop === 'number') pre.scrollTop = state.scrollTop;
    },

    toggleStepLog(traceId) {
        const state = this.stepLogs[traceId] || (this.stepLogs[traceId] = { open: false });
        state.open = !state.open;
        this.refreshStepLog(traceId);
        if (state.open && !state.loaded && !state.loading) this.loadStepLog(traceId, false);
    },

    // earlier=false loads the tail; earlier=true prepends the chunk before what is already shown.
    async loadStepLog(traceId, earlier) {
        const state = this.stepLogs[traceId];
        if (!state || state.loading) return;
        state.loading = true;
        if (!earlier) state.error = '';
        this.refreshStepLog(traceId);
        try {
            const response = await API.ops.getLogChunk(traceId, this.STEP_LOG_CHUNK_BYTES, earlier ? state.offset : undefined);
            const data = response.data || {};
            const content = String(data.content || '');
            if (earlier) {
                state.text = content + state.text;
                state.keepPosition = true;
            } else {
                state.text = content;
                state.end = Number(data.end) || 0;
                state.scrollToBottom = true;
            }
            state.offset = Number(data.offset) || 0;
            state.total = Number(data.total_size) || 0;
            state.lines = this.countLines(state.text);
            state.loaded = true;
            state.error = '';
        } catch (error) {
            if (earlier) Utils.showError(error.message || 'Failed to load the earlier part of the log');
            else state.error = error.status === 404 ? 'it was deleted or moved' : (error.message || 'request failed');
        }
        state.loading = false;
        this.refreshStepLog(traceId);
    },

    countLines(text) {
        if (!text) return 0;
        let count = 0;
        for (let index = text.indexOf('\n'); index !== -1; index = text.indexOf('\n', index + 1)) count++;
        return text.endsWith('\n') ? count : count + 1;
    },

    // Copies the text shown in the panel (the loaded part of the log, or the saved output fallback).
    async copyStepLog(traceId, button) {
        const state = this.stepLogs[traceId];
        if (!state) return;
        await Utils.copyToClipboardButton(state.error ? (state.savedOutput || '') : (state.text || ''), button);
    },

    async downloadStepLog(traceId, button) {
        const original = button ? button.innerHTML : '';
        if (button) { button.disabled = true; button.innerHTML = '<i class="fas fa-spinner fa-spin me-1"></i>Downloading…'; }
        try {
            await API.ops.downloadLog(traceId);
        } catch (error) {
            Utils.showError(error.message || 'Failed to download the log');
        } finally {
            if (button && button.isConnected) { button.disabled = false; button.innerHTML = original; }
        }
    },

    // Call before replacing run HTML, so the redraw keeps each open panel's scroll position.
    captureStepLogScroll(container) {
        if (!container) return;
        container.querySelectorAll('.runbook-step-logbox').forEach(box => {
            const state = this.stepLogs[box.dataset.traceId];
            const pre = box.querySelector('.runbook-step-log-text');
            if (state && pre) state.scrollTop = pre.scrollTop;
        });
    },

    // Call after replacing run HTML: restore scroll and open the log of failed steps once.
    afterRunRender(container, run) {
        if (!container) return;
        container.querySelectorAll('.runbook-step-logbox').forEach(box => this.restoreStepLogScroll(box, box.dataset.traceId, null));
        (run.steps || []).forEach(step => {
            if (!step.trace_id || !['failed', 'timed_out'].includes(step.status) || this.stepLogAutoOpened[step.trace_id]) return;
            this.stepLogAutoOpened[step.trace_id] = true;
            const state = this.stepLogs[step.trace_id];
            if (state && !state.open) this.toggleStepLog(step.trace_id);
        });
    },
    async stop(id, runId) { try { await API.ops.stopRunBook(id, runId); Utils.showSuccess('RunBook will stop after the current step'); } catch (error) { Utils.showError(error.message || 'Unable to stop RunBook'); } },
    async refreshHistory(id) { const response = await API.ops.getRunBookRuns(id); this.runs[id] = response.data || []; },
    async showHistory(id) {
        await this.refreshHistory(id); const runs = this.runs[id] || [];
        $('#runBooksView').html(`<div class="commands-page-header"><div><button class="btn btn-link ps-0" onclick="RunBooks.renderList()"><i class="fas fa-arrow-left me-2"></i>RunBooks</button><h1>${this.escape(id)} history</h1></div></div><div class="commands-table-card"><table class="commands-table"><thead><tr><th>Started</th><th>Trigger</th><th>Status</th><th>Duration</th><th>Steps</th><th></th></tr></thead><tbody>${runs.map(run => `<tr><td>${Utils.formatDate(run.started_at)}</td><td>${this.escape(run.trigger)}</td><td>${this.statusBadge(run.status)}</td><td>${this.escape(run.duration || '—')}</td><td>${run.success_count} succeeded / ${run.failure_count} failed</td><td><button class="btn btn-sm btn-outline-primary" onclick="RunBooks.showRun('${this.escape(id)}','${this.escape(run.run_id)}')">View</button></td></tr>`).join('') || '<tr><td colspan="6" class="text-center py-4 text-muted">No runs yet</td></tr>'}</tbody></table></div>`);
    },
    statusLabel(status) { return ({ timed_out: 'Timed out', interrupted: 'Interrupted (server restarted)', 'never run': 'Never run', succeeded: 'Succeeded', failed: 'Failed', running: 'Running', retrying: 'Retrying', stopped: 'Stopped', stopping: 'Stopping', skipped: 'Skipped', queued: 'Queued', pending: 'Pending' })[status] || String(status || '').replace(/_/g, ' '); },
    statusBadge(status) { const tone = status === 'succeeded' ? 'success' : ['failed','timed_out','interrupted'].includes(status) ? 'danger' : ['running','stopping','retrying'].includes(status) ? 'primary' : 'secondary'; return `<span class="badge bg-${tone}" title="${this.escape(status)}">${this.escape(this.statusLabel(status))}</span>`; },
    clearPoll() { if (this.pollTimer) { clearInterval(this.pollTimer); this.pollTimer = null; } },
    cleanup() { this.clearPoll(); this.closeRowActionsMenu(); },
    escape(value) { return String(value ?? '').replace(/[&<>"']/g, character => ({ '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#039;' })[character]); }
};

window.RunBooks = RunBooks;
