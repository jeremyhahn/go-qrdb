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

// GetRoleByName retrieves a role by its tenant-scoped name via the
// qrdb_role_get_by_name tool.
func (t *Transport) GetRoleByName(ctx context.Context, req *transport.GetRoleByNameRequest) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{"name": req.Name}
	if req.TenantID != "" {
		args["tenant_id"] = req.TenantID
	}

	text, err := t.callTool(ctx, "qrdb_role_get_by_name", args)
	if err != nil {
		if isToolNotFound(err) {
			return nil, &transport.RoleNotFoundError{ID: req.Name}
		}
		return nil, err
	}

	var raw serverRole
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetRoleByName", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverRoleToTransport(&raw), nil
}

// ResolvePermissions returns the union of effective permissions for the
// given role IDs via the qrdb_permissions_resolve tool.
func (t *Transport) ResolvePermissions(ctx context.Context, req *transport.ResolvePermissionsRequest) (*transport.ResolvePermissionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_permissions_resolve", map[string]any{
		"role_ids": strings.Join(req.RoleIDs, ","),
	})
	if err != nil {
		return nil, err
	}

	var raw struct {
		Permissions []string `json:"permissions"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ResolvePermissions", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.ResolvePermissionsResponse{Permissions: raw.Permissions}, nil
}
