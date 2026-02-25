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

package mvcc

import (
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Store is a multi-version concurrency control engine backed by PebbleDB.
// It maintains versioned key-value pairs with revision metadata, supporting
// point-in-time reads, range queries, and compaction of old revisions.
//
// Thread safety: PebbleDB handles its own internal synchronization.
// The atomic revision counter is lock-free.
type Store struct {
	db        *pebble.DB
	revision  atomic.Int64
	compacted atomic.Int64
}

// New creates a new MVCC store backed by the given PebbleDB instance.
// The store loads the current revision and compacted marker from the
// database if they exist.
func New(db *pebble.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.loadRevision(); err != nil {
		return nil, err
	}
	if err := s.loadCompacted(); err != nil {
		return nil, err
	}
	return s, nil
}

// loadRevision reads the persisted revision counter from PebbleDB.
func (s *Store) loadRevision() error {
	val, closer, err := s.db.Get(revisionKey())
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil // Fresh database, revision starts at 0.
		}
		return &StoreError{Op: "load revision", Err: err}
	}
	defer func() { _ = closer.Close() }()

	rev, err := decodeRevision(val)
	if err != nil {
		return err
	}
	s.revision.Store(rev)
	return nil
}

// loadCompacted reads the persisted compacted revision from PebbleDB.
func (s *Store) loadCompacted() error {
	val, closer, err := s.db.Get(compactedKey())
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil
		}
		return &StoreError{Op: "load compacted", Err: err}
	}
	defer func() { _ = closer.Close() }()

	rev, err := decodeRevision(val)
	if err != nil {
		return err
	}
	s.compacted.Store(rev)
	return nil
}

// nextRevision atomically increments and returns the next revision.
func (s *Store) nextRevision() int64 {
	return s.revision.Add(1)
}

// CurrentRevision returns the current global revision.
func (s *Store) CurrentRevision() int64 {
	return s.revision.Load()
}

// CompactedRevision returns the last compacted revision. Reads at or
// below this revision will return RevisionCompactedError.
func (s *Store) CompactedRevision() int64 {
	return s.compacted.Load()
}

// Put stores a key-value pair at a new revision. Returns the resulting
// KeyValue with full revision metadata.
func (s *Store) Put(key, value []byte, leaseID int64) (*service.KeyValue, error) {
	rev := s.nextRevision()

	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()

	// Read existing metadata for version tracking.
	var meta metadata
	existingMeta, err := s.getMetadata(key)
	if err == nil {
		meta = existingMeta
		meta.ModRevision = rev
		meta.Version++
		meta.Lease = leaseID
	} else {
		// New key.
		meta = metadata{
			CreateRevision: rev,
			ModRevision:    rev,
			Version:        1,
			Lease:          leaseID,
		}
	}

	// Write data entry.
	dk := encodeDataKey(key, rev)
	dv := encodeDataValue(value)
	if err := batch.Set(dk, dv, pebble.Sync); err != nil {
		return nil, &StoreError{Op: "put data", Err: err}
	}

	// Write metadata.
	mk := encodeMetaKey(key)
	if err := batch.Set(mk, meta.encode(), pebble.Sync); err != nil {
		return nil, &StoreError{Op: "put meta", Err: err}
	}

	// Persist revision counter.
	if err := batch.Set(revisionKey(), encodeRevision(rev), pebble.Sync); err != nil {
		return nil, &StoreError{Op: "put revision", Err: err}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, &StoreError{Op: "put commit", Err: err}
	}

	return &service.KeyValue{
		Key:            key,
		Value:          value,
		CreateRevision: meta.CreateRevision,
		ModRevision:    meta.ModRevision,
		Version:        meta.Version,
		LeaseID:        meta.Lease,
	}, nil
}

// Get retrieves the latest value for a key. Returns KeyNotFoundError
// if the key does not exist.
func (s *Store) Get(key []byte) (*service.KeyValue, error) {
	meta, err := s.getMetadata(key)
	if err != nil {
		return nil, &KeyNotFoundError{Key: string(key)}
	}

	value, err := s.getDataAtRevision(key, meta.ModRevision)
	if err != nil {
		return nil, err
	}

	return &service.KeyValue{
		Key:            key,
		Value:          value,
		CreateRevision: meta.CreateRevision,
		ModRevision:    meta.ModRevision,
		Version:        meta.Version,
		LeaseID:        meta.Lease,
	}, nil
}

// GetAtRevision retrieves the value of a key at a specific revision.
// Returns RevisionCompactedError if the revision has been compacted,
// or RevisionNotFoundError if no version exists at or before the
// requested revision.
func (s *Store) GetAtRevision(key []byte, revision int64) (*service.KeyValue, error) {
	compacted := s.compacted.Load()
	if revision > 0 && revision <= compacted {
		return nil, &RevisionCompactedError{
			Requested: revision,
			Compacted: compacted,
		}
	}

	// Find the data entry at or before the requested revision.
	kv, err := s.findAtRevision(key, revision)
	if err != nil {
		return nil, err
	}

	return kv, nil
}

// Delete removes a key by writing a tombstone at a new revision.
// Returns the previous KeyValue state. Returns KeyNotFoundError if
// the key does not exist.
func (s *Store) Delete(key []byte) (*service.KeyValue, error) {
	meta, err := s.getMetadata(key)
	if err != nil {
		return nil, &KeyNotFoundError{Key: string(key)}
	}

	// Read the current value for the return.
	prevValue, err := s.getDataAtRevision(key, meta.ModRevision)
	if err != nil {
		return nil, err
	}

	rev := s.nextRevision()
	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()

	// Write tombstone data entry.
	dk := encodeDataKey(key, rev)
	if err := batch.Set(dk, encodeTombstone(), pebble.Sync); err != nil {
		return nil, &StoreError{Op: "delete data", Err: err}
	}

	// Remove metadata (key is no longer "live").
	mk := encodeMetaKey(key)
	if err := batch.Delete(mk, pebble.Sync); err != nil {
		return nil, &StoreError{Op: "delete meta", Err: err}
	}

	// Persist revision counter.
	if err := batch.Set(revisionKey(), encodeRevision(rev), pebble.Sync); err != nil {
		return nil, &StoreError{Op: "delete revision", Err: err}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, &StoreError{Op: "delete commit", Err: err}
	}

	return &service.KeyValue{
		Key:            key,
		Value:          prevValue,
		CreateRevision: meta.CreateRevision,
		ModRevision:    meta.ModRevision,
		Version:        meta.Version,
		LeaseID:        meta.Lease,
	}, nil
}

// Range returns all key-value pairs in the range [start, end).
// If end is nil, returns only the key matching start.
// Returns the key-values and the current store revision.
func (s *Store) Range(start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, error) {
	ro := service.ApplyRangeOptions(opts)
	currentRev := s.CurrentRevision()

	targetRev := currentRev
	if ro.Revision > 0 {
		compacted := s.compacted.Load()
		if ro.Revision <= compacted {
			return nil, 0, &RevisionCompactedError{
				Requested: ro.Revision,
				Compacted: compacted,
			}
		}
		targetRev = ro.Revision
	}

	// Single key lookup if no end specified.
	if end == nil {
		kv, err := s.rangeGetSingle(start, targetRev, ro)
		if err != nil {
			return nil, currentRev, nil // Key not found is not an error for range.
		}
		return []*service.KeyValue{kv}, currentRev, nil
	}

	// Range scan over metadata keys.
	lowerMeta := encodeMetaKey(start)
	upperMeta := encodeMetaKey(end)

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lowerMeta,
		UpperBound: upperMeta,
	})
	if err != nil {
		return nil, 0, &StoreError{Op: "range iter", Err: err}
	}
	defer func() { _ = iter.Close() }()

	var results []*service.KeyValue
	var count int64

	for iter.First(); iter.Valid(); iter.Next() {
		userKey, err := decodeMetaKey(iter.Key())
		if err != nil {
			continue
		}

		if ro.CountOnly {
			count++
			continue
		}

		kv, err := s.findAtRevision(userKey, targetRev)
		if err != nil {
			continue // Key might not exist at this revision.
		}

		if ro.KeysOnly {
			kv.Value = nil
		}

		results = append(results, kv)
		count++

		if ro.Limit > 0 && count >= ro.Limit {
			break
		}
	}

	if err := iter.Error(); err != nil {
		return nil, 0, &StoreError{Op: "range scan", Err: err}
	}

	if ro.CountOnly {
		return nil, count, nil
	}

	if results == nil {
		results = []*service.KeyValue{}
	}

	return results, currentRev, nil
}

// Compact removes all data entries at or below the given revision.
// The compacted revision is persisted to prevent future reads at
// compacted revisions.
func (s *Store) Compact(revision int64) error {
	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()

	// Iterate all data entries and delete those at or below the revision.
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte{dataPrefix},
		UpperBound: []byte{dataPrefix + 1},
	})
	if err != nil {
		return &CompactionError{Err: err}
	}
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		_, entryRev, err := decodeDataKey(iter.Key())
		if err != nil {
			continue
		}
		if entryRev <= revision {
			keyCopy := make([]byte, len(iter.Key()))
			copy(keyCopy, iter.Key())
			if err := batch.Delete(keyCopy, pebble.Sync); err != nil {
				return &CompactionError{Err: err}
			}
		}
	}

	if err := iter.Error(); err != nil {
		return &CompactionError{Err: err}
	}

	// Persist the compacted revision marker.
	if err := batch.Set(compactedKey(), encodeRevision(revision), pebble.Sync); err != nil {
		return &CompactionError{Err: err}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return &CompactionError{Err: err}
	}

	s.compacted.Store(revision)
	return nil
}

// PutInBatch writes a key-value pair into the given PebbleDB batch
// at the specified revision. Used by the state machine to apply MVCC
// operations within a Raft entry batch. Returns the resulting KeyValue.
func (s *Store) PutInBatch(batch *pebble.Batch, key, value []byte, leaseID int64, rev int64) (*service.KeyValue, error) {
	// Read existing metadata for version tracking.
	var meta metadata
	existingMeta, err := s.getMetadata(key)
	if err == nil {
		meta = existingMeta
		meta.ModRevision = rev
		meta.Version++
		meta.Lease = leaseID
	} else {
		meta = metadata{
			CreateRevision: rev,
			ModRevision:    rev,
			Version:        1,
			Lease:          leaseID,
		}
	}

	// Write data entry.
	dk := encodeDataKey(key, rev)
	dv := encodeDataValue(value)
	if err := batch.Set(dk, dv, pebble.Sync); err != nil {
		return nil, &StoreError{Op: "batch put data", Err: err}
	}

	// Write metadata.
	mk := encodeMetaKey(key)
	if err := batch.Set(mk, meta.encode(), pebble.Sync); err != nil {
		return nil, &StoreError{Op: "batch put meta", Err: err}
	}

	return &service.KeyValue{
		Key:            key,
		Value:          value,
		CreateRevision: meta.CreateRevision,
		ModRevision:    rev,
		Version:        meta.Version,
		LeaseID:        meta.Lease,
	}, nil
}

// DeleteInBatch writes a tombstone for a key in the given PebbleDB batch
// at the specified revision. Used by the state machine. Returns the
// previous KeyValue, or nil if the key didn't exist.
func (s *Store) DeleteInBatch(batch *pebble.Batch, key []byte, rev int64) (*service.KeyValue, error) {
	var prevKV *service.KeyValue
	meta, err := s.getMetadata(key)
	if err == nil {
		prevValue, err := s.getDataAtRevision(key, meta.ModRevision)
		if err == nil {
			prevKV = &service.KeyValue{
				Key:            key,
				Value:          prevValue,
				CreateRevision: meta.CreateRevision,
				ModRevision:    meta.ModRevision,
				Version:        meta.Version,
				LeaseID:        meta.Lease,
			}
		}
	}

	// Write tombstone.
	dk := encodeDataKey(key, rev)
	if err := batch.Set(dk, encodeTombstone(), pebble.Sync); err != nil {
		return nil, &StoreError{Op: "batch delete data", Err: err}
	}

	// Remove metadata.
	mk := encodeMetaKey(key)
	if err := batch.Delete(mk, pebble.Sync); err != nil {
		return nil, &StoreError{Op: "batch delete meta", Err: err}
	}

	return prevKV, nil
}

// SetRevision updates the revision counter. Used by the state machine
// when applying entries that carry pre-assigned revisions.
func (s *Store) SetRevision(rev int64) {
	s.revision.Store(rev)
}

// DB returns the underlying PebbleDB instance.
func (s *Store) DB() *pebble.DB {
	return s.db
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// getMetadata reads the current metadata for a key.
func (s *Store) getMetadata(key []byte) (metadata, error) {
	mk := encodeMetaKey(key)
	val, closer, err := s.db.Get(mk)
	if err != nil {
		if err == pebble.ErrNotFound {
			return metadata{}, &KeyNotFoundError{Key: string(key)}
		}
		return metadata{}, &StoreError{Op: "get meta", Err: err}
	}
	defer func() { _ = closer.Close() }()

	return decodeMetadata(val)
}

// getDataAtRevision reads the data value at an exact revision.
func (s *Store) getDataAtRevision(key []byte, revision int64) ([]byte, error) {
	dk := encodeDataKey(key, revision)
	val, closer, err := s.db.Get(dk)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, &RevisionNotFoundError{Key: string(key), Revision: revision}
		}
		return nil, &StoreError{Op: "get data", Err: err}
	}
	defer func() { _ = closer.Close() }()

	value, isTombstone, err := decodeDataValue(val)
	if err != nil {
		return nil, err
	}
	if isTombstone {
		return nil, &KeyNotFoundError{Key: string(key)}
	}

	// Return a defensive copy.
	cp := make([]byte, len(value))
	copy(cp, value)
	return cp, nil
}

// findAtRevision finds the data entry at or before the requested revision.
// Uses PebbleDB's SeekGE with complement encoding to find the correct entry.
func (s *Store) findAtRevision(key []byte, revision int64) (*service.KeyValue, error) {
	prefix := dataKeyPrefix(key)
	upper := dataKeyUpperBound(key)
	seekKey := encodeDataKey(key, revision)

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upper,
	})
	if err != nil {
		return nil, &StoreError{Op: "find at revision", Err: err}
	}
	defer func() { _ = iter.Close() }()

	// SeekGE with complement encoding: complement(revision) seeks to the
	// first entry with complement >= complement(revision), which means
	// entryRevision <= revision (newest entry at or before requested revision).
	if !iter.SeekGE(seekKey) {
		if err := iter.Error(); err != nil {
			return nil, &StoreError{Op: "find at revision seek", Err: err}
		}
		return nil, &RevisionNotFoundError{Key: string(key), Revision: revision}
	}

	_, entryRev, err := decodeDataKey(iter.Key())
	if err != nil {
		return nil, err
	}

	val, err := iter.ValueAndErr()
	if err != nil {
		return nil, &StoreError{Op: "find at revision value", Err: err}
	}

	value, isTombstone, err := decodeDataValue(val)
	if err != nil {
		return nil, err
	}
	if isTombstone {
		return nil, &KeyNotFoundError{Key: string(key)}
	}

	// Read metadata for full KeyValue construction. If metadata doesn't
	// exist (key was deleted after this revision), construct partial metadata.
	meta, metaErr := s.getMetadata(key)
	var kv service.KeyValue
	if metaErr == nil {
		kv = service.KeyValue{
			Key:            key,
			Value:          make([]byte, len(value)),
			CreateRevision: meta.CreateRevision,
			ModRevision:    entryRev,
			Version:        meta.Version,
			LeaseID:        meta.Lease,
		}
	} else {
		// Key was deleted; provide revision info from the data entry.
		kv = service.KeyValue{
			Key:            key,
			Value:          make([]byte, len(value)),
			CreateRevision: entryRev,
			ModRevision:    entryRev,
			Version:        1,
		}
	}
	copy(kv.Value, value)

	return &kv, nil
}

// rangeGetSingle handles Range with nil end (single key lookup).
func (s *Store) rangeGetSingle(key []byte, targetRev int64, ro service.RangeOptions) (*service.KeyValue, error) {
	kv, err := s.findAtRevision(key, targetRev)
	if err != nil {
		return nil, err
	}
	if ro.KeysOnly {
		kv.Value = nil
	}
	return kv, nil
}
