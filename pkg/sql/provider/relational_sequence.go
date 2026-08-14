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

package provider

import (
	"context"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// sequenceBlock: block allocation over a Raft-committed counter.
//
// Two of a RelationalTable's generated values are drawn from this: its
// AUTO_INCREMENT column (relational_autoincrement.go) and, for a table with
// no declared PRIMARY KEY, its hidden row id (relational_rowid.go). Both
// have identical requirements, so both use this one allocator rather than
// two near-copies of it.
//
// Every value ultimately comes from service.SequenceServicer -- a
// Raft-committed compare-and-swap counter, never a process-local one -- so a
// value handed out on one node can never be handed out again on another.
// Values are allocated in growing blocks rather than one
// SequenceServicer.Allocate round trip per row, so a multi-row INSERT pays
// for one Raft proposal per block rather than one per row.
//
// Correctness bar: gaps are acceptable, duplicates are not. A block a
// statement does not fully consume (because the connection was lost
// mid-INSERT, or a later row failed validation) is never handed back; its
// unused values are simply skipped forever. But every block boundary is
// established by a Raft-committed CAS, so two concurrent allocators -- on
// the same node or different ones -- can never both believe they own the
// same value.
// ---------------------------------------------------------------------------

const (
	// sequenceInitialBlockSize is the size of the first block a given
	// sequenceBlock ever allocates. A single-row INSERT -- overwhelmingly
	// the common case -- must not reserve (and then waste) more than the one
	// value it uses.
	sequenceInitialBlockSize = 1

	// sequenceMaxBlockSize caps how large a single allocated block can grow,
	// bounding both the Raft-proposal-avoidance benefit of a very large
	// INSERT and the worst-case gap a statement that only partially consumes
	// its block leaves behind.
	sequenceMaxBlockSize = 1024
)

// sequenceBlock is a mutable block-allocation cursor over one named
// SequenceServicer counter. It is safe for concurrent use, though in
// practice next is only ever called sequentially within a single statement's
// row loop.
type sequenceBlock struct {
	mu       sync.Mutex
	cursor   uint64 // next unused value in the reserved block
	end      uint64 // exclusive upper bound of the reserved block
	lastSize uint64 // size of the most recently requested block; 0 before the first allocation
}

// next returns the next value to hand out, allocating a fresh block from
// seqSvc via a Raft-committed CAS whenever the current one is exhausted.
// Each successive block requested by the same sequenceBlock is at least as
// large as the last (see nextBlockSizeLocked), amortizing the Raft round
// trip cost across a large multi-row INSERT.
func (b *sequenceBlock) next(ctx context.Context, seqSvc service.SequenceServicer, database, seqName string) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.cursor >= b.end {
		size := b.nextBlockSizeLocked()
		first, err := seqSvc.Allocate(ctx, database, seqName, size)
		if err != nil {
			return 0, err
		}
		b.cursor = first
		b.end = first + size
	}

	v := b.cursor
	b.cursor++
	return v, nil
}

// nextBlockSizeLocked returns the size of the next block to request,
// doubling the previous request up to sequenceMaxBlockSize. Callers must
// hold b.mu.
func (b *sequenceBlock) nextBlockSizeLocked() uint64 {
	switch {
	case b.lastSize == 0:
		b.lastSize = sequenceInitialBlockSize
	case b.lastSize < sequenceMaxBlockSize:
		b.lastSize *= 2
		if b.lastSize > sequenceMaxBlockSize {
			b.lastSize = sequenceMaxBlockSize
		}
	}
	return b.lastSize
}

// discard drops any block reserved so far, so the next call to next
// re-reads the sequence's current (freshly-committed) value instead of
// handing out a value from a block reserved before that commit. Called by
// relationalAutoIncSetter.SetAutoIncrementValue.
func (b *sequenceBlock) discard() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cursor, b.end, b.lastSize = 0, 0, 0
}
