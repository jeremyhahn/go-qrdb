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

package etcd

import (
	"context"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
)

// leaseServer implements etcdserverpb.LeaseServer by delegating to
// qrdb's LeaseServicer.
type leaseServer struct {
	pb.UnimplementedLeaseServer
	leaseSvc  lease.LeaseServicer
	clusterID uint64
	memberID  uint64
}

// LeaseGrant handles etcd LeaseGrant requests.
func (s *leaseServer) LeaseGrant(ctx context.Context, req *pb.LeaseGrantRequest) (*pb.LeaseGrantResponse, error) {
	l, err := s.leaseSvc.Grant(ctx, req.TTL)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "lease grant: %v", err)
	}
	return &pb.LeaseGrantResponse{
		Header: responseHeader(s.clusterID, s.memberID, 0, 0),
		ID:     l.ID,
		TTL:    l.TTL,
	}, nil
}

// LeaseRevoke handles etcd LeaseRevoke requests.
func (s *leaseServer) LeaseRevoke(ctx context.Context, req *pb.LeaseRevokeRequest) (*pb.LeaseRevokeResponse, error) {
	if err := s.leaseSvc.Revoke(ctx, req.ID); err != nil {
		return nil, status.Errorf(codes.Internal, "lease revoke: %v", err)
	}
	return &pb.LeaseRevokeResponse{
		Header: responseHeader(s.clusterID, s.memberID, 0, 0),
	}, nil
}

// LeaseKeepAlive handles the bidirectional KeepAlive stream.
func (s *leaseServer) LeaseKeepAlive(stream pb.Lease_LeaseKeepAliveServer) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}

		l, err := s.leaseSvc.KeepAlive(stream.Context(), req.ID)
		if err != nil {
			return status.Errorf(codes.Internal, "lease keepalive: %v", err)
		}

		if sendErr := stream.Send(&pb.LeaseKeepAliveResponse{
			Header: responseHeader(s.clusterID, s.memberID, 0, 0),
			ID:     l.ID,
			TTL:    l.TTL,
		}); sendErr != nil {
			return sendErr
		}
	}
}

// LeaseTimeToLive handles etcd LeaseTimeToLive requests.
func (s *leaseServer) LeaseTimeToLive(ctx context.Context, req *pb.LeaseTimeToLiveRequest) (*pb.LeaseTimeToLiveResponse, error) {
	l, err := s.leaseSvc.TimeToLive(ctx, req.ID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "lease not found: %v", err)
	}
	resp := &pb.LeaseTimeToLiveResponse{
		Header:     responseHeader(s.clusterID, s.memberID, 0, 0),
		ID:         l.ID,
		TTL:        l.RemainingTTL,
		GrantedTTL: l.GrantedTTL,
	}
	if req.Keys {
		resp.Keys = make([][]byte, len(l.Keys))
		copy(resp.Keys, l.Keys)
	}
	return resp, nil
}

// LeaseLeases handles etcd LeaseLeases requests.
func (s *leaseServer) LeaseLeases(ctx context.Context, _ *pb.LeaseLeasesRequest) (*pb.LeaseLeasesResponse, error) {
	leases, err := s.leaseSvc.Leases(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "lease list: %v", err)
	}
	statuses := make([]*pb.LeaseStatus, len(leases))
	for i, l := range leases {
		statuses[i] = &pb.LeaseStatus{ID: l.ID}
	}
	return &pb.LeaseLeasesResponse{
		Header: responseHeader(s.clusterID, s.memberID, 0, 0),
		Leases: statuses,
	}, nil
}
