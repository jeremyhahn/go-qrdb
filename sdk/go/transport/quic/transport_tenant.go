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
	"encoding/base64"
	"encoding/json"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/tenantconv"
)

// Compile-time interface assertion.
var _ transport.TenantClient = (*Transport)(nil)

// CreateTenant creates a new tenant via the QUIC tenant.Create handler.
func (t *Transport) CreateTenant(ctx context.Context, req *transport.CreateTenantRequest) (*transport.CreateTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.TenantCreateRequest{
		Id:                req.ID,
		Name:              req.Name,
		IsolationType:     string(req.IsolationType),
		BarrierAutoUnseal: req.BarrierAutoUnseal,
		DataRoot:          req.DataRoot,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CreateTenant", Err: err}
	}

	respBytes, err := t.call(ctx, "tenant.Create", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "CreateTenant", Err: err}
	}

	var resp pb.TenantResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "CreateTenant", Err: err}
	}

	return &transport.CreateTenantResponse{Tenant: quicProtoToTenantInfo(&resp)}, nil
}

// GetTenant retrieves a tenant by ID via the QUIC tenant.Get handler.
func (t *Transport) GetTenant(ctx context.Context, tenantID string) (*transport.GetTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.TenantGetRequest{Id: tenantID})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetTenant", Err: err}
	}

	respBytes, err := t.call(ctx, "tenant.Get", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetTenant", Err: err}
	}

	var resp pb.TenantResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "GetTenant", Err: err}
	}

	return &transport.GetTenantResponse{Tenant: quicProtoToTenantInfo(&resp)}, nil
}

// ListTenants returns all tenants via the QUIC tenant.List handler.
func (t *Transport) ListTenants(ctx context.Context) (*transport.ListTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	respBytes, err := t.call(ctx, "tenant.List", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListTenants", Err: err}
	}

	var resp pb.TenantListResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "ListTenants", Err: err}
	}

	tenants := make([]transport.TenantInfo, len(resp.GetTenants()))
	for i, pt := range resp.GetTenants() {
		tenants[i] = quicProtoToTenantInfo(pt)
	}

	return &transport.ListTenantsResponse{Tenants: tenants}, nil
}

// ListDeletedTenants returns one page of retired tenant ids via the QUIC
// tenant.ListDeleted handler.
func (t *Transport) ListDeletedTenants(ctx context.Context, req *transport.ListDeletedTenantsRequest) (*transport.ListDeletedTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		req = &transport.ListDeletedTenantsRequest{}
	}
	payload, err := proto.Marshal(&pb.TenantListDeletedRequest{PageSize: req.PageSize, PageToken: req.PageToken})
	if err != nil {
		return nil, &transport.RequestError{Method: "ListDeletedTenants", Err: err}
	}
	respBytes, err := t.call(ctx, "tenant.ListDeleted", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListDeletedTenants", Err: err}
	}
	var resp pb.TenantListDeletedResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "ListDeletedTenants", Err: err}
	}
	return tenantconv.ToListDeletedTenantsResponse(&resp), nil
}

// ReconcileDeletedTenants runs the deleted-tenant reconcile on the node serving
// the request via the QUIC tenant.ReconcileDeleted handler.
func (t *Transport) ReconcileDeletedTenants(ctx context.Context) (*transport.ReconcileDeletedTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	respBytes, err := t.call(ctx, "tenant.ReconcileDeleted", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ReconcileDeletedTenants", Err: err}
	}
	var resp pb.TenantReconcileDeletedResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "ReconcileDeletedTenants", Err: err}
	}
	out := tenantconv.ToReconcileDeletedTenantsResponse(&resp)
	return out, transport.ReconcileFailure(out, nil)
}

// DeleteTenant removes a tenant via the QUIC tenant.Delete handler.
func (t *Transport) DeleteTenant(ctx context.Context, tenantID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.TenantDeleteRequest{Id: tenantID})
	if err != nil {
		return &transport.RequestError{Method: "DeleteTenant", Err: err}
	}

	_, err = t.call(ctx, "tenant.Delete", payload)
	if err != nil {
		return &transport.RequestError{Method: "DeleteTenant", Err: err}
	}

	return nil
}

// UpdateTenant applies a partial update to an existing tenant via the QUIC tenant.Update handler.
func (t *Transport) UpdateTenant(ctx context.Context, req *transport.UpdateTenantRequest) (*transport.UpdateTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.TenantUpdateRequest{
		Id:            req.TenantID,
		Name:          req.Name,
		IsolationType: string(req.IsolationType),
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenant", Err: err}
	}

	respBytes, err := t.call(ctx, "tenant.Update", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenant", Err: err}
	}

	var resp pb.TenantResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenant", Err: err}
	}

	return &transport.UpdateTenantResponse{Tenant: quicProtoToTenantInfo(&resp)}, nil
}

// UpdateTenantQuotas sets the resource quotas for a tenant via the QUIC tenant.UpdateQuotas handler.
func (t *Transport) UpdateTenantQuotas(ctx context.Context, req *transport.UpdateTenantQuotasRequest) (*transport.UpdateTenantQuotasResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.TenantUpdateQuotasRequest{
		Id: req.TenantID,
		Quotas: &pb.TenantQuotas{
			MaxDatabases:          req.Quotas.MaxDatabases,
			MaxRaftGroups:         req.Quotas.MaxRaftGroups,
			MaxReplicasPerGroup:   req.Quotas.MaxReplicasPerGroup,
			MaxTotalReplicas:      req.Quotas.MaxTotalReplicas,
			MaxCollections:        req.Quotas.MaxCollections,
			MaxKeys:               req.Quotas.MaxKeys,
			MaxStorageBytes:       req.Quotas.MaxStorageBytes,
			MaxDatabaseSizeBytes:  req.Quotas.MaxDatabaseSizeBytes,
			MaxUsers:              req.Quotas.MaxUsers,
			MaxRoles:              req.Quotas.MaxRoles,
			MaxCertificates:       req.Quotas.MaxCertificates,
			MaxBackups:            req.Quotas.MaxBackups,
			MaxVectorDims:         req.Quotas.MaxVectorDims,
			MaxTsMetrics:          req.Quotas.MaxTSMetrics,
			SystemBarrierQuota:    req.Quotas.SystemBarrierQuota,
			MaxDedicatedWalGroups: req.Quotas.MaxDedicatedWALGroups,
		},
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenantQuotas", Err: err}
	}

	respBytes, err := t.call(ctx, "tenant.UpdateQuotas", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenantQuotas", Err: err}
	}

	var resp pb.TenantResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenantQuotas", Err: err}
	}

	return &transport.UpdateTenantQuotasResponse{Tenant: quicProtoToTenantInfo(&resp)}, nil
}

// UpdateTenantEntitlements sets the feature entitlements for a tenant via the
// QUIC tenant.UpdateEntitlements handler.
func (t *Transport) UpdateTenantEntitlements(ctx context.Context, req *transport.UpdateTenantEntitlementsRequest) (*transport.UpdateTenantEntitlementsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.TenantUpdateEntitlementsRequest{
		Id: req.TenantID,
		Entitlements: &pb.TenantEntitlements{
			AllowedIsolationModes:     req.Entitlements.AllowedIsolationModes,
			AllowDedicatedRaft:        req.Entitlements.AllowDedicatedRaft,
			AllowVolumeStorage:        req.Entitlements.AllowVolumeStorage,
			AllowSystemManagedBarrier: req.Entitlements.AllowSystemManagedBarrier,
			AllowedBarrierModes:       req.Entitlements.AllowedBarrierModes,
			AllowDedicatedWal:         req.Entitlements.AllowDedicatedWAL,
		},
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenantEntitlements", Err: err}
	}

	respBytes, err := t.call(ctx, "tenant.UpdateEntitlements", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenantEntitlements", Err: err}
	}

	var resp pb.TenantResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenantEntitlements", Err: err}
	}

	return &transport.UpdateTenantEntitlementsResponse{Tenant: quicProtoToTenantInfo(&resp)}, nil
}

// ---------------------------------------------------------------------------
// Per-tenant barrier operations
//
// The barrier handlers use JSON wire payloads (matching the audit and auth
// factor frame conventions) since these schema-light operations do not warrant
// generating dedicated protobuf messages. Field shapes are defined server-side
// in pkg/api/quic/handlers_tenant_barrier.go.
// ---------------------------------------------------------------------------

// quicTenantBarrierInitRequest mirrors the server-side wire schema for
// tenant.BarrierInit.
type quicTenantBarrierInitRequest struct {
	TenantID   string `json:"tenant_id"`
	Passphrase []byte `json:"passphrase,omitempty"`
	Threshold  int    `json:"threshold,omitempty"`
	Shares     int    `json:"shares,omitempty"`
	Strategy   string `json:"strategy,omitempty"`
}

// quicTenantBarrierUnsealRequest mirrors the server-side wire schema for
// tenant.BarrierUnseal.
type quicTenantBarrierUnsealRequest struct {
	TenantID string `json:"tenant_id"`
	Share    []byte `json:"share,omitempty"`
	Key      []byte `json:"key,omitempty"`
}

// quicTenantBarrierSealRequest mirrors the server-side wire schema for
// tenant.BarrierSeal.
type quicTenantBarrierSealRequest struct {
	TenantID string `json:"tenant_id"`
}

// quicTenantBarrierStatusRequest mirrors the server-side wire schema for
// tenant.BarrierStatus.
type quicTenantBarrierStatusRequest struct {
	TenantID string `json:"tenant_id"`
}

// quicTenantBarrierInitResponse mirrors the server-side wire schema for
// tenant.BarrierInit responses.
type quicTenantBarrierInitResponse struct {
	TenantID    string   `json:"tenant_id"`
	Strategy    string   `json:"strategy"`
	Shares      []string `json:"shares,omitempty"`
	Threshold   int      `json:"threshold,omitempty"`
	TotalShares int      `json:"total_shares,omitempty"`
}

// quicTenantBarrierUnsealShareRequest mirrors the server-side wire schema for
// tenant.BarrierUnsealShare.
type quicTenantBarrierUnsealShareRequest struct {
	TenantID string `json:"tenant_id"`
	Share    string `json:"share"`
}

// quicTenantBarrierUnsealProgress mirrors the server-side wire schema for
// tenant.BarrierUnsealShare responses.
type quicTenantBarrierUnsealProgress struct {
	TenantID  string `json:"tenant_id"`
	Required  int    `json:"required"`
	Submitted int    `json:"submitted"`
	Complete  bool   `json:"complete"`
	Sealed    bool   `json:"sealed"`
}

// quicTenantBarrierStatusResponse mirrors the server-side wire schema for
// tenant.BarrierStatus responses.
type quicTenantBarrierStatusResponse struct {
	TenantID        string `json:"tenant_id"`
	Sealed          bool   `json:"sealed"`
	Initialized     bool   `json:"initialized"`
	Strategy        string `json:"strategy"`
	HardwareBacked  bool   `json:"hardware_backed"`
	ShamirThreshold int    `json:"shamir_threshold,omitempty"`
	ShamirTotal     int    `json:"shamir_total,omitempty"`
}

// nilTenantBarrierRequestError is returned when a per-tenant barrier method
// is called with a nil request struct.
type nilTenantBarrierRequestError struct {
	Method string
}

// Error returns a human-readable description.
func (e *nilTenantBarrierRequestError) Error() string {
	return "transport: " + e.Method + ": nil request"
}

// TenantBarrierInit initializes a per-tenant barrier via the QUIC
// tenant.BarrierInit handler. For the Shamir strategy the response carries the
// generated M-of-N shares; for none/passphrase the share fields are empty.
func (t *Transport) TenantBarrierInit(ctx context.Context, req *transport.TenantBarrierInitRequest) (*transport.TenantBarrierInitResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: &nilTenantBarrierRequestError{Method: "TenantBarrierInit"}}
	}

	payload, err := json.Marshal(quicTenantBarrierInitRequest{
		TenantID:   req.TenantID,
		Passphrase: req.Passphrase,
		Threshold:  req.Threshold,
		Shares:     req.Shares,
		Strategy:   req.Strategy,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: err}
	}

	respBytes, err := t.call(ctx, "tenant.BarrierInit", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: err}
	}

	// Empty body means none/passphrase strategy — return a minimal response
	// populated from the request.
	if len(respBytes) == 0 {
		return &transport.TenantBarrierInitResponse{TenantID: req.TenantID, Strategy: req.Strategy}, nil
	}

	var wire quicTenantBarrierInitResponse
	if err := json.Unmarshal(respBytes, &wire); err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: err}
	}
	return &transport.TenantBarrierInitResponse{
		TenantID:    wire.TenantID,
		Strategy:    wire.Strategy,
		ShareCount:  len(wire.Shares),
		Threshold:   wire.Threshold,
		TotalShares: wire.TotalShares,
	}, nil
}

// TenantBarrierUnsealShare submits a single Shamir share toward a per-tenant
// barrier's quorum threshold via the QUIC tenant.BarrierUnsealShare handler.
func (t *Transport) TenantBarrierUnsealShare(ctx context.Context, req *transport.TenantBarrierUnsealShareRequest) (*transport.TenantBarrierUnsealProgress, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierUnsealShare", Err: &nilTenantBarrierRequestError{Method: "TenantBarrierUnsealShare"}}
	}

	payload, err := json.Marshal(quicTenantBarrierUnsealShareRequest{
		TenantID: req.TenantID,
		Share:    req.Share,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierUnsealShare", Err: err}
	}

	respBytes, err := t.call(ctx, "tenant.BarrierUnsealShare", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierUnsealShare", Err: err}
	}

	var wire quicTenantBarrierUnsealProgress
	if err := json.Unmarshal(respBytes, &wire); err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierUnsealShare", Err: err}
	}
	return &transport.TenantBarrierUnsealProgress{
		TenantID:  wire.TenantID,
		Required:  wire.Required,
		Submitted: wire.Submitted,
		Complete:  wire.Complete,
		Sealed:    wire.Sealed,
	}, nil
}

// TenantBarrierUnseal unseals a per-tenant barrier via the QUIC
// tenant.BarrierUnseal handler.
func (t *Transport) TenantBarrierUnseal(ctx context.Context, req *transport.TenantBarrierUnsealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "TenantBarrierUnseal", Err: &nilTenantBarrierRequestError{Method: "TenantBarrierUnseal"}}
	}

	payload, err := json.Marshal(quicTenantBarrierUnsealRequest{
		TenantID: req.TenantID,
		Share:    req.Share,
		Key:      req.Key,
	})
	if err != nil {
		return &transport.RequestError{Method: "TenantBarrierUnseal", Err: err}
	}

	if _, err := t.call(ctx, "tenant.BarrierUnseal", payload); err != nil {
		return &transport.RequestError{Method: "TenantBarrierUnseal", Err: err}
	}
	return nil
}

// TenantBarrierSeal seals a per-tenant barrier via the QUIC
// tenant.BarrierSeal handler.
func (t *Transport) TenantBarrierSeal(ctx context.Context, req *transport.TenantBarrierSealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "TenantBarrierSeal", Err: &nilTenantBarrierRequestError{Method: "TenantBarrierSeal"}}
	}

	payload, err := json.Marshal(quicTenantBarrierSealRequest{TenantID: req.TenantID})
	if err != nil {
		return &transport.RequestError{Method: "TenantBarrierSeal", Err: err}
	}

	if _, err := t.call(ctx, "tenant.BarrierSeal", payload); err != nil {
		return &transport.RequestError{Method: "TenantBarrierSeal", Err: err}
	}
	return nil
}

// TenantBarrierStatus returns the current sealed/initialized state and active
// strategy metadata for a per-tenant barrier via the QUIC
// tenant.BarrierStatus handler.
func (t *Transport) TenantBarrierStatus(ctx context.Context, req *transport.TenantBarrierStatusRequest) (*transport.TenantBarrierStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: &nilTenantBarrierRequestError{Method: "TenantBarrierStatus"}}
	}

	payload, err := json.Marshal(quicTenantBarrierStatusRequest{TenantID: req.TenantID})
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: err}
	}

	respBytes, err := t.call(ctx, "tenant.BarrierStatus", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: err}
	}

	var wire quicTenantBarrierStatusResponse
	if err := json.Unmarshal(respBytes, &wire); err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: err}
	}

	return &transport.TenantBarrierStatusResponse{
		TenantID:        wire.TenantID,
		Sealed:          wire.Sealed,
		Initialized:     wire.Initialized,
		Strategy:        wire.Strategy,
		HardwareBacked:  wire.HardwareBacked,
		ShamirThreshold: wire.ShamirThreshold,
		ShamirTotal:     wire.ShamirTotal,
	}, nil
}

// ---------------------------------------------------------------------------
// Proto conversion helpers
// ---------------------------------------------------------------------------

// quicProtoToTenantInfo converts a protobuf TenantResponse to a transport.TenantInfo.
func quicProtoToTenantInfo(resp *pb.TenantResponse) transport.TenantInfo {
	if resp == nil {
		return transport.TenantInfo{}
	}

	info := transport.TenantInfo{
		ID:            resp.GetId(),
		Name:          resp.GetName(),
		IsolationType: transport.TenantIsolationType(resp.GetIsolationType()),
		DataRoot:      resp.GetDataRoot(),
	}

	if pbQ := resp.GetQuotas(); pbQ != nil {
		info.Quotas = &transport.TenantQuotas{
			MaxDatabases:          pbQ.GetMaxDatabases(),
			MaxRaftGroups:         pbQ.GetMaxRaftGroups(),
			MaxReplicasPerGroup:   pbQ.GetMaxReplicasPerGroup(),
			MaxTotalReplicas:      pbQ.GetMaxTotalReplicas(),
			MaxCollections:        pbQ.GetMaxCollections(),
			MaxKeys:               pbQ.GetMaxKeys(),
			MaxStorageBytes:       pbQ.GetMaxStorageBytes(),
			MaxDatabaseSizeBytes:  pbQ.GetMaxDatabaseSizeBytes(),
			MaxUsers:              pbQ.GetMaxUsers(),
			MaxRoles:              pbQ.GetMaxRoles(),
			MaxCertificates:       pbQ.GetMaxCertificates(),
			MaxBackups:            pbQ.GetMaxBackups(),
			MaxVectorDims:         pbQ.GetMaxVectorDims(),
			MaxTSMetrics:          pbQ.GetMaxTsMetrics(),
			SystemBarrierQuota:    pbQ.GetSystemBarrierQuota(),
			MaxDedicatedWALGroups: pbQ.GetMaxDedicatedWalGroups(),
		}
	}

	if pbE := resp.GetEntitlements(); pbE != nil {
		info.Entitlements = &transport.TenantEntitlements{
			AllowedIsolationModes:     pbE.GetAllowedIsolationModes(),
			AllowDedicatedRaft:        pbE.GetAllowDedicatedRaft(),
			AllowVolumeStorage:        pbE.GetAllowVolumeStorage(),
			AllowSystemManagedBarrier: pbE.GetAllowSystemManagedBarrier(),
			AllowedBarrierModes:       pbE.GetAllowedBarrierModes(),
			AllowDedicatedWAL:         pbE.GetAllowDedicatedWal(),
		}
	}

	if ts := resp.GetCreatedAt(); ts != "" {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			info.CreatedAt = parsed
		}
	}
	if ts := resp.GetUpdatedAt(); ts != "" {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			info.UpdatedAt = parsed
		}
	}

	return info
}

// ---------------------------------------------------------------------------
// Tenant barrier officer / share-claim / seal-vote
// ---------------------------------------------------------------------------

// quicTenantOfficerRegReq is the JSON payload sent by the SDK for
// tenant.BarrierOfficerRegister frames.
type quicTenantOfficerRegReq struct {
	TenantID          string `json:"tenant_id"`
	Username          string `json:"username"`
	CSRPEM            string `json:"csr_pem"`
	AuthenticatorType string `json:"authenticator_type,omitempty"`
}

// quicTenantOfficerRegResp is the JSON response for
// tenant.BarrierOfficerRegister frames.
type quicTenantOfficerRegResp struct {
	CertPEM    string `json:"cert_pem"`
	ShareIndex int    `json:"share_index"`
}

// quicTenantOfficerListReq is the JSON payload for
// tenant.BarrierOfficerList and tenant.BarrierOfficerClaimStatus frames.
type quicTenantOfficerListReq struct {
	TenantID string `json:"tenant_id"`
}

// quicTenantOfficerStatusWire is the JSON representation of a Security Officer
// enrollment record within a tenant barrier response.
type quicTenantOfficerStatusWire struct {
	Username       string `json:"username"`
	CertThumbprint string `json:"cert_thumbprint"`
	ShareIndex     int    `json:"share_index"`
	AuthFactor     string `json:"auth_factor"`
	SharePending   bool   `json:"share_pending"`
	EnrolledAt     string `json:"enrolled_at,omitempty"`
	ClaimedAt      string `json:"claimed_at,omitempty"`
}

// quicTenantOfficersListResp is the JSON envelope for officer-list responses.
type quicTenantOfficersListResp struct {
	Officers []quicTenantOfficerStatusWire `json:"officers"`
}

// quicTenantShareClaimBeginReq is the JSON payload for
// tenant.BarrierShareClaimBegin frames.
type quicTenantShareClaimBeginReq struct {
	TenantID string `json:"tenant_id"`
	Username string `json:"username"`
}

// quicTenantShareClaimBeginResp is the JSON response for
// tenant.BarrierShareClaimBegin frames.
type quicTenantShareClaimBeginResp struct {
	NonceHex  string `json:"nonce_hex"`
	ExpiresAt int64  `json:"expires"` // unix timestamp
}

// quicTenantShareClaimCompleteReq is the JSON payload for
// tenant.BarrierShareClaimComplete frames.
type quicTenantShareClaimCompleteReq struct {
	TenantID  string `json:"tenant_id"`
	Username  string `json:"username"`
	NonceHex  string `json:"nonce_hex"`
	Signature string `json:"signature"` // base64-encoded
}

// quicTenantShareClaimCompleteResp is the JSON response for
// tenant.BarrierShareClaimComplete frames.
type quicTenantShareClaimCompleteResp struct {
	ShareBytes string `json:"share_bytes"` // base64-encoded
}

// quicTenantSealVoteReq is the JSON payload for
// tenant.BarrierShamirSealVote frames.
type quicTenantSealVoteReq struct {
	TenantID string `json:"tenant_id"`
	Share    string `json:"share"`
}

// quicTenantSealVoteResp is the JSON response for
// tenant.BarrierShamirSealVote frames.
type quicTenantSealVoteResp struct {
	Required  int  `json:"required"`
	Submitted int  `json:"submitted"`
	Complete  bool `json:"complete"`
}

// TenantBarrierOfficerRegister registers a Security Officer for the named
// tenant's barrier via the QUIC tenant.BarrierOfficerRegister handler.
func (t *Transport) TenantBarrierOfficerRegister(ctx context.Context, req *transport.TenantOfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerRegister", Err: &nilTenantBarrierRequestError{Method: "TenantBarrierOfficerRegister"}}
	}
	payload, err := json.Marshal(quicTenantOfficerRegReq{
		TenantID:          req.TenantID,
		Username:          req.Username,
		CSRPEM:            string(req.CSRPEM),
		AuthenticatorType: req.AuthenticatorType,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerRegister", Err: err}
	}
	respBytes, err := t.call(ctx, "tenant.BarrierOfficerRegister", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerRegister", Err: err}
	}
	var wire quicTenantOfficerRegResp
	if err := json.Unmarshal(respBytes, &wire); err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerRegister", Err: err}
	}
	return &transport.OfficerRegisterResponse{
		CertPEM:    []byte(wire.CertPEM),
		ShareIndex: wire.ShareIndex,
	}, nil
}

// TenantBarrierOfficerList returns all enrolled Security Officers for the
// named tenant's barrier via the QUIC tenant.BarrierOfficerList handler.
func (t *Transport) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := json.Marshal(quicTenantOfficerListReq{TenantID: tenantID})
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerList", Err: err}
	}
	respBytes, err := t.call(ctx, "tenant.BarrierOfficerList", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerList", Err: err}
	}
	var wire quicTenantOfficersListResp
	if err := json.Unmarshal(respBytes, &wire); err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerList", Err: err}
	}
	return quicTenantOfficerWiresToStatuses(wire.Officers), nil
}

// TenantBarrierOfficerClaimStatus returns a per-officer claim status snapshot
// for the named tenant's barrier via the QUIC tenant.BarrierOfficerClaimStatus
// handler.
func (t *Transport) TenantBarrierOfficerClaimStatus(ctx context.Context, tenantID string) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := json.Marshal(quicTenantOfficerListReq{TenantID: tenantID})
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerClaimStatus", Err: err}
	}
	respBytes, err := t.call(ctx, "tenant.BarrierOfficerClaimStatus", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerClaimStatus", Err: err}
	}
	var wire quicTenantOfficersListResp
	if err := json.Unmarshal(respBytes, &wire); err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerClaimStatus", Err: err}
	}
	return quicTenantOfficerWiresToStatuses(wire.Officers), nil
}

// TenantBarrierShareClaimBegin issues a challenge nonce to the named Security
// Officer for the tenant barrier share claim handshake via the QUIC
// tenant.BarrierShareClaimBegin handler.
func (t *Transport) TenantBarrierShareClaimBegin(ctx context.Context, req *transport.TenantShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShareClaimBegin", Err: &nilTenantBarrierRequestError{Method: "TenantBarrierShareClaimBegin"}}
	}
	payload, err := json.Marshal(quicTenantShareClaimBeginReq{
		TenantID: req.TenantID,
		Username: req.Username,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShareClaimBegin", Err: err}
	}
	respBytes, err := t.call(ctx, "tenant.BarrierShareClaimBegin", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShareClaimBegin", Err: err}
	}
	var wire quicTenantShareClaimBeginResp
	if err := json.Unmarshal(respBytes, &wire); err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShareClaimBegin", Err: err}
	}
	return &transport.ShareClaimBeginResponse{
		NonceHex:  wire.NonceHex,
		ExpiresAt: time.Unix(wire.ExpiresAt, 0),
	}, nil
}

// TenantBarrierShareClaimComplete verifies the Security Officer's nonce
// signature and returns the tenant barrier share bytes exactly once via the
// QUIC tenant.BarrierShareClaimComplete handler.
func (t *Transport) TenantBarrierShareClaimComplete(ctx context.Context, req *transport.TenantShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShareClaimComplete", Err: &nilTenantBarrierRequestError{Method: "TenantBarrierShareClaimComplete"}}
	}
	payload, err := json.Marshal(quicTenantShareClaimCompleteReq{
		TenantID:  req.TenantID,
		Username:  req.Username,
		NonceHex:  req.NonceHex,
		Signature: quicB64Encode(req.Signature),
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShareClaimComplete", Err: err}
	}
	respBytes, err := t.call(ctx, "tenant.BarrierShareClaimComplete", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShareClaimComplete", Err: err}
	}
	var wire quicTenantShareClaimCompleteResp
	if err := json.Unmarshal(respBytes, &wire); err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShareClaimComplete", Err: err}
	}
	shareBytes, decErr := quicB64Decode(wire.ShareBytes)
	if decErr != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShareClaimComplete", Err: decErr}
	}
	return &transport.ShareClaimCompleteResponse{
		ShareBytes: shareBytes,
	}, nil
}

// TenantBarrierShamirSealVote submits a Shamir share as a seal vote for the
// named tenant's barrier via the QUIC tenant.BarrierShamirSealVote handler.
func (t *Transport) TenantBarrierShamirSealVote(ctx context.Context, req *transport.TenantShamirSealVoteRequest) (*transport.ShamirSealVoteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShamirSealVote", Err: &nilTenantBarrierRequestError{Method: "TenantBarrierShamirSealVote"}}
	}
	payload, err := json.Marshal(quicTenantSealVoteReq{
		TenantID: req.TenantID,
		Share:    req.Share,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShamirSealVote", Err: err}
	}
	respBytes, err := t.call(ctx, "tenant.BarrierShamirSealVote", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShamirSealVote", Err: err}
	}
	var wire quicTenantSealVoteResp
	if err := json.Unmarshal(respBytes, &wire); err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShamirSealVote", Err: err}
	}
	return &transport.ShamirSealVoteResponse{
		Required:  wire.Required,
		Submitted: wire.Submitted,
		Complete:  wire.Complete,
	}, nil
}

// quicB64Encode returns the standard base64 encoding of b.
func quicB64Encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// quicB64Decode decodes a standard base64-encoded string.
func quicB64Decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// quicTenantOfficerWiresToStatuses converts JSON wire officer records to
// transport.OfficerStatus values.
func quicTenantOfficerWiresToStatuses(wires []quicTenantOfficerStatusWire) []*transport.OfficerStatus {
	out := make([]*transport.OfficerStatus, len(wires))
	for i, w := range wires {
		s := &transport.OfficerStatus{
			Username:       w.Username,
			CertThumbprint: w.CertThumbprint,
			ShareIndex:     w.ShareIndex,
			AuthFactor:     w.AuthFactor,
			SharePending:   w.SharePending,
		}
		if w.EnrolledAt != "" {
			if t, err := time.Parse(time.RFC3339, w.EnrolledAt); err == nil {
				s.EnrolledAt = t
			}
		}
		if w.ClaimedAt != "" {
			if t, err := time.Parse(time.RFC3339, w.ClaimedAt); err == nil {
				s.ClaimedAt = t
			}
		}
		out[i] = s
	}
	return out
}
