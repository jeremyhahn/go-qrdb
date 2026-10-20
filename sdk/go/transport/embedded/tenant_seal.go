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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sealconv"
)

// TenantBarrierSeal seals a per-tenant barrier by delegating to the
// configured TenantServicer.
func (t *Transport) TenantBarrierSeal(ctx context.Context, req *transport.TenantBarrierSealRequest) error {
	if err := t.gate("TenantBarrierSeal"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return &NilServiceError{Service: "TenantServicer"}
	}
	if req == nil {
		return &NilRequestError{Method: "TenantBarrierSeal"}
	}
	if err := t.authorizeTenantTarget(ctx, req.TenantID, permission.TenantBarrierSeal); err != nil {
		return err
	}
	return t.tenantSvc.TenantBarrierSeal(ctx, req.TenantID)
}

// TenantBarrierStatus reports the current sealed/initialized state for a
// per-tenant barrier by delegating to the configured TenantServicer.
func (t *Transport) TenantBarrierStatus(ctx context.Context, req *transport.TenantBarrierStatusRequest) (*transport.TenantBarrierStatusResponse, error) {
	if err := t.gate("TenantBarrierStatus"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if req == nil {
		return nil, &NilRequestError{Method: "TenantBarrierStatus"}
	}
	if err := t.authorizeTenantBarrierSealedOp(ctx, req.TenantID, permission.TenantBarrierStatus); err != nil {
		return nil, err
	}
	status, err := t.tenantSvc.TenantBarrierStatus(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	return &transport.TenantBarrierStatusResponse{
		TenantID:        status.TenantID,
		Sealed:          status.Sealed,
		Initialized:     status.Initialized,
		Strategy:        status.Strategy,
		HardwareBacked:  status.HardwareBacked,
		ShamirThreshold: status.ShamirThreshold,
		ShamirTotal:     status.ShamirTotal,
		ClusterSeal:     sealconv.FromReport(status.ClusterSeal),
		KeyFingerprint:  status.KeyFingerprint,
		Nodes:           barrierNodesFromTenant(status.Nodes),
	}, nil
}

// barrierNodesFromTenant converts the nodes serving a tenant barrier.
func barrierNodesFromTenant(nodes []tenant.BarrierNode) []transport.TenantBarrierNode {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]transport.TenantBarrierNode, len(nodes))
	for i, n := range nodes {
		out[i] = transport.TenantBarrierNode{Node: n.Node, Endpoints: n.Endpoints}
	}
	return out
}
