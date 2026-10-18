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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func TestPermissionsOwnTool_ReturnsTheCallersOwnGrants(t *testing.T) {
	roles := authtest.NewTenantRoutedRoles(t, authtest.BindingTenant, authtest.OtherTenant)
	id := &auth.Identity{
		Subject:  "alice",
		TenantID: authtest.BindingTenant,
		Roles:    []string{role.SystemRoleID(authtest.BindingTenant, role.RoleNameAuditor)},
	}
	ctx := auth.WithIdentity(context.Background(), id)

	result, err := handlePermissionsOwn(roles)(ctx, mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handlePermissionsOwn: %v", err)
	}
	var out map[string][]role.PermissionInfo
	if err := json.Unmarshal([]byte(resultText(t, result)), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	want, err := role.ListOwnPermissions(ctx, roles)
	if err != nil {
		t.Fatalf("role.ListOwnPermissions: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("the auditor role resolved to no permissions")
	}
	if !reflect.DeepEqual(out["permissions"], want) {
		t.Fatalf("permissions = %+v, want %+v", out["permissions"], want)
	}
}

func TestPermissionsOwnTool_NeverReturnsAnotherTenantsGrants(t *testing.T) {
	roles := authtest.NewTenantRoutedRoles(t, authtest.BindingTenant, authtest.OtherTenant)
	ctx := auth.WithIdentity(context.Background(), authtest.AdminIdentity(authtest.BindingTenant, "alice"))
	ctx = tenant.WithTenantID(ctx, authtest.OtherTenant)

	if _, err := handlePermissionsOwn(roles)(ctx, mcp.CallToolRequest{}); err != nil {
		t.Fatalf("handlePermissionsOwn: %v", err)
	}
	if got := roles.Resolved(); !reflect.DeepEqual(got, []string{authtest.BindingTenant}) {
		t.Fatalf("resolved tenants %v, want only the caller's own %q", got, authtest.BindingTenant)
	}
}

func TestPermissionsOwnTool_RefusesAnUnauthenticatedCaller(t *testing.T) {
	roles := authtest.NewTenantRoutedRoles(t, authtest.BindingTenant)

	_, err := handlePermissionsOwn(roles)(context.Background(), mcp.CallToolRequest{})
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("error = %v, want auth.ErrUnauthenticated", err)
	}
	if got := ServiceErrorCode(err); got != wire.CodeUnauthenticated {
		t.Fatalf("code = %v, want %v", got, wire.CodeUnauthenticated)
	}
	if got := roles.Resolved(); len(got) != 0 {
		t.Fatalf("an unauthenticated call resolved tenants %v", got)
	}
}

func TestPermissionsOwnTool_IsIdentityGated(t *testing.T) {
	for _, tl := range roleTools(nil, nil, nil) {
		if tl.Tool.Name != "qrdb_permissions_own" {
			continue
		}
		if tl.Perm != permission.IdentityGated {
			t.Fatalf("qrdb_permissions_own permission = %q, want %q", tl.Perm, permission.IdentityGated)
		}
		return
	}
	t.Fatal("qrdb_permissions_own is not among the role tools")
}
