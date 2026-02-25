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
	"errors"
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// encodeDataKey / decodeDataKey roundtrip
// ---------------------------------------------------------------------------

func TestEncodeDecodeDataKey_Roundtrip(t *testing.T) {
	tests := []struct {
		name     string
		key      []byte
		revision int64
	}{
		{"simple_key_rev1", []byte("hello"), 1},
		{"simple_key_rev_max", []byte("hello"), math.MaxInt64},
		{"single_byte_key", []byte{0xAB}, 42},
		{"long_key", bytes.Repeat([]byte("x"), 256), 100},
		{"revision_zero", []byte("zero"), 0},
		{"binary_key", []byte{0x00, 0xFF, 0x01, 0xFE}, 999},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := encodeDataKey(tc.key, tc.revision)
			gotKey, gotRev, err := decodeDataKey(encoded)
			if err != nil {
				t.Fatalf("decodeDataKey: unexpected error: %v", err)
			}
			if !bytes.Equal(gotKey, tc.key) {
				t.Errorf("key: got %x, want %x", gotKey, tc.key)
			}
			if gotRev != tc.revision {
				t.Errorf("revision: got %d, want %d", gotRev, tc.revision)
			}
		})
	}
}

func TestEncodeDataKey_PrefixByte(t *testing.T) {
	encoded := encodeDataKey([]byte("k"), 1)
	if encoded[0] != dataPrefix {
		t.Errorf("prefix byte: got 0x%02x, want 0x%02x", encoded[0], dataPrefix)
	}
}

// ---------------------------------------------------------------------------
// decodeDataKey error paths
// ---------------------------------------------------------------------------

func TestDecodeDataKey_TooShort(t *testing.T) {
	// Minimum valid data key is 11 bytes: 1 prefix + 2 keyLen + 0 key + 8 rev.
	short := []byte{dataPrefix, 0x00, 0x00, 0x00, 0x00}
	_, _, err := decodeDataKey(short)
	if err == nil {
		t.Fatal("decodeDataKey: expected error for short data, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("decodeDataKey: expected *EncodingError, got %T", err)
	}
}

func TestDecodeDataKey_WrongPrefix(t *testing.T) {
	buf := encodeDataKey([]byte("key"), 1)
	buf[0] = metaPrefix // corrupt prefix
	_, _, err := decodeDataKey(buf)
	if err == nil {
		t.Fatal("decodeDataKey: expected error for wrong prefix, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("decodeDataKey: expected *EncodingError, got %T", err)
	}
}

func TestDecodeDataKey_LengthMismatch(t *testing.T) {
	buf := encodeDataKey([]byte("key"), 1)
	// Truncate last byte to cause length mismatch.
	buf = buf[:len(buf)-1]
	_, _, err := decodeDataKey(buf)
	if err == nil {
		t.Fatal("decodeDataKey: expected error for length mismatch, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("decodeDataKey: expected *EncodingError, got %T", err)
	}
}

func TestDecodeDataKey_EmptyInput(t *testing.T) {
	_, _, err := decodeDataKey(nil)
	if err == nil {
		t.Fatal("decodeDataKey: expected error for nil input, got nil")
	}
}

// ---------------------------------------------------------------------------
// Complement encoding produces descending revision order
// ---------------------------------------------------------------------------

func TestEncodeDataKey_DescendingRevisionOrder(t *testing.T) {
	key := []byte("order-test")
	rev1 := encodeDataKey(key, 1)
	rev2 := encodeDataKey(key, 2)
	rev100 := encodeDataKey(key, 100)

	// Higher revisions should sort BEFORE lower revisions (descending).
	if bytes.Compare(rev100, rev2) >= 0 {
		t.Error("rev100 should sort before rev2")
	}
	if bytes.Compare(rev2, rev1) >= 0 {
		t.Error("rev2 should sort before rev1")
	}
}

func TestEncodeDataKey_SameKeyDifferentRevisions_SharedPrefix(t *testing.T) {
	key := []byte("shared")
	prefix := dataKeyPrefix(key)
	rev5 := encodeDataKey(key, 5)
	rev10 := encodeDataKey(key, 10)

	// Both encoded keys should share the same prefix.
	if !bytes.HasPrefix(rev5, prefix) {
		t.Error("rev5 should have the data key prefix")
	}
	if !bytes.HasPrefix(rev10, prefix) {
		t.Error("rev10 should have the data key prefix")
	}
}

// ---------------------------------------------------------------------------
// dataKeyPrefix
// ---------------------------------------------------------------------------

func TestDataKeyPrefix_CorrectFormat(t *testing.T) {
	key := []byte("mykey")
	prefix := dataKeyPrefix(key)
	if prefix[0] != dataPrefix {
		t.Errorf("prefix[0]: got 0x%02x, want 0x%02x", prefix[0], dataPrefix)
	}
	// Length should be 1 + 2 + len(key).
	expected := 1 + 2 + len(key)
	if len(prefix) != expected {
		t.Errorf("prefix length: got %d, want %d", len(prefix), expected)
	}
}

func TestDataKeyPrefix_EmptyKey(t *testing.T) {
	prefix := dataKeyPrefix([]byte{})
	// 1 prefix + 2 keyLen + 0 key = 3 bytes.
	if len(prefix) != 3 {
		t.Errorf("prefix length: got %d, want 3", len(prefix))
	}
	if prefix[0] != dataPrefix {
		t.Errorf("prefix[0]: got 0x%02x, want 0x%02x", prefix[0], dataPrefix)
	}
}

// ---------------------------------------------------------------------------
// dataKeyUpperBound
// ---------------------------------------------------------------------------

func TestDataKeyUpperBound_StrictlyAfterAnyValidKey(t *testing.T) {
	key := []byte("bound-test")
	upper := dataKeyUpperBound(key)

	// Every valid encoded data key for this key should sort before upper.
	for _, rev := range []int64{0, 1, 100, math.MaxInt64} {
		encoded := encodeDataKey(key, rev)
		if bytes.Compare(encoded, upper) >= 0 {
			t.Errorf("data key rev=%d should sort before upper bound", rev)
		}
	}
}

func TestDataKeyUpperBound_EmptyKey(t *testing.T) {
	upper := dataKeyUpperBound([]byte{})
	encoded := encodeDataKey([]byte{}, 0)
	if bytes.Compare(encoded, upper) >= 0 {
		t.Error("empty key data entry should sort before upper bound")
	}
}

// ---------------------------------------------------------------------------
// encodeMetaKey / decodeMetaKey roundtrip
// ---------------------------------------------------------------------------

func TestEncodeDecodeMetaKey_Roundtrip(t *testing.T) {
	tests := []struct {
		name string
		key  []byte
	}{
		{"simple", []byte("hello")},
		{"single_byte", []byte{0xFF}},
		{"long_key", bytes.Repeat([]byte("z"), 1024)},
		{"binary", []byte{0x00, 0x01, 0x02}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := encodeMetaKey(tc.key)
			gotKey, err := decodeMetaKey(encoded)
			if err != nil {
				t.Fatalf("decodeMetaKey: unexpected error: %v", err)
			}
			if !bytes.Equal(gotKey, tc.key) {
				t.Errorf("key: got %x, want %x", gotKey, tc.key)
			}
		})
	}
}

func TestEncodeMetaKey_PrefixByte(t *testing.T) {
	encoded := encodeMetaKey([]byte("k"))
	if encoded[0] != metaPrefix {
		t.Errorf("prefix byte: got 0x%02x, want 0x%02x", encoded[0], metaPrefix)
	}
}

// ---------------------------------------------------------------------------
// decodeMetaKey error paths
// ---------------------------------------------------------------------------

func TestDecodeMetaKey_TooShort(t *testing.T) {
	_, err := decodeMetaKey(nil)
	if err == nil {
		t.Fatal("decodeMetaKey: expected error for nil input, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("decodeMetaKey: expected *EncodingError, got %T", err)
	}
}

func TestDecodeMetaKey_EmptySlice(t *testing.T) {
	_, err := decodeMetaKey([]byte{})
	if err == nil {
		t.Fatal("decodeMetaKey: expected error for empty slice, got nil")
	}
}

func TestDecodeMetaKey_WrongPrefix(t *testing.T) {
	buf := encodeMetaKey([]byte("key"))
	buf[0] = dataPrefix // corrupt prefix
	_, err := decodeMetaKey(buf)
	if err == nil {
		t.Fatal("decodeMetaKey: expected error for wrong prefix, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("decodeMetaKey: expected *EncodingError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Meta key lexicographic ordering
// ---------------------------------------------------------------------------

func TestEncodeMetaKey_LexicographicOrder(t *testing.T) {
	a := encodeMetaKey([]byte("aaa"))
	b := encodeMetaKey([]byte("bbb"))
	z := encodeMetaKey([]byte("zzz"))

	if bytes.Compare(a, b) >= 0 {
		t.Error("meta key 'aaa' should sort before 'bbb'")
	}
	if bytes.Compare(b, z) >= 0 {
		t.Error("meta key 'bbb' should sort before 'zzz'")
	}
}

// ---------------------------------------------------------------------------
// revisionKey / compactedKey
// ---------------------------------------------------------------------------

func TestRevisionKey_IsSingleByte(t *testing.T) {
	k := revisionKey()
	if len(k) != 1 {
		t.Errorf("revisionKey length: got %d, want 1", len(k))
	}
	if k[0] != revisionPrefix {
		t.Errorf("revisionKey[0]: got 0x%02x, want 0x%02x", k[0], revisionPrefix)
	}
}

func TestRevisionKey_Deterministic(t *testing.T) {
	a := revisionKey()
	b := revisionKey()
	if !bytes.Equal(a, b) {
		t.Error("revisionKey should return the same value on each call")
	}
}

func TestCompactedKey_IsSingleByte(t *testing.T) {
	k := compactedKey()
	if len(k) != 1 {
		t.Errorf("compactedKey length: got %d, want 1", len(k))
	}
	if k[0] != compactedPrefix {
		t.Errorf("compactedKey[0]: got 0x%02x, want 0x%02x", k[0], compactedPrefix)
	}
}

func TestCompactedKey_Deterministic(t *testing.T) {
	a := compactedKey()
	b := compactedKey()
	if !bytes.Equal(a, b) {
		t.Error("compactedKey should return the same value on each call")
	}
}

// ---------------------------------------------------------------------------
// encodeRevision / decodeRevision roundtrip
// ---------------------------------------------------------------------------

func TestEncodeDecodeRevision_Roundtrip(t *testing.T) {
	tests := []struct {
		name string
		rev  int64
	}{
		{"zero", 0},
		{"one", 1},
		{"large", 1_000_000_000},
		{"max_int64", math.MaxInt64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := encodeRevision(tc.rev)
			got, err := decodeRevision(encoded)
			if err != nil {
				t.Fatalf("decodeRevision: unexpected error: %v", err)
			}
			if got != tc.rev {
				t.Errorf("revision: got %d, want %d", got, tc.rev)
			}
		})
	}
}

func TestEncodeRevision_Length(t *testing.T) {
	encoded := encodeRevision(42)
	if len(encoded) != 8 {
		t.Errorf("encodeRevision length: got %d, want 8", len(encoded))
	}
}

// ---------------------------------------------------------------------------
// decodeRevision error paths
// ---------------------------------------------------------------------------

func TestDecodeRevision_WrongSize(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"too_short", []byte{0x01, 0x02, 0x03}},
		{"too_long", make([]byte, 9)},
		{"seven_bytes", make([]byte, 7)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeRevision(tc.data)
			if err == nil {
				t.Fatal("decodeRevision: expected error, got nil")
			}
			var ee *EncodingError
			if !errors.As(err, &ee) {
				t.Fatalf("decodeRevision: expected *EncodingError, got %T", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// metadata encode / decode roundtrip
// ---------------------------------------------------------------------------

func TestMetadata_EncodeDecodeRoundtrip(t *testing.T) {
	tests := []struct {
		name string
		meta metadata
	}{
		{
			"all_zeros",
			metadata{},
		},
		{
			"typical_values",
			metadata{
				CreateRevision: 10,
				ModRevision:    25,
				Version:        3,
				Lease:          100,
			},
		},
		{
			"large_values",
			metadata{
				CreateRevision: math.MaxInt64,
				ModRevision:    math.MaxInt64,
				Version:        math.MaxInt64,
				Lease:          math.MaxInt64,
			},
		},
		{
			"no_lease",
			metadata{
				CreateRevision: 1,
				ModRevision:    5,
				Version:        5,
				Lease:          0,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := tc.meta.encode()
			if len(encoded) != metadataSize {
				t.Fatalf("encode length: got %d, want %d", len(encoded), metadataSize)
			}
			got, err := decodeMetadata(encoded)
			if err != nil {
				t.Fatalf("decodeMetadata: unexpected error: %v", err)
			}
			if got != tc.meta {
				t.Errorf("metadata: got %+v, want %+v", got, tc.meta)
			}
		})
	}
}

func TestMetadata_Encode_FixedSize(t *testing.T) {
	m := metadata{CreateRevision: 1, ModRevision: 2, Version: 3, Lease: 4}
	encoded := m.encode()
	if len(encoded) != metadataSize {
		t.Errorf("encode length: got %d, want %d", len(encoded), metadataSize)
	}
}

// ---------------------------------------------------------------------------
// decodeMetadata error paths
// ---------------------------------------------------------------------------

func TestDecodeMetadata_WrongSize(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"too_short", make([]byte, 31)},
		{"too_long", make([]byte, 33)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeMetadata(tc.data)
			if err == nil {
				t.Fatal("decodeMetadata: expected error, got nil")
			}
			var ee *EncodingError
			if !errors.As(err, &ee) {
				t.Fatalf("decodeMetadata: expected *EncodingError, got %T", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// encodeDataValue / decodeDataValue roundtrip
// ---------------------------------------------------------------------------

func TestEncodeDecodeDataValue_Roundtrip(t *testing.T) {
	tests := []struct {
		name  string
		value []byte
	}{
		{"empty_value", []byte{}},
		{"simple", []byte("hello world")},
		{"binary", []byte{0x00, 0xFF, 0x80}},
		{"long_value", bytes.Repeat([]byte("v"), 4096)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := encodeDataValue(tc.value)
			got, isTombstone, err := decodeDataValue(encoded)
			if err != nil {
				t.Fatalf("decodeDataValue: unexpected error: %v", err)
			}
			if isTombstone {
				t.Fatal("decodeDataValue: unexpected tombstone")
			}
			if !bytes.Equal(got, tc.value) {
				t.Errorf("value: got %x, want %x", got, tc.value)
			}
		})
	}
}

func TestEncodeDataValue_PrefixByte(t *testing.T) {
	encoded := encodeDataValue([]byte("v"))
	if encoded[0] != entryLive {
		t.Errorf("prefix byte: got 0x%02x, want 0x%02x", encoded[0], entryLive)
	}
}

// ---------------------------------------------------------------------------
// encodeTombstone / decodeDataValue
// ---------------------------------------------------------------------------

func TestEncodeTombstone_Roundtrip(t *testing.T) {
	encoded := encodeTombstone()
	_, isTombstone, err := decodeDataValue(encoded)
	if err != nil {
		t.Fatalf("decodeDataValue: unexpected error: %v", err)
	}
	if !isTombstone {
		t.Error("decodeDataValue: expected tombstone, got live entry")
	}
}

func TestEncodeTombstone_Length(t *testing.T) {
	encoded := encodeTombstone()
	if len(encoded) != 1 {
		t.Errorf("encodeTombstone length: got %d, want 1", len(encoded))
	}
	if encoded[0] != entryTombstone {
		t.Errorf("encodeTombstone[0]: got 0x%02x, want 0x%02x", encoded[0], entryTombstone)
	}
}

// ---------------------------------------------------------------------------
// decodeDataValue error paths
// ---------------------------------------------------------------------------

func TestDecodeDataValue_TooShort(t *testing.T) {
	_, _, err := decodeDataValue(nil)
	if err == nil {
		t.Fatal("decodeDataValue: expected error for nil, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("decodeDataValue: expected *EncodingError, got %T", err)
	}
}

func TestDecodeDataValue_EmptySlice(t *testing.T) {
	_, _, err := decodeDataValue([]byte{})
	if err == nil {
		t.Fatal("decodeDataValue: expected error for empty slice, got nil")
	}
}

func TestDecodeDataValue_UnknownFlag(t *testing.T) {
	_, _, err := decodeDataValue([]byte{0xFE, 0x01, 0x02})
	if err == nil {
		t.Fatal("decodeDataValue: expected error for unknown flag, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("decodeDataValue: expected *EncodingError, got %T", err)
	}
}

func TestDecodeDataValue_UnknownFlagDifferentValue(t *testing.T) {
	_, _, err := decodeDataValue([]byte{0x99})
	if err == nil {
		t.Fatal("decodeDataValue: expected error for unknown flag 0x99, got nil")
	}
}

// ---------------------------------------------------------------------------
// Cross-namespace prefix isolation
// ---------------------------------------------------------------------------

func TestPrefixes_NoOverlap(t *testing.T) {
	// Ensure the four prefix bytes are distinct.
	prefixes := []byte{dataPrefix, metaPrefix, revisionPrefix, compactedPrefix}
	seen := make(map[byte]bool)
	for _, p := range prefixes {
		if seen[p] {
			t.Errorf("duplicate prefix: 0x%02x", p)
		}
		seen[p] = true
	}
}

func TestPrefixes_DataDoesNotOverlapMeta(t *testing.T) {
	dk := encodeDataKey([]byte("key"), 1)
	mk := encodeMetaKey([]byte("key"))
	if dk[0] == mk[0] {
		t.Error("data key and meta key share the same prefix byte")
	}
}
