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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Tenant handlers
// ---------------------------------------------------------------------------

// TenantCreate creates a new tenant via the tenant service.
func (s *qrdbServiceServer) TenantCreate(ctx context.Context, req *pb.TenantCreateRequest) (*pb.TenantCreateResponse, error) {
	if s.onboarding == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant onboarding"})
	}

	result, err := s.onboarding.OnboardTenant(ctx, &service.OnboardTenantRequest{
		ID:                req.GetId(),
		Name:              req.GetName(),
		IsolationType:     tenant.IsolationType(req.GetIsolationType()),
		BarrierAutoUnseal: req.GetBarrierAutoUnseal(),
		VolumePool:        req.GetVolumePool(),
		AdminUsername:     req.GetAdminUsername(),
		AdminCSRPEM:       []byte(req.GetAdminCsrPem()),
		AdminCertPEM:      []byte(req.GetAdminCertPem()),
		AdminChainPEM:     []byte(req.GetAdminChainPem()),
		TrustAnchorsPEM:   []byte(req.GetTrustAnchorsPem()),
		SOUsername:        req.GetSoUsername(),
		SOCSRPEM:          []byte(req.GetSoCsrPem()),
		SOCertPEM:         []byte(req.GetSoCertPem()),
		SOChainPEM:        []byte(req.GetSoChainPem()),
		SOPIN:             []byte(req.GetSoPin()),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.TenantCreateResponse{
		Tenant: tenantToProto(result.Tenant),
		Admin:  tenantAdminCredentialToProto(&result.Admin),
		So:     tenantAdminCredentialToProto(result.SO),
	}, nil
}

// tenantAdminCredentialToProto renders an account onboarding enrolled; nil
// for none.
func tenantAdminCredentialToProto(c *service.TenantAdminCredential) *pb.TenantAdminCredential {
	if c == nil {
		return nil
	}
	return &pb.TenantAdminCredential{
		UserId:            c.UserID,
		Username:          c.Username,
		CertPem:           string(c.CertPEM),
		Serial:            c.Serial,
		Thumbprint:        c.Thumbprint,
		AwaitsBarrierInit: c.AwaitsBarrierInit,
	}
}

// TenantGet retrieves a tenant by ID.
func (s *qrdbServiceServer) TenantGet(ctx context.Context, req *pb.TenantGetRequest) (*pb.TenantResponse, error) {
	if s.tenantSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant"})
	}

	if err := s.authorizeTenantTarget(ctx, req.GetId(), permission.TenantsRead); err != nil {
		return nil, err
	}

	info, err := s.tenantSvc.GetTenant(ctx, req.GetId())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return tenantToProto(info), nil
}

// TenantList returns metadata for every tenant to a caller holding
// tenants:list:cross, and for the caller's own tenant only to anyone else.
func (s *qrdbServiceServer) TenantList(ctx context.Context, _ *emptypb.Empty) (*pb.TenantListResponse, error) {
	if s.tenantSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant"})
	}

	tenants, err := s.tenantSvc.ListTenants(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	// Every tenant to a tenants:list:cross holder, the caller's own to anyone
	// else (authmw.FilterTenantList).
	if s.authMW != nil {
		if tenants, err = s.authMW.FilterTenantList(ctx, tenants); err != nil {
			return nil, authmw.GRPCError(err, codes.PermissionDenied)
		}
	}

	pbTenants := make([]*pb.TenantResponse, len(tenants))
	for i := range tenants {
		pbTenants[i] = tenantToProto(&tenants[i])
	}

	return &pb.TenantListResponse{Tenants: pbTenants}, nil
}

// TenantListDeleted returns one page of retired tenant ids.
func (s *qrdbServiceServer) TenantListDeleted(ctx context.Context, req *pb.TenantListDeletedRequest) (*pb.TenantListDeletedResponse, error) {
	if s.tenantSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant"})
	}
	page, err := s.tenantSvc.ListDeletedTenants(ctx, req.GetPageToken(), int(req.GetPageSize()))
	if err != nil {
		return nil, mapServiceError(err)
	}
	return DeletedTenantsPageToProto(page), nil
}

// TenantReconcileDeleted reclaims, on this node, the residue of retired
// tenant ids.
func (s *qrdbServiceServer) TenantReconcileDeleted(ctx context.Context, _ *emptypb.Empty) (*pb.TenantReconcileDeletedResponse, error) {
	if s.tenantSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant"})
	}
	report, err := s.tenantSvc.ReconcileDeletedTenants(ctx)
	// A pass that failed only for some ids answers with its report, whose
	// failed list names them; only a guard failure is an error status.
	if rerr := tenant.ReconcileResponseError(report, err); rerr != nil {
		return nil, mapServiceError(rerr)
	}
	return ReconcileReportToProto(report), nil
}

// DeletedTenantsPageToProto converts a page of retired tenant ids to its
// protobuf form. It is exported because every transport that speaks the
// qrdb.v1 messages -- gRPC, the Unix socket and QUIC -- encodes the page the
// same way, and one conversion keeps them from drifting.
func DeletedTenantsPageToProto(page *tenant.DeletedTenantsPage) *pb.TenantListDeletedResponse {
	resp := &pb.TenantListDeletedResponse{}
	if page == nil {
		return resp
	}
	resp.NextPageToken = page.NextPageToken
	resp.Tenants = make([]*pb.DeletedTenant, len(page.Tombstones))
	for i := range page.Tombstones {
		ts := &page.Tombstones[i]
		dt := &pb.DeletedTenant{
			Id:            ts.ID,
			Name:          ts.Name,
			IsolationType: string(ts.IsolationType),
			VolumePool:    ts.VolumePool,
		}
		if !ts.CreatedAt.IsZero() {
			dt.CreatedAt = ts.CreatedAt.UTC().Format(time.RFC3339)
		}
		if !ts.DeletedAt.IsZero() {
			dt.DeletedAt = ts.DeletedAt.UTC().Format(time.RFC3339)
		}
		resp.Tenants[i] = dt
	}
	return resp
}

// ReconcileReportToProto converts a deleted-tenant reconcile report to its
// protobuf form; see DeletedTenantsPageToProto for why it is exported.
func ReconcileReportToProto(report *tenant.ReconcileReport) *pb.TenantReconcileDeletedResponse {
	resp := &pb.TenantReconcileDeletedResponse{}
	if report == nil {
		return resp
	}
	resp.Inspected = int32(report.Inspected) //nolint:gosec // bounded by the number of deletions
	for _, r := range report.Reclaimed {
		resp.Reclaimed = append(resp.Reclaimed, &pb.TenantResiduePath{TenantId: r.TenantID, Path: r.Path})
	}
	for _, s := range report.Skipped {
		resp.Skipped = append(resp.Skipped, &pb.TenantResiduePath{TenantId: s.TenantID, Path: s.Path, Reason: s.Reason})
	}
	for _, f := range report.Failed {
		resp.Failed = append(resp.Failed, &pb.TenantResidueFailure{TenantId: f.TenantID, Op: f.Op, Path: f.Path, Error: f.Error})
	}
	return resp
}

// TenantUpdate applies a partial update to an existing tenant.
func (s *qrdbServiceServer) TenantUpdate(ctx context.Context, req *pb.TenantUpdateRequest) (*pb.TenantResponse, error) {
	if s.tenantSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant"})
	}

	patch := tenant.TenantPatch{
		Name:          req.GetName(),
		IsolationType: tenant.IsolationType(req.GetIsolationType()),
		Description:   req.Description,
	}
	if req.Status != nil {
		s := tenant.Status(req.GetStatus())
		patch.Status = &s
	}
	// A present metadata message replaces the map; its empty entries clear it.
	if md := req.GetMetadata(); md != nil {
		patch.Metadata = md.GetEntries()
		if patch.Metadata == nil {
			patch.Metadata = map[string]string{}
		}
	}
	if req.IsolationPolicy != nil {
		patch.IsolationPolicy = tenant.TenantIsolationPolicy(req.GetIsolationPolicy())
	}

	if err := s.authorizeTenantUpdate(ctx, req.GetId(), patch); err != nil {
		return nil, err
	}
	if req.IsolationPolicy != nil && patch.IsolationPolicy == tenant.TenantIsolationPolicyInherit {
		if err := refuseIsolationPolicyLoosening(ctx, s.tenantSvc, req.GetId()); err != nil {
			return nil, mapServiceError(err)
		}
	}

	info, err := s.tenantSvc.UpdateTenant(ctx, req.GetId(), patch)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return tenantToProto(info), nil
}

// refuseIsolationPolicyLoosening refuses an explicit request to move tenant
// id back to inherit once it is strict. tenant.TenantPatch reads an empty
// IsolationPolicy as "unchanged", so the inherit request is checked here.
func refuseIsolationPolicyLoosening(ctx context.Context, svc tenant.TenantServicer, id string) error {
	info, err := svc.GetTenant(ctx, id)
	if err != nil {
		return err
	}
	if info.IsolationPolicy == tenant.TenantIsolationPolicyStrict {
		return &tenant.IsolationPolicyChangeError{From: string(tenant.TenantIsolationPolicyStrict), To: "inherit"}
	}
	return nil
}

// GetClusterIsolationPolicy returns the cluster-wide isolation floor; an
// unset floor reads as operator-access.
func (s *qrdbServiceServer) GetClusterIsolationPolicy(ctx context.Context, _ *pb.GetClusterIsolationPolicyRequest) (*pb.ClusterIsolationPolicyResponse, error) {
	if s.tenantSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant"})
	}
	policy, err := s.tenantSvc.ClusterIsolationPolicy(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.ClusterIsolationPolicyResponse{Policy: string(policy)}, nil
}

// SetClusterIsolationPolicy sets the cluster-wide isolation floor. It is
// tighten-only: a loosening is *tenant.IsolationPolicyChangeError,
// FAILED_PRECONDITION.
func (s *qrdbServiceServer) SetClusterIsolationPolicy(ctx context.Context, req *pb.SetClusterIsolationPolicyRequest) (*pb.ClusterIsolationPolicyResponse, error) {
	if s.tenantSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant"})
	}
	if err := s.tenantSvc.SetClusterIsolationPolicy(ctx, tenant.ClusterIsolationPolicy(req.GetPolicy())); err != nil {
		return nil, mapServiceError(err)
	}
	return s.GetClusterIsolationPolicy(ctx, nil)
}

// TenantDelete removes a tenant and its metadata.
func (s *qrdbServiceServer) TenantDelete(ctx context.Context, req *pb.TenantDeleteRequest) (*emptypb.Empty, error) {
	if s.tenantSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant"})
	}

	if err := s.authorizeTenantTarget(ctx, req.GetId(), permission.TenantsDelete); err != nil {
		return nil, err
	}

	if err := s.tenantSvc.DeleteTenant(ctx, req.GetId()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// TenantUpdateQuotas sets the resource quotas for an existing tenant.
func (s *qrdbServiceServer) TenantUpdateQuotas(ctx context.Context, req *pb.TenantUpdateQuotasRequest) (*pb.TenantResponse, error) {
	if s.tenantSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant"})
	}

	pbQ := req.GetQuotas()
	if pbQ == nil {
		return nil, status.Error(codes.InvalidArgument, "quotas is required")
	}

	q := &quota.Quotas{
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

	if err := s.authorizeTenantTarget(ctx, req.GetId(), permission.TenantsSetQuotas); err != nil {
		return nil, err
	}

	info, err := s.tenantSvc.UpdateQuotas(ctx, req.GetId(), q)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return tenantToProto(info), nil
}

// TenantUpdateEntitlements sets the feature entitlements for an existing tenant.
func (s *qrdbServiceServer) TenantUpdateEntitlements(ctx context.Context, req *pb.TenantUpdateEntitlementsRequest) (*pb.TenantResponse, error) {
	if s.tenantSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant"})
	}

	pbE := req.GetEntitlements()
	if pbE == nil {
		return nil, status.Error(codes.InvalidArgument, "entitlements is required")
	}

	e := &quota.Entitlements{
		AllowedIsolationModes:     pbE.GetAllowedIsolationModes(),
		AllowDedicatedRaft:        pbE.GetAllowDedicatedRaft(),
		AllowVolumeStorage:        pbE.GetAllowVolumeStorage(),
		AllowSystemManagedBarrier: pbE.GetAllowSystemManagedBarrier(),
		AllowedBarrierModes:       pbE.GetAllowedBarrierModes(),
		AllowDedicatedWAL:         pbE.GetAllowDedicatedWal(),
		AllowedVolumePools:        pbE.GetAllowedVolumePools(),
	}

	if err := s.authorizeTenantTarget(ctx, req.GetId(), permission.TenantsSetEntitlements); err != nil {
		return nil, err
	}

	info, err := s.tenantSvc.UpdateEntitlements(ctx, req.GetId(), e)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return tenantToProto(info), nil
}

// ---------------------------------------------------------------------------
// Tenant conversion helpers
// ---------------------------------------------------------------------------

// tenantToProto converts a tenant.TenantInfo to its protobuf representation.
func tenantToProto(info *tenant.TenantInfo) *pb.TenantResponse {
	if info == nil {
		return nil
	}
	resp := &pb.TenantResponse{
		Id:              info.ID,
		Name:            info.Name,
		IsolationType:   string(info.EffectiveIsolationType()),
		CreatedAt:       info.CreatedAt.Format(time.RFC3339),
		UpdatedAt:       info.UpdatedAt.Format(time.RFC3339),
		VolumePool:      info.VolumePool,
		Description:     info.Description,
		Metadata:        info.Metadata,
		Status:          string(info.Status),
		IsolationPolicy: string(info.IsolationPolicy),
	}
	if info.Quotas != nil {
		resp.Quotas = quotaToProto(info.Quotas)
	}
	if info.Entitlements != nil {
		resp.Entitlements = entitlementsToProto(info.Entitlements)
	}
	if refusal := tenant.RefusalOf(info.Refusal); refusal != nil {
		resp.Refusal = &pb.TenantRefusal{Code: refusal.Code, Message: refusal.Message}
	}
	return resp
}

// entitlementsToProto converts a quota.Entitlements to its protobuf form.
func entitlementsToProto(e *quota.Entitlements) *pb.TenantEntitlements {
	if e == nil {
		return nil
	}
	return &pb.TenantEntitlements{
		AllowedIsolationModes:     e.AllowedIsolationModes,
		AllowDedicatedRaft:        e.AllowDedicatedRaft,
		AllowVolumeStorage:        e.AllowVolumeStorage,
		AllowSystemManagedBarrier: e.AllowSystemManagedBarrier,
		AllowedBarrierModes:       e.AllowedBarrierModes,
		AllowDedicatedWal:         e.AllowDedicatedWAL,
		AllowedVolumePools:        e.AllowedVolumePools,
	}
}

// quotaToProto converts a quota.Quotas to its protobuf representation.
func quotaToProto(q *quota.Quotas) *pb.TenantQuotas {
	if q == nil {
		return nil
	}
	return &pb.TenantQuotas{
		MaxDatabases:          q.MaxDatabases,
		MaxRaftGroups:         q.MaxRaftGroups,
		MaxReplicasPerGroup:   q.MaxReplicasPerGroup,
		MaxTotalReplicas:      q.MaxTotalReplicas,
		MaxCollections:        q.MaxCollections,
		MaxKeys:               q.MaxKeys,
		MaxStorageBytes:       q.MaxStorageBytes,
		MaxDatabaseSizeBytes:  q.MaxDatabaseSizeBytes,
		MaxUsers:              q.MaxUsers,
		MaxRoles:              q.MaxRoles,
		MaxCertificates:       q.MaxCertificates,
		MaxBackups:            q.MaxBackups,
		MaxVectorDims:         q.MaxVectorDims,
		MaxTsMetrics:          q.MaxTSMetrics,
		SystemBarrierQuota:    q.SystemBarrierQuota,
		MaxDedicatedWalGroups: q.MaxDedicatedWALGroups,
	}
}

// ---------------------------------------------------------------------------
// Tenant error mapping
// ---------------------------------------------------------------------------

// authorizeTenantTarget re-checks perm against the tenant the REQUEST names
// (authmw.AuthorizeTenantTarget). The interceptor checked it against the
// caller's own tenant only, which would let a tenant-local Admin read,
// change or delete any tenant.
func (s *qrdbServiceServer) authorizeTenantTarget(ctx context.Context, tenantID string, perm permission.Permission) error {
	return s.authorizeTenantRequest(tenantID, func(mw *authmw.AuthMiddleware) error {
		return mw.AuthorizeTenantTarget(ctx, tenantID, perm)
	})
}

// authorizeTenantUpdate authorizes patch against the tenant the REQUEST names
// (authmw.AuthorizeTenantUpdate): tenants:update for a rename, plus
// tenants:set_isolation when the patch changes the isolation tier.
func (s *qrdbServiceServer) authorizeTenantUpdate(ctx context.Context, tenantID string, patch tenant.TenantPatch) error {
	return s.authorizeTenantRequest(tenantID, func(mw *authmw.AuthMiddleware) error {
		return mw.AuthorizeTenantUpdate(ctx, tenantID, patch)
	})
}

// authorizeTenantRequest refuses InvalidArgument an id that cannot name a
// tenant (tenant.ValidateTenantIDShape), then runs decide against the auth
// middleware. With no auth middleware the authorization step admits.
func (s *qrdbServiceServer) authorizeTenantRequest(tenantID string, decide func(*authmw.AuthMiddleware) error) error {
	if err := tenant.ValidateTenantIDShape(tenantID); err != nil {
		return mapServiceError(err)
	}
	if s.authMW == nil {
		return nil
	}
	if err := decide(s.authMW); err != nil {
		return authmw.GRPCError(err, codes.PermissionDenied)
	}
	return nil
}
