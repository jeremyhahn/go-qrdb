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

// batchTools returns the MCP tool registrations for batch operations.
func batchTools(batchSvc service.BatchServicer) []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{
			Tool:    toolBatchExecute(),
			Handler: handleBatchExecute(batchSvc),
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
