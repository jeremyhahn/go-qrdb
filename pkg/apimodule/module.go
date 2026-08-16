// Package apimodule defines the ServiceModule seam: a self-contained capability
// that is registered exactly once into go-qrdb's unified multi-protocol server
// and thereby exposed across every network transport (gRPC, unix, REST, QUIC and
// MCP) at parity, gated by the same authorizer.
//
// It generalizes the existing narrow registries (StrategyRegistry, FactorRegistry,
// PINBackend): a higher layer (e.g. go-xkms) injects a ServiceModule via
// server.WithModule instead of standing up its own parallel transport stack. The
// module's operations are Tier 1 (data/control plane, all transports); the
// injection itself is Tier 2 (in-process functional option), per the ownership
// constitution's Tier 1 + Tier 2 pairing.
package apimodule

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"google.golang.org/grpc"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// HTTPRegistrar is the unified server's HTTP registration surface for modules.
// It embeds the live chi.Router (full router capability — subrouters, chi
// middleware) and adds the permission-recording registration the server's own
// routes use: every module route declares its permission at the registration
// site, feeding the same authorization registry and the same default-deny
// startup assertion as the built-in routes. A route registered directly on the
// embedded chi.Router without Handle/HandlePublic is caught by that assertion
// at startup — fail-closed, never silently public.
type HTTPRegistrar struct {
	chi.Router

	route  func(method, pattern string, perm permission.Permission, h http.HandlerFunc)
	public func(method, pattern string, h http.HandlerFunc)
}

// NewHTTPRegistrar builds the registrar the server passes to modules. route and
// public are the server's own permission-recording registration helpers.
func NewHTTPRegistrar(
	r chi.Router,
	route func(method, pattern string, perm permission.Permission, h http.HandlerFunc),
	public func(method, pattern string, h http.HandlerFunc),
) *HTTPRegistrar {
	return &HTTPRegistrar{Router: r, route: route, public: public}
}

// Handle registers an authorized module route: the handler is served on REST
// (and, when the HTTP surface is shared, QUIC/HTTP3) and the request must carry
// an identity authorized for perm.
func (h *HTTPRegistrar) Handle(method, pattern string, perm permission.Permission, handler http.HandlerFunc) {
	h.route(method, pattern, perm, handler)
}

// HandlePublic registers an unauthenticated module route. Reserve it for
// genuine first-contact surfaces (health, trust bootstrap); everything else
// uses Handle with a permission.
func (h *HTTPRegistrar) HandlePublic(method, pattern string, handler http.HandlerFunc) {
	h.public(method, pattern, handler)
}

// ServiceModule is a capability registered into the unified server. The server
// invokes each hook while standing up the matching transport(s), so one
// registration reaches multiple transports:
//
//	RegisterGRPC -> gRPC AND unix (unix serves the same gRPC wire over a UDS)
//	RegisterHTTP -> REST (chi)
//	RegisterMCP  -> MCP (the real mcp-go server)
//
// Transport coverage caveats (see plans/unified-api-server-2026-08-14.md):
//   - QUIC: go-qrdb's QUIC transport is a bespoke frame protocol (method-keyed
//     dispatch), not HTTP-over-chi, so a module is NOT reached over QUIC by these
//     hooks. Module QUIC parity would need a separate RegisterQUIC hook and is a
//     deferred follow-up.
//   - embedded: the in-process transport is each capability owner's own SDK
//     surface (e.g. go-xkms's sdk/go/embedded), independent of this seam.
//
// Permissions are NOT declared here: an operation's required permission lives on
// its gRPC method (the proto option, enforced by the shared interceptor) and is
// referenced by the chi route and MCP tool. Embed BaseModule and override only the
// hooks a module actually uses.
type ServiceModule interface {
	// Name uniquely identifies the module (logs, duplicate detection, metrics).
	Name() string

	// RegisterGRPC attaches the module's gRPC service(s) to the shared server by
	// calling the module's own generated pb.Register<X>Server(reg, impl). Invoked
	// once per gRPC-wire transport (gRPC and unix).
	RegisterGRPC(reg grpc.ServiceRegistrar)

	// RegisterHTTP attaches the module's routes through the server's
	// permission-recording registrar: each route declares its permission at the
	// registration site (Handle) or is explicitly public (HandlePublic), feeding
	// the same authorization registry and default-deny startup assertion as the
	// server's built-in routes.
	RegisterHTTP(r *HTTPRegistrar)

	// RegisterMCP adds the module's tools (and resources/prompts, if any) to the
	// shared MCP server.
	RegisterMCP(m *mcpserver.MCPServer)

	// Start acquires the module's background resources (connection pools, hardware
	// handles, timers) before the server begins serving; it runs after all
	// transports register and before they accept traffic.
	Start(ctx context.Context) error

	// Stop releases the module's resources during graceful shutdown (LIFO with the
	// server's own stop).
	Stop(ctx context.Context) error
}

// MCPToolPermissioner is an optional ServiceModule extension. A module that
// registers MCP tools implements it to declare each tool's required
// permission; the unified server feeds the declarations into its MCP tool
// registry so the authorization middleware can authorize module tool calls
// instead of default-denying them. Use permission.IdentityGated for tools
// whose authorization is deferred to the module's own handler. A module
// tool registered via RegisterMCP without a matching declaration here stays
// fail-closed on an authenticated server.
type MCPToolPermissioner interface {
	MCPToolPermissions() map[string]permission.Permission
}

// GRPCPublicMethodLister is an optional ServiceModule extension. A module
// whose gRPC surface (registered via RegisterGRPC) includes methods that must
// be reachable without authentication implements it to declare those methods
// by their full gRPC method name ("/package.Service/Method", the same string
// grpc.UnaryServerInfo.FullMethod / grpc.StreamServerInfo.FullMethod carry).
// The unified server feeds the declarations into the gRPC auth middleware
// (AuthMiddleware.RegisterPublicGRPCMethods) so those exact methods bypass
// both authentication and authorization on the gRPC and unix transports,
// symmetric with HTTPRegistrar.HandlePublic on REST. Reserve it for genuine
// first-contact or legacy-unauthenticated surfaces (health, version);
// everything else is reached through the RPC's (qrdb.v1.permission) proto
// option. A malformed method name is rejected at registration time so a typo
// fails server startup loudly instead of silently staying auth-gated.
type GRPCPublicMethodLister interface {
	PublicGRPCMethods() []string
}

// BaseModule provides no-op defaults for every hook except Name, so a module
// embeds it and overrides only what applies. It does not implement Name, forcing
// each module to supply its own identity.
type BaseModule struct{}

// RegisterGRPC is a no-op default.
func (BaseModule) RegisterGRPC(grpc.ServiceRegistrar) {}

// RegisterHTTP is a no-op default.
func (BaseModule) RegisterHTTP(*HTTPRegistrar) {}

// RegisterMCP is a no-op default.
func (BaseModule) RegisterMCP(*mcpserver.MCPServer) {}

// Start is a no-op default.
func (BaseModule) Start(context.Context) error { return nil }

// Stop is a no-op default.
func (BaseModule) Stop(context.Context) error { return nil }
