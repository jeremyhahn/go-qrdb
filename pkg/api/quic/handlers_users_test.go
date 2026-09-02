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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// errMockUser is a sentinel error used by mock user service failures.
var errMockUser = errors.New("mock user error")

// newUserServer creates a Server with the given user service mock.
func newUserServer(us user.UserServicer) *Server {
	return NewServer(nil, nil, "localhost:0", WithUserService(us))
}

// ---------------------------------------------------------------------------
// handleCreateUser
// ---------------------------------------------------------------------------

func TestHandleCreateUser_Success(t *testing.T) {
	now := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	mock := &mockUserService{
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			if req.Username != "alice" {
				t.Fatalf("expected username 'alice', got %q", req.Username)
			}
			if req.TenantID != "tenant-1" {
				t.Fatalf("expected tenant 'tenant-1', got %q", req.TenantID)
			}
			return &user.User{
				ID:        1001,
				Username:  req.Username,
				TenantID:  req.TenantID,
				RoleIDs:   req.RoleIDs,
				Enabled:   true,
				CreatedAt: now,
				UpdatedAt: now,
			}, nil
		},
	}

	srv := newUserServer(mock)
	payload, _ := json.Marshal(createUserWireRequest{
		Username: "alice",
		TenantID: "tenant-1",
		RoleIDs:  []string{"role-a"},
	})

	resp, err := srv.handleCreateUser(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out userWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.ID != 1001 {
		t.Fatalf("expected ID 1001, got %d", out.ID)
	}
	if out.Username != "alice" {
		t.Fatalf("expected username 'alice', got %q", out.Username)
	}
	if out.TenantID != "tenant-1" {
		t.Fatalf("expected tenant 'tenant-1', got %q", out.TenantID)
	}
	if !out.Enabled {
		t.Fatal("expected enabled=true")
	}
	if len(out.RoleIDs) != 1 || out.RoleIDs[0] != "role-a" {
		t.Fatalf("unexpected roleIDs: %v", out.RoleIDs)
	}
}

func TestHandleCreateUser_ServiceError(t *testing.T) {
	mock := &mockUserService{
		createUserFn: func(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
			return nil, errMockUser
		},
	}

	srv := newUserServer(mock)
	payload, _ := json.Marshal(createUserWireRequest{Username: "bob"})

	_, err := srv.handleCreateUser(context.Background(), payload)
	if !errors.Is(err, errMockUser) {
		t.Fatalf("expected errMockUser, got %v", err)
	}
}

func TestHandleCreateUser_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(createUserWireRequest{Username: "x"})

	_, err := srv.handleCreateUser(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleCreateUser_InvalidJSON(t *testing.T) {
	mock := &mockUserService{}
	srv := newUserServer(mock)

	_, err := srv.handleCreateUser(context.Background(), []byte("not json"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleGetUser
// ---------------------------------------------------------------------------

func TestHandleGetUser_Success(t *testing.T) {
	u := testUser(1002, "bob", "tenant-1")
	mock := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			if id != 1002 {
				t.Fatalf("expected ID 1002, got %d", id)
			}
			return u, nil
		},
	}

	srv := newUserServer(mock)
	payload, _ := json.Marshal(getUserWireRequest{ID: 1002})

	resp, err := srv.handleGetUser(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out userWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.ID != 1002 {
		t.Fatalf("expected ID 1002, got %d", out.ID)
	}
	if out.Username != "bob" {
		t.Fatalf("expected username 'bob', got %q", out.Username)
	}
}

// TestHandleGetUser_Uint64IDRoundTrip proves a large uint64 user id survives
// the QUIC JSON-over-frame wire on both request and response, exercising the
// numeric (non-string) encoding of the id field.
func TestHandleGetUser_Uint64IDRoundTrip(t *testing.T) {
	const wantID uint64 = 1<<63 + 424242
	var got uint64
	mock := &mockUserService{
		getUserFn: func(_ context.Context, id uint64) (*user.User, error) {
			got = id
			return testUser(id, "snowflake", "tenant-1"), nil
		},
	}
	srv := newUserServer(mock)
	payload, _ := json.Marshal(getUserWireRequest{ID: wantID})

	resp, err := srv.handleGetUser(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantID {
		t.Fatalf("request id lost on wire: got %d, want %d", got, wantID)
	}
	var out userWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.ID != wantID {
		t.Fatalf("response id lost on wire: got %d, want %d", out.ID, wantID)
	}
}

func TestHandleGetUser_ServiceError(t *testing.T) {
	mock := &mockUserService{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) {
			return nil, errMockUser
		},
	}

	srv := newUserServer(mock)
	payload, _ := json.Marshal(getUserWireRequest{ID: 9999})

	_, err := srv.handleGetUser(context.Background(), payload)
	if !errors.Is(err, errMockUser) {
		t.Fatalf("expected errMockUser, got %v", err)
	}
}

func TestHandleGetUser_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(getUserWireRequest{ID: 1})

	_, err := srv.handleGetUser(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleUpdateUser
// ---------------------------------------------------------------------------

func TestHandleUpdateUser_Success(t *testing.T) {
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	mock := &mockUserService{
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			if req.ID != 1003 {
				t.Fatalf("expected ID 1003, got %d", req.ID)
			}
			if req.Username != "carol-updated" {
				t.Fatalf("expected username 'carol-updated', got %q", req.Username)
			}
			return &user.User{
				ID:        req.ID,
				Username:  req.Username,
				TenantID:  "tenant-1",
				RoleIDs:   req.RoleIDs,
				Enabled:   true,
				CreatedAt: now,
				UpdatedAt: now,
			}, nil
		},
	}

	srv := newUserServer(mock)
	payload, _ := json.Marshal(updateUserWireRequest{
		ID:       1003,
		Username: "carol-updated",
		RoleIDs:  []string{"role-b", "role-c"},
	})

	resp, err := srv.handleUpdateUser(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out userWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Username != "carol-updated" {
		t.Fatalf("expected username 'carol-updated', got %q", out.Username)
	}
	if len(out.RoleIDs) != 2 {
		t.Fatalf("expected 2 roles, got %d", len(out.RoleIDs))
	}
}

func TestHandleUpdateUser_ServiceError(t *testing.T) {
	mock := &mockUserService{
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
			return nil, errMockUser
		},
	}

	srv := newUserServer(mock)
	payload, _ := json.Marshal(updateUserWireRequest{ID: 1003})

	_, err := srv.handleUpdateUser(context.Background(), payload)
	if !errors.Is(err, errMockUser) {
		t.Fatalf("expected errMockUser, got %v", err)
	}
}

func TestHandleUpdateUser_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(updateUserWireRequest{ID: 1})

	_, err := srv.handleUpdateUser(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleDeleteUser
// ---------------------------------------------------------------------------

func TestHandleDeleteUser_Success(t *testing.T) {
	mock := &mockUserService{
		deleteUserFn: func(_ context.Context, id uint64) error {
			if id != 1004 {
				t.Fatalf("expected ID 1004, got %d", id)
			}
			return nil
		},
	}

	srv := newUserServer(mock)
	payload, _ := json.Marshal(deleteUserWireRequest{ID: 1004})

	resp, err := srv.handleDeleteUser(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out statusWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Status != "ok" {
		t.Fatalf("expected status 'ok', got %q", out.Status)
	}
}

func TestHandleDeleteUser_ServiceError(t *testing.T) {
	mock := &mockUserService{
		deleteUserFn: func(_ context.Context, _ uint64) error {
			return errMockUser
		},
	}

	srv := newUserServer(mock)
	payload, _ := json.Marshal(deleteUserWireRequest{ID: 1004})

	_, err := srv.handleDeleteUser(context.Background(), payload)
	if !errors.Is(err, errMockUser) {
		t.Fatalf("expected errMockUser, got %v", err)
	}
}

func TestHandleDeleteUser_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(deleteUserWireRequest{ID: 1})

	_, err := srv.handleDeleteUser(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleListUsers
// ---------------------------------------------------------------------------

func TestHandleListUsers_Success(t *testing.T) {
	now := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	mock := &mockUserService{
		listUsersFn: func(_ context.Context, req *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			if req.TenantID != "tenant-1" {
				t.Fatalf("expected tenant 'tenant-1', got %q", req.TenantID)
			}
			if req.Page != 1 {
				t.Fatalf("expected page 1, got %d", req.Page)
			}
			return &user.ListUsersResponse{
				Users: []*user.User{
					{ID: 1, Username: "alice", TenantID: "tenant-1", RoleIDs: []string{}, Enabled: true, CreatedAt: now, UpdatedAt: now},
					{ID: 2, Username: "bob", TenantID: "tenant-1", RoleIDs: []string{}, Enabled: true, CreatedAt: now, UpdatedAt: now},
				},
				Total:   2,
				HasMore: false,
			}, nil
		},
	}

	srv := newUserServer(mock)
	payload, _ := json.Marshal(listUsersWireRequest{
		TenantID: "tenant-1",
		Page:     1,
		PageSize: 10,
	})

	resp, err := srv.handleListUsers(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out listUsersWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.Users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(out.Users))
	}
	if out.Total != 2 {
		t.Fatalf("expected total=2, got %d", out.Total)
	}
	if out.HasMore {
		t.Fatal("expected hasMore=false")
	}
}

func TestHandleListUsers_ServiceError(t *testing.T) {
	mock := &mockUserService{
		listUsersFn: func(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			return nil, errMockUser
		},
	}

	srv := newUserServer(mock)
	payload, _ := json.Marshal(listUsersWireRequest{TenantID: "t"})

	_, err := srv.handleListUsers(context.Background(), payload)
	if !errors.Is(err, errMockUser) {
		t.Fatalf("expected errMockUser, got %v", err)
	}
}

func TestHandleListUsers_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(listUsersWireRequest{TenantID: "t"})

	_, err := srv.handleListUsers(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleEnableUser
// ---------------------------------------------------------------------------

func TestHandleEnableUser_Success(t *testing.T) {
	mock := &mockUserService{
		enableUserFn: func(_ context.Context, id uint64) error {
			if id != 1005 {
				t.Fatalf("expected ID 1005, got %d", id)
			}
			return nil
		},
	}

	srv := newUserServer(mock)
	payload, _ := json.Marshal(enableUserWireRequest{ID: 1005})

	resp, err := srv.handleEnableUser(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out statusWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Status != "ok" {
		t.Fatalf("expected status 'ok', got %q", out.Status)
	}
}

func TestHandleEnableUser_ServiceError(t *testing.T) {
	mock := &mockUserService{
		enableUserFn: func(_ context.Context, _ uint64) error {
			return errMockUser
		},
	}

	srv := newUserServer(mock)
	payload, _ := json.Marshal(enableUserWireRequest{ID: 1005})

	_, err := srv.handleEnableUser(context.Background(), payload)
	if !errors.Is(err, errMockUser) {
		t.Fatalf("expected errMockUser, got %v", err)
	}
}

func TestHandleEnableUser_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(enableUserWireRequest{ID: 1})

	_, err := srv.handleEnableUser(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleDisableUser
// ---------------------------------------------------------------------------

func TestHandleDisableUser_Success(t *testing.T) {
	mock := &mockUserService{
		disableUserFn: func(_ context.Context, id uint64) error {
			if id != 1006 {
				t.Fatalf("expected ID 1006, got %d", id)
			}
			return nil
		},
	}

	srv := newUserServer(mock)
	payload, _ := json.Marshal(disableUserWireRequest{ID: 1006})

	resp, err := srv.handleDisableUser(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out statusWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Status != "ok" {
		t.Fatalf("expected status 'ok', got %q", out.Status)
	}
}

func TestHandleDisableUser_ServiceError(t *testing.T) {
	mock := &mockUserService{
		disableUserFn: func(_ context.Context, _ uint64) error {
			return errMockUser
		},
	}

	srv := newUserServer(mock)
	payload, _ := json.Marshal(disableUserWireRequest{ID: 1006})

	_, err := srv.handleDisableUser(context.Background(), payload)
	if !errors.Is(err, errMockUser) {
		t.Fatalf("expected errMockUser, got %v", err)
	}
}

func TestHandleDisableUser_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(disableUserWireRequest{ID: 1})

	_, err := srv.handleDisableUser(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// userToWireResponse
// ---------------------------------------------------------------------------

func TestUserToWireResponse_NilInput(t *testing.T) {
	result := userToWireResponse(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestUserToWireResponse_NilRoleIDs(t *testing.T) {
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	u := &user.User{
		ID:        1,
		Username:  "alice",
		TenantID:  "tenant-1",
		RoleIDs:   nil,
		Enabled:   true,
		System:    false,
		CreatedAt: now,
		UpdatedAt: now,
	}
	result := userToWireResponse(u)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.RoleIDs == nil {
		t.Fatal("expected non-nil RoleIDs (empty slice), got nil")
	}
	if len(result.RoleIDs) != 0 {
		t.Fatalf("expected 0 role IDs, got %d", len(result.RoleIDs))
	}
	if result.Username != "alice" {
		t.Fatalf("expected username 'alice', got %q", result.Username)
	}
}

func TestUserToWireResponse_WithRoleIDs(t *testing.T) {
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	u := &user.User{
		ID:        2,
		Username:  "bob",
		TenantID:  "tenant-2",
		RoleIDs:   []string{"role-a", "role-b"},
		Enabled:   false,
		System:    true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	result := userToWireResponse(u)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.RoleIDs) != 2 {
		t.Fatalf("expected 2 role IDs, got %d", len(result.RoleIDs))
	}
	if !result.System {
		t.Fatal("expected system to be true")
	}
	if result.Enabled {
		t.Fatal("expected enabled to be false")
	}
	expectedCreatedAt := now.Format(time.RFC3339)
	if result.CreatedAt != expectedCreatedAt {
		t.Fatalf("expected createdAt %q, got %q", expectedCreatedAt, result.CreatedAt)
	}
}

// ---------------------------------------------------------------------------
// InvalidJSON tests for remaining user handlers
// ---------------------------------------------------------------------------

func TestHandleGetUser_InvalidJSON(t *testing.T) {
	srv := newUserServer(&mockUserService{
		getUserFn: func(_ context.Context, _ uint64) (*user.User, error) { return nil, nil },
	})
	_, err := srv.handleGetUser(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleUpdateUser_InvalidJSON(t *testing.T) {
	srv := newUserServer(&mockUserService{
		updateUserFn: func(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) { return nil, nil },
	})
	_, err := srv.handleUpdateUser(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleDeleteUser_InvalidJSON(t *testing.T) {
	srv := newUserServer(&mockUserService{
		deleteUserFn: func(_ context.Context, _ uint64) error { return nil },
	})
	_, err := srv.handleDeleteUser(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleListUsers_InvalidJSON(t *testing.T) {
	srv := newUserServer(&mockUserService{
		listUsersFn: func(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			return nil, nil
		},
	})
	_, err := srv.handleListUsers(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleEnableUser_InvalidJSON(t *testing.T) {
	srv := newUserServer(&mockUserService{
		enableUserFn: func(_ context.Context, _ uint64) error { return nil },
	})
	_, err := srv.handleEnableUser(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleDisableUser_InvalidJSON(t *testing.T) {
	srv := newUserServer(&mockUserService{
		disableUserFn: func(_ context.Context, _ uint64) error { return nil },
	})
	_, err := srv.handleDisableUser(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

// A QUIC client that could only read `enabled` would see a suspended, locked
// and pending account as identically "false". The canonical state is carried
// alongside it so the distinction survives the wire.
func TestUserToWireResponse_CarriesLifecycle(t *testing.T) {
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		lifecycle user.Lifecycle
		enabled   bool
	}{
		{user.LifecycleActive, true},
		{user.LifecycleSuspended, false},
		{user.LifecycleLocked, false},
		{user.LifecycleDeleting, false},
	} {
		u := &user.User{
			ID:        3,
			Username:  "carol",
			TenantID:  "tenant-3",
			Lifecycle: tc.lifecycle,
			Enabled:   tc.enabled,
			CreatedAt: now,
			UpdatedAt: now,
		}
		result := userToWireResponse(u)
		if result.Lifecycle != string(tc.lifecycle) {
			t.Errorf("Lifecycle: got %q, want %q", result.Lifecycle, tc.lifecycle)
		}
		if result.Enabled != tc.enabled {
			t.Errorf("Enabled: got %v, want %v", result.Enabled, tc.enabled)
		}
	}
}

// Every identity field must reach a QUIC client.
func TestUserToWireResponse_CarriesEveryIdentityField(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	u := &user.User{
		ID:          4,
		Username:    "dave",
		TenantID:    "tenant-4",
		Lifecycle:   user.LifecycleActive,
		Enabled:     true,
		GroupIDs:    []string{"platform"},
		AuthMethods: []string{"piv"},
		LastAuth:    &at,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	got := userToWireResponse(u)
	if len(got.GroupIDs) != 1 || got.GroupIDs[0] != "platform" {
		t.Errorf("groupIDs: got %v", got.GroupIDs)
	}
	if len(got.AuthMethods) != 1 || got.AuthMethods[0] != "piv" {
		t.Errorf("authMethods: got %v", got.AuthMethods)
	}
	if got.LastAuth != at.Format(time.RFC3339) {
		t.Errorf("lastAuth: got %q", got.LastAuth)
	}

	u.LastAuth = nil
	if got := userToWireResponse(u).LastAuth; got != "" {
		t.Errorf("never-authenticated lastAuth: got %q, want empty", got)
	}
}
