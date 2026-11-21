// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package schema

import (
	"bytes"
	"testing"
)

func TestEncodeVectorKeyPrefix(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		id        uint64
	}{
		{"simple", "default", 1},
		{"large_id", "default", ^uint64(0)},
		{"long_namespace", "very-long-namespace-name", 12345},
		{"zero_id", "test", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := EncodeVectorKeyPrefix(tt.namespace, tt.id)

			// Verify prefix structure
			if prefix[0] != PrefixVector {
				t.Errorf("expected prefix %c, got %c", PrefixVector, prefix[0])
			}

			if prefix[1] != Separator {
				t.Errorf("expected separator at position 1")
			}

			// Verify it's a valid prefix for full keys
			key1 := EncodeVectorKey(tt.namespace, tt.id, 1)
			key2 := EncodeVectorKey(tt.namespace, tt.id, 100)

			if !bytes.HasPrefix(key1, prefix) {
				t.Error("version 1 key should match prefix")
			}

			if !bytes.HasPrefix(key2, prefix) {
				t.Error("version 100 key should match prefix")
			}

			// Different ID should not match
			differentKey := EncodeVectorKey(tt.namespace, tt.id+1, 1)
			if bytes.HasPrefix(differentKey, prefix) {
				t.Error("different ID should not match prefix")
			}
		})
	}
}

func TestEncodeReverseKeyPrefix(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		id        uint64
	}{
		{"simple", "default", 1},
		{"large_id", "test", 999999},
		{"zero_id", "ns", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := EncodeReverseKeyPrefix(tt.namespace, tt.id)

			// Verify prefix structure
			if prefix[0] != PrefixReverse {
				t.Errorf("expected prefix %c, got %c", PrefixReverse, prefix[0])
			}

			if prefix[1] != Separator {
				t.Errorf("expected separator at position 1")
			}

			// Verify it matches full reverse keys
			key1 := EncodeReverseKey(tt.namespace, tt.id, "field1", 1)
			key2 := EncodeReverseKey(tt.namespace, tt.id, "field2", 1)

			if !bytes.HasPrefix(key1, prefix) {
				t.Error("field1 key should match prefix")
			}

			if !bytes.HasPrefix(key2, prefix) {
				t.Error("field2 key should match prefix")
			}

			// Different ID should not match
			differentKey := EncodeReverseKey(tt.namespace, tt.id+1, "field1", 1)
			if bytes.HasPrefix(differentKey, prefix) {
				t.Error("different ID should not match prefix")
			}
		})
	}
}

func TestEncodeNamespacesPrefix(t *testing.T) {
	prefix := EncodeNamespacesPrefix()

	// Verify structure
	if len(prefix) != 2 {
		t.Errorf("expected length 2, got %d", len(prefix))
	}

	if prefix[0] != PrefixNamespace {
		t.Errorf("expected prefix %c, got %c", PrefixNamespace, prefix[0])
	}

	if prefix[1] != Separator {
		t.Errorf("expected separator at position 1")
	}

	// Verify it matches namespace keys
	key1 := EncodeNamespaceMetaKey("default")
	key2 := EncodeNamespaceMetaKey("test")
	key3 := EncodeNamespaceMetaKey("production")

	if !bytes.HasPrefix(key1, prefix) {
		t.Error("default namespace key should match prefix")
	}

	if !bytes.HasPrefix(key2, prefix) {
		t.Error("test namespace key should match prefix")
	}

	if !bytes.HasPrefix(key3, prefix) {
		t.Error("production namespace key should match prefix")
	}

	// Keys with different prefixes should not match
	vectorKey := EncodeVectorKey("default", 1, 1)
	if bytes.HasPrefix(vectorKey, prefix) {
		t.Error("vector key should not match namespace prefix")
	}
}

func TestEncodeTombstonePrefix(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
	}{
		{"simple", "default"},
		{"long_name", "very-long-namespace-name-for-testing"},
		{"special_chars", "test-ns_123"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := EncodeTombstonePrefix(tt.namespace)

			// Verify structure
			if prefix[0] != PrefixTombstone {
				t.Errorf("expected prefix %c, got %c", PrefixTombstone, prefix[0])
			}

			if prefix[1] != Separator {
				t.Errorf("expected separator at position 1")
			}

			// Verify it matches tombstone keys
			key1 := EncodeTombstoneKey(tt.namespace, 1, 1000)
			key2 := EncodeTombstoneKey(tt.namespace, 2, 2000)

			if !bytes.HasPrefix(key1, prefix) {
				t.Error("tombstone key 1 should match prefix")
			}

			if !bytes.HasPrefix(key2, prefix) {
				t.Error("tombstone key 2 should match prefix")
			}

			// Different namespace should not match
			if tt.namespace != "" {
				differentKey := EncodeTombstoneKey(tt.namespace+"_different", 1, 1000)
				if bytes.HasPrefix(differentKey, prefix) {
					t.Error("different namespace should not match prefix")
				}
			}
		})
	}
}

func TestDecodeMetadataKeyErrors(t *testing.T) {
	tests := []struct {
		name string
		key  []byte
	}{
		{"empty", []byte{}},
		{"too_short", []byte{PrefixMetadata}},
		{"wrong_prefix", []byte{PrefixVector, Separator, 'd', 'e', 'f'}},
		{"missing_namespace_sep", []byte{PrefixMetadata, Separator, 'd', 'e', 'f'}},
		{"missing_field_sep", []byte{PrefixMetadata, Separator, 'n', 's', Separator, 'f', 'i', 'e', 'l', 'd'}},
		{"missing_value_sep", []byte{PrefixMetadata, Separator, 'n', Separator, 'f', Separator, 'v'}},
		{"insufficient_id_bytes", []byte{PrefixMetadata, Separator, 'n', Separator, 'f', Separator, 'v', Separator, 0x01}},
		{"missing_id_sep", []byte{PrefixMetadata, Separator, 'n', Separator, 'f', Separator, 'v', Separator, 0, 0, 0, 0, 0, 0, 0, 1}},
		{"insufficient_version_bytes", []byte{PrefixMetadata, Separator, 'n', Separator, 'f', Separator, 'v', Separator, 0, 0, 0, 0, 0, 0, 0, 1, Separator, 0x01}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, _, _, err := DecodeMetadataKey(tt.key)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestDecodeReverseKeyErrors(t *testing.T) {
	tests := []struct {
		name string
		key  []byte
	}{
		{"empty", []byte{}},
		{"too_short", []byte{PrefixReverse}},
		{"wrong_prefix", []byte{PrefixVector, Separator}},
		{"missing_namespace_sep", []byte{PrefixReverse, Separator, 'n', 's'}},
		{"insufficient_id_bytes", []byte{PrefixReverse, Separator, 'n', 's', Separator, 0x01}},
		{"missing_id_sep", []byte{PrefixReverse, Separator, 'n', Separator, 0, 0, 0, 0, 0, 0, 0, 1}},
		{"missing_field_sep", []byte{PrefixReverse, Separator, 'n', Separator, 0, 0, 0, 0, 0, 0, 0, 1, Separator, 'f'}},
		{"insufficient_version_bytes", []byte{PrefixReverse, Separator, 'n', Separator, 0, 0, 0, 0, 0, 0, 0, 1, Separator, 'f', Separator, 0x01}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, _, err := DecodeReverseKey(tt.key)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestParseKeyIDSequence(t *testing.T) {
	key := EncodeIDSequenceKey("test-namespace")
	info, err := ParseKey(key)
	if err != nil {
		t.Fatalf("ParseKey failed: %v", err)
	}

	if info.Type != "id_sequence" {
		t.Errorf("expected type 'id_sequence', got %q", info.Type)
	}

	if info.Namespace != "test-namespace" {
		t.Errorf("expected namespace 'test-namespace', got %q", info.Namespace)
	}

	// Test String() output
	s := info.String()
	if !bytes.Contains([]byte(s), []byte("id_sequence")) {
		t.Error("String() should contain type")
	}
}

func TestSplitKey(t *testing.T) {
	tests := []struct {
		name     string
		key      []byte
		expected int
	}{
		{"no_separators", []byte("hello"), 1},
		{"one_separator", []byte{'a', Separator, 'b'}, 2},
		{"multiple_separators", []byte{'a', Separator, 'b', Separator, 'c', Separator, 'd'}, 4},
		{"trailing_separator", []byte{'a', 'b', Separator}, 1}, // Doesn't add empty part
		{"leading_separator", []byte{Separator, 'a', 'b'}, 2},  // Empty first part, then content
		{"empty", []byte{}, 0},
		{"only_separator", []byte{Separator}, 1}, // One empty part before separator
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts := splitKey(tt.key)
			if len(parts) != tt.expected {
				t.Errorf("splitKey(%q) returned %d parts, want %d. Parts: %v", tt.key, len(parts), tt.expected, parts)
			}
		})
	}
}

func TestPrefixSuccessorEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		prefix   []byte
		expected []byte
	}{
		{"single_0x00", []byte{0x00}, []byte{0x01}},
		{"single_0x7F", []byte{0x7F}, []byte{0x80}},
		{"single_0xFE", []byte{0xFE}, []byte{0xFF}},
		{"rollover_middle", []byte{0x01, 0xFF, 0x02}, []byte{0x01, 0xFF, 0x03}},
		{"complex_rollover", []byte{0x01, 0x02, 0xFF, 0xFF}, []byte{0x01, 0x03}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := PrefixSuccessor(tt.prefix)
			if !bytes.Equal(result, tt.expected) {
				t.Errorf("PrefixSuccessor(%x) = %x, want %x", tt.prefix, result, tt.expected)
			}
		})
	}
}

func TestKeyRangeScan(t *testing.T) {
	// Test that we can properly create range bounds
	namespace := "test"

	// Vector range for all versions of ID 1
	prefix := EncodeVectorKeyPrefix(namespace, 1)
	upperBound := PrefixSuccessor(prefix)

	key1 := EncodeVectorKey(namespace, 1, 1)
	key2 := EncodeVectorKey(namespace, 1, 2)
	key3 := EncodeVectorKey(namespace, 1, 100)
	keyDifferentID := EncodeVectorKey(namespace, 2, 1)

	// Keys with matching ID should be in range
	if !bytes.HasPrefix(key1, prefix) {
		t.Error("key1 should be in range")
	}
	if !bytes.HasPrefix(key2, prefix) {
		t.Error("key2 should be in range")
	}
	if !bytes.HasPrefix(key3, prefix) {
		t.Error("key3 should be in range")
	}

	// Should be less than upper bound
	if bytes.Compare(key1, upperBound) >= 0 {
		t.Error("key1 should be less than upper bound")
	}
	if bytes.Compare(key2, upperBound) >= 0 {
		t.Error("key2 should be less than upper bound")
	}

	// Different ID should be outside range if we have upper bound
	if upperBound != nil && bytes.HasPrefix(keyDifferentID, prefix) {
		t.Error("different ID should not match prefix")
	}
}

func TestMetadataIndexRangeQuery(t *testing.T) {
	// Test metadata filtering with prefix
	namespace := "test"
	field := "category"
	value := "tech"

	prefix := EncodeMetadataKeyPrefix(namespace, field, value)

	// Create several keys that should match
	key1 := EncodeMetadataKey(namespace, field, value, 1, 1)
	key2 := EncodeMetadataKey(namespace, field, value, 2, 1)
	key3 := EncodeMetadataKey(namespace, field, value, 100, 1)

	// And one that shouldn't
	keyDifferentValue := EncodeMetadataKey(namespace, field, "science", 1, 1)
	keyDifferentField := EncodeMetadataKey(namespace, "author", value, 1, 1)

	// Verify matching keys
	if !bytes.HasPrefix(key1, prefix) {
		t.Error("key1 should match prefix")
	}
	if !bytes.HasPrefix(key2, prefix) {
		t.Error("key2 should match prefix")
	}
	if !bytes.HasPrefix(key3, prefix) {
		t.Error("key3 should match prefix")
	}

	// Verify non-matching keys
	if bytes.HasPrefix(keyDifferentValue, prefix) {
		t.Error("different value should not match prefix")
	}
	if bytes.HasPrefix(keyDifferentField, prefix) {
		t.Error("different field should not match prefix")
	}
}

func BenchmarkEncodeVectorKeyPrefix(b *testing.B) {
	namespace := "default"
	id := uint64(12345)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EncodeVectorKeyPrefix(namespace, id)
	}
}

func BenchmarkEncodeReverseKeyPrefix(b *testing.B) {
	namespace := "default"
	id := uint64(12345)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EncodeReverseKeyPrefix(namespace, id)
	}
}

func BenchmarkSplitKey(b *testing.B) {
	key := EncodeMetadataKey("default", "category", "tech", 12345, 67890)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = splitKey(key)
	}
}
