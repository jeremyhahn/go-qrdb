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

package raft

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
)

// groups is a MembershipSource over fixed groups.
type groups map[uint64]featuregate.GroupMembership

func (g groups) GroupMembership(shardID uint64) (featuregate.GroupMembership, error) {
	return g[shardID], nil
}

func gateOver(t *testing.T, members groups) *featuregate.Gate {
	t.Helper()
	g, err := featuregate.New(featuregate.Config{Self: featuregate.Self{ReplicaID: 1}, SystemShardID: 1, Members: members})
	require.NoError(t, err)
	return g
}

// guardedWritesActivatedKV is an ActivationKV listing guarded-writes as
// already activated -- the state Gate.CheckActivated reads to mark a
// feature enabled without needing Start or any membership at all.
type guardedWritesActivatedKV struct{}

func (guardedWritesActivatedKV) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return false, nil
}

func (guardedWritesActivatedKV) ListAll(context.Context, string) ([]string, error) {
	return []string{featuregate.ActivationPrefix + string(featuregate.GuardedWrites)}, nil
}

// With a gate, the adapter answers from it: Enabled is Activated and nothing
// else (go-qrdb#482). Before anything is activated, neither group is
// enabled, regardless of what either group's current members support --
// gate.Start is never called in this test, so nothing is. Once the cluster
// records guarded-writes as activated, CheckActivated adopts that record
// (the same read Start itself does at startup) and both groups become
// enabled, because activation is cluster-wide, not per-group: a constant
// False FeatureEnabled would never show this.
func TestHostAdapter_FeatureEnabledAsksTheGate(t *testing.T) {
	gate := gateOver(t, groups{
		1: {ConfigChangeID: 1, Replicas: map[uint64]string{1: "n1", 2: "n2"}},
		7: {ConfigChangeID: 1, Replicas: map[uint64]string{9: "n1"}},
	})
	a := NewWithInner(nil, &mockQuicraftHost{})
	WithFeatureGate(gate)(a)
	assert.False(t, a.FeatureEnabled(1, featuregate.GuardedWrites), "node 2 advertises nothing")
	assert.False(t, a.FeatureEnabled(7, featuregate.GuardedWrites), "nothing is activated cluster-wide yet")

	require.NoError(t, gate.CheckActivated(context.Background(), guardedWritesActivatedKV{}))
	assert.True(t, a.FeatureEnabled(1, featuregate.GuardedWrites), "guarded-writes is now activated cluster-wide")
	assert.True(t, a.FeatureEnabled(7, featuregate.GuardedWrites), "activation enables every group, not just one")
}

// With no gate and no host -- a test adapter -- every replica is this
// process.
func TestHostAdapter_FeatureEnabledWithoutAGateOrHost(t *testing.T) {
	a := NewWithInner(nil, &mockQuicraftHost{})
	assert.True(t, a.FeatureEnabled(1, featuregate.GuardedWrites))
}

// A host with no transport needs no gate: NewFeatureGate returns none and
// the adapter over it enables every feature.
func TestNewFeatureGate_TransportlessHostNeedsNone(t *testing.T) {
	root := t.TempDir()
	host, err := quicraft.NewHost(quicraftconfig.HostConfig{
		WALDir:        filepath.Join(root, "wal"),
		NodeHostDir:   filepath.Join(root, "nodehost"),
		RaftAddress:   "127.0.0.1:63999",
		ListenAddress: "127.0.0.1:63999",
		DeploymentID:  1,
	}, quicraft.WithoutTransport(), quicraft.WithoutEncryption())
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close() })

	gate, err := NewFeatureGate(host, 1, 1)
	require.NoError(t, err)
	assert.Nil(t, gate)
	a := New(host, WithFeatureGate(gate))
	assert.True(t, a.FeatureEnabled(1, featuregate.GuardedWrites))
}

// activatedKV lists one activated feature this binary does not implement.
type activatedKV struct{}

func (activatedKV) PutIfAbsent(context.Context, string, []byte) (bool, error) { return false, nil }

func (activatedKV) ListAll(context.Context, string) ([]string, error) {
	return []string{featuregate.ActivationPrefix + "future-opcode"}, nil
}

// A node whose binary lacks a feature the cluster activated reports itself
// degraded on its health check, the one surface every transport serves.
func TestHostAdapter_HealthIsDegradedUnderARefusal(t *testing.T) {
	root := t.TempDir()
	host, err := quicraft.NewHost(quicraftconfig.HostConfig{
		WALDir:        filepath.Join(root, "wal"),
		NodeHostDir:   filepath.Join(root, "nodehost"),
		RaftAddress:   "127.0.0.1:63998",
		ListenAddress: "127.0.0.1:63998",
		DeploymentID:  1,
	}, quicraft.WithoutTransport(), quicraft.WithoutEncryption())
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close() })

	gate := gateOver(t, groups{1: {ConfigChangeID: 1, Replicas: map[uint64]string{1: "n1"}}})
	a := New(host, WithFeatureGate(gate))
	ok := &quicraft.HealthStatus{Status: "ok", DiskOK: true}
	assert.Same(t, ok, a.withGateVerdict(ok), "no refusal, no change")
	assert.Equal(t, host.HealthCheck().Status, a.HealthCheck().Status)

	require.Error(t, gate.CheckActivated(context.Background(), activatedKV{}))
	got := a.withGateVerdict(ok)
	assert.Equal(t, "degraded", got.Status)
	assert.True(t, got.DiskOK, "the rest of the report is kept")
	assert.Equal(t, "ok", ok.Status, "the host's own report is not mutated")
	unhealthy := &quicraft.HealthStatus{Status: "unhealthy"}
	assert.Equal(t, "unhealthy", a.withGateVerdict(unhealthy).Status, "a worse verdict is never masked")
	assert.Equal(t, host.HealthCheck().Status, a.HealthCheck().Status, "a host that is not ok stays as it is")
	assert.Same(t, ok, New(host).withGateVerdict(ok), "an ungated adapter is unaffected")
}

// The adapter admits a replica only where its gate does; without a gate no
// feature is ever enabled, so every node is admitted.
func TestHostAdapter_AdmitReplica(t *testing.T) {
	ungated := NewWithInner(nil, &mockQuicraftHost{})
	assert.NoError(t, ungated.AdmitReplica(7, "old:1"))

	gate := gateOver(t, groups{
		1: {ConfigChangeID: 1, Replicas: map[uint64]string{1: "n1"}},
		7: {ConfigChangeID: 1, Replicas: map[uint64]string{9: "n1"}},
	})
	a := NewWithInner(nil, &mockQuicraftHost{})
	WithFeatureGate(gate)(a)
	var refused *featuregate.UnsupportedReplicaError
	require.ErrorAs(t, a.AdmitReplica(7, "old:1"), &refused)
	assert.Equal(t, "old:1", refused.Address)
	assert.NoError(t, a.AdmitReplica(7, "n1"))
}
