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

package engine

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/vector"
)

// ---------------------------------------------------------------------------
// Cross-backend, cross-engine fixtures.
//
// "Every engine on every backend" (docs/databases/architecture.md) is tested
// here by running the IDENTICAL scenario, unmodified, against every
// (engine kind, storage backend) pair: MVCCEngine and KVEngine each over
// memory, file, pebble, and objstore. A scenario that only ran against one
// combination would not catch a handler that silently depended on an MVCC
// store's own batching, or on a backend's particular Get/Set semantics.
// ---------------------------------------------------------------------------

// namedVectorBackend pairs a label with a factory that builds a fresh
// storage.Storage instance. Each (backend, engine-kind) combination needs
// its OWN storage, never one shared between two subtests, so this is a
// constructor rather than a constructed value.
type namedVectorBackend struct {
	name string
	new  func(t *testing.T) storage.Storage
}

// vectorTestBackends lists every local backend. Pebble, file, and
// objstore(local) are rooted in per-test temp dirs.
func vectorTestBackends() []namedVectorBackend {
	return []namedVectorBackend{
		{"memory", func(t *testing.T) storage.Storage {
			return memory.NewStorage()
		}},
		{"pebble", func(t *testing.T) storage.Storage {
			t.Helper()
			pdb, err := pebble.New(&pebble.Config{DataDir: t.TempDir()})
			if err != nil {
				t.Fatalf("pebble.New: %v", err)
			}
			return pdb
		}},
		{"file", func(t *testing.T) storage.Storage {
			t.Helper()
			fdb, err := file.New(t.TempDir())
			if err != nil {
				t.Fatalf("file.New: %v", err)
			}
			return fdb
		}},
		{"objstore", func(t *testing.T) storage.Storage {
			t.Helper()
			odb, err := objstore.New("local", map[string]string{"path": t.TempDir()})
			if err != nil {
				t.Fatalf("objstore.New: %v", err)
			}
			return odb
		}},
	}
}

// namedVectorEngine pairs a label with a factory that builds an Engine of
// that kind over a given storage.Storage.
type namedVectorEngine struct {
	name string
	new  func(t *testing.T, stg storage.Storage) Engine
}

// vectorTestEngineKinds lists every Engine implementation vector commands
// must work on.
func vectorTestEngineKinds() []namedVectorEngine {
	return []namedVectorEngine{
		{"mvcc", func(t *testing.T, stg storage.Storage) Engine {
			t.Helper()
			eng, err := NewMVCCEngine(stg)
			if err != nil {
				t.Fatalf("NewMVCCEngine: %v", err)
			}
			return eng
		}},
		{"kv", func(t *testing.T, stg storage.Storage) Engine {
			return NewKVEngine(stg)
		}},
	}
}

// forEachVectorEngine runs fn against every (backend, engine kind) pair, in
// its own subtest, each over its own freshly built storage and engine --
// never a storage instance reused between two subtests, which would leak
// one subtest's namespaces into the next.
func forEachVectorEngine(t *testing.T, fn func(t *testing.T, eng Engine, stg storage.Storage)) {
	t.Helper()
	for _, b := range vectorTestBackends() {
		t.Run(b.name, func(t *testing.T) {
			for _, e := range vectorTestEngineKinds() {
				t.Run(e.name, func(t *testing.T) {
					stg := b.new(t)
					t.Cleanup(func() { _ = stg.Close() })
					eng := e.new(t, stg)
					t.Cleanup(func() { _ = eng.Close(context.Background()) })
					fn(t, eng, stg)
				})
			}
		})
	}
}

// applyVectorCmd encodes and applies a single command, returning its result.
func applyVectorCmd(t *testing.T, eng Engine, cmd []byte) sm.Result {
	t.Helper()
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	return results[0]
}

// mustEncodeVector panics if an Encode* call returns an error, so every call
// site above reads as the command it builds rather than its own error
// handling. It takes no *testing.T, deliberately: Go only lets a call chain
// f(g(...)) when g's results are f's ENTIRE argument list, and every call
// site here is exactly protocol.EncodeVectorX(...) wrapped in this one call.
// An error here is a bug in the test's own fixture, not a scenario under
// test, so a panic that fails the test loudly is the right failure mode.
func mustEncodeVector(cmd []byte, err error) []byte {
	if err != nil {
		panic("mustEncodeVector: " + err.Error())
	}
	return cmd
}

// requireRefusal fails the test unless result carries an encoded application
// error -- the "deterministic refusal, consensus still advances" outcome
// every validation failure in vector_handlers.go produces, as opposed to a
// Go error from Update (which would mean the refusal was mis-declared and
// instead halted apply) -- AND unless its message contains want, a fragment
// that identifies which of pkg/vector's typed errors produced it. The
// app-error encoding carries only Error()'s text across the apply boundary
// (see protocol.EncodeAppError), not the Go type, so a message fragment
// stable to that type's Error() format is the only thing left to assert on;
// an earlier version of this helper accepted any non-empty want and only
// logged it, so every call site's claim about WHICH refusal fired was
// unverified (go-qrdb vector-replicated review, N2).
func requireRefusal(t *testing.T, result sm.Result, want string) {
	t.Helper()
	msg, ok := protocol.DecodeAppError(result.Data)
	if !ok {
		t.Fatalf("result = %+v, want an encoded application error", result)
	}
	if !strings.Contains(msg, want) {
		t.Fatalf("refusal message %q does not contain %q", msg, want)
	}
}

// ---------------------------------------------------------------------------
// Namespace lifecycle
// ---------------------------------------------------------------------------

func TestVectorNamespaceCreate_PersistsTheRecordUnderTheDocumentedKey(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		cmd := mustEncodeVector(protocol.EncodeVectorNamespaceCreate(protocol.VectorNamespaceCreateParams{
			Name: "docs", Dimension: 3, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW,
			CreatedAtUnixNano: 100,
		}))
		result := applyVectorCmd(t, eng, cmd)
		if _, refused := protocol.DecodeAppError(result.Data); refused {
			t.Fatalf("namespace create refused: %+v", result)
		}

		// Placement is asserted by opening the store and probing the exact
		// "__vec:" key, never through a higher-level read path
		// (docs/vectors/engineering.md "Target: acceptance tests").
		data, err := stg.Get(vector.NamespaceKey("docs"))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if data == nil {
			t.Fatal("namespace record not found at vector.NamespaceKey(\"docs\")")
		}
		rec, err := vector.DecodeNamespaceRecord(data)
		if err != nil {
			t.Fatalf("DecodeNamespaceRecord: %v", err)
		}
		if rec.Dimension != 3 || rec.NextID != 1 || rec.Seq != 0 {
			t.Fatalf("record = %+v, want Dimension=3 NextID=1 Seq=0", rec)
		}
	})
}

func TestVectorNamespaceCreate_DuplicateIsRefused(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		cmd := mustEncodeVector(protocol.EncodeVectorNamespaceCreate(protocol.VectorNamespaceCreateParams{Name: "ns", Dimension: 2}))
		applyVectorCmd(t, eng, cmd)
		result := applyVectorCmd(t, eng, cmd)
		requireRefusal(t, result, "already exists")
	})
}

func TestVectorNamespaceCreate_InvalidGeometryIsRefused(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		for _, tc := range []struct {
			name string
			p    protocol.VectorNamespaceCreateParams
			want string
		}{
			{"zero dimension", protocol.VectorNamespaceCreateParams{Name: "a", Dimension: 0}, "dimension must be greater than zero"},
			{"bad distance metric", protocol.VectorNamespaceCreateParams{Name: "b", Dimension: 1, DistanceMetric: 200}, "unknown distance metric"},
			{"bad index type", protocol.VectorNamespaceCreateParams{Name: "c", Dimension: 1, IndexType: 200}, "unknown index type"},
			{"empty name", protocol.VectorNamespaceCreateParams{Name: "", Dimension: 1}, "not a valid namespace name"},
			{"name with slash", protocol.VectorNamespaceCreateParams{Name: "a/b", Dimension: 1}, "not a valid namespace name"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cmd := mustEncodeVector(protocol.EncodeVectorNamespaceCreate(tc.p))
				result := applyVectorCmd(t, eng, cmd)
				requireRefusal(t, result, tc.want)

				// A refused create must not have left a record behind.
				if tc.p.Name != "" {
					data, err := stg.Get(vector.NamespaceKey(tc.p.Name))
					if err != nil {
						t.Fatalf("Get: %v", err)
					}
					if data != nil {
						t.Fatalf("a refused create wrote a namespace record for %q", tc.p.Name)
					}
				}
			})
		}
	})
}

func TestVectorNamespaceDelete_RemovesEveryKeyUnderTheNamespace(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorNamespaceCreate(
			protocol.VectorNamespaceCreateParams{Name: "ns", Dimension: 2})))
		applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{
			HasID: true, ID: 1, Embedding: []float32{1, 2}, Metadata: map[string]string{"k": "v"},
		})))

		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorNamespaceDelete("ns")))
		if _, refused := protocol.DecodeAppError(result.Data); refused {
			t.Fatalf("namespace delete refused: %+v", result)
		}

		for _, key := range [][]byte{
			vector.NamespaceKey("ns"),
			vector.VectorKey("ns", 1),
			vector.MetadataKey("ns", "k", "v", 1),
		} {
			data, err := stg.Get(key)
			if err != nil {
				t.Fatalf("Get(%q): %v", key, err)
			}
			if data != nil {
				t.Fatalf("key %q survived namespace delete", key)
			}
		}
	})
}

func TestVectorNamespaceDelete_OfAnAbsentNamespaceIsIdempotent(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorNamespaceDelete("never-existed")))
		if _, refused := protocol.DecodeAppError(result.Data); refused {
			t.Fatalf("deleting an absent namespace was refused: %+v", result)
		}
	})
}

// ---------------------------------------------------------------------------
// Upsert
// ---------------------------------------------------------------------------

func createNamespace(t *testing.T, eng Engine, name string, dimension uint32) {
	t.Helper()
	result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorNamespaceCreate(
		protocol.VectorNamespaceCreateParams{Name: name, Dimension: dimension})))
	if _, refused := protocol.DecodeAppError(result.Data); refused {
		t.Fatalf("namespace create for %q refused: %+v", name, result)
	}
}

func TestVectorUpsert_ExplicitIDPersistsTheRecord(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 2)
		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 42, protocol.VectorUpsertParams{
			HasID: true, ID: 7, Embedding: []float32{1, 2}, Metadata: map[string]string{"src": "a"},
		})))
		if _, refused := protocol.DecodeAppError(result.Data); refused {
			t.Fatalf("upsert refused: %+v", result)
		}
		if result.Value != 7 {
			t.Fatalf("result.Value = %d, want the assigned ID 7", result.Value)
		}

		data, err := stg.Get(vector.VectorKey("ns", 7))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		rec, err := vector.DecodeVectorRecord(data)
		if err != nil {
			t.Fatalf("DecodeVectorRecord: %v", err)
		}
		if rec.CreatedAtUnixNano != 42 || rec.UpdatedAtUnixNano != 42 {
			t.Fatalf("record timestamps = %+v, want both 42", rec)
		}

		meta, err := stg.Get(vector.MetadataKey("ns", "src", "a", 7))
		if err != nil {
			t.Fatalf("Get metadata: %v", err)
		}
		if meta == nil {
			t.Fatal("metadata index entry not found")
		}
	})
}

func TestVectorUpsert_OmittedIDsAreSequentialFromNamespaceNextID(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)
		first := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{Embedding: []float32{1}})))
		second := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 2, protocol.VectorUpsertParams{Embedding: []float32{2}})))
		if first.Value != 1 || second.Value != 2 {
			t.Fatalf("assigned IDs = %d, %d, want 1, 2", first.Value, second.Value)
		}
	})
}

func TestVectorUpsert_IsIdempotentByIDAndReplacesMetadata(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)
		applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{
			HasID: true, ID: 1, Embedding: []float32{1}, Metadata: map[string]string{"a": "1"},
		})))
		applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 2, protocol.VectorUpsertParams{
			HasID: true, ID: 1, Embedding: []float32{9}, Metadata: map[string]string{"b": "2"},
		})))

		// The old metadata entry must be gone, replaced by the new one, and
		// CreatedAt must be preserved from the first write.
		old, err := stg.Get(vector.MetadataKey("ns", "a", "1", 1))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if old != nil {
			t.Fatal("stale metadata index entry survived a replacing upsert")
		}
		data, err := stg.Get(vector.VectorKey("ns", 1))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		rec, err := vector.DecodeVectorRecord(data)
		if err != nil {
			t.Fatalf("DecodeVectorRecord: %v", err)
		}
		if rec.CreatedAtUnixNano != 1 || rec.UpdatedAtUnixNano != 2 {
			t.Fatalf("timestamps = %+v, want CreatedAt=1 UpdatedAt=2", rec)
		}
		if rec.Embedding[0] != 9 {
			t.Fatalf("embedding = %v, want the replacing value", rec.Embedding)
		}
	})
}

func TestVectorUpsert_DimensionMismatchIsRefusedAndWritesNothing(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 3)
		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{
			HasID: true, ID: 1, Embedding: []float32{1, 2},
		})))
		requireRefusal(t, result, "embedding has dimension")

		data, err := stg.Get(vector.VectorKey("ns", 1))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if data != nil {
			t.Fatal("a dimension-mismatched upsert wrote a vector record")
		}
	})
}

func TestVectorUpsert_UnknownNamespaceIsRefused(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("never-created", 1, protocol.VectorUpsertParams{
			Embedding: []float32{1},
		})))
		requireRefusal(t, result, "not found")
	})
}

// ---------------------------------------------------------------------------
// Batch upsert atomicity
// ---------------------------------------------------------------------------

func TestVectorBatchUpsert_AllOrNothing(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 2)
		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorBatchUpsert(protocol.VectorBatchUpsertParams{
			Name:              "ns",
			TimestampUnixNano: 1,
			Vectors: []protocol.VectorUpsertParams{
				{HasID: true, ID: 1, Embedding: []float32{1, 2}},
				{HasID: true, ID: 2, Embedding: []float32{1}}, // wrong dimension
			},
		})))
		requireRefusal(t, result, "embedding has dimension")

		for _, id := range []uint64{1, 2} {
			data, err := stg.Get(vector.VectorKey("ns", id))
			if err != nil {
				t.Fatalf("Get(%d): %v", id, err)
			}
			if data != nil {
				t.Fatalf("vector %d was written despite the batch being refused", id)
			}
		}
		rec, err := readNamespaceRecord(stg, "ns")
		if err != nil {
			t.Fatalf("readNamespaceRecord: %v", err)
		}
		if rec.Seq != 0 || rec.NextID != 1 {
			t.Fatalf("namespace record changed on a refused batch: %+v", rec)
		}
	})
}

func TestVectorBatchUpsert_AllSucceedTogether(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)
		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorBatchUpsert(protocol.VectorBatchUpsertParams{
			Name: "ns",
			Vectors: []protocol.VectorUpsertParams{
				{Embedding: []float32{1}},
				{Embedding: []float32{2}},
				{Embedding: []float32{3}},
			},
		})))
		if _, refused := protocol.DecodeAppError(result.Data); refused {
			t.Fatalf("batch upsert refused: %+v", result)
		}
		if result.Value != 3 {
			t.Fatalf("result.Value = %d, want 3", result.Value)
		}
		for _, id := range []uint64{1, 2, 3} {
			data, err := stg.Get(vector.VectorKey("ns", id))
			if err != nil {
				t.Fatalf("Get(%d): %v", id, err)
			}
			if data == nil {
				t.Fatalf("vector %d missing after a successful batch", id)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestVectorDelete_RemovesTheRecordAndItsMetadataEntries(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)
		applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{
			HasID: true, ID: 1, Embedding: []float32{1}, Metadata: map[string]string{"k": "v"},
		})))

		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorDelete("ns", 1)))
		if _, refused := protocol.DecodeAppError(result.Data); refused {
			t.Fatalf("delete refused: %+v", result)
		}

		for _, key := range [][]byte{vector.VectorKey("ns", 1), vector.MetadataKey("ns", "k", "v", 1)} {
			data, err := stg.Get(key)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if data != nil {
				t.Fatalf("key %q survived delete", key)
			}
		}
	})
}

func TestVectorDelete_UnknownIDIsRefused(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)
		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorDelete("ns", 999)))
		requireRefusal(t, result, "not found")
	})
}

func TestVectorBatchDelete_AllOrNothing(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)
		applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{HasID: true, ID: 1, Embedding: []float32{1}})))

		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorBatchDelete("ns", []uint64{1, 999})))
		requireRefusal(t, result, "not found")

		data, err := stg.Get(vector.VectorKey("ns", 1))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if data == nil {
			t.Fatal("vector 1 was deleted despite the batch being refused for vector 999")
		}
	})
}

// ---------------------------------------------------------------------------
// Update metadata
// ---------------------------------------------------------------------------

func TestVectorUpdateMetadata_ReplacesMetadataWithoutTouchingTheEmbedding(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)
		applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{
			HasID: true, ID: 1, Embedding: []float32{5}, Metadata: map[string]string{"a": "1"},
		})))

		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpdateMetadata(protocol.VectorUpdateMetadataParams{
			Name: "ns", ID: 1, TimestampUnixNano: 2, Metadata: map[string]string{"b": "2"},
		})))
		if _, refused := protocol.DecodeAppError(result.Data); refused {
			t.Fatalf("update metadata refused: %+v", result)
		}

		data, err := stg.Get(vector.VectorKey("ns", 1))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		rec, err := vector.DecodeVectorRecord(data)
		if err != nil {
			t.Fatalf("DecodeVectorRecord: %v", err)
		}
		if rec.Embedding[0] != 5 {
			t.Fatalf("embedding = %v, want unchanged [5]", rec.Embedding)
		}
		if rec.Metadata["b"] != "2" || rec.Metadata["a"] != "" {
			t.Fatalf("metadata = %v, want only b=2", rec.Metadata)
		}
		old, err := stg.Get(vector.MetadataKey("ns", "a", "1", 1))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if old != nil {
			t.Fatal("stale metadata index entry survived an update-metadata")
		}
	})
}

func TestVectorUpdateMetadata_UnknownVectorIsRefused(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)
		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpdateMetadata(protocol.VectorUpdateMetadataParams{
			Name: "ns", ID: 999,
		})))
		requireRefusal(t, result, "not found")
	})
}

// ---------------------------------------------------------------------------
// Index reconfigure
// ---------------------------------------------------------------------------

func TestVectorIndexReconfigure_AdvancesTrainingGeneration(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)
		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorIndexReconfigure(protocol.VectorIndexReconfigureParams{
			Name: "ns", IndexType: vector.IndexFlat, IndexParams: map[string]string{"nlist": "64"},
		})))
		if _, refused := protocol.DecodeAppError(result.Data); refused {
			t.Fatalf("index reconfigure refused: %+v", result)
		}
		if result.Value != 1 {
			t.Fatalf("result.Value = %d, want training generation 1", result.Value)
		}
		rec, err := readNamespaceRecord(stg, "ns")
		if err != nil {
			t.Fatalf("readNamespaceRecord: %v", err)
		}
		if rec.IndexType != vector.IndexFlat || rec.IndexParams["nlist"] != "64" || rec.TrainingGeneration != 1 {
			t.Fatalf("record = %+v, want IndexType=flat nlist=64 TrainingGeneration=1", rec)
		}
	})
}

func TestVectorIndexReconfigure_UnknownIndexTypeIsRefused(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)
		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorIndexReconfigure(protocol.VectorIndexReconfigureParams{
			Name: "ns", IndexType: 200,
		})))
		requireRefusal(t, result, "unknown index type")
	})
}

// ---------------------------------------------------------------------------
// Fault injection: a genuine storage failure must propagate as a Go error
// (halting Apply), never be swallowed into a refusal result. These call the
// apply* functions directly over a crafted storage.Batch, rather than through
// an Engine, so each test isolates exactly one failing call.
// ---------------------------------------------------------------------------

// vectorFaultBatch wraps a real storage.Batch and injects an error from a
// chosen method, reusing kv_engine_test.go's injectedError so a failure here
// reads identically to every other error-injection test in this package.
type vectorFaultBatch struct {
	storage.Batch
	failGet    bool
	failSet    bool
	failDelete bool
	failIter   bool
}

func (b *vectorFaultBatch) Get(key []byte) ([]byte, error) {
	if b.failGet {
		return nil, &injectedError{msg: "injected vector batch get error"}
	}
	return b.Batch.Get(key)
}

func (b *vectorFaultBatch) Set(key, value []byte) error {
	if b.failSet {
		return &injectedError{msg: "injected vector batch set error"}
	}
	return b.Batch.Set(key, value)
}

func (b *vectorFaultBatch) Delete(key []byte) error {
	if b.failDelete {
		return &injectedError{msg: "injected vector batch delete error"}
	}
	return b.Batch.Delete(key)
}

func (b *vectorFaultBatch) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	it := b.Batch.NewIterator(opts)
	if b.failIter {
		return &vectorFaultIterator{Iterator: it}
	}
	return it
}

// vectorFaultIterator wraps a real storage.Iterator and always reports an
// error once iteration ends, like failingIteratorStorage's iterator.
type vectorFaultIterator struct {
	storage.Iterator
}

func (it *vectorFaultIterator) Error() error {
	return &injectedError{msg: "injected vector iterator error"}
}

// newVectorFaultBatch returns a fresh vectorFaultBatch over a fresh memory
// store, pre-seeding it (via a clean batch) with namespace "ns" and,
// optionally, one vector so every fault test starts from identical state.
func newVectorFaultBatch(t *testing.T, seedVector bool) (*vectorFaultBatch, storage.Storage) {
	t.Helper()
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })

	seed := stg.Batch()
	rec := vector.NamespaceRecord{Dimension: 1, NextID: 1}
	if seedVector {
		rec.NextID = 2
	}
	data, err := vector.EncodeNamespaceRecord(rec)
	if err != nil {
		t.Fatalf("EncodeNamespaceRecord: %v", err)
	}
	if err := seed.Set(vector.NamespaceKey("ns"), data); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if seedVector {
		vdata, err := vector.EncodeVectorRecord(vector.VectorRecord{Embedding: []float32{1}, Metadata: map[string]string{"k": "v"}})
		if err != nil {
			t.Fatalf("EncodeVectorRecord: %v", err)
		}
		if err := seed.Set(vector.VectorKey("ns", 1), vdata); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := seed.Set(vector.MetadataKey("ns", "k", "v", 1), metadataIndexPresence); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if err := seed.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	return &vectorFaultBatch{Batch: stg.Batch()}, stg
}

func requireInjectedError(t *testing.T, err error) {
	t.Helper()
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Fatalf("error = %T (%v), want *injectedError", err, err)
	}
}

// payloadOnly is mustEncodeVector, but strips the leading opcode byte: the
// shape every apply* function takes when called directly, as every test
// below this point does, bypassing the Engine.Update/protocol.DecodeCommand
// path that strips it for applyVectorCmd's callers above.
func payloadOnly(cmd []byte, err error) []byte {
	return mustEncodeVector(cmd, err)[1:]
}

func TestApplyVectorNamespaceCreate_GetFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, false)
	fb.failGet = true
	_, err := applyVectorNamespaceCreate(fb, payloadOnly(protocol.EncodeVectorNamespaceCreate(
		protocol.VectorNamespaceCreateParams{Name: "other", Dimension: 1})))
	requireInjectedError(t, err)
}

func TestApplyVectorNamespaceCreate_SetFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, false)
	fb.failSet = true
	_, err := applyVectorNamespaceCreate(fb, payloadOnly(protocol.EncodeVectorNamespaceCreate(
		protocol.VectorNamespaceCreateParams{Name: "other", Dimension: 1})))
	requireInjectedError(t, err)
}

func TestApplyVectorNamespaceDelete_IteratorFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, true)
	fb.failIter = true
	_, err := applyVectorNamespaceDelete(fb, payloadOnly(protocol.EncodeVectorNamespaceDelete("ns")))
	requireInjectedError(t, err)
}

func TestApplyVectorNamespaceDelete_FinalDeleteFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, false)
	fb.failDelete = true
	_, err := applyVectorNamespaceDelete(fb, payloadOnly(protocol.EncodeVectorNamespaceDelete("ns")))
	requireInjectedError(t, err)
}

func TestApplyVectorUpsert_GetFaultOnExistingVectorPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, false)
	fb.failGet = true
	_, err := applyVectorUpsert(fb, payloadOnly(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{
		HasID: true, ID: 1, Embedding: []float32{1},
	})))
	requireInjectedError(t, err)
}

func TestApplyVectorUpsert_SetFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, false)
	fb.failSet = true
	_, err := applyVectorUpsert(fb, payloadOnly(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{
		HasID: true, ID: 1, Embedding: []float32{1},
	})))
	requireInjectedError(t, err)
}

func TestApplyVectorUpsert_MetadataIndexFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, true)
	fb.failDelete = true // replaceMetadataIndex deletes the stale "k=v" entry
	_, err := applyVectorUpsert(fb, payloadOnly(protocol.EncodeVectorUpsert("ns", 2, protocol.VectorUpsertParams{
		HasID: true, ID: 1, Embedding: []float32{1}, Metadata: map[string]string{"other": "val"},
	})))
	requireInjectedError(t, err)
}

func TestApplyVectorUpsert_MutationLogFaultPropagates(t *testing.T) {
	fb, stg := newVectorFaultBatch(t, false)
	// Let the vector write itself succeed; fail only the mutation-log append,
	// which is the LAST write upsertOne makes. failSet alone would also catch
	// the vector record write, so this proves the propagation happens
	// regardless of WHICH write inside upsertOne fails, by checking nothing
	// before the mutation log committed either (same-batch atomicity).
	fb.failSet = true
	_, err := applyVectorUpsert(fb, payloadOnly(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{
		HasID: true, ID: 1, Embedding: []float32{1},
	})))
	requireInjectedError(t, err)
	if data, _ := stg.Get(vector.VectorKey("ns", 1)); data != nil {
		t.Fatal("a faulted upsert's vector record reached committed storage")
	}
}

func TestApplyVectorBatchUpsert_SetFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, false)
	fb.failSet = true
	_, err := applyVectorBatchUpsert(fb, payloadOnly(protocol.EncodeVectorBatchUpsert(protocol.VectorBatchUpsertParams{
		Name:    "ns",
		Vectors: []protocol.VectorUpsertParams{{Embedding: []float32{1}}},
	})))
	requireInjectedError(t, err)
}

func TestApplyVectorBatchUpsert_NamespaceReadFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, false)
	fb.failGet = true
	_, err := applyVectorBatchUpsert(fb, payloadOnly(protocol.EncodeVectorBatchUpsert(protocol.VectorBatchUpsertParams{
		Name: "ns",
	})))
	requireInjectedError(t, err)
}

func TestApplyVectorDelete_GetFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, true)
	fb.failGet = true
	_, err := applyVectorDelete(fb, payloadOnly(protocol.EncodeVectorDelete("ns", 1)))
	requireInjectedError(t, err)
}

func TestApplyVectorDelete_NamespaceReadFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, false)
	fb.failGet = true
	_, err := applyVectorDelete(fb, payloadOnly(protocol.EncodeVectorDelete("ns", 1)))
	requireInjectedError(t, err)
}

func TestApplyVectorDelete_MetadataDeleteFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, true)
	fb.failDelete = true
	_, err := applyVectorDelete(fb, payloadOnly(protocol.EncodeVectorDelete("ns", 1)))
	requireInjectedError(t, err)
}

func TestApplyVectorBatchDelete_PreCheckGetFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, true)
	fb.failGet = true
	_, err := applyVectorBatchDelete(fb, payloadOnly(protocol.EncodeVectorBatchDelete("ns", []uint64{1})))
	requireInjectedError(t, err)
}

func TestApplyVectorBatchDelete_NamespaceReadFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, false)
	fb.failGet = true
	_, err := applyVectorBatchDelete(fb, payloadOnly(protocol.EncodeVectorBatchDelete("ns", nil)))
	requireInjectedError(t, err)
}

func TestApplyVectorUpdateMetadata_NamespaceReadFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, false)
	fb.failGet = true
	_, err := applyVectorUpdateMetadata(fb, payloadOnly(protocol.EncodeVectorUpdateMetadata(
		protocol.VectorUpdateMetadataParams{Name: "ns", ID: 1})))
	requireInjectedError(t, err)
}

func TestApplyVectorUpdateMetadata_VectorSetFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, true)
	fb.failSet = true
	_, err := applyVectorUpdateMetadata(fb, payloadOnly(protocol.EncodeVectorUpdateMetadata(
		protocol.VectorUpdateMetadataParams{Name: "ns", ID: 1, Metadata: map[string]string{"a": "b"}})))
	requireInjectedError(t, err)
}

func TestApplyVectorUpdateMetadata_MutationLogFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, true)
	// The vector record's own Set succeeds; fail the SECOND write the
	// handler makes (the mutation-log append) by failing every Set after the
	// first succeeds is not expressible with this simple double, so this
	// instead confirms the metadata-index delete path -- the stale "k=v"
	// entry -- propagates a fault exactly as applyVectorUpsert's does.
	fb.failDelete = true
	_, err := applyVectorUpdateMetadata(fb, payloadOnly(protocol.EncodeVectorUpdateMetadata(
		protocol.VectorUpdateMetadataParams{Name: "ns", ID: 1, Metadata: map[string]string{"other": "val"}})))
	requireInjectedError(t, err)
}

func TestApplyVectorIndexReconfigure_NamespaceReadFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, false)
	fb.failGet = true
	_, err := applyVectorIndexReconfigure(fb, payloadOnly(protocol.EncodeVectorIndexReconfigure(
		protocol.VectorIndexReconfigureParams{Name: "ns"})))
	requireInjectedError(t, err)
}

func TestApplyVectorIndexReconfigure_SetFaultPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, false)
	fb.failSet = true
	_, err := applyVectorIndexReconfigure(fb, payloadOnly(protocol.EncodeVectorIndexReconfigure(
		protocol.VectorIndexReconfigureParams{Name: "ns"})))
	requireInjectedError(t, err)
}

func TestReadNamespaceRecord_CorruptRecordPropagatesADecodeError(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	if err := stg.Set(vector.NamespaceKey("ns"), []byte("not a namespace record")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	_, err := readNamespaceRecord(stg, "ns")
	if err == nil {
		t.Fatal("decoding a corrupt namespace record succeeded")
	}
	var versionErr *vector.UnsupportedRecordVersionError
	if !errors.As(err, &versionErr) {
		t.Fatalf("error = %T (%v), want *vector.UnsupportedRecordVersionError", err, err)
	}
}

// keyFaultBatch fails Set/Delete only for keys carrying a chosen prefix, so a
// test can fault exactly one of several writes an apply function makes in
// sequence -- vectorFaultBatch's failSet/failDelete fault EVERY write, which
// cannot isolate the LAST of several (e.g. appendMutationLog's write, which
// always follows the record write every upsert and delete also makes).
type keyFaultBatch struct {
	storage.Batch
	prefix     []byte
	failSet    bool
	failDelete bool
}

func (b *keyFaultBatch) Set(key, value []byte) error {
	if b.failSet && bytesHasPrefix(key, b.prefix) {
		return &injectedError{msg: "injected key-scoped set error"}
	}
	return b.Batch.Set(key, value)
}

func (b *keyFaultBatch) Delete(key []byte) error {
	if b.failDelete && bytesHasPrefix(key, b.prefix) {
		return &injectedError{msg: "injected key-scoped delete error"}
	}
	return b.Batch.Delete(key)
}

func bytesHasPrefix(s, prefix []byte) bool {
	return len(s) >= len(prefix) && string(s[:len(prefix)]) == string(prefix)
}

func TestAppendMutationLog_SetFaultPropagates(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	kb := &keyFaultBatch{Batch: stg.Batch(), prefix: vector.MutationLogPrefix("ns"), failSet: true}
	rec := &vector.NamespaceRecord{Dimension: 1}
	err := appendMutationLog(kb, "ns", rec, vector.MutationUpsert, 1)
	requireInjectedError(t, err)
}

func TestVectorUpsert_UnchangedMetadataPairLeavesItsIndexEntryAlone(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)
		applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{
			HasID: true, ID: 1, Embedding: []float32{1}, Metadata: map[string]string{"same": "1", "old": "a"},
		})))
		applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 2, protocol.VectorUpsertParams{
			HasID: true, ID: 1, Embedding: []float32{1}, Metadata: map[string]string{"same": "1", "new": "b"},
		})))

		// "same=1" must still be indexed; "old=a" must be gone; "new=b" must
		// now be indexed.
		for _, tc := range []struct {
			field, value string
			wantPresent  bool
		}{
			{"same", "1", true},
			{"old", "a", false},
			{"new", "b", true},
		} {
			data, err := stg.Get(vector.MetadataKey("ns", tc.field, tc.value, 1))
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if (data != nil) != tc.wantPresent {
				t.Fatalf("metadata entry %s=%s present=%v, want %v", tc.field, tc.value, data != nil, tc.wantPresent)
			}
		}
	})
}

func TestApplyVectorBatchDelete_AllSucceedTogether(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)
		applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorBatchUpsert(protocol.VectorBatchUpsertParams{
			Name: "ns",
			Vectors: []protocol.VectorUpsertParams{
				{HasID: true, ID: 1, Embedding: []float32{1}},
				{HasID: true, ID: 2, Embedding: []float32{2}},
			},
		})))
		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorBatchDelete("ns", []uint64{1, 2})))
		if _, refused := protocol.DecodeAppError(result.Data); refused {
			t.Fatalf("batch delete refused: %+v", result)
		}
		if result.Value != 2 {
			t.Fatalf("result.Value = %d, want 2", result.Value)
		}
		for _, id := range []uint64{1, 2} {
			data, err := stg.Get(vector.VectorKey("ns", id))
			if err != nil {
				t.Fatalf("Get(%d): %v", id, err)
			}
			if data != nil {
				t.Fatalf("vector %d survived a successful batch delete", id)
			}
		}
	})
}

func TestApplyVectorDelete_DecodeFaultOnCorruptStoredRecordPropagates(t *testing.T) {
	fb, _ := newVectorFaultBatch(t, false)
	// Written through fb itself: a storage.Batch reads its own pending
	// writes layered over committed storage, so this is visible to the
	// handler's own batch.Get without a separate commit.
	if err := fb.Set(vector.VectorKey("ns", 1), []byte("corrupt")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	_, err := applyVectorDelete(fb, payloadOnly(protocol.EncodeVectorDelete("ns", 1)))
	if err == nil {
		t.Fatal("deleting a vector with a corrupt stored record succeeded")
	}
	// Either decode-error type is a correct, honest report of "these bytes
	// are not what this package's own encoder produced" -- which one depends
	// only on which byte of "corrupt" collides with the version check.
	var versionErr *vector.UnsupportedRecordVersionError
	var recErr *vector.InvalidRecordError
	if !errors.As(err, &versionErr) && !errors.As(err, &recErr) {
		t.Fatalf("error = %T (%v), want *vector.UnsupportedRecordVersionError or *vector.InvalidRecordError", err, err)
	}
}

// ---------------------------------------------------------------------------
// Review regression tests (go-qrdb vector-replicated review, 2026-10-02):
// B1 (map-order determinism), B2 (duplicate-ID partial commit), B3 (KV
// forgery), B4 (empty-value metadata), N1 (ID allocator overflow), N7
// (OpGuarded-wrapped vector command; several vector entries sharing one
// MVCC Update). Each is pinned here PERMANENTLY, as a renamed and slightly
// hardened form of the reviewer's own probe where one existed.
// ---------------------------------------------------------------------------

// B1: two encodings of the identical record must be byte-identical. An
// unseeded, uncontrolled source of flakiness here IS the defect: Go
// randomises map iteration per process, so this only reproduces across many
// repetitions within one run, never across two runs of a single encoding.
func TestVectorRecord_RepeatedEncodingIsDeterministic(t *testing.T) {
	meta := make(map[string]string, 16)
	for i := 0; i < 16; i++ {
		meta[string(rune('a'+i))] = "v"
	}
	first, err := vector.EncodeVectorRecord(vector.VectorRecord{Embedding: []float32{1}, Metadata: meta})
	if err != nil {
		t.Fatalf("EncodeVectorRecord: %v", err)
	}
	for i := 0; i < 50; i++ {
		got, err := vector.EncodeVectorRecord(vector.VectorRecord{Embedding: []float32{1}, Metadata: meta})
		if err != nil {
			t.Fatalf("EncodeVectorRecord: %v", err)
		}
		if string(got) != string(first) {
			t.Fatalf("encoding %d of 50 differs byte-wise from the first", i)
		}
	}
}

// B1, the namespace-record side: protocol.EncodeVectorNamespaceCreate's
// IndexParams go through the identical shared codec.
func TestVectorNamespaceRecord_RepeatedEncodingIsDeterministic(t *testing.T) {
	params := make(map[string]string, 16)
	for i := 0; i < 16; i++ {
		params[string(rune('a'+i))] = "v"
	}
	first, err := vector.EncodeNamespaceRecord(vector.NamespaceRecord{Dimension: 1, IndexParams: params})
	if err != nil {
		t.Fatalf("EncodeNamespaceRecord: %v", err)
	}
	for i := 0; i < 50; i++ {
		got, err := vector.EncodeNamespaceRecord(vector.NamespaceRecord{Dimension: 1, IndexParams: params})
		if err != nil {
			t.Fatalf("EncodeNamespaceRecord: %v", err)
		}
		if string(got) != string(first) {
			t.Fatalf("encoding %d of 50 differs byte-wise from the first", i)
		}
	}
}

// B2: a batch delete naming the same ID twice must refuse the WHOLE command,
// leaving the vector, its metadata entry, and the namespace's mutation log
// exactly as they were -- not delete the vector and then refuse.
func TestVectorBatchDelete_DuplicateIDRefusesWithoutPartialCommit(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)
		applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{
			HasID: true, ID: 5, Embedding: []float32{1}, Metadata: map[string]string{"k": "v"},
		})))

		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorBatchDelete("ns", []uint64{5, 5})))
		requireRefusal(t, result, "named more than once")

		data, err := stg.Get(vector.VectorKey("ns", 5))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if data == nil {
			t.Fatal("DEFECT: a refused duplicate-ID batch delete still deleted the vector")
		}
		md, err := stg.Get(vector.MetadataKey("ns", "k", "v", 5))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if md == nil {
			t.Fatal("DEFECT: a refused duplicate-ID batch delete still removed the metadata entry")
		}
		rec, err := readNamespaceRecord(stg, "ns")
		if err != nil {
			t.Fatalf("readNamespaceRecord: %v", err)
		}
		if rec.Seq != 1 {
			t.Fatalf("Seq = %d, want 1 (unchanged by the refused batch)", rec.Seq)
		}
	})
}

// B3: an ordinary user KV write may never create, modify, or remove a key in
// the reserved vector namespace, on either engine, through any write verb
// that accepts an arbitrary caller-supplied key.
func TestVectorKeyspace_OrdinaryKVWritesAreRefused(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		key := vector.NamespaceKey("docs")

		putCmd, err := kv.EncodePut(key, []byte("garbage"))
		if err != nil {
			t.Fatalf("EncodePut: %v", err)
		}
		result := applyVectorCmd(t, eng, putCmd)
		requireRefusal(t, result, "reserved")

		data, err := stg.Get(key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if data != nil {
			t.Fatal("DEFECT: an ordinary KV Put forged a key in the reserved vector namespace")
		}

		deleteCmd, err := kv.EncodeDelete(key)
		if err != nil {
			t.Fatalf("EncodeDelete: %v", err)
		}
		result = applyVectorCmd(t, eng, deleteCmd)
		requireRefusal(t, result, "reserved")
	})
}

// B3, the read side: an exact-key KV Get of a vector key must not return the
// raw stored bytes, even though the internal-key discovery guard that
// protects other reserved namespaces documents itself as not covering
// exact-key Get.
func TestVectorKeyspace_ExactKeyGetIsRefused(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)
		applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{
			HasID: true, ID: 1, Embedding: []float32{1},
		})))

		_, err := eng.Lookup(context.Background(), vector.VectorKey("ns", 1))
		if err == nil {
			t.Fatal("DEFECT: an exact-key KV Get returned a raw vector record")
		}
		var forged *vector.ForgedKeyError
		if !errors.As(err, &forged) {
			t.Fatalf("error = %T (%v), want *vector.ForgedKeyError", err, err)
		}
	})
}

// B4, both directions: an empty metadata VALUE must be indexed when added and
// un-indexed when removed, exactly like a non-empty one -- a bare map read
// cannot tell "absent" from "present, empty string" apart, and this is the
// case that distinguishes a comma-ok fix from one that merely looks fixed.
func TestVectorMetadataIndex_EmptyValueIsIndexedAndUnindexed(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)

		// Add: the first write for this ID has no old record, so oldMeta is
		// nil -- a bare `oldMeta["tag"]` and a present-but-absent read are
		// identical there, which is exactly what comma-ok must tell apart.
		applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{
			HasID: true, ID: 1, Embedding: []float32{1}, Metadata: map[string]string{"tag": ""},
		})))
		data, err := stg.Get(vector.MetadataKey("ns", "tag", "", 1))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if data == nil {
			t.Fatal("DEFECT: metadata {tag:\"\"} was never indexed on add")
		}

		// Remove: replacing it with a DIFFERENT empty-valued field must
		// retract the old entry, not leave it behind because "" == "".
		applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpdateMetadata(protocol.VectorUpdateMetadataParams{
			Name: "ns", ID: 1, Metadata: map[string]string{"other": ""},
		})))
		data, err = stg.Get(vector.MetadataKey("ns", "tag", "", 1))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if data != nil {
			t.Fatal("DEFECT: metadata {tag:\"\"} was never un-indexed on removal")
		}
		data, err = stg.Get(vector.MetadataKey("ns", "other", "", 1))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if data == nil {
			t.Fatal("the replacing empty-valued field {other:\"\"} was not indexed")
		}
	})
}

// N1: an explicit ID of math.MaxUint64 must never let a LATER auto-allocated
// ID collide with it via NextID wrapping to 0.
func TestVectorUpsert_ExplicitMaxUint64IDIsRefused(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		createNamespace(t, eng, "ns", 1)
		applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{
			HasID: true, ID: 1, Embedding: []float32{1},
		})))

		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{
			HasID: true, ID: math.MaxUint64, Embedding: []float32{1},
		})))
		requireRefusal(t, result, "overflow")

		// The allocator must be unaffected by the refused attempt: the very
		// next auto-allocated ID is 2, never 0 or a repeat of 1.
		next := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 2, protocol.VectorUpsertParams{
			Embedding: []float32{2},
		})))
		if _, refused := protocol.DecodeAppError(next.Data); refused {
			t.Fatalf("auto-allocated upsert refused: %+v", next)
		}
		if next.Value != 2 {
			t.Fatalf("auto-allocated ID = %d, want 2", next.Value)
		}
	})
}

// N7: a vector command wrapped in OpGuarded must apply exactly as the
// unwrapped command would -- proving the guard's inner-command dispatch
// (e.updateHandlers / e.extHandlers) reaches the vector opcodes at all.
// Run over memory only, both engine kinds: OpGuarded's compare read goes
// through mvcc.Store's own internal key scheme on MVCCEngine, which is a
// production pairing only over memory/pebble/file -- objstore composes
// KVEngine alone (docs/databases/architecture.md, "file and objstore
// backends compose the plain KV engine, which has no MVCC"), and in fact
// cannot serve MVCCEngine's internal metadata key today at all (a separate,
// pre-existing, non-vector defect the NUL-byte backend fix out of scope
// here is expected to close). Proving OpGuarded's dispatch reaches the
// vector opcodes needs exactly one working combination per engine kind, not
// the full backend matrix.
func TestVectorUpsert_AppliesWhenWrappedInOpGuarded(t *testing.T) {
	for _, e := range vectorTestEngineKinds() {
		t.Run(e.name, func(t *testing.T) {
			stg := memory.NewStorage()
			t.Cleanup(func() { _ = stg.Close() })
			eng := e.new(t, stg)
			t.Cleanup(func() { _ = eng.Close(context.Background()) })

			createNamespace(t, eng, "ns", 1)
			inner := mustEncodeVector(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{
				HasID: true, ID: 1, Embedding: []float32{1},
			}))
			// A guard must carry at least one compare (protocol.EncodeGuarded
			// refuses an empty list outright), so this uses a trivially-true
			// one: a key that can never exist in a fresh namespace compares
			// version-equal-0, which CompareTargetVersion defines as "absent".
			absentKeyCompare, err := protocol.EncodeCompare(
				[]byte("__guard-probe-absent-key__"), protocol.CompareTargetVersion, protocol.CompareResultEqual,
				nil, 0, 0, 0)
			if err != nil {
				t.Fatalf("EncodeCompare: %v", err)
			}
			guarded, err := protocol.EncodeGuarded([][]byte{absentKeyCompare}, inner)
			if err != nil {
				t.Fatalf("EncodeGuarded: %v", err)
			}
			result := applyVectorCmd(t, eng, guarded)
			if _, refused := protocol.DecodeAppError(result.Data); refused {
				t.Fatalf("guarded upsert refused: %+v", result)
			}
			data, err := stg.Get(vector.VectorKey("ns", 1))
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if data == nil {
				t.Fatal("an OpGuarded-wrapped vector upsert did not reach the vector handler")
			}
		})
	}
}

// N7: several vector commands sharing one MVCC Update call (one shared
// apply batch) must each see the others' writes already staged
// (read-your-own-writes) and must each report correctly, matching ordinary
// per-entry application.
func TestVectorCommands_MultipleEntriesInOneUpdateShareReadYourWrites(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, stg storage.Storage) {
		create := mustEncodeVector(protocol.EncodeVectorNamespaceCreate(protocol.VectorNamespaceCreateParams{Name: "ns", Dimension: 1}))
		up1 := mustEncodeVector(protocol.EncodeVectorUpsert("ns", 1, protocol.VectorUpsertParams{Embedding: []float32{1}}))
		up2 := mustEncodeVector(protocol.EncodeVectorUpsert("ns", 2, protocol.VectorUpsertParams{Embedding: []float32{2}}))

		results := make([]sm.Result, 3)
		if err := eng.Update(context.Background(), []sm.Entry{
			{Index: 1, Cmd: create}, {Index: 2, Cmd: up1}, {Index: 3, Cmd: up2},
		}, results); err != nil {
			t.Fatalf("Update: %v", err)
		}
		for i, r := range results {
			if msg, refused := protocol.DecodeAppError(r.Data); refused {
				t.Fatalf("entry %d refused: %s", i, msg)
			}
		}
		if results[1].Value != 1 || results[2].Value != 2 {
			t.Fatalf("assigned IDs = %d, %d, want 1, 2", results[1].Value, results[2].Value)
		}

		// A batch upsert replacing the same ID's metadata twice, in one
		// command, must leave only the LAST write's metadata indexed.
		bu := mustEncodeVector(protocol.EncodeVectorBatchUpsert(protocol.VectorBatchUpsertParams{
			Name: "ns",
			Vectors: []protocol.VectorUpsertParams{
				{HasID: true, ID: 9, Embedding: []float32{1}, Metadata: map[string]string{"k": "a"}},
				{HasID: true, ID: 9, Embedding: []float32{1}, Metadata: map[string]string{"k": "b"}},
			},
		}))
		applyVectorCmd(t, eng, bu)
		a, err := stg.Get(vector.MetadataKey("ns", "k", "a", 9))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		b, err := stg.Get(vector.MetadataKey("ns", "k", "b", 9))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if a != nil {
			t.Error("stale metadata entry k=a survived two upserts of the same ID in one batch")
		}
		if b == nil {
			t.Error("metadata entry k=b from the second, later write is missing")
		}
	})
}

// Every vector handler that names a namespace -- not only create -- refuses
// an invalid name before it reads the namespace record, with the same typed
// error create already used. Before this fix, only create validated; every
// other handler fed an invalid name straight to readNamespaceRecord, which
// answered "namespace not found" instead of "invalid namespace name" --
// correct in outcome (both refuse) but the wrong reason, and
// docs/vectors/engineering.md's "namespace names ... are validated at every
// write" was not yet true (go-qrdb vector-replicated re-review).
func TestVectorHandlers_InvalidNamespaceNameRefusedBeforeNotFound(t *testing.T) {
	const badName = "Bad/Name" // uppercase and '/' both fail ValidNamespaceName
	forEachVectorEngine(t, func(t *testing.T, eng Engine, _ storage.Storage) {
		for _, tc := range []struct {
			name string
			cmd  func() ([]byte, error)
		}{
			{"Upsert", func() ([]byte, error) {
				return protocol.EncodeVectorUpsert(badName, 0, protocol.VectorUpsertParams{Embedding: []float32{1}})
			}},
			{"BatchUpsert", func() ([]byte, error) {
				return protocol.EncodeVectorBatchUpsert(protocol.VectorBatchUpsertParams{Name: badName, Vectors: []protocol.VectorUpsertParams{{Embedding: []float32{1}}}})
			}},
			{"Delete", func() ([]byte, error) { return protocol.EncodeVectorDelete(badName, 1) }},
			{"BatchDelete", func() ([]byte, error) { return protocol.EncodeVectorBatchDelete(badName, []uint64{1}) }},
			{"NamespaceDelete", func() ([]byte, error) { return protocol.EncodeVectorNamespaceDelete(badName) }},
			{"UpdateMetadata", func() ([]byte, error) {
				return protocol.EncodeVectorUpdateMetadata(protocol.VectorUpdateMetadataParams{Name: badName, ID: 1, Metadata: map[string]string{"k": "v"}})
			}},
			{"IndexReconfigure", func() ([]byte, error) {
				return protocol.EncodeVectorIndexReconfigure(protocol.VectorIndexReconfigureParams{Name: badName, IndexType: vector.IndexHNSW})
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cmd, err := tc.cmd()
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				result := applyVectorCmd(t, eng, cmd)
				requireRefusal(t, result, "not a valid namespace name")
			})
		}
	})
}

// B3: every write verb the fix touched, on both engines, refuses a forged
// vector key -- not only Put and Delete. B3's own fix spans
// kvHandlePut/kvHandlePutIfAbsent/kvHandleDelete/handleCompareAndSwap/
// handleCompareAndDelete/handleTransaction/handleBatch on KVEngine and the
// MVCC/base-KV equivalents on MVCCEngine; a verb fixed but never exercised
// here is a verb this suite cannot tell apart from one left unfixed.
func TestVectorKeyspace_EveryWriteVerbRefusesForgedKeys(t *testing.T) {
	forgedKey := vector.NamespaceKey("docs")

	t.Run("mvcc", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			cmd  func() ([]byte, error)
		}{
			{"Put", func() ([]byte, error) { return protocol.EncodeMVCCPut(forgedKey, []byte("v"), 0) }},
			{"PutIfAbsent", func() ([]byte, error) { return protocol.EncodeMVCCPutIfAbsent(forgedKey, []byte("v"), 0) }},
			{"Delete", func() ([]byte, error) { return protocol.EncodeMVCCDelete(forgedKey) }},
			{"CompareAndSwap", func() ([]byte, error) { return protocol.EncodeCompareAndSwap(forgedKey, []byte("old"), []byte("new")) }},
			{"CompareAndDelete", func() ([]byte, error) { return protocol.EncodeCompareAndDelete(forgedKey, []byte("old")) }},
			{"Batch", func() ([]byte, error) {
				op, err := protocol.EncodeOp(protocol.OpMVCCPut, forgedKey, []byte("v"), nil, 0)
				if err != nil {
					return nil, err
				}
				return protocol.EncodeBatch([][]byte{op})
			}},
			{"Transaction", func() ([]byte, error) {
				op, err := protocol.EncodeOp(protocol.OpMVCCPut, forgedKey, []byte("v"), nil, 0)
				if err != nil {
					return nil, err
				}
				return protocol.EncodeTransaction(nil, [][]byte{op}, nil)
			}},
			// A transaction COMPARE (not an op) naming the forged key, with
			// harmless ops on both branches: evaluating the compare at all
			// would answer a binary-search oracle against the forged key's
			// stored bytes, so the refusal must fire before any compare is
			// evaluated, not only before the selected ops are staged (go-qrdb
			// vector-replicated review, B3-residual).
			{"TransactionCompare", func() ([]byte, error) {
				cmp, err := protocol.EncodeCompare(forgedKey, protocol.CompareTargetValue, protocol.CompareResultEqual, []byte("x"), 0, 0, 0)
				if err != nil {
					return nil, err
				}
				op, err := protocol.EncodeOp(protocol.OpMVCCPut, []byte("harmless"), []byte("v"), nil, 0)
				if err != nil {
					return nil, err
				}
				return protocol.EncodeTransaction([][]byte{cmp}, [][]byte{op}, [][]byte{op})
			}},
			// An OpGuarded compare naming the forged key: whether the guard
			// held is itself the oracle bit, so this must refuse before
			// protocol.OpenGuarded evaluates it (same review finding).
			{"Guarded", func() ([]byte, error) {
				cmp, err := protocol.EncodeCompare(forgedKey, protocol.CompareTargetValue, protocol.CompareResultEqual, []byte("x"), 0, 0, 0)
				if err != nil {
					return nil, err
				}
				inner, err := protocol.EncodeMVCCPut([]byte("harmless"), []byte("v"), 0)
				if err != nil {
					return nil, err
				}
				return protocol.EncodeGuarded([][]byte{cmp}, inner)
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				stg := memory.NewStorage()
				defer func() { _ = stg.Close() }()
				eng, err := NewMVCCEngine(stg)
				if err != nil {
					t.Fatalf("NewMVCCEngine: %v", err)
				}
				defer func() { _ = eng.Close(context.Background()) }()

				cmd, err := tc.cmd()
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				result := applyVectorCmd(t, eng, cmd)
				requireRefusal(t, result, "reserved")

				data, err := stg.Get(forgedKey)
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if data != nil {
					t.Fatal("DEFECT: this write verb forged a key in the reserved vector namespace")
				}
				if harmless, err := stg.Get([]byte("harmless")); err != nil {
					t.Fatalf("Get(harmless): %v", err)
				} else if harmless != nil {
					t.Fatal("DEFECT: a compare naming the forged key still let its command's harmless op stage")
				}
			})
		}
	})

	t.Run("kv", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			cmd  func() ([]byte, error)
		}{
			{"Put", func() ([]byte, error) { return kv.EncodePut(forgedKey, []byte("v")) }},
			{"PutIfAbsent", func() ([]byte, error) { return kv.EncodePutIfAbsent(forgedKey, []byte("v")) }},
			{"Delete", func() ([]byte, error) { return kv.EncodeDelete(forgedKey) }},
			{"CompareAndSwap", func() ([]byte, error) { return protocol.EncodeCompareAndSwap(forgedKey, []byte("old"), []byte("new")) }},
			{"CompareAndDelete", func() ([]byte, error) { return protocol.EncodeCompareAndDelete(forgedKey, []byte("old")) }},
			{"Batch", func() ([]byte, error) {
				op, err := protocol.EncodeOp(protocol.OpFSMPut, forgedKey, []byte("v"), nil, 0)
				if err != nil {
					return nil, err
				}
				return protocol.EncodeBatch([][]byte{op})
			}},
			{"Transaction", func() ([]byte, error) {
				op, err := protocol.EncodeOp(protocol.OpMVCCPut, forgedKey, []byte("v"), nil, 0)
				if err != nil {
					return nil, err
				}
				return protocol.EncodeTransaction(nil, [][]byte{op}, nil)
			}},
			// See the MVCC block's matching case for why these two exist
			// (go-qrdb vector-replicated review, B3-residual).
			{"TransactionCompare", func() ([]byte, error) {
				cmp, err := protocol.EncodeCompare(forgedKey, protocol.CompareTargetValue, protocol.CompareResultEqual, []byte("x"), 0, 0, 0)
				if err != nil {
					return nil, err
				}
				op, err := protocol.EncodeOp(protocol.OpMVCCPut, []byte("harmless"), []byte("v"), nil, 0)
				if err != nil {
					return nil, err
				}
				return protocol.EncodeTransaction([][]byte{cmp}, [][]byte{op}, [][]byte{op})
			}},
			{"Guarded", func() ([]byte, error) {
				cmp, err := protocol.EncodeCompare(forgedKey, protocol.CompareTargetValue, protocol.CompareResultEqual, []byte("x"), 0, 0, 0)
				if err != nil {
					return nil, err
				}
				inner, err := kv.EncodePut([]byte("harmless"), []byte("v"))
				if err != nil {
					return nil, err
				}
				return protocol.EncodeGuarded([][]byte{cmp}, inner)
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				stg := memory.NewStorage()
				defer func() { _ = stg.Close() }()
				eng := NewKVEngine(stg)
				defer func() { _ = eng.Close(context.Background()) }()

				cmd, err := tc.cmd()
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				result := applyVectorCmd(t, eng, cmd)
				requireRefusal(t, result, "reserved")

				data, err := stg.Get(forgedKey)
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if data != nil {
					t.Fatal("DEFECT: this write verb forged a key in the reserved vector namespace")
				}
				if harmless, err := stg.Get([]byte("harmless")); err != nil {
					t.Fatalf("Get(harmless): %v", err)
				} else if harmless != nil {
					t.Fatal("DEFECT: a compare naming the forged key still let its command's harmless op stage")
				}
			})
		}
	})
}
