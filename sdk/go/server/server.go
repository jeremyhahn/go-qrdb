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
	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	restapi "github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
	qrdbserver "github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// ---------------------------------------------------------------------------
// Embedded qrdb server options
//
// QuicBaaS composes an embedded qrdb server by supplying these functional
// options. Re-exported so the composer never imports pkg/server directly.
// ---------------------------------------------------------------------------

// ServerOption configures an embedded qrdb server at construction time.
type ServerOption = qrdbserver.ServerOption

// WithAuditLogger attaches a compliance-grade audit sink to the embedded
// qrdb node. Without it the node has no default audit logger and
// Client().Audit().EmitAudit returns a missing-logger error.
func WithAuditLogger(l sdk.NodeAuditLogger) ServerOption {
	return qrdbserver.WithAuditLogger(l)
}

// CertProviderConflictError is returned when a node is built with both
// node.WithCertProvider and node.WithCertKeyProviderFactory: the factory keys
// the built-in login CA the provider replaces, so the pair has no meaning.
// (Aliases pkg/server/lifecycle.CertProviderConflictError.)
type CertProviderConflictError = lifecycle.CertProviderConflictError

// WithStrategyRegistry attaches a seal strategy registry to the embedded
// qrdb server.
var WithStrategyRegistry = qrdbserver.WithStrategyRegistry

// WithBarrierKeyProvider attaches the BarrierKeyProvider the embedded qrdb
// server's tenant barriers resolve each node's sealing strategy through.
var WithBarrierKeyProvider = qrdbserver.WithBarrierKeyProvider

// WithHeadSignerProvider attaches the HeadSignerProvider the embedded qrdb
// server's verifiable logs create and sign with their head keys through.
var WithHeadSignerProvider = qrdbserver.WithHeadSignerProvider

// WithPINBackend attaches a PIN backend to the embedded qrdb server.
var WithPINBackend = qrdbserver.WithPINBackend

// WithFactorRegistry attaches an auth factor registry to the embedded qrdb
// server.
var WithFactorRegistry = qrdbserver.WithFactorRegistry

// WithCeremonyCA attaches the certificate authority the init ceremony uses to
// issue the TLS serving certificate, sign officer CSRs, and publish the CA
// bundle / SPKI pin. Composers supply go-xkms's CAAdapter — the stack's single
// hardware-backed CA — via sdk/go NewCAAdapter. Without it every CA-dependent
// ceremony verb fails closed with ErrCeremonyCANotInitialized. The argument
// type is the re-exported CeremonyCA (see types_ceremony.go).
var WithCeremonyCA = qrdbserver.WithCeremonyCA

// WithServerTLSKey sets the crypto.Signer behind the TLS serving leaf the
// transports auto-provision from the CeremonyCA. Composers pass a device-held
// signer so the leaf key never leaves the device; unset, each leaf gets a
// fresh in-memory P-256 key.
var WithServerTLSKey = qrdbserver.WithServerTLSKey

// ---------------------------------------------------------------------------
// Pre-auth resource bounds (go-qrdb#576)
//
// The unified server's gRPC/Unix and MCP listeners buffer an incoming
// message or request body before any interceptor or auth hook runs, so their
// bounds have to be set at the listener, not recovered after the fact. Every
// consumer that serves through them -- go-xkms included -- gets the
// conservative default unless it opts in through these.
// ---------------------------------------------------------------------------

// WithGRPCMaxConcurrentStreams overrides the TCP gRPC and Unix socket
// servers' default cap on simultaneously active RPCs per HTTP/2 connection.
var WithGRPCMaxConcurrentStreams = qrdbserver.WithGRPCMaxConcurrentStreams

// WithMCPMaxConnections overrides the MCP server's default cap on
// simultaneously open connections to its /mcp listener.
var WithMCPMaxConnections = qrdbserver.WithMCPMaxConnections

// DefaultGRPCMaxMessageBytes is the TCP gRPC and Unix socket servers' max
// message size, applied to every RPC when the embedder's configuration
// supplies no `transport.max_message_bytes` override (go-qrdb#576).
const DefaultGRPCMaxMessageBytes = grpcapi.DefaultMaxMessageBytes

// DefaultGRPCMaxConcurrentStreams is the TCP gRPC and Unix socket servers'
// cap on simultaneously active RPCs per HTTP/2 connection when
// WithGRPCMaxConcurrentStreams is not used.
const DefaultGRPCMaxConcurrentStreams = grpcapi.DefaultMaxConcurrentStreams

// DefaultMCPMaxConnections is the MCP server's cap on simultaneously open
// connections when WithMCPMaxConnections is not used.
const DefaultMCPMaxConnections = mcpapi.DefaultMaxConnections

// ---------------------------------------------------------------------------
// Capability modules
//
// A higher layer (e.g. go-xkms) registers its capabilities as a ServiceModule
// via WithModule, instead of standing up a parallel transport stack. The
// module's gRPC/HTTP(REST)/MCP surfaces are exposed by qrdb's own unified
// server, gated by qrdb's authorizer.
// ---------------------------------------------------------------------------

// ServiceModule is a self-contained capability registered into the embedded
// qrdb server via WithModule. See pkg/apimodule for the contract.
type ServiceModule = apimodule.ServiceModule

// BaseModule provides no-op defaults for every ServiceModule hook except Name;
// embed it and override only what applies.
type BaseModule = apimodule.BaseModule

// ModuleHTTPRegistrar is the permission-recording HTTP registration surface the
// unified server passes to ServiceModule.RegisterHTTP. Routes registered via
// Handle declare their permission at the registration site; HandlePublic marks
// genuine first-contact surfaces; HandleOptional authenticates a caller that
// presents a credential and admits one that presents none anonymous, the
// handler telling them apart with IdentityFromContext. It embeds the live
// chi.Router.
type ModuleHTTPRegistrar = apimodule.HTTPRegistrar

// ModuleMCPToolPermissioner is the optional ServiceModule extension through
// which a module declares its MCP tools' permissions, admitting them into
// the unified server's single MCP authorization path.
type ModuleMCPToolPermissioner = apimodule.MCPToolPermissioner

// ModuleGRPCPublicMethodLister is the optional ServiceModule extension
// through which a module declares which of its gRPC methods (registered via
// RegisterGRPC) must be reachable without authentication, admitting them
// into the unified server's gRPC and unix transports the same way
// HTTPRegistrar.HandlePublic admits unauthenticated REST routes.
type ModuleGRPCPublicMethodLister = apimodule.GRPCPublicMethodLister

// ModuleMCPPublicToolLister is the optional ServiceModule extension through
// which a module declares which MCP tools must be reachable without
// credentials (health, liveness, readiness, login). The unified MCP server
// serves them, plus initialize and tools/list, to an unauthenticated caller,
// and refuses at startup a declared tool that reaches data.
type ModuleMCPPublicToolLister = apimodule.MCPPublicToolLister

// TenantTargetAuthorizer is the minimal capability a module needs to
// authorize its own handler against a request-supplied target tenant,
// under the host's RBAC ScopeCross/ScopeSelf rules (go-qrdb#587). Satisfied
// by the AuthMiddleware every network transport already authorizes
// against; a module never builds one itself.
type TenantTargetAuthorizer = apimodule.TenantTargetAuthorizer

// ModuleTenantTargetAware is the optional ServiceModule extension through
// which a module receives its TenantTargetAuthorizer, once, before any of
// its Register* hooks run. A module implementing it can authorize a
// handler registered on ANY transport (REST, gRPC, unix, QUIC, MCP)
// against a caller-named target tenant, instead of being confined to the
// request's own identity tenant. The authorizer handed over is nil when
// the host's auth system is disabled -- treat that exactly as every
// built-in tenant route does: admit unconditionally, never refuse.
type ModuleTenantTargetAware = apimodule.ModuleTenantTargetAware

// ModuleConnObserver is the optional ServiceModule extension through which a
// module observes the connection lifecycle on every listener of the unified
// server: each accepted connection is reported once to ConnOpened and, when
// it ends, once to ConnClosed, with the ModuleTransport it arrived on. It lets
// a composing host keep active-connection metrics for the connections qrdb
// accepts on its behalf. Both methods run synchronously on the accept and
// close paths and must not block.
type ModuleConnObserver = apimodule.ConnObserver

// ModuleTransport names the listener a ModuleConnObserver event came from.
type ModuleTransport = apimodule.Transport

// The listeners a ModuleConnObserver hears from. REST is reported per socket:
// ModuleTransportREST for TCP (HTTP/1.1, HTTP/2) and ModuleTransportHTTP3 for
// QUIC.
const (
	ModuleTransportGRPC  = apimodule.TransportGRPC
	ModuleTransportUnix  = apimodule.TransportUnix
	ModuleTransportREST  = apimodule.TransportREST
	ModuleTransportHTTP3 = apimodule.TransportHTTP3
	ModuleTransportMCP   = apimodule.TransportMCP
	ModuleTransportQUIC  = apimodule.TransportQUIC
)

// WithModule registers a capability module into the embedded qrdb server. Its
// gRPC/REST/MCP/QUIC surfaces are attached to every transport at startup and
// its Start/Stop lifecycle runs with the server's.
var WithModule = qrdbserver.WithModule

// gRPC-Web on the embedded server's REST listener. WithGRPCWeb serves the
// gRPC-Web protocol for every gRPC service the server hosts -- its own and
// every WithModule module's -- under /grpc-web/, on HTTP/1.1, HTTP/2 and
// HTTP/3, through the gRPC server's own interceptor chain, replacing the
// configuration file's server.grpc_web block. It is off by default. Start
// refuses an AllowedOrigins entry that is neither "*" nor a serialized origin
// (scheme://host[:port]), and "*" with AllowCredentials, with a
// *DaemonConfigValidationError naming the server.grpc_web field.
type (
	// GRPCWebConfig configures gRPC-Web: Enabled, and the CORS policy a
	// browser client needs (AllowedOrigins, AllowCredentials), off when
	// AllowedOrigins is empty. The zero value is off.
	GRPCWebConfig = config.GRPCWebConfig
	// GRPCWebUnavailableError is Start's refusal of gRPC-Web enabled while
	// protocols.grpc or protocols.rest is off.
	GRPCWebUnavailableError = qrdbserver.GRPCWebUnavailableError
)

// WithGRPCWeb enables gRPC-Web with cfg; see GRPCWebConfig.
var WithGRPCWeb = qrdbserver.WithGRPCWeb

type (
	// WebTransportSession is a WebTransport session
	// (draft-ietf-webtrans-http3-16) on the REST HTTP/3 listener.
	WebTransportSession = restapi.WebTransportSession
	// WebTransportSessionErrorCode is the application error code a session is
	// closed with and a WebTransportSessionError carries.
	WebTransportSessionErrorCode = restapi.WebTransportSessionErrorCode
	// WebTransportStreamErrorCode is the application error code a stream is
	// cancelled with and a WebTransportStreamError carries.
	WebTransportStreamErrorCode = restapi.WebTransportStreamErrorCode
	// WebTransportSessionError is the error a closed session's operations
	// return.
	WebTransportSessionError = restapi.WebTransportSessionError
	// WebTransportStreamError is the error a cancelled stream's operations
	// return.
	WebTransportStreamError = restapi.WebTransportStreamError
	// WebTransportHandler serves one WebTransport session, given the caller
	// the REST authentication chain admitted and the tenant it bound.
	WebTransportHandler = restapi.WebTransportHandler
	// InvalidWebTransportHandlerError is Start's refusal of a
	// WithWebTransportHandler given a nil handler or a relative path.
	InvalidWebTransportHandlerError = restapi.InvalidWebTransportHandlerError
)

// WithWebTransportHandler serves WebTransport sessions on path of the REST
// HTTP/3 listener, handing each to h once its CONNECT has passed the REST
// authentication and tenant-binding chain. Without it the listener
// advertises no WebTransport SETTINGS.
var WithWebTransportHandler = qrdbserver.WithWebTransportHandler

// NewModuleHTTPRegistrar builds the permission-recording registrar the server
// passes to ServiceModule.RegisterHTTP. Exported for module hosts and module
// tests that need a capture registrar without importing pkg/apimodule.
var NewModuleHTTPRegistrar = apimodule.NewHTTPRegistrar

// The REST route-ownership guard (go-qrdb#194). The embedded server refuses,
// at construction, any second claim on a REST route -- a module over a
// built-in or over another module -- and a module route registered directly on
// the router that a permission-recorded route also claims. These are the panic
// values it raises, and the route identity it decides "the same route" by, so
// a host can recover and report them without importing pkg/api/rest.
type (
	// ModuleRouteClaim names one route a module registered directly on the
	// embedded router (ModuleHTTPRegistrar.DirectRoutes).
	ModuleRouteClaim = apimodule.RouteClaim
	// ModuleRegistrationClosedError is the panic of a registration made after
	// the module's RegisterHTTP returned.
	ModuleRegistrationClosedError = apimodule.RegistrationClosedError
	// ModuleUnsupportedRouterMethodError is the panic of a chi.Router method
	// a module may not call on the registrar's router: Mount, Route,
	// NotFound, MethodNotAllowed or Use.
	ModuleUnsupportedRouterMethodError = apimodule.UnsupportedRouterMethodError
	// RESTRouteCollisionError is the panic of a second claim on a REST route.
	RESTRouteCollisionError = restapi.RouteCollisionError
	// RESTDirectRouteCollisionError is the panic of a direct router
	// registration that a permission-recorded registration also claims.
	RESTDirectRouteCollisionError = restapi.DirectRouteCollisionError
	// RESTGRPCWebRouteCollisionError is the panic of a REST route that
	// would match a path under /grpc-web/ while gRPC-Web is enabled.
	RESTGRPCWebRouteCollisionError = restapi.GRPCWebRouteCollisionError
)

// RESTRouteKey is the identity of a REST route: method plus pattern with
// path-parameter names erased and regexp constraints kept -- the identity chi
// routes by, and the one the collision guard compares.
var RESTRouteKey = authmw.RESTRouteKey

// WithServerTLS supplies the TLS configuration the embedded server's
// network transports serve with. qrdb transports are TLS-only: without this
// option (and without a CeremonyCA to auto-provision a certificate) the
// server refuses to start a network transport. Module hosts that terminate
// TLS with their own certificate material pass it here.
var WithServerTLS = qrdbserver.WithTLS

// WithBootstrapCABundle makes the host the source of the bundle qrdb serves
// on GET /v1/ca/bootstrap, read per request: a host that owns first-contact
// trust (go-xkms's xkmsd) supplies the data, and qrdb owns the one route, as
// for /health. Only CERTIFICATE blocks are served. With neither this nor
// qrdb's cluster CA configured the path is not registered at all; both
// together are refused at Start with *CABootstrapConflictError.
var WithBootstrapCABundle = qrdbserver.WithBootstrapCABundle

// CABootstrapConflictError refuses a server given both qrdb's cluster CA and
// a host bundle for /v1/ca/bootstrap.
type CABootstrapConflictError = restapi.CABootstrapConflictError

// WithRESTListener hands the embedded server a TCP listener the caller already
// bound, which it serves REST on instead of binding server.admin_port. Bind
// ":0" and read the port back to get a kernel-assigned port with no window in
// which another process can take it. The server owns the listener from Start.
var WithRESTListener = qrdbserver.WithRESTListener

// WithRESTPacketConn hands the embedded server the UDP socket to serve HTTP/3
// on; it must sit on the WithRESTListener listener's host and port.
var WithRESTPacketConn = qrdbserver.WithRESTPacketConn

// WithGRPCListener hands the embedded server a TCP listener the caller already
// bound, which it serves gRPC on instead of binding server.grpc_port.
var WithGRPCListener = qrdbserver.WithGRPCListener

// WithQUICPacketConn hands the embedded server a UDP socket the caller already
// bound, which it serves the QUIC transport on instead of binding
// server.quic_port.
var WithQUICPacketConn = qrdbserver.WithQUICPacketConn

// WithMCPListener hands the embedded server a TCP listener the caller already
// bound, which it serves MCP on instead of binding mcp.port.
var WithMCPListener = qrdbserver.WithMCPListener

// ModuleAuthFallback is the optional ServiceModule extension for capability
// session-token planes: when the unified server's REST middleware finds no
// qrdb identity on a request targeting the module's routes, it invokes the
// owning module's fallback; the returned identity is enriched and
// authorized by the same store-resolved path as an mTLS identity.
type ModuleAuthFallback = apimodule.ModuleAuthFallback
