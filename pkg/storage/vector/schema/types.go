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
	"github.com/vmihailenco/msgpack/v5"
)

// VectorValue represents a stored vector with metadata.
type VectorValue struct {
	ID        uint64            `msgpack:"id"`
	Embedding []float32         `msgpack:"emb"`
	Metadata  map[string]string `msgpack:"meta"`
	CreatedAt int64             `msgpack:"created"`
	UpdatedAt int64             `msgpack:"updated"`
	Version   uint64            `msgpack:"ver"`
}

// NamespaceInfo contains metadata about a namespace.
type NamespaceInfo struct {
	Name        string `msgpack:"name"`
	VectorCount uint64 `msgpack:"count"`
	Dimension   uint32 `msgpack:"dim"`
	CreatedAt   int64  `msgpack:"created"`
	UpdatedAt   int64  `msgpack:"updated"`

	// Index configuration
	DistanceMetric string            `msgpack:"distance_metric,omitempty"`
	IndexType      string            `msgpack:"index_type,omitempty"`
	IndexConfig    map[string]string `msgpack:"index_config,omitempty"`
	Metadata       map[string]string `msgpack:"metadata,omitempty"`

	// Multi-tenant fields
	TenantID     string            `msgpack:"tenant_id,omitempty"`
	OwnerUserID  string            `msgpack:"owner_user_id,omitempty"`
	Labels       map[string]string `msgpack:"labels,omitempty"`
	StorageBytes int64             `msgpack:"storage_bytes,omitempty"`
}

// EncodeVectorValue serializes a VectorValue using MessagePack.
func EncodeVectorValue(v *VectorValue) ([]byte, error) {
	return msgpack.Marshal(v)
}

// DecodeVectorValue deserializes a VectorValue from MessagePack.
func DecodeVectorValue(data []byte) (*VectorValue, error) {
	var v VectorValue
	err := msgpack.Unmarshal(data, &v)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// EncodeNamespaceInfo serializes namespace metadata using MessagePack.
func EncodeNamespaceInfo(info *NamespaceInfo) ([]byte, error) {
	return msgpack.Marshal(info)
}

// DecodeNamespaceInfo deserializes namespace metadata from MessagePack.
func DecodeNamespaceInfo(data []byte) (*NamespaceInfo, error) {
	var info NamespaceInfo
	err := msgpack.Unmarshal(data, &info)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// EstimateVectorSize estimates the storage size of a vector in bytes.
func EstimateVectorSize(v *VectorValue) int {
	size := 8 + 8 + 8 // ID + CreatedAt + UpdatedAt + Version

	// Embedding size (4 bytes per float32)
	size += len(v.Embedding) * 4

	// Metadata size
	for k, val := range v.Metadata {
		size += len(k) + len(val) + 16 // key + value + overhead
	}

	// MessagePack overhead (~10%)
	return int(float64(size) * 1.1)
}
