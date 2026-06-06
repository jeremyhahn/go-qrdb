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

// TestNotInitializedError_Error verifies the error string returned by
// NotInitializedError contains a meaningful message.
func TestNotInitializedError_Error(t *testing.T) {
	e := &NotInitializedError{}
	got := e.Error()
	if !strings.Contains(got, "initialized") && !strings.Contains(got, "CatchInterrupt") {
		t.Fatalf("NotInitializedError.Error() = %q: expected mention of initialized or CatchInterrupt", got)
	}
}

// TestErrorTypes_Roundtrip exercises errors.As on each error type to ensure
// the type system agrees with the implementations.
func TestErrorTypes_Roundtrip(t *testing.T) {
	t.Run("NilSecretError", func(t *testing.T) {
		var target *NilSecretError
		if !errors.As(&NilSecretError{}, &target) {
			t.Fatal("errors.As failed")
		}
		_ = target.Error()
	})
	t.Run("NilBufferError", func(t *testing.T) {
		var target *NilBufferError
		if !errors.As(&NilBufferError{}, &target) {
			t.Fatal("errors.As failed")
		}
		_ = target.Error()
	})
	t.Run("InvalidSizeError", func(t *testing.T) {
		var target *InvalidSizeError
		if !errors.As(&InvalidSizeError{Size: -1}, &target) {
			t.Fatal("errors.As failed")
		}
		_ = target.Error()
	})
	t.Run("NotInitializedError", func(t *testing.T) {
		var target *NotInitializedError
		if !errors.As(&NotInitializedError{}, &target) {
			t.Fatal("errors.As failed")
		}
		_ = target.Error()
	})
}
