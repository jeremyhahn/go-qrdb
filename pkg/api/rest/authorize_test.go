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
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// authorizeServer is a REST server whose auth chain is g.
func authorizeServer(g auth.Authorizer) *Server {
	srv := newAuthServer(nil, nil, nil)
	srv.authMW = authmw.New(g)
	return srv
}

// postAuthorize dispatches POST /permissions/authorize as the caller on ctx.
func postAuthorize(ctx context.Context, t *testing.T, srv *Server, req authorizeRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/permissions/authorize", bytes.NewReader(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, r)
	return rec
}

// authorizeCaller authenticates subject in tenant t1 and targets t1.
func authorizeCaller(subject string) context.Context {
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), &auth.Identity{Subject: subject, TenantID: "t1"}), "t1")
}

func TestHandleAuthorize_AnswersForTheCaller(t *testing.T) {
	srv := authorizeServer(authtest.NewGrantAuthorizer(map[string][]string{"alice": {"kv:get"}}))
	for _, tc := range []struct {
		action string
		want   bool
	}{{"get", true}, {"put", false}} {
		rec := postAuthorize(authorizeCaller("alice"), t, srv, authorizeRequest{Resource: "kv", Action: tc.action})
		assertStatus(t, rec, http.StatusOK)
		var resp authorizeResponse
		decodeJSON(t, rec, &resp)
		if resp.Allowed != tc.want || (!tc.want && resp.Reason == "") {
			t.Fatalf("kv:%s = %+v, want allowed=%v with a reason on denial", tc.action, resp, tc.want)
		}
	}
}

func TestHandleAuthorize_RefusesAnUnauthenticatedCaller(t *testing.T) {
	srv := authorizeServer(authtest.NewGrantAuthorizer(nil))
	rec := postAuthorize(context.Background(), t, srv, authorizeRequest{Resource: "kv", Action: "get"})
	assertStatus(t, rec, http.StatusUnauthorized)
}

func TestHandleAuthorize_AnotherIdentityNeedsPermissionsResolve(t *testing.T) {
	g := authtest.NewGrantAuthorizer(map[string][]string{"bob": {"kv:get"}})
	srv := authorizeServer(g)
	rec := postAuthorize(authorizeCaller("alice"), t, srv, authorizeRequest{
		Identity: &authorizeIdentity{Subject: "bob", TenantID: "t1"}, Resource: "kv", Action: "get",
	})
	assertStatus(t, rec, http.StatusForbidden)
	if g.Evaluated("bob") {
		t.Fatal("bob was evaluated for a caller without permissions:resolve")
	}

	srv = authorizeServer(authtest.NewGrantAuthorizer(map[string][]string{"alice": {"permissions:resolve"}, "bob": {"kv:get"}}))
	rec = postAuthorize(authorizeCaller("alice"), t, srv, authorizeRequest{
		Identity: &authorizeIdentity{Subject: "bob", TenantID: "t1"}, Resource: "kv", Action: "get",
	})
	assertStatus(t, rec, http.StatusOK)
	var resp authorizeResponse
	decodeJSON(t, rec, &resp)
	if !resp.Allowed {
		t.Fatalf("bob kv:get = %+v, want allowed", resp)
	}
}

func TestHandleAuthorize_NeverAnswersForAnotherTenant(t *testing.T) {
	g := authtest.NewGrantAuthorizer(map[string][]string{"alice": {"permissions:resolve"}, "carol": {"kv:get"}})
	srv := authorizeServer(g)
	rec := postAuthorize(authorizeCaller("alice"), t, srv, authorizeRequest{
		Identity: &authorizeIdentity{Subject: "carol", TenantID: "t2"}, Resource: "kv", Action: "get",
	})
	if rec.Code == http.StatusOK {
		t.Fatal("a t1 caller got an answer about a t2 identity")
	}
	if g.Evaluated("carol") {
		t.Fatal("the other tenant's identity was evaluated")
	}
}

func TestHandleAuthorize_RejectsAMalformedBody(t *testing.T) {
	srv := authorizeServer(authtest.NewGrantAuthorizer(nil))
	r := httptest.NewRequest(http.MethodPost, "/permissions/authorize", bytes.NewReader([]byte("{"))).WithContext(authorizeCaller("alice"))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, r)
	assertStatus(t, rec, http.StatusBadRequest)
}

// TestHandleAuthorize_DevicesOnboard_TenantScoped answers through the real
// RBAC authorizer for a holder of acme's custom device-onboarder role: allowed
// in acme, refused with a reason in globex.
func TestHandleAuthorize_DevicesOnboard_TenantScoped(t *testing.T) {
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	dana := &auth.Identity{Subject: "dana", TenantID: "acme", Roles: []string{
		authtest.CustomRole(t, roles, "acme", "device-onboarder", permission.DevicesOnboard),
	}}
	srv := authorizeServer(rbac.New(roles))
	req := authorizeRequest{
		Resource: permission.Resource(permission.DevicesOnboard),
		Action:   permission.Action(permission.DevicesOnboard),
	}
	for _, tc := range []struct {
		requestTenant string
		want          bool
	}{{"acme", true}, {"globex", false}} {
		ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), dana), tc.requestTenant)
		rec := postAuthorize(ctx, t, srv, req)
		assertStatus(t, rec, http.StatusOK)
		var resp authorizeResponse
		decodeJSON(t, rec, &resp)
		if resp.Allowed != tc.want || (!tc.want && resp.Reason == "") {
			t.Fatalf("dana@acme devices:onboard in %s = %+v; want allowed=%v with a reason on denial",
				tc.requestTenant, resp, tc.want)
		}
	}
}
