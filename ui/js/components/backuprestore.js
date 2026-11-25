// Backup & Restore Component
const BackupRestore = {
    inspectedFile: null,
    inspectedPassword: '',
    lastPreview: null,

    async init() {
        this.render();
        this.bindEvents();
    },

    render() {
        const html = `
            <div class="page-header">
                <h2><i class="fas fa-archive"></i> Backup & Restore</h2>
            </div>

            <ul class="nav nav-tabs mb-3" id="backupRestoreTabs" role="tablist">
                <li class="nav-item" role="presentation">
                    <button class="nav-link active" id="backupExportTab" data-bs-toggle="tab" data-bs-target="#backupExportPane" type="button" role="tab" aria-controls="backupExportPane" aria-selected="true">
                        <i class="fas fa-download"></i> Export Backup
                    </button>
                </li>
                <li class="nav-item" role="presentation">
                    <button class="nav-link" id="backupRestoreTab" data-bs-toggle="tab" data-bs-target="#backupRestorePane" type="button" role="tab" aria-controls="backupRestorePane" aria-selected="false">
                        <i class="fas fa-upload"></i> Restore Backup
                    </button>
                </li>
            </ul>

            <div class="tab-content" id="backupRestoreTabContent">
                <div class="tab-pane fade show active" id="backupExportPane" role="tabpanel" aria-labelledby="backupExportTab" tabindex="0">
                    <div class="card mb-4">
                        <div class="card-header">
                            <i class="fas fa-download"></i> Export Backup
                        </div>
                        <div class="card-body">
                            <div class="mb-3">
                                <label class="form-label" for="backupExportPassword">Backup Password</label>
                                <input type="password" class="form-control" id="backupExportPassword" autocomplete="new-password" minlength="12">
                                <div class="form-text">Minimum 12 characters. The password is required to restore the backup.</div>
                            </div>
                            <div class="mb-3">
                                <label class="form-label" for="backupExportConfirm">Confirm Password</label>
                                <input type="password" class="form-control" id="backupExportConfirm" autocomplete="new-password" minlength="12">
                            </div>
                            <button class="btn btn-success" id="backupExportButton" type="button">
                                <i class="fas fa-file-download"></i> Download Backup
                            </button>
                        </div>
                    </div>
                </div>

                <div class="tab-pane fade" id="backupRestorePane" role="tabpanel" aria-labelledby="backupRestoreTab" tabindex="0">
                    <div class="card mb-4">
                        <div class="card-header">
                            <i class="fas fa-upload"></i> Restore Backup
                        </div>
                        <div class="card-body">
                            <div class="row">
                                <div class="col-md-7">
                                    <div class="mb-3">
                                        <label class="form-label" for="backupRestoreFile">Backup File</label>
                                        <input type="file" class="form-control" id="backupRestoreFile" accept=".cgbak,application/octet-stream">
                                    </div>
                                </div>
                                <div class="col-md-5">
                                    <div class="mb-3">
                                        <label class="form-label" for="backupRestorePassword">Password</label>
                                        <input type="password" class="form-control" id="backupRestorePassword" autocomplete="current-password" minlength="12">
                                    </div>
                                </div>
                            </div>
                            <button class="btn btn-primary" id="backupInspectButton" type="button">
                                <i class="fas fa-search"></i> Inspect Backup
                            </button>
                        </div>
                    </div>

                    <div id="backupPreviewContainer" class="d-none"></div>
                </div>
            </div>
        `;
        $('#mainContent').html(html);
    },

    bindEvents() {
        $('#backupExportButton').on('click', () => this.exportBackup());
        $('#backupInspectButton').on('click', () => this.inspectBackup());
        $('#backupRestoreFile, #backupRestorePassword').on('change input', () => {
            this.inspectedFile = null;
            this.inspectedPassword = '';
            this.lastPreview = null;
            $('#backupPreviewContainer').addClass('d-none').empty();
        });
    },

    async exportBackup() {
        const password = $('#backupExportPassword').val();
        const confirm = $('#backupExportConfirm').val();
        if (!this.validPassword(password)) {
            Utils.showError('Backup password must be at least 12 characters');
            return;
        }
        if (password !== confirm) {
            Utils.showError('Backup passwords do not match');
            return;
        }
        const ok = await Utils.confirm(
            'Download encrypted backup?',
            'The file will include config.json, email.tpl when present, and all managed PEM files.',
            'Download Backup'
        );
        if (!ok) {
            return;
        }
        this.setBusy('#backupExportButton', true);
        try {
            await API.backups.export(password);
            Utils.showSuccess('Backup download started');
        } catch (error) {
            Utils.showError(error.message || 'Failed to export backup');
        } finally {
            this.setBusy('#backupExportButton', false);
        }
    },

    async inspectBackup() {
        const file = $('#backupRestoreFile')[0].files[0];
        const password = $('#backupRestorePassword').val();
        if (!file) {
            Utils.showError('Select a backup file');
            return;
        }
        if (!this.validPassword(password)) {
            Utils.showError('Backup password must be at least 12 characters');
            return;
        }
        this.setBusy('#backupInspectButton', true);
        try {
            const response = await API.backups.inspect(file, password);
            if (!response.success) {
                throw new Error(response.error || 'Failed to inspect backup');
            }
            this.inspectedFile = file;
            this.inspectedPassword = password;
            this.lastPreview = response.data;
            this.renderPreview(response.data);
            Utils.showSuccess('Backup inspected');
        } catch (error) {
            $('#backupPreviewContainer').addClass('d-none').empty();
            Utils.showError(error.message || 'Failed to inspect backup');
        } finally {
            this.setBusy('#backupInspectButton', false);
        }
    },

    renderPreview(preview) {
        const counts = preview.counts || {};
        const compatible = !!preview.compatible;
        const restartBadge = preview.requires_restart
            ? '<span class="badge bg-warning text-dark">Restart Required</span>'
            : '<span class="badge bg-success">Hot Reload</span>';
        const html = `
            <div class="card">
                <div class="card-header d-flex justify-content-between align-items-center">
                    <span><i class="fas fa-clipboard-check"></i> Backup Preview</span>
                    ${compatible ? '<span class="badge bg-success">Compatible</span>' : '<span class="badge bg-danger">Newer Version</span>'}
                </div>
                <div class="card-body">
                    <div class="row mb-3">
                        <div class="col-sm-6">
                            <div class="small text-muted">Backup Version</div>
                            <div>${this.escape(preview.backup_app_version || '')}</div>
                        </div>
                        <div class="col-sm-6">
                            <div class="small text-muted">Current Version</div>
                            <div>${this.escape(preview.current_app_version || '')}</div>
                        </div>
                        <div class="col-sm-6 mt-3">
                            <div class="small text-muted">Created</div>
                            <div>${this.escape(preview.created_at || '')}</div>
                        </div>
                        <div class="col-sm-6 mt-3">
                            <div class="small text-muted">Restore Mode</div>
                            <div>Exact snapshot ${restartBadge}</div>
                        </div>
                    </div>

                    <div class="table-responsive mb-3">
                        <table class="table table-sm table-bordered mb-0">
                            <thead>
                                <tr>
                                    <th>Hosts</th>
                                    <th>Commands</th>
                                    <th>RunBooks</th>
                                    <th>PEM Files</th>
                                    <th>email.tpl</th>
                                </tr>
                            </thead>
                            <tbody>
                                <tr>
                                    <td>${counts.hosts || 0}</td>
                                    <td>${counts.commands || 0}</td>
                                    <td>${counts.runbooks || 0}</td>
                                    <td>${counts.pem_files || 0}</td>
                                    <td>${preview.includes_email_tpl ? 'Included' : 'Not included'}</td>
                                </tr>
                            </tbody>
                        </table>
                    </div>

                    ${this.renderPEMChanges(preview)}

                    <div class="alert alert-danger mt-3 mb-3">
                        <i class="fas fa-exclamation-triangle"></i>
                        Restore replaces config.json and email.tpl, restores PEM files from the backup, and deletes managed PEM files that are not in the backup.
                    </div>

                    <div class="mb-3">
                        <label class="form-label" for="backupRestoreConfirm">Type RESTORE to confirm</label>
                        <input type="text" class="form-control" id="backupRestoreConfirm" autocomplete="off">
                    </div>
                    <button class="btn btn-danger" id="backupRestoreButton" type="button" ${compatible ? '' : 'disabled'}>
                        <i class="fas fa-undo"></i> Restore Backup
                    </button>
                </div>
            </div>
        `;
        $('#backupPreviewContainer').removeClass('d-none').html(html);
        $('#backupRestoreButton').on('click', () => this.restoreBackup());
    },

    renderPEMChanges(preview) {
        const rows = [
            ['Added', preview.pem_added || [], 'success'],
            ['Updated', preview.pem_updated || [], 'warning'],
            ['Deleted', preview.pem_deleted || [], 'danger'],
            ['Unchanged', preview.pem_unchanged || [], 'secondary']
        ].map(([label, values, color]) => `
            <tr>
                <th class="text-nowrap"><span class="badge bg-${color}">${label}</span></th>
                <td>${values.length ? values.map(name => `<code>${this.escape(name)}</code>`).join(', ') : '<span class="text-muted">None</span>'}</td>
            </tr>
        `).join('');
        return `
            <div class="table-responsive">
                <table class="table table-sm table-bordered mb-0">
                    <tbody>${rows}</tbody>
                </table>
            </div>
        `;
    },

    async restoreBackup() {
        const confirm = $('#backupRestoreConfirm').val().trim();
        if (confirm !== 'RESTORE') {
            Utils.showError('Type RESTORE to confirm');
            return;
        }
        if (!this.inspectedFile || !this.inspectedPassword || !this.lastPreview) {
            Utils.showError('Inspect the backup before restoring');
            return;
        }
        const ok = await Utils.confirm(
            'Restore this backup?',
            'Current configuration and managed PEM files will be replaced. Active Commands and RunBooks must be idle.',
            'Restore Backup'
        );
        if (!ok) {
            return;
        }
        this.setBusy('#backupRestoreButton', true);
        try {
            const response = await API.backups.restore(this.inspectedFile, this.inspectedPassword, 'RESTORE');
            if (!response.success) {
                throw new Error(response.error || 'Failed to restore backup');
            }
            Utils.showSuccess('Backup restored. Sign in again.');
            setTimeout(() => {
                API.removeToken();
                window.location.href = '/login.html';
            }, 900);
        } catch (error) {
            Utils.showError(error.message || 'Failed to restore backup');
            this.setBusy('#backupRestoreButton', false);
        }
    },

    validPassword(password) {
        return typeof password === 'string' && password.length >= 12;
    },

    setBusy(selector, busy) {
        const button = $(selector);
        button.prop('disabled', busy);
        if (busy) {
            button.data('original-html', button.html());
            button.html('<span class="spinner-border spinner-border-sm"></span> Working...');
        } else if (button.data('original-html')) {
            button.html(button.data('original-html'));
        }
    },

    escape(value) {
        return Utils.escapeHtml(String(value || ''));
    },

    cleanup() {
        this.inspectedFile = null;
        this.inspectedPassword = '';
        this.lastPreview = null;
    }
};

window.BackupRestore = BackupRestore;
