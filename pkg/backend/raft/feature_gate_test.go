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
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/release"
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

func (k guardedWritesActivatedKV) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return k.ListAll(ctx, prefix)
}

func (guardedWritesActivatedKV) Delete(context.Context, string) error { return nil }

var _ featuregate.ActivationKV = guardedWritesActivatedKV{}

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

func (k activatedKV) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return k.ListAll(ctx, prefix)
}

func (activatedKV) Delete(context.Context, string) error { return nil }

var _ featuregate.ActivationKV = activatedKV{}

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

// lastRanKV holds only the release member 1 last recorded.
type lastRanKV struct{ rel string }

func (lastRanKV) PutIfAbsent(context.Context, string, []byte) (bool, error) { return false, nil }

func (k lastRanKV) ListAll(_ context.Context, prefix string) ([]string, error) {
	if prefix != featuregate.ReleasePrefix {
		return nil, nil
	}
	return []string{featuregate.ReleasePrefix + "1/" + k.rel}, nil
}

func (k lastRanKV) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return k.ListAll(ctx, prefix)
}

func (lastRanKV) Delete(context.Context, string) error { return nil }

// noAdverts is a membership store no member advertises in.
type noAdverts struct{}

func (noAdverts) PutMember(context.Context, uint64, []byte, time.Duration) error { return nil }
func (noAdverts) DeleteMember(context.Context, uint64) error                     { return nil }
func (noAdverts) WatchMembers(context.Context) (<-chan cluster.MemberEvent, error) {
	return nil, errors.New("watch unavailable")
}
func (noAdverts) ListMembers(context.Context) ([]cluster.Member, error) { return nil, nil }

// TestHostAdapter_HealthIsDegradedAfterARestartReleaseGap: a member that
// last ran 0.58 restarted on 0.60 is refused by the gate's start, and the
// adapter's health reports the refusal rather than an ok node.
func TestHostAdapter_HealthIsDegradedAfterARestartReleaseGap(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	gate := gateOver(t, groups{1: {ConfigChangeID: 1, Replicas: map[uint64]string{1: "n1"}}})
	var gap *release.GapError
	require.ErrorAs(t, gate.Start(context.Background(), lastRanKV{rel: "0.58.0-alpha"}, noAdverts{}), &gap)

	a := New(nil, WithFeatureGate(gate))
	got := a.withGateVerdict(&quicraft.HealthStatus{Status: "ok", DiskOK: true})
	assert.Equal(t, "degraded", got.Status)
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

func TestHostAdapter_AdmitReplicaAt(t *testing.T) {
	ungated := NewWithInner(nil, &mockQuicraftHost{})
	id, err := ungated.AdmitReplicaAt(7, "old:1")
	require.NoError(t, err)
	assert.Zero(t, id)

	gate := gateOver(t, groups{
		1: {ConfigChangeID: 1, Replicas: map[uint64]string{1: "n1"}},
		7: {ConfigChangeID: 1, Replicas: map[uint64]string{9: "n1"}},
	})
	a := NewWithInner(nil, &mockQuicraftHost{})
	WithFeatureGate(gate)(a)

	for _, shard := range []uint64{1, 7} {
		wantID, wantErr := gate.AdmitReplicaAt(shard, "n1")
		require.NoError(t, wantErr)
		id, err = a.AdmitReplicaAt(shard, "n1")
		require.NoError(t, err)
		assert.Equal(t, wantID, id, "shard %d", shard)
	}

	var refused *featuregate.UnsupportedReplicaError
	id, err = a.AdmitReplicaAt(7, "old:1")
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, "old:1", refused.Address)
	assert.Zero(t, id)

	// CheckConfigBinding delegates to the gate; an ungated adapter binds
	// nothing.
	stale := featuregate.ConfigBinding{ConfigChangeID: 0, Required: true}
	require.NoError(t, ungated.CheckConfigBinding(1, stale))
	var staleErr *featuregate.StaleConfigurationError
	require.ErrorAs(t, a.CheckConfigBinding(1, stale), &staleErr)
	require.NoError(t, a.CheckConfigBinding(1, featuregate.ConfigBinding{ConfigChangeID: 1, Required: true}))
}

// ProposeBound runs propose directly on an ungated adapter, returning its
// error, and on a gated adapter delegates to the gate: a matching binding
// runs propose, a stale one is refused before propose runs.
func TestHostAdapter_ProposeBound(t *testing.T) {
	gate := gateOver(t, groups{1: {ConfigChangeID: 1, Replicas: map[uint64]string{1: "n1"}}})
	gated := NewWithInner(nil, &mockQuicraftHost{})
	WithFeatureGate(gate)(gated)
	ungated := NewWithInner(nil, &mockQuicraftHost{})

	proposeErr := errors.New("propose failed")
	current := featuregate.ConfigBinding{ConfigChangeID: 1, Required: true}
	stale := featuregate.ConfigBinding{ConfigChangeID: 0, Required: true}

	tests := []struct {
		name    string
		adapter *HostAdapter
		binding featuregate.ConfigBinding
		result  error
		ran     bool
		wantErr func(t *testing.T, err error)
	}{
		{"ungated zero binding runs", ungated, featuregate.ConfigBinding{}, nil, true,
			func(t *testing.T, err error) { require.NoError(t, err) }},
		{"ungated stale binding still runs", ungated, stale, nil, true,
			func(t *testing.T, err error) { require.NoError(t, err) }},
		{"ungated returns propose error", ungated, featuregate.ConfigBinding{}, proposeErr, true,
			func(t *testing.T, err error) { require.ErrorIs(t, err, proposeErr) }},
		{"gated current binding runs", gated, current, nil, true,
			func(t *testing.T, err error) { require.NoError(t, err) }},
		{"gated returns propose error", gated, current, proposeErr, true,
			func(t *testing.T, err error) { require.ErrorIs(t, err, proposeErr) }},
		{"gated stale binding refused", gated, stale, nil, false,
			func(t *testing.T, err error) {
				var staleErr *featuregate.StaleConfigurationError
				require.ErrorAs(t, err, &staleErr)
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ran := false
			err := tc.adapter.ProposeBound(1, tc.binding, func(<-chan struct{}) (<-chan struct{}, error) {
				ran = true
				return nil, tc.result
			})
			tc.wantErr(t, err)
			assert.Equal(t, tc.ran, ran)
		})
	}
}
