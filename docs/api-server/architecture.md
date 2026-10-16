# Unified API Server — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-09

One server hosting six transports, with one authorization decision behind all of
them, and a seam for higher layers to add capabilities without forking it.

## Problem

Six transports — embedded, Unix socket, REST, gRPC, QUIC, MCP — each with its
own handler set is six chances to authorize differently, six places to forget a
permission check, and six surfaces that drift. And a layer above (go-xkms,
go-quicbaas, go-trusted-platform) that needs to add its own capability has only
two bad options: fork the server, or run a second one beside it.

## Shape chosen

**One server, one authorization registry, one module seam.**

```
                     ┌──────────── pkg/server ────────────┐
  embedded ─────────►│                                    │
  unix (gRPC/UDS) ──►│   authorization registry           │──► pkg/service
  REST ─────────────►│   (permission per route/RPC/tool)  │    (per tenant+db)
  gRPC ─────────────►│                                    │──► engine / raft
  QUIC ─────────────►│   ServiceModule registrations      │
  MCP ──────────────►│                                    │
                     └────────────────────────────────────┘
```

Reached through `node.NewStandaloneNode` / `node.NewClusteredNode` with server
options. **This is hosting infrastructure**: consumed by embedding, and
composer-only from above.

### One-source authorization

A capability's required permission is declared **once, at its registration
site**, and every transport reads the same declaration:

- **gRPC and Unix** — the `qrdb.v1.permission` proto option on the RPC,
  enforced by a shared interceptor.
- **REST** — the route declares its permission at registration through the
  permission-recording registrar (`Handle`) or is explicitly public
  (`HandlePublic`).
- **MCP** — the tool declares its permission through `MCPToolPermissions()`.

Those declarations feed one registry and a **default-deny startup assertion**: a
route or tool with no declaration does not quietly become open, it fails the
assertion.

### The `ServiceModule` seam

A capability module registers itself into the shared server:

```go
type ServiceModule interface {
	Name() string
	RegisterGRPC(reg grpc.ServiceRegistrar)   // once per gRPC-wire transport (gRPC and unix)
	RegisterHTTP(r *HTTPRegistrar)
	RegisterMCP(m *mcpserver.MCPServer)
	RegisterQUIC(reg grpc.ServiceRegistrar)   // projected onto the native QUIC frame protocol
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}
```

**Permissions are deliberately not declared in the interface**: an operation's
permission lives on its gRPC method (the proto option) and is *referenced* by
the chi route, the MCP tool, and the QUIC frame projection. One source, four
readers.

`RegisterQUIC` shares `RegisterGRPC`'s exact signature on purpose: go-qrdb's
native QUIC transport is a bespoke, method-keyed frame protocol, not a real
`*grpc.Server`, so it cannot simply be handed to `RegisterGRPC` a second time
the way the unix transport reuses the gRPC wire. Instead the QUIC server
passes `RegisterQUIC` a `grpc.ServiceRegistrar` that PROJECTS the registered
`grpc.ServiceDesc` onto the frame protocol: each unary method's frame key
becomes its full gRPC method name (`/package.Service/Method`), its permission
is resolved from the same `(qrdb.v1.permission)` proto option `RegisterGRPC`'s
methods carry, and its payload is decoded/encoded with the same protobuf
messages the generated `Handler` already expects. A module typically
implements it by delegating straight to `RegisterGRPC`. Streaming methods are
not projected — QUIC's frame protocol has no generic streaming path — so a
module whose surface is entirely streaming leaves this hook a no-op.

**The MCP transport carries a module's tools too**, through `RegisterMCP`, and
a module typically implements them over the code that serves its gRPC surface —
so it answers in gRPC's vocabulary there as well. That is why the MCP error
vocabulary is also one code per gRPC status code. The MCP server consults the
shared classification table first and honours a module's gRPC status only for
an error the table leaves unclassified — the same order the QUIC server uses —
so the two projections of a module's answer cannot disagree, and a module can
never shadow qrdb's own classification of one of its errors.

**This projection is what decided the QUIC status vocabulary.** A projected
method's handler is the module's own generated gRPC code, so it answers with a
gRPC status by construction — it has never seen the frame protocol and cannot
be asked to. The frame protocol therefore declares one status per gRPC status
code, and the QUIC server translates a module's answer through a single table
asserted to be a bijection. Anything narrower would be lossy for every
projected module permanently, and could not be fixed by adding a clause to a
handler: there is no qrdb handler in that path to add it to.

That constraint is the reason the question "should the QUIC wire mirror gRPC's
codes?" has a measured answer rather than a taste-based one. See
[api.md](api.md#the-quic-status-vocabulary) for the vocabulary and
[the alternatives below](#alternatives-rejected) for what was rejected.

`BaseModule` supplies no-op defaults for every hook **except `Name`**, so a
module embeds it and overrides only what applies — and cannot accidentally be
anonymous.

Optional extensions:

| Interface | Purpose |
|---|---|
| `MCPToolPermissioner` | declare each MCP tool's permission; **a tool registered without one stays fail-closed** |
| `GRPCPublicMethodLister` | declare gRPC methods reachable without auth, symmetric with `HandlePublic` |
| `MCPPublicToolLister` | declare MCP tools reachable without auth (first-contact only); **a tool that reaches data is refused at startup** |
| `ModuleAuthFallback` | authenticate credentials the host does not understand — a capability's own session-token plane |
| `ConnObserver` | observe each connection opened and closed on every listener (REST, HTTP/3, gRPC, unix, QUIC, MCP), so a host keeps its own active-connection metrics for connections qrdb accepts on its behalf |
| `ModuleTenantTargetAware` | receive the host's tenant-target authorizer, once, before any Register* hook runs, so a module's own handler can authorize an operation against a caller-named target tenant under the host's RBAC ScopeCross/ScopeSelf rules, on whichever transport it registered on |

`ConnObserver` is a per-module option on the one listener set, not a second
listener. The alternative — a host counting connections on a listener of its
own — is exactly the parallel transport stack the seam exists to remove. It is
reported per listener (`Transport`), and REST's two sockets are reported apart
(`rest` for TCP, `http3` for QUIC) because they are different connections.

### `ModuleTenantTargetAware` — authorizing against a named tenant

A module's permission check, by default, is scoped to the REQUEST's own
identity tenant: the shared per-route permission declaration admits or
refuses the caller for its own tenant, and nothing more. A module whose
operation legitimately acts on a tenant the caller NAMES — a system-scope
caller managing a tenant it does not belong to, the same shape qrdb's own
tenant routes support via `AuthMiddleware.AuthorizeTenantTarget` — has no way
to ask that question with the per-route check alone (#587).

A module that needs this implements `SetTenantTargetAuthorizer`
(`apimodule.TenantTargetAuthorizer`, a minimal interface
`*middleware.AuthMiddleware` already satisfies). The server calls it exactly
once, before any of the module's `Register*` hooks run — so the authorizer is
in place before the module's handlers are reachable on any transport — and
again with `nil` when the host's auth system is disabled, which a module
must treat exactly as every built-in tenant route does: admit
unconditionally, never refuse. An embedded/Unix host that holds the `Node`
directly, rather than registering a `ServiceModule`, reaches the identical
capability through `sdk/go/node.Node.AuthMiddleware` / `Node.Authorizer`
instead.

### `ModuleAuthFallback` — the important one

A module whose HTTP surface accepts credentials the host's authenticators do not
understand (the JWT access/refresh pairs go-xkms's FIDO2/PIV login ceremonies
mint, or go-quicbaas's platform tokens) implements this. When the REST
middleware authenticates a request targeting that module's routes and the host's
authenticator chain produces no identity, it invokes **the owning module's**
fallback.

**A returned identity is then finalized and authorized by the host's single
enrichment and authorizer path, exactly like an mTLS identity: roles come from
the store, never from the token, and the ONE authorizer renders the decision.**
Implementations must fail closed.

That is the whole design in one paragraph: a module may say *who* you are; it
never says *what you may do*.

**A fallback is scoped to the path, not to one verb.** For a request whose
method matches no registered route, the host resolves the fallback from the
path's owning module rather than from the (method, path) route, so a caller the
module can authenticate is told its *verb* is wrong (405) instead of being told
at 401 that its credentials are. That widening is confined to the case that
provably cannot reach a handler or the authorizer; a method that has its own
registered route still uses that route's fallback, or none. A path whose verbs
are split between two modules with different fallbacks resolves to none, since
neither owns a verb neither registered. See
[api.md](api.md#403-and-405-are-different-claims).

### Alternatives rejected

- **Per-transport authorization.** Rejected — six answers to one question.
- **Modules declaring their own permissions in the interface.** Rejected: the
  permission belongs on the RPC, and the route and tool reference it.
- **Allowing an undeclared route or tool.** Rejected: default-deny, with a
  startup assertion so the failure is at boot rather than in production.
- **Letting a module's token carry roles.** Rejected explicitly — roles come
  from the store. A module that could mint roles would be an authorization
  bypass wearing a fallback's clothes.
- **A second server beside the first.** Rejected — that is what the module seam
  exists to avoid.
- **A `RegisterQUIC(reg *QUICRegistrar)` with its own `Handle`-style method
  registration, mirroring `HTTPRegistrar`.** Rejected: it would make a module
  declare its QUIC methods (and, to stay fail-closed, their permissions) a
  second time by hand, duplicating what `RegisterGRPC`'s proto-declared
  permission already states once. Projecting the registered
  `grpc.ServiceDesc` instead keeps the one-declaration property the other
  three hooks already have.
- **A second, optional `QUICRegistrar` interface, type-asserted for.**
  Rejected — an operation a module needs (QUIC reachability) belongs on the
  interface every module implements, not behind a capability check a module
  can silently fail to satisfy.
- **A small, purpose-built QUIC status set — one status per condition that has
  actually been asked for.** Rejected. It is how the six-status vocabulary was
  arrived at, and four separate pieces of work then escalated to it in turn; a
  set assembled an issue at a time ends up arbitrary, and every future
  condition is a protocol change rather than a lookup. Measured against the
  code rather than against taste: `pkg/api/grpc` classifies roughly eighty
  conditions across fifteen of gRPC's seventeen codes, so "QUIC is deliberately
  narrower" described the frame layout and was never true of the conditions the
  handlers behind it reach.
- **Adopting gRPC's status codes by NUMBER as well as by meaning.** Rejected:
  it would renumber `StatusError`, `StatusUnimplemented`,
  `StatusUnauthenticated`, `StatusPermissionDenied` and `StatusUnavailable`,
  which breaks every deployed peer simultaneously and buys nothing a lookup
  table does not already give. Pre-1.0 permission to renumber is not a reason
  to. The correspondence is therefore by meaning, through one table, and no
  arithmetic relates the two numberings.
- **Declaring only the fifteen codes qrdb's own handlers emit.** Rejected, but
  narrowly: the two left out (`DEADLINE_EXCEEDED`, `UNKNOWN`) are emitted by
  nothing in qrdb today, so on a "what does the codebase demonstrably need"
  test they do not qualify. They are declared anyway because the module
  projection above makes the table's TOTALITY the property that matters — a
  missing row degrades a module's answer silently, and silence is what this
  whole change exists to remove. The cost is two constants that qrdb's own
  handlers never produce, and it is named in the wire package rather than
  glossed.
- **A bespoke error vocabulary for MCP.** Rejected. MCP had no codes at all, so
  nothing constrained the choice and the temptation was to design a set around
  what MCP's tools happen to return. It would have handed a consumer a second
  translation table days after the QUIC vocabulary gave them their first, and
  made the transport-by-transport conformance matrix a translation exercise
  rather than a comparison. MCP declares one code per gRPC status code, by
  meaning, for the same reasons QUIC does — with JSON-RPC numbers, because the
  envelope is JSON-RPC's and importing a foreign registry's integers into it
  would be the worst of both.
- **Emitting MCP's codes as JSON-RPC `error` objects.** Rejected on the
  specification: a failure inside a tool is a RESULT carrying `isError`, and
  only a dispatch failure is a protocol error. It is also not available — the
  mcp-go server pins every handler-returned error to `INTERNAL_ERROR` with a
  nil `data` field and gives a handler no way to influence either, so the
  spec-correct channel is the only channel.
- **A third ~80-clause error classifier, in `pkg/api/mcp`.** Rejected. Two such
  ladders already existed and had to agree with nothing forcing them to; a
  third would have compounded that. The MCP server renders the gRPC code
  instead, so it owns no condition clauses and has nothing to drift.
- **One classifier per transport, kept in agreement by tests.** Rejected, and
  replaced by a transport-neutral table. REST ran twelve per-family status
  ladders, gRPC nine, QUIC one of its own, and they had drifted: the same
  error meant different things on different transports, and QUIC had no
  clause at all for most families. `pkg/api/errclass` now owns what kind of
  failure every typed error is, as an ordered table of one-line rows; each
  transport owns only a total map from that class to its own code. This is the
  shape the authentication classifier (`middleware.ClassifyAuthError`) had
  already proved, and that classifier stays the owner of the auth failures:
  the table delegates to it, because its classes also drive control flow a wire
  class cannot express.
- **Classes that are exactly gRPC's seventeen codes.** Rejected: HTTP draws
  distinctions a client acts on that gRPC does not (413 against 429; 410, 412
  and 422 against 409), and collapsing them would have changed REST answers
  that were chosen deliberately. A class is a meaning; each transport's
  projection is a function of the class alone, which is what makes "one error,
  one meaning" a property of the table.
- **Stamping a code on every MCP tool error, including the argument refusals.**
  Rejected for now. Those refusals are built from string literals at some four
  hundred call sites, and the only way to code them centrally is to infer a
  classification from prose — the exact thing being deleted. They stay uncoded,
  a client degrades them as it always did, and a test pins the boundary so it
  is a stated gap. Coding them means giving those call sites typed errors
  first, which is mechanical, wide, and its own change.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Unified server, transport hosting, authorization registry | `pkg/server` | go-qrdb |
| `ServiceModule` and its extensions | `pkg/apimodule` | go-qrdb |
| Per-transport handlers | `pkg/api/{rest,grpc,quic,mcp,unix,etcd}` | go-qrdb |
| Per-(tenant, database) service resolution | `pkg/service` (`service.Registry`) | go-qrdb |
| Node composition entry points | `sdk/go/node` | go-qrdb |
| SDK Tier 2 aliases | `sdk/go/types_server.go` | go-qrdb |
| Capability modules | — | **go-xkms, go-quicbaas, go-trusted-platform** |

### Where the transports bind

Every listener the server opens — REST, the HTTP/3 (QUIC) listener that shares
the REST port, gRPC, the native QUIC transport, MCP, MySQL, PostgreSQL — joins
`config.Server.Host` with its own port. One bind host, one containment
decision, no per-transport literal; see
[../configuration/architecture.md](../configuration/architecture.md#infrastructure-values-are-never-literals).

HTTP/3 is the case worth stating explicitly, because it is the one transport
that does not get its own address from configuration: it shares the REST
listener's port, and it takes the host that listener **actually bound**, read
back off the socket. Both sockets are opened before either serves, and if the
kernel puts them on different bind hosts — one wildcard, one specific — start
fails with `*rest.ListenerHostMismatchError` instead of serving the same
handler and the same TLS config on a wider surface than was asked for. The
QUIC socket's bind failure is likewise a `Start` error rather than a log line
on a goroutine, so a REST server never reports success with no HTTP/3 listener
behind it. Previously the host was a literal wildcard, and a REST server bound
to loopback still accepted QUIC datagrams on every interface — a containment
decision honoured on one transport and silently discarded on the other.

HTTP/3 starts whenever TLS is configured; making it opt-in or independently
addressable is an open architecture question, not a decided one.

When `qrdbd start` must auto-select a port for Raft, REST/admin, gRPC or native
QUIC, it uses `internal/netport` to probe the same bind host and every protocol
the eventual listener family will occupy. REST/admin is therefore checked on TCP
and on the HTTP/3 UDP sibling together; single-protocol listeners are checked in
their own namespace at the configured host rather than at loopback.

`internal/netport` is the one port-reservation package in the tree, shared by
`qrdbd` and the test harness. It has two pickers. `FreePortOn` draws a
kernel-assigned number and proves it free on the named protocols at the named
host, holding every probe until all of them succeed; `qrdbd`'s auto-ports use
it. `Reserve` is for processes that start many listeners in a row — tests and
`sdk/go/testutil/qrdbdharness` — and draws from a window below the kernel's
ephemeral range, certifies each port on TCP and UDP together, and keeps a
cross-process lock on it, so no other process in the network namespace is
handed the same number.

### Pre-bound sockets

An embedder that needs a kernel-assigned port — a test process booting many
nodes, typically — must not reserve one by listening on `:0`, closing, and
passing the number: another process can take the port in between, on TCP or
on the HTTP/3 UDP side. It binds the socket itself and hands the socket over:

| Option | Replaces the bind of |
|---|---|
| `server.WithRESTListener(net.Listener)` | `server.admin_port` (TCP) |
| `server.WithRESTPacketConn(net.PacketConn)` | the HTTP/3 socket beside it (UDP); requires `WithRESTListener` |
| `server.WithGRPCListener(net.Listener)` | `server.grpc_port` (TCP) |
| `server.WithQUICPacketConn(net.PacketConn)` | `server.quic_port`, the QUIC transport (UDP) |
| `server.WithMCPListener(net.Listener)` | `mcp.port` (TCP) |

A handed-over socket **outlives any one server**. An embedded node rebuilds its
server in process — a system encryption migration tears the data plane down and
builds a new server from the same options — so each server lifetime serves on a
wrapper whose close ends that lifetime's use of the socket without closing the
socket: it wakes the lifetime's blocked `Accept` or datagram read with a past
deadline, waits for every such call to leave the socket, then clears the
deadline. The wait matters on the HTTP/3 side: the QUIC stack stops reading a
socket it did not open by moving the read deadline into the past and never
moving it back, so without the clear the next lifetime's HTTP/3 server would
read nothing, and without the wait the ended lifetime's reader could still be
taking the next lifetime's datagrams. Nor does the ended QUIC stack stop at
close: a connection it closed keeps a timer that moves the read deadline into
the past again roughly three probe timeouts later, when the socket already
belongs to the next lifetime. So once a lifetime has ended, its wrapper's
deadline setters no longer reach the socket. The next lifetime serves on the
same port. The socket is really closed by whoever builds the servers once
it is done with them — the SDK node in `Node.Close`, anyone else through
`server.PreboundSockets(opts...)` — and by a `Start` that refuses to serve at
all. `node.NewStandaloneNode` and `node.NewClusteredNode` likewise close the
sockets when they return an error: the caller has handed them over, and no
node exists whose `Close` would. `Start` refuses a pre-bound socket for a protocol that is disabled, and a
listener that cannot take a deadline, with `*server.PreboundSocketError`.

`qrdbd` is such a builder. One `qrdbd start` can run three servers on the same
ports: the sealed-window listener, the node's own server once the barrier is
unsealed, and a rebuilt server after each in-process system encryption
migration. It binds the REST TCP listener, the HTTP/3 UDP socket beside it,
the gRPC listener, the QUIC transport's UDP socket and the MCP listener —
each one whose protocol is enabled — once, immediately before the first of those servers starts,
hands them to every one of them, and closes them when `start` returns. Were each
server to bind its own, the ports would belong to nobody between the
sealed-window listener's `Stop` and the node server's `Start` — as long as
opening the node and first boot take — and an outbound connection drawing one
as its ephemeral source port, or any other process, could take it: the node
would unseal and then fail to serve with `bind: address already in use`. A
failed bind is reported as `*ServerSocketBindError`, naming the protocol,
network and address; when the barrier is sealed it is the sealed-window listener
that cannot start, and the error is wrapped in `*SealedListenerStartupError`.
No port `qrdbd` serves on is released while the node is sealed or across the
unseal.

A pre-bound HTTP/3 socket is held to the same containment rule as a bound one,
and additionally to the TCP listener's port, which is the one Alt-Svc
advertises (`*rest.ListenerPortMismatchError`).

Where pre-binding is impossible, port 0 is refused instead: the multi-node Raft
transport is bound by go-quicraft itself and advertised to peers, so
`raft.raft_address` / `raft.listen_address` with port 0 on a multi-member node
is refused with `*cluster.RaftPortZeroError` — wrapped in a `*node.ConfigError`
by the SDK node, and returned directly by `qrdbd start` for `--raft-address` /
`--listen`, before any directory is created. Without a pre-bound listener, a **zero** admin or gRPC
port on the SDK node still selects the fixed default (63001 / 63002), not a
kernel-assigned port.

## Trust boundaries

- **A bind address holds on every transport or on none.** REST and HTTP/3 serve
  the same handler on the same port; binding is enforced to agree at start
  rather than reported in a log line an operator would have to notice.
- **Default deny.** A route, RPC or tool with no permission declaration fails the
  startup assertion rather than serving unauthenticated.
- **The route that is authorized is the route that is served.** Authorization,
  the sealed-window allowlist, the SO gate and every handler resolve the
  request's decoded path; chi on its own would route on the raw, still-encoded
  path whenever the request carried an escape such as `%2F`, so one request
  could be authorized as one route and served by another. The outermost layer
  of the REST handler — which the HTTP/3 listener shares — therefore
  canonicalises every request to its decoded path before anything else sees
  it. A name containing `/` is not addressable through a single-segment route
  (`/v1/databases/{name}` and the like); a wildcard route (`/kv/*`) receives
  the decoded key as it always did.
- **Public routes are explicit and few** — `GET /health`,
  `GET /v1/config/initialized`, `GET /v1/ca/bootstrap` when configured, the
  two cluster key routes when a receiver is wired (they authorize the peer
  node themselves), and the MCP protected-resource metadata document when
  configured. `GET /metrics` requires authentication but no specific
  permission. The full list is in
  [engineering.md](engineering.md#public-and-authenticated-only-routes).
- **MCP authenticates before the MCP layer reads anything.** With
  authentication configured, every request to the MCP endpoint is
  authenticated at the HTTP layer, so `initialize`, `ping` and `tools/list`
  are not answered anonymously, and every request body is bounded. The one
  exception is the embedder's declared first-contact surface: a module that
  implements `MCPPublicToolLister` names tools — health, liveness, readiness,
  login begin/finish — served without credentials, and while any exist an
  anonymous caller may also `initialize` and `tools/list`, which lists the
  public set only. `ping` and every other tool still answer `401`.
- **A public MCP tool cannot reach data.** A declared tool that carries any
  permission but `admin:health` — a KV, SQL, document, vector, admin, IAM or
  seal verb — is refused with `PublicMCPToolRefusedError`, and a declared name
  no tool answers with `UnknownPublicToolError`; either stops the MCP server
  from starting.
- **`tools/list` shows what the caller may call.** With authentication
  configured, the listing is filtered by the same decision that authorizes the
  call, without auditing it: an authenticated caller sees the public set plus
  every tool its grants allow. The filter is advisory — every call is still
  authorized.
- **A module cannot grant itself authority.** Its fallback returns an identity;
  the host enriches and authorizes it.
- **The embedder's service principal exists only in process.** A node built
  with `node.WithServicePrincipal(name)` hands the principal
  `qrdb-service:<name>` to its embedder through `Node.ServiceContext`, for
  calls through the node's own embedded client. The principal's authority is
  the set of permissions registered under its name, and the same authorizer
  decides every call it makes. The authentication middleware refuses any
  identity that claims the principal on every network transport. It checks
  that in the one function every transport's authentication reaches, so it
  makes no difference whether the claim came from a bearer token, a
  certificate, a cluster peer's on-behalf assertion or a module fallback. A
  call the embedded client forwards to another replica carries the
  `WithEmbeddedCaller` identity, never the principal. The one exception is a
  delegation certificate a tenant's own CA issued to the principal: it acts
  inside that tenant only, with the delegated scope intersected with the
  principal's grants (see
  [../identity-rbac/](../identity-rbac/architecture.md#tenant-issued-delegations)).
  See [engineering.md](engineering.md#service-principal).
- **A malformed public gRPC method name is rejected at registration**, so a typo
  fails startup loudly instead of silently leaving a method auth-gated.
- **A REST route has exactly one owner.** Modules register before the
  built-ins, and a second claim on a route — a module over a built-in, a module
  over another module — **fails construction** with `rest.RouteCollisionError`
  naming both parties. Nothing yields: a built-in used to give way silently to a
  module that claimed its path, which let any module a host mounted take over a
  core endpoint unnoticed. "The same route" is chi's notion, not the string's:
  `/a/{id}` and `/a/{deviceID}` are one route (chi serves whichever registered
  last), `/a/{id:[0-9]+}` and `/a/{id}` are two.
- **A module cannot route around its own permission declaration.** A route it
  registers straight on the embedded `chi.Router` (bypassing `Handle` /
  `HandlePublic`) is recorded; one that is the same route as a
  permission-recorded registration fails construction with
  `rest.DirectRouteCollisionError`, because chi would serve the direct handler
  under the other registration's permission — a case the default-deny assertion
  cannot see, since the pattern IS covered. One nothing covers stays the
  default-deny assertion's refusal. The router methods that serve requests no
  registration describes — `Mount`, `Route`, `NotFound`, `MethodNotAllowed`,
  `Use` — are refused outright; a module serves a subtree as
  `Handle(method, "/prefix/*", perm, h)`, and an unmatched request under a
  module's route prefix is refused rather than passed to the router's
  fallback.
  Registration closes when `RegisterHTTP` returns: a module that keeps the
  registrar, or any router it handed out, and registers later panics with
  `apimodule.RegistrationClosedError` instead of presenting a surface nobody
  validated.

## What this feature deliberately does not do

- **No business logic.** It is hosting infrastructure.
- **No composition decisions.** Which backend fills which seam is go-quicbaas's
  call.
- **No per-module authorization model.** One registry, one authorizer.
