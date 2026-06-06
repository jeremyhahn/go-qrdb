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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// toolDatabaseSeal defines the qrdb_database_seal tool schema.
func toolDatabaseSeal() mcp.Tool {
	return mcp.NewTool("qrdb_database_seal",
		mcp.WithDescription("Seal the encryption barrier for a database."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name."),
		),
	)
}

// handleDatabaseSeal returns a tool handler that seals a database barrier.
func handleDatabaseSeal(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		if err := dbSvc.SealDatabase(ctx, name); err != nil {
			return nil, &ToolError{Tool: "qrdb_database_seal", Err: err}
		}
		return mcp.NewToolResultText(`{"sealed":true}`), nil
	}
}

// toolDatabaseUnseal defines the qrdb_database_unseal tool schema.
func toolDatabaseUnseal() mcp.Tool {
	return mcp.NewTool("qrdb_database_unseal",
		mcp.WithDescription("Unseal the encryption barrier for a database using the provided secret."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name."),
		),
		mcp.WithString("secret",
			mcp.Description("The unseal secret for the database barrier."),
		),
	)
}

// handleDatabaseUnseal returns a tool handler that unseals a database barrier.
func handleDatabaseUnseal(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		secret := mcp.ParseString(req, "secret", "")
		if err := dbSvc.UnsealDatabase(ctx, name, seal.Credentials{Secret: secret}); err != nil {
			return nil, &ToolError{Tool: "qrdb_database_unseal", Err: err}
		}
		return mcp.NewToolResultText(`{"unsealed":true}`), nil
	}
}

// toolDatabaseBarrierStatus defines the qrdb_database_barrier_status tool schema.
func toolDatabaseBarrierStatus() mcp.Tool {
	return mcp.NewTool("qrdb_database_barrier_status",
		mcp.WithDescription("Return the encryption-barrier status (sealed state and strategy) for a database."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name."),
		),
	)
}

// handleDatabaseBarrierStatus returns a tool handler that reports a database
// barrier's status.
func handleDatabaseBarrierStatus(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		st, err := dbSvc.DatabaseBarrierStatus(name)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_database_barrier_status", Err: err}
		}
		return marshalToolResult("qrdb_database_barrier_status", st)
	}
}
