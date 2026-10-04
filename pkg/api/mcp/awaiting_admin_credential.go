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

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
)

// awaitingAdminCredentialProbe is the abstraction the tool-handler
// middleware needs to know whether the node is currently "awaiting admin
// credential" (go-qrdb#548 review item 3). Mirrors sealedWindowProbe's
// shape.
type awaitingAdminCredentialProbe interface {
	AwaitingAdminCredential() bool
}

// AwaitingAdminCredentialAllowlist is the set of MCP tool names that remain
// reachable while the node is awaiting admin credential. Derived from the
// canonical transport-neutral verb table so it cannot drift from the other
// transports.
var AwaitingAdminCredentialAllowlist = sealedwindow.MCPAwaitingAdminCredential()

// awaitingAdminCredentialAdmittedKey marks a context the
// awaiting-admin-credential gate has already admitted. toolAuthzMiddleware
// checks it and skips straight to the tool handler: the gate ran BEFORE it
// in the middleware chain and already decided this call needs no identity
// at all, which is the whole reason the node is in this boot state. A
// SEPARATE marker from sealedwindow.WithAnonymous/Anonymous on purpose --
// that one means "the boot-time sealed-window listener has no identity
// plane", a different state with a different lifetime, and conflating the
// two would let one gate's admission decision leak into the other's.
type awaitingAdminCredentialAdmittedKey struct{}

// withAwaitingAdminCredentialAdmitted marks ctx as already admitted by the
// awaiting-admin-credential gate.
func withAwaitingAdminCredentialAdmitted(ctx context.Context) context.Context {
	return context.WithValue(ctx, awaitingAdminCredentialAdmittedKey{}, true)
}

// awaitingAdminCredentialAdmitted reports whether ctx carries the mark.
func awaitingAdminCredentialAdmitted(ctx context.Context) bool {
	v, _ := ctx.Value(awaitingAdminCredentialAdmittedKey{}).(bool)
	return v
}

// awaitingAdminCredentialMiddleware returns the tool-handler middleware for
// the awaiting-admin-credential gate. While s.awaitingAdminCredentialProbe
// reports awaiting, a tool outside AwaitingAdminCredentialAllowlist is
// refused with wire.CodeUnavailable before next -- and therefore
// toolAuthzMiddleware -- ever runs; an allowlisted tool is admitted and its
// context marked so toolAuthzMiddleware (positioned after this one in the
// chain) skips its own authorization check entirely, since there is no
// bound identity to authorize. Once the probe reports false every call
// reaches next unchanged.
func (s *Server) awaitingAdminCredentialMiddleware() mcpserver.ToolHandlerMiddleware {
	return func(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
		return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			if s.awaitingAdminCredentialProbe == nil || !s.awaitingAdminCredentialProbe.AwaitingAdminCredential() {
				return next(ctx, req)
			}
			if _, ok := AwaitingAdminCredentialAllowlist[req.Params.Name]; !ok {
				return toolErrorResult(req.Params.Name, wire.CodeUnavailable, sealedwindow.AwaitingAdminCredentialClosedMessage), nil
			}
			return next(withAwaitingAdminCredentialAdmitted(ctx), req)
		}
	}
}
