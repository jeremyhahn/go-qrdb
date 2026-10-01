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
	"net/http"

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
			name := req.Params.Name
			if s.sealedWindowListener != nil {
				// Held until the tool call completes; see
				// sealedwindow.Admission.
				_, transition := sealedWindowTransition[name]
				defer s.sealedWindowListener.Admit(transition)()
			}
			_, allowlisted := SealedWindowAllowlist[name]
			decision := sealedwindow.Request{
				Sealed:      s.sealedWindowSealed(),
				Allowlisted: allowlisted,
				Listener:    s.sealedWindowListener != nil,
			}
			if _, preOp := sealedWindowPreOperationalOnly[name]; preOp && decision.Listener && decision.Sealed && decision.Allowlisted {
				decision.PreOperationalOnly = true
				decision.EnrollmentOpen = s.sealedWindowListener.EnrollmentOpen()
			}
			refusal, refused := sealedWindowRefusals[sealedwindow.Decide(decision)]
			if !refused {
				if decision.Listener {
					ctx = sealedwindow.WithAnonymous(ctx)
				}
				return next(ctx, req)
			}
			// CodeUnavailable for a sealed or closed window, matching REST's
			// 503, gRPC's codes.Unavailable and QUIC's StatusUnavailable for
			// the same gate. A sealed barrier is a node that will serve this
			// tool later, and a client that can read that from the code
			// retries instead of reporting a failure its operator then has to
			// diagnose from prose (go-qrdb#438). A closed enrollment is
			// CodePermissionDenied: retrying will not help.
			return toolErrorResult(name, refusal.code, refusal.msg), nil
		}
	}
}

// sealedWindowTransition holds the MCP tools of the Transition verbs
// (initialize and unseal), which run alone on the boot-time listener.
var sealedWindowTransition = sealedwindow.MCPTransition()

// sealedWindowPreOperationalOnly holds the MCP tools of the
// PreOperationalOnly verbs (officer and recovery-officer registration).
var sealedWindowPreOperationalOnly = sealedwindow.MCPPreOperationalOnly()

// sealedWindowRefusal is how the MCP gate answers one refusing Decision.
type sealedWindowRefusal struct {
	code wire.Code
	msg  string
}

// sealedWindowRefusals maps each refusing sealedwindow.Decision to its tool
// error. sealedwindow.Serve is absent: it admits.
var sealedWindowRefusals = map[sealedwindow.Decision]sealedWindowRefusal{
	sealedwindow.RefuseSealed:           {code: wire.CodeUnavailable, msg: "barrier sealed"},
	sealedwindow.RefuseClosed:           {code: wire.CodeUnavailable, msg: sealedwindow.ClosedMessage},
	sealedwindow.RefuseEnrollmentClosed: {code: wire.CodePermissionDenied, msg: sealedwindow.EnrollmentClosedMessage},
}

// sealedWindowSealed samples the probe. A nil probe reads as unsealed,
// which the boot-time listener's gate refuses.
func (s *Server) sealedWindowSealed() bool {
	return s.sealedWindowProbe != nil && s.sealedWindowProbe.IsSealed()
}

// sealedWindowClosedHandler wraps the MCP endpoint of the boot-time
// sealed-window listener. Once the barrier is unsealed it answers every
// HTTP request 503 before mcp-go reads it, so not even initialize or
// tools/list is served anonymously against an unsealed barrier. The tool
// middleware above refuses the calls themselves.
func (s *Server) sealedWindowClosedHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.sealedWindowSealed() {
			w.Header().Set("Retry-After", "5")
			http.Error(w, sealedwindow.ClosedMessage, http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}
