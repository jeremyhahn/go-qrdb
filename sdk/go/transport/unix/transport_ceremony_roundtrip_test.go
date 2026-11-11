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

// Round-trip tests for the Ceremony service over the Unix domain socket
// transport. Also covers Scan and Capabilities admin paths that were
// previously only exercised via closed-transport tests.

package unix

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockCeremonySvc — service.CeremonyServicer stub
// ---------------------------------------------------------------------------

type mockCeremonySvc struct {
	initFn       func(ctx context.Context, ca ceremony.CeremonyCA) (*service.CeremonyInitResult, error)
	claimShareFn func(ctx context.Context, username string) ([]byte, error)
	signCSRFn    func(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error)
	statusFn     func(ctx context.Context) (*service.CeremonyStatusInfo, error)
}

func (m *mockCeremonySvc) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, _ *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	if m.initFn != nil {
		return m.initFn(ctx, ca)
	}
	return &service.CeremonyInitResult{
		SPKIPin:    "spki-pin",
		CACertPEM:  []byte("ca-pem"),
		TLSCertPEM: []byte("tls-pem"),
		State:      "initialized",
		ShareCount: 3,
	}, nil
}

func (m *mockCeremonySvc) CeremonySignCSR(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error) {
	if m.signCSRFn != nil {
		return m.signCSRFn(ctx, username, soPin, csrPEM, role)
	}
	return &service.CeremonySignCSRResult{CertPEM: []byte("signed-cert-pem")}, nil
}

func (m *mockCeremonySvc) CeremonyStatus(ctx context.Context) (*service.CeremonyStatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.CeremonyStatusInfo{State: "ready", SPKIPin: "spki-pin"}, nil
}

var _ service.CeremonyServicer = (*mockCeremonySvc)(nil)

// ---------------------------------------------------------------------------
// Ceremony round-trips
// ---------------------------------------------------------------------------

// stubCeremonyCA is a non-nil CeremonyCA placeholder. CeremonyInitialize's
// handler requires a configured CA (fail-closed since #54's nil-guard) before
// delegating to the servicer; the mock servicer ignores the CA argument, so
// these stub methods are never invoked.
type stubCeremonyCA struct{}

func (stubCeremonyCA) SignCSR(context.Context, []byte, *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, nil
}
func (stubCeremonyCA) CACertificate(context.Context) (*x509.Certificate, error) { return nil, nil }
func (stubCeremonyCA) CABundle() ([]byte, error)                                { return nil, nil }
func (stubCeremonyCA) IssueCertificate(context.Context, *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return nil, nil
}

func TestUnixRT_CeremonyInitialize(t *testing.T) {
	svc := &mockCeremonySvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCeremonyService(svc), unixserver.WithCeremonyCA(stubCeremonyCA{}))
	defer cleanup()

	resp, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{
		SOPin:     "1234",
		UserPin:   "5678",
		Threshold: 2,
		Officers: []transport.CeremonyOfficerRequest{
			{Username: "officer1", CSRPEM: "csr-pem-1"},
		},
		Hostname: "qrdb.local",
		SANs:     []string{"qrdb.local"},
	})
	if err != nil {
		t.Fatalf("CeremonyInitialize: %v", err)
	}
	if resp.SPKIPin != "spki-pin" {
		t.Errorf("got SPKIPin=%q", resp.SPKIPin)
	}
}

func TestUnixRT_CeremonyStatus(t *testing.T) {
	svc := &mockCeremonySvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCeremonyService(svc))
	defer cleanup()

	resp, err := tr.CeremonyStatus(context.Background())
	if err != nil {
		t.Fatalf("CeremonyStatus: %v", err)
	}
	if resp.State != "ready" {
		t.Errorf("got State=%q, want ready", resp.State)
	}
}

func TestUnixRT_CeremonySignCSR(t *testing.T) {
	svc := &mockCeremonySvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithCeremonyService(svc))
	defer cleanup()

	resp, err := tr.CeremonySignCSR(context.Background(), &transport.SignCSRInitRequest{
		Username: "officer1",
		SOPin:    "1234",
		CSRPEM:   "csr-pem",
		Role:     "officer",
	})
	if err != nil {
		t.Fatalf("CeremonySignCSR: %v", err)
	}
	if resp.CertPEM != "signed-cert-pem" {
		t.Errorf("got CertPEM=%q", resp.CertPEM)
	}
}

func TestUnixRT_CeremonyInitialize_ServiceError(t *testing.T) {
	svc := &mockCeremonySvc{
		initFn: func(_ context.Context, _ ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
			return nil, errors.New("ceremony init failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCeremonyService(svc))
	defer cleanup()

	_, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{})
	if err == nil {
		t.Fatal("expected error from server")
	}
}

// ---------------------------------------------------------------------------
// Admin: Scan, Capabilities — paths that weren't covered by the existing
// PutGetDelete tests.
// ---------------------------------------------------------------------------

func TestUnixRT_Scan(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	if err := tr.Put(ctx, "app:1", []byte("v1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := tr.Put(ctx, "app:2", []byte("v2")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := tr.Scan(ctx, "app:")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d, want 2 entries", len(got))
	}
	if string(got["app:1"]) != "v1" {
		t.Errorf("got app:1=%q, want v1", got["app:1"])
	}
}

func TestUnixRT_Capabilities(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := tr.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !resp.KV || !resp.MVCC || !resp.Transaction || !resp.Batch || !resp.Watch || !resp.Lease || !resp.Index {
		t.Errorf("expected all capabilities=true, got %+v", resp)
	}
}

func TestUnixRT_Exists(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	if err := tr.Put(ctx, "present", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	exists, err := tr.Exists(ctx, "present")
	if err != nil {
		t.Fatalf("Exists(present): %v", err)
	}
	if !exists {
		t.Error("expected Exists=true for present key")
	}

	exists, err = tr.Exists(ctx, "missing")
	if err != nil {
		t.Fatalf("Exists(missing): %v", err)
	}
	if exists {
		t.Error("expected Exists=false for missing key")
	}
}

func TestUnixRT_ConnState_AfterConnect(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	state := tr.ConnState()
	// A connected gRPC client should not report Shutdown.
	if state.String() == "SHUTDOWN" {
		t.Errorf("expected non-shutdown state, got %s", state)
	}
}
