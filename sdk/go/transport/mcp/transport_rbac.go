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

package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.RBACProvider  = (*Transport)(nil)
	_ transport.RBACEndpoints = rbacEndpoints{}
)

// RBACClient returns the RBACClient over the server's MCP permission tools.
func (t *Transport) RBACClient() transport.RBACClient {
	return transport.NewRemoteRBACClient(rbacEndpoints{t: t})
}

// rbacEndpoints adapts the MCP transport to transport.RBACEndpoints.
type rbacEndpoints struct {
	t *Transport
}

// ResolvePermissions maps to the qrdb_permissions_resolve tool.
func (e rbacEndpoints) ResolvePermissions(ctx context.Context, req *transport.ResolvePermissionsRequest) (*transport.ResolvePermissionsResponse, error) {
	return e.t.ResolvePermissions(ctx, req)
}

// ListOwnPermissions maps to the qrdb_permissions_own tool.
func (e rbacEndpoints) ListOwnPermissions(ctx context.Context) ([]transport.PermissionInfo, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	text, err := e.t.callTool(ctx, "qrdb_permissions_own", nil)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Permissions []transport.PermissionInfo `json:"permissions"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListOwnPermissions", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return raw.Permissions, nil
}

// EvaluateAuthorization maps to the qrdb_permissions_authorize tool.
func (e rbacEndpoints) EvaluateAuthorization(ctx context.Context, req *transport.AuthorizeRequest) (*transport.AuthorizeDecision, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	args := map[string]any{"resource": req.Resource, "action": req.Action}
	if id := req.Identity; id != nil {
		args["subject"] = id.Subject
		args["tenant_id"] = id.TenantID
		args["roles"] = strings.Join(id.Roles, ",")
	}
	text, err := e.t.callTool(ctx, "qrdb_permissions_authorize", args)
	if err != nil {
		return nil, err
	}
	var decision transport.AuthorizeDecision
	if jsonErr := json.Unmarshal([]byte(text), &decision); jsonErr != nil {
		return nil, &transport.RequestError{Method: "Authorize", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &decision, nil
}
