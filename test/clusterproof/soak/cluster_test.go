//go:build clusterproof

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

package soak

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// bareTransport is a connected transport with no admin surface.
type bareTransport struct{ closed *atomic.Int32 }

func (bareTransport) Connect(context.Context) error                   { return nil }
func (t bareTransport) Close() error                                  { t.closed.Add(1); return nil }
func (bareTransport) Healthy(context.Context) bool                    { return true }
func (bareTransport) Request(context.Context, string, any, any) error { return nil }

var _ transport.Transport = bareTransport{}

// replicaTransport is a node's admin surface that answers only the
// replica-state read. Any other admin call panics on the nil AdminClient.
type replicaTransport struct {
	bareTransport
	transport.AdminClient
	resp     *transport.ReplicaStatesResponse
	err      error
	gotShard *uint64
}

func (t replicaTransport) ReplicaStates(_ context.Context, shardID uint64) (*transport.ReplicaStatesResponse, error) {
	*t.gotShard = shardID
	return t.resp, t.err
}

var _ transport.AdminClient = replicaTransport{}

// replicaConnector serves each endpoint the transport in byAddr, counting
// closes in closed.
func replicaConnector(byAddr map[string]func(*atomic.Int32) transport.Transport, closed *atomic.Int32) func(context.Context, string) (transport.Transport, error) {
	return func(_ context.Context, endpoint string) (transport.Transport, error) {
		mk, ok := byAddr[endpoint]
		if !ok {
			return nil, errors.New("no node at " + endpoint)
		}
		return mk(closed), nil
	}
}

func answering(resp *transport.ReplicaStatesResponse, err error, gotShard *uint64) func(*atomic.Int32) transport.Transport {
	return func(closed *atomic.Int32) transport.Transport {
		return replicaTransport{bareTransport: bareTransport{closed}, resp: resp, err: err, gotShard: gotShard}
	}
}

func TestSDKClusterReplicaStatesTagsEveryNode(t *testing.T) {
	shard1, shard2 := uint64(99), uint64(99)
	var closed atomic.Int32
	c := &SDKCluster{
		Nodes: []Node{{Name: "n1", Address: "a1"}, {Name: "n2", Address: "a2"}},
		Connect: replicaConnector(map[string]func(*atomic.Int32) transport.Transport{
			"a1": answering(&transport.ReplicaStatesResponse{NodeID: "x", Replicas: []transport.ReplicaState{
				{ShardID: 1, ReplicaID: 1, AppliedIndex: 9, Hash: 1<<63 + 5},
				{ShardID: 2, ReplicaID: 1, AppliedIndex: 4, Hash: 3},
			}}, nil, &shard1),
			"a2": answering(&transport.ReplicaStatesResponse{NodeID: "y", Replicas: []transport.ReplicaState{
				{ShardID: 1, ReplicaID: 2, AppliedIndex: 9, Hash: 1<<63 + 5},
			}}, nil, &shard2),
		}, &closed),
	}
	got, err := c.ReplicaStates(context.Background())
	if err != nil {
		t.Fatalf("ReplicaStates: %v", err)
	}
	want := []ReplicaState{
		{Node: "n1", ShardID: 1, ReplicaID: 1, AppliedIndex: 9, Hash: 1<<63 + 5},
		{Node: "n1", ShardID: 2, ReplicaID: 1, AppliedIndex: 4, Hash: 3},
		{Node: "n2", ShardID: 1, ReplicaID: 2, AppliedIndex: 9, Hash: 1<<63 + 5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReplicaStates = %+v, want %+v; rows carry the configured node name", got, want)
	}
	if shard1 != 0 || shard2 != 0 {
		t.Errorf("shards asked = %d, %d; want 0 (every shard) of each node", shard1, shard2)
	}
	if n := closed.Load(); n != 2 {
		t.Errorf("closed %d transports, want 2", n)
	}
}

func TestSDKClusterReplicaStatesOneNodeFailsTheRead(t *testing.T) {
	var shard uint64
	var closed atomic.Int32
	refusal := &transport.CapabilityUnsupportedError{Message: "replica states requires mode: raft"}
	c := &SDKCluster{
		Nodes: []Node{{Name: "n1", Address: "a1"}, {Name: "n2", Address: "a2"}},
		Connect: replicaConnector(map[string]func(*atomic.Int32) transport.Transport{
			"a1": answering(&transport.ReplicaStatesResponse{Replicas: []transport.ReplicaState{{ShardID: 1}}}, nil, &shard),
			"a2": answering(nil, refusal, &shard),
		}, &closed),
	}
	got, err := c.ReplicaStates(context.Background())
	var ce *ClusterError
	if !errors.As(err, &ce) || ce.Node != "n2" || ce.Op != "replica-states" || !errors.Is(err, refusal) {
		t.Fatalf("err = %v (%T), want a replica-states ClusterError for n2 carrying the refusal", err, err)
	}
	if got != nil {
		t.Errorf("states = %+v, want none: a partial read could omit the replica that diverged", got)
	}
}

func TestSDKClusterReplicaStatesUnreachableOrNoAdmin(t *testing.T) {
	cases := map[string]struct {
		byAddr map[string]func(*atomic.Int32) transport.Transport
		want   error
	}{
		"no admin surface": {
			byAddr: map[string]func(*atomic.Int32) transport.Transport{
				"a1": func(closed *atomic.Int32) transport.Transport { return bareTransport{closed} },
			},
			want: ErrUnavailable,
		},
		"unreachable": {byAddr: map[string]func(*atomic.Int32) transport.Transport{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var closed atomic.Int32
			c := &SDKCluster{Nodes: []Node{{Name: "n1", Address: "a1"}}, Connect: replicaConnector(tc.byAddr, &closed)}
			_, err := c.ReplicaStates(context.Background())
			var ce *ClusterError
			if !errors.As(err, &ce) || ce.Node != "n1" || ce.Op != "replica-states" {
				t.Fatalf("err = %v (%T), want a replica-states ClusterError for n1", err, err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want it to wrap %v", err, tc.want)
			}
		})
	}
}

func TestSettled(t *testing.T) {
	cases := map[string]struct {
		states []ReplicaState
		want   bool
	}{
		"none": {want: true},
		"shards differ, each settled": {states: []ReplicaState{
			{ShardID: 1, AppliedIndex: 9}, {ShardID: 2, AppliedIndex: 4}, {ShardID: 1, AppliedIndex: 9}, {ShardID: 2, AppliedIndex: 4},
		}, want: true},
		"a follower trails": {states: []ReplicaState{
			{ShardID: 1, AppliedIndex: 9}, {ShardID: 2, AppliedIndex: 4}, {ShardID: 2, AppliedIndex: 3},
		}, want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := settled(tc.states); got != tc.want {
				t.Fatalf("settled = %v, want %v", got, tc.want)
			}
		})
	}
}

// catchingUp reports a trailing follower for its first lag reads, then the
// follower caught up.
type catchingUp struct {
	lag   int32
	reads atomic.Int32
}

func (c *catchingUp) ReplicaStates(context.Context) ([]ReplicaState, error) {
	follower := uint64(9)
	if c.reads.Add(1) <= c.lag {
		follower = 8
	}
	return []ReplicaState{
		{Node: "n1", ShardID: 1, ReplicaID: 1, AppliedIndex: 9, Hash: 7},
		{Node: "n2", ShardID: 1, ReplicaID: 2, AppliedIndex: follower, Hash: 7},
	}, nil
}

func TestReplicasWaitsForAFollowerToCatchUp(t *testing.T) {
	src := &catchingUp{lag: 2}
	o := &Options{Replicas: src, RecoveryTimeout: time.Minute}
	got := o.replicas(context.Background())
	if got.Status != StatusPass {
		t.Fatalf("E3 = %s (%s), want pass once the follower caught up", got.Status, got.Detail)
	}
	if n := src.reads.Load(); n != 3 {
		t.Errorf("read %d times, want 3: two trailing reads, then the settled one", n)
	}
}

func TestReplicasJudgesTheLastReadAtTheTimeout(t *testing.T) {
	src := &catchingUp{lag: 1 << 30}
	o := &Options{Replicas: src, RecoveryTimeout: 50 * time.Millisecond}
	got := o.replicas(context.Background())
	if got.Status != StatusUnavailable || !strings.Contains(got.Detail, "did not quiesce") {
		t.Fatalf("E3 = %s (%s), want unavailable: the follower never caught up", got.Status, got.Detail)
	}
}

func TestReplicasReadErrorIsUnavailable(t *testing.T) {
	o := &Options{Replicas: failingReplicas{}, RecoveryTimeout: time.Minute}
	got := o.replicas(context.Background())
	if got.Status != StatusUnavailable || !strings.HasPrefix(got.Detail, "cannot read replica states: ") {
		t.Fatalf("E3 = %s (%s), want unavailable naming the failed read", got.Status, got.Detail)
	}
}

// failingReplicas cannot read any replica's state.
type failingReplicas struct{}

func (failingReplicas) ReplicaStates(context.Context) ([]ReplicaState, error) {
	return nil, &ClusterError{Node: "n1", Op: "replica-states", Err: ErrUnavailable}
}
