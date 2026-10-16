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
	"bytes"
	"context"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/vector"
)

// deleteRangeEntry encodes an OpDeleteRange entry over [start, end).
func deleteRangeEntry(t *testing.T, idx uint64, start, end string) sm.Entry {
	t.Helper()
	var endBytes []byte
	if end != "" {
		endBytes = []byte(end)
	}
	cmd, err := protocol.EncodeDeleteRange([]byte(start), endBytes)
	if err != nil {
		t.Fatalf("EncodeDeleteRange: %v", err)
	}
	return sm.Entry{Index: idx, Cmd: cmd}
}

// rawDeleteRangeEntry encodes an OpDeleteRange entry without the encoder's
// own checks, so a test can commit bounds a proposer would never send.
func rawDeleteRangeEntry(t *testing.T, idx uint64, start, end []byte) sm.Entry {
	t.Helper()
	cmd, err := protocol.EncodeOp(protocol.OpDeleteRange, start, nil, end, 0)
	if err != nil {
		t.Fatalf("EncodeOp: %v", err)
	}
	return sm.Entry{Index: idx, Cmd: cmd}
}

// assertRefused fails unless result is an encoded application refusal.
func assertRefused(t *testing.T, result sm.Result) {
	t.Helper()
	if _, ok := protocol.DecodeAppError(result.Data); !ok {
		t.Fatalf("expected an encoded refusal, got result=%+v", result)
	}
}

// ---------------------------------------------------------------------------
// deleteRangeOps
// ---------------------------------------------------------------------------

func TestDeleteRangeOps_SortsAndTypes(t *testing.T) {
	ops := deleteRangeOps(protocol.OpMVCCDelete, [][]byte{[]byte("bb"), []byte("a"), []byte("b")})
	want := []string{"a", "b", "bb"}
	if len(ops) != len(want) {
		t.Fatalf("len(ops) = %d, want %d", len(ops), len(want))
	}
	for i, op := range ops {
		if op.Type != protocol.OpMVCCDelete || string(op.Key) != want[i] {
			t.Fatalf("ops[%d] = {%#x %q}, want {%#x %q}", i, op.Type, op.Key, protocol.OpMVCCDelete, want[i])
		}
	}
}

func TestDeleteRangeOps_Empty(t *testing.T) {
	if ops := deleteRangeOps(protocol.OpFSMDelete, nil); len(ops) != 0 {
		t.Fatalf("deleteRangeOps(nil) = %v, want none", ops)
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine
// ---------------------------------------------------------------------------

func seedMVCC(t *testing.T, eng *MVCCEngine, keys ...string) {
	t.Helper()
	entries := make([]sm.Entry, len(keys))
	for i, k := range keys {
		entries[i] = mvccPutEntry(uint64(i+1), []byte(k), []byte("v-"+k), 0)
	}
	applyMVCCEntries(t, eng, entries)
}

func mvccLive(t *testing.T, eng *MVCCEngine, key string) bool {
	t.Helper()
	kv, err := eng.Store().Get([]byte(key))
	return err == nil && kv != nil
}

func TestMVCCEngine_DeleteRange_DeletesOnlyTheRangeAtOneRevision(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()
	// "b" and "bz" and "c0" differ in length, so the range spans several of
	// the metadata namespace's per-length runs.
	seedMVCC(t, eng, "a", "b", "bz", "c0", "d")
	before := eng.Store().CurrentRevision()

	results := applyMVCCEntries(t, eng, []sm.Entry{deleteRangeEntry(t, 10, "b", "d")})
	if _, refused := protocol.DecodeAppError(results[0].Data); refused {
		t.Fatalf("range delete refused: %+v", results[0])
	}
	for _, k := range []string{"b", "bz", "c0"} {
		if mvccLive(t, eng, k) {
			t.Errorf("key %q in range survived", k)
		}
	}
	for _, k := range []string{"a", "d"} {
		if !mvccLive(t, eng, k) {
			t.Errorf("key %q outside the range was deleted", k)
		}
	}
	if got := eng.Store().CurrentRevision(); got != before+1 {
		t.Fatalf("revision = %d, want %d: a range delete is one revision", got, before+1)
	}
}

func TestMVCCEngine_DeleteRange_UnboundedEnd(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()
	seedMVCC(t, eng, "a", "m", "mm", "z")

	applyMVCCEntries(t, eng, []sm.Entry{deleteRangeEntry(t, 10, "m", "")})
	if !mvccLive(t, eng, "a") {
		t.Error("key below the range was deleted")
	}
	for _, k := range []string{"m", "mm", "z"} {
		if mvccLive(t, eng, k) {
			t.Errorf("key %q at or above start survived an unbounded range delete", k)
		}
	}
}

func TestMVCCEngine_DeleteRange_EmptyRangeSucceeds(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()
	seedMVCC(t, eng, "a")
	before := eng.Store().CurrentRevision()

	results := applyMVCCEntries(t, eng, []sm.Entry{deleteRangeEntry(t, 10, "x", "y")})
	if _, refused := protocol.DecodeAppError(results[0].Data); refused {
		t.Fatalf("an empty range was refused: %+v", results[0])
	}
	if got := eng.Store().CurrentRevision(); got != before {
		t.Fatalf("revision = %d, want %d: deleting nothing must not advance it", got, before)
	}
}

func TestMVCCEngine_DeleteRange_SeesEarlierEntriesOfTheSameApply(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("p/1"), []byte("v"), 0),
		deleteRangeEntry(t, 2, "p/", "p0"),
	})
	if mvccLive(t, eng, "p/1") {
		t.Fatal("a key put earlier in the same apply survived the range delete")
	}
}

func TestMVCCEngine_DeleteRange_RefusesInvalidRangeWithoutHalting(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()
	seedMVCC(t, eng, "b")

	results := applyMVCCEntries(t, eng, []sm.Entry{rawDeleteRangeEntry(t, 10, []byte("c"), []byte("a"))})
	assertRefused(t, results[0])
	if !mvccLive(t, eng, "b") {
		t.Fatal("a refused range delete deleted a key")
	}
}

func TestMVCCEngine_DeleteRange_RefusesReservedStart(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	results := applyMVCCEntries(t, eng, []sm.Entry{rawDeleteRangeEntry(t, 1, []byte{0x00}, []byte("a"))})
	assertRefused(t, results[0])
}

// ---------------------------------------------------------------------------
// KVEngine
// ---------------------------------------------------------------------------

func newSeededKV(t *testing.T, keys ...string) (*KVEngine, *memory.MemoryStorage) {
	t.Helper()
	stg := memory.NewStorage()
	eng := NewKVEngine(stg)
	entries := make([]sm.Entry, len(keys))
	for i, k := range keys {
		entries[i] = kvPutEntry(uint64(i+1), k, "v-"+k)
	}
	applyKVEntries(t, eng, entries)
	return eng, stg
}

func kvLive(t *testing.T, stg storage.Storage, key string) bool {
	t.Helper()
	val, err := stg.Get([]byte(key))
	return err == nil && val != nil
}

func TestKVEngine_DeleteRange_DeletesOnlyTheRange(t *testing.T) {
	eng, stg := newSeededKV(t, "a", "b", "bz", "c0", "d")
	defer func() { _ = stg.Close() }()

	results := applyKVEntries(t, eng, []sm.Entry{deleteRangeEntry(t, 10, "b", "d")})
	if _, refused := protocol.DecodeAppError(results[0].Data); refused {
		t.Fatalf("range delete refused: %+v", results[0])
	}
	if results[0].Value != 3 {
		t.Fatalf("result Value = %d, want 3 keys deleted", results[0].Value)
	}
	for _, k := range []string{"b", "bz", "c0"} {
		if kvLive(t, stg, k) {
			t.Errorf("key %q in range survived", k)
		}
	}
	for _, k := range []string{"a", "d"} {
		if !kvLive(t, stg, k) {
			t.Errorf("key %q outside the range was deleted", k)
		}
	}
}

func TestKVEngine_DeleteRange_UnboundedEnd(t *testing.T) {
	eng, stg := newSeededKV(t, "a", "m", "z")
	defer func() { _ = stg.Close() }()

	applyKVEntries(t, eng, []sm.Entry{deleteRangeEntry(t, 10, "m", "")})
	if !kvLive(t, stg, "a") {
		t.Error("key below the range was deleted")
	}
	for _, k := range []string{"m", "z"} {
		if kvLive(t, stg, k) {
			t.Errorf("key %q at or above start survived an unbounded range delete", k)
		}
	}
}

func TestKVEngine_DeleteRange_RefusesInvalidRangeWithoutHalting(t *testing.T) {
	eng, stg := newSeededKV(t, "b")
	defer func() { _ = stg.Close() }()

	results := applyKVEntries(t, eng, []sm.Entry{rawDeleteRangeEntry(t, 10, []byte("b"), []byte("b"))})
	assertRefused(t, results[0])
	if !kvLive(t, stg, "b") {
		t.Fatal("a refused range delete deleted a key")
	}
}

func TestKVEngine_DeleteRange_RefusesReservedStart(t *testing.T) {
	eng, stg := newSeededKV(t)
	defer func() { _ = stg.Close() }()
	if err := stg.Set([]byte{0x00, 'x'}, []byte("engine record")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	results := applyKVEntries(t, eng, []sm.Entry{rawDeleteRangeEntry(t, 1, []byte{0x00}, []byte("a"))})
	assertRefused(t, results[0])
	if val, _ := stg.Get([]byte{0x00, 'x'}); val == nil {
		t.Fatal("a reserved record was deleted despite the refusal")
	}
}

func TestKVEngine_DeleteRange_LeavesVectorRecords(t *testing.T) {
	eng, stg := newSeededKV(t, "__a")
	defer func() { _ = stg.Close() }()
	forged := []byte(vector.KeyPrefix + "ns")
	if err := stg.Set(forged, []byte("vector record")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	results := applyKVEntries(t, eng, []sm.Entry{deleteRangeEntry(t, 10, "__", "__z")})
	if _, refused := protocol.DecodeAppError(results[0].Data); refused {
		t.Fatalf("a range over vector records was refused: %+v", results[0])
	}
	if results[0].Value != 1 {
		t.Fatalf("result Value = %d, want 1: only the client key is deleted", results[0].Value)
	}
	if val, _ := stg.Get(forged); !bytes.Equal(val, []byte("vector record")) {
		t.Fatal("a vector record was deleted by a range delete")
	}
	if kvLive(t, stg, "__a") {
		t.Fatal("the client key in the range survived")
	}
}
