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

package statemachine

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/cespare/xxhash/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

const kvFastShardCount = 256

// Compile-time interface assertion.
//
// FastBackend is the one Backend in this tree that holds real storage and
// answers before using it, so it is the only one for which Backend's two write
// contracts are genuinely different code:
//
//	Put / Delete                — asynchronous. "Your write is queued."
//	                              A storage failure is LOGGED. This is the
//	                              contract the backend exists to provide and
//	                              this change does not touch it.
//	PutDurable / DeleteDurable  — synchronous on the calling goroutine, the
//	                              storage error RETURNED, and the cache entry
//	                              WITHDRAWN when the write does not land.
//
// It reached that pair in three steps, and the middle one is worth keeping
// because a reader will otherwise assume the whole thing arrived at once.
//
// go-qrdb#224 gave it ordering. Each write used to persist on its own
// goroutine ordered against nothing, so storage could apply them in any order.
// Measured on the pre-fix code with ORDINARY writes and no guarded verb
// involved:
//
//	Put(k,"old"); Put(k,"new")  -> storage saw [set:k=new set:k=old], durable "old"
//	Put(k,"v");   Delete(k)     -> storage saw [delete:k set:k=v], key PRESENT "v"
//
// A lost write and a resurrected key. FastStateMachine and
// ConcurrentStateMachine never had it: they own their storage and draw a
// persistOrder ticket under the shard lock, where apply order is already
// established (go-qrdb#86). This backend was written later, reproduced the
// pre-#86 write path, and did not receive the fix until #224.
//
// go-qrdb#175 then REFUSED the guarded verbs here, because ordering alone is
// not the guarantee: the write was still acknowledged before it was durable
// and its failure was still swallowed.
//
// go-qrdb#216 — this — supplies the remaining two promises, on the durable
// pair only. Do NOT close the gap instead by making Put synchronous: that
// discards the asynchronous contract the backend exists to provide, and it was
// rejected on those grounds twice.
var _ Backend = (*FastBackend)(nil)

// kvFastShard is a single partition of the in-memory cache for
// FastBackend, protected by a read-write mutex. A nil value entry
// serves as a tombstone for a deleted key.
type kvFastShard struct {
	mu   sync.RWMutex
	data map[string][]byte

	// order keeps this shard's storage writes in apply order, asynchronous and
	// durable alike. See persistOrder — and go-qrdb#224 for why this field
	// arrived late.
	order persistOrder

	// claims maps a key with an IN-FLIGHT durable write to that write's
	// ticket. A durable write releases the shard lock while storage runs, so a
	// later write to the same key can land in the window; this record is how a
	// failed write can tell whether the entry it would roll back is still its
	// own. See claim.withdraw.
	claims map[string]uint64
}

// FastBackend is a 256-shard in-memory cache backed by persistent
// storage. Writes land in the cache first (for fast reads) and are
// flushed asynchronously to the underlying storage.Storage. This
// mirrors the FastStateMachine's storage model.
type FastBackend struct {
	shardID   uint64
	replicaID uint64
	storage   storage.Storage
	shards    [kvFastShardCount]kvFastShard
	wg        sync.WaitGroup
	logger    *slog.Logger
}

// NewFastBackend creates a new FastBackend with the given identity,
// backing storage, and logger.
func NewFastBackend(shardID, replicaID uint64, store storage.Storage, logger *slog.Logger) *FastBackend {
	if logger == nil {
		logger = slog.Default()
	}
	b := &FastBackend{
		shardID:   shardID,
		replicaID: replicaID,
		storage:   store,
		logger:    logger,
	}
	for i := range b.shards {
		b.shards[i].data = make(map[string][]byte)
		b.shards[i].claims = make(map[string]uint64)
		b.shards[i].order.init()
	}
	return b
}

// shardFor returns the cache shard for the given key.
func (b *FastBackend) shardFor(key string) *kvFastShard {
	return &b.shards[b.shardIndex(key)]
}

// shardIndex is the index of the cache shard key belongs to. shardFor and
// ApplyAtomic both resolve through it, so they cannot disagree about a key.
func (b *FastBackend) shardIndex(key string) int {
	return int(xxhash.Sum64String(key) % kvFastShardCount)
}

// Get retrieves the value for the given key. It checks the cache first
// and falls back to the backing storage on cache miss.
func (b *FastBackend) Get(key []byte) ([]byte, error) {
	k := string(key)
	s := b.shardFor(k)

	s.mu.RLock()
	val, found := s.data[k]
	s.mu.RUnlock()

	if found {
		if val == nil {
			return nil, nil // tombstone
		}
		cp := make([]byte, len(val))
		copy(cp, val)
		return cp, nil
	}

	// Cache miss: fall back to storage.
	stored, err := b.storage.Get(key)
	if err != nil {
		return nil, &StorageError{Op: "get", Inner: err}
	}
	return stored, nil
}

// Put writes to the cache shard and asynchronously persists to storage, in
// this shard's apply order.
//
// The ticket is drawn under the SAME lock acquisition as the cache write, which
// is the whole mechanism: that is the point at which apply order is already
// established, so the order of tickets is the order of applies. Drawing it
// after releasing the lock would race exactly as the unordered goroutines did.
func (b *FastBackend) Put(key, value []byte) error {
	k := string(key)
	owned := make([]byte, len(value))
	copy(owned, value)

	s := b.shardFor(k)
	s.mu.Lock()
	s.data[k] = owned
	ticket := s.order.ticket()
	// A later write to this key retires any in-flight durable claim on it: the
	// claim's rollback must not restore a value this write has superseded
	// (go-qrdb#94, Codex review on PR #110). Done under the same lock as the
	// cache write, so the retirement and the write are one step.
	delete(s.claims, k)
	s.mu.Unlock()

	b.persistAsync(s, ticket, Mutation{Key: []byte(k), Value: owned})
	return nil
}

// Delete sets a tombstone in the cache and asynchronously deletes from
// storage, in this shard's apply order.
//
// Ordering matters at least as much here as for Put: an unordered delete can be
// followed on disk by an older pending Set, which resurrects the value the
// delete retired (go-qrdb#224).
func (b *FastBackend) Delete(key []byte) error {
	k := string(key)
	s := b.shardFor(k)

	s.mu.Lock()
	s.data[k] = nil // tombstone
	ticket := s.order.ticket()
	// Retires an in-flight durable claim, for the reason Put's does.
	delete(s.claims, k)
	s.mu.Unlock()

	b.persistAsync(s, ticket, Mutation{Key: []byte(k), Delete: true})
	return nil
}

// persistAsync writes m to storage on its own goroutine, in its shard's apply
// order (ticket), logging rather than returning a storage failure: the
// write-through contract of Put, Delete and ApplyAtomic. Close waits for it.
func (b *FastBackend) persistAsync(s *kvFastShard, ticket uint64, m Mutation) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		s.order.run(ticket, func() {
			var err error
			op := "set"
			if m.Delete {
				op = "delete"
				err = b.storage.Delete(m.Key)
			} else {
				err = b.storage.Set(m.Key, m.Value)
			}
			if err != nil {
				b.logger.Error("async storage "+op+" failed",
					"key_fingerprint", keyFingerprint(m.Key),
					"error", err,
					"shard_id", b.shardID,
					"replica_id", b.replicaID)
			}
		})
	}()
}

// keyFingerprint returns a short, non-reversible identifier for key, safe to
// place in a log line. A key is caller-supplied content and can carry a
// secret or other sensitive data a client embedded in it; a log line that
// named it verbatim leaked that content to every log sink the process
// writes to. The fingerprint is stable for a given key (so repeated
// failures of the same key are visibly the same key to an operator
// correlating log lines) without making the key itself recoverable.
func keyFingerprint(key []byte) string {
	return strconv.FormatUint(xxhash.Sum64(key), 16)
}

// PutDurable writes the cache shard and then persists SYNCHRONOUSLY, on the
// calling goroutine, returning the storage failure instead of logging it.
//
// It is the guarded half of this backend's two write contracts (see the note
// above the interface assertion). kv.ResultStored is the answer to "did I win
// the claim?" — the identity index uses it to decide that a role id, tenant id
// or single-use token belongs to this caller and to nobody else — so a claim
// acknowledged before it is durable is two holders after a restart, the exact
// outcome the guard exists to prevent.
//
// A failed write WITHDRAWS the cache entry rather than leaving it advertising a
// write that never landed, which is what persistClaimLocked does; see
// claim.withdraw for why the entry is restored only while it is still this
// write's.
func (b *FastBackend) PutDurable(key, value []byte) error {
	k := string(key)
	owned := make([]byte, len(value))
	copy(owned, value)

	s := b.shardFor(k)
	s.mu.Lock()
	claimed := cacheClaimLocked(s.data, s.claims, &s.order, k, owned)
	s.mu.Unlock()

	return b.persistDurable(s, claimed, k, owned, b.storage.Set, "put_durable")
}

// DeleteDurable tombstones the cache shard and then removes the key from
// storage SYNCHRONOUSLY, returning the failure instead of logging it.
//
// It is the delete twin of PutDurable and not a flag on it, because the two
// differ in exactly one thing — storage.Delete rather than storage.Set — and
// every other property must stay identical.
func (b *FastBackend) DeleteDurable(key []byte) error {
	k := string(key)

	s := b.shardFor(k)
	s.mu.Lock()
	// nil is the TOMBSTONE this cache records a delete as, not an absence.
	claimed := cacheClaimLocked(s.data, s.claims, &s.order, k, nil)
	s.mu.Unlock()

	return b.persistDurable(s, claimed, k,
		nil, func(dk, _ []byte) error { return b.storage.Delete(dk) }, "delete_durable")
}

// persistDurable is the synchronous storage write shared by the durable pair.
// The decision lives in persistClaimLocked, shared with the two sharded state
// machines; this adds only the log line, which needs this replica's identity.
//
// The write waits for this shard's earlier ASYNCHRONOUS writes first, through
// the same persistOrder ticket cacheClaimLocked drew. Skipping the wait would
// let a Put or Delete accepted BEFORE this one land after it, so a write
// reported durable could be overwritten by an older one. The wait cannot
// stall: the writers ahead run on their own goroutines and need nothing this
// one holds.
func (b *FastBackend) persistDurable(
	s *kvFastShard,
	claimed claim,
	key string,
	value []byte,
	write func(k, v []byte) error,
	op string,
) error {
	err := persistClaimLocked(&s.mu, s.data, s.claims, &s.order, write, op, claimed, key, value)
	if err != nil {
		b.logger.Error("durable storage write failed; cache entry withdrawn",
			"key_fingerprint", keyFingerprint([]byte(key)),
			"op", op,
			"error", err,
			"shard_id", b.shardID,
			"replica_id", b.replicaID)
	}
	return err
}

// List returns all keys matching the given prefix by merging storage
// and cache overlay. Tombstoned keys are excluded.
func (b *FastBackend) List(prefix []byte) ([][]byte, error) {
	p := string(prefix)
	seen := make(map[string]struct{})

	// Phase 1: scan storage via iterator.
	var iterOpts *storage.IteratorOptions
	if len(prefix) > 0 {
		upper := prefixUpperBound(prefix)
		iterOpts = &storage.IteratorOptions{LowerBound: prefix}
		if upper != nil {
			iterOpts.UpperBound = upper
		}
	}

	iter := b.storage.NewIterator(iterOpts)
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		k := string(iter.Key())
		if p == "" || strings.HasPrefix(k, p) {
			seen[k] = struct{}{}
		}
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return nil, &StorageError{Op: "list-keys", Inner: err}
	}
	if err := iter.Close(); err != nil {
		return nil, &StorageError{Op: "list-keys-close", Inner: err}
	}

	// Phase 2: overlay cache entries.
	for i := range b.shards {
		s := &b.shards[i]
		s.mu.RLock()
		for k, v := range s.data {
			if p != "" && !strings.HasPrefix(k, p) {
				continue
			}
			if v == nil {
				delete(seen, k)
			} else {
				seen[k] = struct{}{}
			}
		}
		s.mu.RUnlock()
	}

	keys := make([][]byte, 0, len(seen))
	for k := range seen {
		keys = append(keys, []byte(k))
	}
	sort.Slice(keys, func(i, j int) bool {
		return string(keys[i]) < string(keys[j])
	})
	return keys, nil
}

// Scan returns all key-value pairs matching the given prefix by merging
// storage and cache overlay. Tombstoned keys are excluded.
func (b *FastBackend) Scan(prefix []byte, limit int) (map[string][]byte, error) {
	p := string(prefix)
	if limit <= 0 {
		limit = 10000
	}

	result := make(map[string][]byte)

	// Phase 1: scan storage via iterator.
	upperBound := prefixUpperBound(prefix)
	opts := &storage.IteratorOptions{LowerBound: prefix}
	if upperBound != nil {
		opts.UpperBound = upperBound
	}

	iter := b.storage.NewIterator(opts)
	for iter.Seek(prefix); iter.Valid() && len(result) < limit; iter.Next() {
		k := string(iter.Key())
		if !strings.HasPrefix(k, p) {
			break
		}
		v := make([]byte, len(iter.Value()))
		copy(v, iter.Value())
		result[k] = v
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return nil, &StorageError{Op: "prefix-scan", Inner: err}
	}
	if err := iter.Close(); err != nil {
		return nil, &StorageError{Op: "prefix-scan-close", Inner: err}
	}

	// Phase 2: overlay cache entries.
	for i := range b.shards {
		s := &b.shards[i]
		s.mu.RLock()
		for k, v := range s.data {
			if !strings.HasPrefix(k, p) {
				continue
			}
			if v == nil {
				delete(result, k)
			} else if len(result) < limit {
				result[k] = v
			}
		}
		s.mu.RUnlock()
	}

	return result, nil
}

// SaveSnapshot serializes the in-memory cache to the writer.
func (b *FastBackend) SaveSnapshot(w io.Writer, stopper <-chan struct{}) error {
	type entry struct {
		key   string
		value []byte
	}
	var entries []entry

	for i := range b.shards {
		s := &b.shards[i]
		s.mu.RLock()
		for k, v := range s.data {
			if v != nil {
				entries = append(entries, entry{key: k, value: v})
			}
		}
		s.mu.RUnlock()

		select {
		case <-stopper:
			return &SnapshotError{Op: "save", Inner: context.Canceled}
		default:
		}
	}

	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, uint32(len(entries)))
	if _, err := w.Write(countBuf); err != nil {
		return &SnapshotError{Op: "save", Inner: err}
	}

	buf := make([]byte, 4)
	for _, e := range entries {
		select {
		case <-stopper:
			return &SnapshotError{Op: "save", Inner: context.Canceled}
		default:
		}

		binary.LittleEndian.PutUint32(buf, uint32(len(e.key)))
		if _, err := w.Write(buf); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
		if _, err := w.Write([]byte(e.key)); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}

		binary.LittleEndian.PutUint32(buf, uint32(len(e.value)))
		if _, err := w.Write(buf); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
		if _, err := w.Write(e.value); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
	}

	return nil
}

// RecoverSnapshot replaces the cache and storage with data from the
// reader.
func (b *FastBackend) RecoverSnapshot(r io.Reader, stopper <-chan struct{}) error {
	// Wait for in-flight async persistence.
	b.wg.Wait()

	// Clear all cache shards.
	for i := range b.shards {
		s := &b.shards[i]
		s.mu.Lock()
		s.data = make(map[string][]byte)
		// The in-flight records go with the cache they describe: a claim whose
		// entry no longer exists cannot be withdrawn onto anything meaningful,
		// and every writer that drew one has finished — b.wg.Wait() above.
		s.claims = make(map[string]uint64)
		s.mu.Unlock()
	}

	// Clear storage.
	if err := b.clearStorage(); err != nil {
		return &SnapshotError{Op: "recover", Inner: err}
	}

	countBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, countBuf); err != nil {
		return &SnapshotError{Op: "recover", Inner: err}
	}
	entryCount := binary.LittleEndian.Uint32(countBuf)

	lenBuf := make([]byte, 4)
	for i := uint32(0); i < entryCount; i++ {
		select {
		case <-stopper:
			return &SnapshotError{Op: "recover", Inner: context.Canceled}
		default:
		}

		if _, err := io.ReadFull(r, lenBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}
		keyLen := binary.LittleEndian.Uint32(lenBuf)
		keyBuf := make([]byte, keyLen)
		if _, err := io.ReadFull(r, keyBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}

		if _, err := io.ReadFull(r, lenBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}
		valLen := binary.LittleEndian.Uint32(lenBuf)
		valBuf := make([]byte, valLen)
		if _, err := io.ReadFull(r, valBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}

		key := string(keyBuf)
		s := b.shardFor(key)
		s.mu.Lock()
		s.data[key] = valBuf
		s.mu.Unlock()

		// Persist synchronously.
		if err := b.storage.Set(keyBuf, valBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}
	}

	return nil
}

// GetHash computes a deterministic xxhash64 hash over non-tombstone
// key-value pairs, visited in ascending key order.
func (b *FastBackend) GetHash() (uint64, error) {
	return xxhashRecords(b.Records)
}

// Records visits every non-tombstone key-value pair in the cache in
// ascending key order: the cache is the authoritative keyspace and the set
// SaveSnapshot carries. Each shard is read under its own read lock; a view
// consistent across shards needs the state machine's batch lock held
// exclusively.
func (b *FastBackend) Records(fn RecordVisitor) error {
	var recs []kvRecord
	for i := range b.shards {
		s := &b.shards[i]
		s.mu.RLock()
		for k, v := range s.data {
			if v != nil {
				recs = append(recs, kvRecord{key: k, value: v})
			}
		}
		s.mu.RUnlock()
	}
	return visitSorted(recs, fn)
}

// Close waits for async persistence and releases resources.
func (b *FastBackend) Close() error {
	b.wg.Wait()
	return nil
}

// clearStorage removes all existing keys from the backing storage.
func (b *FastBackend) clearStorage() error {
	iter := b.storage.NewIterator(nil)
	var keys [][]byte
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		k := make([]byte, len(iter.Key()))
		copy(k, iter.Key())
		keys = append(keys, k)
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return err
	}
	if err := iter.Close(); err != nil {
		return err
	}

	// One batch, not a delete per key: a reset that dies part-way leaves the
	// storage it started from, not a mixture.
	set := make([]Mutation, len(keys))
	for i, k := range keys {
		set[i] = Mutation{Key: k, Delete: true}
	}
	return commitMutations(b.storage, set)
}

// ApplyAtomic applies the set to the cache under every touched shard's lock at
// once — taken in ascending order and released only after the last entry is
// written — so a reader sees each key before the set or after it, never part
// of it. The cache writes cannot fail, so there is nothing to roll back.
//
// Storage is then written through exactly as Put and Delete write it:
// asynchronously, one ticket per mutation drawn under the shard lock, so each
// key's writes reach storage in apply order. That is deliberately NOT a
// synchronous storage commit. This backend's durability does not come from
// its storage: the fast state machine is an in-memory one to Raft, which
// rebuilds every replica from the latest snapshot or a full log replay at each
// start, and NewKVFastCreateFunc resets the storage before that rebuild. A
// committed transaction is durable because its log entry is; a synchronous
// batch would buy an fsync on the apply path and protect nothing a restart
// reads.
func (b *FastBackend) ApplyAtomic(muts []Mutation) error {
	set := ownMutations(lastWritePerKey(muts))
	if len(set) == 0 {
		return nil
	}

	var touched []int
	seen := make(map[int]struct{}, len(set))
	for _, m := range set {
		i := b.shardIndex(string(m.Key))
		if _, dup := seen[i]; !dup {
			seen[i] = struct{}{}
			touched = append(touched, i)
		}
	}
	sort.Ints(touched)

	tickets := make([]uint64, len(set))
	for _, i := range touched {
		b.shards[i].mu.Lock()
	}
	for j, m := range set {
		s := &b.shards[b.shardIndex(string(m.Key))]
		var next []byte // nil is the cache's delete tombstone
		if !m.Delete {
			next = m.Value
		}
		s.data[string(m.Key)] = next
		tickets[j] = s.order.ticket()
		// A later write retires any in-flight durable claim on the key, as
		// Put's does.
		delete(s.claims, string(m.Key))
	}
	for j := len(touched) - 1; j >= 0; j-- {
		b.shards[touched[j]].mu.Unlock()
	}

	for j, m := range set {
		b.persistAsync(&b.shards[b.shardIndex(string(m.Key))], tickets[j], m)
	}
	return nil
}
