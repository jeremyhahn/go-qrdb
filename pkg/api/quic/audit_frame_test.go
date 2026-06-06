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
