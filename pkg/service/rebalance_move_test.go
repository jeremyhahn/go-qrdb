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
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeKeyDeliverer is a test double for RebalanceKeyDeliverer.
type fakeKeyDeliverer struct {
	deliverFn func(ctx context.Context, p *placement.Placement, barrier *seal.StorageBarrier, selfNodeID string, autoUnseal bool) error
}

func (f *fakeKeyDeliverer) DistributeTenantKey(ctx context.Context, p *placement.Placement, barrier *seal.StorageBarrier, selfNodeID string, autoUnseal bool) error {
	if f.deliverFn != nil {
		return f.deliverFn(ctx, p, barrier, selfNodeID, autoUnseal)
	}
	return nil
}

// fakeBarrierLookup is a test double for RebalanceBarrierLookup.
type fakeBarrierLookup struct {
	getFn func(tenantID string) (*seal.TenantBarrier, error)
}

func (f *fakeBarrierLookup) GetTenant(tenantID string) (*seal.TenantBarrier, error) {
	if f.getFn != nil {
		return f.getFn(tenantID)
	}
	return nil, errors.New("no barrier configured")
}

// newTestPlacementStore creates an in-memory *placement.Store pre-seeded with
// the provided placements.
func newTestPlacementStore(t *testing.T, placements ...*placement.Placement) *placement.Store {
	t.Helper()
	kv := newMemPlacementKV()
	store, err := placement.NewStore(kv)
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	for _, p := range placements {
		if err := store.Put(context.Background(), p); err != nil {
			t.Fatalf("placement.Store.Put: %v", err)
		}
	}
	return store
}

// newTestPlacement creates a minimal 2-replica Placement for (tenant, db).
func newTestPlacement(tenantID, database string, shardID uint64) *placement.Placement {
	return &placement.Placement{
		TenantID:        tenantID,
		Database:        database,
		ShardID:         shardID,
		RF:              2,
		LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "node-a:9100", RaftAddr: "node-a:9100", DataRoot: "/data"},
			{ReplicaID: 2, NodeID: "node-b:9100", RaftAddr: "node-b:9100", DataRoot: "/data"},
		},
	}
}

// hostForMove returns a mockHost configured for a successful GENERIC
// (system-shard) rebalance move: RequestAddNode returns success. Dedicated-
// tenant MOVE tests instead construct a *fakeRaftHost directly --
// moveTenantGroupShard no longer calls RequestAddNode/RequestRemoveNode on
// the generic Host.
func hostForMove(t *testing.T) *mockHost {
	t.Helper()
	return &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{}, nil
		},
		requestAddNodeFn: func(_ context.Context, _ uint64, _ uint64, _ string) (*quicraft.RequestState, error) {
			return newCompletedRequestState(quicraft.RequestResult{}), nil
		},
		requestRemoveNodeFn: func(_ context.Context, _ uint64, _ uint64) (*quicraft.RequestState, error) {
			return newCompletedRequestState(quicraft.RequestResult{}), nil
		},
	}
}

// ---------------------------------------------------------------------------
// NewRebalanceService with options
// ---------------------------------------------------------------------------

func TestRebalanceService_NewRebalanceService_WithOptions_ReturnsNonNil(t *testing.T) {
	store := newTestPlacementStore(t)
	svc := NewRebalanceService(&mockHost{},
		WithRebalancePlacementStore(store),
		WithRebalanceSelfNodeID("self:9100"),
	)
	if svc == nil {
		t.Fatal("expected non-nil RebalanceServicer")
	}
}

func TestRebalanceService_NewRebalanceService_NoOptions_GenericPathUnchanged(t *testing.T) {
	// Calling NewRebalanceService without options must not panic and must behave
	// identically to the original constructor.
	svc := NewRebalanceService(&mockHost{})
	if svc == nil {
		t.Fatal("expected non-nil RebalanceServicer")
	}
}

// ---------------------------------------------------------------------------
// SetRebalanceDeps
// ---------------------------------------------------------------------------

func TestRebalanceService_SetRebalanceDeps_InjectsFields(t *testing.T) {
	host := &mockHost{}
	svc := NewRebalanceService(host)
	concrete, ok := svc.(*RebalanceService)
	if !ok {
		t.Fatal("NewRebalanceService must return *RebalanceService")
	}

	store := newTestPlacementStore(t)
	deliverer := &fakeKeyDeliverer{}
	lookup := &fakeBarrierLookup{}

	concrete.SetRebalanceDeps(store, deliverer, lookup, "self:9100")

	if concrete.placementStore != store {
		t.Error("placementStore not set by SetRebalanceDeps")
	}
	if concrete.keyDeliverer != deliverer {
		t.Error("keyDeliverer not set by SetRebalanceDeps")
	}
	if concrete.barrierLookup != lookup {
		t.Error("barrierLookup not set by SetRebalanceDeps")
	}
	if concrete.selfNodeID != "self:9100" {
		t.Errorf("selfNodeID = %q, want self:9100", concrete.selfNodeID)
	}
}

func TestRebalanceService_SetRebalanceDeps_NilArgumentsAccepted(t *testing.T) {
	svc := NewRebalanceService(&mockHost{})
	concrete := svc.(*RebalanceService)
	// Nil arguments must not panic and must zero the fields.
	concrete.SetRebalanceDeps(nil, nil, nil, "")
	if concrete.placementStore != nil {
		t.Error("expected nil placementStore")
	}
	if concrete.keyDeliverer != nil {
		t.Error("expected nil keyDeliverer")
	}
}

// ---------------------------------------------------------------------------
// buildShardPlacementIndex
// ---------------------------------------------------------------------------

func TestBuildShardPlacementIndex_NilStore_ReturnsNil(t *testing.T) {
	svc := &RebalanceService{host: &mockHost{}}
	idx := svc.buildShardPlacementIndex(context.Background())
	if idx != nil {
		t.Fatalf("expected nil index with nil store, got %v", idx)
	}
}

func TestBuildShardPlacementIndex_PopulatedStore_IndexesByShardID(t *testing.T) {
	p1 := newTestPlacement("tenant-1", "db1", 42)
	p2 := newTestPlacement("tenant-2", "db2", 99)
	store := newTestPlacementStore(t, p1, p2)
	svc := &RebalanceService{host: &mockHost{}, placementStore: store}

	idx := svc.buildShardPlacementIndex(context.Background())
	if len(idx) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(idx))
	}
	if idx[42] == nil {
		t.Error("expected placement for shard 42")
	}
	if idx[99] == nil {
		t.Error("expected placement for shard 99")
	}
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

func TestFindReplicaByAddr_FindsNodeID(t *testing.T) {
	p := newTestPlacement("t", "db", 1)
	r, ok := findReplicaByAddr(p, "node-a:9100")
	if !ok {
		t.Fatal("expected to find node-a:9100 by NodeID")
	}
	if r.ReplicaID != 1 {
		t.Fatalf("expected ReplicaID 1, got %d", r.ReplicaID)
	}
}

func TestFindReplicaByAddr_FindsRaftAddr(t *testing.T) {
	p := newTestPlacement("t", "db", 1)
	r, ok := findReplicaByAddr(p, "node-b:9100")
	if !ok {
		t.Fatal("expected to find node-b:9100 by RaftAddr")
	}
	if r.ReplicaID != 2 {
		t.Fatalf("expected ReplicaID 2, got %d", r.ReplicaID)
	}
}

func TestFindReplicaByAddr_NotFound(t *testing.T) {
	p := newTestPlacement("t", "db", 1)
	_, ok := findReplicaByAddr(p, "node-z:9100")
	if ok {
		t.Fatal("expected not-found for unknown node")
	}
}

func TestIsPlacementMember_FindsByNodeID(t *testing.T) {
	p := newTestPlacement("t", "db", 1)
	if !isPlacementMember(p, "node-a:9100") {
		t.Fatal("expected node-a:9100 to be recognized as a member")
	}
}

func TestIsPlacementMember_UnknownNodeIsNotAMember(t *testing.T) {
	p := newTestPlacement("t", "db", 1)
	if isPlacementMember(p, "node-z:9100") {
		t.Fatal("expected node-z:9100 to NOT be recognized as a member")
	}
}

func TestAnyMemberAddrExcept_SkipsExcluded(t *testing.T) {
	p := newTestPlacement("t", "db", 1)
	addr, ok := anyMemberAddrExcept(p, "node-a:9100")
	if !ok {
		t.Fatal("expected an alternative member")
	}
	if addr != "node-b:9100" {
		t.Errorf("addr = %q, want node-b:9100", addr)
	}
}

func TestAnyMemberAddrExcept_NoAlternativeWhenSoleReplicaIsExcluded(t *testing.T) {
	p := &placement.Placement{
		Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "node-a:9100", RaftAddr: "node-a:9100"}},
	}
	if _, ok := anyMemberAddrExcept(p, "node-a:9100"); ok {
		t.Fatal("expected no alternative when the sole replica is excluded")
	}
}

func TestPlacementMaxReplicaID_ReturnsMax(t *testing.T) {
	p := newTestPlacement("t", "db", 1)
	if max := placementMaxReplicaID(p); max != 2 {
		t.Fatalf("expected max 2, got %d", max)
	}
}

func TestPlacementMaxReplicaID_EmptyPlacement(t *testing.T) {
	p := &placement.Placement{}
	if max := placementMaxReplicaID(p); max != 0 {
		t.Fatalf("expected 0 for empty placement, got %d", max)
	}
}

func TestClonePlacementWithReplica_AddsReplicaAndUpdatesRF(t *testing.T) {
	p := newTestPlacement("t", "db", 1)
	newR := placement.Replica{ReplicaID: 3, NodeID: "node-c:9100", RaftAddr: "node-c:9100", DataRoot: "/data"}
	cloned := clonePlacementWithReplica(p, newR)

	if cloned.RF != 3 {
		t.Fatalf("expected RF 3, got %d", cloned.RF)
	}
	if len(cloned.Replicas) != 3 {
		t.Fatalf("expected 3 replicas, got %d", len(cloned.Replicas))
	}
	// Original must be unchanged.
	if p.RF != 2 {
		t.Fatalf("original RF modified, got %d", p.RF)
	}
}

func TestDropReplicaFromPlacement_RemovesReplicaAndUpdatesRF(t *testing.T) {
	p := newTestPlacement("t", "db", 1)
	result := dropReplicaFromPlacement(p, 1)

	if result.RF != 1 {
		t.Fatalf("expected RF 1, got %d", result.RF)
	}
	if len(result.Replicas) != 1 {
		t.Fatalf("expected 1 replica, got %d", len(result.Replicas))
	}
	if result.Replicas[0].ReplicaID != 2 {
		t.Fatalf("expected remaining replica ID 2, got %d", result.Replicas[0].ReplicaID)
	}
	// Original must be unchanged.
	if p.RF != 2 {
		t.Fatalf("original RF modified")
	}
}

func TestDropReplicaFromPlacement_UnknownIDIsNoop(t *testing.T) {
	p := newTestPlacement("t", "db", 1)
	result := dropReplicaFromPlacement(p, 999)
	if result.RF != 2 {
		t.Fatalf("expected RF 2, got %d", result.RF)
	}
}

// ---------------------------------------------------------------------------
// RebalanceMoveError and sourceReplicaNotFoundError
// ---------------------------------------------------------------------------

func TestRebalanceMoveError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("dial timeout")
	e := &RebalanceMoveError{
		ShardID:  7,
		TenantID: "acme",
		Step:     "key-delivery",
		Err:      inner,
	}
	msg := e.Error()
	if msg != "service: rebalance move shard 7 tenant acme failed at key-delivery: dial timeout" {
		t.Fatalf("unexpected message: %s", msg)
	}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap must return inner error")
	}
	if !errors.Is(e, inner) {
		t.Fatal("errors.Is must match through Unwrap")
	}
}

func TestSourceReplicaNotFoundError_Error(t *testing.T) {
	e := &sourceReplicaNotFoundError{ShardID: 5, FromNode: "node-x:9100"}
	msg := e.Error()
	if msg != "rebalance: source replica not found for shard 5 node node-x:9100" {
		t.Fatalf("unexpected message: %s", msg)
	}
}

// ---------------------------------------------------------------------------
// moveTenantGroupShard: source not found
// ---------------------------------------------------------------------------

// TestMoveTenantGroupShard_SourceNotFound uses no raftHost, so resolveMoveLeader
// degrades to local execution (step 0) and the leader-wait (step 1) is skipped
// -- moveTenantGroupShard reaches find-source (step 2) directly, exactly as
// before the observer-first/leader-routing fix.
func TestMoveTenantGroupShard_SourceNotFound(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-a:9100",
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "nonexistent:9100", ToNode: "node-c:9100"}
	err := svc.moveTenantGroupShard(context.Background(), move, p)
	if err == nil {
		t.Fatal("expected error for unknown source node")
	}
	var mve *RebalanceMoveError
	if !errors.As(err, &mve) {
		t.Fatalf("expected *RebalanceMoveError, got %T", err)
	}
	if mve.Step != "find-source" {
		t.Fatalf("expected step find-source, got %q", mve.Step)
	}
	var snfe *sourceReplicaNotFoundError
	if !errors.As(err, &snfe) {
		t.Fatalf("expected wrapped *sourceReplicaNotFoundError, got %T", errors.Unwrap(err))
	}
}

// ---------------------------------------------------------------------------
// moveTenantGroupShard: leader routing
// ---------------------------------------------------------------------------

// TestMoveTenantGroupShard_LeaderRouting_ForwardsWhenNotLeader verifies that
// when the local node is not the dedicated group's current Raft leader,
// moveTenantGroupShard fails fast (no placement mutation, no Raft calls)
// with a *RebalanceMoveError wrapping *NotLeaderError, mirroring
// DatabaseService.ScaleForTenant's resolveScaleLeader forwarding contract so
// the REST layer's tryForwardWrite can proxy the request to the leader.
func TestMoveTenantGroupShard_LeaderRouting_ForwardsWhenNotLeader(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	// node-a is replica 1 (self); the live leader is replica 2 (node-b).
	host := &fakeRaftHost{leaderID: 2, leaderOK: true}
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-a:9100",
		raftHost:       host,
		logger:         testLogger(),
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	err := svc.moveTenantGroupShard(context.Background(), move, p)
	if err == nil {
		t.Fatal("expected NotLeaderError")
	}
	var mve *RebalanceMoveError
	if !errors.As(err, &mve) {
		t.Fatalf("expected *RebalanceMoveError, got %T", err)
	}
	if mve.Step != "leader-routing" {
		t.Fatalf("expected step leader-routing, got %q", mve.Step)
	}
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected wrapped *NotLeaderError, got %T", errors.Unwrap(err))
	}
	if nle.LeaderAddress != "node-b:9100" {
		t.Errorf("LeaderAddress = %q, want node-b:9100", nle.LeaderAddress)
	}

	// No membership change or placement mutation must have been attempted.
	if len(host.observerCalls) != 0 || len(host.removeCalls) != 0 {
		t.Error("no Raft calls expected when forwarding to the leader")
	}
	current, gerr := store.Get(context.Background(), p.TenantID, p.Database)
	if gerr != nil {
		t.Fatalf("store.Get: %v", gerr)
	}
	if len(current.Replicas) != 2 {
		t.Errorf("expected placement unchanged (2 replicas), got %d", len(current.Replicas))
	}
}

// TestMoveTenantGroupShard_LeaderRouting_ExecutesLocallyWhenLeader verifies
// the local node executes the MOVE (no forwarding, no leadership transfer)
// when it is confirmed to be the dedicated group's current Raft leader AND
// the source of the MOVE is some OTHER replica (the ordinary case). See
// TestMoveTenantGroupShard_LeaderRouting_TransfersAwayWhenSelfIsSource for
// the case where the leader is ALSO the MOVE's source. node-b here is also
// exactly the node TestMoveTenantGroupShard_LeaderRouting_NonMemberAvoidsGuessingTheSource
// guesses from a non-member; together the two tests cover both hops of a
// MOVE submitted from outside the dedicated group: hop 1 never guesses the
// source, and hop 2 (this test) executes directly with no further hop.
// TestMoveTenantGroupShard_RejectsDroppingPlacement proves finding 5's fix
// for the rebalance path: a MOVE plan computed against a placement snapshot
// that already shows Dropping=true must be refused before leader routing,
// quota, or any placement mutation -- a database mid-drop is never
// rebalanced.
func TestMoveTenantGroupShard_RejectsDroppingPlacement(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	p.Dropping = true
	store := newTestPlacementStore(t, p)

	host := &fakeRaftHost{leaderID: 2, leaderOK: true}
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-b:9100",
		raftHost:       host,
		logger:         testLogger(),
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	err := svc.moveTenantGroupShard(context.Background(), move, p)
	var mve *RebalanceMoveError
	if !errors.As(err, &mve) {
		t.Fatalf("error = %v, want *RebalanceMoveError", err)
	}
	if mve.Step != "dropping" {
		t.Errorf("Step = %q, want \"dropping\"", mve.Step)
	}
	var de *database.DroppingError
	if !errors.As(err, &de) {
		t.Fatalf("wrapped error = %v, want *database.DroppingError", errors.Unwrap(err))
	}
	if len(host.observerCalls) != 0 || len(host.removeCalls) != 0 {
		t.Error("no AddObserver/RemoveFollowerNode calls expected against a dropping database")
	}
}

func TestMoveTenantGroupShard_LeaderRouting_ExecutesLocallyWhenLeader(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	// node-b is replica 2 (self) and also the live leader; the MOVE's
	// source is node-a (replica 1, NOT self).
	host := &fakeRaftHost{leaderID: 2, leaderOK: true}
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-b:9100",
		raftHost:       host,
		logger:         testLogger(),
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	if err := svc.moveTenantGroupShard(context.Background(), move, p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(host.observerCalls) != 1 {
		t.Errorf("expected 1 AddObserver call, got %d", len(host.observerCalls))
	}
	if len(host.removeCalls) != 1 {
		t.Errorf("expected 1 RemoveFollowerNode call, got %d", len(host.removeCalls))
	}
	if len(host.transferCalls) != 0 {
		t.Errorf("expected 0 RequestLeaderTransfer calls (source is not self), got %d", len(host.transferCalls))
	}
}

// TestMoveTenantGroupShard_LeaderRouting_TransfersAwayWhenSelfIsSource is
// the direct regression test for the bug this test file's ordering
// discovered while it was being written: when the LOCAL node is BOTH the
// dedicated group's current leader AND the MOVE's source, moveTenantGroupShard
// must NOT attempt to remove itself locally (go-quicraft applies a
// self-removing leader's config-change entry to its own log and steps down
// immediately, without necessarily replicating it to any surviving voter
// first -- if the reconciler then tears the removed leader down before that
// entry ever reaches a survivor, the survivor is stranded campaigning
// forever). Instead resolveMoveLeader proactively transfers leadership to
// another voting replica and reports a *NotLeaderError pointing at the NEW
// leader, WITHOUT ever mutating placement or attempting AddObserver, so the
// whole request is safe to forward and retry in full.
func TestMoveTenantGroupShard_LeaderRouting_TransfersAwayWhenSelfIsSource(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	// node-a is replica 1 (self), also the live leader, AND the MOVE's
	// source -- the case that requires a proactive transfer.
	host := &fakeRaftHost{leaderID: 1, leaderOK: true}
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-a:9100",
		raftHost:       host,
		logger:         testLogger(),
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	err := svc.moveTenantGroupShard(context.Background(), move, p)
	if err == nil {
		t.Fatal("expected NotLeaderError")
	}
	var mve *RebalanceMoveError
	if !errors.As(err, &mve) {
		t.Fatalf("expected *RebalanceMoveError, got %T", err)
	}
	if mve.Step != "leader-routing" {
		t.Fatalf("expected step leader-routing, got %q", mve.Step)
	}
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected wrapped *NotLeaderError, got %T", errors.Unwrap(err))
	}
	if nle.LeaderAddress != "node-b:9100" {
		t.Errorf("LeaderAddress = %q, want node-b:9100 (the new leader after transfer)", nle.LeaderAddress)
	}

	// The transfer itself must have been attempted, targeting the only
	// other voting replica.
	if len(host.transferCalls) != 1 || host.transferCalls[0].targetReplicaID != 2 {
		t.Fatalf("transferCalls = %+v, want exactly 1 call targeting replica 2", host.transferCalls)
	}
	// No membership change or placement mutation must have been attempted.
	if len(host.observerCalls) != 0 || len(host.removeCalls) != 0 {
		t.Error("no AddObserver/RemoveFollowerNode calls expected when forwarding after a self-source transfer")
	}
	current, gerr := store.Get(context.Background(), p.TenantID, p.Database)
	if gerr != nil {
		t.Fatalf("store.Get: %v", gerr)
	}
	if len(current.Replicas) != 2 {
		t.Errorf("expected placement unchanged (2 replicas), got %d", len(current.Replicas))
	}
}

// TestMoveTenantGroupShard_LeaderRouting_SelfIsSourceTransferTimeout
// verifies that when the leadership transfer itself times out (the new
// leader never actually takes over), resolveMoveLeader degrades to LOCAL
// execution rather than forwarding to a stale/incorrect address -- mirroring
// resolveGroupLeader's own degrade-to-local behavior when the leader cannot
// be resolved. The subsequent AddObserver/RemoveFollowerNode calls proceed
// (and, in this fake, succeed) since the underlying RaftHost double does not
// itself enforce leader-only semantics.
func TestMoveTenantGroupShard_LeaderRouting_SelfIsSourceTransferTimeout(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	host := &fakeRaftHost{leaderID: 1, leaderOK: true, transferNoop: true}
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-a:9100",
		raftHost:       host,
		logger:         testLogger(),
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	if err := svc.moveTenantGroupShard(context.Background(), move, p); err != nil {
		t.Fatalf("unexpected error (must degrade to local execution): %v", err)
	}
	if len(host.transferCalls) != 1 {
		t.Fatalf("expected exactly 1 RequestLeaderTransfer attempt, got %d", len(host.transferCalls))
	}
}

// ---------------------------------------------------------------------------
// moveTenantGroupShard: leader routing from a node outside the group
// ---------------------------------------------------------------------------

// TestMoveTenantGroupShard_LeaderRouting_NonMemberAvoidsGuessingTheSource is
// the direct regression test for the false "forwarding loop" bug: a MOVE
// submitted to (or forwarded to) a node that holds no replica of the
// dedicated group at all must never be routed to the MOVE's own source
// replica. Guessing the source is the one guess certain to risk a SECOND
// forwarding hop: if that guessed source also happens to be the group's
// current leader, resolveMoveLeader's own self-source transfer (see
// TestMoveTenantGroupShard_LeaderRouting_TransfersAwayWhenSelfIsSource)
// fires on the very next hop and needs a hop of its own to reach the new
// leader -- two hops where forwardToLeader's old single-hop guard rejected
// the second as a false loop. This was observed directly in practice. See
// resolveMoveLeader's doc comment, case 2.
func TestMoveTenantGroupShard_LeaderRouting_NonMemberAvoidsGuessingTheSource(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	// node-z hosts no replica of this group at all.
	host := &fakeRaftHost{leaderID: 1, leaderOK: true}
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-z:9100",
		raftHost:       host,
		logger:         testLogger(),
	}

	// node-a (replica 1) is the MOVE's source; host.leaderID: 1 means node-a
	// would ALSO be reported as the current leader if anyone asked it --
	// precisely the worst case the comment describes. The guess must still
	// land on node-b, never node-a.
	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	err := svc.moveTenantGroupShard(context.Background(), move, p)
	if err == nil {
		t.Fatal("expected NotLeaderError")
	}
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected wrapped *NotLeaderError, got %T", errors.Unwrap(err))
	}
	if nle.LeaderAddress != "node-b:9100" {
		t.Errorf("LeaderAddress = %q, want node-b:9100 (never the MOVE's source)", nle.LeaderAddress)
	}

	// A non-member host has no local Raft state to consult, and none of
	// this must have been attempted while merely picking a forwarding
	// target.
	if len(host.observerCalls) != 0 || len(host.removeCalls) != 0 || len(host.transferCalls) != 0 {
		t.Error("no Raft calls expected when guessing a forwarding target")
	}
}

// TestMoveTenantGroupShard_LeaderRouting_NonMemberFallsBackWhenSourceIsOnlyReplica
// verifies the one case where there is no alternative to guess: an RF=1
// dedicated group, where the sole replica IS the MOVE's source by
// definition. resolveMoveLeader falls back to resolveGroupLeader's ordinary
// guess (that replica itself), unchanged from before this fix -- see
// resolveMoveLeader's doc comment, case 2's final paragraph.
func TestMoveTenantGroupShard_LeaderRouting_NonMemberFallsBackWhenSourceIsOnlyReplica(t *testing.T) {
	p := &placement.Placement{
		TenantID: "acme", Database: "db1", ShardID: 10, RF: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "node-a:9100", RaftAddr: "node-a:9100", DataRoot: "/data"},
		},
	}
	store := newTestPlacementStore(t, p)

	host := &fakeRaftHost{leaderID: 1, leaderOK: true}
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-z:9100",
		raftHost:       host,
		logger:         testLogger(),
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	err := svc.moveTenantGroupShard(context.Background(), move, p)
	if err == nil {
		t.Fatal("expected NotLeaderError")
	}
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected wrapped *NotLeaderError, got %T", errors.Unwrap(err))
	}
	if nle.LeaderAddress != "node-a:9100" {
		t.Errorf("LeaderAddress = %q, want node-a:9100 (no alternative replica exists)", nle.LeaderAddress)
	}
}

// ---------------------------------------------------------------------------
// moveTenantGroupShard: key delivery failure is non-fatal
// ---------------------------------------------------------------------------

// TestMoveTenantGroupShard_KeyDeliveryFailure_NonFatal verifies that, exactly
// like DatabaseService.scaleUp, a key-delivery failure for the MOVE
// destination is logged and does NOT fail the call: the destination's own
// TenantShardReconciler pulls the key independently once its placement view
// catches up. This is a deliberate behavior change from the move's old
// key-delivery-is-fatal semantics, which predates observer-first admission
// and could never have worked anyway once placement-put was correctly
// reordered to precede key delivery (see addObserverReplica's doc comment).
func TestMoveTenantGroupShard_KeyDeliveryFailure_NonFatal(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	deliverErr := errors.New("HPKE seal failed")
	host := &fakeRaftHost{leaderID: 2, leaderOK: true}
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-b:9100",
		raftHost:       host,
		logger:         testLogger(),
		keyDeliverer: &fakeKeyDeliverer{
			deliverFn: func(_ context.Context, _ *placement.Placement, _ *seal.StorageBarrier, _ string, _ bool) error {
				return deliverErr
			},
		},
		barrierLookup: &fakeBarrierLookup{
			getFn: func(_ string) (*seal.TenantBarrier, error) {
				return nil, errors.New("barrier not found")
			},
		},
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	if err := svc.moveTenantGroupShard(context.Background(), move, p); err != nil {
		t.Fatalf("unexpected error (key-delivery failure must be non-fatal): %v", err)
	}

	// The move must still have completed: dst admitted as observer, src
	// evicted, placement fully updated.
	if len(host.observerCalls) != 1 {
		t.Errorf("expected 1 AddObserver call, got %d", len(host.observerCalls))
	}
	if len(host.removeCalls) != 1 {
		t.Errorf("expected 1 RemoveFollowerNode call, got %d", len(host.removeCalls))
	}
	current, getErr := store.Get(context.Background(), p.TenantID, p.Database)
	if getErr != nil {
		t.Fatalf("store.Get: %v", getErr)
	}
	if len(current.Replicas) != 2 {
		t.Fatalf("expected 2 replicas after move, got %d", len(current.Replicas))
	}
}

// ---------------------------------------------------------------------------
// moveTenantGroupShard: AddObserver failure
// ---------------------------------------------------------------------------

// TestMoveTenantGroupShard_AddObserverFailure verifies a failing AddObserver
// fails the whole MOVE with step "add-observer", wrapping the underlying
// error, and that the placement-put for the destination (which happens
// BEFORE AddObserver is attempted) has already been persisted -- mirroring
// DatabaseService.scaleUp's identical AddObserver-failure contract.
func TestMoveTenantGroupShard_AddObserverFailure(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	addErr := errors.New("shard not yet started")
	host := &fakeRaftHost{leaderID: 2, leaderOK: true, observerErr: addErr}
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-b:9100",
		raftHost:       host,
		logger:         testLogger(),
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	err := svc.moveTenantGroupShard(context.Background(), move, p)
	if err == nil {
		t.Fatal("expected error from AddObserver failure")
	}
	var mve *RebalanceMoveError
	if !errors.As(err, &mve) {
		t.Fatalf("expected *RebalanceMoveError, got %T", err)
	}
	if mve.Step != "add-observer" {
		t.Fatalf("expected step add-observer, got %q", mve.Step)
	}
	if !errors.Is(err, addErr) {
		t.Fatal("expected underlying addErr")
	}
	if len(host.observerCalls) != 1 {
		t.Errorf("expected 1 AddObserver attempt, got %d", len(host.observerCalls))
	}
	if len(host.removeCalls) != 0 {
		t.Error("RemoveFollowerNode must not be attempted when AddObserver fails")
	}

	// Placement-put for the destination already committed before the failed
	// AddObserver call.
	current, gerr := store.Get(context.Background(), p.TenantID, p.Database)
	if gerr != nil {
		t.Fatalf("store.Get: %v", gerr)
	}
	if len(current.Replicas) != 3 {
		t.Fatalf("expected dst already persisted (3 replicas), got %d", len(current.Replicas))
	}
}

// ---------------------------------------------------------------------------
// moveTenantGroupShard: add-placement failure
// ---------------------------------------------------------------------------

// TestMoveTenantGroupShard_AddPlacementFailure verifies a failing
// placement-put for the destination fails the whole MOVE with step
// "add-placement" and never attempts AddObserver.
func TestMoveTenantGroupShard_AddPlacementFailure(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)

	kv := &failingPutKV{inner: newMemPlacementKV()}
	store, err := placement.NewStore(kv)
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	if putErr := store.Put(context.Background(), p); putErr != nil {
		t.Fatalf("initial Put: %v", putErr)
	}
	kv.failPut = true

	host := &fakeRaftHost{leaderID: 2, leaderOK: true}
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-b:9100",
		raftHost:       host,
		logger:         testLogger(),
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	err = svc.moveTenantGroupShard(context.Background(), move, p)
	if err == nil {
		t.Fatal("expected error from add-placement failure")
	}
	var mve *RebalanceMoveError
	if !errors.As(err, &mve) {
		t.Fatalf("expected *RebalanceMoveError, got %T", err)
	}
	if mve.Step != "add-placement" {
		t.Fatalf("expected step add-placement, got %q", mve.Step)
	}
	if len(host.observerCalls) != 0 {
		t.Error("AddObserver must not be attempted when the placement-put fails")
	}
}

// failingPutKV wraps memPlacementKV and rejects Put when failPut is set
// (every subsequent call) or when failOnPutN matches the 1-indexed call
// number of this Put invocation, and rejects Scan when failScan is set.
type failingPutKV struct {
	inner      *memPlacementKV
	failPut    bool
	failScan   bool
	putCount   int
	failOnPutN int
}

func (f *failingPutKV) Put(ctx context.Context, key string, value []byte) error {
	f.putCount++
	if f.failPut || (f.failOnPutN > 0 && f.putCount == f.failOnPutN) {
		return errors.New("kv: storage full")
	}
	return f.inner.Put(ctx, key, value)
}

func (f *failingPutKV) Get(ctx context.Context, key string) ([]byte, error) {
	return f.inner.Get(ctx, key)
}

func (f *failingPutKV) Delete(ctx context.Context, key string) error {
	return f.inner.Delete(ctx, key)
}

func (f *failingPutKV) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	if f.failScan {
		return errors.New("kv: scan unavailable")
	}
	return f.inner.Scan(ctx, prefix, fn)
}

// CompareAndSwap applies the identical fault-injection rules as Put: every
// call addObserverReplica/removeReplicaFromGroup make against placement now
// goes through CompareAndSwapPlacement rather than a bare Put, so this
// double's "placement write fails" behavior must be reachable from both.
func (f *failingPutKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	f.putCount++
	if f.failPut || (f.failOnPutN > 0 && f.putCount == f.failOnPutN) {
		return false, errors.New("kv: storage full")
	}
	return f.inner.CompareAndSwap(ctx, key, expected, next)
}

// ---------------------------------------------------------------------------
// moveTenantGroupShard: remove-placement failure
// ---------------------------------------------------------------------------

// TestMoveTenantGroupShard_RemovePlacementFailure verifies a failing
// placement-put for the source removal fails the whole MOVE with step
// "remove-placement", after the destination has already been successfully
// admitted (add-placement + AddObserver both already succeeded).
func TestMoveTenantGroupShard_RemovePlacementFailure(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)

	kv := &failingPutKV{inner: newMemPlacementKV()}
	store, err := placement.NewStore(kv)
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	if putErr := store.Put(context.Background(), p); putErr != nil { // put #1: initial seed
		t.Fatalf("initial Put: %v", putErr)
	}
	kv.failOnPutN = 3 // put #2 (add dst) succeeds; put #3 (remove src) fails.

	host := &fakeRaftHost{leaderID: 2, leaderOK: true}
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-b:9100",
		raftHost:       host,
		logger:         testLogger(),
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	err = svc.moveTenantGroupShard(context.Background(), move, p)
	if err == nil {
		t.Fatal("expected error from remove-placement failure")
	}
	var mve *RebalanceMoveError
	if !errors.As(err, &mve) {
		t.Fatalf("expected *RebalanceMoveError, got %T", err)
	}
	if mve.Step != "remove-placement" {
		t.Fatalf("expected step remove-placement, got %q", mve.Step)
	}
	if len(host.observerCalls) != 1 {
		t.Errorf("expected dst already admitted (1 AddObserver call), got %d", len(host.observerCalls))
	}
	if len(host.removeCalls) != 0 {
		t.Error("RemoveFollowerNode must not be attempted when the remove placement-put fails")
	}
}

// ---------------------------------------------------------------------------
// moveTenantGroupShard: RemoveFollowerNode failure
// ---------------------------------------------------------------------------

// TestMoveTenantGroupShard_RemoveNodeFailure verifies a failing
// RemoveFollowerNode fails the whole MOVE with step "remove-node", but the
// placement already reflects the intended post-move state (dst in, src out)
// -- mirroring DatabaseService.scaleDown's identical contract.
func TestMoveTenantGroupShard_RemoveNodeFailure(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	removeErr := errors.New("config change rejected")
	host := &fakeRaftHost{leaderID: 2, leaderOK: true, removeErr: removeErr}
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-b:9100",
		raftHost:       host,
		logger:         testLogger(),
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	err := svc.moveTenantGroupShard(context.Background(), move, p)
	if err == nil {
		t.Fatal("expected error from RemoveFollowerNode failure")
	}
	var mve *RebalanceMoveError
	if !errors.As(err, &mve) {
		t.Fatalf("expected *RebalanceMoveError, got %T", err)
	}
	if mve.Step != "remove-node" {
		t.Fatalf("expected step remove-node, got %q", mve.Step)
	}
	if !errors.Is(err, removeErr) {
		t.Fatal("expected underlying removeErr")
	}

	// Placement MUST have been updated (dst added, src removed) even though
	// the Raft RemoveFollowerNode call failed.
	current, getErr := store.Get(context.Background(), p.TenantID, p.Database)
	if getErr != nil {
		t.Fatalf("store.Get: %v", getErr)
	}
	if len(current.Replicas) != 2 {
		t.Fatalf("expected 2 replicas after placement update, got %d", len(current.Replicas))
	}
	foundDst := false
	for _, r := range current.Replicas {
		if r.NodeID == "node-c:9100" {
			foundDst = true
		}
		if r.NodeID == "node-a:9100" {
			t.Error("src replica should have been removed from placement")
		}
	}
	if !foundDst {
		t.Error("dst replica should be present in updated placement")
	}
}

// ---------------------------------------------------------------------------
// moveTenantGroupShard: happy path (observer-first, no key delivery configured)
// ---------------------------------------------------------------------------

// TestMoveTenantGroupShard_HappyPath_NoKeyDelivery verifies the full,
// correct MOVE sequence: the destination is admitted via AddObserver (NEVER
// a voter add), the source is evicted via RemoveFollowerNode, and the final
// placement reflects dst-in/src-out with the destination recorded as
// placement.ReplicaStateObserver (promotion to a full voter happens
// asynchronously via TenantShardReconciler, off this request path).
func TestMoveTenantGroupShard_HappyPath_NoKeyDelivery(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	host := &fakeRaftHost{leaderID: 2, leaderOK: true}
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-b:9100",
		raftHost:       host,
		logger:         testLogger(),
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	if err := svc.moveTenantGroupShard(context.Background(), move, p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the destination was admitted as a non-voting OBSERVER, never a
	// voter (host.requestAddNodeFn from hostForMove is never wired to be
	// called by moveTenantGroupShard at all).
	if len(host.observerCalls) != 1 {
		t.Fatalf("AddObserver called %d times, want 1", len(host.observerCalls))
	}
	oc := host.observerCalls[0]
	if oc.shardID != 10 {
		t.Errorf("AddObserver: want shardID 10, got %d", oc.shardID)
	}
	if oc.replicaID != 3 { // max(1,2)+1
		t.Errorf("AddObserver: want replicaID 3, got %d", oc.replicaID)
	}
	if oc.raftAddr != "node-c:9100" {
		t.Errorf("AddObserver: want addr node-c:9100, got %q", oc.raftAddr)
	}

	if len(host.removeCalls) != 1 {
		t.Fatalf("RemoveFollowerNode called %d times, want 1", len(host.removeCalls))
	}
	rc := host.removeCalls[0]
	if rc.shardID != 10 {
		t.Errorf("RemoveFollowerNode: want shardID 10, got %d", rc.shardID)
	}
	if rc.replicaID != 1 { // node-a:9100 is replicaID 1
		t.Errorf("RemoveFollowerNode: want replicaID 1 (src), got %d", rc.replicaID)
	}

	// Verify placement updated: dst in (as an observer), src out.
	current, err := store.Get(context.Background(), p.TenantID, p.Database)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if current.RF != 2 {
		t.Errorf("want RF 2 after move, got %d", current.RF)
	}
	foundDst, foundSrc := false, false
	for _, r := range current.Replicas {
		if r.NodeID == "node-c:9100" {
			foundDst = true
			// DataRoot MUST be empty, never copied from an existing replica
			// (e.g. the source's "/data" fixture value below): the
			// destination resolves its own local volume-pool root via
			// LocalRootResolver, exactly like a scale-up observer
			// (placement.Scheduler.PlaceAdditional). See moveTenantGroupShard's
			// step-3 doc comment.
			if r.DataRoot != "" {
				t.Errorf("dst DataRoot = %q, want empty string", r.DataRoot)
			}
			if r.State != placement.ReplicaStateObserver {
				t.Errorf("dst State = %v, want ReplicaStateObserver", r.State)
			}
		}
		if r.NodeID == "node-a:9100" {
			foundSrc = true
		}
	}
	if !foundDst {
		t.Error("dst node-c:9100 missing from updated placement")
	}
	if foundSrc {
		t.Error("src node-a:9100 still present in updated placement")
	}
}

// TestMoveTenantGroupShard_PlacementBeforeKeyDeliveryBeforeAddObserverOrdering
// verifies the step ordering: the placement update (dst added) is persisted
// BEFORE the tenant barrier key is delivered, which in turn happens before
// the Raft AddObserver call -- reusing the SAME ordering fakes
// (orderingPlacementKV / orderedKeyDistributor) database_service_scale_test.go
// uses to pin scale-up's identical ordering contract, since both now share
// the addObserverReplica helper. This is the direct regression test for the
// ordering defect fixed in moveTenantGroupShard (placement-before-key-
// delivery; the receiver's placement gate rejects a key delivered before its
// own copy of the placement table lists the destination replica).
func TestMoveTenantGroupShard_PlacementBeforeKeyDeliveryBeforeAddObserverOrdering(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)

	log := &stepLog{}
	store, err := placement.NewStore(&orderingPlacementKV{memPlacementKV: newMemPlacementKV(), log: log})
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	if putErr := store.Put(context.Background(), p); putErr != nil {
		t.Fatalf("initial Put: %v", putErr)
	}
	// The initial seed put is irrelevant to this assertion; reset the log.
	log.mu.Lock()
	log.steps = nil
	log.mu.Unlock()

	host := &fakeRaftHost{leaderID: 2, leaderOK: true, onObserver: func() { log.record("add-node") }}
	dist := &orderedKeyDistributor{log: log}
	tb := newTestTenantBarrier(t, "acme")
	lookup := &fakeTenantBarrierLookup{tb: tb}

	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "node-b:9100",
		raftHost:       host,
		logger:         testLogger(),
		keyDeliverer:   dist,
		barrierLookup:  lookup,
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	if err := svc.moveTenantGroupShard(context.Background(), move, p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := log.snapshot()
	// The remove-src placement-put also records "placement-put", so at least
	// 3 steps are expected; only the first 3 (dst admission) are asserted.
	if len(got) < 3 {
		t.Fatalf("step log = %v, want at least 3 steps", got)
	}
	if got[0] != "placement-put" || got[1] != "key-delivery" || got[2] != "add-node" {
		t.Fatalf("step order = %v, want [placement-put key-delivery add-node ...]", got)
	}
}

// ---------------------------------------------------------------------------
// ExecuteRebalance: generic shard unchanged when no placement store
// ---------------------------------------------------------------------------

func TestExecuteRebalance_GenericShard_NoPlacementStore(t *testing.T) {
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{ShardID: 5, Nodes: map[uint64]string{1: "node-a:9100"}},
				},
			}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{}, nil
		},
		requestAddNodeFn: func(_ context.Context, _ uint64, _ uint64, _ string) (*quicraft.RequestState, error) {
			return newCompletedRequestState(quicraft.RequestResult{}), nil
		},
	}

	// No placementStore wired → generic path.
	svc := NewRebalanceService(host)
	result, err := svc.ExecuteRebalance(context.Background(), &RebalancePlanResult{
		Moves: []RebalanceMovePlan{
			{ShardID: 5, FromNode: "node-a:9100", ToNode: "node-b:9100"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MovesCompleted != 1 {
		t.Fatalf("expected 1 completed generic move, got %d", result.MovesCompleted)
	}
	if result.MovesFailed != 0 {
		t.Fatalf("expected 0 failed moves, got %d", result.MovesFailed)
	}
}

// ---------------------------------------------------------------------------
// ExecuteRebalance: dedicated-tenant shard routed through moveTenantGroupShard
// ---------------------------------------------------------------------------

func TestExecuteRebalance_DedicatedTenantShard_SuccessfulMove(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	raftHost := &fakeRaftHost{leaderID: 2, leaderOK: true}
	svc := NewRebalanceService(hostForMove(t),
		WithRebalancePlacementStore(store),
		WithRebalanceSelfNodeID("node-b:9100"),
		WithRebalanceRaftHost(raftHost),
	)
	result, err := svc.ExecuteRebalance(context.Background(), &RebalancePlanResult{
		Moves: []RebalanceMovePlan{
			{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MovesCompleted != 1 {
		t.Fatalf("expected 1 completed tenant move, got %d", result.MovesCompleted)
	}
	if result.MovesFailed != 0 {
		t.Fatalf("expected 0 failed, got %d", result.MovesFailed)
	}
	if len(raftHost.observerCalls) != 1 || len(raftHost.removeCalls) != 1 {
		t.Errorf("expected observer-first admit + remove, got %d observer / %d remove calls",
			len(raftHost.observerCalls), len(raftHost.removeCalls))
	}
}

func TestExecuteRebalance_DedicatedTenantShard_FailureCounted(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	raftHost := &fakeRaftHost{leaderID: 2, leaderOK: true, observerErr: errors.New("add failed")}
	svc := NewRebalanceService(hostForMove(t),
		WithRebalancePlacementStore(store),
		WithRebalanceSelfNodeID("node-b:9100"),
		WithRebalanceRaftHost(raftHost),
	)
	result, err := svc.ExecuteRebalance(context.Background(), &RebalancePlanResult{
		Moves: []RebalanceMovePlan{
			{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MovesFailed != 1 {
		t.Fatalf("expected 1 failed tenant move, got %d", result.MovesFailed)
	}
	if result.MovesCompleted != 0 {
		t.Fatalf("expected 0 completed, got %d", result.MovesCompleted)
	}
}

// ---------------------------------------------------------------------------
// ExecuteRebalance: mixed moves (one tenant, one generic)
// ---------------------------------------------------------------------------

func TestExecuteRebalance_MixedMoves(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	// The generic system-shard path (shard 5) still goes through the
	// generic Host's RequestAddNode; the dedicated-tenant path (shard 10)
	// goes through raftHost's AddObserver/RemoveFollowerNode.
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					// Generic shard 5.
					{ShardID: 5, Nodes: map[uint64]string{1: "node-a:9100"}},
				},
			}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{}, nil
		},
		requestAddNodeFn: func(_ context.Context, _ uint64, _ uint64, _ string) (*quicraft.RequestState, error) {
			return newCompletedRequestState(quicraft.RequestResult{}), nil
		},
	}

	svc := NewRebalanceService(host,
		WithRebalancePlacementStore(store),
		WithRebalanceSelfNodeID("node-b:9100"),
		WithRebalanceRaftHost(&fakeRaftHost{leaderID: 2, leaderOK: true}),
	)
	result, err := svc.ExecuteRebalance(context.Background(), &RebalancePlanResult{
		Moves: []RebalanceMovePlan{
			// Dedicated-tenant move.
			{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"},
			// Generic system-shard move.
			{ShardID: 5, FromNode: "node-a:9100", ToNode: "node-b:9100"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MovesCompleted != 2 {
		t.Fatalf("expected 2 completed moves, got %d", result.MovesCompleted)
	}
	if result.MovesFailed != 0 {
		t.Fatalf("expected 0 failures, got %d", result.MovesFailed)
	}
}

// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// buildShardPlacementIndex: List failure
// ---------------------------------------------------------------------------

func TestBuildShardPlacementIndex_ListFailure_ReturnsNil(t *testing.T) {
	// A KV whose Scan always fails causes Store.List to return an error.
	kv := &failingPutKV{inner: newMemPlacementKV(), failPut: false}
	store, err := placement.NewStore(kv)
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	// Arm scan failure.
	kv.failScan = true

	svc := &RebalanceService{host: &mockHost{}, placementStore: store}
	idx := svc.buildShardPlacementIndex(context.Background())
	if idx != nil {
		t.Fatalf("expected nil index when List fails, got %v", idx)
	}
}

// ---------------------------------------------------------------------------
// deliverKeyToDst: success path
// ---------------------------------------------------------------------------

// TestMoveTenantGroupShard_KeyDeliverySuccess_PropagatesAutoUnsealAndSelfNodeID
// verifies moveTenantGroupShard's key delivery (through the shared
// addObserverReplica -> deliverTenantKeyToReplicaWithRetry ->
// deliverTenantKeyToReplica chain) resolves the tenant barrier and
// correctly propagates autoUnseal and selfNodeID to the deliverer, exactly
// as DatabaseService.scaleUp's identical delivery does. This replaces the
// old TestDeliverKeyToDst_SuccessPath, which exercised a
// RebalanceService.deliverKeyToDst method now superseded by the shared
// addObserverReplica helper.
func TestMoveTenantGroupShard_KeyDeliverySuccess_PropagatesAutoUnsealAndSelfNodeID(t *testing.T) {
	// Build a minimal real TenantBarrier (sealed, StrategyNone) so that
	// Barrier().ActiveStrategy() is callable. A sealed barrier returns "" from
	// ActiveStrategy, which is ≠ StrategySystemBarrier, so autoUnseal = false.
	backend := seal.NewMemoryBackend()
	tb, err := seal.NewTenantBarrier(
		"acme",
		slog.Default(),
		backend,
		seal.BarrierConfig{
			PreferenceOrder: []seal.StrategyID{seal.StrategyNone},
			RootKeyPath:     "root",
		},
		seal.NewNoneStrategy(),
	)
	if err != nil {
		t.Fatalf("NewTenantBarrier: %v", err)
	}

	var capturedAutoUnseal bool
	var capturedSelfNodeID string
	deliverer := &fakeKeyDeliverer{
		deliverFn: func(_ context.Context, _ *placement.Placement, _ *seal.StorageBarrier, selfNodeID string, autoUnseal bool) error {
			capturedAutoUnseal = autoUnseal
			capturedSelfNodeID = selfNodeID
			return nil
		},
	}

	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		// self is node-b (replica 2, the leader); the MOVE's source is
		// node-a (replica 1, NOT self) so this exercises the ordinary path
		// without triggering the self-source leadership transfer (see
		// TestMoveTenantGroupShard_LeaderRouting_TransfersAwayWhenSelfIsSource).
		selfNodeID:   "node-b:9100",
		raftHost:     &fakeRaftHost{leaderID: 2, leaderOK: true},
		logger:       testLogger(),
		keyDeliverer: deliverer,
		barrierLookup: &fakeBarrierLookup{
			getFn: func(_ string) (*seal.TenantBarrier, error) {
				return tb, nil
			},
		},
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	if err := svc.moveTenantGroupShard(context.Background(), move, p); err != nil {
		t.Fatalf("moveTenantGroupShard: %v", err)
	}

	// A sealed barrier (not initialized) returns "" for ActiveStrategy, which is
	// not StrategySystemBarrier → autoUnseal must be false.
	if capturedAutoUnseal {
		t.Error("expected autoUnseal=false for non-system-barrier strategy")
	}
	if capturedSelfNodeID != "node-b:9100" {
		t.Errorf("selfNodeID = %q, want node-b:9100", capturedSelfNodeID)
	}
}

// ---------------------------------------------------------------------------
// WithRebalance* option constructors
// ---------------------------------------------------------------------------

func TestWithRebalancePlacementStore_SetsField(t *testing.T) {
	store := newTestPlacementStore(t)
	svc := &RebalanceService{host: &mockHost{}}
	WithRebalancePlacementStore(store)(svc)
	if svc.placementStore != store {
		t.Error("WithRebalancePlacementStore did not set field")
	}
}

func TestWithRebalanceKeyDeliverer_SetsField(t *testing.T) {
	d := &fakeKeyDeliverer{}
	svc := &RebalanceService{host: &mockHost{}}
	WithRebalanceKeyDeliverer(d)(svc)
	if svc.keyDeliverer != d {
		t.Error("WithRebalanceKeyDeliverer did not set field")
	}
}

func TestWithRebalanceBarrierLookup_SetsField(t *testing.T) {
	lookup := &fakeBarrierLookup{}
	svc := &RebalanceService{host: &mockHost{}}
	WithRebalanceBarrierLookup(lookup)(svc)
	if svc.barrierLookup != lookup {
		t.Error("WithRebalanceBarrierLookup did not set field")
	}
}

func TestWithRebalanceSelfNodeID_SetsField(t *testing.T) {
	svc := &RebalanceService{host: &mockHost{}}
	WithRebalanceSelfNodeID("my-node:9100")(svc)
	if svc.selfNodeID != "my-node:9100" {
		t.Errorf("WithRebalanceSelfNodeID: want my-node:9100, got %q", svc.selfNodeID)
	}
}
