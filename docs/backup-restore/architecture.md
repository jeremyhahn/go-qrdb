# Backup and Restore — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Problem

A logical undo of a few keys ([../key-value/](../key-value/architecture.md)'s
`kv restore`) is not disaster recovery. Recovering a shard, a node or a cluster
needs a point-in-time capture of shard state that can be moved off the machine,
verified independently, and restored onto different hardware.

## Shape chosen

**Shard snapshots with a manifest**: a backup captures one or more shards to a
destination, records a SHA-256 checksum with its metadata, and can be listed,
inspected, verified and restored — whole or shard by shard.

```
create   →  capture shard snapshots  →  compress  →  encrypt  →  destination
verify   →  recompute SHA-256        →  compare with the stored checksum
restore  →  (optionally verify)      →  decrypt   →  decompress → apply to shards
```

Selective at both ends: back up specific shards, restore specific shards.

### Alternatives rejected

- **Filesystem-level copy of the data directory.** Rejected: not consistent
  under load, not portable across topologies, and it captures nothing about
  which shards it holds.
- **Encryption always on.** Rejected as a default (`none` is the default), but
  the *conditional* rule is strict: enabling encryption without a key is
  refused, and restoring an encrypted backup without a key is refused. There is
  no path that silently writes or reads plaintext.
- **Verification folded into restore.** Rejected as mandatory: verification is a
  separate operation you can run on a schedule, and `ValidateFirst` makes it
  optional-but-available on restore. A backup you have never verified is a
  backup you do not have, and separating them is what makes routine
  verification possible.
- **Restore as an atomic whole-cluster operation.** Rejected: `RestoreResult`
  carries **per-shard errors**, so a partial restore is reported precisely
  rather than collapsing into one failure.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Backup service, manifest, checksums | `pkg/service` (backup) | go-qrdb |
| Shard snapshot capture | driven through `pkg/cluster` over **go-quicraft** | go-qrdb / go-quicraft |
| Destination storage (local paths, S3, GCS, Azure, MinIO, Glacier) | — | **go-objstore** |
| Compression and encryption | `pkg/crypto`, `pkg/codec` | go-qrdb |
| SDK surface | `sdk/go` (`transport.BackupClient`) | go-qrdb |

Destinations are URIs, so a backup can land on a local path
(`/backups/daily`) or an object store (`s3://bucket/prefix`) with the same call.
Cloud destinations require the corresponding go-objstore build tag.

## Run-mode dependency

**Shard backup and restore are raft-mode capabilities.** They operate on shard
snapshots, which exist only where there is a replicated log. In standalone mode
they refuse with `requires mode: raft` — see
[../run-modes/](../run-modes/architecture.md).

## CA key material

Both CA keys (Key A, the cluster CA; Key B, the per-tenant login CA) are
ordinary barrier-encrypted records in the system shard's key-value space
(`__sys:cluster_ca/`, `__sys:cert_ca/{tenantID}/` — see
[../certificate-authority/architecture.md](../certificate-authority/architecture.md#the-ca-key-provider-seam-pkgcakey)),
not a special case: a shard snapshot that includes the system shard captures
them exactly as it captures any other `__sys:` record, and restoring that
snapshot onto a new node or cluster restores both CAs along with everything
else. There is no separate CA-key export/import path, and none is needed.
This holds only for qrdb's built-in `cakey.SoftwareProvider`; a CA key held
by an injected HSM/TPM/KMS provider lives on that device and is out of scope
for a qrdb backup by construction — qrdb never held it to begin with.

## Trust boundaries

- **A backup leaves the process and usually the machine.** Encryption is
  therefore a data-classification decision, not a performance one: an
  unencrypted backup of a barrier-encrypted store is plaintext outside the
  barrier.
- **Restore is the most destructive operation in the system** — it overwrites
  shard state. `backup:restore` is a distinct permission from
  `backup:create`/`backup:read` for exactly that reason, and `backup:delete` is
  separate again.
- **The checksum is integrity, not authenticity.** SHA-256 detects corruption
  and truncation. Authenticated encryption (AES-256-GCM) is what detects
  tampering, and it is opt-in.

## What this feature deliberately does not do

- **No scheduling.** There is no built-in cron; a scheduler drives `create` from
  outside.
- **No retention policy.** Deleting old backups is `delete`, driven externally.
- **No incremental or differential backups.** Each backup is a full capture of
  the shards it names.
- **No logical (row-level) export.** For that, use SQL or
  `qrdb batch import`/`kv range`.
- **No cross-version guarantee beyond the shard snapshot format.** Restoring
  onto a materially different topology is not a supported migration path.
