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

package statemachine

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/api"
)

// VectorOp represents the type of vector operation.
type VectorOp uint8

const (
	OpUpsertVector VectorOp = iota + 1
	OpDeleteVector
	OpBatchUpsert
	OpCreateNamespace
	OpDeleteNamespace
	OpUpdateMetadata
	OpSearchSimilar
	OpGetVector
	OpSearchByID
	OpListVectors
	OpGetNamespaceInfo
	OpListNamespaces
	OpUpdateNamespace
	OpGetStats
	OpRebuildIndex
	OpGetIndexProgress
	OpHealthCheck
)

// String returns the string representation of a VectorOp.
func (op VectorOp) String() string {
	switch op {
	case OpUpsertVector:
		return "UpsertVector"
	case OpDeleteVector:
		return "DeleteVector"
	case OpBatchUpsert:
		return "BatchUpsert"
	case OpCreateNamespace:
		return "CreateNamespace"
	case OpDeleteNamespace:
		return "DeleteNamespace"
	case OpUpdateMetadata:
		return "UpdateMetadata"
	case OpSearchSimilar:
		return "SearchSimilar"
	case OpGetVector:
		return "GetVector"
	case OpSearchByID:
		return "SearchByID"
	case OpListVectors:
		return "ListVectors"
	case OpGetNamespaceInfo:
		return "GetNamespaceInfo"
	case OpListNamespaces:
		return "ListNamespaces"
	case OpUpdateNamespace:
		return "UpdateNamespace"
	case OpGetStats:
		return "GetStats"
	case OpRebuildIndex:
		return "RebuildIndex"
	case OpGetIndexProgress:
		return "GetIndexProgress"
	case OpHealthCheck:
		return "HealthCheck"
	default:
		return fmt.Sprintf("Unknown(%d)", op)
	}
}

// VectorCommand represents a Raft log entry for vector operations.
type VectorCommand struct {
	Op        VectorOp               `msgpack:"op"`
	Namespace string                 `msgpack:"ns"`
	ID        uint64                 `msgpack:"id,omitempty"`
	Vector    []float32              `msgpack:"vec,omitempty"`
	Metadata  map[string]interface{} `msgpack:"meta,omitempty"`
	Merge     bool                   `msgpack:"merge,omitempty"`
	Version   uint64                 `msgpack:"ver,omitempty"`
	Timestamp int64                  `msgpack:"ts"`
	NodeID    string                 `msgpack:"node,omitempty"`

	// Batch operations
	IDs       []uint64                 `msgpack:"ids,omitempty"`
	Vectors   [][]float32              `msgpack:"vecs,omitempty"`
	BatchMeta []map[string]interface{} `msgpack:"batch_meta,omitempty"` // Metadata for each vector in batch

	// Namespace operations
	Dimension         uint32            `msgpack:"dim,omitempty"`
	DistanceMetric    string            `msgpack:"dist_metric,omitempty"`
	IndexType         string            `msgpack:"idx_type,omitempty"`
	IndexParams       map[string]string `msgpack:"idx_params,omitempty"`
	NamespaceMetadata map[string]string `msgpack:"ns_meta,omitempty"`

	// Search operations
	K          int     `msgpack:"k,omitempty"`
	EfSearch   int     `msgpack:"ef,omitempty"`
	MaxResults int     `msgpack:"max,omitempty"`
	MinScore   float32 `msgpack:"min_score,omitempty"`

	// List operations
	Limit  int    `msgpack:"limit,omitempty"`
	Offset int    `msgpack:"offset,omitempty"`
	Cursor string `msgpack:"cursor,omitempty"`

	// Index rebuild operations
	JobID string `msgpack:"job_id,omitempty"`

	// Multi-tenant fields
	TenantID    string            `msgpack:"tenant_id,omitempty"`
	OwnerUserID string            `msgpack:"owner_user_id,omitempty"`
	Quotas      *NamespaceQuotas  `msgpack:"quotas,omitempty"`
	Labels      map[string]string `msgpack:"labels,omitempty"`
}

// VectorResult represents the result of a vector operation.
type VectorResult struct {
	Success       bool                   `msgpack:"success"`
	Error         string                 `msgpack:"error,omitempty"`
	Version       uint64                 `msgpack:"ver,omitempty"`
	Timestamp     int64                  `msgpack:"ts"`
	Results       []VectorSearchResult   `msgpack:"results,omitempty"`
	Metadata      map[string]interface{} `msgpack:"meta,omitempty"`
	Vector        []float32              `msgpack:"vec,omitempty"`
	VectorID      uint64                 `msgpack:"vec_id,omitempty"`
	Vectors       []VectorData           `msgpack:"vectors,omitempty"`
	NamespaceInfo *NamespaceData         `msgpack:"ns_info,omitempty"`
	Namespaces    []string               `msgpack:"namespaces,omitempty"`
	NextCursor    string                 `msgpack:"next_cursor,omitempty"`
	TotalCount    uint64                 `msgpack:"total,omitempty"`

	// Update namespace result
	Updated         bool `msgpack:"updated,omitempty"`
	RequiresRebuild bool `msgpack:"requires_rebuild,omitempty"`

	// Stats result
	VectorStats *VectorStats `msgpack:"vector_stats,omitempty"`

	// Index progress result
	IndexProgress *IndexProgress `msgpack:"index_progress,omitempty"`

	// Health check result
	HealthStatus *HealthStatus `msgpack:"health_status,omitempty"`
}

// VectorSearchResult represents a single search result.
type VectorSearchResult struct {
	ID       uint64                 `msgpack:"id"`
	Score    float32                `msgpack:"score"`
	Vector   []float32              `msgpack:"vec,omitempty"`
	Metadata map[string]interface{} `msgpack:"meta,omitempty"`
}

// VectorData represents a complete vector with all metadata.
type VectorData struct {
	ID        uint64                 `msgpack:"id"`
	Vector    []float32              `msgpack:"vec"`
	Metadata  map[string]interface{} `msgpack:"meta,omitempty"`
	Version   uint64                 `msgpack:"ver"`
	CreatedAt int64                  `msgpack:"created"`
	UpdatedAt int64                  `msgpack:"updated"`
}

// NamespaceData represents namespace information.
type NamespaceData struct {
	Name        string `msgpack:"name"`
	Dimension   uint32 `msgpack:"dim"`
	VectorCount uint64 `msgpack:"count"`
	CreatedAt   int64  `msgpack:"created"`
	UpdatedAt   int64  `msgpack:"updated"`
}

// VectorStats represents statistics for vectors.
type VectorStats struct {
	VectorCount uint64 `msgpack:"vector_count"`
	Dimension   uint32 `msgpack:"dimension"`
	IndexSize   uint64 `msgpack:"index_size"`
	StorageSize uint64 `msgpack:"storage_size"`
}

// IndexProgress represents index rebuild progress.
type IndexProgress struct {
	InProgress       bool    `msgpack:"in_progress"`
	VectorsProcessed uint64  `msgpack:"vectors_processed"`
	TotalVectors     uint64  `msgpack:"total_vectors"`
	PercentComplete  float64 `msgpack:"percent_complete"`
	JobID            string  `msgpack:"job_id,omitempty"`
}

// HealthStatus represents health check status.
type HealthStatus struct {
	Healthy   bool   `msgpack:"healthy"`
	Message   string `msgpack:"message"`
	LeaderID  uint64 `msgpack:"leader_id"`
	ShardID   uint64 `msgpack:"shard_id"`
	NodeCount int    `msgpack:"node_count"`
}

// UpdateNamespaceResult represents the result of a namespace update.
type UpdateNamespaceResult struct {
	Updated         bool `msgpack:"updated"`
	RequiresRebuild bool `msgpack:"requires_rebuild"`
}

// Encode serializes a VectorCommand to bytes using msgpack.
func (cmd *VectorCommand) Encode() ([]byte, error) {
	data, err := msgpack.Marshal(cmd)
	if err != nil {
		return nil, api.NewEncodeError("VectorCommand.Encode", "vector command", err)
	}
	return data, nil
}

// DecodeVectorCommand deserializes a VectorCommand from bytes.
func DecodeVectorCommand(data []byte) (*VectorCommand, error) {
	var cmd VectorCommand
	if err := msgpack.Unmarshal(data, &cmd); err != nil {
		return nil, api.NewDecodeError("DecodeVectorCommand", "vector command", err)
	}
	return &cmd, nil
}

// Encode serializes a VectorResult to bytes using msgpack.
func (r *VectorResult) Encode() ([]byte, error) {
	data, err := msgpack.Marshal(r)
	if err != nil {
		return nil, api.NewEncodeError("VectorResult.Encode", "vector result", err)
	}
	return data, nil
}

// Decode deserializes a VectorResult from bytes.
func (r *VectorResult) Decode(data []byte) error {
	if err := msgpack.Unmarshal(data, r); err != nil {
		return api.NewDecodeError("VectorResult.Decode", "vector result", err)
	}
	return nil
}

// Encode serializes an UpdateNamespaceResult to bytes using msgpack.
func (r *UpdateNamespaceResult) Encode() ([]byte, error) {
	data, err := msgpack.Marshal(r)
	if err != nil {
		return nil, api.NewEncodeError("UpdateNamespaceResult.Encode", "update namespace result", err)
	}
	return data, nil
}

// Decode deserializes an UpdateNamespaceResult from bytes.
func (r *UpdateNamespaceResult) Decode(data []byte) error {
	if err := msgpack.Unmarshal(data, r); err != nil {
		return api.NewDecodeError("UpdateNamespaceResult.Decode", "update namespace result", err)
	}
	return nil
}

// Validate validates the VectorCommand.
func (cmd *VectorCommand) Validate() error {
	// Some operations don't require namespace
	requiresNamespace := true
	switch cmd.Op {
	case OpHealthCheck, OpListNamespaces:
		requiresNamespace = false
	}

	if requiresNamespace && cmd.Namespace == "" {
		return api.NewValidationError("VectorCommand.Validate", "namespace is required")
	}

	switch cmd.Op {
	case OpUpsertVector:
		if cmd.ID == 0 {
			return api.NewValidationError("VectorCommand.Validate", "ID is required for upsert")
		}
		if len(cmd.Vector) == 0 {
			return api.NewValidationError("VectorCommand.Validate", "vector is required for upsert")
		}

	case OpUpdateMetadata:
		if cmd.ID == 0 {
			return api.NewValidationError("VectorCommand.Validate", "ID is required for metadata update")
		}
		if len(cmd.Metadata) == 0 {
			return api.NewValidationError("VectorCommand.Validate", "metadata is required for metadata update")
		}

	case OpDeleteVector:
		// Allow either single delete (ID) or batch delete (IDs)
		if cmd.ID == 0 && len(cmd.IDs) == 0 {
			return api.NewValidationError("VectorCommand.Validate", "ID or IDs are required for delete")
		}

	case OpBatchUpsert:
		if len(cmd.IDs) == 0 || len(cmd.Vectors) == 0 {
			return api.NewValidationError("VectorCommand.Validate", "IDs and vectors are required for batch upsert")
		}
		if len(cmd.IDs) != len(cmd.Vectors) {
			return api.NewValidationError("VectorCommand.Validate", "IDs and vectors length mismatch")
		}

	case OpCreateNamespace:
		if cmd.Dimension == 0 {
			return api.NewValidationError("VectorCommand.Validate", "dimension is required for namespace creation")
		}

	case OpSearchSimilar:
		if len(cmd.Vector) == 0 {
			return api.NewValidationError("VectorCommand.Validate", "query vector is required for search")
		}
		if cmd.K <= 0 {
			return api.NewValidationError("VectorCommand.Validate", "k must be positive for search")
		}

	case OpGetVector:
		if cmd.ID == 0 {
			return api.NewValidationError("VectorCommand.Validate", "ID is required for get")
		}

	case OpSearchByID:
		if cmd.ID == 0 {
			return api.NewValidationError("VectorCommand.Validate", "ID is required for search by ID")
		}
		if cmd.K <= 0 {
			return api.NewValidationError("VectorCommand.Validate", "k must be positive for search")
		}
	}

	return nil
}

// Size returns the approximate size of the command in bytes.
func (cmd *VectorCommand) Size() int {
	size := 1 + len(cmd.Namespace) + 8 + 8 + 8 + len(cmd.NodeID) + 4 // op, namespace, id, version, timestamp, nodeID, dimension

	// Vector data
	size += len(cmd.Vector) * 4

	// Batch data
	size += len(cmd.IDs) * 8
	for _, vec := range cmd.Vectors {
		size += len(vec) * 4
	}

	// Metadata (approximate)
	if cmd.Metadata != nil {
		size += len(cmd.Metadata) * 100
	}

	// Index params (approximate)
	if cmd.IndexParams != nil {
		size += len(cmd.IndexParams) * 100
	}

	return size
}

// NewUpsertCommand creates a new upsert vector command.
func NewUpsertCommand(namespace string, id uint64, vector []float32, metadata map[string]interface{}, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpUpsertVector,
		Namespace: namespace,
		ID:        id,
		Vector:    vector,
		Metadata:  metadata,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// NewDeleteCommand creates a new delete vector command.
func NewDeleteCommand(namespace string, id uint64, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpDeleteVector,
		Namespace: namespace,
		ID:        id,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// NewUpdateMetadataCommand creates a new update metadata command.
func NewUpdateMetadataCommand(namespace string, id uint64, metadata map[string]interface{}, merge bool, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpUpdateMetadata,
		Namespace: namespace,
		ID:        id,
		Metadata:  metadata,
		Merge:     merge,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// NewBatchUpsertCommand creates a new batch upsert command.
func NewBatchUpsertCommand(namespace string, ids []uint64, vectors [][]float32, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpBatchUpsert,
		Namespace: namespace,
		IDs:       ids,
		Vectors:   vectors,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// NewCreateNamespaceCommand creates a new namespace creation command.
func NewCreateNamespaceCommand(namespace string, dimension uint32, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpCreateNamespace,
		Namespace: namespace,
		Dimension: dimension,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// NewCreateNamespaceCommandWithMetric creates a new namespace creation command with distance metric.
func NewCreateNamespaceCommandWithMetric(namespace string, dimension uint32, distanceMetric string, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:             OpCreateNamespace,
		Namespace:      namespace,
		Dimension:      dimension,
		DistanceMetric: distanceMetric,
		Timestamp:      time.Now().UnixNano(),
		NodeID:         nodeID,
	}
}

// NamespaceConfig holds all configuration options for namespace creation.
type NamespaceConfig struct {
	Namespace      string
	Dimension      uint32
	DistanceMetric string
	IndexType      string
	IndexParams    map[string]string
	Metadata       map[string]string
	NodeID         string
}

// NewCreateNamespaceCommandFull creates a namespace command with all configuration options.
func NewCreateNamespaceCommandFull(cfg *NamespaceConfig) *VectorCommand {
	return &VectorCommand{
		Op:                OpCreateNamespace,
		Namespace:         cfg.Namespace,
		Dimension:         cfg.Dimension,
		DistanceMetric:    cfg.DistanceMetric,
		IndexType:         cfg.IndexType,
		IndexParams:       cfg.IndexParams,
		NamespaceMetadata: cfg.Metadata,
		Timestamp:         time.Now().UnixNano(),
		NodeID:            cfg.NodeID,
	}
}

// NewCreateNamespaceCommandWithTenant creates a new namespace creation command with multi-tenant support.
func NewCreateNamespaceCommandWithTenant(
	namespace string,
	dimension uint32,
	nodeID string,
	tenantID string,
	ownerUserID string,
	quotas *NamespaceQuotas,
	labels map[string]string,
) *VectorCommand {
	return &VectorCommand{
		Op:          OpCreateNamespace,
		Namespace:   namespace,
		Dimension:   dimension,
		Timestamp:   time.Now().UnixNano(),
		NodeID:      nodeID,
		TenantID:    tenantID,
		OwnerUserID: ownerUserID,
		Quotas:      quotas,
		Labels:      labels,
	}
}

// NewDeleteNamespaceCommand creates a new namespace deletion command.
func NewDeleteNamespaceCommand(namespace string, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpDeleteNamespace,
		Namespace: namespace,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// NewSearchCommand creates a new search command.
func NewSearchCommand(namespace string, queryVector []float32, k int, efSearch int, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpSearchSimilar,
		Namespace: namespace,
		Vector:    queryVector,
		K:         k,
		EfSearch:  efSearch,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// NewGetVectorCommand creates a new get vector command.
func NewGetVectorCommand(namespace string, id uint64, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpGetVector,
		Namespace: namespace,
		ID:        id,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// NewSearchByIDCommand creates a new search by ID command.
func NewSearchByIDCommand(namespace string, id uint64, k int, efSearch int, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpSearchByID,
		Namespace: namespace,
		ID:        id,
		K:         k,
		EfSearch:  efSearch,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// NewListVectorsCommand creates a new list vectors command.
func NewListVectorsCommand(namespace string, limit int, cursor string, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpListVectors,
		Namespace: namespace,
		Limit:     limit,
		Cursor:    cursor,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// NewGetNamespaceInfoCommand creates a new get namespace info command.
func NewGetNamespaceInfoCommand(namespace string, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpGetNamespaceInfo,
		Namespace: namespace,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// NewListNamespacesCommand creates a new list namespaces command.
func NewListNamespacesCommand(nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpListNamespaces,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// NewUpdateNamespaceCommand creates a new update namespace command.
func NewUpdateNamespaceCommand(namespace string, indexParams map[string]string, metadata map[string]interface{}, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:          OpUpdateNamespace,
		Namespace:   namespace,
		IndexParams: indexParams,
		Metadata:    metadata,
		Timestamp:   time.Now().UnixNano(),
		NodeID:      nodeID,
	}
}

// NewGetStatsCommand creates a new get stats command.
func NewGetStatsCommand(namespace string, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpGetStats,
		Namespace: namespace,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// NewRebuildIndexCommand creates a new rebuild index command.
func NewRebuildIndexCommand(namespace string, jobID string, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpRebuildIndex,
		Namespace: namespace,
		JobID:     jobID,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// NewGetIndexProgressCommand creates a new get index progress command.
func NewGetIndexProgressCommand(namespace string, jobID string, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpGetIndexProgress,
		Namespace: namespace,
		JobID:     jobID,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// NewHealthCheckCommand creates a new health check command.
func NewHealthCheckCommand(nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpHealthCheck,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}

// EncodeUint64 encodes a uint64 to bytes (big-endian).
func EncodeUint64(val uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, val)
	return b
}

// DecodeUint64 decodes a uint64 from bytes (big-endian).
func DecodeUint64(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}

// EncodeUint32 encodes a uint32 to bytes (big-endian).
func EncodeUint32(val uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, val)
	return b
}

// DecodeUint32 decodes a uint32 from bytes (big-endian).
func DecodeUint32(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}

// VectorSnapshot represents a point-in-time snapshot of the vector state machine.
type VectorSnapshot struct {
	Namespaces map[string]*NamespaceState `msgpack:"ns"`
	Timestamp  int64                      `msgpack:"ts"`
	Version    uint64                     `msgpack:"ver"`
}

// NewBatchDeleteCommand creates a new batch delete command.
func NewBatchDeleteCommand(namespace string, ids []uint64, nodeID string) *VectorCommand {
	return &VectorCommand{
		Op:        OpDeleteVector,
		Namespace: namespace,
		IDs:       ids,
		Timestamp: time.Now().UnixNano(),
		NodeID:    nodeID,
	}
}
