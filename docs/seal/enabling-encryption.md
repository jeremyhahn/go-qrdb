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
  `shamir`) and is **fixed thereafter**. There is no in-place "flip a switch"
  to change strategy on a barrier that already holds data.
- The routine key-lifecycle operation is **rekey** (rotate the unseal
  credential / re-wrap the root key), *not* disabling encryption.
- To **enable encryption on an existing `none` deployment**, provision a new
  instance with a `passphrase` or `shamir` barrier and migrate via
  **backup & restore**. This is a deliberate, audited migration — by design.
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
| **Disable / change strategy in place** | — | — | **Not supported** |

Rekey is the routine, compliant rotation operation (PCI-DSS 3.6.1). It changes
*how you unseal*, never *whether data is encrypted*.

## Enabling encryption on an existing `none` deployment

Because the strategy is fixed at initialization, enabling encryption on a
deployment that was initialized with `none` (or with the seal subsystem disabled
entirely) is a **planned data migration**, not a runtime toggle:

1. Provision a **new** qrdb instance (or cluster) configured with a
   `passphrase` or `shamir` barrier and complete its initialization.
2. Take a backup of the unencrypted source instance (see
   [backup/README.md](../backup/README.md)).
3. Restore that backup **into** the new, keyed instance. As the data lands, the
   destination barrier encrypts it at rest transparently.
4. Cut traffic over to the encrypted instance and retire the old one
   (securely wiping its disks).

The GUI surfaces this in the Seal Manager: when no barrier is configured it
shows an **"Encryption Not Configured"** panel with this migration guidance
rather than a button that would silently re-encrypt (or fail against) a live
store. For a barrier subsystem that is present but **never initialized**, the
panel instead offers an in-place **Configure Encryption** action (a first-time
`Initialize`, which is safe because there is no committed strategy or data yet).

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
