// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package errors

import (
	stderrors "errors"
	"testing"
)

// ---------------------------------------------------------------------------
// IsResourceExhausted
// ---------------------------------------------------------------------------

// TestIsResourceExhausted_WithMatch verifies that IsResourceExhausted returns
// true for an ErrResourceExhausted error.
func TestIsResourceExhausted_WithMatch(t *testing.T) {
	err := New(ErrResourceExhausted, "test.Op", "quota exceeded")
	if !IsResourceExhausted(err) {
		t.Error("expected IsResourceExhausted to return true")
	}
}

// TestIsResourceExhausted_WithNil verifies that IsResourceExhausted returns
// false for nil.
func TestIsResourceExhausted_WithNil(t *testing.T) {
	if IsResourceExhausted(nil) {
		t.Error("expected IsResourceExhausted(nil) to return false")
	}
}

// TestIsResourceExhausted_WithDifferentCode verifies that IsResourceExhausted
// returns false for an error with a different code.
func TestIsResourceExhausted_WithDifferentCode(t *testing.T) {
	err := New(ErrNotFound, "test.Op", "not found")
	if IsResourceExhausted(err) {
		t.Error("expected IsResourceExhausted to return false for ErrNotFound")
	}
}

// TestIsResourceExhausted_WithStandardError verifies that IsResourceExhausted
// returns false for a non-DragonError.
func TestIsResourceExhausted_WithStandardError(t *testing.T) {
	err := stderrors.New("standard error")
	if IsResourceExhausted(err) {
		t.Error("expected IsResourceExhausted to return false for non-DragonError")
	}
}

// ---------------------------------------------------------------------------
// IsNotFound / IsCanceled / IsInvalidInput - standard error branch
// ---------------------------------------------------------------------------

// TestIsNotFound_WithStandardError verifies that IsNotFound returns false for
// a plain non-DragonError, exercising the errors.As fallthrough branch.
func TestIsNotFound_WithStandardError(t *testing.T) {
	err := stderrors.New("plain error")
	if IsNotFound(err) {
		t.Error("expected IsNotFound to return false for non-DragonError")
	}
}

// TestIsCanceled_WithStandardError verifies that IsCanceled returns false for
// a plain non-DragonError, exercising the errors.As fallthrough branch.
func TestIsCanceled_WithStandardError(t *testing.T) {
	err := stderrors.New("plain error")
	if IsCanceled(err) {
		t.Error("expected IsCanceled to return false for non-DragonError")
	}
}

// TestIsInvalidInput_WithStandardError verifies that IsInvalidInput returns
// false for a plain non-DragonError, exercising the errors.As fallthrough branch.
func TestIsInvalidInput_WithStandardError(t *testing.T) {
	err := stderrors.New("plain error")
	if IsInvalidInput(err) {
		t.Error("expected IsInvalidInput to return false for non-DragonError")
	}
}

// ---------------------------------------------------------------------------
// WrappedMessageError.Unwrap
// ---------------------------------------------------------------------------

// TestWrappedMessageError_Unwrap verifies that Unwrap returns the underlying
// cause, enabling errors.Is and errors.As traversal.
func TestWrappedMessageError_Unwrap(t *testing.T) {
	cause := stderrors.New("the root cause")
	wrapped := &WrappedMessageError{
		Message: "outer context",
		Cause:   cause,
	}

	// Verify Unwrap returns the cause.
	if wrapped.Unwrap() != cause {
		t.Errorf("Unwrap() = %v, want %v", wrapped.Unwrap(), cause)
	}

	// Verify errors.Is traversal works through the wrapper.
	if !stderrors.Is(wrapped, cause) {
		t.Error("errors.Is(wrapped, cause) should return true via Unwrap")
	}
}

// TestWrappedMessageError_Error verifies that Error() concatenates message and
// cause.
func TestWrappedMessageError_Error(t *testing.T) {
	cause := stderrors.New("database connection failed")
	wrapped := &WrappedMessageError{
		Message: "service unavailable",
		Cause:   cause,
	}
	msg := wrapped.Error()
	if msg == "" {
		t.Fatal("Error() returned empty string")
	}
	if !containsStr(msg, "service unavailable") {
		t.Errorf("Error() = %q, want to contain 'service unavailable'", msg)
	}
	if !containsStr(msg, "database connection failed") {
		t.Errorf("Error() = %q, want to contain cause", msg)
	}
}

// ---------------------------------------------------------------------------
// CircuitOpen / IsCircuitOpen
// ---------------------------------------------------------------------------

// TestCircuitOpen_CreatesCircuitOpenError verifies that CircuitOpen returns a
// DragonError with code ErrCircuitOpen.
func TestCircuitOpen_CreatesCircuitOpenError(t *testing.T) {
	err := CircuitOpen("downstream.Call", "payment-service")
	if err == nil {
		t.Fatal("CircuitOpen returned nil")
	}

	var de *DragonError
	if !stderrors.As(err, &de) {
		t.Fatalf("expected *DragonError, got %T", err)
	}
	if de.Code != ErrCircuitOpen {
		t.Errorf("Code: got %v, want ErrCircuitOpen", de.Code)
	}
	if de.Op != "downstream.Call" {
		t.Errorf("Op: got %q, want downstream.Call", de.Op)
	}
}

// TestIsCircuitOpen_WithMatch verifies that IsCircuitOpen returns true for a
// circuit open error.
func TestIsCircuitOpen_WithMatch(t *testing.T) {
	err := CircuitOpen("op", "my-service")
	if !IsCircuitOpen(err) {
		t.Error("expected IsCircuitOpen to return true for circuit open error")
	}
}

// TestIsCircuitOpen_WithNil verifies that IsCircuitOpen returns false for nil.
func TestIsCircuitOpen_WithNil(t *testing.T) {
	if IsCircuitOpen(nil) {
		t.Error("expected IsCircuitOpen(nil) to return false")
	}
}

// TestIsCircuitOpen_WithDifferentCode verifies that IsCircuitOpen returns
// false for an error with a different code.
func TestIsCircuitOpen_WithDifferentCode(t *testing.T) {
	err := New(ErrTimeout, "op", "timed out")
	if IsCircuitOpen(err) {
		t.Error("expected IsCircuitOpen to return false for ErrTimeout")
	}
}

// TestIsCircuitOpen_WithStandardError verifies that IsCircuitOpen returns
// false for a non-DragonError.
func TestIsCircuitOpen_WithStandardError(t *testing.T) {
	err := stderrors.New("plain error")
	if IsCircuitOpen(err) {
		t.Error("expected IsCircuitOpen to return false for non-DragonError")
	}
}

// ---------------------------------------------------------------------------
// ErrorCode.String - missing codes
// ---------------------------------------------------------------------------

// TestErrorCode_String_MissingCodes verifies that error codes not exercised
// by the existing test suite produce the correct string representations.
func TestErrorCode_String_MissingCodes(t *testing.T) {
	cases := []struct {
		code ErrorCode
		want string
	}{
		{ErrQuorumLost, "quorum_lost"},
		{ErrSplitBrain, "split_brain"},
		{ErrRateLimited, "rate_limited"},
		{ErrNotImplemented, "not_implemented"},
		{ErrNotAllowed, "not_allowed"},
		{ErrSystemKeyspaceViolation, "system_keyspace_violation"},
		{ErrCrossTenantAccess, "cross_tenant_access"},
		{ErrMissingTenantContext, "missing_tenant_context"},
		{ErrMissingJustification, "missing_justification"},
		{ErrMissingUserContext, "missing_user_context"},
		{ErrCircuitOpen, "circuit_open"},
		{ErrorCode(9999), "unknown"}, // default case
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := tc.code.String()
			if got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ErrorCode.HTTPStatus - missing codes
// ---------------------------------------------------------------------------

// TestErrorCode_HTTPStatus_MissingCodes verifies HTTP status mappings for
// error codes not covered by the existing test suite.
func TestErrorCode_HTTPStatus_MissingCodes(t *testing.T) {
	cases := []struct {
		code   ErrorCode
		status int
	}{
		{ErrNotAllowed, 403},
		{ErrSystemKeyspaceViolation, 403},
		{ErrCrossTenantAccess, 403},
		{ErrMissingTenantContext, 400},
		{ErrMissingJustification, 400},
		{ErrMissingUserContext, 400},
		{ErrQuorumLost, 503},
		{ErrSplitBrain, 500},
		{ErrRateLimited, 429},
		{ErrNotImplemented, 501},
		{ErrCircuitOpen, 503},
		{ErrorCode(9999), 500}, // default case
	}
	for _, tc := range cases {
		t.Run(tc.code.String(), func(t *testing.T) {
			got := tc.code.HTTPStatus()
			if got != tc.status {
				t.Errorf("HTTPStatus() = %d, want %d", got, tc.status)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && findSubstr(s, sub)
}

func findSubstr(s, sub string) bool {
	for i := range s {
		if i+len(sub) <= len(s) && s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
