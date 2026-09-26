# Barrier and Seal — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-10

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

Officer, share-claim and recovery-officer endpoints belong to
[../shamir-custody/api.md](../shamir-custody/api.md).

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Barrier()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Status | `GET /v1/barrier/status` | `BarrierStatus` | `barrier.Status` | `qrdb_barrier_status` | `Status` | `seal:status` |
| Initialize | `POST /v1/barrier/initialize` | `BarrierInitialize` | `barrier.Initialize` | `qrdb_barrier_initialize` | `Initialize` | `seal:bootstrap` |
| Initialize Shamir | `POST /v1/barrier/shamir/initialize` | `BarrierShamirInitialize` | `barrier.ShamirInitialize` | `qrdb_barrier_shamir_initialize` | `ShamirInitialize` | `seal:initialize_shamir` |
| Unseal (passphrase) | `POST /v1/barrier/unseal` | `BarrierUnseal` | `barrier.Unseal` | `qrdb_barrier_unseal` | `Unseal` | `seal:unseal` |
| Unseal — one share | `POST /v1/barrier/shamir/unseal-share` | `BarrierShamirUnsealShare` | `barrier.ShamirUnsealShare` | `qrdb_barrier_shamir_unseal_share` | `UnsealShare` | `barrier:unseal-share` |
| Unseal — many shares | `POST /v1/barrier/shamir/unseal-shares` | `BarrierShamirUnsealShares` | `barrier.ShamirUnsealShares` | `qrdb_barrier_shamir_unseal_shares` | `UnsealWithShares` | `barrier:unseal-share` |
| Seal | `POST /v1/barrier/seal` | `BarrierSeal` | `barrier.Seal` | `qrdb_barrier_seal` | `Seal` | `seal:seal` |
| Shamir seal vote | `POST /v1/barrier/shamir/seal-vote` | `BarrierShamirSealVote` | `barrier.ShamirSealVote` | `qrdb_barrier_shamir_seal_vote` | `ShamirSealVote` | `seal:submit_seal_vote` |
| Rekey | `POST /v1/barrier/rekey` | `BarrierRekey` | `barrier.Rekey` | `qrdb_barrier_rekey` | `Rekey` | `seal:rekey` |
| Enable encryption | `POST /v1/barrier/enable-encryption` | `BarrierEnableEncryption` | `barrier.EnableEncryption` | `qrdb_barrier_enable_encryption` | `EnableEncryption` | `seal:enable_encryption` |
| Root token | `POST /v1/barrier/root-token` | `BarrierRootToken` | `barrier.RootToken` | `qrdb_barrier_root_token` | `RootToken` | `seal:bootstrap` |

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
| Tenant unseal share | `POST /v1/tenants/{id}/barrier/unseal-share` | identity-gated |
| Tenant seal vote | `POST /v1/tenants/{id}/barrier/seal-vote` | `TenantBarrierSealVote` |

Tenant barrier operations are served by the dedicated gRPC service
`qrdb.v1.TenantBarrierService` (`Init`, `Unseal`, `UnsealShare`, `Seal`,
`Status`, plus the officer methods). See
[../multi-tenancy/api.md](../multi-tenancy/api.md).

**Identity-gated** means the caller's own authenticated identity authorizes the
call rather than a named permission — a tenant acts on its own barrier.

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

`POST /v1/barrier/shamir/initialize`: same base64 `secret`, plus `threshold`
(M) and `total_shares` (N). **The response is share-free** — shares are sealed
server side to the enrolled officers' enclaves.

`POST /v1/barrier/unseal`: the base64 `secret` alone.

`POST /v1/barrier/shamir/unseal-shares`: a list of shares. Submitting ≥ M
unseals in one call; fewer accumulate and the response reports progress toward
the threshold. Share fields are text strings, not byte strings — see the
limitation note below.

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
initialization timestamp, and — for Shamir barriers — accumulated share
progress.

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
| Wrong passphrase or share set | 403 | `PERMISSION_DENIED` — stays sealed |
| Several strategies registered, none named | 400 | `INVALID_ARGUMENT` — `ErrNoStrategySpecified` |
| Unknown strategy name | 400 | `INVALID_ARGUMENT` |
| One PIN supplied without the other | 400 | `INVALID_ARGUMENT` — dual control |
| Any PIN with `strategy: none` | 400 | `INVALID_ARGUMENT` — `PINsNotAllowedError` |
| Shares below threshold | 200 with progress | `OK` with progress — not an error |
| `enable-encryption` on a tenant using the shared system barrier | 409 | `FAILED_PRECONDITION` |
| `enable-encryption` while another migration on the same barrier (system or tenant) is already in progress | 409 | `FAILED_PRECONDITION` — `seal.ErrMigrationInProgress` |
| CA operation while sealed | 503 | `UNAVAILABLE` — `CAKeyNotAvailableError` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| No encryption migrator wired (standalone, or a composer that never configured one) | `501` | `UNIMPLEMENTED` |
| Migrator wired, but this node cannot migrate (no seal store, not yet unsealed, boot parameters not retained) | `501` | `UNIMPLEMENTED` — `ErrMigrationProviderUnavailable` |
| Tenant `enable-encryption` while a system-scope migration is rebuilding the data plane | `503` | `UNAVAILABLE` — `DataPlaneRecyclingError`; retry |

The complete HTTP status mapping table is in
[seal/README.md](../seal/README.md#barrier-http-status-mapping) if that file is
still present in your tree; the rows above are the ones this feature guarantees.

## Known limitation — share and PIN fields are still text strings

The wipeable-bytes rule above covers the **wrapping credential**. It does not
yet cover every secret on this surface:

- Shamir **share** fields (`share`, `shares`, recovery `keys`, seal-vote
  `share`) are text strings on all transports.
- `so_pin` and `user_pin` are text strings.

Those are secrets too, and a Go string is no more wipeable for a share than it
is for a passphrase. They are named here so the gap is visible rather than
implied by the absence of a sentence, and so a reader does not conclude from
the section above that the whole surface is covered.

## Versioning

Barrier REST paths are under `/v1/`. Nothing here is deprecated, but note the
config-level alias: `seal.strategy: software` is a **deprecated alias** for
`passphrase`, canonicalized at load.

**Breaking change — credential fields moved from string to bytes.** The
credential fields listed under *Schemas* were text strings in earlier
pre-publication releases and are byte strings now. A REST client that sends the
plaintext gets a `400`; a Go SDK caller assigning a string literal gets a
compile error. Both failures are loud, which is why the field was changed in
place rather than duplicated: a parallel byte-typed field would have left the
unwipeable one reachable, and the whole point is that it is not.
