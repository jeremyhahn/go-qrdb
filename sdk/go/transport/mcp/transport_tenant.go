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
	"encoding/base64"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.TenantClient = (*Transport)(nil)

// mcpTenantInfo is the JSON shape returned by the MCP tenant tools.
// Fields match the tenant.TenantInfo JSON tags.
type mcpTenantInfo struct {
	ID            string                        `json:"id"`
	Name          string                        `json:"name"`
	IsolationType transport.TenantIsolationType `json:"isolation_type,omitempty"`
	Quotas        *transport.TenantQuotas       `json:"quotas,omitempty"`
	CreatedAt     string                        `json:"created_at"`
	UpdatedAt     string                        `json:"updated_at"`
}

// mcpTenantBarrierStatus mirrors the tenant.TenantBarrierStatus JSON shape
// returned by the qrdb_tenant_barrier_status tool.
type mcpTenantBarrierStatus struct {
	TenantID       string `json:"tenant_id"`
	Sealed         bool   `json:"sealed"`
	Initialized    bool   `json:"initialized"`
	Strategy       string `json:"strategy"`
	HardwareBacked bool   `json:"hardware_backed"`
}

// CreateTenant creates a new tenant by calling the qrdb_tenant_create MCP tool.
func (t *Transport) CreateTenant(ctx context.Context, req *transport.CreateTenantRequest) (*transport.CreateTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"id":   req.ID,
		"name": req.Name,
	}
	if req.IsolationType != "" {
		args["isolation_type"] = string(req.IsolationType)
	}

	text, err := t.callTool(ctx, "qrdb_tenant_create", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "CreateTenant", Err: err}
	}

	var info mcpTenantInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CreateTenant", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.CreateTenantResponse{Tenant: mcpTenantInfoToTransport(&info)}, nil
}

// GetTenant retrieves a tenant by ID by calling the qrdb_tenant_get MCP tool.
func (t *Transport) GetTenant(ctx context.Context, tenantID string) (*transport.GetTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_tenant_get", map[string]any{"id": tenantID})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetTenant", Err: err}
	}

	var info mcpTenantInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetTenant", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.GetTenantResponse{Tenant: mcpTenantInfoToTransport(&info)}, nil
}

// ListTenants returns all tenants by calling the qrdb_tenant_list MCP tool.
func (t *Transport) ListTenants(ctx context.Context) (*transport.ListTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_tenant_list", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListTenants", Err: err}
	}

	var raw []mcpTenantInfo
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListTenants", Err: &jsonDecodeError{Err: jsonErr}}
	}

	tenants := make([]transport.TenantInfo, len(raw))
	for i := range raw {
		tenants[i] = mcpTenantInfoToTransport(&raw[i])
	}

	return &transport.ListTenantsResponse{Tenants: tenants}, nil
}

// DeleteTenant removes a tenant by calling the qrdb_tenant_delete MCP tool.
func (t *Transport) DeleteTenant(ctx context.Context, tenantID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_tenant_delete", map[string]any{"id": tenantID})
	if err != nil {
		return &transport.RequestError{Method: "DeleteTenant", Err: err}
	}

	return nil
}

// UpdateTenant applies a partial update to an existing tenant by calling the
// qrdb_tenant_update MCP tool.
func (t *Transport) UpdateTenant(ctx context.Context, req *transport.UpdateTenantRequest) (*transport.UpdateTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{"id": req.TenantID}
	if req.Name != "" {
		args["name"] = req.Name
	}
	if req.IsolationType != "" {
		args["isolation_type"] = string(req.IsolationType)
	}

	text, err := t.callTool(ctx, "qrdb_tenant_update", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenant", Err: err}
	}

	var info mcpTenantInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "UpdateTenant", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.UpdateTenantResponse{Tenant: mcpTenantInfoToTransport(&info)}, nil
}

// UpdateTenantQuotas sets the resource quotas for a tenant by calling the
// qrdb_tenant_update_quotas MCP tool.
func (t *Transport) UpdateTenantQuotas(ctx context.Context, req *transport.UpdateTenantQuotasRequest) (*transport.UpdateTenantQuotasResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"id":                req.TenantID,
		"max_databases":     req.Quotas.MaxDatabases,
		"max_raft_groups":   req.Quotas.MaxRaftGroups,
		"max_collections":   req.Quotas.MaxCollections,
		"max_keys":          req.Quotas.MaxKeys,
		"max_storage_bytes": req.Quotas.MaxStorageBytes,
		"max_users":         req.Quotas.MaxUsers,
		"max_roles":         req.Quotas.MaxRoles,
		"max_certificates":  req.Quotas.MaxCertificates,
		"max_backups":       req.Quotas.MaxBackups,
		"max_vector_dims":   req.Quotas.MaxVectorDims,
		"max_ts_metrics":    req.Quotas.MaxTSMetrics,
	}

	text, err := t.callTool(ctx, "qrdb_tenant_update_quotas", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenantQuotas", Err: err}
	}

	var info mcpTenantInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "UpdateTenantQuotas", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.UpdateTenantQuotasResponse{Tenant: mcpTenantInfoToTransport(&info)}, nil
}

// TenantBarrierInit initializes a per-tenant barrier by calling the
// qrdb_tenant_barrier_init MCP tool.
func (t *Transport) TenantBarrierInit(ctx context.Context, req *transport.TenantBarrierInitRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "TenantBarrierInit", Err: &nilRequestError{Method: "TenantBarrierInit"}}
	}

	args := map[string]any{
		"tenant_id":  req.TenantID,
		"passphrase": req.Passphrase,
	}
	if req.Threshold != 0 {
		args["threshold"] = req.Threshold
	}
	if req.Shares != 0 {
		args["shares"] = req.Shares
	}
	if req.Strategy != "" {
		args["strategy"] = req.Strategy
	}

	if _, err := t.callTool(ctx, "qrdb_tenant_barrier_init", args); err != nil {
		return &transport.RequestError{Method: "TenantBarrierInit", Err: err}
	}

	return nil
}

// TenantBarrierUnseal unseals a per-tenant barrier by calling the
// qrdb_tenant_barrier_unseal MCP tool. The Key/Share bytes are base64-encoded
// for transport over JSON.
func (t *Transport) TenantBarrierUnseal(ctx context.Context, req *transport.TenantBarrierUnsealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "TenantBarrierUnseal", Err: &nilRequestError{Method: "TenantBarrierUnseal"}}
	}

	args := map[string]any{
		"tenant_id": req.TenantID,
	}
	// Prefer Key when set; fall back to Share. Both carry per-tenant root
	// key material on this transport.
	keyMaterial := req.Key
	if len(keyMaterial) == 0 {
		keyMaterial = req.Share
	}
	if len(keyMaterial) > 0 {
		args["key"] = base64.StdEncoding.EncodeToString(keyMaterial)
	}

	if _, err := t.callTool(ctx, "qrdb_tenant_barrier_unseal", args); err != nil {
		return &transport.RequestError{Method: "TenantBarrierUnseal", Err: err}
	}

	return nil
}

// TenantBarrierSeal seals a per-tenant barrier by calling the
// qrdb_tenant_barrier_seal MCP tool. Sealing requires no credentials.
func (t *Transport) TenantBarrierSeal(ctx context.Context, req *transport.TenantBarrierSealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "TenantBarrierSeal", Err: &nilRequestError{Method: "TenantBarrierSeal"}}
	}

	if _, err := t.callTool(ctx, "qrdb_tenant_barrier_seal", map[string]any{
		"tenant_id": req.TenantID,
	}); err != nil {
		return &transport.RequestError{Method: "TenantBarrierSeal", Err: err}
	}

	return nil
}

// TenantBarrierStatus returns the current sealed/initialized state and
// active strategy of a per-tenant barrier by calling the
// qrdb_tenant_barrier_status MCP tool.
func (t *Transport) TenantBarrierStatus(ctx context.Context, req *transport.TenantBarrierStatusRequest) (*transport.TenantBarrierStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: &nilRequestError{Method: "TenantBarrierStatus"}}
	}

	text, err := t.callTool(ctx, "qrdb_tenant_barrier_status", map[string]any{
		"tenant_id": req.TenantID,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: err}
	}

	var raw mcpTenantBarrierStatus
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.TenantBarrierStatusResponse{
		TenantID:       raw.TenantID,
		Sealed:         raw.Sealed,
		Initialized:    raw.Initialized,
		Strategy:       raw.Strategy,
		HardwareBacked: raw.HardwareBacked,
	}, nil
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

// mcpTenantInfoToTransport converts a mcpTenantInfo to a transport.TenantInfo.
func mcpTenantInfoToTransport(m *mcpTenantInfo) transport.TenantInfo {
	if m == nil {
		return transport.TenantInfo{}
	}
	return transport.TenantInfo{
		ID:            m.ID,
		Name:          m.Name,
		IsolationType: m.IsolationType,
		Quotas:        m.Quotas,
	}
}
