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
	"testing"
	"time"
)

func TestMemoryVectorStorage_ImplementsStorage(t *testing.T) {
	var _ Storage = NewMemoryVectorStorage()
}

func TestMemoryVectorStorage_UpsertGetDelete(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	vec := &VectorValue{ID: 1, Embedding: []float32{1, 2, 3}, Metadata: map[string]string{"k": "v"}}
	if err := s.UpsertVector(ctx, "ns", vec); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}

	got, err := s.GetVector(ctx, "ns", 1)
	if err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if got.ID != 1 || len(got.Embedding) != 3 || got.Metadata["k"] != "v" {
		t.Fatalf("GetVector returned %+v", got)
	}

	// Mutating the returned value must not alias the store's copy.
	got.Embedding[0] = 99
	got.Metadata["k"] = "mutated"
	again, err := s.GetVector(ctx, "ns", 1)
	if err != nil {
		t.Fatalf("GetVector (2nd): %v", err)
	}
	if again.Embedding[0] == 99 || again.Metadata["k"] == "mutated" {
		t.Fatal("GetVector result aliases the store's internal copy")
	}

	if err := s.DeleteVector(ctx, "ns", 1); err != nil {
		t.Fatalf("DeleteVector: %v", err)
	}
	if _, err := s.GetVector(ctx, "ns", 1); !errors.Is(err, ErrVectorNotFound) {
		t.Fatalf("GetVector after delete = %v, want ErrVectorNotFound", err)
	}
}

func TestMemoryVectorStorage_UpsertDoesNotAliasCaller(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	emb := []float32{1, 2, 3}
	meta := map[string]string{"k": "v"}
	vec := &VectorValue{ID: 1, Embedding: emb, Metadata: meta}
	if err := s.UpsertVector(ctx, "ns", vec); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}

	emb[0] = 100
	meta["k"] = "changed"

	got, err := s.GetVector(ctx, "ns", 1)
	if err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if got.Embedding[0] == 100 || got.Metadata["k"] == "changed" {
		t.Fatal("UpsertVector aliased the caller's slice/map")
	}
}

func TestMemoryVectorStorage_UpsertVector_Errors(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	if err := s.UpsertVector(ctx, "ns", nil); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("UpsertVector(nil) = %v, want ErrInvalidVector", err)
	}
	if err := s.UpsertVector(ctx, "ns", &VectorValue{ID: 0}); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("UpsertVector(id=0) = %v, want ErrInvalidID", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.UpsertVector(cancelled, "ns", &VectorValue{ID: 1}); !errors.Is(err, ErrContextCanceled) {
		t.Fatalf("UpsertVector with cancelled ctx = %v, want ErrContextCanceled", err)
	}
}

func TestMemoryVectorStorage_GetVector_Errors(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	if _, err := s.GetVector(ctx, "ns", 0); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("GetVector(id=0) = %v, want ErrInvalidID", err)
	}
	if _, err := s.GetVector(ctx, "missing-ns", 1); !errors.Is(err, ErrVectorNotFound) {
		t.Fatalf("GetVector(missing namespace) = %v, want ErrVectorNotFound", err)
	}
}

func TestMemoryVectorStorage_DeleteVector_Errors(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	if err := s.DeleteVector(ctx, "ns", 0); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("DeleteVector(id=0) = %v, want ErrInvalidID", err)
	}
	if err := s.DeleteVector(ctx, "missing-ns", 1); !errors.Is(err, ErrVectorNotFound) {
		t.Fatalf("DeleteVector(missing namespace) = %v, want ErrVectorNotFound", err)
	}

	if err := s.UpsertVector(ctx, "ns", &VectorValue{ID: 1}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	if err := s.DeleteVector(ctx, "ns", 2); !errors.Is(err, ErrVectorNotFound) {
		t.Fatalf("DeleteVector(missing id) = %v, want ErrVectorNotFound", err)
	}
}

func TestMemoryVectorStorage_BatchUpsertAndDelete(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	vecs := []*VectorValue{{ID: 1}, {ID: 2}, {ID: 3}}
	if err := s.BatchUpsertVectors(ctx, "ns", vecs); err != nil {
		t.Fatalf("BatchUpsertVectors: %v", err)
	}
	for _, id := range []uint64{1, 2, 3} {
		if _, err := s.GetVector(ctx, "ns", id); err != nil {
			t.Fatalf("GetVector(%d): %v", id, err)
		}
	}

	if err := s.BatchDeleteVectors(ctx, "ns", []uint64{1, 3, 999}); err != nil {
		t.Fatalf("BatchDeleteVectors: %v", err)
	}
	if _, err := s.GetVector(ctx, "ns", 1); !errors.Is(err, ErrVectorNotFound) {
		t.Fatalf("GetVector(1) after batch delete = %v, want ErrVectorNotFound", err)
	}
	if _, err := s.GetVector(ctx, "ns", 2); err != nil {
		t.Fatalf("GetVector(2) survives batch delete: %v", err)
	}
}

func TestMemoryVectorStorage_BatchUpsertAndDelete_Errors(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	if err := s.BatchUpsertVectors(ctx, "ns", nil); !errors.Is(err, ErrEmptyBatch) {
		t.Fatalf("BatchUpsertVectors(nil) = %v, want ErrEmptyBatch", err)
	}
	if err := s.BatchDeleteVectors(ctx, "ns", nil); !errors.Is(err, ErrEmptyBatch) {
		t.Fatalf("BatchDeleteVectors(nil) = %v, want ErrEmptyBatch", err)
	}
	if err := s.BatchUpsertVectors(ctx, "ns", []*VectorValue{{ID: 1}, nil}); !errors.Is(err, ErrInvalidVector) {
		t.Fatalf("BatchUpsertVectors with a nil element = %v, want ErrInvalidVector", err)
	}
	if err := s.BatchUpsertVectors(ctx, "ns", []*VectorValue{{ID: 0}}); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("BatchUpsertVectors with id=0 = %v, want ErrInvalidID", err)
	}
}

func TestMemoryVectorStorage_ScanByMetadata(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	for i, color := range []string{"red", "blue", "red"} {
		vec := &VectorValue{ID: uint64(i + 1), Metadata: map[string]string{"color": color}} //nolint:gosec // G115: test fixture
		if err := s.UpsertVector(ctx, "ns", vec); err != nil {
			t.Fatalf("UpsertVector: %v", err)
		}
	}

	results, err := s.ScanByMetadata(ctx, "ns", "color", "red", nil)
	if err != nil {
		t.Fatalf("ScanByMetadata: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("ScanByMetadata returned %d results, want 2", len(results))
	}
	if results[0].ID != 1 || results[1].ID != 3 {
		t.Fatalf("ScanByMetadata results not sorted by ID: %+v", results)
	}

	limited, err := s.ScanByMetadata(ctx, "ns", "color", "red", &ScanOptions{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("ScanByMetadata with opts: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != 3 {
		t.Fatalf("ScanByMetadata with Offset/Limit = %+v, want [id=3]", limited)
	}
}

func TestMemoryVectorStorage_ScanByMultipleFilters(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	vecs := []*VectorValue{
		{ID: 1, Metadata: map[string]string{"color": "red", "size": "s"}},
		{ID: 2, Metadata: map[string]string{"color": "red", "size": "m"}},
		{ID: 3, Metadata: map[string]string{"color": "blue", "size": "s"}},
	}
	if err := s.BatchUpsertVectors(ctx, "ns", vecs); err != nil {
		t.Fatalf("BatchUpsertVectors: %v", err)
	}

	// No filters delegates to ScanNamespace.
	all, err := s.ScanByMultipleFilters(ctx, "ns", nil, nil)
	if err != nil {
		t.Fatalf("ScanByMultipleFilters(nil): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ScanByMultipleFilters(nil) = %d results, want 3", len(all))
	}

	filters := []MetadataFilter{
		{Field: "color", Value: "red", Operator: FilterEqual},
		{Field: "size", Value: "s", Operator: FilterEqual},
	}
	results, err := s.ScanByMultipleFilters(ctx, "ns", filters, nil)
	if err != nil {
		t.Fatalf("ScanByMultipleFilters: %v", err)
	}
	if len(results) != 1 || results[0].ID != 1 {
		t.Fatalf("ScanByMultipleFilters = %+v, want [id=1]", results)
	}

	notEqual := []MetadataFilter{{Field: "color", Value: "red", Operator: FilterNotEqual}}
	results, err = s.ScanByMultipleFilters(ctx, "ns", notEqual, nil)
	if err != nil {
		t.Fatalf("ScanByMultipleFilters(NotEqual): %v", err)
	}
	if len(results) != 1 || results[0].ID != 3 {
		t.Fatalf("ScanByMultipleFilters(NotEqual) = %+v, want [id=3]", results)
	}
}

// TestMatchesFilter_UnhandledOperatorAlwaysPasses pins the documented parity
// with PebbleVectorStorage's ScanByMultipleFilters switch: FilterIn and
// FilterNotIn have no case there, so a filter using one never narrows the
// result, and this type must behave identically rather than silently
// filtering more strictly.
func TestMatchesFilter_UnhandledOperatorAlwaysPasses(t *testing.T) {
	vec := &VectorValue{Metadata: map[string]string{"color": "red"}}
	if !matchesFilter(vec, "color", "blue", FilterIn) {
		t.Error("FilterIn must always pass (unhandled, parity with PebbleVectorStorage)")
	}
	if !matchesFilter(vec, "color", "blue", FilterNotIn) {
		t.Error("FilterNotIn must always pass (unhandled, parity with PebbleVectorStorage)")
	}
}

func TestApplyScanOptions_OffsetBeyondLength(t *testing.T) {
	results := []*VectorValue{{ID: 1}, {ID: 2}}
	out := applyScanOptions(results, &ScanOptions{Offset: 10})
	if len(out) != 0 {
		t.Fatalf("applyScanOptions with Offset beyond length = %d results, want 0", len(out))
	}
}

func TestVectorValueSize_NoMetadataOrEmbedding(t *testing.T) {
	if vectorValueSize(&VectorValue{ID: 1}) == 0 {
		t.Fatal("vectorValueSize must count the fixed fields even with no embedding/metadata")
	}
}

func TestMemoryVectorStorage_ScanNamespace(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	// A namespace reached only through Upsert (never CreateNamespace) still
	// scans -- parity with PebbleVectorStorage.
	if err := s.UpsertVector(ctx, "unregistered", &VectorValue{ID: 1}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	results, err := s.ScanNamespace(ctx, "unregistered", nil)
	if err != nil {
		t.Fatalf("ScanNamespace: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("ScanNamespace = %d results, want 1", len(results))
	}

	empty, err := s.ScanNamespace(ctx, "never-touched", nil)
	if err != nil {
		t.Fatalf("ScanNamespace(never-touched): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("ScanNamespace(never-touched) = %d results, want 0", len(empty))
	}
}

func TestMemoryVectorStorage_NamespaceLifecycle(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	info := &NamespaceInfo{Name: "ns", Dimension: 3}
	if err := s.CreateNamespace(ctx, info); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if err := s.CreateNamespace(ctx, info); !errors.Is(err, ErrNamespaceExists) {
		t.Fatalf("CreateNamespace (duplicate) = %v, want ErrNamespaceExists", err)
	}

	list, err := s.ListNamespaces(ctx)
	if err != nil {
		t.Fatalf("ListNamespaces: %v", err)
	}
	if len(list) != 1 || list[0].Name != "ns" {
		t.Fatalf("ListNamespaces = %+v, want [ns]", list)
	}

	if err := s.UpsertVector(ctx, "ns", &VectorValue{ID: 1, Embedding: []float32{1, 2}}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}

	got, err := s.GetNamespaceInfo(ctx, "ns")
	if err != nil {
		t.Fatalf("GetNamespaceInfo: %v", err)
	}
	if got.VectorCount != 1 {
		t.Fatalf("GetNamespaceInfo.VectorCount = %d, want 1", got.VectorCount)
	}
	if got.Dimension != 3 {
		t.Fatalf("GetNamespaceInfo.Dimension = %d, want 3 (registered metadata preserved)", got.Dimension)
	}

	if err := s.DeleteNamespace(ctx, "ns"); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	if _, err := s.GetNamespaceInfo(ctx, "ns"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("GetNamespaceInfo after delete = %v, want ErrNamespaceNotFound", err)
	}
	if _, err := s.GetVector(ctx, "ns", 1); !errors.Is(err, ErrVectorNotFound) {
		t.Fatalf("GetVector after DeleteNamespace = %v, want ErrVectorNotFound (vectors removed too)", err)
	}
}

func TestMemoryVectorStorage_NamespaceErrors(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	if err := s.CreateNamespace(ctx, nil); !errors.Is(err, ErrInvalidNamespace) {
		t.Fatalf("CreateNamespace(nil) = %v, want ErrInvalidNamespace", err)
	}
	if err := s.CreateNamespace(ctx, &NamespaceInfo{Name: ""}); !errors.Is(err, ErrInvalidNamespace) {
		t.Fatalf("CreateNamespace(empty name) = %v, want ErrInvalidNamespace", err)
	}
	if err := s.DeleteNamespace(ctx, "missing"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("DeleteNamespace(missing) = %v, want ErrNamespaceNotFound", err)
	}
	if _, err := s.GetNamespaceInfo(ctx, "missing"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("GetNamespaceInfo(missing) = %v, want ErrNamespaceNotFound", err)
	}
}

func TestMemoryVectorStorage_GetStats(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	stats, err := s.GetStats(ctx, "empty")
	if err != nil {
		t.Fatalf("GetStats(empty): %v", err)
	}
	if stats.Count != 0 {
		t.Fatalf("GetStats(empty).Count = %d, want 0", stats.Count)
	}

	if err := s.UpsertVector(ctx, "ns", &VectorValue{ID: 1, Embedding: []float32{1, 2, 3}}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	stats, err = s.GetStats(ctx, "ns")
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.Count != 1 {
		t.Fatalf("GetStats.Count = %d, want 1", stats.Count)
	}
	if stats.SizeBytes == 0 {
		t.Fatal("GetStats.SizeBytes = 0, want a non-zero estimate")
	}
}

func TestMemoryVectorStorage_IDAllocation(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	if _, err := s.NextID("missing"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("NextID(missing) = %v, want ErrNamespaceNotFound", err)
	}
	if _, err := s.BatchAllocateIDs("missing", 3); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("BatchAllocateIDs(missing) = %v, want ErrNamespaceNotFound", err)
	}

	if err := s.CreateNamespace(ctx, &NamespaceInfo{Name: "ns"}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}

	first, err := s.NextID("ns")
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	second, err := s.NextID("ns")
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	if second != first+1 {
		t.Fatalf("NextID sequence = %d, %d; want consecutive", first, second)
	}

	if _, err := s.BatchAllocateIDs("ns", 0); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("BatchAllocateIDs(count=0) = %v, want ErrInvalidID", err)
	}
	batch, err := s.BatchAllocateIDs("ns", 3)
	if err != nil {
		t.Fatalf("BatchAllocateIDs: %v", err)
	}
	if len(batch) != 3 || batch[0] != second+1 || batch[2] != second+3 {
		t.Fatalf("BatchAllocateIDs = %v, want 3 consecutive IDs starting at %d", batch, second+1)
	}
}

func TestMemoryVectorStorage_CompactAndCleanupTombstones_AreNoOps(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	if err := s.Compact(ctx, "ns"); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := s.CleanupTombstones(ctx, time.Hour); err != nil {
		t.Fatalf("CleanupTombstones: %v", err)
	}
}

func TestMemoryVectorStorage_Close(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()

	if err := s.UpsertVector(ctx, "ns", &VectorValue{ID: 1}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Closing twice is a no-op, not an error.
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	ops := map[string]func() error{
		"UpsertVector": func() error { return s.UpsertVector(ctx, "ns", &VectorValue{ID: 1}) },
		"DeleteVector": func() error { return s.DeleteVector(ctx, "ns", 1) },
		"Compact":      func() error { return s.Compact(ctx, "ns") },
		"CreateNamespace": func() error {
			return s.CreateNamespace(ctx, &NamespaceInfo{Name: "x"})
		},
		"DeleteNamespace": func() error { return s.DeleteNamespace(ctx, "ns") },
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, ErrStorageClosed) {
			t.Errorf("%s after Close = %v, want ErrStorageClosed", name, err)
		}
	}

	if _, err := s.GetVector(ctx, "ns", 1); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("GetVector after Close = %v, want ErrStorageClosed", err)
	}
	if _, err := s.ScanNamespace(ctx, "ns", nil); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("ScanNamespace after Close = %v, want ErrStorageClosed", err)
	}
	if _, err := s.ScanByMetadata(ctx, "ns", "f", "v", nil); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("ScanByMetadata after Close = %v, want ErrStorageClosed", err)
	}
	if _, err := s.ScanByMultipleFilters(ctx, "ns", nil, nil); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("ScanByMultipleFilters after Close = %v, want ErrStorageClosed", err)
	}
	if _, err := s.ListNamespaces(ctx); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("ListNamespaces after Close = %v, want ErrStorageClosed", err)
	}
	if _, err := s.GetNamespaceInfo(ctx, "ns"); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("GetNamespaceInfo after Close = %v, want ErrStorageClosed", err)
	}
	if _, err := s.GetStats(ctx, "ns"); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("GetStats after Close = %v, want ErrStorageClosed", err)
	}
	if _, err := s.NextID("ns"); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("NextID after Close = %v, want ErrStorageClosed", err)
	}
	if _, err := s.BatchAllocateIDs("ns", 1); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("BatchAllocateIDs after Close = %v, want ErrStorageClosed", err)
	}
	if err := s.BatchUpsertVectors(ctx, "ns", []*VectorValue{{ID: 1}}); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("BatchUpsertVectors after Close = %v, want ErrStorageClosed", err)
	}
	if err := s.BatchDeleteVectors(ctx, "ns", []uint64{1}); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("BatchDeleteVectors after Close = %v, want ErrStorageClosed", err)
	}
	if err := s.CleanupTombstones(ctx, time.Hour); !errors.Is(err, ErrStorageClosed) {
		t.Errorf("CleanupTombstones after Close = %v, want ErrStorageClosed", err)
	}
}
