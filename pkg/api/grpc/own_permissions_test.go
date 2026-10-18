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

//go:build !integration

package grpc

import (
	"context"
	"reflect"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ownPermissionNames returns what role.ListOwnPermissions yields for id, the
// reference the handler's response is held to.
func ownPermissionNames(t *testing.T, roles role.RoleServicer, id *auth.Identity) []string {
	t.Helper()
	perms, err := role.ListOwnPermissions(auth.WithIdentity(context.Background(), id), roles)
	if err != nil {
		t.Fatalf("role.ListOwnPermissions: %v", err)
	}
	names := make([]string, len(perms))
	for i, p := range perms {
		names[i] = p.Name
	}
	return names
}

func TestGRPC_ListOwnPermissions_ReturnsTheCallersOwnGrants(t *testing.T) {
	roles := authtest.NewTenantRoutedRoles(t, authtest.BindingTenant, authtest.OtherTenant)
	srv := &qrdbServiceServer{roleSvc: roles}
	id := &auth.Identity{
		Subject:  "alice",
		TenantID: authtest.BindingTenant,
		Roles:    []string{role.SystemRoleID(authtest.BindingTenant, role.RoleNameAuditor)},
	}

	resp, err := srv.ListOwnPermissions(auth.WithIdentity(context.Background(), id), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("ListOwnPermissions: %v", err)
	}
	if len(resp.GetPermissions()) == 0 {
		t.Fatal("the auditor role resolved to no permissions")
	}
	got := make([]string, len(resp.GetPermissions()))
	for i, p := range resp.GetPermissions() {
		got[i] = p.GetName()
		perm := permission.Permission(p.GetName())
		scope, err := permission.LookupScope(perm)
		if err != nil {
			t.Fatalf("%s: %v", p.GetName(), err)
		}
		if p.GetResource() != permission.Resource(perm) || p.GetAction() != permission.Action(perm) || p.GetScope() != scope.String() {
			t.Fatalf("%s: got resource=%q action=%q scope=%q", p.GetName(), p.GetResource(), p.GetAction(), p.GetScope())
		}
	}
	if want := ownPermissionNames(t, roles, id); !reflect.DeepEqual(got, want) {
		t.Fatalf("permissions = %v, want %v", got, want)
	}
}

func TestGRPC_ListOwnPermissions_NeverReturnsAnotherTenantsGrants(t *testing.T) {
	roles := authtest.NewTenantRoutedRoles(t, authtest.BindingTenant, authtest.OtherTenant)
	srv := &qrdbServiceServer{roleSvc: roles}
	id := authtest.AdminIdentity(authtest.BindingTenant, "alice")
	ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), id), authtest.OtherTenant)

	if _, err := srv.ListOwnPermissions(ctx, &emptypb.Empty{}); err != nil {
		t.Fatalf("ListOwnPermissions: %v", err)
	}
	if got := roles.Resolved(); !reflect.DeepEqual(got, []string{authtest.BindingTenant}) {
		t.Fatalf("resolved tenants %v, want only the caller's own %q", got, authtest.BindingTenant)
	}
}

func TestGRPC_ListOwnPermissions_RefusesAnUnauthenticatedCaller(t *testing.T) {
	roles := authtest.NewTenantRoutedRoles(t, authtest.BindingTenant)
	srv := &qrdbServiceServer{roleSvc: roles}

	_, err := srv.ListOwnPermissions(context.Background(), &emptypb.Empty{})
	if code := status.Code(err); code != codes.Unauthenticated {
		t.Fatalf("code = %s (%v), want Unauthenticated", code, err)
	}
	if got := roles.Resolved(); len(got) != 0 {
		t.Fatalf("an unauthenticated call resolved tenants %v", got)
	}
}

func TestGRPC_ListOwnPermissions_NilService(t *testing.T) {
	srv := &qrdbServiceServer{}
	ctx := auth.WithIdentity(context.Background(), authtest.AdminIdentity(authtest.BindingTenant, "alice"))

	_, err := srv.ListOwnPermissions(ctx, &emptypb.Empty{})
	if code := status.Code(err); code != codes.Unimplemented {
		t.Fatalf("code = %s (%v), want Unimplemented", code, err)
	}
}

func TestGRPC_ListOwnPermissions_IsIdentityGated(t *testing.T) {
	got, ok := middleware.LookupGRPCFromDescriptor(pb.QRDBService_ListOwnPermissions_FullMethodName)
	if !ok || got != permission.IdentityGated {
		t.Fatalf("got (%q, %v), want (%q, true)", got, ok, permission.IdentityGated)
	}
}
