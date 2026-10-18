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

package grpc

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.RBACProvider  = (*Transport)(nil)
	_ transport.RBACEndpoints = rbacEndpoints{}
)

// RBACClient returns the RBACClient over the server's gRPC RPCs.
func (t *Transport) RBACClient() transport.RBACClient {
	return transport.NewRemoteRBACClient(rbacEndpoints{t: t})
}

// rbacEndpoints adapts the gRPC transport to transport.RBACEndpoints.
type rbacEndpoints struct {
	t *Transport
}

// ResolvePermissions maps to the ResolveRolePermissions RPC.
func (e rbacEndpoints) ResolvePermissions(ctx context.Context, req *transport.ResolvePermissionsRequest) (*transport.ResolvePermissionsResponse, error) {
	return e.t.ResolvePermissions(ctx, req)
}

// ListOwnPermissions maps to the ListOwnPermissions RPC.
func (e rbacEndpoints) ListOwnPermissions(ctx context.Context) ([]transport.PermissionInfo, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := e.t.client.ListOwnPermissions(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("ListOwnPermissions", err)
	}
	out := make([]transport.PermissionInfo, len(resp.GetPermissions()))
	for i, p := range resp.GetPermissions() {
		out[i] = transport.PermissionInfo{
			Name:     p.GetName(),
			Resource: p.GetResource(),
			Action:   p.GetAction(),
			Scope:    transport.PermissionScope(p.GetScope()),
		}
	}
	return out, nil
}

// EvaluateAuthorization maps to the Authorize RPC.
func (e rbacEndpoints) EvaluateAuthorization(ctx context.Context, req *transport.AuthorizeRequest) (*transport.AuthorizeDecision, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	in := &pb.AuthorizeRequest{Resource: req.Resource, Action: req.Action}
	if id := req.Identity; id != nil {
		in.Identity = &pb.AuthorizeIdentity{Subject: id.Subject, TenantId: id.TenantID, Roles: id.Roles}
	}
	resp, err := e.t.client.Authorize(ctx, in)
	if err != nil {
		return nil, mapGRPCError("Authorize", err)
	}
	return &transport.AuthorizeDecision{Allowed: resp.GetAllowed(), Reason: resp.GetReason()}, nil
}
