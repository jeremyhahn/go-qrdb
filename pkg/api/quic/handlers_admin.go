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
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// handleHealth calls the admin service Health endpoint and returns a
// serialized HealthResponse. The payload is ignored.
//
// admin.Health is on the sealed-window allowlist (pkg/api/sealedwindow.Table
// VerbHealth), so this handler is reachable BEFORE the admin service exists
// (adminSvc is nil throughout the pre-host sealed window --
// cmd/qrdbd/seal_subsystem.go's awaitUnseal only wires the barrier/ceremony/
// credential services). Rather than error in that state (which the
// per-stream recover() in server.go would otherwise turn into an opaque
// "internal server error", and which would make sdk/go/transport/quic's
// Connect() -- which requires a successful Health RPC -- treat a perfectly
// reachable, correctly-sealed daemon as unreachable), report the documented
// "sealed" HealthStatus verdict directly.
func (s *Server) handleHealth(ctx context.Context, _ []byte) ([]byte, error) {
	if s.adminSvc == nil {
		resp, err := proto.Marshal(&pb.HealthResponse{Status: service.HealthStatusSealed})
		if err != nil {
			return nil, &MarshalError{Err: err}
		}
		return resp, nil
	}
	info, err := s.adminSvc.Health(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.HealthResponse{
		Status: info.Status,
		Leader: info.Leader,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleStatus calls the admin service Status endpoint and returns a
// serialized StatusResponse. The payload is ignored.
//
// admin.Status is also on the sealed-window allowlist (VerbStatus); see
// handleHealth's doc comment for why the nil guard below is required.
func (s *Server) handleStatus(ctx context.Context, _ []byte) ([]byte, error) {
	if s.adminSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "admin"}
	}
	info, err := s.adminSvc.Status(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.StatusResponse{
		NodeId:      info.NodeID,
		RaftAddress: info.RaftAddress,
		IsLeader:    info.IsLeader,
		LeaderId:    info.LeaderID,
		ShardCount:  int32(info.ShardCount),
		IsDraining:  info.IsDraining,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handlePeers calls the admin service Peers endpoint and returns a serialized
// PeersResponse with nested ShardPeers and PeerInfo. The payload is ignored.
func (s *Server) handlePeers(ctx context.Context, _ []byte) ([]byte, error) {
	info, err := s.adminSvc.Peers(ctx)
	if err != nil {
		return nil, err
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

	resp, err := proto.Marshal(&pb.PeersResponse{Shards: shards})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleAddMember unmarshals an AddMemberRequest, adds the new member via
// the admin service, and returns an empty serialized response.
func (s *Server) handleAddMember(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.AddMemberRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.adminSvc.AddMember(ctx, req.GetShardId(), req.GetReplicaId(), req.GetRaftAddress()); err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleRemoveMember unmarshals a RemoveMemberRequest, removes the member
// via the admin service, and returns an empty serialized response.
func (s *Server) handleRemoveMember(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.RemoveMemberRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.adminSvc.RemoveMember(ctx, req.GetShardId(), req.GetReplicaId()); err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleRequestSnapshot unmarshals a RequestSnapshotMessage, triggers a
// snapshot for the given shard, and returns an empty serialized response.
func (s *Server) handleRequestSnapshot(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.RequestSnapshotMessage
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.adminSvc.RequestSnapshot(ctx, req.GetShardId()); err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleDrain transitions the node into drain mode via the admin service
// and returns an empty serialized response. The payload is ignored.
func (s *Server) handleDrain(ctx context.Context, _ []byte) ([]byte, error) {
	if err := s.adminSvc.Drain(ctx); err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCapabilities calls the admin service Capabilities endpoint and
// returns a serialized CapabilitiesResponse. The payload is ignored.
func (s *Server) handleCapabilities(ctx context.Context, _ []byte) ([]byte, error) {
	caps, err := s.adminSvc.Capabilities(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.CapabilitiesResponse{
		Kv:          caps.KV,
		Mvcc:        caps.MVCC,
		Transaction: caps.Transaction,
		Batch:       caps.Batch,
		Watch:       caps.Watch,
		Lease:       caps.Lease,
		Index:       caps.Index,
		TimeSeries:  caps.TimeSeries,
		Vector:      caps.Vector,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleStartShard unmarshals a StartShardRequest, starts the shard via
// the admin service, and returns an empty serialized response.
func (s *Server) handleStartShard(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.StartShardRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	svcReq := &service.StartShardRequest{
		ShardID:   req.GetShardId(),
		ReplicaID: req.GetReplicaId(),
		Members:   req.GetMembers(),
		Join:      req.GetJoin(),
	}

	if err := s.adminSvc.StartShard(ctx, svcReq); err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleStopShard unmarshals a StopShardRequest, stops the shard via
// the admin service, and returns an empty serialized response.
func (s *Server) handleStopShard(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.StopShardRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.adminSvc.StopShard(ctx, req.GetShardId()); err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleGetShardState unmarshals a GetShardStateRequest, queries the
// shard state via the admin service, and returns a serialized
// ShardStateResponse.
func (s *Server) handleGetShardState(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.GetShardStateRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	state, err := s.adminSvc.GetShardState(ctx, req.GetShardId())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.ShardStateResponse{
		ShardId:           state.ShardID,
		ConfigChangeIndex: state.ConfigChangeIndex,
		Term:              state.Term,
		LeaderId:          state.LeaderID,
		HasLeader:         state.HasLeader,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleGetTopology calls the admin service GetTopology endpoint and
// returns a serialized TopologyResponse. The payload is ignored.
func (s *Server) handleGetTopology(ctx context.Context, _ []byte) ([]byte, error) {
	topo, err := s.adminSvc.GetTopology(ctx)
	if err != nil {
		return nil, err
	}

	shards := make([]*pb.ShardInfoProto, 0, len(topo.Shards))
	for _, si := range topo.Shards {
		shards = append(shards, &pb.ShardInfoProto{
			ShardId:   si.ShardID,
			ReplicaId: si.ReplicaID,
			IsLeader:  si.IsLeader,
			LeaderId:  si.LeaderID,
			Nodes:     si.Nodes,
		})
	}

	resp, err := proto.Marshal(&pb.TopologyResponse{
		NodeId:      topo.NodeID,
		RaftAddress: topo.RaftAddress,
		ShardCount:  int32(topo.ShardCount),
		Shards:      shards,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
