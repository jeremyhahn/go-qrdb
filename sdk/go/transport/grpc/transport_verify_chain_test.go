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

package grpc

import (
	"context"
	"errors"
	"net"
	"testing"

	googlegrpc "google.golang.org/grpc"

	"github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// fakeVerifyingAuditLogger is a minimal audit.AuditLogger + audit.Verifier
// for round-trip VerifyChain tests.
type fakeVerifyingAuditLogger struct {
	verifyFn func(ctx context.Context) (*audit.ChainVerification, error)
}

func (f *fakeVerifyingAuditLogger) Emit(_ context.Context, _ audit.AuditEvent) error { return nil }
func (f *fakeVerifyingAuditLogger) Query(_ context.Context, _ audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}
func (f *fakeVerifyingAuditLogger) Close() error { return nil }
func (f *fakeVerifyingAuditLogger) VerifyChainSet(ctx context.Context) (*audit.ChainVerification, error) {
	return f.verifyFn(ctx)
}

var (
	_ audit.AuditLogger = (*fakeVerifyingAuditLogger)(nil)
	_ audit.Verifier    = (*fakeVerifyingAuditLogger)(nil)
)

func startAuditVerifyServer(t *testing.T, auditLogger audit.AuditLogger) (string, func()) {
	t.Helper()
	srv := googlegrpc.NewServer()
	grpc.RegisterService(srv, nil, nil)
	if auditLogger != nil {
		grpc.RegisterAuditService(srv, auditLogger, nil)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

// fakeReplicatedInner implements audit.AuditLogger and
// service.TenantChainVerifier, standing in for a *service.ReplicatedLogger
// in round-trip tests.
type fakeReplicatedInner struct {
	fakeVerifyingAuditLogger
	report *audit.ChainContinuityReport
}

func (f *fakeReplicatedInner) VerifyTenantChain(context.Context, string) (*audit.ChainContinuityReport, error) {
	return f.report, nil
}

type fakeEntitlementLookup struct{ enabled map[string]bool }

func (f *fakeEntitlementLookup) ReplicatedAuditEnabled(_ context.Context, tenantID string) (bool, error) {
	return f.enabled[tenantID], nil
}

func startReplicatedAuditServer(t *testing.T, replicatedAudit *service.OptInAuditLogger) (string, func()) {
	t.Helper()
	srv := googlegrpc.NewServer()
	grpc.RegisterService(srv, nil, nil)
	grpc.RegisterAuditService(srv, nil, replicatedAudit)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

// TestGRPCVerifyReplicatedChain_RoundTrip verifies the client/server wire
// shapes agree. The round trip has no mTLS identity attached, so the
// caller resolves to the system tenant on both sides; the request names no
// tenant (confirming the caller's own) and platformEnabled=true entitles
// it, exercising success without needing to stand up identity propagation.
func TestGRPCVerifyReplicatedChain_RoundTrip(t *testing.T) {
	inner := &fakeReplicatedInner{report: &audit.ChainContinuityReport{Verified: true, Records: 5, Head: 5, Low: 1, Genesis: true}}
	replicated := service.NewOptInAuditLogger(inner, &fakeEntitlementLookup{}, true)
	addr, stop := startReplicatedAuditServer(t, replicated)
	defer stop()
	tr, cleanup := connectMiscTransport(t, addr)
	defer cleanup()

	result, err := tr.VerifyReplicatedChain(context.Background(), "")
	if err != nil {
		t.Fatalf("VerifyReplicatedChain: %v", err)
	}
	if !result.Verified || result.Records != 5 || result.Head != 5 || result.Low != 1 || !result.Genesis {
		t.Fatalf("result = %+v", result)
	}
}

func TestGRPCVerifyReplicatedChain_NotImplemented(t *testing.T) {
	addr, stop := startReplicatedAuditServer(t, nil)
	defer stop()
	tr, cleanup := connectMiscTransport(t, addr)
	defer cleanup()

	_, err := tr.VerifyReplicatedChain(context.Background(), "acme")
	var nie *transport.NotImplementedError
	if !errors.As(err, &nie) {
		t.Fatalf("expected *transport.NotImplementedError, got %v", err)
	}
}

func TestGRPCVerifyChain_RoundTrip(t *testing.T) {
	logger := &fakeVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 2, Records: 10, Genesis: true}, nil
		},
	}
	addr, stop := startAuditVerifyServer(t, logger)
	defer stop()
	tr, cleanup := connectMiscTransport(t, addr)
	defer cleanup()

	result, err := tr.VerifyChain(context.Background())
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !result.Verified || result.Files != 2 || result.Records != 10 {
		t.Fatalf("result = %+v", result)
	}
}

// TestGRPCVerifyChain_ChainBroken asserts a tamper verdict decodes as a
// successful result, not an error.
func TestGRPCVerifyChain_ChainBroken(t *testing.T) {
	logger := &fakeVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 1, Records: 3},
				&audit.ChainBrokenError{Path: "audit-events.ndjson", Index: 1, Expected: "aa", Got: "bb"}
		},
	}
	addr, stop := startAuditVerifyServer(t, logger)
	defer stop()
	tr, cleanup := connectMiscTransport(t, addr)
	defer cleanup()

	result, err := tr.VerifyChain(context.Background())
	if err != nil {
		t.Fatalf("expected a successful result carrying the verdict, got error: %v", err)
	}
	if result.Verified {
		t.Error("expected Verified=false")
	}
	if result.Reason == "" {
		t.Error("expected a non-empty Reason")
	}
}

func TestGRPCVerifyChain_NotImplemented(t *testing.T) {
	// No AuditService registered at all.
	addr, stop := startAuditVerifyServer(t, nil)
	defer stop()
	tr, cleanup := connectMiscTransport(t, addr)
	defer cleanup()

	_, err := tr.VerifyChain(context.Background())
	var nie *transport.NotImplementedError
	if !errors.As(err, &nie) {
		t.Fatalf("expected *transport.NotImplementedError, got %v", err)
	}
}
