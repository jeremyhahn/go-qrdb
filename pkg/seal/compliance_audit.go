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

package seal

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// BarrierAuditResource is the Resource of every barrier event on the
// compliance trail.
const BarrierAuditResource = "barrier"

// SystemBarrierAuditName names the system barrier on the compliance trail.
const SystemBarrierAuditName = "system"

// TenantBarrierAuditName names tenantID's barrier on the compliance trail.
func TenantBarrierAuditName(tenantID string) string { return "tenant:" + tenantID }

// DatabaseBarrierAuditName names the barrier of database in tenantID on the
// compliance trail.
func DatabaseBarrierAuditName(tenantID, database string) string {
	return "database:" + tenantid.Normalize(tenantID) + "/" + database
}

// WithAuditTrail records the events of every tenant barrier the registry
// builds on sink, the node's compliance trail, keyed by the tenant
// (NewComplianceAuditLogger). A registration that supplies its own
// BarrierConfig.AuditLogger keeps it. A nil sink is a no-op.
func WithAuditTrail(sink audit.AuditLogger) Option {
	return func(r *BarrierRegistry) {
		r.poolMu.Lock()
		defer r.poolMu.Unlock()
		if sink != nil {
			r.auditTrail = sink
		}
	}
}

// BarrierAuditComponent labels a barrier record that could not be written,
// on qrdb_audit_write_failures_total{component=...}.
const BarrierAuditComponent = "barrier"

// barrierAuditAttributes is every barrier event detail the compliance trail
// carries. Each is a name, a count, a strategy or a thumbprint -- public
// material only. Any other detail is dropped, so a detail added to an
// emission site later reaches the trail only once it is reviewed onto this
// list. The failure text of a refused operation ("error", "reason") is never
// recorded: it is free text from a sealing strategy -- an injected go-xkms
// KMS, HSM or TPM strategy's among them -- and the trail is tenant-readable,
// hash-chained and cannot be redacted. The barrier's operational log carries
// it.
var barrierAuditAttributes = map[string]bool{
	"domain": true, "op": true, "strategy": true, "threshold": true,
	"removed": true, "from": true, "to": true, "rolled_back": true,
	"submitted": true, "required": true,
	// The holder registry's record conflict (holderScope.evictConflict).
	"scope": true, "username": true, "cached_cert": true,
}

// complianceAuditLogger records a barrier's events on the node's compliance
// trail, keyed by the tenant that owns the barrier.
type complianceAuditLogger struct {
	sink     audit.AuditLogger
	tenantID string
	barrier  string
}

var _ AuditLogger = (*complianceAuditLogger)(nil)

// NewComplianceAuditLogger returns the AuditLogger that records the events of
// one barrier -- initialize, unseal, seal, quorum-complete, distribute and
// the rest -- on sink, the node's one compliance trail, keyed by tenantID,
// the tenant that owns the barrier, and naming the barrier (attribute
// "barrier": "system", "tenant:<id>", "database:<tenant>/<name>"). Only the
// details on an allow-list reach the trail; none carries key material. The
// records are post-hoc -- the operation has happened, or been refused -- so
// a sink that refuses one is counted and logged (audit.EmitBestEffort),
// never returned to the barrier. Close does not close sink: the composer
// that opened the trail owns it. A nil sink returns nil, recording nothing.
func NewComplianceAuditLogger(sink audit.AuditLogger, tenantID, barrier string) AuditLogger {
	if sink == nil {
		return nil
	}
	return &complianceAuditLogger{sink: sink, tenantID: tenantid.Normalize(tenantID), barrier: barrier}
}

// Log records ev on the compliance trail.
func (l *complianceAuditLogger) Log(ctx context.Context, ev *AuditEvent) error {
	if ev == nil {
		return nil
	}
	attrs := map[string]any{"barrier": l.barrier}
	for k, v := range ev.Details {
		if barrierAuditAttributes[k] {
			attrs[k] = v
		}
	}
	audit.EmitBestEffort(ctx, l.sink, BarrierAuditComponent, audit.AuditEvent{
		Timestamp:  ev.Timestamp,
		Actor:      ev.Subject,
		TenantID:   l.tenantID,
		Resource:   BarrierAuditResource,
		Action:     ev.Action,
		Outcome:    barrierOutcome(ev),
		Attributes: attrs,
	})
	return nil
}

// Close is a no-op: the trail belongs to the composer that opened it.
func (l *complianceAuditLogger) Close() error { return nil }

// barrierOutcome renders a barrier event's outcome in the compliance
// vocabulary: "allow" is success; a refusal that failed with an error is
// failure, any other refusal denied. Neither carries the event's text.
func barrierOutcome(ev *AuditEvent) string {
	if ev.Outcome == "allow" {
		return audit.OutcomeSuccess
	}
	if _, failed := ev.Details["error"]; failed {
		return audit.OutcomeFailure
	}
	return audit.OutcomeDenied
}
