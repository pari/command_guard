package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// startHostKeyTestServer starts an SSH server that accepts user "app" / "app-pass"
// and presents signer as its host key.
func startHostKeyTestServer(t *testing.T, signer ssh.Signer) (string, int) {
	t.Helper()
	config := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
		if c.User() == "app" && string(pass) == "app-pass" {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	config.AddHostKey(signer)
	return startSSHTestServerWithHandler(t, config, nil)
}

func addHostKeyTestHost(t *testing.T, ts *TestServer, id, addr string, port int) {
	t.Helper()
	resp := ts.DoAuthRequest(t, "POST", "/api/config/hosts", map[string]interface{}{
		"name": id, "connection_type": "ssh", "host": addr, "port": port, "username": "app", "password": "app-pass",
	})
	defer resp.Body.Close()
	AssertStatus(t, http.StatusCreated, resp.StatusCode)
}

func setHostKeyMode(t *testing.T, mode string) {
	t.Helper()
	err := NewConfigAPI(cache.configFile, authManager).mutateConfig(func(cfg map[string]interface{}) error {
		cfg["ssh_host_key_checking"] = mode
		return nil
	})
	if err != nil {
		t.Fatalf("set ssh_host_key_checking=%s: %v", mode, err)
	}
}

func connectToConfiguredHost(id string) error {
	host := cache.GetConfig().Hosts[id]
	normalizeHost(&host)
	executor := &SSHExecutor{host: host}
	if err := executor.Connect(); err != nil {
		return err
	}
	return executor.Close()
}

func TestHostKeyTrustOnFirstUse(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	signer, _ := newSSHTestSigner(t)
	addr, port := startHostKeyTestServer(t, signer)
	addHostKeyTestHost(t, ts, "tofu-host", addr, port)

	if got := cache.GetConfig().Hosts["tofu-host"].HostKey; got != "" {
		t.Fatalf("a new host must start without a key, got %q", got)
	}
	if err := connectToConfiguredHost("tofu-host"); err != nil {
		t.Fatalf("first connection: %v", err)
	}
	want := formatHostKey(signer.PublicKey())
	if got := cache.GetConfig().Hosts["tofu-host"].HostKey; got != want {
		t.Fatalf("host key not recorded: got %q want %q", got, want)
	}
	if err := connectToConfiguredHost("tofu-host"); err != nil {
		t.Fatalf("second connection with the recorded key: %v", err)
	}

	t.Run("changed key is refused and the stored key kept", func(t *testing.T) {
		other, _ := newSSHTestSigner(t)
		otherAddr, otherPort := startHostKeyTestServer(t, other)
		// Same host entry, but the "machine" now presents another key: store the
		// first key against the second server's address.
		resp := ts.DoAuthRequest(t, "PUT", "/api/config/hosts/tofu-host", map[string]interface{}{
			"connection_type": "ssh", "host": otherAddr, "port": otherPort, "username": "app", "host_key": want,
		})
		resp.Body.Close()
		AssertStatus(t, http.StatusOK, resp.StatusCode)

		err := connectToConfiguredHost("tofu-host")
		if err == nil {
			t.Fatal("a host presenting a different key was accepted")
		}
		var refused *hostKeyError
		if !errors.As(err, &refused) || refused.kind != "changed" {
			t.Fatalf("expected a host key change error, got: %v", err)
		}
		if !strings.Contains(err.Error(), ssh.FingerprintSHA256(other.PublicKey())) || !strings.Contains(err.Error(), ssh.FingerprintSHA256(signer.PublicKey())) {
			t.Fatalf("error does not name both fingerprints: %v", err)
		}
		if got := cache.GetConfig().Hosts["tofu-host"].HostKey; got != want {
			t.Fatalf("stored key was replaced by a refused key: %q", got)
		}

		t.Run("scan reports the mismatch", func(t *testing.T) {
			resp := ts.DoAuthRequest(t, "POST", "/api/config/hosts/tofu-host/host-key/scan", nil)
			defer resp.Body.Close()
			AssertStatus(t, http.StatusOK, resp.StatusCode)
			data := AssertJSONResponse(t, resp)["data"].(map[string]interface{})
			if data["match"] != false || data["presented_key"] != formatHostKey(other.PublicKey()) || data["stored_fingerprint"] != ssh.FingerprintSHA256(signer.PublicKey()) {
				t.Fatalf("unexpected scan result: %v", data)
			}
		})

		t.Run("trusting the presented key restores access", func(t *testing.T) {
			resp := ts.DoAuthRequest(t, "PUT", "/api/config/hosts/tofu-host", map[string]interface{}{
				"connection_type": "ssh", "host": otherAddr, "port": otherPort, "username": "app", "host_key": formatHostKey(other.PublicKey()),
			})
			resp.Body.Close()
			AssertStatus(t, http.StatusOK, resp.StatusCode)
			if err := connectToConfiguredHost("tofu-host"); err != nil {
				t.Fatalf("connection after trusting the new key: %v", err)
			}
			resp = ts.DoAuthRequest(t, "POST", "/api/config/hosts/tofu-host/host-key/scan", nil)
			defer resp.Body.Close()
			if data := AssertJSONResponse(t, resp)["data"].(map[string]interface{}); data["match"] != true {
				t.Fatalf("scan should match after trusting: %v", data)
			}
		})
	})
}

func TestHostKeyModes(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	signer, _ := newSSHTestSigner(t)
	addr, port := startHostKeyTestServer(t, signer)
	addHostKeyTestHost(t, ts, "mode-host", addr, port)

	t.Run("strict refuses a host with no stored key", func(t *testing.T) {
		setHostKeyMode(t, "strict")
		err := connectToConfiguredHost("mode-host")
		var refused *hostKeyError
		if !errors.As(err, &refused) || refused.kind != "unknown" {
			t.Fatalf("expected an unknown host key error, got: %v", err)
		}
		if got := cache.GetConfig().Hosts["mode-host"].HostKey; got != "" {
			t.Fatalf("strict mode recorded a key: %q", got)
		}
	})

	t.Run("strict accepts a pinned key", func(t *testing.T) {
		resp := ts.DoAuthRequest(t, "PUT", "/api/config/hosts/mode-host", map[string]interface{}{
			"connection_type": "ssh", "host": addr, "port": port, "username": "app", "host_key": formatHostKey(signer.PublicKey()) + " some comment",
		})
		resp.Body.Close()
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		if err := connectToConfiguredHost("mode-host"); err != nil {
			t.Fatalf("pinned host refused: %v", err)
		}
	})

	t.Run("off accepts a wrong key and records nothing", func(t *testing.T) {
		other, _ := newSSHTestSigner(t)
		resp := ts.DoAuthRequest(t, "PUT", "/api/config/hosts/mode-host", map[string]interface{}{
			"connection_type": "ssh", "host": addr, "port": port, "username": "app", "host_key": formatHostKey(other.PublicKey()),
		})
		resp.Body.Close()
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		if err := connectToConfiguredHost("mode-host"); err == nil {
			t.Fatal("wrong pinned key accepted in strict mode")
		}
		setHostKeyMode(t, "off")
		if err := connectToConfiguredHost("mode-host"); err != nil {
			t.Fatalf("off mode refused a connection: %v", err)
		}
	})

	t.Run("invalid mode is rejected and rolled back", func(t *testing.T) {
		err := NewConfigAPI(cache.configFile, authManager).mutateConfig(func(cfg map[string]interface{}) error {
			cfg["ssh_host_key_checking"] = "sometimes"
			return nil
		})
		if err == nil {
			t.Fatal("invalid ssh_host_key_checking was saved")
		}
		if mode := sshHostKeyMode(); mode != hostKeyModeOff {
			t.Fatalf("mode after a rejected save = %q, want off", mode)
		}
	})
}

func TestHostKeyConcurrentFirstConnections(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	signer, _ := newSSHTestSigner(t)
	addr, port := startHostKeyTestServer(t, signer)
	addHostKeyTestHost(t, ts, "race-host", addr, port)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- connectToConfiguredHost("race-host")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent first connection failed: %v", err)
		}
	}
	if got := cache.GetConfig().Hosts["race-host"].HostKey; got != formatHostKey(signer.PublicKey()) {
		t.Fatalf("unexpected stored key after concurrent first connections: %q", got)
	}
}

func TestHostKeyUpdateRules(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	signer, _ := newSSHTestSigner(t)
	key := formatHostKey(signer.PublicKey())
	put := func(body map[string]interface{}) (int, map[string]interface{}) {
		resp := ts.DoAuthRequest(t, "PUT", "/api/config/hosts/rules-host", body)
		defer resp.Body.Close()
		return resp.StatusCode, AssertJSONResponse(t, resp)
	}
	stored := func() string { return cache.GetConfig().Hosts["rules-host"].HostKey }

	resp := ts.DoAuthRequest(t, "POST", "/api/config/hosts", map[string]interface{}{
		"name": "rules-host", "connection_type": "ssh", "host": "192.0.2.10", "port": 22, "username": "app", "password": "p", "host_key": key,
	})
	body := AssertJSONResponse(t, resp)
	resp.Body.Close()
	AssertStatus(t, http.StatusCreated, resp.StatusCode)
	if data := body["data"].(map[string]interface{}); data["host_key_fingerprint"] != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatalf("create response lacks the fingerprint: %v", data)
	}

	if code, _ := put(map[string]interface{}{"connection_type": "ssh", "host": "192.0.2.10", "port": 22, "username": "other"}); code != http.StatusOK || stored() != key {
		t.Fatalf("update without host_key must keep it: code %d key %q", code, stored())
	}
	if code, _ := put(map[string]interface{}{"connection_type": "ssh", "host": "192.0.2.10", "port": 22, "username": "app", "host_key": "ssh-ed25519 not-a-key"}); code != http.StatusBadRequest || stored() != key {
		t.Fatalf("invalid host key: code %d key %q", code, stored())
	}
	if code, _ := put(map[string]interface{}{"connection_type": "ssh", "host": "192.0.2.11", "port": 22, "username": "app"}); code != http.StatusOK || stored() != "" {
		t.Fatalf("changing the address must clear the key: code %d key %q", code, stored())
	}
	if code, _ := put(map[string]interface{}{"connection_type": "ssh", "host": "192.0.2.11", "port": 22, "username": "app", "host_key": key}); code != http.StatusOK || stored() != key {
		t.Fatalf("pinning a key: code %d key %q", code, stored())
	}
	if code, _ := put(map[string]interface{}{"connection_type": "ssh", "host": "192.0.2.11", "port": 22, "username": "app", "host_key": ""}); code != http.StatusOK || stored() != "" {
		t.Fatalf("empty host_key must forget it: code %d key %q", code, stored())
	}

	t.Run("list reports the fingerprint", func(t *testing.T) {
		put(map[string]interface{}{"connection_type": "ssh", "host": "192.0.2.11", "port": 22, "username": "app", "host_key": key})
		resp := ts.DoAuthRequest(t, "GET", "/api/config/hosts", nil)
		defer resp.Body.Close()
		found := false
		for _, raw := range AssertJSONResponse(t, resp)["data"].([]interface{}) {
			h := raw.(map[string]interface{})
			if h["name"] == "rules-host" && h["host_key_fingerprint"] == ssh.FingerprintSHA256(signer.PublicKey()) && h["host_key_type"] == "ssh-ed25519" {
				found = true
			}
		}
		if !found {
			t.Fatal("GET /api/config/hosts does not report the host key fingerprint")
		}
	})

	t.Run("scan needs a sign-in and an SSH host", func(t *testing.T) {
		resp := ts.DoRequest(t, "POST", "/api/config/hosts/rules-host/host-key/scan", nil, nil)
		resp.Body.Close()
		AssertStatus(t, http.StatusUnauthorized, resp.StatusCode)
		resp = ts.DoAuthRequest(t, "POST", "/api/config/hosts/no-such-host/host-key/scan", nil)
		resp.Body.Close()
		AssertStatus(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestHostKeyEnsureForTerminals(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	signer, _ := newSSHTestSigner(t)
	addr, port := startHostKeyTestServer(t, signer)
	addHostKeyTestHost(t, ts, "term-host", addr, port)

	host := cache.GetConfig().Hosts["term-host"]
	got, err := ensureHostKey(host)
	if err != nil || got != formatHostKey(signer.PublicKey()) {
		t.Fatalf("ensureHostKey first use: key %q err %v", got, err)
	}
	if stored := cache.GetConfig().Hosts["term-host"].HostKey; stored != got {
		t.Fatalf("ensureHostKey did not record the key: %q", stored)
	}

	other, _ := newSSHTestSigner(t)
	otherAddr, otherPort := startHostKeyTestServer(t, other)
	moved := cache.GetConfig().Hosts["term-host"]
	moved.Host, moved.Port = otherAddr, otherPort
	if _, err := ensureHostKey(moved); err == nil || !strings.Contains(err.Error(), "HOST KEY CHANGED") {
		t.Fatalf("ensureHostKey accepted a changed key: %v", err)
	}
}

func TestHostKeyConfigProblems(t *testing.T) {
	signer, _ := newSSHTestSigner(t)
	cfg := &Config{SSHHostKeyChecking: "strict", Hosts: map[string]Host{
		"good":    {ConnectionType: "ssh", HostKey: formatHostKey(signer.PublicKey())},
		"bad":     {ConnectionType: "ssh", HostKey: "garbage"},
		"missing": {ConnectionType: "ssh"},
		"windows": {ConnectionType: "winrm"},
	}}
	problems, warnings := hostKeyConfigProblems(cfg)
	if len(problems) != 1 || !strings.Contains(problems[0], "'bad'") {
		t.Fatalf("problems = %v", problems)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "'missing'") {
		t.Fatalf("warnings = %v", warnings)
	}
	if got := fmt.Sprint(pinnedHostKeyAlgorithms(signer.PublicKey())); got != "[ssh-ed25519]" {
		t.Fatalf("pinned algorithms = %s", got)
	}
	for _, algo := range unpinnedHostKeyAlgorithms() {
		if strings.Contains(algo, "cert") {
			t.Fatalf("certificate algorithm offered to an unpinned host: %s", algo)
		}
	}
}
