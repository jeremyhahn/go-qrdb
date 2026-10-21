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
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/policywire"
)

// policyUnconfigured is the status for a server built without a policy
// service, rendered through the classifier like every other service error.
func policyUnconfigured() error {
	return mapServiceError(&ServiceNotConfiguredError{Service: "abac policy"})
}

// policyFromRequest converts a request's policy, refusing one whose wire
// form does not convert as an invalid policy.
func policyFromRequest(req *pb.ABACPolicy) (*abac.Policy, error) {
	p, err := policywire.FromProto(req)
	if err != nil {
		return nil, &service.PolicyInvalidError{Name: req.GetMetadata().GetName(), Reason: "decode", Err: err}
	}
	if p == nil {
		return nil, service.ErrNilRequest
	}
	return p, nil
}

// policyResponse converts a stored policy to its gRPC form.
func policyResponse(p *abac.Policy, err error) (*pb.ABACPolicy, error) {
	if err != nil {
		return nil, mapServiceError(err)
	}
	out, err := policywire.ToProto(p)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return out, nil
}

// CreateABACPolicy validates, compiles and stores a new policy.
func (s *qrdbServiceServer) CreateABACPolicy(ctx context.Context, req *pb.ABACPolicy) (*pb.ABACPolicy, error) {
	if s.policySvc == nil {
		return nil, policyUnconfigured()
	}
	p, err := policyFromRequest(req)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return policyResponse(s.policySvc.CreatePolicy(ctx, p))
}

// GetABACPolicy returns one stored policy document.
func (s *qrdbServiceServer) GetABACPolicy(ctx context.Context, req *pb.ABACPolicyRequest) (*pb.ABACPolicy, error) {
	if s.policySvc == nil {
		return nil, policyUnconfigured()
	}
	return policyResponse(s.policySvc.GetPolicy(ctx, req.GetTenantId(), req.GetName()))
}

// ListABACPolicies returns every policy of the tenant, sorted by name.
func (s *qrdbServiceServer) ListABACPolicies(ctx context.Context, req *pb.ABACPolicyListRequest) (*pb.ABACPolicyListResponse, error) {
	if s.policySvc == nil {
		return nil, policyUnconfigured()
	}
	policies, err := s.policySvc.ListPolicies(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapServiceError(err)
	}
	out, err := policywire.ListToProto(policies)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return out, nil
}

// UpdateABACPolicy replaces an existing policy's document.
func (s *qrdbServiceServer) UpdateABACPolicy(ctx context.Context, req *pb.ABACPolicy) (*pb.ABACPolicy, error) {
	if s.policySvc == nil {
		return nil, policyUnconfigured()
	}
	p, err := policyFromRequest(req)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return policyResponse(s.policySvc.UpdatePolicy(ctx, p))
}

// DeleteABACPolicy removes one policy.
func (s *qrdbServiceServer) DeleteABACPolicy(ctx context.Context, req *pb.ABACPolicyRequest) (*emptypb.Empty, error) {
	if s.policySvc == nil {
		return nil, policyUnconfigured()
	}
	if err := s.policySvc.DeletePolicy(ctx, req.GetTenantId(), req.GetName()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}
