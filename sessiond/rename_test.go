package main

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// renameOverSocket sends one rename request through serve, the same path cguard uses.
func renameOverSocket(t *testing.T, d *daemon, id, name string) response {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	go d.serve(server)
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	line, _ := json.Marshal(request{Op: "rename", ID: id, Name: name})
	if _, err := client.Write(append(line, '\n')); err != nil {
		t.Fatalf("write request: %v", err)
	}
	reply, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp response
	if err := json.Unmarshal(reply, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func TestRenameSession(t *testing.T) {
	d := &daemon{sessions: map[string]*session{}, started: time.Now()}
	d.sessions["abc"] = &session{info: sessionInfo{ID: "abc", HostRef: "web-1", Name: "web-1", Status: "running"}}

	resp := renameOverSocket(t, d, "abc", "deploy shell")
	if !resp.OK || resp.Session == nil || resp.Session.Name != "deploy shell" {
		t.Fatalf("rename failed: %+v", resp)
	}
	if got := d.list()[0].Name; got != "deploy shell" {
		t.Fatalf("list still shows %q", got)
	}

	resp = renameOverSocket(t, d, "abc", "")
	if !resp.OK || resp.Session.Name != "web-1" {
		t.Fatalf("empty name should fall back to the host ID: %+v", resp)
	}

	resp = renameOverSocket(t, d, "missing", "x")
	if resp.OK || resp.Error != "session not found" {
		t.Fatalf("expected session not found, got %+v", resp)
	}
}
