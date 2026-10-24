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

package policywire

import (
	"context"

	"google.golang.org/grpc"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Encode converts a policy document into its protobuf message. A document
// the wire cannot carry (a condition match that does not encode) is a
// RequestError for method.
func Encode(method string, p *abac.Policy) (*pb.ABACPolicy, error) {
	m, err := ToProto(p)
	if err != nil {
		return nil, &transport.RequestError{Method: method, Err: err}
	}
	return m, nil
}

// Decode converts a protobuf policy reply into the document. A reply that
// does not decode is a RequestError for method.
func Decode(method string, m *pb.ABACPolicy) (*abac.Policy, error) {
	p, err := FromProto(m)
	if err != nil {
		return nil, &transport.RequestError{Method: method, Err: err}
	}
	return p, nil
}

// DecodeList converts a protobuf list reply. An empty list is an empty,
// non-nil slice, as on every other transport.
func DecodeList(method string, resp *pb.ABACPolicyListResponse) (*transport.ABACPolicyListResponse, error) {
	policies, err := ListFromProto(resp)
	if err != nil {
		return nil, &transport.RequestError{Method: method, Err: err}
	}
	return &transport.ABACPolicyListResponse{Policies: policies}, nil
}

// Request converts a request naming one policy.
func Request(req *transport.ABACPolicyRequest) *pb.ABACPolicyRequest {
	return &pb.ABACPolicyRequest{TenantId: req.TenantID, Name: req.Name}
}

// ListRequest converts a list request.
func ListRequest(req *transport.ABACPolicyListRequest) *pb.ABACPolicyListRequest {
	return &pb.ABACPolicyListRequest{TenantId: req.TenantID}
}

// NilRequest is the error every policy method returns for a nil request.
func NilRequest(method string) error {
	return &transport.RequestError{Method: method, Err: &transport.NilRequestError{}}
}

// Caller issues the policy RPCs over a QRDBService client, mapping a gRPC
// status through MapError. The gRPC and Unix transports both hold one.
type Caller struct {
	Client   pb.QRDBServiceClient
	MapError func(method string, err error) error
}

// Create stores a new policy and returns the stored document.
func (c Caller) Create(ctx context.Context, policy *abac.Policy) (*abac.Policy, error) {
	return c.document(ctx, "CreateABACPolicy", policy, c.Client.CreateABACPolicy)
}

// Get returns one stored policy.
func (c Caller) Get(ctx context.Context, req *transport.ABACPolicyRequest) (*abac.Policy, error) {
	if req == nil {
		return nil, NilRequest("GetABACPolicy")
	}
	resp, err := c.Client.GetABACPolicy(ctx, Request(req))
	if err != nil {
		return nil, c.MapError("GetABACPolicy", err)
	}
	return Decode("GetABACPolicy", resp)
}

// List returns every policy the tenant holds.
func (c Caller) List(ctx context.Context, req *transport.ABACPolicyListRequest) (*transport.ABACPolicyListResponse, error) {
	if req == nil {
		return nil, NilRequest("ListABACPolicies")
	}
	resp, err := c.Client.ListABACPolicies(ctx, ListRequest(req))
	if err != nil {
		return nil, c.MapError("ListABACPolicies", err)
	}
	return DecodeList("ListABACPolicies", resp)
}

// Update replaces an existing policy's document and returns the stored one.
func (c Caller) Update(ctx context.Context, policy *abac.Policy) (*abac.Policy, error) {
	return c.document(ctx, "UpdateABACPolicy", policy, c.Client.UpdateABACPolicy)
}

// Delete removes one policy.
func (c Caller) Delete(ctx context.Context, req *transport.ABACPolicyRequest) error {
	if req == nil {
		return NilRequest("DeleteABACPolicy")
	}
	if _, err := c.Client.DeleteABACPolicy(ctx, Request(req)); err != nil {
		return c.MapError("DeleteABACPolicy", err)
	}
	return nil
}

// documentRPC is the shape of the two RPCs that take and return a whole
// policy document: CreateABACPolicy and UpdateABACPolicy.
type documentRPC func(ctx context.Context, in *pb.ABACPolicy, opts ...grpc.CallOption) (*pb.ABACPolicy, error)

// document issues a create or update.
func (c Caller) document(ctx context.Context, method string, policy *abac.Policy, rpc documentRPC) (*abac.Policy, error) {
	if policy == nil {
		return nil, NilRequest(method)
	}
	in, err := Encode(method, policy)
	if err != nil {
		return nil, err
	}
	resp, err := rpc(ctx, in)
	if err != nil {
		return nil, c.MapError(method, err)
	}
	return Decode(method, resp)
}
