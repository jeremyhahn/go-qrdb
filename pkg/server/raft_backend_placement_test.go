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
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
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

// TestPlacementInfoSource_LocalReplicaWins verifies the placement entry's shard
// ID and the LOCAL replica's data root take precedence over the fallback.
func TestPlacementInfoSource_LocalReplicaWins(t *testing.T) {
	store := newPlacementStore(t)
	ctx := context.Background()
	if err := store.Put(ctx, &placement.Placement{
		TenantID: "rt", Database: "orders", ShardID: 7777, RF: 2, LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1", DataRoot: "/placement/local"},
			{ReplicaID: 2, NodeID: "other:1", RaftAddr: "other:1", DataRoot: "/placement/other"},
		},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	fallback := newFakeRaftInfoSource()
	fallback.add("rt", "orders", 1234, "/meta/root") // different shard/dir

	src := &placementRaftInfoSource{store: store, localNodeID: "local:1", fallback: fallback, timeout: placementResolveTimeout}
	shardID, dataDir, ok := src.databaseInfo("rt", "orders")
	if !ok {
		t.Fatalf("databaseInfo not ok")
	}
	if shardID != 7777 {
		t.Errorf("shard = %d, want 7777 (from placement)", shardID)
	}
	if dataDir != "/placement/local" {
		t.Errorf("dataDir = %q, want /placement/local (local replica)", dataDir)
	}
}

// TestPlacementInfoSource_NoEntryFallsBack verifies resolution falls back to the
// meta source when no placement entry exists (Phase-4b compatibility).
func TestPlacementInfoSource_NoEntryFallsBack(t *testing.T) {
	store := newPlacementStore(t)
	fallback := newFakeRaftInfoSource()
	fallback.add("rt", "orders", 1234, "/meta/root")

	src := &placementRaftInfoSource{store: store, localNodeID: "local:1", fallback: fallback, timeout: placementResolveTimeout}
	shardID, dataDir, ok := src.databaseInfo("rt", "orders")
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
			{ReplicaID: 1, NodeID: "remote:1", RaftAddr: "remote:1", DataRoot: "/placement/remote"},
		},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	fallback := newFakeRaftInfoSource()
	fallback.add("rt", "orders", 1234, "/meta/root")

	src := &placementRaftInfoSource{store: store, localNodeID: "local:1", fallback: fallback, timeout: placementResolveTimeout}
	shardID, _, ok := src.databaseInfo("rt", "orders")
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
			{ReplicaID: 1, NodeID: "leader:1", RaftAddr: "leader:1", DataRoot: "/leader/data"},
			// local node recorded with empty DataRoot (as written by the leader).
			{ReplicaID: 2, NodeID: "local:1", RaftAddr: "local:1", DataRoot: ""},
			{ReplicaID: 3, NodeID: "other:1", RaftAddr: "other:1", DataRoot: ""},
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

	shardID, dataDir, ok := src.databaseInfo("follower", "orders")
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
			{ReplicaID: 1, NodeID: "leader:1", RaftAddr: "leader:1", DataRoot: "/leader/data"},
			{ReplicaID: 2, NodeID: "local:1", RaftAddr: "local:1", DataRoot: ""},
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

	shardID, dataDir, ok := src.databaseInfo("follower", "orders")
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
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1", DataRoot: "/root"},
		},
	}
	if err := store.Put(ctx, p); err != nil {
		t.Fatalf("Put: %v", err)
	}

	src := &placementRaftInfoSource{
		store:       store,
		localNodeID: "local:1",
		fallback:    newFakeRaftInfoSource(),
		timeout:     placementResolveTimeout,
	}

	got, ok := src.placementInfo("rt", "db")
	if !ok {
		t.Fatal("placementInfo = ok false; want true")
	}
	if got.ShardID != 5555 {
		t.Errorf("placementInfo.ShardID = %d, want 5555", got.ShardID)
	}
	// No local replica → false.
	_, ok2 := src.placementInfo("rt", "nonexistent")
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

	dataRoot, _ := registerVolumeTenant(t, reg, "pm")
	shardDataDir := filepath.Join(dataRoot, "databases", "db")

	store := newPlacementStore(t)
	if err := store.Put(ctx, &placement.Placement{
		TenantID: "pm", Database: "db", ShardID: 1000, RF: 1, LeaderReplicaID: sharedHostReplicaID,
		Replicas: []placement.Replica{
			{
				ReplicaID: sharedHostReplicaID,
				NodeID:    sharedHostRaftAddr,
				RaftAddr:  sharedHostRaftAddr,
				DataRoot:  shardDataDir,
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
			dataRoot, _ := registerVolumeTenant(t, reg, tenantID)
			shardDataDir := filepath.Join(dataRoot, "databases", "db")
			shardID := uint64(9500 + i)

			store := newPlacementStore(t)
			if err := store.Put(ctx, &placement.Placement{
				TenantID: tenantID, Database: "db", ShardID: shardID, RF: 1, LeaderReplicaID: sharedHostReplicaID,
				Replicas: []placement.Replica{
					{
						ReplicaID: sharedHostReplicaID,
						NodeID:    sharedHostRaftAddr,
						RaftAddr:  sharedHostRaftAddr,
						DataRoot:  shardDataDir,
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
			{ReplicaID: 1, NodeID: "local:1", RaftAddr: "local:1", DataRoot: "/placement/local"},
		},
		DedicatedWAL: true,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	fallback := newFakeRaftInfoSource()
	src := &placementRaftInfoSource{store: store, localNodeID: "local:1", fallback: fallback, timeout: placementResolveTimeout}
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
