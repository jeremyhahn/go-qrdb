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

package main

import (
	"fmt"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// printClusterMigration appends a barrier's cluster-wide encryption migration
// to a status table: one summary line, then one line per owner in rolling
// order -- its step, and its own report for the migration's generation.
func printClusterMigration(m *transport.ClusterMigration) {
	const label = "Migration:       "
	switch {
	case m == nil:
		_, _ = fmt.Fprintf(outputWriter, "%snone in progress\n", label)
		return
	case m.Unconfirmed:
		_, _ = fmt.Fprintf(outputWriter, "%sunconfirmed (the replicated record could not be read)\n", label)
		return
	}
	_, _ = fmt.Fprintf(outputWriter, "%sto %s, generation %d, %d of %d owners done\n", label, m.Target, m.Generation, m.Acked, m.Total)
	for _, n := range m.Nodes {
		step := n.Step
		if step == "" {
			step = "-"
		}
		phase := n.Phase
		if phase == "" {
			phase = "not reported"
		}
		if n.Attempts > 0 {
			phase = fmt.Sprintf("%s, attempt %d", phase, n.Attempts)
		}
		if n.Detail != "" {
			phase = fmt.Sprintf("%s: %s", phase, n.Detail)
		}
		_, _ = fmt.Fprintf(outputWriter, "  %-14s %-10s %s\n", n.NodeID, step, phase)
	}
}
