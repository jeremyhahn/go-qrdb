// Copyright (c) 2026 Jeremy Hahn
// Copyright (c) 2026 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import (
	"sync"
	"testing"
)

// decodeSnowflake splits an ID back into its (timestamp, node, sequence)
// components for assertions.
func decodeSnowflake(id uint64) (ts, node, seq uint64) {
	ts = id >> (snowflakeNodeBits + snowflakeSeqBits)
	node = (id >> snowflakeSeqBits) & snowflakeMaxNode
	seq = id & snowflakeMaxSeq
	return ts, node, seq
}

// TestSnowflake_NodeRangeError rejects a node id beyond the 10-bit field.
func TestSnowflake_NodeRangeError(t *testing.T) {
	if _, err := NewSnowflakeGenerator(snowflakeMaxNode + 1); err == nil {
		t.Fatal("expected SnowflakeNodeRangeError for node id 1024")
	} else if _, ok := err.(*SnowflakeNodeRangeError); !ok {
		t.Fatalf("expected *SnowflakeNodeRangeError, got %T", err)
	}
	if _, err := NewSnowflakeGenerator(snowflakeMaxNode); err != nil {
		t.Fatalf("node id 1023 must be valid: %v", err)
	}
}

// TestSnowflake_EmbedsNodeAndIsPositive verifies the node component is encoded
// and the high bit stays clear (positive int64/uint64).
func TestSnowflake_EmbedsNodeAndIsPositive(t *testing.T) {
	g, err := NewSnowflakeGenerator(42)
	if err != nil {
		t.Fatal(err)
	}
	id := g.Next()
	if id>>63 != 0 {
		t.Errorf("high bit must be zero, got id=%d", id)
	}
	_, node, _ := decodeSnowflake(id)
	if node != 42 {
		t.Errorf("node = %d, want 42", node)
	}
}

// TestSnowflake_MonotonicSingleThread asserts strictly increasing IDs.
func TestSnowflake_MonotonicSingleThread(t *testing.T) {
	g, err := NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatal(err)
	}
	prev := g.Next()
	for i := 0; i < 100_000; i++ {
		id := g.Next()
		if id <= prev {
			t.Fatalf("not monotonic: id=%d prev=%d at i=%d", id, prev, i)
		}
		prev = id
	}
}

// TestSnowflake_SequenceRolloverWithinMillisecond uses a frozen clock to force
// the per-millisecond sequence to exhaust and roll into the next logical ms.
func TestSnowflake_SequenceRolloverWithinMillisecond(t *testing.T) {
	g, err := NewSnowflakeGenerator(7)
	if err != nil {
		t.Fatal(err)
	}
	// Freeze the clock so every ID falls in the same wall-clock millisecond.
	frozen := int64(snowflakeEpochMillis + 5)
	g.nowMillis = func() int64 { return frozen }

	seen := make(map[uint64]struct{})
	var prev uint64
	// Generate one full millisecond of sequence space plus two more to force
	// rollover into the next logical millisecond.
	total := snowflakeMaxSeq + 1 + 2
	for i := 0; i < total; i++ {
		id := g.Next()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %d at i=%d", id, i)
		}
		seen[id] = struct{}{}
		if i > 0 && id <= prev {
			t.Fatalf("not monotonic across rollover: id=%d prev=%d i=%d", id, prev, i)
		}
		prev = id
	}
	// The ID after the boundary must have advanced its timestamp component.
	_, _, lastSeq := decodeSnowflake(prev)
	if lastSeq != 1 {
		t.Errorf("after rollover+2, sequence = %d, want 1", lastSeq)
	}
}

// TestSnowflake_ClockRegression proves a backwards clock never produces a
// duplicate or a non-monotonic ID — the generator advances logically instead.
func TestSnowflake_ClockRegression(t *testing.T) {
	g, err := NewSnowflakeGenerator(3)
	if err != nil {
		t.Fatal(err)
	}
	current := int64(snowflakeEpochMillis + 1000)
	g.nowMillis = func() int64 { return current }

	first := g.Next()
	// Slam the clock backwards by 500ms.
	current = snowflakeEpochMillis + 500
	second := g.Next()
	if second <= first {
		t.Fatalf("clock regression broke monotonicity: first=%d second=%d", first, second)
	}
}

// TestSnowflake_ConcurrentUniqueness asserts no duplicates under heavy
// concurrency using the real clock.
func TestSnowflake_ConcurrentUniqueness(t *testing.T) {
	g, err := NewSnowflakeGenerator(11)
	if err != nil {
		t.Fatal(err)
	}
	const goroutines = 16
	const perG = 5000

	var wg sync.WaitGroup
	results := make([][]uint64, goroutines)
	for w := 0; w < goroutines; w++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ids := make([]uint64, perG)
			for i := 0; i < perG; i++ {
				ids[i] = g.Next()
			}
			results[idx] = ids
		}(w)
	}
	wg.Wait()

	seen := make(map[uint64]struct{}, goroutines*perG)
	for _, ids := range results {
		for _, id := range ids {
			if _, dup := seen[id]; dup {
				t.Fatalf("duplicate id %d under concurrency", id)
			}
			seen[id] = struct{}{}
		}
	}
	if len(seen) != goroutines*perG {
		t.Fatalf("expected %d unique ids, got %d", goroutines*perG, len(seen))
	}
}

// TestSnowflake_DistinctNodesNeverCollide proves two generators with different
// node ids cannot produce the same ID even from the same millisecond/sequence.
func TestSnowflake_DistinctNodesNeverCollide(t *testing.T) {
	g1, _ := NewSnowflakeGenerator(1)
	g2, _ := NewSnowflakeGenerator(2)
	frozen := int64(snowflakeEpochMillis + 99)
	g1.nowMillis = func() int64 { return frozen }
	g2.nowMillis = func() int64 { return frozen }

	seen := make(map[uint64]struct{})
	for i := 0; i < 1000; i++ {
		for _, id := range []uint64{g1.Next(), g2.Next()} {
			if _, dup := seen[id]; dup {
				t.Fatalf("cross-node collision at i=%d id=%d", i, id)
			}
			seen[id] = struct{}{}
		}
	}
}

// TestSnowflake_NextIDDelegates confirms the IDGenerator interface method
// returns the same stream as Next.
func TestSnowflake_NextIDDelegates(t *testing.T) {
	g, err := NewSnowflakeGenerator(5)
	if err != nil {
		t.Fatal(err)
	}
	a := g.NextID(nil)
	b := g.Next()
	if b <= a {
		t.Fatalf("NextID/Next not monotonic: a=%d b=%d", a, b)
	}
	_, node, _ := decodeSnowflake(a)
	if node != 5 {
		t.Errorf("node = %d, want 5", node)
	}
}
