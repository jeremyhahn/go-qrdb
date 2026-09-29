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
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
)

// PebbleDescriptor describes the "pebble" state machine — the daemon's
// default raft.sm_type — as a node composes it: with a lessor, a watch hub and
// time-series storage wired in (cmd/qrdbd). Its transactions compare on value
// and version and refuse revision compares, because it keeps no revisions, so
// it does not declare MVCC.
//
// The descriptor is what the node's capability report is narrowed by
// (statemachine.NarrowCapabilities), so it must name exactly what a node
// running this state machine serves.
func PebbleDescriptor() statemachine.Descriptor {
	return statemachine.Descriptor{
		Description: "PebbleDB-backed on-disk state machine",
		Persistence: statemachine.PersistenceDisk,
		Kind:        statemachine.SMKindDisk,
		Capabilities: []statemachine.Capability{
			statemachine.CapabilityKV,
			statemachine.CapabilityBatch,
			statemachine.CapabilityTransaction,
			statemachine.CapabilityIndex,
			statemachine.CapabilityLease,
			statemachine.CapabilityWatch,
			statemachine.CapabilityTimeSeries,
		},
	}
}

// MVCCDescriptor describes the "mvcc" state machine as a node composes it:
// revisioned MVCC storage with a lessor, a watch hub and time-series storage
// wired in. It is the one state machine that keeps revisions.
func MVCCDescriptor() statemachine.Descriptor {
	return statemachine.Descriptor{
		Description: "MVCC state machine with transactions, leases, indexes, and time-series",
		Persistence: statemachine.PersistenceDisk,
		Kind:        statemachine.SMKindDisk,
		Capabilities: []statemachine.Capability{
			statemachine.CapabilityKV,
			statemachine.CapabilityMVCC,
			statemachine.CapabilityTransaction,
			statemachine.CapabilityBatch,
			statemachine.CapabilityLease,
			statemachine.CapabilityIndex,
			statemachine.CapabilityTimeSeries,
			statemachine.CapabilityWatch,
		},
	}
}

// RegisterStateMachines registers this package's state machines — "pebble"
// and "mvcc" — in the default statemachine registry. They are registered from
// here rather than by pkg/statemachine, which cannot import this package.
// Every composition root that resolves an sm_type by name calls it once at
// startup; registering again replaces the entries with identical ones.
func RegisterStateMachines() {
	statemachine.RegisterWithDescriptor("pebble", func() sm.CreateFunc {
		return sm.NewDiskCreateFunc(func(_, _ uint64) sm.DiskStateMachine {
			return NewPebbleStateMachine(nil)
		})
	}, PebbleDescriptor())
	statemachine.RegisterWithDescriptor("mvcc", func() sm.CreateFunc {
		return NewMVCCCreateFunc()
	}, MVCCDescriptor())
}
