// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file covers IDSequence -- the Advance verb that lets a caller which
// assigned an identifier itself reserve it against every identifier the
// generator produces afterwards (go-qrdb#447).

// TestAtomicIncrementGenerator_Advance_RaisesTheCounter is the property
// Advance exists for: after Advance(n), no NextID may return a value at or
// below n.
func TestAtomicIncrementGenerator_Advance_RaisesTheCounter(t *testing.T) {
	gen := NewAtomicIncrementGenerator(0)
	require.Equal(t, uint64(1), gen.NextID(nil))

	gen.Advance(40)
	assert.Equal(t, uint64(41), gen.NextID(nil))
	assert.Equal(t, uint64(42), gen.NextID(nil))
}

// TestAtomicIncrementGenerator_Advance_IsANoOpWhenAlreadyPast pins the other
// half of the contract. A counter that could be lowered would reissue every
// identifier between the new value and the old one, which is worse than the
// defect Advance was added to fix.
func TestAtomicIncrementGenerator_Advance_IsANoOpWhenAlreadyPast(t *testing.T) {
	gen := NewAtomicIncrementGenerator(100)

	gen.Advance(100) // exactly at the target
	assert.Equal(t, uint64(101), gen.NextID(nil))

	gen.Advance(7) // below the target
	assert.Equal(t, uint64(102), gen.NextID(nil))

	gen.Advance(0)
	assert.Equal(t, uint64(103), gen.NextID(nil))
}

// TestAtomicIncrementGenerator_Advance_ConcurrentWithNextID runs the
// compare-and-swap loop against concurrent allocation. Every identifier
// handed out must still be unique, and the counter must still have ended up
// at or above every value it was advanced to.
func TestAtomicIncrementGenerator_Advance_ConcurrentWithNextID(t *testing.T) {
	gen := NewAtomicIncrementGenerator(0)

	const workers = 8
	const perWorker = 500

	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := make(map[uint64]struct{}, workers*perWorker)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if i%2 == 0 {
					gen.Advance(uint64(w * i))
					continue
				}
				id := gen.NextID(nil)
				mu.Lock()
				_, dup := seen[id]
				seen[id] = struct{}{}
				mu.Unlock()
				assert.False(t, dup, "NextID handed out %d twice", id)
			}
		}(w)
	}
	wg.Wait()

	// The counter never goes down, so a final Advance below the watermark
	// changes nothing and the next identifier is strictly above every one
	// already issued.
	gen.Advance(1)
	next := gen.NextID(nil)
	mu.Lock()
	defer mu.Unlock()
	for id := range seen {
		require.Greater(t, next, id, "the counter must never have been lowered")
	}
}

// TestIDSequence_OnlyCounterBackedGeneratorsImplementIt states the capability
// boundary as a test rather than only as a comment: a clock reading and a
// hash of a natural key are not drawn from a counter, so there is nothing for
// Advance to raise and the assertion a caller performs must fail for them.
func TestIDSequence_OnlyCounterBackedGeneratorsImplementIt(t *testing.T) {
	var counterBacked IDGenerator = NewAtomicIncrementGenerator(0)
	_, ok := counterBacked.(IDSequence)
	assert.True(t, ok, "AtomicIncrementGenerator is counter-backed and must satisfy IDSequence")

	for name, gen := range map[string]IDGenerator{
		"UnixNanoGenerator":  &UnixNanoGenerator{},
		"FieldHashGenerator": NewFieldHashGenerator("Name"),
	} {
		_, ok := gen.(IDSequence)
		assert.False(t, ok, "%s has no counter and must not claim IDSequence", name)
	}
}
