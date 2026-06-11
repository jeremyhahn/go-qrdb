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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Permission types
// ---------------------------------------------------------------------------

// Permission represents a fine-grained access control permission in
// "resource:action" format (e.g., "kv:put", "admin:health").
type Permission = permission.Permission

// Category groups related permissions under a display name for UI rendering.
type PermissionCategory = permission.Category

// ---------------------------------------------------------------------------
// Permission error types
// ---------------------------------------------------------------------------

// InvalidPermissionError is returned when a permission string does not
// conform to the required "resource:action" format.
type InvalidPermissionError = permission.InvalidPermissionError

// UnregisteredPermissionError is returned when an operation references a
// permission that has not been registered in the global registry.
type UnregisteredPermissionError = permission.UnregisteredPermissionError

// ---------------------------------------------------------------------------
// Permission functions
// ---------------------------------------------------------------------------

// ParsePermission validates a raw string and returns the corresponding
// Permission.
var ParsePermission = permission.Parse

// RegisterPermissions adds one or more permissions to the global registry.
var RegisterPermissions = permission.Register

// AllPermissions returns every registered permission as a sorted slice.
var AllPermissions = permission.All

// IsPermissionRegistered reports whether p exists in the global registry.
var IsPermissionRegistered = permission.IsRegistered

// PermissionsByResource returns all registered permissions whose resource
// component matches the given name.
var PermissionsByResource = permission.ByResource

// PermissionCategories returns all permission categories for UI rendering.
var PermissionCategories = permission.Categories

// PermissionResource returns the resource component of a permission string.
var PermissionResource = permission.Resource

// PermissionAction returns the action component of a permission string.
var PermissionAction = permission.Action

// ---------------------------------------------------------------------------
// Per-tenant permission scope types
// ---------------------------------------------------------------------------

// PermissionScopeResolver resolves a permission's scope in the context of a
// specific tenant. It is the tenant-aware counterpart to the process-global
// scope registry and is the interface the RBAC authorizer depends on so that
// a caller-defined permission resolves in the caller's own tenant.
// (Aliases pkg/permission.ScopeResolver.)
type PermissionScopeResolver = permission.ScopeResolver

// TenantScopeStore persists and resolves caller-defined, per-tenant permission
// scopes. Two tenants may define the same permission name with different scopes
// without colliding, because each tenant's definitions are stored in that
// tenant's own store as selected by the TenantStoreResolver. (Aliases
// pkg/permission.TenantScopeStore.)
type TenantScopeStore = permission.TenantScopeStore

// NewTenantScopeStore constructs a TenantScopeStore over the given
// TenantStoreResolver. The resolver must be non-nil. (Aliases
// pkg/permission.NewTenantScopeStore.)
var NewTenantScopeStore = func(resolver tenant.StoreResolver) (*permission.TenantScopeStore, error) {
	return permission.NewTenantScopeStore(resolver)
}
