// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package clustermig

import "slices"

// StepState is where one owner stands in a rolling migration.
type StepState string

const (
	// StepPending is an owner whose turn has not come: it does not recycle.
	StepPending StepState = "pending"
	// StepRecycling is the one owner whose turn it is: it recycles to the
	// target, and stays here until its ack for the Generation is recorded.
	StepRecycling StepState = "recycling"
	// StepServing is an owner that recycled and acked the Generation with
	// PhaseComplete: back and serving at the target strategy.
	StepServing StepState = "serving"
)

// NodeStep is one owner's place in a rolling migration's order.
type NodeStep struct {
	NodeID string    `json:"node_id"`
	State  StepState `json:"state"`

	// CatchUpIndex is the index of the shard's Raft log this owner must hold
	// before its step is serving: the leader's commit index when the leader
	// first saw the owner's PhaseComplete ack (Coordinator.Advance, with
	// WithCatchUp). Zero until then. It is in the record rather than in the
	// leader's memory so a new leader judges the same target.
	CatchUpIndex uint64 `json:"catch_up_index,omitempty"`
}

// rollingSteps orders owners for a rolling migration, as a rolling restart
// does: every other owner in placement order, then self (the coordinating
// leader) last, so the migration costs one leader change rather than one per
// step. The first step starts recycling at once.
//
// inFlight names the owner a superseded generation authorized to recycle and
// that has not acknowledged it: that owner may be sealed or rebooting right
// now, so it keeps the turn and goes first, and no other owner recycles until
// it serves again. Ordering self last would otherwise hand the first turn to
// an owner that is still serving, and with the in-flight one that is two
// owners away at once. An inFlight that is no longer an owner is left out: it
// no longer votes in the shard, so its absence costs no quorum.
func rollingSteps(owners []string, self, inFlight string) []NodeStep {
	steps := make([]NodeStep, 0, len(owners))
	if inFlight != "" && slices.Contains(owners, inFlight) {
		steps = append(steps, NodeStep{NodeID: inFlight, State: StepPending})
	}
	last := false
	for _, o := range owners {
		if o == inFlight {
			continue
		}
		if o == self {
			last = true
			continue
		}
		steps = append(steps, NodeStep{NodeID: o, State: StepPending})
	}
	if last {
		steps = append(steps, NodeStep{NodeID: self, State: StepPending})
	}
	if len(steps) > 0 {
		steps[0].State = StepRecycling
	}
	return steps
}

// turnOf reports whether nodeID may recycle m now: always for a migration
// without steps, and for a rolling one only while nodeID's step is
// StepRecycling.
func (m *ClusterMigration) turnOf(nodeID string) bool {
	if len(m.Steps) == 0 {
		return true
	}
	for _, s := range m.Steps {
		if s.NodeID == nodeID {
			return s.State == StepRecycling
		}
	}
	return false
}

// rolledOut reports whether every step of a rolling migration is
// StepServing.
func (m *ClusterMigration) rolledOut() bool {
	for _, s := range m.Steps {
		if s.State != StepServing {
			return false
		}
	}
	return len(m.Steps) > 0
}
