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
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// handleHealth calls the admin service Health endpoint and returns a
// serialized HealthResponse. The payload is ignored. A sealed node never
// reaches it: the sealed-window gate refuses it first.
func (s *Server) handleHealth(ctx context.Context, _ []byte) ([]byte, error) {
	if s.adminSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "admin"}
	}
	info, err := s.adminSvc.Health(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.HealthResponse{
		Status:             info.Status,
		Leader:             info.Leader,
		DiagnosticsDropped: info.DiagnosticsDropped,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleStatus calls the admin service Status endpoint and returns a
// serialized StatusResponse. The payload is ignored.
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

	if err := s.adminSvc.AddMember(ctx, req.GetShardId(), req.GetReplicaId(), req.GetRaftAddress(), req.GetMemberIdentity()); err != nil {
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

// handlePromoteMember unmarshals a PromoteMemberRequest, promotes the
// committed observer via the admin service's MemberPromoter capability, and
// returns an empty serialized response.
func (s *Server) handlePromoteMember(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.PromoteMemberRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	promoter, ok := s.adminSvc.(service.MemberPromoter)
	if !ok {
		return nil, &service.MemberPromotionUnsupportedError{}
	}
	if err := promoter.PromoteMember(ctx, req.GetShardId(), req.GetReplicaId()); err != nil {
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
		Mode:        caps.Mode,
		Kv:          caps.KV,
		Mvcc:        caps.MVCC,
		Transaction: caps.Transaction,
		Batch:       caps.Batch,
		Watch:       caps.Watch,
		Lease:       caps.Lease,
		Lock:        caps.Lock,
		Index:       caps.Index,
		Cluster:     caps.Cluster,
		Backup:      caps.Backup,
		Rebalance:   caps.Rebalance,
		TimeSeries:  caps.TimeSeries,
		Vector:      caps.Vector,
		Sql:         caps.SQL,
		Mysql:       caps.MySQL,
		Postgresql:  caps.PostgreSQL,
		Etcd:        caps.Etcd,
		Fts:         caps.FTS,

		UnencryptedAtRest: caps.UnencryptedAtRest,
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

// handleGetReplicaStates decodes a GetReplicaStatesRequest and returns a
// serialized ReplicaStatesResponse for this node's replicas. It is never
// forwarded: each node answers for itself.
func (s *Server) handleGetReplicaStates(ctx context.Context, payload []byte) ([]byte, error) {
	reader, ok := s.adminSvc.(service.ReplicaStateReader)
	if !ok {
		return nil, &service.ReplicaStateReadUnsupportedError{}
	}
	var req pb.GetReplicaStatesRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	states, err := reader.ReplicaStates(ctx, req.GetShardId())
	if err != nil {
		return nil, err
	}

	replicas := make([]*pb.ReplicaStateProto, 0, len(states.Replicas))
	for _, rs := range states.Replicas {
		replicas = append(replicas, &pb.ReplicaStateProto{
			ShardId:      rs.ShardID,
			ReplicaId:    rs.ReplicaID,
			AppliedIndex: rs.AppliedIndex,
			Hash:         rs.Hash,
		})
	}
	resp, err := proto.Marshal(&pb.ReplicaStatesResponse{NodeId: states.NodeID, Replicas: replicas})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleMintJoinToken unmarshals a MintJoinTokenRequest, mints a single-use
// join token via the admin service, and returns a serialized
// MintJoinTokenResponse. s.adminSvc not implementing
// service.ClusterJoinTokenMinter (member-side CSR join never configured on
// this node) reports the same typed error the REST, gRPC and embedded
// transports do.
func (s *Server) handleMintJoinToken(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.MintJoinTokenRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	minter, ok := s.adminSvc.(service.ClusterJoinTokenMinter)
	if !ok {
		return nil, &service.ClusterJoinTokenNotConfiguredError{}
	}
	ttl, err := service.ParseJoinTokenTTLSeconds(req.GetTtlSeconds())
	if err != nil {
		return nil, err
	}
	token, expiresAt, err := minter.MintJoinToken(ctx, req.GetNodeId(), ttl)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.MintJoinTokenResponse{
		Token:     token,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleAddObserverMember unmarshals an AddObserverMemberRequest, adds the
// new member as a non-voting observer via the admin service's
// ObserverMemberAdder capability, and returns an empty serialized response.
func (s *Server) handleAddObserverMember(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.AddObserverMemberRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	adder, ok := s.adminSvc.(service.ObserverMemberAdder)
	if !ok {
		return nil, &service.ClusterJoinRequestReviewUnsupportedError{}
	}
	if err := adder.AddObserverMember(ctx, req.GetShardId(), req.GetReplicaId(), req.GetRaftAddress(), req.GetMemberIdentity()); err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleListPendingJoinRequests calls the admin service's
// JoinRequestReviewer capability and returns a serialized
// ListPendingJoinRequestsResponse. The payload is ignored.
func (s *Server) handleListPendingJoinRequests(ctx context.Context, _ []byte) ([]byte, error) {
	reviewer, ok := s.adminSvc.(service.JoinRequestReviewer)
	if !ok {
		return nil, &service.ClusterJoinRequestReviewUnsupportedError{}
	}
	pending, err := reviewer.ListPendingJoinRequests(ctx)
	if err != nil {
		return nil, err
	}

	requests := make([]*pb.PendingJoinRequestMessage, 0, len(pending))
	for _, p := range pending {
		requests = append(requests, &pb.PendingJoinRequestMessage{
			Id:          p.ID,
			NodeUri:     p.NodeURI,
			ReplicaId:   p.ReplicaID,
			Features:    p.Features,
			SubmittedAt: p.SubmittedAt.UTC().Format(time.RFC3339),
			ExpiresAt:   p.ExpiresAt.UTC().Format(time.RFC3339),
		})
	}

	resp, err := proto.Marshal(&pb.ListPendingJoinRequestsResponse{Requests: requests})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleApproveJoinRequest unmarshals an ApproveJoinRequestRequest, binds
// the queued CSR to the named node identity via the admin service's
// JoinRequestReviewer capability, and returns an empty serialized response.
func (s *Server) handleApproveJoinRequest(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.ApproveJoinRequestRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	reviewer, ok := s.adminSvc.(service.JoinRequestReviewer)
	if !ok {
		return nil, &service.ClusterJoinRequestReviewUnsupportedError{}
	}
	if err := reviewer.ApproveJoinRequest(ctx, req.GetId(), req.GetNodeId()); err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleRejectJoinRequest unmarshals a RejectJoinRequestRequest, records a
// terminal refusal via the admin service's JoinRequestReviewer capability,
// and returns an empty serialized response.
func (s *Server) handleRejectJoinRequest(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.RejectJoinRequestRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	reviewer, ok := s.adminSvc.(service.JoinRequestReviewer)
	if !ok {
		return nil, &service.ClusterJoinRequestReviewUnsupportedError{}
	}
	if err := reviewer.RejectJoinRequest(ctx, req.GetId(), req.GetReason()); err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleRetireJoinIdentity unmarshals a RetireJoinIdentityRequest,
// permanently refuses any future admission for the named node identity via
// the admin service's JoinIdentityRetirer capability, and returns an empty
// serialized response. s.adminSvc not implementing service.
// JoinIdentityRetirer (member-side CSR join never configured on this node)
// reports the same typed error the REST, gRPC and embedded transports do.
func (s *Server) handleRetireJoinIdentity(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.RetireJoinIdentityRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	retirer, ok := s.adminSvc.(service.JoinIdentityRetirer)
	if !ok {
		return nil, &service.ClusterJoinTokenNotConfiguredError{}
	}
	if err := retirer.RetireJoinIdentity(ctx, req.GetNodeId(), req.GetReason()); err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
