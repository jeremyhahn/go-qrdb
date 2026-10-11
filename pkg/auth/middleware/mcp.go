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
//
// The permission is authorized in the caller's own tenant, the tenant the
// authenticated context is bound to. A tool that acts on a tenant its
// arguments name re-checks there itself (AuthorizeTenantTarget).
//
// A tool declared public (MCPToolRegistry.RegisterPublic) is admitted with no
// identity and no authorization, as REST admits a public route.
func (m *AuthMiddleware) AuthorizeMCP(ctx context.Context, tools *MCPToolRegistry, toolName string) error {
	if tools.IsPublic(toolName) {
		return nil
	}
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

// MCPToolVisible reports whether the caller in ctx may call the named tool,
// for filtering tools/list down to what the caller can use. A public tool is
// visible to everyone; any other tool only to an identity whose grants cover
// its permission, and never to an anonymous caller.
//
// It renders the same decision AuthorizeMCP does but is not one: listing
// grants nothing, so it neither charges the caller's rate limit nor writes an
// audit record per tool, and it does not run the per-call peer-attestation
// gate. Every call is still authorized by AuthorizeMCP.
func (m *AuthMiddleware) MCPToolVisible(ctx context.Context, tools *MCPToolRegistry, toolName string) bool {
	if tools.IsPublic(toolName) {
		return true
	}
	identity := auth.IdentityFromContext(ctx)
	if identity == nil {
		return false
	}
	perm, ok := tools.Lookup(toolName)
	if !ok {
		return false
	}
	return m.decide(ctx, identity, perm) == nil
}
