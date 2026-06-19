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
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// memPlacementKV is an in-memory placement.KV for service-layer placement tests.
type memPlacementKV struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemPlacementKV() *memPlacementKV { return &memPlacementKV{data: map[string][]byte{}} }

func (m *memPlacementKV) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[key] = cp
	return nil
}

func (m *memPlacementKV) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (m *memPlacementKV) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memPlacementKV) Scan(_ context.Context, prefix string, fn func(string, []byte) error) error {
	m.mu.Lock()
	snapshot := make(map[string][]byte)
	for k, v := range m.data {
		if strings.HasPrefix(k, prefix) {
			snapshot[k] = v
		}
	}
	m.mu.Unlock()
	for k, v := range snapshot {
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

// staticTopology is a mock placement.TopologyProvider for service tests.
type staticTopology struct {
	nodes []placement.NodeLoad
}

func (s *staticTopology) Nodes() []placement.NodeLoad { return s.nodes }

// newPlacementDatabaseService builds a DatabaseService for an IsolationRaft
// tenant wired with a placement store + scheduler over the given topology.
func newPlacementDatabaseService(t *testing.T, info *tenant.TenantInfo, f *fakeReplicaQuota, nodes []placement.NodeLoad) (DatabaseServicer, *placement.Store) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir(), database.WithAllocator(shard.NewAllocator()))
	t.Cleanup(func() { _ = mgr.Close() })

	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	scheduler, err := placement.NewScheduler(&staticTopology{nodes: nodes})
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}

	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: info}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
	)
	return svc, store
}

func healthyNodes(ids ...string) []placement.NodeLoad {
	out := make([]placement.NodeLoad, 0, len(ids))
	for _, id := range ids {
		out = append(out, placement.NodeLoad{NodeID: id, RaftAddr: id + ":1", Healthy: true})
	}
	return out
}

// TestCreateForTenant_PersistsPlacement verifies a dedicated-raft database's
// placement is computed and persisted on create.
func TestCreateForTenant_PersistsPlacement(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc, store := newPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1", "n2", "n3"))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 3
	info, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	p, err := store.Get(ctx, "rt", "orders")
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	if p.ShardID != info.ShardID {
		t.Errorf("placement shard = %d, want %d (db shard)", p.ShardID, info.ShardID)
	}
	if p.RF != 3 || len(p.Replicas) != 3 {
		t.Errorf("placement RF = %d replicas = %d, want 3/3", p.RF, len(p.Replicas))
	}
	if p.LeaderReplicaID == 0 {
		t.Errorf("placement leader replica ID unset")
	}
	// Every replica's data root is the database's DataDir at this stage.
	for _, r := range p.Replicas {
		if r.DataRoot != info.DataDir {
			t.Errorf("replica data root = %q, want %q", r.DataRoot, info.DataDir)
		}
	}
}

// newPlacementDatabaseServiceWithLocalNode is like newPlacementDatabaseService
// but wires a localNodeID via WithLocalNodeID. The node ID must appear in the
// topology so the scheduler can assign a replica to it.
func newPlacementDatabaseServiceWithLocalNode(t *testing.T, info *tenant.TenantInfo, f *fakeReplicaQuota, nodes []placement.NodeLoad, localNodeID string) (DatabaseServicer, *placement.Store) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir(), database.WithAllocator(shard.NewAllocator()))
	t.Cleanup(func() { _ = mgr.Close() })

	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	scheduler, err := placement.NewScheduler(&staticTopology{nodes: nodes})
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}

	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: info}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
		WithLocalNodeID(localNodeID),
	)
	return svc, store
}

// TestCreateForTenant_PersistsPlacementLocalNodeDataRoot verifies the BUG 1 fix:
// when localNodeID is set, only the local node's placement Replica.DataRoot is
// populated with the leader's DataDir; remote replicas record "" so each follower
// can resolve its own local pool path.
func TestCreateForTenant_PersistsPlacementLocalNodeDataRoot(t *testing.T) {
	const localNode = "local:1"
	nodes := healthyNodes(localNode, "remote:1", "remote:2")
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc, store := newPlacementDatabaseServiceWithLocalNode(t, raftTenant(t), f, nodes, localNode)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("bug1db")
	cfg.Replicas = 3
	info, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	p, err := store.Get(ctx, "rt", "bug1db")
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	if len(p.Replicas) != 3 {
		t.Fatalf("expected 3 replicas, got %d", len(p.Replicas))
	}

	var localCount, emptyCount int
	for _, r := range p.Replicas {
		if r.NodeID == localNode {
			if r.DataRoot != info.DataDir {
				t.Errorf("local replica DataRoot = %q, want %q (leader DataDir)", r.DataRoot, info.DataDir)
			}
			localCount++
		} else {
			if r.DataRoot != "" {
				t.Errorf("remote replica %q DataRoot = %q, want empty string", r.NodeID, r.DataRoot)
			}
			emptyCount++
		}
	}
	if localCount != 1 {
		t.Errorf("local replica count = %d, want 1", localCount)
	}
	if emptyCount != 2 {
		t.Errorf("remote replica count = %d, want 2", emptyCount)
	}
}

// TestDropForTenant_RemovesPlacement verifies the placement entry is removed on
// drop.
func TestDropForTenant_RemovesPlacement(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc, store := newPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1", "n2"))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 2
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if _, err := store.Get(ctx, "rt", "orders"); err != nil {
		t.Fatalf("placement should exist after create: %v", err)
	}

	if err := svc.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	if _, err := store.Get(ctx, "rt", "orders"); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement after drop error = %v, want ErrPlacementNotFound", err)
	}
}

// TestCreateForTenant_ClampsRFToClusterSize verifies the requested replication
// factor is clamped to the live cluster size before metering and placement.
func TestCreateForTenant_ClampsRFToClusterSize(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 0, MaxTotalReplicas: 100}}
	// Cluster of two nodes; request five replicas.
	svc, store := newPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1", "n2"))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 5
	info, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if info.Replicas != 2 {
		t.Errorf("persisted Replicas = %d, want 2 (clamped to cluster size)", info.Replicas)
	}
	if f.incTotal != 2 {
		t.Errorf("metered replicas = %d, want 2 (clamped)", f.incTotal)
	}
	p, err := store.Get(ctx, "rt", "orders")
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	if p.RF != 2 {
		t.Errorf("placement RF = %d, want 2", p.RF)
	}
}

// TestCreateForTenant_PlacementFailureRollsBack verifies that when placement
// cannot be computed (no healthy nodes), the create is rolled back: the database
// is dropped and the quota reservations are released.
func TestCreateForTenant_PlacementFailureRollsBack(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	// One unhealthy node => ClusterSize 1 (no clamp below 1), but Place fails
	// with ErrInsufficientNodes because no node is healthy.
	svc, store := newPlacementDatabaseService(t, raftTenant(t), f,
		[]placement.NodeLoad{{NodeID: "n1", RaftAddr: "n1:1", Healthy: false}})
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 1
	_, err := svc.CreateForTenant(ctx, cfg)
	if !errors.Is(err, placement.ErrInsufficientNodes) {
		t.Fatalf("CreateForTenant error = %v, want ErrInsufficientNodes", err)
	}
	// Database rolled back.
	if _, gerr := svc.GetForTenant(ctx, "orders"); gerr == nil {
		t.Errorf("database should have been dropped after placement failure")
	}
	// No placement entry persisted.
	if _, gerr := store.Get(ctx, "rt", "orders"); !errors.Is(gerr, placement.ErrPlacementNotFound) {
		t.Errorf("no placement entry expected, got err = %v", gerr)
	}
	// Quota reservations released.
	if f.incRaft != f.decRaft || f.incTotal != f.decTotal {
		t.Errorf("quota not balanced after rollback: incRaft=%d decRaft=%d incTotal=%d decTotal=%d",
			f.incRaft, f.decRaft, f.incTotal, f.decTotal)
	}
}
