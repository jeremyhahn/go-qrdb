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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func TestHandleListOwnPermissions_ReturnsTheCallersOwnGrants(t *testing.T) {
	roles := authtest.NewTenantRoutedRoles(t, authtest.BindingTenant, authtest.OtherTenant)
	srv := newRoleServer(roles, nil)
	id := &auth.Identity{
		Subject:  "alice",
		TenantID: authtest.BindingTenant,
		Roles:    []string{role.SystemRoleID(authtest.BindingTenant, role.RoleNameAuditor)},
	}
	ctx := auth.WithIdentity(context.Background(), id)

	data, err := srv.handleListOwnPermissions(ctx, nil)
	if err != nil {
		t.Fatalf("handleListOwnPermissions: %v", err)
	}
	var out listOwnPermissionsWireResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	want, err := role.ListOwnPermissions(ctx, roles)
	if err != nil {
		t.Fatalf("role.ListOwnPermissions: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("the auditor role resolved to no permissions")
	}
	if !reflect.DeepEqual(out.Permissions, want) {
		t.Fatalf("permissions = %+v, want %+v", out.Permissions, want)
	}
}

func TestHandleListOwnPermissions_NeverReturnsAnotherTenantsGrants(t *testing.T) {
	roles := authtest.NewTenantRoutedRoles(t, authtest.BindingTenant, authtest.OtherTenant)
	srv := newRoleServer(roles, nil)
	ctx := auth.WithIdentity(context.Background(), authtest.AdminIdentity(authtest.BindingTenant, "alice"))
	ctx = tenant.WithTenantID(ctx, authtest.OtherTenant)

	if _, err := srv.handleListOwnPermissions(ctx, nil); err != nil {
		t.Fatalf("handleListOwnPermissions: %v", err)
	}
	if got := roles.Resolved(); !reflect.DeepEqual(got, []string{authtest.BindingTenant}) {
		t.Fatalf("resolved tenants %v, want only the caller's own %q", got, authtest.BindingTenant)
	}
}

func TestHandleListOwnPermissions_RefusesAnUnauthenticatedCaller(t *testing.T) {
	roles := authtest.NewTenantRoutedRoles(t, authtest.BindingTenant)
	srv := newRoleServer(roles, nil)

	_, err := srv.handleListOwnPermissions(context.Background(), nil)
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("error = %v, want auth.ErrUnauthenticated", err)
	}
	if got := ServiceErrorStatus(err); got != wire.StatusUnauthenticated {
		t.Fatalf("status = %d, want %d", got, wire.StatusUnauthenticated)
	}
	if got := roles.Resolved(); len(got) != 0 {
		t.Fatalf("an unauthenticated call resolved tenants %v", got)
	}
}

func TestHandleListOwnPermissions_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	ctx := auth.WithIdentity(context.Background(), authtest.AdminIdentity(authtest.BindingTenant, "alice"))

	_, err := srv.handleListOwnPermissions(ctx, nil)
	var notConfigured *ServiceNotConfiguredError
	if !errors.As(err, &notConfigured) {
		t.Fatalf("error = %v, want *ServiceNotConfiguredError", err)
	}
}
