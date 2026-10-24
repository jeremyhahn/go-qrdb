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

package quic

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/policywire"
)

// Compile-time interface assertion.
var _ transport.ABACPolicyClient = (*Transport)(nil)

// ABACPolicyClient implementation: the abacpolicy.* QUIC methods, framed with
// the gRPC protobuf messages. The server handlers live in
// pkg/api/quic/handlers_policy.go.

// CreateABACPolicy stores a new policy and returns the stored document.
func (t *Transport) CreateABACPolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error) {
	return t.policyDocument(ctx, "CreateABACPolicy", "abacpolicy.Create", policy)
}

// GetABACPolicy returns one stored policy.
func (t *Transport) GetABACPolicy(ctx context.Context, req *transport.ABACPolicyRequest) (*abac.Policy, error) {
	if req == nil {
		return nil, policywire.NilRequest("GetABACPolicy")
	}
	var resp pb.ABACPolicy
	if err := t.protoCall(ctx, "abacpolicy.Get", policywire.Request(req), &resp); err != nil {
		return nil, err
	}
	return policywire.Decode("GetABACPolicy", &resp)
}

// ListABACPolicies returns every policy the tenant holds.
func (t *Transport) ListABACPolicies(ctx context.Context, req *transport.ABACPolicyListRequest) (*transport.ABACPolicyListResponse, error) {
	if req == nil {
		return nil, policywire.NilRequest("ListABACPolicies")
	}
	var resp pb.ABACPolicyListResponse
	if err := t.protoCall(ctx, "abacpolicy.List", policywire.ListRequest(req), &resp); err != nil {
		return nil, err
	}
	return policywire.DecodeList("ListABACPolicies", &resp)
}

// UpdateABACPolicy replaces an existing policy's document.
func (t *Transport) UpdateABACPolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error) {
	return t.policyDocument(ctx, "UpdateABACPolicy", "abacpolicy.Update", policy)
}

// DeleteABACPolicy removes one policy.
func (t *Transport) DeleteABACPolicy(ctx context.Context, req *transport.ABACPolicyRequest) error {
	if req == nil {
		return policywire.NilRequest("DeleteABACPolicy")
	}
	return t.protoCall(ctx, "abacpolicy.Delete", policywire.Request(req), &emptypb.Empty{})
}

// policyDocument issues a create or update: both take and return a whole
// policy document.
func (t *Transport) policyDocument(ctx context.Context, method, quicMethod string, policy *abac.Policy) (*abac.Policy, error) {
	if policy == nil {
		return nil, policywire.NilRequest(method)
	}
	in, err := policywire.Encode(method, policy)
	if err != nil {
		return nil, err
	}
	var resp pb.ABACPolicy
	if err := t.protoCall(ctx, quicMethod, in, &resp); err != nil {
		return nil, err
	}
	return policywire.Decode(method, &resp)
}
