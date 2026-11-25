package main

import (
	"bytes"
	"testing"
)

func TestRingBufferKeepsMostRecentBytes(t *testing.T) {
	r := newRingBuffer(1024)
	r.Write([]byte("hello"))
	if got := r.Bytes(); string(got) != "hello" {
		t.Fatalf("expected hello, got %q", got)
	}
	// Overflow by writing more than the capacity in several chunks.
	chunk := bytes.Repeat([]byte("x"), 600)
	r.Write(chunk)
	r.Write([]byte("tail"))
	got := r.Bytes()
	if len(got) != 609 {
		t.Fatalf("expected 609 bytes, got %d", len(got))
	}
	if !bytes.HasSuffix(got, []byte("tail")) || !bytes.HasPrefix(got, []byte("hello")) {
		t.Fatalf("unexpected content ordering: %q...%q", got[:5], got[len(got)-4:])
	}
	r.Write(bytes.Repeat([]byte("y"), 500))
	got = r.Bytes()
	if len(got) != 1024 || !bytes.HasSuffix(got, bytes.Repeat([]byte("y"), 500)) {
		t.Fatalf("buffer should be full and end with the newest write; len=%d", len(got))
	}
	if bytes.HasPrefix(got, []byte("hello")) {
		t.Fatalf("oldest bytes should have been discarded")
	}
}

func TestRingBufferSingleOversizedWrite(t *testing.T) {
	r := newRingBuffer(1024)
	big := bytes.Repeat([]byte("ab"), 2000)
	r.Write(big)
	if got := r.Bytes(); !bytes.Equal(got, big[len(big)-1024:]) {
		t.Fatalf("expected the last 1024 bytes of the oversized write")
	}
}
