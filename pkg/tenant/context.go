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
// MUST act on. For a normal tenant caller the effective tenant is NEVER
// sourced from a client-supplied request field — it is the identity tenant —
// mirroring the data-plane rule enforced by the REST scope middleware.
//
// Rules:
//   - System-tenant callers (identity == tenantid.System) may target any
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
	if tenantid.IsSystem(identityTenant) {
		if requested == "" {
			return tenantid.System, nil
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
