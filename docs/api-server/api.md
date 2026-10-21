# Unified API Server — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21

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
| MCP | Model Context Protocol over HTTPS | 63005, `/mcp` | tools named `qrdb_*` — seventeen JSON-RPC error codes, one per gRPC code, see [the MCP error vocabulary](#the-mcp-error-vocabulary) |

**The Unix transport is not a distinct protocol.** It serves the same gRPC wire
over a domain socket, which is why every feature's `api.md` gives one column for
both.

**gRPC-Web is not a distinct transport either.** With `server.grpc_web.enabled`,
the REST listener (HTTP/1.1, HTTP/2 and HTTP/3) serves every gRPC service the
node hosts as gRPC-Web, under `/grpc-web/`: a client takes
`https://db.example.com:63002/grpc-web` as its base URL, and
`POST /grpc-web/qrdb.v1.QRDBService/Put` is the gRPC method
`/qrdb.v1.QRDBService/Put`. It runs the gRPC server's own interceptor chain, so
its authentication, authorization and status codes are gRPC's column in every
table here.

| | |
|---|---|
| Method | `POST`; a CORS preflight `OPTIONS` is answered; anything else 405 with `Allow: POST` |
| Request `Content-Type` | `application/grpc-web` or `application/grpc-web-text`, with an optional message-format suffix (`+proto` when absent) passed to the gRPC server as `application/grpc+<format>`; any other media type 415 |
| Response encoding | binary, or base64 text when the request was text or its `Accept` names `application/grpc-web-text` |
| Status | `grpc-status` / `grpc-message` in the final trailer frame, or in the headers of a trailers-only response |
| Metadata | request headers: `x-qrdb-database`, `authorization`, and any other gRPC metadata |
| Credential | the TLS client certificate, read by the gRPC chain as on the gRPC listener |
| CORS | only for the origins in `server.grpc_web.allowed_origins`; `Grpc-Status`, `Grpc-Message` and `Grpc-Status-Details-Bin` are exposed; an unlisted origin's preflight is 403 |

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

**The `qrdb-service:` subject namespace is reserved.** It belongs to the
embedder's in-process service principal (`node.WithServicePrincipal`). A
request whose credential resolves to a subject in that namespace, or to an
identity carrying the principal marker, is refused as unauthenticated (REST
401, gRPC `Unauthenticated`) before any handler or authorization runs — unless
it is a delegation certificate a tenant's own CA issued to that principal,
which acts inside that tenant only (see
[../identity-rbac/api.md](../identity-rbac/api.md#delegation-certificates)).
See [engineering.md](engineering.md#service-principal).

## Authorization

Every capability declares its required permission **once**:

| Transport | Where the declaration lives |
|---|---|
| gRPC, Unix | the `qrdb.v1.permission` proto option on the RPC, enforced by a shared interceptor |
| REST | at the route registration site (`Handle`), or `HandlePublic`, or `HandleOptional` |
| MCP | `MCPToolPermissions()`, or `PublicMCPTools()` for a tool that reaches no data |

All of them feed one registry with a **default-deny startup assertion**. A
route, RPC or tool with no declaration fails the assertion at boot rather than
serving unauthenticated.

**Optional-auth module routes.** A module route registered with
`HandleOptional` serves callers with and without a credential. A request that
presents one — a client certificate, an `Authorization` or `DPoP` header, or a
forwarded caller assertion — runs the configured auth chain, including the
module's own `AuthenticateHTTP` fallback, exactly as on a `Handle` route: the
chain resolves a client certificate to its user account, and an invalid
credential is refused with the same status and body. A request that presents
none reaches the handler anonymous. The handler tells the two apart with
`sdk.IdentityFromContext`, which is nil for an anonymous caller. No permission
is checked on the route; what an authenticated caller may do there is the
handler's decision. A certificate from a CA the node does not trust fails the
TLS handshake before any route is reached.

**MCP public tools.** A module's `PublicMCPTools()` names the tools an
anonymous MCP caller may call — its first-contact surface, and optionally the
built-in `qrdb_health`. While any are declared, an anonymous `initialize` and
`tools/list` are answered too; the listing shows the public set only, and
every other request still answers 401. A declared tool carrying any
permission but `admin:health` is refused (`PublicMCPToolRefusedError`), as is
a name no tool answers (`UnknownPublicToolError`); the MCP server does not
start. An authenticated caller's `tools/list` shows the public set plus the
tools its grants allow. See
[engineering.md](engineering.md#mcp-public-tools).

**The permission option is public.** It is defined in
`qrdb/v1/permission.proto`, shipped in the sdk/go module's `proto/` directory,
so a service module built outside go-qrdb annotates its RPCs the way qrdb's
own do:

```proto
syntax = "proto3";

package billing.v1;

option go_package = "example.com/billing/gen/billing/v1;billingv1";

import "qrdb/v1/permission.proto";

service InvoiceService {
  rpc Issue(IssueRequest) returns (IssueResponse) {
    option (qrdb.v1.permission) = "billing:issue";
  }
}
```

Compile it with that directory on the include path:

```sh
protoc -I "$(go list -m -f '{{.Dir}}' github.com/jeremyhahn/go-qrdb/sdk/go)/proto" -I . \
  --go_out=. --go-grpc_out=. billing/v1/invoice.proto
```

The generated code imports `github.com/jeremyhahn/go-qrdb/sdk/go/proto/qrdb/v1`,
which registers the option, so the shared interceptor resolves the annotated
method's permission like any of qrdb's own. The option is field 51000 of
`google.protobuf.MethodOptions`.

**The registry is populated unconditionally**, even for routes a given server
does not enable, so SO dual-factor gating recognizes SO-permissioned paths on
servers that do not serve them.

**A module authorizing against a tenant the caller named, not its own.** The
per-route declaration above confines a module's handler to the REQUEST's own
identity tenant. A module whose operation legitimately acts on a
caller-named tenant implements `ModuleTenantTargetAware`
(`sdk.ModuleTenantTargetAware`) to receive a `TenantTargetAuthorizer`
(`sdk.TenantTargetAuthorizer`) and calls its `AuthorizeTenantTarget(ctx,
tenantID, perm)` from the handler, admitted or refused under the SAME RBAC
ScopeCross/ScopeSelf rules qrdb's own tenant routes enforce — see
[../multi-tenancy/api.md](../multi-tenancy/api.md). An embedded/Unix host
holding the `Node` directly reaches the identical capability through
`sdk/go/node.Node.AuthMiddleware` / `Node.Authorizer`.

## Unauthenticated routes — the complete list

| Route | Condition |
|---|---|
| `GET /health` | registered when admin is enabled |
| `GET /v1/config/initialized` | always |
| `GET /v1/ca/bootstrap` | only when exactly one bundle source is configured: the cluster CA (`WithCABootstrap`) or a host bundle (`WithBootstrapCABundle`); both is a startup error |
| `GET /v1/cluster/nodekey` | only when a cluster key receiver is wired |
| `POST /v1/cluster/keydelivery` | only when a cluster key receiver is wired |

`GET /metrics` requires an authenticated caller but no specific permission. It
is registered by default and absent only when the embedder passed
`server.WithGatherer(nil)`.

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
| **Database** | `X-QRDB-Database` on REST, `x-qrdb-database` metadata on gRPC/Unix (a request header on gRPC-Web), the equivalent field on QUIC/MCP, `Client.DatabaseScoped(name)` embedded |
| **Consistency** | `consistency` parameter or field: `strong`, `linearizable`, `lease_read`, `eventual` |
| **Write mode** | request field: `sync`, `wal_durable`, `batched` |

Absent a database, the server's default (`system`) is used.

## Common status mapping

**Every transport answers one error the same way**, because none of them
decides what kind of failure an error is. That decision is made once, by the
classification table in `pkg/api/errclass`, which maps each typed error to a
**class**; each wire transport then owns one total table from class to its own
code. The embedded transport returns the typed error itself.

| Class | Condition | REST | gRPC / Unix | QUIC | MCP |
|---|---|---|---|---|---|
| — | Success | 200 | `OK` | `StatusOK` (0) | no error envelope |
| `InvalidArgument` | Bad request | 400 | `INVALID_ARGUMENT` | `StatusInvalidArgument` (7) | `INVALID_ARGUMENT` (−32012) |
| `Unauthenticated` | Credential missing or does not verify | 401 | `UNAUTHENTICATED` | `StatusUnauthenticated` (3) | `UNAUTHENTICATED` (−32001) |
| `PermissionDenied` | Missing permission; an operation no caller may perform | 403 | `PERMISSION_DENIED` | `StatusPermissionDenied` (4) | `PERMISSION_DENIED` (−32003) |
| — | Known path, unregistered method | **405** + `Allow` | — (gRPC has no verb axis) | — (no verb axis) | — (no verb axis) |
| `NotFound` | Not found | 404 | `NOT_FOUND` | `StatusNotFound` (6) | `NOT_FOUND` (−32004) |
| `AlreadyExists` | Name or identity taken | 409 | `ALREADY_EXISTS` | `StatusAlreadyExists` (8) | `ALREADY_EXISTS` (−32013) |
| `Aborted` | Concurrency conflict, held lock | 409 | `ABORTED` | `StatusAborted` (11) | `ABORTED` (−32015) |
| `FailedPrecondition` | Resource not in a state this operation starts from | 409 | `FAILED_PRECONDITION` | `StatusFailedPrecondition` (9) | `FAILED_PRECONDITION` (−32009) |
| `Gone` | Revoked or expired factor or challenge | 410 | `FAILED_PRECONDITION` | `StatusFailedPrecondition` (9) | `FAILED_PRECONDITION` (−32009) |
| `PreconditionFailed` | Consistency requirement unmet; PIN or strategy never set | 412 | `FAILED_PRECONDITION` | `StatusFailedPrecondition` (9) | `FAILED_PRECONDITION` (−32009) |
| `Unprocessable` | Feature not enabled or ceremony not configured here | 422 | `FAILED_PRECONDITION` | `StatusFailedPrecondition` (9) | `FAILED_PRECONDITION` (−32009) |
| `ResourceExhausted` | Quota exceeded, rate limited, PIN locked out | 429 | `RESOURCE_EXHAUSTED` | `StatusResourceExhausted` (10) | `RESOURCE_EXHAUSTED` (−32008) |
| `TooLarge` | Body, payload, key or import over its bound | 413 | `RESOURCE_EXHAUSTED` | `StatusResourceExhausted` (10) | `RESOURCE_EXHAUSTED` (−32008) |
| `OutOfRange` | Revision below the compaction floor | 416 | `OUT_OF_RANGE` | `StatusOutOfRange` (12) | `OUT_OF_RANGE` (−32016) |
| `Canceled` | Work stopped (`context.Canceled`, a canceled consensus request, a canceled watch) | 500 | `CANCELED` | `StatusCanceled` (13) | `CANCELED` (−32011) |
| `DataLoss` | Persisted or secondary state corrupted | 500 | `DATA_LOSS` | `StatusDataLoss` (14) | `DATA_LOSS` (−32017) |
| `Unavailable` | Barrier sealed, seal state unconfirmed (`SEAL_STATE_UNCONFIRMED`, retryable — not sealed), store closed, not leader, draining; consensus refused the request for now (busy, dropped, shard not ready, no leader yet) or a raft request's wait ran out (`quicraft: timeout`); a server-side bound ran out after rolling back what it had done | 503 | `UNAVAILABLE` | `StatusUnavailable` (5) | `UNAVAILABLE` (−32006) |
| `Unimplemented` | Raft-only capability in standalone | 501 | `UNIMPLEMENTED` — `requires mode: raft` | `StatusUnimplemented` (2) | `UNIMPLEMENTED` (−32014) |
| — | Capability not compiled in | 404 (route unregistered) | `UNIMPLEMENTED` | `StatusUnimplemented` (2) | `UNIMPLEMENTED` (−32014) |
| `DeadlineExceeded` | The caller's deadline passed before the operation completed (`context.DeadlineExceeded`), or a module's origin said so; raft reports its own wait running out as `quicraft: timeout`, which is `Unavailable` | 504 | `DEADLINE_EXCEEDED` | `StatusDeadlineExceeded` (15) | `DEADLINE_EXCEEDED` (−32007) |
| — | A module's origin could not classify | — | `UNKNOWN` | `StatusUnknown` (16) | `UNKNOWN` (−32018) |
| `Internal`, or unclassified | Server fault | 500 | `INTERNAL` | `StatusError` (1) | `INTERNAL` (−32603) |

Four classes exist because HTTP draws a distinction a client acts on that gRPC
does not: a body too large to send (413) is not a quota to wait out (429), and
a revoked factor (410), an unmet precondition (412) and an unconfigured feature
(422) each ask the caller to do something different. They share a gRPC code,
and QUIC and MCP render that code. `Canceled` and `DataLoss` have no HTTP
status of their own and answer 500. The row without a class is reachable
only through a projected capability module, which answers in gRPC's vocabulary
directly.

**One table, and a test that drives every transport.** A typed error gets its
class from exactly one row of `pkg/api/errclass`, and a transport's own refusal
types (a REST body-size refusal, a "service not configured" refusal) declare
their class beside the type. `TestEveryTransportAgreesOnEveryCondition` feeds
every row of the table through the real REST, gRPC, QUIC and MCP mappings and
fails when any two disagree in meaning, whichever side moved.

### The QUIC status vocabulary

The QUIC frame protocol carries a `uint16` status. It declares **seventeen**
statuses — one for each of gRPC's seventeen status codes, so the set of
conditions the two transports can express is identical and one event never
reaches a QUIC caller and a gRPC caller as two different conditions.

Before adding to it:

- **The numbering is qrdb's own, not gRPC's.** `StatusNotFound` is 6 while
  `codes.NotFound` is 5. The correspondence is by MEANING and goes through one
  table in the wire package (`wire.StatusForGRPCCode`,
  `wire.GRPCCodeForStatus`); there is no arithmetic relating the two.
- **A new condition is a lookup, not a protocol change.** Anything the service
  layer can express already has a status, because gRPC's set is closed and this
  set matches it. A genuinely new condition appends at the next free number.
- **`StatusUnknown` is reachable only through a projected capability module.**
  The QUIC transport dispatches a module's unary gRPC methods as frames, so a
  module answers in gRPC's vocabulary by construction; a partial
  correspondence would silently degrade its answers to `StatusError`.

**Version skew.** A client that does not know a status reads it as an
unrecognised number and falls through to its generic request error, never a
misclassification. The server
and the Go SDK ship from one repository, so a mixed deployment loses precision
on old clients and nothing else.

### QUIC protocol version and response acknowledgement

The QUIC listener speaks exactly one protocol, selected by TLS ALPN:
**`qrdb-quic/3`**. A client that does not offer it fails the TLS handshake. The
Go SDK offers only `qrdb-quic/3` and refuses a connection on which any other
protocol was selected.

No other identifier is accepted, so a Go SDK and a server that do not both
speak `qrdb-quic/3` cannot connect. Upgrade both together.

**Error frames.** The body of every response frame whose status is not
`StatusOK` is a protobuf `google.rpc.Status` (`wire.ErrorPayload`):

| Field | Carries |
|---|---|
| `code` | the gRPC equivalent of the frame's status (`wire.GRPCCodeForStatus`), so the body is a well-formed `google.rpc.Status` to any reader. The frame's status is authoritative; a client classifies on it alone |
| `message` | the server's account of the failure. Bytes that are not valid UTF-8 are replaced with U+FFFD |
| `details` | a `google.rpc.RetryInfo` when the failure names how long to wait before a retry can succeed -- a throttled request, an SO PIN lockout, a rate-limited caller -- with the exact delay. It is REST's `Retry-After`, gRPC's `RetryInfo` and MCP's `retry_after_seconds` on this transport. A reader ignores details it does not know |

The Go SDK decodes it with `wire.DecodeErrorPayload`, and hands the delay to
the caller as `RequestError.RetryAfter` (`sdk.RequestError`), as it does on
every other transport. A body that does not
decode is reported as `*wire.PayloadDecodeError` on the request error --
never shown as the server's message, and never classified as a typed
condition such as absence.

| | The client closes its side of the request stream | What the server does after writing a response |
|---|---|---|
| `qrdb-quic/3` | only after it has read the whole response | waits, up to 2 s, for that close before the request counts as finished |

The client's close is an acknowledgement that the response arrived. The
server needs one because QUIC gives it no other way to learn that a reply was
delivered rather than merely queued, and closing a connection discards
anything still queued. The case that matters is a server that is stopping: it
waits for in-flight requests to finish before it closes their connections, and
"finished" means "acknowledged". A system-scope `enable-encryption` restarts
the listener that is answering the request, so this is how the caller of a
fast migration receives `migration_started` rather than a connection closed
with `server shutting down`.

The server closes its own side of the stream in the same step as it writes the
response, so a client that reads to the end of the stream rather than one frame
is not held for the acknowledgement. Watch streams are the exception to
"wait for in-flight requests": a watch never finishes on its own, so a stopping
server ends every open watch at once instead of waiting for it. It ends each
with a final frame — `Canceled`, reason `server shutting down` — which the
client acknowledges by closing its side, exactly as it does a response, so the
frame is delivered before the connection closes.

The listener advertises only `qrdb-quic/3` even when its TLS configuration is
shared with the REST listener, whose `h3` and `h2` it does not speak.

### Absence is typed over QUIC, and never read out of the message

`StatusNotFound` is what lets an SDK consumer's `errors.As` mean the same thing
over QUIC as it does over REST, gRPC, unix, MCP and embedded. No client reads
absence out of message text: a transport failure whose text carries the phrase
"key not found" is not a missing key, and the lease client tells a gone lease
from a broken node by status alone.

The typed error a consumer receives is reconstructed from the id or key the
**caller** supplied, not from the server's prose. Where a not-found can mean
more than one subject — assigning a role to a user, where either may be missing
— every transport answers the shared not-found status and the SDK types
**neither**, because typing one would report the wrong subject as absent.

### The MCP error vocabulary

MCP declares **seventeen** error codes, one for each of gRPC's seventeen status
codes, for the same reason QUIC does: the service layer behind the `qrdb_*`
tools is the one the gRPC transport classifies, and a narrower set is not
simpler, it is lossy.

Four rules:

- **The codes ride in the TOOL RESULT, not in a JSON-RPC `error` object.** The
  Model Context Protocol draws a line the JSON-RPC layer does not: a failure to
  DISPATCH a call — unparseable JSON, an unknown method, an unknown tool — is a
  JSON-RPC error, while a failure INSIDE a tool is an ordinary result carrying
  `isError`. A missing user is the second kind. The payload is an
  `ErrorEnvelope` in the result's `structuredContent`:

  ```json
  {
    "isError": true,
    "content": [{"type": "text", "text": "user not found: 999999"}],
    "structuredContent": {
      "error": {
        "code": -32004,
        "status": "NOT_FOUND",
        "message": "user not found: 999999",
        "tool": "qrdb_user_get"
      }
    }
  }
  ```

  A client that reads only the text content still gets the server's message.

- **The numbers are JSON-RPC numbers even so.** They are drawn from the
  −32000..−32099 band JSON-RPC 2.0 sets aside for implementation-defined server
  errors, and they do not collide with the codes the protocol layer emits for a
  dispatch failure. A consumer therefore reads one integer space whichever
  envelope carried the failure. `INTERNAL` is JSON-RPC's own −32603 for the
  same reason: an unclassified fault carries JSON-RPC's internal-error number.

- **Three numbers in the band are deliberately unassigned** — −32002, −32005
  and −32010. The portfolio's error convention names fourteen application codes
  at −32001..−32014, and three of those names have no gRPC sibling and nothing
  here that means them. Leaving the numbers alone is what stops one integer
  meaning two things depending on which server answered.

- **The server owns no classification clauses of its own.** A tool failure is
  classified by `pkg/api/errclass`, the table every transport renders, and
  reaches the MCP layer as the gRPC code the gRPC transport gives that class;
  the MCP layer only renders it through a seventeen-row correspondence table.
  A condition cannot mean one thing over gRPC and another over MCP, and a new
  condition needs one row in one table.

**Argument refusals carry no code**, and that is a stated gap rather than
an oversight. A tool that refuses before it reaches the service layer —
`key is required`, `value must be valid base64` — builds its own error result
from a string literal, and stamping a code on it centrally would mean guessing
one from prose, which is the classification this vocabulary exists to remove.
Those refusals reach a client as an uncoded failure; giving them codes means
giving their call sites typed errors first.

Every refusal with a TYPED error to hand carries a code: a tool that cannot
resolve the service for its `(tenant, database)` scope returns the error rather
than its message, so it is classified like everything else.

**A module's tools get the envelope without building it.** The error-code
middleware is server-wide and outermost, so a tool a `ServiceModule` registers
through `RegisterMCP` is classified exactly like a `qrdb_*` tool: a handler that
fails by returning a gRPC status gets that code verbatim, one that returns a
qrdb typed error gets its mapped code, and anything else is `INTERNAL`. A
module reports failure by returning an error and never constructs the envelope
itself.

**The contract is exported through the SDK** for clients and tests, so a
composing host does not import the server's wire package: `sdk.MCPCode` and the
seventeen `sdk.MCPCode*` constants, `sdk.MCPErrorPayload`,
`sdk.MCPErrorEnvelope`, `sdk.NewMCPErrorEnvelope`, `sdk.DecodeMCPErrorPayload`
and `sdk.MCPCodes`.

**Version skew** behaves as it does on QUIC: a client that does not know a code
reads an unrecognised number and falls through to its generic request error,
never a misclassification. An uncoded failure degrades the same way.

### Absence over MCP is typed, and never read out of the message

`NOT_FOUND` (−32004) is what lets an SDK consumer's `errors.As` mean the same
thing over MCP as over every other transport. No client reads absence out of
message text.

The lease client reads the message in one place and under one condition: a failure has to
be a `NOT_FOUND` **first**, at a call site that already knows its subject is a
lease, before the message is consulted at all. No transport here has an
expired-specific sibling of not-found, and gRPC's lease client makes the same
split behind the same gate.

As on every other transport, the typed error a consumer receives is
reconstructed from the id or key the **caller** supplied. The error payload
carries the classification and the server's prose — it deliberately does not
name the subject, because a second source for that fact is a second source that
can disagree with the first.

### 403 and 405 are different claims

A request whose **path** is a registered API route but whose **method** carries
no registered permission gets **405 Method Not Allowed** with an `Allow` header
listing the verbs that path does serve. It is still rejected before the handler
— default-deny holds — but the response says the **verb** is wrong
rather than the caller's authorization.

This distinction is not cosmetic. Answering 403 there asserts an authorization
decision about a route that was never registered, and points an operator at
their grants when the problem is the verb.

**The 405 is only reachable after authentication.** An anonymous caller still
gets 401 with no `Allow` header, so the verb set is never disclosed to an
unauthenticated request. 403 keeps its meaning: an authenticated caller whose
grants do not cover a route that exists.

`Allow` is required on a 405 (RFC 9110 §15.5.6), not a nicety.

**A module's own callers reach the 405 too.** A caller on a capability
module's session-token plane (`ModuleAuthFallback`) is not authenticated by the
host, so it would otherwise be stopped one layer earlier, at 401. A 401 says
the credentials failed, sending the operator off to re-issue a
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

The disclosure guarantee holds throughout: a caller the fallback also
rejects gets 401 with no `Allow` header.

**gRPC does not have this case.** grpc-go rejects an unregistered method with
`UNIMPLEMENTED` before any interceptor runs, so a permission-lookup miss there
means the method *is* served and carries no annotation — a server
misconfiguration, not a client's wrong verb.

## Module registration order

Modules register **before** built-ins, and a REST route has exactly one owner:
a module claiming a built-in's route, or another module's, **fails
construction** with a panic naming both claimants. Two patterns that differ
only in a path parameter's name are the same route. A route a module registers
directly on the embedded router that another registration also claims fails
construction too, and so does registering after `RegisterHTTP` has returned.
A module may not call `Mount`, `Route`, `NotFound`, `MethodNotAllowed` or `Use`
on the registrar's router; it serves a subtree as
`Handle(method, "/prefix/*", perm, h)`. Namespace module routes under the
module's own prefix, and keep its first static segment clear of the embedded web
UI's paths: an unmatched request under a module's route prefix gets 404 rather
than the web UI, so an overlapping prefix hides those web-UI paths.

## Versioning

Path versioning is inconsistent across features and that is the current state,
not a scheme: control-plane and newer surfaces are under `/v1/`
(`/v1/tenants`, `/v1/barrier`, `/v1/ceremony`, `/v1/backups`, `/v1/credentials`,
`/v1/collections`, `/v1/databases`, `/v1/rag`, `/v1/embeddings`, `/v1/audit`,
`/v1/rebalance`), while the data plane and several older surfaces are
unversioned (`/kv`, `/watch`, `/lease`, `/lock`, `/index`, `/ts`, `/vector`,
`/tx`, `/batch`, `/certs`, `/users`, `/roles`, `/health`, `/status`, `/peers`).
The gRPC service is `qrdb.v1` throughout.

Nothing is deprecated at the route level. The certificate routes are under
`/certs`; the CLI group that drives them is `qrdb ca`.
