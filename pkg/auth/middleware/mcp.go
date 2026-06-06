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
// named MCP tool. Unlike the QUIC/REST/gRPC authorizers — which treat an
// unmapped method as a public endpoint — MCP is DEFAULT-DENY: the MCP surface
// is a privileged agent interface exposing the full administrative API, so a
// tool with no explicit permission mapping is denied rather than left open. A
// new tool must be added to mcpPermissions to become callable.
//
// The identity must already have been established (see the MCP server's HTTP
// context func, which authenticates the mTLS peer and injects the identity).
func (m *AuthMiddleware) AuthorizeMCP(ctx context.Context, toolName string) error {
	identity := auth.IdentityFromContext(ctx)
	if identity == nil {
		return &auth.AuthError{Code: "unauthenticated", Message: "mcp: no authenticated identity"}
	}

	perm, ok := LookupMCP(toolName)
	if !ok {
		return &auth.AuthError{
			Code:    "unauthorized",
			Message: "mcp: tool not permitted (no permission mapping): " + toolName,
		}
	}

	return m.authorize(ctx, identity, perm)
}
