// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package secure

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"unsafe"
)

func TestNewSecret(t *testing.T) {
	tests := []struct {
		name    string
		in      []byte
		wantErr bool
	}{
		{name: "valid", in: []byte("super secret key material"), wantErr: false},
		{name: "single byte", in: []byte{0x42}, wantErr: false},
		{name: "nil", in: nil, wantErr: true},
		{name: "empty", in: []byte{}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Copy the input; NewSecret wipes its argument.
			src := append([]byte(nil), tc.in...)
			want := append([]byte(nil), tc.in...)

			sec, err := NewSecret(context.Background(), src)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				var ise *InvalidSizeError
				if !errors.As(err, &ise) {
					t.Fatalf("expected *InvalidSizeError, got %T", err)
				}
				if sec != nil {
					t.Fatalf("expected nil Secret on error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			defer sec.Destroy()

			// Source slice must be wiped.
			if bytes.Equal(src, want) {
				t.Fatalf("NewSecret did not wipe source slice")
			}

			buf, err := sec.Open(context.Background())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer buf.Destroy()
			if !bytes.Equal(contents(buf), want) {
				t.Fatalf("round-trip mismatch:\n got=%x\nwant=%x", contents(buf), want)
			}
		})
	}
}

func TestNewSecretFromBuffer(t *testing.T) {
	payload := []byte("envelope-sealed key")
	want := append([]byte(nil), payload...)

	buf, err := NewBufferFromBytes(payload)
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}

	sec, err := NewSecretFromBuffer(context.Background(), buf)
	if err != nil {
		t.Fatalf("NewSecretFromBuffer: %v", err)
	}
	defer sec.Destroy()

	// The Buffer must report destroyed after consumption.
	if buf.Size() != 0 {
		t.Fatalf("after NewSecretFromBuffer, buf.Size() = %d, want 0", buf.Size())
	}
	if contents(buf) != nil {
		t.Fatalf("after NewSecretFromBuffer, contents(buf) != nil")
	}

	open, err := sec.Open(context.Background())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer open.Destroy()
	if !bytes.Equal(contents(open), want) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestNewSecretFromBuffer_Nil(t *testing.T) {
	sec, err := NewSecretFromBuffer(context.Background(), nil)
	if err == nil || sec != nil {
		t.Fatalf("expected error and nil Secret, got sec=%v err=%v", sec, err)
	}
	var nb *NilBufferError
	if !errors.As(err, &nb) {
		t.Fatalf("expected *NilBufferError, got %T", err)
	}
}

func TestNewSecretFromBuffer_Destroyed(t *testing.T) {
	buf, err := NewBuffer(32)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	buf.Destroy()
	sec, err := NewSecretFromBuffer(context.Background(), buf)
	if err == nil || sec != nil {
		t.Fatalf("expected error for destroyed buffer")
	}
	var nb *NilBufferError
	if !errors.As(err, &nb) {
		t.Fatalf("expected *NilBufferError, got %T", err)
	}
}

// TestNewSecretFromBuffer_ZeroValue drives the size==0 guard with a Buffer
// that never went through a constructor at all — the zero value the package
// documentation calls unusable. Unlike a nil *Buffer (NilBufferError from the
// top check) or a Destroyed one (NilBufferError from the destroyed check),
// this exercises the third guard: a live-looking Buffer whose enclave was
// never allocated, where enc.Size() must report 0 rather than panic.
func TestNewSecretFromBuffer_ZeroValue(t *testing.T) {
	sec, err := NewSecretFromBuffer(context.Background(), &Buffer{})
	if err == nil || sec != nil {
		t.Fatalf("expected error for a zero-value buffer, got sec=%v err=%v", sec, err)
	}
	var nb *NilBufferError
	if !errors.As(err, &nb) {
		t.Fatalf("expected *NilBufferError, got %T", err)
	}
}

func TestSecret_OpenAfterDestroy(t *testing.T) {
	sec, err := NewSecret(context.Background(), []byte("short-lived"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	sec.Destroy()

	buf, err := sec.Open(context.Background())
	if err == nil || buf != nil {
		t.Fatalf("expected error and nil Buffer after Destroy")
	}
	var ns *NilSecretError
	if !errors.As(err, &ns) {
		t.Fatalf("expected *NilSecretError, got %T", err)
	}
}

func TestSecret_DoubleDestroyIsSafe(t *testing.T) {
	sec, err := NewSecret(context.Background(), []byte("short-lived"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	sec.Destroy()
	sec.Destroy()
	sec.Destroy()
}

func TestSecret_NilReceiverIsSafe(t *testing.T) {
	var sec *Secret
	// Must not panic.
	sec.Destroy()
	buf, err := sec.Open(context.Background())
	if err == nil || buf != nil {
		t.Fatalf("expected error and nil Buffer")
	}
	var ns *NilSecretError
	if !errors.As(err, &ns) {
		t.Fatalf("expected *NilSecretError, got %T", err)
	}
}

func TestSecret_ConcurrentDestroyAndOpen(t *testing.T) {
	sec, err := NewSecret(context.Background(), []byte("contended"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	var wg sync.WaitGroup
	// Race Open against Destroy. Either Open succeeds with the
	// material or returns NilSecretError; no panic, no corruption.
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b, err := sec.Open(context.Background()); err == nil {
				b.Destroy()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		sec.Destroy()
	}()
	wg.Wait()
}

func TestSecret_ZeroValueUnusable(t *testing.T) {
	var sec Secret // zero value
	if _, err := sec.Open(context.Background()); err == nil {
		t.Fatalf("expected Open() on zero-value Secret to fail")
	}
	// Destroy on zero value must not panic.
	sec.Destroy()
}

// TestSecret_Open_DecryptionFailure exercises the *OpenError branch of
// Secret.Open by corrupting the sealed ciphertext inside the underlying
// enclave.SealedEnclave via reflect+unsafe. This is deliberately limited to
// test code — no production code is modified. The technique accesses the
// unexported "sealed" []byte field so that decryption fails
// authentication, causing Open to return an *OpenError wrapping the
// underlying go-enclave error.
func TestSecret_Open_DecryptionFailure(t *testing.T) {
	src := []byte("material-for-corruption-test")
	sec, err := NewSecret(context.Background(), src)
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	defer sec.Destroy()

	// Load the enclave pointer and reach into its unexported "sealed" field
	// to corrupt it so decryption fails deterministically.
	sealed := sec.enclave.Load()
	if sealed == nil {
		t.Fatal("enclave pointer is nil before corruption")
	}
	sealedVal := reflect.ValueOf(sealed).Elem()
	ctField := sealedVal.FieldByName("sealed")
	if !ctField.IsValid() {
		// go-enclave's internal field layout changed under this test. That is
		// a real failure of this test's technique, not a reason to skip: a
		// skipped test reports green while verifying nothing, which is worse
		// than reporting the break (AGENTS.md: no skip guards).
		t.Fatalf("SealedEnclave has no \"sealed\" field; go-enclave's internal " +
			"layout changed and this test's reflection technique needs updating")
	}
	// Use unsafe to obtain a writable reference to the unexported slice.
	// #nosec G103 -- the test tampers with go-enclave's unexported ciphertext on purpose, to prove Open detects it; the field is checked valid above.
	ctPtr := (*[]byte)(unsafe.Pointer(ctField.UnsafeAddr()))
	ct := *ctPtr
	if len(ct) == 0 {
		// A non-empty NewSecret input always produces non-empty ciphertext
		// (plaintext length plus nonce and authentication tag), so this is
		// the same class of failure as above: the test's assumption about
		// go-enclave's internals broke, and it must fail loudly rather than
		// report a skip that looks like a pass in CI.
		t.Fatal("sealed ciphertext is empty; cannot corrupt a value that isn't there")
	}
	// Flip all bytes — the AEAD's authentication tag will fail on decryption.
	for i := range ct {
		ct[i] ^= 0xFF
	}

	buf, err := sec.Open(context.Background())
	if err == nil || buf != nil {
		t.Fatalf("expected Open to fail after ciphertext corruption, got buf=%v err=nil", buf)
	}
	var oe *OpenError
	if !errors.As(err, &oe) {
		t.Fatalf("expected *OpenError, got %T: %v", err, err)
	}
	if oe.Unwrap() == nil {
		t.Fatalf("OpenError.Unwrap() must return the underlying go-enclave error")
	}
}
