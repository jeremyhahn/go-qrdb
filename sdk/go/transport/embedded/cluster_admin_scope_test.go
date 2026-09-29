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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestClusterAdminScope: over the REAL built-in roles, the embedded
// RBACClient admits the system tenant's Admin to add or remove a replica,
// request a snapshot and drain the node, and denies a tenant's Admin and SO,
// whose built-in roles carry the same grants. It pins the authorization
// decision an embedder asks for, not a gate: the embedded transport's own
// admin methods run in-process for a trusted embedder and authorize nothing
// themselves.
func TestClusterAdminScope(t *testing.T) {
	roles := authtest.BuiltInRoles(t, tenantid.System, authtest.ClusterAdminTenant)
	tr := newRBACTransport(t, roles)
	for _, perm := range []permission.Permission{
		permission.AdminAddMember, permission.AdminRemoveMember,
		permission.AdminSnapshot, permission.AdminDrain,
	} {
		for _, c := range authtest.ClusterAdminCallers(t) {
			t.Run(string(perm)+"/"+c.Name, func(t *testing.T) {
				id := &transport.Identity{Subject: c.Identity.Subject, TenantID: c.Identity.TenantID, Roles: c.Identity.Roles}
				ctx := tenant.WithTenantID(context.Background(), id.TenantID)
				err := tr.RBACClient().Authorize(ctx, id, permission.Resource(perm), permission.Action(perm))
				if c.Admitted {
					if err != nil {
						t.Fatalf("Authorize: %v", err)
					}
					return
				}
				var denied *rbac.PermissionDeniedError
				if !errors.As(err, &denied) {
					t.Fatalf("Authorize = %T (%v), want *rbac.PermissionDeniedError", err, err)
				}
			})
		}
	}
}
