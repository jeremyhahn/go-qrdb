package seal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"
)

// Share custody on every exit a quorum round has. A share is held in a
// protected buffer from the moment admitLocked accepts it, and the caller's
// plain buffer is wiped whatever the submission's outcome. Expiry, Seal and
// Close are covered in quorum_sweep_test.go; the exits below are completion,
// a failed reconstruction, replacement or deletion of the share set the round
// was opened on, and the unseal that leaves a round unable to run.

// submitBuf sends share, a buffer the test keeps a reference to, as holder
// with a valid proof, so the test can check what the barrier left in it.
func (f *holderFixture) submitBuf(domain QuorumDomain, op QuorumOp, holder string, share []byte) (*QuorumOutcome, error) {
	f.t.Helper()
	_, digest, err := f.b.HolderRoster(context.Background(), domain)
	if err != nil {
		return nil, err
	}
	return f.b.SubmitHolderShare(context.Background(), &HolderSubmission{
		Domain: domain,
		Op:     op,
		Holder: holder,
		Proof:  f.verifier.issue(domain, op, holder, digest),
		Share:  share,
	})
}

// requireWiped fails unless every byte of a submitted share buffer is zero:
// the barrier leaves no plain copy behind in the caller's buffer.
func requireWiped(t *testing.T, buf []byte) {
	t.Helper()
	if len(buf) == 0 {
		t.Fatal("no share buffer to check")
	}
	for _, c := range buf {
		if c != 0 {
			t.Fatal("the submitted share buffer was not wiped")
		}
	}
}

// requireReadable fails unless every share buffer can still be read.
func requireReadable(t *testing.T, shares []*shamir.ProtectedShare) {
	t.Helper()
	for i, s := range shares {
		if err := s.WithValue(func([]byte) error { return nil }); err != nil {
			t.Fatalf("share %d of a live round was destroyed: %v", i, err)
		}
	}
}

func TestQuorum_CompletionDestroysEveryShareAndReconstructs(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")
	clock := f.withManualClock(time.Hour)

	first := []byte(f.dist.share(DomainUnseal, "h1"))
	if _, err := f.submitBuf(DomainUnseal, OpUnseal, "h1", first); err != nil {
		t.Fatal(err)
	}
	requireWiped(t, first)
	shares := f.roundShares(DomainUnseal, OpUnseal)

	last := []byte(f.dist.share(DomainUnseal, "h2"))
	o, err := f.submitBuf(DomainUnseal, OpUnseal, "h2", last)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Complete || f.b.IsSealed() {
		t.Fatalf("the quorum did not reconstruct the key and unseal: %+v", o)
	}
	requireWiped(t, last)
	requireDestroyed(t, shares)
	if n := f.openRounds(); n != 0 {
		t.Fatalf("%d rounds still open after completion", n)
	}
	if clock.Waiters() != 0 {
		t.Fatal("a completed round left its expiry timer pending")
	}
}

func TestQuorum_FailedReconstructionDestroysEveryShare(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)
	stale := []byte(f.dist.share(DomainUnseal, "h2"))
	if _, err := f.b.StartRekey(ctx, DomainUnseal, RekeyParams{}); err != nil {
		t.Fatal(err)
	}
	f.quorum(DomainUnseal, OpRekey, "h1", "h2")
	if _, err := f.submit(DomainUnseal, OpRootToken, "h1"); err != nil {
		t.Fatal(err)
	}
	shares := f.roundShares(DomainUnseal, OpRootToken)

	// h2's share from before the rekey carries h2's index, so it is counted,
	// but it reconstructs a key that no longer wraps anything.
	if _, err := f.submitBuf(DomainUnseal, OpRootToken, "h2", stale); !errors.Is(err, ErrUnsealKeyMismatch) {
		t.Fatalf("quorum with a pre-rekey share = %v, want ErrUnsealKeyMismatch", err)
	}
	requireWiped(t, stale)
	requireDestroyed(t, shares)
	if n := f.openRounds(); n != 0 {
		t.Fatalf("%d rounds still open after a failed reconstruction", n)
	}
}

func TestQuorum_RedistributionDestroysTheDomainsOpenRounds(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)
	f.dist.setHolders(DomainRecovery, "r1", "r2", "r3")
	if _, err := f.b.GenerateRecoveryKeys(ctx, 2, FactorPolicyAnyEnrolled); err != nil {
		t.Fatal(err)
	}
	clock := f.withManualClock(time.Hour)
	if _, err := f.submit(DomainUnseal, OpRootToken, "h1"); err != nil {
		t.Fatal(err)
	}
	replaced := f.roundShares(DomainUnseal, OpRootToken)
	if _, err := f.b.StartRekey(ctx, DomainRecovery, RekeyParams{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.submit(DomainRecovery, OpRekey, "r1"); err != nil {
		t.Fatal(err)
	}
	otherDomain := f.roundShares(DomainRecovery, OpRekey)

	if _, err := f.b.StartRekey(ctx, DomainUnseal, RekeyParams{}); err != nil {
		t.Fatal(err)
	}
	if o := f.quorum(DomainUnseal, OpRekey, "h2", "h3"); !o.Complete {
		t.Fatalf("rekey incomplete: %+v", o)
	}

	// The root-token round was opened on the share set the rekey replaced;
	// its shares are destroyed now, not when its TTL runs out.
	requireDestroyed(t, replaced)
	if st := f.b.openRound(DomainUnseal, OpRootToken); st != nil {
		t.Fatalf("a round on the replaced share set is still open: %+v", st)
	}
	requireReadable(t, otherDomain)
	if n := f.openRounds(); n != 1 {
		t.Fatalf("%d rounds open, want only the recovery-domain rekey", n)
	}
	if clock.Waiters() != 1 {
		t.Fatalf("%d expiry timers pending, want only the recovery-domain rekey's", clock.Waiters())
	}
}

func TestDeleteRecoveryKeys_DestroysTheDomainsOpenRounds(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)
	f.dist.setHolders(DomainRecovery, "r1", "r2", "r3")
	if _, err := f.b.GenerateRecoveryKeys(ctx, 2, FactorPolicyAnyEnrolled); err != nil {
		t.Fatal(err)
	}
	f.withManualClock(time.Hour)
	if _, err := f.b.StartRekey(ctx, DomainRecovery, RekeyParams{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.submit(DomainRecovery, OpRekey, "r1"); err != nil {
		t.Fatal(err)
	}
	deleted := f.roundShares(DomainRecovery, OpRekey)
	if _, err := f.submit(DomainUnseal, OpRootToken, "h1"); err != nil {
		t.Fatal(err)
	}
	otherDomain := f.roundShares(DomainUnseal, OpRootToken)

	if err := f.b.DeleteRecoveryKeys(ctx); err != nil {
		t.Fatal(err)
	}

	requireDestroyed(t, deleted)
	requireReadable(t, otherDomain)
	if n := f.openRounds(); n != 1 {
		t.Fatalf("%d rounds open, want only the unseal-domain root-token round", n)
	}
}

func TestQuorum_UnsealDestroysTheRoundsItEnds(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)
	f.dist.setHolders(DomainRecovery, "r1", "r2", "r3")
	if _, err := f.b.GenerateRecoveryKeys(ctx, 2, FactorPolicyAnyEnrolled); err != nil {
		t.Fatal(err)
	}
	f.sealWith("h1", "h2")
	f.withManualClock(time.Hour)
	if _, err := f.submit(DomainRecovery, OpRecover, "r1"); err != nil {
		t.Fatal(err)
	}
	recovery := f.roundShares(DomainRecovery, OpRecover)
	// A sealed barrier runs only the unseal and recovery rounds: a root token
	// minted while sealed would grant past the seal, so its round never opens.
	if _, err := f.submit(DomainUnseal, OpRootToken, "h3"); !errors.Is(err, ErrSealed) {
		t.Fatalf("root-token share while sealed: err = %v, want ErrSealed", err)
	}

	if o := f.quorum(DomainUnseal, OpUnseal, "h1", "h2"); !o.Complete || f.b.IsSealed() {
		t.Fatalf("unseal quorum incomplete: %+v", o)
	}

	// An unsealed barrier refuses recovery, so the recovery round can never
	// complete: its shares go now, and no round opened while sealed survives.
	requireDestroyed(t, recovery)
	if n := f.openRounds(); n != 0 {
		t.Fatalf("%d rounds open after unseal, want none", n)
	}
	if o := f.quorum(DomainUnseal, OpRootToken, "h1", "h2"); !o.Complete || o.RootToken == nil {
		t.Fatalf("a root-token round on the unsealed barrier did not complete: %+v", o)
	}
}

func TestQuorum_RefusedSubmissionLeavesNoPlainCopy(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")
	if _, err := f.submit(DomainUnseal, OpUnseal, "h1"); err != nil {
		t.Fatal(err)
	}
	_, digest, err := f.b.HolderRoster(context.Background(), DomainUnseal)
	if err != nil {
		t.Fatal(err)
	}
	// shareOf names the holder whose distributed share the case sends.
	cases := []struct {
		name    string
		shareOf string
		sub     *HolderSubmission
	}{
		{"unauthenticated", "h2", &HolderSubmission{Domain: DomainUnseal, Op: OpUnseal, Holder: "h2",
			Proof: HolderProof{ChallengeID: "never-issued"}}},
		{"holder not in the roster", "h2", &HolderSubmission{Domain: DomainUnseal, Op: OpUnseal, Holder: "mallory",
			Proof: f.verifier.issue(DomainUnseal, OpUnseal, "mallory", digest)}},
		{"holder already submitted", "h1", &HolderSubmission{Domain: DomainUnseal, Op: OpUnseal, Holder: "h1",
			Proof: f.verifier.issue(DomainUnseal, OpUnseal, "h1", digest)}},
		{"somebody else's share", "h2", &HolderSubmission{Domain: DomainUnseal, Op: OpUnseal, Holder: "h3",
			Proof: f.verifier.issue(DomainUnseal, OpUnseal, "h3", digest)}},
		{"operation the barrier's state refuses", "h2", &HolderSubmission{Domain: DomainUnseal, Op: OpSeal, Holder: "h2",
			Proof: f.verifier.issue(DomainUnseal, OpSeal, "h2", digest)}},
		{"operation invalid in the domain", "h2", &HolderSubmission{Domain: DomainRecovery, Op: OpUnseal, Holder: "h2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			share := []byte(f.dist.share(DomainUnseal, tc.shareOf))
			tc.sub.Share = share
			if _, err := f.b.SubmitHolderShare(context.Background(), tc.sub); err == nil {
				t.Fatal("the submission was accepted")
			}
			requireWiped(t, share)
			if st := f.b.openRound(DomainUnseal, OpUnseal); st == nil || st.Submitted != 1 {
				t.Fatalf("a refused submission changed the open round: %+v", st)
			}
		})
	}
}
