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

	info, err := s.tenantSvc.CreateTenant(ctx, req.GetId(), req.GetName(), tenant.IsolationType(req.GetIsolationType()))
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

	info, err := s.tenantSvc.GetTenant(ctx, req.GetId())
	if err != nil {
		return nil, mapTenantError(err)
	}

	return tenantToProto(info), nil
}

// TenantList returns metadata for all registered tenants.
func (s *qrdbServiceServer) TenantList(ctx context.Context, _ *emptypb.Empty) (*pb.TenantListResponse, error) {
	if s.tenantSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant"}).Error())
	}

	tenants, err := s.tenantSvc.ListTenants(ctx)
	if err != nil {
		return nil, mapTenantError(err)
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
		MaxDatabases:    pbQ.GetMaxDatabases(),
		MaxRaftGroups:   pbQ.GetMaxRaftGroups(),
		MaxCollections:  pbQ.GetMaxCollections(),
		MaxKeys:         pbQ.GetMaxKeys(),
		MaxStorageBytes: pbQ.GetMaxStorageBytes(),
		MaxUsers:        pbQ.GetMaxUsers(),
		MaxRoles:        pbQ.GetMaxRoles(),
		MaxCertificates: pbQ.GetMaxCertificates(),
		MaxBackups:      pbQ.GetMaxBackups(),
		MaxVectorDims:   pbQ.GetMaxVectorDims(),
		MaxTSMetrics:    pbQ.GetMaxTsMetrics(),
	}

	info, err := s.tenantSvc.UpdateQuotas(ctx, req.GetId(), q)
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
		CreatedAt:     info.CreatedAt.Format(time.RFC3339),
		UpdatedAt:     info.UpdatedAt.Format(time.RFC3339),
	}
	if info.Quotas != nil {
		resp.Quotas = quotaToProto(info.Quotas)
	}
	return resp
}

// quotaToProto converts a quota.Quotas to its protobuf representation.
func quotaToProto(q *quota.Quotas) *pb.TenantQuotas {
	if q == nil {
		return nil
	}
	return &pb.TenantQuotas{
		MaxDatabases:    q.MaxDatabases,
		MaxRaftGroups:   q.MaxRaftGroups,
		MaxCollections:  q.MaxCollections,
		MaxKeys:         q.MaxKeys,
		MaxStorageBytes: q.MaxStorageBytes,
		MaxUsers:        q.MaxUsers,
		MaxRoles:        q.MaxRoles,
		MaxCertificates: q.MaxCertificates,
		MaxBackups:      q.MaxBackups,
		MaxVectorDims:   q.MaxVectorDims,
		MaxTsMetrics:    q.MaxTSMetrics,
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
	var ise *tenant.InvalidStrategyError
	var tse *tenant.TenantScopeError

	switch {
	case errors.As(err, &tse):
		return status.Error(codes.PermissionDenied, err.Error())
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
	case errors.As(err, &ise):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, tenant.ErrBarrierIsolationUnavailable):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, tenant.ErrBarrierNotEnabled):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, tenant.ErrTenantAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, tenant.ErrTenantHasData):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, tenant.ErrTenantNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.As(err, &ite):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.As(err, &se):
		return status.Error(codes.Internal, err.Error())
	case errors.As(err, &be):
		return status.Error(codes.Internal, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
