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
	"crypto/x509"
	"errors"
	"sync"
	"testing"
	"time"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockCeremonySvc implements service.CeremonyServicer for transport tests.
// ---------------------------------------------------------------------------

type mockCeremonySvc struct {
	mu sync.Mutex

	initErr           error
	beginCertErr      error
	completeCertErr   error
	claimShareErr     error
	signCSRErr        error
	statusErr         error
	claimShareBegErr  error
	claimShareComErr  error
	submitUnsealErr   error
	enrollExternalErr error

	lastUsername string
}

var _ service.CeremonyServicer = (*mockCeremonySvc)(nil)

func newMockCeremonySvc() *mockCeremonySvc {
	return &mockCeremonySvc{}
}

func (m *mockCeremonySvc) CeremonyInitialize(_ context.Context, _ ceremony.CeremonyCA, _ *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	if m.initErr != nil {
		return nil, m.initErr
	}
	return &service.CeremonyInitResult{SPKIPin: "pin123", State: "initialized", ShareCount: 3}, nil
}

func (m *mockCeremonySvc) CeremonyBeginClaimCert(_ context.Context, username string) (*service.CeremonyChallenge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastUsername = username
	if m.beginCertErr != nil {
		return nil, m.beginCertErr
	}
	return &service.CeremonyChallenge{
		NonceHex:  "deadbeef",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}, nil
}

func (m *mockCeremonySvc) CeremonyCompleteClaimCert(_ context.Context, username, _ string, _ []byte) (*service.CeremonyClaimCertResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastUsername = username
	if m.completeCertErr != nil {
		return nil, m.completeCertErr
	}
	return &service.CeremonyClaimCertResult{
		CertPEM:   []byte("cert"),
		CACertPEM: []byte("ca"),
	}, nil
}

func (m *mockCeremonySvc) CeremonyBeginClaimShare(_ context.Context, username, _ string) (*service.CeremonyChallenge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastUsername = username
	if m.claimShareErr != nil {
		return nil, m.claimShareErr
	}
	return &service.CeremonyChallenge{NonceHex: "abcd", ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (m *mockCeremonySvc) CeremonyCompleteClaimShare(_ context.Context, username, _ string, _ []byte, _ string) (*service.CeremonyShareClaimResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastUsername = username
	if m.claimShareErr != nil {
		return nil, m.claimShareErr
	}
	return &service.CeremonyShareClaimResult{ShareIndex: 1, ShareBytes: []byte("share-data")}, nil
}

func (m *mockCeremonySvc) CeremonySignCSR(_ context.Context, username, _, _, _ string) (*service.CeremonySignCSRResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastUsername = username
	if m.signCSRErr != nil {
		return nil, m.signCSRErr
	}
	return &service.CeremonySignCSRResult{CertPEM: []byte("signed-cert")}, nil
}

func (m *mockCeremonySvc) CeremonyStatus(_ context.Context) (*service.CeremonyStatusInfo, error) {
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	return &service.CeremonyStatusInfo{State: "initialized", SPKIPin: "pin123"}, nil
}

func (m *mockCeremonySvc) CeremonyClaimShareBegin(_ context.Context, _ string) (*service.CeremonyCustodianChallenge, error) {
	if m.claimShareBegErr != nil {
		return nil, m.claimShareBegErr
	}
	return &service.CeremonyCustodianChallenge{Nonce: []byte("nonce")}, nil
}

func (m *mockCeremonySvc) CeremonyClaimShareComplete(_ context.Context, _, _ string, _ []byte) (*custodian.Custodian, error) {
	if m.claimShareComErr != nil {
		return nil, m.claimShareComErr
	}
	return &custodian.Custodian{}, nil
}

func (m *mockCeremonySvc) CeremonySubmitUnsealShare(_ context.Context, _, _ string, _, _ []byte) (*service.CeremonyUnsealShareResult, error) {
	if m.submitUnsealErr != nil {
		return nil, m.submitUnsealErr
	}
	return &service.CeremonyUnsealShareResult{Submitted: 1, Required: 3}, nil
}

func (m *mockCeremonySvc) CeremonyEnrollExternalOfficer(_ context.Context, _ *service.CeremonyEnrollExternalOfficerRequest) (*service.CeremonyEnrollExternalOfficerResponse, error) {
	if m.enrollExternalErr != nil {
		return nil, m.enrollExternalErr
	}
	return &service.CeremonyEnrollExternalOfficerResponse{SealedShare: "sealed-share"}, nil
}

// ---------------------------------------------------------------------------
// mockCeremonyCA implements ceremony.CeremonyCA for transport tests.
// ---------------------------------------------------------------------------

type mockCeremonyCA struct{}

func (m *mockCeremonyCA) SignCSR(_ context.Context, _ []byte, _ *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, nil
}

func (m *mockCeremonyCA) CACertificate() (*x509.Certificate, error) { return nil, nil }
func (m *mockCeremonyCA) CABundle() ([]byte, error)                 { return []byte("ca"), nil }
func (m *mockCeremonyCA) IssueCertificate(_ context.Context, _ *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return &ceremony.IssuedCert{CertPEM: []byte("cert")}, nil
}

func connectCeremonyInProcess(t *testing.T, svc service.CeremonyServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithCeremonyService(svc),
		mcpapi.WithCeremonyCA(&mockCeremonyCA{}),
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
// CeremonyStatus
// ---------------------------------------------------------------------------

func TestCeremonyStatus_Success(t *testing.T) {
	svc := newMockCeremonySvc()
	tr, cleanup := connectCeremonyInProcess(t, svc)
	defer cleanup()

	resp, err := tr.CeremonyStatus(context.Background())
	if err != nil {
		t.Fatalf("CeremonyStatus: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil status")
	}
}

func TestCeremonyStatus_ServiceError(t *testing.T) {
	svc := newMockCeremonySvc()
	svc.statusErr = errors.New("unavailable")
	tr, cleanup := connectCeremonyInProcess(t, svc)
	defer cleanup()

	if _, err := tr.CeremonyStatus(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// CeremonyClaimCertBegin
// ---------------------------------------------------------------------------

func TestCeremonyClaimCertBegin_Success(t *testing.T) {
	svc := newMockCeremonySvc()
	tr, cleanup := connectCeremonyInProcess(t, svc)
	defer cleanup()

	resp, err := tr.CeremonyClaimCertBegin(context.Background(), &transport.ClaimCertBeginRequest{
		Username: "officer1",
	})
	if err != nil {
		t.Fatalf("CeremonyClaimCertBegin: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestCeremonyClaimCertBegin_ServiceError(t *testing.T) {
	svc := newMockCeremonySvc()
	svc.beginCertErr = errors.New("not found")
	tr, cleanup := connectCeremonyInProcess(t, svc)
	defer cleanup()

	if _, err := tr.CeremonyClaimCertBegin(context.Background(), &transport.ClaimCertBeginRequest{Username: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// CeremonyClaimCertComplete
// ---------------------------------------------------------------------------

func TestCeremonyClaimCertComplete_Success(t *testing.T) {
	svc := newMockCeremonySvc()
	tr, cleanup := connectCeremonyInProcess(t, svc)
	defer cleanup()

	resp, err := tr.CeremonyClaimCertComplete(context.Background(), &transport.ClaimCertCompleteRequest{
		Username:  "officer1",
		NonceHex:  "deadbeef",
		Signature: "c2ln",
	})
	if err != nil {
		t.Fatalf("CeremonyClaimCertComplete: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestCeremonyClaimCertComplete_ServiceError(t *testing.T) {
	svc := newMockCeremonySvc()
	svc.completeCertErr = errors.New("invalid signature")
	tr, cleanup := connectCeremonyInProcess(t, svc)
	defer cleanup()

	if _, err := tr.CeremonyClaimCertComplete(context.Background(), &transport.ClaimCertCompleteRequest{
		Username: "x", NonceHex: "dead", Signature: "eA==",
	}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// CeremonyBeginClaimShare / CeremonyCompleteClaimShare
// ---------------------------------------------------------------------------

// TestCeremonyClaimShare_RequiresClientCert proves the in-process MCP session
// carries no mTLS peer certificate, so the server-side tool refuses the claim
// and no share material comes back.
func TestCeremonyClaimShare_RequiresClientCert(t *testing.T) {
	svc := newMockCeremonySvc()
	tr, cleanup := connectCeremonyInProcess(t, svc)
	defer cleanup()

	if _, err := tr.CeremonyBeginClaimShare(context.Background(), &transport.BeginClaimShareRequest{
		Username: "officer1",
	}); err == nil {
		t.Fatal("expected refusal without a client certificate")
	}

	resp, err := tr.CeremonyCompleteClaimShare(context.Background(), &transport.CompleteClaimShareRequest{
		Username: "officer1", NonceHex: "abcd", Signature: []byte("sig"),
	})
	if err == nil {
		t.Fatal("expected refusal without a client certificate")
	}
	if resp != nil {
		t.Fatalf("share material leaked on refusal: %#v", resp)
	}
}

func TestCeremonyClaimShare_ServiceError(t *testing.T) {
	svc := newMockCeremonySvc()
	svc.claimShareErr = errors.New("not enrolled")
	tr, cleanup := connectCeremonyInProcess(t, svc)
	defer cleanup()

	if _, err := tr.CeremonyBeginClaimShare(context.Background(), &transport.BeginClaimShareRequest{Username: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// CeremonySignCSR
// ---------------------------------------------------------------------------

func TestCeremonySignCSR_Success(t *testing.T) {
	svc := newMockCeremonySvc()
	tr, cleanup := connectCeremonyInProcess(t, svc)
	defer cleanup()

	resp, err := tr.CeremonySignCSR(context.Background(), &transport.SignCSRInitRequest{
		Username: "officer1",
		SOPin:    "1234",
		CSRPEM:   "-----BEGIN CERTIFICATE REQUEST-----\nfake\n-----END CERTIFICATE REQUEST-----",
		Role:     "officer",
	})
	if err != nil {
		t.Fatalf("CeremonySignCSR: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestCeremonySignCSR_ServiceError(t *testing.T) {
	svc := newMockCeremonySvc()
	svc.signCSRErr = errors.New("bad CSR")
	tr, cleanup := connectCeremonyInProcess(t, svc)
	defer cleanup()

	if _, err := tr.CeremonySignCSR(context.Background(), &transport.SignCSRInitRequest{
		Username: "x", SOPin: "x", CSRPEM: "x", Role: "x",
	}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// CeremonyInitialize
// ---------------------------------------------------------------------------

func TestCeremonyInitialize_Success(t *testing.T) {
	svc := newMockCeremonySvc()
	tr, cleanup := connectCeremonyInProcess(t, svc)
	defer cleanup()

	resp, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{
		SOPin:                  "1234",
		UserPin:                "5678",
		Threshold:              2,
		CredentialSealStrategy: "passphrase",
		Hostname:               "qrdb.local",
		SANs:                   []string{"127.0.0.1"},
		Officers:               []transport.CeremonyOfficerRequest{{Username: "off1"}},
	})
	if err != nil {
		t.Fatalf("CeremonyInitialize: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestCeremonyInitialize_ServiceError(t *testing.T) {
	svc := newMockCeremonySvc()
	svc.initErr = errors.New("already initialized")
	tr, cleanup := connectCeremonyInProcess(t, svc)
	defer cleanup()

	if _, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{Threshold: 1}); err == nil {
		t.Fatal("expected error")
	}
}
