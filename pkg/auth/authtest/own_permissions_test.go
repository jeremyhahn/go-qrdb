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

package authtest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func TestTenantRoutedRoles_RoutesByTheContextTenantAndRecordsIt(t *testing.T) {
	roles := NewTenantRoutedRoles(t, BindingTenant, OtherTenant)
	ids := []string{role.SystemRoleID(OtherTenant, role.RoleNameAuditor)}

	perms, err := roles.ResolvePermissions(tenant.WithTenantID(context.Background(), OtherTenant), ids)
	if err != nil {
		t.Fatalf("ResolvePermissions in %s: %v", OtherTenant, err)
	}
	if len(perms) == 0 {
		t.Fatal("the auditor role resolved to no permissions")
	}
	// The same role ID is not a role of BindingTenant's store.
	if _, err := roles.ResolvePermissions(tenant.WithTenantID(context.Background(), BindingTenant), ids); err == nil {
		t.Fatalf("%s's role resolved in %s", OtherTenant, BindingTenant)
	}
	if got, want := roles.Resolved(), []string{OtherTenant, BindingTenant}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Resolved() = %v, want %v", got, want)
	}
}

func TestTenantRoutedRoles_UnknownTenantIsNotFound(t *testing.T) {
	roles := NewTenantRoutedRoles(t, BindingTenant)

	if _, err := roles.Resolve(context.Background(), OtherTenant); !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("Resolve(%s) error = %v, want tenant.ErrTenantNotFound", OtherTenant, err)
	}
	if got := roles.Resolved(); !reflect.DeepEqual(got, []string{OtherTenant}) {
		t.Fatalf("Resolved() = %v, want [%s]", got, OtherTenant)
	}
}
