// Copyright 2026 Jeremy Hahn
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

package cluster

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errShardHalted is what sealedLog returns for a write it took while sealed.
var errShardHalted = errors.New("sealedLog: shard halted by a write while the barrier was sealed")

// sealedLog is a MembershipStore that behaves like the replicated log under
// the node barrier: every member write is a proposal, and a proposal that
// reaches the log while the barrier is sealed halts the shard
// (go-quicraft#296). It is also the barrier's probe, so the advertiser under
// test and the log agree on when the barrier is sealed.
type sealedLog struct {
	*fakeStore
	sealed atomic.Bool
	halted atomic.Bool
}

func newSealedLog() *sealedLog { return &sealedLog{fakeStore: newFakeStore()} }

func (l *sealedLog) IsSealed() bool { return l.sealed.Load() }

func (l *sealedLog) PutMember(ctx context.Context, nodeID uint64, data []byte, ttl time.Duration) error {
	if l.sealed.Load() {
		l.halted.Store(true)
		return errShardHalted
	}
	return l.fakeStore.PutMember(ctx, nodeID, data, ttl)
}

func (l *sealedLog) DeleteMember(ctx context.Context, nodeID uint64) error {
	if l.sealed.Load() {
		l.halted.Store(true)
		return errShardHalted
	}
	return l.fakeStore.DeleteMember(ctx, nodeID)
}

var _ SealProbe = (*sealedLog)(nil)

func newSealedAdvertiser(t *testing.T, log *sealedLog) *Advertiser {
	t.Helper()
	a, err := NewAdvertiser(log, Member{NodeID: 7, Address: "10.0.0.7:63001"}, AdvertiseConfig{
		LeaseTTL:          time.Second,
		KeepAliveInterval: 5 * time.Millisecond,
	}, WithAdvertiseSealProbe(log))
	require.NoError(t, err)
	return a
}

// TestAdvertiser_SealedBarrier_KeepAliveDoesNotPropose is the go-qrdb#662
// regression: a keepalive tick that fires while the node barrier is sealed
// must not reach the log, and the refresh resumes once the barrier opens.
func TestAdvertiser_SealedBarrier_KeepAliveDoesNotPropose(t *testing.T) {
	log := newSealedLog()
	a := newSealedAdvertiser(t, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, a.Start(ctx))

	log.sealed.Store(true)
	sealedAt := log.putCallCount()
	require.Never(t, log.halted.Load, 150*time.Millisecond, 5*time.Millisecond,
		"a keepalive was proposed while the node barrier was sealed")
	assert.Equal(t, sealedAt, log.putCallCount(), "no refresh lands while sealed")

	log.sealed.Store(false)
	require.Eventually(t, func() bool { return log.putCallCount() > sealedAt },
		time.Second, 5*time.Millisecond, "the keepalive did not resume after the unseal")
	require.NoError(t, a.Close())
}

// TestAdvertiser_SealedBarrier_RegisterRefused verifies a registration attempted
// while sealed is refused with a typed error before it reaches the log.
func TestAdvertiser_SealedBarrier_RegisterRefused(t *testing.T) {
	log := newSealedLog()
	log.sealed.Store(true)
	a := newSealedAdvertiser(t, log)

	err := a.Start(context.Background())
	var sealed *NodeSealedError
	require.ErrorAs(t, err, &sealed)
	assert.Equal(t, "register", sealed.Op)
	assert.False(t, log.halted.Load(), "the registration reached the sealed log")
	assert.Zero(t, log.putCallCount())

	// The refusal leaves the advertiser startable once the barrier opens.
	log.sealed.Store(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, a.Start(ctx))
	require.NoError(t, a.Close())
}

// TestAdvertiser_SealedBarrier_CloseLeavesRecordToItsLease verifies a Close
// while sealed does not propose the withdrawal: the record expires with its
// lease instead, and the refusal is reported.
func TestAdvertiser_SealedBarrier_CloseLeavesRecordToItsLease(t *testing.T) {
	log := newSealedLog()
	a := newSealedAdvertiser(t, log)
	require.NoError(t, a.Start(context.Background()))

	log.sealed.Store(true)
	err := a.Close()
	var sealed *NodeSealedError
	require.ErrorAs(t, err, &sealed)
	assert.Equal(t, "deregister", sealed.Op)
	assert.False(t, log.halted.Load(), "the withdrawal reached the sealed log")
	assert.Zero(t, log.deleteCount())
}

// TestAdvertiser_NoSealProbe_WritesUnconditionally verifies an advertiser
// composed without a probe keeps its previous behaviour: nothing to consult,
// every refresh is written.
func TestAdvertiser_NoSealProbe_WritesUnconditionally(t *testing.T) {
	store := newFakeStore()
	a, err := NewAdvertiser(store, Member{NodeID: 8, Address: "10.0.0.8:63001"}, AdvertiseConfig{
		KeepAliveInterval: 5 * time.Millisecond,
	}, WithAdvertiseSealProbe(nil))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, a.Start(ctx))
	require.Eventually(t, func() bool { return store.putCallCount() >= 2 },
		time.Second, 5*time.Millisecond)
	require.NoError(t, a.Close())
	assert.Equal(t, 1, store.deleteCount())
}

// TestMembershipManager_SealProbeReachesItsAdvertiser verifies the manager
// hands its probe to the advertiser it owns, so a manager started while the
// barrier is sealed proposes nothing.
func TestMembershipManager_SealProbeReachesItsAdvertiser(t *testing.T) {
	log := newSealedLog()
	log.sealed.Store(true)
	m, err := NewMembershipManager(log, Member{NodeID: 9, Address: "10.0.0.9:63001"}, 1,
		MembershipConfig{}, WithMembershipSealProbe(log))
	require.NoError(t, err)

	err = m.Start(context.Background())
	var sealed *NodeSealedError
	require.ErrorAs(t, err, &sealed)
	assert.False(t, log.halted.Load(), "the manager's registration reached the sealed log")

	// Close while still sealed leaves the record to its lease.
	err = m.Close()
	require.ErrorAs(t, err, &sealed)
	assert.Equal(t, "deregister", sealed.Op)
	assert.False(t, log.halted.Load(), "the manager's withdrawal reached the sealed log")
	assert.Zero(t, log.deleteCount())
}

// TestMembershipManager_CloseUnsealed_Deregisters verifies the manager's own
// withdrawal is still proposed once the barrier is open.
func TestMembershipManager_CloseUnsealed_Deregisters(t *testing.T) {
	log := newSealedLog()
	m, err := NewMembershipManager(log, Member{NodeID: 10, Address: "10.0.0.10:63001"}, 1,
		MembershipConfig{}, WithMembershipSealProbe(log))
	require.NoError(t, err)
	require.NoError(t, m.Start(context.Background()))
	require.NoError(t, m.Close())
	assert.False(t, log.halted.Load())
	assert.Positive(t, log.deleteCount())
}

func TestNodeSealedError_Error(t *testing.T) {
	err := &NodeSealedError{Op: "register"}
	assert.Equal(t, "cluster: node barrier is sealed: register not proposed", err.Error())
}
