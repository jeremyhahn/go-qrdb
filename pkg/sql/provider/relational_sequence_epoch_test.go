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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Sequence fencing (go-qrdb#547)
//
// SequenceServicer.Reset -- reached by ALTER TABLE ... AUTO_INCREMENT = 0,
// and TableServicer.TruncateTable's MySQL-dialect sequence reset -- advances
// the sequence's epoch in the same compare-and-swap that zeroes its counter.
// sequenceBlock records the epoch its cached [cursor, end) range was reserved
// under and verifies it against SequenceServicer.Epoch before handing out
// ANY cached value, discarding the range on a mismatch. These tests pin what
// that gives a caller: a range that still has unconsumed capacity when a
// reset commits through a different handle stops being usable at once, so a
// session holding a pre-reset block never emits a value the post-reset
// sequence will hand out again.
// ---------------------------------------------------------------------------

// newEpochTestTable resolves def the way a statement does: a fresh
// RelationalTable, and so a fresh sequenceBlock, over the harness's shared
// services.
func newEpochTestTable(t *testing.T, h *ddlHarness) *RelationalTable {
	t.Helper()
	def, err := h.tableSvc.GetTable(context.Background(), "shop", "widgets")
	require.NoError(t, err)
	tbl, err := NewRelationalTable(def, h.kvSvc, h.mvccSvc,
		WithTableService(h.tableSvc),
		WithTransactionService(h.txnSvc),
		WithSequenceService(h.seqSvc),
	)
	require.NoError(t, err)
	return tbl
}

// drawAutoInc draws n AUTO_INCREMENT values from tbl.
func drawAutoInc(t *testing.T, tbl *RelationalTable, n int) []uint64 {
	t.Helper()
	out := make([]uint64, 0, n)
	for i := 0; i < n; i++ {
		v, err := tbl.GetNextAutoIncrementValue(newTestContext(), nil)
		require.NoError(t, err)
		out = append(out, v)
	}
	return out
}

// assertStaleBlockFenced is the shared body of the reset-kind tests below.
// Session A draws four values, which leaves it holding [5, 8) -- a reserved
// block with unconsumed capacity (block sizes grow 1, 2, 4). reset then
// resets the sequence through a different handle, and a third session B
// draws eight post-reset values. Every value A draws after the reset must be
// disjoint from B's: before fencing, A kept emitting 5, 6, 7 from its
// pre-reset block while B was handed 1..8 from the post-reset counter, so
// the two sessions would each insert a row keyed 5 and one of them would
// fail with a duplicate-key error.
func assertStaleBlockFenced(t *testing.T, reset func(t *testing.T, h *ddlHarness)) {
	t.Helper()
	h := newDDLHarness()
	_, err := h.tableSvc.CreateTable(context.Background(), autoIncCreateRequest())
	require.NoError(t, err)

	tblA := newEpochTestTable(t, h)
	require.Equal(t, []uint64{1, 2, 3, 4}, drawAutoInc(t, tblA, 4))

	reset(t, h)

	afterA := drawAutoInc(t, tblA, 3)
	fromB := drawAutoInc(t, newEpochTestTable(t, h), 8)

	for _, v := range afterA {
		assert.NotContains(t, fromB, v,
			"session A emitted %d from a block reserved before the reset; the post-reset sequence hands it out again", v)
	}
	assert.Equal(t, uint64(1), afterA[0], "A's first post-reset value must come from the post-reset counter")
}

// TestSequenceEpoch_Truncate_FencesUnconsumedBlock covers TRUNCATE TABLE on
// the MySQL dialect, which resets AUTO_INCREMENT in the same transaction
// that advances the table's generation.
func TestSequenceEpoch_Truncate_FencesUnconsumedBlock(t *testing.T) {
	assertStaleBlockFenced(t, func(t *testing.T, h *ddlHarness) {
		_, err := newEpochTestTable(t, h).Truncate(newTestContext())
		require.NoError(t, err)
	})
}

// TestSequenceEpoch_SetAutoIncrementZero_FencesUnconsumedBlock covers ALTER
// TABLE ... AUTO_INCREMENT = 0, which reaches SequenceServicer.Reset without
// changing the table's generation -- so no DML fence stands behind it, and
// the epoch is the only thing that stops a stale block.
func TestSequenceEpoch_SetAutoIncrementZero_FencesUnconsumedBlock(t *testing.T) {
	assertStaleBlockFenced(t, func(t *testing.T, h *ddlHarness) {
		tbl := newEpochTestTable(t, h)
		require.NoError(t, tbl.AutoIncrementSetter(newTestContext()).SetAutoIncrementValue(newTestContext(), 0))
	})
}

// TestSequenceEpoch_Truncate_ExhaustedBlockRefillsFromReset covers the
// block with nothing left cached: A's next value comes from a refill, which
// must read the post-reset counter rather than continue the old sequence.
func TestSequenceEpoch_Truncate_ExhaustedBlockRefillsFromReset(t *testing.T) {
	h := newDDLHarness()
	_, err := h.tableSvc.CreateTable(context.Background(), autoIncCreateRequest())
	require.NoError(t, err)

	drawAutoInc(t, newEpochTestTable(t, h), 3)
	tblA := newEpochTestTable(t, h)
	require.Equal(t, []uint64{4}, drawAutoInc(t, tblA, 1), "a block of one, fully consumed")

	_, err = newEpochTestTable(t, h).Truncate(newTestContext())
	require.NoError(t, err)

	assert.Equal(t, []uint64{1}, drawAutoInc(t, tblA, 1))
}

// epochFailingSequence is a real SequenceServicer whose Epoch read fails,
// standing in for a linearizable read that cannot complete (no leader, a
// cancelled context).
type epochFailingSequence struct {
	service.SequenceServicer
}

func (epochFailingSequence) Epoch(context.Context, string, string) (uint64, error) {
	return 0, assert.AnError
}

// TestSequenceBlock_EpochReadErrorFailsTheValue verifies a block whose
// freshness cannot be proven hands out nothing: the Epoch read's error is
// the caller's, and the cached value is not consumed.
func TestSequenceBlock_EpochReadErrorFailsTheValue(t *testing.T) {
	h := newDDLHarness()
	ctx := context.Background()

	var b sequenceBlock
	for i := 0; i < 2; i++ {
		_, _, err := b.next(ctx, h.seqSvc, "shop", "seq")
		require.NoError(t, err)
	}
	require.Less(t, b.cursor, b.end, "the second block of two has one value left")
	cursor := b.cursor

	_, _, err := b.next(ctx, epochFailingSequence{h.seqSvc}, "shop", "seq")
	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, cursor, b.cursor, "a value whose block could not be proven current is not consumed")

	v, _, err := b.next(ctx, h.seqSvc, "shop", "seq")
	require.NoError(t, err)
	assert.Equal(t, cursor, v)
}

// TestSequenceBlock_EmptyBlockSkipsEpochRead verifies the epoch read is paid
// only for a cached value: a block with nothing cached goes straight to
// Allocate, which reports the epoch itself.
func TestSequenceBlock_EmptyBlockSkipsEpochRead(t *testing.T) {
	h := newDDLHarness()

	var b sequenceBlock
	v, epoch, err := b.next(context.Background(), epochFailingSequence{h.seqSvc}, "shop", "seq")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), v)
	assert.Equal(t, uint64(0), epoch)
}

// TestSequenceEpoch_ConcurrentAllocationsNeverDuplicateWithinAnEpoch races
// several sessions drawing AUTO_INCREMENT values, each through its own
// RelationalTable and so its own sequenceBlock, against a session that
// repeatedly truncates. A value may legitimately recur across epochs --
// TRUNCATE empties the table, so 1 in the next epoch collides with nothing
// -- but never within one: that is the invariant a stale pre-reset block
// broke, by handing out values the post-reset counter was also handing out.
func TestSequenceEpoch_ConcurrentAllocationsNeverDuplicateWithinAnEpoch(t *testing.T) {
	h := newDDLHarness()
	_, err := h.tableSvc.CreateTable(context.Background(), autoIncCreateRequest())
	require.NoError(t, err)

	const goroutines = 8
	const perGoroutine = 50

	type key struct{ epoch, value uint64 }
	var (
		mu   sync.Mutex
		seen = make(map[key]int, goroutines*perGoroutine)
		wg   sync.WaitGroup
	)

	tables := make([]*RelationalTable, goroutines)
	for g := range tables {
		tables[g] = newEpochTestTable(t, h)
	}
	truncator := newEpochTestTable(t, h)

	for _, tbl := range tables {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				v, epoch, err := tbl.autoInc.next(context.Background(), h.seqSvc, "shop", tbl.autoIncSequenceName())
				if err != nil {
					t.Errorf("next: %v", err)
					return
				}
				mu.Lock()
				seen[key{epoch: epoch, value: v}]++
				mu.Unlock()
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			if _, err := truncator.Truncate(newTestContext()); err != nil {
				t.Errorf("Truncate: %v", err)
				return
			}
		}
	}()

	wg.Wait()

	require.NotEmpty(t, seen)
	for k, count := range seen {
		assert.Equal(t, 1, count, "value %d in epoch %d was generated %d times", k.value, k.epoch, count)
	}
}
