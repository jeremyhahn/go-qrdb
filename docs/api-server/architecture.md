# Unified API Server — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-27
> Conflicts with this document are defects. Fix them here first.

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
  permission-recording registrar (`Handle`), is explicitly public
  (`HandlePublic`), or is explicitly optional-auth (`HandleOptional`: a
  presented credential is authenticated as on `Handle`, none reaches the
  handler anonymous).
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

See [api.md](api.md#the-quic-status-vocabulary) for the vocabulary.

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
to ask that question with the per-route check alone.

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
- **gRPC-Web through connectrpc's vanguard (`vanguardgrpc`).** Rejected: it
  re-registers the generated services on a connect server and calls each
  method handler with a nil gRPC interceptor, and the handler's peer carries no
  TLS state, so the one interceptor chain is skipped and the client
  certificate is lost (vanguard v0.5.0, whose registrar takes no options:
  interceptors belong to the connect server, a second chain). The framing is small and specified, so it is
  implemented in `pkg/api/grpcweb` over the gRPC server's own handler instead.
  Both behaviours are a reproducible probe, `test/vanguardprobe` (a module of
  its own, outside every qrdb build): `make check-vanguard-probe` is the check
  run at each vanguard release, and fails, naming the replacement of
  `pkg/api/grpcweb`, the day a release keeps the interceptor or carries the
  peer's TLS state ([engineering.md](engineering.md#test-strategy)).
- **gRPC-Web through an external proxy (Envoy).** Rejected: a second process
  terminating TLS loses the caller's certificate at the node, the identity every
  authorization decision starts from.
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
- **A small, purpose-built QUIC status set, one status per condition asked
  for.** Rejected: a set assembled one condition at a time ends up arbitrary,
  and every new condition becomes a protocol change rather than a lookup. The
  handlers behind QUIC reach most of gRPC's codes (`pkg/api/grpc`), so the
  QUIC vocabulary (`pkg/api/quic/wire`) declares one status per gRPC code.
- **Adopting gRPC's status codes by NUMBER as well as by meaning.** Rejected:
  it would renumber `StatusError`, `StatusUnimplemented`,
  `StatusUnauthenticated`, `StatusPermissionDenied` and `StatusUnavailable`,
  which breaks every deployed peer at once and buys nothing a lookup table does
  not give. The correspondence is by meaning, through one table, and no
  arithmetic relates the two numberings.
- **Declaring only the codes qrdb's own handlers emit.** Rejected: the module
  projection above makes the table's TOTALITY the property that matters. A
  missing row silently degrades a module's answer, so every gRPC code has a
  status, whether or not a qrdb handler produces it.
- **A bespoke error vocabulary for MCP.** Rejected: it would hand a consumer a
  second translation table and make the transport-by-transport conformance
  matrix a translation exercise rather than a comparison. MCP declares one code
  per gRPC status code, by meaning, as QUIC does, with JSON-RPC numbers,
  because the envelope is JSON-RPC's and a foreign registry's integers do not
  belong in it.
- **Emitting MCP's codes as JSON-RPC `error` objects.** Rejected on the
  specification: a failure inside a tool is a RESULT carrying `isError`, and
  only a dispatch failure is a protocol error. It is also not available — the
  mcp-go server pins every handler-returned error to `INTERNAL_ERROR` with a
  nil `data` field and gives a handler no way to influence either, so the
  spec-correct channel is the only channel.
- **An error classifier of its own in `pkg/api/mcp`.** Rejected: one more
  classifier to keep in agreement with the others. The MCP server renders the
  gRPC code instead, so it owns no condition clauses and has nothing to drift.
- **One classifier per transport, kept in agreement by tests.** Rejected:
  per-transport classifiers drift, and the same error comes to mean different
  things on different transports. `pkg/api/errclass` owns what kind of failure
  every typed error is, as an ordered table of one-line rows; each transport
  owns only a total map from that class to its own code. The authentication
  classifier (`middleware.ClassifyAuthError`) stays the owner of the auth
  failures: the table delegates to it, because its classes also drive control
  flow a wire class cannot express.
- **Classes that are exactly gRPC's seventeen codes.** Rejected: HTTP draws
  distinctions a client acts on that gRPC does not (413 against 429; 410, 412
  and 422 against 409), and collapsing them would change REST answers clients
  rely on. A class is a meaning; each transport's projection is a function of
  the class alone, which is what makes "one error, one meaning" a property of
  the table.
- **Stamping a code on every MCP tool error, including the argument refusals.**
  Rejected: those refusals are built from string literals, and the only way to
  code them centrally is to infer a classification from prose. They stay
  uncoded, and a test pins the boundary so it is a stated gap. Coding them
  needs typed errors at those call sites first.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Unified server, transport hosting, authorization registry | `pkg/server` | go-qrdb |
| `ServiceModule` and its extensions | `pkg/apimodule` | go-qrdb |
| Per-transport handlers | `pkg/api/{rest,grpc,quic,mcp,unix,etcd}` | go-qrdb |
| gRPC-Web framing over the gRPC server's handler | `pkg/api/grpcweb` | go-qrdb |
| Per-(tenant, database) service resolution | `pkg/service` (`service.Registry`) | go-qrdb |
| Node composition entry points | `sdk/go/node` | go-qrdb |
| SDK Tier 2 aliases | `sdk/go/server/server.go` | go-qrdb |
| Capability modules | — | **go-xkms, go-quicbaas, go-trusted-platform** |

### Capability rows and the SDK edge

The feature is the three rows of the constitution's capability registry under
*API server framework*:

| Capability | Tier | Reach via the qrdb SDK |
|---|---|---|
| Unified multi-protocol API server | none: hosting infrastructure, consumed by embedding | `node.NewStandaloneNode` / `node.NewClusteredNode` server options |
| `ServiceModule` seam | 2 | `sdk/go/server`: `ServiceModule`, `BaseModule`, `WithModule`, `ModuleHTTPRegistrar` |
| One-source authorization | 2 | the `qrdb.v1.permission` proto option and the per-transport registration registries |

All three are composer-only from the layers above. The server's own lower
layer is go-quicraft, the Raft library this repository embeds: `pkg/server`
reaches it through go-quicraft's public `pkg` packages (the host, `config`,
`sm`, `logdb`), never through its internals.

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
behind it.

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

### gRPC-Web — the gRPC server's own handler, not a seventh chain

A browser cannot speak native gRPC: it can neither read HTTP/2 trailers nor
present the framing gRPC needs. gRPC-Web (`docs/reference/grpc-web-protocol.md`)
is the standard answer, and the node serves it from the REST server under
`/grpc-web/` when `server.grpc_web.enabled` is set (or `server.WithGRPCWeb` is
given). It is **a wire encoding of the gRPC transport, not a transport of its
own**:

```
  browser / gRPC-Web client
        │ POST /grpc-web/qrdb.v1.QRDBService/Put   (REST listener: HTTP/1.1, HTTP/2, HTTP/3)
        ▼
  pkg/api/rest  grpcWebDispatch ── ahead of every REST layer
        ▼
  pkg/api/grpcweb  Handler ── framing only: media type, base64 text, trailer frame, CORS
        ▼
  (*grpc.Server).ServeHTTP ── the gRPC server's own interceptor chain
        ▼
  pkg/service
```

`pkg/api/grpcweb` translates each request into a native gRPC request and hands
it to `(*grpc.Server).ServeHTTP` of the server that serves the gRPC listener.
So a gRPC-Web call runs **the one gRPC interceptor chain** — authentication,
tenant derivation, the sealed window, the `qrdb.v1.permission` check, request
logging — and grpc-go reads the caller's client certificate from the request's
TLS state exactly as it does on the gRPC listener. Every gRPC service the
server hosts is reachable, its own and every module's, with no registration
of its own. The translator holds no authorization logic, so there is nothing
in it to drift from native gRPC.

The REST server sends a request under the prefix straight to the translator,
ahead of its own authentication, authorization and CORS layers — those would be
a second chain — and refuses at construction any REST route that would match a
path under the prefix (`*rest.GRPCWebRouteCollisionError`), so the prefix has
one owner. CORS for gRPC-Web is its own, configured beside it
(`allowed_origins`, `allow_credentials`), off unless origins are listed.

### Metrics: one gatherer, two registries

`/metrics` serves two Prometheus registries through one gatherer
(`pkg/server/metrics_gatherer.go`), the server's default unless the embedder
passes `server.WithGatherer`:

- the **process registry** (`pkg/util/metrics`) — the Go runtime and process
  collectors and every `qrdb_*` series;
- the **shared go-quicraft collector's registry**
  (`database.SharedRaftMetrics`) — every `quicraft_*` series.

The process registry is read on each scrape, so a test that swaps it is
scraped as swapped. go-quicraft's registry carries no runtime collectors, so
the two never yield the same family twice.

**One collector per process.** go-quicraft's `PrometheusMetricsCollector` is
created once, on first use, and every Raft host in the process reports to it
through a `database.RaftMetrics` wrapper: the WAL that `database.NewRaftWAL`
opens (the path both `qrdbd start` and `sdk/go/node` boot through), the
per-database Raft hosts the backend factory builds, and the per-tenant hosts.
The SDK runs several nodes in one test process; a collector per node would
either collide on one registry or leave all but one unscraped. Two hosts can
run replicas of the same shard ID, and go-quicraft deletes a shard's series on
unregistration, so the wrapper reference-counts shard registrations and passes
only the first and the last through.

**Sync latency.** Every durable sync goes through a `pkg/syncwatch.Observer`,
which records the duration and logs one warning when it crosses the slow-sync
threshold. For the Raft WAL the observation point is qrdb's side of go-quicraft's
`MetricsCollector.ObserveFsyncLatency` — `RaftMetrics` records into
`quicraft_logdb_fsync_latency_seconds` and then checks the threshold, naming
the WAL directory — so go-quicraft is unchanged. For storage, the pebble
backend and the pebble-backed vector store (`backend="pebble"`, naming its
directory) time each write committed with `pebble.Sync` and the file backend each
record, rename and directory fsync, into
`qrdb_storage_sync_duration_seconds{backend}`; `database.openStorage` and the
store's pebble wrappers name the database and directory on the observer. The
observer reads an injected `clock.Clock`, so tests time a sync exactly without
touching a disk.

Neither `syncwatch` nor the collector reaches the lean standalone import graph:
`syncwatch` names Prometheus only through `pkg/util/metrics`, and the collector
lives in `pkg/database`, which already carries the go-quicraft edge
(`test/buildgates/lean_standalone_test.go`).

**The threshold is configuration, held process-wide.** It is
`observability.slow_sync_threshold` (`pkg/config`): a Go duration, default one
second (`syncwatch.DefaultSlowThreshold`, etcd's slow-fdatasync default),
refused by `Validate` with a typed `ValidationError` unless it is greater than
zero and at most one minute. It is an observation threshold for the stall
signal only, NOT a durability knob: quicraft keeps per-entry fsync with no
tunable. `ObservabilityConfig.ApplySlowSyncThreshold` is the one place it
reaches `syncwatch`: both boot paths call it before opening any storage —
`qrdbd start` from `configureObservability`, beside the logger it sets there,
and `NewStandaloneNode`/`NewClusteredNode` first thing after their nil check.
It sets `syncwatch.SetSlowThreshold`, a process value every observer built
without `WithThreshold` reads at `Observe` time, which is every production
observer, so it reaches the Raft WAL and every storage backend however and
whenever they were built.

Process-wide rather than threaded through each constructor for the same reason
the collector is: a storage backend opened with its default observer — the
CLI's embedded paths, restore and the partition factory among them — has no configuration in reach, and an observer that took
the threshold at construction would miss one set later. The cost is the one
the collector already pays: several SDK nodes in one process share one
threshold, the last constructor's.

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
port on the SDK node still selects the SDK node's fixed default (63001 / 63002), not a
kernel-assigned port. Those SDK defaults differ from the daemon's configuration
defaults (REST 63002, gRPC 63003, QUIC 63004, `config.DefaultConfig`): the SDK
node's gRPC default is the daemon's REST port.

## Data flow

One request, from the wire to the store and back:

1. **The transport terminates the connection.** REST serves HTTP/2 over TCP
   and HTTP/3 over UDP on the same port; gRPC and the MCP endpoint listen on
   TCP, the native QUIC transport on UDP, each on its own port of
   `config.Server.Host`; the Unix transport serves gRPC on a local socket; the
   embedded transport is a Go call inside the process and crosses no
   boundary. TLS, and the client certificate when mTLS is configured, ends at
   the listener.
2. **Authentication** turns the presented credential into an identity: a
   certificate, a bearer token, a cluster peer's on-behalf assertion or, for a
   credential the host does not understand, a module's `ModuleAuthFallback`.
   The tenant comes from that identity; the database comes from the request's
   database scope (`X-QRDB-Database` or its per-transport equivalent).
3. **Authorization** looks up the permission the route, RPC or tool declared
   at registration and asks the authorizer for it. A denial is the
   transport's typed refusal, written to the audit log, and nothing below
   this step runs.
4. **The handler delegates to `pkg/service`.** `service.Registry` resolves the
   servicer for the request's (tenant, database), and the servicer runs the
   operation against its engine. A request a `ServiceModule` registered goes
   to that module's handler instead, after the same steps 1-3.
5. **The engine reaches Raft.** A write is proposed to the database's shard
   through go-quicraft and applied by each replica's state machine to its
   storage backend, where it is persisted; replication crosses the host
   boundary over go-quicraft's QUIC transport. A request that reaches a node
   which cannot serve it is forwarded as
   [../replica-forwarding/](../replica-forwarding/) describes.
6. **The result returns** along the same path and is encoded in the
   transport's own form, an error through the shared error classification.

The per-capability detail of steps 4 and 5 is in each feature's own data flow,
for example [../key-value/architecture.md](../key-value/architecture.md#data-flow).

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
  the decoded key.
- **Public routes are explicit and few** — Health on every transport (`GET /health`,
  gRPC / Unix `Health`, QUIC `admin.Health`, MCP `qrdb_health`, one
  `pkg/api/publicverbs.Table` row, rate-limited before serving under the
  `admin:health` dimension by caller address), invitation redemption,
  `GET /v1/config/initialized`, `GET /v1/ca/bootstrap` when configured, the
  two cluster key routes when a receiver is wired (they authorize the peer
  node themselves), and the MCP protected-resource metadata document when
  configured. `GET /metrics` requires authentication but no specific
  permission, and is served unless the embedder turns it off. The full list is in
  [engineering.md](engineering.md#public-and-authenticated-only-routes).
- **MCP authenticates before the MCP layer reads anything.** With
  authentication configured, every request to the MCP endpoint is
  authenticated at the HTTP layer, so `initialize`, `ping` and `tools/list`
  are not answered anonymously, and every request body is bounded. The one
  exception is the public tools: the built-in `qrdb_health` (a
  `publicverbs.Table` verb, whenever the admin service is on) and the
  embedder's declared first-contact surface — a module that implements
  `MCPPublicToolLister` names tools such as liveness, readiness and login
  begin/finish. They are served without credentials, and while any exist an
  anonymous caller may also `initialize` and `tools/list`, which lists the
  public set only. `ping` and every other tool still answer `401`.
- **The SDK connects without a credential.** `Connect` on every Go SDK
  network transport probes Health, which is public, so a client with no
  credential connects and is refused with the typed `Unauthenticated` error
  at its first authenticated call (ruling 264(b)); over MCP one refusal
  capture types the refusal of `initialize` and of every `tools/call` alike.
- **A public MCP tool cannot reach data.** A declared tool that carries any
  permission but `identity:public` — a KV, SQL, document, vector, admin, IAM or
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
- **The embedded transport binds its caller before anything dispatches.** A
  network transport's identity comes from its authenticating listener; the
  embedded transport has none, so the identity on the caller's context is the
  caller. `embedded.WithContextBinder` (re-exported as
  `node.WithContextBinder`, which binds the transport behind `Node.Client`)
  is the in-process counterpart of that listener. Every unary and streaming
  entry point opens with one helper, `Transport.admit`: the sealed-window gate,
  then the binder. The context the binder returns is the one the auth
  middleware authorizes and the handler runs on; a binder error is returned
  unchanged and no handler is reached; a nil context is refused with
  `NilBoundContextError`. The bound context is marked with the binder that
  bound it, so an entry point that re-enters another (`Request` dispatching to
  `Put`) or a database-scoped clone, which shares the binder, binds once. The
  node's internal clients are built without it. A reflection test walks every
  request method of the transport and its sub-clients under a refusing binder,
  so an entry point added without `admit` fails it. Calls a non-replica
  node forwards to a replica leader carry the `WithEmbeddedCaller` identity,
  as above; the binder runs on the local node, before the forward.
- **gRPC-Web is decided by the gRPC chain, never by REST's.** A call under
  `/grpc-web/` bypasses the REST middleware and reaches the gRPC server's own
  interceptors with its TLS state intact, so every caller gets the status code
  native gRPC gives it. A REST route under the prefix fails construction.
- **A malformed public gRPC method name is rejected at registration**, so a typo
  fails startup loudly instead of silently leaving a method auth-gated.
- **A REST route has exactly one owner.** Modules register before the
  built-ins, and a second claim on a route — a module over a built-in, a module
  over another module — **fails construction** with `rest.RouteCollisionError`
  naming both parties. Nothing yields, so no module a host mounts can take over a
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
