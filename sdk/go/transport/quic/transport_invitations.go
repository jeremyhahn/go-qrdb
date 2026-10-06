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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface check.
var _ transport.InvitationClient = (*Transport)(nil)

// QUIC frame methods for invitation self-enrolment. These are the client's
// own copy of the wire contract pkg/api/quic/handlers_invitations.go
// declares server-side; the SDK does not import the server package.
const (
	methodUserInvite       = "user.Invite"
	methodRedeemInvitation = "invitation.Redeem"
)

// InviteUser issues a one-time enrolment token for a pending account of the
// caller's own tenant, via the QUIC user.Invite method.
func (t *Transport) InviteUser(ctx context.Context, req *transport.InviteUserRequest) (*transport.Invitation, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "InviteUser", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, methodUserInvite, payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "InviteUser", Err: err}
	}

	var inv transport.Invitation
	if err := json.Unmarshal(data, &inv); err != nil {
		return nil, &transport.RequestError{Method: "InviteUser", Err: &UnmarshalError{Err: err}}
	}
	return &inv, nil
}

// RedeemInvitation redeems a one-time token via the PUBLIC invitation.Redeem
// frame method -- the one QUIC method with no registered permission at all
// (QUICMethodRegistry.RegisterPublic). It dials its own connection when none
// exists yet rather than requiring Connect first (see
// transport.InvitationClient): Connect's health probe fails closed for an
// uncredentialed caller (see reachableButUnauthorized), which is exactly
// what the redeemer is.
func (t *Transport) RedeemInvitation(ctx context.Context, req *transport.RedeemInvitationRequest) (*transport.EnrolledCert, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if err := t.ensureConn(ctx); err != nil {
		return nil, err
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "RedeemInvitation", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, methodRedeemInvitation, payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "RedeemInvitation", Err: err}
	}

	var out transport.EnrolledCert
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, &transport.RequestError{Method: "RedeemInvitation", Err: &UnmarshalError{Err: err}}
	}
	return &out, nil
}
