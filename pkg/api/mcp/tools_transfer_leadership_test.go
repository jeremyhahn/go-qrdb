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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
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

func TestHandleTransferLeadership_Success(t *testing.T) {
	var gotShardID, gotReplicaID uint64
	handler := handleTransferLeadership(&leaderTransferAdmin{
		mockAdminService: newMockAdminService(),
		transferFn: func(_ context.Context, shardID, replicaID uint64) error {
			gotShardID, gotReplicaID = shardID, replicaID
			return nil
		},
	})

	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_transfer_leadership", map[string]any{
		"shard_id":   float64(1),
		"replica_id": float64(3),
	}))
	if err != nil {
		t.Fatalf("handleTransferLeadership: %v", err)
	}
	if text := resultText(t, result); text != "ok" {
		t.Errorf("result = %q, want ok", text)
	}
	if gotShardID != 1 || gotReplicaID != 3 {
		t.Fatalf("transfer(shard=%d, replica=%d), want (1, 3)", gotShardID, gotReplicaID)
	}
}

func TestHandleTransferLeadership_MissingArguments(t *testing.T) {
	handler := handleTransferLeadership(&leaderTransferAdmin{
		mockAdminService: newMockAdminService(),
		transferFn: func(context.Context, uint64, uint64) error {
			t.Fatal("an incomplete request reached the service")
			return nil
		},
	})

	for name, args := range map[string]map[string]any{
		"shard_id":   {"replica_id": float64(3)},
		"replica_id": {"shard_id": float64(1)},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_transfer_leadership", args))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.IsError {
				t.Fatalf("expected an error result for a missing %s", name)
			}
		})
	}
}

func TestHandleTransferLeadership_Unsupported(t *testing.T) {
	handler := handleTransferLeadership(newMockAdminService())

	_, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_transfer_leadership", map[string]any{
		"shard_id":   float64(1),
		"replica_id": float64(3),
	}))
	var unsupported *service.LeaderTransferUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v (%T), want *LeaderTransferUnsupportedError", err, err)
	}
}

func TestHandleTransferLeadership_TimeoutEnvelopeCarriesTheRefusal(t *testing.T) {
	want := &service.LeaderTransferTimeoutError{ShardID: 1, FromReplicaID: 1, ToReplicaID: 3, Timeout: 5 * time.Second}
	handler := handleTransferLeadership(&leaderTransferAdmin{
		mockAdminService: newMockAdminService(),
		transferFn:       func(context.Context, uint64, uint64) error { return want },
	})

	_, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_transfer_leadership", map[string]any{
		"shard_id":   float64(1),
		"replica_id": float64(3),
	}))
	if err == nil {
		t.Fatal("expected the timeout")
	}

	env, ok := classifiedToolError("qrdb_transfer_leadership", err).StructuredContent.(*wire.ErrorEnvelope)
	if !ok {
		t.Fatal("tool error carries no envelope")
	}
	if env.Error.Code != wire.CodeUnavailable {
		t.Errorf("code = %v, want Unavailable", env.Error.Code)
	}
	if env.Error.Reason != replicaforward.ReasonLeaderTransferTimeout {
		t.Fatalf("reason = %q, want %s", env.Error.Reason, replicaforward.ReasonLeaderTransferTimeout)
	}
	got, err := replicaforward.ParseLeaderTransferTimeout(env.Error.Metadata)
	if err != nil {
		t.Fatalf("ParseLeaderTransferTimeout: %v", err)
	}
	if *got != *want {
		t.Fatalf("rebuilt %+v, want %+v", *got, *want)
	}
}
