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

//go:build !integration

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The service doubles the success-path test below uses live in
// service_doubles_test.go, which every build of this package compiles.

// ---------------------------------------------------------------------------
// startDynamicMembership success-path test
// ---------------------------------------------------------------------------

// TestStartDynamicMembership_SuccessPath covers start.go:1953.2,1953.17
// (the "return mgr, nil" success path). All three supplementary services
// (lease, MVCC, watch) are provided as in-memory mocks so embedded.New,
// sdkgo.NewClient, NewMembershipManager, and mgr.Start all succeed, and
// the function returns a non-nil manager without error.
func TestStartDynamicMembership_SuccessPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := make(map[string][]byte)
	kvMock := newMockKVStore(store)
	adminMock := &minimalAdminServicer{}
	leaseMock := &mockLeaseServicer{}
	mvccMock := &mockMVCCKVServicer{mockKVServicer: &mockKVServicer{}}
	watchMock := &mockWatchServicer{}

	f := &startFlags{
		replicaID:       1,
		raftAddress:     "127.0.0.1:63099",
		shardID:         1,
		clusterMinPeers: 3,
	}

	mgr, err := startDynamicMembership(ctx, f, kvMock, adminMock, mvccMock, leaseMock, watchMock)
	require.NoError(t, err, "startDynamicMembership must succeed with valid mock services")
	require.NotNil(t, mgr)

	// Signal shutdown; mgr.Close cancels the run context and waits for
	// background goroutines to drain before deregistering the member lease.
	cancel()
	if closeErr := mgr.Close(); closeErr != nil {
		t.Logf("mgr.Close() = %v (acceptable on test shutdown)", closeErr)
	}
}
