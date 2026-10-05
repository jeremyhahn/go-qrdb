# Barrier and Seal — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-01

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

Share holders — enrolment, claims, the quorum rounds that unseal, seal,
mint root tokens, recover and rekey a Shamir barrier, and revocation — belong
to [../shamir-custody/api.md](../shamir-custody/api.md).

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Barrier()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Status | `GET /v1/barrier/status` | `BarrierStatus` | `barrier.Status` | `qrdb_barrier_status` | `Status` | `seal:status` |
| Initialize | `POST /v1/barrier/initialize` | `BarrierInitialize` | `barrier.Initialize` | `qrdb_barrier_initialize` | `Initialize` | `seal:bootstrap` |
| Initialize Shamir | `POST /v1/barrier/shamir/initialize` | `BarrierShamirInitialize` | `barrier.ShamirInitialize` | `qrdb_barrier_shamir_initialize` | `BarrierInitializeShamir` | `seal:initialize_shamir` |
| Unseal (passphrase) | `POST /v1/barrier/unseal` | `BarrierUnseal` | `barrier.Unseal` | `qrdb_barrier_unseal` | `Unseal` | `seal:unseal` |
| Seal (non-Shamir) | `POST /v1/barrier/seal` | `BarrierSeal` | `barrier.Seal` | `qrdb_barrier_seal` | `Seal` | `seal:seal` + SO PIN |
| Enable encryption | `POST /v1/barrier/enable-encryption` | `BarrierEnableEncryption` | `barrier.EnableEncryption` | `qrdb_barrier_enable_encryption` | `EnableEncryption` | `seal:enable_encryption` |
| Cluster unseal | `POST /v1/barrier/cluster-unseal` | `BarrierClusterUnseal` | `barrier.ClusterUnseal` | `qrdb_barrier_cluster_unseal` | `ClusterUnseal` | `seal:unseal` |

A Shamir barrier is unsealed, sealed and rekeyed, and root tokens are minted,
only by a quorum of authenticated share holders: `POST /v1/barrier/quorum/*`
and `POST /v1/barrier/rekey/start`, in
[../shamir-custody/api.md](../shamir-custody/api.md). `BarrierUnseal` refuses a
Shamir barrier (`ErrShamirUnsealRequiresQuorum`) and `BarrierSeal` refuses one
(`ErrShamirBarrierRequiresQuorum`).

### Who may call these

The node barrier belongs to the platform, not to a tenant. Every permission in
the table except `seal:status` is **cross-scope**: only an identity of the
`system` tenant may exercise it, on every transport. The built-in SO, Admin,
Operator and Custodian roles of any other tenant carry the same grants and are
refused with `403` / `PermissionDenied`. Apart from enable-encryption's
optional `tenant_id`, none of these operations names a tenant, so there is
nothing a tenant could own here; a tenant's own barrier has its own endpoints
and `tenant_barrier:*` permissions (below). `seal:status` reads state and
names no share holder.

**Enable-encryption is a platform-operator act for both of its targets.**
Migrating the node barrier, or a tenant's dedicated barrier named by
`tenant_id`, is done by the `system` tenant. There is no tenant-side route: a
tenant cannot migrate even its own barrier, and the declared
`tenant_barrier:enable_encryption` permission is checked by no handler.

A node holder's shares reach the node barrier only through the identity-gated
quorum submission, authenticated against the roster; there is no permission a
tenant identity could hold to feed the node barrier's quorum. A tenant
holder's shares belong to its tenant's barrier, whose quorum routes are bound
to the tenant they name.

On the boot-time sealed-window listener no RBAC runs at all, so first-boot
initialize and unseal are unaffected.

Enable-encryption is registered **unconditionally** on REST, gRPC, Unix, QUIC,
and MCP, independent of whether a barrier service exists on the node (a
seal-disabled node in raft/clustered mode still runs its system migrator).
When no encryption migrator is configured for that deployment (standalone
mode, or a composer that never wired one), the request still reaches the
handler/tool and is refused there with a typed "no encryption migrator" error
mapped to `501` / `Unimplemented` — never a plain route-not-found `404` or a
"tool absent" response. All five network transports agree on this; only the
transport-specific error type differs (REST returns its usual
`ServiceNotImplementedError`-shaped body, gRPC/Unix `codes.Unimplemented`,
QUIC/MCP their own `ServiceNotConfiguredError`/`ToolError`-wrapped
`service.ErrMigrationProviderUnavailable`).

A migrator that **is** wired but finds this node cannot migrate — no seal
store to persist the migrated root key in, not yet unsealed, or boot
parameters not retained — refuses with an error that matches the same
`service.ErrMigrationProviderUnavailable` through `errors.Is`, and every
transport answers it identically: `501` on REST, `Unimplemented` on gRPC and
Unix, `StatusUnimplemented` on QUIC, `UNIMPLEMENTED` on MCP. It refuses before
anything is torn down.

Every enable-encryption error has the same classification on every transport.
QUIC answers each one with the status the gRPC row names — `StatusInvalidArgument`
for an invalid target, mechanism, availability or Shamir threshold,
`StatusFailedPrecondition` for a source that is not `none` or a migration
already in progress, `StatusPermissionDenied` for a shared-barrier tenant —
rather than its generic `StatusError`. A tenant-scope request that arrives
while a system-scope migration has the data plane torn down gets
`service.DataPlaneRecyclingError`: `503` / `Unavailable`, retry once the
system migration finishes. REST previously gated route
*registration itself* on a barrier service also being present, which made a
seal-disabled node with the migrator configured 404 instead of returning that
typed refusal — the one transport that behaved differently for the exact same
condition.

Embedded (`sdk/go/transport/embedded`) reaches the same refusal through the
same sentinel rather than a route: `sdk/go/node` wires its
`EncryptionMigrator` into the embedded transport the same way the daemon
wires one into the network transports (`server.NewScopedEncryptionMigrator`),
so `EnableEncryption` migrates when one is available and refuses with
`service.ErrMigrationProviderUnavailable` only when it truly isn't. An
embedder can also drive system-scope enable-encryption directly through
`sdk/go/node`'s `Node.EnableEncryption(ctx, scope, opts)` API instead of going
through the `Client.Barrier()` transport (see
[../seal/enabling-encryption.md](../seal/enabling-encryption.md)).

Every Go SDK client (`sdk/go/transport/{rest,grpc,unix,quic,mcp,embedded}`)
forwards `BarrierEnableEncryption` to its transport's server handler above
using the same marshal/dial pattern it uses for every other barrier call; none
of them stub the call locally.

## Per-database and per-tenant barriers

| Capability | REST | Permission |
|---|---|---|
| Database barrier status | `GET /v1/databases/{name}/barrier/status` | `databases:barrier_status` |
| Database seal / unseal | `POST /v1/databases/{name}/seal` · `/unseal` | `databases:seal` · `databases:unseal` |
| Database barrier config | `PATCH /v1/databases/{name}/barrier` | `databases:update` |
| Tenant barrier init | `POST /v1/tenants/{id}/barrier/init` | identity-gated |
| Tenant barrier status | `GET /v1/tenants/{id}/barrier/status` | identity-gated |
| Tenant barrier seal | `POST /v1/tenants/{id}/barrier/seal` | `TenantBarrierSeal` |
| Tenant barrier unseal | `POST /v1/tenants/{id}/barrier/unseal` | identity-gated |

Tenant barrier operations are served by the dedicated gRPC service
`qrdb.v1.TenantBarrierService` (`Init`, `Unseal`, `Seal`, `Status`, plus the
share-holder methods). A Shamir tenant barrier is unsealed and sealed through
its holder quorum. See
[../multi-tenancy/api.md](../multi-tenancy/api.md).

**Identity-gated** means the caller's own authenticated identity authorizes the
call rather than a named permission — a tenant acts on its own barrier.

**Tenant and database barriers admit only their owner.** Every tenant barrier
permission is tenant-only: the platform operator (an identity of the `system`
tenant) is refused for another tenant's barrier, and the tenant service makes
the same refusal for seal and unseal (`TenantScopeError`). A Shamir tenant
barrier's holder quorum is scoped differently: opening a round and submitting
to it (`tenant_barrier` quorum begin and submit) admit the `system` tenant as
well as the tenant itself, because a round completes only with the shares of
the tenant's own authenticated holders -- the platform operator can open a
round, never complete one. `databases:seal`, `databases:unseal` and
`databases:barrier_status` admit only the tenant that owns the database — the
`system` tenant included: it seals its own databases and no other tenant's.

## Sealing in a cluster

Once the cluster has activated the `cluster-seal` feature, every seal on this
page — node seal, tenant seal and database seal, each by one caller or by a
Shamir barrier's share-holder seal quorum — writes the barrier's cluster seal record and is enforced on **every**
node, on all six transports alike; see
[architecture.md](architecture.md#sealing-in-a-cluster). Request bodies do not
change. Before activation each seal acts on the node that receives it.

Unseal stays per node. A tenant or database unseal that succeeds on a node
also writes the clear record when the barrier's record says sealed. The node
barrier's record is cleared by its own capability, **cluster unseal**, in the
matrix above:

- It writes the clear record and returns the barrier's `cluster_seal` object.
- The receiving node must already have unsealed itself; otherwise
  `ErrSealed` (`503` / `UNAVAILABLE`).
- It is on the sealed-window allowlist, because a node held by a cluster seal
  refuses everything else, and it is still authenticated and authorized there.
- A record that is already clear is not rewritten; the call returns it.
- It has no request fields. gRPC and Unix take `BarrierClusterUnsealRequest {}`
  and return `ClusterSealState`.

### The `cluster_seal` status object

Node barrier status (`BarrierStatus`), tenant barrier status
(`TenantBarrierService/Status`, `GET /v1/tenants/{id}/barrier/status`) and
database barrier status (`DatabaseBarrierStatus`,
`GET /v1/databases/{name}/barrier/status`) each carry a `cluster_seal` object on
every transport, and cluster unseal returns one:

```json
"cluster_seal": {
  "active": true,
  "sealed": true,
  "epoch": 7,
  "principal": "alice",
  "principal_tenant": "acme",
  "node": "192.0.2.11:5000",
  "at": "2026-10-01T12:00:00Z",
  "local": "sealed"
}
```

| Field | Meaning |
|---|---|
| `active` | the cluster has activated `cluster-seal`. `false` means every seal on this node is node-local, and the other fields are empty |
| `sealed`, `epoch` | the barrier's record: sealed or clear, and its epoch (`0`: never written) |
| `principal`, `principal_tenant`, `node`, `at` | who wrote the record, from which tenant, through which node, and when |
| `local` | this node's state for the barrier: `serving`, `sealed`, `held` (unsealed here, but the record is sealed — node barrier only), `cut_off` (lease expired), or `unconfirmed` (no successful read since this node started) |

## Schemas

### The barrier credential is bytes, and therefore base64 in REST JSON

Every field that carries a caller-supplied barrier credential — `secret` on
initialize, unseal and shamir-initialize; `passphrase` on enable-encryption and
tenant barrier init — is a **byte string**, not a text string:

- In REST JSON it is **base64**. `{"secret":"aHVudGVyMg=="}` is the passphrase
  `hunter2`. Sending the plaintext where base64 is expected is a `400`, not a
  silently-wrong credential.
- In protobuf (gRPC, Unix, QUIC) the field is `bytes`. proto3 encodes `bytes`
  and `string` identically, so this costs nothing on the wire.
- In the Go SDK the request field is `[]byte`.

The reason is memory hygiene, and it is the same reason at every layer. A Go
string is **immutable**, so a passphrase held as one cannot be zeroized: it
survives on the heap until the garbage collector happens to reuse the page, and
is recoverable from a core dump or a memory scrape for an unbounded period. A
byte slice can be wiped, and is:

- The **caller** owns the slice it puts in the request and may zeroize it as
  soon as the call returns. Nothing in the SDK retains it.
- The **daemon** owns the slice its decoder produced and zeroizes it before the
  handler returns — on the success path and on every error path, including a
  rejected unseal.

Read that as a claim about **retention**, not about the total absence of
plaintext. A wipe destroys the slice and its aliases; it does not reach the
transient copy each encoder makes — the base64 in the JSON body, protobuf's
out-of-place marshalling. Those go unreferenced at the end of the call and are
collected rather than zeroized, and no API at this layer can change that, since
serializing a value means copying it and the wire format owns that copy. What
you get is that the two long-lived copies — the caller's and the daemon's — are
destroyable on demand, which a `string` never was.

One transport is an exception and says so: **MCP** tool arguments are JSON
strings by definition of the protocol, so the MCP client and server each carry
the credential through one Go string at a single documented conversion point.
Callers who cannot accept that should not drive barrier operations over MCP.

### Request bodies

`POST /v1/barrier/initialize`:

```json
{ "secret": "<base64 passphrase>", "barrier_strategy": "passphrase",
  "so_pin": "SO123", "user_pin": "USR123" }
```

`secret` is required for `passphrase`/`shamir` and **rejected** for `none`.
PINs come as a pair or not at all, and are rejected outright with `none`.

Those two rules are enforced **client side**, in `qrdb barrier init`
(`validateBarrierInitFlags`), before any network call, so an operator gets a
local and actionable error. `barrier_strategy` (`none` · `passphrase` ·
`shamir`) is carried on the wire so a client can state its intent, but **the
daemon takes its strategy from its own boot-time seal config** —
`BarrierInitialize` has no strategy parameter on any transport, and a server
that receives the field ignores it.

`POST /v1/barrier/shamir/initialize`: `threshold` (M), `total_shares` (N,
which must equal the number of enrolled holders) and `policy`. There is no
secret: the barrier generates its keys. **The response is share-free** — each
share is sealed to its holder.

`POST /v1/barrier/unseal`: the base64 `secret` alone.

`POST /v1/barrier/enable-encryption`:

```json
{ "strategy": "passphrase", "passphrase": "<base64 passphrase>",
  "shamir_threshold": 0, "shamir_total": 0,
  "tenant_id": "", "mechanism": "swap", "availability": "quiesced" }
```

`mechanism`: `swap` (default) · `in-place`. `availability`: `quiesced`
(default) · `online`. **Asynchronous** — the call returns once the migration has
started; poll `BarrierStatus` until `strategy` changes.

`BarrierStatus` reports the sealed/unsealed state, the active strategy, the
initialization timestamp, and — for Shamir barriers — the threshold and
total of the share set in force.

## Sealed-window behaviour

While the barrier is sealed the data path returns `ErrBarrierSealed` for every
read and write. Only an allowlisted set of endpoints is reachable — the
allowlist, the format-version sentinel and the pre-host listener boot sequence
are specified in [seal/sealed-window.md](../seal/sealed-window.md).

`GET /v1/barrier/status` is read-only and **safe against a sealed barrier**;
that is deliberate, since it is how an operator finds out what to do next.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Barrier sealed (data path) | 503 | `UNAVAILABLE` — `ErrBarrierSealed` |
| Wrong passphrase | 403 | `PERMISSION_DENIED` — stays sealed |
| Credential unseal or single-caller seal of a Shamir barrier | 409 / 422 | `FAILED_PRECONDITION` — use the holder quorum |
| Several strategies registered, none named | 400 | `INVALID_ARGUMENT` — `ErrNoStrategySpecified` |
| Unknown strategy name | 400 | `INVALID_ARGUMENT` |
| One PIN supplied without the other | 400 | `INVALID_ARGUMENT` — dual control |
| Any PIN with `strategy: none` | 400 | `INVALID_ARGUMENT` — `PINsNotAllowedError` |
| `enable-encryption` on a tenant using the shared system barrier | 409 | `FAILED_PRECONDITION` |
| `enable-encryption` while another migration on the same barrier (system or tenant) is already in progress | 409 | `FAILED_PRECONDITION` — `seal.ErrMigrationInProgress` |
| CA operation while sealed | 503 | `UNAVAILABLE` — `CAKeyNotAvailableError` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| No encryption migrator wired (standalone, or a composer that never configured one) | `501` | `UNIMPLEMENTED` |
| Migrator wired, but this node cannot migrate (no seal store, not yet unsealed, boot parameters not retained) | `501` | `UNIMPLEMENTED` — `ErrMigrationProviderUnavailable` |
| Tenant `enable-encryption` while a system-scope migration is rebuilding the data plane | `503` | `UNAVAILABLE` — `DataPlaneRecyclingError`; retry |
| `enable-encryption` (or unseal) on an embedded node that is closing | `503` | `UNAVAILABLE` — `service.ErrNodeClosed`; retry against another node |
| Two seal or clear requests for one barrier race; this one lost | 409 | `ABORTED` — `clusterseal.SealEpochConflictError`; read the record and retry |
| A seal whose record could not be confirmed. **The receiving node is sealed**; the other nodes may not be | 503 | `UNAVAILABLE` — `clusterseal.RecordNotConfirmedError`; retry through another node |
| An unseal whose clear record could not be written. The node sealed the barrier again | 503 | `UNAVAILABLE` — `clusterseal.RecordNotConfirmedError`; retry |
| Cluster unseal before `cluster-seal` is activated | 409 | `FAILED_PRECONDITION` — `clusterseal.ErrInactive` |
| Any request on a node that is held, cut off, or has not yet read the records | 503 | `UNAVAILABLE` — the sealed-window refusal (`SEALED`); `barrier status` shows why in `cluster_seal.local` |
| An open stream ended by a seal | — | the barrier's sealed error: `UNAVAILABLE` |
| An open stream ended because this node's lease ran out | — | `UNAVAILABLE` — `clusterseal.SealStateUnconfirmedError`, retryable |

Share-holder and quorum errors are classified in
[../shamir-custody/api.md](../shamir-custody/api.md#error-classes).

## Known limitation — share and PIN fields are still text strings

The wipeable-bytes rule above covers the **wrapping credential**. It does not
yet cover every secret on this surface:

- `so_pin` and `user_pin` are text strings.

Quorum submissions carry the share as bytes, and every transport wipes the
decoded share after the call.

Those are secrets too, and a Go string is no more wipeable for a share than it
is for a passphrase. They are named here so the gap is visible rather than
implied by the absence of a sentence, and so a reader does not conclude from
the section above that the whole surface is covered.

## Versioning

Barrier REST paths are under `/v1/`. Nothing here is deprecated.

**Breaking change — credential fields moved from string to bytes.** The
credential fields listed under *Schemas* were text strings in earlier
pre-publication releases and are byte strings now. A REST client that sends the
plaintext gets a `400`; a Go SDK caller assigning a string literal gets a
compile error. Both failures are loud, which is why the field was changed in
place rather than duplicated: a parallel byte-typed field would have left the
unwipeable one reachable, and the whole point is that it is not.
