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
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// auditTools returns the MCP tool registrations for audit event retrieval:
// qrdb_audit_query (against the node-local trail, or the cluster-wide
// replicated chain when its "replicated" parameter is true and
// replicatedAudit is non-nil -- docs/audit/architecture.md
// #cluster-wide-replicated-trail), qrdb_audit_verify (node-local), and
// qrdb_audit_verify_replicated (registered only when replicatedAudit is
// non-nil). Authorization: requires "audit:query"/"audit:verify" permission.
// TODO: switch to permission.AuditQuery constant once Phase 2 lands.
func auditTools(l audit.AuditLogger, replicatedAudit *service.OptInAuditLogger) []serverTool {
	tools := []serverTool{
		{Tool: toolAuditQuery(), Perm: permission.AuditQuery, Handler: handleAuditQuery(l, replicatedAudit)},
		{Tool: toolAuditVerify(), Perm: permission.AuditVerify, Handler: handleAuditVerify(l)},
	}
	if replicatedAudit != nil {
		tools = append(tools, serverTool{
			Tool: toolAuditVerifyReplicated(), Perm: permission.AuditVerifyReplicated, Handler: handleAuditVerifyReplicated(replicatedAudit),
		})
	}
	return tools
}

// toolAuditQuery defines the qrdb_audit_query tool schema.
func toolAuditQuery() mcp.Tool {
	return mcp.NewTool("qrdb_audit_query",
		mcp.WithDescription("Query compliance-grade audit events. Requires audit:query permission."),
		mcp.WithString("from",
			mcp.Description("RFC3339 start time (inclusive). Example: 2026-01-01T00:00:00Z"),
		),
		mcp.WithString("to",
			mcp.Description("RFC3339 end time (exclusive)."),
		),
		mcp.WithString("actor",
			mcp.Description("Filter events by actor identity (cert CN, JWT sub)."),
		),
		mcp.WithString("resource",
			mcp.Description("Filter events by resource (e.g. 'barrier', 'user:alice')."),
		),
		mcp.WithString("action",
			mcp.Description("Filter events by action (e.g. 'initialize', 'unseal')."),
		),
		mcp.WithString("outcome",
			mcp.Description("Filter by outcome prefix: 'success' or 'failure'."),
		),
		mcp.WithString("tenant_id",
			mcp.Description("The caller's own tenant ID. Events are always confined to the caller's tenant; naming another tenant is refused."),
		),
		mcp.WithString("limit",
			mcp.Description("Maximum events per page (default 100, max 1000)."),
		),
		mcp.WithString("cursor",
			mcp.Description("Opaque pagination cursor from a previous response."),
		),
		mcp.WithBoolean("replicated",
			mcp.Description("Read the cluster-wide replicated audit chain instead of the node-local "+
				"trail. A tenant not entitled to it answers an empty page (total -1), never an error."),
		),
	)
}

// handleAuditQuery returns a tool handler that queries the audit logger,
// confined to the caller's own tenant (tenant.ConfineAuditFilter).
// replicated=true in the request reads replicatedAudit instead of l.
func handleAuditQuery(l audit.AuditLogger, replicatedAudit *service.OptInAuditLogger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		logger := l
		if mcp.ParseBoolean(req, "replicated", false) {
			if replicatedAudit == nil {
				return nil, &ToolError{Tool: "qrdb_audit_query", Err: &ServiceNotConfiguredError{Service: "replicated audit"}}
			}
			logger = replicatedAudit
		} else if logger == nil {
			return nil, &ToolError{Tool: "qrdb_audit_query", Err: &ServiceNotConfiguredError{Service: "audit"}}
		}

		f := audit.Filter{
			Actor:    mcp.ParseString(req, "actor", ""),
			Resource: mcp.ParseString(req, "resource", ""),
			Action:   mcp.ParseString(req, "action", ""),
			Outcome:  mcp.ParseString(req, "outcome", ""),
			TenantID: mcp.ParseString(req, "tenant_id", ""),
			Cursor:   mcp.ParseString(req, "cursor", ""),
		}

		if v := mcp.ParseString(req, "from", ""); v != "" {
			ts, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return mcp.NewToolResultError("invalid from timestamp: " + v), nil
			}
			f.From = ts
		}
		if v := mcp.ParseString(req, "to", ""); v != "" {
			ts, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return mcp.NewToolResultError("invalid to timestamp: " + v), nil
			}
			f.To = ts
		}
		if v := mcp.ParseString(req, "limit", ""); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return mcp.NewToolResultError("invalid limit: " + v), nil
			}
			f.Limit = n
		}

		f, err := tenant.ConfineAuditFilter(ctx, f)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_audit_query", Err: err}
		}

		page, err := logger.Query(ctx, f)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_audit_query", Err: err}
		}

		b, err := json.Marshal(page)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_audit_query", Err: err}
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}

// toolAuditVerify defines the qrdb_audit_verify tool schema.
func toolAuditVerify() mcp.Tool {
	return mcp.NewTool("qrdb_audit_verify",
		mcp.WithDescription("Verify the audit log's tamper-evidence hash chain across the active "+
			"file and every rotation archive. Requires audit:verify permission. A response with "+
			"\"verified\": false is a tamper verdict, not a tool failure."),
	)
}

// handleAuditVerify returns a tool handler that verifies the audit logger's
// hash chain. A non-nil error means verification could not be PERFORMED
// (unsupported backend, missing file, or a rotation race); a successful
// result with verified=false is a TAMPER VERDICT, not a failure -- see
// pkg/audit.Verify.
func handleAuditVerify(l audit.AuditLogger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		report, err := audit.Verify(ctx, l)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_audit_verify", Err: err}
		}

		b, err := json.Marshal(report)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_audit_verify", Err: err}
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}

// toolAuditVerifyReplicated defines the qrdb_audit_verify_replicated tool
// schema: hash-chain continuity for the cluster-wide replicated audit chain
// (docs/audit/architecture.md#cluster-wide-replicated-trail), for the
// caller's own tenant.
func toolAuditVerifyReplicated() mcp.Tool {
	return mcp.NewTool("qrdb_audit_verify_replicated",
		mcp.WithDescription("Verify the hash-chain continuity of the cluster-wide REPLICATED audit "+
			"chain for the caller's own tenant. Requires audit:verify_replicated permission. A response with "+
			"\"verified\": false is a tamper verdict, not a tool failure. A tenant not entitled to "+
			"the replicated chain is refused rather than answering a trivially-true report."),
		mcp.WithString("tenant_id",
			mcp.Description("The caller's own tenant ID. Omitted means the caller's own tenant; naming another tenant is refused."),
		),
	)
}

// handleAuditVerifyReplicated returns a tool handler that verifies one
// tenant's replicated hash-chain continuity, confined to the caller's own
// tenant (tenant.Confine).
func handleAuditVerifyReplicated(replicatedAudit *service.OptInAuditLogger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID, err := tenant.Confine(ctx, mcp.ParseString(req, "tenant_id", ""), "verify replicated audit chain")
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_audit_verify_replicated", Err: err}
		}

		report, err := replicatedAudit.VerifyTenantChain(ctx, tenantID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_audit_verify_replicated", Err: err}
		}

		b, err := json.Marshal(report)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_audit_verify_replicated", Err: err}
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}
