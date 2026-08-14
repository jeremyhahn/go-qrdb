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
	"sort"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Store is a multi-version concurrency control engine backed by storage.Storage.
// It maintains versioned key-value pairs with revision metadata, supporting
// point-in-time reads, range queries, and compaction of old revisions.
//
// Reads come in two flavours. The plain methods (Get, Range, ScanChanges) read
// committed storage and are what external, linearizable readers use. The
// *InBatch methods take the storage.Batch a Raft apply loop is filling and read
// through it, so an entry observes the writes of every earlier entry in the
// same apply batch. The distinction matters because the apply loop packs a
// variable, timing-dependent number of entries into one batch: an apply-path
// read that went to committed storage would make the packing observable, and
// two replicas that packed the same log differently would reach different
// states. The reader is threaded through as a parameter rather than stashed on
// the Store because Lookup reads the Store concurrently with Update.
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

	// Record the change in the revision index for watch replay.
	if err := s.setRevIndexInBatch(batch, rev, key, changePut, meta.CreateRevision, meta.Version, meta.LeaseID); err != nil {
		return nil, err
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

// Get retrieves the latest value for a key from committed storage. Returns
// KeyNotFoundError if the key does not exist.
func (s *Store) Get(key []byte) (*service.KeyValue, error) {
	return s.getFrom(s.storage, key)
}

// GetInBatch retrieves the latest value for a key as seen through batch: the
// batch's own uncommitted writes layered over committed storage. Apply-path
// handlers must use this rather than Get so a compare, an existence guard, or
// a previous-value read observes the entries already applied into the same
// batch.
func (s *Store) GetInBatch(batch storage.Batch, key []byte) (*service.KeyValue, error) {
	return s.getFrom(batch, key)
}

// getFrom retrieves the latest value for a key through r.
func (s *Store) getFrom(r storage.Reader, key []byte) (*service.KeyValue, error) {
	meta, err := s.getMetadataFrom(r, key)
	if err != nil {
		return nil, &KeyNotFoundError{Key: string(key)}
	}

	value, err := s.getDataAtRevisionFrom(r, key, meta.ModRevision)
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

	// Record the change in the revision index for watch replay. A delete event's
	// KV carries only the key and its mod revision, so create/version/lease are 0.
	if err := s.setRevIndexInBatch(batch, rev, key, changeDelete, 0, 0, 0); err != nil {
		return nil, err
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

// Range returns all key-value pairs in the range [start, end), the number
// of matched keys, and the MVCC revision the read was performed at. If end
// is nil, only the key matching start is considered.
//
// The returned count is distinct from the returned revision: count is the
// number of matched keys (when RangeOptions.CountOnly is set, every match is
// counted regardless of any Limit; otherwise count reflects the number of
// returned results, capped by Limit if set). revision is always the store's
// current MVCC revision at the time of the read (or the store's current
// revision when a historical RangeOptions.Revision was requested — the
// revision the scan targeted is available via that option, not this return
// value).
//
// When RangeOptions.SortOrder/SortTarget request a non-default ordering, all
// matches are collected before sorting so that Limit is applied to the
// sorted result set rather than to whatever order the underlying storage
// iterator happens to produce.
func (s *Store) Range(start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	return s.rangeFrom(s.storage, start, end, opts...)
}

// RangeInBatch is Range as seen through batch: it includes the keys written by
// earlier entries in the same apply batch and excludes the ones they deleted.
// Apply-path handlers that scan (index backfill, for example) must use it, or
// the state they derive would depend on how the apply loop grouped the log.
func (s *Store) RangeInBatch(batch storage.Batch, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	return s.rangeFrom(batch, start, end, opts...)
}

// rangeFrom implements Range against an arbitrary reader.
func (s *Store) rangeFrom(r storage.Reader, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	ro := service.ApplyRangeOptions(opts)
	currentRev := s.CurrentRevision()

	targetRev := currentRev
	if ro.Revision > 0 {
		compacted := s.compacted.Load()
		if ro.Revision <= compacted {
			return nil, 0, 0, &RevisionCompactedError{
				Requested: ro.Revision,
				Compacted: compacted,
			}
		}
		targetRev = ro.Revision
	}

	// Single key lookup if no end specified.
	if end == nil {
		kv, err := s.rangeGetSingle(r, start, targetRev, ro)
		if err != nil {
			// Key not found is not an error for range.
			return nil, 0, currentRev, nil
		}
		if ro.CountOnly {
			return nil, 1, currentRev, nil
		}
		return []*service.KeyValue{kv}, 1, currentRev, nil
	}

	// A non-default sort request requires the full match set before Limit is
	// applied, since Limit must select from the SORTED order, not from
	// whatever order the storage iterator produces.
	sorting := ro.SortOrder != service.SortNone || ro.SortTarget != service.SortByKey

	// Range scan over all metadata keys, filtering by user key in [start, end).
	// With length-prefixed meta keys, we must scan the entire meta namespace
	// because keys of different lengths sort by length prefix first.
	iter := r.NewIterator(&storage.IteratorOptions{
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

		kv, err := s.findAtRevisionFrom(r, userKey, targetRev)
		if err != nil {
			continue // Key might not exist at this revision.
		}

		if ro.KeysOnly {
			kv.Value = nil
		}

		results = append(results, kv)
		count++

		if !sorting && ro.Limit > 0 && count >= ro.Limit {
			break
		}
	}

	if err := iter.Error(); err != nil {
		return nil, 0, 0, &StoreError{Op: "range scan", Err: err}
	}

	if ro.CountOnly {
		return nil, count, currentRev, nil
	}

	if sorting {
		if err := sortKeyValues(results, ro.SortOrder, ro.SortTarget); err != nil {
			return nil, 0, 0, err
		}
		if ro.Limit > 0 && int64(len(results)) > ro.Limit {
			results = results[:ro.Limit]
		}
		count = int64(len(results))
	}

	if results == nil {
		results = []*service.KeyValue{}
	}

	return results, count, currentRev, nil
}

// sortKeyValues sorts results in place according to target, in ascending
// order unless order is service.SortDescend. Sorting is stable so that
// keys comparing equal on target retain their relative scan order. Returns
// an UnsupportedSortTargetError for a target value outside the range
// service.SortTarget defines, rather than silently leaving results
// unsorted.
func sortKeyValues(results []*service.KeyValue, order service.SortOrder, target service.SortTarget) error {
	less, err := sortLessFunc(results, target)
	if err != nil {
		return err
	}
	if order == service.SortDescend {
		asc := less
		less = func(i, j int) bool { return asc(j, i) }
	}
	sort.SliceStable(results, less)
	return nil
}

// sortLessFunc returns the ascending less-than comparator for target over
// results.
func sortLessFunc(results []*service.KeyValue, target service.SortTarget) (func(i, j int) bool, error) {
	switch target {
	case service.SortByKey:
		return func(i, j int) bool {
			return bytes.Compare(results[i].Key, results[j].Key) < 0
		}, nil
	case service.SortByVersion:
		return func(i, j int) bool {
			return results[i].Version < results[j].Version
		}, nil
	case service.SortByCreateRevision:
		return func(i, j int) bool {
			return results[i].CreateRevision < results[j].CreateRevision
		}, nil
	case service.SortByModRevision:
		return func(i, j int) bool {
			return results[i].ModRevision < results[j].ModRevision
		}, nil
	case service.SortByValue:
		return func(i, j int) bool {
			return bytes.Compare(results[i].Value, results[j].Value) < 0
		}, nil
	default:
		return nil, &UnsupportedSortTargetError{Target: target}
	}
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

	if err := s.CompactInBatch(batch, revision); err != nil {
		return err
	}

	if err := batch.Commit(); err != nil {
		return &CompactionError{Err: err}
	}

	s.compacted.Store(revision)
	return nil
}

// CompactInBatch queues a compaction into batch instead of committing its own.
// The apply path uses it for two reasons: the scan reads through batch, so it
// sees the data entries earlier entries in the same apply batch wrote; and the
// deletions land in the caller's batch, so compaction commits atomically with
// the rest of the apply rather than half a step ahead of it.
//
// The compacted marker is advanced immediately, matching Compact: the marker is
// in-memory state that the caller's commit makes durable, and the apply loop
// treats a failed Update as fatal.
func (s *Store) CompactInBatch(batch storage.Batch, revision int64) error {
	if err := s.compactInto(batch, revision); err != nil {
		return err
	}
	s.compacted.Store(revision)
	return nil
}

// compactInto queues the compaction deletions and the compacted marker into
// batch, reading the entries to remove through batch as well.
func (s *Store) compactInto(batch storage.Batch, revision int64) error {
	// Iterate all data entries.
	iter := batch.NewIterator(&storage.IteratorOptions{
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

	// Drop revision-index entries at or below the compaction point: a replay at
	// or below CompactedRevision is refused, so these change-log entries serve no
	// purpose and must not accumulate.
	if err := s.gcRevIndexInBatch(batch, revision); err != nil {
		return err
	}

	// Persist the compacted revision marker.
	if err := batch.Set(CompactedKey(), EncodeRevision(revision)); err != nil {
		return &CompactionError{Err: err}
	}

	return nil
}

// PutInBatch writes a key-value pair into the given storage.Batch
// at the specified revision. Used by the state machine to apply MVCC
// operations within a Raft entry batch. Returns the resulting KeyValue.
//
// The version/create-revision lookup reads through batch, so a key written
// twice within one apply batch gets Version 2 and keeps its original
// CreateRevision — the same metadata it would have if each entry had been
// applied in its own batch.
func (s *Store) PutInBatch(batch storage.Batch, key, value []byte, leaseID int64, rev int64) (*service.KeyValue, error) {
	// Read existing metadata for version tracking.
	var meta KeyMeta
	existingMeta, err := s.getMetadataFrom(batch, key)
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

	// Record the change in the revision index within the same batch.
	if err := s.setRevIndexInBatch(batch, rev, key, changePut, meta.CreateRevision, meta.Version, meta.LeaseID); err != nil {
		return nil, err
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
	meta, err := s.getMetadataFrom(batch, key)
	if err == nil {
		prevValue, err := s.getDataAtRevisionFrom(batch, key, meta.ModRevision)
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

	// Record the change in the revision index within the same batch. A delete
	// event's KV carries only the key and its mod revision, so create/version/
	// lease are 0.
	if err := s.setRevIndexInBatch(batch, rev, key, changeDelete, 0, 0, 0); err != nil {
		return nil, err
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

// getMetadata reads the current metadata for a key from committed storage.
func (s *Store) getMetadata(key []byte) (*KeyMeta, error) {
	return s.getMetadataFrom(s.storage, key)
}

// getMetadataFrom reads the current metadata for a key through r.
func (s *Store) getMetadataFrom(r storage.Reader, key []byte) (*KeyMeta, error) {
	mk := EncodeMetaKey(key)
	val, err := r.Get(mk)
	if err != nil {
		return nil, &StoreError{Op: "get meta", Err: err}
	}
	if val == nil {
		return nil, &KeyNotFoundError{Key: string(key)}
	}

	return DecodeMeta(val)
}

// getDataAtRevision reads the data value at an exact revision from committed
// storage.
func (s *Store) getDataAtRevision(key []byte, revision int64) ([]byte, error) {
	return s.getDataAtRevisionFrom(s.storage, key, revision)
}

// getDataAtRevisionFrom reads the data value at an exact revision through r.
func (s *Store) getDataAtRevisionFrom(r storage.Reader, key []byte, revision int64) ([]byte, error) {
	dk := EncodeDataKey(key, revision)
	val, err := r.Get(dk)
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

// findAtRevision finds the data entry at or before the requested revision in
// committed storage.
func (s *Store) findAtRevision(key []byte, revision int64) (*service.KeyValue, error) {
	return s.findAtRevisionFrom(s.storage, key, revision)
}

// findAtRevisionFrom finds the data entry at or before the requested revision
// through r. Uses storage.Iterator's Seek with complement encoding to find the
// correct entry.
func (s *Store) findAtRevisionFrom(r storage.Reader, key []byte, revision int64) (*service.KeyValue, error) {
	prefix := DataKeyPrefix(key)
	upper := DataKeyUpperBound(key)
	seekKey := EncodeDataKey(key, revision)

	iter := r.NewIterator(&storage.IteratorOptions{
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
	meta, metaErr := s.getMetadataFrom(r, key)
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
func (s *Store) rangeGetSingle(r storage.Reader, key []byte, targetRev int64, ro service.RangeOptions) (*service.KeyValue, error) {
	kv, err := s.findAtRevisionFrom(r, key, targetRev)
	if err != nil {
		return nil, err
	}
	if ro.KeysOnly {
		kv.Value = nil
	}
	return kv, nil
}
