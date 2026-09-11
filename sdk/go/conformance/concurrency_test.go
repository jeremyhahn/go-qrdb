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

package conformance

import (
	"fmt"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// TestGuardedVerbConcurrency is the go-qrdb#276 deliverable.
//
// TestGuardedVerbConformanceMatrix calls each guarded verb SEQUENTIALLY --
// once, observe, once more -- so the strongest thing it can certify is that
// the answers come back correct and in order. A get-then-put EMULATION
// passes that battery too: called alone on an absent key it stores and
// reports true, called again it correctly declines. The two implementations
// are indistinguishable until something calls the verb TWICE AT ONCE, which
// the matrix never does. See the doc comment on TestGuardedVerbConformanceMatrix
// for the property that stays certified and the property that does not.
//
// This suite is the thing that actually contends: for each guarded verb, N
// proposers call it on the SAME key at the SAME time, released together off
// one starting gate, and the suite asserts the one invariant an emulation
// cannot produce under contention: exactly one of them wins, and the store
// afterwards holds that winner's write.
//
// ONE ROUND IS NOT ENOUGH, AND THAT IS MEASURED, NOT ASSUMED (go-qrdb#276).
// A get-then-put emulation only loses the race when two proposers are BOTH
// inside its Get-to-Put window at the same instant, and whether that happens
// on a given round depends on real scheduling -- exactly the "passes only on
// an idle/lucky host" shape this repo's testing conventions warn against for
// a single live trial. Measured against the mutation this file's doc records
// below: at concurrentProposers=16, a single round MISSED the race on
// roughly a third of local attempts; at concurrentProposers=64 a single
// round still missed on multiple attempts (raising N did not help much once
// the client-side goroutine count exceeded what the round-trip time could
// spread across). What closes the gap is independent ROUNDS: each round is a
// fresh key and its own fair coin flip against the bug, so the miss
// probability multiplies down every round the suite adds, and a genuine
// atomic implementation cannot fail ANY round no matter how many are run --
// it has no scheduling-dependent failure mode to be lucky about.
// concurrencyRounds below was chosen from that measurement, not guessed; see
// its own doc comment for the arithmetic and the commit message for the
// per-attempt transcript.
//
// WHY NOT GATE THE READS INSTEAD, per docs/guarded-swap/engineering.md's
// testing notes: that file's own lesson is exactly on point --
// "any 'exactly one winner' assertion over a fast in-process fake needs a
// barrier that holds every participant at the read, or it is asserting the
// scheduler's behaviour rather than the code's" -- and its fakeTxnPlane
// gates every participant at the read point so the race is forced rather
// than hoped for. That technique needs a seam INSIDE the code under test,
// which is available there because the test owns the fake transaction
// plane. It is not available here: this suite is black-box over six real
// transports and a real server process, with no hook into
// pkg/api/*/handlers_kv.go to hold a request mid-flight, and adding one
// would be test-only production code for the sake of the test that finds
// it, which this repository's architecture does not do. Independent rounds
// is the substitute that works without one: it cannot force the interleave
// on any single attempt, but it needs it to happen only once across
// concurrencyRounds independent attempts, and the measurement above is what
// says how many that takes.
//
// One cell per transport, standalone mode, the memory backend: this suite
// tests the TRANSPORT-TO-SERVICE delegation path -- does the handler call the
// atomic verb, or does it reconstruct one out of a Get and a Put? -- not the
// storage engine underneath. The state machine's own atomicity comes from
// applying one proposal at a time off a single committed log position
// (service.KVService.PutIfAbsent / CompareAndSwap / CompareAndDelete encode
// ONE command and propose it ONCE), which is a property of that package, not
// of the transport, and every cell of a given transport is served by the
// same handler regardless of which engine or deployment mode answers it. So
// contending on every matrix cell would spend CPU without widening what
// could be caught. Standalone is chosen over raft for the same reason the
// matrix avoids raft where it can: no leader election, and no risk of the
// concurrent load itself starving a single-member group's apply loop and
// turning an atomicity question into a liveness one.
func TestGuardedVerbConcurrency(t *testing.T) {
	for _, tk := range transportKinds {
		c := cell{backend: database.BackendMemory, mode: standalone, transport: tk, want: fullPassage}
		t.Run(string(tk), func(t *testing.T) {
			cl := c.boot(t)
			t.Run("PutIfAbsent", func(t *testing.T) { assertPutIfAbsentIsAtomic(t, cl) })
			t.Run("CompareAndSwap", func(t *testing.T) { assertCompareAndSwapIsAtomic(t, cl) })
			t.Run("CompareAndDelete", func(t *testing.T) { assertCompareAndDeleteIsAtomic(t, cl) })
		})
	}
}

// concurrentProposers is how many callers race for the same guarded verb on
// the same key within one round.
const concurrentProposers = 16

// concurrencyRounds is how many independent rounds each verb's race is run,
// each on its own fresh key.
//
// This is the number that actually buys detection, not concurrentProposers
// (see TestGuardedVerbConcurrency's doc comment for the measurement). Treat
// the two as separate knobs: proposers is bounded by what one round can
// usefully spread over a loopback round trip before client-side goroutine
// scheduling itself becomes the bottleneck, rounds is bounded only by test
// time and is where raising the number keeps paying off, because each round
// is an independent trial against the same bug.
//
// Measured (go-qrdb#276): 20 back-to-back attempts of the full mutation
// transcript recorded in the commit message, at concurrentProposers=16 and
// concurrencyRounds=20, caught the get-then-put emulation on every attempt.
// A single round alone (concurrencyRounds=1) missed it on a visible fraction
// of attempts at the same proposer count.
const concurrencyRounds = 20

// raceGuardedVerb releases concurrentProposers goroutines off one starting
// gate and collects each call's (bool, error) into a private slot, so the
// goroutines share no mutable state and the collection itself needs no lock
// -- which matters under `go test -race`, since this suite's whole job is to
// prove a race exists on the OTHER side of the call.
func raceGuardedVerb(call func(i int) (bool, error)) (stored []bool, errs []error) {
	stored = make([]bool, concurrentProposers)
	errs = make([]error, concurrentProposers)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range concurrentProposers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			stored[i], errs[i] = call(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return stored, errs
}

// assertExactlyOneWinner is the shared verdict for one round of any of the
// three races: fail on any call error (a race test that swallowed a
// proposer's error could not tell "lost cleanly" from "never actually ran"),
// then require the boolean results to carry EXACTLY one true.
//
// The message reports the measured count against the required one, per the
// go-qrdb#254 finding that a verdict alone is not auditable -- a reader can
// check "3 of 16" against the invariant without re-running anything.
func assertExactlyOneWinner(t *testing.T, verb, key string, round int, stored []bool, errs []error) (winner int) {
	t.Helper()
	winner = -1
	winners := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("round %d, proposer %d: %s(%q): %v", round, i, verb, key, err)
		}
		if stored[i] {
			winners++
			winner = i
		}
	}
	if winners != 1 {
		t.Fatalf("round %d: %d of %d concurrent %s(%q) calls reported success, want exactly 1 -- "+
			"this is the guarantee a guarded verb exists to provide under contention; a get-then-put "+
			"emulation lets more than one caller believe it won", round, winners, concurrentProposers, verb, key)
	}
	return winner
}

// assertPutIfAbsentIsAtomic races concurrentProposers PutIfAbsent calls, each
// with a distinguishable value, onto one absent key -- concurrencyRounds
// times, each on its own fresh key.
func assertPutIfAbsentIsAtomic(t *testing.T, cl clients) {
	t.Helper()
	ctx := boundedCtx(t)

	for round := range concurrencyRounds {
		key := fmt.Sprintf("concurrency/put-if-absent/%d", round)
		values := make([]string, concurrentProposers)
		for i := range concurrentProposers {
			values[i] = fmt.Sprintf("proposer-%d", i)
		}

		stored, errs := raceGuardedVerb(func(i int) (bool, error) {
			return cl.guarded.PutIfAbsent(ctx, key, []byte(values[i]))
		})
		winner := assertExactlyOneWinner(t, "PutIfAbsent", key, round, stored, errs)

		got, err := cl.kv.Get(ctx, key)
		if err != nil {
			t.Fatalf("round %d: Get(%q) after the race: %v", round, key, err)
		}
		if string(got) != values[winner] {
			t.Fatalf("round %d: the store holds %q after the race, but proposer %d was reported "+
				"as the winner (value %q) -- the reported winner's own write did not survive",
				round, got, winner, values[winner])
		}
	}
}

// assertCompareAndSwapIsAtomic seeds a key, then races concurrentProposers
// CompareAndSwap calls that all expect the SAME seeded value -- so at most
// one can observe a match, since the first swap to apply changes the value
// every later comparison is decided against. Run concurrencyRounds times,
// each on its own fresh key.
func assertCompareAndSwapIsAtomic(t *testing.T, cl clients) {
	t.Helper()
	ctx := boundedCtx(t)
	const seed = "seed"

	for round := range concurrencyRounds {
		key := fmt.Sprintf("concurrency/compare-and-swap/%d", round)
		if err := cl.kv.Put(ctx, key, []byte(seed)); err != nil {
			t.Fatalf("round %d: seed Put(%q): %v", round, key, err)
		}

		nexts := make([]string, concurrentProposers)
		for i := range concurrentProposers {
			nexts[i] = fmt.Sprintf("swapper-%d", i)
		}

		swapped, errs := raceGuardedVerb(func(i int) (bool, error) {
			return cl.guarded.CompareAndSwap(ctx, key, []byte(seed), []byte(nexts[i]))
		})
		winner := assertExactlyOneWinner(t, "CompareAndSwap", key, round, swapped, errs)

		got, err := cl.kv.Get(ctx, key)
		if err != nil {
			t.Fatalf("round %d: Get(%q) after the race: %v", round, key, err)
		}
		if string(got) != nexts[winner] {
			t.Fatalf("round %d: the store holds %q after the race, but proposer %d was reported "+
				"as the winner (value %q) -- the reported winner's own write did not survive",
				round, got, winner, nexts[winner])
		}
	}
}

// assertCompareAndDeleteIsAtomic seeds a key, then races concurrentProposers
// CompareAndDelete calls that all expect the same seeded value -- at most one
// can delete it, since the first to apply removes the key every later
// comparison would need to find. Run concurrencyRounds times, each on its own
// fresh key.
func assertCompareAndDeleteIsAtomic(t *testing.T, cl clients) {
	t.Helper()
	ctx := boundedCtx(t)
	const seed = "seed"

	for round := range concurrencyRounds {
		key := fmt.Sprintf("concurrency/compare-and-delete/%d", round)
		if err := cl.kv.Put(ctx, key, []byte(seed)); err != nil {
			t.Fatalf("round %d: seed Put(%q): %v", round, key, err)
		}

		deleted, errs := raceGuardedVerb(func(int) (bool, error) {
			return cl.guarded.CompareAndDelete(ctx, key, []byte(seed))
		})
		assertExactlyOneWinner(t, "CompareAndDelete", key, round, deleted, errs)

		assertKeyAbsent(t, cl, key, fmt.Sprintf("round %d: a key that exactly one concurrent "+
			"CompareAndDelete deleted must be absent afterward", round))
	}
}
