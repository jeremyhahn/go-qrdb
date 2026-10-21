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

package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// authorizeCaller authenticates subject in tenant t1 and targets t1.
func authorizeCaller(subject string) context.Context {
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), &auth.Identity{Subject: subject, TenantID: "t1"}), "t1")
}

func authorizeServer(g auth.Authorizer) *qrdbServiceServer {
	return &qrdbServiceServer{authMW: middleware.New(g)}
}

func TestGRPC_Authorize_AnswersForTheCaller(t *testing.T) {
	srv := authorizeServer(authtest.NewGrantAuthorizer(map[string][]string{"alice": {"kv:get"}}))
	resp, err := srv.Authorize(authorizeCaller("alice"), &pb.AuthorizeRequest{Resource: "kv", Action: "get"})
	if err != nil || !resp.GetAllowed() {
		t.Fatalf("kv:get = %v, %v; want allowed", resp, err)
	}
	resp, err = srv.Authorize(authorizeCaller("alice"), &pb.AuthorizeRequest{Resource: "kv", Action: "put"})
	if err != nil || resp.GetAllowed() || resp.GetReason() == "" {
		t.Fatalf("kv:put = %v, %v; want denied with a reason", resp, err)
	}
}

func TestGRPC_Authorize_RefusesAnUnauthenticatedCaller(t *testing.T) {
	srv := authorizeServer(authtest.NewGrantAuthorizer(nil))
	_, err := srv.Authorize(context.Background(), &pb.AuthorizeRequest{Resource: "kv", Action: "get"})
	if code := status.Code(err); code != codes.Unauthenticated {
		t.Fatalf("code = %s (%v), want Unauthenticated", code, err)
	}
}

func TestGRPC_Authorize_AnotherIdentityNeedsPermissionsResolve(t *testing.T) {
	g := authtest.NewGrantAuthorizer(map[string][]string{"bob": {"kv:get"}})
	bob := &pb.AuthorizeRequest{Identity: &pb.AuthorizeIdentity{Subject: "bob", TenantId: "t1"}, Resource: "kv", Action: "get"}
	_, err := authorizeServer(g).Authorize(authorizeCaller("alice"), bob)
	if code := status.Code(err); code != codes.PermissionDenied {
		t.Fatalf("code = %s (%v), want PermissionDenied", code, err)
	}
	if g.Evaluated("bob") {
		t.Fatal("bob was evaluated for a caller without permissions:resolve")
	}

	g = authtest.NewGrantAuthorizer(map[string][]string{"alice": {"permissions:resolve"}, "bob": {"kv:get"}})
	resp, err := authorizeServer(g).Authorize(authorizeCaller("alice"), bob)
	if err != nil || !resp.GetAllowed() {
		t.Fatalf("bob kv:get = %v, %v; want allowed", resp, err)
	}
}

func TestGRPC_Authorize_NeverAnswersForAnotherTenant(t *testing.T) {
	g := authtest.NewGrantAuthorizer(map[string][]string{"alice": {"permissions:resolve"}, "carol": {"kv:get"}})
	carol := &pb.AuthorizeRequest{Identity: &pb.AuthorizeIdentity{Subject: "carol", TenantId: "t2"}, Resource: "kv", Action: "get"}
	if _, err := authorizeServer(g).Authorize(authorizeCaller("alice"), carol); err == nil {
		t.Fatal("a t1 caller got an answer about a t2 identity")
	}
	if g.Evaluated("carol") {
		t.Fatal("the other tenant's identity was evaluated")
	}
}

func TestGRPC_Authorize_IsIdentityGated(t *testing.T) {
	got, ok := middleware.LookupGRPCFromDescriptor(pb.QRDBService_Authorize_FullMethodName)
	if !ok || got != permission.IdentityGated {
		t.Fatalf("got (%q, %v), want (%q, true)", got, ok, permission.IdentityGated)
	}
}

// TestGRPC_Authorize_DevicesOnboard_TenantScoped answers through the real RBAC
// authorizer for a holder of acme's custom device-onboarder role: allowed in
// acme, refused with a reason in globex.
func TestGRPC_Authorize_DevicesOnboard_TenantScoped(t *testing.T) {
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	dana := &auth.Identity{Subject: "dana", TenantID: "acme", Roles: []string{
		authtest.CustomRole(t, roles, "acme", "device-onboarder", permission.DevicesOnboard),
	}}
	srv := authorizeServer(rbac.New(roles))
	req := &pb.AuthorizeRequest{
		Resource: permission.Resource(permission.DevicesOnboard),
		Action:   permission.Action(permission.DevicesOnboard),
	}
	for _, tc := range []struct {
		requestTenant string
		want          bool
	}{{"acme", true}, {"globex", false}} {
		ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), dana), tc.requestTenant)
		resp, err := srv.Authorize(ctx, req)
		if err != nil || resp.GetAllowed() != tc.want || (!tc.want && resp.GetReason() == "") {
			t.Fatalf("dana@acme devices:onboard in %s = %v, %v; want allowed=%v with a reason on denial",
				tc.requestTenant, resp, err, tc.want)
		}
	}
}
