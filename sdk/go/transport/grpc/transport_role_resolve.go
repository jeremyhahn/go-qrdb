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

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// GetRoleByName retrieves a role by its tenant-scoped name.
func (t *Transport) GetRoleByName(ctx context.Context, req *transport.GetRoleByNameRequest) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.GetRoleByName(ctx, &pb.GetRoleByNameRequest{
		TenantId: req.TenantID,
		Name:     req.Name,
	})
	if err != nil {
		return nil, mapGRPCError("GetRoleByName", err)
	}

	return protoToRole(resp), nil
}

// ResolvePermissions returns the union of effective permissions for the
// given role IDs (sorted, deduplicated).
func (t *Transport) ResolvePermissions(ctx context.Context, req *transport.ResolvePermissionsRequest) (*transport.ResolvePermissionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.ResolveRolePermissions(ctx, &pb.ResolveRolePermissionsRequest{
		RoleIds: req.RoleIDs,
	})
	if err != nil {
		return nil, mapGRPCError("ResolvePermissions", err)
	}

	return &transport.ResolvePermissionsResponse{Permissions: resp.GetPermissions()}, nil
}
