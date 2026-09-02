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

import "context"

// SystemTenantID is the reserved identifier for the platform's structurally
// privileged tenant. Membership in the system tenant grants cross-tenant
// scope to whatever roles a user holds inside it (see the system-tenant
// rule in pkg/auth/rbac). It is also the implicit fallback returned by
// FromContext when no tenant is present on a context — code paths that
// run without an explicit tenant binding (bootstrap, daemon-internal
// maintenance) execute against the system tenant.
//
// SystemTenantID is reserved: TenantServicer.CreateTenant rejects this id
// (and a small set of other reserved patterns) for all external callers.
// Only first-boot bootstrap may create the system tenant, by passing
// WithInternalCall(ctx) to bypass the reservation check.
const SystemTenantID = "system"

// IsSystemTenant reports whether id denotes the structurally privileged
// system tenant. It returns true for both SystemTenantID and the empty
// string: per the documented rule, an identity or context with no tenant
// binding resolves to the system tenant (see FromContext), so "no tenant"
// and "system tenant" are the same scope for authorization purposes. This
// is the single canonical predicate for that question — callers must use
// it instead of re-deriving the `x == "" || x == SystemTenantID` (or bare
// `x == SystemTenantID`) comparison at each call site.
//
// The empty case is a PRECONDITION, not a convenience: on a normalized path
// the predicate must never observe "". Normalize is the single fold that
// establishes it, and every boundary that binds a tenant onto a context, a
// session or an identity calls it — see FromContext,
// auth/middleware.finalizeIdentity and sql/provider.QRDBSession.TenantID.
// Before #188 that precondition was documented here and enforced nowhere:
// auth/middleware folded the CONTEXT tenant but left identity.TenantID raw,
// and rbac.IsSystemTenant reads exactly that raw field, so an identity
// authenticated without a tenant binding reached this predicate unnormalized.
//
// The empty branch is retained rather than removed because unscoped STORE
// callers depend on it (identity_index, store_resolver, kv_service and
// database/manager_tenant all ask "is this the unscoped keyspace?"), and
// because folding is what the fix does — "" is turned into SystemTenantID at
// the boundary, not made to mean something new here. It follows that this
// predicate cannot distinguish "unknown" from "system"; making an absent
// tenant fail CLOSED is a separate, deliberate decision that belongs to a
// distinct authorization-facing predicate (see #188, options 1 and 3).
func IsSystemTenant(id string) bool {
	return id == "" || id == SystemTenantID
}

// Normalize folds a tenant identifier onto its canonical form: the empty
// string — "no tenant binding" — becomes SystemTenantID, and every other
// value is returned unchanged.
//
// This is the enforcement half of IsSystemTenant's precondition. Boundaries
// that bind a tenant MUST call it rather than open-coding
// `if id == "" { id = SystemTenantID }`; five copies of that fold existed
// before #188 and the one that was missing — identity.TenantID in
// auth/middleware — is what let an unnormalized "" reach an authorization
// decision.
//
// Normalize does not decide privilege. It records that a caller arrived
// without a tenant and states the tenant that decision has always mapped to,
// in one place, so a future change to that mapping has one place to change.
func Normalize(id string) string {
	if id == "" {
		return SystemTenantID
	}
	return id
}

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
// Returns SystemTenantID when no tenant is set or the value is empty —
// callers without an explicit tenant binding execute against the
// structurally privileged system tenant.
func FromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKey{}).(string)
	return Normalize(v)
}

// IDFromContext extracts the tenant ID from the context with comma-ok
// semantics. Unlike FromContext, it does not fall back to SystemTenantID:
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
// MUST act on. For a normal tenant caller the effective tenant is NEVER
// sourced from a client-supplied request field — it is the identity tenant —
// mirroring the data-plane rule enforced by the REST scope middleware.
//
// Rules:
//   - System-tenant callers (identity == SystemTenantID) may target any
//     tenant: the request-supplied tenant is honoured, defaulting to the
//     system tenant when empty. This is the legitimate cross-tenant admin
//     path (first-boot bootstrap, platform provisioning).
//   - Normal tenant callers may act only on their own tenant. An empty or
//     matching request tenant resolves to the identity tenant; a non-empty
//     request tenant that differs from the identity tenant is rejected with a
//     *CrossTenantDeniedError (fail closed — never silently coerced).
//
// op names the attempted operation for the error message (e.g. "create user").
func EffectiveTenant(ctx context.Context, requested, op string) (string, error) {
	identityTenant := FromContext(ctx)
	if IsSystemTenant(identityTenant) {
		if requested == "" {
			return SystemTenantID, nil
		}
		return requested, nil
	}
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
