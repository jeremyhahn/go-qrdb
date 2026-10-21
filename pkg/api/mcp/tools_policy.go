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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// policyTenantArg describes the optional tenant_id argument every policy
// tool takes: the policy API acts on the caller's own tenant, and naming
// another one is refused.
const policyTenantArg = "Tenant whose policies are addressed. Defaults to the caller's own tenant; naming another tenant is refused."

// policyDocumentArg describes the policy argument of create and update.
const policyDocumentArg = "The ABAC policy document (apiVersion, kind, metadata, spec) as a JSON string, the same document the REST API takes."

// policyListResult is the qrdb_abac_policy_list result, the shape of the
// REST list response.
type policyListResult struct {
	Policies []*abac.Policy `json:"policies"`
}

// policyTools returns the qrdb_abac_policy_* tools over the tenant ABAC
// policy store.
func policyTools(svc service.PolicyServicer) []serverTool {
	return []serverTool{
		{Tool: toolPolicyCreate(), Perm: permission.ABACPoliciesCreate, Handler: handlePolicyCreate(svc)},
		{Tool: toolPolicyGet(), Perm: permission.ABACPoliciesRead, Handler: handlePolicyGet(svc)},
		{Tool: toolPolicyList(), Perm: permission.ABACPoliciesList, Handler: handlePolicyList(svc)},
		{Tool: toolPolicyUpdate(), Perm: permission.ABACPoliciesUpdate, Handler: handlePolicyUpdate(svc)},
		{Tool: toolPolicyDelete(), Perm: permission.ABACPoliciesDelete, Handler: handlePolicyDelete(svc)},
	}
}

func toolPolicyCreate() mcp.Tool {
	return mcp.NewTool("qrdb_abac_policy_create",
		mcp.WithDescription("Create an ABAC policy in the caller's tenant. Refuses a name the tenant already holds. Returns the stored document."),
		mcp.WithString("policy", mcp.Required(), mcp.Description(policyDocumentArg)),
	)
}

func handlePolicyCreate(svc service.PolicyServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, failure, err := policyArg(req, "qrdb_abac_policy_create")
		if failure != nil || err != nil {
			return failure, err
		}
		stored, err := svc.CreatePolicy(ctx, p)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_abac_policy_create", Err: err}
		}
		return toolJSONResult("qrdb_abac_policy_create", stored)
	}
}

func toolPolicyGet() mcp.Tool {
	return mcp.NewTool("qrdb_abac_policy_get",
		mcp.WithDescription("Return one stored ABAC policy document by name."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Policy name.")),
		mcp.WithString("tenant_id", mcp.Description(policyTenantArg)),
	)
}

func handlePolicyGet(svc service.PolicyServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError((&RequiredParameterError{Parameter: "name"}).Error()), nil
		}
		p, err := svc.GetPolicy(ctx, mcp.ParseString(req, "tenant_id", ""), name)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_abac_policy_get", Err: err}
		}
		return toolJSONResult("qrdb_abac_policy_get", p)
	}
}

func toolPolicyList() mcp.Tool {
	return mcp.NewTool("qrdb_abac_policy_list",
		mcp.WithDescription("List every ABAC policy document the tenant holds, sorted by name."),
		mcp.WithString("tenant_id", mcp.Description(policyTenantArg)),
	)
}

func handlePolicyList(svc service.PolicyServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		policies, err := svc.ListPolicies(ctx, mcp.ParseString(req, "tenant_id", ""))
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_abac_policy_list", Err: err}
		}
		if policies == nil {
			policies = []*abac.Policy{}
		}
		return toolJSONResult("qrdb_abac_policy_list", policyListResult{Policies: policies})
	}
}

func toolPolicyUpdate() mcp.Tool {
	return mcp.NewTool("qrdb_abac_policy_update",
		mcp.WithDescription("Replace an existing ABAC policy's document, named by its metadata.name, keeping its creation time. Returns the stored document."),
		mcp.WithString("policy", mcp.Required(), mcp.Description(policyDocumentArg)),
	)
}

func handlePolicyUpdate(svc service.PolicyServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, failure, err := policyArg(req, "qrdb_abac_policy_update")
		if failure != nil || err != nil {
			return failure, err
		}
		stored, err := svc.UpdatePolicy(ctx, p)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_abac_policy_update", Err: err}
		}
		return toolJSONResult("qrdb_abac_policy_update", stored)
	}
}

func toolPolicyDelete() mcp.Tool {
	return mcp.NewTool("qrdb_abac_policy_delete",
		mcp.WithDescription("Delete one ABAC policy by name."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Policy name.")),
		mcp.WithString("tenant_id", mcp.Description(policyTenantArg)),
	)
}

func handlePolicyDelete(svc service.PolicyServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError((&RequiredParameterError{Parameter: "name"}).Error()), nil
		}
		if err := svc.DeletePolicy(ctx, mcp.ParseString(req, "tenant_id", ""), name); err != nil {
			return nil, &ToolError{Tool: "qrdb_abac_policy_delete", Err: err}
		}
		return mcp.NewToolResultText(`{"deleted":true}`), nil
	}
}

// policyArg decodes the policy argument of create and update. A missing
// argument is a tool-result error, like every other required argument; a
// document that does not decode is the service's own *PolicyInvalidError,
// so it carries the InvalidArgument classification it carries on every
// other transport.
func policyArg(req mcp.CallToolRequest, tool string) (*abac.Policy, *mcp.CallToolResult, error) {
	raw := mcp.ParseString(req, "policy", "")
	if raw == "" {
		return nil, mcp.NewToolResultError((&RequiredParameterError{Parameter: "policy"}).Error()), nil
	}
	var p abac.Policy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, nil, &ToolError{Tool: tool, Err: &service.PolicyInvalidError{Reason: "decode", Err: err}}
	}
	return &p, nil, nil
}
