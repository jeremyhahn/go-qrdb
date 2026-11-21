package service

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
)

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
