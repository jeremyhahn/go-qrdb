package service

import (
	"context"
	"sync"
	"testing"
)

// TestValueSequenceService_ConcurrentAllocateIsUnique: over a dedicated
// store (value compare-and-swap, no transaction layer) concurrent Allocate
// calls hand out each value once, and Delete then Allocate restarts at 1.
func TestValueSequenceService_ConcurrentAllocateIsUnique(t *testing.T) {
	ctx := context.Background()
	seq := NewValueSequenceService(newDedicatedStoreKV(t))

	const n = 64
	got := make([]uint64, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for {
				got[i], _, errs[i] = seq.Allocate(ctx, "db", "s", 1)
				if _, conflict := errs[i].(*SequenceConflictError); !conflict {
					return
				}
			}
		}(i)
	}
	wg.Wait()

	seen := make(map[uint64]bool, n)
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("Allocate %d: %v", i, errs[i])
		}
		if seen[got[i]] {
			t.Fatalf("value %d allocated twice", got[i])
		}
		seen[got[i]] = true
	}
	if peek, err := seq.Peek(ctx, "db", "s"); err != nil || peek != n {
		t.Fatalf("Peek = %d, %v; want %d", peek, err, n)
	}

	if err := seq.Delete(ctx, "db", "s"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := seq.Delete(ctx, "db", "s"); err != nil {
		t.Fatalf("Delete of an absent sequence: %v", err)
	}
	if first, _, err := seq.Allocate(ctx, "db", "s", 1); err != nil || first != 1 {
		t.Fatalf("Allocate after Delete = %d, %v; want 1", first, err)
	}
}
