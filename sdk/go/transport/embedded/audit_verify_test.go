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

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// mockAuditLogger is a minimal audit.AuditLogger for embedded transport
// tests. It deliberately does NOT implement audit.Verifier, so it doubles
// for a backend with no hash chain (RingBuffer, NoopLogger, SlogSink).
type mockAuditLogger struct {
	emitErr  error
	queryFn  func(ctx context.Context, f audit.Filter) (*audit.Page, error)
	queryErr error
}

func (m *mockAuditLogger) Emit(_ context.Context, _ audit.AuditEvent) error { return m.emitErr }

func (m *mockAuditLogger) Query(ctx context.Context, f audit.Filter) (*audit.Page, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	if m.queryFn != nil {
		return m.queryFn(ctx, f)
	}
	return &audit.Page{Events: []audit.AuditEvent{}, Total: 0}, nil
}

func (m *mockAuditLogger) Close() error { return nil }

var _ audit.AuditLogger = (*mockAuditLogger)(nil)

// mockVerifyingAuditLogger extends mockAuditLogger with VerifyChainSet, so
// it satisfies audit.Verifier.
type mockVerifyingAuditLogger struct {
	mockAuditLogger
	verifyFn func(ctx context.Context) (*audit.ChainVerification, error)
}

func (m *mockVerifyingAuditLogger) VerifyChainSet(ctx context.Context) (*audit.ChainVerification, error) {
	return m.verifyFn(ctx)
}

var _ audit.Verifier = (*mockVerifyingAuditLogger)(nil)

func TestEmbeddedAuditClient_NilLogger(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if tr.AuditClient() != nil {
		t.Fatal("expected nil AuditClient when no logger is injected")
	}
}

func TestEmbeddedAuditClient_QueryAudit_Success(t *testing.T) {
	logger := &mockAuditLogger{
		queryFn: func(_ context.Context, f audit.Filter) (*audit.Page, error) {
			return &audit.Page{
				Events: []audit.AuditEvent{{Actor: "alice", TenantID: f.TenantID}},
				Total:  1,
			}, nil
		},
	}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithAuditLogger(logger))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ac := tr.AuditClient()
	if ac == nil {
		t.Fatal("expected non-nil AuditClient")
	}
	// An audit query is confined to the caller's own tenant (v0.28.1), so
	// the caller here is tenant-1 querying its own events.
	page, err := ac.QueryAudit(tenant.WithTenantID(context.Background(), "tenant-1"), transport.AuditFilter{TenantID: "tenant-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if page.Total != 1 || len(page.Events) != 1 || page.Events[0].TenantID != "tenant-1" {
		t.Errorf("unexpected page: %+v", page)
	}
}

func TestEmbeddedAuditClient_EmitAudit_PropagatesError(t *testing.T) {
	wantErr := errors.New("write failed")
	logger := &mockAuditLogger{emitErr: wantErr}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithAuditLogger(logger))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.AuditClient().EmitAudit(context.Background(), transport.AuditEvent{}); !errors.Is(err, wantErr) {
		t.Errorf("expected propagated error %v, got %v", wantErr, err)
	}
}

// ---------------------------------------------------------------------------
// VerifyChain (go-qrdb#453)
// ---------------------------------------------------------------------------

func TestEmbeddedAuditClient_VerifyChain_MissingLogger(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// AuditClient() returns nil without a logger, so exercise the client
	// type directly to cover the MissingAuditLoggerError branch.
	ac := &auditClient{transport: tr}
	_, err = ac.VerifyChain(context.Background())
	var mal *MissingAuditLoggerError
	if !errors.As(err, &mal) {
		t.Fatalf("expected *MissingAuditLoggerError, got %v", err)
	}
}

func TestEmbeddedAuditClient_VerifyChain_UnsupportedBackend(t *testing.T) {
	// mockAuditLogger does not implement audit.Verifier.
	tr, err := New(newMockKVService(), &mockAdminService{}, WithAuditLogger(&mockAuditLogger{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = tr.AuditClient().VerifyChain(context.Background())
	var nie *transport.NotImplementedError
	if !errors.As(err, &nie) {
		t.Fatalf("expected *transport.NotImplementedError, got %v", err)
	}
}

func TestEmbeddedAuditClient_VerifyChain_Verified(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 2, Records: 10, Genesis: true, Seed: "", Handoff: "cafe"}, nil
		},
	}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithAuditLogger(logger))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := tr.AuditClient().VerifyChain(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Verified {
		t.Error("expected Verified=true")
	}
	if result.Files != 2 || result.Records != 10 || result.Handoff != "cafe" {
		t.Errorf("unexpected result: %+v", result)
	}
}

// TestEmbeddedAuditClient_VerifyChain_ChainBroken asserts a tamper verdict
// is reported through the result, not returned as an error.
func TestEmbeddedAuditClient_VerifyChain_ChainBroken(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 1, Records: 3},
				&audit.ChainBrokenError{Path: "audit-events.ndjson", Index: 2, Expected: "aa", Got: "bb"}
		},
	}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithAuditLogger(logger))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := tr.AuditClient().VerifyChain(context.Background())
	if err != nil {
		t.Fatalf("expected a successful result carrying the verdict, got error: %v", err)
	}
	if result.Verified {
		t.Error("expected Verified=false for a broken chain")
	}
	if result.Reason == "" {
		t.Error("expected a non-empty Reason")
	}
}

func TestEmbeddedAuditClient_VerifyChain_ConcurrentRotation(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return nil, &audit.ConcurrentRotationError{Path: "audit-events.ndjson", Attempts: 5}
		},
	}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithAuditLogger(logger))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = tr.AuditClient().VerifyChain(context.Background())
	var crErr *audit.ConcurrentRotationError
	if !errors.As(err, &crErr) {
		t.Fatalf("expected *audit.ConcurrentRotationError, got %v", err)
	}
}

func TestEmbeddedAuditClient_VerifyChain_Closed(t *testing.T) {
	logger := &mockAuditLogger{}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithAuditLogger(logger))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = tr.AuditClient().VerifyChain(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *transport.ClosedError, got %v", err)
	}
}
