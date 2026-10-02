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

package factortest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// TestFactor_RoundTrip pins the double's faithful behaviour: an enrolled
// authenticator's assertion verifies, another authenticator's does not, and
// an expired challenge is refused.
func TestFactor_RoundTrip(t *testing.T) {
	ctx := context.Background()
	f := New("fido2", time.Minute)
	if f.Name() != "fido2" {
		t.Fatalf("Name = %q", f.Name())
	}
	a, err := NewAuthenticator()
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewAuthenticator()
	if err != nil {
		t.Fatal(err)
	}
	enr, err := f.Enroll(ctx, &authfactors.EnrollParams{UserID: "carol", MethodParams: a.EnrollParams()})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	ch, err := f.Challenge(ctx, enr)
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	if err := f.Verify(ctx, ch, &authfactors.Response{Value: a.Assert(ch.Nonce)}); err != nil {
		t.Errorf("the enrolled authenticator must verify: %v", err)
	}
	var vf *authfactors.VerificationFailedError
	if err := f.Verify(ctx, ch, &authfactors.Response{Value: other.Assert(ch.Nonce)}); !errors.As(err, &vf) {
		t.Errorf("another authenticator must be refused: %v", err)
	}
	ch.ExpiresAt = time.Now().Add(-time.Second)
	var expired *authfactors.ChallengeExpiredError
	if err := f.Verify(ctx, ch, &authfactors.Response{Value: a.Assert(ch.Nonce)}); !errors.As(err, &expired) {
		t.Errorf("an expired challenge must be refused: %v", err)
	}
}

// TestFactor_Refusals covers the argument checks.
func TestFactor_Refusals(t *testing.T) {
	ctx := context.Background()
	f := New("fido2", 0)
	if f.ttl != 5*time.Minute {
		t.Errorf("default ttl = %v", f.ttl)
	}
	if _, err := f.Enroll(ctx, nil); err == nil {
		t.Error("nil params must be refused")
	}
	if _, err := f.Enroll(ctx, &authfactors.EnrollParams{MethodParams: map[string]any{ParamPublicKey: "AAAA"}}); err == nil {
		t.Error("a short key must be refused")
	}
	if _, err := f.Challenge(ctx, nil); err == nil {
		t.Error("a nil enrolment must be refused")
	}
	if err := f.Verify(ctx, nil, nil); err == nil {
		t.Error("a nil challenge must be refused")
	}
	ch := &authfactors.Challenge{ExpiresAt: time.Now().Add(time.Minute), MethodData: map[string]any{}}
	if err := f.Verify(ctx, ch, &authfactors.Response{Value: []byte{1}}); err == nil {
		t.Error("a challenge without an enrolled key must be refused")
	}
}

// TestFactor_DecoyEnrollment: the decoy is this factor's shape, derived from
// the seed and the user alone -- the same on every call, different for
// another user or seed -- and a challenge issued from it is well formed.
func TestFactor_DecoyEnrollment(t *testing.T) {
	f := New("fido2", time.Minute)
	seed := []byte("decoy-seed-0123456789abcdef01234")
	a, err := f.DecoyEnrollment(seed, "mallory", nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.DecoyEnrollment(seed, "mallory", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.EnrollmentID != again.EnrollmentID || string(a.PublicKey) != string(again.PublicKey) {
		t.Fatal("the decoy is not stable across calls")
	}
	if a.FactorName != "fido2" || a.UserID != "mallory" {
		t.Fatalf("decoy = %+v", a)
	}
	other, err := f.DecoyEnrollment(seed, "trudy", nil)
	if err != nil {
		t.Fatal(err)
	}
	reseeded, err := f.DecoyEnrollment([]byte("another-seed-0123456789abcdef012"), "mallory", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(other.PublicKey) == string(a.PublicKey) || string(reseeded.PublicKey) == string(a.PublicKey) {
		t.Fatal("the decoy does not depend on the user and the seed")
	}
	if _, err := f.Challenge(context.Background(), a); err != nil {
		t.Fatalf("challenge from the decoy: %v", err)
	}
}
