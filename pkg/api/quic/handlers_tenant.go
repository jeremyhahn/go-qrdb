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

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
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
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	var req pb.TenantCreateRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	info, err := s.tenantSvc.CreateTenant(ctx, req.GetId(), req.GetName(), tenant.IsolationType(req.GetIsolationType()))
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(tenantInfoToProto(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
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

// handleTenantList returns metadata for all registered tenants.
func (s *Server) handleTenantList(ctx context.Context, _ []byte) ([]byte, error) {
	if s.tenantSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "tenant"}
	}

	tenants, err := s.tenantSvc.ListTenants(ctx)
	if err != nil {
		return nil, err
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
	}
	if info.Quotas != nil {
		resp.Quotas = &pb.TenantQuotas{
			MaxDatabases:    info.Quotas.MaxDatabases,
			MaxRaftGroups:   info.Quotas.MaxRaftGroups,
			MaxCollections:  info.Quotas.MaxCollections,
			MaxKeys:         info.Quotas.MaxKeys,
			MaxStorageBytes: info.Quotas.MaxStorageBytes,
			MaxUsers:        info.Quotas.MaxUsers,
			MaxRoles:        info.Quotas.MaxRoles,
			MaxCertificates: info.Quotas.MaxCertificates,
			MaxBackups:      info.Quotas.MaxBackups,
			MaxVectorDims:   info.Quotas.MaxVectorDims,
			MaxTsMetrics:    info.Quotas.MaxTSMetrics,
		}
	}
	return resp
}
