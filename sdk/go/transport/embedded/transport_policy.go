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

	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.ABACPolicyClient = (*Transport)(nil)

// ABACPolicyClient implementation: direct calls into the injected
// service.PolicyServicer. The tenant acted on is the one bound to ctx (see
// sdk.ContextWithTenant), or the system tenant when none is bound; a request
// naming any other tenant is refused by the service.

// CreateABACPolicy implements transport.ABACPolicyClient.
func (t *Transport) CreateABACPolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error) {
	ctx, err := t.policyReady(ctx, "CreateABACPolicy", policy == nil)
	if err != nil {
		return nil, err
	}
	return t.policySvc.CreatePolicy(ctx, policy)
}

// GetABACPolicy implements transport.ABACPolicyClient.
func (t *Transport) GetABACPolicy(ctx context.Context, req *transport.ABACPolicyRequest) (*abac.Policy, error) {
	ctx, err := t.policyReady(ctx, "GetABACPolicy", req == nil)
	if err != nil {
		return nil, err
	}
	return t.policySvc.GetPolicy(ctx, req.TenantID, req.Name)
}

// ListABACPolicies implements transport.ABACPolicyClient.
func (t *Transport) ListABACPolicies(ctx context.Context, req *transport.ABACPolicyListRequest) (*transport.ABACPolicyListResponse, error) {
	ctx, err := t.policyReady(ctx, "ListABACPolicies", req == nil)
	if err != nil {
		return nil, err
	}
	policies, err := t.policySvc.ListPolicies(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	if policies == nil {
		policies = []*abac.Policy{}
	}
	return &transport.ABACPolicyListResponse{Policies: policies}, nil
}

// UpdateABACPolicy implements transport.ABACPolicyClient.
func (t *Transport) UpdateABACPolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error) {
	ctx, err := t.policyReady(ctx, "UpdateABACPolicy", policy == nil)
	if err != nil {
		return nil, err
	}
	return t.policySvc.UpdatePolicy(ctx, policy)
}

// DeleteABACPolicy implements transport.ABACPolicyClient.
func (t *Transport) DeleteABACPolicy(ctx context.Context, req *transport.ABACPolicyRequest) error {
	ctx, err := t.policyReady(ctx, "DeleteABACPolicy", req == nil)
	if err != nil {
		return err
	}
	return t.policySvc.DeletePolicy(ctx, req.TenantID, req.Name)
}

// policyReady runs the checks every policy method shares, in the order the
// other embedded methods run them: the sealed-window gate, the caller's
// permission, then a closed transport, an unwired service and a nil
// request.
func (t *Transport) policyReady(ctx context.Context, method string, nilRequest bool) (context.Context, error) {
	ctx, err := t.admit(ctx, method)
	if err != nil {
		return ctx, err
	}
	if err := t.authorize(ctx, method); err != nil {
		return ctx, err
	}
	if t.closed.Load() {
		return ctx, &transport.ClosedError{}
	}
	if t.policySvc == nil {
		return ctx, &NilServiceError{Service: "PolicyServicer"}
	}
	if nilRequest {
		return ctx, &transport.RequestError{Method: method, Err: &transport.NilRequestError{}}
	}
	return ctx, nil
}

// WithPolicyService injects the tenant ABAC policy store. Without it every
// ABACPolicyClient method returns *NilServiceError.
func WithPolicyService(svc service.PolicyServicer) Option {
	return func(t *Transport) {
		t.policySvc = svc
	}
}
