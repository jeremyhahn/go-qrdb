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
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	vectorstorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// sealableHost is the binding store's host as a sealed barrier leaves it:
// while sealed every engine read is refused with the barrier's
// *database.SealedError, as a barrier-wrapped binding backend refuses it.
type sealableHost struct {
	service.Host
	sealed atomic.Bool
}

func (h *sealableHost) SyncRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
	if h.sealed.Load() {
		return nil, &database.SealedError{}
	}
	return h.Host.SyncRead(ctx, shardID, query)
}

// newSealableVectorService builds a vector service over an apply host the
// test can seal, with namespace "ns" holding vectors 1 and 2, and warms the
// in-memory index with one search.
func newSealableVectorService(t *testing.T) (service.VectorService, *sealableHost) {
	t.Helper()
	ctx := context.Background()
	eng, err := engine.NewMVCCEngine(memory.NewStorage())
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := &sealableHost{Host: backend.NewLocalBackend(eng)}
	svc, err := service.NewVectorService(nil, hostVecLogger(t), service.WithVectorApplyHost(host, shard.ShardKV))
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	t.Cleanup(func() { closeVectorService(svc) })
	if _, err := svc.CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "COSINE"}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	for id, v := range map[uint64][]float32{1: {1, 0}, 2: {0, 1}} {
		if _, err := svc.UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: id, Vector: v}); err != nil {
			t.Fatalf("UpsertVector(%d): %v", id, err)
		}
	}
	searchAnswers(t, svc)
	return svc, host
}

func searchNS(svc service.VectorService) (*service.SearchSimilarResponse, error) {
	return svc.SearchSimilar(context.Background(), &service.SearchSimilarRequest{Namespace: "ns", QueryVector: []float32{1, 0}, K: 2})
}

func searchAnswers(t *testing.T, svc service.VectorService) {
	t.Helper()
	got, err := searchNS(svc)
	if err != nil {
		t.Fatalf("SearchSimilar: %v", err)
	}
	if len(got.Results) == 0 || got.Results[0].ID != 1 {
		t.Fatalf("SearchSimilar = %+v, want id 1 first", got.Results)
	}
}

// A search the warm in-memory index could answer is refused once the store
// under it is sealed, and answers again after the unseal.
func TestVectorService_ApplyHostCachedSearchRefusedWhenStoreSealed(t *testing.T) {
	svc, host := newSealableVectorService(t)
	host.sealed.Store(true)

	got, err := searchNS(svc)
	var sealed *service.BarrierSealedError
	if !errors.As(err, &sealed) {
		t.Fatalf("SearchSimilar on a sealed store = %+v, %v; want *BarrierSealedError", got, err)
	}
	if got != nil {
		t.Fatalf("a sealed store returned results %+v", got.Results)
	}

	host.sealed.Store(false)
	searchAnswers(t, svc)
}

// Index stats, which read the cached index, are refused on a sealed store.
func TestVectorService_ApplyHostCachedIndexStatsRefusedWhenStoreSealed(t *testing.T) {
	svc, host := newSealableVectorService(t)
	host.sealed.Store(true)

	_, err := svc.GetIndexStats(context.Background(), &service.GetIndexStatsRequest{Namespace: "ns"})
	var sealed *service.BarrierSealedError
	if !errors.As(err, &sealed) {
		t.Fatalf("GetIndexStats on a sealed store = %v, want *BarrierSealedError", err)
	}
}

// A dropped namespace behind a warm index answers no results, not the
// cached vectors.
func TestVectorService_ApplyHostCachedSearchSeesDroppedNamespace(t *testing.T) {
	svc, _ := newSealableVectorService(t)
	if _, err := svc.DeleteNamespace(context.Background(), &service.DeleteNamespaceRequest{Namespace: "ns"}); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	got, err := searchNS(svc)
	if err == nil && len(got.Results) != 0 {
		t.Fatalf("SearchSimilar after DeleteNamespace = %+v, want no results", got.Results)
	}
}

// A service over its own vector storage (no apply host) keeps serving the
// cached index as before.
func TestVectorService_NoApplyHostCachedSearchUnchanged(t *testing.T) {
	ctx := context.Background()
	svc, err := service.NewVectorService(vectorstorage.NewMemoryVectorStorage(), hostVecLogger(t))
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	t.Cleanup(func() { closeVectorService(svc) })
	if _, err := svc.CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "COSINE"}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	for id, v := range map[uint64][]float32{1: {1, 0}, 2: {0, 1}} {
		if _, err := svc.UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: id, Vector: v}); err != nil {
			t.Fatalf("UpsertVector(%d): %v", id, err)
		}
	}
	searchAnswers(t, svc)
	searchAnswers(t, svc)
}
