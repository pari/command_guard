// Authentication Module
const Auth = {
	currentUser: null,
    // Check if user is authenticated
    isAuthenticated() {
        const token = API.getToken();
        return token !== null && token !== '';
    },
    
    // Validate current token
    async validateToken() {
        if (!this.isAuthenticated()) {
            return false;
        }
        
        try {
			const response = await API.auth.validate();
			if (response.success && response.data && response.data.valid) {
				this.currentUser = response.data;
				return true;
			}
			this.currentUser = null;
			return false;
        } catch (error) {
            console.error('Token validation failed:', error);
            return false;
        }
    },
    
    // Initialize authentication check
    async init() {
        // Check if we're on the login page
        if (window.location.pathname.includes('login.html')) {
            return;
        }
        
        // Validate token
        const isValid = await this.validateToken();
        
        if (!isValid) {
            // Redirect to login
            window.location.href = '/login.html';
			return false;
        }
		this.applyRoleNavigation();
		return true;
    },

	isAdmin() { return this.currentUser?.role === 'admin'; },

	applyRoleNavigation() {
		const admin = this.isAdmin();
		$('[data-admin-only]').toggleClass('d-none', !admin);
		$('#currentIdentity').text(this.currentUser ? `${this.currentUser.username} · ${this.currentUser.role}` : '');
		if (window.Logs) Logs.allowDestructiveActions = admin;
	},
    
    // Logout
    async logout() {
        try {
            await API.auth.logout();
        } catch (error) {
            // Even if logout fails, clear token and redirect
            API.removeToken();
            window.location.href = '/login.html';
        }
    },
    
    // Auto-refresh token (called periodically)
    async refreshTokenIfNeeded() {
        if (!this.isAuthenticated()) {
            return;
        }
        
        try {
            const response = await API.auth.validate();
            if (response.data && response.data.expires_at) {
                const expiresAt = new Date(response.data.expires_at);
                const now = new Date();
                const hoursUntilExpiry = (expiresAt - now) / (1000 * 60 * 60);
                
                // If less than 2 hours until expiry, prompt user to re-login
                if (hoursUntilExpiry < 2) {
                    if (confirm('Your session will expire soon. Would you like to continue working?')) {
                        // In a real implementation, we'd refresh the token here
                        // For now, we'll just redirect to login
                        this.logout();
                    }
                }
            }
        } catch (error) {
            console.error('Failed to check token expiry:', error);
        }
    },
    
    // Setup periodic token validation
    setupTokenValidation() {
        // Check token every 30 minutes
        setInterval(() => {
            this.refreshTokenIfNeeded();
        }, 30 * 60 * 1000);
    }
};
// Make Auth globally available
window.Auth = Auth;
