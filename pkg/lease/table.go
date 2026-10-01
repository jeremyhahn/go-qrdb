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

package lease

import (
	"bytes"
	"encoding/binary"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// The lease table is the durable half of the Lessor. Every mutation the
// Lessor makes during apply -- a grant, a revoke, a key bound to a lease or
// released from one -- is staged into the state machine's apply batch through
// a Writer, in the same batch as the data write that caused it, so the two
// commit together or not at all. Restore rebuilds the in-memory Lessor from it
// when a state machine opens and after it recovers from a snapshot.
//
// Layout, under TablePrefix in the state machine's raw keyspace:
//
//	TablePrefix + "l/" + id (8 bytes BE)  ->  granted TTL in seconds (8 bytes BE)
//	TablePrefix + "b/" + key              ->  lease id the key is bound to (8 bytes BE)
//	TablePrefix + "seq"                   ->  highest lease id ever granted (8 bytes BE)
//
// There is one binding record per key, keyed by the key, so "which lease does
// this key belong to" is a point read and a key can never be bound to two
// leases at once. The lease -> keys direction is rebuilt in memory from the
// bindings rather than stored twice.
//
// The prefix begins with 0x00, the byte both state machines reserve for their
// own records, so it sorts ahead of every user key and is hidden from user
// key enumeration. Unlike the last-applied index, which is local to a node,
// the lease table is replicated state: snapshots must carry it and replica
// hashes must cover it. IsTableKey is how the state machines tell the two
// apart.

// TablePrefix is the raw state-machine key prefix the lease table is stored
// under.
const TablePrefix = "\x00qrdb.lease/"

// The three record families under TablePrefix.
const (
	leaseRecordPrefix = TablePrefix + "l/"
	bindingPrefix     = TablePrefix + "b/"
	sequenceKey       = TablePrefix + "seq"
)

// int64Size is the encoded width of every integer in the table.
const int64Size = 8

// Writer stages lease-table mutations. It is the state machine's apply batch:
// writing through it is what makes a lease change atomic with the data write
// that caused it. storage.Batch satisfies it.
type Writer interface {
	// Set stages key = value.
	Set(key, value []byte) error

	// Delete stages the removal of key.
	Delete(key []byte) error
}

// IsTableKey reports whether k is a lease-table record.
func IsTableKey(k []byte) bool {
	return bytes.HasPrefix(k, []byte(TablePrefix))
}

// leaseRecordKey returns the key of lease id's record.
func leaseRecordKey(id int64) []byte {
	k := make([]byte, 0, len(leaseRecordPrefix)+int64Size)
	k = append(k, leaseRecordPrefix...)
	return binary.BigEndian.AppendUint64(k, uint64(id))
}

// bindingKey returns the key of key's binding record.
func bindingKey(key []byte) []byte {
	k := make([]byte, 0, len(bindingPrefix)+len(key))
	k = append(k, bindingPrefix...)
	return append(k, key...)
}

// encodeInt64 encodes v as 8 big-endian bytes.
func encodeInt64(v int64) []byte {
	return binary.BigEndian.AppendUint64(make([]byte, 0, int64Size), uint64(v))
}

// decodeInt64 decodes an 8-byte big-endian value; ok is false for any other
// width.
func decodeInt64(b []byte) (v int64, ok bool) {
	if len(b) != int64Size {
		return 0, false
	}
	return int64(binary.BigEndian.Uint64(b)), true
}

// tableSnapshot is the lease table as read from storage, before it replaces
// the Lessor's in-memory state.
type tableSnapshot struct {
	seq      int64
	ttls     map[int64]int64
	bindings map[string]int64
}

// readTable reads and validates the whole lease table from r. A record that
// does not decode, a binding to a lease the table does not hold, or a lease id
// above the recorded sequence is a CorruptTableError: the table is the only
// durable record of which keys a lease deletes, so guessing past a damaged one
// would either delete keys that should live or keep keys that should expire.
func readTable(r storage.Reader) (*tableSnapshot, error) {
	t := &tableSnapshot{
		ttls:     make(map[int64]int64),
		bindings: make(map[string]int64),
	}

	raw, err := r.Get([]byte(sequenceKey))
	if err != nil {
		return nil, &TableReadError{Err: err}
	}
	if raw != nil {
		seq, ok := decodeInt64(raw)
		if !ok {
			return nil, &CorruptTableError{Key: sequenceKey, Reason: "sequence is not 8 bytes"}
		}
		t.seq = seq
	}

	err = scanPrefix(r, []byte(leaseRecordPrefix), func(k, v []byte) error {
		id, idOK := decodeInt64(k[len(leaseRecordPrefix):])
		ttl, ttlOK := decodeInt64(v)
		if !idOK || !ttlOK || id <= 0 || ttl <= 0 {
			return &CorruptTableError{Key: string(k), Reason: "malformed lease record"}
		}
		if id > t.seq {
			return &CorruptTableError{Key: string(k), Reason: "lease id is above the recorded sequence"}
		}
		t.ttls[id] = ttl
		return nil
	})
	if err != nil {
		return nil, err
	}

	err = scanPrefix(r, []byte(bindingPrefix), func(k, v []byte) error {
		id, ok := decodeInt64(v)
		if !ok {
			return &CorruptTableError{Key: string(k), Reason: "malformed binding record"}
		}
		if _, held := t.ttls[id]; !held {
			return &CorruptTableError{Key: string(k), Reason: "binding names a lease the table does not hold"}
		}
		t.bindings[string(k[len(bindingPrefix):])] = id
		return nil
	})
	if err != nil {
		return nil, err
	}

	return t, nil
}

// scanPrefix calls fn for every record under prefix in ascending key order.
// The key and value passed to fn are only valid for the duration of the call.
func scanPrefix(r storage.Reader, prefix []byte, fn func(k, v []byte) error) error {
	it := r.NewIterator(&storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		if err := fn(it.Key(), it.Value()); err != nil {
			_ = it.Close()
			return err
		}
	}
	if err := it.Error(); err != nil {
		_ = it.Close()
		return &TableReadError{Err: err}
	}
	if err := it.Close(); err != nil {
		return &TableReadError{Err: err}
	}
	return nil
}

// prefixEnd returns the exclusive upper bound of the keys starting with
// prefix. Every prefix in this file ends in '/', so the increment never
// carries.
func prefixEnd(prefix []byte) []byte {
	end := bytes.Clone(prefix)
	end[len(end)-1]++
	return end
}
