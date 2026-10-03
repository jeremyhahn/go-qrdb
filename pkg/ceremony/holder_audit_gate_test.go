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
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/factortest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// switchableTrail is a trail that refuses every record while refuse is set,
// as a full disk refuses the chained file, and records on ring otherwise.
type switchableTrail struct {
	ring   *audit.RingBuffer
	refuse bool
}

var _ audit.AuditLogger = (*switchableTrail)(nil)

func (s *switchableTrail) Emit(ctx context.Context, ev audit.AuditEvent) error {
	if s.refuse {
		return errors.New("audit file: no space left on device")
	}
	return s.ring.Emit(ctx, ev)
}

func (s *switchableTrail) Query(ctx context.Context, f audit.Filter) (*audit.Page, error) {
	return s.ring.Query(ctx, f)
}

func (s *switchableTrail) Close() error { return nil }

// requireGateRefusal asserts err is the trail's refusal of a gating record.
func requireGateRefusal(t *testing.T, err error, what string) {
	t.Helper()
	var gate *audit.GateError
	require.True(t, errors.As(err, &gate), "%s with a refusing trail under fail-closed = %v (%T), want *audit.GateError", what, err, err)
}

// TestHolderAudit_FailClosedRefusesUnrecordedOperations pins the posture of
// the records written BEFORE their operation takes effect, in every scope:
// under fail-closed -- the default -- a trail that cannot record a
// registration, a claim or a rekey opening refuses it with nothing changed.
// The holder is not enrolled, the envelope stays held and claimable, and no
// round opens; each succeeds once the trail records again.
func TestHolderAudit_FailClosedRefusesUnrecordedOperations(t *testing.T) {
	for name := range auditScopes(t, nil) {
		t.Run(name, func(t *testing.T) {
			trail := &switchableTrail{ring: audit.NewRingBuffer(256)}
			a := auditScopes(t, trail)[name](t)
			ctx := context.Background()

			alice, bob, carol := newHolder(t, "alice", ""), newHolder(t, "bob", ""), newHolder(t, "carol", "")
			trail.refuse = true
			_, err := a.register(ctx, alice.request("admin-1"))
			requireGateRefusal(t, err, "register")
			trail.refuse = false
			// Nothing was stored: alice's name and key are still free.
			a.enrol(t, ctx, alice, bob, carol)
			require.NoError(t, a.distribute(ctx))

			// A proved claim the trail cannot record is refused, and the
			// envelope stays held.
			ch, err := a.beginClaim(ctx, "alice", alice.thumb)
			require.NoError(t, err)
			sig, err := SignShareClaim(alice.certKey, a.claimLabel, "alice", ch.Nonce)
			require.NoError(t, err)
			trail.refuse = true
			_, err = a.complete(ctx, "alice", hex.EncodeToString(ch.Nonce), sig, alice.thumb)
			requireGateRefusal(t, err, "claim")
			trail.refuse = false
			var sec secrets
			a.claim(t, &sec, alice)
			assert.NotEmpty(t, alice.openShare(t), "the envelope survived the refused claim intact")
			a.claim(t, &sec, bob)

			trail.refuse = true
			_, err = a.revoke(ctx, "carol")
			requireGateRefusal(t, err, "rekey start")
			trail.refuse = false
			// No round was opened: a second opening is not "in progress".
			_, err = a.revoke(ctx, "carol")
			require.NoError(t, err)

			starts := eventsOf(scopeEvents(trail.ring, a.scope), ActionHolderRekeyStart)
			require.Len(t, starts, 1, "the opening is recorded once, when it was admitted")
			assert.Equal(t, audit.OutcomeSuccess, starts[0].Outcome)
			claims := eventsOf(scopeEvents(trail.ring, a.scope), ActionHolderClaim)
			require.Len(t, claims, 2)
		})
	}
}

// canaryFactor is a factor whose every refusal carries secret material in its
// text, as an injected hardware factor's error may (a PIN, a slot, a key
// handle). The trail must record the refusal without it.
type canaryFactor struct {
	*factortest.Factor
	canary string
}

func (c canaryFactor) Verify(context.Context, *authfactors.Challenge, *authfactors.Response) error {
	return errors.New("pkcs11: C_Login(slot 0, pin=" + c.canary + ") rejected the response")
}

// TestHolderAudit_FactorErrorTextNeverReachesTheTrail pins that a factor's
// own error text is never recorded: a refused submission is
// denied:verification-failed, however the factor described it.
func TestHolderAudit_FactorErrorTextNeverReachesTheTrail(t *testing.T) {
	const canary = "CANARY-7f3a91c2-secret"
	ctx := context.Background()
	ring := audit.NewRingBuffer(64)
	reg := authfactors.NewFactorRegistry()
	require.NoError(t, reg.Register(canaryFactor{Factor: factortest.New(testFactorName, 0), canary: canary}))
	barrier := shamirBarrier(t, seal.NewMemoryBackend(), 2, 2)
	svc, err := NewDatabaseCeremonyService("acme", "orders", barrier, HolderDeps{CA: testCA(t), Factors: reg, Audit: ring}, testLogger())
	require.NoError(t, err)
	barrier.SetCeremonyDistributor(svc)
	barrier.SetHolderVerifier(svc)

	alice := newHolder(t, "alice", "").withAuthenticator(t)
	bob := newHolder(t, "bob", "")
	for _, h := range []*testHolder{alice, bob} {
		res, err := svc.RegisterOfficer(ctx, h.request("admin-1"))
		require.NoError(t, err)
		h.recordCert(t, res.CertPEM)
	}
	_, err = barrier.InitializeShamir(ctx, seal.FactorPolicyAnyEnrolled)
	require.NoError(t, err)
	for _, h := range []*testHolder{alice, bob} {
		ch, err := svc.BeginClaimShare(ctx, h.username, h.thumb)
		require.NoError(t, err)
		sig, err := SignShareClaim(h.certKey, svc.holder.scope.ClaimLabel(), h.username, ch.Nonce)
		require.NoError(t, err)
		res, err := svc.CompleteClaimShare(ctx, h.username, hex.EncodeToString(ch.Nonce), sig, h.thumb)
		require.NoError(t, err)
		h.envelope = res.Envelope
	}
	require.NoError(t, barrier.Seal())

	ch, err := svc.BeginQuorum(ctx, barrier, seal.OpUnseal, "alice", testFactorName)
	require.NoError(t, err)
	_, err = barrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
		Domain: seal.DomainUnseal, Op: seal.OpUnseal, Holder: "alice",
		Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: alice.answer(t, ch)},
		Share: alice.openShare(t),
	})
	require.Error(t, err)
	var authErr *seal.HolderAuthenticationError
	require.True(t, errors.As(err, &authErr), "the factor refused the response: %v", err)

	// A caller-chosen holder name of any length or alphabet is not recorded.
	_, err = barrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
		Domain: seal.DomainUnseal, Op: seal.OpUnseal, Holder: strings.Repeat("A", 300) + "\n<script>",
		Proof: seal.HolderProof{ChallengeID: "no-such-challenge"},
		Share: []byte("not-a-share"),
	})
	require.Error(t, err)

	raw, err := json.Marshal(ring.Snapshot())
	require.NoError(t, err)
	assert.NotContains(t, string(raw), canary, "a factor's error text reached the trail")
	assert.NotContains(t, string(raw), strings.Repeat("A", 65), "a caller-chosen holder name reached the trail")
	var denied []audit.AuditEvent
	for _, ev := range eventsOf(ring.Snapshot(), ActionHolderQuorumSubmit) {
		if ev.Outcome != audit.OutcomeSuccess {
			denied = append(denied, ev)
		}
	}
	require.Len(t, denied, 2)
	assert.Equal(t, audit.OutcomeDenied+":verification-failed", denied[0].Outcome)
	assert.Equal(t, "alice", denied[0].Attributes["holder"])
	assert.Equal(t, audit.OutcomeDenied+":not-found", denied[1].Outcome)
	assert.Equal(t, holderInvalidName, denied[1].Attributes["holder"])
}
