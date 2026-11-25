package main

/*
RFB (VNC protocol, RFC 6143) side of the Remote Desktops proxy.

cguard is the VNC client towards the real server: it negotiates the protocol
version, picks a security type, authenticates (None, VNC, Apple ARD, Tight,
VeNCrypt Plain/X509*), sends ClientInit with the shared flag set and reads
ServerInit. Towards the browser it then plays a VNC server that only offers
security type None and replays that ServerInit, so noVNC never needs (or sees)
the password.

After the handshake, messages from the browser are decoded one by one
(vncClientFilter) so view-only and "no clipboard" can be enforced on the server
and anything unexpected closes the session (fail closed). Server messages are
passed through untouched: their length depends on the negotiated encodings, so
they cannot be parsed safely without decoding the framebuffer.
*/

import (
	"crypto/aes"
	"crypto/des"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	vncDialTimeout = 20 * time.Second

	rfbSecInvalid  = 0
	rfbSecNone     = 1
	rfbSecVNC      = 2
	rfbSecTight    = 16
	rfbSecVeNCrypt = 19
	rfbSecARD      = 30

	vencPlain     = 256
	vencTLSNone   = 257
	vencTLSVnc    = 258
	vencTLSPlain  = 259
	vencX509None  = 260
	vencX509Vnc   = 261
	vencX509Plain = 262

	rfbEncExtendedMouseButtons = -316
	rfbEncExtendedClipboard    = int32(-1063131698) // 0xC0A1E5CE

	vncMaxCutText     = 10 << 20
	vncMaxReasonBytes = 4096
	vncMaxNameBytes   = 64 * 1024
	vncMaxEncodings   = 1024
)

func rfbSecurityName(t int) string {
	switch t {
	case rfbSecNone:
		return "None"
	case rfbSecVNC:
		return "VNC password"
	case rfbSecTight:
		return "Tight"
	case rfbSecVeNCrypt:
		return "VeNCrypt"
	case rfbSecARD:
		return "Apple ARD"
	case 5, 6:
		return "RealVNC RSA-AES"
	case 13, 129:
		return "Unix login"
	case 113, 114:
		return "UltraVNC MS-Logon"
	}
	return "type " + strconv.Itoa(t)
}

// vncServerInfo describes an authenticated server connection.
type vncServerInfo struct {
	Protocol     string // version spoken with the server, e.g. "3.8"
	ServerBanner string // what the server announced, e.g. "RFB 003.889"
	SecurityName string
	Width        int
	Height       int
	Name         string
	ServerInit   []byte // ServerInit as sent to the browser (without Tight extensions)
}

// vncTunnelConn closes the SSH connection(s) together with the forwarded channel.
type vncTunnelConn struct {
	net.Conn
	executor *SSHExecutor
	once     sync.Once
}

func (c *vncTunnelConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.Conn.Close()
		c.executor.Close()
	})
	return err
}

// dialVNCServer connects to the VNC server of conn (directly or through its SSH
// tunnel host), authenticates and completes the RFB initialisation. hosts must be
// the loaded config's hosts (bastions resolved).
func dialVNCServer(conn VNCConnection, hosts map[string]Host, timeout time.Duration) (net.Conn, *vncServerInfo, error) {
	normalizeVNCConnection(&conn)
	address := net.JoinHostPort(conn.targetHost(), strconv.Itoa(conn.port()))

	var raw net.Conn
	if conn.TunnelHostRef != "" {
		host, ok := hosts[conn.TunnelHostRef]
		if !ok {
			return nil, nil, fmt.Errorf("tunnel host %q not found", conn.TunnelHostRef)
		}
		normalizeHost(&host)
		executor := &SSHExecutor{host: host, connectTimeout: timeout}
		if err := executor.Connect(); err != nil {
			return nil, nil, fmt.Errorf("SSH tunnel host %q: %v", conn.TunnelHostRef, err)
		}
		type dialResult struct {
			c   net.Conn
			err error
		}
		results := make(chan dialResult, 1)
		go func() {
			c, err := executor.client.Dial("tcp", address)
			results <- dialResult{c, err}
		}()
		select {
		case res := <-results:
			if res.err != nil {
				executor.Close()
				text := res.err.Error()
				if strings.Contains(text, "administratively prohibited") {
					return nil, nil, fmt.Errorf("SSH tunnel host %q refuses to forward to %s (check AllowTcpForwarding / PermitOpen in its sshd_config): %v", conn.TunnelHostRef, address, res.err)
				}
				return nil, nil, fmt.Errorf("SSH tunnel host %q could not reach the VNC server at %s (is it running and listening there?): %v", conn.TunnelHostRef, address, res.err)
			}
			raw = &vncTunnelConn{Conn: res.c, executor: executor}
		case <-time.After(timeout):
			executor.Close()
			return nil, nil, fmt.Errorf("SSH tunnel host %q: opening a connection to %s timed out", conn.TunnelHostRef, address)
		}
	} else {
		c, err := net.DialTimeout("tcp", address, timeout)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot connect to VNC server %s: %v", address, err)
		}
		raw = c
	}

	// One timer for the whole handshake. Forwarded SSH channels do not support
	// deadlines, so on expiry the connection is closed to unblock the reads.
	var expired atomic.Bool
	timer := time.AfterFunc(timeout, func() {
		expired.Store(true)
		raw.Close()
	})
	server, info, err := rfbClientHandshake(raw, conn)
	if !timer.Stop() || expired.Load() {
		raw.Close()
		return nil, nil, fmt.Errorf("VNC handshake with %s timed out after %s", address, timeout)
	}
	if err != nil {
		raw.Close()
		return nil, nil, err
	}
	return server, info, nil
}

func readFull(r io.Reader, n int) ([]byte, error) {
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return buf, err
}

func readU8(r io.Reader) (byte, error) {
	b, err := readFull(r, 1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func readU16(r io.Reader) (uint16, error) {
	b, err := readFull(r, 2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func readU32(r io.Reader) (uint32, error) {
	b, err := readFull(r, 4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func writeU32(w io.Writer, v uint32) error {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	_, err := w.Write(b[:])
	return err
}

func readReason(r io.Reader) string {
	n, err := readU32(r)
	if err != nil || n == 0 {
		return ""
	}
	if n > vncMaxReasonBytes {
		n = vncMaxReasonBytes
	}
	b, err := readFull(r, int(n))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func rfbIOError(stage string, err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("VNC server closed the connection during %s", stage)
	}
	return fmt.Errorf("VNC %s failed: %v", stage, err)
}

// rfbClientHandshake runs the client side of the RFB handshake on c. It returns
// the connection to use afterwards (wrapped in TLS for VeNCrypt X509).
func rfbClientHandshake(c net.Conn, cfg VNCConnection) (net.Conn, *vncServerInfo, error) {
	info := &vncServerInfo{}
	banner, err := readFull(c, 12)
	if err != nil {
		return nil, nil, rfbIOError("protocol version exchange", err)
	}
	if !strings.HasPrefix(string(banner), "RFB ") || banner[11] != '\n' {
		return nil, nil, fmt.Errorf("not a VNC server (unexpected greeting %q)", strings.TrimSpace(string(banner)))
	}
	info.ServerBanner = strings.TrimSpace(string(banner))
	major, _ := strconv.Atoi(string(banner[4:7]))
	minor, _ := strconv.Atoi(string(banner[8:11]))
	version := 3.8
	switch {
	case major > 3:
		version = 3.8
	case major < 3:
		return nil, nil, fmt.Errorf("unsupported VNC protocol %s", info.ServerBanner)
	case minor >= 8: // includes Apple's 003.889
		version = 3.8
	case minor == 7:
		version = 3.7
	default:
		version = 3.3
	}
	reply := map[float64]string{3.3: "RFB 003.003\n", 3.7: "RFB 003.007\n", 3.8: "RFB 003.008\n"}[version]
	info.Protocol = strings.TrimPrefix(strings.TrimSpace(reply), "RFB 00")
	info.Protocol = strings.Replace(info.Protocol, ".00", ".", 1)
	if _, err := c.Write([]byte(reply)); err != nil {
		return nil, nil, rfbIOError("protocol version exchange", err)
	}

	// Security type selection
	chosen := 0
	if version >= 3.7 {
		n, err := readU8(c)
		if err != nil {
			return nil, nil, rfbIOError("security negotiation", err)
		}
		if n == 0 {
			return nil, nil, fmt.Errorf("VNC server refused the connection: %s", orUnknown(readReason(c)))
		}
		types, err := readFull(c, int(n))
		if err != nil {
			return nil, nil, rfbIOError("security negotiation", err)
		}
		offered := make([]int, len(types))
		for i, t := range types {
			offered[i] = int(t)
		}
		chosen = chooseRFBSecurity(cfg, offered)
		if chosen == rfbSecInvalid {
			return nil, nil, unsupportedSecurityError(cfg, offered)
		}
		if _, err := c.Write([]byte{byte(chosen)}); err != nil {
			return nil, nil, rfbIOError("security negotiation", err)
		}
	} else {
		t, err := readU32(c)
		if err != nil {
			return nil, nil, rfbIOError("security negotiation", err)
		}
		if t == 0 {
			return nil, nil, fmt.Errorf("VNC server refused the connection: %s", orUnknown(readReason(c)))
		}
		chosen = int(t)
		if chosen != rfbSecNone && chosen != rfbSecVNC {
			return nil, nil, unsupportedSecurityError(cfg, []int{chosen})
		}
		if !securityAllowedByConfig(cfg, chosen) {
			return nil, nil, unsupportedSecurityError(cfg, []int{chosen})
		}
	}
	info.SecurityName = rfbSecurityName(chosen)

	conn := c
	needResult := version >= 3.8 || chosen != rfbSecNone
	tight := false
	switch chosen {
	case rfbSecNone:
	case rfbSecVNC:
		if err := rfbVNCAuth(conn, cfg.Password); err != nil {
			return nil, nil, err
		}
	case rfbSecARD:
		if err := rfbARDAuth(conn, cfg.Username, cfg.Password); err != nil {
			return nil, nil, err
		}
	case rfbSecTight:
		tight = true
		sub, err := rfbTightAuth(conn, cfg)
		if err != nil {
			return nil, nil, err
		}
		info.SecurityName = "Tight (" + sub + ")"
		needResult = true
	case rfbSecVeNCrypt:
		upgraded, sub, err := rfbVeNCryptAuth(conn, cfg)
		if err != nil {
			return nil, nil, err
		}
		conn = upgraded
		info.SecurityName = "VeNCrypt " + sub
		needResult = true
	}

	if needResult {
		status, err := readU32(conn)
		if err != nil {
			return nil, nil, rfbIOError("authentication", err)
		}
		if status != 0 {
			reason := ""
			if version >= 3.8 {
				reason = readReason(conn)
			}
			msg := "VNC authentication failed (" + info.SecurityName + ")"
			if reason != "" {
				msg += ": " + reason
			} else if chosen == rfbSecARD {
				msg += ": check the macOS username and password, and that this user may use Screen Sharing"
			} else {
				msg += ": check the password"
			}
			return nil, nil, errors.New(msg)
		}
	}

	// ClientInit: always shared, so several viewers (and the console) coexist.
	if _, err := conn.Write([]byte{1}); err != nil {
		return nil, nil, rfbIOError("initialisation", err)
	}
	header, err := readFull(conn, 24)
	if err != nil {
		return nil, nil, rfbIOError("initialisation", err)
	}
	nameLen := binary.BigEndian.Uint32(header[20:24])
	if nameLen > vncMaxNameBytes {
		return nil, nil, fmt.Errorf("VNC server sent an oversized desktop name (%d bytes)", nameLen)
	}
	name, err := readFull(conn, int(nameLen))
	if err != nil {
		return nil, nil, rfbIOError("initialisation", err)
	}
	if tight {
		ext, err := readFull(conn, 8)
		if err != nil {
			return nil, nil, rfbIOError("Tight initialisation", err)
		}
		total := int(binary.BigEndian.Uint16(ext[0:2])) + int(binary.BigEndian.Uint16(ext[2:4])) + int(binary.BigEndian.Uint16(ext[4:6]))
		if _, err := readFull(conn, 16*total); err != nil {
			return nil, nil, rfbIOError("Tight initialisation", err)
		}
	}
	info.Width = int(binary.BigEndian.Uint16(header[0:2]))
	info.Height = int(binary.BigEndian.Uint16(header[2:4]))
	info.Name = string(name)
	info.ServerInit = append(append([]byte{}, header...), name...)
	return conn, info, nil
}

func orUnknown(s string) string {
	if s == "" {
		return "no reason given"
	}
	return s
}

func securityAllowedByConfig(cfg VNCConnection, t int) bool {
	switch cfg.authType() {
	case "none":
		return t == rfbSecNone || t == rfbSecTight
	case "vnc":
		return t == rfbSecVNC || t == rfbSecTight
	case "ard":
		return t == rfbSecARD
	case "vencrypt":
		return t == rfbSecVeNCrypt
	}
	return true
}

// chooseRFBSecurity picks the security type to use from what the server offers.
// auto: None when offered, ARD when a username is set, plain VNC auth when a
// password is set, then Tight and VeNCrypt (VeNCrypt last: once chosen there is
// no way back if it only offers anonymous TLS, which Go cannot do).
func chooseRFBSecurity(cfg VNCConnection, offered []int) int {
	has := map[int]bool{}
	for _, t := range offered {
		has[t] = true
	}
	var order []int
	switch cfg.authType() {
	case "none":
		order = []int{rfbSecNone, rfbSecTight}
	case "vnc":
		order = []int{rfbSecVNC, rfbSecTight}
	case "ard":
		order = []int{rfbSecARD}
	case "vencrypt":
		order = []int{rfbSecVeNCrypt}
	default:
		order = []int{rfbSecNone}
		if cfg.Username != "" && cfg.Password != "" {
			order = append(order, rfbSecARD)
		}
		if cfg.Password != "" {
			order = append(order, rfbSecVNC)
		}
		order = append(order, rfbSecTight, rfbSecVeNCrypt)
	}
	for _, t := range order {
		if has[t] {
			return t
		}
	}
	return rfbSecInvalid
}

func unsupportedSecurityError(cfg VNCConnection, offered []int) error {
	names := make([]string, 0, len(offered))
	realVNC := false
	for _, t := range offered {
		names = append(names, rfbSecurityName(t))
		if t == 5 || t == 6 {
			realVNC = true
		}
	}
	msg := fmt.Sprintf("none of the authentication methods the VNC server offers (%s) can be used with auth type %q", strings.Join(names, ", "), cfg.authType())
	if cfg.authType() == "auto" && cfg.Password == "" {
		msg += "; no password is configured"
	}
	if realVNC {
		msg += ". RealVNC Server: set Authentication to \"VNC password\" and Encryption to \"Prefer off\" (or \"Always off\") in its Options > Security"
	}
	return errors.New(msg)
}

// vncDESKey builds the classic VNC DES key: up to 8 password bytes, each with
// its bits reversed.
func vncDESKey(password string) []byte {
	key := make([]byte, 8)
	copy(key, password)
	for i, b := range key {
		var r byte
		for bit := 0; bit < 8; bit++ {
			if b&(1<<uint(bit)) != 0 {
				r |= 1 << uint(7-bit)
			}
		}
		key[i] = r
	}
	return key
}

func vncDESResponse(password string, challenge []byte) ([]byte, error) {
	block, err := des.NewCipher(vncDESKey(password))
	if err != nil {
		return nil, err
	}
	out := make([]byte, 16)
	block.Encrypt(out[0:8], challenge[0:8])
	block.Encrypt(out[8:16], challenge[8:16])
	return out, nil
}

func rfbVNCAuth(c io.ReadWriter, password string) error {
	if password == "" {
		return errors.New("the VNC server asks for a VNC password but none is configured")
	}
	challenge, err := readFull(c, 16)
	if err != nil {
		return rfbIOError("VNC password authentication", err)
	}
	response, err := vncDESResponse(password, challenge)
	if err != nil {
		return err
	}
	if _, err := c.Write(response); err != nil {
		return rfbIOError("VNC password authentication", err)
	}
	return nil
}

func padLeft(b []byte, n int) []byte {
	if len(b) >= n {
		return b[len(b)-n:]
	}
	out := make([]byte, n)
	copy(out[n-len(b):], b)
	return out
}

// rfbARDAuth implements Apple Remote Desktop authentication (security type 30):
// Diffie-Hellman with the server's parameters, MD5 of the shared secret as an
// AES-128 key, and the username/password (64 bytes each, NUL terminated, random
// padding) encrypted with AES-128-ECB.
func rfbARDAuth(c io.ReadWriter, username, password string) error {
	if username == "" || password == "" {
		return errors.New("the VNC server (macOS Screen Sharing) asks for a macOS username and password but they are not configured")
	}
	params, err := readFull(c, 4)
	if err != nil {
		return rfbIOError("Apple ARD authentication", err)
	}
	generator := new(big.Int).SetBytes(params[0:2])
	keyLen := int(binary.BigEndian.Uint16(params[2:4]))
	if keyLen <= 0 || keyLen > 1024 {
		return fmt.Errorf("Apple ARD authentication: invalid key length %d", keyLen)
	}
	values, err := readFull(c, 2*keyLen)
	if err != nil {
		return rfbIOError("Apple ARD authentication", err)
	}
	prime := new(big.Int).SetBytes(values[:keyLen])
	serverPub := new(big.Int).SetBytes(values[keyLen:])
	if prime.Sign() <= 0 {
		return errors.New("Apple ARD authentication: invalid prime")
	}
	privBytes := make([]byte, keyLen)
	if _, err := rand.Read(privBytes); err != nil {
		return err
	}
	priv := new(big.Int).SetBytes(privBytes)
	priv.Mod(priv, prime)
	clientPub := padLeft(new(big.Int).Exp(generator, priv, prime).Bytes(), keyLen)
	shared := padLeft(new(big.Int).Exp(serverPub, priv, prime).Bytes(), keyLen)
	key := md5.Sum(shared)

	creds := make([]byte, 128)
	if _, err := rand.Read(creds); err != nil {
		return err
	}
	user := []byte(username)
	if len(user) > 63 {
		user = user[:63]
	}
	pass := []byte(password)
	if len(pass) > 63 {
		pass = pass[:63]
	}
	copy(creds[0:], user)
	creds[len(user)] = 0
	copy(creds[64:], pass)
	creds[64+len(pass)] = 0

	block, err := aes.NewCipher(key[:])
	if err != nil {
		return err
	}
	encrypted := make([]byte, 128)
	for i := 0; i < 128; i += aes.BlockSize {
		block.Encrypt(encrypted[i:i+aes.BlockSize], creds[i:i+aes.BlockSize])
	}
	if _, err := c.Write(append(encrypted, clientPub...)); err != nil {
		return rfbIOError("Apple ARD authentication", err)
	}
	return nil
}

// rfbTightAuth runs the Tight security type: NOTUNNEL, then None or VNC auth.
func rfbTightAuth(c io.ReadWriter, cfg VNCConnection) (string, error) {
	tunnels, err := readU32(c)
	if err != nil {
		return "", rfbIOError("Tight negotiation", err)
	}
	if tunnels > 64 {
		return "", errors.New("Tight negotiation: too many tunnel types")
	}
	if tunnels > 0 {
		caps, err := readFull(c, 16*int(tunnels))
		if err != nil {
			return "", rfbIOError("Tight negotiation", err)
		}
		notunnel := false
		for i := 0; i < int(tunnels); i++ {
			code := binary.BigEndian.Uint32(caps[i*16:])
			vendor := string(caps[i*16+4 : i*16+8])
			if code == 0 || (code == 1 && vendor == "SICR") {
				notunnel = true
			}
		}
		if !notunnel {
			return "", errors.New("Tight negotiation: the server requires a tunnel type this app does not support")
		}
		if err := writeU32(c, 0); err != nil {
			return "", rfbIOError("Tight negotiation", err)
		}
	}
	count, err := readU32(c)
	if err != nil {
		return "", rfbIOError("Tight negotiation", err)
	}
	if count == 0 {
		return "no auth", nil
	}
	if count > 64 {
		return "", errors.New("Tight negotiation: too many auth types")
	}
	caps, err := readFull(c, 16*int(count))
	if err != nil {
		return "", rfbIOError("Tight negotiation", err)
	}
	offered := map[string]uint32{}
	names := []string{}
	for i := 0; i < int(count); i++ {
		code := binary.BigEndian.Uint32(caps[i*16:])
		sig := string(caps[i*16+4 : i*16+16])
		offered[sig] = code
		names = append(names, strings.TrimRight(sig, "_"))
	}
	if code, ok := offered["STDVNOAUTH__"]; ok && cfg.authType() != "vnc" {
		if err := writeU32(c, code); err != nil {
			return "", rfbIOError("Tight negotiation", err)
		}
		return "no auth", nil
	}
	if code, ok := offered["STDVVNCAUTH_"]; ok && cfg.authType() != "none" {
		if err := writeU32(c, code); err != nil {
			return "", rfbIOError("Tight negotiation", err)
		}
		return "VNC password", rfbVNCAuth(c, cfg.Password)
	}
	return "", fmt.Errorf("Tight negotiation: no usable auth type (server offers %s)", strings.Join(names, ", "))
}

// rfbVeNCryptAuth runs VeNCrypt 0.2. Plain and the X509 (certificate TLS)
// subtypes are supported; the anonymous-TLS subtypes (TLSNone/TLSVnc/TLSPlain)
// need anonymous Diffie-Hellman, which Go's TLS does not implement.
func rfbVeNCryptAuth(c net.Conn, cfg VNCConnection) (net.Conn, string, error) {
	version, err := readFull(c, 2)
	if err != nil {
		return nil, "", rfbIOError("VeNCrypt negotiation", err)
	}
	if version[0] != 0 || version[1] < 2 {
		return nil, "", fmt.Errorf("unsupported VeNCrypt version %d.%d", version[0], version[1])
	}
	if _, err := c.Write([]byte{0, 2}); err != nil {
		return nil, "", rfbIOError("VeNCrypt negotiation", err)
	}
	ack, err := readU8(c)
	if err != nil {
		return nil, "", rfbIOError("VeNCrypt negotiation", err)
	}
	if ack != 0 {
		return nil, "", errors.New("VeNCrypt: the server rejected version 0.2")
	}
	n, err := readU8(c)
	if err != nil {
		return nil, "", rfbIOError("VeNCrypt negotiation", err)
	}
	if n == 0 {
		return nil, "", errors.New("VeNCrypt: the server offers no subtypes")
	}
	raw, err := readFull(c, 4*int(n))
	if err != nil {
		return nil, "", rfbIOError("VeNCrypt negotiation", err)
	}
	has := map[uint32]bool{}
	offered := []string{}
	for i := 0; i < int(n); i++ {
		t := binary.BigEndian.Uint32(raw[i*4:])
		has[t] = true
		offered = append(offered, vencryptName(t))
	}
	order := []uint32{}
	if cfg.Username != "" && cfg.Password != "" {
		order = append(order, vencX509Plain)
	}
	if cfg.Password != "" {
		order = append(order, vencX509Vnc)
	}
	order = append(order, vencX509None)
	if cfg.Username != "" && cfg.Password != "" {
		order = append(order, vencPlain)
	}
	if cfg.Password != "" {
		order = append(order, rfbSecVNC)
	}
	order = append(order, rfbSecNone)
	chosen := uint32(0)
	for _, t := range order {
		if has[t] {
			chosen = t
			break
		}
	}
	if chosen == 0 {
		msg := fmt.Sprintf("VeNCrypt: no usable subtype (server offers %s)", strings.Join(offered, ", "))
		if has[vencTLSNone] || has[vencTLSVnc] || has[vencTLSPlain] {
			msg += ". Anonymous TLS is not supported: enable X509 subtypes or plain VncAuth on the server (e.g. TigerVNC -SecurityTypes X509Vnc,VncAuth), and prefer tunnelling through an SSH host"
		}
		return nil, "", errors.New(msg)
	}
	if err := writeU32(c, chosen); err != nil {
		return nil, "", rfbIOError("VeNCrypt negotiation", err)
	}
	name := vencryptName(chosen)
	conn := c
	if chosen >= vencX509None && chosen <= vencX509Plain {
		ok, err := readU8(c)
		if err != nil {
			return nil, "", rfbIOError("VeNCrypt TLS setup", err)
		}
		if ok != 1 {
			return nil, "", errors.New("VeNCrypt: the server is not ready for the TLS handshake")
		}
		serverName := cfg.targetHost()
		if h, _, err := net.SplitHostPort(serverName); err == nil {
			serverName = h
		}
		tlsConn := tls.Client(c, &tls.Config{ServerName: serverName, InsecureSkipVerify: cfg.TLSSkipVerify, MinVersion: tls.VersionTLS12})
		if err := tlsConn.Handshake(); err != nil {
			msg := fmt.Sprintf("VeNCrypt TLS handshake failed: %v", err)
			if !cfg.TLSSkipVerify {
				msg += " (enable \"Skip TLS certificate verification\" for self-signed server certificates)"
			}
			return nil, "", errors.New(msg)
		}
		conn = tlsConn
	}
	switch chosen {
	case rfbSecVNC, vencX509Vnc:
		if err := rfbVNCAuth(conn, cfg.Password); err != nil {
			return nil, "", err
		}
	case vencPlain, vencX509Plain:
		user, pass := []byte(cfg.Username), []byte(cfg.Password)
		msg := make([]byte, 8, 8+len(user)+len(pass))
		binary.BigEndian.PutUint32(msg[0:4], uint32(len(user)))
		binary.BigEndian.PutUint32(msg[4:8], uint32(len(pass)))
		msg = append(append(msg, user...), pass...)
		if _, err := conn.Write(msg); err != nil {
			return nil, "", rfbIOError("VeNCrypt Plain authentication", err)
		}
	}
	return conn, name, nil
}

func vencryptName(t uint32) string {
	switch t {
	case rfbSecNone:
		return "None"
	case rfbSecVNC:
		return "VncAuth"
	case vencPlain:
		return "Plain"
	case vencTLSNone:
		return "TLSNone"
	case vencTLSVnc:
		return "TLSVnc"
	case vencTLSPlain:
		return "TLSPlain"
	case vencX509None:
		return "X509None"
	case vencX509Vnc:
		return "X509Vnc"
	case vencX509Plain:
		return "X509Plain"
	}
	return "type " + strconv.FormatUint(uint64(t), 10)
}

// ---------------------------------------------------------------------------
// Browser side
// ---------------------------------------------------------------------------

// rfbServeBrowserHandshake plays the server side of the handshake towards noVNC:
// RFB 3.8, security type None, SecurityResult OK, then the real server's ServerInit.
func rfbServeBrowserHandshake(r io.Reader, w io.Writer, serverInit []byte) error {
	if _, err := w.Write([]byte("RFB 003.008\n")); err != nil {
		return err
	}
	version, err := readFull(r, 12)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(version), "RFB 003.") {
		return fmt.Errorf("unexpected viewer version %q", strings.TrimSpace(string(version)))
	}
	legacy := string(version) < "RFB 003.007\n"
	if legacy {
		// 3.3: the server decides the security type (u32) and sends no result for None.
		if err := writeU32(w, rfbSecNone); err != nil {
			return err
		}
	} else {
		if _, err := w.Write([]byte{1, rfbSecNone}); err != nil {
			return err
		}
		choice, err := readU8(r)
		if err != nil {
			return err
		}
		if choice != rfbSecNone {
			return fmt.Errorf("viewer chose unsupported security type %d", choice)
		}
		if string(version) >= "RFB 003.008\n" {
			if err := writeU32(w, 0); err != nil {
				return err
			}
		}
	}
	if _, err := readU8(r); err != nil { // ClientInit shared flag: ignored, the proxy always shares
		return err
	}
	_, err = w.Write(serverInit)
	return err
}

// vncClientFilter decodes browser-to-server RFB messages and forwards the allowed ones.
type vncClientFilter struct {
	viewOnly  bool
	clipboard func() bool // live "clipboard allowed" for this connection
	onInput   func()      // keyboard / pointer activity
}

var errVNCUnexpectedMessage = errors.New("unexpected message from the viewer")

func (f *vncClientFilter) run(r io.Reader, w io.Writer) error {
	for {
		t, err := readU8(r)
		if err != nil {
			return err
		}
		var msg []byte
		forward := true
		input := false
		switch t {
		case 0: // SetPixelFormat
			msg, err = readMessage(r, t, 19)
		case 2: // SetEncodings
			msg, err = f.readSetEncodings(r)
		case 3: // FramebufferUpdateRequest
			msg, err = readMessage(r, t, 9)
		case 4: // KeyEvent
			msg, err = readMessage(r, t, 7)
			forward, input = !f.viewOnly, true
		case 5: // PointerEvent (extended mouse buttons are never negotiated, see readSetEncodings)
			msg, err = readMessage(r, t, 5)
			if err == nil && msg[1]&0x80 != 0 {
				return fmt.Errorf("%w: extended pointer event", errVNCUnexpectedMessage)
			}
			forward, input = !f.viewOnly, true
		case 6: // ClientCutText (negative length = extended clipboard)
			msg, err = readMessage(r, t, 7)
			if err == nil {
				length := int64(int32(binary.BigEndian.Uint32(msg[4:8])))
				if length < 0 {
					length = -length
				}
				if length > vncMaxCutText {
					return fmt.Errorf("%w: clipboard text too large (%d bytes)", errVNCUnexpectedMessage, length)
				}
				var body []byte
				body, err = readFull(r, int(length))
				msg = append(msg, body...)
			}
			forward = !f.viewOnly && (f.clipboard == nil || f.clipboard())
		case 150: // EnableContinuousUpdates
			msg, err = readMessage(r, t, 9)
		case 248: // Fence
			msg, err = readMessage(r, t, 8)
			if err == nil {
				n := int(msg[8])
				if n > 64 {
					return fmt.Errorf("%w: fence payload too large", errVNCUnexpectedMessage)
				}
				var payload []byte
				payload, err = readFull(r, n)
				msg = append(msg, payload...)
			}
		case 250: // xvp (shutdown / reboot / reset)
			msg, err = readMessage(r, t, 3)
			forward = !f.viewOnly
		case 251: // SetDesktopSize
			msg, err = readMessage(r, t, 7)
			if err == nil {
				var screens []byte
				screens, err = readFull(r, 16*int(msg[6]))
				msg = append(msg, screens...)
			}
			forward = !f.viewOnly
		case 255: // QEMU client message
			msg, err = readMessage(r, t, 1)
			if err == nil {
				if msg[1] != 0 {
					return fmt.Errorf("%w: QEMU subtype %d", errVNCUnexpectedMessage, msg[1])
				}
				var rest []byte
				rest, err = readFull(r, 10)
				msg = append(msg, rest...)
			}
			forward, input = !f.viewOnly, true
		default:
			return fmt.Errorf("%w: type %d", errVNCUnexpectedMessage, t)
		}
		if err != nil {
			return err
		}
		if input && f.onInput != nil && !f.viewOnly {
			f.onInput()
		}
		if forward {
			if _, err := w.Write(msg); err != nil {
				return err
			}
		}
	}
}

func readMessage(r io.Reader, t byte, rest int) ([]byte, error) {
	msg := make([]byte, 1+rest)
	msg[0] = t
	_, err := io.ReadFull(r, msg[1:])
	return msg, err
}

// readSetEncodings strips pseudo-encodings that would change the format of later
// viewer messages (extended mouse buttons) or that are not wanted (extended
// clipboard when the clipboard is off).
func (f *vncClientFilter) readSetEncodings(r io.Reader) ([]byte, error) {
	head, err := readMessage(r, 2, 3)
	if err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(head[2:4]))
	if n > vncMaxEncodings {
		return nil, fmt.Errorf("%w: too many encodings", errVNCUnexpectedMessage)
	}
	raw, err := readFull(r, 4*n)
	if err != nil {
		return nil, err
	}
	clipboard := !f.viewOnly && (f.clipboard == nil || f.clipboard())
	kept := make([]byte, 0, len(raw))
	for i := 0; i < n; i++ {
		enc := int32(binary.BigEndian.Uint32(raw[i*4:]))
		if enc == rfbEncExtendedMouseButtons || (enc == rfbEncExtendedClipboard && !clipboard) {
			continue
		}
		kept = append(kept, raw[i*4:i*4+4]...)
	}
	binary.BigEndian.PutUint16(head[2:4], uint16(len(kept)/4))
	return append(head, kept...), nil
}
