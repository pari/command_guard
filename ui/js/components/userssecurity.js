const UsersSecurity = {
    users: [], commands: [], runbooks: [], httpRequests: [], hosts: [], vncConnections: [], timezone: '', editing: null, auditPage: 1,

    // Create / Edit user dialog state. The form object is the source of truth; the inputs write to it.
    form: null,
    section: 'account',
    accessTab: 'commands',
    accessFilter: '',
    selectedOnly: false,
    showErrors: false,
    visited: new Set(),
    snapshot: '',
    closeConfirmed: false,

    WEEKDAYS: ['monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday'],
    SECTIONS: [
        { id: 'account', label: 'Account' },
        { id: 'access', label: 'Access' },
        { id: 'rules', label: 'Sign-in rules' }
    ],
    // Same rules as validateUser in auth.go and the reset-password endpoint
    USERNAME_PATTERN: /^[a-z0-9.-]+$/,
    PASSWORD_MIN: 12,
    PASSWORD_MAX: 128,
    // No look-alike characters (0/O, 1/l/I), so the password can be read out or typed from a note
    PASSWORD_ALPHABET: 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#%^*-_=+?',
    PASSWORD_LENGTH: 16,

    esc(value) { return $('<div>').text(value == null ? '' : String(value)).html(); },
    // For attribute values: esc() leaves quotes alone
    escAttr(value) { return this.esc(value).replace(/"/g, '&quot;').replace(/'/g, '&#39;'); },

    async init() { this.render(); await this.load(); },

    render() {
        $('#mainContent').html(`<div class="page-header"><div><h2><i class="fas fa-users-cog me-2"></i>Users & Security</h2><p class="text-muted">Manage role-based access, sign-in policies, and the security audit trail.</p></div></div>
        <ul class="nav nav-tabs mb-3"><li class="nav-item"><button class="nav-link active" data-bs-toggle="tab" data-bs-target="#usersTab">Users</button></li><li class="nav-item"><button class="nav-link" data-bs-toggle="tab" data-bs-target="#auditTab" onclick="UsersSecurity.loadAudit()">Security audit</button></li></ul>
        <div class="tab-content"><div class="tab-pane fade show active" id="usersTab"><div class="d-flex justify-content-between align-items-center mb-3"><small class="text-muted" id="userTimezone"></small><button class="btn btn-primary" onclick="UsersSecurity.openUser()"><i class="fas fa-plus me-1"></i>Create user</button></div><div class="card"><div class="table-responsive"><table class="table table-hover mb-0"><thead><tr><th>User</th><th>Status</th><th>Commands</th><th>RunBooks</th><th>HTTP Requests</th><th>Terminals</th><th>Desktops</th><th>Network & time policy</th><th></th></tr></thead><tbody id="securityUsersBody"></tbody></table></div></div></div>
        <div class="tab-pane fade" id="auditTab"><div class="input-group mb-3" style="max-width:520px"><input id="auditFilter" class="form-control" placeholder="Filter actor, action, target, IP…"><button class="btn btn-outline-primary" onclick="UsersSecurity.loadAudit(1)">Filter</button></div><div class="card"><div class="table-responsive"><table class="table table-sm mb-0"><thead><tr><th>Time</th><th>Actor</th><th>Source IP</th><th>Action</th><th>Target</th><th>Outcome</th></tr></thead><tbody id="securityAuditBody"></tbody></table></div><div class="card-footer d-flex justify-content-between"><button class="btn btn-sm btn-outline-secondary" id="auditPrev" onclick="UsersSecurity.loadAudit(UsersSecurity.auditPage-1)">Previous</button><span id="auditPageLabel"></span><button class="btn btn-sm btn-outline-secondary" id="auditNext" onclick="UsersSecurity.loadAudit(UsersSecurity.auditPage+1)">Next</button></div></div></div></div>
        ${this.userModalHtml()}`);
        this.bindUserModal();
    },

    userModalHtml() {
        return `
        <div class="modal fade um-modal" id="securityUserModal" tabindex="-1" aria-labelledby="securityUserTitle">
          <div class="modal-dialog modal-xl modal-dialog-scrollable"><div class="modal-content">
            <div class="modal-header"><h5 class="modal-title" id="securityUserTitle">Create user</h5><button type="button" class="btn-close" data-bs-dismiss="modal" aria-label="Close"></button></div>
            <nav class="um-steps" id="umSteps" aria-label="Sections"></nav>
            <div class="modal-body">
              <form id="securityUserForm" novalidate autocomplete="off" onsubmit="return false">
                <div class="um-layout">
                  <div class="um-main">

                    <section class="um-section" data-section="account">
                      <div class="mb-4">
                        <label class="form-label" for="securityUsername">Username</label>
                        <input id="securityUsername" class="form-control" maxlength="64" autocomplete="off" autocapitalize="none" spellcheck="false">
                        <div class="um-fixed-value d-none" id="umUsernameFixed"></div>
                        <div class="um-hint" id="umUsernameHint"></div>
                      </div>
                      <div class="mb-4" id="temporaryPasswordGroup">
                        <label class="form-label" for="securityTemporaryPassword">Temporary password</label>
                        <div class="input-group">
                          <input id="securityTemporaryPassword" type="password" class="form-control font-monospace" maxlength="${this.PASSWORD_MAX}" autocomplete="new-password" spellcheck="false">
                          <button type="button" class="btn btn-outline-secondary" id="umPwShow" title="Show password" aria-label="Show password"><i class="fas fa-eye"></i></button>
                          <button type="button" class="btn btn-outline-secondary" id="umPwNew" title="Generate a new password"><i class="fas fa-sync-alt me-1"></i>New</button>
                          <button type="button" class="btn btn-outline-secondary" id="umPwCopy" title="Copy the password"><i class="fas fa-copy me-1"></i>Copy</button>
                        </div>
                        <div class="um-hint" id="umPasswordHint"></div>
                        <div class="form-text"><i class="fas fa-info-circle me-1"></i>Send it to them securely. At first sign-in they choose their own password and set up an authenticator app.</div>
                      </div>
                      <div class="mb-4 d-none" id="umAccountStatus"></div>
                      <div>
                        <label class="form-label d-block" for="securityEnabled">Status</label>
                        <div class="form-check form-switch um-switch">
                          <input id="securityEnabled" class="form-check-input" type="checkbox" role="switch">
                          <label class="form-check-label" for="securityEnabled" id="umEnabledLabel"></label>
                        </div>
                      </div>
                    </section>

                    <section class="um-section" data-section="access">
                      <ul class="nav nav-pills um-access-tabs" id="umAccessTabs" role="tablist"></ul>
                      <div class="um-access-tools">
                        <div class="input-group input-group-sm um-filter">
                          <span class="input-group-text"><i class="fas fa-search"></i></span>
                          <input id="umAccessFilter" class="form-control" placeholder="Filter…" aria-label="Filter">
                        </div>
                        <div class="form-check form-switch mb-0">
                          <input id="umSelectedOnly" class="form-check-input" type="checkbox" role="switch">
                          <label class="form-check-label small" for="umSelectedOnly">Selected only</label>
                        </div>
                        <div class="ms-auto d-flex gap-1">
                          <button type="button" class="btn btn-sm btn-outline-secondary" id="umSelectShown">Select all shown</button>
                          <button type="button" class="btn btn-sm btn-outline-secondary" id="umClearShown">Clear shown</button>
                        </div>
                      </div>
                      <div class="um-checklist" id="umChecklist" role="group"></div>
                      <div class="form-text" id="umAccessHelp"></div>
                    </section>

                    <section class="um-section" data-section="rules">
                      <h6 class="um-group-title">Where from</h6>
                      <div class="form-check">
                        <input class="form-check-input" type="radio" name="umIpMode" id="umIpAny" value="any">
                        <label class="form-check-label" for="umIpAny">Anywhere <span class="text-muted small">any IP address</span></label>
                      </div>
                      <div class="form-check">
                        <input class="form-check-input" type="radio" name="umIpMode" id="umIpList" value="list">
                        <label class="form-check-label" for="umIpList">Only these addresses</label>
                      </div>
                      <div class="um-indent" id="umIpBox">
                        <div class="form-control um-chips" id="umIpChips">
                          <input id="umIpInput" class="um-chip-input" placeholder="IP or CIDR, then Enter" aria-label="Add an IP address or CIDR range" autocomplete="off" spellcheck="false">
                        </div>
                        <div class="um-hint" id="umIpHint"></div>
                        <div class="um-exception">
                          <div class="fw-semibold small mb-1"><i class="fas fa-clock me-1"></i>Temporary exception</div>
                          <label class="form-label small mb-1" for="securityTempAnyIP">Allow any address until</label>
                          <div class="d-flex flex-wrap gap-2 align-items-center">
                            <input id="securityTempAnyIP" type="datetime-local" class="form-control form-control-sm um-datetime">
                            <button type="button" class="btn btn-sm btn-outline-secondary" data-temp-hours="1">+1 hour</button>
                            <button type="button" class="btn btn-sm btn-outline-secondary" data-temp-hours="24">+24 hours</button>
                            <button type="button" class="btn btn-sm btn-link" id="umTempClear">Clear</button>
                          </div>
                          <div class="form-text" id="umTempNote">Lifts only the address rule, for example while travelling. Days and times still apply.</div>
                        </div>
                      </div>

                      <h6 class="um-group-title">Which days</h6>
                      <div class="um-days" id="securityWeekdays" role="group" aria-label="Allowed weekdays"></div>
                      <div class="small text-muted mt-1">Presets: <button type="button" class="btn btn-link btn-sm p-0 align-baseline" data-days="all">Every day</button> · <button type="button" class="btn btn-link btn-sm p-0 align-baseline" data-days="weekdays">Weekdays</button></div>
                      <div class="um-hint" id="umDaysHint"></div>

                      <h6 class="um-group-title">What time</h6>
                      <div class="form-check">
                        <input class="form-check-input" type="radio" name="umTimeMode" id="umTimeAll" value="all">
                        <label class="form-check-label" for="umTimeAll">All day</label>
                      </div>
                      <div class="form-check">
                        <input class="form-check-input" type="radio" name="umTimeMode" id="umTimeWindow" value="window">
                        <label class="form-check-label" for="umTimeWindow">Between</label>
                      </div>
                      <div class="um-indent" id="umTimeBox">
                        <div class="d-flex flex-wrap align-items-center gap-2">
                          <input id="securityTimeStart" type="time" class="form-control form-control-sm um-time" aria-label="Start time">
                          <span>and</span>
                          <input id="securityTimeEnd" type="time" class="form-control form-control-sm um-time" aria-label="End time">
                        </div>
                        <div class="form-text">Server time (<span class="securityTimezoneLabel"></span>). From the start up to, not including, the end. The window can't cross midnight.</div>
                        <div class="um-hint" id="umTimeHint"></div>
                      </div>
                    </section>

                  </div>
                  <aside class="um-summary" id="umSummary" aria-live="polite"></aside>
                </div>
              </form>
            </div>
            <div class="alert alert-danger um-error d-none" id="umError" role="alert"></div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary me-auto" data-bs-dismiss="modal">Cancel</button>
              <button type="button" class="btn btn-outline-secondary" id="umBack"><i class="fas fa-arrow-left me-1"></i>Back</button>
              <button type="button" class="btn btn-outline-primary" id="umNext"></button>
              <button type="button" class="btn btn-primary" id="umSave"></button>
            </div>
          </div></div>
        </div>`;
    },

    // Delegated handlers, bound once per page render
    bindUserModal() {
        const modal = $('#securityUserModal');
        modal.on('click', '.um-step', (e) => this.goTo(e.currentTarget.dataset.section));
        modal.on('click', '#umBack', () => this.step(-1));
        modal.on('click', '#umNext', () => this.step(1));
        modal.on('click', '#umSave', () => this.saveUser());

        // Account
        modal.on('input', '#securityUsername', (e) => {
            const lower = e.target.value.toLowerCase();
            if (lower !== e.target.value) {
                const pos = e.target.selectionStart;
                e.target.value = lower;
                e.target.setSelectionRange(pos, pos);
            }
            this.form.username = lower.trim();
            this.refresh();
        });
        modal.on('input', '#securityTemporaryPassword', (e) => { this.form.password = e.target.value; this.refresh(); });
        modal.on('click', '#umPwShow', () => this.togglePasswordVisible());
        modal.on('click', '#umPwNew', () => {
            this.form.password = this.generatePassword();
            $('#securityTemporaryPassword').val(this.form.password);
            this.refresh();
        });
        modal.on('click', '#umPwCopy', () => {
            if (this.form.password) Utils.copyToClipboard(this.form.password);
        });
        modal.on('change', '#securityEnabled', (e) => { this.form.enabled = e.target.checked; this.refresh(); });

        // Access
        modal.on('click', '.um-access-tab', (e) => {
            this.accessTab = e.currentTarget.dataset.kind;
            this.accessFilter = '';
            $('#umAccessFilter').val('');
            this.renderAccess();
        });
        modal.on('input', '#umAccessFilter', (e) => { this.accessFilter = e.target.value; this.renderChecklist(); });
        modal.on('change', '#umSelectedOnly', (e) => { this.selectedOnly = e.target.checked; this.renderChecklist(); });
        modal.on('change', '.um-check', (e) => {
            const set = this.form.grants[this.accessTab];
            if (e.target.checked) set.add(e.target.value); else set.delete(e.target.value);
            $(e.target).closest('.um-check-row').toggleClass('checked', e.target.checked)
                .find('.um-vnc-level').prop('disabled', !e.target.checked);
            this.renderAccessTabs();
            this.refresh();
        });
        modal.on('change', '.um-vnc-level', (e) => { this.form.vncLevels[e.target.dataset.id] = e.target.value; this.refresh(); });
        modal.on('click', '#umSelectShown', () => this.setShown(true));
        modal.on('click', '#umClearShown', () => this.setShown(false));

        // Sign-in rules
        modal.on('change', 'input[name="umIpMode"]', (e) => { this.form.ipMode = e.target.value; this.renderRules(); this.refresh(); });
        modal.on('click', '#umIpChips', (e) => { if (e.target.id === 'umIpChips') $('#umIpInput').trigger('focus'); });
        modal.on('keydown', '#umIpInput', (e) => {
            if (e.key === 'Enter' || e.key === ',' || e.key === ' ') {
                if (e.target.value.trim()) {
                    e.preventDefault();
                    this.addIPs(e.target.value);
                }
            } else if (e.key === 'Backspace' && !e.target.value && this.form.ips.length) {
                this.form.ips.pop();
                this.renderChips();
                this.refresh();
            }
        });
        modal.on('input', '#umIpInput', (e) => { this.form.ipDraft = e.target.value; this.refresh(); });
        modal.on('paste', '#umIpInput', (e) => {
            const text = (e.originalEvent.clipboardData || window.clipboardData).getData('text');
            if (/[\s,]/.test(text.trim())) {
                e.preventDefault();
                this.addIPs(e.target.value + text);
            }
        });
        modal.on('blur', '#umIpInput', (e) => { if (e.target.value.trim()) this.addIPs(e.target.value); });
        modal.on('click', '.um-chip-remove', (e) => {
            this.form.ips.splice(Number(e.currentTarget.dataset.index), 1);
            this.renderChips();
            $('#umIpInput').trigger('focus');
            this.refresh();
        });
        modal.on('input change', '#securityTempAnyIP', (e) => { this.form.tempAnyIP = e.target.value; this.refresh(); });
        modal.on('click', '[data-temp-hours]', (e) => {
            const date = new Date(Date.now() + Number(e.currentTarget.dataset.tempHours) * 3600000);
            this.form.tempAnyIP = this.toLocalInput(date);
            $('#securityTempAnyIP').val(this.form.tempAnyIP);
            this.refresh();
        });
        modal.on('click', '#umTempClear', () => { this.form.tempAnyIP = ''; $('#securityTempAnyIP').val(''); this.refresh(); });
        modal.on('click', '.um-day', (e) => {
            const day = e.currentTarget.dataset.day;
            if (this.form.days.has(day)) this.form.days.delete(day); else this.form.days.add(day);
            this.renderDays();
            this.refresh();
        });
        modal.on('click', '[data-days]', (e) => {
            const preset = e.currentTarget.dataset.days === 'weekdays' ? this.WEEKDAYS.slice(0, 5) : this.WEEKDAYS;
            this.form.days = new Set(preset);
            this.renderDays();
            this.refresh();
        });
        modal.on('change', 'input[name="umTimeMode"]', (e) => { this.form.timeMode = e.target.value; this.renderRules(); this.refresh(); });
        modal.on('input change', '#securityTimeStart', (e) => { this.form.start = e.target.value; this.refresh(); });
        modal.on('input change', '#securityTimeEnd', (e) => { this.form.end = e.target.value; this.refresh(); });

        // Closing with unsaved changes asks first (Cancel, ✕, Esc and a backdrop click all end up here)
        modal.on('hide.bs.modal', (e) => {
            if (e.target !== modal[0] || this.closeConfirmed || !this.form || !this.hasUnsavedChanges()) return;
            e.preventDefault();
            this.confirmDiscard();
        });
    },

    renderUsers() { const rows=this.users.map(user=>{const policy=`${(user.allowed_ips||['*']).join(', ')} · ${(user.allowed_weekdays||[]).map(day=>day.slice(0,3)).join(', ')}${user.allowed_time_start?` · ${user.allowed_time_start}–${user.allowed_time_end}`:' · all day'}`;return `<tr><td><strong>${this.esc(user.username)}</strong><br><small>${user.totp_enrolled?'TOTP enrolled':'TOTP enrollment required'}${user.force_password_change?' · password change required':''}</small></td><td><span class="badge ${user.enabled?'bg-success':'bg-secondary'}">${user.enabled?'Enabled':'Disabled'}</span></td><td>${(user.command_ids||[]).length}</td><td>${(user.runbook_ids||[]).length}</td><td>${(user.http_request_ids||[]).length}</td><td>${(user.terminal_hosts||[]).length}</td><td>${(user.vnc_access||[]).length}</td><td><small>${this.esc(policy)}</small></td><td class="text-end"><div class="btn-group btn-group-sm"><button class="btn btn-outline-primary" onclick="UsersSecurity.openUser('${this.esc(user.username)}')">Edit</button><button class="btn btn-outline-secondary dropdown-toggle dropdown-toggle-split" data-bs-toggle="dropdown"></button><ul class="dropdown-menu dropdown-menu-end"><li><button class="dropdown-item" onclick="UsersSecurity.toggleUser('${this.esc(user.username)}',${!user.enabled})">${user.enabled?'Disable':'Enable'}</button></li><li><button class="dropdown-item" onclick="UsersSecurity.resetPassword('${this.esc(user.username)}')">Reset password</button></li><li><button class="dropdown-item" onclick="UsersSecurity.resetTOTP('${this.esc(user.username)}')">Reset TOTP</button></li><li><hr class="dropdown-divider"></li><li><button class="dropdown-item text-danger" onclick="UsersSecurity.deleteUser('${this.esc(user.username)}')">Delete</button></li></ul></div></td></tr>`}).join('');$('#securityUsersBody').html(rows||'<tr><td colspan="9" class="text-center py-5">No regular users configured.</td></tr>'); },

    // ---- Create / Edit user dialog ----

    // What a user can be granted, in the order of the Access tabs. `field` is the API field.
    accessKinds() {
        return [
            {
                key: 'commands', field: 'command_ids', label: 'Commands', one: 'Command', many: 'Commands',
                items: this.commands.map(c => ({ id: c.cmd_id, text: c.description || '', meta: c.host_ref || '' })),
                help: 'Can run these Commands, reschedule them and read their logs.',
                none: 'No Commands are configured yet.'
            },
            {
                key: 'runbooks', field: 'runbook_ids', label: 'RunBooks', one: 'RunBook', many: 'RunBooks',
                items: this.runbooks.map(r => {
                    const steps = (r.steps || []).length;
                    return { id: r.runbook_id, text: r.description || '', meta: `${steps} step${steps === 1 ? '' : 's'}` };
                }),
                help: 'Can run these RunBooks, reschedule them and read their logs.',
                none: 'No RunBooks are configured yet.'
            },
            {
                key: 'http', field: 'http_request_ids', label: 'HTTP requests', one: 'HTTP request', many: 'HTTP requests',
                items: this.httpRequests.map(q => ({ id: q.request_id, text: q.description || '', meta: `${String(q.method || 'GET').toUpperCase()} ${q.url || ''}` })),
                help: 'Can run these HTTP requests, reschedule them and read their logs (HTTP Requests page).',
                none: 'No HTTP requests are configured yet.'
            },
            {
                key: 'hosts', field: 'terminal_hosts', label: 'Terminal hosts', one: 'Terminal host', many: 'Terminal hosts',
                items: this.hosts.map(h => ({ id: h.name, text: '', meta: `${h.username || ''}@${h.host || ''}` })),
                help: 'Can open persistent browser terminals on these SSH hosts (Terminals page). WinRM hosts are not listed.',
                none: 'No SSH hosts are configured yet.'
            },
            {
                key: 'vnc', field: 'vnc_access', label: 'Remote desktops', one: 'Remote desktop', many: 'Remote desktops', levels: true,
                items: this.vncConnections.map(c => ({ id: c.vnc_id, text: c.name && c.name !== c.vnc_id ? c.name : '', meta: c.tunnel_host_ref ? `${c.host || '127.0.0.1'}:${c.port} via ${c.tunnel_host_ref}` : `${c.host}:${c.port}` })),
                help: 'Can open these VNC desktops on the Remote Desktops page. View-only is enforced by the server: keyboard, mouse and clipboard input are dropped.',
                none: 'No remote desktop (VNC) connections are configured yet.'
            }
        ];
    },

    openUser(username = '') {
        this.editing = username || null;
        const user = this.users.find(value => value.username === username) || { enabled: true, allowed_ips: ['*'], allowed_weekdays: this.WEEKDAYS.slice(), command_ids: [], runbook_ids: [] };
        const ips = (user.allowed_ips && user.allowed_ips.length ? user.allowed_ips : ['*']);
        const start = user.allowed_time_start || user.daily_time_window?.start || '';
        const end = user.allowed_time_end || user.daily_time_window?.end || '';
        // Weekdays may be stored as full names or three-letter ones
        const storedDays = (user.allowed_weekdays && user.allowed_weekdays.length ? user.allowed_weekdays : this.WEEKDAYS).map(d => String(d).toLowerCase().slice(0, 3));
        let tempAnyIP = '';
        if (user.temporary_any_ip_until) {
            const date = new Date(user.temporary_any_ip_until);
            if (!isNaN(date)) tempAnyIP = this.toLocalInput(date);
        }
        this.form = {
            username: user.username || '',
            password: this.editing ? '' : this.generatePassword(),
            enabled: user.enabled !== false,
            grants: {
                commands: new Set(user.command_ids || []),
                runbooks: new Set(user.runbook_ids || []),
                http: new Set(user.http_request_ids || []),
                hosts: new Set(user.terminal_hosts || []),
                vnc: new Set((user.vnc_access || []).map(g => g.vnc_id))
            },
            vncLevels: Object.fromEntries((user.vnc_access || []).map(g => [g.vnc_id, g.level === 'control' ? 'control' : 'view'])),
            ipMode: ips.includes('*') ? 'any' : 'list',
            ips: ips.filter(ip => ip !== '*'),
            ipDraft: '',
            tempAnyIP,
            days: new Set(this.WEEKDAYS.filter(d => storedDays.includes(d.slice(0, 3)))),
            timeMode: start || end ? 'window' : 'all',
            start, end,
            totpEnrolled: !!user.totp_enrolled,
            forcePasswordChange: !!user.force_password_change
        };
        // Editing usually means changing what the person can use
        this.section = this.editing ? 'access' : 'account';
        this.visited = new Set([this.section]);
        this.accessTab = 'commands';
        this.accessFilter = '';
        this.selectedOnly = false;
        this.showErrors = false;
        this.closeConfirmed = false;

        $('#securityUserTitle').text(this.editing ? `Edit user · ${this.editing}` : 'Create user');
        $('#securityUsername').val(this.form.username).toggleClass('d-none', !!this.editing);
        $('#umUsernameFixed').toggleClass('d-none', !this.editing).html(`<i class="fas fa-user me-2"></i>${this.esc(this.form.username)}`);
        $('#temporaryPasswordGroup').toggleClass('d-none', !!this.editing);
        $('#securityTemporaryPassword').val(this.form.password).attr('type', 'password');
        $('#umPwShow').attr({ title: 'Show password', 'aria-label': 'Show password' }).html('<i class="fas fa-eye"></i>');
        $('#umAccountStatus').toggleClass('d-none', !this.editing).html(this.editing ? this.accountStatusHtml() : '');
        $('#securityEnabled').prop('checked', this.form.enabled);
        $('#umAccessFilter').val('');
        $('#umSelectedOnly').prop('checked', false);
        $('#umIpInput').val('');
        $('#securityTempAnyIP').val(this.form.tempAnyIP);
        $('#securityTimeStart').val(this.form.start);
        $('#securityTimeEnd').val(this.form.end);
        $('#umError').addClass('d-none').removeClass('server').text('');

        this.renderAccess();
        this.renderRules();
        this.renderDays();
        this.renderChips();
        this.showSection();
        this.snapshot = JSON.stringify(this.formData());
        bootstrap.Modal.getOrCreateInstance(document.getElementById('securityUserModal')).show();
        if (!this.editing) {
            $('#securityUserModal').one('shown.bs.modal', () => $('#securityUsername').trigger('focus'));
        }
    },

    accountStatusHtml() {
        const items = [
            this.form.totpEnrolled ? '<i class="fas fa-check-circle text-success me-1"></i>Authenticator set up' : '<i class="fas fa-hourglass-half text-warning me-1"></i>Authenticator not set up yet',
            this.form.forcePasswordChange ? '<i class="fas fa-key text-warning me-1"></i>Must change password at next sign-in' : ''
        ].filter(Boolean);
        return `<label class="form-label d-block">Sign-in setup</label><div class="small">${items.join('<br>')}</div>
            <div class="form-text">To issue a new temporary password or authenticator, close this dialog and use the arrow next to the user's <strong>Edit</strong> button.</div>`;
    },

    // Replaces a container's buttons without dropping keyboard focus: if a button in it had focus,
    // the new button with the same data attribute gets it back (otherwise focus falls to the page
    // and Esc no longer closes the dialog).
    renderKeepingFocus(selector, attr, html) {
        const container = $(selector);
        const active = document.activeElement;
        const key = active && container[0] && container[0].contains(active) ? active.getAttribute(attr) : null;
        container.html(html);
        if (key !== null) container.find(`[${attr}="${key}"]`).first().trigger('focus');
    },

    // ---- Sections ----

    goTo(section) {
        if (!section || section === this.section) return;
        this.section = section;
        this.visited.add(section);
        this.showSection();
    },

    step(delta) {
        const ids = this.SECTIONS.map(s => s.id);
        const next = ids[ids.indexOf(this.section) + delta];
        if (next) this.goTo(next);
    },

    showSection() {
        $('#securityUserModal .um-section').each((_, el) => { el.hidden = el.dataset.section !== this.section; });
        const ids = this.SECTIONS.map(s => s.id);
        const index = ids.indexOf(this.section);
        const last = index === ids.length - 1;
        $('#umBack').toggleClass('d-none', index === 0);
        const nextSection = this.SECTIONS[index + 1];
        $('#umNext').toggleClass('d-none', last).html(nextSection ? `Next: ${nextSection.label}<i class="fas fa-arrow-right ms-1"></i>` : '');
        // Creating: the final button is on the last section, and everywhere once a save was tried
        // (a problem sends you to another section). Editing: Save is always there.
        $('#umSave').toggleClass('d-none', !this.editing && !last && !this.showErrors)
            .html(this.editing ? '<i class="fas fa-check me-1"></i>Save changes' : '<i class="fas fa-check me-1"></i>Create user');
        $('#umNext').toggleClass('btn-primary', !this.editing).toggleClass('btn-outline-primary', !!this.editing);
        $('#securityUserModal .modal-body').scrollTop(0);
        this.refresh();
    },

    renderSteps(problems) {
        const html = this.SECTIONS.map((s, i) => {
            const active = s.id === this.section;
            const bad = this.showErrors && problems[s.id].length > 0;
            const done = !active && !bad && this.visited.has(s.id) && problems[s.id].length === 0;
            const mark = bad ? '!' : (done ? '<i class="fas fa-check"></i>' : String(i + 1));
            const cls = ['um-step', active ? 'active' : '', done ? 'done' : '', bad ? 'invalid' : ''].filter(Boolean).join(' ');
            const extra = s.id === 'access' ? ` <span class="um-step-count">${this.grantTotal()}</span>` : '';
            return `${i ? '<span class="um-step-line" aria-hidden="true"></span>' : ''}<button type="button" class="${cls}" data-section="${s.id}" ${active ? 'aria-current="step"' : ''}><span class="um-step-mark">${mark}</span><span class="um-step-label">${s.label}${extra}</span></button>`;
        }).join('');
        this.renderKeepingFocus('#umSteps', 'data-section', html);
    },

    grantTotal() {
        return Object.values(this.form.grants).reduce((sum, set) => sum + this.knownGrants(set).length, 0);
    },

    // ---- Access ----

    renderAccess() {
        this.renderAccessTabs();
        this.renderChecklist();
    },

    renderAccessTabs() {
        this.renderKeepingFocus('#umAccessTabs', 'data-kind', this.accessKinds().map(kind => {
            const count = this.knownGrants(this.form.grants[kind.key], kind).length;
            const active = kind.key === this.accessTab;
            return `<li class="nav-item" role="presentation"><button type="button" class="nav-link um-access-tab ${active ? 'active' : ''}" data-kind="${kind.key}" role="tab" aria-selected="${active}">${kind.label} <span class="badge rounded-pill ${count ? 'bg-primary' : 'bg-light text-muted'}">${count}</span></button></li>`;
        }).join(''));
    },

    visibleItems(kind) {
        const set = this.form.grants[kind.key];
        const query = this.accessFilter.trim().toLowerCase();
        return kind.items.filter(item => {
            if (this.selectedOnly && !set.has(item.id)) return false;
            if (!query) return true;
            return `${item.id} ${item.text} ${item.meta}`.toLowerCase().includes(query);
        });
    },

    renderChecklist() {
        const kind = this.accessKinds().find(k => k.key === this.accessTab);
        const set = this.form.grants[kind.key];
        const items = this.visibleItems(kind);
        $('#umAccessFilter').attr('placeholder', `Filter ${kind.label.toLowerCase()}…`);
        $('#umAccessHelp').text(kind.help);
        let html;
        if (!kind.items.length) {
            html = `<div class="um-empty">${kind.none}</div>`;
        } else if (!items.length) {
            html = `<div class="um-empty">${this.selectedOnly && !set.size ? `No ${kind.label.toLowerCase()} selected yet.` : 'Nothing matches the filter.'}</div>`;
        } else {
            html = items.map((item, i) => {
                const checked = set.has(item.id);
                const id = `umCheck_${kind.key}_${i}`;
                return `<label class="um-check-row ${checked ? 'checked' : ''}" for="${id}">
                    <input class="form-check-input um-check" type="checkbox" id="${id}" value="${this.escAttr(item.id)}" ${checked ? 'checked' : ''}>
                    <span class="um-check-id">${this.esc(item.id)}</span>
                    <span class="um-check-text">${this.esc(item.text)}</span>
                    <span class="um-check-meta">${this.esc(item.meta)}</span>
                    ${kind.levels ? `<select class="form-select form-select-sm um-vnc-level" data-id="${this.escAttr(item.id)}" ${checked ? '' : 'disabled'} aria-label="Access level for ${this.escAttr(item.id)}">
                        <option value="view" ${(this.form.vncLevels[item.id] || 'view') === 'view' ? 'selected' : ''}>View only</option>
                        <option value="control" ${this.form.vncLevels[item.id] === 'control' ? 'selected' : ''}>Control</option>
                    </select>` : ''}
                </label>`;
            }).join('');
        }
        $('#umChecklist').html(html);
        const disabled = !items.length;
        $('#umSelectShown, #umClearShown').prop('disabled', disabled);
        $('#umAccessFilter').prop('disabled', !kind.items.length);
    },

    setShown(selected) {
        const kind = this.accessKinds().find(k => k.key === this.accessTab);
        const set = this.form.grants[kind.key];
        this.visibleItems(kind).forEach(item => { if (selected) set.add(item.id); else set.delete(item.id); });
        this.renderAccess();
        this.refresh();
    },

    // Grants that still exist, in the configured order. Others would be refused by the server.
    knownGrants(set, kind) {
        const k = kind || this.accessKinds().find(x => this.form.grants[x.key] === set);
        return k ? k.items.map(item => item.id).filter(id => set.has(id)) : [];
    },

    // ---- Sign-in rules ----

    renderRules() {
        $('#umIpAny').prop('checked', this.form.ipMode === 'any');
        $('#umIpList').prop('checked', this.form.ipMode === 'list');
        $('#umIpBox').toggleClass('d-none', this.form.ipMode !== 'list');
        $('#umTimeAll').prop('checked', this.form.timeMode === 'all');
        $('#umTimeWindow').prop('checked', this.form.timeMode === 'window');
        $('#umTimeBox').toggleClass('d-none', this.form.timeMode !== 'window');
    },

    renderDays() {
        this.renderKeepingFocus('#securityWeekdays', 'data-day', this.WEEKDAYS.map(day => {
            const on = this.form.days.has(day);
            return `<button type="button" class="um-day ${on ? 'on' : ''}" data-day="${day}" aria-pressed="${on}" title="${day.charAt(0).toUpperCase() + day.slice(1)}">${day.charAt(0).toUpperCase() + day.slice(1, 3)}</button>`;
        }).join(''));
    },

    renderChips() {
        $('#umIpChips .um-chip').remove();
        $('#umIpInput').before(this.form.ips.map((ip, i) => `<span class="um-chip"><span>${this.esc(ip)}</span><button type="button" class="um-chip-remove" data-index="${i}" aria-label="Remove ${this.escAttr(ip)}">&times;</button></span>`).join(''));
    },

    // Adds every valid entry from the text. Invalid ones stay in the input so they can be fixed.
    addIPs(text) {
        const bad = [];
        text.split(/[\s,]+/).map(v => v.trim()).filter(Boolean).forEach((value) => {
            if (value === '*') {
                this.form.ipMode = 'any';
                this.renderRules();
            } else if (!this.validIPRule(value)) {
                bad.push(value);
            } else if (!this.form.ips.includes(value)) {
                this.form.ips.push(value);
            }
        });
        this.form.ipDraft = bad.join(' ');
        $('#umIpInput').val(this.form.ipDraft);
        this.renderChips();
        this.refresh();
    },

    // Mirrors net.ParseIP / net.ParseCIDR closely enough to catch typing mistakes; the server checks again
    validIPRule(value) {
        const parts = value.split('/');
        if (parts.length > 2) return false;
        const [ip, bits] = parts;
        const octet = '(25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)';
        let max;
        if (new RegExp(`^${octet}(\\.${octet}){3}$`).test(ip)) {
            max = 32;
        } else if (ip.includes(':') && /^[0-9a-fA-F:.]+$/.test(ip)) {
            try { new URL(`http://[${ip}]/`); } catch (_) { return false; }
            max = 128;
        } else {
            return false;
        }
        if (bits === undefined) return true;
        return /^(0|[1-9]\d{0,2})$/.test(bits) && Number(bits) <= max;
    },

    // ---- Account helpers ----

    generatePassword() {
        const alphabet = this.PASSWORD_ALPHABET;
        // Rejection sampling keeps every character equally likely
        const limit = 256 - (256 % alphabet.length);
        let out = '';
        while (out.length < this.PASSWORD_LENGTH) {
            const bytes = crypto.getRandomValues(new Uint8Array(32));
            for (const b of bytes) {
                if (b < limit && out.length < this.PASSWORD_LENGTH) out += alphabet[b % alphabet.length];
            }
        }
        return out;
    },

    togglePasswordVisible() {
        const input = $('#securityTemporaryPassword');
        const show = input.attr('type') === 'password';
        input.attr('type', show ? 'text' : 'password');
        $('#umPwShow').attr({ title: show ? 'Hide password' : 'Show password', 'aria-label': show ? 'Hide password' : 'Show password' })
            .html(`<i class="fas ${show ? 'fa-eye-slash' : 'fa-eye'}"></i>`);
    },

    toLocalInput(date) {
        return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
    },

    // ---- Validation, summary and saving ----

    // Problems per section, as { field, message }. Mirrors validateUser in auth.go.
    problems() {
        const f = this.form;
        const out = { account: [], access: [], rules: [] };
        if (!this.editing) {
            if (!f.username) out.account.push({ field: 'securityUsername', message: 'Enter a username.' });
            else if (!this.USERNAME_PATTERN.test(f.username)) out.account.push({ field: 'securityUsername', message: 'Use only lowercase letters, digits, dots and hyphens.' });
            else if (f.username === 'admin') out.account.push({ field: 'securityUsername', message: '"admin" is reserved for the built-in administrator.' });
            else if (this.users.some(u => u.username === f.username)) out.account.push({ field: 'securityUsername', message: `A user called ${f.username} already exists.` });
            if (f.password.length < this.PASSWORD_MIN) out.account.push({ field: 'securityTemporaryPassword', message: `Use at least ${this.PASSWORD_MIN} characters (now ${f.password.length}).` });
        }
        if (f.ipMode === 'list') {
            if (f.ipDraft.trim()) out.rules.push({ field: 'umIpInput', message: `"${f.ipDraft.trim()}" is not a valid IP address or CIDR range.` });
            else if (!f.ips.length) out.rules.push({ field: 'umIpInput', message: 'Add at least one address, or choose Anywhere.' });
        }
        if (!f.days.size) out.rules.push({ field: 'securityWeekdays', message: 'Pick at least one day. To block sign-in completely, disable the account instead.' });
        if (f.timeMode === 'window') {
            if (!f.start || !f.end) out.rules.push({ field: f.start ? 'securityTimeEnd' : 'securityTimeStart', message: 'Enter both a start and an end time.' });
            else if (f.start >= f.end) out.rules.push({ field: 'securityTimeEnd', message: 'The end must be later than the start. The window can\'t cross midnight.' });
        }
        return out;
    },

    setHint(id, message, invalid) {
        $(`#${id}`).text(message || '').toggleClass('invalid', !!invalid).toggleClass('valid', !invalid && !!message && message.startsWith('✓'));
    },

    // Updates everything derived from the form, without re-rendering the inputs being typed in
    refresh() {
        if (!this.form) return;
        const f = this.form;
        const problems = this.problems();
        const all = [...problems.account, ...problems.access, ...problems.rules];
        const problemFor = field => all.find(p => p.field === field);
        this.renderSteps(problems);

        // Account hints: shown as soon as something is typed, or after a save attempt
        if (!this.editing) {
            const user = problemFor('securityUsername');
            const showUser = user && (this.showErrors || f.username);
            this.setHint('umUsernameHint', showUser ? user.message : (f.username && !user ? '✓ Lowercase letters, digits, dots and hyphens. It can\'t be changed later.' : 'Lowercase letters, digits, dots and hyphens. It can\'t be changed later.'), !!showUser);
            $('#securityUsername').toggleClass('is-invalid', !!showUser);
            const pw = problemFor('securityTemporaryPassword');
            this.setHint('umPasswordHint', pw ? pw.message : `✓ ${f.password.length} characters`, !!pw && (this.showErrors || f.password.length > 0));
            $('#securityTemporaryPassword').toggleClass('is-invalid', !!pw && (this.showErrors || f.password.length > 0));
            $('#umPwCopy').prop('disabled', !f.password);
        }
        $('#umEnabledLabel').text(f.enabled ? 'Enabled. Can sign in once saved.' : 'Disabled. Can\'t sign in until enabled.');

        // Rules hints
        const ip = problemFor('umIpInput');
        const showIP = ip && (this.showErrors || f.ipDraft.trim());
        this.setHint('umIpHint', showIP ? ip.message : 'IPv4 or IPv6 address, or a CIDR range such as 10.0.0.0/8. Press Enter, comma or space to add.', !!showIP);
        $('#umIpChips').toggleClass('is-invalid', !!showIP);
        const days = problemFor('securityWeekdays');
        this.setHint('umDaysHint', days ? days.message : '', !!days);
        const time = problemFor('securityTimeStart') || problemFor('securityTimeEnd');
        const showTime = time && (this.showErrors || (f.start && f.end));
        this.setHint('umTimeHint', showTime ? time.message : '', !!showTime);
        $('#securityTimeStart, #securityTimeEnd').removeClass('is-invalid');
        if (showTime) $(`#${time.field}`).addClass('is-invalid');
        const temp = f.tempAnyIP ? new Date(f.tempAnyIP) : null;
        const expired = temp && !isNaN(temp) && temp < new Date();
        $('#umTempNote').text(expired ? 'This exception has already ended. Clear it or pick a later time.' : 'Lifts only the address rule, for example while travelling. Days and times still apply.').toggleClass('text-warning', !!expired);

        if (!all.length && !$('#umError').hasClass('server')) $('#umError').addClass('d-none');
        this.renderSummary();
    },

    renderSummary() {
        const f = this.form;
        const kinds = this.accessKinds();
        const name = f.username || (this.editing ? this.editing : 'New user');
        const access = kinds.map(kind => {
            const ids = this.knownGrants(f.grants[kind.key], kind);
            const label = `${ids.length} ${ids.length === 1 ? kind.one : kind.many}`;
            const shown = ids.slice(0, 4).map(id => `<li>${this.esc(id)}</li>`).join('');
            const more = ids.length > 4 ? `<li class="text-muted">+${ids.length - 4} more</li>` : '';
            return `<div class="um-sum-row ${ids.length ? '' : 'none'}">${label}</div>${ids.length ? `<ul class="um-sum-names">${shown}${more}</ul>` : ''}`;
        }).join('');

        let where = 'From anywhere';
        if (f.ipMode === 'list') {
            where = f.ips.length ? `From ${f.ips.slice(0, 3).map(ip => this.esc(ip)).join(', ')}${f.ips.length > 3 ? ` +${f.ips.length - 3} more` : ''}` : '<span class="text-danger">No addresses yet</span>';
            const temp = f.tempAnyIP ? new Date(f.tempAnyIP) : null;
            if (temp && !isNaN(temp) && temp > new Date()) {
                where += `<div class="text-muted small">Any address until ${this.esc(temp.toLocaleString([], { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }))}</div>`;
            }
        }
        const when = f.timeMode === 'window' && f.start && f.end ? `${this.esc(f.start)}–${this.esc(f.end)}` : 'All day';

        $('#umSummary').html(`
            <div class="um-sum-title">Summary</div>
            <div class="um-sum-user"><i class="fas fa-user-circle"></i><span>${this.esc(name)}</span></div>
            <span class="badge ${f.enabled ? 'bg-success' : 'bg-secondary'}">${f.enabled ? 'Enabled' : 'Disabled'}</span>
            <div class="um-sum-title">Can use</div>
            ${access}
            <div class="um-sum-title">Sign-in</div>
            <div class="um-sum-row">${where}</div>
            <div class="um-sum-row">${this.describeDays(f.days)}</div>
            <div class="um-sum-row">${when}</div>
            <div class="text-muted small mt-1">Server time${this.timezone ? `: ${this.esc(this.timezone)}` : ''}</div>`);
    },

    describeDays(days) {
        const idx = this.WEEKDAYS.map((d, i) => (days.has(d) ? i : -1)).filter(i => i >= 0);
        const short = i => this.WEEKDAYS[i].charAt(0).toUpperCase() + this.WEEKDAYS[i].slice(1, 3);
        if (!idx.length) return '<span class="text-danger">No days selected</span>';
        if (idx.length === 7) return 'Every day';
        const contiguous = idx.every((v, i) => i === 0 || v === idx[i - 1] + 1);
        if (contiguous && idx.length >= 3) return `${short(idx[0])}–${short(idx[idx.length - 1])}`;
        return idx.map(short).join(', ');
    },

    // The same payload the dialog has always sent
    formData() {
        const f = this.form;
        const grants = {};
        this.accessKinds().forEach(kind => { grants[kind.field] = this.knownGrants(f.grants[kind.key], kind); });
        const temp = f.ipMode === 'list' && f.tempAnyIP ? new Date(f.tempAnyIP) : null;
        const windowed = f.timeMode === 'window';
        return {
            username: f.username.trim().toLowerCase(),
            enabled: f.enabled,
            temporary_password: this.editing ? '' : f.password,
            command_ids: grants.command_ids,
            runbook_ids: grants.runbook_ids,
            http_request_ids: grants.http_request_ids,
            terminal_hosts: grants.terminal_hosts,
            vnc_access: grants.vnc_access.map(id => ({ vnc_id: id, level: f.vncLevels[id] === 'control' ? 'control' : 'view' })),
            allowed_ips: f.ipMode === 'any' ? ['*'] : f.ips.slice(),
            allowed_weekdays: this.WEEKDAYS.filter(d => f.days.has(d)),
            allowed_time_start: windowed ? f.start : '',
            allowed_time_end: windowed ? f.end : '',
            temporary_any_ip_until: temp && !isNaN(temp) ? temp.toISOString() : ''
        };
    },

    hasUnsavedChanges() {
        return JSON.stringify(this.formData()) !== this.snapshot;
    },

    async confirmDiscard() {
        const element = document.getElementById('securityUserModal');
        const instance = bootstrap.Modal.getInstance(element);
        // Bootstrap's focus trap would pull focus back from the SweetAlert buttons
        instance?._focustrap?.deactivate();
        const result = await Swal.fire({
            icon: 'warning',
            title: 'Discard your changes?',
            text: this.editing ? `Your changes to ${this.editing} haven't been saved.` : 'This user hasn\'t been created yet.',
            showCancelButton: true,
            confirmButtonColor: '#dc3545',
            cancelButtonColor: '#6c757d',
            confirmButtonText: 'Discard',
            cancelButtonText: 'Keep editing'
        });
        if (result.isConfirmed) {
            this.closeConfirmed = true;
            instance?.hide();
        } else {
            instance?._focustrap?.activate();
        }
    },

    // Shows a server or validation message inside the dialog, where it stays until fixed
    showDialogError(message, fromServer) {
        $('#umError').toggleClass('server', !!fromServer).removeClass('d-none').html(`<i class="fas fa-exclamation-circle me-2"></i>${this.esc(message)}`);
    },

    // Which section a server error belongs to, so the dialog can open it
    sectionForServerError(message) {
        const text = String(message || '').toLowerCase();
        if (/command|runbook|http request|terminal host|host /.test(text)) return 'access';
        if (/ip|cidr|weekday|time window|time start|any-ip/.test(text)) return 'rules';
        if (/username|password|exists|admin/.test(text)) return 'account';
        return null;
    },

    async saveUser() {
        this.showErrors = true;
        const problems = this.problems();
        const first = this.SECTIONS.find(s => problems[s.id].length);
        if (first) {
            const count = Object.values(problems).reduce((n, list) => n + list.length, 0);
            this.goTo(first.id);
            this.showSection();
            this.showDialogError(count === 1 ? problems[first.id][0].message : `Fix ${count} problems before saving. Sections with a red mark need attention.`, false);
            const field = problems[first.id][0].field;
            const el = document.getElementById(field);
            if (el) (el.matches('input') ? el : el.querySelector('button, input'))?.focus();
            return;
        }
        const data = this.formData();
        $('#umError').addClass('d-none').removeClass('server');
        $('#umSave').prop('disabled', true);
        try {
            if (this.editing) await API.users.update(this.editing, data);
            else await API.users.create(data);
            this.closeConfirmed = true;
            bootstrap.Modal.getInstance(document.getElementById('securityUserModal'))?.hide();
            await this.load();
            Utils.showSuccess(this.editing ? 'User updated' : 'User created');
        } catch (error) {
            const message = error.message || 'Failed to save user';
            const section = this.sectionForServerError(message);
            if (section) this.goTo(section);
            this.showDialogError(message, true);
        } finally {
            $('#umSave').prop('disabled', false);
        }
    },

    async load() { try { const [users,commands,runbooks,hosts,httpRequests,vncConnections]=await Promise.all([API.users.list(),API.config.getCommands(),API.config.getRunBooks(),API.config.getHosts(),API.httpRequests.list().catch(()=>({data:[]})),API.vnc.admin.list().catch(()=>({data:[]}))]);this.vncConnections=(vncConnections.data||[]).slice().sort((a,b)=>a.vnc_id.localeCompare(b.vnc_id));this.users=users.data?.users||[];this.timezone=users.data?.server_timezone||'';this.commands=commands.data||[];this.runbooks=runbooks.data||[];this.httpRequests=httpRequests.data||[];this.hosts=(hosts.data||[]).filter(host=>(host.connection_type||'ssh').toLowerCase()==='ssh').sort((a,b)=>a.name.localeCompare(b.name));$('#userTimezone').text(`Policies use server timezone: ${this.timezone}`);$('.securityTimezoneLabel').text(this.timezone);this.renderUsers(); } catch(error){Utils.showError(error.message||'Failed to load users');} },
    async toggleUser(username,enabled){const result=await Swal.fire({title:`${enabled?'Enable':'Disable'} ${username}?`,text:enabled?'The account can sign in again; prior sessions remain revoked.':'Access is denied immediately on every existing session.',icon:'warning',showCancelButton:true,confirmButtonText:enabled?'Enable':'Disable'});if(!result.isConfirmed)return;try{await API.users.action(username,enabled?'enable':'disable');await this.load();}catch(error){Utils.showError(error.message);}},
    async resetPassword(username){const result=await Swal.fire({title:`Reset password · ${username}`,text:'This revokes all sessions and requires complete password/TOTP onboarding.',input:'password',inputLabel:'New temporary password',inputAttributes:{minlength:12,maxlength:128,autocomplete:'new-password'},showCancelButton:true,inputValidator:value=>value.length<12?'Use at least 12 characters.':undefined});if(!result.isConfirmed)return;try{await API.users.action(username,'reset-password',{temporary_password:result.value});await this.load();Utils.showSuccess('Password reset');}catch(error){Utils.showError(error.message);}},
    async resetTOTP(username){const result=await Swal.fire({title:`Reset TOTP · ${username}?`,text:'All sessions are revoked. The user must enroll a new authenticator at next sign-in.',icon:'warning',showCancelButton:true,confirmButtonText:'Reset TOTP'});if(!result.isConfirmed)return;try{await API.users.action(username,'reset-totp');await this.load();Utils.showSuccess('TOTP reset');}catch(error){Utils.showError(error.message);}},
    async deleteUser(username){const result=await Swal.fire({title:`Delete ${username}?`,text:'This permanently removes the account and immediately invalidates its sessions.',icon:'warning',showCancelButton:true,confirmButtonColor:'#dc3545',confirmButtonText:'Delete user'});if(!result.isConfirmed)return;try{await API.users.remove(username);await this.load();Utils.showSuccess('User deleted');}catch(error){Utils.showError(error.message);}},
    async loadAudit(page=1){if(page<1)return;try{const response=await API.users.audit(page,$('#auditFilter').val()||'');const data=response.data||{};this.auditPage=data.page||1;$('#securityAuditBody').html((data.items||[]).map(entry=>`<tr><td><small>${this.esc(new Date(entry.timestamp).toLocaleString())}</small></td><td>${this.esc(entry.actor)} <small class="text-muted">${this.esc(entry.role)}</small></td><td><code>${this.esc(entry.source_ip||'—')}</code></td><td>${this.esc(entry.action)}</td><td>${this.esc(entry.target||'—')}</td><td><span class="badge ${entry.outcome==='success'?'bg-success':'bg-secondary'}">${this.esc(entry.outcome)}</span></td></tr>`).join('')||'<tr><td colspan="6" class="text-center py-4">No matching audit entries.</td></tr>');const pages=Math.max(1,Math.ceil((data.total||0)/(data.page_size||50)));$('#auditPageLabel').text(`Page ${this.auditPage} of ${pages} · ${data.total||0} entries`);$('#auditPrev').prop('disabled',this.auditPage<=1);$('#auditNext').prop('disabled',this.auditPage>=pages);}catch(error){Utils.showError(error.message||'Failed to load audit');}}
};
window.UsersSecurity = UsersSecurity;
