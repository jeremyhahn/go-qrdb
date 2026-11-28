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

package service_test

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// seedHostNamespace creates namespace "ns" with two vectors through svc.
func seedHostNamespace(t *testing.T, svc service.VectorService) {
	t.Helper()
	ctx := context.Background()
	if _, err := svc.CreateNamespace(ctx, &service.CreateNamespaceRequest{
		Namespace: "ns", Dimension: 2, DistanceMetric: "COSINE",
		IndexConfig: map[string]string{"m": "16"},
	}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	for id, v := range map[uint64][]float32{1: {1, 0}, 2: {0, 1}} {
		if _, err := svc.UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: id, Vector: v}); err != nil {
			t.Fatalf("UpsertVector(%d): %v", id, err)
		}
	}
}

func searchNearest(t *testing.T, svc service.VectorService) (*service.SearchSimilarResponse, error) {
	t.Helper()
	return svc.SearchSimilar(context.Background(), &service.SearchSimilarRequest{Namespace: "ns", QueryVector: []float32{1, 0}, K: 1})
}

// TestHostVectorRebuild_RebuildsTheIndexSearchesRead opens a second engine
// over a store that already holds a namespace, without warming its index, so
// its searches have no index to read. A rebuild through the host must build
// the engine index the search path reads; rebuilding only a service-local
// index would leave the search failing.
func TestHostVectorRebuild_RebuildsTheIndexSearchesRead(t *testing.T) {
	stg := memory.NewStorage()
	seedHostNamespace(t, newHostVectorService(t, stg))

	cold, err := engine.NewMVCCEngine(stg, engine.WithMVCCVectorIndex("", nil))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	svc, err := service.NewVectorService(nil, hostVecLogger(t),
		service.WithVectorApplyHost(backend.NewLocalBackend(cold), shard.ShardKV))
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	t.Cleanup(func() { closeVectorService(svc) })

	if _, err := searchNearest(t, svc); err == nil {
		t.Fatal("search on an unbuilt engine index succeeded; the test needs a cold index")
	}

	resp, err := svc.RebuildIndex(context.Background(), &service.RebuildIndexRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	if !resp.Success || resp.Status != "completed" {
		t.Fatalf("RebuildIndex = %+v, want a completed rebuild", resp)
	}

	sim, err := searchNearest(t, svc)
	if err != nil {
		t.Fatalf("SearchSimilar after rebuild: %v", err)
	}
	if len(sim.Results) != 1 || sim.Results[0].ID != 1 {
		t.Fatalf("SearchSimilar after rebuild = %+v, want id 1", sim.Results)
	}
}

// TestHostVectorRebuild_NewIndexConfigIsRecorded checks a rebuild with
// changed parameters records them on the namespace, which every replica's
// engine index is rebuilt under, and searches still answer afterwards.
func TestHostVectorRebuild_NewIndexConfigIsRecorded(t *testing.T) {
	ctx := context.Background()
	svc := newHostVectorService(t, memory.NewStorage())
	seedHostNamespace(t, svc)

	if _, err := svc.RebuildIndex(ctx, &service.RebuildIndexRequest{
		Namespace: "ns", IndexConfig: map[string]string{"ef_search": "32"},
	}); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	info, err := svc.GetNamespaceInfo(ctx, &service.GetNamespaceInfoRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("GetNamespaceInfo: %v", err)
	}
	if info.IndexConfig["ef_search"] != "32" || info.IndexConfig["m"] != "16" {
		t.Fatalf("IndexConfig after rebuild = %v, want ef_search=32 and m=16", info.IndexConfig)
	}
	sim, err := searchNearest(t, svc)
	if err != nil {
		t.Fatalf("SearchSimilar after rebuild: %v", err)
	}
	if len(sim.Results) != 1 || sim.Results[0].ID != 1 {
		t.Fatalf("SearchSimilar after rebuild = %+v, want id 1", sim.Results)
	}
}
