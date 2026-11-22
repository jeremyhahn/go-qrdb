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

//go:build qrdb_vlog

package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// BarrierLogKeyspace is the reserved keyspace a barrier tenant's verifiable
// logs occupy inside the tenant barrier on a standalone node. Like
// tenant.HomeName it is a reserved name ("__" first), so neither the home
// store (tenant.HomeName + "/") nor any database of the tenant shares it.
const BarrierLogKeyspace = "__vlog"

// barrierLogPrefix is the key prefix of BarrierLogKeyspace. A vlog key is
// stored under it in lower-case hex, which keeps the byte order of the
// keys in the string order of the stored keys and any byte of a key legal.
const barrierLogPrefix = BarrierLogKeyspace + "/"

// errBarrierLogStoreNotAtomic refuses a tenant barrier store that cannot
// apply a command's writes in one atomic batch: the log is never written
// unbatched.
var errBarrierLogStoreNotAtomic = errors.New("vlog barrier store: the tenant barrier store has no atomic batch")

// barrierLogStore serves the verifiable logs of one barrier-isolated tenant
// on a standalone node from the tenant barrier's own store, in
// BarrierLogKeyspace: every value is encrypted under the tenant's key, and
// every call while the barrier is sealed is refused with
// tenant.ErrTenantSealed, as the Raft path refuses it. Commands are applied
// by the shared logChain core, each command's writes in one
// PutAllIfAllUnchanged batch.
//
// mu serializes every propose and read. The chain is dropped whenever the
// barrier is seen sealed or a store call reports the seal; a recycled or
// restored barrier is a different instance, and the route builds a fresh
// store (and chain) for it.
type barrierLogStore struct {
	tenantID string
	barrier  LogBarrier
	kv       kvstore.MultiGuardedBatchKVStore

	mu   sync.Mutex
	logs logChain
}

// newBarrierLogStore returns the store of tenantID's barrier, served by kv.
// kv must apply atomic batches (kvstore.MultiGuardedBatchKVStore), else
// errBarrierLogStoreNotAtomic.
func newBarrierLogStore(tenantID string, barrier LogBarrier, kv kvstore.KVStore) (*barrierLogStore, error) {
	batched, ok := kv.(kvstore.MultiGuardedBatchKVStore)
	if !ok || barrier == nil {
		return nil, errBarrierLogStoreNotAtomic
	}
	return &barrierLogStore{tenantID: tenantID, barrier: barrier, kv: batched}, nil
}

// sealed reports whether the barrier is sealed, dropping the chain when it
// is. s.mu must be held.
func (s *barrierLogStore) sealed() bool {
	if s.barrier.IsSealed() {
		s.logs.drop()
		return true
	}
	return false
}

// isSealedError reports whether err is the tenant barrier's seal.
func isSealedError(err error) bool {
	return errors.Is(err, seal.ErrSealed) || errors.Is(err, seal.ErrTenantSealed) || errors.Is(err, tenant.ErrTenantSealed)
}

// translateStoreError answers the barrier's seal with tenant.ErrTenantSealed,
// dropping the chain, and returns any other error as it is. s.mu must be
// held.
func (s *barrierLogStore) translateStoreError(err error) error {
	if isSealedError(err) {
		s.logs.drop()
		return tenant.ErrTenantSealed
	}
	return err
}

// propose applies one encoded OpVerifiableLog command to the tenant
// barrier's store and returns the apply's result.
func (s *barrierLogStore) propose(ctx context.Context, cmd []byte) (sm.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed() {
		return sm.Result{}, tenant.ErrTenantSealed
	}
	r := barrierKVReader{ctx: ctx, kv: s.kv}
	res, err := s.logs.apply(r, func(ops []storage.BufferedOp) error {
		return applyToBarrierKV(ctx, s.kv, ops)
	}, cmd)
	if err != nil {
		return sm.Result{}, s.translateStoreError(err)
	}
	return res, nil
}

// read answers one vlog.Request from the tenant barrier's store.
func (s *barrierLogStore) read(ctx context.Context, req vlog.Request) (interface{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed() {
		return nil, tenant.ErrTenantSealed
	}
	v, err := readLog(barrierKVReader{ctx: ctx, kv: s.kv}, req, isSealedError)
	if err != nil {
		return nil, s.translateStoreError(err)
	}
	return v, nil
}

// featureEnabled reports true: the tenant barrier's store is applied by this
// binary alone, so no replica can lack a feature.
func (s *barrierLogStore) featureEnabled(feature.Feature) bool { return true }

// serving refuses a sealed tenant barrier with tenant.ErrTenantSealed.
func (s *barrierLogStore) serving() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed() {
		return tenant.ErrTenantSealed
	}
	return nil
}

// applyToBarrierKV writes ops to kv in one atomic, unguarded batch.
func applyToBarrierKV(ctx context.Context, kv kvstore.MultiGuardedBatchKVStore, ops []storage.BufferedOp) error {
	if len(ops) == 0 {
		return nil
	}
	writes := make([]kvstore.KV, 0, len(ops))
	for _, op := range ops {
		if op.Range {
			return errRangeUnsupported
		}
		writes = append(writes, kvstore.KV{Key: barrierLogKey(op.Key), Value: op.Value, Delete: op.Deleted})
	}
	applied, err := kv.PutAllIfAllUnchanged(ctx, nil, writes)
	if err != nil {
		return err
	}
	if !applied {
		return errBarrierLogBatchNotApplied
	}
	return nil
}

// errBarrierLogBatchNotApplied is an unguarded batch the store reported not
// applied: the store broke its contract, so the command fails.
var errBarrierLogBatchNotApplied = errors.New("vlog barrier store: the batch was not applied")

// barrierLogKey is key's stored key in BarrierLogKeyspace.
func barrierLogKey(key []byte) string {
	return barrierLogPrefix + hex.EncodeToString(key)
}

// barrierKVReader is a storage.Reader over a tenant barrier's KV store,
// confined to BarrierLogKeyspace.
type barrierKVReader struct {
	ctx context.Context
	kv  kvstore.KVStore
}

// Get returns key's value, nil when it is absent.
func (r barrierKVReader) Get(key []byte) ([]byte, error) {
	return r.kv.Get(r.ctx, barrierLogKey(key))
}

// NewIterator returns an ascending iterator over the keys in opts' bounds.
// The scan is narrowed to the hex prefix the two bounds share.
func (r barrierKVReader) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	var lower, upper []byte
	if opts != nil {
		if opts.Reverse {
			return storage.NewSliceIterator(nil, nil, errReverseUnsupported)
		}
		lower, upper = opts.LowerBound, opts.UpperBound
	}
	scan := barrierLogPrefix
	if upper != nil {
		scan += sharedPrefix(hex.EncodeToString(lower), hex.EncodeToString(upper))
	}
	type kv struct{ key, value []byte }
	var kvs []kv
	err := r.kv.Scan(r.ctx, scan, func(stored string, value []byte) error {
		k, err := hex.DecodeString(strings.TrimPrefix(stored, barrierLogPrefix))
		if err != nil {
			return err
		}
		if lower != nil && bytes.Compare(k, lower) < 0 {
			return nil
		}
		if upper != nil && bytes.Compare(k, upper) >= 0 {
			return nil
		}
		kvs = append(kvs, kv{key: k, value: append([]byte(nil), value...)})
		return nil
	})
	if err != nil {
		return storage.NewSliceIterator(nil, nil, err)
	}
	sort.Slice(kvs, func(i, j int) bool { return bytes.Compare(kvs[i].key, kvs[j].key) < 0 })
	keys := make([][]byte, len(kvs))
	values := make([][]byte, len(kvs))
	for i := range kvs {
		keys[i], values[i] = kvs[i].key, kvs[i].value
	}
	return storage.NewSliceIterator(keys, values, nil)
}

// sharedPrefix returns the longest common prefix of a and b.
func sharedPrefix(a, b string) string {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}
