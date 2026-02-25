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

// Package writemode re-exports write durability modes from go-quicraft.
//
// Applications should import this package rather than go-quicraft directly.
// Three modes are supported:
//   - Sync: waits for quorum commit (strongest durability).
//   - WALDurable: waits only for local WAL persistence (lower latency).
//   - Batched: collects writes and proposes as a single Raft entry (amortized cost).
package writemode

import (
	qcwritemode "github.com/jeremyhahn/go-quicraft/pkg/writemode"
)

// Mode represents the durability guarantee level for write operations.
type Mode = qcwritemode.Mode

// Write mode constants.
const (
	// Sync waits for the write to be replicated to a quorum and committed.
	// Strongest durability (~10-30ms per write). Uses Host.SyncPropose().
	Sync = qcwritemode.Sync

	// WALDurable waits only for local WAL persistence.
	// Faster (~1-2ms) but risks data loss if leader crashes before replication.
	// Uses Host.Propose() + RequestState.CommittedC().
	WALDurable = qcwritemode.WALDurable

	// Batched collects writes and proposes as a single Raft entry.
	// Same durability as Sync but amortized cost (~0.1-0.3ms per op).
	// Uses batch.Aggregator.Submit().
	Batched = qcwritemode.Batched
)

// FromString parses a Mode from its string representation.
// An empty string defaults to Sync. The following aliases are accepted:
//   - "wal" and "async" for WALDurable
//   - "batch" for Batched
func FromString(s string) (Mode, error) {
	return qcwritemode.FromString(s)
}
