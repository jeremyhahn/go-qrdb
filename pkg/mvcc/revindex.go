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

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Change is a single historical key change replayed from the revision index.
// It carries everything a watch event needs about the NEW state of the key at
// its revision; the previous value (for a prev-kv watch on a delete) is fetched
// separately by the caller, since it lives at an earlier revision.
type Change struct {
	Revision       int64 // == ModRevision of this change
	Key            []byte
	Value          []byte // nil for a delete
	IsDelete       bool
	CreateRevision int64
	Version        int64
	LeaseID        int64
}

// setRevIndexInBatch records a change in the revision index within batch. Every
// data-mutating path (Put/Delete/PutInBatch/DeleteInBatch) calls this so the
// index is a complete, durable, revision-ordered log of changes. The value is
// intentionally not stored here — it is the data entry at (key, rev).
func (s *Store) setRevIndexInBatch(batch storage.Batch, rev int64, key []byte, eventType byte, createRev, version, leaseID int64) error {
	rec := &ChangeRecord{
		EventType:      eventType,
		CreateRevision: createRev,
		Version:        version,
		LeaseID:        leaseID,
	}
	if err := batch.Set(EncodeRevIndexKey(rev, key), EncodeChangeRecord(rec)); err != nil {
		return &StoreError{Op: "put rev index", Err: err}
	}
	return nil
}

// ScanChanges invokes fn for every change with startRev <= revision <= endRev,
// in ascending revision order (and, within a revision, ascending key order).
// fn returns false to stop the scan early. Changes are read from the durable
// revision index, so replay survives restarts and snapshot restores; only
// a change at or below the compaction floor covering its key is unavailable
// (the caller checks the floor of the range it replays -- CompactedRevisionForRange
// -- and returns a compaction error before calling ScanChanges).
//
// For a put, Change.Value is the value stored at that revision; for a delete,
// Value is nil and IsDelete is true.
func (s *Store) ScanChanges(startRev, endRev int64, fn func(Change) bool) error {
	if startRev > endRev {
		return nil
	}

	iter := s.storage.NewIterator(&storage.IteratorOptions{
		LowerBound: RevIndexLowerBound(startRev),
		UpperBound: RevIndexUpperBound(endRev),
	})
	defer func() { _ = iter.Close() }()

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		rev, key, err := DecodeRevIndexKey(iter.Key())
		if err != nil {
			continue
		}
		rec, err := DecodeChangeRecord(iter.Value())
		if err != nil {
			continue
		}

		ch := Change{
			Revision:       rev,
			Key:            key,
			IsDelete:       rec.EventType == changeDelete,
			CreateRevision: rec.CreateRevision,
			Version:        rec.Version,
			LeaseID:        rec.LeaseID,
		}

		// For a put, read the value from its data entry at this exact revision.
		if !ch.IsDelete {
			raw, gerr := s.storage.Get(EncodeDataKey(key, rev))
			if gerr != nil {
				return &StoreError{Op: "scan changes value", Err: gerr}
			}
			if raw == nil {
				// The rev-index entry exists but its data entry is gone: a
				// concurrent Compact advanced the floor past this revision
				// between the (snapshot) index iterator and this fresh point
				// read. Fail the scan closed with a compaction error rather than
				// yield a put with a silently empty value — the caller turns this
				// into a fail-closed watch, matching etcd's "catch up fully or
				// return compacted" contract.
				return &RevisionCompactedError{Requested: rev, Compacted: s.CompactedRevisionForKey(key)}
			}
			v, tomb, derr := DecodeValue(raw)
			if derr != nil {
				return derr
			}
			if !tomb {
				cp := make([]byte, len(v))
				copy(cp, v)
				ch.Value = cp
			}
		}

		if !fn(ch) {
			return nil
		}
	}

	if err := iter.Error(); err != nil {
		return &StoreError{Op: "scan changes", Err: err}
	}
	return nil
}

// gcRevIndexInBatch removes, within batch, the revision-index entries in
// [from, revision] whose key begins with prefix. Called from a compaction of
// prefix, so the change log honours the same floor as the data entries: a
// replay at or below the floor is refused, so those entries serve no purpose
// and must not accumulate. Entries of every other prefix -- another
// database's change log -- are left alone.
//
// The scan reads through batch so that index entries written by earlier entries
// of the same apply batch are collected too; scanning committed storage instead
// would leave them behind whenever the apply loop happened to pack the writes
// and the compaction into one call.
func (s *Store) gcRevIndexInBatch(batch storage.Batch, prefix []byte, from, revision int64) error {
	if from > revision {
		return nil
	}
	iter := batch.NewIterator(&storage.IteratorOptions{
		LowerBound: RevIndexLowerBound(from),
		UpperBound: RevIndexUpperBound(revision),
	})
	defer func() { _ = iter.Close() }()

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		_, key, err := DecodeRevIndexKey(iter.Key())
		if err != nil || !bytes.HasPrefix(key, prefix) {
			continue
		}
		kc := make([]byte, len(iter.Key()))
		copy(kc, iter.Key())
		if err := batch.Delete(kc); err != nil {
			return &CompactionError{Err: err}
		}
	}
	if err := iter.Error(); err != nil {
		return &CompactionError{Err: err}
	}
	return nil
}
