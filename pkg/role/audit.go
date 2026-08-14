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

package role

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// Option configures a kvRoleStore at construction time.
type Option func(*kvRoleStore)

// WithAuditLogger attaches a compliance-grade audit logger so role
// mutations (create, update, delete) leave an audit trail. A nil logger
// disables emission, so the option is safe to apply unconditionally.
func WithAuditLogger(logger audit.AuditLogger) Option {
	return func(s *kvRoleStore) {
		s.audit = logger
	}
}

// emitAudit records a successful role mutation. It captures the acting
// identity (Subject + TenantID) from the request context and is a no-op
// when no audit logger is configured. Emission is best-effort per the
// AuditLogger contract and never affects the mutation outcome.
func (s *kvRoleStore) emitAudit(ctx context.Context, action, resource string, attrs map[string]any) {
	if s.audit == nil {
		return
	}
	var actor, tenantID string
	if id := auth.IdentityFromContext(ctx); id != nil {
		actor = id.Subject
		tenantID = id.TenantID
	}
	s.audit.Emit(ctx, audit.AuditEvent{
		Actor:      actor,
		TenantID:   tenantID,
		Resource:   resource,
		Action:     action,
		Outcome:    audit.OutcomeSuccess,
		Attributes: attrs,
	})
}
