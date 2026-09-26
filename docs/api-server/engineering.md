# Unified API Server — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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
```

`sdk/go/types_server.go` — the Tier 2 alias file:

```go
type ServerOption = qrdbserver.ServerOption

var WithStrategyRegistry = qrdbserver.WithStrategyRegistry
var WithPINBackend       = qrdbserver.WithPINBackend
var WithFactorRegistry   = qrdbserver.WithFactorRegistry
var WithCeremonyCA       = qrdbserver.WithCeremonyCA
var WithModule           = qrdbserver.WithModule
var WithServerTLS        = qrdbserver.WithTLS
var WithRESTListener     = qrdbserver.WithRESTListener
var WithRESTPacketConn   = qrdbserver.WithRESTPacketConn
var WithGRPCListener     = qrdbserver.WithGRPCListener

type ServiceModule                = apimodule.ServiceModule
type BaseModule                   = apimodule.BaseModule
type ModuleHTTPRegistrar          = apimodule.HTTPRegistrar
type ModuleMCPToolPermissioner    = apimodule.MCPToolPermissioner
type ModuleGRPCPublicMethodLister = apimodule.GRPCPublicMethodLister
type ModuleAuthFallback           = apimodule.ModuleAuthFallback

var NewModuleHTTPRegistrar = apimodule.NewHTTPRegistrar
```

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
```

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

type ModuleAuthFallback interface {
	AuthenticateHTTP(r *http.Request) (*auth.Identity, error)
}
```

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
  One source, four readers.
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
  silently leaving the method auth-gated. **QUIC has no unauthenticated-method
  path at all** (every frame requires a verified mTLS client certificate), so
  a declared-public method with no `(qrdb.v1.permission)` option is skipped —
  logged, not fatal — by the QUIC projection rather than tripping its
  default-deny assertion; it stays reachable on gRPC/unix as declared.
- **`ModuleAuthFallback` may establish identity, never authority.** A returned
  identity is finalized and authorized by the host's single enrichment and
  authorizer path: **roles come from the store, never from the token**, and the
  ONE authorizer renders the decision. Implementations must fail closed. The
  method set intentionally matches `middleware.RESTAuthFallback` so a module
  value satisfies the seam structurally.
- **Modules register first and built-ins yield.** A built-in route whose
  method+pattern a module has already claimed logs a debug line and yields.
  **Module-vs-module duplicates fail construction loudly.**
- **The permission registry is populated unconditionally**, even for routes that
  are not enabled — so SO dual-factor gating recognizes SO-permissioned paths
  even on servers that do not serve them.
- **Default-deny startup assertion.** Every route, RPC and tool must carry a
  declaration — a permission, an explicit public marker, or an authenticated
  marker.

## Public and authenticated-only routes

| Route | Marker |
|---|---|
| `GET /health` | **public** (registered when admin is on) |
| `GET /v1/config/initialized` | **public** |
| `GET /v1/ca/bootstrap` | **public**, only when a bootstrap certificate is configured |
| `GET /v1/cluster/nodekey`, `POST /v1/cluster/keydelivery` | **public**, only when a cluster key receiver is wired |
| `GET /metrics` | **authenticated**, no specific permission, only when a gatherer is configured |

Everything else carries a permission.

**Public means RBAC-exempt, not ungated.** The two cluster key routes run their
own authorization sequence in `pkg/clusterkeys` — CA chain, node SPIFFE SAN,
certificate revocation, then the placement table — because being off the
middleware's path means they inherit none of its checks, revocation included.

## Transports

| Transport | Wire |
|---|---|
| embedded | in-process Go calls |
| unix | **gRPC over a UDS** — same service definitions as gRPC |
| REST | HTTP/JSON, chi router |
| gRPC | HTTP/2 |
| QUIC | method/frame dispatch — module unary RPCs are reached via `RegisterQUIC` projection |
| MCP | Model Context Protocol tools |

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
| Two modules claiming the same method+pattern | construction fails loudly | **yes** |
| A module claiming a built-in's method+pattern | the built-in yields, debug-logged | n/a |
| MCP tool with no `MCPToolPermissions()` entry | fail-closed at call time | **yes** |
| Malformed public gRPC method name | rejected at registration; startup fails | **yes** |
| Module gRPC method declared public with no permission option, projected onto QUIC | skipped (logged); unreachable over QUIC only | n/a |
| `ModuleAuthFallback` returning no identity | request unauthenticated | **yes** |
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
  tenant, a cross-tenant refusal, a failed index rollback. **Family** blocks
  hold the leaf conditions. **Generic** wrappers (`ProposeError`,
  `ReadError`, the store errors) come last, so they only classify what nothing
  more specific recognised.
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

## Test strategy

- **Unit**: the default-deny startup assertion (a route without a declaration
  must fail), module-vs-module duplicate detection, built-in yielding to a
  module, malformed public gRPC method rejection, and the fail-closed MCP tool
  path.
- **The `ModuleAuthFallback` boundary is a security test**: an identity returned
  by a module must still be enriched from the store and authorized by the host
  authorizer, and a token carrying role claims must not affect the decision.
- **Transport parity** is the standing integration property: the same capability
  reaches the same decision on all six.

## Observability

- `GET /metrics` in Prometheus format, gated on a configured gatherer.
- `GET /health` public, `GET /status` and `GET /capabilities` authenticated —
  see [../clustering/](../clustering/api.md) and [../run-modes/](../run-modes/).
- Route-yield events are logged at debug level, which is how you confirm a
  module actually took over a built-in path.
