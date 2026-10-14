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
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

func TestGRPC_GetReplicaStates_Success(t *testing.T) {
	var gotShard uint64
	admin := &replicaStateAdmin{mockAdminService: newMockAdminService(),
		replicaStatesFn: func(_ context.Context, shardID uint64) (*service.ReplicaStatesResponse, error) {
			gotShard = shardID
			return &service.ReplicaStatesResponse{NodeID: "node-3", Replicas: []service.ReplicaState{
				{ShardID: 7, ReplicaID: 3, AppliedIndex: 48211, Hash: 1<<63 + 5},
			}}, nil
		}}
	client, cleanup := startTestServer(t, newMockKVService(), admin)
	defer cleanup()

	resp, err := client.GetReplicaStates(context.Background(), &pb.GetReplicaStatesRequest{ShardId: 7})
	if err != nil {
		t.Fatalf("GetReplicaStates: %v", err)
	}
	if gotShard != 7 {
		t.Fatalf("shard passed to the service = %d, want 7", gotShard)
	}
	if resp.GetNodeId() != "node-3" || len(resp.GetReplicas()) != 1 {
		t.Fatalf("resp = %v", resp)
	}
	r := resp.GetReplicas()[0]
	if r.GetShardId() != 7 || r.GetReplicaId() != 3 || r.GetAppliedIndex() != 48211 || r.GetHash() != 1<<63+5 {
		t.Fatalf("replica = %v; the full 64-bit hash must survive the wire", r)
	}
}

func TestGRPC_GetReplicaStates_Errors(t *testing.T) {
	cases := map[string]struct {
		err  error
		code codes.Code
	}{
		"not hosted": {&service.ReplicaNotHostedError{ShardID: 9}, codes.NotFound},
		"standalone": {&service.ReplicaStateUnsupportedError{}, codes.Unimplemented},
		"unhashable": {&service.StateMachineHashUnsupportedError{ShardID: 6}, codes.Unimplemented},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			admin := &replicaStateAdmin{mockAdminService: newMockAdminService(),
				replicaStatesFn: func(context.Context, uint64) (*service.ReplicaStatesResponse, error) {
					return nil, tc.err
				}}
			client, cleanup := startTestServer(t, newMockKVService(), admin)
			defer cleanup()
			_, err := client.GetReplicaStates(context.Background(), &pb.GetReplicaStatesRequest{ShardId: 9})
			if st, _ := status.FromError(err); st.Code() != tc.code {
				t.Fatalf("code = %s, want %s (err %v)", st.Code(), tc.code, err)
			}
		})
	}
}

func TestGRPC_GetReplicaStates_AdminServiceCannotRead(t *testing.T) {
	client, cleanup := startTestServer(t, newMockKVService(), newMockAdminService())
	defer cleanup()
	_, err := client.GetReplicaStates(context.Background(), &pb.GetReplicaStatesRequest{})
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("code = %s, want Unimplemented (err %v)", st.Code(), err)
	}
}

func TestGRPC_GetReplicaStates_NoAdminService(t *testing.T) {
	_, err := (&qrdbServiceServer{}).GetReplicaStates(context.Background(), &pb.GetReplicaStatesRequest{})
	if err == nil {
		t.Fatal("expected an error with no admin service configured")
	}
}
