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

// credentialTools returns the MCP tool registrations for credential
// management operations including submission, strategy query, and deletion.
func credentialTools(svc service.CredentialServicer) []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{Tool: toolCredentialSubmit(), Handler: handleCredentialSubmit(svc)},
		{Tool: toolCredentialStrategy(), Handler: handleCredentialStrategy(svc)},
		{Tool: toolCredentialDelete(), Handler: handleCredentialDelete(svc)},
	}
}

// toolCredentialSubmit defines the qrdb_credential_submit tool schema.
func toolCredentialSubmit() mcp.Tool {
	return mcp.NewTool("qrdb_credential_submit",
		mcp.WithDescription("Submit a named credential, notifying any blocked retrievers for the manual strategy."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The credential name."),
		),
		mcp.WithString("value",
			mcp.Required(),
			mcp.Description("The credential value."),
		),
	)
}

// handleCredentialSubmit returns a tool handler that delegates to
// svc.CredentialSubmit.
func handleCredentialSubmit(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		value := mcp.ParseString(req, "value", "")
		if value == "" {
			return mcp.NewToolResultError("value is required"), nil
		}

		if err := svc.CredentialSubmit(ctx, name, []byte(value)); err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_submit", Err: err}
		}

		return mcp.NewToolResultText("credential submitted"), nil
	}
}

// toolCredentialStrategy defines the qrdb_credential_strategy tool schema.
func toolCredentialStrategy() mcp.Tool {
	return mcp.NewTool("qrdb_credential_strategy",
		mcp.WithDescription("Return the active credential strategy name."),
	)
}

// handleCredentialStrategy returns a tool handler that delegates to
// svc.CredentialStrategy.
func handleCredentialStrategy(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		strategy := svc.CredentialStrategy()

		result := struct {
			Strategy string `json:"strategy"`
		}{
			Strategy: strategy,
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_credential_strategy", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCredentialDelete defines the qrdb_credential_delete tool schema.
func toolCredentialDelete() mcp.Tool {
	return mcp.NewTool("qrdb_credential_delete",
		mcp.WithDescription("Delete a named credential from storage."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The credential name to delete."),
		),
	)
}

// handleCredentialDelete returns a tool handler that delegates to
// svc.CredentialDelete.
func handleCredentialDelete(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		if err := svc.CredentialDelete(ctx, name); err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_delete", Err: err}
		}

		return mcp.NewToolResultText("credential deleted"), nil
	}
}
