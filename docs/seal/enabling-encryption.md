<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Enabling, Rotating & Disabling Encryption at Rest

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-13
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

This document explains the encryption-at-rest lifecycle for a qrdb deployment:
how transparent decryption works, why the sealing strategy is fixed at
initialization, how to enable encryption on an existing unencrypted deployment,
and how encrypted backups are restored. It is the canonical reference behind the
**Seal Manager → "Encryption Not Configured"** panel in the GUI.

## TL;DR

- Encryption at rest is **transparent**: while the barrier is unsealed, qrdb
  decrypts every read and encrypts every write automatically. Data is never
  write-only and is never locked away from a running, unsealed instance.
- The **sealing strategy is chosen at initialization** (`none` / `passphrase` /
  `shamir`). Enabling encryption on a `none` barrier after the fact is a
  deliberate, in-place **re-encryption migration** — not a config flag — driven
  by `qrdb barrier enable-encryption` (see [Enabling encryption after the
  fact](#enabling-encryption-after-the-fact)).
- The routine key-lifecycle operation is **rekey** (rotate the unseal
  credential / re-wrap the root key), *not* disabling encryption.
- **Enable encryption on an existing `none` deployment** in place with
  `qrdb barrier enable-encryption --strategy passphrase|shamir`. The node
  re-encrypts all at-rest data and transitions the live barrier to the keyed
  strategy with no process restart. Scope is the system barrier, or a single
  tenant's dedicated barrier with `--tenant`.
- **Disabling encryption is not offered** as a casual operation. Dropping a
  required control silently is a compliance regression (PCI-DSS, HIPAA,
  FedRAMP). See [Why "disable encryption" is not a toggle](#why-disable-encryption-is-not-a-toggle).

## The transparent-decryption model

qrdb wraps the entire Pebble VFS file-by-file with the storage barrier
(`atrest.New(barrier)`). When the barrier is **unsealed**:

- Every file read is decrypted on the fly before the engine sees it.
- Every file write is encrypted before it touches disk.

Consequently, "encrypt-only, never decrypt" is a misconception: a running,
unsealed qrdb always serves plaintext to authorized clients. "Decryption" is
not a user-facing operation because it is continuous and automatic.

The only thing that is *not* casually reversible is **stripping the barrier off
the physical files** — and that is intentional, because the encrypted-at-rest
guarantee is exactly the property compliance regimes require you to be unable to
silently drop.

## Strategy is fixed at initialization

`StorageBarrier.Initialize` is a **one-time** operation. A second call returns
`ErrAlreadyInitialized`. The strategy recorded at initialization
(`none`/`passphrase`/`shamir`) determines how the root key is protected for the
life of that barrier.

| Operation | Changes the unseal credential? | Re-encrypts stored data? | Changes the strategy? |
|---|---|---|---|
| **Rekey** (`StorageBarrier.StartRekey`, completed by a holder quorum) | Yes (re-wraps the root key; new Shamir params) | No | No |
| **Re-initialize** (uninitialized barrier only) | Sets it for the first time | n/a (no data yet) | Sets it for the first time |
| **Enable encryption** (`qrdb barrier enable-encryption`) | Sets it (the new keyed credential) | **Yes** (full at-rest re-encryption) | `none` → `passphrase`/`shamir` |
| **Disable / downgrade to plaintext in place** | — | — | **Not supported** (see below) |

Rekey is the routine, compliant rotation operation (PCI-DSS 3.6.1). It changes
*how you unseal*, never *whether data is encrypted*. Enable-encryption is the
deliberate, one-way migration that turns encryption *on*.

## Enabling encryption after the fact

A barrier initialized with `none` can be migrated to a keyed strategy in place,
re-encrypting all at-rest data, with **no process restart**. The source state is
a daemon (or embedded node) configured with `seal.enabled: true` and
`seal.strategy: none` — a barrier-present-but-unencrypted store (zero-epoch
plaintext) that is initialized with an empty secret and serves normally until
migrated. A self-managed tenant barrier reaches the same state via
`POST /v1/tenants/{id}/barrier/init` with `{"strategy":"none"}`.

```
# system barrier (admin)
qrdb barrier enable-encryption --strategy passphrase --passphrase '…'

# a single tenant's dedicated barrier
qrdb barrier enable-encryption --tenant acme --strategy passphrase --passphrase '…'

# Shamir M-of-N target
qrdb barrier enable-encryption --strategy shamir --shamir-threshold 2 --shamir-total 3 --passphrase '…'
```

How it works:

1. The server mints a fresh keyed barrier and re-encrypts every owned store —
   either by a logical copy into a keyed sibling (`--mechanism swap`, default)
   or a byte-faithful raw-file recodec (`--mechanism in-place`), each verified
   against the source before commit (no data loss).
2. The store directories are atomically swapped and the live barrier is
   transitioned to the keyed strategy. The data plane briefly recycles in
   process (a short maintenance window) and resumes — no operator restart.
3. The migration runs **asynchronously**: the command returns once it has
   started; poll `qrdb barrier status` until the strategy changes from `none`
   to the chosen strategy to confirm completion.

**Post-migration unseal (system scope).** A **system**-scope migration recycles
the whole node data plane in process and the keyed barrier comes back up
**sealed** — the daemon does not hold the credential you just set. Unseal once
with the new credential (`qrdb barrier unseal …`, or submit the Shamir quorum)
to resume serving. A **tenant**-scope migration recycles only that tenant's Host
and reopens its barrier **unsealed** (the migration holds the supplied
credential), so no unseal step is needed and the rest of the node keeps serving
throughout.

**Scope is never global.** It is either the **system** barrier (admin authority,
`seal:enable_encryption`) or a single tenant's **dedicated** barrier (`--tenant`).
A tenant on the **shared system barrier** inherits the system settings and
cannot change its own encryption — the server rejects such a request. Take a
verified backup before migrating (see [backup/README.md](../backup-restore/architecture.md)).

The GUI surfaces the same operation from the Seal Manager. When a `none`
barrier is configured it offers the migration; for a barrier subsystem present
but **never initialized**, it offers a first-time **Configure Encryption**
action (a plain `Initialize`, safe because there is no committed strategy or
data yet).

> **Transport support:** the endpoint/tool is available on every SDK
> transport — REST (`POST /v1/barrier/enable-encryption`), gRPC, Unix (the
> default CLI transport), QUIC, MCP, and embedded — registered
> unconditionally and refusing with a typed "no encryption migrator
> configured" error (never a bare 404/tool-not-found/`NotImplementedError`)
> on a deployment where none is wired (standalone mode). Every Go SDK client
> forwards the call to its already-working server handler the same way it
> forwards every other barrier call; none of them stub it locally. An
> embedded node can also drive the migration directly via the
> `node.EnableEncryption(ctx, scope, opts)` API
> (see [Embedded-node tenant enable-encryption](#embedded-node-tenant-enable-encryption) below).
>
> **Deployment-path support:** both scopes work in process (no OS restart) on the
> standalone `qrdbd` daemon AND the embedder / SDK-node path (`sdk/go/node`, used
> by downstream composers such as xkms and quicbaas):
> - **tenant** scope recycles the tenant's dedicated IsolationVolume Host,
>   re-encrypting its state-machine Pebble and Raft write-ahead log in place; the
>   rest of the node keeps serving.
> - **system** scope recycles the node's whole data plane. On the daemon this is
>   an in-process re-initialization (the OS process never exits): it quiesces the
>   data plane, re-encrypts the system Raft host's WAL + state-machine Pebble and
>   the vector store, adopts the keyed root key, and re-runs boot under the keyed
>   barrier (which then awaits the post-migration unseal described above).

## Cluster-wide enable-encryption (multi-node)

A multi-node cluster (`RF > 1`) can migrate a previously-unencrypted barrier
cluster-wide with **no shared key, no global coordination beyond the intent
record, and rolling per-node recycling**. Each node independently recycles its own
replica while the Raft group stays available; zero data loss is guaranteed by the
logical snapshot boundary and per-node key derivation (see
[barrier-seal/architecture.md](../barrier-seal/architecture.md#sealing-in-a-cluster)).

### How cluster enable-encryption works

**Single command, system-wide fan-out:**

```bash
# System barrier (admin scope) — all nodes migrate
qrdb barrier enable-encryption --strategy passphrase --passphrase '…'

# Tenant's dedicated barrier only — all replica nodes of that tenant migrate
qrdb barrier enable-encryption --tenant acme --strategy passphrase --passphrase '…'
```

**Internally (Phase D architecture):**

1. **Coordinator (leader only):** Routes the request to a cluster coordinator if
   the scope has `RF > 1`. The leader resolves the owning nodes (system scope =
   all cluster members; tenant scope = the placement Replicas), writes a
   **replicated migration intent** record under `__sys:encmig/{tenant}/{db}` to
   the system KV, and returns `202 Accepted` immediately.

2. **Each node supplies its own credential (none in the record):** The intent
   record carries the target, the mechanism, the Shamir M/N, the owners and the
   generation — no credential, sealed or otherwise. The leader keeps the
   request's passphrase in protected memory for its own recycle; every other
   owner reads its own from `<seal.migration_credentials_dir>/system` or
   `<seal.migration_credentials_dir>/tenant-<id>`, and waits in phase
   `awaiting-credential` until the file is there. A Shamir target needs none:
   each node seals a fresh share set to its own officers. **Correction
   (2026-10-01):** earlier releases sealed the credential to each owner with
   HPKE and stored the envelopes in the record; that path is removed.

3. **Per-node reconciler (all nodes):** Each owning node detects the replicated
   intent (either immediately via change-feed or on reboot). It obtains its own
   credential, runs the **proven single-node recycle** on its local replica,
   and writes back a per-node status. The migration is complete when all owners
   ack the current `Generation` with a terminal-success phase.

4. **Rolling execution:** Nodes self-pace. For system-scope, each node's local
   system recycle is independent (no shared key). For tenant-scope, all replicas
   hold the same tenant barrier but encrypt independently, so mixed state (some
   replicas keyed, others not) is safe throughout — the Raft group never blocks
   on a laggard.

### Scope differences

| Aspect | System barrier | Tenant dedicated barrier (IsolationRaft) |
|---|---|---|
| **Unseal after migration** | Each node boots sealed; operator unseals each with the new passphrase (system-scope bootstrap unavoidable) | Tenant barrier reopens unsealed; no unseal step needed; rest of node serves throughout |
| **Data-plane impact** | System-scope recycles quiesce the entire node data plane briefly (in-process reboot of all Raft hosts) | Tenant-scope recycles only that tenant's local replicas; no system impact |
| **Cluster availability** | Can be orchestrated rolling (one node at a time); the system Raft group stays available throughout | All tenant replicas stay available; other tenants unaffected |
| **Single-node vs cluster** | Same endpoint + no `--tenant` flag covers both | Same endpoint + `--tenant` flag covers both |

### Failure modes and recovery

- **Node down during migration:** The replicated intent record lives in the system
  KV, so the node discovers it on reboot (or the next reconciler pass). It reads
  the replicated intent, checks that it hasn't acked the current `Generation`,
  reads its credential file, and resumes the recycle. A leader that restarts
  mid-migration no longer holds the request's passphrase: it, too, reads its
  credential file. The `Generation` guard
  ensures idempotency — a node cannot be re-cycled for the same generation twice.

- **Leader change:** The intent and per-node statuses live in replicated state.
  A new leader re-derives outstanding work from the replicated record and
  continues. The cluster never blocks on a laggard.

- **Mid-migration crash:** Each node's local recycle is crash-safe via the
  `MigrationJournal` (existing mechanism, deployed in single-node migrations).
  On restart, the journal roll-forwards or rolls back the replica deterministically,
  and the node re-publishes its phase to the replicated status. The journal
  records the migration's store directories and staged snapshot files, so
  recovery is scope-agnostic: the system barrier is reconciled at daemon boot
  (`RecoverInterruptedMigration` before the barrier build), and each per-tenant
  barrier is reconciled by the same recoverer, injected into the
  `BarrierRegistry` and run when the tenant barrier is re-registered — before
  the tenant's data stores open. A crash in the commit window (directory swaps
  done, root key not yet adopted) rolls forward from the staged root-key blob;
  an uncommitted crash rolls back to the untouched source stores.

### Observing completion

Poll the barrier status to observe per-node progress:

```bash
qrdb barrier status                    # system scope
qrdb barrier status --tenant acme      # tenant scope
```

The response includes the strategy (changes from `none` to `passphrase`/`shamir`
when all replicas are keyed) and, for tenant-scope, per-node migration phases.
Clients may also watch the change-feed for the replicated intent record if
available.

### Operator notes

- **Unseal required (system-scope).** After a system-scope cluster migration,
  each node boots sealed and must be unsealed with the new passphrase before it
  resumes serving. This is inherent to system-barrier bootstrap and is the same
  as initializing the barrier for the first time.

- **Online (tenant-scope).** Tenant-scope migrations are fully online — no unseal
  step, no maintenance window. The migration command returns 202, and the nodes
  self-reconcile in the background.

- **Pre-migration backup recommended.** As with single-node migrations, it is
  prudent to take a verified backup before initiating a cluster migration.

## Embedded-node tenant enable-encryption

An embedded node (via `sdk/go/node.NewStandaloneNode` or `NewClusteredNode`) can
enable encryption on tenant barriers programmatically:

```go
import (
    "github.com/jeremyhahn/go-qrdb/sdk/go/node"
    qrdbsdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// Enable encryption on a specific tenant's barrier. scope is passed by value
// (qrdbsdk.BarrierScope = service.BarrierScope); opts is qrdbsdk.MigrationOptions
// (= seal.MigrationOptions).
scope := qrdbsdk.BarrierScope{
    Kind:     qrdbsdk.ScopeTenant,
    TenantID: "acme",
}

err := n.EnableEncryption(ctx, scope, qrdbsdk.MigrationOptions{
    TargetStrategy: qrdbsdk.StrategyPassphrase,
    Mechanism:      qrdbsdk.MechanismSwap,
    Availability:   qrdbsdk.AvailabilityOnline,
    Credentials:    qrdbsdk.Credentials{Secret: []byte("tenant-secret")},
})
```

The embedded node must have been created with `node.WithTenantIsolation()` to
enable tenant-scoped encryption:

```go
n, err := node.NewStandaloneNode(ctx, cfg, nil,
    node.WithTenantIsolation(),
)
```

**Supported deployment paths:**

- **Standalone (LocalBackend):** System-scope and IsolationVolume tenant-scope
  enable-encryption are supported.
- **Clustered (real quicraft.Host):** System-scope, IsolationVolume tenant-scope,
  and IsolationRaft tenant-scope enable-encryption are all supported.

In both cases, the embedded node uses the same `scopedEncryptionMigrator` and
recycle logic as the daemon — there is no duplication of migration code between
embedded and daemon paths.

**Valid migration sources:** Both `StrategyNone` (plaintext) and `StrategySystemBarrier`
(auto-unseal dedicated tenants) are credential-free sources eligible for
enable-encryption. User-credential strategies (`passphrase`, `shamir`) are not
valid sources. Tenants on the **shared system barrier** (not a dedicated barrier)
are rejected with `ErrSharedBarrierImmutable`.

---

## Backups and how encrypted data is "decrypted"

qrdb snapshots are produced by the host's `ExportSnapshot`/`ImportSnapshot`
path (`pkg/service/shard_snapshot_provider.go`) and **include the epoch/barrier
metadata** needed for at-rest-encrypted shards. The backup carries the
encrypted shard plus the metadata required to interpret it — but **not** the
key.

The mental model: **the backup is a vault; the key lives in the recovery
quorum, not in the backup.**

To read an encrypted backup you do **not** decrypt the file standalone (qrdb
deliberately ships no key-extraction primitive). Instead you:

1. **Restore the backup into a qrdb instance whose barrier can decrypt it**, and
2. read through qrdb, which decrypts transparently while unsealed.

For disaster recovery to a *fresh* cluster, the barrier key must be recoverable
there. That is the job of the **Shamir recovery-officer quorum**
(`pkg/seal/recovery.go` + recovery-domain share holders) — the supported "get my
data back" path — or, for cluster expansion, the node-to-node key transport
(`pkg/keydelivery` + `pkg/clusterkeys`). See
[shamir-custody](../shamir-custody/) for share holders and the recovery domain.

## Why "disable encryption" is not a toggle

Making encryption easy to turn off is an anti-pattern:

- **Compliance.** PCI-DSS v4, HIPAA (45 CFR 164.312(a)(2)(i)), and FedRAMP treat
  at-rest encryption as a *required* control that extends to backups. A one-click
  "disable encryption" lets an operator silently drop a mandated control and
  produce plaintext backups of regulated data.
- **Industry norm.** Enterprise TDE (SQL Server, Oracle, Postgres TDE) does not
  expose disable as a casual switch; where it exists it triggers a background
  re-encrypt-to-plaintext and is flagged as a downgrade event.
- **It solves a non-problem.** Data is already always readable while unsealed,
  and the genuine lifecycle needs — rotate the credential, recover the key,
  restore a backup — are covered by rekey and the Shamir recovery officers.

If a true downgrade is ever required (e.g. migrating off qrdb), the compliant
shape is a deliberate, audited, **empty-barrier-only** disable (refused if any
data exists) plus an explicit background re-encrypt for the data case — never a
Seal Manager toggle. This is not implemented; request it explicitly if needed.

## See Also

- [README.md](README.md) — Seal subsystem overview
- [compliance.md](compliance.md) — FIPS/PCI/HIPAA/FedRAMP capability matrix
  (see the `StrategyNone` row)
- [shamir-custody](../shamir-custody/) — share holders, the recovery domain and the
  authenticated quorum
- [backup/README.md](../backup-restore/architecture.md) — Backup & restore
- [../cli/barrier.md](../barrier-seal/cli.md) — `qrdb barrier` CLI (init, unseal,
  rekey, status)
