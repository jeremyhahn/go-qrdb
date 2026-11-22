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
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
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

// TestSearchByMetadata_MixedTypeCompoundFilterSkipsLegacy checks that a
// compound filter with a non-string predicate does not match a legacy record
// that holds that value as a string.
func TestSearchByMetadata_MixedTypeCompoundFilterSkipsLegacy(t *testing.T) {
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
	if err := store.UpsertVector(ctx, scopedFromContext(ctx, "typed"), &vector.VectorValue{
		ID: 1, Embedding: []float32{1, 0}, Metadata: map[string]string{"topic": "news", "score": "5"},
	}); err != nil {
		t.Fatalf("legacy UpsertVector: %v", err)
	}
	if _, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "typed", ID: 2, Vector: []float32{0, 1},
		Metadata: map[string]interface{}{"topic": "news", "score": float64(5)},
	}); err != nil {
		t.Fatalf("UpsertVector 2: %v", err)
	}
	for _, tc := range []struct {
		score interface{}
		want  []uint64
	}{
		{float64(5), []uint64{2}},
		{"5", []uint64{1}},
	} {
		resp, err := svc.SearchByMetadata(ctx, &SearchByMetadataRequest{
			Namespace: "typed", Filters: map[string]interface{}{"topic": "news", "score": tc.score},
		})
		if err != nil {
			t.Fatalf("SearchByMetadata(score=%#v): %v", tc.score, err)
		}
		var ids []uint64
		for _, r := range resp.Results {
			ids = append(ids, r.ID)
		}
		if !reflect.DeepEqual(ids, tc.want) {
			t.Fatalf("SearchByMetadata(score=%#v) matched IDs = %v, want %v", tc.score, ids, tc.want)
		}
	}
}

// TestSearchByMetadata_EmptyFiltersListLegacyAndJSON checks that an empty
// filter list matches every record, legacy-format ones included, and that
// offset and limit page across both formats in ID order.
func TestSearchByMetadata_EmptyFiltersListLegacyAndJSON(t *testing.T) {
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
	// ID 1 is a legacy-format record; ID 2 is written now, JSON-encoded.
	if err := store.UpsertVector(ctx, scopedFromContext(ctx, "typed"), &vector.VectorValue{
		ID: 1, Embedding: []float32{1, 0}, Metadata: map[string]string{"topic": "news"},
		MetadataFormat: vector.MetadataFormatLegacy,
	}); err != nil {
		t.Fatalf("legacy UpsertVector: %v", err)
	}
	if _, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "typed", ID: 2, Vector: []float32{0, 1}, Metadata: map[string]interface{}{"topic": "sport"},
	}); err != nil {
		t.Fatalf("UpsertVector 2: %v", err)
	}
	for _, tc := range []struct {
		limit, offset int
		want          []uint64
	}{
		{0, 0, []uint64{1, 2}},
		{1, 0, []uint64{1}},
		{1, 1, []uint64{2}},
		{1, 2, nil},
	} {
		resp, err := svc.SearchByMetadata(ctx, &SearchByMetadataRequest{
			Namespace: "typed", Filters: map[string]interface{}{}, Limit: tc.limit, Offset: tc.offset,
		})
		if err != nil {
			t.Fatalf("SearchByMetadata(limit %d, offset %d): %v", tc.limit, tc.offset, err)
		}
		var ids []uint64
		for _, r := range resp.Results {
			ids = append(ids, r.ID)
		}
		if !reflect.DeepEqual(ids, tc.want) {
			t.Fatalf("SearchByMetadata(limit %d, offset %d) IDs = %v, want %v", tc.limit, tc.offset, ids, tc.want)
		}
	}
}

// TestUpsert_MetadataFormatFollowsTheFeatureGate checks that upserts and
// batch upserts store legacy-format metadata until
// featuregate.VectorMetadataJSON is active for the group, and JSON after.
func TestUpsert_MetadataFormatFollowsTheFeatureGate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		active bool
		format uint8
		stored string
	}{
		{"gated off", false, vector.MetadataFormatLegacy, "news"},
		{"gated on", true, vector.MetadataFormatJSON, `"news"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := vector.NewMemoryVectorStorage()
			gates := &gatedRaftNode{enabled: map[feature.Feature]bool{featuregate.VectorMetadataJSON: tc.active}}
			svc, err := NewVectorService(store, newVecTestLogger(), WithFeatureGates(gates))
			if err != nil {
				t.Fatalf("NewVectorService: %v", err)
			}
			ctx := context.Background()
			if _, err := svc.CreateNamespace(ctx, &CreateNamespaceRequest{
				Namespace: "typed", Dimension: 2, DistanceMetric: "COSINE", IndexType: "HNSW",
			}); err != nil {
				t.Fatalf("CreateNamespace: %v", err)
			}
			meta := map[string]interface{}{"topic": "news"}
			if _, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
				Namespace: "typed", ID: 1, Vector: []float32{1, 0}, Metadata: meta,
			}); err != nil {
				t.Fatalf("UpsertVector: %v", err)
			}
			if _, err := svc.BatchUpsert(ctx, &BatchUpsertRequest{
				Namespace: "typed", Vectors: []*VectorData{{ID: 2, Vector: []float32{0, 1}, Metadata: meta}},
			}); err != nil {
				t.Fatalf("BatchUpsert: %v", err)
			}
			for _, id := range []uint64{1, 2} {
				rec, err := store.GetVector(ctx, scopedFromContext(ctx, "typed"), id)
				if err != nil {
					t.Fatalf("GetVector %d: %v", id, err)
				}
				if rec.MetadataFormat != tc.format || rec.Metadata["topic"] != tc.stored {
					t.Fatalf("record %d: format %d topic %q, want format %d topic %q",
						id, rec.MetadataFormat, rec.Metadata["topic"], tc.format, tc.stored)
				}
			}
		})
	}
}

// TestSearchByMetadata_LegacyJSONLookingStringsStayStrings proves a
// pre-upgrade entry whose verbatim text is itself valid JSON ("5", "true", a
// quoted string) still reads back as that string and still matches a string
// filter, while a typed value of the same text matches only its typed filter.
func TestSearchByMetadata_LegacyJSONLookingStringsStayStrings(t *testing.T) {
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
	legacy := map[uint64]string{1: "5", 2: "true", 3: `"q"`}
	for id, text := range legacy {
		if err := store.UpsertVector(ctx, scopedFromContext(ctx, "typed"), &vector.VectorValue{
			ID: id, Embedding: []float32{1, 0}, Metadata: map[string]string{"v": text},
		}); err != nil {
			t.Fatalf("legacy UpsertVector %d: %v", id, err)
		}
	}
	typed := []*UpsertVectorRequest{
		{Namespace: "typed", ID: 4, Vector: []float32{0, 1}, Metadata: map[string]interface{}{"v": float64(5)}},
		{Namespace: "typed", ID: 5, Vector: []float32{0, 1}, Metadata: map[string]interface{}{"v": true}},
		{Namespace: "typed", ID: 6, Vector: []float32{0, 1}, Metadata: map[string]interface{}{"v": "5"}},
	}
	for _, w := range typed {
		if _, err := svc.UpsertVector(ctx, w); err != nil {
			t.Fatalf("UpsertVector %d: %v", w.ID, err)
		}
	}
	for _, tc := range []struct {
		value interface{}
		want  []uint64
	}{
		{"5", []uint64{1, 6}},
		{"true", []uint64{2}},
		{`"q"`, []uint64{3}},
		{float64(5), []uint64{4}},
		{true, []uint64{5}},
	} {
		resp, err := svc.SearchByMetadata(ctx, &SearchByMetadataRequest{
			Namespace: "typed", Filters: map[string]interface{}{"v": tc.value}, IncludeMetadata: true,
		})
		if err != nil {
			t.Fatalf("SearchByMetadata(%#v): %v", tc.value, err)
		}
		var ids []uint64
		for _, r := range resp.Results {
			ids = append(ids, r.ID)
			if text, ok := legacy[r.ID]; ok {
				if got, isStr := r.Metadata["v"].(string); !isStr || got != text {
					t.Fatalf("legacy ID %d metadata = %#v, want the string %q", r.ID, r.Metadata["v"], text)
				}
			}
		}
		if !reflect.DeepEqual(ids, tc.want) {
			t.Fatalf("SearchByMetadata(%#v) matched IDs = %v, want %v", tc.value, ids, tc.want)
		}
	}
}

// TestSearchByMetadata_RefusesNegativeOffset proves a negative offset is an
// invalid argument, refused before the scan, even when a legacy entry makes
// the search take the merge path that slices by the offset.
func TestSearchByMetadata_RefusesNegativeOffset(t *testing.T) {
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
	if err := store.UpsertVector(ctx, scopedFromContext(ctx, "typed"), &vector.VectorValue{
		ID: 1, Embedding: []float32{1, 0}, Metadata: map[string]string{"topic": "news"},
	}); err != nil {
		t.Fatalf("legacy UpsertVector: %v", err)
	}
	if _, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "typed", ID: 2, Vector: []float32{0, 1}, Metadata: map[string]interface{}{"topic": "news"},
	}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	resp, err := svc.SearchByMetadata(ctx, &SearchByMetadataRequest{
		Namespace: "typed", Filters: map[string]interface{}{"topic": "news"}, Offset: -1,
	})
	var invalid *InvalidVectorError
	if !errors.As(err, &invalid) {
		t.Fatalf("SearchByMetadata(offset -1) = %v, %v; want InvalidVectorError", resp, err)
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

// TestLegacyControlPrefixedMetadataRoundTrips proves a record written before
// metadata carried a format version, whose string begins with \u0001, reads
// back verbatim through get and list and keeps that value through a merge.
func TestLegacyControlPrefixedMetadataRoundTrips(t *testing.T) {
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
	if err := store.UpsertVector(ctx, scopedFromContext(ctx, "typed"), &vector.VectorValue{
		ID: 1, Embedding: []float32{1, 0}, Metadata: map[string]string{"k": "\x01true", "s": "plain"},
	}); err != nil {
		t.Fatalf("legacy UpsertVector: %v", err)
	}
	want := map[string]interface{}{"k": "\x01true", "s": "plain"}

	got, err := svc.GetVector(ctx, &GetVectorRequest{Namespace: "typed", ID: 1, IncludeMetadata: true})
	if err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if !reflect.DeepEqual(got.Metadata, want) {
		t.Fatalf("get metadata = %#v, want %#v", got.Metadata, want)
	}

	list, err := svc.ListVectors(ctx, &ListVectorsRequest{Namespace: "typed", IncludeMetadata: true})
	if err != nil {
		t.Fatalf("ListVectors: %v", err)
	}
	if len(list.Vectors) != 1 || !reflect.DeepEqual(list.Vectors[0].Metadata, want) {
		t.Fatalf("list = %#v, want one vector with %#v", list.Vectors, want)
	}

	if _, err := svc.UpdateMetadata(ctx, &UpdateVectorMetadataRequest{
		Namespace: "typed", ID: 1, MergeMetadata: true, Metadata: map[string]interface{}{"n": 1},
	}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	merged, err := svc.GetVector(ctx, &GetVectorRequest{Namespace: "typed", ID: 1, IncludeMetadata: true})
	if err != nil {
		t.Fatalf("GetVector after merge: %v", err)
	}
	wantMerged := map[string]interface{}{"k": "\x01true", "s": "plain", "n": float64(1)}
	if !reflect.DeepEqual(merged.Metadata, wantMerged) {
		t.Fatalf("merged metadata = %#v, want %#v", merged.Metadata, wantMerged)
	}
}

// TestUpsertVector_ConcurrentCreateReportsOnce proves concurrent upserts of
// one missing ID report created exactly once.
func TestUpsertVector_ConcurrentCreateReportsOnce(t *testing.T) {
	svc := newTypedMetadataService(t)
	ctx := context.Background()
	const writers = 16
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		errs    []error
	)
	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, err := svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "typed", ID: 9, Vector: []float32{0, 1}})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if resp.Created {
				created++
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(errs) != 0 {
		t.Fatalf("UpsertVector errors: %v", errs)
	}
	if created != 1 {
		t.Fatalf("created reported %d times, want exactly 1", created)
	}
}

// TestUpsertVector_RaceWithDeleteReportsStoredState proves an upsert racing a
// delete reports created consistently with the order the two writes took:
// the upsert created the vector exactly when it ran after the delete.
func TestUpsertVector_RaceWithDeleteReportsStoredState(t *testing.T) {
	svc := newTypedMetadataService(t)
	ctx := context.Background()
	for round := 0; round < 32; round++ {
		if _, err := svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "typed", ID: 11, Vector: []float32{0, 1}}); err != nil {
			t.Fatalf("round %d seed UpsertVector: %v", round, err)
		}
		var (
			wg        sync.WaitGroup
			resp      *UpsertVectorResponse
			upErr     error
			deleteErr error
		)
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			resp, upErr = svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: "typed", ID: 11, Vector: []float32{1, 0}})
		}()
		go func() {
			defer wg.Done()
			<-start
			_, deleteErr = svc.DeleteVector(ctx, &DeleteVectorRequest{Namespace: "typed", ID: 11})
		}()
		close(start)
		wg.Wait()
		if upErr != nil {
			t.Fatalf("round %d UpsertVector: %v", round, upErr)
		}
		if deleteErr != nil {
			t.Fatalf("round %d DeleteVector: %v", round, deleteErr)
		}
		_, getErr := svc.GetVector(ctx, &GetVectorRequest{Namespace: "typed", ID: 11})
		stored := getErr == nil
		// Upsert last: the vector is stored and the upsert created it after
		// the delete. Delete last: the vector is gone and the upsert replaced
		// the seeded one.
		if resp.Created != stored {
			t.Fatalf("round %d: created = %v, stored after both writes = %v (get err %v)",
				round, resp.Created, stored, getErr)
		}
	}
}
