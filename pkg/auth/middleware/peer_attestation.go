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

package middleware

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// PeerAttestationPolicy says which requests require an attested peer, and how
// fresh the evidence behind the verdict must be (go-qrdb#278).
type PeerAttestationPolicy struct {
	// Permissions are the operations that require the calling peer to be
	// attested. A request for any other permission is not gated. Required:
	// a gate that gates nothing is a configuration mistake, not a posture.
	Permissions []permission.Permission

	// MaxEvidenceAge is the oldest evidence the gate accepts, measured from
	// the verdict's CollectedAt. Required and positive. The gate enforces it
	// itself, whatever bound the verifier applied.
	MaxEvidenceAge time.Duration
}

// PeerAttestationGate is the attested-caller authorization gate: after a
// request has been authenticated and RBAC has granted it, a request for a
// gated permission is admitted only when the injected
// auth.PeerAttestationVerifier reports that the peer on the connection is
// currently attested (go-qrdb#278).
//
// It lives in the one authorization funnel every network transport shares
// (AuthMiddleware.authorize), so installing it with WithPeerAttestationGate
// gates REST, gRPC, unix, QUIC and MCP identically. The in-process embedded
// transport has no peer and is not gated.
//
// Every refusal is fail-closed and distinguishable: *PeerAttestationRefusedError
// carries the reason and whether it is a security event, and a refusal is
// audited as its own "auth.peer_attestation" record beside the ordinary
// authorization decision.
type PeerAttestationGate struct {
	verifier auth.PeerAttestationVerifier
	gated    map[permission.Permission]struct{}
	maxAge   time.Duration
	now      func() time.Time
}

// NewPeerAttestationGate validates the policy and builds the gate. A nil
// verifier, an empty permission set, or a non-positive maximum evidence age is
// a *PeerAttestationConfigError: the embedder asked for a gate and did not
// describe one, and running without the gate it asked for would be the
// fail-open reading of that mistake.
func NewPeerAttestationGate(verifier auth.PeerAttestationVerifier, policy PeerAttestationPolicy) (*PeerAttestationGate, error) {
	if verifier == nil {
		return nil, &PeerAttestationConfigError{Reason: "no verifier"}
	}
	if len(policy.Permissions) == 0 {
		return nil, &PeerAttestationConfigError{Reason: "no gated permissions"}
	}
	if policy.MaxEvidenceAge <= 0 {
		return nil, &PeerAttestationConfigError{Reason: "max evidence age must be positive"}
	}
	gated := make(map[permission.Permission]struct{}, len(policy.Permissions))
	for _, p := range policy.Permissions {
		if p == "" {
			return nil, &PeerAttestationConfigError{Reason: "empty permission in the gated set"}
		}
		gated[p] = struct{}{}
	}
	return &PeerAttestationGate{
		verifier: verifier,
		gated:    gated,
		maxAge:   policy.MaxEvidenceAge,
		now:      time.Now,
	}, nil
}

// Gates reports whether perm requires an attested peer.
func (g *PeerAttestationGate) Gates(perm permission.Permission) bool {
	if g == nil {
		return false
	}
	_, ok := g.gated[perm]
	return ok
}

// WithPeerAttestationGate installs the attested-caller gate. A nil gate
// installs nothing.
func WithPeerAttestationGate(g *PeerAttestationGate) Option {
	return func(m *AuthMiddleware) {
		m.peerAttestation = g
	}
}

// PeerAttestationReason says why the gate refused. Each value is stable: it is
// the "reason" attribute of the audit record.
type PeerAttestationReason string

const (
	// PeerAttestationNoPeerCertificate: the connection carries no verified
	// client certificate, so there is no peer to attest.
	PeerAttestationNoPeerCertificate PeerAttestationReason = "no-peer-certificate"

	// PeerAttestationForwarded: the request was forwarded by a cluster node on
	// the subject's behalf, so the certificate on the connection is the
	// forwarder's and the subject's measured state cannot be checked here. Not
	// a security event: it is how a follower routes a write, and the remedy is
	// to send the gated operation to the node that serves it.
	PeerAttestationForwarded PeerAttestationReason = "forwarded"

	// PeerAttestationVerifierError: the verifier could not decide.
	PeerAttestationVerifierError PeerAttestationReason = "verifier-error"

	// PeerAttestationNoVerdict: the verifier returned neither a verdict nor an
	// error.
	PeerAttestationNoVerdict PeerAttestationReason = "no-verdict"

	// PeerAttestationPeerMismatch: the verdict is about a different peer than
	// the one on the connection.
	PeerAttestationPeerMismatch PeerAttestationReason = "peer-mismatch"

	// PeerAttestationNoEvidence: the verdict names no collection time.
	PeerAttestationNoEvidence PeerAttestationReason = "no-evidence"

	// PeerAttestationEvidenceStale: the evidence is older than the policy's
	// maximum age.
	PeerAttestationEvidenceStale PeerAttestationReason = "evidence-stale"

	// PeerAttestationEvidenceFromFuture: the evidence claims to have been
	// collected after now, which would make it fresh forever.
	PeerAttestationEvidenceFromFuture PeerAttestationReason = "evidence-from-future"

	// PeerAttestationStateRejected: the verifier judged the evidence and
	// refused it.
	PeerAttestationStateRejected PeerAttestationReason = "state-rejected"

	// PeerAttestationInconsistentVerdict: the verdict passed and named a
	// failure class at the same time.
	PeerAttestationInconsistentVerdict PeerAttestationReason = "inconsistent-verdict"
)

// peerAttestationAuditAction is the audit Action of a gate refusal. It is not
// "auth.authorize": the decision record says the request was denied, this one
// says it was denied because the peer's measured state was unacceptable.
const peerAttestationAuditAction = "auth.peer_attestation"

// check renders the gate's decision for one request. It returns nil when perm
// is not gated or the peer is attested; otherwise a
// *PeerAttestationRefusedError. It never admits on an error.
func (g *PeerAttestationGate) check(ctx context.Context, identity *auth.Identity, perm permission.Permission) error {
	if !g.Gates(perm) {
		return nil
	}
	refuse := func(reason PeerAttestationReason, securityRelevant bool, detail string, err error) error {
		e := &PeerAttestationRefusedError{
			Permission:       perm,
			Reason:           reason,
			SecurityRelevant: securityRelevant,
			Detail:           detail,
			Err:              err,
		}
		if identity != nil {
			e.Subject, e.TenantID = identity.Subject, identity.TenantID
		}
		return e
	}

	// FIRST, before the verifier is asked anything: a forwarded identity's
	// connection certificate belongs to the forwarding node, so no verdict
	// about it could be a verdict about the subject. Refusing here names the
	// real cause instead of letting it surface as a thumbprint mismatch --
	// which would be reported as a security event it is not.
	if isOnBehalfIdentity(identity) {
		return refuse(PeerAttestationForwarded, false, "forwarded by node "+identity.Metadata["forwarder_nodeid"], nil)
	}

	cert, err := verifiedPeerCertificate(auth.TLSStateFromContext(ctx))
	if err != nil || cert == nil {
		return refuse(PeerAttestationNoPeerCertificate, false, "", err)
	}
	thumbprint := user.ComputeSPKIThumbprint(cert)

	verdict, err := g.verifier.VerifyPeer(ctx, auth.AttestedPeer{
		Identity:    identity,
		Certificate: cert,
		Thumbprint:  thumbprint,
	})
	switch {
	case err != nil:
		return refuse(PeerAttestationVerifierError, false, "", err)
	case verdict == nil:
		return refuse(PeerAttestationNoVerdict, false, "", nil)
	case verdict.PeerThumbprint != thumbprint:
		// Evidence for somebody else presented as this peer's is not an
		// absence -- it is the substitution the binding exists to stop.
		return refuse(PeerAttestationPeerMismatch, true, verdict.Reason, nil)
	case verdict.CollectedAt.IsZero():
		return refuse(PeerAttestationNoEvidence, false, verdict.Reason, nil)
	}

	age := g.now().Sub(verdict.CollectedAt)
	switch {
	case age < 0:
		return refuse(PeerAttestationEvidenceFromFuture, true, verdict.Reason, nil)
	case age > g.maxAge:
		return refuse(PeerAttestationEvidenceStale, false,
			"evidence age "+age.Truncate(time.Second).String()+" exceeds "+g.maxAge.String(), nil)
	case !verdict.Passed:
		return refuse(PeerAttestationStateRejected,
			verdict.Failure == auth.PeerAttestationFailureCryptographic, verdict.Reason, nil)
	case verdict.Failure != auth.PeerAttestationFailureNone:
		return refuse(PeerAttestationInconsistentVerdict, false, verdict.Reason, nil)
	}
	return nil
}

// checkPeerAttestation applies the gate, when one is installed, and records a
// refusal as its own audit event. The returned error is the refusal or, when
// that record could not be written under a fail-closed audit policy, the
// *audit.GateError -- the same rule every other gate here follows.
func (m *AuthMiddleware) checkPeerAttestation(ctx context.Context, identity *auth.Identity, perm permission.Permission) error {
	gerr := m.peerAttestation.check(ctx, identity, perm)
	if gerr == nil {
		return nil
	}
	var refusal *PeerAttestationRefusedError
	if !errors.As(gerr, &refusal) {
		// check only ever returns a refusal. Anything else is still a denial,
		// returned as it is rather than logged and audited as a refusal it
		// is not.
		return gerr
	}
	level := slog.LevelWarn
	if refusal.SecurityRelevant {
		level = slog.LevelError
	}
	slog.Log(ctx, level, "auth/middleware: peer attestation refused",
		"subject", refusal.Subject,
		"tenant", refusal.TenantID,
		"permission", perm.String(),
		"reason", string(refusal.Reason),
		"security_relevant", refusal.SecurityRelevant,
		"detail", refusal.Detail,
	)
	if auditErr := m.auditPeerAttestation(ctx, refusal); auditErr != nil {
		return auditErr
	}
	return gerr
}

// auditPeerAttestation writes the refusal's own record: action
// "auth.peer_attestation", result denied, and the reason and security
// classification as attributes, so a measured-state refusal is never mistaken
// for an ordinary login failure.
func (m *AuthMiddleware) auditPeerAttestation(ctx context.Context, refusal *PeerAttestationRefusedError) error {
	if m.audit == nil {
		return nil
	}
	err := m.audit.Log(ctx, &audit.Entry{
		Timestamp: time.Now().UTC(),
		Subject:   refusal.Subject,
		TenantID:  refusal.TenantID,
		Action:    peerAttestationAuditAction,
		Resource:  refusal.Permission.String(),
		Result:    audit.ResultDenied,
		Metadata: map[string]string{
			"reason":            string(refusal.Reason),
			"security_relevant": strconv.FormatBool(refusal.SecurityRelevant),
			"detail":            refusal.Detail,
		},
	})
	if err == nil {
		return nil
	}
	auditWriteFailures().With(auditFailureOutcomes[m.auditFailure]).Inc()
	slog.Error("auth/middleware: peer attestation audit log failed",
		"error", err,
		"subject", refusal.Subject,
		"permission", refusal.Permission.String(),
		"policy", m.auditFailure.String(),
	)
	if m.auditFailure == audit.FailOpen {
		return nil
	}
	return &audit.GateError{Component: authorizeComponent, Err: err}
}
