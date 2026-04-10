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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mvccTools returns the MCP tool registrations for MVCC key-value operations.
func mvccTools(mvccSvc service.MVCCKVServicer) []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{
			Tool:    toolMVCCPutWithLease(),
			Handler: handleMVCCPutWithLease(mvccSvc),
		},
		{
			Tool:    toolMVCCGetRevision(),
			Handler: handleMVCCGetRevision(mvccSvc),
		},
		{
			Tool:    toolMVCCGetAtRevision(),
			Handler: handleMVCCGetAtRevision(mvccSvc),
		},
		{
			Tool:    toolMVCCRange(),
			Handler: handleMVCCRange(mvccSvc),
		},
		{
			Tool:    toolMVCCCompact(),
			Handler: handleMVCCCompact(mvccSvc),
		},
		{
			Tool:    toolMVCCCurrentRevision(),
			Handler: handleMVCCCurrentRevision(mvccSvc),
		},
	}
}

// toolMVCCPutWithLease defines the qrdb_mvcc_put_with_lease tool schema.
func toolMVCCPutWithLease() mcp.Tool {
	return mcp.NewTool("qrdb_mvcc_put_with_lease",
		mcp.WithDescription("Store a key-value pair with an attached lease through MVCC-aware Raft consensus. When the lease expires, the key is automatically deleted."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to store."),
		),
		mcp.WithString("value",
			mcp.Required(),
			mcp.Description("The value to store (UTF-8 text)."),
		),
		mcp.WithNumber("lease_id",
			mcp.Required(),
			mcp.Description("The lease ID to attach to the key."),
		),
	)
}

// handleMVCCPutWithLease returns a tool handler that delegates to
// mvccSvc.PutWithLease.
func handleMVCCPutWithLease(mvccSvc service.MVCCKVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}
		value := mcp.ParseString(req, "value", "")
		if value == "" {
			return mcp.NewToolResultError("value is required"), nil
		}
		leaseID := mcp.ParseInt64(req, "lease_id", 0)

		kv, err := mvccSvc.PutWithLease(ctx, key, []byte(value), leaseID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_put_with_lease", Err: err}
		}

		data, jsonErr := json.Marshal(kv)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_put_with_lease", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolMVCCGetRevision defines the qrdb_mvcc_get_revision tool schema.
func toolMVCCGetRevision() mcp.Tool {
	return mcp.NewTool("qrdb_mvcc_get_revision",
		mcp.WithDescription("Retrieve a key-value pair with full MVCC revision metadata at the latest revision."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to retrieve."),
		),
	)
}

// handleMVCCGetRevision returns a tool handler that delegates to
// mvccSvc.GetRevision.
func handleMVCCGetRevision(mvccSvc service.MVCCKVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}

		kv, err := mvccSvc.GetRevision(ctx, key)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_get_revision", Err: err}
		}

		data, jsonErr := json.Marshal(kv)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_get_revision", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolMVCCGetAtRevision defines the qrdb_mvcc_get_at_revision tool schema.
func toolMVCCGetAtRevision() mcp.Tool {
	return mcp.NewTool("qrdb_mvcc_get_at_revision",
		mcp.WithDescription("Retrieve a key-value pair as it existed at a specific historical MVCC revision."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to retrieve."),
		),
		mcp.WithNumber("revision",
			mcp.Required(),
			mcp.Description("The historical MVCC revision to read at."),
		),
	)
}

// handleMVCCGetAtRevision returns a tool handler that delegates to
// mvccSvc.GetAtRevision.
func handleMVCCGetAtRevision(mvccSvc service.MVCCKVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}
		revision := mcp.ParseInt64(req, "revision", 0)
		if revision == 0 {
			return mcp.NewToolResultError("revision is required"), nil
		}

		kv, err := mvccSvc.GetAtRevision(ctx, key, revision)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_get_at_revision", Err: err}
		}

		data, jsonErr := json.Marshal(kv)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_get_at_revision", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolMVCCRange defines the qrdb_mvcc_range tool schema.
func toolMVCCRange() mcp.Tool {
	return mcp.NewTool("qrdb_mvcc_range",
		mcp.WithDescription("Retrieve key-value pairs in the half-open interval [start, end) with MVCC revision metadata."),
		mcp.WithString("start",
			mcp.Required(),
			mcp.Description("The start key of the range (inclusive)."),
		),
		mcp.WithString("end",
			mcp.Required(),
			mcp.Description("The end key of the range (exclusive)."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of key-values to return. 0 for unlimited."),
		),
		mcp.WithNumber("revision",
			mcp.Description("Read key-values at this historical revision. 0 for latest."),
		),
		mcp.WithBoolean("count_only",
			mcp.Description("Return only the count of matching keys without values."),
		),
		mcp.WithBoolean("keys_only",
			mcp.Description("Return keys without their values."),
		),
	)
}

// mvccRangeResponse is the JSON envelope for MVCC range results.
type mvccRangeResponse struct {
	KeyValues json.RawMessage `json:"key_values"`
	Count     int64           `json:"count"`
}

// handleMVCCRange returns a tool handler that delegates to mvccSvc.Range.
func handleMVCCRange(mvccSvc service.MVCCKVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := mcp.ParseString(req, "start", "")
		if start == "" {
			return mcp.NewToolResultError("start is required"), nil
		}
		end := mcp.ParseString(req, "end", "")
		if end == "" {
			return mcp.NewToolResultError("end is required"), nil
		}

		var opts []service.RangeOption
		if limit := mcp.ParseInt64(req, "limit", 0); limit > 0 {
			opts = append(opts, service.WithLimit(limit))
		}
		if revision := mcp.ParseInt64(req, "revision", 0); revision > 0 {
			opts = append(opts, service.WithRevision(revision))
		}
		if mcp.ParseBoolean(req, "count_only", false) {
			opts = append(opts, service.WithCountOnly())
		}
		if mcp.ParseBoolean(req, "keys_only", false) {
			opts = append(opts, service.WithKeysOnly())
		}

		kvs, count, err := mvccSvc.Range(ctx, []byte(start), []byte(end), opts...)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_range", Err: err}
		}

		kvsJSON, jsonErr := json.Marshal(kvs)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_range", Err: jsonErr}
		}

		resp := mvccRangeResponse{
			KeyValues: kvsJSON,
			Count:     count,
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_range", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolMVCCCompact defines the qrdb_mvcc_compact tool schema.
func toolMVCCCompact() mcp.Tool {
	return mcp.NewTool("qrdb_mvcc_compact",
		mcp.WithDescription("Discard all MVCC revisions older than the specified revision to reclaim storage."),
		mcp.WithNumber("revision",
			mcp.Required(),
			mcp.Description("Compact all revisions older than this value."),
		),
	)
}

// handleMVCCCompact returns a tool handler that delegates to
// mvccSvc.Compact.
func handleMVCCCompact(mvccSvc service.MVCCKVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		revision := mcp.ParseInt64(req, "revision", 0)
		if revision == 0 {
			return mcp.NewToolResultError("revision is required"), nil
		}

		if err := mvccSvc.Compact(ctx, revision); err != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_compact", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolMVCCCurrentRevision defines the qrdb_mvcc_current_revision tool schema.
func toolMVCCCurrentRevision() mcp.Tool {
	return mcp.NewTool("qrdb_mvcc_current_revision",
		mcp.WithDescription("Return the latest committed MVCC revision."),
	)
}

// handleMVCCCurrentRevision returns a tool handler that delegates to
// mvccSvc.CurrentRevision.
func handleMVCCCurrentRevision(mvccSvc service.MVCCKVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rev, err := mvccSvc.CurrentRevision(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_current_revision", Err: err}
		}

		data, jsonErr := json.Marshal(map[string]int64{"revision": rev})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_mvcc_current_revision", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}
