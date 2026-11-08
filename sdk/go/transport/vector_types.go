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

package transport

import "strings"

// ---------------------------------------------------------------------------
// Enumeration type aliases
// ---------------------------------------------------------------------------

// DistanceMetric is a type alias for vector distance metric strings.
type DistanceMetric = string

// Distance metric constants.
const (
	DistanceCosine     DistanceMetric = "cosine"
	DistanceEuclidean  DistanceMetric = "euclidean"
	DistanceDotProduct DistanceMetric = "dot_product"
	DistanceManhattan  DistanceMetric = "manhattan"
)

// VectorIndexType is a type alias for vector index type strings.
type VectorIndexType = string

// Index type constants.
const (
	IndexHNSW    VectorIndexType = "hnsw"
	IndexFlat    VectorIndexType = "flat"
	IndexIVFFlat VectorIndexType = "ivf_flat"
)

// serverIndexTypes maps every index-type name a server reports, upper-cased,
// to its VectorIndexType: the service's canonical names (HNSW, FLAT, IVF) and
// the SDK's own, which a namespace record written by an SDK client carries.
var serverIndexTypes = map[string]VectorIndexType{
	"HNSW":     IndexHNSW,
	"FLAT":     IndexFlat,
	"IVF":      IndexIVFFlat,
	"IVF_FLAT": IndexIVFFlat,
}

// ParseVectorIndexType resolves an index-type name a server reports,
// case-insensitively, to its VectorIndexType, so a transport that carries the
// name as a string reports the same value as one that carries the protobuf
// enum. A name it does not know is returned unchanged.
func ParseVectorIndexType(name string) VectorIndexType {
	if indexType, ok := serverIndexTypes[strings.ToUpper(name)]; ok {
		return indexType
	}
	return name
}

// serverDistanceMetrics maps the distance metric names a server reports, in
// upper case, to the SDK constants. A server reports a metric in the case it
// was created with, and L2 for Euclidean.
var serverDistanceMetrics = map[string]DistanceMetric{
	"COSINE":      DistanceCosine,
	"EUCLIDEAN":   DistanceEuclidean,
	"L2":          DistanceEuclidean,
	"DOT_PRODUCT": DistanceDotProduct,
	"MANHATTAN":   DistanceManhattan,
}

// ParseDistanceMetric converts a distance metric name a server reported, in
// any case, to its SDK constant, so every transport reports one name for one
// metric. A name the SDK does not know is refused with
// *UnknownDistanceMetricError, never read as cosine.
func ParseDistanceMetric(name string) (DistanceMetric, error) {
	if metric, ok := serverDistanceMetrics[strings.ToUpper(name)]; ok {
		return metric, nil
	}
	return "", &UnknownDistanceMetricError{Metric: name}
}

// ---------------------------------------------------------------------------
// Namespace types
// ---------------------------------------------------------------------------

// CreateNamespaceRequest contains the parameters for creating a vector namespace.
type CreateNamespaceRequest struct {
	Namespace      string                 `json:"namespace"`
	Dimension      uint32                 `json:"dimension"`
	DistanceMetric string                 `json:"distance_metric"`
	IndexType      string                 `json:"index_type"`
	IndexConfig    map[string]string      `json:"index_config,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
	ShardID        uint64                 `json:"shard_id,omitempty"`
}

// CreateNamespaceResponse contains the result of a namespace creation.
type CreateNamespaceResponse struct {
	Namespace string `json:"namespace"`
	Dimension uint32 `json:"dimension"`
	Timestamp int64  `json:"timestamp"`
	Success   bool   `json:"success"`
}

// UpdateNamespaceRequest changes a namespace in place. Each key of
// IndexConfig and Metadata is merged over the stored value; a key the request
// does not name keeps its value. Dimension, when non-zero, must equal the
// namespace's dimension.
type UpdateNamespaceRequest struct {
	Namespace   string                 `json:"namespace"`
	Dimension   uint32                 `json:"dimension,omitempty"`
	IndexConfig map[string]string      `json:"index_config,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// UpdateNamespaceResponse reports an in-place namespace update.
// RequiresRebuild is set when an index parameter changed, so the index must
// be rebuilt (RebuildIndex) before it reflects the new parameters.
type UpdateNamespaceResponse struct {
	Namespace       string `json:"namespace"`
	RequiresRebuild bool   `json:"requires_rebuild"`
	Timestamp       int64  `json:"timestamp"`
	Success         bool   `json:"success"`
}

// DeleteNamespaceRequest contains the parameters for deleting a namespace.
type DeleteNamespaceRequest struct {
	Namespace string `json:"namespace"`
	ShardID   uint64 `json:"shard_id,omitempty"`
}

// DeleteNamespaceResponse contains the result of namespace deletion.
type DeleteNamespaceResponse struct {
	Success        bool   `json:"success"`
	VectorsDeleted uint64 `json:"vectors_deleted"`
	Timestamp      int64  `json:"timestamp"`
}

// ListNamespacesRequest contains the parameters for listing namespaces.
type ListNamespacesRequest struct {
	Prefix  string `json:"prefix,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	ShardID uint64 `json:"shard_id,omitempty"`
}

// ListNamespacesResponse contains the list of namespaces.
type ListNamespacesResponse struct {
	Namespaces []*NamespaceInfo `json:"namespaces"`
	TotalCount int              `json:"total_count"`
	Timestamp  int64            `json:"timestamp"`
}

// GetNamespaceInfoRequest contains the parameters for retrieving namespace metadata.
type GetNamespaceInfoRequest struct {
	Namespace string `json:"namespace"`
	ShardID   uint64 `json:"shard_id,omitempty"`
}

// GetNamespaceInfoResponse contains namespace information.
type GetNamespaceInfoResponse struct {
	Namespace      string                 `json:"namespace"`
	Dimension      uint32                 `json:"dimension"`
	VectorCount    uint64                 `json:"vector_count"`
	DistanceMetric string                 `json:"distance_metric"`
	IndexType      string                 `json:"index_type"`
	IndexConfig    map[string]string      `json:"index_config,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt      int64                  `json:"created_at"`
	UpdatedAt      int64                  `json:"updated_at"`
}

// NamespaceInfo contains metadata about a vector namespace.
type NamespaceInfo struct {
	Namespace      string            `json:"namespace"`
	Dimension      uint32            `json:"dimension"`
	VectorCount    uint64            `json:"vector_count"`
	DistanceMetric string            `json:"distance_metric"`
	IndexType      string            `json:"index_type"`
	IndexConfig    map[string]string `json:"index_config,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	CreatedAt      int64             `json:"created_at"`
	UpdatedAt      int64             `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Vector CRUD types
// ---------------------------------------------------------------------------

// VectorData represents a single vector with its metadata.
type VectorData struct {
	ID        uint64                 `json:"id"`
	Vector    []float32              `json:"vector"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Version   uint64                 `json:"version"`
	CreatedAt int64                  `json:"created_at"`
	UpdatedAt int64                  `json:"updated_at"`
}

// UpsertVectorRequest contains the parameters for inserting or updating a vector.
type UpsertVectorRequest struct {
	Namespace string                 `json:"namespace"`
	ID        uint64                 `json:"id"`
	Vector    []float32              `json:"vector"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	ShardID   uint64                 `json:"shard_id,omitempty"`
}

// UpsertVectorResponse contains the result of a vector upsert.
type UpsertVectorResponse struct {
	ID        uint64 `json:"id"`
	Version   uint64 `json:"version"`
	Timestamp int64  `json:"timestamp"`
	Success   bool   `json:"success"`
}

// GetVectorRequest contains the parameters for retrieving a single vector.
type GetVectorRequest struct {
	Namespace       string `json:"namespace"`
	ID              uint64 `json:"id"`
	IncludeVector   bool   `json:"include_vector,omitempty"`
	IncludeMetadata bool   `json:"include_metadata,omitempty"`
	ShardID         uint64 `json:"shard_id,omitempty"`
	Consistency     uint8  `json:"consistency,omitempty"`
}

// GetVectorResponse contains the retrieved vector.
type GetVectorResponse struct {
	ID        uint64                 `json:"id"`
	Vector    []float32              `json:"vector,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Version   uint64                 `json:"version"`
	CreatedAt int64                  `json:"created_at"`
	UpdatedAt int64                  `json:"updated_at"`
	Found     bool                   `json:"found"`
}

// DeleteVectorRequest contains the parameters for deleting a single vector.
type DeleteVectorRequest struct {
	Namespace string `json:"namespace"`
	ID        uint64 `json:"id"`
	ShardID   uint64 `json:"shard_id,omitempty"`
}

// DeleteVectorResponse contains the result of vector deletion.
type DeleteVectorResponse struct {
	Success   bool  `json:"success"`
	Timestamp int64 `json:"timestamp"`
}

// BatchUpsertRequest contains the parameters for bulk vector upserts.
type BatchUpsertRequest struct {
	Namespace string        `json:"namespace"`
	Vectors   []*VectorData `json:"vectors"`
	ShardID   uint64        `json:"shard_id,omitempty"`
}

// BatchUpsertResponse contains the result of a batch upsert operation.
type BatchUpsertResponse struct {
	IDs           []uint64 `json:"ids"`
	Versions      []uint64 `json:"versions"`
	Timestamp     int64    `json:"timestamp"`
	Success       bool     `json:"success"`
	InsertedCount int      `json:"inserted_count"`
}

// BatchDeleteVectorsRequest contains the parameters for bulk vector deletion.
type BatchDeleteVectorsRequest struct {
	Namespace string   `json:"namespace"`
	IDs       []uint64 `json:"ids"`
	ShardID   uint64   `json:"shard_id,omitempty"`
}

// BatchDeleteVectorsResponse contains the result of a batch delete.
type BatchDeleteVectorsResponse struct {
	DeletedCount int      `json:"deleted_count"`
	FailedIDs    []uint64 `json:"failed_ids,omitempty"`
	Timestamp    int64    `json:"timestamp"`
	Success      bool     `json:"success"`
}

// ListVectorsRequest contains the parameters for paginated vector listing.
type ListVectorsRequest struct {
	Namespace       string `json:"namespace"`
	Limit           int    `json:"limit,omitempty"`
	Cursor          string `json:"cursor,omitempty"`
	IDPrefix        string `json:"id_prefix,omitempty"`
	IncludeVectors  bool   `json:"include_vectors,omitempty"`
	IncludeMetadata bool   `json:"include_metadata,omitempty"`
	ShardID         uint64 `json:"shard_id,omitempty"`
	Consistency     uint8  `json:"consistency,omitempty"`
}

// ListVectorsResponse contains a paginated list of vectors.
type ListVectorsResponse struct {
	Vectors    []*VectorData `json:"vectors"`
	NextCursor string        `json:"next_cursor,omitempty"`
	TotalCount uint64        `json:"total_count"`
	Timestamp  int64         `json:"timestamp"`
}

// UpdateVectorMetadataRequest contains the parameters for updating vector metadata.
type UpdateVectorMetadataRequest struct {
	Namespace     string                 `json:"namespace"`
	ID            uint64                 `json:"id"`
	Metadata      map[string]interface{} `json:"metadata"`
	MergeMetadata bool                   `json:"merge_metadata,omitempty"`
	DeleteKeys    []string               `json:"delete_keys,omitempty"`
	ShardID       uint64                 `json:"shard_id,omitempty"`
}

// UpdateVectorMetadataResponse contains the result of metadata update.
type UpdateVectorMetadataResponse struct {
	ID        uint64 `json:"id"`
	Version   uint64 `json:"version"`
	Timestamp int64  `json:"timestamp"`
	Success   bool   `json:"success"`
}

// ---------------------------------------------------------------------------
// Search types
// ---------------------------------------------------------------------------

// VectorSearchResult represents a single result from a similarity search.
type VectorSearchResult struct {
	ID       uint64                 `json:"id"`
	Score    float32                `json:"score"`
	Vector   []float32              `json:"vector,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Distance float32                `json:"distance"`
	Rank     int                    `json:"rank"`
}

// SearchSimilarRequest contains the parameters for a k-NN similarity search.
type SearchSimilarRequest struct {
	Namespace       string                 `json:"namespace"`
	QueryVector     []float32              `json:"query_vector"`
	K               int                    `json:"k"`
	EfSearch        int                    `json:"ef_search,omitempty"`
	MinScore        float32                `json:"min_score,omitempty"`
	IncludeVectors  bool                   `json:"include_vectors,omitempty"`
	IncludeMetadata bool                   `json:"include_metadata,omitempty"`
	MetadataFilter  map[string]interface{} `json:"metadata_filter,omitempty"`
	ShardID         uint64                 `json:"shard_id,omitempty"`
	Consistency     uint8                  `json:"consistency,omitempty"`
}

// SearchSimilarResponse contains the results of a similarity search.
type SearchSimilarResponse struct {
	Results       []*VectorSearchResult `json:"results"`
	Timestamp     int64                 `json:"timestamp"`
	QueryDuration int64                 `json:"query_duration"`
}

// SearchByMetadataRequest contains the parameters for metadata-only search.
type SearchByMetadataRequest struct {
	Namespace       string                 `json:"namespace"`
	Filters         map[string]interface{} `json:"filters"`
	FilterOperator  string                 `json:"filter_operator,omitempty"`
	Limit           int                    `json:"limit,omitempty"`
	Offset          int                    `json:"offset,omitempty"`
	IncludeVectors  bool                   `json:"include_vectors,omitempty"`
	IncludeMetadata bool                   `json:"include_metadata,omitempty"`
	ShardID         uint64                 `json:"shard_id,omitempty"`
	Consistency     uint8                  `json:"consistency,omitempty"`
}

// SearchByMetadataResponse contains the results of a metadata search.
type SearchByMetadataResponse struct {
	Results    []*VectorData `json:"results"`
	TotalCount uint64        `json:"total_count"`
	Timestamp  int64         `json:"timestamp"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// HybridSearchRequest contains the parameters for combined vector+metadata search.
type HybridSearchRequest struct {
	Namespace          string                 `json:"namespace"`
	QueryVector        []float32              `json:"query_vector"`
	K                  int                    `json:"k"`
	EfSearch           int                    `json:"ef_search,omitempty"`
	MinScore           float32                `json:"min_score,omitempty"`
	MetadataFilter     map[string]interface{} `json:"metadata_filter,omitempty"`
	FilterOperator     string                 `json:"filter_operator,omitempty"`
	RerankingAlgorithm string                 `json:"reranking_algorithm,omitempty"`
	IncludeVectors     bool                   `json:"include_vectors,omitempty"`
	IncludeMetadata    bool                   `json:"include_metadata,omitempty"`
	ShardID            uint64                 `json:"shard_id,omitempty"`
	Consistency        uint8                  `json:"consistency,omitempty"`
}

// HybridSearchResponse contains the results of a hybrid search.
type HybridSearchResponse struct {
	Results         []*VectorSearchResult `json:"results"`
	TotalCandidates int                   `json:"total_candidates"`
	TotalFiltered   int                   `json:"total_filtered"`
	Timestamp       int64                 `json:"timestamp"`
	QueryDuration   int64                 `json:"query_duration"`
}

// SearchByIDRequest contains the parameters for searching by existing vector ID.
type SearchByIDRequest struct {
	Namespace       string                 `json:"namespace"`
	ID              uint64                 `json:"id"`
	K               int                    `json:"k"`
	EfSearch        int                    `json:"ef_search,omitempty"`
	MinScore        float32                `json:"min_score,omitempty"`
	IncludeVectors  bool                   `json:"include_vectors,omitempty"`
	IncludeMetadata bool                   `json:"include_metadata,omitempty"`
	MetadataFilter  map[string]interface{} `json:"metadata_filter,omitempty"`
	ExcludeSelf     bool                   `json:"exclude_self,omitempty"`
	ShardID         uint64                 `json:"shard_id,omitempty"`
	Consistency     uint8                  `json:"consistency,omitempty"`
}

// SearchByIDResponse contains search by ID results.
type SearchByIDResponse struct {
	SourceID      uint64                `json:"source_id"`
	Results       []*VectorSearchResult `json:"results"`
	Timestamp     int64                 `json:"timestamp"`
	QueryDuration int64                 `json:"query_duration"`
}

// ---------------------------------------------------------------------------
// Index management types
// ---------------------------------------------------------------------------

// RebuildIndexRequest contains the parameters for triggering an index rebuild.
type RebuildIndexRequest struct {
	Namespace   string            `json:"namespace"`
	IndexConfig map[string]string `json:"index_config,omitempty"`
	Async       bool              `json:"async,omitempty"`
	ShardID     uint64            `json:"shard_id,omitempty"`
}

// RebuildIndexResponse contains the result of an index rebuild request.
type RebuildIndexResponse struct {
	JobID                  string  `json:"job_id"`
	Status                 string  `json:"status"`
	VectorsProcessed       uint64  `json:"vectors_processed"`
	TotalVectors           uint64  `json:"total_vectors"`
	PercentComplete        float64 `json:"percent_complete"`
	EstimatedTimeRemaining int64   `json:"estimated_time_remaining"`
	Timestamp              int64   `json:"timestamp"`
	Success                bool    `json:"success"`
}

// GetRebuildProgressRequest contains the parameters for querying rebuild progress.
type GetRebuildProgressRequest struct {
	JobID     string `json:"job_id,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// GetRebuildProgressResponse contains the current rebuild progress.
type GetRebuildProgressResponse struct {
	JobID                    string  `json:"job_id"`
	Namespace                string  `json:"namespace"`
	Status                   string  `json:"status"`
	VectorsProcessed         uint64  `json:"vectors_processed"`
	TotalVectors             uint64  `json:"total_vectors"`
	PercentComplete          float64 `json:"percent_complete"`
	ElapsedTimeMs            int64   `json:"elapsed_time_ms"`
	EstimatedTimeRemainingMs int64   `json:"estimated_time_remaining_ms"`
	StartedAt                int64   `json:"started_at"`
	CompletedAt              int64   `json:"completed_at"`
	Error                    string  `json:"error,omitempty"`
}

// GetIndexStatsRequest contains the parameters for querying index statistics.
type GetIndexStatsRequest struct {
	Namespace string `json:"namespace"`
	ShardID   uint64 `json:"shard_id,omitempty"`
}

// GetIndexStatsResponse contains index statistics.
type GetIndexStatsResponse struct {
	Namespace         string                   `json:"namespace"`
	IndexType         string                   `json:"index_type"`
	VectorCount       uint64                   `json:"vector_count"`
	Dimension         uint32                   `json:"dimension"`
	IndexSizeBytes    uint64                   `json:"index_size_bytes"`
	MemoryUsageBytes  uint64                   `json:"memory_usage_bytes"`
	IndexConfig       map[string]string        `json:"index_config,omitempty"`
	SearchPerformance *IndexPerformanceMetrics `json:"search_performance,omitempty"`
	Timestamp         int64                    `json:"timestamp"`
}

// IndexPerformanceMetrics contains index performance statistics.
type IndexPerformanceMetrics struct {
	AverageSearchTimeMs float64 `json:"average_search_time_ms"`
	P50SearchTimeMs     float64 `json:"p50_search_time_ms"`
	P95SearchTimeMs     float64 `json:"p95_search_time_ms"`
	P99SearchTimeMs     float64 `json:"p99_search_time_ms"`
	TotalSearches       uint64  `json:"total_searches"`
	CacheHitRate        float64 `json:"cache_hit_rate"`
}

// ---------------------------------------------------------------------------
// Compaction types
// ---------------------------------------------------------------------------

// CompactNamespaceRequest contains the parameters for triggering compaction.
type CompactNamespaceRequest struct {
	Namespace string `json:"namespace"`
	Async     bool   `json:"async,omitempty"`
	TimeoutMs int64  `json:"timeout_ms,omitempty"`
	ShardID   uint64 `json:"shard_id,omitempty"`
}

// CompactNamespaceResponse contains the result of a compaction request.
type CompactNamespaceResponse struct {
	OperationID    string `json:"operation_id"`
	StartedAt      int64  `json:"started_at"`
	CompletedAt    *int64 `json:"completed_at,omitempty"`
	BytesCompacted int64  `json:"bytes_compacted"`
	Success        bool   `json:"success"`
}

// GetCompactionProgressRequest contains the parameters for querying compaction progress.
type GetCompactionProgressRequest struct {
	Namespace   string `json:"namespace"`
	OperationID string `json:"operation_id,omitempty"`
	ShardID     uint64 `json:"shard_id,omitempty"`
}

// GetCompactionProgressResponse contains compaction progress information.
type GetCompactionProgressResponse struct {
	OperationID     string  `json:"operation_id"`
	Namespace       string  `json:"namespace"`
	BytesCompacted  int64   `json:"bytes_compacted"`
	TotalBytes      int64   `json:"total_bytes"`
	PercentComplete float64 `json:"percent_complete"`
	InProgress      bool    `json:"in_progress"`
	StartedAt       int64   `json:"started_at"`
	CompletedAt     *int64  `json:"completed_at,omitempty"`
	Error           string  `json:"error,omitempty"`
}
