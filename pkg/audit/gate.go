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

package audit

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// FailurePolicy selects what a GATING call site does when the audit logger
// cannot record the event that gates it (go-qrdb#335, go-qrdb#445).
//
// It is a POSTURE, not a fallback. The call site does exactly what it is
// configured to do, once, with no retry, no degradation ladder and no
// automatic switch between the two values. An operator who wants the
// availability reading states it; nothing in the code decides it for them at
// runtime.
//
// IT LIVES HERE, IN pkg/audit, RATHER THAN IN pkg/auth/middleware, because
// there is exactly ONE posture across the stack and it has to be reachable
// from both sides of an import edge. The authorization middleware
// (pkg/auth/middleware) and the security-officer factor (pkg/auth) are two
// distinct assertions guarding the same request, and pkg/auth/middleware
// imports pkg/auth -- so a policy type owned by the middleware could never
// reach the SO path without a cycle. go-qrdb#445 ruled that both fail closed
// on ONE operator lever (auth.audit_failure_policy); this is where that one
// value is spelled.
type FailurePolicy uint8

const (
	// FailClosed refuses the operation when its audit record could not be
	// written. It is the ZERO VALUE on purpose: a call site built from a
	// struct literal that never mentions the field, by a test, an embedder or
	// a future constructor nobody remembered to update, is fail-closed. A
	// security default that has to be assigned is a security default that will
	// eventually not be.
	//
	// The reasoning for it being the default: an unrecorded GRANT -- an RBAC
	// authorization, an SO-PIN verification -- is precisely the event audit
	// exists to prevent. A system that authorizes without recording has lost
	// the property, and losing it silently is worse than refusing.
	FailClosed FailurePolicy = iota

	// FailOpen permits the operation when its audit record could not be
	// written, counting the event on the gate's write-failure counter with
	// outcome="permitted".
	//
	// It gives up the property "no gating decision is made unless its audit
	// record persisted". It exists because fail-closed has a real availability
	// cliff -- a full disk denies every authorized request -- and an operator
	// facing that needs a documented lever that is not "patch the binary".
	// Selecting it logs a warning at every startup naming the property given
	// up.
	FailOpen
)

// String returns the configuration spelling of the policy.
func (p FailurePolicy) String() string {
	if p == FailOpen {
		return "fail-open"
	}
	return "fail-closed"
}

// failurePolicyNames maps the accepted configuration spellings onto the
// policy. Empty maps to FailClosed because an omitted key must resolve to the
// secure posture through the SAME table every other spelling goes through,
// rather than through a separate empty-string branch that a later edit could
// forget.
var failurePolicyNames = map[string]FailurePolicy{
	"":            FailClosed,
	"fail-closed": FailClosed,
	"failclosed":  FailClosed,
	"closed":      FailClosed,
	"fail-open":   FailOpen,
	"failopen":    FailOpen,
	"open":        FailOpen,
}

// ParseFailurePolicy resolves an operator's configured spelling.
//
// An unrecognised value is an *InvalidFailurePolicyError, never a silent fall
// back to either posture. A typo must fail the boot: resolving "fail_open" to
// fail-closed would deny a fleet the operator meant to keep serving, and
// resolving an unreadable value to fail-open would weaken the posture on a
// typo. Neither is a decision this function is entitled to make.
func ParseFailurePolicy(s string) (FailurePolicy, error) {
	p, ok := failurePolicyNames[strings.ToLower(strings.TrimSpace(s))]
	if !ok {
		return FailClosed, &InvalidFailurePolicyError{Value: s}
	}
	return p, nil
}

// InvalidFailurePolicyError is returned by ParseFailurePolicy when the
// operator's configured auth.audit_failure_policy is not a value this build
// understands.
//
// It fails the boot rather than resolving to either posture. Guessing
// fail-closed on a typo denies a fleet the operator meant to keep serving;
// guessing fail-open weakens the security posture on a typo. A daemon that
// cannot tell which posture was asked for must not choose one.
type InvalidFailurePolicyError struct {
	Value string
}

// Error returns a human-readable description naming the rejected value and
// the accepted ones.
func (e *InvalidFailurePolicyError) Error() string {
	return "audit: invalid audit failure policy " + strconv.Quote(e.Value) +
		" (want \"fail-closed\" or \"fail-open\")"
}

// GateError is what a gating call site returns when the event that gates the
// operation could not be recorded and the policy is FailClosed -- the
// default. The operation is REFUSED.
//
// ONE TYPE FOR BOTH GATES. The authorization middleware and the SO factor are
// two distinct assertions about the same request, and before go-qrdb#445 only
// one of them could produce this error at all. Giving each its own type would
// have meant two classification rules, two transport mappings and two things
// to keep in step; the Component field carries the difference that actually
// matters to a reader, which is WHICH trail now has the gap.
//
// It wraps the sink's own error -- typically a *WriteError naming the backend
// -- so errors.As reaches it and an operator learns which destination failed,
// not merely that one did.
//
// On the wire it is a retryable server-side condition, not a denial: the
// credential is valid and the grant (or the PIN) may well have been valid too.
// REST answers 503 and gRPC codes.Unavailable, matching StoreUnavailableError,
// because the honest instruction to the caller is "retry", never "you are not
// permitted". pkg/auth/middleware.ClassifyAuthError is where that mapping
// lives for every transport at once.
type GateError struct {
	// Component names the subsystem whose gating record was lost, using the
	// same vocabulary as EmitBestEffort's component label: "authorize",
	// "so-auth", "so-dual-factor".
	Component string

	// Err is the sink's own failure.
	Err error
}

// Error returns a human-readable description of the refusal.
//
// The phrase "audit log failed" is load-bearing: it is what reaches a REST
// caller in the error body, and operators and tests alike match on it.
func (e *GateError) Error() string {
	if e.Component == "" {
		return "audit: audit log failed, refusing operation: " + e.Err.Error()
	}
	return "audit: " + e.Component + ": audit log failed, refusing operation: " + e.Err.Error()
}

// Unwrap returns the underlying sink error.
func (e *GateError) Unwrap() error { return e.Err }

// gateWriteFailures counts every GATING audit record that could not be
// written, labelled by the subsystem whose trail now has the gap and by what
// the configured policy then did about it.
//
// It is THE observability surface for the audit-of-the-audit problem at every
// gate other than the authorization middleware, which keeps its own
// longer-standing qrdb_auth_audit_write_failures_total. When the audit sink is
// down, the refusal (or the grant) caused by that outage cannot itself be
// audited -- it would go to the same sink that just failed, and retrying it
// would be exactly the fallback this feature forbids. So the fact is carried
// by a counter that does not depend on the audit plane at all, alongside an
// slog.Error on the operational log stream. A security control that fails
// silently when its recorder is down is the worst case; this is what keeps it
// from being silent.
//
// outcome="denied"    -- fail-closed refused the operation.
// outcome="permitted" -- fail-open let it through. A non-zero rate here is the
//
//	exact size of the gap in that component's trail.
var gateWriteFailures = sync.OnceValue(func() *metrics.CounterVec {
	return metrics.NewCounterVec(metrics.CounterOpts{
		Namespace: "qrdb",
		Subsystem: "audit",
		Name:      "gate_failures_total",
		Help: "Gating audit records that could not be written, by the subsystem " +
			"whose trail is therefore incomplete and by what the configured " +
			"audit failure policy did about it.",
	}, []string{"component", "outcome"})
})

// Metric label values for gateWriteFailures.
const (
	gateOutcomeDenied    = "denied"
	gateOutcomePermitted = "permitted"
)

// gateOutcomes maps the policy onto the label it records, so the counter and
// the branch that increments it cannot drift apart.
var gateOutcomes = map[FailurePolicy]string{
	FailClosed: gateOutcomeDenied,
	FailOpen:   gateOutcomePermitted,
}

// EmitGated records ev on sink and REPORTS, rather than logs, a write failure
// when policy is FailClosed. A nil sink is a no-op: nothing could fail, so
// nothing is refused.
//
// It is the counterpart to EmitBestEffort and the single home for the "this
// record GATES the operation" decision, made visibly, at the call site, in a
// form a reader can grep for. Emit returning an error (go-qrdb#335) means
// every caller now states what it does with a failed audit write; the two
// honest answers are:
//
//   - PROPAGATE, when the record is part of the decision being made and the
//     operation it gates has NOT yet happened. The authorization middleware is
//     the canonical one, and the SO-PIN verification is the other
//     (go-qrdb#445): a second factor that passes unrecorded is precisely the
//     event an audit trail exists to capture. That is this function.
//   - LOG AND CONTINUE, when the operation has ALREADY COMMITTED and this is
//     its record. A user was created, a custodian enrolled, an SO PIN
//     committed to the replicated store -- there is no decision left to refuse
//     and no rollback the emitter could perform, so returning the error would
//     mislabel a completed operation as failed. That is EmitBestEffort.
//
// THE REFUSAL IS UNIFORM within a gate. It does not matter whether the event
// being recorded was a success or a refusal: "we answer only what we could
// record" is a property an auditor can state and a reader can check, while "we
// answer only what we could record, except when we were going to refuse
// anyway" is two rules that will drift. The dangerous case is the unrecorded
// success, and carving out the others buys nothing -- every one of them
// refuses the caller either way, and under fail-closed the caller is told to
// retry rather than told its credential was bad, which is the truth.
//
// THE REFUSAL IS NOT ITSELF AUDITED. The only sink is the one that just
// failed; writing to it again would be a retry on a sink that has already said
// no, which is the fallback this feature forbids. The event rides
// qrdb_audit_gate_failures_total and an ERROR line on the operational log
// stream instead -- two planes that do not depend on the audit plane. During
// an audit outage the trail therefore has a GAP BY CONSTRUCTION; what the
// counter guarantees is that the gap is visible and measurable rather than
// silent, and the hash chain guarantees it cannot be back-filled with forged
// records.
func EmitGated(ctx context.Context, sink AuditLogger, component string, policy FailurePolicy, ev AuditEvent) error {
	if sink == nil {
		return nil
	}
	err := sink.Emit(ctx, ev)
	if err == nil {
		return nil
	}

	gateWriteFailures().With(component, gateOutcomes[policy]).Inc()
	slog.Error("audit: gating record not written",
		"component", component,
		"resource", ev.Resource,
		"action", ev.Action,
		"outcome", ev.Outcome,
		"policy", policy.String(),
		"error", err,
	)

	if policy == FailOpen {
		return nil
	}
	return &GateError{Component: component, Err: err}
}
