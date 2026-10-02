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

package server

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// The point of these tests is the DISCRIMINATOR, not the wait. go-qrdb#139 is
// five CI incidents in which "tenant shard did not elect a leader" was reported
// for a host that was merely too busy to run the poller, and each one cost a
// manual diagnosis to establish that. The verdict branches are therefore
// asserted directly, on constructed observations, so they are deterministic and
// need neither a raft group nor a starved machine -- a test for "reports host
// starvation correctly" that could only run on a starved host would be the same
// defect one level up.
//
// THE SAME ARGUMENT APPLIES TO THE WAIT'S OWN TIME (go-qrdb#312). Every test
// here that runs the real loop scripts its fixture BY POLL NUMBER -- fail on
// poll 1, fail again on poll 3 -- while the loop stops on a wall-clock budget.
// Those are the same quantity only on an idle host. On CI they diverged: a 30ms
// budget at a 1ms interval bought 2 polls where an idle host delivers 32, so
// poll 3 never ran and the test failed asserting on an error its fixture had
// never been asked to produce. The message it was checking was CORRECT --
// "budget 30ms, 2 polls, longest gap 36ms" is the starvation verdict, reported
// accurately about the very starvation that broke the test.
//
// So the loop now reads time through shardLeaderClock, and these tests supply
// virtualClock, under which time advances only when the loop sleeps and by
// exactly the amount it asked for. A poll count is then an exact consequence of
// the budget rather than a hope about the scheduler, and pollBudget converts
// between them so each test states the number of polls it actually needs. Do
// not "fix" a future flake here by enlarging a budget: under a virtual clock a
// budget cannot be missed, so a red here is a real defect.

// fakeLeaderSource is a shardLeaderSource whose answers the test controls.
type fakeLeaderSource struct {
	polls   atomic.Int64
	leader  bool
	err     error
	onPoll  func(n int64)
	leaderA int64 // become leader on this poll number (0 = never)
	// errOn makes GetLeaderID fail on exactly these poll numbers and answer
	// cleanly (no leader, no error) on every other one. That INTERMITTENT shape
	// is what a uniformly-erroring fake cannot express, and it is the shape that
	// caught the lastErr erasure -- see
	// TestShardLeaderWaitError_IntermittentPollErrorIsNotErased.
	errOn map[int64]error

	// reads counts ReadReady probes. readyAt is the probe on which a
	// linearizable read first succeeds -- the new leader has committed in
	// its term; every probe before it answers quicraft.ErrReadIndexNotReady,
	// exactly what a leader elected a moment ago answers. 0 means the first
	// probe succeeds. readErr, when set, is returned by every probe instead.
	reads   atomic.Int64
	readyAt int64
	readErr error
}

// ReadReady probes a linearizable read on shardID.
func (f *fakeLeaderSource) ReadReady(_ context.Context, _ uint64) error {
	n := f.reads.Add(1)
	if f.readErr != nil {
		return f.readErr
	}
	if n < f.readyAt {
		return quicraft.ErrReadIndexNotReady
	}
	return nil
}

func (f *fakeLeaderSource) GetLeaderID(shardID uint64) (uint64, bool, error) {
	n := f.polls.Add(1)
	if f.onPoll != nil {
		f.onPoll(n)
	}
	if e, ok := f.errOn[n]; ok {
		return 0, false, e
	}
	if f.err != nil {
		return 0, false, f.err
	}
	if f.leaderA > 0 && n >= f.leaderA {
		return 1, true, nil
	}
	if f.leader {
		return 1, true, nil
	}
	return 0, false, nil
}

// virtualClock is a shardLeaderClock in which time advances ONLY when the wait
// sleeps, and by exactly the interval it asked to sleep for. Nothing else can
// move it: not a loaded host, not a descheduled goroutine, not a slow poll.
//
// That makes every quantity the loop derives from time an exact function of the
// budget and interval it was given -- the poll count, Elapsed, and MaxGap -- so
// a fixture scripted by poll number always reaches the poll it scripted. It is
// the direct answer to go-qrdb#312, where the real clock ran out after two
// polls of a thirty-two-poll budget.
//
// It needs no lock: waitForShardServingWithin polls from the calling goroutine
// only, so Now and Sleep are called in sequence by that one goroutine. Each
// test constructs its own, so nothing is shared between tests either -- which
// is why this is a value passed in rather than a package-level clock variable.
//
// MaxGap under this clock is exactly one interval, so Starved() is false and
// the starvation branch cannot fire spuriously. The starvation branch itself is
// asserted on constructed observations in TestShardLeaderWaitError_Verdicts,
// which is the right place for it: reproducing real starvation to test the
// report of starvation is the defect this file exists to avoid.
type virtualClock struct {
	now time.Time
}

// newVirtualClock returns a clock at a fixed, arbitrary, non-zero instant. It
// is non-zero so that a caller reading Elapsed cannot confuse "no time passed"
// with "this clock was never started".
func newVirtualClock() *virtualClock {
	return &virtualClock{now: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)}
}

// Now returns the virtual instant, which changes only in Sleep.
func (c *virtualClock) Now() time.Time { return c.now }

// Sleep advances the virtual instant by d without blocking. The test spends no
// wall time at all, so these budgets stay honest on any host at any load.
func (c *virtualClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }

// pollBudget returns the timeout that makes waitForShardServingWithin perform
// exactly polls polls before it gives up, when it reads time through a
// virtualClock. polls must be at least 2.
//
// The derivation, which is worth stating because the off-by-one is not
// guessable: the loop reads the clock, polls, then tests the deadline, and only
// then sleeps. So poll k happens at start+(k-1)*interval, and the loop returns
// at the first k for which (k-1)*interval exceeds the budget. Setting the
// budget to (polls-2)*interval makes that first k equal polls.
//
// Every caller ALSO asserts the count it asked for. That is deliberate: the
// arithmetic above is a claim about the loop's shape, and a change to that
// shape must fail loudly and deterministically here rather than silently
// handing some test one poll fewer than its fixture scripts.
func pollBudget(polls int, interval time.Duration) time.Duration {
	if polls < 2 {
		panic("pollBudget: the loop always polls at least twice before the deadline can pass")
	}
	return time.Duration(polls-2) * interval
}

// virtualInterval is the poll interval these tests hand the wait. Under
// virtualClock it is a UNIT OF VIRTUAL TIME, not a delay -- no test here waits
// for it, and its magnitude changes nothing. It reads as a duration because the
// production parameter is one.
const virtualInterval = time.Millisecond

// wantPolls fails the test unless the wait asked the shard exactly n times.
// Under virtualClock the count is exact, so an inequality here is a defect and
// never a slow host.
func wantPolls(t *testing.T, we *ShardLeaderWaitError, n int) {
	t.Helper()
	if we.Polls != n {
		t.Fatalf("Polls = %d, want exactly %d. Under virtualClock the poll count is a "+
			"function of the budget alone, so this is a change in the loop's shape, "+
			"not host load", we.Polls, n)
	}
}

func TestWaitForShardLeader_ReturnsOnceLeaderElected(t *testing.T) {
	f := &fakeLeaderSource{leaderA: 3}
	// Budgeted for ten polls: a wait that ignored the elected leader would run
	// all ten and return a timeout error, so the early return is what is being
	// asserted rather than the budget running out at the right moment.
	if err := waitForShardServingWithin(context.Background(), f, 7, pollBudget(10, virtualInterval), virtualInterval, newVirtualClock()); err != nil {
		t.Fatalf("waitForShardServingWithin: %v", err)
	}
	if got := f.polls.Load(); got != 3 {
		t.Errorf("polls = %d, want 3 (it must stop at the first success, not keep polling)", got)
	}
}

// The sentinel is load-bearing: callers and tenant_host_errors_test.go assert
// on it, and this change must not break them while adding detail.
func TestShardLeaderWaitError_StillMatchesTheSentinel(t *testing.T) {
	f := &fakeLeaderSource{}
	err := waitForShardServingWithin(context.Background(), f, 7, pollBudget(4, virtualInterval), virtualInterval, newVirtualClock())
	if err == nil {
		t.Fatal("want a timeout error, got nil")
	}
	if !errors.Is(err, ErrTenantShardNoLeader) {
		t.Fatalf("errors.Is(err, ErrTenantShardNoLeader) = false; every existing caller checks this. err = %v", err)
	}
	var we *ShardLeaderWaitError
	if !errors.As(err, &we) {
		t.Fatalf("errors.As(*ShardLeaderWaitError) = false; err = %v", err)
	}
	if we.ShardID != 7 {
		t.Errorf("ShardID = %d, want 7", we.ShardID)
	}
	wantPolls(t, we, 4)
	if we.Elapsed == 0 {
		t.Error("Elapsed = 0; the error must record the time spent")
	}
}

// The defect this replaces: `if _, ok, err := host.GetLeaderID(id); err == nil
// && ok` discarded err entirely, so a shard answering "no such shard" 750 times
// was reported as one that failed to elect.
func TestShardLeaderWaitError_ReportsTheSwallowedPollError(t *testing.T) {
	boom := errors.New("shard 7 not found")
	f := &fakeLeaderSource{err: boom}

	err := waitForShardServingWithin(context.Background(), f, 7, pollBudget(4, virtualInterval), virtualInterval, newVirtualClock())
	var we *ShardLeaderWaitError
	if !errors.As(err, &we) {
		t.Fatalf("want *ShardLeaderWaitError, got %v", err)
	}
	wantPolls(t, we, 4)
	if !errors.Is(we.LastErr, boom) {
		t.Fatalf("LastErr = %v, want it to carry the GetLeaderID failure %v", we.LastErr, boom)
	}
	msg := we.Error()
	if !strings.Contains(msg, "COULD NOT BE ASKED") {
		t.Errorf("message must lead with the poll failure, not the timing.\ngot: %s", msg)
	}
	if !strings.Contains(msg, boom.Error()) {
		t.Errorf("message must quote the underlying error.\ngot: %s", msg)
	}
}

// The three verdicts, asserted on constructed observations so each branch is
// reachable without reproducing the condition that produces it in the wild.
//
// EVERY DURATION BELOW IS DATA, NOT A WAIT. These cases never run the poll loop
// and never touch a clock of any kind: they hand Error() and Starved() a
// finished measurement and read the verdict back. So no seam is needed here and
// none is missing -- there is nothing to seam. The same holds for
// TestShardLeaderWaitError_MessageCarriesTheMeasurement and
// TestShardLeaderWaitError_ZeroIntervalIsNotAutomaticallyStarved. Those, plus
// virtualInterval, are the only time literals left in this file, and no test
// here spends any wall time at all.
func TestShardLeaderWaitError_Verdicts(t *testing.T) {
	const interval = 20 * time.Millisecond

	cases := []struct {
		name       string
		err        *ShardLeaderWaitError
		wantStarve bool
		wantPhrase string
		denyPhrase string
	}{
		{
			name: "attentive poller that never saw a leader is a REAL failure",
			err: &ShardLeaderWaitError{
				ShardID: 1, Timeout: 15 * time.Second, Interval: interval,
				Elapsed: 15 * time.Second, Polls: 750, MaxGap: 21 * time.Millisecond,
			},
			wantStarve: false,
			wantPhrase: "GENUINELY DID NOT ELECT",
			denyPhrase: "STARVED",
		},
		{
			name: "a gap of ten intervals is starvation, not an election defect",
			err: &ShardLeaderWaitError{
				ShardID: 1, Timeout: 15 * time.Second, Interval: interval,
				Elapsed: 15 * time.Second, Polls: 40, MaxGap: 200 * time.Millisecond,
			},
			wantStarve: true,
			wantPhrase: "POLLER WAS STARVED",
			denyPhrase: "GENUINELY DID NOT ELECT",
		},
		{
			name: "one tick below the threshold is still a real failure",
			err: &ShardLeaderWaitError{
				ShardID: 1, Timeout: 15 * time.Second, Interval: interval,
				Elapsed: 15 * time.Second, Polls: 700, MaxGap: 199 * time.Millisecond,
			},
			wantStarve: false,
			wantPhrase: "GENUINELY DID NOT ELECT",
			denyPhrase: "STARVED",
		},
		{
			name: "a poll error supersedes both timing verdicts",
			err: &ShardLeaderWaitError{
				ShardID: 1, Timeout: 15 * time.Second, Interval: interval,
				Elapsed: 15 * time.Second, Polls: 40, MaxGap: 5 * time.Second,
				// ErrCount travels with LastErr: the loop only records one
				// alongside the other, so a fixture that sets just LastErr
				// describes a state that cannot occur.
				ErrCount: 40, LastErr: errors.New("shard 1 not found"),
			},
			wantStarve: true, // the measurement still says starved ...
			wantPhrase: "COULD NOT BE ASKED",
			denyPhrase: "STARVED", // ... but the message must not lead with it
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Starved(); got != tc.wantStarve {
				t.Errorf("Starved() = %v, want %v (MaxGap %v against interval %v)",
					got, tc.wantStarve, tc.err.MaxGap, tc.err.Interval)
			}
			msg := tc.err.Error()
			if !strings.Contains(msg, tc.wantPhrase) {
				t.Errorf("message must contain %q.\ngot: %s", tc.wantPhrase, msg)
			}
			if tc.denyPhrase != "" && strings.Contains(msg, tc.denyPhrase) {
				t.Errorf("message must NOT contain %q -- the verdicts would be ambiguous.\ngot: %s",
					tc.denyPhrase, msg)
			}
		})
	}
}

// The message is read by someone deciding whether to re-run or to investigate,
// so it has to carry the numbers that decision rests on.
func TestShardLeaderWaitError_MessageCarriesTheMeasurement(t *testing.T) {
	we := &ShardLeaderWaitError{
		ShardID: 42, Timeout: 15 * time.Second, Interval: 20 * time.Millisecond,
		Elapsed: 15200 * time.Millisecond, Polls: 37, MaxGap: 900 * time.Millisecond,
	}
	msg := we.Error()
	for _, want := range []string{"42", "15.2s", "15s", "37 polls", "900ms"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message must carry %q so the reader can judge it without the job log.\ngot: %s", want, msg)
		}
	}
}

// A zero Interval must not make every observation look starved: MaxGap >= 0 is
// trivially true, and a verdict that is always "the host was busy" is a verdict
// that never says anything.
func TestShardLeaderWaitError_ZeroIntervalIsNotAutomaticallyStarved(t *testing.T) {
	we := &ShardLeaderWaitError{ShardID: 1, MaxGap: 0, Interval: 0}
	if we.Starved() {
		t.Error("Starved() = true for a zero interval; the threshold must not degenerate")
	}
}

// THE ERASURE. `lastErr = err` written unconditionally is overwritten by the
// very next poll that answers (ok=false, err=nil), so on the sequence
//
//	poll 1  error
//	poll 2..N  no leader, no error
//	timeout
//
// the query failure is lost and the wait reports that the shard "GENUINELY DID
// NOT ELECT" -- naming a cause it did not establish, which is precisely the
// defect this whole error type exists to prevent.
//
// The pre-existing tests could not catch it: a fake that errors on EVERY poll
// keeps the field non-nil by luck, so the fixture's uniformity was hiding the
// bug. This one scripts a single early failure among clean polls.
func TestShardLeaderWaitError_IntermittentPollErrorIsNotErased(t *testing.T) {
	boom := errors.New("shard 7 not found")
	f := &fakeLeaderSource{errOn: map[int64]error{1: boom}}

	// Five polls: the failure is on poll 1 and polls 2-5 answer cleanly, which
	// is the sequence that erases lastErr when the assignment is unguarded. It
	// is an exact count under virtualClock, so the clean polls after the
	// failing one are guaranteed rather than hoped for.
	err := waitForShardServingWithin(context.Background(), f, 7, pollBudget(5, virtualInterval), virtualInterval, newVirtualClock())
	var we *ShardLeaderWaitError
	if !errors.As(err, &we) {
		t.Fatalf("want *ShardLeaderWaitError, got %v", err)
	}
	wantPolls(t, we, 5)
	if !errors.Is(we.LastErr, boom) {
		t.Fatalf("LastErr = %v, want %v -- a later clean poll erased the real "+
			"failure, so the verdict below is about a cause never established",
			we.LastErr, boom)
	}
	if we.ErrCount != 1 {
		t.Errorf("ErrCount = %d, want 1", we.ErrCount)
	}

	msg := we.Error()
	if !strings.Contains(msg, "COULD NOT BE ASKED") {
		t.Errorf("message must name the query failure.\ngot: %s", msg)
	}
	if strings.Contains(msg, "GENUINELY DID NOT ELECT") {
		t.Errorf("message must NOT claim a failed election: one poll never got "+
			"an answer, so no election verdict is supportable.\ngot: %s", msg)
	}
	// The count is the actionable half: it sends a reader to a different
	// investigation than a persistent failure would.
	if !strings.Contains(msg, "INTERMITTENT") {
		t.Errorf("message must distinguish intermittent from persistent.\ngot: %s", msg)
	}
	if !strings.Contains(msg, "1 of ") {
		t.Errorf("message must carry the failure count.\ngot: %s", msg)
	}
}

// The counterpart: every poll failing is a DIFFERENT finding and must read
// differently, or the count adds nothing.
func TestShardLeaderWaitError_PersistentPollErrorReadsAsPersistent(t *testing.T) {
	f := &fakeLeaderSource{err: errors.New("shard 7 not found")}
	err := waitForShardServingWithin(context.Background(), f, 7, pollBudget(5, virtualInterval), virtualInterval, newVirtualClock())
	var we *ShardLeaderWaitError
	if !errors.As(err, &we) {
		t.Fatalf("want *ShardLeaderWaitError, got %v", err)
	}
	wantPolls(t, we, 5)
	if we.ErrCount != we.Polls {
		t.Fatalf("ErrCount = %d, Polls = %d; want them equal", we.ErrCount, we.Polls)
	}
	msg := we.Error()
	if !strings.Contains(msg, "persistent") {
		t.Errorf("message must read as persistent.\ngot: %s", msg)
	}
	if strings.Contains(msg, "INTERMITTENT") {
		t.Errorf("a total failure must not read as intermittent.\ngot: %s", msg)
	}
}

// When the shard fails one way and later another, the last error alone hides
// half of it.
func TestShardLeaderWaitError_ReportsFirstAndLastWhenTheyDiffer(t *testing.T) {
	first := errors.New("shard 7 not found")
	later := errors.New("shard 7 closed")
	f := &fakeLeaderSource{errOn: map[int64]error{1: first, 3: later}}

	// THE go-qrdb#312 CASE. The fixture scripts poll 3, so the wait must reach
	// poll 3. Under the wall clock that was a bet on the scheduler and CI lost
	// it: 30ms bought two polls. Under virtualClock five polls is arithmetic.
	err := waitForShardServingWithin(context.Background(), f, 7, pollBudget(5, virtualInterval), virtualInterval, newVirtualClock())
	var we *ShardLeaderWaitError
	if !errors.As(err, &we) {
		t.Fatalf("want *ShardLeaderWaitError, got %v", err)
	}
	wantPolls(t, we, 5)
	if we.ErrCount != 2 {
		t.Fatalf("ErrCount = %d, want 2 -- polls 1 and 3 were both scripted to fail; "+
			"a lower count means the wait never reached the poll carrying the second error", we.ErrCount)
	}
	msg := we.Error()
	if !strings.Contains(msg, later.Error()) {
		t.Errorf("message must carry the last failure.\ngot: %s", msg)
	}
	if !strings.Contains(msg, first.Error()) {
		t.Errorf("message must carry the differing FIRST failure too.\ngot: %s", msg)
	}
}

// TestWaitForShardLeader_WaitsForTheTermsFirstCommit reproduces the race the
// host-open paths lost under load, deterministically: the shard elects a leader
// at once, but a linearizable read is refused (ErrReadIndexNotReady,
// "commitPending") until that leader has committed an entry in its term --
// here, until the third probe. A wait that returns on "leader known" hands
// the caller a shard whose first read fails; the wait must instead return
// only once a read succeeds.
func TestWaitForShardLeader_WaitsForTheTermsFirstCommit(t *testing.T) {
	f := &fakeLeaderSource{leader: true, readyAt: 3}
	if err := waitForShardServingWithin(context.Background(), f, 7, pollBudget(10, virtualInterval), virtualInterval, newVirtualClock()); err != nil {
		t.Fatalf("waitForShardServingWithin: %v", err)
	}
	if got := f.reads.Load(); got != 3 {
		t.Fatalf("read probes = %d, want 3: the wait returned before the shard could serve a linearizable read", got)
	}
	if err := f.ReadReady(context.Background(), 7); err != nil {
		t.Fatalf("the first read after the wait failed: %v", err)
	}
}

// A leader that never commits in its term times out with *ShardReadWaitError:
// the probes are counted within the one budget, every one answered
// not-ready, and the verdict is the genuine one -- no probe error, no
// starvation.
func TestShardReadWaitError_LeaderThatNeverCommits(t *testing.T) {
	f := &fakeLeaderSource{leader: true, readyAt: 1 << 62}
	err := waitForShardServingWithin(context.Background(), f, 7, pollBudget(6, virtualInterval), virtualInterval, newVirtualClock())
	var we *ShardReadWaitError
	if !errors.As(err, &we) {
		t.Fatalf("err = %v, want *ShardReadWaitError", err)
	}
	if !errors.Is(err, ErrShardNotReadReady) {
		t.Fatal("ShardReadWaitError does not unwrap to ErrShardNotReadReady")
	}
	// The first probe runs at the instant the leader poll succeeded -- no sleep
	// between the phases -- so the probes get the budget's six slots.
	if f.polls.Load() != 1 || we.Polls != 6 || we.ErrCount != 0 || we.LastErr != nil {
		t.Fatalf("leader polls %d, read probes %d, errors %d (%v); want 1, 6, 0",
			f.polls.Load(), we.Polls, we.ErrCount, we.LastErr)
	}
	if msg := we.Error(); !strings.Contains(msg, "GENUINELY DID NOT COMMIT") || !strings.Contains(msg, "shard 7") {
		t.Fatalf("message %q lacks the verdict", msg)
	}
}

// A probe that fails for another reason is the finding, and is reported.
func TestShardReadWaitError_ReportsTheProbeError(t *testing.T) {
	boom := errors.New("shard failed")
	f := &fakeLeaderSource{leader: true, readErr: boom}
	err := waitForShardServingWithin(context.Background(), f, 7, pollBudget(4, virtualInterval), virtualInterval, newVirtualClock())
	var we *ShardReadWaitError
	if !errors.As(err, &we) || !errors.Is(we.LastErr, boom) || we.ErrCount != we.Polls {
		t.Fatalf("err = %v, want every probe's error reported", err)
	}
	if msg := we.Error(); !strings.Contains(msg, "read probe FAILED") || !strings.Contains(msg, "shard failed") {
		t.Fatalf("message %q lacks the probe error", msg)
	}
}

// The verdicts are asserted on constructed observations, as for the leader
// wait: starvation outranks the genuine verdict, and differing first and last
// errors are both reported.
func TestShardReadWaitError_Verdicts(t *testing.T) {
	starved := &ShardReadWaitError{ShardID: 3, Timeout: time.Second, Interval: 20 * time.Millisecond, Polls: 2, MaxGap: time.Second}
	if !starved.Starved() || !strings.Contains(starved.Error(), "POLLER WAS STARVED") {
		t.Fatalf("starved observation read as %q", starved.Error())
	}
	both := &ShardReadWaitError{ShardID: 3, Polls: 3, ErrCount: 2, FirstErr: errors.New("first"), LastErr: errors.New("last")}
	if msg := both.Error(); !strings.Contains(msg, "first failure differed: first") {
		t.Fatalf("message %q drops the first error", msg)
	}
}

// A cancelled context ends the wait with the context's error in either phase.
func TestWaitForShardServing_HonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, f := range map[string]*fakeLeaderSource{
		"no leader yet":  {},
		"not read-ready": {leader: true, readyAt: 1 << 62},
	} {
		if err := waitForShardServingWithin(ctx, f, 7, pollBudget(10, virtualInterval), virtualInterval, newVirtualClock()); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s: err = %v, want context.Canceled", name, err)
		}
	}
}
