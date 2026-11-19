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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// methodInviteUser is the frame method of invitation issuance. Redemption,
// the one PUBLIC invitation method, is named by publicverbs.Table: an invited
// user holds no credential yet, so the token is the authority (see
// handleStream).
const methodInviteUser = "user.Invite"

// inviteUserWireRequest is the JSON wire payload for user.Invite.
type inviteUserWireRequest struct {
	Username string   `json:"username"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	RoleIDs  []string `json:"roleIDs,omitempty"`
	// KeyPinFingerprint, when set, pins the invitation to one key: the SPKI
	// thumbprint the invited user sent out of band.
	KeyPinFingerprint string `json:"keyPinFingerprint,omitempty"`
}

// invitationWireResponse carries the one-time token, returned only here.
type invitationWireResponse struct {
	ID        uint64    `json:"id,string"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	UserID    uint64    `json:"userID,string"`
	Username  string    `json:"username"`
	TenantID  string    `json:"tenantID"`
	Created   bool      `json:"created"`
	// KeyPinFingerprint is the key the invitation is pinned to; empty for
	// an unpinned invitation.
	KeyPinFingerprint string `json:"keyPinFingerprint,omitempty"`
}

// redeemInvitationWireRequest is the JSON wire payload for invitation.Redeem.
type redeemInvitationWireRequest struct {
	TenantID string `json:"tenantID"`
	Token    string `json:"token"`
	CSRPEM   string `json:"csrPEM"`
	CertPEM  string `json:"certPEM,omitempty"`
	ChainPEM string `json:"chainPEM,omitempty"`
}

// handleInviteUser creates a pending account in the caller's own tenant, or
// re-invites one, and returns its one-time enrolment token.
func (s *Server) handleInviteUser(ctx context.Context, payload []byte) ([]byte, error) {
	if s.invitations == nil {
		return nil, &ServiceNotConfiguredError{Service: "invitations"}
	}
	var req inviteUserWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	tenantID := callerTenant(ctx)
	if err := s.authorizeCertTenant(ctx, tenantID, permission.UsersInvite); err != nil {
		return nil, err
	}
	if err := s.authMW.AuthorizeRoleAssignmentWriteOnCreate(ctx, s.roleSvc, tenantID, req.RoleIDs); err != nil {
		return nil, err
	}
	inv, err := s.invitations.InviteUser(ctx, &service.InviteUserRequest{
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
	out, err := json.Marshal(invitationWireResponse{
		ID:                inv.ID,
		Token:             inv.Token,
		ExpiresAt:         inv.ExpiresAt,
		UserID:            inv.UserID,
		Username:          inv.Username,
		TenantID:          inv.TenantID,
		Created:           inv.Created,
		KeyPinFingerprint: inv.KeyPinFingerprint,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleRedeemInvitation redeems a one-time token with the redeemer's own
// key. It is PUBLIC: handleStream neither authenticates nor authorizes it.
func (s *Server) handleRedeemInvitation(ctx context.Context, payload []byte) ([]byte, error) {
	if s.invitations == nil {
		return nil, &ServiceNotConfiguredError{Service: "invitations"}
	}
	var req redeemInvitationWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	enrolled, err := s.invitations.RedeemInvitation(ctx, &service.RedeemInvitationRequest{
		TenantID: req.TenantID,
		Token:    req.Token,
		CSRPEM:   []byte(req.CSRPEM),
		CertPEM:  []byte(req.CertPEM),
		ChainPEM: []byte(req.ChainPEM),
	})
	if err != nil {
		return nil, err
	}
	return marshalEnrolled(enrolled)
}
