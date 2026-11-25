// Logs Component
const Logs = {
	allowDestructiveActions: true,
    currentLogs: [],
    // Job ID to pre-select in the Job filter the next time the page loads (set by Logs.openForJob).
    pendingJobFilter: null,
    currentFilter: {
        search: '',
        host: '',
        command: '',
        type: '',
        status: '',
        dateFrom: '',
        dateTo: ''
    },
    
    // Initialize logs page
    async init() {
        try {
            // Clear cached data to force refresh
            this.currentLogs = [];
            
            this.render();
            Utils.searchableSelect('#hostFilter');
            Utils.searchableSelect('#commandFilter');
            await this.loadLogs();
            this.setupAutoRefresh();
        } catch (error) {
            console.error('Logs init error:', error);
            Utils.showError('Failed to load logs');
        }
    },
    
    // Render logs layout
    render() {
        const html = `
            <div class="page-header">
                <h2><i class="fas fa-history"></i> Execution Logs</h2>
                <div class="btn-group">
                    <button class="btn btn-outline-primary" onclick="Logs.refreshLogs()">
                        <i class="fas fa-sync-alt"></i> Refresh
                    </button>
                    <button class="btn btn-outline-success" onclick="Logs.exportLogs()">
                        <i class="fas fa-download"></i> Export
                    </button>
					${this.allowDestructiveActions ? `<button class="btn btn-outline-danger" onclick="Logs.clearOldLogs()">
                        <i class="fas fa-trash"></i> Clear Old
					</button>` : ''}
                </div>
            </div>
            
            <!-- Filters -->
            <div class="card mb-3">
                <div class="card-header">
                    <i class="fas fa-filter"></i> Filters
                    <button class="btn btn-sm btn-outline-secondary float-end" onclick="Logs.clearFilters()">
                        Clear Filters
                    </button>
                </div>
                <div class="card-body">
                    <div class="row g-3">
                        <div class="col-md-2">
                            <label class="form-label">Search</label>
                            <input type="text" class="form-control" id="searchFilter" 
                                   placeholder="Search logs..." onkeyup="Logs.applyFilters()">
                        </div>
                        <div class="col-md-2">
                            <label class="form-label">Host</label>
                            <select class="form-select" id="hostFilter" onchange="Logs.applyFilters()">
                                <option value="">All Hosts</option>
                            </select>
                        </div>
                        <div class="col-md-2">
                            <label class="form-label">Job</label>
                            <select class="form-select" id="commandFilter" onchange="Logs.applyFilters()">
                                <option value="">All Jobs</option>
                            </select>
                        </div>
                        <div class="col-md-2">
                            <label class="form-label">Type</label>
                            <select class="form-select" id="typeFilter" onchange="Logs.applyFilters()">
                                <option value="">All Types</option>
                                <option value="command">Command</option>
                                <option value="runbook">RunBook step</option>
                                <option value="http_request">HTTP request</option>
                                <option value="disk_check">Disk check</option>
                            </select>
                        </div>
                        <div class="col-md-2">
                            <label class="form-label">Status</label>
                            <select class="form-select" id="statusFilter" onchange="Logs.applyFilters()">
                                <option value="">All Status</option>
                                <option value="success">Success</option>
                                <option value="failed">Failed</option>
                                <option value="running">Running</option>
                                <option value="unknown">Unknown</option>
                            </select>
                        </div>
                        <div class="col-md-12 col-xl-2">
                            <label class="form-label">Date Range</label>
                            <div class="input-group">
                                <input type="date" class="form-control" id="dateFromFilter" 
                                       placeholder="From" onchange="Logs.applyFilters()" style="max-width: 170px;">
                                <span class="input-group-text">to</span>
                                <input type="date" class="form-control" id="dateToFilter" 
                                       placeholder="To" onchange="Logs.applyFilters()" style="max-width: 170px;">
                            </div>
                        </div>
                    </div>
                </div>
            </div>
            
            <!-- Logs Table -->
            <div class="card">
                <div class="card-header">
                    <i class="fas fa-list"></i> Execution History
                    <small class="text-muted float-end" id="logsCount">0 logs</small>
                </div>
                <div class="card-body">
                    <div class="table-responsive">
                        <table id="logsTable" class="table table-striped table-hover">
                            <thead>
                                <tr>
                                    <th>Timestamp</th>
                                    <th>Job</th>
                                    <th class="d-none d-md-table-cell">Host</th>
                                    <th>Status</th>
                                    <th class="d-none d-lg-table-cell">Size</th>
                                    <th class="d-none d-lg-table-cell">Trace ID</th>
                                    <th style="min-width: 120px;">Actions</th>
                                </tr>
                            </thead>
                            <tbody id="logsTableBody">
                                <tr>
                                    <td colspan="7" class="text-center">
                                        <i class="fas fa-spinner fa-spin"></i> Loading logs...
                                    </td>
                                </tr>
                            </tbody>
                        </table>
                    </div>
                    
                    <!-- Pagination -->
                    <nav id="logsPagination" class="mt-3" style="display: none;">
                        <ul class="pagination justify-content-center">
                            <!-- Pagination will be generated here -->
                        </ul>
                    </nav>
                </div>
            </div>
            
            <!-- Log Statistics Modal -->
            <div class="modal fade" id="statsModal" tabindex="-1">
                <div class="modal-dialog modal-lg">
                    <div class="modal-content">
                        <div class="modal-header">
                            <h5 class="modal-title">Log Statistics</h5>
                            <button type="button" class="btn-close" data-bs-dismiss="modal"></button>
                        </div>
                        <div class="modal-body">
                            <div id="logStats">
                                <div class="text-center">
                                    <i class="fas fa-spinner fa-spin"></i> Calculating statistics...
                                </div>
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
        this.ensureLogModal();
    },

    // The Log Details modal is shared by every page (Logs, Overview, RunBook runs), so it lives on <body>.
    ensureLogModal() {
        if (document.getElementById('logModal')) return;
        $(document.body).append(`
            <!-- Log Details Modal -->
            <div class="modal fade" id="logModal" tabindex="-1">
                <div class="modal-dialog modal-xl">
                    <div class="modal-content">
                        <div class="modal-header">
                            <h5 class="modal-title" id="logModalTitle">Log Details</h5>
                            <button type="button" class="btn-close" data-bs-dismiss="modal"></button>
                        </div>
                        <div class="modal-body">
                            <div id="logDetails">
                                <div class="text-center">
                                    <i class="fas fa-spinner fa-spin"></i> Loading log details...
                                </div>
                            </div>
                        </div>
                        <div class="modal-footer">
                            <button type="button" class="btn btn-outline-secondary" id="copyLogBtn" disabled>
                                <i class="far fa-copy"></i> Copy to Clipboard
                            </button>
                            <button type="button" class="btn btn-primary" id="downloadLogBtn">
                                <i class="fas fa-download"></i> Download
                            </button>
                            <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Close</button>
                        </div>
                    </div>
                </div>
            </div>`);
        // When this modal was opened on top of another one, keep the page locked for the one still open.
        document.getElementById('logModal').addEventListener('hidden.bs.modal', () => {
            if (document.querySelector('.modal.show')) document.body.classList.add('modal-open');
        });
    },
    
    // Load logs
    async loadLogs() {
        try {
            const response = await API.ops.getLogs();
            
            if (response.success && response.data) {
                this.currentLogs = response.data;
                this.populateFilters();
                this.applyFilters();
            } else {
                Utils.showError('Failed to load logs');
            }
        } catch (error) {
            console.error('Error loading logs:', error);
            Utils.showError('Failed to load logs');
        }
    },
    
    // Open the Logs page with the Job filter set to one command / HTTP request ID.
    // Used by the row menus on other pages ("Logs" option).
    openForJob(jobId) {
        this.pendingJobFilter = jobId ? String(jobId) : null;
        return Router.navigate('logs', true, true);
    },

    // Populate filter dropdowns
    populateFilters() {
        const hosts = new Set();
        const commands = new Set();
        
        this.currentLogs.forEach(log => {
            if (log.host) hosts.add(log.host);
            if (log.cmd_id) commands.add(log.cmd_id);
        });
        
        // Keep the current selections: this runs again on every auto-refresh.
        const hostFilter = $('#hostFilter');
        const commandFilter = $('#commandFilter');
        const selectedHost = hostFilter.val() || '';
        let selectedCommand = commandFilter.val() || '';
        if (this.pendingJobFilter) {
            selectedCommand = this.pendingJobFilter;
            this.pendingJobFilter = null;
        }
        // A job opened from another page may never have run yet; still show it as the selected filter.
        if (selectedCommand) commands.add(selectedCommand);
        
        // Populate host filter
        hostFilter.empty().append('<option value="">All Hosts</option>');
        Array.from(hosts).sort().forEach(host => {
            const safeHost = Utils.escapeHtml(String(host));
            hostFilter.append(`<option value="${safeHost}">${safeHost}</option>`);
        });
        hostFilter.val(hosts.has(selectedHost) ? selectedHost : '');
        Utils.syncSearchableSelect(hostFilter[0]);
        
        // Populate command filter
        commandFilter.empty().append('<option value="">All Jobs</option>');
        Array.from(commands).sort().forEach(cmd => {
            const safeCmd = Utils.escapeHtml(String(cmd));
            commandFilter.append(`<option value="${safeCmd}">${safeCmd}</option>`);
        });
        commandFilter.val(selectedCommand);
        Utils.syncSearchableSelect(commandFilter[0]);
    },
    
    // Apply filters
    applyFilters() {
        this.currentFilter = {
            search: $('#searchFilter').val().toLowerCase(),
            host: $('#hostFilter').val(),
            command: $('#commandFilter').val(),
            type: $('#typeFilter').val(),
            status: $('#statusFilter').val(),
            dateFrom: $('#dateFromFilter').val(),
            dateTo: $('#dateToFilter').val()
        };
        
        this.renderLogsTable();
    },
    
    // Clear filters
    clearFilters() {
        $('#searchFilter').val('');
        Utils.setSelectValue('#hostFilter', '');
        Utils.setSelectValue('#commandFilter', '');
        $('#typeFilter').val('');
        $('#statusFilter').val('');
        $('#dateFromFilter').val('');
        $('#dateToFilter').val('');
        this.applyFilters();
    },
    
    // Filter logs based on current criteria
    getFilteredLogs() {
        return this.currentLogs.filter(log => {
            // Search filter
            if (this.currentFilter.search) {
                const searchText = `${log.cmd_id || ''} ${log.host || ''} ${log.filename || ''}`.toLowerCase();
                if (!searchText.includes(this.currentFilter.search)) {
                    return false;
                }
            }
            
            // Host filter
            if (this.currentFilter.host && log.host !== this.currentFilter.host) {
                return false;
            }
            
            // Command filter
            if (this.currentFilter.command && log.cmd_id !== this.currentFilter.command) {
                return false;
            }

            // Type filter (command / runbook step / http_request)
            if (this.currentFilter.type && this.logType(log) !== this.currentFilter.type) {
                return false;
            }
            
            // Status filter
            if (this.currentFilter.status) {
                const logStatus = (log.status || 'Unknown').toLowerCase();
                if (logStatus !== this.currentFilter.status) {
                    return false;
                }
            }
            
            // Date filters
            if (this.currentFilter.dateFrom || this.currentFilter.dateTo) {
                const logDate = new Date(log.modified);
                if (this.currentFilter.dateFrom) {
                    const fromDate = new Date(this.currentFilter.dateFrom);
                    if (logDate < fromDate) return false;
                }
                if (this.currentFilter.dateTo) {
                    const toDate = new Date(this.currentFilter.dateTo);
                    toDate.setHours(23, 59, 59, 999); // End of day
                    if (logDate > toDate) return false;
                }
            }
            
            return true;
        });
    },
    
    // Render logs table
    renderLogsTable() {
        const filteredLogs = this.getFilteredLogs();
        const tbody = $('#logsTableBody');
        let html = '';
        
        $('#logsCount').text(`${filteredLogs.length} logs`);
        
        if (filteredLogs.length === 0) {
            html = `
                <tr>
                    <td colspan="7" class="text-center text-muted">
                        ${this.currentLogs.length === 0 ? 
                            'No logs found. Execute some commands to see logs here.' :
                            'No logs match the current filters.'
                        }
                    </td>
                </tr>
            `;
        } else {
            // Sort by timestamp (newest first)
            filteredLogs.sort((a, b) => new Date(b.modified) - new Date(a.modified));
            
            // Take first 100 for performance
            const displayLogs = filteredLogs.slice(0, 100);
            
            displayLogs.forEach(log => {
                // Use actual status from log file if available
                const status = log.status || 'Unknown';
                const isError = status === 'Failed';
                const statusBadge = status === 'Success' ?
                    '<span class="badge bg-success">Success</span>' :
                    status === 'Failed' ?
                    '<span class="badge bg-danger">Failed</span>' :
                    status === 'Running' ?
                    '<span class="badge bg-info text-dark">Running</span>' :
                    '<span class="badge bg-secondary">Unknown</span>';
                
                // Extract trace ID from filename
                const traceId = log.trace_id || this.extractTraceId(log.filename);
                
                // Format file size
                const fileSize = log.size ? Utils.formatFileSize(log.size) : 'N/A';
                
                html += `
                    <tr class="${isError ? 'table-danger' : ''}">
                        <td>
                            <div class="d-none d-sm-block">${Utils.formatDate(log.modified)}</div>
                            <small class="text-muted">${Utils.formatRelativeTime(log.modified)}</small>
                        </td>
                        <td>
                            <strong class="text-truncate d-inline-block" style="max-width: 150px;">${Utils.escapeHtml(String(log.cmd_id || 'Unknown'))}</strong>
                            ${this.logTypeBadge(log)}
                        </td>
                        <td class="d-none d-md-table-cell">
                            <span class="badge bg-secondary">${log.host || 'Unknown'}</span>
                        </td>
                        <td>${statusBadge}</td>
                        <td class="d-none d-lg-table-cell">${fileSize}</td>
                        <td class="d-none d-lg-table-cell">
                            <code class="small">${traceId || 'N/A'}</code>
                        </td>
                        <td>
                            <div class="btn-group btn-group-sm d-flex justify-content-end">
                                <button class="btn btn-sm btn-outline-primary p-1" onclick="Logs.viewLog('${traceId}')" title="View Details">
                                    <i class="fas fa-eye"></i>
                                </button>
                                <button class="btn btn-sm btn-outline-success p-1" onclick="Logs.downloadLog('${traceId}')" title="Download">
                                    <i class="fas fa-download"></i>
                                </button>
								${this.allowDestructiveActions ? `<button class="btn btn-sm btn-outline-danger p-1" onclick="Logs.deleteLog('${log.filename}')" title="Delete">
                                    <i class="fas fa-trash"></i>
								</button>` : ''}
                            </div>
                        </td>
                    </tr>
                `;
            });
            
            if (filteredLogs.length > 100) {
                html += `
                    <tr>
                        <td colspan="7" class="text-center text-info">
                            Showing first 100 of ${filteredLogs.length} logs. Use filters to narrow down results.
                        </td>
                    </tr>
                `;
            }
        }
        
        tbody.html(html);
    },
    
    // Classify a log entry: http_request, runbook (step log) or command
    logType(log) {
        if (log.job_type === 'disk_check') return 'disk_check';
        if (log.job_type === 'http_request') return 'http_request';
        if (log.runbook_id) return 'runbook';
        return 'command';
    },

    logTypeBadge(log) {
        const type = this.logType(log);
        if (type === 'http_request') return '<br><span class="badge bg-success-subtle text-success-emphasis border border-success-subtle"><i class="fas fa-globe me-1"></i>HTTP</span>';
        if (type === 'disk_check') return '<br><span class="badge bg-warning-subtle text-warning-emphasis border border-warning-subtle"><i class="fas fa-hdd me-1"></i>Disk check</span>';
        if (type === 'runbook') return `<br><span class="badge bg-info-subtle text-info-emphasis border border-info-subtle" title="RunBook ${Utils.escapeHtml(String(log.runbook_id))}"><i class="fas fa-book me-1"></i>RunBook step</span>`;
        return '';
    },

    // Extract trace ID from filename
    extractTraceId(filename) {
        if (!filename) return null;
        const matches = filename.match(/_([a-f0-9]{16,})_/);
        return matches ? matches[1] : null;
    },
    
    // View log details
    async viewLog(traceId) {
        try {
            this.ensureLogModal();
            document.body.appendChild(document.getElementById('logModal')); // last in DOM = on top of any open modal
            $('#logDetails').html(`
                <div class="text-center">
                    <i class="fas fa-spinner fa-spin"></i> Loading log details...
                </div>
            `);
            $('#logModalTitle').text(`Log Details - ${traceId}`);
            // Disabled until this log loads, so the previous log's text can't be copied meanwhile
            $('#copyLogBtn').prop('disabled', true).off('click');
            $('#logModal').modal('show');
            
            const response = await API.ops.getLogContent(traceId);
            
            if (response.success) {
                const data = response.data;
                
                const html = `
                    <div class="row mb-3">
                        <div class="col-md-6">
                            <strong>Filename:</strong> ${data.filename}<br>
                            <strong>Size:</strong> ${Utils.formatFileSize(data.size || 0)}<br>
                            <strong>Modified:</strong> ${Utils.formatDate(data.modified)}
                        </div>
                        <div class="col-md-6">
                            <strong>Trace ID:</strong> <code>${traceId}</code><br>
                            <strong>${data.job_type === 'http_request' ? 'HTTP request' : 'Command'}:</strong> ${data.cmd_id || 'Unknown'}<br>
                            <strong>Host:</strong> ${data.host || 'Unknown'}
                        </div>
                    </div>
                    
                    <div class="mb-3">
                        <strong>Log Content:</strong>
                        <div class="border rounded p-3" style="max-height: 400px; overflow-y: auto;">
                            <pre class="mb-0">${Utils.escapeHtml(data.content || 'No content available')}</pre>
                        </div>
                    </div>
                `;
                
                $('#logDetails').html(html);
                
                // Update download button
                $('#downloadLogBtn').off('click').on('click', () => {
                    this.downloadLog(traceId);
                });

                if (data.content) {
                    $('#copyLogBtn').prop('disabled', false).on('click', (e) => {
                        this.copyLogContent(data.content, e.currentTarget);
                    });
                }
            } else {
                $('#logDetails').html(`
                    <div class="alert alert-danger">
                        Failed to load log content: ${response.error || 'Unknown error'}
                    </div>
                `);
            }
        } catch (error) {
            console.error('Error viewing log:', error);
            $('#logDetails').html(`
                <div class="alert alert-danger">
                    Error loading log details
                </div>
            `);
        }
    },
    
    // Copy the log shown in Log Details and briefly show "Copied" on the button
    async copyLogContent(text, button) {
        await Utils.copyToClipboard(text);
        if (!button || !button.isConnected) return;
        clearTimeout(button._copyResetTimer);
        button.innerHTML = '<i class="fas fa-check"></i> Copied';
        button._copyResetTimer = setTimeout(() => {
            if (!button.isConnected) return;
            button.innerHTML = '<i class="far fa-copy"></i> Copy to Clipboard';
        }, 1500);
    },

    // Download log
    async downloadLog(traceId) {
        try {
            await API.ops.downloadLog(traceId);
        } catch (error) {
            console.error('Error downloading log:', error);
            Utils.showError('Failed to download log');
        }
    },
    
    // Delete log
    async deleteLog(filename) {
        if (!confirm(`Are you sure you want to delete log "${filename}"?`)) {
            return;
        }
        
        try {
            Utils.showLoading();
            
            const response = await API.ops.deleteLog(filename);
            
            if (response.success) {
                Utils.showSuccess('Log deleted successfully');
                await this.loadLogs();
            } else {
                Utils.showError(response.error || 'Failed to delete log');
            }
        } catch (error) {
            console.error('Error deleting log:', error);
            Utils.showError('Failed to delete log');
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Refresh logs
    async refreshLogs() {
        Utils.showLoading();
        try {
            await this.loadLogs();
            Utils.showSuccess('Logs refreshed');
        } catch (error) {
            Utils.showError('Failed to refresh logs');
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Export logs
    async exportLogs() {
        try {
            const filteredLogs = this.getFilteredLogs();
            const csvContent = this.convertLogsToCSV(filteredLogs);
            
            const blob = new Blob([csvContent], { type: 'text/csv' });
            const url = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url;
            a.download = `execution_logs_${Utils.formatDate(new Date(), 'YYYY-MM-DD')}.csv`;
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            URL.revokeObjectURL(url);
            
            Utils.showSuccess('Logs exported successfully');
        } catch (error) {
            console.error('Error exporting logs:', error);
            Utils.showError('Failed to export logs');
        }
    },
    
    // Convert logs to CSV format
    convertLogsToCSV(logs) {
        const headers = ['Timestamp', 'Command ID', 'Host', 'Status', 'Size', 'Trace ID'];
        const rows = logs.map(log => {
            const isError = log.filename && log.filename.includes('error');
            const status = isError ? 'Error' : 'Success';
            const traceId = log.trace_id || this.extractTraceId(log.filename);
            
            return [
                Utils.formatDate(log.modified),
                log.cmd_id || 'Unknown',
                log.host || 'Unknown',
                status,
                log.filename || '',
                traceId || ''
            ];
        });
        
        const csvContent = [headers, ...rows]
            .map(row => row.map(field => `"${field}"`).join(','))
            .join('\n');
            
        return csvContent;
    },
    
    // Clear old logs
    async clearOldLogs() {
        const days = prompt('Delete logs older than how many days?', '30');
        if (!days || isNaN(days) || days < 1) {
            return;
        }
        
        if (!confirm(`Are you sure you want to delete logs older than ${days} days?`)) {
            return;
        }
        
        try {
            Utils.showLoading();
            
            const response = await API.ops.clearOldLogs(parseInt(days));
            
            if (response.success) {
                Utils.showSuccess(`Deleted ${response.data.deleted_count || 0} old log files`);
                await this.loadLogs();
            } else {
                Utils.showError(response.error || 'Failed to clear old logs');
            }
        } catch (error) {
            console.error('Error clearing old logs:', error);
            Utils.showError('Failed to clear old logs');
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Setup auto refresh
    setupAutoRefresh() {
        // Auto refresh every 30 seconds
        this.refreshInterval = setInterval(() => {
            this.loadLogs();
        }, 30000);
    },
    
    // Cleanup
    cleanup() {
        if (this.refreshInterval) {
            clearInterval(this.refreshInterval);
        }
        Utils.destroySearchableSelect('#hostFilter');
        Utils.destroySearchableSelect('#commandFilter');
    }
};
// Make component globally available
window.Logs = Logs;
