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

// Package schema provides key encoding and decoding for vector storage.
package schema

import (
	"encoding/binary"
	"fmt"
	"strings"

	api "github.com/jeremyhahn/go-qrdb/pkg/api"
)

// Key prefixes (1 byte each).
const (
	PrefixVector    = 'v' // 0x76 - Vector data (primary)
	PrefixMetadata  = 'm' // 0x6D - Metadata index (secondary)
	PrefixNamespace = 'n' // 0x6E - Namespace catalog
	PrefixIDSeq     = 'i' // 0x69 - ID allocator (sequence)
	PrefixStats     = 's' // 0x73 - Statistics (counters)
	PrefixTombstone = 't' // 0x74 - Tombstone markers
	PrefixReverse   = 'r' // 0x72 - Reverse index (ID→metadata)

	// Separator between key components.
	Separator = '\x00'
)

// EncodeVectorKey creates a primary vector key format.
// Format: v\x00{namespace}\x00{id:uint64}\x00{version:uint64}.
func EncodeVectorKey(namespace string, id, version uint64) []byte {
	// Pre-allocate buffer for efficiency
	// 1 (prefix) + 1 (sep) + len(ns) + 1 (sep) + 8 (id) + 1 (sep) + 8 (version)
	capacity := 1 + 1 + len(namespace) + 1 + 8 + 1 + 8
	key := make([]byte, 0, capacity)

	key = append(key, PrefixVector, Separator)
	key = append(key, []byte(namespace)...)
	key = append(key, Separator)
	key = appendUint64(key, id)
	key = append(key, Separator)
	key = appendUint64(key, version)

	return key
}

// EncodeVectorKeyPrefix creates a prefix for scanning all versions of a vector.
// Format: v\x00{namespace}\x00{id:uint64}\x00.
func EncodeVectorKeyPrefix(namespace string, id uint64) []byte {
	capacity := 1 + 1 + len(namespace) + 1 + 8 + 1
	key := make([]byte, 0, capacity)

	key = append(key, PrefixVector, Separator)
	key = append(key, []byte(namespace)...)
	key = append(key, Separator)
	key = appendUint64(key, id)
	key = append(key, Separator)

	return key
}

// EncodeNamespacePrefix creates a prefix for scanning all vectors in a namespace.
// Format: v\x00{namespace}\x00.
func EncodeNamespacePrefix(namespace string) []byte {
	capacity := 1 + 1 + len(namespace) + 1
	key := make([]byte, 0, capacity)

	key = append(key, PrefixVector, Separator)
	key = append(key, []byte(namespace)...)
	key = append(key, Separator)

	return key
}

// DecodeVectorKey extracts namespace, ID, and version from a vector key.
func DecodeVectorKey(key []byte) (namespace string, id, version uint64, err error) {
	if len(key) < 3 || key[0] != PrefixVector || key[1] != Separator {
		return "", 0, 0, api.NewInvalidKeyEncodingError("schema.DecodeVectorKey", "vector", "bad prefix")
	}

	// Find namespace end (first separator after prefix)
	pos := 2
	nsStart := pos
	for pos < len(key) && key[pos] != Separator {
		pos++
	}
	if pos >= len(key) {
		return "", 0, 0, api.NewInvalidKeyEncodingError("schema.DecodeVectorKey", "vector", "missing namespace separator")
	}
	namespace = string(key[nsStart:pos])
	pos++ // Skip separator

	// Read ID (8 bytes)
	if pos+8 > len(key) {
		return "", 0, 0, api.NewInvalidKeyEncodingError("schema.DecodeVectorKey", "vector", "insufficient bytes for ID")
	}
	id = binary.BigEndian.Uint64(key[pos : pos+8])
	pos += 8

	// Skip separator
	if pos >= len(key) || key[pos] != Separator {
		return "", 0, 0, api.NewInvalidKeyEncodingError("schema.DecodeVectorKey", "vector", "missing ID separator")
	}
	pos++

	// Read version (8 bytes)
	if pos+8 > len(key) {
		return "", 0, 0, api.NewInvalidKeyEncodingError("schema.DecodeVectorKey", "vector", "insufficient bytes for version")
	}
	version = binary.BigEndian.Uint64(key[pos : pos+8])

	return namespace, id, version, nil
}

// EncodeMetadataKey creates a secondary index key.
// Format: m\x00{namespace}\x00{field}\x00{value}\x00{id:uint64}\x00{version:uint64}.
func EncodeMetadataKey(namespace, field, value string, id, version uint64) []byte {
	capacity := 1 + 1 + len(namespace) + 1 + len(field) + 1 + len(value) + 1 + 8 + 1 + 8
	key := make([]byte, 0, capacity)

	key = append(key, PrefixMetadata, Separator)
	key = append(key, []byte(namespace)...)
	key = append(key, Separator)
	key = append(key, []byte(field)...)
	key = append(key, Separator)
	key = append(key, []byte(value)...)
	key = append(key, Separator)
	key = appendUint64(key, id)
	key = append(key, Separator)
	key = appendUint64(key, version)

	return key
}

// EncodeMetadataKeyPrefix creates a prefix for scanning metadata index.
// Format: m\x00{namespace}\x00{field}\x00{value}\x00.
func EncodeMetadataKeyPrefix(namespace, field, value string) []byte {
	capacity := 1 + 1 + len(namespace) + 1 + len(field) + 1 + len(value) + 1
	key := make([]byte, 0, capacity)

	key = append(key, PrefixMetadata, Separator)
	key = append(key, []byte(namespace)...)
	key = append(key, Separator)
	key = append(key, []byte(field)...)
	key = append(key, Separator)
	key = append(key, []byte(value)...)
	key = append(key, Separator)

	return key
}

// DecodeMetadataKey extracts components from a metadata index key.
func DecodeMetadataKey(key []byte) (namespace, field, value string, id, version uint64, err error) {
	if len(key) < 3 || key[0] != PrefixMetadata || key[1] != Separator {
		return "", "", "", 0, 0, api.NewInvalidKeyEncodingError("schema.DecodeMetadataKey", "metadata", "bad prefix")
	}

	pos := 2

	// Read namespace
	nsStart := pos
	for pos < len(key) && key[pos] != Separator {
		pos++
	}
	if pos >= len(key) {
		return "", "", "", 0, 0, api.NewInvalidKeyEncodingError("schema.DecodeMetadataKey", "metadata", "missing namespace separator")
	}
	namespace = string(key[nsStart:pos])
	pos++

	// Read field
	fieldStart := pos
	for pos < len(key) && key[pos] != Separator {
		pos++
	}
	if pos >= len(key) {
		return "", "", "", 0, 0, api.NewInvalidKeyEncodingError("schema.DecodeMetadataKey", "metadata", "missing field separator")
	}
	field = string(key[fieldStart:pos])
	pos++

	// Read value
	valueStart := pos
	for pos < len(key) && key[pos] != Separator {
		pos++
	}
	if pos >= len(key) {
		return "", "", "", 0, 0, api.NewInvalidKeyEncodingError("schema.DecodeMetadataKey", "metadata", "missing value separator")
	}
	value = string(key[valueStart:pos])
	pos++

	// Read ID (8 bytes)
	if pos+8 > len(key) {
		return "", "", "", 0, 0, api.NewInvalidKeyEncodingError("schema.DecodeMetadataKey", "metadata", "insufficient bytes for ID")
	}
	id = binary.BigEndian.Uint64(key[pos : pos+8])
	pos += 8

	// Skip separator
	if pos >= len(key) || key[pos] != Separator {
		return "", "", "", 0, 0, api.NewInvalidKeyEncodingError("schema.DecodeMetadataKey", "metadata", "missing ID separator")
	}
	pos++

	// Read version (8 bytes)
	if pos+8 > len(key) {
		return "", "", "", 0, 0, api.NewInvalidKeyEncodingError("schema.DecodeMetadataKey", "metadata", "insufficient bytes for version")
	}
	version = binary.BigEndian.Uint64(key[pos : pos+8])

	return namespace, field, value, id, version, nil
}

// EncodeReverseKey creates a reverse index key.
// Format: r\x00{namespace}\x00{id:uint64}\x00{field}\x00{version:uint64}.
func EncodeReverseKey(namespace string, id uint64, field string, version uint64) []byte {
	capacity := 1 + 1 + len(namespace) + 1 + 8 + 1 + len(field) + 1 + 8
	key := make([]byte, 0, capacity)

	key = append(key, PrefixReverse, Separator)
	key = append(key, []byte(namespace)...)
	key = append(key, Separator)
	key = appendUint64(key, id)
	key = append(key, Separator)
	key = append(key, []byte(field)...)
	key = append(key, Separator)
	key = appendUint64(key, version)

	return key
}

// EncodeReverseKeyPrefix creates a prefix for scanning all metadata fields of a vector.
// Format: r\x00{namespace}\x00{id:uint64}\x00.
func EncodeReverseKeyPrefix(namespace string, id uint64) []byte {
	capacity := 1 + 1 + len(namespace) + 1 + 8 + 1
	key := make([]byte, 0, capacity)

	key = append(key, PrefixReverse, Separator)
	key = append(key, []byte(namespace)...)
	key = append(key, Separator)
	key = appendUint64(key, id)
	key = append(key, Separator)

	return key
}

// DecodeReverseKey extracts components from a reverse index key.
func DecodeReverseKey(key []byte) (namespace string, id uint64, field string, version uint64, err error) {
	if len(key) < 3 || key[0] != PrefixReverse || key[1] != Separator {
		return "", 0, "", 0, api.NewInvalidKeyEncodingError("schema.DecodeReverseKey", "reverse", "bad prefix")
	}

	pos := 2

	// Read namespace
	nsStart := pos
	for pos < len(key) && key[pos] != Separator {
		pos++
	}
	if pos >= len(key) {
		return "", 0, "", 0, api.NewInvalidKeyEncodingError("schema.DecodeReverseKey", "reverse", "missing namespace separator")
	}
	namespace = string(key[nsStart:pos])
	pos++

	// Read ID (8 bytes)
	if pos+8 > len(key) {
		return "", 0, "", 0, api.NewInvalidKeyEncodingError("schema.DecodeReverseKey", "reverse", "insufficient bytes for ID")
	}
	id = binary.BigEndian.Uint64(key[pos : pos+8])
	pos += 8

	// Skip separator
	if pos >= len(key) || key[pos] != Separator {
		return "", 0, "", 0, api.NewInvalidKeyEncodingError("schema.DecodeReverseKey", "reverse", "missing ID separator")
	}
	pos++

	// Read field
	fieldStart := pos
	for pos < len(key) && key[pos] != Separator {
		pos++
	}
	if pos >= len(key) {
		return "", 0, "", 0, api.NewInvalidKeyEncodingError("schema.DecodeReverseKey", "reverse", "missing field separator")
	}
	field = string(key[fieldStart:pos])
	pos++

	// Read version (8 bytes)
	if pos+8 > len(key) {
		return "", 0, "", 0, api.NewInvalidKeyEncodingError("schema.DecodeReverseKey", "reverse", "insufficient bytes for version")
	}
	version = binary.BigEndian.Uint64(key[pos : pos+8])

	return namespace, id, field, version, nil
}

// EncodeNamespaceMetaKey creates a namespace metadata key.
// Format: n\x00{namespace}\x00meta.
func EncodeNamespaceMetaKey(namespace string) []byte {
	capacity := 1 + 1 + len(namespace) + 1 + 4
	key := make([]byte, 0, capacity)

	key = append(key, PrefixNamespace, Separator)
	key = append(key, []byte(namespace)...)
	key = append(key, Separator)
	key = append(key, []byte("meta")...)

	return key
}

// EncodeNamespacesPrefix creates a prefix for listing all namespaces.
// Format: n\x00.
func EncodeNamespacesPrefix() []byte {
	return []byte{PrefixNamespace, Separator}
}

// EncodeIDSequenceKey creates an ID sequence key.
// Format: i\x00{namespace}\x00seq.
func EncodeIDSequenceKey(namespace string) []byte {
	capacity := 1 + 1 + len(namespace) + 1 + 3
	key := make([]byte, 0, capacity)

	key = append(key, PrefixIDSeq, Separator)
	key = append(key, []byte(namespace)...)
	key = append(key, Separator)
	key = append(key, []byte("seq")...)

	return key
}

// EncodeStatsKey creates a statistics key.
// Format: s\x00{namespace}\x00{metric}.
func EncodeStatsKey(namespace, metric string) []byte {
	capacity := 1 + 1 + len(namespace) + 1 + len(metric)
	key := make([]byte, 0, capacity)

	key = append(key, PrefixStats, Separator)
	key = append(key, []byte(namespace)...)
	key = append(key, Separator)
	key = append(key, []byte(metric)...)

	return key
}

// EncodeTombstoneKey creates a tombstone key.
// Format: t\x00{namespace}\x00{id:uint64}\x00{timestamp:uint64}.
func EncodeTombstoneKey(namespace string, id, timestamp uint64) []byte {
	capacity := 1 + 1 + len(namespace) + 1 + 8 + 1 + 8
	key := make([]byte, 0, capacity)

	key = append(key, PrefixTombstone, Separator)
	key = append(key, []byte(namespace)...)
	key = append(key, Separator)
	key = appendUint64(key, id)
	key = append(key, Separator)
	key = appendUint64(key, timestamp)

	return key
}

// EncodeTombstonePrefix creates a prefix for scanning tombstones.
// Format: t\x00{namespace}\x00.
func EncodeTombstonePrefix(namespace string) []byte {
	capacity := 1 + 1 + len(namespace) + 1
	key := make([]byte, 0, capacity)

	key = append(key, PrefixTombstone, Separator)
	key = append(key, []byte(namespace)...)
	key = append(key, Separator)

	return key
}

// PrefixSuccessor returns the next key after the given prefix for range scans.
// This is used as the upper bound in iterator options.
func PrefixSuccessor(prefix []byte) []byte {
	if len(prefix) == 0 {
		return nil
	}

	// Create a copy and increment the last byte
	next := make([]byte, len(prefix))
	copy(next, prefix)

	for i := len(next) - 1; i >= 0; i-- {
		if next[i] < 0xFF {
			next[i]++
			return next[:i+1]
		}
	}

	// Overflow - return nil (no upper bound)
	return nil
}

// Helper: Append uint64 as big-endian bytes (for proper sorting).
func appendUint64(buf []byte, v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return append(buf, b...)
}

// Helper: Encode uint64 as big-endian bytes.
func encodeUint64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// Helper: Decode uint64 from big-endian bytes.
func decodeUint64(b []byte) (uint64, error) {
	if len(b) != 8 {
		return 0, api.NewInvalidUint64EncodingError("schema.decodeUint64", 8, len(b))
	}
	return binary.BigEndian.Uint64(b), nil
}

// Helper: Split key by separator.
func splitKey(key []byte) [][]byte {
	var parts [][]byte
	start := 0

	for i, b := range key {
		if b == Separator {
			parts = append(parts, key[start:i])
			start = i + 1
		}
	}

	// Add final part (no trailing separator)
	if start < len(key) {
		parts = append(parts, key[start:])
	}

	return parts
}

// KeyInfo provides human-readable information about a key.
type KeyInfo struct {
	Type      string
	Namespace string
	ID        uint64
	Version   uint64
	Field     string
	Value     string
	Metric    string
}

// ParseKey attempts to parse any key and return its information.
func ParseKey(key []byte) (*KeyInfo, error) {
	if len(key) < 2 {
		return nil, api.NewInvalidKeyEncodingError("schema.ParseKey", "unknown", "key too short")
	}

	info := &KeyInfo{}

	switch key[0] {
	case PrefixVector:
		info.Type = "vector"
		ns, id, ver, err := DecodeVectorKey(key)
		if err != nil {
			return nil, api.NewDecodeError("schema.ParseKey", "vector key", err)
		}
		info.Namespace = ns
		info.ID = id
		info.Version = ver

	case PrefixMetadata:
		info.Type = "metadata"
		ns, field, value, id, ver, err := DecodeMetadataKey(key)
		if err != nil {
			return nil, api.NewDecodeError("schema.ParseKey", "metadata key", err)
		}
		info.Namespace = ns
		info.Field = field
		info.Value = value
		info.ID = id
		info.Version = ver

	case PrefixReverse:
		info.Type = "reverse"
		ns, id, field, ver, err := DecodeReverseKey(key)
		if err != nil {
			return nil, api.NewDecodeError("schema.ParseKey", "reverse key", err)
		}
		info.Namespace = ns
		info.ID = id
		info.Field = field
		info.Version = ver

	case PrefixNamespace:
		info.Type = "namespace"
		parts := splitKey(key[2:])
		if len(parts) >= 1 {
			info.Namespace = string(parts[0])
		}

	case PrefixIDSeq:
		info.Type = "id_sequence"
		parts := splitKey(key[2:])
		if len(parts) >= 1 {
			info.Namespace = string(parts[0])
		}

	case PrefixStats:
		info.Type = "stats"
		parts := splitKey(key[2:])
		if len(parts) >= 2 {
			info.Namespace = string(parts[0])
			info.Metric = string(parts[1])
		}

	case PrefixTombstone:
		info.Type = "tombstone"
		parts := splitKey(key[2:])
		if len(parts) >= 2 {
			info.Namespace = string(parts[0])
			if id, err := decodeUint64(parts[1]); err == nil {
				info.ID = id
			}
		}

	default:
		return nil, api.NewUnknownKeyPrefixError("schema.ParseKey", key[0])
	}

	return info, nil
}

// String returns a human-readable representation of the key.
func (k *KeyInfo) String() string {
	var parts []string

	parts = append(parts, fmt.Sprintf("Type=%s", k.Type))

	if k.Namespace != "" {
		parts = append(parts, fmt.Sprintf("Namespace=%s", k.Namespace))
	}
	if k.ID != 0 {
		parts = append(parts, fmt.Sprintf("ID=%d", k.ID))
	}
	if k.Version != 0 {
		parts = append(parts, fmt.Sprintf("Version=%d", k.Version))
	}
	if k.Field != "" {
		parts = append(parts, fmt.Sprintf("Field=%s", k.Field))
	}
	if k.Value != "" {
		parts = append(parts, fmt.Sprintf("Value=%s", k.Value))
	}
	if k.Metric != "" {
		parts = append(parts, fmt.Sprintf("Metric=%s", k.Metric))
	}

	return strings.Join(parts, ", ")
}
