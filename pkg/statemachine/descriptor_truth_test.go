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
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// A Descriptor's Capabilities list is what a caller consults to decide whether
// an operation is available on a given sm_type. When it over-claims, a clean
// "not supported here" becomes a runtime failure at the worst possible moment,
// and a whole class of behaviour looks like a conformance gap rather than a
// capability that was never built (go-qrdb#135, found by the #121 matrix).
//
// Every registry factory constructs its state machine with ZERO options, so
// the optional subsystems — MVCC store, lease manager, index manager,
// time-series store — are all nil. WithKVMVCCStore has no production caller
// anywhere in this repository. The declarations said otherwise.
//
// This test measures each capability by EXERCISING it and compares the result
// against the declaration. The yardstick has to be behaviour: a test that read
// the descriptor and checked it against a second hand-written list of expected
// capabilities would agree with itself no matter what the constructors did,
// which is precisely how the descriptions drifted from the code in the first
// place.

// probeOp is the operation that decides whether one capability is really
// present, and how to read the answer.
//
// For the optional subsystems the handler checks its manager for nil BEFORE
// decoding the payload, so an empty payload separates the two cases cleanly: a
// missing subsystem yields ServiceNotConfiguredError, while a present one gets
// as far as the decoder and fails there instead. For kv and batch the payload
// is real, because those are backend-backed and must actually succeed.
type probeOp struct {
	cap Capability
	cmd func() []byte
}

func capabilityProbes(t *testing.T) []probeOp {
	t.Helper()
	mustCmd := func(b []byte, err error) []byte {
		t.Helper()
		if err != nil {
			t.Fatalf("encode probe command: %v", err)
		}
		return b
	}
	return []probeOp{
		{CapabilityKV, func() []byte {
			return mustCmd(kv.EncodePut([]byte("probe"), []byte("v")))
		}},
		{CapabilityBatch, func() []byte {
			op := mustCmd(protocol.EncodeOp(kv.OpPut, []byte("probe"), []byte("v"), nil, 0))
			return mustCmd(protocol.EncodeBatch([][]byte{op}))
		}},
		{CapabilityMVCC, func() []byte { return []byte{protocol.OpMVCCPut} }},
		{CapabilityTransaction, func() []byte { return []byte{protocol.OpTransaction} }},
		{CapabilityLease, func() []byte { return []byte{protocol.OpLeaseGrant} }},
		{CapabilityIndex, func() []byte { return []byte{protocol.OpIndexCreate} }},
		{CapabilityTimeSeries, func() []byte { return []byte{protocol.OpTSWrite} }},
	}
}

// applyProbe runs one entry through whichever state machine shape the registry
// produced and reports whether the capability is genuinely present.
//
// Absent means the state machine refused for lack of the subsystem: either an
// encoded "<service> not configured" application error, or no handler for the
// opcode at all. Anything else — success, or a failure from the decoder that
// only runs once the subsystem check has passed — means the capability is
// wired.
func applyProbe(t *testing.T, machine interface{}, cmd []byte) (present bool) {
	t.Helper()
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	var err error
	switch m := machine.(type) {
	case interface {
		Update(context.Context, []sm.Entry, []sm.Result) error
	}:
		err = m.Update(context.Background(), entries, results)
	case interface {
		Update([]sm.Entry, []sm.Result) error
	}:
		err = m.Update(entries, results)
	default:
		t.Fatalf("state machine %T exposes no recognised Update", machine)
	}

	if msg, ok := protocol.DecodeAppError(results[0].Data); ok {
		if strings.Contains(msg, "not configured") {
			return false
		}
	}
	var opErr *InvalidOpcodeError
	if errors.As(err, &opErr) {
		return false
	}
	var sncErr *ServiceNotConfiguredError
	if errors.As(err, &sncErr) {
		return false
	}
	return true
}

// TestRegistryDescriptorsMatchBehaviour is the acceptance test for go-qrdb#135:
// every capability a registered state machine DECLARES must be one it can
// actually perform.
//
// It deliberately checks every capability on every registration rather than the
// two the issue named. A descriptor that was wrong about two things may be
// wrong about a third, and a sweep built only from the findings already in hand
// can only rediscover those.
func TestRegistryDescriptorsMatchBehaviour(t *testing.T) {
	probes := capabilityProbes(t)

	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			desc, err := Describe(name)
			if err != nil {
				t.Fatalf("Describe(%q): %v", name, err)
			}
			declared := make(map[Capability]bool, len(desc.Capabilities))
			for _, c := range desc.Capabilities {
				declared[c] = true
			}

			for _, p := range probes {
				// A fresh state machine per probe: an earlier probe's write must
				// not decide a later one's answer.
				createFn, resolveErr := ResolveTyped(name, "")
				if resolveErr != nil {
					t.Fatalf("ResolveTyped(%q): %v", name, resolveErr)
				}
				machine := createFn(1, 1)

				present := applyProbe(t, machine, p.cmd())
				if declared[p.cap] && !present {
					t.Errorf("sm_type %q DECLARES %s but cannot perform it — a caller that "+
						"trusts this descriptor gets a runtime failure instead of a clean "+
						"'not supported here'", name, p.cap)
				}
				if !declared[p.cap] && present {
					t.Errorf("sm_type %q performs %s but does not declare it — a caller is "+
						"denied a capability the state machine has", name, p.cap)
				}
			}
		})
	}
}
