// Copyright 2026 Jeremy Hahn
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

package unix

import (
	"context"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/enrollconv"
)

// Compile-time interface check.
var _ transport.InvitationClient = (*Transport)(nil)

// InviteUser creates a pending account in the caller's own tenant, or
// re-invites one, and returns its one-time enrolment token.
func (t *Transport) InviteUser(ctx context.Context, req *transport.InviteUserRequest) (*transport.Invitation, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.InviteUser(ctx, &pb.InviteUserRequest{
		Username:          req.Username,
		Email:             req.Email,
		Name:              req.Name,
		RoleIds:           req.RoleIDs,
		KeyPinFingerprint: req.KeyPinFingerprint,
	})
	if err != nil {
		return nil, mapGRPCError("InviteUser", err)
	}
	return enrollconv.Invitation(resp), nil
}

// RedeemInvitation is UNAUTHENTICATED: the token from InviteUser is the
// authority. The server registers this RPC as a public method, so no
// credential is sent or required for this one call. It dials its own
// connection when none exists yet rather than requiring Connect first (see
// transport.InvitationClient): the redeemer holds no credential to connect
// with, and this is the one call this transport serves to an unconnected
// caller.
func (t *Transport) RedeemInvitation(ctx context.Context, req *transport.RedeemInvitationRequest) (*transport.EnrolledCert, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if err := t.ensureClient(); err != nil {
		return nil, err
	}
	resp, err := t.client.RedeemInvitation(ctx, &pb.RedeemInvitationRequest{
		TenantId: req.TenantID,
		Token:    req.Token,
		CsrPem:   req.CSRPEM,
		CertPem:  req.CertPEM,
		ChainPem: req.ChainPEM,
	})
	if err != nil {
		return nil, mapGRPCError("RedeemInvitation", err)
	}
	return enrollconv.EnrolledCert(resp), nil
}
