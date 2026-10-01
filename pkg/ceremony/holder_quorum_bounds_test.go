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
	"crypto/tls"
	"crypto/x509"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// submitChallenge answers ch as h and submits h's share.
func submitChallenge(t *testing.T, e *holderEnv, ch *QuorumChallenge, h *testHolder) (*seal.QuorumOutcome, error) {
	t.Helper()
	return e.barrier.SubmitHolderShare(context.Background(), &seal.HolderSubmission{
		Domain: ch.Domain, Op: ch.Op, Holder: h.username,
		Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: h.answer(t, ch)},
		Share: h.openShare(t),
	})
}

// withPeerCert is ctx carrying a TLS connection that presented h's enrolled
// certificate, as every network transport records it.
func withPeerCert(t *testing.T, ctx context.Context, h *testHolder) context.Context {
	t.Helper()
	cert, err := mtls.ParseCertificatePEM(h.certPEM)
	require.NoError(t, err)
	return auth.WithTLSState(ctx, &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}})
}

// TestQuorum_PendingChallengesBoundedPerHolderAndOp: a holder has at most
// maxPendingPerHolderOp unanswered challenges for one operation. A further
// request evicts the oldest, so naming a real holder over and over cannot
// fill the scope's table and lock every other holder out.
func TestQuorum_PendingChallengesBoundedPerHolderAndOp(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)

	var issued []*QuorumChallenge
	for i := 0; i < maxPendingPerHolderOp+1; i++ {
		ch, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
		require.NoError(t, err)
		issued = append(issued, ch)
	}
	_, err := submitChallenge(t, e, issued[0], a)
	require.ErrorIs(t, err, ErrQuorumChallengeNotFound, "the oldest challenge was evicted")
	_, err = submitChallenge(t, e, issued[len(issued)-1], a)
	require.NoError(t, err, "the newest challenge stands")

	// Another holder is unaffected by alice's churn.
	ch, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "bob", "")
	require.NoError(t, err)
	out, err := submitChallenge(t, e, ch, b)
	require.NoError(t, err)
	require.True(t, out.Complete)
}

// TestQuorum_CertificateBoundChallengeSurvivesAnonymousChurn: a challenge
// requested over a connection presenting the holder's own enrolled
// certificate is bound to it, and requests that do not present it can never
// evict it. A holder who connects with their certificate cannot be locked out
// by an anonymous caller naming them.
func TestQuorum_CertificateBoundChallengeSurvivesAnonymousChurn(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)

	bound, err := e.svc.BeginQuorum(withPeerCert(t, ctx, a), seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err)
	for i := 0; i < 2*maxPendingPerHolderOp; i++ {
		_, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
		require.NoError(t, err)
	}
	_, err = submitChallenge(t, e, bound, a)
	require.NoError(t, err, "the certificate-bound challenge outlived the anonymous requests")

	// Another holder's certificate does not bind a challenge for alice.
	notBound, err := e.svc.BeginQuorum(withPeerCert(t, ctx, b), seal.DomainUnseal, seal.OpRootToken, "alice", "")
	require.NoError(t, err)
	for i := 0; i < maxPendingPerHolderOp; i++ {
		_, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpRootToken, "alice", "")
		require.NoError(t, err)
	}
	_, err = submitChallenge(t, e, notBound, a)
	require.ErrorIs(t, err, ErrQuorumChallengeNotFound, "bob's certificate did not protect alice's challenge")
}

// challengeShape is everything a quorum challenge reveals about its holder
// apart from random values.
func challengeShape(ch *QuorumChallenge) (string, int, []string, time.Duration) {
	keys := make([]string, 0, len(ch.MethodData))
	for k := range ch.MethodData {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return ch.Factor, len(ch.Nonce), keys, time.Until(ch.ExpiresAt).Round(time.Minute)
}

// TestQuorum_UnknownHolderIsIndistinguishable: the challenge request is
// reachable anonymously in the sealed window, so it must not tell a caller
// which holder names exist. A name outside the roster, or a holder asked for
// a factor the policy does not admit, gets a challenge of the same shape as a
// real holder's; it simply never verifies.
func TestQuorum_UnknownHolderIsIndistinguishable(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)

	real, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err)
	for _, tc := range []struct{ name, holder, factor string }{
		{"unknown holder", "mallory", ""},
		{"unknown holder, explicit mtls", "mallory", mtls.Name},
		{"known holder, factor not admitted", "alice", testFactorName},
	} {
		decoy, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, tc.holder, tc.factor)
		require.NoError(t, err, tc.name)
		wantFactor, wantNonce, wantKeys, wantTTL := challengeShape(real)
		gotFactor, gotNonce, gotKeys, gotTTL := challengeShape(decoy)
		if tc.factor != "" && tc.factor != mtls.Name {
			wantFactor = tc.factor
		}
		assert.Equal(t, wantFactor, gotFactor, tc.name)
		assert.Equal(t, wantNonce, gotNonce, tc.name)
		assert.Equal(t, wantKeys, gotKeys, tc.name)
		assert.Equal(t, wantTTL, gotTTL, tc.name)
		assert.Equal(t, tc.holder, decoy.Holder, tc.name)
		assert.NotEqual(t, real.MethodData[mtls.MetaThumbprint], decoy.MethodData[mtls.MetaThumbprint], tc.name)

		sig, err := mtls.SignChallenge(a.certKey, decoy.ChallengeID, decoy.MethodData[mtls.MethodDataPurpose], decoy.Nonce)
		require.NoError(t, err)
		_, err = e.barrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
			Domain: seal.DomainUnseal, Op: seal.OpSeal, Holder: "alice",
			Proof: seal.HolderProof{ChallengeID: decoy.ChallengeID, Response: sig},
			Share: a.openShare(t),
		})
		require.ErrorIs(t, err, ErrQuorumChallengeNotFound, "%s: a decoy never verifies", tc.name)
	}
}

// recordingLimiter is a faithful QuorumLimitFunc double: it admits
// burst requests per key, then refuses, and records every key it saw.
type recordingLimiter struct {
	mu    sync.Mutex
	burst int
	seen  map[string]int
	anon  map[string]bool
}

func newRecordingLimiter(burst int) *recordingLimiter {
	return &recordingLimiter{burst: burst, seen: map[string]int{}, anon: map[string]bool{}}
}

func (l *recordingLimiter) allow(key string, anonymous bool) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen[key]++
	l.anon[key] = anonymous
	if l.seen[key] > l.burst {
		return false, time.Second
	}
	return true, 0
}

// TestQuorum_BeginIsRateLimitedPerCaller: every quorum-challenge request,
// including one for an unknown holder, passes the composer's limiter, keyed
// per scope and per caller: an authenticated identity, else a verified TLS peer
// certificate, else the one anonymous bucket of the scope. A refused request
// is *QuorumRateLimitedError carrying the retry delay, and issues
// nothing.
func TestQuorum_BeginIsRateLimitedPerCaller(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)
	lim := newRecordingLimiter(3)
	e.svc.SetQuorumLimiter(lim.allow)

	for i := 0; i < 3; i++ {
		_, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "mallory", "")
		require.NoError(t, err)
	}
	_, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
	var limited *QuorumRateLimitedError
	require.ErrorAs(t, err, &limited, "the anonymous bucket is spent, for every holder name")
	assert.Equal(t, time.Second, limited.RetryAfter)

	// A caller presenting a verified certificate has a bucket of its own, which its
	// submission draws on too.
	certCtx := withVerifiedPeerCert(t, ctx, a)
	ch, err := e.svc.BeginQuorum(certCtx, seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err)
	_, err = e.barrier.SubmitHolderShare(certCtx, &seal.HolderSubmission{
		Domain: ch.Domain, Op: ch.Op, Holder: a.username,
		Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: a.answer(t, ch)},
		Share: a.openShare(t),
	})
	require.NoError(t, err)

	// So does an authenticated identity.
	_, err = e.svc.BeginQuorum(auth.WithIdentity(ctx, &auth.Identity{Subject: "ops", TenantID: "system"}), seal.DomainUnseal, seal.OpSeal, "bob", "")
	require.NoError(t, err)

	anonymous := 0
	for key, anon := range lim.anon {
		if anon {
			anonymous++
			assert.Equal(t, 4, lim.seen[key], "every anonymous request hit one bucket")
		}
	}
	assert.Equal(t, 1, anonymous)
	assert.Len(t, lim.seen, 4, "anonymous, certificate (challenge and submission) and identity buckets")
}

// TestQuorum_ScopesHaveSeparateBuckets: the limiter key names the scope, so
// one scope's anonymous traffic does not spend another's.
func TestQuorum_ScopesHaveSeparateBuckets(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	e.initShamir("", newHolder(t, "alice", ""), newHolder(t, "bob", ""))
	lim := newRecordingLimiter(100)
	e.svc.SetQuorumLimiter(lim.allow)
	_, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err)
	// The recovery domain has no roster yet, so the request fails after the
	// limiter has admitted it: throttling runs before any roster read.
	_, err = e.svc.BeginQuorum(ctx, seal.DomainRecovery, seal.OpRecover, "alice", "")
	require.Error(t, err)
	assert.Len(t, lim.seen, 2, "the unseal and recovery scopes keep separate buckets")
}
