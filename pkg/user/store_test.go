// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package user

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// memKVStore: in-memory KVStore for unit testing
// ---------------------------------------------------------------------------

type memKVStore struct {
	data sync.Map
}

func newMemKVStore() *memKVStore {
	return &memKVStore{}
}

func (m *memKVStore) Put(_ context.Context, key string, value []byte) error {
	m.data.Store(key, append([]byte(nil), value...))
	return nil
}

func (m *memKVStore) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m.data.Load(key)
	if !ok {
		return nil, nil
	}
	return v.([]byte), nil
}

func (m *memKVStore) Delete(_ context.Context, key string) error {
	m.data.Delete(key)
	return nil
}

func (m *memKVStore) Scan(_ context.Context, prefix string, fn func(key string, value []byte) error) error {
	var scanErr error
	m.data.Range(func(k, v any) bool {
		key := k.(string)
		if strings.HasPrefix(key, prefix) {
			if err := fn(key, v.([]byte)); err != nil {
				scanErr = err
				return false
			}
		}
		return true
	})
	return scanErr
}

func (m *memKVStore) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	m.data.Range(func(k, _ any) bool {
		key := k.(string)
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return true
	})
	sort.Strings(keys)
	return keys, nil
}

func (m *memKVStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := m.data.Load(key)
	return ok, nil
}

func (m *memKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

func (m *memKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

func (m *memKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (m *memKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (m *memKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// errKVStore: a KVStore that returns errors for fault injection
// ---------------------------------------------------------------------------

type errKVStore struct {
	putErr  error
	getErr  error
	delErr  error
	scanErr error
}

func (m *errKVStore) Put(context.Context, string, []byte) error {
	return m.putErr
}

func (m *errKVStore) Get(context.Context, string) ([]byte, error) {
	return nil, m.getErr
}

func (m *errKVStore) Delete(context.Context, string) error {
	return m.delErr
}

func (m *errKVStore) Scan(context.Context, string, func(string, []byte) error) error {
	return m.scanErr
}

func (m *errKVStore) List(context.Context, string) ([]string, error) {
	return nil, nil
}

func (m *errKVStore) Exists(context.Context, string) (bool, error) {
	return false, m.getErr
}

func (m *errKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

func (m *errKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

func (m *errKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (m *errKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (m *errKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestStore(t *testing.T) UserServicer {
	t.Helper()
	store, err := NewStore(newMemKVStore())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func createTestUser(t *testing.T, svc UserServicer, username, tenantID string) *User {
	t.Helper()
	ctx := context.Background()
	user, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: username,
		TenantID: tenantID,
		RoleIDs:  []string{"viewer"},
	})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", username, err)
	}
	return user
}

// ---------------------------------------------------------------------------
// NewStore
// ---------------------------------------------------------------------------

func TestNewStore_Success(t *testing.T) {
	svc, err := NewStore(newMemKVStore())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil UserServicer")
	}
}

func TestNewStore_NilKVStore(t *testing.T) {
	_, err := NewStore(nil)
	if err == nil {
		t.Fatal("expected error for nil kvstore")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
	if target.Op != "new" {
		t.Errorf("Op: got %q, want %q", target.Op, "new")
	}
}

// ---------------------------------------------------------------------------
// CreateUser
// ---------------------------------------------------------------------------

func TestCreateUser_Success(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "alice",
		TenantID: "acme",
		RoleIDs:  []string{"admin", "editor"},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID == "" {
		t.Error("expected non-empty ID")
	}
	if user.Username != "alice" {
		t.Errorf("Username: got %q, want %q", user.Username, "alice")
	}
	if user.TenantID != "acme" {
		t.Errorf("TenantID: got %q, want %q", user.TenantID, "acme")
	}
	if len(user.RoleIDs) != 2 {
		t.Errorf("RoleIDs: got %d, want 2", len(user.RoleIDs))
	}
	if !user.Enabled {
		t.Error("expected Enabled=true")
	}
	if user.System {
		t.Error("expected System=false")
	}
	if user.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
	if user.UpdatedAt.IsZero() {
		t.Error("expected non-zero UpdatedAt")
	}
}

func TestCreateUser_DuplicateUsername(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createTestUser(t, svc, "alice", "acme")

	_, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "alice",
		TenantID: "acme",
	})
	if err == nil {
		t.Fatal("expected error for duplicate username")
	}
	var target *UsernameExistsError
	if !errors.As(err, &target) {
		t.Fatalf("expected *UsernameExistsError, got %T: %v", err, err)
	}
	if target.Username != "alice" {
		t.Errorf("Username: got %q, want %q", target.Username, "alice")
	}
}

func TestCreateUser_SameUsernameDifferentTenant(t *testing.T) {
	svc := newTestStore(t)

	u1 := createTestUser(t, svc, "alice", "tenant-a")
	u2 := createTestUser(t, svc, "alice", "tenant-b")

	if u1.ID == u2.ID {
		t.Error("users in different tenants should have different IDs")
	}
}

func TestCreateUser_EmptyUsername(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "",
		TenantID: "acme",
	})
	if err == nil {
		t.Fatal("expected error for empty username")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if target.Field != "username" {
		t.Errorf("Field: got %q, want %q", target.Field, "username")
	}
}

func TestCreateUser_NilRequest(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.CreateUser(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestCreateUser_DefaultTenant(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "bob",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.TenantID != tenant.DefaultTenantID {
		t.Errorf("TenantID: got %q, want %q", user.TenantID, tenant.DefaultTenantID)
	}
}

func TestCreateUser_NilRoleIDs(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "charlie",
		TenantID: "acme",
		RoleIDs:  nil,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.RoleIDs == nil {
		t.Fatal("expected non-nil RoleIDs (empty slice)")
	}
	if len(user.RoleIDs) != 0 {
		t.Errorf("RoleIDs length: got %d, want 0", len(user.RoleIDs))
	}
}

func TestCreateUser_StoreGetError(t *testing.T) {
	eStore := &errKVStore{getErr: errors.New("disk fault")}
	svc, err := NewStore(eStore)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	_, err = svc.CreateUser(ctx, &CreateUserRequest{
		Username: "alice",
		TenantID: "acme",
	})
	if err == nil {
		t.Fatal("expected error from store Get")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetUser
// ---------------------------------------------------------------------------

func TestGetUser_Found(t *testing.T) {
	svc := newTestStore(t)
	created := createTestUser(t, svc, "alice", "acme")
	ctx := context.Background()

	fetched, err := svc.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if fetched.ID != created.ID {
		t.Errorf("ID: got %q, want %q", fetched.ID, created.ID)
	}
	if fetched.Username != "alice" {
		t.Errorf("Username: got %q, want %q", fetched.Username, "alice")
	}
}

func TestGetUser_NotFound(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.GetUser(ctx, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent user")
	}
	var target *UserNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("expected *UserNotFoundError, got %T: %v", err, err)
	}
}

func TestGetUser_EmptyID(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.GetUser(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetUserByUsername
// ---------------------------------------------------------------------------

func TestGetUserByUsername_Found(t *testing.T) {
	svc := newTestStore(t)
	created := createTestUser(t, svc, "alice", "acme")
	ctx := context.Background()

	fetched, err := svc.GetUserByUsername(ctx, "acme", "alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if fetched.ID != created.ID {
		t.Errorf("ID: got %q, want %q", fetched.ID, created.ID)
	}
}

func TestGetUserByUsername_NotFound(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.GetUserByUsername(ctx, "acme", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent username")
	}
	var target *UserNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("expected *UserNotFoundError, got %T: %v", err, err)
	}
}

func TestGetUserByUsername_EmptyUsername(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.GetUserByUsername(ctx, "acme", "")
	if err == nil {
		t.Fatal("expected error for empty username")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestGetUserByUsername_DefaultTenant(t *testing.T) {
	svc := newTestStore(t)
	created := createTestUser(t, svc, "bob", "")
	ctx := context.Background()

	fetched, err := svc.GetUserByUsername(ctx, "", "bob")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if fetched.ID != created.ID {
		t.Errorf("ID: got %q, want %q", fetched.ID, created.ID)
	}
}

// ---------------------------------------------------------------------------
// UpdateUser
// ---------------------------------------------------------------------------

func TestUpdateUser_Success(t *testing.T) {
	svc := newTestStore(t)
	created := createTestUser(t, svc, "alice", "acme")
	ctx := context.Background()

	updated, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:      created.ID,
		RoleIDs: []string{"admin", "super"},
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if len(updated.RoleIDs) != 2 {
		t.Errorf("RoleIDs: got %d, want 2", len(updated.RoleIDs))
	}
	if updated.RoleIDs[0] != "admin" {
		t.Errorf("RoleIDs[0]: got %q, want %q", updated.RoleIDs[0], "admin")
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) && !updated.UpdatedAt.Equal(created.UpdatedAt) {
		t.Error("expected UpdatedAt to be >= CreatedAt")
	}
}

func TestUpdateUser_UsernameChange(t *testing.T) {
	svc := newTestStore(t)
	created := createTestUser(t, svc, "alice", "acme")
	ctx := context.Background()

	updated, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:       created.ID,
		Username: "alice2",
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if updated.Username != "alice2" {
		t.Errorf("Username: got %q, want %q", updated.Username, "alice2")
	}

	// Old username should no longer resolve.
	_, err = svc.GetUserByUsername(ctx, "acme", "alice")
	if err == nil {
		t.Fatal("expected error for old username")
	}
	var target *UserNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("expected *UserNotFoundError, got %T: %v", err, err)
	}

	// New username should resolve.
	fetched, err := svc.GetUserByUsername(ctx, "acme", "alice2")
	if err != nil {
		t.Fatalf("GetUserByUsername(alice2): %v", err)
	}
	if fetched.ID != created.ID {
		t.Errorf("ID: got %q, want %q", fetched.ID, created.ID)
	}
}

func TestUpdateUser_NotFound(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:       "nonexistent-id",
		Username: "bob",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent user")
	}
	var target *UserNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("expected *UserNotFoundError, got %T: %v", err, err)
	}
}

func TestUpdateUser_DuplicateUsername(t *testing.T) {
	svc := newTestStore(t)
	createTestUser(t, svc, "alice", "acme")
	bob := createTestUser(t, svc, "bob", "acme")
	ctx := context.Background()

	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:       bob.ID,
		Username: "alice",
	})
	if err == nil {
		t.Fatal("expected error for duplicate username")
	}
	var target *UsernameExistsError
	if !errors.As(err, &target) {
		t.Fatalf("expected *UsernameExistsError, got %T: %v", err, err)
	}
}

func TestUpdateUser_NilRequest(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.UpdateUser(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestUpdateUser_EmptyID(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: ""})
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestUpdateUser_NilRoleIDsNoChange(t *testing.T) {
	svc := newTestStore(t)
	created := createTestUser(t, svc, "alice", "acme")
	ctx := context.Background()

	updated, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:      created.ID,
		RoleIDs: nil, // no change
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if len(updated.RoleIDs) != 1 || updated.RoleIDs[0] != "viewer" {
		t.Errorf("RoleIDs: got %v, want [viewer]", updated.RoleIDs)
	}
}

func TestUpdateUser_SameUsernameNoOp(t *testing.T) {
	svc := newTestStore(t)
	created := createTestUser(t, svc, "alice", "acme")
	ctx := context.Background()

	// Updating with the same username should not error.
	updated, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:       created.ID,
		Username: "alice",
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if updated.Username != "alice" {
		t.Errorf("Username: got %q, want %q", updated.Username, "alice")
	}
}

// ---------------------------------------------------------------------------
// DeleteUser
// ---------------------------------------------------------------------------

func TestDeleteUser_Success(t *testing.T) {
	svc := newTestStore(t)
	created := createTestUser(t, svc, "alice", "acme")
	ctx := context.Background()

	if err := svc.DeleteUser(ctx, created.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	// Should no longer be found by ID.
	_, err := svc.GetUser(ctx, created.ID)
	if err == nil {
		t.Fatal("expected user not found after delete")
	}
	var nf *UserNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected *UserNotFoundError, got %T: %v", err, err)
	}

	// Should no longer be found by username.
	_, err = svc.GetUserByUsername(ctx, "acme", "alice")
	if err == nil {
		t.Fatal("expected username not found after delete")
	}
}

func TestDeleteUser_NotFound(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	err := svc.DeleteUser(ctx, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent user")
	}
	var target *UserNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("expected *UserNotFoundError, got %T: %v", err, err)
	}
}

func TestDeleteUser_SystemUser(t *testing.T) {
	kv := newMemKVStore()
	svc, _ := NewStore(kv)
	ctx := context.Background()

	// Create a user then mark it as system directly in KV.
	user := createTestUser(t, svc, "root", "acme")

	// Fetch, modify to system, and re-store.
	fetched, _ := svc.GetUser(ctx, user.ID)
	fetched.System = true

	// Re-serialize and put back.
	data, _ := json.Marshal(fetched)
	_ = kv.Put(ctx, primaryKey(fetched.ID), data)

	err := svc.DeleteUser(ctx, user.ID)
	if err == nil {
		t.Fatal("expected error for system user")
	}
	var target *SystemUserImmutableError
	if !errors.As(err, &target) {
		t.Fatalf("expected *SystemUserImmutableError, got %T: %v", err, err)
	}
}

func TestDeleteUser_EmptyID(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	err := svc.DeleteUser(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestDeleteUser_ThenList(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	u1 := createTestUser(t, svc, "alice", "acme")
	createTestUser(t, svc, "bob", "acme")

	if err := svc.DeleteUser(ctx, u1.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	resp, err := svc.ListUsers(ctx, &ListUsersRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("Total: got %d, want 1", resp.Total)
	}
	if resp.Users[0].Username != "bob" {
		t.Errorf("remaining user: got %q, want %q", resp.Users[0].Username, "bob")
	}
}

// ---------------------------------------------------------------------------
// ListUsers
// ---------------------------------------------------------------------------

func TestListUsers_Empty(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	resp, err := svc.ListUsers(ctx, &ListUsersRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if resp.Total != 0 {
		t.Errorf("Total: got %d, want 0", resp.Total)
	}
	if len(resp.Users) != 0 {
		t.Errorf("Users: got %d, want 0", len(resp.Users))
	}
	if resp.HasMore {
		t.Error("expected HasMore=false")
	}
}

func TestListUsers_WithUsers(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createTestUser(t, svc, "alice", "acme")
	createTestUser(t, svc, "bob", "acme")
	createTestUser(t, svc, "charlie", "acme")

	resp, err := svc.ListUsers(ctx, &ListUsersRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if resp.Total != 3 {
		t.Errorf("Total: got %d, want 3", resp.Total)
	}
	if len(resp.Users) != 3 {
		t.Errorf("Users: got %d, want 3", len(resp.Users))
	}
	if resp.HasMore {
		t.Error("expected HasMore=false")
	}
}

func TestListUsers_ByTenant(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createTestUser(t, svc, "alice", "acme")
	createTestUser(t, svc, "bob", "acme")
	createTestUser(t, svc, "charlie", "globex")

	respAcme, err := svc.ListUsers(ctx, &ListUsersRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("ListUsers(acme): %v", err)
	}
	if respAcme.Total != 2 {
		t.Errorf("acme Total: got %d, want 2", respAcme.Total)
	}

	respGlobex, err := svc.ListUsers(ctx, &ListUsersRequest{TenantID: "globex"})
	if err != nil {
		t.Fatalf("ListUsers(globex): %v", err)
	}
	if respGlobex.Total != 1 {
		t.Errorf("globex Total: got %d, want 1", respGlobex.Total)
	}
}

func TestListUsers_Pagination(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createTestUser(t, svc, "alice", "acme")
	createTestUser(t, svc, "bob", "acme")
	createTestUser(t, svc, "charlie", "acme")

	// Page 1: size 2
	resp1, err := svc.ListUsers(ctx, &ListUsersRequest{
		TenantID: "acme",
		Page:     1,
		PageSize: 2,
	})
	if err != nil {
		t.Fatalf("ListUsers page 1: %v", err)
	}
	if len(resp1.Users) != 2 {
		t.Errorf("page 1 Users: got %d, want 2", len(resp1.Users))
	}
	if resp1.Total != 3 {
		t.Errorf("page 1 Total: got %d, want 3", resp1.Total)
	}
	if !resp1.HasMore {
		t.Error("expected HasMore=true for page 1")
	}

	// Page 2: size 2
	resp2, err := svc.ListUsers(ctx, &ListUsersRequest{
		TenantID: "acme",
		Page:     2,
		PageSize: 2,
	})
	if err != nil {
		t.Fatalf("ListUsers page 2: %v", err)
	}
	if len(resp2.Users) != 1 {
		t.Errorf("page 2 Users: got %d, want 1", len(resp2.Users))
	}
	// HasMore is false when the page end >= total count.
	_ = resp2.HasMore
}

func TestListUsers_DefaultTenant(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createTestUser(t, svc, "alice", "")

	resp, err := svc.ListUsers(ctx, &ListUsersRequest{TenantID: ""})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("Total: got %d, want 1", resp.Total)
	}
}

func TestListUsers_NilRequest(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.ListUsers(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestListUsers_DefaultPageParams(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createTestUser(t, svc, "alice", "acme")

	resp, err := svc.ListUsers(ctx, &ListUsersRequest{
		TenantID: "acme",
		Page:     0,
		PageSize: 0,
	})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("Total: got %d, want 1", resp.Total)
	}
}

func TestListUsers_PageBeyondTotal(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createTestUser(t, svc, "alice", "acme")

	resp, err := svc.ListUsers(ctx, &ListUsersRequest{
		TenantID: "acme",
		Page:     100,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(resp.Users) != 0 {
		t.Errorf("Users: got %d, want 0", len(resp.Users))
	}
	if resp.Total != 1 {
		t.Errorf("Total: got %d, want 1", resp.Total)
	}
}

// ---------------------------------------------------------------------------
// EnableUser
// ---------------------------------------------------------------------------

func TestEnableUser_Success(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	user := createTestUser(t, svc, "alice", "acme")

	// Disable first.
	if err := svc.DisableUser(ctx, user.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}

	// Enable.
	if err := svc.EnableUser(ctx, user.ID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}

	fetched, err := svc.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if !fetched.Enabled {
		t.Error("expected Enabled=true after EnableUser")
	}
}

func TestEnableUser_NotFound(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	err := svc.EnableUser(ctx, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent user")
	}
	var target *UserNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("expected *UserNotFoundError, got %T: %v", err, err)
	}
}

func TestEnableUser_EmptyID(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	err := svc.EnableUser(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DisableUser
// ---------------------------------------------------------------------------

func TestDisableUser_Success(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	user := createTestUser(t, svc, "alice", "acme")

	if err := svc.DisableUser(ctx, user.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}

	fetched, err := svc.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if fetched.Enabled {
		t.Error("expected Enabled=false after DisableUser")
	}
}

func TestDisableUser_NotFound(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	err := svc.DisableUser(ctx, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent user")
	}
	var target *UserNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("expected *UserNotFoundError, got %T: %v", err, err)
	}
}

func TestDisableUser_EmptyID(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	err := svc.DisableUser(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Key functions
// ---------------------------------------------------------------------------

func TestPrimaryKey(t *testing.T) {
	key := primaryKey("abc-123")
	want := "__sys:users/id/abc-123"
	if key != want {
		t.Errorf("primaryKey: got %q, want %q", key, want)
	}
}

func TestTenantIndexKey(t *testing.T) {
	key := tenantIndexKey("acme", "abc-123")
	want := "__sys:users/tenant/acme/abc-123"
	if key != want {
		t.Errorf("tenantIndexKey: got %q, want %q", key, want)
	}
}

func TestUsernameIndexKey(t *testing.T) {
	key := usernameIndexKey("acme", "alice")
	want := "__sys:user_idx/username/acme/alice"
	if key != want {
		t.Errorf("usernameIndexKey: got %q, want %q", key, want)
	}
}

// ---------------------------------------------------------------------------
// Fault injection: Store error paths
// ---------------------------------------------------------------------------

// faultKVStore wraps memKVStore and fails Put/Delete/Scan after a
// configurable number of successful calls.
type faultKVStore struct {
	inner     *memKVStore
	putCount  int
	putFailAt int // fail on the Nth Put (1-based), 0 = never
	putErr    error
	delCount  int
	delFailAt int
	delErr    error
	scanErr   error
	getErr    error
	getFailAt int
	getCount  int
}

func (f *faultKVStore) Put(ctx context.Context, key string, value []byte) error {
	f.putCount++
	if f.putFailAt > 0 && f.putCount >= f.putFailAt {
		return f.putErr
	}
	return f.inner.Put(ctx, key, value)
}

func (f *faultKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	f.getCount++
	if f.getFailAt > 0 && f.getCount >= f.getFailAt {
		return nil, f.getErr
	}
	return f.inner.Get(ctx, key)
}

func (f *faultKVStore) Delete(ctx context.Context, key string) error {
	f.delCount++
	if f.delFailAt > 0 && f.delCount >= f.delFailAt {
		return f.delErr
	}
	return f.inner.Delete(ctx, key)
}

func (f *faultKVStore) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	if f.scanErr != nil {
		return f.scanErr
	}
	return f.inner.Scan(ctx, prefix, fn)
}

func (f *faultKVStore) List(ctx context.Context, prefix string) ([]string, error) {
	return f.inner.List(ctx, prefix)
}

func (f *faultKVStore) Exists(ctx context.Context, key string) (bool, error) {
	f.getCount++
	if f.getFailAt > 0 && f.getCount >= f.getFailAt {
		return false, f.getErr
	}
	return f.inner.Exists(ctx, key)
}

func (f *faultKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

func (f *faultKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

func (f *faultKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (f *faultKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (f *faultKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func TestCreateUser_PutPrimaryFails(t *testing.T) {
	fkv := &faultKVStore{
		inner:     newMemKVStore(),
		putFailAt: 1,
		putErr:    errors.New("disk full"),
	}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	_, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "alice",
		TenantID: "acme",
	})
	if err == nil {
		t.Fatal("expected error on Put primary")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestCreateUser_PutTenantIndexFails(t *testing.T) {
	fkv := &faultKVStore{
		inner:     newMemKVStore(),
		putFailAt: 2, // first Put (primary) succeeds, second (tenant index) fails
		putErr:    errors.New("disk full"),
	}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	_, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "alice",
		TenantID: "acme",
	})
	if err == nil {
		t.Fatal("expected error on Put tenant index")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestCreateUser_PutUsernameIndexFails(t *testing.T) {
	fkv := &faultKVStore{
		inner:     newMemKVStore(),
		putFailAt: 3, // primary and tenant succeed, username index fails
		putErr:    errors.New("disk full"),
	}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	_, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "alice",
		TenantID: "acme",
	})
	if err == nil {
		t.Fatal("expected error on Put username index")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestGetUser_StoreGetError(t *testing.T) {
	fkv := &faultKVStore{
		inner:     newMemKVStore(),
		getFailAt: 1,
		getErr:    errors.New("read error"),
	}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	_, err := svc.GetUser(ctx, "some-id")
	if err == nil {
		t.Fatal("expected error from store Get")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestGetUserByUsername_StoreGetError(t *testing.T) {
	fkv := &faultKVStore{
		inner:     newMemKVStore(),
		getFailAt: 1,
		getErr:    errors.New("read error"),
	}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	_, err := svc.GetUserByUsername(ctx, "acme", "alice")
	if err == nil {
		t.Fatal("expected error from store Get")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestUpdateUser_PutPrimaryFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	user := createTestUser(t, svc, "alice", "acme")

	// Now make Put fail.
	fkv.putFailAt = fkv.putCount + 1
	fkv.putErr = errors.New("disk full")

	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:      user.ID,
		RoleIDs: []string{"admin"},
	})
	if err == nil {
		t.Fatal("expected error on Put primary")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestUpdateUser_UsernameChange_GetFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	user := createTestUser(t, svc, "alice", "acme")

	// Fail on the Get for username uniqueness check.
	// After create, getCount has been used. We need the next
	// Get calls: 1 for GetUser(id), 2 for username index check.
	fkv.getFailAt = fkv.getCount + 2
	fkv.getErr = errors.New("read error")

	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:       user.ID,
		Username: "alice2",
	})
	if err == nil {
		t.Fatal("expected error on username index Get check")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestUpdateUser_UsernameChange_DeleteOldIndexFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	user := createTestUser(t, svc, "alice", "acme")

	// Fail on Delete (old username index removal).
	fkv.delFailAt = 1
	fkv.delErr = errors.New("delete error")

	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:       user.ID,
		Username: "alice2",
	})
	if err == nil {
		t.Fatal("expected error on Delete old username index")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestUpdateUser_UsernameChange_PutNewIndexFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	user := createTestUser(t, svc, "alice", "acme")

	// Fail on the next Put (new username index).
	fkv.putFailAt = fkv.putCount + 1
	fkv.putErr = errors.New("write error")

	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:       user.ID,
		Username: "alice2",
	})
	if err == nil {
		t.Fatal("expected error on Put new username index")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDeleteUser_DeletePrimaryFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	user := createTestUser(t, svc, "alice", "acme")

	fkv.delFailAt = 1
	fkv.delErr = errors.New("delete error")

	err := svc.DeleteUser(ctx, user.ID)
	if err == nil {
		t.Fatal("expected error on Delete primary")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDeleteUser_DeleteTenantIndexFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	user := createTestUser(t, svc, "alice", "acme")

	fkv.delFailAt = 2 // first Delete (primary) succeeds, second (tenant index) fails
	fkv.delErr = errors.New("delete error")

	err := svc.DeleteUser(ctx, user.ID)
	if err == nil {
		t.Fatal("expected error on Delete tenant index")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDeleteUser_DeleteUsernameIndexFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	user := createTestUser(t, svc, "alice", "acme")

	fkv.delFailAt = 3 // primary + tenant succeed, username index fails
	fkv.delErr = errors.New("delete error")

	err := svc.DeleteUser(ctx, user.ID)
	if err == nil {
		t.Fatal("expected error on Delete username index")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestListUsers_ScanFails(t *testing.T) {
	fkv := &faultKVStore{
		inner:   newMemKVStore(),
		scanErr: errors.New("scan error"),
	}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	_, err := svc.ListUsers(ctx, &ListUsersRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error on Scan")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestEnableUser_PutFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	user := createTestUser(t, svc, "alice", "acme")

	fkv.putFailAt = fkv.putCount + 1
	fkv.putErr = errors.New("write error")

	err := svc.EnableUser(ctx, user.ID)
	if err == nil {
		t.Fatal("expected error on Put")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDisableUser_PutFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	user := createTestUser(t, svc, "alice", "acme")

	fkv.putFailAt = fkv.putCount + 1
	fkv.putErr = errors.New("write error")

	err := svc.DisableUser(ctx, user.ID)
	if err == nil {
		t.Fatal("expected error on Put")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestGetUser_CorruptedData(t *testing.T) {
	mem := newMemKVStore()
	svc, _ := NewStore(mem)
	ctx := context.Background()

	// Write garbage directly to the primary key.
	_ = mem.Put(ctx, primaryKey("corrupt-id"), []byte("{invalid json"))

	_, err := svc.GetUser(ctx, "corrupt-id")
	if err == nil {
		t.Fatal("expected error for corrupted data")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
	if target.Detail != "unmarshal" {
		t.Errorf("Detail: got %q, want %q", target.Detail, "unmarshal")
	}
}

// ---------------------------------------------------------------------------
// Integration: full lifecycle
// ---------------------------------------------------------------------------

func TestUserLifecycle(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	// Create.
	user, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "lifecycle",
		TenantID: "test-tenant",
		RoleIDs:  []string{"viewer"},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Get by ID.
	fetched, err := svc.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if fetched.Username != "lifecycle" {
		t.Errorf("Username: got %q, want %q", fetched.Username, "lifecycle")
	}

	// Get by username.
	byName, err := svc.GetUserByUsername(ctx, "test-tenant", "lifecycle")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if byName.ID != user.ID {
		t.Errorf("ID mismatch: got %q, want %q", byName.ID, user.ID)
	}

	// Update.
	updated, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:       user.ID,
		Username: "lifecycle-v2",
		RoleIDs:  []string{"admin"},
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if updated.Username != "lifecycle-v2" {
		t.Errorf("Username: got %q, want %q", updated.Username, "lifecycle-v2")
	}

	// Disable.
	if err := svc.DisableUser(ctx, user.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	disabled, _ := svc.GetUser(ctx, user.ID)
	if disabled.Enabled {
		t.Error("expected disabled")
	}

	// Enable.
	if err := svc.EnableUser(ctx, user.ID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
	enabled, _ := svc.GetUser(ctx, user.ID)
	if !enabled.Enabled {
		t.Error("expected enabled")
	}

	// List.
	resp, err := svc.ListUsers(ctx, &ListUsersRequest{TenantID: "test-tenant"})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("Total: got %d, want 1", resp.Total)
	}

	// Delete.
	if err := svc.DeleteUser(ctx, user.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	// Verify deleted.
	_, err = svc.GetUser(ctx, user.ID)
	if err == nil {
		t.Fatal("expected not found after delete")
	}
}

// ---------------------------------------------------------------------------
// Quota enforcement tests
// ---------------------------------------------------------------------------

// mockUserQuotaChecker is a test double for quota.Checker.
type mockUserQuotaChecker struct {
	checkFn func(ctx context.Context, tenantID string, resource quota.Resource) error
}

func (m *mockUserQuotaChecker) Check(ctx context.Context, tenantID string, resource quota.Resource) error {
	if m.checkFn != nil {
		return m.checkFn(ctx, tenantID, resource)
	}
	return nil
}

func (m *mockUserQuotaChecker) CheckN(ctx context.Context, tenantID string, resource quota.Resource, n int64) error {
	return nil
}

func (m *mockUserQuotaChecker) Usage(ctx context.Context, tenantID string, resource quota.Resource) (int64, error) {
	return 0, nil
}

// newTestStoreWithQuota creates a kvUserStore with the given quota.Checker.
func newTestStoreWithQuota(t *testing.T, checker quota.Checker) *kvUserStore {
	t.Helper()
	s := &kvUserStore{store: newMemKVStore()}
	return s.WithQuotaChecker(checker)
}

func TestKVUserStore_CreateUser_QuotaExceeded(t *testing.T) {
	exceeded := &quota.ExceededError{
		TenantID: "acme",
		Resource: quota.ResourceUsers,
		Current:  100,
		Limit:    100,
	}
	checker := &mockUserQuotaChecker{
		checkFn: func(_ context.Context, _ string, _ quota.Resource) error {
			return exceeded
		},
	}

	svc := newTestStoreWithQuota(t, checker)
	ctx := context.Background()

	_, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "blocked",
		TenantID: "acme",
	})
	if err == nil {
		t.Fatal("CreateUser() returned nil, want *quota.ExceededError")
	}
	var qErr *quota.ExceededError
	if !errors.As(err, &qErr) {
		t.Fatalf("CreateUser() error type = %T, want *quota.ExceededError", err)
	}
	if qErr.Resource != quota.ResourceUsers {
		t.Errorf("ExceededError.Resource = %v, want ResourceUsers", qErr.Resource)
	}
}

func TestKVUserStore_CreateUser_NoQuotaChecker(t *testing.T) {
	// Without a quota checker, creation must succeed normally.
	svc := newTestStore(t)
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "noquota",
		TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("CreateUser() returned unexpected error: %v", err)
	}
	if user.Username != "noquota" {
		t.Errorf("User.Username = %q, want %q", user.Username, "noquota")
	}
}

func TestKVUserStore_CreateUser_QuotaPasses(t *testing.T) {
	var checkedTenantID string
	var checkedResource quota.Resource
	checker := &mockUserQuotaChecker{
		checkFn: func(_ context.Context, tenantID string, resource quota.Resource) error {
			checkedTenantID = tenantID
			checkedResource = resource
			return nil // quota allows creation
		},
	}

	svc := newTestStoreWithQuota(t, checker)
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "allowed",
		TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("CreateUser() returned unexpected error: %v", err)
	}
	if user.Username != "allowed" {
		t.Errorf("User.Username = %q, want %q", user.Username, "allowed")
	}
	if checkedTenantID != "acme" {
		t.Errorf("quota.Check() tenantID = %q, want %q", checkedTenantID, "acme")
	}
	if checkedResource != quota.ResourceUsers {
		t.Errorf("quota.Check() resource = %v, want ResourceUsers", checkedResource)
	}
}

func TestKVUserStore_WithQuotaChecker_DefaultTenant(t *testing.T) {
	// When TenantID is empty the store falls back to DefaultTenantID.
	var checkedTenantID string
	checker := &mockUserQuotaChecker{
		checkFn: func(_ context.Context, tenantID string, _ quota.Resource) error {
			checkedTenantID = tenantID
			return nil
		},
	}

	svc := newTestStoreWithQuota(t, checker)
	ctx := context.Background()

	_, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "default-tenant-user",
		TenantID: "", // empty → should default
	})
	if err != nil {
		t.Fatalf("CreateUser() returned unexpected error: %v", err)
	}
	if checkedTenantID != tenant.DefaultTenantID {
		t.Errorf("quota.Check() tenantID = %q, want %q", checkedTenantID, tenant.DefaultTenantID)
	}
}
