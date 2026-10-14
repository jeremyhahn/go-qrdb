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

//go:build clusterproof

package soak

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/nemesis"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/workload"
)

// Node is one node under test: the name the schedule and the bundle use, and
// the API address the workload dials.
type Node struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// Cluster is what the soak asks the cluster under test beyond the workload's
// own operations: who leads, for the leader-isolating partition, and whether
// every shard has a leader, for E4.
type Cluster interface {
	// Leader returns the name of the node that reports itself leader.
	Leader(ctx context.Context) (string, error)
	// Shards returns, for every shard node hosts, the replica it reports as
	// that shard's leader; zero means the shard has none.
	Shards(ctx context.Context, node string) (map[uint64]uint64, error)
}

// ReplicaState is one replica's applied state: the input to E3.
type ReplicaState struct {
	Node         string `json:"node"`
	ShardID      uint64 `json:"shard_id"`
	ReplicaID    uint64 `json:"replica_id"`
	AppliedIndex uint64 `json:"applied_index"`
	Hash         uint64 `json:"hash"`
}

// ReplicaStates is the E3 seam: it reads every replica's applied index and
// state-machine hash once the cluster has quiesced. SDKCluster implements it
// over each node's replica-state read; a run without an implementation
// reports E3 unavailable rather than passing it.
type ReplicaStates interface {
	ReplicaStates(ctx context.Context) ([]ReplicaState, error)
}

// replicaStatesUnavailable is the detail E3 carries when no ReplicaStates is
// configured. It names what is missing, so the gap reads as a gap.
const replicaStatesUnavailable = "no replica state source: nothing is configured to read each " +
	"replica's (shard, replica, applied index, state-machine hash)"

// NodeInfo identifies the software and configuration one node ran. The
// configuration is kept only as a digest: it may carry credentials, and the
// bundle is meant to be published.
type NodeInfo struct {
	Name         string   `json:"name"`
	Address      string   `json:"address"`
	Version      string   `json:"version"`
	ConfigSHA256 string   `json:"config_sha256"`
	Errors       []string `json:"errors,omitempty"`
}

// Inspector reads a node's version and configuration fingerprint for the
// bundle.
type Inspector interface {
	Inspect(ctx context.Context, node string) (NodeInfo, error)
}

// notAvailable is what a NodeInfo field says when nothing could read it.
const notAvailable = "not available"

// maxVersionBytes bounds the version text kept from a node.
const maxVersionBytes = 4096

// ExecInspector inspects a node by running commands on it through its
// nemesis target: Version prints the server's version, Config prints its
// configuration, which is hashed and never kept.
type ExecInspector struct {
	Targets map[string]nemesis.Target
	Version []string
	Config  []string
}

var _ Inspector = (*ExecInspector)(nil)

// Inspect runs the configured commands on node. A command that is not
// configured leaves its field "not available"; one that fails is reported in
// Errors.
func (x *ExecInspector) Inspect(ctx context.Context, node string) (NodeInfo, error) {
	info := NodeInfo{Name: node, Version: notAvailable, ConfigSHA256: notAvailable}
	t, ok := x.Targets[node]
	if !ok {
		return info, &ClusterError{Node: node, Op: "inspect", Err: ErrUnavailable}
	}
	if len(x.Version) > 0 {
		out, err := t.Exec(ctx, x.Version)
		if err != nil {
			info.Errors = append(info.Errors, (&ClusterError{Node: node, Op: "read version", Err: err}).Error())
		} else {
			info.Version = versionText(out)
		}
	}
	if len(x.Config) > 0 {
		out, err := t.Exec(ctx, x.Config)
		if err != nil {
			info.Errors = append(info.Errors, (&ClusterError{Node: node, Op: "read config", Err: err}).Error())
		} else {
			sum := sha256.Sum256(out)
			info.ConfigSHA256 = hex.EncodeToString(sum[:])
		}
	}
	return info, nil
}

// versionText is a node's version output as the bundle keeps it: valid
// UTF-8, trimmed, bounded.
func versionText(out []byte) string {
	if len(out) > maxVersionBytes {
		out = out[:maxVersionBytes]
	}
	s := strings.TrimSpace(strings.ToValidUTF8(string(out), string(utf8.RuneError)))
	if s == "" {
		return notAvailable
	}
	return s
}

// SDKCluster is the Cluster over the SDK's admin surface: each query dials
// the node through the workload's transport and credentials.
type SDKCluster struct {
	Nodes   []Node
	Connect workload.Connector
}

var (
	_ Cluster       = (*SDKCluster)(nil)
	_ ReplicaStates = (*SDKCluster)(nil)
)

// Leader asks every node for its status and returns the one node that
// reports itself leader. None, or more than one, is an error: the leader is
// the target of a fault, and guessing would aim it at the wrong node.
func (c *SDKCluster) Leader(ctx context.Context) (string, error) {
	var leaders []string
	var errs []string
	for _, n := range c.Nodes {
		var st *transport.StatusResponse
		err := c.admin(ctx, n, "status", func(a transport.AdminClient) (err error) {
			st, err = a.Status(ctx)
			return err
		})
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if st.IsLeader {
			leaders = append(leaders, n.Name)
		}
	}
	if len(leaders) == 1 {
		return leaders[0], nil
	}
	return "", &LeaderError{Leaders: leaders, Failures: errs}
}

// Shards returns each shard node hosts and the leader it reports.
func (c *SDKCluster) Shards(ctx context.Context, node string) (map[uint64]uint64, error) {
	var n *Node
	for i := range c.Nodes {
		if c.Nodes[i].Name == node {
			n = &c.Nodes[i]
		}
	}
	if n == nil {
		return nil, &ClusterError{Node: node, Op: "peers", Err: ErrUnavailable}
	}
	var peers *transport.PeersResponse
	if err := c.admin(ctx, *n, "peers", func(a transport.AdminClient) (err error) {
		peers, err = a.Peers(ctx)
		return err
	}); err != nil {
		return nil, err
	}
	out := make(map[uint64]uint64, len(peers.Shards))
	for _, s := range peers.Shards {
		out[s.ShardID] = s.LeaderID
	}
	return out, nil
}

// ReplicaStates asks every node for the state of each replica it hosts. A
// node answers only for its own replicas and never forwards, so the cluster's
// states are the union of every node's answer. One node that cannot answer
// fails the read: E3 compares replicas, and a missing one could be the one
// that diverged.
func (c *SDKCluster) ReplicaStates(ctx context.Context) ([]ReplicaState, error) {
	var out []ReplicaState
	for _, n := range c.Nodes {
		var resp *transport.ReplicaStatesResponse
		if err := c.admin(ctx, n, "replica-states", func(a transport.AdminClient) (err error) {
			resp, err = a.ReplicaStates(ctx, 0)
			return err
		}); err != nil {
			return nil, err
		}
		for _, r := range resp.Replicas {
			out = append(out, ReplicaState{
				Node: n.Name, ShardID: r.ShardID, ReplicaID: r.ReplicaID,
				AppliedIndex: r.AppliedIndex, Hash: r.Hash,
			})
		}
	}
	return out, nil
}

// admin runs one admin call on a fresh connection to n.
func (c *SDKCluster) admin(ctx context.Context, n Node, op string, call func(transport.AdminClient) error) error {
	t, err := c.Connect(ctx, n.Address)
	if err != nil {
		return &ClusterError{Node: n.Name, Op: op, Err: err}
	}
	defer t.Close()
	a, ok := t.(transport.AdminClient)
	if !ok {
		return &ClusterError{Node: n.Name, Op: op, Err: ErrUnavailable}
	}
	if err := call(a); err != nil {
		return &ClusterError{Node: n.Name, Op: op, Err: err}
	}
	return nil
}
