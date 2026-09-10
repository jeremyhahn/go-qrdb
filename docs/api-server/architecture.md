# Unified API Server — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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

`BaseModule` supplies no-op defaults for every hook **except `Name`**, so a
module embeds it and overrides only what applies — and cannot accidentally be
anonymous.

Optional extensions:

| Interface | Purpose |
|---|---|
| `MCPToolPermissioner` | declare each MCP tool's permission; **a tool registered without one stays fail-closed** |
| `GRPCPublicMethodLister` | declare gRPC methods reachable without auth, symmetric with `HandlePublic` |
| `ModuleAuthFallback` | authenticate credentials the host does not understand — a capability's own session-token plane |

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

## Trust boundaries

- **A bind address holds on every transport or on none.** REST and HTTP/3 serve
  the same handler on the same port; binding is enforced to agree at start
  rather than reported in a log line an operator would have to notice.
- **Default deny.** A route, RPC or tool with no permission declaration fails the
  startup assertion rather than serving unauthenticated.
- **Public routes are explicit and few** — `GET /health`,
  `GET /v1/config/initialized`, and `GET /v1/ca/bootstrap` when configured.
  `GET /metrics` requires authentication but no specific permission.
- **A module cannot grant itself authority.** Its fallback returns an identity;
  the host enriches and authorizes it.
- **A malformed public gRPC method name is rejected at registration**, so a typo
  fails startup loudly instead of silently leaving a method auth-gated.
- **Modules register before built-ins yield**: a built-in route whose
  method+pattern a module has claimed yields to the module; module-vs-module
  duplicates fail construction loudly.

## What this feature deliberately does not do

- **No business logic.** It is hosting infrastructure.
- **No composition decisions.** Which backend fills which seam is go-quicbaas's
  call.
- **No per-module authorization model.** One registry, one authorizer.
