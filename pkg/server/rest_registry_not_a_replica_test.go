package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// notAReplicaRegistry builds a raft-mode REST registry for a prefix tenant
// whose backend: raft database "app" is resolved through the placement
// table: placed reports whether the table records an RF=1 placement naming
// only another node.
func notAReplicaRegistry(t *testing.T, placed bool) (*restServiceRegistry, *RaftBackendFactory, *DatabaseServiceRegistry) {
	t.Helper()
	host, mux := newSharedTestHostWithMux(t)
	reg := newVolumePooledRegistry(t)
	store := newPlacementStore(t)
	if placed {
		if err := store.Put(context.Background(), &placement.Placement{
			TenantID: "pfx", Database: "app", ShardID: 7781, RF: 1, LeaderReplicaID: 1,
			Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "remote:1", RaftAddr: "remote:1"}},
		}); err != nil {
			t.Fatalf("Put placement: %v", err)
		}
	}
	fallback := newFakeRaftInfoSource()
	fallback.add("pfx", "app", 1101, t.TempDir()) // the local catalog knows it too
	src := &placementRaftInfoSource{store: store, localNodeID: "local:1", fallback: fallback,
		timeout: placementResolveTimeout, localRootResolver: poolRootResolver, logger: volTestLogger()}
	lookup := func(context.Context, string) (tenant.IsolationType, error) { return tenant.IsolationPrefix, nil }
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src, volTestLogger(),
		WithMultiplexLogDB(mux), WithTenantIsolation(lookup))
	t.Cleanup(func() { _ = f.CloseAll() })
	shared := newFilledRegistry()
	rr := &restServiceRegistry{
		dbReg:        shared,
		barrierReg:   reg,
		tenantSvc:    &fakeRaftTenantSvc{info: &tenant.TenantInfo{ID: "pfx", IsolationType: tenant.IsolationPrefix}},
		catalog:      raftCatalogStub{tenantID: "pfx", db: "app"},
		raftBackend:  f,
		systemTenant: "system",
	}
	return rr, f, shared
}

// A backend: raft database whose placement names only other nodes is
// refused with *placement.NotAReplicaError -- the trigger that forwards the
// request to a replica -- and is never served from the shared store, which
// the real replicas never see.
func TestRESTServiceRegistry_NotAReplicaRaftDatabaseIsRefusedNotShared(t *testing.T) {
	rr, f, shared := notAReplicaRegistry(t, true)
	hosted, err := f.HostsDatabase("pfx", "app")
	var nar *placement.NotAReplicaError
	if hosted || !errors.As(err, &nar) {
		t.Fatalf("HostsDatabase = %v, %v; want *placement.NotAReplicaError", hosted, err)
	}
	kv, err := rr.KVService("pfx", "app")
	if !errors.As(err, &nar) {
		t.Fatalf("KVService = %v, %v; want *placement.NotAReplicaError", kv, err)
	}
	if nar.ShardID != 7781 || len(nar.Replicas) != 1 || nar.Replicas[0].NodeID != "remote:1" {
		t.Fatalf("NotAReplicaError = %+v; want shard 7781 with replica remote:1", nar)
	}
	if kv != nil && kv == shared.Default().KV {
		t.Fatal("a non-replica request was served from the shared store")
	}
	f.mu.Lock()
	started := len(f.shards)
	f.mu.Unlock()
	if started != 0 {
		t.Fatalf("a node that is not a replica started %d shard(s)", started)
	}
}

// With no placement recorded the database still resolves through the local
// catalog and runs its own group.
func TestRESTServiceRegistry_RaftDatabaseWithoutPlacementFallsBack(t *testing.T) {
	rr, f, shared := notAReplicaRegistry(t, false)
	hosted, err := f.HostsDatabase("pfx", "app")
	if err != nil || !hosted {
		t.Fatalf("HostsDatabase = %v, %v; want true via the local catalog", hosted, err)
	}
	assertWriteLandsInOwnGroup(t, rr, f, shared, "pfx")
}
