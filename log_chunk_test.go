package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeChunkTestLog(t *testing.T, traceID string, lines int) (string, string) {
	t.Helper()
	if err := os.MkdirAll("logs", 0755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	writeLogField(&b, "trace_id", traceID)
	writeLogField(&b, "cmd_id", "chunk-cmd")
	writeLogField(&b, "host", "chunk-host")
	writeLogOutputMarker(&b)
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "line %05d some output text to make the line longer\n", i)
	}
	writeLogField(&b, "exitcode", "0")
	path := filepath.Join("logs", "2026_10_17_000000_chunk-cmd_"+traceID+"_log.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	return path, b.String()
}

func TestLogChunkAlignment(t *testing.T) {
	content := []byte("aaaa\nbbbb\ncccc\n")
	cases := []struct {
		size, before int64
		want         string
		offset       int64
	}{
		{1024, -1, "aaaa\nbbbb\ncccc\n", 0},
		{7, -1, "cccc\n", 10}, // window "bb\ncccc\n" starts mid-line
		{5, -1, "cccc\n", 10}, // window starts exactly on a line
		{5, 10, "bbbb\n", 5},  // paging backwards
		{3, 10, "bb\n", 7},    // no newline in window: keep the partial line
		{1024, 99, string(content), 0},
	}
	for _, c := range cases {
		got, offset, _ := sliceLogChunk(content, c.size, c.before)
		if string(got) != c.want || offset != c.offset {
			t.Errorf("size=%d before=%d: got %q@%d, want %q@%d", c.size, c.before, got, offset, c.want, c.offset)
		}
	}
}

func TestLogContentChunks(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()
	traceID := "c0ffee0000000001"
	path, full := writeChunkTestLog(t, traceID, 2000)

	get := func(query string) (int, map[string]interface{}) {
		resp := ts.DoAuthRequest(t, "GET", "/api/logs/"+traceID+"/content"+query, nil)
		defer resp.Body.Close()
		body := AssertJSONResponse(t, resp)
		data, _ := body["data"].(map[string]interface{})
		return resp.StatusCode, data
	}

	// Tail: line-aligned, ends at the end of the file.
	status, data := get("?bytes=4096")
	if status != http.StatusOK {
		t.Fatalf("tail status %d", status)
	}
	tail := data["content"].(string)
	if int64(data["total_size"].(float64)) != int64(len(full)) || int64(data["end"].(float64)) != int64(len(full)) {
		t.Errorf("tail sizes wrong: %v", data)
	}
	if !strings.HasSuffix(full, tail) || !strings.HasPrefix(tail, "line ") || len(tail) > 4096 {
		t.Errorf("tail not line aligned: %q", tail[:40])
	}
	if data["cmd_id"] != "chunk-cmd" || data["host"] != "chunk-host" {
		t.Errorf("header fields missing: %v", data)
	}

	// Paging backwards with before= rebuilds the whole file.
	pieces := []string{tail}
	offset := int64(data["offset"].(float64))
	for offset > 0 {
		_, page := get(fmt.Sprintf("?bytes=4096&before=%d", offset))
		next := int64(page["offset"].(float64))
		if next >= offset {
			t.Fatalf("paging did not move back: %d -> %d", offset, next)
		}
		pieces = append([]string{page["content"].(string)}, pieces...)
		offset = next
	}
	if strings.Join(pieces, "") != full {
		t.Error("paged chunks do not join back to the full log")
	}

	// Small file fits in one window.
	smallTrace := "c0ffee0000000002"
	_, smallFull := writeChunkTestLog(t, smallTrace, 3)
	resp := ts.DoAuthRequest(t, "GET", "/api/logs/"+smallTrace+"/content?bytes=131072", nil)
	small := AssertJSONResponse(t, resp)["data"].(map[string]interface{})
	resp.Body.Close()
	if small["content"] != smallFull || small["offset"].(float64) != 0 {
		t.Errorf("small log not returned whole: %v", small)
	}

	// Bad parameters.
	for _, query := range []string{"?bytes=10", "?bytes=99999999", "?bytes=abc", "?before=-5"} {
		resp := ts.DoAuthRequest(t, "GET", "/api/logs/"+traceID+"/content"+query, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", query, resp.StatusCode)
		}
	}

	// No parameters: unchanged full response.
	_, whole := get("")
	if whole["content"] != full {
		t.Error("plain /content no longer returns the whole log")
	}
	if _, ok := whole["total_size"]; ok {
		t.Error("plain /content should not switch to ranged mode")
	}

	// A regular user without a grant cannot read a window.
	original := authManager
	defer func() { authManager = original }()
	authManager = &AuthManager{users: map[string]User{"viewer": normalizeUser(User{Username: "viewer", Enabled: true})}}
	request := withClaims(httptest.NewRequest("GET", "/api/logs/"+traceID+"/content?bytes=4096", nil), &Claims{Username: "viewer", Role: RoleUser})
	recorder := httptest.NewRecorder()
	NewConfigAPI("", authManager).HandleLogContent(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Errorf("ungranted user got %d, want 403", recorder.Code)
	}
	_ = path
}
