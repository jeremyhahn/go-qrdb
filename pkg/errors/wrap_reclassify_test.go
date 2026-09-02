// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
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

// EVERY TEST IN THIS FILE WRAPS A REAL *QRDBError, and that is the point.
//
// Wrap's old behaviour was TYPE-DEPENDENT: wrapping a plain fmt.Errorf applied
// the caller's code correctly, and only wrapping a *QRDBError discarded it. So
// a test written with a plain error passed while production -- where the inner
// error is virtually always a QRDBError from a real operation -- took the
// other branch. That asymmetry is why the defect survived, and a test that
// does not wrap a QRDBError proves nothing about the deployed path
// (go-qrdb#187).

// TestWrap_ReclassifiesAQRDBError is the case with no coverage before #187.
func TestWrap_ReclassifiesAQRDBError(t *testing.T) {
	inner := New(ErrTimeout, "storage.Get", "timeout")
	wrapped := Wrap(inner, ErrCanceled, "retry.Do")

	if !Is(wrapped, ErrCanceled) {
		t.Errorf("Is(wrapped, ErrCanceled) = false: the caller's code was discarded")
	}
	if Is(wrapped, ErrTimeout) {
		t.Errorf("Is(wrapped, ErrTimeout) = true: the wrapper reports the inner code")
	}
	if got := Code(wrapped); got != ErrCanceled {
		t.Errorf("Code(wrapped) = %v, want ErrCanceled", got)
	}

	var qe *QRDBError
	if !As(wrapped, &qe) {
		t.Fatal("As did not resolve the wrapper")
	}
	if qe.Op != "retry.Do" {
		t.Errorf("Op = %q, want retry.Do", qe.Op)
	}

	// Reclassifying must not DESTROY the inner classification, only stop it
	// being what a single Is call answers about. The chain still carries it.
	var innerFound *QRDBError
	if !stderrors.As(qe.Unwrap(), &innerFound) {
		t.Fatal("the inner *QRDBError is not reachable through Unwrap: " +
			"reclassification must re-label the outer frame, not drop the cause")
	}
	if innerFound.Code != ErrTimeout {
		t.Errorf("inner Code = %v, want ErrTimeout", innerFound.Code)
	}
	if innerFound.Op != "storage.Get" {
		t.Errorf("inner Op = %q, want storage.Get", innerFound.Op)
	}
}

// A cancellation must never come back marked retryable. This is the constraint
// the #187 issue attached to every candidate resolution, and the half with a
// live consequence: a caller honouring Retry would retry an operation that was
// cancelled.
func TestWrap_CancellationIsNotRetryable(t *testing.T) {
	// ErrTimeout IS retryable, so the inner error is the worst case: under the
	// old behaviour Retry was inherited and the result reported true.
	inner := New(ErrTimeout, "storage.Get", "timeout")
	if !inner.IsRetryable() {
		t.Fatal("precondition: ErrTimeout must be retryable, or this test is vacuous")
	}

	wrapped := Wrap(inner, ErrCanceled, "retry.Do")

	var qe *QRDBError
	if !As(wrapped, &qe) {
		t.Fatal("As did not resolve the wrapper")
	}
	if qe.Retry {
		t.Error("Retry = true on a cancellation: retryability was inherited from the inner error")
	}
	// IsRetryable is Retry || Code.IsRetryable(), so both halves must be false.
	if qe.IsRetryable() {
		t.Error("IsRetryable() = true on a cancellation")
	}
}

// The type-dependence itself: both branches must now agree. Before #187 these
// two produced DIFFERENT classifications from identical call sites.
func TestWrap_SameClassificationForPlainAndQRDBInner(t *testing.T) {
	plain := Wrap(stderrors.New("boom"), ErrCanceled, "op")
	qrdb := Wrap(New(ErrTimeout, "inner", "boom"), ErrCanceled, "op")

	if Code(plain) != Code(qrdb) {
		t.Errorf("Wrap classifies by the inner error's TYPE: plain=%v qrdb=%v",
			Code(plain), Code(qrdb))
	}
	if Code(plain) != ErrCanceled {
		t.Errorf("Code = %v, want ErrCanceled", Code(plain))
	}
}

// A retryable reclassification must set Retry, not merely leave it false --
// the derivation runs in both directions.
func TestWrap_RetryableCodeSetsRetry(t *testing.T) {
	inner := New(ErrNotFound, "storage.Get", "missing") // not retryable
	wrapped := Wrap(inner, ErrUnavailable, "cluster.Propose")

	var qe *QRDBError
	if !As(wrapped, &qe) {
		t.Fatal("As did not resolve the wrapper")
	}
	if !qe.Retry {
		t.Error("Retry = false for ErrUnavailable: retryability must derive from the caller's code")
	}
}

// Context attached by a lower layer survives the wrap, and the wrapper does
// NOT share the inner's map. The old implementation assigned de.Context
// directly, so annotating either error mutated both.
func TestWrap_CarriesContextForwardWithoutAliasing(t *testing.T) {
	inner := New(ErrTimeout, "storage.Get", "timeout").WithContext("key", "k1")
	wrapped := Wrap(inner, ErrCanceled, "retry.Do")

	var qe *QRDBError
	if !As(wrapped, &qe) {
		t.Fatal("As did not resolve the wrapper")
	}
	if v, ok := qe.GetContext("key"); !ok || v != "k1" {
		t.Errorf("context not carried forward: got (%v, %v)", v, ok)
	}

	qe.WithContext("added", "on the wrapper")
	if _, ok := inner.GetContext("added"); ok {
		t.Error("the wrapper and the wrapped error share one Context map: " +
			"annotating the wrapper mutated the inner error")
	}
}

// Wrap(nil, ...) stays nil -- unchanged, asserted because the rewrite touched
// the function's only early return.
func TestWrap_NilIsNil(t *testing.T) {
	if got := Wrap(nil, ErrCanceled, "op"); got != nil {
		t.Errorf("Wrap(nil) = %v, want nil", got)
	}
}
