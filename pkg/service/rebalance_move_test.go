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

// hostForMove returns a mockHost configured for a successful dedicated-tenant
// MOVE: RequestAddNode and RequestRemoveNode both return success.
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

func TestPlacementSharedDataRoot_ReturnsFirstReplica(t *testing.T) {
	p := newTestPlacement("t", "db", 1)
	if dr := placementSharedDataRoot(p); dr != "/data" {
		t.Fatalf("expected /data, got %q", dr)
	}
}

func TestPlacementSharedDataRoot_EmptyPlacement(t *testing.T) {
	p := &placement.Placement{}
	if dr := placementSharedDataRoot(p); dr != "" {
		t.Fatalf("expected empty string, got %q", dr)
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

func TestMoveTenantGroupShard_SourceNotFound(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "self:9100",
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
// moveTenantGroupShard: key delivery failure
// ---------------------------------------------------------------------------

func TestMoveTenantGroupShard_KeyDeliveryFailure(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	deliverErr := errors.New("HPKE seal failed")
	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "self:9100",
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
	err := svc.moveTenantGroupShard(context.Background(), move, p)
	if err == nil {
		t.Fatal("expected error from key delivery")
	}
	var mve *RebalanceMoveError
	if !errors.As(err, &mve) {
		t.Fatalf("expected *RebalanceMoveError, got %T", err)
	}
	if mve.Step != "key-delivery" {
		t.Fatalf("expected step key-delivery, got %q", mve.Step)
	}

	// Placement must remain unchanged (no Add or Remove was called).
	current, getErr := store.Get(context.Background(), p.TenantID, p.Database)
	if getErr != nil {
		t.Fatalf("store.Get: %v", getErr)
	}
	if len(current.Replicas) != 2 {
		t.Fatalf("expected placement unchanged (2 replicas), got %d", len(current.Replicas))
	}
}

// ---------------------------------------------------------------------------
// moveTenantGroupShard: RequestAddNode failure
// ---------------------------------------------------------------------------

func TestMoveTenantGroupShard_AddNodeFailure(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	addErr := errors.New("shard not found")
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{}, nil
		},
		requestAddNodeFn: func(_ context.Context, _ uint64, _ uint64, _ string) (*quicraft.RequestState, error) {
			return nil, addErr
		},
	}

	svc := &RebalanceService{
		host:           host,
		placementStore: store,
		selfNodeID:     "self:9100",
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	err := svc.moveTenantGroupShard(context.Background(), move, p)
	if err == nil {
		t.Fatal("expected error from RequestAddNode failure")
	}
	var mve *RebalanceMoveError
	if !errors.As(err, &mve) {
		t.Fatalf("expected *RebalanceMoveError, got %T", err)
	}
	if mve.Step != "add-node" {
		t.Fatalf("expected step add-node, got %q", mve.Step)
	}
	if !errors.Is(err, addErr) {
		t.Fatal("expected underlying addErr")
	}

	// Placement must remain unchanged.
	current, _ := store.Get(context.Background(), p.TenantID, p.Database)
	if len(current.Replicas) != 2 {
		t.Fatalf("expected placement unchanged, got %d replicas", len(current.Replicas))
	}
}

// ---------------------------------------------------------------------------
// moveTenantGroupShard: AddNode result error
// ---------------------------------------------------------------------------

func TestMoveTenantGroupShard_AddNodeResultError(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	resultErr := errors.New("config change rejected")
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{}, nil
		},
		requestAddNodeFn: func(_ context.Context, _ uint64, _ uint64, _ string) (*quicraft.RequestState, error) {
			return newCompletedRequestState(quicraft.RequestResult{Err: resultErr}), nil
		},
	}

	svc := &RebalanceService{
		host:           host,
		placementStore: store,
		selfNodeID:     "self:9100",
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	err := svc.moveTenantGroupShard(context.Background(), move, p)
	if err == nil {
		t.Fatal("expected error from AddNode result failure")
	}
	var mve *RebalanceMoveError
	if !errors.As(err, &mve) {
		t.Fatalf("expected *RebalanceMoveError, got %T", err)
	}
	if mve.Step != "add-node-result" {
		t.Fatalf("expected step add-node-result, got %q", mve.Step)
	}
}

// ---------------------------------------------------------------------------
// moveTenantGroupShard: placement update failure
// ---------------------------------------------------------------------------

func TestMoveTenantGroupShard_PlacementUpdateFailure(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)

	// Use a KV that rejects Put after the first read (pre-seeded) so the
	// placement update fails.
	kv := &failingPutKV{inner: newMemPlacementKV()}
	store, err := placement.NewStore(kv)
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	if putErr := store.Put(context.Background(), p); putErr != nil {
		t.Fatalf("initial Put: %v", putErr)
	}
	// Now arm the failure for subsequent Put calls.
	kv.failPut = true

	svc := &RebalanceService{
		host:           hostForMove(t),
		placementStore: store,
		selfNodeID:     "self:9100",
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	err = svc.moveTenantGroupShard(context.Background(), move, p)
	if err == nil {
		t.Fatal("expected error from placement update failure")
	}
	var mve *RebalanceMoveError
	if !errors.As(err, &mve) {
		t.Fatalf("expected *RebalanceMoveError, got %T", err)
	}
	if mve.Step != "placement-update" {
		t.Fatalf("expected step placement-update, got %q", mve.Step)
	}
}

// failingPutKV wraps memPlacementKV and rejects Put when failPut is set and
// Scan when failScan is set.
type failingPutKV struct {
	inner    *memPlacementKV
	failPut  bool
	failScan bool
}

func (f *failingPutKV) Put(ctx context.Context, key string, value []byte) error {
	if f.failPut {
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

// ---------------------------------------------------------------------------
// moveTenantGroupShard: RequestRemoveNode failure
// ---------------------------------------------------------------------------

func TestMoveTenantGroupShard_RemoveNodeFailure(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	removeErr := errors.New("not the leader")
	host := &mockHost{
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
			return nil, removeErr
		},
	}

	svc := &RebalanceService{
		host:           host,
		placementStore: store,
		selfNodeID:     "self:9100",
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	err := svc.moveTenantGroupShard(context.Background(), move, p)
	if err == nil {
		t.Fatal("expected error from RequestRemoveNode failure")
	}
	var mve *RebalanceMoveError
	if !errors.As(err, &mve) {
		t.Fatalf("expected *RebalanceMoveError, got %T", err)
	}
	if mve.Step != "remove-node" {
		t.Fatalf("expected step remove-node, got %q", mve.Step)
	}

	// Placement MUST have been updated (dst added, src removed) even though
	// the Raft RemoveNode call failed.
	current, getErr := store.Get(context.Background(), p.TenantID, p.Database)
	if getErr != nil {
		t.Fatalf("store.Get: %v", getErr)
	}
	if len(current.Replicas) != 2 {
		t.Fatalf("expected 2 replicas after placement update, got %d", len(current.Replicas))
	}
	// dst (node-c:9100) must be present; src (node-a:9100, replicaID=1) must be gone.
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
// moveTenantGroupShard: RemoveNode result error
// ---------------------------------------------------------------------------

func TestMoveTenantGroupShard_RemoveNodeResultError(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	resultErr := errors.New("membership change in progress")
	host := &mockHost{
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
			return newCompletedRequestState(quicraft.RequestResult{Err: resultErr}), nil
		},
	}

	svc := &RebalanceService{
		host:           host,
		placementStore: store,
		selfNodeID:     "self:9100",
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	err := svc.moveTenantGroupShard(context.Background(), move, p)
	if err == nil {
		t.Fatal("expected error from RemoveNode result failure")
	}
	var mve *RebalanceMoveError
	if !errors.As(err, &mve) {
		t.Fatalf("expected *RebalanceMoveError, got %T", err)
	}
	if mve.Step != "remove-node-result" {
		t.Fatalf("expected step remove-node-result, got %q", mve.Step)
	}
}

// ---------------------------------------------------------------------------
// moveTenantGroupShard: happy path (no key delivery configured)
// ---------------------------------------------------------------------------

func TestMoveTenantGroupShard_HappyPath_NoKeyDelivery(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	var capturedAddShardID, capturedAddReplicaID uint64
	var capturedAddAddr string
	var capturedRemoveShardID, capturedRemoveReplicaID uint64

	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{}, nil
		},
		requestAddNodeFn: func(_ context.Context, shardID, replicaID uint64, addr string) (*quicraft.RequestState, error) {
			capturedAddShardID = shardID
			capturedAddReplicaID = replicaID
			capturedAddAddr = addr
			return newCompletedRequestState(quicraft.RequestResult{}), nil
		},
		requestRemoveNodeFn: func(_ context.Context, shardID, replicaID uint64) (*quicraft.RequestState, error) {
			capturedRemoveShardID = shardID
			capturedRemoveReplicaID = replicaID
			return newCompletedRequestState(quicraft.RequestResult{}), nil
		},
	}

	svc := &RebalanceService{
		host:           host,
		placementStore: store,
		selfNodeID:     "self:9100",
	}

	move := RebalanceMovePlan{ShardID: 10, FromNode: "node-a:9100", ToNode: "node-c:9100"}
	if err := svc.moveTenantGroupShard(context.Background(), move, p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify Raft calls.
	if capturedAddShardID != 10 {
		t.Errorf("RequestAddNode: want shardID 10, got %d", capturedAddShardID)
	}
	if capturedAddReplicaID != 3 { // max(1,2)+1
		t.Errorf("RequestAddNode: want replicaID 3, got %d", capturedAddReplicaID)
	}
	if capturedAddAddr != "node-c:9100" {
		t.Errorf("RequestAddNode: want addr node-c:9100, got %q", capturedAddAddr)
	}
	if capturedRemoveShardID != 10 {
		t.Errorf("RequestRemoveNode: want shardID 10, got %d", capturedRemoveShardID)
	}
	if capturedRemoveReplicaID != 1 { // node-a:9100 is replicaID 1
		t.Errorf("RequestRemoveNode: want replicaID 1 (src), got %d", capturedRemoveReplicaID)
	}

	// Verify placement updated: dst in, src out.
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
			if r.DataRoot != "/data" {
				t.Errorf("dst DataRoot = %q, want /data", r.DataRoot)
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

	host := &mockHost{
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

	svc := NewRebalanceService(host, WithRebalancePlacementStore(store), WithRebalanceSelfNodeID("self:9100"))
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
}

func TestExecuteRebalance_DedicatedTenantShard_FailureCounted(t *testing.T) {
	p := newTestPlacement("acme", "db1", 10)
	store := newTestPlacementStore(t, p)

	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{}, nil
		},
		requestAddNodeFn: func(_ context.Context, _ uint64, _ uint64, _ string) (*quicraft.RequestState, error) {
			return nil, errors.New("add failed")
		},
	}

	svc := NewRebalanceService(host, WithRebalancePlacementStore(store), WithRebalanceSelfNodeID("self:9100"))
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
		requestRemoveNodeFn: func(_ context.Context, _ uint64, _ uint64) (*quicraft.RequestState, error) {
			return newCompletedRequestState(quicraft.RequestResult{}), nil
		},
	}

	svc := NewRebalanceService(host, WithRebalancePlacementStore(store), WithRebalanceSelfNodeID("self:9100"))
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

func TestDeliverKeyToDst_SuccessPath(t *testing.T) {
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
		host:           &mockHost{},
		placementStore: store,
		selfNodeID:     "self:9100",
		keyDeliverer:   deliverer,
		barrierLookup: &fakeBarrierLookup{
			getFn: func(_ string) (*seal.TenantBarrier, error) {
				return tb, nil
			},
		},
	}

	dst := placement.Replica{ReplicaID: 3, NodeID: "node-c:9100", RaftAddr: "node-c:9100", DataRoot: "/data"}
	if err := svc.deliverKeyToDst(context.Background(), p, dst); err != nil {
		t.Fatalf("deliverKeyToDst: %v", err)
	}

	// A sealed barrier (not initialized) returns "" for ActiveStrategy, which is
	// not StrategySystemBarrier → autoUnseal must be false.
	if capturedAutoUnseal {
		t.Error("expected autoUnseal=false for non-system-barrier strategy")
	}
	if capturedSelfNodeID != "self:9100" {
		t.Errorf("selfNodeID = %q, want self:9100", capturedSelfNodeID)
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
