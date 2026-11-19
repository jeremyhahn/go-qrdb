// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package secure

import (
	"errors"
	"strings"
	"testing"
)

// TestNilSecretError_Error verifies the error string returned by NilSecretError.
func TestNilSecretError_Error(t *testing.T) {
	e := &NilSecretError{}
	got := e.Error()
	if !strings.Contains(got, "nil") && !strings.Contains(got, "destroyed") {
		t.Fatalf("NilSecretError.Error() = %q: expected mention of nil or destroyed", got)
	}
}

// TestNilBufferError_Error verifies the error string returned by NilBufferError.
func TestNilBufferError_Error(t *testing.T) {
	e := &NilBufferError{}
	got := e.Error()
	if !strings.Contains(got, "nil") && !strings.Contains(got, "destroyed") {
		t.Fatalf("NilBufferError.Error() = %q: expected mention of nil or destroyed", got)
	}
}

// TestOpenError_ErrorAndUnwrap verifies OpenError wraps the underlying cause
// and exposes it via Unwrap so errors.Is traversal works.
func TestOpenError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("decryption failed")
	e := &OpenError{Err: cause}

	got := e.Error()
	if !strings.Contains(got, cause.Error()) {
		t.Fatalf("OpenError.Error() = %q: expected to contain %q", got, cause.Error())
	}

	if unwrapped := e.Unwrap(); unwrapped != cause {
		t.Fatalf("OpenError.Unwrap() = %v, want %v", unwrapped, cause)
	}
	// errors.Is should traverse via Unwrap.
	if !errors.Is(e, cause) {
		t.Fatalf("errors.Is(OpenError, cause) = false; expected true")
	}
}

// TestInvalidSizeError_Error verifies the error string returned by
// InvalidSizeError for both zero and negative sizes.
func TestInvalidSizeError_Error(t *testing.T) {
	tests := []struct {
		name string
		size int
	}{
		{name: "zero", size: 0},
		{name: "negative", size: -3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &InvalidSizeError{Size: tc.size}
			got := e.Error()
			if got == "" {
				t.Fatalf("InvalidSizeError.Error() returned empty string for size=%d", tc.size)
			}
		})
	}
}

// TestProviderError_ErrorAndUnwrap verifies ProviderError includes the source
// label and underlying error message, and that Unwrap returns the cause.
func TestProviderError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("read failed")
	e := &ProviderError{Source: "stdin", Err: cause}

	got := e.Error()
	if !strings.Contains(got, "stdin") {
		t.Fatalf("ProviderError.Error() = %q: expected source label %q", got, "stdin")
	}
	if !strings.Contains(got, cause.Error()) {
		t.Fatalf("ProviderError.Error() = %q: expected cause message %q", got, cause.Error())
	}

	if unwrapped := e.Unwrap(); unwrapped != cause {
		t.Fatalf("ProviderError.Unwrap() = %v, want %v", unwrapped, cause)
	}
	if !errors.Is(e, cause) {
		t.Fatalf("errors.Is(ProviderError, cause) = false; expected true")
	}
}

// NestedBorrowError's message must name the operation AND carry the outer
// borrow's stack. The stack is the half a reader cannot recover from the
// panic's own trace, and a report without it says "something nested" without
// saying what.
func TestNestedBorrowError(t *testing.T) {
	err := &NestedBorrowError{Op: "Buffer.Destroy", BorrowStack: "goroutine 7 [running]:\nsome.Frame()"}
	msg := err.Error()
	for _, want := range []string{"Buffer.Destroy", "this goroutine is borrowing", "some.Frame()"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() does not contain %q; got:\n%s", want, msg)
		}
	}
	// The zero value must still produce a usable message rather than panic.
	if (&NestedBorrowError{}).Error() == "" {
		t.Error("the zero value produced an empty message")
	}
}

// EmptyBorrowError must say what happened rather than what was expected: the
// pages are gone, which is why the caller is being refused instead of handed
// zeros.
func TestEmptyBorrowError(t *testing.T) {
	err := &EmptyBorrowError{}
	if !strings.Contains(err.Error(), "yielded no bytes") {
		t.Errorf("Error() = %q, want it to say the borrow yielded nothing", err.Error())
	}
	var target *EmptyBorrowError
	if !errors.As(error(err), &target) {
		t.Error("EmptyBorrowError does not satisfy errors.As against its own type")
	}
}
