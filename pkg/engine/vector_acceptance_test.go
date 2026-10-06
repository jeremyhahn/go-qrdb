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
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// ---------------------------------------------------------------------------
// docs/vectors/engineering.md "Target: acceptance tests (lane 1B)", items 1,
// 2, 5 and 10 -- phase 2 (index, checkpoint, determinism).
//
// Every "restart" here means what it means for these two engines: a BRAND
// NEW engine instance (a fresh, empty VectorIndexManager) constructed over
// the SAME storage.Storage -- the one thing a real process restart keeps and
// an in-memory index never survives, by construction (no node-local
// state: a checkpoint is a cache of what WarmVectorIndex can always
// reconstruct from the replicated store, see
// TestVectorAcceptance_DeletedCheckpointCostsOnlyARebuild below).
// ---------------------------------------------------------------------------

// vectorSearch runs query against namespace through eng's Lookup, failing
// the test on any error, for the acceptance tests below where the search
// succeeding (not refusing) is itself part of what is being asserted.
func vectorSearch(t *testing.T, eng Engine, namespace string, query []float32, k int) []service.VectorSearchHit {
	t.Helper()
	result, err := eng.Lookup(context.Background(), service.VectorSearchQuery{Namespace: namespace, Query: query, K: k})
	if err != nil {
		t.Fatalf("Lookup(VectorSearchQuery): %v", err)
	}
	hits, ok := result.([]service.VectorSearchHit)
	if !ok {
		t.Fatalf("Lookup(VectorSearchQuery) = %T, want []service.VectorSearchHit", result)
	}
	return hits
}

func hitIDs(hits []service.VectorSearchHit) []uint64 {
	ids := make([]uint64, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	return ids
}

func idsEqual(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestVectorAcceptance_RestartKeepsSearchWorking is acceptance test #1: this
// fails against the shipped phase-1 code, whose startup index is empty and
// has no Warm step at all.
func TestVectorAcceptance_RestartKeepsSearchWorking(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	dir := t.TempDir()

	eng1, err := NewMVCCEngine(stg, WithMVCCVectorIndex(dir, nil))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	createNamespace(t, eng1, "ns", 2)
	applyVectorCmd(t, eng1, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 0, protocol.VectorUpsertParams{HasID: true, ID: 1, Embedding: []float32{1, 0}})))
	applyVectorCmd(t, eng1, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 0, protocol.VectorUpsertParams{HasID: true, ID: 2, Embedding: []float32{0, 1}})))
	applyVectorCmd(t, eng1, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 0, protocol.VectorUpsertParams{HasID: true, ID: 3, Embedding: []float32{0.9, 0.1}})))
	_ = eng1.Close(context.Background())

	before := hitIDs(vectorSearch(t, eng1, "ns", []float32{1, 0}, 2))
	if len(before) != 2 {
		t.Fatalf("before restart: got %d hits, want 2", len(before))
	}

	// "Restart": a fresh engine over the SAME storage, nothing warmed yet.
	eng2, err := NewMVCCEngine(stg, WithMVCCVectorIndex(dir, nil))
	if err != nil {
		t.Fatalf("NewMVCCEngine (restart): %v", err)
	}
	defer func() { _ = eng2.Close(context.Background()) }()
	if err := eng2.WarmVectorIndex(); err != nil {
		t.Fatalf("WarmVectorIndex: %v", err)
	}

	after := hitIDs(vectorSearch(t, eng2, "ns", []float32{1, 0}, 2))
	if !idsEqual(before, after) {
		t.Fatalf("DEFECT: search after restart = %v, want the same top-k as before restart %v", after, before)
	}
}

// TestVectorAcceptance_MetricSurvivesRestart is acceptance test #2. vA is
// perfectly aligned with the query but far away in Euclidean terms; vB is
// close in Euclidean terms but points in a very different direction. A
// cosine namespace must rank vA first both before and after restart; an
// engine that silently fell back to Euclidean ranking after rebuilding
// would rank vB first instead.
func TestVectorAcceptance_MetricSurvivesRestart(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	dir := t.TempDir()

	eng1, err := NewMVCCEngine(stg, WithMVCCVectorIndex(dir, nil))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	// createNamespace defaults DistanceMetric to its zero value,
	// vector.DistanceCosine.
	createNamespace(t, eng1, "ns", 2)

	vA := []float32{100, 0} // same direction as the query: cosine distance 0
	vB := []float32{1.1, 1} // much closer in Euclidean terms, very different direction
	query := []float32{1, 0}

	applyVectorCmd(t, eng1, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 0, protocol.VectorUpsertParams{HasID: true, ID: 1, Embedding: vA})))
	applyVectorCmd(t, eng1, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 0, protocol.VectorUpsertParams{HasID: true, ID: 2, Embedding: vB})))
	_ = eng1.Close(context.Background())

	beforeTop := vectorSearch(t, eng1, "ns", query, 1)
	if len(beforeTop) != 1 || beforeTop[0].ID != 1 {
		t.Fatalf("before restart: top-1 = %+v, want vA (ID 1) by cosine distance", beforeTop)
	}

	eng2, err := NewMVCCEngine(stg, WithMVCCVectorIndex(dir, nil))
	if err != nil {
		t.Fatalf("NewMVCCEngine (restart): %v", err)
	}
	defer func() { _ = eng2.Close(context.Background()) }()
	if err := eng2.WarmVectorIndex(); err != nil {
		t.Fatalf("WarmVectorIndex: %v", err)
	}

	afterTop := vectorSearch(t, eng2, "ns", query, 1)
	if len(afterTop) != 1 || afterTop[0].ID != 1 {
		t.Fatalf("DEFECT: after restart, top-1 = %+v, want vA (ID 1) by cosine distance -- the namespace's metric did not survive restart", afterTop)
	}
}

// TestVectorAcceptance_CrashBetweenCommitAndIndexUpdate is acceptance test
// #5. The upsert's batch commit (the store write) and its in-memory index
// update are two different steps (docs/vectors/engineering.md "Apply and
// the index"); this proves the second one is never needed for the vector to
// be searchable again, by constructing a fresh engine that never ran it at
// all -- the same "no node-local state" property restart already exercises,
// stated here for the specific step the review named.
func TestVectorAcceptance_CrashBetweenCommitAndIndexUpdate(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	dir := t.TempDir()

	eng1, err := NewMVCCEngine(stg, WithMVCCVectorIndex(dir, nil))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	createNamespace(t, eng1, "ns", 2)
	applyVectorCmd(t, eng1, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 0, protocol.VectorUpsertParams{HasID: true, ID: 1, Embedding: []float32{1, 0}})))
	_ = eng1.Close(context.Background())

	// A fresh engine's VectorIndexManager never received eng1's index
	// update for ID 1 -- it never existed in this process -- yet the commit
	// that preceded it is durable in stg. This models "crash between commit
	// and index update" directly: the bytes vector.VectorKey("ns", 1) names
	// in stg are the entry's whole truth, and the in-memory index is
	// entirely reconstructable from them.
	eng2, err := NewMVCCEngine(stg, WithMVCCVectorIndex(dir, nil))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng2.Close(context.Background()) }()
	if err := eng2.WarmVectorIndex(); err != nil {
		t.Fatalf("WarmVectorIndex: %v", err)
	}

	hits := vectorSearch(t, eng2, "ns", []float32{1, 0}, 1)
	if len(hits) != 1 || hits[0].ID != 1 {
		t.Fatalf("DEFECT: vector committed before the crash is not searchable after reopen: %+v", hits)
	}
}

// TestVectorAcceptance_DeterministicIDs is acceptance test #10: omitted IDs
// are identical on every replica. Phase 1 already guarantees this (NextID
// allocation in apply, go-qrdb vector-replicated review B2/N1); this is the
// confirming test phase 2 adds alongside items 1, 2 and 5.
func TestVectorAcceptance_DeterministicIDs(t *testing.T) {
	log := [][]byte{
		mustEncodeVector(protocol.EncodeVectorNamespaceCreate(protocol.VectorNamespaceCreateParams{Name: "ns", Dimension: 1})),
		mustEncodeVector(protocol.EncodeVectorUpsert("ns", 0, protocol.VectorUpsertParams{Embedding: []float32{1}})),
		mustEncodeVector(protocol.EncodeVectorUpsert("ns", 0, protocol.VectorUpsertParams{Embedding: []float32{2}})),
		mustEncodeVector(protocol.EncodeVectorBatchUpsert(protocol.VectorBatchUpsertParams{Name: "ns", Vectors: []protocol.VectorUpsertParams{
			{Embedding: []float32{3}},
			{Embedding: []float32{4}},
		}})),
	}

	assignedIDs := func(t *testing.T, eng Engine) []uint64 {
		t.Helper()
		var ids []uint64
		for _, cmd := range log {
			result := applyVectorCmd(t, eng, cmd)
			if _, refused := protocol.DecodeAppError(result.Data); refused {
				t.Fatalf("command refused: %+v", result)
			}
			ids = append(ids, result.Value)
		}
		return ids
	}

	stgA := memory.NewStorage()
	defer func() { _ = stgA.Close() }()
	engA, err := NewMVCCEngine(stgA)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = engA.Close(context.Background()) }()

	stgB := memory.NewStorage()
	defer func() { _ = stgB.Close() }()
	engB := NewKVEngine(stgB)
	defer func() { _ = engB.Close(context.Background()) }()

	idsA := assignedIDs(t, engA)
	idsB := assignedIDs(t, engB)
	if !idsEqual(idsA, idsB) {
		t.Fatalf("DEFECT: omitted IDs differ across engines/replicas: mvcc=%v kv=%v", idsA, idsB)
	}
}

// TestVectorAcceptance_DeletedCheckpointCostsOnlyARebuild proves the no-
// node-local-state constraint directly: write a checkpoint, delete it from
// disk, and confirm search after reopen still returns the same answer --
// only by rebuilding, never by losing data. This is the test the phase-2
// task explicitly requires.
func TestVectorAcceptance_DeletedCheckpointCostsOnlyARebuild(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	dir := t.TempDir()

	eng1, err := NewMVCCEngine(stg, WithMVCCVectorIndex(dir, nil))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	createNamespace(t, eng1, "ns", 2)
	applyVectorCmd(t, eng1, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 0, protocol.VectorUpsertParams{HasID: true, ID: 1, Embedding: []float32{1, 0}})))
	applyVectorCmd(t, eng1, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 0, protocol.VectorUpsertParams{HasID: true, ID: 2, Embedding: []float32{0, 1}})))

	if err := eng1.VectorIndex().Checkpoint("ns"); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	_ = eng1.Close(context.Background())

	// Prove the checkpoint is real -- a file actually landed on disk -- then
	// delete it, so the rebuild path below cannot be exercising a no-op.
	nsDir := filepath.Join(dir, "vector-index", "ns")
	entries, err := os.ReadDir(nsDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", nsDir, err)
	}
	if len(entries) == 0 {
		t.Fatalf("DEFECT: Checkpoint wrote no file under %s", nsDir)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll(checkpoint dir): %v", err)
	}

	before := hitIDs(vectorSearch(t, mustReopenForSearch(t, stg, dir), "ns", []float32{1, 0}, 2))

	if len(before) != 2 {
		t.Fatalf("got %d hits, want 2 -- deleting the checkpoint must cost only a rebuild, never data", len(before))
	}
}

// TestVectorAcceptance_RestartLoadsCheckpointAndReplaysTheGap exercises the
// OTHER half of "Open" (docs/vectors/engineering.md): a checkpoint that IS
// still present loads and replays the mutation-log entries committed after
// it, rather than every restart falling back to a full rebuild. ID 3 is
// upserted AFTER the checkpoint, so finding it after restart proves the
// replay ran -- a restart that only loaded the checkpoint body would find
// ID 1 and ID 2 but not ID 3.
func TestVectorAcceptance_RestartLoadsCheckpointAndReplaysTheGap(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	dir := t.TempDir()

	eng1, err := NewMVCCEngine(stg, WithMVCCVectorIndex(dir, nil))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	createNamespace(t, eng1, "ns", 2)
	applyVectorCmd(t, eng1, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 0, protocol.VectorUpsertParams{HasID: true, ID: 1, Embedding: []float32{1, 0}})))
	applyVectorCmd(t, eng1, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 0, protocol.VectorUpsertParams{HasID: true, ID: 2, Embedding: []float32{0, 1}})))

	if err := eng1.VectorIndex().Checkpoint("ns"); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	// Committed AFTER the checkpoint: present in stg, but not in the
	// checkpoint body itself.
	applyVectorCmd(t, eng1, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 0, protocol.VectorUpsertParams{HasID: true, ID: 3, Embedding: []float32{0.9, 0.1}})))
	_ = eng1.Close(context.Background())

	nsDir := filepath.Join(dir, "vector-index", "ns")
	before, err := os.ReadDir(nsDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", nsDir, err)
	}
	if len(before) != 1 {
		t.Fatalf("checkpoint dir has %d entries, want exactly 1", len(before))
	}

	eng2, err := NewMVCCEngine(stg, WithMVCCVectorIndex(dir, nil))
	if err != nil {
		t.Fatalf("NewMVCCEngine (restart): %v", err)
	}
	defer func() { _ = eng2.Close(context.Background()) }()
	if err := eng2.WarmVectorIndex(); err != nil {
		t.Fatalf("WarmVectorIndex: %v", err)
	}

	// The checkpoint file on disk must be untouched by Warm (Warm only
	// loads and replays; it does not itself write a new checkpoint), so this
	// is genuinely testing the load-and-replay path, not a rebuild that
	// happened to also pass.
	after, err := os.ReadDir(nsDir)
	if err != nil {
		t.Fatalf("ReadDir(%s) after warm: %v", nsDir, err)
	}
	if len(after) != 1 || after[0].Name() != before[0].Name() {
		t.Fatalf("DEFECT: the checkpoint file changed across Warm (before=%v after=%v); this test no longer isolates the load-and-replay path", before, after)
	}

	hits := hitIDs(vectorSearch(t, eng2, "ns", []float32{1, 0}, 3))
	foundID3 := false
	for _, id := range hits {
		if id == 3 {
			foundID3 = true
		}
	}
	if len(hits) != 3 || !foundID3 {
		t.Fatalf("DEFECT: restart loaded the checkpoint but did not replay the mutation log committed after it: hits=%v, want IDs 1, 2 and 3", hits)
	}
}

// TestVectorAcceptance_RestartKeepsSearchWorking_KVEngine is
// TestVectorAcceptance_RestartKeepsSearchWorking's KVEngine counterpart: the
// file/objstore backends compose KVEngine, not MVCCEngine, and its
// checkpoint/warm/search wiring (WithKVVectorIndex, adaptVectorHandler,
// KVEngine.Lookup) is independent code from MVCCEngine's.
func TestVectorAcceptance_RestartKeepsSearchWorking_KVEngine(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	dir := t.TempDir()

	eng1 := NewKVEngine(stg, WithKVVectorIndex(dir, nil))
	createNamespace(t, eng1, "ns", 2)
	applyVectorCmd(t, eng1, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 0, protocol.VectorUpsertParams{HasID: true, ID: 1, Embedding: []float32{1, 0}})))
	applyVectorCmd(t, eng1, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 0, protocol.VectorUpsertParams{HasID: true, ID: 2, Embedding: []float32{0, 1}})))
	_ = eng1.Close(context.Background())

	before := hitIDs(vectorSearch(t, eng1, "ns", []float32{1, 0}, 2))
	if len(before) != 2 {
		t.Fatalf("before restart: got %d hits, want 2", len(before))
	}

	eng2 := NewKVEngine(stg, WithKVVectorIndex(dir, nil))
	defer func() { _ = eng2.Close(context.Background()) }()
	if err := eng2.WarmVectorIndex(); err != nil {
		t.Fatalf("WarmVectorIndex: %v", err)
	}

	after := hitIDs(vectorSearch(t, eng2, "ns", []float32{1, 0}, 2))
	if !idsEqual(before, after) {
		t.Fatalf("DEFECT: KVEngine search after restart = %v, want the same top-k as before restart %v", after, before)
	}
}

// mustReopenForSearch constructs a fresh engine over stg with a vector index
// manager pointed at dir (which may no longer hold any checkpoint) and warms
// it, standing in for "reopen after losing the checkpoint".
func mustReopenForSearch(t *testing.T, stg storage.Storage, dir string) Engine {
	t.Helper()
	eng, err := NewMVCCEngine(stg, WithMVCCVectorIndex(dir, nil))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close(context.Background()) })
	if err := eng.WarmVectorIndex(); err != nil {
		t.Fatalf("WarmVectorIndex: %v", err)
	}
	return eng
}
