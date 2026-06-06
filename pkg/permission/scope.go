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
// ScopeSelf permissions act on the identity's own tenant. The RBAC
// authorizer admits a ScopeSelf grant when the target tenant matches the
// identity's tenant, or when the identity belongs to the structurally
// privileged system tenant (the system-tenant rule lifts self-scope perms
// across all tenants for system members).
//
// ScopeCross permissions act across tenants and are reserved for the
// system tenant. The RBAC authorizer rejects a ScopeCross grant when the
// identity's tenant is not the system tenant — a tenant-local Admin role
// holding a cross-scope permission cannot exercise it outside its own
// tenant.
//
// Permissions default to ScopeSelf. New cross-tenant permissions
// (tenant:list:cross, tenant:approve, volumepool:*, etc.) explicitly
// register as ScopeCross.
type Scope int

const (
	// ScopeSelf is the default — the permission acts on the identity's own
	// tenant unless the identity belongs to the system tenant, in which
	// case the system-tenant rule allows the permission to act on any
	// tenant.
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
	default:
		return "unknown"
	}
}

// Valid reports whether s is one of the defined scope constants. The
// "unknown" sentinel produced by String for out-of-range values is not
// valid.
func (s Scope) Valid() bool {
	switch s {
	case ScopeSelf, ScopeCross, ScopeTenantOnly:
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
	default:
		return ScopeSelf, &InvalidScopeError{Value: label}
	}
}

// scopeRegistry holds the process-global, built-in/default scope layer.
// These are the uniform platform permissions registered by qrdb's own
// init() functions (cross_scope.go, tenant_scope.go); they are correctly
// global because a built-in permission means the same thing in every
// tenant. Permissions absent from the registry default to ScopeSelf via
// LookupScope. Per-tenant, caller-defined scope overrides do NOT live here
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
// permission. When the permission has not been explicitly registered, it
// returns ScopeSelf — the safe default that confines a permission to the
// identity's own tenant unless lifted by the system-tenant rule.
//
// LookupScope consults ONLY the built-in default layer; it is the
// back-compat / built-in-only path. Tenant-correct callers (RBAC
// Authorize) must use ResolveScope so a caller-defined permission resolves
// in its own tenant.
func LookupScope(p Permission) Scope {
	scopeMu.RLock()
	defer scopeMu.RUnlock()
	if s, ok := scopeRegistry[p]; ok {
		return s
	}
	return ScopeSelf
}

// LookupScopeByName is a convenience wrapper around LookupScope that
// accepts a raw permission string. It is equivalent to
// LookupScope(Permission(name)).
func LookupScopeByName(name string) Scope {
	return LookupScope(Permission(name))
}

// IsCrossScope reports whether the given permission has been registered as
// ScopeCross in the built-in default layer. Equivalent to
// LookupScope(p) == ScopeCross.
func IsCrossScope(p Permission) bool {
	return LookupScope(p) == ScopeCross
}

// resetScopeRegistryForTesting clears the scope registry. It is intended
// for use by tests that need to reset state between cases. The function
// is unexported and not part of the public API.
func resetScopeRegistryForTesting() {
	scopeMu.Lock()
	defer scopeMu.Unlock()
	scopeRegistry = map[Permission]Scope{}
}
