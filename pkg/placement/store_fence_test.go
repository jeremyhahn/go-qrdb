// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package placement_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// fenceGuardedKV is a faithful in-memory double of the system KV the
// placement store runs over in production (lifecycle's
// guardedKVStoreAdapter): plain Put/Get/Delete/Scan, the single-key
// CompareAndSwap and PutIfAbsent capabilities, and PutAllIfAllUnchanged,
// which evaluates every guard and applies every write under one lock (a
// nil guard value means "the key must be absent"). beforeBatch runs inside
// that lock, before the guards are evaluated: it models a write another
// proposal committed between the caller's read and its guarded write.
type fenceGuardedKV struct {
	mu          sync.Mutex
	data        map[string][]byte
	beforeBatch func(data map[string][]byte)
	batches     int
	plainPuts   int
}

func newFenceGuardedKV() *fenceGuardedKV {
	return &fenceGuardedKV{data: map[string][]byte{}}
}

func (k *fenceGuardedKV) Put(_ context.Context, key string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.plainPuts++
	k.data[key] = append([]byte(nil), value...)
	return nil
}

func (k *fenceGuardedKV) Get(_ context.Context, key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.data[key]
	if !ok {
		return nil, nil
	}
	return append([]byte(nil), v...), nil
}

func (k *fenceGuardedKV) Delete(_ context.Context, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.data, key)
	return nil
}

func (k *fenceGuardedKV) Scan(_ context.Context, prefix string, fn func(string, []byte) error) error {
	k.mu.Lock()
	snap := map[string][]byte{}
	for key, v := range k.data {
		if strings.HasPrefix(key, prefix) {
			snap[key] = append([]byte(nil), v...)
		}
	}
	k.mu.Unlock()
	for key, v := range snap {
		if err := fn(key, v); err != nil {
			return err
		}
	}
	return nil
}

func (k *fenceGuardedKV) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !bytes.Equal(k.data[key], expected) {
		return false, nil
	}
	k.data[key] = append([]byte(nil), next...)
	return true, nil
}

func (k *fenceGuardedKV) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.data[key]; ok {
		return false, nil
	}
	k.data[key] = append([]byte(nil), value...)
	return true, nil
}

func (k *fenceGuardedKV) PutAllIfAllUnchanged(_ context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.batches++
	if k.beforeBatch != nil {
		k.beforeBatch(k.data)
	}
	for _, g := range guards {
		v, ok := k.data[g.Key]
		if g.Value == nil {
			if ok {
				return false, nil
			}
			continue
		}
		if !ok || !bytes.Equal(v, g.Value) {
			return false, nil
		}
	}
	for _, w := range writes {
		if w.Delete {
			delete(k.data, w.Key)
			continue
		}
		k.data[w.Key] = append([]byte(nil), w.Value...)
	}
	return true, nil
}

func (k *fenceGuardedKV) set(key string, value []byte) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.data[key] = value
}

func (k *fenceGuardedKV) raw(key string) []byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.data[key]
}

// plainOnlyKV is a KV with the single-key capabilities but no guarded
// batch: a fence configured over it cannot be honoured atomically.
type plainOnlyKV struct{ inner *fenceGuardedKV }

func (p plainOnlyKV) Put(ctx context.Context, key string, value []byte) error {
	return p.inner.Put(ctx, key, value)
}
func (p plainOnlyKV) Get(ctx context.Context, key string) ([]byte, error) {
	return p.inner.Get(ctx, key)
}
func (p plainOnlyKV) Delete(ctx context.Context, key string) error {
	return p.inner.Delete(ctx, key)
}
func (p plainOnlyKV) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	return p.inner.Scan(ctx, prefix, fn)
}
func (p plainOnlyKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return p.inner.CompareAndSwap(ctx, key, expected, next)
}
func (p plainOnlyKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return p.inner.PutIfAbsent(ctx, key, value)
}

const fenceTestPrefix = "__test:moves/"

func fenceTestKey(tenantID string) string { return fenceTestPrefix + tenantID }

// fenceTestRefusalError is the injected refusal: production injects a
// *volume.TenantMovingReplicaError constructor; placement knows neither.
type fenceTestRefusalError struct {
	TenantID string
	Fence    string
}

func (e *fenceTestRefusalError) Error() string { return "tenant " + e.TenantID + " is moving" }

func newFencedStore(t *testing.T, kv placement.KV) *placement.Store {
	t.Helper()
	s, err := placement.NewStore(kv, placement.WithReplicaAddFence(fenceTestKey, func(tenantID string, fence []byte) error {
		return &fenceTestRefusalError{TenantID: tenantID, Fence: string(fence)}
	}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func fencePlacement(nodes ...string) *placement.Placement {
	p := &placement.Placement{TenantID: "t1", Database: "db1", ShardID: 7, RF: len(nodes)}
	for i, n := range nodes {
		p.Replicas = append(p.Replicas, placement.Replica{ReplicaID: uint64(i + 1), NodeID: n, RaftAddr: n + ":1"})
	}
	if len(nodes) > 0 {
		p.LeaderReplicaID = 1
	}
	return p
}

func withReplica(p *placement.Placement, node string, state placement.ReplicaState) *placement.Placement {
	b, _ := json.Marshal(p)
	var c placement.Placement
	_ = json.Unmarshal(b, &c)
	c.Replicas = append(c.Replicas, placement.Replica{ReplicaID: uint64(len(c.Replicas) + 1), NodeID: node, RaftAddr: node + ":1", State: state})
	return &c
}

func seedPlacement(t *testing.T, kv *fenceGuardedKV, p *placement.Placement) {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	kv.set(placement.PlacementPrefix+p.TenantID+"/"+p.Database, b)
}

func placementRaw(kv *fenceGuardedKV, p *placement.Placement) []byte {
	return kv.raw(placement.PlacementPrefix + p.TenantID + "/" + p.Database)
}

func requireFenceRefusal(t *testing.T, err error) {
	t.Helper()
	var refusal *fenceTestRefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want the injected *fenceTestRefusalError", err)
	}
	if refusal.TenantID != "t1" || refusal.Fence != "journal" {
		t.Fatalf("refusal = %+v, want tenant t1 with the stored journal", refusal)
	}
}

func TestStoreFence_ReplicaAddingCASRefusedWhileJournalled(t *testing.T) {
	ctx := context.Background()
	kv := newFenceGuardedKV()
	s := newFencedStore(t, kv)
	cur := fencePlacement("n1")
	seedPlacement(t, kv, cur)
	before := placementRaw(kv, cur)
	kv.set(fenceTestKey("t1"), []byte("journal"))

	swapped, err := s.CompareAndSwapPlacement(ctx, cur, withReplica(cur, "n2", placement.ReplicaStateObserver))
	requireFenceRefusal(t, err)
	if swapped {
		t.Fatal("swapped = true, want the replica add refused")
	}
	if !bytes.Equal(placementRaw(kv, cur), before) {
		t.Fatal("placement changed under a refused replica add")
	}
	if got := string(kv.raw(fenceTestKey("t1"))); got != "journal" {
		t.Fatalf("fence key = %q, the store must never write it", got)
	}
}

// TestStoreFence_JournalAdmittedBetweenReadAndWriteRefusedAtomically is the
// round 4 P1 3 / round 5 P1 2 interleaving: the caller read the placement
// (and passed every early MoveFence read) before the journal was admitted;
// the journal is committed between that read and the placement write. The
// write must see it, in the same atomic batch.
func TestStoreFence_JournalAdmittedBetweenReadAndWriteRefusedAtomically(t *testing.T) {
	ctx := context.Background()
	kv := newFenceGuardedKV()
	s := newFencedStore(t, kv)
	seedPlacement(t, kv, fencePlacement("n1"))
	cur, err := s.Get(ctx, "t1", "db1")
	if err != nil {
		t.Fatal(err)
	}
	before := placementRaw(kv, cur)
	kv.beforeBatch = func(data map[string][]byte) {
		data[fenceTestKey("t1")] = []byte("journal")
	}

	swapped, err := s.CompareAndSwapPlacement(ctx, cur, withReplica(cur, "n2", placement.ReplicaStateObserver))
	requireFenceRefusal(t, err)
	if swapped {
		t.Fatal("swapped = true after the journal was admitted")
	}
	if !bytes.Equal(placementRaw(kv, cur), before) {
		t.Fatal("placement changed after the journal was admitted")
	}
	if kv.batches != 1 {
		t.Fatalf("guarded batches = %d, want the fence in the one placement write", kv.batches)
	}
}

func TestStoreFence_ClaimOfNewPlacementRefusedWhileJournalled(t *testing.T) {
	ctx := context.Background()
	kv := newFenceGuardedKV()
	s := newFencedStore(t, kv)
	kv.set(fenceTestKey("t1"), []byte("journal"))
	p := fencePlacement("n1")

	claimed, existing, err := s.ClaimPlacement(ctx, p)
	requireFenceRefusal(t, err)
	if claimed || existing != nil {
		t.Fatalf("claimed=%v existing=%v, want a refusal", claimed, existing)
	}
	if placementRaw(kv, p) != nil {
		t.Fatal("a refused claim wrote the placement")
	}
}

func TestStoreFence_ClaimStillReportsAnExistingPlacement(t *testing.T) {
	ctx := context.Background()
	kv := newFenceGuardedKV()
	s := newFencedStore(t, kv)
	other := fencePlacement("n9")
	seedPlacement(t, kv, other)

	claimed, existing, err := s.ClaimPlacement(ctx, fencePlacement("n1"))
	if err != nil {
		t.Fatalf("ClaimPlacement: %v", err)
	}
	if claimed || existing == nil || existing.Replicas[0].NodeID != "n9" {
		t.Fatalf("claimed=%v existing=%+v, want the existing placement", claimed, existing)
	}
}

func TestStoreFence_PutOfNewPlacementRefusedWhileJournalled(t *testing.T) {
	ctx := context.Background()
	kv := newFenceGuardedKV()
	s := newFencedStore(t, kv)
	kv.set(fenceTestKey("t1"), []byte("journal"))
	p := fencePlacement("n1")

	requireFenceRefusal(t, s.Put(ctx, p))
	if placementRaw(kv, p) != nil {
		t.Fatal("a refused Put wrote the placement")
	}
}

func TestStoreFence_PutAddingAReplicaRefusedWhileJournalled(t *testing.T) {
	ctx := context.Background()
	kv := newFenceGuardedKV()
	s := newFencedStore(t, kv)
	cur := fencePlacement("n1")
	seedPlacement(t, kv, cur)
	before := placementRaw(kv, cur)
	kv.set(fenceTestKey("t1"), []byte("journal"))

	requireFenceRefusal(t, s.Put(ctx, withReplica(cur, "n2", placement.ReplicaStateActive)))
	if !bytes.Equal(placementRaw(kv, cur), before) {
		t.Fatal("a refused Put changed the placement")
	}
}

// TestStoreFence_WritesAddingNoReplicaAreNotFenced: removals, promotions and
// the drop fence add no replica (departures are the move's SyncMoveNodes
// business), so they commit while the tenant's move is journalled.
func TestStoreFence_WritesAddingNoReplicaAreNotFenced(t *testing.T) {
	ctx := context.Background()
	kv := newFenceGuardedKV()
	s := newFencedStore(t, kv)
	cur := withReplica(fencePlacement("n1", "n2"), "n3", placement.ReplicaStateObserver)
	seedPlacement(t, kv, cur)
	kv.set(fenceTestKey("t1"), []byte("journal"))

	promoted := withReplica(fencePlacement("n1", "n2"), "n3", placement.ReplicaStateActive)
	if swapped, err := s.CompareAndSwapPlacement(ctx, cur, promoted); err != nil || !swapped {
		t.Fatalf("promotion: swapped=%v err=%v, want it unfenced", swapped, err)
	}
	removed := fencePlacement("n1", "n2")
	removed.RF = 2
	if swapped, err := s.CompareAndSwapPlacement(ctx, promoted, removed); err != nil || !swapped {
		t.Fatalf("removal: swapped=%v err=%v, want it unfenced", swapped, err)
	}
	if err := s.Put(ctx, removed); err != nil {
		t.Fatalf("Put of an unchanged replica set: %v, want it unfenced", err)
	}
	if err := s.SetDropping(ctx, "t1", "db1", true); err != nil {
		t.Fatalf("SetDropping: %v, want it unfenced", err)
	}
	got, err := s.Get(ctx, "t1", "db1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Dropping || len(got.Replicas) != 2 {
		t.Fatalf("placement = %+v, want the removal and the drop fence applied", got)
	}
}

func TestStoreFence_ConflictWithoutJournalStillDeclines(t *testing.T) {
	ctx := context.Background()
	kv := newFenceGuardedKV()
	s := newFencedStore(t, kv)
	cur := fencePlacement("n1")
	seedPlacement(t, kv, cur)
	seedPlacement(t, kv, fencePlacement("n1", "n5"))

	swapped, err := s.CompareAndSwapPlacement(ctx, cur, withReplica(cur, "n2", placement.ReplicaStateObserver))
	if err != nil || swapped {
		t.Fatalf("swapped=%v err=%v, want (false, nil) for a changed placement", swapped, err)
	}
}

func TestStoreFence_WithoutGuardedCapabilityFailsClosed(t *testing.T) {
	ctx := context.Background()
	inner := newFenceGuardedKV()
	s := newFencedStore(t, plainOnlyKV{inner: inner})
	cur := fencePlacement("n1")
	seedPlacement(t, inner, cur)
	before := placementRaw(inner, cur)

	_, err := s.CompareAndSwapPlacement(ctx, cur, withReplica(cur, "n2", placement.ReplicaStateObserver))
	var unsupported *placement.AtomicWriteUnsupportedError
	var storeErr *placement.StoreError
	if !errors.As(err, &storeErr) || !errors.As(err, &unsupported) {
		t.Fatalf("CAS error = %v, want a *StoreError wrapping *AtomicWriteUnsupportedError", err)
	}
	if _, _, err := s.ClaimPlacement(ctx, fencePlacement("n1")); !errors.As(err, &unsupported) {
		t.Fatalf("claim error = %v, want *AtomicWriteUnsupportedError", err)
	}
	if err := s.Put(ctx, withReplica(cur, "n2", placement.ReplicaStateActive)); !errors.As(err, &unsupported) {
		t.Fatalf("put error = %v, want *AtomicWriteUnsupportedError", err)
	}
	if !bytes.Equal(placementRaw(inner, cur), before) || inner.plainPuts != 0 {
		t.Fatal("a fail-closed write reached the KV")
	}
}

func TestStoreFence_WriteSucceedsAfterJournalDeleted(t *testing.T) {
	ctx := context.Background()
	kv := newFenceGuardedKV()
	s := newFencedStore(t, kv)
	cur := fencePlacement("n1")
	seedPlacement(t, kv, cur)
	kv.set(fenceTestKey("t1"), []byte("journal"))
	next := withReplica(cur, "n2", placement.ReplicaStateObserver)
	if _, err := s.CompareAndSwapPlacement(ctx, cur, next); err == nil {
		t.Fatal("want the add refused while journalled")
	}
	if err := kv.Delete(ctx, fenceTestKey("t1")); err != nil {
		t.Fatal(err)
	}

	swapped, err := s.CompareAndSwapPlacement(ctx, cur, next)
	if err != nil || !swapped {
		t.Fatalf("after the move ended: swapped=%v err=%v, want the add committed", swapped, err)
	}
	if _, err := s.Get(ctx, "t1", "db1"); err != nil {
		t.Fatal(err)
	}
	if kv.raw(fenceTestKey("t1")) != nil {
		t.Fatal("the store wrote the fence key")
	}
}
