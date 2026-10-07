# Key Wrapping — API

> **Status: NORMATIVE; lands incrementally.** Scope: go-qrdb · Last verified: 2026-10-06

Key wrapping adds no new RPC surface of its own. It changes the request and
response shapes of `POST /v1/backups` and `POST /v1/backups/restore`
(and their gRPC/QUIC/MCP/embedded equivalents), documented in full in
[backup-restore/api.md](../backup-restore/api.md). This document covers only
the fields this feature adds or changes to that existing schema.

## `POST /v1/backups` — `BackupRequest`, new fields

```json
{ "destination": "s3://acme-backups/qrdb",
  "compression": "zstd",
  "encryption": "aes256",
  "key_provider_id": "software",
  "key_ref": "backup.key",
  "metadata": { "schedule": "nightly" },
  "shard_ids": [1, 2, 3] }
```

| Field | Type | Meaning |
|---|---|---|
| `key_provider_id` | string | the registered `keywrap.Provider` ID to wrap this backup's DEK with. Empty defaults to `"software"` when `key_ref` is set. |
| `key_ref` | string | the key reference the named provider resolves — for `"software"`, the bare NAME of a key file inside the server's configured key directory (`--backup-key-dir`), never a path. |

**`encryption_key` is retired** (owner ruling — see
[architecture.md](architecture.md#owner-ruling-the-inline-key-is-retired)):
the field, and the raw-bytes-over-the-wire path it carried, no longer exist
on this message. `key_ref` is required when `encryption` is `"aes256"` (or
defaulted to it); `key_ref` naming an unregistered `key_provider_id` is
refused before any shard is captured.

## Response — `BackupInfo`, new field

```json
{ "id": "bkp-20261004-0300", "timestamp": "2026-10-04T03:00:00Z",
  "size": 4823945216, "shard_count": 3, "status": "complete",
  "checksum": "sha256:…", "destination": "s3://acme-backups/qrdb",
  "compression": "zstd", "encryption": "aes256",
  "key_provider_id": "software",
  "metadata": { "schedule": "nightly" },
  "duration": 184000000000, "error_message": "" }
```

`key_provider_id` is empty for a `"none"` backup. Nothing about the wrapped
DEK, the envelope's nonce, or the format version is surfaced here — see
[architecture.md](architecture.md#backup-integration) for why.

## `POST /v1/backups/restore` — `RestoreRequest`, new field

```json
{ "backup_id": "bkp-20261004-0300",
  "target_shards": [1, 2],
  "validate_first": true,
  "decryption_key_ref": "backup.key" }
```

| Field | Type | Meaning |
|---|---|---|
| `decryption_key_ref` | string | overrides the key reference stored in the backup's own envelope (e.g. the key file moved, or restore runs on a different node). Left empty, the stored reference is used — restore needs no key material supplied at all in the common case. Mirrors `key_ref` on create. |

**`decryption_key` is retired** alongside `encryption_key` — see
[architecture.md](architecture.md#owner-ruling-the-inline-key-is-retired).

The provider actually used to `Unwrap` is whichever `key_provider_id` the
backup itself was created under — a restore request does not choose a
different provider than the one that wrapped the DEK; `decryption_key_ref`
only supplies that provider's own reference (a bare file NAME inside the
server's configured key directory, for `"software"`).

## Capability matrix

All six transports carry the same request/response shapes, unchanged from
the matrix in [backup-restore/api.md](../backup-restore/api.md#capability-matrix)
— key wrapping adds fields to existing messages, not new verbs.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| `key_provider_id` names an unregistered provider (create or restore) | 400 | `INVALID_ARGUMENT` — `*BackupDecryptionFailedError` |
| Wrong key file on restore (an explicit `decryption_key_ref` override, or the stored reference, that does not match) | 400 | `INVALID_ARGUMENT` — `*BackupDecryptionFailedError` |
| Tampered shard ciphertext on restore — aborts the WHOLE restore, before any shard is imported | 400 | `INVALID_ARGUMENT` — `*BackupVerificationFailedError` (wrapping `*BackupShardIntegrityError`) |
| Tampered manifest on restore (the sealed header, not a shard) — aborts before any shard is touched | 400 | `INVALID_ARGUMENT` — `*BackupManifestInvalidError`, returned directly (not wrapped in `*BackupVerificationFailedError`: the manifest is authenticated once, up front, before PASS 1's per-shard verification loop even starts) |
| Stored metadata and the artifact disagree about encryption (a downgrade attempt) | 400 | `INVALID_ARGUMENT` — `*BackupMetadataMismatchError` |
| The built-in `"software"` key-wrap provider is resolved with no key directory configured (`--backup-key-dir` / `server.WithKeyDir` / `lifecycle.Infra.KeyDir`) — a server-side capability gap, never a caller-supplied-value problem | 501 | `UNIMPLEMENTED` — `*BackupKeyDirNotConfiguredError` |

These join, not replace, the existing mapping table in
[backup-restore/api.md](../backup-restore/api.md#status-and-error-mapping).
