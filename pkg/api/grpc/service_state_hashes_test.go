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
	"slices"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

func (a *stateHashAdmin) StateHashes(_ context.Context, shardIDs []uint64) (*service.StateHashesResponse, error) {
	a.gotShards = shardIDs
	return a.resp, a.err
}

func TestGRPC_GetStateHashes_ConvertsTheResponse(t *testing.T) {
	admin := &stateHashAdmin{mockAdminService: newMockAdminService(), resp: &service.StateHashesResponse{
		NodeID: "node-a",
		Replicas: []service.ReplicaStateHash{
			{ShardID: 1, ReplicaID: 2, AppliedIndex: 9, Records: 3, Hash: "ab"},
		},
	}}
	s := &qrdbServiceServer{adminSvc: admin}

	resp, err := s.GetStateHashes(context.Background(), &pb.GetStateHashesRequest{ShardIds: []uint64{1, 7}})
	if err != nil {
		t.Fatalf("GetStateHashes: %v", err)
	}
	if !slices.Equal(admin.gotShards, []uint64{1, 7}) {
		t.Fatalf("service asked for %v, want [1 7]", admin.gotShards)
	}
	if resp.GetNodeId() != "node-a" || len(resp.GetReplicas()) != 1 || resp.GetReplicas()[0].GetAppliedIndex() != 9 {
		t.Fatalf("response = %v", resp)
	}
}

func TestGRPC_GetStateHashes_ServiceErrorMapped(t *testing.T) {
	admin := &stateHashAdmin{mockAdminService: newMockAdminService(), err: &tenant.CrossTenantDeniedError{}}
	s := &qrdbServiceServer{adminSvc: admin}

	_, err := s.GetStateHashes(context.Background(), &pb.GetStateHashesRequest{})
	if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
}

func TestGRPC_GetStateHashes_UnsupportedAndUnconfigured(t *testing.T) {
	s := &qrdbServiceServer{adminSvc: newMockAdminService()}
	_, err := s.GetStateHashes(context.Background(), &pb.GetStateHashesRequest{})
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("admin without the capability: err = %v, want Unimplemented", err)
	}

	s = &qrdbServiceServer{}
	if _, err := s.GetStateHashes(context.Background(), &pb.GetStateHashesRequest{}); err == nil {
		t.Fatal("no admin service: want an error")
	}
}
