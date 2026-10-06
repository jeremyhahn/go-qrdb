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
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// scriptedAllocator is a volume.Allocator double whose Resolve, ListAll and
// Release return the configured errors. Reserve and AdjustReservation are
// never reached by the tests that use it.
type scriptedAllocator struct {
	resolveErr error
	listErr    error
	releaseErr error
}

var _ volume.Allocator = (*scriptedAllocator)(nil)

func (a *scriptedAllocator) Resolve(context.Context, string) (*volume.Assignment, error) {
	return nil, a.resolveErr
}

func (a *scriptedAllocator) Reserve(context.Context, string, string, string, int64) (*volume.Assignment, error) {
	return nil, errors.New("scriptedAllocator: Reserve is not used by these tests")
}

func (a *scriptedAllocator) Release(context.Context, string) error { return a.releaseErr }

func (a *scriptedAllocator) ListAll(context.Context) ([]volume.Assignment, error) {
	return nil, a.listErr
}

func (a *scriptedAllocator) AdjustReservation(context.Context, string, int64) (*volume.Assignment, error) {
	return nil, errors.New("scriptedAllocator: AdjustReservation is not used by these tests")
}

// narrowAllocator exposes only the volume.Allocator half of a *volume.Catalog,
// hiding its volume.Service management surface.
type narrowAllocator struct{ volume.Allocator }

// scriptedResolver is a VolumeResolver double: Pools reports pools in order,
// and Root answers from roots or fails with rootErr for a pool not in it.
type scriptedResolver struct {
	pools   []string
	roots   map[string]string
	rootErr error
}

var _ VolumeResolver = (*scriptedResolver)(nil)

func (r *scriptedResolver) Root(_ context.Context, pool string) (string, error) {
	if root, ok := r.roots[pool]; ok {
		return root, nil
	}
	return "", r.rootErr
}

func (r *scriptedResolver) Pools() []string { return r.pools }

// newUnsealedRegistryWith builds a registry over an initialized (unsealed)
// pool-test system barrier with the given options.
func newUnsealedRegistryWith(t *testing.T, opts ...Option) *BarrierRegistry {
	t.Helper()
	system := newPoolSystemBarrier(t)
	if err := system.Initialize(context.Background(), MustCredentials(context.Background(), []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	reg, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	return reg
}

// TestBarrierRegistry_VolumeAccessorsWithoutAPool: a registry with no volume
// pool attached reports none through every accessor a composer gates the
// volume/raft tenant build on, so nothing downstream wires a pool that is not
// there.
func TestBarrierRegistry_VolumeAccessorsWithoutAPool(t *testing.T) {
	reg := newUnsealedRegistryWith(t)

	if reg.HasVolumePool() {
		t.Error("HasVolumePool() = true with no allocator or resolver attached")
	}
	if a := reg.VolumeAllocator(); a != nil {
		t.Errorf("VolumeAllocator() = %T, want nil", a)
	}
	if svc, ok := reg.VolumeService(); ok || svc != nil {
		t.Errorf("VolumeService() = (%T, %v), want (nil, false)", svc, ok)
	}
	if p := reg.PerTenantPebbleRegistry(); p != nil {
		t.Errorf("PerTenantPebbleRegistry() = %T, want nil", p)
	}
}

// TestBarrierRegistry_VolumeAccessorsWithAPool: with a catalog, resolver and
// per-tenant registry attached, the accessors hand back the SAME instances --
// every transport's pool-management endpoints and the tenant service's
// reservations must share one catalog, never a second copy.
func TestBarrierRegistry_VolumeAccessorsWithAPool(t *testing.T) {
	reg, _, perTenantReg := newPooledRegistry(t)
	allocator, _ := reg.volumeResources()

	if !reg.HasVolumePool() {
		t.Fatal("HasVolumePool() = false with an allocator and a resolver attached")
	}
	if got := reg.VolumeAllocator(); got != allocator {
		t.Errorf("VolumeAllocator() = %p, want the attached allocator %p", got, allocator)
	}
	svc, ok := reg.VolumeService()
	if !ok {
		t.Fatal("VolumeService() ok = false for a *volume.Catalog allocator")
	}
	if catalog, isCatalog := svc.(*volume.Catalog); !isCatalog || volume.Allocator(catalog) != allocator {
		t.Errorf("VolumeService() = %T, want the attached catalog", svc)
	}
	if got := reg.PerTenantPebbleRegistry(); got != perTenantReg {
		t.Errorf("PerTenantPebbleRegistry() = %p, want the attached registry %p", got, perTenantReg)
	}
}

// TestBarrierRegistry_VolumeServiceNeedsAManagementCapableAllocator: an
// allocator that resolves placement but cannot manage pools still makes the
// registry pool-capable, but offers no management service -- a transport must
// not be handed pool-management endpoints that do not exist.
func TestBarrierRegistry_VolumeServiceNeedsAManagementCapableAllocator(t *testing.T) {
	catalog, open, _ := newTestVolumeFixture(t)
	reg := newUnsealedRegistryWith(t,
		WithVolumeAllocator(narrowAllocator{catalog}),
		WithVolumeResolver(open),
	)

	if !reg.HasVolumePool() {
		t.Fatal("HasVolumePool() = false with an allocator and a resolver attached")
	}
	if reg.VolumeAllocator() == nil {
		t.Fatal("VolumeAllocator() = nil with an allocator attached")
	}
	if svc, ok := reg.VolumeService(); ok || svc != nil {
		t.Fatalf("VolumeService() = (%T, %v), want (nil, false) for a placement-only allocator", svc, ok)
	}
}

// TestBarrierRegistry_PoolBasePathsSkipsAPoolThatFailsToResolve: one pool
// failing to resolve must not drop every other pool from the residue guard's
// list -- the guard would then let the reconcile remove a path under a
// healthy pool's root.
func TestBarrierRegistry_PoolBasePathsSkipsAPoolThatFailsToResolve(t *testing.T) {
	healthyRoot := t.TempDir()
	reg := newUnsealedRegistryWith(t,
		WithVolumeAllocator(&scriptedAllocator{}),
		WithVolumeResolver(&scriptedResolver{
			pools:   []string{"broken", "healthy"},
			roots:   map[string]string{"healthy": healthyRoot},
			rootErr: &volume.PoolUnavailableError{Pool: "broken", Err: errors.New("not mounted")},
		}),
	)

	got, err := reg.PoolBasePaths(context.Background())
	if err != nil {
		t.Fatalf("PoolBasePaths: %v", err)
	}
	if want := []string{healthyRoot}; !slices.Equal(got, want) {
		t.Fatalf("PoolBasePaths() = %v, want %v", got, want)
	}
}

// TestBarrierRegistry_HasTenantStateEdges covers the answers HasTenantState
// gives from the catalog alone: no catalog means no state; an interim-shaped
// record that Resolve refuses is still state that needs reclaiming; and a
// lookup that fails for any other reason is an error, never a "no".
func TestBarrierRegistry_HasTenantStateEdges(t *testing.T) {
	ctx := context.Background()
	errCatalogDown := errors.New("catalog unavailable")

	t.Run("no catalog", func(t *testing.T) {
		reg := newUnsealedRegistryWith(t)
		has, err := reg.HasTenantState(ctx, "acme")
		if err != nil || has {
			t.Fatalf("HasTenantState() = (%v, %v), want (false, nil)", has, err)
		}
	})
	t.Run("unusable record still counts", func(t *testing.T) {
		reg := newUnsealedRegistryWith(t,
			WithVolumeAllocator(&scriptedAllocator{resolveErr: &volume.InvalidAssignmentPathError{TenantID: "acme", Path: "../acme"}}),
			WithVolumeResolver(&scriptedResolver{}),
		)
		has, err := reg.HasTenantState(ctx, "acme")
		if err != nil || !has {
			t.Fatalf("HasTenantState() = (%v, %v), want (true, nil)", has, err)
		}
	})
	t.Run("lookup failure is an error", func(t *testing.T) {
		reg := newUnsealedRegistryWith(t,
			WithVolumeAllocator(&scriptedAllocator{resolveErr: errCatalogDown}),
			WithVolumeResolver(&scriptedResolver{}),
		)
		has, err := reg.HasTenantState(ctx, "acme")
		if !errors.Is(err, errCatalogDown) || has {
			t.Fatalf("HasTenantState() = (%v, %v), want (false, %v)", has, err, errCatalogDown)
		}
	})
}

// TestRestoreVolumeTenants_WithoutAPoolIsANoop: a node with no volume pool has
// no volume tenants to restore, and says so without an error.
func TestRestoreVolumeTenants_WithoutAPoolIsANoop(t *testing.T) {
	reg := newUnsealedRegistryWith(t)
	if err := reg.RestoreVolumeTenants(context.Background()); err != nil {
		t.Fatalf("RestoreVolumeTenants without a pool: %v", err)
	}
}

// TestRestoreVolumeTenants_CatalogListFailureIsReturned: when the catalog
// cannot be listed, the restore reports it instead of claiming there was
// nothing to restore -- the caller's reconciler must know to retry.
func TestRestoreVolumeTenants_CatalogListFailureIsReturned(t *testing.T) {
	errList := errors.New("catalog list failed")
	reg := newUnsealedRegistryWith(t,
		WithVolumeAllocator(&scriptedAllocator{listErr: errList}),
		WithVolumeResolver(&scriptedResolver{}),
	)
	if err := reg.RestoreVolumeTenants(context.Background()); !errors.Is(err, errList) {
		t.Fatalf("RestoreVolumeTenants() = %v, want %v", err, errList)
	}
}

// TestRestoreVolumeTenants_SymlinkedTenantDirectoryIsNotRestored: a tenant
// whose directory under the pool was replaced by a symlink between runs is
// refused at boot -- its barrier is never reopened through the link -- while
// every other tenant on the pool is still restored.
func TestRestoreVolumeTenants_SymlinkedTenantDirectoryIsNotRestored(t *testing.T) {
	ctx := context.Background()
	system, opts, roots := newShareableComponents(t)
	if err := system.Initialize(ctx, MustCredentials(context.Background(), []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg1, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}
	for _, id := range []string{"good", "victim"} {
		reserveVolumeTenant(t, reg1, id)
		if _, _, err := reg1.RegisterVolumeTenant(ctx, id, false); err != nil {
			t.Fatalf("RegisterVolumeTenant(%s): %v", id, err)
		}
	}
	// Let go of the victim's store, then move its directory out of the pool
	// and leave a symlink to it in its place.
	if err := reg1.ReleaseTenant(ctx, "victim"); err != nil {
		t.Fatalf("ReleaseTenant(victim): %v", err)
	}
	victimDir := filepath.Join(roots[0], "victim")
	moved := filepath.Join(t.TempDir(), "victim")
	if err := os.Rename(victimDir, moved); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := os.Symlink(moved, victimDir); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	reg2, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg2): %v", err)
	}
	if err := reg2.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants must skip the refused tenant, not abort: %v", err)
	}
	if _, err := reg2.GetTenant("victim"); err == nil {
		t.Fatal("a tenant whose directory is a symlink was restored through it")
	}
	if _, err := reg2.GetTenant("good"); err != nil {
		t.Fatalf("the healthy tenant was not restored alongside the refused one: %v", err)
	}
}

// TestRequireNotSymlinked_UnexaminablePathIsRefused: a tenant path that cannot
// be examined (here, because the pool root is not a directory) is refused,
// never mistaken for "does not exist yet" and accepted.
func TestRequireNotSymlinked_UnexaminablePathIsRefused(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pool")
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err := requireNotSymlinked(root, filepath.Join(root, "acme"), "acme", "acme")
	var target *TenantPathIsSymlinkError
	if !errors.As(err, &target) {
		t.Fatalf("requireNotSymlinked() = %v, want *TenantPathIsSymlinkError", err)
	}
	if target.TenantID != "acme" {
		t.Errorf("TenantID = %q, want acme", target.TenantID)
	}
	if target.Err == nil || errors.Is(target.Err, fs.ErrNotExist) {
		t.Errorf("Err = %v, want the non-ENOENT cause", target.Err)
	}
}

// TestDropTenantRecords_ReleaseFailureIsReported: a catalog Assignment that
// cannot be released would resurrect the tenant's barrier at the next boot, so
// the failure is returned, naming the record.
func TestDropTenantRecords_ReleaseFailureIsReported(t *testing.T) {
	errRelease := errors.New("release failed")
	reg := newUnsealedRegistryWith(t,
		WithVolumeAllocator(&scriptedAllocator{releaseErr: errRelease}),
		WithVolumeResolver(&scriptedResolver{}),
	)

	err := reg.DropTenantRecords(context.Background(), "acme")
	var target *TenantRecordDeleteError
	if !errors.As(err, &target) {
		t.Fatalf("DropTenantRecords() = %v, want *TenantRecordDeleteError", err)
	}
	if target.TenantID != "acme" || target.Record != "volume-pool-assignment" {
		t.Errorf("TenantRecordDeleteError = %+v, want TenantID=acme Record=volume-pool-assignment", target)
	}
	if !errors.Is(err, errRelease) {
		t.Errorf("error does not wrap the release failure: %v", err)
	}
}

// TestUnsealSystemManaged_ForgottenTenantIsDroppedNotRetried: a system-managed
// tenant the node seal sealed, and which was then forgotten before the node
// unsealed, is dropped from the pending set -- it is neither resurrected nor
// retried at every later unseal -- while a tenant still registered comes back.
func TestUnsealSystemManaged_ForgottenTenantIsDroppedNotRetried(t *testing.T) {
	reg, _, _ := newPooledRegistryUnsealed(t)
	ctx := context.Background()

	for _, id := range []string{"kept", "forgotten"} {
		reserveVolumeTenant(t, reg, id)
		if _, _, err := reg.RegisterVolumeTenant(ctx, id, true); err != nil {
			t.Fatalf("RegisterVolumeTenant(%s): %v", id, err)
		}
		if err := reg.InitializeTenant(ctx, id, Credentials{}); err != nil {
			t.Fatalf("InitializeTenant(%s): %v", id, err)
		}
	}

	if err := reg.SealAll(); err != nil {
		t.Fatalf("SealAll: %v", err)
	}
	pending := reg.PendingTenantUnseals()
	if _, ok := pending["forgotten"]; !ok {
		t.Fatalf("PendingTenantUnseals() = %v, want the node seal to record forgotten", pending)
	}
	if err := reg.RemoveTenant("forgotten"); err != nil {
		t.Fatalf("RemoveTenant: %v", err)
	}

	if err := reg.SystemBarrier().Unseal(ctx, MustCredentials(ctx, []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system Unseal: %v", err)
	}

	if pending := reg.PendingTenantUnseals(); len(pending) != 0 {
		t.Fatalf("PendingTenantUnseals() after unseal = %v, want empty", pending)
	}
	if _, err := reg.GetTenant("forgotten"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("GetTenant(forgotten) = %v, want ErrTenantNotFound", err)
	}
	kept, err := reg.GetTenant("kept")
	if err != nil {
		t.Fatalf("GetTenant(kept): %v", err)
	}
	if kept.IsSealed() {
		t.Fatal("the still-registered system-managed tenant did not unseal with the node")
	}
}
