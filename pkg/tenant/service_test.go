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

	"github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/local"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
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
func (f *failingBackend) PutIfAbsent(ctx context.Context, key string, data []byte) (bool, error) {
	if f.failPut {
		return false, errStoreFault
	}
	return f.inner.PutIfAbsent(ctx, key, data)
}

// PutIfAllAbsent is the fenced create CreateTenant commits through. It is a
// write, so failPut applies to it exactly as it does to Put: a double that
// injected faults into Put but let the fenced create through would stop
// modelling a store whose writes fail.
func (f *failingBackend) PutIfAllAbsent(ctx context.Context, key string, data []byte, fences ...string) (bool, error) {
	if f.failPut {
		return false, errStoreFault
	}
	fenced, ok := f.inner.(kvstore.FencedCreator)
	if !ok {
		return false, seal.ErrFencedCreateUnsupported
	}
	return fenced.PutIfAllAbsent(ctx, key, data, fences...)
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (f *failingBackend) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}

func (f *failingBackend) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
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
// and a pool-driven per-tenant Pebble flow for tenant service tests.
// Barrier-isolated tenants registered through RegisterTenantPooled get a
// durable, independent per-tenant Pebble at a t-scoped temp directory, so
// crypto/physical isolation can be exercised against a real seal backend
// without modifying the host system.
func newTestBarrierRegistry(t *testing.T, extra ...seal.Option) *seal.BarrierRegistry {
	t.Helper()
	store := seal.NewMemoryBackend()
	system, err := seal.NewBarrier(testLogger(), store, seal.BarrierConfig{
		RootKeyPath: "system/root-key",
	}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewBarrier (system): %v", err)
	}

	// Single-path local volume pool backed by a t-scoped temp dir.
	localStore := local.New()
	if cfgErr := localStore.Configure(map[string]string{"path": t.TempDir()}); cfgErr != nil {
		t.Fatalf("Configure local backend: %v", cfgErr)
	}
	mgr := objpool.NewManager()

	stateStorage, err := pebblestore.New(&pebblestore.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open state-store Pebble: %v", err)
	}
	t.Cleanup(func() { _ = stateStorage.Close() })
	state, err := qrdbpool.NewPebbleStateStore(stateStorage)
	if err != nil {
		t.Fatalf("NewPebbleStateStore: %v", err)
	}
	rr := &objpool.RoundRobinStrategy{State: state}
	if regErr := mgr.Register("default", []common.Storage{localStore}, rr); regErr != nil {
		t.Fatalf("pool Register: %v", regErr)
	}

	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	opts := append([]seal.Option{
		seal.WithVolumePool(mgr, state, "default"),
		seal.WithPerTenantRegistry(perTenantReg),
	}, extra...)
	reg, err := seal.NewBarrierRegistry(testLogger(), system, opts...)
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

	info, err := svc.CreateTenant(ctx, "acme", "Acme Corp", "")
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

	_, err := svc.CreateTenant(ctx, "dup", "First", "")
	if err != nil {
		t.Fatalf("first CreateTenant: %v", err)
	}

	_, err = svc.CreateTenant(ctx, "dup", "Second", "")
	if !errors.Is(err, ErrTenantAlreadyExists) {
		t.Errorf("expected ErrTenantAlreadyExists, got %v", err)
	}
}

func TestTenantService_CreateEmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "", "NoID", "")
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantService_CreateEmptyName(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "valid-id", "", "")
	if !errors.Is(err, ErrEmptyTenantName) {
		t.Errorf("expected ErrEmptyTenantName, got %v", err)
	}
}

func TestTenantService_CreateStoreGetFault(t *testing.T) {
	fb := &failingBackend{inner: seal.NewMemoryBackend(), failGet: true}
	svc, _ := newTestServiceWithStore(t, fb)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "test", "Test", "")
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %v", err)
	}
}

func TestTenantService_CreateStorePutFault(t *testing.T) {
	fb := &failingBackend{inner: seal.NewMemoryBackend(), failPut: true}
	svc, _ := newTestServiceWithStore(t, fb)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "test", "Test", "")
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

	// Write invalid JSON directly under the canonical metadata prefix.
	if err := store.Put(ctx, metadataPrefix+"corrupt", []byte("not-json")); err != nil {
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
		_, err := svc.CreateTenant(ctx, id, "Tenant "+id, "")
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
	_, err = svc.CreateTenant(ctx, "valid", "Valid Tenant", "")
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

	_, err := svc.CreateTenant(ctx, "to-delete", "Delete Me", "")
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
	_, err = svc.CreateTenant(ctx, "del-fault", "Fault", "")
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

	// A prefix tenant (the default) does not pre-register a barrier; the
	// barrier is registered-and-initialized on demand by TenantBarrierInit
	// via the register-shell path.
	_, err := svc.CreateTenant(ctx, "init-test", "Init Test", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// No barrier registered yet: TenantBarrierStatus reports uninitialized.
	preStatus, err := svc.TenantBarrierStatus(ctx, "init-test")
	if err != nil {
		t.Fatalf("TenantBarrierStatus before init: %v", err)
	}
	if preStatus.Initialized {
		t.Error("expected uninitialized before init")
	}

	// Initialize the barrier with a caller-supplied passphrase. This
	// register-shell-initializes the tenant barrier and leaves it unsealed.
	if _, err := svc.TenantBarrierInit(ctx, "init-test", []byte("init-test-passphrase"), 0, 0); err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}

	// Verify barrier is unsealed after init.
	status, err := reg.TenantStatus("init-test")
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

	_, err := svc.TenantBarrierInit(ctx, "nonexistent", []byte("pw"), 0, 0)
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestTenantService_BarrierInitEmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.TenantBarrierInit(ctx, "", []byte("pw"), 0, 0)
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantService_BarrierInitAlreadyInitialized(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "double-init", "Double Init", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	if _, err := svc.TenantBarrierInit(ctx, "double-init", []byte("double-init-pw"), 0, 0); err != nil {
		t.Fatalf("first TenantBarrierInit: %v", err)
	}

	// Second init should fail with BarrierError wrapping ErrTenantAlreadyInitialized.
	_, err = svc.TenantBarrierInit(ctx, "double-init", []byte("double-init-pw"), 0, 0)
	var be *BarrierError
	if !errors.As(err, &be) {
		t.Errorf("expected BarrierError, got %v", err)
	}
}

// TestTenantService_BarrierInitEmptyPassphraseRejected proves that the
// passphrase strategy rejects an empty passphrase with ErrEmptyPassphrase and
// does NOT fall back to any derived "tenant-init-<id>" credential.
func TestTenantService_BarrierInitEmptyPassphraseRejected(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "empty-pw", "Empty PW", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	_, err := svc.TenantBarrierInit(ctx, "empty-pw", []byte(""), 0, 0)
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("expected ErrEmptyPassphrase, got %v", err)
	}
}

// TestTenantService_BarrierInitSuppliedPassphraseGoverns proves that when the
// tenant barrier is initialized through the register-shell path (no barrier
// pre-registered), the caller-supplied passphrase — not any derived value —
// governs unseal: the same passphrase unseals, a wrong passphrase fails, and
// the old derived "tenant-init-<id>" value does NOT unseal.
func TestTenantService_BarrierInitSuppliedPassphraseGoverns(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := context.Background()

	const tenantID = "supplied-pw"
	const passphrase = "correct-horse-battery-staple"

	if _, err := svc.CreateTenant(ctx, tenantID, "Supplied PW", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Remove the default (fixed-password software) barrier that CreateTenant
	// pre-registers so that TenantBarrierInit takes the register-shell path
	// and registers a deferred-software strategy that binds the supplied
	// passphrase at init time.
	// Prefix tenants (the new default) do not pre-register a barrier, so
	// UnregisterTenant is a tolerated no-op here; the subsequent
	// TenantBarrierInit takes the register-shell path either way.
	if err := reg.UnregisterTenant(tenantID); err != nil && !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("UnregisterTenant: %v", err)
	}

	if _, err := svc.TenantBarrierInit(ctx, tenantID, []byte(passphrase), 0, 0); err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}

	// The deferred-software strategy is active for the tenant barrier.
	st, err := reg.TenantStatus(tenantID)
	if err != nil {
		t.Fatalf("TenantStatus: %v", err)
	}
	if st.Strategy != seal.StrategyPassphrase {
		t.Fatalf("strategy = %q, want %q", st.Strategy, seal.StrategyPassphrase)
	}

	// Seal, then prove only the supplied passphrase unseals.
	if err := reg.SealTenant(tenantID); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}

	// Wrong passphrase must fail.
	if err := svc.TenantBarrierUnseal(WithTenantID(ctx, tenantID), tenantID, []byte("wrong-passphrase")); err == nil {
		t.Fatal("unseal with wrong passphrase succeeded, want failure")
	}

	// The previously derived credential must NOT unseal.
	if err := svc.TenantBarrierUnseal(WithTenantID(ctx, tenantID), tenantID, []byte("tenant-init-"+tenantID)); err == nil {
		t.Fatal("unseal with derived credential succeeded, want failure (no derived fallback)")
	}

	// The supplied passphrase must unseal.
	if err := svc.TenantBarrierUnseal(WithTenantID(ctx, tenantID), tenantID, []byte(passphrase)); err != nil {
		t.Fatalf("unseal with supplied passphrase: %v", err)
	}

	st, err = reg.TenantStatus(tenantID)
	if err != nil {
		t.Fatalf("TenantStatus after unseal: %v", err)
	}
	if st.Sealed {
		t.Fatal("expected unsealed after unseal with supplied passphrase")
	}
}

// ---------- TenantBarrierInitWithStrategy ----------

// TestTenantService_BarrierInitWithStrategy_DefaultPassphrase proves that
// calling TenantBarrierInitWithStrategy with an empty strategy ID falls back
// to seal.StrategyPassphrase — identical behaviour to TenantBarrierInit.
func TestTenantService_BarrierInitWithStrategy_DefaultPassphrase(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := context.Background()

	const tenantID = "strategy-default"
	const passphrase = "default-strategy-passphrase"

	if _, err := svc.CreateTenant(ctx, tenantID, "Strategy Default", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// Prefix tenants (the new default) do not pre-register a barrier, so
	// UnregisterTenant is a tolerated no-op here; the subsequent
	// TenantBarrierInit takes the register-shell path either way.
	if err := reg.UnregisterTenant(tenantID); err != nil && !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("UnregisterTenant: %v", err)
	}

	// Empty strategyID should resolve to passphrase.
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(passphrase), 0, 0, ""); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy(empty): %v", err)
	}

	st, err := reg.TenantStatus(tenantID)
	if err != nil {
		t.Fatalf("TenantStatus: %v", err)
	}
	if st.Strategy != seal.StrategyPassphrase {
		t.Fatalf("strategy = %q, want %q", st.Strategy, seal.StrategyPassphrase)
	}
	if st.Sealed {
		t.Fatal("expected barrier to be unsealed after init")
	}
}

// TestTenantService_BarrierInitWithStrategy_ExplicitPassphrase proves that
// explicitly requesting seal.StrategyPassphrase has the same semantics as
// the zero-value default.
func TestTenantService_BarrierInitWithStrategy_ExplicitPassphrase(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := context.Background()

	const tenantID = "strategy-explicit"
	const passphrase = "explicit-strategy-pw"

	if _, err := svc.CreateTenant(ctx, tenantID, "Strategy Explicit", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// Prefix tenants (the new default) do not pre-register a barrier, so
	// UnregisterTenant is a tolerated no-op here; the subsequent
	// TenantBarrierInit takes the register-shell path either way.
	if err := reg.UnregisterTenant(tenantID); err != nil && !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("UnregisterTenant: %v", err)
	}

	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(passphrase), 0, 0, seal.StrategyPassphrase); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy(passphrase): %v", err)
	}

	// Seal then confirm only the supplied passphrase can unseal.
	if err := reg.SealTenant(tenantID); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	if err := svc.TenantBarrierUnseal(WithTenantID(ctx, tenantID), tenantID, []byte(passphrase)); err != nil {
		t.Fatalf("unseal with supplied passphrase: %v", err)
	}
}

// TestTenantService_BarrierInitWithStrategy_NoneStrategy proves that the
// "none" strategy (no encryption) can be selected and that the barrier
// initializes and unseals correctly without a passphrase.
func TestTenantService_BarrierInitWithStrategy_NoneStrategy(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := context.Background()

	const tenantID = "strategy-none"

	if _, err := svc.CreateTenant(ctx, tenantID, "Strategy None", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// Prefix tenants (the new default) do not pre-register a barrier, so
	// UnregisterTenant is a tolerated no-op here; the subsequent
	// TenantBarrierInit takes the register-shell path either way.
	if err := reg.UnregisterTenant(tenantID); err != nil && !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("UnregisterTenant: %v", err)
	}

	// The "none" strategy requires no passphrase.
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(""), 0, 0, seal.StrategyNone); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy(none): %v", err)
	}

	st, err := reg.TenantStatus(tenantID)
	if err != nil {
		t.Fatalf("TenantStatus: %v", err)
	}
	if st.Sealed {
		t.Fatal("expected barrier unsealed after init with none strategy")
	}
}

// TestTenantService_BarrierInitWithStrategy_RegistryInjection proves that
// when a StrategyRegistry is injected via WithStrategyRegistry, the service
// resolves the strategy from the registry. Requesting the "none" strategy
// via the registry succeeds; requesting an unregistered name returns
// InvalidStrategyError.
func TestTenantService_BarrierInitWithStrategy_RegistryInjection(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	stratReg := seal.NewDefaultStrategyRegistry()
	svc, err := NewTenantService(store, reg, testLogger(), WithStrategyRegistry(stratReg))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}

	ctx := context.Background()
	const tenantID = "registry-injection"

	if _, err := svc.CreateTenant(ctx, tenantID, "Registry Injection", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// Prefix tenants (the new default) do not pre-register a barrier, so
	// UnregisterTenant is a tolerated no-op here; the subsequent
	// TenantBarrierInit takes the register-shell path either way.
	if err := reg.UnregisterTenant(tenantID); err != nil && !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("UnregisterTenant: %v", err)
	}

	// "none" is pre-registered in the default registry.
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(""), 0, 0, seal.StrategyNone); err != nil {
		t.Fatalf("TenantBarrierInitWithStrategy via registry: %v", err)
	}

	st, err := reg.TenantStatus(tenantID)
	if err != nil {
		t.Fatalf("TenantStatus: %v", err)
	}
	if st.Sealed {
		t.Fatal("expected barrier unsealed after init")
	}
}

// TestTenantService_BarrierInitWithStrategy_UnknownStrategy proves that
// requesting an unregistered strategy name returns InvalidStrategyError.
func TestTenantService_BarrierInitWithStrategy_UnknownStrategy(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	stratReg := seal.NewDefaultStrategyRegistry()
	svc, err := NewTenantService(store, reg, testLogger(), WithStrategyRegistry(stratReg))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}

	ctx := context.Background()
	const tenantID = "unknown-strategy"

	if _, err := svc.CreateTenant(ctx, tenantID, "Unknown Strategy", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// Entitle the tenant to "tpm2" so the barrier-mode governance gate passes and
	// the request reaches the strategy-resolution path under test.
	if _, err := svc.UpdateEntitlements(ctx, tenantID, &quota.Entitlements{AllowedBarrierModes: []string{"tpm2"}}); err != nil {
		t.Fatalf("UpdateEntitlements: %v", err)
	}

	_, err = svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte("irrelevant"), 0, 0, "tpm2")
	var invErr *InvalidStrategyError
	if !errors.As(err, &invErr) {
		t.Fatalf("expected InvalidStrategyError, got %v", err)
	}
	if invErr.Strategy != "tpm2" {
		t.Errorf("Strategy = %q, want %q", invErr.Strategy, "tpm2")
	}
}

// TestTenantService_BarrierInitWithStrategy_NoRegistryUnknown proves that
// when no StrategyRegistry is injected, requesting a hardware/cloud strategy
// returns InvalidStrategyError (no registry fallback).
func TestTenantService_BarrierInitWithStrategy_NoRegistryUnknown(t *testing.T) {
	svc, _ := newTestService(t) // no WithStrategyRegistry
	ctx := context.Background()

	const tenantID = "no-registry-hw"
	if _, err := svc.CreateTenant(ctx, tenantID, "No Registry HW", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// Entitle the tenant to "tpm2" so the request reaches the no-registry path.
	if _, err := svc.UpdateEntitlements(ctx, tenantID, &quota.Entitlements{AllowedBarrierModes: []string{"tpm2"}}); err != nil {
		t.Fatalf("UpdateEntitlements: %v", err)
	}

	_, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(""), 0, 0, "tpm2")
	var invErr *InvalidStrategyError
	if !errors.As(err, &invErr) {
		t.Fatalf("expected InvalidStrategyError, got %v", err)
	}
}

// TestTenantService_BarrierInitWithStrategy_EmptyPassphraseForPassphrase
// proves that the passphrase strategy requires a non-empty passphrase even
// when called via TenantBarrierInitWithStrategy.
func TestTenantService_BarrierInitWithStrategy_EmptyPassphraseForPassphrase(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	const tenantID = "empty-pass-strategy"
	if _, err := svc.CreateTenant(ctx, tenantID, "Empty Pass Strategy", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	_, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, []byte(""), 0, 0, seal.StrategyPassphrase)
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("expected ErrEmptyPassphrase, got %v", err)
	}
}

// TestTenantService_BarrierInitWithStrategy_BackCompat proves that
// TenantBarrierInit (unchanged signature) still works identically to calling
// TenantBarrierInitWithStrategy(seal.StrategyPassphrase).
func TestTenantService_BarrierInitWithStrategy_BackCompat(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := context.Background()

	const tenantID = "backcompat"
	const passphrase = "backcompat-pw"

	if _, err := svc.CreateTenant(ctx, tenantID, "BackCompat", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// Prefix tenants (the new default) do not pre-register a barrier, so
	// UnregisterTenant is a tolerated no-op here; the subsequent
	// TenantBarrierInit takes the register-shell path either way.
	if err := reg.UnregisterTenant(tenantID); err != nil && !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("UnregisterTenant: %v", err)
	}

	// Old signature must still work unchanged.
	if _, err := svc.TenantBarrierInit(ctx, tenantID, []byte(passphrase), 0, 0); err != nil {
		t.Fatalf("TenantBarrierInit (old signature): %v", err)
	}

	st, err := reg.TenantStatus(tenantID)
	if err != nil {
		t.Fatalf("TenantStatus: %v", err)
	}
	if st.Sealed {
		t.Fatal("expected barrier unsealed after init")
	}
	if st.Strategy != seal.StrategyPassphrase {
		t.Fatalf("strategy = %q, want %q", st.Strategy, seal.StrategyPassphrase)
	}
}

// TestTenantService_BarrierUnsealEmptyKeyRejected proves the unseal path
// rejects empty key material rather than deriving a fallback credential.
func TestTenantService_BarrierUnsealEmptyKeyRejected(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "unseal-empty", "Unseal Empty", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	err := svc.TenantBarrierUnseal(WithTenantID(ctx, "unseal-empty"), "unseal-empty", nil)
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("expected ErrEmptyPassphrase, got %v", err)
	}
}

// ---------- TenantBarrierUnseal ----------

func TestTenantService_BarrierUnseal(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "unseal-test", "Unseal Test", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Initialize first.
	if _, err := svc.TenantBarrierInit(ctx, "unseal-test", []byte("unseal-test-pw"), 0, 0); err != nil {
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

	// Unseal via service with the caller-supplied key material. (The
	// pre-registered default barrier uses a fixed software-strategy master
	// password, so the supplied key is accepted as Credentials.Secret; the
	// derived "tenant-init-" fallback has been removed.)
	if err := svc.TenantBarrierUnseal(WithTenantID(ctx, "unseal-test"), "unseal-test", []byte("unseal-test-pw")); err != nil {
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

	_, err := svc.CreateTenant(ctx, "unseal-key", "Unseal Key", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Initialize the barrier with a caller-supplied passphrase.
	if _, err := svc.TenantBarrierInit(ctx, "unseal-key", []byte("unseal-key-pw"), 0, 0); err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}

	// Seal and try to unseal with explicit key material.
	if err := reg.SealTenant("unseal-key"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}

	// Unseal with explicit caller-supplied key material.
	err = svc.TenantBarrierUnseal(WithTenantID(ctx, "unseal-key"), "unseal-key", []byte("unseal-key-pw"))
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

	err := svc.TenantBarrierUnseal(WithTenantID(ctx, "nonexistent"), "nonexistent", []byte("pw"))
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestTenantService_BarrierUnsealEmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	err := svc.TenantBarrierUnseal(WithTenantID(ctx, ""), "", nil)
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantService_BarrierUnsealNotInitialized(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "no-init", "Not Initialized", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Unseal without initializing should fail with BarrierError.
	err = svc.TenantBarrierUnseal(WithTenantID(ctx, "no-init"), "no-init", []byte("pw"))
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
		{key: metadataPrefix + "acme", want: "acme"},
		{key: metadataPrefix, want: ""},
		{key: "tenants/acme", want: ""}, // legacy prefix — not matched after Bug 2 fix
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

	_, err := svc.CreateTenant(ctx, "quota-test", "Quota Test", "")
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

	_, err = svc.CreateTenant(ctx, "put-fault", "Put Fault", "")
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

	_, err := svc.CreateTenant(ctx, "qp-test", "QP Test", "")
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

	_, err := svc.CreateTenant(ctx, "no-quota", "No Quota", "")
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

	_, err := svc.CreateTenant(ctx, "rt-test", "Round Trip", "")
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

// ---------------------------------------------------------------------------
// Reserved tenant id rejection (full TenantService path)
// ---------------------------------------------------------------------------

func TestTenantService_CreateRejectsSystemID(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.CreateTenant(context.Background(), tenantid.System, "System", "")
	var rerr *ReservedTenantIDError
	if !errors.As(err, &rerr) {
		t.Fatalf("expected *ReservedTenantIDError, got %T: %v", err, err)
	}
}

func TestTenantService_CreateRejectsLegacyDefaultID(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.CreateTenant(context.Background(), "default", "Default", "")
	var rerr *ReservedTenantIDError
	if !errors.As(err, &rerr) {
		t.Fatalf("expected *ReservedTenantIDError, got %T: %v", err, err)
	}
}

func TestTenantService_CreateRejectsUnderscorePrefix(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.CreateTenant(context.Background(), "_secret", "Secret", "")
	var rerr *ReservedTenantIDError
	if !errors.As(err, &rerr) {
		t.Fatalf("expected *ReservedTenantIDError, got %T: %v", err, err)
	}
}

func TestTenantService_CreateRejectsColonInfix(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.CreateTenant(context.Background(), "scope:value", "Scoped", "")
	var rerr *ReservedTenantIDError
	if !errors.As(err, &rerr) {
		t.Fatalf("expected *ReservedTenantIDError, got %T: %v", err, err)
	}
}

func TestTenantService_CreateInternalCallAllowsSystemID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := WithInternalCall(context.Background())
	info, err := svc.CreateTenant(ctx, tenantid.System, "System", "")
	if err != nil {
		t.Fatalf("internal-call CreateTenant(system): %v", err)
	}
	if info.ID != tenantid.System {
		t.Errorf("ID = %q, want %q", info.ID, tenantid.System)
	}
}

// TestTenantService_CheckBarrierMode_Governance proves the barrier-mode
// governance gate: dedicated modes are gated by AllowedBarrierModes, the shared
// system-barrier option by SystemBarrierQuota, and the system tenant is exempt.
func TestTenantService_CheckBarrierMode_Governance(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	const tid = "gov"
	if _, err := svc.CreateTenant(ctx, tid, "Gov", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Default entitlements (empty AllowedBarrierModes) -> software allowed.
	if err := svc.CheckBarrierMode(ctx, tid, seal.StrategyPassphrase); err != nil {
		t.Fatalf("passphrase should be allowed by default: %v", err)
	}
	// Hardware denied without an explicit grant.
	if err := svc.CheckBarrierMode(ctx, tid, seal.StrategyID("tpm2")); err == nil {
		t.Fatal("tpm2 must be denied without an explicit AllowedBarrierModes grant")
	}

	// System-barrier option denied while SystemBarrierQuota == 0 (default).
	if err := svc.CheckBarrierMode(ctx, tid, seal.StrategySystemBarrier); err == nil {
		t.Fatal("system-barrier option must be denied with SystemBarrierQuota=0")
	}
	// Granting the quota enables it.
	if _, err := svc.UpdateQuotas(ctx, tid, &quota.Quotas{SystemBarrierQuota: 1 << 20}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}
	if err := svc.CheckBarrierMode(ctx, tid, seal.StrategySystemBarrier); err != nil {
		t.Fatalf("system-barrier should be allowed with SystemBarrierQuota>0: %v", err)
	}

	// Granting tpm2 explicitly enables it.
	if _, err := svc.UpdateEntitlements(ctx, tid, &quota.Entitlements{AllowedBarrierModes: []string{"tpm2"}}); err != nil {
		t.Fatalf("UpdateEntitlements: %v", err)
	}
	if err := svc.CheckBarrierMode(ctx, tid, seal.StrategyID("tpm2")); err != nil {
		t.Fatalf("tpm2 should be allowed once granted: %v", err)
	}

	// The system tenant is exempt from all gates.
	if err := svc.CheckBarrierMode(ctx, tenantid.System, seal.StrategySystemBarrier); err != nil {
		t.Fatalf("system tenant must be exempt: %v", err)
	}
}
