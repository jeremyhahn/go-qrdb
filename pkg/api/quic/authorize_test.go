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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// authorizeServer is a QUIC server whose auth chain is g.
func authorizeServer(g auth.Authorizer) *Server {
	srv := NewServer(nil, nil, "localhost:0")
	srv.authMW = authmw.New(g)
	return srv
}

// authorizeCaller authenticates subject in tenant t1 and targets t1.
func authorizeCaller(subject string) context.Context {
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), &auth.Identity{Subject: subject, TenantID: "t1"}), "t1")
}

// callAuthorize runs role.Authorize with req as the caller on ctx.
func callAuthorize(ctx context.Context, t *testing.T, srv *Server, req authorizeWireRequest) (authorizeWireResponse, error) {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out authorizeWireResponse
	data, err := srv.handleAuthorize(ctx, payload)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out, nil
}

func TestHandleAuthorize_AnswersForTheCaller(t *testing.T) {
	srv := authorizeServer(authtest.NewGrantAuthorizer(map[string][]string{"alice": {"kv:get"}}))
	resp, err := callAuthorize(authorizeCaller("alice"), t, srv, authorizeWireRequest{Resource: "kv", Action: "get"})
	if err != nil || !resp.Allowed {
		t.Fatalf("kv:get = %+v, %v; want allowed", resp, err)
	}
	resp, err = callAuthorize(authorizeCaller("alice"), t, srv, authorizeWireRequest{Resource: "kv", Action: "put"})
	if err != nil || resp.Allowed || resp.Reason == "" {
		t.Fatalf("kv:put = %+v, %v; want denied with a reason", resp, err)
	}
}

func TestHandleAuthorize_RefusesAnUnauthenticatedCaller(t *testing.T) {
	srv := authorizeServer(authtest.NewGrantAuthorizer(nil))
	_, err := callAuthorize(context.Background(), t, srv, authorizeWireRequest{Resource: "kv", Action: "get"})
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
}

func TestHandleAuthorize_AnotherIdentityNeedsPermissionsResolve(t *testing.T) {
	bob := authorizeWireRequest{Identity: &authorizeWireIdentity{Subject: "bob", TenantID: "t1"}, Resource: "kv", Action: "get"}
	g := authtest.NewGrantAuthorizer(map[string][]string{"bob": {"kv:get"}})
	if _, err := callAuthorize(authorizeCaller("alice"), t, authorizeServer(g), bob); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if g.Evaluated("bob") {
		t.Fatal("bob was evaluated for a caller without permissions:resolve")
	}

	g = authtest.NewGrantAuthorizer(map[string][]string{"alice": {"permissions:resolve"}, "bob": {"kv:get"}})
	resp, err := callAuthorize(authorizeCaller("alice"), t, authorizeServer(g), bob)
	if err != nil || !resp.Allowed {
		t.Fatalf("bob kv:get = %+v, %v; want allowed", resp, err)
	}
}

func TestHandleAuthorize_NeverAnswersForAnotherTenant(t *testing.T) {
	g := authtest.NewGrantAuthorizer(map[string][]string{"alice": {"permissions:resolve"}, "carol": {"kv:get"}})
	carol := authorizeWireRequest{Identity: &authorizeWireIdentity{Subject: "carol", TenantID: "t2"}, Resource: "kv", Action: "get"}
	if _, err := callAuthorize(authorizeCaller("alice"), t, authorizeServer(g), carol); err == nil {
		t.Fatal("a t1 caller got an answer about a t2 identity")
	}
	if g.Evaluated("carol") {
		t.Fatal("the other tenant's identity was evaluated")
	}
}

func TestHandleAuthorize_InvalidJSON(t *testing.T) {
	srv := authorizeServer(authtest.NewGrantAuthorizer(nil))
	_, err := srv.handleAuthorize(authorizeCaller("alice"), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %T %v, want UnmarshalError", err, err)
	}
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
	req := authorizeWireRequest{
		Resource: permission.Resource(permission.DevicesOnboard),
		Action:   permission.Action(permission.DevicesOnboard),
	}
	for _, tc := range []struct {
		requestTenant string
		want          bool
	}{{"acme", true}, {"globex", false}} {
		ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), dana), tc.requestTenant)
		resp, err := callAuthorize(ctx, t, srv, req)
		if err != nil || resp.Allowed != tc.want || (!tc.want && resp.Reason == "") {
			t.Fatalf("dana@acme devices:onboard in %s = %+v, %v; want allowed=%v with a reason on denial",
				tc.requestTenant, resp, err, tc.want)
		}
	}
}
