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

package sdk_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// newEffectiveRoleStore returns a real RoleServicer over the in-memory store,
// with a role in tenant tid granting perms. It returns the role's id.
func newEffectiveRoleStore(t *testing.T, tid string, perms ...sdk.Permission) (sdk.RoleServicer, string) {
	t.Helper()
	roles, err := role.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("role.NewStore: %v", err)
	}
	r, err := roles.CreateRole(tenant.WithTenantID(context.Background(), tid), &sdk.CreateRoleRequest{
		Name:        "effective",
		TenantID:    tid,
		Permissions: perms,
	})
	if err != nil {
		t.Fatalf("CreateRole(%s): %v", tid, err)
	}
	return roles, r.ID
}

func permissionNames(perms []sdk.Permission) []string {
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		out = append(out, p.String())
	}
	return out
}

// TestSDK_EffectivePermissions_ExpandsAWildcardGrant is the behaviour the
// re-export exists for: ResolvePermissions returns objstore:* as stored, and
// EffectivePermissions returns the registered objstore permissions instead.
func TestSDK_EffectivePermissions_ExpandsAWildcardGrant(t *testing.T) {
	svc, id := newEffectiveRoleStore(t, "acme", permission.ObjStoreAll)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	got, err := sdk.EffectivePermissions(ctx, svc, []string{id})
	if err != nil {
		t.Fatalf("EffectivePermissions: %v", err)
	}
	want := permissionNames(permission.Expand(permission.ObjStoreAll))
	sort.Strings(want)
	if len(want) < 2 {
		t.Fatalf("objstore:* expands to %v; the registry floor is two members", want)
	}
	if names := permissionNames(got); !reflect.DeepEqual(names, want) {
		t.Fatalf("EffectivePermissions = %v, want %v", names, want)
	}
	for _, p := range got {
		if p == permission.ObjStoreAll {
			t.Fatalf("the wildcard %q was returned unexpanded", p)
		}
	}
}

// TestSDK_EffectivePermissions_ResolvesInTheContextTenant proves a role
// created in another tenant grants nothing in the context tenant.
func TestSDK_EffectivePermissions_ResolvesInTheContextTenant(t *testing.T) {
	svc, id := newEffectiveRoleStore(t, "globex", permission.ObjStoreAll)

	own, err := sdk.EffectivePermissions(tenant.WithTenantID(context.Background(), "globex"), svc, []string{id})
	if err != nil || len(own) == 0 {
		t.Fatalf("control: globex's own role resolved to %v, %v; want its grants", own, err)
	}

	got, err := sdk.EffectivePermissions(tenant.WithTenantID(context.Background(), "acme"), svc, []string{id})
	if err == nil && len(got) != 0 {
		t.Fatalf("acme resolved globex's role to %v; want no grants", permissionNames(got))
	}
}

// TestSDK_EffectivePermissions_NilServiceIsACompositionFault pins the typed
// error a consumer classifies through the SDK name.
func TestSDK_EffectivePermissions_NilServiceIsACompositionFault(t *testing.T) {
	got, err := sdk.EffectivePermissions(context.Background(), nil, []string{"r1"})
	var required *sdk.RoleServiceRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("err = %T (%v), want *sdk.RoleServiceRequiredError", err, err)
	}
	if got != nil {
		t.Fatalf("permissions = %v, want nil", got)
	}
}
