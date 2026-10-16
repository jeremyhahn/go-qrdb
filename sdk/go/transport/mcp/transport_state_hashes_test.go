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
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// stateHashAdmin is an admin double implementing service.ReplicaStateHasher,
// recording the shard IDs it was asked for.
type stateHashAdmin struct {
	*mockAdminService
	gotShards []uint64
	resp      *service.StateHashesResponse
	err       error
}

var _ service.ReplicaStateHasher = (*stateHashAdmin)(nil)

func (a *stateHashAdmin) StateHashes(_ context.Context, shardIDs []uint64) (*service.StateHashesResponse, error) {
	a.gotShards = shardIDs
	return a.resp, a.err
}

func TestStateHashes_SelectsAndConverts(t *testing.T) {
	admin := &stateHashAdmin{mockAdminService: newMockAdminService(), resp: &service.StateHashesResponse{
		NodeID: "node-a",
		Replicas: []service.ReplicaStateHash{
			{TenantID: "acme", ShardID: 3, ReplicaID: 2, AppliedIndex: 9, Records: 4, Hash: "ab"},
			{ShardID: 5, ReplicaID: 2, Error: "not started"},
		},
	}}
	tr, cleanup := connectInProcess(t, newMockKVService(), admin)
	defer cleanup()
	ctx := context.Background()

	got, err := tr.StateHashes(ctx, []uint64{3, 5})
	if err != nil {
		t.Fatalf("StateHashes: %v", err)
	}
	if !slices.Equal(admin.gotShards, []uint64{3, 5}) {
		t.Fatalf("service asked for %v, want [3 5]", admin.gotShards)
	}
	want := []transport.ReplicaStateHash{
		{TenantID: "acme", ShardID: 3, ReplicaID: 2, AppliedIndex: 9, Records: 4, Hash: "ab"},
		{ShardID: 5, ReplicaID: 2, Error: "not started"},
	}
	if got.NodeID != "node-a" || !slices.Equal(got.Replicas, want) {
		t.Fatalf("StateHashes = %+v, want node-a %+v", got, want)
	}

	if _, err := tr.StateHashes(ctx, nil); err != nil || len(admin.gotShards) != 0 {
		t.Fatalf("StateHashes(nil) = %v (asked %v); want every shard", err, admin.gotShards)
	}
}

func TestStateHashes_Failures(t *testing.T) {
	ctx := context.Background()
	plain, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()
	if _, err := plain.StateHashes(ctx, nil); err == nil {
		t.Fatal("StateHashes against an admin without the capability succeeded")
	}

	denied, cleanup2 := connectInProcess(t, newMockKVService(), &stateHashAdmin{mockAdminService: newMockAdminService(), err: &tenant.CrossTenantDeniedError{}})
	defer cleanup2()
	if _, err := denied.StateHashes(ctx, nil); err == nil {
		t.Fatal("a refused StateHashes succeeded")
	}

	_ = denied.Close()
	var closed *transport.ClosedError
	if _, err := denied.StateHashes(ctx, nil); !errors.As(err, &closed) {
		t.Fatalf("StateHashes on a closed transport = %v, want *transport.ClosedError", err)
	}
}
