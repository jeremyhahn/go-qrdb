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

package service

import (
	"context"
	"sort"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/raftadmin"
)

// topologyReportTimeout bounds the load report the topology provider gathers so
// a stalled read pipeline cannot block a placement decision indefinitely.
const topologyReportTimeout = 5 * time.Second

// HostTopologyProvider implements placement.TopologyProvider over a live Raft
// host and the RebalanceService load scoring. It is the production seam between
// the pure placement scheduler and the cluster: the node roster comes from the
// host's NodeHostInfo (every node that hosts at least the shared system shard),
// and per-node byte/shard load reuses the RebalanceService coefficient-of-
// variation report rather than re-deriving load.
//
// The provider is read-only and safe for concurrent use.
type HostTopologyProvider struct {
	host      raftadmin.Host
	rebalance RebalanceServicer
	timeout   time.Duration
}

// Compile-time interface check.
var _ placement.TopologyProvider = (*HostTopologyProvider)(nil)

// NewHostTopologyProvider constructs a topology provider over the given host.
// rebalance is optional: when non-nil its imbalance report supplies per-node
// byte and shard load (the primary scheduler signal); when nil, load falls back
// to the shard counts derivable from the host's NodeHostInfo. host must be
// non-nil.
func NewHostTopologyProvider(host raftadmin.Host, rebalance RebalanceServicer) *HostTopologyProvider {
	return &HostTopologyProvider{host: host, rebalance: rebalance, timeout: topologyReportTimeout}
}

// Nodes returns the current cluster topology and per-node load. Every node that
// hosts a shard the local host knows about (which, in a healthy cluster,
// includes the shared system shard replicated to all members) appears in the
// result, marked healthy. Per-node bytes and shard counts come from the
// RebalanceService report when available; otherwise shard counts are derived
// from the host's shard membership. The result is sorted by NodeID for
// deterministic scheduling.
func (p *HostTopologyProvider) Nodes() []placement.NodeLoad {
	if p == nil || p.host == nil {
		return nil
	}

	nodes := make(map[string]*placement.NodeLoad)
	ensure := func(addr string) *placement.NodeLoad {
		n, ok := nodes[addr]
		if !ok {
			n = &placement.NodeLoad{NodeID: addr, RaftAddr: addr, Healthy: true}
			nodes[addr] = n
		}
		return n
	}

	// Roster + shard counts from the host's view. NodeHostInfo.ShardInfoList
	// enumerates every shard and its members (replicaID -> raft address), so
	// the union of member addresses is the cluster roster.
	info := p.host.NodeHostInfo(quicraft.NodeHostInfoOption{SkipLogInfo: true})
	if info != nil {
		if info.RaftAddress != "" {
			ensure(info.RaftAddress)
		}
		for _, si := range info.ShardInfoList {
			for _, addr := range si.Nodes {
				if addr == "" {
					continue
				}
				ensure(addr).ShardCount++
			}
		}
	}

	// Prefer the RebalanceService report for load: it already aggregates
	// per-node shard counts and stored bytes (the coefficient-of-variation
	// scoring the placement objective extends). Its NodeID is the node's Raft
	// address, matching the roster keys above.
	if p.rebalance != nil {
		ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
		report, err := p.rebalance.GetImbalanceReport(ctx)
		cancel()
		if err == nil && report != nil {
			for _, nl := range report.Nodes {
				n := ensure(nl.Address)
				n.Bytes = nl.TotalBytes
				n.ShardCount = nl.ShardCount
			}
		}
	}

	out := make([]placement.NodeLoad, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}
