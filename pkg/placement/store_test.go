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

package placement

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeKV is an in-memory KV harness modeled on the membership-store test fake:
// it implements the placement KV substrate and (optionally) the Watcher seam.
type fakeKV struct {
	mu      sync.Mutex
	data    map[string][]byte
	watchCh chan WatchEvent
}

func newFakeKV() *fakeKV { return &fakeKV{data: make(map[string][]byte)} }

func (f *fakeKV) Put(_ context.Context, key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	f.data[key] = cp
	return nil
}

func (f *fakeKV) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[key]
	if !ok {
		return nil, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (f *fakeKV) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, key)
	return nil
}

func (f *fakeKV) Scan(_ context.Context, prefix string, fn func(string, []byte) error) error {
	f.mu.Lock()
	keys := make([]string, 0, len(f.data))
	for k := range f.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	snapshot := make(map[string][]byte, len(keys))
	for _, k := range keys {
		snapshot[k] = f.data[k]
	}
	f.mu.Unlock()
	sort.Strings(keys)
	for _, k := range keys {
		if err := fn(k, snapshot[k]); err != nil {
			return err
		}
	}
	return nil
}

// watchKV embeds fakeKV and adds the Watcher capability.
type watchKV struct {
	*fakeKV
	ch chan WatchEvent
}

func newWatchKV() *watchKV {
	return &watchKV{fakeKV: newFakeKV(), ch: make(chan WatchEvent, 8)}
}

func (w *watchKV) Watch(ctx context.Context, _ string) (<-chan WatchEvent, error) {
	return w.ch, nil
}

// atomicKV embeds fakeKV and adds the AtomicKV capability with a real
// compare-and-swap (guarded by fakeKV's own mutex via the exported
// methods), so tests can exercise CompareAndSwapPlacement/SetDropping's
// CAS path rather than only their bare-Put fallback.
type atomicKV struct {
	*fakeKV
}

func newAtomicKV() *atomicKV { return &atomicKV{fakeKV: newFakeKV()} }

func (a *atomicKV) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	current, ok := a.data[key]
	if !ok || !bytes.Equal(current, expected) {
		return false, nil
	}
	cp := make([]byte, len(next))
	copy(cp, next)
	a.data[key] = cp
	return true, nil
}

// PutIfAbsent gives atomicKV the PutIfAbsentKV capability, so tests can
// exercise ClaimPlacement's create-if-absent path rather than only its
// capability-refusal fallback.
func (a *atomicKV) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.data[key]; ok {
		return false, nil
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	a.data[key] = cp
	return true, nil
}

func samplePlacement(tenant, db string, shardID uint64) *Placement {
	return &Placement{
		TenantID:        tenant,
		DatabaseID:      db,
		Database:        db,
		ShardID:         shardID,
		RF:              2,
		LeaderReplicaID: 1,
		Replicas: []Replica{
			{ReplicaID: 1, NodeID: "n1", RaftAddr: "n1:1", DataRoot: "/d/n1"},
			{ReplicaID: 2, NodeID: "n2", RaftAddr: "n2:1", DataRoot: "/d/n2"},
		},
	}
}

func TestNewStore_NilKV(t *testing.T) {
	if _, err := NewStore(nil); !errors.Is(err, ErrNilKV) {
		t.Fatalf("NewStore(nil) error = %v, want ErrNilKV", err)
	}
}

// TestStore_RoundTrip exercises Put/Get/Delete against the in-memory KV harness.
func TestStore_RoundTrip(t *testing.T) {
	s, err := NewStore(newFakeKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	want := samplePlacement("acme", "orders", 1000)
	if err := s.Put(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ShardID != want.ShardID || got.RF != want.RF || len(got.Replicas) != 2 {
		t.Fatalf("round-trip mismatch: got %+v", got)
	}
	if got.Replicas[1].DataRoot != "/d/n2" {
		t.Errorf("replica data root = %q, want /d/n2", got.Replicas[1].DataRoot)
	}

	if err := s.Delete(ctx, "acme", "orders"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "acme", "orders"); !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("Get after delete error = %v, want ErrPlacementNotFound", err)
	}
}

// TestStore_SetDropping_RoundTrips is the direct unit test for the
// replicated drop fence (go-qrdb#482): setting it persists through Get, and
// clearing it does too, leaving every other field untouched.
func TestStore_SetDropping_RoundTrips(t *testing.T) {
	s, err := NewStore(newAtomicKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	want := samplePlacement("acme", "orders", 1000)
	if err := s.Put(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := s.SetDropping(ctx, "acme", "orders", true); err != nil {
		t.Fatalf("SetDropping(true): %v", err)
	}
	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Dropping {
		t.Error("Dropping = false after SetDropping(true), want true")
	}
	if got.ShardID != want.ShardID || len(got.Replicas) != 2 {
		t.Errorf("SetDropping disturbed unrelated fields: got %+v", got)
	}

	if err := s.SetDropping(ctx, "acme", "orders", false); err != nil {
		t.Fatalf("SetDropping(false): %v", err)
	}
	got, err = s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Dropping {
		t.Error("Dropping = true after SetDropping(false), want false")
	}
}

// TestStore_SetDropping_NoOpWhenAlreadySet pins that a redundant SetDropping
// call (the flag already holds the requested value) still succeeds rather
// than requiring the caller to check first -- important for the retry path,
// where re-marking an already-fenced placement is expected to be a no-op.
func TestStore_SetDropping_NoOpWhenAlreadySet(t *testing.T) {
	s, _ := NewStore(newFakeKV())
	ctx := context.Background()
	want := samplePlacement("acme", "orders", 1000)
	if err := s.Put(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := s.SetDropping(ctx, "acme", "orders", false); err != nil {
		t.Fatalf("SetDropping(false) on a never-fenced entry: %v", err)
	}
	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Dropping {
		t.Error("Dropping = true, want false")
	}
}

// TestStore_SetDropping_NotFound proves there is nothing to fence when no
// placement entry exists yet.
func TestStore_SetDropping_NotFound(t *testing.T) {
	s, _ := NewStore(newFakeKV())
	err := s.SetDropping(context.Background(), "acme", "no-such-db", true)
	if !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("error = %v, want ErrPlacementNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// go-qrdb#482 round-2 finding (3): SetDropping (and any other
// read-modify-write of an existing placement entry) was a bare Put, so a
// concurrent mutation of the SAME entry -- a scale admitting a replica, a
// reconciler promoting one -- could silently overwrite it, in either
// direction: a stale scale-derived Put clearing a just-set Dropping flag,
// or a stale SetDropping clearing a just-persisted replica change.
// CompareAndSwapPlacement/SetDropping now commit through AtomicKV when the
// backing KV provides it.
// ---------------------------------------------------------------------------

// TestStore_CompareAndSwapPlacement_SucceedsOnMatch is the direct positive
// case: expected matches what is stored, so the swap commits.
func TestStore_CompareAndSwapPlacement_SucceedsOnMatch(t *testing.T) {
	s, err := NewStore(newAtomicKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	want := samplePlacement("acme", "orders", 1000)
	if err := s.Put(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	current, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	next := *current
	next.Dropping = true
	swapped, err := s.CompareAndSwapPlacement(ctx, current, &next)
	if err != nil {
		t.Fatalf("CompareAndSwapPlacement: %v", err)
	}
	if !swapped {
		t.Fatal("swapped = false, want true")
	}
	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Dropping {
		t.Error("Dropping = false after a successful CAS, want true")
	}
}

// TestStore_CompareAndSwapPlacement_DeclinesOnStaleExpected is the failing
// half: expected no longer matches what is stored (something else
// committed first), so the swap declines WITHOUT applying next -- and
// without an error, since this is the CAS doing its job, not a failure to
// reach the store.
func TestStore_CompareAndSwapPlacement_DeclinesOnStaleExpected(t *testing.T) {
	s, err := NewStore(newAtomicKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	original := samplePlacement("acme", "orders", 1000)
	if err := s.Put(ctx, original); err != nil {
		t.Fatalf("Put: %v", err)
	}
	stale, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Someone else commits first.
	if err := s.SetDropping(ctx, "acme", "orders", true); err != nil {
		t.Fatalf("SetDropping: %v", err)
	}

	next := *stale
	next.RF = 99
	swapped, err := s.CompareAndSwapPlacement(ctx, stale, &next)
	if err != nil {
		t.Fatalf("CompareAndSwapPlacement: %v", err)
	}
	if swapped {
		t.Fatal("swapped = true, want false: expected no longer matched the stored entry")
	}

	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RF == 99 {
		t.Error("the declined swap's next value was applied anyway")
	}
	if !got.Dropping {
		t.Error("the declined swap discarded the concurrent SetDropping's commit")
	}
}

// TestStore_CompareAndSwapPlacement_UnsupportedWithoutAtomicKV pins the
// capability refusal: a KV backend with no AtomicKV must refuse rather than
// silently falling back to an unconditional Put, which would defeat the
// entire reason a caller chose this over Put.
func TestStore_CompareAndSwapPlacement_UnsupportedWithoutAtomicKV(t *testing.T) {
	s, err := NewStore(newFakeKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	p := samplePlacement("acme", "orders", 1000)
	if err := s.Put(ctx, p); err != nil {
		t.Fatalf("Put: %v", err)
	}

	next := *p
	next.Dropping = true
	_, err = s.CompareAndSwapPlacement(ctx, p, &next)
	if uerr := (*AtomicWriteUnsupportedError)(nil); !errors.As(err, &uerr) {
		t.Fatalf("error = %v, want *AtomicWriteUnsupportedError", err)
	}
}

// TestStore_CompareAndSwapPlacement_Validation pins the same argument
// validation Put itself enforces, applied to next: a nil next, an empty
// TenantID, or an empty Database are all caller errors CompareAndSwapPlacement
// must refuse before ever touching the backing KV.
func TestStore_CompareAndSwapPlacement_Validation(t *testing.T) {
	s, err := NewStore(newAtomicKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	t.Run("nil next", func(t *testing.T) {
		_, err := s.CompareAndSwapPlacement(ctx, nil, nil)
		if !errors.Is(err, ErrPlacementNotFound) {
			t.Fatalf("error = %v, want ErrPlacementNotFound", err)
		}
	})
	t.Run("empty tenant", func(t *testing.T) {
		_, err := s.CompareAndSwapPlacement(ctx, nil, &Placement{DatabaseID: "orders", Database: "orders"})
		if !errors.Is(err, ErrEmptyTenant) {
			t.Fatalf("error = %v, want ErrEmptyTenant", err)
		}
	})
	t.Run("empty database", func(t *testing.T) {
		_, err := s.CompareAndSwapPlacement(ctx, nil, &Placement{TenantID: "acme"})
		if !errors.Is(err, ErrEmptyDatabaseID) {
			t.Fatalf("error = %v, want ErrEmptyDatabaseID", err)
		}
	})
}

// erroringAtomicKV wraps an atomicKV whose CompareAndSwap always fails,
// standing in for a genuine backend error (as opposed to a declined swap).
type erroringAtomicKV struct {
	*atomicKV
	err error
}

func (e *erroringAtomicKV) CompareAndSwap(_ context.Context, _ string, _, _ []byte) (bool, error) {
	return false, e.err
}

// TestStore_CompareAndSwapPlacement_BackendError proves a genuine failure
// from the backing AtomicKV -- not a declined swap -- is wrapped and
// returned as an error, not silently reported as swapped=false.
func TestStore_CompareAndSwapPlacement_BackendError(t *testing.T) {
	boom := errors.New("kv: unavailable")
	s, err := NewStore(&erroringAtomicKV{atomicKV: newAtomicKV(), err: boom})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	p := &Placement{TenantID: "acme", DatabaseID: "orders", Database: "orders", ShardID: 1000, RF: 1}

	_, err = s.CompareAndSwapPlacement(ctx, nil, p)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want to wrap %v", err, boom)
	}
}

// TestStore_ClaimPlacement_CreatesWhenAbsent proves the first claim of a
// (tenant, database) name commits the entry and reports claimed=true with
// no existing value to report.
func TestStore_ClaimPlacement_CreatesWhenAbsent(t *testing.T) {
	s, err := NewStore(newAtomicKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	p := &Placement{TenantID: "acme", DatabaseID: "orders", Database: "orders", Backend: BackendFile, ShardID: 1000, RF: 1}

	claimed, existing, err := s.ClaimPlacement(ctx, p)
	if err != nil {
		t.Fatalf("ClaimPlacement: %v", err)
	}
	if !claimed {
		t.Fatalf("claimed = false, want true for the first claim")
	}
	if existing != nil {
		t.Fatalf("existing = %+v, want nil when claimed", existing)
	}

	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Backend != BackendFile || got.ShardID != 1000 {
		t.Fatalf("stored placement = %+v, want the claimed entry", got)
	}
}

// TestStore_ClaimPlacement_DeclinesAndReturnsExistingOnConflict proves a
// second claim of the same name -- the race a local binding's single
// owner must be decided by -- declines and reports the first claim's own
// entry, never silently overwriting it: a database created on one node
// must resolve to the same binding everywhere, including on the node that
// lost the race.
func TestStore_ClaimPlacement_DeclinesAndReturnsExistingOnConflict(t *testing.T) {
	s, err := NewStore(newAtomicKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	first := &Placement{TenantID: "acme", DatabaseID: "orders", Database: "orders", Backend: BackendFile, ShardID: 1000, RF: 1,
		Replicas: []Replica{{ReplicaID: 1, NodeID: "n1", RaftAddr: "n1:1"}}, LeaderReplicaID: 1}
	claimed, _, err := s.ClaimPlacement(ctx, first)
	if err != nil || !claimed {
		t.Fatalf("first ClaimPlacement: claimed=%v err=%v", claimed, err)
	}

	second := &Placement{TenantID: "acme", DatabaseID: "orders", Database: "orders", Backend: BackendFile, ShardID: 2000, RF: 1,
		Replicas: []Replica{{ReplicaID: 1, NodeID: "n2", RaftAddr: "n2:1"}}, LeaderReplicaID: 1}
	claimed, existing, err := s.ClaimPlacement(ctx, second)
	if err != nil {
		t.Fatalf("second ClaimPlacement: %v", err)
	}
	if claimed {
		t.Fatalf("second claim: claimed = true, want false")
	}
	if existing == nil || existing.Replicas[0].NodeID != "n1" {
		t.Fatalf("existing = %+v, want the first claim's own entry (owner n1)", existing)
	}
}

// TestStore_ClaimPlacement_UnsupportedWithoutPutIfAbsentKV proves a KV
// backend that cannot PutIfAbsent is refused, not silently degraded to an
// unconditional Put -- degrading would let two racing creates both "win".
func TestStore_ClaimPlacement_UnsupportedWithoutPutIfAbsentKV(t *testing.T) {
	s, err := NewStore(newFakeKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	p := &Placement{TenantID: "acme", DatabaseID: "orders", Database: "orders", Backend: BackendFile, ShardID: 1000, RF: 1}

	_, _, err = s.ClaimPlacement(context.Background(), p)
	if uerr := (*AtomicWriteUnsupportedError)(nil); !errors.As(err, &uerr) {
		t.Fatalf("error = %v, want *AtomicWriteUnsupportedError", err)
	}
}

// TestStore_ClaimPlacement_Validation proves a nil placement or an empty
// tenant/database is refused before any store access.
func TestStore_ClaimPlacement_Validation(t *testing.T) {
	s, err := NewStore(newAtomicKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	if _, _, err := s.ClaimPlacement(ctx, nil); !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("nil placement error = %v, want ErrPlacementNotFound", err)
	}
	if _, _, err := s.ClaimPlacement(ctx, &Placement{DatabaseID: "orders", Database: "orders"}); !errors.Is(err, ErrEmptyTenant) {
		t.Fatalf("empty tenant error = %v, want ErrEmptyTenant", err)
	}
	if _, _, err := s.ClaimPlacement(ctx, &Placement{TenantID: "acme"}); !errors.Is(err, ErrEmptyDatabaseID) {
		t.Fatalf("empty database error = %v, want ErrEmptyDatabaseID", err)
	}
}

// erroringPutIfAbsentKV overrides PutIfAbsent to fail, standing in for a
// genuine backend error rather than a declined create.
type erroringPutIfAbsentKV struct {
	*atomicKV
	err error
}

func (e *erroringPutIfAbsentKV) PutIfAbsent(_ context.Context, _ string, _ []byte) (bool, error) {
	return false, e.err
}

// TestStore_ClaimPlacement_BackendError proves a genuine failure from the
// backing PutIfAbsentKV -- not a declined create -- is wrapped and
// returned as an error, not silently reported as claimed=false.
func TestStore_ClaimPlacement_BackendError(t *testing.T) {
	boom := errors.New("kv: unavailable")
	s, err := NewStore(&erroringPutIfAbsentKV{atomicKV: newAtomicKV(), err: boom})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	p := &Placement{TenantID: "acme", DatabaseID: "orders", Database: "orders", Backend: BackendFile, ShardID: 1000, RF: 1}

	_, _, err = s.ClaimPlacement(context.Background(), p)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want to wrap %v", err, boom)
	}
}

// TestStore_ClaimPlacement_GetErrorOnConflict proves a failure reading back
// the winner's entry after losing the race is returned as an error, not
// silently reported as a declined claim with no existing value.
func TestStore_ClaimPlacement_GetErrorOnConflict(t *testing.T) {
	boom := errors.New("kv: unavailable")
	kv := newAtomicKV()
	s, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	first := &Placement{TenantID: "acme", DatabaseID: "orders", Database: "orders", Backend: BackendFile, ShardID: 1000, RF: 1}
	if claimed, _, err := s.ClaimPlacement(ctx, first); err != nil || !claimed {
		t.Fatalf("first ClaimPlacement: claimed=%v err=%v", claimed, err)
	}

	s2, err := NewStore(&erroringGetKV{atomicKV: kv, err: boom})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	second := &Placement{TenantID: "acme", DatabaseID: "orders", Database: "orders", Backend: BackendFile, ShardID: 2000, RF: 1}
	_, _, err = s2.ClaimPlacement(ctx, second)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want to wrap %v", err, boom)
	}
}

// erroringGetKV overrides Get to fail, while leaving PutIfAbsent/CompareAndSwap
// pointed at the shared backing data so a prior claim is still visible.
type erroringGetKV struct {
	*atomicKV
	err error
}

func (e *erroringGetKV) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, e.err
}

// TestStore_SetDropping_SurvivesConcurrentStaleWrite is the exact scenario
// the review named: SetDropping(true) commits, and a stale, scale-derived
// CompareAndSwapPlacement (read BEFORE the fence was set) must be declined
// rather than silently clearing it -- the caller holding the stale read is
// the one that has to notice the decline and retry against the fresh
// value, which is precisely what CompareAndSwapPlacement's (false, nil)
// return exists to make possible.
func TestStore_SetDropping_SurvivesConcurrentStaleWrite(t *testing.T) {
	s, err := NewStore(newAtomicKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	original := samplePlacement("acme", "orders", 1000)
	if err := s.Put(ctx, original); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// The scale path reads the entry BEFORE the drop fence is set --
	// standing in for addObserverReplica/removeReplicaFromGroup's own
	// earlier Get, held across the same window Drop races.
	staleRead, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if err := s.SetDropping(ctx, "acme", "orders", true); err != nil {
		t.Fatalf("SetDropping: %v", err)
	}

	// The scale path now tries to commit its own change against the value
	// it read -- which is stale, because SetDropping committed since then.
	staleNext := *staleRead
	staleNext.RF = 5
	swapped, err := s.CompareAndSwapPlacement(ctx, staleRead, &staleNext)
	if err != nil {
		t.Fatalf("CompareAndSwapPlacement: %v", err)
	}
	if swapped {
		t.Fatal("the stale scale write must be declined, not applied over the fence")
	}

	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Dropping {
		t.Error("Dropping = false after a declined concurrent write, want true: the fence must survive")
	}
	if got.RF == 5 {
		t.Error("the declined scale write's RF was applied anyway")
	}
}

// conflictOnceKV wraps an atomicKV and injects one competing write
// immediately before its FIRST CompareAndSwap call, standing in for a
// concurrent mutation (a scale, a reconciler promotion) that commits in the
// window between SetDropping's own Get and CompareAndSwap. Every
// subsequent call is unaffected, so a caller's retry proceeds normally.
type conflictOnceKV struct {
	*atomicKV
	inject    func()
	triggered bool
}

func (c *conflictOnceKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if !c.triggered {
		c.triggered = true
		if c.inject != nil {
			c.inject()
		}
	}
	return c.atomicKV.CompareAndSwap(ctx, key, expected, next)
}

// TestStore_SetDropping_RetriesOnConflictThenSucceeds proves SetDropping's
// own optimistic loop: an entry that changes between the loop's Get and its
// CompareAndSwapPlacement is retried, not reported as a conflict, as long
// as a later attempt's read-and-swap succeeds -- and the racing write's own
// change survives alongside the fence, since SetDropping's retry re-reads
// the fresh value rather than reapplying its original, now-stale one.
func TestStore_SetDropping_RetriesOnConflictThenSucceeds(t *testing.T) {
	inner := newAtomicKV()
	ctx := context.Background()
	if err := inner.Put(ctx, placementKey("acme", "orders"), mustMarshalPlacement(t, samplePlacement("acme", "orders", 1000))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	kv := &conflictOnceKV{atomicKV: inner}
	kv.inject = func() {
		// The racing write: a scale-style mutation committing directly
		// against the backing KV, bypassing CompareAndSwapPlacement
		// entirely -- exactly as an independent concurrent caller would.
		p, err := (&Store{kv: inner}).Get(ctx, "acme", "orders")
		if err != nil {
			t.Fatalf("racing Get: %v", err)
		}
		p.RF = 7
		if err := inner.Put(ctx, placementKey("acme", "orders"), mustMarshalPlacement(t, p)); err != nil {
			t.Fatalf("racing Put: %v", err)
		}
	}

	s, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s.SetDropping(ctx, "acme", "orders", true); err != nil {
		t.Fatalf("SetDropping: %v", err)
	}
	if !kv.triggered {
		t.Fatal("the conflict injection never ran; this test exercised no race at all")
	}

	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Dropping {
		t.Error("Dropping = false after SetDropping, want true")
	}
	if got.RF != 7 {
		t.Errorf("RF = %d, want 7 (the racing write's change must survive alongside the fence)", got.RF)
	}
}

// mustMarshalPlacement is a small test helper: SetDropping/
// CompareAndSwapPlacement's tests need to write raw bytes directly through
// the backing KV to simulate a racing mutation, bypassing Store's own
// (tenant, database)-validated Put.
func mustMarshalPlacement(t *testing.T, p *Placement) []byte {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

// ---------------------------------------------------------------------------
// go-qrdb#482 round 3, finding 4: TryMarkDropping is the ownership-aware
// primitive dropRaftPlacement uses in place of SetDropping(true), so a
// caller can tell "I just claimed the fence" apart from "someone else
// already holds it" and gate a rollback on that.
// ---------------------------------------------------------------------------

// TestStore_TryMarkDropping_ClaimsAnUnsetFence proves the success path: the
// first call against an unfenced entry transitions it and reports
// owned=true.
func TestStore_TryMarkDropping_ClaimsAnUnsetFence(t *testing.T) {
	s, _ := NewStore(newAtomicKV())
	ctx := context.Background()
	if err := s.Put(ctx, samplePlacement("acme", "orders", 1000)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	owned, err := s.TryMarkDropping(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("TryMarkDropping: %v", err)
	}
	if !owned {
		t.Error("owned = false, want true (the entry was unfenced)")
	}
	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Dropping {
		t.Error("Dropping = false after TryMarkDropping, want true")
	}
}

// TestStore_TryMarkDropping_AlreadySetReportsUnowned proves the fence-already-
// held path: a second call against an already-fenced entry must report
// owned=false with a nil error, and must not disturb the entry.
func TestStore_TryMarkDropping_AlreadySetReportsUnowned(t *testing.T) {
	s, _ := NewStore(newAtomicKV())
	ctx := context.Background()
	if err := s.Put(ctx, samplePlacement("acme", "orders", 1000)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := s.TryMarkDropping(ctx, "acme", "orders"); err != nil {
		t.Fatalf("first TryMarkDropping: %v", err)
	}

	owned, err := s.TryMarkDropping(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("second TryMarkDropping: %v", err)
	}
	if owned {
		t.Error("owned = true, want false (another call already claimed the fence)")
	}
}

// TestStore_TryMarkDropping_NotFound proves there is nothing to claim when no
// placement entry exists.
func TestStore_TryMarkDropping_NotFound(t *testing.T) {
	s, _ := NewStore(newFakeKV())
	owned, err := s.TryMarkDropping(context.Background(), "acme", "no-such-db")
	if !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("error = %v, want ErrPlacementNotFound", err)
	}
	if owned {
		t.Error("owned = true on a not-found entry, want false")
	}
}

// TestStore_TryMarkDropping_RefusesWithoutAtomicKV proves the round-3
// finding 4 fix: a backend with no compare-and-swap must be refused
// outright, not degraded to an unconditional Put -- two concurrent
// TryMarkDropping calls against such a backend could otherwise both observe
// owned=true, exactly the race this primitive exists to prevent.
func TestStore_TryMarkDropping_RefusesWithoutAtomicKV(t *testing.T) {
	s, _ := NewStore(newFakeKV()) // fakeKV implements KV but not AtomicKV.
	ctx := context.Background()
	if err := s.Put(ctx, samplePlacement("acme", "orders", 1000)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	owned, err := s.TryMarkDropping(ctx, "acme", "orders")
	if uerr := (*AtomicWriteUnsupportedError)(nil); !errors.As(err, &uerr) {
		t.Fatalf("error = %v, want *AtomicWriteUnsupportedError", err)
	}
	if owned {
		t.Error("owned = true, want false (a refused call must never report ownership)")
	}
	got, gerr := s.Get(ctx, "acme", "orders")
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if got.Dropping {
		t.Error("Dropping = true after a refused TryMarkDropping, want false (unchanged)")
	}
}

// TestStore_TryMarkDropping_RetriesOnConflictThenSucceeds mirrors
// TestStore_SetDropping_RetriesOnConflictThenSucceeds: a write racing between
// this call's Get and its CompareAndSwap is retried, not reported as a
// conflict.
func TestStore_TryMarkDropping_RetriesOnConflictThenSucceeds(t *testing.T) {
	inner := newAtomicKV()
	ctx := context.Background()
	if err := inner.Put(ctx, placementKey("acme", "orders"), mustMarshalPlacement(t, samplePlacement("acme", "orders", 1000))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	kv := &conflictOnceKV{atomicKV: inner}
	kv.inject = func() {
		p, err := (&Store{kv: inner}).Get(ctx, "acme", "orders")
		if err != nil {
			t.Fatalf("racing Get: %v", err)
		}
		p.RF = 9
		if err := inner.Put(ctx, placementKey("acme", "orders"), mustMarshalPlacement(t, p)); err != nil {
			t.Fatalf("racing Put: %v", err)
		}
	}

	s, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	owned, err := s.TryMarkDropping(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("TryMarkDropping: %v", err)
	}
	if !owned {
		t.Error("owned = false, want true (this call must still be the one to claim the fence after retrying)")
	}
	if !kv.triggered {
		t.Fatal("the conflict injection never ran; this test exercised no race at all")
	}
	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Dropping || got.RF != 9 {
		t.Errorf("got Dropping=%v RF=%d, want true/9", got.Dropping, got.RF)
	}
}

// TestStore_TryMarkDropping_ConflictExhaustsRetries proves the bounded retry
// loop's failure mode: a KV that always declines the swap reports
// *ConflictError rather than retrying forever.
func TestStore_TryMarkDropping_ConflictExhaustsRetries(t *testing.T) {
	s, err := NewStore(&alwaysDeclineKV{atomicKV: newAtomicKV()})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	if err := s.Put(ctx, samplePlacement("acme", "orders", 1000)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err = s.TryMarkDropping(ctx, "acme", "orders")
	if cerr := (*ConflictError)(nil); !errors.As(err, &cerr) {
		t.Fatalf("error = %v, want *ConflictError", err)
	}
}

// TestStore_SetDropping_BackendError proves a genuine CompareAndSwapPlacement
// failure (not *AtomicWriteUnsupportedError, not a decline) propagates as an
// error rather than being swallowed or retried forever.
func TestStore_SetDropping_BackendError(t *testing.T) {
	boom := errors.New("kv: unavailable")
	s, err := NewStore(&erroringAtomicKV{atomicKV: newAtomicKV(), err: boom})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	if err := s.Put(ctx, samplePlacement("acme", "orders", 1000)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := s.SetDropping(ctx, "acme", "orders", true); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want to wrap %v", err, boom)
	}
}

// TestStore_SetDropping_ConflictExhaustsRetries mirrors
// TestStore_TryMarkDropping_ConflictExhaustsRetries for SetDropping's own
// bounded retry loop.
func TestStore_SetDropping_ConflictExhaustsRetries(t *testing.T) {
	s, err := NewStore(&alwaysDeclineKV{atomicKV: newAtomicKV()})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	if err := s.Put(ctx, samplePlacement("acme", "orders", 1000)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	err = s.SetDropping(ctx, "acme", "orders", true)
	if cerr := (*ConflictError)(nil); !errors.As(err, &cerr) {
		t.Fatalf("error = %v, want *ConflictError", err)
	}
}

func TestStore_RemoveReplica_DropsReplicaAndUpdatesRF(t *testing.T) {
	s, err := NewStore(newAtomicKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	p := samplePlacement("acme", "orders", 1000)
	if err := s.Put(ctx, p); err != nil {
		t.Fatalf("Put: %v", err)
	}

	updated, err := s.RemoveReplica(ctx, p, 1)
	if err != nil {
		t.Fatalf("RemoveReplica: %v", err)
	}
	if updated.RF != 1 || len(updated.Replicas) != 1 || updated.Replicas[0].ReplicaID != 2 {
		t.Errorf("returned = RF %d replicas %+v, want RF 1 with only replica 2", updated.RF, updated.Replicas)
	}
	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RF != 1 || len(got.Replicas) != 1 || got.Replicas[0].ReplicaID != 2 {
		t.Errorf("stored = RF %d replicas %+v, want RF 1 with only replica 2", got.RF, got.Replicas)
	}
	if p.RF != 2 || len(p.Replicas) != 2 {
		t.Error("RemoveReplica modified the caller's target")
	}
}

func TestStore_RemoveReplica_UnknownIDIsNoop(t *testing.T) {
	s, err := NewStore(newAtomicKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	p := samplePlacement("acme", "orders", 1000)
	if err := s.Put(ctx, p); err != nil {
		t.Fatalf("Put: %v", err)
	}

	updated, err := s.RemoveReplica(ctx, p, 999)
	if err != nil {
		t.Fatalf("RemoveReplica: %v", err)
	}
	if updated.RF != 2 || len(updated.Replicas) != 2 {
		t.Errorf("returned = RF %d replicas %+v, want the entry unchanged", updated.RF, updated.Replicas)
	}
}

// TestStore_RemoveReplica_AppliesToEntryAsItStands proves the removal is
// applied to the stored entry rather than to the caller's target: a change
// committed after the target was read is kept, not refused or overwritten.
func TestStore_RemoveReplica_AppliesToEntryAsItStands(t *testing.T) {
	s, err := NewStore(newAtomicKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	stale := samplePlacement("acme", "orders", 1000)
	if err := s.Put(ctx, stale); err != nil {
		t.Fatalf("Put: %v", err)
	}
	moved := samplePlacement("acme", "orders", 1000)
	moved.LeaderReplicaID = 2
	if err := s.Put(ctx, moved); err != nil {
		t.Fatalf("racing Put: %v", err)
	}

	if _, err := s.RemoveReplica(ctx, stale, 1); err != nil {
		t.Fatalf("RemoveReplica: %v", err)
	}
	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LeaderReplicaID != 2 {
		t.Errorf("LeaderReplicaID = %d, want 2 (the concurrent change must be kept)", got.LeaderReplicaID)
	}
	if len(got.Replicas) != 1 || got.Replicas[0].ReplicaID != 2 {
		t.Errorf("Replicas = %+v, want only replica 2", got.Replicas)
	}
}

func TestStore_RemoveReplica_NotFound(t *testing.T) {
	s, err := NewStore(newAtomicKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	_, err = s.RemoveReplica(context.Background(), samplePlacement("acme", "no-such-db", 1000), 1)
	if !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("error = %v, want ErrPlacementNotFound", err)
	}
	_, err = s.RemoveReplica(context.Background(), nil, 1)
	if !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("nil target: error = %v, want ErrPlacementNotFound", err)
	}
}

func TestStore_RemoveReplica_ConflictExhaustsRetries(t *testing.T) {
	s, err := NewStore(&alwaysDeclineKV{atomicKV: newAtomicKV()})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	p := samplePlacement("acme", "orders", 1000)
	if err := s.Put(ctx, p); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err = s.RemoveReplica(ctx, p, 1)
	if cerr := (*ConflictError)(nil); !errors.As(err, &cerr) {
		t.Fatalf("error = %v, want *ConflictError", err)
	}
}

// TestStore_TryMarkDropping_BackendError mirrors
// TestStore_CompareAndSwapPlacement_BackendError for TryMarkDropping's own
// error path.
func TestStore_TryMarkDropping_BackendError(t *testing.T) {
	boom := errors.New("kv: unavailable")
	s, err := NewStore(&erroringAtomicKV{atomicKV: newAtomicKV(), err: boom})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	if err := s.Put(ctx, samplePlacement("acme", "orders", 1000)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err = s.TryMarkDropping(ctx, "acme", "orders")
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want to wrap %v", err, boom)
	}
}

// TestStore_MarkDropCommitted_BackendError mirrors
// TestStore_TryMarkDropping_BackendError for MarkDropCommitted.
func TestStore_MarkDropCommitted_BackendError(t *testing.T) {
	boom := errors.New("kv: unavailable")
	s, err := NewStore(&erroringAtomicKV{atomicKV: newAtomicKV(), err: boom})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	if err := s.Put(ctx, samplePlacement("acme", "orders", 1000)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	err = s.MarkDropCommitted(ctx, "acme", "orders")
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want to wrap %v", err, boom)
	}
}

// TestStore_MarkDropCommitted_GetErrorNonNotFound proves a genuine backend
// failure reading the entry (as opposed to ErrPlacementNotFound, which is a
// no-op) propagates as an error.
func TestStore_MarkDropCommitted_GetErrorNonNotFound(t *testing.T) {
	s, _ := NewStore(&errKV{})
	if err := s.MarkDropCommitted(context.Background(), "acme", "orders"); !errors.Is(err, errBackend) {
		t.Fatalf("error = %v, want to wrap errBackend", err)
	}
}

// alwaysDeclineKV wraps an atomicKV whose CompareAndSwap always reports a
// decline (false, nil), standing in for a pathologically hot entry that
// exhausts a bounded optimistic-retry loop.
type alwaysDeclineKV struct{ *atomicKV }

func (a *alwaysDeclineKV) CompareAndSwap(_ context.Context, _ string, _, _ []byte) (bool, error) {
	return false, nil
}

// ---------------------------------------------------------------------------
// go-qrdb#482 round 3, finding 5: MarkDropCommitted is the signal
// DropForTenant uses to tell a legitimate retry-after-teardown apart from a
// request landing on a node that never had the local record at all.
// ---------------------------------------------------------------------------

// TestStore_MarkDropCommitted_SetsTheFlag proves the success path.
func TestStore_MarkDropCommitted_SetsTheFlag(t *testing.T) {
	s, _ := NewStore(newAtomicKV())
	ctx := context.Background()
	if err := s.Put(ctx, samplePlacement("acme", "orders", 1000)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := s.MarkDropCommitted(ctx, "acme", "orders"); err != nil {
		t.Fatalf("MarkDropCommitted: %v", err)
	}
	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.DropCommitted {
		t.Error("DropCommitted = false after MarkDropCommitted, want true")
	}
}

// TestStore_MarkDropCommitted_NoOpWhenAlreadySet proves the idempotent
// re-mark case: a second call succeeds without a store round trip beyond
// the read.
func TestStore_MarkDropCommitted_NoOpWhenAlreadySet(t *testing.T) {
	s, _ := NewStore(newAtomicKV())
	ctx := context.Background()
	if err := s.Put(ctx, samplePlacement("acme", "orders", 1000)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.MarkDropCommitted(ctx, "acme", "orders"); err != nil {
		t.Fatalf("first MarkDropCommitted: %v", err)
	}

	if err := s.MarkDropCommitted(ctx, "acme", "orders"); err != nil {
		t.Fatalf("second MarkDropCommitted: %v", err)
	}
	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.DropCommitted {
		t.Error("DropCommitted = false, want true (unchanged)")
	}
}

// TestStore_MarkDropCommitted_NotFoundIsANoOp proves there is nothing left to
// mark once the entry is already fully torn down -- the drop already
// finished completely, matching the doc comment's contract.
func TestStore_MarkDropCommitted_NotFoundIsANoOp(t *testing.T) {
	s, _ := NewStore(newFakeKV())
	if err := s.MarkDropCommitted(context.Background(), "acme", "no-such-db"); err != nil {
		t.Fatalf("MarkDropCommitted on a missing entry: %v", err)
	}
}

// TestStore_MarkDropCommitted_RefusesWithoutAtomicKV mirrors
// TestStore_TryMarkDropping_RefusesWithoutAtomicKV: no fallback to Put over
// a backend that cannot compare-and-swap.
func TestStore_MarkDropCommitted_RefusesWithoutAtomicKV(t *testing.T) {
	s, _ := NewStore(newFakeKV())
	ctx := context.Background()
	if err := s.Put(ctx, samplePlacement("acme", "orders", 1000)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	err := s.MarkDropCommitted(ctx, "acme", "orders")
	if uerr := (*AtomicWriteUnsupportedError)(nil); !errors.As(err, &uerr) {
		t.Fatalf("error = %v, want *AtomicWriteUnsupportedError", err)
	}
	got, gerr := s.Get(ctx, "acme", "orders")
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if got.DropCommitted {
		t.Error("DropCommitted = true after a refused MarkDropCommitted, want false (unchanged)")
	}
}

// TestStore_MarkDropCommitted_RetriesOnConflictThenSucceeds mirrors the
// identical proof for SetDropping/TryMarkDropping.
func TestStore_MarkDropCommitted_RetriesOnConflictThenSucceeds(t *testing.T) {
	inner := newAtomicKV()
	ctx := context.Background()
	if err := inner.Put(ctx, placementKey("acme", "orders"), mustMarshalPlacement(t, samplePlacement("acme", "orders", 1000))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	kv := &conflictOnceKV{atomicKV: inner}
	kv.inject = func() {
		p, err := (&Store{kv: inner}).Get(ctx, "acme", "orders")
		if err != nil {
			t.Fatalf("racing Get: %v", err)
		}
		p.RF = 11
		if err := inner.Put(ctx, placementKey("acme", "orders"), mustMarshalPlacement(t, p)); err != nil {
			t.Fatalf("racing Put: %v", err)
		}
	}

	s, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s.MarkDropCommitted(ctx, "acme", "orders"); err != nil {
		t.Fatalf("MarkDropCommitted: %v", err)
	}
	if !kv.triggered {
		t.Fatal("the conflict injection never ran; this test exercised no race at all")
	}
	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.DropCommitted || got.RF != 11 {
		t.Errorf("got DropCommitted=%v RF=%d, want true/11", got.DropCommitted, got.RF)
	}
}

// TestStore_MarkDropCommitted_ConflictExhaustsRetries mirrors
// TestStore_TryMarkDropping_ConflictExhaustsRetries.
func TestStore_MarkDropCommitted_ConflictExhaustsRetries(t *testing.T) {
	s, err := NewStore(&alwaysDeclineKV{atomicKV: newAtomicKV()})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	if err := s.Put(ctx, samplePlacement("acme", "orders", 1000)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	err = s.MarkDropCommitted(ctx, "acme", "orders")
	if cerr := (*ConflictError)(nil); !errors.As(err, &cerr) {
		t.Fatalf("error = %v, want *ConflictError", err)
	}
}

// TestStore_GetNotFound verifies a missing entry is a typed not-found error.
func TestStore_GetNotFound(t *testing.T) {
	s, _ := NewStore(newFakeKV())
	_, err := s.Get(context.Background(), "nope", "db")
	if !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("error = %v, want ErrPlacementNotFound", err)
	}
}

// TestStore_List returns all placements and ListForTenant filters by tenant.
func TestStore_List(t *testing.T) {
	s, _ := NewStore(newFakeKV())
	ctx := context.Background()
	for _, p := range []*Placement{
		samplePlacement("acme", "orders", 1000),
		samplePlacement("acme", "users", 1001),
		samplePlacement("beta", "logs", 1002),
	} {
		if err := s.Put(ctx, p); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	all, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List len = %d, want 3", len(all))
	}

	acme, err := s.ListForTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("ListForTenant: %v", err)
	}
	if len(acme) != 2 {
		t.Fatalf("ListForTenant(acme) len = %d, want 2", len(acme))
	}
	for _, p := range acme {
		if p.TenantID != "acme" {
			t.Errorf("ListForTenant returned foreign tenant %q", p.TenantID)
		}
	}
}

// TestStore_PutValidation rejects placements missing identity.
func TestStore_PutValidation(t *testing.T) {
	s, _ := NewStore(newFakeKV())
	ctx := context.Background()
	if err := s.Put(ctx, &Placement{DatabaseID: "db", Database: "db"}); !errors.Is(err, ErrEmptyTenant) {
		t.Errorf("Put without tenant error = %v, want ErrEmptyTenant", err)
	}
	if err := s.Put(ctx, &Placement{TenantID: "t"}); !errors.Is(err, ErrEmptyDatabaseID) {
		t.Errorf("Put without database error = %v, want ErrEmptyDatabaseID", err)
	}
}

// TestStore_WatchUnsupported returns ErrWatchUnsupported for a KV without the
// Watcher capability.
func TestStore_WatchUnsupported(t *testing.T) {
	s, _ := NewStore(newFakeKV())
	if _, err := s.Watch(context.Background()); !errors.Is(err, ErrWatchUnsupported) {
		t.Fatalf("Watch error = %v, want ErrWatchUnsupported", err)
	}
}

// TestStore_Watch translates backend events into typed placement events.
func TestStore_Watch(t *testing.T) {
	kv := newWatchKV()
	s, _ := NewStore(kv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := s.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	p := samplePlacement("acme", "orders", 1000)
	val, _ := s.kvMarshal(p)
	kv.ch <- WatchEvent{Key: placementKey("acme", "orders"), Value: val}

	ev := <-events
	if ev.Type != EventPut || ev.TenantID != "acme" || ev.Database != "orders" {
		t.Fatalf("put event = %+v, want EventPut acme/orders", ev)
	}
	if ev.Placement == nil || ev.Placement.ShardID != 1000 {
		t.Fatalf("put event placement = %+v, want shard 1000", ev.Placement)
	}

	// A delete carrying the prior value recovers the identity.
	kv.ch <- WatchEvent{Deleted: true, Key: placementKey("acme", "orders"), PrevValue: val}
	ev = <-events
	if ev.Type != EventDelete || ev.TenantID != "acme" || ev.Database != "orders" {
		t.Fatalf("delete event = %+v, want EventDelete acme/orders", ev)
	}
}

// kvMarshal is a tiny test helper exposing the JSON encoding the store uses.
func (s *Store) kvMarshal(p *Placement) ([]byte, error) {
	kv := newFakeKV()
	tmp, _ := NewStore(kv)
	if err := tmp.Put(context.Background(), p); err != nil {
		return nil, err
	}
	return kv.Get(context.Background(), placementKey(p.TenantID, p.Database))
}

// The typed placement refusals describe themselves and are matched by type.
func TestPlacementTypedErrors(t *testing.T) {
	for _, err := range []error{&ConflictError{}, &AtomicWriteUnsupportedError{}} {
		if err.Error() == "" {
			t.Errorf("%T has no message", err)
		}
		wrapped := &StoreError{TenantID: "t", DatabaseID: "d", Op: "put", Err: err}
		if !errors.Is(wrapped, err) {
			t.Errorf("a *StoreError does not unwrap to its %T", err)
		}
	}
	var conflict *ConflictError
	if errors.As(&StoreError{Err: &AtomicWriteUnsupportedError{}}, &conflict) {
		t.Error("an unsupported backend must not read as a conflict")
	}
}
