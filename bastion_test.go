package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// markerSessionHandler answers "exec" with marker on stdout and exit status 0.
func markerSessionHandler(marker string) func(ssh.Channel, <-chan *ssh.Request) {
	return func(channel ssh.Channel, requests <-chan *ssh.Request) {
		defer channel.Close()
		for req := range requests {
			if req.Type == "exec" {
				req.Reply(true, nil)
				channel.Write([]byte(marker + "\n"))
				channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
				return
			}
			req.Reply(false, nil)
		}
	}
}

func passwordServerConfig(t *testing.T, user, password string, attempts chan<- string) *ssh.ServerConfig {
	t.Helper()
	config := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
		if attempts != nil {
			attempts <- c.User()
		}
		if c.User() == user && string(pass) == password {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	signer, _ := newSSHTestSigner(t)
	config.AddHostKey(signer)
	return config
}

// startBastionPair starts a forwarding bastion and a target behind it and returns
// the target Host wired to go through the bastion, plus the target's auth attempts.
func startBastionPair(t *testing.T, marker string) (Host, chan string) {
	t.Helper()
	targetAttempts := make(chan string, 16)
	targetAddr, targetPort := startSSHTestServerWithHandler(t, passwordServerConfig(t, "app", "app-pass", targetAttempts), markerSessionHandler(marker))
	bastionAddr, bastionPort := startForwardingSSHTestServer(t, passwordServerConfig(t, "jump", "jump-pass", nil), nil)
	target := Host{
		ConnectionType: "ssh", Host: targetAddr, Port: targetPort, Username: "app", Password: "app-pass",
		BastionHostRef: "jump-host",
		Bastion:        &Host{ConnectionType: "ssh", Host: bastionAddr, Port: bastionPort, Username: "jump", Password: "jump-pass"},
	}
	return target, targetAttempts
}

func TestSSHExecutorConnectsThroughBastion(t *testing.T) {
	target, _ := startBastionPair(t, "hello-from-target")
	executor := &SSHExecutor{host: target}
	if err := executor.Connect(); err != nil {
		t.Fatalf("connect through bastion: %v", err)
	}
	if executor.bastionClient == nil {
		t.Fatal("bastion client not kept on the executor")
	}
	logFile, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	output, _, err := executor.Execute("echo hi", logFile, 1<<20)
	if err != nil {
		t.Fatalf("execute through bastion: %v", err)
	}
	if !strings.Contains(output, "hello-from-target") {
		t.Fatalf("command did not run on the target: %q", output)
	}
	if err := executor.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestSSHExecutorBastionRefusesForwarding(t *testing.T) {
	target, targetAttempts := startBastionPair(t, "unused")
	// A bastion without forwarding rejects every direct-tcpip channel.
	noForwardAddr, noForwardPort := startSSHTestServer(t, &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }})
	target.Bastion = &Host{ConnectionType: "ssh", Host: noForwardAddr, Port: noForwardPort, Username: "jump", Password: "x"}

	err := (&SSHExecutor{host: target}).Connect()
	if err == nil {
		t.Fatal("expected the connection to fail")
	}
	for _, want := range []string{`bastion host "jump-host"`, "refuses to forward", "AllowTcpForwarding"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
	select {
	case user := <-targetAttempts:
		t.Fatalf("target was contacted directly (auth attempt by %q)", user)
	default:
	}
}

func TestSSHExecutorBastionErrorsNameTheHop(t *testing.T) {
	target, _ := startBastionPair(t, "unused")

	wrongBastion := target
	wrongBastion.Bastion = &Host{ConnectionType: "ssh", Host: target.Bastion.Host, Port: target.Bastion.Port, Username: "jump", Password: "wrong"}
	err := (&SSHExecutor{host: wrongBastion}).Connect()
	if err == nil || !strings.Contains(err.Error(), `failed to connect to bastion host "jump-host"`) {
		t.Fatalf("bastion auth failure not attributed to the bastion: %v", err)
	}

	wrongTarget := target
	wrongTarget.Password = "wrong"
	err = (&SSHExecutor{host: wrongTarget}).Connect()
	if err == nil || !strings.Contains(err.Error(), `via bastion host "jump-host"`) || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("target auth failure through the bastion not explained: %v", err)
	}
}

func TestSSHExecutorUnresolvedBastionFailsLoudly(t *testing.T) {
	target, targetAttempts := startBastionPair(t, "unused")
	target.Bastion = nil // configured reference, nothing resolved: must not dial the target directly
	err := (&SSHExecutor{host: target}).Connect()
	if err == nil || !strings.Contains(err.Error(), "was not resolved") {
		t.Fatalf("expected the unresolved-bastion guard, got %v", err)
	}
	select {
	case user := <-targetAttempts:
		t.Fatalf("target was contacted directly (auth attempt by %q)", user)
	default:
	}
}

func TestRunDiskDfThroughBastion(t *testing.T) {
	target := startDiskTestSSHServer(t)
	bastionAddr, bastionPort := startForwardingSSHTestServer(t, passwordServerConfig(t, "jump", "jump-pass", nil), nil)
	target.BastionHostRef = "jump-host"
	target.Bastion = &Host{ConnectionType: "ssh", Host: bastionAddr, Port: bastionPort, Username: "jump", Password: "jump-pass"}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	run := runDiskDf(ctx, target, "df -PB1 /")
	if run.ConnErr != nil || run.RunErr == nil && run.Stdout == "" {
		t.Fatalf("df through bastion failed: conn=%v run=%v stdout=%q", run.ConnErr, run.RunErr, run.Stdout)
	}
	if !strings.Contains(run.Stdout, "Filesystem") {
		t.Fatalf("unexpected df output through bastion: %q", run.Stdout)
	}

	target.Bastion = nil
	run = runDiskDf(ctx, target, "df -PB1 /")
	if run.ConnErr == nil || !strings.Contains(run.ConnErr.Error(), "was not resolved") {
		t.Fatalf("disk check bypassed an unresolved bastion: %v", run.ConnErr)
	}
}

func TestResolveBastionHosts(t *testing.T) {
	ssh := func(ref string) Host {
		return Host{ConnectionType: "ssh", Host: "h", Username: "u", Password: "p", BastionHostRef: ref}
	}
	cases := []struct {
		name  string
		hosts map[string]Host
		want  string // substring of the error, "" = valid
	}{
		{"direct hosts", map[string]Host{"a": ssh(""), "b": ssh("")}, ""},
		{"valid single hop", map[string]Host{"a": ssh("b"), "b": ssh("")}, ""},
		{"missing reference", map[string]Host{"a": ssh("nope")}, "non-existent bastion host 'nope'"},
		{"self reference", map[string]Host{"a": ssh("a")}, "cannot use itself"},
		{"chained bastion", map[string]Host{"a": ssh("b"), "b": ssh("c"), "c": ssh("")}, "only a single hop"},
		{"winrm target", map[string]Host{"a": {ConnectionType: "winrm", Host: "w", Username: "u", Password: "p", BastionHostRef: "b"}, "b": ssh("")}, "only SSH hosts can connect via a bastion"},
		{"winrm bastion", map[string]Host{"a": ssh("w"), "w": {ConnectionType: "winrm", Host: "w", Username: "u", Password: "p"}}, "a bastion must be an SSH host"},
		{"bastion without auth", map[string]Host{"a": ssh("b"), "b": {ConnectionType: "ssh", Host: "h", Username: "u"}}, "has no authentication method"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := resolveBastionHosts(tc.hosts)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				for name, host := range tc.hosts {
					if host.BastionHostRef == "" && host.Bastion != nil {
						t.Errorf("%s: bastion attached without reference", name)
					}
					if host.BastionHostRef != "" && (host.Bastion == nil || host.Bastion.Port != 22) {
						t.Errorf("%s: bastion not attached/normalized: %+v", name, host.Bastion)
					}
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
			if problems := bastionRefProblems(tc.hosts); len(problems) == 0 {
				t.Fatalf("bastionRefProblems reported nothing for %s", tc.name)
			}
		})
	}
}

func TestBastionHostAPI(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	post := func(body map[string]interface{}) int {
		resp := ts.DoAuthRequest(t, "POST", "/api/config/hosts", body)
		defer resp.Body.Close()
		return resp.StatusCode
	}
	put := func(id string, body map[string]interface{}) (int, map[string]interface{}) {
		resp := ts.DoAuthRequest(t, "PUT", "/api/config/hosts/"+id, body)
		defer resp.Body.Close()
		return resp.StatusCode, AssertJSONResponse(t, resp)
	}
	del := func(id string) int {
		resp := ts.DoAuthRequest(t, "DELETE", "/api/config/hosts/"+id, nil)
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := post(map[string]interface{}{"name": "jump-host", "connection_type": "ssh", "host": "10.0.0.1", "port": 22, "username": "jump", "password": "p"}); code != http.StatusCreated {
		t.Fatalf("create bastion: %d", code)
	}
	if code := post(map[string]interface{}{"name": "inner", "connection_type": "ssh", "host": "10.0.1.5", "port": 22, "username": "app", "password": "p", "bastion_host_ref": "jump-host"}); code != http.StatusCreated {
		t.Fatalf("create host via bastion: %d", code)
	}
	if code := post(map[string]interface{}{"name": "bad", "connection_type": "ssh", "host": "x", "port": 22, "username": "u", "password": "p", "bastion_host_ref": "missing"}); code != http.StatusBadRequest {
		t.Fatalf("missing bastion reference accepted: %d", code)
	}
	if code := post(map[string]interface{}{"name": "chained", "connection_type": "ssh", "host": "x", "port": 22, "username": "u", "password": "p", "bastion_host_ref": "inner"}); code != http.StatusBadRequest {
		t.Fatalf("chained bastion accepted: %d", code)
	}

	// The list API and the loaded config both carry the reference
	resp := ts.DoAuthRequest(t, "GET", "/api/config/hosts", nil)
	body := AssertJSONResponse(t, resp)
	resp.Body.Close()
	found := false
	for _, raw := range body["data"].([]interface{}) {
		h := raw.(map[string]interface{})
		if h["name"] == "inner" && h["bastion_host_ref"] == "jump-host" {
			found = true
		}
	}
	if !found {
		t.Fatalf("GET /api/config/hosts does not report bastion_host_ref: %v", body["data"])
	}
	if inner := cache.GetConfig().Hosts["inner"]; inner.Bastion == nil || inner.Bastion.Host != "10.0.0.1" {
		t.Fatalf("reload did not resolve the bastion: %+v", inner)
	}

	// A WinRM bastion is refused; an update without the key keeps the reference
	if code, _ := put("inner", map[string]interface{}{"connection_type": "ssh", "host": "10.0.1.5", "port": 22, "username": "app", "bastion_host_ref": "test-winrm-server"}); code != http.StatusBadRequest {
		t.Fatalf("WinRM bastion accepted: %d", code)
	}
	if code, _ := put("inner", map[string]interface{}{"connection_type": "ssh", "host": "10.0.1.6", "port": 22, "username": "app"}); code != http.StatusOK {
		t.Fatalf("update without bastion key: %d", code)
	}
	if inner := ts.GetConfig(t)["hosts"].(map[string]interface{})["inner"].(map[string]interface{}); inner["bastion_host_ref"] != "jump-host" || inner["password"] != "p" {
		t.Fatalf("bastion reference or password not preserved on update: %v", inner)
	}
	// Switching the bastion itself to WinRM is refused while it is in use
	if code, _ := put("jump-host", map[string]interface{}{"connection_type": "winrm", "host": "10.0.0.1", "port": 5986, "username": "jump", "password": "p"}); code != http.StatusBadRequest {
		t.Fatalf("bastion switched to WinRM while in use: %d", code)
	}

	// Delete protection, then release and delete
	if code := del("jump-host"); code != http.StatusConflict {
		t.Fatalf("bastion in use deleted: %d", code)
	}
	if code, _ := put("inner", map[string]interface{}{"connection_type": "ssh", "host": "10.0.1.6", "port": 22, "username": "app", "bastion_host_ref": ""}); code != http.StatusOK {
		t.Fatalf("clearing bastion: %d", code)
	}
	if code := del("jump-host"); code != http.StatusOK {
		t.Fatalf("delete released bastion: %d", code)
	}
}
