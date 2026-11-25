package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// A crafted backup must be refused before any key derivation is attempted.
func TestDecryptBackupRejectsHostileEnvelope(t *testing.T) {
	valid := encryptedBackupEnvelope{
		Format: backupEnvelopeFormat, Version: backupFormatVersion, KDF: "scrypt",
		ScryptN: backupScryptN, ScryptR: backupScryptR, ScryptP: backupScryptP,
		Salt:       base64.StdEncoding.EncodeToString(make([]byte, 16)),
		Nonce:      base64.StdEncoding.EncodeToString(make([]byte, backupGCMNonceBytes)),
		Ciphertext: base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
	cases := map[string]func(e *encryptedBackupEnvelope){
		"huge N":         func(e *encryptedBackupEnvelope) { e.ScryptN = 1 << 30 },
		"N not power 2":  func(e *encryptedBackupEnvelope) { e.ScryptN = 32769 },
		"huge r":         func(e *encryptedBackupEnvelope) { e.ScryptR = 1 << 20 },
		"huge p":         func(e *encryptedBackupEnvelope) { e.ScryptP = 1 << 20 },
		"one-byte nonce": func(e *encryptedBackupEnvelope) { e.Nonce = base64.StdEncoding.EncodeToString([]byte{1}) },
		"empty salt":     func(e *encryptedBackupEnvelope) { e.Salt = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			envelope := valid
			mutate(&envelope)
			data, _ := json.Marshal(envelope)
			if _, err := decryptBackup(data, "a-long-enough-password"); err == nil {
				t.Fatal("expected the envelope to be rejected")
			} else if strings.Contains(err.Error(), "password") {
				t.Fatalf("rejected too late (after key derivation): %v", err)
			}
		})
	}
}

func TestHostResponseOmitsSecrets(t *testing.T) {
	out := hostResponse(map[string]interface{}{"host": "192.0.2.10", "password": "s3cret", "pem_password": "p3m"})
	data, _ := json.Marshal(out)
	if strings.Contains(string(data), "s3cret") || strings.Contains(string(data), "p3m") {
		t.Fatalf("secret leaked in host response: %s", data)
	}
	if out["has_password"] != true || out["has_pem_password"] != true || out["host"] != "192.0.2.10" {
		t.Fatalf("unexpected host response: %s", data)
	}
}

// stdout and stderr are copied into one LimitedBuffer from two goroutines.
// Run with -race to check the writes are serialised.
func TestLimitedBufferConcurrentWrites(t *testing.T) {
	buffer := NewLimitedBuffer(1000)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = buffer.Write([]byte("0123456789"))
			}
		}()
	}
	wg.Wait()
	if !buffer.overflow || buffer.curSize != 1000 {
		t.Fatalf("expected a full, overflowed buffer; size=%d overflow=%v", buffer.curSize, buffer.overflow)
	}
}

func TestCloneRunBookRunIsIndependent(t *testing.T) {
	run := &RunBookRun{RunID: "r1", Steps: []RunBookStepResult{{StepID: "a", Status: "pending"}}}
	copy := cloneRunBookRun(run)
	copy.Steps[0].Status = "changed"
	if run.Steps[0].Status != "pending" {
		t.Fatal("clone shares step storage with the live run")
	}
}
