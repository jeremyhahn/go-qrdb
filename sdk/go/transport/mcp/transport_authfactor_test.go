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

package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockAuthFactorSvc implements service.AuthFactorServicer for transport
// round-trip tests.
// ---------------------------------------------------------------------------

type mockAuthFactorSvc struct {
	mu sync.Mutex

	enrollErr    error
	challengeErr error
	verifyErr    error
	listErr      error
	revokeErr    error

	lastEnrollFactor  string
	lastEnrollSubject string
	lastChallengeID   string
	lastVerifyID      string
	lastListSubject   string
	lastRevokeID      string

	enrollResp    *authfactors.Enrollment
	challengeResp *authfactors.Challenge
	listResp      []*authfactors.Enrollment
}

var _ service.AuthFactorServicer = (*mockAuthFactorSvc)(nil)

func newMockAuthFactorSvc() *mockAuthFactorSvc {
	now := time.Now().UTC()
	return &mockAuthFactorSvc{
		enrollResp: &authfactors.Enrollment{
			EnrollmentID: "enr-1",
			UserID:       "alice",
			FactorName:   "piv",
			TenantID:     "t1",
			PublicKey:    []byte("pub"),
			Metadata:     map[string]string{"slot": "9a"},
			EnrolledAt:   now,
		},
		challengeResp: &authfactors.Challenge{
			ChallengeID:  "ch-1",
			EnrollmentID: "enr-1",
			Nonce:        []byte("random-nonce"),
			ExpiresAt:    now.Add(5 * time.Minute),
			MethodData:   map[string]any{"alg": "ecdsa"},
		},
		listResp: []*authfactors.Enrollment{
			{EnrollmentID: "enr-1", UserID: "alice", FactorName: "piv", TenantID: "t1", EnrolledAt: now},
		},
	}
}

func (m *mockAuthFactorSvc) EnrollFactor(_ context.Context, factorName string, subject string, _ authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastEnrollFactor = factorName
	m.lastEnrollSubject = subject
	if m.enrollErr != nil {
		return nil, m.enrollErr
	}
	return m.enrollResp, nil
}

func (m *mockAuthFactorSvc) ChallengeFactor(_ context.Context, enrollmentID string) (*authfactors.Challenge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastChallengeID = enrollmentID
	if m.challengeErr != nil {
		return nil, m.challengeErr
	}
	return m.challengeResp, nil
}

func (m *mockAuthFactorSvc) VerifyFactor(_ context.Context, _ string, challengeID string, _ *authfactors.Response) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastVerifyID = challengeID
	return m.verifyErr
}

func (m *mockAuthFactorSvc) ListFactors(_ context.Context, subject string) ([]*authfactors.Enrollment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastListSubject = subject
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResp, nil
}

func (m *mockAuthFactorSvc) RevokeFactor(_ context.Context, enrollmentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRevokeID = enrollmentID
	return m.revokeErr
}

func connectAuthFactorInProcess(t *testing.T, svc service.AuthFactorServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithAuthFactorService(svc),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}
	return tr, func() { _ = tr.Close() }
}

// ---------------------------------------------------------------------------
// EnrollFactor
// ---------------------------------------------------------------------------

func TestEnrollFactor_Success(t *testing.T) {
	svc := newMockAuthFactorSvc()
	tr, cleanup := connectAuthFactorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{
		FactorName:   "piv",
		Subject:      "alice",
		TenantID:     "t1",
		MethodParams: map[string]any{"slot": "9a"},
	})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if resp.EnrollmentID != "enr-1" {
		t.Errorf("enrollment id: %q", resp.EnrollmentID)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastEnrollFactor != "piv" {
		t.Errorf("factor: %q", svc.lastEnrollFactor)
	}
	if svc.lastEnrollSubject != "alice" {
		t.Errorf("subject: %q", svc.lastEnrollSubject)
	}
}

func TestEnrollFactor_ServiceError(t *testing.T) {
	svc := newMockAuthFactorSvc()
	svc.enrollErr = errors.New("invalid params")
	tr, cleanup := connectAuthFactorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{
		FactorName: "x", Subject: "y",
	}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// ChallengeFactor
// ---------------------------------------------------------------------------

func TestChallengeFactor_Success(t *testing.T) {
	svc := newMockAuthFactorSvc()
	tr, cleanup := connectAuthFactorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.ChallengeFactor(context.Background(), &transport.ChallengeFactorRequest{
		EnrollmentID: "enr-1",
	})
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	if resp.ChallengeID != "ch-1" {
		t.Errorf("challenge id: %q", resp.ChallengeID)
	}
}

func TestChallengeFactor_ServiceError(t *testing.T) {
	svc := newMockAuthFactorSvc()
	svc.challengeErr = errors.New("enrollment not found")
	tr, cleanup := connectAuthFactorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.ChallengeFactor(context.Background(), &transport.ChallengeFactorRequest{EnrollmentID: "missing"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// VerifyFactor
// ---------------------------------------------------------------------------

func TestVerifyFactor_Success(t *testing.T) {
	svc := newMockAuthFactorSvc()
	tr, cleanup := connectAuthFactorInProcess(t, svc)
	defer cleanup()

	err := tr.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{
		FactorName:  "piv",
		ChallengeID: "ch-1",
		Value:       []byte("signature"),
		Metadata:    map[string]string{"alg": "ecdsa"},
	})
	if err != nil {
		t.Fatalf("VerifyFactor: %v", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastVerifyID != "ch-1" {
		t.Errorf("challenge id: %q", svc.lastVerifyID)
	}
}

func TestVerifyFactor_ServiceError(t *testing.T) {
	svc := newMockAuthFactorSvc()
	svc.verifyErr = errors.New("invalid signature")
	tr, cleanup := connectAuthFactorInProcess(t, svc)
	defer cleanup()

	err := tr.VerifyFactor(context.Background(), &transport.VerifyFactorRequest{
		FactorName: "piv", ChallengeID: "ch-1", Value: []byte("x"),
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// ListFactors
// ---------------------------------------------------------------------------

func TestListFactors_Success(t *testing.T) {
	svc := newMockAuthFactorSvc()
	tr, cleanup := connectAuthFactorInProcess(t, svc)
	defer cleanup()

	resp, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{Subject: "alice"})
	if err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("total: %d", resp.Total)
	}
}

func TestListFactors_NilRequest(t *testing.T) {
	svc := newMockAuthFactorSvc()
	tr, cleanup := connectAuthFactorInProcess(t, svc)
	defer cleanup()

	_, err := tr.ListFactors(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListFactors(nil): %v", err)
	}
}

func TestListFactors_ServiceError(t *testing.T) {
	svc := newMockAuthFactorSvc()
	svc.listErr = errors.New("list failed")
	tr, cleanup := connectAuthFactorInProcess(t, svc)
	defer cleanup()

	if _, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// RevokeFactor
// ---------------------------------------------------------------------------

func TestRevokeFactor_Success(t *testing.T) {
	svc := newMockAuthFactorSvc()
	tr, cleanup := connectAuthFactorInProcess(t, svc)
	defer cleanup()

	err := tr.RevokeFactor(context.Background(), &transport.RevokeFactorRequest{EnrollmentID: "enr-1"})
	if err != nil {
		t.Fatalf("RevokeFactor: %v", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastRevokeID != "enr-1" {
		t.Errorf("revoke id: %q", svc.lastRevokeID)
	}
}

func TestRevokeFactor_ServiceError(t *testing.T) {
	svc := newMockAuthFactorSvc()
	svc.revokeErr = errors.New("not found")
	tr, cleanup := connectAuthFactorInProcess(t, svc)
	defer cleanup()

	if err := tr.RevokeFactor(context.Background(), &transport.RevokeFactorRequest{EnrollmentID: "x"}); err == nil {
		t.Fatal("expected error")
	}
}
