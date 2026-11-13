# Unified API Server — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-24
> Conflicts with this document are defects. Fix them here first.

## Exported surface

`sdk/go/node`:

```go
func NewStandaloneNode(ctx context.Context, cfg *qrdbconfig.Config,
	serverOpts []server.ServerOption, nodeOpts ...NodeOption) (*Node, error)

func NewClusteredNode(ctx context.Context, cfg *qrdbconfig.Config,
	members map[uint64]string, serverOpts []server.ServerOption,
	nodeOpts ...NodeOption) (*Node, error)

func (n *Node) Server()      *server.Server
func (n *Node) Client()      *sdk.Client
func (n *Node) Services()    *lifecycle.Node
func (n *Node) KVService()    service.KVServicer
func (n *Node) AdminService() service.AdminServicer
func (n *Node) Close()       error   // tears down in reverse order

// The embedder's own service principal (see "Service principal" below).
func WithServicePrincipal(name string) NodeOption
func (n *Node) ServiceContext(ctx context.Context) (context.Context, error)
var ErrNoServicePrincipal error
```

`sdk/go/types_auth.go` re-exports the principal's wire-side names:

```go
const ServicePrincipalSubjectPrefix = auth.ServicePrincipalSubjectPrefix // "qrdb-service:"
var   IsServicePrincipal            = auth.IsServicePrincipal
type  ServicePrincipalPresentedError = authmw.ServicePrincipalPresentedError
```

### Service principal

An embedding host's own in-process work (loading its CA state, its stores, its
signing keys) has no end user behind it. With authentication on, an embedded
call carrying no identity is refused, so the node sanctions one principal for
that work:

```go
sdk.RegisterPermissionRoleGrant("xkms", sdk.PermissionCredentialsStore, sdk.PermissionCredentialsRetrieve)
n, err := node.NewStandaloneNode(ctx, cfg, serverOpts, node.WithServicePrincipal("xkms"))
svcCtx, err := n.ServiceContext(ctx)
secret, err := n.Client().Credentials().CredentialRetrieveBuffer(svcCtx, "ca-state")
```

- **Its authority is declared, not implied.** `ServiceContext` builds the
  identity `qrdb-service:<name>` in the system tenant. That identity has no
  roles, and its `ScopePermissions` are exactly the permissions registered
  under `<name>` through the role-grant seam, read when `ServiceContext` is
  called. The node's authorizer decides every call as it decides any
  caller's: the scope rules, the SO deny-list and ABAC all apply. A
  permission granted to no one is refused with `*sdk.PermissionDeniedError`.
- **The name is validated at construction.** It must match `[a-z0-9][a-z0-9._-]*`,
  be at most 64 bytes long and not be a built-in role name (`so`, `admin`,
  `operator`, `user`, `auditor`). Anything else is a `*node.ConfigError`.
- **It never comes from the network on its own say-so.** See
  [architecture.md](architecture.md#trust-boundaries). The one network form is a
  delegation certificate a **tenant's** own CA issued naming the principal,
  which the mTLS authenticator verifies in full and which acts inside that
  tenant only, holding the delegated scope intersected with the permissions
  registered under `<name>` — see
  [../identity-rbac/architecture.md](../identity-rbac/architecture.md#tenant-issued-delegations).
  A node built without `WithServicePrincipal` accepts no delegation.
- **It is audited by name.** Each authorization decision is recorded with
  actor `qrdb-service:<name>`, action `auth.authorize` and the permission as
  the resource.

`sdk/go/server/server.go` (package `server`, imported as
`github.com/jeremyhahn/go-qrdb/sdk/go/server`) — the Tier 2 alias file. It
lives outside the `sdk/go` root so that importing the root's config, entity,
seal and error types links no server (#627):

```go
type ServerOption = qrdbserver.ServerOption

var WithStrategyRegistry = qrdbserver.WithStrategyRegistry
var WithPINBackend       = qrdbserver.WithPINBackend
var WithFactorRegistry   = qrdbserver.WithFactorRegistry
var WithCeremonyCA       = qrdbserver.WithCeremonyCA
var WithServerTLSKey     = qrdbserver.WithServerTLSKey
var WithModule           = qrdbserver.WithModule
var WithServerTLS        = qrdbserver.WithTLS
var WithRESTListener     = qrdbserver.WithRESTListener
var WithRESTPacketConn   = qrdbserver.WithRESTPacketConn
var WithGRPCListener     = qrdbserver.WithGRPCListener
var WithQUICPacketConn   = qrdbserver.WithQUICPacketConn
var WithMCPListener      = qrdbserver.WithMCPListener
var WithGRPCWeb          = qrdbserver.WithGRPCWeb

type ServiceModule                = apimodule.ServiceModule
type BaseModule                   = apimodule.BaseModule
type ModuleHTTPRegistrar          = apimodule.HTTPRegistrar
type ModuleMCPToolPermissioner    = apimodule.MCPToolPermissioner
type ModuleGRPCPublicMethodLister = apimodule.GRPCPublicMethodLister
type ModuleMCPPublicToolLister    = apimodule.MCPPublicToolLister
type ModuleAuthFallback           = apimodule.ModuleAuthFallback
type ModuleConnObserver           = apimodule.ConnObserver
type ModuleTransport              = apimodule.Transport   // + ModuleTransportGRPC … ModuleTransportQUIC
type TenantTargetAuthorizer       = apimodule.TenantTargetAuthorizer
type ModuleTenantTargetAware      = apimodule.ModuleTenantTargetAware

var NewModuleHTTPRegistrar = apimodule.NewHTTPRegistrar
```

`sdk/go/types_mcp_wire.go` — the MCP tool-error contract (`pkg/api/mcp/wire`):

```go
type MCPCode          = wire.Code          // + MCPCodeOK … MCPCodeInternal, all seventeen
type MCPErrorPayload  = wire.ErrorPayload
type MCPErrorEnvelope = wire.ErrorEnvelope

var NewMCPErrorEnvelope   = wire.NewErrorEnvelope
var DecodeMCPErrorPayload = wire.DecodeErrorPayload
var MCPCodes              = wire.Codes
```

A module does not use these to report a failure: its MCP tool returns an error
and the server's outermost `errorCodeMiddleware` renders the envelope, exactly
as for a built-in tool (`TestModuleTool_ErrorsCarryTheClassifiedEnvelope`).

`sdk/go/server/mcp.go` — the combined MCP server, the one `startMCP` in
`pkg/server` builds for a regular listener: `mcp.NewServer` over the KV and
admin services, with every tool an option adds and, under
`WithSealedWindowProbe` and `WithSealedWindowDispatch`, the sealed-window
tools behind the same `/mcp` endpoint. An embedder builds, starts and stops
it from `sdk/go` alone:

```go
type MCPServer                = mcpapi.Server   // Start, Addr, Stop
type MCPServerOption          = mcpapi.ServerOption
type MCPTLSNotConfiguredError = mcpapi.TLSNotConfiguredError

var NewMCPServer                      = mcpapi.NewServer
var WithMCPServerTLS                  = mcpapi.WithTLS
var WithMCPServerListener             = mcpapi.WithListener
var WithMCPServerMaxConnections       = mcpapi.WithMaxConnections
var WithMCPServerMaxBodyBytes         = mcpapi.WithMaxBodyBytes
var WithMCPServerMaxConnsPerPeer      = mcpapi.WithMaxConnsPerPeer
var WithMCPServerAuthMiddleware       = mcpapi.WithAuthMiddleware
var WithMCPServerSealedWindowProbe    = mcpapi.WithSealedWindowProbe
var WithMCPServerSealedWindowDispatch = mcpapi.WithSealedWindowDispatch
```

The per-service tool options (`WithMVCC`, `WithBarrierService` and the rest)
are not re-exported yet: a node started through `sdk/go/node` serves this
server with every service it composes wired in.
`TestNewMCPServer_ServesToolsOverListener` drives `tools/list`, `qrdb_put`
and `qrdb_get` over a caller-bound HTTPS listener.

## `ServiceModule`

```go
type ServiceModule interface {
	Name() string
	RegisterGRPC(reg grpc.ServiceRegistrar)
	RegisterHTTP(r *HTTPRegistrar)
	RegisterMCP(m *mcpserver.MCPServer)
	RegisterQUIC(reg grpc.ServiceRegistrar)
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

type BaseModule struct{}   // no-op defaults for every hook EXCEPT Name
```

`HTTPRegistrar`:

```go
func (h *HTTPRegistrar) Handle(method, pattern string, perm permission.Permission, handler http.HandlerFunc)
func (h *HTTPRegistrar) HandlePublic(method, pattern string, handler http.HandlerFunc)
func (h *HTTPRegistrar) HandleOptional(method, pattern string, handler http.HandlerFunc)
```

`HandleOptional` registers the route as `RESTRouteOptional` in the REST route
registry, and attaches the module's `ModuleAuthFallback` when it has one, as
`Handle` does. The REST auth middleware admits an optional-route request
straight to the handler, with no identity, only when it presents no credential
at all: no peer certificate, and no `Authorization`, `DPoP`,
`X-Qrdb-On-Behalf-Assertion` or `X-Qrdb-On-Behalf-Sig` header. Any credential sends the
request down the authenticated path unchanged, so the chain binds a
certificate to its user account and a refusal is byte-identical to a `Handle`
route's (`TestRESTMiddleware_OptionalRoute_InvalidCredentialRejectedAsOnHandle`).
A credential that authenticates is treated as on an authenticated-only route:
no permission is checked, and a holder identity is still refused. HTTP/3
serves the same router and middleware. `sdkserver.NewModuleHTTPRegistrar` takes the
optional registration function as its fourth argument.

`RegisterQUIC` takes the identical `grpc.ServiceRegistrar` type `RegisterGRPC`
does; a module usually implements it by calling the same generated
`pb.Register<X>Server(reg, impl)`, or simply `func (m *M) RegisterQUIC(r
grpc.ServiceRegistrar) { m.RegisterGRPC(r) }`. The QUIC server supplies a
registrar that PROJECTS the resulting `grpc.ServiceDesc` onto the native
frame protocol: each unary method's frame key is its full gRPC method name
(`/package.Service/Method`, identical to `grpc.UnaryServerInfo.FullMethod`),
its permission comes from the same `(qrdb.v1.permission)` proto descriptor
option `LookupGRPCFromDescriptor` resolves for gRPC and unix, and its payload
is decoded/encoded with the request/response messages the generated
`MethodDesc.Handler` already expects. Streaming methods
(`grpc.ServiceDesc.Streams`) are not projected — logged, not fatal.

Optional extensions:

```go
type MCPToolPermissioner interface {
	MCPToolPermissions() map[string]permission.Permission
}

type GRPCPublicMethodLister interface {
	PublicGRPCMethods() []string   // "/package.Service/Method"
}

type MCPPublicToolLister interface {
	PublicMCPTools() []string   // see "MCP public tools"
}

type ModuleAuthFallback interface {
	AuthenticateHTTP(r *http.Request) (*auth.Identity, error)
}

type ConnObserver interface {
	ConnOpened(t Transport)   // Transport: grpc, unix, rest, http3, mcp, quic
	ConnClosed(t Transport)
}

type TenantTargetAuthorizer interface {
	AuthorizeTenantTarget(ctx context.Context, tenantID string, perm permission.Permission) error
}

type ModuleTenantTargetAware interface {
	SetTenantTargetAuthorizer(a TenantTargetAuthorizer)
}
```

`*middleware.AuthMiddleware` satisfies `TenantTargetAuthorizer` as-is — the
interface is declared minimally in `pkg/apimodule` precisely so that package
need not import `pkg/auth/middleware`. `wireModuleTenantTargetAuthorizers`
(`pkg/server`) calls `SetTenantTargetAuthorizer` on every module implementing
the extension, before any transport's `Register*` hook runs, passing the
server's own `*authmw.AuthMiddleware`, or a literal (true) `nil` when auth is
disabled — never a `nil` pointer boxed into the interface, which would leave
a module's own `if authz == nil` check unable to see it.

`ConnObserver` is wired from each transport's existing `WithServiceModules`
option: at `Start`, every listener collects the modules that implement it
(`apimodule.ConnObserversOf`) and reports through them. The zero set costs
nothing — the listener is not wrapped at all.

| Listener | Where a connection is reported |
|---|---|
| gRPC, unix | `ConnObservers.Credentials` wraps the TLS transport credentials: `ServerHandshake` reports the open, returns a conn that reports its close, and reports the close at once on a failed handshake. The listener is NOT wrapped — grpc-go sets `TCP_USER_TIMEOUT` only on the `*net.TCPConn` it accepts, and a wrapped listener would drop it from 20000ms to 0 (`TestConnObserver_KeepsGRPCTCPUserTimeout` reads the kernel's value) |
| REST (TCP), MCP | the raw TCP listener is wrapped **beneath** `tls.NewListener`, so `http.Server` still sees a `*tls.Conn` and negotiates HTTP/2 |
| REST (HTTP/3) | `http3.Server.ConnContext` reports the open; `context.AfterFunc` on the connection's context reports the close |
| QUIC | `handleConnection` spans exactly the connection's life; it reports the open on entry and the close on return, before `Stop`'s wait completes |

Each connection is reported opened exactly once and closed exactly once (the
close function is a `sync.OnceFunc`, so every close path may call it). A
connection that fails its TLS handshake was accepted, so it is counted and then
closed. The callbacks run synchronously on the accept and close paths and must
not block. `TestModuleConnObserver_SeesEveryListener` (pkg/server) proves the
counts balance on all six listeners of a real server while each carries a real
request.

## Invariants

- **`RegisterGRPC` is invoked once per gRPC-wire transport** — gRPC *and* unix.
  A module registering side effects there will see them twice.
- **`RegisterQUIC` is invoked once, at QUIC server construction**, with a
  registrar that projects unary methods onto the frame dispatch table — it is
  NOT the same `*grpc.Server` `RegisterGRPC` receives, so a module that keys
  state off the registrar identity will see two different objects.
- **`Start` runs after all transports register and before any accepts
  traffic**; `Stop` runs LIFO with the server's own stop.
- **`BaseModule` does not implement `Name`**, forcing every module to supply its
  own identity. A module cannot be anonymous by omission.
- **Permissions are not declared on `ServiceModule`.** An operation's permission
  lives on its gRPC method as the `qrdb.v1.permission` proto option and is
  *referenced* by the chi route, the MCP tool, and the QUIC frame projection.
  One source, four readers. A module defined outside go-qrdb declares it the
  same way: its `.proto` imports `"qrdb/v1/permission.proto"` from the sdk/go
  module's `proto/` directory, and its generated Go code imports
  `github.com/jeremyhahn/go-qrdb/sdk/go/proto/qrdb/v1`. That file is the one
  copy; qrdb's own `qrdb.proto` imports it by the same path. See
  [api.md](api.md#authorization).
- **A `RegisterQUIC` method whose descriptor carries no `(qrdb.v1.permission)`
  option fails QUIC server construction** — the same default-deny startup
  assertion an undeclared REST route or QUIC built-in method fails, just
  reached through the proto descriptor lookup instead of a missing literal.
- **An MCP tool registered via `RegisterMCP` without a matching
  `MCPToolPermissions()` entry stays fail-closed** on an authenticated server.
  Use `permission.IdentityGated` for tools whose authorization the module's own
  handler performs.
- **`PublicGRPCMethods()` bypasses both authentication and authorization** for
  the named methods, symmetric with `HandlePublic`. **A malformed method name is
  rejected at registration**, so a typo fails startup loudly rather than
  silently leaving the method auth-gated. **The native QUIC projection reads
  the same list**: a declared-public method registered through `RegisterQUIC`
  is served to a caller with no client certificate (the listener verifies a
  certificate if given), so one declaration is the public set on gRPC, unix
  and QUIC. As on gRPC, the declaration wins over a `(qrdb.v1.permission)`
  option the method also carries.
- **`ModuleAuthFallback` may establish identity, never authority.** A returned
  identity is finalized and authorized by the host's single enrichment and
  authorizer path: **roles come from the store, never from the token**, and the
  ONE authorizer renders the decision. Implementations must fail closed. The
  method set intentionally matches `middleware.RESTAuthFallback` so a module
  value satisfies the seam structurally.
- **Modules register first, and every REST route has one owner.**
  `Server.claimRoute` records the claimant of each route
  (`authmw.RESTRouteKey`) — `built-in` or `module <name>` — and a second claim
  panics `*RouteCollisionError` at construction. `RESTRouteRegistry` keys its
  entries the same way and keeps its own `*RESTRouteCollisionError` as a
  backstop.
- **`RESTRouteKey(method, pattern)` erases path-parameter names and keeps
  regexp constraints**, so the registry agrees with chi about which patterns
  are one route. `TestChiTreatsRenamedParamsAsOneRoute` measures chi's behaviour
  rather than assuming it.
- **Direct registrations are recorded and checked.** `HTTPRegistrar` embeds a
  recording wrapper over the live router: `Get`/`Post`/…/`Method`/`Handle` and
  routes on `With`/`Group` inline routers are forwarded and recorded
  (`DirectRoutes`). After all registration, `assertNoDirectRouteCollisions`
  panics `*DirectRouteCollisionError` for a recorded route that a
  permission-recorded registration also covers.
- **`Mount`, `Route`, `NotFound`, `MethodNotAllowed` and `Use` are refused** on
  the registrar's router (and on every inline router it hands out) with
  `*apimodule.UnsupportedRouterMethodError{Method}`. Each serves requests no
  route registration describes: a mount overwrites every method's handler at
  its pattern and hides that from `Routes()`; a sub-router's or the router's
  own `NotFound`/`MethodNotAllowed` answers every unmatched path; `Use` runs
  ahead of routing for every route on the server, built-ins included. A module
  serves a subtree by registering it: `Handle(method, "/prefix/*", perm, h)`.
- **Module subtrees refuse unmatched paths.** Every module route's static prefix
  before its first parameter or wildcard (`/v1/devices/` for
  `/v1/devices/{id}`) is recorded (`RESTRouteRegistry.RegisterModulePrefix`),
  and `RESTMiddleware` answers an unmatched request under one with 404 instead
  of passing it to the router's fallback — defence in depth under the
  refusals above. Fully static routes, and routes whose first segment is
  already dynamic, claim no subtree.
- **A module's first static path segment must not collide with the web UI's
  paths.** The embedded web UI is the router's fallback: it answers every path
  no route matches. A module prefix that overlaps a path the web UI serves
  (its asset directories, its client-side routes) turns those paths into
  module-owned 404s. Keep module routes under a prefix of their own — `/v1/<module>/…` —
  and never under a web-UI top-level path.
- **`HTTPRegistrar.Freeze` closes registration** when `RegisterHTTP` returns;
  a later `Handle`, `HandlePublic` or direct registration through the registrar
  or any router it handed out panics `*apimodule.RegistrationClosedError`.
- **The permission registry is populated unconditionally**, even for routes that
  are not enabled — so SO dual-factor gating recognizes SO-permissioned paths
  even on servers that do not serve them.
- **Default-deny startup assertion.** Every route, RPC and tool must carry a
  declaration — a permission, an explicit public marker, or an authenticated
  marker.

## Public and authenticated-only routes

The no-login client verbs (Health and invitation redemption) are one table,
`pkg/api/publicverbs.Table`, with each verb's name on every transport and its
pre-authentication rate-limit dimension. REST, gRPC, Unix, QUIC and MCP read
their public set from it, so a verb's login requirement is the same on every
transport (rulings 158 and 264(b)).

| Route | Marker |
|---|---|
| `GET /health`, gRPC / Unix `/qrdb.v1.QRDBService/Health`, QUIC `admin.Health`, MCP `qrdb_health` | **public** on every transport (registered when admin is on); each draws the `admin:health` rate-limit bucket by caller address before serving |
| `POST /v1/invitations/redeem`, gRPC `RedeemInvitation`, QUIC `invitation.Redeem`, MCP `qrdb_invitation_redeem` | **public** (registered when invitations are on) |
| `GET /v1/config/initialized` | **public** |
| `GET /v1/ca/bootstrap` | **public**, only when exactly one bundle source is configured (cluster CA or host bundle); unconfigured it is not even claimed, so a host never collides with it |
| `GET /v1/cluster/nodekey`, `POST /v1/cluster/keydelivery` | **public**, only when a cluster key receiver is wired |
| `GET /metrics` | **authenticated**, no specific permission; served by default, absent only under `server.WithGatherer(nil)` |
| gRPC / Unix `/grpc.health.v1.Health/*` | **public**: liveness only |
| gRPC / Unix server reflection (`/grpc.reflection.*`) | **not public**. qrdb registers none. One a composer registers is authenticated and default-denied like any other unmapped method, unless the composer declares it public through `GRPCPublicMethodLister` |
| MCP `GET /.well-known/oauth-protected-resource` | **public**, only when `mcp.WithProtectedResourceMetadata` is configured |
| MCP `/mcp` public tools: the built-in `qrdb_health` (from `publicverbs.Table`, when admin is on) and any tool a module declares through `MCPPublicToolLister` | **public**, only those tools; while any exist, an anonymous `initialize`, `notifications/initialized` and `tools/list` are answered too (the listing shows the public set). See [MCP public tools](#mcp-public-tools) |
| MCP `/mcp`, every other JSON-RPC method (`ping` included) and every other tool | **authenticated at the HTTP layer** before the MCP layer reads the request; each tool call then carries a permission |

Everything else carries a permission.

**The SDK connects without a credential.** Every Go SDK network transport's
`Connect` confirms the connection with a Health call. Health is public, so a
client with no certificate and no token connects, and the server's refusal
comes at its first call that needs an identity, as the typed
`Unauthenticated` error (ruling 264(b)). A certificate the node cannot verify
still fails the TLS handshake, so `Connect` fails. The MCP SDK reads the auth
chain's `401`/`403` on `initialize` and on every `tools/call` through one
refusal capture (`sdk/go/transport/mcp/refusal_capture.go`), so a refused tool
call surfaces as the same typed error as on every other transport.

**Public means RBAC-exempt, not ungated.** The two cluster key routes run their
own authorization sequence in `pkg/clusterkeys` — CA chain, node SPIFFE SAN,
certificate revocation, then the placement table — because being off the
middleware's path means they inherit none of its checks, revocation included.

## MCP public tools

An embedder that needs a pre-authentication MCP surface — a login ceremony,
liveness and readiness probes — declares it from a service module:

```go
func (m *Module) PublicMCPTools() []string {
    return []string{"example_login_begin", "example_login_finish",
        "example_liveness", "example_readiness"}
}
```

`apimodule.MCPPublicToolLister` (re-exported from the SDK as
`ModuleMCPPublicToolLister`) is the declaration seam, the MCP twin of
`GRPCPublicMethodLister`. A name may be one of the module's own tools or a
built-in no-login tool. The built-in `qrdb_health` needs no declaration: it is
a `publicverbs.Table` verb, public on every transport, so the MCP server
registers it public itself whenever the admin service is on.

**Registration.** After every built-in and module tool permission is
registered, the MCP server records each declared name with
`MCPToolRegistry.RegisterPublic`:

| Declared name | Outcome |
|---|---|
| a module tool with no permission | public |
| a tool carrying `identity:public` (`qrdb_health`) | public — already public on every transport through `publicverbs.Table` |
| a tool carrying any other permission | refused: `*middleware.PublicMCPToolRefusedError` |
| a name no registered tool answers | refused: `*mcp.UnknownPublicToolError` |

A refusal is returned by `Server.Start`, so the server never serves a surface
it refused. Every permission other than `identity:public` names a verb that
reaches data or changes the node — KV, SQL, documents, vectors, admin, IAM,
seal — so the eligible set is closed rather than a deny list a new family
could slip past. A module's own public tool declares no permission, which
makes its handler responsible for reaching no data.

**Admission.** The HTTP layer still authenticates every request. When that
fails and the server has public tools, a single JSON-RPC message sent by
`POST` (read under the anonymous body bound) is admitted anonymously by
method:

| Method | Admitted when |
|---|---|
| `initialize`, `notifications/initialized`, `tools/list` | the failure is `401`-class; a `503` or `429` is returned unchanged, so a caller with a credential the node could not finish checking is told to retry |
| `tools/call` | the named tool is public, whatever the failure |
| anything else, a batch, a `GET` | never — `401` |

An admitted request carries no identity; the tool gate passes a public tool
and refuses every other with `401`.

**Listing.** With authentication configured, `tools/list` is filtered per
caller through `AuthMiddleware.MCPToolVisible`: a public tool is always
listed, an anonymous caller sees nothing else, and an authenticated caller
sees each tool its grants allow — the authorizer's decision without the audit
record, rate limit or peer attestation of a call. While the node awaits its
admin credential, the tools that gate admits are listed as well.

## Transports

| Transport | Wire |
|---|---|
| embedded | in-process Go calls |
| unix | **gRPC over a UDS** — same service definitions as gRPC |
| REST | HTTP/JSON, chi router |
| gRPC | HTTP/2 |
| QUIC | method/frame dispatch — module unary RPCs are reached via `RegisterQUIC` projection |
| MCP | Model Context Protocol tools |

### gRPC-Web

`pkg/api/grpcweb`:

```go
const PathPrefix = "/grpc-web/"

type Config struct {
	Enabled          bool
	AllowedOrigins   []string
	AllowCredentials bool
}
func (c Config) Validate() error   // *OriginError, *WildcardCredentialsError

func NewHandler(cfg Config, backend http.Handler) (*Handler, error)
```

The `server.grpc_web` block is `config.GRPCWebConfig`, the same three fields
with their YAML keys; `pkg/config` imports no API package, so `pkg/server`
converts the block to `grpcweb.Config`. `qrdbd start` copies it into the server
configuration (`applyConfigToFlags`, `buildServerConfig`); it has no flag.
`server.WithGRPCWeb(cfg config.GRPCWebConfig)` (SDK `sdkserver.WithGRPCWeb`,
`sdkserver.GRPCWebConfig`) replaces the block for an embedded node. `pkg/server` hands
`rest.WithGRPCWeb(cfg, backend)` a backend that is the gRPC listener's own
`*grpc.Server`, published by `startGRPC` through an atomic pointer: REST starts
first, so a call before that, or after the gRPC server stops, is answered
`Unavailable`.

The handler is framing only (`docs/reference/grpc-web-protocol.md`, over
`docs/reference/grpc-http2-protocol.md`): it relabels the media type, sets the
request to HTTP/2 for `(*grpc.Server).ServeHTTP`, decodes and encodes the
base64 text form (a request body may be a run of separately padded chunks; a
response is encoded a whole message per chunk), moves the gRPC trailers into the body's final trailer frame
(flag `0x80`, lowercase `name: value` lines ending CRLF, sorted), and answers
CORS. It is not a vanguard or connect translation; see
[architecture.md](architecture.md#alternatives-rejected) for why.

Invariants:

- **One interceptor chain.** A gRPC-Web call reaches the same unary and stream
  interceptors as a call on the gRPC listener, with `peer.AuthInfo` built from
  the request's TLS state. `TestGRPCWeb_InterceptorParity` (`pkg/api/grpc`)
  and `TestGRPCWeb_SameAnswerAsNativeGRPC` (`test/integration`) hold every
  caller to the same status code on both.
- **The prefix has one owner.** `grpcWebDispatch` runs before every REST layer;
  a REST route whose first segment matches or could match `grpc-web` panics
  `*rest.GRPCWebRouteCollisionError` at construction.
- **Off by default, and refused when it cannot be served.** `config.Validate`
  refuses `enabled` without `protocols.grpc` (`*config.ValidationError`, field
  `server.grpc_web.enabled`). `Server.Start` checks the CORS fields with
  `grpcweb.Config.Validate`, whether or not `Enabled` is set, and reports a
  fault as a `*config.ValidationError` naming the field; it refuses an enabled
  configuration with gRPC or REST off (`*server.GRPCWebUnavailableError`).

### WebTransport

`pkg/api/rest` serves WebTransport sessions (draft-ietf-webtrans-http3-16,
`docs/reference/draft-ietf-webtrans-http3-16.txt`) on the HTTP/3 listener
through `internal/webtransport`, a patched copy of
`github.com/quic-go/webtransport-go` v0.13.0 that is a package of the go-qrdb
module (its README.md lists the patches):

```go
type WebTransportSession = webtransport.Session
type WebTransportSessionErrorCode = webtransport.SessionErrorCode
type WebTransportStreamErrorCode = webtransport.StreamErrorCode
type WebTransportSessionError = webtransport.SessionError
type WebTransportStreamError = webtransport.StreamError
type WebTransportHandler func(sess *WebTransportSession, caller *auth.Identity, tenantID string)

func WithWebTransportHandler(path string, h WebTransportHandler) ServerOption
```

`server.WithWebTransportHandler(path, h)` passes it through `pkg/server`, and
the SDK re-exports the option and the types (`sdkserver.WithWebTransportHandler`,
`sdkserver.WebTransportSession`, `sdkserver.WebTransportHandler`,
`sdkserver.InvalidWebTransportHandlerError`, and the error codes and errors
`sdkserver.WebTransportSessionErrorCode`, `sdkserver.WebTransportStreamErrorCode`,
`sdkserver.WebTransportSessionError`, `sdkserver.WebTransportStreamError`), so
an embedder can close a session with a code held in a `uint32` variable and
match a peer's error with `errors.As`. There is one handler, for one path.

Invariants:

- **Authenticated before the upgrade.** The session's extended CONNECT is
  registered as an authenticated REST route on the same chi router, so it
  passes the same handler chain as every REST request: authentication, tenant
  binding, authorization and the sealed-window gate. A CONNECT the chain refuses gets
  the status REST would give: 401 with no caller, and so on. The handler is
  then not called and nothing is upgraded. A CONNECT on another path is not
  upgraded.
- **HTTP/3 only.** The route on the HTTP/1.1 or HTTP/2 listener answers 400
  (`*rest.WebTransportUpgradeError` over `*rest.WebTransportNotHTTP3Error`),
  as does a failed upgrade (`*rest.WebTransportUpgradeError`).
- **Nothing changes without a handler.** Only with a handler does the HTTP/3
  listener set `EnableDatagrams` and `EnableStreamResetPartialDelivery` in its
  QUIC configuration (-16 section 3), and only then does it send the
  WebTransport SETTINGS. Without one, the path is an unknown route (404). The
  SETTINGS the libraries send whether or not this applies are recorded as
  deviations in [docs/reference/README.md](../reference/README.md).
- **One accept loop.** webtransport-go has no `ServeListener`, so
  `pkg/api/rest` accepts from the connection-limited QUIC listener itself and
  hands each connection to `ServeQUICConn`. The per-peer cap holds for
  WebTransport too.
- **Bounded buffering before CONNECT.** A stream for a session that is not
  established yet is buffered until the session arrives or the 5 s handshake
  timeout passes. The timeout runs from the first stream for that session;
  later streams do not extend it. A connection buffers at most 16 streams per
  unknown session and 64 in total. Each stream past either cap is reset at
  once with WT_BUFFERED_STREAM_REJECTED (-16 section 4.6), and so is each
  buffered stream at the timeout. The caps live in `internal/webtransport`.
- **Bounded streams per established session.** The server advertises 100
  bidirectional and 100 unidirectional streams and a 16 MiB data window per
  session (SETTINGS_WT_INITIAL_MAX_STREAMS_BIDI/_UNI and
  SETTINGS_WT_INITIAL_MAX_DATA, -16 section 5). A peer that negotiates flow
  control and opens past the limit closes the session with
  WT_FLOW_CONTROL_ERROR. With or without flow control, at most 100 streams of
  each direction wait for the handler to accept them; each stream past that is
  reset with WT_BUFFERED_STREAM_REJECTED and the session stays up. QUIC
  stream credit is returned when a stream completes, not when the handler
  accepts it, since quic-go's credit-release mode would apply
  to every HTTP/3 request stream on the listener.
- **Stream operations never wait on a gone session.** After a peer resets a
  stream with WT_SESSION_GONE, a handler's Read or Write waits for the session
  to close; the handler's own `CancelRead` or `CancelWrite` also ends that
  wait. Opening a stream at the peer's limit while the session's capsule queue
  is full closes the session with H3_EXCESSIVE_LOAD instead of blocking.
  `internal/webtransport/README.md` lists the patches.
- **Same-origin by default.** `CheckOrigin` is left nil, so webtransport-go
  applies its default: a request carrying an `Origin` header must match the
  request's host. The REST authentication chain is the access control.
- **Invalid options fail Start.** A nil handler, a path not starting with
  `/`, or a path the authentication middleware exempts as public for every
  method (`middleware.IsPublicPath`: `/health`, `/v1/ca/bootstrap`, and the
  rest of its allowlist), makes `Start` return
  `*rest.InvalidWebTransportHandlerError`. A CONNECT on a public path would
  reach the handler with no caller, so every session would be refused.
- **Closed with the server.** `Stop` closes every WebTransport connection
  first, so each open session and its streams end and the peer sees the
  close before the HTTP/3 server goes down.

**One path per request on every HTTP surface.** The REST handler chain opens
with `canonicalPathMiddleware`, which clears `r.URL.RawPath` so chi routes on
the same decoded `r.URL.Path` that authorization, the sealed-window allowlist,
the SO gate and the handlers read; the HTTP/3 server serves the same handler.
The MCP HTTP listener routes only the exact paths `/mcp` and the
protected-resource metadata document and authorizes by tool name from the
request body, never by path, so it has no route/authorization split to close.

Additionally, **per-database protocol listeners** run beside these: MySQL and
PostgreSQL ([../sql/](../sql/)) and etcd v3
([../etcd-compatibility/](../etcd-compatibility/)). Those are separate servers
over the same services, not a seventh transport.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Request whose raw path differs from its decoded path (`%2F` and other escapes) | canonicalised to the decoded path by the outermost layer (`canonicalPathMiddleware`) before the sealed-window, auth, SO and routing layers, so all of them resolve one route | **yes** |
| Route, RPC or tool with no permission declaration | **startup assertion fails** | **yes** |
| Two registrations of the same route (module/module or module/built-in, parameter names ignored) | `*RouteCollisionError` panics at construction, naming both | **yes** |
| A route registered directly on the router that a permission-recorded registration also claims | `*DirectRouteCollisionError` panics at construction | **yes** |
| A module registering after its `RegisterHTTP` returned | `*apimodule.RegistrationClosedError` panic | **yes** |
| A module calling `Mount`, `Route`, `NotFound`, `MethodNotAllowed` or `Use` on the registrar's router | `*apimodule.UnsupportedRouterMethodError` panics at construction | **yes** |
| An unmatched request under a module's route prefix | 404 from the REST middleware; never reaches the router's fallback | **yes** |
| MCP tool with no `MCPToolPermissions()` entry | fail-closed at call time | **yes** |
| Client offering 0-RTT early data on a resumed session, on the native QUIC listener or REST's HTTP/3 listener | refused. Neither listener allows 0-RTT, because early data is replayable, so the client completes a full handshake before any request is accepted. HTTP/3 sets this through an explicit `QUICConfig`, because quic-go's `http3.Server` allows 0-RTT by default | **yes** |
| MCP request no authenticator accepts, with authentication configured | 401 at the HTTP layer before the MCP layer reads the body, with `WWW-Authenticate` when `mcp.WithBearerChallenge` is set | **yes** |
| MCP request whose credential the node could not finish checking -- a tenant user lookup that overran its budget, an unreachable revocation source, a rate limit | the status REST answers (`authmw.HTTPStatus`): 503 or 429, with `Retry-After` when the error names a delay, and no `WWW-Authenticate`. It is never 401: an MCP client treats 401 at `initialize` as terminal, and the credential was never judged | **yes** |
| MCP request body over `defaultMaxRequestBodyBytes` (4 MiB, the same pre-auth cap every transport applies, configurable with `mcp.WithMaxBodyBytes`) | the read fails with `*http.MaxBytesError` and the request is refused; a caller with a larger payload uses the chunked import session API | **yes** |
| MCP request body over 1 MiB (`maxAnonymousRequestBodyBytes`) on a listener without authentication -- the boot-time sealed-window listener, or a node with authentication off | refused the same way. Its callers are anonymous, and everything it serves them is a small JSON-RPC document | **yes** |
| MCP connection beyond `DefaultMaxConnections` (configurable with `mcp.WithMaxConnections`) | the accept is not handed to the HTTP server until an existing connection closes (`netutil.LimitListener`); it never reads a byte in the meantime | **yes** |
| MCP connection beyond `transport.max_conns_per_peer` from a single remote IP (configurable with `mcp.WithMaxConnsPerPeer`) | the accept is refused at the listener via `pkg/api/connlimit` before anything is read | **yes** |
| gRPC or Unix socket message over `DefaultMaxMessageBytes` (4 MiB, configurable with `WithMaxMessageBytes`/`transport.max_message_bytes`) | `codes.ResourceExhausted`, enforced by grpc-go before any interceptor (so before auth) runs; a caller with a larger payload uses the chunked import session API | **yes** — every RPC on both servers, with no larger-message opt-in |
| gRPC or Unix socket connection opening more than `DefaultMaxConcurrentStreams` simultaneous RPCs (configurable with `WithMaxConcurrentStreams`) | grpc-go refuses the extra stream at the HTTP/2 layer | **yes** |
| gRPC or Unix socket client ping faster than the keepalive `EnforcementPolicy`'s `MinTime` (5s) with no active stream | connection closed with `ENHANCE_YOUR_CALM` | **yes** |
| Malformed public gRPC method name | rejected at registration; startup fails | **yes** |
| `server.grpc_web` with an origin that is not `*` or a serialized origin, or `*` with `allow_credentials` | `Start` returns a `*config.ValidationError` (field `server.grpc_web.allowed_origins` / `.allow_credentials`) wrapping `*grpcweb.OriginError` / `*grpcweb.WildcardCredentialsError`; the node does not start | **yes** |
| `server.grpc_web.enabled` with gRPC (or REST) off | `*config.ValidationError` from `Validate`; `*server.GRPCWebUnavailableError` from `Start` | **yes** |
| A REST route under `/grpc-web/` | `*rest.GRPCWebRouteCollisionError` panics at construction | **yes** |
| gRPC-Web request that is not `POST`, or not a gRPC-Web media type | 405 with `Allow: POST`, or 415; never reaches the gRPC server | **yes** |
| gRPC-Web CORS preflight from an origin not listed, or for a method other than `POST` | 403, no CORS headers | **yes** |
| Module gRPC method declared public with no permission option, projected onto QUIC | skipped (logged); unreachable over QUIC only | n/a |
| `ModuleAuthFallback` returning no identity | request unauthenticated | **yes** |
| A presented identity claiming a service principal (subject `qrdb-service:*` or the principal marker), from any authenticator, an on-behalf assertion or a module fallback, on any network transport — other than a tenant-issued delegation the mTLS authenticator verified in full | `*ServicePrincipalPresentedError`, which unwraps to `auth.ErrUnauthenticated`: REST 401, gRPC/Unix `Unauthenticated`, and the same class on QUIC, MCP, SQL and etcd. No identity is bound | **yes** |
| `ServiceContext` on a node built without `WithServicePrincipal` | `node.ErrNoServicePrincipal` | **yes** |
| `WithServicePrincipal` name that is empty, too long, outside `[a-z0-9][a-z0-9._-]*` or a built-in role name | `*node.ConfigError` from the constructor | **yes** |
| A service-principal call for a permission not granted to its name | `*rbac.PermissionDeniedError` (`*sdk.PermissionDeniedError`), audited as denied under the principal | **yes** |
| Raft-only capability in standalone mode | `requires mode: raft` — REST 501, gRPC/QUIC `Unimplemented` | **yes** |

### Error classification

What kind of failure a typed error is, is decided once, by `pkg/api/errclass`:

- `errclass.Classify(err) Class` walks an ordered, first-match-wins table.
  Rows are one line each — `as(&service.KeyNotFoundError{}, NotFound)` for a type,
  `is(tenant.ErrTenantNotFound, NotFound)` for a sentinel — grouped into one
  block per family, so adding a condition is a one-line change to one block.
- The table is ordered in three bands. **Dominant** rows win however deeply
  their condition is wrapped: a transport type's own declared class
  (`errclass.Classed`), the authentication classifier, a closed store, a sealed
  tenant, a cross-tenant refusal, a failed index rollback, a database create
  that ran out of its own deadline. **Family** blocks hold the leaf
  conditions. After them come the context conditions -- the caller's deadline
  or cancellation, and go-quicraft's own `ErrCanceled` -- and then every other refusal go-quicraft reports as
  temporary (`quicraft.Temporary`: busy, dropped, shard not ready, no leader
  yet) as `Unavailable`, so a consensus step that could not finish answers as
  retryable rather than as a server fault. go-quicraft's `ErrTimeout` is a
  family row, also `Unavailable`, because it most often means an election
  rather than the caller's deadline. **Generic** wrappers
  (`ProposeError`, `ReadError`, the store errors) come last, so they only
  classify what nothing more specific recognised -- a permanent consensus
  failure such as a missing shard among them.
- A transport's own refusal types implement `errclass.Classed` beside the
  transport's projection table, because `pkg/api/errclass` cannot import the
  transports.
- Each transport owns one map from class to its code — `httpStatusByClass`
  (REST), `grpcCodeByClass` (gRPC, the unix socket, and through it MCP),
  `wireStatusByClass` (QUIC) — and each is asserted TOTAL over `errclass.All()`.
- QUIC (`quic.ServiceErrorStatus`) and MCP (`mcp.ServiceErrorCode`) consult
  the table FIRST, and a projected module's own gRPC status only for an error
  the table leaves unclassified — the same order on both — so a module can
  never shadow qrdb's own classification.

Tests that guard it: `TestRules_AreNotShadowed` (every row's sample, wrapped,
must be answered by that row and not an earlier one),
`TestRules_EverySampleIsClaimedByItsOwnRow`, `TestRules_NamesAreUnique`, a
discovery floor on the row count, and `TestEveryTransportAgreesOnEveryCondition`,
which drives every row through the real REST, gRPC, QUIC and MCP mappings.

## Configuration

Transport enablement is per protocol in the daemon config; see
[../configuration/](../configuration/). Which transports are composed at all
follows the run mode ([../run-modes/](../run-modes/)).

gRPC-Web is `server.grpc_web` (`enabled`, `allowed_origins`,
`allow_credentials`), off by default, with no `qrdbd` flag; `server.WithGRPCWeb`
replaces it for an embedded node. See [gRPC-Web](#grpc-web) above.

## Test strategy

- **Unit**: the default-deny startup assertion (a route without a declaration
  must fail), route-collision detection on the real server (module over a
  built-in, module over module, a renamed path parameter, a direct router
  registration over a permission-recorded one, late registration), chi's own
  renamed-parameter behaviour, malformed public gRPC method rejection, and the
  fail-closed MCP tool path.
- **The `ModuleAuthFallback` boundary is a security test**: an identity returned
  by a module must still be enriched from the store and authorized by the host
  authorizer, and a token carrying role claims must not affect the decision.
- **The service-principal boundary is a security test.** A claim is refused
  through `Authenticate`, `AuthenticateQUIC`, the REST middleware, the unary
  interceptor and both on-behalf paths
  (`pkg/auth/middleware/service_principal_test.go`). An SDK-only test shows
  four things with authentication on: an identity-less call is refused, the
  principal is denied before the embedder grants it anything, it is admitted
  within its grant and denied outside it, and every decision is audited under
  its subject. That test imports `sdk/go` and `sdk/go/node` and nothing under
  `pkg/` (`sdk/go/node/service_principal_sdk_test.go`).
- **Transport parity** is the standing integration property: the same capability
  reaches the same decision on all six.
- **gRPC-Web parity.** `TestGRPCWeb_InterceptorParity` (`pkg/api/grpc`) calls
  one server natively and as gRPC-Web for each caller shape and requires the
  same code and the same interceptor log. `TestGRPCWeb_SameAnswerAsNativeGRPC`
  (`test/integration`, in `make integration-test-api-server`) does the same
  against a live `qrdbd` with `server.grpc_web.enabled`: the admin certificate,
  a tenant writer, no credentials, a bearer the node never issued, another
  tenant's certificate and an unprivileged bound user. `pkg/api/grpcweb` unit
  tests cover the framing, the text encoding and CORS.
- **gRPC-Web conformance.** `pkg/api/grpcweb/conformance_test.go` holds the
  handler to every server-side requirement of the pinned protocol,
  [grpc-web-protocol.md](../reference/grpc-web-protocol.md), against a real
  `grpc.Server`. Each test is named after its section:

  | Section of `grpc-web-protocol.md` | Requirement | Test (`TestConformance_…`) |
  |---|---|---|
  | Content-Type 1 | `application/grpc-web`, `+proto`; a missing format is `+proto` | `ContentType_GRPCWebDefaultsToProto`, `ContentType_SubtypeReachesServer` |
  | Content-Type 2 | `application/grpc-web-text`, `-text+proto`, answered in kind | `ContentType_GRPCWebText` |
  | Content-Type | any other media type is refused (415), the gRPC server never called | `ContentType_UnsupportedIsRefused` |
  | HTTP wire protocols 1, 3 | any HTTP version; the end of the body closes the stream | `WireProtocol_AnyHTTPVersion` |
  | HTTP wire protocols 2 | request header names in any case over HTTP/1.1; trailer names lower-case | `WireProtocol_MixedCaseHeaderNames`, `WireProtocol_TrailerNamesLowerCase` |
  | Message framing 1, 2 | the status in the body, as a `0x80` uncompressed trailer frame holding an HTTP/1 header block without the terminating empty line; no HTTP trailers, HTTP 200 | `MessageFraming_StatusInBody` |
  | Message framing 2, 3 | data frames `0x00`; one trailer frame, last | `MessageFraming_TrailersLast` |
  | Message framing 4 | trailers-only: the trailers in the headers, no body | `MessageFraming_TrailersOnly` |
  | Message framing (errors) | a truncated frame, a request frame flagged as a trailer, or a corrupt or truncated base64 body ends in a trailer frame with a non-OK status under HTTP 200: `13`, and `14` for corrupt base64, grpc-go's code for a body read error | `MessageFraming_MalformedRequest` |
  | User Agent | `X-User-Agent` reaches the gRPC server as metadata | `UserAgent_XUserAgentReachesServer` |
  | Text-encoded streams 1 | `Accept: application/grpc-web-text` selects a text response | `TextStreams_AcceptSelectsText` |
  | Text-encoded streams 2 | base64, no `Content-Transfer-Encoding`; each message encoded atomically, so every padded chunk ends on a message boundary | `TextStreams_MessagesEncodedAtomically` |
  | Text-encoded streams 2 | a body of separately padded chunks reads as one stream | `TextStreams_ChunkedRequestBody` |

  Design goals, HTTP/2 related behavior (no stream-id, no go-away), Retries,
  Keep-alive, Bidi-streaming, Versioning and Browser-specific features state
  nothing a server does, and have no test.
- **The vanguard blocker probe.** `test/vanguardprobe`, a module of its own so
  that `connectrpc.com/vanguard` never enters the database's module graph, holds
  the reason `pkg/api/grpcweb` is not vanguard
  ([architecture.md](architecture.md#alternatives-rejected)) to the pinned
  release, over loopback TLS with an in-memory client certificate and a
  `grpc.Server` control for each claim. `TestVanguardSkipsInterceptor`: a
  service registered through `vanguardgrpc` receives a nil interceptor, so a
  unary interceptor runs zero times. `TestVanguardDropsPeerTLS`: the handler's
  `peer.FromContext` carries no `credentials.TLSInfo` with the client
  certificate. Run `make check-vanguard-probe` at each vanguard release; a
  failure, `vanguard now keeps the interceptor` or `vanguard now carries peer
  TLS`, means the blocker is gone and the replacement is due. It is not part of
  `make test` or CI's unit stage.

## Observability

- `GET /metrics` in Prometheus format, served by default: the process registry
  and the shared go-quicraft collector together (`metricsGatherer`). Metric
  names, including the sync-latency histograms
  `quicraft_logdb_fsync_latency_seconds` and
  `qrdb_storage_sync_duration_seconds{backend}`, are in [user.md](user.md#metrics).
- A sync slower than the process's slow-sync threshold logs one `WARN` per
  sync: `slow storage sync` with `backend`, `database`, `dir`, `took`,
  `threshold`, or `slow raft WAL fsync` with `wal_dir`, `took`, `threshold`.
  The threshold is `observability.slow_sync_threshold`
  (`config.ObservabilityConfig`): default `config.DefaultSlowSyncThreshold`
  (1 s), accepted in (0, `config.MaxSlowSyncThreshold` = 1 m], anything else a
  `*config.ValidationError` with `Field` `observability.slow_sync_threshold`,
  from `Config.Validate` and from `ApplySlowSyncThreshold`. `qrdbd start`
  (`configureObservability`) and `node.NewStandaloneNode` /
  `node.NewClusteredNode` call `ApplySlowSyncThreshold`, which sets
  `syncwatch.SetSlowThreshold` before any storage opens; every observer reads
  `syncwatch.SlowThreshold()` per sync unless `syncwatch.WithThreshold` pins
  it. It is an observation threshold for the stall signal only, NOT a
  durability knob: quicraft keeps per-entry fsync with no tunable.
- `GET /health` public, `GET /status` and `GET /capabilities` authenticated —
  see [../clustering/](../clustering/api.md) and [../run-modes/](../run-modes/).

## Integration Target

Run `make integration-test-api-server` for this feature. The per-transport accounting row is `test/integration/COVERAGE.md` under `api-server`.
