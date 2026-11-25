// Hosts Component
const Hosts = {
    currentHosts: {},
    currentConfig: null,
    editingHostOriginal: null, // Store original host data when editing
    availablePemFiles: [], // Store available PEM files
    listState: {
        search: '',
        connectionType: 'all',
        usage: 'all',
        sortKey: 'host_id',
        sortDirection: 'asc',
        page: 1,
        pageSize: 10
    },
    selectedHostIds: new Set(),
    activeActionsMenu: null,
    
    // Initialize hosts page
    async init() {
        try {
            // Clear cached data to force refresh
            this.currentHosts = {};
            this.currentConfig = null;
            this.listState = {
                search: '',
                connectionType: 'all',
                usage: 'all',
                sortKey: 'host_id',
                sortDirection: 'asc',
                page: 1,
                pageSize: 10
            };
            this.selectedHostIds.clear();
            this.closeRowActionsMenu();
            
            this.render();
            await this.loadHosts();
        } catch (error) {
            console.error('Hosts init error:', error);
            Utils.showError('Failed to load hosts');
        }
    },
    
    // Render hosts layout
    render() {
        const html = `
            <div class="hosts-container commands-page">
                <div class="commands-page-header">
                    <div>
                        <h1>Hosts</h1>
                        <p>Manage SSH and WinRM connections across your infrastructure.</p>
                    </div>
                    <button class="btn btn-primary commands-add-button" onclick="Hosts.showAddModal()">
                        <i class="fas fa-plus me-2"></i>New host
                    </button>
                </div>

                <div class="hosts-toolbar commands-toolbar" aria-label="Host filters">
                    <div class="commands-search-wrap">
                        <i class="fas fa-search" aria-hidden="true"></i>
                        <input type="search" class="form-control" id="hostsSearch"
                               placeholder="Search hosts..." aria-label="Search hosts"
                               oninput="Hosts.updateSearch(this.value)">
                    </div>
                    <div class="commands-filter-field">
                        <label for="hostsTypeFilter">Connection type</label>
                        <select class="form-select" id="hostsTypeFilter" onchange="Hosts.updateFilter('connectionType', this.value)">
                            <option value="all">All types</option>
                            <option value="ssh">SSH</option>
                            <option value="winrm">WinRM</option>
                        </select>
                    </div>
                    <div class="commands-filter-field">
                        <label for="hostsUsageFilter">Usage</label>
                        <select class="form-select" id="hostsUsageFilter" onchange="Hosts.updateFilter('usage', this.value)">
                            <option value="all">All hosts</option>
                            <option value="in-use">In use</option>
                            <option value="unused">Unused</option>
                        </select>
                    </div>
                    <button class="btn commands-clear-button" type="button" onclick="Hosts.clearFilters()">
                        <i class="fas fa-filter me-2"></i>Clear filters
                    </button>
                </div>

                <div id="hostsSummary" class="commands-summary" aria-label="Hosts summary"></div>

                <div id="hostsListContainer">
                    <div class="text-center py-5">
                        <i class="fas fa-spinner fa-spin fa-2x text-primary"></i>
                        <p class="mt-3 text-muted">Loading hosts...</p>
                    </div>
                </div>
            </div>
            
            <!-- Add/Edit Host Modal -->
            <div class="modal fade" id="hostModal" tabindex="-1">
                <div class="modal-dialog modal-lg">
                    <div class="modal-content">
                        <div class="modal-header">
                            <h5 class="modal-title" id="hostModalTitle">Add Host</h5>
                            <button type="button" class="btn-close" data-bs-dismiss="modal"></button>
                        </div>
                        <div class="modal-body">
                            <form id="hostForm">
                                <div class="row">
                                    <div class="col-md-6">
                                        <div class="mb-3">
                                            <label class="form-label">Host ID *</label>
                                            <input type="text" class="form-control" id="hostId" required>
                                            <div class="form-text">Unique identifier for this host</div>
                                        </div>
                                    </div>
                                    <div class="col-md-6">
                                        <div class="mb-3">
                                            <label class="form-label">Connection Type *</label>
                                            <select class="form-select" id="hostType" required onchange="Hosts.toggleConnectionFields()">
                                                <option value="">Select type...</option>
                                                <option value="ssh">SSH</option>
                                                <option value="winrm">WinRM</option>
                                            </select>
                                        </div>
                                    </div>
                                </div>
                                
                                <div class="row">
                                    <div class="col-md-8">
                                        <div class="mb-3">
                                            <label class="form-label">Hostname/IP *</label>
                                            <input type="text" class="form-control" id="hostAddress" required>
                                        </div>
                                    </div>
                                    <div class="col-md-4">
                                        <div class="mb-3">
                                            <label class="form-label">Port *</label>
                                            <input type="number" class="form-control" id="hostPort" required onchange="Hosts.onPortChange()">
                                        </div>
                                    </div>
                                </div>
                                
                                <div class="row">
                                    <div class="col-md-6">
                                        <div class="mb-3">
                                            <label class="form-label">Username *</label>
                                            <input type="text" class="form-control" id="hostUsername" required>
                                        </div>
                                    </div>
                                    <div class="col-md-6">
                                        <div class="mb-3">
                                            <label class="form-label">Password</label>
                                            <input type="password" class="form-control" id="hostPassword">
                                            <div class="form-text">Leave empty for SSH Key (PEM File) based Auth or to continue using existing Password</div>
                                        </div>
                                    </div>
                                </div>
                                
                                <div id="sshFields" style="display: none;">
                                    <div class="mb-3">
                                        <label class="form-label">SSH Key (PEM File)</label>
                                        <select class="form-select" id="hostKeyPath">
                                            <option value="">None - Use password authentication</option>
                                        </select>
                                        <div class="form-text">Select a PEM file from the uploaded keys</div>
                                    </div>
                                    <div class="mb-3">
                                        <label class="form-label">Connect via bastion host</label>
                                        <select class="form-select" id="hostBastionRef">
                                            <option value="">None - connect directly</option>
                                        </select>
                                        <div class="form-text">The bastion (jump host) is logged into first with its own credentials; this host is then reached through it. Only SSH hosts that do not use a bastion themselves can be chosen (single hop).</div>
                                    </div>
                                </div>
                                
                                <div id="winrmFields" style="display: none;">
                                    <div class="row">
                                        <div class="col-md-6">
                                            <div class="mb-3">
                                                <div class="form-check">
                                                    <input class="form-check-input" type="checkbox" id="hostUseSSL" onchange="Hosts.onSSLChange()">
                                                    <label class="form-check-label">Use SSL</label>
                                                </div>
                                                <div class="form-text">Port 5986 uses HTTPS, port 5985 uses HTTP</div>
                                            </div>
                                        </div>
                                        <div class="col-md-6" id="skipSSLContainer">
                                            <div class="mb-3">
                                                <div class="form-check">
                                                    <input class="form-check-input" type="checkbox" id="hostInsecure">
                                                    <label class="form-check-label">Skip SSL Verification</label>
                                                </div>
                                                <div class="form-text">Allow self-signed or invalid certificates</div>
                                            </div>
                                        </div>
                                    </div>
                                </div>
                            </form>
                        </div>
                        <div class="modal-footer">
                            <button type="button" class="btn btn-success" onclick="Hosts.testConnection()">
                                <i class="fas fa-plug"></i> Test Connection
                            </button>
                            <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Cancel</button>
                            <button type="button" class="btn btn-primary" onclick="Hosts.saveHost()">
                                <i class="fas fa-save"></i> Save Host
                            </button>
                        </div>
                    </div>
                </div>
            </div>
        `;
        
        $('#mainContent').html(html);
    },
    
    // Load hosts data
    async loadHosts() {
        try {
            const response = await API.config.get();
            
            if (response.success && response.data) {
                this.currentConfig = response.data;
                this.currentHosts = response.data.hosts || {};
                const currentHostIds = new Set(Object.keys(this.currentHosts));
                this.selectedHostIds.forEach(hostId => {
                    if (!currentHostIds.has(hostId)) this.selectedHostIds.delete(hostId);
                });
                this.renderHostsTable();
            } else {
                Utils.showError('Failed to load hosts configuration');
            }
        } catch (error) {
            console.error('Error loading hosts:', error);
            Utils.showError('Failed to load hosts');
        }
    },
    
    // Count commands for a specific host
    getHostCommandCount(hostId) {
        if (!this.currentConfig || !this.currentConfig.commands) return 0;
        return this.currentConfig.commands.filter(cmd => cmd.host_ref === hostId).length;
    },
    
    // Count Disk Monitor volumes that reference a specific host.
    getHostDiskVolumeCount(hostId) {
        const volumes = this.currentConfig && this.currentConfig.disk_monitor && this.currentConfig.disk_monitor.volumes;
        if (!Array.isArray(volumes)) return 0;
        return volumes.filter(volume => volume.host_ref === hostId).length;
    },

    // Count shared Agent Sessions (Terminals page launch presets) that reference a specific host.
    getHostAgentSessionCount(hostId) {
        const presets = this.currentConfig && this.currentConfig.terminals && this.currentConfig.terminals.agent_sessions;
        if (!Array.isArray(presets)) return 0;
        return presets.filter(preset => preset.host_ref === hostId).length;
    },

    // Count Remote Desktop (VNC) connections that tunnel through a specific host.
    getHostVNCCount(hostId) {
        const conns = this.currentConfig && this.currentConfig.vnc_connections;
        if (!Array.isArray(conns)) return 0;
        return conns.filter(conn => conn.tunnel_host_ref === hostId).length;
    },

    // Hosts that connect through this host as their bastion (jump host).
    getHostBastionDependents(hostId) {
        if (!this.currentHosts) return [];
        return Object.keys(this.currentHosts)
            .filter(id => id !== hostId && this.currentHosts[id] && this.currentHosts[id].bastion_host_ref === hostId)
            .sort();
    },

    // Fill the "Connect via bastion host" dropdown: SSH hosts other than the one being
    // edited that do not use a bastion themselves (single hop only).
    populateBastionSelect(excludeId) {
        const select = $('#hostBastionRef');
        const current = select.val();
        select.empty().append('<option value="">None - connect directly</option>');
        Object.keys(this.currentHosts || {}).sort().forEach(id => {
            const host = this.currentHosts[id];
            if (id === excludeId || !host) return;
            if (host.connection_type && host.connection_type !== 'ssh') return;
            if (host.bastion_host_ref) return;
            const option = document.createElement('option');
            option.value = id;
            option.textContent = `${id} (${host.username || ''}@${host.host || ''})`;
            select.append(option);
        });
        select.val(current && select.find(`option[value="${current.replace(/"/g, '\\"')}"]`).length ? current : '');
    },

    // Count RunBooks that use a specific host through their Commands.
    getHostRunBookCount(hostId) {
        if (!this.currentConfig || !this.currentConfig.runbooks) return 0;
        if (!this.currentConfig.commands) return 0;
        
        let count = 0;
        
        this.currentConfig.runbooks.forEach(runBook => {
            if (runBook.steps && runBook.steps.length > 0) {
                let usesHost = false;
                
                for (const step of runBook.steps) {
                    const command = this.currentConfig.commands.find(cmd => cmd.cmd_id === step.cmd_id);
                    
                    if (command && command.host_ref === hostId) {
                        usesHost = true;
                        break;
                    }
                }
                
                if (usesHost) {
                    count++;
                }
            }
        });
        
        return count;
    },
    
    updateSearch(value) {
        this.listState.search = String(value || '').trim().toLowerCase();
        this.listState.page = 1;
        this.renderHostsTable();
    },

    updateFilter(filterName, value) {
        if (!Object.prototype.hasOwnProperty.call(this.listState, filterName)) return;
        this.listState[filterName] = value;
        this.listState.page = 1;
        this.renderHostsTable();
    },

    clearFilters() {
        this.listState.search = '';
        this.listState.connectionType = 'all';
        this.listState.usage = 'all';
        this.listState.page = 1;
        $('#hostsSearch').val('');
        $('#hostsTypeFilter').val('all');
        $('#hostsUsageFilter').val('all');
        this.renderHostsTable();
    },

    getFilteredHosts() {
        const hosts = Object.entries(this.currentHosts).filter(([hostId, host]) => {
            const matchesSearch = !this.listState.search || [hostId, host.host, host.username]
                .some(value => String(value || '').toLowerCase().includes(this.listState.search));
            const matchesType = this.listState.connectionType === 'all'
                || host.connection_type === this.listState.connectionType;
            const isInUse = this.getHostCommandCount(hostId) > 0 || this.getHostRunBookCount(hostId) > 0 || this.getHostDiskVolumeCount(hostId) > 0 || this.getHostAgentSessionCount(hostId) > 0 || this.getHostVNCCount(hostId) > 0;
            const matchesUsage = this.listState.usage === 'all'
                || (this.listState.usage === 'in-use' ? isInUse : !isInUse);
            return matchesSearch && matchesType && matchesUsage;
        });

        const direction = this.listState.sortDirection === 'asc' ? 1 : -1;
        return hosts.sort(([aId, aHost], [bId, bHost]) => {
            let aValue;
            let bValue;
            switch (this.listState.sortKey) {
                case 'host':
                case 'connection_type':
                case 'username':
                case 'port':
                    aValue = aHost[this.listState.sortKey] || '';
                    bValue = bHost[this.listState.sortKey] || '';
                    break;
                case 'commands':
                    aValue = this.getHostCommandCount(aId);
                    bValue = this.getHostCommandCount(bId);
                    break;
                case 'runbooks':
                    aValue = this.getHostRunBookCount(aId);
                    bValue = this.getHostRunBookCount(bId);
                    break;
                default:
                    aValue = aId;
                    bValue = bId;
            }
            if (typeof aValue === 'number' && typeof bValue === 'number') {
                return (aValue - bValue) * direction;
            }
            return String(aValue).localeCompare(String(bValue)) * direction;
        });
    },

    sortHosts(sortKey) {
        if (this.listState.sortKey === sortKey) {
            this.listState.sortDirection = this.listState.sortDirection === 'asc' ? 'desc' : 'asc';
        } else {
            this.listState.sortKey = sortKey;
            this.listState.sortDirection = 'asc';
        }
        this.listState.page = 1;
        this.renderHostsTable();
    },

    getSortIcon(sortKey) {
        if (this.listState.sortKey !== sortKey) return '<i class="fas fa-sort"></i>';
        return this.listState.sortDirection === 'asc'
            ? '<i class="fas fa-sort-up"></i>'
            : '<i class="fas fa-sort-down"></i>';
    },

    changePage(page) {
        const pageCount = Math.max(1, Math.ceil(this.getFilteredHosts().length / this.listState.pageSize));
        this.listState.page = Math.min(Math.max(1, page), pageCount);
        this.renderHostsTable();
    },

    changePageSize(value) {
        this.listState.pageSize = Number(value) || 10;
        this.listState.page = 1;
        this.renderHostsTable();
    },

    toggleHostSelection(hostId, checked) {
        if (checked) this.selectedHostIds.add(hostId);
        else this.selectedHostIds.delete(hostId);
        this.updateSelectionState();
    },

    togglePageSelection(checked) {
        $('#hostsTableBody input[data-host-select]').each((_, input) => {
            input.checked = checked;
            const hostId = input.getAttribute('data-host-select');
            if (checked) this.selectedHostIds.add(hostId);
            else this.selectedHostIds.delete(hostId);
        });
        this.updateSelectionState();
    },

    updateSelectionState() {
        const checkboxes = $('#hostsTableBody input[data-host-select]');
        const checkedCount = checkboxes.filter(':checked').length;
        const selectAll = document.getElementById('hostsSelectAll');
        if (selectAll) {
            selectAll.checked = checkboxes.length > 0 && checkedCount === checkboxes.length;
            selectAll.indeterminate = checkedCount > 0 && checkedCount < checkboxes.length;
        }
        $('#hostsSelectedCount').text(this.selectedHostIds.size ? `${this.selectedHostIds.size} selected` : '');
    },

    renderHostsSummary() {
        const hostEntries = Object.entries(this.currentHosts);
        const sshCount = hostEntries.filter(([, host]) => host.connection_type === 'ssh').length;
        const winrmCount = hostEntries.filter(([, host]) => host.connection_type === 'winrm').length;
        const inUseCount = hostEntries.filter(([hostId]) =>
            this.getHostCommandCount(hostId) > 0 || this.getHostRunBookCount(hostId) > 0 || this.getHostDiskVolumeCount(hostId) > 0 || this.getHostAgentSessionCount(hostId) > 0 || this.getHostVNCCount(hostId) > 0
        ).length;
        const commandCount = hostEntries.reduce((total, [hostId]) => total + this.getHostCommandCount(hostId), 0);
        const metrics = [
            ['fa-server', 'Total hosts', hostEntries.length, 'primary'],
            ['fa-terminal', 'SSH', sshCount, 'primary'],
            ['fa-desktop', 'WinRM', winrmCount, 'primary'],
            ['fa-link', 'In use', inUseCount, 'primary'],
            ['fa-unlink', 'Unused', hostEntries.length - inUseCount, 'primary'],
            ['fa-code', 'Commands', commandCount, 'success']
        ];
        $('#hostsSummary').html(metrics.map(([icon, label, value, tone]) => `
            <div class="commands-summary-item">
                <span class="commands-summary-icon commands-summary-icon-${tone}"><i class="fas ${icon}"></i></span>
                <span><small>${label}</small><strong>${value}</strong></span>
            </div>
        `).join(''));
    },

    getRowActions(hostId) {
        const safeHostId = Utils.escapeHtml(String(hostId));
        return `
            <div class="commands-row-actions">
                <button class="btn commands-more-button" type="button" data-host-id="${safeHostId}"
                        aria-expanded="false" aria-haspopup="menu" aria-label="Actions for ${safeHostId}"
                        onclick="Hosts.toggleRowActions(this, this.dataset.hostId, event)">
                    <i class="fas fa-ellipsis-v"></i>
                </button>
            </div>`;
    },

    toggleRowActions(trigger, hostId, event) {
        event.preventDefault();
        event.stopPropagation();

        if (this.activeActionsMenu && this.activeActionsMenu.trigger === trigger) {
            this.closeRowActionsMenu();
            return;
        }

        this.closeRowActionsMenu();

        const commandCount = this.getHostCommandCount(hostId);
        const runBookCount = this.getHostRunBookCount(hostId);
        const diskVolumeCount = this.getHostDiskVolumeCount(hostId);
        const bastionCount = this.getHostBastionDependents(hostId).length;
        const agentSessionCount = this.getHostAgentSessionCount(hostId);
        const vncCount = this.getHostVNCCount(hostId);
        const isInUse = commandCount > 0 || runBookCount > 0 || diskVolumeCount > 0 || bastionCount > 0 || agentSessionCount > 0 || vncCount > 0;
        const menu = document.createElement('div');
        menu.className = 'dropdown-menu show commands-actions-menu hosts-actions-menu-portal';
        menu.setAttribute('role', 'menu');
        menu.setAttribute('aria-label', `Actions for ${hostId}`);

        const editButton = document.createElement('button');
        editButton.className = 'dropdown-item';
        editButton.type = 'button';
        editButton.innerHTML = '<i class="fas fa-edit"></i><span>Edit</span>';
        editButton.addEventListener('click', () => {
            this.closeRowActionsMenu();
            this.editHost(hostId);
        });
        menu.appendChild(editButton);

        const divider = document.createElement('div');
        divider.className = 'dropdown-divider';
        menu.appendChild(divider);

        const deleteButton = document.createElement('button');
        deleteButton.type = 'button';
        deleteButton.innerHTML = '<i class="fas fa-trash"></i><span>Delete</span>';
        if (isInUse) {
            deleteButton.className = 'dropdown-item text-muted';
            deleteButton.disabled = true;
            deleteButton.title = `Used by ${commandCount} command(s), ${runBookCount} RunBook(s), ${diskVolumeCount} disk monitor volume(s), ${agentSessionCount} Agent Session(s), ${vncCount} remote desktop(s) and ${bastionCount} host(s) as bastion`;
            const status = document.createElement('span');
            status.className = 'ms-auto small';
            status.textContent = 'In use';
            deleteButton.appendChild(status);
        } else {
            deleteButton.className = 'dropdown-item text-danger';
            deleteButton.addEventListener('click', () => {
                this.closeRowActionsMenu();
                this.deleteHost(hostId);
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

    // Render the searchable, filterable host table
    renderHostsTable() {
        this.closeRowActionsMenu();
        const container = $('#hostsListContainer');
        this.renderHostsSummary();

        if (Object.keys(this.currentHosts).length === 0) {
            container.html(`
                <div class="commands-empty-state">
                    <i class="fas fa-server fa-3x text-muted mb-3"></i>
                    <p class="text-muted">No hosts configured yet.</p>
                    <button class="btn btn-primary" onclick="Hosts.showAddModal()">
                        <i class="fas fa-plus me-1"></i> Add Your First Host
                    </button>
                </div>
            `);
            return;
        }

        const filteredHosts = this.getFilteredHosts();
        const pageCount = Math.max(1, Math.ceil(filteredHosts.length / this.listState.pageSize));
        if (this.listState.page > pageCount) this.listState.page = pageCount;
        const startIndex = (this.listState.page - 1) * this.listState.pageSize;
        const pageHosts = filteredHosts.slice(startIndex, startIndex + this.listState.pageSize);
        const pageButtons = Array.from({ length: pageCount }, (_, index) => index + 1)
            .filter(page => page === 1 || page === pageCount || Math.abs(page - this.listState.page) <= 1);
        let lastPage = 0;
        const pagination = pageButtons.map(page => {
            const gap = lastPage && page - lastPage > 1 ? '<span class="commands-page-gap">&hellip;</span>' : '';
            lastPage = page;
            return `${gap}<button class="commands-page-number ${page === this.listState.page ? 'active' : ''}" onclick="Hosts.changePage(${page})">${page}</button>`;
        }).join('');

        const rows = pageHosts.map(([hostId, host]) => {
            const commandCount = this.getHostCommandCount(hostId);
            const runBookCount = this.getHostRunBookCount(hostId);
            const safeId = Utils.escapeHtml(String(hostId));
            const safeAddress = Utils.escapeHtml(String(host.host || ''));
            const safeBastion = host.bastion_host_ref ? Utils.escapeHtml(String(host.bastion_host_ref)) : '';
            const viaBastion = safeBastion ? `<br><small class="text-muted" title="Connects through bastion host ${safeBastion}"><i class="fas fa-route me-1"></i>via ${safeBastion}</small>` : '';
            const safeUsername = Utils.escapeHtml(String(host.username || ''));
            const hostType = host.connection_type === 'winrm' ? 'winrm' : 'ssh';
            const checked = this.selectedHostIds.has(hostId) ? 'checked' : '';
            return `
                <tr>
                    <td class="commands-checkbox-cell"><input class="form-check-input" type="checkbox" ${checked}
                        data-host-select="${safeId}" onchange="Hosts.toggleHostSelection(this.dataset.hostSelect, this.checked)"
                        aria-label="Select ${safeId}"></td>
                    <td><button type="button" class="commands-command-name d-block border-0 bg-transparent p-0 text-start" data-host-id="${safeId}" onclick="Hosts.editHost(this.dataset.hostId)" aria-label="Edit ${safeId}" title="Edit ${safeId}">${safeId}</button></td>
                    <td><span class="hosts-address-text" title="${safeAddress}">${safeAddress}</span>${viaBastion}</td>
                    <td><span class="commands-host-badge is-${hostType}"><i class="fas fa-server"></i>${hostType.toUpperCase()}</span></td>
                    <td><span class="hosts-username-text" title="${safeUsername}">${safeUsername || '&mdash;'}</span></td>
                    <td class="commands-number-cell">${Number(host.port) || '&mdash;'}</td>
                    <td class="commands-number-cell">${commandCount}</td>
                    <td class="commands-number-cell">${runBookCount}</td>
                    <td>${this.getRowActions(hostId)}</td>
                </tr>`;
        }).join('');

        container.html(`
            <div class="commands-table-card">
                <div class="commands-table-scroll">
                    <table class="commands-table hosts-table">
                        <thead><tr>
                            <th class="commands-checkbox-cell"><input class="form-check-input" id="hostsSelectAll" type="checkbox"
                                onchange="Hosts.togglePageSelection(this.checked)" aria-label="Select all visible hosts"></th>
                            <th><button onclick="Hosts.sortHosts('host_id')">Host ID ${this.getSortIcon('host_id')}</button></th>
                            <th><button onclick="Hosts.sortHosts('host')">Address ${this.getSortIcon('host')}</button></th>
                            <th><button onclick="Hosts.sortHosts('connection_type')">Type ${this.getSortIcon('connection_type')}</button></th>
                            <th><button onclick="Hosts.sortHosts('username')">Username ${this.getSortIcon('username')}</button></th>
                            <th><button onclick="Hosts.sortHosts('port')">Port ${this.getSortIcon('port')}</button></th>
                            <th><button onclick="Hosts.sortHosts('commands')">Commands ${this.getSortIcon('commands')}</button></th>
                            <th><button onclick="Hosts.sortHosts('runbooks')">RunBooks ${this.getSortIcon('runbooks')}</button></th>
                            <th>Actions</th>
                        </tr></thead>
                        <tbody id="hostsTableBody">${rows || `
                            <tr><td colspan="9"><div class="commands-no-results"><i class="fas fa-search"></i><strong>No hosts found</strong><span>Try changing or clearing the filters.</span></div></td></tr>
                        `}</tbody>
                    </table>
                </div>
                <div class="commands-table-footer">
                    <div>
                        <span>${filteredHosts.length ? `Showing ${startIndex + 1} to ${Math.min(startIndex + this.listState.pageSize, filteredHosts.length)} of ${filteredHosts.length}` : 'Showing 0 hosts'}</span>
                        <span id="hostsSelectedCount" class="commands-selected-count"></span>
                    </div>
                    <div class="commands-pagination">
                        <button class="commands-page-arrow" onclick="Hosts.changePage(${this.listState.page - 1})" ${this.listState.page === 1 ? 'disabled' : ''} aria-label="Previous page"><i class="fas fa-chevron-left"></i></button>
                        ${pagination}
                        <button class="commands-page-arrow" onclick="Hosts.changePage(${this.listState.page + 1})" ${this.listState.page === pageCount ? 'disabled' : ''} aria-label="Next page"><i class="fas fa-chevron-right"></i></button>
                        <select class="form-select commands-page-size" onchange="Hosts.changePageSize(this.value)" aria-label="Rows per page">
                            ${[10, 25, 50].map(size => `<option value="${size}" ${size === this.listState.pageSize ? 'selected' : ''}>${size} / page</option>`).join('')}
                        </select>
                    </div>
                </div>
            </div>
        `);
        this.updateSelectionState();
    },
    
    // Load available PEM files
    async loadPemFiles() {
        try {
            const response = await API.pemFiles.list();
            if (response.success && response.data) {
                this.availablePemFiles = response.data;
                this.updatePemFileDropdown();
            }
        } catch (error) {
            console.error('Error loading PEM files:', error);
            // Continue without PEM files, just show empty dropdown
            this.availablePemFiles = [];
            this.updatePemFileDropdown();
        }
    },
    
    // Update PEM file dropdown
    updatePemFileDropdown() {
        const select = $('#hostKeyPath');
        select.empty();
        select.append('<option value="">None - Use password authentication</option>');
        
        if (this.availablePemFiles.length > 0) {
            this.availablePemFiles.forEach(file => {
                // Store the full path that will be used
                const pemPath = `./pem_files/${file.name}`;
                select.append(`<option value="${Utils.escapeHtml(pemPath)}">${Utils.escapeHtml(String(file.name))}</option>`);
            });
        }
    },
    
    // Show add modal
    async showAddModal() {
        // Clear any stored original data
        this.editingHostOriginal = null;
        
        // Load available PEM files
        await this.loadPemFiles();
        
        $('#hostModalTitle').text('Add New Host');
        $('#hostForm')[0].reset();
        $('#hostId').prop('readonly', false);
        $('#hostId').attr('data-mode', 'create'); // Mark as create mode
        $('#hostPassword').attr('placeholder', 'Leave empty for SSH Key (PEM File) based Auth or to continue using existing Password');
        // Reset the help text for new host
        $('#hostId').parent().find('.form-text').text('Unique identifier for this host');
        this.populateBastionSelect(null);
        $('#hostBastionRef').val('');
        this.toggleConnectionFields();
        $('#hostModal').modal('show');
    },
    
    // Edit host
    async editHost(hostId) {
        const host = this.currentHosts[hostId];
        if (!host) return;
        
        // Store the original host data including password
        this.editingHostOriginal = { ...host };
        
        // Load available PEM files
        await this.loadPemFiles();
        
        // Check if host is in use by Commands or RunBooks.
        const commandCount = this.getHostCommandCount(hostId);
        const runBookCount = this.getHostRunBookCount(hostId);
        const diskVolumeCount = this.getHostDiskVolumeCount(hostId);
        const bastionCount = this.getHostBastionDependents(hostId).length;
        const agentSessionCount = this.getHostAgentSessionCount(hostId);
        const vncCount = this.getHostVNCCount(hostId);
        const isHostInUse = commandCount > 0 || runBookCount > 0 || diskVolumeCount > 0 || bastionCount > 0 || agentSessionCount > 0 || vncCount > 0;
        
        $('#hostModalTitle').text('Edit Host');
        $('#hostId').val(hostId);
        $('#hostId').attr('data-mode', 'edit'); // Mark as edit mode
        $('#hostId').attr('data-original-id', hostId); // Store original ID
        
        // Only allow editing Host ID when it has no references.
        if (isHostInUse) {
            $('#hostId').prop('readonly', true);
            // Add a tooltip or help text to explain why it's readonly
            $('#hostId').attr('title', `Host ID cannot be changed - used by ${commandCount} command(s), ${runBookCount} RunBook(s), ${diskVolumeCount} disk monitor volume(s), ${agentSessionCount} Agent Session(s), ${vncCount} remote desktop(s) and ${bastionCount} host(s) as bastion`);
            // Also add visual feedback
            $('#hostId').parent().find('.form-text').html(
                `<span class="text-warning">Host ID is locked - referenced by ${commandCount} command(s), ${runBookCount} RunBook(s), ${diskVolumeCount} disk monitor volume(s), ${agentSessionCount} Agent Session(s), ${vncCount} remote desktop(s) and ${bastionCount} host(s) as bastion</span>`
            );
        } else {
            $('#hostId').prop('readonly', false);
            $('#hostId').attr('title', '');
            $('#hostId').parent().find('.form-text').text('Unique identifier for this host (can be changed since not in use)');
        }
        $('#hostType').val(host.connection_type);
        $('#hostAddress').val(host.host);
        $('#hostPort').val(host.port);
        $('#hostUsername').val(host.username);
        
        // Show password placeholder if password exists
        if ( host.password || !host.pem_file ) {
            $('#hostPassword').val('').attr('placeholder', 'Leave empty to use Existing password or to use SSH Key (PEM File) based Auth');
        } else {
            $('#hostPassword').val('').attr('placeholder', 'Leave empty for SSH Key (PEM File) based Auth or to continue using existing Password');
        }
        
        // Set the PEM file if it exists
        if (host.pem_file) {
            $('#hostKeyPath').val(host.pem_file);
        } else if (host.key_path) {
            // For backward compatibility, if key_path exists but not pem_file
            $('#hostKeyPath').val(host.key_path);
        } else {
            $('#hostKeyPath').val('');
        }
        
        $('#hostUseSSL').prop('checked', host.winrm_https || false);
        $('#hostInsecure').prop('checked', host.winrm_insecure || false);

        this.populateBastionSelect(hostId);
        $('#hostBastionRef').val(host.bastion_host_ref || '');
        
        this.toggleConnectionFields();
        $('#hostModal').modal('show');
    },
    
    // Toggle connection type fields
    toggleConnectionFields() {
        const type = $('#hostType').val();
        const isSSH = type === 'ssh';
        const isWinRM = type === 'winrm';

        $('#sshFields').toggle(isSSH);
        $('#winrmFields').toggle(isWinRM);

        // Set default ports
        if (isSSH && !$('#hostPort').val()) {
            $('#hostPort').val(22);
        } else if (isWinRM && !$('#hostPort').val()) {
            $('#hostPort').val(5986);
            $('#hostUseSSL').prop('checked', true);
        }

        // Update Skip SSL Verification visibility
        if (isWinRM) {
            this.updateSkipSSLVisibility();
        }
    },

    // Handle port change - sync SSL checkbox
    onPortChange() {
        const type = $('#hostType').val();
        if (type !== 'winrm') return;

        const port = parseInt($('#hostPort').val());
        if (port === 5986) {
            $('#hostUseSSL').prop('checked', true);
        } else if (port === 5985) {
            $('#hostUseSSL').prop('checked', false);
        }
        this.updateSkipSSLVisibility();
    },

    // Handle SSL checkbox change - sync port
    onSSLChange() {
        const useSSL = $('#hostUseSSL').is(':checked');
        if (useSSL) {
            $('#hostPort').val(5986);
        } else {
            $('#hostPort').val(5985);
            $('#hostInsecure').prop('checked', false);
        }
        this.updateSkipSSLVisibility();
    },

    // Show/hide Skip SSL Verification based on Use SSL state
    updateSkipSSLVisibility() {
        const useSSL = $('#hostUseSSL').is(':checked');
        $('#skipSSLContainer').toggle(useSSL);
    },
    
    // Save host
    async saveHost() {
        try {
            const form = $('#hostForm')[0];
            if (!form.checkValidity()) {
                form.reportValidity();
                return;
            }
            
            // Check if this is a new host or an update based on mode
            const mode = $('#hostId').attr('data-mode');
            const isNewHost = mode === 'create';
            const originalHostId = $('#hostId').attr('data-original-id');
            
            const hostData = {
                connection_type: $('#hostType').val(),
                host: $('#hostAddress').val().trim(),
                port: parseInt($('#hostPort').val()),
                username: $('#hostUsername').val().trim()
            };
            
            const password = $('#hostPassword').val();
            if (password) {
                // New password entered
                hostData.password = password;
            }
            // Note: If no password entered when editing, don't send password field at all
            // Backend will preserve the existing password
            
            if (hostData.connection_type === 'ssh') {
                const keyPath = $('#hostKeyPath').val();
                if (keyPath && keyPath !== '') {
                    // Use pem_file field for PEM files from ./pem_files folder
                    hostData.pem_file = keyPath;
                }
                // Always sent: "" switches the host back to a direct connection
                hostData.bastion_host_ref = $('#hostBastionRef').val() || '';
            } else if (hostData.connection_type === 'winrm') {
                hostData.winrm_https = $('#hostUseSSL').is(':checked');
                hostData.winrm_insecure = $('#hostInsecure').is(':checked');
            }
            
            Utils.showLoading();
            
            const hostId = $('#hostId').val().trim();
            let response;
            
            if (isNewHost) {
                // Create new host - add hostId as 'name' field and use POST
                hostData.name = hostId;  // Include the host ID as 'name' in the data
                response = await API.config.createHost(hostData);
            } else {
                // Check if host ID has changed (only possible when not in use)
                const hostIdChanged = originalHostId && originalHostId !== hostId;
                
                if (hostIdChanged) {
                    // Host ID changed - need to delete old and create new
                    // First create the new host
                    hostData.name = hostId;
                    response = await API.config.createHost(hostData);
                    
                    if (response.success) {
                        // Then delete the old host
                        await API.config.deleteHost(originalHostId);
                    }
                } else {
                    // Regular update - host ID unchanged
                    response = await API.config.updateHost(hostId, hostData);
                }
            }
            
            if (response.success) {
                Utils.showSuccess(isNewHost ? 'Host created successfully' : 'Host updated successfully');
                $('#hostModal').modal('hide');
                await this.loadHosts();
            } else {
                Utils.showError(response.error || 'Failed to save host');
            }
        } catch (error) {
            console.error('Error saving host:', error);
            Utils.showError('Failed to save host');
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Test connection
    async testConnection(hostId = null) {
        try {
            let testHostId = hostId;
            
            if (!testHostId) {
                // Test from form
                testHostId = $('#hostId').val().trim();
                if (!testHostId) {
                    Utils.showError('Please enter a host ID');
                    return;
                }
            }
            
            Utils.showLoading();
            
            const response = await API.ops.testConnection(testHostId);
            
            if (response.success) {
                // Check if it's an execution response with data
                if (response.data && response.data.output) {
                    Utils.showSuccess(`Connection test successful!\nOutput: ${response.data.output}`);
                } else {
                    Utils.showSuccess('Connection test successful!');
                }
            } else {
                Utils.showErrorDetails(
                    'SSH connection failed',
                    response.error || response.message || 'Connection test failed'
                );
            }
        } catch (error) {
            console.error('Error testing connection:', error);
            Utils.showErrorDetails(
                'SSH connection failed',
                error.message || 'Failed to test connection'
            );
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Delete host
    async deleteHost(hostId) {
        // Check if host is in use
        const commandCount = this.getHostCommandCount(hostId);
        const runBookCount = this.getHostRunBookCount(hostId);
        const diskVolumeCount = this.getHostDiskVolumeCount(hostId);
        const bastionCount = this.getHostBastionDependents(hostId).length;
        const agentSessionCount = this.getHostAgentSessionCount(hostId);
        const vncCount = this.getHostVNCCount(hostId);
        const isHostInUse = commandCount > 0 || runBookCount > 0 || diskVolumeCount > 0 || bastionCount > 0 || agentSessionCount > 0 || vncCount > 0;
        
        if (isHostInUse) {
            // Safety check - should not reach here due to disabled button, but just in case
            Utils.showError(`Cannot delete host "${hostId}" - it is used by ${commandCount} command(s), ${runBookCount} RunBook(s), ${diskVolumeCount} disk monitor volume(s), ${agentSessionCount} Agent Session(s), ${vncCount} remote desktop(s) and ${bastionCount} host(s) as bastion. Remove all references first.`);
            return;
        }
        
        if (!confirm(`Are you sure you want to delete host "${hostId}"?`)) {
            return;
        }
        
        try {
            Utils.showLoading();
            
            const response = await API.config.deleteHost(hostId);
            
            if (response.success) {
                Utils.showSuccess('Host deleted successfully');
                await this.loadHosts();
            } else {
                Utils.showError(response.error || 'Failed to delete host');
            }
        } catch (error) {
            console.error('Error deleting host:', error);
            Utils.showError('Failed to delete host');
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Cleanup
    cleanup() {
        this.closeRowActionsMenu();
    }
};
// Make component globally available
window.Hosts = Hosts;
