// Scheduled Jobs Overview Component
const Overview = {
    data: null,
    selectedDate: null,
    activeView: 'timeline',
    ganttInstance: null,
    table: null,
    charts: {},
    refreshTimer: null,
    resizeObserver: null,
    visibilityHandler: null,
    loadVersion: 0,
    occurrenceIndex: {},
    runModalTimer: null,
    emailSettings: null,
    originalEmailSettings: null,
    emailSMTPReady: false,
    emailDefaultSubject: 'Scheduled Jobs Overview — {{window_label}}',

    statusLabels: {
        success: 'Success',
        failed: 'Failed',
        long_run: 'Long run',
        running: 'Running',
        skipped: 'Skipped',
        no_record: 'No record',
        upcoming: 'Upcoming',
        disabled: 'Disabled',
        history_unavailable: 'History unavailable'
    },

    async init() {
        this.resetState();
        this.render();
        this.bindEvents();
        const loads = [this.loadData()];
        if (Auth.isAdmin()) loads.push(this.loadEmailSettings());
        await Promise.all(loads);
        if (Auth.isAdmin() && this.emailSettings) this.populateEmailSettings();
        this.startAutoRefresh();
    },

    resetState() {
        this.data = null;
        this.selectedDate = null;
        this.activeView = 'timeline';
        this.ganttInstance = null;
        this.table = null;
        this.charts = {};
        this.refreshTimer = null;
        this.resizeObserver = null;
        this.visibilityHandler = null;
        this.loadVersion = 0;
        this.occurrenceIndex = {};
        this.runModalTimer = null;
        this.emailSettings = null;
        this.originalEmailSettings = null;
        this.emailSMTPReady = false;
    },

    render() {
        $('#mainContent').html(`
            <div class="overview-page">
                <div class="overview-toolbar">
                    <div class="overview-title">
                        <h1><i class="fas fa-calendar-day"></i> Scheduled Jobs Overview</h1>
                        <p>Day schedule and execution outcomes</p>
                    </div>
                    <div class="overview-date-controls" aria-label="Overview date controls">
                        <button class="btn btn-outline-secondary btn-sm" id="overviewPreviousDay" type="button" title="Previous day" aria-label="Previous day">
                            <i class="fas fa-chevron-left"></i>
                        </button>
                        <div class="overview-date-input-wrap">
                            <i class="far fa-calendar-alt"></i>
                            <input class="form-control form-control-sm" id="overviewDate" type="date" aria-label="Overview date">
                        </div>
                        <button class="btn btn-outline-secondary btn-sm" id="overviewNextDay" type="button" title="Next day" aria-label="Next day">
                            <i class="fas fa-chevron-right"></i>
                        </button>
                        <button class="btn btn-outline-primary btn-sm" id="overviewToday" type="button">Today</button>
                        <button class="btn btn-outline-primary btn-sm" id="overviewRefresh" type="button">
                            <i class="fas fa-sync-alt"></i> Refresh
                        </button>
                    </div>
                    <div class="overview-updated" id="overviewUpdated" aria-live="polite">Loading…</div>
                </div>

                <div id="overviewNotice"></div>

                <div class="overview-kpi-grid" id="overviewKpis" aria-label="Daily execution statistics">
                    ${this.renderKPISkeletons()}
                </div>

                <div class="overview-main-grid">
                    <section class="overview-panel overview-schedule-panel" aria-labelledby="overviewScheduleTitle">
                        <div class="overview-panel-header">
                            <div>
                                <h2 id="overviewScheduleTitle">Schedule</h2>
                                <div class="overview-legend" aria-label="Schedule status legend">
                                    ${this.renderLegend()}
                                </div>
                            </div>
                            <button class="btn btn-outline-secondary btn-sm" id="overviewViewToggle" type="button">
                                <i class="fas fa-table"></i> View Table
                            </button>
                        </div>
                        <div class="overview-view overview-timeline-view" id="overviewTimelineView">
                            <div class="overview-widget-loading"><i class="fas fa-spinner fa-spin"></i> Loading schedule…</div>
                        </div>
                        <div class="overview-view overview-table-view d-none" id="overviewTableView">
                            <div id="overviewTable"></div>
                        </div>
                    </section>

                    <aside class="overview-analytics" aria-label="Daily schedule analytics">
                        <section class="overview-panel overview-chart-panel">
                            <div class="overview-panel-header compact">
                                <div><h2>Runs by Hour</h2><p>Expected vs observed</p></div>
                            </div>
                            <div class="overview-chart" id="overviewHourlyChart" role="img" aria-label="Expected and observed runs by hour"></div>
                        </section>
                        <section class="overview-panel overview-chart-panel">
                            <div class="overview-panel-header compact">
                                <div><h2>Outcome Breakdown</h2><p>Selected day schedule state</p></div>
                            </div>
                            <div class="overview-chart" id="overviewOutcomeChart" role="img" aria-label="Scheduled job outcome breakdown"></div>
                        </section>
                    </aside>
                </div>

                <div class="overview-footer-note" id="overviewFooterNote"></div>

                ${Auth.isAdmin() ? this.renderEmailSettingsPanel() : ''}
            </div>
        `);
    },

    renderEmailSettingsPanel() {
        return `
            <section class="overview-panel overview-email-settings" aria-labelledby="overviewEmailSettingsTitle">
                <div class="overview-panel-header compact">
                    <div>
                        <h2 id="overviewEmailSettingsTitle"><i class="far fa-envelope"></i> Daily Overview Email</h2>
                        <p>Send this dashboard summary automatically using the configured SMTP server.</p>
                    </div>
                </div>
                <form id="overviewEmailSettingsForm" class="overview-email-settings-body">
                    <div class="form-check form-switch overview-email-enable">
                        <input class="form-check-input" type="checkbox" id="overviewEmailEnabled" disabled>
                        <label class="form-check-label" for="overviewEmailEnabled">Send daily <strong>Scheduled Jobs Overview</strong> email</label>
                    </div>
                    <div class="overview-email-sentence">
                        <span>to</span>
                        <label class="visually-hidden" for="overviewEmailRecipient">Recipient email address</label>
                        <input class="form-control form-control-sm overview-email-recipient" id="overviewEmailRecipient" type="email" maxlength="254" placeholder="Enter your email address" autocomplete="email" disabled>
                        <span>at</span>
                        <label class="visually-hidden" for="overviewEmailHour">Delivery hour</label>
                        <input class="form-control form-control-sm overview-email-time" id="overviewEmailHour" type="number" min="0" max="23" inputmode="numeric" aria-label="Delivery hour" disabled>
                        <span>hrs</span>
                        <label class="visually-hidden" for="overviewEmailMinute">Delivery minute</label>
                        <input class="form-control form-control-sm overview-email-time" id="overviewEmailMinute" type="number" min="0" max="59" inputmode="numeric" aria-label="Delivery minute" disabled>
                        <span>mins</span>
                    </div>
                    <div class="overview-email-period" role="radiogroup" aria-label="Overview reporting period">
                        <div class="form-check form-check-inline">
                            <input class="form-check-input" type="radio" name="overviewEmailPeriod" id="overviewEmailPreviousDay" value="previous_day" disabled>
                            <label class="form-check-label" for="overviewEmailPreviousDay">Previous day</label>
                        </div>
                        <div class="form-check form-check-inline">
                            <input class="form-check-input" type="radio" name="overviewEmailPeriod" id="overviewEmailPast24Hours" value="past_24_hours" disabled>
                            <label class="form-check-label" for="overviewEmailPast24Hours">Past 24 hours</label>
                        </div>
                    </div>
                    <div>
                        <label class="form-label mb-1" for="overviewEmailSubject">Subject line</label>
                        <input class="form-control form-control-sm" id="overviewEmailSubject" type="text" maxlength="200" aria-describedby="overviewEmailSubjectHelp" disabled>
                        <div class="form-text" id="overviewEmailSubjectHelp">Available placeholders: <code>{{window_label}}</code>, <code>{{period}}</code>, <code>{{window_start}}</code>, and <code>{{window_end}}</code>.</div>
                    </div>
                    <div class="overview-email-meta">
                        <span id="overviewEmailTimezone">Delivery uses the server timezone.</span>
                        <span id="overviewEmailSMTPNotice"></span>
                    </div>
                    <div>
                        <button class="btn btn-primary btn-sm" id="overviewEmailSave" type="submit" disabled>
                            <i class="fas fa-save"></i> Save Email Schedule
                        </button>
                    </div>
                </form>
            </section>
        `;
    },

    renderKPISkeletons() {
        return ['Observed runs', 'Failed runs', 'Average runtime', 'Longest run'].map(label => `
            <div class="overview-kpi is-loading">
                <span>${label}</span>
                <strong>—</strong>
                <small>Loading</small>
            </div>
        `).join('');
    },

    renderLegend() {
        return [
            ['success', 'Success'],
            ['failed', 'Failed'],
            ['long_run', 'Long run'],
            ['running', 'Running'],
            ['no_record', 'No record'],
            ['upcoming', 'Upcoming']
        ].map(([status, label]) => `<span><i class="overview-legend-swatch status-${status}"></i>${label}</span>`).join('');
    },

    bindEvents() {
        $('#overviewPreviousDay').on('click', () => this.shiftDate(-1));
        $('#overviewNextDay').on('click', () => this.shiftDate(1));
        $('#overviewToday').on('click', () => {
            this.selectedDate = this.data?.server_today || null;
            this.loadData();
        });
        $('#overviewRefresh').on('click', () => this.loadData());
        $('#overviewDate').on('change', (event) => {
            this.selectedDate = event.target.value || null;
            this.loadData();
        });
        $('#overviewViewToggle').on('click', () => this.toggleView());
        // Clicking a run on the timeline opens its log / run details. Delegated so it survives Gantt re-renders.
        const openFromElement = event => {
            event.preventDefault();
            event.stopPropagation();
            this.openOccurrence(event.currentTarget.dataset.occurrenceId);
        };
        $('#mainContent').off('.overviewRun')
            .on('click.overviewRun', '.overview-occurrence.is-clickable', openFromElement)
            .on('keydown.overviewRun', '.overview-occurrence.is-clickable', event => {
                if (event.key === 'Enter' || event.key === ' ') openFromElement(event);
            });
        if (Auth.isAdmin()) {
            $('#overviewEmailEnabled').on('change', () => this.updateEmailSettingsControlState());
            $('#overviewEmailSettingsForm').on('submit', event => {
                event.preventDefault();
                this.saveEmailSettings();
            });
        }

        this.visibilityHandler = () => {
            if (document.visibilityState === 'visible' && this.isShowingServerToday()) {
                this.loadData({ quiet: true });
            }
        };
        document.addEventListener('visibilitychange', this.visibilityHandler);

        if (window.ResizeObserver) {
            this.resizeObserver = new ResizeObserver(() => this.resizeWidgets());
            const grid = document.querySelector('.overview-main-grid');
            if (grid) this.resizeObserver.observe(grid);
        }
    },

    async loadData({ quiet = false } = {}) {
        const version = ++this.loadVersion;
        if (!quiet) this.setLoading(true);

        try {
            const response = await API.ops.getScheduledJobsOverview(this.selectedDate);
            if (version !== this.loadVersion) return;
            if (!response.success || !response.data) {
                throw new Error(response.error || 'Overview request was unsuccessful');
            }

            this.data = response.data;
            this.selectedDate = response.data.date;
            $('#overviewDate').val(this.selectedDate);
            this.renderData();
        } catch (error) {
            if (version !== this.loadVersion) return;
            console.error('Overview load error:', error);
            if (!this.data) {
                $('#overviewTimelineView').html(`
                    <div class="overview-error-state" role="alert">
                        <i class="fas fa-exclamation-triangle"></i>
                        <h3>Unable to load the schedule</h3>
                        <p>Please check the server and try again.</p>
                    </div>
                `);
            }
            $('#overviewUpdated').text('Refresh failed');
            Utils.showError('Failed to load scheduled jobs overview');
        } finally {
            if (version === this.loadVersion && !quiet) this.setLoading(false);
        }
    },

    async loadEmailSettings() {
        try {
            const response = await API.config.getScheduledJobsOverviewEmail();
            if (!response.success || !response.data?.settings) {
                throw new Error(response.error || 'Email settings request was unsuccessful');
            }
            this.emailSettings = { ...response.data.settings };
            this.originalEmailSettings = { ...response.data.settings };
            this.emailSMTPReady = response.data.smtp_ready === true;
            this.populateEmailSettings();
        } catch (error) {
            console.error('Overview email settings load error:', error);
            $('#overviewEmailSettingsForm').html(`
                <div class="alert alert-danger mb-0" role="alert">
                    <i class="fas fa-exclamation-triangle"></i> Unable to load daily email settings.
                </div>
            `);
        }
    },

    populateEmailSettings() {
        const settings = this.emailSettings || { enabled: false, recipient: '', hour: 8, minute: 0, period: 'previous_day', subject: this.emailDefaultSubject };
        $('#overviewEmailEnabled').prop('checked', settings.enabled === true).prop('disabled', false);
        $('#overviewEmailRecipient').val(settings.recipient || '');
        $('#overviewEmailHour').val(String(Number.isInteger(settings.hour) ? settings.hour : 8).padStart(2, '0'));
        $('#overviewEmailMinute').val(String(Number.isInteger(settings.minute) ? settings.minute : 0).padStart(2, '0'));
        $('#overviewEmailSubject').val(settings.subject || this.emailDefaultSubject);
        $(`input[name="overviewEmailPeriod"][value="${settings.period === 'past_24_hours' ? 'past_24_hours' : 'previous_day'}"]`).prop('checked', true);
        const timezone = this.data?.timezone ? this.formatTimezone(this.data.timezone) : 'server local time';
        $('#overviewEmailTimezone').text(`Delivery time uses ${timezone}.`);
        $('#overviewEmailSMTPNotice').html(this.emailSMTPReady
            ? '<span class="text-success"><i class="fas fa-check-circle"></i> SMTP ready</span>'
            : '<span class="text-danger"><i class="fas fa-exclamation-circle"></i> SMTP is unavailable. <a href="#smtp" data-page="smtp">Configure SMTP</a></span>');
        this.updateEmailSettingsControlState();
    },

    updateEmailSettingsControlState() {
        const enabled = $('#overviewEmailEnabled').is(':checked');
        $('#overviewEmailRecipient, #overviewEmailHour, #overviewEmailMinute, #overviewEmailSubject, input[name="overviewEmailPeriod"]').prop('disabled', !enabled);
        $('#overviewEmailRecipient').prop('required', enabled);
        $('#overviewEmailSave').prop('disabled', false);
        $('.overview-email-settings-body').toggleClass('is-disabled', !enabled);
    },

    getEmailSettingsFormValues() {
        return {
            enabled: $('#overviewEmailEnabled').is(':checked'),
            recipient: $('#overviewEmailRecipient').val().trim(),
            hour: Number.parseInt($('#overviewEmailHour').val(), 10),
            minute: Number.parseInt($('#overviewEmailMinute').val(), 10),
            period: $('input[name="overviewEmailPeriod"]:checked').val() || 'previous_day',
            subject: $('#overviewEmailSubject').val().trim()
        };
    },

    validateEmailSettings(settings) {
        if (!Number.isInteger(settings.hour) || settings.hour < 0 || settings.hour > 23) {
            Utils.showError('Delivery hour must be between 00 and 23');
            return false;
        }
        if (!Number.isInteger(settings.minute) || settings.minute < 0 || settings.minute > 59) {
            Utils.showError('Delivery minute must be between 00 and 59');
            return false;
        }
        if (settings.enabled) {
            const recipient = document.getElementById('overviewEmailRecipient');
            if (!recipient?.checkValidity()) {
                recipient?.reportValidity();
                return false;
            }
            if (!this.emailSMTPReady) {
                Utils.showError('Configure and enable SMTP before enabling the daily overview email');
                return false;
            }
        }
        return true;
    },

    async saveEmailSettings() {
        const settings = this.getEmailSettingsFormValues();
        if (!this.validateEmailSettings(settings)) return false;

        const button = $('#overviewEmailSave');
        button.prop('disabled', true).html('<i class="fas fa-spinner fa-spin"></i> Saving…');
        try {
            const response = await API.config.updateScheduledJobsOverviewEmail(settings);
            if (!response.success || !response.data?.settings) {
                throw new Error(response.error || 'Failed to save email schedule');
            }
            this.emailSettings = { ...response.data.settings };
            this.originalEmailSettings = { ...response.data.settings };
            this.emailSMTPReady = response.data.smtp_ready === true;
            this.populateEmailSettings();
            Utils.showSuccess('Daily Scheduled Jobs Overview email settings saved');
            return true;
        } catch (error) {
            console.error('Overview email settings save error:', error);
            Utils.showError(error.message || 'Failed to save daily overview email settings');
            return false;
        } finally {
            button.html('<i class="fas fa-save"></i> Save Email Schedule').prop('disabled', false);
        }
    },

    setLoading(loading) {
        $('#overviewRefresh, #overviewPreviousDay, #overviewNextDay, #overviewToday, #overviewDate').prop('disabled', loading);
        $('#overviewRefresh i').toggleClass('fa-spin', loading);
        $('.overview-page').toggleClass('is-refreshing', loading);
    },

    renderData() {
        this.renderNotices();
        this.renderKPIs();
        this.renderCharts();
        this.renderActiveView();

        const timezone = this.formatTimezone(this.data.timezone);
        $('#overviewUpdated').text(`Updated ${this.formatServerDateTime(this.data.generated_at)} · ${timezone}`);
        $('#overviewScheduleTitle').text(`Schedule (${this.data.date})`);
        $('#overviewFooterNote').text(`All hours use the server timezone (${timezone}). Auto-refresh runs every minute while viewing today.`);
    },

    renderNotices() {
        const notices = [];
        if (!this.data.cron_enabled) {
            notices.push({ type: 'warning', icon: 'fa-pause-circle', text: 'Cron scheduling is currently disabled. Future occurrences are shown as Disabled.' });
        }
        if (this.data.date !== this.data.server_today) {
            notices.push({ type: 'info', icon: 'fa-info-circle', text: 'Historical and future projections use the cron expressions in the current configuration.' });
        }
        if ((this.data.summary?.history_unavailable_runs || 0) > 0) {
            notices.push({ type: 'secondary', icon: 'fa-archive', text: `Some execution history is outside the configured ${this.data.log_retention_days}-day retention window.` });
        }
        (this.data.warnings || []).forEach(text => notices.push({ type: 'warning', icon: 'fa-exclamation-triangle', text }));

        $('#overviewNotice').html(notices.map(notice => `
            <div class="alert alert-${notice.type} overview-notice" role="status">
                <i class="fas ${notice.icon}"></i><span>${this.escape(notice.text)}</span>
            </div>
        `).join(''));
    },

    renderKPIs() {
        const summary = this.data.summary || {};
        const cards = [
            { label: 'Observed runs', value: this.formatNumber(summary.observed_runs), detail: `${this.formatNumber(summary.expected_runs)} expected`, tone: 'primary', icon: 'fa-play-circle' },
            { label: 'Failed runs', value: this.formatNumber(summary.failed_runs), detail: summary.running_runs ? `${summary.running_runs} running` : 'Completed failures', tone: 'danger', icon: 'fa-exclamation-circle' },
            { label: 'Average runtime', value: this.formatDuration(summary.average_runtime_ms), detail: 'Completed observed runs', tone: 'info', icon: 'fa-stopwatch' },
            { label: 'Longest run', value: this.formatDuration(summary.longest_runtime_ms), detail: summary.longest_runtime_job || 'No completed runs', tone: 'warning', icon: 'fa-hourglass-half' }
        ];
        $('#overviewKpis').html(cards.map(card => `
            <div class="overview-kpi tone-${card.tone}">
                <div class="overview-kpi-icon"><i class="fas ${card.icon}"></i></div>
                <div><span>${card.label}</span><strong>${this.escape(card.value)}</strong><small>${this.escape(card.detail)}</small></div>
            </div>
        `).join(''));
    },

    renderActiveView() {
        this.occurrenceIndex = {};
        (this.data?.jobs || []).forEach(job => {
            (job.occurrences || []).forEach(occurrence => { this.occurrenceIndex[occurrence.id] = { occurrence, job }; });
        });
        if (this.activeView === 'table') {
            this.renderTable();
        } else {
            this.renderGantt();
        }
    },

    renderGantt() {
        this.destroyGantt();
        const jobs = this.data?.jobs || [];
        const container = $('#overviewTimelineView');
        if (!jobs.length) {
            container.html(`
                <div class="overview-empty-state">
                    <i class="far fa-calendar-times"></i>
                    <h3>No scheduled jobs</h3>
                    <p>Add a cron schedule to a Command, RunBook or HTTP request to see it here.</p>
                </div>
            `);
            return;
        }

        const height = Math.min(720, Math.max(380, 74 + jobs.length * 34));
        container.html(`<div id="overviewGantt" style="height:${height}px"></div>`);
        const factory = window.dhtmlxgantt?.Gantt || window.Gantt;
        if (!factory || typeof factory.getGanttInstance !== 'function') {
            container.html('<div class="overview-error-state"><p>DHTMLX Gantt failed to load.</p></div>');
            return;
        }

        const gantt = factory.getGanttInstance();
        this.ganttInstance = gantt;
        const dayStart = this.syntheticDate(this.data.date);
        const dayEnd = new Date(dayStart.getFullYear(), dayStart.getMonth(), dayStart.getDate() + 1);

        gantt.config.readonly = true;
        gantt.config.show_links = false;
        gantt.config.drag_move = false;
        gantt.config.drag_resize = false;
        gantt.config.drag_progress = false;
        gantt.config.drag_links = false;
        gantt.config.details_on_dblclick = false;
        gantt.config.select_task = false;
        gantt.config.smart_rendering = true;
        gantt.config.row_height = 34;
        gantt.config.task_height = 20;
        gantt.config.scale_height = 34;
        gantt.config.grid_width = 205;
        gantt.config.min_column_width = 54;
        gantt.config.start_date = dayStart;
        gantt.config.end_date = dayEnd;
        gantt.config.fit_tasks = false;
        gantt.config.columns = [{ name: 'text', label: 'Job', width: '*', tree: false, template: task => this.escape(task.text) }];
        gantt.config.scales = [{
            unit: 'hour',
            step: 2,
            format: date => `${String(date.getHours()).padStart(2, '0')}:00`
        }];
        gantt.templates.task_class = () => 'overview-gantt-task';
        gantt.templates.task_text = (start, end, task) => this.renderOccurrenceSegments(task.occurrences || []);
        gantt.templates.tooltip_text = () => '';

        gantt.init('overviewGantt');
        gantt.parse({
            data: jobs.map((job, index) => ({
                id: index + 1,
                text: job.job_id,
                start_date: new Date(dayStart),
                end_date: new Date(dayEnd),
                readonly: true,
                occurrences: job.occurrences || []
            })),
            links: []
        });
    },

    renderOccurrenceSegments(occurrences) {
        const secondsInDay = 24 * 60 * 60;
        return `<div class="overview-gantt-track">${occurrences.map(occurrence => {
            const rawOffset = occurrence.actual_offset_seconds ?? occurrence.scheduled_offset_seconds ?? 0;
            const offset = Math.max(0, Math.min(secondsInDay, rawOffset));
            const durationSeconds = occurrence.actual_start_at ? Math.max(0, (occurrence.duration_ms || 0) / 1000) : 0;
            const left = (offset / secondsInDay) * 100;
            const width = Math.max(0, Math.min(100 - left, (durationSeconds / secondsInDay) * 100));
            const status = this.safeStatus(occurrence.status);
            const clickable = this.isOpenable(occurrence);
            const label = this.occurrenceLabel(occurrence) + (clickable ? ' · Click to open' : '');
            return `<span class="overview-occurrence status-${status}${clickable ? ' is-clickable' : ''}" data-occurrence-id="${this.escape(occurrence.id)}"${clickable ? ' role="button"' : ''} style="left:${left.toFixed(5)}%;width:${width.toFixed(5)}%" title="${this.escape(label)}" aria-label="${this.escape(label)}" tabindex="0"></span>`;
        }).join('')}</div>`;
    },

    // A run can be opened when a log (Command / HTTP request) or a RunBook run exists for it.
    isOpenable(occurrence) {
        return Boolean(occurrence && (occurrence.trace_id || occurrence.run_id));
    },

    openOccurrence(id) {
        const entry = this.occurrenceIndex[id];
        if (!entry || !this.isOpenable(entry.occurrence)) return;
        const { occurrence, job } = entry;
        if (occurrence.run_id && (job.type === 'runbook' || !occurrence.trace_id)) {
            this.showRunBookRun(job.job_id, occurrence.run_id);
            return;
        }
        Logs.viewLog(occurrence.trace_id);
    },

    ensureRunModal() {
        if (document.getElementById('overviewRunModal')) return;
        $(document.body).append(`
            <div class="modal fade" id="overviewRunModal" tabindex="-1" aria-labelledby="overviewRunModalTitle">
                <div class="modal-dialog modal-xl modal-dialog-scrollable">
                    <div class="modal-content">
                        <div class="modal-header">
                            <h5 class="modal-title" id="overviewRunModalTitle">RunBook run</h5>
                            <button type="button" class="btn-close" data-bs-dismiss="modal" aria-label="Close"></button>
                        </div>
                        <div class="modal-body"><div id="overviewRunDetails"></div></div>
                        <div class="modal-footer">
                            <span id="overviewRunModalHint" class="me-auto small text-muted"></span>
                            <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Close</button>
                            <button type="button" class="btn btn-primary" id="overviewRunOpenButton"><i class="fas fa-book me-1"></i>Open in RunBooks</button>
                        </div>
                    </div>
                </div>
            </div>`);
        document.getElementById('overviewRunModal').addEventListener('hidden.bs.modal', () => {
            this.stopRunModalRefresh();
            if (document.querySelector('.modal.show')) document.body.classList.add('modal-open');
        });
    },

    stopRunModalRefresh() {
        if (this.runModalTimer) window.clearInterval(this.runModalTimer);
        this.runModalTimer = null;
    },

    // Popup with a RunBook run's progress and steps (each step has its own Log panel). Refreshes while the run is active.
    async showRunBookRun(runBookId, runId) {
        this.ensureRunModal();
        this.stopRunModalRefresh();
        const modalEl = document.getElementById('overviewRunModal');
        document.body.appendChild(modalEl); // last in DOM = on top of any other open modal
        $('#overviewRunModalTitle').text(`${runBookId} · run ${runId}`);
        $('#overviewRunDetails').html('<div class="text-center py-4 text-muted"><i class="fas fa-spinner fa-spin me-2"></i>Loading run…</div>');
        $('#overviewRunModalHint').text('');
        $('#overviewRunOpenButton').toggle(Auth.isAdmin()).off('click').on('click', async () => {
            bootstrap.Modal.getOrCreateInstance(modalEl).hide();
            if (await Router.navigate('runbooks', true, true)) RunBooks.showRun(runBookId, runId);
        });
        bootstrap.Modal.getOrCreateInstance(modalEl).show();

        RunBooks.resetStepLogs();
        const refresh = async () => {
            try {
                const response = await API.ops.getRunBookRun(runBookId, runId);
                const run = response.data || {};
                const terminal = RunBooks.isTerminalRunStatus(run.status);
                const details = document.getElementById('overviewRunDetails');
                RunBooks.captureStepLogScroll(details);
                $('#overviewRunDetails').html(`
                    <div class="d-flex justify-content-between align-items-start mb-3">
                        <div><strong>${this.escape(runBookId)}</strong><div class="small text-muted">Run ${this.escape(run.run_id || runId)}${run.trigger ? ` · ${this.escape(run.trigger)}` : ''}${run.started_at ? ` · ${Utils.formatDate(run.started_at)}` : ''}</div></div>
                        ${RunBooks.statusBadge(run.status)}
                    </div>
                    ${run.error ? `<div class="alert alert-danger">${this.escape(run.error)}</div>` : ''}
                    ${RunBooks.runDetailsHtml(runBookId, run, { allowEdit: false })}`);
                RunBooks.afterRunRender(details, run);
                $('#overviewRunModalHint').text(terminal ? '' : 'Still running · refreshing every 2 seconds');
                if (terminal) this.stopRunModalRefresh();
            } catch (error) {
                this.stopRunModalRefresh();
                $('#overviewRunDetails').html(`<div class="alert alert-danger">${this.escape(error.message || 'Failed to load this run')}</div>`);
            }
        };
        this.runModalTimer = window.setInterval(refresh, 2000);
        await refresh();
    },

    occurrenceLabel(occurrence) {
        const parts = [this.statusLabels[occurrence.status] || occurrence.status];
        if (occurrence.expected) parts.push(`Scheduled ${this.formatServerTime(occurrence.scheduled_at)}`);
        if (occurrence.actual_start_at) parts.push(`Started ${this.formatServerTime(occurrence.actual_start_at)}`);
        if (occurrence.duration_ms) parts.push(`Runtime ${this.formatDuration(occurrence.duration_ms)}`);
        if (occurrence.unmatched_actual) parts.push('Not matched by the current schedule');
        return parts.join(' · ');
    },

    renderTable() {
        const rows = this.flattenOccurrences();
        const height = Math.min(720, Math.max(380, 74 + Math.min(rows.length, 18) * 34));
        $('#overviewTable').css('height', `${height}px`);
        if (this.table) {
            this.table.replaceData(rows);
            return;
        }
        if (!window.Tabulator) {
            $('#overviewTable').html('<div class="overview-error-state"><p>Tabulator failed to load.</p></div>');
            return;
        }

        this.table = new Tabulator('#overviewTable', {
            data: rows,
            index: 'id',
            height: `${height}px`,
            layout: 'fitColumns',
            placeholder: 'No occurrences for this day',
            initialSort: [{ column: 'scheduled_sort', dir: 'asc' }],
            rowFormatter: row => {
                const entry = this.occurrenceIndex[row.getData().id];
                row.getElement().classList.toggle('overview-row-clickable', Boolean(entry && this.isOpenable(entry.occurrence)));
            },
            columns: [
                { title: 'Scheduled', field: 'scheduled', width: 105, sorter: 'string' },
                { title: 'Job', field: 'job_id', minWidth: 180, headerFilter: 'input', formatter: 'plaintext' },
                { title: 'Type', field: 'type', width: 115, headerFilter: 'list', headerFilterParams: { values: { '': 'All', command: 'Command', runbook: 'RunBook', http_request: 'HTTP Request' } }, formatter: cell => ({ runbook: 'RunBook', http_request: 'HTTP Request' })[cell.getValue()] || 'Command' },
                { title: 'Cron', field: 'cron_schedule', minWidth: 125, formatter: 'plaintext' },
                { title: 'Status', field: 'status', width: 155, headerFilter: 'list', headerFilterParams: { values: this.tableStatusOptions() }, formatter: cell => this.statusBadge(cell.getValue()) },
                { title: 'Actual start', field: 'actual_start', width: 115 },
                { title: 'Runtime', field: 'runtime', width: 105, hozAlign: 'right' },
                { title: 'Trace / Run ID', field: 'execution_id', minWidth: 165, formatter: 'plaintext' },
                { title: '_sort', field: 'scheduled_sort', visible: false }
            ]
        });
        this.table.on('rowClick', (event, row) => this.openOccurrence(row.getData().id));
    },

    flattenOccurrences() {
        const rows = [];
        (this.data?.jobs || []).forEach(job => {
            (job.occurrences || []).forEach(occurrence => {
                rows.push({
                    id: occurrence.id,
                    scheduled: this.formatServerTime(occurrence.scheduled_at),
                    scheduled_sort: occurrence.scheduled_at,
                    job_id: job.job_id,
                    type: job.type,
                    cron_schedule: job.cron_schedule || (job.configured ? '—' : 'Historical'),
                    status: occurrence.status,
                    actual_start: occurrence.actual_start_at ? this.formatServerTime(occurrence.actual_start_at) : '—',
                    runtime: occurrence.actual_start_at ? this.formatDuration(occurrence.duration_ms) : '—',
                    execution_id: occurrence.trace_id || occurrence.run_id || '—'
                });
            });
        });
        return rows;
    },

    tableStatusOptions() {
        const values = { '': 'All statuses' };
        Object.entries(this.statusLabels).forEach(([value, label]) => { values[value] = label; });
        return values;
    },

    statusBadge(status) {
        const element = document.createElement('span');
        element.className = `overview-status-badge status-${this.safeStatus(status)}`;
        element.textContent = this.statusLabels[status] || status;
        return element;
    },

    renderCharts() {
        if (!window.echarts) return;
        this.renderHourlyChart();
        this.renderOutcomeChart();
    },

    renderHourlyChart() {
        const element = document.getElementById('overviewHourlyChart');
        if (!element) return;
        const chart = this.charts.hourly || echarts.init(element);
        this.charts.hourly = chart;
        const hourly = this.data.hourly || [];
        chart.setOption({
            animationDuration: 350,
            aria: { enabled: true },
            color: ['#c9d8ee', '#2f73c9'],
            tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
            legend: { bottom: 0, itemWidth: 12, itemHeight: 8, textStyle: { fontSize: 11 } },
            grid: { top: 14, right: 12, bottom: 48, left: 38 },
            xAxis: {
                type: 'category',
                data: hourly.map(bucket => String(bucket.hour).padStart(2, '0')),
                axisTick: { show: false },
                axisLabel: { interval: 3, fontSize: 10 }
            },
            yAxis: { type: 'value', minInterval: 1, splitLine: { lineStyle: { color: '#edf0f4' } } },
            series: [
                { name: 'Expected', type: 'bar', data: hourly.map(bucket => bucket.expected), barMaxWidth: 12 },
                { name: 'Observed', type: 'bar', data: hourly.map(bucket => bucket.observed), barMaxWidth: 12 }
            ]
        }, true);
    },

    renderOutcomeChart() {
        const element = document.getElementById('overviewOutcomeChart');
        if (!element) return;
        const chart = this.charts.outcomes || echarts.init(element);
        this.charts.outcomes = chart;
        const summary = this.data.summary || {};
        const values = [
            ['Success', Math.max(0, (summary.success_runs || 0) - (summary.long_runs || 0)), '#4caf50'],
            ['Long run', summary.long_runs || 0, '#f59e0b'],
            ['Failed', summary.failed_runs || 0, '#ef4444'],
            ['Running', summary.running_runs || 0, '#2563eb'],
            ['Skipped', summary.skipped_runs || 0, '#8b5cf6'],
            ['No record', summary.no_record_runs || 0, '#9ca3af'],
            ['Upcoming', summary.upcoming_runs || 0, '#93c5fd'],
            ['Disabled', summary.disabled_runs || 0, '#64748b'],
            ['Unavailable', summary.history_unavailable_runs || 0, '#d1d5db']
        ].filter(item => item[1] > 0);

        chart.setOption({
            animationDuration: 350,
            aria: { enabled: true },
            tooltip: { trigger: 'item', formatter: '{b}: {c} ({d}%)' },
            legend: { type: 'scroll', bottom: 0, itemWidth: 11, itemHeight: 8, textStyle: { fontSize: 10 } },
            graphic: values.length ? [] : [{ type: 'text', left: 'center', top: 'middle', style: { text: 'No occurrences', fill: '#7b8491', fontSize: 13 } }],
            series: [{
                type: 'pie',
                radius: ['44%', '70%'],
                center: ['50%', '43%'],
                avoidLabelOverlap: true,
                label: { show: false },
                emphasis: { label: { show: true, fontWeight: 600 } },
                data: values.map(([name, value, color]) => ({ name, value, itemStyle: { color } }))
            }]
        }, true);
    },

    toggleView() {
        this.activeView = this.activeView === 'timeline' ? 'table' : 'timeline';
        const showingTable = this.activeView === 'table';
        $('#overviewTimelineView').toggleClass('d-none', showingTable);
        $('#overviewTableView').toggleClass('d-none', !showingTable);
        $('#overviewViewToggle').html(showingTable
            ? '<i class="fas fa-stream"></i> View Timeline'
            : '<i class="fas fa-table"></i> View Table');
        this.renderActiveView();
        window.setTimeout(() => this.resizeWidgets(), 0);
    },

    startAutoRefresh() {
        this.refreshTimer = window.setInterval(() => {
            if (document.visibilityState === 'visible' && this.isShowingServerToday()) {
                this.loadData({ quiet: true });
            }
        }, 60000);
    },

    isShowingServerToday() {
        return !!this.data && this.selectedDate === this.data.server_today;
    },

    shiftDate(days) {
        const base = this.selectedDate || this.data?.server_today;
        if (!base) return;
        const [year, month, day] = base.split('-').map(Number);
        const shifted = new Date(Date.UTC(year, month - 1, day + days));
        this.selectedDate = `${shifted.getUTCFullYear()}-${String(shifted.getUTCMonth() + 1).padStart(2, '0')}-${String(shifted.getUTCDate()).padStart(2, '0')}`;
        this.loadData();
    },

    syntheticDate(value) {
        const [year, month, day] = value.split('-').map(Number);
        return new Date(year, month - 1, day, 0, 0, 0, 0);
    },

    resizeWidgets() {
        Object.values(this.charts).forEach(chart => chart?.resize());
        if (this.ganttInstance && this.activeView === 'timeline') {
            this.ganttInstance.setSizes();
        }
        if (this.table && this.activeView === 'table') {
            this.table.redraw(true);
        }
    },

    destroyGantt() {
        if (!this.ganttInstance) return;
        try {
            this.ganttInstance.destructor();
        } catch (error) {
            console.warn('Failed to destroy Overview Gantt instance:', error);
        }
        this.ganttInstance = null;
    },

    safeStatus(status) {
        return Object.prototype.hasOwnProperty.call(this.statusLabels, status) ? status : 'history_unavailable';
    },

    formatServerDateTime(value) {
        const match = String(value || '').match(/^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2})/);
        return match ? `${match[1]} ${match[2]}` : String(value || '');
    },

    formatServerTime(value) {
        const match = String(value || '').match(/T(\d{2}:\d{2}:\d{2})/);
        return match ? match[1] : '—';
    },

    formatTimezone(timezone = {}) {
        const seconds = Number(timezone.offset_seconds || 0);
        const sign = seconds >= 0 ? '+' : '-';
        const absolute = Math.abs(seconds);
        const hours = String(Math.floor(absolute / 3600)).padStart(2, '0');
        const minutes = String(Math.floor((absolute % 3600) / 60)).padStart(2, '0');
        return `${timezone.name || 'Server'} (UTC${sign}${hours}:${minutes})`;
    },

    formatDuration(milliseconds) {
        const value = Number(milliseconds || 0);
        if (value <= 0) return '—';
        if (value < 1000) return `${Math.round(value)}ms`;
        if (value < 60000) return `${(value / 1000).toFixed(value < 10000 ? 1 : 0)}s`;
        const minutes = Math.floor(value / 60000);
        const seconds = Math.round((value % 60000) / 1000);
        if (minutes < 60) return `${minutes}m ${seconds}s`;
        const hours = Math.floor(minutes / 60);
        return `${hours}h ${minutes % 60}m`;
    },

    formatNumber(value) {
        return Number(value || 0).toLocaleString();
    },

    escape(value) {
        return String(value ?? '')
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#039;');
    },

    hasUnsavedChanges() {
        if (!Auth.isAdmin() || !this.originalEmailSettings || !document.getElementById('overviewEmailSettingsForm')) return false;
        return JSON.stringify(this.getEmailSettingsFormValues()) !== JSON.stringify(this.originalEmailSettings);
    },

    async confirmNavigation() {
        if (!this.hasUnsavedChanges()) return true;
        const result = await Swal.fire({
            icon: 'warning',
            title: 'Save your email schedule?',
            text: 'You have unsaved Daily Overview Email changes.',
            showCancelButton: true,
            showDenyButton: true,
            confirmButtonColor: '#667eea',
            denyButtonColor: '#dc3545',
            cancelButtonColor: '#6c757d',
            confirmButtonText: 'Save Changes',
            denyButtonText: 'Discard Changes',
            cancelButtonText: 'Stay on This Page'
        });
        if (result.isConfirmed) return this.saveEmailSettings();
        return result.isDenied === true;
    },

    cleanup() {
        this.loadVersion++;
        if (this.refreshTimer) window.clearInterval(this.refreshTimer);
        this.stopRunModalRefresh();
        $('#mainContent').off('.overviewRun');
        const runModal = document.getElementById('overviewRunModal');
        if (runModal && runModal.classList.contains('show')) bootstrap.Modal.getOrCreateInstance(runModal).hide();
        if (this.visibilityHandler) document.removeEventListener('visibilitychange', this.visibilityHandler);
        if (this.resizeObserver) this.resizeObserver.disconnect();
        this.destroyGantt();
        if (this.table) {
            this.table.destroy();
            this.table = null;
        }
        Object.values(this.charts).forEach(chart => chart?.dispose());
        this.charts = {};
    }
};

window.Overview = Overview;
