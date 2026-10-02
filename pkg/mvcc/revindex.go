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

// ScanChanges invokes fn for every change to a key match accepts with
// startRev <= revision <= endRev, in ascending revision order (and, within a
// revision, ascending key order). A nil match accepts every key. fn returns
// false to stop the scan early. Changes are read from the durable revision
// index, so replay survives restarts and snapshot restores; only a change at
// or below the compaction floor covering its key is unavailable (the caller
// checks the floor of the range it replays -- CompactedRevisionForRange -- and
// returns a compaction error before calling ScanChanges).
//
// match runs BEFORE anything else is read for a change. The change log is
// revision-ordered across every database on the store, so a scan of one
// database walks past the others' changes; reading their data entries would
// make this scan depend on their compaction -- another database compacting
// mid-scan removed the entries and failed the scan, cancelling a watch replay
// that never asked for them. A change match rejects costs its index entry
// alone.
//
// For a put, Change.Value is the value stored at that revision; for a delete,
// Value is nil and IsDelete is true.
func (s *Store) ScanChanges(startRev, endRev int64, match func(key []byte) bool, fn func(Change) bool) error {
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
		if match != nil && !match(key) {
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
