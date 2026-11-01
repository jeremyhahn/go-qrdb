// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mcp

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.VectorClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Server-side JSON decode types for vector responses
// ---------------------------------------------------------------------------
//
// The MCP server serializes service-layer types using json.Marshal with
// Go's default PascalCase field names (the service structs have no json
// tags). These intermediate types decode the server's PascalCase JSON
// and are then mapped to the SDK transport response types which use
// snake_case json tags.

// serverCreateNamespaceResponse mirrors service.CreateNamespaceResponse.
type serverCreateNamespaceResponse struct {
	Namespace string `json:"Namespace"`
	Dimension uint32 `json:"Dimension"`
	Timestamp int64  `json:"Timestamp"`
	Success   bool   `json:"Success"`
}

// serverUpdateNamespaceResponse mirrors service.UpdateNamespaceResponse.
type serverUpdateNamespaceResponse struct {
	Namespace       string `json:"Namespace"`
	RequiresRebuild bool   `json:"RequiresRebuild"`
	Timestamp       int64  `json:"Timestamp"`
	Success         bool   `json:"Success"`
}

// serverDeleteNamespaceResponse mirrors service.DeleteNamespaceResponse.
type serverDeleteNamespaceResponse struct {
	Success        bool   `json:"Success"`
	VectorsDeleted uint64 `json:"VectorsDeleted"`
	Timestamp      int64  `json:"Timestamp"`
}

// serverNamespaceInfo mirrors service.NamespaceInfo.
type serverNamespaceInfo struct {
	Namespace      string            `json:"Namespace"`
	Dimension      uint32            `json:"Dimension"`
	VectorCount    uint64            `json:"VectorCount"`
	DistanceMetric string            `json:"DistanceMetric"`
	IndexType      string            `json:"IndexType"`
	IndexConfig    map[string]string `json:"IndexConfig,omitempty"`
	Metadata       map[string]string `json:"Metadata,omitempty"`
	CreatedAt      int64             `json:"CreatedAt"`
	UpdatedAt      int64             `json:"UpdatedAt"`
}

// serverListNamespacesResponse mirrors service.ListNamespacesResponse.
type serverListNamespacesResponse struct {
	Namespaces []*serverNamespaceInfo `json:"Namespaces"`
	TotalCount int                    `json:"TotalCount"`
	Timestamp  int64                  `json:"Timestamp"`
}

// serverGetNamespaceInfoResponse mirrors service.GetNamespaceInfoResponse.
type serverGetNamespaceInfoResponse struct {
	Namespace      string                 `json:"Namespace"`
	Dimension      uint32                 `json:"Dimension"`
	VectorCount    uint64                 `json:"VectorCount"`
	DistanceMetric string                 `json:"DistanceMetric"`
	IndexType      string                 `json:"IndexType"`
	IndexConfig    map[string]string      `json:"IndexConfig,omitempty"`
	Metadata       map[string]interface{} `json:"Metadata,omitempty"`
	CreatedAt      int64                  `json:"CreatedAt"`
	UpdatedAt      int64                  `json:"UpdatedAt"`
}

// serverUpsertVectorResponse mirrors service.UpsertVectorResponse.
type serverUpsertVectorResponse struct {
	ID        uint64 `json:"ID"`
	Version   uint64 `json:"Version"`
	Timestamp int64  `json:"Timestamp"`
	Success   bool   `json:"Success"`
}

// serverGetVectorResponse mirrors service.GetVectorResponse.
type serverGetVectorResponse struct {
	ID        uint64                 `json:"ID"`
	Vector    []float32              `json:"Vector,omitempty"`
	Metadata  map[string]interface{} `json:"Metadata,omitempty"`
	Version   uint64                 `json:"Version"`
	CreatedAt int64                  `json:"CreatedAt"`
	UpdatedAt int64                  `json:"UpdatedAt"`
	Found     bool                   `json:"Found"`
}

// serverDeleteVectorResponse mirrors service.DeleteVectorResponse.
type serverDeleteVectorResponse struct {
	Success   bool  `json:"Success"`
	Timestamp int64 `json:"Timestamp"`
}

// serverVectorData mirrors service.VectorData.
type serverVectorData struct {
	ID        uint64                 `json:"ID"`
	Vector    []float32              `json:"Vector,omitempty"`
	Metadata  map[string]interface{} `json:"Metadata,omitempty"`
	Version   uint64                 `json:"Version"`
	CreatedAt int64                  `json:"CreatedAt"`
	UpdatedAt int64                  `json:"UpdatedAt"`
}

// serverBatchUpsertResponse mirrors service.BatchUpsertResponse.
type serverBatchUpsertResponse struct {
	IDs           []uint64 `json:"IDs"`
	Versions      []uint64 `json:"Versions"`
	Timestamp     int64    `json:"Timestamp"`
	Success       bool     `json:"Success"`
	InsertedCount int      `json:"InsertedCount"`
}

// serverBatchDeleteVectorsResponse mirrors service.BatchDeleteVectorsResponse.
type serverBatchDeleteVectorsResponse struct {
	DeletedCount int      `json:"DeletedCount"`
	FailedIDs    []uint64 `json:"FailedIDs,omitempty"`
	Timestamp    int64    `json:"Timestamp"`
	Success      bool     `json:"Success"`
}

// serverListVectorsResponse mirrors service.ListVectorsResponse.
type serverListVectorsResponse struct {
	Vectors    []*serverVectorData `json:"Vectors"`
	NextCursor string              `json:"NextCursor,omitempty"`
	TotalCount uint64              `json:"TotalCount"`
	Timestamp  int64               `json:"Timestamp"`
}

// serverUpdateVectorMetadataResponse mirrors service.UpdateVectorMetadataResponse.
type serverUpdateVectorMetadataResponse struct {
	ID        uint64 `json:"ID"`
	Version   uint64 `json:"Version"`
	Timestamp int64  `json:"Timestamp"`
	Success   bool   `json:"Success"`
}

// serverVectorSearchResult mirrors service.VectorSearchResult.
type serverVectorSearchResult struct {
	ID       uint64                 `json:"ID"`
	Score    float32                `json:"Score"`
	Vector   []float32              `json:"Vector,omitempty"`
	Metadata map[string]interface{} `json:"Metadata,omitempty"`
	Distance float32                `json:"Distance"`
	Rank     int                    `json:"Rank"`
}

// serverSearchSimilarResponse mirrors service.SearchSimilarResponse.
type serverSearchSimilarResponse struct {
	Results       []*serverVectorSearchResult `json:"Results"`
	Timestamp     int64                       `json:"Timestamp"`
	QueryDuration int64                       `json:"QueryDuration"`
}

// serverSearchByMetadataResponse mirrors service.SearchByMetadataResponse.
type serverSearchByMetadataResponse struct {
	Results    []*serverVectorData `json:"Results"`
	TotalCount uint64              `json:"TotalCount"`
	Timestamp  int64               `json:"Timestamp"`
	NextCursor string              `json:"NextCursor,omitempty"`
}

// serverHybridSearchResponse mirrors service.HybridSearchResponse.
type serverHybridSearchResponse struct {
	Results         []*serverVectorSearchResult `json:"Results"`
	TotalCandidates int                         `json:"TotalCandidates"`
	TotalFiltered   int                         `json:"TotalFiltered"`
	Timestamp       int64                       `json:"Timestamp"`
	QueryDuration   int64                       `json:"QueryDuration"`
}

// serverSearchByIDResponse mirrors service.SearchByIDResponse.
type serverSearchByIDResponse struct {
	SourceID      uint64                      `json:"SourceID"`
	Results       []*serverVectorSearchResult `json:"Results"`
	Timestamp     int64                       `json:"Timestamp"`
	QueryDuration int64                       `json:"QueryDuration"`
}

// serverRebuildIndexResponse mirrors service.RebuildIndexResponse.
type serverRebuildIndexResponse struct {
	JobID                  string  `json:"JobID"`
	Status                 string  `json:"Status"`
	VectorsProcessed       uint64  `json:"VectorsProcessed"`
	TotalVectors           uint64  `json:"TotalVectors"`
	PercentComplete        float64 `json:"PercentComplete"`
	EstimatedTimeRemaining int64   `json:"EstimatedTimeRemaining"`
	Timestamp              int64   `json:"Timestamp"`
	Success                bool    `json:"Success"`
}

// serverGetRebuildProgressResponse mirrors service.GetRebuildProgressResponse.
type serverGetRebuildProgressResponse struct {
	JobID                    string  `json:"JobID"`
	Namespace                string  `json:"Namespace"`
	Status                   string  `json:"Status"`
	VectorsProcessed         uint64  `json:"VectorsProcessed"`
	TotalVectors             uint64  `json:"TotalVectors"`
	PercentComplete          float64 `json:"PercentComplete"`
	ElapsedTimeMs            int64   `json:"ElapsedTimeMs"`
	EstimatedTimeRemainingMs int64   `json:"EstimatedTimeRemainingMs"`
	StartedAt                int64   `json:"StartedAt"`
	CompletedAt              int64   `json:"CompletedAt"`
	Error                    string  `json:"Error,omitempty"`
}

// serverGetIndexStatsResponse mirrors service.GetIndexStatsResponse.
type serverGetIndexStatsResponse struct {
	Namespace         string                         `json:"Namespace"`
	IndexType         string                         `json:"IndexType"`
	VectorCount       uint64                         `json:"VectorCount"`
	Dimension         uint32                         `json:"Dimension"`
	IndexSizeBytes    uint64                         `json:"IndexSizeBytes"`
	MemoryUsageBytes  uint64                         `json:"MemoryUsageBytes"`
	IndexConfig       map[string]string              `json:"IndexConfig,omitempty"`
	SearchPerformance *serverIndexPerformanceMetrics `json:"SearchPerformance,omitempty"`
	Timestamp         int64                          `json:"Timestamp"`
}

// serverIndexPerformanceMetrics mirrors service.IndexPerformanceMetrics.
type serverIndexPerformanceMetrics struct {
	AverageSearchTimeMs float64 `json:"AverageSearchTimeMs"`
	P50SearchTimeMs     float64 `json:"P50SearchTimeMs"`
	P95SearchTimeMs     float64 `json:"P95SearchTimeMs"`
	P99SearchTimeMs     float64 `json:"P99SearchTimeMs"`
	TotalSearches       uint64  `json:"TotalSearches"`
	CacheHitRate        float64 `json:"CacheHitRate"`
}

// serverCompactNamespaceResponse mirrors service.CompactNamespaceResponse.
type serverCompactNamespaceResponse struct {
	OperationID    string `json:"OperationID"`
	StartedAt      int64  `json:"StartedAt"`
	CompletedAt    *int64 `json:"CompletedAt,omitempty"`
	BytesCompacted int64  `json:"BytesCompacted"`
	Success        bool   `json:"Success"`
}

// serverGetCompactionProgressResponse mirrors service.GetCompactionProgressResponse.
type serverGetCompactionProgressResponse struct {
	OperationID     string  `json:"OperationID"`
	Namespace       string  `json:"Namespace"`
	BytesCompacted  int64   `json:"BytesCompacted"`
	TotalBytes      int64   `json:"TotalBytes"`
	PercentComplete float64 `json:"PercentComplete"`
	InProgress      bool    `json:"InProgress"`
	StartedAt       int64   `json:"StartedAt"`
	CompletedAt     *int64  `json:"CompletedAt,omitempty"`
	Error           string  `json:"Error,omitempty"`
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

// serverVectorSearchResultToTransport converts a server-side VectorSearchResult
// to the SDK transport representation.
func serverVectorSearchResultToTransport(r *serverVectorSearchResult) *transport.VectorSearchResult {
	return &transport.VectorSearchResult{
		ID:       r.ID,
		Score:    r.Score,
		Vector:   r.Vector,
		Metadata: r.Metadata,
		Distance: r.Distance,
		Rank:     r.Rank,
	}
}

// serverVectorDataToTransport converts a server-side VectorData to the SDK
// transport representation.
func serverVectorDataToTransport(v *serverVectorData) *transport.VectorData {
	return &transport.VectorData{
		ID:        v.ID,
		Vector:    v.Vector,
		Metadata:  v.Metadata,
		Version:   v.Version,
		CreatedAt: v.CreatedAt,
		UpdatedAt: v.UpdatedAt,
	}
}

// serverNamespaceInfoToTransport converts a server-side NamespaceInfo to the
// SDK transport representation.
func serverNamespaceInfoToTransport(n *serverNamespaceInfo) *transport.NamespaceInfo {
	return &transport.NamespaceInfo{
		Namespace:      n.Namespace,
		Dimension:      n.Dimension,
		VectorCount:    n.VectorCount,
		DistanceMetric: n.DistanceMetric,
		IndexType:      transport.ParseVectorIndexType(n.IndexType),
		IndexConfig:    n.IndexConfig,
		Metadata:       n.Metadata,
		CreatedAt:      n.CreatedAt,
		UpdatedAt:      n.UpdatedAt,
	}
}

// ---------------------------------------------------------------------------
// VectorClient implementation
// ---------------------------------------------------------------------------

// CreateNamespace creates a new vector namespace with the specified
// dimensionality, distance metric, and index configuration. Calls
// the qrdb_vector_create_namespace MCP tool.
func (t *Transport) CreateNamespace(ctx context.Context, req *transport.CreateNamespaceRequest) (*transport.CreateNamespaceResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"namespace":       req.Namespace,
		"dimension":       req.Dimension,
		"distance_metric": req.DistanceMetric,
		"index_type":      req.IndexType,
	}

	if len(req.IndexConfig) > 0 {
		data, err := json.Marshal(req.IndexConfig)
		if err != nil {
			return nil, &transport.RequestError{Method: "CreateNamespace", Err: &jsonDecodeError{Err: err}}
		}
		args["index_config"] = string(data)
	}

	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, err := t.callTool(ctx, "qrdb_vector_create_namespace", args)
	if err != nil {
		return nil, err
	}

	var raw serverCreateNamespaceResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CreateNamespace", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.CreateNamespaceResponse{
		Namespace: raw.Namespace,
		Dimension: raw.Dimension,
		Timestamp: raw.Timestamp,
		Success:   raw.Success,
	}, nil
}

// UpdateNamespace changes a namespace's index parameters and metadata in
// place through the qrdb_vector_update_namespace tool, keeping every vector.
func (t *Transport) UpdateNamespace(ctx context.Context, req *transport.UpdateNamespaceRequest) (*transport.UpdateNamespaceResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{"namespace": req.Namespace}
	if req.Dimension != 0 {
		args["dimension"] = req.Dimension
	}
	if len(req.IndexConfig) > 0 {
		data, err := json.Marshal(req.IndexConfig)
		if err != nil {
			return nil, &transport.RequestError{Method: "UpdateNamespace", Err: &jsonDecodeError{Err: err}}
		}
		args["index_config"] = string(data)
	}
	if len(req.Metadata) > 0 {
		data, err := json.Marshal(req.Metadata)
		if err != nil {
			return nil, &transport.RequestError{Method: "UpdateNamespace", Err: &jsonDecodeError{Err: err}}
		}
		args["metadata"] = string(data)
	}

	text, err := t.callTool(ctx, "qrdb_vector_update_namespace", args)
	if err != nil {
		return nil, err
	}

	var raw serverUpdateNamespaceResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "UpdateNamespace", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.UpdateNamespaceResponse{
		Namespace:       raw.Namespace,
		RequiresRebuild: raw.RequiresRebuild,
		Timestamp:       raw.Timestamp,
		Success:         raw.Success,
	}, nil
}

// DeleteNamespace removes a vector namespace and all its vectors.
// Calls the qrdb_vector_delete_namespace MCP tool.
func (t *Transport) DeleteNamespace(ctx context.Context, req *transport.DeleteNamespaceRequest) (*transport.DeleteNamespaceResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"namespace": req.Namespace,
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, err := t.callTool(ctx, "qrdb_vector_delete_namespace", args)
	if err != nil {
		return nil, err
	}

	var raw serverDeleteNamespaceResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "DeleteNamespace", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.DeleteNamespaceResponse{
		Success:        raw.Success,
		VectorsDeleted: raw.VectorsDeleted,
		Timestamp:      raw.Timestamp,
	}, nil
}

// ListNamespaces returns all vector namespace names. Calls the
// qrdb_vector_list_namespaces MCP tool.
func (t *Transport) ListNamespaces(ctx context.Context, req *transport.ListNamespacesRequest) (*transport.ListNamespacesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, err := t.callTool(ctx, "qrdb_vector_list_namespaces", args)
	if err != nil {
		return nil, err
	}

	var raw serverListNamespacesResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListNamespaces", Err: &jsonDecodeError{Err: jsonErr}}
	}

	namespaces := make([]*transport.NamespaceInfo, len(raw.Namespaces))
	for i, n := range raw.Namespaces {
		namespaces[i] = serverNamespaceInfoToTransport(n)
	}

	return &transport.ListNamespacesResponse{
		Namespaces: namespaces,
		TotalCount: raw.TotalCount,
		Timestamp:  raw.Timestamp,
	}, nil
}

// GetNamespaceInfo retrieves metadata about a specific namespace.
// Calls the qrdb_vector_get_namespace_info MCP tool.
func (t *Transport) GetNamespaceInfo(ctx context.Context, req *transport.GetNamespaceInfoRequest) (*transport.GetNamespaceInfoResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"namespace": req.Namespace,
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, err := t.callTool(ctx, "qrdb_vector_get_namespace_info", args)
	if err != nil {
		return nil, err
	}

	var raw serverGetNamespaceInfoResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetNamespaceInfo", Err: &jsonDecodeError{Err: jsonErr}}
	}
	metric, err := transport.ParseDistanceMetric(raw.DistanceMetric)
	if err != nil {
		return nil, err
	}

	return &transport.GetNamespaceInfoResponse{
		Namespace:      raw.Namespace,
		Dimension:      raw.Dimension,
		VectorCount:    raw.VectorCount,
		DistanceMetric: metric,
		IndexType:      transport.ParseVectorIndexType(raw.IndexType),
		IndexConfig:    raw.IndexConfig,
		Metadata:       raw.Metadata,
		CreatedAt:      raw.CreatedAt,
		UpdatedAt:      raw.UpdatedAt,
	}, nil
}

// UpsertVector inserts or updates a vector in the specified namespace.
// Calls the qrdb_vector_upsert MCP tool.
func (t *Transport) UpsertVector(ctx context.Context, req *transport.UpsertVectorRequest) (*transport.UpsertVectorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	vectorJSON, err := json.Marshal(req.Vector)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpsertVector", Err: &jsonDecodeError{Err: err}}
	}

	args := map[string]any{
		"namespace": req.Namespace,
		"id":        req.ID,
		"vector":    string(vectorJSON),
	}

	if len(req.Metadata) > 0 {
		metaJSON, metaErr := json.Marshal(req.Metadata)
		if metaErr != nil {
			return nil, &transport.RequestError{Method: "UpsertVector", Err: &jsonDecodeError{Err: metaErr}}
		}
		args["metadata"] = string(metaJSON)
	}

	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, callErr := t.callTool(ctx, "qrdb_vector_upsert", args)
	if callErr != nil {
		return nil, callErr
	}

	var raw serverUpsertVectorResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "UpsertVector", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.UpsertVectorResponse{
		ID:        raw.ID,
		Version:   raw.Version,
		Timestamp: raw.Timestamp,
		Success:   raw.Success,
	}, nil
}

// GetVector retrieves a single vector by ID from the specified namespace.
// Calls the qrdb_vector_get MCP tool.
func (t *Transport) GetVector(ctx context.Context, req *transport.GetVectorRequest) (*transport.GetVectorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"namespace": req.Namespace,
		"id":        req.ID,
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	// REAL CROSS-TRANSPORT PARITY BUG (found + fixed): this used to swallow
	// a not-found tool error into a synthetic Found:false success response.
	// pkg/service/vector.go's GetVector never actually produces a
	// Found:false response on its success path -- a missing ID is always a
	// server-side error (ErrNotFound) -- and REST/gRPC/QUIC/Unix all
	// propagate that error to the caller unmodified, so `vector get` of a
	// missing ID fails closed on every other transport. This was the only
	// GetVector*-style caller in this package using isNotFoundError to
	// SYNTHESIZE a success value; every sibling caller (GetRevision,
	// GetAtRevision, etc. in transport_mvcc.go/transport_auth.go) uses it
	// to produce a more specific TYPED error while still returning err !=
	// nil, which is the pattern this now follows with
	// transport.VectorNotFoundError.
	text, err := t.callTool(ctx, "qrdb_vector_get", args)
	if err != nil {
		if isToolNotFound(err) {
			return nil, &transport.VectorNotFoundError{Namespace: req.Namespace, ID: strconv.FormatUint(req.ID, 10)}
		}
		return nil, err
	}

	var raw serverGetVectorResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetVector", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.GetVectorResponse{
		ID:        raw.ID,
		Vector:    raw.Vector,
		Metadata:  raw.Metadata,
		Version:   raw.Version,
		CreatedAt: raw.CreatedAt,
		UpdatedAt: raw.UpdatedAt,
		Found:     raw.Found,
	}, nil
}

// DeleteVector removes a single vector by ID from the specified namespace.
// Calls the qrdb_vector_delete MCP tool.
func (t *Transport) DeleteVector(ctx context.Context, req *transport.DeleteVectorRequest) (*transport.DeleteVectorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"namespace": req.Namespace,
		"id":        req.ID,
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, err := t.callTool(ctx, "qrdb_vector_delete", args)
	if err != nil {
		return nil, err
	}

	var raw serverDeleteVectorResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "DeleteVector", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.DeleteVectorResponse{
		Success:   raw.Success,
		Timestamp: raw.Timestamp,
	}, nil
}

// BatchUpsert inserts or updates multiple vectors in a single operation.
// Calls the qrdb_vector_batch_upsert MCP tool.
func (t *Transport) BatchUpsert(ctx context.Context, req *transport.BatchUpsertRequest) (*transport.BatchUpsertResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	type vectorJSON struct {
		ID       uint64                 `json:"id"`
		Vector   []float32              `json:"vector"`
		Metadata map[string]interface{} `json:"metadata,omitempty"`
	}

	batch := make([]vectorJSON, len(req.Vectors))
	for i, v := range req.Vectors {
		batch[i] = vectorJSON{
			ID:       v.ID,
			Vector:   v.Vector,
			Metadata: v.Metadata,
		}
	}

	data, err := json.Marshal(batch)
	if err != nil {
		return nil, &transport.RequestError{Method: "BatchUpsert", Err: &jsonDecodeError{Err: err}}
	}

	args := map[string]any{
		"namespace": req.Namespace,
		"vectors":   string(data),
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, callErr := t.callTool(ctx, "qrdb_vector_batch_upsert", args)
	if callErr != nil {
		return nil, callErr
	}

	var raw serverBatchUpsertResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "BatchUpsert", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.BatchUpsertResponse{
		IDs:           raw.IDs,
		Versions:      raw.Versions,
		Timestamp:     raw.Timestamp,
		Success:       raw.Success,
		InsertedCount: raw.InsertedCount,
	}, nil
}

// BatchDeleteVectors removes multiple vectors by ID in a single operation.
// Calls the qrdb_vector_batch_delete MCP tool.
func (t *Transport) BatchDeleteVectors(ctx context.Context, req *transport.BatchDeleteVectorsRequest) (*transport.BatchDeleteVectorsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	idsJSON, err := json.Marshal(req.IDs)
	if err != nil {
		return nil, &transport.RequestError{Method: "BatchDeleteVectors", Err: &jsonDecodeError{Err: err}}
	}

	args := map[string]any{
		"namespace": req.Namespace,
		"ids":       string(idsJSON),
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, callErr := t.callTool(ctx, "qrdb_vector_batch_delete", args)
	if callErr != nil {
		return nil, callErr
	}

	var raw serverBatchDeleteVectorsResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "BatchDeleteVectors", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.BatchDeleteVectorsResponse{
		DeletedCount: raw.DeletedCount,
		FailedIDs:    raw.FailedIDs,
		Timestamp:    raw.Timestamp,
		Success:      raw.Success,
	}, nil
}

// ListVectors returns a paginated list of vectors in a namespace.
// Calls the qrdb_vector_list MCP tool.
func (t *Transport) ListVectors(ctx context.Context, req *transport.ListVectorsRequest) (*transport.ListVectorsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"namespace": req.Namespace,
	}
	if req.Limit > 0 {
		args["limit"] = int64(req.Limit)
	}
	if req.Cursor != "" {
		args["cursor"] = req.Cursor
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, err := t.callTool(ctx, "qrdb_vector_list", args)
	if err != nil {
		return nil, err
	}

	var raw serverListVectorsResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListVectors", Err: &jsonDecodeError{Err: jsonErr}}
	}

	vectors := make([]*transport.VectorData, len(raw.Vectors))
	for i, v := range raw.Vectors {
		vectors[i] = serverVectorDataToTransport(v)
	}

	return &transport.ListVectorsResponse{
		Vectors:    vectors,
		NextCursor: raw.NextCursor,
		TotalCount: raw.TotalCount,
		Timestamp:  raw.Timestamp,
	}, nil
}

// UpdateMetadata updates the metadata for an existing vector without
// modifying the vector data. Calls the qrdb_vector_update_metadata
// MCP tool.
func (t *Transport) UpdateMetadata(ctx context.Context, req *transport.UpdateVectorMetadataRequest) (*transport.UpdateVectorMetadataResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	metaJSON, err := json.Marshal(req.Metadata)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateMetadata", Err: &jsonDecodeError{Err: err}}
	}

	args := map[string]any{
		"namespace": req.Namespace,
		"id":        req.ID,
		"metadata":  string(metaJSON),
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, callErr := t.callTool(ctx, "qrdb_vector_update_metadata", args)
	if callErr != nil {
		return nil, callErr
	}

	var raw serverUpdateVectorMetadataResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "UpdateMetadata", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.UpdateVectorMetadataResponse{
		ID:        raw.ID,
		Version:   raw.Version,
		Timestamp: raw.Timestamp,
		Success:   raw.Success,
	}, nil
}

// SearchSimilar performs a k-nearest-neighbor search using a query
// vector. Calls the qrdb_vector_search_similar MCP tool.
func (t *Transport) SearchSimilar(ctx context.Context, req *transport.SearchSimilarRequest) (*transport.SearchSimilarResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	vectorJSON, err := json.Marshal(req.QueryVector)
	if err != nil {
		return nil, &transport.RequestError{Method: "SearchSimilar", Err: &jsonDecodeError{Err: err}}
	}

	args := map[string]any{
		"namespace": req.Namespace,
		"vector":    string(vectorJSON),
		"k":         req.K,
	}
	if req.EfSearch > 0 {
		args["ef_search"] = req.EfSearch
	}
	if req.MinScore > 0 {
		args["min_score"] = req.MinScore
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, callErr := t.callTool(ctx, "qrdb_vector_search_similar", args)
	if callErr != nil {
		return nil, callErr
	}

	var raw serverSearchSimilarResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "SearchSimilar", Err: &jsonDecodeError{Err: jsonErr}}
	}

	results := make([]*transport.VectorSearchResult, len(raw.Results))
	for i, r := range raw.Results {
		results[i] = serverVectorSearchResultToTransport(r)
	}

	return &transport.SearchSimilarResponse{
		Results:       results,
		Timestamp:     raw.Timestamp,
		QueryDuration: raw.QueryDuration,
	}, nil
}

// SearchByMetadata returns vectors matching metadata filters. Calls
// the qrdb_vector_search_by_metadata MCP tool.
func (t *Transport) SearchByMetadata(ctx context.Context, req *transport.SearchByMetadataRequest) (*transport.SearchByMetadataResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	filtersJSON, err := json.Marshal(req.Filters)
	if err != nil {
		return nil, &transport.RequestError{Method: "SearchByMetadata", Err: &jsonDecodeError{Err: err}}
	}

	args := map[string]any{
		"namespace": req.Namespace,
		"filters":   string(filtersJSON),
	}
	if req.Limit > 0 {
		args["limit"] = int64(req.Limit)
	}
	if req.Offset > 0 {
		args["offset"] = int64(req.Offset)
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, callErr := t.callTool(ctx, "qrdb_vector_search_by_metadata", args)
	if callErr != nil {
		return nil, callErr
	}

	var raw serverSearchByMetadataResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "SearchByMetadata", Err: &jsonDecodeError{Err: jsonErr}}
	}

	results := make([]*transport.VectorData, len(raw.Results))
	for i, v := range raw.Results {
		results[i] = serverVectorDataToTransport(v)
	}

	return &transport.SearchByMetadataResponse{
		Results:    results,
		TotalCount: raw.TotalCount,
		Timestamp:  raw.Timestamp,
		NextCursor: raw.NextCursor,
	}, nil
}

// HybridSearch combines vector similarity and metadata filtering.
// Calls the qrdb_vector_hybrid_search MCP tool.
func (t *Transport) HybridSearch(ctx context.Context, req *transport.HybridSearchRequest) (*transport.HybridSearchResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	vectorJSON, err := json.Marshal(req.QueryVector)
	if err != nil {
		return nil, &transport.RequestError{Method: "HybridSearch", Err: &jsonDecodeError{Err: err}}
	}

	filterJSON, err := json.Marshal(req.MetadataFilter)
	if err != nil {
		return nil, &transport.RequestError{Method: "HybridSearch", Err: &jsonDecodeError{Err: err}}
	}

	args := map[string]any{
		"namespace":       req.Namespace,
		"vector":          string(vectorJSON),
		"metadata_filter": string(filterJSON),
		"k":               req.K,
	}
	if req.EfSearch > 0 {
		args["ef_search"] = req.EfSearch
	}
	if req.MinScore > 0 {
		args["min_score"] = req.MinScore
	}
	if req.FilterOperator != "" {
		args["filter_operator"] = req.FilterOperator
	}
	if req.RerankingAlgorithm != "" {
		args["reranking_algorithm"] = req.RerankingAlgorithm
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, callErr := t.callTool(ctx, "qrdb_vector_hybrid_search", args)
	if callErr != nil {
		return nil, callErr
	}

	var raw serverHybridSearchResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "HybridSearch", Err: &jsonDecodeError{Err: jsonErr}}
	}

	results := make([]*transport.VectorSearchResult, len(raw.Results))
	for i, r := range raw.Results {
		results[i] = serverVectorSearchResultToTransport(r)
	}

	return &transport.HybridSearchResponse{
		Results:         results,
		TotalCandidates: raw.TotalCandidates,
		TotalFiltered:   raw.TotalFiltered,
		Timestamp:       raw.Timestamp,
		QueryDuration:   raw.QueryDuration,
	}, nil
}

// SearchByID searches for similar vectors using an existing vector's
// ID as the query. Calls the qrdb_vector_search_by_id MCP tool.
func (t *Transport) SearchByID(ctx context.Context, req *transport.SearchByIDRequest) (*transport.SearchByIDResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"namespace": req.Namespace,
		"id":        req.ID,
		"k":         req.K,
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, err := t.callTool(ctx, "qrdb_vector_search_by_id", args)
	if err != nil {
		return nil, err
	}

	var raw serverSearchByIDResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "SearchByID", Err: &jsonDecodeError{Err: jsonErr}}
	}

	results := make([]*transport.VectorSearchResult, len(raw.Results))
	for i, r := range raw.Results {
		results[i] = serverVectorSearchResultToTransport(r)
	}

	return &transport.SearchByIDResponse{
		SourceID:      raw.SourceID,
		Results:       results,
		Timestamp:     raw.Timestamp,
		QueryDuration: raw.QueryDuration,
	}, nil
}

// RebuildIndex triggers an asynchronous index rebuild for a namespace.
// Calls the qrdb_vector_rebuild_index MCP tool.
func (t *Transport) RebuildIndex(ctx context.Context, req *transport.RebuildIndexRequest) (*transport.RebuildIndexResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"namespace": req.Namespace,
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, err := t.callTool(ctx, "qrdb_vector_rebuild_index", args)
	if err != nil {
		return nil, err
	}

	var raw serverRebuildIndexResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "RebuildIndex", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.RebuildIndexResponse{
		JobID:                  raw.JobID,
		Status:                 raw.Status,
		VectorsProcessed:       raw.VectorsProcessed,
		TotalVectors:           raw.TotalVectors,
		PercentComplete:        raw.PercentComplete,
		EstimatedTimeRemaining: raw.EstimatedTimeRemaining,
		Timestamp:              raw.Timestamp,
		Success:                raw.Success,
	}, nil
}

// GetRebuildProgress returns the current progress of an index rebuild
// job. Calls the qrdb_vector_get_rebuild_progress MCP tool.
func (t *Transport) GetRebuildProgress(ctx context.Context, req *transport.GetRebuildProgressRequest) (*transport.GetRebuildProgressResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"namespace": req.Namespace,
		"job_id":    req.JobID,
	}

	text, err := t.callTool(ctx, "qrdb_vector_get_rebuild_progress", args)
	if err != nil {
		return nil, err
	}

	var raw serverGetRebuildProgressResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetRebuildProgress", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.GetRebuildProgressResponse{
		JobID:                    raw.JobID,
		Namespace:                raw.Namespace,
		Status:                   raw.Status,
		VectorsProcessed:         raw.VectorsProcessed,
		TotalVectors:             raw.TotalVectors,
		PercentComplete:          raw.PercentComplete,
		ElapsedTimeMs:            raw.ElapsedTimeMs,
		EstimatedTimeRemainingMs: raw.EstimatedTimeRemainingMs,
		StartedAt:                raw.StartedAt,
		CompletedAt:              raw.CompletedAt,
		Error:                    raw.Error,
	}, nil
}

// GetIndexStats returns statistics about a namespace's vector index.
// Calls the qrdb_vector_get_index_stats MCP tool.
func (t *Transport) GetIndexStats(ctx context.Context, req *transport.GetIndexStatsRequest) (*transport.GetIndexStatsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"namespace": req.Namespace,
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, err := t.callTool(ctx, "qrdb_vector_get_index_stats", args)
	if err != nil {
		return nil, err
	}

	var raw serverGetIndexStatsResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetIndexStats", Err: &jsonDecodeError{Err: jsonErr}}
	}

	var perf *transport.IndexPerformanceMetrics
	if raw.SearchPerformance != nil {
		perf = &transport.IndexPerformanceMetrics{
			AverageSearchTimeMs: raw.SearchPerformance.AverageSearchTimeMs,
			P50SearchTimeMs:     raw.SearchPerformance.P50SearchTimeMs,
			P95SearchTimeMs:     raw.SearchPerformance.P95SearchTimeMs,
			P99SearchTimeMs:     raw.SearchPerformance.P99SearchTimeMs,
			TotalSearches:       raw.SearchPerformance.TotalSearches,
			CacheHitRate:        raw.SearchPerformance.CacheHitRate,
		}
	}

	return &transport.GetIndexStatsResponse{
		Namespace:         raw.Namespace,
		IndexType:         transport.ParseVectorIndexType(raw.IndexType),
		VectorCount:       raw.VectorCount,
		Dimension:         raw.Dimension,
		IndexSizeBytes:    raw.IndexSizeBytes,
		MemoryUsageBytes:  raw.MemoryUsageBytes,
		IndexConfig:       raw.IndexConfig,
		SearchPerformance: perf,
		Timestamp:         raw.Timestamp,
	}, nil
}

// CompactNamespace triggers compaction of a namespace's storage to
// reclaim space from deleted vectors. Calls the
// qrdb_vector_compact_namespace MCP tool.
func (t *Transport) CompactNamespace(ctx context.Context, req *transport.CompactNamespaceRequest) (*transport.CompactNamespaceResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"namespace": req.Namespace,
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, err := t.callTool(ctx, "qrdb_vector_compact_namespace", args)
	if err != nil {
		return nil, err
	}

	var raw serverCompactNamespaceResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CompactNamespace", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.CompactNamespaceResponse{
		OperationID:    raw.OperationID,
		StartedAt:      raw.StartedAt,
		CompletedAt:    raw.CompletedAt,
		BytesCompacted: raw.BytesCompacted,
		Success:        raw.Success,
	}, nil
}

// GetCompactionProgress returns the current progress of a compaction
// operation. Calls the qrdb_vector_get_compaction_progress MCP tool.
func (t *Transport) GetCompactionProgress(ctx context.Context, req *transport.GetCompactionProgressRequest) (*transport.GetCompactionProgressResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"namespace":    req.Namespace,
		"operation_id": req.OperationID,
	}
	if req.ShardID != 0 {
		args["shard_id"] = req.ShardID
	}

	text, err := t.callTool(ctx, "qrdb_vector_get_compaction_progress", args)
	if err != nil {
		return nil, err
	}

	var raw serverGetCompactionProgressResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetCompactionProgress", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.GetCompactionProgressResponse{
		OperationID:     raw.OperationID,
		Namespace:       raw.Namespace,
		BytesCompacted:  raw.BytesCompacted,
		TotalBytes:      raw.TotalBytes,
		PercentComplete: raw.PercentComplete,
		InProgress:      raw.InProgress,
		StartedAt:       raw.StartedAt,
		CompletedAt:     raw.CompletedAt,
		Error:           raw.Error,
	}, nil
}
