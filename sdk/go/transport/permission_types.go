// Copyright 2026 Jeremy Hahn
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

package transport

import "context"

// ---------------------------------------------------------------------------
// Permission registry types
// ---------------------------------------------------------------------------
//
// Phase 3 exposes the permission registry verbatim — the SDK is the
// transport-agnostic projection of pkg/permission. The Scope metadata
// added in Phase 0 (ScopeSelf vs. ScopeCross) is surfaced so callers can
// answer "is this permission cross-tenant?" without importing pkg/permission
// directly.

// PermissionScope identifies the tenancy reach of a permission. It is the
// SDK projection of permission.Scope. Values are stable strings so they
// survive wire serialization unchanged.
type PermissionScope string

const (
	// PermissionScopeSelf identifies a permission that acts on the
	// identity's own tenant. The RBAC authorizer admits a self-scope grant
	// when the target tenant matches the identity's tenant or when the
	// identity belongs to the system tenant.
	PermissionScopeSelf PermissionScope = "self"

	// PermissionScopeCross identifies a cross-tenant permission. Only
	// identities whose TenantID equals the system tenant id may exercise
	// these.
	PermissionScopeCross PermissionScope = "cross"
)

// PermissionInfo is the SDK projection of a registered permission. It
// carries the canonical name in "resource:action" form plus the Scope
// metadata that gates cross-tenant exercise.
type PermissionInfo struct {
	// Name is the permission in "resource:action" form (e.g. "kv:put").
	Name string `json:"name"`

	// Resource is the portion before the colon.
	Resource string `json:"resource"`

	// Action is the portion after the colon.
	Action string `json:"action"`

	// Scope is the tenancy reach of the permission.
	Scope PermissionScope `json:"scope"`
}

// ---------------------------------------------------------------------------
// PermissionClient interface
// ---------------------------------------------------------------------------

// PermissionClient exposes the read-only permission registry. Permissions
// are statically declared; the registry is a single source of truth shared
// by the RBAC authorizer, the role service, and any UI that renders the
// permission tree.
type PermissionClient interface {
	// List returns every registered permission, sorted by name. The result
	// is independent of the caller's identity — the permission registry is
	// global.
	List(ctx context.Context) ([]PermissionInfo, error)

	// Lookup returns the permission with the given name. Returns
	// PermissionNotFoundError if the name is not registered.
	Lookup(ctx context.Context, name string) (PermissionInfo, error)

	// LookupScope returns the scope of the permission with the given name.
	// Returns PermissionScopeSelf for unregistered names — that is the
	// safe default that confines a permission to the identity's own
	// tenant unless lifted by the system-tenant rule.
	LookupScope(ctx context.Context, name string) (PermissionScope, error)
}
