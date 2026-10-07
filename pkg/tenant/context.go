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

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// contextKey is an unexported type used as the key for tenant ID values
// in context.Context. Using a dedicated type prevents collisions with
// keys from other packages.
type contextKey struct{}

// internalCallKey is an unexported type used to mark a context as
// originating from a trusted internal caller (e.g. first-boot bootstrap).
// External callers cannot construct this key, so they cannot bypass
// reserved-id checks on TenantServicer.CreateTenant.
type internalCallKey struct{}

// WithTenantID returns a new context carrying the given tenant ID.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, contextKey{}, tenantID)
}

// FromContext extracts the tenant ID from the context.
// Returns tenantid.System when no tenant is set or the value is empty —
// callers without an explicit tenant binding execute against the
// structurally privileged system tenant.
func FromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKey{}).(string)
	return tenantid.Normalize(v)
}

// IDFromContext extracts the tenant ID from the context with comma-ok
// semantics. Unlike FromContext, it does not fall back to tenantid.System:
// ok is false when no tenant is bound to the context or the bound value is
// empty, letting callers distinguish an explicit binding from the implicit
// system-tenant default.
func IDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(contextKey{}).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// EffectiveTenant reconciles a request-supplied tenant against the caller's
// authenticated identity tenant and returns the tenant that an IAM operation
// MUST act on. The effective tenant is NEVER sourced from a client-supplied
// request field — it is always the identity tenant — mirroring the
// data-plane rule enforced by the REST scope middleware.
//
// Every identity, the system tenant included, may act only on its own
// tenant: an empty or matching request tenant resolves to the identity
// tenant; a non-empty request tenant that differs from the identity tenant is
// rejected with a *CrossTenantDeniedError (fail closed — never silently
// coerced).
//
// There is no system-tenant lift here (owner ruling, 2026-10-01:
// "tenant.EffectiveTenant stops honouring a requested tenant from a
// system-tenant caller" — docs/multi-tenancy/architecture.md "Strict tenant
// isolation"). A platform-internal flow that legitimately provisions another
// tenant's records (e.g. seeding a new tenant's built-in roles at creation)
// must act on a store already scoped to that tenant directly, never through
// this function with an elevated identity.
//
// op names the attempted operation for the error message (e.g. "create user").
func EffectiveTenant(ctx context.Context, requested, op string) (string, error) {
	identityTenant := FromContext(ctx)
	if requested != "" && requested != identityTenant {
		return "", &CrossTenantDeniedError{
			IdentityTenantID:  identityTenant,
			RequestedTenantID: requested,
			Op:                op,
		}
	}
	return identityTenant, nil
}

// OwnTenant reconciles a request-supplied tenant against the caller's
// authenticated identity tenant with NO exception for the system tenant: a
// system identity is held to the same "act only on your own tenant" rule as
// every other caller.
//
// This is EffectiveTenant's rule minus its system-tenant lift. qrdb's own
// IAM routing uses EffectiveTenant because the system tenant legitimately
// acts across tenants (first-boot bootstrap, platform provisioning,
// AuthorizeTenantTarget's RBAC-gated admin paths). A caller enforcing the
// irreducible "no caller, system included, ever acts on another tenant"
// rule (go-qrdb#587) uses OwnTenant instead; go-quicbaas's audit surfaces
// are one such caller.
//
// Rules:
//   - An empty or matching request tenant resolves to the identity tenant.
//   - A non-empty request tenant that differs from the identity tenant is
//     rejected with a *CrossTenantDeniedError (fail closed), regardless of
//     whether the identity is the system tenant.
//
// op names the attempted operation for the error message (e.g. "list audit
// records").
func OwnTenant(ctx context.Context, requested, op string) (string, error) {
	identityTenant := FromContext(ctx)
	if requested != "" && requested != identityTenant {
		return "", &CrossTenantDeniedError{
			IdentityTenantID:  identityTenant,
			RequestedTenantID: requested,
			Op:                op,
		}
	}
	return identityTenant, nil
}

// WithInternalCall returns a context marked as originating from a trusted
// internal caller. TenantServicer.CreateTenant honours this marker to
// bypass reserved-id rejection for first-boot bootstrap of the system
// tenant. The marker is package-private; external callers cannot fabricate
// it because internalCallKey is unexported.
func WithInternalCall(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalCallKey{}, true)
}

// isInternalCall reports whether the context was marked by WithInternalCall.
func isInternalCall(ctx context.Context) bool {
	v, ok := ctx.Value(internalCallKey{}).(bool)
	return ok && v
}
