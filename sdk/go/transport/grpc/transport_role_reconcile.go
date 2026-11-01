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

package grpc

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/roleconv"
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

	return roleconv.ReconcileFromProto(resp), nil
}

// ReconcileBuiltInRolesAllTenants converges the stored built-in roles of every
// tenant on the server onto the server build's definitions. The caller must be
// a platform operator holding roles:reconcile_all.
func (t *Transport) ReconcileBuiltInRolesAllTenants(ctx context.Context) (*transport.ReconcileAllTenantsRolesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.ReconcileBuiltInRolesAllTenants(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("ReconcileBuiltInRolesAllTenants", err)
	}

	return roleconv.AllTenantsFromProto(resp), nil
}
