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
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// WithAuditFailurePolicy selects the posture for audit-write failure. Not
// applying this option leaves audit.FailClosed, the zero value.
//
// THE POSTURE ITSELF IS audit.FailurePolicy, not a middleware type. It was one
// until go-qrdb#445, and being one is what kept the operator's single lever
// from reaching the security-officer factor: pkg/auth/middleware imports
// pkg/auth, so a policy owned here could never be read there. One posture,
// one type, one lever, both gates -- see audit.FailurePolicy.
//
// Selecting audit.FailOpen logs a warning HERE, which is the one place the
// operator's choice becomes a running middleware -- once per startup, every
// startup. A security posture that can be weakened silently will be.
func WithAuditFailurePolicy(p audit.FailurePolicy) Option {
	return func(m *AuthMiddleware) {
		m.auditFailure = p
		if p == audit.FailOpen {
			slog.Warn("auth/middleware: audit failure policy is FAIL-OPEN; "+
				"an authorization decision or SO-PIN verification whose audit record cannot be written will be PERMITTED, "+
				"giving up the property that no decision is made unless its audit record persisted",
				"policy", p.String(),
				"metrics", "qrdb_auth_audit_write_failures_total{outcome=\"permitted\"}, qrdb_audit_gate_failures_total{outcome=\"permitted\"}",
			)
		}
	}
}

// auditWriteFailures counts every authorization decision whose audit record
// could not be written, labelled by what the middleware then did about it.
//
// It is THE observability surface for the audit-of-the-audit problem on the
// authorization gate. When the audit sink is down, the refusal (or the grant)
// caused by that outage cannot itself be audited -- it would go to the same
// sink that just failed, and retrying it would be exactly the fallback this
// feature forbids. So the fact is carried by a counter that does not depend on
// the audit plane at all, alongside an slog.Error on the operational log
// stream. A security control that fails silently when its recorder is down is
// the worst case; this is what keeps it from being silent.
//
// Every OTHER gate -- the SO factor, and anything added later -- counts on
// qrdb_audit_gate_failures_total{component,outcome} instead (audit.EmitGated).
// The two are deliberately separate rather than one relabelled counter: this
// one shipped with go-qrdb#335 and an operator's alert on it must keep meaning
// exactly what it meant, which folding a new component into it silently would
// not.
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
var auditFailureOutcomes = map[audit.FailurePolicy]string{
	audit.FailClosed: auditOutcomeDenied,
	audit.FailOpen:   auditOutcomePermitted,
}

// authorizeComponent is the component name the authorization gate stamps on
// the *audit.GateError it returns, matching the vocabulary EmitBestEffort and
// EmitGated use at every other site.
const authorizeComponent = "authorize"

// soDualFactorComponent is the component name the SO dual-factor gate stamps
// on its records and on the *audit.GateError it returns.
const soDualFactorComponent = "so-dual-factor"
