// App Settings Component
const AppSettings = {
    currentConfig: {},
    originalValues: {},

    // Fields that require a full restart to take effect
    RESTART_FIELDS: ['port', 'bind_address'],

    // Must match terminalMaxSessionsPerUserLimit in terminals.go
    MAX_TERMINAL_SESSIONS: 200,
    // Must match terminalImageRetentionMaxDays in terminals_images.go
    MAX_IMAGE_RETENTION_DAYS: 365,
    // Must match vncMaxSessionsPerUserLimit / vncMaxIdleMinutes in vnc.go
    MAX_VNC_SESSIONS: 50,
    MAX_VNC_IDLE_MINUTES: 10080,

    // Idle minutes: blank = default (60), 0 = never
    vncIdleValue() {
        const raw = $('#appVncIdleMinutes').val();
        return raw === '' ? null : Number(raw);
    },

    // Blank means the default, but a typed 0 is a mistake.
    validWholeNumber(selector, max) {
        const input = $(selector)[0];
        if (input.validity.badInput) return false;
        if (input.value === '') return true;
        const value = Number(input.value);
        return Number.isInteger(value) && value >= 1 && value <= max;
    },

    getS3FormConfig() {
        const s3Config = {
            access_key_id: $('#appS3AccessKeyID').val().trim(),
            region: $('#appS3Region').val().trim(),
            bucket: $('#appS3Bucket').val().trim(),
            prefix: $('#appS3Prefix').val().trim()
        };
        const secret = Utils.getMaskedSecretValue('#appS3SecretAccessKey').trim();
        if (secret) {
            s3Config.secret_access_key = secret;
        }

        const hasValues = Object.values(s3Config).some(value => value && value.trim && value.trim() !== '');
        return hasValues ? s3Config : null;
    },

    comparableS3Config(s3Config, existingHasSecret = false) {
        if (!s3Config) {
            return null;
        }

        return {
            access_key_id: s3Config.access_key_id || '',
            region: s3Config.region || '',
            bucket: s3Config.bucket || '',
            prefix: s3Config.prefix || '',
            has_secret_access_key: !!s3Config.secret_access_key || existingHasSecret,
            secret_updated: !!s3Config.secret_access_key
        };
    },

    valuesEqual(left, right) {
        return JSON.stringify(left ?? null) === JSON.stringify(right ?? null);
    },

    getFormValues() {
        return {
            port: parseInt($('#appPort').val()) || 0,
            bind_address: $('#appBindAddress').val().trim(),
            access_url: $('#appAccessURL').val().trim(),
            cron_enabled: $('#appCronEnabled').val(),
            dev_mode: $('#appDevMode').is(':checked'),
            log_retention_days: parseInt($('#appLogRetentionDays').val()) || 0,
            max_terminal_sessions: Number($('#appMaxTerminalSessions').val()) || 0,
            image_retention_days: Number($('#appImageRetentionDays').val()) || 0,
            vnc_enabled: $('#appVncEnabled').is(':checked'),
            vnc_idle_minutes: this.vncIdleValue(),
            vnc_max_sessions: Number($('#appVncMaxSessions').val()) || 0,
            s3: this.getS3FormConfig()
        };
    },

    getComparableFormValues(newValues = this.getFormValues()) {
        const originalHadS3Secret = this.originalValues.s3?.has_secret_access_key === true;
        return {
            ...newValues,
            s3: this.comparableS3Config(
                newValues.s3,
                originalHadS3Secret && !newValues.s3?.secret_access_key
            )
        };
    },

    getChangedFields(comparableValues = this.getComparableFormValues()) {
        return Object.entries(comparableValues)
            .filter(([key, value]) => !this.valuesEqual(value, this.originalValues[key]))
            .map(([key]) => key);
    },

    hasUnsavedChanges() {
        return this.getChangedFields().length > 0;
    },

    async confirmNavigation() {
        if (!this.hasUnsavedChanges()) {
            return true;
        }

        const result = await Swal.fire({
            icon: 'warning',
            title: 'Save your changes?',
            text: 'You have unsaved changes. Would you like to save them before leaving this page?',
            showCancelButton: true,
            showDenyButton: true,
            confirmButtonColor: '#667eea',
            denyButtonColor: '#dc3545',
            cancelButtonColor: '#6c757d',
            confirmButtonText: 'Save Changes',
            denyButtonText: 'Discard Changes',
            cancelButtonText: 'Stay on This Page'
        });

        if (result.isConfirmed) {
            return this.saveConfig();
        }

        return result.isDenied === true;
    },

    // Initialize
    async init() {
        try {
            this.render();
            await this.loadConfig();
        } catch (error) {
            console.error('AppSettings init error:', error);
            Utils.showError('Failed to load application settings');
        }
    },

    // Render layout
    render() {
        const html = `
            <div class="page-header">
                <h2><i class="fas fa-sliders-h"></i> Application Settings</h2>
                <button class="btn btn-success" onclick="AppSettings.saveConfig()">
                    <i class="fas fa-save"></i> Apply Changes
                </button>
            </div>

            <div class="alert alert-info">
                <i class="fas fa-info-circle"></i>
                <strong>Application Settings</strong> control core server behavior.
                Some settings (Port, Bind Address) require a full application restart to take effect.
            </div>

            <div class="row">
                <div class="col-lg-8">
                    <div class="card mb-4">
                        <div class="card-header">
                            <i class="fas fa-network-wired"></i> Network Settings
                        </div>
                        <div class="card-body">
                            <div class="row">
                                <div class="col-md-4">
                                    <div class="mb-3">
                                        <label class="form-label">Port <span class="badge bg-warning text-dark">Restart Required</span></label>
                                        <input type="number" class="form-control" id="appPort" min="1" max="65535" placeholder="8080">
                                        <div class="form-text">Server listening port</div>
                                    </div>
                                </div>
                                <div class="col-md-8">
                                    <div class="mb-3">
                                        <label class="form-label">Bind Address <span class="badge bg-warning text-dark">Restart Required</span></label>
                                        <input type="text" class="form-control" id="appBindAddress" placeholder="0.0.0.0">
                                        <div class="form-text">IP address to bind to. Use 0.0.0.0 for all interfaces, 127.0.0.1 for localhost only</div>
                                    </div>
                                </div>
                            </div>
                            <div class="mb-3">
                                <label class="form-label">Access URL</label>
                                <input type="text" class="form-control" id="appAccessURL" placeholder="https://commandguard.example.com">
                                <div class="form-text">Public URL used in email notifications and log links</div>
                            </div>
                        </div>
                    </div>

                    <div class="card mb-4">
                        <div class="card-header">
                            <i class="fas fa-cogs"></i> General Settings
                        </div>
                        <div class="card-body">
                            <div class="row">
                                <div class="col-md-6">
                                    <div class="mb-3">
                                        <label class="form-label">Cron Scheduler</label>
                                        <select class="form-select" id="appCronEnabled">
                                            <option value="Y">Enabled</option>
                                            <option value="N">Disabled</option>
                                        </select>
                                        <div class="form-text">Enable or disable automatic scheduled command execution</div>
                                    </div>
                                </div>
                                <div class="col-md-6">
                                    <div class="mb-3">
                                        <label class="form-label">Local Log Retention Days</label>
                                        <input type="number" class="form-control" id="appLogRetentionDays" min="1" max="365" placeholder="3">
                                        <div class="form-text">Automatically delete local log files older than this many days (default: 3)</div>
                                    </div>
                                </div>
                            </div>
                            <div class="row">
                                <div class="col-md-6">
                                    <div class="mb-3">
                                        <label class="form-label" for="appMaxTerminalSessions">Max Terminal Sessions Per User</label>
                                        <input type="number" class="form-control" id="appMaxTerminalSessions" min="1" max="${this.MAX_TERMINAL_SESSIONS}" step="1" placeholder="10">
                                        <div class="form-text">Running sessions each account may have at once, 1 to ${this.MAX_TERMINAL_SESSIONS} (blank = 10). Lowering it does not end running sessions.</div>
                                    </div>
                                </div>
                                <div class="col-md-6">
                                    <div class="mb-3">
                                        <label class="form-label" for="appImageRetentionDays">Uploaded File Retention Days</label>
                                        <input type="number" class="form-control" id="appImageRetentionDays" min="1" max="${this.MAX_IMAGE_RETENTION_DAYS}" step="1" placeholder="7">
                                        <div class="form-text">Images and files copied into terminals stay in /tmp on the host this long, 1 to ${this.MAX_IMAGE_RETENTION_DAYS} (blank = 7). Older ones are removed at the next upload on that host.</div>
                                    </div>
                                </div>
                            </div>
                            <div class="row">
                                <div class="col-md-4">
                                    <div class="mb-3">
                                        <label class="form-label d-block" for="appVncEnabled">Remote Desktops (VNC)</label>
                                        <div class="form-check form-switch">
                                            <input class="form-check-input" type="checkbox" id="appVncEnabled">
                                            <label class="form-check-label" for="appVncEnabled">Enabled</label>
                                        </div>
                                        <div class="form-text">Turning it off disconnects every open remote desktop within about 15 seconds.</div>
                                    </div>
                                </div>
                                <div class="col-md-4">
                                    <div class="mb-3">
                                        <label class="form-label" for="appVncIdleMinutes">Remote Desktop Idle Disconnect (minutes)</label>
                                        <input type="number" class="form-control" id="appVncIdleMinutes" min="0" max="${this.MAX_VNC_IDLE_MINUTES}" step="1" placeholder="60">
                                        <div class="form-text">Disconnect a viewer after this long without keyboard or mouse use (blank = 60, 0 = never). The desktop keeps running.</div>
                                    </div>
                                </div>
                                <div class="col-md-4">
                                    <div class="mb-3">
                                        <label class="form-label" for="appVncMaxSessions">Max Remote Desktops Per User</label>
                                        <input type="number" class="form-control" id="appVncMaxSessions" min="1" max="${this.MAX_VNC_SESSIONS}" step="1" placeholder="5">
                                        <div class="form-text">Desktops each account may have open at once, 1 to ${this.MAX_VNC_SESSIONS} (blank = 5).</div>
                                    </div>
                                </div>
                            </div>
                            <div class="mb-3">
                                <div class="form-check form-switch">
                                    <input class="form-check-input" type="checkbox" id="appDevMode">
                                    <label class="form-check-label" for="appDevMode">
                                        Development Mode
                                    </label>
                                </div>
                                <div class="form-text text-danger">
                                    <i class="fas fa-exclamation-triangle"></i>
                                    Enables authentication bypasses for development. Never enable in production.
                                </div>
                            </div>
                        </div>
                    </div>

                    <div class="card mb-4">
                        <div class="card-header d-flex justify-content-between align-items-center">
                            <span><i class="fas fa-cloud-upload-alt"></i> S3 Log Upload Settings</span>
                            <button class="btn btn-sm btn-outline-light" type="button" onclick="AppSettings.testS3Settings()">
                                <i class="fas fa-plug"></i> Test Settings
                            </button>
                        </div>
                        <div class="card-body">
                            <div class="row">
                                <div class="col-md-6">
                                    <div class="mb-3">
                                        <label class="form-label">Access Key ID</label>
                                        <input type="text" class="form-control" id="appS3AccessKeyID" autocomplete="off">
                                    </div>
                                </div>
                                <div class="col-md-6">
                                    <div class="mb-3">
                                        <label class="form-label">Secret Access Key</label>
                                        <div class="input-group">
                                            <input type="password" class="form-control" id="appS3SecretAccessKey" autocomplete="new-password">
                                            <button class="btn btn-outline-secondary" type="button" id="appS3SecretAccessKeyReveal" title="Reveal secret" aria-label="Reveal secret" aria-pressed="false">
                                                <i class="fas fa-eye"></i>
                                            </button>
                                        </div>
                                        <div class="form-text">Saved access keys are masked. Start typing to replace the existing secret.</div>
                                    </div>
                                </div>
                            </div>
                            <div class="row">
                                <div class="col-md-6">
                                    <div class="mb-3">
                                        <label class="form-label">Region</label>
                                        <input type="text" class="form-control" id="appS3Region" placeholder="us-east-1">
                                    </div>
                                </div>
                                <div class="col-md-6">
                                    <div class="mb-3">
                                        <label class="form-label">Bucket</label>
                                        <input type="text" class="form-control" id="appS3Bucket" placeholder="commandguard-logs">
                                    </div>
                                </div>
                            </div>
                            <div class="mb-3">
                                <label class="form-label">Key Prefix</label>
                                <input type="text" class="form-control" id="appS3Prefix" placeholder="logs">
                                <div class="form-text">Optional folder-style prefix. S3 deletion/lifecycle is managed outside this application.</div>
                            </div>
                        </div>
                    </div>
                </div>

                <div class="col-lg-4">
                    <div class="card">
                        <div class="card-header">
                            <i class="fas fa-info-circle"></i> Info
                        </div>
                        <div class="card-body">
                            <p class="mb-2"><strong>Hot-Reloaded Settings</strong></p>
                            <ul class="small text-muted mb-3">
                                <li>Access URL</li>
                                <li>Cron Scheduler</li>
                                <li>Local Log Retention Days</li>
                                <li>Max Terminal Sessions Per User</li>
                                <li>Uploaded File Retention Days</li>
                                <li>Remote Desktops settings</li>
                                <li>Development Mode</li>
                                <li>S3 Log Upload Settings</li>
                            </ul>
                            <p class="small text-muted mb-3">Local retention only deletes files under <code>logs/</code>. S3 bucket lifecycle and deletion are outside this application.</p>
                            <p class="mb-2"><strong>Restart Required</strong></p>
                            <ul class="small text-muted mb-0">
                                <li>Port</li>
                                <li>Bind Address</li>
                            </ul>
                        </div>
                    </div>
                </div>
            </div>
        `;

        $('#mainContent').html(html);
    },

    // Load config from API
    async loadConfig() {
        try {
            const response = await API.config.get();
            if (response.success && response.data) {
                this.currentConfig = response.data;
                this.populateForm();
            } else {
                Utils.showError('Failed to load configuration');
            }
        } catch (error) {
            console.error('Error loading config:', error);
            Utils.showError('Failed to load configuration');
        }
    },

    // Populate form fields
    populateForm() {
        const cfg = this.currentConfig;
        $('#appPort').val(cfg.port || '');
        $('#appBindAddress').val(cfg.bind_address || '');
        $('#appAccessURL').val(cfg.access_url || '');
        $('#appCronEnabled').val(cfg.cron_enabled || 'Y');
        $('#appDevMode').prop('checked', cfg.dev_mode === true);
        $('#appLogRetentionDays').val(cfg.log_retention_days || '');
        $('#appMaxTerminalSessions').val(cfg.terminals?.max_sessions_per_user || '');
        $('#appImageRetentionDays').val(cfg.terminals?.image_retention_days || '');
        $('#appVncEnabled').prop('checked', cfg.vnc?.enabled !== false);
        $('#appVncIdleMinutes').val(cfg.vnc?.idle_disconnect_minutes ?? '');
        $('#appVncMaxSessions').val(cfg.vnc?.max_sessions_per_user || '');
        const s3 = cfg.s3 || {};
        $('#appS3AccessKeyID').val(s3.access_key_id || '');
        Utils.setupMaskedSecretField('#appS3SecretAccessKey', '#appS3SecretAccessKeyReveal', s3.secret_access_key_length || 0);
        $('#appS3Region').val(s3.region || '');
        $('#appS3Bucket').val(s3.bucket || '');
        $('#appS3Prefix').val(s3.prefix || '');

        // Store original values for comparison on save
        this.originalValues = {
            port: cfg.port,
            bind_address: cfg.bind_address || '',
            access_url: cfg.access_url || '',
            cron_enabled: cfg.cron_enabled || 'Y',
            dev_mode: cfg.dev_mode === true,
            log_retention_days: cfg.log_retention_days || 0,
            max_terminal_sessions: cfg.terminals?.max_sessions_per_user || 0,
            image_retention_days: cfg.terminals?.image_retention_days || 0,
            vnc_enabled: cfg.vnc?.enabled !== false,
            vnc_idle_minutes: cfg.vnc?.idle_disconnect_minutes ?? null,
            vnc_max_sessions: cfg.vnc?.max_sessions_per_user || 0,
            s3: this.comparableS3Config(s3.access_key_id || s3.region || s3.bucket || s3.prefix || s3.has_secret_access_key ? s3 : null, s3.has_secret_access_key === true)
        };
    },

    // Save config
    async saveConfig() {
        try {
            // Collect new values from form
            const newValues = this.getFormValues();
            const originalHadS3Secret = this.originalValues.s3?.has_secret_access_key === true;
            const comparableNewValues = this.getComparableFormValues(newValues);

            // Validate port
            if (newValues.port < 1 || newValues.port > 65535) {
                Utils.showError('Port must be between 1 and 65535');
                return false;
            }

            if (!this.validWholeNumber('#appMaxTerminalSessions', this.MAX_TERMINAL_SESSIONS)) {
                Utils.showError(`Max Terminal Sessions Per User must be a whole number from 1 to ${this.MAX_TERMINAL_SESSIONS}, or blank for the default of 10`);
                return false;
            }
            if (!this.validWholeNumber('#appImageRetentionDays', this.MAX_IMAGE_RETENTION_DAYS)) {
                Utils.showError(`Uploaded File Retention Days must be a whole number from 1 to ${this.MAX_IMAGE_RETENTION_DAYS}, or blank for the default of 7`);
                return false;
            }

            if (!this.validWholeNumber('#appVncMaxSessions', this.MAX_VNC_SESSIONS)) {
                Utils.showError(`Max Remote Desktops Per User must be a whole number from 1 to ${this.MAX_VNC_SESSIONS}, or blank for the default of 5`);
                return false;
            }
            const idleInput = $('#appVncIdleMinutes')[0];
            const idle = this.vncIdleValue();
            if (idleInput.validity.badInput || (idle !== null && (!Number.isInteger(idle) || idle < 0 || idle > this.MAX_VNC_IDLE_MINUTES))) {
                Utils.showError(`Remote Desktop Idle Disconnect must be a whole number of minutes from 0 (never) to ${this.MAX_VNC_IDLE_MINUTES}, or blank for the default of 60`);
                return false;
            }

            // Find which fields changed
            const changed = this.getChangedFields(comparableNewValues);
            const restartNeeded = changed.filter(key => this.RESTART_FIELDS.includes(key));

            if (changed.length === 0) {
                Utils.showInfo('No changes detected', 'App Settings');
                return true;
            }

            Utils.showLoading();

            // Re-fetch full config to overlay changes onto
            const configResponse = await API.config.get();
            if (!configResponse.success || !configResponse.data) {
                Utils.showError('Failed to read current configuration');
                Utils.hideLoading();
                return false;
            }

            const fullConfig = configResponse.data;

            // Apply new values
            fullConfig.port = newValues.port;
            fullConfig.bind_address = newValues.bind_address;
            fullConfig.access_url = newValues.access_url;
            fullConfig.cron_enabled = newValues.cron_enabled;
            fullConfig.dev_mode = newValues.dev_mode;
            fullConfig.log_retention_days = newValues.log_retention_days;
            // Only these keys: the block also holds other settings and the shared Agent Session presets.
            const terminals = fullConfig.terminals || {};
            const setOrClear = (key, value) => {
                if (value) terminals[key] = value;
                else delete terminals[key];
            };
            setOrClear('max_sessions_per_user', newValues.max_terminal_sessions);
            setOrClear('image_retention_days', newValues.image_retention_days);
            if (Object.keys(terminals).length) {
                fullConfig.terminals = terminals;
            } else {
                delete fullConfig.terminals;
            }
            // Remote Desktops block: only keys that differ from the defaults are stored
            const vnc = {};
            if (!newValues.vnc_enabled) vnc.enabled = false;
            if (newValues.vnc_idle_minutes !== null) vnc.idle_disconnect_minutes = newValues.vnc_idle_minutes;
            if (newValues.vnc_max_sessions) vnc.max_sessions_per_user = newValues.vnc_max_sessions;
            if (Object.keys(vnc).length) {
                fullConfig.vnc = vnc;
            } else {
                delete fullConfig.vnc;
            }
            if (newValues.s3) {
                fullConfig.s3 = newValues.s3;
            } else {
                delete fullConfig.s3;
            }

            const response = await API.config.update(fullConfig);
            Utils.hideLoading();

            if (response.success) {
                // Update stored originals
                const savedComparableValues = { ...comparableNewValues };
                if (newValues.s3) {
                    const secretLength = newValues.s3.secret_access_key
                        ? Utils.characterCount(newValues.s3.secret_access_key)
                        : (this.currentConfig.s3?.secret_access_key_length || 0);
                    savedComparableValues.s3 = this.comparableS3Config(
                        {
                            ...newValues.s3,
                            secret_access_key: ''
                        },
                        !!newValues.s3.secret_access_key || originalHadS3Secret
                    );
                    this.originalValues = savedComparableValues;
                    this.currentConfig.s3 = {
                        access_key_id: newValues.s3.access_key_id || '',
                        region: newValues.s3.region || '',
                        bucket: newValues.s3.bucket || '',
                        prefix: newValues.s3.prefix || '',
                        has_secret_access_key: !!newValues.s3.secret_access_key || originalHadS3Secret,
                        secret_access_key_length: secretLength
                    };
                    Utils.setupMaskedSecretField('#appS3SecretAccessKey', '#appS3SecretAccessKeyReveal', secretLength);
                } else {
                    this.originalValues = savedComparableValues;
                    delete this.currentConfig.s3;
                    Utils.setupMaskedSecretField('#appS3SecretAccessKey', '#appS3SecretAccessKeyReveal', 0);
                }

                if (restartNeeded.length > 0) {
                    const fieldNames = restartNeeded.map(f => {
                        const labels = {
                            'port': 'Port',
                            'bind_address': 'Bind Address'
                        };
                        return labels[f] || f;
                    }).join(', ');

                    await Swal.fire({
                        icon: 'warning',
                        title: 'Restart Required',
                        html: `<strong>${fieldNames}</strong> was updated.<br><br>This change will only take effect after a <strong>full restart</strong> of the application.`,
                        confirmButtonText: 'OK'
                    });
                } else {
                    Utils.showSuccess('Settings saved successfully');
                }
                return true;
            } else {
                Utils.showError(response.error || 'Failed to save settings');
                return false;
            }
        } catch (error) {
            console.error('Error saving config:', error);
            Utils.showError('Failed to save settings');
            Utils.hideLoading();
            return false;
        }
    },

    async testS3Settings() {
        const s3Config = this.getS3FormConfig();
        if (!s3Config) {
            Utils.showError('Enter S3 settings before testing');
            return;
        }

        try {
            Utils.showLoading();
            const response = await API.config.testS3(s3Config);
            if (response.success) {
                Utils.showSuccess('S3 settings tested successfully');
            } else {
                Utils.showError(response.error || 'S3 test failed');
            }
        } catch (error) {
            console.error('Error testing S3 settings:', error);
            Utils.showError(error.message || 'S3 test failed');
        } finally {
            Utils.hideLoading();
        }
    },

    // Cleanup
    cleanup() {}
};
window.AppSettings = AppSettings;
