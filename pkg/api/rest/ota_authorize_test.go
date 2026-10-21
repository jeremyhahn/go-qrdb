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

package rest

import (
	"context"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestHandleAuthorize_OTACustomRole_TenantScoped drives POST
// /permissions/authorize over the real RBAC authorizer: a caller whose custom
// role in acme holds ota:activate or ota:confirm is answered allowed on acme
// and denied, with a reason, on globex; and a resolver in globex gets no
// answer about the acme holder at all.
func TestHandleAuthorize_OTACustomRole_TenantScoped(t *testing.T) {
	for _, perm := range []permission.Permission{permission.OTAActivate, permission.OTAConfirm} {
		roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
		srv := newAuthServer(nil, nil, nil)
		srv.authMW = authmw.New(rbac.New(roles))
		holder := authtest.Grant(t, roles, &auth.Identity{Subject: "ota-operator", TenantID: "acme"}, perm)
		resolverB := authtest.Grant(t, roles, &auth.Identity{Subject: "resolver", TenantID: "globex"}, permission.PermissionsResolve)
		req := authorizeRequest{Resource: permission.Resource(perm), Action: permission.Action(perm)}

		for _, tc := range []struct {
			routedTo string
			want     bool
		}{{"acme", true}, {"globex", false}} {
			ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), holder), tc.routedTo)
			rec := postAuthorize(ctx, t, srv, req)
			assertStatus(t, rec, http.StatusOK)
			var resp authorizeResponse
			decodeJSON(t, rec, &resp)
			if resp.Allowed != tc.want || (!tc.want && resp.Reason == "") {
				t.Fatalf("custom role@acme %s routed to %s = %+v, want allowed=%v with a reason on denial", perm, tc.routedTo, resp, tc.want)
			}
		}

		about := req
		about.Identity = &authorizeIdentity{Subject: holder.Subject, TenantID: holder.TenantID, Roles: holder.Roles}
		ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), resolverB), "globex")
		if rec := postAuthorize(ctx, t, srv, about); rec.Code == http.StatusOK {
			t.Fatalf("globex resolver got an answer about the acme holder's %s: %s", perm, rec.Body.String())
		}
	}
}
