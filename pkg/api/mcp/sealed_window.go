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
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

// sealedWindowProbe is the abstraction the sealed-window tool-handler
// middleware needs to know whether the storage barrier is currently
// sealed. Mirrors grpcpkg's unexported interface of the same name.
type sealedWindowProbe interface {
	IsSealed() bool
}

// SealedWindowAllowlist is the set of MCP tool names that remain
// operative while the barrier is sealed. Tools not in this list --
// qrdb_health and qrdb_status included -- are refused with CodeUnavailable
// and reason sealedwindow.ReasonSealed while the probe reports sealed. The
// list is derived from the canonical transport-neutral verb table in
// pkg/api/sealedwindow so it cannot drift from the other transports: the
// unseal call and the first-boot verbs.
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
			_, authOnly := sealedWindowAuthenticatedOnly[name]
			_, listenerOnly := sealedWindowListenerOnly[name]
			sealed, unconfirmed := sealedwindow.Sample(s.sealedWindowProbe)
			decision := sealedwindow.Request{
				Sealed:            sealed,
				Unconfirmed:       unconfirmed,
				Allowlisted:       allowlisted,
				Listener:          s.sealedWindowListener != nil,
				AuthenticatedOnly: authOnly,
				ListenerOnly:      listenerOnly,
			}
			if _, preOp := sealedWindowPreOperationalOnly[name]; preOp && decision.Sealed && decision.Allowlisted {
				decision.PreOperationalOnly = true
				decision.EnrollmentOpen = s.sealedWindowListener != nil && s.sealedWindowListener.EnrollmentOpen(ctx)
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
			// diagnose from prose (go-qrdb#438).
			return refusal.result(name), nil
		}
	}
}

// sealedWindowAuthenticatedOnly holds the MCP tools of the AuthenticatedOnly
// verbs (cluster unseal), never served anonymously.
var sealedWindowAuthenticatedOnly = sealedwindow.MCPAuthenticatedOnly()

// sealedWindowListenerOnly holds the MCP tools of the ListenerOnly verbs
// (barrier status), never served by a sealed regular listener.
var sealedWindowListenerOnly = sealedwindow.MCPListenerOnly()

// sealedWindowTransition holds the MCP tools of the Transition verbs
// (initialize and unseal), which run alone on the boot-time listener.
var sealedWindowTransition = sealedwindow.MCPTransition()

// sealedWindowPreOperationalOnly holds the MCP tools of the
// PreOperationalOnly verbs: the first-boot verbs a sealed node serves only
// while enrollment is open.
var sealedWindowPreOperationalOnly = sealedwindow.MCPPreOperationalOnly()

// sealedWindowRefusal is how the MCP gate answers one refusing Decision.
type sealedWindowRefusal struct {
	code wire.Code
	msg  string
	// reason is the envelope's structured reason; the sealed refusal
	// carries sealedwindow.ReasonSealed so a client reads "up and sealed"
	// from it.
	reason string
	// err, when set, is the typed error the refusal reports; it wins over
	// code, msg and reason, and is classified like any service error, so its
	// envelope names its own reason (SEAL_STATE_UNCONFIRMED).
	err error
}

// sealedWindowRefusals maps each refusing sealedwindow.Decision to its tool
// error. sealedwindow.Serve is absent: it admits.
var sealedWindowRefusals = map[sealedwindow.Decision]sealedWindowRefusal{
	sealedwindow.RefuseSealed:      {code: wire.CodeUnavailable, msg: sealedwindow.SealedMessage, reason: sealedwindow.ReasonSealed},
	sealedwindow.RefuseUnconfirmed: {err: &clusterseal.SealStateUnconfirmedError{}},
	sealedwindow.RefuseClosed:      {code: wire.CodeUnavailable, msg: sealedwindow.ClosedMessage},
}

// result renders the refusal as tool's error result.
func (r sealedWindowRefusal) result(tool string) *mcpgo.CallToolResult {
	if r.err != nil {
		return classifiedToolError(tool, r.err)
	}
	res := toolErrorResult(tool, r.code, r.msg)
	if env, ok := res.StructuredContent.(*wire.ErrorEnvelope); ok && r.reason != "" {
		env.Error.Reason = r.reason
	}
	return res
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

// sealedWindowDispatchHandler sends each request to sealed, the boot-time
// sealed-window listener's endpoint, while probe reports the node barrier
// sealed, and to regular otherwise. A re-sealed regular listener thereby
// answers exactly as a node that booted sealed does: the allowlisted calls
// anonymously, with no user-store lookup, and everything else with the
// sealed refusal.
func sealedWindowDispatchHandler(probe sealedwindow.ListenerProbe, sealed, regular http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if probe.IsSealed() {
			sealed.ServeHTTP(w, r)
			return
		}
		regular.ServeHTTP(w, r)
	})
}
