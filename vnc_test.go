package main

import (
	"bytes"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// Fake VNC server
// ---------------------------------------------------------------------------

type fakeVNCServer struct {
	t         *testing.T
	ln        net.Listener
	secTypes  []byte
	password  string
	ardUser   string
	ardPass   string
	name      string
	mu        sync.Mutex
	shared    []byte
	received  bytes.Buffer
	authCalls int
}

func newFakeVNCServer(t *testing.T, secTypes []byte) *fakeVNCServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeVNCServer{t: t, ln: ln, secTypes: secTypes, password: "secret12", ardUser: "macuser", ardPass: "mac-pass", name: "Fake Desktop"}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeVNCServer) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeVNCServer) receivedBytes() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte{}, f.received.Bytes()...)
}

func (f *fakeVNCServer) fail(c net.Conn, reason string) {
	binary.Write(c, binary.BigEndian, uint32(1))
	binary.Write(c, binary.BigEndian, uint32(len(reason)))
	c.Write([]byte(reason))
	c.Close()
}

func (f *fakeVNCServer) serve(c net.Conn) {
	defer c.Close()
	c.Write([]byte("RFB 003.008\n"))
	if _, err := readFull(c, 12); err != nil {
		return
	}
	c.Write(append([]byte{byte(len(f.secTypes))}, f.secTypes...))
	choice, err := readU8(c)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.authCalls++
	f.mu.Unlock()
	switch choice {
	case rfbSecNone:
	case rfbSecVNC:
		challenge := make([]byte, 16)
		rand.Read(challenge)
		c.Write(challenge)
		resp, err := readFull(c, 16)
		if err != nil {
			return
		}
		want, _ := vncDESResponse(f.password, challenge)
		if !bytes.Equal(resp, want) {
			f.fail(c, "wrong password")
			return
		}
	case rfbSecARD:
		prime, _ := rand.Prime(rand.Reader, 512)
		keyLen := 64
		priv, _ := rand.Int(rand.Reader, prime)
		g := big.NewInt(2)
		pub := new(big.Int).Exp(g, priv, prime)
		msg := []byte{0, 2, 0, byte(keyLen)}
		msg = append(msg, padLeft(prime.Bytes(), keyLen)...)
		msg = append(msg, padLeft(pub.Bytes(), keyLen)...)
		c.Write(msg)
		reply, err := readFull(c, 128+keyLen)
		if err != nil {
			return
		}
		clientPub := new(big.Int).SetBytes(reply[128:])
		shared := padLeft(new(big.Int).Exp(clientPub, priv, prime).Bytes(), keyLen)
		key := md5.Sum(shared)
		block, _ := aes.NewCipher(key[:])
		plain := make([]byte, 128)
		for i := 0; i < 128; i += 16 {
			block.Decrypt(plain[i:i+16], reply[i:i+16])
		}
		user := string(plain[:bytes.IndexByte(plain[:64], 0)])
		pass := string(plain[64 : 64+bytes.IndexByte(plain[64:], 0)])
		if user != f.ardUser || pass != f.ardPass {
			f.fail(c, "ARD login failed")
			return
		}
	default:
		f.fail(c, "unsupported")
		return
	}
	binary.Write(c, binary.BigEndian, uint32(0))
	shared, err := readU8(c)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.shared = append(f.shared, shared)
	f.mu.Unlock()
	init := make([]byte, 24)
	binary.BigEndian.PutUint16(init[0:], 800)
	binary.BigEndian.PutUint16(init[2:], 600)
	init[4], init[5], init[7] = 32, 24, 1
	binary.BigEndian.PutUint32(init[20:], uint32(len(f.name)))
	c.Write(append(init, f.name...))
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			f.mu.Lock()
			f.received.Write(buf[:n])
			f.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func fakeConn(f *fakeVNCServer, auth string) VNCConnection {
	return VNCConnection{VNCID: "fake", Host: "127.0.0.1", Port: f.port(), AuthType: auth, Username: f.ardUser, Password: f.password}
}

// ---------------------------------------------------------------------------
// Protocol tests
// ---------------------------------------------------------------------------

func TestVNCDESKeyBitReversal(t *testing.T) {
	key := vncDESKey("\x01\x80ab")
	if key[0] != 0x80 || key[1] != 0x01 || key[4] != 0 {
		t.Fatalf("unexpected key %x", key)
	}
	if len(vncDESKey("longer-than-eight")) != 8 {
		t.Fatal("key must be 8 bytes")
	}
}

func TestVNCHandshakeAuthTypes(t *testing.T) {
	t.Run("VNCPassword", func(t *testing.T) {
		f := newFakeVNCServer(t, []byte{rfbSecVNC})
		c, info, err := dialVNCServer(fakeConn(f, ""), nil, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		if info.Name != "Fake Desktop" || info.Width != 800 || info.Height != 600 || info.SecurityName != "VNC password" {
			t.Fatalf("unexpected info %+v", info)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.shared) != 1 || f.shared[0] != 1 {
			t.Fatalf("ClientInit must request a shared session, got %v", f.shared)
		}
	})
	t.Run("WrongPassword", func(t *testing.T) {
		f := newFakeVNCServer(t, []byte{rfbSecVNC})
		conn := fakeConn(f, "vnc")
		conn.Password = "nope"
		_, _, err := dialVNCServer(conn, nil, 5*time.Second)
		if err == nil || !strings.Contains(err.Error(), "wrong password") {
			t.Fatalf("expected auth failure with reason, got %v", err)
		}
	})
	t.Run("AppleARD", func(t *testing.T) {
		f := newFakeVNCServer(t, []byte{rfbSecVNC, rfbSecARD})
		ard := fakeConn(f, "")
		ard.Password = f.ardPass
		c, info, err := dialVNCServer(ard, nil, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		if info.SecurityName != "Apple ARD" {
			t.Fatalf("auto with username should prefer ARD, got %s", info.SecurityName)
		}
		conn := fakeConn(f, "ard")
		conn.Password = "bad"
		if _, _, err := dialVNCServer(conn, nil, 5*time.Second); err == nil || !strings.Contains(err.Error(), "ARD login failed") {
			t.Fatalf("expected ARD failure, got %v", err)
		}
	})
	t.Run("NonePreferred", func(t *testing.T) {
		f := newFakeVNCServer(t, []byte{rfbSecVNC, rfbSecNone})
		_, info, err := dialVNCServer(fakeConn(f, ""), nil, 5*time.Second)
		if err != nil || info.SecurityName != "None" {
			t.Fatalf("got %v %v", info, err)
		}
	})
	t.Run("UnsupportedRealVNC", func(t *testing.T) {
		f := newFakeVNCServer(t, []byte{5, 6})
		_, _, err := dialVNCServer(fakeConn(f, ""), nil, 5*time.Second)
		if err == nil || !strings.Contains(err.Error(), "RealVNC Server") {
			t.Fatalf("expected RealVNC hint, got %v", err)
		}
	})
	t.Run("ExplicitTypeNotOffered", func(t *testing.T) {
		f := newFakeVNCServer(t, []byte{rfbSecVNC})
		if _, _, err := dialVNCServer(fakeConn(f, "ard"), nil, 5*time.Second); err == nil {
			t.Fatal("ard must not fall back to VNC auth")
		}
	})
	t.Run("Timeout", func(t *testing.T) {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		defer ln.Close()
		go func() {
			c, err := ln.Accept()
			if err == nil {
				time.Sleep(2 * time.Second)
				c.Close()
			}
		}()
		conn := VNCConnection{VNCID: "x", Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port}
		start := time.Now()
		_, _, err := dialVNCServer(conn, nil, 300*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(start) > 1500*time.Millisecond {
			t.Fatalf("expected quick timeout, got %v after %s", err, time.Since(start))
		}
	})
}

// ---------------------------------------------------------------------------
// Browser handshake + filter
// ---------------------------------------------------------------------------

func encU32(v int32) uint32 { return uint32(v) }

func buildViewerMessages() (all []byte, kinds []string) {
	var b bytes.Buffer
	add := func(kind string, msg []byte) {
		b.Write(msg)
		kinds = append(kinds, kind)
	}
	pf := make([]byte, 20)
	add("pixelformat", pf)
	enc := []int32{0, 7, rfbEncExtendedMouseButtons, rfbEncExtendedClipboard, -223}
	msg := []byte{2, 0, 0, byte(len(enc))}
	for _, e := range enc {
		msg = binary.BigEndian.AppendUint32(msg, uint32(e))
	}
	add("encodings", msg)
	add("fbreq", []byte{3, 0, 0, 0, 0, 0, 3, 32, 2, 88})
	add("key", []byte{4, 1, 0, 0, 0, 0, 0, 0x61})
	add("pointer", []byte{5, 1, 0, 10, 0, 20})
	cut := []byte{6, 0, 0, 0, 0, 0, 0, 5}
	add("cut", append(cut, "hello"...))
	add("fence", []byte{248, 0, 0, 0, 0, 0, 0, 1, 2, 9, 9})
	add("qemukey", []byte{255, 0, 0, 1, 0, 0, 0, 0x61, 0, 0, 0, 30})
	add("resize", append([]byte{251, 0, 3, 32, 2, 88, 1, 0}, make([]byte, 16)...))
	return b.Bytes(), kinds
}

func TestVNCClientFilter(t *testing.T) {
	msgs, _ := buildViewerMessages()
	run := func(viewOnly, clipboard bool) ([]byte, int) {
		var out bytes.Buffer
		inputs := 0
		f := &vncClientFilter{viewOnly: viewOnly, clipboard: func() bool { return clipboard }, onInput: func() { inputs++ }}
		err := f.run(bytes.NewReader(msgs), &out)
		if err != io.EOF {
			t.Fatalf("expected EOF at end of stream, got %v", err)
		}
		return out.Bytes(), inputs
	}
	control, inputs := run(false, true)
	if inputs != 3 {
		t.Errorf("expected 3 input events, got %d", inputs)
	}
	if bytes.Contains(control, binary.BigEndian.AppendUint32(nil, encU32(rfbEncExtendedMouseButtons))) {
		t.Error("extended mouse buttons must always be stripped")
	}
	if !bytes.Contains(control, binary.BigEndian.AppendUint32(nil, encU32(rfbEncExtendedClipboard))) {
		t.Error("extended clipboard must be kept when the clipboard is allowed")
	}
	for _, want := range [][]byte{{4, 1, 0, 0, 0, 0, 0, 0x61}, []byte("hello"), {251, 0, 3, 32}} {
		if !bytes.Contains(control, want) {
			t.Errorf("control session must forward %x", want)
		}
	}

	view, inputs := run(true, true)
	if inputs != 0 {
		t.Errorf("view-only must not count input, got %d", inputs)
	}
	for _, banned := range [][]byte{{4, 1, 0, 0, 0, 0, 0, 0x61}, {5, 1, 0, 10, 0, 20}, []byte("hello"), {255, 0, 0, 1}, {251, 0, 3, 32}} {
		if bytes.Contains(view, banned) {
			t.Errorf("view-only session forwarded %x", banned)
		}
	}
	if !bytes.Contains(view, []byte{3, 0, 0, 0, 0, 0, 3, 32, 2, 88}) || !bytes.Contains(view, []byte{248, 0, 0, 0}) {
		t.Error("view-only must still forward update requests and fences")
	}

	noClip, _ := run(false, false)
	if bytes.Contains(noClip, []byte("hello")) || bytes.Contains(noClip, binary.BigEndian.AppendUint32(nil, encU32(rfbEncExtendedClipboard))) {
		t.Error("clipboard off must drop cut text and the extended clipboard encoding")
	}
	if !bytes.Contains(noClip, []byte{4, 1, 0, 0, 0, 0, 0, 0x61}) {
		t.Error("clipboard off must still forward keys")
	}

	for name, bad := range map[string][]byte{
		"unknown type":      {77, 0, 0},
		"extended pointer":  {5, 0x81, 0, 1, 0, 1, 0},
		"huge cut":          {6, 0, 0, 0, 0x7f, 0xff, 0xff, 0xff},
		"qemu other":        {255, 1, 0, 0},
		"oversize fence":    {248, 0, 0, 0, 0, 0, 0, 0, 200},
		"too many encoding": {2, 0, 0xff, 0xff},
	} {
		f := &vncClientFilter{}
		if err := f.run(bytes.NewReader(bad), io.Discard); err == nil || err == io.EOF {
			t.Errorf("%s: expected protocol error, got %v", name, err)
		}
	}
}

func TestVNCBrowserHandshake(t *testing.T) {
	serverSide, viewer := net.Pipe()
	defer serverSide.Close()
	defer viewer.Close()
	serverInit := []byte("SERVERINIT-BYTES")
	done := make(chan error, 1)
	go func() { done <- rfbServeBrowserHandshake(serverSide, serverSide, serverInit) }()
	banner, _ := readFull(viewer, 12)
	if string(banner) != "RFB 003.008\n" {
		t.Fatalf("banner %q", banner)
	}
	viewer.Write([]byte("RFB 003.008\n"))
	types, _ := readFull(viewer, 2)
	if !bytes.Equal(types, []byte{1, rfbSecNone}) {
		t.Fatalf("browser must only be offered None, got %v", types)
	}
	viewer.Write([]byte{rfbSecNone})
	result, _ := readU32(viewer)
	if result != 0 {
		t.Fatal("security result must be OK")
	}
	viewer.Write([]byte{0})
	got, _ := readFull(viewer, len(serverInit))
	if !bytes.Equal(got, serverInit) {
		t.Fatalf("ServerInit not replayed: %q", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Config validation
// ---------------------------------------------------------------------------

func TestVNCConfigValidation(t *testing.T) {
	hosts := map[string]Host{"ssh1": {Host: "h", Username: "u", Password: "p"}, "win": {ConnectionType: "winrm", Host: "w"}}
	cases := map[string]struct {
		conns []VNCConnection
		want  string
	}{
		"ok direct":       {[]VNCConnection{{VNCID: "a", Host: "10.0.0.1"}}, ""},
		"ok tunnel":       {[]VNCConnection{{VNCID: "a", TunnelHostRef: "ssh1", Port: 5901}}, ""},
		"bad id":          {[]VNCConnection{{VNCID: "a b", Host: "x"}}, "vnc_id"},
		"duplicate":       {[]VNCConnection{{VNCID: "a", Host: "x"}, {VNCID: "a", Host: "y"}}, "duplicate"},
		"no host":         {[]VNCConnection{{VNCID: "a"}}, "host is required"},
		"missing tunnel":  {[]VNCConnection{{VNCID: "a", TunnelHostRef: "nope"}}, "non-existent tunnel host"},
		"winrm tunnel":    {[]VNCConnection{{VNCID: "a", TunnelHostRef: "win"}}, "must be an SSH host"},
		"bad port":        {[]VNCConnection{{VNCID: "a", Host: "x", Port: 70000}}, "port"},
		"bad auth":        {[]VNCConnection{{VNCID: "a", Host: "x", AuthType: "rsa"}}, "auth_type"},
		"ard needs creds": {[]VNCConnection{{VNCID: "a", Host: "x", AuthType: "ard", Password: "p"}}, "username and password"},
		"vnc needs pass":  {[]VNCConnection{{VNCID: "a", Host: "x", AuthType: "vnc"}}, "requires a password"},
	}
	for name, tc := range cases {
		err := validateVNCConnections(tc.conns, hosts)
		if tc.want == "" && err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: expected %q, got %v", name, tc.want, err)
		}
	}
	idle := -1
	if validateVNCSettings(&VNCSettings{IdleDisconnectMinutes: &idle}) == nil {
		t.Error("negative idle must be rejected")
	}
	grants := normalizeVNCGrants([]VNCGrant{{VNCID: "a", Level: "view"}, {VNCID: "a", Level: "CONTROL"}, {VNCID: "b"}})
	if len(grants) != 2 || grants[0].Level != vncLevelControl || grants[1].Level != vncLevelView {
		t.Errorf("unexpected normalized grants %+v", grants)
	}
	if err := validateVNCGrants("bob", []VNCGrant{{VNCID: "zzz", Level: "view"}}, map[string]bool{"a": true}); err == nil {
		t.Error("grant to a missing connection must be rejected")
	}
}

// ---------------------------------------------------------------------------
// API + WebSocket end to end
// ---------------------------------------------------------------------------

func TestVNCAPIEndToEnd(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()
	f := newFakeVNCServer(t, []byte{rfbSecVNC})

	mustStatus := func(t *testing.T, resp *http.Response, status int) map[string]interface{} {
		t.Helper()
		body := AssertJSONResponse(t, resp)
		resp.Body.Close()
		if resp.StatusCode != status {
			t.Fatalf("expected %d, got %d: %v", status, resp.StatusCode, body)
		}
		return body
	}

	conn := map[string]interface{}{"vnc_id": "desk1", "name": "Desk One", "host": "127.0.0.1", "port": f.port(), "auth_type": "vnc", "password": f.password}
	created := mustStatus(t, ts.DoAuthRequest(t, "POST", "/api/config/vnc-connections", conn), http.StatusCreated)
	if data := created["data"].(map[string]interface{}); data["password"] != nil || data["has_password"] != true {
		t.Fatalf("password must be redacted: %v", data)
	}
	mustStatus(t, ts.DoAuthRequest(t, "POST", "/api/config/vnc-connections", conn), http.StatusConflict)

	// GET /api/config must not leak it, and PUT /api/config must keep it.
	got := mustStatus(t, ts.DoAuthRequest(t, "GET", "/api/config", nil), http.StatusOK)
	full := got["data"].(map[string]interface{})
	if strings.Contains(fmt.Sprint(full["vnc_connections"]), f.password) {
		t.Fatal("GET /api/config leaked the VNC password")
	}
	delete(full, "vnc_connections")
	mustStatus(t, ts.DoAuthRequest(t, "PUT", "/api/config", full), http.StatusOK)
	if c, ok := findVNCConnection(cache.GetConfig(), "desk1"); !ok || c.Password != f.password {
		t.Fatal("PUT /api/config must preserve vnc_connections and their passwords")
	}

	// Update without password keeps it; test endpoint authenticates.
	conn["password"] = ""
	conn["name"] = "Desk 1"
	mustStatus(t, ts.DoAuthRequest(t, "PUT", "/api/config/vnc-connections/desk1", conn), http.StatusOK)
	test := mustStatus(t, ts.DoAuthRequest(t, "POST", "/api/config/vnc-connections/desk1/test", nil), http.StatusOK)
	if test["data"].(map[string]interface{})["desktop_name"] != "Fake Desktop" {
		t.Fatalf("unexpected test result %v", test)
	}

	// Users and mapping
	for _, name := range []string{"viewer", "operator"} {
		mustStatus(t, ts.DoAuthRequest(t, "POST", "/api/users", map[string]interface{}{"username": name, "enabled": true, "temporary_password": "temporary-password-123"}), http.StatusCreated)
	}
	mustStatus(t, ts.DoAuthRequest(t, "PUT", "/api/config/vnc-connections/desk1/access", map[string]interface{}{
		"access": []map[string]string{{"username": "viewer", "level": "view"}, {"username": "operator", "level": "control"}},
	}), http.StatusOK)
	mustStatus(t, ts.DoAuthRequest(t, "PUT", "/api/config/vnc-connections/desk1/access", map[string]interface{}{
		"access": []map[string]string{{"username": "ghost", "level": "view"}},
	}), http.StatusBadRequest)
	access := mustStatus(t, ts.DoAuthRequest(t, "GET", "/api/config/vnc-connections/desk1/access", nil), http.StatusOK)
	if n := len(access["data"].(map[string]interface{})["access"].([]interface{})); n != 2 {
		t.Fatalf("expected 2 users with access, got %d", n)
	}
	mustStatus(t, ts.DoAuthRequest(t, "DELETE", "/api/config/vnc-connections/desk1", nil), http.StatusConflict)

	userToken := func(name string) map[string]string {
		authManager.mu.RLock()
		u := authManager.users[name]
		authManager.mu.RUnlock()
		tok, err := authManager.generateJWTFor(name, RoleUser, u.TokenVersion)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]string{"Authorization": "Bearer " + tok}
	}
	stranger := map[string]string{}
	mustStatus(t, ts.DoAuthRequest(t, "POST", "/api/users", map[string]interface{}{"username": "stranger", "enabled": true, "temporary_password": "temporary-password-123"}), http.StatusCreated)
	stranger = userToken("stranger")
	list := mustStatus(t, ts.DoRequest(t, "GET", "/api/vnc/connections", nil, stranger), http.StatusOK)
	if n := len(list["data"].(map[string]interface{})["connections"].([]interface{})); n != 0 {
		t.Fatalf("unmapped user must see no connections, got %d", n)
	}
	mustStatus(t, ts.DoRequest(t, "POST", "/api/vnc/connections/desk1/sessions", nil, stranger), http.StatusNotFound)

	// connect opens a session and runs the browser handshake over the WebSocket
	connect := func(t *testing.T, headers map[string]string) (string, *websocket.Conn) {
		t.Helper()
		body := mustStatus(t, ts.DoRequest(t, "POST", "/api/vnc/connections/desk1/sessions", map[string]interface{}{}, headers), http.StatusCreated)
		data := body["data"].(map[string]interface{})
		wsURL := "ws" + strings.TrimPrefix(ts.BaseURL, "http") + data["ws_path"].(string)
		ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		read := func(n int) []byte {
			var out []byte
			for len(out) < n {
				_, msg, err := ws.ReadMessage()
				if err != nil {
					t.Fatalf("read: %v", err)
				}
				out = append(out, msg...)
			}
			return out
		}
		if string(read(12)) != "RFB 003.008\n" {
			t.Fatal("bad banner")
		}
		ws.WriteMessage(websocket.BinaryMessage, []byte("RFB 003.008\n"))
		read(2)
		ws.WriteMessage(websocket.BinaryMessage, []byte{rfbSecNone})
		read(4)
		ws.WriteMessage(websocket.BinaryMessage, []byte{1})
		init := read(24 + len("Fake Desktop"))
		if !strings.HasSuffix(string(init), "Fake Desktop") {
			t.Fatalf("bad ServerInit %q", init)
		}
		// A ticket is single use
		if _, _, err := websocket.DefaultDialer.Dial(wsURL, nil); err == nil {
			t.Fatal("ticket must be single use")
		}
		return data["session_id"].(string), ws
	}

	t.Run("ViewOnlyEnforcedOnServer", func(t *testing.T) {
		sid, ws := connect(t, userToken("viewer"))
		defer ws.Close()
		before := len(f.receivedBytes())
		ws.WriteMessage(websocket.BinaryMessage, []byte{4, 1, 0, 0, 0, 0, 0, 0x7a}) // key 'z'
		ws.WriteMessage(websocket.BinaryMessage, []byte{3, 0, 0, 0, 0, 0, 0, 10, 0, 10})
		WaitForCondition(t, func() bool { return len(f.receivedBytes()) >= before+10 }, 3*time.Second, "update request forwarded")
		if bytes.Contains(f.receivedBytes()[before:], []byte{4, 1, 0, 0, 0, 0, 0, 0x7a}) {
			t.Fatal("key event from a view-only user reached the VNC server")
		}
		// Grant removed -> disconnected by enforcement
		mustStatus(t, ts.DoAuthRequest(t, "PUT", "/api/config/vnc-connections/desk1/access", map[string]interface{}{
			"access": []map[string]string{{"username": "operator", "level": "control"}},
		}), http.StatusOK)
		testVNCAPI.enforce(time.Now())
		s := testVNCAPI.sessions[sid]
		if s.Status != "ended" || !strings.Contains(s.EndReason, "removed") {
			t.Fatalf("expected session ended for removed access, got %s %q", s.Status, s.EndReason)
		}
	})

	t.Run("ControlAndKick", func(t *testing.T) {
		headers := userToken("operator")
		sid, ws := connect(t, headers)
		defer ws.Close()
		ws.WriteMessage(websocket.BinaryMessage, []byte{4, 1, 0, 0, 0, 0, 0, 0x79})
		WaitForCondition(t, func() bool { return bytes.Contains(f.receivedBytes(), []byte{4, 1, 0, 0, 0, 0, 0, 0x79}) }, 3*time.Second, "key forwarded")

		list := mustStatus(t, ts.DoRequest(t, "GET", "/api/vnc/connections", nil, headers), http.StatusOK)
		conns := list["data"].(map[string]interface{})["connections"].([]interface{})
		if len(conns) != 1 || len(conns[0].(map[string]interface{})["viewers"].([]interface{})) != 1 {
			t.Fatalf("expected one viewer listed: %v", conns)
		}
		if _, leaked := conns[0].(map[string]interface{})["target"]; leaked {
			t.Fatal("users must not see the connection target")
		}
		mustStatus(t, ts.DoAuthRequest(t, "DELETE", "/api/vnc/sessions/"+sid, nil), http.StatusOK)
		got := mustStatus(t, ts.DoRequest(t, "GET", "/api/vnc/sessions/"+sid, nil, headers), http.StatusOK)
		if reason := got["data"].(map[string]interface{})["end_reason"]; reason != "Disconnected by the administrator" {
			t.Fatalf("unexpected end reason %v", reason)
		}
		ws.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				break
			}
		}
	})

	t.Run("ConnectionChangeDisconnects", func(t *testing.T) {
		admin := map[string]string{"Authorization": "Bearer " + ts.AdminToken}
		sid, ws := connect(t, admin)
		defer ws.Close()
		renamed := map[string]interface{}{"vnc_id": "desk1", "name": "Renamed", "host": "127.0.0.1", "port": f.port(), "auth_type": "vnc"}
		mustStatus(t, ts.DoAuthRequest(t, "PUT", "/api/config/vnc-connections/desk1", renamed), http.StatusOK)
		testVNCAPI.enforce(time.Now())
		if s := testVNCAPI.sessions[sid]; s.Status != "active" {
			t.Fatalf("a rename must not disconnect viewers, got %s %q", s.Status, s.EndReason)
		}
		renamed["password"] = "different"
		mustStatus(t, ts.DoAuthRequest(t, "PUT", "/api/config/vnc-connections/desk1", renamed), http.StatusOK)
		testVNCAPI.enforce(time.Now())
		if s := testVNCAPI.sessions[sid]; s.Status != "ended" || !strings.Contains(s.EndReason, "settings were changed") {
			t.Fatalf("a credential change must disconnect viewers, got %s %q", s.Status, s.EndReason)
		}
		renamed["password"] = f.password
		mustStatus(t, ts.DoAuthRequest(t, "PUT", "/api/config/vnc-connections/desk1", renamed), http.StatusOK)
	})

	t.Run("Thumbnail", func(t *testing.T) {
		orig := vncThumbnailDir
		vncThumbnailDir = t.TempDir()
		defer func() { vncThumbnailDir = orig }()
		headers := userToken("operator")
		req, _ := http.NewRequest("PUT", ts.BaseURL+"/api/vnc/connections/desk1/thumbnail", bytes.NewReader([]byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2}))
		req.Header.Set("Authorization", headers["Authorization"])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		mustStatus(t, resp, http.StatusOK)
		req, _ = http.NewRequest("PUT", ts.BaseURL+"/api/vnc/connections/desk1/thumbnail", strings.NewReader("<svg>"))
		req.Header.Set("Authorization", headers["Authorization"])
		resp, _ = http.DefaultClient.Do(req)
		mustStatus(t, resp, http.StatusBadRequest)
		got := mustStatus(t, ts.DoRequest(t, "GET", "/api/vnc/connections/desk1/thumbnail", nil, headers), http.StatusOK)
		if !strings.HasPrefix(got["data"].(map[string]interface{})["data_url"].(string), "data:image/jpeg;base64,") {
			t.Fatal("thumbnail must come back as a JPEG data URL")
		}
		mustStatus(t, ts.DoRequest(t, "GET", "/api/vnc/connections/desk1/thumbnail", nil, stranger), http.StatusNotFound)
	})

	t.Run("HostTunnelProtection", func(t *testing.T) {
		tunnel := map[string]interface{}{"vnc_id": "tunneled", "tunnel_host_ref": "test-ssh-server", "port": 5901, "auth_type": "none"}
		mustStatus(t, ts.DoAuthRequest(t, "POST", "/api/config/vnc-connections", tunnel), http.StatusCreated)
		resp := ts.DoAuthRequest(t, "DELETE", "/api/config/hosts/test-ssh-server", nil)
		resp.Body.Close()
		AssertStatus(t, http.StatusConflict, resp.StatusCode)
		bad := map[string]interface{}{"vnc_id": "bad", "tunnel_host_ref": "test-winrm-server"}
		mustStatus(t, ts.DoAuthRequest(t, "POST", "/api/config/vnc-connections", bad), http.StatusBadRequest)
		mustStatus(t, ts.DoAuthRequest(t, "DELETE", "/api/config/vnc-connections/tunneled", nil), http.StatusOK)
	})

	t.Run("DisableEndsSessions", func(t *testing.T) {
		sid, ws := connect(t, userToken("operator"))
		defer ws.Close()
		full := ts.GetConfig(t)
		full["vnc"] = map[string]interface{}{"enabled": false}
		ts.UpdateConfig(t, full)
		testVNCAPI.enforce(time.Now())
		if s := testVNCAPI.sessions[sid]; s.Status != "ended" || !strings.Contains(s.EndReason, "disabled") {
			t.Fatalf("expected session ended after disable, got %s %q", s.Status, s.EndReason)
		}
		mustStatus(t, ts.DoRequest(t, "GET", "/api/vnc/connections", nil, userToken("operator")), http.StatusForbidden)
	})
}
