# Unified API Server — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

This feature *is* the API surface — it hosts every other feature's capabilities.
What follows is the shape common to all of them; the capability tables live in
each feature's own `api.md`.

## The six transports

| Transport | Wire | Default port / path | Notes |
|---|---|---|---|
| embedded | in-process Go calls | — | desktop/GUI build for the CLI; `sdk/go/node` in library use |
| unix | **gRPC over a UDS** | `/var/run/qrdbd/qrdbd.sock` | identical service definitions to gRPC |
| REST | HTTP/JSON (chi) | 63002 | |
| gRPC | HTTP/2 | 63003 | `qrdb.v1.QRDBService` and the auxiliary services |
| QUIC | method/frame dispatch | 63004 | seventeen wire statuses, one per gRPC code — see [the status vocabulary](#the-quic-status-vocabulary) |
| MCP | Model Context Protocol over HTTPS | configured endpoint | tools named `qrdb_*` |

**The Unix transport is not a distinct protocol.** It serves the same gRPC wire
over a domain socket, which is why every feature's `api.md` gives one column for
both.

Beside these run **per-database protocol listeners** — MySQL and PostgreSQL
([../sql/api.md](../sql/api.md)) and etcd v3
([../etcd-compatibility/api.md](../etcd-compatibility/api.md)). Those speak
foreign protocols over the same services; they are not a seventh transport.

## gRPC services

| Service | Covers |
|---|---|
| `qrdb.v1.QRDBService` | the great majority of capabilities — data plane, admin, identity, barrier, ceremony, tenants, credentials, backup, rebalance, embedding, RAG, SQL |
| `qrdb.v1.TenantBarrierService` | per-tenant barrier — see [../multi-tenancy/api.md](../multi-tenancy/api.md) |
| `qrdb.v1.AuditService` | audit query — see [../audit/api.md](../audit/api.md) |
| `qrdb.v1.AuthFactorService` | ceremony-time custodian factors |
| `qrdb.v1.UserAuthFactorService` | the user-side N-factor model — see [../auth-factors/api.md](../auth-factors/api.md) |

## Authentication

**mTLS on every transport.** The certificate CN is the username and the OU is
the tenant; the SPKI thumbprint is what a user record binds to. There is no
password anywhere. See [../identity-rbac/api.md](../identity-rbac/api.md).

A capability module may implement `ModuleAuthFallback` to authenticate
credentials the host does not understand — a session-token plane, for instance.
**The identity it returns is then enriched and authorized by the host's single
path: roles come from the store, never from the token.**

## Authorization

Every capability declares its required permission **once**:

| Transport | Where the declaration lives |
|---|---|
| gRPC, Unix | the `qrdb.v1.permission` proto option on the RPC, enforced by a shared interceptor |
| REST | at the route registration site (`Handle`), or `HandlePublic` |
| MCP | `MCPToolPermissions()` |

All of them feed one registry with a **default-deny startup assertion**. A
route, RPC or tool with no declaration fails the assertion at boot rather than
serving unauthenticated.

**The registry is populated unconditionally**, even for routes a given server
does not enable, so SO dual-factor gating recognizes SO-permissioned paths on
servers that do not serve them.

## Unauthenticated routes — the complete list

| Route | Condition |
|---|---|
| `GET /health` | registered when admin is enabled |
| `GET /v1/config/initialized` | always |
| `GET /v1/ca/bootstrap` | only when a bootstrap certificate is configured |
| `GET /v1/cluster/nodekey` | only when a cluster key receiver is wired |
| `POST /v1/cluster/keydelivery` | only when a cluster key receiver is wired |

`GET /metrics` requires an authenticated caller but no specific permission, and
is registered only when a metrics gatherer is configured.

**Nothing else is public.** A route not on this list and not carrying a
permission fails the startup assertion.

"Unauthenticated" here means "not subject to user RBAC", not "unauthorized".
The two cluster key routes authorize the calling NODE: its certificate must
chain to the cluster CA, carry a cluster-node SPIFFE SAN, and not be revoked,
and the placement table must assign the requested replica to this node. A
revoked node certificate is refused with 401, and a node that cannot read
revocation state answers 503 rather than admitting the delivery.

## Scoping

| What | How it travels |
|---|---|
| **Tenant** | derived from the authenticated mTLS identity — **never a request field** |
| **Database** | `X-QRDB-Database` on REST, `x-qrdb-database` metadata on gRPC/Unix, the equivalent field on QUIC/MCP, `Client.DatabaseScoped(name)` embedded |
| **Consistency** | `consistency` parameter or field: `strong`, `linearizable`, `lease_read`, `eventual` |
| **Write mode** | request field: `sync`, `wal_durable`, `batched` |

Absent a database, the server's default (`system`) is used.

## Common status mapping

| Condition | REST | gRPC / Unix | QUIC |
|---|---|---|---|
| Success | 200 | `OK` | `StatusOK` (0) |
| Bad request | 400 | `INVALID_ARGUMENT` | `StatusInvalidArgument` (7) |
| Unauthenticated | 401 | `UNAUTHENTICATED` | `StatusUnauthenticated` (3) |
| Missing permission | 403 | `PERMISSION_DENIED` | `StatusPermissionDenied` (4) |
| Known path, unregistered method | **405** + `Allow` | — (gRPC has no verb axis) | — (no verb axis) |
| Not found | 404 | `NOT_FOUND` | `StatusNotFound` (6) |
| Conflict | 409 | `ALREADY_EXISTS` | `StatusAlreadyExists` (8) |
| Precondition | 409 | `FAILED_PRECONDITION` | `StatusFailedPrecondition` (9) |
| Quota exceeded, rate limited | 429 | `RESOURCE_EXHAUSTED` | `StatusResourceExhausted` (10) |
| Concurrency conflict | 409 | `ABORTED` | `StatusAborted` (11) |
| Revision below the compaction floor | 400 | `OUT_OF_RANGE` | `StatusOutOfRange` (12) |
| Work stopped | 499 | `CANCELED` | `StatusCanceled` (13) |
| Persisted state corrupted | 500 | `DATA_LOSS` | `StatusDataLoss` (14) |
| Barrier sealed, store closed, not leader, draining | 503 | `UNAVAILABLE` | `StatusUnavailable` (5) |
| Raft-only capability in standalone | 501 | `UNIMPLEMENTED` — `requires mode: raft` | `StatusUnimplemented` (2) |
| Capability not compiled in | 404 (route unregistered) | `UNIMPLEMENTED` | `StatusUnimplemented` (2) |
| Unclassified server fault | 500 | `INTERNAL` | `StatusError` (1) |

### The QUIC status vocabulary

The QUIC frame protocol carries a `uint16` status. It declares **seventeen**
statuses — one for each of gRPC's seventeen status codes, so the set of
conditions the two transports can express is identical and one event never
reaches a QUIC caller and a gRPC caller as two different conditions.

It carried **six** until that change, and everything outside those six arrived
as the generic `StatusError`: a missing key, a missing user, a bad argument, a
quota rejection and a broken node were one frame. Four separate pieces of work
escalated to the gap rather than inventing a status locally, which is why the
whole vocabulary was settled at once instead of a status at a time.

Three things follow from the decision and are worth knowing before adding to it:

- **The numbering is qrdb's own, not gRPC's.** Statuses 0–5 predate the
  vocabulary and were not renumbered, so `StatusNotFound` is 6 while
  `codes.NotFound` is 5. The correspondence is by MEANING and goes through one
  table in the server package; there is no arithmetic relating the two.
- **A new condition is a lookup, not a protocol change.** Anything the service
  layer can express already has a status, because gRPC's set is closed and this
  set matches it. A genuinely new condition appends at the next free number.
- **Two statuses are reachable only through a projected capability module**
  (`StatusDeadlineExceeded`, `StatusUnknown`). The QUIC transport dispatches a
  module's unary gRPC methods as frames, so a module answers in gRPC's
  vocabulary by construction; a partial correspondence would silently degrade
  its answers to `StatusError`.

**Version skew.** A client built before a status existed reads it as an
unrecognised number and falls through to its generic request error — degraded
to exactly the behaviour it had before, never a misclassification. The server
and the Go SDK ship from one repository, so a mixed deployment loses precision
on old clients and nothing else.

### Absence is typed, and never read out of the message

`StatusNotFound` is what lets an SDK consumer's `errors.As` mean the same thing
over QUIC as it does over REST, gRPC, unix and embedded. Two message-text
classifications were deleted when it landed:

- the client's `strings.Contains(msg, "key not found")` test, which read a
  transport failure whose text happened to carry the phrase as "this key does
  not exist";
- the lease client's gate on `StatusError`-plus-message, which could not tell a
  gone lease from a broken node.

The typed error a consumer receives is reconstructed from the id or key the
**caller** supplied, not from the server's prose. Where a not-found can mean
more than one subject — assigning a role to a user, where either may be missing
— every transport answers the shared not-found status and the SDK types
**neither**, because typing one would report the wrong subject as absent.

### 403 and 405 are different claims

A request whose **path** is a registered API route but whose **method** carries
no registered permission gets **405 Method Not Allowed** with an `Allow` header
listing the verbs that path does serve. It is still rejected before the handler
— default-deny has not changed — but the response says the **verb** is wrong
rather than the caller's authorization.

This distinction is not cosmetic. Answering 403 there asserts an authorization
decision about a route that was never registered, and points an operator at
their grants when the problem is the verb. Measured: an operator
probing with `PUT` where the write verb is `POST` read the 403 as a permissions
defect and filed a bug against the route-permission table — a fix that would
have been inert, because that table only decorates routes that exist.

**The 405 is only reachable after authentication.** An anonymous caller still
gets 401 with no `Allow` header, so the verb set is never disclosed to an
unauthenticated request. 403 keeps its meaning: an authenticated caller whose
grants do not cover a route that exists.

`Allow` is required on a 405 (RFC 9110 §15.5.6), not a nicety.

**A module's own callers reach the 405 too.** A caller on a capability
module's session-token plane (`ModuleAuthFallback`) is not authenticated by the
host, so it would otherwise be stopped one layer earlier, at 401 — and a 401 is
a worse signpost than the 403 this section replaced. 403 at least says "you got
in"; 401 says the credentials failed, sending the operator off to re-issue a
token or suspect clock skew for what is a wrong verb. So when the request's
method matches no route on the path, the fallback lookup widens from the
(method, path) route to the **path's owning module**: a module owns a path's
auth plane, not one verb of it.

The widening is bounded by the condition that triggers it. It applies only when
the method matched no route — which is exactly the case that cannot reach a
handler and cannot reach the authorizer — so the identity it establishes only
ever decides 405 versus 401. A method that *does* have a registered route is
untouched: it uses that route's own fallback, or none, because widening there
would let one module's auth plane establish the identity a different owner's
handler is then dispatched for.

Two owners on one path resolve to **no** fallback. Routes are keyed by
method *and* pattern, so two modules may split one path's verbs between them
and bring one auth plane each; which of them owns a verb that neither
registered is not answerable, and the caller keeps its 401.

The disclosure guarantee is unchanged throughout: a caller the fallback also
rejects gets 401 with no `Allow` header.

**gRPC does not have this case.** grpc-go rejects an unregistered method with
`UNIMPLEMENTED` before any interceptor runs, so a permission-lookup miss there
means the method *is* served and carries no annotation — a server
misconfiguration, not a client's wrong verb.

## Module registration order

Modules register **before** built-ins. A built-in route whose method+pattern a
module has claimed **yields** to the module and logs at debug level.
**Module-vs-module duplicates fail construction loudly.**

## Versioning

Path versioning is inconsistent across features and that is the current state,
not a scheme: control-plane and newer surfaces are under `/v1/`
(`/v1/tenants`, `/v1/barrier`, `/v1/ceremony`, `/v1/backups`, `/v1/credentials`,
`/v1/collections`, `/v1/databases`, `/v1/rag`, `/v1/embeddings`, `/v1/audit`,
`/v1/rebalance`), while the data plane and several older surfaces are
unversioned (`/kv`, `/watch`, `/lease`, `/lock`, `/index`, `/ts`, `/vector`,
`/tx`, `/batch`, `/certs`, `/users`, `/roles`, `/health`, `/status`, `/peers`).
The gRPC service is `qrdb.v1` throughout.

Nothing is deprecated at the route level. The `qrdb certs` **CLI** group is
deprecated in favour of `qrdb ca`, but its REST paths were not renamed.
