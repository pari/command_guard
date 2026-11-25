// Variables Component
const Variables = {
    currentVariables: {},
    
    // Initialize variables page
    async init() {
        try {
            this.render();
            await this.loadVariables();
        } catch (error) {
            console.error('Variables init error:', error);
            Utils.showError('Failed to load variables');
        }
    },
    
    // Render variables layout
    render() {
        const html = `
            <div class="page-header">
                <h2><i class="fas fa-cogs"></i> Global Variables</h2>
                <button class="btn btn-primary" onclick="Variables.showAddModal()">
                    <i class="fas fa-plus"></i> Add Variable
                </button>
            </div>
            
            <div class="alert alert-info">
                <i class="fas fa-info-circle"></i>
                <strong>Global Variables</strong> can be used in commands and scripts using the format: <code>{{.VariableName}}</code>
            </div>
            
            <div class="card">
                <div class="card-body">
                    <div class="table-responsive">
                        <table id="variablesTable" class="table table-striped table-hover">
                            <thead>
                                <tr>
                                    <th>Variable Name</th>
                                    <th>Value</th>
                                    <th>Type</th>
                                    <th>Description</th>
                                    <th>Usage Example</th>
                                    <th>Actions</th>
                                </tr>
                            </thead>
                            <tbody id="variablesTableBody">
                                <tr>
                                    <td colspan="6" class="text-center">
                                        <i class="fas fa-spinner fa-spin"></i> Loading variables...
                                    </td>
                                </tr>
                            </tbody>
                        </table>
                    </div>
                </div>
            </div>
            
            <!-- Add/Edit Variable Modal -->
            <div class="modal fade" id="variableModal" tabindex="-1">
                <div class="modal-dialog modal-lg">
                    <div class="modal-content">
                        <div class="modal-header">
                            <h5 class="modal-title" id="variableModalTitle">Add Variable</h5>
                            <button type="button" class="btn-close" data-bs-dismiss="modal"></button>
                        </div>
                        <div class="modal-body">
                            <form id="variableForm">
                                <div class="row">
                                    <div class="col-md-6">
                                        <div class="mb-3">
                                            <label class="form-label">Variable Name *</label>
                                            <input type="text" class="form-control" id="varName" required 
                                                   pattern="[A-Za-z][A-Za-z0-9_]*" placeholder="MyVariable">
                                            <div class="form-text">Use only letters, numbers, and underscores. Must start with a letter.</div>
                                        </div>
                                    </div>
                                    <div class="col-md-6">
                                        <div class="mb-3">
                                            <label class="form-label">Variable Type</label>
                                            <select class="form-select" id="varType" onchange="Variables.toggleValueField()">
                                                <option value="string">String</option>
                                                <option value="number">Number</option>
                                                <option value="boolean">Boolean</option>
                                                <option value="json">JSON Object</option>
                                                <option value="secret">Secret</option>
                                            </select>
                                        </div>
                                    </div>
                                </div>
                                
                                <div class="mb-3">
                                    <label class="form-label">Description</label>
                                    <input type="text" class="form-control" id="varDescription" 
                                           placeholder="Brief description of this variable">
                                </div>
                                
                                <div class="mb-3" id="stringValueField">
                                    <label class="form-label">Value *</label>
                                    <input type="text" class="form-control" id="varValue" required>
                                </div>
                                
                                <div class="mb-3" id="textareaValueField" style="display: none;">
                                    <label class="form-label">Value *</label>
                                    <textarea class="form-control" id="varValueTextarea" rows="4"></textarea>
                                    <div class="form-text">For JSON objects, enter valid JSON format</div>
                                </div>
                                
                                <div class="mb-3" id="numberValueField" style="display: none;">
                                    <label class="form-label">Value *</label>
                                    <input type="number" class="form-control" id="varValueNumber" step="any">
                                </div>
                                
                                <div class="mb-3" id="booleanValueField" style="display: none;">
                                    <label class="form-label">Value *</label>
                                    <select class="form-select" id="varValueBoolean">
                                        <option value="true">True</option>
                                        <option value="false">False</option>
                                    </select>
                                </div>
                                
                                <div class="mb-3" id="secretValueField" style="display: none;">
                                    <label class="form-label">Secret Value *</label>
                                    <input type="password" class="form-control" id="varValueSecret">
                                    <div class="form-text">Secret values are encrypted and not visible in the UI</div>
                                </div>
                                
                                <div class="mb-3">
                                    <label class="form-label">Usage Example</label>
                                    <div class="bg-light p-2 rounded">
                                        <code id="usageExample">{{.VariableName}}</code>
                                    </div>
                                </div>
                                
                                <div class="row">
                                    <div class="col-md-6">
                                        <div class="mb-3">
                                            <div class="form-check">
                                                <input class="form-check-input" type="checkbox" id="varReadOnly">
                                                <label class="form-check-label">Read Only</label>
                                                <div class="form-text">Prevent modification through API</div>
                                            </div>
                                        </div>
                                    </div>
                                    <div class="col-md-6">
                                        <div class="mb-3">
                                            <div class="form-check">
                                                <input class="form-check-input" type="checkbox" id="varGlobal" checked>
                                                <label class="form-check-label">Global Scope</label>
                                                <div class="form-text">Available to all commands</div>
                                            </div>
                                        </div>
                                    </div>
                                </div>
                            </form>
                        </div>
                        <div class="modal-footer">
                            <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Cancel</button>
                            <button type="button" class="btn btn-primary" onclick="Variables.saveVariable()">
                                <i class="fas fa-save"></i> Save Variable
                            </button>
                        </div>
                    </div>
                </div>
            </div>
            
            <!-- Import Variables Modal -->
            <div class="modal fade" id="importModal" tabindex="-1">
                <div class="modal-dialog">
                    <div class="modal-content">
                        <div class="modal-header">
                            <h5 class="modal-title">Import Variables</h5>
                            <button type="button" class="btn-close" data-bs-dismiss="modal"></button>
                        </div>
                        <div class="modal-body">
                            <div class="mb-3">
                                <label class="form-label">Import Format</label>
                                <select class="form-select" id="importFormat">
                                    <option value="json">JSON</option>
                                    <option value="env">Environment (.env)</option>
                                    <option value="yaml">YAML</option>
                                </select>
                            </div>
                            <div class="mb-3">
                                <label class="form-label">Variables Data</label>
                                <textarea class="form-control" id="importData" rows="10" 
                                          placeholder="Paste your variables here..."></textarea>
                            </div>
                            <div class="form-check">
                                <input class="form-check-input" type="checkbox" id="overwriteExisting">
                                <label class="form-check-label">Overwrite existing variables</label>
                            </div>
                        </div>
                        <div class="modal-footer">
                            <button type="button" class="btn btn-secondary" data-bs-dismiss="modal">Cancel</button>
                            <button type="button" class="btn btn-primary" onclick="Variables.importVariables()">
                                <i class="fas fa-upload"></i> Import
                            </button>
                        </div>
                    </div>
                </div>
            </div>
            
            <!-- Additional Actions -->
            <div class="mt-3">
                <div class="btn-group">
                    <button class="btn btn-outline-success" onclick="Variables.showImportModal()">
                        <i class="fas fa-upload"></i> Import Variables
                    </button>
                    <button class="btn btn-outline-primary" onclick="Variables.exportVariables()">
                        <i class="fas fa-download"></i> Export Variables
                    </button>
                    <button class="btn btn-outline-info" onclick="Variables.showSystemVariables()">
                        <i class="fas fa-info"></i> System Variables
                    </button>
                </div>
            </div>
        `;
        
        $('#mainContent').html(html);
    },
    
    // Load variables
    async loadVariables() {
        try {
            const response = await API.config.get();
            
            if (response.success && response.data) {
                this.currentVariables = response.data.variables || {};
                this.renderVariablesTable();
            } else {
                Utils.showError('Failed to load variables configuration');
            }
        } catch (error) {
            console.error('Error loading variables:', error);
            Utils.showError('Failed to load variables');
        }
    },
    
    // Render variables table
    renderVariablesTable() {
        const tbody = $('#variablesTableBody');
        let html = '';
        
        if (Object.keys(this.currentVariables).length === 0) {
            html = `
                <tr>
                    <td colspan="6" class="text-center text-muted">
                        No variables configured yet.
                        <br><br>
                        <button class="btn btn-primary" onclick="Variables.showAddModal()">
                            <i class="fas fa-plus"></i> Add Your First Variable
                        </button>
                    </td>
                </tr>
            `;
        } else {
            Object.entries(this.currentVariables).forEach(([name, varData]) => {
                const value = this.formatVariableValue(varData);
                const type = this.getVariableType(varData);
                const isSecret = type === 'secret';
                
                html += `
                    <tr>
                        <td>
                            <strong>${name}</strong>
                            ${varData.read_only ? '<span class="badge bg-warning ms-1">Read Only</span>' : ''}
                        </td>
                        <td>
                            ${isSecret ? 
                                '<span class="text-muted"><i class="fas fa-eye-slash"></i> Hidden</span>' : 
                                `<code class="text-break">${Utils.escapeHtml(value)}</code>`
                            }
                        </td>
                        <td>
                            <span class="badge ${this.getTypeBadgeClass(type)}">${type.toUpperCase()}</span>
                        </td>
                        <td>${varData.description || '<span class="text-muted">No description</span>'}</td>
                        <td>
                            <code class="small">{{.${name}}}</code>
                        </td>
                        <td>
                            <div class="btn-group btn-group-sm">
                                <button class="btn btn-outline-primary" onclick="Variables.editVariable('${name}')" title="Edit">
                                    <i class="fas fa-edit"></i>
                                </button>
                                <button class="btn btn-outline-info" onclick="Variables.testVariable('${name}')" title="Test Usage">
                                    <i class="fas fa-vial"></i>
                                </button>
                                <button class="btn btn-outline-danger" onclick="Variables.deleteVariable('${name}')" title="Delete">
                                    <i class="fas fa-trash"></i>
                                </button>
                            </div>
                        </td>
                    </tr>
                `;
            });
        }
        
        tbody.html(html);
    },
    
    // Format variable value for display
    formatVariableValue(varData) {
        if (typeof varData === 'string') {
            return varData.length > 50 ? varData.substring(0, 50) + '...' : varData;
        }
        
        if (varData.type === 'secret') {
            return '***hidden***';
        }
        
        const value = varData.value || varData;
        if (typeof value === 'object') {
            const jsonStr = JSON.stringify(value, null, 2);
            return jsonStr.length > 100 ? JSON.stringify(value) : jsonStr;
        }
        
        return String(value);
    },
    
    // Get variable type
    getVariableType(varData) {
        if (typeof varData === 'string') return 'string';
        if (varData.type) return varData.type;
        if (typeof varData.value === 'number') return 'number';
        if (typeof varData.value === 'boolean') return 'boolean';
        if (typeof varData.value === 'object') return 'json';
        return 'string';
    },
    
    // Get type badge CSS class
    getTypeBadgeClass(type) {
        const classes = {
            string: 'bg-primary',
            number: 'bg-success',
            boolean: 'bg-info',
            json: 'bg-warning',
            secret: 'bg-danger'
        };
        return classes[type] || 'bg-secondary';
    },
    
    // Show add modal
    showAddModal() {
        $('#variableModalTitle').text('Add New Variable');
        $('#variableForm')[0].reset();
        $('#varName').prop('readonly', false);
        $('#varType').val('string');
        $('#varGlobal').prop('checked', true);
        this.toggleValueField();
        this.updateUsageExample();
        $('#variableModal').modal('show');
    },
    
    // Edit variable
    editVariable(name) {
        const varData = this.currentVariables[name];
        if (!varData) return;
        
        $('#variableModalTitle').text('Edit Variable');
        $('#varName').val(name).prop('readonly', true);
        $('#varDescription').val(varData.description || '');
        
        const type = this.getVariableType(varData);
        $('#varType').val(type);
        
        // Set value based on type
        const value = varData.value !== undefined ? varData.value : varData;
        
        switch (type) {
            case 'number':
                $('#varValueNumber').val(value);
                break;
            case 'boolean':
                $('#varValueBoolean').val(String(value));
                break;
            case 'json':
                $('#varValueTextarea').val(typeof value === 'object' ? JSON.stringify(value, null, 2) : value);
                break;
            case 'secret':
                $('#varValueSecret').val(''); // Don't populate secret values
                break;
            default:
                $('#varValue').val(value);
                break;
        }
        
        $('#varReadOnly').prop('checked', varData.read_only || false);
        $('#varGlobal').prop('checked', varData.global !== false);
        
        this.toggleValueField();
        this.updateUsageExample();
        $('#variableModal').modal('show');
    },
    
    // Toggle value field based on type
    toggleValueField() {
        const type = $('#varType').val();
        
        // Hide all value fields
        $('#stringValueField, #textareaValueField, #numberValueField, #booleanValueField, #secretValueField').hide();
        
        // Show appropriate field
        switch (type) {
            case 'number':
                $('#numberValueField').show();
                break;
            case 'boolean':
                $('#booleanValueField').show();
                break;
            case 'json':
                $('#textareaValueField').show();
                break;
            case 'secret':
                $('#secretValueField').show();
                break;
            default:
                $('#stringValueField').show();
                break;
        }
        
        this.updateUsageExample();
    },
    
    // Update usage example
    updateUsageExample() {
        const name = $('#varName').val() || 'VariableName';
        $('#usageExample').text(`{{.${name}}}`);
    },
    
    // Save variable
    async saveVariable() {
        try {
            const form = $('#variableForm')[0];
            if (!form.checkValidity()) {
                form.reportValidity();
                return;
            }
            
            const name = $('#varName').val().trim();
            const type = $('#varType').val();
            let value;
            
            // Get value based on type
            switch (type) {
                case 'number':
                    value = parseFloat($('#varValueNumber').val());
                    if (isNaN(value)) {
                        Utils.showError('Please enter a valid number');
                        return;
                    }
                    break;
                case 'boolean':
                    value = $('#varValueBoolean').val() === 'true';
                    break;
                case 'json':
                    try {
                        value = JSON.parse($('#varValueTextarea').val());
                    } catch (e) {
                        Utils.showError('Please enter valid JSON');
                        return;
                    }
                    break;
                case 'secret':
                    value = $('#varValueSecret').val();
                    if (!value && !this.currentVariables[name]) {
                        Utils.showError('Please enter a secret value');
                        return;
                    }
                    break;
                default:
                    value = $('#varValue').val();
                    break;
            }
            
            const varData = {
                value: value,
                type: type,
                global: $('#varGlobal').is(':checked')
            };
            
            const description = $('#varDescription').val().trim();
            if (description) varData.description = description;
            
            if ($('#varReadOnly').is(':checked')) {
                varData.read_only = true;
            }
            
            // Don't send empty secret values for existing variables
            if (type === 'secret' && !value && this.currentVariables[name]) {
                delete varData.value;
            }
            
            Utils.showLoading();
            
            const response = await API.config.updateVariable(name, varData);
            
            if (response.success) {
                Utils.showSuccess('Variable saved successfully');
                $('#variableModal').modal('hide');
                await this.loadVariables();
            } else {
                Utils.showError(response.error || 'Failed to save variable');
            }
        } catch (error) {
            console.error('Error saving variable:', error);
            Utils.showError('Failed to save variable');
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Test variable
    async testVariable(name) {
        try {
            const response = await API.ops.testVariable(name);
            
            if (response.success) {
                const result = response.data;
                Utils.showInfo(`Variable "${name}" resolves to: ${JSON.stringify(result.value)}`);
            } else {
                Utils.showError(response.error || 'Failed to test variable');
            }
        } catch (error) {
            console.error('Error testing variable:', error);
            Utils.showError('Failed to test variable');
        }
    },
    
    // Delete variable
    async deleteVariable(name) {
        if (!confirm(`Are you sure you want to delete variable "${name}"?`)) {
            return;
        }
        
        try {
            Utils.showLoading();
            
            const response = await API.config.deleteVariable(name);
            
            if (response.success) {
                Utils.showSuccess('Variable deleted successfully');
                await this.loadVariables();
            } else {
                Utils.showError(response.error || 'Failed to delete variable');
            }
        } catch (error) {
            console.error('Error deleting variable:', error);
            Utils.showError('Failed to delete variable');
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Show import modal
    showImportModal() {
        $('#importModal').modal('show');
    },
    
    // Import variables
    async importVariables() {
        try {
            const format = $('#importFormat').val();
            const data = $('#importData').val().trim();
            const overwrite = $('#overwriteExisting').is(':checked');
            
            if (!data) {
                Utils.showError('Please enter variables data to import');
                return;
            }
            
            let variables;
            
            try {
                switch (format) {
                    case 'json':
                        variables = JSON.parse(data);
                        break;
                    case 'env':
                        variables = this.parseEnvFormat(data);
                        break;
                    case 'yaml':
                        // Simple YAML parser for basic key-value pairs
                        variables = this.parseYamlFormat(data);
                        break;
                    default:
                        throw new Error('Unsupported format');
                }
            } catch (e) {
                Utils.showError(`Failed to parse ${format.toUpperCase()} data: ${e.message}`);
                return;
            }
            
            Utils.showLoading();
            
            const response = await API.config.importVariables(variables, overwrite);
            
            if (response.success) {
                Utils.showSuccess(`Imported ${Object.keys(variables).length} variables successfully`);
                $('#importModal').modal('hide');
                await this.loadVariables();
            } else {
                Utils.showError(response.error || 'Failed to import variables');
            }
        } catch (error) {
            console.error('Error importing variables:', error);
            Utils.showError('Failed to import variables');
        } finally {
            Utils.hideLoading();
        }
    },
    
    // Parse .env format
    parseEnvFormat(data) {
        const variables = {};
        const lines = data.split('\n');
        
        lines.forEach(line => {
            line = line.trim();
            if (line && !line.startsWith('#')) {
                const [key, ...valueParts] = line.split('=');
                if (key && valueParts.length > 0) {
                    let value = valueParts.join('=').trim();
                    // Remove quotes if present
                    if ((value.startsWith('"') && value.endsWith('"')) || 
                        (value.startsWith("'") && value.endsWith("'"))) {
                        value = value.slice(1, -1);
                    }
                    variables[key.trim()] = {
                        value: value,
                        type: 'string'
                    };
                }
            }
        });
        
        return variables;
    },
    
    // Parse simple YAML format
    parseYamlFormat(data) {
        const variables = {};
        const lines = data.split('\n');
        
        lines.forEach(line => {
            line = line.trim();
            if (line && !line.startsWith('#')) {
                const colonIndex = line.indexOf(':');
                if (colonIndex > 0) {
                    const key = line.substring(0, colonIndex).trim();
                    let value = line.substring(colonIndex + 1).trim();
                    
                    // Remove quotes
                    if ((value.startsWith('"') && value.endsWith('"')) || 
                        (value.startsWith("'") && value.endsWith("'"))) {
                        value = value.slice(1, -1);
                    }
                    
                    // Detect type
                    let type = 'string';
                    if (value === 'true' || value === 'false') {
                        type = 'boolean';
                        value = value === 'true';
                    } else if (!isNaN(value) && value !== '') {
                        type = 'number';
                        value = parseFloat(value);
                    }
                    
                    variables[key] = {
                        value: value,
                        type: type
                    };
                }
            }
        });
        
        return variables;
    },
    
    // Export variables
    exportVariables() {
        try {
            const exportData = {
                variables: this.currentVariables,
                exported_at: new Date().toISOString(),
                version: '1.0'
            };
            
            const jsonContent = JSON.stringify(exportData, null, 2);
            const blob = new Blob([jsonContent], { type: 'application/json' });
            const url = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url;
            a.download = `variables_export_${Utils.formatDate(new Date(), 'YYYY-MM-DD')}.json`;
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            URL.revokeObjectURL(url);
            
            Utils.showSuccess('Variables exported successfully');
        } catch (error) {
            console.error('Error exporting variables:', error);
            Utils.showError('Failed to export variables');
        }
    },
    
    // Show system variables
    showSystemVariables() {
        const systemVars = [
            { name: 'Date', example: '{{.Date}}', description: 'Current date (YYYY-MM-DD)' },
            { name: 'DateTime', example: '{{.DateTime}}', description: 'Current date and time' },
            { name: 'Timestamp', example: '{{.Timestamp}}', description: 'Unix timestamp' },
            { name: 'Host', example: '{{.Host}}', description: 'Current hostname' },
            { name: 'User', example: '{{.User}}', description: 'Current user' },
            { name: 'TraceID', example: '{{.TraceID}}', description: 'Execution trace ID' },
            { name: 'CmdID', example: '{{.CmdID}}', description: 'Command ID being executed' }
        ];
        
        let html = `
            <div class="alert alert-info">
                <h6><i class="fas fa-info-circle"></i> System Variables</h6>
                <p>These variables are automatically available in all commands:</p>
            </div>
            <div class="table-responsive">
                <table class="table table-sm">
                    <thead>
                        <tr>
                            <th>Variable</th>
                            <th>Usage</th>
                            <th>Description</th>
                        </tr>
                    </thead>
                    <tbody>
        `;
        
        systemVars.forEach(sysVar => {
            html += `
                <tr>
                    <td><strong>${sysVar.name}</strong></td>
                    <td><code>${sysVar.example}</code></td>
                    <td>${sysVar.description}</td>
                </tr>
            `;
        });
        
        html += `
                    </tbody>
                </table>
            </div>
        `;
        
        Utils.showInfo(html, 'System Variables', 'lg');
    },
    
    // Setup event listeners
    setupEventListeners() {
        $('#varName').on('input', () => this.updateUsageExample());
    },
    
    // Cleanup
    cleanup() {
        // Clean up any event listeners or timers
    }
};
// Make component globally available
window.Variables = Variables;
