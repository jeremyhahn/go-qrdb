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

package seal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// pebbleAbsent reports whether NO Pebble store exists at path, treating a
// missing directory as absence and any other read failure as a failed
// measurement.
//
// The distinction is what go-qrdb#236 is about. pebbleExists returns
// (false, err) for every failure, so the previous form at these call sites --
//
//	if exists, _ := pebbleExists(p); exists { t.Error(...) }
//
// scored an UNREADABLE directory as "nothing was created", which is the exact
// claim the assertion exists to make.
//
// But not every error means the measurement failed. A directory that does not
// exist IS the expected success state here: the mount was never created, so
// nothing could have been written to it. readDir propagates ENOENT rather than
// swallowing it. Anything else (a permission failure, a corrupt mount) is a
// measurement that did not happen, and fails the test.
func pebbleAbsent(t *testing.T, path string) bool {
	t.Helper()
	exists, err := pebbleExists(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true
		}
		t.Fatalf("pebbleExists(%q): %v -- the directory read failed, so this never established whether a Pebble store was created", path, err)
	}
	return !exists
}

// reserveVolumeTenant reserves tenantID on reg's single configured pool
// ("default") via its volumeAllocator, with tenantID as both the one-clean-
// path-component Path and reserveBytes left at 0 (these tests are not about
// quota). RegisterVolumeTenant and ReattachVolumeTenant require the
// Assignment to already exist -- there is no "pick a fresh path" branch in
// either of them any more (see barrier_registry.go's doc comments); the
// catalog's Reserve is the caller's job, mirroring pkg/tenant.CreateTenant.
func reserveVolumeTenant(t *testing.T, reg *BarrierRegistry, tenantID string) {
	t.Helper()
	allocator, _ := reg.volumeResources()
	if allocator == nil {
		t.Fatalf("reserveVolumeTenant(%s): registry has no volume allocator configured", tenantID)
	}
	if _, err := allocator.Reserve(context.Background(), tenantID, "default", tenantID, 0); err != nil {
		t.Fatalf("Reserve(%s): %v", tenantID, err)
	}
}

// TestRegisterVolumeTenant_AllocatesDataRootAndBarrier verifies the
// IsolationVolume registration layout: DataRoot is allocated under the pool
// root as {root}/{tenantID}, and the barrier IAM Pebble lands in the
// dedicated "barrier" subdirectory, leaving "databases" free for the
// per-tenant Host.
func TestRegisterVolumeTenant_AllocatesDataRootAndBarrier(t *testing.T) {
	reg, roots, _ := newPooledRegistryUnsealed(t)
	ctx := context.Background()

	reserveVolumeTenant(t, reg, "vol-alpha")
	dataRoot, tb, err := reg.RegisterVolumeTenant(ctx, "vol-alpha", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	if tb == nil {
		t.Fatal("expected non-nil TenantBarrier")
	}

	// DataRoot must be {poolRoot}/vol-alpha.
	if dataRoot != filepath.Join(roots[0], "vol-alpha") {
		t.Fatalf("DataRoot %q is not {poolRoot}/vol-alpha (%v)", dataRoot, roots)
	}

	// Barrier Pebble must live under {DataRoot}/barrier, not at DataRoot.
	assertPebbleAtPath(t, filepath.Join(dataRoot, "barrier"))
	if !pebbleAbsent(t, dataRoot) {
		t.Errorf("barrier Pebble unexpectedly opened at DataRoot %q (want {DataRoot}/barrier)", dataRoot)
	}

	// Initialize with empty credentials (inherited system strategy) and verify
	// the barrier becomes usable, mirroring the IsolationBarrier create flow.
	if err := reg.InitializeTenant(ctx, "vol-alpha", Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}
	if tb.IsSealed() {
		t.Error("barrier still sealed after Initialize")
	}
}

// TestReattachVolumeTenant_PresentStoreSucceeds verifies the boot re-attach
// path succeeds when the tenant's persisted barrier store is present on
// disk: reg1 creates it, a fresh reg2 (same system barrier + catalog)
// re-attaches it.
func TestReattachVolumeTenant_PresentStoreSucceeds(t *testing.T) {
	ctx := context.Background()
	system, opts, roots := newShareableComponents(t)
	if err := system.Initialize(ctx, MustCredentials(context.Background(), []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg1, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}
	reserveVolumeTenant(t, reg1, "present")
	dataRoot, _, err := reg1.RegisterVolumeTenant(ctx, "present", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant (create): %v", err)
	}
	if dataRoot != filepath.Join(roots[0], "present") {
		t.Fatalf("dataRoot = %q, want %q", dataRoot, filepath.Join(roots[0], "present"))
	}
	assertPebbleAtPath(t, filepath.Join(dataRoot, "barrier"))

	reg2, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg2): %v", err)
	}
	root, tb, err := reg2.ReattachVolumeTenant(ctx, "present", false)
	if err != nil {
		t.Fatalf("ReattachVolumeTenant (present store): %v", err)
	}
	if root != dataRoot {
		t.Errorf("root = %q, want %q", root, dataRoot)
	}
	if tb == nil {
		t.Fatal("expected non-nil TenantBarrier")
	}
}

// TestReattachVolumeTenant_AbsentStoreReturnsMountMissing is the L3 regression
// guard: re-attaching a tenant whose Assignment exists but whose barrier
// store is NOT present on disk (dedicated mount absent, or an empty leftover
// dir) must fail closed with DataRootMountMissingError and must NOT mint a
// fresh empty barrier.
func TestReattachVolumeTenant_AbsentStoreReturnsMountMissing(t *testing.T) {
	ctx := context.Background()

	t.Run("missing directory", func(t *testing.T) {
		reg, roots, _ := newPooledRegistryUnsealed(t)
		reserveVolumeTenant(t, reg, "absent")
		dataRoot := filepath.Join(roots[0], "absent") // RequireInsideLocalPool's resolved path
		_, _, err := reg.ReattachVolumeTenant(ctx, "absent", false)
		assertMountMissing(t, reg, "absent", dataRoot, err)
	})

	t.Run("empty barrier directory", func(t *testing.T) {
		reg, roots, _ := newPooledRegistryUnsealed(t)
		reserveVolumeTenant(t, reg, "empty")
		dataRoot := filepath.Join(roots[0], "empty")
		// Simulate the mount being present but the barrier store never
		// populated (an empty {dataRoot}/barrier directory).
		if err := os.MkdirAll(filepath.Join(dataRoot, "barrier"), 0o700); err != nil {
			t.Fatalf("mkdir empty barrier: %v", err)
		}
		_, _, err := reg.ReattachVolumeTenant(ctx, "empty", false)
		assertMountMissing(t, reg, "empty", dataRoot, err)
	})
}

// assertMountMissing asserts the re-attach failed closed: a
// DataRootMountMissingError was returned, the tenant is not registered, and no
// fresh Pebble was created at {dataRoot}/barrier.
func assertMountMissing(t *testing.T, reg *BarrierRegistry, tenantID, dataRoot string, err error) {
	t.Helper()
	var mm *DataRootMountMissingError
	if !errors.As(err, &mm) {
		t.Fatalf("ReattachVolumeTenant = %v, want *DataRootMountMissingError", err)
	}
	if _, gerr := reg.GetTenant(tenantID); gerr == nil {
		t.Error("tenant must NOT be registered after a missing-mount failure")
	}
	if !pebbleAbsent(t, filepath.Join(dataRoot, "barrier")) {
		t.Error("a fresh barrier Pebble was created at the missing mount; must fail closed")
	}
}

// TestRegisterVolumeTenant_SealedSystemBarrierRefusesClosed verifies that
// RegisterVolumeTenant (and, by the same shared registerVolumeTenant,
// ReattachVolumeTenant) refuses with ErrSealed while the system barrier is
// sealed, rather than silently proceeding to resolve the assignment and open
// a Pebble store for it.
func TestRegisterVolumeTenant_SealedSystemBarrierRefusesClosed(t *testing.T) {
	// newPooledRegistry's system barrier starts sealed (never initialized);
	// this test is the one place in the file that must NOT unseal it.
	reg, roots, _ := newPooledRegistry(t)
	ctx := context.Background()

	// loadTenantBarrierPosture / RequireInsideLocalPool both read through the
	// (sealed) system barrier; registerVolumeTenant's ErrSealed guard fires
	// before any of that, so no Reserve is even needed to observe it -- but
	// Reserve itself does not touch the system barrier, so taking it first
	// changes nothing about what this test proves.
	dataRoot := filepath.Join(roots[0], "sealed-refusal")

	_, tb, err := reg.RegisterVolumeTenant(ctx, "sealed-refusal", false)
	if !errors.Is(err, ErrSealed) {
		t.Fatalf("RegisterVolumeTenant on a sealed system barrier = %v, want ErrSealed", err)
	}
	if tb != nil {
		t.Error("RegisterVolumeTenant must not return a TenantBarrier on refusal")
	}
	if _, gerr := reg.GetTenant("sealed-refusal"); gerr == nil {
		t.Error("tenant must NOT be registered after a sealed-system refusal")
	}
	if !pebbleAbsent(t, filepath.Join(dataRoot, "barrier")) {
		t.Error("a Pebble store was opened despite the sealed-system refusal; must fail closed before touching storage")
	}

	// ReattachVolumeTenant shares the same guard.
	_, tb, err = reg.ReattachVolumeTenant(ctx, "sealed-refusal", false)
	if !errors.Is(err, ErrSealed) {
		t.Fatalf("ReattachVolumeTenant on a sealed system barrier = %v, want ErrSealed", err)
	}
	if tb != nil {
		t.Error("ReattachVolumeTenant must not return a TenantBarrier on refusal")
	}
}

// TestRestoreVolumeTenants_MissingMountSkipped verifies that boot restore
// fails a tenant whose dedicated mount is absent CLOSED (skips it, logs)
// instead of silently opening a fresh empty barrier -- and does not abort
// the whole node.
func TestRestoreVolumeTenants_MissingMountSkipped(t *testing.T) {
	ctx := context.Background()
	system, opts, roots := newShareableComponents(t)
	if err := system.Initialize(ctx, MustCredentials(context.Background(), []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	// Reserve the assignment (so RestoreVolumeTenants' ListAll finds it) but
	// never create the on-disk barrier store under it -- the dedicated disk
	// is "not mounted this boot".
	reserveVolumeTenant(t, reg, "ghost")
	missingRoot := filepath.Join(roots[0], "ghost")

	// The whole node must not crash: restore returns nil, skipping the tenant.
	if err := reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants must not abort on a missing mount: %v", err)
	}
	if _, gerr := reg.GetTenant("ghost"); gerr == nil {
		t.Error("tenant with a missing mount must NOT be registered after restore")
	}
	if !pebbleAbsent(t, filepath.Join(missingRoot, "barrier")) {
		t.Error("restore created a fresh empty barrier at the missing mount; must fail closed")
	}
}

// TestRegisterVolumeTenant_SystemManagedAutoUnseal verifies the system-managed
// (auto-unseal) volume path: the barrier is initialized credential-free and,
// after sealing, re-attaches and unseals credential-free because its DEK is
// wrapped under the (unsealed) system barrier.
func TestRegisterVolumeTenant_SystemManagedAutoUnseal(t *testing.T) {
	reg, _, _ := newPooledRegistry(t)
	ctx := context.Background()

	// The pool system barrier starts sealed; the system-managed strategy needs
	// it unsealed to act as the unseal authority.
	if err := reg.SystemBarrier().Initialize(ctx, MustCredentials(context.Background(), []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reserveVolumeTenant(t, reg, "vol-auto")
	_, tb, err := reg.RegisterVolumeTenant(ctx, "vol-auto", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant (auto): %v", err)
	}
	if err := reg.InitializeTenant(ctx, "vol-auto", Credentials{}); err != nil {
		t.Fatalf("credential-free InitializeTenant: %v", err)
	}
	if tb.IsSealed() {
		t.Fatal("barrier should be unsealed after credential-free Initialize")
	}
	if got := tb.Barrier().ActiveStrategy(); got != StrategySystemBarrier {
		t.Fatalf("active strategy = %q, want %q", got, StrategySystemBarrier)
	}

	// Seal, then re-attach + credential-free unseal (the boot path).
	if err := reg.SealTenant("vol-auto"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	if err := reg.UnsealTenant(ctx, "vol-auto", Credentials{}); err != nil {
		t.Fatalf("credential-free UnsealTenant after seal: %v", err)
	}
	if tb.IsSealed() {
		t.Fatal("barrier should be unsealed after credential-free re-unseal")
	}
}

// TestRegisterVolumeTenant_SelfManagedStaysSealed verifies the self-managed
// (default) volume path: the barrier is registered but NOT initialized, so it
// stays sealed until the tenant admin drives Initialize/Unseal.
func TestRegisterVolumeTenant_SelfManagedStaysSealed(t *testing.T) {
	reg, _, _ := newPooledRegistryUnsealed(t)
	ctx := context.Background()

	reserveVolumeTenant(t, reg, "vol-self")
	_, tb, err := reg.RegisterVolumeTenant(ctx, "vol-self", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant (self): %v", err)
	}
	if !tb.IsSealed() {
		t.Fatal("self-managed barrier must be sealed until the tenant admin initializes it")
	}
}

// TestRegisterVolumeTenant_RejectsTraversalID verifies the filesystem
// containment guard: a tenant ID that would escape the pool root is rejected
// at Reserve time (ValidateAssignmentPath), before RegisterVolumeTenant is
// ever reached.
func TestRegisterVolumeTenant_RejectsTraversalID(t *testing.T) {
	reg, _, _ := newPooledRegistry(t)
	allocator, _ := reg.volumeResources()

	if _, err := allocator.Reserve(context.Background(), "../escape", "default", "../escape", 0); err == nil {
		t.Fatal("expected error reserving a traversal tenant ID, got nil")
	}
}

// TestRegisterVolumeTenant_EmptyTenantID verifies that RegisterVolumeTenant
// rejects an empty tenant ID immediately with ErrEmptyTenantID.
func TestRegisterVolumeTenant_EmptyTenantID(t *testing.T) {
	reg, _, _ := newPooledRegistry(t)

	_, _, err := reg.RegisterVolumeTenant(context.Background(), "", false)
	if err == nil {
		t.Fatal("expected ErrEmptyTenantID, got nil")
	}
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("error = %v, want wrapping ErrEmptyTenantID", err)
	}
}

// TestRegisterVolumeTenant_NoPoolConfigured verifies the volume path requires a
// configured volume allocator/resolver.
func TestRegisterVolumeTenant_NoPoolConfigured(t *testing.T) {
	system := newPoolSystemBarrier(t)
	reg, err := NewBarrierRegistry(poolTestLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	if _, _, err := reg.RegisterVolumeTenant(context.Background(), "vol-x", false); err == nil {
		t.Fatal("expected ErrPoolNotConfigured, got nil")
	}
}

// TestLocalTenantDataRoot verifies that LocalTenantDataRoot returns the same
// path that RegisterVolumeTenant allocated for the tenant, without opening
// any storage.
func TestLocalTenantDataRoot(t *testing.T) {
	reg, roots, _ := newPooledRegistryUnsealed(t)
	ctx := context.Background()

	reserveVolumeTenant(t, reg, "loc-alpha")
	dataRoot, _, err := reg.RegisterVolumeTenant(ctx, "loc-alpha", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}

	// LocalTenantDataRoot must return the SAME path without opening storage.
	resolved, err := reg.LocalTenantDataRoot(ctx, "loc-alpha")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot: %v", err)
	}
	if resolved != filepath.Join(roots[0], "loc-alpha") {
		t.Errorf("LocalTenantDataRoot = %q, not {poolRoot}/loc-alpha (%v)", resolved, roots)
	}

	// The paths must match — the follower uses LocalTenantDataRoot to find its
	// shard DataDir, which must agree with what the leader persisted.
	if resolved != dataRoot {
		t.Errorf("LocalTenantDataRoot = %q, want %q (same as RegisterVolumeTenant)", resolved, dataRoot)
	}
}

// TestLocalTenantDataRoot_NoPool verifies that LocalTenantDataRoot returns an
// error when no volume allocator/resolver is configured rather than silently
// returning empty.
func TestLocalTenantDataRoot_NoPool(t *testing.T) {
	system := newPoolSystemBarrier(t)
	reg, err := NewBarrierRegistry(poolTestLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	_, err = reg.LocalTenantDataRoot(context.Background(), "any-tenant")
	if err == nil {
		t.Fatal("expected error from LocalTenantDataRoot with no pool configured, got nil")
	}
}

// TestLocalTenantDataRoot_NotRegistered verifies that LocalTenantDataRoot
// returns an AssignmentNotFoundError when the tenant has no catalog
// Assignment yet. This is the error the reconciler observes when key
// delivery has not yet completed (the shard start is deferred).
func TestLocalTenantDataRoot_NotRegistered(t *testing.T) {
	reg, _, _ := newPooledRegistry(t)
	_, err := reg.LocalTenantDataRoot(context.Background(), "never-registered")
	if err == nil {
		t.Fatal("expected error for unregistered tenant, got nil")
	}
	var notFound *volume.AssignmentNotFoundError
	if !errors.As(err, &notFound) {
		t.Errorf("error = %v, want wrapping *volume.AssignmentNotFoundError", err)
	}
}

// TestRestoreVolumeTenants_RestoredAfterFreshRegistry verifies the central
// restart-safety property: when RegisterVolumeTenant was called on a previous
// registry run (R1), a fresh BarrierRegistry (R2) backed by the same system
// barrier and catalog can restore the tenant and answer LocalTenantDataRoot.
func TestRestoreVolumeTenants_RestoredAfterFreshRegistry(t *testing.T) {
	ctx := context.Background()
	system, opts, roots := newShareableComponents(t)

	if err := system.Initialize(ctx, MustCredentials(context.Background(), []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	// reg1: original node that registered the tenant.
	reg1, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}
	reserveVolumeTenant(t, reg1, "vol-restore")
	dataRoot1, _, err := reg1.RegisterVolumeTenant(ctx, "vol-restore", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant on reg1: %v", err)
	}
	if dataRoot1 != filepath.Join(roots[0], "vol-restore") {
		t.Fatalf("reg1 DataRoot %q not {poolRoot}/vol-restore (%v)", dataRoot1, roots)
	}

	// reg2: simulates a fresh process (new BarrierRegistry, same system barrier
	// and catalog).
	reg2, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg2): %v", err)
	}

	// Tenant must NOT be visible in reg2 before RestoreVolumeTenants.
	if _, gerr := reg2.GetTenant("vol-restore"); gerr == nil {
		t.Fatal("expected tenant not found in fresh registry before RestoreVolumeTenants")
	}

	if err := reg2.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants: %v", err)
	}

	// Tenant must now be visible in reg2.
	tb, err := reg2.GetTenant("vol-restore")
	if err != nil {
		t.Fatalf("GetTenant after RestoreVolumeTenants: %v", err)
	}
	if tb == nil {
		t.Fatal("expected non-nil TenantBarrier after RestoreVolumeTenants")
	}

	// LocalTenantDataRoot must return the same path that reg1 allocated.
	resolved, err := reg2.LocalTenantDataRoot(ctx, "vol-restore")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot after RestoreVolumeTenants: %v", err)
	}
	if resolved != dataRoot1 {
		t.Errorf("LocalTenantDataRoot = %q, want %q (same as reg1)", resolved, dataRoot1)
	}
}

// TestRestoreVolumeTenants_AutoUnsealUnsealed verifies that a system-managed
// tenant (autoUnseal=true) that was initialized on reg1 is automatically
// unsealed by RestoreVolumeTenants on a fresh reg2. This is the follower
// boot path: key delivery wrote the DEK to disk wrapped under the (unsealed)
// system barrier, and the boot restore must unseal it credential-free.
func TestRestoreVolumeTenants_AutoUnsealUnsealed(t *testing.T) {
	ctx := context.Background()
	system, opts, _ := newShareableComponents(t)

	if err := system.Initialize(ctx, MustCredentials(context.Background(), []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg1, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}
	reserveVolumeTenant(t, reg1, "vol-auto-restore")
	_, _, err = reg1.RegisterVolumeTenant(ctx, "vol-auto-restore", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant (auto): %v", err)
	}
	// Initialize so the DEK is written and wrapped under the system barrier.
	if err := reg1.InitializeTenant(ctx, "vol-auto-restore", Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}

	// reg2: simulates restart with same system barrier.
	reg2, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg2): %v", err)
	}

	if err := reg2.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants: %v", err)
	}

	tb, err := reg2.GetTenant("vol-auto-restore")
	if err != nil {
		t.Fatalf("GetTenant after RestoreVolumeTenants: %v", err)
	}
	if tb.IsSealed() {
		t.Error("auto-unseal tenant must be unsealed after RestoreVolumeTenants")
	}
}

// TestRestoreVolumeTenants_SelfManagedStaysSealed verifies that a
// self-managed tenant (autoUnseal=false) remains sealed after restore —
// RestoreVolumeTenants must NOT attempt an unseal for self-managed tenants.
func TestRestoreVolumeTenants_SelfManagedStaysSealed(t *testing.T) {
	ctx := context.Background()
	system, opts, _ := newShareableComponents(t)

	if err := system.Initialize(ctx, MustCredentials(context.Background(), []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg1, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}
	reserveVolumeTenant(t, reg1, "vol-self-restore")
	if _, _, err := reg1.RegisterVolumeTenant(ctx, "vol-self-restore", false); err != nil {
		t.Fatalf("RegisterVolumeTenant (self): %v", err)
	}

	// reg2: simulates restart.
	reg2, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg2): %v", err)
	}

	if err := reg2.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants: %v", err)
	}

	tb, err := reg2.GetTenant("vol-self-restore")
	if err != nil {
		t.Fatalf("GetTenant after RestoreVolumeTenants: %v", err)
	}
	if !tb.IsSealed() {
		t.Error("self-managed tenant must remain sealed after RestoreVolumeTenants")
	}
}

// TestRestoreVolumeTenants_IdempotentWhenAlreadyRegistered verifies that
// RestoreVolumeTenants is a no-op for tenants already present in the registry.
// Calling it on the same registry that ran RegisterVolumeTenant must not
// return an error or alter the existing registration.
func TestRestoreVolumeTenants_IdempotentWhenAlreadyRegistered(t *testing.T) {
	ctx := context.Background()
	system, opts, _ := newShareableComponents(t)

	if err := system.Initialize(ctx, MustCredentials(context.Background(), []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	reserveVolumeTenant(t, reg, "vol-idem")
	if _, _, err := reg.RegisterVolumeTenant(ctx, "vol-idem", false); err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}

	// RestoreVolumeTenants on the same registry must skip the already-registered
	// tenant and return nil.
	if err := reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants (idempotent): %v", err)
	}

	// Tenant must still be accessible.
	if _, err := reg.GetTenant("vol-idem"); err != nil {
		t.Fatalf("GetTenant after idempotent restore: %v", err)
	}
}

// TestRestoreVolumeTenants_SealedSystemBarrierIsNoop verifies that
// RestoreVolumeTenants returns nil without performing any work when the
// system barrier is still sealed (e.g., before the daemon unseals).
func TestRestoreVolumeTenants_SealedSystemBarrierIsNoop(t *testing.T) {
	// newPooledRegistry produces a sealed system barrier (not yet initialized).
	reg, _, _ := newPooledRegistry(t)

	if err := reg.RestoreVolumeTenants(context.Background()); err != nil {
		t.Fatalf("RestoreVolumeTenants on sealed system barrier: %v", err)
	}
}

// TestRestoreVolumeTenants_AutoUnsealFailure verifies the warning-and-continue
// path inside RestoreVolumeTenants for autoUnseal tenants whose barrier was
// never initialized: UnsealTenant fails (ErrNotInitialized) but
// RestoreVolumeTenants must return nil and the tenant must still be registered
// in the in-memory registry (registration succeeded; only the unseal failed).
func TestRestoreVolumeTenants_AutoUnsealFailure(t *testing.T) {
	ctx := context.Background()
	system, opts, _ := newShareableComponents(t)

	if err := system.Initialize(ctx, MustCredentials(context.Background(), []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg1, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}

	// Register with autoUnseal=true but deliberately skip InitializeTenant.
	// The Assignment is reserved, but the barrier Pebble has no root-key blob.
	// When reg2 restores and calls UnsealTenant it will receive
	// ErrNotInitialized, log a warning, and continue.
	reserveVolumeTenant(t, reg1, "uninit-auto")
	if _, _, err := reg1.RegisterVolumeTenant(ctx, "uninit-auto", true); err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}

	// reg2 simulates a follower node or process restart.
	reg2, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg2): %v", err)
	}

	// Must return nil even though UnsealTenant fails.
	if err := reg2.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants returned error on unseal failure: %v", err)
	}

	// The tenant must still be registered (register step succeeded; unseal failed).
	if _, gerr := reg2.GetTenant("uninit-auto"); gerr != nil {
		t.Errorf("GetTenant after failed unseal: %v", gerr)
	}
}

// TestLocalTenantDataRoot_NotInPersistentStoreEither verifies that when the
// system barrier is unsealed but the tenant was never registered on any
// node, LocalTenantDataRoot returns an AssignmentNotFoundError.
func TestLocalTenantDataRoot_NotInPersistentStoreEither(t *testing.T) {
	ctx := context.Background()
	system, opts, _ := newShareableComponents(t)

	// Unseal the system barrier so the catalog lookup is attempted.
	if err := system.Initialize(ctx, MustCredentials(context.Background(), []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	// "ghost-tenant" was never registered on any node, so no catalog
	// Assignment exists and no in-memory entry exists.
	_, terr := reg.LocalTenantDataRoot(ctx, "ghost-tenant")
	if terr == nil {
		t.Fatal("expected an error for unregistered tenant, got nil")
	}
	var notFound *volume.AssignmentNotFoundError
	if !errors.As(terr, &notFound) {
		t.Errorf("error = %v, want wrapping *volume.AssignmentNotFoundError", terr)
	}
}

// TestRegisterVolumeTenant_MigrationRecoverer_RunsBeforeBarrierBuild verifies
// the crash-recovery hook attached via WithMigrationRecoverer is invoked over
// the tenant's freshly-opened backend, with the tenant's canonical root-key
// path, BEFORE the barrier is registered — the seam that closes the
// tenant-scope enable-encryption crash window.
func TestRegisterVolumeTenant_MigrationRecoverer_RunsBeforeBarrierBuild(t *testing.T) {
	reg, _, _ := newPooledRegistryUnsealed(t)
	ctx := context.Background()

	var gotRootKeyPath string
	var gotBackend StorageBackend
	calls := 0
	reg.Attach(WithMigrationRecoverer(
		func(_ context.Context, backend StorageBackend, rootKeyPath string) error {
			calls++
			gotBackend = backend
			gotRootKeyPath = rootKeyPath
			// The barrier must not exist yet when recovery runs.
			if _, err := reg.GetTenant("vol-recover"); err == nil {
				t.Error("recoverer ran AFTER the tenant barrier was registered")
			}
			return nil
		}))

	reserveVolumeTenant(t, reg, "vol-recover")
	if _, _, err := reg.RegisterVolumeTenant(ctx, "vol-recover", true); err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	if calls != 1 {
		t.Fatalf("recoverer calls = %d, want 1", calls)
	}
	if gotRootKeyPath != "tenant/vol-recover/root-key" {
		t.Fatalf("recoverer rootKeyPath = %q, want tenant/vol-recover/root-key", gotRootKeyPath)
	}
	if gotBackend == nil {
		t.Fatal("recoverer must receive the tenant's opened storage backend")
	}
}

// TestRegisterVolumeTenant_MigrationRecoverer_FailsClosed verifies a recovery
// failure aborts the registration with a typed MigrationRecoveryError: the
// barrier must never be built over unreconciled (half-swapped) state.
func TestRegisterVolumeTenant_MigrationRecoverer_FailsClosed(t *testing.T) {
	reg, _, _ := newPooledRegistryUnsealed(t)
	ctx := context.Background()

	sentinel := errors.New("recovery exploded")
	reg.Attach(WithMigrationRecoverer(
		func(context.Context, StorageBackend, string) error { return sentinel }))

	reserveVolumeTenant(t, reg, "vol-recover-fail")
	_, _, err := reg.RegisterVolumeTenant(ctx, "vol-recover-fail", true)
	var mre *MigrationRecoveryError
	if !errors.As(err, &mre) {
		t.Fatalf("err = %v, want *MigrationRecoveryError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("err must wrap the recoverer's cause; got %v", err)
	}
	if mre.TenantID != "vol-recover-fail" {
		t.Fatalf("TenantID = %q", mre.TenantID)
	}
	// Fail-closed: no barrier registered.
	if _, gerr := reg.GetTenant("vol-recover-fail"); gerr == nil {
		t.Fatal("tenant barrier must NOT be registered after a recovery failure")
	}
}

// TestRestoreVolumeTenants_UnmappedPoolSkipped verifies that an Assignment
// for a pool this node's resolver does not map (e.g. a pool another node
// serves) is skipped quietly by RestoreVolumeTenants rather than causing a
// failure.
func TestRestoreVolumeTenants_UnmappedPoolSkipped(t *testing.T) {
	ctx := context.Background()
	system, opts, _ := newShareableComponents(t)
	if err := system.Initialize(ctx, MustCredentials(context.Background(), []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	allocator, _ := reg.volumeResources()

	// Create a SECOND catalog pool that this node's resolver never opened
	// (not part of newShareableComponents' OpenVolumes), and reserve a
	// tenant on it directly through the catalog.
	cat, ok := allocator.(*volume.Catalog)
	if !ok {
		t.Fatalf("test assumes a *volume.Catalog allocator, got %T", allocator)
	}
	if _, err := cat.CreatePool(ctx, volume.CreatePoolRequest{Name: "elsewhere", Provider: "dir"}); err != nil {
		t.Fatalf("CreatePool(elsewhere): %v", err)
	}
	if _, err := allocator.Reserve(ctx, "remote-tenant", "elsewhere", "remote-tenant", 0); err != nil {
		t.Fatalf("Reserve(remote-tenant): %v", err)
	}

	if err := reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants must skip an unmapped pool's assignment, not fail: %v", err)
	}
	if _, gerr := reg.GetTenant("remote-tenant"); gerr == nil {
		t.Error("a tenant on a pool this node does not map must not be registered")
	}
}

// TestLoadTenantBarrierPosture_CorruptRecordDefaultsSelfManaged verifies
// that a corrupt (non-JSON) persisted posture record is treated as absent
// -- (false, false, nil) -- so the fail-closed self-managed default
// applies rather than propagating a decode error.
func TestLoadTenantBarrierPosture_CorruptRecordDefaultsSelfManaged(t *testing.T) {
	ctx := context.Background()
	reg, _, _ := newPooledRegistryUnsealed(t)

	if err := reg.SystemBarrier().Put(ctx, tenantBarrierPostureStorePrefix+"corrupt", []byte("}{not valid json")); err != nil {
		t.Fatalf("Put corrupt posture record: %v", err)
	}

	autoUnseal, found, err := reg.loadTenantBarrierPosture(ctx, "corrupt")
	if err != nil {
		t.Fatalf("loadTenantBarrierPosture: %v", err)
	}
	if found {
		t.Error("a corrupt posture record must read as not found")
	}
	if autoUnseal {
		t.Error("a corrupt posture record must default to self-managed (autoUnseal=false)")
	}
}
