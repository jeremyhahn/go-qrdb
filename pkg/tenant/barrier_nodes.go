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

package tenant

import (
	"context"
	"log/slog"
	"sync/atomic"
)

// BarrierNode is one node serving a tenant's barrier with its own key: its
// Raft address, and the address it serves each client API protocol on
// ("rest", "grpc", "quic", "mcp"), as its member record publishes them.
type BarrierNode struct {
	Node      string            `json:"node"`
	Endpoints map[string]string `json:"endpoints,omitempty"`
}

// BarrierNodeSource lists the nodes serving a tenant's barrier, for a client
// that fans a barrier init out to every one of them.
type BarrierNodeSource interface {
	BarrierNodes(ctx context.Context, tenantID string) ([]BarrierNode, error)
}

// BarrierNodeWiring is how a composition wires the node source, which reads
// the cluster's placements and member records and so exists only after the
// tenant service is built.
type BarrierNodeWiring interface {
	SetBarrierNodeSource(BarrierNodeSource)
}

// Compile-time check.
var _ BarrierNodeWiring = (*TenantService)(nil)

// barrierFanOut is what TenantBarrierStatus reports for a client fanning a
// barrier init out to every node serving a barrier.
type barrierFanOut struct {
	// nodeSource lists the nodes serving a barrier; see SetBarrierNodeSource.
	nodeSource atomic.Pointer[barrierNodeSourceBox]
}

// barrierNodeSourceBox lets the source be swapped atomically: it is wired
// after the service is built, while statuses may be reading it.
type barrierNodeSourceBox struct{ source BarrierNodeSource }

// SetBarrierNodeSource makes TenantBarrierStatus list the nodes serving the
// barrier. Without one it lists none, which is right only for a composition
// that serves each barrier on one node.
func (f *barrierFanOut) SetBarrierNodeSource(source BarrierNodeSource) {
	f.nodeSource.Store(&barrierNodeSourceBox{source: source})
}

func (f *barrierFanOut) barrierNodeSource() BarrierNodeSource {
	if box := f.nodeSource.Load(); box != nil {
		return box.source
	}
	return nil
}

// reportServingNodes adds to status the nodes a client fans a barrier init
// out to. They are not the barrier's own state, so a list this node cannot
// read is logged and left out rather than failing the status.
func (s *TenantService) reportServingNodes(ctx context.Context, status *TenantBarrierStatus) {
	source := s.barrierNodeSource()
	if source == nil {
		return
	}
	nodes, err := source.BarrierNodes(ctx, status.TenantID)
	if err != nil {
		s.logger.Warn("tenant barrier status: serving nodes unreadable",
			slog.String("tenant_id", status.TenantID), slog.Any("error", err))
		status.NodesUnreadable = true
		return
	}
	status.Nodes = nodes
}
