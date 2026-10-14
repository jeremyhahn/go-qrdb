// Copyright (c) 2026 Jeremy Hahn
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

package file

import (
	"golang.org/x/sync/errgroup"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// newBatch creates a batch that buffers writes, serves reads through
// them, and commits them atomically via the commit journal: the whole
// batch is durably published to a single journal file BEFORE any per-key
// file is touched, then applied, then the journal is cleared. A crash at
// any point leaves either none of the batch (journal never published) or,
// after Open-time recovery, all of it (journal replayed) — the
// all-or-nothing contract raft state-machine storage requires.
func newBatch(e *Engine) storage.Batch {
	return storage.NewBufferedBatch(e, e.commitBufferedOps)
}

// commitBufferedOps runs the journaled commit protocol for one batch.
//
// Every op is checked with checkKey BEFORE the journal is written — not
// folded into the loop inside applyBufferedOps, which runs AFTER the journal
// is already durable. A refusal is a DETERMINISTIC property of the op
// itself: every replica refuses the identical op the identical way, and
// retrying it after a crash can never make it succeed. Writing the journal
// first and finding the refusal only inside applyBufferedOps left a
// committed journal recovery could never get past: recoverJournal replays
// it at the next Open by calling this same apply path, hits the identical
// refusal again, and fails the open — permanently, since nothing about a
// deterministic refusal changes between restarts (go-qrdb#592 B2/B3). A
// transient fault (disk full, a permissions change) is different: it can
// only be discovered by actually attempting the write, which is why
// applyBufferedOps's own errors still leave the journal in place for
// recovery to retry — retrying is exactly right for a fault that might not
// recur.
//
// The commit holds the engine's write lock throughout (see Engine), which is
// what keeps two commits -- in this process or another -- from interleaving
// publish/apply/clear on the single journal file.
func (e *Engine) commitBufferedOps(ops []storage.BufferedOp) (err error) {
	if len(ops) == 0 {
		return nil
	}
	for _, op := range ops {
		if err := checkKey(op.Key); err != nil {
			return err
		}
	}

	if err := e.lockWrites(); err != nil {
		return err
	}
	defer func() { err = e.unlockWrites(err) }()

	if err := e.writeJournal(encodeJournal(ops)); err != nil {
		return err
	}
	if err := e.applyBufferedOps(ops); err != nil {
		// The journal stays in place: recovery at next open completes
		// the commit, preserving all-or-nothing semantics even when the
		// in-process apply failed midway.
		return err
	}
	return e.clearJournal()
}

// applyConcurrency bounds how many records one commit writes at once. Every
// record costs a file fsync and a directory fsync, so a commit of K keys
// applied one after another waited 2K fsyncs in a row; issued together they
// overlap in the device queue and the commit waits roughly one. The bound
// keeps a large batch from opening thousands of descriptors at once. It is a
// property of the write path, not a tuning knob.
const applyConcurrency = 32

// applyBufferedOps writes a batch's operations to the engine. Each Set/Delete
// is individually durable (see Engine.Set). The caller holds the write lock
// -- a commit, or open-time recovery -- so the unlocked set and del are used,
// and nothing returns until every write has finished, so no write outlives
// the lock or the journal that covers it.
//
// Order only matters between operations on the same key, so the batch is
// reduced to the last operation per key -- the state applying every operation
// in order would leave -- and the distinct keys, which share no file, are
// written concurrently. A failure leaves the journal in place exactly as a
// failure midway through a serial apply did: recovery replays the whole batch.
func (e *Engine) applyBufferedOps(ops []storage.BufferedOp) error {
	var g errgroup.Group
	g.SetLimit(applyConcurrency)
	for _, op := range lastOpPerKey(ops) {
		g.Go(func() error {
			if op.Deleted {
				return e.del(op.Key)
			}
			return e.set(op.Key, op.Value)
		})
	}
	return g.Wait()
}

// lastOpPerKey returns ops reduced to the final operation on each key, in the
// order those final operations appear. A batch that touches each key once is
// returned as is.
func lastOpPerKey(ops []storage.BufferedOp) []storage.BufferedOp {
	last := make(map[string]int, len(ops))
	for i, op := range ops {
		last[string(op.Key)] = i
	}
	if len(last) == len(ops) {
		return ops
	}
	final := make([]storage.BufferedOp, 0, len(last))
	for i, op := range ops {
		if last[string(op.Key)] == i {
			final = append(final, op)
		}
	}
	return final
}
