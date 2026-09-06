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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// serverReconcileReport mirrors role.ReconcileReport for JSON decoding.
type serverReconcileReport struct {
	TenantID string `json:"tenantID"`
	Roles    []struct {
		RoleID  string   `json:"roleID"`
		Name    string   `json:"name"`
		Created bool     `json:"created"`
		Added   []string `json:"added"`
		Removed []string `json:"removed"`
	} `json:"roles"`
}

// ReconcileBuiltInRoles converges the tenant's stored built-in roles onto the
// server build's definitions via the qrdb_role_reconcile_builtin tool.
func (t *Transport) ReconcileBuiltInRoles(ctx context.Context, req *transport.ReconcileRolesRequest) (*transport.ReconcileRolesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{}
	if req != nil && req.TenantID != "" {
		args["tenant_id"] = req.TenantID
	}

	text, err := t.callTool(ctx, "qrdb_role_reconcile_builtin", args)
	if err != nil {
		return nil, err
	}

	var raw serverReconcileReport
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ReconcileBuiltInRoles", Err: &jsonDecodeError{Err: jsonErr}}
	}

	out := &transport.ReconcileRolesResponse{TenantID: raw.TenantID}
	for _, r := range raw.Roles {
		out.Roles = append(out.Roles, transport.RoleReconcileResult{
			RoleID:  r.RoleID,
			Name:    r.Name,
			Created: r.Created,
			Added:   r.Added,
			Removed: r.Removed,
		})
	}
	return out, nil
}
