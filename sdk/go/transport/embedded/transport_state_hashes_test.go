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
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// stateHashAdmin is an admin double implementing service.ReplicaStateHasher,
// recording the shard IDs it was asked for.
type stateHashAdmin struct {
	mockAdminService
	gotShards []uint64
	resp      *service.StateHashesResponse
	err       error
}

var _ service.ReplicaStateHasher = (*stateHashAdmin)(nil)

func (a *stateHashAdmin) StateHashes(_ context.Context, shardIDs []uint64) (*service.StateHashesResponse, error) {
	a.gotShards = shardIDs
	return a.resp, a.err
}

func TestEmbedded_StateHashes_SelectsAndConverts(t *testing.T) {
	admin := &stateHashAdmin{resp: &service.StateHashesResponse{
		NodeID: "node-a",
		Replicas: []service.ReplicaStateHash{
			{TenantID: "acme", ShardID: 3, ReplicaID: 2, AppliedIndex: 9, Records: 4, Hash: "ab"},
			{ShardID: 5, ReplicaID: 2, Error: "not started"},
		},
	}}
	tr, err := New(newMockKVService(), admin)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := tr.StateHashes(context.Background(), []uint64{3, 5})
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
}

func TestFromServiceStateHashes_NilIsEmpty(t *testing.T) {
	got := fromServiceStateHashes(nil)
	if got == nil || got.NodeID != "" || got.Replicas == nil || len(got.Replicas) != 0 {
		t.Fatalf("fromServiceStateHashes(nil) = %+v, want an empty non-nil response", got)
	}
}

// An in-process caller scoped to a tenant is refused by the real admin
// service, exactly as a remote tenant caller is refused by the RBAC entry
// check, and the refusal keeps its type through the transport.
func TestEmbedded_StateHashes_RefusesATenantContext(t *testing.T) {
	tr, err := New(newMockKVService(), service.NewAdminService(nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var denied *tenant.CrossTenantDeniedError
	if _, err := tr.StateHashes(tenant.WithTenantID(context.Background(), "acme"), nil); !errors.As(err, &denied) {
		t.Fatalf("StateHashes as acme = %v; want *tenant.CrossTenantDeniedError", err)
	}
}

func TestEmbedded_StateHashes_Failures(t *testing.T) {
	ctx := context.Background()
	plain, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var unsupported *transport.CapabilityUnsupportedError
	if _, err := plain.StateHashes(ctx, nil); !errors.As(err, &unsupported) {
		t.Fatalf("admin without the capability = %v, want *transport.CapabilityUnsupportedError", err)
	}

	tr, err := New(newMockKVService(), &stateHashAdmin{err: &service.StateDigestUnsupportedError{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tr.StateHashes(ctx, nil); err == nil {
		t.Fatal("a failing StateHashes succeeded")
	}
	_ = tr.Close()
	var closed *transport.ClosedError
	if _, err := tr.StateHashes(ctx, nil); !errors.As(err, &closed) {
		t.Fatalf("StateHashes on a closed transport = %v, want *transport.ClosedError", err)
	}
}
