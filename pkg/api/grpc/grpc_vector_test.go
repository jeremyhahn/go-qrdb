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

//go:build !integration

package grpc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// Mock vector service
// ---------------------------------------------------------------------------

type mockVectorService struct {
	createNSResult         *service.CreateNamespaceResponse
	createNSErr            error
	deleteNSResult         *service.DeleteNamespaceResponse
	deleteNSErr            error
	listNSResult           *service.ListNamespacesResponse
	listNSErr              error
	getNSInfoResult        *service.GetNamespaceInfoResponse
	getNSInfoErr           error
	upsertResult           *service.UpsertVectorResponse
	upsertErr              error
	getVectorResult        *service.GetVectorResponse
	getVectorErr           error
	deleteVectorResult     *service.DeleteVectorResponse
	deleteVectorErr        error
	batchUpsertResult      *service.BatchUpsertResponse
	batchUpsertErr         error
	batchDeleteResult      *service.BatchDeleteVectorsResponse
	batchDeleteErr         error
	listVectorsResult      *service.ListVectorsResponse
	listVectorsErr         error
	updateMetadataResult   *service.UpdateVectorMetadataResponse
	updateMetadataErr      error
	searchSimilarResult    *service.SearchSimilarResponse
	searchSimilarErr       error
	searchByMetadataResult *service.SearchByMetadataResponse
	searchByMetadataErr    error
	hybridSearchResult     *service.HybridSearchResponse
	hybridSearchErr        error
	searchByIDResult       *service.SearchByIDResponse
	searchByIDErr          error
	rebuildIndexResult     *service.RebuildIndexResponse
	rebuildIndexErr        error
	getRebuildResult       *service.GetRebuildProgressResponse
	getRebuildErr          error
	getIndexStatsResult    *service.GetIndexStatsResponse
	getIndexStatsErr       error
	compactResult          *service.CompactNamespaceResponse
	compactErr             error
	getCompactionResult    *service.GetCompactionProgressResponse
	getCompactionErr       error
}

func (m *mockVectorService) UpdateNamespace(_ context.Context, req *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
	return &service.UpdateNamespaceResponse{Namespace: req.Namespace, RequiresRebuild: len(req.IndexConfig) > 0, Timestamp: 1000, Success: true}, nil
}

func (m *mockVectorService) CreateNamespace(_ context.Context, _ *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	if m.createNSErr != nil {
		return nil, m.createNSErr
	}
	return m.createNSResult, nil
}

func (m *mockVectorService) DeleteNamespace(_ context.Context, _ *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
	if m.deleteNSErr != nil {
		return nil, m.deleteNSErr
	}
	return m.deleteNSResult, nil
}

func (m *mockVectorService) ListNamespaces(_ context.Context, _ *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	if m.listNSErr != nil {
		return nil, m.listNSErr
	}
	return m.listNSResult, nil
}

func (m *mockVectorService) GetNamespaceInfo(_ context.Context, _ *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	if m.getNSInfoErr != nil {
		return nil, m.getNSInfoErr
	}
	return m.getNSInfoResult, nil
}

func (m *mockVectorService) UpsertVector(_ context.Context, _ *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	if m.upsertErr != nil {
		return nil, m.upsertErr
	}
	return m.upsertResult, nil
}

func (m *mockVectorService) GetVector(_ context.Context, _ *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	if m.getVectorErr != nil {
		return nil, m.getVectorErr
	}
	return m.getVectorResult, nil
}

func (m *mockVectorService) DeleteVector(_ context.Context, _ *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
	if m.deleteVectorErr != nil {
		return nil, m.deleteVectorErr
	}
	return m.deleteVectorResult, nil
}

func (m *mockVectorService) BatchUpsert(_ context.Context, _ *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	if m.batchUpsertErr != nil {
		return nil, m.batchUpsertErr
	}
	return m.batchUpsertResult, nil
}

func (m *mockVectorService) BatchDeleteVectors(_ context.Context, _ *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
	if m.batchDeleteErr != nil {
		return nil, m.batchDeleteErr
	}
	return m.batchDeleteResult, nil
}

func (m *mockVectorService) ListVectors(_ context.Context, _ *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
	if m.listVectorsErr != nil {
		return nil, m.listVectorsErr
	}
	return m.listVectorsResult, nil
}

func (m *mockVectorService) UpdateMetadata(_ context.Context, _ *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	if m.updateMetadataErr != nil {
		return nil, m.updateMetadataErr
	}
	return m.updateMetadataResult, nil
}

func (m *mockVectorService) SearchSimilar(_ context.Context, _ *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	if m.searchSimilarErr != nil {
		return nil, m.searchSimilarErr
	}
	return m.searchSimilarResult, nil
}

func (m *mockVectorService) SearchByMetadata(_ context.Context, _ *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	if m.searchByMetadataErr != nil {
		return nil, m.searchByMetadataErr
	}
	return m.searchByMetadataResult, nil
}

func (m *mockVectorService) HybridSearch(_ context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	if m.hybridSearchErr != nil {
		return nil, m.hybridSearchErr
	}
	return m.hybridSearchResult, nil
}

func (m *mockVectorService) SearchByID(_ context.Context, _ *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	if m.searchByIDErr != nil {
		return nil, m.searchByIDErr
	}
	return m.searchByIDResult, nil
}

func (m *mockVectorService) RebuildIndex(_ context.Context, _ *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	if m.rebuildIndexErr != nil {
		return nil, m.rebuildIndexErr
	}
	return m.rebuildIndexResult, nil
}

func (m *mockVectorService) GetRebuildProgress(_ context.Context, _ *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
	if m.getRebuildErr != nil {
		return nil, m.getRebuildErr
	}
	return m.getRebuildResult, nil
}

func (m *mockVectorService) GetIndexStats(_ context.Context, _ *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
	if m.getIndexStatsErr != nil {
		return nil, m.getIndexStatsErr
	}
	return m.getIndexStatsResult, nil
}

func (m *mockVectorService) CompactNamespace(_ context.Context, _ *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	if m.compactErr != nil {
		return nil, m.compactErr
	}
	return m.compactResult, nil
}

func (m *mockVectorService) GetCompactionProgress(_ context.Context, _ *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
	if m.getCompactionErr != nil {
		return nil, m.getCompactionErr
	}
	return m.getCompactionResult, nil
}

// ---------------------------------------------------------------------------
// ID conversion helper tests
// ---------------------------------------------------------------------------

func TestParseVectorID_ValidID(t *testing.T) {
	id, err := parseVectorID("12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 12345 {
		t.Fatalf("expected 12345, got %d", id)
	}
}

func TestParseVectorID_EmptyString(t *testing.T) {
	id, err := parseVectorID("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 0 {
		t.Fatalf("expected 0 for empty string, got %d", id)
	}
}

func TestParseVectorID_InvalidString(t *testing.T) {
	_, err := parseVectorID("abc")
	if err == nil {
		t.Fatal("expected error for invalid ID, got nil")
	}
	var vpe *VectorIDParseError
	if !errors.As(err, &vpe) {
		t.Fatalf("expected *VectorIDParseError, got %T", err)
	}
	if vpe.ID != "abc" {
		t.Fatalf("expected ID 'abc', got %q", vpe.ID)
	}
}

func TestParseVectorID_NegativeNumber(t *testing.T) {
	_, err := parseVectorID("-1")
	if err == nil {
		t.Fatal("expected error for negative number, got nil")
	}
	var vpe *VectorIDParseError
	if !errors.As(err, &vpe) {
		t.Fatalf("expected *VectorIDParseError, got %T", err)
	}
}

func TestFormatVectorID_Zero(t *testing.T) {
	result := formatVectorID(0)
	if result != "0" {
		t.Fatalf("expected '0', got %q", result)
	}
}

func TestFormatVectorID_NonZero(t *testing.T) {
	result := formatVectorID(42)
	if result != "42" {
		t.Fatalf("expected '42', got %q", result)
	}
}

func TestFormatVectorID_Large(t *testing.T) {
	result := formatVectorID(18446744073709551615) // max uint64
	if result != "18446744073709551615" {
		t.Fatalf("expected max uint64 string, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// Metadata helper tests
// ---------------------------------------------------------------------------

func TestMarshalMetadata_Nil(t *testing.T) {
	result, err := marshalMetadata(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Fatalf("expected empty string, got %q", result)
	}
}

func TestMarshalMetadata_Empty(t *testing.T) {
	result, err := marshalMetadata(map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Fatalf("expected empty string, got %q", result)
	}
}

func TestMarshalMetadata_Valid(t *testing.T) {
	m := map[string]interface{}{"key": "value", "num": float64(42)}
	result, err := marshalMetadata(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty JSON string")
	}
	if !strings.Contains(result, "key") || !strings.Contains(result, "value") {
		t.Fatalf("expected JSON to contain key/value, got %q", result)
	}
}

func TestMarshalMetadata_Unmarshallable(t *testing.T) {
	m := map[string]interface{}{"fn": func() {}}
	_, err := marshalMetadata(m)
	if err == nil {
		t.Fatal("expected error for unmarshallable metadata, got nil")
	}
	var mme *MetadataMarshalError
	if !errors.As(err, &mme) {
		t.Fatalf("expected *MetadataMarshalError, got %T", err)
	}
}

func TestUnmarshalMetadata_Empty(t *testing.T) {
	result, err := unmarshalMetadata("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestUnmarshalMetadata_Valid(t *testing.T) {
	result, err := unmarshalMetadata(`{"key":"value"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["key"] != "value" {
		t.Fatalf("expected key=value, got %v", result["key"])
	}
}

func TestUnmarshalMetadata_InvalidJSON(t *testing.T) {
	_, err := unmarshalMetadata("{invalid")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	var mue *MetadataUnmarshalError
	if !errors.As(err, &mue) {
		t.Fatalf("expected *MetadataUnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// indexParamsToStringMap tests
// ---------------------------------------------------------------------------

func TestIndexParamsToStringMap_Nil(t *testing.T) {
	result := indexParamsToStringMap(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestIndexParamsToStringMap_Empty(t *testing.T) {
	result := indexParamsToStringMap(map[string]interface{}{})
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestIndexParamsToStringMap_Valid(t *testing.T) {
	params := map[string]interface{}{
		"M":              float64(16),
		"efConstruction": float64(200),
	}
	result := indexParamsToStringMap(params)
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if result["M"] != "16" {
		t.Fatalf("expected M=16, got %q", result["M"])
	}
}

// ---------------------------------------------------------------------------
// parseStringIDs tests
// ---------------------------------------------------------------------------

func TestParseStringIDs_Valid(t *testing.T) {
	result, err := parseStringIDs([]string{"1", "2", "3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 IDs, got %d", len(result))
	}
	if result[0] != 1 || result[1] != 2 || result[2] != 3 {
		t.Fatalf("expected [1,2,3], got %v", result)
	}
}

func TestParseStringIDs_Empty(t *testing.T) {
	result, err := parseStringIDs([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 IDs, got %d", len(result))
	}
}

func TestParseStringIDs_Invalid(t *testing.T) {
	_, err := parseStringIDs([]string{"1", "bad", "3"})
	if err == nil {
		t.Fatal("expected error for invalid ID, got nil")
	}
	var vpe *VectorIDParseError
	if !errors.As(err, &vpe) {
		t.Fatalf("expected *VectorIDParseError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// vectorDataToProto tests
// ---------------------------------------------------------------------------

func TestVectorDataToProto_Nil(t *testing.T) {
	result, err := vectorDataToProto(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestVectorDataToProto_Valid(t *testing.T) {
	vd := &service.VectorData{
		ID:        42,
		Vector:    []float32{1.0, 2.0, 3.0},
		Metadata:  map[string]interface{}{"color": "red"},
		Version:   5,
		CreatedAt: 1000,
		UpdatedAt: 2000,
	}
	result, err := vectorDataToProto(vd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.GetId() != "42" {
		t.Fatalf("expected ID '42', got %q", result.GetId())
	}
	if len(result.GetVector()) != 3 {
		t.Fatalf("expected 3-dim vector, got %d", len(result.GetVector()))
	}
	if result.GetVersion() != 5 {
		t.Fatalf("expected version 5, got %d", result.GetVersion())
	}
	if result.GetCreatedAt() != 1000 {
		t.Fatalf("expected created_at 1000, got %d", result.GetCreatedAt())
	}
	if result.GetUpdatedAt() != 2000 {
		t.Fatalf("expected updated_at 2000, got %d", result.GetUpdatedAt())
	}
}

func TestVectorDataToProto_NoMetadata(t *testing.T) {
	vd := &service.VectorData{ID: 1, Vector: []float32{0.5}}
	result, err := vectorDataToProto(vd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.GetMetadataJson() != "" {
		t.Fatalf("expected empty metadata_json, got %q", result.GetMetadataJson())
	}
}

// ---------------------------------------------------------------------------
// getVectorResponseToProto tests
// ---------------------------------------------------------------------------

func TestGetVectorResponseToProto_Nil(t *testing.T) {
	result, err := getVectorResponseToProto(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestGetVectorResponseToProto_Valid(t *testing.T) {
	resp := &service.GetVectorResponse{
		ID:        10,
		Vector:    []float32{0.1, 0.2},
		Metadata:  map[string]interface{}{"type": "test"},
		Version:   3,
		CreatedAt: 500,
		UpdatedAt: 600,
	}
	result, err := getVectorResponseToProto(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.GetId() != "10" {
		t.Fatalf("expected ID '10', got %q", result.GetId())
	}
	if result.GetVersion() != 3 {
		t.Fatalf("expected version 3, got %d", result.GetVersion())
	}
}

// ---------------------------------------------------------------------------
// protoToVectorData tests
// ---------------------------------------------------------------------------

func TestProtoToVectorData_Valid(t *testing.T) {
	pbVD := &pb.VectorDataProto{
		Id:           "100",
		Vector:       []float32{1.0, 2.0},
		MetadataJson: `{"key":"val"}`,
		Version:      7,
		CreatedAt:    3000,
		UpdatedAt:    4000,
	}
	result, err := protoToVectorData(pbVD)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != 100 {
		t.Fatalf("expected ID 100, got %d", result.ID)
	}
	if len(result.Vector) != 2 {
		t.Fatalf("expected 2-dim vector, got %d", len(result.Vector))
	}
	if result.Metadata["key"] != "val" {
		t.Fatalf("expected metadata key=val, got %v", result.Metadata["key"])
	}
	if result.Version != 7 {
		t.Fatalf("expected version 7, got %d", result.Version)
	}
}

func TestProtoToVectorData_InvalidMetadata(t *testing.T) {
	pbVD := &pb.VectorDataProto{
		Id:           "1",
		MetadataJson: "{invalid",
	}
	_, err := protoToVectorData(pbVD)
	if err == nil {
		t.Fatal("expected error for invalid metadata JSON, got nil")
	}
}

func TestProtoToVectorData_InvalidID(t *testing.T) {
	pbVD := &pb.VectorDataProto{
		Id: "not-a-number",
	}
	_, err := protoToVectorData(pbVD)
	if err == nil {
		t.Fatal("expected error for invalid vector ID, got nil")
	}
}

func TestProtoToVectorData_EmptyMetadata(t *testing.T) {
	pbVD := &pb.VectorDataProto{
		Id:     "5",
		Vector: []float32{0.5},
	}
	result, err := protoToVectorData(pbVD)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Metadata != nil {
		t.Fatalf("expected nil metadata, got %v", result.Metadata)
	}
}

// ---------------------------------------------------------------------------
// searchResultToProto tests
// ---------------------------------------------------------------------------

func TestSearchResultToProto_Nil(t *testing.T) {
	result, err := searchResultToProto(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestSearchResultToProto_Valid(t *testing.T) {
	sr := &service.VectorSearchResult{
		ID:       42,
		Score:    0.95,
		Vector:   []float32{1.0, 2.0},
		Metadata: map[string]interface{}{"rank": "first"},
	}
	result, err := searchResultToProto(sr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.GetId() != "42" {
		t.Fatalf("expected ID '42', got %q", result.GetId())
	}
	if result.GetScore() != float64(float32(0.95)) {
		t.Fatalf("expected score %f, got %f", float64(float32(0.95)), result.GetScore())
	}
}

// ---------------------------------------------------------------------------
// searchResultsToProto tests
// ---------------------------------------------------------------------------

func TestSearchResultsToProto_Empty(t *testing.T) {
	result, err := searchResultsToProto(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 results, got %d", len(result))
	}
}

func TestSearchResultsToProto_Multiple(t *testing.T) {
	results := []*service.VectorSearchResult{
		{ID: 1, Score: 0.9},
		{ID: 2, Score: 0.8},
	}
	pbResults, err := searchResultsToProto(results)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pbResults) != 2 {
		t.Fatalf("expected 2 results, got %d", len(pbResults))
	}
}

// ---------------------------------------------------------------------------
// progressStatusString tests
// ---------------------------------------------------------------------------

func TestProgressStatusString_InProgress(t *testing.T) {
	if progressStatusString(true) != "running" {
		t.Fatalf("expected 'running', got %q", progressStatusString(true))
	}
}

func TestProgressStatusString_Completed(t *testing.T) {
	if progressStatusString(false) != "completed" {
		t.Fatalf("expected 'completed', got %q", progressStatusString(false))
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestVectorIDParseError_Error(t *testing.T) {
	e := &VectorIDParseError{ID: "abc", Err: errors.New("parse error")}
	msg := e.Error()
	if !strings.Contains(msg, "abc") {
		t.Fatalf("expected ID in error message, got %q", msg)
	}
	if !strings.Contains(msg, "parse error") {
		t.Fatalf("expected underlying error in message, got %q", msg)
	}
}

func TestVectorIDParseError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	e := &VectorIDParseError{ID: "x", Err: inner}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return the underlying error")
	}
}

func TestMetadataMarshalError_Error(t *testing.T) {
	e := &MetadataMarshalError{Err: errors.New("marshal failed")}
	msg := e.Error()
	if !strings.Contains(msg, "marshal") {
		t.Fatalf("expected 'marshal' in error message, got %q", msg)
	}
}

func TestMetadataMarshalError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	e := &MetadataMarshalError{Err: inner}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return the underlying error")
	}
}

func TestMetadataUnmarshalError_Error(t *testing.T) {
	e := &MetadataUnmarshalError{Err: errors.New("unmarshal failed")}
	msg := e.Error()
	if !strings.Contains(msg, "unmarshal") {
		t.Fatalf("expected 'unmarshal' in error message, got %q", msg)
	}
}

func TestMetadataUnmarshalError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	e := &MetadataUnmarshalError{Err: inner}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return the underlying error")
	}
}

func TestTTLParseError_Error(t *testing.T) {
	e := &TTLParseError{Value: "bad-ttl", Err: errors.New("parse failed")}
	msg := e.Error()
	if !strings.Contains(msg, "bad-ttl") {
		t.Fatalf("expected value in error message, got %q", msg)
	}
	if !strings.Contains(msg, "parse failed") {
		t.Fatalf("expected underlying error in message, got %q", msg)
	}
}

func TestTTLParseError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	e := &TTLParseError{Value: "x", Err: inner}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return the underlying error")
	}
}

// ---------------------------------------------------------------------------
// mapServiceError - vector error mapping tests
// ---------------------------------------------------------------------------

func TestMapServiceError_VectorNotFoundError(t *testing.T) {
	err := mapServiceError(&service.VectorNotFoundError{ID: "42", Namespace: "ns"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestMapServiceError_NamespaceNotFoundError(t *testing.T) {
	err := mapServiceError(&service.NamespaceNotFoundError{Namespace: "missing"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestMapServiceError_NamespaceExistsError(t *testing.T) {
	err := mapServiceError(&service.NamespaceExistsError{Namespace: "dup"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

func TestMapServiceError_DimensionMismatchError(t *testing.T) {
	err := mapServiceError(&service.DimensionMismatchError{Expected: 128, Actual: 256})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestMapServiceError_InvalidVectorError(t *testing.T) {
	err := mapServiceError(&service.InvalidVectorError{Reason: "empty vector"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Namespace handler tests (via gRPC round-trip)
// ---------------------------------------------------------------------------

func TestGRPC_VectorCreateNamespace_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		createNSResult: &service.CreateNamespaceResponse{
			Namespace: "test-ns",
			Dimension: 128,
			Timestamp: 12345,
			Success:   true,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorCreateNamespace(context.Background(), &pb.VectorCreateNamespaceRequest{
		Namespace:      "test-ns",
		Dimension:      128,
		DistanceMetric: pb.DistanceMetric_DISTANCE_COSINE,
		IndexType:      pb.VectorIndexType_INDEX_HNSW,
	})
	if err != nil {
		t.Fatalf("VectorCreateNamespace failed: %v", err)
	}
	if !resp.GetCreated() {
		t.Fatal("expected Created=true")
	}
	if resp.GetTimestamp() != 12345 {
		t.Fatalf("expected timestamp 12345, got %d", resp.GetTimestamp())
	}
}

func TestGRPC_VectorCreateNamespace_WithIndexParams(t *testing.T) {
	vecSvc := &mockVectorService{
		createNSResult: &service.CreateNamespaceResponse{Success: true},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorCreateNamespace(context.Background(), &pb.VectorCreateNamespaceRequest{
		Namespace:       "ns",
		Dimension:       128,
		IndexParamsJson: `{"M":"16","efConstruction":"200"}`,
	})
	if err != nil {
		t.Fatalf("VectorCreateNamespace failed: %v", err)
	}
	if !resp.GetCreated() {
		t.Fatal("expected Created=true")
	}
}

func TestGRPC_VectorCreateNamespace_InvalidParams(t *testing.T) {
	vecSvc := &mockVectorService{}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorCreateNamespace(context.Background(), &pb.VectorCreateNamespaceRequest{
		Namespace:       "ns",
		Dimension:       128,
		IndexParamsJson: "{invalid",
	})
	if err == nil {
		t.Fatal("expected error for invalid index params JSON, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_VectorCreateNamespace_Error(t *testing.T) {
	vecSvc := &mockVectorService{
		createNSErr: &service.NamespaceExistsError{Namespace: "dup"},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorCreateNamespace(context.Background(), &pb.VectorCreateNamespaceRequest{
		Namespace: "dup",
		Dimension: 128,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

func TestGRPC_VectorCreateNamespace_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorCreateNamespace(context.Background(), &pb.VectorCreateNamespaceRequest{
		Namespace: "ns",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorDeleteNamespace_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		deleteNSResult: &service.DeleteNamespaceResponse{Success: true},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorDeleteNamespace(context.Background(), &pb.VectorDeleteNamespaceRequest{
		Namespace: "ns",
	})
	if err != nil {
		t.Fatalf("VectorDeleteNamespace failed: %v", err)
	}
}

func TestGRPC_VectorDeleteNamespace_Error(t *testing.T) {
	vecSvc := &mockVectorService{
		deleteNSErr: &service.NamespaceNotFoundError{Namespace: "missing"},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorDeleteNamespace(context.Background(), &pb.VectorDeleteNamespaceRequest{
		Namespace: "missing",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_VectorDeleteNamespace_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorDeleteNamespace(context.Background(), &pb.VectorDeleteNamespaceRequest{
		Namespace: "ns",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorListNamespaces_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		listNSResult: &service.ListNamespacesResponse{
			Namespaces: []*service.NamespaceInfo{
				{Namespace: "ns1"},
				{Namespace: "ns2"},
			},
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorListNamespaces(context.Background(), &pb.VectorListNamespacesRequest{})
	if err != nil {
		t.Fatalf("VectorListNamespaces failed: %v", err)
	}
	if len(resp.GetNamespaces()) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(resp.GetNamespaces()))
	}
}

func TestGRPC_VectorListNamespaces_Error(t *testing.T) {
	vecSvc := &mockVectorService{
		listNSErr: &service.ReadError{Err: errors.New("list failed")},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorListNamespaces(context.Background(), &pb.VectorListNamespacesRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_VectorListNamespaces_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorListNamespaces(context.Background(), &pb.VectorListNamespacesRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorGetNamespaceInfo_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		getNSInfoResult: &service.GetNamespaceInfoResponse{
			Namespace:      "ns1",
			Dimension:      128,
			DistanceMetric: "COSINE",
			IndexType:      "HNSW",
			VectorCount:    500,
			CreatedAt:      1000,
			UpdatedAt:      2000,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorGetNamespaceInfo(context.Background(), &pb.VectorGetNamespaceInfoRequest{
		Namespace: "ns1",
	})
	if err != nil {
		t.Fatalf("VectorGetNamespaceInfo failed: %v", err)
	}
	if resp.GetNamespace() != "ns1" {
		t.Fatalf("expected namespace 'ns1', got %q", resp.GetNamespace())
	}
	if resp.GetDimension() != 128 {
		t.Fatalf("expected dimension 128, got %d", resp.GetDimension())
	}
	if resp.GetVectorCount() != 500 {
		t.Fatalf("expected vector_count 500, got %d", resp.GetVectorCount())
	}
}

func TestGRPC_VectorGetNamespaceInfo_IndexConfigAndMetadata(t *testing.T) {
	vecSvc := &mockVectorService{
		getNSInfoResult: &service.GetNamespaceInfoResponse{
			Namespace:   "ns1",
			Dimension:   4,
			IndexConfig: map[string]string{"ef_construction": "321"},
			Metadata:    map[string]interface{}{"owner": "team-a"},
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorGetNamespaceInfo(context.Background(), &pb.VectorGetNamespaceInfoRequest{
		Namespace: "ns1",
	})
	if err != nil {
		t.Fatalf("VectorGetNamespaceInfo failed: %v", err)
	}
	if resp.GetIndexConfig()["ef_construction"] != "321" {
		t.Fatalf("index_config = %v, want ef_construction=321", resp.GetIndexConfig())
	}
	if resp.GetMetadataJson() != `{"owner":"team-a"}` {
		t.Fatalf("metadata_json = %q, want {\"owner\":\"team-a\"}", resp.GetMetadataJson())
	}
}

func TestGRPC_VectorGetNamespaceInfo_MetadataMarshalError(t *testing.T) {
	vecSvc := &mockVectorService{
		getNSInfoResult: &service.GetNamespaceInfoResponse{
			Namespace: "ns1",
			Metadata:  map[string]interface{}{"bad": make(chan int)},
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	if _, err := client.VectorGetNamespaceInfo(context.Background(), &pb.VectorGetNamespaceInfoRequest{
		Namespace: "ns1",
	}); err == nil {
		t.Fatal("expected an error for metadata that cannot be marshalled")
	}
}

func TestGRPC_VectorGetNamespaceInfo_Error(t *testing.T) {
	vecSvc := &mockVectorService{
		getNSInfoErr: &service.NamespaceNotFoundError{Namespace: "missing"},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorGetNamespaceInfo(context.Background(), &pb.VectorGetNamespaceInfoRequest{
		Namespace: "missing",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_VectorGetNamespaceInfo_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorGetNamespaceInfo(context.Background(), &pb.VectorGetNamespaceInfoRequest{
		Namespace: "ns",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Vector CRUD handler tests
// ---------------------------------------------------------------------------

func TestGRPC_VectorUpsert_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		upsertResult: &service.UpsertVectorResponse{
			ID: 42, Version: 1, Timestamp: 1000, Success: true,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorUpsert(context.Background(), &pb.VectorUpsertRequest{
		Namespace:    "ns",
		Id:           "42",
		Vector:       []float32{1.0, 2.0},
		MetadataJson: `{"key":"val"}`,
	})
	if err != nil {
		t.Fatalf("VectorUpsert failed: %v", err)
	}
	if !resp.GetCreated() {
		t.Fatal("expected Created=true")
	}
	if resp.GetVersion() != 1 {
		t.Fatalf("expected version 1, got %d", resp.GetVersion())
	}
}

func TestGRPC_VectorUpsert_InvalidMetadata(t *testing.T) {
	vecSvc := &mockVectorService{}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorUpsert(context.Background(), &pb.VectorUpsertRequest{
		Namespace:    "ns",
		Id:           "1",
		Vector:       []float32{1.0},
		MetadataJson: "{invalid",
	})
	if err == nil {
		t.Fatal("expected error for invalid metadata, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_VectorUpsert_InvalidID(t *testing.T) {
	vecSvc := &mockVectorService{}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorUpsert(context.Background(), &pb.VectorUpsertRequest{
		Namespace: "ns",
		Id:        "not-a-number",
		Vector:    []float32{1.0},
	})
	if err == nil {
		t.Fatal("expected error for invalid ID, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_VectorUpsert_Error(t *testing.T) {
	vecSvc := &mockVectorService{
		upsertErr: &service.DimensionMismatchError{Expected: 128, Actual: 64},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorUpsert(context.Background(), &pb.VectorUpsertRequest{
		Namespace: "ns",
		Id:        "1",
		Vector:    []float32{1.0},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_VectorUpsert_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorUpsert(context.Background(), &pb.VectorUpsertRequest{
		Namespace: "ns", Id: "1", Vector: []float32{1.0},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorGet_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		getVectorResult: &service.GetVectorResponse{
			ID: 42, Vector: []float32{1.0, 2.0}, Version: 3,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorGet(context.Background(), &pb.VectorGetRequest{
		Namespace: "ns", Id: "42",
	})
	if err != nil {
		t.Fatalf("VectorGet failed: %v", err)
	}
	if resp.GetId() != "42" {
		t.Fatalf("expected ID '42', got %q", resp.GetId())
	}
}

func TestGRPC_VectorGet_InvalidID(t *testing.T) {
	vecSvc := &mockVectorService{}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorGet(context.Background(), &pb.VectorGetRequest{
		Namespace: "ns", Id: "bad",
	})
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_VectorGet_Error(t *testing.T) {
	vecSvc := &mockVectorService{
		getVectorErr: &service.VectorNotFoundError{ID: "99", Namespace: "ns"},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorGet(context.Background(), &pb.VectorGetRequest{
		Namespace: "ns", Id: "99",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_VectorGet_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorGet(context.Background(), &pb.VectorGetRequest{
		Namespace: "ns", Id: "1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorDelete_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		deleteVectorResult: &service.DeleteVectorResponse{Success: true},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorDelete(context.Background(), &pb.VectorDeleteRequest{
		Namespace: "ns", Id: "42",
	})
	if err != nil {
		t.Fatalf("VectorDelete failed: %v", err)
	}
}

func TestGRPC_VectorDelete_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorDelete(context.Background(), &pb.VectorDeleteRequest{
		Namespace: "ns", Id: "1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorDelete_InvalidID(t *testing.T) {
	vecSvc := &mockVectorService{}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorDelete(context.Background(), &pb.VectorDeleteRequest{
		Namespace: "ns", Id: "abc",
	})
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
}

func TestGRPC_VectorBatchUpsert_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		batchUpsertResult: &service.BatchUpsertResponse{
			InsertedCount: 2, Success: true,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorBatchUpsert(context.Background(), &pb.VectorBatchUpsertRequest{
		Namespace: "ns",
		Vectors: []*pb.VectorDataProto{
			{Id: "1", Vector: []float32{0.1}},
			{Id: "2", Vector: []float32{0.2}},
		},
	})
	if err != nil {
		t.Fatalf("VectorBatchUpsert failed: %v", err)
	}
	if resp.GetSucceeded() != 2 {
		t.Fatalf("expected 2 succeeded, got %d", resp.GetSucceeded())
	}
	if resp.GetFailed() != 0 {
		t.Fatalf("expected 0 failed, got %d", resp.GetFailed())
	}
}

func TestGRPC_VectorBatchUpsert_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorBatchUpsert(context.Background(), &pb.VectorBatchUpsertRequest{
		Namespace: "ns",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorBatchUpsert_InvalidVector(t *testing.T) {
	vecSvc := &mockVectorService{}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorBatchUpsert(context.Background(), &pb.VectorBatchUpsertRequest{
		Namespace: "ns",
		Vectors: []*pb.VectorDataProto{
			{Id: "not-a-number", Vector: []float32{0.1}},
		},
	})
	if err == nil {
		t.Fatal("expected error for invalid vector ID")
	}
}

func TestGRPC_VectorBatchDelete_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		batchDeleteResult: &service.BatchDeleteVectorsResponse{
			DeletedCount: 2, Success: true,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorBatchDelete(context.Background(), &pb.VectorBatchDeleteRequest{
		Namespace: "ns", Ids: []string{"1", "2"},
	})
	if err != nil {
		t.Fatalf("VectorBatchDelete failed: %v", err)
	}
	if resp.GetDeleted() != 2 {
		t.Fatalf("expected 2 deleted, got %d", resp.GetDeleted())
	}
}

func TestGRPC_VectorBatchDelete_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorBatchDelete(context.Background(), &pb.VectorBatchDeleteRequest{
		Namespace: "ns", Ids: []string{"1"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorBatchDelete_InvalidIDs(t *testing.T) {
	vecSvc := &mockVectorService{}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorBatchDelete(context.Background(), &pb.VectorBatchDeleteRequest{
		Namespace: "ns", Ids: []string{"1", "bad"},
	})
	if err == nil {
		t.Fatal("expected error for invalid IDs")
	}
}

func TestGRPC_VectorBatchDelete_WithFailedIDs(t *testing.T) {
	vecSvc := &mockVectorService{
		batchDeleteResult: &service.BatchDeleteVectorsResponse{
			DeletedCount: 1,
			FailedIDs:    []uint64{2},
			Success:      true,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorBatchDelete(context.Background(), &pb.VectorBatchDeleteRequest{
		Namespace: "ns", Ids: []string{"1", "2"},
	})
	if err != nil {
		t.Fatalf("VectorBatchDelete failed: %v", err)
	}
	if resp.GetFailed() != 1 {
		t.Fatalf("expected 1 failed, got %d", resp.GetFailed())
	}
	if len(resp.GetErrors()) != 1 {
		t.Fatalf("expected 1 error entry, got %d", len(resp.GetErrors()))
	}
	if resp.GetErrors()[0].GetId() != "2" {
		t.Fatalf("expected failed ID '2', got %q", resp.GetErrors()[0].GetId())
	}
}

func TestGRPC_VectorList_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		listVectorsResult: &service.ListVectorsResponse{
			Vectors: []*service.VectorData{
				{ID: 1, Vector: []float32{0.1}},
				{ID: 2, Vector: []float32{0.2}},
			},
			NextCursor: "cursor-2",
			TotalCount: 100,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorList(context.Background(), &pb.VectorListRequest{
		Namespace: "ns", Limit: 2,
	})
	if err != nil {
		t.Fatalf("VectorList failed: %v", err)
	}
	if len(resp.GetVectors()) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(resp.GetVectors()))
	}
	if resp.GetNextCursor() != "cursor-2" {
		t.Fatalf("expected cursor 'cursor-2', got %q", resp.GetNextCursor())
	}
	if resp.GetTotal() != 100 {
		t.Fatalf("expected total 100, got %d", resp.GetTotal())
	}
}

func TestGRPC_VectorList_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorList(context.Background(), &pb.VectorListRequest{Namespace: "ns"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorUpdateMetadata_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		updateMetadataResult: &service.UpdateVectorMetadataResponse{
			ID: 42, Version: 2, Success: true,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorUpdateMetadata(context.Background(), &pb.VectorUpdateMetadataRequest{
		Namespace:    "ns",
		Id:           "42",
		MetadataJson: `{"updated":"true"}`,
	})
	if err != nil {
		t.Fatalf("VectorUpdateMetadata failed: %v", err)
	}
}

func TestGRPC_VectorUpdateMetadata_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorUpdateMetadata(context.Background(), &pb.VectorUpdateMetadataRequest{
		Namespace: "ns", Id: "1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorUpdateMetadata_InvalidMetadata(t *testing.T) {
	vecSvc := &mockVectorService{}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorUpdateMetadata(context.Background(), &pb.VectorUpdateMetadataRequest{
		Namespace: "ns", Id: "1", MetadataJson: "{invalid",
	})
	if err == nil {
		t.Fatal("expected error for invalid metadata")
	}
}

func TestGRPC_VectorUpdateMetadata_InvalidID(t *testing.T) {
	vecSvc := &mockVectorService{}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorUpdateMetadata(context.Background(), &pb.VectorUpdateMetadataRequest{
		Namespace: "ns", Id: "abc",
	})
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
}

// ---------------------------------------------------------------------------
// Search handler tests
// ---------------------------------------------------------------------------

func TestGRPC_VectorSearchSimilar_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		searchSimilarResult: &service.SearchSimilarResponse{
			Results: []*service.VectorSearchResult{
				{ID: 1, Score: 0.99},
				{ID: 2, Score: 0.85},
			},
			QueryDuration: 150,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorSearchSimilar(context.Background(), &pb.VectorSearchSimilarRequest{
		Namespace: "ns",
		Vector:    []float32{1.0, 2.0},
		K:         10,
	})
	if err != nil {
		t.Fatalf("VectorSearchSimilar failed: %v", err)
	}
	if len(resp.GetResults()) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.GetResults()))
	}
	if resp.GetTook() != 150 {
		t.Fatalf("expected took 150, got %d", resp.GetTook())
	}
}

func TestGRPC_VectorSearchSimilar_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorSearchSimilar(context.Background(), &pb.VectorSearchSimilarRequest{
		Namespace: "ns", Vector: []float32{1.0}, K: 5,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorSearchSimilar_Error(t *testing.T) {
	vecSvc := &mockVectorService{
		searchSimilarErr: &service.InvalidVectorError{Reason: "empty"},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorSearchSimilar(context.Background(), &pb.VectorSearchSimilarRequest{
		Namespace: "ns", Vector: []float32{}, K: 5,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_VectorSearchByMetadata_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		searchByMetadataResult: &service.SearchByMetadataResponse{
			Results: []*service.VectorData{
				{ID: 1, Vector: []float32{0.1}},
			},
			TotalCount: 1,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorSearchByMetadata(context.Background(), &pb.VectorSearchByMetadataRequest{
		Namespace:   "ns",
		FiltersJson: `{"color":"red"}`,
	})
	if err != nil {
		t.Fatalf("VectorSearchByMetadata failed: %v", err)
	}
	if len(resp.GetResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.GetResults()))
	}
}

func TestGRPC_VectorSearchByMetadata_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorSearchByMetadata(context.Background(), &pb.VectorSearchByMetadataRequest{
		Namespace: "ns",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorSearchByMetadata_InvalidFilters(t *testing.T) {
	vecSvc := &mockVectorService{}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorSearchByMetadata(context.Background(), &pb.VectorSearchByMetadataRequest{
		Namespace:   "ns",
		FiltersJson: "{invalid",
	})
	if err == nil {
		t.Fatal("expected error for invalid filters JSON")
	}
}

func TestGRPC_VectorHybridSearch_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		hybridSearchResult: &service.HybridSearchResponse{
			Results: []*service.VectorSearchResult{
				{ID: 1, Score: 0.95},
			},
			QueryDuration: 200,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorHybridSearch(context.Background(), &pb.VectorHybridSearchRequest{
		Namespace:           "ns",
		Vector:              []float32{1.0},
		K:                   5,
		MetadataFiltersJson: `{"type":"test"}`,
	})
	if err != nil {
		t.Fatalf("VectorHybridSearch failed: %v", err)
	}
	if len(resp.GetResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.GetResults()))
	}
}

func TestGRPC_VectorHybridSearch_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorHybridSearch(context.Background(), &pb.VectorHybridSearchRequest{
		Namespace: "ns",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorHybridSearch_InvalidFilters(t *testing.T) {
	vecSvc := &mockVectorService{}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorHybridSearch(context.Background(), &pb.VectorHybridSearchRequest{
		Namespace:           "ns",
		Vector:              []float32{1.0},
		K:                   5,
		MetadataFiltersJson: "{bad",
	})
	if err == nil {
		t.Fatal("expected error for invalid filters")
	}
}

func TestGRPC_VectorSearchByID_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		searchByIDResult: &service.SearchByIDResponse{
			SourceID: 42,
			Results: []*service.VectorSearchResult{
				{ID: 10, Score: 0.88},
			},
			QueryDuration: 75,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorSearchByID(context.Background(), &pb.VectorSearchByIDRequest{
		Namespace: "ns", Id: "42", K: 5,
	})
	if err != nil {
		t.Fatalf("VectorSearchByID failed: %v", err)
	}
	if len(resp.GetResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.GetResults()))
	}
}

func TestGRPC_VectorSearchByID_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorSearchByID(context.Background(), &pb.VectorSearchByIDRequest{
		Namespace: "ns", Id: "1", K: 5,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorSearchByID_InvalidID(t *testing.T) {
	vecSvc := &mockVectorService{}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorSearchByID(context.Background(), &pb.VectorSearchByIDRequest{
		Namespace: "ns", Id: "bad", K: 5,
	})
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
}

// ---------------------------------------------------------------------------
// Index management handler tests
// ---------------------------------------------------------------------------

func TestGRPC_VectorRebuildIndex_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		rebuildIndexResult: &service.RebuildIndexResponse{
			JobID: "job-1", Success: true, Timestamp: 5000,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorRebuildIndex(context.Background(), &pb.VectorRebuildIndexRequest{
		Namespace: "ns",
	})
	if err != nil {
		t.Fatalf("VectorRebuildIndex failed: %v", err)
	}
	if resp.GetJobId() != "job-1" {
		t.Fatalf("expected job ID 'job-1', got %q", resp.GetJobId())
	}
	if !resp.GetStarted() {
		t.Fatal("expected started=true")
	}
}

func TestGRPC_VectorRebuildIndex_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorRebuildIndex(context.Background(), &pb.VectorRebuildIndexRequest{
		Namespace: "ns",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorRebuildIndex_Error(t *testing.T) {
	vecSvc := &mockVectorService{
		rebuildIndexErr: &service.NamespaceNotFoundError{Namespace: "missing"},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorRebuildIndex(context.Background(), &pb.VectorRebuildIndexRequest{
		Namespace: "missing",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_VectorGetRebuildProgress_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		getRebuildResult: &service.GetRebuildProgressResponse{
			JobID:           "job-1",
			Status:          "running",
			PercentComplete: 50.5,
			StartedAt:       1000,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorGetRebuildProgress(context.Background(), &pb.VectorGetRebuildProgressRequest{
		Namespace: "ns", JobId: "job-1",
	})
	if err != nil {
		t.Fatalf("VectorGetRebuildProgress failed: %v", err)
	}
	if resp.GetJobId() != "job-1" {
		t.Fatalf("expected job ID 'job-1', got %q", resp.GetJobId())
	}
	if resp.GetProgress() != 50.5 {
		t.Fatalf("expected progress 50.5, got %f", resp.GetProgress())
	}
}

func TestGRPC_VectorGetRebuildProgress_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorGetRebuildProgress(context.Background(), &pb.VectorGetRebuildProgressRequest{
		Namespace: "ns",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorGetIndexStats_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		getIndexStatsResult: &service.GetIndexStatsResponse{
			VectorCount:      1000,
			Dimension:        128,
			IndexType:        "HNSW",
			IndexSizeBytes:   50000,
			MemoryUsageBytes: 100000,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorGetIndexStats(context.Background(), &pb.VectorGetIndexStatsRequest{
		Namespace: "ns",
	})
	if err != nil {
		t.Fatalf("VectorGetIndexStats failed: %v", err)
	}
	if resp.GetVectorCount() != 1000 {
		t.Fatalf("expected 1000 vectors, got %d", resp.GetVectorCount())
	}
	if resp.GetDimension() != 128 {
		t.Fatalf("expected dimension 128, got %d", resp.GetDimension())
	}
}

func TestGRPC_VectorGetIndexStats_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorGetIndexStats(context.Background(), &pb.VectorGetIndexStatsRequest{
		Namespace: "ns",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorGetIndexStats_Error(t *testing.T) {
	vecSvc := &mockVectorService{
		getIndexStatsErr: &service.NamespaceNotFoundError{Namespace: "missing"},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorGetIndexStats(context.Background(), &pb.VectorGetIndexStatsRequest{
		Namespace: "missing",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Compaction handler tests
// ---------------------------------------------------------------------------

func TestGRPC_VectorCompactNamespace_Success(t *testing.T) {
	vecSvc := &mockVectorService{
		compactResult: &service.CompactNamespaceResponse{
			OperationID: "compact-1", Success: true,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorCompactNamespace(context.Background(), &pb.VectorCompactNamespaceRequest{
		Namespace: "ns",
	})
	if err != nil {
		t.Fatalf("VectorCompactNamespace failed: %v", err)
	}
	if resp.GetJobId() != "compact-1" {
		t.Fatalf("expected job ID 'compact-1', got %q", resp.GetJobId())
	}
	if !resp.GetStarted() {
		t.Fatal("expected started=true")
	}
}

func TestGRPC_VectorCompactNamespace_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorCompactNamespace(context.Background(), &pb.VectorCompactNamespaceRequest{
		Namespace: "ns",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_VectorCompactNamespace_Error(t *testing.T) {
	vecSvc := &mockVectorService{
		compactErr: &service.NamespaceNotFoundError{Namespace: "missing"},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	_, err := client.VectorCompactNamespace(context.Background(), &pb.VectorCompactNamespaceRequest{
		Namespace: "missing",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_VectorGetCompactionProgress_Success(t *testing.T) {
	completedAt := int64(9999)
	vecSvc := &mockVectorService{
		getCompactionResult: &service.GetCompactionProgressResponse{
			OperationID:     "compact-1",
			InProgress:      false,
			PercentComplete: 100.0,
			StartedAt:       5000,
			CompletedAt:     &completedAt,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorGetCompactionProgress(context.Background(), &pb.VectorGetCompactionProgressRequest{
		Namespace: "ns", JobId: "compact-1",
	})
	if err != nil {
		t.Fatalf("VectorGetCompactionProgress failed: %v", err)
	}
	if resp.GetJobId() != "compact-1" {
		t.Fatalf("expected job ID 'compact-1', got %q", resp.GetJobId())
	}
	if resp.GetStatus() != "completed" {
		t.Fatalf("expected status 'completed', got %q", resp.GetStatus())
	}
	if resp.GetFinishedAt() != 9999 {
		t.Fatalf("expected finished_at 9999, got %d", resp.GetFinishedAt())
	}
}

func TestGRPC_VectorGetCompactionProgress_InProgress(t *testing.T) {
	vecSvc := &mockVectorService{
		getCompactionResult: &service.GetCompactionProgressResponse{
			OperationID:     "compact-2",
			InProgress:      true,
			PercentComplete: 45.0,
			StartedAt:       3000,
		},
	}
	client, cleanup := startTestServerFull(t, WithVector(vecSvc))
	defer cleanup()

	resp, err := client.VectorGetCompactionProgress(context.Background(), &pb.VectorGetCompactionProgressRequest{
		Namespace: "ns", JobId: "compact-2",
	})
	if err != nil {
		t.Fatalf("VectorGetCompactionProgress failed: %v", err)
	}
	if resp.GetStatus() != "running" {
		t.Fatalf("expected status 'running', got %q", resp.GetStatus())
	}
	if resp.GetFinishedAt() != 0 {
		t.Fatalf("expected finished_at 0 (in progress), got %d", resp.GetFinishedAt())
	}
}

func TestGRPC_VectorGetCompactionProgress_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.VectorGetCompactionProgress(context.Background(), &pb.VectorGetCompactionProgressRequest{
		Namespace: "ns",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// WithVector ServerOption test
// ---------------------------------------------------------------------------

func TestWithVector(t *testing.T) {
	svc := &mockVectorService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithVector(svc))
	if srv.vectorSvc == nil {
		t.Fatal("expected vectorSvc to be set")
	}
}

func TestWithVector_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithVector(nil))
	if srv.vectorSvc != nil {
		t.Fatal("expected vectorSvc to be nil")
	}
}

// ---------------------------------------------------------------------------
// Enum map coverage
// ---------------------------------------------------------------------------

func TestProtoDistanceMetricToString_AllValues(t *testing.T) {
	expected := map[pb.DistanceMetric]string{
		pb.DistanceMetric_DISTANCE_COSINE:      "COSINE",
		pb.DistanceMetric_DISTANCE_EUCLIDEAN:   "L2",
		pb.DistanceMetric_DISTANCE_DOT_PRODUCT: "DOT_PRODUCT",
	}
	for k, v := range expected {
		got := protoDistanceMetricToString[k]
		if got != v {
			t.Fatalf("expected %q for %v, got %q", v, k, got)
		}
	}
}

func TestStringToProtoDistanceMetric_AllValues(t *testing.T) {
	expected := map[string]pb.DistanceMetric{
		"COSINE":      pb.DistanceMetric_DISTANCE_COSINE,
		"L2":          pb.DistanceMetric_DISTANCE_EUCLIDEAN,
		"DOT_PRODUCT": pb.DistanceMetric_DISTANCE_DOT_PRODUCT,
	}
	for k, v := range expected {
		got := stringToProtoDistanceMetric[k]
		if got != v {
			t.Fatalf("expected %v for %q, got %v", v, k, got)
		}
	}
}

func TestProtoIndexTypeToString_AllValues(t *testing.T) {
	expected := map[pb.VectorIndexType]string{
		pb.VectorIndexType_INDEX_HNSW:     "HNSW",
		pb.VectorIndexType_INDEX_FLAT:     "FLAT",
		pb.VectorIndexType_INDEX_IVF_FLAT: "IVF",
	}
	for k, v := range expected {
		got := protoIndexTypeToString[k]
		if got != v {
			t.Fatalf("expected %q for %v, got %q", v, k, got)
		}
	}
}

func TestStringToProtoIndexType_AllValues(t *testing.T) {
	expected := map[string]pb.VectorIndexType{
		"HNSW": pb.VectorIndexType_INDEX_HNSW,
		"FLAT": pb.VectorIndexType_INDEX_FLAT,
		"IVF":  pb.VectorIndexType_INDEX_IVF_FLAT,
	}
	for k, v := range expected {
		got := stringToProtoIndexType[k]
		if got != v {
			t.Fatalf("expected %v for %q, got %v", v, k, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Scan handler test (previously uncovered)
// ---------------------------------------------------------------------------

func TestGRPC_Scan_Success(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["key1"] = []byte("val1")
	kvSvc.store["key2"] = []byte("val2")
	client, cleanup := startTestServer(t, kvSvc, newMockAdminService())
	defer cleanup()

	resp, err := client.Scan(context.Background(), &pb.ScanRequest{Prefix: ""})
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(resp.GetPairs()) != 2 {
		t.Fatalf("expected 2 pairs, got %d", len(resp.GetPairs()))
	}
}

func TestGRPC_Scan_WithPrefix(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["app:1"] = []byte("v1")
	kvSvc.store["app:2"] = []byte("v2")
	kvSvc.store["other:1"] = []byte("v3")
	client, cleanup := startTestServer(t, kvSvc, newMockAdminService())
	defer cleanup()

	resp, err := client.Scan(context.Background(), &pb.ScanRequest{Prefix: "app:"})
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(resp.GetPairs()) != 2 {
		t.Fatalf("expected 2 pairs with prefix 'app:', got %d", len(resp.GetPairs()))
	}
}

// ---------------------------------------------------------------------------
// dataPointPtrToProto test (previously uncovered)
// ---------------------------------------------------------------------------

func TestDataPointPtrToProto(t *testing.T) {
	dp := &timeseries.DataPoint{
		Metric:    "cpu.load",
		Value:     3.14,
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Tags:      map[string]string{"host": "node1"},
	}
	result := dataPointPtrToProto(dp)
	if result.GetMetric() != "cpu.load" {
		t.Fatalf("expected 'cpu.load', got %q", result.GetMetric())
	}
	if result.GetValue() != 3.14 {
		t.Fatalf("expected 3.14, got %f", result.GetValue())
	}
}

func TestDataPointPtrToProto_NoTags(t *testing.T) {
	dp := &timeseries.DataPoint{
		Metric:    "mem",
		Value:     1024,
		Timestamp: time.Unix(0, 1000),
	}
	result := dataPointPtrToProto(dp)
	if len(result.GetTags()) != 0 {
		t.Fatalf("expected 0 tags, got %d", len(result.GetTags()))
	}
}
