// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// certAuditComponent labels a cert-enrolment record that could not be
// written, on qrdb_audit_write_failures_total{component=...}.
const certAuditComponent = "cert-enrollment"

// Cert-enrolment actions recorded on the compliance audit trail.
const (
	// ActionCertIssue records SignCertificate: a certificate signed from a
	// CSR for an existing account, bound to it.
	ActionCertIssue = "cert.issue"

	// ActionCertEnroll records EnrollCertificate: a bring-your-own
	// certificate verified against the tenant's trust anchors and bound to
	// an existing account.
	ActionCertEnroll = "cert.enroll"
)

// certEnrollmentEvent renders a cert-enrolment event keyed by tenantID --
// the tenant the certificate is issued or enrolled IN, which is the tenant
// AFFECTED by the operation regardless of which identity is recorded as the
// actor.
func certEnrollmentEvent(ctx context.Context, tenantID, action string, attrs map[string]any) audit.AuditEvent {
	var actor string
	if id := auth.IdentityFromContext(ctx); id != nil {
		actor = id.Subject
	}
	return audit.AuditEvent{
		Actor:      actor,
		TenantID:   tenantid.Normalize(tenantID),
		Resource:   "cert",
		Action:     action,
		Attributes: attrs,
	}
}

// gateCertAudit writes ev on the compliance trail as a GATING record, before
// the certificate it describes is issued or enrolled, and follows the
// node's one audit failure posture (auth.audit_failure_policy): fail-closed
// -- the default -- refuses the operation with *audit.GateError when the
// record cannot be written; fail-open lets it proceed, counting the gap.
// Without a configured sink it is a no-op.
func (s *CertEnrollmentService) gateCertAudit(ctx context.Context, ev audit.AuditEvent) error {
	return gateCertAuditOn(ctx, s.audit, s.auditPolicy, ev)
}

// gateCertAuditOn is gateCertAudit's underlying free function, shared with
// InvitationService.RedeemInvitation: redemption issues or enrolls a
// certificate exactly as SignCertificate and EnrollCertificate do, and is
// held to the same gating record before it takes effect.
func gateCertAuditOn(ctx context.Context, sink audit.AuditLogger, policy audit.FailurePolicy, ev audit.AuditEvent) error {
	if sink == nil {
		return nil
	}
	ev.Outcome = audit.OutcomePermitted
	return audit.EmitGated(ctx, sink, certAuditComponent, policy, ev)
}

// settleCertAudit records ev's final outcome once the operation it gated
// has run to completion -- success, or the failure that undid it (a
// binding failure revokes the certificate SignCertificate just issued, and
// the trail records the operation's true final outcome, not the
// intermediate "the certificate was signed"). Best-effort: a sink that
// refuses it is counted and logged, never returned as the caller's error.
func (s *CertEnrollmentService) settleCertAudit(ctx context.Context, ev audit.AuditEvent, err error) {
	settleCertAuditOn(ctx, s.audit, ev, err)
}

// settleCertAuditOn is settleCertAudit's underlying free function; see
// gateCertAuditOn.
func settleCertAuditOn(ctx context.Context, sink audit.AuditLogger, ev audit.AuditEvent, err error) {
	if sink == nil {
		return
	}
	ev.Outcome = certOutcome(err)
	audit.EmitBestEffort(ctx, sink, certAuditComponent, ev)
}

// certOutcome renders err as an audit outcome: success, or a fixed failure
// token. An error's own text never reaches the trail: it is free text from
// a user-store or cert-provider failure, and the trail is tenant-readable,
// hash-chained and cannot be redacted.
func certOutcome(err error) string {
	if err == nil {
		return audit.OutcomeSuccess
	}
	return audit.OutcomeFailure + ":cert-enrollment-failed"
}
