//go:build qrdb_etcd

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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// clusterServer implements etcdserverpb.ClusterServer by delegating
// to qrdb's AdminServicer scoped to the database's shard.
type clusterServer struct {
	pb.UnimplementedClusterServer
	adminSvc  service.AdminServicer
	clusterID uint64
	memberID  uint64
}

// MemberList returns the cluster membership. Peers from all loaded
// shards are flattened into a single etcd-compatible member list.
func (s *clusterServer) MemberList(ctx context.Context, _ *pb.MemberListRequest) (*pb.MemberListResponse, error) {
	if s.adminSvc == nil {
		return nil, status.Error(codes.Unavailable, "admin service not configured")
	}
	peersInfo, err := s.adminSvc.Peers(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "member list: %v", err)
	}

	// Flatten all shard peers into a deduplicated member list keyed
	// by ReplicaID to avoid duplicates across shards.
	seen := make(map[uint64]struct{})
	var members []*pb.Member
	for _, sp := range peersInfo.Shards {
		for _, p := range sp.Peers {
			if _, ok := seen[p.ReplicaID]; ok {
				continue
			}
			seen[p.ReplicaID] = struct{}{}
			members = append(members, &pb.Member{
				ID:         p.ReplicaID,
				Name:       p.Address,
				PeerURLs:   []string{p.Address},
				ClientURLs: []string{p.Address},
			})
		}
	}
	return &pb.MemberListResponse{
		Header:  responseHeader(s.clusterID, s.memberID, 0, 0),
		Members: members,
	}, nil
}

// MemberAdd adds a member to the cluster. Not supported through the
// etcd API; use the qrdb admin API instead.
func (s *clusterServer) MemberAdd(_ context.Context, _ *pb.MemberAddRequest) (*pb.MemberAddResponse, error) {
	return nil, status.Error(codes.Unimplemented, "member add: use qrdb admin API")
}

// MemberRemove removes a member from the cluster. Not supported through
// the etcd API; use the qrdb admin API instead.
func (s *clusterServer) MemberRemove(_ context.Context, _ *pb.MemberRemoveRequest) (*pb.MemberRemoveResponse, error) {
	return nil, status.Error(codes.Unimplemented, "member remove: use qrdb admin API")
}

// MemberUpdate updates a member's configuration. Not supported through
// the etcd API; use the qrdb admin API instead.
func (s *clusterServer) MemberUpdate(_ context.Context, _ *pb.MemberUpdateRequest) (*pb.MemberUpdateResponse, error) {
	return nil, status.Error(codes.Unimplemented, "member update: use qrdb admin API")
}

// MemberPromote promotes a learner member. Not supported through the
// etcd API; use the qrdb admin API instead.
func (s *clusterServer) MemberPromote(_ context.Context, _ *pb.MemberPromoteRequest) (*pb.MemberPromoteResponse, error) {
	return nil, status.Error(codes.Unimplemented, "member promote: use qrdb admin API")
}
