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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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

func newMockAuditLogger() *mockAuditLogger { return &mockAuditLogger{} }

// mockVerifyingAuditLogger extends mockAuditLogger with VerifyChainSet, so
// it satisfies audit.Verifier for handleAuditVerify tests. mockAuditLogger
// itself deliberately does NOT implement Verifier, so it doubles for a
// backend with no hash chain (RingBuffer, NoopLogger, SlogSink).
type mockVerifyingAuditLogger struct {
	mockAuditLogger
	verifyFn func(ctx context.Context) (*audit.ChainVerification, error)
}

func (m *mockVerifyingAuditLogger) VerifyChainSet(ctx context.Context) (*audit.ChainVerification, error) {
	return m.verifyFn(ctx)
}

var _ audit.Verifier = (*mockVerifyingAuditLogger)(nil)

// ---------------------------------------------------------------------------
// handleAuditQuery – success
// ---------------------------------------------------------------------------

func TestHandleAuditQuery_Success(t *testing.T) {
	logger := newMockAuditLogger()
	handler := handleAuditQuery(logger)

	req := buildCallToolRequest(t, "qrdb_audit_query", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}

	text := resultText(t, result)
	var page audit.Page
	if err := json.Unmarshal([]byte(text), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if page.Total != 1 {
		t.Errorf("expected Total=1, got %d", page.Total)
	}
	if len(page.Events) == 0 || page.Events[0].Actor != "alice" {
		t.Errorf("unexpected events: %+v", page.Events)
	}
}

// ---------------------------------------------------------------------------
// handleAuditQuery – with filter fields (actor, resource, action, outcome, tenant)
// ---------------------------------------------------------------------------

func TestHandleAuditQuery_WithFilterFields(t *testing.T) {
	var capturedFilter audit.Filter
	logger := &mockAuditLogger{
		queryFn: func(_ context.Context, f audit.Filter) (*audit.Page, error) {
			capturedFilter = f
			return &audit.Page{Events: []audit.AuditEvent{}, Total: 0}, nil
		},
	}
	handler := handleAuditQuery(logger)

	req := buildCallToolRequest(t, "qrdb_audit_query", map[string]any{
		"actor":     "bob",
		"resource":  "barrier",
		"action":    "seal",
		"outcome":   "success",
		"tenant_id": "tenant-1",
		"limit":     "50",
		"cursor":    "abc",
	})
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "bob", TenantID: "tenant-1"})
	result, err := handler(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}
	if capturedFilter.Actor != "bob" {
		t.Errorf("expected actor 'bob', got %q", capturedFilter.Actor)
	}
	if capturedFilter.Resource != "barrier" {
		t.Errorf("expected resource 'barrier', got %q", capturedFilter.Resource)
	}
	if capturedFilter.Action != "seal" {
		t.Errorf("expected action 'seal', got %q", capturedFilter.Action)
	}
	if capturedFilter.TenantID != "tenant-1" {
		t.Errorf("expected tenant_id 'tenant-1', got %q", capturedFilter.TenantID)
	}
	if capturedFilter.Limit != 50 {
		t.Errorf("expected limit 50, got %d", capturedFilter.Limit)
	}
}

// TestHandleAuditQuery_ForeignTenantIsRefused: a tenant_id other than the
// caller's own is refused before the logger is reached, and an absent one is
// filled in with the caller's tenant rather than read as "every tenant".
func TestHandleAuditQuery_ForeignTenantIsRefused(t *testing.T) {
	var queried []audit.Filter
	logger := &mockAuditLogger{
		queryFn: func(_ context.Context, f audit.Filter) (*audit.Page, error) {
			queried = append(queried, f)
			return &audit.Page{Events: []audit.AuditEvent{}}, nil
		},
	}
	handler := handleAuditQuery(logger)
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "bob", TenantID: "acme"})

	_, err := handler(ctx, buildCallToolRequest(t, "qrdb_audit_query", map[string]any{"tenant_id": "globex"}))
	var denied *tenant.CrossTenantDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v; want *tenant.CrossTenantDeniedError", err)
	}
	if len(queried) != 0 {
		t.Fatalf("a refused query reached the logger with %+v", queried)
	}

	if _, err := handler(ctx, buildCallToolRequest(t, "qrdb_audit_query", map[string]any{})); err != nil {
		t.Fatalf("unfiltered query: %v", err)
	}
	if len(queried) != 1 || queried[0].TenantID != "acme" || queried[0].Untenanted {
		t.Fatalf("unfiltered query reached the logger as %+v; want it confined to acme", queried)
	}
}

// ---------------------------------------------------------------------------
// handleAuditQuery – valid from/to timestamps
// ---------------------------------------------------------------------------

func TestHandleAuditQuery_WithFromTo(t *testing.T) {
	logger := newMockAuditLogger()
	handler := handleAuditQuery(logger)

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	to := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)

	req := buildCallToolRequest(t, "qrdb_audit_query", map[string]any{
		"from": from,
		"to":   to,
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}
}

// ---------------------------------------------------------------------------
// handleAuditQuery – invalid from timestamp
// ---------------------------------------------------------------------------

func TestHandleAuditQuery_InvalidFromTimestamp(t *testing.T) {
	logger := newMockAuditLogger()
	handler := handleAuditQuery(logger)

	req := buildCallToolRequest(t, "qrdb_audit_query", map[string]any{
		"from": "not-a-timestamp",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for invalid from timestamp")
	}
}

// ---------------------------------------------------------------------------
// handleAuditQuery – invalid to timestamp
// ---------------------------------------------------------------------------

func TestHandleAuditQuery_InvalidToTimestamp(t *testing.T) {
	logger := newMockAuditLogger()
	handler := handleAuditQuery(logger)

	req := buildCallToolRequest(t, "qrdb_audit_query", map[string]any{
		"to": "bad-time",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for invalid to timestamp")
	}
}

// ---------------------------------------------------------------------------
// handleAuditQuery – invalid limit
// ---------------------------------------------------------------------------

func TestHandleAuditQuery_InvalidLimit(t *testing.T) {
	logger := newMockAuditLogger()
	handler := handleAuditQuery(logger)

	req := buildCallToolRequest(t, "qrdb_audit_query", map[string]any{
		"limit": "not-a-number",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for invalid limit")
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
	handler := handleAuditQuery(logger)

	req := buildCallToolRequest(t, "qrdb_audit_query", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
	if te.Tool != "qrdb_audit_query" {
		t.Errorf("expected tool 'qrdb_audit_query', got %q", te.Tool)
	}
}

// ---------------------------------------------------------------------------
// auditTools registration
// ---------------------------------------------------------------------------

func TestWithAuditLogger_ToolsRegistered(t *testing.T) {
	logger := newMockAuditLogger()
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithAuditLogger(logger))

	if s.auditLogger == nil {
		t.Fatal("expected auditLogger to be set on server")
	}

	tools := s.MCPServer().ListTools()
	if _, ok := tools["qrdb_audit_query"]; !ok {
		t.Error("tool qrdb_audit_query not registered")
	}
	if _, ok := tools["qrdb_audit_verify"]; !ok {
		t.Error("tool qrdb_audit_verify not registered")
	}
}

// ---------------------------------------------------------------------------
// handleAuditVerify
// ---------------------------------------------------------------------------

func TestHandleAuditVerify_UnsupportedBackend(t *testing.T) {
	// mockAuditLogger does not implement audit.Verifier.
	handler := handleAuditVerify(newMockAuditLogger())

	req := buildCallToolRequest(t, "qrdb_audit_verify", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError for a backend with no hash chain")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
	var unsupported *audit.VerificationUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected wrapped *audit.VerificationUnsupportedError, got %v", err)
	}
}

func TestHandleAuditVerify_Verified(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 2, Records: 10, Genesis: true}, nil
		},
	}
	handler := handleAuditVerify(logger)

	req := buildCallToolRequest(t, "qrdb_audit_verify", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}

	text := resultText(t, result)
	var report audit.VerificationReport
	if err := json.Unmarshal([]byte(text), &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !report.Verified {
		t.Error("expected Verified=true")
	}
	if report.Files != 2 || report.Records != 10 {
		t.Errorf("unexpected report: %+v", report)
	}
}

// TestHandleAuditVerify_ChainBroken asserts a tamper verdict is reported as
// a successful tool result with Verified=false, NOT a tool error.
func TestHandleAuditVerify_ChainBroken(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 1, Records: 3},
				&audit.ChainBrokenError{Path: "audit-events.ndjson", Index: 1, Expected: "aa", Got: "bb"}
		},
	}
	handler := handleAuditVerify(logger)

	req := buildCallToolRequest(t, "qrdb_audit_verify", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("expected a successful result carrying the verdict, got error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError for a tamper verdict: %v", result.Content)
	}

	text := resultText(t, result)
	var report audit.VerificationReport
	if err := json.Unmarshal([]byte(text), &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if report.Verified {
		t.Error("expected Verified=false for a broken chain")
	}
	if report.Reason == "" {
		t.Error("expected a non-empty Reason")
	}
}

func TestHandleAuditVerify_ConcurrentRotation(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return nil, &audit.ConcurrentRotationError{Path: "audit-events.ndjson", Attempts: 5}
		},
	}
	handler := handleAuditVerify(logger)

	req := buildCallToolRequest(t, "qrdb_audit_verify", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError for a concurrent-rotation race")
	}
	var crErr *audit.ConcurrentRotationError
	if !errors.As(err, &crErr) {
		t.Fatalf("expected wrapped *audit.ConcurrentRotationError, got %v", err)
	}
}
