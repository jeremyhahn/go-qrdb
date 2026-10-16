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

package ceremony

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// fiveHolders builds 2 Security Officers and 3 custodians using different
// envelope KEMs and, for two custodians, the assertion factor.
func fiveHolders(t *testing.T) []*testHolder {
	t.Helper()
	return []*testHolder{
		newHolder(t, "so-alice", seal.RoleSecurityOfficer),
		newP384Holder(t, "so-bob", seal.RoleSecurityOfficer),
		newHolder(t, "cust-carol", seal.RoleCustodian).withEncryptionKey(t, testEnvelopeKEM(keydelivery.KEMX25519)).withAuthenticator(t),
		newHolder(t, "cust-dave", seal.RoleCustodian).withEncryptionKey(t, testEnvelopeKEM(keydelivery.KEMMLKEM768X25519)).withAuthenticator(t),
		newHolder(t, "cust-erin", seal.RoleCustodian),
	}
}

// TestClaim_EnvelopeOnceAndOpensToAShare pins the claim handshake: the
// holder's certificate must be presented, the nonce must be signed by the
// holder's key, the envelope is delivered once, and it opens with the
// holder's own key.
func TestClaim_EnvelopeOnceAndOpensToAShare(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.register(seal.DomainUnseal, a, b)
	_, err := e.barrier.InitializeShamir(ctx, "")
	require.NoError(t, err)

	list, err := e.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	for _, s := range list {
		assert.True(t, s.SharePending, s.Username)
		assert.NotZero(t, s.ShareIndex, s.Username)
	}

	_, err = e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", b.thumb)
	assert.ErrorIs(t, err, ErrCertThumbprintMismatch, "the holder's own certificate must be presented")
	_, err = e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "nobody", a.thumb)
	assert.ErrorIs(t, err, ErrOfficerNotFound)
	_, err = e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "", a.thumb)
	assert.ErrorIs(t, err, ErrMissingUsername)

	ch, err := e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", a.thumb)
	require.NoError(t, err)
	wrongSig, err := SignShareClaim(b.certKey, ScopeSystem.ClaimLabel(), "alice", ch.Nonce)
	require.NoError(t, err)
	_, err = e.svc.CompleteClaimShare(ctx, seal.DomainUnseal, "alice", hex.EncodeToString(ch.Nonce), wrongSig, a.thumb)
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
	_, err = e.svc.CompleteClaimShare(ctx, seal.DomainUnseal, "alice", hex.EncodeToString(ch.Nonce), wrongSig, a.thumb)
	assert.ErrorIs(t, err, ErrChallengeNotFound, "a claim nonce is single use")

	e.claim(seal.DomainUnseal, a)
	share := a.openShare(t)
	assert.NotEmpty(t, share)

	_, err = e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", a.thumb)
	assert.ErrorIs(t, err, ErrNoPendingShare, "an envelope is delivered once")

	env, err := ParseShareEnvelope(a.envelope)
	require.NoError(t, err)
	_, err = env.OpenWithKey(b.certKey)
	assert.Error(t, err, "another holder's key cannot open the envelope")

	assert.Equal(t, StateAwaitingInit, e.svc.State(), "Initialize was not used, so state does not advance")
}

// TestClaim_WrongNonceRefused pins the nonce echo check.
func TestClaim_WrongNonceRefused(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.register(seal.DomainUnseal, a, b)
	_, err := e.barrier.InitializeShamir(ctx, "")
	require.NoError(t, err)

	ch, err := e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", a.thumb)
	require.NoError(t, err)
	sig, err := SignShareClaim(a.certKey, ScopeSystem.ClaimLabel(), "alice", ch.Nonce)
	require.NoError(t, err)
	_, err = e.svc.CompleteClaimShare(ctx, seal.DomainUnseal, "alice", "00ff", sig, a.thumb)
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
	_, err = e.svc.CompleteClaimShare(ctx, seal.DomainUnseal, "", "00", sig, a.thumb)
	assert.ErrorIs(t, err, ErrMissingUsername)
}

// TestQuorum_ThreeOfFiveUnsealAfterRestart is the core custody flow: five
// holders of mixed roles, KEMs and factors; the node restarts sealed; three
// holders unseal it with different factors; a duplicate submission is
// refused.
func TestQuorum_ThreeOfFiveUnsealAfterRestart(t *testing.T) {
	e := newHolderEnv(t, 3, 5)
	holders := fiveHolders(t)
	e.initShamir(seal.FactorPolicyAnyEnrolled, holders...)
	require.False(t, e.barrier.IsSealed())

	b2, svc2 := e.restart(3, 5)

	out, err := e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, holders[0], mtls.Name)
	require.NoError(t, err)
	assert.Equal(t, 1, out.Submitted)
	assert.Equal(t, 3, out.Required)

	_, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, holders[0], mtls.Name)
	assert.True(t, errorAs[*seal.HolderAlreadySubmittedError](err), "a holder counts once: %v", err)

	_, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, holders[2], testFactorName)
	require.NoError(t, err)
	require.True(t, b2.IsSealed())
	out, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, holders[3], testFactorName)
	require.NoError(t, err)
	assert.True(t, out.Complete)
	assert.False(t, b2.IsSealed(), "three authenticated holders unseal")
}

// TestQuorum_DesignatedPolicyRefusesOtherFactors pins the designated policy:
// only the factor fixed at enrolment is admitted.
func TestQuorum_DesignatedPolicyRefusesOtherFactors(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a := newHolder(t, "alice", "").withAuthenticator(t)
	b := newHolder(t, "bob", "")
	e.initShamir(seal.FactorPolicyDesignated, a, b)
	b2, svc2 := e.restart(2, 2)

	// The request is answered with a decoy, so it does not reveal that alice
	// exists; the decoy never verifies.
	ch, err := svc2.BeginQuorum(ctx, seal.DomainUnseal, seal.OpUnseal, "alice", testFactorName)
	require.NoError(t, err)
	_, err = b2.SubmitHolderShare(ctx, &seal.HolderSubmission{
		Domain: seal.DomainUnseal, Op: seal.OpUnseal, Holder: "alice",
		Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: testFactorName, Response: a.answer(t, ch)},
		Share: a.openShare(t),
	})
	requireAuthRefused(t, err) // designated policy admits only mtls

	out, err := e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, a, "")
	require.NoError(t, err, "an empty factor selects the designated one")
	assert.Equal(t, 1, out.Submitted)
}

// TestQuorum_AuthenticationRefusals covers the verifier's refusals.
func TestQuorum_AuthenticationRefusals(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 3)
	a, b, c := newHolder(t, "alice", ""), newHolder(t, "bob", ""), newHolder(t, "carol", "")
	e.initShamir("", a, b, c)
	b2, svc2 := e.restart(2, 3)

	submit := func(proof seal.HolderProof, h *testHolder, op seal.QuorumOp) error {
		_, err := b2.SubmitHolderShare(ctx, &seal.HolderSubmission{Domain: seal.DomainUnseal, Op: op, Holder: h.username, Proof: proof, Share: h.openShare(t)})
		return err
	}

	// Unknown challenge.
	err := submit(seal.HolderProof{ChallengeID: "nope", Factor: mtls.Name, Response: []byte{1}}, a, seal.OpUnseal)
	requireAuthCause(t, err, ErrQuorumChallengeNotFound)

	// A challenge issued to alice cannot be spent by bob.
	ch, err := svc2.BeginQuorum(ctx, seal.DomainUnseal, seal.OpUnseal, "alice", "")
	require.NoError(t, err)
	err = submit(seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: mtls.Name, Response: b.answer(t, ch)}, b, seal.OpUnseal)
	requireAuthCause(t, err, ErrQuorumChallengeMismatch)

	// A challenge is consumed by a failed attempt.
	err = submit(seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: mtls.Name, Response: a.answer(t, ch)}, a, seal.OpUnseal)
	requireAuthCause(t, err, ErrQuorumChallengeNotFound)

	// A bad signature.
	ch, err = svc2.BeginQuorum(ctx, seal.DomainUnseal, seal.OpUnseal, "alice", "")
	require.NoError(t, err)
	err = submit(seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: mtls.Name, Response: b.answer(t, ch)}, a, seal.OpUnseal)
	assert.True(t, errorAs[*seal.HolderAuthenticationError](err), err)

	// The TLS peer certificate, when present, must be the enrolled one.
	ch, err = svc2.BeginQuorum(ctx, seal.DomainUnseal, seal.OpUnseal, "alice", "")
	require.NoError(t, err)
	err = submit(seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: mtls.Name, Response: a.answer(t, ch), PeerCertThumbprint: b.thumb}, a, seal.OpUnseal)
	assert.True(t, errorAs[*seal.HolderAuthenticationError](err), err)

	// A client cannot forge the peer thumbprint through response metadata.
	ch, err = svc2.BeginQuorum(ctx, seal.DomainUnseal, seal.OpUnseal, "alice", "")
	require.NoError(t, err)
	err = submit(seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: mtls.Name, Response: a.answer(t, ch),
		ResponseMetadata: map[string]string{"peer_cert_thumbprint": b.thumb}}, a, seal.OpUnseal)
	require.NoError(t, err, "client-supplied peer metadata is discarded")

	// Holders outside the roster get a decoy, refused at submission.
	ch, err = svc2.BeginQuorum(ctx, seal.DomainUnseal, seal.OpUnseal, "mallory", "")
	require.NoError(t, err)
	err = submit(seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: mtls.Name, Response: c.answer(t, ch)}, c, seal.OpUnseal)
	requireAuthRefused(t, err)

	// An operation outside the domain is refused.
	_, err = svc2.BeginQuorum(ctx, seal.DomainUnseal, seal.OpRecover, "alice", "")
	assert.True(t, errorAs[*seal.QuorumOpInvalidError](err), err)
}

// TestQuorum_PendingChallengesAreBounded pins that no caller can grow the
// challenge table without limit: once every slot a holder has for an
// operation is bound to the holder's certificate, a request that does not
// present it is answered with a challenge stored apart, in the bounded decoy
// table, rather than evicting one of the holder's.
func TestQuorum_PendingChallengesAreBounded(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)
	for i := 0; i < maxPendingPerHolderOp; i++ {
		_, err := e.svc.BeginQuorum(withPeerCert(t, ctx, a), seal.DomainUnseal, seal.OpSeal, "alice", "")
		require.NoError(t, err)
	}
	fallback, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err)
	assert.NotContains(t, e.svc.system.quorum, fallback.ChallengeID)
	assert.Contains(t, e.svc.system.decoys, fallback.ChallengeID)
	assert.Len(t, e.svc.system.quorum, maxPendingPerHolderOp)
	_, err = e.svc.BeginQuorum(withPeerCert(t, ctx, a), seal.DomainUnseal, seal.OpSeal, "alice", "")
	assert.NoError(t, err, "the holder's own certificate evicts its oldest challenge")
	assert.LessOrEqual(t, len(e.svc.system.quorum), maxPendingPerHolderOp)
}

// TestQuorum_SealAndRootToken drives the seal and root-token operations.
func TestQuorum_SealAndRootToken(t *testing.T) {
	e := newHolderEnv(t, 2, 3)
	holders := []*testHolder{newHolder(t, "alice", ""), newHolder(t, "bob", ""), newHolder(t, "carol", "")}
	e.initShamir("", holders...)

	_, err := e.submit(e.svc, e.barrier, seal.DomainUnseal, seal.OpRootToken, holders[0], "")
	require.NoError(t, err)
	out, err := e.submit(e.svc, e.barrier, seal.DomainUnseal, seal.OpRootToken, holders[1], "")
	require.NoError(t, err)
	require.True(t, out.Complete)
	require.NotNil(t, out.RootToken)
	assert.NotEmpty(t, out.RootToken.Token)

	_, err = e.submit(e.svc, e.barrier, seal.DomainUnseal, seal.OpSeal, holders[1], "")
	require.NoError(t, err)
	out, err = e.submit(e.svc, e.barrier, seal.DomainUnseal, seal.OpSeal, holders[2], "")
	require.NoError(t, err)
	assert.True(t, out.Complete)
	assert.True(t, e.barrier.IsSealed())

	// Sealed, the node issues a challenge for the unseal round only.
	for _, op := range []seal.QuorumOp{seal.OpRootToken, seal.OpSeal, seal.OpRekey} {
		_, err = e.svc.BeginQuorum(context.Background(), seal.DomainUnseal, op, "alice", "")
		assert.ErrorIs(t, err, seal.ErrSealed, op)
	}
	_, err = e.svc.BeginQuorum(context.Background(), seal.DomainUnseal, seal.OpUnseal, "alice", "")
	assert.NoError(t, err)
}

// TestRevoke_QuorumOfRemainingHoldersRedistributes pins revocation: it is a
// rekey that the revoked holder cannot take part in, and on completion the
// remaining holders hold fresh envelopes while the revoked holder's old
// share no longer opens the barrier.
func TestRevoke_QuorumOfRemainingHoldersRedistributes(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 3)
	a, b, c := newHolder(t, "alice", ""), newHolder(t, "bob", ""), newHolder(t, "carol", "")
	e.initShamir("", a, b, c)
	oldCarolEnvelope := c.envelope

	round, err := e.svc.RevokeOfficer(ctx, seal.DomainUnseal, "carol", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"carol"}, round.Remove)

	_, err = e.submit(e.svc, e.barrier, seal.DomainUnseal, seal.OpRekey, c, "")
	assert.True(t, errorAs[*seal.HolderRevokedError](err), "the revoked holder cannot vote on its own removal: %v", err)

	_, err = e.submit(e.svc, e.barrier, seal.DomainUnseal, seal.OpRekey, a, "")
	require.NoError(t, err)
	out, err := e.submit(e.svc, e.barrier, seal.DomainUnseal, seal.OpRekey, b, "")
	require.NoError(t, err)
	require.True(t, out.Complete)
	require.NotNil(t, out.Rekey)
	assert.Equal(t, 2, out.Rekey.TotalShares)

	list, err := e.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	require.Len(t, list, 2)
	for _, s := range list {
		assert.NotEqual(t, "carol", s.Username)
	}
	e.claim(seal.DomainUnseal, a)
	e.claim(seal.DomainUnseal, b)

	// After a restart the new split opens the barrier and carol's old share
	// is neither in the roster nor a valid share.
	b2, svc2 := e.restart(2, 2)
	c.envelope = oldCarolEnvelope
	ch, err := svc2.BeginQuorum(ctx, seal.DomainUnseal, seal.OpUnseal, "carol", "")
	require.NoError(t, err, "a revoked holder gets a decoy")
	_, err = b2.SubmitHolderShare(ctx, &seal.HolderSubmission{
		Domain: seal.DomainUnseal, Op: seal.OpUnseal, Holder: "carol",
		Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: c.answer(t, ch)},
		Share: c.openShare(t),
	})
	// The decoy carries a decoy key, so even carol's own answer fails its
	// verification exactly as a wrong answer to a holder's challenge would;
	// roster membership is not revealed.
	assert.True(t, errorAs[*seal.HolderAuthenticationError](err), err)
	requireAuthRefused(t, err)
	assert.False(t, errorAs[*seal.HolderNotInRosterError](err), err)
	_, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, a, "")
	require.NoError(t, err)
	out, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, b, "")
	require.NoError(t, err)
	assert.True(t, out.Complete)
	assert.False(t, b2.IsSealed())
}

// TestRevoke_RefusedWhenItBreaksTheQuorum pins that revocation refuses to
// leave fewer holders than the threshold.
func TestRevoke_RefusedWhenItBreaksTheQuorum(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)
	_, err := e.svc.RevokeOfficer(ctx, seal.DomainUnseal, "bob", 0)
	assert.True(t, errorAs[*seal.RevocationBreaksQuorumError](err), err)
	_, err = e.svc.RevokeOfficer(ctx, seal.DomainUnseal, "", 0)
	assert.ErrorIs(t, err, ErrMissingUsername)
}

// TestRecoveryDomain_IndependentKeyRecovers drives the recovery domain: its
// holders are separate, its key independent, and a recovery quorum opens a
// sealed barrier.
func TestRecoveryDomain_IndependentKeyRecovers(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)
	r1, r2 := newHolder(t, "rec-1", seal.RoleCustodian), newHolder(t, "rec-2", seal.RoleCustodian)
	e.register(seal.DomainRecovery, r1, r2)
	_, err := e.barrier.GenerateRecoveryKeys(ctx, 2, "")
	require.NoError(t, err)
	e.claim(seal.DomainRecovery, r1)
	e.claim(seal.DomainRecovery, r2)

	b2, svc2 := e.restart(2, 2)
	_, err = svc2.BeginQuorum(ctx, seal.DomainRecovery, seal.OpUnseal, "rec-1", "")
	assert.True(t, errorAs[*seal.QuorumOpInvalidError](err), "the recovery domain only recovers: %v", err)
	_, err = e.submit(svc2, b2, seal.DomainRecovery, seal.OpRecover, r1, "")
	require.NoError(t, err)
	out, err := e.submit(svc2, b2, seal.DomainRecovery, seal.OpRecover, r2, "")
	require.NoError(t, err)
	assert.True(t, out.Complete)
	assert.False(t, b2.IsSealed())
}
