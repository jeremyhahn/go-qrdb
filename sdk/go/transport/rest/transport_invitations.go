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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface check.
var _ transport.InvitationClient = (*Transport)(nil)

// InviteUser issues a one-time enrolment token for a pending account of the
// caller's own tenant, via POST /users/invite. req's JSON tags already match
// the server's wire contract, so it is marshaled directly with no
// transport-specific wire type.
func (t *Transport) InviteUser(ctx context.Context, req *transport.InviteUserRequest) (*transport.Invitation, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var inv transport.Invitation
	if err := t.postJSONDecode(ctx, "InviteUser", "/users/invite", req, &inv); err != nil {
		return nil, err
	}
	return &inv, nil
}

// RedeemInvitation redeems a one-time token via the PUBLIC
// POST /invitations/redeem, binding a certificate over the redeemer's own
// key to the invited account. It builds its own HTTP client when none
// exists yet rather than requiring Connect first (see
// transport.InvitationClient): the redeemer holds no credential to connect
// with, and this is the one call a transport serves to an unconnected
// caller.
func (t *Transport) RedeemInvitation(ctx context.Context, req *transport.RedeemInvitationRequest) (*transport.EnrolledCert, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if err := t.ensureClient(); err != nil {
		return nil, err
	}

	var enrolled transport.EnrolledCert
	if err := t.postJSONDecode(ctx, "RedeemInvitation", "/invitations/redeem", req, &enrolled); err != nil {
		return nil, err
	}
	return &enrolled, nil
}
