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

			sec, err := NewSecret(src)
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

			buf, err := sec.Open()
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer buf.Destroy()
			if !bytes.Equal(buf.Bytes(), want) {
				t.Fatalf("round-trip mismatch:\n got=%x\nwant=%x", buf.Bytes(), want)
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

	sec, err := NewSecretFromBuffer(buf)
	if err != nil {
		t.Fatalf("NewSecretFromBuffer: %v", err)
	}
	defer sec.Destroy()

	// The Buffer must report destroyed after consumption.
	if buf.Size() != 0 {
		t.Fatalf("after NewSecretFromBuffer, buf.Size() = %d, want 0", buf.Size())
	}
	if buf.Bytes() != nil {
		t.Fatalf("after NewSecretFromBuffer, buf.Bytes() != nil")
	}

	open, err := sec.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer open.Destroy()
	if !bytes.Equal(open.Bytes(), want) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestNewSecretFromBuffer_Nil(t *testing.T) {
	sec, err := NewSecretFromBuffer(nil)
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
	sec, err := NewSecretFromBuffer(buf)
	if err == nil || sec != nil {
		t.Fatalf("expected error for destroyed buffer")
	}
	var nb *NilBufferError
	if !errors.As(err, &nb) {
		t.Fatalf("expected *NilBufferError, got %T", err)
	}
}

func TestSecret_OpenAfterDestroy(t *testing.T) {
	sec, err := NewSecret([]byte("short-lived"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	sec.Destroy()

	buf, err := sec.Open()
	if err == nil || buf != nil {
		t.Fatalf("expected error and nil Buffer after Destroy")
	}
	var ns *NilSecretError
	if !errors.As(err, &ns) {
		t.Fatalf("expected *NilSecretError, got %T", err)
	}
}

func TestSecret_DoubleDestroyIsSafe(t *testing.T) {
	sec, err := NewSecret([]byte("short-lived"))
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
	buf, err := sec.Open()
	if err == nil || buf != nil {
		t.Fatalf("expected error and nil Buffer")
	}
	var ns *NilSecretError
	if !errors.As(err, &ns) {
		t.Fatalf("expected *NilSecretError, got %T", err)
	}
}

func TestSecret_ConcurrentDestroyAndOpen(t *testing.T) {
	sec, err := NewSecret([]byte("contended"))
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
			if b, err := sec.Open(); err == nil {
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
	if _, err := sec.Open(); err == nil {
		t.Fatalf("expected Open() on zero-value Secret to fail")
	}
	// Destroy on zero value must not panic.
	sec.Destroy()
}

// TestSecret_Open_DecryptionFailure exercises the *OpenError branch of
// Secret.Open by corrupting the sealed ciphertext inside the underlying
// memguard.Enclave via reflect+unsafe. This is deliberately limited to test
// code — no production code is modified. The technique accesses the
// unexported ciphertext []byte field of the internal core.Enclave struct so
// that decryption fails with ErrDecryptionFailed, causing Open to return a
// *OpenError wrapping the underlying memguard error.
func TestSecret_Open_DecryptionFailure(t *testing.T) {
	src := []byte("material-for-corruption-test")
	sec, err := NewSecret(src)
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	defer sec.Destroy()

	// Load the enclave pointer and reach into its unexported ciphertext field
	// to corrupt it so decryption fails deterministically.
	enc := sec.enclave.Load()
	if enc == nil {
		t.Fatal("enclave pointer is nil before corruption")
	}
	// enc is *memguard.Enclave which embeds *core.Enclave as field Enclave.
	// Reflect into the core.Enclave to find its ciphertext.
	coreEncVal := reflect.ValueOf(enc.Enclave).Elem()
	ctField := coreEncVal.FieldByName("ciphertext")
	if !ctField.IsValid() {
		t.Skip("ciphertext field not found via reflection; memguard internals may have changed")
	}
	// Use unsafe to obtain a writable reference to the unexported slice.
	ctPtr := (*[]byte)(unsafe.Pointer(ctField.UnsafeAddr()))
	ct := *ctPtr
	if len(ct) == 0 {
		t.Skip("ciphertext is empty; cannot corrupt")
	}
	// Flip all bytes — the secretbox MAC will fail on decryption.
	for i := range ct {
		ct[i] ^= 0xFF
	}

	buf, err := sec.Open()
	if err == nil || buf != nil {
		t.Fatalf("expected Open to fail after ciphertext corruption, got buf=%v err=nil", buf)
	}
	var oe *OpenError
	if !errors.As(err, &oe) {
		t.Fatalf("expected *OpenError, got %T: %v", err, err)
	}
	if oe.Unwrap() == nil {
		t.Fatalf("OpenError.Unwrap() must return the underlying memguard error")
	}
}
