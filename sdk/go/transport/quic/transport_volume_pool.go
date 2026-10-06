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
	"google.golang.org/protobuf/types/known/wrapperspb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.VolumePoolClient = (*Transport)(nil)

// ListPools lists every pool in the catalog via the QUIC
// volumepool.List handler.
func (t *Transport) ListPools(ctx context.Context) ([]transport.VolumePool, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	respBytes, err := t.call(ctx, "volumepool.List", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListPools", Err: err}
	}
	var resp pb.VolumePoolListResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "ListPools", Err: err}
	}
	pools := make([]transport.VolumePool, len(resp.GetPools()))
	for i, p := range resp.GetPools() {
		pools[i] = quicVolumePoolFromProto(p)
	}
	return pools, nil
}

// GetPool inspects one pool via the QUIC volumepool.Get handler.
func (t *Transport) GetPool(ctx context.Context, name string) (*transport.VolumePoolDetail, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.VolumePoolGetRequest{Name: name})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetPool", Err: err}
	}
	respBytes, err := t.call(ctx, "volumepool.Get", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetPool", Err: err}
	}
	var resp pb.VolumePoolDetail
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "GetPool", Err: err}
	}
	return quicVolumePoolDetailFromProto(&resp), nil
}

// CreatePool adds a pool to the catalog via the QUIC volumepool.Create
// handler.
func (t *Transport) CreatePool(ctx context.Context, req *transport.CreateVolumePoolRequest) (*transport.VolumePool, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.VolumePoolCreateRequest{
		Name:          req.Name,
		Provider:      req.Provider,
		Description:   req.Description,
		CapacityBytes: req.CapacityBytes,
		Overcommit:    req.Overcommit,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CreatePool", Err: err}
	}
	respBytes, err := t.call(ctx, "volumepool.Create", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "CreatePool", Err: err}
	}
	var resp pb.VolumePool
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "CreatePool", Err: err}
	}
	out := quicVolumePoolFromProto(&resp)
	return &out, nil
}

// UpdatePool changes a pool's description, capacity or overcommit via the
// QUIC volumepool.Update handler.
func (t *Transport) UpdatePool(ctx context.Context, req *transport.UpdateVolumePoolRequest) (*transport.VolumePool, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
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
	payload, err := proto.Marshal(pbReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdatePool", Err: err}
	}
	respBytes, err := t.call(ctx, "volumepool.Update", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdatePool", Err: err}
	}
	var resp pb.VolumePool
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "UpdatePool", Err: err}
	}
	out := quicVolumePoolFromProto(&resp)
	return &out, nil
}

// DeletePool removes a pool from the catalog via the QUIC
// volumepool.Delete handler.
func (t *Transport) DeletePool(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.VolumePoolDeleteRequest{Name: name})
	if err != nil {
		return &transport.RequestError{Method: "DeletePool", Err: err}
	}
	if _, err := t.call(ctx, "volumepool.Delete", payload); err != nil {
		return &transport.RequestError{Method: "DeletePool", Err: err}
	}
	return nil
}

// ListPoolTenants lists the tenants assigned to a pool via the QUIC
// volumepool.ListTenants handler.
func (t *Transport) ListPoolTenants(ctx context.Context, req *transport.ListVolumePoolTenantsRequest) (*transport.ListVolumePoolTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		req = &transport.ListVolumePoolTenantsRequest{}
	}
	payload, err := proto.Marshal(&pb.VolumePoolListTenantsRequest{
		Pool:      req.Pool,
		PageToken: req.PageToken,
		PageSize:  int32(req.PageSize),
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "ListPoolTenants", Err: err}
	}
	respBytes, err := t.call(ctx, "volumepool.ListTenants", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListPoolTenants", Err: err}
	}
	var resp pb.VolumePoolListTenantsResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "ListPoolTenants", Err: err}
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

func quicVolumePoolFromProto(p *pb.VolumePool) transport.VolumePool {
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
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			out.CreatedAt = parsed
		}
	}
	if ts := p.GetUpdatedAt(); ts != "" {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			out.UpdatedAt = parsed
		}
	}
	return out
}

func quicVolumePoolDetailFromProto(d *pb.VolumePoolDetail) *transport.VolumePoolDetail {
	out := &transport.VolumePoolDetail{
		NodeReports: make([]transport.VolumePoolNodeReport, len(d.GetNodeReports())),
	}
	if p := d.GetPool(); p != nil {
		out.VolumePool = quicVolumePoolFromProto(p)
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
			if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
				row.ReportedAt = parsed
			}
		}
		out.NodeReports[i] = row
	}
	return out
}
