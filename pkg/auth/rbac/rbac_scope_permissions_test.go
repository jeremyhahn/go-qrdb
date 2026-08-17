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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestAuthorize_ScopePermissionsGrant proves the pre-resolved token
// permission set authorizes a principal with no store roles: the exact
// permission stamped at issuance grants, anything else still denies.
func TestAuthorize_ScopePermissionsGrant(t *testing.T) {
	a := New(newMockRoleService(nil))
	id := &auth.Identity{
		Subject:          "app-client-1",
		TenantID:         "tenant-1",
		ScopePermissions: []string{"secrets:read"},
	}

	ctx := tenant.WithTenantID(context.Background(), "tenant-1")
	if err := a.Authorize(ctx, id, "secrets", "read"); err != nil {
		t.Fatalf("scope-granted permission must authorize: %v", err)
	}
	if err := a.Authorize(ctx, id, "secrets", "write"); err == nil {
		t.Fatal("a permission outside the token's scp set must deny")
	}
	// Empty scope set: everything denies.
	anon := &auth.Identity{Subject: "app-client-2", TenantID: "tenant-1"}
	if err := a.Authorize(ctx, anon, "secrets", "read"); err == nil {
		t.Fatal("no roles and no scope permissions must deny")
	}
}

// TestAuthorize_ScopePermissions_SODenyStillOverrides proves the SO
// deny-list precedes scope grants: an SO-role identity cannot reach a
// denied cryptographic operation via a token scope.
func TestAuthorize_ScopePermissions_SODenyStillOverrides(t *testing.T) {
	a := New(newMockRoleService(nil))
	id := &auth.Identity{
		Subject:          "so-officer",
		TenantID:         "tenant-1",
		Roles:            []string{"so"},
		ScopePermissions: []string{"keys:sign"},
	}
	if err := a.Authorize(context.Background(), id, "keys", "sign"); err == nil {
		t.Fatal("the SO deny-list must override a scope grant")
	}
}
