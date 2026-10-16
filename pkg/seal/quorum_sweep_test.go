package seal

import (
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
)

// withManualClock puts the fixture's rounds on a fake clock and a known TTL.
// The fake fires a due expiry timer inside Advance, so nothing sleeps.
func (f *holderFixture) withManualClock(ttl time.Duration) *clocktest.Fake {
	f.t.Helper()
	c := clocktest.New()
	f.b.SetClock(c)
	f.b.config.Shamir.QuorumTTL = ttl
	return c
}

// expiryDone returns the channel the open round's expiry goroutine closes when
// it returns, so a test can wait for the sweep an Advance set off.
func (f *holderFixture) expiryDone(domain QuorumDomain, op QuorumOp) <-chan struct{} {
	f.t.Helper()
	f.b.quorumMu.Lock()
	defer f.b.quorumMu.Unlock()
	r, ok := f.b.rounds[quorumRoundKey{domain: domain, op: op}]
	if !ok {
		f.t.Fatalf("no open %s/%s round", domain, op)
	}
	return r.expiryDone
}

// awaitSweep waits for an expiry goroutine to return. The guard only bounds a
// broken sweep; a working one is already on its way when Advance returns.
func awaitSweep(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the round's expiry sweep never ran")
	}
}

// roundShares returns the share buffers the open round holds, before they are
// destroyed, so a test can check them afterwards.
func (f *holderFixture) roundShares(domain QuorumDomain, op QuorumOp) []*shamir.ProtectedShare {
	f.t.Helper()
	f.b.quorumMu.Lock()
	defer f.b.quorumMu.Unlock()
	r, ok := f.b.rounds[quorumRoundKey{domain: domain, op: op}]
	if !ok || len(r.shares) == 0 {
		f.t.Fatalf("no open %s/%s round holding shares", domain, op)
	}
	return append([]*shamir.ProtectedShare(nil), r.shares...)
}

// requireDestroyed fails unless every share buffer has been destroyed: its
// value can no longer be read.
func requireDestroyed(t *testing.T, shares []*shamir.ProtectedShare) {
	t.Helper()
	for i, s := range shares {
		if err := s.WithValue(func([]byte) error { return nil }); err == nil {
			t.Fatalf("share %d is still readable: its buffer was not destroyed", i)
		}
	}
}

func (f *holderFixture) openRounds() int {
	f.b.quorumMu.Lock()
	defer f.b.quorumMu.Unlock()
	return len(f.b.rounds)
}

func TestQuorum_ExpiredRoundIsSweptWithoutAnotherShare(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")
	clock := f.withManualClock(time.Minute)
	if _, err := f.submit(DomainUnseal, OpUnseal, "h1"); err != nil {
		t.Fatal(err)
	}
	shares := f.roundShares(DomainUnseal, OpUnseal)
	done := f.expiryDone(DomainUnseal, OpUnseal)

	clock.Advance(time.Minute)
	awaitSweep(t, done)

	requireDestroyed(t, shares)
	if n := f.openRounds(); n != 0 {
		t.Fatalf("%d rounds still open after expiry", n)
	}
	if clock.Waiters() != 0 {
		t.Fatal("an expiry timer is still pending")
	}
}

func TestQuorum_RoundBeforeItsTTLIsKept(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")
	clock := f.withManualClock(time.Minute)
	if _, err := f.submit(DomainUnseal, OpUnseal, "h1"); err != nil {
		t.Fatal(err)
	}

	clock.Advance(time.Minute - time.Nanosecond)

	if f.openRounds() != 1 {
		t.Fatal("the round was swept before its TTL")
	}
	o, err := f.submit(DomainUnseal, OpUnseal, "h2")
	if err != nil {
		t.Fatal(err)
	}
	if !o.Complete {
		t.Fatalf("the round did not complete inside its TTL: %+v", o)
	}
	if clock.Waiters() != 0 {
		t.Fatal("a completed round left its expiry timer pending")
	}
}

func TestQuorum_SweepLeavesANewerRoundAlone(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")
	clock := f.withManualClock(time.Minute)
	if _, err := f.submit(DomainUnseal, OpUnseal, "h1"); err != nil {
		t.Fatal(err)
	}
	done := f.expiryDone(DomainUnseal, OpUnseal)
	clock.Advance(time.Minute)
	awaitSweep(t, done)
	if _, err := f.submit(DomainUnseal, OpUnseal, "h1"); err != nil {
		t.Fatal(err)
	}
	shares := f.roundShares(DomainUnseal, OpUnseal)

	clock.Advance(time.Minute - time.Nanosecond)

	if f.openRounds() != 1 {
		t.Fatal("the first round's timer swept its successor")
	}
	for i, s := range shares {
		if err := s.WithValue(func([]byte) error { return nil }); err != nil {
			t.Fatalf("share %d of the live round was destroyed: %v", i, err)
		}
	}
}

func TestSeal_DestroysOpenRounds(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	clock := f.withManualClock(time.Hour)
	// A seal round opens while the barrier is unsealed and stays one share
	// short of its quorum.
	if _, err := f.submit(DomainUnseal, OpSeal, "h1"); err != nil {
		t.Fatal(err)
	}
	shares := f.roundShares(DomainUnseal, OpSeal)

	if err := f.b.Seal(); err != nil {
		t.Fatal(err)
	}

	requireDestroyed(t, shares)
	if n := f.openRounds(); n != 0 {
		t.Fatalf("%d rounds still open after Seal", n)
	}
	if clock.Waiters() != 0 {
		t.Fatal("an expiry timer is still pending after Seal")
	}
}

func TestClose_DestroysOpenRoundsOfASealedBarrier(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")
	f.withManualClock(time.Hour)
	// The barrier is already sealed, so Seal returns early: Close has to
	// destroy the unseal round itself.
	if _, err := f.submit(DomainUnseal, OpUnseal, "h1"); err != nil {
		t.Fatal(err)
	}
	shares := f.roundShares(DomainUnseal, OpUnseal)

	if err := f.b.Close(); err != nil {
		t.Fatal(err)
	}

	requireDestroyed(t, shares)
	if n := f.openRounds(); n != 0 {
		t.Fatalf("%d rounds still open after Close", n)
	}
}
