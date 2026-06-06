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

	"google.golang.org/protobuf/types/known/emptypb"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.TenantClient = (*Transport)(nil)

// CreateTenant creates a new tenant via the Unix gRPC-over-UDS TenantCreate RPC.
func (t *Transport) CreateTenant(ctx context.Context, req *transport.CreateTenantRequest) (*transport.CreateTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.TenantCreate(ctx, &pb.TenantCreateRequest{
		Id:            req.ID,
		Name:          req.Name,
		IsolationType: string(req.IsolationType),
	})
	if err != nil {
		return nil, mapGRPCError("CreateTenant", err)
	}

	return &transport.CreateTenantResponse{Tenant: unixProtoToTenantInfo(resp)}, nil
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

	return &transport.GetTenantResponse{Tenant: unixProtoToTenantInfo(resp)}, nil
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
		tenants[i] = unixProtoToTenantInfo(pt)
	}

	return &transport.ListTenantsResponse{Tenants: tenants}, nil
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

	return &transport.UpdateTenantResponse{Tenant: unixProtoToTenantInfo(resp)}, nil
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
			MaxDatabases:    req.Quotas.MaxDatabases,
			MaxRaftGroups:   req.Quotas.MaxRaftGroups,
			MaxCollections:  req.Quotas.MaxCollections,
			MaxKeys:         req.Quotas.MaxKeys,
			MaxStorageBytes: req.Quotas.MaxStorageBytes,
			MaxUsers:        req.Quotas.MaxUsers,
			MaxRoles:        req.Quotas.MaxRoles,
			MaxCertificates: req.Quotas.MaxCertificates,
			MaxBackups:      req.Quotas.MaxBackups,
			MaxVectorDims:   req.Quotas.MaxVectorDims,
			MaxTsMetrics:    req.Quotas.MaxTSMetrics,
		},
	})
	if err != nil {
		return nil, mapGRPCError("UpdateTenantQuotas", err)
	}

	return &transport.UpdateTenantQuotasResponse{Tenant: unixProtoToTenantInfo(resp)}, nil
}

// ---------------------------------------------------------------------------
// Per-tenant barrier RPCs (delegate to the gRPC TenantBarrierService over UDS)
// ---------------------------------------------------------------------------

// TenantBarrierInit initializes the per-tenant barrier with the given
// threshold/shares parameters via the Unix gRPC-over-UDS TenantBarrierService.
// The Unix wire shares the gRPC server descriptor; this transport simply
// invokes the JSON-over-gRPC unary helper exported from pkg/api/grpc.
func (t *Transport) TenantBarrierInit(ctx context.Context, req *transport.TenantBarrierInitRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "TenantBarrierInit", Err: errNilRequest}
	}

	grpcReq := &grpcpkg.GRPCTenantBarrierInitRequest{
		TenantID:   req.TenantID,
		Passphrase: req.Passphrase,
		Threshold:  req.Threshold,
		Shares:     req.Shares,
		Strategy:   req.Strategy,
	}
	if err := grpcpkg.TenantBarrierInitUnary(ctx, t.conn, grpcReq); err != nil {
		return mapGRPCError("TenantBarrierInit", err)
	}
	return nil
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
		return &transport.RequestError{Method: "TenantBarrierUnseal", Err: errNilRequest}
	}

	grpcReq := &grpcpkg.GRPCTenantBarrierUnsealRequest{
		TenantID: req.TenantID,
		Share:    req.Share,
		Key:      req.Key,
	}
	if err := grpcpkg.TenantBarrierUnsealUnary(ctx, t.conn, grpcReq); err != nil {
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
		return &transport.RequestError{Method: "TenantBarrierSeal", Err: errNilRequest}
	}

	grpcReq := &grpcpkg.GRPCTenantBarrierSealRequest{
		TenantID: req.TenantID,
	}
	if err := grpcpkg.TenantBarrierSealUnary(ctx, t.conn, grpcReq); err != nil {
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
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: errNilRequest}
	}

	grpcReq := &grpcpkg.GRPCTenantBarrierStatusRequest{
		TenantID: req.TenantID,
	}
	resp, err := grpcpkg.TenantBarrierStatusUnary(ctx, t.conn, grpcReq)
	if err != nil {
		return nil, mapGRPCError("TenantBarrierStatus", err)
	}

	return &transport.TenantBarrierStatusResponse{
		TenantID:       resp.TenantID,
		Sealed:         resp.Sealed,
		Initialized:    resp.Initialized,
		Strategy:       resp.Strategy,
		HardwareBacked: resp.HardwareBacked,
	}, nil
}

// ---------------------------------------------------------------------------
// Proto conversion helpers
// ---------------------------------------------------------------------------

// unixProtoToTenantInfo converts a protobuf TenantResponse to a transport.TenantInfo.
func unixProtoToTenantInfo(resp *pb.TenantResponse) transport.TenantInfo {
	if resp == nil {
		return transport.TenantInfo{}
	}

	info := transport.TenantInfo{
		ID:            resp.GetId(),
		Name:          resp.GetName(),
		IsolationType: transport.TenantIsolationType(resp.GetIsolationType()),
	}

	if pbQ := resp.GetQuotas(); pbQ != nil {
		info.Quotas = &transport.TenantQuotas{
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
