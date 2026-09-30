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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/authfactorstest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// ---------------------------------------------------------------------------
// go-qrdb#577: the enrollment's factor is authoritative
//
// An enrollment is made with a strict factor ("piv"). A second, permissive
// factor ("presence") is registered beside it, and accepts any non-empty
// response. VerifyFactor used to dispatch to whatever factor name the caller
// supplied, so naming the permissive factor satisfied the strict enrollment's
// challenge with a junk response.
// ---------------------------------------------------------------------------

type confusionFixture struct {
	svc    AuthFactorServicer
	strict *authfactorstest.Factor
	weak   *authfactorstest.Factor
}

func newConfusionFixture(t *testing.T) *confusionFixture {
	t.Helper()
	strict := authfactorstest.New("piv")
	weak := authfactorstest.NewPermissive("presence")
	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{strict, weak},
		KV:      kvstore.NewMemoryKVStore(),
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}
	return &confusionFixture{svc: svc, strict: strict, weak: weak}
}

// challenge enrols a subject with the strict factor and issues a challenge.
func (f *confusionFixture) challenge(t *testing.T) (*authfactors.Enrollment, *authfactors.Challenge) {
	t.Helper()
	ctx := context.Background()
	e, err := f.svc.EnrollFactor(ctx, "piv", "custodian", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	c, err := f.svc.ChallengeFactor(ctx, e.EnrollmentID)
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	return e, c
}

func TestVerifyFactor_CallerFactorNameCannotOverrideEnrollment(t *testing.T) {
	f := newConfusionFixture(t)
	e, c := f.challenge(t)

	err := f.svc.VerifyFactor(context.Background(), "presence", c.ChallengeID,
		&authfactors.Response{Value: []byte("anything")})
	if err == nil {
		t.Fatal("a piv enrollment was satisfied through the permissive factor")
	}
	if f.weak.Verifies() != 0 {
		t.Fatalf("the caller-named factor's Verify ran %d times; it must never be consulted", f.weak.Verifies())
	}
	assertFactorMismatch(t, err, e.EnrollmentID, "piv", "presence")
}

// assertFactorMismatch requires err to be the typed mismatch naming both
// factors.
func assertFactorMismatch(t *testing.T, err error, enrollmentID, enrolled, requested string) {
	t.Helper()
	var mm *authfactors.FactorMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("err = %T %v, want *authfactors.FactorMismatchError", err, err)
	}
	if mm.EnrollmentID != enrollmentID || mm.Enrolled != enrolled || mm.Requested != requested {
		t.Fatalf("mismatch = %+v, want enrollment %s enrolled %s requested %s", mm, enrollmentID, enrolled, requested)
	}
	if mm.Error() == "" {
		t.Fatal("empty error message")
	}
}

// TestVerifyFactor_MismatchConsumesChallenge: the refused attempt burns the
// challenge, so a caller cannot probe factor names against one challenge.
func TestVerifyFactor_MismatchConsumesChallenge(t *testing.T) {
	f := newConfusionFixture(t)
	_, c := f.challenge(t)
	ctx := context.Background()

	if err := f.svc.VerifyFactor(ctx, "presence", c.ChallengeID, &authfactors.Response{Value: []byte("x")}); err == nil {
		t.Fatal("mismatch accepted")
	}
	err := f.svc.VerifyFactor(ctx, "piv", c.ChallengeID, &authfactors.Response{Value: f.strict.Respond(c.Nonce)})
	var nf *authfactors.EnrollmentNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("second attempt err = %v, want the consumed challenge refused as not found", err)
	}
}

// TestVerifyFactor_EnrolledFactorNameVerifies: naming the enrollment's own
// factor, with a valid proof, still verifies.
func TestVerifyFactor_EnrolledFactorNameVerifies(t *testing.T) {
	f := newConfusionFixture(t)
	_, c := f.challenge(t)
	if err := f.svc.VerifyFactor(context.Background(), "piv", c.ChallengeID,
		&authfactors.Response{Value: f.strict.Respond(c.Nonce)}); err != nil {
		t.Fatalf("VerifyFactor(piv, valid proof) = %v", err)
	}
}

// TestVerifyFactor_EmptyNameDispatchesToEnrollment: an empty name asserts
// nothing and dispatches to the enrollment's factor, which still checks the
// proof.
func TestVerifyFactor_EmptyNameDispatchesToEnrollment(t *testing.T) {
	f := newConfusionFixture(t)
	_, good := f.challenge(t)
	if err := f.svc.VerifyFactor(context.Background(), "", good.ChallengeID,
		&authfactors.Response{Value: f.strict.Respond(good.Nonce)}); err != nil {
		t.Fatalf("VerifyFactor(\"\", valid proof) = %v", err)
	}
	_, bad := f.challenge(t)
	err := f.svc.VerifyFactor(context.Background(), "", bad.ChallengeID, &authfactors.Response{Value: []byte("junk")})
	var vf *authfactors.VerificationFailedError
	if !errors.As(err, &vf) {
		t.Fatalf("VerifyFactor(\"\", junk) = %v, want *authfactors.VerificationFailedError", err)
	}
	if f.weak.Verifies() != 0 {
		t.Fatal("the permissive factor was consulted for a piv enrollment")
	}
}

// TestVerifyFactor_RevokedAfterChallengeIsRefused: revoking an enrollment
// voids the challenges already issued for it.
func TestVerifyFactor_RevokedAfterChallengeIsRefused(t *testing.T) {
	f := newConfusionFixture(t)
	e, c := f.challenge(t)
	ctx := context.Background()
	if err := f.svc.RevokeFactor(ctx, e.EnrollmentID); err != nil {
		t.Fatalf("RevokeFactor: %v", err)
	}
	err := f.svc.VerifyFactor(ctx, "piv", c.ChallengeID, &authfactors.Response{Value: f.strict.Respond(c.Nonce)})
	var rv *authfactors.EnrollmentRevokedError
	if !errors.As(err, &rv) {
		t.Fatalf("VerifyFactor after revoke = %v, want *authfactors.EnrollmentRevokedError", err)
	}
}

// TestVerifyFactor_RegistrationUsesTheSameNormalisation: a factor whose Name()
// is not lower case is registered under the same key the lookup computes, so
// it can be enrolled and verified at all.
func TestVerifyFactor_RegistrationUsesTheSameNormalisation(t *testing.T) {
	upper := authfactorstest.New("PIV")
	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{upper},
		KV:      kvstore.NewMemoryKVStore(),
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}
	ctx := context.Background()
	e, err := svc.EnrollFactor(ctx, "PIV", "custodian", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor(PIV): %v", err)
	}
	c, err := svc.ChallengeFactor(ctx, e.EnrollmentID)
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	if err := svc.VerifyFactor(ctx, "piv", c.ChallengeID, &authfactors.Response{Value: upper.Respond(c.Nonce)}); err != nil {
		t.Fatalf("VerifyFactor(piv) for a PIV enrollment = %v", err)
	}
}

// TestVerifyFactor_FactorNameComparedAsFactorLookupDoes:the caller's name is
// normalised exactly as the factor registry lookup normalises it, so the
// enrolled factor named in another case is the same factor, not a mismatch --
// and a different factor in any case is still refused.
func TestVerifyFactor_FactorNameComparedAsFactorLookupDoes(t *testing.T) {
	f := newConfusionFixture(t)
	_, c := f.challenge(t)
	if err := f.svc.VerifyFactor(context.Background(), "PIV", c.ChallengeID,
		&authfactors.Response{Value: f.strict.Respond(c.Nonce)}); err != nil {
		t.Fatalf("VerifyFactor(PIV) for a piv enrollment = %v", err)
	}
	e, c2 := f.challenge(t)
	err := f.svc.VerifyFactor(context.Background(), "Presence", c2.ChallengeID,
		&authfactors.Response{Value: []byte("x")})
	assertFactorMismatch(t, err, e.EnrollmentID, "piv", "Presence")
	if f.weak.Verifies() != 0 {
		t.Fatal("the permissive factor was consulted")
	}
}
