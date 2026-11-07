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

// TestEmbeddedAuthFactor_Delegates drives every auth-factor verb through a
// service that answers and through one that fails: the transport converts
// the answer and passes the failure through unchanged.
func TestEmbeddedAuthFactor_Delegates(t *testing.T) {
	ctx := t.Context()
	tests := []struct {
		name string
		call func(t *testing.T, tr *Transport) error
	}{
		{"EnrollFactor", func(t *testing.T, tr *Transport) error {
			resp, err := tr.EnrollFactor(ctx, &transport.EnrollFactorRequest{FactorName: "pkcs11", Subject: "alice", TenantID: "t1"})
			if err == nil && (resp.EnrollmentID != "enr-1" || resp.UserID != "alice") {
				t.Errorf("resp = %+v", resp)
			}
			return err
		}},
		{"ChallengeFactor", func(t *testing.T, tr *Transport) error {
			resp, err := tr.ChallengeFactor(ctx, &transport.ChallengeFactorRequest{EnrollmentID: "enr-1"})
			if err == nil && resp.ChallengeID != "ch-1" {
				t.Errorf("ChallengeID = %q", resp.ChallengeID)
			}
			return err
		}},
		{"VerifyFactor", func(_ *testing.T, tr *Transport) error {
			return tr.VerifyFactor(ctx, &transport.VerifyFactorRequest{FactorName: "pkcs11", ChallengeID: "ch-1"})
		}},
		{"ListFactors", func(t *testing.T, tr *Transport) error {
			resp, err := tr.ListFactors(ctx, &transport.ListFactorsRequest{Subject: "alice"})
			if err == nil && (resp.Total != 1 || len(resp.Enrollments) != 1) {
				t.Errorf("resp = %+v", resp)
			}
			return err
		}},
		{"RevokeFactor", func(_ *testing.T, tr *Transport) error {
			return tr.RevokeFactor(ctx, &transport.RevokeFactorRequest{EnrollmentID: "enr-1"})
		}},
	}
	ok := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	bad := newExtTransport(t, WithAuthFactorService(failingAuthFactorService(errSentinel)))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(t, ok); err != nil {
				t.Fatalf("answering service: %v", err)
			}
			if err := tt.call(t, bad); !errors.Is(err, errSentinel) {
				t.Fatalf("failing service: err = %v, want the service's error", err)
			}
		})
	}
}

// TestEmbeddedAuthFactor_ConvertsRequests asserts what the service receives
// and what the caller gets back where the transport fills in or maps a
// field rather than copying it.
func TestEmbeddedAuthFactor_ConvertsRequests(t *testing.T) {
	ctx := t.Context()
	t.Run("EnrollFactor fills nil MethodParams", func(t *testing.T) {
		var got authfactors.EnrollParams
		tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
			enrollFn: func(_ context.Context, _, _ string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
				got = params
				return &authfactors.Enrollment{EnrollmentID: "x"}, nil
			},
		}))
		if _, err := tr.EnrollFactor(ctx, &transport.EnrollFactorRequest{Subject: "alice"}); err != nil {
			t.Fatalf("EnrollFactor: %v", err)
		}
		if got.MethodParams == nil {
			t.Fatal("MethodParams should be initialized when nil")
		}
	})
	t.Run("VerifyFactor forwards the response and fills nil Metadata", func(t *testing.T) {
		var factor, challenge string
		var resp *authfactors.Response
		tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
			verifyFn: func(_ context.Context, f, ch string, r *authfactors.Response) error {
				factor, challenge, resp = f, ch, r
				return nil
			},
		}))
		if err := tr.VerifyFactor(ctx, &transport.VerifyFactorRequest{FactorName: "pkcs11", ChallengeID: "ch-1", Value: []byte("sig")}); err != nil {
			t.Fatalf("VerifyFactor: %v", err)
		}
		if factor != "pkcs11" || challenge != "ch-1" || resp == nil || string(resp.Value) != "sig" || resp.Metadata == nil {
			t.Fatalf("service received %q %q %+v", factor, challenge, resp)
		}
	})
	t.Run("ListFactors accepts a nil request", func(t *testing.T) {
		tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
		resp, err := tr.ListFactors(ctx, nil)
		if err != nil || resp == nil {
			t.Fatalf("ListFactors(nil) = %v, %v", resp, err)
		}
	})
	t.Run("ListFactors maps RevokedAt", func(t *testing.T) {
		tr := newExtTransport(t, WithAuthFactorService(&mockAuthFactorService{
			listFn: func(context.Context, string) ([]*authfactors.Enrollment, error) {
				return []*authfactors.Enrollment{
					{EnrollmentID: "a", EnrolledAt: time.Unix(1700000000, 0), RevokedAt: time.Unix(1700001000, 0)},
					{EnrollmentID: "b", EnrolledAt: time.Unix(1700000000, 0)},
				}, nil
			},
		}))
		resp, err := tr.ListFactors(ctx, &transport.ListFactorsRequest{Subject: "alice"})
		if err != nil {
			t.Fatalf("ListFactors: %v", err)
		}
		if resp.Enrollments[0].RevokedAt == nil || resp.Enrollments[1].RevokedAt != nil {
			t.Fatalf("RevokedAt = %v, %v; want set for the revoked enrollment only",
				resp.Enrollments[0].RevokedAt, resp.Enrollments[1].RevokedAt)
		}
	})
}

// TestEmbeddedAuthFactor_SealedAfterResolve asserts the embedded transport
// hands a caller the barrier's own sentinel, unchanged, on every verb when a
// tenant seals between Resolve and the store operation — the error
// pkg/service's real-barrier test proves the routed service returns. The SDK
// exports it as sdk.ErrSealed (an alias of seal.ErrSealed), which is what the
// auth-factors API documentation tells an embedded caller to match.
func TestEmbeddedAuthFactor_SealedAfterResolve(t *testing.T) {
	svc := failingAuthFactorService(seal.ErrSealed)
	svc.listFn = func(context.Context, string) ([]*authfactors.Enrollment, error) {
		return nil, &service.AuthFactorListReadError{Key: "__sys:authfactor/e1", Err: seal.ErrSealed}
	}
	tr := newExtTransport(t, WithAuthFactorService(svc))
	ctx := context.Background()
	calls := map[string]func() error{
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
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, seal.ErrSealed) {
				t.Errorf("%s = %v, want an error matching seal.ErrSealed", name, err)
			}
		})
	}
}

// mockAuthFactorService answers every auth-factor call with a fixed result;
// a non-nil Fn field replaces the answer.
type mockAuthFactorService struct {
	enrollFn    func(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error)
	challengeFn func(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error)
	verifyFn    func(ctx context.Context, factorName, challengeID string, response *authfactors.Response) error
	listFn      func(ctx context.Context, subject string) ([]*authfactors.Enrollment, error)
	revokeFn    func(ctx context.Context, enrollmentID string) error
}

var _ service.AuthFactorServicer = (*mockAuthFactorService)(nil)

// failingAuthFactorService returns a mockAuthFactorService whose every call
// fails with err.
func failingAuthFactorService(err error) *mockAuthFactorService {
	return &mockAuthFactorService{
		enrollFn: func(context.Context, string, string, authfactors.EnrollParams) (*authfactors.Enrollment, error) {
			return nil, err
		},
		challengeFn: func(context.Context, string) (*authfactors.Challenge, error) { return nil, err },
		verifyFn:    func(context.Context, string, string, *authfactors.Response) error { return err },
		listFn:      func(context.Context, string) ([]*authfactors.Enrollment, error) { return nil, err },
		revokeFn:    func(context.Context, string) error { return err },
	}
}

func (m *mockAuthFactorService) EnrollFactor(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	if m.enrollFn != nil {
		return m.enrollFn(ctx, factorName, subject, params)
	}
	return &authfactors.Enrollment{
		EnrollmentID: "enr-1",
		FactorName:   factorName,
		UserID:       subject,
		TenantID:     params.TenantID,
		Metadata:     map[string]string{"k": "v"},
		EnrolledAt:   time.Unix(1700000000, 0).UTC(),
	}, nil
}

func (m *mockAuthFactorService) ChallengeFactor(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error) {
	if m.challengeFn != nil {
		return m.challengeFn(ctx, enrollmentID)
	}
	return &authfactors.Challenge{
		ChallengeID:  "ch-1",
		EnrollmentID: enrollmentID,
		Nonce:        []byte("nonce"),
		ExpiresAt:    time.Unix(1700001000, 0).UTC(),
		MethodData:   map[string]any{"method": "pkcs11"},
	}, nil
}

func (m *mockAuthFactorService) VerifyFactor(ctx context.Context, factorName, challengeID string, response *authfactors.Response) error {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, factorName, challengeID, response)
	}
	return nil
}

func (m *mockAuthFactorService) ListFactors(ctx context.Context, subject string) ([]*authfactors.Enrollment, error) {
	if m.listFn != nil {
		return m.listFn(ctx, subject)
	}
	return []*authfactors.Enrollment{
		{EnrollmentID: "enr-1", FactorName: "pkcs11", UserID: subject, EnrolledAt: time.Unix(1700000000, 0).UTC()},
	}, nil
}

func (m *mockAuthFactorService) RevokeFactor(ctx context.Context, enrollmentID string) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, enrollmentID)
	}
	return nil
}
