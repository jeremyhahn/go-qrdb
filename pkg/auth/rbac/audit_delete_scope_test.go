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

package rbac

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// authorizeAuditDelete authorizes audit:delete on target for a principal of
// callerTenant whose one custom role, created in callerTenant, holds perms.
// The caller is a tenant principal: callerTenant is never the system tenant,
// so no system-tenant lift can stand in for the self scope under test.
func authorizeAuditDelete(t *testing.T, callerTenant, target string, perms ...permission.Permission) error {
	t.Helper()
	if callerTenant == tenantid.System {
		t.Fatalf("authorizeAuditDelete: caller tenant %q is the system tenant", callerTenant)
	}
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	roleID := authtest.CustomRole(t, roles, callerTenant, "audit-custodian", perms...)
	id := &auth.Identity{
		Subject:  "audit-custodian@" + callerTenant,
		TenantID: callerTenant,
		Roles:    []string{roleID},
	}
	ctx := tenant.WithTenantID(context.Background(), target)
	return New(roles).Authorize(ctx, id, permission.Resource(permission.AuditDelete), permission.Action(permission.AuditDelete))
}

// TestAuthorize_AuditDelete_GrantedInOwnTenant proves #1314's first
// verification: audit:delete is self scope, so a tenant principal whose role
// holds it is authorized for its own tenant's audit delete.
func TestAuthorize_AuditDelete_GrantedInOwnTenant(t *testing.T) {
	for _, tc := range []struct {
		name  string
		perms []permission.Permission
	}{
		{"delete only", []permission.Permission{permission.AuditDelete}},
		{"delete and query", []permission.Permission{permission.AuditDelete, permission.AuditQuery}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, tenantID := range []string{"acme", "globex"} {
				if err := authorizeAuditDelete(t, tenantID, tenantID, tc.perms...); err != nil {
					t.Errorf("%s audit:delete on %s: got %v, want allowed", tenantID, tenantID, err)
				}
			}
		})
	}
}

// TestAuthorize_AuditDelete_QueryOnlyDenied proves #1314's second
// verification: audit:query does not imply audit:delete, so a tenant
// principal holding only audit:query is refused it on its own tenant.
func TestAuthorize_AuditDelete_QueryOnlyDenied(t *testing.T) {
	for _, tc := range []struct {
		name  string
		perms []permission.Permission
	}{
		{"query only", []permission.Permission{permission.AuditQuery}},
		{"query and read", []permission.Permission{permission.AuditQuery, permission.AuditRead}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := authorizeAuditDelete(t, "acme", "acme", tc.perms...)
			var denied *PermissionDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("acme %v audit:delete on acme: got %v, want *PermissionDeniedError", tc.perms, err)
			}
			var resolve *ResolveError
			if errors.As(err, &resolve) {
				t.Fatalf("acme %v audit:delete on acme failed to resolve its own roles: %v", tc.perms, err)
			}
		})
	}
}

// TestAuthorize_AuditDelete_CrossTenantDenied proves self scope's
// confinement: the tenant principal holding audit:delete is refused it on
// another tenant, in either direction.
func TestAuthorize_AuditDelete_CrossTenantDenied(t *testing.T) {
	for _, tc := range []struct {
		caller, target string
	}{
		{"acme", "globex"},
		{"globex", "acme"},
		{"acme", tenantid.System},
	} {
		t.Run(tc.caller+" on "+tc.target, func(t *testing.T) {
			err := authorizeAuditDelete(t, tc.caller, tc.target, permission.AuditDelete)
			var denied *PermissionDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("%s audit:delete on %s: got %v, want *PermissionDeniedError", tc.caller, tc.target, err)
			}
			var resolve *ResolveError
			if errors.As(err, &resolve) {
				t.Fatalf("%s audit:delete on %s failed to resolve its own roles: %v", tc.caller, tc.target, err)
			}
		})
	}
}
