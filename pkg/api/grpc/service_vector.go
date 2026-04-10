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

package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Enum conversion maps
// ---------------------------------------------------------------------------

// protoDistanceMetricToString converts proto DistanceMetric values to service
// string-based distance metric identifiers.
var protoDistanceMetricToString = map[pb.DistanceMetric]string{
	pb.DistanceMetric_DISTANCE_COSINE:      "COSINE",
	pb.DistanceMetric_DISTANCE_EUCLIDEAN:   "L2",
	pb.DistanceMetric_DISTANCE_DOT_PRODUCT: "DOT_PRODUCT",
}

// stringToProtoDistanceMetric converts service string-based distance metric
// identifiers to proto DistanceMetric values.
var stringToProtoDistanceMetric = map[string]pb.DistanceMetric{
	"COSINE":      pb.DistanceMetric_DISTANCE_COSINE,
	"L2":          pb.DistanceMetric_DISTANCE_EUCLIDEAN,
	"DOT_PRODUCT": pb.DistanceMetric_DISTANCE_DOT_PRODUCT,
}

// protoIndexTypeToString converts proto VectorIndexType values to service
// string-based index type identifiers.
var protoIndexTypeToString = map[pb.VectorIndexType]string{
	pb.VectorIndexType_INDEX_HNSW:     "HNSW",
	pb.VectorIndexType_INDEX_FLAT:     "FLAT",
	pb.VectorIndexType_INDEX_IVF_FLAT: "IVF",
}

// stringToProtoIndexType converts service string-based index type identifiers
// to proto VectorIndexType values.
var stringToProtoIndexType = map[string]pb.VectorIndexType{
	"HNSW": pb.VectorIndexType_INDEX_HNSW,
	"FLAT": pb.VectorIndexType_INDEX_FLAT,
	"IVF":  pb.VectorIndexType_INDEX_IVF_FLAT,
}

// ---------------------------------------------------------------------------
// ID conversion helpers
// ---------------------------------------------------------------------------

// parseVectorID converts a proto string ID to the service uint64 ID.
func parseVectorID(id string) (uint64, error) {
	if id == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return 0, &VectorIDParseError{ID: id, Err: err}
	}
	return parsed, nil
}

// formatVectorID converts a service uint64 ID to the proto string ID.
func formatVectorID(id uint64) string {
	return strconv.FormatUint(id, 10)
}

// ---------------------------------------------------------------------------
// Type conversion helpers
// ---------------------------------------------------------------------------

// vectorDataToProto converts a service-layer VectorData to its protobuf
// representation. Metadata is serialized as JSON.
func vectorDataToProto(v *service.VectorData) (*pb.VectorDataProto, error) {
	if v == nil {
		return nil, nil
	}
	metadataJSON, err := marshalMetadata(v.Metadata)
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

// getVectorResponseToProto converts a service-layer GetVectorResponse to its
// protobuf representation. Metadata is serialized as JSON.
func getVectorResponseToProto(v *service.GetVectorResponse) (*pb.VectorDataProto, error) {
	if v == nil {
		return nil, nil
	}
	metadataJSON, err := marshalMetadata(v.Metadata)
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

// protoToVectorData converts a proto VectorDataProto to a service-layer
// VectorData pointer. Metadata is deserialized from JSON.
func protoToVectorData(v *pb.VectorDataProto) (*service.VectorData, error) {
	metadata, err := unmarshalMetadata(v.GetMetadataJson())
	if err != nil {
		return nil, err
	}
	id, err := parseVectorID(v.GetId())
	if err != nil {
		return nil, err
	}
	return &service.VectorData{
		ID:        id,
		Vector:    v.GetVector(),
		Metadata:  metadata,
		Version:   uint64(v.GetVersion()),
		CreatedAt: v.GetCreatedAt(),
		UpdatedAt: v.GetUpdatedAt(),
	}, nil
}

// searchResultToProto converts a service-layer VectorSearchResult to its
// protobuf representation.
func searchResultToProto(r *service.VectorSearchResult) (*pb.VectorSearchResultProto, error) {
	if r == nil {
		return nil, nil
	}
	metadataJSON, err := marshalMetadata(r.Metadata)
	if err != nil {
		return nil, err
	}
	return &pb.VectorSearchResultProto{
		Id:           formatVectorID(r.ID),
		Score:        float64(r.Score),
		Vector:       r.Vector,
		MetadataJson: metadataJSON,
	}, nil
}

// marshalMetadata serializes a metadata map to JSON. Returns an empty string
// for nil or empty maps.
func marshalMetadata(m map[string]interface{}) (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return "", &MetadataMarshalError{Err: err}
	}
	return string(data), nil
}

// unmarshalMetadata deserializes a JSON string into a metadata map. Returns
// nil for empty strings.
func unmarshalMetadata(s string) (map[string]interface{}, error) {
	if s == "" {
		return nil, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, &MetadataUnmarshalError{Err: err}
	}
	return m, nil
}

// indexParamsToStringMap converts a map[string]interface{} to map[string]string
// for the service-layer IndexConfig field.
func indexParamsToStringMap(params map[string]interface{}) map[string]string {
	if len(params) == 0 {
		return nil
	}
	result := make(map[string]string, len(params))
	for k, v := range params {
		result[k] = fmt.Sprintf("%v", v)
	}
	return result
}

// parseStringIDs converts a slice of string IDs to uint64 IDs.
func parseStringIDs(ids []string) ([]uint64, error) {
	result := make([]uint64, len(ids))
	for i, id := range ids {
		parsed, err := parseVectorID(id)
		if err != nil {
			return nil, err
		}
		result[i] = parsed
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Namespace handlers
// ---------------------------------------------------------------------------

// VectorCreateNamespace creates a new vector namespace with the specified
// dimensionality, distance metric, and index configuration.
func (s *qrdbServiceServer) VectorCreateNamespace(ctx context.Context, req *pb.VectorCreateNamespaceRequest) (*pb.VectorCreateNamespaceResponse, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	var indexConfig map[string]string
	if req.GetIndexParamsJson() != "" {
		params, err := unmarshalMetadata(req.GetIndexParamsJson())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		indexConfig = indexParamsToStringMap(params)
	}

	resp, err := s.vectorSvc.CreateNamespace(ctx, &service.CreateNamespaceRequest{
		Namespace:      req.GetNamespace(),
		Dimension:      uint32(req.GetDimension()),
		DistanceMetric: protoDistanceMetricToString[req.GetDistanceMetric()],
		IndexType:      protoIndexTypeToString[req.GetIndexType()],
		IndexConfig:    indexConfig,
		ShardID:        req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.VectorCreateNamespaceResponse{
		Created:   resp.Success,
		Timestamp: resp.Timestamp,
	}, nil
}

// VectorDeleteNamespace removes a namespace and all its vectors.
func (s *qrdbServiceServer) VectorDeleteNamespace(ctx context.Context, req *pb.VectorDeleteNamespaceRequest) (*emptypb.Empty, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	_, err := s.vectorSvc.DeleteNamespace(ctx, &service.DeleteNamespaceRequest{
		Namespace: req.GetNamespace(),
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// VectorListNamespaces returns all vector namespace names.
func (s *qrdbServiceServer) VectorListNamespaces(ctx context.Context, req *pb.VectorListNamespacesRequest) (*pb.VectorListNamespacesResponse, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	resp, err := s.vectorSvc.ListNamespaces(ctx, &service.ListNamespacesRequest{
		ShardID: req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	names := make([]string, len(resp.Namespaces))
	for i, ns := range resp.Namespaces {
		names[i] = ns.Namespace
	}

	return &pb.VectorListNamespacesResponse{
		Namespaces: names,
	}, nil
}

// VectorGetNamespaceInfo returns metadata about a namespace including
// dimension, distance metric, index type, and vector count.
func (s *qrdbServiceServer) VectorGetNamespaceInfo(ctx context.Context, req *pb.VectorGetNamespaceInfoRequest) (*pb.VectorNamespaceInfo, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	info, err := s.vectorSvc.GetNamespaceInfo(ctx, &service.GetNamespaceInfoRequest{
		Namespace: req.GetNamespace(),
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.VectorNamespaceInfo{
		Namespace:      info.Namespace,
		Dimension:      int32(info.Dimension),
		DistanceMetric: stringToProtoDistanceMetric[info.DistanceMetric],
		IndexType:      stringToProtoIndexType[info.IndexType],
		VectorCount:    int64(info.VectorCount),
		CreatedAt:      info.CreatedAt,
		UpdatedAt:      info.UpdatedAt,
	}, nil
}

// ---------------------------------------------------------------------------
// Vector CRUD handlers
// ---------------------------------------------------------------------------

// VectorUpsert inserts or updates a vector in a namespace.
func (s *qrdbServiceServer) VectorUpsert(ctx context.Context, req *pb.VectorUpsertRequest) (*pb.VectorUpsertResponse, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	metadata, err := unmarshalMetadata(req.GetMetadataJson())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	id, err := parseVectorID(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	resp, err := s.vectorSvc.UpsertVector(ctx, &service.UpsertVectorRequest{
		Namespace: req.GetNamespace(),
		ID:        id,
		Vector:    req.GetVector(),
		Metadata:  metadata,
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.VectorUpsertResponse{
		Version:   int64(resp.Version),
		Timestamp: resp.Timestamp,
		Created:   resp.Success,
	}, nil
}

// VectorGet retrieves a single vector by ID from the specified namespace.
func (s *qrdbServiceServer) VectorGet(ctx context.Context, req *pb.VectorGetRequest) (*pb.VectorDataProto, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	id, err := parseVectorID(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	data, err := s.vectorSvc.GetVector(ctx, &service.GetVectorRequest{
		Namespace:       req.GetNamespace(),
		ID:              id,
		IncludeVector:   true,
		IncludeMetadata: true,
		Consistency:     service.ConsistencyLevel(req.GetConsistency()),
		ShardID:         req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbData, err := getVectorResponseToProto(data)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return pbData, nil
}

// VectorDelete removes a single vector by ID from the specified namespace.
func (s *qrdbServiceServer) VectorDelete(ctx context.Context, req *pb.VectorDeleteRequest) (*emptypb.Empty, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	id, err := parseVectorID(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	_, err = s.vectorSvc.DeleteVector(ctx, &service.DeleteVectorRequest{
		Namespace: req.GetNamespace(),
		ID:        id,
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// VectorBatchUpsert inserts or updates multiple vectors in a single operation.
func (s *qrdbServiceServer) VectorBatchUpsert(ctx context.Context, req *pb.VectorBatchUpsertRequest) (*pb.VectorBatchUpsertResponse, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	vectors := make([]*service.VectorData, len(req.GetVectors()))
	for i, v := range req.GetVectors() {
		vd, err := protoToVectorData(v)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		vectors[i] = vd
	}

	resp, err := s.vectorSvc.BatchUpsert(ctx, &service.BatchUpsertRequest{
		Namespace: req.GetNamespace(),
		Vectors:   vectors,
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.VectorBatchUpsertResponse{
		Succeeded: int32(resp.InsertedCount),
		Failed:    int32(len(vectors) - resp.InsertedCount),
	}, nil
}

// VectorBatchDelete removes multiple vectors by ID in a single operation.
func (s *qrdbServiceServer) VectorBatchDelete(ctx context.Context, req *pb.VectorBatchDeleteRequest) (*pb.VectorBatchDeleteResponse, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	ids, err := parseStringIDs(req.GetIds())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	resp, err := s.vectorSvc.BatchDeleteVectors(ctx, &service.BatchDeleteVectorsRequest{
		Namespace: req.GetNamespace(),
		IDs:       ids,
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbErrors := make([]*pb.VectorBatchError, len(resp.FailedIDs))
	for i, failedID := range resp.FailedIDs {
		pbErrors[i] = &pb.VectorBatchError{
			Id: formatVectorID(failedID),
		}
	}

	return &pb.VectorBatchDeleteResponse{
		Deleted: int32(resp.DeletedCount),
		Failed:  int32(len(resp.FailedIDs)),
		Errors:  pbErrors,
	}, nil
}

// VectorList returns a paginated list of vectors in a namespace.
func (s *qrdbServiceServer) VectorList(ctx context.Context, req *pb.VectorListRequest) (*pb.VectorListResponse, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	resp, err := s.vectorSvc.ListVectors(ctx, &service.ListVectorsRequest{
		Namespace: req.GetNamespace(),
		Limit:     int(req.GetLimit()),
		Cursor:    req.GetCursor(),
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbVectors := make([]*pb.VectorDataProto, len(resp.Vectors))
	for i, v := range resp.Vectors {
		pbV, err := vectorDataToProto(v)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		pbVectors[i] = pbV
	}

	return &pb.VectorListResponse{
		Vectors:    pbVectors,
		NextCursor: resp.NextCursor,
		Total:      int64(resp.TotalCount),
	}, nil
}

// VectorUpdateMetadata updates the metadata for an existing vector without
// modifying the vector data itself.
func (s *qrdbServiceServer) VectorUpdateMetadata(ctx context.Context, req *pb.VectorUpdateMetadataRequest) (*emptypb.Empty, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	metadata, err := unmarshalMetadata(req.GetMetadataJson())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	id, err := parseVectorID(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	_, err = s.vectorSvc.UpdateMetadata(ctx, &service.UpdateVectorMetadataRequest{
		Namespace: req.GetNamespace(),
		ID:        id,
		Metadata:  metadata,
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// ---------------------------------------------------------------------------
// Search handlers
// ---------------------------------------------------------------------------

// VectorSearchSimilar performs a k-nearest-neighbor search using a query vector.
func (s *qrdbServiceServer) VectorSearchSimilar(ctx context.Context, req *pb.VectorSearchSimilarRequest) (*pb.VectorSearchSimilarResponse, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	resp, err := s.vectorSvc.SearchSimilar(ctx, &service.SearchSimilarRequest{
		Namespace:   req.GetNamespace(),
		QueryVector: req.GetVector(),
		K:           int(req.GetK()),
		EfSearch:    int(req.GetEfSearch()),
		MinScore:    float32(req.GetMinScore()),
		Consistency: service.ConsistencyLevel(req.GetConsistency()),
		ShardID:     req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbResults, err := searchResultsToProto(resp.Results)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &pb.VectorSearchSimilarResponse{
		Results: pbResults,
		Took:    resp.QueryDuration,
	}, nil
}

// VectorSearchByMetadata returns vectors matching the given metadata filters.
func (s *qrdbServiceServer) VectorSearchByMetadata(ctx context.Context, req *pb.VectorSearchByMetadataRequest) (*pb.VectorSearchByMetadataResponse, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	filters, err := unmarshalMetadata(req.GetFiltersJson())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	resp, err := s.vectorSvc.SearchByMetadata(ctx, &service.SearchByMetadataRequest{
		Namespace:   req.GetNamespace(),
		Filters:     filters,
		Limit:       int(req.GetLimit()),
		Offset:      int(req.GetOffset()),
		Consistency: service.ConsistencyLevel(req.GetConsistency()),
		ShardID:     req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbResults := make([]*pb.VectorDataProto, len(resp.Results))
	for i, v := range resp.Results {
		pbV, err := vectorDataToProto(v)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		pbResults[i] = pbV
	}

	return &pb.VectorSearchByMetadataResponse{
		Results: pbResults,
		Total:   int64(resp.TotalCount),
	}, nil
}

// VectorHybridSearch combines vector similarity and metadata filtering with
// configurable weights for each scoring component.
func (s *qrdbServiceServer) VectorHybridSearch(ctx context.Context, req *pb.VectorHybridSearchRequest) (*pb.VectorHybridSearchResponse, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	metadataFilter, err := unmarshalMetadata(req.GetMetadataFiltersJson())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	resp, err := s.vectorSvc.HybridSearch(ctx, &service.HybridSearchRequest{
		Namespace:      req.GetNamespace(),
		QueryVector:    req.GetVector(),
		MetadataFilter: metadataFilter,
		K:              int(req.GetK()),
		Consistency:    service.ConsistencyLevel(req.GetConsistency()),
		ShardID:        req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbResults, err := searchResultsToProto(resp.Results)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &pb.VectorHybridSearchResponse{
		Results: pbResults,
		Took:    resp.QueryDuration,
	}, nil
}

// VectorSearchByID performs a k-nearest-neighbor search using an existing
// vector's ID as the query, returning its most similar neighbors.
func (s *qrdbServiceServer) VectorSearchByID(ctx context.Context, req *pb.VectorSearchByIDRequest) (*pb.VectorSearchSimilarResponse, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	id, err := parseVectorID(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	resp, err := s.vectorSvc.SearchByID(ctx, &service.SearchByIDRequest{
		Namespace:   req.GetNamespace(),
		ID:          id,
		K:           int(req.GetK()),
		Consistency: service.ConsistencyLevel(req.GetConsistency()),
		ShardID:     req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbResults, err := searchResultsToProto(resp.Results)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &pb.VectorSearchSimilarResponse{
		Results: pbResults,
		Took:    resp.QueryDuration,
	}, nil
}

// searchResultsToProto converts a slice of service-layer VectorSearchResult
// to their protobuf representations.
func searchResultsToProto(results []*service.VectorSearchResult) ([]*pb.VectorSearchResultProto, error) {
	pbResults := make([]*pb.VectorSearchResultProto, len(results))
	for i := range results {
		pbR, err := searchResultToProto(results[i])
		if err != nil {
			return nil, err
		}
		pbResults[i] = pbR
	}
	return pbResults, nil
}

// ---------------------------------------------------------------------------
// Index management handlers
// ---------------------------------------------------------------------------

// VectorRebuildIndex triggers an asynchronous index rebuild for a namespace.
func (s *qrdbServiceServer) VectorRebuildIndex(ctx context.Context, req *pb.VectorRebuildIndexRequest) (*pb.VectorRebuildIndexResponse, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	resp, err := s.vectorSvc.RebuildIndex(ctx, &service.RebuildIndexRequest{
		Namespace: req.GetNamespace(),
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.VectorRebuildIndexResponse{
		JobId:     resp.JobID,
		Started:   resp.Success,
		Timestamp: resp.Timestamp,
	}, nil
}

// VectorGetRebuildProgress returns the current progress of an index rebuild job.
func (s *qrdbServiceServer) VectorGetRebuildProgress(ctx context.Context, req *pb.VectorGetRebuildProgressRequest) (*pb.VectorRebuildProgress, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	progress, err := s.vectorSvc.GetRebuildProgress(ctx, &service.GetRebuildProgressRequest{
		Namespace: req.GetNamespace(),
		JobID:     req.GetJobId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.VectorRebuildProgress{
		JobId:      progress.JobID,
		Status:     progress.Status,
		Progress:   progress.PercentComplete,
		StartedAt:  progress.StartedAt,
		FinishedAt: progress.CompletedAt,
		Error:      progress.Error,
	}, nil
}

// VectorGetIndexStats returns statistics about a namespace's vector index
// including vector count, memory usage, and disk usage.
func (s *qrdbServiceServer) VectorGetIndexStats(ctx context.Context, req *pb.VectorGetIndexStatsRequest) (*pb.VectorIndexStats, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	stats, err := s.vectorSvc.GetIndexStats(ctx, &service.GetIndexStatsRequest{
		Namespace: req.GetNamespace(),
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.VectorIndexStats{
		VectorCount:    int64(stats.VectorCount),
		Dimension:      int32(stats.Dimension),
		DistanceMetric: stringToProtoDistanceMetric[stats.IndexType],
		IndexType:      stringToProtoIndexType[stats.IndexType],
		MemoryUsage:    int64(stats.MemoryUsageBytes),
		DiskUsage:      int64(stats.IndexSizeBytes),
	}, nil
}

// ---------------------------------------------------------------------------
// Compaction handlers
// ---------------------------------------------------------------------------

// VectorCompactNamespace triggers an asynchronous compaction of a namespace's
// storage to reclaim space from deleted vectors.
func (s *qrdbServiceServer) VectorCompactNamespace(ctx context.Context, req *pb.VectorCompactNamespaceRequest) (*pb.VectorCompactNamespaceResponse, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	resp, err := s.vectorSvc.CompactNamespace(ctx, &service.CompactNamespaceRequest{
		Namespace: req.GetNamespace(),
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.VectorCompactNamespaceResponse{
		JobId:   resp.OperationID,
		Started: resp.Success,
	}, nil
}

// VectorGetCompactionProgress returns the current progress of a compaction job.
func (s *qrdbServiceServer) VectorGetCompactionProgress(ctx context.Context, req *pb.VectorGetCompactionProgressRequest) (*pb.VectorCompactionProgress, error) {
	if s.vectorSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "vector"}).Error())
	}

	progress, err := s.vectorSvc.GetCompactionProgress(ctx, &service.GetCompactionProgressRequest{
		Namespace:   req.GetNamespace(),
		OperationID: req.GetJobId(),
		ShardID:     req.GetShardId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	var finishedAt int64
	if progress.CompletedAt != nil {
		finishedAt = *progress.CompletedAt
	}

	return &pb.VectorCompactionProgress{
		JobId:      progress.OperationID,
		Status:     progressStatusString(progress.InProgress),
		Progress:   progress.PercentComplete,
		StartedAt:  progress.StartedAt,
		FinishedAt: finishedAt,
		Error:      progress.Error,
	}, nil
}

// progressStatusString returns a human-readable status string based on
// whether the operation is in progress.
func progressStatusString(inProgress bool) string {
	if inProgress {
		return "running"
	}
	return "completed"
}
