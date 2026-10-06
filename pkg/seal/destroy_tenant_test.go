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

package seal

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// newPooledTestRegistry builds a BarrierRegistry over a real single-path
// local volume (via newTestVolumeFixture's real "dir" provider and
// Catalog) and a real per-tenant Pebble registry, with its system barrier
// initialized and unsealed.
//
// Everything here is the production type. DestroyTenant's whole job is to
// remove records from the system barrier store, close a real Pebble and
// unlink a real directory, so a double for any of the three would remove
// exactly the thing under test.
func newPooledTestRegistry(t *testing.T, extra ...Option) (*BarrierRegistry, *StorageBarrier) {
	t.Helper()
	catalog, open, _ := newTestVolumeFixture(t)
	return newPooledTestRegistryWith(t, catalog, open, extra...)
}

// newPooledTestRegistryAt is newPooledTestRegistry pinned to a caller-chosen
// pool path (newTestVolumeFixtureAt), for a guard test that needs an exact
// pool path shape rather than a fresh t.TempDir().
func newPooledTestRegistryAt(t *testing.T, poolRoot string, extra ...Option) (*BarrierRegistry, *StorageBarrier) {
	t.Helper()
	catalog, open := newTestVolumeFixtureAt(t, poolRoot)
	return newPooledTestRegistryWith(t, catalog, open, extra...)
}

// newPooledTestRegistryAtAlias is newPooledTestRegistryAt, but the pool's
// Volume reports reportedRoot from Root() instead of realRoot (see
// aliasVolume / newTestVolumeFixtureAtAlias), for a guard test proving
// containment through a symlinked pool path.
func newPooledTestRegistryAtAlias(t *testing.T, realRoot, reportedRoot string, extra ...Option) (*BarrierRegistry, *StorageBarrier) {
	t.Helper()
	catalog, open := newTestVolumeFixtureAtAlias(t, realRoot, reportedRoot)
	return newPooledTestRegistryWith(t, catalog, open, extra...)
}

func newPooledTestRegistryWith(t *testing.T, catalog *volume.Catalog, open *volume.OpenVolumes, extra ...Option) (*BarrierRegistry, *StorageBarrier) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	strategy, err := NewSoftwareStrategy(context.Background(), []byte("destroy-tenant-system-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	system, err := NewStorageBarrier(logger, NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "system/root-key"}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := system.Initialize(context.Background(), Credentials{}); err != nil {
		t.Fatalf("system Initialize: %v", err)
	}

	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	opts := append([]Option{
		WithVolumeAllocator(catalog),
		WithVolumeResolver(open),
		WithPerTenantRegistry(perTenantReg),
	}, extra...)
	reg, err := NewBarrierRegistry(logger, system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	return reg, system
}

// ---------------------------------------------------------------------------
// go-qrdb#413 -- UnregisterTenant is not a delete
//
// UnregisterTenant seals and forgets the IN-MEMORY barrier. Two durable
// records outlive it: the DataRoot at "__registry:tenant_root/{id}" and the
// volume-pool slot assignment. RestoreVolumeTenants reads the first at boot,
// so a tenant deleted through UnregisterTenant came back on the next restart,
// at the same directory, over the same store. DestroyTenant is the operation
// a deletion needs.
// ---------------------------------------------------------------------------

// reserveTestVolumeTenant reserves tenantID on reg's configured "default"
// pool via its volumeAllocator (same package, unexported field), the step
// RegisterVolumeTenant/ReattachVolumeTenant now require their caller to
// have already taken (see barrier_registry.go's doc comments) -- the
// catalog's Reserve replaces the old implicit "pick a fresh path" branch.
// reserveBytes is 0: these tests are not about quota.
func reserveTestVolumeTenant(t *testing.T, reg *BarrierRegistry, tenantID string) {
	t.Helper()
	allocator, _ := reg.volumeResources()
	if allocator == nil {
		t.Fatalf("reserveTestVolumeTenant(%s): registry has no volume allocator configured", tenantID)
	}
	if _, err := allocator.Reserve(context.Background(), tenantID, "default", tenantID, 0); err != nil {
		t.Fatalf("Reserve(%s): %v", tenantID, err)
	}
}

// TestDestroyTenant_RemovesTheRecordsThatWouldRestoreIt is the test that
// separates DestroyTenant from UnregisterTenant.
func TestDestroyTenant_RemovesTheRecordsThatWouldRestoreIt(t *testing.T) {
	ctx := context.Background()
	reg, _ := newPooledTestRegistry(t)
	allocator, _ := reg.volumeResources()

	reserveTestVolumeTenant(t, reg, "acme")
	dataRoot, _, err := reg.RegisterVolumeTenant(ctx, "acme", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	if err := reg.InitializeTenant(ctx, "acme", Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}
	if _, err := allocator.Resolve(ctx, "acme"); err != nil {
		t.Fatalf("the fixture did not persist a catalog assignment: %v", err)
	}

	if err := reg.DestroyTenant(ctx, "acme"); err != nil {
		t.Fatalf("DestroyTenant: %v", err)
	}

	var notFound *volume.AssignmentNotFoundError
	if _, err := allocator.Resolve(ctx, "acme"); !errors.As(err, &notFound) {
		t.Errorf("the catalog assignment survived DestroyTenant: %v", err)
	}
	if _, err := reg.GetTenant("acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("GetTenant after destroy = %v, want ErrTenantNotFound", err)
	}
	if _, err := os.Stat(dataRoot); !os.IsNotExist(err) {
		t.Errorf("the DataRoot %s survived DestroyTenant (stat err %v)", dataRoot, err)
	}

	// The boot path must not bring it back.
	if err := reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants: %v", err)
	}
	if _, err := reg.GetTenant("acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("a destroyed tenant was restored at boot: %v", err)
	}
}

// TestDestroyTenant_IsIdempotent: a destroy that is re-issued after an
// interrupted one must finish rather than fail, which is what makes
// DeleteTenant re-issuable.
func TestDestroyTenant_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	reg, _ := newPooledTestRegistry(t)

	reserveTestVolumeTenant(t, reg, "acme")
	if _, _, err := reg.RegisterVolumeTenant(ctx, "acme", true); err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	if err := reg.DestroyTenant(ctx, "acme"); err != nil {
		t.Fatalf("first DestroyTenant: %v", err)
	}
	// The second call has nothing registered, which is reported as
	// ErrTenantNotFound so a caller can tell "was never here" from "failed".
	if err := reg.DestroyTenant(ctx, "acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("second DestroyTenant = %v, want ErrTenantNotFound", err)
	}
}

// TestDestroyTenant_EmptyIDIsRefused guards against a destroy with no scope.
func TestDestroyTenant_EmptyIDIsRefused(t *testing.T) {
	reg, _ := newPooledTestRegistry(t)
	if err := reg.DestroyTenant(context.Background(), ""); !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("DestroyTenant(\"\") = %v, want ErrEmptyTenantID", err)
	}
}

// TestDestroyTenant_KeyspaceModeTenantHasNoDataRoot: an IsolationBarrier
// tenant registered with an explicit in-memory backend has no dedicated
// directory, so the filesystem step is skipped rather than guessed at.
func TestDestroyTenant_KeyspaceModeTenantHasNoDataRoot(t *testing.T) {
	ctx := context.Background()
	reg, _ := newPooledTestRegistry(t)
	allocator, _ := reg.volumeResources()

	if _, err := reg.RegisterTenantDefault("acme"); err != nil {
		t.Fatalf("RegisterTenantDefault: %v", err)
	}
	if err := reg.DestroyTenant(ctx, "acme"); err != nil {
		t.Fatalf("DestroyTenant: %v", err)
	}
	if _, err := reg.GetTenant("acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("GetTenant after destroy = %v, want ErrTenantNotFound", err)
	}
	var notFound *volume.AssignmentNotFoundError
	if _, err := allocator.Resolve(ctx, "acme"); !errors.As(err, &notFound) {
		t.Errorf("a keyspace-mode tenant must have no catalog assignment: %v", err)
	}
}

// TestDestroyTenant_InterimShapedRecordIsCleanedUpNotRefused proves
// decision D9(a) actually lets an operator finish deleting a tenant
// stuck with a record written by the pre-catalog interim fix ({pool,
// slot}, no Path field). Resolve refuses such a record with
// *volume.InvalidAssignmentPathError, which before this fix was treated
// by DestroyTenant as a hard failure (it only tolerated
// *AssignmentNotFoundError) -- so there was NO way to ever finish
// deleting such a tenant, the opposite of D9's own ruling ("the tenant is
// deleted and re-created").
func TestDestroyTenant_InterimShapedRecordIsCleanedUpNotRefused(t *testing.T) {
	ctx := context.Background()
	poolRoot := t.TempDir()

	registry := volume.NewRegistry()
	provider, err := registry.Lookup("dir")
	if err != nil {
		t.Fatalf("registry.Lookup(dir): %v", err)
	}
	vol, err := provider.Open(ctx, volume.Spec{Pool: "default", NodeID: "test-node", Path: poolRoot})
	if err != nil {
		t.Fatalf("provider.Open: %v", err)
	}
	t.Cleanup(func() { _ = vol.Close(context.Background()) })
	open := volume.NewOpenVolumes("test-node")
	if err := open.Add("default", vol); err != nil {
		t.Fatalf("open.Add: %v", err)
	}

	kv := newSealTestFakeKV()
	tx := newSealTestFakeTx(kv)
	catalog, err := volume.NewCatalog(kv, tx, registry)
	if err != nil {
		t.Fatalf("volume.NewCatalog: %v", err)
	}
	if _, err := catalog.CreatePool(ctx, volume.CreatePoolRequest{Name: "default", Provider: "dir"}); err != nil {
		t.Fatalf("catalog.CreatePool: %v", err)
	}

	// Seed the interim-shaped record directly into the catalog's own KV,
	// bypassing Catalog.Reserve (which validates Path and would refuse
	// this) -- exactly as the real, pre-catalog interim fix wrote it.
	if err := kv.Put(ctx, volume.AssignmentKey("legacy-tenant"), []byte(`{"pool":"default","slot":2}`)); err != nil {
		t.Fatalf("seed interim record: %v", err)
	}

	system := newSystemBarrier(t)
	initBarrier(t, system)
	reg, err := NewBarrierRegistry(testLogger(), system,
		WithVolumeAllocator(catalog), WithVolumeResolver(open))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	// Confirm this really is the shape under test: Resolve refuses it.
	var invalidPath *volume.InvalidAssignmentPathError
	if _, err := catalog.Resolve(ctx, "legacy-tenant"); !errors.As(err, &invalidPath) {
		t.Fatalf("Resolve(interim-shaped) error = %v, want *volume.InvalidAssignmentPathError", err)
	}

	// "legacy-tenant" was never registered in memory (only a raw catalog
	// record), so DestroyTenant returns ErrTenantNotFound by its own
	// documented idempotency contract -- the SAME return value a
	// second destroy of an already-destroyed tenant gets. What matters
	// for D9 is that the record is actually gone afterward, asserted
	// below; a hard failure here would mean the fix did not take.
	if err := reg.DestroyTenant(ctx, "legacy-tenant"); err != nil && !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("DestroyTenant(interim-shaped) = %v, want nil or ErrTenantNotFound", err)
	}

	// The record is gone, not merely tolerated in place.
	var notFound *volume.AssignmentNotFoundError
	if _, err := catalog.Resolve(ctx, "legacy-tenant"); !errors.As(err, &notFound) {
		t.Errorf("the interim-shaped record survived DestroyTenant: %v", err)
	}
}

// TestDestroyTenant_LeavesOtherTenantsAlone: the destroy is scoped.
func TestDestroyTenant_LeavesOtherTenantsAlone(t *testing.T) {
	ctx := context.Background()
	reg, _ := newPooledTestRegistry(t)
	allocator, _ := reg.volumeResources()

	reserveTestVolumeTenant(t, reg, "acme")
	rootA, _, err := reg.RegisterVolumeTenant(ctx, "acme", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant(acme): %v", err)
	}
	reserveTestVolumeTenant(t, reg, "globex")
	rootB, _, err := reg.RegisterVolumeTenant(ctx, "globex", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant(globex): %v", err)
	}
	if rootA == rootB {
		t.Fatalf("both tenants were given the same DataRoot %s", rootA)
	}

	if err := reg.DestroyTenant(ctx, "acme"); err != nil {
		t.Fatalf("DestroyTenant: %v", err)
	}

	if _, err := reg.GetTenant("globex"); err != nil {
		t.Errorf("the other tenant's barrier was removed: %v", err)
	}
	if _, err := allocator.Resolve(ctx, "globex"); err != nil {
		t.Errorf("the other tenant's catalog assignment was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootB, "barrier")); err != nil {
		t.Errorf("the other tenant's store was removed: %v", err)
	}
}

// TestDestroyTenantErrors_NameWhatFailed covers the three typed failures.
//
// They are constructed directly rather than provoked. Each one wraps an
// operation the operating system decides the outcome of -- closing a Pebble,
// deleting a barrier record, unlinking a directory -- and the only portable
// way to provoke the third is to make a directory unremovable, which root
// ignores. A test that passed by being skipped on half the machines that run
// it would be asserting nothing there; what is worth pinning is that each
// error names the tenant, names what failed, and carries the cause.
func TestDestroyTenantErrors_NameWhatFailed(t *testing.T) {
	cause := errors.New("disk on fire")

	closeErr := &TenantStoreCloseError{TenantID: "acme", Err: cause}
	if !strings.Contains(closeErr.Error(), "acme") ||
		!strings.Contains(closeErr.Error(), cause.Error()) {
		t.Errorf("TenantStoreCloseError = %q, want the tenant and the cause", closeErr)
	}
	if !errors.Is(closeErr, cause) {
		t.Error("TenantStoreCloseError must unwrap to its cause")
	}

	recErr := &TenantRecordDeleteError{TenantID: "acme", Record: "tenant-root", Err: cause}
	if !strings.Contains(recErr.Error(), "tenant-root") ||
		!strings.Contains(recErr.Error(), "acme") {
		t.Errorf("TenantRecordDeleteError = %q, want the record and the tenant", recErr)
	}
	if !errors.Is(recErr, cause) {
		t.Error("TenantRecordDeleteError must unwrap to its cause")
	}

	rmErr := &TenantDataRootRemoveError{TenantID: "acme", DataRoot: "/srv/acme", Err: cause}
	if !strings.Contains(rmErr.Error(), "/srv/acme") ||
		!strings.Contains(rmErr.Error(), "acme") {
		t.Errorf("TenantDataRootRemoveError = %q, want the directory and the tenant", rmErr)
	}
	if !errors.Is(rmErr, cause) {
		t.Error("TenantDataRootRemoveError must unwrap to its cause")
	}
}

// TestDestroyTenant_RecoversTheDataRootFromThePersistedRecord: a node that has
// not re-attached the tenant in this process still knows where its data is,
// because the catalog's Assignment is the authority, resolved fresh on every
// call -- there is no in-memory DataRoot cache to go stale. Without this, a
// destroy issued on a follower would remove the records and leave the
// directory.
func TestDestroyTenant_RecoversTheDataRootFromThePersistedRecord(t *testing.T) {
	ctx := context.Background()
	reg, _ := newPooledTestRegistry(t)

	reserveTestVolumeTenant(t, reg, "acme")
	dataRoot, _, err := reg.RegisterVolumeTenant(ctx, "acme", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	// Simulate a process that never re-attached: drop the in-memory handle,
	// leaving only the catalog's Assignment.
	if err := reg.RemoveTenant("acme"); err != nil {
		t.Fatalf("RemoveTenant: %v", err)
	}

	if err := reg.DestroyTenant(ctx, "acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("DestroyTenant = %v, want ErrTenantNotFound (nothing registered here)", err)
	}
	if _, err := os.Stat(dataRoot); !os.IsNotExist(err) {
		t.Errorf("the DataRoot %s survived a destroy driven by the persisted record (stat err %v)",
			dataRoot, err)
	}
}
