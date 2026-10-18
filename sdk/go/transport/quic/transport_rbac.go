// Copyright (c) 2026 Jeremy Hahn
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

package quic

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.RBACProvider  = (*Transport)(nil)
	_ transport.RBACEndpoints = rbacEndpoints{}
)

// RBACClient returns the RBACClient over the server's QUIC role methods.
func (t *Transport) RBACClient() transport.RBACClient {
	return transport.NewRemoteRBACClient(rbacEndpoints{t: t})
}

// rbacEndpoints adapts the QUIC transport to transport.RBACEndpoints.
type rbacEndpoints struct {
	t *Transport
}

// authorizeWireRequest is the role.Authorize request body.
type authorizeWireRequest struct {
	Identity *authorizeWireIdentity `json:"identity,omitempty"`
	Resource string                 `json:"resource"`
	Action   string                 `json:"action"`
}

// authorizeWireIdentity is the identity a role.Authorize request asks about.
type authorizeWireIdentity struct {
	Subject  string   `json:"subject"`
	TenantID string   `json:"tenant_id"`
	Roles    []string `json:"roles,omitempty"`
}

// ownPermissionsWireResponse is the role.ListOwnPermissions response body.
type ownPermissionsWireResponse struct {
	Permissions []transport.PermissionInfo `json:"permissions"`
}

// ResolvePermissions maps to role.ResolvePermissions.
func (e rbacEndpoints) ResolvePermissions(ctx context.Context, req *transport.ResolvePermissionsRequest) (*transport.ResolvePermissionsResponse, error) {
	return e.t.ResolvePermissions(ctx, req)
}

// ListOwnPermissions maps to role.ListOwnPermissions.
func (e rbacEndpoints) ListOwnPermissions(ctx context.Context) ([]transport.PermissionInfo, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	data, err := e.t.call(ctx, "role.ListOwnPermissions", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListOwnPermissions", Err: err}
	}
	var wire ownPermissionsWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "ListOwnPermissions", Err: &UnmarshalError{Err: err}}
	}
	return wire.Permissions, nil
}

// EvaluateAuthorization maps to role.Authorize.
func (e rbacEndpoints) EvaluateAuthorization(ctx context.Context, req *transport.AuthorizeRequest) (*transport.AuthorizeDecision, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	body := authorizeWireRequest{Resource: req.Resource, Action: req.Action}
	if id := req.Identity; id != nil {
		body.Identity = &authorizeWireIdentity{Subject: id.Subject, TenantID: id.TenantID, Roles: id.Roles}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, &transport.RequestError{Method: "Authorize", Err: &MarshalError{Err: err}}
	}
	data, err := e.t.call(ctx, "role.Authorize", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "Authorize", Err: err}
	}
	var decision transport.AuthorizeDecision
	if err := json.Unmarshal(data, &decision); err != nil {
		return nil, &transport.RequestError{Method: "Authorize", Err: &UnmarshalError{Err: err}}
	}
	return &decision, nil
}
