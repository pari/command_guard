package main

import "testing"

func TestSanitizeConfigSecretMetadata(t *testing.T) {
	config := &Config{
		AdminPassword: "admin-secret",
		TOTPSecret:    "ADMIN-TOTP",
		JWTSecret:     "jwt-secret",
		Users:         []User{{Username: "operator", PasswordHash: "bcrypt-hash", TOTPSecret: "USER-TOTP"}},
		SMTP:          &SMTPConfig{Password: "smtp😀"},
		S3:            &S3Config{SecretAccessKey: "s3🔑"},
	}

	sanitized := (&ConfigAPI{}).sanitizeConfig(config)
	for _, field := range []string{"admin_password", "totp_secret", "jwt_secret"} {
		if _, exists := sanitized[field]; exists {
			t.Fatalf("sanitized configuration exposed %s", field)
		}
	}
	users, ok := sanitized["users"].([]interface{})
	if !ok || len(users) != 1 {
		t.Fatal("expected sanitized user metadata")
	}
	user, _ := users[0].(map[string]interface{})
	if _, exists := user["password_hash"]; exists {
		t.Fatal("sanitized user exposed password hash")
	}
	if _, exists := user["totp_secret"]; exists {
		t.Fatal("sanitized user exposed TOTP secret")
	}
	smtp, ok := sanitized["smtp"].(map[string]interface{})
	if !ok {
		t.Fatal("expected sanitized SMTP configuration")
	}
	if _, exists := smtp["password"]; exists {
		t.Fatal("sanitized SMTP configuration exposed password")
	}
	if got, ok := smtp["password_length"].(int); !ok || got != 5 {
		t.Fatalf("password_length = %v, want 5", smtp["password_length"])
	}

	s3, ok := sanitized["s3"].(map[string]interface{})
	if !ok {
		t.Fatal("expected sanitized S3 configuration")
	}
	if _, exists := s3["secret_access_key"]; exists {
		t.Fatal("sanitized S3 configuration exposed secret access key")
	}
	if got, ok := s3["secret_access_key_length"].(int); !ok || got != 3 {
		t.Fatalf("secret_access_key_length = %v, want 3", s3["secret_access_key_length"])
	}
}
