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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// fakeInvitations is a faithful service.InvitationServicer double: each
// method returns exactly what was configured and records its request.
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

func TestHandleInviteUser_InvitesIntoTheCallersTenant(t *testing.T) {
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	var got *service.InviteUserRequest
	srv := NewServer(nil, nil, "localhost:0", WithInvitations(&fakeInvitations{
		inviteFn: func(_ context.Context, req *service.InviteUserRequest) (*service.Invitation, error) {
			got = req
			return &service.Invitation{ID: 3, Token: "tok", ExpiresAt: expires, UserID: 9, Username: req.Username, TenantID: req.TenantID, Created: true}, nil
		},
	}))
	ctx := tenant.WithTenantID(context.Background(), "acme")
	out, err := srv.handleInviteUser(ctx, []byte(`{"username":"bob","email":"bob@example.com","name":"Bob"}`))
	if err != nil {
		t.Fatalf("handleInviteUser: %v", err)
	}
	if got.TenantID != "acme" || got.Username != "bob" || got.Email != "bob@example.com" || got.Name != "Bob" {
		t.Fatalf("service request = %+v", got)
	}
	var resp invitationWireResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token != "tok" || resp.UserID != 9 || resp.ID != 3 || !resp.Created || !resp.ExpiresAt.Equal(expires) {
		t.Fatalf("response = %+v", resp)
	}
}

func TestHandleInviteUser_Refusals(t *testing.T) {
	if _, err := NewServer(nil, nil, "localhost:0").handleInviteUser(context.Background(), nil); !errors.As(err, new(*ServiceNotConfiguredError)) {
		t.Fatalf("unconfigured: %v", err)
	}
	boom := errors.New("refused")
	srv := NewServer(nil, nil, "localhost:0", WithInvitations(&fakeInvitations{
		inviteFn: func(context.Context, *service.InviteUserRequest) (*service.Invitation, error) { return nil, boom },
	}))
	if _, err := srv.handleInviteUser(context.Background(), []byte("{")); !errors.As(err, new(*UnmarshalError)) {
		t.Fatalf("bad payload: %v", err)
	}
	if _, err := srv.handleInviteUser(context.Background(), []byte(`{"username":"bob"}`)); !errors.Is(err, boom) {
		t.Fatalf("service failure: %v", err)
	}
	// Roles name a grant, so they need a role service to be validated.
	if _, err := srv.handleInviteUser(context.Background(), []byte(`{"username":"bob","roleIDs":["r"]}`)); err == nil {
		t.Fatal("roles were accepted with no role service to validate them")
	}
}

func TestHandleRedeemInvitation_PassesTheRequestThrough(t *testing.T) {
	var got *service.RedeemInvitationRequest
	srv := NewServer(nil, nil, "localhost:0", WithInvitations(&fakeInvitations{
		redeemFn: func(ctx context.Context, req *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
			if auth.IdentityFromContext(ctx) != nil {
				t.Fatal("the redemption carried an identity")
			}
			got = req
			return &service.EnrolledCertificate{UserID: 9, Username: "bob", TenantID: req.TenantID, CertPEM: []byte("pem"), Serial: "s", Thumbprint: "tp"}, nil
		},
	}))
	out, err := srv.handleRedeemInvitation(context.Background(), []byte(`{"tenantID":"acme","token":"tok","csrPEM":"csr","certPEM":"c","chainPEM":"ch"}`))
	if err != nil {
		t.Fatalf("handleRedeemInvitation: %v", err)
	}
	if got.TenantID != "acme" || got.Token != "tok" || string(got.CSRPEM) != "csr" || string(got.CertPEM) != "c" || string(got.ChainPEM) != "ch" {
		t.Fatalf("service request = %+v", got)
	}
	var resp enrolledCertWireResponse
	if err := json.Unmarshal(out, &resp); err != nil || resp.Username != "bob" || resp.Serial != "s" {
		t.Fatalf("response = %+v, %v", resp, err)
	}
}

func TestHandleRedeemInvitation_Refusals(t *testing.T) {
	if _, err := NewServer(nil, nil, "localhost:0").handleRedeemInvitation(context.Background(), nil); !errors.As(err, new(*ServiceNotConfiguredError)) {
		t.Fatalf("unconfigured: %v", err)
	}
	invalid := &service.InvitationInvalidError{}
	srv := NewServer(nil, nil, "localhost:0", WithInvitations(&fakeInvitations{
		redeemFn: func(context.Context, *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
			return nil, invalid
		},
	}))
	if _, err := srv.handleRedeemInvitation(context.Background(), []byte("{")); !errors.As(err, new(*UnmarshalError)) {
		t.Fatalf("bad payload: %v", err)
	}
	if _, err := srv.handleRedeemInvitation(context.Background(), []byte(`{}`)); !errors.Is(err, invalid) {
		t.Fatalf("service failure: %v", err)
	}
}

// The redeem method is public and the invite method is permission-mapped,
// whether or not an invitation service is wired.
func TestInvitationMethods_Registration(t *testing.T) {
	for _, srv := range []*Server{
		NewServer(nil, nil, "localhost:0"),
		NewServer(nil, nil, "localhost:0", WithInvitations(&fakeInvitations{})),
	} {
		if !srv.methodPerms.IsPublic(publicverbs.Table[publicverbs.VerbInvitationRedeem].QUIC) || srv.methodPerms.IsPublic(methodInviteUser) {
			t.Fatal("public registration is wrong")
		}
		if !srv.methodPerms.Covered(publicverbs.Table[publicverbs.VerbInvitationRedeem].QUIC) || !srv.methodPerms.Covered(methodInviteUser) {
			t.Fatal("an invitation method is not covered")
		}
	}
	if _, served := NewServer(nil, nil, "localhost:0").handlers[publicverbs.Table[publicverbs.VerbInvitationRedeem].QUIC]; served {
		t.Fatal("redeem is dispatched with no invitation service")
	}
}
