package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSH host key verification.
//
// Each SSH host stores one public key in config.json (hosts.<id>.host_key, in
// authorized_keys form). The top-level ssh_host_key_checking setting decides what
// happens when a host has no stored key yet:
//
//	tofu   (default) the first connection records the key it is shown
//	strict           the connection is refused until an admin stores a key
//	off              host keys are not checked at all
//
// A host whose presented key differs from the stored one is always refused
// (except in "off" mode) until an admin accepts the new key on the Hosts page.
const (
	hostKeyModeTOFU   = "tofu"
	hostKeyModeStrict = "strict"
	hostKeyModeOff    = "off"
)

// normalizeHostKeyMode maps the config value to one of the hostKeyMode constants.
func normalizeHostKeyMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", hostKeyModeTOFU:
		return hostKeyModeTOFU, nil
	case hostKeyModeStrict:
		return hostKeyModeStrict, nil
	case hostKeyModeOff:
		return hostKeyModeOff, nil
	}
	return "", fmt.Errorf("invalid ssh_host_key_checking %q (valid: tofu, strict, off)", value)
}

// sshHostKeyMode returns the mode of the loaded configuration.
func sshHostKeyMode() string {
	if cfg := cache.GetConfig(); cfg != nil {
		if mode, err := normalizeHostKeyMode(cfg.SSHHostKeyChecking); err == nil {
			return mode
		}
	}
	return hostKeyModeTOFU
}

// parseHostKey parses a stored host key ("ssh-ed25519 AAAA… [comment]").
func parseHostKey(text string) (ssh.PublicKey, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("host key is empty")
	}
	if strings.ContainsAny(text, "\r\n") {
		return nil, errors.New("host key must be a single line")
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("not a valid SSH public key (expected a line such as \"ssh-ed25519 AAAA…\"): %v", err)
	}
	if _, isCert := key.(*ssh.Certificate); isCert {
		return nil, errors.New("host certificates are not supported; store the host's plain public key")
	}
	return key, nil
}

// formatHostKey renders a key the way it is stored in config.json.
func formatHostKey(key ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// hostKeyInfo describes a stored key for API responses; ok is false when text is
// empty or unparsable.
func hostKeyInfo(text string) (keyType, fingerprint string, ok bool) {
	key, err := parseHostKey(text)
	if err != nil {
		return "", "", false
	}
	return key.Type(), ssh.FingerprintSHA256(key), true
}

// pinnedHostKeyAlgorithms limits the handshake to the algorithms of the stored
// key, so that a server holding several host keys presents the pinned one.
func pinnedHostKeyAlgorithms(key ssh.PublicKey) []string {
	if key.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{key.Type()}
}

// unpinnedHostKeyAlgorithms is the list offered to a host with no stored key:
// every plain key algorithm, no host certificates (a certificate is re-issued
// regularly, so pinning one would raise false alarms). Ed25519 comes first, then
// ECDSA, then the rest (RSA), which is also the order OpenSSH prefers, so the key
// recorded here is normally the one an admin sees in their own known_hosts.
func unpinnedHostKeyAlgorithms() []string {
	all := append(ssh.SupportedAlgorithms().HostKeys, ssh.InsecureAlgorithms().HostKeys...)
	rank := func(algo string) int {
		switch {
		case algo == ssh.KeyAlgoED25519:
			return 0
		case strings.HasPrefix(algo, "ecdsa-"):
			return 1
		}
		return 2
	}
	plain := make([]string, 0, len(all))
	for _, algo := range all {
		if !strings.Contains(algo, "-cert-") {
			plain = append(plain, algo)
		}
	}
	sort.SliceStable(plain, func(i, j int) bool { return rank(plain[i]) < rank(plain[j]) })
	return plain
}

// hostKeyError is returned from the handshake when a host key is refused.
type hostKeyError struct {
	kind      string // changed | unknown
	hostID    string
	expected  string // SHA256 fingerprint of the stored key (changed only)
	presented string // type and SHA256 fingerprint of the key the server showed
}

func (e *hostKeyError) Error() string {
	name := e.hostID
	if name == "" {
		name = "this host"
	}
	if e.kind == "unknown" {
		return fmt.Sprintf("the host key of %s is not approved yet (the server presented %s) and SSH host key checking is set to strict. Open the host on the Hosts page, check the key and trust it", name, e.presented)
	}
	return fmt.Sprintf("HOST KEY CHANGED for %s: expected %s but the server presented %s. Either the server was reinstalled or its keys were regenerated, or the connection is being intercepted. If the change is expected, open the host on the Hosts page and trust the new key", name, e.expected, e.presented)
}

// hostKeyConfigProblems lists invalid host key settings in cfg (loader, restore
// and --validate) and, separately, settings worth a warning.
func hostKeyConfigProblems(cfg *Config) (problems, warnings []string) {
	mode, err := normalizeHostKeyMode(cfg.SSHHostKeyChecking)
	if err != nil {
		problems = append(problems, err.Error())
	}
	if mode == hostKeyModeOff {
		warnings = append(warnings, "ssh_host_key_checking is off: SSH host keys are not verified")
	}
	names := make([]string, 0, len(cfg.Hosts))
	for name := range cfg.Hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		host := cfg.Hosts[name]
		if !isSSHHost(host) {
			continue
		}
		if strings.TrimSpace(host.HostKey) == "" {
			if mode == hostKeyModeStrict {
				warnings = append(warnings, fmt.Sprintf("Host '%s' has no host_key and ssh_host_key_checking is strict: connections to it will be refused", name))
			}
			continue
		}
		if _, err := parseHostKey(host.HostKey); err != nil {
			problems = append(problems, fmt.Sprintf("host '%s' has an invalid host_key: %v", name, err))
		}
	}
	return problems, warnings
}

// storedHostKey returns the newest stored key for host: the loaded configuration
// wins over the (possibly older) copy the caller holds.
func storedHostKey(host Host) string {
	if host.ID != "" {
		if cfg := cache.GetConfig(); cfg != nil {
			if current, ok := cfg.Hosts[host.ID]; ok {
				return strings.TrimSpace(current.HostKey)
			}
		}
	}
	return strings.TrimSpace(host.HostKey)
}

// hostKeyVerifier returns the HostKeyCallback and HostKeyAlgorithms for one
// connection to host under the current mode.
func hostKeyVerifier(host Host) (ssh.HostKeyCallback, []string, error) {
	mode := sshHostKeyMode()
	if mode == hostKeyModeOff {
		return ssh.InsecureIgnoreHostKey(), nil, nil
	}
	stored := storedHostKey(host)
	if stored != "" {
		pinned, err := parseHostKey(stored)
		if err != nil {
			return nil, nil, fmt.Errorf("the stored host key is invalid: %v", err)
		}
		callback := func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if bytes.Equal(key.Marshal(), pinned.Marshal()) {
				return nil
			}
			return refuseChangedHostKey(host.ID, pinned, key)
		}
		return callback, pinnedHostKeyAlgorithms(pinned), nil
	}
	callback := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if mode == hostKeyModeStrict {
			return &hostKeyError{kind: "unknown", hostID: host.ID, presented: describeHostKey(key)}
		}
		if host.ID == "" {
			return nil // a host that is not in config.json: nowhere to record the key
		}
		return recordHostKey(host.ID, key)
	}
	return callback, unpinnedHostKeyAlgorithms(), nil
}

func describeHostKey(key ssh.PublicKey) string {
	return key.Type() + " " + ssh.FingerprintSHA256(key)
}

var (
	hostKeyMismatchAudited   = map[string]time.Time{}
	hostKeyMismatchAuditLock sync.Mutex
)

// refuseChangedHostKey builds the mismatch error and writes one audit entry per
// host and presented key per hour (a cron job must not flood the audit log).
func refuseChangedHostKey(hostID string, pinned, presented ssh.PublicKey) error {
	failure := &hostKeyError{kind: "changed", hostID: hostID, expected: describeHostKey(pinned), presented: describeHostKey(presented)}
	auditKey := hostID + "|" + ssh.FingerprintSHA256(presented)
	hostKeyMismatchAuditLock.Lock()
	last, seen := hostKeyMismatchAudited[auditKey]
	audit := !seen || time.Since(last) > time.Hour
	if audit {
		hostKeyMismatchAudited[auditKey] = time.Now()
	}
	hostKeyMismatchAuditLock.Unlock()
	if audit {
		log.Printf("SSH host key mismatch for host %q: expected %s, presented %s", hostID, failure.expected, failure.presented)
		writeSecurityAudit(nil, "system", "system", "ssh_host_key_mismatch", hostID, "denied", map[string]interface{}{
			"expected": failure.expected, "presented": failure.presented,
		})
	}
	return failure
}

var errHostKeyAlreadyStored = errors.New("host key already stored")

// recordHostKey stores the key a host presented on its first connection. Two
// first connections may race: the second finds the key of the first and must
// match it. A failure to write config.json is logged and does not fail the
// connection (the host simply stays unrecorded); a conflicting key does.
func recordHostKey(hostID string, key ssh.PublicKey) error {
	var conflict error
	err := NewConfigAPI(cache.configFile, authManager).mutateConfig(func(cfg map[string]interface{}) error {
		hosts, _ := cfg["hosts"].(map[string]interface{})
		hostData, ok := hosts[hostID].(map[string]interface{})
		if !ok {
			return fmt.Errorf("host %q is no longer in the configuration", hostID)
		}
		if existing, _ := hostData["host_key"].(string); strings.TrimSpace(existing) != "" {
			pinned, perr := parseHostKey(existing)
			if perr == nil && !bytes.Equal(pinned.Marshal(), key.Marshal()) {
				conflict = refuseChangedHostKey(hostID, pinned, key)
			}
			return errHostKeyAlreadyStored // nothing to write
		}
		hostData["host_key"] = formatHostKey(key)
		return nil
	})
	if conflict != nil {
		return conflict
	}
	if errors.Is(err, errHostKeyAlreadyStored) {
		return nil
	}
	if err != nil {
		log.Printf("Could not record the SSH host key of host %q (it will be recorded on a later connection): %v", hostID, err)
		return nil
	}
	log.Printf("Recorded SSH host key of host %q on first use: %s", hostID, describeHostKey(key))
	writeSecurityAudit(nil, "system", "system", "ssh_host_key_learned", hostID, "success", map[string]interface{}{
		"key": describeHostKey(key),
	})
	return nil
}

// errHostKeyProbeDone ends a probe handshake once the host key has been seen.
var errHostKeyProbeDone = errors.New("host key probe complete")

const hostKeyProbeTimeout = 15 * time.Second

// probeSSHHandshake runs the SSH handshake with host up to the host key check and
// then stops: no credentials are sent to host. A bastion, when configured, is
// logged into normally (its own key is verified by SSHExecutor.Connect).
func probeSSHHandshake(host Host, callback ssh.HostKeyCallback, algorithms []string) error {
	if host.BastionHostRef != "" && host.Bastion == nil {
		return fmt.Errorf("host is configured to connect via bastion host %q but the bastion was not resolved (configuration not loaded)", host.BastionHostRef)
	}
	normalizeHost(&host)
	cfg := &ssh.ClientConfig{
		User:              host.Username,
		HostKeyCallback:   callback,
		HostKeyAlgorithms: algorithms,
		Timeout:           hostKeyProbeTimeout,
	}
	address := fmt.Sprintf("%s:%d", host.Host, host.Port)
	var client *ssh.Client
	var err error
	if host.Bastion == nil {
		client, err = dialSSH(address, cfg, hostKeyProbeTimeout)
	} else {
		bastion := &SSHExecutor{host: *host.Bastion, connectTimeout: hostKeyProbeTimeout}
		normalizeHost(&bastion.host)
		if berr := bastion.Connect(); berr != nil {
			return fmt.Errorf("bastion host %q: %w", host.BastionHostRef, berr)
		}
		defer bastion.Close()
		client, err = dialThroughBastion(bastion.client, address, cfg, hostKeyProbeTimeout)
	}
	if client != nil { // only when the server lets a client in with no credentials
		client.Close()
	}
	return err
}

// fetchHostKey returns the key host presents right now, without checking it.
// pinned (optional) asks the server for a key of that type first.
func fetchHostKey(host Host, pinned ssh.PublicKey) (ssh.PublicKey, error) {
	var seen ssh.PublicKey
	capture := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		seen = key
		return errHostKeyProbeDone
	}
	algorithms := unpinnedHostKeyAlgorithms()
	if pinned != nil {
		algorithms = pinnedHostKeyAlgorithms(pinned)
	}
	err := probeSSHHandshake(host, capture, algorithms)
	if seen != nil {
		return seen, nil
	}
	if err == nil {
		err = errors.New("the server did not present a host key")
	}
	return nil, err
}

// ensureHostKey verifies host's key with a probe handshake (recording it on first
// use) and returns the key now stored for it, "" when checking is off. The
// terminal handler calls it before handing a connection to cg_sessiond, so the
// check also holds for an older running daemon that does not verify keys itself.
func ensureHostKey(host Host) (string, error) {
	if sshHostKeyMode() == hostKeyModeOff {
		return "", nil
	}
	verify, algorithms, err := hostKeyVerifier(host)
	if err != nil {
		return "", err
	}
	var seen ssh.PublicKey
	callback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if verr := verify(hostname, remote, key); verr != nil {
			return verr
		}
		seen = key
		return errHostKeyProbeDone
	}
	err = probeSSHHandshake(host, callback, algorithms)
	if seen != nil {
		return formatHostKey(seen), nil
	}
	if err == nil {
		err = errors.New("the server did not present a host key")
	}
	return "", explainHostKeyFailure(host, err)
}

// explainHostKeyFailure adds a hint when a pinned host can no longer offer a key
// of the stored type (for example after its ed25519 key was removed).
func explainHostKeyFailure(host Host, cause error) error {
	var refused *hostKeyError
	if errors.As(cause, &refused) {
		return refused
	}
	if storedHostKey(host) != "" && strings.Contains(cause.Error(), "no common algorithm") && strings.Contains(cause.Error(), "host key") {
		keyType, _, _ := hostKeyInfo(storedHostKey(host))
		return fmt.Errorf("the server no longer offers a host key of the stored type (%s). If its keys were regenerated, open the host on the Hosts page and trust the new key. Original SSH error: %w", keyType, cause)
	}
	return cause
}

// hostAddressChanged reports whether an update moves a host to another machine
// (host name or port), in which case its stored key no longer applies.
func hostAddressChanged(existing, updated map[string]interface{}) bool {
	address := func(m map[string]interface{}) string {
		host, _ := m["host"].(string)
		port := 0
		switch v := m["port"].(type) {
		case float64:
			port = int(v)
		case int:
			port = v
		}
		if port == 0 {
			port = 22
		}
		return fmt.Sprintf("%s:%d", strings.TrimSpace(host), port)
	}
	return address(existing) != address(updated)
}

// applyHostKeyField normalises host_key in a host create/update body. existing is
// the stored host (nil on create). It returns whether the stored key changes.
func applyHostKeyField(existing, hostData map[string]interface{}) (bool, error) {
	previous := ""
	if existing != nil {
		previous, _ = existing["host_key"].(string)
		previous = strings.TrimSpace(previous)
	}
	raw, sent := hostData["host_key"]
	delete(hostData, "host_key_type")
	delete(hostData, "host_key_fingerprint")
	if !sent {
		// Field left out: keep the stored key unless the host now points elsewhere.
		if previous != "" && !hostAddressChanged(existing, hostData) {
			hostData["host_key"] = previous
			return false, nil
		}
		return previous != "", nil
	}
	text, isString := raw.(string)
	if !isString {
		return false, httpErr(http.StatusBadRequest, "host_key must be a string")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		delete(hostData, "host_key")
		return previous != "", nil
	}
	key, err := parseHostKey(text)
	if err != nil {
		return false, httpErr(http.StatusBadRequest, "Invalid host key: "+err.Error())
	}
	hostData["host_key"] = formatHostKey(key)
	return hostData["host_key"] != previous, nil
}

// HandleHostItem routes /api/config/hosts/{id} (update, delete) and
// /api/config/hosts/{id}/host-key/scan.
func (ca *ConfigAPI) HandleHostItem(w http.ResponseWriter, r *http.Request) {
	if hostID, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/api/config/hosts/"), "/host-key/scan"); ok {
		ca.HandleScanHostKey(w, r, hostID)
		return
	}
	switch r.Method {
	case http.MethodPut:
		ca.HandleUpdateHost(w, r)
	case http.MethodDelete:
		ca.HandleDeleteHost(w, r)
	default:
		sendJSONResponse(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Error: "Method not allowed"})
	}
}

// HandleScanHostKey answers POST /api/config/hosts/{id}/host-key/scan with the
// stored key and the key the host presents right now. It changes nothing; the
// admin accepts a new key with a normal host update.
func (ca *ConfigAPI) HandleScanHostKey(w http.ResponseWriter, r *http.Request, hostID string) {
	if r.Method != http.MethodPost {
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	cfg := cache.GetConfig()
	if cfg == nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Configuration not loaded")
		return
	}
	host, ok := cfg.Hosts[hostID]
	if !ok {
		sendConfigJSONError(w, http.StatusNotFound, "Host not found")
		return
	}
	if !isSSHHost(host) {
		sendConfigJSONError(w, http.StatusBadRequest, "Only SSH hosts have a host key")
		return
	}
	result := map[string]interface{}{"mode": sshHostKeyMode(), "match": false}
	var pinned ssh.PublicKey
	if stored := strings.TrimSpace(host.HostKey); stored != "" {
		if key, err := parseHostKey(stored); err == nil {
			pinned = key
			result["stored_key"] = formatHostKey(key)
			result["stored_type"] = key.Type()
			result["stored_fingerprint"] = ssh.FingerprintSHA256(key)
		}
	}
	presented, err := fetchHostKey(host, pinned)
	if err != nil && pinned != nil {
		// The server may no longer hold a key of the stored type: ask for any.
		presented, err = fetchHostKey(host, nil)
	}
	if err != nil {
		sendConfigJSONError(w, http.StatusBadGateway, "Could not read the host key: "+err.Error())
		return
	}
	result["presented_key"] = formatHostKey(presented)
	result["presented_type"] = presented.Type()
	result["presented_fingerprint"] = ssh.FingerprintSHA256(presented)
	result["match"] = pinned != nil && bytes.Equal(pinned.Marshal(), presented.Marshal())
	sendConfigJSONSuccess(w, http.StatusOK, result, "Host key read")
}
