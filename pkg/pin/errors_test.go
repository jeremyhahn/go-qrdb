// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pin

import (
	"errors"
	"strings"
	"testing"
)

func TestErrStoragePersistFailed_Error(t *testing.T) {
	cause := errors.New("disk full")
	err := &ErrStoragePersistFailed{Key: "so-pin", Cause: cause}
	msg := err.Error()
	if !strings.Contains(msg, "so-pin") {
		t.Fatalf("Error() = %q, want it to contain the key", msg)
	}
	if !strings.Contains(msg, "disk full") {
		t.Fatalf("Error() = %q, want it to contain the cause", msg)
	}
}

func TestErrStoragePersistFailed_Unwrap(t *testing.T) {
	cause := errors.New("underlying")
	err := &ErrStoragePersistFailed{Key: "k", Cause: cause}
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is must surface wrapped cause")
	}
	if errors.Unwrap(err) != cause {
		t.Fatalf("Unwrap = %v, want %v", errors.Unwrap(err), cause)
	}
}

func TestErrStorageLoadFailed_Error(t *testing.T) {
	cause := errors.New("io error")
	err := &ErrStorageLoadFailed{Key: "user-pin", Cause: cause}
	msg := err.Error()
	if !strings.Contains(msg, "user-pin") {
		t.Fatalf("Error() = %q, want it to contain the key", msg)
	}
	if !strings.Contains(msg, "io error") {
		t.Fatalf("Error() = %q, want it to contain the cause", msg)
	}
}

func TestErrStorageLoadFailed_Unwrap(t *testing.T) {
	cause := errors.New("backend down")
	err := &ErrStorageLoadFailed{Key: "k", Cause: cause}
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is must surface wrapped cause")
	}
}

func TestErrUnsupportedPBKDF2Hash_Error(t *testing.T) {
	err := &ErrUnsupportedPBKDF2Hash{Hash: "md4"}
	msg := err.Error()
	if !strings.Contains(msg, "md4") {
		t.Fatalf("Error() = %q, want it to contain hash name", msg)
	}
}

func TestErrTPMLocked_Error(t *testing.T) {
	err := &ErrTPMLocked{Status: &LockoutStatus{RecoverySeconds: 42}}
	msg := err.Error()
	if !strings.Contains(msg, "42") {
		t.Fatalf("Error() = %q, want it to contain recovery seconds", msg)
	}
	if !strings.Contains(msg, "locked") {
		t.Fatalf("Error() = %q, want it to mention 'locked'", msg)
	}
}

func TestErrPINInvalidWithStatus_Error(t *testing.T) {
	err := &ErrPINInvalidWithStatus{Status: &LockoutStatus{FailedAttempts: 2, MaxAttempts: 5}}
	msg := err.Error()
	// Expect "3 of 5 attempts remaining" — the message must surface both
	// the remaining count and the max so the operator knows the budget.
	if !strings.Contains(msg, "3 of 5") {
		t.Fatalf("Error() = %q, want it to surface 3 of 5 attempts remaining", msg)
	}
}

func TestErrPINInvalidWithStatus_ZeroRemaining(t *testing.T) {
	err := &ErrPINInvalidWithStatus{Status: &LockoutStatus{FailedAttempts: 5, MaxAttempts: 5}}
	msg := err.Error()
	if !strings.Contains(msg, "0 of 5") {
		t.Fatalf("Error() = %q, want 0 of 5 (locked out boundary)", msg)
	}
}
