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
	"google.golang.org/grpc/codes"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// authHTTPContext authenticates the mTLS peer of an incoming MCP HTTP request
// and injects the resulting identity into the per-request context. It is
// registered via mcp-go's WithHTTPContextFunc, so the context it returns is
// the one ultimately passed to every tool handler. Authentication failures are
// NOT surfaced here (the hook cannot return an error); instead no identity is
// injected, and the tool-handler authorization middleware denies the call.
//
// authenticateHandler runs in front of mcp-go whenever authentication is
// configured and refuses an unauthenticated request outright, so on that path
// the identity is always already bound and this hook only reuses it. It
// remains the tool gate's second line, not its first.
func (s *Server) authHTTPContext(ctx context.Context, r *http.Request) context.Context {
	// The SO second factor rides the HTTP request, and this hook is the one
	// place the tool path can see it. It is bound before the early return
	// below so a request authenticateHandler already authenticated carries
	// it too.
	ctx = withSOFactor(ctx, r)
	// With an SO gate but no auth middleware there is no one to
	// authenticate; the gate then refuses every SO-gated tool.
	if s.authMW == nil {
		return ctx
	}
	// authenticateHandler has already authenticated this request and bound
	// the identity into the request context; a second pass would only
	// repeat the same chain walk.
	if auth.IdentityFromContext(ctx) != nil {
		return ctx
	}
	ctx = withHTTPRequestCredentials(ctx, r)
	authCtx, _, err := s.authMW.Authenticate(ctx)
	if err != nil {
		// Leave the identity unset so AuthorizeMCP denies; log at debug so a
		// misconfigured client is diagnosable without log spam.
		slog.Debug("mcp: peer authentication failed", "error", err)
		return ctx
	}
	return authCtx
}

// withSOFactor binds the SO second factor the /mcp request presented. The
// header is read only from a TLS connection, as REST reads it.
func withSOFactor(ctx context.Context, r *http.Request) context.Context {
	if r == nil {
		return ctx
	}
	return authmw.WithSOFactor(ctx, authmw.SOFactorFromHTTPRequest(r))
}

// withHTTPRequestCredentials binds the request's transport-level credential
// material into ctx for the authenticator chain: the mTLS connection state
// for the built-in mTLS authenticator, and the raw Authorization header for
// embedder-supplied credential authenticators (e.g. an OAuth 2.1 bearer
// validator registered via wiring.AuthMiddlewareConfig.ExtraAuthenticators —
// qrdb ships no such validator itself).
func withHTTPRequestCredentials(ctx context.Context, r *http.Request) context.Context {
	if r == nil {
		return ctx
	}
	if r.TLS != nil {
		ctx = auth.WithTLSState(ctx, r.TLS)
	}
	return auth.WithHTTPAuthorization(ctx, r.Header.Get("Authorization"))
}

// protectedResourceMetadataHandler serves the embedder-supplied RFC 9728
// Protected Resource Metadata document. GET returns the JSON body, HEAD the
// headers only, and every other method 405 — the document is immutable
// discovery metadata, never a mutation target.
func (s *Server) protectedResourceMetadataHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(s.protectedResourceMetadata)
	}
}

// authenticateHandler wraps the MCP endpoint whenever authentication is
// configured, and authenticates every HTTP request BEFORE mcp-go reads any of
// it. A request no configured authenticator accepts is answered 401 at the
// HTTP layer, so initialize, ping and tools/list -- which enumerates the whole
// tool surface -- are never answered to an anonymous caller, and mcp-go never
// parses an anonymous body. A request that does authenticate proceeds with its
// identity bound into the request context, where authHTTPContext picks it up
// without re-authenticating, and the per-tool DEFAULT-DENY gate still
// authorizes every call.
//
// When a bearer challenge is configured (WithBearerChallenge), the 401 carries
// it as WWW-Authenticate: the discovery entry point of the MCP authorization
// flow, which an OAuth client reads as "go authorize".
func (s *Server) authenticateHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := withHTTPRequestCredentials(r.Context(), r)
		// A request a cluster node forwarded on a caller's behalf
		// authenticates as that caller (docs/replica-forwarding).
		authCtx, _, err := s.authMW.AuthenticateHTTP(ctx, w, r)
		if err != nil {
			slog.Debug("mcp: request authentication failed", "error", err)
			if s.bearerChallenge != "" {
				w.Header().Set("WWW-Authenticate", s.bearerChallenge)
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(authCtx))
	})
}

// maxRequestBodyBytes bounds an MCP HTTP request body. The largest legitimate
// request is a batch import, whose base64 "file" argument is at most
// maxEncodedImportFileSize; the 1 MiB above it is the JSON-RPC envelope's
// headroom. mcp-go reads and decodes the whole body before any tool handler
// runs, so the bound has to be applied here, at the HTTP layer.
var maxRequestBodyBytes = int64(maxEncodedImportFileSize) + 1<<20

// maxAnonymousRequestBodyBytes bounds an MCP request body on a listener with
// no authentication: the boot-time sealed-window listener, or a node with
// authentication off. Its callers are anonymous, and everything it serves
// them -- initialize, tools/list and the sealed-window tools -- is a small
// JSON-RPC document, so they get 1 MiB, not the batch-import bound.
const maxAnonymousRequestBodyBytes = int64(1 << 20)

// requestBodyLimit is the body bound for this server's MCP endpoint.
func (s *Server) requestBodyLimit() int64 {
	if s.authMW == nil || s.sealedWindowListener != nil {
		return maxAnonymousRequestBodyBytes
	}
	return maxRequestBodyBytes
}

// boundedBodyHandler caps every request body at limit before next reads it.
// A body over the cap fails the read with *http.MaxBytesError, which mcp-go
// answers as a malformed request, and the connection is closed.
func boundedBodyHandler(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// authzToolError renders a failed authorization gate as an MCP tool error
// whose CODE and words both match what actually happened.
//
// THE NAMED PROTOCOL EXCLUSION IS GONE (go-qrdb#438). It read: "MCP tool
// results carry text, not a status code. REST answers 503, gRPC
// codes.Unavailable and QUIC wire.StatusUnavailable for a node-side fault; MCP
// has no equivalent channel and cannot be made to agree with them
// mechanically." It has one now, so it does agree with them: a retryable
// node-side fault -- a full audit disk, the case go-qrdb#335's 503 mapping
// exists to keep from being misread as a permissions problem -- carries
// CodeUnavailable, the same family REST reports as 503 and gRPC as
// codes.Unavailable, and a genuine refusal carries CodePermissionDenied.
//
// The prose distinction the exclusion introduced is kept verbatim: an operator
// reads the message, and it was already saying the right thing. What changes
// is that a program no longer has to.
//
// The tool name is not available at this gate -- it is called from the
// authorization middleware, which knows the request but renders the refusal
// for every tool alike -- so the envelope's Tool field is left empty rather
// than filled with a guess. A caller that needs it has the request in hand.
//
// The CODE comes from authmw.GRPCError -- the shared classifier REST and gRPC
// already answer from -- and not from the retryable/not split the prose uses,
// so the five auth failure classes keep their own codes instead of collapsing
// into two. A THROTTLED caller in particular reaches an MCP client as
// RESOURCE_EXHAUSTED, the same answer gRPC gives and the sibling of REST's
// 429; the coarser split would have said UNAVAILABLE and lost the reason.
//
// Two of the five DO share a code, deliberately: AuthFailureUnavailable (the
// node could not evaluate the request -- a sealed tenant, an unreadable user
// store) and AuthFailureAuditUnavailable (it evaluated but could not RECORD
// the decision) both carry CodeUnavailable, because go-qrdb#460 split that
// pair for CONTROL FLOW inside pkg/auth/middleware, not for the wire: every
// transport, MCP included, is meant to answer both identically. What must
// never collapse is the retryable/not distinction between classes that mean
// different things to a caller -- Unauthenticated, Unauthorized,
// RateLimited, and the (Audit)Unavailable pair -- not the count of classes
// itself.
func authzToolError(err error) *mcpgo.CallToolResult {
	// PermissionDenied is the fallback for a failure the classifier does not
	// recognise, matching the wording below and the fallback gRPC's
	// authorization interceptor passes.
	code := ServiceErrorCode(authmw.GRPCError(err, codes.PermissionDenied))

	if authmw.ClassifyAuthError(err).Retryable() {
		return withRetryAfter(toolErrorResult("", code, "authorization unavailable, retry: "+err.Error()), err)
	}
	return toolErrorResult("", code, "authorization denied: "+err.Error())
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

// authorizeTenantBarrierInit authorizes a tenant barrier initialization via
// the shared trust-on-first-init rule (middleware.AuthorizeTenantBarrierInit
// — the single cross-transport home of the bootstrap-window semantics). When
// authMW is nil the gate is a no-op (unauthenticated deployment).
func (s *Server) authorizeTenantBarrierInit(ctx context.Context, tenantID string) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenantBarrierInit(ctx, tenantID, s.tenantSvc)
}

// authorizeTenantBarrierSealedOp authorizes the tenant barrier operations
// that must remain reachable while the tenant's IAM itself is unreachable
// (status/unseal) via the shared sealed-window rule
// (middleware.AuthorizeTenantBarrierSealedOp). When authMW is nil the gate
// is a no-op.
func (s *Server) authorizeTenantBarrierSealedOp(ctx context.Context, tenantID string, perm permission.Permission) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenantBarrierSealedOp(ctx, tenantID, perm)
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
			// The SO PIN bound for this request is wiped once the call is
			// done, whether or not the tool was SO-gated.
			defer authmw.WipeSOFactor(ctx)
			if s.authMW != nil {
				if err := s.authMW.AuthorizeMCP(ctx, s.toolPerms, req.Params.Name, req.GetArguments()); err != nil {
					slog.Warn("mcp: tool call denied",
						"tool", req.Params.Name, "error", err)
					return authzToolError(err), nil
				}
			}
			if s.soGate != nil {
				if perm, ok := s.toolPerms.Lookup(req.Params.Name); ok {
					if err := s.soGate.Enforce(ctx, perm, req.Params.Name, "call"); err != nil {
						slog.Warn("mcp: SO dual factor refused",
							"tool", req.Params.Name, "error", err)
						return authzToolError(err), nil
					}
				}
			}
			return next(ctx, req)
		}
	}
}
