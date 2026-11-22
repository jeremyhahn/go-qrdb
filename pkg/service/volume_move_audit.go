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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// ActionVolumePoolMoveStart records the start of a tenant move between
// volume pools: the event carries the destination pool and decision Q9's
// allow_unencrypted_destination flag, and its outcome the source pool.
const ActionVolumePoolMoveStart = "volume_pool.move_start"

// volumeMoveAuditComponent labels a move-start record that could not be
// written, on qrdb_audit_write_failures_total{component=...}.
const volumeMoveAuditComponent = "volume-move"

// volumeMoveSettleTimeout bounds the write of a move start's outcome
// record, which runs on a context detached from the request's cancellation.
const volumeMoveSettleTimeout = 5 * time.Second

// volumeMoveAudit implements volume.MoveStartAuditor over audit.EmitGated,
// as tenantStatusAudit does for a status change.
type volumeMoveAudit struct {
	audit  audit.AuditLogger
	policy audit.FailurePolicy
}

// NewVolumeMoveAudit returns the volume.MoveStartAuditor a composition sets
// with volume.Catalog.SetMoveStartAuditor: a permitted record, gated by
// policy (audit.FailClosed refuses the start when it cannot be written),
// before the move is admitted, and a best-effort outcome record after. A nil
// logger yields nil: no auditor, as with no audit sink configured.
func NewVolumeMoveAudit(logger audit.AuditLogger, policy audit.FailurePolicy) volume.MoveStartAuditor {
	if logger == nil {
		return nil
	}
	return &volumeMoveAudit{audit: logger, policy: policy}
}

// moveStartEvent builds the record of req's start, without an outcome.
func moveStartEvent(ctx context.Context, req volume.MoveTenantRequest) audit.AuditEvent {
	var actor string
	if id := auth.IdentityFromContext(ctx); id != nil {
		actor = id.Subject
	}
	return audit.AuditEvent{
		Actor:    actor,
		TenantID: req.TenantID,
		Resource: "volume_pool",
		Action:   ActionVolumePoolMoveStart,
		Attributes: map[string]any{
			"to_pool":                       req.ToPool,
			"allow_unencrypted_destination": req.AllowUnencryptedDestination,
		},
	}
}

// BeginMoveStart implements volume.MoveStartAuditor.
func (a *volumeMoveAudit) BeginMoveStart(ctx context.Context, req volume.MoveTenantRequest) (func(*volume.Move, error), error) {
	ev := moveStartEvent(ctx, req)
	ev.Outcome = audit.OutcomePermitted
	if err := audit.EmitGated(ctx, a.audit, volumeMoveAuditComponent, a.policy, ev); err != nil {
		return nil, err
	}
	return func(m *volume.Move, err error) {
		out := moveStartEvent(ctx, req)
		if m != nil {
			out.Attributes["from_pool"] = m.FromPool
		}
		if err == nil {
			out.Outcome = audit.OutcomeSuccess
		} else {
			out.Outcome = audit.OutcomeFailure + ":move-start-failed"
		}
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), volumeMoveSettleTimeout)
		defer cancel()
		audit.EmitBestEffort(actx, a.audit, volumeMoveAuditComponent, out)
	}, nil
}
