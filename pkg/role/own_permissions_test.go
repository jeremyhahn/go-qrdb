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

package role

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// tenantRoutedResolver is a RoleServicer double that resolves a role ID in
// the CONTEXT tenant, the way routingRoleService.ResolvePermissions routes:
// grants[tenant][roleID] is what that role grants in that tenant. Every
// other RoleServicer method is the nil embedded interface and panics if
// called.
type tenantRoutedResolver struct {
	RoleServicer
	grants  map[string]map[string][]permission.Permission
	err     error
	tenants []string
}

func (r *tenantRoutedResolver) ResolvePermissions(ctx context.Context, roleIDs []string) ([]permission.Permission, error) {
	tid := tenant.FromContext(ctx)
	r.tenants = append(r.tenants, tid)
	if r.err != nil {
		return nil, r.err
	}
	var out []permission.Permission
	for _, id := range roleIDs {
		out = append(out, r.grants[tid][id]...)
	}
	return out, nil
}

func ownCtx(tenantID string, roles ...string) context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: tenantID, Roles: roles})
}

func names(infos []PermissionInfo) []string {
	out := make([]string, len(infos))
	for i, info := range infos {
		out[i] = info.Name
	}
	return out
}

func TestListOwnPermissions_ReturnsTheCallersOwnGrantsWithScope(t *testing.T) {
	svc := &tenantRoutedResolver{grants: map[string]map[string][]permission.Permission{
		"acme": {"reader": {permission.KVGet, permission.RolesRead}, "other": {permission.KVPut}},
	}}
	got, err := ListOwnPermissions(ownCtx("acme", "reader"), svc)
	if err != nil {
		t.Fatalf("ListOwnPermissions: %v", err)
	}
	if want := []string{"kv:get", "roles:read"}; !reflect.DeepEqual(names(got), want) {
		t.Fatalf("names = %v, want %v (only the caller's own role's grants, sorted)", names(got), want)
	}
	for _, info := range got {
		scope, err := permission.LookupScope(permission.Permission(info.Name))
		if err != nil {
			t.Fatalf("LookupScope(%s): %v", info.Name, err)
		}
		if info.Scope != scope.String() || info.Resource+":"+info.Action != info.Name {
			t.Errorf("info = %+v, want scope %q and resource:action matching the name", info, scope)
		}
	}
}

func TestListOwnPermissions_NeverResolvesInAnotherTenant(t *testing.T) {
	svc := &tenantRoutedResolver{grants: map[string]map[string][]permission.Permission{
		"acme":   {"r1": {permission.KVGet}},
		"globex": {"r1": {permission.KVPut, permission.KVDelete}},
	}}
	// The request targets globex; the caller is acme's. The resolve must
	// run in acme, so globex's same-id role contributes nothing.
	ctx := tenant.WithTenantID(ownCtx("acme", "r1"), "globex")
	got, err := ListOwnPermissions(ctx, svc)
	if err != nil {
		t.Fatalf("ListOwnPermissions: %v", err)
	}
	if want := []string{"kv:get"}; !reflect.DeepEqual(names(got), want) {
		t.Fatalf("names = %v, want %v: another tenant's grants were returned", names(got), want)
	}
	if want := []string{"acme"}; !reflect.DeepEqual(svc.tenants, want) {
		t.Fatalf("resolved in tenants %v, want %v", svc.tenants, want)
	}
}

func TestListOwnPermissions_RefusesAnUnauthenticatedCaller(t *testing.T) {
	svc := &tenantRoutedResolver{}
	cases := map[string]context.Context{
		"no identity":  context.Background(),
		"no subject":   auth.WithIdentity(context.Background(), &auth.Identity{TenantID: "acme"}),
		"no tenant id": auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice"}),
	}
	for name, ctx := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ListOwnPermissions(ctx, svc)
			if !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("err = %v, want auth.ErrUnauthenticated", err)
			}
		})
	}
	if len(svc.tenants) != 0 {
		t.Fatalf("an unauthenticated caller reached the role service (%v)", svc.tenants)
	}
}

func TestListOwnPermissions_ExpandsWildcardGrants(t *testing.T) {
	svc := &tenantRoutedResolver{grants: map[string]map[string][]permission.Permission{
		"acme": {"kvadmin": {"kv:*", permission.KVGet}},
	}}
	got, err := ListOwnPermissions(ownCtx("acme", "kvadmin"), svc)
	if err != nil {
		t.Fatalf("ListOwnPermissions: %v", err)
	}
	members := permission.Expand("kv:*")
	if len(members) < 2 || len(got) != len(members) {
		t.Fatalf("got %d permissions, want the %d registered kv members (deduplicated)", len(got), len(members))
	}
	for _, info := range got {
		if info.Name == "kv:*" {
			t.Fatalf("a wildcard was projected as a permission: %+v", info)
		}
	}
}

func TestListOwnPermissions_PropagatesErrors(t *testing.T) {
	boom := errors.New("store down")
	if _, err := ListOwnPermissions(ownCtx("acme", "r"), &tenantRoutedResolver{err: boom}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the resolve error", err)
	}
	var sr *ServiceRequiredError
	if _, err := ListOwnPermissions(ownCtx("acme", "r"), nil); !errors.As(err, &sr) {
		t.Fatalf("err = %v, want *ServiceRequiredError", err)
	}
}

func TestEffectivePermissions_DeduplicatesAndSorts(t *testing.T) {
	svc := &tenantRoutedResolver{grants: map[string]map[string][]permission.Permission{
		"acme": {"a": {permission.RolesRead, permission.KVGet}, "b": {permission.KVGet}},
	}}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	got, err := EffectivePermissions(ctx, svc, []string{"a", "b"})
	if err != nil {
		t.Fatalf("EffectivePermissions: %v", err)
	}
	if want := []permission.Permission{permission.KVGet, permission.RolesRead}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	empty, err := EffectivePermissions(ctx, svc, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("no roles: got %v, %v; want an empty set", empty, err)
	}
}
