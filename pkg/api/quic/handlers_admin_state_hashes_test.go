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
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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

func TestHandleGetStateHashes_Success(t *testing.T) {
	admin := &stateHashAdmin{mockAdminService: &mockAdminService{}, resp: &service.StateHashesResponse{
		NodeID:   "node-a",
		Replicas: []service.ReplicaStateHash{{TenantID: "acme", ShardID: 3, ReplicaID: 1, AppliedIndex: 5, Records: 2, Hash: "cd"}},
	}}
	srv := newTestServer(&mockKVService{}, admin)
	payload, err := proto.Marshal(&pb.GetStateHashesRequest{ShardIds: []uint64{3}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	out, err := srv.handleGetStateHashes(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleGetStateHashes: %v", err)
	}
	if !slices.Equal(admin.gotShards, []uint64{3}) {
		t.Fatalf("service asked for %v, want [3]", admin.gotShards)
	}
	var resp pb.GetStateHashesResponse
	if err := proto.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.GetNodeId() != "node-a" || len(resp.GetReplicas()) != 1 || resp.GetReplicas()[0].GetTenantId() != "acme" {
		t.Fatalf("response = %v", &resp)
	}
}

func TestHandleGetStateHashes_ServiceError(t *testing.T) {
	admin := &stateHashAdmin{mockAdminService: &mockAdminService{}, err: &tenant.CrossTenantDeniedError{}}
	srv := newTestServer(&mockKVService{}, admin)

	_, err := srv.handleGetStateHashes(context.Background(), nil)
	var denied *tenant.CrossTenantDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v, want *tenant.CrossTenantDeniedError", err)
	}
}

func TestHandleGetStateHashes_UnsupportedAndUnmarshal(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleGetStateHashes(context.Background(), nil)
	var unsupported *service.ReplicaStateHashesUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("admin without the capability: err = %v", err)
	}

	_, err = srv.handleGetStateHashes(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("bad payload: err = %v, want *UnmarshalError", err)
	}
}
