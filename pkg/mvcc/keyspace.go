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

// keyspace.go is the SINGLE source of truth for all MVCC on-disk key encoding.
//
// Canonical format:
//
//	DataKey:      [0x01][keyLen:8 BE][key][complement(revision):8 BE]
//	MetaKey:      [0x02][keyLen:8 BE][key]
//	RevisionKey:  [0x03]
//	CompactedKey: [0x04]
//	Metadata:     [createRev:8][modRev:8][version:8][leaseID:8]  (32 bytes fixed)
//	Entry:        [flag:flagLive|flagTombstone][value]
//
// The complement (math.MaxUint64 - revision) ensures descending revision sort
// in byte order, making the latest revision the first result in a prefix scan.

package mvcc

import (
	"bytes"
	"encoding/binary"
	"math"
)

// Key prefixes partition the storage keyspace into logical namespaces.
const (
	prefixData     = byte(0x01)
	prefixMeta     = byte(0x02)
	prefixRevision = byte(0x03)
	prefixCompact  = byte(0x04)
)

// Entry flags distinguish live values from tombstones in data entries.
const (
	flagLive      = byte(0x00)
	flagTombstone = byte(0x01)
)

// metaSize is the fixed size of serialized MVCC metadata in bytes.
// Layout: [createRevision:8][modRevision:8][version:8][leaseID:8]
const metaSize = 32

// KeyMeta holds per-key MVCC state persisted alongside metadata keys.
type KeyMeta struct {
	CreateRevision int64
	ModRevision    int64
	Version        int64
	LeaseID        int64
}

// ---------------------------------------------------------------------------
// Data key encoding
// ---------------------------------------------------------------------------

// EncodeDataKey creates a versioned data key for storage.
// Format: [0x01][keyLen:8 BE][key][complement(revision):8 BE]
//
// The complement (MaxUint64 - revision) ensures descending revision order
// within a key's entries, so prefix iteration yields the latest first.
func EncodeDataKey(key []byte, revision int64) []byte {
	buf := make([]byte, 1+8+len(key)+8)
	buf[0] = prefixData
	//nolint:gosec // G115: int to uint64 conversion for length encoding - key length is always positive and bounded
	binary.BigEndian.PutUint64(buf[1:9], uint64(len(key)))
	copy(buf[9:9+len(key)], key)
	binary.BigEndian.PutUint64(buf[9+len(key):], complementRevision(revision))
	return buf
}

// DecodeDataKey extracts the user key and revision from an encoded data key.
func DecodeDataKey(encoded []byte) (key []byte, revision int64, err error) {
	if len(encoded) < 17 { // 1 + 8 + 0 + 8 minimum
		return nil, 0, &EncodingError{Reason: "data key too short"}
	}
	if encoded[0] != prefixData {
		return nil, 0, &EncodingError{Reason: "invalid data key prefix"}
	}
	keyLen := binary.BigEndian.Uint64(encoded[1:9])
	//nolint:gosec // G115: uint64 to int conversion - keyLen is bounded by encoded length
	if 1+8+int(keyLen)+8 != len(encoded) {
		return nil, 0, &EncodingError{Reason: "data key length mismatch"}
	}
	key = make([]byte, keyLen)
	copy(key, encoded[9:9+keyLen])
	complement := binary.BigEndian.Uint64(encoded[9+keyLen:])
	revision = uncomplement(complement)
	return key, revision, nil
}

// DataKeyPrefix returns the prefix for all revisions of a given key.
// Used for prefix-bounded iteration over a key's version history.
func DataKeyPrefix(key []byte) []byte {
	buf := make([]byte, 1+8+len(key))
	buf[0] = prefixData
	//nolint:gosec // G115: int to uint64 conversion for length encoding - key length is always positive and bounded
	binary.BigEndian.PutUint64(buf[1:9], uint64(len(key)))
	copy(buf[9:], key)
	return buf
}

// DataKeyUpperBound returns the exclusive upper bound for iterating
// all revisions of a given key. This is the data key prefix with the
// complement bytes set to 0xFF (representing revision 0, the lowest),
// followed by a sentinel byte to be strictly after any valid entry.
func DataKeyUpperBound(key []byte) []byte {
	prefix := DataKeyPrefix(key)
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

// EncodeMetaKey creates a metadata key for storage.
// Format: [0x02][keyLen:8 BE][key]
//
// The 8-byte length prefix ensures unambiguous decoding while preserving
// correct ordering within the meta keyspace.
func EncodeMetaKey(key []byte) []byte {
	buf := make([]byte, 1+8+len(key))
	buf[0] = prefixMeta
	//nolint:gosec // G115: int to uint64 conversion for length encoding - key length is always positive and bounded
	binary.BigEndian.PutUint64(buf[1:9], uint64(len(key)))
	copy(buf[9:], key)
	return buf
}

// DecodeMetaKey extracts the user key from an encoded metadata key.
func DecodeMetaKey(encoded []byte) ([]byte, error) {
	if len(encoded) < 9 { // 1 prefix + 8 keyLen minimum
		return nil, &EncodingError{Reason: "meta key too short"}
	}
	if encoded[0] != prefixMeta {
		return nil, &EncodingError{Reason: "invalid meta key prefix"}
	}
	keyLen := binary.BigEndian.Uint64(encoded[1:9])
	//nolint:gosec // G115: uint64 to int conversion for validation - keyLen is bounded by encoded length
	if len(encoded) != int(1+8+keyLen) {
		return nil, &EncodingError{Reason: "meta key length mismatch"}
	}
	key := make([]byte, keyLen)
	copy(key, encoded[9:])
	return key, nil
}

// MetaLowerBound returns the lower bound for iterating all meta keys.
func MetaLowerBound() []byte {
	return []byte{prefixMeta}
}

// MetaUpperBound returns the exclusive upper bound for iterating all meta keys.
func MetaUpperBound() []byte {
	return []byte{prefixRevision}
}

// ---------------------------------------------------------------------------
// Revision key encoding
// ---------------------------------------------------------------------------

// RevisionKey returns the storage key for the global revision counter.
func RevisionKey() []byte {
	return []byte{prefixRevision}
}

// CompactedKey returns the storage key for the compacted revision marker.
func CompactedKey() []byte {
	return []byte{prefixCompact}
}

// EncodeRevision serializes a revision as 8-byte big-endian.
func EncodeRevision(rev int64) []byte {
	buf := make([]byte, 8)
	//nolint:gosec // G115: int64 to uint64 conversion for binary encoding - revisions are always positive
	binary.BigEndian.PutUint64(buf, uint64(rev))
	return buf
}

// DecodeRevision deserializes a revision from 8-byte big-endian.
func DecodeRevision(data []byte) (int64, error) {
	if len(data) != 8 {
		return 0, &EncodingError{Reason: "revision data must be 8 bytes"}
	}
	//nolint:gosec // G115: uint64 to int64 conversion from storage - original value was int64
	return int64(binary.BigEndian.Uint64(data)), nil
}

// ---------------------------------------------------------------------------
// Metadata encoding
// ---------------------------------------------------------------------------

// EncodeMeta serializes KeyMeta to a fixed 32-byte representation.
func EncodeMeta(m *KeyMeta) []byte {
	buf := make([]byte, metaSize)
	//nolint:gosec // G115: int64 to uint64 conversion for binary encoding - values are always positive revision numbers
	binary.BigEndian.PutUint64(buf[0:8], uint64(m.CreateRevision))
	//nolint:gosec // G115: int64 to uint64 conversion for binary encoding - values are always positive revision numbers
	binary.BigEndian.PutUint64(buf[8:16], uint64(m.ModRevision))
	//nolint:gosec // G115: int64 to uint64 conversion for binary encoding - values are always positive version numbers
	binary.BigEndian.PutUint64(buf[16:24], uint64(m.Version))
	//nolint:gosec // G115: int64 to uint64 conversion for binary encoding - lease IDs are always positive
	binary.BigEndian.PutUint64(buf[24:32], uint64(m.LeaseID))
	return buf
}

// DecodeMeta deserializes KeyMeta from its binary representation.
func DecodeMeta(data []byte) (*KeyMeta, error) {
	if len(data) != metaSize {
		return nil, &EncodingError{Reason: "metadata must be 32 bytes"}
	}
	return &KeyMeta{
		//nolint:gosec // G115: uint64 to int64 conversion from storage - original values were int64
		CreateRevision: int64(binary.BigEndian.Uint64(data[0:8])),
		//nolint:gosec // G115: uint64 to int64 conversion from storage - original values were int64
		ModRevision: int64(binary.BigEndian.Uint64(data[8:16])),
		//nolint:gosec // G115: uint64 to int64 conversion from storage - original values were int64
		Version: int64(binary.BigEndian.Uint64(data[16:24])),
		//nolint:gosec // G115: uint64 to int64 conversion from storage - original values were int64
		LeaseID: int64(binary.BigEndian.Uint64(data[24:32])),
	}, nil
}

// ---------------------------------------------------------------------------
// Data value encoding
// ---------------------------------------------------------------------------

// EncodeValue prepends a live flag byte to the value.
func EncodeValue(value []byte) []byte {
	buf := make([]byte, 1+len(value))
	buf[0] = flagLive
	copy(buf[1:], value)
	return buf
}

// EncodeTombstone creates a tombstone data value.
func EncodeTombstone() []byte {
	return []byte{flagTombstone}
}

// DecodeValue extracts the flag and value from a data entry.
// Returns isTombstone=true for deletion tombstones.
func DecodeValue(data []byte) (value []byte, isTombstone bool, err error) {
	if len(data) < 1 {
		return nil, false, &EncodingError{Reason: "data value too short"}
	}
	switch data[0] {
	case flagLive:
		return data[1:], false, nil
	case flagTombstone:
		return nil, true, nil
	default:
		return nil, false, &EncodingError{Reason: "unknown data value flag"}
	}
}

// ---------------------------------------------------------------------------
// Key comparison
// ---------------------------------------------------------------------------

// CompareKeys compares two user keys lexicographically.
// Returns:
//
//	-1 if a < b
//	 0 if a == b
//	+1 if a > b
func CompareKeys(a, b []byte) int {
	return bytes.Compare(a, b)
}

// IsInRange checks if key is within [start, end).
// If end is nil, only checks if key >= start.
func IsInRange(key, start, end []byte) bool {
	if CompareKeys(key, start) < 0 {
		return false
	}
	if end != nil && CompareKeys(key, end) >= 0 {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// complementRevision converts a revision to its complement for descending sort.
func complementRevision(rev int64) uint64 {
	return math.MaxUint64 - uint64(rev) //nolint:gosec // G115: int64 to uint64 for complement encoding
}

// uncomplement converts a complement value back to the original revision.
func uncomplement(c uint64) int64 {
	return int64(math.MaxUint64 - c) //nolint:gosec // G115: uint64 to int64 for complement decoding
}
