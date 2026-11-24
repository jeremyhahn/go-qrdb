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

package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/release"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// activationKV serves only what the feature gate reads and writes; any other
// KV call on it is a test failure (a nil embedded interface panics).
type activationKV struct {
	service.KVServicer
	keys []string
	// unbounded counts the reads made without a deadline.
	unbounded atomic.Int32
}

func (a *activationKV) ListAll(ctx context.Context, prefix string) ([]string, error) {
	if _, ok := ctx.Deadline(); !ok {
		a.unbounded.Add(1)
	}
	var out []string
	for _, k := range a.keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

func (a *activationKV) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return false, nil
}

// leader reports the system shard's leader as known or not.
type leader struct {
	known atomic.Bool
	asked atomic.Int32
}

func (l *leader) LeaderID(uint64) (uint64, bool, error) {
	l.asked.Add(1)
	return 1, l.known.Load(), nil
}

type soloSystem struct{}

func (soloSystem) GroupMembership(uint64) (featuregate.GroupMembership, error) {
	return featuregate.GroupMembership{ConfigChangeID: 1, Replicas: map[uint64]string{1: "n1"}}, nil
}

func gatedNode(t *testing.T, kv service.KVServicer, opts ...embedded.Option) *Node {
	t.Helper()
	gate, err := featuregate.New(featuregate.Config{Self: featuregate.Self{ReplicaID: 1}, SystemShardID: 1, Members: soloSystem{}})
	require.NoError(t, err)
	tr, err := embedded.New(kv, struct{ service.AdminServicer }{}, opts...)
	require.NoError(t, err)
	client, err := sdk.NewClient(tr)
	require.NoError(t, err)
	n := &Node{featureGate: gate}
	n.featureClient.Store(client)
	return n
}

// A clustered node under a cluster that activated a feature its binary lacks
// never advertises: the background start gives up rather than join.
func TestNode_StartFeatureGateNeverAdvertisesUnderAnUnknownActivation(t *testing.T) {
	kv := &activationKV{keys: []string{featuregate.ActivationPrefix + "future-opcode"}}
	n := gatedNode(t, kv)
	l := &leader{}
	l.known.Store(true)

	n.startFeatureGate(l, kv)
	n.featureWG.Wait()
	assert.Nil(t, n.featureAdv.Load(), "the node advertised under an activation it cannot honour")
	var unsupported *featuregate.UnsupportedFeatureError
	assert.ErrorAs(t, n.featureGate.Refusal(), &unsupported, "the refusal must be kept for the node's health")
	n.bgCancel()
	require.NoError(t, n.stopFeatureGate())
}

// TestFeatureGateRefused_RestartReleaseGapIsTerminal: the start loop stops
// on a restart across a release gap as it does on an activated feature the
// binary lacks, however the start wraps it, and retries a store failure.
func TestFeatureGateRefused_RestartReleaseGapIsTerminal(t *testing.T) {
	gap := &release.GapError{Channel: "raft-membership", Local: "0.60.0-alpha", Peer: "0.58.0-alpha"}
	assert.True(t, featureGateRefused(gap))
	assert.True(t, featureGateRefused(fmt.Errorf("start: %w", gap)))
	assert.True(t, featureGateRefused(&featuregate.UnsupportedFeatureError{Feature: "future-opcode"}))
	assert.False(t, featureGateRefused(&featuregate.StoreError{Op: "list releases", Err: errors.New("no leader")}))
	assert.False(t, featureGateRefused(context.DeadlineExceeded))
}

// closingLeader is a leader whose host can be closed, as the quarantine of a
// refused node does.
type closingLeader struct {
	leader
	closed atomic.Int32
}

func (l *closingLeader) Close() error {
	l.closed.Add(1)
	return nil
}

// assertQuarantined checks that a node refused by the release check is off
// the data plane: no advertisement, the Raft host closed, every gated call
// refused, and the refusal kept.
func assertQuarantined(t *testing.T, n *Node, l *closingLeader, target any) {
	t.Helper()
	assert.Nil(t, n.featureAdv.Load(), "a refused node keeps no feature advertisement")
	assert.Nil(t, n.memberAdv.Load(), "a refused node keeps no member record")
	assert.Equal(t, int32(1), l.closed.Load(), "the refused node's Raft host must be closed")
	require.ErrorAs(t, n.Refusal(), target, "the node keeps the refusal")
	probe := n.refusalProbe(nil)
	assert.True(t, probe.IsSealed(), "every data-plane call must be refused")
	sealed, unconfirmed := probe.SealState()
	assert.True(t, sealed)
	assert.False(t, unconfirmed)
	n.bgCancel()
	require.NoError(t, n.stopFeatureGate())
	require.NoError(t, n.stopMemberRecord())
	require.NoError(t, n.stopFeatureGate(), "stopping again after the quarantine must stay safe")
}

func TestNode_RestartReleaseGapQuarantinesTheNode(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	kv := &releaseHistoryKV{keys: map[string]bool{featuregate.ReleasePrefix + "1/0.58.0-alpha": true}}
	n := gatedNode(t, kv, embedded.WithLeaseService(noLeases{}))
	require.NoError(t, n.Refusal(), "a node not yet checked is not refused")
	assert.False(t, n.refusalProbe(nil).IsSealed())
	l := &closingLeader{}
	l.known.Store(true)

	n.startFeatureGate(l, kv)
	n.featureWG.Wait()
	var gap *release.GapError
	assertQuarantined(t, n, l, &gap)
}

// quarantineSealDisabled runs the restart-release-gap check on a node with
// no barrier (seal disabled) and returns it quarantined.
func quarantineSealDisabled(t *testing.T) *Node {
	t.Helper()
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	kv := &releaseHistoryKV{keys: map[string]bool{featuregate.ReleasePrefix + "1/0.58.0-alpha": true}}
	n := gatedNode(t, kv, embedded.WithLeaseService(noLeases{}))
	require.Nil(t, n.barrier.Load(), "the node must be seal-disabled")
	assert.False(t, n.embeddedSealProbe(nil).IsSealed(), "a node not yet checked is not refused")
	assert.False(t, n.listenerSealProbe(nil).IsSealed(), "a node not yet checked is not refused")
	l := &closingLeader{}
	l.known.Store(true)
	n.startFeatureGate(l, kv)
	n.featureWG.Wait()
	var gap *release.GapError
	require.ErrorAs(t, n.Refusal(), &gap)
	t.Cleanup(func() {
		n.bgCancel()
		require.NoError(t, n.stopFeatureGate())
		require.NoError(t, n.stopMemberRecord())
	})
	return n
}

// F3 (#1628): a seal-disabled node's embedded client consults Node.Refusal,
// so a quarantined node refuses the caller's embedded calls.
func TestNode_QuarantineRefusesSealDisabledEmbeddedClient(t *testing.T) {
	n := quarantineSealDisabled(t)
	probe := n.embeddedSealProbe(nil)
	assert.True(t, probe.IsSealed(), "the embedded client must be refused")
	tr, err := embedded.New(&releaseHistoryKV{keys: map[string]bool{}}, struct{ service.AdminServicer }{},
		embedded.WithSealProbe(probe))
	require.NoError(t, err)
	_, err = tr.Get(context.Background(), "k")
	require.Error(t, err, "a quarantined node's embedded Get must be refused")
}

// F3 (#1628): the probe the node hands its network listeners (REST, gRPC,
// QUIC, unix, MCP) reports sealed once the node is quarantined, seal
// enabled or not.
func TestNode_QuarantineRefusesOnEveryListener(t *testing.T) {
	n := quarantineSealDisabled(t)
	probe := n.listenerSealProbe(nil)
	assert.True(t, probe.IsSealed(), "every listener must refuse a quarantined node's calls")
	sealed, unconfirmed := probe.SealState()
	assert.True(t, sealed)
	assert.False(t, unconfirmed)
}

func TestNode_UnsupportedFeatureQuarantinesTheNode(t *testing.T) {
	kv := &activationKV{keys: []string{featuregate.ActivationPrefix + "future-opcode"}}
	n := gatedNode(t, kv)
	l := &closingLeader{}
	l.known.Store(true)

	n.startFeatureGate(l, kv)
	n.featureWG.Wait()
	var unsupported *featuregate.UnsupportedFeatureError
	assertQuarantined(t, n, l, &unsupported)
}

// releaseHistoryKV is a store holding the release history whose state
// machine has no leases (the embedded transport is built without one).
type releaseHistoryKV struct {
	service.KVServicer
	mu      sync.Mutex
	keys    map[string]bool
	records map[string][]byte
}

func (k *releaseHistoryKV) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make(map[string][]byte)
	for key, v := range k.records {
		if strings.HasPrefix(key, prefix) {
			out[key] = v
		}
	}
	return out, nil
}

func (k *releaseHistoryKV) list(prefix string) []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []string
	for key := range k.keys {
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
	}
	return out
}

func (k *releaseHistoryKV) ListAll(_ context.Context, prefix string) ([]string, error) {
	return k.list(prefix), nil
}

func (k *releaseHistoryKV) ListStale(_ context.Context, prefix string) ([]string, error) {
	return k.list(prefix), nil
}

func (k *releaseHistoryKV) PutIfAbsent(_ context.Context, key string, _ []byte) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys[key] {
		return false, nil
	}
	k.keys[key] = true
	return true, nil
}

func (k *releaseHistoryKV) Delete(_ context.Context, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.keys, key)
	return nil
}

func (k *releaseHistoryKV) has(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.keys[key]
}

// noLeases is the lease service of a state machine without leases (the
// memory and disk state machines): a grant is refused as unsupported.
type noLeases struct{ service.LeaseServicer }

func (noLeases) Grant(context.Context, int64) (*lease.Lease, error) {
	return nil, &service.CapabilityUnsupportedError{Message: "leases need the MVCC state machine"}
}

// An embedded node whose state machine has no leases cannot advertise, so
// Gate.Start never runs; the restart release check still does: a recorded
// release gap is a terminal refusal, and an adjacent restart records the
// running release.
func TestNode_StartFeatureGateWithoutLeasesEnforcesTheRelease(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	cases := []struct {
		name    string
		last    string
		refused bool
	}{
		{name: "restart across a gap", last: "0.58.0-alpha", refused: true},
		{name: "restart on the next release", last: "0.59.0-alpha"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			last := featuregate.ReleasePrefix + "1/" + tc.last
			kv := &releaseHistoryKV{keys: map[string]bool{last: true}}
			n := gatedNode(t, kv, embedded.WithLeaseService(noLeases{}))
			l := &leader{}
			l.known.Store(true)

			n.startFeatureGate(l, kv)
			n.featureWG.Wait()
			assert.Nil(t, n.featureAdv.Load(), "a node without leases has no advertisement")
			var gap *release.GapError
			if tc.refused {
				require.ErrorAs(t, n.featureGate.Refusal(), &gap, "the gap refusal must be kept for the node's health")
				assert.False(t, kv.has(featuregate.ReleasePrefix+"1/0.60.0-alpha"), "a refused binary records no release")
			} else {
				require.NoError(t, n.featureGate.Refusal())
				assert.True(t, kv.has(featuregate.ReleasePrefix+"1/0.60.0-alpha"), "the running release is recorded")
				assert.False(t, kv.has(last), "the previous release is replaced")
			}
			n.bgCancel()
			require.NoError(t, n.stopFeatureGate())
		})
	}
}

// A node without leases admits a same-release joiner from its unleased record.
func TestNode_StartFeatureGateWithoutLeasesAdmitsASameReleaseCandidate(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	joiner, err := json.Marshal(cluster.Member{
		NodeID: 4, Address: "n4:7000", Features: featuregate.SupportedNames(), ReleaseVersion: "0.60.0-alpha",
	})
	require.NoError(t, err)
	kv := &releaseHistoryKV{
		keys:    map[string]bool{featuregate.ReleasePrefix + "1/0.59.0-alpha": true},
		records: map[string][]byte{featuregate.AdvertisementPrefix + "4": joiner},
	}
	n := gatedNode(t, kv, embedded.WithLeaseService(noLeases{}))
	l := &leader{}
	l.known.Store(true)

	n.startFeatureGate(l, kv)
	n.featureWG.Wait()
	require.NoError(t, n.featureGate.Refusal())
	binding, err := n.featureGate.AdmitReplicaAt(1, "n4:7000")
	require.NoError(t, err)
	assert.Equal(t, []string{"0.60.0-alpha"}, binding.CandidateReleases)
	var unannounced *featuregate.UnannouncedCandidateError
	_, err = n.featureGate.AdmitReplicaAt(1, "n9:7000")
	require.ErrorAs(t, err, &unannounced, "a candidate with no record must be refused")

	n.bgCancel()
	require.NoError(t, n.stopFeatureGate())
}

// Each start attempt reads under a deadline: a ReadIndex the leader drops is
// never answered, and an attempt without one would wait for it forever
// instead of retrying.
func TestNode_StartFeatureGateBoundsEachAttempt(t *testing.T) {
	kv := &activationKV{keys: []string{featuregate.ActivationPrefix + "future-opcode"}}
	n := gatedNode(t, kv)
	l := &leader{}
	l.known.Store(true)

	n.startFeatureGate(l, kv)
	n.featureWG.Wait()
	assert.Zero(t, kv.unbounded.Load(), "a start attempt read without a deadline")
	n.bgCancel()
	require.NoError(t, n.stopFeatureGate())
}

// While the system shard has no leader the start keeps waiting, and Close
// ends the wait.
func TestNode_StartFeatureGateWaitsForALeaderAndStopsOnClose(t *testing.T) {
	kv := &activationKV{}
	n := gatedNode(t, kv)
	l := &leader{}

	n.startFeatureGate(l, kv)
	require.Eventually(t, func() bool { return l.asked.Load() > 0 }, 2*time.Second, 5*time.Millisecond)
	n.bgCancel()
	require.NoError(t, n.stopFeatureGate())
	assert.Nil(t, n.featureAdv.Load())
}

// A node with no gate -- a host with no transport -- starts nothing.
func TestNode_StartFeatureGateWithoutAGate(t *testing.T) {
	n := &Node{}
	n.startFeatureGate(&leader{}, &activationKV{})
	require.NoError(t, n.stopFeatureGate())
}

// The feature client needs a started node: over an empty one it refuses
// with the typed client-creation error rather than connecting.
func TestNewConnectedEmbeddedClient_RefusesAnEmptyNode(t *testing.T) {
	var create *ClientCreateError
	if c, err := newConnectedEmbeddedClient(context.Background(), &lifecycle.Node{}, nil); c != nil || !errors.As(err, &create) {
		t.Fatalf("client over an empty node = %v, %v; want *ClientCreateError", c, err)
	}
}
