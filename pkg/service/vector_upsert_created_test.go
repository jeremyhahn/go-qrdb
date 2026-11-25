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
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

func newUpsertTestService(t *testing.T) VectorService {
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

// TestSearchByMetadata_RefusesNegativeOffset proves a negative offset is an
// invalid argument, refused before the scan.
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
	svc := newUpsertTestService(t)
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

// TestUpsertVector_ConcurrentCreateReportsOnce proves concurrent upserts of
// one missing ID report created exactly once.
func TestUpsertVector_ConcurrentCreateReportsOnce(t *testing.T) {
	svc := newUpsertTestService(t)
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
	svc := newUpsertTestService(t)
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

// TestHybridSearch_NumericFilterMatchesStringMetadata proves a numeric filter
// value matches a record that stores the same value as a string.
func TestHybridSearch_NumericFilterMatchesStringMetadata(t *testing.T) {
	svc := newUpsertTestService(t)
	ctx := context.Background()
	if _, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "typed", ID: 1, Vector: []float32{1, 0}, Metadata: map[string]interface{}{"price": "5"},
	}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	resp, err := svc.HybridSearch(ctx, &HybridSearchRequest{
		Namespace: "typed", QueryVector: []float32{1, 0}, K: 5,
		MetadataFilter: map[string]interface{}{"price": 5}, IncludeMetadata: true,
	})
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].ID != 1 {
		t.Fatalf("HybridSearch(price = 5) = %+v; want ID 1", resp.Results)
	}
}
