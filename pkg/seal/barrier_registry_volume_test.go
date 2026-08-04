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
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestRegisterVolumeTenant_AllocatesDataRootAndBarrier verifies the
// IsolationVolume registration layout: DataRoot is allocated under a pool root
// as {root}/{tenantID}, and the barrier IAM Pebble lands in the dedicated
// "barrier" subdirectory, leaving "databases" free for the per-tenant Host.
func TestRegisterVolumeTenant_AllocatesDataRootAndBarrier(t *testing.T) {
	reg, roots, _ := newPooledRegistry(t)
	ctx := context.Background()

	dataRoot, tb, err := reg.RegisterVolumeTenant(ctx, "vol-alpha", "", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	if tb == nil {
		t.Fatal("expected non-nil TenantBarrier")
	}

	// DataRoot must be {someRoot}/vol-alpha.
	matched := false
	for _, r := range roots {
		if dataRoot == filepath.Join(r, "vol-alpha") {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("DataRoot %q is not {poolRoot}/vol-alpha for any of %v", dataRoot, roots)
	}

	// Barrier Pebble must live under {DataRoot}/barrier, not at DataRoot.
	assertPebbleAtPath(t, filepath.Join(dataRoot, "barrier"))
	if exists, _ := pebbleExists(dataRoot); exists {
		t.Errorf("barrier Pebble unexpectedly opened at DataRoot %q (want {DataRoot}/barrier)", dataRoot)
	}

	// Initialize with empty credentials (inherited system strategy) and verify
	// the barrier becomes usable, mirroring the IsolationBarrier create flow.
	if err := reg.InitializeTenant(ctx, "vol-alpha", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}
	if tb.IsSealed() {
		t.Error("barrier still sealed after Initialize")
	}
}

// TestRegisterVolumeTenant_ExplicitDataRoot verifies that passing an explicit
// DataRoot (the boot re-attach path) binds the barrier to that exact on-disk
// location verbatim instead of allocating a new path from the pool. The real
// daemon re-attach uses a fresh registry/process with the persisted DataRoot.
func TestRegisterVolumeTenant_ExplicitDataRoot(t *testing.T) {
	reg, _, _ := newPooledRegistry(t)
	ctx := context.Background()

	explicit := t.TempDir()
	root, tb, err := reg.RegisterVolumeTenant(ctx, "vol-beta", explicit, false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant (explicit): %v", err)
	}
	if root != explicit {
		t.Errorf("DataRoot = %q, want explicit %q", root, explicit)
	}
	assertPebbleAtPath(t, filepath.Join(explicit, "barrier"))

	if err := reg.UnsealTenant(ctx, "vol-beta", seal.Credentials{}); err == nil {
		// A freshly-registered, never-initialized barrier cannot be unsealed;
		// initialize then verify it becomes usable.
		t.Log("unexpected unseal success on uninitialized barrier")
	}
	if err := reg.InitializeTenant(ctx, "vol-beta", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}
	if tb.IsSealed() {
		t.Error("barrier still sealed after Initialize")
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
	if err := reg.SystemBarrier().Initialize(ctx, seal.Credentials{Secret: []byte("pool-test-pass")}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	_, tb, err := reg.RegisterVolumeTenant(ctx, "vol-auto", "", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant (auto): %v", err)
	}
	if err := reg.InitializeTenant(ctx, "vol-auto", seal.Credentials{}); err != nil {
		t.Fatalf("credential-free InitializeTenant: %v", err)
	}
	if tb.IsSealed() {
		t.Fatal("barrier should be unsealed after credential-free Initialize")
	}
	if got := tb.Barrier().ActiveStrategy(); got != seal.StrategySystemBarrier {
		t.Fatalf("active strategy = %q, want %q", got, seal.StrategySystemBarrier)
	}

	// Seal, then re-attach + credential-free unseal (the boot path).
	if err := reg.SealTenant("vol-auto"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	if err := reg.UnsealTenant(ctx, "vol-auto", seal.Credentials{}); err != nil {
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
	reg, _, _ := newPooledRegistry(t)
	ctx := context.Background()

	_, tb, err := reg.RegisterVolumeTenant(ctx, "vol-self", "", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant (self): %v", err)
	}
	if !tb.IsSealed() {
		t.Fatal("self-managed barrier must be sealed until the tenant admin initializes it")
	}
}

// TestRegisterVolumeTenant_RejectsTraversalID verifies the filesystem
// containment guard: a tenant ID that would escape the pool root is rejected.
func TestRegisterVolumeTenant_RejectsTraversalID(t *testing.T) {
	reg, _, _ := newPooledRegistry(t)

	if _, _, err := reg.RegisterVolumeTenant(context.Background(), "../escape", "", false); err == nil {
		t.Fatal("expected error for traversal tenant ID, got nil")
	}
}

// TestRegisterVolumeTenant_EmptyTenantID verifies that RegisterVolumeTenant
// rejects an empty tenant ID immediately with ErrEmptyTenantID.
func TestRegisterVolumeTenant_EmptyTenantID(t *testing.T) {
	reg, _, _ := newPooledRegistry(t)

	_, _, err := reg.RegisterVolumeTenant(context.Background(), "", "", false)
	if err == nil {
		t.Fatal("expected ErrEmptyTenantID, got nil")
	}
	if !errors.Is(err, seal.ErrEmptyTenantID) {
		t.Errorf("error = %v, want wrapping ErrEmptyTenantID", err)
	}
}

// TestRegisterVolumeTenant_NoPoolConfigured verifies the volume path requires a
// configured volume pool.
func TestRegisterVolumeTenant_NoPoolConfigured(t *testing.T) {
	system := newPoolSystemBarrier(t)
	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	if _, _, err := reg.RegisterVolumeTenant(context.Background(), "vol-x", "", false); err == nil {
		t.Fatal("expected ErrPoolNotConfigured, got nil")
	}
}

// TestLocalTenantDataRoot verifies that LocalTenantDataRoot returns the same
// path that RegisterVolumeTenant would allocate for the tenant without opening
// any storage. This is the BUG 1 follower path: a replica whose placement
// DataRoot is "" calls LocalTenantDataRoot to compute its own local pool root.
func TestLocalTenantDataRoot(t *testing.T) {
	reg, roots, _ := newPooledRegistry(t)
	ctx := context.Background()

	// Pre-register the tenant so the pool's round-robin state advances and we
	// can compare the resulting path.
	dataRoot, _, err := reg.RegisterVolumeTenant(ctx, "loc-alpha", "", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}

	// LocalTenantDataRoot must return the SAME path without opening storage.
	resolved, err := reg.LocalTenantDataRoot(ctx, "loc-alpha")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot: %v", err)
	}

	// The resolved path must equal one of the pool roots joined with the tenantID.
	wantOne := false
	for _, r := range roots {
		if resolved == filepath.Join(r, "loc-alpha") {
			wantOne = true
		}
	}
	if !wantOne {
		t.Errorf("LocalTenantDataRoot = %q, not under any pool root %v", resolved, roots)
	}

	// The paths must match — the follower uses LocalTenantDataRoot to find its
	// shard DataDir, which must agree with what the leader persisted.
	if resolved != dataRoot {
		t.Errorf("LocalTenantDataRoot = %q, want %q (same as RegisterVolumeTenant)", resolved, dataRoot)
	}
}

// TestLocalTenantDataRoot_NoPool verifies that LocalTenantDataRoot returns an
// error when no volume pool is configured rather than silently returning empty.
func TestLocalTenantDataRoot_NoPool(t *testing.T) {
	system := newPoolSystemBarrier(t)
	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	_, err = reg.LocalTenantDataRoot(context.Background(), "any-tenant")
	if err == nil {
		t.Fatal("expected error from LocalTenantDataRoot with no pool configured, got nil")
	}
}

// TestLocalTenantDataRoot_NotRegistered verifies that LocalTenantDataRoot
// returns ErrTenantNotFound when the tenant has not yet been registered via
// RegisterVolumeTenant. This is the error the reconciler observes when key
// delivery has not yet completed (the shard start is deferred).
func TestLocalTenantDataRoot_NotRegistered(t *testing.T) {
	reg, _, _ := newPooledRegistry(t)
	_, err := reg.LocalTenantDataRoot(context.Background(), "never-registered")
	if err == nil {
		t.Fatal("expected error for unregistered tenant, got nil")
	}
	if !errors.Is(err, seal.ErrTenantNotFound) {
		t.Errorf("error = %v, want wrapping ErrTenantNotFound", err)
	}
}

// TestRestoreVolumeTenants_RestoredAfterFreshRegistry verifies the central
// restart-safety property: when RegisterVolumeTenant was called on a previous
// registry run (R1), a fresh BarrierRegistry (R2) backed by the same system
// barrier can restore the tenant and answer LocalTenantDataRoot without
// advancing the round-robin pool cursor.
func TestRestoreVolumeTenants_RestoredAfterFreshRegistry(t *testing.T) {
	ctx := context.Background()
	system, opts, roots := newShareableComponents(t)

	// Initialize the system barrier so persistTenantRoot can write.
	if err := system.Initialize(ctx, seal.Credentials{Secret: []byte("pool-test-pass")}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	// reg1: original node that registered the tenant.
	reg1, err := seal.NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}
	dataRoot1, _, err := reg1.RegisterVolumeTenant(ctx, "vol-restore", "", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant on reg1: %v", err)
	}

	// Verify the DataRoot was allocated under one of the pool roots.
	matched := false
	for _, r := range roots {
		if filepath.Join(r, "vol-restore") == dataRoot1 {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("reg1 DataRoot %q not under any pool root %v", dataRoot1, roots)
	}

	// reg2: simulates a fresh process (new BarrierRegistry, same system barrier).
	reg2, err := seal.NewBarrierRegistry(poolTestLogger(), system, opts...)
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

	if err := system.Initialize(ctx, seal.Credentials{Secret: []byte("pool-test-pass")}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg1, err := seal.NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}
	_, _, err = reg1.RegisterVolumeTenant(ctx, "vol-auto-restore", "", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant (auto): %v", err)
	}
	// Initialize so the DEK is written and wrapped under the system barrier.
	if err := reg1.InitializeTenant(ctx, "vol-auto-restore", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}

	// reg2: simulates restart with same system barrier.
	reg2, err := seal.NewBarrierRegistry(poolTestLogger(), system, opts...)
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

	if err := system.Initialize(ctx, seal.Credentials{Secret: []byte("pool-test-pass")}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg1, err := seal.NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}
	if _, _, err := reg1.RegisterVolumeTenant(ctx, "vol-self-restore", "", false); err != nil {
		t.Fatalf("RegisterVolumeTenant (self): %v", err)
	}

	// reg2: simulates restart.
	reg2, err := seal.NewBarrierRegistry(poolTestLogger(), system, opts...)
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

	if err := system.Initialize(ctx, seal.Credentials{Secret: []byte("pool-test-pass")}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	if _, _, err := reg.RegisterVolumeTenant(ctx, "vol-idem", "", false); err != nil {
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

// TestLocalTenantDataRoot_FallsBackToPersistentStore verifies the fallback
// path in LocalTenantDataRoot: when tenantDataRoots sync.Map is empty (as it
// is on a fresh registry after restart) but the system barrier holds a
// persisted DataRoot record, LocalTenantDataRoot must return the correct path
// without advancing the round-robin pool cursor.
func TestLocalTenantDataRoot_FallsBackToPersistentStore(t *testing.T) {
	ctx := context.Background()
	system, opts, roots := newShareableComponents(t)

	if err := system.Initialize(ctx, seal.Credentials{Secret: []byte("pool-test-pass")}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg1, err := seal.NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}
	dataRoot1, _, err := reg1.RegisterVolumeTenant(ctx, "loc-fallback", "", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant on reg1: %v", err)
	}

	// reg2: fresh tenantDataRoots sync.Map, same system barrier (has the record).
	reg2, err := seal.NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg2): %v", err)
	}

	// LocalTenantDataRoot must fall back to the persistent store.
	resolved, err := reg2.LocalTenantDataRoot(ctx, "loc-fallback")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot (fallback): %v", err)
	}

	// Verify the path is under one of the pool roots.
	valid := false
	for _, r := range roots {
		if filepath.Join(r, "loc-fallback") == resolved {
			valid = true
		}
	}
	if !valid {
		t.Errorf("resolved DataRoot %q not under any pool root %v", resolved, roots)
	}
	if resolved != dataRoot1 {
		t.Errorf("LocalTenantDataRoot = %q, want %q (same as reg1)", resolved, dataRoot1)
	}

	// Second call must return the same value (cache hit, not another store lookup).
	resolved2, err := reg2.LocalTenantDataRoot(ctx, "loc-fallback")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot (second call): %v", err)
	}
	if resolved2 != resolved {
		t.Errorf("LocalTenantDataRoot second call = %q, want %q (cache hit)", resolved2, resolved)
	}
}

// tenantRootKeyPrefix is the internal store key prefix used by
// BarrierRegistry.persistTenantRoot. It is declared here as a test-scoped
// constant so the coverage tests can write sentinel records directly without
// going through the public API, exercising the warning/skip branches inside
// RestoreVolumeTenants.
const tenantRootKeyPrefix = "__registry:tenant_root/"

// TestRestoreVolumeTenants_SkipsEmptyDataRoot verifies that
// RestoreVolumeTenants skips records whose data_root field is empty (which
// would otherwise open a barrier at the wrong path). The restore must return
// nil and NOT register the tenant.
func TestRestoreVolumeTenants_SkipsEmptyDataRoot(t *testing.T) {
	ctx := context.Background()
	system, opts, _ := newShareableComponents(t)

	if err := system.Initialize(ctx, seal.Credentials{Secret: []byte("pool-test-pass")}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	// Write a record with an empty DataRoot directly into the system barrier.
	if err := system.Put(ctx, tenantRootKeyPrefix+"empty-root", []byte(`{"data_root":""}`)); err != nil {
		t.Fatalf("system.Put empty DataRoot record: %v", err)
	}

	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	// RestoreVolumeTenants must skip the bad record without returning an error.
	if err := reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants returned error on empty DataRoot record: %v", err)
	}

	// The tenant with the empty DataRoot must NOT have been registered.
	if _, gerr := reg.GetTenant("empty-root"); gerr == nil {
		t.Fatal("expected empty-root tenant NOT to be registered after skip, but GetTenant succeeded")
	}
}

// TestRestoreVolumeTenants_SkipsCorruptRecord verifies that
// RestoreVolumeTenants skips records whose JSON payload is malformed and
// does not register the corresponding tenant or return an error.
func TestRestoreVolumeTenants_SkipsCorruptRecord(t *testing.T) {
	ctx := context.Background()
	system, opts, _ := newShareableComponents(t)

	if err := system.Initialize(ctx, seal.Credentials{Secret: []byte("pool-test-pass")}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	// Write a deliberately corrupt (non-JSON) payload.
	if err := system.Put(ctx, tenantRootKeyPrefix+"corrupt-tenant", []byte(`}{not valid json`)); err != nil {
		t.Fatalf("system.Put corrupt record: %v", err)
	}

	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	if err := reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants returned error on corrupt record: %v", err)
	}

	if _, gerr := reg.GetTenant("corrupt-tenant"); gerr == nil {
		t.Fatal("expected corrupt-tenant NOT to be registered, but GetTenant succeeded")
	}
}

// TestRestoreVolumeTenants_ShortKeySkipped injects a store key whose length
// equals the tenantRootKeyPrefix exactly (empty tenantID suffix). The loop
// guard in RestoreVolumeTenants must skip such entries and return nil.
//
// Covers: barrier_registry.go line 656-657 (len(key) <= prefixLen continue).
func TestRestoreVolumeTenants_ShortKeySkipped(t *testing.T) {
	ctx := context.Background()
	system, opts, _ := newShareableComponents(t)

	if err := system.Initialize(ctx, seal.Credentials{Secret: []byte("pool-test-pass")}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	// Write a key whose length equals the prefix (zero-length tenantID).
	if err := system.Put(ctx, tenantRootKeyPrefix, []byte(`{}`)); err != nil {
		t.Fatalf("system.Put at prefix-only key: %v", err)
	}

	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	if err := reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants returned error on short key: %v", err)
	}
	// The prefix-only entry must not cause any tenant to be registered.
	if ids := reg.ListTenants(); len(ids) != 0 {
		t.Errorf("expected no tenants, got %v", ids)
	}
}

// TestRestoreVolumeTenants_AutoUnsealFailure verifies the warning-and-continue
// path inside RestoreVolumeTenants for autoUnseal tenants whose barrier was
// never initialized: UnsealTenant fails (ErrNotInitialized) but
// RestoreVolumeTenants must return nil and the tenant must still be registered
// in the in-memory registry (registration succeeded; only the unseal failed).
//
// Covers: barrier_registry.go lines 699-703 (logger.Warn + continue on
// UnsealTenant failure).
func TestRestoreVolumeTenants_AutoUnsealFailure(t *testing.T) {
	ctx := context.Background()
	system, opts, _ := newShareableComponents(t)

	if err := system.Initialize(ctx, seal.Credentials{Secret: []byte("pool-test-pass")}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg1, err := seal.NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}

	// Register with autoUnseal=true but deliberately skip InitializeTenant.
	// The persistent DataRoot record is written, but the barrier Pebble has
	// no root-key blob. When reg2 restores and calls UnsealTenant it will
	// receive ErrNotInitialized, log a warning, and continue.
	if _, _, err := reg1.RegisterVolumeTenant(ctx, "uninit-auto", "", true); err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}

	// reg2 simulates a follower node or process restart.
	reg2, err := seal.NewBarrierRegistry(poolTestLogger(), system, opts...)
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
// system barrier is unsealed (so the persistent-store fallback path is
// reachable) but the tenant was never registered on any node,
// LocalTenantDataRoot returns ErrTenantNotFound. This covers the
// ErrSecretNotFound branch inside loadTenantRoot.
func TestLocalTenantDataRoot_NotInPersistentStoreEither(t *testing.T) {
	ctx := context.Background()
	system, opts, _ := newShareableComponents(t)

	// Unseal the system barrier so the persistent-store lookup is attempted.
	if err := system.Initialize(ctx, seal.Credentials{Secret: []byte("pool-test-pass")}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg, err := seal.NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	// "ghost-tenant" was never registered on any node, so no persistent record
	// exists and no in-memory entry exists. The fallback must propagate
	// ErrTenantNotFound.
	_, terr := reg.LocalTenantDataRoot(ctx, "ghost-tenant")
	if terr == nil {
		t.Fatal("expected ErrTenantNotFound for unregistered tenant, got nil")
	}
	if !errors.Is(terr, seal.ErrTenantNotFound) {
		t.Errorf("error = %v, want wrapping ErrTenantNotFound", terr)
	}
}

// TestRegisterVolumeTenant_MigrationRecoverer_RunsBeforeBarrierBuild verifies
// the crash-recovery hook attached via WithMigrationRecoverer is invoked over
// the tenant's freshly-opened backend, with the tenant's canonical root-key
// path, BEFORE the barrier is registered — the seam that closes the
// tenant-scope enable-encryption crash window.
func TestRegisterVolumeTenant_MigrationRecoverer_RunsBeforeBarrierBuild(t *testing.T) {
	reg, _, _ := newPooledRegistry(t)
	ctx := context.Background()

	var gotRootKeyPath string
	var gotBackend seal.StorageBackend
	calls := 0
	reg.Attach(seal.WithMigrationRecoverer(
		func(_ context.Context, backend seal.StorageBackend, rootKeyPath string) error {
			calls++
			gotBackend = backend
			gotRootKeyPath = rootKeyPath
			// The barrier must not exist yet when recovery runs.
			if _, err := reg.GetTenant("vol-recover"); err == nil {
				t.Error("recoverer ran AFTER the tenant barrier was registered")
			}
			return nil
		}))

	if _, _, err := reg.RegisterVolumeTenant(ctx, "vol-recover", "", true); err != nil {
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
	reg, _, _ := newPooledRegistry(t)
	ctx := context.Background()

	sentinel := errors.New("recovery exploded")
	reg.Attach(seal.WithMigrationRecoverer(
		func(context.Context, seal.StorageBackend, string) error { return sentinel }))

	_, _, err := reg.RegisterVolumeTenant(ctx, "vol-recover-fail", "", true)
	var mre *seal.MigrationRecoveryError
	if !errors.As(err, &mre) {
		t.Fatalf("err = %v, want *seal.MigrationRecoveryError", err)
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
