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

package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

// ---------------------------------------------------------------------------
// mockAuditLogger is a minimal AuditLogger for unit tests.
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
			{
				Actor:     "alice",
				Resource:  "barrier",
				Action:    "initialize",
				Outcome:   "success",
				Timestamp: time.Now(),
			},
		},
		Total: 1,
	}, nil
}

func (m *mockAuditLogger) Close() error { return nil }

var _ audit.AuditLogger = (*mockAuditLogger)(nil)

// ---------------------------------------------------------------------------
// startAuditTestServer starts a bare grpc.Server with RegisterAuditService.
// ---------------------------------------------------------------------------

func startAuditTestServer(t *testing.T, logger audit.AuditLogger) (*grpc.ClientConn, func()) {
	t.Helper()
	srv := grpc.NewServer()
	RegisterAuditService(srv, logger)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		t.Fatalf("grpc.NewClient: %v", err)
	}
	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
	}
	return conn, cleanup
}

// ---------------------------------------------------------------------------
// auditServiceServer.query – nil logger
// ---------------------------------------------------------------------------

func TestAuditQuery_NilLogger(t *testing.T) {
	srv := &auditServiceServer{auditLogger: nil}
	req := wrapperspb.Bytes([]byte(`{}`))
	_, err := srv.query(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for nil auditLogger")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// auditServiceServer.query – success with empty filter
// ---------------------------------------------------------------------------

func TestAuditQuery_Success_EmptyFilter(t *testing.T) {
	logger := &mockAuditLogger{}
	srv := &auditServiceServer{auditLogger: logger}
	req := wrapperspb.Bytes(nil) // zero-length: skip unmarshal
	resp, err := srv.query(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	var page audit.Page
	if err := json.Unmarshal(resp.GetValue(), &page); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if page.Total != 1 {
		t.Errorf("expected Total=1, got %d", page.Total)
	}
}

// ---------------------------------------------------------------------------
// auditServiceServer.query – success with filter fields
// ---------------------------------------------------------------------------

func TestAuditQuery_Success_WithFilter(t *testing.T) {
	var capturedFilter audit.Filter
	logger := &mockAuditLogger{
		queryFn: func(_ context.Context, f audit.Filter) (*audit.Page, error) {
			capturedFilter = f
			return &audit.Page{Events: []audit.AuditEvent{}, Total: 0}, nil
		},
	}
	srv := &auditServiceServer{auditLogger: logger}

	f := audit.Filter{Actor: "bob", Resource: "user:alice", Limit: 50}
	filterBytes, _ := json.Marshal(f)

	req := wrapperspb.Bytes(filterBytes)
	resp, err := srv.query(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if capturedFilter.Actor != "bob" {
		t.Errorf("expected actor 'bob', got %q", capturedFilter.Actor)
	}
	if capturedFilter.Resource != "user:alice" {
		t.Errorf("expected resource 'user:alice', got %q", capturedFilter.Resource)
	}
	if capturedFilter.Limit != 50 {
		t.Errorf("expected limit 50, got %d", capturedFilter.Limit)
	}
}

// ---------------------------------------------------------------------------
// auditServiceServer.query – invalid filter JSON
// ---------------------------------------------------------------------------

func TestAuditQuery_InvalidFilterJSON(t *testing.T) {
	logger := &mockAuditLogger{}
	srv := &auditServiceServer{auditLogger: logger}
	req := wrapperspb.Bytes([]byte(`{not-json`))
	_, err := srv.query(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for invalid JSON filter")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// auditServiceServer.query – service error
// ---------------------------------------------------------------------------

func TestAuditQuery_ServiceError(t *testing.T) {
	logger := &mockAuditLogger{
		queryFn: func(_ context.Context, _ audit.Filter) (*audit.Page, error) {
			return nil, errors.New("storage unavailable")
		},
	}
	srv := &auditServiceServer{auditLogger: logger}
	req := wrapperspb.Bytes(nil)
	_, err := srv.query(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// RegisterAuditService + auditQueryHandler over real gRPC round-trip
// ---------------------------------------------------------------------------

func TestRegisterAuditService_NilLogger(t *testing.T) {
	// When logger is nil the query method returns Unimplemented, so we test
	// the handler path directly.
	srv := &auditServiceServer{auditLogger: nil}
	req := wrapperspb.Bytes([]byte(`{}`))
	_, err := srv.query(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestRegisterAuditService_RoundTrip(t *testing.T) {
	logger := &mockAuditLogger{
		queryFn: func(_ context.Context, _ audit.Filter) (*audit.Page, error) {
			return &audit.Page{
				Events: []audit.AuditEvent{{Actor: "alice", Action: "seal"}},
				Total:  1,
			}, nil
		},
	}
	conn, cleanup := startAuditTestServer(t, logger)
	defer cleanup()

	filterBytes, _ := json.Marshal(audit.Filter{Actor: "alice"})
	req := wrapperspb.Bytes(filterBytes)
	resp := new(wrapperspb.BytesValue)
	err := conn.Invoke(context.Background(), "/qrdb.v1.AuditService/Query", req, resp)
	if err != nil {
		t.Fatalf("invoke error: %v", err)
	}
	var page audit.Page
	if err := json.Unmarshal(resp.GetValue(), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if page.Total != 1 {
		t.Errorf("expected Total=1, got %d", page.Total)
	}
	if len(page.Events) != 1 || page.Events[0].Actor != "alice" {
		t.Errorf("unexpected events: %+v", page.Events)
	}
}

// ---------------------------------------------------------------------------
// RegisterAuditService – nil logger still registers but returns Unimplemented
// ---------------------------------------------------------------------------

func TestRegisterAuditService_NilLogger_RoundTrip(t *testing.T) {
	conn, cleanup := startAuditTestServer(t, nil)
	defer cleanup()

	req := wrapperspb.Bytes([]byte(`{}`))
	resp := new(wrapperspb.BytesValue)
	err := conn.Invoke(context.Background(), "/qrdb.v1.AuditService/Query", req, resp)
	if err == nil {
		t.Fatal("expected error for nil logger")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}
