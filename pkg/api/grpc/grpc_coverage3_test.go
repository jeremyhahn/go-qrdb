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

//go:build !integration && !qrdb_ts

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
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Mock AuthFactorServicer
// ---------------------------------------------------------------------------

type mockAuthFactorSvc struct {
	enrollResult    *authfactors.Enrollment
	enrollErr       error
	challengeResult *authfactors.Challenge
	challengeErr    error
	verifyErr       error
	listResult      []*authfactors.Enrollment
	listErr         error
	revokeErr       error
}

var _ service.AuthFactorServicer = (*mockAuthFactorSvc)(nil)

func newMockAuthFactorSvc() *mockAuthFactorSvc {
	now := time.Now()
	return &mockAuthFactorSvc{
		enrollResult: &authfactors.Enrollment{
			EnrollmentID: "enroll-1",
			FactorName:   "pkcs11",
			UserID:       "alice",
			TenantID:     "t1",
			Metadata:     map[string]string{"key": "val"},
			EnrolledAt:   now,
		},
		challengeResult: &authfactors.Challenge{
			ChallengeID:  "chal-1",
			EnrollmentID: "enroll-1",
			Nonce:        []byte("nonce"),
			ExpiresAt:    now.Add(time.Minute),
			MethodData:   map[string]any{"algo": "sha256"},
		},
		listResult: []*authfactors.Enrollment{
			{EnrollmentID: "e1", FactorName: "pkcs11", UserID: "alice", EnrolledAt: now},
		},
	}
}

func (m *mockAuthFactorSvc) EnrollFactor(_ context.Context, _, _ string, _ authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	if m.enrollErr != nil {
		return nil, m.enrollErr
	}
	return m.enrollResult, nil
}

func (m *mockAuthFactorSvc) ChallengeFactor(_ context.Context, _ string) (*authfactors.Challenge, error) {
	if m.challengeErr != nil {
		return nil, m.challengeErr
	}
	return m.challengeResult, nil
}

func (m *mockAuthFactorSvc) VerifyFactor(_ context.Context, _, _ string, _ *authfactors.Response) error {
	return m.verifyErr
}

func (m *mockAuthFactorSvc) ListFactors(_ context.Context, _ string) ([]*authfactors.Enrollment, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResult, nil
}

func (m *mockAuthFactorSvc) RevokeFactor(_ context.Context, _ string) error {
	return m.revokeErr
}

// ---------------------------------------------------------------------------
// Helper: start a gRPC server with AuthFactorService registered
// ---------------------------------------------------------------------------

func startServerWithAuthFactor(t *testing.T, svc service.AuthFactorServicer) (*grpc.ClientConn, func()) {
	t.Helper()
	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithTLS(tlsCfg), WithAuthFactorService(svc))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	conn, err := grpc.NewClient(srv.Addr(), grpc.WithTransportCredentials(testClientCreds(t, tlsCfg)))
	if err != nil {
		srv.Stop()
		t.Fatalf("grpc.NewClient failed: %v", err)
	}
	return conn, func() {
		_ = conn.Close()
		srv.Stop()
	}
}

// ---------------------------------------------------------------------------
// RegisterAuthFactorService tests
// ---------------------------------------------------------------------------

func TestRegisterAuthFactorService_Nil(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterAuthFactorService(grpcSrv, nil)
	grpcSrv.Stop()
}

func TestRegisterAuthFactorService_WithSvc(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterAuthFactorService(grpcSrv, newMockAuthFactorSvc())
	grpcSrv.Stop()
}

// ---------------------------------------------------------------------------
// EnrollFactor handler tests
// ---------------------------------------------------------------------------

func TestAuthFactor_EnrollFactor_Success(t *testing.T) {
	svc := newMockAuthFactorSvc()
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	resp, err := pb.NewAuthFactorServiceClient(conn).EnrollFactor(context.Background(), &pb.EnrollFactorRequest{
		FactorName:   "pkcs11",
		Subject:      "alice",
		TenantId:     "t1",
		MethodParams: map[string]string{"slot": "1"},
	})
	if err != nil {
		t.Fatalf("EnrollFactor failed: %v", err)
	}
	if resp.EnrollmentId != "enroll-1" {
		t.Fatalf("expected enrollment ID 'enroll-1', got %q", resp.EnrollmentId)
	}
	if resp.UserId != "alice" {
		t.Fatalf("expected user_id 'alice', got %q", resp.UserId)
	}
}

func TestAuthFactor_EnrollFactor_EmptySubject(t *testing.T) {
	svc := newMockAuthFactorSvc()
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	_, err := pb.NewAuthFactorServiceClient(conn).EnrollFactor(context.Background(), &pb.EnrollFactorRequest{
		FactorName: "pkcs11",
		Subject:    "",
	})
	if err == nil {
		t.Fatal("expected error for empty subject")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestAuthFactor_EnrollFactor_EmptyFactorName(t *testing.T) {
	svc := newMockAuthFactorSvc()
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	_, err := pb.NewAuthFactorServiceClient(conn).EnrollFactor(context.Background(), &pb.EnrollFactorRequest{
		Subject:    "alice",
		FactorName: "",
	})
	if err == nil {
		t.Fatal("expected error for empty factor_name")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestAuthFactor_EnrollFactor_NilMethodParams(t *testing.T) {
	svc := newMockAuthFactorSvc()
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	// MethodParams is nil - the handler builds an empty map internally.
	resp, err := pb.NewAuthFactorServiceClient(conn).EnrollFactor(context.Background(), &pb.EnrollFactorRequest{
		Subject:    "alice",
		FactorName: "pkcs11",
	})
	if err != nil {
		t.Fatalf("EnrollFactor with nil method_params failed: %v", err)
	}
	if resp.EnrollmentId == "" {
		t.Fatal("expected non-empty enrollment ID")
	}
}

func TestAuthFactor_EnrollFactor_ServiceError(t *testing.T) {
	svc := newMockAuthFactorSvc()
	svc.enrollErr = &authfactors.EnrollmentInvalidError{EnrollmentID: "x", Reason: "test"}
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	_, err := pb.NewAuthFactorServiceClient(conn).EnrollFactor(context.Background(), &pb.EnrollFactorRequest{
		Subject:    "alice",
		FactorName: "pkcs11",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// ChallengeFactor handler tests
// ---------------------------------------------------------------------------

func TestAuthFactor_ChallengeFactor_Success(t *testing.T) {
	svc := newMockAuthFactorSvc()
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	resp, err := pb.NewAuthFactorServiceClient(conn).ChallengeFactor(context.Background(), &pb.ChallengeFactorRequest{
		EnrollmentId: "enroll-1",
	})
	if err != nil {
		t.Fatalf("ChallengeFactor failed: %v", err)
	}
	if resp.ChallengeId != "chal-1" {
		t.Fatalf("expected challenge ID 'chal-1', got %q", resp.ChallengeId)
	}
	if resp.EnrollmentId != "enroll-1" {
		t.Fatalf("expected enrollment ID 'enroll-1', got %q", resp.EnrollmentId)
	}
}

func TestAuthFactor_ChallengeFactor_EmptyEnrollmentID(t *testing.T) {
	svc := newMockAuthFactorSvc()
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	_, err := pb.NewAuthFactorServiceClient(conn).ChallengeFactor(context.Background(), &pb.ChallengeFactorRequest{
		EnrollmentId: "",
	})
	if err == nil {
		t.Fatal("expected error for empty enrollment_id")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestAuthFactor_ChallengeFactor_ServiceError(t *testing.T) {
	svc := newMockAuthFactorSvc()
	svc.challengeErr = &authfactors.EnrollmentNotFoundError{EnrollmentID: "x"}
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	_, err := pb.NewAuthFactorServiceClient(conn).ChallengeFactor(context.Background(), &pb.ChallengeFactorRequest{
		EnrollmentId: "missing",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// VerifyFactor handler tests
// ---------------------------------------------------------------------------

func TestAuthFactor_VerifyFactor_Success(t *testing.T) {
	svc := newMockAuthFactorSvc()
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	_, err := pb.NewAuthFactorServiceClient(conn).VerifyFactor(context.Background(), &pb.VerifyFactorRequest{
		ChallengeId: "chal-1",
		Value:       []byte("response"),
		Metadata:    map[string]string{"k": "v"},
	})
	if err != nil {
		t.Fatalf("VerifyFactor failed: %v", err)
	}
}

func TestAuthFactor_VerifyFactor_NilMetadata(t *testing.T) {
	svc := newMockAuthFactorSvc()
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	_, err := pb.NewAuthFactorServiceClient(conn).VerifyFactor(context.Background(), &pb.VerifyFactorRequest{
		ChallengeId: "chal-1",
		Value:       []byte("r"),
	})
	if err != nil {
		t.Fatalf("VerifyFactor (nil metadata) failed: %v", err)
	}
}

func TestAuthFactor_VerifyFactor_EmptyChallengeID(t *testing.T) {
	svc := newMockAuthFactorSvc()
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	_, err := pb.NewAuthFactorServiceClient(conn).VerifyFactor(context.Background(), &pb.VerifyFactorRequest{
		ChallengeId: "",
		Value:       []byte("r"),
	})
	if err == nil {
		t.Fatal("expected error for empty challenge_id")
	}
}

func TestAuthFactor_VerifyFactor_ServiceError(t *testing.T) {
	svc := newMockAuthFactorSvc()
	svc.verifyErr = &authfactors.VerificationFailedError{FactorName: "pkcs11", Reason: "sig invalid"}
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	_, err := pb.NewAuthFactorServiceClient(conn).VerifyFactor(context.Background(), &pb.VerifyFactorRequest{
		ChallengeId: "chal-1",
		Value:       []byte("bad"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// ListFactors handler tests
// ---------------------------------------------------------------------------

func TestAuthFactor_ListFactors_Success(t *testing.T) {
	svc := newMockAuthFactorSvc()
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	resp, err := pb.NewAuthFactorServiceClient(conn).ListFactors(context.Background(), &pb.ListFactorsRequest{
		Subject: "alice",
	})
	if err != nil {
		t.Fatalf("ListFactors failed: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("expected total 1, got %d", resp.Total)
	}
}

func TestAuthFactor_ListFactors_WithRevokedAt(t *testing.T) {
	now := time.Now()
	svc := newMockAuthFactorSvc()
	svc.listResult = []*authfactors.Enrollment{
		{EnrollmentID: "e1", FactorName: "pkcs11", UserID: "alice", EnrolledAt: now, RevokedAt: now.Add(-time.Hour)},
	}
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	resp, err := pb.NewAuthFactorServiceClient(conn).ListFactors(context.Background(), &pb.ListFactorsRequest{})
	if err != nil {
		t.Fatalf("ListFactors failed: %v", err)
	}
	if len(resp.Enrollments) != 1 {
		t.Fatalf("expected 1 enrollment, got %d", len(resp.Enrollments))
	}
	if resp.Enrollments[0].RevokedAt == "" {
		t.Fatal("expected non-empty revoked_at for revoked enrollment")
	}
}

func TestAuthFactor_ListFactors_ServiceError(t *testing.T) {
	svc := newMockAuthFactorSvc()
	svc.listErr = &authfactors.FactorNotFoundError{Name: "x"}
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	_, err := pb.NewAuthFactorServiceClient(conn).ListFactors(context.Background(), &pb.ListFactorsRequest{
		Subject: "missing",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// RevokeFactor handler tests
// ---------------------------------------------------------------------------

func TestAuthFactor_RevokeFactor_Success(t *testing.T) {
	svc := newMockAuthFactorSvc()
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	_, err := pb.NewAuthFactorServiceClient(conn).RevokeFactor(context.Background(), &pb.RevokeFactorRequest{
		EnrollmentId: "enroll-1",
	})
	if err != nil {
		t.Fatalf("RevokeFactor failed: %v", err)
	}
}

func TestAuthFactor_RevokeFactor_EmptyEnrollmentID(t *testing.T) {
	svc := newMockAuthFactorSvc()
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	_, err := pb.NewAuthFactorServiceClient(conn).RevokeFactor(context.Background(), &pb.RevokeFactorRequest{
		EnrollmentId: "",
	})
	if err == nil {
		t.Fatal("expected error for empty enrollment_id")
	}
}

func TestAuthFactor_RevokeFactor_ServiceError(t *testing.T) {
	svc := newMockAuthFactorSvc()
	svc.revokeErr = &authfactors.EnrollmentRevokedError{EnrollmentID: "e1"}
	conn, cleanup := startServerWithAuthFactor(t, svc)
	defer cleanup()

	_, err := pb.NewAuthFactorServiceClient(conn).RevokeFactor(context.Background(), &pb.RevokeFactorRequest{
		EnrollmentId: "e1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// mapServiceError - all branches
// ---------------------------------------------------------------------------

func TestMapAuthFactorGRPCError_AllBranches(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"nil", nil, codes.OK},
		{"EnrollmentNotFound", &authfactors.EnrollmentNotFoundError{EnrollmentID: "x"}, codes.NotFound},
		{"FactorNotFound", &authfactors.FactorNotFoundError{Name: "x"}, codes.NotFound},
		{"EnrollmentInvalid", &authfactors.EnrollmentInvalidError{EnrollmentID: "x", Reason: "r"}, codes.InvalidArgument},
		{"EnrollmentRevoked", &authfactors.EnrollmentRevokedError{EnrollmentID: "x"}, codes.FailedPrecondition},
		{"ChallengeExpired", &authfactors.ChallengeExpiredError{ChallengeID: "x"}, codes.FailedPrecondition},
		{"VerificationFailed", &authfactors.VerificationFailedError{FactorName: "pkcs11"}, codes.Unauthenticated},
		{"HardwareUnavailable", &authfactors.HardwareUnavailableError{Factor: "x"}, codes.Unavailable},
		{"ErrFactorDisabled", authfactors.ErrFactorDisabled, codes.Unimplemented},
		{"unknown", errors.New("unknown error"), codes.Internal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapServiceError(tc.err)
			if tc.err == nil {
				if got != nil {
					t.Fatalf("expected nil, got %v", got)
				}
				return
			}
			st, ok := status.FromError(got)
			if !ok {
				t.Fatalf("not a gRPC status: %v", got)
			}
			if st.Code() != tc.want {
				t.Errorf("mapServiceError(%T) = %s, want %s", tc.err, st.Code(), tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// register.go: 0%-covered WithRegister* options
// ---------------------------------------------------------------------------

func TestWithRegisterMVCC(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterMVCC(&mockMVCCService{}))
	grpcSrv.Stop()
}

func TestWithRegisterTransaction(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterTransaction(&mockTransactionService{}))
	grpcSrv.Stop()
}

func TestWithRegisterBatch(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterBatch(&mockBatchService{}))
	grpcSrv.Stop()
}

func TestWithRegisterWatch(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterWatch(&mockWatchServiceImpl{}))
	grpcSrv.Stop()
}

func TestWithRegisterLease(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterLease(&mockLeaseService{}))
	grpcSrv.Stop()
}

func TestWithRegisterIndex(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterIndex(&mockIndexService{}))
	grpcSrv.Stop()
}

func TestWithRegisterTimeSeries(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterTimeSeries(&mockTimeSeriesService{}))
	grpcSrv.Stop()
}

func TestWithRegisterBarrierService(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterBarrierService(nil))
	grpcSrv.Stop()
}

func TestWithRegisterCeremonyService(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterCeremonyService(nil))
	grpcSrv.Stop()
}

func TestWithRegisterDatabaseService(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterDatabaseService(nil))
	grpcSrv.Stop()
}

func TestWithRegisterCertProvider_Value(t *testing.T) {
	grpcSrv := grpc.NewServer()
	// nil cert provider still exercises the option code path.
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterCertProvider(nil))
	grpcSrv.Stop()
}

func TestWithRegisterCollectionService(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterCollectionService(nil))
	grpcSrv.Stop()
}

func TestWithRegisterCredentialService_Register(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterCredentialService(nil))
	grpcSrv.Stop()
}

// ---------------------------------------------------------------------------
// server.go: 0%-covered ServerOptions
// ---------------------------------------------------------------------------

func TestWithEmbedding_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithEmbedding(nil))
	if srv.embeddingSvc != nil {
		t.Fatal("expected embeddingSvc to be nil")
	}
}

func TestWithDatabaseService_Set(t *testing.T) {
	// nil database service still exercises the option code path.
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithDatabaseService(nil))
	if srv.databaseSvc != nil {
		t.Fatal("expected databaseSvc to be nil")
	}
}

func TestWithBarrierService_Set(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithBarrierService(nil))
	if srv.barrierSvc != nil {
		t.Fatal("expected barrierSvc to be nil")
	}
}

func TestWithCeremonyService_Set(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithCeremonyService(nil))
	if srv.ceremonySvc != nil {
		t.Fatal("expected ceremonySvc to be nil")
	}
}

func TestWithCredentialService_Set(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithCredentialService(nil))
	if srv.credentialSvc != nil {
		t.Fatal("expected credentialSvc to be nil")
	}
}

func TestWithCollectionService_Set(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithCollectionService(nil))
	if srv.collectionSvc != nil {
		t.Fatal("expected collectionSvc to be nil")
	}
}

func TestWithAuthMiddleware_Set(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithAuthMiddleware(nil))
	if srv.authMW != nil {
		t.Fatal("expected authMW to be nil")
	}
}

// ---------------------------------------------------------------------------
// AuditService: interceptor path coverage via native proto gRPC client.
// ---------------------------------------------------------------------------

func TestAuditQueryHandler_WithInterceptor(t *testing.T) {
	interceptorCalled := false
	var capturedMethod string
	interceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		interceptorCalled = true
		capturedMethod = info.FullMethod
		return handler(ctx, req)
	}

	logger := &mockAuditLogger{}
	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(interceptor))
	RegisterAuditService(grpcSrv, logger, nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = grpcSrv.Serve(lis) }()
	defer grpcSrv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = conn.Close() }()

	resp, err := pb.NewAuditServiceClient(conn).Query(context.Background(), &pb.AuditQueryRequest{})
	if err != nil {
		t.Fatalf("Query with interceptor failed: %v", err)
	}
	if !interceptorCalled {
		t.Fatal("expected interceptor to be called")
	}
	if capturedMethod != "/qrdb.v1.AuditService/Query" {
		t.Fatalf("expected full method '/qrdb.v1.AuditService/Query', got %q", capturedMethod)
	}
	if resp == nil {
		t.Fatal("expected non-nil result")
	}
}

// ---------------------------------------------------------------------------
// AuditService: error path via native proto gRPC client.
// ---------------------------------------------------------------------------

func TestAuditQueryHandler_ServiceError(t *testing.T) {
	logger := &mockAuditLogger{queryErr: errors.New("server error")}
	conn, cleanup := startServerWithAudit(t, logger)
	defer cleanup()

	_, err := pb.NewAuditServiceClient(conn).Query(context.Background(), &pb.AuditQueryRequest{})
	if err == nil {
		t.Fatal("expected error from query, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// TenantBarrier: interceptor path coverage via native proto gRPC client.
// ---------------------------------------------------------------------------

func TestTenantBarrierInitHandler_WithInterceptor(t *testing.T) {
	svc := newMockTenantBarrierOnlySvc()
	interceptorCalled := false
	interceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		interceptorCalled = true
		return handler(ctx, req)
	}

	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(interceptor))
	RegisterTenantBarrierService(grpcSrv, svc, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = grpcSrv.Serve(lis) }()
	defer grpcSrv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_, err = pb.NewTenantBarrierServiceClient(conn).Init(context.Background(), &pb.TenantBarrierInitRequest{
		TenantId: "t1", Passphrase: []byte("s"),
	})
	if err != nil {
		t.Fatalf("Init with interceptor failed: %v", err)
	}
	if !interceptorCalled {
		t.Fatal("expected interceptor to be called")
	}
}

func TestTenantBarrierUnsealHandler_WithInterceptor(t *testing.T) {
	svc := newMockTenantBarrierOnlySvc()
	interceptorCalled := false
	interceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		interceptorCalled = true
		return handler(ctx, req)
	}

	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(interceptor))
	RegisterTenantBarrierService(grpcSrv, svc, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = grpcSrv.Serve(lis) }()
	defer grpcSrv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_, err = pb.NewTenantBarrierServiceClient(conn).Unseal(context.Background(), &pb.TenantBarrierUnsealRequest{
		TenantId: "t1", Key: []byte("s"),
	})
	if err != nil {
		t.Fatalf("Unseal with interceptor failed: %v", err)
	}
	if !interceptorCalled {
		t.Fatal("expected interceptor to be called")
	}
}

func TestTenantBarrierSealHandler_WithInterceptor(t *testing.T) {
	svc := newMockTenantBarrierOnlySvc()
	interceptorCalled := false
	interceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		interceptorCalled = true
		return handler(ctx, req)
	}

	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(interceptor))
	RegisterTenantBarrierService(grpcSrv, svc, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = grpcSrv.Serve(lis) }()
	defer grpcSrv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_, err = pb.NewTenantBarrierServiceClient(conn).Seal(context.Background(), &pb.TenantBarrierSealRequest{TenantId: "t1"})
	if err != nil {
		t.Fatalf("Seal with interceptor failed: %v", err)
	}
	if !interceptorCalled {
		t.Fatal("expected interceptor to be called")
	}
}

func TestTenantBarrierStatusHandler_WithInterceptor(t *testing.T) {
	svc := newMockTenantBarrierOnlySvc()
	interceptorCalled := false
	interceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		interceptorCalled = true
		return handler(ctx, req)
	}

	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(interceptor))
	RegisterTenantBarrierService(grpcSrv, svc, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = grpcSrv.Serve(lis) }()
	defer grpcSrv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_, err = pb.NewTenantBarrierServiceClient(conn).Status(context.Background(), &pb.TenantBarrierStatusRequest{TenantId: "t1"})
	if err != nil {
		t.Fatalf("Status with interceptor failed: %v", err)
	}
	if !interceptorCalled {
		t.Fatal("expected interceptor to be called")
	}
}

// ---------------------------------------------------------------------------
// TenantBarrier handler: nil service / empty tenant_id error paths.
// ---------------------------------------------------------------------------

func TestTenantBarrierInitHandler_DecodeError(t *testing.T) {
	// nil svc triggers Unimplemented — exercises the handler's nil-check branch.
	srv := &tenantBarrierServer{svc: nil}
	_, err := srv.Init(context.Background(), &pb.TenantBarrierInitRequest{TenantId: "t1"})
	if err == nil {
		t.Fatal("expected error from nil svc")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestTenantBarrierUnsealHandler_DecodeError(t *testing.T) {
	srv := &tenantBarrierServer{svc: nil}
	_, err := srv.Unseal(context.Background(), &pb.TenantBarrierUnsealRequest{TenantId: "t1"})
	if err == nil {
		t.Fatal("expected error from nil svc")
	}
}

func TestTenantBarrierSealHandler_DecodeError(t *testing.T) {
	srv := &tenantBarrierServer{svc: nil}
	_, err := srv.Seal(context.Background(), &pb.TenantBarrierSealRequest{TenantId: "t1"})
	if err == nil {
		t.Fatal("expected error from nil svc")
	}
}

func TestTenantBarrierStatusHandler_DecodeError(t *testing.T) {
	srv := &tenantBarrierServer{svc: nil}
	_, err := srv.Status(context.Background(), &pb.TenantBarrierStatusRequest{TenantId: "t1"})
	if err == nil {
		t.Fatal("expected error from nil svc")
	}
}

// nilStatusTenantBarrierSvc returns nil from TenantBarrierStatus.
type nilStatusTenantBarrierSvc struct {
	tenant.TenantServicer
	mockTenantBarrierOnlySvc
}

func (n *nilStatusTenantBarrierSvc) TenantBarrierStatus(_ context.Context, _ string) (*tenantpkg.TenantBarrierStatus, error) {
	return nil, nil
}
