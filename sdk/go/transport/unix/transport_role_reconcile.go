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

package unix

import (
	"context"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ReconcileBuiltInRoles converges the tenant's stored built-in roles onto the
// server build's definitions.
func (t *Transport) ReconcileBuiltInRoles(ctx context.Context, req *transport.ReconcileRolesRequest) (*transport.ReconcileRolesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	tenantID := ""
	if req != nil {
		tenantID = req.TenantID
	}

	resp, err := t.client.ReconcileBuiltInRoles(ctx, &pb.ReconcileBuiltInRolesRequest{TenantId: tenantID})
	if err != nil {
		return nil, mapGRPCError("ReconcileBuiltInRoles", err)
	}

	return protoToReconcileReport(resp), nil
}

// protoToReconcileReport converts a proto reconciliation report to its
// transport representation.
func protoToReconcileReport(resp *pb.ReconcileBuiltInRolesResponse) *transport.ReconcileRolesResponse {
	if resp == nil {
		return nil
	}
	out := &transport.ReconcileRolesResponse{TenantID: resp.GetTenantId()}
	for _, r := range resp.GetRoles() {
		out.Roles = append(out.Roles, transport.RoleReconcileResult{
			RoleID:  r.GetRoleId(),
			Name:    r.GetName(),
			Created: r.GetCreated(),
			Added:   r.GetAdded(),
			Removed: r.GetRemoved(),
		})
	}
	return out
}
