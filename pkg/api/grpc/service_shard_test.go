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

package grpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// erroringAdminService wraps mockAdminService to inject errors on demand
// for methods that the base mock always succeeds on.
type erroringAdminService struct {
	*mockAdminService
	startShardErr    error
	stopShardErr     error
	getShardStateErr error
	getTopologyErr   error
}

func (e *erroringAdminService) StartShard(_ context.Context, _ *service.StartShardRequest) error {
	return e.startShardErr
}

func (e *erroringAdminService) StopShard(_ context.Context, _ uint64) error {
	return e.stopShardErr
}

func (e *erroringAdminService) GetShardState(_ context.Context, shardID uint64) (*service.ShardStateResponse, error) {
	if e.getShardStateErr != nil {
		return nil, e.getShardStateErr
	}
	return e.mockAdminService.GetShardState(nil, shardID)
}

func (e *erroringAdminService) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	if e.getTopologyErr != nil {
		return nil, e.getTopologyErr
	}
	return &service.TopologyResponse{}, nil
}

func (e *erroringAdminService) NodeCompaction(_ context.Context) (*compaction.Policy, error) {
	return nil, nil
}

func (e *erroringAdminService) SetNodeCompaction(_ context.Context, _ *compaction.Policy) error {
	return nil
}

func TestGRPC_StartShard_Success(t *testing.T) {
	client, cleanup := startTestServer(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.StartShard(context.Background(), &pb.StartShardRequest{
		ShardId:   1,
		ReplicaId: 1,
		Members:   map[uint64]string{1: "127.0.0.1:63001"},
		Join:      false,
	})
	if err != nil {
		t.Fatalf("StartShard failed: %v", err)
	}
}

func TestGRPC_StopShard_Success(t *testing.T) {
	client, cleanup := startTestServer(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.StopShard(context.Background(), &pb.StopShardRequest{ShardId: 1})
	if err != nil {
		t.Fatalf("StopShard failed: %v", err)
	}
}

func TestGRPC_GetShardState_Success(t *testing.T) {
	client, cleanup := startTestServer(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := client.GetShardState(context.Background(), &pb.GetShardStateRequest{ShardId: 1})
	if err != nil {
		t.Fatalf("GetShardState failed: %v", err)
	}
	if resp.GetShardId() != 1 {
		t.Fatalf("expected shard_id=1, got %d", resp.GetShardId())
	}
}

func TestGRPC_GetTopology_Success(t *testing.T) {
	client, cleanup := startTestServer(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := client.GetTopology(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("GetTopology failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// TestGRPC_ShardAdmin_ServiceErrorIsInternal checks that each shard admin
// handler maps a failed propose or read from the admin service to Internal.
func TestGRPC_ShardAdmin_ServiceErrorIsInternal(t *testing.T) {
	tests := []struct {
		name  string
		admin *erroringAdminService
		call  func(pb.QRDBServiceClient) error
	}{
		{
			name:  "StartShard",
			admin: &erroringAdminService{startShardErr: &service.ProposeError{Err: errors.New("shard start failed")}},
			call: func(c pb.QRDBServiceClient) error {
				_, err := c.StartShard(context.Background(), &pb.StartShardRequest{ShardId: 1, ReplicaId: 1})
				return err
			},
		},
		{
			name:  "StopShard",
			admin: &erroringAdminService{stopShardErr: &service.ProposeError{Err: errors.New("stop failed")}},
			call: func(c pb.QRDBServiceClient) error {
				_, err := c.StopShard(context.Background(), &pb.StopShardRequest{ShardId: 1})
				return err
			},
		},
		{
			name:  "GetShardState",
			admin: &erroringAdminService{getShardStateErr: &service.ReadError{Err: errors.New("state error")}},
			call: func(c pb.QRDBServiceClient) error {
				_, err := c.GetShardState(context.Background(), &pb.GetShardStateRequest{ShardId: 1})
				return err
			},
		},
		{
			name:  "GetTopology",
			admin: &erroringAdminService{getTopologyErr: &service.ReadError{Err: errors.New("topology error")}},
			call: func(c pb.QRDBServiceClient) error {
				_, err := c.GetTopology(context.Background(), &emptypb.Empty{})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.admin.mockAdminService = newMockAdminService()
			client, cleanup := startTestServer(t, newMockKVService(), tt.admin)
			defer cleanup()

			err := tt.call(client)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if st, _ := status.FromError(err); st.Code() != codes.Internal {
				t.Fatalf("expected Internal, got %s", st.Code())
			}
		})
	}
}
