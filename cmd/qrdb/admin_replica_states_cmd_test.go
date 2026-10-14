//go:build !integration

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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// replicaStateAdmin is a topoAdminService that can read replica states, as
// the real AdminService can.
type replicaStateAdmin struct {
	*topoAdminService
	replicaStatesFn func(ctx context.Context, shardID uint64) (*service.ReplicaStatesResponse, error)
}

func (a *replicaStateAdmin) ReplicaStates(ctx context.Context, shardID uint64) (*service.ReplicaStatesResponse, error) {
	return a.replicaStatesFn(ctx, shardID)
}

var _ service.ReplicaStateReader = (*replicaStateAdmin)(nil)

// twoReplicaAdmin answers with two replicas and records the shard it was
// asked for in *got.
func twoReplicaAdmin(got *uint64) *replicaStateAdmin {
	return &replicaStateAdmin{topoAdminService: &topoAdminService{},
		replicaStatesFn: func(_ context.Context, shardID uint64) (*service.ReplicaStatesResponse, error) {
			*got = shardID
			return &service.ReplicaStatesResponse{NodeID: "node-2", Replicas: []service.ReplicaState{
				{ShardID: 1, ReplicaID: 2, AppliedIndex: 48211, Hash: 0xc0ffee},
				{ShardID: 7, ReplicaID: 2, AppliedIndex: 9, Hash: 1<<63 + 5},
			}}, nil
		}}
}

func TestAdminReplicaStates_Table(t *testing.T) {
	gotShard := uint64(99)
	output, err := executeAdminCmd(t, twoReplicaAdmin(&gotShard), "replica-states")
	if err != nil {
		t.Fatalf("admin replica-states: %v", err)
	}
	if gotShard != 0 {
		t.Fatalf("shard passed to the service = %d, want 0 (every replica)", gotShard)
	}
	for _, want := range []string{
		"SHARD", "REPLICA", "APPLIED INDEX", "HASH",
		"48211", "0000000000c0ffee",
		"8000000000000005",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestAdminReplicaStates_ShardIDFlag(t *testing.T) {
	var gotShard uint64
	if _, err := executeAdminCmd(t, twoReplicaAdmin(&gotShard), "replica-states", "--shard-id", "7"); err != nil {
		t.Fatalf("admin replica-states: %v", err)
	}
	if gotShard != 7 {
		t.Fatalf("shard passed to the service = %d, want 7", gotShard)
	}
}

func TestAdminReplicaStates_JSONKeepsFullHash(t *testing.T) {
	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	var gotShard uint64
	output, err := executeAdminCmd(t, twoReplicaAdmin(&gotShard), "replica-states")
	if err != nil {
		t.Fatalf("admin replica-states: %v", err)
	}
	var resp transport.ReplicaStatesResponse
	if jsonErr := json.Unmarshal([]byte(output), &resp); jsonErr != nil {
		t.Fatalf("JSON unmarshal: %v, output: %q", jsonErr, output)
	}
	want := transport.ReplicaStatesResponse{NodeID: "node-2", Replicas: []transport.ReplicaState{
		{ShardID: 1, ReplicaID: 2, AppliedIndex: 48211, Hash: 0xc0ffee},
		{ShardID: 7, ReplicaID: 2, AppliedIndex: 9, Hash: 1<<63 + 5},
	}}
	if !reflect.DeepEqual(resp, want) {
		t.Fatalf("resp = %+v, want %+v", resp, want)
	}
}

func TestAdminReplicaStates_NotHosted(t *testing.T) {
	admin := &replicaStateAdmin{topoAdminService: &topoAdminService{},
		replicaStatesFn: func(context.Context, uint64) (*service.ReplicaStatesResponse, error) {
			return nil, &service.ReplicaNotHostedError{ShardID: 9}
		}}
	_, err := executeAdminCmd(t, admin, "replica-states", "--shard-id", "9")
	var notHosted *service.ReplicaNotHostedError
	if !errors.As(err, &notHosted) {
		t.Fatalf("err = %v (%T), want it to carry *service.ReplicaNotHostedError", err, err)
	}
}

func TestAdminReplicaStates_Unsupported(t *testing.T) {
	_, err := executeAdminCmd(t, &topoAdminService{}, "replica-states")
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v (%T), want *transport.CapabilityUnsupportedError", err, err)
	}
}

func TestAdminReplicaStates_RejectsArgs(t *testing.T) {
	var gotShard uint64
	if _, err := executeAdminCmd(t, twoReplicaAdmin(&gotShard), "replica-states", "1"); err == nil {
		t.Fatal("expected an error for a positional argument")
	}
}
