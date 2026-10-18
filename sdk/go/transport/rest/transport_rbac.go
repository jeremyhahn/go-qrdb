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

package rest

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.RBACProvider  = (*Transport)(nil)
	_ transport.RBACEndpoints = rbacEndpoints{}
)

// RBACClient returns the RBACClient over the server's REST endpoints.
func (t *Transport) RBACClient() transport.RBACClient {
	return transport.NewRemoteRBACClient(rbacEndpoints{t: t})
}

// rbacEndpoints adapts the REST transport to transport.RBACEndpoints.
type rbacEndpoints struct {
	t *Transport
}

// authorizeWireRequest is the POST /permissions/authorize body.
type authorizeWireRequest struct {
	Identity *authorizeWireIdentity `json:"identity,omitempty"`
	Resource string                 `json:"resource"`
	Action   string                 `json:"action"`
}

// authorizeWireIdentity is the identity an Authorize request asks about.
type authorizeWireIdentity struct {
	Subject  string   `json:"subject"`
	TenantID string   `json:"tenant_id"`
	Roles    []string `json:"roles,omitempty"`
}

// ownPermissionsWireResponse is the GET /permissions/own response.
type ownPermissionsWireResponse struct {
	Permissions []transport.PermissionInfo `json:"permissions"`
}

// ResolvePermissions maps to POST /roles/resolve-permissions.
func (e rbacEndpoints) ResolvePermissions(ctx context.Context, req *transport.ResolvePermissionsRequest) (*transport.ResolvePermissionsResponse, error) {
	return e.t.ResolvePermissions(ctx, req)
}

// ListOwnPermissions maps to GET /permissions/own.
func (e rbacEndpoints) ListOwnPermissions(ctx context.Context) ([]transport.PermissionInfo, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var resp ownPermissionsWireResponse
	if err := e.t.getJSONDecode(ctx, "ListOwnPermissions", "/permissions/own", &resp); err != nil {
		return nil, err
	}
	return resp.Permissions, nil
}

// EvaluateAuthorization maps to POST /permissions/authorize.
func (e rbacEndpoints) EvaluateAuthorization(ctx context.Context, req *transport.AuthorizeRequest) (*transport.AuthorizeDecision, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	body := authorizeWireRequest{Resource: req.Resource, Action: req.Action}
	if id := req.Identity; id != nil {
		body.Identity = &authorizeWireIdentity{Subject: id.Subject, TenantID: id.TenantID, Roles: id.Roles}
	}
	var decision transport.AuthorizeDecision
	if err := e.t.postJSONDecode(ctx, "Authorize", "/permissions/authorize", body, &decision); err != nil {
		return nil, err
	}
	return &decision, nil
}
