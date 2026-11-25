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

package vector

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// plainKV hides the atomic methods of a MemoryKVStore so the mutex path of
// the ID allocator is exercised.
type plainKV struct{ kvstore.KVStore }

func newKVStorage(t *testing.T, kv kvstore.KVStore) *KVVectorStorage {
	t.Helper()
	s, err := NewKVVectorStorage(kv)
	if err != nil {
		t.Fatalf("NewKVVectorStorage: %v", err)
	}
	return s
}

func kvVec(id uint64, meta map[string]string) *VectorValue {
	return &VectorValue{ID: id, Embedding: []float32{float32(id), 1}, Metadata: meta, CreatedAt: 1, UpdatedAt: 2, Version: 1}
}

func TestKVVectorStorage_NilKVRefused(t *testing.T) {
	if _, err := NewKVVectorStorage(nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewKVVectorStorage(nil) = %v, want ErrInvalidConfig", err)
	}
}

func TestKVVectorStorage_NamespaceLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newKVStorage(t, kvstore.NewMemoryKVStore())

	if _, err := s.GetNamespaceInfo(ctx, "ns"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("GetNamespaceInfo unknown = %v, want ErrNamespaceNotFound", err)
	}
	if err := s.DeleteNamespace(ctx, "ns"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("DeleteNamespace unknown = %v, want ErrNamespaceNotFound", err)
	}
	if err := s.UpdateNamespace(ctx, "ns", &NamespaceUpdate{}); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("UpdateNamespace unknown = %v, want ErrNamespaceNotFound", err)
	}
	if err := s.CreateNamespace(ctx, &NamespaceInfo{}); !errors.Is(err, ErrInvalidNamespace) {
		t.Fatalf("CreateNamespace empty = %v, want ErrInvalidNamespace", err)
	}

	info := &NamespaceInfo{Name: "ns", Dimension: 2, DistanceMetric: "cosine", IndexType: "hnsw", CreatedAt: time.Now().UnixNano()}
	if err := s.CreateNamespace(ctx, info); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if err := s.CreateNamespace(ctx, info); !errors.Is(err, ErrNamespaceExists) {
		t.Fatalf("CreateNamespace twice = %v, want ErrNamespaceExists", err)
	}
	if err := s.CreateNamespace(ctx, &NamespaceInfo{Name: "other", Dimension: 3}); err != nil {
		t.Fatalf("CreateNamespace other: %v", err)
	}

	got, err := s.GetNamespaceInfo(ctx, "ns")
	if err != nil || got.Name != "ns" || got.Dimension != 2 || got.DistanceMetric != "cosine" {
		t.Fatalf("GetNamespaceInfo = %+v, %v", got, err)
	}
	list, err := s.ListNamespaces(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListNamespaces = %v, %v; want 2", list, err)
	}

	upd := &NamespaceUpdate{IndexConfig: map[string]string{"m": "16"}, Metadata: map[string]string{"k": "v"}, UpdatedAt: 42}
	if err := s.UpdateNamespace(ctx, "ns", upd); err != nil {
		t.Fatalf("UpdateNamespace: %v", err)
	}
	got, _ = s.GetNamespaceInfo(ctx, "ns")
	if got.IndexConfig["m"] != "16" || got.Metadata["k"] != "v" || got.UpdatedAt != 42 || got.Dimension != 2 {
		t.Fatalf("after update = %+v", got)
	}

	if err := s.DeleteNamespace(ctx, "ns"); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	if _, err := s.GetNamespaceInfo(ctx, "ns"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("GetNamespaceInfo after delete = %v", err)
	}
	if _, err := s.NextID("ns"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("NextID after delete = %v", err)
	}
}

func TestKVVectorStorage_VectorCRUDAndScans(t *testing.T) {
	ctx := context.Background()
	s := newKVStorage(t, kvstore.NewMemoryKVStore())
	if err := s.CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: 2}); err != nil {
		t.Fatal(err)
	}

	if err := s.UpsertVector(ctx, "ns", nil); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("Upsert nil = %v", err)
	}
	if err := s.UpsertVector(ctx, "ns", kvVec(0, nil)); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("Upsert id 0 = %v", err)
	}
	if _, err := s.GetVector(ctx, "ns", 1); !errors.Is(err, ErrVectorNotFound) {
		t.Fatalf("Get missing = %v", err)
	}

	if err := s.UpsertVector(ctx, "ns", kvVec(1, map[string]string{"color": "red", "size": "s"})); err != nil {
		t.Fatal(err)
	}
	if err := s.BatchUpsertVectors(ctx, "ns", []*VectorValue{
		kvVec(2, map[string]string{"color": "blue", "size": "s"}),
		kvVec(3, map[string]string{"color": "red", "size": "l"}),
		kvVec(17, map[string]string{"color": "red", "size": "s"}),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.BatchUpsertVectors(ctx, "ns", nil); !errors.Is(err, ErrEmptyBatch) {
		t.Fatalf("empty batch = %v", err)
	}
	// A namespace whose name extends "ns" must not leak into its scans.
	if err := s.UpsertVector(ctx, "ns/x", kvVec(5, nil)); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetVector(ctx, "ns", 3)
	if err != nil || got.ID != 3 || got.Metadata["size"] != "l" || len(got.Embedding) != 2 {
		t.Fatalf("GetVector = %+v, %v", got, err)
	}

	all, err := s.ScanNamespace(ctx, "ns", nil)
	if err != nil || len(all) != 4 {
		t.Fatalf("ScanNamespace = %d, %v; want 4", len(all), err)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].ID >= all[i].ID {
			t.Fatalf("ScanNamespace not ordered by ID: %d then %d", all[i-1].ID, all[i].ID)
		}
	}
	page, _ := s.ScanNamespace(ctx, "ns", &ScanOptions{FromID: 2, Offset: 1, Limit: 1})
	if len(page) != 1 || page[0].ID != 3 {
		t.Fatalf("ScanNamespace page = %+v", page)
	}

	red, _ := s.ScanByMetadata(ctx, "ns", "color", "red", nil)
	if len(red) != 3 {
		t.Fatalf("ScanByMetadata red = %d, want 3", len(red))
	}
	redS, _ := s.ScanByMultipleFilters(ctx, "ns", []MetadataFilter{
		{Field: "color", Value: "red", Operator: FilterEqual},
		{Field: "size", Value: "l", Operator: FilterNotEqual},
	}, nil)
	if len(redS) != 2 || redS[0].ID != 1 || redS[1].ID != 17 {
		t.Fatalf("ScanByMultipleFilters = %+v", redS)
	}
	none, _ := s.ScanByMultipleFilters(ctx, "ns", nil, nil)
	if len(none) != 4 {
		t.Fatalf("ScanByMultipleFilters no filters = %d, want 4", len(none))
	}

	stats, err := s.GetStats(ctx, "ns")
	if err != nil || stats.Count != 4 || stats.SizeBytes == 0 {
		t.Fatalf("GetStats = %+v, %v", stats, err)
	}
	info, _ := s.GetNamespaceInfo(ctx, "ns")
	if info.VectorCount != 4 {
		t.Fatalf("VectorCount = %d, want 4", info.VectorCount)
	}

	if err := s.DeleteVector(ctx, "ns", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteVector(ctx, "ns", 1); !errors.Is(err, ErrVectorNotFound) {
		t.Fatalf("DeleteVector twice = %v", err)
	}
	if err := s.BatchDeleteVectors(ctx, "ns", []uint64{2, 99}); err != nil {
		t.Fatal(err)
	}
	rest, _ := s.ScanNamespace(ctx, "ns", nil)
	if len(rest) != 2 {
		t.Fatalf("after deletes = %d, want 2", len(rest))
	}
	if err := s.Compact(ctx, "ns"); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupTombstones(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
}

func TestKVVectorStorage_DeleteNamespaceRemovesEveryKey(t *testing.T) {
	ctx := context.Background()
	kv := kvstore.NewMemoryKVStore()
	s := newKVStorage(t, kv)
	if err := s.CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNamespace(ctx, &NamespaceInfo{Name: "keep", Dimension: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextID("ns"); err != nil {
		t.Fatal(err)
	}
	if err := s.BatchUpsertVectors(ctx, "ns", []*VectorValue{kvVec(1, nil), kvVec(2, nil)}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertVector(ctx, "keep", kvVec(1, nil)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNamespace(ctx, "ns"); err != nil {
		t.Fatal(err)
	}
	keys, err := kv.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if strings.HasSuffix(k, "/ns") || strings.Contains(k, "/ns/") {
			t.Fatalf("key %q of the deleted namespace survives", k)
		}
	}
	if len(keys) != 4 {
		t.Fatalf("keys = %v, want the 4 keys of namespace keep", keys)
	}
}

// TestKVVectorStorage_GenerationAdvancesOnEveryWrite: each vector write and
// delete advances the namespace's generation; a missing namespace has none.
func TestKVVectorStorage_GenerationAdvancesOnEveryWrite(t *testing.T) {
	ctx := context.Background()
	s := newKVStorage(t, kvstore.NewMemoryKVStore())
	if _, err := s.Generation(ctx, "ns"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("Generation(missing) err = %v, want ErrNamespaceNotFound", err)
	}
	if err := s.CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: 2}); err != nil {
		t.Fatal(err)
	}
	writes := []func() error{
		func() error { return s.UpsertVector(ctx, "ns", kvVec(1, nil)) },
		func() error { return s.BatchUpsertVectors(ctx, "ns", []*VectorValue{kvVec(2, nil), kvVec(3, nil)}) },
		func() error { return s.DeleteVector(ctx, "ns", 1) },
		func() error { return s.BatchDeleteVectors(ctx, "ns", []uint64{2, 3}) },
	}
	for i, write := range writes {
		if err := write(); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		gen, err := s.Generation(ctx, "ns")
		if err != nil {
			t.Fatal(err)
		}
		if gen != uint64(i+1) {
			t.Fatalf("generation after write %d = %d, want %d", i, gen, i+1)
		}
	}
}

// interleavingKV runs during, once, after the first Exists call has read.
type interleavingKV struct {
	*kvstore.MemoryKVStore
	during func()
}

func (k *interleavingKV) Exists(ctx context.Context, key string) (bool, error) {
	exists, err := k.MemoryKVStore.Exists(ctx, key)
	if hook := k.during; hook != nil {
		k.during = nil
		hook()
	}
	return exists, err
}

// TestKVVectorStorage_CreateNamespaceRacingReplicaKeepsTheFirst: a create on
// one replica that interleaves with another replica's create and ID
// allocation is refused and leaves the first creator's metadata and counter.
func TestKVVectorStorage_CreateNamespaceRacingReplicaKeepsTheFirst(t *testing.T) {
	ctx := context.Background()
	mem := kvstore.NewMemoryKVStore()
	first := newKVStorage(t, mem)
	kv := &interleavingKV{MemoryKVStore: mem}
	late := newKVStorage(t, kv)
	kv.during = func() {
		if err := first.CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: 2}); err != nil {
			t.Fatalf("first create: %v", err)
		}
		if _, err := first.BatchAllocateIDs("ns", 3); err != nil {
			t.Fatalf("allocate: %v", err)
		}
	}
	if err := late.CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: 8}); !errors.Is(err, ErrNamespaceExists) {
		t.Fatalf("racing create err = %v, want ErrNamespaceExists", err)
	}
	info, err := first.GetNamespaceInfo(ctx, "ns")
	if err != nil {
		t.Fatal(err)
	}
	if info.Dimension != 2 {
		t.Fatalf("dimension = %d, want the first creator's 2", info.Dimension)
	}
	id, err := first.NextID("ns")
	if err != nil {
		t.Fatal(err)
	}
	if id != 4 {
		t.Fatalf("NextID after 3 allocations = %d, want 4", id)
	}
}

// TestKVVectorStorage_ConcurrentCreateNamespaceOneWinner: replicas creating
// one namespace at once yield one creator, whose metadata stands, and IDs
// allocated afterwards on every replica never repeat.
func TestKVVectorStorage_ConcurrentCreateNamespaceOneWinner(t *testing.T) {
	ctx := context.Background()
	mem := kvstore.NewMemoryKVStore()
	const replicas = 16
	stores := make([]*KVVectorStorage, replicas)
	for i := range stores {
		stores[i] = newKVStorage(t, mem)
	}
	created := make([]bool, replicas)
	ids := make([][]uint64, replicas)
	var wg sync.WaitGroup
	for i := range stores {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := stores[i].CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: uint32(i + 1)})
			switch {
			case err == nil:
				created[i] = true
			case !errors.Is(err, ErrNamespaceExists):
				t.Errorf("create %d: %v", i, err)
				return
			}
			for n := 0; n < 8; n++ {
				id, err := stores[i].NextID("ns")
				if err != nil {
					t.Errorf("NextID %d: %v", i, err)
					return
				}
				ids[i] = append(ids[i], id)
			}
		}(i)
	}
	wg.Wait()
	winner := -1
	for i, ok := range created {
		if ok {
			if winner >= 0 {
				t.Fatalf("creates %d and %d both succeeded", winner, i)
			}
			winner = i
		}
	}
	if winner < 0 {
		t.Fatal("no create succeeded")
	}
	info, err := stores[0].GetNamespaceInfo(ctx, "ns")
	if err != nil {
		t.Fatal(err)
	}
	if info.Dimension != uint32(winner+1) {
		t.Fatalf("dimension = %d, want the creator's %d", info.Dimension, winner+1)
	}
	seen := map[uint64]bool{}
	for _, list := range ids {
		for _, id := range list {
			if seen[id] {
				t.Fatalf("id %d allocated twice", id)
			}
			seen[id] = true
		}
	}
}

func TestKVVectorStorage_IDsPersistAcrossInstances(t *testing.T) {
	for name, kv := range map[string]kvstore.KVStore{
		"atomic": kvstore.NewMemoryKVStore(),
		"plain":  plainKV{kvstore.NewMemoryKVStore()},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			first := newKVStorage(t, kv)
			if err := first.CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: 2}); err != nil {
				t.Fatal(err)
			}
			if _, err := first.NextID("missing"); !errors.Is(err, ErrNamespaceNotFound) {
				t.Fatalf("NextID unknown = %v", err)
			}
			if _, err := first.BatchAllocateIDs("ns", 0); !errors.Is(err, ErrInvalidID) {
				t.Fatalf("BatchAllocateIDs 0 = %v", err)
			}
			id1, err := first.NextID("ns")
			if err != nil || id1 != 1 {
				t.Fatalf("NextID = %d, %v; want 1", id1, err)
			}
			ids, err := first.BatchAllocateIDs("ns", 3)
			if err != nil || len(ids) != 3 || ids[0] != 2 || ids[2] != 4 {
				t.Fatalf("BatchAllocateIDs = %v, %v; want [2 3 4]", ids, err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := first.NextID("ns"); !errors.Is(err, ErrStorageClosed) {
				t.Fatalf("NextID after Close = %v", err)
			}

			second := newKVStorage(t, kv)
			id5, err := second.NextID("ns")
			if err != nil || id5 != 5 {
				t.Fatalf("NextID on a second instance = %d, %v; want 5", id5, err)
			}
			if _, err := second.GetNamespaceInfo(ctx, "ns"); err != nil {
				t.Fatalf("namespace lost across instances: %v", err)
			}
		})
	}
}

func TestKVVectorStorage_ClosedAndCanceled(t *testing.T) {
	s := newKVStorage(t, kvstore.NewMemoryKVStore())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.UpsertVector(ctx, "ns", kvVec(1, nil)); !errors.Is(err, ErrContextCanceled) {
		t.Fatalf("Upsert canceled = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if _, err := s.ListNamespaces(context.Background()); !errors.Is(err, ErrStorageClosed) {
		t.Fatalf("ListNamespaces closed = %v", err)
	}
}
