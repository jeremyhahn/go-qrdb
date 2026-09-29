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
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Tenant handlers
// ---------------------------------------------------------------------------

// TenantCreate creates a new tenant via the tenant service.
func (s *qrdbServiceServer) TenantCreate(ctx context.Context, req *pb.TenantCreateRequest) (*pb.TenantResponse, error) {
	if s.tenantSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant"}).Error())
	}

	opts := []tenant.CreateTenantOption{tenant.WithBarrierAutoUnseal(req.GetBarrierAutoUnseal())}
	if dataRoot := req.GetDataRoot(); dataRoot != "" {
		opts = append(opts, tenant.WithDataRoot(dataRoot))
	}

	info, err := s.tenantSvc.CreateTenant(ctx, req.GetId(), req.GetName(), tenant.IsolationType(req.GetIsolationType()), opts...)
	if err != nil {
		return nil, mapTenantError(err)
	}

	return tenantToProto(info), nil
}

// TenantGet retrieves a tenant by ID.
func (s *qrdbServiceServer) TenantGet(ctx context.Context, req *pb.TenantGetRequest) (*pb.TenantResponse, error) {
	if s.tenantSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant"}).Error())
	}

	if err := s.authorizeTenantTarget(ctx, req.GetId(), permission.TenantsRead); err != nil {
		return nil, err
	}

	info, err := s.tenantSvc.GetTenant(ctx, req.GetId())
	if err != nil {
		return nil, mapTenantError(err)
	}

	return tenantToProto(info), nil
}

// TenantList returns metadata for every tenant to a caller holding
// tenants:list:cross, and for the caller's own tenant only to anyone else.
func (s *qrdbServiceServer) TenantList(ctx context.Context, _ *emptypb.Empty) (*pb.TenantListResponse, error) {
	if s.tenantSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant"}).Error())
	}

	tenants, err := s.tenantSvc.ListTenants(ctx)
	if err != nil {
		return nil, mapTenantError(err)
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

// TenantUpdate applies a partial update to an existing tenant.
func (s *qrdbServiceServer) TenantUpdate(ctx context.Context, req *pb.TenantUpdateRequest) (*pb.TenantResponse, error) {
	if s.tenantSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant"}).Error())
	}

	patch := tenant.TenantPatch{
		Name:          req.GetName(),
		IsolationType: tenant.IsolationType(req.GetIsolationType()),
	}

	if err := s.authorizeTenantUpdate(ctx, req.GetId(), patch); err != nil {
		return nil, err
	}

	info, err := s.tenantSvc.UpdateTenant(ctx, req.GetId(), patch)
	if err != nil {
		return nil, mapTenantError(err)
	}

	return tenantToProto(info), nil
}

// TenantDelete removes a tenant and its metadata.
func (s *qrdbServiceServer) TenantDelete(ctx context.Context, req *pb.TenantDeleteRequest) (*emptypb.Empty, error) {
	if s.tenantSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant"}).Error())
	}

	if err := s.authorizeTenantTarget(ctx, req.GetId(), permission.TenantsDelete); err != nil {
		return nil, err
	}

	if err := s.tenantSvc.DeleteTenant(ctx, req.GetId()); err != nil {
		return nil, mapTenantError(err)
	}

	return &emptypb.Empty{}, nil
}

// TenantUpdateQuotas sets the resource quotas for an existing tenant.
func (s *qrdbServiceServer) TenantUpdateQuotas(ctx context.Context, req *pb.TenantUpdateQuotasRequest) (*pb.TenantResponse, error) {
	if s.tenantSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant"}).Error())
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
		return nil, mapTenantError(err)
	}

	return tenantToProto(info), nil
}

// TenantUpdateEntitlements sets the feature entitlements for an existing tenant.
func (s *qrdbServiceServer) TenantUpdateEntitlements(ctx context.Context, req *pb.TenantUpdateEntitlementsRequest) (*pb.TenantResponse, error) {
	if s.tenantSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant"}).Error())
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
	}

	if err := s.authorizeTenantTarget(ctx, req.GetId(), permission.TenantsSetEntitlements); err != nil {
		return nil, err
	}

	info, err := s.tenantSvc.UpdateEntitlements(ctx, req.GetId(), e)
	if err != nil {
		return nil, mapTenantError(err)
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
		Id:            info.ID,
		Name:          info.Name,
		IsolationType: string(info.EffectiveIsolationType()),
		DataRoot:      info.DataRoot,
		CreatedAt:     info.CreatedAt.Format(time.RFC3339),
		UpdatedAt:     info.UpdatedAt.Format(time.RFC3339),
	}
	if info.Quotas != nil {
		resp.Quotas = quotaToProto(info.Quotas)
	}
	if info.Entitlements != nil {
		resp.Entitlements = entitlementsToProto(info.Entitlements)
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

// mapTenantError converts tenant domain errors to gRPC status errors.
func mapTenantError(err error) error {
	var se *tenant.StoreError
	var be *tenant.BarrierError
	var ite *tenant.InvalidTenantIDError
	var itce *tenant.InvalidTenantIDCharError
	var ise *tenant.InvalidStrategyError
	var tse *tenant.TenantScopeError
	var idre *tenant.InvalidDataRootError
	var dne *tenant.DataRootNotEmptyError
	var dite *tenant.DataRootIsolationTypeError
	var tde *tenant.TenantDeletedError
	var thd *tenant.TenantHasDatabasesError
	var rte *tenant.ReservedTenantIDError
	var tdpe *tenant.TenantDataProbeError

	switch {
	case errors.As(err, &tse):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, tenant.ErrEmptyDataRoot):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.As(err, &idre):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.As(err, &dite):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.As(err, &dne):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, tenant.ErrEmptyTenantID):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, tenant.ErrEmptyTenantName):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, tenant.ErrEmptyPassphrase):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, tenant.ErrEmptyShare):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, tenant.ErrSystemTenantMustBePrefix):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, tenant.ErrNilQuotas):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, tenant.ErrNilEntitlements):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.As(err, &ise):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, tenant.ErrBarrierIsolationUnavailable):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, tenant.ErrBarrierNotEnabled):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, tenant.ErrTenantAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.As(err, &tde):
		// A deleted tenant id is retired permanently: the id is taken, in
		// exactly the sense ErrTenantAlreadyExists means it is taken.
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.As(err, &thd):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, tenant.ErrTenantHasData):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, tenant.ErrTenantDataUnverifiable), errors.As(err, &tdpe):
		// The node could not confirm the tenant is empty, so it refused the
		// isolation change rather than risk orphaning data.
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, tenant.ErrTenantNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.As(err, &ite):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.As(err, &itce):
		// An id carrying a path separator or control byte cannot name a
		// tenant. It fell through to Internal before.
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.As(err, &rte):
		// A reserved id is not the caller's to name, on create or on delete.
		// It fell through to Internal before.
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.As(err, &se):
		return status.Error(codes.Internal, err.Error())
	case errors.As(err, &be):
		return status.Error(codes.Internal, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

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
		return mapTenantError(err)
	}
	if s.authMW == nil {
		return nil
	}
	if err := decide(s.authMW); err != nil {
		return authmw.GRPCError(err, codes.PermissionDenied)
	}
	return nil
}
