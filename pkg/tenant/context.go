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
	if v, ok := ctx.Value(contextKey{}).(string); ok && v != "" {
		return v
	}
	return SystemTenantID
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
