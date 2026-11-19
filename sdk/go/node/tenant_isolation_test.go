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

//go:build !integration

package node

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// awaitReadIndexReady performs a warm-up linearizable read, waiting out the
// ReadIndex window on a freshly (re-)attached tenant shard — a fresh leader
// rejects linearizable reads with quicraft.ErrReadIndexNotReady until its
// first entry of the current term commits. Bounded so a wedged shard still
// fails the test.
func awaitReadIndexReady(ctx context.Context, t *testing.T, kv service.KVServicer, key string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, err := kv.Get(ctx, key)
		if err == nil || !errors.Is(err, quicraft.ErrReadIndexNotReady) || time.Now().After(deadline) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestNode_TenantIsolation_EnableEncryption verifies zero-data-loss enable-
// encryption on an IsolationVolume tenant: write 100 keys through the
// tenant's none-strategy dedicated barrier, call Node.EnableEncryption with
// ScopeTenant, then verify all 100 keys are readable through the recycled,
// now-keyed tenant Host. The tenant barrier strategy must flip to passphrase
// after the migration.
func TestNode_TenantIsolation_EnableEncryption(t *testing.T) {
	ctx := context.Background()
	cfg := newTenantIsolationConfig(t)

	n, err := NewStandaloneNode(ctx, cfg, nil,
		WithTenantIsolation(),
		WithDefaultTenantEntitlements(volumeEntitlements()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	require.NotNil(t, n.tenantHosts)
	require.NotNil(t, n.encMigrator.Load(), "encMigrator must be built by wireClientAndServer")

	createDefaultPool(t, n)

	// Create the tenant.
	_, err = n.Services().Tenant.CreateTenant(ctx, "beta", "Beta Inc",
		tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true))
	require.NoError(t, err)

	// Get initial (none-strategy) tenant services and write data.
	svc, err := n.tenantHosts.Services(ctx, "beta")
	require.NoError(t, err)
	want := make(map[string]string, 100)
	for i := 0; i < 100; i++ {
		k := fmt.Sprintf("enc/key/%03d", i)
		v := fmt.Sprintf("enc-val-%d", i)
		require.NoErrorf(t, svc.KV.Put(ctx, k, []byte(v)), "Put(%q)", k)
		want[k] = v
	}

	// Enable tenant-scope encryption: none → passphrase. The scoped router in
	// n.encMigrator routes this to TenantHostRegistry.MigrateEncryption which
	// recycles only this tenant's Host, leaving the system data plane running.
	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("tenant-pass-beta")),
	}
	require.NoError(t, n.EnableEncryption(ctx,
		service.BarrierScope{Kind: service.ScopeTenant, TenantID: "beta"}, opts),
		"tenant-scope enable-encryption must succeed")

	// Verify the system data plane is untouched: the node's system KV must
	// still be responsive after the per-tenant migration.
	sysKV := n.KVService()
	require.NotNil(t, sysKV, "system KV must be live after tenant migration")
	require.NoError(t, sysKV.Put(ctx, "sys/health", []byte("ok")))

	// Resolve the post-migration tenant services (TenantHostRegistry re-attaches
	// the Host under the keyed barrier at the end of MigrateEncryption).
	svc2, err := n.tenantHosts.Services(ctx, "beta")
	require.NoError(t, err, "Services(beta) must resolve post-migration Host")
	require.NotNil(t, svc2.KV)

	// Verify all 100 keys are readable through the now-keyed tenant Host.
	awaitReadIndexReady(ctx, t, svc2.KV, "enc/key/000")
	for k, v := range want {
		got, gerr := svc2.KV.Get(ctx, k)
		require.NoErrorf(t, gerr, "Get(%q) after tenant encryption migration", k)
		require.Equalf(t, v, string(got), "value for %q after tenant migration", k)
	}

	// Verify the tenant barrier strategy flipped to passphrase.
	tb, err := n.barrierRegistry.Load().GetTenant("beta")
	require.NoError(t, err)
	st := tb.Barrier().Status()
	require.Equalf(t, seal.StrategyPassphrase, st.Strategy,
		"tenant barrier strategy must be passphrase after migration")
}

// TestNode_TenantIsolation_RestartSurvival verifies that a volume-isolation
// tenant survives a node close-and-reopen: after writing data and closing the
// node, a fresh node constructed from the same dataDir with WithTenantIsolation
// must restore the tenant barrier (via RestoreVolumeTenants) and return all
// previously-written keys intact through the re-attached tenant Host.
func TestNode_TenantIsolation_RestartSurvival(t *testing.T) {
	ctx := context.Background()
	cfg := newTenantIsolationConfig(t)

	// --- First boot: create tenant and write data. ---------------------------
	n, err := NewStandaloneNode(ctx, cfg, nil,
		WithTenantIsolation(),
		WithDefaultTenantEntitlements(volumeEntitlements()),
	)
	require.NoError(t, err)

	require.NotNil(t, n.tenantHosts)

	createDefaultPool(t, n)

	_, err = n.Services().Tenant.CreateTenant(ctx, "gamma", "Gamma LLC",
		tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true))
	require.NoError(t, err)

	svc, err := n.tenantHosts.Services(ctx, "gamma")
	require.NoError(t, err)
	want := make(map[string]string, 100)
	for i := 0; i < 100; i++ {
		k := fmt.Sprintf("persist/key/%03d", i)
		v := fmt.Sprintf("persist-val-%d", i)
		require.NoErrorf(t, svc.KV.Put(ctx, k, []byte(v)), "Put(%q)", k)
		want[k] = v
	}
	require.NoError(t, n.Close(), "first node must close cleanly")

	// --- Second boot: reopen from the same data dir. ------------------------
	// Use the same config (same dataDir, same pool paths). WithTenantIsolation
	// causes completeStandaloneBoot to call BuildTenantInfra, which calls
	// RestoreVolumeTenants to re-register gamma's barrier from the persisted
	// tenant-root record, and reattachVolumeTenants to eagerly rebuild gamma's
	// Host (auto-unseal = true, so the barrier is unsealed credential-free).
	n2, err := NewStandaloneNode(ctx, cfg, nil,
		WithTenantIsolation(),
		WithDefaultTenantEntitlements(volumeEntitlements()),
	)
	require.NoError(t, err, "second boot from same dataDir must succeed")
	t.Cleanup(func() { _ = n2.Close() })

	require.NotNil(t, n2.tenantHosts, "tenantHosts must be non-nil on second boot")

	// The tenant host should already be attached (auto-unseal eager re-attach).
	svc2, err := n2.tenantHosts.Services(ctx, "gamma")
	require.NoError(t, err, "Services(gamma) must succeed on second boot (barrier restored)")
	require.NotNil(t, svc2.KV)

	// All previously-written keys must be intact.
	awaitReadIndexReady(ctx, t, svc2.KV, "post/restart-warmup")
	for k, v := range want {
		got, gerr := svc2.KV.Get(ctx, k)
		require.NoErrorf(t, gerr, "Get(%q) after restart", k)
		require.Equalf(t, v, string(got), "value for %q after restart", k)
	}

	// Verify new writes succeed on the re-attached host.
	require.NoError(t, svc2.KV.Put(ctx, "post/restart", []byte("alive")))
	got, err := svc2.KV.Get(ctx, "post/restart")
	require.NoError(t, err)
	require.Equal(t, "alive", string(got))
}

// TestNode_ClusteredIsolationRaft_KV verifies the end-to-end IsolationRaft path
// on an embedded single-node clustered node: create a raft-isolation tenant,
// create a dedicated-raft database through the lifecycle-built DatabaseService,
// write 100 KV keys through the tenant's dedicated raft shard, and read them
// all back. This exercises BuildTenantInfra's RaftBackendFactory.Services (lazy
// shard start on the shared quicraft.Host) and the per-shard Pebble state
// machine stack.
// createRaftTenant creates an IsolationRaft tenant with a system-managed
// auto-unseal barrier and provisions its home group, as onboarding does
// (bootstrap.ProvisionTenant): TenantService.CreateTenant alone places no home,
// and the tenant's database catalog records live in it.
func createRaftTenant(t *testing.T, n *Node, id, name string) {
	t.Helper()
	ctx := context.Background()
	_, err := n.Services().Tenant.CreateTenant(ctx, id, name,
		tenant.IsolationRaft, tenant.WithBarrierAutoUnseal(true))
	require.NoError(t, err, "CreateTenant(IsolationRaft) must succeed with raft entitlements")
	router, ok := n.Services().StoreResolver.(tenant.HomeRouter)
	require.True(t, ok, "the node's StoreResolver must route tenant homes")
	require.NoError(t, router.EnsureHome(ctx, id), "EnsureHome(%s)", id)
}

func TestNode_ClusteredIsolationRaft_KV(t *testing.T) {
	ctx := context.Background()
	cfg := newRaftIsolationConfig(t)

	n, err := NewClusteredNode(ctx, cfg, raftMembers(), nil,
		WithTenantIsolation(),
		WithDefaultTenantEntitlements(raftEntitlements()),
		WithNoSyncWrites(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	require.False(t, n.IsSealed(), "none-strategy node must boot unsealed")
	require.NotNil(t, n.raftBackend, "raftBackend must be wired when WithTenantIsolation() is set on a clustered node")

	createDefaultPool(t, n)

	// Create an IsolationRaft tenant with a system-managed auto-unseal barrier.
	// CreateTenant allocates the DataRoot from the pool, registers the tenant
	// barrier, and initializes it so the tenant is immediately ready for use.
	createRaftTenant(t, n, "acme", "Acme Corp")

	// Create a dedicated-raft database for the tenant. The lifecycle-built
	// DatabaseService has WithTenantInfoLookup wired (via buildDatabaseQuotaOpts)
	// so it auto-sets Backend=Raft, DataDir, and Raft.ClusterMode=Local from
	// the tenant record without requiring explicit backend flags in the call.
	_, err = n.Services().Database.CreateForTenant(
		tenant.WithTenantID(ctx, "acme"),
		database.DatabaseConfig{Name: "orders"},
	)
	require.NoError(t, err, "CreateForTenant(orders) must succeed for IsolationRaft tenant")

	// Resolve the per-database data-plane services. RaftBackendFactory lazily
	// starts the dedicated raft shard on the shared quicraft.Host on first call.
	svc, err := n.raftBackend.Services(ctx, "acme", "orders")
	require.NoError(t, err, "Services(acme/orders) must resolve the dedicated-raft shard")
	require.NotNil(t, svc, "DatabaseServices must be non-nil")
	require.NotNil(t, svc.KV, "tenant KV service must be non-nil")

	// Write 100 keys through the tenant's dedicated raft shard.
	want := make(map[string]string, 100)
	for i := 0; i < 100; i++ {
		k := fmt.Sprintf("raft/key/%03d", i)
		v := fmt.Sprintf("raft-value-%d", i)
		require.NoErrorf(t, svc.KV.Put(ctx, k, []byte(v)), "Put(%q)", k)
		want[k] = v
	}

	// Read all 100 keys back and verify values.
	for k, v := range want {
		got, gerr := svc.KV.Get(ctx, k)
		require.NoErrorf(t, gerr, "Get(%q)", k)
		require.Equalf(t, v, string(got), "value for %q", k)
	}

	// Verify the system data plane is isolated from the tenant raft shard: a
	// system-level write and read must succeed alongside the tenant shard.
	sysKV := n.KVService()
	require.NotNil(t, sysKV, "system KV must be live alongside the tenant raft shard")
	require.NoError(t, sysKV.Put(ctx, "sys/isolation/probe", []byte("ok")))
	sysGot, sysErr := sysKV.Get(ctx, "sys/isolation/probe")
	require.NoError(t, sysErr)
	require.Equal(t, "ok", string(sysGot))
}

// TestNode_ClusteredIsolationRaft_EnableEncryption verifies zero-data-loss
// enable-encryption on an IsolationRaft tenant on a clustered embedded node:
// write 100 keys through the tenant's none-strategy dedicated raft shard, call
// Node.EnableEncryption with ScopeTenant, then verify all 100 keys are readable
// through the recycled, now-keyed raft shard. The tenant barrier strategy must
// flip to passphrase after the migration. The system data plane must remain
// untouched throughout.
func TestNode_ClusteredIsolationRaft_EnableEncryption(t *testing.T) {
	ctx := context.Background()
	cfg := newRaftIsolationConfig(t)

	n, err := NewClusteredNode(ctx, cfg, raftMembers(), nil,
		WithTenantIsolation(),
		WithDefaultTenantEntitlements(raftEntitlements()),
		WithNoSyncWrites(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	require.NotNil(t, n.raftBackend)
	require.NotNil(t, n.encMigrator.Load(), "encMigrator must be built by wireClientAndServer")

	createDefaultPool(t, n)

	// Create the tenant and a dedicated-raft database.
	createRaftTenant(t, n, "beta", "Beta Inc")

	_, err = n.Services().Database.CreateForTenant(
		tenant.WithTenantID(ctx, "beta"),
		database.DatabaseConfig{Name: "sales"},
	)
	require.NoError(t, err)

	// Start the shard and write 100 keys. Services() MUST be called BEFORE
	// EnableEncryption: MigrateTenantEncryption requires at least one started
	// shard in the factory's live-shard map so it knows which shards to recycle.
	svc, err := n.raftBackend.Services(ctx, "beta", "sales")
	require.NoError(t, err, "Services(beta/sales) must start the dedicated-raft shard")
	require.NotNil(t, svc.KV)

	want := make(map[string]string, 100)
	for i := 0; i < 100; i++ {
		k := fmt.Sprintf("enc/key/%03d", i)
		v := fmt.Sprintf("enc-val-%d", i)
		require.NoErrorf(t, svc.KV.Put(ctx, k, []byte(v)), "Put(%q)", k)
		want[k] = v
	}

	// Enable tenant-scope encryption: none → passphrase. The scoped router in
	// n.encMigrator routes ScopeTenant to RaftBackendFactory.MigrateTenantEncryption,
	// which stops the shard, migrates the tenant barrier, and restarts the shard
	// under the keyed barrier — leaving the system data plane running.
	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("tenant-pass-beta")),
	}
	require.NoError(t, n.EnableEncryption(ctx,
		service.BarrierScope{Kind: service.ScopeTenant, TenantID: "beta"}, opts),
		"tenant-scope enable-encryption must succeed on raft shard")

	// Verify the system data plane is untouched after the per-tenant migration.
	sysKV := n.KVService()
	require.NotNil(t, sysKV, "system KV must be live after tenant raft migration")
	require.NoError(t, sysKV.Put(ctx, "sys/health", []byte("ok")))

	// Resolve the post-migration tenant services. RaftBackendFactory re-registers
	// the shard under the keyed barrier at the end of MigrateTenantEncryption, so
	// Services() returns a fresh DatabaseServices pointing at the keyed shard.
	svc2, err := n.raftBackend.Services(ctx, "beta", "sales")
	require.NoError(t, err, "Services(beta/sales) must resolve the post-migration shard")
	require.NotNil(t, svc2.KV)

	// All 100 keys must be readable through the now-keyed raft shard.
	for k, v := range want {
		got, gerr := svc2.KV.Get(ctx, k)
		require.NoErrorf(t, gerr, "Get(%q) after raft shard encryption migration", k)
		require.Equalf(t, v, string(got), "value for %q after raft migration", k)
	}

	// The tenant barrier strategy must have flipped to passphrase.
	tb, err := n.barrierRegistry.Load().GetTenant("beta")
	require.NoError(t, err)
	st := tb.Barrier().Status()
	require.Equalf(t, seal.StrategyPassphrase, st.Strategy,
		"tenant barrier strategy must be passphrase after raft encryption migration")
}
