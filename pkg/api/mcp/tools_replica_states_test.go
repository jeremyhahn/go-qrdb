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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

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
	want := &service.ReplicaStatesResponse{NodeID: "node-1", Replicas: []service.ReplicaState{
		{ShardID: 3, ReplicaID: 2, AppliedIndex: 48211, Hash: 1<<63 + 5},
	}}
	admin := &replicaStateAdmin{mockAdminService: newMockAdminService(),
		replicaStatesFn: func(_ context.Context, shardID uint64) (*service.ReplicaStatesResponse, error) {
			gotShard = shardID
			return want, nil
		}}
	req := buildCallToolRequest(t, "qrdb_replica_states", map[string]any{"shard_id": float64(3)})
	result, err := handleGetReplicaStates(admin)(context.Background(), req)
	if err != nil {
		t.Fatalf("handleGetReplicaStates: %v", err)
	}
	if gotShard != 3 {
		t.Fatalf("shard passed to the service = %d, want 3", gotShard)
	}
	var got service.ReplicaStatesResponse
	if err := json.Unmarshal([]byte(resultText(t, result)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(&got, want) {
		t.Fatalf("result = %+v, want %+v; the full 64-bit hash must survive the text", got, want)
	}
}

func TestHandleGetReplicaStates_NoShardReadsEveryShard(t *testing.T) {
	gotShard := uint64(99)
	admin := &replicaStateAdmin{mockAdminService: newMockAdminService(),
		replicaStatesFn: func(_ context.Context, shardID uint64) (*service.ReplicaStatesResponse, error) {
			gotShard = shardID
			return &service.ReplicaStatesResponse{NodeID: "node-1", Replicas: []service.ReplicaState{}}, nil
		}}
	req := buildCallToolRequest(t, "qrdb_replica_states", map[string]any{})
	if _, err := handleGetReplicaStates(admin)(context.Background(), req); err != nil {
		t.Fatalf("handleGetReplicaStates: %v", err)
	}
	if gotShard != 0 {
		t.Fatalf("shard passed to the service = %d, want 0", gotShard)
	}
}

func TestHandleGetReplicaStates_ServiceError(t *testing.T) {
	admin := &replicaStateAdmin{mockAdminService: newMockAdminService(),
		replicaStatesFn: func(context.Context, uint64) (*service.ReplicaStatesResponse, error) {
			return nil, &service.ReplicaNotHostedError{ShardID: 9}
		}}
	req := buildCallToolRequest(t, "qrdb_replica_states", map[string]any{"shard_id": float64(9)})
	_, err := handleGetReplicaStates(admin)(context.Background(), req)
	var toolErr *ToolError
	if !errors.As(err, &toolErr) || toolErr.Tool != "qrdb_replica_states" {
		t.Fatalf("err = %v, want *ToolError for qrdb_replica_states", err)
	}
	var nh *service.ReplicaNotHostedError
	if !errors.As(err, &nh) {
		t.Fatalf("err = %v does not carry *service.ReplicaNotHostedError", err)
	}
}

func TestHandleGetReplicaStates_AdminServiceCannotRead(t *testing.T) {
	req := buildCallToolRequest(t, "qrdb_replica_states", map[string]any{})
	_, err := handleGetReplicaStates(newMockAdminService())(context.Background(), req)
	var un *service.ReplicaStateReadUnsupportedError
	if !errors.As(err, &un) {
		t.Fatalf("err = %v, want *service.ReplicaStateReadUnsupportedError", err)
	}
}

func TestToolGetReplicaStates_ShardIDOptional(t *testing.T) {
	tool := toolGetReplicaStates()
	if tool.Name != "qrdb_replica_states" {
		t.Fatalf("name = %q", tool.Name)
	}
	if _, ok := tool.InputSchema.Properties["shard_id"]; !ok {
		t.Fatal("schema has no shard_id")
	}
	for _, r := range tool.InputSchema.Required {
		if r == "shard_id" {
			t.Fatal("shard_id is required; omitting it must read every shard")
		}
	}
}
