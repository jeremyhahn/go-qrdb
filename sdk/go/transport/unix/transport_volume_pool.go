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
	"google.golang.org/protobuf/types/known/wrapperspb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.VolumePoolClient = (*Transport)(nil)

// parseRFC3339 parses an RFC 3339 timestamp, for the wire's createdAt /
// updatedAt / reportedAt string fields.
func parseRFC3339(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

// ListPools lists every pool in the catalog via the Unix gRPC-over-UDS
// VolumePoolList RPC.
func (t *Transport) ListPools(ctx context.Context) ([]transport.VolumePool, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.VolumePoolList(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("ListPools", err)
	}
	pools := make([]transport.VolumePool, len(resp.GetPools()))
	for i, p := range resp.GetPools() {
		pools[i] = volumePoolFromProto(p)
	}
	return pools, nil
}

// GetPool inspects one pool via the Unix gRPC-over-UDS VolumePoolGet RPC.
func (t *Transport) GetPool(ctx context.Context, name string) (*transport.VolumePoolDetail, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.VolumePoolGet(ctx, &pb.VolumePoolGetRequest{Name: name})
	if err != nil {
		return nil, mapGRPCError("GetPool", err)
	}
	return volumePoolDetailFromProto(resp), nil
}

// CreatePool adds a pool to the catalog via the Unix gRPC-over-UDS
// VolumePoolCreate RPC.
func (t *Transport) CreatePool(ctx context.Context, req *transport.CreateVolumePoolRequest) (*transport.VolumePool, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.VolumePoolCreate(ctx, &pb.VolumePoolCreateRequest{
		Name:          req.Name,
		Provider:      req.Provider,
		Description:   req.Description,
		CapacityBytes: req.CapacityBytes,
		Overcommit:    req.Overcommit,
	})
	if err != nil {
		return nil, mapGRPCError("CreatePool", err)
	}
	out := volumePoolFromProto(resp)
	return &out, nil
}

// UpdatePool changes a pool's description, capacity or overcommit via the
// Unix gRPC-over-UDS VolumePoolUpdate RPC.
func (t *Transport) UpdatePool(ctx context.Context, req *transport.UpdateVolumePoolRequest) (*transport.VolumePool, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "UpdatePool", Err: &transport.NilRequestError{}}
	}
	pbReq := &pb.VolumePoolUpdateRequest{Name: req.Name}
	if req.Description != nil {
		pbReq.Description = wrapperspb.String(*req.Description)
	}
	if req.CapacityBytes != nil {
		pbReq.CapacityBytes = wrapperspb.Int64(*req.CapacityBytes)
	}
	if req.Overcommit != nil {
		pbReq.Overcommit = wrapperspb.Double(*req.Overcommit)
	}
	resp, err := t.client.VolumePoolUpdate(ctx, pbReq)
	if err != nil {
		return nil, mapGRPCError("UpdatePool", err)
	}
	out := volumePoolFromProto(resp)
	return &out, nil
}

// DeletePool removes a pool from the catalog via the Unix gRPC-over-UDS
// VolumePoolDelete RPC.
func (t *Transport) DeletePool(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if _, err := t.client.VolumePoolDelete(ctx, &pb.VolumePoolDeleteRequest{Name: name}); err != nil {
		return mapGRPCError("DeletePool", err)
	}
	return nil
}

// ListPoolTenants lists the tenants assigned to a pool via the Unix
// gRPC-over-UDS VolumePoolListTenants RPC.
func (t *Transport) ListPoolTenants(ctx context.Context, req *transport.ListVolumePoolTenantsRequest) (*transport.ListVolumePoolTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		req = &transport.ListVolumePoolTenantsRequest{}
	}
	resp, err := t.client.VolumePoolListTenants(ctx, &pb.VolumePoolListTenantsRequest{
		Pool:      req.Pool,
		PageToken: req.PageToken,
		PageSize:  int32(req.PageSize),
	})
	if err != nil {
		return nil, mapGRPCError("ListPoolTenants", err)
	}
	out := &transport.ListVolumePoolTenantsResponse{
		Tenants:       make([]transport.VolumePoolTenant, len(resp.GetTenants())),
		NextPageToken: resp.GetNextPageToken(),
	}
	for i, tn := range resp.GetTenants() {
		out.Tenants[i] = transport.VolumePoolTenant{TenantID: tn.GetTenantId(), ReservedBytes: tn.GetReservedBytes()}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Proto conversion helpers
// ---------------------------------------------------------------------------

func volumePoolFromProto(p *pb.VolumePool) transport.VolumePool {
	out := transport.VolumePool{
		Name:          p.GetName(),
		Provider:      p.GetProvider(),
		Description:   p.GetDescription(),
		CapacityBytes: p.GetCapacityBytes(),
		ReservedBytes: p.GetReservedBytes(),
		UsedBytes:     p.GetUsedBytes(),
		TenantCount:   int(p.GetTenantCount()),
		Overcommit:    p.GetOvercommit(),
	}
	if n := p.GetNodes(); n != nil {
		out.Nodes = transport.VolumePoolNodeCounts{
			Ready:       int(n.GetReady()),
			Locked:      int(n.GetLocked()),
			Unavailable: int(n.GetUnavailable()),
			Unmapped:    int(n.GetUnmapped()),
		}
	}
	if ts := p.GetCreatedAt(); ts != "" {
		if parsed, err := parseRFC3339(ts); err == nil {
			out.CreatedAt = parsed
		}
	}
	if ts := p.GetUpdatedAt(); ts != "" {
		if parsed, err := parseRFC3339(ts); err == nil {
			out.UpdatedAt = parsed
		}
	}
	return out
}

func volumePoolDetailFromProto(d *pb.VolumePoolDetail) *transport.VolumePoolDetail {
	out := &transport.VolumePoolDetail{
		NodeReports: make([]transport.VolumePoolNodeReport, len(d.GetNodeReports())),
	}
	if p := d.GetPool(); p != nil {
		out.VolumePool = volumePoolFromProto(p)
	}
	for i, nr := range d.GetNodeReports() {
		row := transport.VolumePoolNodeReport{
			NodeID:    nr.GetNodeId(),
			State:     nr.GetState(),
			SizeBytes: nr.GetSizeBytes(),
			FreeBytes: nr.GetFreeBytes(),
			Detail:    nr.GetDetail(),
		}
		if ts := nr.GetReportedAt(); ts != "" {
			if parsed, err := parseRFC3339(ts); err == nil {
				row.ReportedAt = parsed
			}
		}
		out.NodeReports[i] = row
	}
	return out
}
