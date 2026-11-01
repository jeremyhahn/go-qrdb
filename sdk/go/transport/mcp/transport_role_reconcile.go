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

	return raw.toTransport(), nil
}

// toTransport converts the server's report to its transport representation.
func (raw serverReconcileReport) toTransport() *transport.ReconcileRolesResponse {
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
	return out
}

// serverAllTenantsReconcileReport mirrors the JSON the server's
// qrdb_role_reconcile_builtin_all_tenants tool returns
// (role.AllTenantsReconcileReport).
type serverAllTenantsReconcileReport struct {
	Tenants []serverReconcileReport `json:"tenants"`
	Skipped []struct {
		TenantID string `json:"tenantID"`
		Reason   string `json:"reason"`
		Detail   string `json:"detail"`
	} `json:"skipped"`
	Failed []struct {
		TenantID string `json:"tenantID"`
		Error    string `json:"error"`
	} `json:"failed"`
}

// ReconcileBuiltInRolesAllTenants converges the stored built-in roles of every
// tenant on the server onto the server build's definitions. The caller must be
// a platform operator holding roles:reconcile_all.
func (t *Transport) ReconcileBuiltInRolesAllTenants(ctx context.Context) (*transport.ReconcileAllTenantsRolesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_role_reconcile_builtin_all_tenants", map[string]any{})
	if err != nil {
		return nil, err
	}

	var raw serverAllTenantsReconcileReport
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ReconcileBuiltInRolesAllTenants", Err: &jsonDecodeError{Err: jsonErr}}
	}

	out := &transport.ReconcileAllTenantsRolesResponse{}
	for _, tr := range raw.Tenants {
		out.Tenants = append(out.Tenants, *tr.toTransport())
	}
	for _, sk := range raw.Skipped {
		out.Skipped = append(out.Skipped, transport.RoleReconcileSkippedTenant{TenantID: sk.TenantID, Reason: sk.Reason, Detail: sk.Detail})
	}
	for _, f := range raw.Failed {
		out.Failed = append(out.Failed, transport.RoleReconcileFailedTenant{TenantID: f.TenantID, Error: f.Error})
	}
	return out, nil
}
