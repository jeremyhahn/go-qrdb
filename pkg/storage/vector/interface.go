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

// Package vector provides the storage layer for vector embeddings and metadata.
package vector

import (
	"context"
	"maps"
	"time"
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

// NamespaceStats contains statistics for a namespace.
type NamespaceStats struct {
	Namespace    string
	Count        uint64
	SizeBytes    uint64
	TotalInserts uint64
	TotalDeletes uint64
}

// ScanOptions configures vector scanning behavior.
type ScanOptions struct {
	Limit      int
	Offset     int
	OrderBy    string
	Descending bool
}

// MetadataFilter represents a metadata query filter.
type MetadataFilter struct {
	Field    string
	Value    string
	Operator FilterOperator
}

// FilterOperator defines metadata filter operations.
type FilterOperator int

const (
	FilterEqual FilterOperator = iota
	FilterNotEqual
	FilterIn
	FilterNotIn
)

// NamespaceUpdate carries the fields of a namespace that an in-place update
// replaces. Each map replaces the stored one whole: the caller computes any
// merge, so storage never has to decide what an absent key means.
type NamespaceUpdate struct {
	// IndexConfig replaces the namespace's index parameters.
	IndexConfig map[string]string

	// Metadata replaces the namespace's metadata.
	Metadata map[string]string

	// UpdatedAt is the update timestamp in Unix nanoseconds.
	UpdatedAt int64
}

// MergeNamespaceUpdate builds the update that sets each key of indexConfig and
// metadata over info's stored values, leaving every key they do not name as
// it is. requiresRebuild reports that an index parameter was added or
// changed, so an index built under the old parameters no longer matches.
func MergeNamespaceUpdate(info *NamespaceInfo, indexConfig, metadata map[string]string, updatedAt int64) (update *NamespaceUpdate, requiresRebuild bool) {
	mergedIndex := make(map[string]string, len(info.IndexConfig)+len(indexConfig))
	maps.Copy(mergedIndex, info.IndexConfig)
	for k, val := range indexConfig {
		if current, ok := mergedIndex[k]; !ok || current != val {
			requiresRebuild = true
		}
		mergedIndex[k] = val
	}

	mergedMeta := make(map[string]string, len(info.Metadata)+len(metadata))
	maps.Copy(mergedMeta, info.Metadata)
	maps.Copy(mergedMeta, metadata)

	return &NamespaceUpdate{
		IndexConfig: mergedIndex,
		Metadata:    mergedMeta,
		UpdatedAt:   updatedAt,
	}, requiresRebuild
}

// Storage is the main interface for vector storage operations.
type Storage interface {
	// Vector Operations
	UpsertVector(ctx context.Context, namespace string, vec *VectorValue) error
	GetVector(ctx context.Context, namespace string, id uint64) (*VectorValue, error)
	DeleteVector(ctx context.Context, namespace string, id uint64) error
	BatchUpsertVectors(ctx context.Context, namespace string, vecs []*VectorValue) error
	BatchDeleteVectors(ctx context.Context, namespace string, ids []uint64) error

	// Query Operations
	ScanByMetadata(ctx context.Context, namespace, field, value string, opts *ScanOptions) ([]*VectorValue, error)
	ScanByMultipleFilters(ctx context.Context, namespace string, filters []MetadataFilter, opts *ScanOptions) ([]*VectorValue, error)
	ScanNamespace(ctx context.Context, namespace string, opts *ScanOptions) ([]*VectorValue, error)

	// Namespace Operations
	CreateNamespace(ctx context.Context, info *NamespaceInfo) error
	// UpdateNamespace replaces a namespace's mutable fields in place and
	// leaves its vectors, dimension, metric and index type untouched. It
	// returns ErrNamespaceNotFound when the namespace does not exist.
	UpdateNamespace(ctx context.Context, namespace string, update *NamespaceUpdate) error
	DeleteNamespace(ctx context.Context, namespace string) error
	ListNamespaces(ctx context.Context) ([]NamespaceInfo, error)
	GetNamespaceInfo(ctx context.Context, namespace string) (*NamespaceInfo, error)

	// Statistics
	GetStats(ctx context.Context, namespace string) (*NamespaceStats, error)

	// ID Allocation
	NextID(namespace string) (uint64, error)
	BatchAllocateIDs(namespace string, count int) ([]uint64, error)

	// Maintenance
	Compact(ctx context.Context, namespace string) error
	CleanupTombstones(ctx context.Context, olderThan time.Duration) error

	// Lifecycle
	Close() error
}

// Snapshot represents a point-in-time view of the storage.
type Snapshot interface {
	// Get retrieves a vector from the snapshot
	Get(ctx context.Context, namespace string, id uint64) (*VectorValue, error)

	// Scan performs a range scan in the snapshot
	Scan(ctx context.Context, namespace string, opts *ScanOptions) ([]*VectorValue, error)

	// Release releases the snapshot resources
	Release() error
}

// Iterator provides cursor-based iteration over vectors.
type Iterator interface {
	// Next advances to the next vector
	Next() bool

	// Value returns the current vector
	Value() (*VectorValue, error)

	// Key returns the current key (namespace, id, version)
	Key() (namespace string, id uint64, version uint64)

	// Err returns any iteration error
	Err() error

	// Close releases iterator resources
	Close() error
}

// Batch provides atomic multi-operation commits.
type Batch interface {
	// UpsertVector adds a vector upsert to the batch
	UpsertVector(namespace string, vec *VectorValue) error

	// DeleteVector adds a vector deletion to the batch
	DeleteVector(namespace string, id uint64) error

	// Commit atomically commits all operations
	Commit() error

	// Reset clears all pending operations
	Reset()

	// Close releases batch resources
	Close() error

	// Count returns the number of pending operations
	Count() int
}

// Config is defined in config.go to avoid duplication

// DefaultConfig is defined in config.go to avoid duplication

// Metrics contains storage performance metrics.
type Metrics struct {
	// Operation counts
	GetCount         uint64
	UpsertCount      uint64
	DeleteCount      uint64
	ScanCount        uint64
	BatchCommitCount uint64

	// Latency histograms (p50, p95, p99 in microseconds)
	GetLatencyP50 uint64
	GetLatencyP95 uint64
	GetLatencyP99 uint64

	UpsertLatencyP50 uint64
	UpsertLatencyP95 uint64
	UpsertLatencyP99 uint64

	// Storage stats
	DiskUsageBytes    uint64
	MemTableBytes     uint64
	BlockCacheHitRate float64

	// Compaction stats
	CompactionCount   uint64
	CompactionSeconds uint64

	// Errors
	ErrorCount uint64
}

// MetricsCollector provides access to storage metrics.
type MetricsCollector interface {
	// GetMetrics returns current metrics snapshot
	GetMetrics() *Metrics

	// ResetMetrics resets all counters
	ResetMetrics()
}
