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
	"bytes"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Store is a multi-version concurrency control engine backed by storage.Storage.
// It maintains versioned key-value pairs with revision metadata, supporting
// point-in-time reads, range queries, and compaction of old revisions.
//
// Thread safety: The underlying storage.Storage implementation handles its own
// internal synchronization. The atomic revision counter is lock-free.
type Store struct {
	storage   storage.Storage
	revision  atomic.Int64
	compacted atomic.Int64
}

// New creates a new MVCC store backed by the given storage.Storage instance.
// The store loads the current revision and compacted marker from the
// storage if they exist.
func New(store storage.Storage) (*Store, error) {
	s := &Store{storage: store}
	if err := s.loadRevision(); err != nil {
		return nil, err
	}
	if err := s.loadCompacted(); err != nil {
		return nil, err
	}
	return s, nil
}

// loadRevision reads the persisted revision counter from storage.
func (s *Store) loadRevision() error {
	val, err := s.storage.Get(RevisionKey())
	if err != nil {
		return &StoreError{Op: "load revision", Err: err}
	}
	if val == nil {
		return nil // Fresh database, revision starts at 0.
	}

	rev, err := DecodeRevision(val)
	if err != nil {
		return err
	}
	s.revision.Store(rev)
	return nil
}

// loadCompacted reads the persisted compacted revision from storage.
func (s *Store) loadCompacted() error {
	val, err := s.storage.Get(CompactedKey())
	if err != nil {
		return &StoreError{Op: "load compacted", Err: err}
	}
	if val == nil {
		return nil
	}

	rev, err := DecodeRevision(val)
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

	batch := s.storage.Batch()
	defer func() { _ = batch.Close() }()

	// Read existing metadata for version tracking.
	var meta KeyMeta
	existingMeta, err := s.getMetadata(key)
	if err == nil {
		meta = *existingMeta
		meta.ModRevision = rev
		meta.Version++
		meta.LeaseID = leaseID
	} else {
		// New key.
		meta = KeyMeta{
			CreateRevision: rev,
			ModRevision:    rev,
			Version:        1,
			LeaseID:        leaseID,
		}
	}

	// Write data entry.
	dk := EncodeDataKey(key, rev)
	dv := EncodeValue(value)
	if err := batch.Set(dk, dv); err != nil {
		return nil, &StoreError{Op: "put data", Err: err}
	}

	// Write metadata.
	mk := EncodeMetaKey(key)
	if err := batch.Set(mk, EncodeMeta(&meta)); err != nil {
		return nil, &StoreError{Op: "put meta", Err: err}
	}

	// Persist revision counter.
	if err := batch.Set(RevisionKey(), EncodeRevision(rev)); err != nil {
		return nil, &StoreError{Op: "put revision", Err: err}
	}

	if err := batch.Commit(); err != nil {
		return nil, &StoreError{Op: "put commit", Err: err}
	}

	return &service.KeyValue{
		Key:            key,
		Value:          value,
		CreateRevision: meta.CreateRevision,
		ModRevision:    meta.ModRevision,
		Version:        meta.Version,
		LeaseID:        meta.LeaseID,
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
		LeaseID:        meta.LeaseID,
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
	batch := s.storage.Batch()
	defer func() { _ = batch.Close() }()

	// Write tombstone data entry.
	dk := EncodeDataKey(key, rev)
	if err := batch.Set(dk, EncodeTombstone()); err != nil {
		return nil, &StoreError{Op: "delete data", Err: err}
	}

	// Remove metadata (key is no longer "live").
	mk := EncodeMetaKey(key)
	if err := batch.Delete(mk); err != nil {
		return nil, &StoreError{Op: "delete meta", Err: err}
	}

	// Persist revision counter.
	if err := batch.Set(RevisionKey(), EncodeRevision(rev)); err != nil {
		return nil, &StoreError{Op: "delete revision", Err: err}
	}

	if err := batch.Commit(); err != nil {
		return nil, &StoreError{Op: "delete commit", Err: err}
	}

	return &service.KeyValue{
		Key:            key,
		Value:          prevValue,
		CreateRevision: meta.CreateRevision,
		ModRevision:    meta.ModRevision,
		Version:        meta.Version,
		LeaseID:        meta.LeaseID,
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

	// Range scan over all metadata keys, filtering by user key in [start, end).
	// With length-prefixed meta keys, we must scan the entire meta namespace
	// because keys of different lengths sort by length prefix first.
	iter := s.storage.NewIterator(&storage.IteratorOptions{
		LowerBound: MetaLowerBound(),
		UpperBound: MetaUpperBound(),
	})
	defer func() { _ = iter.Close() }()

	var results []*service.KeyValue
	var count int64

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		userKey, err := DecodeMetaKey(iter.Key())
		if err != nil {
			continue
		}

		// Filter by user key range [start, end).
		if !IsInRange(userKey, start, end) {
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

// Compact removes old data entries at revisions at or below the given revision,
// while preserving the latest (highest) revision per unique key. This ensures
// that keys whose only write is within the compaction range (e.g., system keys
// written during bootstrap) remain readable after compaction.
//
// Data keys are sorted by [prefix][keyLen][key][complement(revision)], so within
// each user key group the highest revision appears first in iteration order.
// The method tracks the current key prefix and skips the first entry per group
// (the latest revision), deleting only subsequent older entries at or below the
// compaction revision.
//
// Metadata is preserved so that Get and Delete can still detect the key's
// existence via its metadata entry.
func (s *Store) Compact(revision int64) error {
	batch := s.storage.Batch()
	defer func() { _ = batch.Close() }()

	// Iterate all data entries.
	iter := s.storage.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte{prefixData},
		UpperBound: []byte{prefixData + 1},
	})
	defer func() { _ = iter.Close() }()

	// Track the current user key prefix to detect group boundaries.
	// Data key format: [0x01][keyLen:8 BE][key][complement:8 BE]
	// The prefix for grouping is everything except the last 8 bytes.
	var currentPrefix []byte

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		raw := iter.Key()
		if len(raw) < 17 { // 1 + 8 + 0 + 8 minimum
			continue
		}

		_, entryRev, err := DecodeDataKey(raw)
		if err != nil {
			continue
		}

		// Extract the key prefix (everything except the trailing 8-byte complement).
		keyPrefix := raw[:len(raw)-8]

		// Detect new key group: skip the first entry (latest revision) in each group.
		if !bytes.Equal(keyPrefix, currentPrefix) {
			currentPrefix = make([]byte, len(keyPrefix))
			copy(currentPrefix, keyPrefix)
			// This is the latest revision for this key. Preserve it.
			continue
		}

		// Subsequent (older) entry in the same key group.
		// Delete if at or below the compaction point.
		if entryRev <= revision {
			keyCopy := make([]byte, len(raw))
			copy(keyCopy, raw)
			if err := batch.Delete(keyCopy); err != nil {
				return &CompactionError{Err: err}
			}
		}
	}

	if err := iter.Error(); err != nil {
		return &CompactionError{Err: err}
	}

	// Persist the compacted revision marker.
	if err := batch.Set(CompactedKey(), EncodeRevision(revision)); err != nil {
		return &CompactionError{Err: err}
	}

	if err := batch.Commit(); err != nil {
		return &CompactionError{Err: err}
	}

	s.compacted.Store(revision)
	return nil
}

// PutInBatch writes a key-value pair into the given storage.Batch
// at the specified revision. Used by the state machine to apply MVCC
// operations within a Raft entry batch. Returns the resulting KeyValue.
func (s *Store) PutInBatch(batch storage.Batch, key, value []byte, leaseID int64, rev int64) (*service.KeyValue, error) {
	// Read existing metadata for version tracking.
	var meta KeyMeta
	existingMeta, err := s.getMetadata(key)
	if err == nil {
		meta = *existingMeta
		meta.ModRevision = rev
		meta.Version++
		meta.LeaseID = leaseID
	} else {
		meta = KeyMeta{
			CreateRevision: rev,
			ModRevision:    rev,
			Version:        1,
			LeaseID:        leaseID,
		}
	}

	// Write data entry.
	dk := EncodeDataKey(key, rev)
	dv := EncodeValue(value)
	if err := batch.Set(dk, dv); err != nil {
		return nil, &StoreError{Op: "batch put data", Err: err}
	}

	// Write metadata.
	mk := EncodeMetaKey(key)
	if err := batch.Set(mk, EncodeMeta(&meta)); err != nil {
		return nil, &StoreError{Op: "batch put meta", Err: err}
	}

	return &service.KeyValue{
		Key:            key,
		Value:          value,
		CreateRevision: meta.CreateRevision,
		ModRevision:    rev,
		Version:        meta.Version,
		LeaseID:        meta.LeaseID,
	}, nil
}

// DeleteInBatch writes a tombstone for a key in the given storage.Batch
// at the specified revision. Used by the state machine. Returns the
// previous KeyValue, or nil if the key didn't exist.
func (s *Store) DeleteInBatch(batch storage.Batch, key []byte, rev int64) (*service.KeyValue, error) {
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
				LeaseID:        meta.LeaseID,
			}
		}
	}

	// Write tombstone.
	dk := EncodeDataKey(key, rev)
	if err := batch.Set(dk, EncodeTombstone()); err != nil {
		return nil, &StoreError{Op: "batch delete data", Err: err}
	}

	// Remove metadata.
	mk := EncodeMetaKey(key)
	if err := batch.Delete(mk); err != nil {
		return nil, &StoreError{Op: "batch delete meta", Err: err}
	}

	return prevKV, nil
}

// SetRevision updates the revision counter. Used by the state machine
// when applying entries that carry pre-assigned revisions.
func (s *Store) SetRevision(rev int64) {
	s.revision.Store(rev)
}

// Storage returns the underlying storage.Storage instance.
func (s *Store) Storage() storage.Storage {
	return s.storage
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// getMetadata reads the current metadata for a key.
func (s *Store) getMetadata(key []byte) (*KeyMeta, error) {
	mk := EncodeMetaKey(key)
	val, err := s.storage.Get(mk)
	if err != nil {
		return nil, &StoreError{Op: "get meta", Err: err}
	}
	if val == nil {
		return nil, &KeyNotFoundError{Key: string(key)}
	}

	return DecodeMeta(val)
}

// getDataAtRevision reads the data value at an exact revision.
func (s *Store) getDataAtRevision(key []byte, revision int64) ([]byte, error) {
	dk := EncodeDataKey(key, revision)
	val, err := s.storage.Get(dk)
	if err != nil {
		return nil, &StoreError{Op: "get data", Err: err}
	}
	if val == nil {
		return nil, &RevisionNotFoundError{Key: string(key), Revision: revision}
	}

	value, isTombstone, err := DecodeValue(val)
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
// Uses storage.Iterator's Seek with complement encoding to find the correct entry.
func (s *Store) findAtRevision(key []byte, revision int64) (*service.KeyValue, error) {
	prefix := DataKeyPrefix(key)
	upper := DataKeyUpperBound(key)
	seekKey := EncodeDataKey(key, revision)

	iter := s.storage.NewIterator(&storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: upper,
	})
	defer func() { _ = iter.Close() }()

	// Seek with complement encoding: complement(revision) seeks to the
	// first entry with complement >= complement(revision), which means
	// entryRevision <= revision (newest entry at or before requested revision).
	if !iter.Seek(seekKey) {
		if err := iter.Error(); err != nil {
			return nil, &StoreError{Op: "find at revision seek", Err: err}
		}
		return nil, &RevisionNotFoundError{Key: string(key), Revision: revision}
	}

	_, entryRev, err := DecodeDataKey(iter.Key())
	if err != nil {
		return nil, err
	}

	val := iter.Value()
	if err := iter.Error(); err != nil {
		return nil, &StoreError{Op: "find at revision value", Err: err}
	}

	value, isTombstone, err := DecodeValue(val)
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
			LeaseID:        meta.LeaseID,
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
