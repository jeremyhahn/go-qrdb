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
	"net/url"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// GetRoleByName retrieves a role by its tenant-scoped name. Maps to
// GET /roles/by-name?tenant_id=&name=.
func (t *Transport) GetRoleByName(ctx context.Context, req *transport.GetRoleByNameRequest) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	q := url.Values{}
	q.Set("name", req.Name)
	if req.TenantID != "" {
		q.Set("tenant_id", req.TenantID)
	}

	var role transport.Role
	if err := t.getJSONDecode(ctx, "GetRoleByName", "/roles/by-name?"+q.Encode(), &role); err != nil {
		return nil, err
	}
	return &role, nil
}

// ResolvePermissions returns the union of effective permissions for the
// given role IDs. Maps to POST /roles/resolve-permissions.
func (t *Transport) ResolvePermissions(ctx context.Context, req *transport.ResolvePermissionsRequest) (*transport.ResolvePermissionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	body := map[string][]string{"role_ids": req.RoleIDs}
	var resp transport.ResolvePermissionsResponse
	if err := t.postJSONDecode(ctx, "ResolvePermissions", "/roles/resolve-permissions", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
