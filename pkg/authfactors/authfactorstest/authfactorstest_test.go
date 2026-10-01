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

package authfactorstest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

func enrollAndChallenge(t *testing.T, f *Factor) (*authfactors.Enrollment, *authfactors.Challenge) {
	t.Helper()
	e, err := f.Enroll(context.Background(), &authfactors.EnrollParams{UserID: "u", TenantID: "t"})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if e.FactorName != f.Name() || e.UserID != "u" || e.TenantID != "t" || e.EnrollmentID == "" {
		t.Fatalf("enrollment = %+v", e)
	}
	c, err := f.Challenge(context.Background(), e)
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	if c.EnrollmentID != e.EnrollmentID || len(c.Nonce) != 32 || c.Expired() {
		t.Fatalf("challenge = %+v", c)
	}
	return e, c
}

func TestFactor_StrictAcceptsOnlyTheProof(t *testing.T) {
	f := New("piv")
	_, c := enrollAndChallenge(t, f)
	ctx := context.Background()

	if err := f.Verify(ctx, c, &authfactors.Response{Value: f.Respond(c.Nonce)}); err != nil {
		t.Fatalf("valid proof refused: %v", err)
	}
	var vf *authfactors.VerificationFailedError
	if err := f.Verify(ctx, c, &authfactors.Response{Value: []byte("junk")}); !errors.As(err, &vf) {
		t.Fatalf("junk proof: %v, want VerificationFailedError", err)
	}
	if err := New("piv").Verify(ctx, c, &authfactors.Response{Value: f.Respond(c.Nonce)}); !errors.As(err, &vf) {
		t.Fatalf("another device's proof: %v, want VerificationFailedError", err)
	}
	if f.Verifies() != 2 {
		t.Fatalf("Verifies = %d, want 2", f.Verifies())
	}
}

func TestFactor_PermissiveAcceptsAnyNonEmptyResponse(t *testing.T) {
	f := NewPermissive("presence")
	_, c := enrollAndChallenge(t, f)
	ctx := context.Background()

	if err := f.Verify(ctx, c, &authfactors.Response{Value: []byte("junk")}); err != nil {
		t.Fatalf("permissive refused a non-empty response: %v", err)
	}
	var vf *authfactors.VerificationFailedError
	if err := f.Verify(ctx, c, &authfactors.Response{}); !errors.As(err, &vf) {
		t.Fatalf("empty response: %v, want VerificationFailedError", err)
	}
	if err := f.Verify(ctx, c, nil); !errors.As(err, &vf) {
		t.Fatalf("nil response: %v, want VerificationFailedError", err)
	}
}

func TestFactor_NoncesAndIdentifiersAreFresh(t *testing.T) {
	f := New("piv")
	e1, c1 := enrollAndChallenge(t, f)
	e2, c2 := enrollAndChallenge(t, f)
	if e1.EnrollmentID == e2.EnrollmentID || c1.ChallengeID == c2.ChallengeID || bytes.Equal(c1.Nonce, c2.Nonce) {
		t.Fatal("identifiers or nonces repeated")
	}
}
