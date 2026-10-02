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
	"encoding/binary"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// Per-prefix compaction
//
// A compaction is confined to one keyspace prefix: the "<tenant>/<database>/"
// namespace of a database on a shared store, or the empty prefix of a store
// that is one database's keyspace. There is no store-global compaction --
// one store serves many tenants, and a compaction that discarded every key's
// history would discard every tenant's.
// ---------------------------------------------------------------------------

// CompactionFloor returns the highest compaction floor covering prefix's
// keyspace from prefix itself or any prefix of it: the revision at or below
// which the history of every key beginning with prefix is gone.
func (s *Store) CompactionFloor(prefix []byte) int64 {
	return s.floors.Load().forKey(prefix)
}

// CompactedRevisionForKey returns the compaction floor covering key: a read
// of key at or below it is refused with *RevisionCompactedError.
func (s *Store) CompactedRevisionForKey(key []byte) int64 {
	return s.floors.Load().forKey(key)
}

// CompactedRevisionForRange returns the highest compaction floor whose
// keyspace overlaps [start, end); a nil end is unbounded above. A range read,
// watch replay or change scan over [start, end) at or below it is refused.
func (s *Store) CompactedRevisionForRange(start, end []byte) int64 {
	return s.floors.Load().forRange(start, end)
}

// CompactedRevisionForPrefix returns the highest compaction floor whose
// keyspace overlaps the keys beginning with prefix (every key, for an empty
// prefix).
func (s *Store) CompactedRevisionForPrefix(prefix []byte) int64 {
	return s.floors.Load().forRange(prefix, prefixEnd(prefix))
}

// CompactPrefix compacts the keys beginning with prefix to revision in a batch
// of its own. See CompactPrefixInBatch.
func (s *Store) CompactPrefix(prefix []byte, revision int64) error {
	batch := s.storage.Batch()
	defer func() { _ = batch.Close() }()

	if err := s.validateCompaction(prefix, revision); err != nil {
		return err
	}
	if err := s.compactInto(batch, prefix, revision); err != nil {
		return err
	}
	if err := batch.Commit(); err != nil {
		return &CompactionError{Err: err}
	}
	s.publishFloor(prefix, revision)
	return nil
}

// CompactPrefixInBatch queues a compaction of the keys beginning with prefix
// into batch: every data entry of such a key at or below revision is deleted
// except the key's newest one, which is its current state and stays
// readable, along with those keys' change-log entries at or below revision;
// and prefix's floor becomes revision.
//
// The apply path uses the batch form for two reasons: the scan reads through
// batch, so it sees the data entries earlier entries in the same apply batch
// wrote; and the deletions land in the caller's batch, so compaction commits
// atomically with the rest of the apply rather than half a step ahead of it.
// The floor is published immediately, matching CompactPrefix: it is in-memory
// state the caller's commit makes durable, and the apply loop treats a failed
// Update as fatal.
//
// The request is validated first, and a refusal changes nothing:
//
//   - revision below 1 is *CompactRevisionInvalidError;
//   - revision above the current revision is *CompactRevisionFutureError;
//   - revision at or below the floor already covering prefix is
//     *RevisionCompactedError, so two compactions racing to one revision can
//     never move a floor backwards.
//
// Every check reads state the log alone determines, so every replica refuses
// the same commands.
func (s *Store) CompactPrefixInBatch(batch storage.Batch, prefix []byte, revision int64) error {
	if err := s.validateCompaction(prefix, revision); err != nil {
		return err
	}
	if err := s.compactInto(batch, prefix, revision); err != nil {
		return err
	}
	s.publishFloor(prefix, revision)
	return nil
}

// validateCompaction refuses a compaction of prefix to revision that is out
// of range. Every input is the store's in-memory revision and floors, which
// the apply loop advances entry by entry.
func (s *Store) validateCompaction(prefix []byte, revision int64) error {
	if revision < 1 {
		return &CompactRevisionInvalidError{Revision: revision}
	}
	if current := s.CurrentRevision(); revision > current {
		return &CompactRevisionFutureError{Requested: revision, Current: current}
	}
	if floor := s.CompactionFloor(prefix); revision <= floor {
		return &RevisionCompactedError{Requested: revision, Compacted: floor}
	}
	return nil
}

// publishFloor makes revision prefix's floor in the in-memory snapshot.
func (s *Store) publishFloor(prefix []byte, revision int64) {
	for {
		cur := s.floors.Load()
		if s.floors.CompareAndSwap(cur, cur.with(prefix, revision)) {
			return
		}
	}
}

// compactInto queues the compaction deletions and prefix's floor into batch,
// reading the entries to remove through batch as well.
func (s *Store) compactInto(batch storage.Batch, prefix []byte, revision int64) error {
	iter := batch.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte{prefixData},
		UpperBound: []byte{prefixData + 1},
	})
	defer func() { _ = iter.Close() }()

	// Entries of one user key are contiguous, newest first: the key group is
	// the encoded key without its trailing 8-byte complemented revision.
	var currentGroup []byte
	err := forEachDataKeyWithPrefix(iter, prefix, func(raw []byte) error {
		group := raw[:len(raw)-8]
		if !bytes.Equal(group, currentGroup) {
			// The key's newest entry: its current state. Preserved.
			currentGroup = append(currentGroup[:0], group...)
			return nil
		}
		_, entryRev, err := DecodeDataKey(raw)
		if err != nil || entryRev > revision {
			return nil
		}
		if err := batch.Delete(append([]byte(nil), raw...)); err != nil {
			return &CompactionError{Err: err}
		}
		return nil
	})
	if err != nil {
		return err
	}

	// The change log of these keys at or below the floor serves no purpose
	// once the floor refuses every replay that would read it. Entries below
	// the floor already covering prefix went with that compaction.
	if err := s.gcRevIndexInBatch(batch, prefix, s.CompactionFloor(prefix)+1, revision); err != nil {
		return err
	}

	if err := batch.Set(CompactionFloorKey(prefix), EncodeRevision(revision)); err != nil {
		return &CompactionError{Err: err}
	}
	return nil
}

// forEachDataKeyWithPrefix calls visit, in storage order, for every encoded
// data key whose user key begins with prefix. The iterator must be bounded to
// the data namespace.
//
// A data key sorts by user-key LENGTH first ([0x01][keyLen:8][key][rev:8]), so
// the keys under one prefix are one contiguous run per key length, not one
// run overall. This seeks to the prefix within each length present instead of
// walking the namespace, so its cost is one seek per distinct key length plus
// the entries under the prefix -- not every entry of every other database on
// the store.
func forEachDataKeyWithPrefix(iter storage.Iterator, prefix []byte, visit func(raw []byte) error) error {
	keyLen := len(prefix)
	for iter.Seek(dataSeekKey(keyLen, prefix)) && iter.Valid() {
		landed, ok := dataKeyLen(iter.Key())
		if !ok || landed < keyLen {
			break
		}
		if landed > keyLen {
			// No key of this length is under prefix; the next length present
			// is where the iterator landed.
			keyLen = landed
			continue
		}
		for ; iter.Valid(); iter.Next() {
			raw := iter.Key()
			if length, ok := dataKeyLen(raw); !ok || length != keyLen || len(raw) != 1+8+keyLen+8 {
				break
			}
			if !bytes.HasPrefix(raw[9:9+keyLen], prefix) {
				// Past the prefix's run for this length.
				break
			}
			if err := visit(raw); err != nil {
				return err
			}
		}
		keyLen++
	}
	if err := iter.Error(); err != nil {
		return &CompactionError{Err: err}
	}
	return nil
}

// dataSeekKey is the encoded data key a seek for keys of length keyLen at or
// above lower targets: the namespace byte, the length, then lower.
func dataSeekKey(keyLen int, lower []byte) []byte {
	buf := make([]byte, 1+8+len(lower))
	buf[0] = prefixData
	binary.BigEndian.PutUint64(buf[1:9], uint64(keyLen)) //nolint:gosec // G115: keyLen is a non-negative key length
	copy(buf[9:], lower)
	return buf
}

// dataKeyLen returns the user-key length an encoded data key declares.
func dataKeyLen(encoded []byte) (int, bool) {
	if len(encoded) < 17 || encoded[0] != prefixData {
		return 0, false
	}
	return int(binary.BigEndian.Uint64(encoded[1:9])), true //nolint:gosec // G115: bounded by the encoded key's own length
}
