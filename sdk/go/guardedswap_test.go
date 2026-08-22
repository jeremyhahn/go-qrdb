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

package sdk

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
)

// The no-retry predicate: N writers claim one mark, exactly one wins.
//
// This is go-quicbaas's scheduler firing-claim shape. Losing the guard means
// losing the claim outright — there is nothing to retry, because the thing
// being claimed is gone — so Decide returns SwapLose the moment it sees the
// record occupied.
//
// The assertion that matters is "exactly one", not "at least one". The defect
// this whole helper exists to prevent is two writers both believing they
// claimed the same thing, which is what a read-modify-write under a
// process-local mutex produces on a multi-node store.
func TestGuardedSwap_NoRetryPredicateAdmitsExactlyOneWinner(t *testing.T) {
	const writers = 16
	plane := newFakeTxnPlane()
	// Hold every writer at its initial read, so all sixteen decide against the
	// same empty record and only the guard can separate them. Without this the
	// test passes with no guard at all — see fakeTxnPlane's gate.
	plane.gateReads(writers)
	key := []byte("__sys:claim/task-1")

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []string
		losses  int
		other   []error
	)

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			me := "worker-" + strconv.Itoa(id)
			res, err := GuardedSwap(context.Background(), plane, GuardedSwapRequest{
				Key: key,
				Decide: func(_ []byte, exists bool) ([]byte, SwapDecision, error) {
					if exists {
						// Someone already claimed it. A claim is not a
						// negotiation.
						return nil, SwapLose, nil
					}
					return []byte(me), SwapWrite, nil
				},
			})

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && res.Written:
				winners = append(winners, me)
			case isSwapLost(err):
				losses++
			default:
				other = append(other, err)
			}
		}(i)
	}
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors: %v", other)
	}
	if len(winners) != 1 {
		t.Fatalf("%d writers claimed the mark, want exactly 1: %v", len(winners), winners)
	}
	if losses != writers-1 {
		t.Errorf("%d writers reported a loss, want %d", losses, writers-1)
	}

	// And the record holds the winner's value, not some later writer's.
	stored, ok := plane.get(string(key))
	if !ok {
		t.Fatal("the mark is absent after a successful claim")
	}
	if string(stored) != winners[0] {
		t.Errorf("stored claim = %q, want the winner %q", stored, winners[0])
	}
}

// The retry predicate: N writers each hold a generation, the highest wins and
// nobody is left believing they won.
//
// This is go-quicbaas's SDLC lease shape. A writer that loses the guard
// re-reads and retries while the stored generation is still below its own, and
// concedes once a higher one is present — so every writer terminates, and the
// record converges on the maximum.
func TestGuardedSwap_RetryPredicateConvergesOnTheHighestGeneration(t *testing.T) {
	const writers = 12
	plane := newFakeTxnPlane()
	plane.gateReads(writers)
	key := []byte("__sys:lease/resource-1")

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		wrote    int
		conceded int
		other    []error
	)

	for gen := 1; gen <= writers; gen++ {
		wg.Add(1)
		go func(generation int) {
			defer wg.Done()
			res, err := GuardedSwap(context.Background(), plane, GuardedSwapRequest{
				Key: key,
				// Generous bound: with this many writers a low generation can
				// legitimately be beaten several times before it concedes.
				MaxAttempts: writers * 4,
				Decide: func(current []byte, exists bool) ([]byte, SwapDecision, error) {
					if exists {
						held, perr := strconv.Atoi(string(current))
						if perr != nil {
							return nil, SwapLose, perr
						}
						if held >= generation {
							// A newer holder is installed; this writer is done.
							return nil, SwapLose, nil
						}
					}
					return []byte(strconv.Itoa(generation)), SwapWrite, nil
				},
			})

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && res.Written:
				wrote++
			case isSwapLost(err):
				conceded++
			default:
				other = append(other, err)
			}
		}(gen)
	}
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors: %v", other)
	}
	if wrote+conceded != writers {
		t.Fatalf("%d writers accounted for, want %d", wrote+conceded, writers)
	}

	stored, ok := plane.get(string(key))
	if !ok {
		t.Fatal("the lease is absent after every writer finished")
	}
	if string(stored) != strconv.Itoa(writers) {
		t.Errorf("stored generation = %q, want the highest (%d)", stored, writers)
	}
}

// Concurrent CREATORS are guarded too, and by a different compare than
// concurrent updaters — CompareCreateRevision == 0 rather than a value or
// revision guard, because "has never existed" is not expressible as a
// comparison against the record's contents.
func TestGuardedSwap_ConcurrentCreatorsAdmitExactlyOneCreate(t *testing.T) {
	const writers = 16
	plane := newFakeTxnPlane()
	plane.gateReads(writers)
	key := []byte("__sys:singleton")

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
	)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			res, err := GuardedSwap(context.Background(), plane, GuardedSwapRequest{
				Key: key,
				Decide: func(_ []byte, exists bool) ([]byte, SwapDecision, error) {
					if exists {
						// Idempotent: the singleton is already there.
						return nil, SwapDone, nil
					}
					return []byte("created-by-" + strconv.Itoa(id)), SwapWrite, nil
				},
			})
			if err != nil {
				t.Errorf("worker %d: %v", id, err)
				return
			}
			if res.Written {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if created != 1 {
		t.Errorf("%d writers created the singleton, want exactly 1", created)
	}
}

// A guard refused while the record does not move is a FAULT, reported
// immediately, not retried to the attempt bound.
//
// Driven deterministically with a plane that always refuses and always serves
// the same record — the interrupted-takeover shape, where the store is not
// honouring the guard or is not evaluating the compare target in use. Retrying
// cannot change the outcome, so spinning to the bound would report contention
// for something that is not contention.
func TestGuardedSwap_RefusedWithoutMovementIsAStall(t *testing.T) {
	plane := &refusingPlane{
		serve: func(int) ([]byte, int64, bool) {
			return []byte("frozen"), 7, true
		},
	}

	res, err := GuardedSwap(context.Background(), plane, GuardedSwapRequest{
		Key:         []byte("k"),
		MaxAttempts: 25,
		Decide: func(_ []byte, _ bool) ([]byte, SwapDecision, error) {
			return []byte("mine"), SwapWrite, nil
		},
	})

	var stalled *SwapStalledError
	if !errors.As(err, &stalled) {
		t.Fatalf("error is %T (%v), want *SwapStalledError", err, err)
	}
	if res.Written {
		t.Error("a stalled swap reported a write")
	}
	if res.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 — a stall must be reported on the first "+
			"refusal, not spun to the bound", res.Attempts)
	}
	// One refused transaction, and two reads: the initial one and the
	// read-back that showed the record had not moved. Spinning to the bound
	// would show 25 transactions.
	if plane.calls != 1 || plane.reads != 2 {
		t.Errorf("round trips = %d transactions / %d reads, want 1 / 2",
			plane.calls, plane.reads)
	}
}

// A guard refused while the record DOES move on every attempt is contention,
// and it is bounded.
//
// Same driver as the stall case with one difference — the served record changes
// each time — which is what makes the two errors distinguishable rather than
// two names for "the guard kept failing".
func TestGuardedSwap_MovingRecordIsBoundedContention(t *testing.T) {
	const bound = 5
	plane := &refusingPlane{
		serve: func(call int) ([]byte, int64, bool) {
			// A different value and revision every time: real contention.
			return []byte("v" + strconv.Itoa(call)), int64(call), true
		},
	}

	res, err := GuardedSwap(context.Background(), plane, GuardedSwapRequest{
		Key:         []byte("k"),
		MaxAttempts: bound,
		Decide: func(_ []byte, _ bool) ([]byte, SwapDecision, error) {
			return []byte("mine"), SwapWrite, nil
		},
	})

	var contended *SwapContendedError
	if !errors.As(err, &contended) {
		t.Fatalf("error is %T (%v), want *SwapContendedError", err, err)
	}
	if res.Written {
		t.Error("a contended swap reported a write")
	}
	if contended.Attempts != bound || res.Attempts != bound {
		t.Errorf("attempts = %d/%d, want %d", contended.Attempts, res.Attempts, bound)
	}
	// `bound` refused transactions, and one more read than that: the initial
	// one plus a read-back after each refusal.
	if plane.calls != bound || plane.reads != bound+1 {
		t.Errorf("round trips = %d transactions / %d reads, want %d / %d",
			plane.calls, plane.reads, bound, bound+1)
	}
}

// The default ModRevision guard refuses an ABA swap; the opt-in value guard
// admits it.
//
// This is the test that justifies GuardOnValue defaulting to false. A record
// that goes A → B → A between the read and the swap is unchanged by value and
// changed by revision, so a value guard lets a swap land on top of a state its
// Decide never saw. Documenting the caveat is not the same as showing it, and a
// caller choosing between the two should be able to read what the difference
// actually does.
func TestGuardedSwap_ValueGuardAdmitsABAWhereTheRevisionGuardRefusesIt(t *testing.T) {
	for _, tt := range []struct {
		name         string
		guardOnValue bool
		wantWritten  bool
	}{
		{name: "revision guard refuses", guardOnValue: false, wantWritten: false},
		{name: "value guard admits", guardOnValue: true, wantWritten: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plane := newFakeTxnPlane()
			key := []byte("k")
			plane.put(string(key), []byte("A"))

			// The ABA happens between this caller's read and its write, which
			// is exactly where Decide sits.
			var once sync.Once
			res, err := GuardedSwap(context.Background(), plane, GuardedSwapRequest{
				Key:          key,
				MaxAttempts:  1,
				GuardOnValue: tt.guardOnValue,
				Decide: func(current []byte, _ bool) ([]byte, SwapDecision, error) {
					once.Do(func() {
						plane.put(string(key), []byte("B"))
						plane.put(string(key), []byte("A"))
					})
					if string(current) != "A" {
						t.Fatalf("Decide saw %q, want the pre-ABA value A", current)
					}
					return []byte("C"), SwapWrite, nil
				},
			})

			if res.Written != tt.wantWritten {
				t.Errorf("written = %v, want %v (err=%v)", res.Written, tt.wantWritten, err)
			}
			if tt.wantWritten && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantWritten && err == nil {
				t.Error("the revision guard admitted an ABA swap")
			}
		})
	}
}

// SwapDone stops without writing and without erroring, and costs one round
// trip.
func TestGuardedSwap_DoneWritesNothing(t *testing.T) {
	plane := newFakeTxnPlane()
	plane.put("k", []byte("already-correct"))

	res, err := GuardedSwap(context.Background(), plane, GuardedSwapRequest{
		Key: []byte("k"),
		Decide: func(current []byte, exists bool) ([]byte, SwapDecision, error) {
			if !exists || string(current) != "already-correct" {
				t.Fatalf("Decide saw (%q, %v)", current, exists)
			}
			return nil, SwapDone, nil
		},
	})
	if err != nil {
		t.Fatalf("GuardedSwap: %v", err)
	}
	if res.Written {
		t.Error("SwapDone wrote")
	}
	if res.Attempts != 0 {
		t.Errorf("attempts = %d, want 0", res.Attempts)
	}
	if plane.calls != 0 || plane.reads != 1 {
		t.Errorf("round trips = %d transactions / %d reads, want 0 / 1 (the read only)",
			plane.calls, plane.reads)
	}
	if v, _ := plane.get("k"); string(v) != "already-correct" {
		t.Errorf("record = %q, want it untouched", v)
	}
}

// An error from Decide is the caller's, and is passed through unwrapped rather
// than being reported as contention.
func TestGuardedSwap_DecideErrorIsPassedThrough(t *testing.T) {
	sentinel := errors.New("cannot decode this record")
	plane := newFakeTxnPlane()

	_, err := GuardedSwap(context.Background(), plane, GuardedSwapRequest{
		Key: []byte("k"),
		Decide: func([]byte, bool) ([]byte, SwapDecision, error) {
			return nil, SwapWrite, sentinel
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the sentinel from Decide", err)
	}
	var contended *SwapContendedError
	if errors.As(err, &contended) {
		t.Error("a Decide error was reported as contention")
	}
}

// A request that cannot be executed as written is a typed configuration error,
// never contention.
func TestGuardedSwap_RejectsAnUnusableRequest(t *testing.T) {
	plane := newFakeTxnPlane()
	decide := func([]byte, bool) ([]byte, SwapDecision, error) { return nil, SwapDone, nil }

	for _, tt := range []struct {
		name string
		req  GuardedSwapRequest
		nil  bool
	}{
		{name: "empty key", req: GuardedSwapRequest{Decide: decide}},
		{
			// Zero means "use the default"; negative means the caller computed
			// the bound wrongly, and silently substituting the default would
			// leave an invalid configuration looking valid.
			name: "negative MaxAttempts",
			req:  GuardedSwapRequest{Key: []byte("k"), Decide: decide, MaxAttempts: -1},
		},
		{name: "nil Decide", req: GuardedSwapRequest{Key: []byte("k")}},
		{name: "nil client", req: GuardedSwapRequest{Key: []byte("k"), Decide: decide}, nil: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.nil {
				_, err = GuardedSwap(context.Background(), nil, tt.req)
			} else {
				_, err = GuardedSwap(context.Background(), plane, tt.req)
			}
			var cfg *SwapConfigError
			if !errors.As(err, &cfg) {
				t.Fatalf("error is %T (%v), want *SwapConfigError", err, err)
			}
		})
	}
}

func isSwapLost(err error) bool {
	var lost *SwapLostError
	return errors.As(err, &lost)
}

// Zero MaxAttempts still selects the default, which is what makes the negative
// case above a distinction rather than a blanket rejection of anything
// non-positive.
func TestGuardedSwap_ZeroMaxAttemptsUsesTheDefault(t *testing.T) {
	plane := &refusingPlane{
		serve: func(call int) ([]byte, int64, bool) {
			return []byte("v" + strconv.Itoa(call)), int64(call), true
		},
	}

	res, err := GuardedSwap(context.Background(), plane, GuardedSwapRequest{
		Key:         []byte("k"),
		MaxAttempts: 0,
		Decide: func([]byte, bool) ([]byte, SwapDecision, error) {
			return []byte("mine"), SwapWrite, nil
		},
	})

	var contended *SwapContendedError
	if !errors.As(err, &contended) {
		t.Fatalf("error is %T (%v), want *SwapContendedError", err, err)
	}
	if res.Attempts != DefaultSwapAttempts {
		t.Errorf("attempts = %d, want DefaultSwapAttempts (%d)", res.Attempts, DefaultSwapAttempts)
	}
}
