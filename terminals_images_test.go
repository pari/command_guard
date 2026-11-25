package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startImageSSHServer runs an in-process SSH server that really executes exec requests, so the
// shell commands of copyImageToHost run for real. It also forwards direct-tcpip like a bastion.
func startImageSSHServer(t *testing.T) (port int, forwards *int32) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	forwards = new(int32)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					switch ch.ChannelType() {
					case "session":
						go serveImageExec(ch)
					case "direct-tcpip":
						atomic.AddInt32(forwards, 1)
						go serveImageForward(ch)
					default:
						ch.Reject(ssh.UnknownChannelType, "unsupported")
					}
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, forwards
}

func serveImageExec(newCh ssh.NewChannel) {
	ch, reqs, err := newCh.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		ssh.Unmarshal(req.Payload, &payload)
		req.Reply(true, nil)
		cmd := exec.Command("/bin/sh", "-c", payload.Command)
		cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
		cmd.Stdin = ch
		status := uint32(0)
		if err := cmd.Run(); err != nil {
			status = 1
		}
		ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
		return
	}
}

func serveImageForward(newCh ssh.NewChannel) {
	var target struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}
	ssh.Unmarshal(newCh.ExtraData(), &target)
	conn, err := net.Dial("tcp", net.JoinHostPort(target.Host, fmt.Sprint(target.Port)))
	if err != nil {
		newCh.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := newCh.Accept()
	if err != nil {
		conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	go func() { io.Copy(ch, conn); ch.Close() }()
	go func() { io.Copy(conn, ch); conn.Close() }()
}

// imageTestSessiond answers "list" with the given sessions.
func imageTestSessiond(t *testing.T, sessions ...TerminalSession) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cgsd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			bufio.NewReader(conn).ReadBytes('\n')
			out, _ := json.Marshal(sessiondResponse{OK: true, Sessions: sessions})
			conn.Write(append(out, '\n'))
			conn.Close()
		}
	}()
	return path
}

// imagesOnHost lists cg-paste files in the real /tmp, which the test server writes to.
func imagesOnHost(t *testing.T) map[string]bool {
	t.Helper()
	found, err := filepath.Glob("/tmp/cg-paste-*")
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, f := range found {
		set[f] = true
	}
	return set
}

// removeNewImages deletes whatever a test added, leaving earlier files alone.
func removeNewImages(t *testing.T, before map[string]bool) {
	t.Helper()
	t.Cleanup(func() {
		for f := range imagesOnHost(t) {
			if !before[f] {
				os.Remove(f)
			}
		}
	})
}

func newImages(t *testing.T, before map[string]bool) []string {
	t.Helper()
	var added []string
	for f := range imagesOnHost(t) {
		if !before[f] {
			added = append(added, f)
		}
	}
	return added
}

var testPNG = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 2000)...)

func TestTerminalImageUpload(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()
	port, _ := startImageSSHServer(t)

	cfg := cache.GetConfig()
	cfg.Hosts["img-host"] = Host{ConnectionType: "ssh", Host: "127.0.0.1", Port: port, Username: "u", Password: "p"}
	t.Cleanup(func() { delete(cfg.Hosts, "img-host") })

	running := TerminalSession{ID: "s1", Owner: "alice", HostRef: "img-host", Host: "127.0.0.1", Username: "u", Status: "running"}
	moved := running
	moved.ID, moved.Host = "moved", "10.9.9.9"
	relogin := running
	relogin.ID, relogin.Username = "relogin", "other"
	ended := running
	ended.ID, ended.Status = "ended", "exited"

	api := NewTerminalsAPI(authManager)
	api.socketPath = imageTestSessiond(t, running, moved, relogin, ended)
	admin := &Claims{Username: "admin", Role: RoleAdmin}

	post := func(claims *Claims, settings terminalSettings, id string, body io.Reader) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		api.handleImageUpload(rec, httptest.NewRequest("POST", "/api/terminals/"+id+"/images", body), claims, settings, id)
		return rec
	}
	settings := currentTerminalSettings()

	t.Run("CopiesAPrivateFileAndReturnsItsPath", func(t *testing.T) {
		before := imagesOnHost(t)
		removeNewImages(t, before)
		rec := post(admin, settings, "s1", bytes.NewReader(testPNG))
		AssertStatus(t, http.StatusCreated, rec.Code)
		var resp struct {
			Data struct {
				Path    string `json:"path"`
				HostRef string `json:"host_ref"`
				Size    int64  `json:"size"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(resp.Data.Path, "/tmp/cg-paste-") || !strings.HasSuffix(resp.Data.Path, ".png") {
			t.Fatalf("path = %q", resp.Data.Path)
		}
		if resp.Data.HostRef != "img-host" || resp.Data.Size != int64(len(testPNG)) {
			t.Fatalf("response = %+v", resp.Data)
		}
		got, err := os.ReadFile(resp.Data.Path)
		if err != nil || !bytes.Equal(got, testPNG) {
			t.Fatalf("file content differs (err %v, %d bytes)", err, len(got))
		}
		info, err := os.Stat(resp.Data.Path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, err %v", info.Mode().Perm(), err)
		}
		if added := newImages(t, before); len(added) != 1 {
			t.Fatalf("expected only the final file, found %v", added)
		}
	})

	t.Run("ExtensionComesFromTheBytes", func(t *testing.T) {
		before := imagesOnHost(t)
		removeNewImages(t, before)
		jpeg := append([]byte("\xff\xd8\xff\xe0"), make([]byte, 100)...)
		rec := post(admin, settings, "s1", bytes.NewReader(jpeg))
		AssertStatus(t, http.StatusCreated, rec.Code)
		if !strings.Contains(rec.Body.String(), ".jpg") {
			t.Fatalf("body = %s", rec.Body.String())
		}
	})

	t.Run("RefusesNonImages", func(t *testing.T) {
		before := imagesOnHost(t)
		for _, body := range []string{
			`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
			"just some text",
			"\x7fELF\x02\x01\x01",
		} {
			AssertStatus(t, http.StatusUnsupportedMediaType, post(admin, settings, "s1", strings.NewReader(body)).Code)
		}
		AssertStatus(t, http.StatusBadRequest, post(admin, settings, "s1", strings.NewReader("")).Code)
		if added := newImages(t, before); len(added) != 0 {
			t.Fatalf("refused uploads left files: %v", added)
		}
	})

	t.Run("OverSizeLeavesNothingBehind", func(t *testing.T) {
		before := imagesOnHost(t)
		removeNewImages(t, before)
		big := io.MultiReader(bytes.NewReader(testPNG), io.LimitReader(rand.Reader, terminalImageMaxBytes))
		// Unknown length, so the limit is hit while streaming rather than from the header.
		AssertStatus(t, http.StatusRequestEntityTooLarge, post(admin, settings, "s1", big).Code)
		if added := newImages(t, before); len(added) != 0 {
			t.Fatalf("over-size upload left files: %v", added)
		}
	})

	t.Run("CutOffUploadGetsNoFinalFile", func(t *testing.T) {
		before := imagesOnHost(t)
		removeNewImages(t, before)
		mux := http.NewServeMux()
		done := make(chan int, 1)
		mux.HandleFunc("/up", func(w http.ResponseWriter, r *http.Request) {
			rec := httptest.NewRecorder()
			api.handleImageUpload(rec, r, admin, settings, "s1")
			done <- rec.Code
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "POST /up HTTP/1.1\r\nHost: x\r\nContent-Length: 100000\r\n\r\n")
		conn.Write(testPNG)
		time.Sleep(200 * time.Millisecond)
		conn.Close()
		select {
		case code := <-done:
			if code == http.StatusCreated {
				t.Fatal("a cut-off upload was reported as a success")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("handler did not finish")
		}
		if added := newImages(t, before); len(added) != 0 {
			t.Fatalf("cut-off upload left files: %v", added)
		}
	})

	t.Run("ReadDeadlineIsExtendedThroughTheRealMiddleware", func(t *testing.T) {
		before := imagesOnHost(t)
		removeNewImages(t, before)
		mux := http.NewServeMux()
		mux.HandleFunc("/up", func(w http.ResponseWriter, r *http.Request) {
			api.handleImageUpload(w, r, admin, settings, "s1")
		})
		srv := httptest.NewUnstartedServer(securityHeadersMiddleware(shutdownMiddleware(mux)))
		srv.Config.ReadTimeout = 300 * time.Millisecond // Stands in for the real 30 s
		srv.Start()
		defer srv.Close()
		pr, pw := io.Pipe()
		go func() {
			pw.Write(testPNG[:1000])
			time.Sleep(900 * time.Millisecond)
			pw.Write(testPNG[1000:])
			pw.Close()
		}()
		resp, err := http.Post(srv.URL+"/up", "image/png", pr)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		AssertStatus(t, http.StatusCreated, resp.StatusCode)
	})

	t.Run("RefusedSessions", func(t *testing.T) {
		AssertStatus(t, http.StatusNotFound, post(admin, settings, "nope", bytes.NewReader(testPNG)).Code)
		AssertStatus(t, http.StatusNotFound, post(&Claims{Username: "mallory", Role: "user"}, settings, "s1", bytes.NewReader(testPNG)).Code)
		AssertStatus(t, http.StatusConflict, post(admin, settings, "ended", bytes.NewReader(testPNG)).Code)
		for _, id := range []string{"moved", "relogin"} {
			rec := post(admin, settings, id, bytes.NewReader(testPNG))
			AssertStatus(t, http.StatusConflict, rec.Code)
			if !strings.Contains(rec.Body.String(), "has changed since this session started") {
				t.Fatalf("%s: body = %s", id, rec.Body.String())
			}
		}
	})

	t.Run("DisabledInConfig", func(t *testing.T) {
		off := settings
		off.imagesEnabled = false
		AssertStatus(t, http.StatusForbidden, post(admin, off, "s1", bytes.NewReader(testPNG)).Code)

		disabled := false
		saved := cfg.Terminals
		cfg.Terminals = &TerminalsConfig{ImagesEnabled: &disabled}
		defer func() { cfg.Terminals = saved }()
		if currentTerminalSettings().imagesEnabled {
			t.Fatal("images_enabled: false was not applied")
		}
	})

	t.Run("RemovesOnlyOldImages", func(t *testing.T) {
		before := imagesOnHost(t)
		removeNewImages(t, before)
		old, recent := "/tmp/cg-paste-testold.png", "/tmp/cg-paste-testrecent.png"
		other := "/tmp/cg-pasteother-testold.png"
		for _, f := range []string{old, recent, other} {
			if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Remove(f) })
		}
		retention := settings.imageRetention
		stale := time.Now().Add(-retention - time.Hour)
		kept := time.Now().Add(-retention + time.Hour)
		os.Chtimes(old, stale, stale)
		os.Chtimes(other, stale, stale)
		os.Chtimes(recent, kept, kept)

		AssertStatus(t, http.StatusCreated, post(admin, settings, "s1", bytes.NewReader(testPNG)).Code)
		if _, err := os.Stat(old); !os.IsNotExist(err) {
			t.Fatalf("old image was not removed (err %v)", err)
		}
		for _, f := range []string{recent, other} {
			if _, err := os.Stat(f); err != nil {
				t.Fatalf("%s should have been kept: %v", f, err)
			}
		}
	})

	t.Run("GoesThroughTheBastion", func(t *testing.T) {
		bastionPort, forwards := startImageSSHServer(t)
		host := cfg.Hosts["img-host"]
		host.BastionHostRef = "img-bastion"
		host.Bastion = &Host{ConnectionType: "ssh", Host: "127.0.0.1", Port: bastionPort, Username: "b", Password: "p"}
		cfg.Hosts["img-host"] = host
		defer func() {
			host.BastionHostRef, host.Bastion = "", nil
			cfg.Hosts["img-host"] = host
		}()
		before := imagesOnHost(t)
		removeNewImages(t, before)
		AssertStatus(t, http.StatusCreated, post(admin, settings, "s1", bytes.NewReader(testPNG)).Code)
		if atomic.LoadInt32(forwards) != 1 {
			t.Fatalf("expected the connection to go through the bastion, forwards = %d", *forwards)
		}
	})

	t.Run("UnresolvedBastionFailsInsteadOfDialingDirectly", func(t *testing.T) {
		host := cfg.Hosts["img-host"]
		host.BastionHostRef, host.Bastion = "img-bastion", nil
		cfg.Hosts["img-host"] = host
		defer func() {
			host.BastionHostRef = ""
			cfg.Hosts["img-host"] = host
		}()
		before := imagesOnHost(t)
		rec := post(admin, settings, "s1", bytes.NewReader(testPNG))
		AssertStatus(t, http.StatusBadGateway, rec.Code)
		if added := newImages(t, before); len(added) != 0 {
			t.Fatalf("file written despite the unresolved bastion: %v", added)
		}
	})
}

func TestTerminalSafeFileName(t *testing.T) {
	long := strings.Repeat("a", 300)
	cases := map[string]string{
		"report.pdf":           "report.pdf",
		"Q3 final (v2).xlsx":   "Q3_final_v2_.xlsx",
		"it's.txt":             "it_s.txt",
		"$(reboot).sh":         "_reboot_.sh",
		"a;rm -rf x.txt":       "a_rm_-rf_x.txt",
		"`id`.txt":             "_id_.txt",
		"../../etc/passwd":     "passwd",
		`C:\Users\me\notes.md`: "notes.md",
		"रिपोर्ट.pdf":          "_.pdf",
		"new\nline.txt":        "new_line.txt",
		"":                     "file",
		"..":                   "file",
		"/":                    "file",
		"...---___":            "file",
		"日本語":                  "file",
		long + ".pdf":          long[:96] + ".pdf",
		long + "." + long:      long[:100],
	}
	for in, want := range cases {
		got := terminalSafeFileName(in)
		if got != want {
			t.Errorf("terminalSafeFileName(%q) = %q, want %q", in, got, want)
		}
		if !terminalUploadSuffix.MatchString("-" + got) {
			t.Errorf("terminalSafeFileName(%q) = %q is not a safe suffix", in, got)
		}
	}
}

func TestTerminalFileUpload(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()
	port, _ := startImageSSHServer(t)

	cfg := cache.GetConfig()
	cfg.Hosts["file-host"] = Host{ConnectionType: "ssh", Host: "127.0.0.1", Port: port, Username: "u", Password: "p"}
	t.Cleanup(func() { delete(cfg.Hosts, "file-host") })

	running := TerminalSession{ID: "s1", Owner: "alice", HostRef: "file-host", Host: "127.0.0.1", Username: "u", Status: "running"}
	moved := running
	moved.ID, moved.Host = "moved", "10.9.9.9"
	ended := running
	ended.ID, ended.Status = "ended", "exited"

	api := NewTerminalsAPI(authManager)
	api.socketPath = imageTestSessiond(t, running, moved, ended)
	admin := &Claims{Username: "admin", Role: RoleAdmin}
	settings := currentTerminalSettings()

	post := func(claims *Claims, settings terminalSettings, id, name string, body io.Reader) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		target := "/api/terminals/" + id + "/files?name=" + url.QueryEscape(name)
		api.handleFileUpload(rec, httptest.NewRequest("POST", target, body), claims, settings, id)
		return rec
	}
	pathOf := func(t *testing.T, rec *httptest.ResponseRecorder) string {
		t.Helper()
		AssertStatus(t, http.StatusCreated, rec.Code)
		var resp struct {
			Data struct {
				Path string `json:"path"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.Data.Path
	}

	t.Run("AnyTypeKeepsItsNameAndIsPrivate", func(t *testing.T) {
		before := imagesOnHost(t)
		removeNewImages(t, before)
		for name, body := range map[string][]byte{
			"report final.pdf": []byte("%PDF-1.7 not really"),
			"notes.txt":        []byte("just some text"),
			"tool":             []byte("\x7fELF\x02\x01\x01"),
			"picture.svg":      []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
		} {
			path := pathOf(t, post(admin, settings, "s1", name, bytes.NewReader(body)))
			if !regexp.MustCompile(`^/tmp/cg-paste-[A-Za-z0-9]+-` + regexp.QuoteMeta(terminalSafeFileName(name)) + `$`).MatchString(path) {
				t.Fatalf("%s: path = %q", name, path)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("%s: content differs (err %v, %d bytes)", name, err, len(got))
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("%s: mode = %v, err %v", name, info.Mode().Perm(), err)
			}
		}
		if added := newImages(t, before); len(added) != 4 {
			t.Fatalf("expected only the 4 final files, found %v", added)
		}
	})

	t.Run("HostileNameRunsNothing", func(t *testing.T) {
		before := imagesOnHost(t)
		removeNewImages(t, before)
		marker := filepath.Join(t.TempDir(), "ran")
		for _, name := range []string{
			"a'; touch " + marker + "; '.txt",
			"$(touch " + marker + ").txt",
			"`touch " + marker + "`.txt",
			"x\n touch " + marker + "\n.txt",
		} {
			path := pathOf(t, post(admin, settings, "s1", name, strings.NewReader("data")))
			if !terminalUploadSuffix.MatchString(strings.TrimPrefix(path, path[:strings.LastIndex(path, "-"+terminalSafeFileName(name))])) {
				t.Fatalf("%q: unsafe path %q", name, path)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("%q ran a command on the host (err %v)", name, err)
			}
		}
	})

	t.Run("CopyRefusesAnUnsafeSuffix", func(t *testing.T) {
		for _, suffix := range []string{"", "-a b", "-a'b", "-$(id)", ".PNG;id", "-a/b"} {
			if _, _, err := copyImageToHost(nil, nil, suffix, settings.imageRetention); err == nil || !strings.Contains(err.Error(), "unsafe") {
				t.Fatalf("suffix %q: err = %v", suffix, err)
			}
		}
	})

	t.Run("EmptyIsRefused", func(t *testing.T) {
		before := imagesOnHost(t)
		AssertStatus(t, http.StatusBadRequest, post(admin, settings, "s1", "a.txt", strings.NewReader("")).Code)
		if added := newImages(t, before); len(added) != 0 {
			t.Fatalf("empty upload left files: %v", added)
		}
	})

	t.Run("ExactlyTheLimitIsAcceptedAndOverIsRefused", func(t *testing.T) {
		before := imagesOnHost(t)
		removeNewImages(t, before)
		// Hidden behind a MultiReader for an unknown length, so the limit is hit while streaming.
		atLimit := io.MultiReader(io.LimitReader(rand.Reader, terminalFileMaxBytes))
		path := pathOf(t, post(admin, settings, "s1", "big.bin", atLimit))
		if info, err := os.Stat(path); err != nil || info.Size() != terminalFileMaxBytes {
			t.Fatalf("size = %v, err %v", info.Size(), err)
		}
		os.Remove(path)

		overStream := io.MultiReader(io.LimitReader(rand.Reader, terminalFileMaxBytes+1))
		AssertStatus(t, http.StatusRequestEntityTooLarge, post(admin, settings, "s1", "big.bin", overStream).Code)
		if added := newImages(t, before); len(added) != 0 {
			t.Fatalf("over-size upload left files: %v", added)
		}

		// A declared length is refused before connecting to the host.
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/terminals/s1/files?name=big.bin", strings.NewReader("x"))
		req.ContentLength = terminalFileMaxBytes + 1
		api.handleFileUpload(rec, req, admin, settings, "s1")
		AssertStatus(t, http.StatusRequestEntityTooLarge, rec.Code)
	})

	t.Run("CutOffUploadGetsNoFinalFile", func(t *testing.T) {
		before := imagesOnHost(t)
		removeNewImages(t, before)
		mux := http.NewServeMux()
		done := make(chan int, 1)
		mux.HandleFunc("/up", func(w http.ResponseWriter, r *http.Request) {
			rec := httptest.NewRecorder()
			api.handleFileUpload(rec, r, admin, settings, "s1")
			done <- rec.Code
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "POST /up?name=a.pdf HTTP/1.1\r\nHost: x\r\nContent-Length: 100000\r\n\r\npartial")
		time.Sleep(200 * time.Millisecond)
		conn.Close()
		select {
		case code := <-done:
			if code == http.StatusCreated {
				t.Fatal("a cut-off upload was reported as a success")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("handler did not finish")
		}
		if added := newImages(t, before); len(added) != 0 {
			t.Fatalf("cut-off upload left files: %v", added)
		}
	})

	t.Run("RefusedSessionsAndDisabled", func(t *testing.T) {
		before := imagesOnHost(t)
		body := func() io.Reader { return strings.NewReader("data") }
		AssertStatus(t, http.StatusNotFound, post(admin, settings, "nope", "a.txt", body()).Code)
		AssertStatus(t, http.StatusNotFound, post(&Claims{Username: "mallory", Role: "user"}, settings, "s1", "a.txt", body()).Code)
		AssertStatus(t, http.StatusConflict, post(admin, settings, "ended", "a.txt", body()).Code)
		AssertStatus(t, http.StatusConflict, post(admin, settings, "moved", "a.txt", body()).Code)
		off := settings
		off.imagesEnabled = false
		AssertStatus(t, http.StatusForbidden, post(admin, off, "s1", "a.txt", body()).Code)
		if added := newImages(t, before); len(added) != 0 {
			t.Fatalf("refused uploads left files: %v", added)
		}
	})

	t.Run("RemovesOldFilesWithNames", func(t *testing.T) {
		before := imagesOnHost(t)
		removeNewImages(t, before)
		old := "/tmp/cg-paste-testold-report.pdf"
		if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(old) })
		stale := time.Now().Add(-settings.imageRetention - time.Hour)
		os.Chtimes(old, stale, stale)
		pathOf(t, post(admin, settings, "s1", "a.txt", strings.NewReader("data")))
		if _, err := os.Stat(old); !os.IsNotExist(err) {
			t.Fatalf("old file was not removed (err %v)", err)
		}
	})

	t.Run("GoesThroughTheBastion", func(t *testing.T) {
		bastionPort, forwards := startImageSSHServer(t)
		host := cfg.Hosts["file-host"]
		host.BastionHostRef = "file-bastion"
		host.Bastion = &Host{ConnectionType: "ssh", Host: "127.0.0.1", Port: bastionPort, Username: "b", Password: "p"}
		cfg.Hosts["file-host"] = host
		defer func() {
			host.BastionHostRef, host.Bastion = "", nil
			cfg.Hosts["file-host"] = host
		}()
		before := imagesOnHost(t)
		removeNewImages(t, before)
		pathOf(t, post(admin, settings, "s1", "a.txt", strings.NewReader("data")))
		if atomic.LoadInt32(forwards) != 1 {
			t.Fatalf("expected the connection to go through the bastion, forwards = %d", *forwards)
		}
	})

	t.Run("RouteIsWired", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/terminals/nope/files?name=a.txt", strings.NewReader("data"))
		req.Header.Set("Authorization", "Bearer "+ts.AdminToken)
		api.Handle(rec, req)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Terminal session not found") {
			t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
		}
	})
}
