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

// sqlTools returns the MCP tool registrations for ad-hoc SQL query
// execution.
func sqlTools(svc service.SQLServicer) []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{Tool: toolSQLQuery(), Handler: handleSQLQuery(svc)},
	}
}

// toolSQLQuery defines the qrdb_sql_query tool schema.
func toolSQLQuery() mcp.Tool {
	return mcp.NewTool("qrdb_sql_query",
		mcp.WithDescription("Execute an ad-hoc SQL query against a named database and return the result set."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The SQL query to execute."),
		),
		mcp.WithString("database",
			mcp.Required(),
			mcp.Description("The database name to query."),
		),
	)
}

// handleSQLQuery returns a tool handler that delegates to svc.Execute.
// Mirrors the REST POST /sql/query and gRPC/QUIC SQLQuery handlers.
func handleSQLQuery(svc service.SQLServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := mcp.ParseString(req, "query", "")
		if query == "" {
			return nil, &ToolError{Tool: "qrdb_sql_query", Err: &service.SQLEmptyQueryError{}}
		}
		database := mcp.ParseString(req, "database", "")
		if database == "" {
			return nil, &ToolError{Tool: "qrdb_sql_query", Err: &service.SQLEmptyDatabaseError{}}
		}

		result, err := svc.Execute(ctx, database, query)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_sql_query", Err: err}
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_sql_query", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}
