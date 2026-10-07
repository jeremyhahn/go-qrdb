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
	"strings"
	"time"

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
	VolumePool    string                        `json:"volume_pool,omitempty"`
	Quotas        *transport.TenantQuotas       `json:"quotas,omitempty"`
	Entitlements  *transport.TenantEntitlements `json:"entitlements,omitempty"`
	// Refusal is set when the answering node will not serve this
	// tenant's data (docs/volume-pools/api.md's TenantRefusal); absent
	// otherwise. Another node may still serve it.
	Refusal   *transport.TenantRefusal `json:"refusal,omitempty"`
	CreatedAt string                   `json:"created_at"`
	UpdatedAt string                   `json:"updated_at"`
}

// mcpTenantBarrierInitResponse mirrors the JSON shape returned by the
// qrdb_tenant_barrier_init tool.
type mcpTenantBarrierInitResponse struct {
	TenantID    string                              `json:"tenant_id"`
	Strategy    string                              `json:"strategy"`
	ShareCount  int                                 `json:"share_count,omitempty"`
	Threshold   int                                 `json:"threshold,omitempty"`
	TotalShares int                                 `json:"total_shares,omitempty"`
	Holders     []transport.TenantBarrierHolderCert `json:"holders,omitempty"`
}

// mcpTenantBarrierStatus mirrors the tenant.TenantBarrierStatus JSON shape
// returned by the qrdb_tenant_barrier_status tool.
type mcpTenantBarrierStatus struct {
	TenantID        string `json:"tenant_id"`
	Sealed          bool   `json:"sealed"`
	Initialized     bool   `json:"initialized"`
	Strategy        string `json:"strategy"`
	HardwareBacked  bool   `json:"hardware_backed"`
	ShamirThreshold int    `json:"shamir_threshold,omitempty"`
	ShamirTotal     int    `json:"shamir_total,omitempty"`

	ClusterSeal *transport.ClusterSeal `json:"cluster_seal,omitempty"`
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
	if req.BarrierAutoUnseal {
		args["barrier_auto_unseal"] = true
	}
	if req.VolumePool != "" {
		args["volume_pool"] = req.VolumePool
	}
	args["admin_username"] = req.AdminUsername
	for name, value := range map[string]string{
		"admin_csr_pem":     req.AdminCSRPEM,
		"admin_cert_pem":    req.AdminCertPEM,
		"admin_chain_pem":   req.AdminChainPEM,
		"trust_anchors_pem": req.TrustAnchorsPEM,
	} {
		if value != "" {
			args[name] = value
		}
	}

	text, err := t.callTool(ctx, "qrdb_tenant_create", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "CreateTenant", Err: err}
	}

	var result struct {
		Tenant mcpTenantInfo                   `json:"tenant"`
		Admin  transport.TenantAdminCredential `json:"admin"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &result); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CreateTenant", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.CreateTenantResponse{Tenant: mcpTenantInfoToTransport(&result.Tenant), Admin: result.Admin}, nil
}

// trustAnchorTool calls a trust-anchor tool and decodes the tenant's
// anchors; the tool's JSON is transport.TrustAnchorList's.
func (t *Transport) trustAnchorTool(ctx context.Context, method, tool string, args map[string]any) (*transport.TrustAnchorList, error) {
	text, err := t.callTool(ctx, tool, args)
	if err != nil {
		return nil, &transport.RequestError{Method: method, Err: err}
	}
	var out transport.TrustAnchorList
	if jsonErr := json.Unmarshal([]byte(text), &out); jsonErr != nil {
		return nil, &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &out, nil
}

// AddTrustAnchors registers trust anchors of req.TenantID by calling the
// qrdb_trust_anchor_add MCP tool.
func (t *Transport) AddTrustAnchors(ctx context.Context, req *transport.AddTrustAnchorsRequest) (*transport.TrustAnchorList, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.trustAnchorTool(ctx, "AddTrustAnchors", "qrdb_trust_anchor_add",
		map[string]any{"tenant_id": req.TenantID, "certs_pem": req.CertsPEM})
}

// ListTrustAnchors lists tenantID's trust anchors by calling the
// qrdb_trust_anchor_list MCP tool.
func (t *Transport) ListTrustAnchors(ctx context.Context, tenantID string) (*transport.TrustAnchorList, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.trustAnchorTool(ctx, "ListTrustAnchors", "qrdb_trust_anchor_list",
		map[string]any{"tenant_id": tenantID})
}

// RemoveTrustAnchor withdraws one of tenantID's trust anchors by calling the
// qrdb_trust_anchor_remove MCP tool.
func (t *Transport) RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if _, err := t.callTool(ctx, "qrdb_trust_anchor_remove",
		map[string]any{"tenant_id": tenantID, "fingerprint": fingerprint}); err != nil {
		return &transport.RequestError{Method: "RemoveTrustAnchor", Err: err}
	}
	return nil
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

// ListDeletedTenants returns one page of retired tenant ids by calling the
// qrdb_tenant_list_deleted MCP tool.
func (t *Transport) ListDeletedTenants(ctx context.Context, req *transport.ListDeletedTenantsRequest) (*transport.ListDeletedTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		req = &transport.ListDeletedTenantsRequest{}
	}
	args := map[string]any{}
	if req.PageSize != 0 {
		args["page_size"] = req.PageSize
	}
	if req.PageToken != "" {
		args["page_token"] = req.PageToken
	}
	text, err := t.callTool(ctx, "qrdb_tenant_list_deleted", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListDeletedTenants", Err: err}
	}
	out := &transport.ListDeletedTenantsResponse{}
	if jsonErr := json.Unmarshal([]byte(text), out); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListDeletedTenants", Err: &jsonDecodeError{Err: jsonErr}}
	}
	if out.Tenants == nil {
		out.Tenants = []transport.DeletedTenant{}
	}
	return out, nil
}

// ReconcileDeletedTenants runs the deleted-tenant reconcile on the node serving
// the request by calling the qrdb_tenant_reconcile_deleted MCP tool.
func (t *Transport) ReconcileDeletedTenants(ctx context.Context) (*transport.ReconcileDeletedTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	text, err := t.callTool(ctx, "qrdb_tenant_reconcile_deleted", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ReconcileDeletedTenants", Err: err}
	}
	out := &transport.ReconcileDeletedTenantsResponse{}
	if jsonErr := json.Unmarshal([]byte(text), out); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ReconcileDeletedTenants", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return out, transport.ReconcileFailure(out, nil)
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
		"id":                      req.TenantID,
		"max_databases":           req.Quotas.MaxDatabases,
		"max_raft_groups":         req.Quotas.MaxRaftGroups,
		"max_replicas_per_group":  req.Quotas.MaxReplicasPerGroup,
		"max_total_replicas":      req.Quotas.MaxTotalReplicas,
		"max_collections":         req.Quotas.MaxCollections,
		"max_keys":                req.Quotas.MaxKeys,
		"max_storage_bytes":       req.Quotas.MaxStorageBytes,
		"max_database_size_bytes": req.Quotas.MaxDatabaseSizeBytes,
		"max_users":               req.Quotas.MaxUsers,
		"max_roles":               req.Quotas.MaxRoles,
		"max_certificates":        req.Quotas.MaxCertificates,
		"max_backups":             req.Quotas.MaxBackups,
		"max_vector_dims":         req.Quotas.MaxVectorDims,
		"max_ts_metrics":          req.Quotas.MaxTSMetrics,
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

// UpdateTenantEntitlements sets the feature entitlements for a tenant by
// calling the qrdb_tenant_update_entitlements MCP tool.
func (t *Transport) UpdateTenantEntitlements(ctx context.Context, req *transport.UpdateTenantEntitlementsRequest) (*transport.UpdateTenantEntitlementsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"id":                           req.TenantID,
		"allowed_isolation_modes":      strings.Join(req.Entitlements.AllowedIsolationModes, ","),
		"allow_dedicated_raft":         req.Entitlements.AllowDedicatedRaft,
		"allow_volume_storage":         req.Entitlements.AllowVolumeStorage,
		"allow_system_managed_barrier": req.Entitlements.AllowSystemManagedBarrier,
		"allow_dedicated_wal":          req.Entitlements.AllowDedicatedWAL,
		"allowed_barrier_modes":        strings.Join(req.Entitlements.AllowedBarrierModes, ","),
	}

	text, err := t.callTool(ctx, "qrdb_tenant_update_entitlements", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenantEntitlements", Err: err}
	}

	var info mcpTenantInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "UpdateTenantEntitlements", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.UpdateTenantEntitlementsResponse{Tenant: mcpTenantInfoToTransport(&info)}, nil
}

// TenantBarrierInit initializes a per-tenant barrier by calling the
// qrdb_tenant_barrier_init MCP tool. For the Shamir strategy the response
// carries the generated M-of-N shares; for none/passphrase the share fields
// are empty.
func (t *Transport) TenantBarrierInit(ctx context.Context, req *transport.TenantBarrierInitRequest) (*transport.TenantBarrierInitResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: &nilRequestError{Method: "TenantBarrierInit"}}
	}

	args := map[string]any{
		"tenant_id":  req.TenantID,
		"passphrase": secretArg(req.Passphrase),
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
	if len(req.Officers) > 0 {
		officersJSON, err := json.Marshal(req.Officers)
		if err != nil {
			return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: &jsonEncodeError{Err: err}}
		}
		args["officers"] = string(officersJSON)
	}
	if req.Policy != "" {
		args["policy"] = req.Policy
	}

	text, err := t.callTool(ctx, "qrdb_tenant_barrier_init", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: err}
	}

	// Empty body means none/passphrase strategy — return a minimal response
	// populated from the request.
	if text == "" {
		return &transport.TenantBarrierInitResponse{TenantID: req.TenantID, Strategy: req.Strategy}, nil
	}

	var wire mcpTenantBarrierInitResponse
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &transport.TenantBarrierInitResponse{
		TenantID:    wire.TenantID,
		Strategy:    wire.Strategy,
		ShareCount:  wire.ShareCount,
		Threshold:   wire.Threshold,
		TotalShares: wire.TotalShares,
		Holders:     wire.Holders,
	}, nil
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
	if len(req.Key) > 0 {
		args["key"] = base64.StdEncoding.EncodeToString(req.Key)
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
		TenantID:        raw.TenantID,
		Sealed:          raw.Sealed,
		Initialized:     raw.Initialized,
		Strategy:        raw.Strategy,
		HardwareBacked:  raw.HardwareBacked,
		ShamirThreshold: raw.ShamirThreshold,
		ShamirTotal:     raw.ShamirTotal,
		ClusterSeal:     raw.ClusterSeal,
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
	info := transport.TenantInfo{
		ID:            m.ID,
		Name:          m.Name,
		IsolationType: m.IsolationType,
		VolumePool:    m.VolumePool,
		Quotas:        m.Quotas,
		Entitlements:  m.Entitlements,
		Refusal:       m.Refusal,
	}
	if m.CreatedAt != "" {
		if parsed, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
			info.CreatedAt = parsed
		}
	}
	if m.UpdatedAt != "" {
		if parsed, err := time.Parse(time.RFC3339, m.UpdatedAt); err == nil {
			info.UpdatedAt = parsed
		}
	}
	return info
}

// ---------------------------------------------------------------------------
// Tenant barrier share holders
// ---------------------------------------------------------------------------

// TenantBarrierOfficerRegister enrols a share holder of a tenant barrier.
func (t *Transport) TenantBarrierOfficerRegister(ctx context.Context, req *transport.TenantOfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	var resp transport.OfficerRegisterResponse
	if err := t.callHolderTool(ctx, "TenantBarrierOfficerRegister", "qrdb_tenant_barrier_officer_register", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantBarrierOfficerList lists a tenant barrier's holders.
func (t *Transport) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*transport.OfficerStatus, error) {
	var wire struct {
		Officers []*transport.OfficerStatus `json:"officers"`
	}
	req := struct {
		TenantID string `json:"tenant_id"`
	}{TenantID: tenantID}
	if err := t.callHolderTool(ctx, "TenantBarrierOfficerList", "qrdb_tenant_barrier_officer_list", req, &wire); err != nil {
		return nil, err
	}
	return wire.Officers, nil
}

// TenantBarrierShareClaimBegin issues a claim nonce to a tenant holder.
func (t *Transport) TenantBarrierShareClaimBegin(ctx context.Context, req *transport.TenantShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	var resp transport.ShareClaimBeginResponse
	if err := t.callHolderTool(ctx, "TenantBarrierShareClaimBegin", "qrdb_tenant_barrier_share_claim_begin", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantBarrierShareClaimComplete delivers a tenant holder's sealed envelope
// exactly once.
func (t *Transport) TenantBarrierShareClaimComplete(ctx context.Context, req *transport.TenantShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	var resp transport.ShareClaimCompleteResponse
	if err := t.callHolderTool(ctx, "TenantBarrierShareClaimComplete", "qrdb_tenant_barrier_share_claim_complete", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantBarrierQuorumBegin issues a tenant holder's quorum challenge.
func (t *Transport) TenantBarrierQuorumBegin(ctx context.Context, req *transport.TenantQuorumBeginRequest) (*transport.QuorumChallenge, error) {
	var resp transport.QuorumChallenge
	if err := t.callHolderTool(ctx, "TenantBarrierQuorumBegin", "qrdb_tenant_barrier_quorum_begin", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantBarrierQuorumSubmit submits one authenticated tenant holder's share.
func (t *Transport) TenantBarrierQuorumSubmit(ctx context.Context, req *transport.TenantQuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	var resp transport.QuorumOutcome
	if err := t.callHolderTool(ctx, "TenantBarrierQuorumSubmit", "qrdb_tenant_barrier_quorum_submit", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantBarrierRekeyStart opens a rekey round on a tenant barrier.
func (t *Transport) TenantBarrierRekeyStart(ctx context.Context, req *transport.TenantRekeyStartRequest) (*transport.QuorumRound, error) {
	var resp transport.QuorumRound
	if err := t.callHolderTool(ctx, "TenantBarrierRekeyStart", "qrdb_tenant_barrier_rekey_start", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantBarrierOfficerRevoke revokes a tenant holder through a quorum-gated
// rekey.
func (t *Transport) TenantBarrierOfficerRevoke(ctx context.Context, req *transport.TenantOfficerRevokeRequest) (*transport.QuorumRound, error) {
	var resp transport.QuorumRound
	if err := t.callHolderTool(ctx, "TenantBarrierOfficerRevoke", "qrdb_tenant_barrier_officer_revoke", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
