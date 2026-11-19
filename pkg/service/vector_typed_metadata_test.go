// Copyright (c) 2026 Jeremy Hahn
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

package service

import (
	"context"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

func newTypedMetadataService(t *testing.T) VectorService {
	t.Helper()
	svc, err := NewVectorService(vector.NewMemoryVectorStorage(), newVecTestLogger())
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	if _, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "typed", Dimension: 2, DistanceMetric: "COSINE", IndexType: "HNSW",
	}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	return svc
}

// TestUpsertVector_MetadataKeepsJSONTypes proves a number, a bool and an
// array stored as vector metadata come back with their JSON types.
func TestUpsertVector_MetadataKeepsJSONTypes(t *testing.T) {
	svc := newTypedMetadataService(t)
	ctx := context.Background()
	meta := map[string]interface{}{
		"score": float64(0.75),
		"live":  true,
		"tags":  []interface{}{"a", float64(2)},
		"name":  "doc",
	}
	if _, err := svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "typed", ID: 1, Vector: []float32{1, 0}, Metadata: meta}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	got, err := svc.GetVector(ctx, &GetVectorRequest{Namespace: "typed", ID: 1, IncludeMetadata: true})
	if err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if !reflect.DeepEqual(got.Metadata, meta) {
		t.Fatalf("metadata = %#v, want %#v", got.Metadata, meta)
	}
}

// TestListVectors_MetadataKeepsJSONTypes pins that ListVectors decodes the
// stored JSON metadata like GetVector does: a string comes back without its
// JSON quotes, a number as float64 and a bool as bool.
func TestListVectors_MetadataKeepsJSONTypes(t *testing.T) {
	svc := newTypedMetadataService(t)
	ctx := context.Background()
	meta := map[string]interface{}{
		"doc_id": "doc-kept",
		"score":  float64(3),
		"live":   true,
	}
	if _, err := svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "typed", ID: 1, Vector: []float32{1, 0}, Metadata: meta}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	resp, err := svc.ListVectors(ctx, &ListVectorsRequest{Namespace: "typed", IncludeMetadata: true})
	if err != nil {
		t.Fatalf("ListVectors: %v", err)
	}
	if len(resp.Vectors) != 1 {
		t.Fatalf("ListVectors returned %d vectors, want 1", len(resp.Vectors))
	}
	if got := resp.Vectors[0].Metadata; !reflect.DeepEqual(got, meta) {
		t.Fatalf("metadata = %#v, want %#v", got, meta)
	}
}

func TestUpdateMetadata_MergeKeepsJSONTypes(t *testing.T) {
	svc := newTypedMetadataService(t)
	ctx := context.Background()
	meta := map[string]interface{}{
		"doc_id": "doc",
		"score":  float64(5),
		"live":   true,
		"drop":   "me",
	}
	if _, err := svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "typed", ID: 1, Vector: []float32{1, 0}, Metadata: meta}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	updates := []*UpdateVectorMetadataRequest{
		{Namespace: "typed", ID: 1, MergeMetadata: true, Metadata: map[string]interface{}{"extra": float64(1)}},
		{Namespace: "typed", ID: 1, MergeMetadata: true, DeleteKeys: []string{"drop"}},
		{Namespace: "typed", ID: 1, MergeMetadata: true, Metadata: map[string]interface{}{"extra": float64(2)}},
	}
	for i, u := range updates {
		if _, err := svc.UpdateMetadata(ctx, u); err != nil {
			t.Fatalf("UpdateMetadata #%d: %v", i, err)
		}
	}
	resp, err := svc.ListVectors(ctx, &ListVectorsRequest{Namespace: "typed", IncludeMetadata: true})
	if err != nil {
		t.Fatalf("ListVectors: %v", err)
	}
	if len(resp.Vectors) != 1 {
		t.Fatalf("ListVectors returned %d vectors, want 1", len(resp.Vectors))
	}
	want := map[string]interface{}{
		"doc_id": "doc",
		"score":  float64(5),
		"live":   true,
		"extra":  float64(2),
	}
	if got := resp.Vectors[0].Metadata; !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata = %#v, want %#v", got, want)
	}
}

func TestSearchByMetadata_MatchesPreUpgradePlainStringEntries(t *testing.T) {
	store := vector.NewMemoryVectorStorage()
	svc, err := NewVectorService(store, newVecTestLogger())
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	if _, err := svc.CreateNamespace(ctx, &CreateNamespaceRequest{
		Namespace: "typed", Dimension: 2, DistanceMetric: "COSINE", IndexType: "HNSW",
	}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	// ID 1 is a record written before metadata kept its JSON types: the
	// string is stored and indexed verbatim.
	if err := store.UpsertVector(ctx, scopedFromContext(ctx, "typed"), &vector.VectorValue{
		ID: 1, Embedding: []float32{1, 0}, Metadata: map[string]string{"topic": "news"},
	}); err != nil {
		t.Fatalf("legacy UpsertVector: %v", err)
	}
	// ID 2 is written now, JSON-encoded; ID 3 does not match; ID 4 holds the
	// number 5, which a filter on the string "5" must not match.
	writes := []*UpsertVectorRequest{
		{Namespace: "typed", ID: 2, Vector: []float32{0, 1}, Metadata: map[string]interface{}{"topic": "news"}},
		{Namespace: "typed", ID: 3, Vector: []float32{0, 1}, Metadata: map[string]interface{}{"topic": "sport"}},
		{Namespace: "typed", ID: 4, Vector: []float32{0, 1}, Metadata: map[string]interface{}{"topic": float64(5)}},
	}
	for _, w := range writes {
		if _, err := svc.UpsertVector(ctx, w); err != nil {
			t.Fatalf("UpsertVector %d: %v", w.ID, err)
		}
	}
	for _, tc := range []struct {
		value interface{}
		want  []uint64
	}{
		{"news", []uint64{1, 2}},
		{"5", nil},
		{float64(5), []uint64{4}},
	} {
		resp, err := svc.SearchByMetadata(ctx, &SearchByMetadataRequest{
			Namespace: "typed", Filters: map[string]interface{}{"topic": tc.value},
		})
		if err != nil {
			t.Fatalf("SearchByMetadata(%#v): %v", tc.value, err)
		}
		var ids []uint64
		for _, r := range resp.Results {
			ids = append(ids, r.ID)
		}
		if !reflect.DeepEqual(ids, tc.want) {
			t.Fatalf("SearchByMetadata(%#v) matched IDs = %v, want %v", tc.value, ids, tc.want)
		}
	}
}

// TestUpsertVector_ReportsCreated proves the first upsert of an ID reports
// created and a second upsert of the same ID does not.
func TestUpsertVector_ReportsCreated(t *testing.T) {
	svc := newTypedMetadataService(t)
	ctx := context.Background()
	req := &UpsertVectorRequest{Namespace: "typed", ID: 7, Vector: []float32{0, 1}}
	first, err := svc.UpsertVector(ctx, req)
	if err != nil {
		t.Fatalf("first UpsertVector: %v", err)
	}
	if !first.Created {
		t.Fatal("first upsert: created = false, want true")
	}
	second, err := svc.UpsertVector(ctx, req)
	if err != nil {
		t.Fatalf("second UpsertVector: %v", err)
	}
	if second.Created {
		t.Fatal("second upsert: created = true, want false")
	}
}
