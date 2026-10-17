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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// leaderTransferAdminService is an fnAdminService that also implements
// service.LeaderTransferrer, so the embedded transport offers the capability.
type leaderTransferAdminService struct {
	*fnAdminService
	transferFn func(ctx context.Context, shardID, replicaID uint64) error
}

// TransferLeadership implements service.LeaderTransferrer.
func (m *leaderTransferAdminService) TransferLeadership(ctx context.Context, shardID, replicaID uint64) error {
	return m.transferFn(ctx, shardID, replicaID)
}

var _ service.LeaderTransferrer = (*leaderTransferAdminService)(nil)

func TestAdminMemberTransferLeadership_Success(t *testing.T) {
	var gotShardID, gotReplicaID uint64
	adminSvc := &leaderTransferAdminService{
		fnAdminService: &fnAdminService{},
		transferFn: func(_ context.Context, shardID, replicaID uint64) error {
			gotShardID, gotReplicaID = shardID, replicaID
			return nil
		},
	}

	output, err := executeAdminCmd(t, adminSvc,
		"member", "transfer-leadership", "3",
		"--shard-id", "1",
	)
	if err != nil {
		t.Fatalf("admin member transfer-leadership error: %v", err)
	}
	if !strings.Contains(output, "OK") {
		t.Errorf("output missing 'OK', got: %q", output)
	}
	if gotShardID != 1 || gotReplicaID != 3 {
		t.Errorf("transfer(shard=%d, replica=%d), want (1, 3)", gotShardID, gotReplicaID)
	}
}

func TestAdminMemberTransferLeadership_TimeoutIsTyped(t *testing.T) {
	want := &service.LeaderTransferTimeoutError{ShardID: 1, FromReplicaID: 1, ToReplicaID: 3, Timeout: 5 * time.Second}
	adminSvc := &leaderTransferAdminService{
		fnAdminService: &fnAdminService{},
		transferFn: func(context.Context, uint64, uint64) error {
			return want
		},
	}

	_, err := executeAdminCmd(t, adminSvc,
		"member", "transfer-leadership", "3",
		"--shard-id", "1",
	)
	var timeoutErr *sdk.LeaderTransferTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v (%T), want *LeaderTransferTimeoutError", err, err)
	}
	if *timeoutErr != *want {
		t.Errorf("timeout = %+v, want %+v", *timeoutErr, *want)
	}
}

func TestAdminMemberTransferLeadership_Unsupported(t *testing.T) {
	_, err := executeAdminCmd(t, &fnAdminService{},
		"member", "transfer-leadership", "3",
		"--shard-id", "1",
	)
	if err == nil {
		t.Fatal("expected an error from an admin service without the capability")
	}
}

func TestAdminMemberTransferLeadership_InvalidReplicaID(t *testing.T) {
	_, err := executeAdminCmd(t, &fnAdminService{},
		"member", "transfer-leadership", "not-a-number",
		"--shard-id", "1",
	)
	var invalid *InvalidReplicaIDError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v (%T), want *InvalidReplicaIDError", err, err)
	}
}

func TestAdminMemberTransferLeadership_MissingShardID(t *testing.T) {
	_, err := executeAdminCmd(t, &fnAdminService{},
		"member", "transfer-leadership", "3",
	)
	if err == nil || !strings.Contains(err.Error(), "shard-id") {
		t.Fatalf("err = %v, want one naming shard-id", err)
	}
}
