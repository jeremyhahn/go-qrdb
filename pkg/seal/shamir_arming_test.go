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
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// armedNode is a second serving node's keyless Shamir barrier, armed from the
// first node's init params, with its own holder verifier.
type armedNode struct {
	t        *testing.T
	base     StorageBackend
	b        *StorageBarrier
	verifier *testVerifier
	digest   []byte
}

func newShamirNode(t *testing.T, base StorageBackend, threshold, total int) *StorageBarrier {
	t.Helper()
	strategy, err := NewShamirStrategy(threshold, total)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewStorageBarrier(testLogger(), base, BarrierConfig{
		RootKeyPath: "root-key",
		Shamir:      &ShamirConfig{Threshold: threshold, TotalShares: total},
	}, strategy)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newArmedNode(t *testing.T, first *holderFixture) *armedNode {
	t.Helper()
	base := NewMemoryBackend()
	n := &armedNode{
		t:        t,
		base:     base,
		b:        newShamirNode(t, base, first.params.Threshold, first.params.TotalShares),
		verifier: newTestVerifier(),
	}
	sum := sha256.Sum256(first.params.Roster)
	n.digest = sum[:]
	n.b.SetHolderVerifier(n.verifier)
	if err := n.b.ArmShamir(context.Background(), first.params.Roster, first.params.UnsealKeyCheck); err != nil {
		t.Fatalf("ArmShamir: %v", err)
	}
	return n
}

func (n *armedNode) submit(holder, share string) (*QuorumOutcome, error) {
	n.t.Helper()
	return n.b.SubmitHolderShare(context.Background(), &HolderSubmission{
		Domain: DomainUnseal,
		Op:     OpUnseal,
		Holder: holder,
		Proof:  n.verifier.issue(DomainUnseal, OpUnseal, holder, n.digest),
		Share:  []byte(share),
	})
}

func (n *armedNode) keyed() bool {
	n.t.Helper()
	ok, err := n.base.Exists(context.Background(), "root-key")
	if err != nil {
		n.t.Fatal(err)
	}
	return ok
}

func rootKeyOf(t *testing.T, b *StorageBarrier) []byte {
	t.Helper()
	buf, err := b.masterKeyBuffer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer buf.Destroy()
	key, err := secure.CopyOut(buf)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestUnsealKeyCheck_IsAFixedLengthFunctionOfTheKey(t *testing.T) {
	a := bytes.Repeat([]byte{1}, unsealKeySize)
	b := bytes.Repeat([]byte{2}, unsealKeySize)
	ca, err := UnsealKeyCheck(a)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := UnsealKeyCheck(a)
	cb, _ := UnsealKeyCheck(b)
	if len(ca) != UnsealKeyCheckSize || !bytes.Equal(ca, again) || bytes.Equal(ca, cb) || bytes.Equal(ca, a) {
		t.Fatalf("check values: a=%x again=%x b=%x", ca, again, cb)
	}
	var invalid *InvalidUnsealKeyError
	if _, err := UnsealKeyCheck(a[:16]); !errors.As(err, &invalid) || invalid.Error() == "" {
		t.Fatalf("short key = %v, want InvalidUnsealKeyError", err)
	}
}

func TestInitializeShamir_ReturnsTheCheckValueAndTheRosterItSplitTo(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	if len(f.params.UnsealKeyCheck) != UnsealKeyCheckSize {
		t.Fatalf("check value is %d bytes", len(f.params.UnsealKeyCheck))
	}
	_, digest, err := f.b.HolderRoster(context.Background(), DomainUnseal)
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(f.params.Roster); !bytes.Equal(sum[:], digest) {
		t.Fatal("the returned roster is not the one the key is wrapped with")
	}
	// The check value verifies the key the holders' shares reconstruct: a
	// node armed with it accepts exactly those shares.
	n := newArmedNode(t, f)
	n.submit("h1", f.dist.share(DomainUnseal, "h1"))
	if _, err := n.submit("h2", f.dist.share(DomainUnseal, "h2")); err != nil {
		t.Fatalf("the first node's own shares fail its check value: %v", err)
	}
}

func TestArmedNode_AQuorumFromTheWrongShareSetIsRefusedAndKeepsNothing(t *testing.T) {
	first := newHolderFixture(t, 2, 3)
	other := newHolderFixture(t, 2, 3)
	n := newArmedNode(t, first)

	if _, err := n.submit("h1", other.dist.share(DomainUnseal, "h1")); err != nil {
		t.Fatal(err)
	}
	_, err := n.submit("h2", other.dist.share(DomainUnseal, "h2"))
	var mismatch *UnsealKeyCheckMismatchError
	if !errors.As(err, &mismatch) || !errors.Is(err, ErrUnsealKeyMismatch) {
		t.Fatalf("wrong share set = %v, want UnsealKeyCheckMismatchError", err)
	}
	if !n.b.IsSealed() || n.keyed() || !n.b.ShamirArmed() {
		t.Fatalf("after a refused quorum: sealed=%v keyed=%v armed=%v", n.b.IsSealed(), n.keyed(), n.b.ShamirArmed())
	}
	if len(n.b.rounds) != 0 {
		t.Fatalf("the refused quorum left %d open rounds", len(n.b.rounds))
	}

	// The node is still armed: the right holders key it afterwards.
	n.submit("h1", first.dist.share(DomainUnseal, "h1"))
	if o, err := n.submit("h3", first.dist.share(DomainUnseal, "h3")); err != nil || !o.Complete {
		t.Fatalf("right quorum after a refused one = %+v, %v", o, err)
	}
}

func TestArmedNode_TheRightQuorumGeneratesItsOwnKeyAndUnseals(t *testing.T) {
	first := newHolderFixture(t, 2, 3)
	n := newArmedNode(t, first)
	if !n.b.IsSealed() || n.keyed() {
		t.Fatal("an armed node holds a key before its quorum")
	}

	n.submit("h1", first.dist.share(DomainUnseal, "h1"))
	o, err := n.submit("h3", first.dist.share(DomainUnseal, "h3"))
	if err != nil || !o.Complete {
		t.Fatalf("quorum = %+v, %v", o, err)
	}
	if n.b.IsSealed() || !n.keyed() || n.b.ShamirArmed() {
		t.Fatalf("after the quorum: sealed=%v keyed=%v armed=%v", n.b.IsSealed(), n.keyed(), n.b.ShamirArmed())
	}
	if bytes.Equal(rootKeyOf(t, n.b), rootKeyOf(t, first.b)) {
		t.Fatal("the armed node took the first node's root key")
	}
	_, digest, err := n.b.HolderRoster(context.Background(), DomainUnseal)
	if err != nil || !bytes.Equal(digest, n.digest) {
		t.Fatalf("the node's key is wrapped under roster %x (%v), want %x", digest, err, n.digest)
	}

	// After a restart the node unseals from the same holders through its own
	// wrapped key, with no arming.
	restarted := newShamirNode(t, n.base, first.params.Threshold, first.params.TotalShares)
	restarted.SetHolderVerifier(n.verifier)
	n.b = restarted
	if restarted.ShamirArmed() {
		t.Fatal("a restarted node is armed without a statement")
	}
	n.submit("h2", first.dist.share(DomainUnseal, "h2"))
	if o, err := n.submit("h1", first.dist.share(DomainUnseal, "h1")); err != nil || !o.Complete || restarted.IsSealed() {
		t.Fatalf("unseal after restart = %+v, %v", o, err)
	}
}

func TestArmedNode_ASecondSubmitByTheSameHolderIsANoOp(t *testing.T) {
	first := newHolderFixture(t, 2, 3)
	n := newArmedNode(t, first)

	if o, err := n.submit("h1", first.dist.share(DomainUnseal, "h1")); err != nil || o.Submitted != 1 {
		t.Fatalf("first submit = %+v, %v", o, err)
	}
	var again *HolderAlreadySubmittedError
	if _, err := n.submit("h1", first.dist.share(DomainUnseal, "h1")); !errors.As(err, &again) {
		t.Fatalf("second submit = %v, want HolderAlreadySubmittedError", err)
	}
	if !n.b.IsSealed() || n.keyed() {
		t.Fatal("a repeated submission keyed the node")
	}
	o, err := n.submit("h2", first.dist.share(DomainUnseal, "h2"))
	if err != nil || !o.Complete || o.Submitted != 2 {
		t.Fatalf("quorum after a repeat = %+v, %v", o, err)
	}
	key := rootKeyOf(t, n.b)
	// A holder whose share arrives after the node unsealed changes nothing.
	if _, err := n.submit("h3", first.dist.share(DomainUnseal, "h3")); !errors.Is(err, ErrAlreadyUnsealed) {
		t.Fatalf("late submit = %v, want ErrAlreadyUnsealed", err)
	}
	if !bytes.Equal(rootKeyOf(t, n.b), key) {
		t.Fatal("a late submission replaced the node's key")
	}
}

func TestArmShamir_Refusals(t *testing.T) {
	first := newHolderFixture(t, 2, 3)
	ctx := context.Background()
	b := newShamirNode(t, NewMemoryBackend(), 2, 3)

	var badCheck *InvalidUnsealKeyCheckError
	if err := b.ArmShamir(ctx, first.params.Roster, []byte("short")); !errors.As(err, &badCheck) || badCheck.Error() == "" {
		t.Fatalf("short check = %v", err)
	}
	if err := b.ArmShamir(ctx, nil, first.params.UnsealKeyCheck); !errors.Is(err, ErrHolderRosterMissing) {
		t.Fatalf("no roster = %v", err)
	}
	roster, _, err := decodeRoster(first.params.Roster)
	if err != nil {
		t.Fatal(err)
	}
	roster.Domain = DomainRecovery
	recoveryRaw, _, err := encodeRoster(roster)
	if err != nil {
		t.Fatal(err)
	}
	var invalid *InvalidRosterError
	if err := b.ArmShamir(ctx, recoveryRaw, first.params.UnsealKeyCheck); !errors.As(err, &invalid) {
		t.Fatalf("recovery roster = %v, want InvalidRosterError", err)
	}
	if b.ShamirArmed() {
		t.Fatal("a refused arming armed the node")
	}

	// A node that already holds a key ignores the arming.
	if err := first.b.ArmShamir(ctx, first.params.Roster, first.params.UnsealKeyCheck); err != nil || first.b.ShamirArmed() {
		t.Fatalf("arming a keyed node = %v, armed=%v", err, first.b.ShamirArmed())
	}
	plain, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{RootKeyPath: "root-key"}, NewNoneStrategy())
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.ArmShamir(ctx, first.params.Roster, first.params.UnsealKeyCheck); !errors.Is(err, ErrShamirNotConfigured) {
		t.Fatalf("arming a non-Shamir barrier = %v", err)
	}
}

func TestBarrierRegistry_ArmTenantShamir(t *testing.T) {
	first := newHolderFixture(t, 2, 3)
	ctx := context.Background()
	reg, _, _ := newPooledRegistry(t)

	// A node with no barrier for the tenant registers a Shamir one and arms it.
	if err := reg.ArmTenantShamir(ctx, "alpha", first.params.Roster, first.params.UnsealKeyCheck); err != nil {
		t.Fatalf("arming an unregistered tenant: %v", err)
	}
	tb, err := reg.GetTenant("alpha")
	if err != nil || !tb.Barrier().ShamirArmed() || tb.Barrier().ShamirStrategy() == nil {
		t.Fatalf("alpha after arming: %v", err)
	}
	// Re-arming from the next poll is idempotent.
	if err := reg.ArmTenantShamir(ctx, "alpha", first.params.Roster, first.params.UnsealKeyCheck); err != nil {
		t.Fatalf("re-arming: %v", err)
	}

	// A keyless shell registered under another strategy is switched to Shamir.
	shell, err := reg.RegisterTenantPooledWithStrategy(ctx, "beta", NewNoneStrategy())
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.ArmTenantShamir(ctx, "beta", first.params.Roster, first.params.UnsealKeyCheck); err != nil {
		t.Fatalf("arming a keyless shell: %v", err)
	}
	if shell.Barrier().ShamirStrategy() == nil || !shell.Barrier().ShamirArmed() {
		t.Fatal("the shell was not switched to an armed Shamir barrier")
	}

	if err := reg.ArmTenantShamir(ctx, "gamma", []byte("{"), first.params.UnsealKeyCheck); err == nil {
		t.Fatal("arming from a malformed roster succeeded")
	}
	if _, err := reg.GetTenant("gamma"); err == nil {
		t.Fatal("a malformed roster registered a barrier")
	}
}

// A barrier keyed under another strategy is not switched to Shamir nor armed:
// only a key the holders' quorum produced gets its Shamir strategy back.
func TestBarrierRegistry_ArmTenantShamir_LeavesABarrierKeyedUnderAnotherStrategy(t *testing.T) {
	first := newHolderFixture(t, 2, 3)
	ctx := context.Background()
	reg, _, _ := newPooledRegistryUnsealed(t)
	tb, err := reg.RegisterTenantPooledWithStrategy(ctx, "delta", newPoolSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := tb.Barrier().Initialize(ctx, MustCredentials(ctx, []byte("pool-test-pass"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := reg.ArmTenantShamir(ctx, "delta", first.params.Roster, first.params.UnsealKeyCheck); err != nil {
		t.Fatalf("ArmTenantShamir: %v", err)
	}
	if tb.Barrier().ShamirStrategy() != nil || tb.Barrier().ShamirArmed() {
		t.Fatal("a barrier keyed under another strategy was switched to Shamir")
	}
	if tb.IsSealed() {
		t.Fatal("ArmTenantShamir sealed a keyed barrier")
	}
}

// A node keyed by its holders' first quorum restarts with a barrier rebuilt
// from the system's strategies, which carry no Shamir one. The statement
// watcher's next ArmTenantShamir restores Shamir from the persisted key before
// it counts the key as complete, so the holders' next quorum unseals it.
func TestBarrierRegistry_ArmTenantShamir_RestoresShamirOnARestartedKeyedBarrier(t *testing.T) {
	first := newHolderFixture(t, 2, 3)
	ctx := context.Background()
	system, opts, _ := newShareableComponents(t)
	if err := system.Initialize(ctx, MustCredentials(ctx, []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	sum := sha256.Sum256(first.params.Roster)
	verifier := newTestVerifier()
	submit := func(b *StorageBarrier, holder string) (*QuorumOutcome, error) {
		t.Helper()
		return b.SubmitHolderShare(ctx, &HolderSubmission{
			Domain: DomainUnseal,
			Op:     OpUnseal,
			Holder: holder,
			Proof:  verifier.issue(DomainUnseal, OpUnseal, holder, sum[:]),
			Share:  []byte(first.dist.share(DomainUnseal, holder)),
		})
	}

	reg1, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg1.ArmTenantShamir(ctx, "acme", first.params.Roster, first.params.UnsealKeyCheck); err != nil {
		t.Fatalf("ArmTenantShamir: %v", err)
	}
	armed, err := reg1.GetTenant("acme")
	if err != nil {
		t.Fatal(err)
	}
	armed.Barrier().SetHolderVerifier(verifier)
	if _, err := submit(armed.Barrier(), "h1"); err != nil {
		t.Fatal(err)
	}
	if o, err := submit(armed.Barrier(), "h2"); err != nil || !o.Complete || armed.IsSealed() {
		t.Fatalf("first quorum = %+v, %v", o, err)
	}

	// The restart: a fresh registry over the same system barrier and pool.
	reg2, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg2.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants: %v", err)
	}
	restored, err := reg2.GetTenant("acme")
	if err != nil {
		t.Fatalf("the keyed tenant was not restored: %v", err)
	}
	if restored.Barrier().ShamirStrategy() != nil {
		t.Fatal("the restore registered a Shamir strategy itself; the test no longer reaches the watcher's path")
	}
	if err := reg2.ArmTenantShamir(ctx, "acme", first.params.Roster, first.params.UnsealKeyCheck); err != nil {
		t.Fatalf("ArmTenantShamir after the restart: %v", err)
	}
	if restored.Barrier().ShamirArmed() {
		t.Fatal("a keyed barrier was armed again")
	}
	if st := restored.Barrier().Status(); st.ShamirThreshold != 2 || st.ShamirTotal != 3 {
		t.Fatalf("restored scheme = %d/%d, want 2/3", st.ShamirThreshold, st.ShamirTotal)
	}

	restored.Barrier().SetHolderVerifier(verifier)
	if _, err := submit(restored.Barrier(), "h2"); err != nil {
		t.Fatal(err)
	}
	if o, err := submit(restored.Barrier(), "h3"); err != nil || !o.Complete || restored.IsSealed() {
		t.Fatalf("the holders' quorum after the restart = %+v, %v", o, err)
	}
}

func TestArmedNode_HolderRosterIsTheArmedOne(t *testing.T) {
	first := newHolderFixture(t, 2, 3)
	n := newArmedNode(t, first)
	roster, digest, err := n.b.HolderRoster(context.Background(), DomainUnseal)
	if err != nil || roster.Threshold != 2 || !bytes.Equal(digest, n.digest) {
		t.Fatalf("armed HolderRoster = %+v, %x, %v", roster, digest, err)
	}
	if _, _, err := n.b.HolderRoster(context.Background(), DomainRecovery); !errors.Is(err, ErrHolderRosterMissing) {
		t.Fatalf("armed recovery roster = %v, want ErrHolderRosterMissing", err)
	}
}

// newPreparedNode is a first node with holders enrolled whose Shamir key is
// prepared but not completed.
func newPreparedNode(t *testing.T) (*holderFixture, *ShamirInitParams) {
	t.Helper()
	f := &holderFixture{t: t, b: newShamirNode(t, NewMemoryBackend(), 2, 3), dist: newCaptureDistributor(holderNames(3)...), verifier: newTestVerifier()}
	f.b.SetCeremonyDistributor(f.dist)
	f.b.SetHolderVerifier(f.verifier)
	params, err := f.b.PrepareShamir(context.Background(), "")
	if err != nil {
		t.Fatalf("PrepareShamir: %v", err)
	}
	f.params = params
	return f, params
}

func TestPrepareShamir_SplitsAKeyItsCheckValueVerifies(t *testing.T) {
	f, params := newPreparedNode(t)
	if len(params.UnsealKeyCheck) != UnsealKeyCheckSize || params.ShareCount != 3 || params.Threshold != 2 {
		t.Fatalf("params = %+v", params)
	}
	if !f.b.ShamirPrepared() || !f.b.IsSealed() {
		t.Fatal("a prepared node holds a root key or no prepared key")
	}
	if ok, _ := f.b.base.Exists(context.Background(), "root-key"); ok {
		t.Fatal("PrepareShamir wrote a root key")
	}
	// The split is staged: no holder's share is live until completion.
	if share := f.dist.share(DomainUnseal, "h1"); share != "" {
		t.Fatal("a prepared split went live before the statement was claimed")
	}
}

func TestCompleteShamir_KeysTheFirstNodeUnderThePreparedKey(t *testing.T) {
	f, params := newPreparedNode(t)
	ctx := context.Background()
	if err := f.b.CompleteShamir(ctx, params.Roster, params.UnsealKeyCheck); err != nil {
		t.Fatalf("CompleteShamir: %v", err)
	}
	if f.b.IsSealed() || f.b.ShamirPrepared() {
		t.Fatalf("after completion: sealed=%v prepared=%v", f.b.IsSealed(), f.b.ShamirPrepared())
	}
	_, digest, err := f.b.HolderRoster(ctx, DomainUnseal)
	if sum := sha256.Sum256(params.Roster); err != nil || !bytes.Equal(digest, sum[:]) {
		t.Fatalf("wrapped roster digest %x, %v", digest, err)
	}
	if err := f.b.CompleteShamir(ctx, params.Roster, params.UnsealKeyCheck); !errors.Is(err, ErrShamirNotPrepared) {
		t.Fatalf("a second completion = %v, want ErrShamirNotPrepared", err)
	}
	if _, err := f.b.PrepareShamir(ctx, ""); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("preparing a keyed node = %v", err)
	}
	// The live shares reconstruct the key the check value names: a node armed
	// from the prepared params accepts them and keys itself with its own key.
	n := newArmedNode(t, f)
	n.submit("h1", f.dist.share(DomainUnseal, "h1"))
	if o, err := n.submit("h2", f.dist.share(DomainUnseal, "h2")); err != nil || !o.Complete {
		t.Fatalf("armed from prepared params = %+v, %v", o, err)
	}
	key := rootKeyOf(t, f.b)
	if bytes.Equal(rootKeyOf(t, n.b), key) {
		t.Fatal("the armed node took the first node's root key")
	}
	// The holders unseal the node after a seal from the prepared distribution.
	if err := f.b.Seal(); err != nil {
		t.Fatal(err)
	}
	f.quorum(DomainUnseal, OpUnseal, "h1", "h3")
	if f.b.IsSealed() || !bytes.Equal(rootKeyOf(t, f.b), key) {
		t.Fatal("the holders' shares do not unseal the completed node")
	}
}

func TestCompleteShamir_RefusesAStatementForAnotherSplit(t *testing.T) {
	f, params := newPreparedNode(t)
	ctx := context.Background()
	otherCheck := bytes.Clone(params.UnsealKeyCheck)
	otherCheck[0] ^= 0xff
	otherRoster := append(bytes.Clone(params.Roster), ' ')
	for name, c := range map[string]struct {
		roster, check []byte
		field         string
	}{
		"check":       {params.Roster, otherCheck, "unseal key check"},
		"empty check": {params.Roster, nil, "unseal key check"},
		"roster":      {otherRoster, params.UnsealKeyCheck, "roster"},
	} {
		var mismatch *PreparedShamirMismatchError
		if err := f.b.CompleteShamir(ctx, c.roster, c.check); !errors.As(err, &mismatch) || mismatch.Field != c.field {
			t.Fatalf("%s: CompleteShamir = %v, want PreparedShamirMismatchError{%q}", name, err, c.field)
		}
		if mismatch.Error() == "" {
			t.Fatalf("%s: empty error text", name)
		}
		if !f.b.IsSealed() || !f.b.ShamirPrepared() {
			t.Fatalf("%s: after a refusal sealed=%v prepared=%v", name, f.b.IsSealed(), f.b.ShamirPrepared())
		}
	}
	if err := f.b.CompleteShamir(ctx, params.Roster, params.UnsealKeyCheck); err != nil || f.b.IsSealed() {
		t.Fatalf("the prepared statement after refusals = %v", err)
	}
}

func TestPrepareShamir_Refusals(t *testing.T) {
	ctx := context.Background()
	if err := newShamirNode(t, NewMemoryBackend(), 2, 3).CompleteShamir(ctx, nil, nil); !errors.Is(err, ErrShamirNotPrepared) {
		t.Fatalf("completing an unprepared node = %v", err)
	}
	if _, err := newShamirNode(t, NewMemoryBackend(), 2, 3).PrepareShamir(ctx, ""); !errors.Is(err, ErrCeremonyDistributorNotConfigured) {
		t.Fatalf("preparing without a distributor = %v", err)
	}
	plain, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{RootKeyPath: "root-key"}, NewNoneStrategy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.PrepareShamir(ctx, ""); !errors.Is(err, ErrShamirNotConfigured) {
		t.Fatalf("preparing a non-Shamir barrier = %v", err)
	}
	short := newShamirNode(t, NewMemoryBackend(), 2, 3)
	short.SetCeremonyDistributor(newCaptureDistributor("h1", "h2"))
	var mismatch *HolderCountMismatchError
	if _, err := short.PrepareShamir(ctx, ""); !errors.As(err, &mismatch) || short.ShamirPrepared() {
		t.Fatalf("preparing with too few holders = %v", err)
	}

	// Preparing again replaces the prepared key and its split.
	f, first := newPreparedNode(t)
	second, err := f.b.PrepareShamir(ctx, "")
	if err != nil || bytes.Equal(first.UnsealKeyCheck, second.UnsealKeyCheck) {
		t.Fatalf("re-prepare = %v", err)
	}
	if err := f.b.CompleteShamir(ctx, first.Roster, first.UnsealKeyCheck); err == nil {
		t.Fatal("completing with the replaced split's check value succeeded")
	}
	if err := f.b.CompleteShamir(ctx, second.Roster, second.UnsealKeyCheck); err != nil {
		t.Fatal(err)
	}
	if err := f.b.Seal(); err != nil {
		t.Fatal(err)
	}
	f.quorum(DomainUnseal, OpUnseal, "h1", "h2")
	if f.b.IsSealed() {
		t.Fatal("the second split's shares do not unseal the node")
	}
}
