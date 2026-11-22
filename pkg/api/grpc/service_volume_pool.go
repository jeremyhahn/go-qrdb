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
	"math"
	"time"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// ---------------------------------------------------------------------------
// Volume pool handlers (docs/volume-pools/api.md). Permission is enforced
// structurally from each RPC's `option (qrdb.v1.permission)` declaration in
// the proto (ScopeCross, system tenant only); no handler here authorizes by
// hand -- see TenantUpdateQuotas for the same pattern on an existing
// platform-operator-only verb.
// ---------------------------------------------------------------------------

// VolumePoolList lists every pool in the catalog.
func (s *qrdbServiceServer) VolumePoolList(ctx context.Context, _ *emptypb.Empty) (*pb.VolumePoolListResponse, error) {
	if s.volumeSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "volume"})
	}
	pools, err := s.volumeSvc.ListPools(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	resp := &pb.VolumePoolListResponse{Pools: make([]*pb.VolumePool, len(pools))}
	for i := range pools {
		resp.Pools[i] = volumePoolToProto(&pools[i])
	}
	return resp, nil
}

// VolumePoolGet inspects one pool, with one row per node that has reported it.
func (s *qrdbServiceServer) VolumePoolGet(ctx context.Context, req *pb.VolumePoolGetRequest) (*pb.VolumePoolDetail, error) {
	if s.volumeSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "volume"})
	}
	detail, err := s.volumeSvc.GetPool(ctx, req.GetName())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return volumePoolDetailToProto(detail), nil
}

// VolumePoolCreate adds a pool to the catalog.
func (s *qrdbServiceServer) VolumePoolCreate(ctx context.Context, req *pb.VolumePoolCreateRequest) (*pb.VolumePool, error) {
	if s.volumeSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "volume"})
	}
	pool, err := s.volumeSvc.CreatePool(ctx, volume.CreatePoolRequest{
		Name:          req.GetName(),
		Provider:      req.GetProvider(),
		Description:   req.GetDescription(),
		CapacityBytes: req.GetCapacityBytes(),
		Overcommit:    req.GetOvercommit(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return volumePoolToProto(&volume.PoolView{Pool: *pool}), nil
}

// VolumePoolUpdate changes a pool's description, capacity or overcommit.
func (s *qrdbServiceServer) VolumePoolUpdate(ctx context.Context, req *pb.VolumePoolUpdateRequest) (*pb.VolumePool, error) {
	if s.volumeSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "volume"})
	}
	update := volume.UpdatePoolRequest{}
	if d := req.GetDescription(); d != nil {
		v := d.GetValue()
		update.Description = &v
	}
	if c := req.GetCapacityBytes(); c != nil {
		v := c.GetValue()
		update.CapacityBytes = &v
	}
	if o := req.GetOvercommit(); o != nil {
		v := o.GetValue()
		update.Overcommit = &v
	}
	pool, err := s.volumeSvc.UpdatePool(ctx, req.GetName(), update)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return volumePoolToProto(&volume.PoolView{Pool: *pool}), nil
}

// VolumePoolDelete removes a pool from the catalog.
func (s *qrdbServiceServer) VolumePoolDelete(ctx context.Context, req *pb.VolumePoolDeleteRequest) (*emptypb.Empty, error) {
	if s.volumeSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "volume"})
	}
	if err := s.volumeSvc.DeletePool(ctx, req.GetName()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// VolumePoolListTenants lists the tenants assigned to a pool.
func (s *qrdbServiceServer) VolumePoolListTenants(ctx context.Context, req *pb.VolumePoolListTenantsRequest) (*pb.VolumePoolListTenantsResponse, error) {
	if s.volumeSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "volume"})
	}
	page, err := s.volumeSvc.ListAssignments(ctx, req.GetPool(), req.GetPageToken(), int(req.GetPageSize()))
	if err != nil {
		return nil, mapServiceError(err)
	}
	resp := &pb.VolumePoolListTenantsResponse{
		Tenants:       make([]*pb.VolumePoolTenant, len(page.Assignments)),
		NextPageToken: page.NextPageToken,
	}
	for i, a := range page.Assignments {
		resp.Tenants[i] = &pb.VolumePoolTenant{TenantId: a.TenantID, ReservedBytes: a.ReservedBytes}
	}
	return resp, nil
}

// VolumePoolMoveTenant starts moving a tenant to req.ToPool.
func (s *qrdbServiceServer) VolumePoolMoveTenant(ctx context.Context, req *pb.VolumePoolMoveTenantRequest) (*pb.VolumePoolMove, error) {
	if s.volumeSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "volume"})
	}
	m, err := s.volumeSvc.MoveTenant(ctx, volume.MoveTenantRequest{TenantID: req.GetTenant(), ToPool: req.GetToPool()})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return VolumePoolMoveToProto(m), nil
}

// VolumePoolGetMove answers a tenant's move in progress.
func (s *qrdbServiceServer) VolumePoolGetMove(ctx context.Context, req *pb.VolumePoolGetMoveRequest) (*pb.VolumePoolMove, error) {
	if s.volumeSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "volume"})
	}
	m, err := s.volumeSvc.GetMove(ctx, req.GetTenant())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return VolumePoolMoveToProto(m), nil
}

// VolumePoolCancelMove cancels a tenant's move before cut over.
func (s *qrdbServiceServer) VolumePoolCancelMove(ctx context.Context, req *pb.VolumePoolCancelMoveRequest) (*pb.VolumePoolMove, error) {
	if s.volumeSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "volume"})
	}
	m, err := s.volumeSvc.CancelMove(ctx, req.GetTenant())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return VolumePoolMoveToProto(m), nil
}

// VolumePoolMoveToProto converts a move journal to its wire message.
// The QUIC transport shares it. Times are RFC 3339 UTC; a zero
// UpdatedAt is left empty.
func VolumePoolMoveToProto(m *volume.Move) *pb.VolumePoolMove {
	if m == nil {
		return nil
	}
	out := &pb.VolumePoolMove{
		TenantId:            m.TenantID,
		FromPool:            m.FromPool,
		ToPool:              m.ToPool,
		State:               m.State,
		NodesCopied:         m.NodesCopied,
		NodesPending:        m.NodesPending,
		NodesRetired:        m.NodesRetired,
		StepNode:            m.StepNode,
		Error:               m.Error,
		ReservedBytes:       m.ReservedBytes,
		SourceReservedBytes: m.SourceReservedBytes,
		TenantRecorded:      m.TenantRecorded,
	}
	if m.StepAttempts > 0 && m.StepAttempts <= math.MaxInt32 {
		out.StepAttempts = int32(m.StepAttempts)
	}
	if !m.AdmittedAt.IsZero() {
		out.AdmittedAt = m.AdmittedAt.UTC().Format(time.RFC3339Nano)
	}
	if !m.UpdatedAt.IsZero() {
		out.UpdatedAt = m.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// ---------------------------------------------------------------------------
// Proto conversion helpers. Exported so the Unix transport (which registers
// this same qrdbServiceServer over a UDS listener) and QUIC (which encodes
// the SAME qrdbv1 messages over its own framing) can reuse them without
// duplicating the mapping.
// ---------------------------------------------------------------------------

// VolumePoolToProto converts a volume.PoolView to its protobuf form.
func VolumePoolToProto(p *volume.PoolView) *pb.VolumePool { return volumePoolToProto(p) }

func volumePoolToProto(p *volume.PoolView) *pb.VolumePool {
	out := &pb.VolumePool{
		Name:          p.Name,
		Provider:      p.Provider,
		Description:   p.Description,
		CapacityBytes: p.CapacityBytes,
		ReservedBytes: p.ReservedBytes,
		UsedBytes:     p.UsedBytes,
		TenantCount:   int32(p.TenantCount),
		Nodes: &pb.VolumePoolNodeCounts{
			Ready:       int32(p.NodeCounts.Ready),
			Locked:      int32(p.NodeCounts.Locked),
			Unavailable: int32(p.NodeCounts.Unavailable),
			Unmapped:    int32(p.NodeCounts.Unmapped),
		},
		Overcommit: p.Overcommit,
	}
	if !p.CreatedAt.IsZero() {
		out.CreatedAt = p.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !p.UpdatedAt.IsZero() {
		out.UpdatedAt = p.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// VolumePoolDetailToProto converts a volume.PoolDetail to its protobuf form.
func VolumePoolDetailToProto(d *volume.PoolDetail) *pb.VolumePoolDetail {
	return volumePoolDetailToProto(d)
}

func volumePoolDetailToProto(d *volume.PoolDetail) *pb.VolumePoolDetail {
	out := &pb.VolumePoolDetail{
		Pool:        volumePoolToProto(&d.PoolView),
		NodeReports: make([]*pb.VolumePoolNodeReport, len(d.NodeReports)),
	}
	for i, nr := range d.NodeReports {
		row := &pb.VolumePoolNodeReport{
			NodeId: nr.NodeID,
			State:  nr.State,
		}
		if nr.State != volume.NodeStateNoReport {
			row.SizeBytes = nr.SizeBytes
			row.FreeBytes = nr.FreeBytes
			row.Detail = nr.Detail
		}
		if !nr.ReportedAt.IsZero() {
			row.ReportedAt = nr.ReportedAt.UTC().Format(time.RFC3339)
		}
		out.NodeReports[i] = row
	}
	return out
}
