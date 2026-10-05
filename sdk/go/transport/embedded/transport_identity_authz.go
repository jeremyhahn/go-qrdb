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

package embedded

import (
	"context"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// This file is the embedded transport's half of the identity surface
// (users and roles): every CRUD operation on a user or role record is
// authorized against the tenant the record names or belongs to, resolved
// exactly the way pkg/api/quic/targets.go resolves it for the SAME
// operation (UserTarget looks the record up through t.userSvc, RoleTarget
// through t.roleSvc, falling back to the caller's own tenant when the
// record is absent or hidden from it -- see AuthMiddleware.ResolveTarget).
//
// A tenant-scoped identity cannot read, write, or delete another tenant's
// user or role through this path: a request naming a user/role in another
// tenant resolves ITS tenant, and the self-scope permission denies a caller
// whose own tenant does not match it.
//
// Role-ASSIGNMENT writes (RoleIDs, RoleBindings, Attributes, Assign,
// Unassign) are not here -- they are AuthorizeRoleAssignmentWrite /
// AuthorizeRoleUnassignment (role_assignment.go), already wired in
// transport_auth.go, which resolve their own target by reading the
// record and would double-charge the rate limiter and the audit log if
// also run through authorizeUserTarget below.

// authorizeUserTarget authorizes perm against the tenant userID belongs to.
func (t *Transport) authorizeUserTarget(ctx context.Context, userID uint64, perm permission.Permission) error {
	if t.authMW == nil {
		return nil
	}
	return t.authMW.AuthorizeTarget(ctx, perm, authmw.UserTarget(userID))
}

// authorizeRoleTarget authorizes perm against the tenant roleID belongs to.
func (t *Transport) authorizeRoleTarget(ctx context.Context, roleID string, perm permission.Permission) error {
	if t.authMW == nil {
		return nil
	}
	return t.authMW.AuthorizeTarget(ctx, perm, authmw.RoleTarget(roleID))
}

// authorizeIdentityTenant authorizes perm against tenantID, or the caller's
// own tenant when tenantID is empty -- the resolution every identity
// operation naming a tenant directly (create, list, get-by-username/email,
// reconcile-built-in) uses.
func (t *Transport) authorizeIdentityTenant(ctx context.Context, tenantID string, perm permission.Permission) error {
	if t.authMW == nil {
		return nil
	}
	return t.authMW.AuthorizeTarget(ctx, perm, authmw.TenantTarget(tenantID))
}
