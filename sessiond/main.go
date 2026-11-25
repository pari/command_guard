/*
cg_sessiond - persistent SSH terminal session daemon for Command Guard.

This small companion process owns interactive SSH sessions (PTY + shell) so they
survive restarts of the main cguard web server. cguard talks to it over a
Unix domain socket and proxies browser WebSocket traffic to it.

The daemon knows nothing about config.json: cguard resolves the host
credentials and passes them in the "create" request. Authorization (who may
create, attach to or kill a session) is enforced entirely by cguard.

Protocol (one request per connection):
 1. Client sends a single JSON line: {"op": "ping"|"list"|"create"|"attach"|"resize"|"kill", ...}
 2. Daemon answers with a single JSON line: {"ok": true, ...} or {"ok": false, "error": "..."}
 3. For "attach" only, the connection then becomes a bidirectional framed stream:
    frame = 1 byte type + 4 byte big-endian payload length + payload
    client -> daemon: frameData (keystrokes), frameResize ({"cols","rows"} JSON), frameDetach
    daemon -> client: frameData (terminal output), frameReplayEnd (sent once after the scrollback
    replay, even when it is empty), frameExit (JSON), frameTakeover
*/
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

const daemonVersion = "1.3.0" // 1.2.0: bastion (jump host) support in create; 1.2.1: SSH handshake deadline; 1.3.0: host key pinning (host_key)

// Frame types shared with terminals.go in cguard. Keep both in sync.
const (
	frameData      byte = 0
	frameResize    byte = 1
	frameDetach    byte = 2
	frameExit      byte = 3
	frameTakeover  byte = 4
	frameReplayEnd byte = 5 // scrollback replay finished; live output follows
)

const (
	maxFramePayload   = 1 << 20
	keepaliveInterval = 30 * time.Second
	exitedRetention   = 2 * time.Minute
	clientWriteWait   = 15 * time.Second
)

// bastionParams are the jump-host credentials of a create request (cg_sessiond >= 1.2.0).
// Mirrors sessiondBastion in terminals.go.
type bastionParams struct {
	HostRef     string `json:"host_ref,omitempty"`
	Host        string `json:"host"`
	Port        int    `json:"port,omitempty"`
	Username    string `json:"username"`
	Password    string `json:"password,omitempty"`
	PEM         string `json:"pem,omitempty"`
	PEMPassword string `json:"pem_password,omitempty"`
	HostKey     string `json:"host_key,omitempty"` // pinned public key; empty = not checked
}

type request struct {
	Op    string `json:"op"`
	ID    string `json:"id,omitempty"`
	Owner string `json:"owner,omitempty"`
	Name  string `json:"name,omitempty"`

	// Connection parameters (create only)
	HostRef     string `json:"host_ref,omitempty"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	PEM         string `json:"pem,omitempty"`
	PEMPassword string `json:"pem_password,omitempty"`
	HostKey     string `json:"host_key,omitempty"` // pinned public key; empty = not checked
	Term        string `json:"term,omitempty"`

	// Terminal geometry (create, attach, resize)
	Cols int `json:"cols,omitempty"`
	Rows int `json:"rows,omitempty"`

	// Limits (create only)
	ScrollbackBytes int `json:"scrollback_bytes,omitempty"`
	IdleKillSeconds int `json:"idle_kill_seconds,omitempty"`

	// Bastion (create only): log into this host first and reach Host through it.
	Bastion *bastionParams `json:"bastion,omitempty"`
}

type response struct {
	OK       bool          `json:"ok"`
	Error    string        `json:"error,omitempty"`
	Session  *sessionInfo  `json:"session,omitempty"`
	Sessions []sessionInfo `json:"sessions,omitempty"`
	Version  string        `json:"version,omitempty"`
	PID      int           `json:"pid,omitempty"`
	Started  string        `json:"started_at,omitempty"`
}

type sessionInfo struct {
	ID             string    `json:"id"`
	Owner          string    `json:"owner"`
	HostRef        string    `json:"host_ref"`
	Name           string    `json:"name"`
	Host           string    `json:"host"`
	Username       string    `json:"username"`
	Bastion        string    `json:"bastion,omitempty"` // host_ref of the bastion the session goes through
	CreatedAt      time.Time `json:"created_at"`
	LastAttachedAt time.Time `json:"last_attached_at"`
	LastDetachedAt time.Time `json:"last_detached_at"`
	Attached       bool      `json:"attached"`
	Cols           int       `json:"cols"`
	Rows           int       `json:"rows"`
	Status         string    `json:"status"` // running | exited
	ExitCode       int       `json:"exit_code"`
	ExitReason     string    `json:"exit_reason,omitempty"`
	ExitedAt       time.Time `json:"exited_at,omitempty"`
}

type session struct {
	mu   sync.Mutex
	info sessionInfo

	client        *ssh.Client
	bastionClient *ssh.Client // nil for direct connections
	sess          *ssh.Session
	stdin         io.WriteCloser
	ring          *ringBuffer

	attached  net.Conn   // currently attached client connection, nil when detached
	attachMu  sync.Mutex // serializes writes to the attached connection
	idleKill  time.Duration
	exited    bool
	done      chan struct{}
	closeOnce sync.Once
}

type daemon struct {
	mu       sync.Mutex
	sessions map[string]*session
	started  time.Time
}

func main() {
	socketPath := flag.String("socket", "run/sessiond.sock", "Unix socket path")
	flag.Parse()

	log.SetFlags(log.LstdFlags)
	log.SetPrefix("[cg_sessiond] ")

	d := &daemon{sessions: make(map[string]*session), started: time.Now()}

	if conn, err := net.DialTimeout("unix", *socketPath, time.Second); err == nil {
		conn.Close()
		log.Fatalf("another daemon is already serving %s", *socketPath)
	}
	_ = os.Remove(*socketPath)
	if dir := dirOf(*socketPath); dir != "" {
		_ = os.MkdirAll(dir, 0700)
	}
	listener, err := net.Listen("unix", *socketPath)
	if err != nil {
		log.Fatalf("listen %s: %v", *socketPath, err)
	}
	_ = os.Chmod(*socketPath, 0600)
	log.Printf("version %s listening on %s (pid %d)", daemonVersion, *socketPath, os.Getpid())

	// Survive the controlling terminal going away and shut down cleanly on TERM/INT.
	signal.Ignore(syscall.SIGHUP, syscall.SIGPIPE)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigs
		log.Printf("received %v, closing %d session(s)", sig, d.count())
		listener.Close()
		d.closeAll("daemon shutting down")
		_ = os.Remove(*socketPath)
		os.Exit(0)
	}()

	go d.janitor()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("accept: %v", err)
			continue
		}
		go d.serve(conn)
	}
}

func dirOf(path string) string {
	if idx := strings.LastIndex(path, "/"); idx > 0 {
		return path[:idx]
	}
	return ""
}

func (d *daemon) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.sessions)
}

func (d *daemon) get(id string) (*session, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.sessions[id]
	return s, ok
}

func (d *daemon) closeAll(reason string) {
	d.mu.Lock()
	all := make([]*session, 0, len(d.sessions))
	for _, s := range d.sessions {
		all = append(all, s)
	}
	d.mu.Unlock()
	for _, s := range all {
		s.terminate(reason)
	}
}

// janitor removes exited sessions after a grace period and enforces idle kill.
func (d *daemon) janitor() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		d.mu.Lock()
		candidates := make([]*session, 0, len(d.sessions))
		for _, s := range d.sessions {
			candidates = append(candidates, s)
		}
		d.mu.Unlock()
		for _, s := range candidates {
			s.mu.Lock()
			exited, exitedAt := s.exited, s.info.ExitedAt
			idle := s.idleKill > 0 && s.attached == nil && !s.info.LastDetachedAt.IsZero() && now.Sub(s.info.LastDetachedAt) > s.idleKill
			id := s.info.ID
			s.mu.Unlock()
			if exited && now.Sub(exitedAt) > exitedRetention {
				d.mu.Lock()
				delete(d.sessions, id)
				d.mu.Unlock()
			} else if idle {
				log.Printf("session %s idle for more than %s, terminating", id, s.idleKill)
				s.terminate("idle timeout")
			}
		}
	}
}

func writeResponse(w *bufio.Writer, resp response) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(resp); err != nil {
		return err
	}
	return w.Flush()
}

func (d *daemon) serve(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReaderSize(conn, 64*1024)
	writer := bufio.NewWriter(conn)

	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		_ = writeResponse(writer, response{OK: false, Error: "invalid request: " + err.Error()})
		return
	}

	switch req.Op {
	case "ping":
		_ = writeResponse(writer, response{OK: true, Version: daemonVersion, PID: os.Getpid(), Started: d.started.Format(time.RFC3339)})
	case "list":
		_ = writeResponse(writer, response{OK: true, Sessions: d.list()})
	case "create":
		info, err := d.create(req)
		if err != nil {
			_ = writeResponse(writer, response{OK: false, Error: err.Error()})
			return
		}
		_ = writeResponse(writer, response{OK: true, Session: &info})
	case "kill":
		s, ok := d.get(req.ID)
		if !ok {
			_ = writeResponse(writer, response{OK: false, Error: "session not found"})
			return
		}
		s.terminate("killed")
		_ = writeResponse(writer, response{OK: true})
	case "rename":
		s, ok := d.get(req.ID)
		if !ok {
			_ = writeResponse(writer, response{OK: false, Error: "session not found"})
			return
		}
		info := s.rename(req.Name)
		_ = writeResponse(writer, response{OK: true, Session: &info})
	case "resize":
		s, ok := d.get(req.ID)
		if !ok {
			_ = writeResponse(writer, response{OK: false, Error: "session not found"})
			return
		}
		s.resize(req.Cols, req.Rows)
		_ = writeResponse(writer, response{OK: true})
	case "attach":
		s, ok := d.get(req.ID)
		if !ok {
			_ = writeResponse(writer, response{OK: false, Error: "session not found"})
			return
		}
		d.attach(s, conn, reader, writer, req)
	default:
		_ = writeResponse(writer, response{OK: false, Error: "unknown op"})
	}
}

func (d *daemon) list() []sessionInfo {
	d.mu.Lock()
	all := make([]*session, 0, len(d.sessions))
	for _, s := range d.sessions {
		all = append(all, s)
	}
	d.mu.Unlock()
	result := make([]sessionInfo, 0, len(all))
	for _, s := range all {
		result = append(result, s.snapshot())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

func (s *session) snapshot() sessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := s.info
	info.Attached = s.attached != nil
	return info
}

// rename changes the display name; an empty name falls back to the host ID, as on create.
func (s *session) rename(name string) sessionInfo {
	s.mu.Lock()
	if name == "" {
		name = s.info.HostRef
	}
	s.info.Name = name
	s.mu.Unlock()
	return s.snapshot()
}

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (d *daemon) create(req request) (sessionInfo, error) {
	if req.Host == "" || req.Username == "" {
		return sessionInfo{}, errors.New("host and username are required")
	}
	if req.Password == "" && req.PEM == "" {
		return sessionInfo{}, errors.New("no authentication method available (neither password nor PEM key configured)")
	}
	if req.Port <= 0 {
		req.Port = 22
	}
	if req.Cols <= 0 {
		req.Cols = 80
	}
	if req.Rows <= 0 {
		req.Rows = 24
	}
	if req.Term == "" {
		req.Term = "xterm-256color"
	}
	if req.ScrollbackBytes <= 0 {
		req.ScrollbackBytes = 2 * 1024 * 1024
	}

	cfg, err := clientConfig(req.Username, req.PEM, req.PEMPassword, req.Password, req.HostKey)
	if err != nil {
		return sessionInfo{}, err
	}
	address := fmt.Sprintf("%s:%d", req.Host, req.Port)

	var client, bastionClient *ssh.Client
	if req.Bastion == nil {
		client, err = dialSSH(address, cfg)
		if err != nil {
			return sessionInfo{}, fmt.Errorf("failed to connect to %s: %v", address, err)
		}
	} else {
		bastion := req.Bastion
		if bastion.Host == "" || bastion.Username == "" {
			return sessionInfo{}, errors.New("bastion host and username are required")
		}
		if bastion.Password == "" && bastion.PEM == "" {
			return sessionInfo{}, fmt.Errorf("bastion host %q has no authentication method (neither password nor PEM key configured)", bastion.HostRef)
		}
		if bastion.Port <= 0 {
			bastion.Port = 22
		}
		bastionCfg, err := clientConfig(bastion.Username, bastion.PEM, bastion.PEMPassword, bastion.Password, bastion.HostKey)
		if err != nil {
			return sessionInfo{}, fmt.Errorf("bastion host %q: %v", bastion.HostRef, err)
		}
		bastionAddress := fmt.Sprintf("%s:%d", bastion.Host, bastion.Port)
		bastionClient, err = dialSSH(bastionAddress, bastionCfg)
		if err != nil {
			return sessionInfo{}, fmt.Errorf("failed to connect to bastion host %q (%s): %v", bastion.HostRef, bastionAddress, err)
		}
		client, err = dialThroughBastion(bastionClient, address, cfg, connectTimeout)
		if err != nil {
			bastionClient.Close()
			return sessionInfo{}, fmt.Errorf("failed to connect to %s via bastion host %q: %v", address, bastion.HostRef, err)
		}
	}
	// closeClients releases both hops in the right order on any later failure.
	closeClients := func() {
		client.Close()
		if bastionClient != nil {
			bastionClient.Close()
		}
	}

	sess, err := client.NewSession()
	if err != nil {
		closeClients()
		return sessionInfo{}, fmt.Errorf("failed to open SSH session: %v", err)
	}
	// Servers usually reject env requests unless AcceptEnv allows them; ignore failures.
	_ = sess.Setenv("LANG", "en_US.UTF-8")
	_ = sess.Setenv("COLORTERM", "truecolor")

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty(req.Term, req.Rows, req.Cols, modes); err != nil {
		sess.Close()
		closeClients()
		return sessionInfo{}, fmt.Errorf("failed to request PTY: %v", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		closeClients()
		return sessionInfo{}, fmt.Errorf("failed to open stdin: %v", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		closeClients()
		return sessionInfo{}, fmt.Errorf("failed to open stdout: %v", err)
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		sess.Close()
		closeClients()
		return sessionInfo{}, fmt.Errorf("failed to open stderr: %v", err)
	}
	if err := sess.Shell(); err != nil {
		sess.Close()
		closeClients()
		return sessionInfo{}, fmt.Errorf("failed to start shell: %v", err)
	}

	now := time.Now()
	s := &session{
		info: sessionInfo{
			ID: newSessionID(), Owner: req.Owner, HostRef: req.HostRef, Name: req.Name,
			Host: req.Host, Username: req.Username, CreatedAt: now, LastDetachedAt: now,
			Cols: req.Cols, Rows: req.Rows, Status: "running",
		},
		client: client, bastionClient: bastionClient, sess: sess, stdin: stdin,
		ring:     newRingBuffer(req.ScrollbackBytes),
		idleKill: time.Duration(req.IdleKillSeconds) * time.Second,
		done:     make(chan struct{}),
	}
	if s.info.Name == "" {
		s.info.Name = req.HostRef
	}
	if req.Bastion != nil {
		s.info.Bastion = req.Bastion.HostRef
	}

	d.mu.Lock()
	d.sessions[s.info.ID] = s
	d.mu.Unlock()

	go s.pump(stdout)
	go s.pump(stderr)
	go s.keepalive()
	go s.waitForExit()

	if bastionClient != nil {
		log.Printf("session %s created for %s on %s (%s@%s via bastion %s)", s.info.ID, s.info.Owner, s.info.HostRef, req.Username, address, s.info.Bastion)
	} else {
		log.Printf("session %s created for %s on %s (%s@%s)", s.info.ID, s.info.Owner, s.info.HostRef, req.Username, address)
	}
	return s.snapshot(), nil
}

const connectTimeout = 30 * time.Second

// hostKeyCheck returns the host key callback and algorithms for one hop. cguard
// decides the policy (trust on first use, strict or off) and verifies the key
// itself before asking for a session; it then passes the key to pin here. An empty
// hostKey means checking is switched off in cguard.
func hostKeyCheck(hostKey string) (ssh.HostKeyCallback, []string, error) {
	hostKey = strings.TrimSpace(hostKey)
	if hostKey == "" {
		return ssh.InsecureIgnoreHostKey(), nil, nil
	}
	pinned, _, _, _, err := ssh.ParseAuthorizedKey([]byte(hostKey))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid pinned host key: %v", err)
	}
	algorithms := []string{pinned.Type()}
	if pinned.Type() == ssh.KeyAlgoRSA {
		algorithms = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	callback := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if bytes.Equal(key.Marshal(), pinned.Marshal()) {
			return nil
		}
		return fmt.Errorf("HOST KEY CHANGED: expected %s %s but the server presented %s %s; the session was not opened",
			pinned.Type(), ssh.FingerprintSHA256(pinned), key.Type(), ssh.FingerprintSHA256(key))
	}
	return callback, algorithms, nil
}

// clientConfig builds the SSH client configuration for one hop.
func clientConfig(username, pem, pemPassword, password, hostKey string) (*ssh.ClientConfig, error) {
	hostKeyCallback, hostKeyAlgorithms, err := hostKeyCheck(hostKey)
	if err != nil {
		return nil, err
	}
	authMethods := []ssh.AuthMethod{}
	if pem != "" {
		var signer ssh.Signer
		var err error
		if pemPassword != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(pem), []byte(pemPassword))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(pem))
		}
		if err != nil {
			return nil, fmt.Errorf("failed to parse PEM key: %v", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if password != "" {
		authMethods = append(authMethods, ssh.Password(password), ssh.KeyboardInteractive(func(user, instruction string, questions []string, echos []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range questions {
				answers[i] = password
			}
			return answers, nil
		}))
	}
	return &ssh.ClientConfig{
		User:              username,
		Auth:              authMethods,
		HostKeyCallback:   hostKeyCallback,
		HostKeyAlgorithms: hostKeyAlgorithms,
		Timeout:           connectTimeout,
	}, nil
}

// dialSSH is ssh.Dial with a deadline that also covers the SSH handshake
// (ClientConfig.Timeout only bounds the TCP connect). Same logic as in cguard.
func dialSSH(address string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	conn, err := net.DialTimeout("tcp", address, cfg.Timeout)
	if err != nil {
		return nil, err
	}
	if cfg.Timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(cfg.Timeout))
	}
	ncc, chans, reqs, err := ssh.NewClientConn(conn, address, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(ncc, chans, reqs), nil
}

// dialThroughBastion opens a TCP forward to address on the bastion connection and
// runs the target's SSH handshake over it. ClientConfig.Timeout only covers a direct
// TCP dial and the forwarded connection does not support deadlines, so both steps
// run under one timer; on expiry the forwarded connection is closed to unblock the
// handshake (the caller closes the bastion client). Same logic as in cguard.
func dialThroughBastion(bastionClient *ssh.Client, address string, cfg *ssh.ClientConfig, timeout time.Duration) (*ssh.Client, error) {
	type hopResult struct {
		client *ssh.Client
		err    error
	}
	results := make(chan hopResult, 1)
	conns := make(chan net.Conn, 1)
	go func() {
		conn, err := bastionClient.Dial("tcp", address)
		if err != nil {
			text := err.Error()
			switch {
			case strings.Contains(text, "administratively prohibited"):
				err = fmt.Errorf("the bastion refuses to forward connections to %s (check AllowTcpForwarding / PermitOpen in its sshd_config): %v", address, err)
			case strings.Contains(text, "connect failed"):
				err = fmt.Errorf("the bastion could not reach %s: %v", address, err)
			}
			results <- hopResult{err: err}
			return
		}
		conns <- conn
		ncc, chans, reqs, err := ssh.NewClientConn(conn, address, cfg) // closes conn itself on failure
		if err != nil {
			results <- hopResult{err: err}
			return
		}
		results <- hopResult{client: ssh.NewClient(ncc, chans, reqs)}
	}()
	select {
	case r := <-results:
		return r.client, r.err
	case <-time.After(timeout):
		select {
		case conn := <-conns:
			conn.Close()
		default:
		}
		return nil, fmt.Errorf("no SSH response from %s within %s", address, timeout)
	}
}

// pump copies remote output into the scrollback ring and to the attached client.
func (s *session) pump(r io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			s.mu.Lock()
			s.ring.Write(chunk)
			conn := s.attached
			s.mu.Unlock()
			if conn != nil {
				if werr := s.writeFrame(conn, frameData, chunk); werr != nil {
					s.detachConn(conn)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *session) keepalive() {
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			if _, _, err := s.client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				log.Printf("session %s keepalive failed: %v", s.info.ID, err)
				s.closeClients()
				return
			}
		}
	}
}

func (s *session) waitForExit() {
	err := s.sess.Wait()
	code, reason := 0, "shell exited"
	var exitErr *ssh.ExitError
	var missing *ssh.ExitMissingError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitStatus()
	case errors.As(err, &missing):
		code, reason = -1, "connection closed"
	default:
		code, reason = -1, "connection lost: "+err.Error()
	}
	s.finish(code, reason)
}

// finish marks the session as exited, notifies the attached client and releases resources.
func (s *session) finish(code int, reason string) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.exited = true
		s.info.Status = "exited"
		s.info.ExitCode = code
		s.info.ExitReason = reason
		s.info.ExitedAt = time.Now()
		conn := s.attached
		s.attached = nil
		s.mu.Unlock()
		close(s.done)
		if conn != nil {
			payload, _ := json.Marshal(map[string]interface{}{"exit_code": code, "reason": reason})
			_ = s.writeFrame(conn, frameExit, payload)
			conn.Close()
		}
		s.sess.Close()
		s.closeClients()
		log.Printf("session %s ended (%s, exit code %d)", s.info.ID, reason, code)
	})
}

// closeClients closes the target connection and then the bastion connection (if
// any). The order matters: closing the bastion first would surface as a misleading
// EOF on the target.
func (s *session) closeClients() {
	s.client.Close()
	if s.bastionClient != nil {
		s.bastionClient.Close()
	}
}

func (s *session) terminate(reason string) {
	s.mu.Lock()
	exited := s.exited
	s.mu.Unlock()
	if exited {
		return
	}
	// Closing the channel hangs up the remote PTY; the shell and its children receive SIGHUP.
	_ = s.sess.Signal(ssh.SIGHUP)
	s.sess.Close()
	s.closeClients()
	// waitForExit will normally report the exit; make sure the reason is recorded if it does not.
	go func() {
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			s.finish(-1, reason)
		}
	}()
}

func (s *session) resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	s.mu.Lock()
	s.info.Cols, s.info.Rows = cols, rows
	exited := s.exited
	s.mu.Unlock()
	if !exited {
		_ = s.sess.WindowChange(rows, cols)
	}
}

func (s *session) writeFrame(conn net.Conn, kind byte, payload []byte) error {
	s.attachMu.Lock()
	defer s.attachMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(clientWriteWait))
	defer conn.SetWriteDeadline(time.Time{})
	for len(payload) > maxFramePayload {
		if err := writeOneFrame(conn, kind, payload[:maxFramePayload]); err != nil {
			return err
		}
		payload = payload[maxFramePayload:]
	}
	return writeOneFrame(conn, kind, payload)
}

func writeOneFrame(w io.Writer, kind byte, payload []byte) error {
	header := make([]byte, 5)
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

func readFrame(r *bufio.Reader) (byte, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > maxFramePayload {
		return 0, nil, fmt.Errorf("frame too large: %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

// detachConn clears the attached connection if it is still the given one.
func (s *session) detachConn(conn net.Conn) {
	s.mu.Lock()
	if s.attached == conn {
		s.attached = nil
		s.info.LastDetachedAt = time.Now()
	}
	s.mu.Unlock()
	conn.Close()
}

func (d *daemon) attach(s *session, conn net.Conn, reader *bufio.Reader, writer *bufio.Writer, req request) {
	s.mu.Lock()
	if s.exited {
		s.mu.Unlock()
		_ = writeResponse(writer, response{OK: false, Error: "session has ended"})
		return
	}
	previous := s.attached
	s.attached = conn
	s.info.LastAttachedAt = time.Now()
	if req.Cols > 0 && req.Rows > 0 {
		s.info.Cols, s.info.Rows = req.Cols, req.Rows
	}
	cols, rows := s.info.Cols, s.info.Rows
	replay := s.ring.Bytes()
	info := s.info
	info.Attached = true
	s.mu.Unlock()

	if previous != nil {
		// Only one viewer at a time: tell the old client it was superseded.
		_ = s.writeFrame(previous, frameTakeover, nil)
		previous.Close()
	}

	if err := writeResponse(writer, response{OK: true, Session: &info}); err != nil {
		s.detachConn(conn)
		return
	}
	if len(replay) > 0 {
		if err := s.writeFrame(conn, frameData, replay); err != nil {
			s.detachConn(conn)
			return
		}
	}
	// The replay contains terminal queries (DA, cursor position, OSC colours, DECRQM ...)
	// that programs such as Claude Code sent at startup. The browser terminal would answer
	// them again, so it discards its own replies until this marker arrives.
	if err := s.writeFrame(conn, frameReplayEnd, nil); err != nil {
		s.detachConn(conn)
		return
	}
	// Force full-screen programs (vim, htop, Claude Code, ...) to repaint: TIOCSWINSZ
	// only raises SIGWINCH when the size actually changes, so nudge it by one column.
	if cols > 1 {
		_ = s.sess.WindowChange(rows, cols-1)
	}
	_ = s.sess.WindowChange(rows, cols)
	log.Printf("session %s attached (%dx%d)", s.info.ID, cols, rows)

readLoop:
	for {
		kind, payload, err := readFrame(reader)
		if err != nil {
			break
		}
		switch kind {
		case frameData:
			if _, err := s.stdin.Write(payload); err != nil {
				break readLoop
			}
		case frameResize:
			var size struct {
				Cols int `json:"cols"`
				Rows int `json:"rows"`
			}
			if json.Unmarshal(payload, &size) == nil {
				s.resize(size.Cols, size.Rows)
			}
		case frameDetach:
			s.detachConn(conn)
			log.Printf("session %s detached", s.info.ID)
			return
		}
	}
	s.detachConn(conn)
	log.Printf("session %s detached (connection closed)", s.info.ID)
}
