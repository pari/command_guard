package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func newTestHostSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestHostKeyPinning(t *testing.T) {
	signer := newTestHostSigner(t)
	serverConfig := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				if sc, chans, reqs, err := ssh.NewServerConn(conn, serverConfig); err == nil {
					go ssh.DiscardRequests(reqs)
					go func() {
						for ch := range chans {
							ch.Reject(ssh.Prohibited, "test")
						}
					}()
					sc.Wait()
				}
			}()
		}
	}()

	dial := func(hostKey string) error {
		cfg, err := clientConfig("app", "", "", "pw", hostKey)
		if err != nil {
			return err
		}
		client, err := dialSSH(listener.Addr().String(), cfg)
		if err != nil {
			return err
		}
		return client.Close()
	}

	pinned := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if err := dial(pinned); err != nil {
		t.Fatalf("matching pinned key refused: %v", err)
	}
	other := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(newTestHostSigner(t).PublicKey())))
	if err := dial(other); err == nil || !strings.Contains(err.Error(), "HOST KEY CHANGED") {
		t.Fatalf("wrong pinned key accepted: %v", err)
	}
	if err := dial(""); err != nil {
		t.Fatalf("empty host key (checking off) refused: %v", err)
	}
	if _, err := clientConfig("app", "", "", "pw", "not a key"); err == nil {
		t.Fatal("invalid pinned key accepted")
	}
}
