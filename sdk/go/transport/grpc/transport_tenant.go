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

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.TenantClient = (*Transport)(nil)

// CreateTenant creates a new tenant via the gRPC TenantCreate RPC.
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

	return &transport.CreateTenantResponse{Tenant: protoToTenantInfo(resp)}, nil
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

	return &transport.GetTenantResponse{Tenant: protoToTenantInfo(resp)}, nil
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
		tenants[i] = protoToTenantInfo(pt)
	}

	return &transport.ListTenantsResponse{Tenants: tenants}, nil
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

	return &transport.UpdateTenantResponse{Tenant: protoToTenantInfo(resp)}, nil
}

// UpdateTenantQuotas sets the resource quotas for a tenant via the gRPC TenantUpdateQuotas RPC.
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

	return &transport.UpdateTenantQuotasResponse{Tenant: protoToTenantInfo(resp)}, nil
}

// ---------------------------------------------------------------------------
// Per-tenant barrier RPCs (JSON-over-gRPC, no proto regeneration required).
// ---------------------------------------------------------------------------

// TenantBarrierInit initializes a per-tenant barrier via the JSON-over-gRPC
// TenantBarrierService.Init RPC.
func (t *Transport) TenantBarrierInit(ctx context.Context, req *transport.TenantBarrierInitRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "TenantBarrierInit", Err: errNilRequest{}}
	}

	wireReq := &grpcpkg.GRPCTenantBarrierInitRequest{
		TenantID:   req.TenantID,
		Passphrase: req.Passphrase,
		Threshold:  req.Threshold,
		Shares:     req.Shares,
		Strategy:   req.Strategy,
	}
	if err := grpcpkg.TenantBarrierInitUnary(ctx, t.conn, wireReq); err != nil {
		return mapGRPCError("TenantBarrierInit", err)
	}
	return nil
}

// TenantBarrierUnseal unseals a per-tenant barrier via the JSON-over-gRPC
// TenantBarrierService.Unseal RPC.
func (t *Transport) TenantBarrierUnseal(ctx context.Context, req *transport.TenantBarrierUnsealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "TenantBarrierUnseal", Err: errNilRequest{}}
	}

	wireReq := &grpcpkg.GRPCTenantBarrierUnsealRequest{
		TenantID: req.TenantID,
		Share:    req.Share,
		Key:      req.Key,
	}
	if err := grpcpkg.TenantBarrierUnsealUnary(ctx, t.conn, wireReq); err != nil {
		return mapGRPCError("TenantBarrierUnseal", err)
	}
	return nil
}

// TenantBarrierSeal seals a per-tenant barrier via the JSON-over-gRPC
// TenantBarrierService.Seal RPC.
func (t *Transport) TenantBarrierSeal(ctx context.Context, req *transport.TenantBarrierSealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "TenantBarrierSeal", Err: errNilRequest{}}
	}

	wireReq := &grpcpkg.GRPCTenantBarrierSealRequest{TenantID: req.TenantID}
	if err := grpcpkg.TenantBarrierSealUnary(ctx, t.conn, wireReq); err != nil {
		return mapGRPCError("TenantBarrierSeal", err)
	}
	return nil
}

// TenantBarrierStatus returns the current sealed/initialized state and active
// strategy metadata for a per-tenant barrier via the JSON-over-gRPC
// TenantBarrierService.Status RPC.
func (t *Transport) TenantBarrierStatus(ctx context.Context, req *transport.TenantBarrierStatusRequest) (*transport.TenantBarrierStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: errNilRequest{}}
	}

	wireReq := &grpcpkg.GRPCTenantBarrierStatusRequest{TenantID: req.TenantID}
	resp, err := grpcpkg.TenantBarrierStatusUnary(ctx, t.conn, wireReq)
	if err != nil {
		return nil, mapGRPCError("TenantBarrierStatus", err)
	}
	if resp == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: errEmptyResponse{}}
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

// protoToTenantInfo converts a protobuf TenantResponse to a transport.TenantInfo.
func protoToTenantInfo(resp *pb.TenantResponse) transport.TenantInfo {
	if resp == nil {
		return transport.TenantInfo{}
	}

	info := transport.TenantInfo{
		ID:            resp.GetId(),
		Name:          resp.GetName(),
		IsolationType: transport.TenantIsolationType(resp.GetIsolationType()),
	}

	if pbQ := resp.GetQuotas(); pbQ != nil {
		q := &transport.TenantQuotas{
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
		info.Quotas = q
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
// Local typed errors used by per-tenant barrier transport calls.
// ---------------------------------------------------------------------------

// errNilRequest is returned when a tenant barrier RPC receives a nil request.
type errNilRequest struct{}

// Error returns a fixed, lowercase identifier consistent with Go error style.
func (errNilRequest) Error() string { return "tenant barrier: nil request" }

// errEmptyResponse is returned when the server replies with a successful
// status but the decoded payload is nil.
type errEmptyResponse struct{}

// Error returns a fixed, lowercase identifier.
func (errEmptyResponse) Error() string { return "tenant barrier: empty response" }
