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
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"

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
	pb.DistanceMetric_DISTANCE_MANHATTAN:   "MANHATTAN",
}

// stringToProtoDistanceMetric converts service string-based distance metric
// identifiers to proto DistanceMetric values.
// The keys are upper case; protoDistanceMetric folds the name it looks up.
var stringToProtoDistanceMetric = map[string]pb.DistanceMetric{
	"COSINE":      pb.DistanceMetric_DISTANCE_COSINE,
	"L2":          pb.DistanceMetric_DISTANCE_EUCLIDEAN,
	"EUCLIDEAN":   pb.DistanceMetric_DISTANCE_EUCLIDEAN,
	"DOT_PRODUCT": pb.DistanceMetric_DISTANCE_DOT_PRODUCT,
	"MANHATTAN":   pb.DistanceMetric_DISTANCE_MANHATTAN,
}

// serviceDistanceMetric converts a wire DistanceMetric to the service's name.
// A value outside the enum is refused, never read as DISTANCE_COSINE.
func serviceDistanceMetric(metric pb.DistanceMetric) (string, error) {
	if name, ok := protoDistanceMetricToString[metric]; ok {
		return name, nil
	}
	return "", &service.UnknownDistanceMetricError{Metric: metric.String()}
}

// protoDistanceMetric converts the distance metric stored for namespace to
// its proto value. The service reports the name a namespace was created with,
// which the REST and embedded transports send in lower case. A name with no
// wire value is refused, never answered as DISTANCE_COSINE.
func protoDistanceMetric(namespace, name string) (pb.DistanceMetric, error) {
	if metric, ok := stringToProtoDistanceMetric[strings.ToUpper(name)]; ok {
		return metric, nil
	}
	return 0, &service.UnsupportedNamespaceGeometryError{Namespace: namespace, Field: "distance metric", Value: name}
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

// progressStatusString returns a human-readable status string based on
// whether the operation is in progress.
func progressStatusString(inProgress bool) string {
	if inProgress {
		return "running"
	}
	return "completed"
}

// ---------------------------------------------------------------------------
// Vector error types
// ---------------------------------------------------------------------------

// VectorIDParseError is returned when a vector ID string cannot be parsed
// as a uint64.
type VectorIDParseError struct {
	ID  string
	Err error
}

// Error returns a human-readable description.
func (e *VectorIDParseError) Error() string {
	return "quic: invalid vector ID " + e.ID + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VectorIDParseError) Unwrap() error {
	return e.Err
}

// MetadataMarshalError is returned when metadata JSON marshaling fails.
type MetadataMarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *MetadataMarshalError) Error() string {
	return "quic: failed to marshal metadata: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MetadataMarshalError) Unwrap() error {
	return e.Err
}

// MetadataUnmarshalError is returned when metadata JSON unmarshaling fails.
type MetadataUnmarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *MetadataUnmarshalError) Error() string {
	return "quic: failed to unmarshal metadata: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MetadataUnmarshalError) Unwrap() error {
	return e.Err
}

// ---------------------------------------------------------------------------
// Namespace handlers
// ---------------------------------------------------------------------------

// handleVectorCreateNamespace creates a new vector namespace with the
// specified dimensionality, distance metric, and index configuration.
func (s *Server) handleVectorCreateNamespace(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorCreateNamespaceRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	var indexConfig map[string]string
	if req.GetIndexParamsJson() != "" {
		params, err := unmarshalMetadata(req.GetIndexParamsJson())
		if err != nil {
			return nil, err
		}
		indexConfig = indexParamsToStringMap(params)
	}
	metric, err := serviceDistanceMetric(req.GetDistanceMetric())
	if err != nil {
		return nil, err
	}

	resp, err := vectorSvc.CreateNamespace(ctx, &service.CreateNamespaceRequest{
		Namespace:      req.GetNamespace(),
		Dimension:      uint32(req.GetDimension()),
		DistanceMetric: metric,
		IndexType:      protoIndexTypeToString[req.GetIndexType()],
		IndexConfig:    indexConfig,
		ShardID:        req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	out, err := proto.Marshal(&pb.VectorCreateNamespaceResponse{
		Created:   resp.Success,
		Timestamp: resp.Timestamp,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVectorUpdateNamespace changes a namespace's index parameters and
// metadata in place, keeping every vector.
func (s *Server) handleVectorUpdateNamespace(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorUpdateNamespaceRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	var indexConfig map[string]string
	if req.GetIndexParamsJson() != "" {
		params, err := unmarshalMetadata(req.GetIndexParamsJson())
		if err != nil {
			return nil, err
		}
		indexConfig = indexParamsToStringMap(params)
	}
	var metadata map[string]interface{}
	if req.GetMetadataJson() != "" {
		md, err := unmarshalMetadata(req.GetMetadataJson())
		if err != nil {
			return nil, err
		}
		metadata = md
	}

	resp, err := vectorSvc.UpdateNamespace(ctx, &service.UpdateNamespaceRequest{
		Namespace:   req.GetNamespace(),
		Dimension:   uint32(req.GetDimension()),
		IndexConfig: indexConfig,
		Metadata:    metadata,
	})
	if err != nil {
		return nil, err
	}

	out, err := proto.Marshal(&pb.VectorUpdateNamespaceResponse{
		Updated:         resp.Success,
		RequiresRebuild: resp.RequiresRebuild,
		Timestamp:       resp.Timestamp,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVectorDeleteNamespace removes a namespace and all its vectors.
func (s *Server) handleVectorDeleteNamespace(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorDeleteNamespaceRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	_, err := vectorSvc.DeleteNamespace(ctx, &service.DeleteNamespaceRequest{
		Namespace: req.GetNamespace(),
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	return []byte{}, nil
}

// handleVectorListNamespaces returns all vector namespace names.
func (s *Server) handleVectorListNamespaces(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorListNamespacesRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	resp, err := vectorSvc.ListNamespaces(ctx, &service.ListNamespacesRequest{
		ShardID: req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	names := make([]string, len(resp.Namespaces))
	for i, ns := range resp.Namespaces {
		names[i] = ns.Namespace
	}

	out, err := proto.Marshal(&pb.VectorListNamespacesResponse{
		Namespaces: names,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVectorGetNamespaceInfo returns metadata about a namespace including
// dimension, distance metric, index type, and vector count.
func (s *Server) handleVectorGetNamespaceInfo(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorGetNamespaceInfoRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	info, err := vectorSvc.GetNamespaceInfo(ctx, &service.GetNamespaceInfoRequest{
		Namespace: req.GetNamespace(),
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}
	metric, err := protoDistanceMetric(req.GetNamespace(), info.DistanceMetric)
	if err != nil {
		return nil, err
	}
	metadataJSON, err := marshalMetadata(info.Metadata)
	if err != nil {
		return nil, err
	}

	out, err := proto.Marshal(&pb.VectorNamespaceInfo{
		Namespace:      info.Namespace,
		Dimension:      int32(info.Dimension),
		DistanceMetric: metric,
		IndexType:      stringToProtoIndexType[info.IndexType],
		VectorCount:    int64(info.VectorCount),
		CreatedAt:      info.CreatedAt,
		UpdatedAt:      info.UpdatedAt,
		IndexConfig:    info.IndexConfig,
		MetadataJson:   metadataJSON,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Vector CRUD handlers
// ---------------------------------------------------------------------------

// handleVectorUpsert inserts or updates a vector in a namespace.
func (s *Server) handleVectorUpsert(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorUpsertRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	metadata, err := unmarshalMetadata(req.GetMetadataJson())
	if err != nil {
		return nil, err
	}

	id, err := parseVectorID(req.GetId())
	if err != nil {
		return nil, err
	}

	resp, err := vectorSvc.UpsertVector(ctx, &service.UpsertVectorRequest{
		Namespace: req.GetNamespace(),
		ID:        id,
		Vector:    req.GetVector(),
		Metadata:  metadata,
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	out, err := proto.Marshal(&pb.VectorUpsertResponse{
		Version:   int64(resp.Version),
		Timestamp: resp.Timestamp,
		Created:   resp.Success,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVectorGet retrieves a single vector by ID from the specified namespace.
func (s *Server) handleVectorGet(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorGetRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	id, err := parseVectorID(req.GetId())
	if err != nil {
		return nil, err
	}

	data, err := vectorSvc.GetVector(ctx, &service.GetVectorRequest{
		Namespace:       req.GetNamespace(),
		ID:              id,
		IncludeVector:   true,
		IncludeMetadata: true,
		Consistency:     vectorConsistencyFromWire(req.GetConsistency()),
		ShardID:         req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	pbData, err := getVectorResponseToProto(data)
	if err != nil {
		return nil, err
	}

	out, err := proto.Marshal(pbData)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVectorDelete removes a single vector by ID from the specified namespace.
func (s *Server) handleVectorDelete(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorDeleteRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	id, err := parseVectorID(req.GetId())
	if err != nil {
		return nil, err
	}

	_, err = vectorSvc.DeleteVector(ctx, &service.DeleteVectorRequest{
		Namespace: req.GetNamespace(),
		ID:        id,
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	return []byte{}, nil
}

// handleVectorBatchUpsert inserts or updates multiple vectors in a single
// operation.
func (s *Server) handleVectorBatchUpsert(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorBatchUpsertRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	vectors := make([]*service.VectorData, len(req.GetVectors()))
	for i, v := range req.GetVectors() {
		vd, err := protoToVectorData(v)
		if err != nil {
			return nil, err
		}
		vectors[i] = vd
	}

	resp, err := vectorSvc.BatchUpsert(ctx, &service.BatchUpsertRequest{
		Namespace: req.GetNamespace(),
		Vectors:   vectors,
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	out, err := proto.Marshal(&pb.VectorBatchUpsertResponse{
		Succeeded: int32(resp.InsertedCount),
		Failed:    int32(len(vectors) - resp.InsertedCount),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVectorBatchDelete removes multiple vectors by ID in a single operation.
func (s *Server) handleVectorBatchDelete(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorBatchDeleteRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	ids, err := parseStringIDs(req.GetIds())
	if err != nil {
		return nil, err
	}

	resp, err := vectorSvc.BatchDeleteVectors(ctx, &service.BatchDeleteVectorsRequest{
		Namespace: req.GetNamespace(),
		IDs:       ids,
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	pbErrors := make([]*pb.VectorBatchError, len(resp.FailedIDs))
	for i, failedID := range resp.FailedIDs {
		pbErrors[i] = &pb.VectorBatchError{
			Id: formatVectorID(failedID),
		}
	}

	out, err := proto.Marshal(&pb.VectorBatchDeleteResponse{
		Deleted: int32(resp.DeletedCount),
		Failed:  int32(len(resp.FailedIDs)),
		Errors:  pbErrors,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVectorList returns a paginated list of vectors in a namespace.
func (s *Server) handleVectorList(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorListRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	resp, err := vectorSvc.ListVectors(ctx, &service.ListVectorsRequest{
		Namespace: req.GetNamespace(),
		Limit:     int(req.GetLimit()),
		Cursor:    req.GetCursor(),
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	pbVectors := make([]*pb.VectorDataProto, len(resp.Vectors))
	for i, v := range resp.Vectors {
		pbV, err := vectorDataToProto(v)
		if err != nil {
			return nil, err
		}
		pbVectors[i] = pbV
	}

	out, err := proto.Marshal(&pb.VectorListResponse{
		Vectors:    pbVectors,
		NextCursor: resp.NextCursor,
		Total:      int64(resp.TotalCount),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVectorUpdateMetadata updates the metadata for an existing vector
// without modifying the vector data itself.
func (s *Server) handleVectorUpdateMetadata(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorUpdateMetadataRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	metadata, err := unmarshalMetadata(req.GetMetadataJson())
	if err != nil {
		return nil, err
	}

	id, err := parseVectorID(req.GetId())
	if err != nil {
		return nil, err
	}

	_, err = vectorSvc.UpdateMetadata(ctx, &service.UpdateVectorMetadataRequest{
		Namespace: req.GetNamespace(),
		ID:        id,
		Metadata:  metadata,
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	return []byte{}, nil
}

// ---------------------------------------------------------------------------
// Search handlers
// ---------------------------------------------------------------------------

// handleVectorSearchSimilar performs a k-nearest-neighbor search using a
// query vector.
func (s *Server) handleVectorSearchSimilar(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorSearchSimilarRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	resp, err := vectorSvc.SearchSimilar(ctx, &service.SearchSimilarRequest{
		Namespace:   req.GetNamespace(),
		QueryVector: req.GetVector(),
		K:           int(req.GetK()),
		EfSearch:    int(req.GetEfSearch()),
		MinScore:    float32(req.GetMinScore()),
		Consistency: vectorConsistencyFromWire(req.GetConsistency()),
		ShardID:     req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	pbResults, err := searchResultsToProto(resp.Results)
	if err != nil {
		return nil, err
	}

	out, err := proto.Marshal(&pb.VectorSearchSimilarResponse{
		Results: pbResults,
		Took:    resp.QueryDuration,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVectorSearchByMetadata returns vectors matching the given metadata
// filters.
func (s *Server) handleVectorSearchByMetadata(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorSearchByMetadataRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	filters, err := unmarshalMetadata(req.GetFiltersJson())
	if err != nil {
		return nil, err
	}

	resp, err := vectorSvc.SearchByMetadata(ctx, &service.SearchByMetadataRequest{
		Namespace:   req.GetNamespace(),
		Filters:     filters,
		Limit:       int(req.GetLimit()),
		Offset:      int(req.GetOffset()),
		Consistency: vectorConsistencyFromWire(req.GetConsistency()),
		ShardID:     req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	pbResults := make([]*pb.VectorDataProto, len(resp.Results))
	for i, v := range resp.Results {
		pbV, err := vectorDataToProto(v)
		if err != nil {
			return nil, err
		}
		pbResults[i] = pbV
	}

	out, err := proto.Marshal(&pb.VectorSearchByMetadataResponse{
		Results: pbResults,
		Total:   int64(resp.TotalCount),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVectorHybridSearch combines vector similarity and metadata filtering
// with configurable weights for each scoring component.
func (s *Server) handleVectorHybridSearch(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorHybridSearchRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	metadataFilter, err := unmarshalMetadata(req.GetMetadataFiltersJson())
	if err != nil {
		return nil, err
	}

	resp, err := vectorSvc.HybridSearch(ctx, &service.HybridSearchRequest{
		Namespace:      req.GetNamespace(),
		QueryVector:    req.GetVector(),
		MetadataFilter: metadataFilter,
		K:              int(req.GetK()),
		Consistency:    vectorConsistencyFromWire(req.GetConsistency()),
		ShardID:        req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	pbResults, err := searchResultsToProto(resp.Results)
	if err != nil {
		return nil, err
	}

	out, err := proto.Marshal(&pb.VectorHybridSearchResponse{
		Results: pbResults,
		Took:    resp.QueryDuration,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVectorSearchByID performs a k-nearest-neighbor search using an
// existing vector's ID as the query, returning its most similar neighbors.
func (s *Server) handleVectorSearchByID(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorSearchByIDRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	id, err := parseVectorID(req.GetId())
	if err != nil {
		return nil, err
	}

	resp, err := vectorSvc.SearchByID(ctx, &service.SearchByIDRequest{
		Namespace:   req.GetNamespace(),
		ID:          id,
		K:           int(req.GetK()),
		Consistency: vectorConsistencyFromWire(req.GetConsistency()),
		ShardID:     req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	pbResults, err := searchResultsToProto(resp.Results)
	if err != nil {
		return nil, err
	}

	out, err := proto.Marshal(&pb.VectorSearchSimilarResponse{
		Results: pbResults,
		Took:    resp.QueryDuration,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Index management handlers
// ---------------------------------------------------------------------------

// handleVectorRebuildIndex triggers an asynchronous index rebuild for a
// namespace.
func (s *Server) handleVectorRebuildIndex(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorRebuildIndexRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	resp, err := vectorSvc.RebuildIndex(ctx, &service.RebuildIndexRequest{
		Namespace: req.GetNamespace(),
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	out, err := proto.Marshal(&pb.VectorRebuildIndexResponse{
		JobId:     resp.JobID,
		Started:   resp.Success,
		Timestamp: resp.Timestamp,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVectorGetRebuildProgress returns the current progress of an index
// rebuild job.
func (s *Server) handleVectorGetRebuildProgress(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorGetRebuildProgressRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	progress, err := vectorSvc.GetRebuildProgress(ctx, &service.GetRebuildProgressRequest{
		Namespace: req.GetNamespace(),
		JobID:     req.GetJobId(),
	})
	if err != nil {
		return nil, err
	}

	out, err := proto.Marshal(&pb.VectorRebuildProgress{
		JobId:      progress.JobID,
		Status:     progress.Status,
		Progress:   progress.PercentComplete,
		StartedAt:  progress.StartedAt,
		FinishedAt: progress.CompletedAt,
		Error:      progress.Error,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVectorGetIndexStats returns statistics about a namespace's vector
// index including vector count, memory usage, and disk usage.
func (s *Server) handleVectorGetIndexStats(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorGetIndexStatsRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	stats, err := vectorSvc.GetIndexStats(ctx, &service.GetIndexStatsRequest{
		Namespace: req.GetNamespace(),
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}
	metric, err := protoDistanceMetric(req.GetNamespace(), stats.DistanceMetric)
	if err != nil {
		return nil, err
	}

	out, err := proto.Marshal(&pb.VectorIndexStats{
		VectorCount:    int64(stats.VectorCount),
		Dimension:      int32(stats.Dimension),
		DistanceMetric: metric,
		IndexType:      stringToProtoIndexType[stats.IndexType],
		MemoryUsage:    int64(stats.MemoryUsageBytes),
		DiskUsage:      int64(stats.IndexSizeBytes),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Compaction handlers
// ---------------------------------------------------------------------------

// handleVectorCompactNamespace triggers an asynchronous compaction of a
// namespace's storage to reclaim space from deleted vectors.
func (s *Server) handleVectorCompactNamespace(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorCompactNamespaceRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	resp, err := vectorSvc.CompactNamespace(ctx, &service.CompactNamespaceRequest{
		Namespace: req.GetNamespace(),
		ShardID:   req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	out, err := proto.Marshal(&pb.VectorCompactNamespaceResponse{
		JobId:   resp.OperationID,
		Started: resp.Success,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVectorGetCompactionProgress returns the current progress of a
// compaction job.
func (s *Server) handleVectorGetCompactionProgress(ctx context.Context, payload []byte) ([]byte, error) {
	vectorSvc, svcErr := s.vectorSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.VectorGetCompactionProgressRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	progress, err := vectorSvc.GetCompactionProgress(ctx, &service.GetCompactionProgressRequest{
		Namespace:   req.GetNamespace(),
		OperationID: req.GetJobId(),
		ShardID:     req.GetShardId(),
	})
	if err != nil {
		return nil, err
	}

	var finishedAt int64
	if progress.CompletedAt != nil {
		finishedAt = *progress.CompletedAt
	}

	out, err := proto.Marshal(&pb.VectorCompactionProgress{
		JobId:      progress.OperationID,
		Status:     progressStatusString(progress.InProgress),
		Progress:   progress.PercentComplete,
		StartedAt:  progress.StartedAt,
		FinishedAt: finishedAt,
		Error:      progress.Error,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}
