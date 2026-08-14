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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// maxImportFileSize is the maximum size for imported files (100MB),
// mirroring the REST /batch/import bound. Unlike REST (bounded by
// http.MaxBytesReader) and gRPC (bounded by grpc.MaxRecvMsgSize), the MCP
// HTTP transport has no framework-level request size cap, so this handler
// enforces the bound itself -- both on the base64-encoded "file" argument
// (before decoding, see maxEncodedImportFileSize) and again on the decoded
// result.
const maxImportFileSize = 100 << 20

// maxEncodedImportFileSize is the largest base64-encoded "file" argument
// handleBatchImport will attempt to decode. Rejecting an oversized encoded
// payload BEFORE decoding avoids allocating a full decoded buffer for input
// that was always going to be rejected: base64.StdEncoding.DecodeString
// allocates its entire output up front, so checking len(file) only AFTER
// decoding lets an attacker's multi-GB base64 argument OOM the process
// before the size gate ever runs.
var maxEncodedImportFileSize = base64.StdEncoding.EncodedLen(maxImportFileSize)

// batchTools returns the MCP tool registrations for batch operations.
func batchTools(batchSvc service.BatchServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolBatchExecute(),
			Perm:    permission.BatchExecute,
			Handler: handleBatchExecute(batchSvc),
		},
		{
			Tool:    toolBatchImport(),
			Perm:    permission.BatchExecute,
			Handler: handleBatchImport(batchSvc),
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
			return mcp.NewToolResultError(rerr.Error()), nil
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

		responses, execErr := batchSvc.Execute(ctx, ops)
		if execErr != nil {
			return nil, &ToolError{Tool: "qrdb_batch_execute", Err: execErr}
		}

		data, jsonErr := json.Marshal(responses)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_batch_execute", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolBatchImport defines the qrdb_batch_import tool schema. The file
// contents are carried as a base64-encoded string argument, the same
// convention used for binary payloads elsewhere in the MCP surface (e.g.
// qrdb_backup_create's encryption_key).
func toolBatchImport() mcp.Tool {
	return mcp.NewTool("qrdb_batch_import",
		mcp.WithDescription("Parse and import key-value pairs from a base64-encoded CSV, JSON, NDJSON, YAML, or archive file. This is a bounded, non-streaming call; the decoded file must not exceed 100MB."),
		mcp.WithString("file",
			mcp.Required(),
			mcp.Description("Base64-encoded file contents."),
		),
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

// handleBatchImport returns a tool handler that decodes the base64 file
// argument and delegates to batchSvc.Import.
func handleBatchImport(batchSvc service.BatchServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		batchSvc, rerr := resolveBatch(ctx, batchSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}

		encoded := mcp.ParseString(req, "file", "")
		if encoded == "" {
			return mcp.NewToolResultError("file is required"), nil
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

		// Reject an oversized encoded payload before it is ever decoded --
		// see maxEncodedImportFileSize.
		if len(encoded) > maxEncodedImportFileSize {
			return mcp.NewToolResultError((&ImportFileTooLargeError{Size: len(encoded), Max: maxEncodedImportFileSize}).Error()), nil
		}

		// Decode through a bounded streaming reader (rather than
		// base64.StdEncoding.DecodeString, which allocates its entire
		// decoded output up front) as defense in depth: the encoded-length
		// check above already rejects any input whose base64 form implies
		// a decoded size over maxImportFileSize, but base64 padding/newline
		// variance means it is not an exact bound, so this ensures decoding
		// itself can never allocate more than maxImportFileSize+1 bytes.
		dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
		file, decodeErr := io.ReadAll(io.LimitReader(dec, int64(maxImportFileSize)+1))
		if decodeErr != nil {
			return mcp.NewToolResultError((&ImportFileDecodeError{Err: decodeErr}).Error()), nil
		}
		if len(file) > maxImportFileSize {
			return mcp.NewToolResultError((&ImportFileTooLargeError{Size: len(file), Max: maxImportFileSize}).Error()), nil
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

		result, importErr := batchSvc.Import(ctx, bytes.NewReader(file), opts)
		if importErr != nil {
			return nil, &ToolError{Tool: "qrdb_batch_import", Err: importErr}
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_batch_import", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}
