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
	"io"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	vectorstorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/vector"
)

func hostVecLogger(t *testing.T) interfaces.Logger {
	t.Helper()
	log, err := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stderr"})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return logger.NewAdapter(log)
}

func closeVectorService(svc service.VectorService) {
	if c, ok := svc.(io.Closer); ok {
		_ = c.Close()
	}
}

// newHostVectorService builds an MVCC engine over stg and a vector service
// whose storage is that engine, written through a LocalBackend host.
func newHostVectorService(t *testing.T, stg *memory.MemoryStorage) service.VectorService {
	t.Helper()
	eng, err := engine.NewMVCCEngine(stg, engine.WithMVCCVectorIndex("", nil))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	if err := eng.WarmVectorIndex(); err != nil {
		t.Fatalf("WarmVectorIndex: %v", err)
	}
	svc, err := service.NewVectorService(nil, hostVecLogger(t),
		service.WithVectorApplyHost(backend.NewLocalBackend(eng), shard.ShardKV))
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	t.Cleanup(func() { closeVectorService(svc) })
	return svc
}

func TestHostVectorStorage_RoundTripsThroughTheHost(t *testing.T) {
	ctx := context.Background()
	stg := memory.NewStorage()
	svc := newHostVectorService(t, stg)

	if _, err := svc.CreateNamespace(ctx, &service.CreateNamespaceRequest{
		Namespace: "ns", Dimension: 2, DistanceMetric: "COSINE",
		IndexConfig: map[string]string{"m": "16"},
		Metadata:    map[string]interface{}{"owner": "team"},
	}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	for id, v := range map[uint64][]float32{1: {1, 0}, 2: {0, 1}} {
		if _, err := svc.UpsertVector(ctx, &service.UpsertVectorRequest{
			Namespace: "ns", ID: id, Vector: v, Metadata: map[string]interface{}{"n": map[uint64]string{1: "one", 2: "two"}[id]},
		}); err != nil {
			t.Fatalf("UpsertVector(%d): %v", id, err)
		}
	}

	info, err := svc.GetNamespaceInfo(ctx, &service.GetNamespaceInfoRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("GetNamespaceInfo: %v", err)
	}
	if info.Namespace != "ns" || info.DistanceMetric != "COSINE" || info.Dimension != 2 || info.VectorCount != 2 ||
		info.IndexConfig["m"] != "16" || info.Metadata["owner"] != "team" {
		t.Fatalf("GetNamespaceInfo = %+v, want ns/COSINE/2 dims/2 vectors, m=16, owner=team", info)
	}

	got, err := svc.GetVector(ctx, &service.GetVectorRequest{Namespace: "ns", ID: 2, IncludeVector: true, IncludeMetadata: true})
	if err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if got.ID != 2 || len(got.Vector) != 2 || got.Vector[1] != 1 || got.Metadata["n"] != "two" {
		t.Fatalf("GetVector = %+v, want vector 2", got)
	}

	sim, err := svc.SearchSimilar(ctx, &service.SearchSimilarRequest{Namespace: "ns", QueryVector: []float32{1, 0}, K: 1})
	if err != nil {
		t.Fatalf("SearchSimilar: %v", err)
	}
	if len(sim.Results) != 1 || sim.Results[0].ID != 1 {
		t.Fatalf("SearchSimilar = %+v, want id 1", sim.Results)
	}

	byMeta, err := svc.SearchByMetadata(ctx, &service.SearchByMetadataRequest{Namespace: "ns", Filters: map[string]interface{}{"n": "two"}})
	if err != nil {
		t.Fatalf("SearchByMetadata: %v", err)
	}
	if len(byMeta.Results) != 1 || byMeta.Results[0].ID != 2 {
		t.Fatalf("SearchByMetadata = %+v, want id 2", byMeta.Results)
	}

	if _, err := svc.UpdateNamespace(ctx, &service.UpdateNamespaceRequest{
		Namespace: "ns", IndexConfig: map[string]string{"ef_search": "64"}, Metadata: map[string]interface{}{"owner": "ops"},
	}); err != nil {
		t.Fatalf("UpdateNamespace: %v", err)
	}
	info, err = svc.GetNamespaceInfo(ctx, &service.GetNamespaceInfoRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("GetNamespaceInfo after update: %v", err)
	}
	if info.DistanceMetric != "COSINE" || info.IndexConfig["ef_search"] != "64" || info.Metadata["owner"] != "ops" || info.VectorCount != 2 {
		t.Fatalf("GetNamespaceInfo after update = %+v", info)
	}

	if _, err := svc.DeleteVector(ctx, &service.DeleteVectorRequest{Namespace: "ns", ID: 1}); err != nil {
		t.Fatalf("DeleteVector: %v", err)
	}
	if _, err := svc.GetVector(ctx, &service.GetVectorRequest{Namespace: "ns", ID: 1}); err == nil {
		t.Fatal("GetVector after DeleteVector found the vector")
	}
	if data, _ := stg.Get(vector.VectorKey(service.HostVectorNamespace("", "ns"), 1)); data != nil {
		t.Fatal("DeleteVector left the vector key in the engine store")
	}

	if _, err := svc.DeleteNamespace(ctx, &service.DeleteNamespaceRequest{Namespace: "ns"}); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	if _, err := svc.GetNamespaceInfo(ctx, &service.GetNamespaceInfoRequest{Namespace: "ns"}); err == nil {
		t.Fatal("GetNamespaceInfo after DeleteNamespace found the namespace")
	}
	if data, _ := stg.Get(vector.NamespaceKey(service.HostVectorNamespace("", "ns"))); data != nil {
		t.Fatal("DeleteNamespace left the namespace record in the engine store")
	}
}

func TestHostVectorStorage_DimensionMismatchWritesNothing(t *testing.T) {
	ctx := context.Background()
	stg := memory.NewStorage()
	svc := newHostVectorService(t, stg)
	if _, err := svc.CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if _, err := svc.UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: 5, Vector: []float32{1, 2, 3}}); err == nil {
		t.Fatal("a dimension-mismatch upsert was accepted")
	}
	if data, _ := stg.Get(vector.VectorKey(service.HostVectorNamespace("", "ns"), 5)); data != nil {
		t.Fatal("a refused upsert left its vector key in the engine store")
	}
}

// vectorGateHost is a real local host whose group has not activated
// featuregate.VectorEngine; it counts every proposal that reaches it.
type vectorGateHost struct {
	service.Host
	enabled   bool
	proposals int
}

func (h *vectorGateHost) FeatureEnabled(_ uint64, f feature.Feature) bool {
	return f != featuregate.VectorEngine || h.enabled
}

func (h *vectorGateHost) SyncPropose(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error) {
	h.proposals++
	return h.Host.SyncPropose(ctx, shardID, cmd)
}

// TestHostVectorStorage_InactiveVectorEngineGateProposesNothing: until the
// group activates featuregate.VectorEngine a vector host write is refused
// with *feature.NotActiveError and no OpVector* command reaches Raft.
func TestHostVectorStorage_InactiveVectorEngineGateProposesNothing(t *testing.T) {
	ctx := context.Background()
	eng, err := engine.NewMVCCEngine(memory.NewStorage())
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := &vectorGateHost{Host: backend.NewLocalBackend(eng)}
	stg := service.NewHostVectorStorage(host, shard.ShardKV)
	err = stg.CreateNamespace(ctx, &vectorstorage.NamespaceInfo{Name: "ns", Dimension: 2})
	var notActive *feature.NotActiveError
	if !errors.As(err, &notActive) || notActive.Feature != featuregate.VectorEngine {
		t.Fatalf("CreateNamespace with the gate inactive: err = %v, want *feature.NotActiveError for %s", err, featuregate.VectorEngine)
	}
	if err := stg.UpsertVector(ctx, "ns", &vectorstorage.VectorValue{ID: 1, Embedding: []float32{1, 2}}); !errors.As(err, &notActive) {
		t.Fatalf("UpsertVector with the gate inactive: err = %v, want *feature.NotActiveError", err)
	}
	if host.proposals != 0 {
		t.Fatalf("%d proposals reached the host with the vector-engine gate inactive", host.proposals)
	}
	host.enabled = true
	if err := stg.CreateNamespace(ctx, &vectorstorage.NamespaceInfo{Name: "ns", Dimension: 2}); err != nil {
		t.Fatalf("CreateNamespace with the gate active: %v", err)
	}
	if host.proposals == 0 {
		t.Fatal("an active gate sent no proposal")
	}
}

func TestHostVectorStorage_RestartFindsTheNamespace(t *testing.T) {
	ctx := context.Background()
	stg := memory.NewStorage()
	first := newHostVectorService(t, stg)
	if _, err := first.CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "COSINE"}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if _, err := first.UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: 3, Vector: []float32{0, 1}}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	closeVectorService(first)

	second := newHostVectorService(t, stg)
	resp, err := second.SearchSimilar(ctx, &service.SearchSimilarRequest{Namespace: "ns", QueryVector: []float32{0, 1}, K: 1})
	if err != nil {
		t.Fatalf("SearchSimilar after restart: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].ID != 3 {
		t.Fatalf("SearchSimilar after restart = %+v, want id 3", resp.Results)
	}
}

func TestHostVectorStorage_StorageAndApplyHostConflict(t *testing.T) {
	eng, err := engine.NewMVCCEngine(memory.NewStorage())
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	_, err = service.NewVectorService(vectorstorage.NewMemoryVectorStorage(), hostVecLogger(t),
		service.WithVectorApplyHost(backend.NewLocalBackend(eng), shard.ShardKV))
	var conflict *service.VectorStorageConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("NewVectorService(storage + apply host) = %v, want *VectorStorageConflictError", err)
	}
}

func TestHostVectorStorage_ReservedIndexConfigKeyIsRefused(t *testing.T) {
	svc := newHostVectorService(t, memory.NewStorage())
	if _, err := svc.CreateNamespace(context.Background(), &service.CreateNamespaceRequest{
		Namespace: "ns", Dimension: 2, IndexConfig: map[string]string{"qrdb.x": "1"},
	}); err == nil {
		t.Fatal("an IndexConfig key with the reserved qrdb. prefix was accepted")
	}
}

// newTwoHostVectorServices returns two vector services proposing to one
// apply host, as two daemons' services share one replicated group: neither
// one's writeMu orders the other's writes.
func newTwoHostVectorServices(t *testing.T) (service.VectorService, service.VectorService) {
	t.Helper()
	eng, err := engine.NewMVCCEngine(memory.NewStorage(), engine.WithMVCCVectorIndex("", nil))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	if err := eng.WarmVectorIndex(); err != nil {
		t.Fatalf("WarmVectorIndex: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	open := func() service.VectorService {
		svc, err := service.NewVectorService(nil, hostVecLogger(t), service.WithVectorApplyHost(host, shard.ShardKV))
		if err != nil {
			t.Fatalf("NewVectorService: %v", err)
		}
		t.Cleanup(func() { closeVectorService(svc) })
		return svc
	}
	return open(), open()
}

func TestHostVectorStorage_MetadataUpdateKeepsAConcurrentEmbedding(t *testing.T) {
	ctx := context.Background()
	a, b := newTwoHostVectorServices(t)
	if _, err := a.CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if _, err := a.UpsertVector(ctx, &service.UpsertVectorRequest{
		Namespace: "ns", ID: 1, Vector: []float32{1, 0}, Metadata: map[string]interface{}{"k": "x", "gone": "y"},
	}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}

	// A reads the vector, B commits a new embedding, then A updates the
	// metadata: the update must not carry A's stale embedding back.
	if _, err := a.GetVector(ctx, &service.GetVectorRequest{Namespace: "ns", ID: 1, IncludeVector: true}); err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if _, err := b.UpsertVector(ctx, &service.UpsertVectorRequest{
		Namespace: "ns", ID: 1, Vector: []float32{0, 1}, Metadata: map[string]interface{}{"k": "x", "gone": "y", "b": "1"},
	}); err != nil {
		t.Fatalf("UpsertVector(B): %v", err)
	}
	if _, err := a.UpdateMetadata(ctx, &service.UpdateVectorMetadataRequest{
		Namespace: "ns", ID: 1, Metadata: map[string]interface{}{"k": "z"}, MergeMetadata: true, DeleteKeys: []string{"gone"},
	}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}

	got, err := b.GetVector(ctx, &service.GetVectorRequest{Namespace: "ns", ID: 1, IncludeVector: true, IncludeMetadata: true})
	if err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if len(got.Vector) != 2 || got.Vector[0] != 0 || got.Vector[1] != 1 {
		t.Fatalf("embedding = %v, want B's [0 1]", got.Vector)
	}
	if got.Metadata["k"] != "z" || got.Metadata["b"] != "1" || got.Metadata["gone"] != nil {
		t.Fatalf("metadata = %v, want k=z, b=1 kept, gone deleted", got.Metadata)
	}
}

func TestHostVectorStorage_MetadataUpdateDoesNotUndoAConcurrentDelete(t *testing.T) {
	ctx := context.Background()
	a, b := newTwoHostVectorServices(t)
	if _, err := a.CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if _, err := a.UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: 1, Vector: []float32{1, 0}}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	if _, err := a.GetVector(ctx, &service.GetVectorRequest{Namespace: "ns", ID: 1}); err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if _, err := b.DeleteVector(ctx, &service.DeleteVectorRequest{Namespace: "ns", ID: 1}); err != nil {
		t.Fatalf("DeleteVector(B): %v", err)
	}
	if _, err := a.UpdateMetadata(ctx, &service.UpdateVectorMetadataRequest{
		Namespace: "ns", ID: 1, Metadata: map[string]interface{}{"k": "v"},
	}); err == nil {
		t.Fatal("UpdateMetadata of a deleted vector succeeded")
	}
	if _, err := b.GetVector(ctx, &service.GetVectorRequest{Namespace: "ns", ID: 1}); err == nil {
		t.Fatal("UpdateMetadata re-created a deleted vector")
	}
}

func TestHostVectorStorage_ConcurrentNamespacePatchesBothSurvive(t *testing.T) {
	ctx := context.Background()
	a, b := newTwoHostVectorServices(t)
	if _, err := a.CreateNamespace(ctx, &service.CreateNamespaceRequest{
		Namespace: "ns", Dimension: 2, IndexConfig: map[string]string{"m": "16"},
		Metadata: map[string]interface{}{"owner": "team"},
	}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	// Both instances read the same record before either patch lands.
	for _, svc := range []service.VectorService{a, b} {
		if _, err := svc.GetNamespaceInfo(ctx, &service.GetNamespaceInfoRequest{Namespace: "ns"}); err != nil {
			t.Fatalf("GetNamespaceInfo: %v", err)
		}
	}
	if _, err := a.UpdateNamespace(ctx, &service.UpdateNamespaceRequest{
		Namespace: "ns", IndexConfig: map[string]string{"ef_search": "64"}, Metadata: map[string]interface{}{"a": "1"},
	}); err != nil {
		t.Fatalf("UpdateNamespace(A): %v", err)
	}
	if _, err := b.UpdateNamespace(ctx, &service.UpdateNamespaceRequest{
		Namespace: "ns", IndexConfig: map[string]string{"ef_construction": "100"}, Metadata: map[string]interface{}{"b": "2"},
	}); err != nil {
		t.Fatalf("UpdateNamespace(B): %v", err)
	}

	info, err := a.GetNamespaceInfo(ctx, &service.GetNamespaceInfoRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("GetNamespaceInfo: %v", err)
	}
	if info.IndexConfig["m"] != "16" || info.IndexConfig["ef_search"] != "64" || info.IndexConfig["ef_construction"] != "100" {
		t.Fatalf("IndexConfig = %v, want m=16 ef_search=64 ef_construction=100", info.IndexConfig)
	}
	if info.Metadata["owner"] != "team" || info.Metadata["a"] != "1" || info.Metadata["b"] != "2" {
		t.Fatalf("Metadata = %v, want owner=team a=1 b=2", info.Metadata)
	}
}
