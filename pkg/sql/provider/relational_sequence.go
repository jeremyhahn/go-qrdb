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
	"sync"
	"time"

	sqle "github.com/dolthub/go-mysql-server/sql"

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
//
// Fencing: a reset -- ALTER TABLE ... AUTO_INCREMENT = 0, or a MySQL-dialect
// TRUNCATE TABLE -- restarts the counter at 1, so the post-reset counter
// hands out again every value a block reserved before the reset still
// holds. SequenceServicer carries an epoch that every reset advances, and
// Allocate reports the epoch a block was reserved under; sequenceBlock keeps
// it beside the block, and the first time a statement draws a cached value
// from a block the statement did not itself reserve, it compares that epoch
// with SequenceServicer.Epoch. A block that still has unconsumed capacity
// when a reset commits -- through another session, another handle, or
// another node -- is therefore discarded by the next statement that would
// use it, rather than at its natural exhaustion, and next refills from the
// post-reset counter.
//
// The check is once per statement, not once per value. One linearizable
// read per generated value would cost a multi-row INSERT one round trip per
// row, and it would buy nothing a per-statement check does not: a value
// drawn before a reset and committed after it is a window no allocator-side
// check can close (a reset between the check and the row's commit is
// indistinguishable from one just after the commit), and inside one
// statement that is the only window there is. After TRUNCATE the statement's
// rows are refused by the generation fence anyway; after ALTER TABLE ...
// AUTO_INCREMENT = 0 they are rows already written, which Reset's contract
// leaves to the caller.
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
	epoch    uint64 // the sequence's epoch when [cursor, end) was reserved
	lastSize uint64 // size of the most recently requested block; 0 before the first allocation

	// current is the statement for which [cursor, end) is known to belong
	// to the sequence's current epoch: the one that reserved it, or the one
	// that last verified it. Zero before either has happened.
	current statementID
}

// statementID identifies the statement a *sqle.Context executes. The engine
// builds one context per statement, stamping it with the statement's pid and
// the instant it began; every derived context (WithContext, WithQuery, a
// subquery's) carries both unchanged, so they stay equal for the whole
// statement and differ from the next one's. The zero value names no
// statement.
type statementID struct {
	pid   uint64
	began time.Time
}

// statementOf returns the statementID of the statement ctx executes.
func statementOf(ctx *sqle.Context) statementID {
	return statementID{pid: ctx.Pid(), began: ctx.QueryTime()}
}

// is reports whether id and other name the same statement. time.Time's
// Equal compares the monotonic readings both carry, so two statements that
// began in the same wall-clock tick are still told apart.
func (id statementID) is(other statementID) bool {
	return id.pid == other.pid && !id.began.IsZero() && id.began.Equal(other.began)
}

// next returns the next value to hand out and the epoch it was reserved
// under. A cached value the calling statement has not yet vouched for is
// handed out only after one Epoch read confirms its block's epoch is still
// the sequence's current one (see the fencing note above); a stale block is
// discarded. Whenever the block is exhausted or discarded, next allocates a
// fresh one from seqSvc via a Raft-committed CAS, which reports the epoch
// itself. Each successive block requested by the same sequenceBlock is at
// least as large as the last (see nextBlockSizeLocked), amortizing the Raft
// round trip cost of a large multi-row INSERT's allocations.
func (b *sequenceBlock) next(ctx *sqle.Context, seqSvc service.SequenceServicer, database, seqName string) (uint64, uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	stmt := statementOf(ctx)
	if b.cursor < b.end && !b.current.is(stmt) {
		current, err := seqSvc.Epoch(ctx, database, seqName)
		if err != nil {
			return 0, 0, err
		}
		if current != b.epoch {
			b.cursor, b.end = 0, 0
		}
		b.current = stmt
	}

	if b.cursor >= b.end {
		size := b.nextBlockSizeLocked()
		first, epoch, err := seqSvc.Allocate(ctx, database, seqName, size)
		if err != nil {
			return 0, 0, err
		}
		b.cursor = first
		b.end = first + size
		b.epoch = epoch
		b.current = stmt
	}

	v := b.cursor
	b.cursor++
	return v, b.epoch, nil
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
	b.cursor, b.end, b.epoch, b.lastSize = 0, 0, 0, 0
	b.current = statementID{}
}
