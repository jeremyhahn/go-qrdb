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

// reconcileRolesWireRequest is the JSON wire payload for
// role.ReconcileBuiltIn.
type reconcileRolesWireRequest struct {
	TenantID string `json:"tenantID"`
}

// roleReconcileResultWire is the JSON wire representation of one built-in
// role's reconciliation outcome.
type roleReconcileResultWire struct {
	RoleID  string   `json:"roleID"`
	Name    string   `json:"name"`
	Created bool     `json:"created"`
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
}

// reconcileRolesWireResponse is the JSON wire payload returned by
// role.ReconcileBuiltIn.
type reconcileRolesWireResponse struct {
	TenantID string                    `json:"tenantID"`
	Roles    []roleReconcileResultWire `json:"roles,omitempty"`
}

// ReconcileBuiltInRoles converges the tenant's stored built-in roles onto the
// server build's definitions.
func (t *Transport) ReconcileBuiltInRoles(ctx context.Context, req *transport.ReconcileRolesRequest) (*transport.ReconcileRolesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	wireReq := reconcileRolesWireRequest{}
	if req != nil {
		wireReq.TenantID = req.TenantID
	}

	payload, err := json.Marshal(wireReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ReconcileBuiltInRoles", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "role.ReconcileBuiltIn", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "ReconcileBuiltInRoles", Err: err}
	}

	var wire reconcileRolesWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "ReconcileBuiltInRoles", Err: &UnmarshalError{Err: err}}
	}

	return wireToReconcileReport(&wire), nil
}

// wireToReconcileReport converts a wire reconciliation report to its
// transport representation.
func wireToReconcileReport(w *reconcileRolesWireResponse) *transport.ReconcileRolesResponse {
	if w == nil {
		return nil
	}
	out := &transport.ReconcileRolesResponse{TenantID: w.TenantID}
	for _, r := range w.Roles {
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

// ReconcileBuiltInRolesAllTenants converges the stored built-in roles of every
// tenant on the server onto the server build's definitions. The caller must be
// a platform operator holding roles:reconcile_all. The server answers with the
// protobuf report gRPC returns, decoded by the conversion the gRPC and Unix
// transports share.
func (t *Transport) ReconcileBuiltInRolesAllTenants(ctx context.Context) (*transport.ReconcileAllTenantsRolesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	data, err := t.call(ctx, "role.ReconcileBuiltInAllTenants", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ReconcileBuiltInRolesAllTenants", Err: err}
	}

	var wire reconcileRolesAllTenantsWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "ReconcileBuiltInRolesAllTenants", Err: &UnmarshalError{Err: err}}
	}
	return wireToAllTenantsReport(&wire), nil
}

// reconcileRolesAllTenantsWireResponse mirrors the server's JSON response for
// role.ReconcileBuiltInAllTenants.
type reconcileRolesAllTenantsWireResponse struct {
	Tenants []reconcileRolesWireResponse `json:"tenants,omitempty"`
	Skipped []skippedTenantWire          `json:"skipped,omitempty"`
	Failed  []failedTenantWire           `json:"failed,omitempty"`
}

// skippedTenantWire mirrors role.SkippedTenant on the wire.
type skippedTenantWire struct {
	TenantID string `json:"tenantID"`
	Reason   string `json:"reason"`
	Detail   string `json:"detail,omitempty"`
}

// failedTenantWire mirrors role.FailedTenant on the wire.
type failedTenantWire struct {
	TenantID string `json:"tenantID"`
	Error    string `json:"error"`
}

// wireToAllTenantsReport converts the all-tenant wire response to the
// transport report.
func wireToAllTenantsReport(w *reconcileRolesAllTenantsWireResponse) *transport.ReconcileAllTenantsRolesResponse {
	out := &transport.ReconcileAllTenantsRolesResponse{}
	for i := range w.Tenants {
		out.Tenants = append(out.Tenants, *wireToReconcileReport(&w.Tenants[i]))
	}
	for _, s := range w.Skipped {
		out.Skipped = append(out.Skipped, transport.RoleReconcileSkippedTenant{
			TenantID: s.TenantID,
			Reason:   s.Reason,
			Detail:   s.Detail,
		})
	}
	for _, f := range w.Failed {
		out.Failed = append(out.Failed, transport.RoleReconcileFailedTenant{
			TenantID: f.TenantID,
			Error:    f.Error,
		})
	}
	return out
}
