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
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
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

func gatedNode(t *testing.T, kv service.KVServicer) *Node {
	t.Helper()
	gate, err := featuregate.New(featuregate.Config{Self: featuregate.Self{ReplicaID: 1}, SystemShardID: 1, Members: soloSystem{}})
	require.NoError(t, err)
	tr, err := embedded.New(kv, struct{ service.AdminServicer }{})
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
