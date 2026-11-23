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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
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
