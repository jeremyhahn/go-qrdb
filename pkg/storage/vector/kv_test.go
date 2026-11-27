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
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
)

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

// genOf returns the generation half of namespace's Version.
func genOf(ctx context.Context, s *KVVectorStorage, namespace string) (uint64, error) {
	ver, err := s.Version(ctx, namespace)
	return ver.Generation, err
}

func TestKVVectorStorage_NilKVRefused(t *testing.T) {
	if _, err := NewKVVectorStorage(nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewKVVectorStorage(nil) = %v, want ErrInvalidConfig", err)
	}
}

// TestKVVectorStorage_UnguardedStoreRefused: a store without the
// multi-guarded batch cannot commit a write with its generation, so it is
// refused at construction.
func TestKVVectorStorage_UnguardedStoreRefused(t *testing.T) {
	_, err := NewKVVectorStorage(kvstore.NewMemoryKVStore())
	var unguarded *UnguardedStoreError
	if !errors.As(err, &unguarded) {
		t.Fatalf("NewKVVectorStorage(MemoryKVStore) = %v, want *UnguardedStoreError", err)
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("UnguardedStoreError does not match ErrInvalidConfig: %v", err)
	}
	if unguarded.Store == "" || !strings.Contains(err.Error(), unguarded.Store) {
		t.Fatalf("error %q does not name the store type %q", err, unguarded.Store)
	}
}

func TestKVVectorStorage_NamespaceLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newKVStorage(t, kvstoretest.NewGuardedMemoryStore())

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
	s := newKVStorage(t, kvstoretest.NewGuardedMemoryStore())
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
	if err := s.CreateNamespace(ctx, &NamespaceInfo{Name: "ns/x", Dimension: 2}); err != nil {
		t.Fatal(err)
	}
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
	kv := kvstoretest.NewGuardedMemoryStore()
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
	if len(keys) != 5 {
		t.Fatalf("keys = %v, want the 5 keys of namespace keep", keys)
	}
}

// TestKVVectorStorage_GenerationAdvancesOnEveryWrite: each vector write and
// delete advances the namespace's generation; a missing namespace has none.
func TestKVVectorStorage_GenerationAdvancesOnEveryWrite(t *testing.T) {
	ctx := context.Background()
	s := newKVStorage(t, kvstoretest.NewGuardedMemoryStore())
	if _, err := genOf(ctx, s, "ns"); !errors.Is(err, ErrNamespaceNotFound) {
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
		gen, err := genOf(ctx, s, "ns")
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
	*kvstoretest.GuardedMemoryStore
	during func()
}

func (k *interleavingKV) Exists(ctx context.Context, key string) (bool, error) {
	exists, err := k.GuardedMemoryStore.Exists(ctx, key)
	if hook := k.during; hook != nil {
		k.during = nil
		hook()
	}
	return exists, err
}

// genHookKV runs onGen once, right after the first read of a generation key:
// between a writer's version read and its guarded batch, which is where a
// replica's delete or recreate races it.
type genHookKV struct {
	*kvstoretest.GuardedMemoryStore
	onGen func()
}

func (k *genHookKV) Get(ctx context.Context, key string) ([]byte, error) {
	value, err := k.GuardedMemoryStore.Get(ctx, key)
	if hook := k.onGen; hook != nil && strings.HasPrefix(key, kvGenPrefix) {
		k.onGen = nil
		hook()
	}
	return value, err
}

// TestKVVectorStorage_ReplicaWriteRacingDeleteIsRefused is Codex round 3 (a):
// replica B reads the namespace version, replica A deletes the namespace
// before B's batch lands, and B's write must be refused rather than survive
// into A's recreate.
func TestKVVectorStorage_ReplicaWriteRacingDeleteIsRefused(t *testing.T) {
	ctx := context.Background()
	mem := kvstoretest.NewGuardedMemoryStore()
	a := newKVStorage(t, mem)
	hook := &genHookKV{GuardedMemoryStore: mem}
	b := newKVStorage(t, hook)
	if err := a.CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: 2}); err != nil {
		t.Fatal(err)
	}
	if err := a.UpsertVector(ctx, "ns", kvVec(1, nil)); err != nil {
		t.Fatal(err)
	}
	hook.onGen = func() {
		if err := a.DeleteNamespace(ctx, "ns"); err != nil {
			t.Fatalf("racing delete: %v", err)
		}
	}
	if err := b.UpsertVector(ctx, "ns", kvVec(2, nil)); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("upsert racing a delete = %v, want ErrNamespaceNotFound", err)
	}
	if err := b.UpsertVector(ctx, "ns", kvVec(3, nil)); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("upsert after a delete = %v, want ErrNamespaceNotFound", err)
	}
	if err := a.CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: 2}); err != nil {
		t.Fatal(err)
	}
	vecs, err := b.ScanNamespace(ctx, "ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 0 {
		t.Fatalf("recreated namespace holds %d old vectors, want none", len(vecs))
	}
	if _, err := a.GetVector(ctx, "ns", 1); !errors.Is(err, ErrVectorNotFound) {
		t.Fatalf("old vector 1 after recreate = %v, want ErrVectorNotFound", err)
	}
}

// TestKVVectorStorage_CheckedUpsertBindsTheValidatedIncarnation is the
// Codex P1 at kv.go:228: replica B validates the namespace record, replica
// A deletes and re-creates the namespace with another dimension before B
// commits, and B's write must be refused rather than land in the new
// incarnation it never validated.
func TestKVVectorStorage_CheckedUpsertBindsTheValidatedIncarnation(t *testing.T) {
	ctx := context.Background()
	mem := kvstoretest.NewGuardedMemoryStore()
	a := newKVStorage(t, mem)
	b := newKVStorage(t, mem)
	if err := a.CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: 2}); err != nil {
		t.Fatal(err)
	}
	checks := 0
	check := func(info *NamespaceInfo) error {
		checks++
		if info.Dimension != 2 {
			return fmt.Errorf("dimension %d, want 2", info.Dimension)
		}
		if checks == 1 {
			if err := a.DeleteNamespace(ctx, "ns"); err != nil {
				t.Fatalf("racing delete: %v", err)
			}
			if err := a.CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: 3}); err != nil {
				t.Fatalf("racing create: %v", err)
			}
		}
		return nil
	}
	if err := b.UpsertVectorChecked(ctx, "ns", kvVec(1, nil), check); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("checked upsert racing a recreate = %v, want ErrNamespaceNotFound", err)
	}
	if vecs, err := a.ScanNamespace(ctx, "ns", nil); err != nil || len(vecs) != 0 {
		t.Fatalf("recreated namespace = %d vectors, %v; want none", len(vecs), err)
	}

	// Without a race the checked write lands, and a refusing check refuses.
	if err := b.UpsertVectorChecked(ctx, "ns", &VectorValue{ID: 2, Embedding: []float32{1, 2, 3}}, func(info *NamespaceInfo) error {
		if info.Dimension != 3 {
			return fmt.Errorf("dimension %d, want 3", info.Dimension)
		}
		return nil
	}); err != nil {
		t.Fatalf("checked upsert: %v", err)
	}
	refused := errors.New("refused")
	if err := b.UpsertVectorChecked(ctx, "ns", kvVec(3, nil), func(*NamespaceInfo) error { return refused }); !errors.Is(err, refused) {
		t.Fatalf("refusing check = %v, want its error", err)
	}
	if _, err := a.GetVector(ctx, "ns", 3); !errors.Is(err, ErrVectorNotFound) {
		t.Fatalf("refused vector stored: %v", err)
	}
	if err := b.UpsertVectorChecked(ctx, "missing", kvVec(4, nil), nil); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("checked upsert into a missing namespace = %v, want ErrNamespaceNotFound", err)
	}
	if err := b.UpsertVectorChecked(ctx, "ns", nil, nil); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("nil vector = %v, want ErrInvalidVector", err)
	}
	if err := b.UpsertVectorChecked(ctx, "ns", &VectorValue{}, nil); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("zero id = %v, want ErrInvalidID", err)
	}
}

// TestKVVectorStorage_UpdateVectorKeepsConcurrentUpdates is the Codex P1
// at service/vector.go:3623 at the storage seam: two replicas merge
// metadata into one vector at once, and both updates survive.
func TestKVVectorStorage_UpdateVectorKeepsConcurrentUpdates(t *testing.T) {
	ctx := context.Background()
	mem := kvstoretest.NewGuardedMemoryStore()
	a := newKVStorage(t, mem)
	b := newKVStorage(t, mem)
	if err := a.CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: 2}); err != nil {
		t.Fatal(err)
	}
	if err := a.UpsertVector(ctx, "ns", kvVec(1, map[string]string{"base": "0"})); err != nil {
		t.Fatal(err)
	}
	merge := func(key string) func(*VectorValue) (*VectorValue, error) {
		return func(old *VectorValue) (*VectorValue, error) {
			next := *old
			next.Metadata = maps.Clone(old.Metadata)
			if next.Metadata == nil {
				next.Metadata = map[string]string{}
			}
			next.Metadata[key] = "1"
			return &next, nil
		}
	}

	// Replica A's update lands between replica B's read and B's write.
	calls := 0
	bMerge := merge("b")
	if _, err := b.UpdateVector(ctx, "ns", 1, func(old *VectorValue) (*VectorValue, error) {
		calls++
		if calls == 1 {
			if _, err := a.UpdateVector(ctx, "ns", 1, merge("a")); err != nil {
				t.Fatalf("racing update: %v", err)
			}
		}
		return bMerge(old)
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if calls != 2 {
		t.Fatalf("update ran %d times, want 2 (one retry after the conflict)", calls)
	}

	// Many concurrent updates across both replicas all survive.
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		st := a
		if i%2 == 1 {
			st = b
		}
		wg.Add(1)
		go func(st *KVVectorStorage, key string) {
			defer wg.Done()
			if _, err := st.UpdateVector(ctx, "ns", 1, merge(key)); err != nil {
				t.Errorf("update %s: %v", key, err)
			}
		}(st, fmt.Sprintf("k%d", i))
	}
	wg.Wait()
	got, err := a.GetVector(ctx, "ns", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Metadata) != 3+16 || got.Metadata["a"] != "1" || got.Metadata["b"] != "1" || got.Metadata["base"] != "0" {
		t.Fatalf("metadata = %v, want base, a, b and k0..k15", got.Metadata)
	}

	if _, err := a.UpdateVector(ctx, "ns", 9, merge("x")); !errors.Is(err, ErrVectorNotFound) {
		t.Fatalf("update of a missing vector = %v, want ErrVectorNotFound", err)
	}
	if _, err := a.UpdateVector(ctx, "ns", 1, func(*VectorValue) (*VectorValue, error) { return &VectorValue{ID: 2}, nil }); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("update changing the id = %v, want ErrInvalidVector", err)
	}
	if _, err := a.UpdateVector(ctx, "ns", 0, merge("x")); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("zero id = %v, want ErrInvalidID", err)
	}
}

// TestKVVectorStorage_RecreatedNamespaceRetiresTheOldIncarnation is Codex
// round 3 (a)+(b): a vector of the old incarnation that the delete's sweep
// did not reach is still invisible to the recreate, and the generation after
// delete+recreate is above every generation before the delete.
func TestKVVectorStorage_RecreatedNamespaceRetiresTheOldIncarnation(t *testing.T) {
	ctx := context.Background()
	mem := kvstoretest.NewGuardedMemoryStore()
	s := newKVStorage(t, mem)
	if err := s.CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.BatchUpsertVectors(ctx, "ns", []*VectorValue{kvVec(1, nil), kvVec(2, nil)}); err != nil {
		t.Fatal(err)
	}
	before, err := genOf(ctx, s, "ns")
	if err != nil {
		t.Fatal(err)
	}
	oldInc, err := mem.Get(ctx, kvIncKey("ns"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNamespace(ctx, "ns"); err != nil {
		t.Fatal(err)
	}
	// A straggler in the old incarnation, as a sweep cut short leaves it.
	straggler, err := vectorPut("ns", oldInc, kvVec(1, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Put(ctx, straggler.Key, straggler.Value); err != nil {
		t.Fatal(err)
	}
	if _, err := genOf(ctx, s, "ns"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("Generation of a deleted namespace = %v, want ErrNamespaceNotFound", err)
	}
	if err := s.CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: 2}); err != nil {
		t.Fatal(err)
	}
	ver, err := s.Version(ctx, "ns")
	if err != nil {
		t.Fatal(err)
	}
	if ver.Generation != 0 || before == 0 {
		t.Fatalf("generation before delete = %d, after recreate = %d; want > 0 then 0", before, ver.Generation)
	}
	if bytes.Equal(ver.Incarnation[:], oldInc) {
		t.Fatal("recreated namespace kept the old incarnation")
	}
	vecs, err := s.ScanNamespace(ctx, "ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 0 {
		t.Fatalf("recreated namespace shows %d vectors of the old incarnation", len(vecs))
	}
	if _, err := s.GetVector(ctx, "ns", 1); !errors.Is(err, ErrVectorNotFound) {
		t.Fatalf("GetVector of the straggler = %v, want ErrVectorNotFound", err)
	}
}

// TestKVVectorStorage_CreateNamespaceRacingReplicaKeepsTheFirst: a create on
// one replica that interleaves with another replica's create and ID
// allocation is refused and leaves the first creator's metadata and counter.
func TestKVVectorStorage_CreateNamespaceRacingReplicaKeepsTheFirst(t *testing.T) {
	ctx := context.Background()
	mem := kvstoretest.NewGuardedMemoryStore()
	first := newKVStorage(t, mem)
	kv := &interleavingKV{GuardedMemoryStore: mem}
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
	mem := kvstoretest.NewGuardedMemoryStore()
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
		"guarded": kvstoretest.NewGuardedMemoryStore(),
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
	s := newKVStorage(t, kvstoretest.NewGuardedMemoryStore())
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
