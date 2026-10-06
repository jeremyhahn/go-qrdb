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

	"google.golang.org/protobuf/proto"

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// ---------------------------------------------------------------------------
// Volume pool handlers (docs/volume-pools/api.md). Conversions reuse
// grpcapi.VolumePoolToProto / VolumePoolDetailToProto -- the SAME qrdbv1
// messages gRPC and Unix encode, so the wire shape cannot drift between
// transports.
// ---------------------------------------------------------------------------

// handleVolumePoolList lists every pool in the catalog.
func (s *Server) handleVolumePoolList(ctx context.Context, _ []byte) ([]byte, error) {
	if s.volumeSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "volume"}
	}
	pools, err := s.volumeSvc.ListPools(ctx)
	if err != nil {
		return nil, err
	}
	resp := &pb.VolumePoolListResponse{Pools: make([]*pb.VolumePool, len(pools))}
	for i := range pools {
		resp.Pools[i] = grpcapi.VolumePoolToProto(&pools[i])
	}
	out, err := proto.Marshal(resp)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVolumePoolGet inspects one pool.
func (s *Server) handleVolumePoolGet(ctx context.Context, payload []byte) ([]byte, error) {
	if s.volumeSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "volume"}
	}
	var req pb.VolumePoolGetRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	detail, err := s.volumeSvc.GetPool(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	out, err := proto.Marshal(grpcapi.VolumePoolDetailToProto(detail))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVolumePoolCreate adds a pool to the catalog.
func (s *Server) handleVolumePoolCreate(ctx context.Context, payload []byte) ([]byte, error) {
	if s.volumeSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "volume"}
	}
	var req pb.VolumePoolCreateRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	pool, err := s.volumeSvc.CreatePool(ctx, volume.CreatePoolRequest{
		Name:          req.GetName(),
		Provider:      req.GetProvider(),
		Description:   req.GetDescription(),
		CapacityBytes: req.GetCapacityBytes(),
		Overcommit:    req.GetOvercommit(),
	})
	if err != nil {
		return nil, err
	}
	out, err := proto.Marshal(grpcapi.VolumePoolToProto(&volume.PoolView{Pool: *pool}))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVolumePoolUpdate changes a pool's description, capacity or overcommit.
func (s *Server) handleVolumePoolUpdate(ctx context.Context, payload []byte) ([]byte, error) {
	if s.volumeSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "volume"}
	}
	var req pb.VolumePoolUpdateRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
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
		return nil, err
	}
	out, err := proto.Marshal(grpcapi.VolumePoolToProto(&volume.PoolView{Pool: *pool}))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleVolumePoolDelete removes a pool from the catalog.
func (s *Server) handleVolumePoolDelete(ctx context.Context, payload []byte) ([]byte, error) {
	if s.volumeSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "volume"}
	}
	var req pb.VolumePoolDeleteRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.volumeSvc.DeletePool(ctx, req.GetName()); err != nil {
		return nil, err
	}
	return nil, nil
}

// handleVolumePoolListTenants lists the tenants assigned to a pool.
func (s *Server) handleVolumePoolListTenants(ctx context.Context, payload []byte) ([]byte, error) {
	if s.volumeSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "volume"}
	}
	var req pb.VolumePoolListTenantsRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	page, err := s.volumeSvc.ListAssignments(ctx, req.GetPool(), req.GetPageToken(), int(req.GetPageSize()))
	if err != nil {
		return nil, err
	}
	resp := &pb.VolumePoolListTenantsResponse{
		Tenants:       make([]*pb.VolumePoolTenant, len(page.Assignments)),
		NextPageToken: page.NextPageToken,
	}
	for i, a := range page.Assignments {
		resp.Tenants[i] = &pb.VolumePoolTenant{TenantId: a.TenantID, ReservedBytes: a.ReservedBytes}
	}
	out, err := proto.Marshal(resp)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}
