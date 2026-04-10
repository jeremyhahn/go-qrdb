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
	"bytes"
	"testing"
)

func TestEncodeDecodeVectorKey(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		id        uint64
		version   uint64
	}{
		{"simple", "default", 1, 1},
		{"large_id", "default", 1<<32 - 1, 1},
		{"large_version", "default", 1, 1<<32 - 1},
		{"max_values", "test", ^uint64(0), ^uint64(0)},
		{"long_namespace", "very-long-namespace-name-for-testing", 42, 7},
		{"special_chars", "test-ns_123", 100, 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := EncodeVectorKey(tt.namespace, tt.id, tt.version)

			// Verify key structure
			if key[0] != PrefixVector {
				t.Errorf("expected prefix %c, got %c", PrefixVector, key[0])
			}
			if key[1] != Separator {
				t.Errorf("expected separator at position 1")
			}

			// Decode and verify
			ns, id, ver, err := DecodeVectorKey(key)
			if err != nil {
				t.Fatalf("DecodeVectorKey failed: %v", err)
			}

			if ns != tt.namespace {
				t.Errorf("namespace mismatch: got %q, want %q", ns, tt.namespace)
			}
			if id != tt.id {
				t.Errorf("id mismatch: got %d, want %d", id, tt.id)
			}
			if ver != tt.version {
				t.Errorf("version mismatch: got %d, want %d", ver, tt.version)
			}
		})
	}
}

func TestDecodeVectorKeyErrors(t *testing.T) {
	tests := []struct {
		name string
		key  []byte
	}{
		{"empty", []byte{}},
		{"too_short", []byte{PrefixVector}},
		{"wrong_prefix", []byte{PrefixMetadata, Separator, 'd', 'e', 'f'}},
		{"missing_separator", []byte{PrefixVector, 'd', 'e', 'f'}},
		{"incomplete", []byte{PrefixVector, Separator, 'd', 'e', 'f'}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := DecodeVectorKey(tt.key)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestEncodeDecodeMetadataKey(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		field     string
		value     string
		id        uint64
		version   uint64
	}{
		{"simple", "default", "category", "tech", 1, 1},
		{"empty_value", "default", "tag", "", 2, 3},
		{"special_chars", "ns-1", "author", "John Doe", 100, 200},
		{"long_value", "default", "description", "This is a very long description that contains multiple words and special characters!@#$%", 42, 7},
		{"unicode", "default", "name", "名前", 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := EncodeMetadataKey(tt.namespace, tt.field, tt.value, tt.id, tt.version)

			// Verify key structure
			if key[0] != PrefixMetadata {
				t.Errorf("expected prefix %c, got %c", PrefixMetadata, key[0])
			}

			// Decode and verify
			ns, field, value, id, ver, err := DecodeMetadataKey(key)
			if err != nil {
				t.Fatalf("DecodeMetadataKey failed: %v", err)
			}

			if ns != tt.namespace {
				t.Errorf("namespace mismatch: got %q, want %q", ns, tt.namespace)
			}
			if field != tt.field {
				t.Errorf("field mismatch: got %q, want %q", field, tt.field)
			}
			if value != tt.value {
				t.Errorf("value mismatch: got %q, want %q", value, tt.value)
			}
			if id != tt.id {
				t.Errorf("id mismatch: got %d, want %d", id, tt.id)
			}
			if ver != tt.version {
				t.Errorf("version mismatch: got %d, want %d", ver, tt.version)
			}
		})
	}
}

func TestEncodeDecodeReverseKey(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		id        uint64
		field     string
		version   uint64
	}{
		{"simple", "default", 1, "category", 1},
		{"multiple_fields", "test", 42, "author", 7},
		{"long_field", "ns", 100, "very-long-field-name-for-testing", 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := EncodeReverseKey(tt.namespace, tt.id, tt.field, tt.version)

			// Verify key structure
			if key[0] != PrefixReverse {
				t.Errorf("expected prefix %c, got %c", PrefixReverse, key[0])
			}

			// Decode and verify
			ns, id, field, ver, err := DecodeReverseKey(key)
			if err != nil {
				t.Fatalf("DecodeReverseKey failed: %v", err)
			}

			if ns != tt.namespace {
				t.Errorf("namespace mismatch: got %q, want %q", ns, tt.namespace)
			}
			if id != tt.id {
				t.Errorf("id mismatch: got %d, want %d", id, tt.id)
			}
			if field != tt.field {
				t.Errorf("field mismatch: got %q, want %q", field, tt.field)
			}
			if ver != tt.version {
				t.Errorf("version mismatch: got %d, want %d", ver, tt.version)
			}
		})
	}
}

func TestPrefixSuccessor(t *testing.T) {
	tests := []struct {
		name     string
		prefix   []byte
		expected []byte
	}{
		{"simple", []byte{0x01}, []byte{0x02}},
		{"multi_byte", []byte{0x01, 0x02}, []byte{0x01, 0x03}},
		{"rollover", []byte{0x01, 0xFF}, []byte{0x02}},
		{"all_ff", []byte{0xFF, 0xFF}, nil},
		{"empty", []byte{}, nil},
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

func TestKeySorting(t *testing.T) {
	// Test that keys sort correctly for range scans
	keys := [][]byte{
		EncodeVectorKey("default", 1, 1),
		EncodeVectorKey("default", 1, 2),
		EncodeVectorKey("default", 2, 1),
		EncodeVectorKey("default", 10, 1),
		EncodeVectorKey("default", 100, 1),
	}

	// Verify keys are in ascending order
	for i := 0; i < len(keys)-1; i++ {
		if bytes.Compare(keys[i], keys[i+1]) >= 0 {
			t.Errorf("keys not sorted: %x >= %x", keys[i], keys[i+1])
		}
	}
}

func TestNamespaceIsolation(t *testing.T) {
	// Verify that different namespaces don't overlap
	key1 := EncodeVectorKey("default", 1, 1)
	key2 := EncodeVectorKey("test", 1, 1)

	if bytes.Equal(key1, key2) {
		t.Error("keys from different namespaces should not be equal")
	}

	// Verify prefix scans are isolated
	prefix1 := EncodeNamespacePrefix("default")
	prefix2 := EncodeNamespacePrefix("test")

	if bytes.Equal(prefix1, prefix2) {
		t.Error("namespace prefixes should not be equal")
	}

	// key1 should match prefix1 but not prefix2
	if !bytes.HasPrefix(key1, prefix1) {
		t.Error("key1 should match prefix1")
	}
	if bytes.HasPrefix(key1, prefix2) {
		t.Error("key1 should not match prefix2")
	}
}

func TestMetadataIndexScan(t *testing.T) {
	// Verify metadata index keys sort correctly for filtering
	keys := [][]byte{
		EncodeMetadataKey("default", "category", "tech", 1, 1),
		EncodeMetadataKey("default", "category", "tech", 2, 1),
		EncodeMetadataKey("default", "category", "tech", 10, 1),
		EncodeMetadataKey("default", "category", "science", 1, 1),
	}

	// Verify proper prefix matching
	prefix := EncodeMetadataKeyPrefix("default", "category", "tech")

	matchCount := 0
	for _, key := range keys {
		if bytes.HasPrefix(key, prefix) {
			matchCount++
		}
	}

	if matchCount != 3 {
		t.Errorf("expected 3 keys to match prefix, got %d", matchCount)
	}
}

func TestEncodeNamespaceMetaKey(t *testing.T) {
	key := EncodeNamespaceMetaKey("default")

	if key[0] != PrefixNamespace {
		t.Errorf("expected prefix %c, got %c", PrefixNamespace, key[0])
	}

	if !bytes.Contains(key, []byte("default")) {
		t.Error("key should contain namespace name")
	}

	if !bytes.Contains(key, []byte("meta")) {
		t.Error("key should contain 'meta' suffix")
	}
}

func TestEncodeIDSequenceKey(t *testing.T) {
	key := EncodeIDSequenceKey("default")

	if key[0] != PrefixIDSeq {
		t.Errorf("expected prefix %c, got %c", PrefixIDSeq, key[0])
	}

	if !bytes.Contains(key, []byte("default")) {
		t.Error("key should contain namespace name")
	}

	if !bytes.Contains(key, []byte("seq")) {
		t.Error("key should contain 'seq' suffix")
	}
}

func TestEncodeStatsKey(t *testing.T) {
	key := EncodeStatsKey("default", "count")

	if key[0] != PrefixStats {
		t.Errorf("expected prefix %c, got %c", PrefixStats, key[0])
	}

	if !bytes.Contains(key, []byte("default")) {
		t.Error("key should contain namespace name")
	}

	if !bytes.Contains(key, []byte("count")) {
		t.Error("key should contain metric name")
	}
}

func TestEncodeTombstoneKey(t *testing.T) {
	timestamp := uint64(1234567890)
	key := EncodeTombstoneKey("default", 42, timestamp)

	if key[0] != PrefixTombstone {
		t.Errorf("expected prefix %c, got %c", PrefixTombstone, key[0])
	}

	if !bytes.Contains(key, []byte("default")) {
		t.Error("key should contain namespace name")
	}
}

func TestParseKey(t *testing.T) {
	tests := []struct {
		name     string
		key      []byte
		wantType string
		wantErr  bool
	}{
		{
			name:     "vector_key",
			key:      EncodeVectorKey("default", 1, 1),
			wantType: "vector",
		},
		{
			name:     "metadata_key",
			key:      EncodeMetadataKey("default", "category", "tech", 1, 1),
			wantType: "metadata",
		},
		{
			name:     "reverse_key",
			key:      EncodeReverseKey("default", 1, "category", 1),
			wantType: "reverse",
		},
		{
			name:     "namespace_key",
			key:      EncodeNamespaceMetaKey("default"),
			wantType: "namespace",
		},
		{
			name:     "stats_key",
			key:      EncodeStatsKey("default", "count"),
			wantType: "stats",
		},
		{
			name:     "tombstone_key",
			key:      EncodeTombstoneKey("default", 1, 123),
			wantType: "tombstone",
		},
		{
			name:    "invalid_key",
			key:     []byte{0xFF, 0x00},
			wantErr: true,
		},
		{
			name:    "empty_key",
			key:     []byte{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := ParseKey(tt.key)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("ParseKey failed: %v", err)
			}

			if info.Type != tt.wantType {
				t.Errorf("type mismatch: got %q, want %q", info.Type, tt.wantType)
			}

			// Verify String() doesn't panic
			_ = info.String()
		})
	}
}

func TestKeyInfoString(t *testing.T) {
	info := &KeyInfo{
		Type:      "vector",
		Namespace: "default",
		ID:        42,
		Version:   7,
		Field:     "category",
		Value:     "tech",
		Metric:    "count",
	}

	s := info.String()

	// Should contain all non-zero fields
	for _, expect := range []string{"vector", "default", "42", "7", "category", "tech", "count"} {
		if !bytes.Contains([]byte(s), []byte(expect)) {
			t.Errorf("String() should contain %q, got %q", expect, s)
		}
	}
}

func TestUint64Encoding(t *testing.T) {
	tests := []uint64{
		0,
		1,
		255,
		256,
		65535,
		65536,
		1<<32 - 1,
		1 << 32,
		^uint64(0), // max uint64
	}

	for _, val := range tests {
		encoded := encodeUint64(val)
		if len(encoded) != 8 {
			t.Errorf("encoded length should be 8, got %d", len(encoded))
		}

		decoded, err := decodeUint64(encoded)
		if err != nil {
			t.Errorf("decodeUint64 failed: %v", err)
		}

		if decoded != val {
			t.Errorf("roundtrip failed: got %d, want %d", decoded, val)
		}
	}
}

func TestUint64DecodeErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"too_short", []byte{0x01, 0x02}},
		{"too_long", []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeUint64(tt.data)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func BenchmarkEncodeVectorKey(b *testing.B) {
	namespace := "default"
	id := uint64(12345)
	version := uint64(67890)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EncodeVectorKey(namespace, id, version)
	}
}

func BenchmarkDecodeVectorKey(b *testing.B) {
	key := EncodeVectorKey("default", 12345, 67890)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = DecodeVectorKey(key)
	}
}

func BenchmarkEncodeMetadataKey(b *testing.B) {
	namespace := "default"
	field := "category"
	value := "technology"
	id := uint64(12345)
	version := uint64(67890)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EncodeMetadataKey(namespace, field, value, id, version)
	}
}

func BenchmarkParseKey(b *testing.B) {
	key := EncodeVectorKey("default", 12345, 67890)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ParseKey(key)
	}
}
