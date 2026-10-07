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

// vector_restart_test.go pins the restart and snapshot-install acceptance
// criteria for Lane 1B (docs/vectors/engineering.md, "Target: acceptance
// tests (lane 1B)", items 1 and 2): vectors stay searchable after a restart,
// and the namespace's own configured distance metric -- not a hardcoded one
// -- is what ranks the results. Both tests drive the real PebbleDB-backed
// vector.Storage, not a mock, because the defect they pin (loadNamespaces
// pre-registering a blank L2 index that blocks ensureHNSWIndex's rebuild
// branch forever) only reproduces against a store that genuinely persists
// across a close/reopen.
package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	vectorstorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// newRestartVectorStorage opens a PebbleDB-backed vector store at dir.
func newRestartVectorStorage(t *testing.T, dir string) *vectorstorage.PebbleVectorStorage {
	t.Helper()
	cfg := vectorstorage.DefaultConfig(dir)
	store, err := vectorstorage.NewPebbleVectorStorage(cfg, newVecTestLogger())
	if err != nil {
		t.Fatalf("NewPebbleVectorStorage(%q): %v", dir, err)
	}
	return store
}

// seedCosineNamespace creates namespace "ns" with the COSINE metric and
// upserts two vectors chosen so that L2 and cosine distance DISAGREE on
// which one is nearer to the query [1, 0]:
//
//   - id 1 = [100, 0]: identical direction to the query (cosine distance 0)
//     but far away in L2 (distance 99).
//   - id 2 = [0, 1]:   orthogonal to the query (cosine distance maximal) but
//     close in L2 (distance sqrt(2)).
//
// Under the namespace's configured COSINE metric, id 1 must rank first.
// Under the hardcoded L2 metric the defect falls back to, id 2 would rank
// first instead -- so a test asserting the id-1-first ranking fails loudly,
// rather than vacuously, if the metric is ever discarded again.
func seedCosineNamespace(t *testing.T, ctx context.Context, svc VectorService) {
	t.Helper()

	if _, err := svc.CreateNamespace(ctx, &CreateNamespaceRequest{
		Namespace:      "ns",
		Dimension:      2,
		DistanceMetric: "COSINE",
	}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}

	if _, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "ns",
		ID:        1,
		Vector:    []float32{100, 0},
	}); err != nil {
		t.Fatalf("UpsertVector(1): %v", err)
	}
	if _, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "ns",
		ID:        2,
		Vector:    []float32{0, 1},
	}); err != nil {
		t.Fatalf("UpsertVector(2): %v", err)
	}
}

// assertCosineRanking searches "ns" for the query [1, 0] and asserts both
// vectors come back with id 1 (the cosine-nearest one) ranked first. This is
// the one assertion that distinguishes "the index exists and has data" from
// "the index uses the namespace's own metric".
func assertCosineRanking(t *testing.T, ctx context.Context, svc VectorService) {
	t.Helper()

	resp, err := svc.SearchSimilar(ctx, &SearchSimilarRequest{
		Namespace:   "ns",
		QueryVector: []float32{1, 0},
		K:           2,
	})
	if err != nil {
		t.Fatalf("SearchSimilar: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d (%+v)", len(resp.Results), resp.Results)
	}
	if resp.Results[0].ID != 1 {
		t.Fatalf("expected id 1 (cosine-nearest to [1,0]) ranked first, got id %d first -- "+
			"the index is not using the namespace's COSINE metric", resp.Results[0].ID)
	}
	if resp.Results[1].ID != 2 {
		t.Fatalf("expected id 2 ranked second, got id %d", resp.Results[1].ID)
	}
}

// TestVectorService_RestartPreservesConfiguredMetric is acceptance test 1
// and 2 from docs/vectors/engineering.md's lane-1B list: upsert, restart,
// search returns the same top-k, ranked by the namespace's own metric.
//
// Before the fix, loadNamespaces pre-registered a blank hnsw.L2Distance
// index for "ns" the moment the second service opened, which permanently
// satisfied ensureHNSWIndex's "already exists" check -- so the rebuild
// branch that reads the namespace's real COSINE metric from storage never
// ran, and the reopened index additionally held no vectors at all.
func TestVectorService_RestartPreservesConfiguredMetric(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store1 := newRestartVectorStorage(t, dir)
	svc1, err := NewVectorService(store1, newVecTestLogger())
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	seedCosineNamespace(t, ctx, svc1)

	// Confirm the metric already works pre-restart, so a later failure can
	// only be attributed to the restart path, not to namespace creation.
	assertCosineRanking(t, ctx, svc1)

	if err := store1.Close(); err != nil {
		t.Fatalf("store1.Close: %v", err)
	}

	// "Restart": reopen the same on-disk store and construct a brand-new
	// service over it, exactly as a process restart does in
	// pkg/server/lifecycle.
	store2 := newRestartVectorStorage(t, dir)
	defer func() { _ = store2.Close() }()

	svc2, err := NewVectorService(store2, newVecTestLogger())
	if err != nil {
		t.Fatalf("NewVectorService after restart: %v", err)
	}

	assertCosineRanking(t, ctx, svc2)
}

// TestVectorService_SnapshotInstallPreservesConfiguredMetric is acceptance
// test 4 from the same list, adapted to this service's node-local store: a
// replica that never ran CreateNamespace/UpsertVector itself, but receives
// the fully-populated store -- here, a filesystem copy of the Pebble
// directory, standing in for whatever out-of-band mechanism installs a
// snapshot onto a new or catching-up node -- must serve the same ranked
// results the originating node would, without a caller-triggered rebuild.
//
// This exercises the identical loadNamespaces code path as the restart test
// above (construction time, reading whatever the store already holds), so
// the same defect -- and the same fix -- covers both.
func TestVectorService_SnapshotInstallPreservesConfiguredMetric(t *testing.T) {
	srcDir := t.TempDir()
	ctx := context.Background()

	srcStore := newRestartVectorStorage(t, srcDir)
	srcSvc, err := NewVectorService(srcStore, newVecTestLogger())
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	seedCosineNamespace(t, ctx, srcSvc)
	assertCosineRanking(t, ctx, srcSvc)

	if err := srcStore.Close(); err != nil {
		t.Fatalf("srcStore.Close: %v", err)
	}

	// "Snapshot install": materialize the fully-populated store under a new
	// directory before any service ever opens it there, exactly as a
	// replica that joins after the data already exists would receive it.
	dstDir := filepath.Join(t.TempDir(), "installed")
	if err := os.CopyFS(dstDir, os.DirFS(srcDir)); err != nil {
		t.Fatalf("copy snapshot directory: %v", err)
	}

	dstStore := newRestartVectorStorage(t, dstDir)
	defer func() { _ = dstStore.Close() }()

	dstSvc, err := NewVectorService(dstStore, newVecTestLogger())
	if err != nil {
		t.Fatalf("NewVectorService over installed snapshot: %v", err)
	}

	assertCosineRanking(t, ctx, dstSvc)
}
