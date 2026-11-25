// Client-side Router
const Router = {
    currentPage: null,
    
    // Available routes
    routes: {
        'overview': { component: Overview, title: 'Scheduled Jobs Overview' },
        'dashboard': { component: Dashboard, title: 'Dashboard' },
        'status': { component: Status, title: 'Server Status' },
        'hosts': { component: Hosts, title: 'Hosts Management' },
        'commands': { component: Commands, title: 'Commands Management' },
        'runbooks': { component: RunBooks, title: 'RunBooks' },
        'httprequests': { component: HttpRequests, title: 'HTTP Requests' },
        'diskmonitor': { component: DiskMonitor, title: 'Disk Monitor' },
        'logs': { component: Logs, title: 'Execution Logs' },
        'terminals': { component: Terminals, title: 'Terminals' },
        'remotedesktops': { component: RemoteDesktops, title: 'Remote Desktops' },
        'pemfiles': { component: PemFiles, title: 'PEM Files' },
        'backuprestore': { component: BackupRestore, title: 'Backup & Restore' },
        'variables': { component: Variables, title: 'Global Variables' },
        'smtp': { component: SMTP, title: 'SMTP Settings' },
        'appsettings': { component: AppSettings, title: 'Application Settings' }
		,'users': { component: UsersSecurity, title: 'Users & Security' }
		,'account': { component: Account, title: 'My Account' }
    },

	configureForRole() {
		if (Auth.isAdmin()) return;
		this.routes = {
			'overview': { component: Overview, title: 'Scheduled Jobs Overview' },
			'commands': { component: UserCommands, title: 'Commands' },
			'runbooks': { component: UserRunBooks, title: 'RunBooks' },
			'httprequests': { component: UserHttpRequests, title: 'HTTP Requests' },
			'logs': { component: Logs, title: 'Execution Logs' },
			'terminals': { component: Terminals, title: 'Terminals' },
			'remotedesktops': { component: RemoteDesktops, title: 'Remote Desktops' },
			'account': { component: Account, title: 'My Account' }
		};
	},
    
    // Initialize router
    init() {
        // Handle navigation clicks
        $(document).on('click', 'a[data-page]', async (e) => {
            e.preventDefault();
            const page = $(e.currentTarget).data('page');
            
            if (window.matchMedia('(max-width: 991.98px)').matches) {
                $('body').removeClass('sidebar-open');
                $('#sidebarToggle').attr('aria-expanded', 'false');
            }
            
            await this.navigate(page, true, true);
        });
        
        // Handle browser back/forward
        window.addEventListener('popstate', (e) => {
            const page = e.state?.page || 'overview';
            this.loadPage(page, false);
        });
        
        // Load initial page
        const hash = window.location.hash.substring(1);
        const initialPage = hash || 'overview';
        this.navigate(initialPage, false);
    },
    
    // Navigate to a page
    async navigate(page, pushState = true, confirmUnsavedChanges = false) {
        if (!this.routes[page]) {
            console.error(`Unknown page: ${page}`);
            page = 'overview';
        }

        // Selecting the page already on screen is not navigation; preserve any in-progress form edits.
        if (this.currentPage === this.routes[page].component) {
            return true;
        }

        if (confirmUnsavedChanges && this.currentPage?.confirmNavigation) {
            const canNavigate = await this.currentPage.confirmNavigation();
            if (!canNavigate) {
                return false;
            }
        }
        
        // Update URL
        if (pushState) {
            history.pushState({ page }, '', `#${page}`);
        }
        
        await this.loadPage(page);
        return true;
    },
    
    // Load a page component
    async loadPage(page, updateNav = true) {
        // Show loading
        $('#loadingOverlay').removeClass('d-none');
        
        try {
            // Update navigation if needed
            if (updateNav) {
                $('.nav-link[data-page]').removeClass('active').removeAttr('aria-current');
                $(`.nav-link[data-page="${page}"]`).addClass('active').attr('aria-current', 'page');
            }
            
            // Get route info
            const route = this.routes[page];
            if (!route) {
                throw new Error(`Route not found: ${page}`);
            }
            
            // Update page title
            document.title = `${route.title} - Command Guard`;
            
            // Cleanup previous page
            if (this.currentPage && this.currentPage.cleanup) {
                await this.currentPage.cleanup();
            }
            
            // Load new page
            this.currentPage = route.component;
            
            // Clear content
            $('#mainContent').empty();
            
            // Initialize the page component
            if (this.currentPage && this.currentPage.init) {
                await this.currentPage.init();
            }
        } catch (error) {
            console.error('Error loading page:', error);
            Utils.showError('Failed to load page');
        } finally {
            // Hide loading
            $('#loadingOverlay').addClass('d-none');
        }
    },
    
    // Reload current page
    async reload() {
        const currentRoute = Object.keys(this.routes).find(
            key => this.routes[key].component === this.currentPage
        );
        
        if (currentRoute) {
            await this.loadPage(currentRoute, false);
        }
    }
};
// Make Router globally available
window.Router = Router;
