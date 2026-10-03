// Copyright (c) 2026 Jeremy Hahn
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

package pool_test

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/local"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
)

// fakeReplicatedKV is a tiny in-memory stand-in for the replicated system KV.
// Get follows the kvstore convention: a missing key returns (nil, nil). It is
// safe for concurrent use so it can be shared by two "nodes" in one test.
type fakeReplicatedKV struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newFakeReplicatedKV() *fakeReplicatedKV {
	return &fakeReplicatedKV{data: make(map[string][]byte)}
}

func (k *fakeReplicatedKV) Get(_ context.Context, key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.data[key]
	if !ok {
		return nil, nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func (k *fakeReplicatedKV) Put(_ context.Context, key string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	k.data[key] = cp
	return nil
}

// TestReplicatedStateStore_RoundTrip proves a value Put through the store reads
// back through Get, and that the round-robin cursor key lands under the shared
// __sys:volumepool/ namespace in the backing replicated KV.
func TestReplicatedStateStore_RoundTrip(t *testing.T) {
	kv := newFakeReplicatedKV()
	store, err := qrdbpool.NewBoundReplicatedStateStore(kv)
	if err != nil {
		t.Fatalf("NewBoundReplicatedStateStore: %v", err)
	}
	ctx := context.Background()

	if _, found, err := store.Get(ctx, "pool/default/round_robin/cursor"); err != nil || found {
		t.Fatalf("Get(absent) = (found=%v, %v), want (false, nil)", found, err)
	}

	want := make([]byte, 8)
	binary.BigEndian.PutUint64(want, 7)
	if err := store.Put(ctx, "pool/default/round_robin/cursor", want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, found, err := store.Get(ctx, "pool/default/round_robin/cursor")
	if err != nil || !found {
		t.Fatalf("Get = (found=%v, %v), want (true, nil)", found, err)
	}
	if binary.BigEndian.Uint64(got) != 7 {
		t.Fatalf("Get value = %d, want 7", binary.BigEndian.Uint64(got))
	}

	// The value must be namespaced in the underlying KV.
	if _, raw := kv.Get(ctx, "__sys:volumepool/pool/default/round_robin/cursor"); raw != nil {
		t.Fatalf("expected namespaced key present, got err %v", raw)
	}
}

// TestReplicatedStateStore_Unbound proves Get/Put fail with a typed error
// before a replicated KV is bound, and succeed after Bind.
func TestReplicatedStateStore_Unbound(t *testing.T) {
	store := qrdbpool.NewReplicatedStateStore()
	ctx := context.Background()

	if store.Bound() {
		t.Fatal("fresh store should report Bound() == false")
	}

	_, _, err := store.Get(ctx, "k")
	var unbound *qrdbpool.ReplicatedStateStoreUnboundError
	if !errors.As(err, &unbound) {
		t.Fatalf("Get unbound = %v, want ReplicatedStateStoreUnboundError", err)
	}
	if err := store.Put(ctx, "k", []byte("v")); !errors.As(err, &unbound) {
		t.Fatalf("Put unbound = %v, want ReplicatedStateStoreUnboundError", err)
	}

	if err := store.Bind(newFakeReplicatedKV()); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if !store.Bound() {
		t.Fatal("store should report Bound() == true after Bind")
	}
	if err := store.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put after Bind: %v", err)
	}
}

// TestReplicatedStateStore_Unbind proves Unbind returns a bound store to its
// unbound, fail-closed state, and that it is idempotent on an already-unbound
// store.
func TestReplicatedStateStore_Unbind(t *testing.T) {
	store := qrdbpool.NewReplicatedStateStore()
	ctx := context.Background()

	// Unbinding a fresh (never-bound) store is a no-op, not an error.
	store.Unbind()
	if store.Bound() {
		t.Fatal("Unbind on a fresh store must leave it unbound")
	}

	if err := store.Bind(newFakeReplicatedKV()); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if !store.Bound() {
		t.Fatal("store should report Bound() == true after Bind")
	}

	store.Unbind()
	if store.Bound() {
		t.Fatal("store should report Bound() == false after Unbind")
	}
	var unbound *qrdbpool.ReplicatedStateStoreUnboundError
	if _, _, err := store.Get(ctx, "k"); !errors.As(err, &unbound) {
		t.Fatalf("Get after Unbind = %v, want ReplicatedStateStoreUnboundError", err)
	}

	// Re-binding after Unbind works, exactly like binding fresh.
	if err := store.Bind(newFakeReplicatedKV()); err != nil {
		t.Fatalf("Bind after Unbind: %v", err)
	}
	if err := store.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put after re-Bind: %v", err)
	}
}

// TestReplicatedStateStore_NilBind proves nil KVs are rejected with a typed
// error from both the constructor and Bind.
func TestReplicatedStateStore_NilBind(t *testing.T) {
	var nilErr *qrdbpool.NilReplicatedKVError
	if _, err := qrdbpool.NewBoundReplicatedStateStore(nil); !errors.As(err, &nilErr) {
		t.Fatalf("NewBoundReplicatedStateStore(nil) = %v, want NilReplicatedKVError", err)
	}
	if err := qrdbpool.NewReplicatedStateStore().Bind(nil); !errors.As(err, &nilErr) {
		t.Fatalf("Bind(nil) = %v, want NilReplicatedKVError", err)
	}
}

// TestReplicatedStateStore_EmptyKey proves the empty-key guard fires on both
// Get and Put.
func TestReplicatedStateStore_EmptyKey(t *testing.T) {
	store, _ := qrdbpool.NewBoundReplicatedStateStore(newFakeReplicatedKV())
	ctx := context.Background()
	var emptyKey *qrdbpool.EmptyKeyError
	if _, _, err := store.Get(ctx, ""); !errors.As(err, &emptyKey) {
		t.Fatalf("Get(\"\") = %v, want EmptyKeyError", err)
	}
	if err := store.Put(ctx, "", []byte("v")); !errors.As(err, &emptyKey) {
		t.Fatalf("Put(\"\") = %v, want EmptyKeyError", err)
	}
}

// errReplicatedKV always fails, to exercise the GetError/PutError wrapping.
type errReplicatedKV struct{ err error }

func (e errReplicatedKV) Get(context.Context, string) ([]byte, error) { return nil, e.err }
func (e errReplicatedKV) Put(context.Context, string, []byte) error   { return e.err }

// TestReplicatedStateStore_BackendError proves backend failures are wrapped in
// the package's typed GetError/PutError.
func TestReplicatedStateStore_BackendError(t *testing.T) {
	store, _ := qrdbpool.NewBoundReplicatedStateStore(errReplicatedKV{err: errors.New("boom")})
	ctx := context.Background()
	var getErr *qrdbpool.GetError
	if _, _, err := store.Get(ctx, "k"); !errors.As(err, &getErr) {
		t.Fatalf("Get error = %v, want *GetError", err)
	}
	var putErr *qrdbpool.PutError
	if err := store.Put(ctx, "k", []byte("v")); !errors.As(err, &putErr) {
		t.Fatalf("Put error = %v, want *PutError", err)
	}
}

// TestReplicatedStateStore_ContextCancelled proves a cancelled context is
// honored by both Get and Put before touching the backend.
func TestReplicatedStateStore_ContextCancelled(t *testing.T) {
	store, _ := qrdbpool.NewBoundReplicatedStateStore(newFakeReplicatedKV())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.Get(ctx, "k"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get cancelled = %v, want context.Canceled", err)
	}
	if err := store.Put(ctx, "k", []byte("v")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put cancelled = %v, want context.Canceled", err)
	}
}

// TestReplicatedStateStore_BacksRoundRobinManager proves a round-robin strategy
// backed by the replicated store advances its cursor through the shared KV, so
// two pool managers sharing one replicated store rotate as a single cursor.
func TestReplicatedStateStore_BacksRoundRobinManager(t *testing.T) {
	kv := newFakeReplicatedKV()
	ctx := context.Background()

	newNodeManager := func(t *testing.T) (objpool.Manager, []string) {
		t.Helper()
		store, err := qrdbpool.NewBoundReplicatedStateStore(kv)
		if err != nil {
			t.Fatalf("NewBoundReplicatedStateStore: %v", err)
		}
		var paths []string
		var cands []common.Storage
		for range 3 {
			dir := t.TempDir()
			s := local.New()
			if err := s.Configure(map[string]string{"path": dir}); err != nil {
				t.Fatalf("configure local: %v", err)
			}
			cands = append(cands, s)
			paths = append(paths, dir)
		}
		mgr := objpool.NewManager()
		if err := mgr.Register("default", cands, &objpool.RoundRobinStrategy{State: store}); err != nil {
			t.Fatalf("Register: %v", err)
		}
		return objpool.LocalOnly(mgr), paths
	}

	mgrA, pathsA := newNodeManager(t)
	mgrB, pathsB := newNodeManager(t)

	// Pick once on node A (cursor 0 -> candidate 0, cursor becomes 1), then once
	// on node B. Because both share the replicated cursor, B sees cursor 1 and
	// picks candidate 1 — proving the cursor is cluster-wide, not per node.
	pickA, err := mgrA.Pick(ctx, "default", objpool.Hint{})
	if err != nil {
		t.Fatalf("Pick A: %v", err)
	}
	pickB, err := mgrB.Pick(ctx, "default", objpool.Hint{})
	if err != nil {
		t.Fatalf("Pick B: %v", err)
	}
	if pickA.(common.PathAccessor).LocalPath() != pathsA[0] {
		t.Fatalf("node A first pick = %s, want candidate 0 (%s)", pickA.(common.PathAccessor).LocalPath(), pathsA[0])
	}
	if pickB.(common.PathAccessor).LocalPath() != pathsB[1] {
		t.Fatalf("node B pick = %s, want candidate 1 (%s) via shared cursor", pickB.(common.PathAccessor).LocalPath(), pathsB[1])
	}
}
