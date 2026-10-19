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

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// Report is the `cluster_migration` object a barrier status carries while a
// cluster-wide encryption migration of that barrier is in progress
// (docs/barrier-seal/api.md). It is read from the replicated record, so every
// node answers the same.
type Report struct {
	// Unconfirmed is true when the replicated record could not be read just
	// now (no leader, a store error); every other field is then empty. It is
	// never a claim that no migration is in progress.
	Unconfirmed bool `json:"unconfirmed,omitempty"`
	// Target is the strategy every owner converges to.
	Target seal.StrategyID `json:"target,omitempty"`
	// Generation is the request the steps and phases below belong to.
	Generation uint64 `json:"generation,omitempty"`
	// Acked of Total owners have confirmed the target for Generation.
	Acked int `json:"acked"`
	Total int `json:"total"`
	// Nodes is every owner, in rolling order when the request has steps.
	Nodes []NodeReport `json:"nodes,omitempty"`
}

// NodeReport is one owner's place in a migration.
type NodeReport struct {
	NodeID string `json:"node_id"`
	// Step is the owner's turn: pending, recycling or serving. It is empty
	// for a request made before featuregate.RollingMigration was activated,
	// where every owner recycles at once.
	Step StepState `json:"step,omitempty"`
	// Phase, Attempts and Detail are the owner's own report for Generation;
	// Phase is empty until it reports.
	Phase    seal.MigrationPhase `json:"phase,omitempty"`
	Attempts int                 `json:"attempts,omitempty"`
	Detail   string              `json:"detail,omitempty"`
}

// Report reads the migration of the barrier at (tenantID, database): nil when
// none is in progress. A node status of an older generation is not reported.
func (s *Store) Report(ctx context.Context, tenantID, database string) (*Report, error) {
	m, err := s.GetMigration(ctx, tenantID, database)
	if err != nil || m == nil {
		return nil, err
	}
	statuses, err := s.ListNodeStatuses(ctx, tenantID, database)
	if err != nil {
		return nil, err
	}
	current := make(map[string]*NodeMigrationStatus, len(statuses))
	for _, st := range statuses {
		if st.Generation == m.Generation {
			current[st.NodeID] = st
		}
	}
	steps := make(map[string]StepState, len(m.Steps))
	order := m.Owners
	if len(m.Steps) > 0 {
		order = make([]string, len(m.Steps))
		for i, step := range m.Steps {
			order[i] = step.NodeID
			steps[step.NodeID] = step.State
		}
	}
	r := &Report{Target: m.Target, Generation: m.Generation, Total: len(m.Owners), Nodes: make([]NodeReport, 0, len(order))}
	for _, id := range order {
		n := NodeReport{NodeID: id, Step: steps[id]}
		if st := current[id]; st != nil {
			n.Phase, n.Attempts, n.Detail = st.Phase, st.Attempts, st.Detail
		}
		r.Nodes = append(r.Nodes, n)
	}
	for _, owner := range m.Owners {
		if st := current[owner]; st != nil && st.Phase == seal.PhaseComplete {
			r.Acked++
		}
	}
	return r, nil
}
