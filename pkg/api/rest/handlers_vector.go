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

package rest

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Vector namespace request/response types
// ---------------------------------------------------------------------------

// vectorCreateNamespaceRequest is the JSON payload for POST /vector/namespace.
type vectorCreateNamespaceRequest struct {
	Namespace      string            `json:"namespace"`
	Dimension      uint32            `json:"dimension"`
	DistanceMetric string            `json:"distance_metric"`
	IndexType      string            `json:"index_type"`
	IndexConfig    map[string]string `json:"index_config,omitempty"`
	ShardID        uint64            `json:"shard_id,omitempty"`
}

// vectorCreateNamespaceResponse is the JSON payload returned by
// POST /vector/namespace.
type vectorCreateNamespaceResponse struct {
	Namespace string `json:"namespace"`
	Dimension uint32 `json:"dimension"`
	Timestamp int64  `json:"timestamp"`
	Success   bool   `json:"success"`
}

// vectorUpdateNamespaceRequest is the JSON body for POST
// /vector/namespace/update.
type vectorUpdateNamespaceRequest struct {
	Namespace   string                 `json:"namespace"`
	Dimension   uint32                 `json:"dimension,omitempty"`
	IndexConfig map[string]string      `json:"index_config,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// vectorUpdateNamespaceResponse is the JSON response for POST
// /vector/namespace/update.
type vectorUpdateNamespaceResponse struct {
	Namespace       string `json:"namespace"`
	RequiresRebuild bool   `json:"requires_rebuild"`
	Timestamp       int64  `json:"timestamp"`
	Success         bool   `json:"success"`
}

// vectorDeleteNamespaceRequest is the JSON payload for DELETE /vector/namespace.
type vectorDeleteNamespaceRequest struct {
	Namespace string `json:"namespace"`
	ShardID   uint64 `json:"shard_id,omitempty"`
}

// vectorDeleteNamespaceResponse is the JSON payload returned by
// DELETE /vector/namespace.
type vectorDeleteNamespaceResponse struct {
	Success        bool   `json:"success"`
	VectorsDeleted uint64 `json:"vectors_deleted"`
	Timestamp      int64  `json:"timestamp"`
}

// vectorNamespaceInfoJSON is the JSON representation of namespace info in lists.
type vectorNamespaceInfoJSON struct {
	Namespace      string            `json:"namespace"`
	Dimension      uint32            `json:"dimension"`
	VectorCount    uint64            `json:"vector_count"`
	DistanceMetric string            `json:"distance_metric"`
	IndexType      string            `json:"index_type"`
	IndexConfig    map[string]string `json:"index_config,omitempty"`
	CreatedAt      int64             `json:"created_at"`
	UpdatedAt      int64             `json:"updated_at"`
}

// vectorListNamespacesResponse is the JSON payload returned by
// GET /vector/namespaces.
type vectorListNamespacesResponse struct {
	Namespaces []vectorNamespaceInfoJSON `json:"namespaces"`
	TotalCount int                       `json:"total_count"`
	Timestamp  int64                     `json:"timestamp"`
}

// vectorNamespaceInfoResponse is the JSON payload returned by
// GET /vector/namespace/info.
type vectorNamespaceInfoResponse struct {
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

// ---------------------------------------------------------------------------
// Vector CRUD request/response types
// ---------------------------------------------------------------------------

// vectorUpsertRequest is the JSON payload for POST /vector/upsert.
type vectorUpsertRequest struct {
	Namespace string                 `json:"namespace"`
	ID        uint64                 `json:"id"`
	Vector    []float32              `json:"vector"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	ShardID   uint64                 `json:"shard_id,omitempty"`
}

// vectorUpsertResponse is the JSON payload returned by POST /vector/upsert.
type vectorUpsertResponse struct {
	ID        uint64 `json:"id"`
	Version   uint64 `json:"version"`
	Timestamp int64  `json:"timestamp"`
	Success   bool   `json:"success"`
	// Created is true when the ID was new, false when the upsert replaced a vector.
	Created bool `json:"created"`
}

// vectorDeleteRequest is the JSON payload for POST /vector/delete.
type vectorDeleteRequest struct {
	Namespace string `json:"namespace"`
	ID        uint64 `json:"id"`
	ShardID   uint64 `json:"shard_id,omitempty"`
}

// vectorDeleteResponse is the JSON payload returned by POST /vector/delete.
type vectorDeleteResponse struct {
	Success   bool  `json:"success"`
	Timestamp int64 `json:"timestamp"`
}

// vectorGetResponse is the JSON representation of a single vector get
// response, including the Found flag to indicate whether the vector exists.
type vectorGetResponse struct {
	ID        uint64                 `json:"id"`
	Found     bool                   `json:"found"`
	Vector    []float32              `json:"vector,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Version   uint64                 `json:"version"`
	CreatedAt int64                  `json:"created_at"`
	UpdatedAt int64                  `json:"updated_at"`
}

// vectorDataJSON is the JSON representation of a single vector.
type vectorDataJSON struct {
	ID        uint64                 `json:"id"`
	Vector    []float32              `json:"vector"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Version   uint64                 `json:"version"`
	CreatedAt int64                  `json:"created_at"`
	UpdatedAt int64                  `json:"updated_at"`
}

// vectorBatchUpsertRequest is the JSON payload for POST /vector/batch/upsert.
type vectorBatchUpsertRequest struct {
	Namespace string           `json:"namespace"`
	Vectors   []vectorDataJSON `json:"vectors"`
	ShardID   uint64           `json:"shard_id,omitempty"`
}

// vectorBatchUpsertResponse is the JSON payload returned by
// POST /vector/batch/upsert.
type vectorBatchUpsertResponse struct {
	IDs           []uint64 `json:"ids"`
	Versions      []uint64 `json:"versions"`
	Timestamp     int64    `json:"timestamp"`
	Success       bool     `json:"success"`
	InsertedCount int      `json:"inserted_count"`
}

// vectorBatchDeleteRequest is the JSON payload for POST /vector/batch/delete.
type vectorBatchDeleteRequest struct {
	Namespace string   `json:"namespace"`
	IDs       []uint64 `json:"ids"`
	ShardID   uint64   `json:"shard_id,omitempty"`
}

// vectorBatchDeleteResponse is the JSON payload returned by
// POST /vector/batch/delete.
type vectorBatchDeleteResponse struct {
	DeletedCount int      `json:"deleted_count"`
	FailedIDs    []uint64 `json:"failed_ids,omitempty"`
	Timestamp    int64    `json:"timestamp"`
	Success      bool     `json:"success"`
}

// vectorListResponse is the JSON payload returned by GET /vector/list.
type vectorListResponse struct {
	Vectors    []vectorDataJSON `json:"vectors"`
	NextCursor string           `json:"next_cursor,omitempty"`
	TotalCount uint64           `json:"total_count"`
	Timestamp  int64            `json:"timestamp"`
}

// vectorUpdateMetadataRequest is the JSON payload for POST /vector/metadata.
type vectorUpdateMetadataRequest struct {
	Namespace string                 `json:"namespace"`
	ID        uint64                 `json:"id"`
	Metadata  map[string]interface{} `json:"metadata"`
	ShardID   uint64                 `json:"shard_id,omitempty"`
}

// vectorUpdateMetadataResponse is the JSON payload returned by
// POST /vector/metadata.
type vectorUpdateMetadataResponse struct {
	ID        uint64 `json:"id"`
	Version   uint64 `json:"version"`
	Timestamp int64  `json:"timestamp"`
	Success   bool   `json:"success"`
}

// ---------------------------------------------------------------------------
// Vector search request/response types
// ---------------------------------------------------------------------------

// vectorSearchResultJSON is the JSON representation of a search result.
type vectorSearchResultJSON struct {
	ID       uint64                 `json:"id"`
	Score    float32                `json:"score"`
	Vector   []float32              `json:"vector,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Distance float32                `json:"distance"`
	Rank     int                    `json:"rank"`
}

// vectorSearchSimilarRequest is the JSON payload for
// POST /vector/search/similar.
type vectorSearchSimilarRequest struct {
	Namespace   string    `json:"namespace"`
	QueryVector []float32 `json:"query_vector"`
	K           int       `json:"k"`
	EfSearch    int       `json:"ef_search,omitempty"`
	MinScore    float32   `json:"min_score,omitempty"`
	Consistency uint8     `json:"consistency,omitempty"`
	ShardID     uint64    `json:"shard_id,omitempty"`
}

// vectorSearchSimilarResponse is the JSON payload returned by
// POST /vector/search/similar and POST /vector/search/by-id.
type vectorSearchSimilarResponse struct {
	Results       []vectorSearchResultJSON `json:"results"`
	Timestamp     int64                    `json:"timestamp"`
	QueryDuration int64                    `json:"query_duration"`
}

// vectorSearchByMetadataRequest is the JSON payload for
// POST /vector/search/metadata.
type vectorSearchByMetadataRequest struct {
	Namespace   string                 `json:"namespace"`
	Filters     map[string]interface{} `json:"filters"`
	Limit       int                    `json:"limit,omitempty"`
	Offset      int                    `json:"offset,omitempty"`
	Consistency uint8                  `json:"consistency,omitempty"`
	ShardID     uint64                 `json:"shard_id,omitempty"`
}

// vectorSearchByMetadataResponse is the JSON payload returned by
// POST /vector/search/metadata.
type vectorSearchByMetadataResponse struct {
	Results    []vectorDataJSON `json:"results"`
	TotalCount uint64           `json:"total_count"`
	Timestamp  int64            `json:"timestamp"`
}

// vectorHybridSearchRequest is the JSON payload for
// POST /vector/search/hybrid.
type vectorHybridSearchRequest struct {
	Namespace      string                 `json:"namespace"`
	QueryVector    []float32              `json:"query_vector"`
	K              int                    `json:"k"`
	EfSearch       int                    `json:"ef_search,omitempty"`
	MinScore       float32                `json:"min_score,omitempty"`
	MetadataFilter map[string]interface{} `json:"metadata_filter"`
	Consistency    uint8                  `json:"consistency,omitempty"`
	ShardID        uint64                 `json:"shard_id,omitempty"`
}

// vectorHybridSearchResponse is the JSON payload returned by
// POST /vector/search/hybrid.
type vectorHybridSearchResponse struct {
	Results         []vectorSearchResultJSON `json:"results"`
	TotalCandidates int                      `json:"total_candidates"`
	TotalFiltered   int                      `json:"total_filtered"`
	Timestamp       int64                    `json:"timestamp"`
	QueryDuration   int64                    `json:"query_duration"`
}

// vectorSearchByIDRequest is the JSON payload for POST /vector/search/by-id.
type vectorSearchByIDRequest struct {
	Namespace   string `json:"namespace"`
	ID          uint64 `json:"id"`
	K           int    `json:"k"`
	Consistency uint8  `json:"consistency,omitempty"`
	ShardID     uint64 `json:"shard_id,omitempty"`
}

// ---------------------------------------------------------------------------
// Vector index management request/response types
// ---------------------------------------------------------------------------

// vectorRebuildIndexRequest is the JSON payload for
// POST /vector/index/rebuild.
type vectorRebuildIndexRequest struct {
	Namespace string `json:"namespace"`
	ShardID   uint64 `json:"shard_id,omitempty"`
}

// vectorRebuildIndexResponse is the JSON payload returned by
// POST /vector/index/rebuild.
type vectorRebuildIndexResponse struct {
	JobID                  string  `json:"job_id"`
	Status                 string  `json:"status"`
	VectorsProcessed       uint64  `json:"vectors_processed"`
	TotalVectors           uint64  `json:"total_vectors"`
	PercentComplete        float64 `json:"percent_complete"`
	EstimatedTimeRemaining int64   `json:"estimated_time_remaining"`
	Timestamp              int64   `json:"timestamp"`
	Success                bool    `json:"success"`
}

// vectorRebuildProgressResponse is the JSON payload returned by
// GET /vector/index/rebuild/progress.
type vectorRebuildProgressResponse struct {
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

// vectorIndexStatsResponse is the JSON payload returned by
// GET /vector/index/stats.
type vectorIndexStatsResponse struct {
	Namespace        string            `json:"namespace"`
	IndexType        string            `json:"index_type"`
	DistanceMetric   string            `json:"distance_metric"`
	VectorCount      uint64            `json:"vector_count"`
	Dimension        uint32            `json:"dimension"`
	IndexSizeBytes   uint64            `json:"index_size_bytes"`
	MemoryUsageBytes uint64            `json:"memory_usage_bytes"`
	IndexConfig      map[string]string `json:"index_config,omitempty"`
	Timestamp        int64             `json:"timestamp"`
}

// ---------------------------------------------------------------------------
// Vector compaction request/response types
// ---------------------------------------------------------------------------

// vectorCompactRequest is the JSON payload for POST /vector/compact.
type vectorCompactRequest struct {
	Namespace string `json:"namespace"`
	ShardID   uint64 `json:"shard_id,omitempty"`
}

// vectorCompactResponse is the JSON payload returned by
// POST /vector/compact.
type vectorCompactResponse struct {
	OperationID    string `json:"operation_id"`
	StartedAt      int64  `json:"started_at"`
	CompletedAt    *int64 `json:"completed_at,omitempty"`
	BytesCompacted int64  `json:"bytes_compacted"`
	Success        bool   `json:"success"`
}

// vectorCompactionProgressResponse is the JSON payload returned by
// GET /vector/compact/progress.
type vectorCompactionProgressResponse struct {
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

// ---------------------------------------------------------------------------
// Vector namespace handlers
// ---------------------------------------------------------------------------

// handleVectorCreateNamespace handles POST /vector/namespace -- creates a
// new vector namespace with the specified dimensionality, distance metric,
// and index configuration.
func (s *Server) handleVectorCreateNamespace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	var req vectorCreateNamespaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	resp, err := vectorSvc.CreateNamespace(ctx, &service.CreateNamespaceRequest{
		Namespace:      req.Namespace,
		Dimension:      req.Dimension,
		DistanceMetric: req.DistanceMetric,
		IndexType:      req.IndexType,
		IndexConfig:    req.IndexConfig,
		ShardID:        req.ShardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorCreateNamespaceResponse{
		Namespace: resp.Namespace,
		Dimension: resp.Dimension,
		Timestamp: resp.Timestamp,
		Success:   resp.Success,
	})
}

// handleVectorUpdateNamespace handles POST /vector/namespace/update: it changes
// a namespace's index parameters and metadata in place, keeping every vector.
func (s *Server) handleVectorUpdateNamespace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	var req vectorUpdateNamespaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	resp, err := vectorSvc.UpdateNamespace(ctx, &service.UpdateNamespaceRequest{
		Namespace:   req.Namespace,
		Dimension:   req.Dimension,
		IndexConfig: req.IndexConfig,
		Metadata:    req.Metadata,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorUpdateNamespaceResponse{
		Namespace:       resp.Namespace,
		RequiresRebuild: resp.RequiresRebuild,
		Timestamp:       resp.Timestamp,
		Success:         resp.Success,
	})
}

// handleVectorDeleteNamespace handles DELETE /vector/namespace -- removes
// a namespace and all its vectors.
func (s *Server) handleVectorDeleteNamespace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	var req vectorDeleteNamespaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	resp, err := vectorSvc.DeleteNamespace(ctx, &service.DeleteNamespaceRequest{
		Namespace: req.Namespace,
		ShardID:   req.ShardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorDeleteNamespaceResponse{
		Success:        resp.Success,
		VectorsDeleted: resp.VectorsDeleted,
		Timestamp:      resp.Timestamp,
	})
}

// handleVectorListNamespaces handles GET /vector/namespaces -- returns all
// vector namespace names.
// Query parameters:
//   - shard_id: target shard (optional)
func (s *Server) handleVectorListNamespaces(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	q := r.URL.Query()

	var shardID uint64
	if shardIDStr := q.Get("shard_id"); shardIDStr != "" {
		var parseErr error
		shardID, parseErr = strconv.ParseUint(shardIDStr, 10, 64)
		if parseErr != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "shard_id", Err: parseErr})
			return
		}
	}

	resp, err := vectorSvc.ListNamespaces(ctx, &service.ListNamespacesRequest{
		ShardID: shardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorListNamespacesResponse{
		Namespaces: namespaceInfoSliceToJSON(resp.Namespaces),
		TotalCount: resp.TotalCount,
		Timestamp:  resp.Timestamp,
	})
}

// handleVectorGetNamespaceInfo handles GET /vector/namespace/info -- returns
// metadata about a namespace including dimension, distance metric, index
// type, and vector count.
// Query parameters:
//   - namespace: namespace name (required)
//   - shard_id: target shard (optional)
func (s *Server) handleVectorGetNamespaceInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	q := r.URL.Query()

	namespace := q.Get("namespace")
	if namespace == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "namespace"})
		return
	}

	var shardID uint64
	if shardIDStr := q.Get("shard_id"); shardIDStr != "" {
		var parseErr error
		shardID, parseErr = strconv.ParseUint(shardIDStr, 10, 64)
		if parseErr != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "shard_id", Err: parseErr})
			return
		}
	}

	info, err := vectorSvc.GetNamespaceInfo(ctx, &service.GetNamespaceInfoRequest{
		Namespace: namespace,
		ShardID:   shardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorNamespaceInfoResponse{
		Namespace:      info.Namespace,
		Dimension:      info.Dimension,
		VectorCount:    info.VectorCount,
		DistanceMetric: info.DistanceMetric,
		IndexType:      info.IndexType,
		IndexConfig:    info.IndexConfig,
		Metadata:       info.Metadata,
		CreatedAt:      info.CreatedAt,
		UpdatedAt:      info.UpdatedAt,
	})
}

// ---------------------------------------------------------------------------
// Vector CRUD handlers
// ---------------------------------------------------------------------------

// handleVectorUpsert handles POST /vector/upsert -- inserts or updates a
// vector in the specified namespace.
func (s *Server) handleVectorUpsert(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	var req vectorUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	resp, err := vectorSvc.UpsertVector(ctx, &service.UpsertVectorRequest{
		Namespace: req.Namespace,
		ID:        req.ID,
		Vector:    req.Vector,
		Metadata:  req.Metadata,
		ShardID:   req.ShardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorUpsertResponse{
		ID:        resp.ID,
		Version:   resp.Version,
		Timestamp: resp.Timestamp,
		Success:   resp.Success,
		Created:   resp.Created,
	})
}

// handleVectorGet handles GET /vector/get -- retrieves a single vector by
// ID from the specified namespace.
// Query parameters:
//   - namespace: namespace name (required)
//   - id: vector identifier (required)
//   - consistency: read consistency level (optional, default 0 = strong)
//   - shard_id: target shard (optional)
func (s *Server) handleVectorGet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	q := r.URL.Query()

	namespace := q.Get("namespace")
	if namespace == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "namespace"})
		return
	}

	idStr := q.Get("id")
	if idStr == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "id"})
		return
	}
	id, parseErr := strconv.ParseUint(idStr, 10, 64)
	if parseErr != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "id", Err: parseErr})
		return
	}

	var consistency service.ConsistencyLevel
	if consistencyStr := q.Get("consistency"); consistencyStr != "" {
		val, cErr := strconv.ParseUint(consistencyStr, 10, 8)
		if cErr != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "consistency", Err: cErr})
			return
		}
		consistency = service.ConsistencyLevel(val)
	}

	var shardID uint64
	if shardIDStr := q.Get("shard_id"); shardIDStr != "" {
		var sErr error
		shardID, sErr = strconv.ParseUint(shardIDStr, 10, 64)
		if sErr != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "shard_id", Err: sErr})
			return
		}
	}

	resp, err := vectorSvc.GetVector(ctx, &service.GetVectorRequest{
		Namespace:       namespace,
		ID:              id,
		IncludeVector:   q.Get("include_vector") != "false",
		IncludeMetadata: q.Get("include_metadata") != "false",
		Consistency:     consistency,
		ShardID:         shardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorGetResponse{
		ID:        resp.ID,
		Found:     resp.Found,
		Vector:    resp.Vector,
		Metadata:  resp.Metadata,
		Version:   resp.Version,
		CreatedAt: resp.CreatedAt,
		UpdatedAt: resp.UpdatedAt,
	})
}

// handleVectorDelete handles POST /vector/delete -- removes a single vector
// by ID from the specified namespace.
func (s *Server) handleVectorDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	var req vectorDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	resp, err := vectorSvc.DeleteVector(ctx, &service.DeleteVectorRequest{
		Namespace: req.Namespace,
		ID:        req.ID,
		ShardID:   req.ShardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorDeleteResponse{
		Success:   resp.Success,
		Timestamp: resp.Timestamp,
	})
}

// handleVectorBatchUpsert handles POST /vector/batch/upsert -- inserts or
// updates multiple vectors in a single operation.
func (s *Server) handleVectorBatchUpsert(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	var req vectorBatchUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	vectors := make([]*service.VectorData, len(req.Vectors))
	for i, v := range req.Vectors {
		vectors[i] = &service.VectorData{
			ID:        v.ID,
			Vector:    v.Vector,
			Metadata:  v.Metadata,
			Version:   v.Version,
			CreatedAt: v.CreatedAt,
			UpdatedAt: v.UpdatedAt,
		}
	}

	resp, err := vectorSvc.BatchUpsert(ctx, &service.BatchUpsertRequest{
		Namespace: req.Namespace,
		Vectors:   vectors,
		ShardID:   req.ShardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorBatchUpsertResponse{
		IDs:           resp.IDs,
		Versions:      resp.Versions,
		Timestamp:     resp.Timestamp,
		Success:       resp.Success,
		InsertedCount: resp.InsertedCount,
	})
}

// handleVectorBatchDeleteVectors handles POST /vector/batch/delete --
// removes multiple vectors by ID in a single operation.
func (s *Server) handleVectorBatchDeleteVectors(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	var req vectorBatchDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	resp, err := vectorSvc.BatchDeleteVectors(ctx, &service.BatchDeleteVectorsRequest{
		Namespace: req.Namespace,
		IDs:       req.IDs,
		ShardID:   req.ShardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorBatchDeleteResponse{
		DeletedCount: resp.DeletedCount,
		FailedIDs:    resp.FailedIDs,
		Timestamp:    resp.Timestamp,
		Success:      resp.Success,
	})
}

// handleVectorListVectors handles GET /vector/list -- returns a paginated
// list of vectors in a namespace.
// Query parameters:
//   - namespace: namespace name (required)
//   - limit: maximum number of vectors to return (optional)
//   - cursor: opaque pagination cursor (optional)
//   - shard_id: target shard (optional)
func (s *Server) handleVectorListVectors(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	q := r.URL.Query()

	namespace := q.Get("namespace")
	if namespace == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "namespace"})
		return
	}

	var limit int
	if limitStr := q.Get("limit"); limitStr != "" {
		var parseErr error
		limit, parseErr = strconv.Atoi(limitStr)
		if parseErr != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "limit", Err: parseErr})
			return
		}
	}

	cursor := q.Get("cursor")

	var shardID uint64
	if shardIDStr := q.Get("shard_id"); shardIDStr != "" {
		var parseErr error
		shardID, parseErr = strconv.ParseUint(shardIDStr, 10, 64)
		if parseErr != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "shard_id", Err: parseErr})
			return
		}
	}

	resp, err := vectorSvc.ListVectors(ctx, &service.ListVectorsRequest{
		Namespace: namespace,
		Limit:     limit,
		Cursor:    cursor,
		ShardID:   shardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorListResponse{
		Vectors:    vectorDataPtrSliceToJSON(resp.Vectors),
		NextCursor: resp.NextCursor,
		TotalCount: resp.TotalCount,
		Timestamp:  resp.Timestamp,
	})
}

// handleVectorUpdateMetadata handles POST /vector/metadata -- updates the
// metadata for an existing vector without modifying the vector data itself.
func (s *Server) handleVectorUpdateMetadata(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	var req vectorUpdateMetadataRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	resp, err := vectorSvc.UpdateMetadata(ctx, &service.UpdateVectorMetadataRequest{
		Namespace: req.Namespace,
		ID:        req.ID,
		Metadata:  req.Metadata,
		ShardID:   req.ShardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorUpdateMetadataResponse{
		ID:        resp.ID,
		Version:   resp.Version,
		Timestamp: resp.Timestamp,
		Success:   resp.Success,
	})
}

// ---------------------------------------------------------------------------
// Vector search handlers
// ---------------------------------------------------------------------------

// handleVectorSearchSimilar handles POST /vector/search/similar -- performs
// a k-nearest-neighbor search using a query vector.
func (s *Server) handleVectorSearchSimilar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	var req vectorSearchSimilarRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	resp, err := vectorSvc.SearchSimilar(ctx, &service.SearchSimilarRequest{
		Namespace:   req.Namespace,
		QueryVector: req.QueryVector,
		K:           req.K,
		EfSearch:    req.EfSearch,
		MinScore:    req.MinScore,
		Consistency: service.ConsistencyLevel(req.Consistency),
		ShardID:     req.ShardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorSearchSimilarResponse{
		Results:       searchResultPtrsToJSON(resp.Results),
		Timestamp:     resp.Timestamp,
		QueryDuration: resp.QueryDuration,
	})
}

// handleVectorSearchByMetadata handles POST /vector/search/metadata --
// returns vectors matching the given metadata filters.
func (s *Server) handleVectorSearchByMetadata(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	var req vectorSearchByMetadataRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	resp, err := vectorSvc.SearchByMetadata(ctx, &service.SearchByMetadataRequest{
		Namespace:   req.Namespace,
		Filters:     req.Filters,
		Limit:       req.Limit,
		Offset:      req.Offset,
		Consistency: service.ConsistencyLevel(req.Consistency),
		ShardID:     req.ShardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorSearchByMetadataResponse{
		Results:    vectorDataPtrSliceToJSON(resp.Results),
		TotalCount: resp.TotalCount,
		Timestamp:  resp.Timestamp,
	})
}

// handleVectorHybridSearch handles POST /vector/search/hybrid -- combines
// vector similarity and metadata filtering with configurable weights for
// each scoring component.
func (s *Server) handleVectorHybridSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	var req vectorHybridSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	resp, err := vectorSvc.HybridSearch(ctx, &service.HybridSearchRequest{
		Namespace:      req.Namespace,
		QueryVector:    req.QueryVector,
		K:              req.K,
		EfSearch:       req.EfSearch,
		MinScore:       req.MinScore,
		MetadataFilter: req.MetadataFilter,
		Consistency:    service.ConsistencyLevel(req.Consistency),
		ShardID:        req.ShardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorHybridSearchResponse{
		Results:         searchResultPtrsToJSON(resp.Results),
		TotalCandidates: resp.TotalCandidates,
		TotalFiltered:   resp.TotalFiltered,
		Timestamp:       resp.Timestamp,
		QueryDuration:   resp.QueryDuration,
	})
}

// handleVectorSearchByID handles POST /vector/search/by-id -- performs a
// k-nearest-neighbor search using an existing vector's ID as the query,
// returning its most similar neighbors.
func (s *Server) handleVectorSearchByID(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	var req vectorSearchByIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	resp, err := vectorSvc.SearchByID(ctx, &service.SearchByIDRequest{
		Namespace:   req.Namespace,
		ID:          req.ID,
		K:           req.K,
		Consistency: service.ConsistencyLevel(req.Consistency),
		ShardID:     req.ShardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorSearchSimilarResponse{
		Results:       searchResultPtrsToJSON(resp.Results),
		Timestamp:     resp.Timestamp,
		QueryDuration: resp.QueryDuration,
	})
}

// ---------------------------------------------------------------------------
// Vector index management handlers
// ---------------------------------------------------------------------------

// handleVectorRebuildIndex handles POST /vector/index/rebuild -- triggers
// an asynchronous index rebuild for a namespace.
func (s *Server) handleVectorRebuildIndex(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	var req vectorRebuildIndexRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	resp, err := vectorSvc.RebuildIndex(ctx, &service.RebuildIndexRequest{
		Namespace: req.Namespace,
		ShardID:   req.ShardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorRebuildIndexResponse{
		JobID:                  resp.JobID,
		Status:                 resp.Status,
		VectorsProcessed:       resp.VectorsProcessed,
		TotalVectors:           resp.TotalVectors,
		PercentComplete:        resp.PercentComplete,
		EstimatedTimeRemaining: resp.EstimatedTimeRemaining,
		Timestamp:              resp.Timestamp,
		Success:                resp.Success,
	})
}

// handleVectorGetRebuildProgress handles GET /vector/index/rebuild/progress
// -- returns the current progress of an index rebuild job.
// Query parameters:
//   - namespace: namespace name (required)
//   - job_id: rebuild job identifier (required)
//   - shard_id: target shard (optional)
func (s *Server) handleVectorGetRebuildProgress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	q := r.URL.Query()

	namespace := q.Get("namespace")
	if namespace == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "namespace"})
		return
	}

	jobID := q.Get("job_id")
	if jobID == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "job_id"})
		return
	}

	progress, err := vectorSvc.GetRebuildProgress(ctx, &service.GetRebuildProgressRequest{
		Namespace: namespace,
		JobID:     jobID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorRebuildProgressResponse{
		JobID:                    progress.JobID,
		Namespace:                progress.Namespace,
		Status:                   progress.Status,
		VectorsProcessed:         progress.VectorsProcessed,
		TotalVectors:             progress.TotalVectors,
		PercentComplete:          progress.PercentComplete,
		ElapsedTimeMs:            progress.ElapsedTimeMs,
		EstimatedTimeRemainingMs: progress.EstimatedTimeRemainingMs,
		StartedAt:                progress.StartedAt,
		CompletedAt:              progress.CompletedAt,
		Error:                    progress.Error,
	})
}

// handleVectorGetIndexStats handles GET /vector/index/stats -- returns
// statistics about a namespace's vector index including vector count,
// memory usage, and disk usage.
// Query parameters:
//   - namespace: namespace name (required)
//   - shard_id: target shard (optional)
func (s *Server) handleVectorGetIndexStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	q := r.URL.Query()

	namespace := q.Get("namespace")
	if namespace == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "namespace"})
		return
	}

	var shardID uint64
	if shardIDStr := q.Get("shard_id"); shardIDStr != "" {
		var parseErr error
		shardID, parseErr = strconv.ParseUint(shardIDStr, 10, 64)
		if parseErr != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "shard_id", Err: parseErr})
			return
		}
	}

	stats, err := vectorSvc.GetIndexStats(ctx, &service.GetIndexStatsRequest{
		Namespace: namespace,
		ShardID:   shardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorIndexStatsResponse{
		Namespace:        stats.Namespace,
		IndexType:        stats.IndexType,
		DistanceMetric:   stats.DistanceMetric,
		VectorCount:      stats.VectorCount,
		Dimension:        stats.Dimension,
		IndexSizeBytes:   stats.IndexSizeBytes,
		MemoryUsageBytes: stats.MemoryUsageBytes,
		IndexConfig:      stats.IndexConfig,
		Timestamp:        stats.Timestamp,
	})
}

// ---------------------------------------------------------------------------
// Vector compaction handlers
// ---------------------------------------------------------------------------

// handleVectorCompactNamespace handles POST /vector/compact -- triggers an
// asynchronous compaction of a namespace's storage to reclaim space from
// deleted vectors.
func (s *Server) handleVectorCompactNamespace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	var req vectorCompactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	resp, err := vectorSvc.CompactNamespace(ctx, &service.CompactNamespaceRequest{
		Namespace: req.Namespace,
		ShardID:   req.ShardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorCompactResponse{
		OperationID:    resp.OperationID,
		StartedAt:      resp.StartedAt,
		CompletedAt:    resp.CompletedAt,
		BytesCompacted: resp.BytesCompacted,
		Success:        resp.Success,
	})
}

// handleVectorGetCompactionProgress handles GET /vector/compact/progress --
// returns the current progress of a compaction job.
// Query parameters:
//   - namespace: namespace name (required)
//   - operation_id: compaction operation identifier (required)
//   - shard_id: target shard (optional)
//
// NOTE: this previously read a "job_id" query parameter -- a copy-paste
// holdover from the sibling GetRebuildProgress handler's JobID field --
// which never matched the "operation_id" the SDK REST client
// (sdk/go/transport/rest/transport_vector.go GetCompactionProgress) and the
// CLI (`vector compact-progress --operation-id`) have always sent, and never
// matched transport.GetCompactionProgressRequest/Response's OperationID
// field or the JSON this same handler emits ("operation_id"). Every REST
// call to this endpoint therefore failed closed with "missing required
// parameter: job_id", exclusively on this one transport (gRPC, QUIC, Unix,
// and MCP all key off the request's actual OperationID field). Renamed to
// match the DTO and every other transport.
func (s *Server) handleVectorGetCompactionProgress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vectorSvc := s.vectorSvcFor(w, ctx)
	if vectorSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "vector"})
		}
		return
	}

	q := r.URL.Query()

	namespace := q.Get("namespace")
	if namespace == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "namespace"})
		return
	}

	jobID := q.Get("operation_id")
	if jobID == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "operation_id"})
		return
	}

	var shardID uint64
	if shardIDStr := q.Get("shard_id"); shardIDStr != "" {
		var parseErr error
		shardID, parseErr = strconv.ParseUint(shardIDStr, 10, 64)
		if parseErr != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "shard_id", Err: parseErr})
			return
		}
	}

	progress, err := vectorSvc.GetCompactionProgress(ctx, &service.GetCompactionProgressRequest{
		Namespace:   namespace,
		OperationID: jobID,
		ShardID:     shardID,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, vectorCompactionProgressResponse{
		OperationID:     progress.OperationID,
		Namespace:       progress.Namespace,
		BytesCompacted:  progress.BytesCompacted,
		TotalBytes:      progress.TotalBytes,
		PercentComplete: progress.PercentComplete,
		InProgress:      progress.InProgress,
		StartedAt:       progress.StartedAt,
		CompletedAt:     progress.CompletedAt,
		Error:           progress.Error,
	})
}

// ---------------------------------------------------------------------------
// Vector JSON conversion helpers
// ---------------------------------------------------------------------------

// vectorDataPtrSliceToJSON converts a slice of *service.VectorData to JSON format.
func vectorDataPtrSliceToJSON(vectors []*service.VectorData) []vectorDataJSON {
	result := make([]vectorDataJSON, len(vectors))
	for i, v := range vectors {
		if v == nil {
			continue
		}
		result[i] = vectorDataJSON{
			ID:        v.ID,
			Vector:    v.Vector,
			Metadata:  v.Metadata,
			Version:   v.Version,
			CreatedAt: v.CreatedAt,
			UpdatedAt: v.UpdatedAt,
		}
	}
	return result
}

// searchResultPtrsToJSON converts a slice of *service.VectorSearchResult to
// JSON format.
func searchResultPtrsToJSON(results []*service.VectorSearchResult) []vectorSearchResultJSON {
	resp := make([]vectorSearchResultJSON, len(results))
	for i, r := range results {
		if r == nil {
			continue
		}
		resp[i] = vectorSearchResultJSON{
			ID:       r.ID,
			Score:    r.Score,
			Vector:   r.Vector,
			Metadata: r.Metadata,
			Distance: r.Distance,
			Rank:     r.Rank,
		}
	}
	return resp
}

// namespaceInfoSliceToJSON converts a slice of *service.NamespaceInfo to JSON format.
func namespaceInfoSliceToJSON(namespaces []*service.NamespaceInfo) []vectorNamespaceInfoJSON {
	result := make([]vectorNamespaceInfoJSON, len(namespaces))
	for i, ns := range namespaces {
		if ns == nil {
			continue
		}
		result[i] = vectorNamespaceInfoJSON{
			Namespace:      ns.Namespace,
			Dimension:      ns.Dimension,
			VectorCount:    ns.VectorCount,
			DistanceMetric: ns.DistanceMetric,
			IndexType:      ns.IndexType,
			IndexConfig:    ns.IndexConfig,
			CreatedAt:      ns.CreatedAt,
			UpdatedAt:      ns.UpdatedAt,
		}
	}
	return result
}
