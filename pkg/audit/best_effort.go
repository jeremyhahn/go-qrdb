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
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// bestEffortWriteFailures counts audit records that were NOT written at a
// post-hoc call site, labelled by the subsystem whose trail now has the gap.
//
// It is the counterpart to qrdb_auth_audit_write_failures_total, and it exists
// for the same reason: "the counter makes the gap measurable, and a non-zero
// rate is the exact size of the gap" is as true of a custodian enrolment as of
// an authorization decision. Without it these fourteen call sites had an
// slog.Error and nothing else, and NOBODY CAN ALERT ON AN slog.Error -- the
// asymmetry would have defeated this feature's own argument at every site that
// is not the middleware.
//
// component is the label because it is the one thing that tells an operator
// WHICH trail is incomplete. A single undifferentiated total says the audit
// plane is unwell; qrdb_audit_write_failures_total{component="share-holder"} says
// the share-holder records for this window are missing.
var bestEffortWriteFailures = sync.OnceValue(func() *metrics.CounterVec {
	return metrics.NewCounterVec(metrics.CounterOpts{
		Namespace: "qrdb",
		Subsystem: "audit",
		Name:      "write_failures_total",
		Help: "Audit records that could not be written at a post-hoc call site, " +
			"by the subsystem whose trail is therefore incomplete.",
	}, []string{"component"})
})

// EmitBestEffort records ev on sink and LOGS, rather than returns, a write
// failure. A nil sink is a no-op.
//
// It is the single home for the "this record is post-hoc" decision, and it
// exists so that decision is made VISIBLY, at the call site, in a form a
// reader can grep for. Emit returning an error (go-qrdb#335) means every
// caller now states what it does with a failed audit write; the two honest
// answers are:
//
//   - PROPAGATE, when the record is part of the decision being made. The
//     authorization middleware is the canonical one: it refuses a request
//     whose decision it could not record, because an unrecorded grant is the
//     event audit exists to prevent.
//   - LOG AND CONTINUE, when the operation has ALREADY HAPPENED and this is
//     its record. A user was created, a custodian enrolled, a credential
//     rotated -- there is no decision left to refuse and no rollback the
//     emitter could perform, so returning the error would mislabel a
//     completed operation as failed. That is this function.
//
// What it is NOT is a way to avoid choosing. A caller reaching for it on a
// path where the record IS part of the decision has moved the compliance
// property back to where go-qrdb#335 found it. Refusals count as post-hoc:
// the request is already being refused, and a failed audit write cannot make
// it more refused.
//
// component names the subsystem ("user", "role", "credentials", "so-auth",
// "share-holder", "barrier", "cert", "cert-enrollment", "backup",
// "invitation", "tenant-onboarding", "tenant-status", "user_authfactor",
// "trust-anchor") and appears BOTH on the
// operational log line and as the label on
// qrdb_audit_write_failures_total{component=...}, so an operator reading the
// stream -- or alerting on the counter, which is the part a human is not
// watching at 3am -- can see which trails have gaps and how large.
func EmitBestEffort(ctx context.Context, sink AuditLogger, component string, ev AuditEvent) {
	if sink == nil {
		return
	}
	if err := sink.Emit(ctx, ev); err != nil {
		bestEffortWriteFailures().With(component).Inc()
		slog.Error("audit: record not written",
			"component", component,
			"resource", ev.Resource,
			"action", ev.Action,
			"outcome", ev.Outcome,
			"error", err,
		)
	}
}
