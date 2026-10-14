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

package embedded

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

// recordingReplicaAdmin answers with one replica of shardID and records the
// shard it was asked for in *got.
func recordingReplicaAdmin(got *uint64) *replicaStateAdmin {
	return &replicaStateAdmin{mockAdminService: &mockAdminService{},
		replicaStatesFn: func(_ context.Context, shardID uint64) (*service.ReplicaStatesResponse, error) {
			*got = shardID
			return &service.ReplicaStatesResponse{NodeID: "node-1", Replicas: []service.ReplicaState{
				{ShardID: 4, ReplicaID: 1, AppliedIndex: 48211, Hash: 1<<63 + 5},
			}}, nil
		}}
}

func wantReplicaStates() *transport.ReplicaStatesResponse {
	return &transport.ReplicaStatesResponse{NodeID: "node-1", Replicas: []transport.ReplicaState{
		{ShardID: 4, ReplicaID: 1, AppliedIndex: 48211, Hash: 1<<63 + 5},
	}}
}

func TestReplicaStates_Success(t *testing.T) {
	var gotShard uint64
	tr, _ := New(newMockKVService(), recordingReplicaAdmin(&gotShard))
	resp, err := tr.ReplicaStates(context.Background(), 4)
	if err != nil {
		t.Fatalf("ReplicaStates: %v", err)
	}
	if gotShard != 4 {
		t.Fatalf("shard passed to the service = %d, want 4", gotShard)
	}
	if want := wantReplicaStates(); !reflect.DeepEqual(resp, want) {
		t.Fatalf("ReplicaStates = %+v, want %+v", resp, want)
	}
}

func TestReplicaStates_NoReplicasIsEmptyNotNil(t *testing.T) {
	admin := &replicaStateAdmin{mockAdminService: &mockAdminService{},
		replicaStatesFn: func(context.Context, uint64) (*service.ReplicaStatesResponse, error) {
			return &service.ReplicaStatesResponse{NodeID: "node-1"}, nil
		}}
	tr, _ := New(newMockKVService(), admin)
	resp, err := tr.ReplicaStates(context.Background(), 0)
	if err != nil {
		t.Fatalf("ReplicaStates: %v", err)
	}
	if resp.Replicas == nil || len(resp.Replicas) != 0 {
		t.Fatalf("Replicas = %#v, want empty and non-nil", resp.Replicas)
	}
}

func TestReplicaStates_UnsupportedIsCapabilityUnsupported(t *testing.T) {
	refusals := map[string]error{
		"standalone": &service.ReplicaStateUnsupportedError{},
		"unhashable": &service.StateMachineHashUnsupportedError{ShardID: 6},
	}
	for name, refusal := range refusals {
		t.Run(name, func(t *testing.T) {
			admin := &replicaStateAdmin{mockAdminService: &mockAdminService{},
				replicaStatesFn: func(context.Context, uint64) (*service.ReplicaStatesResponse, error) {
					return nil, refusal
				}}
			tr, _ := New(newMockKVService(), admin)
			_, err := tr.ReplicaStates(context.Background(), 0)
			var unsupported *transport.CapabilityUnsupportedError
			if !errors.As(err, &unsupported) {
				t.Fatalf("err = %v (%T), want *transport.CapabilityUnsupportedError", err, err)
			}
		})
	}
}

func TestReplicaStates_AdminServiceCannotRead(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_, err := tr.ReplicaStates(context.Background(), 0)
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v (%T), want *transport.CapabilityUnsupportedError", err, err)
	}
}

func TestReplicaStates_NotHostedKeepsServiceError(t *testing.T) {
	admin := &replicaStateAdmin{mockAdminService: &mockAdminService{},
		replicaStatesFn: func(context.Context, uint64) (*service.ReplicaStatesResponse, error) {
			return nil, &service.ReplicaNotHostedError{ShardID: 9}
		}}
	tr, _ := New(newMockKVService(), admin)
	_, err := tr.ReplicaStates(context.Background(), 9)
	var notHosted *service.ReplicaNotHostedError
	if !errors.As(err, &notHosted) || notHosted.ShardID != 9 {
		t.Fatalf("err = %v (%T), want it to carry *service.ReplicaNotHostedError for shard 9", err, err)
	}
}

func TestReplicaStates_AfterClose(t *testing.T) {
	var gotShard uint64
	tr, _ := New(newMockKVService(), recordingReplicaAdmin(&gotShard))
	_ = tr.Close()
	_, err := tr.ReplicaStates(context.Background(), 0)
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("err = %v (%T), want *transport.ClosedError", err, err)
	}
}

func TestDispatch_ReplicaStates(t *testing.T) {
	cases := map[string]struct {
		req       any
		wantShard uint64
	}{
		"every shard": {req: nil, wantShard: 0},
		"one shard":   {req: uint64(4), wantShard: 4},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotShard := uint64(99)
			tr, _ := New(newMockKVService(), recordingReplicaAdmin(&gotShard))
			var resp *transport.ReplicaStatesResponse
			if err := tr.Request(context.Background(), "ReplicaStates", tc.req, &resp); err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			if gotShard != tc.wantShard {
				t.Fatalf("shard passed to the service = %d, want %d", gotShard, tc.wantShard)
			}
			if want := wantReplicaStates(); !reflect.DeepEqual(resp, want) {
				t.Fatalf("resp = %+v, want %+v", resp, want)
			}
		})
	}
}

func TestDispatch_ReplicaStates_InvalidRequest(t *testing.T) {
	var gotShard uint64
	tr, _ := New(newMockKVService(), recordingReplicaAdmin(&gotShard))
	var resp *transport.ReplicaStatesResponse
	err := tr.Request(context.Background(), "ReplicaStates", "shard-4", &resp)
	var invalid *InvalidRequestError
	if !errors.As(err, &invalid) || invalid.Method != "ReplicaStates" {
		t.Fatalf("err = %v (%T), want *InvalidRequestError for ReplicaStates", err, err)
	}
}
