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

package statemachine

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// This file covers go-qrdb#592 F5: the Apply-time key-length refusal must
// be gated, not unconditional, so a rolling upgrade cannot make a new
// replica refuse a key an old peer in the same group just applied.

// overLengthPutCmd builds a single-op OpBatch command carrying a key over
// protocol.MaxClientKeyLength. kv.OpPut's own wire format frames its key in
// 2 bytes and so cannot even NAME a key over 65,535 bytes; rawKeyOp's
// 4-byte frame can, which is exactly why validateClientKey's length check
// is needed as a backstop independent of any particular encoder (see its
// own doc comment) and is what this test must drive it through.
func overLengthPutCmd(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, protocol.MaxClientKeyLength+1)
	for i := range key {
		key[i] = 'k'
	}
	cmd, err := protocol.EncodeBatch([][]byte{rawKeyOp(protocol.OpMVCCPut, key, []byte("v"))})
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestKVStateMachine_KeyLengthCap_DefaultEnforced(t *testing.T) {
	k := NewKVStateMachine(NewStandardBackend())
	res := make([]sm.Result, 1)
	if err := k.Update([]sm.Entry{{Index: 1, Cmd: overLengthPutCmd(t)}}, res); err != nil {
		t.Fatalf("Update returned a Go error, which halts Apply: %v", err)
	}
	if _, refused := protocol.DecodeAppError(res[0].Data); !refused {
		t.Fatal("no gate wired: the over-cap key should be refused (the single-binary default)")
	}
}

func TestKVStateMachine_KeyLengthCap_GateDisablesRefusal(t *testing.T) {
	k := NewKVStateMachine(NewStandardBackend(), WithKVKeyLengthCapGate(func() bool { return false }))
	res := make([]sm.Result, 1)
	if err := k.Update([]sm.Entry{{Index: 1, Cmd: overLengthPutCmd(t)}}, res); err != nil {
		t.Fatalf("Update returned a Go error, which halts Apply: %v", err)
	}
	if _, refused := protocol.DecodeAppError(res[0].Data); refused {
		t.Fatal("gate reporting the feature inactive must match an old replica that never checked length: the write must apply")
	}
}

func TestKVStateMachine_KeyLengthCap_GateEnablesRefusal(t *testing.T) {
	k := NewKVStateMachine(NewStandardBackend(), WithKVKeyLengthCapGate(func() bool { return true }))
	res := make([]sm.Result, 1)
	if err := k.Update([]sm.Entry{{Index: 1, Cmd: overLengthPutCmd(t)}}, res); err != nil {
		t.Fatalf("Update returned a Go error, which halts Apply: %v", err)
	}
	if _, refused := protocol.DecodeAppError(res[0].Data); !refused {
		t.Fatal("gate reporting the feature active must refuse the over-cap key")
	}
}

// TestKVStateMachine_KeyLengthCap_GateIsReadFreshPerApply confirms the
// gate's own doc promise: it is called fresh every Apply, not cached at
// construction, so a feature that activates mid-run (as cluster-wide
// activation does, permanently, per pkg/cluster/featuregate) takes effect
// starting with the very next entry.
func TestKVStateMachine_KeyLengthCap_GateIsReadFreshPerApply(t *testing.T) {
	active := false
	k := NewKVStateMachine(NewStandardBackend(), WithKVKeyLengthCapGate(func() bool { return active }))

	res := make([]sm.Result, 1)
	if err := k.Update([]sm.Entry{{Index: 1, Cmd: overLengthPutCmd(t)}}, res); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, refused := protocol.DecodeAppError(res[0].Data); refused {
		t.Fatal("inactive gate refused the key on the first entry")
	}

	active = true
	if err := k.Update([]sm.Entry{{Index: 2, Cmd: overLengthPutCmd(t)}}, res); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, refused := protocol.DecodeAppError(res[0].Data); !refused {
		t.Fatal("gate flipped active but the second entry was still accepted")
	}
}

// Empty and reserved keys are refused regardless of the gate: F5 gates
// ONLY the length check.
func TestKVStateMachine_KeyLengthCap_GateDoesNotAffectEmptyOrReservedKey(t *testing.T) {
	k := NewKVStateMachine(NewStandardBackend(), WithKVKeyLengthCapGate(func() bool { return false }))

	emptyCmd := append([]byte{1, 0, 0}, []byte("v")...) // kv.OpPut, keyLen=0
	res := make([]sm.Result, 1)
	if err := k.Update([]sm.Entry{{Index: 1, Cmd: emptyCmd}}, res); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, refused := protocol.DecodeAppError(res[0].Data); !refused {
		t.Fatal("an empty key must be refused regardless of the key-length gate")
	}

	reserved := []byte(diskMetaKeyLastApplied)
	reservedCmd := append([]byte{1, byte(len(reserved)), byte(len(reserved) >> 8)}, reserved...)
	reservedCmd = append(reservedCmd, 'v')
	if err := k.Update([]sm.Entry{{Index: 2, Cmd: reservedCmd}}, res); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, refused := protocol.DecodeAppError(res[0].Data); !refused {
		t.Fatal("a reserved key must be refused regardless of the key-length gate")
	}
}

// RegularStateMachine's gate is independent of KVStateMachine's.
func TestRegularStateMachine_KeyLengthCap_GateDisablesRefusal(t *testing.T) {
	r := NewRegularStateMachine(WithRegularKeyLengthCapGate(func() bool { return false }))
	res := make([]sm.Result, 1)
	if err := r.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: overLengthPutCmd(t)}}, res); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, refused := protocol.DecodeAppError(res[0].Data); refused {
		t.Fatal("gate reporting the feature inactive must let the write apply")
	}
}

func TestRegularStateMachine_KeyLengthCap_DefaultEnforced(t *testing.T) {
	r := NewRegularStateMachine()
	res := make([]sm.Result, 1)
	if err := r.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: overLengthPutCmd(t)}}, res); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, refused := protocol.DecodeAppError(res[0].Data); !refused {
		t.Fatal("no gate wired: the over-cap key should be refused (the single-binary default)")
	}
}
