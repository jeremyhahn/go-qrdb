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
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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
// into batch, with etcd's semantics: each such key keeps its entry live at
// revision -- the newest at or below it -- so every read at a revision above
// the floor answers as it did before, and loses every entry older than that;
// a kept entry that is a tombstone goes too when the key has a newer entry,
// since it records only that the key did not exist. Those keys' change-log
// entries go with their data entries, and prefix's floor becomes revision.
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
	return s.validateRevision(revision, s.CompactionFloor(prefix))
}

// validateRevision refuses a compaction to revision that is below 1, above
// the current revision, or at or below floor, the floor already covering the
// keyspace being compacted.
func (s *Store) validateRevision(revision, floor int64) error {
	if revision < 1 {
		return &CompactRevisionInvalidError{Revision: revision}
	}
	if current := s.CurrentRevision(); revision > current {
		return &CompactRevisionFutureError{Requested: revision, Current: current}
	}
	if revision <= floor {
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

	deleter := supersededEntryDeleter(batch, s.CompactionFloor(prefix), revision)
	if err := forEachDataKeyWithPrefix(iter, prefix, deleter); err != nil {
		return err
	}
	if err := batch.Set(CompactionFloorKey(prefix), EncodeRevision(revision)); err != nil {
		return &CompactionError{Err: err}
	}
	return nil
}

// supersededEntryDeleter returns a visitor that, fed each key's data entries
// in storage order (newest first, with their values), applies etcd's
// compaction rule for revision and queues the deletions into batch:
//
//   - an entry above revision is history the floor does not reach: kept;
//   - the first entry at or below revision is the key's state at revision,
//     which every read above the floor and below the key's next write
//     returns: kept -- unless it is a tombstone and the key has a newer
//     entry, when it records only an absence nothing can ask about and goes;
//   - every older entry is superseded at revision: deleted.
//
// The change log of the compacted keys at or below revision goes too: every
// change is recorded as a data entry and a change-log entry at the same (key,
// revision), so each data entry in (previous, revision] -- kept or deleted --
// has its change-log entry deleted here, where previous is the floor the
// keyspace had. Entries at or below previous lost theirs to that compaction.
// The change log is therefore collected in exactly the work the data scan
// already does, the entries under the compacted keyspace, and never by
// walking the revision window, which holds every other database's changes
// too.
func supersededEntryDeleter(batch storage.Batch, previous, revision int64) func(raw, value []byte) error {
	// The key group is the encoded key without its trailing 8-byte
	// complemented revision.
	var (
		group  []byte
		newer  bool // the key has an entry above revision
		atLive bool // the key's entry live at revision has been visited
	)
	return func(raw, value []byte) error {
		if g := raw[:len(raw)-8]; !bytes.Equal(g, group) {
			group = append(group[:0], g...)
			newer, atLive = false, false
		}
		_, entryRev, err := DecodeDataKey(raw)
		if err != nil {
			return nil
		}
		if entryRev > revision {
			newer = true
			return nil
		}
		if entryRev > previous {
			if err := deleteKey(batch, changeLogKey(raw, entryRev)); err != nil {
				return err
			}
		}
		if !atLive {
			atLive = true
			if !newer || !isTombstone(value) {
				return nil
			}
		}
		return deleteKey(batch, append([]byte(nil), raw...))
	}
}

// changeLogKey returns the change-log key paired with the data entry raw at
// revision rev.
func changeLogKey(raw []byte, rev int64) []byte {
	key, _ := dataUserKey(raw)
	return EncodeRevIndexKey(rev, key)
}

// deleteKey queues the deletion of key into batch.
func deleteKey(batch storage.Batch, key []byte) error {
	if err := batch.Delete(key); err != nil {
		return &CompactionError{Err: err}
	}
	return nil
}

// isTombstone reports whether an encoded data value is a deletion tombstone.
func isTombstone(value []byte) bool {
	_, tombstone, err := DecodeValue(value)
	return err == nil && tombstone
}

// ---------------------------------------------------------------------------
// Node-keyspace compaction
//
// The node keyspace is every key outside all "<tenant>/<db>/" namespaces
// (tenant.NamespaceOf): the node's own internal records and the system
// tenant's raw keyspace. Compacting it is a platform act that skips every key
// with a database namespace by construction, so it can never discard any
// tenant's history.
// ---------------------------------------------------------------------------

// NodeCompactionFloor returns the node keyspace's compaction floor.
func (s *Store) NodeCompactionFloor() int64 {
	return s.floors.Load().node
}

// CompactNodeKeyspace compacts the node keyspace to revision in a batch of
// its own. See CompactNodeKeyspaceInBatch.
func (s *Store) CompactNodeKeyspace(revision int64) error {
	batch := s.storage.Batch()
	defer func() { _ = batch.Close() }()

	if err := s.compactNodeInto(batch, revision); err != nil {
		return err
	}
	if err := batch.Commit(); err != nil {
		return &CompactionError{Err: err}
	}
	s.publishNodeFloor(revision)
	return nil
}

// CompactNodeKeyspaceInBatch queues a compaction of the node keyspace into
// batch: each key outside all database namespaces is compacted to revision by
// the same rule CompactPrefixInBatch applies -- its entry live at revision is
// kept -- along with those keys' change-log entries; and the node floor
// becomes revision. A key with a database
// namespace is never visited. The revision is validated exactly as
// CompactPrefixInBatch validates it, against the node floor.
func (s *Store) CompactNodeKeyspaceInBatch(batch storage.Batch, revision int64) error {
	if err := s.compactNodeInto(batch, revision); err != nil {
		return err
	}
	s.publishNodeFloor(revision)
	return nil
}

// compactNodeInto validates and queues a node-keyspace compaction into batch.
func (s *Store) compactNodeInto(batch storage.Batch, revision int64) error {
	if err := s.validateRevision(revision, s.NodeCompactionFloor()); err != nil {
		return err
	}

	iter := batch.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte{prefixData},
		UpperBound: []byte{prefixData + 1},
	})
	defer func() { _ = iter.Close() }()

	deleteSuperseded := supersededEntryDeleter(batch, s.NodeCompactionFloor(), revision)
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		raw := iter.Key()
		userKey, ok := dataUserKey(raw)
		if !ok {
			continue
		}
		if _, namespaced := tenant.NamespaceOf(userKey); namespaced {
			continue
		}
		if err := deleteSuperseded(raw, iter.Value()); err != nil {
			return err
		}
	}
	if err := iter.Error(); err != nil {
		return &CompactionError{Err: err}
	}
	if err := batch.Set(NodeCompactionFloorKey(), EncodeRevision(revision)); err != nil {
		return &CompactionError{Err: err}
	}
	return nil
}

// publishNodeFloor makes revision the node floor in the in-memory snapshot.
func (s *Store) publishNodeFloor(revision int64) {
	for {
		cur := s.floors.Load()
		if s.floors.CompareAndSwap(cur, cur.withNode(revision)) {
			return
		}
	}
}

// dataUserKey returns the user key an encoded data key carries, without
// copying it.
func dataUserKey(raw []byte) ([]byte, bool) {
	keyLen, ok := dataKeyLen(raw)
	if !ok || len(raw) != 1+8+keyLen+8 {
		return nil, false
	}
	return raw[9 : 9+keyLen], true
}

// forEachDataKeyWithPrefix calls visit, in storage order, for every encoded
// data key whose user key begins with prefix, with its value. The iterator
// must be bounded to the data namespace.
//
// A data key sorts by user-key LENGTH first ([0x01][keyLen:8][key][rev:8]), so
// the keys under one prefix are one contiguous run per key length, not one
// run overall. This seeks to the prefix within each length present instead of
// walking the namespace, so its cost is one seek per distinct key length plus
// the entries under the prefix -- not every entry of every other database on
// the store.
func forEachDataKeyWithPrefix(iter storage.Iterator, prefix []byte, visit func(raw, value []byte) error) error {
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
			if err := visit(raw, iter.Value()); err != nil {
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
