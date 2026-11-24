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
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A scheduled-compaction policy and its samples are replicated state in the
// database's own store -- never a node's catalog or a file in its data
// directory -- so the schedule survives a node going down and a restart (and,
// in pkg/store, travels in the snapshots backups are made of). Each test below
// drives only the public surface
// and the node's own services, so it runs unchanged against a build that kept
// the policy node-local and fails there.

// compactionTestInterval runs the scheduler often enough to watch it.
const compactionTestInterval = 20 * time.Millisecond

// compactionFloor reports the compaction floor n's store holds for key: the
// floor a read of key at revision 1 is refused with, or 0 when nothing has
// been compacted.
func compactionFloor(ctx context.Context, target compactionTarget, key string) int64 {
	_, err := target.mvcc.GetAtRevision(ctx, key, 1)
	var compacted *service.RevisionCompactedError
	if errors.As(err, &compacted) {
		return compacted.Compacted
	}
	return 0
}

// compactionTarget is the store a compaction test watches: mvcc is the store
// the scheduler compacts and samples, and put writes one key through the
// surface that lands in that store.
type compactionTarget struct {
	mvcc service.MVCCKVServicer
	put  func(ctx context.Context, key string, value []byte) error
}

// sharedTarget watches n's shared store, written through n's client: where a
// clustered node keeps a named database, in the database's namespace.
func sharedTarget(n *Node) compactionTarget {
	return compactionTarget{
		mvcc: n.Services().MVCC,
		put: func(ctx context.Context, key string, value []byte) error {
			return n.Client().KV().Put(ctx, key, value)
		},
	}
}

// materializedTarget watches the system database name that n's standalone
// manager opened in its own store: the database's whole keyspace is its own
// host, so its keys carry no namespace, and the scheduler compacts and
// samples that host rather than the shared store.
func materializedTarget(t *testing.T, ctx context.Context, n *Node, name string) compactionTarget {
	t.Helper()
	db, err := n.dbManager.Open(ctx, tenantid.System, name)
	require.NoError(t, err)
	ms, ok := db.Backend().(interface {
		Host() service.Host
		ShardID() uint64
	})
	require.Truef(t, ok, "database %s is not materialized in its own store", name)
	mvcc := service.NewMVCCKVService(ms.Host(), ms.ShardID(), service.WithDedicatedKeyspace())
	return compactionTarget{mvcc: mvcc, put: mvcc.Put}
}

// raftTarget is the store of a raft database's own group as replica n sees
// it: the bundle the compaction scheduler runs the database's policy on,
// gated on the group's leadership. Its keys carry no namespace. It waits
// until n has started its replica of the group.
func raftTarget(t *testing.T, ctx context.Context, n *Node, tenantID, name string) compactionTarget {
	t.Helper()
	require.NotNil(t, n.raftBackend, "the node runs no raft backend")
	for {
		svcs, err := n.raftBackend.Services(ctx, tenantID, name)
		if err == nil && svcs != nil && svcs.MVCC != nil {
			return compactionTarget{mvcc: svcs.MVCC, put: svcs.MVCC.Put}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no replica of raft database %s on this node (last error: %v): %v", name, err, ctx.Err())
		case <-time.After(clusterPollInterval):
		}
	}
}

// awaitGroupLeader returns the index in nodes of the replica that leads the
// raft database name's own group.
func awaitGroupLeader(t *testing.T, ctx context.Context, nodes []*Node, tenantID, name string) int {
	t.Helper()
	targets := make([]compactionTarget, len(nodes))
	for i, n := range nodes {
		targets[i] = raftTarget(t, ctx, n, tenantID, name)
	}
	for {
		for i, target := range targets {
			if l, ok := target.mvcc.(service.ShardLeader); ok && l.LeadsShard() {
				return i
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no replica leads raft database %s: %v", name, ctx.Err())
		case <-time.After(clusterPollInterval):
		}
	}
}

// putUntilCommittedMVCC writes key through target until a write commits: a
// group's first leader can refuse a write before its term's first entry
// applies. It returns the last error when ctx ends first.
func putUntilCommittedMVCC(ctx context.Context, target compactionTarget, key string, value []byte) error {
	for {
		err := target.put(ctx, key, value)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), err)
		case <-time.After(clusterPollInterval):
		}
	}
}

// keepWriting puts a fresh value under key through n every few milliseconds,
// so the store's revision keeps moving, until the returned stop is called.
func keepWriting(t *testing.T, target compactionTarget, key string) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ctx.Err() == nil; i++ {
			_ = target.put(ctx, key, []byte(strconv.Itoa(i)))
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); wg.Wait() }) }
	// Registered after the nodes' own cleanups, so it runs before them
	// (LIFO): a test that fails before it calls stop never leaves the
	// writer running against a closed node.
	t.Cleanup(stop)
	return stop
}

// awaitFloor waits, for as long as ctx allows, for n's store to compact key's
// history and returns the first floor it observes.
func awaitFloor(t *testing.T, ctx context.Context, target compactionTarget, key string) int64 {
	t.Helper()
	for {
		if floor := compactionFloor(ctx, target, key); floor > 0 {
			return floor
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no compaction of %q: %v", key, ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// awaitSample waits until db's period policy holds a sample taken at at, read
// through n, and returns the revision it sampled. The scheduler stamps its
// samples with the compaction clock, so a caller moves that clock to at and
// this waits for the pass that records the sample the move made due.
func awaitSample(t *testing.T, ctx context.Context, target compactionTarget, db string, at time.Time) int64 {
	t.Helper()
	store, ok := target.mvcc.(service.CompactionStateStore)
	require.True(t, ok, "the watched store keeps no compaction table")
	for {
		states, err := store.CompactionStates(ctx)
		for _, state := range states {
			if state == nil || state.Database != db {
				continue
			}
			for _, s := range state.Samples {
				if s.At == at.UnixNano() {
					return s.Revision
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no sample of %s taken at %s (last read: %v): %v", db, at, err, ctx.Err())
		case <-time.After(clusterPollInterval):
		}
	}
}

// sampleMostOfAPeriod has n's scheduler sample db across five eighths of
// period on clk -- each step moves the clock an eighth and waits for the
// sample that makes due -- and returns the revision of the newest sample. No
// sample is a whole period old, so nothing is compacted yet, and with clk
// standing still no further sample falls due.
func sampleMostOfAPeriod(t *testing.T, ctx context.Context, target compactionTarget, db string, clk *clocktest.Fake, period time.Duration) int64 {
	t.Helper()
	newest := awaitSample(t, ctx, target, db, clk.Now())
	for range 5 {
		clk.Advance(period / 8)
		newest = awaitSample(t, ctx, target, db, clk.Now())
	}
	return newest
}

// setPolicy sets db's policy as its owner, retrying while the cluster
// feature that carries it is not yet activated on the group.
func setPolicy(t *testing.T, ctx context.Context, n *Node, owner, db string, policy *compaction.Policy) {
	t.Helper()
	deadline := time.Now().Add(clusterFormationBudget)
	for {
		_, err := n.Services().Database.UpdateCompactionForTenant(tenant.WithTenantID(ctx, owner), db, policy)
		if err == nil {
			return
		}
		var gate *feature.NotActiveError
		if !errors.As(err, &gate) || time.Now().After(deadline) {
			t.Fatalf("UpdateCompactionForTenant(%s): %v", db, err)
		}
		time.Sleep(clusterPollInterval)
	}
}

// TestStandaloneNode_CompactionScheduleSurvivesRestart: a standalone node's
// period policy keeps its retention clock across a restart. Samples taken
// before the restart are in the database's store, so once the newest of them
// is a period old the restarted node compacts to it, instead of starting the
// period over from its own samples. Both nodes run on one compaction clock the
// test moves, so no wall-clock window decides the outcome.
func TestStandaloneNode_CompactionScheduleSurvivesRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dataDir := t.TempDir()
	clk := clocktest.NewAt(time.Now())
	opts := []server.ServerOption{server.WithCompactionInterval(compactionTestInterval), server.WithCompactionClock(clk)}
	// A standalone node opens a named database in its own store, so the
	// test writes, samples and reads the floor there: its keys carry no
	// namespace.
	const key = "k"

	first, err := NewStandaloneNode(ctx, standaloneRestartConfig(dataDir), opts)
	require.NoError(t, err)
	_, err = first.Services().Database.CreateForTenant(tenant.WithTenantID(ctx, tenantid.System),
		database.DatabaseConfig{Name: "events", Backend: database.BackendPebble})
	require.NoError(t, err)
	period := 4 * time.Second
	setPolicy(t, ctx, first, tenantid.System, "events", &compaction.Policy{Mode: compaction.Period, RetainPeriod: period.String()})

	firstEvents := materializedTarget(t, ctx, first, "events")
	stop := keepWriting(t, firstEvents, key)
	sampledByFirst := sampleMostOfAPeriod(t, ctx, firstEvents, "events", clk, period)
	stop()
	require.Zero(t, compactionFloor(ctx, firstEvents, key), "compacted before a full period was observed")
	require.NoError(t, first.Close())

	second, err := NewStandaloneNode(ctx, standaloneRestartConfig(dataDir), opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	secondEvents := materializedTarget(t, ctx, second, "events")
	stop = keepWriting(t, secondEvents, key)
	defer stop()

	// The first node's newest sample is now a period old. A node that kept
	// the samples compacts to it; one whose retention clock started over has
	// no sample that old and compacts nothing.
	clk.Advance(period)
	floor := awaitFloor(t, ctx, secondEvents, key)
	require.Equalf(t, sampledByFirst, floor,
		"the restarted node compacted to %d, not to the first node's newest sample %d", floor, sampledByFirst)
}

// TestClusteredNodes_CompactionContinuesAfterLeaderFailover: a policy set
// through node A -- the shared shard's leader -- is not A's: after A goes
// down, the new leader runs it, from the samples A's term recorded, so the
// first compaction it makes is to the newest of them once that is a period
// old. The nodes share one compaction clock the test moves, so neither how
// long the leader change takes nor how long the new leader's first reads
// stall decides the outcome.
// raftDatabaseEntitlements lets a volume-isolated tenant own a raft
// database: volume storage on the default pool, plus the raft isolation
// mode its database's own group takes.
func raftDatabaseEntitlements() *quota.Entitlements {
	ent := volumeEntitlements()
	ent.AllowedIsolationModes = append(ent.AllowedIsolationModes, quota.IsolationModeRaft)
	ent.AllowDedicatedRaft = true
	return ent
}

func TestClusteredNodes_CompactionContinuesAfterLeaderFailover(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	// The seal on (strategy none), as a deployed node runs: a node booted
	// with the seal off builds no database catalog and serves no named
	// database at all.
	//
	// Tenant isolation on, over a volume pool: a node serves a raft
	// database's own group only through its raft backend, which isolation
	// builds.
	sealed := func(cfg *qrdbconfig.Config) {
		cfg.Seal.Enabled = true
		cfg.Seal.Strategy = "none"
		cfg.Storage.VolumePools = []qrdbconfig.VolumePoolConfig{{Name: "default", Path: t.TempDir()}}
	}
	clk := clocktest.NewAt(time.Now())
	nodes, err := bootFormationClusterNodeOpts(t, ca, replicas, func(self uint64) string {
		return peerIdentitySpecs(t, self, replicas)
	}, sealed, nil, []NodeOption{WithTenantIsolation(), WithDefaultTenantEntitlements(raftDatabaseEntitlements())}, server.WithCompactionInterval(compactionTestInterval), server.WithCompactionClock(clk))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 4*clusterFormationBudget)
	defer cancel()

	// "events" is a replicated raft database: its own group on all three
	// nodes, its policy run by that group's leader alone. A is that leader.
	// Its group takes a cluster-wide shard ID, which every node must agree
	// to first.
	// The tenant's barrier takes a per-node key.
	awaitFeatureActive(t, nodes, featuregate.ClusterShardIDs)
	awaitFeatureActive(t, nodes, featuregate.PerNodeBarrierKeys)
	// A raft database's group runs under its tenant's own barrier, so it
	// belongs to a volume-isolated tenant, not to the system tenant.
	creator := nodes[awaitAgreedLeader(t, nodes)-1]
	createDefaultPool(t, creator)
	const owner = "tenant-events"
	_, err = creator.Services().Tenant.CreateTenant(ctx, owner, owner,
		tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true))
	require.NoError(t, err)
	_, err = creator.Services().Database.CreateForTenant(tenant.WithTenantID(ctx, owner),
		database.DatabaseConfig{
			Name:     "events",
			Backend:  database.BackendRaft,
			Raft:     &database.DatabaseRaftConfig{ClusterMode: database.RaftDistributed},
			Replicas: 3,
		})
	require.NoError(t, err)
	leader := awaitGroupLeader(t, ctx, nodes, owner, "events")
	a := nodes[leader]
	// The database's own group holds only its keys: no namespace.
	const key = "k"
	aEvents := raftTarget(t, ctx, a, owner, "events")
	require.NoError(t, putUntilCommittedMVCC(ctx, aEvents, key, []byte("first")))
	period := 6 * time.Second
	setPolicy(t, ctx, a, owner, "events", &compaction.Policy{Mode: compaction.Period, RetainPeriod: period.String()})

	stop := keepWriting(t, aEvents, key)
	sampledByA := sampleMostOfAPeriod(t, ctx, aEvents, "events", clk, period)
	stop()
	// A goes down; another node takes the shard. The compaction clock stands
	// still meanwhile, so neither A's last passes nor the new leader's first
	// ones sample or compact, however long the leader change takes.
	//
	// Close deregisters A, which is a write, and reports it failing when the
	// cluster has no leader at that moment — which a loaded runner can produce
	// before A ever stops. A is down either way; any other error fails here.
	if err := a.Close(); err != nil {
		var notLeader *transport.NotLeaderError
		require.ErrorAsf(t, err, &notLeader, "closing A: %v", err)
	}

	var survivor *Node
	for i, n := range nodes {
		if i != leader {
			survivor = n
			break
		}
	}
	survivorEvents := raftTarget(t, ctx, survivor, owner, "events")
	stop = keepWriting(t, survivorEvents, key)
	defer stop()

	// A's newest sample is now a period old. A new leader that runs the
	// policy from A's samples compacts to it; one that started the period
	// over has no sample that old and compacts nothing.
	clk.Advance(period)
	floor := awaitFloor(t, ctx, survivorEvents, key)
	require.Equalf(t, sampledByA, floor,
		"the new leader compacted to %d, not to the newest sample A's term recorded (%d): it did not continue from A's samples",
		floor, sampledByA)
}
