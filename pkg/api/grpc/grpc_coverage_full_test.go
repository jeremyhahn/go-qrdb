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

//go:build qrdb_ts

// grpc_coverage_full_test.go covers gRPC functions under the full build tag
// set. It is constrained to qrdb_ts so it is mutually exclusive with
// grpc_coverage2-5_test.go (which are !qrdb_ts), avoiding duplicate-symbol
// errors under the no-tags `go vet ./...` and the codec_json+raft build.
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
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// Local mocks (not tag-gated)
// ---------------------------------------------------------------------------

// fullMockAuditLogger is a minimal audit.AuditLogger with configurable errors.
type fullMockAuditLogger struct {
	queryResult *audit.Page
	queryErr    error
}

func (m *fullMockAuditLogger) Emit(_ context.Context, _ audit.AuditEvent) error { return nil }
func (m *fullMockAuditLogger) Query(_ context.Context, _ audit.Filter) (*audit.Page, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	if m.queryResult == nil {
		return &audit.Page{}, nil
	}
	return m.queryResult, nil
}
func (m *fullMockAuditLogger) Close() error { return nil }

// fullMockPINBackend is a no-op PINBackend for option tests.
type fullMockPINBackend struct{}

var _ pin.PINBackend = (*fullMockPINBackend)(nil)

func (m *fullMockPINBackend) Strategy() pin.StrategyID             { return "software" }
func (m *fullMockPINBackend) SetSOPIN(_, _ string) error           { return nil }
func (m *fullMockPINBackend) SetUserPIN(_, _ string) error         { return nil }
func (m *fullMockPINBackend) ChangeSOPIN(_, _ string) error        { return nil }
func (m *fullMockPINBackend) ChangeUserPIN(_, _ string) error      { return nil }
func (m *fullMockPINBackend) VerifySOPIN(_ string) error           { return nil }
func (m *fullMockPINBackend) VerifyUserPIN(_ string) error         { return nil }
func (m *fullMockPINBackend) IsInitialized() bool                  { return true }
func (m *fullMockPINBackend) SOPINSet() bool                       { return true }
func (m *fullMockPINBackend) UserPINSet() bool                     { return true }
func (m *fullMockPINBackend) GetLockoutStatus() *pin.LockoutStatus { return nil }
func (m *fullMockPINBackend) ResetLockout(_ string) error          { return nil }
func (m *fullMockPINBackend) ClearPINs() error                     { return nil }

// certThumbprintErrUserSvc returns an error from UserByCertThumbprint.
type certThumbprintErrUserSvc struct {
	*mockUserService
	thumbprintErr error
}

func (m *certThumbprintErrUserSvc) UserByCertThumbprint(_ context.Context, _ string) (*user.User, error) {
	return nil, m.thumbprintErr
}

// ---------------------------------------------------------------------------
// Helpers for starting gRPC test servers
// ---------------------------------------------------------------------------

// startServerWithAuditFull starts a gRPC server that includes the AuditService.
func startServerWithAuditFull(t *testing.T, logger audit.AuditLogger) (*grpc.ClientConn, func()) {
	t.Helper()
	grpcSrv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(recoveryInterceptor, loggingInterceptor),
	)
	RegisterAuditService(grpcSrv, logger, nil)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		grpcSrv.Stop()
		t.Fatalf("grpc.NewClient: %v", err)
	}
	return conn, func() {
		_ = conn.Close()
		grpcSrv.Stop()
	}
}

// ---------------------------------------------------------------------------
// register.go — WithRegisterKVService, WithRegisterAdminService, WithRegisterPINBackend
// ---------------------------------------------------------------------------

func TestWithRegisterKVServiceFull(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, nil, nil, WithRegisterKVService(newMockKVService()))
	grpcSrv.Stop()
}

func TestWithRegisterAdminServiceFull(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, nil, nil, WithRegisterAdminService(newMockAdminService()))
	grpcSrv.Stop()
}

func TestWithRegisterPINBackendFull(t *testing.T) {
	grpcSrv := grpc.NewServer()
	RegisterService(grpcSrv, nil, nil, WithRegisterPINBackend(&fullMockPINBackend{}))
	grpcSrv.Stop()
}

// ---------------------------------------------------------------------------
// server.go — WithUserAuthFactorService
// ---------------------------------------------------------------------------

func TestWithUserAuthFactorServiceFull(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithUserAuthFactorService(svc))
	if srv.userAuthFactorSvc == nil {
		t.Fatal("expected userAuthFactorSvc to be set")
	}
}

func TestWithUserAuthFactorServiceNilFull(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithUserAuthFactorService(nil))
	if srv.userAuthFactorSvc != nil {
		t.Fatal("expected userAuthFactorSvc to be nil")
	}
}

// ---------------------------------------------------------------------------
// service_auth.go — GetUserByUsername and GetUserByCertThumbprint
// ---------------------------------------------------------------------------

func TestGRPC_GetUserByUsernameFull_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.GetUserByUsername(context.Background(), &pb.GetUserByUsernameRequest{
		TenantId: "t1", Username: "alice",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_GetUserByUsernameFull_Success(t *testing.T) {
	userSvc := &mockUserService{}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	resp, err := client.GetUserByUsername(context.Background(), &pb.GetUserByUsernameRequest{
		TenantId: "t1", Username: "alice",
	})
	if err != nil {
		t.Fatalf("GetUserByUsername failed: %v", err)
	}
	_ = resp
}

func TestGRPC_GetUserByUsernameFull_Error(t *testing.T) {
	userSvc := &mockUserService{
		getByUsernameErr: &user.UserNotFoundError{Lookup: "tenant/username"},
	}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	_, err := client.GetUserByUsername(context.Background(), &pb.GetUserByUsernameRequest{
		TenantId: "t1", Username: "nobody",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_GetUserByCertThumbprintFull_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.GetUserByCertThumbprint(context.Background(), &pb.GetUserByCertThumbprintRequest{
		Thumbprint: "AABBCC",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_GetUserByCertThumbprintFull_Success(t *testing.T) {
	userSvc := &mockUserService{}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	_, err := client.GetUserByCertThumbprint(context.Background(), &pb.GetUserByCertThumbprintRequest{
		Thumbprint: "AABB",
	})
	if err != nil {
		t.Fatalf("GetUserByCertThumbprint failed: %v", err)
	}
}

func TestGRPC_GetUserByCertThumbprintFull_Error(t *testing.T) {
	userSvc := &certThumbprintErrUserSvc{
		mockUserService: &mockUserService{},
		thumbprintErr:   &user.UserNotFoundError{Lookup: "cert_spki/X"},
	}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	_, err := client.GetUserByCertThumbprint(context.Background(), &pb.GetUserByCertThumbprintRequest{
		Thumbprint: "AABB",
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
// audit_server.go — AuditService native pb round-trip
// ---------------------------------------------------------------------------

func TestAuditService_QueryUnaryFull_Success(t *testing.T) {
	logger := &fullMockAuditLogger{queryResult: &audit.Page{Events: []audit.AuditEvent{}}}
	conn, cleanup := startServerWithAuditFull(t, logger)
	defer cleanup()

	resp, err := pb.NewAuditServiceClient(conn).Query(context.Background(), &pb.AuditQueryRequest{})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestAuditService_QueryUnaryFull_Error(t *testing.T) {
	logger := &fullMockAuditLogger{queryErr: errors.New("audit error")}
	conn, cleanup := startServerWithAuditFull(t, logger)
	defer cleanup()

	_, err := pb.NewAuditServiceClient(conn).Query(context.Background(), &pb.AuditQueryRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestAuditService_QueryUnaryFull_NilLogger(t *testing.T) {
	conn, cleanup := startServerWithAuditFull(t, nil)
	defer cleanup()

	_, err := pb.NewAuditServiceClient(conn).Query(context.Background(), &pb.AuditQueryRequest{})
	if err == nil {
		t.Fatal("expected error from nil logger, got nil")
	}
}

// ---------------------------------------------------------------------------
// user_auth_factor_server.go — native pb round-trip
// ---------------------------------------------------------------------------

func TestUserAuthFactor_Add_Success(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	resp, err := pb.NewUserAuthFactorServiceClient(conn).Add(context.Background(), &pb.UserAuthFactorAddRequest{
		UserId:     42,
		Type:       "totp",
		Label:      "my-totp",
		Credential: []byte("secret"),
	})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestUserAuthFactor_Add_Error(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	svc.addErr = auth.ErrFactorAlreadyExists
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	_, err := pb.NewUserAuthFactorServiceClient(conn).Add(context.Background(), &pb.UserAuthFactorAddRequest{
		UserId: 1,
		Type:   "totp",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

func TestUserAuthFactor_List_Success(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	resp, err := pb.NewUserAuthFactorServiceClient(conn).List(context.Background(), &pb.UserAuthFactorListRequest{
		UserId: 42,
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("expected total=1, got %d", resp.Total)
	}
}

func TestUserAuthFactor_List_Error(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	svc.listErr = auth.ErrUserHasNoFactors
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	_, err := pb.NewUserAuthFactorServiceClient(conn).List(context.Background(), &pb.UserAuthFactorListRequest{
		UserId: 42,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestUserAuthFactor_Get_Success(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	resp, err := pb.NewUserAuthFactorServiceClient(conn).Get(context.Background(), &pb.UserAuthFactorGetRequest{
		FactorId: "factor-1",
	})
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if resp.Factor.Id != "factor-1" {
		t.Fatalf("expected factor-1, got %q", resp.Factor.Id)
	}
}

func TestUserAuthFactor_Get_Error(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	svc.getErr = auth.ErrFactorNotFound
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	_, err := pb.NewUserAuthFactorServiceClient(conn).Get(context.Background(), &pb.UserAuthFactorGetRequest{
		FactorId: "x",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestUserAuthFactor_Remove_Success(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	resp, err := pb.NewUserAuthFactorServiceClient(conn).Remove(context.Background(), &pb.UserAuthFactorRemoveRequest{
		FactorId: "factor-1",
	})
	if err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	_ = resp
}

func TestUserAuthFactor_Remove_Error(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	svc.removeErr = auth.ErrLastFactor
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	_, err := pb.NewUserAuthFactorServiceClient(conn).Remove(context.Background(), &pb.UserAuthFactorRemoveRequest{
		FactorId: "x",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %s", st.Code())
	}
}

func TestUserAuthFactor_Rotate_Success(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	resp, err := pb.NewUserAuthFactorServiceClient(conn).Rotate(context.Background(), &pb.UserAuthFactorRotateRequest{
		OldFactorId: "factor-1",
		NewFactor:   &pb.UserAuthFactorNewFactor{UserId: 42, Type: "totp"},
	})
	if err != nil {
		t.Fatalf("Rotate failed: %v", err)
	}
	_ = resp
}

func TestUserAuthFactor_Rotate_Error(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	svc.rotateErr = auth.ErrFactorRevoked
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	_, err := pb.NewUserAuthFactorServiceClient(conn).Rotate(context.Background(), &pb.UserAuthFactorRotateRequest{
		OldFactorId: "x",
		NewFactor:   &pb.UserAuthFactorNewFactor{},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %s", st.Code())
	}
}

func TestUserAuthFactor_Relabel_Success(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	resp, err := pb.NewUserAuthFactorServiceClient(conn).Relabel(context.Background(), &pb.UserAuthFactorRelabelRequest{
		FactorId: "factor-1",
		Label:    "new-label",
	})
	if err != nil {
		t.Fatalf("Relabel failed: %v", err)
	}
	_ = resp
}

func TestUserAuthFactor_Relabel_Error(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	svc.relabelErr = auth.ErrFactorTypeUnsupported
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	_, err := pb.NewUserAuthFactorServiceClient(conn).Relabel(context.Background(), &pb.UserAuthFactorRelabelRequest{
		FactorId: "x",
		Label:    "l",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestUserAuthFactor_Login_Success(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	resp, err := pb.NewUserAuthFactorServiceClient(conn).Login(context.Background(), &pb.UserAuthFactorLoginRequest{
		Type:      "totp",
		LookupKey: []byte("key"),
	})
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if resp.Factor.Id != "factor-1" {
		t.Fatalf("expected factor-1, got %q", resp.Factor.Id)
	}
}

func TestUserAuthFactor_Login_Error(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	svc.lookupErr = auth.ErrFactorNotFound
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	_, err := pb.NewUserAuthFactorServiceClient(conn).Login(context.Background(), &pb.UserAuthFactorLoginRequest{
		Type:      "totp",
		LookupKey: []byte("bad"),
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
// mapServiceError
// ---------------------------------------------------------------------------

func TestMapUserAuthFactorGRPCError_Nil(t *testing.T) {
	if mapServiceError(nil) != nil {
		t.Error("expected nil for nil error")
	}
}

func TestMapUserAuthFactorGRPCError_NotFound(t *testing.T) {
	err := mapServiceError(auth.ErrFactorNotFound)
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestMapUserAuthFactorGRPCError_UserHasNoFactors(t *testing.T) {
	err := mapServiceError(auth.ErrUserHasNoFactors)
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestMapUserAuthFactorGRPCError_FactorRevoked(t *testing.T) {
	err := mapServiceError(auth.ErrFactorRevoked)
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %s", st.Code())
	}
}

func TestMapUserAuthFactorGRPCError_LastFactor(t *testing.T) {
	err := mapServiceError(auth.ErrLastFactor)
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %s", st.Code())
	}
}

func TestMapUserAuthFactorGRPCError_AlreadyExists(t *testing.T) {
	err := mapServiceError(auth.ErrFactorAlreadyExists)
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

func TestMapUserAuthFactorGRPCError_TypeUnsupported(t *testing.T) {
	err := mapServiceError(auth.ErrFactorTypeUnsupported)
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestMapUserAuthFactorGRPCError_InvalidFactor(t *testing.T) {
	err := mapServiceError(auth.ErrInvalidFactor)
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestMapUserAuthFactorGRPCError_Internal(t *testing.T) {
	err := mapServiceError(errors.New("unknown error"))
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// userFactorToProto — conversion helpers
// ---------------------------------------------------------------------------

func TestUserFactorToProtoFull_Nil(t *testing.T) {
	got := userFactorToProto(nil)
	if got.Id != "" {
		t.Errorf("expected empty Id for nil factor, got %q", got.Id)
	}
}

func TestUserFactorToProtoFull_WithLastUsed(t *testing.T) {
	now := time.Now()
	f := &auth.AuthFactor{
		ID:           "f1",
		UserID:       1,
		Type:         "totp",
		RegisteredAt: now,
		LastUsedAt:   &now,
	}
	got := userFactorToProto(f)
	if got.LastUsedAt == "" {
		t.Error("expected non-empty LastUsedAt")
	}
}

func TestUserFactorToProtoFull_WithRevokedAt(t *testing.T) {
	now := time.Now()
	f := &auth.AuthFactor{
		ID:           "f1",
		UserID:       1,
		Type:         "totp",
		RegisteredAt: now,
		RevokedAt:    &now,
	}
	got := userFactorToProto(f)
	if got.RevokedAt == "" {
		t.Error("expected non-empty RevokedAt")
	}
}

func TestUserFactorFromAddProtoFull(t *testing.T) {
	req := &pb.UserAuthFactorAddRequest{
		UserId:     42,
		Type:       "totp",
		Label:      "my-totp",
		Credential: []byte("secret"),
		Metadata:   map[string]string{"k": "v"},
		CreatedBy:  "admin",
	}
	got := userFactorFromAddProto(req)
	if got.UserID != 42 {
		t.Fatalf("expected UserID 42, got %d", got.UserID)
	}
	if got.Label != "my-totp" {
		t.Fatalf("expected Label 'my-totp', got %q", got.Label)
	}
}
