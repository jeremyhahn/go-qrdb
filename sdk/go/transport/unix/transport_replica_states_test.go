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

package unix

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
	tr, cleanup := connectTransport(t, newMockKVService(), admin)
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
		t.Fatalf("ReplicaStates = %+v, want %+v", resp, want)
	}
}

func TestReplicaStates_NotHostedIsKeyNotFound(t *testing.T) {
	admin := &replicaStateAdmin{mockAdminService: newMockAdminService(),
		replicaStatesFn: func(context.Context, uint64) (*service.ReplicaStatesResponse, error) {
			return nil, &service.ReplicaNotHostedError{ShardID: 9}
		}}
	tr, cleanup := connectTransport(t, newMockKVService(), admin)
	defer cleanup()

	_, err := tr.ReplicaStates(context.Background(), 9)
	var notFound *transport.KeyNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %v (%T), want *transport.KeyNotFoundError", err, err)
	}
}

func TestReplicaStates_AdminServiceCannotRead(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := tr.ReplicaStates(context.Background(), 0)
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v (%T), want *transport.CapabilityUnsupportedError", err, err)
	}
}

func TestReplicaStates_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	_, err := tr.ReplicaStates(context.Background(), 0)
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("err = %v (%T), want *transport.ClosedError", err, err)
	}
}
