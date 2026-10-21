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

package tenant

import (
	"context"
	"sync/atomic"
)

// StatusChangeAuditor records a change of a tenant's status. UpdateTenant
// calls BeginStatusChange once the change is validated and before it is
// stored, and only when the status actually changes: an error refuses the
// update and nothing is stored. The returned settle function is then called
// exactly once with the store's answer (nil on success).
type StatusChangeAuditor interface {
	BeginStatusChange(ctx context.Context, tenantID string, from, to Status) (settle func(error), err error)
}

// StatusAuditWiring is the post-construction wiring surface for the
// status-change auditor, implemented by every TenantServicer this package
// ships. SetStatusChangeAuditor is safe to call while updates are served.
type StatusAuditWiring interface {
	// SetStatusChangeAuditor sets the auditor UpdateTenant reports a status
	// change to. With none wired, a status change is stored unaudited.
	SetStatusChangeAuditor(StatusChangeAuditor)
}

// Compile-time checks: both tenant services carry the auditor seam.
var (
	_ StatusAuditWiring = (*TenantService)(nil)
	_ StatusAuditWiring = (*kvTenantService)(nil)
)

// statusAuditHook holds UpdateTenant's status-change auditor. It is embedded
// in both tenant services so a status change is audited identically with or
// without the seal subsystem. The auditor is held atomically so it can be
// wired or replaced without racing an update in flight.
type statusAuditHook struct {
	auditor atomic.Pointer[statusAuditorRef]
}

// statusAuditorRef boxes a StatusChangeAuditor for atomic storage.
type statusAuditorRef struct{ auditor StatusChangeAuditor }

// SetStatusChangeAuditor implements StatusAuditWiring. A nil auditor unwires
// it.
func (h *statusAuditHook) SetStatusChangeAuditor(a StatusChangeAuditor) {
	if a == nil {
		h.auditor.Store(nil)
		return
	}
	h.auditor.Store(&statusAuditorRef{auditor: a})
}

// WithStatusChangeAuditor injects the auditor UpdateTenant reports a status
// change to. See StatusAuditWiring.SetStatusChangeAuditor.
func WithStatusChangeAuditor(a StatusChangeAuditor) TenantServiceOption {
	return func(s *TenantService) { s.SetStatusChangeAuditor(a) }
}

// noSettle is the settle function of an update that audits nothing.
func noSettle(error) {}

// beginStatusChange reports a change of tenantID's status from from to to to
// the wired auditor. An update that leaves the status as it was, or a service
// with no auditor, audits nothing.
func (h *statusAuditHook) beginStatusChange(ctx context.Context, tenantID string, from, to Status) (func(error), error) {
	ref := h.auditor.Load()
	if ref == nil || from == to {
		return noSettle, nil
	}
	settle, err := ref.auditor.BeginStatusChange(ctx, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	if settle == nil {
		return noSettle, nil
	}
	return settle, nil
}
