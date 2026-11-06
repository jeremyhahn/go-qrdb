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
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestRegisterStateMachines pins what every composition root gets from one
// call: both state machines resolvable by name, building the right type, and
// described as a node composes them — MVCC declared only where revisions are
// kept.
func TestRegisterStateMachines(t *testing.T) {
	RegisterStateMachines()

	for name, want := range map[string]func(interface{}) bool{
		"pebble": func(m interface{}) bool { _, ok := m.(*PebbleStateMachine); return ok },
		"mvcc":   func(m interface{}) bool { _, ok := m.(*MVCCStateMachine); return ok },
	} {
		createFn, err := statemachine.Resolve(name)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", name, err)
		}
		machine := createFn(1, 1)
		if !want(machine) {
			t.Fatalf("%q built %T", name, machine)
		}
		_ = machine.(sm.DiskStateMachine).Close(context.Background())
	}

	pebbleDesc, err := statemachine.Describe("pebble")
	if err != nil {
		t.Fatal(err)
	}
	if pebbleDesc.Has(statemachine.CapabilityMVCC) || !pebbleDesc.Has(statemachine.CapabilityTransaction) {
		t.Errorf("pebble keeps no revisions but serves transactions: %+v", pebbleDesc.Capabilities)
	}
	mvccDesc, err := statemachine.Describe("mvcc")
	if err != nil {
		t.Fatal(err)
	}
	if !mvccDesc.Has(statemachine.CapabilityMVCC) || mvccDesc.StorageGeneric || pebbleDesc.StorageGeneric {
		t.Errorf("mvcc keeps revisions and both carry their own storage: %+v", mvccDesc)
	}
}

// composedAsTheDaemonComposesIt builds the named state machine with the
// subsystems cmd/qrdbd wires into it — a lessor, a watch hub and time-series
// storage — because that composition is what the descriptor describes.
func composedAsTheDaemonComposesIt(t *testing.T, name string, hub *watch.Hub) sm.DiskStateMachine {
	t.Helper()
	lr := lease.NewLessor()
	// A node without an at-rest key: its encryptor is nil and leaves the
	// filesystem as it is.
	pebbleOpts := []Option{WithEncryptor(nil)}
	var createFn sm.CreateFunc
	switch name {
	case "pebble":
		createFn = NewPebbleCreateFuncWith(pebbleOpts, func(s *PebbleStateMachine) {
			s.SetLessor(lr)
			s.SetWatchHub(hub)
			s.SetTimeSeriesEnabled(true)
		})
	case "mvcc":
		createFn = NewMVCCCreateFuncWith(pebbleOpts, WithLessor(lr), WithWatchHub(hub), WithTimeSeriesEnabled())
	default:
		t.Fatalf("no daemon composition for %q", name)
	}
	machine := createFn(1, 1).(sm.DiskStateMachine)
	if _, err := machine.Open(context.Background(), t.TempDir(), nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = machine.Close(context.Background()) })
	return machine
}

// performs applies cmd and reports whether the state machine carried it out
// rather than refusing it.
func performs(t *testing.T, machine sm.DiskStateMachine, index uint64, cmd []byte) bool {
	t.Helper()
	res := make([]sm.Result, 1)
	if err := machine.Update(context.Background(), []sm.Entry{{Index: index, Cmd: cmd}}, res); err != nil {
		return false
	}
	_, refused := protocol.DecodeAppError(res[0].Data)
	return !refused
}

// TestStoreDescriptorsMatchBehaviour holds PebbleDescriptor and MVCCDescriptor
// to what the state machines DO, capability by capability, as
// pkg/statemachine's TestRegistryDescriptorsMatchBehaviour holds the registry
// ones: a node's capability report is narrowed by these descriptors, so a
// declaration that over- or under-claims is a wrong report.
func TestStoreDescriptorsMatchBehaviour(t *testing.T) {
	for name, desc := range map[string]statemachine.Descriptor{"pebble": PebbleDescriptor(), "mvcc": MVCCDescriptor()} {
		t.Run(name, func(t *testing.T) {
			hub := watch.NewHub()
			t.Cleanup(func() { hub.Close() })
			machine := composedAsTheDaemonComposesIt(t, name, hub)

			w, err := hub.Watch(context.Background(), "watched")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = w.Close() })

			must := func(b []byte, err error) []byte {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			txnCmp := must(protocol.EncodeCompare([]byte("txn"), 0, 0, nil, 0, 0, 0))
			txnOp := must(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txn"), []byte("v"), nil, 0))
			batchOp := must(protocol.EncodeOp(protocol.OpMVCCPut, []byte("batch"), []byte("v"), nil, 0))

			observed := map[statemachine.Capability]bool{
				statemachine.CapabilityKV:          performs(t, machine, 1, must(kv.EncodePut([]byte("watched"), []byte("v")))),
				statemachine.CapabilityBatch:       performs(t, machine, 2, must(protocol.EncodeBatch([][]byte{batchOp}))),
				statemachine.CapabilityTransaction: performs(t, machine, 3, must(protocol.EncodeTransaction([][]byte{txnCmp}, [][]byte{txnOp}, nil))),
				statemachine.CapabilityIndex:       performs(t, machine, 4, must(protocol.EncodeIndexCreateOpts("probe_idx", "f", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))),
				statemachine.CapabilityLease:       performs(t, machine, 5, must(protocol.EncodeLeaseGrant(30))),
				statemachine.CapabilityTimeSeries:  performs(t, machine, 6, must(protocol.EncodeTSWrite("probe", time.Now().UnixNano(), 1, nil))),
			}
			select {
			case resp := <-w.Events():
				observed[statemachine.CapabilityWatch] = len(resp.Events) > 0
			case <-time.After(5 * time.Second):
				observed[statemachine.CapabilityWatch] = false
			}
			_, mvccErr := machine.Lookup(context.Background(), service.MVCCRevisionQuery{})
			observed[statemachine.CapabilityMVCC] = mvccErr == nil

			for c, present := range observed {
				declared := desc.Has(c)
				if c == statemachine.CapabilityTimeSeries && !capabilities.Has("timeSeries") {
					// Compiled out of this binary: the profile the
					// descriptor narrows already reports it off, and the
					// state machine refuses it as absent.
					continue
				}
				if declared != present {
					t.Errorf("%s: %s declared=%v but performs=%v", name, c, declared, present)
				}
			}
			for _, c := range desc.Capabilities {
				if _, probed := observed[c]; !probed && !strings.EqualFold(string(c), "vector") {
					t.Errorf("%s declares %s, which this test does not probe", name, c)
				}
			}
		})
	}
}
