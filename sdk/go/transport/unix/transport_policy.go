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

	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/policywire"
)

// Compile-time interface assertion.
var _ transport.ABACPolicyClient = (*Transport)(nil)

// ABACPolicyClient implementation: the QRDBService *ABACPolicy RPCs over the
// Unix socket, issued through the policywire.Caller the gRPC and Unix
// transports share. The server handlers live in
// pkg/api/grpc/service_policy.go.

// policies returns the shared RPC caller over this transport's client.
func (t *Transport) policies() policywire.Caller {
	return policywire.Caller{Client: t.client, MapError: mapGRPCError}
}

// CreateABACPolicy stores a new policy and returns the stored document.
func (t *Transport) CreateABACPolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.policies().Create(ctx, policy)
}

// GetABACPolicy returns one stored policy.
func (t *Transport) GetABACPolicy(ctx context.Context, req *transport.ABACPolicyRequest) (*abac.Policy, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.policies().Get(ctx, req)
}

// ListABACPolicies returns every policy the tenant holds.
func (t *Transport) ListABACPolicies(ctx context.Context, req *transport.ABACPolicyListRequest) (*transport.ABACPolicyListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.policies().List(ctx, req)
}

// UpdateABACPolicy replaces an existing policy's document.
func (t *Transport) UpdateABACPolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.policies().Update(ctx, policy)
}

// DeleteABACPolicy removes one policy.
func (t *Transport) DeleteABACPolicy(ctx context.Context, req *transport.ABACPolicyRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return t.policies().Delete(ctx, req)
}
