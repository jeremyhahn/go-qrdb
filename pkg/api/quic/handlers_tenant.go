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
	"time"

	"google.golang.org/protobuf/proto"

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// tenantQuotaError is returned when a TenantUpdateQuotas request carries no quotas.
type tenantQuotaError struct{}

// Error returns the error string.
func (e *tenantQuotaError) Error() string {
	return "tenant: quotas field is required"
}

// handleTenantCreate creates a new tenant and returns its metadata.
func (s *Server) handleTenantCreate(ctx context.Context, payload []byte) ([]byte, error) {
	if s.onboarding == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant onboarding"}
	}

	var req pb.TenantCreateRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
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
		return nil, err
	}

	resp, err := proto.Marshal(&pb.TenantCreateResponse{
		Tenant: tenantInfoToProto(result.Tenant),
		Admin:  tenantAdminCredentialToProto(&result.Admin),
		So:     tenantAdminCredentialToProto(result.SO),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
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

// handleTenantGet retrieves a tenant by ID.
func (s *Server) handleTenantGet(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req pb.TenantGetRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.authorizeTenantTarget(ctx, req.GetId(), permission.TenantsRead); err != nil {
		return nil, err
	}

	info, err := s.tenantSvc.GetTenant(ctx, req.GetId())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(tenantInfoToProto(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleTenantList returns metadata for every tenant to a caller holding
// tenants:list:cross, and for the caller's own tenant only to anyone else
// (authmw.FilterTenantList).
func (s *Server) handleTenantList(ctx context.Context, _ []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	tenants, err := s.tenantSvc.ListTenants(ctx)
	if err != nil {
		return nil, err
	}
	if s.authMW != nil {
		if tenants, err = s.authMW.FilterTenantList(ctx, tenants); err != nil {
			return nil, err
		}
	}

	pbTenants := make([]*pb.TenantResponse, len(tenants))
	for i := range tenants {
		pbTenants[i] = tenantInfoToProto(&tenants[i])
	}

	resp, err := proto.Marshal(&pb.TenantListResponse{Tenants: pbTenants})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleTenantListDeleted returns one page of retired tenant ids. The page is
// encoded by the same conversion the gRPC service uses, so the two wires carry
// identical records.
func (s *Server) handleTenantListDeleted(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}
	var req pb.TenantListDeletedRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	page, err := s.tenantSvc.ListDeletedTenants(ctx, req.GetPageToken(), int(req.GetPageSize()))
	if err != nil {
		return nil, err
	}
	resp, err := proto.Marshal(grpcapi.DeletedTenantsPageToProto(page))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleTenantReconcileDeleted reclaims, on this node, the residue of retired
// tenant ids.
func (s *Server) handleTenantReconcileDeleted(ctx context.Context, _ []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}
	report, err := s.tenantSvc.ReconcileDeletedTenants(ctx)
	// A pass that failed only for some ids answers with its report, whose
	// failed list names them; only a guard failure is an error status.
	if rerr := tenant.ReconcileResponseError(report, err); rerr != nil {
		return nil, rerr
	}
	resp, err := proto.Marshal(grpcapi.ReconcileReportToProto(report))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleTenantUpdate applies a partial update to an existing tenant.
func (s *Server) handleTenantUpdate(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req pb.TenantUpdateRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
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

	if err := s.authorizeTenantUpdate(ctx, req.GetId(), patch); err != nil {
		return nil, err
	}

	info, err := s.tenantSvc.UpdateTenant(ctx, req.GetId(), patch)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(tenantInfoToProto(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleTenantDelete removes a tenant and its metadata.
func (s *Server) handleTenantDelete(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req pb.TenantDeleteRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.authorizeTenantTarget(ctx, req.GetId(), permission.TenantsDelete); err != nil {
		return nil, err
	}

	if err := s.tenantSvc.DeleteTenant(ctx, req.GetId()); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleTenantUpdateQuotas sets the resource quotas for an existing tenant.
func (s *Server) handleTenantUpdateQuotas(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req pb.TenantUpdateQuotasRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	pbQ := req.GetQuotas()
	if pbQ == nil {
		return nil, &tenantQuotaError{}
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
		return nil, err
	}

	resp, err := proto.Marshal(tenantInfoToProto(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// tenantEntitlementError is returned when a TenantUpdateEntitlements request
// carries no entitlements.
type tenantEntitlementError struct{}

// Error returns the error string.
func (e *tenantEntitlementError) Error() string {
	return "tenant: entitlements field is required"
}

// handleTenantUpdateEntitlements sets the feature entitlements for an existing tenant.
func (s *Server) handleTenantUpdateEntitlements(ctx context.Context, payload []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req pb.TenantUpdateEntitlementsRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	pbE := req.GetEntitlements()
	if pbE == nil {
		return nil, &tenantEntitlementError{}
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
		return nil, err
	}

	resp, err := proto.Marshal(tenantInfoToProto(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// tenantInfoToProto converts a tenant.TenantInfo to its protobuf representation.
func tenantInfoToProto(info *tenant.TenantInfo) *pb.TenantResponse {
	if info == nil {
		return nil
	}
	resp := &pb.TenantResponse{
		Id:            info.ID,
		Name:          info.Name,
		IsolationType: string(info.EffectiveIsolationType()),
		CreatedAt:     info.CreatedAt.Format(time.RFC3339),
		UpdatedAt:     info.UpdatedAt.Format(time.RFC3339),
		VolumePool:    info.VolumePool,
		Description:   info.Description,
		Metadata:      info.Metadata,
		Status:        string(info.Status),
	}
	if refusal := tenant.RefusalOf(info.Refusal); refusal != nil {
		resp.Refusal = &pb.TenantRefusal{Code: refusal.Code, Message: refusal.Message}
	}
	if info.Quotas != nil {
		resp.Quotas = &pb.TenantQuotas{
			MaxDatabases:          info.Quotas.MaxDatabases,
			MaxRaftGroups:         info.Quotas.MaxRaftGroups,
			MaxReplicasPerGroup:   info.Quotas.MaxReplicasPerGroup,
			MaxTotalReplicas:      info.Quotas.MaxTotalReplicas,
			MaxCollections:        info.Quotas.MaxCollections,
			MaxKeys:               info.Quotas.MaxKeys,
			MaxStorageBytes:       info.Quotas.MaxStorageBytes,
			MaxDatabaseSizeBytes:  info.Quotas.MaxDatabaseSizeBytes,
			MaxUsers:              info.Quotas.MaxUsers,
			MaxRoles:              info.Quotas.MaxRoles,
			MaxCertificates:       info.Quotas.MaxCertificates,
			MaxBackups:            info.Quotas.MaxBackups,
			MaxVectorDims:         info.Quotas.MaxVectorDims,
			MaxTsMetrics:          info.Quotas.MaxTSMetrics,
			SystemBarrierQuota:    info.Quotas.SystemBarrierQuota,
			MaxDedicatedWalGroups: info.Quotas.MaxDedicatedWALGroups,
		}
	}
	if info.Entitlements != nil {
		resp.Entitlements = &pb.TenantEntitlements{
			AllowedIsolationModes:     info.Entitlements.AllowedIsolationModes,
			AllowDedicatedRaft:        info.Entitlements.AllowDedicatedRaft,
			AllowVolumeStorage:        info.Entitlements.AllowVolumeStorage,
			AllowSystemManagedBarrier: info.Entitlements.AllowSystemManagedBarrier,
			AllowedBarrierModes:       info.Entitlements.AllowedBarrierModes,
			AllowDedicatedWal:         info.Entitlements.AllowDedicatedWAL,
			AllowedVolumePools:        info.Entitlements.AllowedVolumePools,
		}
	}
	return resp
}

// authorizeTenantTarget re-checks perm against the tenant the FRAME names
// (authmw.AuthorizeTenantTarget). The dispatch check authorized it against
// the caller's own tenant only, which would let a tenant-local Admin read,
// change or delete any tenant.
func (s *Server) authorizeTenantTarget(ctx context.Context, tenantID string, perm permission.Permission) error {
	return s.authorizeTenantRequest(tenantID, func(mw *authmw.AuthMiddleware) error {
		return mw.AuthorizeTenantTarget(ctx, tenantID, perm)
	})
}

// authorizeTenantUpdate authorizes patch against the tenant the FRAME names
// (authmw.AuthorizeTenantUpdate): tenants:update for a rename, plus
// tenants:set_isolation when the patch changes the isolation tier.
func (s *Server) authorizeTenantUpdate(ctx context.Context, tenantID string, patch tenant.TenantPatch) error {
	return s.authorizeTenantRequest(tenantID, func(mw *authmw.AuthMiddleware) error {
		return mw.AuthorizeTenantUpdate(ctx, tenantID, patch)
	})
}

// authorizeTenantRequest refuses an id that cannot name a tenant
// (tenant.ValidateTenantIDShape, StatusInvalidArgument), then runs decide
// against the auth middleware. With no auth middleware the authorization step
// admits.
func (s *Server) authorizeTenantRequest(tenantID string, decide func(*authmw.AuthMiddleware) error) error {
	if err := tenant.ValidateTenantIDShape(tenantID); err != nil {
		return err
	}
	if s.authMW == nil {
		return nil
	}
	return decide(s.authMW)
}
