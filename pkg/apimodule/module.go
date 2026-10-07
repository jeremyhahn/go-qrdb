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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// HTTPRegistrar is the unified server's HTTP registration surface for modules.
// It embeds the live chi.Router (full router capability — subrouters, chi
// middleware) and adds the permission-recording registration the server's own
// routes use: every module route declares its permission at the registration
// site, feeding the same authorization registry and the same default-deny
// startup assertion as the built-in routes.
//
// A route registered directly on the embedded chi.Router, bypassing
// Handle/HandlePublic, is RECORDED (DirectRoutes). One whose pattern no
// permission covers fails the default-deny assertion at startup; one that
// collides with a route registered through Handle/HandlePublic, or with a
// built-in, fails construction too (go-qrdb#194) — chi would otherwise
// silently serve the direct handler under the other registration's
// permission. Registration closes when RegisterHTTP returns (Freeze): a
// module that keeps the registrar or its router and registers later panics
// with *RegistrationClosedError rather than presenting an unvalidated surface.
type HTTPRegistrar struct {
	chi.Router

	route  func(method, pattern string, perm permission.Permission, h http.HandlerFunc)
	public func(method, pattern string, h http.HandlerFunc)
	rec    *routeRecorder
}

// NewHTTPRegistrar builds the registrar the server passes to modules. route and
// public are the server's own permission-recording registration helpers. The
// embedded router forwards to r and records direct registrations; a nil r
// leaves it nil.
func NewHTTPRegistrar(
	r chi.Router,
	route func(method, pattern string, perm permission.Permission, h http.HandlerFunc),
	public func(method, pattern string, h http.HandlerFunc),
) *HTTPRegistrar {
	rec := &routeRecorder{}
	h := &HTTPRegistrar{route: route, public: public, rec: rec}
	if r != nil {
		h.Router = &recordingRouter{Router: r, rec: rec}
	}
	return h
}

// Handle registers an authorized module route: the handler is served on REST
// (and, when the HTTP surface is shared, QUIC/HTTP3) and the request must carry
// an identity authorized for perm.
func (h *HTTPRegistrar) Handle(method, pattern string, perm permission.Permission, handler http.HandlerFunc) {
	h.rec.admit(method, pattern)
	h.route(method, pattern, perm, handler)
}

// HandlePublic registers an unauthenticated module route. Reserve it for
// genuine first-contact surfaces (health, trust bootstrap); everything else
// uses Handle with a permission.
func (h *HTTPRegistrar) HandlePublic(method, pattern string, handler http.HandlerFunc) {
	h.rec.admit(method, pattern)
	h.public(method, pattern, handler)
}

// DirectRoutes returns the routes registered straight on the embedded router
// (or on an inline router from its With / Group), bypassing Handle and
// HandlePublic, in registration order. The server checks each against the
// routes whose permission was recorded.
func (h *HTTPRegistrar) DirectRoutes() []RouteClaim {
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	out := make([]RouteClaim, len(h.rec.direct))
	copy(out, h.rec.direct)
	return out
}

// Freeze closes registration. The server calls it when the module's
// RegisterHTTP returns; every later registration through this registrar or any
// router it handed out panics with *RegistrationClosedError.
func (h *HTTPRegistrar) Freeze() {
	h.rec.closed.Store(true)
}

// ServiceModule is a capability registered into the unified server. The server
// invokes each hook while standing up the matching transport(s), so one
// registration reaches multiple transports:
//
//	RegisterGRPC -> gRPC AND unix (unix serves the same gRPC wire over a UDS)
//	RegisterHTTP -> REST (chi)
//	RegisterMCP  -> MCP (the real mcp-go server)
//	RegisterQUIC -> QUIC (the native frame transport)
//
// Transport coverage caveat: embedded is the in-process transport, and remains
// each capability owner's own SDK surface (e.g. go-xkms's sdk/go/embedded),
// independent of this seam.
//
// Permissions are NOT declared here: an operation's required permission lives on
// its gRPC method (the proto option, enforced by the shared interceptor) and is
// referenced by the chi route, the MCP tool, and — via RegisterQUIC — the QUIC
// frame dispatch table. Embed BaseModule and override only the hooks a module
// actually uses.
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

	// RegisterQUIC attaches the module's gRPC service(s) to go-qrdb's native QUIC
	// transport. go-qrdb's QUIC transport is a bespoke frame protocol
	// (method-keyed dispatch), not HTTP-over-chi and not a real *grpc.Server, so
	// it cannot share RegisterGRPC's registration call the way unix does. Instead
	// the QUIC server hands RegisterQUIC a grpc.ServiceRegistrar that PROJECTS
	// each unary method of the registered grpc.ServiceDesc onto the frame
	// protocol: the frame's method key is the RPC's full gRPC method name
	// ("/package.Service/Method"), the frame payload is decoded/encoded with the
	// same protobuf messages the generated Handler expects, and the method's
	// permission is read from the identical (qrdb.v1.permission) proto option
	// RegisterGRPC's methods use — no second permission declaration. Because the
	// signature is identical to RegisterGRPC, most modules implement this by
	// calling the same pb.Register<X>Server(reg, impl) a second time (or simply
	// delegating: `func (m *M) RegisterQUIC(r grpc.ServiceRegistrar) { m.RegisterGRPC(r) }`).
	// A method with no declared permission fails the QUIC server's default-deny
	// startup assertion, exactly like an undeclared REST route. Streaming methods
	// (grpc.ServiceDesc.Streams) are NOT projected — QUIC's frame protocol has no
	// generic streaming path — so a module whose surface is entirely streaming
	// leaves this a no-op via BaseModule.
	RegisterQUIC(reg grpc.ServiceRegistrar)

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

// ModuleAuthFallback is implemented by modules whose HTTP surface accepts
// credentials the host's own authenticators do not understand — a
// capability's session-token plane (e.g. the JWT access/refresh pairs
// go-xkms's FIDO2/PIV login ceremonies mint, or go-quicbaas's platform
// tokens). When the unified server's REST middleware authenticates a
// request targeting one of the module's routes and the host's
// authenticator chain produces no identity, it invokes the OWNING module's
// fallback. A returned identity is finalized and authorized by the host's
// single enrichment + authorizer path, exactly like an mTLS identity:
// roles come from the store, never from the token, and the ONE Authorizer
// renders the decision. Implementations must fail closed.
//
// The method set intentionally matches middleware.RESTAuthFallback so a
// module value satisfies the middleware seam structurally.
type ModuleAuthFallback interface {
	AuthenticateHTTP(r *http.Request) (*auth.Identity, error)
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
//
// The native QUIC transport (RegisterQUIC) has no unauthenticated-method path
// at all -- every frame requires a verified mTLS client certificate -- so a
// method declared here that carries no (qrdb.v1.permission) option is simply
// left unreachable over QUIC (logged at registration) rather than failing the
// QUIC server's default-deny startup assertion. The declaration only ever
// widens gRPC/unix reachability; it never narrows QUIC's.
type GRPCPublicMethodLister interface {
	PublicGRPCMethods() []string
}

// BaseModule provides no-op defaults for every hook except Name, so a module
// embeds it and overrides only what applies. It does not implement Name, forcing
// each module to supply its own identity.
// TenantTargetAuthorizer is the minimal capability a module needs to
// authorize its OWN handler against a request-supplied target tenant, under
// the host's RBAC ScopeCross/ScopeSelf rules -- the identical check
// AuthMiddleware.AuthorizeTenantTarget performs for go-qrdb's own tenant
// routes (pkg/auth/middleware). It is declared here as a minimal structural
// interface rather than naming *middleware.AuthMiddleware directly, so this
// package stays free of that import; *middleware.AuthMiddleware already
// satisfies it, as-is, with no adapter required.
type TenantTargetAuthorizer interface {
	// AuthorizeTenantTarget authorizes ctx's identity for perm against
	// tenantID -- a tenant the request named, which may differ from the
	// identity's own tenant. It returns nil when the identity's role
	// grants perm with a scope (ScopeSelf, ScopeCross) that covers
	// tenantID, and a denial otherwise.
	AuthorizeTenantTarget(ctx context.Context, tenantID string, perm permission.Permission) error
}

// ModuleTenantTargetAware is an optional ServiceModule extension. The
// unified server calls SetTenantTargetAuthorizer exactly once, before any of
// the module's Register* hooks run, so a handler the module registers on ANY
// transport (REST, gRPC, unix, QUIC, MCP) can authorize its own operation
// against a caller-supplied target tenant -- e.g. a system-scope caller
// acting on a tenant it named in the request -- under the SAME
// ScopeCross/ScopeSelf RBAC rules go-qrdb's own tenant routes enforce,
// instead of being confined to the request's own identity tenant the way
// the shared per-route permission check alone would leave it (go-qrdb#587).
//
// The authorizer handed to SetTenantTargetAuthorizer is nil when the host's
// auth system is disabled (cfg.Auth.IsEnabled() == false); a module must
// treat a nil authorizer exactly as every built-in tenant route treats that
// condition -- admit unconditionally, because no identity exists on the
// request to confine -- never as a refusal.
//
// Embedded hosts that hold the node directly, rather than registering a
// ServiceModule, reach the same capability through
// sdk/go/node.Node.AuthMiddleware (whose *middleware.AuthMiddleware also
// satisfies TenantTargetAuthorizer) or Node.Authorizer.
type ModuleTenantTargetAware interface {
	SetTenantTargetAuthorizer(a TenantTargetAuthorizer)
}

type BaseModule struct{}

// RegisterGRPC is a no-op default.
func (BaseModule) RegisterGRPC(grpc.ServiceRegistrar) {}

// RegisterHTTP is a no-op default.
func (BaseModule) RegisterHTTP(*HTTPRegistrar) {}

// RegisterMCP is a no-op default.
func (BaseModule) RegisterMCP(*mcpserver.MCPServer) {}

// RegisterQUIC is a no-op default.
func (BaseModule) RegisterQUIC(grpc.ServiceRegistrar) {}

// Start is a no-op default.
func (BaseModule) Start(context.Context) error { return nil }

// Stop is a no-op default.
func (BaseModule) Stop(context.Context) error { return nil }
