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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
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
// swallowing it -- its own doc comment in barrier_registry_pool_test.go says it
// "return[s] an empty slice when the directory does not exist", and the code
// does not do that -- so ENOENT arrives here and must be read as absence.
// Anything else (a permission failure, a corrupt mount) is a measurement that
// did not happen, and fails the test.
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

// TestRegisterVolumeTenant_AllocatesDataRootAndBarrier verifies the
// IsolationVolume registration layout: DataRoot is allocated under a pool root
// as {root}/{tenantID}, and the barrier IAM Pebble lands in the dedicated
// "barrier" subdirectory, leaving "databases" free for the per-tenant Host.
func TestRegisterVolumeTenant_AllocatesDataRootAndBarrier(t *testing.T) {
	reg, roots, _ := newPooledRegistryUnsealed(t)
	ctx := context.Background()

	dataRoot, tb, err := reg.RegisterVolumeTenant(ctx, "vol-alpha", false)
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

// secondNodeOptions builds a pool for a SECOND node: the same pool name with
// the same number of candidate paths, at different directories -- what another
// node's configuration maps the pool to. It shares nothing with the first
// node's pool except the name.
func secondNodeOptions(t *testing.T) (opts []Option, roots []string) {
	t.Helper()
	c1, p1 := configuredLocal(t)
	c2, p2 := configuredLocal(t)
	mgr := objpool.NewManager()
	state, _, _ := newStateStore(t)
	if err := mgr.Register("default", []common.Storage{c1, c2}, &objpool.RoundRobinStrategy{State: state}); err != nil {
		t.Fatalf("second node pool Register: %v", err)
	}
	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })
	return []Option{WithVolumePool(mgr, state, "default"), WithPerTenantRegistry(perTenantReg)}, []string{p1, p2}
}

// slotOfRoot returns the index of the pool root dataRoot lies under, or -1.
func slotOfRoot(roots []string, tenantID, dataRoot string) int {
	for i, r := range roots {
		if filepath.Join(r, tenantID) == dataRoot {
			return i
		}
	}
	return -1
}

// TestTenantRootRecord_RecordsPoolAndSlotNotPath verifies the persisted
// tenant-root record names the pool and relative slot the tenant was given and
// carries no filesystem path: the record is replicated, and a path would name
// the registering node's disk.
func TestTenantRootRecord_RecordsPoolAndSlotNotPath(t *testing.T) {
	reg, roots, _ := newPooledRegistryUnsealed(t)
	ctx := context.Background()

	dataRoot, _, err := reg.RegisterVolumeTenant(ctx, "vol-rec", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	raw, err := reg.SystemBarrier().Get(ctx, tenantRootKeyPrefix+"vol-rec")
	if err != nil {
		t.Fatalf("read tenant-root record: %v", err)
	}
	if bytes.Contains(raw, []byte("data_root")) || bytes.Contains(raw, []byte(dataRoot)) {
		t.Fatalf("tenant-root record %s names a path; it must record only the pool and slot", raw)
	}
	var rec tenantRootRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("decode tenant-root record: %v", err)
	}
	if rec.Pool != "default" || rec.Slot != slotOfRoot(roots, "vol-rec", dataRoot) {
		t.Fatalf("record = pool %q slot %d, want pool default slot %d", rec.Pool, rec.Slot, slotOfRoot(roots, "vol-rec", dataRoot))
	}
}

// TestLocalTenantDataRoot_ResolvesAgainstThisNodesPool is the cross-node
// data-loss regression: two nodes share the replicated tenant-root record but
// map the pool to different directories. The second node must resolve the
// tenant to ITS OWN candidate path for the recorded slot -- never the path the
// first node registered at, which on a shared host is the first node's live
// directory.
func TestLocalTenantDataRoot_ResolvesAgainstThisNodesPool(t *testing.T) {
	ctx := context.Background()
	system, optsA, rootsA := newShareableComponents(t)
	if err := system.Initialize(ctx, MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	nodeA, err := NewBarrierRegistry(poolTestLogger(), system, optsA...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (node A): %v", err)
	}
	rootA, _, err := nodeA.RegisterVolumeTenant(ctx, "shared", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant on node A: %v", err)
	}
	slot := slotOfRoot(rootsA, "shared", rootA)
	if slot < 0 {
		t.Fatalf("node A root %q is not under its pool %v", rootA, rootsA)
	}

	optsB, rootsB := secondNodeOptions(t)
	nodeB, err := NewBarrierRegistry(poolTestLogger(), system, optsB...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (node B): %v", err)
	}
	rootB, err := nodeB.LocalTenantDataRoot(ctx, "shared")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot on node B: %v", err)
	}
	if want := filepath.Join(rootsB[slot], "shared"); rootB != want {
		t.Fatalf("node B resolved %q, want its own slot-%d path %q (node A's is %q)", rootB, slot, want, rootA)
	}
}

// TestTenantRootRecord_AbsolutePathRefused verifies a tenant-root record that
// carries a path is refused rather than trusted: LocalTenantDataRoot returns
// *AbsoluteDataRootRecordError naming the tenant and the path, and boot restore
// skips the tenant without opening anything at that path.
func TestTenantRootRecord_AbsolutePathRefused(t *testing.T) {
	ctx := context.Background()
	system, opts, _ := newShareableComponents(t)
	if err := system.Initialize(ctx, MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	foreign := filepath.Join(t.TempDir(), "another-node", "legacy")
	rec := `{"pool":"default","slot":0,"data_root":"` + foreign + `","auto_unseal":true}`
	if err := system.Put(ctx, tenantRootKeyPrefix+"legacy", []byte(rec)); err != nil {
		t.Fatalf("system.Put legacy record: %v", err)
	}
	reg, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	_, err = reg.LocalTenantDataRoot(ctx, "legacy")
	var abs *AbsoluteDataRootRecordError
	if !errors.As(err, &abs) || abs.TenantID != "legacy" || abs.DataRoot != foreign {
		t.Fatalf("LocalTenantDataRoot = %v, want *AbsoluteDataRootRecordError for legacy at %s", err, foreign)
	}
	if err := reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants must skip, not abort on, a refused record: %v", err)
	}
	if _, gerr := reg.GetTenant("legacy"); gerr == nil {
		t.Error("a tenant whose record names a path must not be restored")
	}
	if !pebbleAbsent(t, filepath.Join(foreign, "barrier")) {
		t.Error("restore opened a store at the path a refused record named")
	}
}

// TestReattachVolumeTenant_PresentStoreSucceeds verifies the boot re-attach
// path succeeds when the tenant's persisted barrier store is present on disk:
// reg1 creates it, a fresh reg2 (same system barrier + pool) re-attaches it at
// the directory the recorded slot resolves to.
func TestReattachVolumeTenant_PresentStoreSucceeds(t *testing.T) {
	ctx := context.Background()
	system, opts, _ := newShareableComponents(t)
	if err := system.Initialize(ctx, MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg1, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}
	dataRoot, _, err := reg1.RegisterVolumeTenant(ctx, "present", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant (create): %v", err)
	}
	assertPebbleAtPath(t, filepath.Join(dataRoot, "barrier"))
	if err := reg1.ReleaseTenant(ctx, "present"); err != nil {
		t.Fatalf("ReleaseTenant (reg1): %v", err)
	}

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
// guard: re-attaching a tenant whose recorded slot resolves to a directory
// whose barrier store is NOT present (the pool's mount absent, or an empty
// leftover dir) must fail closed with DataRootMountMissingError and must NOT
// mint a fresh empty barrier.
func TestReattachVolumeTenant_AbsentStoreReturnsMountMissing(t *testing.T) {
	ctx := context.Background()
	record := []byte(`{"pool":"default","slot":0,"auto_unseal":false}`)

	t.Run("missing directory", func(t *testing.T) {
		reg, roots, _ := newPooledRegistryUnsealed(t)
		if err := reg.SystemBarrier().Put(ctx, tenantRootKeyPrefix+"absent", record); err != nil {
			t.Fatalf("put record: %v", err)
		}
		_, _, err := reg.ReattachVolumeTenant(ctx, "absent", false)
		assertMountMissing(t, reg, "absent", filepath.Join(roots[0], "absent"), err)
	})

	t.Run("empty barrier directory", func(t *testing.T) {
		reg, roots, _ := newPooledRegistryUnsealed(t)
		if err := reg.SystemBarrier().Put(ctx, tenantRootKeyPrefix+"empty", record); err != nil {
			t.Fatalf("put record: %v", err)
		}
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
// sealed, rather than silently proceeding: the tenant-root record it resolves
// from and must write lives behind the system barrier, so a sealed barrier
// would read as "no record" and a direct call on this registry (bypassing
// TenantService.CreateTenant, whose own tombstone check already fails closed
// through the sealed barrier) would allocate a second location and open a
// Pebble store there.
func TestRegisterVolumeTenant_SealedSystemBarrierRefusesClosed(t *testing.T) {
	// newPooledRegistry's system barrier starts sealed (never initialized);
	// this test is the one place in the file that must NOT unseal it.
	reg, roots, _ := newPooledRegistry(t)
	ctx := context.Background()

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
	for _, r := range roots {
		if !pebbleAbsent(t, filepath.Join(r, "sealed-refusal", "barrier")) {
			t.Error("a Pebble store was opened despite the sealed-system refusal; must fail closed before touching storage")
		}
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

// TestRestoreVolumeTenants_MissingMountSkipped verifies that boot restore fails
// a tenant whose pool mount is absent CLOSED (skips it, logs) instead of
// silently opening a fresh empty barrier — and does not abort the whole node.
func TestRestoreVolumeTenants_MissingMountSkipped(t *testing.T) {
	ctx := context.Background()
	system, opts, roots := newShareableComponents(t)
	if err := system.Initialize(ctx, MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	// Persist a tenant-root record whose slot resolves to a directory that
	// was never created (the pool's disk was not mounted this boot).
	rec := `{"pool":"default","slot":1,"auto_unseal":false}`
	if err := system.Put(ctx, tenantRootKeyPrefix+"ghost", []byte(rec)); err != nil {
		t.Fatalf("system.Put missing-mount record: %v", err)
	}
	missingRoot := filepath.Join(roots[1], "ghost")

	reg, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
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
	if err := reg.SystemBarrier().Initialize(ctx, MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

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

	_, tb, err := reg.RegisterVolumeTenant(ctx, "vol-self", false)
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

	if _, _, err := reg.RegisterVolumeTenant(context.Background(), "../escape", false); err == nil {
		t.Fatal("expected error for traversal tenant ID, got nil")
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
// configured volume pool.
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
// path that RegisterVolumeTenant would allocate for the tenant without opening
// any storage. This is the BUG 1 follower path: a replica whose placement
// DataRoot is "" calls LocalTenantDataRoot to compute its own local pool root.
func TestLocalTenantDataRoot(t *testing.T) {
	reg, roots, _ := newPooledRegistryUnsealed(t)
	ctx := context.Background()

	// Pre-register the tenant so the pool's round-robin state advances and we
	// can compare the resulting path.
	dataRoot, _, err := reg.RegisterVolumeTenant(ctx, "loc-alpha", false)
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
// returns ErrTenantNotFound when the tenant has not yet been registered via
// RegisterVolumeTenant. This is the error the reconciler observes when key
// delivery has not yet completed (the shard start is deferred).
func TestLocalTenantDataRoot_NotRegistered(t *testing.T) {
	reg, _, _ := newPooledRegistry(t)
	_, err := reg.LocalTenantDataRoot(context.Background(), "never-registered")
	if err == nil {
		t.Fatal("expected error for unregistered tenant, got nil")
	}
	if !errors.Is(err, ErrTenantNotFound) {
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
	if err := system.Initialize(ctx, MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	// reg1: original node that registered the tenant.
	reg1, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}
	dataRoot1, _, err := reg1.RegisterVolumeTenant(ctx, "vol-restore", false)
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

	if err := system.Initialize(ctx, MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg1, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}
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

	if err := system.Initialize(ctx, MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg1, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}
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

	if err := system.Initialize(ctx, MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
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

// TestLocalTenantDataRoot_FallsBackToPersistentStore verifies the fallback
// path in LocalTenantDataRoot: when tenantDataRoots sync.Map is empty (as it
// is on a fresh registry after restart) but the system barrier holds a
// persisted DataRoot record, LocalTenantDataRoot must return the correct path
// without advancing the round-robin pool cursor.
func TestLocalTenantDataRoot_FallsBackToPersistentStore(t *testing.T) {
	ctx := context.Background()
	system, opts, roots := newShareableComponents(t)

	if err := system.Initialize(ctx, MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg1, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}
	dataRoot1, _, err := reg1.RegisterVolumeTenant(ctx, "loc-fallback", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant on reg1: %v", err)
	}

	// reg2: fresh tenantDataRoots sync.Map, same system barrier (has the record).
	reg2, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
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

	if err := system.Initialize(ctx, MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	// Write a record with an empty DataRoot directly into the system barrier.
	if err := system.Put(ctx, tenantRootKeyPrefix+"empty-root", []byte(`{"data_root":""}`)); err != nil {
		t.Fatalf("system.Put empty DataRoot record: %v", err)
	}

	reg, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
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

	if err := system.Initialize(ctx, MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	// Write a deliberately corrupt (non-JSON) payload.
	if err := system.Put(ctx, tenantRootKeyPrefix+"corrupt-tenant", []byte(`}{not valid json`)); err != nil {
		t.Fatalf("system.Put corrupt record: %v", err)
	}

	reg, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
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

	if err := system.Initialize(ctx, MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	// Write a key whose length equals the prefix (zero-length tenantID).
	if err := system.Put(ctx, tenantRootKeyPrefix, []byte(`{}`)); err != nil {
		t.Fatalf("system.Put at prefix-only key: %v", err)
	}

	reg, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
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

	if err := system.Initialize(ctx, MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg1, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (reg1): %v", err)
	}

	// Register with autoUnseal=true but deliberately skip InitializeTenant.
	// The persistent DataRoot record is written, but the barrier Pebble has
	// no root-key blob. When reg2 restores and calls UnsealTenant it will
	// receive ErrNotInitialized, log a warning, and continue.
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
// system barrier is unsealed (so the persistent-store fallback path is
// reachable) but the tenant was never registered on any node,
// LocalTenantDataRoot returns ErrTenantNotFound. This covers the
// ErrSecretNotFound branch inside loadTenantRoot.
func TestLocalTenantDataRoot_NotInPersistentStoreEither(t *testing.T) {
	ctx := context.Background()
	system, opts, _ := newShareableComponents(t)

	// Unseal the system barrier so the persistent-store lookup is attempted.
	if err := system.Initialize(ctx, MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	reg, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
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
	if !errors.Is(terr, ErrTenantNotFound) {
		t.Errorf("error = %v, want wrapping ErrTenantNotFound", terr)
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
