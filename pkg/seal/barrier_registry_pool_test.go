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

package seal_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/local"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
)

// osReadDir is a thin alias for os.ReadDir kept here so test helpers
// reference a single seam if we ever swap it out for a fake fs.
var osReadDir = os.ReadDir

// poolTestLogger returns a discard logger to avoid noisy test output.
func poolTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newSoftwareStrategy returns a SoftwareStrategy with a deterministic
// passphrase. Used by every pooled-tenant test that needs to register a
// barrier against a fresh per-tenant Pebble.
func newPoolSoftwareStrategy(t *testing.T) *seal.SoftwareStrategy {
	t.Helper()
	s, err := seal.NewSoftwareStrategy([]byte("pool-test-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	return s
}

// newPoolSystemBarrier builds a StorageBarrier suitable as the registry's
// system barrier in pool tests.
func newPoolSystemBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	store := seal.NewMemoryBackend()
	b, err := seal.NewStorageBarrier(poolTestLogger(), store, seal.BarrierConfig{
		RootKeyPath: "system/root-key",
	}, newPoolSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	return b
}

// configuredLocal opens a fresh local-filesystem objstore Backend at a
// temp directory. The directory is t-scoped so cleanup is automatic.
func configuredLocal(t *testing.T) (common.Storage, string) {
	t.Helper()
	dir := t.TempDir()
	s := local.New()
	if err := s.Configure(map[string]string{"path": dir}); err != nil {
		t.Fatalf("Configure local backend: %v", err)
	}
	return s, dir
}

// newStateStore opens a real Pebble at a fresh temp dir and wraps it
// with a qrdb PebbleStateStore. It returns the state store and the
// raw *PebbleStorage (for restart simulation) plus the path.
func newStateStore(t *testing.T) (*qrdbpool.PebbleStateStore, *pebblestore.PebbleStorage, string) {
	t.Helper()
	dir := t.TempDir()
	storage, err := pebblestore.New(&pebblestore.Config{DataDir: dir})
	if err != nil {
		t.Fatalf("open state-store Pebble: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	store, err := qrdbpool.NewPebbleStateStore(storage)
	if err != nil {
		t.Fatalf("NewPebbleStateStore: %v", err)
	}
	return store, storage, dir
}

// newPooledRegistry builds a BarrierRegistry with a two-path local
// volume pool, a Pebble-backed state store, and a per-tenant Pebble
// registry. Returns the registry plus the two pool path roots so tests
// can assert which root each tenant's Pebble landed under.
func newPooledRegistry(t *testing.T) (*seal.BarrierRegistry, []string, pebblestore.PerTenantRegistry) {
	t.Helper()

	c1, p1 := configuredLocal(t)
	c2, p2 := configuredLocal(t)

	mgr := objpool.NewManager()
	state, _, _ := newStateStore(t)
	rr := &objpool.RoundRobinStrategy{State: state}
	if err := mgr.Register("default", []common.Storage{c1, c2}, rr); err != nil {
		t.Fatalf("pool Register: %v", err)
	}

	system := newPoolSystemBarrier(t)
	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	reg, err := seal.NewBarrierRegistry(
		poolTestLogger(),
		system,
		seal.WithVolumePool(mgr, state, "default"),
		seal.WithPerTenantRegistry(perTenantReg),
	)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	return reg, []string{p1, p2}, perTenantReg
}

func TestRegisterTenant_PooledFlow_RoundRobinAcrossTwoPaths(t *testing.T) {
	reg, roots, perTenantReg := newPooledRegistry(t)
	ctx := context.Background()

	// First tenant lands at root[0].
	tb1, err := reg.RegisterTenant(ctx, "alpha", nil, seal.BarrierConfig{}, newPoolSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("RegisterTenant(alpha): %v", err)
	}
	if tb1 == nil {
		t.Fatal("expected non-nil TenantBarrier for alpha")
	}

	storageAlpha, err := perTenantReg.Get("alpha")
	if err != nil {
		t.Fatalf("perTenantReg.Get(alpha): %v", err)
	}
	if storageAlpha == nil {
		t.Fatal("perTenantReg returned nil storage for alpha")
	}

	// Second tenant lands at root[1].
	if _, err := reg.RegisterTenant(ctx, "beta", nil, seal.BarrierConfig{}, newPoolSoftwareStrategy(t)); err != nil {
		t.Fatalf("RegisterTenant(beta): %v", err)
	}

	registered := perTenantReg.List()
	if len(registered) != 2 {
		t.Fatalf("perTenantReg has %d tenants, want 2 (got %v)", len(registered), registered)
	}

	// Verify the picked paths are distinct and cover both roots.
	expectedAlpha := filepath.Join(roots[0], "alpha")
	expectedBeta := filepath.Join(roots[1], "beta")
	assertPebbleAtPath(t, expectedAlpha)
	assertPebbleAtPath(t, expectedBeta)
}

func TestRegisterTenant_PooledFlow_CursorPersistsAcrossRestart(t *testing.T) {
	c1, p1 := configuredLocal(t)
	c2, p2 := configuredLocal(t)

	stateDir := t.TempDir()

	// Boot 1: register two tenants. The cursor advances to 2.
	storage1, err := pebblestore.New(&pebblestore.Config{DataDir: stateDir})
	if err != nil {
		t.Fatalf("open state-store Pebble (boot 1): %v", err)
	}
	state1, err := qrdbpool.NewPebbleStateStore(storage1)
	if err != nil {
		t.Fatalf("NewPebbleStateStore (boot 1): %v", err)
	}

	mgr1 := objpool.NewManager()
	rr1 := &objpool.RoundRobinStrategy{State: state1}
	if err := mgr1.Register("default", []common.Storage{c1, c2}, rr1); err != nil {
		t.Fatalf("pool Register (boot 1): %v", err)
	}
	system1 := newPoolSystemBarrier(t)
	perTenantReg1 := pebblestore.NewPerTenantRegistry()

	reg1, err := seal.NewBarrierRegistry(poolTestLogger(), system1,
		seal.WithVolumePool(mgr1, state1, "default"),
		seal.WithPerTenantRegistry(perTenantReg1),
	)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (boot 1): %v", err)
	}

	ctx := context.Background()
	if _, err := reg1.RegisterTenant(ctx, "alpha", nil, seal.BarrierConfig{}, newPoolSoftwareStrategy(t)); err != nil {
		t.Fatalf("RegisterTenant(alpha): %v", err)
	}
	if _, err := reg1.RegisterTenant(ctx, "beta", nil, seal.BarrierConfig{}, newPoolSoftwareStrategy(t)); err != nil {
		t.Fatalf("RegisterTenant(beta): %v", err)
	}

	if err := perTenantReg1.CloseAll(ctx); err != nil {
		t.Fatalf("CloseAll (boot 1): %v", err)
	}
	if err := storage1.Close(); err != nil {
		t.Fatalf("close state-store (boot 1): %v", err)
	}

	// Boot 2: re-open the same state store. Cursor should be at 2,
	// so the next pick lands at index 0 (root[0] = p1).
	storage2, err := pebblestore.New(&pebblestore.Config{DataDir: stateDir})
	if err != nil {
		t.Fatalf("open state-store Pebble (boot 2): %v", err)
	}
	defer func() { _ = storage2.Close() }()

	state2, err := qrdbpool.NewPebbleStateStore(storage2)
	if err != nil {
		t.Fatalf("NewPebbleStateStore (boot 2): %v", err)
	}

	mgr2 := objpool.NewManager()
	rr2 := &objpool.RoundRobinStrategy{State: state2}
	if err := mgr2.Register("default", []common.Storage{c1, c2}, rr2); err != nil {
		t.Fatalf("pool Register (boot 2): %v", err)
	}
	system2 := newPoolSystemBarrier(t)
	perTenantReg2 := pebblestore.NewPerTenantRegistry()
	defer func() { _ = perTenantReg2.CloseAll(ctx) }()

	reg2, err := seal.NewBarrierRegistry(poolTestLogger(), system2,
		seal.WithVolumePool(mgr2, state2, "default"),
		seal.WithPerTenantRegistry(perTenantReg2),
	)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (boot 2): %v", err)
	}

	if _, err := reg2.RegisterTenant(ctx, "gamma", nil, seal.BarrierConfig{}, newPoolSoftwareStrategy(t)); err != nil {
		t.Fatalf("RegisterTenant(gamma): %v", err)
	}

	// gamma must land at p1 (cursor = 2 % 2 = 0).
	expected := filepath.Join(p1, "gamma")
	assertPebbleAtPath(t, expected)
	// Sanity: not at p2.
	if _, err := pebbleExists(filepath.Join(p2, "gamma")); err == nil {
		t.Errorf("gamma unexpectedly opened at p2 root %q", p2)
	}
}

func TestRegisterTenant_PooledFlow_NoPoolConfigured(t *testing.T) {
	system := newPoolSystemBarrier(t)
	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	_, err = reg.RegisterTenant(context.Background(), "gamma", nil, seal.BarrierConfig{}, newPoolSoftwareStrategy(t))
	if !errors.Is(err, seal.ErrPoolNotConfigured) {
		t.Errorf("RegisterTenant nil backend without pool: got %v, want ErrPoolNotConfigured", err)
	}
}

func TestRegisterTenant_PooledFlow_NoPerTenantRegistryConfigured(t *testing.T) {
	c1, _ := configuredLocal(t)

	mgr := objpool.NewManager()
	state, _, _ := newStateStore(t)
	rr := &objpool.RoundRobinStrategy{State: state}
	if err := mgr.Register("default", []common.Storage{c1}, rr); err != nil {
		t.Fatalf("pool Register: %v", err)
	}

	system := newPoolSystemBarrier(t)
	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system,
		seal.WithVolumePool(mgr, state, "default"),
		// Intentionally no PerTenantRegistry.
	)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	_, err = reg.RegisterTenant(context.Background(), "alpha", nil, seal.BarrierConfig{}, newPoolSoftwareStrategy(t))
	if !errors.Is(err, seal.ErrPerTenantRegistryNotConfigured) {
		t.Errorf("RegisterTenant nil backend without per-tenant registry: got %v, want ErrPerTenantRegistryNotConfigured", err)
	}
}

func TestRegisterTenant_PooledFlow_RejectsCloudBackend(t *testing.T) {
	// fakeCloudStorage is a common.Storage that does NOT satisfy
	// PathAccessor — exactly the shape of an S3/GCS backend in
	// production. Used here without LocalOnly so we can directly
	// exercise the BarrierRegistry's PathAccessor type-assertion
	// fallback.
	cloud := &fakeCloudStorage{}

	mgr := objpool.NewManager()
	state, _, _ := newStateStore(t)
	rr := &objpool.RoundRobinStrategy{State: state}
	if err := mgr.Register("default", []common.Storage{cloud}, rr); err != nil {
		t.Fatalf("pool Register: %v", err)
	}

	system := newPoolSystemBarrier(t)
	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system,
		seal.WithVolumePool(mgr, state, "default"),
		seal.WithPerTenantRegistry(perTenantReg),
	)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	_, err = reg.RegisterTenant(context.Background(), "delta", nil, seal.BarrierConfig{}, newPoolSoftwareStrategy(t))
	if !errors.Is(err, seal.ErrPoolBackendNotLocal) {
		t.Errorf("RegisterTenant with cloud backend: got %v, want ErrPoolBackendNotLocal", err)
	}
}

func TestRegisterTenant_PooledFlow_LocalOnlyRejectsCloud(t *testing.T) {
	// Same shape as above but verifying the LocalOnly wrapper rejects
	// the cloud backend with NonLocalCandidateError before the seal
	// registry's own check fires. This is the path qrdb takes in
	// start.go (LocalOnly always wraps the production manager).
	cloud := &fakeCloudStorage{}

	innerMgr := objpool.NewManager()
	state, _, _ := newStateStore(t)
	rr := &objpool.RoundRobinStrategy{State: state}
	if err := innerMgr.Register("default", []common.Storage{cloud}, rr); err != nil {
		t.Fatalf("pool Register: %v", err)
	}
	mgr := objpool.LocalOnly(innerMgr)

	system := newPoolSystemBarrier(t)
	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system,
		seal.WithVolumePool(mgr, state, "default"),
		seal.WithPerTenantRegistry(perTenantReg),
	)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	_, err = reg.RegisterTenant(context.Background(), "delta", nil, seal.BarrierConfig{}, newPoolSoftwareStrategy(t))
	var nonLocal *objpool.NonLocalCandidateError
	if !errors.As(err, &nonLocal) {
		t.Errorf("RegisterTenant via LocalOnly with cloud backend: got %v, want NonLocalCandidateError", err)
	}
}

func TestRegisterTenant_KeyspaceFlow_UnchangedWhenBackendProvided(t *testing.T) {
	// Verify that the legacy keyspace-mode flow (caller supplies a
	// real backend) still works after the pool integration. No pool
	// is configured; the in-memory backend round-trips through the
	// existing direct-construction path.
	system := newPoolSystemBarrier(t)
	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	tb, err := reg.RegisterTenant(context.Background(),
		"keyspace-tenant",
		seal.NewMemoryBackend(),
		seal.BarrierConfig{},
		newPoolSoftwareStrategy(t),
	)
	if err != nil {
		t.Fatalf("RegisterTenant (keyspace): %v", err)
	}
	if tb.TenantID() != "keyspace-tenant" {
		t.Errorf("TenantID = %q, want %q", tb.TenantID(), "keyspace-tenant")
	}
}

func TestBarrierRegistry_AttachOption_LateBinding(t *testing.T) {
	// Validates that Attach can wire the volume pool post-construction
	// — the start.go path where the registry is created in
	// initSealSubsystem and the pool is attached after firstboot.
	c1, _ := configuredLocal(t)

	mgr := objpool.NewManager()
	state, _, _ := newStateStore(t)
	rr := &objpool.RoundRobinStrategy{State: state}
	if err := mgr.Register("default", []common.Storage{c1}, rr); err != nil {
		t.Fatalf("pool Register: %v", err)
	}

	system := newPoolSystemBarrier(t)
	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	// Before Attach, the pooled flow must error.
	_, err = reg.RegisterTenant(context.Background(), "early", nil, seal.BarrierConfig{}, newPoolSoftwareStrategy(t))
	if !errors.Is(err, seal.ErrPoolNotConfigured) {
		t.Fatalf("expected ErrPoolNotConfigured before Attach, got %v", err)
	}

	// Attach pool + per-tenant registry.
	reg.Attach(
		seal.WithVolumePool(mgr, state, "default"),
		seal.WithPerTenantRegistry(perTenantReg),
	)

	// Now the pooled flow succeeds.
	if _, err := reg.RegisterTenant(context.Background(), "late", nil, seal.BarrierConfig{}, newPoolSoftwareStrategy(t)); err != nil {
		t.Errorf("RegisterTenant after Attach: %v", err)
	}
	if got := reg.PerTenantPebbleRegistry(); got != perTenantReg {
		t.Errorf("PerTenantPebbleRegistry returned %v, want injected registry", got)
	}
}

// assertPebbleAtPath verifies a Pebble database directory has been
// created at the supplied path. We can't assume the database has any
// keys yet — only that pebble.Open succeeded and produced its on-disk
// structure (CURRENT, OPTIONS-*, etc.).
func assertPebbleAtPath(t *testing.T, path string) {
	t.Helper()
	exists, err := pebbleExists(path)
	if err != nil {
		t.Fatalf("pebbleExists(%q): %v", path, err)
	}
	if !exists {
		t.Errorf("expected Pebble at %q, none found", path)
	}
}

// pebbleExists reports whether a directory looks like a Pebble store
// (has at least one CURRENT or OPTIONS-* file).
func pebbleExists(path string) (bool, error) {
	entries, err := readDir(path)
	if err != nil {
		return false, err
	}
	for _, name := range entries {
		if name == "CURRENT" || strings.HasPrefix(name, "OPTIONS-") {
			return true, nil
		}
	}
	return false, nil
}

// readDir lists entries in a directory by name, returning an empty
// slice when the directory does not exist.
func readDir(path string) ([]string, error) {
	entries, err := osReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out, nil
}

// newShareableComponents builds the primitive components (system barrier,
// pool manager, per-tenant Pebble registry) needed to create multiple
// BarrierRegistry instances that share the same underlying persistent state.
// This allows tests to simulate a "restart" or a "follower node": a fresh
// BarrierRegistry is created with the same system barrier and pool, but with
// empty in-memory maps (tenants, tenantDataRoots).
//
// The returned attachOptions slice is pre-built from the shared pool manager
// and per-tenant registry; pass it to seal.NewBarrierRegistry via the
// variadic opts argument. The roots slice contains the two pool-root paths so
// callers can assert DataRoot placement.
func newShareableComponents(t *testing.T) (
	system *seal.StorageBarrier,
	attachOptions []seal.Option,
	roots []string,
) {
	t.Helper()

	c1, p1 := configuredLocal(t)
	c2, p2 := configuredLocal(t)
	mgr := objpool.NewManager()
	state, _, _ := newStateStore(t)
	rr := &objpool.RoundRobinStrategy{State: state}
	if err := mgr.Register("default", []common.Storage{c1, c2}, rr); err != nil {
		t.Fatalf("newShareableComponents pool Register: %v", err)
	}

	system = newPoolSystemBarrier(t)
	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	attachOptions = []seal.Option{
		seal.WithVolumePool(mgr, state, "default"),
		seal.WithPerTenantRegistry(perTenantReg),
	}
	roots = []string{p1, p2}
	return
}

// fakeCloudStorage is a common.Storage stub used to drive the
// non-local-backend test paths. It deliberately does not implement
// common.PathAccessor.
type fakeCloudStorage struct{}

// Implementing a minimal common.Storage surface; only the methods
// the type assertion path touches are needed. Unimplemented ones
// panic to make any accidental real call obvious in tests.
func (f *fakeCloudStorage) Configure(map[string]string) error { return nil }
func (f *fakeCloudStorage) Put(string, io.Reader) error       { panic("fakeCloudStorage.Put") }
func (f *fakeCloudStorage) PutWithContext(context.Context, string, io.Reader) error {
	panic("fakeCloudStorage.PutWithContext")
}
func (f *fakeCloudStorage) PutWithMetadata(context.Context, string, io.Reader, *common.Metadata) error {
	panic("fakeCloudStorage.PutWithMetadata")
}
func (f *fakeCloudStorage) Get(string) (io.ReadCloser, error) { panic("fakeCloudStorage.Get") }
func (f *fakeCloudStorage) GetWithContext(context.Context, string) (io.ReadCloser, error) {
	panic("fakeCloudStorage.GetWithContext")
}
func (f *fakeCloudStorage) GetMetadata(context.Context, string) (*common.Metadata, error) {
	panic("fakeCloudStorage.GetMetadata")
}
func (f *fakeCloudStorage) UpdateMetadata(context.Context, string, *common.Metadata) error {
	panic("fakeCloudStorage.UpdateMetadata")
}
func (f *fakeCloudStorage) Delete(string) error { panic("fakeCloudStorage.Delete") }
func (f *fakeCloudStorage) DeleteWithContext(context.Context, string) error {
	panic("fakeCloudStorage.DeleteWithContext")
}
func (f *fakeCloudStorage) Exists(context.Context, string) (bool, error) {
	panic("fakeCloudStorage.Exists")
}
func (f *fakeCloudStorage) List(string) ([]string, error) { panic("fakeCloudStorage.List") }
func (f *fakeCloudStorage) ListWithContext(context.Context, string) ([]string, error) {
	panic("fakeCloudStorage.ListWithContext")
}
func (f *fakeCloudStorage) ListWithOptions(context.Context, *common.ListOptions) (*common.ListResult, error) {
	panic("fakeCloudStorage.ListWithOptions")
}
func (f *fakeCloudStorage) Archive(string, common.Archiver) error {
	panic("fakeCloudStorage.Archive")
}

// LifecycleManager surface — minimal no-ops, never called in these
// tests.
func (f *fakeCloudStorage) AddPolicy(common.LifecyclePolicy) error { return nil }
func (f *fakeCloudStorage) RemovePolicy(string) error              { return nil }
func (f *fakeCloudStorage) GetPolicies() ([]common.LifecyclePolicy, error) {
	return nil, nil
}
