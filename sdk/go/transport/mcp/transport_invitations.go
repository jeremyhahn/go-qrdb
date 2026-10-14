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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface check.
var _ transport.InvitationClient = (*Transport)(nil)

// Tool names of invitation self-enrolment. These are the client's own copy
// of the wire contract pkg/api/mcp/tools_invitations.go declares
// server-side; the SDK does not import the server package.
const (
	toolUserInvite       = "qrdb_user_invite"
	toolInvitationRedeem = "qrdb_invitation_redeem"
)

// enrollPathSuffix is appended to this transport's configured Address to
// reach the PUBLIC enrolment endpoint invitation redemption is served on --
// a separate MCP server carrying exactly one tool, because the main
// endpoint answers nothing (not even initialize) to an unauthenticated
// caller. See pkg/api/mcp.EnrollPath.
const enrollPathSuffix = "/enroll"

// enrollAddress derives the public enrolment endpoint from this transport's
// configured Address: "https://host/mcp" becomes "https://host/mcp/enroll".
func enrollAddress(address string) string {
	return strings.TrimSuffix(address, "/") + enrollPathSuffix
}

// InviteUser issues a one-time enrolment token for a pending account of the
// caller's own tenant, via the qrdb_user_invite tool on the main
// (authenticated) MCP endpoint. The tool's JSON result tags already match
// transport.Invitation, so it is decoded directly with no wire-specific type.
func (t *Transport) InviteUser(ctx context.Context, req *transport.InviteUserRequest) (*transport.Invitation, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{"username": req.Username}
	if req.Email != "" {
		args["email"] = req.Email
	}
	if req.Name != "" {
		args["name"] = req.Name
	}
	if len(req.RoleIDs) > 0 {
		args["role_ids"] = strings.Join(req.RoleIDs, ",")
	}
	if req.KeyPinFingerprint != "" {
		args["key_pin_fingerprint"] = req.KeyPinFingerprint
	}

	text, err := t.callTool(ctx, toolUserInvite, args)
	if err != nil {
		return nil, err
	}

	var inv transport.Invitation
	if err := json.Unmarshal([]byte(text), &inv); err != nil {
		return nil, &transport.RequestError{Method: "InviteUser", Err: &jsonDecodeError{Err: err}}
	}
	return &inv, nil
}

// RedeemInvitation redeems a one-time token via the SEPARATE, public
// enrolment MCP server's one tool (qrdb_invitation_redeem). It dials its own
// client against that endpoint rather than requiring Connect first (see
// transport.InvitationClient): the redeemer holds no credential, and the
// main endpoint this transport's Connect dials answers nothing -- not even
// initialize -- to an unauthenticated caller.
func (t *Transport) RedeemInvitation(ctx context.Context, req *transport.RedeemInvitationRequest) (*transport.EnrolledCert, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	client, err := t.dialMCP(ctx, enrollAddress(t.config.Address), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()

	args := map[string]any{
		"tenant_id": req.TenantID,
		"token":     req.Token,
		"csr_pem":   req.CSRPEM,
	}
	if req.CertPEM != "" {
		args["cert_pem"] = req.CertPEM
	}
	if req.ChainPEM != "" {
		args["chain_pem"] = req.ChainPEM
	}

	callCtx, cancel := t.callContext(ctx)
	text, err := callToolOn(callCtx, client, toolInvitationRedeem, args)
	cancel()
	if err != nil {
		return nil, err
	}

	var out transport.EnrolledCert
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, &transport.RequestError{Method: "RedeemInvitation", Err: &jsonDecodeError{Err: err}}
	}
	return &out, nil
}
