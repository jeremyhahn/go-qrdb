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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// This file is UNCONSTRAINED so its helper is a superset of every caller's
// constraint. It holds the go-qrdb#520 assertions for the Pebble state
// machine: an opcode it has no handler for must be ANSWERED, never returned to
// Raft, because a halted Apply costs the shard every subsequent linearizable
// read.

// assertRefusedNotHalted asserts an Update refused an entry the way Raft can
// survive: nil error, and the refusal readable in the result payload as a
// capability error.
func assertRefusedNotHalted(t *testing.T, updateErr error, result sm.Result, wantSubstr string) {
	t.Helper()
	if updateErr != nil {
		t.Fatalf("Update returned %v; a refusal must not halt Apply (go-qrdb#520)", updateErr)
	}
	msg, capability, ok := protocol.DecodeAppErrorKind(result.Data)
	if !ok {
		t.Fatalf("result payload %q carries no encoded application error", result.Data)
	}
	if !capability {
		t.Errorf("refusal %q encoded WITHOUT the capability marker", msg)
	}
	if !strings.Contains(msg, wantSubstr) {
		t.Errorf("encoded refusal = %q, want it to contain %q", msg, wantSubstr)
	}
}

// TestPebbleStateMachineRefusesUnknownOpcodeWithoutHalting pins the refusal
// path directly.
func TestPebbleStateMachineRefusesUnknownOpcodeWithoutHalting(t *testing.T) {
	s := openStore(t)
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: []byte{0xFE, 0x00}}}, results)

	assertRefusedNotHalted(t, err, results[0], "unknown op code")
}

// TestPebbleStateMachineKeepsServingAfterARefusal is PROBE-B's shape without
// Raft: a refused entry must not cost the entries applied after it, nor the
// reads that follow.
func TestPebbleStateMachineKeepsServingAfterARefusal(t *testing.T) {
	s := openStore(t)
	put, err := kv.EncodePut([]byte("after"), []byte("value"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}

	results := make([]sm.Result, 2)
	if err := s.Update(context.Background(), []sm.Entry{
		{Index: 1, Cmd: []byte{0xFE, 0x00}},
		{Index: 2, Cmd: put},
	}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Lookup(context.Background(), []byte("after"))
	if err != nil {
		t.Fatalf("Lookup after a refused entry: %v", err)
	}
	if b, ok := got.([]byte); !ok || string(b) != "value" {
		t.Fatalf("Lookup after a refused entry = %v, want \"value\"", got)
	}
}

// TestPebbleStateMachineStillHaltsOnAFault is the half of the contract that
// must not be lost: an error that does not declare itself an absent capability
// halts Apply, so a replica that has actually diverged stops instead of
// serving wrong data.
func TestPebbleStateMachineStillHaltsOnAFault(t *testing.T) {
	s := openStore(t)
	results := make([]sm.Result, 1)

	// An empty command is malformed input to the decoder, not a verb this
	// state machine lacks.
	err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: nil}}, results)
	if err == nil {
		t.Fatal("Update returned nil for a malformed entry; a fault must halt Apply")
	}
	if _, ok := protocol.AsCapabilityError(err); ok {
		t.Fatalf("malformed entry %v classified as an absent capability", err)
	}
}
