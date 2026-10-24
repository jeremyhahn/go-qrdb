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
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ActionTenantStatusChange records a change of a tenant's status
// (tenant.Status): the event carries the old and the new status.
const ActionTenantStatusChange = "tenant.status_change"

// tenantStatusAuditComponent labels a status-change record that could not be
// written, on qrdb_audit_write_failures_total{component=...}.
const tenantStatusAuditComponent = "tenant-status"

// tenantStatusSettleTimeout bounds the write of a status change's outcome
// record, which is made after the change is stored or refused and so does
// not ride on the request's budget.
const tenantStatusSettleTimeout = 5 * time.Second

// tenantStatusAudit records every change of a tenant's status, whichever
// transport made it: the tenant service reports each change to it
// (tenant.StatusAuditWiring). A change is recorded before it is stored --
// a record that cannot be written refuses the change under
// audit.FailClosed -- and its outcome is recorded once the store answers.
type tenantStatusAudit struct {
	audit  audit.AuditLogger
	policy audit.FailurePolicy
}

// Compile-time check: tenantStatusAudit is the tenant service's auditor.
var _ tenant.StatusChangeAuditor = (*tenantStatusAudit)(nil)

// NewTenantStatusAudit returns the status-change auditor writing to logger
// under policy. A nil logger yields a nil auditor: there is nothing to wire.
func NewTenantStatusAudit(logger audit.AuditLogger, policy audit.FailurePolicy) tenant.StatusChangeAuditor {
	if logger == nil {
		return nil
	}
	return &tenantStatusAudit{audit: logger, policy: policy}
}

// statusChangeEvent renders a status-change event keyed by tenantID -- the
// AFFECTED tenant, not the caller's.
func statusChangeEvent(ctx context.Context, tenantID string, from, to tenant.Status) audit.AuditEvent {
	var actor string
	if id := auth.IdentityFromContext(ctx); id != nil {
		actor = id.Subject
	}
	return audit.AuditEvent{
		Actor:    actor,
		TenantID: tenantID,
		Resource: "tenant",
		Action:   ActionTenantStatusChange,
		Attributes: map[string]any{
			"from": string(from),
			"to":   string(to),
		},
	}
}

// BeginStatusChange implements tenant.StatusChangeAuditor. It writes the
// permitted record, gated by the failure policy, and returns the function
// that writes the outcome record on its own bound.
func (a *tenantStatusAudit) BeginStatusChange(ctx context.Context, tenantID string, from, to tenant.Status) (func(error), error) {
	ev := statusChangeEvent(ctx, tenantID, from, to)
	ev.Outcome = audit.OutcomePermitted
	if err := audit.EmitGated(ctx, a.audit, tenantStatusAuditComponent, a.policy, ev); err != nil {
		return nil, err
	}
	return func(err error) {
		if err == nil {
			ev.Outcome = audit.OutcomeSuccess
		} else {
			ev.Outcome = audit.OutcomeFailure + ":status-change-failed"
		}
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tenantStatusSettleTimeout)
		defer cancel()
		audit.EmitBestEffort(actx, a.audit, tenantStatusAuditComponent, ev)
	}, nil
}
