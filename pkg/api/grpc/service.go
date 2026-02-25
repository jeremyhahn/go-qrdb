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

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// qrdbServiceServer implements the generated QRDBServiceServer interface.
// It delegates all operations to the service layer through KVService and
// AdminService interfaces, translating between protobuf messages and
// service-layer types.
type qrdbServiceServer struct {
	pb.UnimplementedQRDBServiceServer
	kvSvc    service.KVServicer
	adminSvc service.AdminServicer
}

// Put stores a key-value pair via Raft consensus.
func (s *qrdbServiceServer) Put(ctx context.Context, req *pb.PutRequest) (*pb.PutResponse, error) {
	if err := s.kvSvc.Put(ctx, req.GetKey(), req.GetValue()); err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.PutResponse{}, nil
}

// Get retrieves a value by key using a linearizable read.
func (s *qrdbServiceServer) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	data, err := s.kvSvc.Get(ctx, req.GetKey())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.GetResponse{Value: data}, nil
}

// Delete removes a key via Raft consensus.
func (s *qrdbServiceServer) Delete(ctx context.Context, req *pb.DeleteRequest) (*pb.DeleteResponse, error) {
	if err := s.kvSvc.Delete(ctx, req.GetKey()); err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.DeleteResponse{}, nil
}

// List returns sorted keys matching a prefix.
func (s *qrdbServiceServer) List(ctx context.Context, req *pb.ListRequest) (*pb.ListResponse, error) {
	keys, err := s.kvSvc.List(ctx, req.GetPrefix())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.ListResponse{Keys: keys}, nil
}

// Health returns the node health status.
func (s *qrdbServiceServer) Health(ctx context.Context, _ *emptypb.Empty) (*pb.HealthResponse, error) {
	info, err := s.adminSvc.Health(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.HealthResponse{
		Status: info.Status,
		Leader: info.Leader,
	}, nil
}

// Status returns detailed node status information.
func (s *qrdbServiceServer) Status(ctx context.Context, _ *emptypb.Empty) (*pb.StatusResponse, error) {
	info, err := s.adminSvc.Status(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.StatusResponse{
		NodeId:      info.NodeID,
		RaftAddress: info.RaftAddress,
		IsLeader:    info.IsLeader,
		LeaderId:    info.LeaderID,
		ShardCount:  int32(info.ShardCount),
		IsDraining:  info.IsDraining,
	}, nil
}

// Peers returns the peer list for all loaded shards.
func (s *qrdbServiceServer) Peers(ctx context.Context, _ *emptypb.Empty) (*pb.PeersResponse, error) {
	info, err := s.adminSvc.Peers(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}

	shards := make([]*pb.ShardPeers, 0, len(info.Shards))
	for _, sp := range info.Shards {
		peers := make([]*pb.PeerInfo, 0, len(sp.Peers))
		for _, p := range sp.Peers {
			peers = append(peers, &pb.PeerInfo{
				ReplicaId: p.ReplicaID,
				Address:   p.Address,
				IsLeader:  p.IsLeader,
			})
		}
		shards = append(shards, &pb.ShardPeers{
			ShardId:  sp.ShardID,
			LeaderId: sp.LeaderID,
			Peers:    peers,
		})
	}

	return &pb.PeersResponse{Shards: shards}, nil
}

// AddMember adds a new node to a shard.
func (s *qrdbServiceServer) AddMember(ctx context.Context, req *pb.AddMemberRequest) (*emptypb.Empty, error) {
	if err := s.adminSvc.AddMember(ctx, req.GetShardId(), req.GetReplicaId(), req.GetRaftAddress()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// RemoveMember removes a node from a shard.
func (s *qrdbServiceServer) RemoveMember(ctx context.Context, req *pb.RemoveMemberRequest) (*emptypb.Empty, error) {
	if err := s.adminSvc.RemoveMember(ctx, req.GetShardId(), req.GetReplicaId()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// RequestSnapshot triggers a snapshot for the given shard.
func (s *qrdbServiceServer) RequestSnapshot(ctx context.Context, req *pb.RequestSnapshotMessage) (*emptypb.Empty, error) {
	if err := s.adminSvc.RequestSnapshot(ctx, req.GetShardId()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// Drain transitions the node into drain mode.
func (s *qrdbServiceServer) Drain(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	if err := s.adminSvc.Drain(ctx); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}
