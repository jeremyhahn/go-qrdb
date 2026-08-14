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
	"log/slog"
	"net/http"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// authHTTPContext authenticates the mTLS peer of an incoming MCP HTTP request
// and injects the resulting identity into the per-request context. It is
// registered via mcp-go's WithHTTPContextFunc, so the context it returns is
// the one ultimately passed to every tool handler. Authentication failures are
// NOT surfaced here (the hook cannot return an error); instead no identity is
// injected, and the tool-handler authorization middleware denies the call.
func (s *Server) authHTTPContext(ctx context.Context, r *http.Request) context.Context {
	if r != nil && r.TLS != nil {
		ctx = auth.WithTLSState(ctx, r.TLS)
	}
	authCtx, _, err := s.authMW.Authenticate(ctx)
	if err != nil {
		// Leave the identity unset so AuthorizeMCP denies; log at debug so a
		// misconfigured client is diagnosable without log spam.
		slog.Debug("mcp: peer authentication failed", "error", err)
		return ctx
	}
	return authCtx
}

// authorizeTenantBarrierOp authorizes a per-tenant barrier tool invocation
// against the TRUE target tenant carried in the tool arguments (not the
// caller's ambient tenant). It delegates to the tenant-scoped authorizer,
// whose ScopeTenantOnly rule denies the system tenant and any cross-tenant
// access. When no auth middleware is configured the gate is a no-op, matching
// the rest of the server's unauthenticated-mode behavior. It mirrors
// authorizeTenantBarrierOp on the QUIC, gRPC, and REST servers.
func (s *Server) authorizeTenantBarrierOp(ctx context.Context, tenantID string, perm permission.Permission) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenant(ctx, tenantID, perm)
}

// toolAuthzMiddleware returns an mcp-go tool-handler middleware that authorizes
// every tool invocation against the caller's identity and the tool's required
// permission BEFORE the tool handler runs. The permission is resolved from
// the registration-site toolPerms registry. Authorization is DEFAULT-DENY: a
// tool with no registered permission, or a caller lacking the permission, is
// rejected. The MCP surface exposes the full administrative API, so it must
// never be reachable without an authenticated, authorized identity.
func (s *Server) toolAuthzMiddleware() mcpserver.ToolHandlerMiddleware {
	return func(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
		return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			if err := s.authMW.AuthorizeMCP(ctx, s.toolPerms, req.Params.Name); err != nil {
				slog.Warn("mcp: tool call denied",
					"tool", req.Params.Name, "error", err)
				return mcpgo.NewToolResultError("authorization denied: " + err.Error()), nil
			}
			return next(ctx, req)
		}
	}
}
