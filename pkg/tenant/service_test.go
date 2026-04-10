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

package tenant

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// errStoreFault is a sentinel error used by failingBackend.
var errStoreFault = errors.New("store: injected fault")

// failingBackend wraps a real StorageBackend and injects errors on
// configurable operations.
type failingBackend struct {
	inner   seal.StorageBackend
	failGet bool
	failPut bool
	failDel bool
	failLst bool
}

func (f *failingBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if f.failGet {
		return nil, errStoreFault
	}
	return f.inner.Get(ctx, key)
}
func (f *failingBackend) Put(ctx context.Context, key string, data []byte) error {
	if f.failPut {
		return errStoreFault
	}
	return f.inner.Put(ctx, key, data)
}
func (f *failingBackend) Delete(ctx context.Context, key string) error {
	if f.failDel {
		return errStoreFault
	}
	return f.inner.Delete(ctx, key)
}
func (f *failingBackend) List(ctx context.Context, prefix string) ([]string, error) {
	if f.failLst {
		return nil, errStoreFault
	}
	return f.inner.List(ctx, prefix)
}
func (f *failingBackend) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	return f.inner.Scan(ctx, prefix, fn)
}
func (f *failingBackend) Exists(ctx context.Context, key string) (bool, error) {
	return f.inner.Exists(ctx, key)
}
func (f *failingBackend) Close() error { return nil }

// testLogger returns a discard logger for tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testSoftwareStrategy creates a SoftwareStrategy for tests.
func testSoftwareStrategy(t *testing.T) *seal.SoftwareStrategy {
	t.Helper()
	s, err := seal.NewSoftwareStrategy([]byte("test-master-password"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	return s
}

// newTestBarrierRegistry creates a BarrierRegistry with a system barrier
// for tenant service tests.
func newTestBarrierRegistry(t *testing.T) *seal.BarrierRegistry {
	t.Helper()
	store := seal.NewMemoryBackend()
	system, err := seal.NewBarrier(testLogger(), store, seal.BarrierConfig{
		RootKeyPath: "system/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier (system): %v", err)
	}
	reg, err := seal.NewBarrierRegistry(testLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	return reg
}

// newTestService creates a TenantService backed by in-memory storage and
// a fresh barrier registry.
func newTestService(t *testing.T) (*TenantService, *seal.BarrierRegistry) {
	t.Helper()
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc, reg
}

// newTestServiceWithStore creates a TenantService with a custom storage backend.
func newTestServiceWithStore(t *testing.T, store seal.StorageBackend) (*TenantService, *seal.BarrierRegistry) {
	t.Helper()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc, reg
}

// ---------- Constructor tests ----------

func TestNewTenantService_Success(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}

func TestNewTenantService_NilStore(t *testing.T) {
	reg := newTestBarrierRegistry(t)
	_, err := NewTenantService(nil, reg, testLogger())
	if !errors.Is(err, ErrNilStore) {
		t.Errorf("expected ErrNilStore, got %v", err)
	}
}

func TestNewTenantService_NilRegistry(t *testing.T) {
	store := seal.NewMemoryBackend()
	_, err := NewTenantService(store, nil, testLogger())
	if !errors.Is(err, ErrNilRegistry) {
		t.Errorf("expected ErrNilRegistry, got %v", err)
	}
}

func TestNewTenantService_NilLogger(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, nil)
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service with default logger")
	}
}

// ---------- CreateTenant / GetTenant ----------

func TestTenantService_CreateGet(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	info, err := svc.CreateTenant(ctx, "acme", "Acme Corp")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if info.ID != "acme" {
		t.Errorf("expected ID acme, got %s", info.ID)
	}
	if info.Name != "Acme Corp" {
		t.Errorf("expected Name 'Acme Corp', got %s", info.Name)
	}
	if info.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
	if info.UpdatedAt.IsZero() {
		t.Error("expected non-zero UpdatedAt")
	}

	// Retrieve and verify.
	got, err := svc.GetTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.ID != info.ID || got.Name != info.Name {
		t.Errorf("get mismatch: got %+v, want %+v", got, info)
	}
}

func TestTenantService_CreateDuplicate(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "dup", "First")
	if err != nil {
		t.Fatalf("first CreateTenant: %v", err)
	}

	_, err = svc.CreateTenant(ctx, "dup", "Second")
	if !errors.Is(err, ErrTenantAlreadyExists) {
		t.Errorf("expected ErrTenantAlreadyExists, got %v", err)
	}
}

func TestTenantService_CreateEmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "", "NoID")
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantService_CreateEmptyName(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "valid-id", "")
	if !errors.Is(err, ErrEmptyTenantName) {
		t.Errorf("expected ErrEmptyTenantName, got %v", err)
	}
}

func TestTenantService_CreateStoreGetFault(t *testing.T) {
	fb := &failingBackend{inner: seal.NewMemoryBackend(), failGet: true}
	svc, _ := newTestServiceWithStore(t, fb)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "test", "Test")
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %v", err)
	}
}

func TestTenantService_CreateStorePutFault(t *testing.T) {
	fb := &failingBackend{inner: seal.NewMemoryBackend(), failPut: true}
	svc, _ := newTestServiceWithStore(t, fb)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "test", "Test")
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %v", err)
	}
}

func TestTenantService_GetNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.GetTenant(ctx, "nonexistent")
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestTenantService_GetEmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.GetTenant(ctx, "")
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantService_GetStoreFault(t *testing.T) {
	fb := &failingBackend{inner: seal.NewMemoryBackend(), failGet: true}
	svc, _ := newTestServiceWithStore(t, fb)
	ctx := context.Background()

	_, err := svc.GetTenant(ctx, "any")
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %v", err)
	}
}

func TestTenantService_GetCorruptData(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := context.Background()

	// Write invalid JSON directly.
	if err := store.Put(ctx, "tenants/corrupt", []byte("not-json")); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	_, err = svc.GetTenant(ctx, "corrupt")
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError for corrupt data, got %v", err)
	}
}

// ---------- ListTenants ----------

func TestTenantService_List(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	// Empty list initially.
	tenants, err := svc.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants (empty): %v", err)
	}
	if len(tenants) != 0 {
		t.Errorf("expected 0 tenants, got %d", len(tenants))
	}

	// Create multiple tenants.
	for _, id := range []string{"alpha", "beta", "gamma"} {
		_, err := svc.CreateTenant(ctx, id, "Tenant "+id)
		if err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
	}

	tenants, err = svc.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(tenants) != 3 {
		t.Fatalf("expected 3 tenants, got %d", len(tenants))
	}

	// Verify all IDs are present.
	found := make(map[string]bool)
	for _, info := range tenants {
		found[info.ID] = true
	}
	for _, expected := range []string{"alpha", "beta", "gamma"} {
		if !found[expected] {
			t.Errorf("missing tenant %s in list", expected)
		}
	}
}

func TestTenantService_ListStoreFault(t *testing.T) {
	fb := &failingBackend{inner: seal.NewMemoryBackend(), failLst: true}
	svc, _ := newTestServiceWithStore(t, fb)
	ctx := context.Background()

	_, err := svc.ListTenants(ctx)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %v", err)
	}
}

func TestTenantService_ListSkipsCorruptEntries(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := context.Background()

	// Create a valid tenant.
	_, err = svc.CreateTenant(ctx, "valid", "Valid Tenant")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Write corrupt metadata directly.
	if err := store.Put(ctx, "tenants/corrupt", []byte("bad-json")); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	tenants, err := svc.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(tenants) != 1 {
		t.Errorf("expected 1 tenant (skipping corrupt), got %d", len(tenants))
	}
	if tenants[0].ID != "valid" {
		t.Errorf("expected valid, got %s", tenants[0].ID)
	}
}

// ---------- DeleteTenant ----------

func TestTenantService_Delete(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "to-delete", "Delete Me")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Verify exists before deletion.
	_, err = svc.GetTenant(ctx, "to-delete")
	if err != nil {
		t.Fatalf("GetTenant before delete: %v", err)
	}

	if err := svc.DeleteTenant(ctx, "to-delete"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	// Verify not found after deletion.
	_, err = svc.GetTenant(ctx, "to-delete")
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound after delete, got %v", err)
	}
}

func TestTenantService_DeleteNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	err := svc.DeleteTenant(ctx, "ghost")
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestTenantService_DeleteEmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	err := svc.DeleteTenant(ctx, "")
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantService_DeleteStoreGetFault(t *testing.T) {
	fb := &failingBackend{inner: seal.NewMemoryBackend(), failGet: true}
	svc, _ := newTestServiceWithStore(t, fb)
	ctx := context.Background()

	err := svc.DeleteTenant(ctx, "any")
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %v", err)
	}
}

func TestTenantService_DeleteStoreDelFault(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := context.Background()

	// Create a tenant so metadata exists.
	_, err = svc.CreateTenant(ctx, "del-fault", "Fault")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Replace store with failing one that has the data.
	fb := &failingBackend{inner: store, failDel: true}
	svc.store = fb

	err = svc.DeleteTenant(ctx, "del-fault")
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %v", err)
	}
}

// ---------- TenantBarrierInit ----------

func TestTenantService_BarrierInit(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "init-test", "Init Test")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Verify barrier is sealed before init.
	status, err := reg.TenantStatus("init-test")
	if err != nil {
		t.Fatalf("TenantStatus before init: %v", err)
	}
	if !status.Sealed {
		t.Error("expected sealed before init")
	}

	// Initialize the barrier.
	if err := svc.TenantBarrierInit(ctx, "init-test", 0, 0); err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}

	// Verify barrier is unsealed after init.
	status, err = reg.TenantStatus("init-test")
	if err != nil {
		t.Fatalf("TenantStatus after init: %v", err)
	}
	if status.Sealed {
		t.Error("expected unsealed after init")
	}
}

func TestTenantService_BarrierInitNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	err := svc.TenantBarrierInit(ctx, "nonexistent", 0, 0)
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestTenantService_BarrierInitEmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	err := svc.TenantBarrierInit(ctx, "", 0, 0)
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantService_BarrierInitAlreadyInitialized(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "double-init", "Double Init")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	if err := svc.TenantBarrierInit(ctx, "double-init", 0, 0); err != nil {
		t.Fatalf("first TenantBarrierInit: %v", err)
	}

	// Second init should fail with BarrierError wrapping ErrTenantAlreadyInitialized.
	err = svc.TenantBarrierInit(ctx, "double-init", 0, 0)
	var be *BarrierError
	if !errors.As(err, &be) {
		t.Errorf("expected BarrierError, got %v", err)
	}
}

// ---------- TenantBarrierUnseal ----------

func TestTenantService_BarrierUnseal(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "unseal-test", "Unseal Test")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Initialize first.
	if err := svc.TenantBarrierInit(ctx, "unseal-test", 0, 0); err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}

	// Seal the barrier.
	if err := reg.SealTenant("unseal-test"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}

	// Verify sealed.
	status, err := reg.TenantStatus("unseal-test")
	if err != nil {
		t.Fatalf("TenantStatus after seal: %v", err)
	}
	if !status.Sealed {
		t.Error("expected sealed after SealTenant")
	}

	// Unseal via service (nil key falls back to default credential).
	if err := svc.TenantBarrierUnseal(ctx, "unseal-test", nil); err != nil {
		t.Fatalf("TenantBarrierUnseal: %v", err)
	}

	// Verify unsealed.
	status, err = reg.TenantStatus("unseal-test")
	if err != nil {
		t.Fatalf("TenantStatus after unseal: %v", err)
	}
	if status.Sealed {
		t.Error("expected unsealed after TenantBarrierUnseal")
	}
}

func TestTenantService_BarrierUnsealWithExplicitKey(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "unseal-key", "Unseal Key")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Initialize the barrier (uses default credential).
	if err := svc.TenantBarrierInit(ctx, "unseal-key", 0, 0); err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}

	// Seal and try to unseal with explicit key (which differs from init secret).
	if err := reg.SealTenant("unseal-key"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}

	// Unseal with the same deterministic key that init used.
	err = svc.TenantBarrierUnseal(ctx, "unseal-key", []byte("tenant-init-unseal-key"))
	if err != nil {
		t.Fatalf("TenantBarrierUnseal with explicit key: %v", err)
	}

	status, err := reg.TenantStatus("unseal-key")
	if err != nil {
		t.Fatalf("TenantStatus: %v", err)
	}
	if status.Sealed {
		t.Error("expected unsealed after TenantBarrierUnseal with explicit key")
	}
}

func TestTenantService_BarrierUnsealNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	err := svc.TenantBarrierUnseal(ctx, "nonexistent", nil)
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestTenantService_BarrierUnsealEmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	err := svc.TenantBarrierUnseal(ctx, "", nil)
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantService_BarrierUnsealNotInitialized(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "no-init", "Not Initialized")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Unseal without initializing should fail with BarrierError.
	err = svc.TenantBarrierUnseal(ctx, "no-init", nil)
	var be *BarrierError
	if !errors.As(err, &be) {
		t.Errorf("expected BarrierError, got %v", err)
	}
}

// ---------- TenantIDFromKey ----------

func TestTenantIDFromKey(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{key: "tenants/acme", want: "acme"},
		{key: "tenants/", want: ""},
		{key: "other/acme", want: ""},
		{key: "", want: ""},
	}
	for _, tt := range tests {
		got := TenantIDFromKey(tt.key)
		if got != tt.want {
			t.Errorf("TenantIDFromKey(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

// ---------- UpdateQuotas ----------

func TestTenantService_UpdateQuotas(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "quota-test", "Quota Test")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	q := &quota.Quotas{
		MaxDatabases:    10,
		MaxUsers:        50,
		MaxRoles:        20,
		MaxKeys:         -1,
		MaxStorageBytes: 1073741824,
	}

	info, err := svc.UpdateQuotas(ctx, "quota-test", q)
	if err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}
	if info.Quotas == nil {
		t.Fatal("expected non-nil quotas after update")
	}
	if info.Quotas.MaxDatabases != 10 {
		t.Errorf("MaxDatabases = %d, want 10", info.Quotas.MaxDatabases)
	}
	if info.Quotas.MaxUsers != 50 {
		t.Errorf("MaxUsers = %d, want 50", info.Quotas.MaxUsers)
	}

	// Verify persistence via GetTenant.
	got, err := svc.GetTenant(ctx, "quota-test")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.Quotas == nil {
		t.Fatal("expected persisted quotas")
	}
	if got.Quotas.MaxDatabases != 10 {
		t.Errorf("persisted MaxDatabases = %d, want 10", got.Quotas.MaxDatabases)
	}
}

func TestTenantService_UpdateQuotasEmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.UpdateQuotas(ctx, "", &quota.Quotas{})
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantService_UpdateQuotasNilQuotas(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.UpdateQuotas(ctx, "any", nil)
	if !errors.Is(err, ErrNilQuotas) {
		t.Errorf("expected ErrNilQuotas, got %v", err)
	}
}

func TestTenantService_UpdateQuotasNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.UpdateQuotas(ctx, "nonexistent", &quota.Quotas{})
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestTenantService_UpdateQuotasStorePutFault(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := context.Background()

	_, err = svc.CreateTenant(ctx, "put-fault", "Put Fault")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Replace store with failing one that has the data.
	fb := &failingBackend{inner: store, failPut: true}
	svc.store = fb

	_, err = svc.UpdateQuotas(ctx, "put-fault", &quota.Quotas{MaxDatabases: 5})
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %v", err)
	}
}

// ---------- Quotas (QuotaProvider) ----------

func TestTenantService_QuotasWithConfigured(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "qp-test", "QP Test")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	q := &quota.Quotas{MaxDatabases: 25, MaxKeys: -1}
	_, err = svc.UpdateQuotas(ctx, "qp-test", q)
	if err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}

	got, err := svc.Quotas(ctx, "qp-test")
	if err != nil {
		t.Fatalf("Quotas: %v", err)
	}
	if got.MaxDatabases != 25 {
		t.Errorf("MaxDatabases = %d, want 25", got.MaxDatabases)
	}
	if got.MaxKeys != -1 {
		t.Errorf("MaxKeys = %d, want -1", got.MaxKeys)
	}
}

func TestTenantService_QuotasWithNilReturnsZero(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "no-quota", "No Quota")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	got, err := svc.Quotas(ctx, "no-quota")
	if err != nil {
		t.Fatalf("Quotas: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil zero-value Quotas")
	}
	if got.MaxDatabases != 0 {
		t.Errorf("MaxDatabases = %d, want 0 (system default)", got.MaxDatabases)
	}
}

func TestTenantService_QuotasNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.Quotas(ctx, "nonexistent")
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

// ---------- TenantInfo with Quotas persistence ----------

func TestTenantInfo_QuotasRoundTrip(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "rt-test", "Round Trip")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Set quotas and verify round-trip through list.
	q := &quota.Quotas{MaxCollections: 100, MaxBackups: 5}
	_, err = svc.UpdateQuotas(ctx, "rt-test", q)
	if err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}

	tenants, err := svc.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(tenants) != 1 {
		t.Fatalf("expected 1 tenant, got %d", len(tenants))
	}
	if tenants[0].Quotas == nil {
		t.Fatal("expected quotas in listed tenant")
	}
	if tenants[0].Quotas.MaxCollections != 100 {
		t.Errorf("MaxCollections = %d, want 100", tenants[0].Quotas.MaxCollections)
	}
	if tenants[0].Quotas.MaxBackups != 5 {
		t.Errorf("MaxBackups = %d, want 5", tenants[0].Quotas.MaxBackups)
	}
}

// ---------- Error types ----------

func TestStoreError(t *testing.T) {
	err := &StoreError{Op: "put", Err: ErrNilStore}
	if err.Error() != "tenant: put: tenant: nil storage backend" {
		t.Errorf("unexpected error string: %s", err.Error())
	}
	if !errors.Is(err, ErrNilStore) {
		t.Error("expected Unwrap to return ErrNilStore")
	}
}

func TestBarrierError(t *testing.T) {
	err := &BarrierError{TenantID: "test", Op: "init", Err: ErrNilRegistry}
	if err.Error() != "tenant: barrier init for test: tenant: nil barrier registry" {
		t.Errorf("unexpected error string: %s", err.Error())
	}
	if !errors.Is(err, ErrNilRegistry) {
		t.Error("expected Unwrap to return ErrNilRegistry")
	}
}
