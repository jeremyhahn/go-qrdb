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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"google.golang.org/grpc/codes"

	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs"
	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
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
	// The caller's remote address (go-qrdb#560 review C), injected first
	// and unconditionally: this hook is the ONE place every MCP HTTP
	// request's context is built, reached regardless of whether auth
	// middleware is configured or the awaiting-admin-credential gate later
	// restricts which tools are callable. Without it, an unauthenticated SO
	// PIN check here -- ceremony-initialize's admin-credential retry -- would
	// fall back to auth.CallerPrincipal's "" and share one lockout bucket
	// with every other unauthenticated source reaching this listener.
	if r != nil {
		ctx = auth.WithRemoteAddr(ctx, r.RemoteAddr)
	}
	// The SO second factor rides the HTTP request, and this hook is the one
	// place the tool path can see it. It is bound before the early return
	// below so a request authenticateHandler already authenticated carries
	// it too.
	ctx = withSOFactor(ctx, r)
	// The peer's TLS state and the listener's chain verifier, bound before
	// and independent of authentication: the trust pool verifies a client
	// certificate in the handshake without building VerifiedChains, and a
	// share claim resolves its verified certificate through it
	// (auth.VerifiedPeerCertificate) in the sealed window, which has no auth
	// middleware, as on the authenticated path.
	if r != nil && r.TLS != nil {
		ctx = auth.WithTLSState(ctx, r.TLS)
	}
	ctx = auth.WithPeerChainVerifier(ctx, s.chainVerifier())
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
	// authenticateHandler has already failed to authenticate this request
	// and admitted it anonymously; it stays anonymous.
	if admitted, _ := ctx.Value(anonymousAdmittedKey{}).(bool); admitted {
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
	ctx = auth.WithHTTPAuthorization(ctx, r.Header.Get("Authorization"))
	return auth.WithDPoPFromHTTP(ctx, r)
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
// HTTP layer (or 503/429 when the node could not finish checking it; see
// writeAuthenticationRefusal), so on a server that declares no public tool,
// initialize, ping and tools/list are never answered to an anonymous caller,
// and mcp-go never parses an anonymous body.
//
// A server whose modules declare public tools (apimodule.MCPPublicToolLister)
// answers exactly three things anonymously: the initialize handshake,
// tools/list -- filtered to the public set by visibleTools -- and a call to a
// public tool (admitAnonymous). Everything else is still refused 401. A
// request that does authenticate proceeds with its
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
		if err == nil {
			next.ServeHTTP(w, r.WithContext(authCtx))
			return
		}
		if anon, ok := s.admitAnonymous(r, err); ok {
			next.ServeHTTP(w, anon)
			return
		}
		writeAuthenticationRefusal(w, err, s.bearerChallenge)
	})
}

// mcpNotificationInitialized is the notification that completes the MCP
// initialize handshake; mcp-go names no constant for it.
const mcpNotificationInitialized = "notifications/initialized"

// anonymousRequest is the part of a JSON-RPC message the anonymous admission
// reads: the method, and the tool a tools/call names.
type anonymousRequest struct {
	Method string `json:"method"`
	Params struct {
		Name string `json:"name"`
	} `json:"params"`
}

// anonymousAdmission maps each JSON-RPC method an unauthenticated caller may
// send, on a server that declares public tools, to the rule that admits it.
// A method absent from the map -- every other request, a batch, a GET stream
// or a DELETE -- is refused as before.
var anonymousAdmission = map[string]func(s *Server, req anonymousRequest, authErr error) bool{
	string(mcpgo.MethodInitialize): admitWhenUnauthenticated,
	mcpNotificationInitialized:     admitWhenUnauthenticated,
	string(mcpgo.MethodToolsList):  admitWhenUnauthenticated,
	string(mcpgo.MethodToolsCall):  admitPublicToolCall,
}

// admitWhenUnauthenticated admits the handshake and the listing only when the
// caller is anonymous (or its credential was refused), never when the node
// could not finish checking a credential: that caller is told to retry, not
// handed the anonymous view of a server it may be entitled to see more of.
func admitWhenUnauthenticated(_ *Server, _ anonymousRequest, authErr error) bool {
	return authmw.HTTPStatus(authErr, http.StatusUnauthorized) == http.StatusUnauthorized
}

// admitPublicToolCall admits a call to a declared public tool whatever the
// authentication outcome, as REST serves a public route without consulting
// the auth chain at all.
func admitPublicToolCall(s *Server, req anonymousRequest, _ error) bool {
	return s.toolPerms.IsPublic(req.Params.Name)
}

// anonymousAdmittedKey marks a request admitted without an identity, so
// authHTTPContext does not walk the authenticator chain a second time.
type anonymousAdmittedKey struct{}

// admitAnonymous decides whether a request the auth chain did not
// authenticate is served anyway, because the server declares public tools
// and the request is one an anonymous caller may make: the initialize
// handshake, tools/list, or a call to a public tool. It reads the body,
// bounded at the anonymous cap, to learn the method, and hands back a request
// whose body replays what it read and whose context carries no identity.
// The per-tool gate still runs on every call, so an admitted request reaches
// nothing but the public set.
func (s *Server) admitAnonymous(r *http.Request, authErr error) (*http.Request, bool) {
	if !s.toolPerms.HasPublic() || r.Method != http.MethodPost || r.Body == nil {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAnonymousRequestBodyBytes+1))
	if err != nil || int64(len(body)) > maxAnonymousRequestBodyBytes {
		return nil, false
	}
	var req anonymousRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, false
	}
	admit, ok := anonymousAdmission[req.Method]
	if !ok || !admit(s, req, authErr) {
		return nil, false
	}
	anon := r.WithContext(context.WithValue(r.Context(), anonymousAdmittedKey{}, true))
	anon.Body = io.NopCloser(bytes.NewReader(body))
	return anon, true
}

// writeAuthenticationRefusal answers a request the auth chain refused with the
// status its failure class names, the same mapping REST applies
// (authmw.HTTPStatus). Only a failure to establish who the caller is gets 401;
// a node that could not finish checking a valid credential -- a tenant's user
// store slower than the lookup budget, an unreachable revocation source, a
// rate limit -- answers 503 or 429, with Retry-After when the error names a
// delay. Answering those 401 told a correctly enrolled caller its credential
// was bad, and an MCP client treats 401 at initialize as terminal.
//
// The bearer challenge rides only a 401: it tells an OAuth client to go and
// authorize, which is the wrong advice when the credential was never judged.
// The body is REST's JSON error ({"error": <the refusal>}), so the client
// learns why it was refused -- a suspended or disabled account, a revoked
// certificate -- exactly as a REST caller does.
func writeAuthenticationRefusal(w http.ResponseWriter, err error, bearerChallenge string) {
	status := authmw.HTTPStatus(err, http.StatusUnauthorized)
	if status == http.StatusUnauthorized {
		// Debug: a misconfigured client is diagnosable without log spam.
		slog.Debug("mcp: request authentication failed", "error", err)
		if bearerChallenge != "" {
			w.Header().Set("WWW-Authenticate", bearerChallenge)
		}
	} else {
		slog.Warn("mcp: request authentication could not complete", "status", status, "error", err)
	}
	if delay, ok := authmw.RetryAfter(err); ok {
		w.Header().Set("Retry-After", strconv.FormatInt(authmw.RetryAfterSeconds(delay), 10))
	}
	// The refusal's own message rides in REST's JSON error body, so a client
	// shows the same typed refusal on every transport.
	authmw.WriteJSONError(w, status, err.Error())
}

// defaultMaxRequestBodyBytes bounds an authenticated MCP HTTP request body
// when the server was not given WithMaxBodyBytes. It is the same pre-auth
// cap every other transport applies by default: mcp-go reads and decodes
// the whole body before any tool handler runs, so the bound has to be
// applied here, at the HTTP layer, and there is no larger-message opt-in
// (go-qrdb#576) -- a caller with a larger payload (bulk import) uses the
// chunked import session API instead of a single oversized request.
const defaultMaxRequestBodyBytes = int64(4 << 20)

// maxAnonymousRequestBodyBytes bounds an MCP request body on a listener with
// no authentication: the boot-time sealed-window listener, or a node with
// authentication off. Its callers are anonymous, and everything it serves
// them -- initialize, tools/list and the sealed-window tools -- is a small
// JSON-RPC document, so they get 1 MiB regardless of the authenticated cap.
const maxAnonymousRequestBodyBytes = int64(1 << 20)

// requestBodyLimit is the body bound for this server's MCP endpoint.
func (s *Server) requestBodyLimit() int64 {
	if s.authMW == nil || s.sealedWindowListener != nil {
		return maxAnonymousRequestBodyBytes
	}
	if s.maxBodyBytes > 0 {
		return s.maxBodyBytes
	}
	return defaultMaxRequestBodyBytes
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

// limitPublicTool applies the rate-limit dimension publicverbs.Table
// declares for a public tool before it is served without authentication.
// AuthorizeMCP admits a public tool before authorize() runs, so without
// this the verb's policy (admin:health for qrdb_health) would never be
// drawn on. It mirrors QUIC's limitPublic: the bucket is keyed by the
// caller's source address (host part), and a refusal is answered as the
// rate-limited tool error authzToolError produces. Tools that are not
// public, or whose RateLimit is empty, pass.
func limitPublicTool(ctx context.Context, m *authmw.AuthMiddleware, tools *authmw.MCPToolRegistry, toolName string) error {
	if m == nil || !tools.IsPublic(toolName) {
		return nil
	}
	for _, v := range publicverbs.Verbs {
		names := publicverbs.Table[v]
		if names.MCP != toolName || names.RateLimit == "" {
			continue
		}
		source, _ := auth.RemoteAddrFromContext(ctx)
		if host, _, err := net.SplitHostPort(source); err == nil {
			source = host
		}
		return m.CheckLoginRateLimit(ctx, "", source, names.RateLimit)
	}
	return nil
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

// beginTenantBarrierInit issues the init nonce through the shared
// trust-on-first-init rule (middleware.BeginTenantBarrierInit -- the single
// cross-transport home of the bootstrap-window semantics and the init
// proof). When authMW is nil the deployment is unauthenticated: there is no
// identity to prove, and the empty challenge says so.
func (s *Server) beginTenantBarrierInit(ctx context.Context, tenantID string) (*initproof.Challenge, error) {
	if s.authMW == nil {
		return &initproof.Challenge{}, nil
	}
	return s.authMW.BeginTenantBarrierInit(ctx, tenantID, s.tenantSvc)
}

// authorizeTenantBarrierInit authorizes a tenant barrier initialization and
// redeems its proof of possession (middleware.AuthorizeTenantBarrierInitProof).
// When authMW is nil the gate is a no-op (unauthenticated deployment).
func (s *Server) authorizeTenantBarrierInit(ctx context.Context, tenantID string, proof *authmw.TenantBarrierInitProof) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenantBarrierInitProof(ctx, tenantID, s.tenantSvc, proof)
}

// authorizeTenantBarrierSignedInit authorizes either half of a two-phase
// signed init as a plain init is authorized -- RBAC, or the tenant's
// trust-on-first-init window -- without the connection-bound proof: the
// signed statement, anchored to the tenant's first admin, is the proof of
// possession.
func (s *Server) authorizeTenantBarrierSignedInit(ctx context.Context, tenantID string) error {
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
			// The awaiting-admin-credential gate (go-qrdb#548 review item 3),
			// positioned before this middleware in the chain, already
			// decided this call needs no identity at all -- there is none to
			// authorize while the node is in that boot state.
			if awaitingAdminCredentialAdmitted(ctx) {
				return next(ctx, req)
			}
			// The SO PIN bound for this request is wiped once the call is
			// done, whether or not the tool was SO-gated.
			defer authmw.WipeSOFactor(ctx)
			if s.authMW != nil {
				if err := s.authMW.AuthorizeMCP(ctx, s.toolPerms, req.Params.Name); err != nil {
					slog.Warn("mcp: tool call denied",
						"tool", req.Params.Name, "error", err)
					return authzToolError(err), nil
				}
				if err := limitPublicTool(ctx, s.authMW, s.toolPerms, req.Params.Name); err != nil {
					slog.Warn("mcp: public tool call rate limited",
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

// visibleTools is the tools/list filter installed whenever authentication is
// configured: it narrows the listing to the tools the caller may call. An
// anonymous caller -- admitted only when the server declares public tools --
// sees the public set; an authenticated caller sees the public set plus every
// tool its grants cover (AuthMiddleware.MCPToolVisible). While the node is
// awaiting its admin credential, the tools that gate admits without an
// identity are listed too, because they are callable.
//
// The listing is advisory. Every call is still authorized by
// toolAuthzMiddleware, so a tool missing from the list grants nothing and a
// tool shown on it is refused if the caller's grants change in between.
func (s *Server) visibleTools(ctx context.Context, tools []mcpgo.Tool) []mcpgo.Tool {
	awaiting := s.awaitingAdminCredentialProbe != nil && s.awaitingAdminCredentialProbe.AwaitingAdminCredential()
	visible := make([]mcpgo.Tool, 0, len(tools))
	for _, t := range tools {
		if _, admitted := AwaitingAdminCredentialAllowlist[t.Name]; awaiting && admitted {
			visible = append(visible, t)
			continue
		}
		if s.authMW.MCPToolVisible(ctx, s.toolPerms, t.Name) {
			visible = append(visible, t)
		}
	}
	return visible
}

// filterToolList is the tools/list after-hook that narrows the listing a
// caller receives to visibleTools. The server sets no pagination limit, so
// the result holds the whole tool set.
func (s *Server) filterToolList(ctx context.Context, _ any, _ *mcpgo.ListToolsRequest, result *mcpgo.ListToolsResult) {
	result.Tools = s.visibleTools(ctx, result.Tools)
}

// registerPublicTools records every tool a module declares public
// (apimodule.MCPPublicToolLister) in the tool registry. It runs after every
// built-in and module permission registration, so the registry can refuse a
// declared tool that reaches data (*authmw.PublicMCPToolRefusedError); a
// declared name no tool answers is refused with *UnknownPublicToolError.
func (s *Server) registerPublicTools() error {
	// The built-in no-login tools are publicverbs.Table's, the same table
	// REST, gRPC and QUIC register theirs from. A row whose tool this
	// endpoint does not serve is skipped: invitation redemption is on the
	// separate invitation endpoint, and health is absent when the admin
	// service is.
	for name := range publicverbs.MCPTools() {
		if s.mcpSrv.GetTool(name) == nil {
			continue
		}
		if err := s.toolPerms.RegisterPublic(name); err != nil {
			return err
		}
	}
	for _, m := range s.modules {
		lister, ok := m.(apimodule.MCPPublicToolLister)
		if !ok {
			continue
		}
		for _, name := range lister.PublicMCPTools() {
			if s.mcpSrv.GetTool(name) == nil {
				return &UnknownPublicToolError{Tool: name}
			}
			if err := s.toolPerms.RegisterPublic(name); err != nil {
				return err
			}
		}
	}
	return nil
}
