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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// nameResolverFake is a RoleNameResolver over a fixed tenant/name -> role
// table; a missing entry is *role.RoleNotFoundError, as the role store says.
type nameResolverFake struct {
	roles map[string]*role.Role
}

func (f nameResolverFake) GetRoleByName(_ context.Context, tenantID, name string) (*role.Role, error) {
	if r, ok := f.roles[tenantID+"/"+name]; ok {
		return r, nil
	}
	return nil, &role.RoleNotFoundError{ID: tenantID + "/" + name}
}

// TestSDK_HoldsSystemRole pins the SDK wrapper of role.HoldsSystemRole: it
// matches the persisted canonical role ID, never the role's name.
func TestSDK_HoldsSystemRole(t *testing.T) {
	const tenantID = "acme"
	adminID := sdk.SystemRoleID(tenantID, role.RoleNameAdmin)
	fake := nameResolverFake{roles: map[string]*role.Role{
		tenantID + "/" + role.RoleNameAdmin: {ID: adminID, Name: role.RoleNameAdmin, TenantID: tenantID},
	}}
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		roleIDs  []string
		roleName string
		want     bool
	}{
		{"canonical ID", []string{"other", adminID}, role.RoleNameAdmin, true},
		{"role name as ID", []string{role.RoleNameAdmin}, role.RoleNameAdmin, false},
		{"no role IDs", nil, role.RoleNameAdmin, false},
		{"unknown role", []string{adminID}, "no-such-role", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sdk.HoldsSystemRole(ctx, fake, tenantID, tc.roleIDs, tc.roleName)
			if err != nil {
				t.Fatalf("HoldsSystemRole: %v", err)
			}
			if got != tc.want {
				t.Errorf("HoldsSystemRole: got %t, want %t", got, tc.want)
			}
		})
	}

	t.Run("nil resolver", func(t *testing.T) {
		got, err := sdk.HoldsSystemRole(ctx, nil, tenantID, []string{adminID}, role.RoleNameAdmin)
		if got {
			t.Errorf("HoldsSystemRole: got true, want false")
		}
		var reqErr *sdk.RoleResolverRequiredError
		if !errors.As(err, &reqErr) {
			t.Fatalf("HoldsSystemRole error: got %v, want *sdk.RoleResolverRequiredError", err)
		}
	})
}
