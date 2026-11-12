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

package role

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// memKVStore: in-memory KVStore for unit testing
// ---------------------------------------------------------------------------

type memKVStore struct {
	data sync.Map
	// mu guards PutAllIfUnchanged, CompareAndDelete and CompareAndSwap (see
	// mem_kvstore_atomic_test.go): sync.Map gives per-key atomicity, not the
	// check-many-then-write-many atomicity a guarded batch needs, and its own
	// compare verbs cannot compare a []byte. Put and Delete take it too, so
	// no plain write lands between one of those checks and its write.
	mu sync.Mutex
}

func newMemKVStore() *memKVStore {
	return &memKVStore{}
}

func (m *memKVStore) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	m.mu.Lock()
	defer m.mu.Unlock()
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
// faultKVStore: wraps memKVStore and fails after N successful calls
// ---------------------------------------------------------------------------

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
	// claimLostErr, when set, makes PutIfAbsent COMMIT and then return it:
	// a claim whose answer was lost after it landed ("it may still commit").
	claimLostErr error
	// onClaim, when set, runs at the start of PutIfAbsent: another create
	// acting between a create's name lookup and its claim.
	onClaim func()
}

var _ kvstore.AtomicKVStore = (*faultKVStore)(nil)

// PutIfAbsent implements kvstore.AtomicKVStore. It is a write, so it counts
// against putFailAt exactly as Put does.
func (f *faultKVStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if f.onClaim != nil {
		f.onClaim()
	}
	f.putCount++
	if f.putFailAt > 0 && f.putCount >= f.putFailAt {
		return false, f.putErr
	}
	stored, err := f.inner.PutIfAbsent(ctx, key, value)
	if err == nil && f.claimLostErr != nil {
		return false, f.claimLostErr
	}
	return stored, err
}

// CompareAndDelete implements kvstore.AtomicKVStore. It is a delete, so it
// counts against delFailAt exactly as Delete does.
func (f *faultKVStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	f.delCount++
	if f.delFailAt > 0 && f.delCount >= f.delFailAt {
		return false, f.delErr
	}
	return f.inner.CompareAndDelete(ctx, key, expected)
}

// CompareAndSwap implements kvstore.AtomicKVStore.
func (f *faultKVStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return f.inner.CompareAndSwap(ctx, key, expected, next)
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

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestStore(t *testing.T) RoleServicer {
	t.Helper()
	store, err := NewStore(newMemKVStore(), WithHolderRevoker(noHolders{}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func createTestRole(t *testing.T, svc RoleServicer, name, tenantID string) *Role {
	t.Helper()
	ctx := context.Background()
	r, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        name,
		TenantID:    tenantID,
		Permissions: []permission.Permission{permission.KVGet, permission.KVPut},
	})
	if err != nil {
		t.Fatalf("CreateRole(%s): %v", name, err)
	}
	return r
}

// ---------------------------------------------------------------------------
// NewStore
// ---------------------------------------------------------------------------

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
// CreateRole
// ---------------------------------------------------------------------------

func TestCreateRole_Success(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	r, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        "editor",
		TenantID:    "acme",
		Permissions: []permission.Permission{permission.KVPut, permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if r.ID == "" {
		t.Error("expected non-empty ID")
	}
	if r.Name != "editor" {
		t.Errorf("Name: got %q, want %q", r.Name, "editor")
	}
	if r.TenantID != "acme" {
		t.Errorf("TenantID: got %q, want %q", r.TenantID, "acme")
	}
	if len(r.Permissions) != 2 {
		t.Errorf("Permissions: got %d, want 2", len(r.Permissions))
	}
	if r.System {
		t.Error("expected System=false")
	}
	if r.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
	if r.UpdatedAt.IsZero() {
		t.Error("expected non-zero UpdatedAt")
	}
}

func TestCreateRole_DuplicateName(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createTestRole(t, svc, "editor", "acme")

	_, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:     "editor",
		TenantID: "acme",
	})
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}
	var target *RoleNameExistsError
	if !errors.As(err, &target) {
		t.Fatalf("expected *RoleNameExistsError, got %T: %v", err, err)
	}
	if target.Name != "editor" {
		t.Errorf("Name: got %q, want %q", target.Name, "editor")
	}
}

func TestCreateRole_SameNameDifferentTenant(t *testing.T) {
	svc := newTestStore(t)

	r1 := createTestRole(t, svc, "editor", "tenant-a")
	r2 := createTestRole(t, svc, "editor", "tenant-b")

	if r1.ID == r2.ID {
		t.Error("roles in different tenants should have different IDs")
	}
}

func TestCreateRole_EmptyName(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:     "",
		TenantID: "acme",
	})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if target.Field != "name" {
		t.Errorf("Field: got %q, want %q", target.Field, "name")
	}
}

func TestCreateRole_NilRequest(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.CreateRole(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestCreateRole_DefaultTenant(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	r, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        "viewer",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if r.TenantID != tenantid.System {
		t.Errorf("TenantID: got %q, want %q", r.TenantID, tenantid.System)
	}
}

func TestCreateRole_NilPermissions(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	r, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        "empty-perms",
		TenantID:    "acme",
		Permissions: nil,
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if r.Permissions == nil {
		t.Fatal("expected non-nil Permissions (empty slice)")
	}
	if len(r.Permissions) != 0 {
		t.Errorf("Permissions length: got %d, want 0", len(r.Permissions))
	}
}

func TestCreateRole_InvalidPermission(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        "bad-perms",
		TenantID:    "acme",
		Permissions: []permission.Permission{"nonexistent:perm"},
	})
	if err == nil {
		t.Fatal("expected error for invalid permission")
	}
	var target *InvalidPermissionError
	if !errors.As(err, &target) {
		t.Fatalf("expected *InvalidPermissionError, got %T: %v", err, err)
	}
	if target.Permission != "nonexistent:perm" {
		t.Errorf("Permission: got %q, want %q", target.Permission, "nonexistent:perm")
	}
}

func TestCreateRole_StoreGetError(t *testing.T) {
	eStore := &errKVStore{getErr: errors.New("disk fault")}
	svc, err := NewStore(eStore)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	_, err = svc.CreateRole(ctx, &CreateRoleRequest{
		Name:     "editor",
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
// GetRole
// ---------------------------------------------------------------------------

func TestGetRole_Found(t *testing.T) {
	svc := newTestStore(t)
	created := createTestRole(t, svc, "editor", "acme")
	ctx := context.Background()

	fetched, err := svc.GetRole(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if fetched.ID != created.ID {
		t.Errorf("ID: got %q, want %q", fetched.ID, created.ID)
	}
	if fetched.Name != "editor" {
		t.Errorf("Name: got %q, want %q", fetched.Name, "editor")
	}
}

func TestGetRole_NotFound(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.GetRole(ctx, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent role")
	}
	var target *RoleNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("expected *RoleNotFoundError, got %T: %v", err, err)
	}
}

func TestGetRole_TenantScoping(t *testing.T) {
	svc := newTestStore(t)
	created := createTestRole(t, svc, "editor", "tenant-a")

	// A caller scoped to a different tenant must not be able to read the role
	// (returns NotFound, not the record — no existence leak).
	otherCtx := tenant.WithTenantID(context.Background(), "tenant-b")
	if _, err := svc.GetRole(otherCtx, created.ID); err == nil {
		t.Fatal("expected RoleNotFoundError for cross-tenant GetRole")
	} else {
		var nf *RoleNotFoundError
		if !errors.As(err, &nf) {
			t.Fatalf("expected *RoleNotFoundError, got %T: %v", err, err)
		}
	}

	// The owning tenant can read its own role.
	ownCtx := tenant.WithTenantID(context.Background(), "tenant-a")
	if r, err := svc.GetRole(ownCtx, created.ID); err != nil {
		t.Fatalf("owning-tenant GetRole: %v", err)
	} else if r.ID != created.ID {
		t.Errorf("ID: got %q, want %q", r.ID, created.ID)
	}

	// The system tenant (admin / internal RBAC resolution) reads any role.
	if _, err := svc.GetRole(context.Background(), created.ID); err != nil {
		t.Fatalf("system GetRole: %v", err)
	}

	// ResolvePermissions funnels through GetRole, so it is scoped too.
	if _, err := svc.ResolvePermissions(otherCtx, []string{created.ID}); err == nil {
		t.Fatal("expected error for cross-tenant ResolvePermissions")
	}
	if perms, err := svc.ResolvePermissions(ownCtx, []string{created.ID}); err != nil {
		t.Fatalf("owning-tenant ResolvePermissions: %v", err)
	} else if len(perms) == 0 {
		t.Error("expected resolved permissions for owning tenant")
	}
}

func TestGetRole_EmptyID(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.GetRole(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetRoleByName
// ---------------------------------------------------------------------------

func TestGetRoleByName_Found(t *testing.T) {
	svc := newTestStore(t)
	created := createTestRole(t, svc, "editor", "acme")
	ctx := context.Background()

	fetched, err := svc.GetRoleByName(ctx, "acme", "editor")
	if err != nil {
		t.Fatalf("GetRoleByName: %v", err)
	}
	if fetched.ID != created.ID {
		t.Errorf("ID: got %q, want %q", fetched.ID, created.ID)
	}
}

func TestGetRoleByName_NotFound(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.GetRoleByName(ctx, "acme", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent role name")
	}
	var target *RoleNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("expected *RoleNotFoundError, got %T: %v", err, err)
	}
}

func TestGetRoleByName_EmptyName(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.GetRoleByName(ctx, "acme", "")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestGetRoleByName_DefaultTenant(t *testing.T) {
	svc := newTestStore(t)
	created := createTestRole(t, svc, "viewer", "")
	ctx := context.Background()

	fetched, err := svc.GetRoleByName(ctx, "", "viewer")
	if err != nil {
		t.Fatalf("GetRoleByName: %v", err)
	}
	if fetched.ID != created.ID {
		t.Errorf("ID: got %q, want %q", fetched.ID, created.ID)
	}
}

// ---------------------------------------------------------------------------
// UpdateRole
// ---------------------------------------------------------------------------

func TestUpdateRole_Success(t *testing.T) {
	svc := newTestStore(t)
	created := createTestRole(t, svc, "editor", "acme")
	ctx := context.Background()

	updated, err := svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID:          created.ID,
		Permissions: []permission.Permission{permission.KVGet, permission.KVList, permission.KVRange},
	})
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if len(updated.Permissions) != 3 {
		t.Errorf("Permissions: got %d, want 3", len(updated.Permissions))
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) && !updated.UpdatedAt.Equal(created.UpdatedAt) {
		t.Error("expected UpdatedAt to be >= CreatedAt")
	}
}

func TestUpdateRole_NameChange(t *testing.T) {
	svc := newTestStore(t)
	created := createTestRole(t, svc, "editor", "acme")
	ctx := context.Background()

	updated, err := svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID:   created.ID,
		Name: "editor-v2",
	})
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if updated.Name != "editor-v2" {
		t.Errorf("Name: got %q, want %q", updated.Name, "editor-v2")
	}

	// Old name should no longer resolve.
	_, err = svc.GetRoleByName(ctx, "acme", "editor")
	if err == nil {
		t.Fatal("expected error for old name")
	}
	var target *RoleNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("expected *RoleNotFoundError, got %T: %v", err, err)
	}

	// New name should resolve.
	fetched, err := svc.GetRoleByName(ctx, "acme", "editor-v2")
	if err != nil {
		t.Fatalf("GetRoleByName(editor-v2): %v", err)
	}
	if fetched.ID != created.ID {
		t.Errorf("ID: got %q, want %q", fetched.ID, created.ID)
	}
}

func TestUpdateRole_NotFound(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID:   "nonexistent-id",
		Name: "new-name",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent role")
	}
	var target *RoleNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("expected *RoleNotFoundError, got %T: %v", err, err)
	}
}

func TestUpdateRole_DuplicateName(t *testing.T) {
	svc := newTestStore(t)
	createTestRole(t, svc, "editor", "acme")
	viewer := createTestRole(t, svc, "viewer", "acme")
	ctx := context.Background()

	_, err := svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID:   viewer.ID,
		Name: "editor",
	})
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}
	var target *RoleNameExistsError
	if !errors.As(err, &target) {
		t.Fatalf("expected *RoleNameExistsError, got %T: %v", err, err)
	}
}

func TestUpdateRole_NilRequest(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.UpdateRole(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestUpdateRole_EmptyID(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.UpdateRole(ctx, &UpdateRoleRequest{ID: ""})
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestUpdateRole_NilPermissionsNoChange(t *testing.T) {
	svc := newTestStore(t)
	created := createTestRole(t, svc, "editor", "acme")
	ctx := context.Background()

	updated, err := svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID:          created.ID,
		Permissions: nil, // no change
	})
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if len(updated.Permissions) != 2 {
		t.Errorf("Permissions: got %d, want 2", len(updated.Permissions))
	}
}

func TestUpdateRole_SameNameNoOp(t *testing.T) {
	svc := newTestStore(t)
	created := createTestRole(t, svc, "editor", "acme")
	ctx := context.Background()

	// Updating with the same name should not error.
	updated, err := svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID:   created.ID,
		Name: "editor",
	})
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if updated.Name != "editor" {
		t.Errorf("Name: got %q, want %q", updated.Name, "editor")
	}
}

func TestUpdateRole_InvalidPermission(t *testing.T) {
	svc := newTestStore(t)
	created := createTestRole(t, svc, "editor", "acme")
	ctx := context.Background()

	_, err := svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID:          created.ID,
		Permissions: []permission.Permission{"bad:perm"},
	})
	if err == nil {
		t.Fatal("expected error for invalid permission")
	}
	var target *InvalidPermissionError
	if !errors.As(err, &target) {
		t.Fatalf("expected *InvalidPermissionError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DeleteRole
// ---------------------------------------------------------------------------

func TestDeleteRole_Success(t *testing.T) {
	svc := newTestStore(t)
	created := createTestRole(t, svc, "editor", "acme")
	ctx := context.Background()

	if err := svc.DeleteRole(ctx, created.ID); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}

	// Should no longer be found by ID.
	_, err := svc.GetRole(ctx, created.ID)
	if err == nil {
		t.Fatal("expected role not found after delete")
	}
	var nf *RoleNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected *RoleNotFoundError, got %T: %v", err, err)
	}

	// Should no longer be found by name.
	_, err = svc.GetRoleByName(ctx, "acme", "editor")
	if err == nil {
		t.Fatal("expected name not found after delete")
	}
}

func TestDeleteRole_NotFound(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	err := svc.DeleteRole(ctx, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent role")
	}
	var target *RoleNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("expected *RoleNotFoundError, got %T: %v", err, err)
	}
}

func TestDeleteRole_SystemRole(t *testing.T) {
	kv := newMemKVStore()
	svc, _ := NewStore(kv)
	ctx := context.Background()

	// Create an ordinary role then mark it as system directly in KV.
	//
	// This exercises DeleteRole's guard against a hand-set flag, which is a
	// weaker statement than it looks: it passed throughout the entire lifetime
	// of go-qrdb#17 precisely because it sets the flag itself. The real
	// coverage is TestSeededSystemRoles_CannotBeDeleted, which seeds through
	// the bootstrap path. The name is non-reserved because a canonical one is
	// now refused on the ordinary create path.
	r := createTestRole(t, svc, "manager", "acme")

	// Fetch, modify to system, and re-store.
	fetched, _ := svc.GetRole(ctx, r.ID)
	fetched.System = true

	data, _ := json.Marshal(fetched)
	_ = kv.Put(ctx, primaryKey(fetched.ID), data)

	err := svc.DeleteRole(ctx, r.ID)
	if err == nil {
		t.Fatal("expected error for system role")
	}
	var target *SystemRoleImmutableError
	if !errors.As(err, &target) {
		t.Fatalf("expected *SystemRoleImmutableError, got %T: %v", err, err)
	}
}

func TestDeleteRole_EmptyID(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	err := svc.DeleteRole(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestDeleteRole_ThenList(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	r1 := createTestRole(t, svc, "editor", "acme")
	createTestRole(t, svc, "viewer", "acme")

	if err := svc.DeleteRole(ctx, r1.ID); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}

	resp, err := svc.ListRoles(ctx, &ListRolesRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("Total: got %d, want 1", resp.Total)
	}
	if resp.Roles[0].Name != "viewer" {
		t.Errorf("remaining role: got %q, want %q", resp.Roles[0].Name, "viewer")
	}
}

// ---------------------------------------------------------------------------
// ListRoles
// ---------------------------------------------------------------------------

func TestListRoles_Empty(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	resp, err := svc.ListRoles(ctx, &ListRolesRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if resp.Total != 0 {
		t.Errorf("Total: got %d, want 0", resp.Total)
	}
	if len(resp.Roles) != 0 {
		t.Errorf("Roles: got %d, want 0", len(resp.Roles))
	}
	if resp.HasMore {
		t.Error("expected HasMore=false")
	}
}

func TestListRoles_ByTenant(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createTestRole(t, svc, "editor", "acme")
	createTestRole(t, svc, "viewer", "acme")
	createTestRole(t, svc, "manager", "globex")

	respAcme, err := svc.ListRoles(ctx, &ListRolesRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("ListRoles(acme): %v", err)
	}
	if respAcme.Total != 2 {
		t.Errorf("acme Total: got %d, want 2", respAcme.Total)
	}

	respGlobex, err := svc.ListRoles(ctx, &ListRolesRequest{TenantID: "globex"})
	if err != nil {
		t.Fatalf("ListRoles(globex): %v", err)
	}
	if respGlobex.Total != 1 {
		t.Errorf("globex Total: got %d, want 1", respGlobex.Total)
	}
}

func TestListRoles_Pagination(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createTestRole(t, svc, "editor", "acme")
	createTestRole(t, svc, "viewer", "acme")
	createTestRole(t, svc, "manager", "acme")

	// Page 1: size 2
	resp1, err := svc.ListRoles(ctx, &ListRolesRequest{
		TenantID: "acme",
		Page:     1,
		PageSize: 2,
	})
	if err != nil {
		t.Fatalf("ListRoles page 1: %v", err)
	}
	if len(resp1.Roles) != 2 {
		t.Errorf("page 1 Roles: got %d, want 2", len(resp1.Roles))
	}
	if resp1.Total != 3 {
		t.Errorf("page 1 Total: got %d, want 3", resp1.Total)
	}
	if !resp1.HasMore {
		t.Error("expected HasMore=true for page 1")
	}

	// Page 2: size 2
	resp2, err := svc.ListRoles(ctx, &ListRolesRequest{
		TenantID: "acme",
		Page:     2,
		PageSize: 2,
	})
	if err != nil {
		t.Fatalf("ListRoles page 2: %v", err)
	}
	if len(resp2.Roles) != 1 {
		t.Errorf("page 2 Roles: got %d, want 1", len(resp2.Roles))
	}
}

func TestListRoles_DefaultTenant(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createTestRole(t, svc, "editor", "")

	resp, err := svc.ListRoles(ctx, &ListRolesRequest{TenantID: ""})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("Total: got %d, want 1", resp.Total)
	}
}

func TestListRoles_NilRequest(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.ListRoles(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestListRoles_DefaultPageParams(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createTestRole(t, svc, "editor", "acme")

	resp, err := svc.ListRoles(ctx, &ListRolesRequest{
		TenantID: "acme",
		Page:     0,
		PageSize: 0,
	})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("Total: got %d, want 1", resp.Total)
	}
}

func TestListRoles_PageBeyondTotal(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createTestRole(t, svc, "editor", "acme")

	resp, err := svc.ListRoles(ctx, &ListRolesRequest{
		TenantID: "acme",
		Page:     100,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if len(resp.Roles) != 0 {
		t.Errorf("Roles: got %d, want 0", len(resp.Roles))
	}
	if resp.Total != 1 {
		t.Errorf("Total: got %d, want 1", resp.Total)
	}
}

// ---------------------------------------------------------------------------
// ResolvePermissions
// ---------------------------------------------------------------------------

func TestResolvePermissions_MultipleRoles(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	r1, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        "kv-writer",
		TenantID:    "acme",
		Permissions: []permission.Permission{permission.KVPut, permission.KVDelete},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	r2, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        "kv-reader",
		TenantID:    "acme",
		Permissions: []permission.Permission{permission.KVGet, permission.KVList},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	perms, err := svc.ResolvePermissions(ctx, []string{r1.ID, r2.ID})
	if err != nil {
		t.Fatalf("ResolvePermissions: %v", err)
	}
	if len(perms) != 4 {
		t.Errorf("Permissions: got %d, want 4", len(perms))
	}

	// Verify sorted.
	if !sort.SliceIsSorted(perms, func(i, j int) bool { return perms[i] < perms[j] }) {
		t.Error("expected sorted permissions")
	}
}

func TestResolvePermissions_Dedup(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	// Both roles have KVGet in common.
	r1, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        "role-a",
		TenantID:    "acme",
		Permissions: []permission.Permission{permission.KVGet, permission.KVPut},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	r2, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        "role-b",
		TenantID:    "acme",
		Permissions: []permission.Permission{permission.KVGet, permission.KVDelete},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	perms, err := svc.ResolvePermissions(ctx, []string{r1.ID, r2.ID})
	if err != nil {
		t.Fatalf("ResolvePermissions: %v", err)
	}

	// KVGet, KVPut, KVDelete = 3 unique.
	if len(perms) != 3 {
		t.Errorf("Permissions: got %d, want 3 (deduplicated)", len(perms))
	}
}

func TestResolvePermissions_EmptyRoleIDs(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	perms, err := svc.ResolvePermissions(ctx, []string{})
	if err != nil {
		t.Fatalf("ResolvePermissions: %v", err)
	}
	if len(perms) != 0 {
		t.Errorf("Permissions: got %d, want 0", len(perms))
	}
}

func TestResolvePermissions_RoleNotFound(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.ResolvePermissions(ctx, []string{"nonexistent-id"})
	if err == nil {
		t.Fatal("expected error for nonexistent role")
	}
	var target *RoleNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("expected *RoleNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// BuiltInRoles
// ---------------------------------------------------------------------------

func TestBuiltInRoles_Count(t *testing.T) {
	roles := BuiltInRoles("acme")
	// Canonical six-role FIPS 140-3 model: SO, Admin, Operator, User,
	// Auditor, Custodian. (No platform-admin role; that authority comes
	// the canonical platform-admin pattern is Admin@system, gated by the
	// system-tenant rule in pkg/auth/rbac.
	if len(roles) != 5 {
		t.Fatalf("BuiltInRoles: got %d roles, want 6", len(roles))
	}
}

func TestBuiltInRoles_Names(t *testing.T) {
	roles := BuiltInRoles("acme")
	// Canonical five-role FIPS 140-3 model.
	expected := []string{
		RoleNameSO, RoleNameAdmin, RoleNameOperator,
		RoleNameUser, RoleNameAuditor,
	}
	for i, name := range expected {
		if roles[i].Name != name {
			t.Errorf("role[%d].Name: got %q, want %q", i, roles[i].Name, name)
		}
	}
}

func TestBuiltInRoles_AllSystem(t *testing.T) {
	for _, r := range BuiltInRoles("acme") {
		if !r.System {
			t.Errorf("role %q: expected System=true", r.Name)
		}
	}
}

func TestBuiltInRoles_TenantID(t *testing.T) {
	for _, r := range BuiltInRoles("globex") {
		if r.TenantID != "globex" {
			t.Errorf("role %q: TenantID: got %q, want %q", r.Name, r.TenantID, "globex")
		}
	}
}

func TestBuiltInRoles_DefaultTenant(t *testing.T) {
	roles := BuiltInRoles("")
	for _, r := range roles {
		if r.TenantID != tenantid.System {
			t.Errorf("role %q: TenantID: got %q, want %q", r.Name, r.TenantID, tenantid.System)
		}
	}
}

func TestBuiltInRoles_DifferentTenantsDifferentIDs(t *testing.T) {
	rolesA := BuiltInRoles("tenant-a")
	rolesB := BuiltInRoles("tenant-b")
	for i := range rolesA {
		if rolesA[i].ID == rolesB[i].ID {
			t.Errorf("role %q: same ID across tenants", rolesA[i].Name)
		}
	}
}

// roleByName is a test helper that locates a role by name in the slice.
// ---------------------------------------------------------------------------
// Broker data-plane grant matrix (tenant-admin)
// ---------------------------------------------------------------------------

// brokerFamilyWildcards lists the eight broker data-plane resource families
// the Admin (tenant-admin) role must hold as resource:* whole-segment
// wildcards so a human tenant-admin's USER token resolves data-plane scopes.
var brokerFamilyWildcards = []permission.Permission{
	permission.ObjStoreAll,
	permission.OrchestratorAll,
	permission.SchedulerAll,
	permission.MessagingAll,
	permission.ParametersAll,
	permission.DynamicSecretsAll,
	permission.ProjectAll,
	permission.OllamaAll,
}

// adminPermSet resolves the Admin built-in role permissions into a set for
// membership assertions.
func adminPermSet(t *testing.T) map[permission.Permission]struct{} {
	t.Helper()
	admin := roleByName(BuiltInRoles("acme"), RoleNameAdmin)
	if admin == nil {
		t.Fatal("admin role not found in BuiltInRoles")
	}
	set := make(map[permission.Permission]struct{}, len(admin.Permissions))
	for _, p := range admin.Permissions {
		set[p] = struct{}{}
	}
	return set
}

// wildcardSatisfies mirrors the RBAC authorizer's whole-segment wildcard match
// (pkg/auth/rbac.permissionGranted): a granted resource:* covers any concrete
// resource:action in that family. It is duplicated here only to assert the
// grant's effect without importing the rbac package (which would create an
// import cycle through role).
func wildcardSatisfies(grants map[permission.Permission]struct{}, required permission.Permission) bool {
	if _, ok := grants[required]; ok {
		return true
	}
	res := permission.Resource(required)
	act := permission.Action(required)
	for _, cand := range []permission.Permission{
		permission.Permission(res + ":*"),
		permission.Permission("*:" + act),
		permission.Permission("*:*"),
	} {
		if _, ok := grants[cand]; ok {
			return true
		}
	}
	return false
}

// TestBuiltInRoles_AdminWildcardsResolveConcreteDataPlane verifies the
// tenant-admin wildcard grants actually satisfy concrete data-plane
// resource:action checks (the operation the broker PEP enforces). Without the
// Stage grant, every one of these resolved to a deny.
func TestBuiltInRoles_AdminWildcardsResolveConcreteDataPlane(t *testing.T) {
	set := adminPermSet(t)
	concrete := []permission.Permission{
		permission.ObjStorePut, permission.ObjStoreGet, permission.ObjStoreAdmin,
		permission.OrchestratorSpawn, permission.OrchestratorStatus,
		permission.SchedulerCreate, permission.SchedulerRead,
		permission.MessagingPublish, permission.MessagingSubscribe,
		permission.ParametersRead, permission.ParametersWrite,
		permission.DynamicSecretsGenerate, permission.DynamicSecretsRoleCreate,
		permission.ProjectRead, permission.ProjectList,
		permission.OllamaEmbed, permission.OllamaModelList,
	}
	for _, p := range concrete {
		if !wildcardSatisfies(set, p) {
			t.Errorf("admin wildcard grants do not satisfy concrete %q", p)
		}
	}
}

// TestBuiltInRoles_AdminBrokerGrantsValidate verifies the wildcard grants on
// the Admin role are accepted by validatePermissions (the same gate
// CreateRole/UpdateRole apply), proving they are legal role grants even though
// they are not registered as concrete permissions.
func TestBuiltInRoles_AdminBrokerGrantsValidate(t *testing.T) {
	admin := roleByName(BuiltInRoles("acme"), RoleNameAdmin)
	if admin == nil {
		t.Fatal("admin role not found")
	}
	if err := validatePermissions(admin.Permissions); err != nil {
		t.Fatalf("admin permissions failed validation: %v", err)
	}
	for _, w := range brokerFamilyWildcards {
		if !permission.IsWildcard(w) {
			t.Errorf("expected %q to be a whole-segment wildcard", w)
		}
		if permission.IsRegistered(w) {
			t.Errorf("wildcard %q must NOT be registered as a concrete permission", w)
		}
	}
}

// TestBrokerDataPlaneConcretePermsAreTenantOnly verifies the concrete
// broker data-plane permissions remain ScopeTenantOnly. This is the
// safety property that keeps the Admin wildcard grants from leaking across
// tenants or to the system tenant: the authorizer resolves scope against the
// concrete requested permission, not the wildcard grant.
func TestBrokerDataPlaneConcretePermsAreTenantOnly(t *testing.T) {
	concrete := []permission.Permission{
		permission.ObjStorePut, permission.ObjStoreAdmin,
		permission.OrchestratorSpawn, permission.SchedulerCreate,
		permission.MessagingPublish, permission.ParametersWrite,
		permission.DynamicSecretsGenerate, permission.DynamicSecretsRoleCreate,
		permission.ProjectRead, permission.OllamaEmbed,
	}
	for _, p := range concrete {
		got, err := permission.LookupScope(p)
		if err != nil {
			t.Fatalf("scope for %q: %v", p, err)
		}
		if got != permission.ScopeTenantOnly {
			t.Errorf("scope for %q: got %v, want ScopeTenantOnly", p, got)
		}
	}
}

// TestNoDataUserRole documents the RESOLVED decision: no dedicated "data-user"
// built-in role exists in qrdb. The day-to-day broker data-plane role is the
// existing Operator role — it carries the non-admin verbs of all eight broker
// data-plane families (objstore, orchestrator, scheduler, messaging,
// parameters, dynamic_secrets, project, ollama), while the Admin role keeps the
// full families via resource:* wildcards. There is therefore no need for a
// separate data-user row in the grant matrix. This guards against a future
// regression that silently reintroduces a data-user role (which would
// duplicate Operator's data-plane grant) and confirms Operator actually carries
// the data-plane families.
func TestNoDataUserRole(t *testing.T) {
	roles := BuiltInRoles("acme")
	for _, r := range roles {
		if r.Name == "data-user" || r.Name == "data_user" {
			t.Fatalf("unexpected data-user role present: %q; the day-to-day"+
				" data-plane role is Operator, not a dedicated data-user", r.Name)
		}
	}
	if OwnerRole("acme").Name == "data-user" {
		t.Fatal("owner role unexpectedly named data-user")
	}

	// The Operator role IS the resolved day-to-day data-plane carrier: it must
	// hold at least one concrete verb from each broker data-plane family.
	operator := roleByName(roles, RoleNameOperator)
	if operator == nil {
		t.Fatal("operator role not found")
	}
	permSet := make(map[permission.Permission]struct{}, len(operator.Permissions))
	for _, p := range operator.Permissions {
		permSet[p] = struct{}{}
	}
	familySentinels := []permission.Permission{
		permission.ObjStoreGet,
		permission.OrchestratorSpawn,
		permission.SchedulerCreate,
		permission.MessagingPublish,
		permission.ParametersRead,
		permission.DynamicSecretsGenerate,
		permission.ProjectRead,
		permission.OllamaEmbed,
	}
	for _, p := range familySentinels {
		if _, ok := permSet[p]; !ok {
			t.Errorf("operator missing day-to-day data-plane permission: %q", p)
		}
	}
}

func roleByName(roles []*Role, name string) *Role {
	for _, r := range roles {
		if r.Name == name {
			return r
		}
	}
	return nil
}

// TestSystemRoleID_Deterministic verifies that SystemRoleID produces stable,
// unique UUIDs for all six canonical roles across multiple calls.
func TestSystemRoleID_Deterministic(t *testing.T) {
	tenant := "test-tenant"
	roleNames := []string{
		RoleNameSO, RoleNameAdmin, RoleNameOperator,
		RoleNameUser, RoleNameAuditor,
	}

	// Verify determinism: same output on repeated calls.
	for _, name := range roleNames {
		id1 := SystemRoleID(tenant, name)
		id2 := SystemRoleID(tenant, name)
		if id1 != id2 {
			t.Errorf("SystemRoleID(%q, %q) not deterministic: %q vs %q", tenant, name, id1, id2)
		}
	}

	// Verify uniqueness: all six IDs are distinct.
	ids := make(map[string]string, len(roleNames))
	for _, name := range roleNames {
		id := SystemRoleID(tenant, name)
		for existingName, existingID := range ids {
			if existingID == id {
				t.Errorf("SystemRoleID collision: %q and %q both produce %q", name, existingName, id)
			}
		}
		ids[name] = id
	}
}

func TestBuiltInRoles_AllPermissionsRegistered(t *testing.T) {
	// Every built-in role permission must be either a concrete registered
	// permission OR a whole-segment wildcard (e.g. "objstore:*"). Wildcards are
	// intentionally NOT registered as concrete permissions; the RBAC authorizer
	// expands them against concrete resource:action checks, and
	// validatePermissions admits them as legal grants. The Admin (tenant-admin)
	// role carries the broker data-plane families as resource:* wildcards.
	for _, r := range BuiltInRoles("acme") {
		for _, p := range r.Permissions {
			if permission.IsWildcard(p) {
				continue
			}
			if !permission.IsRegistered(p) {
				t.Errorf("role %q has unregistered permission: %q", r.Name, p)
			}
		}
	}
}

func TestBuiltInRoles_PermissionsSorted(t *testing.T) {
	for _, r := range BuiltInRoles("acme") {
		if !sort.SliceIsSorted(r.Permissions, func(i, j int) bool {
			return r.Permissions[i] < r.Permissions[j]
		}) {
			t.Errorf("role %q: permissions not sorted", r.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Key functions
// ---------------------------------------------------------------------------

func TestPrimaryKey(t *testing.T) {
	key := primaryKey("abc-123")
	want := "__sys:roles/id/abc-123"
	if string(key) != want {
		t.Errorf("primaryKey: got %q, want %q", key, want)
	}
}

func TestTenantIndexKey(t *testing.T) {
	key := tenantIndexKey("acme", "abc-123")
	want := "__sys:roles/tenant/acme/abc-123"
	if string(key) != want {
		t.Errorf("tenantIndexKey: got %q, want %q", key, want)
	}
}

func TestNameIndexKey(t *testing.T) {
	key := nameIndexKey("acme", "editor")
	want := "__sys:role_idx/name/acme/editor"
	if string(key) != want {
		t.Errorf("nameIndexKey: got %q, want %q", key, want)
	}
}

// ---------------------------------------------------------------------------
// validatePermissions
// ---------------------------------------------------------------------------

func TestValidatePermissions_AllValid(t *testing.T) {
	err := validatePermissions([]permission.Permission{
		permission.KVPut, permission.KVGet, permission.AdminHealth,
	})
	if err != nil {
		t.Fatalf("validatePermissions: unexpected error: %v", err)
	}
}

func TestValidatePermissions_Invalid(t *testing.T) {
	err := validatePermissions([]permission.Permission{
		permission.KVPut, "fake:perm",
	})
	if err == nil {
		t.Fatal("expected error for invalid permission")
	}
	var target *InvalidPermissionError
	if !errors.As(err, &target) {
		t.Fatalf("expected *InvalidPermissionError, got %T: %v", err, err)
	}
	if target.Permission != "fake:perm" {
		t.Errorf("Permission: got %q, want %q", target.Permission, "fake:perm")
	}
}

func TestValidatePermissions_Empty(t *testing.T) {
	err := validatePermissions([]permission.Permission{})
	if err != nil {
		t.Fatalf("validatePermissions(empty): unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Fault injection: Store error paths
// ---------------------------------------------------------------------------

func TestCreateRole_PutPrimaryFails(t *testing.T) {
	fkv := &faultKVStore{
		inner:     newMemKVStore(),
		putFailAt: 2, // name claim(1) succeeds, primary(2) fails
		putErr:    errors.New("disk full"),
	}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	_, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:     "editor",
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

func TestCreateRole_PutTenantIndexFails(t *testing.T) {
	fkv := &faultKVStore{
		inner:     newMemKVStore(),
		putFailAt: 3, // name claim(1) and primary(2) succeed, tenant index(3) fails
		putErr:    errors.New("disk full"),
	}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	_, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:     "editor",
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

func TestCreateRole_PutNameIndexFails(t *testing.T) {
	fkv := &faultKVStore{
		inner:     newMemKVStore(),
		putFailAt: 1, // the name claim, the first write, fails
		putErr:    errors.New("disk full"),
	}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	_, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:     "editor",
		TenantID: "acme",
	})
	if err == nil {
		t.Fatal("expected error on Put name index")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestGetRole_StoreGetError(t *testing.T) {
	fkv := &faultKVStore{
		inner:     newMemKVStore(),
		getFailAt: 1,
		getErr:    errors.New("read error"),
	}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	_, err := svc.GetRole(ctx, "some-id")
	if err == nil {
		t.Fatal("expected error from store Get")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestGetRoleByName_StoreGetError(t *testing.T) {
	fkv := &faultKVStore{
		inner:     newMemKVStore(),
		getFailAt: 1,
		getErr:    errors.New("read error"),
	}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	_, err := svc.GetRoleByName(ctx, "acme", "editor")
	if err == nil {
		t.Fatal("expected error from store Get")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestUpdateRole_PutPrimaryFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	r := createTestRole(t, svc, "editor", "acme")

	// Now make Put fail.
	fkv.putFailAt = fkv.putCount + 1
	fkv.putErr = errors.New("disk full")

	_, err := svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID:          r.ID,
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err == nil {
		t.Fatal("expected error on Put primary")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestUpdateRole_NameChange_GetFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	r := createTestRole(t, svc, "editor", "acme")

	// Fail on the Get for name uniqueness check.
	// After create, getCount has been used. We need the next
	// Get calls: 1 for GetRole(id), 2 for name index check.
	fkv.getFailAt = fkv.getCount + 2
	fkv.getErr = errors.New("read error")

	_, err := svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID:   r.ID,
		Name: "editor-v2",
	})
	if err == nil {
		t.Fatal("expected error on name index Get check")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestUpdateRole_NameChange_DeleteOldIndexFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	r := createTestRole(t, svc, "editor", "acme")

	// Fail on Delete (old name index removal).
	fkv.delFailAt = 1
	fkv.delErr = errors.New("delete error")

	_, err := svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID:   r.ID,
		Name: "editor-v2",
	})
	if err == nil {
		t.Fatal("expected error on Delete old name index")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestUpdateRole_NameChange_PutNewIndexFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	r := createTestRole(t, svc, "editor", "acme")

	// Fail on the next Put (new name index).
	fkv.putFailAt = fkv.putCount + 1
	fkv.putErr = errors.New("write error")

	_, err := svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID:   r.ID,
		Name: "editor-v2",
	})
	if err == nil {
		t.Fatal("expected error on Put new name index")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDeleteRole_DeletePrimaryFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv, WithHolderRevoker(noHolders{}))
	ctx := context.Background()

	r := createTestRole(t, svc, "editor", "acme")

	fkv.delFailAt = 1
	fkv.delErr = errors.New("delete error")

	err := svc.DeleteRole(ctx, r.ID)
	if err == nil {
		t.Fatal("expected error on Delete primary")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDeleteRole_DeleteTenantIndexFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv, WithHolderRevoker(noHolders{}))
	ctx := context.Background()

	r := createTestRole(t, svc, "editor", "acme")

	fkv.delFailAt = 2 // first Delete (primary) succeeds, second (tenant index) fails
	fkv.delErr = errors.New("delete error")

	err := svc.DeleteRole(ctx, r.ID)
	if err == nil {
		t.Fatal("expected error on Delete tenant index")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDeleteRole_DeleteNameIndexFails(t *testing.T) {
	mem := newMemKVStore()
	fkv := &faultKVStore{inner: mem}
	svc, _ := NewStore(fkv, WithHolderRevoker(noHolders{}))
	ctx := context.Background()

	r := createTestRole(t, svc, "editor", "acme")

	fkv.delFailAt = 3 // primary + tenant succeed, name index fails
	fkv.delErr = errors.New("delete error")

	err := svc.DeleteRole(ctx, r.ID)
	if err == nil {
		t.Fatal("expected error on Delete name index")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestListRoles_ScanFails(t *testing.T) {
	fkv := &faultKVStore{
		inner:   newMemKVStore(),
		scanErr: errors.New("scan error"),
	}
	svc, _ := NewStore(fkv)
	ctx := context.Background()

	_, err := svc.ListRoles(ctx, &ListRolesRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error on Scan")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestGetRole_CorruptedData(t *testing.T) {
	mem := newMemKVStore()
	svc, _ := NewStore(mem)
	ctx := context.Background()

	// Write garbage directly to the primary key.
	_ = mem.Put(ctx, primaryKey("corrupt-id"), []byte("{invalid json"))

	_, err := svc.GetRole(ctx, "corrupt-id")
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
// deterministicUUID
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Integration: full lifecycle
// ---------------------------------------------------------------------------

func TestRoleLifecycle(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	// Create.
	r, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        "lifecycle",
		TenantID:    "test-tenant",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	// Get by ID.
	fetched, err := svc.GetRole(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if fetched.Name != "lifecycle" {
		t.Errorf("Name: got %q, want %q", fetched.Name, "lifecycle")
	}

	// Get by name.
	byName, err := svc.GetRoleByName(ctx, "test-tenant", "lifecycle")
	if err != nil {
		t.Fatalf("GetRoleByName: %v", err)
	}
	if byName.ID != r.ID {
		t.Errorf("ID mismatch: got %q, want %q", byName.ID, r.ID)
	}

	// Update.
	updated, err := svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID:          r.ID,
		Name:        "lifecycle-v2",
		Permissions: []permission.Permission{permission.KVGet, permission.KVPut},
	})
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if updated.Name != "lifecycle-v2" {
		t.Errorf("Name: got %q, want %q", updated.Name, "lifecycle-v2")
	}
	if len(updated.Permissions) != 2 {
		t.Errorf("Permissions: got %d, want 2", len(updated.Permissions))
	}

	// Resolve permissions.
	perms, err := svc.ResolvePermissions(ctx, []string{r.ID})
	if err != nil {
		t.Fatalf("ResolvePermissions: %v", err)
	}
	if len(perms) != 2 {
		t.Errorf("Resolved permissions: got %d, want 2", len(perms))
	}

	// List.
	resp, err := svc.ListRoles(ctx, &ListRolesRequest{TenantID: "test-tenant"})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("Total: got %d, want 1", resp.Total)
	}

	// Delete.
	if err := svc.DeleteRole(ctx, r.ID); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}

	// Verify deleted.
	_, err = svc.GetRole(ctx, r.ID)
	if err == nil {
		t.Fatal("expected not found after delete")
	}
}

// ---------------------------------------------------------------------------
// Quota enforcement tests
// ---------------------------------------------------------------------------

// mockRoleQuotaChecker is a test double for quota.Checker.
type mockRoleQuotaChecker struct {
	checkFn func(ctx context.Context, tenantID string, resource quota.Resource) error
}

func (m *mockRoleQuotaChecker) Check(ctx context.Context, tenantID string, resource quota.Resource) error {
	if m.checkFn != nil {
		return m.checkFn(ctx, tenantID, resource)
	}
	return nil
}

func (m *mockRoleQuotaChecker) CheckN(ctx context.Context, tenantID string, resource quota.Resource, n int64) error {
	return nil
}

func (m *mockRoleQuotaChecker) Usage(ctx context.Context, tenantID string, resource quota.Resource) (int64, error) {
	return 0, nil
}

// newTestRoleStoreWithQuota creates a kvRoleStore with the given quota.Checker.
func newTestRoleStoreWithQuota(t *testing.T, checker quota.Checker) *kvRoleStore {
	t.Helper()
	s := &kvRoleStore{store: newMemKVStore()}
	return s.WithQuotaChecker(checker)
}

func TestKVRoleStore_CreateRole_QuotaExceeded(t *testing.T) {
	exceeded := &quota.ExceededError{
		TenantID: "acme",
		Resource: quota.ResourceRoles,
		Current:  50,
		Limit:    50,
	}
	checker := &mockRoleQuotaChecker{
		checkFn: func(_ context.Context, _ string, _ quota.Resource) error {
			return exceeded
		},
	}

	svc := newTestRoleStoreWithQuota(t, checker)
	ctx := context.Background()

	_, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:     "blocked",
		TenantID: "acme",
	})
	if err == nil {
		t.Fatal("CreateRole() returned nil, want *quota.ExceededError")
	}
	var qErr *quota.ExceededError
	if !errors.As(err, &qErr) {
		t.Fatalf("CreateRole() error type = %T, want *quota.ExceededError", err)
	}
	if qErr.Resource != quota.ResourceRoles {
		t.Errorf("ExceededError.Resource = %v, want ResourceRoles", qErr.Resource)
	}
}

func TestKVRoleStore_CreateRole_NoQuotaChecker(t *testing.T) {
	// Without a quota checker, creation must succeed normally.
	svc := newTestStore(t)
	ctx := context.Background()

	r, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:     "noquota",
		TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("CreateRole() returned unexpected error: %v", err)
	}
	if r.Name != "noquota" {
		t.Errorf("Role.Name = %q, want %q", r.Name, "noquota")
	}
}

func TestKVRoleStore_CreateRole_QuotaPasses(t *testing.T) {
	var checkedTenantID string
	var checkedResource quota.Resource
	checker := &mockRoleQuotaChecker{
		checkFn: func(_ context.Context, tenantID string, resource quota.Resource) error {
			checkedTenantID = tenantID
			checkedResource = resource
			return nil // quota allows creation
		},
	}

	svc := newTestRoleStoreWithQuota(t, checker)
	ctx := context.Background()

	r, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:     "allowed",
		TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("CreateRole() returned unexpected error: %v", err)
	}
	if r.Name != "allowed" {
		t.Errorf("Role.Name = %q, want %q", r.Name, "allowed")
	}
	if checkedTenantID != "acme" {
		t.Errorf("quota.Check() tenantID = %q, want %q", checkedTenantID, "acme")
	}
	if checkedResource != quota.ResourceRoles {
		t.Errorf("quota.Check() resource = %v, want ResourceRoles", checkedResource)
	}
}

func TestKVRoleStore_WithQuotaChecker_DefaultTenant(t *testing.T) {
	// When TenantID is empty the store falls back to SystemTenantID.
	var checkedTenantID string
	checker := &mockRoleQuotaChecker{
		checkFn: func(_ context.Context, tenantID string, _ quota.Resource) error {
			checkedTenantID = tenantID
			return nil
		},
	}

	svc := newTestRoleStoreWithQuota(t, checker)
	ctx := context.Background()

	_, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:     "default-tenant-role",
		TenantID: "", // empty → should default
	})
	if err != nil {
		t.Fatalf("CreateRole() returned unexpected error: %v", err)
	}
	if checkedTenantID != tenantid.System {
		t.Errorf("quota.Check() tenantID = %q, want %q", checkedTenantID, tenantid.System)
	}
}

func TestCreateRole_PreAssignedID(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	// A non-reserved name: this test is about honouring a caller-supplied id,
	// and a canonical system role name now requires the canonical id, which
	// would make "the id I asked for" untestable here.
	const wantID = "deterministic-manager-id"
	r, err := svc.CreateRole(ctx, &CreateRoleRequest{
		ID:          wantID,
		Name:        "manager",
		TenantID:    "acme",
		Permissions: []permission.Permission{permission.KVPut},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if r.ID != wantID {
		t.Fatalf("ID: got %q, want %q", r.ID, wantID)
	}

	// The role must be retrievable by the exact pre-assigned ID.
	got, err := svc.GetRole(ctx, wantID)
	if err != nil {
		t.Fatalf("GetRole(%q): %v", wantID, err)
	}
	if got.ID != wantID {
		t.Errorf("GetRole ID: got %q, want %q", got.ID, wantID)
	}
	if got.Name != "manager" {
		t.Errorf("GetRole Name: got %q, want %q", got.Name, "manager")
	}
}

func TestCreateRole_EmptyIDAutoGenerates(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	r, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        "viewer",
		TenantID:    "acme",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if r.ID == "" {
		t.Fatal("expected auto-generated non-empty ID")
	}
	// Auto-generated IDs are UUIDs; ensure retrievable under that ID.
	if _, err := svc.GetRole(ctx, r.ID); err != nil {
		t.Fatalf("GetRole(%q): %v", r.ID, err)
	}
}
