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
	"encoding/base64"
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// batchTools returns the MCP tool registrations for batch operations.
func batchTools(batchSvc service.BatchServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolBatchExecute(),
			Perm:    permission.BatchExecute,
			Handler: handleBatchExecute(batchSvc),
		},
		{
			Tool:    toolImportBegin(),
			Perm:    permission.BatchExecute,
			Handler: handleImportBegin(batchSvc),
		},
		{
			Tool:    toolImportAppend(),
			Perm:    permission.BatchExecute,
			Handler: handleImportAppend(batchSvc),
		},
		{
			Tool:    toolImportCommit(),
			Perm:    permission.BatchExecute,
			Handler: handleImportCommit(batchSvc),
		},
		{
			Tool:    toolImportAbort(),
			Perm:    permission.BatchExecute,
			Handler: handleImportAbort(batchSvc),
		},
	}
}

// toolBatchExecute defines the qrdb_batch_execute tool schema.
func toolBatchExecute() mcp.Tool {
	return mcp.NewTool("qrdb_batch_execute",
		mcp.WithDescription("Execute multiple operations atomically within a single MVCC revision. All operations succeed or fail together."),
		mcp.WithString("ops",
			mcp.Required(),
			mcp.Description(`JSON array of operations. Each element: {"type":"put|get|delete|range","key":"<key>","value":"<base64>","range_end":"<base64>","lease_id":<int>}.`),
		),
	)
}

// handleBatchExecute returns a tool handler that delegates to
// batchSvc.Execute.
func handleBatchExecute(batchSvc service.BatchServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		batchSvc, rerr := resolveBatch(ctx, batchSvc)
		if rerr != nil {
			return nil, rerr
		}
		opsStr := mcp.ParseString(req, "ops", "")
		if opsStr == "" {
			return mcp.NewToolResultError("ops is required"), nil
		}

		ops, err := parseOps(opsStr)
		if err != nil {
			return mcp.NewToolResultError("invalid ops JSON: " + err.Error()), nil
		}

		if len(ops) == 0 {
			return mcp.NewToolResultError("ops must not be empty"), nil
		}

		// The result marshals as {"Revision": N, "Responses": [...]}, the
		// same field naming qrdb_tx_execute uses for its response.
		result, execErr := batchSvc.Execute(ctx, ops)
		if execErr != nil {
			return nil, &ToolError{Tool: "qrdb_batch_execute", Err: execErr}
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_batch_execute", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// Chunked import session tools
//
// See docs/transactions/architecture.md ("Import session"). MCP has no
// native binary framing -- every argument is JSON-RPC text -- so a chunk
// travels as base64 in a string argument, the same convention used for
// binary payloads elsewhere in the MCP surface (e.g. qrdb_backup_create's
// encryption_key). There is no separate pre-decode size gate here: the
// universal pre-auth request-body cap (transport.max_message_bytes)
// already bounds the whole call, chunk included, before this handler ever
// runs.
// ---------------------------------------------------------------------------

// toolImportBegin defines the qrdb_batch_import_begin tool schema.
func toolImportBegin() mcp.Tool {
	return mcp.NewTool("qrdb_batch_import_begin",
		mcp.WithDescription("Start a chunked import session for a dataset larger than one pre-auth message. Returns a session_id for qrdb_batch_import_append/_commit/_abort."),
		mcp.WithString("format",
			mcp.Required(),
			mcp.Description(`File format: "csv", "json", "ndjson", "yaml", "archive", or "auto".`),
		),
		mcp.WithString("filename",
			mcp.Description("Original filename, used for format auto-detection when format is \"auto\" or empty."),
		),
		mcp.WithString("key_field",
			mcp.Required(),
			mcp.Description("Field name to extract as the record key."),
		),
		mcp.WithString("value_mode",
			mcp.Required(),
			mcp.Description(`Value extraction mode: "field" uses value_field's value, "row" serializes the entire row as JSON.`),
		),
		mcp.WithString("value_field",
			mcp.Description("Field name for the value when value_mode is \"field\"."),
		),
		mcp.WithBoolean("skip_header",
			mcp.Description("Skip the first row (CSV only). Defaults to true."),
		),
		mcp.WithBoolean("transactional",
			mcp.Description("All-or-nothing import: any row error rolls back the entire import."),
		),
	)
}

// handleImportBegin returns a tool handler that delegates to
// batchSvc.ImportBegin.
func handleImportBegin(batchSvc service.BatchServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		batchSvc, rerr := resolveBatch(ctx, batchSvc)
		if rerr != nil {
			return nil, rerr
		}

		format := mcp.ParseString(req, "format", "")
		if format == "" {
			return mcp.NewToolResultError("format is required"), nil
		}
		keyField := mcp.ParseString(req, "key_field", "")
		if keyField == "" {
			return mcp.NewToolResultError("key_field is required"), nil
		}
		valueMode := mcp.ParseString(req, "value_mode", "")
		if valueMode == "" {
			return mcp.NewToolResultError("value_mode is required"), nil
		}

		opts := service.ImportOptions{
			Format:        format,
			Filename:      mcp.ParseString(req, "filename", ""),
			KeyField:      keyField,
			ValueMode:     valueMode,
			ValueField:    mcp.ParseString(req, "value_field", ""),
			SkipHeader:    mcp.ParseBoolean(req, "skip_header", true),
			Transactional: mcp.ParseBoolean(req, "transactional", false),
		}

		sessionID, err := batchSvc.ImportBegin(ctx, opts)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_batch_import_begin", Err: err}
		}

		data, jsonErr := json.Marshal(struct {
			SessionID string `json:"session_id"`
		}{SessionID: sessionID})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_batch_import_begin", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolImportAppend defines the qrdb_batch_import_append tool schema.
func toolImportAppend() mcp.Tool {
	return mcp.NewTool("qrdb_batch_import_append",
		mcp.WithDescription("Stage a base64-encoded chunk of a chunked import session. seq must be exactly one more than the last appended sequence, or equal to it with identical content (an idempotent replay)."),
		mcp.WithString("session_id",
			mcp.Required(),
			mcp.Description("The session ID returned by qrdb_batch_import_begin."),
		),
		mcp.WithNumber("seq",
			mcp.Required(),
			mcp.Description("The chunk's sequence number, starting at 0."),
		),
		mcp.WithString("chunk",
			mcp.Required(),
			mcp.Description("Base64-encoded chunk contents."),
		),
	)
}

// handleImportAppend returns a tool handler that decodes the base64 chunk
// argument and delegates to batchSvc.ImportAppend.
func handleImportAppend(batchSvc service.BatchServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		batchSvc, rerr := resolveBatch(ctx, batchSvc)
		if rerr != nil {
			return nil, rerr
		}

		sessionID := mcp.ParseString(req, "session_id", "")
		if sessionID == "" {
			return mcp.NewToolResultError("session_id is required"), nil
		}
		seq := mcp.ParseInt64(req, "seq", -1)
		if seq < 0 {
			return mcp.NewToolResultError("seq is required and must be >= 0"), nil
		}
		encoded := mcp.ParseString(req, "chunk", "")
		if encoded == "" {
			return mcp.NewToolResultError("chunk is required"), nil
		}

		chunk, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if decodeErr != nil {
			return mcp.NewToolResultError((&ImportFileDecodeError{Err: decodeErr}).Error()), nil
		}

		if err := batchSvc.ImportAppend(ctx, sessionID, uint64(seq), chunk); err != nil {
			return nil, &ToolError{Tool: "qrdb_batch_import_append", Err: err}
		}

		return mcp.NewToolResultText("{}"), nil
	}
}

// toolImportCommit defines the qrdb_batch_import_commit tool schema.
func toolImportCommit() mcp.Tool {
	return mcp.NewTool("qrdb_batch_import_commit",
		mcp.WithDescription("Apply every staged chunk of a chunked import session atomically, the same parsing and per-row semantics a single-message import would use, and release the session on success."),
		mcp.WithString("session_id",
			mcp.Required(),
			mcp.Description("The session ID returned by qrdb_batch_import_begin."),
		),
	)
}

// handleImportCommit returns a tool handler that delegates to
// batchSvc.ImportCommit.
func handleImportCommit(batchSvc service.BatchServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		batchSvc, rerr := resolveBatch(ctx, batchSvc)
		if rerr != nil {
			return nil, rerr
		}

		sessionID := mcp.ParseString(req, "session_id", "")
		if sessionID == "" {
			return mcp.NewToolResultError("session_id is required"), nil
		}

		result, err := batchSvc.ImportCommit(ctx, sessionID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_batch_import_commit", Err: err}
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_batch_import_commit", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolImportAbort defines the qrdb_batch_import_abort tool schema.
func toolImportAbort() mcp.Tool {
	return mcp.NewTool("qrdb_batch_import_abort",
		mcp.WithDescription("Discard a chunked import session -- its staged chunks and its lease -- without applying anything."),
		mcp.WithString("session_id",
			mcp.Required(),
			mcp.Description("The session ID returned by qrdb_batch_import_begin."),
		),
	)
}

// handleImportAbort returns a tool handler that delegates to
// batchSvc.ImportAbort.
func handleImportAbort(batchSvc service.BatchServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		batchSvc, rerr := resolveBatch(ctx, batchSvc)
		if rerr != nil {
			return nil, rerr
		}

		sessionID := mcp.ParseString(req, "session_id", "")
		if sessionID == "" {
			return mcp.NewToolResultError("session_id is required"), nil
		}

		if err := batchSvc.ImportAbort(ctx, sessionID); err != nil {
			return nil, &ToolError{Tool: "qrdb_batch_import_abort", Err: err}
		}

		return mcp.NewToolResultText("{}"), nil
	}
}
