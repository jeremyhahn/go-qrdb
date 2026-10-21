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

	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/enrollconv"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/holderconv"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/peertls"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sealconv"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/tenantconv"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/tenantinit"
)

// Compile-time interface assertion.
var _ transport.TenantClient = (*Transport)(nil)

// CreateTenant creates a new tenant via the gRPC TenantCreate RPC.
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

	resp, err := t.client.TenantUpdate(ctx, tenantconv.UpdateRequest(req))
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
			AllowedVolumePools:        req.Entitlements.AllowedVolumePools,
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
// TenantBarrierService InitBegin and InitComplete RPCs, proving possession of
// req.Signer's key over the connection InitBegin answered on. Share material
// is never returned.
func (t *Transport) TenantBarrierInit(ctx context.Context, req *transport.TenantBarrierInitRequest) (*transport.TenantBarrierInitResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: &transport.NilRequestError{}}
	}
	officers, err := holderconv.TenantBarrierRosterToPB(req.Officers)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: err}
	}

	client := pb.NewTenantBarrierServiceClient(t.conn)
	return tenantinit.Run(req, func(round tenantinit.Round) (*transport.TenantBarrierInitResponse, error) {
		var p peer.Peer
		begin, err := client.InitBegin(ctx, &pb.TenantBarrierInitBeginRequest{TenantId: req.TenantID}, grpc.Peer(&p))
		if err != nil {
			return nil, mapGRPCError("TenantBarrierInit", err)
		}
		proof, err := transport.ProveTenantBarrierInit(req.Signer, req.TenantID, begin.GetNonce(), peertls.State(&p))
		if err != nil {
			return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: err}
		}
		wire := &pb.TenantBarrierInitRequest{
			TenantId:       req.TenantID,
			Passphrase:     req.Passphrase,
			Threshold:      int32(req.Threshold), //nolint:gosec // share threshold is a small count
			Shares:         int32(req.Shares),    //nolint:gosec // share count is small
			Strategy:       req.Strategy,
			Officers:       officers,
			Policy:         req.Policy,
			Nonce:          proof.Nonce,
			PublicKey:      proof.PublicKey,
			Signature:      proof.Signature,
			InitStatement:  round.Statement,
			Roster:         round.Roster,
			UnsealKeyCheck: round.UnsealKeyCheck,
		}
		if round.Complete {
			wire.Officers, wire.Policy = nil, ""
		}
		resp, err := client.InitComplete(ctx, wire)
		if err != nil {
			return nil, mapGRPCError("TenantBarrierInit", err)
		}
		return holderconv.TenantBarrierInitResponseFromPB(resp), nil
	})
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

	if _, err := pb.NewTenantBarrierServiceClient(t.conn).Unseal(ctx, &pb.TenantBarrierUnsealRequest{
		TenantId: req.TenantID,
		Key:      req.Key,
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
		ClusterSeal:     sealconv.FromProto(resp.GetClusterSeal()),
		KeyFingerprint:  resp.GetKeyFingerprint(),
		Nodes:           holderconv.TenantBarrierNodesFromPB(resp.GetNodes()),
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
