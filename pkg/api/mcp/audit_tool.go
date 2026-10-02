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
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// auditTools returns the MCP tool registrations for audit event retrieval.
// Authorization: requires "audit:query" permission.
// TODO: switch to permission.AuditQuery constant once Phase 2 lands.
func auditTools(l audit.AuditLogger) []serverTool {
	return []serverTool{
		{Tool: toolAuditQuery(), Perm: permission.AuditQuery, Handler: handleAuditQuery(l)},
	}
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
	)
}

// handleAuditQuery returns a tool handler that queries the audit logger,
// confined to the caller's own tenant (tenant.ConfineAuditFilter).
func handleAuditQuery(l audit.AuditLogger) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

		page, err := l.Query(ctx, f)
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
