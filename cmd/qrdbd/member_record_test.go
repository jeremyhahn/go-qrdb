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
	"time"

	"github.com/stretchr/testify/require"
)

func memberRecordFlags() *startFlags {
	return &startFlags{replicaID: 1, raftAddress: "127.0.0.1:63099", shardID: 1, host: testBindHost, adminPort: 63002}
}

// TestStartNodeAdvertiser_PublishesInTheBackground: the record is published
// once the store accepts it, and Close withdraws it.
func TestStartNodeAdvertiser_PublishesInTheBackground(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := startNodeAdvertiser(ctx, memberRecordFlags(), newMockKVStore(map[string][]byte{}), &minimalAdminServicer{},
		&mockMVCCKVServicer{mockKVServicer: &mockKVServicer{}}, &mockLeaseServicer{}, &mockWatchServicer{})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return p.adv.Load() != nil }, 10*time.Second, 10*time.Millisecond,
		"the member record was never published")
	require.NoError(t, p.Close())
	require.Nil(t, p.adv.Load(), "Close left the record published")
}

// TestStartNodeAdvertiser_AShutdownDuringBootIsNotAFailure: a boot that is
// shut down before the record lands stops publishing and closes cleanly; a
// configuration that cannot be published fails at once.
func TestStartNodeAdvertiser_AShutdownDuringBootIsNotAFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, err := startNodeAdvertiser(ctx, memberRecordFlags(), newMockKVStore(map[string][]byte{}), &minimalAdminServicer{},
		&mockMVCCKVServicer{mockKVServicer: &mockKVServicer{}}, &mockLeaseServicer{}, &mockWatchServicer{})
	require.NoError(t, err)
	require.NoError(t, p.Close(), "closing a publisher whose boot was shut down")
	require.Nil(t, p.adv.Load())

	_, err = startNodeAdvertiser(context.Background(), memberRecordFlags(), nil, nil, nil, nil, nil)
	require.Error(t, err, "a node with no services has nothing to publish through")
}
