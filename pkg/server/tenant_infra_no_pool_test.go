package server

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// noPoolRaftConfig is a raft-mode node with no volume pool configured
// whose prefix tenant "pfx" holds the backend: raft database "app".
func noPoolRaftConfig(t *testing.T) TenantInfraConfig {
	t.Helper()
	svc, reg := newRaftReattachFixture(t)
	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "pfx", "Prefix", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant(pfx): %v", err)
	}
	mgr := claimingManager(t, reattachTestLogger(), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	if _, err := mgr.CreateForTenant(tenant.WithTenantID(ctx, "pfx"), database.DatabaseConfig{
		Name:    "app",
		Backend: database.BackendRaft,
		Raft:    &database.DatabaseRaftConfig{ClusterMode: database.RaftLocal},
	}); err != nil {
		t.Fatalf("CreateForTenant(app): %v", err)
	}
	return TenantInfraConfig{
		Ctx:        ctx,
		BarrierReg: reg,
		TenantSvc:  svc,
		Logger:     reattachTestLogger(),
		Host:       newRaftReattachHost(t),
		Manager:    mgr,
		ReplicaID:  raftReattachReplicaID,
		RaftAddr:   raftReattachRaftAddr,
	}
}

// A raft-mode node with no volume pool still builds the raft database
// factory, and a prefix tenant's backend: raft database runs its own group.
func TestBuildTenantInfra_RaftFactoryWithoutVolumePool(t *testing.T) {
	cfg := noPoolRaftConfig(t)
	ti, err := BuildTenantInfra(cfg)
	if err != nil {
		t.Fatalf("BuildTenantInfra: %v", err)
	}
	if ti.Raft == nil {
		t.Fatal("BuildTenantInfra built no raft factory on a node without a volume pool")
	}
	t.Cleanup(func() { _ = ti.Raft.CloseAll() })
	if ti.Hosts != nil {
		t.Fatal("a volume host registry was built without a volume pool")
	}
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{TenantID: "pfx", Subject: "alice"})
	svcs, err := ti.Raft.Services(ctx, "pfx", "app")
	if err != nil {
		t.Fatalf("prefix-tenant raft database without a volume pool: %v", err)
	}
	if !ti.Raft.HostsShard("pfx", "app") {
		t.Fatal("the prefix-tenant raft database did not start its own group")
	}
	if err := svcs.KV.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("KV.Put on the database's group: %v", err)
	}
	if got, err := svcs.KV.Get(ctx, "k"); err != nil || string(got) != "v" {
		t.Fatalf("KV.Get = %q, %v; want v", got, err)
	}
}

// A standalone node (no Host) builds no raft factory, pool or not, and
// does not fail.
func TestBuildTenantInfra_StandaloneWithoutVolumePoolBuildsNoRaftFactory(t *testing.T) {
	cfg := noPoolRaftConfig(t)
	cfg.Host, cfg.Manager = nil, nil
	ti, err := BuildTenantInfra(cfg)
	if err != nil {
		t.Fatalf("BuildTenantInfra standalone: %v", err)
	}
	if ti.Raft != nil || ti.Reconciler != nil {
		t.Fatal("a standalone node built a raft factory")
	}
}

// The placement root of a tenant whose groups root in a volume pool is
// refused with *RaftGroupVolumePoolRequiredError when no pool is
// configured; prefix and barrier tenants root on the node data dir.
func TestIsolationRootResolver_VolumeTenantWithoutPool(t *testing.T) {
	isolations := map[string]tenant.IsolationType{
		"pfx": tenant.IsolationPrefix, "bar": tenant.IsolationBarrier,
		"vol": tenant.IsolationVolume, "rft": tenant.IsolationRaft,
	}
	lookup := func(_ context.Context, id string) (tenant.IsolationType, error) { return isolations[id], nil }
	nodeDir := t.TempDir()
	resolve := IsolationRootResolver(lookup, nodeDir, nil)
	for _, id := range []string{"pfx", "bar"} {
		root, err := resolve(context.Background(), id)
		if err != nil || root != filepath.Join(nodeDir, "tenants", id) {
			t.Fatalf("root of %s = %q, %v; want the node data dir", id, root, err)
		}
	}
	for _, id := range []string{"vol", "rft"} {
		_, err := resolve(context.Background(), id)
		var required *RaftGroupVolumePoolRequiredError
		if !errors.As(err, &required) || required.TenantID != id || required.Isolation != isolations[id] {
			t.Fatalf("root of %s without a pool = %v; want *RaftGroupVolumePoolRequiredError", id, err)
		}
	}
	poolRoot := filepath.Join(nodeDir, "pool")
	withPool := IsolationRootResolver(lookup, nodeDir, func(context.Context, string) (string, error) { return poolRoot, nil })
	if root, err := withPool(context.Background(), "vol"); err != nil || root != poolRoot {
		t.Fatalf("root of vol with a pool = %q, %v; want %q", root, err, poolRoot)
	}
}
