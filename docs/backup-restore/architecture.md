# Backup and Restore — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-03

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
- **A silently-minted encryption key.** Rejected: a caller that asks for the
  default (leaves `Encryption` unset) and supplies no key is refused outright
  (`BackupEncryptionRequiredError`), naming both ways out -- supply a key (or
  a KMS key reference, where the deployment provides one), or choose `none`
  explicitly. This package never invents key material on a caller's behalf.
- **Plaintext as the default.** Rejected: `aes256` is the default encryption
  algorithm on every backup surface (all transports, both SDKs, the CLI, the
  desktop and web UI). A caller that wants plaintext must set `Encryption` to
  `none` outright; that choice is logged at WARN and, where a compliance audit
  logger is configured, recorded in the audit trail. Restoring an encrypted
  backup without a key is refused the same way it always was. There is no path
  that silently writes or reads plaintext.
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
ordinary records in the system shard's key-value space (`__sys:cluster_ca/`,
`__sys:cert_ca/{tenantID}/` — see
[../certificate-authority/architecture.md](../certificate-authority/architecture.md#the-ca-key-provider-seam-pkgcakey)),
not a special case: a shard snapshot that includes the system shard captures
them exactly as it captures any other `__sys:` record, and restoring that
snapshot onto a new node or cluster restores both CAs along with everything
else. There is no separate CA-key export/import path, and none is needed.
This holds only for qrdb's built-in `cakey.SoftwareProvider`; a CA key held
by an injected HSM/TPM/KMS provider lives on that device and is out of scope
for a qrdb backup by construction — qrdb never held it to begin with.

**Key A and Key B are protected the SAME way now, and a backup inherits it
either way.** Both are stored as generated or adopted, with no additional
AEAD wrap of their own layered on top by `pkg/cakey` — an earlier design
wrapped each with the writing node's own `seal.EncryptionBarrier` first,
which made the replicated record undecryptable on every OTHER node
(barriers are per-node, not shared cluster-wide) and is not how either key
is protected now (the owner's CA-at-rest ruling). What protects both
instead: this node's own at-rest storage encryption, when the deployment
configures one (`pkg/seal/encfs`, transparent below the `kvstore.KVStore`
this package writes through) protects the on-disk copy; cluster mTLS
protects it while Raft replicates the write; and under seal strategy
`none`, both keys are unencrypted at rest in the system shard, exactly like
every other record in that mode. **A raw snapshot of the system shard taken
under `none`, or taken by reading the underlying storage engine directly
rather than through a sealed `encfs`-protected mount, is Key A AND Key B in
the clear.** Encrypt such a backup in transit and at rest by the same
policy as any other sensitive export (see Trust boundaries below); this is
not a gap specific to either key so much as a reminder that neither is MORE
protected than the rest of the system shard, the way an earlier per-record
wrap made it look.

## Trust boundaries

- **`backup:*` is full-data trust, not a narrower "backup data" scope.** A
  shard snapshot that includes the system shard captures both CA keys in the
  clear-or-not state they were stored in (see [CA key
  material](#ca-key-material) above): the cluster CA (Key A) and every
  tenant's login CA (Key B). Whoever can `backup:create` or `backup:read` a
  backup containing the system shard can extract both CAs' private key
  material from it — there is no way to grant backup access to a shard's
  ordinary data without also granting it to whatever CA material that shard
  holds. **These permissions belong only to fully trusted operators**, not to
  a role scoped to "day-to-day database administration": granting
  `backup:create`/`backup:read` is operationally equivalent to granting
  direct access to the cluster's and every tenant's signing keys. This is a
  property of what a shard snapshot IS, not a gap this feature's RBAC design
  closes — see `clusterBackupPermissions`'s own doc comment in
  `pkg/permission` for why these are `ScopeCross` (system tenant only) and
  not `ScopeSelf`, and note that `ScopeCross` is NOT the same promise as
  "Admin-only": a system-tenant role explicitly granted only
  `backup:create`/`backup:read`, without the Admin role, can still extract a
  full snapshot by this same reasoning.
- **A backup leaves the process and usually the machine; it is encrypted by
  default.** `aes256` is the default (see "Alternatives rejected" above):
  creating a backup with no `--encryption` flag and a key configured
  encrypts every captured record, CA key material included. An operator who
  explicitly passes `--encryption none` (or the API/SDK equivalent) IS
  writing an unencrypted backup, and if that backup's source shard held Key
  A or Key B under seal strategy `none`, the backup holds them in the clear.
  Treat encryption at rest and in transit for every backup containing the
  system shard as mandatory operational policy, not an optional hardening
  step: an unencrypted backup of a barrier-encrypted store is plaintext
  outside the barrier, and the CA keys it also carries have no barrier of
  their own to have been outside of in the first place (see [CA key
  material](#ca-key-material)). That is why encryption is the default
  rather than an opt-in: a caller gets plaintext only by choosing it
  explicitly (`Encryption: none`), never by omission, and that choice is
  visible -- logged at WARN and, where configured, audited.
- **Restore is the most destructive operation in the system** — it overwrites
  shard state. `backup:restore` is a distinct permission from
  `backup:create`/`backup:read` for exactly that reason, and `backup:delete` is
  separate again. For the system shard specifically, that overwrite includes
  `__sys:cluster_ca/` (Key A): restoring an OLDER backup replaces the live
  cluster CA with whatever key and certificate that backup captured, and
  restoring one shard-snapshotted from a DIFFERENT cluster entirely replaces
  it outright — `backup:restore` is the authority to roll Key A back or swap
  it for another cluster's, not only to undo an ordinary data mistake.
- **Holding Key A is holding the cluster's signing identity, not merely
  reading a secret.** Every certificate `pkg/clusterca` issues — every
  node's own TLS identity and every joiner admitted through member-side CSR
  join — chains to Key A, and every peer validates the chain against it, not
  against any one node. A party that extracts Key A from a backup (`backup:create`/
  `backup:read`, above) can mint a certificate for ANY node identity the
  cluster will accept and use it to impersonate that node to any client or
  peer that trusts the cluster CA — a reach that crosses every
  barrier-isolated tenant on the cluster, because mTLS trust in Key A is
  cluster-wide, not scoped to one tenant's barrier. This is strictly worse
  than reading the tenant data the same backup carries: tenant data is
  confined by that tenant's barrier (or by `none`'s absence of one, which is
  at least confined to what that tenant stored), while Key A's reach is the
  whole cluster's trust relationship, regardless of any single tenant's own
  encryption posture.
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
