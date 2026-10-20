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

			// The refused opening left nothing: only the admitted one is
			// recorded, permitted and then opened.
			assert.Equal(t, []string{audit.OutcomePermitted, audit.OutcomeSuccess},
				outcomesOf(trail.ring, a.scope, ActionHolderRekeyStart, "carol"))
			assert.Equal(t, []string{audit.OutcomePermitted, audit.OutcomeSuccess},
				outcomesOf(trail.ring, a.scope, ActionHolderClaim, "alice"), "the refused claim left nothing")
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
	svc, err := NewDatabaseCeremonyService("acme", "orders", barrier, HolderDeps{TrustDomain: testTrustDomain, CA: testCA(t), Factors: reg, Audit: ring}, testLogger())
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

// outcomesOf returns the outcomes of ring's action events about holder in
// scope, in order.
func outcomesOf(ring *audit.RingBuffer, scope, action, holder string) []string {
	var out []string
	for _, ev := range eventsOf(scopeEvents(ring, scope), action) {
		if ev.Attributes["holder"] == holder {
			out = append(out, ev.Outcome)
		}
	}
	return out
}

// TestHolderAudit_GatedRegisterFailingAfterTheGate pins, in every scope,
// that the gating record of a registration says only that it was permitted:
// a registration that fails after the gate -- here the factor refuses the
// enrolment, after the certificate was issued -- leaves "permitted" followed
// by the failure on the hash-chained trail, and never "success".
func TestHolderAudit_GatedRegisterFailingAfterTheGate(t *testing.T) {
	for name := range auditScopes(t, nil) {
		t.Run(name, func(t *testing.T) {
			ring := audit.NewRingBuffer(64)
			a := auditScopes(t, ring)[name](t)
			req := newHolder(t, "alice", "").request("admin-1")
			req.Factors = []FactorEnrolment{{Name: testFactorName, Params: map[string]any{"public_key": "not-an-ed25519-key"}}}
			_, err := a.register(context.Background(), req)
			var enrolErr *HolderFactorEnrollmentError
			require.True(t, errors.As(err, &enrolErr), "the factor refused the enrolment: %v", err)

			got := outcomesOf(ring, a.scope, ActionHolderRegister, "alice")
			require.Len(t, got, 2, "the gate's record and the outcome's: %v", got)
			assert.Equal(t, audit.OutcomePermitted, got[0])
			assert.Equal(t, audit.OutcomeFailure+":"+reasonInvalidRequest, got[1], "a factor's refused enrolment has its own fixed reason")
			assert.NotContains(t, got, audit.OutcomeSuccess, "a registration that failed was recorded as a success")
		})
	}
}

// TestHolderAudit_GatedOperationsRecordPermittedThenSuccess pins, in every
// scope, that each gated operation that completes leaves two records: the
// gate's "permitted", written before the effect, then "success", written
// after it.
func TestHolderAudit_GatedOperationsRecordPermittedThenSuccess(t *testing.T) {
	for name := range auditScopes(t, nil) {
		t.Run(name, func(t *testing.T) {
			ring := audit.NewRingBuffer(256)
			a := auditScopes(t, ring)[name](t)
			ctx := context.Background()
			hs := a.enrol(t, ctx, newHolder(t, "alice", ""), newHolder(t, "bob", ""), newHolder(t, "carol", ""))
			require.NoError(t, a.distribute(ctx))
			var sec secrets
			a.claim(t, &sec, hs[0])
			_, err := a.revoke(ctx, "carol")
			require.NoError(t, err)

			want := []string{audit.OutcomePermitted, audit.OutcomeSuccess}
			assert.Equal(t, want, outcomesOf(ring, a.scope, ActionHolderRegister, "alice"), "register")
			assert.Equal(t, want, outcomesOf(ring, a.scope, ActionHolderClaim, "alice"), "claim")
			assert.Equal(t, want, outcomesOf(ring, a.scope, ActionHolderRekeyStart, "carol"), "rekey start")
		})
	}
}

// TestHolderAudit_FactorNamesValidatedBeforeTheGate pins, in every scope,
// that a registration's factor list is input validated before the gate: an
// empty factor name, one named twice and one the node does not know are each
// refused with nothing permitted -- a refusing trail does not turn them into
// gate refusals, and the trail carries each refusal alone.
func TestHolderAudit_FactorNamesValidatedBeforeTheGate(t *testing.T) {
	params := newHolder(t, "x", "").withAuthenticator(t).auth.EnrollParams()
	cases := map[string]struct {
		factors []FactorEnrolment
		want    func(error) bool
	}{
		"empty name": {[]FactorEnrolment{{Name: ""}}, isType[*DuplicateFactorError]},
		"named twice": {[]FactorEnrolment{{Name: testFactorName, Params: params}, {Name: testFactorName, Params: params}},
			isType[*DuplicateFactorError]},
		"mtls named": {[]FactorEnrolment{{Name: "mtls"}}, isType[*DuplicateFactorError]},
		"unknown":    {[]FactorEnrolment{{Name: "no-such-factor"}}, isType[*authfactors.FactorNotFoundError]},
	}
	for name := range auditScopes(t, nil) {
		for cname, c := range cases {
			t.Run(name+"/"+cname, func(t *testing.T) {
				trail := &switchableTrail{ring: audit.NewRingBuffer(64)}
				a := auditScopes(t, trail)[name](t)
				req := newHolder(t, "alice", "").request("admin-1")
				req.Factors = c.factors

				trail.refuse = true
				_, err := a.register(context.Background(), req)
				require.True(t, c.want(err), "under a refusing trail = %v (%T), want the input refusal", err, err)
				trail.refuse = false

				_, err = a.register(context.Background(), req)
				require.True(t, c.want(err), err)
				got := outcomesOf(trail.ring, a.scope, ActionHolderRegister, "alice")
				require.Len(t, got, 1, "the refusal alone: %v", got)
				assert.True(t, strings.HasPrefix(got[0], audit.OutcomeFailure+":"), got[0])
			})
		}
	}
}

// TestHolderAudit_DesignatedFactorValidatedBeforeTheGate pins, in every
// scope, that naming a designated factor the registration does not enrol is
// refused as invalid input before the gate: nothing is permitted, a trail
// that refuses every record does not turn it into a gate refusal, and the
// trail carries the refusal alone.
func TestHolderAudit_DesignatedFactorValidatedBeforeTheGate(t *testing.T) {
	for name := range auditScopes(t, nil) {
		t.Run(name, func(t *testing.T) {
			trail := &switchableTrail{ring: audit.NewRingBuffer(64)}
			a := auditScopes(t, trail)[name](t)
			req := newHolder(t, "alice", "").request("admin-1")
			req.Designated = testFactorName

			trail.refuse = true
			_, err := a.register(context.Background(), req)
			var notEnrolled *FactorNotEnrolledError
			require.True(t, errors.As(err, &notEnrolled), "an undesignatable factor under a refusing trail = %v (%T), want FactorNotEnrolledError", err, err)
			trail.refuse = false

			_, err = a.register(context.Background(), req)
			require.True(t, errors.As(err, &notEnrolled), err)
			got := outcomesOf(trail.ring, a.scope, ActionHolderRegister, "alice")
			require.Len(t, got, 1, "the refusal alone: %v", got)
			assert.True(t, strings.HasPrefix(got[0], audit.OutcomeFailure+":"), got[0])
		})
	}
}
