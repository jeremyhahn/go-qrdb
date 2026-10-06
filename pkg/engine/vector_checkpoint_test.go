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
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/vector"
)

func TestCheckpointBody_RoundTrips(t *testing.T) {
	params := map[string]string{"m": "16", "ef": "200"}
	body, err := encodeCheckpointBody(42, vector.IndexHNSW, params, 7, []byte("payload-bytes"))
	if err != nil {
		t.Fatalf("encodeCheckpointBody: %v", err)
	}
	hdr, err := decodeCheckpointBody(body)
	if err != nil {
		t.Fatalf("decodeCheckpointBody: %v", err)
	}
	if hdr.version != checkpointFormatVersion {
		t.Fatalf("version = %d, want %d", hdr.version, checkpointFormatVersion)
	}
	if hdr.seq != 42 {
		t.Fatalf("seq = %d, want 42", hdr.seq)
	}
	if hdr.indexType != vector.IndexHNSW {
		t.Fatalf("indexType = %d, want %d", hdr.indexType, vector.IndexHNSW)
	}
	if hdr.generation != 7 {
		t.Fatalf("generation = %d, want 7", hdr.generation)
	}
	if hdr.params["m"] != "16" || hdr.params["ef"] != "200" {
		t.Fatalf("params = %v, want %v", hdr.params, params)
	}
	if string(hdr.payload) != "payload-bytes" {
		t.Fatalf("payload = %q, want %q", hdr.payload, "payload-bytes")
	}
}

func TestDecodeCheckpointBody_TooShortIsRefused(t *testing.T) {
	if _, err := decodeCheckpointBody(make([]byte, 5)); err == nil {
		t.Fatal("decodeCheckpointBody(too short) = nil error, want one")
	}
}

func TestDecodeCheckpointBody_TruncatedParamBlockIsRefused(t *testing.T) {
	// A valid 18-byte header with no param block bytes following it: long
	// enough to pass the length floor, too short for ReadParamBlock.
	data := make([]byte, 18)
	if _, err := decodeCheckpointBody(data); err == nil {
		t.Fatal("decodeCheckpointBody(truncated param block) = nil error, want one")
	}
}

func TestVectorIndexManager_LoadCheckpoint_NoDirectoryConfigured(t *testing.T) {
	m := NewVectorIndexManager("", nil)
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	rec := vector.NamespaceRecord{Dimension: 1, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW}
	live, err := vector.NewLiveIndex("ns", rec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if m.loadCheckpoint(stg, "ns", rec, live) {
		t.Fatal("loadCheckpoint with no directory configured = true, want false")
	}
}

func TestVectorIndexManager_LoadCheckpoint_NoCheckpointFile(t *testing.T) {
	m := NewVectorIndexManager(t.TempDir(), nil)
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	rec := vector.NamespaceRecord{Dimension: 1, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW}
	live, err := vector.NewLiveIndex("ns", rec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if m.loadCheckpoint(stg, "ns", rec, live) {
		t.Fatal("loadCheckpoint with no checkpoint file = true, want false")
	}
}

func TestVectorIndexManager_LoadCheckpoint_CorruptFileFallsBackToFalse(t *testing.T) {
	dir := t.TempDir()
	m := NewVectorIndexManager(dir, nil)
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	rec := vector.NamespaceRecord{Dimension: 1, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW}

	nsDir := filepath.Join(dir, "vector-index", "ns")
	if err := os.MkdirAll(nsDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nsDir, "00000000000000000001.idx"), []byte("not a checkpoint"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	live, err := vector.NewLiveIndex("ns", rec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if m.loadCheckpoint(stg, "ns", rec, live) {
		t.Fatal("loadCheckpoint with a corrupt file = true, want false")
	}
}

func TestVectorIndexManager_LoadCheckpoint_StaleGeometryIsRejected(t *testing.T) {
	dir := t.TempDir()
	m := NewVectorIndexManager(dir, nil)
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	rec := vector.NamespaceRecord{Dimension: 1, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW, TrainingGeneration: 2}
	live, err := vector.NewLiveIndex("ns", rec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if err := live.Upsert(1, []float32{1}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := m.writeCheckpoint("ns", rec, live); err != nil {
		t.Fatalf("writeCheckpoint: %v", err)
	}

	// A record whose training generation has moved on since the checkpoint
	// was written: the checkpoint is for the OLD geometry and must be
	// rejected, not loaded as if it matched.
	newerRec := rec
	newerRec.TrainingGeneration = 3
	live2, err := vector.NewLiveIndex("ns", newerRec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if m.loadCheckpoint(stg, "ns", newerRec, live2) {
		t.Fatal("loadCheckpoint with a stale training generation = true, want false")
	}
}

func TestVectorIndexManager_LoadCheckpoint_SeqAheadOfRecordIsRejected(t *testing.T) {
	dir := t.TempDir()
	m := NewVectorIndexManager(dir, nil)
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	rec := vector.NamespaceRecord{Dimension: 1, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW, Seq: 10}
	live, err := vector.NewLiveIndex("ns", rec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if err := live.Upsert(1, []float32{1}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := m.writeCheckpoint("ns", rec, live); err != nil {
		t.Fatalf("writeCheckpoint: %v", err)
	}

	staleRec := rec
	staleRec.Seq = 5 // behind the checkpoint's own sequence
	live2, err := vector.NewLiveIndex("ns", staleRec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if m.loadCheckpoint(stg, "ns", staleRec, live2) {
		t.Fatal("loadCheckpoint with seq ahead of the record = true, want false")
	}
}

func TestVectorIndexManager_LoadCheckpoint_ReplayGapFallsBack(t *testing.T) {
	dir := t.TempDir()
	m := NewVectorIndexManager(dir, nil)
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	rec := vector.NamespaceRecord{Dimension: 1, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW, Seq: 1}
	live, err := vector.NewLiveIndex("ns", rec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if err := live.Upsert(1, []float32{1}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := m.writeCheckpoint("ns", rec, live); err != nil {
		t.Fatalf("writeCheckpoint: %v", err)
	}

	// The record has advanced past the checkpoint's sequence, but stg has NO
	// mutation-log entry for the intervening sequence (it was trimmed, or in
	// this test simply never written) -- a replay gap.
	aheadRec := rec
	aheadRec.Seq = 2
	live2, err := vector.NewLiveIndex("ns", aheadRec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if m.loadCheckpoint(stg, "ns", aheadRec, live2) {
		t.Fatal("loadCheckpoint across a replay gap = true, want false")
	}
}

func TestVectorIndexManager_LoadCheckpoint_ExactSequenceSkipsReplay(t *testing.T) {
	dir := t.TempDir()
	m := NewVectorIndexManager(dir, nil)
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	rec := vector.NamespaceRecord{Dimension: 1, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW, Seq: 3}
	live, err := vector.NewLiveIndex("ns", rec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if err := live.Upsert(1, []float32{1}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := m.writeCheckpoint("ns", rec, live); err != nil {
		t.Fatalf("writeCheckpoint: %v", err)
	}

	live2, err := vector.NewLiveIndex("ns", rec)
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if !m.loadCheckpoint(stg, "ns", rec, live2) {
		t.Fatal("loadCheckpoint at the checkpoint's own sequence = false, want true")
	}
	if got := live2.Count(); got != 1 {
		t.Fatalf("Count after load = %d, want 1", got)
	}
}

func TestReplayMutationLog_SkipsAnIDSinceDeletedAndUpsertsTheLatestEmbedding(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	// Seq 1: upsert ID 1 (ends up deleted -- log says upsert, but the
	// current record is absent by the time replay runs).
	if err := stg.Set(vector.MutationLogKey("ns", 1), vector.EncodeMutationLogEntry(vector.MutationLogEntry{Op: vector.MutationUpsert, ID: 1})); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Seq 2: upsert ID 2, which IS still present with its final embedding.
	if err := stg.Set(vector.MutationLogKey("ns", 2), vector.EncodeMutationLogEntry(vector.MutationLogEntry{Op: vector.MutationUpsert, ID: 2})); err != nil {
		t.Fatalf("Set: %v", err)
	}
	rec := vector.VectorRecord{Embedding: []float32{9}}
	data, err := vector.EncodeVectorRecord(rec)
	if err != nil {
		t.Fatalf("EncodeVectorRecord: %v", err)
	}
	if err := stg.Set(vector.VectorKey("ns", 2), data); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Seq 3: delete ID 1 explicitly too.
	if err := stg.Set(vector.MutationLogKey("ns", 3), vector.EncodeMutationLogEntry(vector.MutationLogEntry{Op: vector.MutationDelete, ID: 1})); err != nil {
		t.Fatalf("Set: %v", err)
	}

	live, err := vector.NewLiveIndex("ns", vector.NamespaceRecord{Dimension: 1, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW})
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if err := replayMutationLog(stg, "ns", 0, 3, live); err != nil {
		t.Fatalf("replayMutationLog: %v", err)
	}
	if got := live.Count(); got != 1 {
		t.Fatalf("Count after replay = %d, want 1 (only ID 2)", got)
	}
	hits, err := live.Search([]float32{9}, 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != 2 {
		t.Fatalf("Search = %+v, want ID 2", hits)
	}
}

func TestReplayMutationLog_MissingLogEntryIsAReplayGap(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	live, err := vector.NewLiveIndex("ns", vector.NamespaceRecord{Dimension: 1, DistanceMetric: vector.DistanceCosine, IndexType: vector.IndexHNSW})
	if err != nil {
		t.Fatalf("NewLiveIndex: %v", err)
	}
	if err := replayMutationLog(stg, "ns", 0, 5, live); err == nil {
		t.Fatal("replayMutationLog across a missing entry = nil error, want one")
	}
}
