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
	storage  storage.Storage
	revision atomic.Int64
	// committed is the highest revision whose writes have committed to
	// storage. revision runs ahead of it while an apply batch is open: the
	// batch assigns revisions as it goes, and none of them is readable until
	// the batch commits.
	committed atomic.Int64
	floors    atomic.Pointer[compactionFloors]
}

// New creates a new MVCC store backed by the given storage.Storage instance.
// The store loads the current revision and the compaction floors from the
// storage if they exist.
func New(store storage.Storage) (*Store, error) {
	s := &Store{storage: store}
	if err := s.loadRevision(); err != nil {
		return nil, err
	}
	if err := s.loadFloors(); err != nil {
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
	s.committed.Store(rev)
	return nil
}

// loadFloors reads every persisted compaction floor from storage.
func (s *Store) loadFloors() error {
	iter := s.storage.NewIterator(&storage.IteratorOptions{
		LowerBound: CompactionFloorLowerBound(),
		UpperBound: CompactionFloorUpperBound(),
	})
	defer func() { _ = iter.Close() }()

	floors := emptyFloors
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		prefix, err := DecodeCompactionFloorKey(iter.Key())
		if err != nil {
			return err
		}
		rev, err := DecodeRevision(iter.Value())
		if err != nil {
			return err
		}
		floors = floors.with(prefix, rev)
	}
	if err := iter.Error(); err != nil {
		return &StoreError{Op: "load compaction floors", Err: err}
	}
	nodeFloor, err := s.storage.Get(NodeCompactionFloorKey())
	if err != nil {
		return &StoreError{Op: "load node compaction floor", Err: err}
	}
	if nodeFloor != nil {
		rev, err := DecodeRevision(nodeFloor)
		if err != nil {
			return err
		}
		floors = floors.withNode(rev)
	}
	s.floors.Store(floors)
	return nil
}

// nextRevision atomically increments and returns the next revision.
func (s *Store) nextRevision() int64 {
	return s.revision.Add(1)
}

// CurrentRevision returns the current global revision. While an apply batch
// is open it names revisions that batch has assigned but not yet committed;
// CommittedRevision is the bound for anything that reads storage.
func (s *Store) CurrentRevision() int64 {
	return s.revision.Load()
}

// CommittedRevision returns the highest revision whose writes have committed
// to storage: every change at or below it is visible to Get, Range and
// ScanChanges.
func (s *Store) CommittedRevision() int64 {
	return s.committed.Load()
}

// MarkCommitted records that every revision through rev has committed. The
// apply loop calls it once its batch commits and before it delivers that
// batch's watch notifications: a watch replay bounded by CommittedRevision
// then finds every revision at or below the bound in storage, and receives
// every one above it live. It never moves the watermark backward.
func (s *Store) MarkCommitted(rev int64) {
	for {
		cur := s.committed.Load()
		if rev <= cur || s.committed.CompareAndSwap(cur, rev) {
			return
		}
	}
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
	s.MarkCommitted(rev)

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
//
// It PROPAGATES the metadata read's error rather than reporting every failure
// as KeyNotFoundError. getMetadataFrom already distinguishes the two — absence
// is *KeyNotFoundError, a storage fault is *StoreError{Op: "get meta"} — and
// collapsing them told an apply-path existence guard that a key it could not
// read was absent. On the conditional-put path that meant overwriting the
// holder and reporting ResultStored; in a replicated group, where the fault is
// LOCAL, it meant replicas deciding the same entry differently and diverging
// from an identical log.
//
// Callers testing for *KeyNotFoundError to mean "absent" are unaffected. A
// caller that only tests err != nil behaves exactly as before.
func (s *Store) getFrom(r storage.Reader, key []byte) (*service.KeyValue, error) {
	meta, err := s.getMetadataFrom(r, key)
	if err != nil {
		return nil, err
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
// Returns RevisionCompactedError if the revision is at or below the
// compaction floor covering key, or RevisionNotFoundError if no version
// exists at or before the requested revision.
func (s *Store) GetAtRevision(key []byte, revision int64) (*service.KeyValue, error) {
	compacted := s.CompactedRevisionForKey(key)
	if revision > 0 && revision <= compacted {
		return nil, &RevisionCompactedError{
			Requested: revision,
			Compacted: compacted,
		}
	}

	// Find the data entry at or before the requested revision.
	kv, err := s.findAtRevision(key, revision)
	// A compaction that committed between the floor check above and the read
	// may have removed the entry the read was looking for, so an answer --
	// absent above all -- is only good if the floor still admits revision.
	if refused := s.refusedAfterRead(key, nil, revision); refused != nil {
		return nil, refused
	}
	if err != nil {
		return nil, err
	}

	return kv, nil
}

// refusedAfterRead re-checks the compaction floor covering [start, end) (the
// single key start when end is nil) after a read at revision, and returns the
// *RevisionCompactedError the read must answer when a compaction moved the
// floor to or past revision while it ran. A compaction publishes its floor
// before its deletions become visible, so a read that observed a deletion
// observes the floor here.
func (s *Store) refusedAfterRead(start, end []byte, revision int64) error {
	if revision <= 0 {
		return nil
	}
	if floor := s.rangeFloor(start, end); revision <= floor {
		return &RevisionCompactedError{Requested: revision, Compacted: floor}
	}
	return nil
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
	s.MarkCommitted(rev)

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
// committed MVCC revision at the time of the read (or the store's committed
// revision when a historical RangeOptions.Revision was requested — the
// revision the scan targeted is available via that option, not this return
// value). Range reads committed storage, so it answers at CommittedRevision,
// never at a revision an open apply batch has claimed: a caller resuming a
// watch from that revision + 1 would skip the batch's changes for good.
//
// When RangeOptions.SortOrder/SortTarget request a non-default ordering, all
// matches are collected before sorting so that Limit is applied to the
// sorted result set rather than to whatever order the underlying storage
// iterator happens to produce.
func (s *Store) Range(start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	return s.rangeFrom(s.storage, s.CommittedRevision(), start, end, opts...)
}

// RangeInBatch is Range as seen through batch: it includes the keys written by
// earlier entries in the same apply batch and excludes the ones they deleted.
// Apply-path handlers that scan (index backfill, for example) must use it, or
// the state they derive would depend on how the apply loop grouped the log.
// It answers at the apply counter, CurrentRevision, which covers the batch's
// own writes.
func (s *Store) RangeInBatch(batch storage.Batch, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	return s.rangeFrom(batch, s.CurrentRevision(), start, end, opts...)
}

// rangeFrom implements Range against an arbitrary reader. currentRev is the
// newest revision r can see: the read's default target and the revision it
// reports.
func (s *Store) rangeFrom(r storage.Reader, currentRev int64, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	ro := service.ApplyRangeOptions(opts)

	targetRev := currentRev
	if ro.Revision > 0 {
		compacted := s.rangeFloor(start, end)
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
		if refused := s.refusedAfterRead(start, nil, ro.Revision); refused != nil {
			return nil, 0, 0, refused
		}
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

	// The keys to answer for. At the current revision they are the
	// metadata keys in [start, end) -- exactly the live keys. At a past
	// revision they are every key with a data entry in [start, end): the
	// metadata holds the CURRENT keys only, so a key deleted since the
	// revision would be missed. Both enumerations seek once per key length
	// rather than walking the namespace (ForEachMetaKeyInRange).
	historical := targetRev < currentRev
	bounds := &storage.IteratorOptions{LowerBound: MetaLowerBound(), UpperBound: MetaUpperBound()}
	forEachKey := ForEachMetaKeyInRange
	if historical {
		bounds = &storage.IteratorOptions{LowerBound: []byte{prefixData}, UpperBound: []byte{prefixData + 1}}
		forEachKey = forEachDataKeyInRange
	}
	iter := r.NewIterator(bounds)
	defer func() { _ = iter.Close() }()

	var results []*service.KeyValue
	var count int64

	scanErr := forEachKey(iter, start, end, func(userKey []byte) bool {
		if ro.CountOnly && !historical {
			count++
			return true
		}

		kv, err := s.findAtRevisionFrom(r, userKey, targetRev)
		if err != nil {
			return true // Key might not exist at this revision.
		}
		if ro.CountOnly {
			count++
			return true
		}

		if ro.KeysOnly {
			kv.Value = nil
		}

		results = append(results, kv)
		count++

		return sorting || ro.Limit <= 0 || count < ro.Limit
	})

	if err := scanErr; err != nil {
		return nil, 0, 0, &StoreError{Op: "range scan", Err: err}
	}
	if refused := s.refusedAfterRead(start, end, ro.Revision); refused != nil {
		return nil, 0, 0, refused
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

// rangeFloor returns the compaction floor a Range over start and end is
// checked against: the floor covering the single key start when end is nil
// (Range's single-key form), otherwise every floor [start, end) overlaps.
func (s *Store) rangeFloor(start, end []byte) int64 {
	if end == nil {
		return s.CompactedRevisionForKey(start)
	}
	return s.CompactedRevisionForRange(start, end)
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
