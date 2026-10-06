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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// fakeRaftHost implements RaftHost for unit tests. It records every
// AddFollowerNode / AddObserver / RemoveFollowerNode call and optionally
// returns a configured error for each. leaderID/leaderOK configure
// LeaderReplicaID's return value (used by ScaleForTenant's scale-down
// tests).
type fakeRaftHost struct {
	calls         []followerNodeCall
	err           error
	observerCalls []followerNodeCall
	observerErr   error
	removeCalls   []removeFollowerNodeCall
	removeErr     error
	leaderID      uint64
	leaderOK      bool
	leaderErr     error

	// transferCalls records every RequestLeaderTransfer invocation.
	// transferErr, when non-nil, is returned by RequestLeaderTransfer
	// itself. transferNoop, when true, records the call but does NOT
	// update leaderID -- simulates a transfer request that never
	// completes, exercising transferLeadershipAwayFromRemoved's timeout
	// path. Otherwise RequestLeaderTransfer sets leaderID to the
	// requested target, simulating an instantaneous, successful transfer
	// (sufficient for unit tests, since the real transfer is asynchronous
	// in production and transferLeadershipAwayFromRemoved polls for it).
	transferCalls []leaderTransferCall
	transferErr   error
	transferNoop  bool

	// onAdd/onObserver/onRemove, when non-nil, are invoked before recording
	// each call -- used by ordering tests to interleave a shared step log
	// with the key-distributor and placement-store fakes.
	onAdd      func()
	onObserver func()
	onRemove   func()
}

// leaderTransferCall captures the arguments of one RequestLeaderTransfer
// invocation.
type leaderTransferCall struct {
	shardID         uint64
	targetReplicaID uint64
}

// RequestLeaderTransfer records the call and, unless transferNoop is set,
// simulates an immediate successful transfer by updating leaderID to
// targetReplicaID. See the fakeRaftHost.transferNoop field doc for the
// timeout-path testing rationale.
func (f *fakeRaftHost) RequestLeaderTransfer(_ context.Context, shardID, targetReplicaID uint64) error {
	f.transferCalls = append(f.transferCalls, leaderTransferCall{shardID: shardID, targetReplicaID: targetReplicaID})
	if f.transferErr != nil {
		return f.transferErr
	}
	if !f.transferNoop {
		f.leaderID = targetReplicaID
	}
	return nil
}

// followerNodeCall captures the arguments of one AddFollowerNode or
// AddObserver invocation.
type followerNodeCall struct {
	shardID   uint64
	replicaID uint64
	raftAddr  string
}

// removeFollowerNodeCall captures the arguments of one RemoveFollowerNode
// invocation.
type removeFollowerNodeCall struct {
	shardID   uint64
	replicaID uint64
}

func (f *fakeRaftHost) RemoveFollowerNode(_ context.Context, shardID, replicaID uint64) error {
	if f.onRemove != nil {
		f.onRemove()
	}
	f.removeCalls = append(f.removeCalls, removeFollowerNodeCall{shardID: shardID, replicaID: replicaID})
	return f.removeErr
}

func (f *fakeRaftHost) LeaderReplicaID(_ context.Context, _ uint64) (uint64, bool, error) {
	return f.leaderID, f.leaderOK, f.leaderErr
}

func (f *fakeRaftHost) AddFollowerNode(_ context.Context, shardID, replicaID uint64, raftAddr string) error {
	if f.onAdd != nil {
		f.onAdd()
	}
	f.calls = append(f.calls, followerNodeCall{
		shardID:   shardID,
		replicaID: replicaID,
		raftAddr:  raftAddr,
	})
	return f.err
}

// AddObserver records a non-voting observer admission. scaleUp calls this
// (never AddFollowerNode) to add a replica to an already-running group; see
// database_service_scale_test.go for the scale-up assertions that exercise
// it.
func (f *fakeRaftHost) AddObserver(_ context.Context, shardID, replicaID uint64, raftAddr string) error {
	if f.onObserver != nil {
		f.onObserver()
	}
	f.observerCalls = append(f.observerCalls, followerNodeCall{
		shardID:   shardID,
		replicaID: replicaID,
		raftAddr:  raftAddr,
	})
	return f.observerErr
}

// newNodeAddDatabaseService builds a DatabaseService for an IsolationRaft
// tenant with placement, quota, and raftHost wired. localNodeID is passed
// as the local node identifier so the leader check inside CreateForTenant
// can decide whether to call AddFollowerNode. nodes must include a node with
// NodeID == localNodeID; that node's placement entry becomes the leader
// (the scheduler picks the least-loaded node first).
func newNodeAddDatabaseService(
	t *testing.T,
	localNodeID string,
	raftHost RaftHost,
	nodes []placement.NodeLoad,
) DatabaseServicer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	scheduler, err := placement.NewScheduler(&staticTopology{nodes: nodes})
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}

	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	opts := []DatabaseServiceOption{
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: nodeAddRaftTenant(t)}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
		WithLocalNodeID(localNodeID),
	}
	if raftHost != nil {
		opts = append(opts, WithRaftHost(raftHost))
	}
	return NewDatabaseService(mgr, opts...)
}

// nodeAddRaftTenant returns a fresh IsolationRaft TenantInfo for add-node tests.
func nodeAddRaftTenant(t *testing.T) *tenant.TenantInfo {
	t.Helper()
	return &tenant.TenantInfo{
		ID:            "nodeadd-tenant",
		IsolationType: tenant.IsolationRaft,
	}
}

// nodeAddCtx returns a context carrying the nodeadd-tenant tenant ID.
func nodeAddCtx() context.Context {
	return tenant.WithTenantID(context.Background(), "nodeadd-tenant")
}

// nodeAddNodes returns a three-node topology where node IDs match the labels
// "node1", "node2", "node3". The scheduler sorts by (Bytes, ShardCount, NodeID)
// ascending, so "node1" is always the least-loaded candidate and thus the
// leader (replicas[0].NodeID == "node1").
func nodeAddNodes() []placement.NodeLoad {
	return healthyNodes("node1", "node2", "node3")
}

// leaderNodeID is the NodeID the scheduler selects as leader for nodeAddNodes.
const leaderNodeID = "node1"

// TestCreateForTenant_AddFollowerNodeCalledForFollowers verifies that
// AddFollowerNode is called once for each follower replica (not the local
// leader) when the local node is the placement leader.
func TestCreateForTenant_AddFollowerNodeCalledForFollowers(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{}
	svc := newNodeAddDatabaseService(t, leaderNodeID, host, nodeAddNodes())

	cfg := testDBConfig("orders")
	cfg.Replicas = 3
	info, err := svc.CreateForTenant(nodeAddCtx(), cfg)
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	// Three replicas → two non-local followers → two AddFollowerNode calls.
	if len(host.calls) != 2 {
		t.Fatalf("AddFollowerNode called %d times, want 2", len(host.calls))
	}

	// Every call must use the database's shard ID.
	for i, call := range host.calls {
		if call.shardID != info.ShardID {
			t.Errorf("call[%d].shardID = %d, want %d", i, call.shardID, info.ShardID)
		}
		if call.replicaID == 0 {
			t.Errorf("call[%d].replicaID must not be zero", i)
		}
		if call.raftAddr == "" {
			t.Errorf("call[%d].raftAddr must not be empty", i)
		}
	}
}

// TestCreateForTenant_AddFollowerNodeSkipsLocalLeader verifies that
// AddFollowerNode is never called for the local node itself: the local leader
// already holds the shard and must not be passed as a follower to add.
func TestCreateForTenant_AddFollowerNodeSkipsLocalLeader(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{}
	svc := newNodeAddDatabaseService(t, leaderNodeID, host, nodeAddNodes())

	cfg := testDBConfig("accounts")
	cfg.Replicas = 3
	if _, err := svc.CreateForTenant(nodeAddCtx(), cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	for i, call := range host.calls {
		// The RaftAddr for the local node is leaderNodeID + ":1" (from
		// healthyNodes). Neither the NodeID nor the RaftAddr of the local
		// leader should appear in any call.
		if call.raftAddr == leaderNodeID+":1" {
			t.Errorf("call[%d]: local leader raftAddr %q passed to AddFollowerNode", i, call.raftAddr)
		}
	}
}

// TestCreateForTenant_AddFollowerNodeSkippedWhenRaftHostNil verifies that
// when no RaftHost is configured the create still succeeds and no membership
// invite is attempted.
func TestCreateForTenant_AddFollowerNodeSkippedWhenRaftHostNil(t *testing.T) {
	t.Parallel()

	// Build without raftHost.
	svc := newNodeAddDatabaseService(t, leaderNodeID, nil, nodeAddNodes())

	cfg := testDBConfig("payments")
	cfg.Replicas = 3
	if _, err := svc.CreateForTenant(nodeAddCtx(), cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	// If we reach here without panic the nil guard is in place.
}

// TestCreateForTenant_AddFollowerNodeErrorIsNonFatal verifies that an error
// from AddFollowerNode is logged but does not fail the database creation: the
// catalog entry and placement are persisted and the info is returned normally.
func TestCreateForTenant_AddFollowerNodeErrorIsNonFatal(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{err: errors.New("shard not yet started")}
	svc := newNodeAddDatabaseService(t, leaderNodeID, host, nodeAddNodes())

	cfg := testDBConfig("events")
	cfg.Replicas = 3
	info, err := svc.CreateForTenant(nodeAddCtx(), cfg)
	if err != nil {
		t.Fatalf("CreateForTenant must succeed even when AddFollowerNode errors, got: %v", err)
	}
	if info == nil {
		t.Fatal("DatabaseInfo must not be nil on success")
	}
	// Calls were still attempted — error did not suppress them.
	if len(host.calls) != 2 {
		t.Errorf("expected 2 AddFollowerNode attempts, got %d", len(host.calls))
	}
}

// TestCreateForTenant_AddFollowerNodeSkippedWhenNotLeader verifies that when
// the local node is a follower (not the leader in the placement) no
// AddFollowerNode calls are made: only the leader performs this step.
func TestCreateForTenant_AddFollowerNodeSkippedWhenNotLeader(t *testing.T) {
	t.Parallel()

	// "node2" and "node3" exist in the topology; the scheduler picks "node1"
	// as leader. Setting localNodeID to "node2" means we are a follower.
	host := &fakeRaftHost{}
	svc := newNodeAddDatabaseService(t, "node2", host, nodeAddNodes())

	cfg := testDBConfig("analytics")
	cfg.Replicas = 3
	if _, err := svc.CreateForTenant(nodeAddCtx(), cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if len(host.calls) != 0 {
		t.Errorf("expected 0 AddFollowerNode calls for follower node, got %d", len(host.calls))
	}
}

// TestCreateForTenant_AddFollowerNodeSkippedWhenLocalNodeIDEmpty verifies
// that when localNodeID is empty (single-node / non-clustered deployment) the
// AddFollowerNode step is silently skipped.
func TestCreateForTenant_AddFollowerNodeSkippedWhenLocalNodeIDEmpty(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{}
	svc := newNodeAddDatabaseService(t, "" /* empty */, host, nodeAddNodes())

	cfg := testDBConfig("metrics")
	cfg.Replicas = 3
	if _, err := svc.CreateForTenant(nodeAddCtx(), cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if len(host.calls) != 0 {
		t.Errorf("expected 0 calls when localNodeID is empty, got %d", len(host.calls))
	}
}

// TestCreateForTenant_AddFollowerNodeSingleReplica verifies that a
// single-replica database produces no AddFollowerNode calls: there are no
// followers to add.
func TestCreateForTenant_AddFollowerNodeSingleReplica(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{}
	// One-node topology — only "node1" available so RF is clamped to 1.
	svc := newNodeAddDatabaseService(t, leaderNodeID, host, healthyNodes("node1"))

	cfg := testDBConfig("singleton")
	cfg.Replicas = 1
	if _, err := svc.CreateForTenant(nodeAddCtx(), cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if len(host.calls) != 0 {
		t.Errorf("expected 0 calls for single-replica database, got %d", len(host.calls))
	}
}

// TestSetRaftHost_PostConstruction verifies that SetRaftHost successfully
// injects the handle after construction, and that a subsequent
// CreateForTenant triggers the follower add calls.
func TestSetRaftHost_PostConstruction(t *testing.T) {
	t.Parallel()

	// Build service without raftHost.
	svc := newNodeAddDatabaseService(t, leaderNodeID, nil /* no raftHost */, nodeAddNodes())

	// Assert to *DatabaseService for SetRaftHost.
	concrete, ok := svc.(*DatabaseService)
	if !ok {
		t.Fatal("NewDatabaseService must return *DatabaseService")
	}

	host := &fakeRaftHost{}
	concrete.SetRaftHost(host)

	cfg := testDBConfig("inject-test")
	cfg.Replicas = 3
	if _, err := svc.CreateForTenant(nodeAddCtx(), cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if len(host.calls) != 2 {
		t.Errorf("AddFollowerNode called %d times after SetRaftHost, want 2", len(host.calls))
	}
}

// TestCreateForTenant_AddFollowerNodePassesCorrectReplicaIDs verifies that
// the replicaID and raftAddr forwarded to AddFollowerNode match the placement
// replica entries (not the local leader's values).
func TestCreateForTenant_AddFollowerNodePassesCorrectReplicaIDs(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{}
	nodes := nodeAddNodes() // node1 (leader), node2, node3
	svc := newNodeAddDatabaseService(t, leaderNodeID, host, nodes)

	// Separately compute the expected placement to have ground-truth replica IDs.
	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	scheduler, err := placement.NewScheduler(&staticTopology{nodes: nodes})
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}

	cfg := testDBConfig("replicacheck")
	cfg.Replicas = 3
	info, createErr := svc.CreateForTenant(nodeAddCtx(), cfg)
	if createErr != nil {
		t.Fatalf("CreateForTenant: %v", createErr)
	}

	// Retrieve the persisted placement by placing the same request the service
	// would have computed to verify the call arguments.
	plan, planErr := scheduler.Place(context.Background(), placement.PlaceRequest{
		TenantID: "nodeadd-tenant",
		Database: "replicacheck",
		ShardID:  info.ShardID,
		RF:       3,
	})
	if planErr != nil {
		t.Fatalf("reference Place: %v", planErr)
	}
	if putErr := store.Put(context.Background(), plan); putErr != nil {
		t.Fatalf("reference store.Put: %v", putErr)
	}

	// Build expected follower set from the reference placement.
	var expectedFollowers []placement.Replica
	for _, r := range plan.Replicas {
		if r.NodeID != leaderNodeID {
			expectedFollowers = append(expectedFollowers, r)
		}
	}
	if len(expectedFollowers) != 2 {
		t.Fatalf("expected 2 reference followers, got %d", len(expectedFollowers))
	}

	if len(host.calls) != 2 {
		t.Fatalf("AddFollowerNode called %d times, want 2", len(host.calls))
	}

	// Build a map from replicaID→raftAddr from the captured calls.
	gotByID := make(map[uint64]string, len(host.calls))
	for _, c := range host.calls {
		gotByID[c.replicaID] = c.raftAddr
	}
	for _, r := range expectedFollowers {
		addr, ok := gotByID[r.ReplicaID]
		if !ok {
			t.Errorf("replicaID %d not found in AddFollowerNode calls", r.ReplicaID)
			continue
		}
		if addr != r.RaftAddr {
			t.Errorf("replicaID %d: raftAddr = %q, want %q", r.ReplicaID, addr, r.RaftAddr)
		}
	}
}
