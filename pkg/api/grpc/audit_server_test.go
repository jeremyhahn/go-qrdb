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
	"math"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
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

// mockVerifyingAuditLogger extends mockAuditLogger with VerifyChainSet, so
// it satisfies audit.Verifier for VerifyChain RPC tests. mockAuditLogger
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
// startAuditTestServer starts a bare grpc.Server with RegisterAuditService.
// ---------------------------------------------------------------------------

func startAuditTestServer(t *testing.T, logger audit.AuditLogger) (*grpc.ClientConn, func()) {
	t.Helper()
	return startAuditTestServerFull(t, logger, nil)
}

func startAuditTestServerFull(t *testing.T, logger audit.AuditLogger, replicatedAudit *service.OptInAuditLogger) (*grpc.ClientConn, func()) {
	t.Helper()
	srv := grpc.NewServer()
	RegisterAuditService(srv, logger, replicatedAudit)

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

// ---------------------------------------------------------------------------
// auditServer.VerifyChain
// ---------------------------------------------------------------------------

func TestAuditVerifyChain_NilLogger(t *testing.T) {
	srv := &auditServer{auditLogger: nil}
	_, err := srv.VerifyChain(context.Background(), &pb.AuditVerifyChainRequest{})
	if err == nil {
		t.Fatal("expected error for nil auditLogger")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestAuditVerifyChain_UnsupportedBackend(t *testing.T) {
	// mockAuditLogger does not implement audit.Verifier.
	srv := &auditServer{auditLogger: &mockAuditLogger{}}
	_, err := srv.VerifyChain(context.Background(), &pb.AuditVerifyChainRequest{})
	if err == nil {
		t.Fatal("expected error for a backend with no hash chain")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestAuditVerifyChain_Verified(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{
				Files: 3, Records: 42, Genesis: true,
				Order: []string{"a.ndjson", "b.ndjson"}, Handoff: "deadbeef",
			}, nil
		},
	}
	srv := &auditServer{auditLogger: logger}
	resp, err := srv.VerifyChain(context.Background(), &pb.AuditVerifyChainRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetVerified() {
		t.Error("expected Verified=true")
	}
	if resp.GetFiles() != 3 || resp.GetRecords() != 42 || !resp.GetGenesis() {
		t.Errorf("unexpected response: %+v", resp)
	}
	if resp.GetHandoff() != "deadbeef" {
		t.Errorf("expected handoff 'deadbeef', got %q", resp.GetHandoff())
	}
}

// TestAuditVerifyChain_ChainBroken asserts a tamper verdict is reported as
// a successful RPC response with Verified=false, NOT a gRPC error.
func TestAuditVerifyChain_ChainBroken(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 1, Records: 5},
				&audit.ChainBrokenError{Path: "audit-events.ndjson", Index: 3, Expected: "aa", Got: "bb"}
		},
	}
	srv := &auditServer{auditLogger: logger}
	resp, err := srv.VerifyChain(context.Background(), &pb.AuditVerifyChainRequest{})
	if err != nil {
		t.Fatalf("expected a successful RPC carrying the verdict, got error: %v", err)
	}
	if resp.GetVerified() {
		t.Error("expected Verified=false for a broken chain")
	}
	if resp.GetReason() == "" {
		t.Error("expected a non-empty Reason describing the break")
	}
}

func TestAuditVerifyChain_ConcurrentRotation(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return nil, &audit.ConcurrentRotationError{Path: "audit-events.ndjson", Attempts: 5}
		},
	}
	srv := &auditServer{auditLogger: logger}
	_, err := srv.VerifyChain(context.Background(), &pb.AuditVerifyChainRequest{})
	if err == nil {
		t.Fatal("expected error for a concurrent-rotation race")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %s", st.Code())
	}
}

func TestAuditVerifyChain_FileError(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return nil, &audit.FileError{Path: "audit-events.ndjson", Err: errors.New("permission denied")}
		},
	}
	srv := &auditServer{auditLogger: logger}
	_, err := srv.VerifyChain(context.Background(), &pb.AuditVerifyChainRequest{})
	if err == nil {
		t.Fatal("expected error for a file failure")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestAuditVerifyChain_Canceled(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return nil, &audit.VerificationCanceledError{Path: "audit-events.ndjson", Err: context.Canceled}
		},
	}
	srv := &auditServer{auditLogger: logger}
	_, err := srv.VerifyChain(context.Background(), &pb.AuditVerifyChainRequest{})
	if err == nil {
		t.Fatal("expected error for a canceled verification")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Canceled {
		t.Fatalf("expected Canceled, got %s", st.Code())
	}
}

func TestAuditVerifyChain_DeadlineExceeded(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return nil, &audit.VerificationCanceledError{Path: "audit-events.ndjson", Err: context.DeadlineExceeded}
		},
	}
	srv := &auditServer{auditLogger: logger}
	_, err := srv.VerifyChain(context.Background(), &pb.AuditVerifyChainRequest{})
	if err == nil {
		t.Fatal("expected error for a verification that exceeded its deadline")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %s", st.Code())
	}
}

func TestAuditVerifyChain_Busy(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return nil, &audit.VerificationBusyError{}
		},
	}
	srv := &auditServer{auditLogger: logger}
	_, err := srv.VerifyChain(context.Background(), &pb.AuditVerifyChainRequest{})
	if err == nil {
		t.Fatal("expected error while a verification is already in progress")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %s", st.Code())
	}
}

func TestRegisterAuditService_VerifyChain_RoundTrip(t *testing.T) {
	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 1, Records: 1, Genesis: true}, nil
		},
	}
	conn, cleanup := startAuditTestServer(t, logger)
	defer cleanup()

	resp, err := pb.NewAuditServiceClient(conn).VerifyChain(context.Background(), &pb.AuditVerifyChainRequest{})
	if err != nil {
		t.Fatalf("invoke error: %v", err)
	}
	if !resp.GetVerified() {
		t.Error("expected Verified=true")
	}
}

// TestRegisterAuditService_VerifyChain_RoundTrip_RecordsAboveInt32Max is the
// wire-width regression this field exists to prevent: Records is int64 on
// both pkg/audit.VerificationReport and the proto, precisely because a
// trail's record count is unbounded over its retention window. A value like
// 5_000_000_042 (chosen to sit well above math.MaxInt32 = 2_147_483_647 and
// to not be representable by truncating to int32 as some smaller,
// plausible-looking number) proves the actual wire round trip -- marshal,
// send, unmarshal -- carries it intact. Reintroducing an int32 field plus a
// cast would silently wrap this value; every other test in this file uses
// values like 10 or 42 that an int32 carries just as well, so none of them
// would catch that regression.
func TestRegisterAuditService_VerifyChain_RoundTrip_RecordsAboveInt32Max(t *testing.T) {
	wantRecords := int64(5_000_000_042)
	if wantRecords <= math.MaxInt32 {
		t.Fatalf("test bug: wantRecords = %d must exceed math.MaxInt32 = %d", wantRecords, math.MaxInt32)
	}

	logger := &mockVerifyingAuditLogger{
		verifyFn: func(_ context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 1, Records: wantRecords, Genesis: true}, nil
		},
	}
	conn, cleanup := startAuditTestServer(t, logger)
	defer cleanup()

	resp, err := pb.NewAuditServiceClient(conn).VerifyChain(context.Background(), &pb.AuditVerifyChainRequest{})
	if err != nil {
		t.Fatalf("invoke error: %v", err)
	}
	if got := resp.GetRecords(); got != wantRecords {
		t.Fatalf("Records = %d after the wire round trip, want %d (wrapped to int32 this would read %d)",
			got, wantRecords, int32(wantRecords))
	}
}

// ---------------------------------------------------------------------------
// auditServer.Query — replicated=true
// ---------------------------------------------------------------------------

// stubOptInEntitlement answers ReplicatedAuditEnabled from a fixed map.
type stubOptInEntitlement struct{ enabled map[string]bool }

func (s *stubOptInEntitlement) ReplicatedAuditEnabled(_ context.Context, tenantID string) (bool, error) {
	return s.enabled[tenantID], nil
}

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

func TestAuditQuery_Replicated_NilNotConfigured(t *testing.T) {
	srv := &auditServer{auditLogger: &mockAuditLogger{}}
	_, err := srv.Query(context.Background(), &pb.AuditQueryRequest{Replicated: true})
	if err == nil {
		t.Fatal("expected error with no replicated audit configured")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestAuditQuery_Replicated_ReadsReplicatedLoggerNotNodeLocal(t *testing.T) {
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
	srv := &auditServer{auditLogger: nodeLocal, replicatedAudit: replicated}

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "a", TenantID: "acme"})
	resp, err := srv.Query(ctx, &pb.AuditQueryRequest{Replicated: true, TenantId: "acme"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Events) != 1 || resp.Events[0].Actor != "op" {
		t.Fatalf("got %+v, want the replicated logger's event", resp.Events)
	}
}

// ---------------------------------------------------------------------------
// auditServer.VerifyReplicatedChain
// ---------------------------------------------------------------------------

func TestAuditVerifyReplicatedChain_NilNotConfigured(t *testing.T) {
	srv := &auditServer{}
	_, err := srv.VerifyReplicatedChain(context.Background(), &pb.AuditVerifyReplicatedChainRequest{})
	if err == nil {
		t.Fatal("expected error with no replicated audit configured")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestAuditVerifyReplicatedChain_Verified(t *testing.T) {
	inner := &mockReplicatedInner{verifyReport: &audit.ChainContinuityReport{Verified: true, Records: 3, Head: 3, Low: 1, Genesis: true}}
	replicated := service.NewOptInAuditLogger(inner, &stubOptInEntitlement{enabled: map[string]bool{"acme": true}}, false)
	srv := &auditServer{replicatedAudit: replicated}

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "a", TenantID: "acme"})
	resp, err := srv.VerifyReplicatedChain(ctx, &pb.AuditVerifyReplicatedChainRequest{TenantId: "acme"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetVerified() || resp.GetRecords() != 3 || resp.GetHeadSeq() != 3 || resp.GetLowSeq() != 1 || !resp.GetGenesis() {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestAuditVerifyReplicatedChain_NotEntitled(t *testing.T) {
	inner := &mockReplicatedInner{}
	replicated := service.NewOptInAuditLogger(inner, &stubOptInEntitlement{enabled: map[string]bool{}}, false)
	srv := &auditServer{replicatedAudit: replicated}

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "a", TenantID: "acme"})
	_, err := srv.VerifyReplicatedChain(ctx, &pb.AuditVerifyReplicatedChainRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error for a tenant not entitled to the replicated chain")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %s", st.Code())
	}
}
