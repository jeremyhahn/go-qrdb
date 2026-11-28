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

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestDecodeVectorBody_AcceptsEverySDKField marshals each JSON body the Go
// SDK's REST transport posts, with every field set, and decodes it into the
// handler's strict DTO. A field the SDK sends that the DTO does not define
// would fail here as an unknown field.
func TestDecodeVectorBody_AcceptsEverySDKField(t *testing.T) {
	filter := map[string]interface{}{"k": "v"}
	meta := map[string]interface{}{"m": "x"}
	cfg := map[string]string{"m": "16"}
	cases := []struct {
		name string
		sdk  any
		dto  any
	}{
		{"CreateNamespace", &transport.CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "cosine", IndexType: "hnsw", IndexConfig: cfg, Metadata: meta}, &vectorCreateNamespaceRequest{}},
		{"UpdateNamespace", &transport.UpdateNamespaceRequest{Namespace: "ns", Dimension: 2, IndexConfig: cfg, Metadata: meta}, &vectorUpdateNamespaceRequest{}},
		{"DeleteNamespace", &transport.DeleteNamespaceRequest{Namespace: "ns"}, &vectorDeleteNamespaceRequest{}},
		{"UpsertVector", &transport.UpsertVectorRequest{Namespace: "ns", ID: 1, Vector: []float32{1, 2}, Metadata: meta}, &vectorUpsertRequest{}},
		{"DeleteVector", &transport.DeleteVectorRequest{Namespace: "ns", ID: 1}, &vectorDeleteRequest{}},
		{"BatchUpsert", &transport.BatchUpsertRequest{Namespace: "ns", Vectors: []*transport.VectorData{{ID: 1, Vector: []float32{1}, Metadata: meta, Version: 1, CreatedAt: 1, UpdatedAt: 1}}}, &vectorBatchUpsertRequest{}},
		{"BatchDeleteVectors", &transport.BatchDeleteVectorsRequest{Namespace: "ns", IDs: []uint64{1}}, &vectorBatchDeleteRequest{}},
		{"UpdateMetadata", &transport.UpdateVectorMetadataRequest{Namespace: "ns", ID: 1, Metadata: meta, MergeMetadata: true, DeleteKeys: []string{"a"}}, &vectorUpdateMetadataRequest{}},
		{"SearchSimilar", &transport.SearchSimilarRequest{Namespace: "ns", QueryVector: []float32{1}, K: 3, EfSearch: 8, MinScore: 0.5, IncludeVectors: true, IncludeMetadata: true, MetadataFilter: filter, Consistency: 1}, &vectorSearchSimilarRequest{}},
		{"SearchByMetadata", &transport.SearchByMetadataRequest{Namespace: "ns", Filters: filter, FilterOperator: "and", Limit: 1, Offset: 1, IncludeVectors: true, IncludeMetadata: true, Consistency: 1}, &vectorSearchByMetadataRequest{}},
		{"HybridSearch", &transport.HybridSearchRequest{Namespace: "ns", QueryVector: []float32{1}, K: 3, EfSearch: 8, MinScore: 0.5, MetadataFilter: filter, FilterOperator: "and", RerankingAlgorithm: "rrf", IncludeVectors: true, IncludeMetadata: true, Consistency: 1}, &vectorHybridSearchRequest{}},
		{"SearchByID", &transport.SearchByIDRequest{Namespace: "ns", ID: 1, K: 3, EfSearch: 8, MinScore: 0.5, IncludeVectors: true, IncludeMetadata: true, MetadataFilter: filter, ExcludeSelf: true, Consistency: 1}, &vectorSearchByIDRequest{}},
		{"RebuildIndex", &transport.RebuildIndexRequest{Namespace: "ns", IndexConfig: cfg, Async: true}, &vectorRebuildIndexRequest{}},
		{"CompactNamespace", &transport.CompactNamespaceRequest{Namespace: "ns", Async: true, TimeoutMs: 10}, &vectorCompactRequest{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.sdk)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			if err := decodeVectorBody(r, tc.dto); err != nil {
				t.Fatalf("decode SDK body %s: %v", body, err)
			}
		})
	}
}

// TestDecodeVectorBody_RefusesUndefinedField keeps the strict decode: a field
// neither the SDK nor the server defines is refused.
func TestDecodeVectorBody_RefusesUndefinedField(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"namespace":"ns","bogus":1}`)))
	if err := decodeVectorBody(r, &vectorSearchSimilarRequest{}); err == nil {
		t.Fatal("decode accepted an undefined field")
	}
}

// TestHandleVectorSearchSimilar_PassesSDKOptions checks the options the SDK
// sets reach the service instead of being dropped after the decode.
func TestHandleVectorSearchSimilar_PassesSDKOptions(t *testing.T) {
	var captured *service.SearchSimilarRequest
	vecSvc := &mockVectorService{
		searchSimilarFn: func(_ context.Context, req *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
			captured = req
			return &service.SearchSimilarResponse{}, nil
		},
	}
	srv := newVectorServer(vecSvc)
	body, err := json.Marshal(&transport.SearchSimilarRequest{
		Namespace: "ns", QueryVector: []float32{1}, K: 2,
		IncludeVectors: true, IncludeMetadata: true,
		MetadataFilter: map[string]interface{}{"k": "v"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/vector/search/similar", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if captured == nil {
		t.Fatal("SearchSimilar was not called")
	}
	if !captured.IncludeVectors || !captured.IncludeMetadata || captured.MetadataFilter["k"] != "v" {
		t.Fatalf("options dropped: %+v", captured)
	}
}
