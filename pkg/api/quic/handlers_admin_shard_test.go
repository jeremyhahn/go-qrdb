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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// handleStartShard
// ---------------------------------------------------------------------------

func TestHandleStartShard_Success(t *testing.T) {
	var capturedReq *service.StartShardRequest
	adminSvc := &mockAdminService{}
	adminSvc.startShardFn = func(_ context.Context, req *service.StartShardRequest) error {
		capturedReq = req
		return nil
	}
	srv := newTestServer(&mockKVService{}, adminSvc)

	payload, err := proto.Marshal(&pb.StartShardRequest{
		ShardId:   10,
		ReplicaId: 20,
		Members:   map[uint64]string{20: "127.0.0.1:5000"},
		Join:      false,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	resp, err := srv.handleStartShard(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var empty emptypb.Empty
	if err := proto.Unmarshal(resp, &empty); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}

	if capturedReq == nil {
		t.Fatal("expected StartShard to be called")
	}
	if capturedReq.ShardID != 10 {
		t.Errorf("expected shardID 10, got %d", capturedReq.ShardID)
	}
	if capturedReq.ReplicaID != 20 {
		t.Errorf("expected replicaID 20, got %d", capturedReq.ReplicaID)
	}
}

func TestHandleStartShard_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "start shard error"}
	adminSvc := &mockAdminService{}
	adminSvc.startShardFn = func(_ context.Context, _ *service.StartShardRequest) error { return svcErr }
	srv := newTestServer(&mockKVService{}, adminSvc)

	payload, _ := proto.Marshal(&pb.StartShardRequest{ShardId: 1, ReplicaId: 1})
	_, err := srv.handleStartShard(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T", err)
	}
}

func TestHandleStartShard_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleStartShard(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleStartShard_WithJoinAndMembers(t *testing.T) {
	var capturedJoin bool
	adminSvc := &mockAdminService{}
	adminSvc.startShardFn = func(_ context.Context, req *service.StartShardRequest) error {
		capturedJoin = req.Join
		return nil
	}
	srv := newTestServer(&mockKVService{}, adminSvc)

	payload, _ := proto.Marshal(&pb.StartShardRequest{
		ShardId:   5,
		ReplicaId: 10,
		Join:      true,
		Members:   map[uint64]string{10: "10.0.0.1:5000", 11: "10.0.0.2:5000"},
	})
	if _, err := srv.handleStartShard(context.Background(), payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !capturedJoin {
		t.Error("expected join=true")
	}
}

// ---------------------------------------------------------------------------
// handleStopShard
// ---------------------------------------------------------------------------

func TestHandleStopShard_Success(t *testing.T) {
	var capturedShardID uint64
	adminSvc := &mockAdminService{}
	adminSvc.stopShardFn = func(_ context.Context, shardID uint64) error {
		capturedShardID = shardID
		return nil
	}
	srv := newTestServer(&mockKVService{}, adminSvc)

	payload, err := proto.Marshal(&pb.StopShardRequest{ShardId: 99})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	resp, err := srv.handleStopShard(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var empty emptypb.Empty
	if err := proto.Unmarshal(resp, &empty); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}

	if capturedShardID != 99 {
		t.Errorf("expected shardID 99, got %d", capturedShardID)
	}
}

func TestHandleStopShard_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "stop shard error"}
	adminSvc := &mockAdminService{}
	adminSvc.stopShardFn = func(_ context.Context, _ uint64) error { return svcErr }
	srv := newTestServer(&mockKVService{}, adminSvc)

	payload, _ := proto.Marshal(&pb.StopShardRequest{ShardId: 1})
	_, err := srv.handleStopShard(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T", err)
	}
}

func TestHandleStopShard_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleStopShard(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleGetShardState
// ---------------------------------------------------------------------------

func TestHandleGetShardState_Success(t *testing.T) {
	adminSvc := &mockAdminService{}
	adminSvc.getShardStateFn = func(_ context.Context, shardID uint64) (*service.ShardStateResponse, error) {
		return &service.ShardStateResponse{
			ShardID:           shardID,
			ConfigChangeIndex: 7,
			Term:              3,
			LeaderID:          10,
			HasLeader:         true,
		}, nil
	}
	srv := newTestServer(&mockKVService{}, adminSvc)

	payload, err := proto.Marshal(&pb.GetShardStateRequest{ShardId: 42})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	resp, err := srv.handleGetShardState(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var stateResp pb.ShardStateResponse
	if err := proto.Unmarshal(resp, &stateResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if stateResp.GetShardId() != 42 {
		t.Errorf("expected shard_id 42, got %d", stateResp.GetShardId())
	}
	if stateResp.GetConfigChangeIndex() != 7 {
		t.Errorf("expected config_change_index 7, got %d", stateResp.GetConfigChangeIndex())
	}
	if stateResp.GetTerm() != 3 {
		t.Errorf("expected term 3, got %d", stateResp.GetTerm())
	}
	if stateResp.GetLeaderId() != 10 {
		t.Errorf("expected leader_id 10, got %d", stateResp.GetLeaderId())
	}
	if !stateResp.GetHasLeader() {
		t.Error("expected has_leader=true")
	}
}

func TestHandleGetShardState_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "get shard state error"}
	adminSvc := &mockAdminService{}
	adminSvc.getShardStateFn = func(_ context.Context, _ uint64) (*service.ShardStateResponse, error) { return nil, svcErr }
	srv := newTestServer(&mockKVService{}, adminSvc)

	payload, _ := proto.Marshal(&pb.GetShardStateRequest{ShardId: 1})
	_, err := srv.handleGetShardState(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T", err)
	}
}

func TestHandleGetShardState_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleGetShardState(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleGetTopology
// ---------------------------------------------------------------------------

func TestHandleGetTopology_Success(t *testing.T) {
	adminSvc := &mockAdminService{}
	adminSvc.getTopologyFn = func(_ context.Context) (*service.TopologyResponse, error) {
		return &service.TopologyResponse{
			NodeID:      "node-1",
			RaftAddress: "127.0.0.1:5000",
			ShardCount:  2,
			Shards: []service.TopologyShardInfo{
				{ShardID: 1, ReplicaID: 10, IsLeader: true, LeaderID: 10, Nodes: map[uint64]string{10: "127.0.0.1:5000"}},
				{ShardID: 2, ReplicaID: 10, IsLeader: false, LeaderID: 11, Nodes: map[uint64]string{10: "127.0.0.1:5000"}},
			},
		}, nil
	}
	srv := newTestServer(&mockKVService{}, adminSvc)

	resp, err := srv.handleGetTopology(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var topoResp pb.TopologyResponse
	if err := proto.Unmarshal(resp, &topoResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if topoResp.GetNodeId() != "node-1" {
		t.Errorf("expected node_id 'node-1', got %q", topoResp.GetNodeId())
	}
	if topoResp.GetRaftAddress() != "127.0.0.1:5000" {
		t.Errorf("expected raft_address '127.0.0.1:5000', got %q", topoResp.GetRaftAddress())
	}
	if topoResp.GetShardCount() != 2 {
		t.Errorf("expected shard_count 2, got %d", topoResp.GetShardCount())
	}
	if len(topoResp.GetShards()) != 2 {
		t.Errorf("expected 2 shards, got %d", len(topoResp.GetShards()))
	}
}

func TestHandleGetTopology_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "topology error"}
	adminSvc := &mockAdminService{}
	adminSvc.getTopologyFn = func(_ context.Context) (*service.TopologyResponse, error) { return nil, svcErr }
	srv := newTestServer(&mockKVService{}, adminSvc)

	_, err := srv.handleGetTopology(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T", err)
	}
}

func TestHandleGetTopology_EmptyShards(t *testing.T) {
	adminSvc := &mockAdminService{}
	adminSvc.getTopologyFn = func(_ context.Context) (*service.TopologyResponse, error) {
		return &service.TopologyResponse{NodeID: "n1", RaftAddress: "addr"}, nil
	}
	srv := newTestServer(&mockKVService{}, adminSvc)

	resp, err := srv.handleGetTopology(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var topoResp pb.TopologyResponse
	if err := proto.Unmarshal(resp, &topoResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(topoResp.GetShards()) != 0 {
		t.Errorf("expected 0 shards, got %d", len(topoResp.GetShards()))
	}
}

// ---------------------------------------------------------------------------
// Server option tests for WithCollectionService, WithDatabaseService,
// WithBarrierService, WithCeremonyService
// ---------------------------------------------------------------------------

func TestWithCollectionService_SetsField(t *testing.T) {
	collSvc := &mockCollectionService{}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithCollectionService(collSvc),
	)
	if srv.collectionSvc != collSvc {
		t.Fatal("expected collectionSvc to be set")
	}
}

func TestWithDatabaseService_SetsField(t *testing.T) {
	dbSvc := &mockDatabaseService{}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithDatabaseService(dbSvc),
	)
	if srv.databaseSvc != dbSvc {
		t.Fatal("expected databaseSvc to be set")
	}
}

func TestWithCeremonyService_SetsField(t *testing.T) {
	ceremonySvc := &mockCeremonyService{}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithCeremonyService(ceremonySvc),
	)
	if srv.ceremonySvc != ceremonySvc {
		t.Fatal("expected ceremonySvc to be set")
	}
}
