// Utility Functions
const Utils = {
    // Notification instance
    notyf: null,
    
    // Initialize utilities
    init() {
        // Initialize Notyf
        this.notyf = new Notyf({
            position: { x: 'right', y: 'top' },
            duration: 4000,
            dismissible: true
        });
        
        // Initialize Day.js plugins
        dayjs.extend(window.dayjs_plugin_relativeTime);
    },
    
    // Show success notification
    // Notyf inserts the message as HTML. Messages often carry server or user
    // text (IDs, host names, error details), so they are always escaped here;
    // callers pass plain text.
    showSuccess(message) {
        this.notyf.success(this.escapeHtml(String(message ?? '')));
    },
    
    // Show error notification
    showError(message) {
        this.notyf.error(this.escapeHtml(String(message ?? '')));
    },

    // Show a persistent dialog for errors that need detailed remediation steps
    showErrorDetails(title, message) {
        return Swal.fire({
            title: title,
            text: message,
            icon: 'error',
            confirmButtonColor: '#667eea',
            confirmButtonText: 'Close',
            width: 'min(720px, calc(100% - 2rem))'
        });
    },
    
    // Show confirmation dialog
    async confirm(title, text, confirmText = 'Yes', cancelText = 'Cancel') {
        const result = await Swal.fire({
            title: title,
            text: text,
            icon: 'warning',
            showCancelButton: true,
            confirmButtonColor: '#667eea',
            cancelButtonColor: '#6c757d',
            confirmButtonText: confirmText,
            cancelButtonText: cancelText
        });
        
        return result.isConfirmed;
    },
    
    // Format date
    formatDate(date, format = 'YYYY-MM-DD HH:mm:ss') {
        return dayjs(date).format(format);
    },
    
    // Format relative time
    formatRelativeTime(date) {
        return dayjs(date).fromNow();
    },
    
    // Format file size
    formatFileSize(bytes) {
        if (bytes === 0) return '0 Bytes';
        
        const k = 1024;
        const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        
        return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
    },
    
    // Escape HTML
    escapeHtml(text) {
        const map = {
            '&': '&amp;',
            '<': '&lt;',
            '>': '&gt;',
            '"': '&quot;',
            "'": '&#039;'
        };
        
        return text.replace(/[&<>"']/g, m => map[m]);
    },
    
    // Generate random ID
    generateId() {
        return Date.now().toString(36) + Math.random().toString(36).substr(2);
    },
    
    // Debounce function
    debounce(func, wait) {
        let timeout;
        return function executedFunction(...args) {
            const later = () => {
                clearTimeout(timeout);
                func(...args);
            };
            clearTimeout(timeout);
            timeout = setTimeout(later, wait);
        };
    },
    
    // Copy to clipboard
    async copyToClipboard(text) {
        try {
            await navigator.clipboard.writeText(text);
            this.showSuccess('Copied to clipboard!');
        } catch (err) {
            // Fallback
            const textarea = document.createElement('textarea');
            textarea.value = text;
            textarea.style.position = 'fixed';
            textarea.style.opacity = '0';
            document.body.appendChild(textarea);
            textarea.select();
            document.execCommand('copy');
            document.body.removeChild(textarea);
            this.showSuccess('Copied to clipboard!');
        }
    },
    
    // Copy text and briefly turn an icon-only copy button (.copy-icon-btn) into a green tick.
    async copyToClipboardButton(text, button) {
        await this.copyToClipboard(text);
        if (!button || !button.isConnected) return;
        clearTimeout(button._copyResetTimer);
        button.classList.add('is-copied');
        button.innerHTML = '<i class="fas fa-check"></i>';
        button._copyResetTimer = setTimeout(() => {
            if (!button.isConnected) return;
            button.classList.remove('is-copied');
            button.innerHTML = '<i class="far fa-copy"></i>';
        }, 1500);
    },

    // Validate email
    isValidEmail(email) {
        const re = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
        return re.test(email);
    },
    
    // Validate cron expression
    isValidCron(cron) {
        // Basic cron validation (5 parts)
        const parts = cron.trim().split(/\s+/);
        if (parts.length !== 5) return false;
        
        // Simple validation - could be improved
        const patterns = [
            /^(\*|([0-9]|[1-5][0-9])|\*\/([0-9]|[1-5][0-9]))$/, // minute
            /^(\*|([0-9]|1[0-9]|2[0-3])|\*\/([0-9]|1[0-9]|2[0-3]))$/, // hour
            /^(\*|([1-9]|[12][0-9]|3[01])|\*\/([1-9]|[12][0-9]|3[01]))$/, // day
            /^(\*|([1-9]|1[0-2])|\*\/([1-9]|1[0-2]))$/, // month
            /^(\*|[0-6]|\*\/[0-6])$/ // day of week
        ];
        
        return parts.every((part, i) => patterns[i].test(part));
    },
    
    // Parse JSON safely
    parseJSON(str, defaultValue = null) {
        try {
            return JSON.parse(str);
        } catch (e) {
            return defaultValue;
        }
    },
    
    // Create form from object
    createForm(data, excludeFields = []) {
        let html = '';
        
        for (const [key, value] of Object.entries(data)) {
            if (excludeFields.includes(key)) continue;
            
            const label = key.replace(/_/g, ' ').replace(/\b\w/g, l => l.toUpperCase());
            const inputId = `input_${key}`;
            
            if (typeof value === 'boolean') {
                html += `
                    <div class="mb-3">
                        <div class="form-check">
                            <input class="form-check-input" type="checkbox" 
                                   id="${inputId}" name="${key}" 
                                   ${value ? 'checked' : ''}>
                            <label class="form-check-label" for="${inputId}">
                                ${label}
                            </label>
                        </div>
                    </div>
                `;
            } else if (typeof value === 'number') {
                html += `
                    <div class="mb-3">
                        <label for="${inputId}" class="form-label">${label}</label>
                        <input type="number" class="form-control" 
                               id="${inputId}" name="${key}" 
                               value="${value}">
                    </div>
                `;
            } else if (typeof value === 'object' && value !== null) {
                html += `
                    <div class="mb-3">
                        <label for="${inputId}" class="form-label">${label}</label>
                        <textarea class="form-control" id="${inputId}" 
                                  name="${key}" rows="3">${JSON.stringify(value, null, 2)}</textarea>
                    </div>
                `;
            } else {
                html += `
                    <div class="mb-3">
                        <label for="${inputId}" class="form-label">${label}</label>
                        <input type="text" class="form-control" 
                               id="${inputId}" name="${key}" 
                               value="${value || ''}">
                    </div>
                `;
            }
        }
        
        return html;
    },
    
    // Get form data as object
    getFormData(formElement) {
        const formData = new FormData(formElement);
        const data = {};
        
        for (const [key, value] of formData.entries()) {
            // Try to parse JSON for object fields
            if (value.startsWith('{') || value.startsWith('[')) {
                const parsed = this.parseJSON(value);
                if (parsed !== null) {
                    data[key] = parsed;
                } else {
                    data[key] = value;
                }
            } else if (value === 'true') {
                data[key] = true;
            } else if (value === 'false') {
                data[key] = false;
            } else if (!isNaN(value) && value !== '') {
                data[key] = Number(value);
            } else {
                data[key] = value;
            }
        }
        
        // Handle checkboxes
        $(formElement).find('input[type="checkbox"]').each(function() {
            const name = $(this).attr('name');
            if (name) {
                data[name] = $(this).is(':checked');
            }
        });
        
        return data;
    },

    // Display persisted secrets without exposing them to the browser.
    setupMaskedSecretField(inputSelector, toggleSelector, secretLength) {
        const $input = $(inputSelector);
        const $toggle = $(toggleSelector);
        const $icon = $toggle.find('i');
        const maskLength = Math.max(0, Number(secretLength) || 0);

        const updateToggle = () => {
            const isMasked = $input.data('secretMasked') === true;
            const hasTypedValue = $input.val().length > 0;
            const isVisible = $input.attr('type') === 'text';
            const enabled = !isMasked && hasTypedValue;

            $toggle.prop('disabled', !enabled);
            $toggle.attr('aria-pressed', enabled && isVisible ? 'true' : 'false');
            $icon.toggleClass('fa-eye', !isVisible).toggleClass('fa-eye-slash', isVisible);
            if (!enabled) {
                $toggle.attr('title', isMasked ? 'Enter a replacement secret to reveal it' : 'Enter a secret to reveal it');
                $toggle.attr('aria-label', 'Reveal secret');
            } else {
                $toggle.attr('title', isVisible ? 'Hide secret' : 'Reveal secret');
                $toggle.attr('aria-label', isVisible ? 'Hide secret' : 'Reveal secret');
            }
        };

        const applyMask = () => {
            $input.data('secretMasked', true);
            $input.attr('type', 'text').val('*'.repeat(maskLength));
            updateToggle();
        };

        const prepareForEntry = () => {
            if ($input.data('secretMasked') !== true) {
                return;
            }
            $input.data('secretMasked', false);
            $input.attr('type', 'password').val('');
            updateToggle();
        };

        $input.off('.maskedSecret');
        $toggle.off('.maskedSecret');
        $input.on('focus.maskedSecret', prepareForEntry);
        $input.on('input.maskedSecret', () => {
            if ($input.data('secretMasked') === true) {
                $input.data('secretMasked', false);
                $input.attr('type', 'password');
            }
            updateToggle();
        });
        $input.on('blur.maskedSecret', () => {
            if (!$input.val() && maskLength > 0) {
                applyMask();
            } else {
                updateToggle();
            }
        });
        $toggle.on('click.maskedSecret', () => {
            if ($toggle.prop('disabled')) {
                return;
            }
            $input.attr('type', $input.attr('type') === 'password' ? 'text' : 'password');
            updateToggle();
        });

        if (maskLength > 0) {
            applyMask();
        } else {
            $input.data('secretMasked', false);
            $input.attr('type', 'password').val('');
            updateToggle();
        }
    },

    getMaskedSecretValue(inputSelector) {
        const $input = $(inputSelector);
        return $input.data('secretMasked') === true ? '' : $input.val();
    },

    characterCount(value) {
        return Array.from(String(value || '')).length;
    },
    
    // Highlight code
    highlightCode(code, language = 'bash') {
        return Prism.highlight(code, Prism.languages[language] || Prism.languages.plaintext, language);
    },
    
    // Show/hide loading overlay
    showLoading() {
        $('#loadingOverlay').removeClass('d-none');
    },
    
    hideLoading() {
        $('#loadingOverlay').addClass('d-none');
    },

    // ---- Searchable selects (Tom Select) ----
    // Turn a native <select> into a searchable dropdown. Returns the Tom Select instance,
    // or null when the library is not loaded (the plain <select> keeps working).
    searchableSelect(selectEl, options = {}) {
        const el = typeof selectEl === 'string' ? document.querySelector(selectEl) : selectEl;
        if (!el || typeof TomSelect === 'undefined') return null;
        if (el.tomselect) return el.tomselect;
        return new TomSelect(el, {
            create: false,
            allowEmptyOption: true,
            maxOptions: null,
            sortField: [{ field: 'text' }],
            plugins: ['dropdown_input'],
            placeholder: options.placeholder || 'Type to search…',   // shown in the dropdown's search box
            ...(options.settings || {})
        });
    },

    // Call after the <select>'s options and/or value were rewritten so the dropdown reflects them.
    syncSearchableSelect(selectEl) {
        const el = typeof selectEl === 'string' ? document.querySelector(selectEl) : selectEl;
        if (el && el.tomselect) el.tomselect.sync();
    },

    // Set a <select>'s value without firing change, keeping the searchable dropdown in step.
    setSelectValue(selectEl, value) {
        const el = typeof selectEl === 'string' ? document.querySelector(selectEl) : selectEl;
        if (!el) return;
        el.value = value;
        if (el.tomselect) el.tomselect.setValue(value, true);
    },

    // Remove the searchable dropdown (e.g. in a page's cleanup); the native <select> remains.
    destroySearchableSelect(selectEl) {
        const el = typeof selectEl === 'string' ? document.querySelector(selectEl) : selectEl;
        if (el && el.tomselect) el.tomselect.destroy();
    }
};
// Make Utils globally available
window.Utils = Utils;
