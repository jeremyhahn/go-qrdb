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
	"sync"
	"testing"
)

func TestNewBuffer(t *testing.T) {
	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "valid small", size: 1, wantErr: false},
		{name: "valid 32", size: 32, wantErr: false},
		{name: "valid 4096", size: 4096, wantErr: false},
		{name: "zero size", size: 0, wantErr: true},
		{name: "negative size", size: -1, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf, err := NewBuffer(tc.size)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				var ise *InvalidSizeError
				if !errors.As(err, &ise) {
					t.Fatalf("expected *InvalidSizeError, got %T", err)
				}
				if buf != nil {
					t.Fatalf("expected nil buffer on error, got %v", buf)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if buf.Size() != tc.size {
				t.Fatalf("Size() = %d, want %d", buf.Size(), tc.size)
			}
			if got := buf.Bytes(); len(got) != tc.size {
				t.Fatalf("len(Bytes()) = %d, want %d", len(got), tc.size)
			}
			buf.Destroy()
			if buf.Size() != 0 {
				t.Fatalf("after Destroy, Size() = %d, want 0", buf.Size())
			}
			if buf.Bytes() != nil {
				t.Fatalf("after Destroy, Bytes() != nil")
			}
		})
	}
}

func TestNewBufferFromBytes_RoundTrip(t *testing.T) {
	src := []byte("correct horse battery staple")
	want := append([]byte(nil), src...)

	buf, err := NewBufferFromBytes(src)
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer buf.Destroy()

	// The source slice must have been wiped.
	if bytes.Equal(src, want) {
		t.Fatalf("source slice not wiped; contents still match original")
	}
	if got := buf.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("Buffer contents mismatch:\n got=%x\nwant=%x", got, want)
	}
}

func TestNewBufferFromBytes_Empty(t *testing.T) {
	buf, err := NewBufferFromBytes(nil)
	if err == nil || buf != nil {
		t.Fatalf("expected error and nil buffer, got buf=%v err=%v", buf, err)
	}
	var ise *InvalidSizeError
	if !errors.As(err, &ise) {
		t.Fatalf("expected *InvalidSizeError, got %T", err)
	}
}

func TestBuffer_DoubleDestroyIsSafe(t *testing.T) {
	buf, err := NewBuffer(16)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	buf.Destroy()
	buf.Destroy() // must not panic
	buf.Destroy()
}

func TestBuffer_NilReceiverIsSafe(t *testing.T) {
	var buf *Buffer
	// None of these may panic.
	buf.Destroy()
	if buf.Size() != 0 {
		t.Fatalf("nil Size() = %d, want 0", buf.Size())
	}
	if buf.Bytes() != nil {
		t.Fatalf("nil Bytes() != nil")
	}
}

func TestBuffer_ConcurrentDestroy(t *testing.T) {
	buf, err := NewBuffer(64)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf.Destroy()
		}()
	}
	wg.Wait()
	if buf.Size() != 0 {
		t.Fatalf("after concurrent destroy, Size() = %d, want 0", buf.Size())
	}
}

// TestBuffer_MemoryInaccessibleAfterDestroy verifies the post-Destroy
// guarantee: the caller cannot observe the prior contents. memguard
// unmaps the underlying page on Destroy, so any attempt to read via a
// retained slice reference faults with SIGSEGV. Our wrapper's public
// API shields the caller from that fault by returning nil for Bytes()
// and 0 for Size() after Destroy; this test asserts that contract.
func TestBuffer_MemoryInaccessibleAfterDestroy(t *testing.T) {
	buf, err := NewBufferFromBytes([]byte("canary-bytes"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	// Before Destroy, Bytes returns non-nil and Size reports the length.
	if buf.Size() != len("canary-bytes") {
		t.Fatalf("pre-destroy Size mismatch")
	}
	if len(buf.Bytes()) != len("canary-bytes") {
		t.Fatalf("pre-destroy Bytes len mismatch")
	}

	buf.Destroy()

	// Post-Destroy: our wrapper must refuse to hand out the underlying
	// slice. This prevents accidental use-after-destroy faults at
	// callers.
	if buf.Bytes() != nil {
		t.Fatalf("Bytes() must be nil after Destroy")
	}
	if buf.Size() != 0 {
		t.Fatalf("Size() must be 0 after Destroy")
	}
}

func TestWipe(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{name: "nil", in: nil},
		{name: "empty", in: []byte{}},
		{name: "small", in: []byte{1, 2, 3, 4}},
		{name: "larger", in: []byte("super-secret-passphrase-data")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Clone so we can assert on the original.
			data := append([]byte(nil), tc.in...)
			Wipe(data)
			for i, b := range data {
				if b != 0 {
					t.Fatalf("byte %d non-zero after Wipe: %x", i, b)
				}
			}
		})
	}
}
