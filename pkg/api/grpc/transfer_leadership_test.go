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
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// leaderTransferAdmin is a mockAdminService that also implements
// service.LeaderTransferrer.
type leaderTransferAdmin struct {
	*mockAdminService
	gotShardID, gotReplicaID uint64
	transferErr              error
}

// TransferLeadership implements service.LeaderTransferrer.
func (m *leaderTransferAdmin) TransferLeadership(_ context.Context, shardID, replicaID uint64) error {
	m.gotShardID, m.gotReplicaID = shardID, replicaID
	return m.transferErr
}

var _ service.LeaderTransferrer = (*leaderTransferAdmin)(nil)

func TestGRPC_TransferLeadership(t *testing.T) {
	adminSvc := &leaderTransferAdmin{mockAdminService: newMockAdminService()}
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	if _, err := client.TransferLeadership(context.Background(), &pb.TransferLeadershipRequest{
		ShardId:   1,
		ReplicaId: 3,
	}); err != nil {
		t.Fatalf("TransferLeadership failed: %v", err)
	}
	if adminSvc.gotShardID != 1 || adminSvc.gotReplicaID != 3 {
		t.Fatalf("transfer(shard=%d, replica=%d), want (1, 3)", adminSvc.gotShardID, adminSvc.gotReplicaID)
	}
}

func TestGRPC_TransferLeadership_TimeoutCarriesTheRefusal(t *testing.T) {
	adminSvc := &leaderTransferAdmin{
		mockAdminService: newMockAdminService(),
		transferErr: &service.LeaderTransferTimeoutError{
			ShardID: 1, FromReplicaID: 1, ToReplicaID: 3, Timeout: 5 * time.Second,
		},
	}
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := client.TransferLeadership(context.Background(), &pb.TransferLeadershipRequest{ShardId: 1, ReplicaId: 3})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Unavailable {
		t.Fatalf("code = %s, want Unavailable", st.Code())
	}

	var info *errdetails.ErrorInfo
	for _, d := range st.Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			info = ei
		}
	}
	if info == nil || info.GetReason() != replicaforward.ReasonLeaderTransferTimeout {
		t.Fatalf("ErrorInfo = %v, want reason %s", info, replicaforward.ReasonLeaderTransferTimeout)
	}
	got, err := replicaforward.ParseLeaderTransferTimeout(info.GetMetadata())
	if err != nil {
		t.Fatalf("ParseLeaderTransferTimeout: %v", err)
	}
	if *got != *adminSvc.transferErr.(*service.LeaderTransferTimeoutError) {
		t.Fatalf("rebuilt %+v, want %+v", *got, adminSvc.transferErr)
	}
}

func TestGRPC_TransferLeadership_Unsupported(t *testing.T) {
	client, cleanup := startTestServer(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := client.TransferLeadership(context.Background(), &pb.TransferLeadershipRequest{ShardId: 1, ReplicaId: 3})
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("code = %s, want Unimplemented", st.Code())
	}
}

func TestGRPC_TransferLeadership_TargetRefused(t *testing.T) {
	adminSvc := &leaderTransferAdmin{
		mockAdminService: newMockAdminService(),
		transferErr:      &service.UnknownReplicaError{ShardID: 1, ReplicaID: 9},
	}
	client, cleanup := startTestServer(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := client.TransferLeadership(context.Background(), &pb.TransferLeadershipRequest{ShardId: 1, ReplicaId: 9})
	if st, _ := status.FromError(err); st.Code() != codes.NotFound {
		t.Fatalf("code = %s, want NotFound", st.Code())
	}
}

func TestGRPC_TransferLeadership_ServiceNotConfigured(t *testing.T) {
	srv := &qrdbServiceServer{}
	if _, err := srv.TransferLeadership(context.Background(), &pb.TransferLeadershipRequest{}); err == nil {
		t.Fatal("expected an error without an admin service")
	}
}
