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

package store

import (
	"context"
	"encoding/binary"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/cespare/xxhash/v2"
	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"
)

// metaKeyLastApplied is the reserved key used to persist the last applied
// Raft log index. This key is excluded from user-facing List/GetHash operations.
const metaKeyLastApplied = "\x00__quicraft_last_applied__"

// PebbleStore implements sm.DiskStateMachine backed by PebbleDB. It provides
// persistent key-value storage with crash recovery, prefix-based listing via
// native PebbleDB iterators, and point-in-time snapshots.
//
// Thread safety: PebbleDB handles its own internal synchronization. Lookup
// may be called concurrently with Update as required by the DiskStateMachine
// contract.
type PebbleStore struct {
	mu   sync.RWMutex
	db   *pebble.DB
	dir  string
	opts *pebble.Options
}

// NewPebbleStore creates a new PebbleStore with the given PebbleDB options.
// If opts is nil, default options are used. The store must be opened via
// Open() before use.
func NewPebbleStore(opts *pebble.Options) *PebbleStore {
	if opts == nil {
		opts = &pebble.Options{}
	}
	return &PebbleStore{opts: opts}
}

// Open initializes the PebbleDB database at the given directory and returns
// the last applied Raft log index for crash recovery. Returns 0 for a fresh
// database.
func (s *PebbleStore) Open(_ context.Context, initialDir string, _ <-chan struct{}) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	db, err := pebble.Open(initialDir, s.opts)
	if err != nil {
		return 0, &OpenError{Dir: initialDir, Err: err}
	}
	s.db = db
	s.dir = initialDir

	lastApplied, err := s.readLastApplied()
	if err != nil {
		_ = s.db.Close()
		s.db = nil
		return 0, &ReadError{Key: metaKeyLastApplied, Err: err}
	}

	return lastApplied, nil
}

// readLastApplied reads the persisted last applied index from the metadata key.
func (s *PebbleStore) readLastApplied() (uint64, error) {
	val, closer, err := s.db.Get([]byte(metaKeyLastApplied))
	if err != nil {
		if err == pebble.ErrNotFound {
			return 0, nil
		}
		return 0, err
	}
	defer func() { _ = closer.Close() }()

	if len(val) != 8 {
		return 0, nil
	}
	return binary.LittleEndian.Uint64(val), nil
}

// Update applies committed entries to the PebbleDB store using an atomic batch.
// Entries are decoded using the KV protocol (OpPut, OpGet, OpDelete). The last
// applied index is persisted in the same batch for crash recovery.
func (s *PebbleStore) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return &NotOpenError{}
	}

	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()

	var lastIndex uint64
	for i, e := range entries {
		cmd, err := kv.DecodeCommand(e.Cmd)
		if err != nil {
			return err
		}

		switch cmd.Op {
		case kv.OpPut:
			if err := batch.Set(cmd.Key, cmd.Value, pebble.Sync); err != nil {
				return &WriteError{Err: err}
			}
			results[i] = sm.Result{Value: uint64(len(cmd.Value))}
		case kv.OpDelete:
			if err := batch.Delete(cmd.Key, pebble.Sync); err != nil {
				return &WriteError{Err: err}
			}
			results[i] = sm.Result{Value: 1}
		case kv.OpGet:
			results[i] = sm.Result{Value: 0}
		}

		if e.Index > lastIndex {
			lastIndex = e.Index
		}
	}

	// Persist the last applied index in the same atomic batch.
	if lastIndex > 0 {
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], lastIndex)
		if err := batch.Set([]byte(metaKeyLastApplied), buf[:], pebble.Sync); err != nil {
			return &WriteError{Err: err}
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return &WriteError{Err: err}
	}

	return nil
}

// Lookup reads state from the PebbleDB store. Supports three query types:
//   - kv.ListQuery: returns sorted []string of keys matching the prefix
//   - []byte: returns a copy of the value for the given key
//   - string: returns a copy of the value for the given key
func (s *PebbleStore) Lookup(_ context.Context, query interface{}) (interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.db == nil {
		return nil, &NotOpenError{}
	}

	if lq, ok := query.(kv.ListQuery); ok {
		return s.listKeys(lq.Prefix)
	}

	key, ok := query.([]byte)
	if !ok {
		if str, ok := query.(string); ok {
			key = []byte(str)
		} else {
			return nil, &InvalidQueryError{Reason: "query must be []byte, string, or kv.ListQuery"}
		}
	}

	return s.getKey(key)
}

// listKeys returns sorted keys matching the given prefix using a native PebbleDB
// prefix iterator. The metadata key is excluded from results.
func (s *PebbleStore) listKeys(prefix string) ([]string, error) {
	iterOpts := &pebble.IterOptions{}
	if prefix != "" {
		lower := []byte(prefix)
		upper := prefixUpperBound(lower)
		iterOpts.LowerBound = lower
		if upper != nil {
			iterOpts.UpperBound = upper
		}
	}

	iter, err := s.db.NewIter(iterOpts)
	if err != nil {
		return nil, &ReadError{Key: prefix, Err: err}
	}
	defer func() { _ = iter.Close() }()

	var keys []string
	for iter.First(); iter.Valid(); iter.Next() {
		k := string(iter.Key())
		// Exclude internal metadata keys (prefixed with \x00).
		if len(k) > 0 && k[0] == '\x00' {
			continue
		}
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	if err := iter.Error(); err != nil {
		return nil, &ReadError{Key: prefix, Err: err}
	}

	sort.Strings(keys)
	if keys == nil {
		keys = []string{}
	}
	return keys, nil
}

// prefixUpperBound computes the upper bound for a prefix scan. Returns nil
// if no upper bound can be computed (prefix is all 0xFF bytes).
func prefixUpperBound(prefix []byte) []byte {
	upper := make([]byte, len(prefix))
	copy(upper, prefix)
	for i := len(upper) - 1; i >= 0; i-- {
		if upper[i] < 0xFF {
			upper[i]++
			return upper[:i+1]
		}
	}
	return nil
}

// getKey retrieves a single key's value, returning a defensive copy.
func (s *PebbleStore) getKey(key []byte) ([]byte, error) {
	val, closer, err := s.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, &kv.KeyNotFoundError{Key: string(key)}
		}
		return nil, &ReadError{Key: string(key), Err: err}
	}
	defer func() { _ = closer.Close() }()

	cp := make([]byte, len(val))
	copy(cp, val)
	return cp, nil
}

// BytesLookup performs a zero-allocation byte-slice lookup for the NALookup
// interface. Returns a defensive copy of the value.
func (s *PebbleStore) BytesLookup(key []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.db == nil {
		return nil, &NotOpenError{}
	}

	return s.getKey(key)
}

// Sync flushes all pending writes to durable storage. Called before snapshot
// operations to ensure consistency.
func (s *PebbleStore) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return &NotOpenError{}
	}

	if err := s.db.Flush(); err != nil {
		return &WriteError{Err: err}
	}
	return nil
}

// PrepareSnapshot creates a point-in-time snapshot of the database for use
// by SaveSnapshot. The returned *pebble.Snapshot must be closed after use.
func (s *PebbleStore) PrepareSnapshot() (interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.db == nil {
		return nil, &NotOpenError{}
	}

	return s.db.NewSnapshot(), nil
}

// SaveSnapshot serializes the database state captured by PrepareSnapshot
// to the writer using the same wire format as the in-memory KV stores:
// [count:4 LE] then for each pair: [keyLen:4 LE][key][valLen:4 LE][val].
// Internal metadata keys are excluded from the snapshot.
func (s *PebbleStore) SaveSnapshot(_ context.Context, snapshotCtx interface{}, w io.Writer, stopper <-chan struct{}) error {
	snap, ok := snapshotCtx.(*pebble.Snapshot)
	if !ok {
		return &SnapshotError{Op: "save", Err: &InvalidQueryError{Reason: "invalid snapshot context type"}}
	}
	defer func() { _ = snap.Close() }()

	// First pass: count non-metadata entries.
	iter, err := snap.NewIter(nil)
	if err != nil {
		return &SnapshotError{Op: "save", Err: err}
	}

	var count uint32
	for iter.First(); iter.Valid(); iter.Next() {
		k := iter.Key()
		if len(k) > 0 && k[0] == '\x00' {
			continue
		}
		count++
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return &SnapshotError{Op: "save", Err: err}
	}
	_ = iter.Close()

	if err := binary.Write(w, binary.LittleEndian, count); err != nil {
		return &SnapshotError{Op: "save", Err: err}
	}

	// Second pass: write entries.
	iter, err = snap.NewIter(nil)
	if err != nil {
		return &SnapshotError{Op: "save", Err: err}
	}
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		select {
		case <-stopper:
			return context.Canceled
		default:
		}

		k := iter.Key()
		if len(k) > 0 && k[0] == '\x00' {
			continue
		}

		v, err := iter.ValueAndErr()
		if err != nil {
			return &SnapshotError{Op: "save", Err: err}
		}

		if err := binary.Write(w, binary.LittleEndian, uint32(len(k))); err != nil {
			return &SnapshotError{Op: "save", Err: err}
		}
		if _, err := w.Write(k); err != nil {
			return &SnapshotError{Op: "save", Err: err}
		}
		if err := binary.Write(w, binary.LittleEndian, uint32(len(v))); err != nil {
			return &SnapshotError{Op: "save", Err: err}
		}
		if _, err := w.Write(v); err != nil {
			return &SnapshotError{Op: "save", Err: err}
		}
	}

	return iter.Error()
}

// RecoverFromSnapshot restores the database state from the reader, replacing
// all existing data. Reads the same wire format written by SaveSnapshot.
func (s *PebbleStore) RecoverFromSnapshot(_ context.Context, r io.Reader, stopper <-chan struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return &NotOpenError{}
	}

	// Clear all existing data.
	if err := s.clearAll(); err != nil {
		return &SnapshotError{Op: "recover", Err: err}
	}

	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return &SnapshotError{Op: "recover", Err: err}
	}

	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()

	for i := uint32(0); i < count; i++ {
		select {
		case <-stopper:
			return context.Canceled
		default:
		}

		var keyLen uint32
		if err := binary.Read(r, binary.LittleEndian, &keyLen); err != nil {
			return &SnapshotError{Op: "recover", Err: err}
		}
		key := make([]byte, keyLen)
		if _, err := io.ReadFull(r, key); err != nil {
			return &SnapshotError{Op: "recover", Err: err}
		}

		var valLen uint32
		if err := binary.Read(r, binary.LittleEndian, &valLen); err != nil {
			return &SnapshotError{Op: "recover", Err: err}
		}
		val := make([]byte, valLen)
		if _, err := io.ReadFull(r, val); err != nil {
			return &SnapshotError{Op: "recover", Err: err}
		}

		if err := batch.Set(key, val, pebble.Sync); err != nil {
			return &SnapshotError{Op: "recover", Err: err}
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return &SnapshotError{Op: "recover", Err: err}
	}

	return nil
}

// clearAll removes all entries from the database using a range deletion.
func (s *PebbleStore) clearAll() error {
	iter, err := s.db.NewIter(nil)
	if err != nil {
		return err
	}
	defer func() { _ = iter.Close() }()

	if !iter.First() {
		return iter.Error()
	}
	firstKey := make([]byte, len(iter.Key()))
	copy(firstKey, iter.Key())

	if !iter.Last() {
		return iter.Error()
	}
	// DeleteRange is [start, end) so we need end > lastKey.
	lastKey := make([]byte, len(iter.Key())+1)
	copy(lastKey, iter.Key())
	lastKey[len(lastKey)-1] = 0xFF

	if err := iter.Error(); err != nil {
		return err
	}

	return s.db.DeleteRange(firstKey, lastKey, pebble.Sync)
}

// Close releases all resources held by the PebbleDB store.
func (s *PebbleStore) Close(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return nil
	}

	err := s.db.Close()
	s.db = nil
	return err
}

// GetHash computes a deterministic xxhash64 hash of all user key-value pairs
// in sorted order. Internal metadata keys are excluded.
func (s *PebbleStore) GetHash() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.db == nil {
		return 0, &NotOpenError{}
	}

	h := xxhash.New()

	iter, err := s.db.NewIter(nil)
	if err != nil {
		return 0, &ReadError{Key: "", Err: err}
	}
	defer func() { _ = iter.Close() }()

	// PebbleDB iterates in sorted order by default.
	for iter.First(); iter.Valid(); iter.Next() {
		k := iter.Key()
		if len(k) > 0 && k[0] == '\x00' {
			continue
		}
		v, err := iter.ValueAndErr()
		if err != nil {
			return 0, &ReadError{Key: string(k), Err: err}
		}
		_, _ = h.Write(k)
		_, _ = h.Write(v)
	}
	if err := iter.Error(); err != nil {
		return 0, &ReadError{Key: "", Err: err}
	}

	return h.Sum64(), nil
}

// Compile-time assertions.
var (
	_ sm.DiskStateMachine = (*PebbleStore)(nil)
	_ sm.Hasher           = (*PebbleStore)(nil)
	_ sm.NALookup         = (*PebbleStore)(nil)
)
