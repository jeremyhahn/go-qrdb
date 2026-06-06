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

// getRoleByNameWireRequest is the JSON wire payload for role.GetByName.
type getRoleByNameWireRequest struct {
	TenantID string `json:"tenantID"`
	Name     string `json:"name"`
}

// resolvePermissionsWireRequest is the JSON wire payload for
// role.ResolvePermissions.
type resolvePermissionsWireRequest struct {
	RoleIDs []string `json:"roleIDs"`
}

// resolvePermissionsWireResponse is the JSON wire payload returned by
// role.ResolvePermissions.
type resolvePermissionsWireResponse struct {
	Permissions []string `json:"permissions"`
}

// GetRoleByName retrieves a role by its tenant-scoped name.
func (t *Transport) GetRoleByName(ctx context.Context, req *transport.GetRoleByNameRequest) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(getRoleByNameWireRequest{TenantID: req.TenantID, Name: req.Name})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetRoleByName", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "role.GetByName", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetRoleByName", Err: err}
	}

	var wire roleWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "GetRoleByName", Err: &UnmarshalError{Err: err}}
	}

	return wireToRole(&wire), nil
}

// ResolvePermissions returns the union of effective permissions for the
// given role IDs (sorted, deduplicated).
func (t *Transport) ResolvePermissions(ctx context.Context, req *transport.ResolvePermissionsRequest) (*transport.ResolvePermissionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(resolvePermissionsWireRequest{RoleIDs: req.RoleIDs})
	if err != nil {
		return nil, &transport.RequestError{Method: "ResolvePermissions", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "role.ResolvePermissions", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "ResolvePermissions", Err: err}
	}

	var wire resolvePermissionsWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "ResolvePermissions", Err: &UnmarshalError{Err: err}}
	}

	return &transport.ResolvePermissionsResponse{Permissions: wire.Permissions}, nil
}
