package service

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
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
	svc.raftHost = h
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
