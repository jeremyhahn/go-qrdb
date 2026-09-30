// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// soloMembers is a system shard whose only replica is this node.
type soloMembers struct{}

func (soloMembers) GroupMembership(uint64) (featuregate.GroupMembership, error) {
	return featuregate.GroupMembership{ConfigChangeID: 1, Replicas: map[uint64]string{1: "127.0.0.1:63100"}}, nil
}

func gateServices(store map[string][]byte) *lifecycle.Node {
	return &lifecycle.Node{
		KV:    newMockKVStore(store),
		Admin: &minimalAdminServicer{},
		MVCC:  &mockMVCCKVServicer{mockKVServicer: &mockKVServicer{}},
		Lease: &mockLeaseServicer{},
		Watch: &mockWatchServicer{},
	}
}

func soloGate(t *testing.T) *featuregate.Gate {
	t.Helper()
	g, err := featuregate.New(featuregate.Config{
		Self:          featuregate.Self{ReplicaID: 1},
		SystemShardID: shard.ShardKV,
		Members:       soloMembers{},
	})
	require.NoError(t, err)
	return g
}

// A gated binary refuses to start under a cluster that activated a feature
// it does not implement -- with a gate, and on a host with no transport that
// runs none.
func TestStartFeatureGate_RefusesAnActivatedFeatureThisBinaryLacks(t *testing.T) {
	store := map[string][]byte{featuregate.ActivationPrefix + "future-opcode": []byte(`{}`)}
	var unsupported *featuregate.UnsupportedFeatureError

	_, err := startFeatureGate(context.Background(), nil, gateServices(store))
	require.ErrorAs(t, err, &unsupported)
	assert.Equal(t, featuregate.Feature("future-opcode"), unsupported.Feature)

	g := soloGate(t)
	t.Cleanup(func() { _ = g.Close() })
	_, err = startFeatureGate(context.Background(), g, gateServices(store))
	require.ErrorAs(t, err, &unsupported)
}

// Without a gate, startup only checks; an activated feature this binary
// implements is no obstacle.
func TestStartFeatureGate_WithoutAGateOnlyChecks(t *testing.T) {
	store := map[string][]byte{featuregate.ActivationPrefix + string(featuregate.GuardedWrites): []byte(`{}`)}
	closer, err := startFeatureGate(context.Background(), nil, gateServices(store))
	require.NoError(t, err)
	require.NoError(t, closer.Close())
}

// With a gate, startup advertises and starts it; a single-node cluster
// activates guarded writes at once, and the closer stops everything.
func TestStartFeatureGate_AdvertisesAndActivates(t *testing.T) {
	store := map[string][]byte{}
	g := soloGate(t)
	closer, err := startFeatureGate(context.Background(), g, gateServices(store))
	require.NoError(t, err)
	assert.True(t, g.Activated(featuregate.GuardedWrites))
	_, recorded := store[featuregate.ActivationPrefix+string(featuregate.GuardedWrites)]
	assert.True(t, recorded, "activation must be recorded in the system store")
	require.NoError(t, closer.Close())
}

// The ReadIndex warm-up of a freshly elected leader is waited out; any
// other failure, and a cancelled context, is returned at once.
func TestWhileReadIndexNotReady(t *testing.T) {
	calls := 0
	err := whileReadIndexNotReady(context.Background(), func() error {
		calls++
		if calls < 3 {
			return quicraft.ErrReadIndexNotReady
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, calls)

	boom := errors.New("boom")
	calls = 0
	err = whileReadIndexNotReady(context.Background(), func() error { calls++; return boom })
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 1, calls)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = whileReadIndexNotReady(ctx, func() error { return quicraft.ErrReadIndexNotReady })
	assert.ErrorIs(t, err, context.Canceled)
}
