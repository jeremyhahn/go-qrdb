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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface check.
var _ transport.InvitationClient = (*Transport)(nil)

// InviteUser implements transport.InvitationClient. It is gated and
// authorized like every other authenticated write: a sealed barrier or an
// unauthorized caller refuses it before the service is ever reached.
func (t *Transport) InviteUser(ctx context.Context, req *transport.InviteUserRequest) (*transport.Invitation, error) {
	ctx, err := t.admit(ctx, "InviteUser")
	if err != nil {
		return nil, err
	}
	if t.invitations == nil {
		return nil, &transport.NotImplementedError{Method: "InviteUser"}
	}
	tenantID, err := tenant.Confine(ctx, "", "invite a user")
	if err != nil {
		return nil, err
	}
	if err := t.authorizeTenant(ctx, tenantID, permission.UsersInvite); err != nil {
		return nil, err
	}
	inv, err := t.invitations.InviteUser(ctx, &service.InviteUserRequest{
		TenantID:          tenantID,
		Username:          req.Username,
		Email:             req.Email,
		Name:              req.Name,
		RoleIDs:           req.RoleIDs,
		KeyPinFingerprint: req.KeyPinFingerprint,
	})
	if err != nil {
		return nil, err
	}
	return invitationToTransport(inv), nil
}

// RedeemInvitation implements transport.InvitationClient. It still runs
// t.gate (a sealed barrier can serve no invitation data, so it refuses with
// *BarrierSealedError exactly like every other operation), but carries NO
// identity and NO authorization check beyond that -- the token is the
// authority, exactly as every other transport's redemption route skips
// authentication and authorization for this one verb. The service itself
// collapses every refusal before the token is spent into one opaque error
// (service.InvitationInvalidError).
func (t *Transport) RedeemInvitation(ctx context.Context, req *transport.RedeemInvitationRequest) (*transport.EnrolledCert, error) {
	ctx, err := t.admit(ctx, "RedeemInvitation")
	if err != nil {
		return nil, err
	}
	if t.invitations == nil {
		return nil, &transport.NotImplementedError{Method: "RedeemInvitation"}
	}
	// A fixed source, exactly as CeremonyInitialize/CeremonySignCSR inject
	// one: there is no network peer to bucket redemption throttling by, and
	// every in-process caller of this host shares one bucket, same as them.
	ctx = auth.WithRemoteAddr(ctx, "embedded")
	enrolled, err := t.invitations.RedeemInvitation(ctx, &service.RedeemInvitationRequest{
		TenantID: req.TenantID,
		Token:    req.Token,
		CSRPEM:   []byte(req.CSRPEM),
		CertPEM:  []byte(req.CertPEM),
		ChainPEM: []byte(req.ChainPEM),
	})
	if err != nil {
		return nil, err
	}
	return enrolledToTransport(enrolled), nil
}

// invitationToTransport converts a service.Invitation to the wire type.
func invitationToTransport(inv *service.Invitation) *transport.Invitation {
	return &transport.Invitation{
		ID:                inv.ID,
		Token:             inv.Token,
		ExpiresAt:         inv.ExpiresAt,
		UserID:            inv.UserID,
		Username:          inv.Username,
		TenantID:          inv.TenantID,
		Created:           inv.Created,
		KeyPinFingerprint: inv.KeyPinFingerprint,
	}
}
