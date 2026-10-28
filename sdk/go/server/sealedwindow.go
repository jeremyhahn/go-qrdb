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

package server

import (
	"net/http"

	mcpserver "github.com/mark3labs/mcp-go/server"

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	quicapi "github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	restapi "github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	qrdbserver "github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// ---------------------------------------------------------------------------
// Sealed-window composition surface
//
// Re-exported so a composing service (e.g. go-quicbaas) can install the
// sealed-window gate on an embedded qrdb server — and mirror it onto its
// own transports — without importing pkg/server or pkg/api/* directly.
// ---------------------------------------------------------------------------

// SealedWindowProbe reports whether the storage barrier is currently
// sealed. *seal.StorageBarrier satisfies this directly via its IsSealed
// method; tests may substitute a deterministic fake.
type SealedWindowProbe = qrdbserver.SealedWindowProbe

// WithSealedWindowProbe installs the sealed-window middleware /
// interceptors on every protocol server of an embedded qrdb server.
// While the probe reports sealed, every endpoint not on the canonical
// allowlist returns 503 / Unavailable with the documented sealed
// envelope.
var WithSealedWindowProbe = qrdbserver.WithSealedWindowProbe

// AuthMiddleware is the qrdb authentication / authorization middleware
// chain shared by every protocol server.
type AuthMiddleware = authmw.AuthMiddleware

// AuthMiddlewareOption configures an AuthMiddleware. (Aliases
// pkg/auth/middleware.Option.)
type AuthMiddlewareOption = authmw.Option

// NewAuthMiddleware builds the middleware chain over an Authorizer, for a
// consumer that puts qrdb's authentication and authorization in front of its
// own router: wrap the router with its RESTMiddleware method over a
// RESTRouteRegistry naming every route's permission. A middleware built
// without WithAuthMiddlewareRoleResolver refuses every authenticated request.
// (Aliases pkg/auth/middleware.New.)
var NewAuthMiddleware = authmw.New

// WithAuthMiddlewareAuthenticator appends an Authenticator to the chain.
// Authenticators are tried in the order they are added; the first to succeed
// provides the identity. (Aliases pkg/auth/middleware.WithAuthenticator.)
var WithAuthMiddlewareAuthenticator = authmw.WithAuthenticator

// WithAuthMiddlewareUserService configures the user store an authenticated
// subject is resolved against: it supplies the identity's persistent roles
// and enforces the account's lifecycle, certificate-binding and
// permitted-method checks. (Aliases pkg/auth/middleware.WithUserService.)
var WithAuthMiddlewareUserService = authmw.WithUserService

// WithAuthMiddlewareRoleResolver configures the role store the caller's
// TenantOwner is decided against, read uncached on every authenticated
// request. It is required. (Aliases pkg/auth/middleware.WithRoleResolver.)
var WithAuthMiddlewareRoleResolver = authmw.WithRoleResolver

// RESTRouteRegistry records each REST route's authorization contract -- its
// permission, or an explicit public, authenticated-only or optional marker --
// which AuthMiddleware.RESTMiddleware enforces. A registered path requested
// with a method it has no entry for is refused; a path with no entry at all
// needs an authenticated caller and no permission. (Aliases
// pkg/auth/middleware.RESTRouteRegistry.)
type RESTRouteRegistry = authmw.RESTRouteRegistry

// NewRESTRouteRegistry creates an empty route registry. (Aliases
// pkg/auth/middleware.NewRESTRouteRegistry.)
var NewRESTRouteRegistry = authmw.NewRESTRouteRegistry

// WithAuthMiddleware configures authentication and authorization
// middleware for the protocol servers of an embedded qrdb server.
var WithAuthMiddleware = qrdbserver.WithAuthMiddleware

// ServeSealedWindow brings up an API-only sealed-window listener and
// blocks until the probe reports unsealed or ctx is canceled. See
// pkg/server.ServeSealedWindow; SDK node embedders normally use the
// higher-level node.ServeSealedWindow instead.
var ServeSealedWindow = qrdbserver.ServeSealedWindow

// SealedWindowCanceledError is returned by ServeSealedWindow when ctx is
// canceled before the operator drives the barrier unsealed.
type SealedWindowCanceledError = qrdbserver.SealedWindowCanceledError

// ---------------------------------------------------------------------------
// Per-transport sealed-window allowlists
//
// All four lists are derived from the canonical transport-neutral verb
// table in pkg/api/sealedwindow, so they cannot drift apart.
// ---------------------------------------------------------------------------

// RESTSealedWindowAllowlist is the REST sealed-window allowlist. Entries
// ending in "/" are prefix matches; everything else matches exactly.
var RESTSealedWindowAllowlist = restapi.SealedWindowAllowlist

// GRPCSealedWindowAllowlist is the gRPC sealed-window allowlist. Entries are
// fully service-qualified gRPC methods (the complete FullMethod path, e.g.
// "/qrdb.v1.QRDBService/BarrierQuorumSubmit") — NOT bare method tails. Matching the
// full path prevents a same-named method on a co-registered service from being
// admitted while the barrier is sealed.
var GRPCSealedWindowAllowlist = grpcapi.SealedWindowAllowlist

// QUICSealedWindowAllowlist is the native QUIC sealed-window allowlist,
// keyed by method name (e.g. "barrier.Unseal").
var QUICSealedWindowAllowlist = quicapi.SealedWindowAllowlist

// MCPSealedWindowAllowlist is the MCP sealed-window allowlist, keyed by
// tool name (e.g. "qrdb_barrier_unseal").
var MCPSealedWindowAllowlist = mcpapi.SealedWindowAllowlist

// SealedWindowRESTAllowlist derives a fresh REST allowlist from the
// canonical verb table (useful for composing with project-specific
// extensions without mutating the shared package var).
var SealedWindowRESTAllowlist = sealedwindow.RESTAllowlist

// SealedWindowRESTEntryMatches reports whether one REST allowlist entry
// admits a request: an optional "METHOD " qualifier, then an exact path, a
// prefix ending in "/", or a pattern with "{name}" segments. It is the
// matcher the REST sealed-window gate applies, so an embedder testing an
// extended allowlist matches entries exactly as the gate does.
var SealedWindowRESTEntryMatches = sealedwindow.RESTEntryMatches

// SealedWindowMiddleware returns an http middleware that 503s every
// request not on the allowlist while the supplied barrier probe reports
// sealed. Pass nil for the allowlist to use the canonical REST list.
var SealedWindowMiddleware = restapi.SealedWindowMiddleware

// SealedWindowUnaryInterceptor returns a grpc.UnaryServerInterceptor
// that rejects every method not on the allowlist with codes.Unavailable
// while the supplied barrier probe reports sealed. Pass nil for the
// allowlist to use the canonical gRPC list.
var SealedWindowUnaryInterceptor = grpcapi.SealedWindowUnaryInterceptor

// SealedWindowStreamInterceptor is the streaming-RPC sibling of
// SealedWindowUnaryInterceptor.
var SealedWindowStreamInterceptor = grpcapi.SealedWindowStreamInterceptor

// SealedWindowListenerProbe is what a boot-time sealed-window listener's
// gate reads per request: the barrier's sealed state and whether anonymous
// officer enrollment is still open.
type SealedWindowListenerProbe = sealedwindow.ListenerProbe

// SealedWindowListenerMiddleware is the REST gate of a boot-time
// sealed-window listener, a listener with no authentication behind it: the
// allowlist while the barrier probe reports sealed, and a 503 for every
// request once it reports unsealed.
var SealedWindowListenerMiddleware = restapi.SealedWindowListenerMiddleware

// SealedWindowListenerUnaryInterceptor is the gRPC unary gate of a
// boot-time sealed-window listener: the allowlist while sealed, and
// codes.Unavailable for every method once unsealed.
var SealedWindowListenerUnaryInterceptor = grpcapi.SealedWindowListenerUnaryInterceptor

// SealedWindowListenerStreamInterceptor is the streaming-RPC sibling of
// SealedWindowListenerUnaryInterceptor.
var SealedWindowListenerStreamInterceptor = grpcapi.SealedWindowListenerStreamInterceptor

// ---------------------------------------------------------------------------
// Seal-only service graph (pre-host sealed window)
// ---------------------------------------------------------------------------

// LifecycleInfra carries the infrastructure dependencies consumed by the
// lifecycle service builders (BuildSealServicesOnly and the full
// BuildServices graph).
type LifecycleInfra = lifecycle.Infra

// LifecycleNode aggregates the service instances produced by the
// lifecycle builders. node.Node.Services() returns this type.
type LifecycleNode = lifecycle.Node

// BuildSealServicesOnly constructs the subset of seal-gated services
// that do not require a Raft host: Barrier, Credential, Ceremony, and
// (when a BarrierRegistry is supplied) Tenant. Intended for the pre-host
// sealed-window boot phase.
var BuildSealServicesOnly = lifecycle.BuildSealServicesOnly

// NewSealedWindowListenerProbe returns the probe that gates a sealed-window
// surface: sealed reports the barrier's state and ceremony (which may be nil)
// decides whether anonymous officer enrollment is still open. Build ONE per
// sealed window and pass it to NewSealedWindowHandler, SealedWindowMCPTools
// and the gRPC listener interceptors, so every transport shares one
// admission and none serves a request anonymously against a barrier another
// transport has just unsealed.
var NewSealedWindowListenerProbe = qrdbserver.NewSealedWindowListenerProbe

// NewSealedWindowHandler returns qrdb's sealed REST surface as an
// http.Handler for a host to mount on its own listener (HTTP/3 included):
// the sealed surface -- initialize and unseal, Shamir initialization,
// officer registration and share claims, the unseal quorum, the first-boot
// ceremony verbs -- served anonymously while the barrier is sealed, every
// other route (GET /health and barrier status included) answering the typed
// sealed refusal (503, code SEALED), and everything refused once the barrier
// is unsealed. It is the
// handler qrdb's own sealed listener serves, so a host mounts it instead of
// copying sealed-window handlers. Any service may be nil; its routes then
// answer as they do on a server without it. Build probe with
// NewSealedWindowListenerProbe.
func NewSealedWindowHandler(barrier sdk.BarrierServicer, ceremony sdk.ServiceCeremonyServicer, credential sdk.ServiceCredentialServicer, probe SealedWindowListenerProbe) http.Handler {
	return restapi.NewSealedWindowHandler(barrier, ceremony, credential, probe)
}

// SealedWindowMCPTools returns qrdb's sealed MCP tools -- every tool on the
// sealed allowlist the services can serve, which has no health or status
// tool because a sealed node answers the unseal call and nothing else -- for a
// host to add to its own MCP server with AddTools. Each handler carries the
// sealed gate and qrdb's error coding, so the tools answer exactly as on
// qrdb's own sealed listener. Pass the same probe as NewSealedWindowHandler.
func SealedWindowMCPTools(barrier sdk.BarrierServicer, ceremony sdk.ServiceCeremonyServicer, credential sdk.ServiceCredentialServicer, probe SealedWindowListenerProbe) []mcpserver.ServerTool {
	return mcpapi.SealedWindowTools(barrier, ceremony, credential, probe)
}
