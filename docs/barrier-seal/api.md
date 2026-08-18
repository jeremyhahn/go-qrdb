# Barrier and Seal — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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

`POST /v1/barrier/enable-encryption` is registered **only when an encryption
migrator is wired** (`s.encMigrator != nil`). Without it the route does not
exist, rather than existing and failing.

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

`POST /v1/barrier/initialize`:

```json
{ "strategy": "passphrase", "passphrase": "<passphrase>",
  "so_pin": "SO123", "user_pin": "USR123" }
```

`strategy`: `none` · `passphrase` · `shamir`. `passphrase` is required for
`passphrase`/`shamir` and **rejected** for `none`. PINs come as a pair or not at
all, and are rejected outright with `none`.

`POST /v1/barrier/shamir/initialize`: adds `threshold` (M) and `total` (N).
**The response is share-free** — shares are sealed server side to the enrolled
officers' enclaves.

`POST /v1/barrier/shamir/unseal-shares`: a list of shares. Submitting ≥ M
unseals in one call; fewer accumulate and the response reports progress toward
the threshold.

`POST /v1/barrier/enable-encryption`:

```json
{ "strategy": "passphrase", "passphrase": "…",
  "shamir_threshold": 0, "shamir_total": 0,
  "tenant": "", "mechanism": "swap", "availability": "quiesced" }
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
| CA operation while sealed | 503 | `UNAVAILABLE` — `CAKeyNotAvailableError` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| No encryption migrator wired | 404 (route unregistered) | `UNIMPLEMENTED` |

The complete HTTP status mapping table is in
[seal/README.md](../seal/README.md#barrier-http-status-mapping) if that file is
still present in your tree; the rows above are the ones this feature guarantees.

## Versioning

Barrier REST paths are under `/v1/`. Nothing here is deprecated, but note the
config-level alias: `seal.strategy: software` is a **deprecated alias** for
`passphrase`, canonicalized at load.
