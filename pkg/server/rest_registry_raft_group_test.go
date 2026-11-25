package server

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// raftGroupRegistry builds a resolver over a raft backend factory for a
// tenant of the given isolation that owns the backend: raft database "app"
// (OD-1 A: one Raft group per database, whatever the tenant's isolation).
func raftGroupRegistry(t *testing.T, tenantID string, isolation tenant.IsolationType, dataRoot string, register bool) (*restServiceRegistry, *RaftBackendFactory, *DatabaseServiceRegistry) {
	t.Helper()
	// A volume tenant's group log always runs on a dedicated WAL, which the
	// host's multiplex log DB carries.
	host, mux := newSharedTestHostWithMux(t)
	reg := newVolumePooledRegistry(t)
	if register {
		root, _ := registerVolumeTenant(t, reg, tenantID)
		if dataRoot == "" {
			dataRoot = root
		}
	}
	if dataRoot == "" {
		dataRoot = t.TempDir()
	}
	src := newFakeRaftInfoSource()
	src.add(tenantID, "app", 1100, dataRoot)
	lookup := func(context.Context, string) (tenant.IsolationType, error) { return isolation, nil }
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src, volTestLogger(),
		WithMultiplexLogDB(mux), WithTenantIsolation(lookup))
	t.Cleanup(func() { _ = f.CloseAll() })

	info := &tenant.TenantInfo{ID: tenantID, IsolationType: isolation}
	if isolation == tenant.IsolationVolume {
		info.VolumePool = "default"
	}
	shared := newFilledRegistry()
	rr := &restServiceRegistry{
		dbReg:        shared,
		barrierReg:   reg,
		tenantSvc:    &fakeRaftTenantSvc{info: info},
		catalog:      raftCatalogStub{tenantID: tenantID, db: "app"},
		raftBackend:  f,
		systemTenant: "system",
	}
	return rr, f, shared
}

// assertWriteLandsInOwnGroup writes through the resolver and asserts the key
// sits at the root of the database's own group store and nowhere in the
// shared (system) shard.
func assertWriteLandsInOwnGroup(t *testing.T, rr *restServiceRegistry, f *RaftBackendFactory, shared *DatabaseServiceRegistry, tenantID string) {
	t.Helper()
	ctx := context.Background()
	kv, err := rr.KVService(tenantID, "app")
	if err != nil {
		t.Fatalf("KVService(%s/app): %v", tenantID, err)
	}
	if err := kv.Put(ctx, "k", []byte(raftGroupValue)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	base, err := f.Services(ctx, tenantID, "app")
	if err != nil {
		t.Fatalf("factory.Services: %v", err)
	}
	raw, err := base.KV.Get(ctx, "k")
	if err != nil || string(raw) != raftGroupValue {
		t.Fatalf("own group KV.Get(k) = %q, %v; want %q", raw, err, raftGroupValue)
	}
	for _, key := range []string{"k", tenant.ComposeNamespace(tenantID, "app") + "k", tenantID + "/app/k"} {
		if got, err := shared.Default().KV.Get(ctx, key); err == nil && got != nil {
			t.Errorf("shared shard holds %q = %q; the write must go to the database's own group", key, got)
		}
	}
}

// raftGroupValue is long and distinctive so a raw scan of the group's files
// would find it if it were stored in plaintext.
const raftGroupValue = "raft-group-at-rest-plaintext-value-0123456789"

// assertGroupStoreUnder asserts the group's state machine wrote files under
// root and that none of them holds the value in plaintext (it is encrypted
// under the group's barrier).
func assertGroupStoreUnder(t *testing.T, root string) {
	t.Helper()
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatalf("open root %s: %v", root, err)
	}
	defer func() { _ = r.Close() }()
	files := 0
	err = fs.WalkDir(r.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		files++
		data, rerr := r.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if bytes.Contains(data, []byte(raftGroupValue)) {
			t.Errorf("%s holds the value in plaintext", filepath.Join(root, path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if files == 0 {
		t.Fatalf("no group store files under %s", root)
	}
}

func TestRESTServiceRegistry_PrefixTenantRaftDatabaseUsesItsOwnGroup(t *testing.T) {
	root := t.TempDir()
	rr, f, shared := raftGroupRegistry(t, "pfx", tenant.IsolationPrefix, root, false)
	assertWriteLandsInOwnGroup(t, rr, f, shared, "pfx")
	assertGroupStoreUnder(t, root)
}

func TestRESTServiceRegistry_BarrierTenantRaftDatabaseUsesItsOwnGroup(t *testing.T) {
	root := t.TempDir()
	rr, f, shared := raftGroupRegistry(t, "bar", tenant.IsolationBarrier, root, true)
	assertWriteLandsInOwnGroup(t, rr, f, shared, "bar")
	assertGroupStoreUnder(t, root)

	// The group runs under the tenant barrier, not the node's: while the
	// tenant's barrier is sealed its group cannot start, though the node
	// barrier is unsealed.
	ctx := context.Background()
	allocVal, _ := testVolumeAllocators.Load(rr.barrierReg)
	sealtest.ReserveVolumeTenant(t, allocVal.(volume.Allocator), "sealedbar")
	if _, _, err := rr.barrierReg.RegisterVolumeTenant(ctx, "sealedbar", false); err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	src := f.dbInfo.(*fakeRaftInfoSource)
	src.add("sealedbar", "app", 1101, t.TempDir())
	if _, err := f.Services(ctx, "sealedbar", "app"); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("group start under a sealed tenant barrier = %v, want seal.ErrSealed", err)
	}
}

func TestRESTServiceRegistry_VolumeTenantRaftDatabaseUsesItsOwnGroup(t *testing.T) {
	rr, f, shared := raftGroupRegistry(t, "vol", tenant.IsolationVolume, "", true)
	rr.tenantHosts = nil
	assertWriteLandsInOwnGroup(t, rr, f, shared, "vol")
	// The group lands under the tenant root the volume registration gave.
	_, dir, ok := f.dbInfo.databaseInfo("vol", "app", true)
	if !ok {
		t.Fatal("no group info for vol/app")
	}
	assertGroupStoreUnder(t, dir)
}

// TestRestRegistry_RaftDatabaseServesOwnGroupWithSharedPrefixKeys proves keys under the database's prefix in the shared shard do not keep it off its own group.
func TestRestRegistry_RaftDatabaseServesOwnGroupWithSharedPrefixKeys(t *testing.T) {
	rr, f, shared := raftGroupRegistry(t, "pfx", tenant.IsolationPrefix, "", false)
	ctx := context.Background()
	sharedKey := tenant.ComposeNamespace("pfx", "app") + "old"
	if err := shared.Default().KV.Put(ctx, sharedKey, []byte("shared")); err != nil {
		t.Fatalf("seed shared key: %v", err)
	}
	svcs, err := rr.resolve("pfx", "app")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if svcs.Placement != service.StorePlacementShard {
		t.Fatalf("Placement = %q, want %q", svcs.Placement, service.StorePlacementShard)
	}
	if !f.HostsShard("pfx", "app") {
		t.Fatal("the raft database did not start its own group")
	}
	raw, err := svcs.KV.Get(ctx, "old")
	if err == nil && string(raw) == "shared" {
		t.Fatal("the raft database served the shared shard's keys")
	}
	assertWriteLandsInOwnGroup(t, rr, f, shared, "pfx")
}

// TestStoreResolver_FencesAPrefixTenantRaftDatabaseInItsGroup: the drop
// fence of a prefix tenant's raft database goes to its own group, by the
// rule resolve applies, and a legacy one keeps the shared store.
func TestStoreResolver_FencesAPrefixTenantRaftDatabaseInItsGroup(t *testing.T) {
	rr, f, shared := raftGroupRegistry(t, "pfx", tenant.IsolationPrefix, "", false)
	withRaftBackendFactory(f)(rr)
	sr := &storeResolver{raft: f, tenantSvc: rr.tenantSvc, systemTenant: "system"}
	ctx := context.Background()

	store, ok, err := sr.FenceTarget(ctx, "pfx", "app", "")
	if err != nil || !ok || !store.PerDatabase || store.KV == nil {
		t.Fatalf("FenceTarget = %+v, %v, %v; want the group's per-database store", store, ok, err)
	}

	if err := shared.Default().KV.Put(ctx, tenant.ComposeNamespace("pfx", "app")+"old", []byte("shared")); err != nil {
		t.Fatalf("seed shared key: %v", err)
	}
	if store, ok, err := sr.FenceTarget(ctx, "pfx", "app", ""); err != nil || !ok || !store.PerDatabase {
		t.Fatalf("FenceTarget with shared prefix keys = %+v, %v, %v; want the group's per-database store", store, ok, err)
	}
}
