package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startHangingSSHTestServer accepts any password, answers "exec" requests and then
// never finishes the command: the session stays open until the client closes it.
func startHangingSSHTestServer(t *testing.T) (string, int) {
	t.Helper()
	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) { return nil, nil },
	}
	hostSigner, _ := newSSHTestSigner(t)
	config.AddHostKey(hostSigner)
	return startSSHTestServerWithHandler(t, config, func(channel ssh.Channel, requests <-chan *ssh.Request) {
		defer channel.Close()
		for request := range requests {
			if request.WantReply {
				request.Reply(request.Type == "exec" || request.Type == "shell", nil)
			}
		}
	})
}

func hangingCommandConfig(host string, port, timeoutSeconds int) *CommandConfig {
	return &CommandConfig{
		Host:    Host{ConnectionType: "ssh", Host: host, Port: port, Username: "u", Password: "p"},
		Command: Command{CmdID: "hang", HostRef: "h", Command: "sleep 600", TimeoutSeconds: timeoutSeconds},
	}
}

func TestRunCommandWithTimeoutStopsHangingCommand(t *testing.T) {
	host, port := startHangingSSHTestServer(t)
	logFile, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	started := time.Now()
	_, _, runErr, timedOut := runCommandWithTimeout(context.Background(), hangingCommandConfig(host, port, 1), logFile)
	elapsed := time.Since(started)
	if runErr == nil || !timedOut {
		t.Fatalf("expected the command's own limit to fire, got err=%v timedOut=%v", runErr, timedOut)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("command was not stopped promptly: took %s", elapsed)
	}
	msg := runErr.Error()
	if !strings.Contains(msg, "the command was stopped after") || !strings.Contains(msg, "time limit of 1 seconds (1s)") || !strings.Contains(msg, "Edit Command dialog") || strings.Contains(msg, "context deadline") {
		t.Errorf("unexpected timeout wording: %s", msg)
	}
}

func TestRunCommandWithTimeoutLeavesParentCancellationToCaller(t *testing.T) {
	host, port := startHangingSSHTestServer(t)
	logFile, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	// Parent (e.g. RunBook step) limit is shorter: the helper must not claim the command limit fired.
	parent, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_, _, runErr, timedOut := runCommandWithTimeout(parent, hangingCommandConfig(host, port, 30), logFile)
	if timedOut || !errors.Is(runErr, context.DeadlineExceeded) {
		t.Fatalf("parent deadline should pass through untouched, got err=%v timedOut=%v", runErr, timedOut)
	}

	// No command limit at all: only the parent limit applies.
	parent2, cancel2 := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel2()
	_, _, runErr, timedOut = runCommandWithTimeout(parent2, hangingCommandConfig(host, port, 0), logFile)
	if timedOut || !errors.Is(runErr, context.DeadlineExceeded) {
		t.Fatalf("without a command limit the parent deadline should pass through, got err=%v timedOut=%v", runErr, timedOut)
	}
}

func TestDescribeTimeoutCommandWording(t *testing.T) {
	msg := describeTimeout("command", 600, 600*time.Second+200*time.Millisecond)
	for _, want := range []string{"Timed out: the command was stopped after 10m0s", "time limit of 600 seconds (10m0s)", `Raise "Timeout (seconds)" in the Edit Command dialog`} {
		if !strings.Contains(msg, want) {
			t.Errorf("command wording missing %q: %s", want, msg)
		}
	}
}

func TestValidateConfigRejectsNegativeCommandTimeout(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, timeout int) string {
		path := filepath.Join(dir, name)
		content := `{"port": 8080, "hosts": {"h": {"connection_type": "ssh", "host": "example.test", "port": 22, "username": "u", "password": "p"}},
"commands": [{"cmd_id": "c", "host_ref": "h", "command": "echo hi", "timeout_seconds": ` + strconv.Itoa(timeout) + `}]}`
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if err := validateConfig(write("ok.json", 120)); err != nil {
		t.Fatalf("positive timeout_seconds rejected: %v", err)
	}
	if err := validateConfig(write("bad.json", -5)); err == nil {
		t.Fatal("negative timeout_seconds was accepted")
	}
}

// End to end: a Command with timeout_seconds run through the API is stopped and the
// explanation reaches the API response and the log file.
func TestCommandTimeoutViaAPI(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()
	host, port := startHangingSSHTestServer(t)

	hostResp := ts.DoAuthRequest(t, "POST", "/api/config/hosts", map[string]interface{}{
		"name": "hang-host", "connection_type": "ssh", "host": host, "port": port, "username": "u", "password": "p",
	})
	hostResp.Body.Close()
	AssertStatus(t, http.StatusCreated, hostResp.StatusCode)

	cmdResp := ts.DoAuthRequest(t, "POST", "/api/config/commands", map[string]interface{}{
		"cmd_id": "hang-cmd", "host_ref": "hang-host", "command": "sleep 600", "timeout_seconds": 1,
	})
	cmdResp.Body.Close()
	AssertStatus(t, http.StatusCreated, cmdResp.StatusCode)

	// The saved value must come back to the editor.
	listed := ts.DoAuthRequest(t, "GET", "/api/config/commands", nil)
	listedBody := AssertJSONResponse(t, listed)
	listed.Body.Close()
	found := false
	for _, item := range listedBody["data"].([]interface{}) {
		cmd := item.(map[string]interface{})
		if cmd["cmd_id"] == "hang-cmd" {
			found = true
			if cmd["timeout_seconds"] != float64(1) {
				t.Errorf("timeout_seconds not returned by the commands list: %v", cmd["timeout_seconds"])
			}
		}
	}
	if !found {
		t.Fatal("hang-cmd missing from commands list")
	}

	started := time.Now()
	exec := ts.DoAuthRequest(t, "GET", "/api/commands/hang-cmd", nil)
	body := AssertJSONResponse(t, exec)
	exec.Body.Close()
	if time.Since(started) > 15*time.Second {
		t.Fatalf("API call was not stopped by the command timeout")
	}
	AssertStatus(t, http.StatusInternalServerError, exec.StatusCode)
	errText, _ := body["error"].(string)
	if !strings.Contains(errText, "the command was stopped after") || !strings.Contains(errText, "time limit of 1 seconds") || strings.Contains(errText, "context deadline") {
		t.Errorf("API error does not explain the timeout: %q", errText)
	}

	matches, _ := filepath.Glob("logs/*_hang-cmd_*_log.txt")
	if len(matches) == 0 {
		t.Fatal("no log file written for the timed-out command")
	}
	content, _ := os.ReadFile(matches[len(matches)-1])
	if !strings.Contains(string(content), "error : Timed out: the command was stopped after") || !strings.Contains(string(content), "exitcode : 1") {
		t.Errorf("log file does not record the timeout:\n%s", content)
	}
	for _, match := range matches {
		os.Remove(match)
	}
}
