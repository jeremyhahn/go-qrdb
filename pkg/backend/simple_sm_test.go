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

package backend

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestSSM() *SimpleStateMachine {
	return NewSimpleStateMachine(memory.NewStorage(), nil)
}

func newTestSSMWithHub() (*SimpleStateMachine, *watch.Hub) {
	hub := watch.NewHub()
	return NewSimpleStateMachine(memory.NewStorage(), hub), hub
}

func applyPut(t *testing.T, ssm *SimpleStateMachine, key, value string) {
	t.Helper()
	cmd, err := kv.EncodePut([]byte(key), []byte(value))
	if err != nil {
		t.Fatalf("EncodePut(%q, %q): %v", key, value, err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := ssm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(put %q): %v", key, err)
	}
	if results[0].Value != uint64(len(value)) {
		t.Fatalf("Update(put %q): expected result.Value=%d, got %d", key, len(value), results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// Update tests
// ---------------------------------------------------------------------------

func TestSimpleStateMachine_Update_Put(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	cmd, err := kv.EncodePut([]byte("k1"), []byte("v1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := ssm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if results[0].Value != uint64(len("v1")) {
		t.Fatalf("expected result.Value=%d, got %d", len("v1"), results[0].Value)
	}
}

func TestSimpleStateMachine_Update_Put_Readable(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	applyPut(t, ssm, "k1", "v1")

	val, err := ssm.Lookup(context.Background(), "k1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	got, ok := val.([]byte)
	if !ok {
		t.Fatalf("expected []byte, got %T", val)
	}
	if !bytes.Equal(got, []byte("v1")) {
		t.Fatalf("expected %q, got %q", "v1", got)
	}
}

func TestSimpleStateMachine_Update_Delete(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	applyPut(t, ssm, "k1", "v1")

	cmd, err := kv.EncodeDelete([]byte("k1"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	entries := []sm.Entry{{Index: 2, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := ssm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(delete): %v", err)
	}
}

func TestSimpleStateMachine_Update_Delete_KeyGone(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	applyPut(t, ssm, "k1", "v1")

	cmd, err := kv.EncodeDelete([]byte("k1"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	entries := []sm.Entry{{Index: 2, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := ssm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(delete): %v", err)
	}

	val, err := ssm.Lookup(context.Background(), "k1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != nil {
		t.Fatalf("expected nil after delete, got %v", val)
	}
}

func TestSimpleStateMachine_Update_MultipleBatch(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	cmdA, err := kv.EncodePut([]byte("a"), []byte("1"))
	if err != nil {
		t.Fatalf("EncodePut(a): %v", err)
	}
	cmdB, err := kv.EncodePut([]byte("b"), []byte("22"))
	if err != nil {
		t.Fatalf("EncodePut(b): %v", err)
	}
	cmdC, err := kv.EncodePut([]byte("c"), []byte("333"))
	if err != nil {
		t.Fatalf("EncodePut(c): %v", err)
	}

	entries := []sm.Entry{
		{Index: 1, Cmd: cmdA},
		{Index: 2, Cmd: cmdB},
		{Index: 3, Cmd: cmdC},
	}
	results := make([]sm.Result, 3)

	if err := ssm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update batch: %v", err)
	}

	if results[0].Value != 1 || results[1].Value != 2 || results[2].Value != 3 {
		t.Fatalf("unexpected results: %v", results)
	}
}

func TestSimpleStateMachine_Update_MultipleBatch_AllReadable(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	cmdA, err := kv.EncodePut([]byte("a"), []byte("1"))
	if err != nil {
		t.Fatalf("EncodePut(a): %v", err)
	}
	cmdB, err := kv.EncodePut([]byte("b"), []byte("22"))
	if err != nil {
		t.Fatalf("EncodePut(b): %v", err)
	}

	entries := []sm.Entry{
		{Index: 1, Cmd: cmdA},
		{Index: 2, Cmd: cmdB},
	}
	results := make([]sm.Result, 2)

	if err := ssm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update batch: %v", err)
	}

	for _, key := range []string{"a", "b"} {
		if _, err := ssm.Lookup(context.Background(), key); err != nil {
			t.Fatalf("Lookup(%q) after batch: %v", key, err)
		}
	}
}

func TestSimpleStateMachine_Update_InvalidCommand(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	// Too short to decode.
	entries := []sm.Entry{{Index: 1, Cmd: []byte{0x01}}}
	results := make([]sm.Result, 1)

	err := ssm.Update(context.Background(), entries, results)
	// Invalid commands are skipped, not fatal.
	if err != nil {
		t.Fatalf("expected nil error for invalid command, got %v", err)
	}
	// Refused in the result, where a proposer reads a refusal: Value:1 alone
	// is what a successful write looks like to KVService.
	if _, refused := protocol.DecodeAppError(results[0].Data); !refused {
		t.Fatalf("expected an application-error refusal for an invalid command, got %+v", results[0])
	}
}

func TestSimpleStateMachine_Update_InvalidCommand_EmptyCmd(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	entries := []sm.Entry{{Index: 1, Cmd: nil}}
	results := make([]sm.Result, 1)

	err := ssm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error for nil command, got %v", err)
	}
	if _, refused := protocol.DecodeAppError(results[0].Data); !refused {
		t.Fatalf("expected an application-error refusal for a nil command, got %+v", results[0])
	}
}

// SimpleStateMachine does not serve guarded writes (protocol.OpGuarded). A
// guarded write it receives must be refused, never acknowledged unwritten.
func TestSimpleStateMachine_Update_GuardedWriteIsRefused(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	c, err := protocol.EncodeCompare([]byte("fence"), protocol.CompareTargetValue, protocol.CompareResultEqual, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("EncodeCompare: %v", err)
	}
	put, err := kv.EncodePut([]byte("k"), []byte("v"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	cmd, err := protocol.EncodeGuarded([][]byte{c}, put)
	if err != nil {
		t.Fatalf("EncodeGuarded: %v", err)
	}
	results := make([]sm.Result, 1)
	if err := ssm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	msg, refused := protocol.DecodeAppError(results[0].Data)
	if !refused {
		t.Fatalf("a guarded write was acknowledged by a state machine that cannot evaluate it: %+v", results[0])
	}
	if msg == "" {
		t.Fatal("the refusal must say why")
	}
}

func TestSimpleStateMachine_Update_UnsupportedOp(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	// OpGet (0x02) is a valid wire op but not supported in Update.
	cmd, err := kv.EncodeGet([]byte("k1"))
	if err != nil {
		t.Fatalf("EncodeGet: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	// Refused in the result payload, not returned to Raft: an opcode this
	// state machine does not implement says nothing about the health of its
	// storage, and returning it halted the shard (go-qrdb#520).
	assertRefusedNotHalted(t, ssm.Update(context.Background(), entries, results),
		results[0], "unsupported operation")
}

// TestSimpleStateMachine_Update_UnsupportedOp_KeepsServing is the local shape
// of go-qrdb#520: a refused entry must not cost the entries applied after it.
func TestSimpleStateMachine_Update_UnsupportedOp_KeepsServing(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	refused, err := kv.EncodeGet([]byte("k1"))
	if err != nil {
		t.Fatalf("EncodeGet: %v", err)
	}
	put, err := kv.EncodePut([]byte("after"), []byte("value"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: refused}, {Index: 2, Cmd: put}}
	results := make([]sm.Result, 2)

	if err := ssm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := ssm.Lookup(context.Background(), []byte("after"))
	if err != nil {
		t.Fatalf("Lookup after a refused entry: %v", err)
	}
	if b, ok := got.([]byte); !ok || string(b) != "value" {
		t.Fatalf("Lookup after a refused entry = %v, want \"value\"", got)
	}
}

// ---------------------------------------------------------------------------
// Lookup tests
// ---------------------------------------------------------------------------

func TestSimpleStateMachine_Lookup_ByteSlice(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	applyPut(t, ssm, "k1", "v1")

	val, err := ssm.Lookup(context.Background(), []byte("k1"))
	if err != nil {
		t.Fatalf("Lookup([]byte): %v", err)
	}
	got := val.([]byte)
	if !bytes.Equal(got, []byte("v1")) {
		t.Fatalf("expected %q, got %q", "v1", got)
	}
}

func TestSimpleStateMachine_Lookup_String(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	applyPut(t, ssm, "k1", "v1")

	val, err := ssm.Lookup(context.Background(), "k1")
	if err != nil {
		t.Fatalf("Lookup(string): %v", err)
	}
	got := val.([]byte)
	if !bytes.Equal(got, []byte("v1")) {
		t.Fatalf("expected %q, got %q", "v1", got)
	}
}

func TestSimpleStateMachine_Lookup_ListQuery(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	applyPut(t, ssm, "app/k1", "v1")
	applyPut(t, ssm, "app/k2", "v2")
	applyPut(t, ssm, "other/k3", "v3")

	val, err := ssm.Lookup(context.Background(), kv.ListQuery{Prefix: "app/"})
	if err != nil {
		t.Fatalf("Lookup(ListQuery): %v", err)
	}
	keys := val.([]string)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys with prefix 'app/', got %d: %v", len(keys), keys)
	}
}

func TestSimpleStateMachine_Lookup_ListQuery_EmptyPrefix(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	applyPut(t, ssm, "a", "1")
	applyPut(t, ssm, "b", "2")

	val, err := ssm.Lookup(context.Background(), kv.ListQuery{Prefix: ""})
	if err != nil {
		t.Fatalf("Lookup(ListQuery empty): %v", err)
	}
	keys := val.([]string)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
}

func TestSimpleStateMachine_Lookup_UnsupportedType(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	_, err := ssm.Lookup(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error for unsupported lookup type")
	}
	var lte *LookupTypeError
	if !errors.As(err, &lte) {
		t.Fatalf("expected LookupTypeError, got %T: %v", err, err)
	}
}

func TestSimpleStateMachine_Lookup_UnsupportedType_Struct(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	type custom struct{ X int }
	_, err := ssm.Lookup(context.Background(), custom{X: 1})
	if err == nil {
		t.Fatal("expected error for custom struct lookup")
	}
	var lte *LookupTypeError
	if !errors.As(err, &lte) {
		t.Fatalf("expected LookupTypeError, got %T: %v", err, err)
	}
}

func TestSimpleStateMachine_Lookup_KeyNotFound(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	val, err := ssm.Lookup(context.Background(), "missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != nil {
		t.Fatalf("expected nil for missing key, got %v", val)
	}
}

func TestSimpleStateMachine_Lookup_KeyNotFound_ByteSlice(t *testing.T) {
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	val, err := ssm.Lookup(context.Background(), []byte("missing"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != nil {
		t.Fatalf("expected nil for missing byte slice key, got %v", val)
	}
}

// ---------------------------------------------------------------------------
// Close tests
// ---------------------------------------------------------------------------

func TestSimpleStateMachine_Close(t *testing.T) {
	ssm := newTestSSM()
	if err := ssm.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSimpleStateMachine_Close_EngineClosedAfter(t *testing.T) {
	ssm := newTestSSM()
	if err := ssm.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := ssm.Lookup(context.Background(), "k1")
	if err == nil {
		t.Fatal("expected error after Close")
	}
}

// ---------------------------------------------------------------------------
// Watch notification tests
// ---------------------------------------------------------------------------

func TestSimpleStateMachine_WatchNotification_Put(t *testing.T) {
	ssm, hub := newTestSSMWithHub()
	defer func() { _ = ssm.Close(context.Background()) }()
	defer hub.Close()

	w, err := hub.Watch(context.Background(), "k1")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	applyPut(t, ssm, "k1", "v1")

	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected at least one event")
		}
		if resp.Events[0].Type != watch.EventPut {
			t.Fatalf("expected EventPut, got %v", resp.Events[0].Type)
		}
	default:
		// Hub notification is synchronous inline, so event should be ready.
		t.Fatal("expected notification, got none")
	}
}

func TestSimpleStateMachine_WatchNotification_Delete(t *testing.T) {
	ssm, hub := newTestSSMWithHub()
	defer func() { _ = ssm.Close(context.Background()) }()
	defer hub.Close()

	applyPut(t, ssm, "k1", "v1")

	w, err := hub.Watch(context.Background(), "k1")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	cmd, err := kv.EncodeDelete([]byte("k1"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	entries := []sm.Entry{{Index: 2, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := ssm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(delete): %v", err)
	}

	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected at least one event")
		}
		if resp.Events[0].Type != watch.EventDelete {
			t.Fatalf("expected EventDelete, got %v", resp.Events[0].Type)
		}
	default:
		t.Fatal("expected notification, got none")
	}
}

func TestSimpleStateMachine_NoHub_NoPanic(t *testing.T) {
	// SimpleStateMachine with nil hub should not panic on Put/Delete.
	ssm := newTestSSM()
	defer func() { _ = ssm.Close(context.Background()) }()

	applyPut(t, ssm, "k1", "v1")

	cmd, err := kv.EncodeDelete([]byte("k1"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	entries := []sm.Entry{{Index: 2, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := ssm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(delete without hub): %v", err)
	}
}

func TestSimpleStateMachine_NoHub_PutSucceeds(t *testing.T) {
	ssm := NewSimpleStateMachine(memory.NewStorage(), nil)
	defer func() { _ = ssm.Close(context.Background()) }()

	applyPut(t, ssm, "x", "y")
	val, err := ssm.Lookup(context.Background(), "x")
	if err != nil {
		t.Fatalf("Lookup after put without hub: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("y")) {
		t.Fatalf("expected 'y', got %q", val)
	}
}
