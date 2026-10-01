// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package seal

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestQuorum_UnsealNeedsThresholdDistinctHolders is the happy path: the
// barrier stays sealed until M distinct holders submit, then unseals, and a
// value written before the seal reads back.
func TestQuorum_UnsealNeedsThresholdDistinctHolders(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 3, 5)
	if err := f.b.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	f.sealWith("h1", "h2", "h3")

	for i, h := range []string{"h5", "h2"} {
		o, err := f.submit(DomainUnseal, OpUnseal, h)
		if err != nil {
			t.Fatal(err)
		}
		if o.Complete || o.Submitted != i+1 || o.Required != 3 {
			t.Fatalf("after %d holders: %+v", i+1, o)
		}
		if !f.b.IsSealed() {
			t.Fatal("unsealed below the threshold")
		}
	}
	o, err := f.submit(DomainUnseal, OpUnseal, "h4")
	if err != nil {
		t.Fatal(err)
	}
	if !o.Complete || f.b.IsSealed() {
		t.Fatalf("threshold reached but barrier sealed=%v, outcome %+v", f.b.IsSealed(), o)
	}
	got, err := f.b.Get(ctx, "k")
	if err != nil || string(got) != "v" {
		t.Fatalf("Get after unseal = %q, %v", got, err)
	}
}

// TestQuorum_AHolderCountsOnce is the dual-control property the old
// value-digest dedup could not give: one holder submitting twice — even a
// DIFFERENT valid share the second time — never makes two of M.
func TestQuorum_AHolderCountsOnce(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")

	if _, err := f.submit(DomainUnseal, OpUnseal, "h1"); err != nil {
		t.Fatal(err)
	}
	// The same holder again, with their own share.
	_, err := f.submit(DomainUnseal, OpUnseal, "h1")
	var again *HolderAlreadySubmittedError
	if !errors.As(err, &again) || !errors.Is(err, ErrShamirDuplicateShare) {
		t.Fatalf("second submission by h1 = %v, want HolderAlreadySubmittedError", err)
	}
	// The same holder again, carrying somebody else's valid share.
	if _, err := f.submitShare(DomainUnseal, OpUnseal, "h1", f.dist.share(DomainUnseal, "h2")); !errors.As(err, &again) {
		t.Fatalf("h1 with h2's share = %v, want HolderAlreadySubmittedError", err)
	}
	if !f.b.IsSealed() {
		t.Fatal("one holder alone unsealed the barrier")
	}
}

// TestQuorum_AHolderMaySubmitOnlyTheirOwnShare pins the index binding: a
// holder who has obtained another holder's share cannot count it.
func TestQuorum_AHolderMaySubmitOnlyTheirOwnShare(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")
	_, err := f.submitShare(DomainUnseal, OpUnseal, "h1", f.dist.share(DomainUnseal, "h3"))
	var mismatch *ShareIndexMismatchError
	if !errors.As(err, &mismatch) || mismatch.Want != 1 {
		t.Fatalf("h1 submitting h3's share = %v, want ShareIndexMismatchError", err)
	}
	// The refused share did not count: h1 can still submit their own.
	if _, err := f.submit(DomainUnseal, OpUnseal, "h1"); err != nil {
		t.Fatalf("h1's own share after a refusal: %v", err)
	}
}

// TestQuorum_RefusesUnauthenticatedAndUnknownSubmitters covers the admission
// checks that precede counting.
func TestQuorum_RefusesUnauthenticatedAndUnknownSubmitters(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")
	_, digest, err := f.b.HolderRoster(ctx, DomainUnseal)
	if err != nil {
		t.Fatal(err)
	}

	var missing *HolderNotInRosterError
	if _, err := f.submitShare(DomainUnseal, OpUnseal, "mallory", f.dist.share(DomainUnseal, "h1")); !errors.As(err, &missing) {
		t.Fatalf("unknown holder = %v", err)
	}

	var auth *HolderAuthenticationError
	cases := map[string]HolderProof{
		"no proof":            {},
		"forged response":     {ChallengeID: f.verifier.issue(DomainUnseal, OpUnseal, "h1", digest).ChallengeID, Response: []byte("x")},
		"other holder's":      f.verifier.issue(DomainUnseal, OpUnseal, "h2", digest),
		"other operation's":   f.verifier.issue(DomainUnseal, OpRootToken, "h1", digest),
		"stale roster digest": f.verifier.issue(DomainUnseal, OpUnseal, "h1", []byte("old")),
	}
	for name, proof := range cases {
		_, err := f.b.SubmitHolderShare(ctx, &HolderSubmission{
			Domain: DomainUnseal, Op: OpUnseal, Holder: "h1", Proof: proof,
			Share: []byte(f.dist.share(DomainUnseal, "h1")),
		})
		if !errors.As(err, &auth) {
			t.Fatalf("%s: %v, want HolderAuthenticationError", name, err)
		}
	}

	// A proof is consumed by its first use, success or not.
	proof := f.verifier.issue(DomainUnseal, OpUnseal, "h1", digest)
	sub := func() error {
		_, err := f.b.SubmitHolderShare(ctx, &HolderSubmission{Domain: DomainUnseal, Op: OpUnseal, Holder: "h1", Proof: proof, Share: []byte(f.dist.share(DomainUnseal, "h1"))})
		return err
	}
	if err := sub(); err != nil {
		t.Fatalf("first use: %v", err)
	}
	f.b.discardRound(DomainUnseal, OpUnseal)
	if err := sub(); !errors.As(err, &auth) {
		t.Fatalf("replayed proof = %v, want HolderAuthenticationError", err)
	}
}

// TestQuorum_FailsClosedWithoutAVerifier pins that no share counts when no
// holder verifier is wired.
func TestQuorum_FailsClosedWithoutAVerifier(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")
	f.b.SetHolderVerifier(nil)
	if _, err := f.submit(DomainUnseal, OpUnseal, "h1"); !errors.Is(err, ErrHolderVerifierNotConfigured) {
		t.Fatalf("err = %v, want ErrHolderVerifierNotConfigured", err)
	}
}

// TestQuorum_TamperedRosterFailsTheUnwrap is the roster binding end to end:
// editing the stored roster (here, swapping in an attacker's certificate)
// makes the completing quorum's key fail to open the blob.
func TestQuorum_TamperedRosterFailsTheUnwrap(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")

	path := f.b.wrappedKeyPath(DomainUnseal)
	blob, err := f.b.base.Get(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var w wrappedRootKey
	if err := json.Unmarshal(blob, &w); err != nil {
		t.Fatal(err)
	}
	roster, _, err := decodeRoster(w.Roster)
	if err != nil {
		t.Fatal(err)
	}
	roster.Holders[0].CertThumbprint = testThumb("attacker")
	w.Roster, _, err = encodeRoster(roster)
	if err != nil {
		t.Fatal(err)
	}
	tampered, _ := json.Marshal(&w)
	if err := f.b.base.Put(ctx, path, tampered); err != nil {
		t.Fatal(err)
	}

	f.quorum(DomainUnseal, OpUnseal, "h1")
	if _, err := f.submit(DomainUnseal, OpUnseal, "h2"); !errors.Is(err, ErrUnsealKeyMismatch) {
		t.Fatalf("quorum over a tampered roster = %v, want ErrUnsealKeyMismatch", err)
	}
	if !f.b.IsSealed() {
		t.Fatal("a tampered roster unsealed the barrier")
	}
}

// TestQuorum_RoundExpiresAndWipes pins the TTL: shares older than the round
// lifetime are discarded and a new round starts.
func TestQuorum_RoundExpiresAndWipes(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")
	f.b.config.Shamir.QuorumTTL = time.Millisecond
	if _, err := f.submit(DomainUnseal, OpUnseal, "h1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if st := f.b.openRound(DomainUnseal, OpUnseal); st != nil {
		t.Fatalf("expired round still reported: %+v", st)
	}
	o, err := f.submit(DomainUnseal, OpUnseal, "h2")
	if err != nil {
		t.Fatal(err)
	}
	if o.Submitted != 1 || o.Complete {
		t.Fatalf("the expired round's share was counted: %+v", o)
	}
}

// TestQuorum_StateAndDomainChecks covers the operation guards.
func TestQuorum_StateAndDomainChecks(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	var invalid *QuorumOpInvalidError
	if _, err := f.submit(DomainUnseal, OpRecover, "h1"); !errors.As(err, &invalid) {
		t.Fatalf("recover in the unseal domain = %v", err)
	}
	if _, err := f.submit(DomainUnseal, OpUnseal, "h1"); !errors.Is(err, ErrAlreadyUnsealed) {
		t.Fatalf("unseal while unsealed = %v", err)
	}
	if _, err := f.submit(DomainUnseal, OpRekey, "h1"); !errors.Is(err, ErrQuorumRoundNotOpen) {
		t.Fatalf("rekey without a round = %v", err)
	}
	if _, err := f.b.SubmitHolderShare(context.Background(), nil); err == nil {
		t.Fatal("nil submission accepted")
	}
	f.sealWith("h1", "h2")
	if _, err := f.submit(DomainUnseal, OpSeal, "h1"); !errors.Is(err, ErrSealed) {
		t.Fatalf("seal while sealed = %v", err)
	}
	if _, err := f.b.StartRekey(context.Background(), DomainUnseal, RekeyParams{}); !errors.Is(err, ErrSealed) {
		t.Fatalf("rekey while sealed = %v", err)
	}
	if _, err := f.submit(DomainRecovery, OpRecover, "h1"); !errors.Is(err, ErrHolderRosterMissing) {
		t.Fatalf("recover with no recovery keys = %v", err)
	}
}

// TestQuorum_RootTokenNeedsAQuorum mints a token only at the threshold, and
// two tokens differ.
func TestQuorum_RootTokenNeedsAQuorum(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	o, err := f.submit(DomainUnseal, OpRootToken, "h1")
	if err != nil || o.RootToken != nil {
		t.Fatalf("token minted below threshold: %+v, %v", o, err)
	}
	o = f.quorum(DomainUnseal, OpRootToken, "h3")
	if !o.Complete || o.RootToken == nil || len(o.RootToken.Token) != 64 {
		t.Fatalf("no root token at the threshold: %+v", o)
	}
	o2 := f.quorum(DomainUnseal, OpRootToken, "h1", "h2")
	if o2.RootToken == nil || o2.RootToken.Token == o.RootToken.Token {
		t.Fatal("two root tokens were identical")
	}
	if f.b.IsSealed() {
		t.Fatal("a root-token round sealed the barrier")
	}
}

// TestQuorum_RecoveryDomainIsIndependent: recovery holders unseal through the
// recovery key; unseal holders cannot, and a rekey of the unseal domain does
// not disturb recovery.
func TestQuorum_RecoveryDomainIsIndependent(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)
	f.dist.setHolders(DomainRecovery, "r1", "r2", "r3")
	res, err := f.b.GenerateRecoveryKeys(ctx, 2, FactorPolicyAnyEnrolled)
	if err != nil {
		t.Fatal(err)
	}
	if res.Threshold != 2 || res.TotalShares != 3 || res.ShareCount != 3 {
		t.Fatalf("recovery result = %+v", res)
	}
	if _, err := f.b.GenerateRecoveryKeys(ctx, 2, ""); !errors.Is(err, ErrRecoveryKeysExist) {
		t.Fatalf("second generation = %v, want ErrRecoveryKeysExist", err)
	}
	if ok, _ := f.b.HasRecoveryKeys(ctx); !ok {
		t.Fatal("HasRecoveryKeys = false")
	}
	roster, _, err := f.b.HolderRoster(ctx, DomainRecovery)
	if err != nil || roster.Policy != FactorPolicyAnyEnrolled {
		t.Fatalf("recovery roster policy = %v, %v", roster, err)
	}

	// Rekey the unseal domain; the recovery shares must still work.
	if _, err := f.b.StartRekey(ctx, DomainUnseal, RekeyParams{}); err != nil {
		t.Fatal(err)
	}
	f.quorum(DomainUnseal, OpRekey, "h1", "h2")
	f.sealWith("h2", "h3")

	var missing *HolderNotInRosterError
	if _, err := f.submit(DomainRecovery, OpRecover, "h1"); !errors.As(err, &missing) {
		t.Fatalf("unseal holder in the recovery domain = %v", err)
	}
	if o := f.quorum(DomainRecovery, OpRecover, "r2", "r3"); !o.Complete {
		t.Fatalf("recovery incomplete: %+v", o)
	}
	if f.b.IsSealed() || f.b.ActiveStrategy() != StrategyID("recovery") {
		t.Fatalf("recovery did not unseal: sealed=%v active=%s", f.b.IsSealed(), f.b.ActiveStrategy())
	}

	if err := f.b.DeleteRecoveryKeys(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, _ := f.b.HasRecoveryKeys(ctx); ok {
		t.Fatal("recovery keys survived deletion")
	}
	if err := f.b.DeleteRecoveryKeys(ctx); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestGenerateRecoveryKeys_Guards(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)
	if _, err := f.b.GenerateRecoveryKeys(ctx, 1, ""); !errors.Is(err, ErrShamirThresholdInvalid) {
		t.Fatalf("threshold 1 = %v", err)
	}
	if _, err := f.b.GenerateRecoveryKeys(ctx, 2, ""); !errors.Is(err, errTestTooFewHolders) {
		t.Fatalf("no recovery holders = %v", err)
	}
	f.sealWith("h1", "h2")
	if _, err := f.b.GenerateRecoveryKeys(ctx, 2, ""); !errors.Is(err, ErrSealed) {
		t.Fatalf("sealed = %v", err)
	}
	if err := f.b.DeleteRecoveryKeys(ctx); !errors.Is(err, ErrSealed) {
		t.Fatalf("delete while sealed = %v", err)
	}
}

// TestQuorum_RevocationIsAQuorumGatedRekey is the revocation contract: the
// revoked holder cannot contribute to the round that revokes them, the round
// needs M of the remaining holders, the new share set goes to N-1 holders,
// and afterwards the revoked holder and every old share are dead.
func TestQuorum_RevocationIsAQuorumGatedRekey(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 3, 5)
	oldShareH2 := f.dist.share(DomainUnseal, "h2")

	info, err := f.b.StartRekey(ctx, DomainUnseal, RekeyParams{Remove: []string{"h5"}})
	if err != nil {
		t.Fatal(err)
	}
	if info.Required != 3 || info.NewThreshold != 3 || len(info.Remove) != 1 {
		t.Fatalf("round = %+v", info)
	}
	if _, err := f.b.StartRekey(ctx, DomainUnseal, RekeyParams{}); !errors.Is(err, ErrQuorumRoundInProgress) {
		t.Fatalf("second StartRekey = %v", err)
	}
	var revoked *HolderRevokedError
	if _, err := f.submit(DomainUnseal, OpRekey, "h5"); !errors.As(err, &revoked) {
		t.Fatalf("revoked holder submitted to their own revocation: %v", err)
	}
	f.quorum(DomainUnseal, OpRekey, "h1", "h2")
	if st := f.b.openRound(DomainUnseal, OpRekey); st == nil || st.Submitted != 2 {
		t.Fatalf("status = %+v", st)
	}
	o := f.quorum(DomainUnseal, OpRekey, "h3")
	if !o.Complete || o.Rekey == nil || o.Rekey.TotalShares != 4 || o.Rekey.Threshold != 3 {
		t.Fatalf("rekey outcome = %+v", o)
	}

	roster, _, err := f.b.HolderRoster(ctx, DomainUnseal)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := roster.Holder("h5"); ok || roster.Total != 4 {
		t.Fatalf("revoked holder still in roster (total %d)", roster.Total)
	}
	if st := f.b.Status(); st.ShamirTotal != 4 {
		t.Fatalf("status total = %d, want 4", st.ShamirTotal)
	}

	f.sealWith("h1", "h2", "h3")
	var missing *HolderNotInRosterError
	if _, err := f.submit(DomainUnseal, OpUnseal, "h5"); !errors.As(err, &missing) {
		t.Fatalf("revoked holder after rekey = %v", err)
	}
	// An old share of a still-enrolled holder is dead too.
	if _, err := f.submitShare(DomainUnseal, OpUnseal, "h2", oldShareH2); err == nil {
		f.quorum(DomainUnseal, OpUnseal, "h1")
		if _, err := f.submit(DomainUnseal, OpUnseal, "h3"); !errors.Is(err, ErrUnsealKeyMismatch) {
			t.Fatalf("quorum including a pre-rekey share = %v, want ErrUnsealKeyMismatch", err)
		}
	}
	if o := f.quorum(DomainUnseal, OpUnseal, "h1", "h3", "h4"); !o.Complete {
		t.Fatalf("new share set did not unseal: %+v", o)
	}
}

// TestStartRekey_RefusesARevocationThatBreaksTheQuorum is the "M > N-1"
// refusal, both for reaching the current quorum and for the new share set.
func TestStartRekey_RefusesARevocationThatBreaksTheQuorum(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 3, 3)
	var breaks *RevocationBreaksQuorumError
	if _, err := f.b.StartRekey(ctx, DomainUnseal, RekeyParams{Remove: []string{"h1"}}); !errors.As(err, &breaks) {
		t.Fatalf("3-of-3 revoking one = %v, want RevocationBreaksQuorumError", err)
	}
	var missing *HolderNotInRosterError
	if _, err := f.b.StartRekey(ctx, DomainUnseal, RekeyParams{Remove: []string{"nobody"}}); !errors.As(err, &missing) {
		t.Fatalf("revoking a non-holder = %v", err)
	}
	var invalid *QuorumOpInvalidError
	if _, err := f.b.StartRekey(ctx, QuorumDomain("x"), RekeyParams{}); !errors.As(err, &invalid) {
		t.Fatalf("unknown domain = %v", err)
	}
	var bad *InvalidRosterError
	if _, err := f.b.StartRekey(ctx, DomainUnseal, RekeyParams{Policy: "whenever"}); !errors.As(err, &bad) {
		t.Fatalf("unknown policy = %v", err)
	}

	g := newHolderFixture(t, 2, 3)
	if _, err := g.b.StartRekey(ctx, DomainUnseal, RekeyParams{Threshold: 3, Remove: []string{"h1"}}); err == nil {
		t.Fatal("a rekey to 3-of-2 was accepted")
	}
}

// TestQuorum_RekeyChangesThePolicy pins that the factor policy is bound per
// distribution and changes only through a rekey.
func TestQuorum_RekeyChangesThePolicy(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)
	roster, _, _ := f.b.HolderRoster(ctx, DomainUnseal)
	if roster.Policy != FactorPolicyDesignated {
		t.Fatalf("default policy = %s", roster.Policy)
	}
	if _, err := f.b.StartRekey(ctx, DomainUnseal, RekeyParams{Policy: FactorPolicyAnyEnrolled}); err != nil {
		t.Fatal(err)
	}
	f.quorum(DomainUnseal, OpRekey, "h1", "h3")
	roster, _, _ = f.b.HolderRoster(ctx, DomainUnseal)
	if roster.Policy != FactorPolicyAnyEnrolled {
		t.Fatalf("policy after rekey = %s", roster.Policy)
	}
}

// TestQuorum_StaleRoundIsDiscardedAfterRedistribution pins that shares
// gathered against a roster that has since been replaced do not count.
func TestQuorum_StaleRoundIsDiscardedAfterRedistribution(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)
	if _, err := f.submit(DomainUnseal, OpRootToken, "h1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.StartRekey(ctx, DomainUnseal, RekeyParams{}); err != nil {
		t.Fatal(err)
	}
	f.quorum(DomainUnseal, OpRekey, "h2", "h3")
	o, err := f.submit(DomainUnseal, OpRootToken, "h2")
	if err != nil {
		t.Fatal(err)
	}
	if o.Complete || o.Submitted != 1 {
		t.Fatalf("a pre-rekey contribution counted: %+v", o)
	}
}

// TestQuorum_DistributionFailureKeepsTheOldShareSet pins the ordering:
// distribute first, persist last.
func TestQuorum_DistributionFailureKeepsTheOldShareSet(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)
	if _, err := f.b.StartRekey(ctx, DomainUnseal, RekeyParams{}); err != nil {
		t.Fatal(err)
	}
	f.quorum(DomainUnseal, OpRekey, "h1")
	boom := errors.New("distribution failed")
	f.dist.mu.Lock()
	f.dist.failNext = boom
	f.dist.mu.Unlock()
	// The capture distributor records shares only on success, so the old
	// shares stay the ones it holds.
	if _, err := f.submit(DomainUnseal, OpRekey, "h2"); !errors.Is(err, boom) {
		t.Fatalf("rekey with a failing distributor = %v", err)
	}
	f.sealWith("h1", "h2")
	if o := f.quorum(DomainUnseal, OpUnseal, "h2", "h3"); !o.Complete {
		t.Fatal("the old share set stopped working after a failed rekey")
	}
}

// TestInitializeShamir_Guards covers initialization refusals.
func TestInitializeShamir_Guards(t *testing.T) {
	ctx := context.Background()
	shamirStrategy, _ := NewShamirStrategy(2, 3)
	mk := func(cfg *ShamirConfig, strategies ...SealingStrategy) *StorageBarrier {
		b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{RootKeyPath: "rk", Shamir: cfg}, strategies...)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	if _, err := mk(nil, shamirStrategy).InitializeShamir(ctx, ""); !errors.Is(err, ErrShamirNotConfigured) {
		t.Fatalf("no shamir config = %v", err)
	}
	if _, err := mk(&ShamirConfig{Threshold: 2, TotalShares: 3}, testSoftwareStrategy(t)).InitializeShamir(ctx, ""); !errors.Is(err, ErrShamirNotConfigured) {
		t.Fatalf("no shamir strategy = %v", err)
	}
	if _, err := mk(&ShamirConfig{Threshold: 2, TotalShares: 3}, shamirStrategy).InitializeShamir(ctx, ""); !errors.Is(err, ErrCeremonyDistributorNotConfigured) {
		t.Fatalf("no distributor = %v", err)
	}
	b := mk(&ShamirConfig{Threshold: 4, TotalShares: 3}, shamirStrategy)
	b.SetCeremonyDistributor(newCaptureDistributor("h1", "h2", "h3"))
	if _, err := b.InitializeShamir(ctx, ""); !errors.Is(err, ErrShamirThresholdInvalid) {
		t.Fatalf("threshold over total = %v", err)
	}
	b = mk(&ShamirConfig{Threshold: 2, TotalShares: 3}, shamirStrategy)
	b.SetCeremonyDistributor(newCaptureDistributor("h1", "h2"))
	var mismatch *HolderCountMismatchError
	if _, err := b.InitializeShamir(ctx, ""); !errors.As(err, &mismatch) {
		t.Fatalf("two holders for three shares = %v", err)
	}
	if exists, _ := b.base.Exists(ctx, "rk"); exists {
		t.Fatal("a refused initialization left a root key behind")
	}
	b.SetCeremonyDistributor(newCaptureDistributor("h1", "h2", "h3"))
	var bad *InvalidRosterError
	if _, err := b.redistribute(ctx, DomainUnseal, nil, 2, "sometimes", nil, 0); !errors.As(err, &bad) {
		t.Fatalf("unknown policy = %v", err)
	}
	if _, err := b.InitializeShamir(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.InitializeShamir(ctx, ""); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("second init = %v", err)
	}
}

// TestQuorum_ConcurrentSubmissionsCountEachHolderOnce races every holder,
// twice each, into one round: exactly one completion, and the barrier
// unseals.
func TestQuorum_ConcurrentSubmissionsCountEachHolderOnce(t *testing.T) {
	f := newHolderFixture(t, 4, 7)
	f.sealWith("h1", "h2", "h3", "h4")

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		completed int
	)
	for _, h := range holderNames(7) {
		for range 2 {
			wg.Add(1)
			go func(h string) {
				defer wg.Done()
				o, err := f.submit(DomainUnseal, OpUnseal, h)
				if err == nil && o.Complete {
					mu.Lock()
					completed++
					mu.Unlock()
				}
			}(h)
		}
	}
	wg.Wait()
	if completed != 1 {
		t.Fatalf("%d submissions completed the round, want exactly 1", completed)
	}
	if f.b.IsSealed() {
		t.Fatal("barrier still sealed")
	}
}

// discardRound drops domain's open round for op, wiping its shares, so a test
// can resubmit without tripping the once-per-holder rule.
func (b *StorageBarrier) discardRound(domain QuorumDomain, op QuorumOp) {
	b.quorumMu.Lock()
	defer b.quorumMu.Unlock()
	key := quorumRoundKey{domain: domain, op: op}
	if r, ok := b.rounds[key]; ok {
		r.destroy()
		delete(b.rounds, key)
	}
}

// openRound reports domain's live round for op, or nil when none is open or
// it has expired.
func (b *StorageBarrier) openRound(domain QuorumDomain, op QuorumOp) *QuorumRoundInfo {
	b.quorumMu.Lock()
	defer b.quorumMu.Unlock()
	r, ok := b.rounds[quorumRoundKey{domain: domain, op: op}]
	if !ok || !time.Now().Before(r.expiresAt) {
		return nil
	}
	return r.info()
}
