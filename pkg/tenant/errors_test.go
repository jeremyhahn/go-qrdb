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

package tenant

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

func TestInvalidTenantIDErrorMessage(t *testing.T) {
	err := &InvalidTenantIDError{TenantID: "bad!tenant", Reason: "contains special characters"}
	got := err.Error()
	if !strings.Contains(got, "invalid tenant ID") {
		t.Errorf("Error() = %q, want it to contain %q", got, "invalid tenant ID")
	}
	if !strings.Contains(got, "bad!tenant") {
		t.Errorf("Error() = %q, want it to contain %q", got, "bad!tenant")
	}
	if !strings.Contains(got, "contains special characters") {
		t.Errorf("Error() = %q, want it to contain %q", got, "contains special characters")
	}
}

func TestInvalidTenantIDErrorEmptyTenantID(t *testing.T) {
	err := &InvalidTenantIDError{TenantID: "", Reason: "must not be empty"}
	got := err.Error()
	if !strings.Contains(got, "must not be empty") {
		t.Errorf("Error() = %q, want it to contain %q", got, "must not be empty")
	}
}

func TestInvalidTenantIDErrorImplementsErrorInterface(t *testing.T) {
	err := &InvalidTenantIDError{TenantID: "x", Reason: "test"}
	var _ error = err
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestInvalidTenantIDErrorFormat(t *testing.T) {
	err := &InvalidTenantIDError{TenantID: "abc", Reason: "too short"}
	want := "invalid tenant ID abc: too short"
	got := err.Error()
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestInvalidTenantIDCharError_NonEmpty(t *testing.T) {
	err := &InvalidTenantIDCharError{TenantID: "bad\x1f", Byte: 0x1f}
	if err.Error() == "" {
		t.Fatal("Error() must not be empty")
	}
}

func TestInvalidTenantIDCharError_ContainsHex(t *testing.T) {
	err := &InvalidTenantIDCharError{TenantID: "b", Byte: 0x1f}
	got := err.Error()
	if !strings.Contains(got, "1f") {
		t.Errorf("Error() = %q, want it to contain hex representation of byte", got)
	}
}

func TestInvalidTenantIDCharError_ImplementsError(t *testing.T) {
	var _ error = &InvalidTenantIDCharError{TenantID: "x", Byte: 0x00}
}

// ---------------------------------------------------------------------------
// StoreError.ReadTransient / newStoreError / isTransientCause (BL2's
// producer-declares-transience marker -- see the doc comments on StoreError,
// newStoreError and isTransientCause).
// ---------------------------------------------------------------------------

func TestTenantStoreError_Message(t *testing.T) {
	err := &StoreError{Op: "get", Err: errors.New("boom")}
	want := "tenant: get: boom"
	if got := err.Error(); got != want {
		t.Fatalf("Error(): got %q, want %q", got, want)
	}
}

func TestTenantStoreError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	err := &StoreError{Op: "put", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("Unwrap should allow errors.Is to match the inner error")
	}
}

// TestTenantStoreError_ReadTransient_DefaultsFalse pins the zero value: a
// composite literal built outside newStoreError -- the only shape anything
// outside this package can construct, since transient is unexported -- can
// never assert its own transience.
func TestTenantStoreError_ReadTransient_DefaultsFalse(t *testing.T) {
	err := &StoreError{Op: "unmarshal", Err: errors.New("invalid character")}
	if err.ReadTransient() {
		t.Fatal("a bare composite literal must read ReadTransient() = false; only newStoreError sets it true")
	}
}

// TestTenantStoreError_ReadTransient_TrueWhenSetByNewStoreError proves the
// method reports exactly the field newStoreError computed.
func TestTenantStoreError_ReadTransient_TrueWhenSetByNewStoreError(t *testing.T) {
	err := newStoreError("get", errors.New("read index not ready"))
	if !err.ReadTransient() {
		t.Fatal("newStoreError on a non-decrypt cause must produce ReadTransient() = true")
	}
}

// TestNewStoreError_NonDecryptCauseIsTransient covers an ordinary read
// failure -- not a decrypt refusal -- which newStoreError must mark
// transient.
func TestNewStoreError_NonDecryptCauseIsTransient(t *testing.T) {
	cause := errors.New("leader lost")
	err := newStoreError("get", cause)
	if !err.ReadTransient() {
		t.Fatal("a non-decrypt cause must classify as transient")
	}
	if !errors.Is(err, cause) {
		t.Fatal("newStoreError must still wrap cause for errors.Is")
	}
}

// TestNewStoreError_DecryptCauseIsNotTransient is N1: a sealed-barrier-backed
// store's decrypt/authentication refusal is definitive, because no retry
// decrypts a tampered ciphertext or authenticates with the wrong key.
func TestNewStoreError_DecryptCauseIsNotTransient(t *testing.T) {
	err := newStoreError("get", seal.ErrDecryptionFailed)
	if err.ReadTransient() {
		t.Fatal("a decrypt-failure cause must classify as definitive (ReadTransient() = false)")
	}
}

// TestNewStoreError_WrappedDecryptCauseIsNotTransient: isTransientCause must
// find seal.ErrDecryptionFailed through a wrapper, not only when it is the
// cause verbatim -- errors.Is, not ==.
func TestNewStoreError_WrappedDecryptCauseIsNotTransient(t *testing.T) {
	wrapped := fmt.Errorf("decrypt tenant record: %w", seal.ErrDecryptionFailed)
	err := newStoreError("get", wrapped)
	if err.ReadTransient() {
		t.Fatal("a wrapped decrypt-failure cause must still classify as definitive")
	}
}

// TestIsTransientCause_NonDecryptCause and TestIsTransientCause_DecryptCause
// exercise isTransientCause directly, independent of newStoreError, so a
// future caller of it is held to the same two-cause contract. This is the
// exact gap a re-review found: forcing tenant's isTransientCause to always
// return true left the whole suite green, because nothing exercised its
// decrypt-exclusion branch on this package's side.
func TestIsTransientCause_NonDecryptCause(t *testing.T) {
	if !isTransientCause(errors.New("leader lost")) {
		t.Fatal("isTransientCause(a generic read failure) must be true")
	}
}

func TestIsTransientCause_DecryptCause(t *testing.T) {
	if isTransientCause(seal.ErrDecryptionFailed) {
		t.Fatal("isTransientCause(seal.ErrDecryptionFailed) must be false")
	}
}

func TestFirstAdminProofError_NamesTenantReasonAndCause(t *testing.T) {
	cause := &initproof.ChallengeError{Reason: initproof.ReasonExpired}
	err := &FirstAdminProofError{TenantID: "acme", Reason: initproof.ReasonExpired, Err: cause}
	got := err.Error()
	for _, want := range []string{`"acme"`, string(initproof.ReasonExpired), cause.Error()} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, want it to contain %q", got, want)
		}
	}
	var ce *initproof.ChallengeError
	if !errors.As(err, &ce) {
		t.Fatal("FirstAdminProofError does not unwrap to its cause")
	}
}

func TestFirstAdminProofError_WithoutCause(t *testing.T) {
	err := &FirstAdminProofError{TenantID: "acme", Reason: initproof.ReasonWrongKey}
	if err.Unwrap() != nil {
		t.Fatal("Unwrap of a causeless error is not nil")
	}
	if !strings.HasSuffix(err.Error(), string(initproof.ReasonWrongKey)) {
		t.Fatalf("Error() = %q", err.Error())
	}
}

// TestRefusalErrors_MessageNamesEveryField checks that each refusal's
// message carries the values an operator needs to act on it.
func TestRefusalErrors_MessageNamesEveryField(t *testing.T) {
	cause := errors.New("disk on fire")
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{"platform operator required", &PlatformOperatorRequiredError{IdentityTenantID: "acme", Op: "create a tenant"},
			[]string{"acme", "create a tenant", "platform operator"}},
		{"volume pool reserve", &VolumePoolReserveError{TenantID: "acme", Pool: "fast", Err: cause},
			[]string{"acme", "fast", "disk on fire"}},
		{"roster requires shamir", &RosterRequiresShamirError{Strategy: "passphrase"},
			[]string{"shamir", "passphrase"}},
		{"invalid factor policy", &InvalidFactorPolicyError{Policy: "bogus"},
			[]string{"bogus"}},
		{"database list", &DatabaseListError{TenantID: "acme", Err: cause},
			[]string{"acme", "disk on fire"}},
		{"strict requires dedicated storage", &StrictRequiresDedicatedStorageError{IsolationType: IsolationPrefix},
			[]string{string(IsolationPrefix), "strict"}},
		{"isolation policy change", &IsolationPolicyChangeError{From: "strict", To: "standard"},
			[]string{"strict", "standard", "tighten only"}},
		{"invalid isolation policy", &InvalidIsolationPolicyError{Value: "loose"},
			[]string{`"loose"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tt.err.Error()
			for _, w := range tt.want {
				if !strings.Contains(msg, w) {
					t.Errorf("Error() = %q, want it to contain %q", msg, w)
				}
			}
		})
	}
}

// TestRefusalErrors_UnwrapReachesTheCause checks that the wrapping refusals
// keep their cause reachable through errors.Is.
func TestRefusalErrors_UnwrapReachesTheCause(t *testing.T) {
	cause := errors.New("underlying")
	tests := []struct {
		name string
		err  error
	}{
		{"volume pool reserve", &VolumePoolReserveError{TenantID: "acme", Pool: "fast", Err: cause}},
		{"keyspace reclaim", &KeyspaceReclaimError{TenantID: "acme", Err: cause}},
		{"database list", &DatabaseListError{TenantID: "acme", Err: cause}},
		{"teardown", &TeardownError{TenantID: "acme", Err: &KeyspaceReclaimError{TenantID: "acme", Err: cause}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.err, cause) {
				t.Fatalf("errors.Is(%v, cause) = false", tt.err)
			}
		})
	}
}

// TestTenantDeletedError_MessageWithAndWithoutTime covers both forms: a
// decoded tombstone names when the id was retired, an undecodable one does
// not.
func TestTenantDeletedError_MessageWithAndWithoutTime(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name    string
		err     *TenantDeletedError
		want    string
		notWant string
	}{
		{"zero time", &TenantDeletedError{TenantID: "acme"}, "acme was deleted and cannot be reused", "deleted at"},
		{"with time", &TenantDeletedError{TenantID: "acme", DeletedAt: at}, "deleted at 2026-01-02T03:04:05Z", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tt.err.Error()
			if !strings.Contains(msg, tt.want) {
				t.Errorf("Error() = %q, want it to contain %q", msg, tt.want)
			}
			if tt.notWant != "" && strings.Contains(msg, tt.notWant) {
				t.Errorf("Error() = %q, must not contain %q", msg, tt.notWant)
			}
		})
	}
}
