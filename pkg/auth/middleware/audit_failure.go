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
	"log/slog"
	"strings"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// AuditFailurePolicy selects what the authorization middleware does when the
// audit logger cannot record a decision (go-qrdb#335).
//
// It is a POSTURE, not a fallback. The middleware does exactly what it is
// configured to do, once, with no retry, no degradation ladder and no
// automatic switch between the two values. An operator who wants the
// availability reading states it; nothing in the code decides it for them at
// runtime.
type AuditFailurePolicy uint8

const (
	// AuditFailClosed refuses the request when its audit record could not be
	// written. It is the ZERO VALUE on purpose: an AuthMiddleware built from a
	// struct literal that never mentions this field, by a test, an embedder or
	// a future constructor nobody remembered to update, is fail-closed. A
	// security default that has to be assigned is a security default that will
	// eventually not be.
	//
	// The reasoning for it being the default: an unrecorded GRANT is precisely
	// the event audit exists to prevent. A system that authorizes without
	// recording has lost the property, and losing it silently is worse than
	// refusing.
	AuditFailClosed AuditFailurePolicy = iota

	// AuditFailOpen permits the request when its audit record could not be
	// written, counting the event on
	// qrdb_auth_audit_write_failures_total{outcome="permitted"}.
	//
	// It gives up the property "no authorization decision is made unless its
	// audit record persisted". It exists because fail-closed has a real
	// availability cliff -- a full disk denies every authorized request -- and
	// an operator facing that needs a documented lever that is not "patch the
	// binary". Selecting it logs a warning at every startup naming the
	// property given up.
	AuditFailOpen
)

// String returns the configuration spelling of the policy.
func (p AuditFailurePolicy) String() string {
	if p == AuditFailOpen {
		return "fail-open"
	}
	return "fail-closed"
}

// auditFailurePolicyNames maps the accepted configuration spellings onto the
// policy. Empty maps to AuditFailClosed because an omitted key must resolve to
// the secure posture through the SAME table every other spelling goes through,
// rather than through a separate empty-string branch that a later edit could
// forget.
var auditFailurePolicyNames = map[string]AuditFailurePolicy{
	"":            AuditFailClosed,
	"fail-closed": AuditFailClosed,
	"failclosed":  AuditFailClosed,
	"closed":      AuditFailClosed,
	"fail-open":   AuditFailOpen,
	"failopen":    AuditFailOpen,
	"open":        AuditFailOpen,
}

// ParseAuditFailurePolicy resolves an operator's configured spelling.
//
// An unrecognised value is an *InvalidAuditFailurePolicyError, never a silent
// fall back to either posture. A typo must fail the boot: resolving "fail_open"
// to fail-closed would deny a fleet the operator meant to keep serving, and
// resolving an unreadable value to fail-open would weaken the posture on a
// typo. Neither is a decision this function is entitled to make.
func ParseAuditFailurePolicy(s string) (AuditFailurePolicy, error) {
	p, ok := auditFailurePolicyNames[strings.ToLower(strings.TrimSpace(s))]
	if !ok {
		return AuditFailClosed, &InvalidAuditFailurePolicyError{Value: s}
	}
	return p, nil
}

// WithAuditFailurePolicy selects the posture for audit-write failure. Not
// applying this option leaves AuditFailClosed, the zero value.
//
// Selecting AuditFailOpen logs a warning HERE, which is the one place the
// operator's choice becomes a running middleware -- once per startup, every
// startup. A security posture that can be weakened silently will be.
func WithAuditFailurePolicy(p AuditFailurePolicy) Option {
	return func(m *AuthMiddleware) {
		m.auditFailure = p
		if p == AuditFailOpen {
			slog.Warn("auth/middleware: audit failure policy is FAIL-OPEN; "+
				"an authorization decision whose audit record cannot be written will be PERMITTED, "+
				"giving up the property that no decision is made unless its audit record persisted",
				"policy", p.String(),
				"metric", "qrdb_auth_audit_write_failures_total{outcome=\"permitted\"}",
			)
		}
	}
}

// auditWriteFailures counts every authorization decision whose audit record
// could not be written, labelled by what the middleware then did about it.
//
// It is THE observability surface for the audit-of-the-audit problem. When the
// audit sink is down, the refusal (or the grant) caused by that outage cannot
// itself be audited -- it would go to the same sink that just failed, and
// retrying it would be exactly the fallback this feature forbids. So the fact
// is carried by a counter that does not depend on the audit plane at all,
// alongside an slog.Error on the operational log stream. A security control
// that fails silently when its recorder is down is the worst case; this is
// what keeps it from being silent.
//
// outcome="denied"    -- fail-closed refused the request.
// outcome="permitted" -- fail-open let it through. A non-zero rate here is the
//
//	exact size of the gap in the audit trail.
var auditWriteFailures = sync.OnceValue(func() *metrics.CounterVec {
	return metrics.NewCounterVec(metrics.CounterOpts{
		Namespace: "qrdb",
		Subsystem: "auth",
		Name:      "audit_write_failures_total",
		Help: "Authorization decisions whose audit record could not be written, " +
			"by what the configured audit failure policy did about it.",
	}, []string{"outcome"})
})

// Metric label values for auditWriteFailures.
const (
	auditOutcomeDenied    = "denied"
	auditOutcomePermitted = "permitted"
)

// auditFailureOutcomes maps the policy onto the label it records, so the
// counter and the branch that increments it cannot drift apart.
var auditFailureOutcomes = map[AuditFailurePolicy]string{
	AuditFailClosed: auditOutcomeDenied,
	AuditFailOpen:   auditOutcomePermitted,
}
