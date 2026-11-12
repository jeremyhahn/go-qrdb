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

// Package service provides the vector service interface and types.
package service

import (
	"context"
)

// =============================================================================
// Vector Service - Vector Similarity Search Operations
// =============================================================================

// VectorService defines vector embedding and similarity search operations.
//
// # Error contract
//
// Every error a VectorService method returns is one of the service layer's
// typed errors, which every transport classifies (pkg/api/errclass), so a
// caller branches with errors.As and never on a code or a message
// (go-qrdb#331):
//
//   - *InvalidVectorError, *DimensionMismatchError, *InvalidNamespaceNameError:
//     the request is malformed (400 / InvalidArgument).
//   - *NamespaceNotFoundError, *VectorNotFoundError, *VectorJobNotFoundError:
//     the named namespace, vector, or rebuild/compaction job does not exist
//     (404 / NotFound).
//   - *NamespaceExistsError: the namespace name is taken (409 / AlreadyExists).
//   - *TenantRequiredError: strict tenant isolation refused a request bound to
//     no tenant (403 / PermissionDenied).
//   - *VectorServiceClosedError, *NotLeaderError: this node cannot serve the
//     request now; retry (503 / Unavailable).
//   - *EncodeRequestError: the command could not be encoded and was never
//     proposed (400 / InvalidArgument).
//   - *CapabilityUnsupportedError, *CommandRejectedError: the vector state
//     machine refused the command (see their docs).
//   - *ProposeError, *VectorOperationError: a server fault (500 / Internal).
type VectorService interface {
	// Namespace Management

	// CreateNamespace creates a new vector namespace with specified dimensions.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Create namespace request with name, dimension, index config
	// Returns:
	//   - CreateNamespaceResponse: Contains namespace info and timestamp
	//   - error: one of the typed errors in the error contract above
	CreateNamespace(ctx context.Context, req *CreateNamespaceRequest) (*CreateNamespaceResponse, error)

	// UpdateNamespace changes a namespace's index parameters and metadata in
	// place and keeps every vector in it.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Update namespace request with the keys to merge
	// Returns:
	//   - UpdateNamespaceResponse: Whether the new index parameters need a rebuild
	//   - error: NamespaceNotFoundError for a missing namespace,
	//     DimensionMismatchError for a dimension change, or another typed
	//     error in the error contract above
	UpdateNamespace(ctx context.Context, req *UpdateNamespaceRequest) (*UpdateNamespaceResponse, error)

	// DeleteNamespace removes a vector namespace and all its vectors.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Delete namespace request with namespace name
	// Returns:
	//   - DeleteNamespaceResponse: Contains deletion status and timestamp
	//   - error: one of the typed errors in the error contract above
	DeleteNamespace(ctx context.Context, req *DeleteNamespaceRequest) (*DeleteNamespaceResponse, error)

	// ListNamespaces returns all available vector namespaces.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: List namespaces request with optional filters
	// Returns:
	//   - ListNamespacesResponse: Contains namespace list and metadata
	//   - error: one of the typed errors in the error contract above
	ListNamespaces(ctx context.Context, req *ListNamespacesRequest) (*ListNamespacesResponse, error)

	// GetNamespaceInfo retrieves metadata about a specific namespace.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Get namespace info request with namespace name
	// Returns:
	//   - GetNamespaceInfoResponse: Contains namespace details and statistics
	//   - error: one of the typed errors in the error contract above
	GetNamespaceInfo(ctx context.Context, req *GetNamespaceInfoRequest) (*GetNamespaceInfoResponse, error)

	// Vector CRUD Operations

	// UpsertVector inserts or updates a vector with optional metadata.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Upsert request with namespace, ID, vector, metadata
	// Returns:
	//   - UpsertVectorResponse: Contains vector ID, version, timestamp
	//   - error: one of the typed errors in the error contract above
	UpsertVector(ctx context.Context, req *UpsertVectorRequest) (*UpsertVectorResponse, error)

	// GetVector retrieves a vector by ID with optional metadata.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Get vector request with namespace and ID
	// Returns:
	//   - GetVectorResponse: Contains vector data, metadata, version
	//   - error: one of the typed errors in the error contract above
	GetVector(ctx context.Context, req *GetVectorRequest) (*GetVectorResponse, error)

	// DeleteVector removes a vector from the namespace.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Delete vector request with namespace and ID
	// Returns:
	//   - DeleteVectorResponse: Contains deletion status and timestamp
	//   - error: one of the typed errors in the error contract above
	DeleteVector(ctx context.Context, req *DeleteVectorRequest) (*DeleteVectorResponse, error)

	// BatchUpsert inserts or updates multiple vectors atomically.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Batch upsert request with namespace and vector list
	// Returns:
	//   - BatchUpsertResponse: Contains inserted IDs, versions, timestamp
	//   - error: one of the typed errors in the error contract above
	BatchUpsert(ctx context.Context, req *BatchUpsertRequest) (*BatchUpsertResponse, error)

	// BatchDelete removes multiple vectors from the namespace.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Batch delete request with namespace and vector IDs
	// Returns:
	//   - BatchDeleteVectorsResponse: Contains deleted count and status
	//   - error: one of the typed errors in the error contract above
	BatchDeleteVectors(ctx context.Context, req *BatchDeleteVectorsRequest) (*BatchDeleteVectorsResponse, error)

	// ListVectors returns vectors in a namespace with pagination.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: List vectors request with namespace and pagination options
	// Returns:
	//   - ListVectorsResponse: Contains vector list and pagination cursor
	//   - error: one of the typed errors in the error contract above
	ListVectors(ctx context.Context, req *ListVectorsRequest) (*ListVectorsResponse, error)

	// UpdateMetadata updates only the metadata of a vector without modifying the vector values.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Update metadata request with namespace, ID, and new metadata
	// Returns:
	//   - UpdateMetadataResponse: Contains updated version and timestamp
	//   - error: one of the typed errors in the error contract above
	UpdateMetadata(ctx context.Context, req *UpdateVectorMetadataRequest) (*UpdateVectorMetadataResponse, error)

	// Search Operations

	// SearchSimilar performs k-nearest neighbor similarity search.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Search request with query vector, k, filters
	// Returns:
	//   - SearchSimilarResponse: Contains matching vectors with scores
	//   - error: one of the typed errors in the error contract above
	SearchSimilar(ctx context.Context, req *SearchSimilarRequest) (*SearchSimilarResponse, error)

	// SearchByMetadata performs metadata-based vector search.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Metadata search request with filters
	// Returns:
	//   - SearchByMetadataResponse: Contains matching vectors
	//   - error: one of the typed errors in the error contract above
	SearchByMetadata(ctx context.Context, req *SearchByMetadataRequest) (*SearchByMetadataResponse, error)

	// HybridSearch combines similarity and metadata filtering.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Hybrid search request with query vector and filters
	// Returns:
	//   - HybridSearchResponse: Contains filtered similarity results
	//   - error: one of the typed errors in the error contract above
	HybridSearch(ctx context.Context, req *HybridSearchRequest) (*HybridSearchResponse, error)

	// SearchByID finds similar vectors using an existing vector's ID as the query.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Search by ID request with namespace, source ID, and k
	// Returns:
	//   - SearchByIDResponse: Contains matching vectors with scores
	//   - error: one of the typed errors in the error contract above
	SearchByID(ctx context.Context, req *SearchByIDRequest) (*SearchByIDResponse, error)

	// Index Management

	// RebuildIndex rebuilds the vector index for a namespace.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Rebuild index request with namespace name
	// Returns:
	//   - RebuildIndexResponse: Contains job ID and status
	//   - error: one of the typed errors in the error contract above
	RebuildIndex(ctx context.Context, req *RebuildIndexRequest) (*RebuildIndexResponse, error)

	// GetRebuildProgress retrieves the progress of an index rebuild operation.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Get rebuild progress request with job ID or namespace
	// Returns:
	//   - GetRebuildProgressResponse: Contains progress, estimated time remaining
	//   - error: one of the typed errors in the error contract above
	GetRebuildProgress(ctx context.Context, req *GetRebuildProgressRequest) (*GetRebuildProgressResponse, error)

	// GetIndexStats retrieves statistics about the vector index.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Get index stats request with namespace name
	// Returns:
	//   - GetIndexStatsResponse: Contains index size, vector count, metrics
	//   - error: one of the typed errors in the error contract above
	GetIndexStats(ctx context.Context, req *GetIndexStatsRequest) (*GetIndexStatsResponse, error)

	// Compaction Operations

	// CompactNamespace triggers compaction for a vector namespace.
	// This reclaims disk space by removing tombstones and old MVCC versions.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Compact namespace request with namespace name and options
	// Returns:
	//   - CompactNamespaceResponse: Contains operation ID and status
	//   - error: one of the typed errors in the error contract above
	CompactNamespace(ctx context.Context, req *CompactNamespaceRequest) (*CompactNamespaceResponse, error)

	// GetCompactionProgress retrieves the progress of a compaction operation.
	// Parameters:
	//   - ctx: Request context with timeout and metadata
	//   - req: Get compaction progress request with namespace and optional operation ID
	// Returns:
	//   - GetCompactionProgressResponse: Contains progress information
	//   - error: one of the typed errors in the error contract above
	GetCompactionProgress(ctx context.Context, req *GetCompactionProgressRequest) (*GetCompactionProgressResponse, error)
}

// VectorServicer is an alias for VectorService, retained for backward
// compatibility with existing transport and API layers.
type VectorServicer = VectorService

// =============================================================================
// Vector Service Types - Namespace Management
// =============================================================================

// CreateNamespaceRequest contains parameters for namespace creation.
type CreateNamespaceRequest struct {
	// Namespace is the unique name for this namespace
	Namespace string

	// Dimension is the vector dimensionality (must match all vectors)
	Dimension uint32

	// DistanceMetric is the distance metric for similarity search (L2, COSINE, DOT_PRODUCT, MANHATTAN)
	DistanceMetric string

	// IndexType is the index type (HNSW, FLAT, etc.)
	IndexType string

	// IndexConfig contains index-specific parameters (e.g., HNSW M, efConstruction)
	IndexConfig map[string]string

	// Metadata contains additional namespace metadata
	Metadata map[string]interface{}

	// ShardID is the target shard for this namespace
	ShardID uint64
}

// CreateNamespaceResponse contains the result of namespace creation.
type CreateNamespaceResponse struct {
	// Namespace is the created namespace name
	Namespace string

	// Dimension is the vector dimensionality
	Dimension uint32

	// Timestamp is the creation timestamp
	Timestamp int64

	// Success indicates if creation succeeded
	Success bool
}

// UpdateNamespaceRequest contains parameters for an in-place namespace update.
// Each key of IndexConfig and Metadata is merged over the stored value; a key
// the request does not name keeps its value.
type UpdateNamespaceRequest struct {
	// Namespace is the namespace to update
	Namespace string

	// Dimension, when non-zero, must equal the namespace's dimension: a
	// dimension cannot change in place, since every stored vector has it
	Dimension uint32

	// IndexConfig contains the index parameters to set (e.g., HNSW m, ef_construction)
	IndexConfig map[string]string

	// Metadata contains the namespace metadata to set
	Metadata map[string]interface{}
}

// UpdateNamespaceResponse contains the result of an in-place namespace update.
type UpdateNamespaceResponse struct {
	// Namespace is the updated namespace name
	Namespace string

	// RequiresRebuild reports that an index parameter changed, so the index
	// must be rebuilt (RebuildIndex) before it reflects the new parameters
	RequiresRebuild bool

	// Timestamp is the update timestamp
	Timestamp int64

	// Success indicates if the update succeeded
	Success bool
}

// DeleteNamespaceRequest contains parameters for namespace deletion.
type DeleteNamespaceRequest struct {
	// Namespace is the namespace to delete
	Namespace string

	// ShardID is the target shard
	ShardID uint64
}

// DeleteNamespaceResponse contains the result of namespace deletion.
type DeleteNamespaceResponse struct {
	// Success indicates if deletion succeeded
	Success bool

	// VectorsDeleted is the count of vectors removed
	VectorsDeleted uint64

	// Timestamp is the deletion timestamp
	Timestamp int64
}

// ListNamespacesRequest contains parameters for listing namespaces.
type ListNamespacesRequest struct {
	// Prefix filters namespaces by name prefix
	Prefix string

	// Limit restricts the number of results
	Limit int

	// Offset is the pagination offset
	Offset int

	// ShardID is the target shard
	ShardID uint64
}

// ListNamespacesResponse contains the list of namespaces.
type ListNamespacesResponse struct {
	// Namespaces contains namespace information
	Namespaces []*NamespaceInfo

	// TotalCount is the total number of namespaces
	TotalCount int

	// Timestamp is the query timestamp
	Timestamp int64
}

// GetNamespaceInfoRequest contains parameters for retrieving namespace info.
type GetNamespaceInfoRequest struct {
	// Namespace is the namespace name
	Namespace string

	// ShardID is the target shard
	ShardID uint64
}

// GetNamespaceInfoResponse contains namespace information.
type GetNamespaceInfoResponse struct {
	// Namespace is the namespace name
	Namespace string

	// Dimension is the vector dimensionality
	Dimension uint32

	// VectorCount is the number of vectors in this namespace
	VectorCount uint64

	// DistanceMetric is the distance metric (L2, COSINE, DOT_PRODUCT, MANHATTAN)
	DistanceMetric string

	// IndexType is the index algorithm (e.g., "HNSW", "IVF")
	IndexType string

	// IndexConfig contains index parameters
	IndexConfig map[string]string

	// Metadata contains namespace metadata
	Metadata map[string]interface{}

	// CreatedAt is the creation timestamp
	CreatedAt int64

	// UpdatedAt is the last update timestamp
	UpdatedAt int64
}

// NamespaceInfo contains basic namespace information.
type NamespaceInfo struct {
	// Namespace is the namespace name
	Namespace string

	// Dimension is the vector dimensionality
	Dimension uint32

	// VectorCount is the number of vectors
	VectorCount uint64

	// DistanceMetric is the distance metric (L2, COSINE, DOT_PRODUCT, MANHATTAN)
	DistanceMetric string

	// IndexType is the index algorithm (e.g., "HNSW", "IVF")
	IndexType string

	// IndexConfig contains index parameters (M, efConstruction, etc.)
	IndexConfig map[string]string

	// Metadata contains namespace metadata
	Metadata map[string]string

	// CreatedAt is the creation timestamp
	CreatedAt int64

	// UpdatedAt is the last update timestamp
	UpdatedAt int64
}

// =============================================================================
// Vector Service Types - Vector CRUD Operations
// =============================================================================

// UpsertVectorRequest contains parameters for vector upsert.
type UpsertVectorRequest struct {
	// Namespace is the target namespace
	Namespace string

	// ID is the unique vector identifier
	ID uint64

	// Vector contains the embedding values
	Vector []float32

	// Metadata contains user-defined attributes
	Metadata map[string]interface{}

	// ShardID is the target shard
	ShardID uint64
}

// UpsertVectorResponse contains the result of vector upsert.
type UpsertVectorResponse struct {
	// ID is the vector identifier
	ID uint64

	// Version is the MVCC version
	Version uint64

	// Timestamp is the operation timestamp
	Timestamp int64

	// Success indicates if upsert succeeded
	Success bool

	// Created is true when no vector with this ID existed in the namespace
	// before the upsert, false when the upsert replaced one
	Created bool
}

// GetVectorRequest contains parameters for vector retrieval.
type GetVectorRequest struct {
	// Namespace is the target namespace
	Namespace string

	// ID is the vector identifier
	ID uint64

	// IncludeVector includes the embedding values in response
	IncludeVector bool

	// IncludeMetadata includes metadata in response
	IncludeMetadata bool

	// ShardID is the target shard
	ShardID uint64

	// Consistency specifies read consistency level
	Consistency ConsistencyLevel
}

// GetVectorResponse contains the retrieved vector.
type GetVectorResponse struct {
	// ID is the vector identifier
	ID uint64

	// Vector contains the embedding values (if requested)
	Vector []float32

	// Metadata contains user-defined attributes (if requested)
	Metadata map[string]interface{}

	// Version is the MVCC version
	Version uint64

	// CreatedAt is the creation timestamp
	CreatedAt int64

	// UpdatedAt is the last update timestamp
	UpdatedAt int64

	// Found indicates if the vector was found
	Found bool
}

// DeleteVectorRequest contains parameters for vector deletion.
type DeleteVectorRequest struct {
	// Namespace is the target namespace
	Namespace string

	// ID is the vector identifier
	ID uint64

	// ShardID is the target shard
	ShardID uint64
}

// DeleteVectorResponse contains the result of vector deletion.
type DeleteVectorResponse struct {
	// Success indicates if deletion succeeded
	Success bool

	// Timestamp is the deletion timestamp
	Timestamp int64
}

// BatchUpsertRequest contains parameters for batch vector upsert.
type BatchUpsertRequest struct {
	// Namespace is the target namespace
	Namespace string

	// Vectors contains the vectors to upsert
	Vectors []*VectorData

	// ShardID is the target shard
	ShardID uint64
}

// BatchUpsertResponse contains the result of batch upsert.
type BatchUpsertResponse struct {
	// IDs contains the vector identifiers
	IDs []uint64

	// Versions contains the MVCC versions
	Versions []uint64

	// Timestamp is the operation timestamp
	Timestamp int64

	// Success indicates if batch succeeded
	Success bool

	// InsertedCount is the number of vectors inserted
	InsertedCount int
}

// BatchDeleteVectorsRequest contains parameters for batch vector deletion.
type BatchDeleteVectorsRequest struct {
	// Namespace is the target namespace
	Namespace string

	// IDs contains the vector identifiers to delete
	IDs []uint64

	// ShardID is the target shard
	ShardID uint64
}

// BatchDeleteVectorsResponse contains the result of batch deletion.
type BatchDeleteVectorsResponse struct {
	// DeletedCount is the number of vectors deleted
	DeletedCount int

	// FailedIDs contains IDs that failed to delete (if any)
	FailedIDs []uint64

	// Timestamp is the deletion timestamp
	Timestamp int64

	// Success indicates if batch deletion succeeded
	Success bool
}

// ListVectorsRequest contains parameters for listing vectors in a namespace.
type ListVectorsRequest struct {
	// Namespace is the target namespace
	Namespace string

	// Limit restricts the number of results (default: 100, max: 1000)
	Limit int

	// Cursor is the pagination cursor from previous response
	Cursor string

	// IDPrefix filters vectors by ID prefix (for partitioned namespaces)
	IDPrefix string

	// IncludeVectors includes the embedding values in response
	IncludeVectors bool

	// IncludeMetadata includes metadata in response
	IncludeMetadata bool

	// ShardID is the target shard
	ShardID uint64

	// Consistency specifies read consistency level
	Consistency ConsistencyLevel
}

// ListVectorsResponse contains the list of vectors.
type ListVectorsResponse struct {
	// Vectors contains the vector data
	Vectors []*VectorData

	// NextCursor is the pagination cursor for the next page
	NextCursor string

	// TotalCount is the total number of vectors in namespace (approximate)
	TotalCount uint64

	// Timestamp is the query timestamp
	Timestamp int64
}

// UpdateVectorMetadataRequest contains parameters for updating vector metadata.
type UpdateVectorMetadataRequest struct {
	// Namespace is the target namespace
	Namespace string

	// ID is the vector identifier
	ID uint64

	// Metadata contains the new metadata values
	Metadata map[string]interface{}

	// MergeMetadata if true, merges with existing metadata; if false, replaces entirely
	MergeMetadata bool

	// DeleteKeys contains metadata keys to delete (only when MergeMetadata is true)
	DeleteKeys []string

	// ShardID is the target shard
	ShardID uint64
}

// UpdateVectorMetadataResponse contains the result of metadata update.
type UpdateVectorMetadataResponse struct {
	// ID is the vector identifier
	ID uint64

	// Version is the new MVCC version
	Version uint64

	// Timestamp is the update timestamp
	Timestamp int64

	// Success indicates if update succeeded
	Success bool
}

// VectorData represents a complete vector with metadata.
type VectorData struct {
	// ID is the vector identifier
	ID uint64

	// Vector contains the embedding values
	Vector []float32

	// Metadata contains user-defined attributes
	Metadata map[string]interface{}

	// Version is the MVCC version
	Version uint64

	// CreatedAt is the creation timestamp
	CreatedAt int64

	// UpdatedAt is the last update timestamp
	UpdatedAt int64
}

// =============================================================================
// Vector Service Types - Search Operations
// =============================================================================

// SearchSimilarRequest contains parameters for similarity search.
type SearchSimilarRequest struct {
	// Namespace is the target namespace
	Namespace string

	// QueryVector is the search query embedding
	QueryVector []float32

	// K is the number of nearest neighbors to return
	K int

	// EfSearch is the HNSW search parameter (higher = more accurate, slower)
	EfSearch int

	// MinScore filters results by minimum similarity score
	MinScore float32

	// IncludeVectors includes embedding values in results
	IncludeVectors bool

	// IncludeMetadata includes metadata in results
	IncludeMetadata bool

	// MetadataFilter filters results by metadata
	MetadataFilter map[string]interface{}

	// ShardID is the target shard
	ShardID uint64

	// Consistency specifies read consistency level
	Consistency ConsistencyLevel
}

// SearchSimilarResponse contains similarity search results.
type SearchSimilarResponse struct {
	// Results contains the matching vectors with scores
	Results []*VectorSearchResult

	// Timestamp is the search timestamp
	Timestamp int64

	// QueryDuration is the search duration in nanoseconds
	QueryDuration int64
}

// VectorSearchResult represents a single search result.
type VectorSearchResult struct {
	// ID is the vector identifier
	ID uint64

	// Score is the similarity score (higher = more similar)
	Score float32

	// Vector contains the embedding values (if requested)
	Vector []float32

	// Metadata contains user-defined attributes (if requested)
	Metadata map[string]interface{}

	// Distance is the distance metric value
	Distance float32

	// Rank is the result rank (1-based)
	Rank int
}

// SearchByMetadataRequest contains parameters for metadata-based search.
type SearchByMetadataRequest struct {
	// Namespace is the target namespace
	Namespace string

	// Filters contains metadata filter criteria
	Filters map[string]interface{}

	// FilterOperator specifies how to combine filters (AND/OR)
	FilterOperator string

	// Limit restricts the number of results
	Limit int

	// Offset is the pagination offset
	Offset int

	// IncludeVectors includes embedding values in results
	IncludeVectors bool

	// IncludeMetadata includes metadata in results
	IncludeMetadata bool

	// ShardID is the target shard
	ShardID uint64

	// Consistency specifies read consistency level
	Consistency ConsistencyLevel
}

// SearchByMetadataResponse contains metadata search results.
type SearchByMetadataResponse struct {
	// Results contains the matching vectors
	Results []*VectorData

	// TotalCount is the total number of matching vectors
	TotalCount uint64

	// Timestamp is the search timestamp
	Timestamp int64

	// NextCursor is the pagination cursor
	NextCursor string
}

// HybridSearchRequest combines similarity and metadata search.
type HybridSearchRequest struct {
	// Namespace is the target namespace
	Namespace string

	// QueryVector is the search query embedding
	QueryVector []float32

	// K is the number of nearest neighbors to return
	K int

	// EfSearch is the HNSW search parameter
	EfSearch int

	// MinScore filters results by minimum similarity score
	MinScore float32

	// MetadataFilter filters results by metadata
	MetadataFilter map[string]interface{}

	// FilterOperator specifies how to combine filters (AND/OR)
	FilterOperator string

	// RerankingAlgorithm specifies the reranking strategy
	RerankingAlgorithm string

	// IncludeVectors includes embedding values in results
	IncludeVectors bool

	// IncludeMetadata includes metadata in results
	IncludeMetadata bool

	// ShardID is the target shard
	ShardID uint64

	// Consistency specifies read consistency level
	Consistency ConsistencyLevel
}

// HybridSearchResponse contains hybrid search results.
type HybridSearchResponse struct {
	// Results contains the matching vectors with scores
	Results []*VectorSearchResult

	// TotalCandidates is the number of candidates before filtering
	TotalCandidates int

	// TotalFiltered is the number of results after filtering
	TotalFiltered int

	// Timestamp is the search timestamp
	Timestamp int64

	// QueryDuration is the search duration in nanoseconds
	QueryDuration int64
}

// SearchByIDRequest contains parameters for searching by vector ID.
type SearchByIDRequest struct {
	// Namespace is the target namespace
	Namespace string

	// ID is the source vector ID to use as query
	ID uint64

	// K is the number of nearest neighbors to return
	K int

	// EfSearch is the HNSW search parameter (higher = more accurate, slower)
	EfSearch int

	// MinScore filters results by minimum similarity score
	MinScore float32

	// IncludeVectors includes embedding values in results
	IncludeVectors bool

	// IncludeMetadata includes metadata in results
	IncludeMetadata bool

	// MetadataFilter filters results by metadata
	MetadataFilter map[string]interface{}

	// ExcludeSelf excludes the source vector from results (default: true)
	ExcludeSelf bool

	// ShardID is the target shard
	ShardID uint64

	// Consistency specifies read consistency level
	Consistency ConsistencyLevel
}

// SearchByIDResponse contains search by ID results.
type SearchByIDResponse struct {
	// SourceID is the ID of the query vector
	SourceID uint64

	// Results contains the matching vectors with scores
	Results []*VectorSearchResult

	// Timestamp is the search timestamp
	Timestamp int64

	// QueryDuration is the search duration in nanoseconds
	QueryDuration int64
}

// =============================================================================
// Vector Service Types - Index Management
// =============================================================================

// RebuildIndexRequest contains parameters for index rebuild.
type RebuildIndexRequest struct {
	// Namespace is the target namespace
	Namespace string

	// IndexConfig contains new index parameters (optional)
	IndexConfig map[string]string

	// Async indicates if rebuild should run asynchronously
	Async bool

	// ShardID is the target shard
	ShardID uint64
}

// RebuildIndexResponse contains the result of index rebuild.
type RebuildIndexResponse struct {
	// JobID is the rebuild job identifier (for async operations)
	JobID string

	// Status is the rebuild status
	Status string

	// VectorsProcessed is the number of vectors processed
	VectorsProcessed uint64

	// TotalVectors is the total number of vectors
	TotalVectors uint64

	// PercentComplete is the rebuild progress percentage
	PercentComplete float64

	// EstimatedTimeRemaining is the estimated time in seconds
	EstimatedTimeRemaining int64

	// Timestamp is the operation timestamp
	Timestamp int64

	// Success indicates if rebuild succeeded (for sync operations)
	Success bool
}

// GetRebuildProgressRequest contains parameters for checking rebuild progress.
type GetRebuildProgressRequest struct {
	// JobID is the rebuild job identifier (optional if Namespace is provided)
	JobID string

	// Namespace to check for rebuild progress (optional if JobID is provided)
	Namespace string
}

// GetRebuildProgressResponse contains the current rebuild progress.
type GetRebuildProgressResponse struct {
	// JobID is the rebuild job identifier
	JobID string

	// Namespace is the namespace being rebuilt
	Namespace string

	// Status is the rebuild status (pending, running, completed, failed)
	Status string

	// VectorsProcessed is the number of vectors processed so far
	VectorsProcessed uint64

	// TotalVectors is the total number of vectors to process
	TotalVectors uint64

	// PercentComplete is the rebuild progress percentage (0-100)
	PercentComplete float64

	// ElapsedTimeMs is the elapsed time in milliseconds
	ElapsedTimeMs int64

	// EstimatedTimeRemainingMs is the estimated time remaining in milliseconds
	EstimatedTimeRemainingMs int64

	// StartedAt is the Unix timestamp when rebuild started
	StartedAt int64

	// CompletedAt is the Unix timestamp when rebuild completed (0 if in progress)
	CompletedAt int64

	// Error contains error message if rebuild failed
	Error string
}

// GetIndexStatsRequest contains parameters for index statistics.
type GetIndexStatsRequest struct {
	// Namespace is the target namespace
	Namespace string

	// ShardID is the target shard
	ShardID uint64
}

// GetIndexStatsResponse contains index statistics.
type GetIndexStatsResponse struct {
	// Namespace is the namespace name
	Namespace string

	// IndexType is the index algorithm
	IndexType string

	// DistanceMetric is the distance metric the namespace was created with,
	// as GetNamespaceInfo reports it
	DistanceMetric string

	// VectorCount is the number of indexed vectors
	VectorCount uint64

	// Dimension is the vector dimensionality
	Dimension uint32

	// IndexSizeBytes is the index size in bytes
	IndexSizeBytes uint64

	// MemoryUsageBytes is the memory usage in bytes
	MemoryUsageBytes uint64

	// IndexConfig contains index parameters
	IndexConfig map[string]string

	// SearchPerformance contains search performance metrics
	SearchPerformance *IndexPerformanceMetrics

	// Timestamp is the query timestamp
	Timestamp int64
}

// IndexPerformanceMetrics contains index performance statistics.
type IndexPerformanceMetrics struct {
	// AverageSearchTimeMs is the average search time in milliseconds
	AverageSearchTimeMs float64

	// P50SearchTimeMs is the 50th percentile search time
	P50SearchTimeMs float64

	// P95SearchTimeMs is the 95th percentile search time
	P95SearchTimeMs float64

	// P99SearchTimeMs is the 99th percentile search time
	P99SearchTimeMs float64

	// TotalSearches is the total number of searches
	TotalSearches uint64

	// CacheHitRate is the cache hit rate percentage
	CacheHitRate float64
}

// =============================================================================
// Vector Service Types - Compaction Operations
// =============================================================================

// CompactNamespaceRequest contains parameters for namespace compaction.
type CompactNamespaceRequest struct {
	// Namespace is the target namespace to compact
	Namespace string

	// Async returns immediately if true, blocks until completion if false
	Async bool

	// TimeoutMs is the maximum time to wait (in milliseconds) for sync operations
	TimeoutMs int64

	// ShardID is the target shard
	ShardID uint64
}

// CompactNamespaceResponse contains the result of compaction.
type CompactNamespaceResponse struct {
	// OperationID uniquely identifies this compaction operation
	OperationID string

	// StartedAt is when the compaction started
	StartedAt int64

	// CompletedAt is when the compaction completed (nil if async/in-progress)
	CompletedAt *int64

	// BytesCompacted is the number of bytes freed
	BytesCompacted int64

	// Success indicates if sync compaction succeeded
	Success bool
}

// GetCompactionProgressRequest contains parameters for checking compaction progress.
type GetCompactionProgressRequest struct {
	// Namespace is the namespace being compacted
	Namespace string

	// OperationID is the compaction operation ID (optional)
	OperationID string

	// ShardID is the target shard
	ShardID uint64
}

// GetCompactionProgressResponse contains compaction progress information.
type GetCompactionProgressResponse struct {
	// OperationID is the compaction operation ID
	OperationID string

	// Namespace is the namespace being compacted
	Namespace string

	// BytesCompacted is the number of bytes compacted so far
	BytesCompacted int64

	// TotalBytes is the estimated total bytes to compact
	TotalBytes int64

	// PercentComplete is the completion percentage (0-100)
	PercentComplete float64

	// InProgress indicates if compaction is still running
	InProgress bool

	// StartedAt is when the compaction started
	StartedAt int64

	// CompletedAt is when the compaction completed (nil if in progress)
	CompletedAt *int64

	// Error contains any error message if compaction failed
	Error string
}
