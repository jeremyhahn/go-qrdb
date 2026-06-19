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

package pool

import (
	"context"
	"sync/atomic"

	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"
)

// ReplicatedKV is the minimal replicated key/value surface the
// ReplicatedStateStore needs. It is satisfied by the qrdb replicated system
// KV (the Raft-backed system shard) accessed through a kvstore.KVStore
// adapter. Get follows the kvstore convention: a missing key returns
// (nil, nil) — found is signalled by a non-nil value, not by an error.
type ReplicatedKV interface {
	// Get returns the value for key, or (nil, nil) when the key is absent.
	Get(ctx context.Context, key string) ([]byte, error)
	// Put stores value under key, overwriting any previous value.
	Put(ctx context.Context, key string, value []byte) error
}

// kvHolder wraps a ReplicatedKV so it can be stored in an atomic.Pointer:
// storing an interface value directly in atomic.Pointer is not possible, and
// the holder keeps Bind lock-free while remaining nil-safe.
type kvHolder struct{ kv ReplicatedKV }

// ReplicatedStateStore implements objpool.StateStore on top of the replicated
// system KV so the round-robin cursor (and any future pool state) is a
// cluster-wide value that survives leader failover instead of diverging
// per node. Keys are namespaced under the same __sys:volumepool/ prefix the
// node-local PebbleStateStore uses, so operator dumps of either backend read
// identically.
//
// The bound KV is set with Bind. Because the volume-pool subsystem is built
// before the replicated KV is available (the system shard is opened later in
// boot), the store is constructed unbound and bound once the data plane comes
// up. Pick — the only caller of Get/Put — runs at tenant-create time, long
// after binding, so an unbound operation indicates a wiring ordering bug and
// returns ReplicatedStateStoreUnboundError.
type ReplicatedStateStore struct {
	holder atomic.Pointer[kvHolder]
}

// Compile-time interface assertion.
var _ objpool.StateStore = (*ReplicatedStateStore)(nil)

// NewReplicatedStateStore constructs an unbound ReplicatedStateStore. The
// caller must call Bind before the first Get/Put.
func NewReplicatedStateStore() *ReplicatedStateStore {
	return &ReplicatedStateStore{}
}

// NewBoundReplicatedStateStore constructs a ReplicatedStateStore already bound
// to kv. Returns NilReplicatedKVError when kv is nil.
func NewBoundReplicatedStateStore(kv ReplicatedKV) (*ReplicatedStateStore, error) {
	if kv == nil {
		return nil, &NilReplicatedKVError{}
	}
	s := &ReplicatedStateStore{}
	s.holder.Store(&kvHolder{kv: kv})
	return s, nil
}

// Bind attaches (or replaces) the replicated KV backing the store. It is
// lock-free and safe to call concurrently with Get/Put, though in practice it
// runs once during boot. Returns NilReplicatedKVError when kv is nil.
func (s *ReplicatedStateStore) Bind(kv ReplicatedKV) error {
	if kv == nil {
		return &NilReplicatedKVError{}
	}
	s.holder.Store(&kvHolder{kv: kv})
	return nil
}

// Bound reports whether a replicated KV has been bound.
func (s *ReplicatedStateStore) Bound() bool {
	_, ok := s.backend()
	return ok
}

// backend returns the currently bound replicated KV, or (nil, false) when
// unbound.
func (s *ReplicatedStateStore) backend() (ReplicatedKV, bool) {
	h := s.holder.Load()
	if h == nil || h.kv == nil {
		return nil, false
	}
	return h.kv, true
}

// Get implements objpool.StateStore. A missing key returns found=false and a
// nil error so the strategy applies a zero default cursor.
func (s *ReplicatedStateStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if key == "" {
		return nil, false, &EmptyKeyError{}
	}
	kv, ok := s.backend()
	if !ok {
		return nil, false, &ReplicatedStateStoreUnboundError{}
	}
	value, err := kv.Get(ctx, namespacedKey(key))
	if err != nil {
		return nil, false, &GetError{Key: key, Err: err}
	}
	if value == nil {
		return nil, false, nil
	}
	return value, true, nil
}

// Put implements objpool.StateStore, persisting value under the namespaced
// key through the replicated KV (and therefore through Raft consensus).
func (s *ReplicatedStateStore) Put(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if key == "" {
		return &EmptyKeyError{}
	}
	kv, ok := s.backend()
	if !ok {
		return &ReplicatedStateStoreUnboundError{}
	}
	if err := kv.Put(ctx, namespacedKey(key), value); err != nil {
		return &PutError{Key: key, Err: err}
	}
	return nil
}
