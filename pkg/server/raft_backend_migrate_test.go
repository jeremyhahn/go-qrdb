// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package server

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// getWhenReadable performs a linearizable KV read, retrying while the shard's
// ReadIndex is still warming up after a restart ("read index not ready" is a
// designed-for transient right after leader election, not a data-loss signal).
// Bounded at 15s so a genuinely wedged shard still fails the test.
func getWhenReadable(ctx context.Context, t *testing.T, get func(context.Context, string) ([]byte, error), key string) ([]byte, error) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		got, err := get(ctx, key)
		if err == nil || !strings.Contains(err.Error(), "read index not ready") || time.Now().After(deadline) {
			return got, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// raftMigrateStack is a single-node IsolationRaft stack whose tenant barriers are
// DEDICATED and UNENCRYPTED (none) — the migratable source state.
type raftMigrateStack struct {
	reg     *seal.BarrierRegistry
	tenants *tenant.TenantService
	dbSvc   service.DatabaseServicer
	raftReg *RaftBackendFactory
}

// newRaftMigrateStack builds the stack with a none system strategy so that a
// self-managed (auto-unseal=false) tenant barrier initialized with empty
// credentials is StrategyNone — exactly what an enable-encryption migration
// converts from.
func newRaftMigrateStack(t *testing.T) *raftMigrateStack {
	t.Helper()
	reg := newVolumePooledRegistryWith(t, seal.NewNoneStrategy())

	tenants, err := tenant.NewTenantService(seal.NewMemoryBackend(), reg, volTestLogger(),
		tenant.WithDefaultEntitlements(&quota.Entitlements{
			AllowedIsolationModes: []string{quota.IsolationModePrefix, quota.IsolationModeRaft},
			AllowDedicatedRaft:    true,
		}))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}

	mgr := claimingManager(t, volTestLogger(), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	dbSvc := service.NewDatabaseService(mgr,
		service.WithEntitlementChecker(tenants),
		service.WithTenantInfoLookup(tenants),
	)

	host, _ := newSharedTestHost(t)
	raftReg := NewRaftBackendFactoryFromManager(
		host, sharedHostReplicaID, sharedHostRaftAddr, reg, mgr, tenantid.System, volTestLogger())
	t.Cleanup(func() { _ = raftReg.CloseAll() })

	return &raftMigrateStack{reg: reg, tenants: tenants, dbSvc: dbSvc, raftReg: raftReg}
}

// createDedicatedRaftDB creates a self-managed IsolationRaft tenant with a
// dedicated none barrier and one database, returning the tenant DataRoot.
func (s *raftMigrateStack) createDedicatedRaftDB(t *testing.T, tenantID, db string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := s.tenants.CreateTenant(ctx, tenantID, tenantID, tenant.IsolationRaft,
		tenant.WithBarrierAutoUnseal(false)); err != nil {
		t.Fatalf("CreateTenant(%s): %v", tenantID, err)
	}
	// Self-managed barrier: initialize as none (unsealed) so the shard can start
	// and the barrier is a valid migration source.
	if err := s.reg.InitializeTenant(ctx, tenantID, seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant(%s): %v", tenantID, err)
	}
	if _, err := s.dbSvc.CreateForTenant(tenant.WithTenantID(ctx, tenantID),
		database.DatabaseConfig{Name: db, Engine: database.EngineKV}); err != nil {
		t.Fatalf("CreateForTenant(%s/%s): %v", tenantID, db, err)
	}
	ti, err := s.tenants.GetTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenant(%s): %v", tenantID, err)
	}
	return ti.DataRoot
}

// TestRaftBackendFactory_MigrateTenantEncryption_NoneToPassphrase is the
// single-node proof of the IsolationRaft per-shard encryption recycle: a
// dedicated-none raft tenant with live KV + MVCC data is migrated to passphrase
// in place; the shard's SM Pebble is re-encrypted, the tenant barrier flips to
// keyed, the shard re-attaches, and every datum survives and stays writable.
func TestRaftBackendFactory_MigrateTenantEncryption_NoneToPassphrase(t *testing.T) {
	ctx := context.Background()
	s := newRaftMigrateStack(t)
	s.createDedicatedRaftDB(t, "rt", "appdata")

	// Seed data through the live (unencrypted) shard across both engines.
	svcs, err := s.raftReg.Services(ctx, "rt", "appdata")
	if err != nil {
		t.Fatalf("Services(pre-migrate): %v", err)
	}
	if err := svcs.KV.Put(ctx, "kv-key", []byte("kv-value")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}
	if err := svcs.MVCC.Put(ctx, "mv-key", []byte("mv-value")); err != nil {
		t.Fatalf("MVCC.Put: %v", err)
	}

	// Pre-migration: the dedicated tenant barrier is genuinely none.
	if got := tenantStrategy(t, s.reg, "rt"); got != seal.StrategyNone {
		t.Fatalf("pre-migrate tenant strategy = %q, want none", got)
	}

	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials([]byte("raft-migrate-pw")),
	}
	if err := s.raftReg.MigrateTenantEncryption(ctx, "rt", opts); err != nil {
		t.Fatalf("MigrateTenantEncryption: %v", err)
	}

	// The live tenant barrier is now keyed.
	if got := tenantStrategy(t, s.reg, "rt"); got != seal.StrategyPassphrase {
		t.Fatalf("post-migrate tenant strategy = %q, want passphrase", got)
	}

	// All data survives the recycle, readable through the re-attached shard.
	svcs, err = s.raftReg.Services(ctx, "rt", "appdata")
	if err != nil {
		t.Fatalf("Services(post-migrate): %v", err)
	}
	if got, err := getWhenReadable(ctx, t, svcs.KV.Get, "kv-key"); err != nil || string(got) != "kv-value" {
		t.Fatalf("KV.Get(post-migrate) = %q, %v; want kv-value", got, err)
	}
	if got, err := svcs.MVCC.Get(ctx, "mv-key"); err != nil || string(got) != "mv-value" {
		t.Fatalf("MVCC.Get(post-migrate) = %q, %v; want mv-value", got, err)
	}

	// Writes continue to work post-migration (the keyed barrier is fully live).
	if err := svcs.KV.Put(ctx, "kv-key2", []byte("after")); err != nil {
		t.Fatalf("KV.Put(post-migrate): %v", err)
	}
	if got, err := svcs.KV.Get(ctx, "kv-key2"); err != nil || string(got) != "after" {
		t.Fatalf("KV.Get(post-migrate new) = %q, %v; want after", got, err)
	}

	// At-rest proof: stop the shard so files flush, the plaintext must be absent.
	dataRoot := mustTenantDataRoot(t, s.tenants, "rt")
	if err := s.raftReg.Close("rt", "appdata"); err != nil {
		t.Fatalf("stop shard: %v", err)
	}
	assertPlaintextAbsent(t, filepath.Join(dataRoot, "databases", "appdata"), []byte("kv-value"))
}

// TestRaftBackendFactory_MigrateTenantEncryption_NoneToShamir proves the
// IsolationRaft per-shard recycle produces a GENUINE M-of-N Shamir tenant
// barrier (correct label, correct threshold/total, real usable shares) rather
// than a silently mislabeled passphrase barrier. Like the IsolationVolume
// path, the reopened keyed barrier is SEALED (Shamir never auto-unseals from
// disk — see ReopenKeyed): the deferred shard re-attach correctly tolerates
// the expected seal.ErrSealed instead of reporting it as a migration failure,
// and the shards stay detached until a Security Officer quorum unseals the
// barrier via UnsealWithShare(s), at which point the normal Attach retry
// succeeds and every pre-migration datum is intact.
func TestRaftBackendFactory_MigrateTenantEncryption_NoneToShamir(t *testing.T) {
	ctx := context.Background()
	s := newRaftMigrateStack(t)
	s.createDedicatedRaftDB(t, "rt-shamir", "appdata")

	tb, err := s.reg.GetTenant("rt-shamir")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	dist := &serverCaptureDist{}
	tb.Barrier().SetCeremonyDistributor(dist)

	svcs, err := s.raftReg.Services(ctx, "rt-shamir", "appdata")
	if err != nil {
		t.Fatalf("Services(pre-migrate): %v", err)
	}
	if err := svcs.KV.Put(ctx, "kv-key", []byte("kv-value")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}

	opts := seal.MigrationOptions{
		TargetStrategy:  seal.StrategyShamir,
		Mechanism:       seal.MechanismSwap,
		Availability:    seal.AvailabilityQuiesced,
		ShamirThreshold: 2,
		ShamirTotal:     3,
	}
	if err := s.raftReg.MigrateTenantEncryption(ctx, "rt-shamir", opts); err != nil {
		t.Fatalf("MigrateTenantEncryption(shamir): %v", err)
	}
	if len(dist.Shares()) != 3 {
		t.Fatalf("distributor captured %d shares, want 3", len(dist.Shares()))
	}

	status := tenantStatus(t, s.reg, "rt-shamir")
	if status.Strategy != seal.StrategyShamir {
		t.Fatalf("post-migrate strategy = %q, want shamir (not silently downgraded to passphrase)", status.Strategy)
	}
	if !status.Sealed {
		t.Fatal("post-migrate shamir tenant barrier must be sealed pending Security Officer quorum")
	}
	if status.ShamirThreshold != 2 || status.ShamirTotal != 3 {
		t.Fatalf("post-migrate scheme = %d/%d, want 2/3", status.ShamirThreshold, status.ShamirTotal)
	}

	// The shard correctly stays detached: Services must fail with the typed
	// seal.ErrSealed, not silently serve stale state and not have aborted the
	// migration (already proven above — MigrateTenantEncryption returned nil).
	if _, err := s.raftReg.Services(ctx, "rt-shamir", "appdata"); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("Services on a sealed post-migration raft tenant = %v, want seal.ErrSealed", err)
	}

	// Submitting a 2-of-3 quorum of the REAL distributed shares unseals the
	// migrated barrier — the "shares are usable" proof.
	tb2, err := s.reg.GetTenant("rt-shamir")
	if err != nil {
		t.Fatalf("GetTenant(post-migrate): %v", err)
	}
	shares := dist.Shares()
	if _, uerr := tb2.UnsealWithShare(ctx, shares[0]); uerr != nil {
		t.Fatalf("UnsealWithShare(1/2): %v", uerr)
	}
	progress, uerr := tb2.UnsealWithShare(ctx, shares[1])
	if uerr != nil {
		t.Fatalf("UnsealWithShare(2/2): %v", uerr)
	}
	if !progress.Complete || tb2.IsSealed() {
		t.Fatalf("barrier not unsealed after a 2-of-3 quorum: progress=%+v sealed=%v", progress, tb2.IsSealed())
	}

	// Every datum written before the migration survives, readable once the
	// shard reattaches under the now-unsealed keyed barrier.
	if err := s.raftReg.Attach(ctx, "rt-shamir", "appdata"); err != nil {
		t.Fatalf("Attach(post-unseal): %v", err)
	}
	svcs, err = s.raftReg.Services(ctx, "rt-shamir", "appdata")
	if err != nil {
		t.Fatalf("Services(post-unseal): %v", err)
	}
	if got, gerr := getWhenReadable(ctx, t, svcs.KV.Get, "kv-key"); gerr != nil || string(got) != "kv-value" {
		t.Fatalf("KV.Get(post-unseal) = %q, %v; want kv-value (DATA LOSS)", got, gerr)
	}
}

// mustTenantDataRoot returns a tenant's DataRoot or fails the test.
func mustTenantDataRoot(t *testing.T, tenants *tenant.TenantService, id string) string {
	t.Helper()
	ti, err := tenants.GetTenant(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTenant(%s): %v", id, err)
	}
	return ti.DataRoot
}

// TestRaftBackendFactory_MigrateTenantEncryption_InvalidTarget rejects a
// none/empty target before any data movement.
func TestRaftBackendFactory_MigrateTenantEncryption_InvalidTarget(t *testing.T) {
	ctx := context.Background()
	s := newRaftMigrateStack(t)
	s.createDedicatedRaftDB(t, "rt", "appdata")
	if _, err := s.raftReg.Services(ctx, "rt", "appdata"); err != nil {
		t.Fatalf("Services: %v", err)
	}
	if err := s.raftReg.MigrateTenantEncryption(ctx, "rt",
		seal.MigrationOptions{TargetStrategy: seal.StrategyNone}); err == nil {
		t.Fatal("MigrateTenantEncryption with none target must be rejected")
	}
	if got := tenantStrategy(t, s.reg, "rt"); got != seal.StrategyNone {
		t.Fatalf("tenant strategy after rejected migration = %q, want none", got)
	}
}

// TestMigrateTenantEncryption_Unavailable proves the nil/misconfigured guards.
func TestMigrateTenantEncryption_Unavailable(t *testing.T) {
	ctx := context.Background()
	var nilFactory *RaftBackendFactory
	if err := nilFactory.MigrateTenantEncryption(ctx, "rt", seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase, Mechanism: seal.MechanismSwap, Availability: seal.AvailabilityQuiesced,
		Credentials: seal.MustCredentials([]byte("pw")),
	}); !errors.Is(err, ErrRaftBackendUnavailable) {
		t.Fatalf("nil factory MigrateTenantEncryption = %v, want ErrRaftBackendUnavailable", err)
	}

	// Invalid options are rejected before any barrier work.
	s := newRaftMigrateStack(t)
	if err := s.raftReg.MigrateTenantEncryption(ctx, "rt", seal.MigrationOptions{TargetStrategy: seal.StrategyNone}); err == nil {
		t.Fatal("MigrateTenantEncryption with none target must error")
	}
}

// TestRaftBackendFactory_MigrateTenantEncryption_RecodeFailureRollsBack injects a
// re-encryption failure mid-recycle and proves the crash-safe rollback: the
// tenant barrier stays UNencrypted (none), the shard re-attaches under the
// original barrier, and no data is lost or stranded — the failure path that the
// integration tests (success-only) never exercise.
func TestRaftBackendFactory_MigrateTenantEncryption_RecodeFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	s := newRaftMigrateStack(t)
	s.createDedicatedRaftDB(t, "rt", "appdata")

	svcs, err := s.raftReg.Services(ctx, "rt", "appdata")
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if err := svcs.KV.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}

	// Inject a recode failure (the swap-mechanism re-encryption step).
	boom := errors.New("induced recode failure")
	s.raftReg.recodeOverride = func(context.Context, string, *atrest.Encryptor, string, *atrest.Encryptor) (int64, error) {
		return 0, boom
	}

	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase, Mechanism: seal.MechanismSwap,
		Availability: seal.AvailabilityQuiesced, Credentials: seal.MustCredentials([]byte("pw")),
	}
	if err := s.raftReg.MigrateTenantEncryption(ctx, "rt", opts); !errors.Is(err, boom) {
		t.Fatalf("MigrateTenantEncryption = %v, want the injected recode failure", err)
	}

	// Rollback: the barrier is untouched (still none).
	if got := tenantStrategy(t, s.reg, "rt"); got != seal.StrategyNone {
		t.Fatalf("post-failure tenant strategy = %q, want none (rollback)", got)
	}
	// The shard re-attached under the original barrier; data survives + writable.
	svcs, err = s.raftReg.Services(ctx, "rt", "appdata")
	if err != nil {
		t.Fatalf("Services after rollback: %v", err)
	}
	if got, gerr := getWhenReadable(ctx, t, svcs.KV.Get, "k"); gerr != nil || string(got) != "v" {
		t.Fatalf("post-rollback data = %q, %v; want v (no data loss)", got, gerr)
	}
	if err := svcs.KV.Put(ctx, "k2", []byte("v2")); err != nil {
		t.Fatalf("post-rollback write failed: %v", err)
	}

	// No staging copies are left behind under the tenant db tree.
	dataRoot := mustTenantDataRoot(t, s.tenants, "rt")
	dbDir := filepath.Join(dataRoot, "databases", "appdata")
	var staging []string
	_ = filepath.WalkDir(dbDir, func(path string, d fs.DirEntry, _ error) error {
		if d != nil && d.IsDir() && strings.HasSuffix(path, database.KeyedDirSuffix) {
			staging = append(staging, path)
		}
		return nil
	})
	if len(staging) != 0 {
		t.Fatalf("rollback left staging dirs behind: %v", staging)
	}
}

// TestRaftBackendFactory_MigrateTenantEncryption_SealedSource proves a sealed
// tenant barrier (key not present on this node) is rejected before any recycle.
func TestRaftBackendFactory_MigrateTenantEncryption_SealedSource(t *testing.T) {
	ctx := context.Background()
	s := newRaftMigrateStack(t)
	s.createDedicatedRaftDB(t, "rt", "appdata")
	if _, err := s.raftReg.Services(ctx, "rt", "appdata"); err != nil {
		t.Fatalf("Services: %v", err)
	}
	// Seal the tenant barrier so the migration source is unreadable.
	if err := s.reg.SealTenant("rt"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	err := s.raftReg.MigrateTenantEncryption(ctx, "rt", seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase, Mechanism: seal.MechanismSwap,
		Availability: seal.AvailabilityQuiesced, Credentials: seal.MustCredentials([]byte("pw")),
	})
	if !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("MigrateTenantEncryption(sealed source) = %v, want ErrSealed", err)
	}
}

// TestRaftBackendFactory_MigrateTenantEncryption_RetryAfterRollback proves the
// staging-blob wedge is fixed: a failed migration rolls back AND cleans up its
// staging keyed root-key blob, so a subsequent retry succeeds (rather than
// failing forever in MintKeyedSibling → Initialize with ErrAlreadyInitialized).
func TestRaftBackendFactory_MigrateTenantEncryption_RetryAfterRollback(t *testing.T) {
	ctx := context.Background()
	s := newRaftMigrateStack(t)
	s.createDedicatedRaftDB(t, "rt", "appdata")
	svcs, err := s.raftReg.Services(ctx, "rt", "appdata")
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if err := svcs.KV.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}

	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase, Mechanism: seal.MechanismSwap,
		Availability: seal.AvailabilityQuiesced, Credentials: seal.MustCredentials([]byte("pw")),
	}

	// First attempt fails mid-recode → rolls back.
	boom := errors.New("induced recode failure")
	s.raftReg.recodeOverride = func(context.Context, string, *atrest.Encryptor, string, *atrest.Encryptor) (int64, error) {
		return 0, boom
	}
	if err := s.raftReg.MigrateTenantEncryption(ctx, "rt", opts); !errors.Is(err, boom) {
		t.Fatalf("first attempt = %v, want induced failure", err)
	}

	// Retry with the real recode — must SUCCEED (staging blob was cleaned up).
	s.raftReg.recodeOverride = nil
	if err := s.raftReg.MigrateTenantEncryption(ctx, "rt", opts); err != nil {
		t.Fatalf("retry after rollback failed (staging-blob wedge?): %v", err)
	}
	if got := tenantStrategy(t, s.reg, "rt"); got != seal.StrategyPassphrase {
		t.Fatalf("post-retry strategy = %q, want passphrase", got)
	}
	svcs, err = s.raftReg.Services(ctx, "rt", "appdata")
	if err != nil {
		t.Fatalf("Services after retry: %v", err)
	}
	if got, gerr := getWhenReadable(ctx, t, svcs.KV.Get, "k"); gerr != nil || string(got) != "v" {
		t.Fatalf("post-retry data = %q, %v; want v", got, gerr)
	}
}

// TestScopedEncryptionMigrator_RoutesIsolationRaft proves a tenant whose
// isolation mode is IsolationRaft is routed to the per-shard raft recycle
// (MigrateTenantEncryption), not the IsolationVolume host path. The tenant has a
// dedicated barrier (passes the authority gate) but no started shard, so the raft
// path surfaces ErrRaftDatabaseUnknown — proof it reached the raft factory.
func TestScopedEncryptionMigrator_RoutesIsolationRaft(t *testing.T) {
	ctx := context.Background()
	s := newRaftMigrateStack(t)
	// Dedicated raft tenant with a none barrier, but no database/shard started.
	if _, err := s.tenants.CreateTenant(ctx, "rt", "rt", tenant.IsolationRaft,
		tenant.WithBarrierAutoUnseal(false)); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := s.reg.InitializeTenant(ctx, "rt", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}

	m := NewScopedEncryptionMigrator(nil, nil, s.raftReg, s.reg, s.tenants)
	err := m.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeTenant, TenantID: "rt"},
		seal.MigrationOptions{
			TargetStrategy: seal.StrategyPassphrase,
			Mechanism:      seal.MechanismSwap,
			Availability:   seal.AvailabilityQuiesced,
			Credentials:    seal.MustCredentials([]byte("pw")),
		})
	if !errors.Is(err, ErrRaftDatabaseUnknown) {
		t.Fatalf("IsolationRaft tenant routed to %v, want raft path (ErrRaftDatabaseUnknown)", err)
	}
}

// TestRaftBackendFactory_MigrateTenantEncryption_NoShards reports an unknown
// database when the tenant has no started shards on this node.
func TestRaftBackendFactory_MigrateTenantEncryption_NoShards(t *testing.T) {
	ctx := context.Background()
	s := newRaftMigrateStack(t)
	// Tenant + barrier exist but no shard was ever started.
	if _, err := s.tenants.CreateTenant(ctx, "rt", "rt", tenant.IsolationRaft,
		tenant.WithBarrierAutoUnseal(false)); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := s.reg.InitializeTenant(ctx, "rt", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}
	err := s.raftReg.MigrateTenantEncryption(ctx, "rt", seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials([]byte("pw")),
	})
	if !errors.Is(err, ErrRaftDatabaseUnknown) {
		t.Fatalf("MigrateTenantEncryption(no shards) = %v, want ErrRaftDatabaseUnknown", err)
	}
}
