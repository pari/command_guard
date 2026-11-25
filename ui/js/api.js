// API Client Module
const API = {
    baseURL: '/api',
    
    // Get auth token from localStorage
    getToken() {
        return localStorage.getItem('auth_token');
    },
    
    // Set auth token
    setToken(token) {
        localStorage.setItem('auth_token', token);
    },
    
    // Remove auth token
    removeToken() {
        localStorage.removeItem('auth_token');
    },
    
    // Make authenticated request
    async request(url, options = {}) {
        const token = API.getToken();
        
        // Debug logging for authentication issues
        console.log('API Request:', url, 'Token exists:', !!token);
        
        const defaultOptions = {
            headers: {
                'Content-Type': 'application/json',
                'Accept': 'application/json',
                ...(token && { 'Authorization': `Bearer ${token}` })
            }
        };
        
        const mergedOptions = {
            ...defaultOptions,
            ...options,
            headers: {
                ...defaultOptions.headers,
                ...(options.headers || {})
            }
        };
        
        try {
            // Ensure baseURL is always included (use API.baseURL instead of this.baseURL)
            const fullURL = `${API.baseURL}${url}`;
            console.log('Full API URL:', fullURL);
            const response = await fetch(fullURL, mergedOptions);
            
            // Handle 401 Unauthorized
            if (response.status === 401) {
                console.error('401 Unauthorized on:', url);
                API.removeToken();
                window.location.href = '/login.html';
                throw new Error('Authentication required');
            }
            
            const data = await response.json();
            
            if (!response.ok) {
                const error = new Error(data.error || `HTTP ${response.status}`);
                error.status = response.status;
                error.data = data.data; // some endpoints (e.g. HTTP request runs) return details with the error
                throw error;
            }
            
            return data;
        } catch (error) {
            console.error('API Error:', error);
            throw error;
        }
    },
    
    // Auth endpoints
    auth: {
        async login(password, username = '', totpCode = '') {
            const response = await fetch('/api/auth/login', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ username, password, totp_code: totpCode })
            });
            return response.json();
        },
        
        async verify2FA(tempToken, totpCode) {
            const response = await fetch('/api/auth/verify-2fa', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ 
                    temp_token: tempToken,
                    totp_code: totpCode 
                })
            });
            return response.json();
        },

		async completeOnboarding(tempToken, newPassword, totpCode) {
			const response = await fetch('/api/auth/complete-onboarding', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ temp_token: tempToken, new_password: newPassword, totp_code: totpCode }) });
			return response.json();
		},
        
        async validate() {
            return API.request('/auth/validate');
        },
        
        async logout() {
            await API.request('/auth/logout', { method: 'POST' });
            API.removeToken();
            window.location.href = '/login.html';
        }
    },

	users: {
		async list() { return API.request('/users'); },
		async create(data) { return API.request('/users', { method: 'POST', body: JSON.stringify(data) }); },
		async update(username, data) { return API.request(`/users/${encodeURIComponent(username)}`, { method: 'PUT', body: JSON.stringify(data) }); },
		async remove(username) { return API.request(`/users/${encodeURIComponent(username)}`, { method: 'DELETE' }); },
		async action(username, action, data = {}) { return API.request(`/users/${encodeURIComponent(username)}/${action}`, { method: 'POST', body: JSON.stringify(data) }); },
		async audit(page = 1, filter = '') { return API.request(`/security/audit?page=${page}&page_size=50&filter=${encodeURIComponent(filter)}`); }
	},

	account: {
		async changePassword(currentPassword, newPassword) { return API.request('/account/password', { method: 'POST', body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }) }); }
	},

	user: {
		async commands() { return API.request('/user/commands'); },
		async runbooks() { return API.request('/user/runbooks'); },
		async commandSchedule(id, cronSchedule) { return API.request(`/user/commands/${encodeURIComponent(id)}/schedule`, { method: 'PUT', body: JSON.stringify({ cron_schedule: cronSchedule }) }); },
		async runbookSchedule(id, cronSchedule) { return API.request(`/user/runbooks/${encodeURIComponent(id)}/schedule`, { method: 'PUT', body: JSON.stringify({ cron_schedule: cronSchedule }) }); },
		async httpRequests() { return API.request('/user/http-requests'); },
		async httpRequestSchedule(id, cronSchedule) { return API.request(`/user/http-requests/${encodeURIComponent(id)}/schedule`, { method: 'PUT', body: JSON.stringify({ cron_schedule: cronSchedule }) }); }
	},

	// Scheduled HTTP requests / API calls (admin config at /api/config/http-requests, execution at /api/http-requests/{id})
	httpRequests: {
		async list() { return API.request('/config/http-requests'); },
		async create(data) { return API.request('/config/http-requests', { method: 'POST', body: JSON.stringify(data) }); },
		async update(id, data) { return API.request(`/config/http-requests/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(data) }); },
		async remove(id) { return API.request(`/config/http-requests/${encodeURIComponent(id)}`, { method: 'DELETE' }); },
		// Admin UI execution relies on the JWT; the request's own bearer_token is only for external callers
		async execute(id) { return API.request(`/http-requests/${encodeURIComponent(id)}`, { method: 'POST' }); }
	},

	// Persistent SSH terminals (sessions live in cg_sessiond; attach is a WebSocket at /api/terminals/{id}/ws)
	terminals: {
		async hosts() { return API.request('/terminals/hosts'); },
		async list() { return API.request('/terminals'); },
		async create(data) { return API.request('/terminals', { method: 'POST', body: JSON.stringify(data) }); },
		async kill(id) { return API.request(`/terminals/${encodeURIComponent(id)}`, { method: 'DELETE' }); },
		async rename(id, name) { return API.request(`/terminals/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify({ name }) }); },
		async saveOrder(ids) { return API.request('/terminals/order', { method: 'PUT', body: JSON.stringify({ ids }) }); },
		async uploadImage(id, image) { return API.request(`/terminals/${encodeURIComponent(id)}/images`, { method: 'POST', body: image, headers: { 'Content-Type': image.type || 'application/octet-stream' } }); },
		async uploadFile(id, file) { return API.request(`/terminals/${encodeURIComponent(id)}/files?name=${encodeURIComponent(file.name)}`, { method: 'POST', body: file, headers: { 'Content-Type': 'application/octet-stream' } }); },
		// Agent Sessions: saved launch presets (shared ones live in config.json, personal ones per user)
		agentSessions: {
			async list() { return API.request('/terminals/agent-sessions'); },
			async create(data) { return API.request('/terminals/agent-sessions', { method: 'POST', body: JSON.stringify(data) }); },
			async update(id, data) { return API.request(`/terminals/agent-sessions/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(data) }); },
			async remove(id, scope) { return API.request(`/terminals/agent-sessions/${encodeURIComponent(id)}?scope=${encodeURIComponent(scope || 'personal')}`, { method: 'DELETE' }); }
		}
	},
    
    // Disk Monitor endpoints (admin only)
    // Remote Desktops (VNC). Viewer: /api/vnc (ticket WebSocket at /api/vnc/ws); admin config: /api/config/vnc-connections
    vnc: {
        async list() { return API.request('/vnc/connections'); },
        async connect(id, viewOnly = false) { return API.request(`/vnc/connections/${encodeURIComponent(id)}/sessions`, { method: 'POST', body: JSON.stringify({ view_only: !!viewOnly }) }); },
        async sessions() { return API.request('/vnc/sessions'); },
        async session(sid) { return API.request(`/vnc/sessions/${encodeURIComponent(sid)}`); },
        async disconnect(sid) { return API.request(`/vnc/sessions/${encodeURIComponent(sid)}`, { method: 'DELETE' }); },
        async ping(sid) { return API.request(`/vnc/sessions/${encodeURIComponent(sid)}/ping`, { method: 'POST' }); },
        async thumbnail(id) { return API.request(`/vnc/connections/${encodeURIComponent(id)}/thumbnail`); },
        async saveThumbnail(id, blob) { return API.request(`/vnc/connections/${encodeURIComponent(id)}/thumbnail`, { method: 'PUT', body: blob, headers: { 'Content-Type': 'image/jpeg' } }); },
        admin: {
            async list() { return API.request('/config/vnc-connections'); },
            async get(id) { return API.request(`/config/vnc-connections/${encodeURIComponent(id)}`); },
            async create(data) { return API.request('/config/vnc-connections', { method: 'POST', body: JSON.stringify(data) }); },
            async update(id, data) { return API.request(`/config/vnc-connections/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(data) }); },
            async remove(id) { return API.request(`/config/vnc-connections/${encodeURIComponent(id)}`, { method: 'DELETE' }); },
            async test(id, data) { return API.request(`/config/vnc-connections/${encodeURIComponent(id)}/test`, { method: 'POST', body: JSON.stringify(data || {}) }); },
            async access(id) { return API.request(`/config/vnc-connections/${encodeURIComponent(id)}/access`); },
            async saveAccess(id, access) { return API.request(`/config/vnc-connections/${encodeURIComponent(id)}/access`, { method: 'PUT', body: JSON.stringify({ access }) }); }
        }
    },

    diskMonitor: {
        async getSettings() { return API.request('/config/disk-monitor'); },
        async saveSettings(settings) { return API.request('/config/disk-monitor', { method: 'PUT', body: JSON.stringify(settings) }); },
        async listVolumes() { return API.request('/config/disk-monitor/volumes'); },
        async createVolume(volume) { return API.request('/config/disk-monitor/volumes', { method: 'POST', body: JSON.stringify(volume) }); },
        async bulkCreate(payload) { return API.request('/config/disk-monitor/volumes/bulk', { method: 'POST', body: JSON.stringify(payload) }); },
        async updateVolume(id, volume) { return API.request(`/config/disk-monitor/volumes/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(volume) }); },
        async deleteVolume(id) { return API.request(`/config/disk-monitor/volumes/${encodeURIComponent(id)}`, { method: 'DELETE' }); },
        async getState() { return API.request('/disk-monitor/state'); },
        async history(id) { return API.request(`/disk-monitor/volumes/${encodeURIComponent(id)}/history`); },
        async checkVolume(id) { return API.request(`/disk-monitor/volumes/${encodeURIComponent(id)}/check`, { method: 'POST' }); },
        async checkHost(hostRef) { return API.request(`/disk-monitor/hosts/${encodeURIComponent(hostRef)}/check`, { method: 'POST' }); },
        async testHost(hostRef, useSudo) { return API.request(`/disk-monitor/hosts/${encodeURIComponent(hostRef)}/test`, { method: 'POST', body: JSON.stringify(useSudo === undefined ? {} : { use_sudo: !!useSudo }) }); },
        async detect(hostRef, useSudo) { return API.request(`/disk-monitor/hosts/${encodeURIComponent(hostRef)}/detect`, { method: 'POST', body: JSON.stringify({ use_sudo: !!useSudo }) }); },
        async testEmail() { return API.request('/disk-monitor/test-email', { method: 'POST' }); }
    },

    // Configuration endpoints
    config: {
        async get() {
            return API.request('/config');
        },
        
        async getHosts() {
            return API.request('/config/hosts');
        },
        
        async createHost(hostData) {
            return API.request('/config/hosts', {
                method: 'POST',
                body: JSON.stringify(hostData)
            });
        },
        
        async updateHost(name, hostData) {
            return API.request(`/config/hosts/${encodeURIComponent(name)}`, {
                method: 'PUT',
                body: JSON.stringify(hostData)
            });
        },
        
        async deleteHost(name) {
            return API.request(`/config/hosts/${encodeURIComponent(name)}`, {
                method: 'DELETE'
            });
        },
        
        async getCommands() {
            return API.request('/config/commands');
        },
        
        async createCommand(cmdData) {
            return API.request('/config/commands', {
                method: 'POST',
                body: JSON.stringify(cmdData)
            });
        },
        
        async updateCommand(cmdId, cmdData) {
            return API.request(`/config/commands/${encodeURIComponent(cmdId)}`, {
                method: 'PUT',
                body: JSON.stringify(cmdData)
            });
        },
        
        async deleteCommand(cmdId) {
            return API.request(`/config/commands/${encodeURIComponent(cmdId)}`, {
                method: 'DELETE'
            });
        },
        
        async executeCommand(cmdId) {
            // The backend expects GET /api/commands/{cmd_id} for execution
            return API.request(`/commands/${encodeURIComponent(cmdId)}`, {
                method: 'GET'
            });
        },
        
        async getRunBooks() {
            return API.request('/config/runbooks');
        },

        async createRunBook(runBook) {
            return API.request('/config/runbooks', { method: 'POST', body: JSON.stringify(runBook) });
        },

        async updateRunBook(runBookId, runBook) {
            return API.request(`/config/runbooks/${encodeURIComponent(runBookId)}`, { method: 'PUT', body: JSON.stringify(runBook) });
        },

        async deleteRunBook(runBookId) {
            return API.request(`/config/runbooks/${encodeURIComponent(runBookId)}`, { method: 'DELETE' });
        },
        
        async update(config) {
            return API.request('/config', {
                method: 'PUT',
                body: JSON.stringify(config)
            });
        },
        
        async getVariables() {
            return API.request('/config/variables');
        },
        
        async updateVariables(variables) {
            return API.request('/config/variables', {
                method: 'PUT',
                body: JSON.stringify(variables)
            });
        },
        
        async getSMTP() {
            return API.request('/config/smtp');
        },
        
        async updateSMTP(smtpData) {
            return API.request('/config/smtp', {
                method: 'PUT',
                body: JSON.stringify(smtpData)
            });
        },

        async getScheduledJobsOverviewEmail() {
            return API.request('/config/scheduled-jobs-overview-email');
        },

        async updateScheduledJobsOverviewEmail(settings) {
            return API.request('/config/scheduled-jobs-overview-email', {
                method: 'PUT',
                body: JSON.stringify(settings)
            });
        },

        async testS3(s3Data) {
            return API.request('/config/s3/test', {
                method: 'POST',
                body: JSON.stringify(s3Data)
            });
        }
    },

    backups: {
        async export(password) {
            const token = API.getToken();
            const response = await fetch(`${API.baseURL}/config/backups/export`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'Authorization': `Bearer ${token}`
                },
                body: JSON.stringify({ password })
            });

            if (response.status === 401) {
                API.removeToken();
                window.location.href = '/login.html';
                throw new Error('Authentication required');
            }
            if (!response.ok) {
                const data = await response.json().catch(() => ({}));
                throw new Error(data.error || 'Failed to export backup');
            }

            const blob = await response.blob();
            const disposition = response.headers.get('Content-Disposition') || '';
            const match = disposition.match(/filename="?([^"]+)"?/);
            const filename = match ? match[1] : `commandguard_backup_${Date.now()}.cgbak`;
            const downloadUrl = window.URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = downloadUrl;
            a.download = filename;
            document.body.appendChild(a);
            a.click();
            window.URL.revokeObjectURL(downloadUrl);
            document.body.removeChild(a);
        },

        async inspect(file, password) {
            return API.backups.sendBackupForm('/config/backups/inspect', file, password, false);
        },

        async restore(file, password) {
            return API.backups.sendBackupForm('/config/backups/restore', file, password, true);
        },

        async sendBackupForm(path, file, password, confirm) {
            const token = API.getToken();
            const formData = new FormData();
            formData.append('backup', file);
            formData.append('password', password);
            if (confirm) {
                formData.append('confirm', 'true');
            }

            const response = await fetch(`${API.baseURL}${path}`, {
                method: 'POST',
                headers: {
                    'Authorization': `Bearer ${token}`
                },
                body: formData
            });

            if (response.status === 401) {
                API.removeToken();
                window.location.href = '/login.html';
                throw new Error('Authentication required');
            }
            const data = await response.json().catch(() => ({}));
            if (!response.ok) {
                throw new Error(data.error || 'Backup request failed');
            }
            return data;
        }
    },
    
    // Command execution endpoints
    commands: {
        async execute(cmdId, headers = {}) {
            // The backend expects GET /api/commands/{cmd_id} for execution
            // Pass any additional headers (like bearer token)
            return API.request(`/commands/${encodeURIComponent(cmdId)}`, {
                method: 'GET',
                headers: headers
            });
        }
    },
    
    // Operational endpoints
    ops: {
        async getStatus() {
            return API.request('/status');
        },

        async sendTestEmail(to) {
            return API.request('/config/smtp/test-email', {
                method: 'POST',
                body: JSON.stringify({ to })
            });
        },

        async getScheduledJobsOverview(date) {
            const query = date ? `?date=${encodeURIComponent(date)}` : '';
            return API.request(`/overview/scheduled-jobs${query}`);
        },
        
        async getLogs(limit) {
            const url = limit ? `/logs?limit=${limit}` : '/logs';
            return API.request(url);
        },
        
        async getLogContent(traceId) {
            return API.request(`/logs/${traceId}/content`);
        },

        // One window of a log: the last `bytes` bytes, or those ending at byte offset `before`.
        async getLogChunk(traceId, bytes, before) {
            const params = new URLSearchParams({ bytes: String(bytes) });
            if (before !== undefined && before !== null) params.set('before', String(before));
            return API.request(`/logs/${encodeURIComponent(traceId)}/content?${params.toString()}`);
        },
        
        async downloadLog(traceId) {
            const token = API.getToken();
            const url = `${API.baseURL}/logs/${traceId}/download`;
            
            // Create a temporary link to download with auth header
            const response = await fetch(url, {
                headers: {
                    'Authorization': `Bearer ${token}`
                }
            });
            
            if (!response.ok) {
                throw new Error('Failed to download log');
            }
            
            const blob = await response.blob();
            const downloadUrl = window.URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = downloadUrl;
            a.download = `log_${traceId}.txt`;
            document.body.appendChild(a);
            a.click();
            window.URL.revokeObjectURL(downloadUrl);
            document.body.removeChild(a);
        },
        
        async executeCommand(cmdId) {
            return API.request(`/commands/${encodeURIComponent(cmdId)}`, {
                method: 'GET'
            });
        },
        
        async startRunBook(runBookId) {
            return API.request(`/runbooks/${encodeURIComponent(runBookId)}/runs`, { method: 'POST', headers: { 'X-RunBook-Trigger': 'ui' } });
        },

        async getRunBookRuns(runBookId) {
            return API.request(`/runbooks/${encodeURIComponent(runBookId)}/runs`);
        },

        async getRunBookRun(runBookId, runId) {
            return API.request(`/runbooks/${encodeURIComponent(runBookId)}/runs/${encodeURIComponent(runId)}`);
        },

        async stopRunBook(runBookId, runId) {
            return API.request(`/runbooks/${encodeURIComponent(runBookId)}/runs/${encodeURIComponent(runId)}/stop`, { method: 'POST' });
        },
        
        async deleteLog(filename) {
            return API.request(`/logs/delete/${encodeURIComponent(filename)}`, {
                method: 'DELETE'
            });
        },

        async clearOldLogs(days) {
            return API.request(`/logs/clear-old?days=${days}`, {
                method: 'DELETE'
            });
        },

        async testConnection(hostId) {
            // Test connection by creating and executing a simple echo command
            const testCmdId = `test-${hostId}-${Date.now()}`;
            
            try {
                // Create a temporary test command
                const createResponse = await API.request('/config/commands', {
                    method: 'POST',
                    body: JSON.stringify({
                        cmd_id: testCmdId,
                        host_ref: hostId,
                        command: 'echo "Connection successful"',
                        description: 'Temporary test command',
                        timeout: 5
                    })
                });
                
                if (!createResponse.success) {
                    throw new Error('Failed to create test command');
                }
                
                // Execute the test command
                const execResponse = await API.request(`/commands/${testCmdId}`, {
                    method: 'GET'
                });
                
                // Clean up - delete the test command
                await API.request(`/config/commands/${testCmdId}`, {
                    method: 'DELETE'
                });
                
                return execResponse;
                
            } catch (error) {
                // Try to clean up if command was created
                try {
                    await API.request(`/config/commands/${testCmdId}`, {
                        method: 'DELETE'
                    });
                } catch (e) {
                    // Ignore cleanup errors
                }
                throw error;
            }
        }
    },
    
    // PEM Files operations
    pemFiles: {
        async list() {
            return API.request('/pemfiles', {
                method: 'GET'
            });
        },
        
        async upload(formData) {
            const token = API.getToken();
            const response = await fetch(`${API.baseURL}/pemfiles`, {
                method: 'POST',
                headers: {
                    'Authorization': `Bearer ${token}`
                },
                body: formData
            });
            return response.json();
        },
        
        async download(fileName) {
            const token = API.getToken();
            const url = `${API.baseURL}/pemfiles/${encodeURIComponent(fileName)}`;
            
            // Fetch the file with proper authorization
            const response = await fetch(url, {
                method: 'GET',
                headers: {
                    'Authorization': `Bearer ${token}`
                }
            });
            
            if (!response.ok) {
                throw new Error('Failed to download PEM file');
            }
            
            // Get the blob and create download link
            const blob = await response.blob();
            const downloadUrl = window.URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = downloadUrl;
            a.download = fileName;
            document.body.appendChild(a);
            a.click();
            window.URL.revokeObjectURL(downloadUrl);
            document.body.removeChild(a);
        },
        
        async rename(oldName, newName) {
            return API.request(`/pemfiles/${encodeURIComponent(oldName)}/rename`, {
                method: 'PUT',
                body: JSON.stringify({ new_name: newName })
            });
        },
        
        async delete(fileName) {
            return API.request(`/pemfiles/${encodeURIComponent(fileName)}`, {
                method: 'DELETE'
            });
        }
    }
};
// Make API globally available
window.API = API;
