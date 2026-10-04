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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// leaseUnsupportedServicer stands in for a state machine type ("memory",
// "disk") that implements no lease subsystem at all: every call answers
// *service.CapabilityUnsupportedError, the same typed error
// statemachine.ServiceNotConfiguredError{Service: "lease"} surfaces as at
// the service layer, which the embedded transport reports as
// *transport.CapabilityUnsupportedError (see TestStartFeatureGate_
// DegradesWhenLeaseIsUnavailable).
type leaseUnsupportedServicer struct{}

func (leaseUnsupportedServicer) Grant(context.Context, int64) (*lease.Lease, error) {
	return nil, &service.CapabilityUnsupportedError{Message: "statemachine: lease not configured"}
}

func (leaseUnsupportedServicer) Revoke(context.Context, int64) error {
	return &service.CapabilityUnsupportedError{Message: "statemachine: lease not configured"}
}

func (leaseUnsupportedServicer) KeepAlive(context.Context, int64) (*lease.Lease, error) {
	return nil, &service.CapabilityUnsupportedError{Message: "statemachine: lease not configured"}
}

func (leaseUnsupportedServicer) TimeToLive(context.Context, int64) (*lease.Lease, error) {
	return nil, &service.CapabilityUnsupportedError{Message: "statemachine: lease not configured"}
}

func (leaseUnsupportedServicer) Leases(context.Context) ([]*lease.Lease, error) {
	return nil, &service.CapabilityUnsupportedError{Message: "statemachine: lease not configured"}
}

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

// TestStartFeatureGate_DegradesWhenLeaseIsUnavailable is the go-qrdb
// hotfix/cakey-restart proof: a state machine with no lease subsystem
// (service.LeaseServicer nil, standing in for the "memory"/"disk" sm types
// TestDEK_Cluster_EveryNodeDecryptsEveryVersion and
// TestRaftSMOverFileStorage_ThreeNodeCluster both boot with) answers
// self-advertisement with *transport.CapabilityUnsupportedError
// (translated from *service.CapabilityUnsupportedError at the embedded
// transport boundary). Before this fix, startFeatureGate propagated it as
// a fatal boot error -- "cluster: membership: failed to register self:
// transport: unsupported capability: statemachine: lease not configured"
// -- crashing every node on such a state machine, including the leader,
// which then stranded its followers on "not leader, leader unknown"
// forever. The node must boot and serve without the optional
// advertisement instead.
func TestStartFeatureGate_DegradesWhenLeaseIsUnavailable(t *testing.T) {
	store := map[string][]byte{}
	svc := gateServices(store)
	svc.Lease = &leaseUnsupportedServicer{}
	g := soloGate(t)
	t.Cleanup(func() { _ = g.Close() })

	closer, err := startFeatureGate(context.Background(), g, svc)
	require.NoError(t, err)
	require.NoError(t, closer.Close())
	assert.False(t, g.Activated(featuregate.GuardedWrites),
		"a degraded gate never started, so it tracks no activation locally")
}
