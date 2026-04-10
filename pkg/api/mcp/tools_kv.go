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

// kvTools returns the MCP tool registrations for KV data-plane operations.
func kvTools(kvSvc service.KVServicer) []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{
			Tool:    toolPut(),
			Handler: handlePut(kvSvc),
		},
		{
			Tool:    toolGet(),
			Handler: handleGet(kvSvc),
		},
		{
			Tool:    toolDelete(),
			Handler: handleDelete(kvSvc),
		},
		{
			Tool:    toolList(),
			Handler: handleList(kvSvc),
		},
		{
			Tool:    toolScan(),
			Handler: handleScan(kvSvc),
		},
	}
}

// toolPut defines the qrdb_put tool schema.
func toolPut() mcp.Tool {
	return mcp.NewTool("qrdb_put",
		mcp.WithDescription("Store a key-value pair through Raft consensus."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to store."),
		),
		mcp.WithString("value",
			mcp.Required(),
			mcp.Description("The value to store (UTF-8 text)."),
		),
	)
}

// handlePut returns a tool handler that delegates to kvSvc.Put.
func handlePut(kvSvc service.KVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}
		value := mcp.ParseString(req, "value", "")
		if value == "" {
			return mcp.NewToolResultError("value is required"), nil
		}

		if err := kvSvc.Put(ctx, key, []byte(value)); err != nil {
			return nil, &ToolError{Tool: "qrdb_put", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolGet defines the qrdb_get tool schema.
func toolGet() mcp.Tool {
	return mcp.NewTool("qrdb_get",
		mcp.WithDescription("Retrieve a value by key with a linearizable read."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to retrieve."),
		),
	)
}

// handleGet returns a tool handler that delegates to kvSvc.Get.
func handleGet(kvSvc service.KVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}

		data, err := kvSvc.Get(ctx, key)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_get", Err: err}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolDelete defines the qrdb_delete tool schema.
func toolDelete() mcp.Tool {
	return mcp.NewTool("qrdb_delete",
		mcp.WithDescription("Delete a key through Raft consensus."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to delete."),
		),
	)
}

// handleDelete returns a tool handler that delegates to kvSvc.Delete.
func handleDelete(kvSvc service.KVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}

		if err := kvSvc.Delete(ctx, key); err != nil {
			return nil, &ToolError{Tool: "qrdb_delete", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolList defines the qrdb_list tool schema.
func toolList() mcp.Tool {
	return mcp.NewTool("qrdb_list",
		mcp.WithDescription("List keys matching a prefix with a linearizable read. An empty prefix returns all keys."),
		mcp.WithString("prefix",
			mcp.Description("Optional key prefix filter."),
		),
	)
}

// handleList returns a tool handler that delegates to kvSvc.List.
func handleList(kvSvc service.KVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		prefix := mcp.ParseString(req, "prefix", "")

		keys, err := kvSvc.List(ctx, prefix)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_list", Err: err}
		}

		data, jsonErr := json.Marshal(keys)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_list", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolScan defines the qrdb_scan tool schema.
func toolScan() mcp.Tool {
	return mcp.NewTool("qrdb_scan",
		mcp.WithDescription("Scan key-value pairs matching a prefix. Returns a JSON map of key to value."),
		mcp.WithString("prefix",
			mcp.Description("Optional key prefix filter."),
		),
	)
}

// handleScan returns a tool handler that delegates to kvSvc.Scan.
func handleScan(kvSvc service.KVServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		prefix := mcp.ParseString(req, "prefix", "")

		pairs, err := kvSvc.Scan(ctx, prefix)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_scan", Err: err}
		}

		// Convert []byte values to strings for JSON readability.
		out := make(map[string]string, len(pairs))
		for k, v := range pairs {
			out[k] = string(v)
		}

		data, jsonErr := json.Marshal(out)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_scan", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}
