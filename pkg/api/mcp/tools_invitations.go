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

package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// EnrollPath is the PUBLIC MCP endpoint invitation redemption is served on.
// It is a separate MCP server from /mcp, carrying exactly one tool
// (qrdb_invitation_redeem), because /mcp answers nothing -- not even
// initialize or tools/list -- to an unauthenticated caller, and an invited
// user holds no credential yet. Nothing else is reachable here.
const EnrollPath = "/mcp/enroll"

// Tool names of invitation self-enrolment.
const (
	toolNameUserInvite       = "qrdb_user_invite"
	toolNameInvitationRedeem = "qrdb_invitation_redeem"
)

// invitationTools returns the authenticated invite tool served on /mcp.
func invitationTools(svc service.InvitationServicer, mw *authmw.AuthMiddleware, roles role.RoleServicer) []serverTool {
	return []serverTool{
		{Tool: toolUserInvite(), Perm: permission.UsersInvite, Handler: handleUserInvite(svc, mw, roles)},
	}
}

// invitationResult carries the one-time token, returned only here.
type invitationResult struct {
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

// toolUserInvite defines the qrdb_user_invite tool schema.
func toolUserInvite() mcp.Tool {
	return mcp.NewTool(toolNameUserInvite,
		mcp.WithDescription("Invite a user into your own tenant: the account is created pending (or, given only its username, an existing pending account is re-invited) and a single-use, short-lived enrolment token is returned once. Deliver the token to the user out of band; they redeem it with a key of their own. You never hold a credential for the account."),
		mcp.WithString("username", mcp.Required(), mcp.Description("The account to invite.")),
		mcp.WithString("email", mcp.Description("Optional email login key for a new account.")),
		mcp.WithString("name", mcp.Description("Optional display name for a new account.")),
		mcp.WithString("role_ids", mcp.Description("Optional comma-separated role ids for a new account.")),
		mcp.WithString("key_pin_fingerprint", mcp.Description("Optional: pin the invitation to the invited user's key -- the base64url SHA-256 of its SubjectPublicKeyInfo, sent by the user out of band. Only that key can redeem it.")),
	)
}

func handleUserInvite(svc service.InvitationServicer, mw *authmw.AuthMiddleware, roles role.RoleServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}
		roleIDs := splitRoleIDs(mcp.ParseString(req, "role_ids", ""))
		tenantID := callerTenant(ctx)
		if err := authorizeCertTenantTool(ctx, mw, tenantID, permission.UsersInvite); err != nil {
			return nil, &ToolError{Tool: toolNameUserInvite, Err: err}
		}
		if err := mw.AuthorizeRoleAssignmentWriteOnCreate(ctx, roles, tenantID, roleIDs); err != nil {
			return nil, &ToolError{Tool: toolNameUserInvite, Err: err}
		}
		inv, err := svc.InviteUser(ctx, &service.InviteUserRequest{
			TenantID:          tenantID,
			Username:          username,
			Email:             mcp.ParseString(req, "email", ""),
			Name:              mcp.ParseString(req, "name", ""),
			RoleIDs:           roleIDs,
			KeyPinFingerprint: mcp.ParseString(req, "key_pin_fingerprint", ""),
		})
		if err != nil {
			return nil, &ToolError{Tool: toolNameUserInvite, Err: err}
		}
		data, err := json.Marshal(invitationResult{
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
			return nil, &ToolError{Tool: toolNameUserInvite, Err: err}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// splitRoleIDs parses a comma-separated role id list, dropping blanks.
func splitRoleIDs(s string) []string {
	var out []string
	for _, id := range strings.Split(s, ",") {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// toolInvitationRedeem defines the qrdb_invitation_redeem tool schema.
func toolInvitationRedeem() mcp.Tool {
	return mcp.NewTool(toolNameInvitationRedeem,
		mcp.WithDescription("Redeem a one-time invitation with a key of your own. Present the token, your tenant, and a certificate signing request for your key; you receive a certificate signed by the tenant's CA and your account is activated. To use your own certificate instead, also present it (it must chain to the tenant's trust anchors and be over the CSR's key). No credential is needed: the token is the authority."),
		mcp.WithString("tenant_id", mcp.Required(), mcp.Description("The tenant the invitation was issued in.")),
		mcp.WithString("token", mcp.Required(), mcp.Description("The one-time invitation token.")),
		mcp.WithString("csr_pem", mcp.Required(), mcp.Description("A PEM certificate signing request for your own key: it proves you hold the key.")),
		mcp.WithString("cert_pem", mcp.Description("Optional: your own PEM certificate over the CSR's key, leaf first.")),
		mcp.WithString("chain_pem", mcp.Description("Optional PEM intermediates for cert_pem.")),
	)
}

func handleInvitationRedeem(svc service.InvitationServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		token, _, err := stringArg(req, "token")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		enrolled, err := svc.RedeemInvitation(ctx, &service.RedeemInvitationRequest{
			TenantID: mcp.ParseString(req, "tenant_id", ""),
			Token:    token,
			CSRPEM:   []byte(mcp.ParseString(req, "csr_pem", "")),
			CertPEM:  []byte(mcp.ParseString(req, "cert_pem", "")),
			ChainPEM: []byte(mcp.ParseString(req, "chain_pem", "")),
		})
		if err != nil {
			return nil, &ToolError{Tool: toolNameInvitationRedeem, Err: err}
		}
		return enrolledResult(toolNameInvitationRedeem, enrolled)
	}
}

// newEnrollServer builds the PUBLIC enrolment MCP server served on
// EnrollPath: one tool, no authentication, the same error coding as /mcp and
// the sealed-window gate when this is a sealed listener.
func (s *Server) newEnrollServer() *mcpserver.MCPServer {
	opts := []mcpserver.ServerOption{
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithToolHandlerMiddleware(errorCodeMiddleware()),
	}
	if s.sealedWindowProbe != nil {
		opts = append(opts, mcpserver.WithToolHandlerMiddleware(s.sealedWindowMiddleware()))
	}
	srv := mcpserver.NewMCPServer("qrdb-enroll", "1.0.0", opts...)
	srv.AddTool(toolInvitationRedeem(), handleInvitationRedeem(s.invitations))
	return srv
}
