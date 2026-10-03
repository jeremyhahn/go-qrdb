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

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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

// awaitFloor waits for n's store to compact key's history and returns the
// first floor it observes.
func awaitFloor(t *testing.T, ctx context.Context, n *Node, key string, budget time.Duration) int64 {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if floor := compactionFloor(ctx, n, key); floor > 0 {
			return floor
		}
		if time.Now().After(deadline) {
			t.Fatalf("no compaction of %q within %s", key, budget)
		}
		time.Sleep(5 * time.Millisecond)
	}
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
// before the restart are in the database's store, so the restarted node
// compacts to one of them -- a revision no newer than the store held at the
// restart -- instead of starting the period over from its own samples.
func TestStandaloneNode_CompactionScheduleSurvivesRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dataDir := t.TempDir()
	opts := []server.ServerOption{server.WithCompactionInterval(compactionTestInterval)}
	key := tenant.ComposeNamespace(tenant.SystemTenantID, "events") + "k"

	first, err := NewStandaloneNode(ctx, standaloneRestartConfig(dataDir), opts)
	require.NoError(t, err)
	_, err = first.Services().Database.CreateForTenant(tenant.WithTenantID(ctx, tenant.SystemTenantID),
		database.DatabaseConfig{Name: "events", Engine: database.EngineKV, Backend: database.BackendPebble})
	require.NoError(t, err)
	period := 4 * time.Second
	setPolicy(t, ctx, first, tenant.SystemTenantID, "events", &compaction.Policy{Mode: compaction.Period, RetainPeriod: period.String()})

	stop := keepWriting(t, first, key)
	time.Sleep(period * 5 / 8) // the first node samples for most of a period
	stop()
	revAtRestart, err := first.Services().MVCC.CurrentRevision(ctx)
	require.NoError(t, err)
	require.Zero(t, compactionFloor(ctx, first, key), "compacted before a full period was observed")
	require.NoError(t, first.Close())

	second, err := NewStandaloneNode(ctx, standaloneRestartConfig(dataDir), opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	stop = keepWriting(t, second, key)
	defer stop()

	floor := awaitFloor(t, ctx, second, key, 3*period)
	require.LessOrEqualf(t, floor, revAtRestart,
		"the restarted node compacted to %d, a revision of its own after the restart (the store held %d at the restart): its retention clock started over",
		floor, revAtRestart)
}

// TestClusteredNodes_CompactionContinuesAfterLeaderFailover: a policy set
// through node A -- the shared shard's leader -- is not A's: after A goes
// down, the new leader runs it, from the samples A's term recorded, so the
// first compaction it makes is to a revision no newer than the store held
// when A went down.
func TestClusteredNodes_CompactionContinuesAfterLeaderFailover(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	// The seal on (strategy none), as a deployed node runs: a node booted
	// with the seal off builds no database catalog and serves no named
	// database at all.
	sealed := func(cfg *qrdbconfig.Config) {
		cfg.Seal.Enabled = true
		cfg.Seal.Strategy = "none"
	}
	nodes, err := bootFormationClusterWith(t, ca, replicas, func(self uint64) string {
		return peerIdentitySpecs(t, self, replicas)
	}, sealed, server.WithCompactionInterval(compactionTestInterval))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 4*clusterFormationBudget)
	defer cancel()

	leader := awaitAgreedLeader(t, nodes)
	a := nodes[leader-1]
	key := tenant.ComposeNamespace(tenant.SystemTenantID, "events") + "k"
	require.NoError(t, putUntilCommitted(ctx, a.Client().KV(), key, []byte("first")))
	_, err = a.Services().Database.CreateForTenant(tenant.WithTenantID(ctx, tenant.SystemTenantID),
		database.DatabaseConfig{Name: "events", Engine: database.EngineKV, Backend: database.BackendPebble})
	require.NoError(t, err)
	period := 6 * time.Second
	setPolicy(t, ctx, a, tenant.SystemTenantID, "events", &compaction.Policy{Mode: compaction.Period, RetainPeriod: period.String()})

	stop := keepWriting(t, a, key)
	time.Sleep(period * 5 / 8) // A samples for most of a period
	stop()
	revAtFailover, err := a.Services().MVCC.CurrentRevision(ctx)
	require.NoError(t, err)
	require.NoError(t, a.Close()) // A goes down; another node takes the shard

	var survivor *Node
	for i, n := range nodes {
		if uint64(i+1) != leader {
			survivor = n
			break
		}
	}
	stop = keepWriting(t, survivor, key)
	defer stop()

	floor := awaitFloor(t, ctx, survivor, key, 3*period)
	require.LessOrEqualf(t, floor, revAtFailover,
		"the new leader compacted to %d, a revision after A went down (the store held %d then): it did not continue from A's samples",
		floor, revAtFailover)
}
