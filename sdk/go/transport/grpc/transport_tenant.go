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

package grpc

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/tenantconv"
)

// Compile-time interface assertion.
var _ transport.TenantClient = (*Transport)(nil)

// CreateTenant creates a new tenant via the gRPC TenantCreate RPC.
func (t *Transport) CreateTenant(ctx context.Context, req *transport.CreateTenantRequest) (*transport.CreateTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.TenantCreate(ctx, &pb.TenantCreateRequest{
		Id:                req.ID,
		Name:              req.Name,
		IsolationType:     string(req.IsolationType),
		BarrierAutoUnseal: req.BarrierAutoUnseal,
		DataRoot:          req.DataRoot,
	})
	if err != nil {
		return nil, mapGRPCError("CreateTenant", err)
	}

	return &transport.CreateTenantResponse{Tenant: tenantconv.ToTenantInfo(resp)}, nil
}

// GetTenant retrieves a tenant by ID via the gRPC TenantGet RPC.
func (t *Transport) GetTenant(ctx context.Context, tenantID string) (*transport.GetTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.TenantGet(ctx, &pb.TenantGetRequest{Id: tenantID})
	if err != nil {
		return nil, mapGRPCError("GetTenant", err)
	}

	return &transport.GetTenantResponse{Tenant: tenantconv.ToTenantInfo(resp)}, nil
}

// ListTenants returns all tenants via the gRPC TenantList RPC.
func (t *Transport) ListTenants(ctx context.Context) (*transport.ListTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.TenantList(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("ListTenants", err)
	}

	tenants := make([]transport.TenantInfo, len(resp.GetTenants()))
	for i, pt := range resp.GetTenants() {
		tenants[i] = tenantconv.ToTenantInfo(pt)
	}

	return &transport.ListTenantsResponse{Tenants: tenants}, nil
}

// ListDeletedTenants returns one page of retired tenant ids via the gRPC
// TenantListDeleted RPC.
func (t *Transport) ListDeletedTenants(ctx context.Context, req *transport.ListDeletedTenantsRequest) (*transport.ListDeletedTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		req = &transport.ListDeletedTenantsRequest{}
	}
	resp, err := t.client.TenantListDeleted(ctx, &pb.TenantListDeletedRequest{
		PageSize:  req.PageSize,
		PageToken: req.PageToken,
	})
	if err != nil {
		return nil, mapGRPCError("ListDeletedTenants", err)
	}
	return tenantconv.ToListDeletedTenantsResponse(resp), nil
}

// ReconcileDeletedTenants runs the deleted-tenant reconcile on the node
// serving the request via the gRPC TenantReconcileDeleted RPC.
func (t *Transport) ReconcileDeletedTenants(ctx context.Context) (*transport.ReconcileDeletedTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.TenantReconcileDeleted(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("ReconcileDeletedTenants", err)
	}
	out := tenantconv.ToReconcileDeletedTenantsResponse(resp)
	return out, transport.ReconcileFailure(out, nil)
}

// DeleteTenant removes a tenant via the gRPC TenantDelete RPC.
func (t *Transport) DeleteTenant(ctx context.Context, tenantID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.TenantDelete(ctx, &pb.TenantDeleteRequest{Id: tenantID})
	if err != nil {
		return mapGRPCError("DeleteTenant", err)
	}

	return nil
}

// UpdateTenant applies a partial update to an existing tenant via the gRPC TenantUpdate RPC.
func (t *Transport) UpdateTenant(ctx context.Context, req *transport.UpdateTenantRequest) (*transport.UpdateTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.TenantUpdate(ctx, &pb.TenantUpdateRequest{
		Id:            req.TenantID,
		Name:          req.Name,
		IsolationType: string(req.IsolationType),
	})
	if err != nil {
		return nil, mapGRPCError("UpdateTenant", err)
	}

	return &transport.UpdateTenantResponse{Tenant: tenantconv.ToTenantInfo(resp)}, nil
}

// UpdateTenantQuotas sets the resource quotas for a tenant via the gRPC TenantUpdateQuotas RPC.
func (t *Transport) UpdateTenantQuotas(ctx context.Context, req *transport.UpdateTenantQuotasRequest) (*transport.UpdateTenantQuotasResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.TenantUpdateQuotas(ctx, &pb.TenantUpdateQuotasRequest{
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
		return nil, mapGRPCError("UpdateTenantQuotas", err)
	}

	return &transport.UpdateTenantQuotasResponse{Tenant: tenantconv.ToTenantInfo(resp)}, nil
}

// UpdateTenantEntitlements sets the feature entitlements for a tenant via the
// gRPC TenantUpdateEntitlements RPC.
func (t *Transport) UpdateTenantEntitlements(ctx context.Context, req *transport.UpdateTenantEntitlementsRequest) (*transport.UpdateTenantEntitlementsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.TenantUpdateEntitlements(ctx, &pb.TenantUpdateEntitlementsRequest{
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
		return nil, mapGRPCError("UpdateTenantEntitlements", err)
	}

	return &transport.UpdateTenantEntitlementsResponse{Tenant: tenantconv.ToTenantInfo(resp)}, nil
}

// ---------------------------------------------------------------------------
// Per-tenant barrier RPCs (native protobuf: qrdb.v1.TenantBarrierService).
// ---------------------------------------------------------------------------

// TenantBarrierInit initializes a per-tenant barrier via the
// TenantBarrierService.Init RPC. For the Shamir strategy the response carries
// the generated M-of-N shares; for none/passphrase the share fields are empty.
func (t *Transport) TenantBarrierInit(ctx context.Context, req *transport.TenantBarrierInitRequest) (*transport.TenantBarrierInitResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: &transport.NilRequestError{}}
	}

	resp, err := pb.NewTenantBarrierServiceClient(t.conn).Init(ctx, &pb.TenantBarrierInitRequest{
		TenantId:   req.TenantID,
		Passphrase: req.Passphrase,
		Threshold:  int32(req.Threshold), //nolint:gosec // share threshold is a small count
		Shares:     int32(req.Shares),    //nolint:gosec // share count is small
		Strategy:   req.Strategy,
	})
	if err != nil {
		return nil, mapGRPCError("TenantBarrierInit", err)
	}
	return &transport.TenantBarrierInitResponse{
		TenantID:    resp.GetTenantId(),
		Strategy:    resp.GetStrategy(),
		Threshold:   int(resp.GetThreshold()),
		TotalShares: int(resp.GetTotalShares()),
		ShareCount:  int(resp.GetShareCount()),
	}, nil
}

// TenantBarrierUnsealShare submits a single Shamir share toward a per-tenant
// barrier's quorum threshold via the TenantBarrierService.UnsealShare RPC.
func (t *Transport) TenantBarrierUnsealShare(ctx context.Context, req *transport.TenantBarrierUnsealShareRequest) (*transport.TenantBarrierUnsealProgress, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierUnsealShare", Err: &transport.NilRequestError{}}
	}

	resp, err := pb.NewTenantBarrierServiceClient(t.conn).UnsealShare(ctx, &pb.TenantBarrierUnsealShareRequest{
		TenantId: req.TenantID,
		Share:    req.Share,
	})
	if err != nil {
		return nil, mapGRPCError("TenantBarrierUnsealShare", err)
	}
	return &transport.TenantBarrierUnsealProgress{
		TenantID:  resp.GetTenantId(),
		Required:  int(resp.GetRequired()),
		Submitted: int(resp.GetSubmitted()),
		Complete:  resp.GetComplete(),
		Sealed:    resp.GetSealed(),
	}, nil
}

// TenantBarrierUnseal unseals a per-tenant barrier with single-key material via
// the TenantBarrierService.Unseal RPC. Key takes precedence over Share.
func (t *Transport) TenantBarrierUnseal(ctx context.Context, req *transport.TenantBarrierUnsealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "TenantBarrierUnseal", Err: &transport.NilRequestError{}}
	}

	key := req.Key
	if len(key) == 0 {
		key = req.Share
	}
	if _, err := pb.NewTenantBarrierServiceClient(t.conn).Unseal(ctx, &pb.TenantBarrierUnsealRequest{
		TenantId: req.TenantID,
		Key:      key,
	}); err != nil {
		return mapGRPCError("TenantBarrierUnseal", err)
	}
	return nil
}

// TenantBarrierSeal seals a per-tenant barrier via the TenantBarrierService.Seal RPC.
func (t *Transport) TenantBarrierSeal(ctx context.Context, req *transport.TenantBarrierSealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "TenantBarrierSeal", Err: &transport.NilRequestError{}}
	}

	if _, err := pb.NewTenantBarrierServiceClient(t.conn).Seal(ctx, &pb.TenantBarrierSealRequest{
		TenantId: req.TenantID,
	}); err != nil {
		return mapGRPCError("TenantBarrierSeal", err)
	}
	return nil
}

// TenantBarrierStatus returns the current sealed/initialized state and active
// strategy metadata for a per-tenant barrier via the TenantBarrierService.Status RPC.
func (t *Transport) TenantBarrierStatus(ctx context.Context, req *transport.TenantBarrierStatusRequest) (*transport.TenantBarrierStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: &transport.NilRequestError{}}
	}

	resp, err := pb.NewTenantBarrierServiceClient(t.conn).Status(ctx, &pb.TenantBarrierStatusRequest{
		TenantId: req.TenantID,
	})
	if err != nil {
		return nil, mapGRPCError("TenantBarrierStatus", err)
	}
	return &transport.TenantBarrierStatusResponse{
		TenantID:        resp.GetTenantId(),
		Sealed:          resp.GetSealed(),
		Initialized:     resp.GetInitialized(),
		Strategy:        resp.GetStrategy(),
		HardwareBacked:  resp.GetHardwareBacked(),
		ShamirThreshold: int(resp.GetShamirThreshold()),
		ShamirTotal:     int(resp.GetShamirTotal()),
	}, nil
}

// ---------------------------------------------------------------------------
// Tenant barrier officer / share-claim operations
// ---------------------------------------------------------------------------

// TenantBarrierOfficerRegister registers a Security Officer for a specific
// tenant's barrier via the TenantBarrierService.OfficerRegister RPC.
func (t *Transport) TenantBarrierOfficerRegister(ctx context.Context, req *transport.TenantOfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerRegister", Err: &transport.NilRequestError{}}
	}
	resp, err := pb.NewTenantBarrierServiceClient(t.conn).OfficerRegister(ctx, &pb.TenantBarrierOfficerRegisterRequest{
		TenantId:          req.TenantID,
		Username:          req.Username,
		CsrPem:            req.CSRPEM,
		AuthenticatorType: req.AuthenticatorType,
	})
	if err != nil {
		return nil, mapGRPCError("TenantBarrierOfficerRegister", err)
	}
	return &transport.OfficerRegisterResponse{
		CertPEM:    resp.GetCertPem(),
		ShareIndex: int(resp.GetShareIndex()),
	}, nil
}

// TenantBarrierOfficerList lists all registered Security Officers for a
// specific tenant's barrier via the TenantBarrierService.OfficerList RPC.
func (t *Transport) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := pb.NewTenantBarrierServiceClient(t.conn).OfficerList(ctx, &pb.TenantBarrierOfficerListRequest{
		TenantId: tenantID,
	})
	if err != nil {
		return nil, mapGRPCError("TenantBarrierOfficerList", err)
	}
	return protoToOfficerStatusSlice(resp.GetOfficers()), nil
}

// TenantBarrierOfficerClaimStatus returns per-officer claim status for a
// specific tenant's barrier via the TenantBarrierService.OfficerClaimStatus RPC.
func (t *Transport) TenantBarrierOfficerClaimStatus(ctx context.Context, tenantID string) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := pb.NewTenantBarrierServiceClient(t.conn).OfficerClaimStatus(ctx, &pb.TenantBarrierOfficerClaimStatusRequest{
		TenantId: tenantID,
	})
	if err != nil {
		return nil, mapGRPCError("TenantBarrierOfficerClaimStatus", err)
	}
	return protoToOfficerStatusSlice(resp.GetOfficers()), nil
}

// TenantBarrierShareClaimBegin issues a challenge nonce for a tenant barrier
// Security Officer via TenantBarrierService.ShareClaimBegin.
func (t *Transport) TenantBarrierShareClaimBegin(ctx context.Context, req *transport.TenantShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShareClaimBegin", Err: &transport.NilRequestError{}}
	}
	resp, err := pb.NewTenantBarrierServiceClient(t.conn).ShareClaimBegin(ctx, &pb.TenantBarrierShareClaimBeginRequest{
		TenantId: req.TenantID,
		Username: req.Username,
	})
	if err != nil {
		return nil, mapGRPCError("TenantBarrierShareClaimBegin", err)
	}
	return &transport.ShareClaimBeginResponse{
		NonceHex:  resp.GetNonceHex(),
		ExpiresAt: time.Unix(resp.GetExpires(), 0),
	}, nil
}

// TenantBarrierShareClaimComplete verifies the tenant barrier officer's nonce
// signature and returns the share bytes exactly once via
// TenantBarrierService.ShareClaimComplete.
func (t *Transport) TenantBarrierShareClaimComplete(ctx context.Context, req *transport.TenantShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShareClaimComplete", Err: &transport.NilRequestError{}}
	}
	resp, err := pb.NewTenantBarrierServiceClient(t.conn).ShareClaimComplete(ctx, &pb.TenantBarrierShareClaimCompleteRequest{
		TenantId:  req.TenantID,
		Username:  req.Username,
		NonceHex:  req.NonceHex,
		Signature: req.Signature,
	})
	if err != nil {
		return nil, mapGRPCError("TenantBarrierShareClaimComplete", err)
	}
	return &transport.ShareClaimCompleteResponse{
		ShareBytes: resp.GetShareBytes(),
	}, nil
}

// TenantBarrierShamirSealVote submits a Shamir share as a seal vote for a
// specific tenant's barrier via TenantBarrierService.ShamirSealVote.
func (t *Transport) TenantBarrierShamirSealVote(ctx context.Context, req *transport.TenantShamirSealVoteRequest) (*transport.ShamirSealVoteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShamirSealVote", Err: &transport.NilRequestError{}}
	}
	resp, err := pb.NewTenantBarrierServiceClient(t.conn).ShamirSealVote(ctx, &pb.TenantBarrierShamirSealVoteRequest{
		TenantId:   req.TenantID,
		ShareValue: req.Share,
	})
	if err != nil {
		return nil, mapGRPCError("TenantBarrierShamirSealVote", err)
	}
	return &transport.ShamirSealVoteResponse{
		Required:  int(resp.GetRequired()),
		Submitted: int(resp.GetSubmitted()),
		Complete:  resp.GetComplete(),
	}, nil
}
