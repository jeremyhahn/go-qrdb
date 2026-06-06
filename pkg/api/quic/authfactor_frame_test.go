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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// mockAuthFactorService implements service.AuthFactorServicer for QUIC tests.
// ---------------------------------------------------------------------------

type mockAuthFactorService struct {
	enrollFn    func(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error)
	challengeFn func(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error)
	verifyFn    func(ctx context.Context, factorName, challengeID string, response *authfactors.Response) error
	listFn      func(ctx context.Context, subject string) ([]*authfactors.Enrollment, error)
	revokeFn    func(ctx context.Context, enrollmentID string) error
}

var _ service.AuthFactorServicer = (*mockAuthFactorService)(nil)

func newMockAuthFactorService() *mockAuthFactorService { return &mockAuthFactorService{} }

func (m *mockAuthFactorService) EnrollFactor(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	if m.enrollFn != nil {
		return m.enrollFn(ctx, factorName, subject, params)
	}
	return &authfactors.Enrollment{
		EnrollmentID: "enroll-1",
		FactorName:   factorName,
		UserID:       subject,
		TenantID:     params.TenantID,
		EnrolledAt:   time.Now(),
	}, nil
}

func (m *mockAuthFactorService) ChallengeFactor(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error) {
	if m.challengeFn != nil {
		return m.challengeFn(ctx, enrollmentID)
	}
	return &authfactors.Challenge{
		ChallengeID:  "challenge-1",
		EnrollmentID: enrollmentID,
		Nonce:        []byte("nonce"),
		ExpiresAt:    time.Now().Add(5 * time.Minute),
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
		{EnrollmentID: "e1", FactorName: "pkcs11", UserID: subject, EnrolledAt: time.Now()},
	}, nil
}

func (m *mockAuthFactorService) RevokeFactor(ctx context.Context, enrollmentID string) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, enrollmentID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// handleAuthFactorEnroll
// ---------------------------------------------------------------------------

func TestHandleAuthFactorEnroll_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicEnrollFactorRequest{FactorName: "pkcs11", Subject: "alice"})

	_, err := srv.handleAuthFactorEnroll(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleAuthFactorEnroll_MissingFields(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))
	// Only subject, missing factor_name
	payload, _ := json.Marshal(quicEnrollFactorRequest{Subject: "alice"})

	_, err := srv.handleAuthFactorEnroll(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for missing factor_name")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleAuthFactorEnroll_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.enrollFn = func(_ context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
		return &authfactors.Enrollment{
			EnrollmentID: "e-42",
			FactorName:   factorName,
			UserID:       subject,
			TenantID:     params.TenantID,
			EnrolledAt:   time.Now(),
		}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))

	payload, _ := json.Marshal(quicEnrollFactorRequest{FactorName: "piv", Subject: "bob", TenantID: "tenant-1"})
	resp, err := srv.handleAuthFactorEnroll(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["enrollment_id"] != "e-42" {
		t.Errorf("expected enrollment_id 'e-42', got %v", out["enrollment_id"])
	}
	if out["factor_name"] != "piv" {
		t.Errorf("expected factor_name 'piv', got %v", out["factor_name"])
	}
}

func TestHandleAuthFactorEnroll_UnmarshalError(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))

	_, err := srv.handleAuthFactorEnroll(context.Background(), []byte(`{bad json`))
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleAuthFactorEnroll_ServiceError(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.enrollFn = func(_ context.Context, _, _ string, _ authfactors.EnrollParams) (*authfactors.Enrollment, error) {
		return nil, errors.New("enroll failed")
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))
	payload, _ := json.Marshal(quicEnrollFactorRequest{FactorName: "pkcs11", Subject: "alice"})

	_, err := srv.handleAuthFactorEnroll(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "enroll failed" {
		t.Errorf("expected 'enroll failed', got %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorChallenge
// ---------------------------------------------------------------------------

func TestHandleAuthFactorChallenge_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicChallengeFactorRequest{EnrollmentID: "e1"})

	_, err := srv.handleAuthFactorChallenge(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleAuthFactorChallenge_MissingEnrollmentID(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))
	payload, _ := json.Marshal(quicChallengeFactorRequest{})

	_, err := srv.handleAuthFactorChallenge(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for missing enrollment_id")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleAuthFactorChallenge_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))

	payload, _ := json.Marshal(quicChallengeFactorRequest{EnrollmentID: "e1"})
	resp, err := srv.handleAuthFactorChallenge(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["challenge_id"] != "challenge-1" {
		t.Errorf("expected challenge_id 'challenge-1', got %v", out["challenge_id"])
	}
}

func TestHandleAuthFactorChallenge_ServiceError(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.challengeFn = func(_ context.Context, _ string) (*authfactors.Challenge, error) {
		return nil, errors.New("not found")
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))
	payload, _ := json.Marshal(quicChallengeFactorRequest{EnrollmentID: "e1"})

	_, err := srv.handleAuthFactorChallenge(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorVerify
// ---------------------------------------------------------------------------

func TestHandleAuthFactorVerify_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicVerifyFactorRequest{ChallengeID: "c1", Value: []byte("sig")})

	_, err := srv.handleAuthFactorVerify(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleAuthFactorVerify_MissingChallengeID(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))
	payload, _ := json.Marshal(quicVerifyFactorRequest{Value: []byte("sig")})

	_, err := srv.handleAuthFactorVerify(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for missing challenge_id")
	}
}

func TestHandleAuthFactorVerify_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))
	payload, _ := json.Marshal(quicVerifyFactorRequest{ChallengeID: "c1", Value: []byte("sig")})

	resp, err := srv.handleAuthFactorVerify(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]string
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["status"] != "verified" {
		t.Errorf("expected status 'verified', got %q", out["status"])
	}
}

func TestHandleAuthFactorVerify_ServiceError(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.verifyFn = func(_ context.Context, _, _ string, _ *authfactors.Response) error {
		return errors.New("sig mismatch")
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))
	payload, _ := json.Marshal(quicVerifyFactorRequest{ChallengeID: "c1", Value: []byte("bad")})

	_, err := srv.handleAuthFactorVerify(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorList
// ---------------------------------------------------------------------------

func TestHandleAuthFactorList_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	_, err := srv.handleAuthFactorList(context.Background(), []byte(`{}`))
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleAuthFactorList_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.listFn = func(_ context.Context, subject string) ([]*authfactors.Enrollment, error) {
		return []*authfactors.Enrollment{
			{EnrollmentID: "e1", FactorName: "pkcs11", UserID: subject, EnrolledAt: time.Now()},
			{EnrollmentID: "e2", FactorName: "piv", UserID: subject, EnrolledAt: time.Now(),
				RevokedAt: time.Now().Add(-time.Hour)},
		}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))
	payload, _ := json.Marshal(quicListFactorsRequest{Subject: "alice"})

	resp, err := srv.handleAuthFactorList(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	total, _ := out["total"].(float64)
	if int(total) != 2 {
		t.Errorf("expected total=2, got %v", out["total"])
	}
}

func TestHandleAuthFactorList_ServiceError(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.listFn = func(_ context.Context, _ string) ([]*authfactors.Enrollment, error) {
		return nil, errors.New("db error")
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))

	_, err := srv.handleAuthFactorList(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorRevoke
// ---------------------------------------------------------------------------

func TestHandleAuthFactorRevoke_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicRevokeFactorRequest{EnrollmentID: "e1"})

	_, err := srv.handleAuthFactorRevoke(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleAuthFactorRevoke_MissingEnrollmentID(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))
	payload, _ := json.Marshal(quicRevokeFactorRequest{})

	_, err := srv.handleAuthFactorRevoke(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for missing enrollment_id")
	}
}

func TestHandleAuthFactorRevoke_Success(t *testing.T) {
	called := false
	svc := newMockAuthFactorService()
	svc.revokeFn = func(_ context.Context, id string) error {
		called = true
		if id != "e1" {
			return errors.New("unexpected id: " + id)
		}
		return nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))
	payload, _ := json.Marshal(quicRevokeFactorRequest{EnrollmentID: "e1"})

	resp, err := srv.handleAuthFactorRevoke(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected RevokeFactor to be called")
	}
	var out map[string]string
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["status"] != "revoked" {
		t.Errorf("expected status 'revoked', got %q", out["status"])
	}
}

func TestHandleAuthFactorRevoke_ServiceError(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.revokeFn = func(_ context.Context, _ string) error {
		return errors.New("revoke failed")
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))
	payload, _ := json.Marshal(quicRevokeFactorRequest{EnrollmentID: "e1"})

	_, err := srv.handleAuthFactorRevoke(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// WithAuthFactorService option
// ---------------------------------------------------------------------------

func TestWithAuthFactorService_SetsField(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))
	if srv.authFactorSvc == nil {
		t.Error("expected authFactorSvc to be set")
	}
}
