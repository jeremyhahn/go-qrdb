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
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// auditScope drives one holder scope through the five audited holder events:
// registration, claim, quorum submission, rekey start and revocation.
type auditScope struct {
	tenantID string
	scope    string
	domain   seal.QuorumDomain
	// quorumOp is the operation the scope's quorum opens the sealed barrier
	// with: unseal, or recover for the recovery domain.
	quorumOp   seal.QuorumOp
	barrier    *seal.StorageBarrier
	claimLabel string

	register    func(ctx context.Context, req RegisterOfficerRequest) (*RegisterOfficerResult, error)
	beginClaim  func(ctx context.Context, username, thumb string) (*ChallengeResponse, error)
	complete    func(ctx context.Context, username, nonceHex string, sig []byte, thumb string) (*ShareClaimResult, error)
	beginQuorum func(ctx context.Context, op seal.QuorumOp, username string) (*QuorumChallenge, error)
	distribute  func(ctx context.Context) error
	revoke      func(ctx context.Context, username string) (*seal.QuorumRoundInfo, error)
}

// secrets collects every piece of secret material a scope's flow handled, so
// the trail can be searched for it.
type secrets [][]byte

func (s *secrets) add(b []byte) {
	if len(b) > 0 {
		*s = append(*s, append([]byte(nil), b...))
	}
}

// enrol registers holders, recording their certificates.
func (a *auditScope) enrol(t *testing.T, ctx context.Context, holders ...*testHolder) []*testHolder {
	t.Helper()
	for _, h := range holders {
		res, err := a.register(ctx, h.request("admin-1"))
		require.NoError(t, err, h.username)
		h.recordCert(t, res.CertPEM)
	}
	return holders
}

// claim runs h's claim handshake and returns its outcome, recording the
// nonce, the signature and the envelope.
func (a *auditScope) claim(t *testing.T, sec *secrets, h *testHolder) {
	t.Helper()
	ctx := context.Background()
	ch, err := a.beginClaim(ctx, h.username, h.thumb)
	require.NoError(t, err, h.username)
	sig, err := SignShareClaim(h.certKey, a.claimLabel, h.username, ch.Nonce)
	require.NoError(t, err)
	sec.add(ch.Nonce)
	sec.add(sig)
	res, err := a.complete(ctx, h.username, hex.EncodeToString(ch.Nonce), sig, h.thumb)
	require.NoError(t, err, h.username)
	h.envelope = res.Envelope
	sec.add(res.Envelope)
}

// submit authenticates h and submits its share to op, recording the factor
// response and the share.
func (a *auditScope) submit(t *testing.T, sec *secrets, op seal.QuorumOp, h *testHolder) (*seal.QuorumOutcome, error) {
	t.Helper()
	ctx := context.Background()
	ch, err := a.beginQuorum(ctx, op, h.username)
	require.NoError(t, err, h.username)
	resp := h.answer(t, ch)
	share := h.openShare(t)
	sec.add(resp)
	sec.add(ch.Nonce)
	sec.add(share)
	return a.barrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
		Domain: a.domain,
		Op:     op,
		Holder: h.username,
		Proof:  seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: resp},
		Share:  share,
	})
}

// auditScopes builds each scope with ring as its audit trail.
func auditScopes(t *testing.T, ring audit.AuditLogger) map[string]func(t *testing.T) *auditScope {
	return auditScopesWithPolicy(t, ring, audit.FailClosed)
}

// auditScopesWithPolicy builds each scope with ring as its audit trail under
// the audit failure posture policy.
func auditScopesWithPolicy(t *testing.T, ring audit.AuditLogger, policy audit.FailurePolicy) map[string]func(t *testing.T) *auditScope {
	scoped := func(tenantID, scope string, build func(*seal.StorageBarrier, HolderDeps) (*ScopedCeremonyService, error)) func(t *testing.T) *auditScope {
		return func(t *testing.T) *auditScope {
			barrier := shamirBarrier(t, seal.NewMemoryBackend(), 2, 3)
			svc, err := build(barrier, HolderDeps{TrustDomain: testTrustDomain, CA: testCA(t), Factors: testFactors(t), Audit: ring, AuditPolicy: policy})
			require.NoError(t, err)
			barrier.SetCeremonyDistributor(svc)
			barrier.SetHolderVerifier(svc)
			return &auditScope{
				tenantID:   tenantID,
				scope:      scope,
				domain:     seal.DomainUnseal,
				quorumOp:   seal.OpUnseal,
				barrier:    barrier,
				claimLabel: svc.holder.scope.ClaimLabel(),
				register:   svc.RegisterOfficer,
				beginClaim: svc.BeginClaimShare,
				complete:   svc.CompleteClaimShare,
				beginQuorum: func(ctx context.Context, op seal.QuorumOp, u string) (*QuorumChallenge, error) {
					return svc.BeginQuorum(ctx, barrier, op, u, "")
				},
				distribute: func(ctx context.Context) error {
					_, err := barrier.InitializeShamir(ctx, "")
					return err
				},
				revoke: func(ctx context.Context, u string) (*seal.QuorumRoundInfo, error) {
					return svc.StartRekey(ctx, barrier, seal.RekeyParams{Remove: []string{u}})
				},
			}
		}
	}
	system := func(domain seal.QuorumDomain) func(t *testing.T) *auditScope {
		return func(t *testing.T) *auditScope {
			e := newHolderEnv(t, 2, 3)
			if domain == seal.DomainRecovery {
				// The recovery key is generated on an initialized barrier;
				// its unseal holders are not this test's subject, and are
				// enrolled before the trail is attached.
				e.initShamir("", newHolder(t, "so-1", ""), newHolder(t, "so-2", ""), newHolder(t, "so-3", ""))
			}
			e.svc.SetAuditLogger(ring)
			e.svc.SetAuditFailurePolicy(policy)
			a := &auditScope{
				tenantID:   tenantid.System,
				scope:      ScopeSystem.String(),
				domain:     domain,
				quorumOp:   seal.OpUnseal,
				barrier:    e.barrier,
				claimLabel: ScopeSystem.ClaimLabel(),
				register: func(ctx context.Context, req RegisterOfficerRequest) (*RegisterOfficerResult, error) {
					return e.svc.RegisterOfficer(ctx, domain, req)
				},
				beginClaim: func(ctx context.Context, u, th string) (*ChallengeResponse, error) {
					return e.svc.BeginClaimShare(ctx, domain, u, th)
				},
				complete: func(ctx context.Context, u, n string, s []byte, th string) (*ShareClaimResult, error) {
					return e.svc.CompleteClaimShare(ctx, domain, u, n, s, th)
				},
				beginQuorum: func(ctx context.Context, op seal.QuorumOp, u string) (*QuorumChallenge, error) {
					return e.svc.BeginQuorum(ctx, domain, op, u, "")
				},
				distribute: func(ctx context.Context) error {
					_, err := e.barrier.InitializeShamir(ctx, "")
					return err
				},
				revoke: func(ctx context.Context, u string) (*seal.QuorumRoundInfo, error) {
					return e.svc.RevokeOfficer(ctx, domain, u, 0)
				},
			}
			if domain == seal.DomainRecovery {
				a.scope = ScopeRecovery.String()
				a.quorumOp = seal.OpRecover
				a.claimLabel = ScopeRecovery.ClaimLabel()
				a.distribute = func(ctx context.Context) error {
					_, err := e.barrier.GenerateRecoveryKeys(ctx, 2, "")
					return err
				}
			}
			return a
		}
	}
	return map[string]func(t *testing.T) *auditScope{
		"system":   system(seal.DomainUnseal),
		"recovery": system(seal.DomainRecovery),
		"tenant": scoped("acme", ScopeTenant("acme").String(), func(b *seal.StorageBarrier, d HolderDeps) (*ScopedCeremonyService, error) {
			return NewTenantCeremonyService("acme", b, d, testLogger())
		}),
		"database": scoped("acme", ScopeDatabase("acme", "orders").String(), func(b *seal.StorageBarrier, d HolderDeps) (*ScopedCeremonyService, error) {
			return NewDatabaseCeremonyService("acme", "orders", b, d, testLogger())
		}),
	}
}

// scopeEvents returns ring's events of scope, in order.
func scopeEvents(ring *audit.RingBuffer, scope string) []audit.AuditEvent {
	var out []audit.AuditEvent
	for _, ev := range ring.Snapshot() {
		if ev.Attributes["scope"] == scope {
			out = append(out, ev)
		}
	}
	return out
}

// eventsOf returns the events of action in evs.
func eventsOf(evs []audit.AuditEvent, action string) []audit.AuditEvent {
	var out []audit.AuditEvent
	for _, ev := range evs {
		if ev.Action == action {
			out = append(out, ev)
		}
	}
	return out
}

// holderAuditAttributes is every attribute a holder event may carry. None is
// secret: names, counts and the operation.
var holderAuditAttributes = map[string]bool{
	"scope": true, "domain": true, "holder": true, "role": true,
	"op": true, "threshold": true, "policy": true, "share_index": true,
}

// assertNoSecretMaterial fails when any event carries an attribute outside
// holderAuditAttributes, or when the trail's serialized form contains any of
// sec -- raw, hex, or base64 in any alphabet -- or any holder's private key.
func assertNoSecretMaterial(t *testing.T, evs []audit.AuditEvent, sec secrets, holders ...*testHolder) {
	t.Helper()
	for _, ev := range evs {
		for k := range ev.Attributes {
			assert.True(t, holderAuditAttributes[k], "event %s carries attribute %q, which is not on the holder audit allow-list", ev.Action, k)
		}
	}
	raw, err := json.Marshal(evs)
	require.NoError(t, err)
	trail := string(raw)
	for _, h := range holders {
		der, err := x509.MarshalPKCS8PrivateKey(h.certKey)
		require.NoError(t, err)
		sec.add(der)
	}
	require.NotEmpty(t, sec, "the flow handled no secret material, so the search proves nothing")
	for _, s := range sec {
		for _, enc := range []string{
			string(s),
			hex.EncodeToString(s),
			base64.StdEncoding.EncodeToString(s),
			base64.RawStdEncoding.EncodeToString(s),
			base64.URLEncoding.EncodeToString(s),
			base64.RawURLEncoding.EncodeToString(s),
		} {
			assert.False(t, strings.Contains(trail, enc), "the audit trail contains secret material (%d bytes)", len(s))
		}
	}
}

// TestHolderAudit_EveryScopeRecordsEveryEvent drives each holder scope --
// system, recovery, tenant and database -- through enrolment, distribution,
// claims, a quorum that opens the sealed barrier, and a revocation, and
// checks the trail: every event is recorded, keyed by the owning tenant and
// naming the scope, the holder and the outcome, and none carries a share, an
// envelope, a claim or factor response, or a key.
func TestHolderAudit_EveryScopeRecordsEveryEvent(t *testing.T) {
	for name := range auditScopes(t, nil) {
		t.Run(name, func(t *testing.T) {
			ring := audit.NewRingBuffer(256)
			a := auditScopes(t, ring)[name](t)
			ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "admin-1", TenantID: a.tenantID})
			var sec secrets

			holders := a.enrol(t, ctx, newHolder(t, "alice", ""), newHolder(t, "bob", seal.RoleCustodian), newHolder(t, "carol", ""))
			require.NoError(t, a.distribute(context.Background()))
			for _, h := range holders {
				a.claim(t, &sec, h)
			}

			require.NoError(t, a.barrier.Seal())
			_, err := a.submit(t, &sec, a.quorumOp, holders[0])
			require.NoError(t, err)
			out, err := a.submit(t, &sec, a.quorumOp, holders[1])
			require.NoError(t, err)
			require.True(t, out.Complete)
			require.False(t, a.barrier.IsSealed())

			_, err = a.revoke(ctx, "carol")
			require.NoError(t, err)
			_, err = a.submit(t, &sec, seal.OpRekey, holders[0])
			require.NoError(t, err)
			out, err = a.submit(t, &sec, seal.OpRekey, holders[1])
			require.NoError(t, err)
			require.True(t, out.Complete)

			all := scopeEvents(ring, a.scope)
			for _, ev := range all {
				assert.Equal(t, a.tenantID, ev.TenantID, "%s is keyed by the scope's owning tenant", ev.Action)
				assert.Equal(t, HolderAuditResource, ev.Resource)
				assert.Equal(t, string(a.domain), ev.Attributes["domain"])
			}
			// A gated operation is recorded as permitted before its effect and
			// as a success after it; every other record is the success itself.
			gated := map[string]bool{ActionHolderRegister: true, ActionHolderClaim: true, ActionHolderRekeyStart: true}
			var evs []audit.AuditEvent
			for i, ev := range all {
				if ev.Outcome == audit.OutcomePermitted {
					require.True(t, gated[ev.Action], "%s was recorded as permitted", ev.Action)
					require.Less(t, i+1, len(all), "%s was permitted and never settled", ev.Action)
					continue
				}
				assert.Equal(t, audit.OutcomeSuccess, ev.Outcome, ev.Action)
				if gated[ev.Action] {
					assert.Equal(t, audit.OutcomePermitted, all[i-1].Outcome, "%s succeeded without being permitted first", ev.Action)
					assert.Equal(t, ev.Action, all[i-1].Action)
					assert.Equal(t, ev.Attributes["holder"], all[i-1].Attributes["holder"])
				}
				evs = append(evs, ev)
			}

			reg := eventsOf(evs, ActionHolderRegister)
			require.Len(t, reg, 3)
			for i, ev := range reg {
				assert.Equal(t, holders[i].username, ev.Attributes["holder"])
				assert.Equal(t, "admin-1", ev.Actor, "the enroller is the actor")
			}
			assert.Equal(t, string(seal.RoleCustodian), reg[1].Attributes["role"])
			assert.Equal(t, string(seal.RoleSecurityOfficer), reg[0].Attributes["role"])

			claims := eventsOf(evs, ActionHolderClaim)
			require.Len(t, claims, 3)
			for i, ev := range claims {
				assert.Equal(t, holders[i].username, ev.Attributes["holder"])
				assert.Equal(t, holders[i].username, ev.Actor, "the holder that proved its key claims")
				assert.NotZero(t, ev.Attributes["share_index"])
			}

			submits := eventsOf(evs, ActionHolderQuorumSubmit)
			require.Len(t, submits, 4)
			for i, op := range []seal.QuorumOp{a.quorumOp, a.quorumOp, seal.OpRekey, seal.OpRekey} {
				assert.Equal(t, string(op), submits[i].Attributes["op"])
				assert.Equal(t, holders[i%2].username, submits[i].Attributes["holder"])
				assert.Equal(t, holders[i%2].username, submits[i].Actor)
			}

			starts := eventsOf(evs, ActionHolderRekeyStart)
			require.Len(t, starts, 1)
			assert.Equal(t, "carol", starts[0].Attributes["holder"])
			assert.Equal(t, string(seal.OpRekey), starts[0].Attributes["op"])
			assert.Equal(t, "admin-1", starts[0].Actor)

			revoked := eventsOf(evs, ActionHolderRevoked)
			require.Len(t, revoked, 1)
			assert.Equal(t, "carol", revoked[0].Attributes["holder"])
			assert.Equal(t, holderActorQuorum, revoked[0].Actor)

			assertNoSecretMaterial(t, all, sec, holders...)
		})
	}
}

// TestHolderAudit_RefusalsAreRecorded checks that a refused registration, a
// claim with a bad signature, a submission whose proof does not verify and a
// rekey that would break the quorum are each recorded, as failures, in every
// scope -- still with no secret material.
func TestHolderAudit_RefusalsAreRecorded(t *testing.T) {
	for name := range auditScopes(t, nil) {
		t.Run(name, func(t *testing.T) {
			ring := audit.NewRingBuffer(256)
			a := auditScopes(t, ring)[name](t)
			ctx := context.Background()
			var sec secrets

			hs := a.enrol(t, ctx, newHolder(t, "alice", ""), newHolder(t, "bob", ""), newHolder(t, "carol", ""))
			alice, bob := hs[0], hs[1]
			_, err := a.register(ctx, alice.request("admin-1"))
			require.Error(t, err, "a second alice is refused")
			require.NoError(t, a.distribute(ctx))

			ch, err := a.beginClaim(ctx, "alice", alice.thumb)
			require.NoError(t, err)
			badSig, err := SignShareClaim(bob.certKey, a.claimLabel, "alice", ch.Nonce)
			require.NoError(t, err)
			sec.add(badSig)
			sec.add(ch.Nonce)
			_, err = a.complete(ctx, "alice", hex.EncodeToString(ch.Nonce), badSig, alice.thumb)
			require.ErrorIs(t, err, ErrChallengeVerificationFailed)
			a.claim(t, &sec, alice)
			a.claim(t, &sec, bob)

			require.NoError(t, a.barrier.Seal())
			qch, err := a.beginQuorum(ctx, a.quorumOp, "alice")
			require.NoError(t, err)
			forged := bob.answer(t, qch)
			share := alice.openShare(t)
			sec.add(forged)
			sec.add(share)
			_, err = a.barrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
				Domain: a.domain, Op: a.quorumOp, Holder: "alice",
				Proof: seal.HolderProof{ChallengeID: qch.ChallengeID, Factor: qch.Factor, Response: forged},
				Share: share,
			})
			require.Error(t, err, "bob's signature does not prove alice")

			_, err = a.submit(t, &sec, a.quorumOp, alice)
			require.NoError(t, err)
			_, err = a.submit(t, &sec, a.quorumOp, bob)
			require.NoError(t, err)
			_, err = a.revoke(ctx, "mallory")
			require.Error(t, err, "mallory holds no share to revoke")

			evs := scopeEvents(ring, a.scope)
			failed := func(action string) []audit.AuditEvent {
				var out []audit.AuditEvent
				for _, ev := range eventsOf(evs, action) {
					if ev.Outcome != audit.OutcomeSuccess && ev.Outcome != audit.OutcomePermitted {
						out = append(out, ev)
					}
				}
				return out
			}
			regFail := failed(ActionHolderRegister)
			require.Len(t, regFail, 1)
			assert.True(t, strings.HasPrefix(regFail[0].Outcome, audit.OutcomeFailure+":"), regFail[0].Outcome)
			assert.Equal(t, "alice", regFail[0].Attributes["holder"])

			claimFail := failed(ActionHolderClaim)
			require.Len(t, claimFail, 1)
			assert.True(t, strings.HasPrefix(claimFail[0].Outcome, audit.OutcomeDenied+":"), claimFail[0].Outcome)
			assert.Equal(t, holderActorAnonymous, claimFail[0].Actor, "an unproven claimant is not the holder")

			submitFail := failed(ActionHolderQuorumSubmit)
			require.Len(t, submitFail, 1)
			assert.True(t, strings.HasPrefix(submitFail[0].Outcome, audit.OutcomeDenied+":"), submitFail[0].Outcome)
			assert.Equal(t, "alice", submitFail[0].Attributes["holder"])
			assert.Equal(t, holderActorAnonymous, submitFail[0].Actor, "an unproven submitter is not the holder")
			assert.Equal(t, string(a.quorumOp), submitFail[0].Attributes["op"])

			startFail := failed(ActionHolderRekeyStart)
			require.Len(t, startFail, 1)
			assert.True(t, strings.HasPrefix(startFail[0].Outcome, audit.OutcomeFailure+":"), startFail[0].Outcome)
			assert.Equal(t, "mallory", startFail[0].Attributes["holder"])

			assert.Empty(t, eventsOf(evs, ActionHolderRevoked), "nothing was revoked")
			for _, ev := range evs {
				assert.Equal(t, a.tenantID, ev.TenantID)
			}
			assertNoSecretMaterial(t, evs, sec, hs...)
		})
	}
}

// refusingAuditLogger refuses every record, as a full disk refuses the
// chained audit file.
type refusingAuditLogger struct {
	err   error
	calls int
}

var _ audit.AuditLogger = (*refusingAuditLogger)(nil)

func (l *refusingAuditLogger) Emit(context.Context, audit.AuditEvent) error {
	l.calls++
	return l.err
}

func (l *refusingAuditLogger) Query(context.Context, audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}

func (l *refusingAuditLogger) Close() error { return nil }

// TestHolderAudit_FailOpenProceeds pins the fail-open posture in every scope:
// a trail that refuses every record -- the gating records of a registration,
// a claim and a rekey opening as much as the post-hoc ones -- leaves each
// operation to proceed, the failure counted rather than returned.
func TestHolderAudit_FailOpenProceeds(t *testing.T) {
	for name := range auditScopes(t, nil) {
		t.Run(name, func(t *testing.T) {
			sink := &refusingAuditLogger{err: errors.New("disk full")}
			a := auditScopesWithPolicy(t, sink, audit.FailOpen)[name](t)
			ctx := context.Background()
			hs := a.enrol(t, ctx, newHolder(t, "alice", ""), newHolder(t, "bob", ""), newHolder(t, "carol", ""))
			alice, bob := hs[0], hs[1]
			require.NoError(t, a.distribute(ctx), "a refused record does not refuse the distribution")
			var sec secrets
			a.claim(t, &sec, alice)
			a.claim(t, &sec, bob)
			require.NoError(t, a.barrier.Seal())
			_, err := a.submit(t, &sec, a.quorumOp, alice)
			require.NoError(t, err)
			out, err := a.submit(t, &sec, a.quorumOp, bob)
			require.NoError(t, err)
			assert.True(t, out.Complete)
			assert.False(t, a.barrier.IsSealed())
			_, err = a.revoke(ctx, "carol")
			require.NoError(t, err, "under fail-open an unrecorded rekey opening proceeds")
			assert.Positive(t, sink.calls, "the records were attempted")
		})
	}
}

// TestCeremonyService_SetAuditLoggerReplacesAndPropagates pins that the
// trail installed last is the one recorded on, and that it is carried to
// tenant and database authorities through HolderDeps.
func TestCeremonyService_SetAuditLoggerReplacesAndPropagates(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	first, second := audit.NewRingBuffer(8), audit.NewRingBuffer(8)
	e.svc.SetAuditLogger(first)
	e.svc.SetAuditLogger(second)
	assert.Same(t, second, e.svc.HolderDeps().Audit.(*audit.RingBuffer))

	h := newHolder(t, "alice", "")
	_, err := e.svc.RegisterOfficer(context.Background(), seal.DomainUnseal, h.request("admin"))
	require.NoError(t, err)
	assert.Empty(t, first.Snapshot())
	require.Len(t, second.Snapshot(), 2, "the registration, permitted then settled")

	e.svc.SetAuditLogger(nil)
	assert.Nil(t, e.svc.HolderDeps().Audit, "nil removes the trail")
}

// TestHolderActor covers the actor rule: the authenticated identity, else
// the fallback, else anonymous.
func TestHolderActor(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, holderActorAnonymous, holderActor(ctx, ""))
	assert.Equal(t, "alice", holderActor(ctx, "alice"))
	idCtx := auth.WithIdentity(ctx, &auth.Identity{Subject: "admin-1"})
	assert.Equal(t, "admin-1", holderActor(idCtx, "alice"))
	assert.Equal(t, "alice", holderActor(auth.WithIdentity(ctx, &auth.Identity{}), "alice"))
}

// TestHolderOutcome covers the outcome rendering.
func TestHolderOutcome(t *testing.T) {
	err := errors.New("boom")
	assert.Equal(t, audit.OutcomeSuccess, holderOutcome(nil, true))
	assert.Equal(t, audit.OutcomeDenied+":unknown", holderOutcome(err, true), "an unrecognised error's text never reaches the trail")
	assert.Equal(t, audit.OutcomeFailure+":unknown", holderOutcome(err, false))
	assert.Equal(t, audit.OutcomeDenied+":verification-failed", holderOutcome(&factorRefusedError{err: err}, true))
}

// Every refusal a holder operation records maps to a fixed token.
func TestHolderReason(t *testing.T) {
	for err, want := range map[error]string{
		ErrOfficerNotFound:                                     reasonNotFound,
		ErrQuorumChallengeNotFound:                             reasonNotFound,
		&seal.HolderNotInRosterError{Holder: "x"}:              reasonNotFound,
		ErrQuorumChallengeMismatch:                             reasonChallengeMismatch,
		ErrCertThumbprintMismatch:                              reasonChallengeMismatch,
		ErrChallengeExpired:                                    reasonExpired,
		ErrChallengeVerificationFailed:                         reasonVerificationFailed,
		&factorRefusedError{err: errors.New("pin=1234")}:       reasonVerificationFailed,
		&QuorumRateLimitedError{}:                              reasonLimitReached,
		ErrDuplicateUsername:                                   reasonDuplicate,
		ErrSelfEnrolment:                                       reasonSelfEnrolment,
		ErrRegistrationWhileSealed:                             reasonSealed,
		&InvalidHolderNameError{Name: "a/b"}:                   reasonInvalidRequest,
		&FactorNotEnrolledError{Factor: "x"}:                   reasonFactorNotAdmitted,
		ErrCANotInitialized:                                    reasonCAUnavailable,
		seal.ErrQuorumRoundInProgress:                          reasonRoundInProgress,
		&seal.RevocationBreaksQuorumError{}:                    reasonBreaksQuorum,
		&audit.GateError{Err: errors.New("x")}:                 reasonAuditUnavailable,
		&OfficerRegistryError{Err: errors.New("disk: /var/x")}: reasonUnknown,
	} {
		assert.Equal(t, want, holderReason(err), "%T %v", err, err)
	}
	assert.Equal(t, reasonExpired, holderReason(&ShareStoreError{Err: ErrShareExpired}), "a wrapped sentinel is found")
}

// A holder name that is not a valid principal name -- caller-chosen on a
// refusal -- never reaches the trail.
func TestHolderName(t *testing.T) {
	assert.Equal(t, "alice", holderName("alice"))
	assert.Equal(t, "", holderName(""))
	assert.Equal(t, holderInvalidName, holderName(strings.Repeat("x", 65)))
	assert.Equal(t, holderInvalidName, holderName("a b\n<script>"))
}

// TestHolderAudit_OneEmitterPerEvent pins that a barrier wired to the same
// trail as its holder authority does not record a second copy of a holder
// event: a quorum submission is holder-quorum-submit alone, a rekey opening
// holder-rekey-start alone, while the round's completion and the barrier's
// unseal are the barrier's own records.
func TestHolderAudit_OneEmitterPerEvent(t *testing.T) {
	ctx := context.Background()
	ring := audit.NewRingBuffer(256)
	strategy, err := seal.NewShamirStrategy(2, 2)
	require.NoError(t, err)
	barrier, err := seal.NewStorageBarrier(testLogger(), seal.NewMemoryBackend(), seal.BarrierConfig{
		RootKeyPath: "test/root-key",
		Shamir:      &seal.ShamirConfig{Threshold: 2, TotalShares: 2},
		AuditLogger: seal.NewComplianceAuditLogger(ring, tenantid.System, seal.SystemBarrierAuditName),
	}, strategy)
	require.NoError(t, err)
	svc, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir()}, barrier, testLogger())
	require.NoError(t, err)
	svc.SetCA(testCA(t))
	svc.SetTrustDomain(testTrustDomain)
	svc.SetAuditLogger(ring)

	holders := []*testHolder{newHolder(t, "alice", ""), newHolder(t, "bob", "")}
	for _, h := range holders {
		res, err := svc.RegisterOfficer(ctx, seal.DomainUnseal, h.request("admin"))
		require.NoError(t, err)
		h.recordCert(t, res.CertPEM)
	}
	_, err = barrier.InitializeShamir(ctx, "")
	require.NoError(t, err)
	for _, h := range holders {
		ch, err := svc.BeginClaimShare(ctx, seal.DomainUnseal, h.username, h.thumb)
		require.NoError(t, err)
		sig, err := SignShareClaim(h.certKey, ScopeSystem.ClaimLabel(), h.username, ch.Nonce)
		require.NoError(t, err)
		res, err := svc.CompleteClaimShare(ctx, seal.DomainUnseal, h.username, hex.EncodeToString(ch.Nonce), sig, h.thumb)
		require.NoError(t, err)
		h.envelope = res.Envelope
	}
	require.NoError(t, barrier.Seal())
	submit := func(op seal.QuorumOp, h *testHolder) {
		t.Helper()
		ch, err := svc.BeginQuorum(ctx, seal.DomainUnseal, op, h.username, "")
		require.NoError(t, err)
		_, err = barrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
			Domain: seal.DomainUnseal, Op: op, Holder: h.username,
			Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: h.answer(t, ch)},
			Share: h.openShare(t),
		})
		require.NoError(t, err)
	}
	for _, h := range holders {
		submit(seal.OpUnseal, h)
	}
	_, err = svc.StartRekey(ctx, seal.DomainUnseal, seal.RekeyParams{})
	require.NoError(t, err)

	count := func(action string) int {
		n := 0
		for _, ev := range ring.Snapshot() {
			if ev.Action == action {
				n++
			}
		}
		return n
	}
	assert.Equal(t, 2, count(ActionHolderQuorumSubmit))
	assert.Zero(t, count("quorum-share"), "the barrier records no second copy of a submission")
	var starts []string
	for _, ev := range eventsOf(ring.Snapshot(), ActionHolderRekeyStart) {
		starts = append(starts, ev.Outcome)
	}
	assert.Equal(t, []string{audit.OutcomePermitted, audit.OutcomeSuccess}, starts,
		"the holder engine records the opening, permitted then settled")
	assert.Zero(t, count("rekey-start"), "the barrier records no second copy of a rekey opening")
	assert.Equal(t, 1, count("quorum-complete"))
	assert.Positive(t, count("unseal"))
	for _, ev := range ring.Snapshot() {
		assert.Equal(t, tenantid.System, ev.TenantID, ev.Action)
	}
}
