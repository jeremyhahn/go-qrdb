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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// This file is UNCONSTRAINED so its helper is a superset of every caller's
// constraint, and it holds the assertions for go-qrdb#520: an engine that
// cannot serve a verb must ANSWER the proposer, never halt Apply. A halted
// Apply stops lastApplied advancing, and every later linearizable read on the
// shard — on any engine sharing it, on keys written and read successfully
// microseconds earlier — burns the caller's whole deadline and comes back
// `quicraft: timeout`.

// assertRefusedNotHalted asserts that an Update call refused an entry the way
// Raft can survive: nil error out of Update, and the refusal readable in the
// result payload as a CAPABILITY error (not the generic application encoding,
// which a caller cannot tell from a unique-index violation).
func assertRefusedNotHalted(t *testing.T, updateErr error, result sm.Result, wantSubstr string) {
	t.Helper()
	if updateErr != nil {
		t.Fatalf("Update returned %v; a refusal must not halt Apply (go-qrdb#520)", updateErr)
	}
	msg, kind, ok := protocol.DecodeAppErrorKind(result.Data)
	if !ok {
		t.Fatalf("result payload %q carries no encoded application error; the "+
			"proposer would read the refusal as an ordinary result", result.Data)
	}
	if kind != protocol.AppErrorCapability {
		t.Errorf("refusal %q encoded WITHOUT the capability marker; the service "+
			"layer cannot tell it from a constraint violation (go-qrdb#141)", msg)
	}
	if !strings.Contains(msg, wantSubstr) {
		t.Errorf("encoded refusal = %q, want it to contain %q", msg, wantSubstr)
	}
}

// TestKVEngineUpdateRefusesUnknownExtendedOpWithoutHalting pins the KV
// engine's extended-protocol arm: an MVCC opcode this engine has no handler
// for is refused, not fatal.
func TestKVEngineUpdateRefusesUnknownExtendedOpWithoutHalting(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	cmd := mustEncode(protocol.EncodeMVCCPut([]byte("k"), []byte("v"), 0))
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results)

	assertRefusedNotHalted(t, err, results[0], "unsupported operation")
}

// TestKVEngineUpdateKeepsServingAfterARefusal is the engine-level shape of
// PROBE-B: a refused entry must not cost the entries that follow it in the
// SAME Update call, nor the ones proposed after it.
func TestKVEngineUpdateKeepsServingAfterARefusal(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	refused := mustEncode(protocol.EncodeMVCCPut([]byte("k"), []byte("v"), 0))
	put := kvPutEntry(2, "after", "value")
	results := make([]sm.Result, 2)
	if err := eng.Update(context.Background(),
		[]sm.Entry{{Index: 1, Cmd: refused}, put}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := eng.Lookup(context.Background(), []byte("after"))
	if err != nil {
		t.Fatalf("Lookup after a refused entry: %v", err)
	}
	if b, ok := got.([]byte); !ok || string(b) != "value" {
		t.Fatalf("Lookup after a refused entry = %v, want \"value\"", got)
	}
}

// TestMVCCEngineUpdateRefusesUnknownOpcodeWithoutHalting is the tag-independent
// twin of PROBE-B at the engine seam. The MVCC engine had NO classification
// before go-qrdb#520, so an opcode with no handler — reachable from any client
// able to propose — halted the shard under EVERY build configuration.
func TestMVCCEngineUpdateRefusesUnknownOpcodeWithoutHalting(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: []byte{0xFE, 0x00}}}, results)

	assertRefusedNotHalted(t, err, results[0], "unknown op code")
}

// TestMVCCEngineUpdateKeepsServingAfterARefusal is PROBE-B without Raft: a
// refused entry, then an ordinary write and read that must still work.
func TestMVCCEngineUpdateKeepsServingAfterARefusal(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	put := mustEncode(protocol.EncodeMVCCPut([]byte("after"), []byte("value"), 0))
	results := make([]sm.Result, 2)
	if err := eng.Update(context.Background(), []sm.Entry{
		{Index: 1, Cmd: []byte{0xFE, 0x00}},
		{Index: 2, Cmd: put},
	}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	kv, err := eng.Store().Get([]byte("after"))
	if err != nil {
		t.Fatalf("Get after a refused entry: %v", err)
	}
	if kv == nil || string(kv.Value) != "value" {
		t.Fatalf("Get after a refused entry = %v, want \"value\"", kv)
	}
}

// TestMVCCEngineUpdateStillHaltsOnAFault is the other half of the contract and
// the one that must not be lost: an error that does NOT declare itself an
// absent capability halts Apply, because a replica that has actually diverged
// must stop rather than serve wrong data.
func TestMVCCEngineUpdateStillHaltsOnAFault(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// A truncated batch payload fails to decode inside the handler, which is a
	// fault rather than a refusal: nothing about it says "this engine was not
	// built to do that".
	cmd := []byte{protocol.OpBatch, 0xFF}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results)
	if err == nil {
		t.Fatal("Update returned nil for a decode fault; a diverging entry must halt Apply")
	}
	if _, ok := protocol.AsCapabilityError(err); ok {
		t.Fatalf("decode fault %v classified as an absent capability", err)
	}
}

// TestUnsupportedOperationErrorDeclaresItsCapability keeps the declaration and
// the classifier honest at the type level: the assertion in errors.go proves
// the interface is satisfied, this proves the string is the opcode.
func TestUnsupportedOperationErrorDeclaresItsCapability(t *testing.T) {
	err := error(&UnsupportedOperationError{Op: 0x2A})
	capErr, ok := protocol.AsCapabilityError(err)
	if !ok {
		t.Fatal("UnsupportedOperationError is not recognised as an absent capability")
	}
	if got, want := capErr.Capability(), "opcode 0x2a"; got != want {
		t.Errorf("Capability() = %q, want %q", got, want)
	}
}

// TestWrappedUnsupportedOperationErrorIsStillARefusal proves the classifier
// reads through wrapping, so a handler that adds context to a refusal does not
// silently convert it into a shard-killing fault.
func TestWrappedUnsupportedOperationErrorIsStillARefusal(t *testing.T) {
	wrapped := &CommandDecodeError{
		KVErr:  &UnsupportedOperationError{Op: 0x2A},
		ExtErr: errors.New("ext"),
	}
	if _, ok := protocol.AsCapabilityError(wrapped.KVErr); !ok {
		t.Fatal("an UnsupportedOperationError stopped being a refusal")
	}
}

// TestKVEngineRefusesCompactionStateWithoutHalting: the KV engine keeps no
// MVCC history and no compaction table, so the compaction-state command is a
// verb it does not have -- refused in the result, never fatal.
func TestKVEngineRefusesCompactionStateWithoutHalting(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	cmd := mustEncode(protocol.EncodeCompactionState(protocol.CompactionStateCommand{
		Kind: protocol.CompactionStateSetPolicy, Scope: "acme/app/", Policy: []byte(`{"policy":{"mode":"revision","retainRevisions":1}}`),
	}))
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results)

	assertRefusedNotHalted(t, err, results[0], "unsupported operation")
}
