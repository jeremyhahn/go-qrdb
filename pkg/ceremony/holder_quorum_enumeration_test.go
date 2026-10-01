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
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// publicFace is what a quorum challenge shows about the holder it names that
// does not change from one request to the next: the enrolled key and the
// certificate thumbprint.
func publicFace(ch *QuorumChallenge) (string, string) {
	return ch.MethodData[mtls.MethodDataPublicKey], ch.MethodData[mtls.MetaThumbprint]
}

// challengeCurve is the curve of the key a challenge carries.
func challengeCurve(t *testing.T, ch *QuorumChallenge) string {
	t.Helper()
	der, err := base64.StdEncoding.DecodeString(ch.MethodData[mtls.MethodDataPublicKey])
	require.NoError(t, err)
	pub, err := x509.ParsePKIXPublicKey(der)
	require.NoError(t, err)
	ec, ok := pub.(*ecdsa.PublicKey)
	require.True(t, ok, "challenge key is %T", pub)
	return ec.Curve.Params().Name
}

// TestQuorum_DecoyIsStableAcrossRequests: a real holder's challenge shows the
// same key and thumbprint on every request, so a decoy must too -- otherwise
// asking twice tells a holder from a name the roster does not hold.
func TestQuorum_DecoyIsStableAcrossRequests(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)

	for _, name := range []string{"alice", "mallory"} {
		first, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, name, "")
		require.NoError(t, err)
		second, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, name, "")
		require.NoError(t, err)
		k1, t1 := publicFace(first)
		k2, t2 := publicFace(second)
		assert.Equal(t, k1, k2, "%s: the key a challenge shows must not change between requests", name)
		assert.Equal(t, t1, t2, "%s: the thumbprint a challenge shows must not change between requests", name)
		assert.NotEqual(t, first.Nonce, second.Nonce, "%s: the nonce is fresh", name)
	}

	mallory, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "mallory", "")
	require.NoError(t, err)
	trent, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "trent", "")
	require.NoError(t, err)
	mk, mt := publicFace(mallory)
	tk, tt := publicFace(trent)
	assert.NotEqual(t, mk, tk, "two names never share a decoy key, as two holders never share a key")
	assert.NotEqual(t, mt, tt)
	assert.Len(t, mt, 64, "a decoy thumbprint is a hex SHA-256, like a real one")
}

// TestQuorum_DecoyMatchesRosterKeyKind: the decoy's key is on the curve the
// roster's holders use.
func TestQuorum_DecoyMatchesRosterKeyKind(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newP384Holder(t, "alice", ""), newP384Holder(t, "bob", "")
	e.initShamir("", a, b)

	real, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err)
	decoy, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "mallory", "")
	require.NoError(t, err)
	assert.Equal(t, challengeCurve(t, real), challengeCurve(t, decoy))
}

// rosterWith is a roster whose holders' mtls keys are pubs.
func rosterWith(t *testing.T, pubs ...any) *seal.HolderRoster {
	t.Helper()
	r := &seal.HolderRoster{}
	for _, pub := range pubs {
		spki, err := x509.MarshalPKIXPublicKey(pub)
		require.NoError(t, err)
		r.Holders = append(r.Holders, seal.RosterHolder{Factors: []seal.RosterFactor{{Name: mtls.Name, PublicKey: spki}}})
	}
	return r
}

// TestRosterDecoyKind_Predominant: the most common derivable kind wins, ties
// go to the earlier kind, and a roster with none yields P-256.
func TestRosterDecoyKind_Predominant(t *testing.T) {
	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	p521, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	require.NoError(t, err)
	edPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	assert.Equal(t, decoyEd25519, rosterDecoyKind(rosterWith(t, edPub, edPub, &p256.PublicKey)))
	assert.Equal(t, decoyP521, rosterDecoyKind(rosterWith(t, &p521.PublicKey, &rsaKey.PublicKey)))
	assert.Equal(t, decoyP256, rosterDecoyKind(rosterWith(t, &p256.PublicKey, edPub)))
	assert.Equal(t, decoyP256, rosterDecoyKind(rosterWith(t, &rsaKey.PublicKey)))
	assert.Equal(t, decoyP256, rosterDecoyKind(nil))
}

// TestDecoyIdentity_EveryKindDerives: every kind derives a parseable key of
// that kind, deterministically.
func TestDecoyIdentity_EveryKindDerives(t *testing.T) {
	h := &holderScope{domain: seal.DomainUnseal, decoySeed: make([]byte, 32)}
	for kind := decoyP256; kind <= decoyEd25519; kind++ {
		first, err := h.decoyIdentityFor("mallory", kind)
		require.NoError(t, err, kind.String())
		again, err := h.decoyIdentityFor("mallory", kind)
		require.NoError(t, err)
		assert.Equal(t, first, again, kind.String())
		got, ok := spkiDecoyKind(first.spki)
		require.True(t, ok, kind.String())
		assert.Equal(t, kind, got)
	}
	_, ok := spkiDecoyKind([]byte("not a key"))
	assert.False(t, ok)
}

// submitBogus submits a share for holder whose proof answers no challenge.
func submitBogus(t *testing.T, e *holderEnv, holder string, share []byte) error {
	t.Helper()
	_, err := e.barrier.SubmitHolderShare(context.Background(), &seal.HolderSubmission{
		Domain: seal.DomainUnseal, Op: seal.OpSeal, Holder: holder,
		Proof: seal.HolderProof{ChallengeID: "no-such-challenge", Factor: mtls.Name, Response: []byte("signature")},
		Share: share,
	})
	return err
}

// TestQuorum_SubmitDoesNotRevealHolders: a submission whose proof does not
// verify is refused the same way whoever it names -- a stranger, a holder, a
// holder who has already submitted -- so submitting does not enumerate the
// roster or the round's progress. Only a proof that verifies learns more.
func TestQuorum_SubmitDoesNotRevealHolders(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 3)
	a, b, c := newHolder(t, "alice", ""), newHolder(t, "bob", ""), newHolder(t, "carol", "")
	e.initShamir("", a, b, c)

	ch, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err)
	out, err := submitChallenge(t, e, ch, a)
	require.NoError(t, err)
	require.False(t, out.Complete)

	for _, holder := range []string{"mallory", "bob", "alice"} {
		err := submitBogus(t, e, holder, []byte("share"))
		var authErr *seal.HolderAuthenticationError
		require.ErrorAs(t, err, &authErr, "%s: an unverified submission is an authentication failure", holder)
		assert.ErrorIs(t, err, ErrQuorumChallengeNotFound, holder)
		var notInRoster *seal.HolderNotInRosterError
		var already *seal.HolderAlreadySubmittedError
		assert.False(t, errors.As(err, &notInRoster), "%s: roster membership leaked", holder)
		assert.False(t, errors.As(err, &already), "%s: round progress leaked", holder)
	}

	// A verified proof still learns that its holder already submitted.
	again, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err)
	_, err = submitChallenge(t, e, again, a)
	var already *seal.HolderAlreadySubmittedError
	require.ErrorAs(t, err, &already)
}

// TestQuorum_SubmitIsRateLimitedPerCaller: submissions pass the same throttle
// as challenge requests, in a bucket of their own, before anything about them
// is examined, so an anonymous caller cannot spend unlimited guesses on
// submit either.
func TestQuorum_SubmitIsRateLimitedPerCaller(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)
	limiter := newRecordingLimiter(2)
	e.svc.SetQuorumLimiter(limiter.allow)

	for i := 0; i < 2; i++ {
		require.ErrorIs(t, submitBogus(t, e, "mallory", []byte("share")), ErrQuorumChallengeNotFound)
	}
	var limited *QuorumRateLimitedError
	require.ErrorAs(t, submitBogus(t, e, "alice", []byte("share")), &limited, "the caller's submission bucket is spent")
	_, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err, "challenge requests have a bucket of their own")
	require.Len(t, limiter.seen, 2, "one submission and one challenge bucket for the anonymous caller")
}

// TestQuorum_FullBoundSlotsAnswerUnboundWithAnUnstoredChallenge: when every
// slot a holder has for an operation is bound to their certificate, an
// unbound request gets a challenge that looks exactly like a real one -- the
// holder's own key and thumbprint -- but is not stored, rather than an error
// no stranger's name could ever produce. The bound challenges survive.
func TestQuorum_FullBoundSlotsAnswerUnboundWithAnUnstoredChallenge(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)

	var bound []*QuorumChallenge
	for i := 0; i < maxPendingPerHolderOp; i++ {
		ch, err := e.svc.BeginQuorum(withPeerCert(t, ctx, a), seal.DomainUnseal, seal.OpSeal, "alice", "")
		require.NoError(t, err)
		bound = append(bound, ch)
	}
	unbound, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err, "an unbound request must not be told the holder's slots are full")
	bk, bt := publicFace(bound[0])
	uk, ut := publicFace(unbound)
	assert.Equal(t, bk, uk)
	assert.Equal(t, bt, ut)

	_, err = submitChallenge(t, e, unbound, a)
	require.ErrorIs(t, err, ErrQuorumChallengeNotFound, "the unbound challenge was never stored")
	out, err := submitChallenge(t, e, bound[0], a)
	require.NoError(t, err, "the bound challenges survive")
	require.False(t, out.Complete)
}
