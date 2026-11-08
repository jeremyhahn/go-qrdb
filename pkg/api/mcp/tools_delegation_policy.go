// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

const (
	toolNameDelegationPolicyPut    = "qrdb_cert_delegation_policy_put"
	toolNameDelegationPolicyList   = "qrdb_cert_delegation_policy_list"
	toolNameDelegationPolicyDelete = "qrdb_cert_delegation_policy_delete"
)

// delegationPolicyResult is a tenant's standing delegation policy as a tool
// returns it; TTL is its lifetime as a Go duration.
type delegationPolicyResult struct {
	TenantID  string   `json:"tenantID"`
	Principal string   `json:"principal"`
	Scope     []string `json:"scope"`
	TTL       string   `json:"ttl"`
}

func delegationPolicyToResult(p *delegation.Policy) delegationPolicyResult {
	return delegationPolicyResult{TenantID: p.Tenant, Principal: p.Principal, Scope: p.Scope, TTL: p.Lifetime().String()}
}

// toolDelegationPolicyPut defines the qrdb_cert_delegation_policy_put tool.
func toolDelegationPolicyPut() mcp.Tool {
	return mcp.NewTool(toolNameDelegationPolicyPut,
		mcp.WithDescription("Write your tenant's standing delegation policy for a service principal: while it stands, the node mints that principal short-lived delegation certificates from your tenant's CA, on its own request, within the scope and lifetime. Delete it to revoke it."),
		mcp.WithString("principal", mcp.Required(), mcp.Description("The service principal the policy names.")),
		mcp.WithArray("scope", mcp.Required(), mcp.WithStringItems(), mcp.Description("The most a minted certificate may delegate, e.g. kv:get.")),
		mcp.WithString("ttl", mcp.Description(`The longest a minted certificate may live, as a Go duration (e.g. "30m"). Empty is one hour; more than 24h is refused.`)),
	)
}

// toolDelegationPolicyList defines the qrdb_cert_delegation_policy_list tool.
func toolDelegationPolicyList() mcp.Tool {
	return mcp.NewTool(toolNameDelegationPolicyList,
		mcp.WithDescription("List your tenant's standing delegation policies."),
	)
}

// toolDelegationPolicyDelete defines the qrdb_cert_delegation_policy_delete
// tool.
func toolDelegationPolicyDelete() mcp.Tool {
	return mcp.NewTool(toolNameDelegationPolicyDelete,
		mcp.WithDescription("Revoke your tenant's standing delegation policy for a service principal: nothing is minted under it again."),
		mcp.WithString("principal", mcp.Required(), mcp.Description("The service principal the policy names.")),
	)
}

// delegationPolicyTenant authorizes certs:delegate in the caller's own tenant
// and returns it.
func delegationPolicyTenant(ctx context.Context, mw *authmw.AuthMiddleware, tool string) (string, error) {
	tenantID := callerTenant(ctx)
	if err := authorizeCertTenantTool(ctx, mw, tenantID, permission.CertsDelegate); err != nil {
		return "", &ToolError{Tool: tool, Err: err}
	}
	return tenantID, nil
}

func jsonToolResult(tool string, v any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	return mcp.NewToolResultText(string(data)), nil
}

func handleDelegationPolicyPut(svc service.CertEnrollmentServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var ttl time.Duration
		if s := mcp.ParseString(req, "ttl", ""); s != "" {
			d, err := time.ParseDuration(s)
			if err != nil {
				return mcp.NewToolResultError("invalid ttl duration: " + err.Error()), nil
			}
			ttl = d
		}
		tenantID, err := delegationPolicyTenant(ctx, mw, toolNameDelegationPolicyPut)
		if err != nil {
			return nil, err
		}
		p, err := svc.PutDelegationPolicy(ctx, &service.DelegationPolicyRequest{
			TenantID:  tenantID,
			Principal: mcp.ParseString(req, "principal", ""),
			Scope:     req.GetStringSlice("scope", nil),
			TTL:       ttl,
		})
		if err != nil {
			return nil, &ToolError{Tool: toolNameDelegationPolicyPut, Err: err}
		}
		return jsonToolResult(toolNameDelegationPolicyPut, delegationPolicyToResult(p))
	}
}

func handleDelegationPolicyList(svc service.CertEnrollmentServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID, err := delegationPolicyTenant(ctx, mw, toolNameDelegationPolicyList)
		if err != nil {
			return nil, err
		}
		policies, err := svc.ListDelegationPolicies(ctx, tenantID)
		if err != nil {
			return nil, &ToolError{Tool: toolNameDelegationPolicyList, Err: err}
		}
		out := struct {
			Policies []delegationPolicyResult `json:"policies"`
		}{Policies: make([]delegationPolicyResult, len(policies))}
		for i := range policies {
			out.Policies[i] = delegationPolicyToResult(&policies[i])
		}
		return jsonToolResult(toolNameDelegationPolicyList, out)
	}
}

func handleDelegationPolicyDelete(svc service.CertEnrollmentServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID, err := delegationPolicyTenant(ctx, mw, toolNameDelegationPolicyDelete)
		if err != nil {
			return nil, err
		}
		if err := svc.DeleteDelegationPolicy(ctx, tenantID, mcp.ParseString(req, "principal", "")); err != nil {
			return nil, &ToolError{Tool: toolNameDelegationPolicyDelete, Err: err}
		}
		return jsonToolResult(toolNameDelegationPolicyDelete, struct{}{})
	}
}
