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
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
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

func TestReplicaStates(t *testing.T) {
	var gotShard uint64
	admin := &replicaStateAdmin{mockAdminService: newMockAdminService(),
		replicaStatesFn: func(_ context.Context, shardID uint64) (*service.ReplicaStatesResponse, error) {
			gotShard = shardID
			return &service.ReplicaStatesResponse{NodeID: "node-3", Replicas: []service.ReplicaState{
				{ShardID: 7, ReplicaID: 3, AppliedIndex: 48211, Hash: 1<<63 + 5},
			}}, nil
		}}
	tr, cleanup := connectInProcess(t, newMockKVService(), admin)
	defer cleanup()

	resp, err := tr.ReplicaStates(context.Background(), 7)
	if err != nil {
		t.Fatalf("ReplicaStates: %v", err)
	}
	if gotShard != 7 {
		t.Fatalf("shard passed to the service = %d, want 7", gotShard)
	}
	want := &transport.ReplicaStatesResponse{NodeID: "node-3", Replicas: []transport.ReplicaState{
		{ShardID: 7, ReplicaID: 3, AppliedIndex: 48211, Hash: 1<<63 + 5},
	}}
	if !reflect.DeepEqual(resp, want) {
		t.Fatalf("ReplicaStates = %+v, want %+v; the full 64-bit hash must survive the tool text", resp, want)
	}
}

func TestReplicaStates_NoShardReadsEveryShard(t *testing.T) {
	gotShard := uint64(99)
	admin := &replicaStateAdmin{mockAdminService: newMockAdminService(),
		replicaStatesFn: func(_ context.Context, shardID uint64) (*service.ReplicaStatesResponse, error) {
			gotShard = shardID
			return &service.ReplicaStatesResponse{NodeID: "node-3"}, nil
		}}
	tr, cleanup := connectInProcess(t, newMockKVService(), admin)
	defer cleanup()

	resp, err := tr.ReplicaStates(context.Background(), 0)
	if err != nil {
		t.Fatalf("ReplicaStates: %v", err)
	}
	if gotShard != 0 {
		t.Fatalf("shard passed to the service = %d, want 0", gotShard)
	}
	if resp.Replicas == nil || len(resp.Replicas) != 0 {
		t.Fatalf("Replicas = %#v, want empty and non-nil", resp.Replicas)
	}
}

func TestReplicaStates_ServiceError(t *testing.T) {
	admin := &replicaStateAdmin{mockAdminService: newMockAdminService(),
		replicaStatesFn: func(context.Context, uint64) (*service.ReplicaStatesResponse, error) {
			return nil, &service.ReplicaNotHostedError{ShardID: 9}
		}}
	tr, cleanup := connectInProcess(t, newMockKVService(), admin)
	defer cleanup()

	if _, err := tr.ReplicaStates(context.Background(), 9); err == nil {
		t.Fatal("expected an error for a shard with no replica on the node")
	}
}

func TestReplicaStates_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	_, err := tr.ReplicaStates(context.Background(), 0)
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("err = %v (%T), want *transport.ClosedError", err, err)
	}
}
