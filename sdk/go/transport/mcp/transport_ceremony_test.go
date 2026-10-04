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

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
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

// ---------------------------------------------------------------------------
// mockCeremonyCA implements ceremony.CeremonyCA for transport tests.
// ---------------------------------------------------------------------------

type mockCeremonyCA struct{}

func (m *mockCeremonyCA) SignCSR(_ context.Context, _ []byte, _ *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, nil
}

func (m *mockCeremonyCA) CACertificate(context.Context) (*x509.Certificate, error) { return nil, nil }
func (m *mockCeremonyCA) CABundle() ([]byte, error)                                { return []byte("ca"), nil }
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
