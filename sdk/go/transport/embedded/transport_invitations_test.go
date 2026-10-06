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

package embedded

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// fakeInvitations is a faithful service.InvitationServicer double.
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

func TestInviteUser_InvitesIntoTheCallersTenant(t *testing.T) {
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	var got *service.InviteUserRequest
	tr := newAuthTransport(t, WithInvitations(&fakeInvitations{
		inviteFn: func(_ context.Context, req *service.InviteUserRequest) (*service.Invitation, error) {
			got = req
			return &service.Invitation{ID: 3, Token: "tok", ExpiresAt: expires, UserID: 9, Username: req.Username, TenantID: req.TenantID, Created: true}, nil
		},
	}))
	inv, err := tr.InviteUser(callerCtx("acme"), &transport.InviteUserRequest{Username: "bob", Email: "bob@example.com", Name: "Bob"})
	if err != nil {
		t.Fatalf("InviteUser: %v", err)
	}
	if got.TenantID != "acme" || got.Username != "bob" || got.Email != "bob@example.com" || got.Name != "Bob" {
		t.Fatalf("service request = %+v", got)
	}
	if inv.Token != "tok" || inv.ID != 3 || inv.UserID != 9 || !inv.Created || !inv.ExpiresAt.Equal(expires) || inv.TenantID != "acme" {
		t.Fatalf("invitation = %+v", inv)
	}
}

func TestInviteUser_Refusals(t *testing.T) {
	_, err := newAuthTransport(t).InviteUser(callerCtx("acme"), &transport.InviteUserRequest{Username: "bob"})
	assertNotImplementedError(t, err, "InviteUser")

	boom := errors.New("refused")
	tr := newAuthTransport(t, WithInvitations(&fakeInvitations{
		inviteFn: func(context.Context, *service.InviteUserRequest) (*service.Invitation, error) { return nil, boom },
	}))
	if _, err := tr.InviteUser(callerCtx("acme"), &transport.InviteUserRequest{Username: "bob"}); !errors.Is(err, boom) {
		t.Fatalf("service failure: %v", err)
	}
}

// TestInviteUser_PermissionDenied pins the authorizeTenant branch: a caller
// whose role grants nothing is refused before the invitation service is
// ever reached, exactly like every other tenant-scoped write.
func TestInviteUser_PermissionDenied(t *testing.T) {
	reached := false
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithInvitations(&fakeInvitations{
			inviteFn: func(context.Context, *service.InviteUserRequest) (*service.Invitation, error) {
				reached = true
				return nil, nil
			},
		}),
		WithAuthMiddleware(newGrantingAuthMW()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = tr.InviteUser(callerCtx("acme"), &transport.InviteUserRequest{Username: "bob"})
	if err == nil {
		t.Fatal("want denied, got nil")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("err = %v, want an authorization denial", err)
	}
	if reached {
		t.Error("invitation service was reached despite a denial")
	}
}

func TestRedeemInvitation_PassesTheRequestThrough(t *testing.T) {
	var got *service.RedeemInvitationRequest
	tr := newAuthTransport(t, WithInvitations(&fakeInvitations{
		redeemFn: func(_ context.Context, req *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
			got = req
			return &service.EnrolledCertificate{UserID: 9, Username: "bob", TenantID: req.TenantID, CertPEM: []byte("pem"), Serial: "s"}, nil
		},
	}))
	out, err := tr.RedeemInvitation(context.Background(), &transport.RedeemInvitationRequest{TenantID: "acme", Token: "tok", CSRPEM: "csr", CertPEM: "c", ChainPEM: "ch"})
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}
	if got.TenantID != "acme" || got.Token != "tok" || string(got.CSRPEM) != "csr" || string(got.CertPEM) != "c" || string(got.ChainPEM) != "ch" {
		t.Fatalf("service request = %+v", got)
	}
	if out.Username != "bob" || out.CertPEM != "pem" || out.Serial != "s" {
		t.Fatalf("enrolled = %+v", out)
	}
}

func TestRedeemInvitation_Refusals(t *testing.T) {
	_, err := newAuthTransport(t).RedeemInvitation(context.Background(), &transport.RedeemInvitationRequest{})
	assertNotImplementedError(t, err, "RedeemInvitation")

	invalid := &service.InvitationInvalidError{}
	tr := newAuthTransport(t, WithInvitations(&fakeInvitations{
		redeemFn: func(context.Context, *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
			return nil, invalid
		},
	}))
	if _, err := tr.RedeemInvitation(context.Background(), &transport.RedeemInvitationRequest{}); !errors.Is(err, invalid) {
		t.Fatalf("service failure: %v", err)
	}
}
