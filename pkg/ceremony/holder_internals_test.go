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
	"bytes"
	"context"
	"encoding/pem"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// TestHolderScope_DistributeRefusesAnUnknownHolder pins that a roster naming
// a holder the scope does not know distributes nothing.
func TestHolderScope_DistributeRefusesAnUnknownHolder(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	e.register(seal.DomainUnseal, newHolder(t, "alice", ""), newHolder(t, "bob", ""))
	roster, err := e.svc.PrepareRoster(ctx, seal.DomainUnseal, 2, "", nil)
	require.NoError(t, err)
	roster.Holders[1].Username = "mallory"
	secret, err := secure.NewBufferFromBytes([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	defer secret.Destroy()
	_, err = e.svc.Distribute(ctx, secret, roster, []byte("digest"))
	assert.ErrorIs(t, err, ErrOfficerNotFound)

	roster.Domain = seal.DomainRecovery
	_, err = e.svc.system.distribute(ctx, secret, roster, nil)
	assert.True(t, errorAs[*seal.QuorumOpInvalidError](err), err)
	_, err = e.svc.system.prepareRoster(ctx, seal.DomainRecovery, 2, "", nil)
	assert.True(t, errorAs[*seal.QuorumOpInvalidError](err), err)
}

// TestHolderScope_PrepareRosterNeedsEnoughHolders pins the threshold check.
func TestHolderScope_PrepareRosterNeedsEnoughHolders(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	e.register(seal.DomainUnseal, newHolder(t, "alice", ""), newHolder(t, "bob", ""))
	_, err := e.svc.PrepareRoster(ctx, seal.DomainUnseal, 3, "", nil)
	assert.ErrorIs(t, err, ErrInsufficientOfficers)
	_, err = e.svc.PrepareRoster(ctx, seal.DomainUnseal, 2, "", []string{"bob"})
	assert.ErrorIs(t, err, ErrInsufficientOfficers, "an exclusion that breaks the quorum is refused")
	_, err = e.svc.PrepareRoster(ctx, seal.DomainUnseal, 1, "", nil)
	assert.ErrorIs(t, err, ErrInsufficientOfficers)
}

// TestHolderScope_CommittedRecordsIndexesAndDropsRemoved pins the commit
// step directly.
func TestHolderScope_CommittedRecordsIndexesAndDropsRemoved(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 3)
	a, b, c := newHolder(t, "alice", ""), newHolder(t, "bob", ""), newHolder(t, "carol", "")
	e.initShamir("", a, b, c)
	roster, err := e.svc.PrepareRoster(ctx, seal.DomainUnseal, 2, "", []string{"carol"})
	require.NoError(t, err)
	roster.Distribution = "d-2"
	stage := func(r *seal.HolderRoster) {
		t.Helper()
		key, err := secure.NewBufferFromBytes(bytes.Repeat([]byte{7}, 32))
		require.NoError(t, err)
		defer key.Destroy()
		_, err = e.svc.Distribute(ctx, key, r, []byte("digest-"+r.Distribution))
		require.NoError(t, err)
	}
	stage(roster)
	require.NoError(t, e.svc.Committed(ctx, roster, []string{"carol"}))
	list, err := e.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	require.Len(t, list, 2)
	for _, s := range list {
		assert.Zero(t, s.ClaimedAt, "a new distribution resets the claim")
	}

	// A committed roster naming a holder the registry does not hold fails
	// to record, after the staged set it belongs to is swapped in.
	next, err := e.svc.PrepareRoster(ctx, seal.DomainUnseal, 2, "", nil)
	require.NoError(t, err)
	next.Distribution = "d-3"
	stage(next)
	next.Holders[0].Username = "ghost"
	assert.ErrorIs(t, e.svc.Committed(ctx, next, nil), ErrUserNotFound)

	// Nothing is staged for a distribution that was never distributed.
	var missing *StagedDistributionMissingError
	assert.ErrorAs(t, e.svc.Committed(ctx, &seal.HolderRoster{Domain: seal.DomainUnseal, Distribution: "d-4"}, nil), &missing)
}

// TestHolderScope_CleanupWipesPendingClaimsAndChallenges covers cleanup and
// challenge pruning.
func TestHolderScope_CleanupWipesPendingClaimsAndChallenges(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.register(seal.DomainUnseal, a, b)
	_, err := e.barrier.InitializeShamir(ctx, "")
	require.NoError(t, err)
	_, err = e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", a.thumb)
	require.NoError(t, err)
	ch, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err)

	// An expired challenge is pruned on the next issue.
	e.svc.system.quorum[ch.ChallengeID].challenge.ExpiresAt = time.Now().Add(-time.Minute)
	_, err = e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "bob", "")
	require.NoError(t, err)
	_, pending := e.svc.system.quorum[ch.ChallengeID]
	assert.False(t, pending, "expired challenges are pruned")

	e.svc.Cleanup()
	assert.Empty(t, e.svc.system.claims)
	assert.Empty(t, e.svc.system.quorum)
	assert.False(t, e.svc.system.allClaimed())
}

// TestHolderScope_ExpiredQuorumChallengeRefused pins the challenge expiry.
func TestHolderScope_ExpiredQuorumChallengeRefused(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)
	ch, err := e.svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpSeal, "alice", "")
	require.NoError(t, err)
	e.svc.system.quorum[ch.ChallengeID].challenge.ExpiresAt = time.Now().Add(-time.Minute)
	_, err = e.barrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
		Domain: seal.DomainUnseal, Op: seal.OpSeal, Holder: "alice",
		Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: a.answer(t, ch)},
		Share: a.openShare(t),
	})
	assert.ErrorIs(t, err, ErrChallengeExpired)
}

// TestClaim_ExpiredNonceRefused pins the claim nonce TTL.
func TestClaim_ExpiredNonceRefused(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.register(seal.DomainUnseal, a, b)
	_, err := e.barrier.InitializeShamir(ctx, "")
	require.NoError(t, err)
	ch, err := e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", a.thumb)
	require.NoError(t, err)
	e.svc.system.claims["alice"].ExpiresAt = time.Now().Add(-time.Second)
	_, err = e.svc.CompleteClaimShare(ctx, seal.DomainUnseal, "alice", "00", []byte{1}, a.thumb)
	assert.ErrorIs(t, err, ErrChallengeExpired)
	_ = ch
}

// TestIdentity_Refusals covers the identity builders' refusals.
func TestIdentity_Refusals(t *testing.T) {
	_, err := principalIdentity("", "system", spiffe.KindUser, "a", "p")
	assert.ErrorIs(t, err, ErrTrustDomainNotConfigured)
	_, err = principalIdentity(testTrustDomain, "system", spiffe.KindUser, "..", "p")
	assert.True(t, errorAs[*InvalidHolderNameError](err), err)
	_, err = principalIdentity(testTrustDomain, "", spiffe.KindUser, "a", "p")
	assert.Error(t, err, "an empty tenant is not a valid SPIFFE identity")
	_, err = holderIdentity(testTrustDomain, "system", "a", "root")
	assert.ErrorIs(t, err, ErrInvalidHolderRole)

	assert.False(t, validPrincipalName(string(make([]byte, maxHolderNameLen+1))))
	assert.True(t, validPrincipalName("a.b_c@d-e"))
}

// TestCSRAndSPKIParsing covers parseCSR and pemSPKI refusals.
func TestCSRAndSPKIParsing(t *testing.T) {
	h := newHolder(t, "a", "")
	block, _ := pem.Decode(h.csrPEM)
	der := append([]byte(nil), block.Bytes...)
	der[len(der)-3] ^= 0xff
	_, err := parseCSR(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
	assert.True(t, errorAs[*CSRParseError](err), err)
	_, err = parseCSR([]byte("x"))
	assert.True(t, errorAs[*CSRParseError](err), err)
	_, err = pemSPKI("not pem")
	assert.ErrorIs(t, err, ErrInvalidPubKeyPEM)
}

// TestEnrollmentPlan_Includes covers the nil-plan default.
func TestEnrollmentPlan_Includes(t *testing.T) {
	var nilPlan *EnrollmentPlan
	assert.True(t, nilPlan.Includes(FactorMTLS))
	assert.False(t, nilPlan.Includes(FactorFIDO2))
	plan, err := NewEnrollmentPlan([]AdminFactor{FactorMTLS})
	require.NoError(t, err)
	assert.True(t, plan.Includes(FactorMTLS))
	assert.False(t, plan.Includes(FactorPIV))
	assert.Contains(t, (&FactorUnavailableError{Factor: FactorPIV}).Error(), "unavailable")
}
