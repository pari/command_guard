// Main Application
const App = {
    // Initialize application
    async init() {
        console.log('Initializing Command Guard Admin Panel...');
        
        // Initialize utilities
        Utils.init();
        
        // Check authentication
		const authenticated = await Auth.init();
		if (!authenticated) return;
        
        // Setup token validation
        Auth.setupTokenValidation();
        
        // Setup global event handlers
        this.setupEventHandlers();

        // Initialize router
		Router.configureForRole();
		Router.init();
        
        console.log('Application initialized successfully');
    },
    
    // Setup global event handlers
    setupEventHandlers() {
        this.setupSidebar();

        // Logout button
        $('#btnLogout').on('click', async (e) => {
            e.preventDefault();
            this.closeMobileSidebar();
            
            const confirmed = await Utils.confirm(
                'Logout',
                'Are you sure you want to logout?',
                'Yes, Logout'
            );
            
            if (confirmed) {
                await Auth.logout();
            }
        });
        
        // Handle form submissions in modals
        $('#formModalSave').on('click', () => {
            const form = $('#formModalBody form');
            if (form.length > 0) {
                form.submit();
            }
        });
        
        // Auto-close alerts
        $(document).on('click', '.alert .btn-close', function() {
            $(this).closest('.alert').fadeOut();
        });
        
        // Handle escape key
        $(document).on('keydown', (e) => {
            if (e.key === 'Escape') {
                this.closeMobileSidebar();
                $('.modal').modal('hide');
            }
        });
        
        // Handle connection errors
        $(document).ajaxError((event, jqXHR) => {
            if (jqXHR.status === 401) {
                Auth.logout();
            } else if (jqXHR.status === 0) {
                Utils.showError('Connection lost. Please check your network.');
            }
        });
    },

    // Setup collapsible sidebar
    setupSidebar() {
        const storageKey = 'commandGuardSidebarCollapsed';
        const mobileQuery = window.matchMedia('(max-width: 991.98px)');
        const body = $('body');
        const sidebarToggle = $('#sidebarToggle');
        const getSavedCollapsedState = () => {
            try {
                return localStorage.getItem(storageKey) === 'true';
            } catch (error) {
                return false;
            }
        };
        const saveCollapsedState = (collapsed) => {
            try {
                localStorage.setItem(storageKey, collapsed ? 'true' : 'false');
            } catch (error) {
                // Sidebar state persistence is optional.
            }
        };

        const updateToggleState = () => {
            const expanded = mobileQuery.matches
                ? body.hasClass('sidebar-open')
                : !body.hasClass('sidebar-collapsed');

            sidebarToggle.attr('aria-expanded', expanded ? 'true' : 'false');
        };

        this.closeMobileSidebar = () => {
            if (mobileQuery.matches) {
                body.removeClass('sidebar-open');
            }
            updateToggleState();
        };

        if (getSavedCollapsedState()) {
            body.addClass('sidebar-collapsed');
        }

        sidebarToggle.on('click', (e) => {
            e.preventDefault();

            if (mobileQuery.matches) {
                body.toggleClass('sidebar-open');
            } else {
                const shouldCollapse = !body.hasClass('sidebar-collapsed');
                body.toggleClass('sidebar-collapsed', shouldCollapse);
                saveCollapsedState(shouldCollapse);
            }

            updateToggleState();
        });

        $('#sidebarBackdrop').on('click', () => {
            this.closeMobileSidebar();
        });

        const handleViewportChange = () => {
            if (!mobileQuery.matches) {
                body.removeClass('sidebar-open');
            }

            updateToggleState();
        };

        if (mobileQuery.addEventListener) {
            mobileQuery.addEventListener('change', handleViewportChange);
        } else {
            mobileQuery.addListener(handleViewportChange);
        }

        updateToggleState();
    },

    closeMobileSidebar() {}
};

// Initialize app when DOM is ready
$(document).ready(() => {
    App.init();
});
// Make App globally available
window.App = App;
