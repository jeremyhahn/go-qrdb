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
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// guardedEntryFor wraps inner with "fence is absent".
func guardedEntryFor(t *testing.T, index uint64, fence string, inner []byte) sm.Entry {
	t.Helper()
	c, err := protocol.EncodeCompare([]byte(fence), protocol.CompareTargetVersion, protocol.CompareResultEqual, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("EncodeCompare: %v", err)
	}
	cmd, err := protocol.EncodeGuarded([][]byte{c}, inner)
	if err != nil {
		t.Fatalf("EncodeGuarded: %v", err)
	}
	return sm.Entry{Index: index, Cmd: cmd}
}

// TestKVEngine_GuardedWriteContract pins the engine a standalone node
// serves from: a guarded write applies while the guard holds, is refused
// without halting once the fence is present, and a guard-less put still
// applies unconditionally.
func TestKVEngine_GuardedWriteContract(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)
	ctx := context.Background()

	entries := []sm.Entry{
		guardedEntryFor(t, 1, "fence", mustEncode(kv.EncodePut([]byte("early"), []byte("v")))),
		kvPutEntry(2, "fence", "dropping"),
		guardedEntryFor(t, 3, "fence", mustEncode(kv.EncodePut([]byte("late"), []byte("v")))),
		kvPutEntry(4, "unguarded", "v"),
	}
	results := make([]sm.Result, len(entries))
	if err := eng.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if key, refused := protocol.DecodeGuardRefusal(results[2].Data); !refused || string(key) != "fence" {
		t.Fatalf("Result.Data = %x, want a guard refusal naming %q", results[2].Data, "fence")
	}
	for key, want := range map[string]bool{"early": true, "late": false, "unguarded": true} {
		got, err := stg.Get([]byte(key))
		if err != nil {
			t.Fatalf("Get(%q): %v", key, err)
		}
		if (got != nil) != want {
			t.Errorf("%q present = %v, want %v", key, got != nil, want)
		}
	}
}

// TestKVEngine_GuardedExtendedCommandAndFaults covers the inner dispatch of
// an extended-protocol command, a read fault during the guard, and an inner
// command this engine does not serve.
func TestKVEngine_GuardedExtendedCommandAndFaults(t *testing.T) {
	ctx := context.Background()

	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)
	cas := mustEncode(protocol.EncodeCompareAndSwap([]byte("k"), []byte("v"), []byte("w")))
	results := make([]sm.Result, 2)
	if err := eng.Update(ctx, []sm.Entry{kvPutEntry(1, "k", "v"), guardedEntryFor(t, 2, "fence", cas)}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if results[1].Value != protocol.ResultSwapped {
		t.Fatalf("guarded CompareAndSwap result = %d, want ResultSwapped", results[1].Value)
	}

	faulty := &readFaultStorage{Storage: memory.NewStorage(), failKey: "fence"}
	eng = NewKVEngine(faulty)
	err := eng.Update(ctx, []sm.Entry{guardedEntryFor(t, 1, "fence", mustEncode(kv.EncodePut([]byte("k"), []byte("v"))))}, make([]sm.Result, 1))
	if !errors.Is(err, errReadFault) {
		t.Fatalf("Update = %v, want the guard's read fault propagated", err)
	}

	eng = NewKVEngine(memory.NewStorage())
	res := make([]sm.Result, 1)
	if err := eng.Update(ctx, []sm.Entry{guardedEntryFor(t, 1, "fence", mustEncode(protocol.EncodeMVCCPut([]byte("k"), []byte("v"), 0)))}, res); err != nil {
		t.Fatalf("an inner opcode the engine does not serve must be answered, not fatal: %v", err)
	}
	if _, refused := protocol.DecodeAppError(res[0].Data); !refused {
		t.Fatalf("Result.Data = %x, want a refusal", res[0].Data)
	}
}

// TestMVCCEngine_GuardedWriteContract is the same contract for the MVCC
// engine, whose guard reads through the apply batch.
func TestMVCCEngine_GuardedWriteContract(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	ctx := context.Background()

	entries := []sm.Entry{
		guardedEntryFor(t, 1, "fence", mustEncode(protocol.EncodeMVCCPut([]byte("early"), []byte("v"), 0))),
		kvPutEntry(2, "fence", "dropping"),
		guardedEntryFor(t, 3, "fence", mustEncode(protocol.EncodeMVCCPut([]byte("late"), []byte("v"), 0))),
		kvPutEntry(4, "unguarded", "v"),
	}
	results := make([]sm.Result, len(entries))
	if err := eng.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if key, refused := protocol.DecodeGuardRefusal(results[2].Data); !refused || string(key) != "fence" {
		t.Fatalf("Result.Data = %x, want a guard refusal naming %q", results[2].Data, "fence")
	}
	for key, want := range map[string]bool{"early": true, "late": false, "unguarded": true} {
		_, gerr := eng.Store().Get([]byte(key))
		if (gerr == nil) != want {
			t.Errorf("%q present = %v, want %v", key, gerr == nil, want)
		}
	}
}

// TestMVCCEngine_GuardedFaultsAndEmptyFence covers a read fault during the
// guard, an unserved inner opcode, and a fence stored with an EMPTY value --
// presence, not content, is what fences.
func TestMVCCEngine_GuardedFaultsAndEmptyFence(t *testing.T) {
	ctx := context.Background()

	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()
	faulty := &mvccReadFaultStorage{Storage: inner}
	eng, err := NewMVCCEngine(faulty)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	faulty.armed = true
	uerr := eng.Update(ctx, []sm.Entry{guardedEntryFor(t, 1, "fence", mustEncode(protocol.EncodeMVCCPut([]byte("k"), []byte("v"), 0)))}, make([]sm.Result, 1))
	faulty.armed = false
	if !errors.Is(uerr, errMVCCReadFault) {
		t.Fatalf("Update = %v, want the guard's read fault propagated", uerr)
	}

	eng, err = NewMVCCEngine(memory.NewStorage())
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	res := make([]sm.Result, 3)
	entries := []sm.Entry{
		guardedEntryFor(t, 1, "fence", []byte{250, 0}),
		kvPutEntry(2, "fence", ""),
		guardedEntryFor(t, 3, "fence", mustEncode(protocol.EncodeMVCCPut([]byte("k"), []byte("v"), 0))),
	}
	if err := eng.Update(ctx, entries, res); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, refused := protocol.DecodeAppError(res[0].Data); !refused {
		t.Fatalf("unserved inner opcode: Result.Data = %x, want a refusal", res[0].Data)
	}
	if _, refused := protocol.DecodeGuardRefusal(res[2].Data); !refused {
		t.Fatal("a fence holding an empty value did not refuse the write")
	}
}
