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

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// CallerTenant returns the tenant the caller in ctx belongs to: the
// authenticated identity's tenant when ctx carries an identity, and otherwise
// the tenant bound to ctx (FromContext). The result is normalized, so a
// caller with no tenant binding is the system tenant.
//
// The identity wins because it is what the caller IS. On the network
// transports the authentication middleware binds both from the same value, so
// they agree; the embedded transport has no middleware, and a context there
// may carry an identity without a bound tenant.
func CallerTenant(ctx context.Context) string {
	if id := auth.IdentityFromContext(ctx); id != nil {
		return Normalize(id.TenantID)
	}
	return FromContext(ctx)
}

// Confine returns the tenant an operation must act on when the request may
// name one: the caller's own tenant (CallerTenant), always. An empty
// requested tenant means the caller's own. A requested tenant that is not the
// caller's is refused with *CrossTenantDeniedError, for EVERY caller.
//
// Unlike EffectiveTenant there is no system-tenant lift: the system tenant is
// confined to its own tenant exactly as every other tenant is. op names the
// attempted operation for the error message (e.g. "query audit events").
func Confine(ctx context.Context, requested, op string) (string, error) {
	caller := CallerTenant(ctx)
	if requested != "" && Normalize(requested) != caller {
		return "", &CrossTenantDeniedError{
			IdentityTenantID:  caller,
			RequestedTenantID: requested,
			Op:                op,
		}
	}
	return caller, nil
}

// ConfineAuditFilter confines an audit query to the caller's own tenant. It
// is the one tenancy rule every transport applies to an audit query before
// the query reaches a logger, because the loggers are node-local and hold
// every tenant's events.
//
// The returned filter always names the caller's tenant, so an empty request
// filter reads the caller's own events rather than everyone's. A filter
// naming another tenant is refused with *CrossTenantDeniedError. A
// system-tenant caller additionally sees the events that carry no tenant at
// all (audit.Filter.Untenanted): those are platform events, and the platform
// is the system tenant. Every other field of f -- time range, actor, cursor,
// limit -- narrows the result further and cannot widen it, because a logger
// applies the tenant match to every event before it paginates.
func ConfineAuditFilter(ctx context.Context, f audit.Filter) (audit.Filter, error) {
	caller, err := Confine(ctx, f.TenantID, "query audit events")
	if err != nil {
		return audit.Filter{}, err
	}
	f.TenantID = caller
	f.Untenanted = IsSystemTenant(caller)
	return f, nil
}
