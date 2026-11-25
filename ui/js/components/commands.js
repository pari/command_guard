// Commands Component
const Commands = {
    currentCommands: [],
    currentHosts: {},
    currentConfig: null,
    editingCommandOriginal: null,
    listState: {
        search: '',
        host: 'all',
        schedule: 'all',
        sortKey: 'cmd_id',
        sortDirection: 'asc',
        page: 1,
        pageSize: 10
    },
    selectedCommandIds: new Set(),
    activeActionsMenu: null,

    // Generate a unique command ID
    generateCommandId() {
        const now = new Date();
        const year = now.getFullYear();
        const month = String(now.getMonth() + 1).padStart(2, '0');
        const day = String(now.getDate()).padStart(2, '0');
        const hours = String(now.getHours()).padStart(2, '0');
        const minutes = String(now.getMinutes()).padStart(2, '0');
        const seconds = String(now.getSeconds()).padStart(2, '0');

        // Base ID: cmd-YYYYMMDD-HHMMSS
        let baseId = `cmd-${year}${month}${day}-${hours}${minutes}${seconds}`;

        // Check if this ID already exists, if so add a suffix
        let candidateId = baseId;
        let suffix = 1;
        while (this.currentCommands.some(c => c.cmd_id === candidateId)) {
            candidateId = `${baseId}-${suffix}`;
            suffix++;
        }

        return candidateId;
    },
    
    // Initialize commands page
    async init() {
        try {
            // Clear cached data to force refresh
            this.currentCommands = [];
            this.currentHosts = {};
            this.currentConfig = null;
            this.listState = {
                search: '',
                host: 'all',
                schedule: 'all',
                sortKey: 'cmd_id',
                sortDirection: 'asc',
                page: 1,
                pageSize: 10
            };
            this.selectedCommandIds.clear();
            this.closeRowActionsMenu();
            
            this.render();
            await this.loadData();
        } catch (error) {
            console.error('Commands init error:', error);
            Utils.showError('Failed to load commands');
        }
    },
    
    // Render commands layout
    render() {
        const html = `
            <div class="commands-container commands-page">
                <div class="commands-page-header">
                    <div>
                        <h1>Commands</h1>
                        <p>Manage and execute system commands across your infrastructure.</p>
                    </div>
                    <button class="btn btn-primary commands-add-button" onclick="Commands.showAddModal()">
                        <i class="fas fa-plus me-2"></i>New command
                    </button>
                </div>

                <div class="commands-toolbar" aria-label="Command filters">
                    <div class="commands-search-wrap">
                        <i class="fas fa-search" aria-hidden="true"></i>
                        <input type="search" class="form-control" id="commandsSearch"
                               placeholder="Search commands..." aria-label="Search commands"
                               oninput="Commands.updateSearch(this.value)">
                    </div>
                    <div class="commands-filter-field">
                        <label for="commandsHostFilter">Host</label>
                        <select class="form-select" id="commandsHostFilter" onchange="Commands.updateFilter('host', this.value)">
                            <option value="all">All hosts</option>
                        </select>
                    </div>
                    <div class="commands-filter-field">
                        <label for="commandsScheduleFilter">Schedule</label>
                        <select class="form-select" id="commandsScheduleFilter" onchange="Commands.updateFilter('schedule', this.value)">
                            <option value="all">All schedules</option>
                            <option value="scheduled">Scheduled</option>
                            <option value="manual">Manual / No schedule</option>
                            <option value="daily">Daily</option>
                            <option value="weekly">Weekly</option>
                            <option value="monthly">Monthly</option>
                            <option value="custom">Custom cron</option>
                        </select>
                    </div>
                    <button class="btn commands-clear-button" type="button" onclick="Commands.clearFilters()">
                        <i class="fas fa-filter me-2"></i>Clear filters
                    </button>
                </div>

                <div id="commandsSummary" class="commands-summary" aria-label="Commands summary"></div>

                <div id="commandsListContainer">
                    <div class="text-center py-5">
                        <i class="fas fa-spinner fa-spin fa-2x text-primary"></i>
                        <p class="mt-3 text-muted">Loading commands...</p>
                    </div>
                </div>
            </div>
            
            <!-- Add/Edit Command Modal -->
            <div class="modal fade" id="commandModal" tabindex="-1">
                <div class="modal-dialog modal-xl">
                    <div class="modal-content">
                        <div class="modal-header">
                            <h5 class="modal-title" id="commandModalTitle">Add Command</h5>
                            <button type="button" class="btn-close" data-bs-dismiss="modal"></button>
                        </div>
                        <div class="modal-body">
                            <form id="commandForm">
                                <!-- Nav tabs -->
                                <ul class="nav nav-tabs mb-3" id="commandTabs" role="tablist">
                                    <li class="nav-item" role="presentation">
                                        <button class="nav-link active" id="basic-tab" data-bs-toggle="tab" data-bs-target="#basic" type="button" role="tab">
                                            <i class="fas fa-terminal me-2"></i>Basic
                                        </button>
                                    </li>
                                    <li class="nav-item" role="presentation">
                                        <button class="nav-link" id="scheduling-tab" data-bs-toggle="tab" data-bs-target="#scheduling" type="button" role="tab">
                                            <i class="fas fa-clock me-2"></i>Scheduling & Security
                                        </button>
                                    </li>
                                    <li class="nav-item" role="presentation">
                                        <button class="nav-link" id="notifications-tab" data-bs-toggle="tab" data-bs-target="#notifications" type="button" role="tab">
                                            <i class="fas fa-bell me-2"></i>Notifications
                                        </button>
                                    </li>
                                    <li class="nav-item" role="presentation">
                                        <button class="nav-link" id="recentlogs-tab" data-bs-toggle="tab" data-bs-target="#recentlogs" type="button" role="tab">
                                            <i class="fas fa-file-alt me-2"></i>Recent Logs
                                        </button>
                                    </li>
                                </ul>
                                
                                <!-- Tab content -->
                                <div class="tab-content" id="commandTabContent">
                                    <!-- Basic Tab -->
                                    <div class="tab-pane fade show active" id="basic" role="tabpanel">
                                        <div class="row">
                                            <div class="col-md-6">
                                                <div class="mb-3">
                                                    <label class="form-label">Command ID *</label>
                                                    <input type="text" class="form-control" id="cmdId" required pattern="[a-zA-Z0-9-]+" title="Only letters, numbers, and hyphens (-) are allowed. Underscores (_) are not permitted.">
                                                    <div class="form-text">Unique ID - cannot be changed after creation (letters, numbers, and hyphens only)</div>
                                                </div>
                                            </div>
                                            <div class="col-md-6">
                                                <div class="mb-3">
                                                    <label class="form-label">Host *</label>
                                                    <select class="form-select" id="cmdHost" required>
                                                        <option value="">Select host...</option>
                                                    </select>
                                                </div>
                                            </div>
                                        </div>
                                        
                                        <div class="mb-3">
                                            <label class="form-label">Description</label>
                                            <input type="text" class="form-control" id="cmdDescription" placeholder="Brief description of what this command does">
                                            <div class="form-text">Brief description of what this command does</div>
                                        </div>
                                        
                                        <div class="mb-3">
                                            <label class="form-label">Command *</label>
                                            <textarea class="form-control font-monospace" id="cmdCommand" rows="6" required placeholder="e.g., df -h"></textarea>
                                            <div class="form-text">The command to execute on the remote host</div>
                                        </div>

                                        <div class="mb-3">
                                            <label class="form-label">Log Storage</label>
                                            <select class="form-select" id="cmdLogStorage">
                                                <option value="local">Local</option>
                                                <option value="s3">S3</option>
                                            </select>
                                            <div class="form-text">S3 uploads the completed log file and removes the local copy after upload succeeds</div>
                                        </div>
                                    </div>
                                    
                                    <!-- Scheduling & Security Tab -->
                                    <div class="tab-pane fade" id="scheduling" role="tabpanel">
                                        <div class="row">
                                            <div class="col-md-6">
                                                <div class="mb-3">
                                                    <label class="form-label">Schedule (Cron)</label>
                                                    <input type="text" class="form-control font-monospace" id="cmdSchedule" placeholder="e.g., 0 2 * * *">
                                                    <div class="form-text">Leave empty to disable scheduling</div>
                                                </div>
                                            </div>
                                            <div class="col-md-6">
                                                <div class="mb-3">
                                                    <label class="form-label">Bearer Token</label>
                                                    <input type="text" class="form-control" id="cmdBearerToken" placeholder="Optional security token">
                                                    <div class="form-text">Required for API execution</div>
                                                </div>
                                            </div>
                                        </div>
                                        
                                        <div class="row">
                                            <div class="col-md-6">
                                                <div class="mb-3">
                                                    <label class="form-label">Max Output Size</label>
                                                    <input type="text" class="form-control" id="cmdMaxOutput" placeholder="e.g., 1MB">
                                                    <div class="form-text">Limit output size (B, KB, MB, GB)</div>
                                                </div>
                                            </div>
                                            <div class="col-md-6">
                                                <div class="mb-3">
                                                    <label class="form-label">Webhook URL</label>
                                                    <input type="url" class="form-control" id="cmdWebhook" placeholder="https://example.com/webhook">
                                                    <div class="form-text">POST notification after execution</div>
                                                </div>
                                            </div>
                                        </div>

                                        <div class="row">
                                            <div class="col-md-6">
                                                <div class="mb-3">
                                                    <label class="form-label" for="cmdTimeout">Timeout (seconds)</label>
                                                    <div class="input-group">
                                                        <input type="number" class="form-control" id="cmdTimeout" min="0" step="1" placeholder="No limit">
                                                        <span class="input-group-text">s</span>
                                                    </div>
                                                    <div class="form-text">Stop waiting for the command after this many seconds and report it as timed out. Leave empty for no limit. Applies to scheduled, API and RunBook runs.</div>
                                                </div>
                                            </div>
                                        </div>

                                        <!-- Skip if running option -->
                                        <div class="row">
                                            <div class="col-12">
                                                <div class="form-check mt-3">
                                                    <input class="form-check-input" type="checkbox" id="cmdSkipIfRunning">
                                                    <label class="form-check-label" for="cmdSkipIfRunning">
                                                        <strong>Skip if already running</strong>
                                                    </label>
                                                    <div class="form-text">
                                                        When enabled, new execution attempts will be skipped if a previous instance is still running.
                                                        Useful for scheduled commands to prevent overlapping executions.
                                                    </div>
                                                </div>
                                            </div>
                                        </div>

                                        <!-- Common cron patterns helper -->
                                        <div class="alert alert-info">
                                            <h6 class="alert-heading">Common Cron Patterns</h6>
                                            <div class="small">
                                                <code>0 * * * *</code> - Every hour<br>
                                                <code>0 0 * * *</code> - Daily at midnight<br>
                                                <code>0 2 * * *</code> - Daily at 2 AM<br>
                                                <code>0 0 * * 1</code> - Weekly on Monday<br>
                                                <code>0 0 1 * *</code> - Monthly on 1st
                                            </div>
                                        </div>
                                    </div>
                                    
                                    <!-- Notifications Tab -->
                                    <div class="tab-pane fade" id="notifications" role="tabpanel">
                                        <h6 class="mb-3">Email Notification Settings</h6>

                                        <div class="row">
                                            <div class="col-md-6">
                                                <div class="mb-3">
                                                    <label class="form-label">To (comma-separated)</label>
                                                    <input type="text" class="form-control" id="cmdEmailTo" placeholder="email1@example.com, email2@example.com">
                                                    <div class="form-text">Leave empty to disable email notifications</div>
                                                </div>
                                            </div>
                                            <div class="col-md-6">
                                                <div class="mb-3">
                                                    <label class="form-label">Subject</label>
                                                    <input type="text" class="form-control" id="cmdEmailSubject" placeholder="{{status}} - {{cmd_id}} on {{host}}">
                                                    <div class="form-text">Use variables: {{status}}, {{cmd_id}}, {{host}}</div>
                                                </div>
                                            </div>
                                        </div>

                                        <div class="row mb-3">
                                            <div class="col-md-4">
                                                <div class="form-check">
                                                    <input class="form-check-input" type="checkbox" id="cmdEmailOnSuccess" checked>
                                                    <label class="form-check-label" for="cmdEmailOnSuccess">Send on Success</label>
                                                </div>
                                            </div>
                                            <div class="col-md-4">
                                                <div class="form-check">
                                                    <input class="form-check-input" type="checkbox" id="cmdEmailOnFailure" checked>
                                                    <label class="form-check-label" for="cmdEmailOnFailure">Send on Failure</label>
                                                </div>
                                            </div>
                                            <div class="col-md-4">
                                                <div class="form-check">
                                                    <input class="form-check-input" type="checkbox" id="cmdEmailIncludeOutput" checked>
                                                    <label class="form-check-label" for="cmdEmailIncludeOutput">Include Output</label>
                                                </div>
                                            </div>
                                        </div>

                                        <div class="row">
                                            <div class="col-md-6">
                                                <div class="mb-3">
                                                    <label class="form-label">Max Output Lines</label>
                                                    <input type="number" class="form-control" id="cmdEmailMaxOutputLines" min="0" placeholder="e.g., 100">
                                                    <div class="form-text">Limit output to this many lines in email (0 or empty for no limit)</div>
                                                </div>
                                            </div>
                                        </div>
                                    </div>

                                    <!-- Recent Logs Tab -->
                                    <div class="tab-pane fade" id="recentlogs" role="tabpanel">
                                        <div id="recentLogsContainer">
                                            <div class="text-center py-4 text-muted">
                                                <i class="fas fa-file-alt fa-2x mb-2"></i>
                                                <p>Recent execution logs will appear here when editing a command.</p>
                                            </div>
                                        </div>
                                    </div>
                                </div>
                            </form>
                        </div>
                        <div class="modal-footer">
                            <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Cancel</button>
                            <button type="button" class="btn btn-success" id="btnSaveAndRun" onclick="Commands.saveAndRunCommand()">
                                <i class="fas fa-play"></i> Save & Run
                            </button>
                            <button type="button" class="btn btn-primary" id="btnSaveCommand" onclick="Commands.saveCommand()">
                                <i class="fas fa-save"></i> Save Command
                            </button>
                        </div>
                    </div>
                </div>
            </div>
            
            <!-- Execution Modal -->
            <div class="modal fade" id="executeModal" tabindex="-1">
                <div class="modal-dialog modal-lg">
                    <div class="modal-content">
                        <div class="modal-header">
                            <h5 class="modal-title">Command Execution Result</h5>
                            <button type="button" class="btn-close" data-bs-dismiss="modal"></button>
                        </div>
                        <div class="modal-body">
                            <div id="executionOutput">
                                <!-- Execution results will be displayed here -->
                            </div>
                        </div>
                        <div class="modal-footer">
                            <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Close</button>
                        </div>
                    </div>
                </div>
            </div>
        `;
        
        $('#mainContent').html(html);
    },
    
    // Load data
    // Timeout (seconds) field → integer for the save payload; empty, 0 or invalid = no limit (field omitted)
    readTimeoutSeconds() {
        const value = Math.floor(Number($('#cmdTimeout').val()));
        return Number.isFinite(value) && value > 0 ? value : undefined;
    },

    async loadData() {
        try {
            const response = await API.config.get();
            
            if (response.success && response.data) {
                this.currentConfig = response.data;
                this.currentCommands = response.data.commands || [];
                this.currentHosts = response.data.hosts || {};
                const currentIds = new Set(this.currentCommands.map(cmd => cmd.cmd_id));
                this.selectedCommandIds = new Set([...this.selectedCommandIds].filter(cmdId => currentIds.has(cmdId)));
                this.populateListHostFilter();
                this.renderCommandsList();
                this.populateHostDropdown();
            } else {
                Utils.showError('Failed to load configuration');
            }
        } catch (error) {
            console.error('Error loading data:', error);
            Utils.showError('Failed to load data');
        }
    },
    
    getCommandRunBookCount(cmdId) {
        return (this.currentConfig?.runbooks || []).filter(runBook =>
            (runBook.steps || []).some(step => step.cmd_id === cmdId)
        ).length;
    },
    
    populateListHostFilter() {
        const select = $('#commandsHostFilter');
        if (!select.length) return;

        select.find('option:not(:first)').remove();
        Object.keys(this.currentHosts).sort((a, b) => a.localeCompare(b)).forEach(hostId => {
            select.append($('<option>', { value: hostId, text: hostId }));
        });
        select.val(this.listState.host);
    },

    updateSearch(value) {
        this.listState.search = value.trim().toLowerCase();
        this.listState.page = 1;
        this.renderCommandsList();
    },

    updateFilter(filter, value) {
        if (!['host', 'schedule'].includes(filter)) return;
        this.listState[filter] = value;
        this.listState.page = 1;
        this.renderCommandsList();
    },

    clearFilters() {
        this.listState.search = '';
        this.listState.host = 'all';
        this.listState.schedule = 'all';
        this.listState.page = 1;
        $('#commandsSearch').val('');
        $('#commandsHostFilter').val('all');
        $('#commandsScheduleFilter').val('all');
        this.renderCommandsList();
    },

    getScheduleType(schedule) {
        if (!schedule || !schedule.trim()) return 'manual';
        const fields = schedule.trim().split(/\s+/);
        if (fields.length !== 5) return 'custom';
        const [, , dayOfMonth, month, dayOfWeek] = fields;
        if (month !== '*') return 'custom';
        if (dayOfWeek !== '*') return 'weekly';
        if (dayOfMonth !== '*') return 'monthly';
        return 'daily';
    },

    commandMatchesSchedule(cmd, selectedSchedule) {
        if (selectedSchedule === 'all') return true;
        const type = this.getScheduleType(cmd.cron_schedule);
        if (selectedSchedule === 'scheduled') return type !== 'manual';
        return type === selectedSchedule;
    },

    getFilteredCommands() {
        const search = this.listState.search;
        const commands = this.currentCommands.filter(cmd => {
            const matchesSearch = !search || [cmd.cmd_id, cmd.description, cmd.host_ref]
                .some(value => String(value || '').toLowerCase().includes(search));
            const matchesHost = this.listState.host === 'all' || cmd.host_ref === this.listState.host;
            return matchesSearch && matchesHost && this.commandMatchesSchedule(cmd, this.listState.schedule);
        });

        const direction = this.listState.sortDirection === 'asc' ? 1 : -1;
        return commands.sort((a, b) => {
            let aValue;
            let bValue;
            switch (this.listState.sortKey) {
                case 'host_ref':
                    aValue = a.host_ref || '';
                    bValue = b.host_ref || '';
                    break;
                case 'cron_schedule':
                    aValue = a.cron_schedule || '';
                    bValue = b.cron_schedule || '';
                    break;
                case 'runbooks':
                    aValue = this.getCommandRunBookCount(a.cmd_id);
                    bValue = this.getCommandRunBookCount(b.cmd_id);
                    break;
                case 'description':
                    aValue = a.description || '';
                    bValue = b.description || '';
                    break;
                default:
                    aValue = a.cmd_id || '';
                    bValue = b.cmd_id || '';
            }
            if (typeof aValue === 'number') return (aValue - bValue) * direction;
            return String(aValue).localeCompare(String(bValue)) * direction;
        });
    },

    sortCommands(sortKey) {
        if (this.listState.sortKey === sortKey) {
            this.listState.sortDirection = this.listState.sortDirection === 'asc' ? 'desc' : 'asc';
        } else {
            this.listState.sortKey = sortKey;
            this.listState.sortDirection = 'asc';
        }
        this.listState.page = 1;
        this.renderCommandsList();
    },

    getSortIcon(sortKey) {
        if (this.listState.sortKey !== sortKey) return '<i class="fas fa-sort"></i>';
        return this.listState.sortDirection === 'asc'
            ? '<i class="fas fa-sort-up"></i>'
            : '<i class="fas fa-sort-down"></i>';
    },

    changePage(page) {
        const pageCount = Math.max(1, Math.ceil(this.getFilteredCommands().length / this.listState.pageSize));
        this.listState.page = Math.min(Math.max(1, page), pageCount);
        this.renderCommandsList();
    },

    changePageSize(value) {
        this.listState.pageSize = Number(value) || 10;
        this.listState.page = 1;
        this.renderCommandsList();
    },

    toggleCommandSelection(cmdId, checked) {
        if (checked) this.selectedCommandIds.add(cmdId);
        else this.selectedCommandIds.delete(cmdId);
        this.updateSelectionState();
    },

    togglePageSelection(checked) {
        $('#commandsTableBody input[data-command-select]').each((_, input) => {
            input.checked = checked;
            const cmdId = input.getAttribute('data-command-select');
            if (checked) this.selectedCommandIds.add(cmdId);
            else this.selectedCommandIds.delete(cmdId);
        });
        this.updateSelectionState();
    },

    updateSelectionState() {
        const checkboxes = $('#commandsTableBody input[data-command-select]');
        const checkedCount = checkboxes.filter(':checked').length;
        const selectAll = document.getElementById('commandsSelectAll');
        if (selectAll) {
            selectAll.checked = checkboxes.length > 0 && checkedCount === checkboxes.length;
            selectAll.indeterminate = checkedCount > 0 && checkedCount < checkboxes.length;
        }
        $('#commandsSelectedCount').text(this.selectedCommandIds.size ? `${this.selectedCommandIds.size} selected` : '');
    },

    renderCommandsSummary() {
        const scheduledCount = this.currentCommands.filter(cmd => this.getScheduleType(cmd.cron_schedule) !== 'manual').length;
        const manualCount = this.currentCommands.length - scheduledCount;
        const runBookReferences = this.currentCommands.reduce((total, cmd) => total + this.getCommandRunBookCount(cmd.cmd_id), 0);
        const usedHosts = new Set(this.currentCommands.map(cmd => cmd.host_ref).filter(Boolean)).size;
        const metrics = [
            ['fa-code', 'Total commands', this.currentCommands.length, 'primary'],
            ['fa-clock', 'Scheduled', scheduledCount, 'primary'],
            ['fa-play', 'Manual', manualCount, 'primary'],
            ['fa-book', 'RunBook references', runBookReferences, 'primary'],
            ['fa-bell', 'Notifications', this.currentCommands.filter(cmd => cmd.notify_email || cmd.notify_webhook).length, 'primary'],
            ['fa-server', 'Hosts', usedHosts, 'success']
        ];
        $('#commandsSummary').html(metrics.map(([icon, label, value, tone]) => `
            <div class="commands-summary-item">
                <span class="commands-summary-icon commands-summary-icon-${tone}"><i class="fas ${icon}"></i></span>
                <span><small>${label}</small><strong>${value}</strong></span>
            </div>
        `).join(''));
    },

    getScheduleBadge(cmd) {
        const schedule = cmd.cron_schedule;
        if (!schedule) return '<span class="commands-schedule-badge is-manual">None</span>';
        return `<span class="commands-schedule-badge"><i class="far fa-clock"></i>${this.escapeHtml(this.formatCronSchedule(schedule))}</span>`;
    },

    getRowActions(cmd) {
        const cmdId = this.escapeHtml(cmd.cmd_id);
        return `
            <div class="commands-row-actions">
                <button class="btn commands-run-button" type="button" onclick="Commands.executeCommand('${cmdId}')">
                    <i class="fas fa-play"></i><span>Run</span>
                </button>
                <button class="btn commands-more-button" type="button" data-command-id="${cmdId}"
                        aria-expanded="false" aria-haspopup="menu" aria-label="Actions for ${cmdId}"
                        onclick="Commands.toggleRowActions(this, this.dataset.commandId, event)">
                    <i class="fas fa-ellipsis-v"></i>
                </button>
            </div>`;
    },

    toggleRowActions(trigger, cmdId, event) {
        event.preventDefault();
        event.stopPropagation();

        if (this.activeActionsMenu && this.activeActionsMenu.trigger === trigger) {
            this.closeRowActionsMenu();
            return;
        }

        this.closeRowActionsMenu();

        const runBookCount = this.getCommandRunBookCount(cmdId);
        const isInUse = runBookCount > 0;
        const menu = document.createElement('div');
        menu.className = 'dropdown-menu show commands-actions-menu commands-actions-menu-portal';
        menu.setAttribute('role', 'menu');
        menu.setAttribute('aria-label', `Actions for ${cmdId}`);

        const editButton = document.createElement('button');
        editButton.className = 'dropdown-item';
        editButton.type = 'button';
        editButton.innerHTML = '<i class="fas fa-edit"></i><span>Edit</span>';
        editButton.addEventListener('click', () => {
            this.closeRowActionsMenu();
            this.editCommand(cmdId);
        });
        menu.appendChild(editButton);

        const duplicateButton = document.createElement('button');
        duplicateButton.className = 'dropdown-item';
        duplicateButton.type = 'button';
        duplicateButton.innerHTML = '<i class="fas fa-copy"></i><span>Duplicate</span>';
        duplicateButton.addEventListener('click', () => {
            this.closeRowActionsMenu();
            this.duplicateCommand(cmdId);
        });
        menu.appendChild(duplicateButton);

        const logsButton = document.createElement('button');
        logsButton.className = 'dropdown-item';
        logsButton.type = 'button';
        logsButton.innerHTML = '<i class="fas fa-history"></i><span>Logs</span>';
        logsButton.addEventListener('click', () => {
            this.closeRowActionsMenu();
            Logs.openForJob(cmdId);
        });
        menu.appendChild(logsButton);

        const divider = document.createElement('div');
        divider.className = 'dropdown-divider';
        menu.appendChild(divider);

        const deleteButton = document.createElement('button');
        deleteButton.type = 'button';
        deleteButton.innerHTML = '<i class="fas fa-trash"></i><span>Delete</span>';
        if (isInUse) {
            deleteButton.className = 'dropdown-item text-muted';
            deleteButton.disabled = true;
            deleteButton.title = `Used by ${runBookCount} RunBook(s)`;
            const status = document.createElement('span');
            status.className = 'ms-auto small';
            status.textContent = 'In use';
            deleteButton.appendChild(status);
        } else {
            deleteButton.className = 'dropdown-item text-danger';
            deleteButton.addEventListener('click', () => {
                this.closeRowActionsMenu();
                this.deleteCommand(cmdId);
            });
        }
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

    // Render the searchable, filterable command table
    renderCommandsList() {
        this.closeRowActionsMenu();
        const container = $('#commandsListContainer');
        this.renderCommandsSummary();
        
        if (this.currentCommands.length === 0) {
            container.html(`
                <div class="commands-empty-state">
                    <i class="fas fa-terminal fa-3x text-muted mb-3"></i>
                    <p class="text-muted">No commands configured yet.</p>
                    <button class="btn btn-primary" onclick="Commands.showAddModal()">
                        <i class="fas fa-plus me-1"></i> Add Your First Command
                    </button>
                </div>
            `);
            return;
        }

        const filteredCommands = this.getFilteredCommands();
        const pageCount = Math.max(1, Math.ceil(filteredCommands.length / this.listState.pageSize));
        if (this.listState.page > pageCount) this.listState.page = pageCount;
        const startIndex = (this.listState.page - 1) * this.listState.pageSize;
        const pageCommands = filteredCommands.slice(startIndex, startIndex + this.listState.pageSize);
        const pageButtons = Array.from({ length: pageCount }, (_, index) => index + 1)
            .filter(page => page === 1 || page === pageCount || Math.abs(page - this.listState.page) <= 1);
        let lastPage = 0;
        const pagination = pageButtons.map(page => {
            const gap = lastPage && page - lastPage > 1 ? '<span class="commands-page-gap">…</span>' : '';
            lastPage = page;
            return `${gap}<button class="commands-page-number ${page === this.listState.page ? 'active' : ''}" onclick="Commands.changePage(${page})">${page}</button>`;
        }).join('');

        const rows = pageCommands.map(cmd => {
                const host = this.currentHosts[cmd.host_ref];
                const hostName = host ? `${cmd.host_ref}` : 'Unknown';
                const hostType = host ? host.connection_type : 'unknown';
                const runBookCount = this.getCommandRunBookCount(cmd.cmd_id);
                const safeId = this.escapeHtml(cmd.cmd_id || '');
                const safeHost = this.escapeHtml(hostName);
                const safeDescription = this.escapeHtml(cmd.description || 'No description');
                const checked = this.selectedCommandIds.has(cmd.cmd_id) ? 'checked' : '';
                const featureIcons = [
                    cmd.bearer_token ? '<i class="fas fa-key" title="Bearer token protected"></i>' : '',
                    cmd.notify_webhook && cmd.notify_webhook.trim() ? '<i class="fas fa-globe" title="Webhook configured"></i>' : '',
                    cmd.notify_email ? '<i class="fas fa-envelope" title="Email notification configured"></i>' : '',
                    cmd.log_storage === 's3' ? '<i class="fas fa-cloud-upload-alt" title="Logs uploaded to S3"></i>' : '',
                    cmd.timeout_seconds > 0 ? `<i class="fas fa-hourglass-half" title="Time limit: ${Number(cmd.timeout_seconds)} s"></i>` : ''
                ].filter(Boolean).join('');
                return `
                    <tr>
                        <td class="commands-checkbox-cell"><input class="form-check-input" type="checkbox" ${checked} data-command-select="${safeId}" onchange="Commands.toggleCommandSelection('${safeId}', this.checked)" aria-label="Select ${safeId}"></td>
                        <td><button type="button" class="commands-command-name d-block border-0 bg-transparent p-0 text-start" data-command-id="${safeId}" onclick="Commands.editCommand(this.dataset.commandId)" aria-label="Edit ${safeId}" title="Edit ${safeId}">${safeId}</button>${featureIcons ? `<div class="commands-feature-icons">${featureIcons}</div>` : ''}</td>
                        <td><span class="commands-description-text">${safeDescription}</span></td>
                        <td><span class="commands-host-badge is-${hostType === 'ssh' ? 'ssh' : 'winrm'}"><i class="fas fa-server"></i>${safeHost}</span></td>
                        <td>${this.getScheduleBadge(cmd)}</td>
                        <td class="commands-number-cell">${runBookCount}</td>
                        <td><span class="commands-status-badge ${cmd.cron_schedule ? 'is-scheduled' : 'is-manual'}"><span></span>${cmd.cron_schedule ? 'Scheduled' : 'Manual'}</span></td>
                        <td>${this.getRowActions(cmd)}</td>
                    </tr>`;
            }).join('');

        container.html(`
            <div class="commands-table-card">
                <div class="commands-table-scroll">
                    <table class="commands-table">
                        <thead><tr>
                            <th class="commands-checkbox-cell"><input class="form-check-input" id="commandsSelectAll" type="checkbox" onchange="Commands.togglePageSelection(this.checked)" aria-label="Select all visible commands"></th>
                            <th><button onclick="Commands.sortCommands('cmd_id')">Command ${this.getSortIcon('cmd_id')}</button></th>
                            <th><button onclick="Commands.sortCommands('description')">Description ${this.getSortIcon('description')}</button></th>
                            <th><button onclick="Commands.sortCommands('host_ref')">Host ${this.getSortIcon('host_ref')}</button></th>
                            <th><button onclick="Commands.sortCommands('cron_schedule')">Schedule ${this.getSortIcon('cron_schedule')}</button></th>
                            <th><button onclick="Commands.sortCommands('runbooks')">RunBooks ${this.getSortIcon('runbooks')}</button></th>
                            <th>Status</th>
                            <th>Actions</th>
                        </tr></thead>
                        <tbody id="commandsTableBody">${rows || `
                            <tr><td colspan="8"><div class="commands-no-results"><i class="fas fa-search"></i><strong>No commands found</strong><span>Try changing or clearing the filters.</span></div></td></tr>
                        `}</tbody>
                    </table>
                </div>
                <div class="commands-table-footer">
                    <div>
                        <span>${filteredCommands.length ? `Showing ${startIndex + 1} to ${Math.min(startIndex + this.listState.pageSize, filteredCommands.length)} of ${filteredCommands.length}` : 'Showing 0 commands'}</span>
                        <span id="commandsSelectedCount" class="commands-selected-count"></span>
                    </div>
                    <div class="commands-pagination">
                        <button class="commands-page-arrow" onclick="Commands.changePage(${this.listState.page - 1})" ${this.listState.page === 1 ? 'disabled' : ''} aria-label="Previous page"><i class="fas fa-chevron-left"></i></button>
                        ${pagination}
                        <button class="commands-page-arrow" onclick="Commands.changePage(${this.listState.page + 1})" ${this.listState.page === pageCount ? 'disabled' : ''} aria-label="Next page"><i class="fas fa-chevron-right"></i></button>
                        <select class="form-select commands-page-size" onchange="Commands.changePageSize(this.value)" aria-label="Rows per page">
                            ${[10, 25, 50].map(size => `<option value="${size}" ${size === this.listState.pageSize ? 'selected' : ''}>${size} / page</option>`).join('')}
                        </select>
                    </div>
                </div>
            </div>
        `);
        this.updateSelectionState();
    },
    
    // Format cron schedule for display
    formatCronSchedule(schedule) {
        if (!schedule) return 'None';
        
        // Common cron patterns
        const patterns = {
            '* * * * *': 'Every minute',
            '*/5 * * * *': 'Every 5 minutes',
            '*/10 * * * *': 'Every 10 minutes',
            '*/15 * * * *': 'Every 15 minutes',
            '*/30 * * * *': 'Every 30 minutes',
            '0 * * * *': 'Every hour',
            '0 0 * * *': 'Daily at midnight',
            '0 2 * * *': 'Daily at 2 AM',
            '0 0 * * 0': 'Weekly on Sunday',
            '0 0 1 * *': 'Monthly on 1st',
        };
        
        return patterns[schedule] || schedule;
    },
    
    // Escape HTML for display
    escapeHtml(text) {
        const map = {
            '&': '&amp;',
            '<': '&lt;',
            '>': '&gt;',
            '"': '&quot;',
            "'": '&#039;'
        };
        return text.replace(/[&<>"']/g, m => map[m]);
    },
    
    // Populate host dropdown (preserves current selection if valid)
    populateHostDropdown() {
        const select = $('#cmdHost');
        const currentValue = select.val(); // Preserve current selection
        select.empty();
        select.append('<option value="">Select host...</option>');

        Object.entries(this.currentHosts).forEach(([hostId, host]) => {
            const icon = host.connection_type === 'ssh' ? '🐧' : '🪟';
            select.append(`<option value="${Utils.escapeHtml(String(hostId))}">${icon} ${Utils.escapeHtml(String(hostId))} (${Utils.escapeHtml(String(host.host || ''))})</option>`);
        });

        // Restore selection if the value still exists
        if (currentValue && select.find(`option[value="${currentValue}"]`).length > 0) {
            select.val(currentValue);
        }
    },

    // Show add modal
    showAddModal() {
        this.editingCommandOriginal = null;

        $('#commandModalTitle').text('Add New Command');
        $('#commandForm')[0].reset();

        // Reset to first tab
        $('#basic-tab').tab('show');
        $('#cmdId').prop('readonly', false);
        $('#cmdId').attr('data-mode', 'create');

        // Auto-generate a unique command ID
        const generatedId = this.generateCommandId();
        $('#cmdId').val(generatedId);

        this.populateHostDropdown();
        $('#cmdLogStorage').val('local');
        $('#cmdTimeout').val('');

        // Add real-time validation for cmd_id
        this.setupCmdIdValidation();

        // Setup recent logs tab for new commands (no logs to show)
        this.setupRecentLogsTab(null);
        this.setupFormChangeListener();

        $('#commandModal').modal('show');
    },

    // Edit command
    editCommand(cmdId) {
        const cmd = this.currentCommands.find(c => c.cmd_id === cmdId);
        if (!cmd) return;
        
        // Store original for reference
        this.editingCommandOriginal = { ...cmd };
        
        $('#commandModalTitle').text('Edit Command');

        // Reset to first tab
        $('#basic-tab').tab('show');

        $('#cmdId').val(cmd.cmd_id);
        $('#cmdId').attr('data-mode', 'edit');
        $('#cmdId').attr('data-original-id', cmdId);

        // Command ID is ALWAYS locked in edit mode - IDs cannot be changed once created
        // This prevents orphaned cron jobs and broken references
        $('#cmdId').prop('readonly', true);
        $('#cmdId').attr('title', 'Command ID cannot be changed after creation');
        $('#cmdId').parent().find('.form-text').html(
            '<span class="text-muted">Command ID cannot be changed after creation</span>'
        );
        
        $('#cmdHost').val(cmd.host_ref);
        $('#cmdDescription').val(cmd.description || '');
        $('#cmdCommand').val(cmd.command);
        $('#cmdSchedule').val(cmd.cron_schedule || '');
        $('#cmdBearerToken').val(cmd.bearer_token || '');
        $('#cmdMaxOutput').val(cmd.max_output_size || '');
        $('#cmdWebhook').val(cmd.notify_webhook || '');
        $('#cmdSkipIfRunning').prop('checked', cmd.skip_if_running || false);
        $('#cmdLogStorage').val(cmd.log_storage === 's3' ? 's3' : 'local');
        $('#cmdTimeout').val(cmd.timeout_seconds > 0 ? cmd.timeout_seconds : '');

        // Email notification
        if (cmd.notify_email) {
            $('#cmdEmailTo').val((cmd.notify_email.to || []).join(', '));
            $('#cmdEmailSubject').val(cmd.notify_email.subject || '');
            $('#cmdEmailOnSuccess').prop('checked', cmd.notify_email.on_success || false);
            $('#cmdEmailOnFailure').prop('checked', cmd.notify_email.on_failure || false);
            $('#cmdEmailIncludeOutput').prop('checked', cmd.notify_email.include_output || false);
            $('#cmdEmailMaxOutputLines').val(cmd.notify_email.max_output_lines || '');
        } else {
            $('#cmdEmailTo').val('');
            $('#cmdEmailSubject').val('');
            $('#cmdEmailOnSuccess').prop('checked', false);
            $('#cmdEmailOnFailure').prop('checked', false);
            $('#cmdEmailIncludeOutput').prop('checked', false);
            $('#cmdEmailMaxOutputLines').val('');
        }

        // Add real-time validation for cmd_id
        this.setupCmdIdValidation();

        // Setup recent logs tab for existing command
        this.setupRecentLogsTab(cmdId);
        this.setupFormChangeListener();

        $('#commandModal').modal('show');
    },

    // Setup form change listener to re-enable Save & Run and Save Command buttons
    setupFormChangeListener() {
        $('#commandForm').off('input.saverun change.saverun');
        $('#commandForm').on('input.saverun change.saverun', function() {
            $('#btnSaveAndRun').prop('disabled', false);
            $('#btnSaveCommand').prop('disabled', false);
        });
    },

    // Setup recent logs tab - loads logs lazily when tab is clicked
    setupRecentLogsTab(cmdId) {
        // Reset the tab content
        $('#recentLogsContainer').html(`
            <div class="text-center py-4 text-muted">
                <i class="fas fa-file-alt fa-2x mb-2"></i>
                <p>${cmdId ? 'Click this tab to load recent logs.' : 'Save the command first to see execution logs.'}</p>
            </div>
        `);

        // Remove previous event listener
        $('#recentlogs-tab').off('shown.bs.tab');

        if (cmdId) {
            // Load logs when the tab is shown
            let logsLoaded = false;
            $('#recentlogs-tab').on('shown.bs.tab', () => {
                if (!logsLoaded) {
                    logsLoaded = true;
                    this.loadRecentLogs(cmdId);
                }
            });
        }
    },

    // Setup real-time validation for cmd_id field
    setupCmdIdValidation() {
        // Remove any existing event listener
        $('#cmdId').off('input.validation');

        // Add new event listener
        $('#cmdId').on('input.validation', function() {
            const value = $(this).val();
            const isValid = /^[a-zA-Z0-9-]*$/.test(value);

            if (!isValid && value !== '') {
                $(this).addClass('is-invalid');
                // Remove the invalid character
                const cleanedValue = value.replace(/[^a-zA-Z0-9-]/g, '');
                $(this).val(cleanedValue);
            } else {
                $(this).removeClass('is-invalid');
            }
        });
    },

    // Save command
    async saveCommand() {
        // Define commandData outside try block so it's accessible in catch block
        let commandData;

        try {
            const form = $('#commandForm')[0];
            if (!form.checkValidity()) {
                form.reportValidity();
                return;
            }

            // Validate cmd_id format (no underscores, only alphanumeric and hyphens)
            const cmdId = $('#cmdId').val().trim();
            if (!/^[a-zA-Z0-9-]+$/.test(cmdId)) {
                Utils.showError('Command ID can only contain letters, numbers, and hyphens. Underscores are not allowed.');
                $('#cmdId').focus();
                return;
            }

            const mode = $('#cmdId').attr('data-mode');
            const isNewCommand = mode === 'create';
            const originalId = $('#cmdId').attr('data-original-id');

            commandData = {
                cmd_id: cmdId,
                host_ref: $('#cmdHost').val(),
                command: $('#cmdCommand').val().trim(),
                description: $('#cmdDescription').val().trim() || undefined,
                cron_schedule: $('#cmdSchedule').val().trim() || undefined,
                bearer_token: $('#cmdBearerToken').val().trim() || undefined,
                max_output_size: $('#cmdMaxOutput').val().trim() || undefined,
                notify_webhook: $('#cmdWebhook').val().trim() || undefined,
                log_storage: $('#cmdLogStorage').val() === 's3' ? 's3' : undefined,
                skip_if_running: $('#cmdSkipIfRunning').is(':checked') || undefined,
                timeout_seconds: this.readTimeoutSeconds()
            };

            // Email notification
            const emailTo = $('#cmdEmailTo').val().trim();
            if (emailTo) {
                commandData.notify_email = {
                    to: emailTo.split(',').map(e => e.trim()).filter(e => e),
                    subject: $('#cmdEmailSubject').val().trim() || '{{status}} - {{cmd_id}} on {{host}}',
                    on_success: $('#cmdEmailOnSuccess').is(':checked'),
                    on_failure: $('#cmdEmailOnFailure').is(':checked'),
                    include_output: $('#cmdEmailIncludeOutput').is(':checked')
                };
                
                // Add max_output_lines if specified
                const maxOutputLines = parseInt($('#cmdEmailMaxOutputLines').val());
                if (!isNaN(maxOutputLines) && maxOutputLines > 0) {
                    commandData.notify_email.max_output_lines = maxOutputLines;
                }
            }
            
            Utils.showLoading();
            
            let response;
            if (isNewCommand) {
                response = await API.config.createCommand(commandData);
            } else {
                // cmd_id cannot change in edit mode (field is readonly), so always use regular update
                response = await API.config.updateCommand(commandData.cmd_id, commandData);
            }
            
            if (response.success) {
                Utils.showSuccess(isNewCommand ? 'Command created successfully' : 'Command updated successfully');
                // Disable both save buttons since nothing has changed
                $('#btnSaveCommand').prop('disabled', true);
                $('#btnSaveAndRun').prop('disabled', true);
                // Update mode to edit after successful save
                $('#cmdId').attr('data-mode', 'edit');
                $('#cmdId').attr('data-original-id', cmdId);
                // Reload data in background
                this.loadData();
            } else {
                Utils.showError(response.error || 'Failed to save command');
            }
        } catch (error) {
            console.error('Error saving command:', error);
            // Extract the actual error message from the API response
            const errorMessage = error.message || 'Failed to save command';
            
            // Check for specific error types and provide better messages
            if (errorMessage.includes('already exists') || errorMessage.includes('Command already exists')) {
                const cmdId = commandData?.cmd_id || $('#cmdId').val().trim();
                Utils.showError(`Command ID "${cmdId}" already exists. Please choose a different ID.`);
            } else if (errorMessage.includes('duplicate')) {
                const cmdId = commandData?.cmd_id || $('#cmdId').val().trim();
                Utils.showError(`Command ID "${cmdId}" is already in use. Please choose a different ID.`);
            } else {
                Utils.showError(errorMessage);
            }
        } finally {
            Utils.hideLoading();
        }
    },

    // Save and run command
    async saveAndRunCommand() {
        let commandData;

        try {
            const form = $('#commandForm')[0];
            if (!form.checkValidity()) {
                form.reportValidity();
                return;
            }

            const cmdId = $('#cmdId').val().trim();
            if (!/^[a-zA-Z0-9-]+$/.test(cmdId)) {
                Utils.showError('Command ID can only contain letters, numbers, and hyphens. Underscores are not allowed.');
                $('#cmdId').focus();
                return;
            }

            const mode = $('#cmdId').attr('data-mode');
            const isNewCommand = mode === 'create';
            const originalId = $('#cmdId').attr('data-original-id');

            commandData = {
                cmd_id: cmdId,
                host_ref: $('#cmdHost').val(),
                command: $('#cmdCommand').val().trim(),
                description: $('#cmdDescription').val().trim() || undefined,
                cron_schedule: $('#cmdSchedule').val().trim() || undefined,
                bearer_token: $('#cmdBearerToken').val().trim() || undefined,
                max_output_size: $('#cmdMaxOutput').val().trim() || undefined,
                notify_webhook: $('#cmdWebhook').val().trim() || undefined,
                log_storage: $('#cmdLogStorage').val() === 's3' ? 's3' : undefined,
                skip_if_running: $('#cmdSkipIfRunning').is(':checked') || undefined,
                timeout_seconds: this.readTimeoutSeconds()
            };

            // Email notification
            const emailTo = $('#cmdEmailTo').val().trim();
            if (emailTo) {
                commandData.notify_email = {
                    to: emailTo.split(',').map(e => e.trim()).filter(e => e),
                    subject: $('#cmdEmailSubject').val().trim() || '{{status}} - {{cmd_id}} on {{host}}',
                    on_success: $('#cmdEmailOnSuccess').is(':checked'),
                    on_failure: $('#cmdEmailOnFailure').is(':checked'),
                    include_output: $('#cmdEmailIncludeOutput').is(':checked')
                };
                const maxOutputLines = parseInt($('#cmdEmailMaxOutputLines').val());
                if (!isNaN(maxOutputLines) && maxOutputLines > 0) {
                    commandData.notify_email.max_output_lines = maxOutputLines;
                }
            }

            // Show activity indicator on the button
            const btn = $('#btnSaveAndRun');
            const originalBtnHtml = btn.html();
            btn.prop('disabled', true);
            $('#btnSaveCommand').prop('disabled', true);
            btn.html('<i class="fas fa-spinner fa-spin me-1"></i> Saving...');

            // Step 1: Save the command
            let response;
            if (isNewCommand) {
                response = await API.config.createCommand(commandData);
            } else {
                // cmd_id cannot change in edit mode (field is readonly), so always use regular update
                response = await API.config.updateCommand(commandData.cmd_id, commandData);
            }

            if (!response.success) {
                Utils.showError(response.error || 'Failed to save command');
                btn.prop('disabled', false);
                $('#btnSaveCommand').prop('disabled', false);
                btn.html(originalBtnHtml);
                return;
            }

            // Update mode to edit after successful save
            $('#cmdId').attr('data-mode', 'edit');
            $('#cmdId').attr('data-original-id', cmdId);

            // Step 2: Execute the command
            btn.html('<i class="fas fa-spinner fa-spin me-1"></i> Running...');

            const execResponse = await API.commands.execute(cmdId, {}, true);

            // Step 3: Switch to Recent Logs tab and show result
            btn.html(originalBtnHtml);
            btn.prop('disabled', true);
            $('#btnSaveCommand').prop('disabled', true);

            // Setup the recent logs tab to reload
            this.setupRecentLogsTab(cmdId);

            // Switch to the Recent Logs tab
            $('#recentlogs-tab').tab('show');

            // Load logs and auto-expand the first one
            await this.loadRecentLogs(cmdId, true);

            // Reload the commands list in background
            this.loadData();

        } catch (error) {
            console.error('Error in save and run:', error);
            const errorMessage = error.message || 'Failed to save/run command';
            if (errorMessage.includes('already exists') || errorMessage.includes('Command already exists')) {
                const cmdId = commandData?.cmd_id || $('#cmdId').val().trim();
                Utils.showError(`Command ID "${cmdId}" already exists. Please choose a different ID.`);
            } else {
                Utils.showError(errorMessage);
            }
            // Reset button state
            const btn = $('#btnSaveAndRun');
            btn.prop('disabled', false);
            $('#btnSaveCommand').prop('disabled', false);
            btn.html('<i class="fas fa-play"></i> Save & Run');
        }
    },

    // Delete command
    async deleteCommand(cmdId) {
        // Check if command is in use
        const runBookCount = this.getCommandRunBookCount(cmdId);
        const isInUse = runBookCount > 0;
        
        if (isInUse) {
            Utils.showError(`Cannot delete command "${cmdId}" - it is used by ${runBookCount} RunBook(s). Remove those references first.`);
            return;
        }
        
        if (!confirm(`Are you sure you want to delete command "${cmdId}"?`)) {
            return;
        }
        
        try {
            Utils.showLoading();
            
            const response = await API.config.deleteCommand(cmdId);
            
            if (response.success) {
                Utils.showSuccess('Command deleted successfully');
                await this.loadData();
            } else {
                Utils.showError(response.error || 'Failed to delete command');
            }
        } catch (error) {
            console.error('Error deleting command:', error);
            Utils.showError('Failed to delete command');
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Duplicate command
    duplicateCommand(cmdId) {
        const cmd = this.currentCommands.find(c => c.cmd_id === cmdId);
        if (!cmd) return;

        // Create a copy with new ID (use -copy suffix, check for conflicts)
        const newCmd = { ...cmd };
        let candidateId = `${cmd.cmd_id}-copy`;
        let suffix = 1;
        while (this.currentCommands.some(c => c.cmd_id === candidateId)) {
            candidateId = `${cmd.cmd_id}-copy-${suffix}`;
            suffix++;
        }
        newCmd.cmd_id = candidateId;

        // Populate modal with copied data
        this.editingCommandOriginal = null;

        $('#commandModalTitle').text('Add New Command (Duplicate)');
        $('#commandForm')[0].reset();
        $('#cmdId').val(newCmd.cmd_id);
        $('#cmdId').prop('readonly', false);
        $('#cmdId').attr('data-mode', 'create');
        
        $('#cmdHost').val(newCmd.host_ref);
        $('#cmdDescription').val(newCmd.description || '');
        $('#cmdCommand').val(newCmd.command);
        $('#cmdSchedule').val(newCmd.cron_schedule || '');
        $('#cmdBearerToken').val(newCmd.bearer_token || '');
        $('#cmdMaxOutput').val(newCmd.max_output_size || '');
        $('#cmdWebhook').val(newCmd.notify_webhook || '');
        $('#cmdTimeout').val(newCmd.timeout_seconds > 0 ? newCmd.timeout_seconds : '');
        
        if (newCmd.notify_email) {
            $('#cmdEmailTo').val((newCmd.notify_email.to || []).join(', '));
            $('#cmdEmailSubject').val(newCmd.notify_email.subject || '');
            $('#cmdEmailOnSuccess').prop('checked', newCmd.notify_email.on_success || false);
            $('#cmdEmailOnFailure').prop('checked', newCmd.notify_email.on_failure || false);
            $('#cmdEmailIncludeOutput').prop('checked', newCmd.notify_email.include_output || false);
            $('#cmdEmailMaxOutputLines').val(newCmd.notify_email.max_output_lines || '');
        }

        // Add real-time validation for cmd_id
        this.setupCmdIdValidation();

        // Setup recent logs tab for duplicated command (no logs yet)
        this.setupRecentLogsTab(null);
        this.setupFormChangeListener();

        $('#commandModal').modal('show');
    },

    // Execute command
    async executeCommand(cmdId) {
        const cmd = this.currentCommands.find(c => c.cmd_id === cmdId);
        if (!cmd) return;
        this.performCommandExecution(cmdId);
    },
    
    // Perform the actual command execution
    async performCommandExecution(cmdId) {
        const cmd = this.currentCommands.find(c => c.cmd_id === cmdId);
        if (!cmd) return;
        
        try {
            // Admin UI execution doesn't need to pass bearer token
            // The JWT auth token is already included and the backend skips
            // command bearer token check when JWT is present
            let headers = {};
            
            // Show loading state in modal
            $('#executionOutput').html(`
                <div class="text-center py-4">
                    <i class="fas fa-spinner fa-spin fa-2x text-primary"></i>
                    <p class="mt-3">Executing command: <strong>${cmdId}</strong>...</p>
                </div>
            `);
            $('#executeModal').modal('show');
            
            const response = await API.commands.execute(cmdId, headers);
            
            if (response.success && response.data) {
                const data = response.data;
                const isSuccess = data.exit_code === 0 || data.exit_code === undefined;
                const output = data.output || 'No output';
                const traceId = data.trace_id || 'N/A';
                const truncated = data.is_output_truncated ? ' <span class="badge bg-warning">Truncated</span>' : '';
                const execTime = data.exec_time || 'N/A';
                const host = this.currentHosts[cmd.host_ref]?.host || cmd.host_ref;
                
                const html = `
                    <div class="alert ${isSuccess ? 'alert-success' : 'alert-danger'} mb-3">
                        <div class="row">
                            <div class="col-md-6">
                                <strong><i class="fas fa-terminal"></i> Command:</strong> ${cmdId}<br>
                                <strong><i class="fas fa-server"></i> Host:</strong> ${host}<br>
                                <strong><i class="fas ${isSuccess ? 'fa-check-circle' : 'fa-times-circle'}"></i> Status:</strong> ${isSuccess ? 'Success' : 'Failed'}
                                ${data.exit_code !== undefined ? `<br><strong><i class="fas fa-sign-out-alt"></i> Exit Code:</strong> ${data.exit_code}` : ''}
                            </div>
                            <div class="col-md-6">
                                <strong><i class="fas fa-clock"></i> Execution Time:</strong> ${execTime}<br>
                                <strong><i class="fas fa-fingerprint"></i> Trace ID:</strong> <code>${traceId}</code><br>
                                ${truncated ? `<strong><i class="fas fa-compress"></i> Output:</strong>${truncated}` : ''}
                            </div>
                        </div>
                    </div>
                    
                    ${output ? `
                        <div class="mb-3">
                            <h6 class="mb-2"><i class="fas fa-terminal"></i> Output:</h6>
                            <pre class="bg-dark text-light p-3 rounded" style="max-height: 400px; overflow-y: auto; white-space: pre-wrap; word-wrap: break-word;">${this.escapeHtml(output)}</pre>
                        </div>
                    ` : ''}
                    
                    ${data.error && data.error !== output ? `
                        <div class="mb-3">
                            <h6 class="mb-2 text-danger"><i class="fas fa-exclamation-triangle"></i> Error:</h6>
                            <pre class="bg-danger bg-opacity-10 text-danger p-3 rounded">${this.escapeHtml(data.error)}</pre>
                        </div>
                    ` : ''}
                    
                    ${traceId !== 'N/A' ? `
                        <div class="text-muted small">
                            <i class="fas fa-info-circle"></i> Full log available at: <code>/api/logs/${traceId}</code>
                        </div>
                    ` : ''}
                `;
                
                $('#executionOutput').html(html);
            } else {
                $('#executionOutput').html(`
                    <div class="alert alert-danger">
                        <i class="fas fa-exclamation-triangle"></i> <strong>Command execution failed!</strong>
                        <p class="mb-0 mt-2">${response.error || 'Unknown error occurred'}</p>
                    </div>
                `);
            }
        } catch (error) {
            console.error('Error executing command:', error);
            $('#executionOutput').html(`
                <div class="alert alert-danger">
                    <i class="fas fa-exclamation-triangle"></i> <strong>Failed to execute command!</strong>
                    <p class="mb-0 mt-2">${error.message || 'Unknown error occurred'}</p>
                </div>
            `);
        }
    },
    
    // Load recent logs for a command
    async loadRecentLogs(cmdId, autoExpand = false) {
        const container = $('#recentLogsContainer');

        if (!cmdId) {
            container.html(`
                <div class="text-center py-4 text-muted">
                    <i class="fas fa-file-alt fa-2x mb-2"></i>
                    <p>Save the command first to see execution logs.</p>
                </div>
            `);
            return;
        }

        container.html(`
            <div class="text-center py-3">
                <i class="fas fa-spinner fa-spin me-2"></i>Loading recent logs...
            </div>
        `);

        try {
            const response = await API.ops.getLogs();
            if (!response.success || !response.data) {
                container.html(`
                    <div class="alert alert-warning">
                        <i class="fas fa-exclamation-triangle me-2"></i>Failed to load logs.
                    </div>
                `);
                return;
            }

            // Filter logs for this command and take only the 5 most recent
            const cmdLogs = response.data
                .filter(log => log.cmd_id === cmdId)
                .slice(0, 5);

            if (cmdLogs.length === 0) {
                container.html(`
                    <div class="text-center py-4 text-muted">
                        <i class="fas fa-inbox fa-2x mb-2"></i>
                        <p>No execution logs found for this command.</p>
                    </div>
                `);
                return;
            }

            this.recentLogTexts = {};
            let html = '<div class="list-group">';
            cmdLogs.forEach((log, index) => {
                const isSuccess = log.status === 'Success';
                const statusBadge = isSuccess
                    ? '<span class="badge bg-success">Success</span>'
                    : (log.status === 'Failed' ? '<span class="badge bg-danger">Failed</span>' : '<span class="badge bg-secondary">Unknown</span>');
                const modifiedDate = new Date(log.modified);
                const dateStr = modifiedDate.toLocaleString();
                const sizeStr = this.formatFileSize(log.size);

                html += `
                    <div class="list-group-item list-group-item-action p-2" style="cursor: pointer;" onclick="Commands.toggleLogContent('${log.trace_id}', ${index})">
                        <div class="d-flex justify-content-between align-items-center">
                            <div>
                                ${statusBadge}
                                <span class="ms-2 small text-muted">${dateStr}</span>
                            </div>
                            <div class="small text-muted">
                                <span class="me-2">${sizeStr}</span>
                                <code class="me-2">${log.trace_id || 'N/A'}</code>
                                ${log.trace_id ? `<button type="button" class="btn btn-sm btn-outline-success p-1 me-2" title="Download log" aria-label="Download log ${log.trace_id}" onclick="event.stopPropagation(); Logs.downloadLog('${log.trace_id}')"><i class="fas fa-download"></i></button>` : ''}
                                <i class="fas fa-chevron-down" id="logChevron_${index}"></i>
                            </div>
                        </div>
                        <div class="d-flex justify-content-between align-items-center small text-muted mt-1">
                            <span><i class="fas fa-server me-1"></i>${log.host || 'Unknown'}</span>
                            <button type="button" class="copy-icon-btn recent-log-copy d-none" id="logCopy_${index}" title="Copy to Clipboard" aria-label="Copy to Clipboard" onclick="event.stopPropagation(); Commands.copyRecentLog(${index}, this)"><i class="far fa-copy"></i></button>
                        </div>
                        <div id="logContent_${index}" style="display: none;" class="mt-2">
                            <div class="text-center py-2">
                                <i class="fas fa-spinner fa-spin"></i> Loading...
                            </div>
                        </div>
                    </div>
                `;
            });
            html += '</div>';

            container.html(html);

            // Auto-expand the first log entry if requested
            if (autoExpand && cmdLogs.length > 0 && cmdLogs[0].trace_id) {
                await this.toggleLogContent(cmdLogs[0].trace_id, 0);
            }
        } catch (error) {
            console.error('Error loading recent logs:', error);
            container.html(`
                <div class="alert alert-danger">
                    <i class="fas fa-exclamation-triangle me-2"></i>Error loading logs: ${error.message || 'Unknown error'}
                </div>
            `);
        }
    },

    // Toggle log content visibility
    async toggleLogContent(traceId, index) {
        const contentDiv = $(`#logContent_${index}`);
        const chevron = $(`#logChevron_${index}`);

        const copyButton = $(`#logCopy_${index}`);
        this.recentLogTexts = this.recentLogTexts || {};

        if (contentDiv.is(':visible')) {
            contentDiv.slideUp(200);
            chevron.removeClass('fa-chevron-up').addClass('fa-chevron-down');
            copyButton.addClass('d-none');
            return;
        }

        chevron.removeClass('fa-chevron-down').addClass('fa-chevron-up');
        contentDiv.slideDown(200);

        // Only fetch if not already loaded
        if (contentDiv.attr('data-loaded') === 'true') {
            copyButton.toggleClass('d-none', typeof this.recentLogTexts[index] !== 'string');
            return;
        }

        try {
            const response = await API.ops.getLogContent(traceId);
            if (response.success && response.data && response.data.content) {
                this.recentLogTexts[index] = response.data.content;
                if (contentDiv.is(':visible')) copyButton.removeClass('d-none');
                contentDiv.html(`
                    <pre class="bg-dark text-light p-2 rounded small" style="max-height: 250px; overflow-y: auto; white-space: pre-wrap; word-wrap: break-word; margin-bottom: 0;">${this.escapeHtml(response.data.content)}</pre>
                `);
            } else {
                contentDiv.html(`<div class="text-muted small">Unable to load log content.</div>`);
            }
            contentDiv.attr('data-loaded', 'true');
        } catch (error) {
            contentDiv.html(`<div class="text-danger small">Error loading log: ${error.message}</div>`);
            contentDiv.attr('data-loaded', 'true');
        }
    },

    // Copy the full log shown in a Recent Logs row.
    async copyRecentLog(index, button) {
        const text = (this.recentLogTexts || {})[index];
        if (typeof text !== 'string') return;
        await Utils.copyToClipboardButton(text, button);
    },

    // Format file size for display
    formatFileSize(bytes) {
        if (!bytes || bytes === 0) return '0 B';
        const units = ['B', 'KB', 'MB', 'GB'];
        let i = 0;
        let size = bytes;
        while (size >= 1024 && i < units.length - 1) {
            size /= 1024;
            i++;
        }
        return size.toFixed(i === 0 ? 0 : 1) + ' ' + units[i];
    },

    // Cleanup
    cleanup() {
        this.closeRowActionsMenu();
    }
};

// Make component globally available
window.Commands = Commands;
