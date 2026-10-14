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

package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
)

// The Raft host serves the read; the standalone backend must not, or a
// standalone node would answer with hashes it never computed.
var _ StateMachineHasher = (*raft.HostAdapter)(nil)

// hashingHost is a mockHost that can hash its state machines.
type hashingHost struct {
	*mockHost
	hashFn func(shardID uint64) (hash, appliedIndex uint64, ok bool, err error)
}

func (h *hashingHost) StateMachineHash(shardID uint64) (uint64, uint64, bool, error) {
	return h.hashFn(shardID)
}

var _ StateMachineHasher = (*hashingHost)(nil)

// replicaReader is the AdminService over host, as the transports reach it.
func replicaReader(host Host) ReplicaStateReader {
	return NewAdminService(host).(ReplicaStateReader)
}

// replicaHost lists shards and hashes each from states, keyed by shard.
func replicaHost(shards []quicraft.ShardInfo, states map[uint64]ReplicaState, errs map[uint64]error) *hashingHost {
	return &hashingHost{
		mockHost: &mockHost{
			idFn: func() string { return "node-2" },
			getNodeHostInfoFn: func(quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
				return &quicraft.NodeHostInfo{ShardInfoList: shards}
			},
		},
		hashFn: func(shardID uint64) (uint64, uint64, bool, error) {
			if err := errs[shardID]; err != nil {
				return 0, 0, false, err
			}
			st, ok := states[shardID]
			if !ok {
				return 0, 0, false, nil
			}
			return st.Hash, st.AppliedIndex, true, nil
		},
	}
}

func TestAdminService_ReplicaStates_AllReplicasOrderedByShard(t *testing.T) {
	host := replicaHost(
		[]quicraft.ShardInfo{{ShardID: 7, ReplicaID: 3}, {ShardID: 1, ReplicaID: 2}},
		map[uint64]ReplicaState{1: {AppliedIndex: 48211, Hash: 11}, 7: {AppliedIndex: 9, Hash: 22}},
		nil,
	)
	got, err := replicaReader(host).ReplicaStates(context.Background(), 0)
	if err != nil {
		t.Fatalf("ReplicaStates: %v", err)
	}
	want := &ReplicaStatesResponse{NodeID: "node-2", Replicas: []ReplicaState{
		{ShardID: 1, ReplicaID: 2, AppliedIndex: 48211, Hash: 11},
		{ShardID: 7, ReplicaID: 3, AppliedIndex: 9, Hash: 22},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReplicaStates = %+v, want %+v", got, want)
	}
}

func TestAdminService_ReplicaStates_FilterReturnsOneShard(t *testing.T) {
	host := replicaHost(
		[]quicraft.ShardInfo{{ShardID: 1, ReplicaID: 2}, {ShardID: 7, ReplicaID: 3}},
		map[uint64]ReplicaState{1: {AppliedIndex: 5, Hash: 11}, 7: {AppliedIndex: 9, Hash: 22}},
		nil,
	)
	got, err := replicaReader(host).ReplicaStates(context.Background(), 7)
	if err != nil {
		t.Fatalf("ReplicaStates: %v", err)
	}
	if want := []ReplicaState{{ShardID: 7, ReplicaID: 3, AppliedIndex: 9, Hash: 22}}; !reflect.DeepEqual(got.Replicas, want) {
		t.Fatalf("Replicas = %+v, want %+v", got.Replicas, want)
	}
}

func TestAdminService_ReplicaStates_EmptyNodeListsNothing(t *testing.T) {
	got, err := replicaReader(replicaHost(nil, nil, nil)).ReplicaStates(context.Background(), 0)
	if err != nil {
		t.Fatalf("ReplicaStates: %v", err)
	}
	if got.Replicas == nil || len(got.Replicas) != 0 {
		t.Fatalf("Replicas = %#v, want empty and non-nil", got.Replicas)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"node_id":"node-2","replicas":[]}`; string(data) != want {
		t.Fatalf("JSON = %s, want %s", data, want)
	}
}

func TestAdminService_ReplicaStates_WitnessNeverAsked(t *testing.T) {
	host := replicaHost(
		[]quicraft.ShardInfo{{ShardID: 1, ReplicaID: 2}, {ShardID: 4, ReplicaID: 5, IsWitness: true}},
		map[uint64]ReplicaState{1: {AppliedIndex: 5, Hash: 11}},
		map[uint64]error{4: errors.New("a witness was asked for its hash")},
	)
	got, err := replicaReader(host).ReplicaStates(context.Background(), 0)
	if err != nil {
		t.Fatalf("ReplicaStates: %v", err)
	}
	if len(got.Replicas) != 1 || got.Replicas[0].ShardID != 1 {
		t.Fatalf("Replicas = %+v, want shard 1 only", got.Replicas)
	}
}

func TestAdminService_ReplicaStates_WitnessFilteredIsNotHosted(t *testing.T) {
	host := replicaHost([]quicraft.ShardInfo{{ShardID: 4, ReplicaID: 5, IsWitness: true}}, nil, nil)
	_, err := replicaReader(host).ReplicaStates(context.Background(), 4)
	var nh *ReplicaNotHostedError
	if !errors.As(err, &nh) || nh.ShardID != 4 {
		t.Fatalf("err = %v, want *ReplicaNotHostedError for shard 4", err)
	}
}

func TestAdminService_ReplicaStates_UnknownShardIsNotHosted(t *testing.T) {
	host := replicaHost([]quicraft.ShardInfo{{ShardID: 1, ReplicaID: 2}}, map[uint64]ReplicaState{1: {}}, nil)
	_, err := replicaReader(host).ReplicaStates(context.Background(), 9)
	var nh *ReplicaNotHostedError
	if !errors.As(err, &nh) || nh.ShardID != 9 {
		t.Fatalf("err = %v, want *ReplicaNotHostedError for shard 9", err)
	}
	if !strings.Contains(err.Error(), "shard 9 is not hosted") {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestAdminService_ReplicaStates_StoppedShardLeftOutUnfiltered(t *testing.T) {
	host := replicaHost(
		[]quicraft.ShardInfo{{ShardID: 1, ReplicaID: 2}, {ShardID: 3, ReplicaID: 1}},
		map[uint64]ReplicaState{1: {AppliedIndex: 5, Hash: 11}},
		map[uint64]error{3: quicraft.ErrShardNotFound},
	)
	got, err := replicaReader(host).ReplicaStates(context.Background(), 0)
	if err != nil {
		t.Fatalf("ReplicaStates: %v", err)
	}
	if len(got.Replicas) != 1 || got.Replicas[0].ShardID != 1 {
		t.Fatalf("Replicas = %+v, want shard 1 only", got.Replicas)
	}
}

func TestAdminService_ReplicaStates_StoppedShardFilteredIsNotHosted(t *testing.T) {
	host := replicaHost([]quicraft.ShardInfo{{ShardID: 3, ReplicaID: 1}}, nil,
		map[uint64]error{3: quicraft.ErrShardNotFound})
	_, err := replicaReader(host).ReplicaStates(context.Background(), 3)
	var nh *ReplicaNotHostedError
	if !errors.As(err, &nh) || nh.ShardID != 3 {
		t.Fatalf("err = %v, want *ReplicaNotHostedError for shard 3", err)
	}
}

func TestAdminService_ReplicaStates_UnhashableStateMachineRefuses(t *testing.T) {
	host := replicaHost([]quicraft.ShardInfo{{ShardID: 1, ReplicaID: 2}, {ShardID: 6, ReplicaID: 1}},
		map[uint64]ReplicaState{1: {AppliedIndex: 5, Hash: 11}}, nil)
	got, err := replicaReader(host).ReplicaStates(context.Background(), 0)
	var uh *StateMachineHashUnsupportedError
	if !errors.As(err, &uh) || uh.ShardID != 6 {
		t.Fatalf("err = %v, want *StateMachineHashUnsupportedError for shard 6", err)
	}
	if got != nil {
		t.Fatalf("response = %+v, want none: a partial answer would hide the unhashable shard", got)
	}
	if !strings.Contains(err.Error(), "shard 6 does not support hashing") {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestAdminService_ReplicaStates_HostErrorWrapped(t *testing.T) {
	cause := quicraft.ErrClosed
	host := replicaHost([]quicraft.ShardInfo{{ShardID: 2, ReplicaID: 1}}, nil, map[uint64]error{2: cause})
	_, err := replicaReader(host).ReplicaStates(context.Background(), 0)
	var rs *ReplicaStateError
	if !errors.As(err, &rs) || rs.ShardID != 2 {
		t.Fatalf("err = %v, want *ReplicaStateError for shard 2", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v does not wrap %v", err, cause)
	}
	if !strings.Contains(err.Error(), "shard 2") || !strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestAdminService_ReplicaStates_StandaloneRefuses(t *testing.T) {
	_, err := replicaReader(&mockHost{}).ReplicaStates(context.Background(), 0)
	var un *ReplicaStateUnsupportedError
	if !errors.As(err, &un) {
		t.Fatalf("err = %v, want *ReplicaStateUnsupportedError", err)
	}
	if !strings.Contains(err.Error(), "requires mode: raft") {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestAdminService_ReplicaStates_LocalBackendCannotHash(t *testing.T) {
	var h any = (*backend.LocalBackend)(nil)
	if _, ok := h.(StateMachineHasher); ok {
		t.Fatal("the standalone backend satisfies StateMachineHasher; a standalone node would report hashes")
	}
}

func TestAdminService_ReplicaStates_CancelledContext(t *testing.T) {
	host := replicaHost([]quicraft.ShardInfo{{ShardID: 1, ReplicaID: 2}}, map[uint64]ReplicaState{1: {}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := replicaReader(host).ReplicaStates(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestReplicaStateReadUnsupportedError_Message(t *testing.T) {
	if got := (&ReplicaStateReadUnsupportedError{}).Error(); !strings.Contains(got, "replica states") {
		t.Fatalf("message = %q", got)
	}
	var err error = &ReplicaStateReadUnsupportedError{}
	var target *ReplicaStateReadUnsupportedError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed")
	}
}
