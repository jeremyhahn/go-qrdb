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
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/enrollconv"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/holderconv"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/tenantconv"
)

// Compile-time interface assertion.
var _ transport.TenantClient = (*Transport)(nil)

// CreateTenant creates a new tenant via the QUIC tenant.Create handler.
func (t *Transport) CreateTenant(ctx context.Context, req *transport.CreateTenantRequest) (*transport.CreateTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(enrollconv.TenantCreateRequest(req))
	if err != nil {
		return nil, &transport.RequestError{Method: "CreateTenant", Err: err}
	}

	respBytes, err := t.call(ctx, "tenant.Create", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "CreateTenant", Err: err}
	}

	var resp pb.TenantCreateResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "CreateTenant", Err: err}
	}

	return &transport.CreateTenantResponse{
		Tenant: quicProtoToTenantInfo(resp.GetTenant()),
		Admin:  enrollconv.AdminCredential(resp.GetAdmin()),
	}, nil
}

// trustAnchorCall sends a trust-anchor request to op and decodes the
// tenant's anchors, when the op answers with them.
func (t *Transport) trustAnchorCall(ctx context.Context, method, op string, req proto.Message, decode bool) (*transport.TrustAnchorList, error) {
	payload, err := proto.Marshal(req)
	if err != nil {
		return nil, &transport.RequestError{Method: method, Err: err}
	}
	respBytes, err := t.call(ctx, op, payload)
	if err != nil {
		return nil, &transport.RequestError{Method: method, Err: err}
	}
	if !decode {
		return nil, nil
	}
	var resp pb.TrustAnchorListResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: method, Err: err}
	}
	return enrollconv.TrustAnchors(&resp), nil
}

// AddTrustAnchors registers trust anchors of req.TenantID via the QUIC
// tenant.TrustAnchorAdd handler.
func (t *Transport) AddTrustAnchors(ctx context.Context, req *transport.AddTrustAnchorsRequest) (*transport.TrustAnchorList, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.trustAnchorCall(ctx, "AddTrustAnchors", "tenant.TrustAnchorAdd",
		&pb.TrustAnchorAddRequest{TenantId: req.TenantID, CertsPem: req.CertsPEM}, true)
}

// ListTrustAnchors lists tenantID's trust anchors via the QUIC
// tenant.TrustAnchorList handler.
func (t *Transport) ListTrustAnchors(ctx context.Context, tenantID string) (*transport.TrustAnchorList, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.trustAnchorCall(ctx, "ListTrustAnchors", "tenant.TrustAnchorList",
		&pb.TrustAnchorListRequest{TenantId: tenantID}, true)
}

// RemoveTrustAnchor withdraws one of tenantID's trust anchors via the QUIC
// tenant.TrustAnchorRemove handler.
func (t *Transport) RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	_, err := t.trustAnchorCall(ctx, "RemoveTrustAnchor", "tenant.TrustAnchorRemove",
		&pb.TrustAnchorRemoveRequest{TenantId: tenantID, Fingerprint: fingerprint}, false)
	return err
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
			AllowedVolumePools:        req.Entitlements.AllowedVolumePools,
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

	ClusterSeal *transport.ClusterSeal `json:"cluster_seal,omitempty"`
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
		ClusterSeal:     wire.ClusterSeal,
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
		VolumePool:    resp.GetVolumePool(),
	}
	if r := resp.GetRefusal(); r != nil {
		info.Refusal = &transport.TenantRefusal{Code: r.GetCode(), Message: r.GetMessage()}
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
			AllowedVolumePools:        pbE.GetAllowedVolumePools(),
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
// Tenant barrier share holders
// ---------------------------------------------------------------------------

// tenantHolderRequest refuses a nil tenant holder request.
func tenantHolderRequest(method string, isNil bool) error {
	if isNil {
		return &transport.RequestError{Method: method, Err: &nilTenantBarrierRequestError{Method: method}}
	}
	return nil
}

// TenantBarrierOfficerRegister enrols a share holder of a tenant barrier.
func (t *Transport) TenantBarrierOfficerRegister(ctx context.Context, req *transport.TenantOfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if err := tenantHolderRequest("TenantBarrierOfficerRegister", req == nil); err != nil {
		return nil, err
	}
	in, err := holderconv.TenantRegisterToPB(req)
	if err != nil {
		return nil, err
	}
	var resp pb.TenantBarrierOfficerRegisterResponse
	if err := t.callPB(ctx, "tenant.BarrierOfficerRegister", in, &resp); err != nil {
		return nil, err
	}
	return holderconv.TenantRegisterFromPB(&resp), nil
}

// TenantBarrierOfficerList lists a tenant barrier's holders.
func (t *Transport) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*transport.OfficerStatus, error) {
	var resp pb.TenantBarrierOfficerListResponse
	if err := t.callPB(ctx, "tenant.BarrierOfficerList", &pb.TenantBarrierOfficerListRequest{TenantId: tenantID}, &resp); err != nil {
		return nil, err
	}
	return holderconv.StatusesFromPB(resp.GetOfficers()), nil
}

// TenantBarrierShareClaimBegin issues a claim nonce to a tenant holder.
func (t *Transport) TenantBarrierShareClaimBegin(ctx context.Context, req *transport.TenantShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if err := tenantHolderRequest("TenantBarrierShareClaimBegin", req == nil); err != nil {
		return nil, err
	}
	var resp pb.TenantBarrierShareClaimBeginResponse
	if err := t.callPB(ctx, "tenant.BarrierShareClaimBegin", &pb.TenantBarrierShareClaimBeginRequest{TenantId: req.TenantID, Username: req.Username}, &resp); err != nil {
		return nil, err
	}
	return &transport.ShareClaimBeginResponse{NonceHex: resp.GetNonceHex(), ExpiresAt: time.Unix(resp.GetExpires(), 0)}, nil
}

// TenantBarrierShareClaimComplete delivers a tenant holder's sealed envelope
// exactly once.
func (t *Transport) TenantBarrierShareClaimComplete(ctx context.Context, req *transport.TenantShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if err := tenantHolderRequest("TenantBarrierShareClaimComplete", req == nil); err != nil {
		return nil, err
	}
	var resp pb.TenantBarrierShareClaimCompleteResponse
	in := &pb.TenantBarrierShareClaimCompleteRequest{TenantId: req.TenantID, Username: req.Username, NonceHex: req.NonceHex, Signature: req.Signature}
	if err := t.callPB(ctx, "tenant.BarrierShareClaimComplete", in, &resp); err != nil {
		return nil, err
	}
	return &transport.ShareClaimCompleteResponse{ShareIndex: int(resp.GetShareIndex()), Envelope: resp.GetEnvelope()}, nil
}

// TenantBarrierQuorumBegin issues a tenant holder's quorum challenge.
func (t *Transport) TenantBarrierQuorumBegin(ctx context.Context, req *transport.TenantQuorumBeginRequest) (*transport.QuorumChallenge, error) {
	if err := tenantHolderRequest("TenantBarrierQuorumBegin", req == nil); err != nil {
		return nil, err
	}
	var resp pb.BarrierQuorumChallenge
	if err := t.callPB(ctx, "tenant.BarrierQuorumBegin", holderconv.TenantBeginToPB(req), &resp); err != nil {
		return nil, err
	}
	return holderconv.ChallengeFromPB(&resp), nil
}

// TenantBarrierQuorumSubmit submits one authenticated tenant holder's share.
func (t *Transport) TenantBarrierQuorumSubmit(ctx context.Context, req *transport.TenantQuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	if err := tenantHolderRequest("TenantBarrierQuorumSubmit", req == nil); err != nil {
		return nil, err
	}
	var resp pb.BarrierQuorumOutcome
	if err := t.callPB(ctx, "tenant.BarrierQuorumSubmit", holderconv.TenantSubmitToPB(req), &resp); err != nil {
		return nil, err
	}
	return holderconv.OutcomeFromPB(&resp), nil
}

// TenantBarrierRekeyStart opens a rekey round on a tenant barrier.
func (t *Transport) TenantBarrierRekeyStart(ctx context.Context, req *transport.TenantRekeyStartRequest) (*transport.QuorumRound, error) {
	if err := tenantHolderRequest("TenantBarrierRekeyStart", req == nil); err != nil {
		return nil, err
	}
	var resp pb.BarrierQuorumRound
	if err := t.callPB(ctx, "tenant.BarrierRekeyStart", holderconv.TenantRekeyToPB(req), &resp); err != nil {
		return nil, err
	}
	return holderconv.RoundFromPB(&resp), nil
}

// TenantBarrierOfficerRevoke revokes a tenant holder through a quorum-gated
// rekey.
func (t *Transport) TenantBarrierOfficerRevoke(ctx context.Context, req *transport.TenantOfficerRevokeRequest) (*transport.QuorumRound, error) {
	if err := tenantHolderRequest("TenantBarrierOfficerRevoke", req == nil); err != nil {
		return nil, err
	}
	var resp pb.BarrierQuorumRound
	if err := t.callPB(ctx, "tenant.BarrierOfficerRevoke", holderconv.TenantRevokeToPB(req), &resp); err != nil {
		return nil, err
	}
	return holderconv.RoundFromPB(&resp), nil
}
