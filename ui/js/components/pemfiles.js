// PEM Files Component
const PemFiles = {
    pemFiles: [],
    currentRenameFile: null,
    currentConfig: null,
    
    // Initialize PEM files page
    async init() {
        try {
            this.render();
            await this.loadPemFiles();
        } catch (error) {
            console.error('PEM Files init error:', error);
            Utils.showError('Failed to load PEM files');
        }
    },
    
    // Render PEM files layout
    render() {
        const html = `
            <div class="pem-files-container commands-page">
                <div class="commands-page-header">
                    <div>
                        <h1>PEM Files</h1>
                        <p>Manage uploaded SSH private keys used by your hosts.</p>
                    </div>
                    <div class="commands-header-actions">
                        <button class="btn btn-outline-primary commands-add-button" onclick="PemFiles.showCreateModal()">
                            <i class="fas fa-key me-2"></i>Create PEM file
                        </button>
                        <button class="btn btn-primary commands-add-button" onclick="PemFiles.showUploadModal()">
                            <i class="fas fa-plus me-2"></i>Upload PEM file
                        </button>
                    </div>
                </div>
                
                <div id="pemFilesListContainer">
                    <div class="text-center py-5">
                        <i class="fas fa-spinner fa-spin fa-2x text-primary"></i>
                        <p class="mt-3 text-muted">Loading PEM files...</p>
                    </div>
                </div>
            </div>
            
            <!-- Upload PEM File Modal -->
            <div class="modal fade" id="pemUploadModal" tabindex="-1">
                <div class="modal-dialog">
                    <div class="modal-content">
                        <div class="modal-header">
                            <h5 class="modal-title">Upload PEM File</h5>
                            <button type="button" class="btn-close" data-bs-dismiss="modal"></button>
                        </div>
                        <div class="modal-body">
                            <form id="pemUploadForm">
                                <div class="mb-3">
                                    <label class="form-label">Select PEM File</label>
                                    <input type="file" class="form-control" id="pemFileInput" accept=".pem" required>
                                    <div class="form-text">Only .pem files are accepted</div>
                                </div>
                                <div class="mb-3">
                                    <label class="form-label">File Name (optional)</label>
                                    <input type="text" class="form-control" id="pemFileName" placeholder="Leave empty to use original name">
                                    <div class="form-text">Custom name for the PEM file (without .pem extension)</div>
                                </div>
                            </form>
                        </div>
                        <div class="modal-footer">
                            <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Cancel</button>
                            <button type="button" class="btn btn-primary" onclick="PemFiles.uploadFile()">
                                <i class="fas fa-upload me-1"></i> Upload
                            </button>
                        </div>
                    </div>
                </div>
            </div>
            
            <!-- Create PEM File Modal -->
            <div class="modal fade" id="pemCreateModal" tabindex="-1">
                <div class="modal-dialog modal-lg">
                    <div class="modal-content">
                        <div class="modal-header">
                            <h5 class="modal-title">Create PEM File</h5>
                            <button type="button" class="btn-close" data-bs-dismiss="modal"></button>
                        </div>
                        <div class="modal-body">
                            <form id="pemCreateForm">
                                <div class="mb-3">
                                    <label class="form-label">File Name</label>
                                    <input type="text" class="form-control" id="pemCreateName" placeholder="deploy-key" required>
                                    <div class="form-text">Enter a file name with or without the .pem extension</div>
                                </div>
                                <div class="mb-3">
                                    <label class="form-label">RSA Private Key</label>
                                    <textarea class="form-control pem-create-key-textarea" id="pemCreateContent" spellcheck="false" placeholder="-----BEGIN RSA PRIVATE KEY-----&#10;...&#10;-----END RSA PRIVATE KEY-----" required></textarea>
                                    <div class="form-text">Paste the full RSA private key block, including BEGIN and END lines</div>
                                </div>
                            </form>
                        </div>
                        <div class="modal-footer">
                            <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Cancel</button>
                            <button type="button" class="btn btn-primary" onclick="PemFiles.createFile()">
                                <i class="fas fa-save me-1"></i> Create
                            </button>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Rename PEM File Modal -->
            <div class="modal fade" id="pemRenameModal" tabindex="-1">
                <div class="modal-dialog">
                    <div class="modal-content">
                        <div class="modal-header">
                            <h5 class="modal-title">Rename PEM File</h5>
                            <button type="button" class="btn-close" data-bs-dismiss="modal"></button>
                        </div>
                        <div class="modal-body">
                            <form id="pemRenameForm">
                                <div class="mb-3">
                                    <label class="form-label">Current Name</label>
                                    <input type="text" class="form-control" id="pemCurrentName" readonly>
                                </div>
                                <div class="mb-3">
                                    <label class="form-label">New Name</label>
                                    <input type="text" class="form-control" id="pemNewName" required>
                                    <div class="form-text">Enter new name for the PEM file (without .pem extension)</div>
                                </div>
                            </form>
                        </div>
                        <div class="modal-footer">
                            <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Cancel</button>
                            <button type="button" class="btn btn-primary" onclick="PemFiles.renameFile()">
                                <i class="fas fa-save me-1"></i> Rename
                            </button>
                        </div>
                    </div>
                </div>
            </div>
        `;
        
        $('#mainContent').html(html);
    },
    
    // Load PEM files list
    async loadPemFiles() {
        try {
            // Load PEM files
            const pemResponse = await API.pemFiles.list();
            
            // Load configuration to count host usage
            const configResponse = await API.config.get();
            
            if (pemResponse.success) {
                this.pemFiles = pemResponse.data || [];
                if (configResponse.success && configResponse.data) {
                    this.currentConfig = configResponse.data;
                }
                this.renderPemFilesList();
            } else {
                Utils.showError('Failed to load PEM files');
            }
        } catch (error) {
            console.error('Error loading PEM files:', error);
            Utils.showError('Failed to load PEM files');
        }
    },
    
    // Count hosts using a specific PEM file
    getHostCountForPemFile(pemFileName) {
        if (!this.currentConfig || !this.currentConfig.hosts) return 0;
        
        const pemPath = `./pem_files/${pemFileName}`;
        let count = 0;
        
        Object.values(this.currentConfig.hosts).forEach(host => {
            if (host.pem_file === pemPath) {
                count++;
            }
        });
        
        return count;
    },
    
    // Render PEM files list
    renderPemFilesList() {
        const container = $('#pemFilesListContainer');
        let html = '';
        
        if (this.pemFiles.length === 0) {
            html = `
                <div class="commands-empty-state">
                    <i class="fas fa-key fa-3x text-muted mb-3"></i>
                    <p class="text-muted">No PEM files uploaded yet.</p>
                    <button class="btn btn-primary" onclick="PemFiles.showUploadModal()">
                        <i class="fas fa-plus me-1"></i> Upload Your First PEM File
                    </button>
                </div>
            `;
        } else {
            const files = [...this.pemFiles].sort((a, b) => String(a.name || '').localeCompare(String(b.name || '')));
            const rows = files.map(file => {
                const hostCount = this.getHostCountForPemFile(file.name);
                const isInUse = hostCount > 0;
                const safeName = Utils.escapeHtml(String(file.name || ''));
                const encodedName = encodeURIComponent(String(file.name || ''));
                const usageLabel = hostCount === 1 ? '1 host' : `${hostCount} hosts`;
                const usageClass = isInUse ? 'is-in-use' : 'is-unused';
                const protectedClass = isInUse ? 'disabled opacity-50' : '';

                return `
                    <tr>
                        <td><div class="commands-command-name" title="${safeName}">${safeName}</div></td>
                        <td class="commands-number-cell">${this.formatFileSize(file.size || 0)}</td>
                        <td>${this.formatModifiedDate(file.modified)}</td>
                        <td class="commands-number-cell"><span class="pem-usage-badge ${usageClass}">${usageLabel}</span></td>
                        <td>
                            <div class="commands-row-actions pem-file-actions">
                                <button class="btn btn-sm btn-icon btn-warning ${protectedClass}"
                                        type="button" data-pem-action="rename" data-pem-name="${encodedName}" data-host-count="${hostCount}"
                                        title="${isInUse ? 'Cannot rename - file in use' : 'Rename'}" aria-label="Rename ${safeName}">
                                    <i class="fas fa-edit"></i>
                                </button>
                                <button class="btn btn-sm btn-icon btn-primary" type="button" data-pem-action="download" data-pem-name="${encodedName}"
                                        title="Download" aria-label="Download ${safeName}">
                                    <i class="fas fa-download"></i>
                                </button>
                                <button class="btn btn-sm btn-icon btn-danger ${protectedClass}"
                                        type="button" data-pem-action="delete" data-pem-name="${encodedName}" data-host-count="${hostCount}"
                                        title="${isInUse ? 'Cannot delete - file in use' : 'Delete'}" aria-label="Delete ${safeName}">
                                    <i class="fas fa-trash"></i>
                                </button>
                            </div>
                        </td>
                    </tr>
                `;
            }).join('');

            html = `
                <div class="commands-table-card">
                    <div class="commands-table-scroll">
                        <table class="commands-table pem-files-table">
                            <thead><tr>
                                <th>PEM File</th>
                                <th>Size</th>
                                <th>Modified</th>
                                <th>Used by Hosts</th>
                                <th>Actions</th>
                            </tr></thead>
                            <tbody>${rows}</tbody>
                        </table>
                    </div>
                    <div class="commands-table-footer">
                        <span>Showing ${files.length} PEM file${files.length === 1 ? '' : 's'}</span>
                    </div>
                </div>
            `;
        }
        
        container.html(html);
        this.bindTableActions(container);
    },
    
    // Format file size
    formatFileSize(bytes) {
        if (bytes === 0) return '0 Bytes';
        const k = 1024;
        const sizes = ['Bytes', 'KB', 'MB', 'GB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return Math.round(bytes / Math.pow(k, i) * 100) / 100 + ' ' + sizes[i];
    },

    formatModifiedDate(value) {
        if (!value) return '—';
        const date = new Date(value);
        return Number.isNaN(date.getTime()) ? '—' : date.toLocaleDateString();
    },

    bindTableActions(container) {
        container.off('click.pemFilesActions', '[data-pem-action]').on('click.pemFilesActions', '[data-pem-action]', event => {
            const button = $(event.currentTarget);
            const action = button.data('pem-action');
            const fileName = decodeURIComponent(String(button.attr('data-pem-name') || ''));
            const hostCount = Number(button.attr('data-host-count') || 0);

            if (!fileName) return;
            if (action === 'rename') {
                hostCount > 0 ? this.showInUseAlert('rename', fileName, hostCount) : this.showRenameModal(fileName);
            } else if (action === 'download') {
                this.downloadFile(fileName);
            } else if (action === 'delete') {
                hostCount > 0 ? this.showInUseAlert('delete', fileName, hostCount) : this.deleteFile(fileName);
            }
        });
    },
    
    // Show upload modal
    showUploadModal() {
        $('#pemUploadForm')[0].reset();
        $('#pemUploadModal').modal('show');
    },

    // Show create modal
    showCreateModal() {
        $('#pemCreateForm')[0].reset();
        $('#pemCreateModal').modal('show');
        setTimeout(() => $('#pemCreateName').trigger('focus'), 150);
    },

    normalizePemFileName(fileName) {
        const trimmedName = String(fileName || '').trim();

        if (!trimmedName) {
            return '';
        }

        return trimmedName.toLowerCase().endsWith('.pem') ? trimmedName : `${trimmedName}.pem`;
    },

    validateManualPem(fileName, pemContent) {
        const finalFileName = this.normalizePemFileName(fileName);
        const normalizedContent = String(pemContent || '').replace(/\r\n?/g, '\n').trim();

        if (!finalFileName) {
            return { valid: false, message: 'Please enter a file name' };
        }

        if (/[\\/]/.test(finalFileName) || finalFileName === '.pem') {
            return { valid: false, message: 'File name cannot contain path separators' };
        }

        if (!normalizedContent) {
            return { valid: false, message: 'Please paste the RSA private key' };
        }

        const rsaPrivateKeyPattern = /^-----BEGIN RSA PRIVATE KEY-----\n[\s\S]+\n-----END RSA PRIVATE KEY-----$/;
        if (!rsaPrivateKeyPattern.test(normalizedContent)) {
            return { valid: false, message: 'Paste a valid RSA private key block with matching BEGIN and END lines' };
        }

        return {
            valid: true,
            fileName: finalFileName,
            content: `${normalizedContent}\n`
        };
    },

    // Create PEM file from pasted RSA private key
    async createFile() {
        const validation = this.validateManualPem($('#pemCreateName').val(), $('#pemCreateContent').val());

        if (!validation.valid) {
            Utils.showError(validation.message);
            return;
        }

        try {
            const pemFile = new File([validation.content], validation.fileName, { type: 'application/x-pem-file' });
            const formData = new FormData();
            formData.append('file', pemFile);
            formData.append('filename', validation.fileName);

            Utils.showLoading();

            const response = await API.pemFiles.upload(formData);

            if (response.success) {
                Utils.showSuccess('PEM file created successfully');
                $('#pemCreateModal').modal('hide');
                $('#pemCreateForm')[0].reset();
                await this.loadPemFiles();
            } else {
                Utils.showError(response.error || 'Failed to create PEM file');
            }
        } catch (error) {
            console.error('Error creating PEM file:', error);
            Utils.showError('Failed to create PEM file');
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Upload PEM file
    async uploadFile() {
        try {
            const fileInput = $('#pemFileInput')[0];
            const customName = $('#pemFileName').val().trim();
            
            if (!fileInput.files || fileInput.files.length === 0) {
                Utils.showError('Please select a PEM file');
                return;
            }
            
            const file = fileInput.files[0];
            
            // Validate file extension
            if (!file.name.endsWith('.pem')) {
                Utils.showError('Only .pem files are allowed');
                return;
            }
            
            // Prepare form data
            const formData = new FormData();
            formData.append('file', file);
            
            // Use custom name if provided, otherwise use original name
            let fileName = customName || file.name;
            if (customName && !customName.endsWith('.pem')) {
                fileName = customName + '.pem';
            }
            formData.append('filename', fileName);
            
            Utils.showLoading();
            
            const response = await API.pemFiles.upload(formData);
            
            if (response.success) {
                Utils.showSuccess('PEM file uploaded successfully');
                $('#pemUploadModal').modal('hide');
                await this.loadPemFiles();
            } else {
                Utils.showError(response.error || 'Failed to upload PEM file');
            }
        } catch (error) {
            console.error('Error uploading PEM file:', error);
            Utils.showError('Failed to upload PEM file');
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Download PEM file
    async downloadFile(fileName) {
        try {
            // Use the API download method with proper authorization
            await API.pemFiles.download(fileName);
            Utils.showSuccess('PEM file downloaded successfully');
        } catch (error) {
            console.error('Error downloading PEM file:', error);
            Utils.showError('Failed to download PEM file');
        }
    },
    
    // Delete PEM file
    async deleteFile(fileName) {
        if (!confirm(`Are you sure you want to delete "${fileName}"?`)) {
            return;
        }
        
        try {
            Utils.showLoading();
            
            const response = await API.pemFiles.delete(fileName);
            
            if (response.success) {
                Utils.showSuccess('PEM file deleted successfully');
                await this.loadPemFiles();
            } else {
                Utils.showError(response.error || 'Failed to delete PEM file');
            }
        } catch (error) {
            console.error('Error deleting PEM file:', error);
            Utils.showError('Failed to delete PEM file');
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Show alert for in-use PEM files
    showInUseAlert(operation, fileName, hostCount) {
        const operationText = operation === 'delete' ? 'delete' : 'rename';
        const hostText = hostCount === 1 ? 'host' : 'hosts';
        
        // Use SweetAlert if available, otherwise use standard alert
        if (typeof Swal !== 'undefined') {
            Swal.fire({
                icon: 'warning',
                title: 'Operation Not Allowed',
                text: `Cannot ${operationText} "${fileName}" because it is currently used by ${hostCount} ${hostText}. Please update or remove the host configuration(s) first.`,
                confirmButtonText: 'OK',
                confirmButtonColor: '#667eea'
            });
        } else {
            alert(`Cannot ${operationText} "${fileName}" because it is currently used by ${hostCount} ${hostText}. Please update or remove the host configuration(s) first.`);
        }
    },
    
    // Show rename modal
    showRenameModal(fileName) {
        $('#pemCurrentName').val(fileName);
        // Remove .pem extension for editing
        const nameWithoutExt = fileName.replace(/\.pem$/i, '');
        $('#pemNewName').val(nameWithoutExt);
        $('#pemRenameModal').modal('show');
        // Store current filename for rename operation
        this.currentRenameFile = fileName;
    },
    
    // Rename PEM file
    async renameFile() {
        try {
            const newName = $('#pemNewName').val().trim();
            
            if (!newName) {
                Utils.showError('Please enter a new name');
                return;
            }
            
            // Ensure .pem extension
            let finalNewName = newName;
            if (!finalNewName.endsWith('.pem')) {
                finalNewName += '.pem';
            }
            
            // Check if new name is same as old name
            if (finalNewName === this.currentRenameFile) {
                Utils.showError('New name is the same as the current name');
                return;
            }
            
            Utils.showLoading();
            
            const response = await API.pemFiles.rename(this.currentRenameFile, finalNewName);
            
            if (response.success) {
                Utils.showSuccess('PEM file renamed successfully');
                $('#pemRenameModal').modal('hide');
                await this.loadPemFiles();
            } else {
                Utils.showError(response.error || 'Failed to rename PEM file');
            }
        } catch (error) {
            console.error('Error renaming PEM file:', error);
            Utils.showError('Failed to rename PEM file');
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Cleanup
    cleanup() {
        // Clean up any event listeners or timers
    }
};

// Make component globally available
window.PemFiles = PemFiles;
