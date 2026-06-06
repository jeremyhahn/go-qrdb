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

package unix

import (
	"context"
	"encoding/json"
	"strconv"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.VectorClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Typed errors
// ---------------------------------------------------------------------------

// metadataMarshalError is returned when JSON marshaling of metadata fails.
type metadataMarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *metadataMarshalError) Error() string {
	return "unix transport: failed to marshal metadata: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *metadataMarshalError) Unwrap() error {
	return e.Err
}

// metadataUnmarshalError is returned when JSON unmarshaling of metadata fails.
type metadataUnmarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *metadataUnmarshalError) Error() string {
	return "unix transport: failed to unmarshal metadata: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *metadataUnmarshalError) Unwrap() error {
	return e.Err
}

// vectorIDParseError is returned when a proto vector ID cannot be parsed
// as a uint64.
type vectorIDParseError struct {
	ID  string
	Err error
}

// Error returns a human-readable description.
func (e *vectorIDParseError) Error() string {
	return "unix transport: failed to parse vector ID " + e.ID + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *vectorIDParseError) Unwrap() error {
	return e.Err
}

// ---------------------------------------------------------------------------
// Enum conversion maps
// ---------------------------------------------------------------------------

// distanceMetricToProto converts transport DistanceMetric strings to proto
// DistanceMetric values.
var distanceMetricToProto = map[string]pb.DistanceMetric{
	transport.DistanceCosine:     pb.DistanceMetric_DISTANCE_COSINE,
	transport.DistanceEuclidean:  pb.DistanceMetric_DISTANCE_EUCLIDEAN,
	transport.DistanceDotProduct: pb.DistanceMetric_DISTANCE_DOT_PRODUCT,
}

// protoDistanceMetricToTransport converts proto DistanceMetric values to
// transport DistanceMetric strings.
var protoDistanceMetricToTransport = map[pb.DistanceMetric]string{
	pb.DistanceMetric_DISTANCE_COSINE:      transport.DistanceCosine,
	pb.DistanceMetric_DISTANCE_EUCLIDEAN:   transport.DistanceEuclidean,
	pb.DistanceMetric_DISTANCE_DOT_PRODUCT: transport.DistanceDotProduct,
}

// vectorIndexTypeToProto converts transport VectorIndexType strings to proto
// VectorIndexType values.
var vectorIndexTypeToProto = map[string]pb.VectorIndexType{
	transport.IndexHNSW:    pb.VectorIndexType_INDEX_HNSW,
	transport.IndexFlat:    pb.VectorIndexType_INDEX_FLAT,
	transport.IndexIVFFlat: pb.VectorIndexType_INDEX_IVF_FLAT,
}

// protoVectorIndexTypeToTransport converts proto VectorIndexType values to
// transport VectorIndexType strings.
var protoVectorIndexTypeToTransport = map[pb.VectorIndexType]string{
	pb.VectorIndexType_INDEX_HNSW:     transport.IndexHNSW,
	pb.VectorIndexType_INDEX_FLAT:     transport.IndexFlat,
	pb.VectorIndexType_INDEX_IVF_FLAT: transport.IndexIVFFlat,
}

// ---------------------------------------------------------------------------
// Namespace operations
// ---------------------------------------------------------------------------

// CreateNamespace creates a new vector namespace with the specified
// dimensionality, distance metric, and index configuration.
func (t *Transport) CreateNamespace(ctx context.Context, req *transport.CreateNamespaceRequest) (*transport.CreateNamespaceResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	indexParamsJSON, err := marshalStringMetadataJSON(req.IndexConfig)
	if err != nil {
		return nil, err
	}

	resp, err := t.client.VectorCreateNamespace(ctx, &pb.VectorCreateNamespaceRequest{
		Namespace:       req.Namespace,
		Dimension:       int32(req.Dimension),
		DistanceMetric:  distanceMetricToProto[req.DistanceMetric],
		IndexType:       vectorIndexTypeToProto[req.IndexType],
		IndexParamsJson: indexParamsJSON,
		ShardId:         req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("CreateNamespace", err)
	}

	return &transport.CreateNamespaceResponse{
		Namespace: req.Namespace,
		Dimension: req.Dimension,
		Timestamp: resp.GetTimestamp(),
		Success:   resp.GetCreated(),
	}, nil
}

// DeleteNamespace removes a namespace and all its vectors.
func (t *Transport) DeleteNamespace(ctx context.Context, req *transport.DeleteNamespaceRequest) (*transport.DeleteNamespaceResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	_, err := t.client.VectorDeleteNamespace(ctx, &pb.VectorDeleteNamespaceRequest{
		Namespace: req.Namespace,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("DeleteNamespace", err)
	}

	return &transport.DeleteNamespaceResponse{Success: true}, nil
}

// ListNamespaces returns all vector namespaces.
func (t *Transport) ListNamespaces(ctx context.Context, req *transport.ListNamespacesRequest) (*transport.ListNamespacesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.VectorListNamespaces(ctx, &pb.VectorListNamespacesRequest{
		ShardId: req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("ListNamespaces", err)
	}

	namespaces := make([]*transport.NamespaceInfo, len(resp.GetNamespaces()))
	for i, name := range resp.GetNamespaces() {
		namespaces[i] = &transport.NamespaceInfo{
			Namespace: name,
		}
	}

	return &transport.ListNamespacesResponse{
		Namespaces: namespaces,
		TotalCount: len(namespaces),
	}, nil
}

// GetNamespaceInfo returns metadata about a namespace including
// dimension, distance metric, index type, and vector count.
func (t *Transport) GetNamespaceInfo(ctx context.Context, req *transport.GetNamespaceInfoRequest) (*transport.GetNamespaceInfoResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.VectorGetNamespaceInfo(ctx, &pb.VectorGetNamespaceInfoRequest{
		Namespace: req.Namespace,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("GetNamespaceInfo", err)
	}

	return &transport.GetNamespaceInfoResponse{
		Namespace:      resp.GetNamespace(),
		Dimension:      uint32(resp.GetDimension()),
		VectorCount:    uint64(resp.GetVectorCount()),
		DistanceMetric: protoDistanceMetricToTransport[resp.GetDistanceMetric()],
		IndexType:      protoVectorIndexTypeToTransport[resp.GetIndexType()],
		CreatedAt:      resp.GetCreatedAt(),
		UpdatedAt:      resp.GetUpdatedAt(),
	}, nil
}

// ---------------------------------------------------------------------------
// Vector CRUD operations
// ---------------------------------------------------------------------------

// UpsertVector inserts or updates a vector in a namespace.
func (t *Transport) UpsertVector(ctx context.Context, req *transport.UpsertVectorRequest) (*transport.UpsertVectorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	metadataJSON, err := marshalInterfaceMetadataJSON(req.Metadata)
	if err != nil {
		return nil, err
	}

	resp, err := t.client.VectorUpsert(ctx, &pb.VectorUpsertRequest{
		Namespace:    req.Namespace,
		Id:           formatVectorID(req.ID),
		Vector:       req.Vector,
		MetadataJson: metadataJSON,
		ShardId:      req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("UpsertVector", err)
	}

	return &transport.UpsertVectorResponse{
		ID:        req.ID,
		Version:   uint64(resp.GetVersion()),
		Timestamp: resp.GetTimestamp(),
		Success:   resp.GetCreated(),
	}, nil
}

// GetVector retrieves a single vector by ID.
func (t *Transport) GetVector(ctx context.Context, req *transport.GetVectorRequest) (*transport.GetVectorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.VectorGet(ctx, &pb.VectorGetRequest{
		Namespace:   req.Namespace,
		Id:          formatVectorID(req.ID),
		Consistency: vectorConsistencyToProto(req.Consistency),
		ShardId:     req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("GetVector", err)
	}

	metadata, err := unmarshalInterfaceMetadataJSON(resp.GetMetadataJson())
	if err != nil {
		return nil, err
	}

	id, err := parseVectorID(resp.GetId())
	if err != nil {
		return nil, err
	}

	return &transport.GetVectorResponse{
		ID:        id,
		Vector:    resp.GetVector(),
		Metadata:  metadata,
		Version:   uint64(resp.GetVersion()),
		CreatedAt: resp.GetCreatedAt(),
		UpdatedAt: resp.GetUpdatedAt(),
		Found:     true,
	}, nil
}

// DeleteVector removes a single vector by ID.
func (t *Transport) DeleteVector(ctx context.Context, req *transport.DeleteVectorRequest) (*transport.DeleteVectorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	_, err := t.client.VectorDelete(ctx, &pb.VectorDeleteRequest{
		Namespace: req.Namespace,
		Id:        formatVectorID(req.ID),
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("DeleteVector", err)
	}

	return &transport.DeleteVectorResponse{Success: true}, nil
}

// BatchUpsert inserts or updates multiple vectors.
func (t *Transport) BatchUpsert(ctx context.Context, req *transport.BatchUpsertRequest) (*transport.BatchUpsertResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	pbVectors := make([]*pb.VectorDataProto, len(req.Vectors))
	for i, v := range req.Vectors {
		pbV, err := vectorDataToProto(v)
		if err != nil {
			return nil, err
		}
		pbVectors[i] = pbV
	}

	resp, err := t.client.VectorBatchUpsert(ctx, &pb.VectorBatchUpsertRequest{
		Namespace: req.Namespace,
		Vectors:   pbVectors,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("BatchUpsert", err)
	}

	return &transport.BatchUpsertResponse{
		InsertedCount: int(resp.GetSucceeded()),
		Success:       resp.GetFailed() == 0,
	}, nil
}

// BatchDeleteVectors removes multiple vectors by ID.
func (t *Transport) BatchDeleteVectors(ctx context.Context, req *transport.BatchDeleteVectorsRequest) (*transport.BatchDeleteVectorsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	ids := make([]string, len(req.IDs))
	for i, id := range req.IDs {
		ids[i] = formatVectorID(id)
	}

	resp, err := t.client.VectorBatchDelete(ctx, &pb.VectorBatchDeleteRequest{
		Namespace: req.Namespace,
		Ids:       ids,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("BatchDeleteVectors", err)
	}

	var failedIDs []uint64
	for _, e := range resp.GetErrors() {
		id, parseErr := parseVectorID(e.GetId())
		if parseErr == nil {
			failedIDs = append(failedIDs, id)
		}
	}

	return &transport.BatchDeleteVectorsResponse{
		DeletedCount: int(resp.GetDeleted()),
		FailedIDs:    failedIDs,
		Success:      resp.GetFailed() == 0,
	}, nil
}

// ListVectors returns a paginated list of vectors in a namespace.
func (t *Transport) ListVectors(ctx context.Context, req *transport.ListVectorsRequest) (*transport.ListVectorsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.VectorList(ctx, &pb.VectorListRequest{
		Namespace: req.Namespace,
		Limit:     int32(req.Limit),
		Cursor:    req.Cursor,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("ListVectors", err)
	}

	vectors := make([]*transport.VectorData, len(resp.GetVectors()))
	for i, v := range resp.GetVectors() {
		vd, err := protoToVectorData(v)
		if err != nil {
			return nil, err
		}
		vectors[i] = vd
	}

	return &transport.ListVectorsResponse{
		Vectors:    vectors,
		NextCursor: resp.GetNextCursor(),
		TotalCount: uint64(resp.GetTotal()),
	}, nil
}

// UpdateMetadata updates the metadata for an existing vector without
// modifying the vector data itself.
func (t *Transport) UpdateMetadata(ctx context.Context, req *transport.UpdateVectorMetadataRequest) (*transport.UpdateVectorMetadataResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	metadataJSON, err := marshalInterfaceMetadataJSON(req.Metadata)
	if err != nil {
		return nil, err
	}

	_, err = t.client.VectorUpdateMetadata(ctx, &pb.VectorUpdateMetadataRequest{
		Namespace:    req.Namespace,
		Id:           formatVectorID(req.ID),
		MetadataJson: metadataJSON,
		ShardId:      req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("UpdateMetadata", err)
	}

	return &transport.UpdateVectorMetadataResponse{
		ID:      req.ID,
		Success: true,
	}, nil
}

// ---------------------------------------------------------------------------
// Search operations
// ---------------------------------------------------------------------------

// SearchSimilar performs a k-nearest-neighbor search using a query vector.
func (t *Transport) SearchSimilar(ctx context.Context, req *transport.SearchSimilarRequest) (*transport.SearchSimilarResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.VectorSearchSimilar(ctx, &pb.VectorSearchSimilarRequest{
		Namespace:   req.Namespace,
		Vector:      req.QueryVector,
		K:           int32(req.K),
		EfSearch:    int32(req.EfSearch),
		MinScore:    float64(req.MinScore),
		Consistency: vectorConsistencyToProto(req.Consistency),
		ShardId:     req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("SearchSimilar", err)
	}

	results, err := protoToSearchResults(resp.GetResults())
	if err != nil {
		return nil, err
	}

	return &transport.SearchSimilarResponse{
		Results:       results,
		QueryDuration: resp.GetTook(),
	}, nil
}

// SearchByMetadata returns vectors matching metadata filters.
func (t *Transport) SearchByMetadata(ctx context.Context, req *transport.SearchByMetadataRequest) (*transport.SearchByMetadataResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	filtersJSON, err := marshalInterfaceMetadataJSON(req.Filters)
	if err != nil {
		return nil, err
	}

	resp, err := t.client.VectorSearchByMetadata(ctx, &pb.VectorSearchByMetadataRequest{
		Namespace:   req.Namespace,
		FiltersJson: filtersJSON,
		Limit:       int32(req.Limit),
		Offset:      int32(req.Offset),
		Consistency: vectorConsistencyToProto(req.Consistency),
		ShardId:     req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("SearchByMetadata", err)
	}

	results := make([]*transport.VectorData, len(resp.GetResults()))
	for i, v := range resp.GetResults() {
		vd, err := protoToVectorData(v)
		if err != nil {
			return nil, err
		}
		results[i] = vd
	}

	return &transport.SearchByMetadataResponse{
		Results:    results,
		TotalCount: uint64(resp.GetTotal()),
	}, nil
}

// HybridSearch combines vector similarity and metadata filtering with
// configurable weights for each scoring component.
func (t *Transport) HybridSearch(ctx context.Context, req *transport.HybridSearchRequest) (*transport.HybridSearchResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	metadataFiltersJSON, err := marshalInterfaceMetadataJSON(req.MetadataFilter)
	if err != nil {
		return nil, err
	}

	resp, err := t.client.VectorHybridSearch(ctx, &pb.VectorHybridSearchRequest{
		Namespace:           req.Namespace,
		Vector:              req.QueryVector,
		MetadataFiltersJson: metadataFiltersJSON,
		K:                   int32(req.K),
		Consistency:         vectorConsistencyToProto(req.Consistency),
		ShardId:             req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("HybridSearch", err)
	}

	results, err := protoToSearchResults(resp.GetResults())
	if err != nil {
		return nil, err
	}

	return &transport.HybridSearchResponse{
		Results:       results,
		QueryDuration: resp.GetTook(),
	}, nil
}

// SearchByID searches using an existing vector's ID as the query,
// returning its most similar neighbors.
func (t *Transport) SearchByID(ctx context.Context, req *transport.SearchByIDRequest) (*transport.SearchByIDResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.VectorSearchByID(ctx, &pb.VectorSearchByIDRequest{
		Namespace:   req.Namespace,
		Id:          formatVectorID(req.ID),
		K:           int32(req.K),
		Consistency: vectorConsistencyToProto(req.Consistency),
		ShardId:     req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("SearchByID", err)
	}

	// Server reuses VectorSearchSimilarResponse for SearchByID.
	results, err := protoToSearchResults(resp.GetResults())
	if err != nil {
		return nil, err
	}

	return &transport.SearchByIDResponse{
		SourceID:      req.ID,
		Results:       results,
		QueryDuration: resp.GetTook(),
	}, nil
}

// ---------------------------------------------------------------------------
// Index management operations
// ---------------------------------------------------------------------------

// RebuildIndex triggers an asynchronous index rebuild for a namespace.
func (t *Transport) RebuildIndex(ctx context.Context, req *transport.RebuildIndexRequest) (*transport.RebuildIndexResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.VectorRebuildIndex(ctx, &pb.VectorRebuildIndexRequest{
		Namespace: req.Namespace,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("RebuildIndex", err)
	}

	return &transport.RebuildIndexResponse{
		JobID:     resp.GetJobId(),
		Status:    "running",
		Timestamp: resp.GetTimestamp(),
		Success:   resp.GetStarted(),
	}, nil
}

// GetRebuildProgress returns the current progress of an index rebuild.
func (t *Transport) GetRebuildProgress(ctx context.Context, req *transport.GetRebuildProgressRequest) (*transport.GetRebuildProgressResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.VectorGetRebuildProgress(ctx, &pb.VectorGetRebuildProgressRequest{
		Namespace: req.Namespace,
		JobId:     req.JobID,
	})
	if err != nil {
		return nil, mapGRPCError("GetRebuildProgress", err)
	}

	return &transport.GetRebuildProgressResponse{
		JobID:           resp.GetJobId(),
		Status:          resp.GetStatus(),
		PercentComplete: resp.GetProgress(),
		StartedAt:       resp.GetStartedAt(),
		CompletedAt:     resp.GetFinishedAt(),
		Error:           resp.GetError(),
	}, nil
}

// GetIndexStats returns statistics about a namespace's vector index
// including vector count, memory usage, and disk usage.
func (t *Transport) GetIndexStats(ctx context.Context, req *transport.GetIndexStatsRequest) (*transport.GetIndexStatsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.VectorGetIndexStats(ctx, &pb.VectorGetIndexStatsRequest{
		Namespace: req.Namespace,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("GetIndexStats", err)
	}

	return &transport.GetIndexStatsResponse{
		Namespace:        req.Namespace,
		IndexType:        protoVectorIndexTypeToTransport[resp.GetIndexType()],
		VectorCount:      uint64(resp.GetVectorCount()),
		Dimension:        uint32(resp.GetDimension()),
		IndexSizeBytes:   uint64(resp.GetDiskUsage()),
		MemoryUsageBytes: uint64(resp.GetMemoryUsage()),
	}, nil
}

// ---------------------------------------------------------------------------
// Compaction operations
// ---------------------------------------------------------------------------

// CompactNamespace triggers compaction of a namespace's storage to
// reclaim space from deleted vectors.
func (t *Transport) CompactNamespace(ctx context.Context, req *transport.CompactNamespaceRequest) (*transport.CompactNamespaceResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.VectorCompactNamespace(ctx, &pb.VectorCompactNamespaceRequest{
		Namespace: req.Namespace,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("CompactNamespace", err)
	}

	return &transport.CompactNamespaceResponse{
		OperationID: resp.GetJobId(),
		Success:     resp.GetStarted(),
	}, nil
}

// GetCompactionProgress returns the current progress of a compaction.
func (t *Transport) GetCompactionProgress(ctx context.Context, req *transport.GetCompactionProgressRequest) (*transport.GetCompactionProgressResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.VectorGetCompactionProgress(ctx, &pb.VectorGetCompactionProgressRequest{
		Namespace: req.Namespace,
		JobId:     req.OperationID,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, mapGRPCError("GetCompactionProgress", err)
	}

	inProgress := resp.GetStatus() == "running"
	var completedAt *int64
	if finishedAt := resp.GetFinishedAt(); finishedAt != 0 {
		completedAt = &finishedAt
	}

	return &transport.GetCompactionProgressResponse{
		OperationID:     resp.GetJobId(),
		Namespace:       req.Namespace,
		PercentComplete: resp.GetProgress(),
		InProgress:      inProgress,
		StartedAt:       resp.GetStartedAt(),
		CompletedAt:     completedAt,
		Error:           resp.GetError(),
	}, nil
}

// ---------------------------------------------------------------------------
// Proto conversion helpers
// ---------------------------------------------------------------------------

// formatVectorID converts a uint64 ID to its string representation
// for the proto wire format.
func formatVectorID(id uint64) string {
	return strconv.FormatUint(id, 10)
}

// parseVectorID converts a proto string ID to a uint64.
func parseVectorID(id string) (uint64, error) {
	if id == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return 0, &vectorIDParseError{ID: id, Err: err}
	}
	return n, nil
}

// vectorDataToProto converts a transport VectorData to a proto
// VectorDataProto. Metadata is serialized as JSON.
func vectorDataToProto(v *transport.VectorData) (*pb.VectorDataProto, error) {
	if v == nil {
		return nil, nil
	}
	metadataJSON, err := marshalInterfaceMetadataJSON(v.Metadata)
	if err != nil {
		return nil, err
	}
	return &pb.VectorDataProto{
		Id:           formatVectorID(v.ID),
		Vector:       v.Vector,
		MetadataJson: metadataJSON,
		Version:      int64(v.Version),
		CreatedAt:    v.CreatedAt,
		UpdatedAt:    v.UpdatedAt,
	}, nil
}

// protoToVectorData converts a proto VectorDataProto to a transport
// VectorData. Metadata is deserialized from JSON.
func protoToVectorData(v *pb.VectorDataProto) (*transport.VectorData, error) {
	if v == nil {
		return nil, nil
	}
	metadata, err := unmarshalInterfaceMetadataJSON(v.GetMetadataJson())
	if err != nil {
		return nil, err
	}
	id, err := parseVectorID(v.GetId())
	if err != nil {
		return nil, err
	}
	return &transport.VectorData{
		ID:        id,
		Vector:    v.GetVector(),
		Metadata:  metadata,
		Version:   uint64(v.GetVersion()),
		CreatedAt: v.GetCreatedAt(),
		UpdatedAt: v.GetUpdatedAt(),
	}, nil
}

// protoToSearchResults converts a slice of proto VectorSearchResultProto
// to transport VectorSearchResult pointers.
func protoToSearchResults(results []*pb.VectorSearchResultProto) ([]*transport.VectorSearchResult, error) {
	out := make([]*transport.VectorSearchResult, len(results))
	for i, r := range results {
		metadata, err := unmarshalInterfaceMetadataJSON(r.GetMetadataJson())
		if err != nil {
			return nil, err
		}
		id, err := parseVectorID(r.GetId())
		if err != nil {
			return nil, err
		}
		out[i] = &transport.VectorSearchResult{
			ID:       id,
			Score:    float32(r.GetScore()),
			Vector:   r.GetVector(),
			Metadata: metadata,
		}
	}
	return out, nil
}

// marshalStringMetadataJSON serializes a map[string]string to a JSON string.
// Returns an empty string for nil or empty maps.
func marshalStringMetadataJSON(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return "", &metadataMarshalError{Err: err}
	}
	return string(data), nil
}

// marshalInterfaceMetadataJSON serializes a map[string]interface{} to a
// JSON string. Returns an empty string for nil or empty maps.
func marshalInterfaceMetadataJSON(m map[string]interface{}) (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return "", &metadataMarshalError{Err: err}
	}
	return string(data), nil
}

// unmarshalInterfaceMetadataJSON deserializes a JSON string into a
// map[string]interface{}. Returns nil for empty strings.
func unmarshalInterfaceMetadataJSON(s string) (map[string]interface{}, error) {
	if s == "" {
		return nil, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, &metadataUnmarshalError{Err: err}
	}
	return m, nil
}
