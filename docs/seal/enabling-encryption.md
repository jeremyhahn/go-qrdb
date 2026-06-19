<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Enabling, Rotating & Disabling Encryption at Rest

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
| **Rekey** (`pkg/seal/rekey.go`) | Yes (re-wraps the root key; new Shamir params) | No | No |
| **Re-initialize** (uninitialized barrier only) | Sets it for the first time | n/a (no data yet) | Sets it for the first time |
| **Enable encryption** (`qrdb barrier enable-encryption`) | Sets it (the new keyed credential) | **Yes** (full at-rest re-encryption) | `none` → `passphrase`/`shamir` |
| **Disable / downgrade to plaintext in place** | — | — | **Not supported** (see below) |

Rekey is the routine, compliant rotation operation (PCI-DSS 3.6.1). It changes
*how you unseal*, never *whether data is encrypted*. Enable-encryption is the
deliberate, one-way migration that turns encryption *on*.

## Enabling encryption after the fact

A barrier initialized with `none` can be migrated to a keyed strategy in place,
re-encrypting all at-rest data, with **no process restart**:

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

**Scope is never global.** It is either the **system** barrier (admin authority,
`seal:enable_encryption`) or a single tenant's **dedicated** barrier (`--tenant`).
A tenant on the **shared system barrier** inherits the system settings and
cannot change its own encryption — the server rejects such a request. Take a
verified backup before migrating (see [backup/README.md](../backup/README.md)).

The GUI surfaces the same operation from the Seal Manager. When a `none`
barrier is configured it offers the migration; for a barrier subsystem present
but **never initialized**, it offers a first-time **Configure Encryption**
action (a plain `Initialize`, safe because there is no committed strategy or
data yet).

> **Transport support:** the operation is available over REST (`POST
> /v1/barrier/enable-encryption`) and gRPC / unix (the default CLI transport).
> The QUIC, MCP, and embedded transports return `NotImplemented`.

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
(`pkg/seal/recovery.go` + recovery Security Officers) — the supported "get my
data back" path — or, for cluster expansion, the node-to-node key transport
(`pkg/keydelivery` + `pkg/clusterkeys`). See
[custodians.md](custodians.md) for the share-delivery protocol.

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
- [custodians.md](custodians.md) — Custodian lifecycle, Shamir recovery quorum,
  share-delivery protocol
- [shamir.md](shamir.md) — Shamir M-of-N unsealing
- [backup/README.md](../backup/README.md) — Backup & restore
- [../cli/barrier.md](../cli/barrier.md) — `qrdb barrier` CLI (init, unseal,
  rekey, status)
