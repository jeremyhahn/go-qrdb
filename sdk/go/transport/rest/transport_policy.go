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

package rest

import (
	"context"
	"net/url"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.ABACPolicyClient = (*Transport)(nil)

// policyBasePath is the collection the /v1/abac/policies routes serve.
const policyBasePath = "/v1/abac/policies"

// CreateABACPolicy implements transport.ABACPolicyClient via
// POST /v1/abac/policies.
func (t *Transport) CreateABACPolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error) {
	if err := t.policyReady("CreateABACPolicy", policy == nil); err != nil {
		return nil, err
	}
	var stored abac.Policy
	if err := t.postJSONDecode(ctx, "CreateABACPolicy", policyBasePath, policy, &stored); err != nil {
		return nil, err
	}
	return &stored, nil
}

// GetABACPolicy implements transport.ABACPolicyClient via
// GET /v1/abac/policies/{name}.
func (t *Transport) GetABACPolicy(ctx context.Context, req *transport.ABACPolicyRequest) (*abac.Policy, error) {
	if err := t.policyReady("GetABACPolicy", req == nil); err != nil {
		return nil, err
	}
	var p abac.Policy
	if err := t.getJSONDecode(ctx, "GetABACPolicy", policyPath(req.Name, req.TenantID), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ListABACPolicies implements transport.ABACPolicyClient via
// GET /v1/abac/policies.
func (t *Transport) ListABACPolicies(ctx context.Context, req *transport.ABACPolicyListRequest) (*transport.ABACPolicyListResponse, error) {
	if err := t.policyReady("ListABACPolicies", req == nil); err != nil {
		return nil, err
	}
	path := policyBasePath
	if req.TenantID != "" {
		path += "?tenant_id=" + url.QueryEscape(req.TenantID)
	}
	var resp transport.ABACPolicyListResponse
	if err := t.getJSONDecode(ctx, "ListABACPolicies", path, &resp); err != nil {
		return nil, err
	}
	if resp.Policies == nil {
		resp.Policies = []*abac.Policy{}
	}
	return &resp, nil
}

// UpdateABACPolicy implements transport.ABACPolicyClient via
// PUT /v1/abac/policies/{name}, the name taken from metadata.name.
func (t *Transport) UpdateABACPolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error) {
	if err := t.policyReady("UpdateABACPolicy", policy == nil); err != nil {
		return nil, err
	}
	var stored abac.Policy
	if err := t.putJSONDecode(ctx, "UpdateABACPolicy", policyPath(policy.Metadata.Name, ""), policy, &stored); err != nil {
		return nil, err
	}
	return &stored, nil
}

// DeleteABACPolicy implements transport.ABACPolicyClient via
// DELETE /v1/abac/policies/{name}.
func (t *Transport) DeleteABACPolicy(ctx context.Context, req *transport.ABACPolicyRequest) error {
	if err := t.policyReady("DeleteABACPolicy", req == nil); err != nil {
		return err
	}
	return t.deleteNoContent(ctx, "DeleteABACPolicy", policyPath(req.Name, req.TenantID))
}

// policyReady refuses a call on a closed transport, then a nil request.
func (t *Transport) policyReady(method string, nilRequest bool) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if nilRequest {
		return &transport.RequestError{Method: method, Err: &transport.NilRequestError{}}
	}
	return nil
}

// policyPath is the route of one named policy, with the tenant_id query
// parameter when a tenant is named.
func policyPath(name, tenantID string) string {
	path := policyBasePath + "/" + url.PathEscape(name)
	if tenantID != "" {
		path += "?tenant_id=" + url.QueryEscape(tenantID)
	}
	return path
}
