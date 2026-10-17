package seal

import (
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
)

// A round is also ended by its successor: roundLocked replaces a round whose
// roster is stale or whose TTL has lapsed before its expiry timer fired. The
// replaced round's shares and expiry goroutine end with it.

// TestQuorum_ReplacingAStaleRosterRoundDestroysItsShares replaces an unseal
// round opened on a roster another node's redistribution has since replaced:
// the next submission, made against the current roster, starts a new round
// and destroys the old round's shares.
func TestQuorum_ReplacingAStaleRosterRoundDestroysItsShares(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")
	clock := f.withManualClock(time.Hour)
	if _, err := f.submit(DomainUnseal, OpUnseal, "h1"); err != nil {
		t.Fatal(err)
	}
	shares := f.roundShares(DomainUnseal, OpUnseal)
	done := f.expiryDone(DomainUnseal, OpUnseal)
	// The stored roster is current; the round was opened on an older one, as
	// a redistribution replicated from another node leaves it here.
	f.b.quorumMu.Lock()
	f.b.rounds[quorumRoundKey{domain: DomainUnseal, op: OpUnseal}].rosterDigest = []byte("a replaced roster")
	f.b.quorumMu.Unlock()

	o, err := f.submit(DomainUnseal, OpUnseal, "h2")
	if err != nil {
		t.Fatal(err)
	}

	if o.Submitted != 1 || o.Complete {
		t.Fatalf("the stale round's share was counted: %+v", o)
	}
	requireDestroyed(t, shares)
	awaitSweep(t, done)
	if n := clock.Waiters(); n != 1 {
		t.Fatalf("%d expiry timers pending, want only the new round's", n)
	}
}

// TestQuorum_ReplacingALapsedRoundDestroysItsShares replaces an unseal round
// whose TTL has passed by the clock's reading but whose expiry timer has not
// fired: the next submission starts a new round and destroys the lapsed
// round's shares instead of counting them.
func TestQuorum_ReplacingALapsedRoundDestroysItsShares(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")
	opened := f.withManualClock(time.Minute)
	if _, err := f.submit(DomainUnseal, OpUnseal, "h1"); err != nil {
		t.Fatal(err)
	}
	shares := f.roundShares(DomainUnseal, OpUnseal)
	done := f.expiryDone(DomainUnseal, OpUnseal)
	// The reading moves past the round's TTL on a clock its timer is not
	// armed on, so the timer never fires and only the submission can end it.
	f.b.SetClock(clocktest.NewAt(opened.Now().Add(time.Minute)))

	o, err := f.submit(DomainUnseal, OpUnseal, "h2")
	if err != nil {
		t.Fatal(err)
	}

	if o.Submitted != 1 || o.Complete {
		t.Fatalf("the lapsed round's share was counted: %+v", o)
	}
	requireDestroyed(t, shares)
	awaitSweep(t, done)
	if opened.Waiters() != 0 {
		t.Fatal("the lapsed round's expiry timer is still pending")
	}
}

// TestClose_DestroysOpenRoundsOfAnUnsealedBarrier closes an unsealed barrier
// holding a seal round and a root-token round short of quorum: no share and
// no expiry goroutine outlives the barrier.
func TestClose_DestroysOpenRoundsOfAnUnsealedBarrier(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	clock := f.withManualClock(time.Hour)
	var shares []*shamir.ProtectedShare
	var done []<-chan struct{}
	for _, op := range []QuorumOp{OpSeal, OpRootToken} {
		if _, err := f.submit(DomainUnseal, op, "h1"); err != nil {
			t.Fatal(err)
		}
		shares = append(shares, f.roundShares(DomainUnseal, op)...)
		done = append(done, f.expiryDone(DomainUnseal, op))
	}

	if err := f.b.Close(); err != nil {
		t.Fatal(err)
	}

	requireDestroyed(t, shares)
	for _, d := range done {
		awaitSweep(t, d)
	}
	if n := f.openRounds(); n != 0 {
		t.Fatalf("%d rounds still open after Close", n)
	}
	if clock.Waiters() != 0 {
		t.Fatal("an expiry timer is still pending after Close")
	}
}
