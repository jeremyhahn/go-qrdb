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

package quic

import (
	"context"
	"encoding/json"
	"strconv"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.VectorClient = (*Transport)(nil)

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

// indexTypeToProto converts transport VectorIndexType strings to proto
// VectorIndexType values.
var indexTypeToProto = map[string]pb.VectorIndexType{
	transport.IndexHNSW:    pb.VectorIndexType_INDEX_HNSW,
	transport.IndexFlat:    pb.VectorIndexType_INDEX_FLAT,
	transport.IndexIVFFlat: pb.VectorIndexType_INDEX_IVF_FLAT,
}

// protoIndexTypeToTransport converts proto VectorIndexType values to
// transport VectorIndexType strings.
var protoIndexTypeToTransport = map[pb.VectorIndexType]string{
	pb.VectorIndexType_INDEX_HNSW:     transport.IndexHNSW,
	pb.VectorIndexType_INDEX_FLAT:     transport.IndexFlat,
	pb.VectorIndexType_INDEX_IVF_FLAT: transport.IndexIVFFlat,
}

// ---------------------------------------------------------------------------
// Namespace operations
// ---------------------------------------------------------------------------

// CreateNamespace creates a new vector namespace through the QUIC
// transport with the specified dimensionality, distance metric, and
// index configuration.
func (t *Transport) CreateNamespace(ctx context.Context, req *transport.CreateNamespaceRequest) (*transport.CreateNamespaceResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	indexParamsJSON, err := marshalMetadataJSON(req.IndexConfig)
	if err != nil {
		return nil, err
	}

	payload, err := proto.Marshal(&pb.VectorCreateNamespaceRequest{
		Namespace:       req.Namespace,
		Dimension:       int32(req.Dimension),
		DistanceMetric:  distanceMetricToProto[req.DistanceMetric],
		IndexType:       indexTypeToProto[req.IndexType],
		IndexParamsJson: indexParamsJSON,
		ShardId:         req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.CreateNamespace", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorCreateNamespaceResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return &transport.CreateNamespaceResponse{
		Namespace: req.Namespace,
		Dimension: req.Dimension,
		Timestamp: pbResp.GetTimestamp(),
		Success:   pbResp.GetCreated(),
	}, nil
}

// UpdateNamespace changes a namespace's index parameters and metadata in
// place, keeping every vector.
func (t *Transport) UpdateNamespace(ctx context.Context, req *transport.UpdateNamespaceRequest) (*transport.UpdateNamespaceResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	indexParamsJSON, err := marshalMetadataJSON(req.IndexConfig)
	if err != nil {
		return nil, err
	}
	metadataJSON, err := marshalInterfaceMetadataJSON(req.Metadata)
	if err != nil {
		return nil, err
	}

	payload, err := proto.Marshal(&pb.VectorUpdateNamespaceRequest{
		Namespace:       req.Namespace,
		Dimension:       int32(req.Dimension),
		IndexParamsJson: indexParamsJSON,
		MetadataJson:    metadataJSON,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.UpdateNamespace", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorUpdateNamespaceResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return &transport.UpdateNamespaceResponse{
		Namespace:       req.Namespace,
		RequiresRebuild: pbResp.GetRequiresRebuild(),
		Timestamp:       pbResp.GetTimestamp(),
		Success:         pbResp.GetUpdated(),
	}, nil
}

// DeleteNamespace removes a namespace and all its vectors through the
// QUIC transport.
func (t *Transport) DeleteNamespace(ctx context.Context, req *transport.DeleteNamespaceRequest) (*transport.DeleteNamespaceResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.VectorDeleteNamespaceRequest{
		Namespace: req.Namespace,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	_, err = t.call(ctx, "vector.DeleteNamespace", payload)
	if err != nil {
		return nil, err
	}

	return &transport.DeleteNamespaceResponse{Success: true}, nil
}

// ListNamespaces returns all vector namespaces through the QUIC
// transport.
func (t *Transport) ListNamespaces(ctx context.Context, req *transport.ListNamespacesRequest) (*transport.ListNamespacesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.VectorListNamespacesRequest{
		ShardId: req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.ListNamespaces", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorListNamespacesResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	namespaces := make([]*transport.NamespaceInfo, len(pbResp.GetNamespaces()))
	for i, name := range pbResp.GetNamespaces() {
		namespaces[i] = &transport.NamespaceInfo{
			Namespace: name,
		}
	}

	return &transport.ListNamespacesResponse{
		Namespaces: namespaces,
		TotalCount: len(namespaces),
	}, nil
}

// GetNamespaceInfo returns metadata about a namespace through the
// QUIC transport including dimension, distance metric, index type,
// and vector count.
func (t *Transport) GetNamespaceInfo(ctx context.Context, req *transport.GetNamespaceInfoRequest) (*transport.GetNamespaceInfoResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.VectorGetNamespaceInfoRequest{
		Namespace: req.Namespace,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.GetNamespaceInfo", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorNamespaceInfo
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return protoToNamespaceInfo(&pbResp)
}

// protoToNamespaceInfo converts a namespace-info reply, decoding its
// metadata JSON.
func protoToNamespaceInfo(p *pb.VectorNamespaceInfo) (*transport.GetNamespaceInfoResponse, error) {
	metadata, err := unmarshalInterfaceMetadataJSON(p.GetMetadataJson())
	if err != nil {
		return nil, err
	}
	return &transport.GetNamespaceInfoResponse{
		Namespace:      p.GetNamespace(),
		Dimension:      uint32(p.GetDimension()),
		VectorCount:    uint64(p.GetVectorCount()),
		DistanceMetric: protoDistanceMetricToTransport[p.GetDistanceMetric()],
		IndexType:      protoIndexTypeToTransport[p.GetIndexType()],
		IndexConfig:    p.GetIndexConfig(),
		Metadata:       metadata,
		CreatedAt:      p.GetCreatedAt(),
		UpdatedAt:      p.GetUpdatedAt(),
	}, nil
}

// ---------------------------------------------------------------------------
// Vector CRUD operations
// ---------------------------------------------------------------------------

// UpsertVector inserts or updates a vector in a namespace through the
// QUIC transport.
func (t *Transport) UpsertVector(ctx context.Context, req *transport.UpsertVectorRequest) (*transport.UpsertVectorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	metadataJSON, err := marshalInterfaceMetadataJSON(req.Metadata)
	if err != nil {
		return nil, err
	}

	payload, err := proto.Marshal(&pb.VectorUpsertRequest{
		Namespace:    req.Namespace,
		Id:           formatVectorID(req.ID),
		Vector:       req.Vector,
		MetadataJson: metadataJSON,
		ShardId:      req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.Upsert", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorUpsertResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return &transport.UpsertVectorResponse{
		ID:        req.ID,
		Version:   uint64(pbResp.GetVersion()),
		Timestamp: pbResp.GetTimestamp(),
		Success:   pbResp.GetCreated(),
	}, nil
}

// GetVector retrieves a single vector by ID through the QUIC
// transport.
func (t *Transport) GetVector(ctx context.Context, req *transport.GetVectorRequest) (*transport.GetVectorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.VectorGetRequest{
		Namespace:   req.Namespace,
		Id:          formatVectorID(req.ID),
		Consistency: vectorConsistencyToProto(req.Consistency),
		ShardId:     req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.Get", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorDataProto
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	metadata, err := unmarshalInterfaceMetadataJSON(pbResp.GetMetadataJson())
	if err != nil {
		return nil, err
	}

	id, err := parseVectorID(pbResp.GetId())
	if err != nil {
		return nil, err
	}

	return &transport.GetVectorResponse{
		ID:        id,
		Vector:    pbResp.GetVector(),
		Metadata:  metadata,
		Version:   uint64(pbResp.GetVersion()),
		CreatedAt: pbResp.GetCreatedAt(),
		UpdatedAt: pbResp.GetUpdatedAt(),
		Found:     true,
	}, nil
}

// DeleteVector removes a single vector by ID through the QUIC
// transport.
func (t *Transport) DeleteVector(ctx context.Context, req *transport.DeleteVectorRequest) (*transport.DeleteVectorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.VectorDeleteRequest{
		Namespace: req.Namespace,
		Id:        formatVectorID(req.ID),
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	_, err = t.call(ctx, "vector.Delete", payload)
	if err != nil {
		return nil, err
	}

	return &transport.DeleteVectorResponse{Success: true}, nil
}

// BatchUpsert inserts or updates multiple vectors through the QUIC
// transport.
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

	payload, err := proto.Marshal(&pb.VectorBatchUpsertRequest{
		Namespace: req.Namespace,
		Vectors:   pbVectors,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.BatchUpsert", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorBatchUpsertResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return &transport.BatchUpsertResponse{
		InsertedCount: int(pbResp.GetSucceeded()),
		Success:       pbResp.GetFailed() == 0,
	}, nil
}

// BatchDeleteVectors removes multiple vectors by ID through the QUIC
// transport.
func (t *Transport) BatchDeleteVectors(ctx context.Context, req *transport.BatchDeleteVectorsRequest) (*transport.BatchDeleteVectorsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	ids := make([]string, len(req.IDs))
	for i, id := range req.IDs {
		ids[i] = formatVectorID(id)
	}

	payload, err := proto.Marshal(&pb.VectorBatchDeleteRequest{
		Namespace: req.Namespace,
		Ids:       ids,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.BatchDelete", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorBatchDeleteResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	var failedIDs []uint64
	for _, e := range pbResp.GetErrors() {
		id, parseErr := parseVectorID(e.GetId())
		if parseErr == nil {
			failedIDs = append(failedIDs, id)
		}
	}

	return &transport.BatchDeleteVectorsResponse{
		DeletedCount: int(pbResp.GetDeleted()),
		FailedIDs:    failedIDs,
		Success:      pbResp.GetFailed() == 0,
	}, nil
}

// ListVectors returns a paginated list of vectors through the QUIC
// transport.
func (t *Transport) ListVectors(ctx context.Context, req *transport.ListVectorsRequest) (*transport.ListVectorsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.VectorListRequest{
		Namespace: req.Namespace,
		Limit:     int32(req.Limit),
		Cursor:    req.Cursor,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.List", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorListResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	vectors := make([]*transport.VectorData, len(pbResp.GetVectors()))
	for i, v := range pbResp.GetVectors() {
		vd, err := protoToVectorData(v)
		if err != nil {
			return nil, err
		}
		vectors[i] = vd
	}

	return &transport.ListVectorsResponse{
		Vectors:    vectors,
		NextCursor: pbResp.GetNextCursor(),
		TotalCount: uint64(pbResp.GetTotal()),
	}, nil
}

// UpdateMetadata updates the metadata for an existing vector through
// the QUIC transport without modifying the vector data itself.
func (t *Transport) UpdateMetadata(ctx context.Context, req *transport.UpdateVectorMetadataRequest) (*transport.UpdateVectorMetadataResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	metadataJSON, err := marshalInterfaceMetadataJSON(req.Metadata)
	if err != nil {
		return nil, err
	}

	payload, err := proto.Marshal(&pb.VectorUpdateMetadataRequest{
		Namespace:    req.Namespace,
		Id:           formatVectorID(req.ID),
		MetadataJson: metadataJSON,
		ShardId:      req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	_, err = t.call(ctx, "vector.UpdateMetadata", payload)
	if err != nil {
		return nil, err
	}

	return &transport.UpdateVectorMetadataResponse{
		ID:      req.ID,
		Success: true,
	}, nil
}

// ---------------------------------------------------------------------------
// Search operations
// ---------------------------------------------------------------------------

// SearchSimilar performs a k-nearest-neighbor search through the QUIC
// transport using a query vector.
func (t *Transport) SearchSimilar(ctx context.Context, req *transport.SearchSimilarRequest) (*transport.SearchSimilarResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.VectorSearchSimilarRequest{
		Namespace:   req.Namespace,
		Vector:      req.QueryVector,
		K:           int32(req.K),
		EfSearch:    int32(req.EfSearch),
		MinScore:    float64(req.MinScore),
		Consistency: vectorConsistencyToProto(req.Consistency),
		ShardId:     req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.SearchSimilar", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorSearchSimilarResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	results, err := protoToSearchResults(pbResp.GetResults())
	if err != nil {
		return nil, err
	}

	return &transport.SearchSimilarResponse{
		Results:       results,
		QueryDuration: pbResp.GetTook(),
	}, nil
}

// SearchByMetadata returns vectors matching metadata filters through
// the QUIC transport.
func (t *Transport) SearchByMetadata(ctx context.Context, req *transport.SearchByMetadataRequest) (*transport.SearchByMetadataResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	filtersJSON, err := marshalInterfaceMetadataJSON(req.Filters)
	if err != nil {
		return nil, err
	}

	payload, err := proto.Marshal(&pb.VectorSearchByMetadataRequest{
		Namespace:   req.Namespace,
		FiltersJson: filtersJSON,
		Limit:       int32(req.Limit),
		Offset:      int32(req.Offset),
		Consistency: vectorConsistencyToProto(req.Consistency),
		ShardId:     req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.SearchByMetadata", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorSearchByMetadataResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	results := make([]*transport.VectorData, len(pbResp.GetResults()))
	for i, v := range pbResp.GetResults() {
		vd, err := protoToVectorData(v)
		if err != nil {
			return nil, err
		}
		results[i] = vd
	}

	return &transport.SearchByMetadataResponse{
		Results:    results,
		TotalCount: uint64(pbResp.GetTotal()),
	}, nil
}

// HybridSearch combines vector similarity and metadata filtering
// through the QUIC transport with configurable weights for each
// scoring component.
func (t *Transport) HybridSearch(ctx context.Context, req *transport.HybridSearchRequest) (*transport.HybridSearchResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	metadataFiltersJSON, err := marshalInterfaceMetadataJSON(req.MetadataFilter)
	if err != nil {
		return nil, err
	}

	payload, err := proto.Marshal(&pb.VectorHybridSearchRequest{
		Namespace:           req.Namespace,
		Vector:              req.QueryVector,
		MetadataFiltersJson: metadataFiltersJSON,
		K:                   int32(req.K),
		Consistency:         vectorConsistencyToProto(req.Consistency),
		ShardId:             req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.HybridSearch", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorHybridSearchResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	results, err := protoToSearchResults(pbResp.GetResults())
	if err != nil {
		return nil, err
	}

	return &transport.HybridSearchResponse{
		Results:       results,
		QueryDuration: pbResp.GetTook(),
	}, nil
}

// SearchByID searches using an existing vector's ID as the query
// through the QUIC transport, returning its most similar neighbors.
func (t *Transport) SearchByID(ctx context.Context, req *transport.SearchByIDRequest) (*transport.SearchByIDResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.VectorSearchByIDRequest{
		Namespace:   req.Namespace,
		Id:          formatVectorID(req.ID),
		K:           int32(req.K),
		Consistency: vectorConsistencyToProto(req.Consistency),
		ShardId:     req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.SearchByID", payload)
	if err != nil {
		return nil, err
	}

	// Server reuses VectorSearchSimilarResponse for SearchByID.
	var pbResp pb.VectorSearchSimilarResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	results, err := protoToSearchResults(pbResp.GetResults())
	if err != nil {
		return nil, err
	}

	return &transport.SearchByIDResponse{
		SourceID:      req.ID,
		Results:       results,
		QueryDuration: pbResp.GetTook(),
	}, nil
}

// ---------------------------------------------------------------------------
// Index management operations
// ---------------------------------------------------------------------------

// RebuildIndex triggers an asynchronous index rebuild for a namespace
// through the QUIC transport.
func (t *Transport) RebuildIndex(ctx context.Context, req *transport.RebuildIndexRequest) (*transport.RebuildIndexResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.VectorRebuildIndexRequest{
		Namespace: req.Namespace,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.RebuildIndex", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorRebuildIndexResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	// pb.VectorRebuildIndexRequest carries no Async field, so the QUIC
	// handleVectorRebuildIndex handler always builds a
	// service.RebuildIndexRequest with Async left at its zero value
	// (false); vectorService.RebuildIndex therefore always runs
	// performIndexRebuild synchronously and has already completed by the
	// time this call returns -- report "completed", matching the REST
	// transport's decoded response (which does forward the server's actual
	// Status) instead of the "running" this client used to hardcode
	// regardless of the true, always-synchronous outcome.
	return &transport.RebuildIndexResponse{
		JobID:     pbResp.GetJobId(),
		Status:    "completed",
		Timestamp: pbResp.GetTimestamp(),
		Success:   pbResp.GetStarted(),
	}, nil
}

// GetRebuildProgress returns the current progress of an index rebuild
// through the QUIC transport.
func (t *Transport) GetRebuildProgress(ctx context.Context, req *transport.GetRebuildProgressRequest) (*transport.GetRebuildProgressResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.VectorGetRebuildProgressRequest{
		Namespace: req.Namespace,
		JobId:     req.JobID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.GetRebuildProgress", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorRebuildProgress
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return &transport.GetRebuildProgressResponse{
		JobID:           pbResp.GetJobId(),
		Status:          pbResp.GetStatus(),
		PercentComplete: pbResp.GetProgress(),
		StartedAt:       pbResp.GetStartedAt(),
		CompletedAt:     pbResp.GetFinishedAt(),
		Error:           pbResp.GetError(),
	}, nil
}

// GetIndexStats returns statistics about a namespace's vector index
// through the QUIC transport including vector count, memory usage,
// and disk usage.
func (t *Transport) GetIndexStats(ctx context.Context, req *transport.GetIndexStatsRequest) (*transport.GetIndexStatsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.VectorGetIndexStatsRequest{
		Namespace: req.Namespace,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.GetIndexStats", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorIndexStats
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return &transport.GetIndexStatsResponse{
		Namespace:        req.Namespace,
		IndexType:        protoIndexTypeToTransport[pbResp.GetIndexType()],
		VectorCount:      uint64(pbResp.GetVectorCount()),
		Dimension:        uint32(pbResp.GetDimension()),
		IndexSizeBytes:   uint64(pbResp.GetDiskUsage()),
		MemoryUsageBytes: uint64(pbResp.GetMemoryUsage()),
	}, nil
}

// ---------------------------------------------------------------------------
// Compaction operations
// ---------------------------------------------------------------------------

// CompactNamespace triggers compaction of a namespace's storage
// through the QUIC transport to reclaim space from deleted vectors.
func (t *Transport) CompactNamespace(ctx context.Context, req *transport.CompactNamespaceRequest) (*transport.CompactNamespaceResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.VectorCompactNamespaceRequest{
		Namespace: req.Namespace,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.CompactNamespace", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorCompactNamespaceResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return &transport.CompactNamespaceResponse{
		OperationID: pbResp.GetJobId(),
		Success:     pbResp.GetStarted(),
	}, nil
}

// GetCompactionProgress returns the current progress of a compaction
// through the QUIC transport.
func (t *Transport) GetCompactionProgress(ctx context.Context, req *transport.GetCompactionProgressRequest) (*transport.GetCompactionProgressResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.VectorGetCompactionProgressRequest{
		Namespace: req.Namespace,
		JobId:     req.OperationID,
		ShardId:   req.ShardID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "vector.GetCompactionProgress", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.VectorCompactionProgress
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	inProgress := pbResp.GetStatus() == "running"
	var completedAt *int64
	if finishedAt := pbResp.GetFinishedAt(); finishedAt != 0 {
		completedAt = &finishedAt
	}

	return &transport.GetCompactionProgressResponse{
		OperationID:     pbResp.GetJobId(),
		Namespace:       req.Namespace,
		PercentComplete: pbResp.GetProgress(),
		InProgress:      inProgress,
		StartedAt:       pbResp.GetStartedAt(),
		CompletedAt:     completedAt,
		Error:           pbResp.GetError(),
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
	return strconv.ParseUint(id, 10, 64)
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

// marshalMetadataJSON serializes a map[string]string to a JSON string.
// Returns an empty string for nil or empty maps.
func marshalMetadataJSON(m map[string]string) (string, error) {
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

// metadataMarshalError is returned when JSON marshaling of metadata fails.
type metadataMarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *metadataMarshalError) Error() string {
	return "quic transport: failed to marshal metadata: " + e.Err.Error()
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
	return "quic transport: failed to unmarshal metadata: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *metadataUnmarshalError) Unwrap() error {
	return e.Err
}
