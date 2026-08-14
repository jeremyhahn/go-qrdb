// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package middleware

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// AuthorizeMCP checks whether the authenticated caller in ctx may invoke the
// named MCP tool. Per-tool permissions come from the MCPToolRegistry the MCP
// server populates at tool-registration time — the single source of truth
// that replaced the old static mcpPermissions table. MCP is DEFAULT-DENY:
// the MCP surface is a privileged agent interface exposing the full
// administrative API, so a tool with no registered permission (or a nil
// registry) is denied rather than left open. A new tool becomes callable
// only by being registered with its permission through the MCP server's
// addTools helper; tools registered directly on the underlying mcp-go server
// (e.g. by a ServiceModule's RegisterMCP hook) bypass the registry and are
// therefore denied until they gain registry coverage.
//
// The identity must already have been established (see the MCP server's HTTP
// context func, which authenticates the mTLS peer and injects the identity).
func (m *AuthMiddleware) AuthorizeMCP(ctx context.Context, tools *MCPToolRegistry, toolName string) error {
	identity := auth.IdentityFromContext(ctx)
	if identity == nil {
		return &auth.AuthError{Code: "unauthenticated", Message: "mcp: no authenticated identity"}
	}

	perm, ok := tools.Lookup(toolName)
	if !ok {
		return &auth.AuthError{
			Code:    "unauthorized",
			Message: "mcp: tool not permitted (no permission registration): " + toolName,
		}
	}

	return m.authorize(ctx, identity, perm)
}
