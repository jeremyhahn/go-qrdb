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
// RBAC types
// ---------------------------------------------------------------------------

// Identity is the SDK projection of an authenticated entity. It is the
// transport-agnostic view of pkg/auth.Identity — a struct used for RBAC
// admission decisions.
//
// Subject is the immutable user id of the account the caller resolved to,
// in decimal; a caller that resolved to no account keeps its authenticated
// name (e.g. the CN of a client cert).
// TenantID is always non-empty for a well-formed identity; the
// system-tenant rule grants cross-tenant scope when TenantID equals
// the system tenant id ("system").
type Identity struct {
	// Subject identifies the authenticated principal.
	Subject string `json:"subject"`

	// TenantID is the tenant scope of the identity. Always non-empty.
	TenantID string `json:"tenantID"`

	// Roles holds the role names assigned to this identity.
	Roles []string `json:"roles,omitempty"`

	// Metadata is free-form attribute storage carried alongside the
	// identity for audit / logging purposes. It does not influence
	// authorization decisions.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ---------------------------------------------------------------------------
// RBACClient interface
// ---------------------------------------------------------------------------

// RBACClient exposes the role-based access control authorizer. It is a
// thin façade over the qrdb auth.Authorizer and honours the Phase 0
// system-tenant rule: identities in the reserved system tenant carry
// cross-tenant scope for self-scope permissions, and exclusively hold
// cross-scope permissions.
type RBACClient interface {
	// Authorize returns nil when identity is permitted to perform action
	// on resource and a typed denial otherwise. The target tenant is
	// derived from the request context (callers thread it via the qrdb
	// tenant package). When no tenant is bound to the context, the
	// request is treated as targeting the system tenant — daemon-internal
	// flows execute against system by convention.
	Authorize(ctx context.Context, identity *Identity, resource, action string) error

	// ListPermissions returns the full set of permissions granted to the
	// identity through its roles, with the Scope metadata resolved per
	// permission. Cross-scope permissions are surfaced regardless of the
	// identity's tenant — the caller is responsible for filtering when a
	// UI wants to show "what you can do here" versus "what you can do at
	// all".
	ListPermissions(ctx context.Context, identity *Identity) ([]PermissionInfo, error)
}
