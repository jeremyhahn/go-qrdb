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
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
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

func TestTransferLeadership(t *testing.T) {
	var gotShardID, gotReplicaID uint64
	adminSvc := &leaderTransferAdmin{
		mockAdminService: newMockAdminService(),
		transferFn: func(_ context.Context, shardID, replicaID uint64) error {
			gotShardID, gotReplicaID = shardID, replicaID
			return nil
		},
	}
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	if err := tr.TransferLeadership(context.Background(), 1, 3); err != nil {
		t.Fatalf("TransferLeadership: %v", err)
	}
	if gotShardID != 1 || gotReplicaID != 3 {
		t.Fatalf("transfer(shard=%d, replica=%d), want (1, 3)", gotShardID, gotReplicaID)
	}
}

func TestTransferLeadership_TimeoutCarriesTheRefusal(t *testing.T) {
	want := &service.LeaderTransferTimeoutError{ShardID: 1, FromReplicaID: 1, ToReplicaID: 3, Timeout: 5 * time.Second}
	adminSvc := &leaderTransferAdmin{
		mockAdminService: newMockAdminService(),
		transferFn:       func(context.Context, uint64, uint64) error { return want },
	}
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.TransferLeadership(context.Background(), 1, 3)
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("err = %v (%T), want *transport.RequestError", err, err)
	}
	if reqErr.Reason != replicaforward.ReasonLeaderTransferTimeout {
		t.Fatalf("reason = %q, want %s", reqErr.Reason, replicaforward.ReasonLeaderTransferTimeout)
	}
	got, parseErr := replicaforward.ParseLeaderTransferTimeout(reqErr.Metadata)
	if parseErr != nil {
		t.Fatalf("ParseLeaderTransferTimeout: %v", parseErr)
	}
	if *got != *want {
		t.Fatalf("rebuilt %+v, want %+v", *got, *want)
	}
}

func TestTransferLeadership_Unsupported(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.TransferLeadership(context.Background(), 1, 3)
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v (%T), want *transport.CapabilityUnsupportedError", err, err)
	}
}

func TestTransferLeadership_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	err := tr.TransferLeadership(context.Background(), 1, 3)
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("err = %v (%T), want *transport.ClosedError", err, err)
	}
}
