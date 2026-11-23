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

//go:build qrdb_vector

package service

import (
	"context"
	"testing"
)

// TestVectorService_BatchDeleteVectors_RemovesFromLiveIndex verifies that a
// successful BatchDeleteVectors removes every deleted ID from the live HNSW
// index, so SearchSimilar no longer returns it (Codex P1 on #1608). It needs
// the real HNSW index, hence the qrdb_vector build tag.
func TestVectorService_BatchDeleteVectors_RemovesFromLiveIndex(t *testing.T) {
	const ns = "bdv-index-ns"
	svc, _ := newFullVecSvc(t, ns, 3)
	ctx := context.Background()

	embeddings := map[uint64][]float32{
		1: {1, 0, 0},
		2: {0, 1, 0},
		3: {0, 0, 1},
	}
	for id, emb := range embeddings {
		if _, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
			Namespace: ns,
			ID:        id,
			Vector:    emb,
		}); err != nil {
			t.Fatalf("UpsertVector %d: %v", id, err)
		}
	}

	search := func() map[uint64]bool {
		t.Helper()
		resp, err := svc.SearchSimilar(ctx, &SearchSimilarRequest{
			Namespace:   ns,
			QueryVector: []float32{0.5, 0.5, 0.5},
			K:           10,
			EfSearch:    50,
		})
		if err != nil {
			t.Fatalf("SearchSimilar: %v", err)
		}
		got := make(map[uint64]bool, len(resp.Results))
		for _, r := range resp.Results {
			got[r.ID] = true
		}
		return got
	}

	before := search()
	for id := range embeddings {
		if !before[id] {
			t.Fatalf("before delete: ID %d missing from search results %v", id, before)
		}
	}

	resp, err := svc.BatchDeleteVectors(ctx, &BatchDeleteVectorsRequest{
		Namespace: ns,
		IDs:       []uint64{1, 2},
	})
	if err != nil {
		t.Fatalf("BatchDeleteVectors: %v", err)
	}
	if !resp.Success || resp.DeletedCount != 2 {
		t.Fatalf("BatchDeleteVectors response = %+v, want success with 2 deleted", resp)
	}

	after := search()
	for _, id := range []uint64{1, 2} {
		if after[id] {
			t.Errorf("after delete: deleted ID %d still in search results %v", id, after)
		}
	}
	if !after[3] {
		t.Errorf("after delete: kept ID 3 missing from search results %v", after)
	}
}

// TestVectorService_BatchDeleteVectors_ConcurrentUpsertKeepsIndexEntry
// verifies that an upsert of an ID racing a batch delete of it is applied to
// the live HNSW index after the delete's index removal, in the order storage
// took the writes: storage keeps the upserted vector, so SearchSimilar must
// return it (Codex round 1 P1 on #1606).
func TestVectorService_BatchDeleteVectors_ConcurrentUpsertKeepsIndexEntry(t *testing.T) {
	const ns = "bdv-race-ns"
	svc, store := newFullVecSvc(t, ns, 3)
	ctx := context.Background()

	upsert := func() error {
		_, err := svc.UpsertVector(ctx, &UpsertVectorRequest{Namespace: ns, ID: 1, Vector: []float32{1, 0, 0}})
		return err
	}
	if err := upsert(); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	store.batchDeleteVectorsFn = func(context.Context, string, []uint64) error {
		close(entered)
		<-release
		return nil
	}

	deleteDone := make(chan error, 1)
	go func() {
		_, err := svc.BatchDeleteVectors(ctx, &BatchDeleteVectorsRequest{Namespace: ns, IDs: []uint64{1}})
		deleteDone <- err
	}()
	<-entered

	started := make(chan struct{})
	upsertDone := make(chan error, 1)
	go func() {
		close(started)
		upsertDone <- upsert()
	}()
	<-started
	close(release)

	if err := <-deleteDone; err != nil {
		t.Fatalf("BatchDeleteVectors: %v", err)
	}
	if err := <-upsertDone; err != nil {
		t.Fatalf("concurrent UpsertVector: %v", err)
	}

	resp, err := svc.SearchSimilar(ctx, &SearchSimilarRequest{
		Namespace:   ns,
		QueryVector: []float32{1, 0, 0},
		K:           10,
		EfSearch:    50,
	})
	if err != nil {
		t.Fatalf("SearchSimilar: %v", err)
	}
	for _, r := range resp.Results {
		if r.ID == 1 {
			return
		}
	}
	t.Fatalf("ID 1 upserted after the batch delete is missing from search results %+v", resp.Results)
}
