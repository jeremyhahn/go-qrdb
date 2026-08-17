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
func (e *Engine) commitBufferedOps(ops []storage.BufferedOp) error {
	if len(ops) == 0 {
		return nil
	}
	e.journalMu.Lock()
	defer e.journalMu.Unlock()

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

// applyBufferedOps writes a batch's operations to the engine in order.
// Each Set/Delete is individually durable (see Engine.Set).
func (e *Engine) applyBufferedOps(ops []storage.BufferedOp) error {
	for _, op := range ops {
		if op.Deleted {
			if err := e.Delete(op.Key); err != nil {
				return err
			}
			continue
		}
		if err := e.Set(op.Key, op.Value); err != nil {
			return err
		}
	}
	return nil
}
