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
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// authorizeCaller authenticates subject in tenant t1 and targets t1.
func authorizeCaller(subject string) context.Context {
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), &auth.Identity{Subject: subject, TenantID: "t1"}), "t1")
}

// callAuthorize runs qrdb_permissions_authorize with args as the caller on
// ctx, against the auth chain g.
func callAuthorize(ctx context.Context, t *testing.T, g auth.Authorizer, args map[string]any) (authorizeResult, error) {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	var out authorizeResult
	result, err := handlePermissionsAuthorize(authmw.New(g))(ctx, req)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(resultText(t, result)), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return out, nil
}

func TestPermissionsAuthorizeTool_AnswersForTheCaller(t *testing.T) {
	g := authtest.NewGrantAuthorizer(map[string][]string{"alice": {"kv:get"}})
	resp, err := callAuthorize(authorizeCaller("alice"), t, g, map[string]any{"resource": "kv", "action": "get"})
	if err != nil || !resp.Allowed {
		t.Fatalf("kv:get = %+v, %v; want allowed", resp, err)
	}
	resp, err = callAuthorize(authorizeCaller("alice"), t, g, map[string]any{"resource": "kv", "action": "put"})
	if err != nil || resp.Allowed || resp.Reason == "" {
		t.Fatalf("kv:put = %+v, %v; want denied with a reason", resp, err)
	}
}

func TestPermissionsAuthorizeTool_RefusesAnUnauthenticatedCaller(t *testing.T) {
	_, err := callAuthorize(context.Background(), t, authtest.NewGrantAuthorizer(nil), map[string]any{"resource": "kv", "action": "get"})
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
}

func TestPermissionsAuthorizeTool_AnotherIdentityNeedsPermissionsResolve(t *testing.T) {
	bob := map[string]any{"subject": "bob", "tenant_id": "t1", "resource": "kv", "action": "get"}
	g := authtest.NewGrantAuthorizer(map[string][]string{"bob": {"kv:get"}})
	if _, err := callAuthorize(authorizeCaller("alice"), t, g, bob); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if g.Evaluated("bob") {
		t.Fatal("bob was evaluated for a caller without permissions:resolve")
	}

	g = authtest.NewGrantAuthorizer(map[string][]string{"alice": {"permissions:resolve"}, "bob": {"kv:get"}})
	resp, err := callAuthorize(authorizeCaller("alice"), t, g, bob)
	if err != nil || !resp.Allowed {
		t.Fatalf("bob kv:get = %+v, %v; want allowed", resp, err)
	}
}

func TestPermissionsAuthorizeTool_NeverAnswersForAnotherTenant(t *testing.T) {
	g := authtest.NewGrantAuthorizer(map[string][]string{"alice": {"permissions:resolve"}, "carol": {"kv:get"}})
	carol := map[string]any{"subject": "carol", "tenant_id": "t2", "resource": "kv", "action": "get"}
	if _, err := callAuthorize(authorizeCaller("alice"), t, g, carol); err == nil {
		t.Fatal("a t1 caller got an answer about a t2 identity")
	}
	if g.Evaluated("carol") {
		t.Fatal("the other tenant's identity was evaluated")
	}
}

func TestPermissionsAuthorizeTool_IsIdentityGated(t *testing.T) {
	for _, tl := range roleTools(nil, nil, nil) {
		if tl.Tool.Name != "qrdb_permissions_authorize" {
			continue
		}
		if tl.Perm != permission.IdentityGated {
			t.Fatalf("qrdb_permissions_authorize permission = %q, want %q", tl.Perm, permission.IdentityGated)
		}
		return
	}
	t.Fatal("qrdb_permissions_authorize is not among the role tools")
}

// TestPermissionsAuthorizeTool_DevicesOnboard_TenantScoped answers through the
// real RBAC authorizer for a holder of acme's custom device-onboarder role:
// allowed in acme, refused with a reason in globex.
func TestPermissionsAuthorizeTool_DevicesOnboard_TenantScoped(t *testing.T) {
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	dana := &auth.Identity{Subject: "dana", TenantID: "acme", Roles: []string{
		authtest.CustomRole(t, roles, "acme", "device-onboarder", permission.DevicesOnboard),
	}}
	authz := rbac.New(roles)
	args := map[string]any{
		"resource": permission.Resource(permission.DevicesOnboard),
		"action":   permission.Action(permission.DevicesOnboard),
	}
	for _, tc := range []struct {
		requestTenant string
		want          bool
	}{{"acme", true}, {"globex", false}} {
		ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), dana), tc.requestTenant)
		resp, err := callAuthorize(ctx, t, authz, args)
		if err != nil || resp.Allowed != tc.want || (!tc.want && resp.Reason == "") {
			t.Fatalf("dana@acme devices:onboard in %s = %+v, %v; want allowed=%v with a reason on denial",
				tc.requestTenant, resp, err, tc.want)
		}
	}
}
