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

package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// fakeInvitations is a faithful service.InvitationServicer double: each
// method returns exactly what was configured.
type fakeInvitations struct {
	inviteFn func(context.Context, *service.InviteUserRequest) (*service.Invitation, error)
	redeemFn func(context.Context, *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error)
}

var _ service.InvitationServicer = (*fakeInvitations)(nil)

func (f *fakeInvitations) InviteUser(ctx context.Context, req *service.InviteUserRequest) (*service.Invitation, error) {
	return f.inviteFn(ctx, req)
}

func (f *fakeInvitations) RedeemInvitation(ctx context.Context, req *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
	return f.redeemFn(ctx, req)
}

// refusingAuthenticator authenticates nobody: the caller presents nothing.
type refusingAuthenticator struct{}

func (refusingAuthenticator) Authenticate(context.Context) (*auth.Identity, error) {
	return nil, auth.ErrUnauthenticated
}

func (refusingAuthenticator) Name() string { return "refusing" }

// newInvitationServer is the full REST chain -- real RBAC, the given
// authenticator -- over inv, with the caller holding users:invite.
func newInvitationServer(inv service.InvitationServicer, authn auth.Authenticator) *Server {
	roles := &mockRoleService{
		resolvePermissionsFn: func(context.Context, []string) ([]permission.Permission, error) {
			return []permission.Permission{permission.UsersInvite}, nil
		},
	}
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithAuthenticator(authn))
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithInvitations(inv), WithRoleService(roles), WithAuthMiddleware(mw))
}

func TestRedeemInvitation_IsServedWithoutAuthentication(t *testing.T) {
	var got *service.RedeemInvitationRequest
	srv := newInvitationServer(&fakeInvitations{
		redeemFn: func(ctx context.Context, req *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
			if auth.IdentityFromContext(ctx) != nil {
				t.Fatal("the redemption carried an identity")
			}
			if _, ok := auth.RemoteAddrFromContext(ctx); !ok {
				t.Error("the redemption carried no source address to throttle by")
			}
			got = req
			return &service.EnrolledCertificate{UserID: 9, Username: "bob", TenantID: req.TenantID, CertPEM: []byte("pem"), Serial: "s", Thumbprint: "tp", NotAfter: time.Now().Add(time.Hour)}, nil
		},
	}, refusingAuthenticator{})

	req := httptest.NewRequest(http.MethodPost, "/invitations/redeem", strings.NewReader(`{"tenantID":"acme","token":"tok","csrPEM":"csr","certPEM":"c","chainPEM":"ch"}`))
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusCreated)
	if got.TenantID != "acme" || got.Token != "tok" || string(got.CSRPEM) != "csr" || string(got.CertPEM) != "c" || string(got.ChainPEM) != "ch" {
		t.Fatalf("service request = %+v", got)
	}
	var resp enrolledCertResponse
	decodeJSON(t, rec, &resp)
	if resp.Username != "bob" || resp.Serial != "s" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestRedeemInvitation_MapsTheOpaqueRefusalToUnauthorized(t *testing.T) {
	srv := newInvitationServer(&fakeInvitations{
		redeemFn: func(context.Context, *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
			return nil, &service.InvitationInvalidError{}
		},
	}, refusingAuthenticator{})
	req := httptest.NewRequest(http.MethodPost, "/invitations/redeem", strings.NewReader(`{"tenantID":"acme","token":"x","csrPEM":"c"}`))
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusUnauthorized)

	bad := httptest.NewRequest(http.MethodPost, "/invitations/redeem", strings.NewReader("{"))
	rec = httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, bad)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestInviteUser_RequiresAuthentication(t *testing.T) {
	srv := newInvitationServer(&fakeInvitations{
		inviteFn: func(context.Context, *service.InviteUserRequest) (*service.Invitation, error) {
			t.Fatal("an unauthenticated invite reached the service")
			return nil, nil
		},
	}, refusingAuthenticator{})
	req := httptest.NewRequest(http.MethodPost, "/users/invite", strings.NewReader(`{"username":"bob"}`))
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusUnauthorized)
}

func TestInviteUser_InvitesIntoTheCallersTenant(t *testing.T) {
	caller := &certScopeAuthenticator{}
	caller.as(certScopeTenantA, "a-admin")
	expires := time.Now().Add(time.Hour).UTC()
	var got *service.InviteUserRequest
	srv := newInvitationServer(&fakeInvitations{
		inviteFn: func(_ context.Context, req *service.InviteUserRequest) (*service.Invitation, error) {
			got = req
			return &service.Invitation{ID: 3, Token: "tok", ExpiresAt: expires, UserID: 9, Username: req.Username, TenantID: req.TenantID, Created: true}, nil
		},
	}, caller)
	req := httptest.NewRequest(http.MethodPost, "/users/invite", strings.NewReader(`{"username":"bob","email":"bob@example.com","name":"Bob"}`))
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusCreated)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("the response carrying the token may be cached")
	}
	if got.TenantID != certScopeTenantA || got.Username != "bob" || got.Email != "bob@example.com" || got.Name != "Bob" {
		t.Fatalf("service request = %+v", got)
	}
	var resp invitationResponse
	decodeJSON(t, rec, &resp)
	if resp.Token != "tok" || resp.UserID != 9 || !resp.Created || !resp.ExpiresAt.Equal(expires) {
		t.Fatalf("response = %+v", resp)
	}

	bad := httptest.NewRequest(http.MethodPost, "/users/invite", strings.NewReader("{"))
	rec = httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, bad)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestInvitationRoutes_NotImplementedWithoutTheService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	for _, h := range []http.HandlerFunc{srv.handleInviteUser, srv.handleRedeemInvitation} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
		assertStatus(t, rec, http.StatusNotImplemented)
	}
}
