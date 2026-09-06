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

package role

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// RoleServicer defines role management operations. All implementations
// must be safe for concurrent use.
type RoleServicer interface {
	// CreateRole creates a new role with a generated UUID. The name must
	// be unique within the tenant.
	CreateRole(ctx context.Context, req *CreateRoleRequest) (*Role, error)

	// GetRole retrieves a role by its globally unique ID.
	GetRole(ctx context.Context, id string) (*Role, error)

	// GetRoleByName retrieves a role by tenant-scoped name.
	GetRoleByName(ctx context.Context, tenantID, name string) (*Role, error)

	// UpdateRole applies partial updates to an existing role.
	UpdateRole(ctx context.Context, req *UpdateRoleRequest) (*Role, error)

	// DeleteRole removes a non-system role by ID.
	DeleteRole(ctx context.Context, id string) error

	// ListRoles returns a paginated list of roles for the given tenant.
	ListRoles(ctx context.Context, req *ListRolesRequest) (*ListRolesResponse, error)

	// ResolvePermissions returns the union of all permissions for the
	// given role IDs. The result is sorted and deduplicated.
	ResolvePermissions(ctx context.Context, roleIDs []string) ([]permission.Permission, error)

	// ReconcileBuiltInRoles converges the tenant's stored built-in roles onto
	// BuiltInRoles — the build's live projection of the permission registry —
	// adding permissions it now grants, removing ones it no longer does, and
	// leaving custom roles untouched. It is the ONE sanctioned writer to a
	// system role's permissions; UpdateRole and DeleteRole still refuse every
	// other caller. Idempotent: a converged tenant is reported unchanged and
	// nothing is written.
	//
	// Call it AFTER module permission registration — BuiltInRoles reads the
	// registry at call time.
	ReconcileBuiltInRoles(ctx context.Context, tenantID string) (*ReconcileReport, error)
}
