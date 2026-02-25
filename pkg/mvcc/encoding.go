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
	"encoding/binary"
	"math"
)

// Key prefixes partition the PebbleDB keyspace into logical namespaces.
const (
	// dataPrefix identifies versioned data entries.
	// Data key format: [0x01][keyLen:2 BE][key][complementRevision:8 BE]
	//
	// The complement (math.MaxUint64 - revision) ensures newer revisions
	// sort before older ones in byte order, making the latest revision
	// the first result in a prefix scan.
	dataPrefix = byte(0x01)

	// metaPrefix identifies per-key metadata entries.
	// Meta key format: [0x02][key]
	//
	// Metadata keys use raw key bytes (no length prefix) so they sort
	// in natural lexicographic order, enabling correct range queries.
	metaPrefix = byte(0x02)

	// revisionPrefix is the single-byte key for the global revision counter.
	// Revision key format: [0x03] → [revision:8 BE]
	revisionPrefix = byte(0x03)

	// compactedPrefix is the single-byte key for the compacted revision marker.
	// Compacted key format: [0x04] → [revision:8 BE]
	compactedPrefix = byte(0x04)
)

// Entry flags distinguish live values from tombstones in data entries.
const (
	// entryLive marks a data entry as a live key-value pair.
	entryLive = byte(0x00)

	// entryTombstone marks a data entry as a deletion tombstone.
	entryTombstone = byte(0x01)
)

// metadataSize is the fixed size of serialized metadata in bytes.
// Layout: [createRevision:8][modRevision:8][version:8][lease:8]
const metadataSize = 32

// ---------------------------------------------------------------------------
// Data key encoding
// ---------------------------------------------------------------------------

// encodeDataKey creates a versioned data key for PebbleDB storage.
// Format: [0x01][keyLen:2 BE][key][complementRevision:8 BE]
//
// The complement (MaxUint64 - revision) ensures descending revision order
// within a key's entries, so prefix iteration yields the latest first.
func encodeDataKey(key []byte, revision int64) []byte {
	buf := make([]byte, 1+2+len(key)+8)
	buf[0] = dataPrefix
	binary.BigEndian.PutUint16(buf[1:3], uint16(len(key)))
	copy(buf[3:3+len(key)], key)
	binary.BigEndian.PutUint64(buf[3+len(key):], math.MaxUint64-uint64(revision))
	return buf
}

// decodeDataKey extracts the user key and revision from a data key.
func decodeDataKey(encoded []byte) (key []byte, revision int64, err error) {
	if len(encoded) < 11 { // 1 + 2 + 0 + 8 minimum
		return nil, 0, &EncodingError{Reason: "data key too short"}
	}
	if encoded[0] != dataPrefix {
		return nil, 0, &EncodingError{Reason: "invalid data key prefix"}
	}
	keyLen := int(binary.BigEndian.Uint16(encoded[1:3]))
	if 3+keyLen+8 != len(encoded) {
		return nil, 0, &EncodingError{Reason: "data key length mismatch"}
	}
	key = make([]byte, keyLen)
	copy(key, encoded[3:3+keyLen])
	complement := binary.BigEndian.Uint64(encoded[3+keyLen:])
	revision = int64(math.MaxUint64 - complement)
	return key, revision, nil
}

// dataKeyPrefix returns the prefix for all revisions of a given key.
// Used for prefix-bounded iteration over a key's version history.
func dataKeyPrefix(key []byte) []byte {
	buf := make([]byte, 1+2+len(key))
	buf[0] = dataPrefix
	binary.BigEndian.PutUint16(buf[1:3], uint16(len(key)))
	copy(buf[3:], key)
	return buf
}

// dataKeyUpperBound returns the exclusive upper bound for iterating
// all revisions of a given key. This is the data key prefix with the
// last byte incremented.
func dataKeyUpperBound(key []byte) []byte {
	prefix := dataKeyPrefix(key)
	upper := make([]byte, len(prefix)+8)
	copy(upper, prefix)
	// All 8 complement bytes set to 0xFF represents revision 0 (the lowest).
	// Any valid entry will sort before this.
	for i := len(prefix); i < len(upper); i++ {
		upper[i] = 0xFF
	}
	// Add one more byte to be strictly after any valid entry.
	return append(upper, 0x00)
}

// ---------------------------------------------------------------------------
// Meta key encoding
// ---------------------------------------------------------------------------

// encodeMetaKey creates a metadata key for PebbleDB storage.
// Format: [0x02][key]
//
// Raw key bytes (no length prefix) preserve lexicographic ordering
// across different keys, enabling correct range queries on metadata.
func encodeMetaKey(key []byte) []byte {
	buf := make([]byte, 1+len(key))
	buf[0] = metaPrefix
	copy(buf[1:], key)
	return buf
}

// decodeMetaKey extracts the user key from a metadata key.
func decodeMetaKey(encoded []byte) ([]byte, error) {
	if len(encoded) < 1 {
		return nil, &EncodingError{Reason: "meta key too short"}
	}
	if encoded[0] != metaPrefix {
		return nil, &EncodingError{Reason: "invalid meta key prefix"}
	}
	key := make([]byte, len(encoded)-1)
	copy(key, encoded[1:])
	return key, nil
}

// ---------------------------------------------------------------------------
// Revision key encoding
// ---------------------------------------------------------------------------

// revisionKey returns the PebbleDB key for the global revision counter.
func revisionKey() []byte {
	return []byte{revisionPrefix}
}

// compactedKey returns the PebbleDB key for the compacted revision marker.
func compactedKey() []byte {
	return []byte{compactedPrefix}
}

// encodeRevision serializes a revision as 8-byte big-endian.
func encodeRevision(rev int64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(rev))
	return buf
}

// decodeRevision deserializes a revision from 8-byte big-endian.
func decodeRevision(data []byte) (int64, error) {
	if len(data) != 8 {
		return 0, &EncodingError{Reason: "revision data must be 8 bytes"}
	}
	return int64(binary.BigEndian.Uint64(data)), nil
}

// ---------------------------------------------------------------------------
// Metadata encoding
// ---------------------------------------------------------------------------

// metadata holds per-key MVCC state persisted alongside metadata keys.
type metadata struct {
	CreateRevision int64
	ModRevision    int64
	Version        int64
	Lease          int64
}

// encode serializes metadata to a fixed 32-byte representation.
func (m *metadata) encode() []byte {
	buf := make([]byte, metadataSize)
	binary.BigEndian.PutUint64(buf[0:8], uint64(m.CreateRevision))
	binary.BigEndian.PutUint64(buf[8:16], uint64(m.ModRevision))
	binary.BigEndian.PutUint64(buf[16:24], uint64(m.Version))
	binary.BigEndian.PutUint64(buf[24:32], uint64(m.Lease))
	return buf
}

// decodeMetadata deserializes metadata from its binary representation.
func decodeMetadata(data []byte) (metadata, error) {
	if len(data) != metadataSize {
		return metadata{}, &EncodingError{Reason: "metadata must be 32 bytes"}
	}
	return metadata{
		CreateRevision: int64(binary.BigEndian.Uint64(data[0:8])),
		ModRevision:    int64(binary.BigEndian.Uint64(data[8:16])),
		Version:        int64(binary.BigEndian.Uint64(data[16:24])),
		Lease:          int64(binary.BigEndian.Uint64(data[24:32])),
	}, nil
}

// ---------------------------------------------------------------------------
// Data value encoding
// ---------------------------------------------------------------------------

// encodeDataValue prepends a flag byte to the value.
func encodeDataValue(value []byte) []byte {
	buf := make([]byte, 1+len(value))
	buf[0] = entryLive
	copy(buf[1:], value)
	return buf
}

// encodeTombstone creates a tombstone data value.
func encodeTombstone() []byte {
	return []byte{entryTombstone}
}

// decodeDataValue extracts the flag and value from a data entry.
// Returns isTombstone=true for deletion tombstones.
func decodeDataValue(data []byte) (value []byte, isTombstone bool, err error) {
	if len(data) < 1 {
		return nil, false, &EncodingError{Reason: "data value too short"}
	}
	switch data[0] {
	case entryLive:
		return data[1:], false, nil
	case entryTombstone:
		return nil, true, nil
	default:
		return nil, false, &EncodingError{Reason: "unknown data value flag"}
	}
}
