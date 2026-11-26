package pinhook

import (
	"context"
	"sync"
	"testing"
)

func TestRecordUnderRecorderKeepsAliases(t *testing.T) {
	var r Recorder
	ctx := With(context.Background(), &r)
	first := []byte("1234")
	second := []byte("5678")
	Record(ctx, first)
	Record(ctx, second)

	got := r.Copies()
	if len(got) != 2 {
		t.Fatalf("Copies() returned %d entries, want 2", len(got))
	}
	if string(got[0]) != "1234" || string(got[1]) != "5678" {
		t.Fatalf("Copies() = %q, want [1234 5678]", got)
	}
	// Record keeps an alias, so a wipe of the caller's slice is visible.
	first[0] = 0
	if got[0][0] != 0 {
		t.Fatal("Record kept a copy, want an alias to the recorded slice")
	}
}

func TestRecordWithoutRecorderRecordsNothing(t *testing.T) {
	// Must not panic and must not reach any recorder.
	Record(context.Background(), []byte("1234"))

	var r Recorder
	if got := r.Copies(); len(got) != 0 {
		t.Fatalf("Copies() on an unused recorder = %q, want empty", got)
	}
}

func TestRecordWithNilRecorderRecordsNothing(t *testing.T) {
	ctx := With(context.Background(), nil)
	Record(ctx, []byte("1234")) // must not panic
}

func TestCopiesReturnsFreshSlice(t *testing.T) {
	var r Recorder
	ctx := With(context.Background(), &r)
	Record(ctx, []byte("1234"))

	got := r.Copies()
	got[0] = nil
	_ = append(got, []byte("extra"))

	again := r.Copies()
	if len(again) != 1 || string(again[0]) != "1234" {
		t.Fatalf("Copies() after mutating a previous result = %q, want [1234]", again)
	}
}

func TestParallelRecordKeepsEveryCall(t *testing.T) {
	const n = 64
	var r Recorder
	ctx := With(context.Background(), &r)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(b byte) {
			defer wg.Done()
			Record(ctx, []byte{b})
		}(byte(i))
	}
	wg.Wait()

	got := r.Copies()
	if len(got) != n {
		t.Fatalf("Copies() returned %d entries, want %d", len(got), n)
	}
	seen := make(map[byte]bool, n)
	for _, pin := range got {
		seen[pin[0]] = true
	}
	if len(seen) != n {
		t.Fatalf("recorded %d distinct pins, want %d", len(seen), n)
	}
}
