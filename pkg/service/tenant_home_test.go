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

package service

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// newHomeDatabaseService returns a placement-wired DatabaseService for the
// raft tenant "rt" over a cluster of nodes.
func newHomeDatabaseService(t *testing.T, nodes []placement.NodeLoad) (*DatabaseService, *placement.Store) {
	t.Helper()
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 100}}
	svc, store := newPlacementDatabaseService(t, raftTenant(t), f, nodes)
	ds := svc.(*DatabaseService)
	// A tenant home grows only through a raft host (growHome).
	ds.SetRaftHost(&fakeRaftHost{leaderID: 1, leaderOK: true})
	return ds, store
}

func TestProvisionTenantHome_PlacesOnEveryNode(t *testing.T) {
	svc, store := newHomeDatabaseService(t, healthyNodes("n1", "n2", "n3"))
	ctx := context.Background()

	plan, err := svc.ProvisionTenantHome(ctx, "rt")
	if err != nil {
		t.Fatalf("ProvisionTenantHome: %v", err)
	}
	if plan.Database != database.TenantHomeName || plan.TenantID != "rt" {
		t.Fatalf("placement = %s/%s, want rt/%s", plan.TenantID, plan.Database, database.TenantHomeName)
	}
	if plan.RF != 3 || len(plan.Replicas) != 3 {
		t.Fatalf("RF = %d with %d replicas, want 3 (every node)", plan.RF, len(plan.Replicas))
	}
	if plan.ShardID == 0 {
		t.Fatal("the home group must carry a ledger-issued shard ID")
	}
	stored, err := store.Get(ctx, "rt", database.TenantHomeName)
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	if stored.ShardID != plan.ShardID {
		t.Fatalf("stored shard %d, returned %d", stored.ShardID, plan.ShardID)
	}
	if !plan.DedicatedWAL || !stored.DedicatedWAL {
		t.Fatalf("the home group must keep its Raft log in a WAL under the tenant's key: returned %t, stored %t", plan.DedicatedWAL, stored.DedicatedWAL)
	}
	// The home group is a placement only: no catalog database exists for it.
	if _, err := svc.GetForTenant(tenant.WithTenantID(ctx, "rt"), database.TenantHomeName); err == nil {
		t.Fatal("the home group must not be a catalog database")
	}
}

func TestProvisionTenantHome_Idempotent(t *testing.T) {
	svc, _ := newHomeDatabaseService(t, healthyNodes("n1", "n2"))
	ctx := context.Background()

	first, err := svc.ProvisionTenantHome(ctx, "rt")
	if err != nil {
		t.Fatalf("first ProvisionTenantHome: %v", err)
	}
	second, err := svc.ProvisionTenantHome(ctx, "rt")
	if err != nil {
		t.Fatalf("second ProvisionTenantHome: %v", err)
	}
	if second.ShardID != first.ShardID {
		t.Fatalf("re-provisioning claimed a new shard %d, want the existing %d", second.ShardID, first.ShardID)
	}
}

// homeOnTopology provisions tenant rt's home group over a topology the test
// can change afterwards, as nodes join or replace one another.
func homeOnTopology(t *testing.T, nodes []placement.NodeLoad) (*DatabaseService, *placement.Store, *staticTopology) {
	t.Helper()
	svc, store := newHomeDatabaseService(t, nodes)
	topo := &staticTopology{nodes: nodes}
	sched, err := placement.NewScheduler(topo)
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}
	svc.placementScheduler = sched
	if _, err := svc.ProvisionTenantHome(context.Background(), "rt"); err != nil {
		t.Fatalf("ProvisionTenantHome: %v", err)
	}
	return svc, store, topo
}

// TestReconcileTenantHomes_ShedsAVoterAbsentFromTheSystemRoster proves the
// tenant home scheduler judges departure by the system shard's roster: a
// node that left the cluster is still listed as a member of the home group
// it was a voter of, so a roster built from every local shard's membership
// would keep it known forever. With the system roster it is removed.
func TestReconcileTenantHomes_ShedsAVoterAbsentFromTheSystemRoster(t *testing.T) {
	svc, store, topo := homeOnTopology(t, healthyNodes("n1", "n2", "n3"))
	host := withHomeView(t, svc, store, true)
	topo.nodes = healthyNodes("n1", "n2", "n3", "n4")
	if err := svc.ReconcileTenantHomes(context.Background()); err != nil {
		t.Fatalf("ReconcileTenantHomes (grow): %v", err)
	}
	home := loadHome(t, store)
	if homeReplicaOn(t, store, "n4:1") == nil {
		t.Fatalf("home spans %d nodes after the grow, want one on n4", len(home.Replicas))
	}
	host.members = membersOf(home)

	// n4 leaves: the system shard's roster drops it, but the home group's
	// membership, as this node hosts it, still lists it.
	const systemShard = 1
	homeMembers := make(map[uint64]string, len(home.Replicas))
	for _, r := range home.Replicas {
		homeMembers[r.ReplicaID] = r.RaftAddr
	}
	raftHost := &mockHost{getNodeHostInfoFn: func(quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
		return &quicraft.NodeHostInfo{
			RaftAddress: "n1:1",
			ShardInfoList: []quicraft.ShardInfo{
				{ShardID: systemShard, Nodes: map[uint64]string{1: "n1:1", 2: "n2:1", 3: "n3:1"}},
				{ShardID: home.ShardID, Nodes: homeMembers},
			},
		}
	}}
	sched, err := placement.NewScheduler(NewHostTopologyProvider(raftHost, nil, WithRosterShard(systemShard)))
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}
	svc.placementScheduler = sched

	if err := svc.ReconcileTenantHomes(context.Background()); err != nil {
		t.Fatalf("ReconcileTenantHomes (n4 left): %v", err)
	}
	if homeReplicaOn(t, store, "n4:1") != nil {
		t.Fatal("home kept its replica on n4, absent from the system roster")
	}
	if len(host.removeCalls) != 1 {
		t.Fatalf("RemoveFollowerNode calls = %d, want 1", len(host.removeCalls))
	}
}

// homeReplicaOn returns the home group's replica on addr, or nil.
func homeReplicaOn(t *testing.T, store *placement.Store, addr string) *placement.Replica {
	t.Helper()
	home, err := store.Get(context.Background(), "rt", database.TenantHomeID)
	if err != nil {
		t.Fatalf("Get home: %v", err)
	}
	for i := range home.Replicas {
		if home.Replicas[i].RaftAddr == addr {
			return &home.Replicas[i]
		}
	}
	return nil
}

// A node that joins after the tenant's home group was placed joins the home
// group observer-first when the home is next ensured, so an isolated
// tenant's serving set covers it (engineering.md, home-group membership).
func TestProvisionTenantHome_GrowsOntoLateJoiningNode(t *testing.T) {
	svc, store, topo := homeOnTopology(t, healthyNodes("n1", "n2", "n3"))
	if homeReplicaOn(t, store, "n4:1") != nil {
		t.Fatal("home group already on n4 before it joined")
	}
	topo.nodes = healthyNodes("n1", "n2", "n3", "n4")

	if _, err := svc.ProvisionTenantHome(context.Background(), "rt"); err != nil {
		t.Fatalf("ProvisionTenantHome after join: %v", err)
	}
	r := homeReplicaOn(t, store, "n4:1")
	if r == nil {
		t.Fatal("home group did not grow onto the late-joining node n4")
	}
	if r.State != placement.ReplicaStateObserver {
		t.Fatalf("n4 joined the home group as %v, want an observer first", r.State)
	}
}

// A node that replaces an original home node joins the home group too.
func TestGrowTenantHome_CoversReplacementNode(t *testing.T) {
	svc, store, topo := homeOnTopology(t, healthyNodes("n1", "n2", "n3"))
	topo.nodes = healthyNodes("n1", "n2", "n5")

	if _, err := svc.GrowTenantHome(context.Background(), "rt"); err != nil {
		t.Fatalf("GrowTenantHome: %v", err)
	}
	if homeReplicaOn(t, store, "n5:1") == nil {
		t.Fatal("home group did not grow onto the replacement node n5")
	}
	if err := svc.ReconcileTenantHomes(context.Background()); err != nil {
		t.Fatalf("ReconcileTenantHomes with nothing to grow: %v", err)
	}
}

// rendezvousPlacementKV holds the first parties reads of key, each made,
// until all of them have arrived: every one of that many concurrent creates
// reads the entry as absent before any of them writes it.
type rendezvousPlacementKV struct {
	*memPlacementKV
	key string

	mu      sync.Mutex
	parties int
	arrived chan struct{}
}

func newRendezvousPlacementKV(key string, parties int) *rendezvousPlacementKV {
	return &rendezvousPlacementKV{memPlacementKV: newMemPlacementKV(), key: key, parties: parties, arrived: make(chan struct{})}
}

func (r *rendezvousPlacementKV) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := r.memPlacementKV.Get(ctx, key)
	if key != r.key {
		return v, err
	}
	r.mu.Lock()
	held := r.parties > 0
	if held {
		r.parties--
		if r.parties == 0 {
			close(r.arrived)
		}
	}
	r.mu.Unlock()
	if held {
		select {
		case <-r.arrived:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return v, err
}

// TestProvisionTenantHome_ConcurrentCallsSettleOnOnePlacement races two
// provisions of one tenant's home group (bootstrap.ProvisionTenant is not
// exclusive), each reading the placement as absent before either writes:
// every caller must get the one placement the table keeps.
func TestProvisionTenantHome_ConcurrentCallsSettleOnOnePlacement(t *testing.T) {
	const callers = 2
	kv := newRendezvousPlacementKV(placement.PlacementPrefix+"rt/"+database.TenantHomeName, callers)
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 100}}
	svcI, store := newPlacementDatabaseServiceOn(t, raftTenant(t), f, healthyNodes("n1", "n2", "n3"), kv)
	svc := svcI.(*DatabaseService)
	ctx := context.Background()

	plans := make([]*placement.Placement, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() { plans[i], errs[i] = svc.ProvisionTenantHome(ctx, "rt") })
	}
	wg.Wait()

	stored, err := store.Get(ctx, "rt", database.TenantHomeName)
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: ProvisionTenantHome: %v", i, errs[i])
		}
		if plans[i].ShardID != stored.ShardID {
			t.Fatalf("caller %d got home shard %d, the table keeps %d: a concurrent provision overwrote the placement", i, plans[i].ShardID, stored.ShardID)
		}
	}
}

// TestProvisionTenantHome_RefusesATableWithoutAtomicCreate proves a placement
// table that cannot create an entry only if it is absent places nothing,
// rather than falling back to a write that could overwrite another caller's.
func TestProvisionTenantHome_RefusesATableWithoutAtomicCreate(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 100}}
	svcI, store := newPlacementDatabaseServiceOn(t, raftTenant(t), f, healthyNodes("n1"), putOnlyPlacementKV{newMemPlacementKV()})
	ctx := context.Background()

	_, err := svcI.(*DatabaseService).ProvisionTenantHome(ctx, "rt")
	var unsupported *placement.AtomicWriteUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want *placement.AtomicWriteUnsupportedError", err)
	}
	if _, err := store.Get(ctx, "rt", database.TenantHomeName); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement Get = %v, want ErrPlacementNotFound", err)
	}
}

// putOnlyPlacementKV is a placement.KV with none of the guarded-write
// capabilities.
type putOnlyPlacementKV struct{ m *memPlacementKV }

func (p putOnlyPlacementKV) Put(ctx context.Context, key string, value []byte) error {
	return p.m.Put(ctx, key, value)
}

func (p putOnlyPlacementKV) Get(ctx context.Context, key string) ([]byte, error) {
	return p.m.Get(ctx, key)
}

func (p putOnlyPlacementKV) Delete(ctx context.Context, key string) error {
	return p.m.Delete(ctx, key)
}

func (p putOnlyPlacementKV) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	return p.m.Scan(ctx, prefix, fn)
}

func TestProvisionTenantHome_UnplaceableWithoutPlacement(t *testing.T) {
	svc := &DatabaseService{}
	_, err := svc.ProvisionTenantHome(context.Background(), "rt")
	var want *TenantHomeUnplaceableError
	if !errors.As(err, &want) || want.TenantID != "rt" {
		t.Fatalf("err = %v, want *TenantHomeUnplaceableError for rt", err)
	}
	if want.Error() != "service: tenant rt home group cannot be placed: placement is not configured on this node" {
		t.Fatalf("Error() = %q", want.Error())
	}
}

func TestProvisionTenantHome_UnknownTenantPlacesNothing(t *testing.T) {
	svc, store := newHomeDatabaseService(t, healthyNodes("n1"))
	ctx := context.Background()

	if _, err := svc.ProvisionTenantHome(ctx, "ghost"); !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("err = %v, want ErrTenantNotFound", err)
	}
	if _, err := store.Get(ctx, "ghost", database.TenantHomeName); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement Get = %v, want ErrPlacementNotFound (nothing may be placed for an unknown tenant)", err)
	}
}

func TestProvisionTenantHome_NoHealthyNodesFails(t *testing.T) {
	svc, store := newHomeDatabaseService(t, nil)
	ctx := context.Background()

	if _, err := svc.ProvisionTenantHome(ctx, "rt"); err == nil {
		t.Fatal("ProvisionTenantHome with no nodes succeeded, want a placement error")
	}
	if _, err := store.Get(ctx, "rt", database.TenantHomeName); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement Get = %v, want ErrPlacementNotFound", err)
	}
}

func TestRetireTenantHome_DeletesThePlacement(t *testing.T) {
	svc, store := newHomeDatabaseService(t, healthyNodes("n1", "n2"))
	ctx := context.Background()

	if _, err := svc.ProvisionTenantHome(ctx, "rt"); err != nil {
		t.Fatalf("ProvisionTenantHome: %v", err)
	}
	if err := svc.RetireTenantHome(ctx, "rt"); err != nil {
		t.Fatalf("RetireTenantHome: %v", err)
	}
	if _, err := store.Get(ctx, "rt", database.TenantHomeName); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement Get = %v, want ErrPlacementNotFound after retiring the home", err)
	}
	// A tenant with no home group is the success case, so a retry converges.
	if err := svc.RetireTenantHome(ctx, "rt"); err != nil {
		t.Fatalf("second RetireTenantHome: %v", err)
	}
}

func TestRetireTenantHome_NoPlacementTableIsNoOp(t *testing.T) {
	svc := &DatabaseService{}
	if err := svc.RetireTenantHome(context.Background(), "rt"); err != nil {
		t.Fatalf("RetireTenantHome without a placement table = %v, want nil", err)
	}
	var _ tenant.HomeRetirer = svc
}

// The home group's placement is not a database a tenant can scale: the
// request is refused as for a name with no dedicated-raft placement, and the
// group keeps the replication factor it was placed with.
func TestScaleForTenant_RefusesTenantHome(t *testing.T) {
	svc, store := newHomeDatabaseService(t, healthyNodes("n1", "n2", "n3"))
	ctx := context.Background()
	if _, err := svc.ProvisionTenantHome(ctx, "rt"); err != nil {
		t.Fatalf("ProvisionTenantHome: %v", err)
	}
	tctx := tenant.WithTenantID(ctx, "rt")
	for _, name := range []string{database.TenantHomeName, "__HOME", "does-not-exist"} {
		_, err := svc.ScaleForTenant(tctx, name, 1)
		var nd *NotDedicatedRaftDatabaseError
		if !errors.As(err, &nd) || nd.Database != name {
			t.Fatalf("ScaleForTenant(%q) = %v (%T), want *NotDedicatedRaftDatabaseError", name, err, err)
		}
	}
	stored, err := store.Get(ctx, "rt", database.TenantHomeName)
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	if stored.RF != 3 {
		t.Fatalf("home group RF = %d after a refused scale, want 3", stored.RF)
	}
}

// Dropping the home group fails exactly as dropping a database that does not
// exist, and leaves its placement in place: only RetireTenantHome removes it.
func TestDropForTenant_RefusesTenantHome(t *testing.T) {
	svc, store := newHomeDatabaseService(t, healthyNodes("n1", "n2", "n3"))
	ctx := context.Background()
	if _, err := svc.ProvisionTenantHome(ctx, "rt"); err != nil {
		t.Fatalf("ProvisionTenantHome: %v", err)
	}
	tctx := tenant.WithTenantID(ctx, "rt")
	unknown := svc.DropForTenant(tctx, "does-not-exist")
	if unknown == nil {
		t.Fatal("dropping an unknown database must fail")
	}
	for _, name := range []string{database.TenantHomeName, "__HOME"} {
		err := svc.DropForTenant(tctx, name)
		if err == nil || reflect.TypeOf(err) != reflect.TypeOf(unknown) {
			t.Fatalf("DropForTenant(%q) = %v (%T), want the unknown-database error %T", name, err, err, unknown)
		}
	}
	stored, err := store.Get(ctx, "rt", database.TenantHomeName)
	if err != nil {
		t.Fatalf("the home group's placement must survive a refused drop: %v", err)
	}
	if stored.Dropping {
		t.Fatal("a refused drop must not fence the home group")
	}
}

// homeViewHost is a RaftHost that also reports local leadership and the
// shard's committed members, as the lifecycle's replica host does.
type homeViewHost struct {
	*fakeRaftHost
	leader  bool
	members map[uint64]string
	removed map[uint64]struct{}
}

func (h *homeViewHost) IsLocalShardLeader(uint64) (bool, error) { return h.leader, nil }

func (h *homeViewHost) ShardMembers(context.Context, uint64) (map[uint64]string, error) {
	return h.members, nil
}

func (h *homeViewHost) RemovedReplicas(context.Context, uint64) (map[uint64]struct{}, error) {
	return h.removed, nil
}

// membersOf returns every replica of the placement as a committed member.
func membersOf(p *placement.Placement) map[uint64]string {
	out := make(map[uint64]string, len(p.Replicas))
	for _, r := range p.Replicas {
		out[r.ReplicaID] = r.RaftAddr
	}
	return out
}

func loadHome(t *testing.T, store *placement.Store) *placement.Placement {
	t.Helper()
	home, err := store.Get(context.Background(), "rt", database.TenantHomeID)
	if err != nil {
		t.Fatalf("Get home: %v", err)
	}
	return home
}

func withHomeView(t *testing.T, svc *DatabaseService, store *placement.Store, leader bool) *homeViewHost {
	t.Helper()
	h := &homeViewHost{
		fakeRaftHost: &fakeRaftHost{leaderID: 1, leaderOK: true},
		leader:       leader,
		members:      membersOf(loadHome(t, store)),
	}
	svc.SetRaftHost(h)
	return h
}

// A follower of the home group that runs the join reconcile changes no
// placement: Raft would refuse its AddObserver, leaving a recorded replica
// that is never admitted.
func TestGrowTenantHome_FollowerLeavesPlacementToTheLeader(t *testing.T) {
	svc, store, topo := homeOnTopology(t, healthyNodes("n1", "n2", "n3"))
	host := withHomeView(t, svc, store, false)
	topo.nodes = healthyNodes("n1", "n2", "n3", "n4")

	if _, err := svc.GrowTenantHome(context.Background(), "rt"); err != nil {
		t.Fatalf("GrowTenantHome: %v", err)
	}
	if homeReplicaOn(t, store, "n4:1") != nil {
		t.Fatal("a follower recorded a home replica on n4")
	}
	if len(host.observerCalls) != 0 {
		t.Fatalf("a follower requested %d observer admissions", len(host.observerCalls))
	}
}

// The leader re-requests admission of a placed replica the group's
// committed membership lacks (its earlier AddObserver failed).
func TestGrowTenantHome_LeaderReadmitsAnUnadmittedReplica(t *testing.T) {
	svc, store, _ := homeOnTopology(t, healthyNodes("n1", "n2", "n3"))
	host := withHomeView(t, svc, store, true)
	missing := loadHome(t, store).Replicas[2]
	delete(host.members, missing.ReplicaID)

	if _, err := svc.GrowTenantHome(context.Background(), "rt"); err != nil {
		t.Fatalf("GrowTenantHome: %v", err)
	}
	if len(host.observerCalls) != 1 || host.observerCalls[0].replicaID != missing.ReplicaID ||
		host.observerCalls[0].raftAddr != missing.RaftAddr {
		t.Fatalf("observer admissions = %+v, want one for replica %d", host.observerCalls, missing.ReplicaID)
	}
}

// TestGrowTenantHome_FinishesACommittedRemovalInsteadOfReadmitting covers a
// removal Raft committed (RemoveFollowerNode) whose placement update then
// failed: the placed replica is in the group's removed set, so the leader
// drops it from placement and never asks Raft to readmit the retired ID.
func TestGrowTenantHome_FinishesACommittedRemovalInsteadOfReadmitting(t *testing.T) {
	svc, store, _ := homeOnTopology(t, healthyNodes("n1", "n2", "n3"))
	host := withHomeView(t, svc, store, true)
	retired := loadHome(t, store).Replicas[2]
	delete(host.members, retired.ReplicaID)
	host.removed = map[uint64]struct{}{retired.ReplicaID: {}}

	if _, err := svc.GrowTenantHome(context.Background(), "rt"); err != nil {
		t.Fatalf("GrowTenantHome: %v", err)
	}
	for _, c := range host.observerCalls {
		if c.replicaID == retired.ReplicaID {
			t.Fatalf("observer admissions = %+v: the retired replica %d was readmitted", host.observerCalls, retired.ReplicaID)
		}
	}
	for _, r := range loadHome(t, store).Replicas {
		if r.ReplicaID == retired.ReplicaID {
			t.Fatalf("placement still holds the retired replica %d after reconcile", retired.ReplicaID)
		}
	}
}

// errReplicaRemoved is the double's stand-in for go-quicraft's ErrNodeRemoved:
// Raft records a removed replica ID permanently and refuses to admit it again.
var errReplicaRemoved = errors.New("replica ID was removed from the shard")

// AddObserver refuses a removed ID, as the pinned go-quicraft does.
func (h *homeViewHost) AddObserver(ctx context.Context, shardID, replicaID uint64, raftAddr string) error {
	if _, gone := h.removed[replicaID]; gone {
		return errReplicaRemoved
	}
	return h.fakeRaftHost.AddObserver(ctx, shardID, replicaID, raftAddr)
}

// casFailingPlacementKV fails every compare-and-swap while failCAS is set.
type casFailingPlacementKV struct {
	*memPlacementKV
	failCAS bool
}

func (k *casFailingPlacementKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if k.failCAS {
		return false, errors.New("placement write refused")
	}
	return k.memPlacementKV.CompareAndSwap(ctx, key, expected, next)
}

// TestReconcileTenantHomes_FinishesARemovalWhosePlacementWriteFailed drives
// the real removal path: shedding a departed home voter commits
// RemoveFollowerNode, then Store.RemoveReplica fails. The next reconcile
// must drop the retired replica from placement and never ask Raft to admit
// the removed ID again.
func TestReconcileTenantHomes_FinishesARemovalWhosePlacementWriteFailed(t *testing.T) {
	kv := &casFailingPlacementKV{memPlacementKV: newMemPlacementKV()}
	nodes := healthyNodes("n1", "n2", "n3")
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 100}}
	raw, store := newPlacementDatabaseServiceOn(t, raftTenant(t), f, nodes, kv)
	svc := raw.(*DatabaseService)
	topo := &staticTopology{nodes: nodes}
	sched, err := placement.NewScheduler(topo)
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}
	svc.placementScheduler = sched
	if _, err := svc.ProvisionTenantHome(context.Background(), "rt"); err != nil {
		t.Fatalf("ProvisionTenantHome: %v", err)
	}
	host := withHomeView(t, svc, store, true)
	topo.nodes = healthyNodes("n1", "n2", "n3", "n4")
	if err := svc.ReconcileTenantHomes(context.Background()); err != nil {
		t.Fatalf("ReconcileTenantHomes (grow): %v", err)
	}
	host.members = membersOf(loadHome(t, store))
	retired := homeReplicaOn(t, store, "n4:1")
	if retired == nil {
		t.Fatal("home has no replica on n4 after the grow")
	}
	host.observerCalls = nil // the grow admitted n4; count only later admissions
	host.removed = map[uint64]struct{}{}
	host.onRemove = func() {
		// Raft commits the removal; the placement write after it fails.
		host.removed[retired.ReplicaID] = struct{}{}
		delete(host.members, retired.ReplicaID)
		kv.failCAS = true
	}

	topo.nodes = healthyNodes("n1", "n2", "n3")
	if err := svc.ReconcileTenantHomes(context.Background()); err == nil {
		t.Fatal("ReconcileTenantHomes (shed) succeeded although the placement write failed")
	}
	if homeReplicaOn(t, store, "n4:1") == nil {
		t.Fatal("placement dropped the retired replica although its write failed")
	}

	kv.failCAS = false
	host.onRemove = nil
	if err := svc.ReconcileTenantHomes(context.Background()); err != nil {
		t.Fatalf("ReconcileTenantHomes (after the failed write): %v", err)
	}
	if homeReplicaOn(t, store, "n4:1") != nil {
		t.Fatal("the committed removal's placement entry is still present")
	}
	for _, c := range host.observerCalls {
		if c.replicaID == retired.ReplicaID {
			t.Fatalf("observer admissions = %+v: the retired replica %d was readmitted", host.observerCalls, retired.ReplicaID)
		}
	}
}

// lockedTopology is a placement.Topology a test can change while a watch
// goroutine reads it.
type lockedTopology struct {
	mu    sync.Mutex
	nodes []placement.NodeLoad
}

func (l *lockedTopology) Nodes() []placement.NodeLoad {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.nodes
}

func (l *lockedTopology) set(nodes []placement.NodeLoad) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nodes = nodes
}

// tickUntil drives the watch one tick at a time until cond holds.
func tickUntil(t *testing.T, tick chan<- time.Time, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("roster watch never %s", what)
		}
		select {
		case tick <- time.Now():
		case <-time.After(time.Until(deadline)):
		}
	}
}

// TestWatchTenantHomes_FollowsAJoinWithoutDynamicMembership covers a node
// that joins the committed roster with no membership manager running
// (dynamic membership off, the default): the roster watch alone grows the
// tenant home onto it, and sheds it again once it leaves.
func TestWatchTenantHomes_FollowsAJoinWithoutDynamicMembership(t *testing.T) {
	svc, store := newHomeDatabaseService(t, healthyNodes("n1", "n2", "n3"))
	topo := &lockedTopology{nodes: healthyNodes("n1", "n2", "n3")}
	sched, err := placement.NewScheduler(topo)
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}
	svc.placementScheduler = sched
	if _, err := svc.ProvisionTenantHome(context.Background(), "rt"); err != nil {
		t.Fatalf("ProvisionTenantHome: %v", err)
	}
	withHomeView(t, svc, store, true)

	tick := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.WatchTenantHomes(ctx, tick)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	topo.set(healthyNodes("n1", "n2", "n3", "n4"))
	tickUntil(t, tick, "grew the home onto the joined node", func() bool {
		return homeReplicaOn(t, store, "n4:1") != nil
	})
	topo.set(healthyNodes("n1", "n2", "n3"))
	tickUntil(t, tick, "shed the departed node from the home", func() bool {
		return homeReplicaOn(t, store, "n4:1") == nil
	})
}

// The watch starts before the raft host is composed: with no host it
// leaves an existing home's placement alone, and once SetRaftHost runs it
// reconciles. Run under -race: SetRaftHost races the watch's reads.
func TestWatchTenantHomes_WaitsForItsRaftHost(t *testing.T) {
	svc, store := newHomeDatabaseService(t, healthyNodes("n1", "n2", "n3"))
	topo := &lockedTopology{nodes: healthyNodes("n1", "n2", "n3")}
	sched, err := placement.NewScheduler(topo)
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}
	svc.placementScheduler = sched
	if _, err := svc.ProvisionTenantHome(context.Background(), "rt"); err != nil {
		t.Fatalf("ProvisionTenantHome: %v", err)
	}
	svc.SetRaftHost(nil)
	before := loadHome(t, store)
	topo.set(healthyNodes("n1", "n2", "n3", "n4"))

	tick := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.WatchTenantHomes(ctx, tick)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// Each send is taken only once the watch finished the iteration
	// before it, so after these the watch has looked at the home with no
	// host several times.
	for range tenantHomeWatchResyncTicks + 2 {
		tick <- time.Now()
	}
	if after := loadHome(t, store); !reflect.DeepEqual(after.Replicas, before.Replicas) {
		t.Fatalf("home replicas changed with no raft host:\n got %+v\nwant %+v", after.Replicas, before.Replicas)
	}

	h := &homeViewHost{
		fakeRaftHost: &fakeRaftHost{leaderID: 1, leaderOK: true},
		leader:       true,
		members:      membersOf(before),
	}
	svc.SetRaftHost(h)
	tickUntil(t, tick, "grew the home onto the joined node once the host is set", func() bool {
		return homeReplicaOn(t, store, "n4:1") != nil
	})
}

// A home grown onto three more nodes sheds each as it retires, so the
// original three remain the whole group and keep quorum.
func TestReconcileTenantHomes_GrowThenShrinkShedsDepartedNodes(t *testing.T) {
	svc, store, topo := homeOnTopology(t, healthyNodes("n1", "n2", "n3"))
	host := withHomeView(t, svc, store, true)
	topo.nodes = healthyNodes("n1", "n2", "n3", "n4", "n5", "n6")
	if err := svc.ReconcileTenantHomes(context.Background()); err != nil {
		t.Fatalf("ReconcileTenantHomes (grow): %v", err)
	}
	if n := len(loadHome(t, store).Replicas); n != 6 {
		t.Fatalf("home spans %d nodes after the grow, want 6", n)
	}
	host.members = membersOf(loadHome(t, store))

	// The added nodes retire one at a time; each leave runs a reconcile.
	for _, left := range [][]string{
		{"n1", "n2", "n3", "n4", "n5"},
		{"n1", "n2", "n3", "n4"},
		{"n1", "n2", "n3"},
	} {
		topo.nodes = healthyNodes(left...)
		if err := svc.ReconcileTenantHomes(context.Background()); err != nil {
			t.Fatalf("ReconcileTenantHomes (shrink to %v): %v", left, err)
		}
	}
	home := loadHome(t, store)
	if len(home.Replicas) != 3 {
		t.Fatalf("home spans %d nodes after the shrink, want 3", len(home.Replicas))
	}
	for _, addr := range []string{"n1:1", "n2:1", "n3:1"} {
		if homeReplicaOn(t, store, addr) == nil {
			t.Fatalf("home lost its replica on %s", addr)
		}
	}
	if len(host.removeCalls) != 3 {
		t.Fatalf("RemoveFollowerNode calls = %d, want 3", len(host.removeCalls))
	}
}

// TestReconcileTenantHomes_RegrowNeverReusesARetiredReplicaID pins that a
// home group regrown after shedding a replica places the new replica under
// an ID the group has never used: Raft refuses a removed replica ID for
// good, so reusing the shed replica's ID would leave the node unadmitted.
func TestReconcileTenantHomes_RegrowNeverReusesARetiredReplicaID(t *testing.T) {
	svc, store, topo := homeOnTopology(t, healthyNodes("n1", "n2", "n3"))
	host := withHomeView(t, svc, store, true)
	topo.nodes = healthyNodes("n1", "n2", "n3", "n4")
	if err := svc.ReconcileTenantHomes(context.Background()); err != nil {
		t.Fatalf("ReconcileTenantHomes (grow): %v", err)
	}
	first := homeReplicaOn(t, store, "n4:1")
	if first == nil {
		t.Fatal("home has no replica on n4 after the grow")
	}
	retired := first.ReplicaID
	host.members = membersOf(loadHome(t, store))

	topo.nodes = healthyNodes("n1", "n2", "n3")
	if err := svc.ReconcileTenantHomes(context.Background()); err != nil {
		t.Fatalf("ReconcileTenantHomes (shrink): %v", err)
	}
	if homeReplicaOn(t, store, "n4:1") != nil {
		t.Fatal("home kept its replica on n4 after n4 left")
	}

	topo.nodes = healthyNodes("n1", "n2", "n3", "n4")
	if err := svc.ReconcileTenantHomes(context.Background()); err != nil {
		t.Fatalf("ReconcileTenantHomes (regrow): %v", err)
	}
	again := homeReplicaOn(t, store, "n4:1")
	if again == nil {
		t.Fatal("home has no replica on n4 after the regrow")
	}
	if again.ReplicaID <= retired {
		t.Fatalf("regrown replica ID = %d, want above the retired ID %d", again.ReplicaID, retired)
	}
}

// Departed replicas are not removed while the replicas on known nodes are
// not a majority: the removal could not commit.
func TestReconcileTenantHomes_KeepsDepartedReplicasWithoutQuorum(t *testing.T) {
	svc, store, topo := homeOnTopology(t, healthyNodes("n1", "n2", "n3"))
	host := withHomeView(t, svc, store, true)
	topo.nodes = healthyNodes("n1")

	if err := svc.ReconcileTenantHomes(context.Background()); err != nil {
		t.Fatalf("ReconcileTenantHomes: %v", err)
	}
	if n := len(loadHome(t, store).Replicas); n != 3 {
		t.Fatalf("home spans %d replicas, want all 3 kept", n)
	}
	if len(host.removeCalls) != 0 {
		t.Fatalf("RemoveFollowerNode calls = %d, want 0", len(host.removeCalls))
	}
}

// TestReconcileTenantHomes_KeepsDepartedReplicaAtTheReplicationFactor pins
// the replication-factor floor: a home at exactly the cluster size, one of
// whose nodes has departed while its replacement is unhealthy (so growth
// places nothing), keeps the departed replica rather than shrink below the
// factor, although the known replicas are a majority.
func TestReconcileTenantHomes_KeepsDepartedReplicaAtTheReplicationFactor(t *testing.T) {
	svc, store, topo := homeOnTopology(t, healthyNodes("n1", "n2", "n3"))
	host := withHomeView(t, svc, store, true)
	replacement := placement.NodeLoad{NodeID: "n4", RaftAddr: "n4:1", Healthy: false}
	topo.nodes = append(healthyNodes("n1", "n2"), replacement)

	if err := svc.ReconcileTenantHomes(context.Background()); err != nil {
		t.Fatalf("ReconcileTenantHomes: %v", err)
	}
	if homeReplicaOn(t, store, "n3:1") == nil {
		t.Fatal("home shed its replica on departed n3 below the replication factor")
	}
	if n := len(loadHome(t, store).Replicas); n != 3 {
		t.Fatalf("home spans %d replicas, want 3", n)
	}
	if len(host.removeCalls) != 0 {
		t.Fatalf("RemoveFollowerNode calls = %d, want 0", len(host.removeCalls))
	}
}
