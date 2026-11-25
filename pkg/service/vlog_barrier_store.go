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
	// admit admits each read and write through the tenant's home-store gate
	// (LogStoreAdmission); nil admits every operation.
	admit LogStoreAdmission
	// source is the LogBarrierStoreSource the store was built from; each
	// admitted operation checks it still holds barrier for the tenant
	// (replaced). nil checks nothing.
	source LogBarrierStoreSource

	mu   sync.Mutex
	logs logChain
}

// newBarrierLogStore returns the store of tenantID's barrier, served by kv.
// kv must apply atomic batches (kvstore.MultiGuardedBatchKVStore), else
// errBarrierLogStoreNotAtomic.
func newBarrierLogStore(tenantID string, barrier LogBarrier, kv kvstore.KVStore, admit LogStoreAdmission, source LogBarrierStoreSource) (*barrierLogStore, error) {
	batched, ok := kv.(kvstore.MultiGuardedBatchKVStore)
	if !ok || barrier == nil {
		return nil, errBarrierLogStoreNotAtomic
	}
	return &barrierLogStore{tenantID: tenantID, barrier: barrier, kv: batched, admit: admit, source: source}, nil
}

// admitOp admits one operation through s.admit; release ends it.
func (s *barrierLogStore) admitOp() (func(), error) {
	if s.admit == nil {
		return func() {}, nil
	}
	return s.admit.AdmitTenantLog(s.tenantID)
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

// replaced refuses with ErrLogStoreReplaced, dropping the chain, when the
// source no longer holds s.barrier for the tenant (an encryption migration's
// ReplaceTenantBarrier) or cannot say. s.mu must be held.
func (s *barrierLogStore) replaced(ctx context.Context) error {
	if s.source == nil {
		return nil
	}
	barrier, _, err := s.source.TenantLogStore(ctx, s.tenantID)
	if err == nil && barrier == s.barrier {
		return nil
	}
	s.logs.drop()
	return ErrLogStoreReplaced
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
	return s.proposeFenced(ctx, cmd, logFence{})
}

// proposeFenced is propose for a write fenced to its database's incarnation.
// The store has no Raft group to carry a drop-fence guard: fence.live is
// checked under s.mu, which the drop's reclamation (DropDatabaseLogs) also
// proposes under, after the drop has fenced the database. A write that holds
// s.mu first applies and is reclaimed; one that takes it after finds the
// database dropping, dropped or created again and stores nothing.
func (s *barrierLogStore) proposeFenced(ctx context.Context, cmd []byte, fence logFence) (sm.Result, error) {
	release, err := s.admitOp()
	if err != nil {
		return sm.Result{}, err
	}
	defer release()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed() {
		return sm.Result{}, tenant.ErrTenantSealed
	}
	if err := s.replaced(ctx); err != nil {
		return sm.Result{}, err
	}
	if fence.live != nil {
		if err := fence.live(); err != nil {
			return sm.Result{}, err
		}
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
	release, err := s.admitOp()
	if err != nil {
		return nil, err
	}
	defer release()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed() {
		return nil, tenant.ErrTenantSealed
	}
	if err := s.replaced(ctx); err != nil {
		return nil, err
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
// The key listing is narrowed to the hex prefix the two bounds share, and
// lists keys only: an entry body is read (and decrypted) only for a position
// whose Value is asked for, so a caller that seeks past bodies never loads
// them.
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
	stored, err := r.kv.List(r.ctx, scan)
	if err != nil {
		return storage.NewSliceIterator(nil, nil, err)
	}
	keys := make([][]byte, 0, len(stored))
	for _, s := range stored {
		k, err := hex.DecodeString(strings.TrimPrefix(s, barrierLogPrefix))
		if err != nil {
			return storage.NewSliceIterator(nil, nil, err)
		}
		if lower != nil && bytes.Compare(k, lower) < 0 {
			continue
		}
		if upper != nil && bytes.Compare(k, upper) >= 0 {
			continue
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
	return &barrierKeyIterator{r: r, keys: keys, pos: -1}
}

// barrierKeyIterator iterates a sorted barrier log key listing. Values are
// read only for positions whose Value is asked for, one Get per position,
// cached until the position changes. A Get error ends the iteration: the
// iterator turns invalid and every later positioning call returns false.
type barrierKeyIterator struct {
	r      barrierKVReader
	keys   [][]byte
	pos    int
	valid  bool
	value  []byte
	loaded bool
	err    error
}

func (it *barrierKeyIterator) setPos(pos int) bool {
	it.value, it.loaded = nil, false
	if it.err != nil {
		it.valid = false
		return false
	}
	it.pos = pos
	it.valid = pos >= 0 && pos < len(it.keys)
	return it.valid
}

// SeekToFirst positions the iterator at the first key.
func (it *barrierKeyIterator) SeekToFirst() bool { return it.setPos(0) }

// SeekToLast positions the iterator at the last key.
func (it *barrierKeyIterator) SeekToLast() bool { return it.setPos(len(it.keys) - 1) }

// Seek positions the iterator at the first key >= target.
func (it *barrierKeyIterator) Seek(target []byte) bool {
	return it.setPos(sort.Search(len(it.keys), func(i int) bool {
		return bytes.Compare(it.keys[i], target) >= 0
	}))
}

// Next advances the iterator to the next key.
func (it *barrierKeyIterator) Next() bool {
	if !it.valid {
		return it.setPos(len(it.keys))
	}
	return it.setPos(it.pos + 1)
}

// Prev moves the iterator to the previous key.
func (it *barrierKeyIterator) Prev() bool {
	if !it.valid {
		return it.setPos(-1)
	}
	return it.setPos(it.pos - 1)
}

// Valid reports whether the iterator is positioned at a key.
func (it *barrierKeyIterator) Valid() bool { return it.valid }

// Key returns the current key, or nil when the iterator is invalid.
func (it *barrierKeyIterator) Key() []byte {
	if !it.valid {
		return nil
	}
	return it.keys[it.pos]
}

// Value reads the current entry body on first use at this position.
func (it *barrierKeyIterator) Value() []byte {
	if !it.valid {
		return nil
	}
	if !it.loaded {
		v, err := it.r.kv.Get(it.r.ctx, barrierLogKey(it.keys[it.pos]))
		if err != nil {
			it.err, it.valid, it.value = err, false, nil
			return nil
		}
		it.value, it.loaded = v, true
	}
	return it.value
}

// Error returns the listing or body read error, if any.
func (it *barrierKeyIterator) Error() error { return it.err }

// Close releases nothing: the iterator holds only the key listing.
func (it *barrierKeyIterator) Close() error { return nil }

// sharedPrefix returns the longest common prefix of a and b.
func sharedPrefix(a, b string) string {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}
