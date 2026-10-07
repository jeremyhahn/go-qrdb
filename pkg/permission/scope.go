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

package permission

import "sync"

// Scope classifies a permission's tenancy reach.
//
// ScopeSelf permissions act ONLY on the identity's own tenant, the system
// tenant included (owner ruling, 2026-10-01: "ScopeSelf has no system lift").
// Until that ruling the RBAC authorizer additionally admitted a ScopeSelf
// grant for any target when the identity belonged to the structurally
// privileged system tenant; that lift is gone, under every isolation policy.
//
// ScopeCross permissions act across tenants and are reserved for the
// system tenant. The RBAC authorizer rejects a ScopeCross grant when the
// identity's tenant is not the system tenant — a tenant-local Admin role
// holding a cross-scope permission cannot exercise it outside its own
// tenant.
//
// Every permission declares its scope; there is no default. Self-scoped
// permissions are named in self_scope.go, tenant-only ones in
// tenant_scope.go, and cross-tenant ones (tenant:list:cross,
// tenant:approve, volumepool:*, etc.) in cross_scope.go. LookupScope refuses
// a permission that appears in none of them.
type Scope int

const (
	// ScopeSelf — the permission acts on the identity's own tenant, and only
	// that tenant, the system tenant included. See the no-system-lift note
	// above.
	//
	// It is the zero value of Scope but NOT a default: LookupScope refuses an
	// unregistered permission rather than answering this (go-qrdb#504).
	ScopeSelf Scope = iota

	// ScopeCross is a cross-tenant permission. Only identities whose
	// TenantID equals SystemTenantID may exercise these.
	ScopeCross

	// ScopeTenantOnly is a strictly tenant-local permission that is denied
	// to the system tenant. Unlike ScopeSelf — which the system-tenant rule
	// lifts across all tenants — a ScopeTenantOnly permission is admitted
	// only when the identity is NOT in the system tenant AND the identity's
	// tenant equals the request's target tenant. It expresses
	// "tenant-admin-only, system-admin-denied, no cross-tenant" semantics
	// (for example, per-tenant barrier key custody, which the platform
	// operator must not hold).
	ScopeTenantOnly

	// ScopeSelfStrict is a permission confined to the identity's OWN tenant,
	// for every identity -- the system tenant included.
	//
	// Since the ScopeSelf system-tenant lift was removed (2026-10-01), this
	// scope is behaviorally identical to ScopeSelf; it remains a distinct
	// type pending a follow-on change to fold the two together
	// (docs/multi-tenancy/architecture.md "Strict tenant isolation"). Prefer
	// ScopeSelf for anything new; this value is kept for the permissions
	// already registered under it.
	//
	// Key escrow is the case that called for it: the system tenant escrows
	// and recovers its own keys, but a platform admin reaching another
	// tenant's escrowed key material would be a cross-tenant escalation
	// wearing an admin role.
	ScopeSelfStrict
)

// String returns a stable, human-readable label for the scope. The label
// is stable across releases and is the on-the-wire form used by ParseScope
// to persist per-tenant scope definitions (see TenantScopeStore); it is
// equally suitable for log and error messages.
func (s Scope) String() string {
	switch s {
	case ScopeSelf:
		return "self"
	case ScopeCross:
		return "cross"
	case ScopeTenantOnly:
		return "tenant-only"
	case ScopeSelfStrict:
		return "self-strict"
	default:
		return "unknown"
	}
}

// Valid reports whether s is one of the defined scope constants. The
// "unknown" sentinel produced by String for out-of-range values is not
// valid.
func (s Scope) Valid() bool {
	switch s {
	case ScopeSelf, ScopeCross, ScopeTenantOnly, ScopeSelfStrict:
		return true
	default:
		return false
	}
}

// ParseScope converts a scope label produced by Scope.String back into a
// Scope value. It is the inverse of String and is used to decode persisted
// per-tenant scope definitions. An unrecognized label fails closed: it
// returns (ScopeSelf, *InvalidScopeError) so a corrupt or unknown stored
// value can never silently widen a permission's reach.
func ParseScope(label string) (Scope, error) {
	switch label {
	case "self":
		return ScopeSelf, nil
	case "cross":
		return ScopeCross, nil
	case "tenant-only":
		return ScopeTenantOnly, nil
	case "self-strict":
		return ScopeSelfStrict, nil
	default:
		return ScopeSelf, &InvalidScopeError{Value: label}
	}
}

// scopeRegistry holds the process-global, built-in/default scope layer.
// These are the uniform platform permissions registered by qrdb's own
// init() functions (cross_scope.go, tenant_scope.go); they are correctly
// global because a built-in permission means the same thing in every
// tenant. A permission absent from the registry has no scope: LookupScope
// returns *UndeclaredScopeError for it, and the authorizer denies.
// Per-tenant, caller-defined scope overrides do NOT live here
// — they live in the tenant's own store via TenantScopeStore and take
// precedence over this layer (see ResolveScope). The registry is guarded
// by scopeMu.
var (
	scopeMu       sync.RWMutex
	scopeRegistry = map[Permission]Scope{}
)

// RegisterScope tags one or more built-in permissions with the given scope
// in the process-global default layer. It is safe for concurrent use and
// is typically called from init() functions that define cross-scope
// permissions. Re-registering a permission overwrites the previous scope.
// Caller-defined, per-tenant scope overrides must NOT use this function;
// they belong in a TenantScopeStore so two tenants cannot collide on the
// same permission name.
func RegisterScope(scope Scope, perms ...Permission) {
	scopeMu.Lock()
	defer scopeMu.Unlock()
	for _, p := range perms {
		scopeRegistry[p] = scope
	}
}

// LookupScope returns the built-in (process-global) scope for the given
// permission, or an *UndeclaredScopeError if no scope has been registered
// for it.
//
// There is no default. Until go-qrdb#504 an unregistered permission answered
// ScopeSelf, the weakest check in the vocabulary, so a permission whose
// tenancy reach nobody had decided was admitted on the same terms as one
// deliberately confined to its own tenant. That is how certs:revoke reached
// every tenant (go-qrdb#474). Every built-in permission is now declared in
// self_scope.go, tenant_scope.go or cross_scope.go, the gate
// TestEveryPermissionDeclaresItsScope refuses a build that adds one without
// a scope, and the RBAC authorizer denies on the error this returns — so the
// undeclared case fails closed at every layer rather than open at the first.
//
// LookupScope consults ONLY the built-in default layer; it is the
// back-compat / built-in-only path. Tenant-correct callers (RBAC
// Authorize) must use ResolveScope so a caller-defined permission resolves
// in its own tenant.
func LookupScope(p Permission) (Scope, error) {
	scopeMu.RLock()
	defer scopeMu.RUnlock()
	if s, ok := scopeRegistry[p]; ok {
		return s, nil
	}
	return ScopeSelf, &UndeclaredScopeError{Permission: p}
}

// LookupScopeByName is a convenience wrapper around LookupScope that
// accepts a raw permission string. It is equivalent to
// LookupScope(Permission(name)).
func LookupScopeByName(name string) (Scope, error) {
	return LookupScope(Permission(name))
}

// IsCrossScope reports whether the given permission has been registered as
// ScopeCross in the built-in default layer. An undeclared permission is not
// cross-scoped — it is not anything — so this reports false for it.
func IsCrossScope(p Permission) bool {
	s, err := LookupScope(p)
	return err == nil && s == ScopeCross
}

// resetScopeRegistryForTesting clears the scope registry. It is intended
// for use by tests that need to reset state between cases. The function
// is unexported and not part of the public API.
func resetScopeRegistryForTesting() {
	scopeMu.Lock()
	defer scopeMu.Unlock()
	scopeRegistry = map[Permission]Scope{}
}
