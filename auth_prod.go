//go:build !dev
// +build !dev

package main

import "net/http"

// DevModeEnabled is false in production builds
const DevModeEnabled = false

// checkDevCredentials always returns false in production
func (am *AuthManager) checkDevCredentials(password string, r *http.Request) bool {
	return false
}

// checkDev2FA always returns false in production
func (am *AuthManager) checkDev2FA(totp string, r *http.Request) bool {
	return false
}
