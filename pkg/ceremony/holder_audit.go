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

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The share-holder events every holder scope records on the compliance audit
// trail (HolderDeps.Audit). Each names the owning tenant (TenantID), the scope
// and the holder; none carries a share, an envelope, a PIN, a factor response
// or any key.
const (
	// HolderAuditResource is the Resource of every share-holder event.
	HolderAuditResource = "share-holder"

	// HolderAuditComponent labels a share-holder record that could not be
	// written, on qrdb_audit_write_failures_total{component=...}.
	HolderAuditComponent = "share-holder"

	// ActionHolderRegister records a holder enrolment.
	ActionHolderRegister = "holder-register"

	// ActionHolderClaim records a holder's share-envelope claim.
	ActionHolderClaim = "holder-claim"

	// ActionHolderQuorumSubmit records a holder's authentication for a
	// share submission to a quorum operation (attribute "op").
	ActionHolderQuorumSubmit = "holder-quorum-submit"

	// ActionHolderRekeyStart records the opening of a rekey round, a
	// revocation included (attribute "holder" names the holder removed).
	ActionHolderRekeyStart = "holder-rekey-start"

	// ActionHolderRevoked records a holder's removal, once the rekey that
	// revoked it is durable.
	ActionHolderRevoked = "holder-revoked"
)

// Actors recorded when no authenticated identity performed the operation.
const (
	// holderActorAnonymous is a sealed-window caller that has not proved
	// itself to be any holder.
	holderActorAnonymous = "anonymous"

	// holderActorQuorum is the quorum of holders whose shares completed a
	// rekey: no single caller performed the revocation it commits.
	holderActorQuorum = "share-holder-quorum"
)

// holderEvent is one share-holder event, before the scope's identity is
// added to it.
type holderEvent struct {
	action string
	// holder is the holder the event is about.
	holder string
	// actor is who performed the operation when the request context carries
	// no authenticated identity: the enroller, a holder that proved itself,
	// or the quorum. Empty records holderActorAnonymous.
	actor string
	// attrs are event-specific attributes. They carry names and counts only.
	attrs map[string]any
	// err is the operation's failure; nil records success.
	err error
	// denied records a failure as a refused authentication rather than a
	// failed operation.
	denied bool
}

// record writes ev on the scope's compliance trail, keyed by the scope's
// owning tenant. A holder operation has already happened -- or been refused --
// by the time it is recorded, so a sink that refuses the record is counted
// and logged (audit.EmitBestEffort), never returned as the operation's
// failure. Without a sink it records nothing.
func (h *holderScope) record(ctx context.Context, d *HolderDeps, ev holderEvent) {
	if d == nil || d.Audit == nil {
		return
	}
	attrs := make(map[string]any, len(ev.attrs)+3)
	for k, v := range ev.attrs {
		attrs[k] = v
	}
	attrs["scope"] = h.scope.String()
	attrs["domain"] = string(h.domain)
	if ev.holder != "" {
		attrs["holder"] = ev.holder
	}
	audit.EmitBestEffort(ctx, d.Audit, HolderAuditComponent, audit.AuditEvent{
		Actor:      holderActor(ctx, ev.actor),
		TenantID:   tenantid.Normalize(h.tenantID),
		Resource:   HolderAuditResource,
		Action:     ev.action,
		Outcome:    holderOutcome(ev.err, ev.denied),
		Attributes: attrs,
	})
}

// holderActor names who performed a holder operation: the authenticated
// identity on ctx, else fallback, else holderActorAnonymous.
func holderActor(ctx context.Context, fallback string) string {
	if id := auth.IdentityFromContext(ctx); id != nil && id.Subject != "" {
		return id.Subject
	}
	if fallback != "" {
		return fallback
	}
	return holderActorAnonymous
}

// holderOutcome renders err as an audit outcome.
func holderOutcome(err error, denied bool) string {
	switch {
	case err == nil:
		return audit.OutcomeSuccess
	case denied:
		return audit.OutcomeDenied + ":" + err.Error()
	default:
		return audit.OutcomeFailure + ":" + err.Error()
	}
}

// recordRekeyStart records the opening of a rekey round on the scope's
// domain with params, and its outcome.
func (h *holderScope) recordRekeyStart(ctx context.Context, d *HolderDeps, params seal.RekeyParams, err error) {
	attrs := map[string]any{"op": string(seal.OpRekey), "threshold": params.Threshold}
	if params.Policy != "" {
		attrs["policy"] = string(params.Policy)
	}
	if len(params.Remove) == 0 {
		h.record(ctx, d, holderEvent{action: ActionHolderRekeyStart, attrs: attrs, err: err})
		return
	}
	for _, name := range params.Remove {
		h.record(ctx, d, holderEvent{action: ActionHolderRekeyStart, holder: name, attrs: attrs, err: err})
	}
}
