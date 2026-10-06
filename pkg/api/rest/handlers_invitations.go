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
	"encoding/json"
	"net/http"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// inviteUserRequest is the JSON payload for POST /users/invite: an account
// to create pending in the caller's own tenant, or an existing pending one
// to re-invite (username alone).
type inviteUserRequest struct {
	Username string   `json:"username"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	RoleIDs  []string `json:"roleIDs,omitempty"`
	// KeyPinFingerprint, when set, pins the invitation to one key: the SPKI
	// thumbprint the invited user sent out of band.
	KeyPinFingerprint string `json:"keyPinFingerprint,omitempty"`
}

// invitationResponse carries the one-time token. It is the only time the
// token is ever returned.
type invitationResponse struct {
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

// redeemInvitationRequest is the JSON payload for POST /invitations/redeem.
type redeemInvitationRequest struct {
	TenantID string `json:"tenantID"`
	Token    string `json:"token"`
	CSRPEM   string `json:"csrPEM"`
	CertPEM  string `json:"certPEM,omitempty"`
	ChainPEM string `json:"chainPEM,omitempty"`
}

// handleInviteUser handles POST /users/invite -- creates a pending account
// in the caller's own tenant (or re-invites one) and returns its one-time
// enrolment token.
func (s *Server) handleInviteUser(w http.ResponseWriter, r *http.Request) {
	if s.invitations == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "invitations"})
		return
	}
	var req inviteUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	tenantID := callerTenant(ctx)
	if err := s.authorizeCertTenant(ctx, tenantID, permission.UsersInvite); err != nil {
		writeAuthzErrorJSON(w, err)
		return
	}
	if err := s.authMW.AuthorizeRoleAssignmentWriteOnCreate(ctx, s.roleSvc, tenantID, req.RoleIDs); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
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
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(invitationResponse{
		ID:                inv.ID,
		Token:             inv.Token,
		ExpiresAt:         inv.ExpiresAt,
		UserID:            inv.UserID,
		Username:          inv.Username,
		TenantID:          inv.TenantID,
		Created:           inv.Created,
		KeyPinFingerprint: inv.KeyPinFingerprint,
	})
}

// handleRedeemInvitation handles POST /invitations/redeem -- PUBLIC: the
// token is the authority. The redeemer's CSR proves it holds the key the
// certificate is issued for (or that a bring-your-own certificate is over).
func (s *Server) handleRedeemInvitation(w http.ResponseWriter, r *http.Request) {
	if s.invitations == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "invitations"})
		return
	}
	var req redeemInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	// The caller is unauthenticated, so the only identity this request
	// carries is its source address -- already injected unconditionally by
	// this server's remote-address middleware (middleware_remote_addr.go),
	// before authentication -- which the service draws its per-source
	// redemption throttle from (auth.RemoteAddrFromContext).
	enrolled, err := s.invitations.RedeemInvitation(ctx, &service.RedeemInvitationRequest{
		TenantID: req.TenantID,
		Token:    req.Token,
		CSRPEM:   []byte(req.CSRPEM),
		CertPEM:  []byte(req.CertPEM),
		ChainPEM: []byte(req.ChainPEM),
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(enrolledToResponse(enrolled))
}
