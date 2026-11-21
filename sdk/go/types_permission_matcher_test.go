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

	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// TestSDK_PermissionMatcher_IsTheAuthorizersDecision proves the re-exported
// matcher is the server's own rule, not a fork of it: for every grant and
// every required permission, sdk.PermissionMatches and sdk.PermissionGranted
// answer exactly what the RBAC authorizer decides for a role holding that
// grant.
func TestSDK_PermissionMatcher_IsTheAuthorizersDecision(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), tenantid.System)
	roles, err := role.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("role.NewStore: %v", err)
	}
	authz := rbac.New(roles)

	grants := []sdk.Permission{
		permission.KVGet,
		"kv:*",
		"*:get",
		"*:*",
		permission.UsersRead,
	}
	required := []sdk.Permission{
		permission.KVGet,
		permission.KVPut,
		permission.UsersRead,
		permission.CertsIssue,
	}

	for i, grant := range grants {
		r, cerr := roles.CreateRole(ctx, &role.CreateRoleRequest{
			Name:        "matcher-" + string(rune('a'+i)),
			TenantID:    tenantid.System,
			Permissions: []permission.Permission{grant},
		})
		if cerr != nil {
			t.Fatalf("CreateRole(%s): %v", grant, cerr)
		}
		id := &sdk.Identity{Subject: "matcher", TenantID: tenantid.System, Roles: []string{r.ID}}
		held := map[sdk.Permission]struct{}{grant: {}}

		for _, req := range required {
			aerr := authz.Authorize(ctx, id, permission.Resource(req), permission.Action(req))
			var denied *sdk.PermissionDeniedError
			if aerr != nil && !errors.As(aerr, &denied) {
				t.Fatalf("Authorize(%s with %s) failed to decide: %v", req, grant, aerr)
			}
			server := aerr == nil

			if got := sdk.PermissionMatches(grant, req); got != server {
				t.Errorf("PermissionMatches(%s, %s) = %v, authorizer admitted = %v", grant, req, got, server)
			}
			if got := sdk.PermissionGranted(held, req); got != server {
				t.Errorf("PermissionGranted({%s}, %s) = %v, authorizer admitted = %v", grant, req, got, server)
			}
		}
	}
}

// TestSDK_PermissionMatcher_RefusesGlobsAndEmpty pins the whole-segment rule
// at the SDK boundary: a glob prefix is not a wildcard and an empty set
// admits nothing.
func TestSDK_PermissionMatcher_RefusesGlobsAndEmpty(t *testing.T) {
	if sdk.PermissionMatches("ke*:sign", "keys:sign") {
		t.Error(`PermissionMatches("ke*:sign", "keys:sign") admitted a glob prefix`)
	}
	if sdk.PermissionGranted(map[sdk.Permission]struct{}{}, "keys:sign") {
		t.Error("PermissionGranted on an empty set admitted keys:sign")
	}
}
