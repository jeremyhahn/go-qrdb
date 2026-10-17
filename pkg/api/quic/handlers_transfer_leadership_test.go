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
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// leaderTransferAdmin is a mockAdminService that also implements
// service.LeaderTransferrer.
type leaderTransferAdmin struct {
	*mockAdminService
	transferFn func(ctx context.Context, shardID, replicaID uint64) error
}

// TransferLeadership implements service.LeaderTransferrer.
func (m *leaderTransferAdmin) TransferLeadership(ctx context.Context, shardID, replicaID uint64) error {
	return m.transferFn(ctx, shardID, replicaID)
}

var _ service.LeaderTransferrer = (*leaderTransferAdmin)(nil)

func transferLeadershipPayload(t *testing.T, shardID, replicaID uint64) []byte {
	t.Helper()
	payload, err := proto.Marshal(&pb.TransferLeadershipRequest{ShardId: shardID, ReplicaId: replicaID})
	if err != nil {
		t.Fatalf("marshal TransferLeadershipRequest: %v", err)
	}
	return payload
}

func TestHandleTransferLeadership_Success(t *testing.T) {
	var gotShardID, gotReplicaID uint64
	adminSvc := &leaderTransferAdmin{
		mockAdminService: &mockAdminService{},
		transferFn: func(_ context.Context, shardID, replicaID uint64) error {
			gotShardID, gotReplicaID = shardID, replicaID
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, adminSvc)

	resp, err := srv.handleTransferLeadership(context.Background(), transferLeadershipPayload(t, 2, 3))
	if err != nil {
		t.Fatalf("handleTransferLeadership: %v", err)
	}
	if gotShardID != 2 || gotReplicaID != 3 {
		t.Fatalf("transfer(shard=%d, replica=%d), want (2, 3)", gotShardID, gotReplicaID)
	}
	if err := proto.Unmarshal(resp, &emptypb.Empty{}); err != nil {
		t.Fatalf("unmarshal Empty: %v", err)
	}
}

func TestHandleTransferLeadership_TimeoutFrameCarriesTheRefusal(t *testing.T) {
	want := &service.LeaderTransferTimeoutError{ShardID: 2, FromReplicaID: 1, ToReplicaID: 3, Timeout: 5 * time.Second}
	adminSvc := &leaderTransferAdmin{
		mockAdminService: &mockAdminService{},
		transferFn:       func(context.Context, uint64, uint64) error { return want },
	}
	srv := newTestServer(&mockKVService{}, adminSvc)

	_, err := srv.handleTransferLeadership(context.Background(), transferLeadershipPayload(t, 2, 3))
	var timeoutErr *service.LeaderTransferTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v (%T), want *LeaderTransferTimeoutError", err, err)
	}

	info := errorPayloadOf(err).Info
	if info == nil || info.Reason != replicaforward.ReasonLeaderTransferTimeout {
		t.Fatalf("frame info = %+v, want reason %s", info, replicaforward.ReasonLeaderTransferTimeout)
	}
	got, err := replicaforward.ParseLeaderTransferTimeout(info.Metadata)
	if err != nil {
		t.Fatalf("ParseLeaderTransferTimeout: %v", err)
	}
	if *got != *want {
		t.Fatalf("rebuilt %+v, want %+v", *got, *want)
	}
}

func TestHandleTransferLeadership_Unsupported(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	_, err := srv.handleTransferLeadership(context.Background(), transferLeadershipPayload(t, 2, 3))
	var unsupported *service.LeaderTransferUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v (%T), want *LeaderTransferUnsupportedError", err, err)
	}
}

func TestHandleTransferLeadership_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	_, err := srv.handleTransferLeadership(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v (%T), want *UnmarshalError", err, err)
	}
}
