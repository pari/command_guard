// SMTP Component
const SMTP = {
    currentConfig: {},
    
    // Initialize SMTP page
    async init() {
        try {
            this.render();
            await this.loadConfig();
        } catch (error) {
            console.error('SMTP init error:', error);
            Utils.showError('Failed to load SMTP configuration');
        }
    },
    
    // Render SMTP layout
    render() {
        const html = `
            <div class="page-header">
                <h2><i class="fas fa-envelope"></i> SMTP Settings</h2>
            </div>
            
            <div class="alert alert-info">
                <i class="fas fa-info-circle"></i>
                <strong>SMTP Configuration</strong> is required for sending Command and RunBook notifications by email.
            </div>
            
            <div class="row">
                <div class="col-lg-8">
                    <div class="card">
                        <div class="card-header">
                            <i class="fas fa-cog"></i> SMTP Server Configuration
                        </div>
                        <div class="card-body">
                            <form id="smtpForm">
                                <div class="row">
                                    <div class="col-md-8">
                                        <div class="mb-3">
                                            <label class="form-label">SMTP Server Host *</label>
                                            <input type="text" class="form-control" id="smtpHost" required 
                                                   placeholder="smtp.gmail.com">
                                            <div class="form-text">SMTP server hostname or IP address</div>
                                        </div>
                                    </div>
                                    <div class="col-md-4">
                                        <div class="mb-3">
                                            <label class="form-label">Port *</label>
                                            <input type="number" class="form-control" id="smtpPort" required 
                                                   placeholder="587" min="1" max="65535">
                                            <div class="form-text">Common: 25, 465, 587</div>
                                        </div>
                                    </div>
                                </div>
                                
                                <div class="row">
                                    <div class="col-md-6">
                                        <div class="mb-3">
                                            <label class="form-label">Username</label>
                                            <input type="text" class="form-control" id="smtpUsername" 
                                                   placeholder="your-email@domain.com">
                                            <div class="form-text">Leave empty for anonymous SMTP</div>
                                        </div>
                                    </div>
                                    <div class="col-md-6">
                                        <div class="mb-3">
                                            <label class="form-label">Password</label>
                                            <div class="input-group">
                                                <input type="password" class="form-control" id="smtpPassword" autocomplete="new-password">
                                                <button class="btn btn-outline-secondary" type="button" id="smtpPasswordReveal" title="Reveal secret" aria-label="Reveal secret" aria-pressed="false">
                                                    <i class="fas fa-eye"></i>
                                                </button>
                                            </div>
                                            <div class="form-text">Saved passwords are masked. Start typing to replace the existing password.</div>
                                        </div>
                                    </div>
                                </div>
                                
                                <div class="row">
                                    <div class="col-md-6">
                                        <div class="mb-3">
                                            <label class="form-label">From Email *</label>
                                            <input type="email" class="form-control" id="smtpFromEmail" required 
                                                   placeholder="noreply@yourcompany.com">
                                            <div class="form-text">Email address notifications are sent from</div>
                                        </div>
                                    </div>
                                    <div class="col-md-6">
                                        <div class="mb-3">
                                            <label class="form-label">From Name</label>
                                            <input type="text" class="form-control" id="smtpFromName" 
                                                   placeholder="Command Guard">
                                            <div class="form-text">Display name for the sender</div>
                                        </div>
                                    </div>
                                </div>
                                
                                <div class="row">
                                    <div class="col-md-4">
                                        <div class="mb-3">
                                            <label class="form-label">Encryption</label>
                                            <select class="form-select" id="smtpEncryption">
                                                <option value="none">None</option>
                                                <option value="tls">STARTTLS</option>
                                                <option value="ssl">SSL/TLS</option>
                                            </select>
                                        </div>
                                    </div>
                                    <div class="col-md-4">
                                        <div class="mb-3">
                                            <label class="form-label">Timeout (seconds)</label>
                                            <input type="number" class="form-control" id="smtpTimeout" 
                                                   value="30" min="5" max="300">
                                        </div>
                                    </div>
                                    <div class="col-md-4">
                                        <div class="mb-3">
                                            <label class="form-label">Max Retries</label>
                                            <input type="number" class="form-control" id="smtpRetries" 
                                                   value="3" min="0" max="10">
                                        </div>
                                    </div>
                                </div>
                                
                                <div class="row">
                                    <div class="col-md-6">
                                        <div class="mb-3">
                                            <div class="form-check">
                                                <input class="form-check-input" type="checkbox" id="smtpSkipCertVerify">
                                                <label class="form-check-label">Skip Certificate Verification</label>
                                                <div class="form-text">Use only for testing or self-signed certificates</div>
                                            </div>
                                        </div>
                                    </div>
                                    <div class="col-md-6">
                                        <div class="mb-3">
                                            <div class="form-check">
                                                <input class="form-check-input" type="checkbox" id="smtpEnabled" checked>
                                                <label class="form-check-label">Enable SMTP</label>
                                                <div class="form-text">Enable/disable email functionality</div>
                                            </div>
                                        </div>
                                    </div>
                                </div>
                                
                                <div class="mb-3">
                                    <button type="button" class="btn btn-primary" onclick="SMTP.saveConfig()">
                                        <i class="fas fa-save"></i> Save Configuration
                                    </button>
                                    <button type="button" class="btn btn-secondary ms-2" onclick="SMTP.resetForm()">
                                        <i class="fas fa-undo"></i> Reset
                                    </button>
                                </div>
                            </form>
                        </div>
                    </div>

                    <div class="card mt-3">
                        <div class="card-header">
                            <i class="fas fa-paper-plane"></i> Test SMTP Configuration
                        </div>
                        <div class="card-body">
                            <div class="mb-3">
                                <label class="form-label">Test Email Address</label>
                                <input type="email" class="form-control" id="testEmailAddress"
                                       placeholder="test@example.com">
                            </div>
                            <button class="btn btn-success" onclick="SMTP.sendTestEmail()">
                                <i class="fas fa-paper-plane"></i> Send Test Email
                            </button>
                        </div>
                    </div>
                </div>
                
                <div class="col-lg-4">
                    <div class="card">
                        <div class="card-header">
                            <i class="fas fa-lightbulb"></i> Quick Setup
                        </div>
                        <div class="card-body">
                            <h6>Common Providers:</h6>
                            <div class="d-grid gap-2">
                                <button class="btn btn-outline-primary btn-sm" onclick="SMTP.quickSetup('gmail')">
                                    <i class="fab fa-google"></i> Gmail
                                </button>
                                <button class="btn btn-outline-primary btn-sm" onclick="SMTP.quickSetup('outlook')">
                                    <i class="fab fa-microsoft"></i> Outlook/Hotmail
                                </button>
                                <button class="btn btn-outline-primary btn-sm" onclick="SMTP.quickSetup('yahoo')">
                                    <i class="fab fa-yahoo"></i> Yahoo Mail
                                </button>
                                <button class="btn btn-outline-secondary btn-sm" onclick="SMTP.quickSetup('office365')">
                                    <i class="fas fa-building"></i> Office 365
                                </button>
                            </div>
                        </div>
                    </div>
                    
                    <div class="card mt-3">
                        <div class="card-header">
                            <i class="fas fa-chart-line"></i> Email Statistics
                        </div>
                        <div class="card-body">
                            <div id="emailStats">
                                <div class="text-center text-muted">
                                    <i class="fas fa-spinner fa-spin"></i> Loading...
                                </div>
                            </div>
                        </div>
                    </div>
                </div>
            </div>
            
            <!-- Test Results Modal -->
            <div class="modal fade" id="testResultModal" tabindex="-1">
                <div class="modal-dialog modal-lg">
                    <div class="modal-content">
                        <div class="modal-header">
                            <h5 class="modal-title" id="testResultTitle">Test Results</h5>
                            <button type="button" class="btn-close" data-bs-dismiss="modal"></button>
                        </div>
                        <div class="modal-body">
                            <div id="testResultContent">
                                <div class="text-center">
                                    <i class="fas fa-spinner fa-spin"></i> Testing...
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
    },
    
    // Load SMTP configuration
    async loadConfig() {
        try {
            const response = await API.config.getSMTP();
            
            if (response.success && response.data) {
                this.currentConfig = response.data;
                this.populateForm();
                await this.loadEmailStats();
            } else {
                Utils.showError('Failed to load SMTP configuration');
            }
        } catch (error) {
            console.error('Error loading SMTP config:', error);
            Utils.showError('Failed to load SMTP configuration');
        }
    },
    
    // Populate form with current configuration
    populateForm() {
        const config = this.currentConfig;
        
        $('#smtpHost').val(config.host || '');
        $('#smtpPort').val(config.port || '');
        $('#smtpUsername').val(config.username || '');
        $('#smtpFromEmail').val(config.from_email || '');
        $('#smtpFromName').val(config.from_name || '');
        $('#smtpEncryption').val(config.encryption || 'none');
        $('#smtpTimeout').val(config.timeout ?? 30);
        $('#smtpRetries').val(config.retries ?? 3);
        $('#smtpSkipCertVerify').prop('checked', config.skip_cert_verify || false);
        $('#smtpEnabled').prop('checked', config.enabled !== false);
        
        Utils.setupMaskedSecretField('#smtpPassword', '#smtpPasswordReveal', config.password_length || 0);
    },
    
    // Quick setup for common providers
    quickSetup(provider) {
        const configs = {
            gmail: {
                host: 'smtp.gmail.com',
                port: 587,
                encryption: 'tls'
            },
            outlook: {
                host: 'smtp-mail.outlook.com',
                port: 587,
                encryption: 'tls'
            },
            yahoo: {
                host: 'smtp.mail.yahoo.com',
                port: 587,
                encryption: 'tls'
            },
            office365: {
                host: 'smtp.office365.com',
                port: 587,
                encryption: 'tls'
            }
        };
        
        const config = configs[provider];
        if (config) {
            $('#smtpHost').val(config.host);
            $('#smtpPort').val(config.port);
            $('#smtpEncryption').val(config.encryption);
            
            Utils.showInfo(`Quick setup applied for ${provider}. Please enter your username and password.`);
        }
    },
    
    // Save SMTP configuration
    async saveConfig() {
        try {
            const form = $('#smtpForm')[0];
            if (!form.checkValidity()) {
                form.reportValidity();
                return;
            }
            
            const timeout = parseInt($('#smtpTimeout').val(), 10);
            const retries = parseInt($('#smtpRetries').val(), 10);
            const config = {
                host: $('#smtpHost').val().trim(),
                port: parseInt($('#smtpPort').val(), 10),
                username: $('#smtpUsername').val().trim(),
                from_email: $('#smtpFromEmail').val().trim(),
                from_name: $('#smtpFromName').val().trim(),
                encryption: $('#smtpEncryption').val() || 'none',
                enabled: $('#smtpEnabled').is(':checked'),
                timeout: Number.isNaN(timeout) ? 30 : timeout,
                retries: Number.isNaN(retries) ? 3 : retries,
                skip_cert_verify: $('#smtpSkipCertVerify').is(':checked')
            };
            
            const password = Utils.getMaskedSecretValue('#smtpPassword');
            if (password) config.password = password;
            
            Utils.showLoading();
            
            const response = await API.config.updateSMTP(config);
            
            if (response.success) {
                Utils.showSuccess('SMTP configuration saved successfully');
                this.currentConfig = response.data;
                this.populateForm();
                await this.loadEmailStats();
            } else {
                Utils.showError(response.error || 'Failed to save SMTP configuration');
            }
        } catch (error) {
            console.error('Error saving SMTP config:', error);
            Utils.showError('Failed to save SMTP configuration');
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Reset form
    resetForm() {
        if (confirm('Are you sure you want to reset the form? Unsaved changes will be lost.')) {
            this.populateForm();
        }
    },
    
    // Test SMTP connection
    async testConnection() {
        try {
            $('#testResultContent').html(`
                <div class="text-center">
                    <i class="fas fa-spinner fa-spin"></i> Testing SMTP connection...
                </div>
            `);
            $('#testResultTitle').text('SMTP Connection Test');
            $('#testResultModal').modal('show');
            
            const response = await API.ops.testSMTP();
            
            if (response.success) {
                $('#testResultContent').html(`
                    <div class="alert alert-success">
                        <i class="fas fa-check-circle"></i>
                        <strong>Connection Successful!</strong>
                        <br>Successfully connected to SMTP server.
                    </div>
                    <div class="mt-3">
                        <h6>Connection Details:</h6>
                        <ul class="list-unstyled">
                            <li><strong>Host:</strong> ${this.currentConfig.host}</li>
                            <li><strong>Port:</strong> ${this.currentConfig.port}</li>
                            <li><strong>Encryption:</strong> ${this.currentConfig.encryption || 'None'}</li>
                            <li><strong>Authentication:</strong> ${this.currentConfig.username ? 'Yes' : 'No'}</li>
                        </ul>
                    </div>
                `);
            } else {
                $('#testResultContent').html(`
                    <div class="alert alert-danger">
                        <i class="fas fa-exclamation-triangle"></i>
                        <strong>Connection Failed!</strong>
                        <br>${response.error || 'Unknown error occurred'}
                    </div>
                    <div class="mt-3">
                        <h6>Troubleshooting Tips:</h6>
                        <ul>
                            <li>Verify the SMTP server host and port are correct</li>
                            <li>Check if your credentials are valid</li>
                            <li>Ensure the SMTP server allows connections from this IP</li>
                            <li>Try enabling "Skip Certificate Verification" for testing</li>
                        </ul>
                    </div>
                `);
            }
        } catch (error) {
            console.error('Error testing SMTP:', error);
            $('#testResultContent').html(`
                <div class="alert alert-danger">
                    <i class="fas fa-exclamation-triangle"></i>
                    <strong>Test Failed!</strong>
                    <br>Unable to perform connection test.
                </div>
            `);
        }
    },
    
    // Send test email
    async sendTestEmail() {
        try {
            const testEmail = $('#testEmailAddress').val().trim();
            if (!testEmail) {
                Utils.showError('Please enter a test email address');
                return;
            }

            const testEmailInput = $('#testEmailAddress')[0];
            if (!testEmailInput.checkValidity()) {
                testEmailInput.reportValidity();
                return;
            }

            const safePendingEmail = Utils.escapeHtml(testEmail);
            $('#testResultContent').html(`
                <div class="text-center">
                    <i class="fas fa-spinner fa-spin"></i> Sending test email to ${safePendingEmail}...
                </div>
            `);
            $('#testResultTitle').text('Test Email');
            $('#testResultModal').modal('show');
            
            const response = await API.ops.sendTestEmail(testEmail);
            
            if (response.success) {
                const details = response.data || {};
                const safeTo = Utils.escapeHtml(String(details.to || testEmail));
                const safeFrom = Utils.escapeHtml(String(details.from || this.currentConfig.from_email || ''));
                const safeSubject = Utils.escapeHtml(String(details.subject || 'Command Guard Test Email'));
                const safeSentAt = Utils.escapeHtml(Utils.formatDate(details.sent_at || new Date()));
                $('#testResultContent').html(`
                    <div class="alert alert-success">
                        <i class="fas fa-paper-plane"></i>
                        <strong>Test Email Sent!</strong>
                        <br>A test email has been successfully sent to <strong>${safeTo}</strong>
                    </div>
                    <div class="mt-3">
                        <h6>Email Details:</h6>
                        <ul class="list-unstyled">
                            <li><strong>To:</strong> ${safeTo}</li>
                            <li><strong>From:</strong> ${safeFrom}</li>
                            <li><strong>Subject:</strong> ${safeSubject}</li>
                            <li><strong>Sent:</strong> ${safeSentAt}</li>
                        </ul>
                        <div class="alert alert-info mt-3">
                            <small>
                                <i class="fas fa-info-circle"></i>
                                Please check the recipient's inbox (and spam folder) for the test email.
                            </small>
                        </div>
                    </div>
                `);
            } else {
                const safeError = Utils.escapeHtml(String(response.error || 'Unknown error occurred'));
                $('#testResultContent').html(`
                    <div class="alert alert-danger">
                        <i class="fas fa-exclamation-triangle"></i>
                        <strong>Email Send Failed!</strong>
                        <br>${safeError}
                    </div>
                    <div class="mt-3">
                        <h6>Common Issues:</h6>
                        <ul>
                            <li>SMTP server is not configured correctly</li>
                            <li>Authentication credentials are invalid</li>
                            <li>Recipient email address is invalid</li>
                            <li>SMTP server is blocking the connection</li>
                        </ul>
                    </div>
                `);
            }
        } catch (error) {
            console.error('Error sending test email:', error);
            const safeError = Utils.escapeHtml(String(error.message || 'Unable to send test email.'));
            $('#testResultContent').html(`
                <div class="alert alert-danger">
                    <i class="fas fa-exclamation-triangle"></i>
                    <strong>Test Failed!</strong>
                    <br>${safeError}
                </div>
            `);
        }
    },
    
    // Load email statistics
    async loadEmailStats() {
        try {
            const response = await API.ops.getEmailStats();
            
            if (response.success) {
                const stats = response.data;
                
                const html = `
                    <div class="row text-center">
                        <div class="col-6">
                            <div class="card bg-light">
                                <div class="card-body p-2">
                                    <h5 class="card-title text-success">${stats.sent_today || 0}</h5>
                                    <p class="card-text small">Sent Today</p>
                                </div>
                            </div>
                        </div>
                        <div class="col-6">
                            <div class="card bg-light">
                                <div class="card-body p-2">
                                    <h5 class="card-title text-primary">${stats.sent_total || 0}</h5>
                                    <p class="card-text small">Total Sent</p>
                                </div>
                            </div>
                        </div>
                    </div>
                    
                    <div class="row text-center mt-2">
                        <div class="col-6">
                            <div class="card bg-light">
                                <div class="card-body p-2">
                                    <h5 class="card-title text-warning">${stats.failed_today || 0}</h5>
                                    <p class="card-text small">Failed Today</p>
                                </div>
                            </div>
                        </div>
                        <div class="col-6">
                            <div class="card bg-light">
                                <div class="card-body p-2">
                                    <h5 class="card-title text-danger">${stats.failed_total || 0}</h5>
                                    <p class="card-text small">Total Failed</p>
                                </div>
                            </div>
                        </div>
                    </div>
                    
                    ${stats.last_sent ? `
                        <div class="mt-3">
                            <small class="text-muted">
                                <strong>Last Sent:</strong><br>
                                ${Utils.formatRelativeTime(stats.last_sent)}
                            </small>
                        </div>
                    ` : ''}
                    
                    ${stats.smtp_status ? `
                        <div class="mt-2">
                            <span class="badge ${stats.smtp_status === 'connected' ? 'bg-success' : 'bg-danger'}">
                                ${stats.smtp_status.toUpperCase()}
                            </span>
                        </div>
                    ` : ''}
                `;
                
                $('#emailStats').html(html);
            } else {
                $('#emailStats').html(`
                    <div class="text-center text-muted">
                        <i class="fas fa-chart-line"></i><br>
                        No statistics available
                    </div>
                `);
            }
        } catch (error) {
            console.error('Error loading email stats:', error);
            $('#emailStats').html(`
                <div class="text-center text-muted">
                    <i class="fas fa-exclamation-triangle"></i><br>
                    Failed to load statistics
                </div>
            `);
        }
    },
    
    // Cleanup
    cleanup() {
        // Clean up any event listeners or timers
    }
};
// Make component globally available
window.SMTP = SMTP;
