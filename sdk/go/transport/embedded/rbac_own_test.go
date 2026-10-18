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
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ownRoleSvc resolves role IDs in the context tenant, as the routed role
// service does, recording every tenant it was asked to resolve in.
func ownRoleSvc(grants map[string][]permission.Permission, tenants *[]string) *mockRoleServicer {
	return &mockRoleServicer{
		resolvePermsFn: func(ctx context.Context, _ []string) ([]permission.Permission, error) {
			tid := tenant.FromContext(ctx)
			*tenants = append(*tenants, tid)
			return grants[tid], nil
		},
	}
}

func TestRBACClient_ListOwnPermissions_ReturnsTheCallersOwnGrants(t *testing.T) {
	var tenants []string
	tr := newRBACTransport(t, ownRoleSvc(map[string][]permission.Permission{
		"acme": {permission.RolesRead, permission.KVGet},
	}, &tenants))
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: "acme", Roles: []string{"r1"}})

	got, err := tr.RBACClient().ListOwnPermissions(ctx)
	if err != nil {
		t.Fatalf("ListOwnPermissions: %v", err)
	}
	want := []transport.PermissionInfo{
		{Name: "kv:get", Resource: "kv", Action: "get", Scope: transport.PermissionScopeSelf},
		{Name: "roles:read", Resource: "roles", Action: "read", Scope: transport.PermissionScopeSelf},
	}
	for i := range want {
		scope, err := permission.LookupScope(permission.Permission(want[i].Name))
		if err != nil {
			t.Fatalf("LookupScope: %v", err)
		}
		want[i].Scope = transport.PermissionScope(scope.String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestRBACClient_ListOwnPermissions_NeverReturnsAnotherTenantsGrants(t *testing.T) {
	var tenants []string
	tr := newRBACTransport(t, ownRoleSvc(map[string][]permission.Permission{
		"acme":   {permission.KVGet},
		"globex": {permission.KVPut, permission.KVDelete},
	}, &tenants))
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: "acme", Roles: []string{"r1"}})
	ctx = tenant.WithTenantID(ctx, "globex")

	got, err := tr.RBACClient().ListOwnPermissions(ctx)
	if err != nil {
		t.Fatalf("ListOwnPermissions: %v", err)
	}
	if len(got) != 1 || got[0].Name != "kv:get" {
		t.Fatalf("got %+v, want only acme's kv:get", got)
	}
	if !reflect.DeepEqual(tenants, []string{"acme"}) {
		t.Fatalf("resolved in %v, want only the caller's own tenant", tenants)
	}
}

func TestRBACClient_ListOwnPermissions_RefusesAnUnauthenticatedCaller(t *testing.T) {
	var tenants []string
	tr := newRBACTransport(t, ownRoleSvc(nil, &tenants))
	if _, err := tr.RBACClient().ListOwnPermissions(context.Background()); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v, want auth.ErrUnauthenticated", err)
	}
	if len(tenants) != 0 {
		t.Fatalf("an unauthenticated caller reached the role service")
	}
}

func TestRBACClient_ListOwnPermissions_MissingRoleServiceAndClosed(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: "acme"})
	var missing *MissingRoleServiceForRBACError
	if _, err := tr.RBACClient().ListOwnPermissions(ctx); !errors.As(err, &missing) {
		t.Fatalf("err = %v, want *MissingRoleServiceForRBACError", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var closed *transport.ClosedError
	if _, err := tr.RBACClient().ListOwnPermissions(ctx); !errors.As(err, &closed) {
		t.Fatalf("err = %v, want *transport.ClosedError", err)
	}
}
