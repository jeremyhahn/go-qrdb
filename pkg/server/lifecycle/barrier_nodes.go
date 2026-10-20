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

package lifecycle

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// barrierNodes is the tenant service's tenant.BarrierNodeSource on a raft
// node: a tenant's barrier is served, each node with its own key, by every
// node holding a replica of one of its groups -- the nodes the barrier init
// watcher serves it on (barrierInitWatch.serves) -- and each is reached at
// the API endpoints its member record publishes.
type barrierNodes struct {
	placements tenantPlacements
	members    service.KVServicer
}

var _ tenant.BarrierNodeSource = barrierNodes{}

// BarrierNodes implements tenant.BarrierNodeSource. A node with no member
// record is listed with no endpoints: it serves the barrier, and a client
// reports it unreachable.
func (b barrierNodes) BarrierNodes(ctx context.Context, tenantID string) ([]tenant.BarrierNode, error) {
	ps, err := b.placements.ListForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var addrs []string
	for _, p := range ps {
		for _, r := range p.Replicas {
			if r.RaftAddr == "" || seen[r.RaftAddr] {
				continue
			}
			seen[r.RaftAddr] = true
			addrs = append(addrs, r.RaftAddr)
		}
	}
	if len(addrs) == 0 {
		return nil, nil
	}
	endpoints, err := b.endpoints(ctx)
	if err != nil {
		return nil, err
	}
	slices.Sort(addrs)
	out := make([]tenant.BarrierNode, 0, len(addrs))
	for _, addr := range addrs {
		out = append(out, tenant.BarrierNode{Node: addr, Endpoints: endpoints[addr]})
	}
	return out, nil
}

// endpoints reads the member records into each member's API endpoints, by
// Raft address. A record that does not decode is skipped, as every reader of
// the member records skips it.
func (b barrierNodes) endpoints(ctx context.Context) (map[string]map[string]string, error) {
	records, err := b.members.Scan(ctx, cluster.MembershipPrefix)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]string, len(records))
	for _, raw := range records {
		var m cluster.Member
		if json.Unmarshal(raw, &m) != nil || m.Address == "" || len(m.APIEndpoints) == 0 {
			continue
		}
		eps := make(map[string]string, len(m.APIEndpoints))
		for proto, addr := range m.APIEndpoints {
			eps[string(proto)] = addr
		}
		out[m.Address] = eps
	}
	return out, nil
}

// wireBarrierNodes gives the tenant service the nodes serving each barrier,
// for TenantBarrierStatus to report to a client fanning a barrier init out.
// Without the placement table no tenant group exists to serve one.
func wireBarrierNodes(n *Node) {
	if n.PlacementStore == nil || n.KV == nil {
		return
	}
	if wiring, ok := n.Tenant.(tenant.BarrierNodeWiring); ok {
		wiring.SetBarrierNodeSource(barrierNodes{placements: n.PlacementStore, members: n.KV})
	}
}
