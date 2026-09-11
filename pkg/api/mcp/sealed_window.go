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

// sealedWindowProbe is the abstraction the sealed-window tool-handler
// middleware needs to know whether the storage barrier is currently
// sealed. Mirrors grpcpkg's unexported interface of the same name.
type sealedWindowProbe interface {
	IsSealed() bool
}

// SealedWindowAllowlist is the set of MCP tool names that remain
// operative while the barrier is sealed. Tools not in this list are
// rejected with a tool-result error while the probe reports sealed. The
// list is derived from the canonical transport-neutral verb table in
// pkg/api/sealedwindow so it cannot drift from the other transports:
// every qrdb_barrier_* and qrdb_ceremony_* unseal-flow tool, plus
// qrdb_health/qrdb_status for liveness probes.
var SealedWindowAllowlist = sealedwindow.MCPAllowlist()

// sealedWindowMiddleware returns an mcp-go tool-handler middleware that
// rejects every tool call NOT on SealedWindowAllowlist while
// s.sealedWindowProbe reports sealed. Once the barrier is unsealed the
// middleware becomes a passthrough; on re-seal it engages again. Mirrors
// SealedWindowUnaryInterceptor (pkg/api/grpc) and the sealed-window gate on
// the native QUIC transport, giving operators a consistent envelope across
// every transport that exposes the unseal flow.
func (s *Server) sealedWindowMiddleware() mcpserver.ToolHandlerMiddleware {
	return func(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
		return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			if s.sealedWindowProbe == nil || !s.sealedWindowProbe.IsSealed() {
				return next(ctx, req)
			}
			if _, ok := SealedWindowAllowlist[req.Params.Name]; ok {
				return next(ctx, req)
			}
			// CodeUnavailable, matching REST's 503, gRPC's codes.Unavailable
			// and QUIC's StatusUnavailable for the same gate. A sealed barrier
			// is a node that will serve this tool later, and a client that can
			// read that from the code retries instead of reporting a failure
			// its operator then has to diagnose from prose (go-qrdb#438).
			return toolErrorResult(req.Params.Name, wire.CodeUnavailable, "barrier sealed"), nil
		}
	}
}
