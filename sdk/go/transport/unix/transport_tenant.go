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

package unix

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sealconv"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/enrollconv"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/holderconv"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/tenantconv"
)

// Compile-time interface assertion.
var _ transport.TenantClient = (*Transport)(nil)

// CreateTenant creates a new tenant via the Unix gRPC-over-UDS TenantCreate RPC.
func (t *Transport) CreateTenant(ctx context.Context, req *transport.CreateTenantRequest) (*transport.CreateTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.TenantCreate(ctx, enrollconv.TenantCreateRequest(req))
	if err != nil {
		return nil, mapGRPCError("CreateTenant", err)
	}

	return &transport.CreateTenantResponse{
		Tenant: tenantconv.ToTenantInfo(resp.GetTenant()),
		Admin:  enrollconv.AdminCredential(resp.GetAdmin()),
	}, nil
}

// AddTrustAnchors registers trust anchors of req.TenantID via the
// TrustAnchorAdd RPC.
func (t *Transport) AddTrustAnchors(ctx context.Context, req *transport.AddTrustAnchorsRequest) (*transport.TrustAnchorList, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.TrustAnchorAdd(ctx, &pb.TrustAnchorAddRequest{TenantId: req.TenantID, CertsPem: req.CertsPEM})
	if err != nil {
		return nil, mapGRPCError("AddTrustAnchors", err)
	}
	return enrollconv.TrustAnchors(resp), nil
}

// ListTrustAnchors lists tenantID's trust anchors via the TrustAnchorList
// RPC.
func (t *Transport) ListTrustAnchors(ctx context.Context, tenantID string) (*transport.TrustAnchorList, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.TrustAnchorList(ctx, &pb.TrustAnchorListRequest{TenantId: tenantID})
	if err != nil {
		return nil, mapGRPCError("ListTrustAnchors", err)
	}
	return enrollconv.TrustAnchors(resp), nil
}

// RemoveTrustAnchor withdraws one of tenantID's trust anchors via the
// TrustAnchorRemove RPC.
func (t *Transport) RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if _, err := t.client.TrustAnchorRemove(ctx, &pb.TrustAnchorRemoveRequest{TenantId: tenantID, Fingerprint: fingerprint}); err != nil {
		return mapGRPCError("RemoveTrustAnchor", err)
	}
	return nil
}

// GetTenant retrieves a tenant by ID via the Unix gRPC-over-UDS TenantGet RPC.
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

// ListTenants returns all tenants via the Unix gRPC-over-UDS TenantList RPC.
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

// ListDeletedTenants returns one page of retired tenant ids via the Unix gRPC-over-UDS
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
// serving the request via the Unix gRPC-over-UDS TenantReconcileDeleted RPC.
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

// DeleteTenant removes a tenant via the Unix gRPC-over-UDS TenantDelete RPC.
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

// UpdateTenant applies a partial update to an existing tenant via the Unix
// gRPC-over-UDS TenantUpdate RPC.
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

// UpdateTenantQuotas sets the resource quotas for a tenant via the Unix
// gRPC-over-UDS TenantUpdateQuotas RPC.
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
// Unix gRPC-over-UDS TenantUpdateEntitlements RPC.
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
			AllowedVolumePools:        req.Entitlements.AllowedVolumePools,
		},
	})
	if err != nil {
		return nil, mapGRPCError("UpdateTenantEntitlements", err)
	}

	return &transport.UpdateTenantEntitlementsResponse{Tenant: tenantconv.ToTenantInfo(resp)}, nil
}

// ---------------------------------------------------------------------------
// Per-tenant barrier RPCs (delegate to the gRPC TenantBarrierService over UDS)
// ---------------------------------------------------------------------------

// TenantBarrierInit initializes the per-tenant barrier with the given
// threshold/shares parameters via the Unix gRPC-over-UDS TenantBarrierService.
// The Unix wire shares the gRPC server descriptor, so this is a native
// protobuf unary call over the UDS connection. For the Shamir strategy the
// response carries the generated M-of-N shares; for none/passphrase the
// share fields are empty.
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

// TenantBarrierUnseal unseals the per-tenant barrier using the provided key
// material via the Unix gRPC-over-UDS TenantBarrierService. The transport
// request's Key field carries raw key material; Share is forwarded for
// callers that prefer Shamir-share semantics.
func (t *Transport) TenantBarrierUnseal(ctx context.Context, req *transport.TenantBarrierUnsealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "TenantBarrierUnseal", Err: &transport.NilRequestError{}}
	}

	if _, err := pb.NewTenantBarrierServiceClient(t.conn).Unseal(ctx, &pb.TenantBarrierUnsealRequest{
		TenantId: req.TenantID,
		Key:      req.Key,
	}); err != nil {
		return mapGRPCError("TenantBarrierUnseal", err)
	}
	return nil
}

// TenantBarrierSeal seals the per-tenant barrier via the Unix gRPC-over-UDS
// TenantBarrierService. After this call returns successfully the barrier
// refuses further reads and writes against the tenant namespace until
// TenantBarrierUnseal is invoked.
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
// strategy metadata for the per-tenant barrier via the Unix gRPC-over-UDS
// TenantBarrierService.
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
		ClusterSeal:     sealconv.FromProto(resp.GetClusterSeal()),
	}, nil
}

// ---------------------------------------------------------------------------
// Tenant barrier share holders
// ---------------------------------------------------------------------------

// TenantBarrierOfficerRegister enrols a share holder of a tenant barrier.
func (t *Transport) TenantBarrierOfficerRegister(ctx context.Context, req *transport.TenantOfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerRegister", Err: &transport.NilRequestError{}}
	}
	in, err := holderconv.TenantRegisterToPB(req)
	if err != nil {
		return nil, err
	}
	resp, err := pb.NewTenantBarrierServiceClient(t.conn).OfficerRegister(ctx, in)
	if err != nil {
		return nil, mapGRPCError("TenantBarrierOfficerRegister", err)
	}
	return holderconv.TenantRegisterFromPB(resp), nil
}

// TenantBarrierOfficerList lists a tenant barrier's holders.
func (t *Transport) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := pb.NewTenantBarrierServiceClient(t.conn).OfficerList(ctx, &pb.TenantBarrierOfficerListRequest{TenantId: tenantID})
	if err != nil {
		return nil, mapGRPCError("TenantBarrierOfficerList", err)
	}
	return holderconv.StatusesFromPB(resp.GetOfficers()), nil
}

// TenantBarrierShareClaimBegin issues a claim nonce to a tenant holder.
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
	return &transport.ShareClaimBeginResponse{NonceHex: resp.GetNonceHex(), ExpiresAt: time.Unix(resp.GetExpires(), 0)}, nil
}

// TenantBarrierShareClaimComplete delivers a tenant holder's sealed envelope
// exactly once.
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
	return &transport.ShareClaimCompleteResponse{ShareIndex: int(resp.GetShareIndex()), Envelope: resp.GetEnvelope()}, nil
}

// TenantBarrierQuorumBegin issues a tenant holder's quorum challenge.
func (t *Transport) TenantBarrierQuorumBegin(ctx context.Context, req *transport.TenantQuorumBeginRequest) (*transport.QuorumChallenge, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierQuorumBegin", Err: &transport.NilRequestError{}}
	}
	resp, err := pb.NewTenantBarrierServiceClient(t.conn).QuorumBegin(ctx, holderconv.TenantBeginToPB(req))
	if err != nil {
		return nil, mapGRPCError("TenantBarrierQuorumBegin", err)
	}
	return holderconv.ChallengeFromPB(resp), nil
}

// TenantBarrierQuorumSubmit submits one authenticated tenant holder's share.
func (t *Transport) TenantBarrierQuorumSubmit(ctx context.Context, req *transport.TenantQuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierQuorumSubmit", Err: &transport.NilRequestError{}}
	}
	resp, err := pb.NewTenantBarrierServiceClient(t.conn).QuorumSubmit(ctx, holderconv.TenantSubmitToPB(req))
	if err != nil {
		return nil, mapGRPCError("TenantBarrierQuorumSubmit", err)
	}
	return holderconv.OutcomeFromPB(resp), nil
}

// TenantBarrierRekeyStart opens a rekey round on a tenant barrier.
func (t *Transport) TenantBarrierRekeyStart(ctx context.Context, req *transport.TenantRekeyStartRequest) (*transport.QuorumRound, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierRekeyStart", Err: &transport.NilRequestError{}}
	}
	resp, err := pb.NewTenantBarrierServiceClient(t.conn).RekeyStart(ctx, holderconv.TenantRekeyToPB(req))
	if err != nil {
		return nil, mapGRPCError("TenantBarrierRekeyStart", err)
	}
	return holderconv.RoundFromPB(resp), nil
}

// TenantBarrierOfficerRevoke revokes a tenant holder through a quorum-gated
// rekey.
func (t *Transport) TenantBarrierOfficerRevoke(ctx context.Context, req *transport.TenantOfficerRevokeRequest) (*transport.QuorumRound, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerRevoke", Err: &transport.NilRequestError{}}
	}
	resp, err := pb.NewTenantBarrierServiceClient(t.conn).OfficerRevoke(ctx, holderconv.TenantRevokeToPB(req))
	if err != nil {
		return nil, mapGRPCError("TenantBarrierOfficerRevoke", err)
	}
	return holderconv.RoundFromPB(resp), nil
}
