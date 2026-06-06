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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// GetRoleByName retrieves a role by its tenant-scoped name.
func (t *Transport) GetRoleByName(ctx context.Context, req *transport.GetRoleByNameRequest) (*transport.Role, error) {
	if t.roleSvc == nil {
		return nil, &transport.NotImplementedError{Method: "GetRoleByName"}
	}
	rl, err := t.roleSvc.GetRoleByName(ctx, req.TenantID, req.Name)
	if err != nil {
		return nil, err
	}
	return roleToTransport(rl), nil
}

// ResolvePermissions returns the union of effective permissions for the
// given role IDs (sorted, deduplicated).
func (t *Transport) ResolvePermissions(ctx context.Context, req *transport.ResolvePermissionsRequest) (*transport.ResolvePermissionsResponse, error) {
	if t.roleSvc == nil {
		return nil, &transport.NotImplementedError{Method: "ResolvePermissions"}
	}
	perms, err := t.roleSvc.ResolvePermissions(ctx, req.RoleIDs)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = p.String()
	}
	return &transport.ResolvePermissionsResponse{Permissions: out}, nil
}
