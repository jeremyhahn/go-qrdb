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
	"errors"
	"math"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// formatVectorID tests
// ---------------------------------------------------------------------------

func TestFormatVectorID_Zero(t *testing.T) {
	if got := formatVectorID(0); got != "0" {
		t.Errorf("formatVectorID(0) = %q, want %q", got, "0")
	}
}

func TestFormatVectorID_Positive(t *testing.T) {
	if got := formatVectorID(12345); got != "12345" {
		t.Errorf("formatVectorID(12345) = %q, want %q", got, "12345")
	}
}

func TestFormatVectorID_MaxUint64(t *testing.T) {
	expected := "18446744073709551615"
	if got := formatVectorID(math.MaxUint64); got != expected {
		t.Errorf("formatVectorID(MaxUint64) = %q, want %q", got, expected)
	}
}

// ---------------------------------------------------------------------------
// parseVectorID tests
// ---------------------------------------------------------------------------

func TestParseVectorID_Empty(t *testing.T) {
	got, err := parseVectorID("")
	if err != nil {
		t.Fatalf("parseVectorID(\"\") returned error: %v", err)
	}
	if got != 0 {
		t.Errorf("parseVectorID(\"\") = %d, want 0", got)
	}
}

func TestParseVectorID_Valid(t *testing.T) {
	got, err := parseVectorID("42")
	if err != nil {
		t.Fatalf("parseVectorID(\"42\") returned error: %v", err)
	}
	if got != 42 {
		t.Errorf("parseVectorID(\"42\") = %d, want 42", got)
	}
}

func TestParseVectorID_Invalid(t *testing.T) {
	_, err := parseVectorID("not-a-number")
	if err == nil {
		t.Fatal("parseVectorID(\"not-a-number\") should return error")
	}
}

func TestParseVectorID_Negative(t *testing.T) {
	_, err := parseVectorID("-1")
	if err == nil {
		t.Fatal("parseVectorID(\"-1\") should return error for uint64")
	}
}

// ---------------------------------------------------------------------------
// vectorDataToProto tests
// ---------------------------------------------------------------------------

func TestVectorDataToProto_Nil(t *testing.T) {
	result, err := vectorDataToProto(nil)
	if err != nil {
		t.Fatalf("vectorDataToProto(nil) returned error: %v", err)
	}
	if result != nil {
		t.Errorf("vectorDataToProto(nil) = %v, want nil", result)
	}
}

func TestVectorDataToProto_Valid(t *testing.T) {
	vd := &transport.VectorData{
		ID:        100,
		Vector:    []float32{1.0, 2.0, 3.0},
		Metadata:  map[string]interface{}{"category": "test"},
		Version:   2,
		CreatedAt: 1000,
		UpdatedAt: 2000,
	}
	result, err := vectorDataToProto(vd)
	if err != nil {
		t.Fatalf("vectorDataToProto() returned error: %v", err)
	}
	if result == nil {
		t.Fatal("vectorDataToProto() returned nil")
	}
	if result.Id != "100" {
		t.Errorf("Id = %q, want %q", result.Id, "100")
	}
	if len(result.Vector) != 3 {
		t.Fatalf("Vector length = %d, want 3", len(result.Vector))
	}
	if result.Vector[0] != 1.0 {
		t.Errorf("Vector[0] = %f, want 1.0", result.Vector[0])
	}
	if result.Version != 2 {
		t.Errorf("Version = %d, want 2", result.Version)
	}
	if result.CreatedAt != 1000 {
		t.Errorf("CreatedAt = %d, want 1000", result.CreatedAt)
	}
}

func TestVectorDataToProto_NoMetadata(t *testing.T) {
	vd := &transport.VectorData{
		ID:     1,
		Vector: []float32{0.5},
	}
	result, err := vectorDataToProto(vd)
	if err != nil {
		t.Fatalf("vectorDataToProto() returned error: %v", err)
	}
	if result.MetadataJson != "" {
		t.Errorf("MetadataJson = %q, want empty", result.MetadataJson)
	}
}

// ---------------------------------------------------------------------------
// protoToVectorData tests
// ---------------------------------------------------------------------------

func TestProtoToVectorData_Nil(t *testing.T) {
	result, err := protoToVectorData(nil)
	if err != nil {
		t.Fatalf("protoToVectorData(nil) returned error: %v", err)
	}
	if result != nil {
		t.Errorf("protoToVectorData(nil) = %v, want nil", result)
	}
}

func TestProtoToVectorData_Valid(t *testing.T) {
	p := &pb.VectorDataProto{
		Id:           "42",
		Vector:       []float32{1.0, 2.0},
		MetadataJson: `{"key":"value"}`,
		Version:      3,
		CreatedAt:    1000,
		UpdatedAt:    2000,
	}
	result, err := protoToVectorData(p)
	if err != nil {
		t.Fatalf("protoToVectorData() returned error: %v", err)
	}
	if result == nil {
		t.Fatal("protoToVectorData() returned nil")
	}
	if result.ID != 42 {
		t.Errorf("ID = %d, want 42", result.ID)
	}
	if len(result.Vector) != 2 {
		t.Fatalf("Vector length = %d, want 2", len(result.Vector))
	}
	if result.Metadata == nil {
		t.Fatal("Metadata is nil")
	}
	if result.Metadata["key"] != "value" {
		t.Errorf("Metadata[\"key\"] = %v, want \"value\"", result.Metadata["key"])
	}
	if result.Version != 3 {
		t.Errorf("Version = %d, want 3", result.Version)
	}
}

func TestProtoToVectorData_NoMetadata(t *testing.T) {
	p := &pb.VectorDataProto{
		Id:     "1",
		Vector: []float32{0.5},
	}
	result, err := protoToVectorData(p)
	if err != nil {
		t.Fatalf("protoToVectorData() returned error: %v", err)
	}
	if result.Metadata != nil {
		t.Errorf("Metadata = %v, want nil", result.Metadata)
	}
}

func TestProtoToVectorData_InvalidID(t *testing.T) {
	p := &pb.VectorDataProto{
		Id: "not-a-number",
	}
	_, err := protoToVectorData(p)
	if err == nil {
		t.Fatal("protoToVectorData() should return error for invalid ID")
	}
}

func TestProtoToVectorData_InvalidMetadata(t *testing.T) {
	p := &pb.VectorDataProto{
		Id:           "1",
		MetadataJson: "not-valid-json",
	}
	_, err := protoToVectorData(p)
	if err == nil {
		t.Fatal("protoToVectorData() should return error for invalid JSON")
	}
}

func TestProtoToNamespaceInfo_IndexConfigAndMetadata(t *testing.T) {
	got, err := protoToNamespaceInfo(&pb.VectorNamespaceInfo{
		Namespace:    "ns",
		Dimension:    4,
		IndexType:    pb.VectorIndexType_INDEX_HNSW,
		IndexConfig:  map[string]string{"ef_construction": "321"},
		MetadataJson: `{"owner":"team-a"}`,
	})
	if err != nil {
		t.Fatalf("protoToNamespaceInfo() error = %v", err)
	}
	if got.Dimension != 4 || got.IndexType != transport.IndexHNSW {
		t.Fatalf("protoToNamespaceInfo() = %+v", got)
	}
	if got.IndexConfig["ef_construction"] != "321" || got.Metadata["owner"] != "team-a" {
		t.Fatalf("IndexConfig = %v, Metadata = %v", got.IndexConfig, got.Metadata)
	}
}

func TestProtoToNamespaceInfo_InvalidMetadata(t *testing.T) {
	if _, err := protoToNamespaceInfo(&pb.VectorNamespaceInfo{Namespace: "ns", MetadataJson: "{{bad-json"}); err == nil {
		t.Fatal("protoToNamespaceInfo() should return error for invalid metadata")
	}
}

// ---------------------------------------------------------------------------
// protoToSearchResults tests
// ---------------------------------------------------------------------------

func TestProtoToSearchResults_Empty(t *testing.T) {
	results, err := protoToSearchResults(nil)
	if err != nil {
		t.Fatalf("protoToSearchResults(nil) returned error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("results length = %d, want 0", len(results))
	}
}

func TestProtoToSearchResults_Valid(t *testing.T) {
	pbResults := []*pb.VectorSearchResultProto{
		{
			Id:           "1",
			Score:        0.95,
			Vector:       []float32{1.0, 2.0},
			MetadataJson: `{"type":"test"}`,
		},
		{
			Id:    "2",
			Score: 0.80,
		},
	}
	results, err := protoToSearchResults(pbResults)
	if err != nil {
		t.Fatalf("protoToSearchResults() returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results length = %d, want 2", len(results))
	}
	if results[0].ID != 1 {
		t.Errorf("results[0].ID = %d, want 1", results[0].ID)
	}
	if results[0].Score != 0.95 {
		t.Errorf("results[0].Score = %f, want 0.95", results[0].Score)
	}
	if results[0].Metadata["type"] != "test" {
		t.Errorf("results[0].Metadata[\"type\"] = %v, want \"test\"", results[0].Metadata["type"])
	}
	if results[1].ID != 2 {
		t.Errorf("results[1].ID = %d, want 2", results[1].ID)
	}
}

func TestProtoToSearchResults_InvalidID(t *testing.T) {
	pbResults := []*pb.VectorSearchResultProto{
		{Id: "invalid"},
	}
	_, err := protoToSearchResults(pbResults)
	if err == nil {
		t.Fatal("protoToSearchResults() should return error for invalid ID")
	}
}

func TestProtoToSearchResults_InvalidMetadata(t *testing.T) {
	pbResults := []*pb.VectorSearchResultProto{
		{Id: "1", MetadataJson: "{{bad-json"},
	}
	_, err := protoToSearchResults(pbResults)
	if err == nil {
		t.Fatal("protoToSearchResults() should return error for invalid metadata JSON")
	}
}

// ---------------------------------------------------------------------------
// marshalMetadataJSON tests
// ---------------------------------------------------------------------------

func TestMarshalMetadataJSON_Nil(t *testing.T) {
	result, err := marshalMetadataJSON(nil)
	if err != nil {
		t.Fatalf("marshalMetadataJSON(nil) returned error: %v", err)
	}
	if result != "" {
		t.Errorf("marshalMetadataJSON(nil) = %q, want empty", result)
	}
}

func TestMarshalMetadataJSON_Empty(t *testing.T) {
	result, err := marshalMetadataJSON(map[string]string{})
	if err != nil {
		t.Fatalf("marshalMetadataJSON({}) returned error: %v", err)
	}
	if result != "" {
		t.Errorf("marshalMetadataJSON({}) = %q, want empty", result)
	}
}

func TestMarshalMetadataJSON_Valid(t *testing.T) {
	m := map[string]string{"key": "value", "foo": "bar"}
	result, err := marshalMetadataJSON(m)
	if err != nil {
		t.Fatalf("marshalMetadataJSON() returned error: %v", err)
	}
	if result == "" {
		t.Fatal("marshalMetadataJSON() returned empty string")
	}
	// Verify it contains the expected data (JSON key order may vary).
	if len(result) < 10 {
		t.Errorf("marshalMetadataJSON() result too short: %q", result)
	}
}

// ---------------------------------------------------------------------------
// marshalInterfaceMetadataJSON tests
// ---------------------------------------------------------------------------

func TestMarshalInterfaceMetadataJSON_Nil(t *testing.T) {
	result, err := marshalInterfaceMetadataJSON(nil)
	if err != nil {
		t.Fatalf("marshalInterfaceMetadataJSON(nil) returned error: %v", err)
	}
	if result != "" {
		t.Errorf("marshalInterfaceMetadataJSON(nil) = %q, want empty", result)
	}
}

func TestMarshalInterfaceMetadataJSON_Empty(t *testing.T) {
	result, err := marshalInterfaceMetadataJSON(map[string]interface{}{})
	if err != nil {
		t.Fatalf("marshalInterfaceMetadataJSON({}) returned error: %v", err)
	}
	if result != "" {
		t.Errorf("marshalInterfaceMetadataJSON({}) = %q, want empty", result)
	}
}

func TestMarshalInterfaceMetadataJSON_Valid(t *testing.T) {
	m := map[string]interface{}{"count": 42, "active": true}
	result, err := marshalInterfaceMetadataJSON(m)
	if err != nil {
		t.Fatalf("marshalInterfaceMetadataJSON() returned error: %v", err)
	}
	if result == "" {
		t.Fatal("marshalInterfaceMetadataJSON() returned empty string")
	}
}

func TestMarshalInterfaceMetadataJSON_InvalidValue(t *testing.T) {
	m := map[string]interface{}{"bad": make(chan int)}
	_, err := marshalInterfaceMetadataJSON(m)
	if err == nil {
		t.Fatal("marshalInterfaceMetadataJSON() should return error for channel value")
	}
	var metaErr *metadataMarshalError
	if !errors.As(err, &metaErr) {
		t.Errorf("expected metadataMarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// unmarshalInterfaceMetadataJSON tests
// ---------------------------------------------------------------------------

func TestUnmarshalInterfaceMetadataJSON_Empty(t *testing.T) {
	result, err := unmarshalInterfaceMetadataJSON("")
	if err != nil {
		t.Fatalf("unmarshalInterfaceMetadataJSON(\"\") returned error: %v", err)
	}
	if result != nil {
		t.Errorf("unmarshalInterfaceMetadataJSON(\"\") = %v, want nil", result)
	}
}

func TestUnmarshalInterfaceMetadataJSON_Valid(t *testing.T) {
	result, err := unmarshalInterfaceMetadataJSON(`{"key":"value","num":42}`)
	if err != nil {
		t.Fatalf("unmarshalInterfaceMetadataJSON() returned error: %v", err)
	}
	if result == nil {
		t.Fatal("unmarshalInterfaceMetadataJSON() returned nil")
	}
	if result["key"] != "value" {
		t.Errorf("result[\"key\"] = %v, want \"value\"", result["key"])
	}
}

func TestUnmarshalInterfaceMetadataJSON_Invalid(t *testing.T) {
	_, err := unmarshalInterfaceMetadataJSON("{invalid-json}")
	if err == nil {
		t.Fatal("unmarshalInterfaceMetadataJSON() should return error for invalid JSON")
	}
	var metaErr *metadataUnmarshalError
	if !errors.As(err, &metaErr) {
		t.Errorf("expected metadataUnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Distance metric and index type map tests
// ---------------------------------------------------------------------------

func TestDistanceMetricToProto_AllValues(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected pb.DistanceMetric
	}{
		{"Cosine", transport.DistanceCosine, pb.DistanceMetric_DISTANCE_COSINE},
		{"Euclidean", transport.DistanceEuclidean, pb.DistanceMetric_DISTANCE_EUCLIDEAN},
		{"DotProduct", transport.DistanceDotProduct, pb.DistanceMetric_DISTANCE_DOT_PRODUCT},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := transportDistanceMetricToProto[tt.input]
			if got != tt.expected {
				t.Errorf("transportDistanceMetricToProto[%q] = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestProtoDistanceMetricToTransport_AllValues(t *testing.T) {
	tests := []struct {
		name     string
		input    pb.DistanceMetric
		expected string
	}{
		{"Cosine", pb.DistanceMetric_DISTANCE_COSINE, transport.DistanceCosine},
		{"Euclidean", pb.DistanceMetric_DISTANCE_EUCLIDEAN, transport.DistanceEuclidean},
		{"DotProduct", pb.DistanceMetric_DISTANCE_DOT_PRODUCT, transport.DistanceDotProduct},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := protoDistanceMetricToTransport[tt.input]
			if got != tt.expected {
				t.Errorf("protoDistanceMetricToTransport[%v] = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestIndexTypeToProto_AllValues(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected pb.VectorIndexType
	}{
		{"HNSW", transport.IndexHNSW, pb.VectorIndexType_INDEX_HNSW},
		{"Flat", transport.IndexFlat, pb.VectorIndexType_INDEX_FLAT},
		{"IVFFlat", transport.IndexIVFFlat, pb.VectorIndexType_INDEX_IVF_FLAT},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := indexTypeToProto[tt.input]
			if got != tt.expected {
				t.Errorf("indexTypeToProto[%q] = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestProtoIndexTypeToTransport_AllValues(t *testing.T) {
	tests := []struct {
		name     string
		input    pb.VectorIndexType
		expected string
	}{
		{"HNSW", pb.VectorIndexType_INDEX_HNSW, transport.IndexHNSW},
		{"Flat", pb.VectorIndexType_INDEX_FLAT, transport.IndexFlat},
		{"IVFFlat", pb.VectorIndexType_INDEX_IVF_FLAT, transport.IndexIVFFlat},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := protoIndexTypeToTransport[tt.input]
			if got != tt.expected {
				t.Errorf("protoIndexTypeToTransport[%v] = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Closed state tests for Vector methods
// ---------------------------------------------------------------------------

func TestTransport_GetNamespaceInfo_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("GetNamespaceInfo() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_UpsertVector_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.UpsertVector(ctx, &transport.UpsertVectorRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("UpsertVector() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_GetVector_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.GetVector(ctx, &transport.GetVectorRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("GetVector() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_DeleteVector_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.DeleteVector(ctx, &transport.DeleteVectorRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("DeleteVector() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_SearchSimilar_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.SearchSimilar(ctx, &transport.SearchSimilarRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("SearchSimilar() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_BatchUpsert_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.BatchUpsert(ctx, &transport.BatchUpsertRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("BatchUpsert() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_BatchDeleteVectors_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("BatchDeleteVectors() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_ListVectors_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.ListVectors(ctx, &transport.ListVectorsRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("ListVectors() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_UpdateMetadata_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("UpdateMetadata() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_SearchByMetadata_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("SearchByMetadata() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_HybridSearch_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.HybridSearch(ctx, &transport.HybridSearchRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("HybridSearch() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_SearchByID_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.SearchByID(ctx, &transport.SearchByIDRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("SearchByID() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_RebuildIndex_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.RebuildIndex(ctx, &transport.RebuildIndexRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("RebuildIndex() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_GetRebuildProgress_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("GetRebuildProgress() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_GetIndexStats_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.GetIndexStats(ctx, &transport.GetIndexStatsRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("GetIndexStats() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_CompactNamespace_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.CompactNamespace(ctx, &transport.CompactNamespaceRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("CompactNamespace() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestTransport_GetCompactionProgress_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	ctx := context.Background()
	_, err = tr.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{Namespace: "test"})
	if err == nil {
		t.Fatal("GetCompactionProgress() on closed transport should return error")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}
