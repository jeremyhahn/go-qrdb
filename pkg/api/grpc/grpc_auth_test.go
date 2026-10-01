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

//go:build !integration

package grpc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// Mock cert provider
// ---------------------------------------------------------------------------

type mockCertProvider struct {
	genResult    *cert.CertBundle
	genErr       error
	exportResult []byte
	exportErr    error
	revokeResult *cert.RevokeResult
	revokeErr    error
	isRevokedRes bool
	isRevokedErr error
	listResult   []*cert.CertRecord
	listErr      error
	caCertPEM    []byte
}

func (m *mockCertProvider) GenerateClientCert(_ context.Context, _ *cert.GenerateRequest) (*cert.CertBundle, error) {
	if m.genErr != nil {
		return nil, m.genErr
	}
	return m.genResult, nil
}

func (m *mockCertProvider) ExportPKCS12(_ context.Context, _, _ []byte, _ string) ([]byte, error) {
	if m.exportErr != nil {
		return nil, m.exportErr
	}
	return m.exportResult, nil
}

func (m *mockCertProvider) RevokeCert(_ context.Context, _ string) (*cert.RevokeResult, error) {
	if m.revokeErr != nil {
		return nil, m.revokeErr
	}
	if m.revokeResult != nil {
		return m.revokeResult, nil
	}
	return &cert.RevokeResult{}, nil
}

func (m *mockCertProvider) IsRevoked(_ context.Context, _ string) (bool, error) {
	return m.isRevokedRes, m.isRevokedErr
}

func (m *mockCertProvider) ListCerts(_ context.Context, _ string) ([]*cert.CertRecord, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResult, nil
}

func (m *mockCertProvider) CACertPEM() []byte {
	return m.caCertPEM
}

// ---------------------------------------------------------------------------
// Helper for test user/role timestamps
// ---------------------------------------------------------------------------

var testTime = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// userToProto tests
// ---------------------------------------------------------------------------

func TestUserToProto_Nil(t *testing.T) {
	result := mustUserToProto(t, nil, nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestUserToProto_Valid(t *testing.T) {
	u := &user.User{
		ID:        1,
		Username:  "alice",
		TenantID:  "tenant-1",
		RoleIDs:   []string{"role-1", "role-2"},
		Lifecycle: user.LifecycleActive,
		System:    false,
		CreatedAt: testTime,
		UpdatedAt: testTime,
	}
	result := mustUserToProto(t, nil, u)
	if result.GetId() != 1 {
		t.Fatalf("expected ID 1, got %d", result.GetId())
	}
	if result.GetUsername() != "alice" {
		t.Fatalf("expected username 'alice', got %q", result.GetUsername())
	}
	if result.GetTenantId() != "tenant-1" {
		t.Fatalf("expected tenant_id 'tenant-1', got %q", result.GetTenantId())
	}
	if len(result.GetRoleIds()) != 2 {
		t.Fatalf("expected 2 role IDs, got %d", len(result.GetRoleIds()))
	}
	if result.GetLifecycle() != string(user.LifecycleActive) {
		t.Fatalf("expected lifecycle active, got %q", result.GetLifecycle())
	}
	if result.GetSystem() {
		t.Fatal("expected system=false")
	}
	if !strings.Contains(result.GetCreatedAt(), "2026") {
		t.Fatalf("expected created_at to contain '2026', got %q", result.GetCreatedAt())
	}
}

// ---------------------------------------------------------------------------
// roleToProto tests
// ---------------------------------------------------------------------------

func TestRoleToProto_Nil(t *testing.T) {
	result := roleToProto(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestRoleToProto_Valid(t *testing.T) {
	r := &role.Role{
		ID:          "role-1",
		Name:        "admin",
		TenantID:    "tenant-1",
		Permissions: []permission.Permission{"kv:put", "kv:get"},
		System:      true,
		CreatedAt:   testTime,
		UpdatedAt:   testTime,
	}
	result := roleToProto(r)
	if result.GetId() != "role-1" {
		t.Fatalf("expected ID 'role-1', got %q", result.GetId())
	}
	if result.GetName() != "admin" {
		t.Fatalf("expected name 'admin', got %q", result.GetName())
	}
	if len(result.GetPermissions()) != 2 {
		t.Fatalf("expected 2 permissions, got %d", len(result.GetPermissions()))
	}
	if !result.GetSystem() {
		t.Fatal("expected system=true")
	}
}

// ---------------------------------------------------------------------------
// certRecordToProto tests
// ---------------------------------------------------------------------------

func TestCertRecordToProto_Nil(t *testing.T) {
	result := certRecordToProto(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestCertRecordToProto_Valid(t *testing.T) {
	rec := &cert.CertRecord{
		Serial:    "ABCD1234",
		Username:  "alice",
		TenantID:  "tenant-1",
		IssuedAt:  testTime,
		ExpiresAt: testTime.Add(24 * time.Hour),
		Revoked:   false,
	}
	result := certRecordToProto(rec)
	if result.GetSerial() != "ABCD1234" {
		t.Fatalf("expected serial 'ABCD1234', got %q", result.GetSerial())
	}
	if result.GetUsername() != "alice" {
		t.Fatalf("expected username 'alice', got %q", result.GetUsername())
	}
	if result.GetRevoked() {
		t.Fatal("expected revoked=false")
	}
}

// ---------------------------------------------------------------------------
// mapServiceError tests
// ---------------------------------------------------------------------------

func TestMapAuthServiceError_Nil(t *testing.T) {
	err := mapServiceError(nil)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestMapAuthServiceError_UserNotFoundError(t *testing.T) {
	err := mapServiceError(&user.UserNotFoundError{ID: 1})
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// A taken email must be AlreadyExists, as its username sibling is. Before this
// it fell through to Internal — a server fault for a condition the caller can
// fix (Codex review, PR #191).
// A compensation failure must not be downgraded to its unwrapped cause. It
// wraps ConcurrentUpdateError, so a mapper checking that first reports Aborted
// — telling the client to retry when secondary state may be inconsistent.
func TestMapAuthServiceError_IndexCompensationOutranksItsCause(t *testing.T) {
	err := mapServiceError(&user.IndexCompensationError{
		Op: "update", UserID: 7,
		Cause:    &user.ConcurrentUpdateError{ID: 7},
		Failures: []error{errors.New("restore failed")},
	})
	if status.Code(err) != codes.DataLoss {
		t.Fatalf("got %v, want DataLoss", status.Code(err))
	}
}

// The create path's cause, which the update path's ordering did not cover.
// CreateUser claims the username index before the email one, so a taken email
// fails the create while the username claim is already held; if releasing it
// then fails, the compensation error's cause is EmailExistsError — and the
// AlreadyExists branches sat ABOVE the wrapper. The client saw "email taken",
// retried with another address, and hit "username taken" forever on a username
// reserved for a record that was never written.
func TestMapAuthServiceError_IndexCompensationOutranksADuplicateHandle(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{"email", &user.EmailExistsError{Email: "a@example.com"}},
		{"username", &user.UsernameExistsError{Username: "a"}},
		{"user id", &user.UserIDExistsError{ID: 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := mapServiceError(&user.IndexCompensationError{
				Op: "create", UserID: 7,
				Cause:    tc.cause,
				Failures: []error{errors.New("release failed")},
			})
			if status.Code(err) != codes.DataLoss {
				t.Fatalf("got %v, want DataLoss", status.Code(err))
			}
		})
	}
}

func TestMapAuthServiceError_ConcurrentUpdateIsAborted(t *testing.T) {
	err := mapServiceError(&user.ConcurrentUpdateError{ID: 7})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("got %v, want Aborted", status.Code(err))
	}
}

func TestMapAuthServiceError_EmailExistsError(t *testing.T) {
	err := mapServiceError(&user.EmailExistsError{Email: "alice@example.com", TenantID: "t1"})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("got %v, want AlreadyExists", status.Code(err))
	}
}

func TestMapAuthServiceError_UsernameExistsError(t *testing.T) {
	err := mapServiceError(&user.UsernameExistsError{Username: "alice"})
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

// TestMapAuthServiceError_UserIDExistsError pins go-qrdb#555's atomic
// create path: a store-generated user id collided with an existing identity
// claim or primary record, and this must classify AlreadyExists the same
// way a taken username does, not fall through to Internal.
func TestMapAuthServiceError_UserIDExistsError(t *testing.T) {
	err := mapServiceError(&user.UserIDExistsError{ID: 42, TenantID: "t1"})
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

// TestGRPC_CreateUser_UserIDExistsError is TestGRPC_CreateUser_Error's
// go-qrdb#555 sibling: a store-generated id collision must reach the wire
// as AlreadyExists too, not Internal.
func TestGRPC_CreateUser_UserIDExistsError(t *testing.T) {
	userSvc := &mockUserService{
		createErr: &user.UserIDExistsError{ID: 42, TenantID: "t1"},
	}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	_, err := client.CreateUser(context.Background(), &pb.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "alice",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

func TestMapAuthServiceError_UserDisabledError(t *testing.T) {
	err := mapServiceError(&user.UserDisabledError{ID: 1})
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %s", st.Code())
	}
}

// TestMapAuthServiceError_ReservedRoleNameError pins InvalidArgument, not
// AlreadyExists: a reserved system role name is refused whether or not any role
// holds it, so it is a malformed request rather than a collision (go-qrdb#17).
func TestMapAuthServiceError_ReservedRoleNameError(t *testing.T) {
	err := mapServiceError(&role.ReservedRoleNameError{Name: "so", TenantID: "t1", Detail: "wrong id"})
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestMapAuthServiceError_SystemUserImmutableError(t *testing.T) {
	err := mapServiceError(&user.SystemUserImmutableError{ID: 1})
	st, _ := status.FromError(err)
	if st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %s", st.Code())
	}
}

func TestMapAuthServiceError_UserValidationError(t *testing.T) {
	err := mapServiceError(&user.ValidationError{Field: "username", Reason: "required"})
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestMapAuthServiceError_UserStoreError(t *testing.T) {
	err := mapServiceError(&user.StoreError{Op: "put", Err: errors.New("disk")})
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestMapAuthServiceError_RoleNotFoundError(t *testing.T) {
	err := mapServiceError(&role.RoleNotFoundError{ID: "r1"})
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestMapAuthServiceError_RoleNameExistsError(t *testing.T) {
	err := mapServiceError(&role.RoleNameExistsError{Name: "admin"})
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

func TestMapAuthServiceError_SystemRoleImmutableError(t *testing.T) {
	// Every Op the store can produce, plus the zero value. Rename and
	// re-permission became reachable with go-qrdb#17; before it, only the
	// delete path could return this error and only in theory, since no seeded
	// role ever carried System: true.
	for _, op := range []string{"", "deleted", "renamed", "re-permissioned"} {
		err := mapServiceError(&role.SystemRoleImmutableError{ID: "r1", Op: op})
		st, _ := status.FromError(err)
		if st.Code() != codes.PermissionDenied {
			t.Fatalf("op %q: expected PermissionDenied, got %s", op, st.Code())
		}
	}
}

func TestMapAuthServiceError_RoleValidationError(t *testing.T) {
	err := mapServiceError(&role.ValidationError{Field: "name", Reason: "empty"})
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestMapAuthServiceError_InvalidPermissionError(t *testing.T) {
	err := mapServiceError(&role.InvalidPermissionError{Permission: "bad:perm"})
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestMapAuthServiceError_RoleStoreError(t *testing.T) {
	err := mapServiceError(&role.StoreError{Op: "get", Err: errors.New("disk")})
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestMapAuthServiceError_CertNotFoundError(t *testing.T) {
	err := mapServiceError(&cert.CertNotFoundError{Serial: "ABC"})
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestMapAuthServiceError_CertRevokedError(t *testing.T) {
	err := mapServiceError(&cert.CertRevokedError{Serial: "ABC"})
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %s", st.Code())
	}
}

func TestMapAuthServiceError_CertGenerateError(t *testing.T) {
	err := mapServiceError(&cert.CertGenerateError{Err: errors.New("gen")})
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestMapAuthServiceError_CertExportError(t *testing.T) {
	err := mapServiceError(&cert.CertExportError{Err: errors.New("export")})
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestMapAuthServiceError_CAKeyNotAvailableError(t *testing.T) {
	err := mapServiceError(&cert.CAKeyNotAvailableError{})
	st, _ := status.FromError(err)
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %s", st.Code())
	}
}

func TestMapAuthServiceError_BarrierSealedError(t *testing.T) {
	err := mapServiceError(&cert.BarrierSealedError{})
	st, _ := status.FromError(err)
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %s", st.Code())
	}
}

func TestMapAuthServiceError_CertValidationError(t *testing.T) {
	err := mapServiceError(&cert.ValidationError{Field: "cn", Reason: "empty"})
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestMapAuthServiceError_CertStoreError(t *testing.T) {
	err := mapServiceError(&cert.StoreError{Op: "put", Err: errors.New("disk")})
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestMapAuthServiceError_UnknownError(t *testing.T) {
	err := mapServiceError(errors.New("unknown error"))
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// User handler tests
// ---------------------------------------------------------------------------

func TestGRPC_CreateUser_Success(t *testing.T) {
	userSvc := &mockUserService{
		createResult: &user.User{
			ID: 1, Username: "alice", TenantID: "t1",
			RoleIDs: []string{"r1"}, Lifecycle: user.LifecycleActive,
			CreatedAt: testTime, UpdatedAt: testTime,
		},
	}
	roleSvc := &mockRoleService{getResult: &role.Role{ID: "r1", TenantID: "t1"}}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc), WithRoleService(roleSvc))
	defer cleanup()

	resp, err := client.CreateUser(context.Background(), &pb.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "alice", TenantId: "t1", RoleIds: []string{"r1"},
	})
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	if resp.GetId() != 1 {
		t.Fatalf("expected ID 1, got %d", resp.GetId())
	}
	if resp.GetUsername() != "alice" {
		t.Fatalf("expected username 'alice', got %q", resp.GetUsername())
	}
}

func TestGRPC_CreateUser_Error(t *testing.T) {
	userSvc := &mockUserService{
		createErr: &user.UsernameExistsError{Username: "alice", TenantID: "t1"},
	}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	_, err := client.CreateUser(context.Background(), &pb.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "alice",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

func TestGRPC_CreateUser_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CreateUser(context.Background(), &pb.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "alice",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_GetUser_Success(t *testing.T) {
	userSvc := &mockUserService{
		getResult: &user.User{
			ID: 1, Username: "bob", Lifecycle: user.LifecycleActive,
			CreatedAt: testTime, UpdatedAt: testTime,
		},
	}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	resp, err := client.GetUser(context.Background(), &pb.GetUserRequest{Id: 1})
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}
	if resp.GetUsername() != "bob" {
		t.Fatalf("expected username 'bob', got %q", resp.GetUsername())
	}
}

// TestGRPC_GetUser_Uint64IDRoundTrip proves a large uint64 user id survives
// the gRPC wire intact on both the request (echoed via lastGetID) and the
// response (UserResponse.id getter), which a string-era encoding could not
// represent without lossy conversion.
func TestGRPC_GetUser_Uint64IDRoundTrip(t *testing.T) {
	const wantID uint64 = 1<<63 + 1234567890

	userSvc := &mockUserService{}
	userSvc.getResult = &user.User{ID: wantID, Username: "snowflake", Lifecycle: user.LifecycleActive, CreatedAt: testTime, UpdatedAt: testTime}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	resp, err := client.GetUser(context.Background(), &pb.GetUserRequest{Id: wantID})
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}
	if userSvc.lastGetID != wantID {
		t.Fatalf("request id lost on wire: got %d, want %d", userSvc.lastGetID, wantID)
	}
	if resp.GetId() != wantID {
		t.Fatalf("response id lost on wire: got %d, want %d", resp.GetId(), wantID)
	}
}

func TestGRPC_GetUser_Error(t *testing.T) {
	userSvc := &mockUserService{
		getErr: &user.UserNotFoundError{ID: 1},
	}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	_, err := client.GetUser(context.Background(), &pb.GetUserRequest{Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_GetUser_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.GetUser(context.Background(), &pb.GetUserRequest{Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_UpdateUser_Success(t *testing.T) {
	userSvc := &mockUserService{
		getResult: &user.User{ID: 1, TenantID: "t1"},
		updateResult: &user.User{
			ID: 1, Username: "alice-updated", Lifecycle: user.LifecycleActive,
			CreatedAt: testTime, UpdatedAt: testTime,
		},
	}
	roleSvc := &mockRoleService{getResult: &role.Role{ID: "r2", TenantID: "t1"}}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc), WithRoleService(roleSvc))
	defer cleanup()

	resp, err := client.UpdateUser(context.Background(), &pb.UpdateUserProtoRequest{
		Id: 1, Username: "alice-updated", RoleIds: []string{"r2"},
	})
	if err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}
	if resp.GetUsername() != "alice-updated" {
		t.Fatalf("expected username 'alice-updated', got %q", resp.GetUsername())
	}
}

func TestGRPC_UpdateUser_Error(t *testing.T) {
	userSvc := &mockUserService{
		updateErr: &user.UserNotFoundError{ID: 1},
	}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	_, err := client.UpdateUser(context.Background(), &pb.UpdateUserProtoRequest{
		Id: 1, Username: "new",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_UpdateUser_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.UpdateUser(context.Background(), &pb.UpdateUserProtoRequest{
		Id: 1,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DeleteUser_Success(t *testing.T) {
	userSvc := &mockUserService{}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	_, err := client.DeleteUser(context.Background(), &pb.DeleteUserRequest{Id: 1})
	if err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}
}

func TestGRPC_DeleteUser_Error(t *testing.T) {
	userSvc := &mockUserService{
		deleteErr: &user.SystemUserImmutableError{ID: 1},
	}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	_, err := client.DeleteUser(context.Background(), &pb.DeleteUserRequest{Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_DeleteUser_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DeleteUser(context.Background(), &pb.DeleteUserRequest{Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_ListUsers_Success(t *testing.T) {
	userSvc := &mockUserService{
		listResult: &user.ListUsersResponse{
			Users: []*user.User{
				{ID: 1, Username: "a", CreatedAt: testTime, UpdatedAt: testTime},
				{ID: 2, Username: "b", CreatedAt: testTime, UpdatedAt: testTime},
			},
			Total:   2,
			HasMore: false,
		},
	}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	resp, err := client.ListUsers(context.Background(), &pb.ListUsersRequest{
		TenantId: "t1", Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListUsers failed: %v", err)
	}
	if len(resp.GetUsers()) != 2 {
		t.Fatalf("expected 2 users, got %d", len(resp.GetUsers()))
	}
	if resp.GetTotal() != 2 {
		t.Fatalf("expected total 2, got %d", resp.GetTotal())
	}
}

func TestGRPC_ListUsers_Error(t *testing.T) {
	userSvc := &mockUserService{
		listErr: &user.StoreError{Op: "list", Err: errors.New("fail")},
	}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	_, err := client.ListUsers(context.Background(), &pb.ListUsersRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_ListUsers_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.ListUsers(context.Background(), &pb.ListUsersRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_EnableUser_Success(t *testing.T) {
	userSvc := &mockUserService{}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	_, err := client.EnableUser(context.Background(), &pb.EnableUserRequest{Id: 1})
	if err != nil {
		t.Fatalf("EnableUser failed: %v", err)
	}
}

func TestGRPC_EnableUser_Error(t *testing.T) {
	userSvc := &mockUserService{
		enableErr: &user.UserNotFoundError{ID: 1},
	}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	_, err := client.EnableUser(context.Background(), &pb.EnableUserRequest{Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_EnableUser_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.EnableUser(context.Background(), &pb.EnableUserRequest{Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DisableUser_Success(t *testing.T) {
	userSvc := &mockUserService{}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	_, err := client.DisableUser(context.Background(), &pb.DisableUserRequest{Id: 1})
	if err != nil {
		t.Fatalf("DisableUser failed: %v", err)
	}
}

func TestGRPC_DisableUser_Error(t *testing.T) {
	userSvc := &mockUserService{
		disableErr: &user.UserNotFoundError{ID: 1},
	}
	client, cleanup := startTestServerFull(t, WithUserService(userSvc))
	defer cleanup()

	_, err := client.DisableUser(context.Background(), &pb.DisableUserRequest{Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_DisableUser_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DisableUser(context.Background(), &pb.DisableUserRequest{Id: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Role handler tests
// ---------------------------------------------------------------------------

func TestGRPC_CreateRole_Success(t *testing.T) {
	roleSvc := &mockRoleService{
		createResult: &role.Role{
			ID: "r1", Name: "admin", TenantID: "t1",
			Permissions: []permission.Permission{"kv:put"},
			CreatedAt:   testTime, UpdatedAt: testTime,
		},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	resp, err := client.CreateRole(context.Background(), &pb.CreateRoleRequest{
		Name: "admin", TenantId: "t1", Permissions: []string{"kv:put"},
	})
	if err != nil {
		t.Fatalf("CreateRole failed: %v", err)
	}
	if resp.GetName() != "admin" {
		t.Fatalf("expected name 'admin', got %q", resp.GetName())
	}
}

func TestGRPC_CreateRole_Error(t *testing.T) {
	roleSvc := &mockRoleService{
		createErr: &role.RoleNameExistsError{Name: "admin"},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	_, err := client.CreateRole(context.Background(), &pb.CreateRoleRequest{
		Name: "admin",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

func TestGRPC_CreateRole_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CreateRole(context.Background(), &pb.CreateRoleRequest{
		Name: "admin",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_GetRole_Success(t *testing.T) {
	roleSvc := &mockRoleService{
		getResult: &role.Role{
			ID: "r1", Name: "viewer",
			CreatedAt: testTime, UpdatedAt: testTime,
		},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	resp, err := client.GetRole(context.Background(), &pb.GetRoleRequest{Id: "r1"})
	if err != nil {
		t.Fatalf("GetRole failed: %v", err)
	}
	if resp.GetName() != "viewer" {
		t.Fatalf("expected name 'viewer', got %q", resp.GetName())
	}
}

func TestGRPC_GetRole_Error(t *testing.T) {
	roleSvc := &mockRoleService{
		getErr: &role.RoleNotFoundError{ID: "r1"},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	_, err := client.GetRole(context.Background(), &pb.GetRoleRequest{Id: "r1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_GetRole_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.GetRole(context.Background(), &pb.GetRoleRequest{Id: "r1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_UpdateRole_Success(t *testing.T) {
	roleSvc := &mockRoleService{
		updateResult: &role.Role{
			ID: "r1", Name: "editor",
			Permissions: []permission.Permission{"kv:put", "kv:get"},
			CreatedAt:   testTime, UpdatedAt: testTime,
		},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	resp, err := client.UpdateRole(context.Background(), &pb.UpdateRoleProtoRequest{
		Id: "r1", Name: "editor", Permissions: []string{"kv:put", "kv:get"},
	})
	if err != nil {
		t.Fatalf("UpdateRole failed: %v", err)
	}
	if resp.GetName() != "editor" {
		t.Fatalf("expected name 'editor', got %q", resp.GetName())
	}
}

func TestGRPC_UpdateRole_NoPermissions(t *testing.T) {
	roleSvc := &mockRoleService{
		updateResult: &role.Role{
			ID: "r1", Name: "renamed",
			CreatedAt: testTime, UpdatedAt: testTime,
		},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	resp, err := client.UpdateRole(context.Background(), &pb.UpdateRoleProtoRequest{
		Id: "r1", Name: "renamed",
	})
	if err != nil {
		t.Fatalf("UpdateRole failed: %v", err)
	}
	if resp.GetName() != "renamed" {
		t.Fatalf("expected name 'renamed', got %q", resp.GetName())
	}
}

func TestGRPC_UpdateRole_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.UpdateRole(context.Background(), &pb.UpdateRoleProtoRequest{
		Id: "r1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_DeleteRole_Success(t *testing.T) {
	roleSvc := &mockRoleService{}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	_, err := client.DeleteRole(context.Background(), &pb.DeleteRoleRequest{Id: "r1"})
	if err != nil {
		t.Fatalf("DeleteRole failed: %v", err)
	}
}

func TestGRPC_DeleteRole_Error(t *testing.T) {
	roleSvc := &mockRoleService{
		deleteErr: &role.SystemRoleImmutableError{ID: "r1"},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	_, err := client.DeleteRole(context.Background(), &pb.DeleteRoleRequest{Id: "r1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_DeleteRole_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.DeleteRole(context.Background(), &pb.DeleteRoleRequest{Id: "r1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_ListRoles_Success(t *testing.T) {
	roleSvc := &mockRoleService{
		listResult: &role.ListRolesResponse{
			Roles: []*role.Role{
				{ID: "r1", Name: "admin", CreatedAt: testTime, UpdatedAt: testTime},
			},
			Total:   1,
			HasMore: false,
		},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	resp, err := client.ListRoles(context.Background(), &pb.ListRolesRequest{
		TenantId: "t1", Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListRoles failed: %v", err)
	}
	if len(resp.GetRoles()) != 1 {
		t.Fatalf("expected 1 role, got %d", len(resp.GetRoles()))
	}
}

func TestGRPC_ListRoles_Error(t *testing.T) {
	roleSvc := &mockRoleService{
		listErr: &role.StoreError{Op: "list", Err: errors.New("fail")},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	_, err := client.ListRoles(context.Background(), &pb.ListRolesRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_ListRoles_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.ListRoles(context.Background(), &pb.ListRolesRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// AssignRole / UnassignRole handler tests
// ---------------------------------------------------------------------------

func TestGRPC_AssignRole_Success(t *testing.T) {
	roleSvc := &mockRoleService{
		getResult: &role.Role{
			ID: "r2", Name: "editor",
			CreatedAt: testTime, UpdatedAt: testTime,
		},
	}
	userSvc := &mockUserService{
		getResult: &user.User{
			ID: 1, RoleIDs: []string{"r1"},
			CreatedAt: testTime, UpdatedAt: testTime,
		},
		updateResult: &user.User{
			ID: 1, RoleIDs: []string{"r1", "r2"},
			CreatedAt: testTime, UpdatedAt: testTime,
		},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc), WithUserService(userSvc))
	defer cleanup()

	_, err := client.AssignRole(context.Background(), &pb.AssignRoleRequest{
		UserId: 1, RoleId: "r2",
	})
	if err != nil {
		t.Fatalf("AssignRole failed: %v", err)
	}
}

func TestGRPC_AssignRole_AlreadyAssigned(t *testing.T) {
	roleSvc := &mockRoleService{
		getResult: &role.Role{
			ID: "r1", CreatedAt: testTime, UpdatedAt: testTime,
		},
	}
	userSvc := &mockUserService{
		getResult: &user.User{
			ID: 1, RoleIDs: []string{"r1"},
			CreatedAt: testTime, UpdatedAt: testTime,
		},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc), WithUserService(userSvc))
	defer cleanup()

	// Should succeed silently (idempotent).
	_, err := client.AssignRole(context.Background(), &pb.AssignRoleRequest{
		UserId: 1, RoleId: "r1",
	})
	if err != nil {
		t.Fatalf("AssignRole failed: %v", err)
	}
}

func TestGRPC_AssignRole_RoleNotFound(t *testing.T) {
	roleSvc := &mockRoleService{
		getErr: &role.RoleNotFoundError{ID: "r99"},
	}
	userSvc := &mockUserService{}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc), WithUserService(userSvc))
	defer cleanup()

	_, err := client.AssignRole(context.Background(), &pb.AssignRoleRequest{
		UserId: 1, RoleId: "r99",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_AssignRole_NilRoleService(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithUserService(&mockUserService{}))
	defer cleanup()

	_, err := client.AssignRole(context.Background(), &pb.AssignRoleRequest{
		UserId: 1, RoleId: "r1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_AssignRole_NilUserService(t *testing.T) {
	roleSvc := &mockRoleService{
		getResult: &role.Role{ID: "r1", CreatedAt: testTime, UpdatedAt: testTime},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	_, err := client.AssignRole(context.Background(), &pb.AssignRoleRequest{
		UserId: 1, RoleId: "r1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_UnassignRole_Success(t *testing.T) {
	roleSvc := &mockRoleService{}
	userSvc := &mockUserService{
		getResult: &user.User{
			ID: 1, RoleIDs: []string{"r1", "r2"},
			CreatedAt: testTime, UpdatedAt: testTime,
		},
		updateResult: &user.User{
			ID: 1, RoleIDs: []string{"r1"},
			CreatedAt: testTime, UpdatedAt: testTime,
		},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc), WithUserService(userSvc))
	defer cleanup()

	_, err := client.UnassignRole(context.Background(), &pb.UnassignRoleRequest{
		UserId: 1, RoleId: "r2",
	})
	if err != nil {
		t.Fatalf("UnassignRole failed: %v", err)
	}
}

func TestGRPC_UnassignRole_NilRoleService(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithUserService(&mockUserService{}))
	defer cleanup()

	_, err := client.UnassignRole(context.Background(), &pb.UnassignRoleRequest{
		UserId: 1, RoleId: "r1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_UnassignRole_NilUserService(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithRoleService(&mockRoleService{}))
	defer cleanup()

	_, err := client.UnassignRole(context.Background(), &pb.UnassignRoleRequest{
		UserId: 1, RoleId: "r1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_UnassignRole_UserNotFound(t *testing.T) {
	roleSvc := &mockRoleService{}
	userSvc := &mockUserService{
		getErr: &user.UserNotFoundError{ID: 1},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc), WithUserService(userSvc))
	defer cleanup()

	_, err := client.UnassignRole(context.Background(), &pb.UnassignRoleRequest{
		UserId: 1, RoleId: "r1",
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
// ListPermissions handler test
// ---------------------------------------------------------------------------

func TestGRPC_ListPermissions(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	resp, err := client.ListPermissions(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("ListPermissions failed: %v", err)
	}
	// The response depends on permission.Categories() which is a global.
	// We just verify it does not error and returns something.
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestGRPC_ListPermissions_HasCategories(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	resp, err := client.ListPermissions(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("ListPermissions failed: %v", err)
	}
	// Categories may or may not be registered. Verify the response is structured.
	_ = resp.GetCategories()
}

// ---------------------------------------------------------------------------
// GetRoleByName handler tests
// ---------------------------------------------------------------------------

func TestGRPC_GetRoleByName_Success(t *testing.T) {
	roleSvc := &mockRoleService{
		getByNameResult: &role.Role{
			ID: "r-001", Name: "editor", TenantID: "t1",
			Permissions: []permission.Permission{"kv:put", "kv:get"},
			CreatedAt:   testTime, UpdatedAt: testTime,
		},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	resp, err := client.GetRoleByName(context.Background(), &pb.GetRoleByNameRequest{
		TenantId: "t1", Name: "editor",
	})
	if err != nil {
		t.Fatalf("GetRoleByName failed: %v", err)
	}
	if resp.GetId() != "r-001" {
		t.Fatalf("expected ID 'r-001', got %q", resp.GetId())
	}
	if resp.GetName() != "editor" {
		t.Fatalf("expected name 'editor', got %q", resp.GetName())
	}
	if resp.GetTenantId() != "t1" {
		t.Fatalf("expected tenantID 't1', got %q", resp.GetTenantId())
	}
	if len(resp.GetPermissions()) != 2 {
		t.Fatalf("expected 2 permissions, got %d", len(resp.GetPermissions()))
	}
}

func TestGRPC_GetRoleByName_NotFound(t *testing.T) {
	roleSvc := &mockRoleService{
		getByNameErr: &role.RoleNotFoundError{ID: "editor"},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	_, err := client.GetRoleByName(context.Background(), &pb.GetRoleByNameRequest{
		TenantId: "t1", Name: "editor",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_GetRoleByName_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.GetRoleByName(context.Background(), &pb.GetRoleByNameRequest{
		TenantId: "t1", Name: "editor",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// ResolveRolePermissions handler tests
// ---------------------------------------------------------------------------

func TestGRPC_ResolveRolePermissions_Success(t *testing.T) {
	roleSvc := &mockRoleService{
		resolveResult: []permission.Permission{"kv:put", "kv:get", "admin:health"},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	resp, err := client.ResolveRolePermissions(context.Background(), &pb.ResolveRolePermissionsRequest{
		RoleIds: []string{"r-001", "r-002"},
	})
	if err != nil {
		t.Fatalf("ResolveRolePermissions failed: %v", err)
	}
	if len(resp.GetPermissions()) != 3 {
		t.Fatalf("expected 3 permissions, got %d", len(resp.GetPermissions()))
	}
	found := false
	for _, p := range resp.GetPermissions() {
		if p == "kv:put" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'kv:put' in permissions, got %v", resp.GetPermissions())
	}
}

func TestGRPC_ResolveRolePermissions_ServiceError(t *testing.T) {
	roleSvc := &mockRoleService{
		resolveErr: &role.StoreError{Op: "resolve", Err: errors.New("storage failure")},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	_, err := client.ResolveRolePermissions(context.Background(), &pb.ResolveRolePermissionsRequest{
		RoleIds: []string{"r-001"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_ResolveRolePermissions_EmptyRoleIDs(t *testing.T) {
	roleSvc := &mockRoleService{
		resolveResult: []permission.Permission{},
	}
	client, cleanup := startTestServerFull(t, WithRoleService(roleSvc))
	defer cleanup()

	resp, err := client.ResolveRolePermissions(context.Background(), &pb.ResolveRolePermissionsRequest{
		RoleIds: []string{},
	})
	if err != nil {
		t.Fatalf("ResolveRolePermissions with empty IDs failed: %v", err)
	}
	if len(resp.GetPermissions()) != 0 {
		t.Fatalf("expected 0 permissions for empty role IDs, got %d", len(resp.GetPermissions()))
	}
}

func TestGRPC_ResolveRolePermissions_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.ResolveRolePermissions(context.Background(), &pb.ResolveRolePermissionsRequest{
		RoleIds: []string{"r-001"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Certificate handler tests
// ---------------------------------------------------------------------------

func TestGRPC_GenerateCert_Success(t *testing.T) {
	certProv := &mockCertProvider{
		genResult: &cert.CertBundle{
			CertPEM: []byte("cert-pem"),
			KeyPEM:  []byte("key-pem"),
			Serial:  "ABC123",
		},
	}
	client, cleanup := startTestServerFull(t, WithCertProvider(certProv))
	defer cleanup()

	resp, err := client.GenerateCert(context.Background(), &pb.GenerateCertRequest{
		Username: "alice", TenantId: "t1",
	})
	if err != nil {
		t.Fatalf("GenerateCert failed: %v", err)
	}
	if resp.GetCertPem() != "cert-pem" {
		t.Fatalf("expected cert PEM 'cert-pem', got %q", resp.GetCertPem())
	}
	if resp.GetSerial() != "ABC123" {
		t.Fatalf("expected serial 'ABC123', got %q", resp.GetSerial())
	}
}

func TestGRPC_GenerateCert_WithTTL(t *testing.T) {
	certProv := &mockCertProvider{
		genResult: &cert.CertBundle{
			CertPEM: []byte("c"), KeyPEM: []byte("k"), Serial: "S",
		},
	}
	client, cleanup := startTestServerFull(t, WithCertProvider(certProv))
	defer cleanup()

	resp, err := client.GenerateCert(context.Background(), &pb.GenerateCertRequest{
		Username: "alice", Ttl: "24h",
	})
	if err != nil {
		t.Fatalf("GenerateCert with TTL failed: %v", err)
	}
	if resp.GetSerial() != "S" {
		t.Fatalf("expected serial 'S', got %q", resp.GetSerial())
	}
}

func TestGRPC_GenerateCert_InvalidTTL(t *testing.T) {
	certProv := &mockCertProvider{}
	client, cleanup := startTestServerFull(t, WithCertProvider(certProv))
	defer cleanup()

	_, err := client.GenerateCert(context.Background(), &pb.GenerateCertRequest{
		Username: "alice", Ttl: "bad-ttl",
	})
	if err == nil {
		t.Fatal("expected error for invalid TTL, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_GenerateCert_Error(t *testing.T) {
	certProv := &mockCertProvider{
		genErr: &cert.CertGenerateError{Err: errors.New("gen failed")},
	}
	client, cleanup := startTestServerFull(t, WithCertProvider(certProv))
	defer cleanup()

	_, err := client.GenerateCert(context.Background(), &pb.GenerateCertRequest{
		Username: "alice",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_GenerateCert_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.GenerateCert(context.Background(), &pb.GenerateCertRequest{
		Username: "alice",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_ExportCert_Success(t *testing.T) {
	certProv := &mockCertProvider{
		exportResult: []byte{0x01, 0x02, 0x03},
	}
	client, cleanup := startTestServerFull(t, WithCertProvider(certProv))
	defer cleanup()

	resp, err := client.ExportCert(context.Background(), &pb.ExportCertRequest{
		CertPem: "cert", KeyPem: "key", Passphrase: "pass",
	})
	if err != nil {
		t.Fatalf("ExportCert failed: %v", err)
	}
	if resp.GetPkcs12() == "" {
		t.Fatal("expected non-empty PKCS12 base64")
	}
}

func TestGRPC_ExportCert_Error(t *testing.T) {
	certProv := &mockCertProvider{
		exportErr: &cert.CertExportError{Err: errors.New("export")},
	}
	client, cleanup := startTestServerFull(t, WithCertProvider(certProv))
	defer cleanup()

	_, err := client.ExportCert(context.Background(), &pb.ExportCertRequest{
		CertPem: "c", KeyPem: "k", Passphrase: "p",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_ExportCert_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.ExportCert(context.Background(), &pb.ExportCertRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_RevokeCert_Success(t *testing.T) {
	certProv := &mockCertProvider{}
	client, cleanup := startTestServerFull(t, WithCertProvider(certProv))
	defer cleanup()

	_, err := client.RevokeCert(context.Background(), &pb.RevokeCertRequest{
		Serial: "ABC",
	})
	if err != nil {
		t.Fatalf("RevokeCert failed: %v", err)
	}
}

func TestGRPC_RevokeCert_Error(t *testing.T) {
	certProv := &mockCertProvider{
		revokeErr: &cert.CertNotFoundError{Serial: "XYZ"},
	}
	client, cleanup := startTestServerFull(t, WithCertProvider(certProv))
	defer cleanup()

	_, err := client.RevokeCert(context.Background(), &pb.RevokeCertRequest{
		Serial: "XYZ",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_RevokeCert_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.RevokeCert(context.Background(), &pb.RevokeCertRequest{
		Serial: "ABC",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_ListCerts_Success(t *testing.T) {
	certProv := &mockCertProvider{
		listResult: []*cert.CertRecord{
			{
				Serial: "S1", Username: "alice", TenantID: "t1",
				IssuedAt: testTime, ExpiresAt: testTime.Add(24 * time.Hour),
			},
		},
	}
	client, cleanup := startTestServerFull(t, WithCertProvider(certProv))
	defer cleanup()

	resp, err := client.ListCerts(context.Background(), &pb.ListCertsRequest{
		TenantId: "t1",
	})
	if err != nil {
		t.Fatalf("ListCerts failed: %v", err)
	}
	if len(resp.GetCerts()) != 1 {
		t.Fatalf("expected 1 cert, got %d", len(resp.GetCerts()))
	}
	if resp.GetCerts()[0].GetSerial() != "S1" {
		t.Fatalf("expected serial 'S1', got %q", resp.GetCerts()[0].GetSerial())
	}
}

func TestGRPC_ListCerts_Error(t *testing.T) {
	certProv := &mockCertProvider{
		listErr: &cert.StoreError{Op: "list", Err: errors.New("disk")},
	}
	client, cleanup := startTestServerFull(t, WithCertProvider(certProv))
	defer cleanup()

	_, err := client.ListCerts(context.Background(), &pb.ListCertsRequest{
		TenantId: "t1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_ListCerts_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.ListCerts(context.Background(), &pb.ListCertsRequest{
		TenantId: "t1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// ServerOption tests for auth services
// ---------------------------------------------------------------------------

func TestWithUserService(t *testing.T) {
	svc := &mockUserService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithUserService(svc))
	if srv.userSvc == nil {
		t.Fatal("expected userSvc to be set")
	}
}

func TestWithUserService_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithUserService(nil))
	if srv.userSvc != nil {
		t.Fatal("expected userSvc to be nil")
	}
}

func TestWithRoleService(t *testing.T) {
	svc := &mockRoleService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithRoleService(svc))
	if srv.roleSvc == nil {
		t.Fatal("expected roleSvc to be set")
	}
}

func TestWithRoleService_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithRoleService(nil))
	if srv.roleSvc != nil {
		t.Fatal("expected roleSvc to be nil")
	}
}

func TestWithCertProvider(t *testing.T) {
	svc := &mockCertProvider{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithCertProvider(svc))
	if srv.certProv == nil {
		t.Fatal("expected certProv to be set")
	}
}

func TestWithCertProvider_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithCertProvider(nil))
	if srv.certProv != nil {
		t.Fatal("expected certProv to be nil")
	}
}

// The gRPC UserResponse carries the canonical lifecycle state verbatim; it is
// the only account-state field on the message.
func TestUserToProto_CarriesLifecycle(t *testing.T) {
	for _, l := range []user.Lifecycle{
		user.LifecyclePending,
		user.LifecycleActive,
		user.LifecycleSuspended,
		user.LifecycleLocked,
		user.LifecycleDisabled,
		user.LifecycleDeleting,
	} {
		u := &user.User{
			ID:        2,
			Username:  "bob",
			TenantID:  "tenant-2",
			Lifecycle: l,
			CreatedAt: testTime,
			UpdatedAt: testTime,
		}
		result := mustUserToProto(t, nil, u)
		if result.GetLifecycle() != string(l) {
			t.Errorf("lifecycle: got %q, want %q", result.GetLifecycle(), l)
		}
	}
}

// The removed `enabled` field stays removed: its name and number are reserved,
// so neither can be reintroduced with a different meaning.
func TestUserResponseProto_EnabledIsReserved(t *testing.T) {
	md := (&pb.UserResponse{}).ProtoReflect().Descriptor()
	if md.Fields().ByName("enabled") != nil {
		t.Error("UserResponse still declares an \"enabled\" field")
	}
	if md.Fields().ByNumber(5) != nil {
		t.Error("UserResponse field 5 is in use; it belonged to the removed \"enabled\"")
	}
	if !md.ReservedNames().Has("enabled") {
		t.Error("\"enabled\" is not a reserved name on UserResponse")
	}
	if !md.ReservedRanges().Has(5) {
		t.Error("field number 5 is not reserved on UserResponse")
	}
}

// group_ids is removed from every user message and its names and numbers are
// reserved: qrdb resolves authorization through role bindings, and a membership
// list no authorization path read must not come back with a new meaning.
func TestUserProtoMessages_GroupIDsAreReserved(t *testing.T) {
	cases := []struct {
		name   string
		msg    protoreflect.MessageDescriptor
		fields map[protoreflect.Name]protoreflect.FieldNumber
	}{
		{"UserResponse", (&pb.UserResponse{}).ProtoReflect().Descriptor(),
			map[protoreflect.Name]protoreflect.FieldNumber{"group_ids": 12}},
		{"CreateUserRequest", (&pb.CreateUserRequest{}).ProtoReflect().Descriptor(),
			map[protoreflect.Name]protoreflect.FieldNumber{"group_ids": 7}},
		{"UpdateUserProtoRequest", (&pb.UpdateUserProtoRequest{}).ProtoReflect().Descriptor(),
			map[protoreflect.Name]protoreflect.FieldNumber{"group_ids": 6, "has_group_ids": 7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for name, num := range tc.fields {
				if tc.msg.Fields().ByName(name) != nil {
					t.Errorf("%s still declares %q", tc.name, name)
				}
				if tc.msg.Fields().ByNumber(num) != nil {
					t.Errorf("%s field %d is in use; it belonged to the removed %q", tc.name, num, name)
				}
				if !tc.msg.ReservedNames().Has(name) {
					t.Errorf("%q is not a reserved name on %s", name, tc.name)
				}
				if !tc.msg.ReservedRanges().Has(num) {
					t.Errorf("field number %d is not reserved on %s", num, tc.name)
				}
			}
		})
	}
}

// A lifecycle refusal is a deliberate server decision, so it must not reach the
// client as Internal. The two errors carry different meanings and get different
// codes: an undefined state is the caller's mistake, a disconnected transition
// is a precondition on the record.
func TestMapAuthServiceError_LifecycleErrors(t *testing.T) {
	invalid := mapServiceError(&user.InvalidLifecycleError{Value: "quarantined", Op: "create"})
	if got := status.Code(invalid); got != codes.InvalidArgument {
		t.Errorf("InvalidLifecycleError: got %v, want %v", got, codes.InvalidArgument)
	}

	refused := mapServiceError(&user.LifecycleTransitionError{
		ID: 7, From: user.LifecycleDeleting, To: user.LifecycleActive,
	})
	if got := status.Code(refused); got != codes.FailedPrecondition {
		t.Errorf("LifecycleTransitionError: got %v, want %v", got, codes.FailedPrecondition)
	}
}

// Every identity field must reach a gRPC client, and "never authenticated" must
// arrive as zero rather than as a 1970 timestamp.
func TestUserToProto_CarriesEveryIdentityField(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	u := &user.User{
		ID:          3,
		Username:    "carol",
		TenantID:    "tenant-3",
		Lifecycle:   user.LifecycleActive,
		AuthMethods: []string{"webauthn", "piv"},
		LastAuth:    &at,
		CreatedAt:   testTime,
		UpdatedAt:   testTime,
	}
	got := mustUserToProto(t, nil, u)
	if len(got.GetAuthMethods()) != 2 || got.GetAuthMethods()[1] != "piv" {
		t.Errorf("auth_methods: got %v", got.GetAuthMethods())
	}
	if got.GetLastAuthUnix() != at.Unix() {
		t.Errorf("last_auth_unix: got %d, want %d", got.GetLastAuthUnix(), at.Unix())
	}

	u.LastAuth = nil
	if got := mustUserToProto(t, nil, u).GetLastAuthUnix(); got != 0 {
		t.Errorf("never-authenticated last_auth_unix: got %d, want 0", got)
	}
}

// The binding set crosses the wire in full, expired ones included; the
// expiry is seconds for the same reason last_auth_unix is (go-qrdb#535).
func TestUserToProto_CarriesCertBindingsIncludingExpired(t *testing.T) {
	expired := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	u := &user.User{ID: 3, Username: "carol", TenantID: "tenant-3", CreatedAt: testTime, UpdatedAt: testTime,
		CertBindings: []user.CertBinding{
			{Thumbprint: "AAAA", Kind: user.CertBindingSPKIThumbprint},
			{Thumbprint: "BBBB", Kind: user.CertBindingPIVAttested, NotAfter: expired},
		}}
	got := mustUserToProto(t, nil, u).GetCertBindings()
	if len(got) != 2 {
		t.Fatalf("cert_bindings: got %d, want 2", len(got))
	}
	if got[0].GetThumbprint() != "AAAA" || got[0].GetKind() != "spki_thumbprint" || got[0].GetNotAfterUnix() != 0 {
		t.Errorf("cert_bindings[0] = %v", got[0])
	}
	if got[1].GetThumbprint() != "BBBB" || got[1].GetKind() != "piv_attested" || got[1].GetNotAfterUnix() != expired.Unix() {
		t.Errorf("cert_bindings[1] = %v; the expired binding must be listed with its expiry", got[1])
	}
	u.CertBindings = nil
	if got := mustUserToProto(t, nil, u).GetCertBindings(); len(got) != 0 {
		t.Errorf("no bindings must render as none, got %v", got)
	}
}

func TestLastAuthUnix(t *testing.T) {
	if got := lastAuthUnix(nil); got != 0 {
		t.Fatalf("nil: got %d, want 0", got)
	}
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if got := lastAuthUnix(&at); got != at.Unix() {
		t.Fatalf("got %d, want %d", got, at.Unix())
	}
}

// proto3 cannot tell an absent repeated field from an empty one, so the has_*
// flag is what carries "leave alone" versus "clear". Without it a gRPC caller
// could never LIFT a method restriction: an empty list would read as "no
// change" and the clear would silently succeed while doing nothing.
func TestUpdateUser_ProtoHasFlagsCarryClearVersusLeaveAlone(t *testing.T) {
	cases := []struct {
		name        string
		req         *pb.UpdateUserProtoRequest
		wantNil     bool
		wantCleared bool
		wantSet     []string
	}{
		{
			name:    "no flag leaves alone",
			req:     &pb.UpdateUserProtoRequest{Id: 1},
			wantNil: true,
		},
		{
			// The trap: values present but the flag not set. Honouring them
			// would make the flag decorative.
			name:    "values without the flag are ignored",
			req:     &pb.UpdateUserProtoRequest{Id: 1, AuthMethods: []string{"piv"}},
			wantNil: true,
		},
		{
			name:        "flag with no values clears",
			req:         &pb.UpdateUserProtoRequest{Id: 1, HasAuthMethods: true},
			wantCleared: true,
		},
		{
			name:    "flag with values replaces",
			req:     &pb.UpdateUserProtoRequest{Id: 1, HasAuthMethods: true, AuthMethods: []string{"webauthn", "piv"}},
			wantSet: []string{"webauthn", "piv"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mockUserService{updateResult: &user.User{ID: 1}}
			s := &qrdbServiceServer{userSvc: svc}
			if _, err := s.UpdateUser(context.Background(), tc.req); err != nil {
				t.Fatalf("UpdateUser: %v", err)
			}
			got := svc.lastUpdateReq.AuthMethods
			switch {
			case tc.wantNil:
				if got != nil {
					t.Fatalf("AuthMethods = %#v, want nil (leave alone)", got)
				}
			case tc.wantCleared:
				if got == nil || len(got) != 0 {
					t.Fatalf("AuthMethods = %#v, want a non-nil empty slice (clear)", got)
				}
			default:
				if len(got) != len(tc.wantSet) || got[0] != tc.wantSet[0] {
					t.Fatalf("AuthMethods = %#v, want %v", got, tc.wantSet)
				}
			}
		})
	}
}

// The identity fields must survive the proto -> domain mapping on both verbs.
func TestCreateAndUpdateUser_ProtoCarriesIdentityFields(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	svc := &mockUserService{createResult: &user.User{ID: 1}, updateResult: &user.User{ID: 1}}
	s := &qrdbServiceServer{userSvc: svc}

	if _, err := s.CreateUser(context.Background(), &pb.CreateUserRequest{
		Username:    "bob",
		Email:       "bob@example.com",
		AuthMethods: []string{"webauthn"},
		Lifecycle:   string(user.LifecyclePending),
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if got := svc.lastCreateReq; got.Lifecycle != user.LifecyclePending ||
		len(got.AuthMethods) != 1 {
		t.Fatalf("create request dropped fields: %+v", got)
	}

	if _, err := s.UpdateUser(context.Background(), &pb.UpdateUserProtoRequest{
		Id:             1,
		Lifecycle:      string(user.LifecycleSuspended),
		HasAuthMethods: true,
		AuthMethods:    []string{"piv"},
		LastAuthUnix:   at.Unix(),
	}); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	got := svc.lastUpdateReq
	if got.Lifecycle != user.LifecycleSuspended {
		t.Errorf("Lifecycle = %q", got.Lifecycle)
	}
	if len(got.AuthMethods) != 1 || got.AuthMethods[0] != "piv" {
		t.Errorf("AuthMethods = %v", got.AuthMethods)
	}
	if got.LastAuth == nil || !got.LastAuth.Equal(at) {
		t.Errorf("LastAuth = %v, want %v", got.LastAuth, at)
	}

	// Zero seconds means "not supplied", not 1970.
	if _, err := s.UpdateUser(context.Background(), &pb.UpdateUserProtoRequest{Id: 1}); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if svc.lastUpdateReq.LastAuth != nil {
		t.Errorf("LastAuth = %v, want nil for last_auth_unix=0", svc.lastUpdateReq.LastAuth)
	}
}

// TenantForSerial derives the owning tenant from this double's OWN
// listResult, so the double cannot answer "tenant B owns it" while reporting a
// certificate inventory that says otherwise. A serial absent from listResult is
// the deny-by-serial case.
func (m *mockCertProvider) TenantForSerial(_ context.Context, serial string) (string, bool, error) {
	if m.listErr != nil {
		return "", false, m.listErr
	}
	for _, rec := range m.listResult {
		if rec != nil && rec.Serial == serial {
			return rec.TenantID, true, nil
		}
	}
	return "", false, nil
}
