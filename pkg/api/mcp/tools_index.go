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

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// indexTools returns the MCP tool registrations for secondary index operations.
func indexTools(indexSvc service.IndexServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolIndexCreate(),
			Perm:    permission.IndexCreate,
			Handler: handleIndexCreate(indexSvc),
		},
		{
			Tool:    toolIndexDrop(),
			Perm:    permission.IndexDrop,
			Handler: handleIndexDrop(indexSvc),
		},
		{
			Tool:    toolIndexQuery(),
			Perm:    permission.IndexQuery,
			Handler: handleIndexQuery(indexSvc),
		},
		{
			Tool:    toolIndexScan(),
			Perm:    permission.IndexScan,
			Handler: handleIndexScan(indexSvc),
		},
		{
			Tool:    toolIndexList(),
			Perm:    permission.IndexList,
			Handler: handleIndexList(indexSvc),
		},
	}
}

// toolIndexCreate defines the qrdb_index_create tool schema.
func toolIndexCreate() mcp.Tool {
	return mcp.NewTool("qrdb_index_create",
		mcp.WithDescription("Create a new secondary index on a specified field. Enables efficient lookups by field values without scanning all keys."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The unique name for the index."),
		),
		mcp.WithString("field_name",
			mcp.Required(),
			mcp.Description("The field to index on."),
		),
		mcp.WithBoolean("unique",
			mcp.Description("Enforce that each indexed value maps to at most one key. Defaults to false."),
		),
		mcp.WithBoolean("sparse",
			mcp.Description("Skip indexing keys where the indexed field is absent or empty. Defaults to false."),
		),
		mcp.WithBoolean("numeric",
			mcp.Description("Encode field values as fixed-width big-endian integers so that ordering is numeric rather than lexicographic. Defaults to false."),
		),
		mcp.WithBoolean("case_insensitive",
			mcp.Description("Fold string field values on write, backfill and lookup, so \"Alpha\" and \"alpha\" are the same term. Defaults to false, i.e. case-sensitive."),
		),
	)
}

// handleIndexCreate returns a tool handler that delegates to
// indexSvc.CreateIndex.
func handleIndexCreate(indexSvc service.IndexServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		indexSvc, rerr := resolveIndex(ctx, indexSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		fieldName := mcp.ParseString(req, "field_name", "")
		if fieldName == "" {
			return mcp.NewToolResultError("field_name is required"), nil
		}

		config := index.IndexConfig{
			Unique: mcp.ParseBoolean(req, "unique", false),
			Sparse: mcp.ParseBoolean(req, "sparse", false),
			// index.IndexConfig names the field for the DEFAULT, so its zero
			// value is case-INSENSITIVE. It must be set explicitly now that
			// the engine derives the index's case behaviour from the config
			// instead of hardcoding CaseSensitive:true (go-qrdb#34).
			CaseSensitive: !mcp.ParseBoolean(req, "case_insensitive", false),
		}
		if mcp.ParseBoolean(req, "numeric", false) {
			config.ValueType = index.IndexValueTypeNumeric
		}

		if err := indexSvc.CreateIndex(ctx, name, fieldName, config); err != nil {
			return nil, &ToolError{Tool: "qrdb_index_create", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolIndexDrop defines the qrdb_index_drop tool schema.
func toolIndexDrop() mcp.Tool {
	return mcp.NewTool("qrdb_index_drop",
		mcp.WithDescription("Remove a secondary index by name."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The name of the index to drop."),
		),
	)
}

// handleIndexDrop returns a tool handler that delegates to
// indexSvc.DropIndex.
func handleIndexDrop(indexSvc service.IndexServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		indexSvc, rerr := resolveIndex(ctx, indexSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		if err := indexSvc.DropIndex(ctx, name); err != nil {
			return nil, &ToolError{Tool: "qrdb_index_drop", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolIndexQuery defines the qrdb_index_query tool schema.
func toolIndexQuery() mcp.Tool {
	return mcp.NewTool("qrdb_index_query",
		mcp.WithDescription("Query a secondary index for keys matching an exact value."),
		mcp.WithString("index_name",
			mcp.Required(),
			mcp.Description("The name of the index to query."),
		),
		mcp.WithString("value",
			mcp.Required(),
			mcp.Description("The value to look up in the index."),
		),
	)
}

// handleIndexQuery returns a tool handler that delegates to
// indexSvc.Query.
func handleIndexQuery(indexSvc service.IndexServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		indexSvc, rerr := resolveIndex(ctx, indexSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		indexName := mcp.ParseString(req, "index_name", "")
		if indexName == "" {
			return mcp.NewToolResultError("index_name is required"), nil
		}
		value := mcp.ParseString(req, "value", "")
		if value == "" {
			return mcp.NewToolResultError("value is required"), nil
		}

		keys, err := indexSvc.Query(ctx, indexName, []byte(value))
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_index_query", Err: err}
		}

		stringKeys := make([]string, len(keys))
		for i, k := range keys {
			stringKeys[i] = string(k)
		}

		data, jsonErr := json.Marshal(stringKeys)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_index_query", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolIndexScan defines the qrdb_index_scan tool schema.
func toolIndexScan() mcp.Tool {
	return mcp.NewTool("qrdb_index_scan",
		mcp.WithDescription("Scan a secondary index for entries with values in the half-open interval [start, end)."),
		mcp.WithString("index_name",
			mcp.Required(),
			mcp.Description("The name of the index to scan."),
		),
		mcp.WithString("start",
			mcp.Description("The start value of the scan range (inclusive). Omit for an open lower bound."),
		),
		mcp.WithString("end",
			mcp.Description("The end value of the scan range (exclusive). Omit for an open upper bound."),
		),
	)
}

// handleIndexScan returns a tool handler that delegates to
// indexSvc.Scan.
func handleIndexScan(indexSvc service.IndexServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		indexSvc, rerr := resolveIndex(ctx, indexSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		indexName := mcp.ParseString(req, "index_name", "")
		if indexName == "" {
			return mcp.NewToolResultError("index_name is required"), nil
		}
		// An omitted start/end is an OPEN bound (nil); a provided one — even
		// empty — is an explicit bound (go-qrdb#36).
		args := req.GetArguments()
		var start, end []byte
		if _, ok := args["start"]; ok {
			start = []byte(mcp.ParseString(req, "start", ""))
			if start == nil {
				start = []byte{}
			}
		}
		if _, ok := args["end"]; ok {
			end = []byte(mcp.ParseString(req, "end", ""))
			if end == nil {
				end = []byte{}
			}
		}

		entries, err := indexSvc.Scan(ctx, indexName, start, end)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_index_scan", Err: err}
		}

		stringEntries := make(map[string][]string, len(entries))
		for val, keys := range entries {
			skeys := make([]string, len(keys))
			for i, k := range keys {
				skeys[i] = string(k)
			}
			stringEntries[val] = skeys
		}

		data, jsonErr := json.Marshal(stringEntries)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_index_scan", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolIndexList defines the qrdb_index_list tool schema.
func toolIndexList() mcp.Tool {
	return mcp.NewTool("qrdb_index_list",
		mcp.WithDescription("List all registered secondary indexes."),
	)
}

// handleIndexList returns a tool handler that delegates to
// indexSvc.ListIndexes.
func handleIndexList(indexSvc service.IndexServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		indexSvc, rerr := resolveIndex(ctx, indexSvc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		names, err := indexSvc.ListIndexes(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_index_list", Err: err}
		}

		data, jsonErr := json.Marshal(names)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_index_list", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}
