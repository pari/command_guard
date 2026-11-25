package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestSSHExecutorExplainsWhenPasswordIsNotOffered(t *testing.T) {
	host, port := startSSHTestServer(t, &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, errors.New("test key rejected")
		},
	})

	const password = "test-password-must-not-appear"
	executor := &SSHExecutor{host: Host{
		Host:     host,
		Port:     port,
		Username: "test-user",
		Password: password,
	}}

	err := executor.Connect()
	if err == nil {
		executor.Close()
		t.Fatal("expected authentication to fail")
	}

	errorText := err.Error()
	for _, expected := range []string{
		`SSH authentication failed for user "test-user"`,
		"the server offered public-key authentication but did not offer password authentication at this stage",
		"configured with a password and no PEM key",
		"Configure a PEM key authorized for this SSH account",
		"Original SSH error",
	} {
		if !strings.Contains(errorText, expected) {
			t.Errorf("expected error to contain %q, got %q", expected, errorText)
		}
	}
	if strings.Contains(errorText, password) {
		t.Fatalf("authentication error exposed the configured password: %q", errorText)
	}
}

func TestSSHExecutorDoesNotMisclassifyRejectedPassword(t *testing.T) {
	passwords := make(chan string, 1)
	host, port := startSSHTestServer(t, &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			passwords <- string(password)
			return nil, errors.New("test password rejected")
		},
	})

	executor := &SSHExecutor{host: Host{
		Host:     host,
		Port:     port,
		Username: "test-user",
		Password: "wrong-password",
	}}

	err := executor.Connect()
	if err == nil {
		executor.Close()
		t.Fatal("expected authentication to fail")
	}
	if strings.Contains(err.Error(), "did not offer password authentication") {
		t.Fatalf("rejected password was incorrectly classified as unavailable: %q", err)
	}
	if !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("expected original SSH authentication error, got %q", err)
	}

	select {
	case attempted := <-passwords:
		if attempted != "wrong-password" {
			t.Fatalf("server received password %q, want configured password", attempted)
		}
	default:
		t.Fatal("server did not receive a password authentication attempt")
	}
}

func TestSSHExecutorConnectsWithPassword(t *testing.T) {
	const password = "accepted-password"
	host, port := startSSHTestServer(t, &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, supplied []byte) (*ssh.Permissions, error) {
			if string(supplied) != password {
				return nil, errors.New("test password rejected")
			}
			return nil, nil
		},
	})

	executor := &SSHExecutor{host: Host{
		Host:     host,
		Port:     port,
		Username: "test-user",
		Password: password,
	}}
	if err := executor.Connect(); err != nil {
		t.Fatalf("password authentication failed: %v", err)
	}
	if err := executor.Close(); err != nil {
		t.Fatalf("failed to close SSH client: %v", err)
	}
}

func TestSSHExecutorConnectsWithAuthorizedPEM(t *testing.T) {
	clientSigner, privateKeyPEM := newSSHTestSigner(t)
	host, port := startSSHTestServer(t, &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), clientSigner.PublicKey().Marshal()) {
				return nil, errors.New("test key rejected")
			}
			return nil, nil
		},
	})

	pemPath := filepath.Join(t.TempDir(), "client.pem")
	if err := os.WriteFile(pemPath, privateKeyPEM, 0600); err != nil {
		t.Fatalf("failed to write test PEM file: %v", err)
	}

	executor := &SSHExecutor{host: Host{
		Host:     host,
		Port:     port,
		Username: "test-user",
		PemFile:  pemPath,
	}}
	if err := executor.Connect(); err != nil {
		t.Fatalf("PEM authentication failed: %v", err)
	}
	if err := executor.Close(); err != nil {
		t.Fatalf("failed to close SSH client: %v", err)
	}
}

func TestSSHExecutorPreservesPEMParseError(t *testing.T) {
	pemPath := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(pemPath, []byte("not a private key"), 0600); err != nil {
		t.Fatalf("failed to write invalid PEM file: %v", err)
	}

	executor := &SSHExecutor{host: Host{
		Host:     "127.0.0.1",
		Port:     1,
		Username: "test-user",
		PemFile:  pemPath,
	}}
	err := executor.Connect()
	if err == nil || !strings.Contains(err.Error(), "failed to parse PEM key") {
		t.Fatalf("expected PEM parse error, got %v", err)
	}
}

func startSSHTestServer(t *testing.T, config *ssh.ServerConfig) (string, int) {
	t.Helper()

	hostSigner, _ := newSSHTestSigner(t)
	config.AddHostKey(hostSigner)
	return startSSHTestServerWithHandler(t, config, nil)
}

// startSSHTestServerWithHandler starts a test SSH server; sessionHandler, when given,
// serves accepted "session" channels (nil rejects every channel). config must already
// carry a host key.
func startSSHTestServerWithHandler(t *testing.T, config *ssh.ServerConfig, sessionHandler func(ssh.Channel, <-chan *ssh.Request)) (string, int) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test SSH listener: %v", err)
	}
	t.Cleanup(func() {
		listener.Close()
	})

	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go serveSSHTestConnection(connection, config, sessionHandler, false)
		}
	}()

	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to parse test SSH address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("failed to parse test SSH port: %v", err)
	}
	return host, port
}

// startForwardingSSHTestServer is startSSHTestServerWithHandler plus "direct-tcpip"
// support, i.e. a bastion: forwarded connections are dialed from the test process.
func startForwardingSSHTestServer(t *testing.T, config *ssh.ServerConfig, sessionHandler func(ssh.Channel, <-chan *ssh.Request)) (string, int) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test SSH listener: %v", err)
	}
	t.Cleanup(func() {
		listener.Close()
	})

	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go serveSSHTestConnection(connection, config, sessionHandler, true)
		}
	}()

	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to parse test SSH address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("failed to parse test SSH port: %v", err)
	}
	return host, port
}

func serveSSHTestConnection(connection net.Conn, config *ssh.ServerConfig, sessionHandler func(ssh.Channel, <-chan *ssh.Request), allowForward bool) {
	defer connection.Close()

	serverConnection, channels, requests, err := ssh.NewServerConn(connection, config)
	if err != nil {
		return
	}
	defer serverConnection.Close()

	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if allowForward && newChannel.ChannelType() == "direct-tcpip" {
			go serveSSHTestForward(newChannel)
			continue
		}
		if sessionHandler == nil || newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.Prohibited, "channels are not supported by this test server")
			continue
		}
		channel, channelRequests, acceptErr := newChannel.Accept()
		if acceptErr != nil {
			continue
		}
		go sessionHandler(channel, channelRequests)
	}
}

// serveSSHTestForward answers one "direct-tcpip" channel open the way sshd does
// with AllowTcpForwarding: dial the requested destination and pipe both ways.
func serveSSHTestForward(newChannel ssh.NewChannel) {
	var payload struct {
		DestAddr string
		DestPort uint32
		OrigAddr string
		OrigPort uint32
	}
	if err := ssh.Unmarshal(newChannel.ExtraData(), &payload); err != nil {
		newChannel.Reject(ssh.ConnectionFailed, "bad direct-tcpip payload")
		return
	}
	target, err := net.Dial("tcp", net.JoinHostPort(payload.DestAddr, strconv.Itoa(int(payload.DestPort))))
	if err != nil {
		newChannel.Reject(ssh.ConnectionFailed, "connect failed: "+err.Error())
		return
	}
	channel, requests, err := newChannel.Accept()
	if err != nil {
		target.Close()
		return
	}
	go ssh.DiscardRequests(requests)
	done := make(chan struct{}, 2)
	go func() { io.Copy(channel, target); done <- struct{}{} }()
	go func() { io.Copy(target, channel); done <- struct{}{} }()
	<-done
	channel.Close()
	target.Close()
}

func newSSHTestSigner(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("failed to create test SSH signer: %v", err)
	}

	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("failed to marshal test SSH key: %v", err)
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})
	return signer, privateKeyPEM
}
