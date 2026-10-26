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
	"runtime"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-enclave"
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
			if got := contents(buf); len(got) != tc.size {
				t.Fatalf("len(Bytes()) = %d, want %d", len(got), tc.size)
			}
			buf.Destroy()
			if buf.Size() != 0 {
				t.Fatalf("after Destroy, Size() = %d, want 0", buf.Size())
			}
			if contents(buf) != nil {
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
	if got := contents(buf); !bytes.Equal(got, want) {
		t.Fatalf("Buffer contents mismatch:\n got=%x\nwant=%x", got, want)
	}
}

// TestNewBufferFromBytes_ResultIsWritable pins that a Buffer built by
// NewBufferFromBytes can be written to through WithBytes exactly like one
// built by NewBuffer. go-enclave's own move constructor (enclave.NewSealed,
// enclave.Allocator.New) FREEZES pages built from existing bytes read-only,
// and this package deliberately does not use that path directly for this
// reason: Buffer has always been uniformly writable regardless of which
// constructor built it, and a write faulting the process only for buffers
// built one particular way would be exactly the kind of landmine a caller has
// no way to see coming. If this regresses, the process crashes rather than
// this test merely failing.
func TestNewBufferFromBytes_ResultIsWritable(t *testing.T) {
	buf, err := NewBufferFromBytes([]byte("mutate me"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer buf.Destroy()

	if err := buf.WithBytes(func(b []byte) error {
		for i := range b {
			b[i] = 'x'
		}
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
	if got := string(contents(buf)); got != "xxxxxxxxx" {
		t.Fatalf("Bytes() after mutation = %q, want all 'x'", got)
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

// TestBuffer_DestroyErr pins the observable half of Destroy's contract: a
// normal release reports nil, it is idempotent (a second call is a no-op that
// also reports nil, not "already destroyed"), and it is safe on a nil
// receiver. Forcing the ErrReleasedWithoutWipe branch itself needs an
// environment that denies mprotect, which is go-enclave's own concern to
// test — this pins that pkg/secure does not swallow whatever DestroyErr
// reports, which TestBuffer_BytesAndWithBytesSurviveTheUnderlyingEnclaveBeingDestroyed's
// sibling tests exercise from the other direction (reaching into buf.enc
// directly).
func TestBuffer_DestroyErr(t *testing.T) {
	buf, err := NewBuffer(16)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	if err := buf.DestroyErr(); err != nil {
		t.Fatalf("DestroyErr() on a live buffer = %v, want nil", err)
	}
	if err := buf.DestroyErr(); err != nil {
		t.Fatalf("second DestroyErr() = %v, want nil (idempotent)", err)
	}

	var nilBuf *Buffer
	if err := nilBuf.DestroyErr(); err != nil {
		t.Fatalf("DestroyErr() on a nil Buffer = %v, want nil", err)
	}
}

// Destroy itself must still discard whatever DestroyErr reports, for the
// existing `defer buf.Destroy()` call sites this package has everywhere.
func TestBuffer_DestroyDiscardsTheReportForTheDeferredCallers(t *testing.T) {
	buf, err := NewBuffer(16)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	buf.Destroy() // must not panic and returns nothing to check
}

func TestBuffer_NilReceiverIsSafe(t *testing.T) {
	var buf *Buffer
	// None of these may panic.
	buf.Destroy()
	if buf.Size() != 0 {
		t.Fatalf("nil Size() = %d, want 0", buf.Size())
	}
	if contents(buf) != nil {
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
// API shields the caller from that fault by refusing WithBytes (so CopyOut
// yields nothing) and returning 0 for Size() after Destroy; this test asserts
// that contract.
func TestBuffer_MemoryInaccessibleAfterDestroy(t *testing.T) {
	buf, err := NewBufferFromBytes([]byte("canary-bytes"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	// Before Destroy, the contents read back and Size reports the length.
	if buf.Size() != len("canary-bytes") {
		t.Fatalf("pre-destroy Size mismatch")
	}
	if len(contents(buf)) != len("canary-bytes") {
		t.Fatalf("pre-destroy contents len mismatch")
	}

	buf.Destroy()

	// Post-Destroy: our wrapper must refuse to hand out the underlying
	// slice. This prevents accidental use-after-destroy faults at
	// callers.
	if contents(buf) != nil {
		t.Fatalf("contents must be unreadable after Destroy")
	}
	if buf.Size() != 0 {
		t.Fatalf("Size() must be 0 after Destroy")
	}
}

// TestWrapAllocError exercises wrapAllocError's two branches directly: it
// translates go-enclave's ErrInvalidSize into this package's typed
// *InvalidSizeError, and passes every other error (chiefly
// enclave.ErrBudgetExhausted and enclave.ErrLockedMemoryUnavailable, which
// callers match with errors.Is) through unchanged.
func TestWrapAllocError(t *testing.T) {
	if got := wrapAllocError(16, nil); got != nil {
		t.Fatalf("wrapAllocError(nil) = %v, want nil", got)
	}

	err := wrapAllocError(16, enclave.ErrInvalidSize)
	var ise *InvalidSizeError
	if !errors.As(err, &ise) {
		t.Fatalf("wrapAllocError(ErrInvalidSize) = %T, want *InvalidSizeError", err)
	}
	if ise.Size != 16 {
		t.Fatalf("InvalidSizeError.Size = %d, want 16", ise.Size)
	}

	if got := wrapAllocError(16, enclave.ErrBudgetExhausted); !errors.Is(got, enclave.ErrBudgetExhausted) {
		t.Fatalf("wrapAllocError(ErrBudgetExhausted) = %v, want it unwrapped to the same sentinel", got)
	}
}

// TestBuffer_ReadsSurviveTheUnderlyingEnclaveBeingDestroyed drives CopyOut and
// WithBytes against a Buffer whose go-enclave memory enclave was
// destroyed WITHOUT going through this package's own Destroy — the one way
// b.destroyed can read false while b.enc no longer holds anything. Both
// accessors must report empty/refused rather than propagate go-enclave's
// ErrEnclaveDestroyed as a foreign error type.
func TestBuffer_ReadsSurviveTheUnderlyingEnclaveBeingDestroyed(t *testing.T) {
	buf, err := NewBuffer(16)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	// Reach past this package's own Destroy so buf.destroyed stays false while
	// the underlying enclave is actually gone.
	if err := buf.enc.Destroy(); err != nil {
		t.Fatalf("enclave Destroy: %v", err)
	}

	if got := contents(buf); got != nil {
		t.Fatalf("CopyOut on a Buffer whose enclave was destroyed = %v, want nil", got)
	}

	called := false
	werr := buf.WithBytes(func([]byte) error { called = true; return nil })
	var nb *NilBufferError
	if !errors.As(werr, &nb) {
		t.Fatalf("WithBytes on a Buffer whose enclave was destroyed = %T (%v), want *NilBufferError", werr, werr)
	}
	if called {
		t.Fatal("WithBytes invoked the callback against a destroyed enclave")
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

// TestBuffer_DestroyErr_AbandonedAttemptDoesNotLatchDestroyed pins that a
// DestroyErr attempt which never reaches the end of the release leaves the
// Buffer retryable rather than falsely marked destroyed (go-qrdb#114 review,
// round 4). The abandonment is driven through the one exit this package
// controls end to end: an armed borrow guard whose violation handler ends the
// call from inside checkNotBorrowed — victim's Destroy reentering victim's own
// borrow — AFTER the destroyed CAS has already fired.
// The two ways a handler can end it — panic (EnableBorrowGuard's default) and
// runtime.Goexit (GuardBorrows') — are both covered, because a reset keyed on
// recover() alone would miss the second.
//
// A latched flag is observable without reaching into go-enclave: Size reports
// 0 for a Buffer marked destroyed, and the live enclave underneath would then
// stay mapped and registered until a process-wide WipeAll.
func TestBuffer_DestroyErr_AbandonedAttemptDoesNotLatchDestroyed(t *testing.T) {
	const size = 32
	tests := []struct {
		name    string
		handler BorrowViolationFunc
		// attempt runs victim.DestroyErr inside victim's own borrow and returns
		// once the attempt has been abandoned.
		attempt func(victim *Buffer)
	}{
		{
			name:    "handler panics",
			handler: nil, // EnableBorrowGuard's default: panic
			attempt: func(victim *Buffer) {
				_ = victim.WithBytes(func([]byte) error {
					func() {
						defer func() { _ = recover() }()
						_ = victim.DestroyErr()
					}()
					return nil
				})
			},
		},
		{
			name:    "handler calls runtime.Goexit",
			handler: func(*NestedBorrowError) { runtime.Goexit() },
			attempt: func(victim *Buffer) {
				done := make(chan struct{})
				go func() {
					defer close(done)
					_ = victim.WithBytes(func([]byte) error {
						_ = victim.DestroyErr()
						return nil
					})
				}()
				<-done
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			victim, err := NewBuffer(size)
			if err != nil {
				t.Fatalf("NewBuffer(victim): %v", err)
			}
			defer victim.Destroy()

			restore := EnableBorrowGuard(tc.handler)
			tc.attempt(victim)
			restore()

			if got := victim.Size(); got != size {
				t.Fatalf("Size after an abandoned DestroyErr = %d, want %d: "+
					"the Buffer was latched destroyed while its enclave is still live", got, size)
			}
			if err := victim.DestroyErr(); err != nil {
				t.Fatalf("retried DestroyErr: %v", err)
			}
			if got := victim.Size(); got != 0 {
				t.Fatalf("Size after the retried DestroyErr = %d, want 0", got)
			}
		})
	}
}
