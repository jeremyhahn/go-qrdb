// Copyright (c) 2026 Jeremy Hahn
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

// Package roleconv converts built-in role reconciliation reports from the
// protobuf messages to the transport-neutral types. The gRPC and Unix
// transports receive the same protobuf messages, so the mapping lives here
// once; the QUIC transport receives JSON and does not import it, which keeps
// it off a lean standalone embedder's import graph. It is internal: only the
// transport implementations may import it.
package roleconv

import (
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ReconcileFromProto converts one tenant's reconciliation report. A nil
// response converts to nil.
func ReconcileFromProto(resp *pb.ReconcileBuiltInRolesResponse) *transport.ReconcileRolesResponse {
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

// AllTenantsFromProto converts an all-tenant reconciliation report, carrying
// every field. A nil response converts to an empty report rather than nil, so
// a caller never has to tell "nothing reconciled" from "no answer".
func AllTenantsFromProto(resp *pb.ReconcileBuiltInRolesAllTenantsResponse) *transport.ReconcileAllTenantsRolesResponse {
	out := &transport.ReconcileAllTenantsRolesResponse{}
	for _, tr := range resp.GetTenants() {
		if conv := ReconcileFromProto(tr); conv != nil {
			out.Tenants = append(out.Tenants, *conv)
		}
	}
	for _, s := range resp.GetSkipped() {
		out.Skipped = append(out.Skipped, transport.RoleReconcileSkippedTenant{
			TenantID: s.GetTenantId(),
			Reason:   s.GetReason(),
			Detail:   s.GetDetail(),
		})
	}
	for _, f := range resp.GetFailed() {
		out.Failed = append(out.Failed, transport.RoleReconcileFailedTenant{
			TenantID: f.GetTenantId(),
			Error:    f.GetError(),
		})
	}
	return out
}
