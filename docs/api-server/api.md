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
| QUIC | method/frame dispatch | 63004 | |
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

## Scoping

| What | How it travels |
|---|---|
| **Tenant** | derived from the authenticated mTLS identity — **never a request field** |
| **Database** | `X-QRDB-Database` on REST, `x-qrdb-database` metadata on gRPC/Unix, the equivalent field on QUIC/MCP, `Client.DatabaseScoped(name)` embedded |
| **Consistency** | `consistency` parameter or field: `strong`, `linearizable`, `lease_read`, `eventual` |
| **Write mode** | request field: `sync`, `wal_durable`, `batched` |

Absent a database, the server's default (`system`) is used.

## Common status mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Bad request | 400 | `INVALID_ARGUMENT` |
| Unauthenticated | 401 | `UNAUTHENTICATED` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Not found | 404 | `NOT_FOUND` |
| Conflict / precondition | 409 | `ALREADY_EXISTS` / `FAILED_PRECONDITION` |
| Quota exceeded | 429 | `RESOURCE_EXHAUSTED` |
| Barrier sealed, not leader, draining | 503 | `UNAVAILABLE` |
| Raft-only capability in standalone | 501 | `UNIMPLEMENTED` — `requires mode: raft` |
| Capability not compiled in | 404 (route unregistered) | `UNIMPLEMENTED` |

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
