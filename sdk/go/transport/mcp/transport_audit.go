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
	"errors"
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
	if f.Replicated {
		args["replicated"] = true
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

// VerifyChain verifies the audit log's tamper-evidence hash chain via the
// "qrdb_audit_verify" MCP tool. A non-nil error means verification could
// not be PERFORMED; a successful response with Verified=false is a TAMPER
// VERDICT, not an error -- see pkg/audit.Verify.
// Authorization: requires "audit:verify" permission.
func (t *Transport) VerifyChain(ctx context.Context) (*transport.AuditVerifyResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_audit_verify", map[string]any{})
	if err != nil {
		// Normalize to the same transport.NotImplementedError REST and gRPC
		// report for this condition (their 501 / codes.Unimplemented special
		// cases in transport_audit.go), rather than the generic
		// CapabilityUnsupportedError typedToolErrors reconstitutes for every
		// other CodeUnimplemented verb. The Tier-1 client contract for "this
		// node cannot verify its audit chain" must not depend on which
		// transport reached it.
		var unsupported *transport.CapabilityUnsupportedError
		if errors.As(err, &unsupported) {
			return nil, &transport.NotImplementedError{Method: "VerifyChain"}
		}
		return nil, err
	}

	var result transport.AuditVerifyResult
	if jsonErr := json.Unmarshal([]byte(text), &result); jsonErr != nil {
		return nil, &transport.RequestError{Method: "VerifyChain", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &result, nil
}

// VerifyReplicatedChain verifies the cluster-wide replicated audit chain's
// hash continuity for tenantID via the "qrdb_audit_verify_replicated" MCP
// tool. A non-nil error means verification could not be PERFORMED,
// including a tenant not entitled to the replicated chain; a successful
// response with Verified=false is a TAMPER VERDICT, not an error.
// Authorization: requires "audit:verify_replicated" permission.
func (t *Transport) VerifyReplicatedChain(ctx context.Context, tenantID string) (*transport.ChainContinuityResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{}
	if tenantID != "" {
		args["tenant_id"] = tenantID
	}

	text, err := t.callTool(ctx, "qrdb_audit_verify_replicated", args)
	if err != nil {
		var unsupported *transport.CapabilityUnsupportedError
		if errors.As(err, &unsupported) {
			return nil, &transport.NotImplementedError{Method: "VerifyReplicatedChain"}
		}
		return nil, err
	}

	var result transport.ChainContinuityResult
	if jsonErr := json.Unmarshal([]byte(text), &result); jsonErr != nil {
		return nil, &transport.RequestError{Method: "VerifyReplicatedChain", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &result, nil
}
