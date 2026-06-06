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
)

// ---------------------------------------------------------------------------
// mockAuditLogger
// ---------------------------------------------------------------------------

type mockAuditLogger struct {
	queryFn func(ctx context.Context, f audit.Filter) (*audit.Page, error)
}

func (m *mockAuditLogger) Emit(_ context.Context, _ audit.AuditEvent) {}

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
	result, err := handler(context.Background(), req)
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
}
