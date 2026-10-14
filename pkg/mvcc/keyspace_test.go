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
// EncodeDataKey / DecodeDataKey roundtrip
// ---------------------------------------------------------------------------

func TestEncodeDecodeDataKey_Keyspace_Roundtrip(t *testing.T) {
	tests := []struct {
		name     string
		key      []byte
		revision int64
	}{
		{"simple_key_rev1", []byte("hello"), 1},
		{"simple_key_rev_max", []byte("hello"), math.MaxInt64},
		{"single_byte_key", []byte{0xAB}, 42},
		{"long_key", bytes.Repeat([]byte("x"), 1024), 100},
		{"revision_zero", []byte("zero"), 0},
		{"binary_key", []byte{0x00, 0xFF, 0x01, 0xFE}, 999},
		{"empty_key", []byte{}, 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := EncodeDataKey(tc.key, tc.revision)
			gotKey, gotRev, err := DecodeDataKey(encoded)
			if err != nil {
				t.Fatalf("DecodeDataKey: unexpected error: %v", err)
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

func TestEncodeDataKey_Keyspace_PrefixByte(t *testing.T) {
	encoded := EncodeDataKey([]byte("k"), 1)
	if encoded[0] != prefixData {
		t.Errorf("prefix byte: got 0x%02x, want 0x%02x", encoded[0], prefixData)
	}
}

func TestEncodeDataKey_Keyspace_Uses8ByteKeyLen(t *testing.T) {
	key := []byte("test-key")
	encoded := EncodeDataKey(key, 1)
	// Total: 1 prefix + 8 keyLen + len(key) + 8 complement
	expected := 1 + 8 + len(key) + 8
	if len(encoded) != expected {
		t.Errorf("length: got %d, want %d", len(encoded), expected)
	}
}

// ---------------------------------------------------------------------------
// DecodeDataKey error paths
// ---------------------------------------------------------------------------

func TestDecodeDataKey_Keyspace_TooShort(t *testing.T) {
	short := []byte{prefixData, 0x00, 0x00}
	_, _, err := DecodeDataKey(short)
	if err == nil {
		t.Fatal("DecodeDataKey: expected error for short data, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("DecodeDataKey: expected *EncodingError, got %T", err)
	}
}

func TestDecodeDataKey_Keyspace_WrongPrefix(t *testing.T) {
	buf := EncodeDataKey([]byte("key"), 1)
	buf[0] = prefixMeta // corrupt prefix
	_, _, err := DecodeDataKey(buf)
	if err == nil {
		t.Fatal("DecodeDataKey: expected error for wrong prefix, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("DecodeDataKey: expected *EncodingError, got %T", err)
	}
}

func TestDecodeDataKey_Keyspace_LengthMismatch(t *testing.T) {
	buf := EncodeDataKey([]byte("key"), 1)
	// Truncate last byte to cause length mismatch.
	buf = buf[:len(buf)-1]
	_, _, err := DecodeDataKey(buf)
	if err == nil {
		t.Fatal("DecodeDataKey: expected error for length mismatch, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("DecodeDataKey: expected *EncodingError, got %T", err)
	}
}

func TestDecodeDataKey_Keyspace_EmptyInput(t *testing.T) {
	_, _, err := DecodeDataKey(nil)
	if err == nil {
		t.Fatal("DecodeDataKey: expected error for nil input, got nil")
	}
}

// ---------------------------------------------------------------------------
// Complement encoding produces descending revision order
// ---------------------------------------------------------------------------

func TestEncodeDataKey_Keyspace_DescendingRevisionOrder(t *testing.T) {
	key := []byte("order-test")
	rev1 := EncodeDataKey(key, 1)
	rev2 := EncodeDataKey(key, 2)
	rev3 := EncodeDataKey(key, 3)
	rev100 := EncodeDataKey(key, 100)

	// Higher revisions should sort BEFORE lower revisions (descending).
	if bytes.Compare(rev100, rev3) >= 0 {
		t.Error("rev100 should sort before rev3")
	}
	if bytes.Compare(rev3, rev2) >= 0 {
		t.Error("rev3 should sort before rev2")
	}
	if bytes.Compare(rev2, rev1) >= 0 {
		t.Error("rev2 should sort before rev1")
	}
}

func TestEncodeDataKey_Keyspace_SameKeyDifferentRevisions_SharedPrefix(t *testing.T) {
	key := []byte("shared")
	prefix := DataKeyPrefix(key)
	rev5 := EncodeDataKey(key, 5)
	rev10 := EncodeDataKey(key, 10)

	// Both encoded keys should share the same prefix.
	if !bytes.HasPrefix(rev5, prefix) {
		t.Error("rev5 should have the data key prefix")
	}
	if !bytes.HasPrefix(rev10, prefix) {
		t.Error("rev10 should have the data key prefix")
	}
}

// ---------------------------------------------------------------------------
// DataKeyPrefix
// ---------------------------------------------------------------------------

func TestDataKeyPrefix_Keyspace_CorrectFormat(t *testing.T) {
	key := []byte("mykey")
	prefix := DataKeyPrefix(key)
	if prefix[0] != prefixData {
		t.Errorf("prefix[0]: got 0x%02x, want 0x%02x", prefix[0], prefixData)
	}
	// Length should be 1 + 8 + len(key).
	expected := 1 + 8 + len(key)
	if len(prefix) != expected {
		t.Errorf("prefix length: got %d, want %d", len(prefix), expected)
	}
}

func TestDataKeyPrefix_Keyspace_EmptyKey(t *testing.T) {
	prefix := DataKeyPrefix([]byte{})
	// 1 prefix + 8 keyLen + 0 key = 9 bytes.
	if len(prefix) != 9 {
		t.Errorf("prefix length: got %d, want 9", len(prefix))
	}
	if prefix[0] != prefixData {
		t.Errorf("prefix[0]: got 0x%02x, want 0x%02x", prefix[0], prefixData)
	}
}

// ---------------------------------------------------------------------------
// DataKeyUpperBound
// ---------------------------------------------------------------------------

func TestDataKeyUpperBound_Keyspace_StrictlyAfterAnyValidKey(t *testing.T) {
	key := []byte("bound-test")
	upper := DataKeyUpperBound(key)

	// Every valid encoded data key for this key should sort before upper.
	for _, rev := range []int64{0, 1, 100, math.MaxInt64} {
		encoded := EncodeDataKey(key, rev)
		if bytes.Compare(encoded, upper) >= 0 {
			t.Errorf("data key rev=%d should sort before upper bound", rev)
		}
	}
}

func TestDataKeyUpperBound_Keyspace_EmptyKey(t *testing.T) {
	upper := DataKeyUpperBound([]byte{})
	encoded := EncodeDataKey([]byte{}, 0)
	if bytes.Compare(encoded, upper) >= 0 {
		t.Error("empty key data entry should sort before upper bound")
	}
}

// ---------------------------------------------------------------------------
// EncodeMetaKey / DecodeMetaKey roundtrip
// ---------------------------------------------------------------------------

func TestEncodeDecodeMetaKey_Keyspace_Roundtrip(t *testing.T) {
	tests := []struct {
		name string
		key  []byte
	}{
		{"simple", []byte("hello")},
		{"single_byte", []byte{0xFF}},
		{"long_key", bytes.Repeat([]byte("z"), 1024)},
		{"binary", []byte{0x00, 0x01, 0x02}},
		{"empty_key", []byte{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := EncodeMetaKey(tc.key)
			gotKey, err := DecodeMetaKey(encoded)
			if err != nil {
				t.Fatalf("DecodeMetaKey: unexpected error: %v", err)
			}
			if !bytes.Equal(gotKey, tc.key) {
				t.Errorf("key: got %x, want %x", gotKey, tc.key)
			}
		})
	}
}

func TestEncodeMetaKey_Keyspace_PrefixByte(t *testing.T) {
	encoded := EncodeMetaKey([]byte("k"))
	if encoded[0] != prefixMeta {
		t.Errorf("prefix byte: got 0x%02x, want 0x%02x", encoded[0], prefixMeta)
	}
}

func TestEncodeMetaKey_Keyspace_Uses8ByteKeyLen(t *testing.T) {
	key := []byte("meta-key")
	encoded := EncodeMetaKey(key)
	// Total: 1 prefix + 8 keyLen + len(key)
	expected := 1 + 8 + len(key)
	if len(encoded) != expected {
		t.Errorf("length: got %d, want %d", len(encoded), expected)
	}
}

// ---------------------------------------------------------------------------
// DecodeMetaKey error paths
// ---------------------------------------------------------------------------

func TestDecodeMetaKey_Keyspace_TooShort(t *testing.T) {
	_, err := DecodeMetaKey(nil)
	if err == nil {
		t.Fatal("DecodeMetaKey: expected error for nil input, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("DecodeMetaKey: expected *EncodingError, got %T", err)
	}
}

func TestDecodeMetaKey_Keyspace_EmptySlice(t *testing.T) {
	_, err := DecodeMetaKey([]byte{})
	if err == nil {
		t.Fatal("DecodeMetaKey: expected error for empty slice, got nil")
	}
}

func TestDecodeMetaKey_Keyspace_WrongPrefix(t *testing.T) {
	buf := EncodeMetaKey([]byte("key"))
	buf[0] = prefixData // corrupt prefix
	_, err := DecodeMetaKey(buf)
	if err == nil {
		t.Fatal("DecodeMetaKey: expected error for wrong prefix, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("DecodeMetaKey: expected *EncodingError, got %T", err)
	}
}

func TestDecodeMetaKey_Keyspace_LengthMismatch(t *testing.T) {
	buf := EncodeMetaKey([]byte("key"))
	// Truncate last byte to cause length mismatch.
	buf = buf[:len(buf)-1]
	_, err := DecodeMetaKey(buf)
	if err == nil {
		t.Fatal("DecodeMetaKey: expected error for length mismatch, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("DecodeMetaKey: expected *EncodingError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Meta key ordering
// ---------------------------------------------------------------------------

func TestEncodeMetaKey_Keyspace_LexicographicOrder(t *testing.T) {
	a := EncodeMetaKey([]byte("aaa"))
	b := EncodeMetaKey([]byte("bbb"))
	c := EncodeMetaKey([]byte("ccc"))

	if bytes.Compare(a, b) >= 0 {
		t.Error("meta key 'aaa' should sort before 'bbb'")
	}
	if bytes.Compare(b, c) >= 0 {
		t.Error("meta key 'bbb' should sort before 'ccc'")
	}
}

func TestEncodeMetaKey_Keyspace_SameLengthKeysSort(t *testing.T) {
	// Keys of equal length should still sort lexicographically after
	// the length prefix (which is identical for same-length keys).
	x := EncodeMetaKey([]byte("xxx"))
	y := EncodeMetaKey([]byte("yyy"))
	if bytes.Compare(x, y) >= 0 {
		t.Error("meta key 'xxx' should sort before 'yyy'")
	}
}

// ---------------------------------------------------------------------------
// MetaLowerBound / MetaUpperBound
// ---------------------------------------------------------------------------

func TestMetaLowerBound_IsPrefix(t *testing.T) {
	lb := MetaLowerBound()
	if len(lb) != 1 || lb[0] != prefixMeta {
		t.Errorf("MetaLowerBound: got %x, want [0x02]", lb)
	}
}

func TestMetaUpperBound_IsRevisionPrefix(t *testing.T) {
	ub := MetaUpperBound()
	if len(ub) != 1 || ub[0] != prefixRevision {
		t.Errorf("MetaUpperBound: got %x, want [0x03]", ub)
	}
}

func TestMetaBounds_ContainAllMetaKeys(t *testing.T) {
	lb := MetaLowerBound()
	ub := MetaUpperBound()

	for _, key := range []string{"a", "middle", "zzz"} {
		mk := EncodeMetaKey([]byte(key))
		if bytes.Compare(mk, lb) < 0 {
			t.Errorf("meta key %q sorts before lower bound", key)
		}
		if bytes.Compare(mk, ub) >= 0 {
			t.Errorf("meta key %q sorts at or after upper bound", key)
		}
	}
}

// ---------------------------------------------------------------------------
// RevisionKey / CompactedKey
// ---------------------------------------------------------------------------

func TestRevisionKey_Keyspace_IsSingleByte(t *testing.T) {
	k := RevisionKey()
	if len(k) != 1 {
		t.Errorf("RevisionKey length: got %d, want 1", len(k))
	}
	if k[0] != prefixRevision {
		t.Errorf("RevisionKey[0]: got 0x%02x, want 0x%02x", k[0], prefixRevision)
	}
}

func TestRevisionKey_Keyspace_Deterministic(t *testing.T) {
	a := RevisionKey()
	b := RevisionKey()
	if !bytes.Equal(a, b) {
		t.Error("RevisionKey should return the same value on each call")
	}
}

func TestCompactedKey_Keyspace_IsSingleByte(t *testing.T) {
	k := CompactionFloorKey(nil)
	if len(k) != 1 {
		t.Errorf("CompactedKey length: got %d, want 1", len(k))
	}
	if k[0] != prefixCompact {
		t.Errorf("CompactedKey[0]: got 0x%02x, want 0x%02x", k[0], prefixCompact)
	}
}

func TestCompactedKey_Keyspace_Deterministic(t *testing.T) {
	a := CompactionFloorKey(nil)
	b := CompactionFloorKey(nil)
	if !bytes.Equal(a, b) {
		t.Error("CompactedKey should return the same value on each call")
	}
}

// ---------------------------------------------------------------------------
// EncodeRevision / DecodeRevision roundtrip
// ---------------------------------------------------------------------------

func TestEncodeDecodeRevision_Keyspace_Roundtrip(t *testing.T) {
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
			encoded := EncodeRevision(tc.rev)
			got, err := DecodeRevision(encoded)
			if err != nil {
				t.Fatalf("DecodeRevision: unexpected error: %v", err)
			}
			if got != tc.rev {
				t.Errorf("revision: got %d, want %d", got, tc.rev)
			}
		})
	}
}

func TestEncodeRevision_Keyspace_Length(t *testing.T) {
	encoded := EncodeRevision(42)
	if len(encoded) != 8 {
		t.Errorf("EncodeRevision length: got %d, want 8", len(encoded))
	}
}

// ---------------------------------------------------------------------------
// DecodeRevision error paths
// ---------------------------------------------------------------------------

func TestDecodeRevision_Keyspace_WrongSize(t *testing.T) {
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
			_, err := DecodeRevision(tc.data)
			if err == nil {
				t.Fatal("DecodeRevision: expected error, got nil")
			}
			var ee *EncodingError
			if !errors.As(err, &ee) {
				t.Fatalf("DecodeRevision: expected *EncodingError, got %T", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// EncodeMeta / DecodeMeta roundtrip
// ---------------------------------------------------------------------------

func TestEncodeMeta_Keyspace_Roundtrip(t *testing.T) {
	tests := []struct {
		name string
		meta *KeyMeta
	}{
		{
			"all_zeros",
			&KeyMeta{},
		},
		{
			"typical_values",
			&KeyMeta{
				CreateRevision: 10,
				ModRevision:    25,
				Version:        3,
				LeaseID:        100,
			},
		},
		{
			"large_values",
			&KeyMeta{
				CreateRevision: math.MaxInt64,
				ModRevision:    math.MaxInt64,
				Version:        math.MaxInt64,
				LeaseID:        math.MaxInt64,
			},
		},
		{
			"no_lease",
			&KeyMeta{
				CreateRevision: 1,
				ModRevision:    5,
				Version:        5,
				LeaseID:        0,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := EncodeMeta(tc.meta)
			if len(encoded) != metaSize {
				t.Fatalf("EncodeMeta length: got %d, want %d", len(encoded), metaSize)
			}
			got, err := DecodeMeta(encoded)
			if err != nil {
				t.Fatalf("DecodeMeta: unexpected error: %v", err)
			}
			if *got != *tc.meta {
				t.Errorf("metadata: got %+v, want %+v", *got, *tc.meta)
			}
		})
	}
}

func TestEncodeMeta_Keyspace_FixedSize(t *testing.T) {
	m := &KeyMeta{CreateRevision: 1, ModRevision: 2, Version: 3, LeaseID: 4}
	encoded := EncodeMeta(m)
	if len(encoded) != metaSize {
		t.Errorf("EncodeMeta length: got %d, want %d", len(encoded), metaSize)
	}
}

// ---------------------------------------------------------------------------
// DecodeMeta error paths
// ---------------------------------------------------------------------------

func TestDecodeMeta_Keyspace_WrongSize(t *testing.T) {
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
			_, err := DecodeMeta(tc.data)
			if err == nil {
				t.Fatal("DecodeMeta: expected error, got nil")
			}
			var ee *EncodingError
			if !errors.As(err, &ee) {
				t.Fatalf("DecodeMeta: expected *EncodingError, got %T", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// EncodeValue / DecodeValue roundtrip
// ---------------------------------------------------------------------------

func TestEncodeDecodeValue_Keyspace_Roundtrip(t *testing.T) {
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
			encoded := EncodeValue(tc.value)
			got, isTombstone, err := DecodeValue(encoded)
			if err != nil {
				t.Fatalf("DecodeValue: unexpected error: %v", err)
			}
			if isTombstone {
				t.Fatal("DecodeValue: unexpected tombstone")
			}
			if !bytes.Equal(got, tc.value) {
				t.Errorf("value: got %x, want %x", got, tc.value)
			}
		})
	}
}

func TestEncodeValue_Keyspace_PrefixByte(t *testing.T) {
	encoded := EncodeValue([]byte("v"))
	if encoded[0] != flagLive {
		t.Errorf("prefix byte: got 0x%02x, want 0x%02x", encoded[0], flagLive)
	}
}

// ---------------------------------------------------------------------------
// EncodeTombstone / DecodeValue
// ---------------------------------------------------------------------------

func TestEncodeTombstone_Keyspace_Roundtrip(t *testing.T) {
	encoded := EncodeTombstone()
	_, isTombstone, err := DecodeValue(encoded)
	if err != nil {
		t.Fatalf("DecodeValue: unexpected error: %v", err)
	}
	if !isTombstone {
		t.Error("DecodeValue: expected tombstone, got live entry")
	}
}

func TestEncodeTombstone_Keyspace_Length(t *testing.T) {
	encoded := EncodeTombstone()
	if len(encoded) != 1 {
		t.Errorf("EncodeTombstone length: got %d, want 1", len(encoded))
	}
	if encoded[0] != flagTombstone {
		t.Errorf("EncodeTombstone[0]: got 0x%02x, want 0x%02x", encoded[0], flagTombstone)
	}
}

// ---------------------------------------------------------------------------
// DecodeValue error paths
// ---------------------------------------------------------------------------

func TestDecodeValue_Keyspace_TooShort(t *testing.T) {
	_, _, err := DecodeValue(nil)
	if err == nil {
		t.Fatal("DecodeValue: expected error for nil, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("DecodeValue: expected *EncodingError, got %T", err)
	}
}

func TestDecodeValue_Keyspace_EmptySlice(t *testing.T) {
	_, _, err := DecodeValue([]byte{})
	if err == nil {
		t.Fatal("DecodeValue: expected error for empty slice, got nil")
	}
}

func TestDecodeValue_Keyspace_UnknownFlag(t *testing.T) {
	_, _, err := DecodeValue([]byte{0xFE, 0x01, 0x02})
	if err == nil {
		t.Fatal("DecodeValue: expected error for unknown flag, got nil")
	}
	var ee *EncodingError
	if !errors.As(err, &ee) {
		t.Fatalf("DecodeValue: expected *EncodingError, got %T", err)
	}
}

func TestDecodeValue_Keyspace_UnknownFlagDifferentValue(t *testing.T) {
	_, _, err := DecodeValue([]byte{0x99})
	if err == nil {
		t.Fatal("DecodeValue: expected error for unknown flag 0x99, got nil")
	}
}

// ---------------------------------------------------------------------------
// CompareKeys
// ---------------------------------------------------------------------------

func TestCompareKeys_Keyspace_Equal(t *testing.T) {
	if CompareKeys([]byte("abc"), []byte("abc")) != 0 {
		t.Error("CompareKeys: equal keys should return 0")
	}
}

func TestCompareKeys_Keyspace_LessThan(t *testing.T) {
	if CompareKeys([]byte("abc"), []byte("def")) >= 0 {
		t.Error("CompareKeys: 'abc' should be less than 'def'")
	}
}

func TestCompareKeys_Keyspace_GreaterThan(t *testing.T) {
	if CompareKeys([]byte("xyz"), []byte("abc")) <= 0 {
		t.Error("CompareKeys: 'xyz' should be greater than 'abc'")
	}
}

func TestCompareKeys_Keyspace_EmptyKeys(t *testing.T) {
	if CompareKeys([]byte{}, []byte{}) != 0 {
		t.Error("CompareKeys: empty keys should be equal")
	}
}

func TestCompareKeys_Keyspace_EmptyLessThanNonEmpty(t *testing.T) {
	if CompareKeys([]byte{}, []byte("a")) >= 0 {
		t.Error("CompareKeys: empty should be less than 'a'")
	}
}

func TestCompareKeys_Keyspace_PrefixComparison(t *testing.T) {
	if CompareKeys([]byte("abc"), []byte("abcd")) >= 0 {
		t.Error("CompareKeys: 'abc' should be less than 'abcd'")
	}
}

// ---------------------------------------------------------------------------
// IsInRange
// ---------------------------------------------------------------------------

func TestIsInRange_Keyspace_InRange(t *testing.T) {
	if !IsInRange([]byte("b"), []byte("a"), []byte("c")) {
		t.Error("IsInRange: 'b' should be in [a, c)")
	}
}

func TestIsInRange_Keyspace_AtStart(t *testing.T) {
	if !IsInRange([]byte("a"), []byte("a"), []byte("c")) {
		t.Error("IsInRange: 'a' should be in [a, c)")
	}
}

func TestIsInRange_Keyspace_AtEndExclusive(t *testing.T) {
	if IsInRange([]byte("c"), []byte("a"), []byte("c")) {
		t.Error("IsInRange: 'c' should NOT be in [a, c)")
	}
}

func TestIsInRange_Keyspace_BeforeStart(t *testing.T) {
	if IsInRange([]byte("0"), []byte("a"), []byte("c")) {
		t.Error("IsInRange: '0' should NOT be in [a, c)")
	}
}

func TestIsInRange_Keyspace_AfterEnd(t *testing.T) {
	if IsInRange([]byte("z"), []byte("a"), []byte("c")) {
		t.Error("IsInRange: 'z' should NOT be in [a, c)")
	}
}

func TestIsInRange_Keyspace_NilEnd(t *testing.T) {
	if !IsInRange([]byte("z"), []byte("a"), nil) {
		t.Error("IsInRange: 'z' should be in [a, nil)")
	}
}

func TestIsInRange_Keyspace_NilEndBeforeStart(t *testing.T) {
	if IsInRange([]byte("0"), []byte("a"), nil) {
		t.Error("IsInRange: '0' should NOT be in [a, nil)")
	}
}

// ---------------------------------------------------------------------------
// complementRevision / uncomplement
// ---------------------------------------------------------------------------

func TestComplementRevision_Keyspace_Roundtrip(t *testing.T) {
	tests := []int64{0, 1, 42, 1000, math.MaxInt64}
	for _, rev := range tests {
		c := complementRevision(rev)
		got := uncomplement(c)
		if got != rev {
			t.Errorf("complement roundtrip: rev=%d, complement=%d, got=%d", rev, c, got)
		}
	}
}

func TestComplementRevision_Keyspace_DescendingOrder(t *testing.T) {
	// Higher revision should have lower complement.
	c1 := complementRevision(1)
	c2 := complementRevision(2)
	c100 := complementRevision(100)

	if c100 >= c2 {
		t.Error("complement(100) should be less than complement(2)")
	}
	if c2 >= c1 {
		t.Error("complement(2) should be less than complement(1)")
	}
}

// ---------------------------------------------------------------------------
// Cross-namespace prefix isolation
// ---------------------------------------------------------------------------

func TestPrefixes_Keyspace_NoOverlap(t *testing.T) {
	prefixes := []byte{prefixData, prefixMeta, prefixRevision, prefixCompact}
	seen := make(map[byte]bool)
	for _, p := range prefixes {
		if seen[p] {
			t.Errorf("duplicate prefix: 0x%02x", p)
		}
		seen[p] = true
	}
}

func TestPrefixes_Keyspace_DataDoesNotOverlapMeta(t *testing.T) {
	dk := EncodeDataKey([]byte("key"), 1)
	mk := EncodeMetaKey([]byte("key"))
	if dk[0] == mk[0] {
		t.Error("data key and meta key share the same prefix byte")
	}
}

// ---------------------------------------------------------------------------
// Edge case: MaxInt64 revision
// ---------------------------------------------------------------------------

func TestEncodeDataKey_Keyspace_MaxRevisionRoundtrip(t *testing.T) {
	key := []byte("max-rev")
	encoded := EncodeDataKey(key, math.MaxInt64)
	gotKey, gotRev, err := DecodeDataKey(encoded)
	if err != nil {
		t.Fatalf("DecodeDataKey: %v", err)
	}
	if !bytes.Equal(gotKey, key) {
		t.Errorf("key mismatch")
	}
	if gotRev != math.MaxInt64 {
		t.Errorf("revision: got %d, want %d", gotRev, int64(math.MaxInt64))
	}
}

func TestEncodeDataKey_Keyspace_ZeroRevisionRoundtrip(t *testing.T) {
	key := []byte("zero-rev")
	encoded := EncodeDataKey(key, 0)
	gotKey, gotRev, err := DecodeDataKey(encoded)
	if err != nil {
		t.Fatalf("DecodeDataKey: %v", err)
	}
	if !bytes.Equal(gotKey, key) {
		t.Errorf("key mismatch")
	}
	if gotRev != 0 {
		t.Errorf("revision: got %d, want 0", gotRev)
	}
}

func TestEncodeDecodeRevIndexKey_Keyspace_Roundtrip(t *testing.T) {
	rev, key, err := DecodeRevIndexKey(EncodeRevIndexKey(77, []byte("user/key")))
	if err != nil {
		t.Fatalf("DecodeRevIndexKey: %v", err)
	}
	if rev != 77 || !bytes.Equal(key, []byte("user/key")) {
		t.Errorf("DecodeRevIndexKey = (%d, %q), want (77, %q)", rev, key, "user/key")
	}
}

func TestDecodeRevIndexKey_Keyspace_TooShort(t *testing.T) {
	encoded := EncodeRevIndexKey(5, nil)
	_, _, err := DecodeRevIndexKey(encoded[:len(encoded)-1])
	var encErr *EncodingError
	if !errors.As(err, &encErr) || encErr.Reason != "rev index key too short" {
		t.Fatalf("err = %v, want *EncodingError for a key shorter than its revision", err)
	}
}

func TestDecodeRevIndexKey_Keyspace_WrongPrefix(t *testing.T) {
	encoded := EncodeRevIndexKey(5, []byte("k"))
	encoded[0] = prefixData
	_, _, err := DecodeRevIndexKey(encoded)
	var encErr *EncodingError
	if !errors.As(err, &encErr) || encErr.Reason != "invalid rev index key prefix" {
		t.Fatalf("err = %v, want *EncodingError for a data-key prefix", err)
	}
}
