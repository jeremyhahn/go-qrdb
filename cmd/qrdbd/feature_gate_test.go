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
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
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

// countingLeaseServicer counts the keepalives an advertisement sends.
type countingLeaseServicer struct {
	mockLeaseServicer
	keepAlives atomic.Int64
}

func (c *countingLeaseServicer) KeepAlive(ctx context.Context, id int64) (*lease.Lease, error) {
	c.keepAlives.Add(1)
	return c.mockLeaseServicer.KeepAlive(ctx, id)
}

// cancelOnGrant ends the start's context while the advertisement registers.
type cancelOnGrant struct {
	mockLeaseServicer
	cancel context.CancelFunc
}

func (c *cancelOnGrant) Grant(ctx context.Context, ttl int64) (*lease.Lease, error) {
	c.cancel()
	return c.mockLeaseServicer.Grant(ctx, ttl)
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

	_, err := startFeatureGate(context.Background(), nil, gateServices(store), nil)
	require.ErrorAs(t, err, &unsupported)
	assert.Equal(t, featuregate.Feature("future-opcode"), unsupported.Feature)

	g := soloGate(t)
	t.Cleanup(func() { _ = g.Close() })
	_, err = startFeatureGate(context.Background(), g, gateServices(store), nil)
	require.ErrorAs(t, err, &unsupported)
}

// Without a gate, startup only checks; an activated feature this binary
// implements is no obstacle.
func TestStartFeatureGate_WithoutAGateOnlyChecks(t *testing.T) {
	store := map[string][]byte{featuregate.ActivationPrefix + string(featuregate.GuardedWrites): []byte(`{}`)}
	closer, err := startFeatureGate(context.Background(), nil, gateServices(store), nil)
	require.NoError(t, err)
	require.NoError(t, closer.Close())
}

// With a gate, startup advertises and starts it; a single-node cluster
// activates guarded writes at once, and the closer stops everything.
func TestStartFeatureGate_AdvertisesAndActivates(t *testing.T) {
	store := map[string][]byte{}
	g := soloGate(t)
	closer, err := startFeatureGate(context.Background(), g, gateServices(store), nil)
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

	closer, err := startFeatureGate(context.Background(), g, svc, nil)
	require.NoError(t, err)
	require.NoError(t, closer.Close())
	assert.False(t, g.Activated(featuregate.GuardedWrites),
		"a degraded gate never started, so it tracks no activation locally")
}

// countingCloser counts withdrawals and fails each one with err.
type countingCloser struct {
	calls int
	err   error
}

func (c *countingCloser) Close() error { c.calls++; return c.err }

// captureDefaultLog routes slog's default logger into a buffer for the test.
// Not parallel: the default logger is process-wide.
func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestWithdrawFeatures_FailureIsLoggedNotFatal: shutdown withdraws the
// advertisement exactly once and goes on tearing down when the withdrawal
// fails; the failure is logged with its cause rather than swallowed.
func TestWithdrawFeatures_FailureIsLoggedNotFatal(t *testing.T) {
	logs := captureDefaultLog(t)
	closer := &countingCloser{err: errors.New("lease revoke timed out")}

	withdrawFeatures(closer)

	assert.Equal(t, 1, closer.calls)
	assert.Contains(t, logs.String(), "feature advertisement withdrawal failed")
	assert.Contains(t, logs.String(), "lease revoke timed out")
}

// TestWithdrawFeatures_SuccessLogsNothing: a clean withdrawal is silent.
func TestWithdrawFeatures_SuccessLogsNothing(t *testing.T) {
	logs := captureDefaultLog(t)
	closer := &countingCloser{}

	withdrawFeatures(closer)

	assert.Equal(t, 1, closer.calls)
	assert.Empty(t, logs.String())
}

// TestStartFeatureGate_BoundsEveryStartRead is the regression for a restarted
// node that never served its API: the gate's activation read ran on the boot
// context, which has no deadline, so a ReadIndex the leader dropped held the
// boot forever. Every read the start makes must carry its attempt's deadline.
func TestStartFeatureGate_BoundsEveryStartRead(t *testing.T) {
	svc := gateServices(map[string][]byte{})
	kv := svc.KV.(*mockKVServicer)
	list := kv.listFn
	var (
		mu        sync.Mutex
		returned  bool
		unbounded []string
	)
	kv.listFn = func(ctx context.Context, prefix string) ([]string, error) {
		mu.Lock()
		if _, ok := ctx.Deadline(); !ok && !returned {
			unbounded = append(unbounded, prefix)
		}
		mu.Unlock()
		return list(ctx, prefix)
	}

	closer, err := startFeatureGate(context.Background(), soloGate(t), svc, nil)
	mu.Lock()
	returned = true
	got := unbounded
	mu.Unlock()
	require.NoError(t, err)
	t.Cleanup(func() { _ = closer.Close() })
	assert.Empty(t, got, "a start read ran without a deadline")
}

// TestStartFeatureGate_AdvertisementOutlivesTheAttempt pins the other half:
// the attempt's context ends when the start returns, and the advertisement
// must go on refreshing its lease until Close rather than stop with it.
func TestStartFeatureGate_AdvertisementOutlivesTheAttempt(t *testing.T) {
	svc := gateServices(map[string][]byte{})
	leases := &countingLeaseServicer{}
	svc.Lease = leases

	closer, err := startFeatureGate(context.Background(), soloGate(t), svc, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = closer.Close() })
	require.Eventually(t, func() bool { return leases.keepAlives.Load() > 0 },
		3*cluster.DefaultKeepAliveInterval, 50*time.Millisecond,
		"the advertisement stopped refreshing when its start attempt ended")
}

// TestStartFeatureGate_RefusesANodeWithoutServices: a gate on a node whose
// services are not wired fails at the embedded client rather than start an
// advertisement it cannot write.
func TestStartFeatureGate_RefusesANodeWithoutServices(t *testing.T) {
	closer, err := startFeatureGate(context.Background(), soloGate(t), &lifecycle.Node{}, nil)
	require.Error(t, err)
	assert.Nil(t, closer)
}

// TestClientStartFeatureGate_FailsWhenItsContextEndsMidStart: a start whose
// context ends while the advertisement registers fails, and withdraws the
// advertisement rather than leave one running that its caller never holds.
func TestClientStartFeatureGate_FailsWhenItsContextEndsMidStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := gateServices(map[string][]byte{})
	svc.Lease = &cancelOnGrant{cancel: cancel}
	client, err := newEmbeddedClient(svc.KV, svc.Admin, svc.MVCC, svc.Lease, svc.Watch)
	require.NoError(t, err)
	g := soloGate(t)
	t.Cleanup(func() { _ = g.Close() })

	adv, err := client.StartFeatureGate(ctx, g, svc.KV)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, adv)
	assert.False(t, g.Activated(featuregate.GuardedWrites), "the gate started although its start failed")
}
