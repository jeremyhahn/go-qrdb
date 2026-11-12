// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lock

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

func TestSentinelErrors_DistinctMessages(t *testing.T) {
	seen := map[string]bool{}
	sentinels := []error{
		ErrInvalidTTL,
		ErrEmptyKey,
		ErrNilTxService,
		ErrNilLeaseService,
		ErrNilWatchService,
		ErrNilKVService,
	}
	for _, s := range sentinels {
		msg := s.Error()
		if seen[msg] {
			t.Errorf("duplicate sentinel message: %q", msg)
		}
		seen[msg] = true
	}
}

// ---------------------------------------------------------------------------
// LockHeldError
// ---------------------------------------------------------------------------

func TestLockHeldError_ContainsHolderID(t *testing.T) {
	err := &LockHeldError{Key: "mykey", HolderID: "holder-abc"}
	msg := err.Error()
	if !strings.Contains(msg, "holder-abc") {
		t.Errorf("LockHeldError.Error() = %q, want it to contain HolderID %q", msg, "holder-abc")
	}
}

func TestLockHeldError_ContainsKey(t *testing.T) {
	err := &LockHeldError{Key: "myresource", HolderID: "h1"}
	msg := err.Error()
	if !strings.Contains(msg, "myresource") {
		t.Errorf("LockHeldError.Error() = %q, want it to contain key %q", msg, "myresource")
	}
}

func TestLockHeldError_EmptyHolder(t *testing.T) {
	err := &LockHeldError{Key: "k", HolderID: ""}
	msg := err.Error()
	if !strings.Contains(msg, "lock: held by") {
		t.Errorf("LockHeldError.Error() = %q, want it to contain %q", msg, "lock: held by")
	}
}

// ---------------------------------------------------------------------------
// NotLockHolderError
// ---------------------------------------------------------------------------

func TestNotLockHolderError_ContainsKey(t *testing.T) {
	err := &NotLockHolderError{Key: "testkey", ExpectedToken: 10, ActualToken: 20}
	msg := err.Error()
	if !strings.Contains(msg, "testkey") {
		t.Errorf("NotLockHolderError.Error() = %q, want it to contain key %q", msg, "testkey")
	}
}

func TestNotLockHolderError_ContainsTokens(t *testing.T) {
	err := &NotLockHolderError{Key: "k", ExpectedToken: 42, ActualToken: 99}
	msg := err.Error()
	if !strings.Contains(msg, "42") {
		t.Errorf("NotLockHolderError.Error() = %q, want it to contain expected token 42", msg)
	}
	if !strings.Contains(msg, "99") {
		t.Errorf("NotLockHolderError.Error() = %q, want it to contain actual token 99", msg)
	}
}

func TestNotLockHolderError_ZeroTokens(t *testing.T) {
	err := &NotLockHolderError{Key: "k", ExpectedToken: 0, ActualToken: 0}
	msg := err.Error()
	if !strings.Contains(msg, "lock: caller is not the lock holder") {
		t.Errorf("NotLockHolderError.Error() = %q, want prefix %q", msg, "lock: caller is not the lock holder")
	}
}

// ---------------------------------------------------------------------------
// LockNotFoundError
// ---------------------------------------------------------------------------

func TestLockNotFoundError_ContainsKey(t *testing.T) {
	err := &LockNotFoundError{Key: "missing-resource"}
	msg := err.Error()
	if !strings.Contains(msg, "missing-resource") {
		t.Errorf("LockNotFoundError.Error() = %q, want it to contain key %q", msg, "missing-resource")
	}
}

func TestLockNotFoundError_ContainsPrefix(t *testing.T) {
	err := &LockNotFoundError{Key: "x"}
	msg := err.Error()
	if !strings.Contains(msg, "lock: not found") {
		t.Errorf("LockNotFoundError.Error() = %q, want it to contain %q", msg, "lock: not found")
	}
}

// TestRefusalMetadata_RoundTrips pins that every typed lock outcome survives
// its structured refusal: what Metadata encodes, the FromMetadata decoder of
// the same reason rebuilds field for field.
func TestRefusalMetadata_RoundTrips(t *testing.T) {
	held := &LockHeldError{Key: "orders/1", HolderID: "worker-7"}
	if got := LockHeldFromMetadata(held.Metadata()); *got != *held {
		t.Errorf("LockHeldFromMetadata = %+v, want %+v", got, held)
	}
	notHolder := &NotLockHolderError{Key: "orders/1", ExpectedToken: 41, ActualToken: -9}
	if got := NotLockHolderFromMetadata(notHolder.Metadata()); *got != *notHolder {
		t.Errorf("NotLockHolderFromMetadata = %+v, want %+v", got, notHolder)
	}
	notFound := &LockNotFoundError{Key: "orders/1"}
	if got := LockNotFoundFromMetadata(notFound.Metadata()); *got != *notFound {
		t.Errorf("LockNotFoundFromMetadata = %+v, want %+v", got, notFound)
	}
}

// TestRefusalMetadata_MissingFieldsKeepTheClass pins that the reason, not the
// metadata, says what happened: a decoder handed nothing, or a malformed
// token, still returns its typed error, with the unreadable fields zero.
func TestRefusalMetadata_MissingFieldsKeepTheClass(t *testing.T) {
	if got := LockHeldFromMetadata(nil); got == nil || *got != (LockHeldError{}) {
		t.Errorf("LockHeldFromMetadata(nil) = %+v, want the zero LockHeldError", got)
	}
	if got := LockNotFoundFromMetadata(nil); got == nil || *got != (LockNotFoundError{}) {
		t.Errorf("LockNotFoundFromMetadata(nil) = %+v, want the zero LockNotFoundError", got)
	}
	md := map[string]string{"key": "k", "expected_token": "not-a-number", "actual_token": "7"}
	want := NotLockHolderError{Key: "k", ActualToken: 7}
	if got := NotLockHolderFromMetadata(md); got == nil || *got != want {
		t.Errorf("NotLockHolderFromMetadata(%v) = %+v, want %+v", md, got, want)
	}
}

// TestRefusalReasons_AreDistinct pins that no two lock outcomes share a
// reason, since a client rebuilds the error from the reason alone.
func TestRefusalReasons_AreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range []string{ReasonLockHeld, ReasonNotLockHolder, ReasonLockNotFound} {
		if r == "" || seen[r] {
			t.Errorf("reason %q is empty or repeated", r)
		}
		seen[r] = true
	}
}
