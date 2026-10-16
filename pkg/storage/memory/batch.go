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

package memory

import (
	"github.com/tidwall/btree"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// newBatch creates a batch that buffers writes, serves reads through them, and
// applies the whole set to the btree under a single write lock on Commit.
func newBatch(parent *MemoryStorage) storage.Batch {
	return storage.NewBufferedBatch(parent, parent.applyBufferedOps)
}

// applyBufferedOps applies a batch's operations to the btree atomically with
// respect to other readers and writers.
func (s *MemoryStorage) applyBufferedOps(ops []storage.BufferedOp) error {
	if s.closed.Load() {
		return &storage.ClosedError{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// A batch's writes fall into a few key families told apart by their
	// leading byte -- an MVCC store's data, metadata and revision-index
	// records, say -- each written in near-sorted runs. One path hint per
	// family lets the tree resume each search where that family's last one
	// ended instead of from the root.
	var hints [256]btree.PathHint
	for _, op := range ops {
		if op.Range {
			s.deleteRangeLocked(op.Key, op.RangeEnd)
			continue
		}
		hint := &hints[op.Key[0]]
		if op.Deleted {
			s.tree.DeleteHint(entry{key: op.Key}, hint)
			continue
		}
		s.tree.SetHint(entry{key: op.Key, value: op.Value}, hint)
	}
	return nil
}
