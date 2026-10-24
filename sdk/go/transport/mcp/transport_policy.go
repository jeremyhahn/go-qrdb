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

package mcp

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.ABACPolicyClient = (*Transport)(nil)

// ABACPolicyClient implementation over the qrdb_abac_policy_* tools served by
// pkg/api/mcp/tools_policy.go. Create and update pass the policy document as
// a JSON string; each tool answers with the same JSON body the REST
// transport returns.

// CreateABACPolicy stores a new policy and returns the stored document.
func (t *Transport) CreateABACPolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error) {
	return t.policyDocument(ctx, "CreateABACPolicy", "qrdb_abac_policy_create", policy)
}

// GetABACPolicy returns one stored policy.
func (t *Transport) GetABACPolicy(ctx context.Context, req *transport.ABACPolicyRequest) (*abac.Policy, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "GetABACPolicy", Err: &transport.NilRequestError{}}
	}
	var p abac.Policy
	if err := t.jsonTool(ctx, "GetABACPolicy", "qrdb_abac_policy_get", policyNameArgs(req), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ListABACPolicies returns every policy the tenant holds.
func (t *Transport) ListABACPolicies(ctx context.Context, req *transport.ABACPolicyListRequest) (*transport.ABACPolicyListResponse, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "ListABACPolicies", Err: &transport.NilRequestError{}}
	}
	var resp transport.ABACPolicyListResponse
	if err := t.jsonTool(ctx, "ListABACPolicies", "qrdb_abac_policy_list", tenantArgs(req.TenantID), &resp); err != nil {
		return nil, err
	}
	if resp.Policies == nil {
		resp.Policies = []*abac.Policy{}
	}
	return &resp, nil
}

// UpdateABACPolicy replaces an existing policy's document.
func (t *Transport) UpdateABACPolicy(ctx context.Context, policy *abac.Policy) (*abac.Policy, error) {
	return t.policyDocument(ctx, "UpdateABACPolicy", "qrdb_abac_policy_update", policy)
}

// DeleteABACPolicy removes one policy.
func (t *Transport) DeleteABACPolicy(ctx context.Context, req *transport.ABACPolicyRequest) error {
	if req == nil {
		return &transport.RequestError{Method: "DeleteABACPolicy", Err: &transport.NilRequestError{}}
	}
	var deleted struct {
		Deleted bool `json:"deleted"`
	}
	return t.jsonTool(ctx, "DeleteABACPolicy", "qrdb_abac_policy_delete", policyNameArgs(req), &deleted)
}

// policyDocument issues a create or update: both take the document as a JSON
// string argument and answer with the stored document.
func (t *Transport) policyDocument(ctx context.Context, method, tool string, policy *abac.Policy) (*abac.Policy, error) {
	if policy == nil {
		return nil, &transport.RequestError{Method: method, Err: &transport.NilRequestError{}}
	}
	doc, err := json.Marshal(policy)
	if err != nil {
		return nil, &transport.RequestError{Method: method, Err: &jsonEncodeError{Err: err}}
	}
	var stored abac.Policy
	if err := t.jsonTool(ctx, method, tool, map[string]any{"policy": string(doc)}, &stored); err != nil {
		return nil, err
	}
	return &stored, nil
}

// policyNameArgs is the argument map of the tools naming one policy.
func policyNameArgs(req *transport.ABACPolicyRequest) map[string]any {
	args := tenantArgs(req.TenantID)
	args["name"] = req.Name
	return args
}
