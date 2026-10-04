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

package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// memKV is an in-memory placement.KV for factory placement-source tests.
type memKV struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemKV() *memKV { return &memKV{data: map[string][]byte{}} }

func (m *memKV) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[key] = cp
	return nil
}

func (m *memKV) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[key], nil
}

func (m *memKV) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

// CompareAndSwap implements placement.AtomicKV so every reconciler and
// factory test built on memKV exercises the same CAS-guarded write path
// production uses (pkg/server/lifecycle's kvStoreAdapter over authStore),
// rather than a weaker double that would silently let a stale write win. A
// nil expected requires the key to be currently absent (a create); an empty,
// non-nil expected is not treated specially -- it must match the stored
// value byte-for-byte like any other expected, mirroring authStore's own
// CompareAndSwap semantics.
func (m *memKV) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.data[key]
	if expected == nil {
		if ok {
			return false, nil
		}
	} else if !ok || !bytes.Equal(current, expected) {
		return false, nil
	}
	cp := make([]byte, len(next))
	copy(cp, next)
	m.data[key] = cp
	return true, nil
}

func (m *memKV) Scan(_ context.Context, prefix string, fn func(string, []byte) error) error {
	m.mu.Lock()
	snap := map[string][]byte{}
	for k, v := range m.data {
		if strings.HasPrefix(k, prefix) {
			snap[k] = v
		}
	}
	m.mu.Unlock()
	for k, v := range snap {
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

func newPlacementStore(t *testing.T) *placement.Store {
	t.Helper()
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	return store
}

// poolRootResolver resolves every tenant to /pool/<tenant>, standing in for
// seal.BarrierRegistry.LocalTenantDataRoot.
func poolRootResolver(_ context.Context, tenantID string) (string, error) {
	return "/pool/" + tenantID, nil
}

// TestPlacementInfoSource_LocalReplicaWins verifies the placement entry's shard
// ID wins over the fallback and the local replica's directory is THIS node's,
// resolved from the tenant's pool.
func TestPlacementInfoSource_LocalReplicaWins(t *testing.T) {
	store := newPlacementStore(t)
	ctx := context.Background()
	if err := store.Put(ctx, &placement.Placement{
		TenantID: "rt", Database: "orders", ShardID: 7777, RF: 2, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1"},
			{ReplicaID: 2, NodeID: "other:1", RaftAddr: "other:1"},
		},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	fallback := newFakeRaftInfoSource()
	fallback.add("rt", "orders", 1234, "/meta/root") // different shard/dir

	src := &placementRaftInfoSource{store: store, localNodeID: "local:1", fallback: fallback,
		timeout: placementResolveTimeout, localRootResolver: poolRootResolver}
	shardID, dataDir, ok := src.databaseInfo("rt", "orders", false)
	if !ok {
		t.Fatalf("databaseInfo not ok")
	}
	if shardID != 7777 {
		t.Errorf("shard = %d, want 7777 (from placement)", shardID)
	}
	if dataDir != "/pool/rt/databases/orders" {
		t.Errorf("dataDir = %q, want /pool/rt/databases/orders (this node's pool)", dataDir)
	}
}

// TestPlacementInfoSource_RefusesAReplicaPath verifies a placement record whose
// local replica names a path is refused with *PlacementDataRootError, never
// opened: the path names the disk of the node that wrote it.
func TestPlacementInfoSource_RefusesAReplicaPath(t *testing.T) {
	store := newPlacementStore(t)
	ctx := context.Background()
	if err := store.Put(ctx, &placement.Placement{
		TenantID: "rt", Database: "orders", ShardID: 7777, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1", DataRoot: "/another-node/rt/databases/orders"},
		},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	src := &placementRaftInfoSource{store: store, localNodeID: "local:1", fallback: newFakeRaftInfoSource(),
		timeout: placementResolveTimeout, localRootResolver: poolRootResolver}

	_, _, _, ok, err := src.databaseAndPlacementInfo("rt", "orders")
	var pe *PlacementDataRootError
	if ok || !errors.As(err, &pe) || pe.DataRoot != "/another-node/rt/databases/orders" {
		t.Fatalf("databaseAndPlacementInfo = ok %v, %v; want *PlacementDataRootError", ok, err)
	}
	if pe.Error() == "" {
		t.Fatal("empty message")
	}
	if _, _, ok := src.databaseInfo("rt", "orders", false); ok {
		t.Fatal("databaseInfo resolved a placement record that names a path")
	}
}

// TestPlacementInfoSource_NoEntryFallsBack verifies resolution falls back to the
// meta source when no placement entry exists (Phase-4b compatibility).
func TestPlacementInfoSource_NoEntryFallsBack(t *testing.T) {
	store := newPlacementStore(t)
	fallback := newFakeRaftInfoSource()
	fallback.add("rt", "orders", 1234, "/meta/root")

	src := &placementRaftInfoSource{store: store, localNodeID: "local:1", fallback: fallback, timeout: placementResolveTimeout}
	shardID, dataDir, ok := src.databaseInfo("rt", "orders", false)
	if !ok || shardID != 1234 {
		t.Fatalf("fallback resolution = (%d,%q,%v), want shard 1234", shardID, dataDir, ok)
	}
	if !strings.HasPrefix(dataDir, "/meta/root") {
		t.Errorf("dataDir = %q, want under /meta/root", dataDir)
	}
}

// TestPlacementInfoSource_NoLocalReplicaFallsBack verifies that a placement
// entry assigning no replica to the local node defers to the fallback so this
// stage never regresses single-node behavior.
func TestPlacementInfoSource_NoLocalReplicaFallsBack(t *testing.T) {
	store := newPlacementStore(t)
	ctx := context.Background()
	if err := store.Put(ctx, &placement.Placement{
		TenantID: "rt", Database: "orders", ShardID: 7777, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "remote:1", RaftAddr: "remote:1"},
		},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	fallback := newFakeRaftInfoSource()
	fallback.add("rt", "orders", 1234, "/meta/root")

	src := &placementRaftInfoSource{store: store, localNodeID: "local:1", fallback: fallback, timeout: placementResolveTimeout}
	shardID, _, ok := src.databaseInfo("rt", "orders", false)
	if !ok || shardID != 1234 {
		t.Fatalf("expected fallback shard 1234, got (%d,%v)", shardID, ok)
	}
}

// TestWithPlacementInfoSource_Wraps verifies the option installs the
// placement-backed source over the existing dbInfo.
func TestWithPlacementInfoSource_Wraps(t *testing.T) {
	store := newPlacementStore(t)
	fallback := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(nil, 1, "local:1", nil, fallback, volTestLogger(),
		WithPlacementInfoSource(store, "local:1"))
	if _, ok := f.dbInfo.(*placementRaftInfoSource); !ok {
		t.Fatalf("dbInfo = %T, want *placementRaftInfoSource", f.dbInfo)
	}

	// Empty localNodeID is a no-op: the fallback source remains in place.
	f2 := NewRaftBackendFactory(nil, 1, "local:1", nil, fallback, volTestLogger(),
		WithPlacementInfoSource(store, ""))
	if _, ok := f2.dbInfo.(*fakeRaftInfoSource); !ok {
		t.Fatalf("dbInfo = %T, want *fakeRaftInfoSource (no-op option)", f2.dbInfo)
	}
}

// TestPlacementInfoSource_EmptyDataRootResolvesLocally verifies that when a
// placement entry assigns the local node but records DataRoot="" (BUG 1 — the
// leader records empty for remote replicas), the resolver is called and the
// returned DataDir is {resolvedRoot}/databases/{db}.
func TestPlacementInfoSource_EmptyDataRootResolvesLocally(t *testing.T) {
	store := newPlacementStore(t)
	ctx := context.Background()
	if err := store.Put(ctx, &placement.Placement{
		TenantID: "follower", Database: "orders", ShardID: 9001, RF: 3,
		LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "leader:1", RaftAddr: "leader:1"},
			// local node recorded with empty DataRoot (as written by the leader).
			{ReplicaID: 2, NodeID: "local:1", RaftAddr: "local:1"},
			{ReplicaID: 3, NodeID: "other:1", RaftAddr: "other:1"},
		},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	const resolvedTenantRoot = "/pool/follower"
	resolver := func(_ context.Context, tenantID string) (string, error) {
		if tenantID == "follower" {
			return resolvedTenantRoot, nil
		}
		return "", placement.ErrEmptyTenant
	}

	fallback := newFakeRaftInfoSource()
	src := &placementRaftInfoSource{
		store:             store,
		localNodeID:       "local:1",
		fallback:          fallback,
		timeout:           placementResolveTimeout,
		localRootResolver: resolver,
	}

	shardID, dataDir, ok := src.databaseInfo("follower", "orders", false)
	if !ok {
		t.Fatal("databaseInfo = ok false; want true")
	}
	if shardID != 9001 {
		t.Errorf("shardID = %d, want 9001", shardID)
	}
	wantDir := "/pool/follower/databases/orders"
	if dataDir != wantDir {
		t.Errorf("dataDir = %q, want %q", dataDir, wantDir)
	}
}

// TestPlacementInfoSource_EmptyDataRootNoResolverFallsBack verifies that when
// DataRoot is empty and no resolver is wired, databaseInfo falls back to the
// meta source rather than returning an empty path.
func TestPlacementInfoSource_EmptyDataRootNoResolverFallsBack(t *testing.T) {
	store := newPlacementStore(t)
	ctx := context.Background()
	if err := store.Put(ctx, &placement.Placement{
		TenantID: "follower", Database: "orders", ShardID: 9001, RF: 2,
		LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "leader:1", RaftAddr: "leader:1"},
			{ReplicaID: 2, NodeID: "local:1", RaftAddr: "local:1"},
		},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// fakeRaftInfoSource.add stores filepath.Join(dataRoot, "databases", db)
	// as the dataDir, mirroring the real manager which returns info.DataDir.
	const fallbackDataRoot = "/meta/follower"
	fallback := newFakeRaftInfoSource()
	fallback.add("follower", "orders", 500, fallbackDataRoot)

	src := &placementRaftInfoSource{
		store:             store,
		localNodeID:       "local:1",
		fallback:          fallback,
		timeout:           placementResolveTimeout,
		localRootResolver: nil, // no resolver
	}

	shardID, dataDir, ok := src.databaseInfo("follower", "orders", false)
	if !ok {
		t.Fatal("databaseInfo = ok false; want fallback to succeed")
	}
	if shardID != 500 {
		t.Errorf("shardID = %d, want 500 (from fallback)", shardID)
	}
	wantFallbackDir := fallbackDataRoot + "/databases/orders"
	if dataDir != wantFallbackDir {
		t.Errorf("dataDir = %q, want %q (from fallback)", dataDir, wantFallbackDir)
	}
}

// TestPlacementInfoSource_PlacementInfoDelegatesToDatabaseAndPlacement verifies
// that placementInfo returns the same Placement as databaseAndPlacementInfo
// (no extra store round-trip).
func TestPlacementInfoSource_PlacementInfoDelegatesToDatabaseAndPlacement(t *testing.T) {
	store := newPlacementStore(t)
	ctx := context.Background()
	p := &placement.Placement{
		TenantID: "rt", Database: "db", ShardID: 5555, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1"},
		},
	}
	if err := store.Put(ctx, p); err != nil {
		t.Fatalf("Put: %v", err)
	}

	src := &placementRaftInfoSource{
		store:             store,
		localNodeID:       "local:1",
		fallback:          newFakeRaftInfoSource(),
		timeout:           placementResolveTimeout,
		localRootResolver: poolRootResolver,
	}

	got, ok, _ := src.placementInfo("rt", "db")
	if !ok {
		t.Fatal("placementInfo = ok false; want true")
	}
	if got.ShardID != 5555 {
		t.Errorf("placementInfo.ShardID = %d, want 5555", got.ShardID)
	}
	// No local replica → false.
	_, ok2, _ := src.placementInfo("rt", "nonexistent")
	if ok2 {
		t.Error("placementInfo for nonexistent db must return false")
	}
}

// TestServices_PlacementMemberStart proves the placement-aware start branch of
// Services: when a placement entry assigns a replica to the local node, the
// shard is started with the FULL placement member set and join=false (the
// system-shard membership model), NOT join=true. With an RF=1 placement whose
// sole replica is the local node the group bootstraps single-node, elects
// itself leader, and serves a KV round-trip — exercising the membership branch
// that all dedicated replicas (leader and followers) now take.
func TestServices_PlacementMemberStart(t *testing.T) {
	host, _ := newSharedTestHost(t)
	reg := newVolumePooledRegistry(t)
	ctx := context.Background()

	registerVolumeTenant(t, reg, "pm")

	store := newPlacementStore(t)
	if err := store.Put(ctx, &placement.Placement{
		TenantID: "pm", Database: "db", ShardID: 1000, RF: 1, LeaderReplicaID: sharedHostReplicaID,
		Replicas: []placement.Replica{
			{
				ReplicaID: sharedHostReplicaID,
				NodeID:    sharedHostRaftAddr,
				RaftAddr:  sharedHostRaftAddr,
			},
		},
	}); err != nil {
		t.Fatalf("placement Put: %v", err)
	}

	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg,
		newFakeRaftInfoSource(), volTestLogger(),
		WithPlacementInfoSource(store, sharedHostRaftAddr, reg.LocalTenantDataRoot))
	t.Cleanup(func() { _ = f.CloseAll() })

	svcs, err := f.Services(ctx, "pm", "db")
	if err != nil {
		t.Fatalf("Services (placement member start): %v", err)
	}
	if svcs == nil || svcs.KV == nil {
		t.Fatal("Services returned nil services for placement member start")
	}

	// A KV round-trip proves the placement-member shard bootstrapped, elected a
	// leader (join=false single-member), and serves the data plane.
	if err := svcs.KV.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("KV.Put on placement member shard: %v", err)
	}
	got, err := svcs.KV.Get(ctx, "k")
	if err != nil {
		t.Fatalf("KV.Get on placement member shard: %v", err)
	}
	if string(got) != "v" {
		t.Errorf("KV.Get = %q, want %q", got, "v")
	}
}

// TestServices_PlacementMemberStart_JoinFlagFromReplicaState is the
// regression test pinning the Phase 4c-2 / observer-first join-flag fix
// (raft_backend_factory.go Services): join is computed from
// localReplica.State, true ONLY for ReplicaStateObserver (a replica that
// has never received a snapshot and must enter go-quicraft's join
// bootstrap window), false for ReplicaStateObserverReady and
// ReplicaStateActive (both already hold committed state -- restarting them
// with join=true would re-trigger the no-data-loss-restart bug) and for the
// zero-value legacy state (backward compatibility). IsShardJoining is used
// as the externally observable proxy for the join flag actually passed to
// go-quicraft's StartShard, since the flag itself is not otherwise exposed.
func TestServices_PlacementMemberStart_JoinFlagFromReplicaState(t *testing.T) {
	tests := []struct {
		name        string
		state       placement.ReplicaState
		wantJoining bool
	}{
		{"ReplicaStateObserver starts join=true (enters bootstrap window)", placement.ReplicaStateObserver, true},
		// A FRESH ObserverReady start (no persisted shard state, as this
		// fixture builds) is the wiped-observer edge: go-quicraft refuses to
		// seed a fresh voting membership naming an observer, so the factory
		// falls back to re-entering the join bootstrap window. A real
		// ObserverReady restart recovers persisted state and starts
		// join=false; that path is exercised by the integration scale tests.
		{"ReplicaStateObserverReady with no persisted state re-enters bootstrap window", placement.ReplicaStateObserverReady, true},
		{"ReplicaStateActive starts join=false", placement.ReplicaStateActive, false},
		{"zero-value legacy state starts join=false", placement.ReplicaState(0), false},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, _ := newSharedTestHost(t)
			reg := newVolumePooledRegistry(t)
			ctx := context.Background()

			tenantID := fmt.Sprintf("jf%d", i)
			registerVolumeTenant(t, reg, tenantID)
			shardID := uint64(9500 + i)

			store := newPlacementStore(t)
			if err := store.Put(ctx, &placement.Placement{
				TenantID: tenantID, Database: "db", ShardID: shardID, RF: 1, LeaderReplicaID: sharedHostReplicaID,
				Replicas: []placement.Replica{
					{
						ReplicaID: sharedHostReplicaID,
						NodeID:    sharedHostRaftAddr,
						RaftAddr:  sharedHostRaftAddr,
						State:     tt.state,
					},
				},
			}); err != nil {
				t.Fatalf("placement Put: %v", err)
			}

			f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg,
				newFakeRaftInfoSource(), volTestLogger(),
				WithPlacementInfoSource(store, sharedHostRaftAddr, reg.LocalTenantDataRoot))
			t.Cleanup(func() { _ = f.CloseAll() })

			if _, err := f.Services(ctx, tenantID, "db"); err != nil {
				t.Fatalf("Services: %v", err)
			}

			if got := f.IsShardJoining(shardID); got != tt.wantJoining {
				t.Errorf("IsShardJoining after start = %v, want %v (state=%v)", got, tt.wantJoining, tt.state)
			}
		})
	}
}

// TestPlacementInfoSource_DedicatedWAL verifies the per-tenant-KEY WAL opt-in is
// read from the replicated placement entry, and falls back to the meta source
// when no placement entry exists.
func TestPlacementInfoSource_DedicatedWAL(t *testing.T) {
	store := newPlacementStore(t)
	ctx := context.Background()
	if err := store.Put(ctx, &placement.Placement{
		TenantID: "rt", Database: "orders", ShardID: 7777, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1"},
		},
		DedicatedWAL: true,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	fallback := newFakeRaftInfoSource()
	src := &placementRaftInfoSource{store: store, localNodeID: "local:1", fallback: fallback,
		timeout: placementResolveTimeout, localRootResolver: poolRootResolver}
	if !src.dedicatedWAL("rt", "orders") {
		t.Errorf("dedicatedWAL from placement = false, want true")
	}

	// No placement entry → fall back to meta source.
	fallback.add("rt", "meta-only", 9, "/meta/root")
	fallback.setDedicated("rt", "meta-only")
	if !src.dedicatedWAL("rt", "meta-only") {
		t.Errorf("dedicatedWAL fallback to meta = false, want true")
	}
	if src.dedicatedWAL("rt", "absent") {
		t.Errorf("dedicatedWAL for unknown db = true, want false")
	}
}

// ---------------------------------------------------------------------------
// go-qrdb#482: the replicated drop fence (Placement.Dropping).
//
// RaftBackendFactory.Services is the resolution seam every node uses for a
// dedicated-raft database, so the fence has to be checked there -- on both
// the cold-start path (a shard never resolved on this node before) and a
// shard already cached from before a drop began. Checking the REPLICATED
// placement table here, rather than only database.Manager's own node-local
// fence, is what makes the fence hold on every node, not only the one that
// issued the Drop.
//
// A genuine multi-member Raft group across two independent, transport-less
// test Hosts cannot reach quorum in-process (neither can reach the other
// over the network), so these tests use TWO INDEPENDENT RaftBackendFactory
// instances -- standing in for two separate resolution attempts, e.g. two
// requests landing on the node hosting the sole RF=1 replica at two
// different times, or the same node before and after a restart -- sharing
// ONE placement.Store. That shared store is the property actually under
// test: the fence lives in the replicated table, not in either factory's
// own local state. This is the closest harness the repo has for placement
// resolution across nodes without a live multi-member cluster.
// ---------------------------------------------------------------------------

// newPlacementRaftFactory builds a RaftBackendFactory whose database-info
// source resolves from store for the given local node identity.
func newPlacementRaftFactory(t *testing.T, store *placement.Store, localNodeID string, reg *seal.BarrierRegistry) *RaftBackendFactory {
	t.Helper()
	host, _ := newSharedTestHost(t)
	src := &placementRaftInfoSource{
		store:             store,
		localNodeID:       localNodeID,
		fallback:          newFakeRaftInfoSource(),
		timeout:           placementResolveTimeout,
		localRootResolver: reg.LocalTenantDataRoot,
	}
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src, volTestLogger())
	t.Cleanup(func() { _ = f.CloseAll() })
	return f
}

// TestRaftBackendFactory_Services_RefusesCachedShardWhenPlacementDropping
// pins the cache-hit half of the check: a shard already started (and
// cached) before a drop marks its placement dropping must still refuse once
// it does -- returning the cached bundle unconditionally would let a write
// reach a keyspace already being reclaimed.
func TestRaftBackendFactory_Services_RefusesCachedShardWhenPlacementDropping(t *testing.T) {
	store := newPlacementStore(t)
	reg := newVolumePooledRegistry(t)
	registerVolumeTenant(t, reg, "rt")
	ctx := context.Background()

	if err := store.Put(ctx, &placement.Placement{
		TenantID: "rt", Database: "orders", ShardID: 5001, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "node-a", RaftAddr: sharedHostRaftAddr},
		},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	f := newPlacementRaftFactory(t, store, "node-a", reg)

	if _, err := f.Services(ctx, "rt", "orders"); err != nil {
		t.Fatalf("first Services() (before the drop) = %v, want success", err)
	}

	// A drop, committed to the shared replicated store -- exactly as
	// DatabaseService.DropForTenant now does BEFORE reclaiming anything.
	if err := store.SetDropping(ctx, "rt", "orders", true); err != nil {
		t.Fatalf("SetDropping: %v", err)
	}

	_, err := f.Services(ctx, "rt", "orders")
	var dropping *database.DroppingError
	if !errors.As(err, &dropping) {
		t.Fatalf("second Services() (after the drop) error = %v (%T), want *database.DroppingError", err, err)
	}
}

// TestRaftBackendFactory_Services_RefusesCachedShardWhenPlacementDeleted is
// go-qrdb#482 round 3's finding 6: a completed drop's LAST step
// (finishRaftPlacementTeardown) removes the whole placement entry, not just
// its Dropping flag -- so placementInfo, once that runs, reports ok=false,
// not a Placement with Dropping=true. The cache-hit path must still refuse a
// cached shard in that state: the reconciler's eviction of this cache entry
// is asynchronous (its next tick), so a request landing in that window must
// not succeed against a database that no longer exists anywhere else in the
// cluster.
func TestRaftBackendFactory_Services_RefusesCachedShardWhenPlacementDeleted(t *testing.T) {
	store := newPlacementStore(t)
	reg := newVolumePooledRegistry(t)
	registerVolumeTenant(t, reg, "rt")
	ctx := context.Background()

	if err := store.Put(ctx, &placement.Placement{
		TenantID: "rt", Database: "orders", ShardID: 5005, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: sharedHostRaftAddr, RaftAddr: sharedHostRaftAddr},
		},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	f := newPlacementRaftFactory(t, store, sharedHostRaftAddr, reg)

	if _, err := f.Services(ctx, "rt", "orders"); err != nil {
		t.Fatalf("first Services() (before the drop) = %v, want success", err)
	}

	// The drop completes in full: unlike SetDropping(true), this removes the
	// WHOLE entry, exactly as finishRaftPlacementTeardown does at the end of
	// a successful DropForTenant/Drop.
	if err := store.Delete(ctx, "rt", "orders"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := f.Services(ctx, "rt", "orders")
	var dropping *database.DroppingError
	if !errors.As(err, &dropping) {
		t.Fatalf("Services() after the placement entry was deleted, error = %v (%T), want *database.DroppingError", err, err)
	}
}

// TestRaftBackendFactory_Services_FallbackShardUnaffectedByMissingPlacement
// is TestRaftBackendFactory_Services_RefusesCachedShardWhenPlacementDeleted's
// negative case: a shard resolved through the Phase-4b fallback path (no
// placement entry ever existed for it) reports placementInfo ok=false
// PERMANENTLY, by design -- that must never be confused with a
// placement-resolved shard whose entry was just deleted, or every
// single-node / no-scheduler deployment's cache-hit path would start
// refusing itself.
func TestRaftBackendFactory_Services_FallbackShardUnaffectedByMissingPlacement(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	ctx := context.Background()

	// fakeRaftInfoSource.placementInfo always reports ok=false, matching
	// managerRaftInfoSource's semantics for every Phase-4b (pre-placement)
	// composition: there is no replicated placement table behind it at all.
	dataRoot, _ := registerVolumeTenant(t, reg, "rt")
	src.add("rt", "orders", 6001, dataRoot)

	if _, err := f.Services(ctx, "rt", "orders"); err != nil {
		t.Fatalf("first Services() = %v, want success", err)
	}
	if _, err := f.Services(ctx, "rt", "orders"); err != nil {
		t.Fatalf("cache-hit Services() = %v, want success (a fallback shard's permanent ok=false must not be mistaken for a deleted placement entry)", err)
	}
}

// ---------------------------------------------------------------------------
// go-qrdb#482 round 3's second review, finding 2: the raw IsolationRaft
// service bundle (pkg/server/rest_registry.go's resolve) bypassed
// applyComposite entirely, so a handle resolved before a Drop set
// Placement.Dropping could keep writing afterward. RaftBackendFactory.
// RefuseIfDropping gives that raw bundle the same per-write recheck every
// other transport's write path has, without resolving or caching a new
// shard.
// ---------------------------------------------------------------------------

// TestRaftBackendFactory_RefuseIfDropping_RefusesAfterCachedShardIsFenced
// proves the core guarantee: a shard resolved (and cached) BEFORE a drop
// sets the replicated fence must still be refused by RefuseIfDropping
// called AFTER the fence is set, exactly as if Services itself had been
// called again -- but without resolving or caching anything new.
func TestRaftBackendFactory_RefuseIfDropping_RefusesAfterCachedShardIsFenced(t *testing.T) {
	store := newPlacementStore(t)
	reg := newVolumePooledRegistry(t)
	registerVolumeTenant(t, reg, "rt")
	ctx := context.Background()

	if err := store.Put(ctx, &placement.Placement{
		TenantID: "rt", Database: "orders", ShardID: 5010, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: sharedHostRaftAddr, RaftAddr: sharedHostRaftAddr},
		},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	f := newPlacementRaftFactory(t, store, sharedHostRaftAddr, reg)

	if _, err := f.Services(ctx, "rt", "orders"); err != nil {
		t.Fatalf("Services() (before the drop) = %v, want success", err)
	}
	if err := f.RefuseIfDropping("rt", "orders"); err != nil {
		t.Fatalf("RefuseIfDropping() (before the drop) = %v, want nil", err)
	}

	if err := store.SetDropping(ctx, "rt", "orders", true); err != nil {
		t.Fatalf("SetDropping: %v", err)
	}

	var dropping *database.DroppingError
	if err := f.RefuseIfDropping("rt", "orders"); !errors.As(err, &dropping) {
		t.Fatalf("RefuseIfDropping() (after the drop) error = %v (%T), want *database.DroppingError", err, err)
	}
}

// TestRaftBackendFactory_RefuseIfDropping_UnresolvedDatabaseIsANoOp proves
// RefuseIfDropping never refuses (and never resolves or caches anything)
// for a database this factory has not seen yet: the next Services call for
// it performs the identical cold-path check on its own.
func TestRaftBackendFactory_RefuseIfDropping_UnresolvedDatabaseIsANoOp(t *testing.T) {
	f, _, _ := newRaftFactory(t)
	if err := f.RefuseIfDropping("rt", "never-resolved"); err != nil {
		t.Fatalf("RefuseIfDropping() = %v, want nil", err)
	}
}

// TestDatabaseFenceApply_PassThroughViewCarriesTheGuard proves the shape the
// IsolationRaft branch of resolve builds with databaseFence.apply(base, "",
// false): KV is a pass-through view -- an EMPTY namespace prefix, because a
// dedicated shard's keyspace needs no composite namespacing -- that carries
// the database's drop-fence guard, while Collection, Watch and Lease are
// left exactly as svcs carried them.
func TestDatabaseFenceApply_PassThroughViewCarriesTheGuard(t *testing.T) {
	fence, err := newDatabaseFence("rt", "orders", "", nil)
	if err != nil {
		t.Fatalf("newDatabaseFence: %v", err)
	}
	svcs := &DatabaseServices{KV: newMockKVService()}

	fenced := fence.apply(svcs, "", false)

	if fenced.Watch != nil || fenced.Collection != nil || fenced.Lease != nil {
		t.Error("apply must leave Watch, Collection and Lease exactly as svcs carried them (nil here)")
	}
	wrapped, ok := fenced.KV.(*namespacedKVService)
	if !ok {
		t.Fatalf("fenced.KV = %T, want *namespacedKVService", fenced.KV)
	}
	if wrapped.prefix != "" {
		t.Errorf("prefix = %q, want empty (a dedicated shard needs no composite namespace)", wrapped.prefix)
	}
	if wrapped.guard.IsZero() {
		t.Fatal("the pass-through view carries no guard: its writes would not be refused once the shard is fenced")
	}
}

// TestRaftBackendFactory_Services_RefusesFreshResolutionWhenPlacementDropping
// pins the cold-path half: a node resolving the database for the FIRST time
// -- standing in for a different replica, or the same one after a restart --
// must see the fence immediately through the replicated placement table
// alone, without ever having witnessed the Drop itself, and without
// starting a shard for a database already being torn down.
func TestRaftBackendFactory_Services_RefusesFreshResolutionWhenPlacementDropping(t *testing.T) {
	store := newPlacementStore(t)
	reg := newVolumePooledRegistry(t)
	registerVolumeTenant(t, reg, "rt")
	ctx := context.Background()

	if err := store.Put(ctx, &placement.Placement{
		TenantID: "rt", Database: "orders", ShardID: 5002, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "node-a", RaftAddr: sharedHostRaftAddr},
		},
		Dropping: true,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// A fresh factory that has never resolved this database before -- it
	// only ever sees the placement entry AFTER the fence was already set.
	f := newPlacementRaftFactory(t, store, "node-a", reg)

	_, err := f.Services(ctx, "rt", "orders")
	var dropping *database.DroppingError
	if !errors.As(err, &dropping) {
		t.Fatalf("Services() error = %v (%T), want *database.DroppingError", err, err)
	}
	if len(f.ListStarted()) != 0 {
		t.Error("Services() started a shard for a database already marked dropping in placement")
	}
}

// TestRefusalFromPlacement is the direct, host-free unit test for the check
// both tests above exercise end to end.
func TestRefusalFromPlacement(t *testing.T) {
	if err := refusalFromPlacement("orders", nil); err != nil {
		t.Errorf("nil placement: error = %v, want nil (no replicated fence to check)", err)
	}
	if err := refusalFromPlacement("orders", &placement.Placement{Database: "orders"}); err != nil {
		t.Errorf("Dropping=false: error = %v, want nil", err)
	}
	err := refusalFromPlacement("orders", &placement.Placement{Database: "orders", Dropping: true})
	var dropping *database.DroppingError
	if !errors.As(err, &dropping) {
		t.Fatalf("Dropping=true: error = %v (%T), want *database.DroppingError", err, err)
	}
	if dropping.Name != "orders" {
		t.Errorf("DroppingError.Name = %q, want %q", dropping.Name, "orders")
	}
}

// TestRaftBackendFactory_FenceServicesResolvesADroppingDatabase proves the
// one exception FenceServices exists for: DROP DATABASE sets the replicated
// placement marker BEFORE it writes the drop fence into the database's own
// shard, so the resolution that write needs must succeed while Services --
// every data-plane resolution -- refuses. Both the cache-hit and the cold
// path are covered.
func TestRaftBackendFactory_FenceServicesResolvesADroppingDatabase(t *testing.T) {
	store := newPlacementStore(t)
	reg := newVolumePooledRegistry(t)
	registerVolumeTenant(t, reg, "rt")
	ctx := context.Background()

	if err := store.Put(ctx, &placement.Placement{
		TenantID: "rt", Database: "orders", ShardID: 5020, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: sharedHostRaftAddr, RaftAddr: sharedHostRaftAddr},
		},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	f := newPlacementRaftFactory(t, store, sharedHostRaftAddr, reg)
	if _, err := f.Services(ctx, "rt", "orders"); err != nil {
		t.Fatalf("Services() before the drop: %v", err)
	}
	if err := store.SetDropping(ctx, "rt", "orders", true); err != nil {
		t.Fatalf("SetDropping: %v", err)
	}

	var dropping *database.DroppingError
	if _, err := f.Services(ctx, "rt", "orders"); !errors.As(err, &dropping) {
		t.Fatalf("Services() after the drop = %v, want *database.DroppingError", err)
	}
	svcs, err := f.FenceServices(ctx, "rt", "orders")
	if err != nil || svcs == nil || svcs.KV == nil {
		t.Fatalf("FenceServices() on the cached shard = (%v, %v), want its services", svcs, err)
	}

	// Cold path: a factory that has never resolved the database sees the
	// marker already set. The first factory's shard is stopped first -- the
	// two would otherwise contend for the same on-disk shard.
	if err := f.Close("rt", "orders"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	cold := newPlacementRaftFactory(t, store, sharedHostRaftAddr, reg)
	if _, err := cold.Services(ctx, "rt", "orders"); !errors.As(err, &dropping) {
		t.Fatalf("cold Services() = %v, want *database.DroppingError", err)
	}
	if svcs, err := cold.FenceServices(ctx, "rt", "orders"); err != nil || svcs == nil {
		t.Fatalf("cold FenceServices() = (%v, %v), want the shard's services", svcs, err)
	}
}

// TestRaftBackendFactory_CachedShardOfADroppedIncarnation pins that a shard
// cached by name never outlives its incarnation. Once the placement entry is
// gone, fencing through the cached shard is refused (the database has no
// current store to mark, and the doomed one must not be unfenced); once the
// name is placed again on a NEW shard, the cached one is evicted and the new
// one served, so the new database's writes never reach the old shard.
func TestRaftBackendFactory_CachedShardOfADroppedIncarnation(t *testing.T) {
	store := newPlacementStore(t)
	reg := newVolumePooledRegistry(t)
	registerVolumeTenant(t, reg, "rt")
	ctx := context.Background()
	place := func(shardID uint64) {
		t.Helper()
		if err := store.Put(ctx, &placement.Placement{
			TenantID: "rt", Database: "orders", ShardID: shardID, RF: 1, LeaderReplicaID: 1,
			Replicas: []placement.Replica{
				{ReplicaID: 1, NodeID: sharedHostRaftAddr, RaftAddr: sharedHostRaftAddr},
			},
		}); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	place(5030)
	f := newPlacementRaftFactory(t, store, sharedHostRaftAddr, reg)
	if _, err := f.Services(ctx, "rt", "orders"); err != nil {
		t.Fatalf("Services(): %v", err)
	}

	if err := store.Delete(ctx, "rt", "orders"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.FenceServices(ctx, "rt", "orders"); !errors.Is(err, ErrRaftDatabaseUnknown) {
		t.Fatalf("FenceServices() after the placement is gone = %v, want ErrRaftDatabaseUnknown", err)
	}
	var dropping *database.DroppingError
	if _, err := f.Services(ctx, "rt", "orders"); !errors.As(err, &dropping) {
		t.Fatalf("Services() after the placement is gone = %v, want *database.DroppingError", err)
	}

	place(5031)
	if _, err := f.Services(ctx, "rt", "orders"); err != nil {
		t.Fatalf("Services() for the new incarnation: %v", err)
	}
	f.mu.Lock()
	rs := f.shards[raftShardKey{tenantID: "rt", database: "orders"}]
	f.mu.Unlock()
	if rs == nil || rs.shardID != 5031 {
		t.Fatalf("cached shard = %+v, want the new incarnation's shard 5031", rs)
	}
}
