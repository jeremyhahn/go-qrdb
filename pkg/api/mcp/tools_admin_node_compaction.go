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

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// nodeCompactionResult is the result of both node-compaction tools: the
// platform's node-keyspace compaction policy, absent when there is none.
type nodeCompactionResult struct {
	Compaction *compaction.Policy `json:"compaction,omitempty"`
}

// toolNodeCompaction defines the qrdb_node_compaction tool schema.
func toolNodeCompaction() mcp.Tool {
	return mcp.NewTool("qrdb_node_compaction",
		mcp.WithDescription("Return the platform's node-keyspace compaction policy: the scheduled compaction of the keys outside every database namespace. System tenant only."),
	)
}

// toolUpdateNodeCompaction defines the qrdb_update_node_compaction tool
// schema.
func toolUpdateNodeCompaction() mcp.Tool {
	return mcp.NewTool("qrdb_update_node_compaction",
		mcp.WithDescription("Set the platform's node-keyspace compaction policy: off, revision (keep the last N revisions) or period (keep history newer than a duration). Omitting mode turns it off. System tenant only."),
		mcp.WithString("mode",
			mcp.Description("\"off\", \"revision\" or \"period\". Omit it to turn node-keyspace compaction off."),
		),
		mcp.WithNumber("retainRevisions",
			mcp.Description("Revision mode: how many of the store's most recent revisions to keep (at least 1)."),
		),
		mcp.WithString("retainPeriod",
			mcp.Description("Period mode: how much history to keep by age, a Go duration of at least 1s (e.g. \"24h\")."),
		),
	)
}

// handleNodeCompaction returns a tool handler that delegates to
// adminSvc.NodeCompaction.
func handleNodeCompaction(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		policy, err := adminSvc.NodeCompaction(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_node_compaction", Err: err}
		}
		return nodeCompactionToolResult("qrdb_node_compaction", policy)
	}
}

// handleUpdateNodeCompaction returns a tool handler that delegates to
// adminSvc.SetNodeCompaction. An omitted mode turns node-keyspace compaction
// off -- the same as an absent compaction on every other transport.
func handleUpdateNodeCompaction(adminSvc service.AdminServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var policy *compaction.Policy
		if mode := mcp.ParseString(req, "mode", ""); mode != "" {
			policy = &compaction.Policy{
				Mode:            compaction.Mode(mode),
				RetainRevisions: mcp.ParseInt64(req, "retainRevisions", 0),
				RetainPeriod:    mcp.ParseString(req, "retainPeriod", ""),
			}
		}
		if err := adminSvc.SetNodeCompaction(ctx, policy); err != nil {
			return nil, &ToolError{Tool: "qrdb_update_node_compaction", Err: err}
		}
		return nodeCompactionToolResult("qrdb_update_node_compaction", policy.Normalized())
	}
}

func nodeCompactionToolResult(tool string, policy *compaction.Policy) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(nodeCompactionResult{Compaction: policy})
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	return mcp.NewToolResultText(string(data)), nil
}
