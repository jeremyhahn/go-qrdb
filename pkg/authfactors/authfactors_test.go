// Copyright 2026 Jeremy Hahn
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

package authfactors_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// ---------------------------------------------------------------------------
// Challenge.Expired tests
// ---------------------------------------------------------------------------

func TestChallenge_Expired_future(t *testing.T) {
	c := &authfactors.Challenge{
		ChallengeID: "c1",
		ExpiresAt:   time.Now().UTC().Add(5 * time.Minute),
	}
	if c.Expired() {
		t.Error("expected non-expired challenge to return false from Expired()")
	}
}

func TestChallenge_Expired_past(t *testing.T) {
	c := &authfactors.Challenge{
		ChallengeID: "c2",
		ExpiresAt:   time.Now().UTC().Add(-1 * time.Second),
	}
	if !c.Expired() {
		t.Error("expected past-expiry challenge to return true from Expired()")
	}
}

func TestChallenge_Expired_zeroValue(t *testing.T) {
	var c authfactors.Challenge
	// Zero time is before now, so Expired() should return true.
	if !c.Expired() {
		t.Error("expected zero-time challenge to be expired")
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestHardwareUnavailableError_Error(t *testing.T) {
	err := &authfactors.HardwareUnavailableError{Factor: "pkcs11", Reason: "library not found"}
	want := "authfactors: hardware unavailable [pkcs11]: library not found"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestEnrollmentInvalidError_withID(t *testing.T) {
	err := &authfactors.EnrollmentInvalidError{EnrollmentID: "e1", Reason: "missing public key"}
	want := "authfactors: invalid enrollment e1: missing public key"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestEnrollmentInvalidError_withoutID(t *testing.T) {
	err := &authfactors.EnrollmentInvalidError{Reason: "missing public key"}
	want := "authfactors: invalid enrollment: missing public key"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestChallengeExpiredError_Error(t *testing.T) {
	err := &authfactors.ChallengeExpiredError{ChallengeID: "ch1"}
	want := "authfactors: challenge expired: ch1"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestVerificationFailedError_Error(t *testing.T) {
	err := &authfactors.VerificationFailedError{FactorName: "piv", Reason: "invalid signature"}
	want := "authfactors: verification failed [piv]: invalid signature"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestFactorNotFoundError_Error(t *testing.T) {
	err := &authfactors.FactorNotFoundError{Name: "totp"}
	want := "authfactors: factor not found: totp"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestEnrollmentNotFoundError_Error(t *testing.T) {
	err := &authfactors.EnrollmentNotFoundError{EnrollmentID: "e99"}
	want := "authfactors: enrollment not found: e99"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestEnrollmentRevokedError_Error(t *testing.T) {
	err := &authfactors.EnrollmentRevokedError{EnrollmentID: "e42"}
	want := "authfactors: enrollment already revoked: e42"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestFactorMismatchError_Error(t *testing.T) {
	err := &authfactors.FactorMismatchError{EnrollmentID: "e7", Enrolled: "piv", Requested: "presence"}
	want := "authfactors: enrollment e7 was made with factor piv, not presence"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestFactorMismatchError_NamesBothFactors(t *testing.T) {
	err := &authfactors.FactorMismatchError{EnrollmentID: "e8", Enrolled: "fido2", Requested: "pkcs11"}
	msg := err.Error()
	for _, part := range []string{"e8", "fido2", "pkcs11"} {
		if !strings.Contains(msg, part) {
			t.Errorf("Error() = %q, missing %q", msg, part)
		}
	}
}

func TestSentinelErrors(t *testing.T) {
	if authfactors.ErrFactorDisabled == nil {
		t.Error("ErrFactorDisabled must not be nil")
	}
	if authfactors.ErrHardwareUnavailable == nil {
		t.Error("ErrHardwareUnavailable must not be nil")
	}
	if authfactors.ErrFactorDisabled.Error() == "" {
		t.Error("ErrFactorDisabled must have a non-empty message")
	}
	if authfactors.ErrHardwareUnavailable.Error() == "" {
		t.Error("ErrHardwareUnavailable must have a non-empty message")
	}
}

// ---------------------------------------------------------------------------
// NewEnrollmentID / NewChallengeID tests
// ---------------------------------------------------------------------------

func TestNewEnrollmentID_uniqueness(t *testing.T) {
	ids := make(map[string]struct{}, 100)
	for i := range 100 {
		id := authfactors.NewEnrollmentID()
		if id == "" {
			t.Fatalf("iteration %d: NewEnrollmentID() returned empty string", i)
		}
		if _, dup := ids[id]; dup {
			t.Fatalf("iteration %d: duplicate enrollment ID %q", i, id)
		}
		ids[id] = struct{}{}
	}
}

func TestNewChallengeID_uniqueness(t *testing.T) {
	ids := make(map[string]struct{}, 100)
	for i := range 100 {
		id := authfactors.NewChallengeID()
		if id == "" {
			t.Fatalf("iteration %d: NewChallengeID() returned empty string", i)
		}
		if _, dup := ids[id]; dup {
			t.Fatalf("iteration %d: duplicate challenge ID %q", i, id)
		}
		ids[id] = struct{}{}
	}
}

func TestNewEnrollmentID_differentFromChallengeID(t *testing.T) {
	eid := authfactors.NewEnrollmentID()
	cid := authfactors.NewChallengeID()
	// UUIDs should differ (not guaranteed, but astronomically unlikely to collide).
	if eid == cid {
		t.Errorf("NewEnrollmentID() == NewChallengeID(): %q (unexpected collision)", eid)
	}
}
