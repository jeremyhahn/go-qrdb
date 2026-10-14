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
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// replicaStateAdmin is a mockAdminService that can read replica states, as
// the real AdminService can.
type replicaStateAdmin struct {
	*mockAdminService
	replicaStatesFn func(ctx context.Context, shardID uint64) (*service.ReplicaStatesResponse, error)
}

func (a *replicaStateAdmin) ReplicaStates(ctx context.Context, shardID uint64) (*service.ReplicaStatesResponse, error) {
	return a.replicaStatesFn(ctx, shardID)
}

var _ service.ReplicaStateReader = (*replicaStateAdmin)(nil)

func TestHandleGetReplicaStates_Success(t *testing.T) {
	var gotShard uint64
	admin := &replicaStateAdmin{mockAdminService: &mockAdminService{},
		replicaStatesFn: func(_ context.Context, shardID uint64) (*service.ReplicaStatesResponse, error) {
			gotShard = shardID
			return &service.ReplicaStatesResponse{NodeID: "node-1", Replicas: []service.ReplicaState{
				{ShardID: 1, ReplicaID: 1, AppliedIndex: 48211, Hash: 1<<63 + 5},
			}}, nil
		}}
	srv := newTestServer(&mockKVService{}, admin)

	payload, err := proto.Marshal(&pb.GetReplicaStatesRequest{ShardId: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := srv.handleGetReplicaStates(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleGetReplicaStates: %v", err)
	}
	if gotShard != 1 {
		t.Fatalf("shard passed to the service = %d, want 1", gotShard)
	}
	var out pb.ReplicaStatesResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetNodeId() != "node-1" || len(out.GetReplicas()) != 1 {
		t.Fatalf("resp = %v", &out)
	}
	if r := out.GetReplicas()[0]; r.GetAppliedIndex() != 48211 || r.GetHash() != 1<<63+5 || r.GetReplicaId() != 1 {
		t.Fatalf("replica = %v", r)
	}
}

func TestHandleGetReplicaStates_EmptyPayloadReadsEveryShard(t *testing.T) {
	gotShard := uint64(99)
	admin := &replicaStateAdmin{mockAdminService: &mockAdminService{},
		replicaStatesFn: func(_ context.Context, shardID uint64) (*service.ReplicaStatesResponse, error) {
			gotShard = shardID
			return &service.ReplicaStatesResponse{NodeID: "node-1"}, nil
		}}
	if _, err := newTestServer(&mockKVService{}, admin).handleGetReplicaStates(context.Background(), nil); err != nil {
		t.Fatalf("handleGetReplicaStates: %v", err)
	}
	if gotShard != 0 {
		t.Fatalf("shard passed to the service = %d, want 0", gotShard)
	}
}

func TestHandleGetReplicaStates_ServiceError(t *testing.T) {
	admin := &replicaStateAdmin{mockAdminService: &mockAdminService{},
		replicaStatesFn: func(context.Context, uint64) (*service.ReplicaStatesResponse, error) {
			return nil, &service.ReplicaNotHostedError{ShardID: 9}
		}}
	payload, _ := proto.Marshal(&pb.GetReplicaStatesRequest{ShardId: 9})
	_, err := newTestServer(&mockKVService{}, admin).handleGetReplicaStates(context.Background(), payload)
	var nh *service.ReplicaNotHostedError
	if !errors.As(err, &nh) {
		t.Fatalf("err = %v, want *service.ReplicaNotHostedError", err)
	}
}

func TestHandleGetReplicaStates_UnmarshalError(t *testing.T) {
	admin := &replicaStateAdmin{mockAdminService: &mockAdminService{},
		replicaStatesFn: func(context.Context, uint64) (*service.ReplicaStatesResponse, error) {
			t.Fatal("the service was called with an undecodable payload")
			return nil, nil
		}}
	_, err := newTestServer(&mockKVService{}, admin).handleGetReplicaStates(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *UnmarshalError", err)
	}
}

func TestHandleGetReplicaStates_AdminServiceCannotRead(t *testing.T) {
	_, err := newTestServer(&mockKVService{}, &mockAdminService{}).handleGetReplicaStates(context.Background(), nil)
	var un *service.ReplicaStateReadUnsupportedError
	if !errors.As(err, &un) {
		t.Fatalf("err = %v, want *service.ReplicaStateReadUnsupportedError", err)
	}
}
