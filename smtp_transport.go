package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"gopkg.in/gomail.v2"
)

const (
	defaultSMTPTimeout = 30
	maxSMTPBackoff     = 30 * time.Second
)

func boolPointer(value bool) *bool {
	return &value
}

func (config *SMTPConfig) isEnabled() bool {
	return config != nil && (config.Enabled == nil || *config.Enabled)
}

func (config *SMTPConfig) timeoutDuration() time.Duration {
	if config == nil || config.Timeout <= 0 {
		return defaultSMTPTimeout * time.Second
	}
	return time.Duration(config.Timeout) * time.Second
}

func (config *SMTPConfig) effectiveEncryption() string {
	if config == nil {
		return "none"
	}
	if encryption := strings.ToLower(strings.TrimSpace(config.Encryption)); encryption != "" {
		return encryption
	}
	if config.Port == 465 {
		return "ssl"
	}
	if config.TLS {
		return "tls"
	}
	return "none"
}

func (config *SMTPConfig) senderParts() (string, string, error) {
	if config == nil {
		return "", "", errors.New("SMTP configuration is not set")
	}
	if strings.TrimSpace(config.FromEmail) != "" {
		address, err := mail.ParseAddress(strings.TrimSpace(config.FromEmail))
		if err != nil || address.Address != strings.TrimSpace(config.FromEmail) {
			return "", "", fmt.Errorf("invalid SMTP from email %q", config.FromEmail)
		}
		return address.Address, strings.TrimSpace(config.FromName), nil
	}
	address, err := mail.ParseAddress(strings.TrimSpace(config.From))
	if err != nil {
		return "", "", fmt.Errorf("invalid legacy SMTP from address %q: %w", config.From, err)
	}
	return address.Address, address.Name, nil
}

func normalizeSMTPConfig(config *SMTPConfig, useNewDefaults bool) (*SMTPConfig, error) {
	wasNil := config == nil
	if config == nil {
		config = &SMTPConfig{}
	}
	normalized := *config
	normalized.Host = strings.TrimSpace(normalized.Host)
	normalized.Username = strings.TrimSpace(normalized.Username)
	normalized.FromEmail = strings.TrimSpace(normalized.FromEmail)
	normalized.FromName = strings.TrimSpace(normalized.FromName)
	if normalized.FromEmail == "" && normalized.From != "" {
		address, err := mail.ParseAddress(strings.TrimSpace(normalized.From))
		if err != nil {
			return nil, fmt.Errorf("invalid legacy SMTP from address %q: %w", normalized.From, err)
		}
		normalized.FromEmail = address.Address
		normalized.FromName = address.Name
	}
	if normalized.Encryption == "" {
		normalized.Encryption = normalized.effectiveEncryption()
	}
	normalized.Encryption = strings.ToLower(strings.TrimSpace(normalized.Encryption))
	if normalized.Enabled == nil {
		normalized.Enabled = boolPointer(true)
	}
	if normalized.Timeout == 0 {
		normalized.Timeout = defaultSMTPTimeout
	}
	if wasNil && useNewDefaults {
		normalized.Retries = 3
	}
	normalized.From = ""
	normalized.TLS = false
	return &normalized, nil
}

func validateSMTPConfig(config *SMTPConfig) error {
	if config == nil {
		return errors.New("SMTP configuration is required")
	}
	if strings.TrimSpace(config.Host) == "" {
		return errors.New("SMTP server host is required")
	}
	if config.Port < 1 || config.Port > 65535 {
		return fmt.Errorf("SMTP port must be between 1 and 65535")
	}
	if _, _, err := config.senderParts(); err != nil {
		return err
	}
	switch config.effectiveEncryption() {
	case "none", "tls", "ssl":
	default:
		return fmt.Errorf("SMTP encryption must be one of: none, tls, ssl")
	}
	if config.Timeout < 5 || config.Timeout > 300 {
		return fmt.Errorf("SMTP timeout must be between 5 and 300 seconds")
	}
	if config.Retries < 0 || config.Retries > 10 {
		return fmt.Errorf("SMTP retries must be between 0 and 10")
	}
	return nil
}

func sanitizedSMTPData(config *SMTPConfig, useNewDefaults bool) map[string]interface{} {
	normalized, err := normalizeSMTPConfig(config, useNewDefaults)
	if err != nil {
		normalized = config
	}
	if normalized == nil {
		normalized = &SMTPConfig{Enabled: boolPointer(true), Timeout: defaultSMTPTimeout, Retries: 3}
	}
	return map[string]interface{}{
		"host":             normalized.Host,
		"port":             normalized.Port,
		"username":         normalized.Username,
		"has_password":     normalized.Password != "",
		"password_length":  len([]rune(normalized.Password)),
		"from_email":       normalized.FromEmail,
		"from_name":        normalized.FromName,
		"encryption":       normalized.effectiveEncryption(),
		"enabled":          normalized.isEnabled(),
		"timeout":          normalized.Timeout,
		"retries":          normalized.Retries,
		"skip_cert_verify": normalized.SkipCertVerify,
	}
}

func (config *SMTPConfig) setFromHeader(message *gomail.Message) error {
	email, name, err := config.senderParts()
	if err != nil {
		return err
	}
	if name == "" {
		message.SetHeader("From", email)
	} else {
		message.SetAddressHeader("From", email, name)
	}
	return nil
}

type configuredSMTPSender struct {
	client *smtp.Client
}

func (sender *configuredSMTPSender) Send(from string, to []string, message io.WriterTo) error {
	if err := sender.client.Mail(from); err != nil {
		return err
	}
	for _, recipient := range to {
		if err := sender.client.Rcpt(recipient); err != nil {
			return err
		}
	}
	writer, err := sender.client.Data()
	if err != nil {
		return err
	}
	if _, err := message.WriteTo(writer); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}

func (sender *configuredSMTPSender) Close() error {
	if err := sender.client.Quit(); err != nil {
		_ = sender.client.Close()
		return err
	}
	return nil
}

type smtpLoginAuth struct {
	username string
	password string
	host     string
}

func (auth *smtpLoginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if server.Name != auth.host {
		return "", nil, errors.New("SMTP authentication host name mismatch")
	}
	return "LOGIN", nil, nil
}

func (auth *smtpLoginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:":
		return []byte(auth.username), nil
	case "password:":
		return []byte(auth.password), nil
	default:
		return nil, fmt.Errorf("unexpected SMTP LOGIN challenge %q", fromServer)
	}
}

func dialConfiguredSMTP(config *SMTPConfig) (gomail.SendCloser, error) {
	timeout := config.timeoutDuration()
	address := net.JoinHostPort(config.Host, fmt.Sprintf("%d", config.Port))
	connection, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return nil, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = connection.Close()
		}
	}()
	if err := connection.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		ServerName:         config.Host,
		InsecureSkipVerify: config.SkipCertVerify, // #nosec G402 -- explicitly controlled by the admin setting.
	}
	if config.effectiveEncryption() == "ssl" {
		tlsConnection := tls.Client(connection, tlsConfig)
		if err := tlsConnection.Handshake(); err != nil {
			return nil, err
		}
		connection = tlsConnection
	}

	client, err := smtp.NewClient(connection, config.Host)
	if err != nil {
		return nil, err
	}
	if config.effectiveEncryption() == "tls" {
		if supported, _ := client.Extension("STARTTLS"); !supported {
			_ = client.Close()
			return nil, errors.New("SMTP server does not support required STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			_ = client.Close()
			return nil, err
		}
	}

	if config.Username != "" {
		if supported, mechanisms := client.Extension("AUTH"); supported {
			var auth smtp.Auth
			switch {
			case strings.Contains(mechanisms, "CRAM-MD5"):
				auth = smtp.CRAMMD5Auth(config.Username, config.Password)
			case strings.Contains(mechanisms, "LOGIN") && !strings.Contains(mechanisms, "PLAIN"):
				auth = &smtpLoginAuth{username: config.Username, password: config.Password, host: config.Host}
			default:
				auth = smtp.PlainAuth("", config.Username, config.Password, config.Host)
			}
			if err := client.Auth(auth); err != nil {
				_ = client.Close()
				return nil, err
			}
		}
	}

	closeOnError = false
	return &configuredSMTPSender{client: client}, nil
}

var smtpSendAttempt = func(config *SMTPConfig, message *gomail.Message) error {
	sender, err := dialConfiguredSMTP(config)
	if err != nil {
		return err
	}
	defer sender.Close()
	from, _, err := config.senderParts()
	if err != nil {
		return err
	}
	recipients, err := smtpMessageRecipients(message)
	if err != nil {
		return err
	}
	return sender.Send(from, recipients, message)
}

func smtpMessageRecipients(message *gomail.Message) ([]string, error) {
	seen := make(map[string]bool)
	var recipients []string
	for _, header := range []string{"To", "Cc", "Bcc"} {
		for _, value := range message.GetHeader(header) {
			addresses, err := mail.ParseAddressList(value)
			if err != nil {
				return nil, fmt.Errorf("invalid SMTP %s address %q: %w", header, value, err)
			}
			for _, address := range addresses {
				if !seen[address.Address] {
					seen[address.Address] = true
					recipients = append(recipients, address.Address)
				}
			}
		}
	}
	if len(recipients) == 0 {
		return nil, errors.New("SMTP message has no recipients")
	}
	return recipients, nil
}

var waitForSMTPRetry = func(delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-appShutdown.Done():
		return errors.New("SMTP retry interrupted by shutdown")
	}
}

func retryableSMTPError(err error) bool {
	if err == nil {
		return false
	}
	var protocolError *textproto.Error
	if errors.As(err, &protocolError) {
		return protocolError.Code >= 400 && protocolError.Code < 500
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return true
	}
	return errors.Is(err, io.EOF)
}

func sendSMTPMessage(config *SMTPConfig, message *gomail.Message) error {
	if config == nil {
		return errors.New("SMTP configuration is not set")
	}
	if !config.isEnabled() {
		return errors.New("SMTP is disabled")
	}
	normalized, err := normalizeSMTPConfig(config, false)
	if err != nil {
		return err
	}
	if err := validateSMTPConfig(normalized); err != nil {
		return err
	}
	config = normalized
	if err := config.setFromHeader(message); err != nil {
		return err
	}

	var lastError error
	for attempt := 0; attempt <= config.Retries; attempt++ {
		if err := smtpSendAttempt(config, message); err == nil {
			return nil
		} else {
			lastError = err
			if attempt == config.Retries || !retryableSMTPError(err) {
				break
			}
		}

		delay := time.Second << attempt
		if delay > maxSMTPBackoff {
			delay = maxSMTPBackoff
		}
		if err := waitForSMTPRetry(delay); err != nil {
			return err
		}
	}
	return lastError
}
