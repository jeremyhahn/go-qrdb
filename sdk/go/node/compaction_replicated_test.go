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
func compactionFloor(ctx context.Context, n *Node, key string) int64 {
	_, err := n.Services().MVCC.GetAtRevision(ctx, key, 1)
	var compacted *service.RevisionCompactedError
	if errors.As(err, &compacted) {
		return compacted.Compacted
	}
	return 0
}

// keepWriting puts a fresh value under key through n every few milliseconds,
// so the store's revision keeps moving, until the returned stop is called.
func keepWriting(t *testing.T, n *Node, key string) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ctx.Err() == nil; i++ {
			_ = n.Client().KV().Put(ctx, key, []byte(strconv.Itoa(i)))
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	return func() { cancel(); wg.Wait() }
}

// awaitFloor waits, for as long as ctx allows, for n's store to compact key's
// history and returns the first floor it observes.
func awaitFloor(t *testing.T, ctx context.Context, n *Node, key string) int64 {
	t.Helper()
	for {
		if floor := compactionFloor(ctx, n, key); floor > 0 {
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
func awaitSample(t *testing.T, ctx context.Context, n *Node, db string, at time.Time) int64 {
	t.Helper()
	store, ok := n.Services().MVCC.(service.CompactionStateStore)
	require.True(t, ok, "the node's store keeps no compaction table")
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
func sampleMostOfAPeriod(t *testing.T, ctx context.Context, n *Node, db string, clk *clocktest.Fake, period time.Duration) int64 {
	t.Helper()
	newest := awaitSample(t, ctx, n, db, clk.Now())
	for range 5 {
		clk.Advance(period / 8)
		newest = awaitSample(t, ctx, n, db, clk.Now())
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
		var gate *featuregate.NotActiveError
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
	key := tenant.ComposeNamespace(tenantid.System, "events") + "k"

	first, err := NewStandaloneNode(ctx, standaloneRestartConfig(dataDir), opts)
	require.NoError(t, err)
	_, err = first.Services().Database.CreateForTenant(tenant.WithTenantID(ctx, tenantid.System),
		database.DatabaseConfig{Name: "events", Backend: database.BackendPebble})
	require.NoError(t, err)
	period := 4 * time.Second
	setPolicy(t, ctx, first, tenantid.System, "events", &compaction.Policy{Mode: compaction.Period, RetainPeriod: period.String()})

	stop := keepWriting(t, first, key)
	sampledByFirst := sampleMostOfAPeriod(t, ctx, first, "events", clk, period)
	stop()
	require.Zero(t, compactionFloor(ctx, first, key), "compacted before a full period was observed")
	require.NoError(t, first.Close())

	second, err := NewStandaloneNode(ctx, standaloneRestartConfig(dataDir), opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	stop = keepWriting(t, second, key)
	defer stop()

	// The first node's newest sample is now a period old. A node that kept
	// the samples compacts to it; one whose retention clock started over has
	// no sample that old and compacts nothing.
	clk.Advance(period)
	floor := awaitFloor(t, ctx, second, key)
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
func TestClusteredNodes_CompactionContinuesAfterLeaderFailover(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	// The seal on (strategy none), as a deployed node runs: a node booted
	// with the seal off builds no database catalog and serves no named
	// database at all.
	sealed := func(cfg *qrdbconfig.Config) {
		cfg.Seal.Enabled = true
		cfg.Seal.Strategy = "none"
	}
	clk := clocktest.NewAt(time.Now())
	nodes, err := bootFormationClusterWith(t, ca, replicas, func(self uint64) string {
		return peerIdentitySpecs(t, self, replicas)
	}, sealed, server.WithCompactionInterval(compactionTestInterval), server.WithCompactionClock(clk))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 4*clusterFormationBudget)
	defer cancel()

	leader := awaitAgreedLeader(t, nodes)
	a := nodes[leader-1]
	key := tenant.ComposeNamespace(tenantid.System, "events") + "k"
	require.NoError(t, putUntilCommitted(ctx, a.Client().KV(), key, []byte("first")))
	_, err = a.Services().Database.CreateForTenant(tenant.WithTenantID(ctx, tenantid.System),
		database.DatabaseConfig{Name: "events", Backend: database.BackendPebble})
	require.NoError(t, err)
	period := 6 * time.Second
	setPolicy(t, ctx, a, tenantid.System, "events", &compaction.Policy{Mode: compaction.Period, RetainPeriod: period.String()})

	stop := keepWriting(t, a, key)
	sampledByA := sampleMostOfAPeriod(t, ctx, a, "events", clk, period)
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
		if uint64(i+1) != leader {
			survivor = n
			break
		}
	}
	stop = keepWriting(t, survivor, key)
	defer stop()

	// A's newest sample is now a period old. A new leader that runs the
	// policy from A's samples compacts to it; one that started the period
	// over has no sample that old and compacts nothing.
	clk.Advance(period)
	floor := awaitFloor(t, ctx, survivor, key)
	require.Equalf(t, sampledByA, floor,
		"the new leader compacted to %d, not to the newest sample A's term recorded (%d): it did not continue from A's samples",
		floor, sampledByA)
}
