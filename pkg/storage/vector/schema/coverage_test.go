// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package schema

import (
	"encoding/binary"
	"testing"
)

// TestDecodeVectorKey_InsufficientBytesForID verifies that DecodeVectorKey
// returns an error when there are not enough bytes for the 8-byte ID field.
func TestDecodeVectorKey_InsufficientBytesForID(t *testing.T) {
	// Key: prefix + separator + namespace + separator + only 4 bytes (not 8 for ID)
	ns := []byte("test")
	key := make([]byte, 0, 1+1+len(ns)+1+4)
	key = append(key, PrefixVector, Separator)
	key = append(key, ns...)
	key = append(key, Separator)
	key = append(key, 0x00, 0x00, 0x00, 0x01) // only 4 bytes, need 8

	_, _, _, err := DecodeVectorKey(key)
	if err == nil {
		t.Fatal("expected error for insufficient ID bytes, got nil")
	}
}

// TestDecodeVectorKey_MissingIDSeparator verifies that DecodeVectorKey
// returns an error when the separator between ID and version is missing.
func TestDecodeVectorKey_MissingIDSeparator(t *testing.T) {
	// Key: prefix + separator + namespace + separator + 8-byte ID (no separator after)
	ns := []byte("test")
	key := make([]byte, 0, 1+1+len(ns)+1+8)
	key = append(key, PrefixVector, Separator)
	key = append(key, ns...)
	key = append(key, Separator)
	id := make([]byte, 8)
	binary.BigEndian.PutUint64(id, 42)
	key = append(key, id...)
	// No separator after ID — end of key.

	_, _, _, err := DecodeVectorKey(key)
	if err == nil {
		t.Fatal("expected error for missing ID separator, got nil")
	}
}

// TestDecodeVectorKey_InsufficientBytesForVersion verifies that DecodeVectorKey
// returns an error when there are not enough bytes for the 8-byte version field.
func TestDecodeVectorKey_InsufficientBytesForVersion(t *testing.T) {
	// Key: prefix + separator + namespace + separator + 8-byte ID + separator + 4 bytes
	ns := []byte("test")
	key := make([]byte, 0, 1+1+len(ns)+1+8+1+4)
	key = append(key, PrefixVector, Separator)
	key = append(key, ns...)
	key = append(key, Separator)
	id := make([]byte, 8)
	binary.BigEndian.PutUint64(id, 42)
	key = append(key, id...)
	key = append(key, Separator)
	key = append(key, 0x00, 0x00, 0x00, 0x01) // only 4 bytes for version, need 8

	_, _, _, err := DecodeVectorKey(key)
	if err == nil {
		t.Fatal("expected error for insufficient version bytes, got nil")
	}
}

// TestParseKey_VectorKeyDecodeError verifies that ParseKey returns an error
// when the vector key itself is malformed (triggers DecodeVectorKey error path).
func TestParseKey_VectorKeyDecodeError(t *testing.T) {
	// A key that starts with PrefixVector but is truncated after namespace.
	key := []byte{PrefixVector, Separator, 'n', 's'} // no namespace separator terminator

	_, err := ParseKey(key)
	if err == nil {
		t.Fatal("expected error for malformed vector key in ParseKey, got nil")
	}
}

// TestParseKey_IDSeqKey verifies that ParseKey handles PrefixIDSeq keys correctly.
func TestParseKey_IDSeqKey(t *testing.T) {
	key := EncodeIDSequenceKey("default")
	info, err := ParseKey(key)
	if err != nil {
		t.Fatalf("ParseKey for IDSeq key: %v", err)
	}
	if info.Type != "id_sequence" {
		t.Errorf("expected type %q, got %q", "id_sequence", info.Type)
	}
}

// TestParseKey_MetadataKeyDecodeError verifies that ParseKey returns an error
// when a metadata key is malformed.
func TestParseKey_MetadataKeyDecodeError(t *testing.T) {
	// A key starting with PrefixMetadata but truncated.
	key := []byte{PrefixMetadata, Separator, 'b', 'a', 'd'}

	_, err := ParseKey(key)
	if err == nil {
		t.Fatal("expected error for malformed metadata key in ParseKey, got nil")
	}
}

// TestParseKey_ReverseKeyDecodeError verifies that ParseKey returns an error
// when a reverse key is malformed.
func TestParseKey_ReverseKeyDecodeError(t *testing.T) {
	// A key starting with PrefixReverse but truncated.
	key := []byte{PrefixReverse, Separator, 'b', 'a', 'd'}

	_, err := ParseKey(key)
	if err == nil {
		t.Fatal("expected error for malformed reverse key in ParseKey, got nil")
	}
}

// TestParseKey_TombstoneWithValidID exercises the tombstone branch where
// decodeUint64 succeeds, setting info.ID. Since the tombstone key stores the
// ID as big-endian uint64 and splitKey splits on 0x00, we use a value where
// all 8 bytes are non-zero to avoid false splits.
func TestParseKey_TombstoneWithValidID(t *testing.T) {
	// 0x0102030405060708 has no zero bytes in its big-endian encoding.
	const testID = uint64(0x0102030405060708)
	const testTimestamp = uint64(0x090a0b0c0d0e0f10)
	key := EncodeTombstoneKey("myns", testID, testTimestamp)

	info, err := ParseKey(key)
	if err != nil {
		t.Fatalf("ParseKey tombstone: %v", err)
	}
	if info.Type != "tombstone" {
		t.Errorf("type = %q, want %q", info.Type, "tombstone")
	}
	if info.Namespace != "myns" {
		t.Errorf("namespace = %q, want %q", info.Namespace, "myns")
	}
	if info.ID != testID {
		t.Errorf("ID = %d, want %d", info.ID, testID)
	}
}
