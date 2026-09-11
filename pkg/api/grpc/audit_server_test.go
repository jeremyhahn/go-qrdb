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
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

// ---------------------------------------------------------------------------
// mockAuditLogger is a minimal AuditLogger for unit tests.
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
// auditServer.Query — nil logger
// ---------------------------------------------------------------------------

func TestAuditQuery_NilLogger(t *testing.T) {
	srv := &auditServer{auditLogger: nil}
	_, err := srv.Query(context.Background(), &pb.AuditQueryRequest{})
	if err == nil {
		t.Fatal("expected error for nil auditLogger")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// auditServer.Query — success with empty filter
// ---------------------------------------------------------------------------

func TestAuditQuery_Success_EmptyFilter(t *testing.T) {
	logger := &mockAuditLogger{}
	srv := &auditServer{auditLogger: logger}
	resp, err := srv.Query(context.Background(), &pb.AuditQueryRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Total != 1 {
		t.Errorf("expected Total=1, got %d", resp.Total)
	}
}

// ---------------------------------------------------------------------------
// auditServer.Query — success with filter fields
// ---------------------------------------------------------------------------

func TestAuditQuery_Success_WithFilter(t *testing.T) {
	var capturedFilter audit.Filter
	logger := &mockAuditLogger{
		queryFn: func(_ context.Context, f audit.Filter) (*audit.Page, error) {
			capturedFilter = f
			return &audit.Page{Events: []audit.AuditEvent{}, Total: 0}, nil
		},
	}
	srv := &auditServer{auditLogger: logger}

	resp, err := srv.Query(context.Background(), &pb.AuditQueryRequest{
		Actor:    "bob",
		Resource: "user:alice",
		Limit:    50,
	})
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
// auditServer.Query — invalid from_rfc3339
// ---------------------------------------------------------------------------

func TestAuditQuery_InvalidFromRFC3339(t *testing.T) {
	logger := &mockAuditLogger{}
	srv := &auditServer{auditLogger: logger}
	_, err := srv.Query(context.Background(), &pb.AuditQueryRequest{
		FromRfc3339: "not-a-timestamp",
	})
	if err == nil {
		t.Fatal("expected error for invalid from_rfc3339")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// auditServer.Query — invalid to_rfc3339
// ---------------------------------------------------------------------------

func TestAuditQuery_InvalidToRFC3339(t *testing.T) {
	logger := &mockAuditLogger{}
	srv := &auditServer{auditLogger: logger}
	_, err := srv.Query(context.Background(), &pb.AuditQueryRequest{
		ToRfc3339: "not-a-timestamp",
	})
	if err == nil {
		t.Fatal("expected error for invalid to_rfc3339")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// auditServer.Query — service error
// ---------------------------------------------------------------------------

func TestAuditQuery_ServiceError(t *testing.T) {
	logger := &mockAuditLogger{
		queryFn: func(_ context.Context, _ audit.Filter) (*audit.Page, error) {
			return nil, errors.New("storage unavailable")
		},
	}
	srv := &auditServer{auditLogger: logger}
	_, err := srv.Query(context.Background(), &pb.AuditQueryRequest{})
	if err == nil {
		t.Fatal("expected error from service")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// RegisterAuditService + native proto round-trip
// ---------------------------------------------------------------------------

func TestRegisterAuditService_NilLogger(t *testing.T) {
	srv := &auditServer{auditLogger: nil}
	_, err := srv.Query(context.Background(), &pb.AuditQueryRequest{})
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
				Events: []audit.AuditEvent{{Actor: "alice", Action: "seal", Timestamp: time.Now()}},
				Total:  1,
			}, nil
		},
	}
	conn, cleanup := startAuditTestServer(t, logger)
	defer cleanup()

	resp, err := pb.NewAuditServiceClient(conn).Query(context.Background(), &pb.AuditQueryRequest{
		Actor: "alice",
	})
	if err != nil {
		t.Fatalf("invoke error: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("expected Total=1, got %d", resp.Total)
	}
	if len(resp.Events) != 1 || resp.Events[0].Actor != "alice" {
		t.Errorf("unexpected events: %+v", resp.Events)
	}
}

// ---------------------------------------------------------------------------
// RegisterAuditService — nil logger skips registration; calls surface
// Unimplemented ("unknown service") like the other optional registrars
// ---------------------------------------------------------------------------

func TestRegisterAuditService_NilLogger_RoundTrip(t *testing.T) {
	conn, cleanup := startAuditTestServer(t, nil)
	defer cleanup()

	_, err := pb.NewAuditServiceClient(conn).Query(context.Background(), &pb.AuditQueryRequest{})
	if err == nil {
		t.Fatal("expected error for nil logger")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// auditAttrString helper
// ---------------------------------------------------------------------------

func TestAuditAttrString_String(t *testing.T) {
	if got := auditAttrString("hello"); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestAuditAttrString_NonString(t *testing.T) {
	got := auditAttrString(42)
	if got != "42" {
		t.Errorf("expected '42', got %q", got)
	}
}
