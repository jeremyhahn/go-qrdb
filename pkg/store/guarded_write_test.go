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

package store

import (
	"context"
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// guardedEntry wraps inner with "fence is absent".
func guardedEntry(t *testing.T, index uint64, fence string, inner []byte) sm.Entry {
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

// kvPutEntry is a base KV put, the command the drop fence is written with.
func kvPutEntry(t *testing.T, index uint64, key, value string) sm.Entry {
	t.Helper()
	cmd, err := kv.EncodePut([]byte(key), []byte(value))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	return sm.Entry{Index: index, Cmd: cmd}
}

// pebbleHas reports whether key is present in s.
func pebbleHas(t *testing.T, s *PebbleStateMachine, key string) bool {
	t.Helper()
	_, closer, err := s.db.Get([]byte(key))
	if errors.Is(err, pebble.ErrNotFound) {
		return false
	}
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = closer.Close()
	return true
}

// The fence and the write it refuses are in ONE Update call here, which is
// the case that matters: the guard must see the fence written by an earlier
// entry of the same batch -- the entry's log position -- not the state
// committed before the batch, or two replicas that packed the log into
// batches differently would disagree.
func TestPebbleStateMachine_GuardedWriteSeesTheFenceAtItsLogPosition(t *testing.T) {
	s := newTestPebbleSM(t)
	entries := []sm.Entry{
		guardedEntry(t, 1, "fence", mustEncode(protocol.EncodeMVCCPut([]byte("early"), []byte("v"), 0))),
		kvPutEntry(t, 2, "fence", "dropping"),
		guardedEntry(t, 3, "fence", mustEncode(protocol.EncodeMVCCPut([]byte("late"), []byte("v"), 0))),
	}
	results := make([]sm.Result, len(entries))
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if _, refused := protocol.DecodeGuardRefusal(results[0].Data); refused {
		t.Fatal("the write before the fence was refused")
	}
	if key, refused := protocol.DecodeGuardRefusal(results[2].Data); !refused || string(key) != "fence" {
		t.Fatalf("the write after the fence: Result.Data = %x, want a guard refusal", results[2].Data)
	}
	if !pebbleHas(t, s, "early") {
		t.Fatal("the write before the fence did not apply")
	}
	if pebbleHas(t, s, "late") {
		t.Fatal("the write after the fence applied")
	}
}

// A base KV command -- which pebble decodes WITH its opcode -- is dispatched
// correctly from inside a guard, and a guard-less command applies
// unconditionally.
func TestPebbleStateMachine_GuardedBaseKVCommandAndGuardlessCommand(t *testing.T) {
	s := newTestPebbleSM(t)
	entries := []sm.Entry{
		guardedEntry(t, 1, "fence", mustEncode(kv.EncodePut([]byte("a"), []byte("v")))),
		kvPutEntry(t, 2, "fence", "dropping"),
		kvPutEntry(t, 3, "b", "v"),
	}
	results := make([]sm.Result, len(entries))
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !pebbleHas(t, s, "a") {
		t.Fatal("a guarded base KV put whose guard held did not apply")
	}
	if !pebbleHas(t, s, "b") {
		t.Fatal("a guard-less put after the fence did not apply: an absent guard must mean unconditional")
	}
}

// A malformed guard halts apply like any other undecodable committed entry,
// and an inner opcode pebble does not serve is answered as a refusal.
func TestPebbleStateMachine_GuardedDecodeFaultAndUnknownInner(t *testing.T) {
	s := newTestPebbleSM(t)
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: []byte{protocol.OpGuarded, 1}}}, results); err == nil {
		t.Fatal("a malformed guarded entry applied without error")
	}

	s = newTestPebbleSM(t)
	if err := s.Update(context.Background(), []sm.Entry{guardedEntry(t, 1, "fence", []byte{250, 0})}, results); err != nil {
		t.Fatalf("an unknown inner opcode must be answered, not fatal: %v", err)
	}
	if _, refused := protocol.DecodeAppError(results[0].Data); !refused {
		t.Fatalf("Result.Data = %x, want a refusal", results[0].Data)
	}
}

// The MVCC state machine (the "mvcc" sm_type, over pkg/engine's MVCCEngine)
// honours the same contract at the same log position.
func TestMVCCStateMachine_GuardedWriteSeesTheFenceAtItsLogPosition(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	entries := []sm.Entry{
		kvPutEntry(t, 1, "fence", "dropping"),
		guardedEntry(t, 2, "fence", mustEncode(protocol.EncodeMVCCPut([]byte("late"), []byte("v"), 0))),
		kvPutEntry(t, 3, "unguarded", "v"),
	}
	results := make([]sm.Result, len(entries))
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if key, refused := protocol.DecodeGuardRefusal(results[1].Data); !refused || string(key) != "fence" {
		t.Fatalf("Result.Data = %x, want a guard refusal", results[1].Data)
	}
	if _, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("late")}); err == nil {
		t.Fatal("the write after the fence applied")
	}
	if _, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("unguarded")}); err != nil {
		t.Fatalf("a guard-less put did not apply: %v", err)
	}
}
