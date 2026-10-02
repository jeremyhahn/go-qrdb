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

// transport_audit.go wires the MCP transport to the server's
// "qrdb_audit_query" tool (registered in pkg/api/mcp/server.go only when an
// AuditLogger is configured -- see pkg/api/mcp/audit_tool.go). The tool
// marshals pkg/audit.Page directly as its JSON result text, and
// transport.AuditPage / transport.AuditEvent carry the same JSON tags, so
// the response text decodes straight into the SDK type.
package mcp

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/auditconv"
)

// Compile-time interface assertions.
var (
	_ transport.AuditClient   = (*Transport)(nil)
	_ transport.AuditProvider = (*Transport)(nil)
)

// AuditClient returns the MCP transport's own AuditClient projection.
// Implements transport.AuditProvider.
func (t *Transport) AuditClient() transport.AuditClient {
	return t
}

// EmitAudit is not supported over the MCP transport: audit events are
// emitted server-side by the request handlers themselves, and the server
// registers no audit-emit tool. Remote callers query audit via QueryAudit;
// emission belongs to the in-process (embedded) transport.
func (t *Transport) EmitAudit(ctx context.Context, ev transport.AuditEvent) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return &transport.NotImplementedError{Method: "EmitAudit"}
}

// QueryAudit queries the audit log via the "qrdb_audit_query" MCP tool.
// Authorization: requires "audit:query" permission.
func (t *Transport) QueryAudit(ctx context.Context, f transport.AuditFilter) (*transport.AuditPage, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{}
	if !f.From.IsZero() {
		args["from"] = f.From.UTC().Format(time.RFC3339)
	}
	if !f.To.IsZero() {
		args["to"] = f.To.UTC().Format(time.RFC3339)
	}
	if f.Actor != "" {
		args["actor"] = f.Actor
	}
	if f.Resource != "" {
		args["resource"] = f.Resource
	}
	if f.Action != "" {
		args["action"] = f.Action
	}
	if f.Outcome != "" {
		args["outcome"] = f.Outcome
	}
	if f.TenantID != "" {
		args["tenant_id"] = f.TenantID
	}
	if f.Limit > 0 {
		args["limit"] = strconv.Itoa(f.Limit)
	}
	if f.Cursor != "" {
		args["cursor"] = f.Cursor
	}

	text, err := t.callTool(ctx, "qrdb_audit_query", args)
	if err != nil {
		return nil, err
	}

	// The tool result is an audit.Page; decoding it as transport.AuditPage,
	// whose field names differ, dropped each event's tenant, client IP,
	// request id and chain hash.
	var page audit.Page
	if jsonErr := json.Unmarshal([]byte(text), &page); jsonErr != nil {
		return nil, &transport.RequestError{Method: "QueryAudit", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return auditconv.PageToTransport(&page), nil
}
