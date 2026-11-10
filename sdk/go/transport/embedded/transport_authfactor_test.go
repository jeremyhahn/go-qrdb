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

package embedded

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestEmbeddedAuthFactor_Enroll(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	resp, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{
		FactorName: "pkcs11",
		Subject:    "alice",
		TenantID:   "t1",
	})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if resp.EnrollmentID != "enr-1" {
		t.Fatalf("EnrollmentID = %q", resp.EnrollmentID)
	}
	if resp.UserID != "alice" {
		t.Fatalf("UserID = %q", resp.UserID)
	}
}

func TestEmbeddedAuthFactor_Enroll_NilMethodParams(t *testing.T) {
	var got authfactors.EnrollParams
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
		enrollFn: func(_ context.Context, _, _ string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
			got = params
			return &authfactors.Enrollment{EnrollmentID: "x"}, nil
		},
	}))
	_, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{Subject: "alice"})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if got.MethodParams == nil {
		t.Fatalf("MethodParams should be initialized when nil")
	}
}

func TestEmbeddedAuthFactor_Challenge(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	resp, err := tr.ChallengeFactor(context.Background(), &transport.ChallengeFactorRequest{EnrollmentID: "enr-1"})
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	if resp.ChallengeID != "ch-1" {
		t.Fatalf("ChallengeID = %q", resp.ChallengeID)
	}
}

func TestEmbeddedAuthFactor_Verify(t *testing.T) {
	var capturedFactor, capturedCh string
	var capturedResp *authfactors.Response
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
		verifyFn: func(_ context.Context, factor, ch string, r *authfactors.Response) error {
			capturedFactor = factor
			capturedCh = ch
			capturedResp = r
			return nil
		},
	}))
	if err := tr.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{
		FactorName:  "pkcs11",
		ChallengeID: "ch-1",
		Value:       []byte("sig"),
	}); err != nil {
		t.Fatalf("VerifyFactor: %v", err)
	}
	if capturedFactor != "pkcs11" || capturedCh != "ch-1" {
		t.Fatalf("unexpected: %q %q", capturedFactor, capturedCh)
	}
	if capturedResp == nil || string(capturedResp.Value) != "sig" {
		t.Fatalf("unexpected response: %+v", capturedResp)
	}
	if capturedResp.Metadata == nil {
		t.Fatalf("Metadata should be initialized when nil")
	}
}

func TestEmbeddedAuthFactor_List(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	resp, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{Subject: "alice"})
	if err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
	if resp.Total != 1 || len(resp.Enrollments) != 1 {
		t.Fatalf("unexpected: %+v", resp)
	}
}

func TestEmbeddedAuthFactor_List_NilRequest(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	resp, err := tr.ListFactors(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
}

func TestEmbeddedAuthFactor_List_WithRevoked(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
		listFn: func(_ context.Context, _ string) ([]*authfactors.Enrollment, error) {
			return []*authfactors.Enrollment{
				{EnrollmentID: "a", EnrolledAt: time.Unix(1700000000, 0), RevokedAt: time.Unix(1700001000, 0)},
				{EnrollmentID: "b", EnrolledAt: time.Unix(1700000000, 0)},
			}, nil
		},
	}))
	resp, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{Subject: "alice"})
	if err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
	if resp.Enrollments[0].RevokedAt == nil {
		t.Fatal("expected RevokedAt set for revoked enrollment")
	}
	if resp.Enrollments[1].RevokedAt != nil {
		t.Fatal("expected RevokedAt nil for active enrollment")
	}
}

func TestEmbeddedAuthFactor_Revoke(t *testing.T) {
	var revoked string
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
		revokeFn: func(_ context.Context, enrollmentID string) error {
			revoked = enrollmentID
			return nil
		},
	}))
	if err := tr.RevokeFactor(context.Background(), &transport.RevokeFactorRequest{EnrollmentID: "enr-1"}); err != nil {
		t.Fatalf("RevokeFactor: %v", err)
	}
	if revoked != "enr-1" {
		t.Fatalf("service revoked %q, want enr-1", revoked)
	}
}

// authFactorCalls drives every auth-factor verb of tr once.
func authFactorCalls(tr *Transport) map[string]func() error {
	ctx := context.Background()
	return map[string]func() error{
		"EnrollFactor": func() error {
			_, err := tr.EnrollFactor(ctx, &transport.EnrollFactorRequest{FactorName: "piv", Subject: "u1"})
			return err
		},
		"ChallengeFactor": func() error {
			_, err := tr.ChallengeFactor(ctx, &transport.ChallengeFactorRequest{EnrollmentID: "e1"})
			return err
		},
		"VerifyFactor": func() error {
			return tr.VerifyFactor(ctx, &transport.VerifyFactorRequest{ChallengeID: "000000000001.c"})
		},
		"ListFactors": func() error {
			_, err := tr.ListFactors(ctx, &transport.ListFactorsRequest{})
			return err
		},
		"RevokeFactor": func() error {
			return tr.RevokeFactor(ctx, &transport.RevokeFactorRequest{EnrollmentID: "e1"})
		},
	}
}

// Every verb answers a transport built without an auth-factor service with
// the typed wiring-defect error.
func TestEmbeddedAuthFactor_NilServiceIsTyped(t *testing.T) {
	for name, call := range authFactorCalls(newExtTransport(t)) {
		var ne *NilServiceError
		if err := call(); !errors.As(err, &ne) {
			t.Errorf("%s: err = %T (%v), want *NilServiceError", name, err, err)
		}
	}
}

// Every verb on a closed transport is refused with *transport.ClosedError
// before it reaches the service.
func TestEmbeddedAuthFactor_ClosedIsRefused(t *testing.T) {
	for name, call := range authFactorCalls(closedExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))) {
		var ce *transport.ClosedError
		if err := call(); !errors.As(err, &ce) {
			t.Errorf("%s: err = %T (%v), want *transport.ClosedError", name, err, err)
		}
	}
}

// TestEmbeddedAuthFactor_SealedAfterResolve asserts the embedded transport
// hands a caller the barrier's own sentinel, unchanged, on every verb when a
// tenant seals between Resolve and the store operation — the error
// pkg/service's real-barrier test proves the routed service returns. The SDK
// exports it as sdk.ErrSealed (an alias of seal.ErrSealed), which is what the
// auth-factors API documentation tells an embedded caller to match.
func TestEmbeddedAuthFactor_SealedAfterResolve(t *testing.T) {
	tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
		enrollFn: func(context.Context, string, string, authfactors.EnrollParams) (*authfactors.Enrollment, error) {
			return nil, seal.ErrSealed
		},
		challengeFn: func(context.Context, string) (*authfactors.Challenge, error) { return nil, seal.ErrSealed },
		verifyFn:    func(context.Context, string, string, *authfactors.Response) error { return seal.ErrSealed },
		listFn: func(context.Context, string) ([]*authfactors.Enrollment, error) {
			return nil, &service.AuthFactorListReadError{Key: "__sys:authfactor/e1", Err: seal.ErrSealed}
		},
		revokeFn: func(context.Context, string) error { return seal.ErrSealed },
	}))
	for name, call := range authFactorCalls(tr) {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, seal.ErrSealed) {
				t.Errorf("%s = %v, want an error matching seal.ErrSealed", name, err)
			}
		})
	}
}
