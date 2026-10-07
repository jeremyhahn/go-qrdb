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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// mockAuditLogger
// ---------------------------------------------------------------------------

type mockAuditLogger struct {
	queryFn func(ctx context.Context, f audit.Filter) (*audit.Page, error)
}

func (m *mockAuditLogger) Emit(_ context.Context, _ audit.AuditEvent) error { return nil }

func (m *mockAuditLogger) Query(ctx context.Context, f audit.Filter) (*audit.Page, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, f)
	}
	return &audit.Page{
		Events: []audit.AuditEvent{
			{Actor: "alice", Resource: "barrier", Action: "initialize", Outcome: "success", Timestamp: time.Now()},
		},
		Total: 1,
	}, nil
}

func (m *mockAuditLogger) Close() error { return nil }

var _ audit.AuditLogger = (*mockAuditLogger)(nil)

// ---------------------------------------------------------------------------
// handleAuditQuery – nil logger
// ---------------------------------------------------------------------------

func TestHandleAuditQuery_NilLogger(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	_, err := srv.handleAuditQuery(context.Background(), []byte(`{}`))
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
	if sncErr.Service != "audit" {
		t.Errorf("expected service 'audit', got %q", sncErr.Service)
	}
}

// ---------------------------------------------------------------------------
// handleAuditQuery – success with empty payload
// ---------------------------------------------------------------------------

func TestHandleAuditQuery_Success_EmptyPayload(t *testing.T) {
	logger := &mockAuditLogger{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuditLogger(logger))

	resp, err := srv.handleAuditQuery(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var page audit.Page
	if err := json.Unmarshal(resp, &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if page.Total != 1 {
		t.Errorf("expected Total=1, got %d", page.Total)
	}
	if len(page.Events) == 0 {
		t.Error("expected at least one event")
	}
}

// ---------------------------------------------------------------------------
// handleAuditQuery – success with JSON filter
// ---------------------------------------------------------------------------

func TestHandleAuditQuery_Success_WithFilter(t *testing.T) {
	var capturedFilter audit.Filter
	logger := &mockAuditLogger{
		queryFn: func(_ context.Context, f audit.Filter) (*audit.Page, error) {
			capturedFilter = f
			return &audit.Page{Events: []audit.AuditEvent{}, Total: 0}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuditLogger(logger))

	filter := audit.Filter{Actor: "bob", Resource: "user:carol", Limit: 25}
	payload, _ := json.Marshal(filter)

	resp, err := srv.handleAuditQuery(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var page audit.Page
	if err := json.Unmarshal(resp, &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if capturedFilter.Actor != "bob" {
		t.Errorf("expected actor 'bob', got %q", capturedFilter.Actor)
	}
	if capturedFilter.Resource != "user:carol" {
		t.Errorf("expected resource 'user:carol', got %q", capturedFilter.Resource)
	}
	if capturedFilter.Limit != 25 {
		t.Errorf("expected limit 25, got %d", capturedFilter.Limit)
	}
}

// ---------------------------------------------------------------------------
// handleAuditQuery – invalid filter JSON
// ---------------------------------------------------------------------------

func TestHandleAuditQuery_InvalidFilterJSON(t *testing.T) {
	logger := &mockAuditLogger{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuditLogger(logger))

	_, err := srv.handleAuditQuery(context.Background(), []byte(`{not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleAuditQuery – service error
// ---------------------------------------------------------------------------

func TestHandleAuditQuery_ServiceError(t *testing.T) {
	logger := &mockAuditLogger{
		queryFn: func(_ context.Context, _ audit.Filter) (*audit.Page, error) {
			return nil, errors.New("storage failure")
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuditLogger(logger))

	_, err := srv.handleAuditQuery(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error from service")
	}
	if err.Error() != "storage failure" {
		t.Errorf("expected 'storage failure', got %q", err.Error())
	}
}

// mockVerifyingAuditLogger extends mockAuditLogger with VerifyChainSet, so
// it satisfies audit.Verifier for handleAuditVerifyChain tests.
// mockAuditLogger itself deliberately does NOT implement Verifier, so it
// doubles for a backend with no hash chain (RingBuffer, NoopLogger,
// SlogSink).
type mockVerifyingAuditLogger struct {
	mockAuditLogger
	verifyFn func(ctx context.Context) (*audit.ChainVerification, error)
}

func (m *mockVerifyingAuditLogger) VerifyChainSet(ctx context.Context) (*audit.ChainVerification, error) {
	return m.verifyFn(ctx)
}

var _ audit.Verifier = (*mockVerifyingAuditLogger)(nil)

// ---------------------------------------------------------------------------
// handleAuditVerifyChain – nil logger
// ---------------------------------------------------------------------------

func TestHandleAuditVerifyChain_NilLogger(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	_, err := srv.handleAuditVerifyChain(context.Background(), nil)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
	if sncErr.Service != "audit" {
		t.Errorf("expected service 'audit', got %q", sncErr.Service)
	}
}

// ---------------------------------------------------------------------------
// handleAuditVerifyChain – unsupported backend
// ---------------------------------------------------------------------------

func TestHandleAuditVerifyChain_UnsupportedBackend(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuditLogger(&mockAuditLogger{}))

	_, err := srv.handleAuditVerifyChain(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for a backend with no hash chain")
	}
	var unsupported *audit.VerificationUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected *audit.VerificationUnsupportedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleAuditVerifyChain – verified
// ---------------------------------------------------------------------------

func TestHandleAuditVerifyChain_Verified(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 2, Records: 10, Genesis: true}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuditLogger(logger))

	resp, err := srv.handleAuditVerifyChain(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var report audit.VerificationReport
	if err := json.Unmarshal(resp, &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !report.Verified {
		t.Error("expected Verified=true")
	}
	if report.Files != 2 || report.Records != 10 {
		t.Errorf("unexpected report: %+v", report)
	}
}

// TestHandleAuditVerifyChain_ChainBroken asserts a tamper verdict is
// reported as a successful response with Verified=false, NOT an error.
func TestHandleAuditVerifyChain_ChainBroken(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 1, Records: 3},
				&audit.ChainBrokenError{Path: "audit-events.ndjson", Index: 1, Expected: "aa", Got: "bb"}
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuditLogger(logger))

	resp, err := srv.handleAuditVerifyChain(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected a successful response carrying the verdict, got error: %v", err)
	}
	var report audit.VerificationReport
	if err := json.Unmarshal(resp, &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if report.Verified {
		t.Error("expected Verified=false for a broken chain")
	}
	if report.Reason == "" {
		t.Error("expected a non-empty Reason")
	}
}

func TestHandleAuditVerifyChain_ConcurrentRotation(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return nil, &audit.ConcurrentRotationError{Path: "audit-events.ndjson", Attempts: 5}
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuditLogger(logger))

	_, err := srv.handleAuditVerifyChain(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for a concurrent-rotation race")
	}
	var crErr *audit.ConcurrentRotationError
	if !errors.As(err, &crErr) {
		t.Fatalf("expected *audit.ConcurrentRotationError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// WithAuditLogger option
// ---------------------------------------------------------------------------

func TestWithAuditLogger_SetsField(t *testing.T) {
	logger := &mockAuditLogger{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuditLogger(logger))
	if srv.auditLogger == nil {
		t.Error("expected auditLogger to be set")
	}
}

// ---------------------------------------------------------------------------
// replicated audit chain: handleAuditQuery(replicated=true) and
// handleAuditVerifyReplicatedChain
// ---------------------------------------------------------------------------

// mockReplicatedInner implements audit.AuditLogger and
// service.TenantChainVerifier, standing in for a *service.ReplicatedLogger.
type mockReplicatedInner struct {
	mockAuditLogger
	verifyReport *audit.ChainContinuityReport
	verifyErr    error
}

func (m *mockReplicatedInner) VerifyTenantChain(context.Context, string) (*audit.ChainContinuityReport, error) {
	return m.verifyReport, m.verifyErr
}

type stubOptInEntitlement struct{ enabled map[string]bool }

func (s *stubOptInEntitlement) ReplicatedAuditEnabled(_ context.Context, tenantID string) (bool, error) {
	return s.enabled[tenantID], nil
}

func TestHandleAuditQuery_Replicated_NilNotConfigured(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuditLogger(&mockAuditLogger{}))

	_, err := srv.handleAuditQuery(context.Background(), []byte(`{"replicated":true}`))
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T (%v)", err, err)
	}
	if sncErr.Service != "replicated audit" {
		t.Errorf("service = %q, want %q", sncErr.Service, "replicated audit")
	}
}

func TestHandleAuditQuery_Replicated_ReadsReplicatedLoggerNotNodeLocal(t *testing.T) {
	nodeLocal := &mockAuditLogger{queryFn: func(context.Context, audit.Filter) (*audit.Page, error) {
		t.Fatal("node-local logger must not be queried when replicated=true")
		return nil, nil
	}}
	inner := &mockReplicatedInner{mockAuditLogger: mockAuditLogger{
		queryFn: func(_ context.Context, f audit.Filter) (*audit.Page, error) {
			return &audit.Page{Events: []audit.AuditEvent{{Actor: "op"}}, Total: -1}, nil
		},
	}}
	replicated := service.NewOptInAuditLogger(inner, &stubOptInEntitlement{enabled: map[string]bool{"acme": true}}, false)
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuditLogger(nodeLocal), WithReplicatedAuditLogger(replicated))

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "a", TenantID: "acme"})
	resp, err := srv.handleAuditQuery(ctx, []byte(`{"replicated":true,"tenant_id":"acme"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var page audit.Page
	if err := json.Unmarshal(resp, &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(page.Events) != 1 || page.Events[0].Actor != "op" {
		t.Fatalf("got %+v, want the replicated logger's event", page.Events)
	}
}

func TestHandleAuditVerifyReplicatedChain_NilNotConfigured(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleAuditVerifyReplicatedChain(context.Background(), nil)
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T (%v)", err, err)
	}
}

func TestHandleAuditVerifyReplicatedChain_Success(t *testing.T) {
	inner := &mockReplicatedInner{verifyReport: &audit.ChainContinuityReport{Verified: true, Records: 3, Head: 3, Low: 1, Genesis: true}}
	replicated := service.NewOptInAuditLogger(inner, &stubOptInEntitlement{enabled: map[string]bool{"acme": true}}, false)
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithReplicatedAuditLogger(replicated))

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "a", TenantID: "acme"})
	resp, err := srv.handleAuditVerifyReplicatedChain(ctx, []byte(`{"tenant_id":"acme"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var report audit.ChainContinuityReport
	if err := json.Unmarshal(resp, &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !report.Verified || report.Records != 3 {
		t.Fatalf("got %+v", report)
	}
}

func TestHandleAuditVerifyReplicatedChain_NotEntitled(t *testing.T) {
	inner := &mockReplicatedInner{}
	replicated := service.NewOptInAuditLogger(inner, &stubOptInEntitlement{enabled: map[string]bool{}}, false)
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithReplicatedAuditLogger(replicated))

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "a", TenantID: "acme"})
	_, err := srv.handleAuditVerifyReplicatedChain(ctx, []byte(`{"tenant_id":"acme"}`))
	if err == nil {
		t.Fatal("expected an error for a tenant not entitled to the replicated chain")
	}
}
