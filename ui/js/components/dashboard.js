// Dashboard Component
const Dashboard = {
    // Initialize dashboard
    async init() {
        try {
            // Render dashboard layout
            this.render();
            
            // Load dashboard data
            await this.loadData();
        } catch (error) {
            console.error('Dashboard init error:', error);
            Utils.showError('Failed to load dashboard');
        }
    },
    
    // Render dashboard layout
    render() {
        const html = `
            <div class="dashboard-header">
                <h2><i class="fas fa-tachometer-alt"></i> Dashboard</h2>
                <p>System Overview</p>
            </div>
            
            <div class="row g-4 mb-4">
                <div class="col-md-4 col-xl col-sm-6">
                    <div class="card stat-card text-primary">
                        <div class="card-body">
                            <i class="fas fa-server"></i>
                            <h3 id="statHosts">0</h3>
                            <p>Total Hosts</p>
                        </div>
                    </div>
                </div>
                <div class="col-md-4 col-xl col-sm-6">
                    <div class="card stat-card text-success">
                        <div class="card-body">
                            <i class="fas fa-code"></i>
                            <h3 id="statCommands">0</h3>
                            <p>Total Commands</p>
                        </div>
                    </div>
                </div>
                <div class="col-md-4 col-xl col-sm-6">
                    <div class="card stat-card text-info">
                        <div class="card-body">
                            <i class="fas fa-book"></i>
                            <h3 id="statRunBooks">0</h3>
                            <p>Total RunBooks</p>
                        </div>
                    </div>
                </div>
                <div class="col-md-4 col-xl col-sm-6">
                    <div class="card stat-card text-secondary">
                        <div class="card-body">
                            <i class="fas fa-globe"></i>
                            <h3 id="statHttpRequests">0</h3>
                            <p>HTTP Requests</p>
                        </div>
                    </div>
                </div>
                <div class="col-md-4 col-xl col-sm-6">
                    <div class="card stat-card text-warning">
                        <div class="card-body">
                            <i class="fas fa-clock"></i>
                            <h3 id="statScheduled">0</h3>
                            <p>Scheduled Jobs</p>
                        </div>
                    </div>
                </div>
            </div>
            
            <div class="row g-4">
                <div class="col-12">
                    <div class="card">
                        <div class="card-header">
                            <i class="fas fa-history"></i> Recent Executions
                        </div>
                        <div class="card-body">
                            <div id="recentExecutions">
                                <div class="text-center text-muted">
                                    <i class="fas fa-spinner fa-spin"></i> Loading...
                                </div>
                            </div>
                        </div>
                    </div>
                </div>
            </div>
            
            <div class="row g-4 mt-2">
                <div class="col-12">
                    <div class="card">
                        <div class="card-header">
                            <i class="fas fa-calendar-alt"></i> Scheduled Tasks
                        </div>
                        <div class="card-body">
                            <div id="scheduledTasks">
                                <div class="text-center text-muted">
                                    <i class="fas fa-spinner fa-spin"></i> Loading...
                                </div>
                            </div>
                        </div>
                    </div>
                </div>
            </div>
        `;
        
        $('#mainContent').html(html);
    },
    
    // Load dashboard data
    async loadData() {
        try {
            // Fetch all required data in parallel
            const [configResponse, statusResponse, logsResponse] = await Promise.all([
                API.config.get(),
                API.ops.getStatus(),
                API.ops.getLogs(5)
            ]);
            
            // Update statistics
            if (configResponse.success && configResponse.data) {
                const config = configResponse.data;
                $('#statHosts').text(Object.keys(config.hosts || {}).length);
                $('#statCommands').text((config.commands || []).length);
                $('#statRunBooks').text((config.runbooks || []).length);
                $('#statHttpRequests').text((config.http_requests || []).length);
                
                // Store config for scheduled tasks
                this.currentConfig = config;
            }
            
            if (statusResponse.success && statusResponse.data) {
                const status = statusResponse.data;
                const scheduledCount = (status.scheduled_jobs || []).length + 
                                      (status.scheduled_runbooks || []).length +
                                      (status.scheduled_http_requests || []).length;
                $('#statScheduled').text(scheduledCount);
                
                // Load scheduled tasks with config data
                this.loadScheduledTasks(status, this.currentConfig);
            }
            
            if (logsResponse.success && logsResponse.data) {
                // Load recent executions
                this.loadRecentExecutions(logsResponse.data);
            }
        } catch (error) {
            console.error('Error loading dashboard data:', error);
            Utils.showError('Failed to load dashboard data');
        }
    },
    
    // Load recent executions
    loadRecentExecutions(logs) {
        let html = '';
        
        // Get last 5 logs
        const recentLogs = logs.slice(0, 5);
        
        if (recentLogs.length === 0) {
            html = '<p class="text-center text-muted">No recent executions</p>';
        } else {
            html = '<div class="list-group">';
            recentLogs.forEach(log => {
                const isSuccess = !log.filename.includes('error');
                html += `
                    <div class="list-group-item log-entry ${isSuccess ? 'success' : 'error'}">
                        <div class="d-flex justify-content-between align-items-center">
                            <div>
                                <strong>${log.cmd_id || 'Unknown'}</strong>
                                <br>
                                <small class="text-muted">
                                    ${Utils.formatRelativeTime(log.modified)}
                                </small>
                            </div>
                            <div>
                                <button class="btn btn-sm btn-outline-info" 
                                        onclick="Dashboard.viewLog('${log.trace_id}')">
                                    <i class="fas fa-eye"></i>
                                </button>
                            </div>
                        </div>
                    </div>
                `;
            });
            html += '</div>';
        }
        
        $('#recentExecutions').html(html);
    },
    
    // Load scheduled tasks
    loadScheduledTasks(status, config) {
        let html = '';
        
        const jobs = status.scheduled_jobs || [];
        const runBooks = status.scheduled_runbooks || [];
        const httpRequests = status.scheduled_http_requests || [];
        
        if (jobs.length === 0 && runBooks.length === 0 && httpRequests.length === 0) {
            html = '<p class="text-center text-muted">No scheduled tasks</p>';
        } else {
            html = `
                <div class="table-responsive">
                    <table class="table table-hover">
                        <thead>
                            <tr>
                                <th>Type</th>
                                <th>ID</th>
                                <th>Schedule</th>
                                <th>Next Run</th>
                            </tr>
                        </thead>
                        <tbody>
            `;
            
            // Create a map of command schedules from config
            const commandSchedules = {};
            if (config && config.commands) {
                config.commands.forEach(cmd => {
                    if (cmd.cron_schedule) {
                        commandSchedules[cmd.cmd_id] = cmd.cron_schedule;
                    }
                });
            }
            
            const runBookSchedules = {};
            if (config && config.runbooks) {
                config.runbooks.forEach(runBook => {
                    if (runBook.cron_schedule) {
                        runBookSchedules[runBook.runbook_id] = runBook.cron_schedule;
                    }
                });
            }
            
            const rows = [
                ...jobs.map(job => ({ type: 'Command', badge: 'bg-primary', id: job.cmd_id, schedule: commandSchedules[job.cmd_id] || job.cron_schedule, next_run: job.next_run })),
                ...runBooks.map(runBook => ({ type: 'RunBook', badge: 'bg-info', id: runBook.cmd_id, schedule: runBookSchedules[runBook.cmd_id] || runBook.cron_schedule, next_run: runBook.next_run })),
                ...httpRequests.map(request => ({ type: 'HTTP', badge: 'bg-success', id: request.cmd_id, schedule: request.cron_schedule, next_run: request.next_run }))
            ];
            // Same order as Status → Upcoming (sortScheduledJobStatuses in main.go): soonest first across
            // all types, rows without a next run last, ties by ID
            const hasNextRun = row => !!row.next_run && !String(row.next_run).startsWith('0001-');
            const compare = (a, b) => (a < b ? -1 : a > b ? 1 : 0);
            rows.sort((a, b) => (hasNextRun(b) - hasNextRun(a))
                || compare(String(a.next_run || ''), String(b.next_run || ''))
                || compare(String(a.id || ''), String(b.id || '')));

            rows.forEach(row => {
                const nextRun = row.next_run || '';
                html += `
                    <tr>
                        <td><span class="badge ${row.badge}">${row.type}</span></td>
                        <td>${Utils.escapeHtml(String(row.id || ''))}</td>
                        <td><code>${Utils.escapeHtml(String(row.schedule || 'N/A'))}</code></td>
                        <td${nextRun ? ` title="${Utils.escapeHtml(nextRun)}"` : ''}>${Utils.escapeHtml(Status.formatNextRun(nextRun, status.current_time))}</td>
                    </tr>
                `;
            });
            
            html += `
                        </tbody>
                    </table>
                </div>
            `;
        }
        
        $('#scheduledTasks').html(html);
    },
    
    // View log
    async viewLog(traceId) {
        try {
            Utils.showLoading();
            
            const response = await API.ops.getLogContent(traceId);
            
            if (response.success) {
                const html = `
                    <div class="exec-log-head">
                        <strong>Log:</strong>
                        <span class="exec-log-name text-truncate" title="${Utils.escapeHtml(response.data.filename || '')}">${Utils.escapeHtml(response.data.filename || '')}</span>
                        <button type="button" class="btn btn-sm btn-outline-primary ms-auto flex-shrink-0" title="Download the complete log file"
                                onclick="API.ops.downloadLog('${traceId}')">
                            <i class="fas fa-download me-1"></i>Download
                        </button>
                    </div>
                    <pre class="bg-dark text-light p-3 rounded mb-0">${Utils.escapeHtml(response.data.content || '')}</pre>
                `;
                
                $('#executionOutput').html(html);
                $('#executeModal').modal('show');
            } else {
                Utils.showError('Failed to load log content');
            }
        } catch (error) {
            console.error('Error loading log:', error);
            Utils.showError('Failed to load log content');
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Cleanup
    cleanup() {
        // Any cleanup needed when leaving dashboard
    }
};
// Make component globally available
window.Dashboard = Dashboard;
