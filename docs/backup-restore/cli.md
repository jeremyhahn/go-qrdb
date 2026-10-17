# Backup and Restore — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first.

Command group: `qrdb backup` — create, list, inspect, verify, delete and restore
backups. All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

**Requires raft mode.** Shard backup and restore operate on shard snapshots; in
standalone mode they refuse with `requires mode: raft`.

## `qrdb backup create`

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--destination` | string | — | storage path or URI, e.g. `/backups/daily` or `s3://bucket/prefix` |
| `--compression` | string | `none` | `none \| zstd \| snappy` |
| `--encryption` | string | *(unset)* | `aes256 \| none` — unset defaults to `aes256` server-side |
| `--encryption-key-file` | string | — | **required unless `--encryption` is explicitly `none`**: the key reference the key-wrap provider resolves — see [../key-wrapping/cli.md](../key-wrapping/cli.md) |
| `--key-provider` | string | `software` | the registered `keywrap.Provider` to wrap this backup's DEK with — see [../key-wrapping/cli.md](../key-wrapping/cli.md) |

**BREAKING (since the encrypt-by-default change): `--encryption` left unset no
longer means plaintext.** The server defaults an unset `--encryption` to
`aes256` and then requires `--encryption-key-file`. A script that relied on
the old plaintext default must now pass `--encryption-key-file`, or pass
`--encryption none` to keep writing plaintext.

```bash
qrdb backup create --destination /backups/daily --encryption-key-file backup.key

qrdb backup create --destination s3://acme-backups/qrdb \
     --compression zstd --encryption-key-file backup.key

# Explicit plaintext override -- the CLI warns, the server logs and audits it.
qrdb backup create --destination /backups/scratch --encryption none
```

Creating with the default (or an explicit `aes256`) and no key reference is
refused. Backing up specific shards is an API-level option (`shard_ids`) and
has no CLI flag; the CLI captures all shards.

The envelope mechanics — what the key reference names, how the DEK is
wrapped, hardware/cloud-KMS providers — are [../key-wrapping/](../key-wrapping/architecture.md)'s
own scope; this document covers only that a key reference is required.

The response carries the backup `id` and its `checksum` — record both.

## `qrdb backup list`

List available backups, **newest first**. No flags.

```bash
qrdb backup list --output json
```

## `qrdb backup get <id>`

Retrieve one backup's details: size, shard count, status, checksum,
destination, compression, encryption, key provider, duration, and any error
message. No flags.

```bash
qrdb backup get bkp-20260819-0300 --output json
```

## `qrdb backup verify <id>`

Verify integrity by recomputing the SHA-256 checksum and comparing it to the
stored value. No flags.

```bash
qrdb backup verify bkp-20260819-0300 --output json
```

The result reports both the expected and the actual checksum, so a mismatch is
diagnosable rather than a bare failure.

## `qrdb backup restore`

Restore shard data from a backup. **This overwrites shard state.**

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--id` | string | — | backup ID to restore from |
| `--shards` | string | all shards | comma-separated shard IDs to restore |
| `--validate` | bool | false | run integrity verification **before** restoring |
| `--decryption-key-file` | string | — | overrides the key reference stored in the backup's own envelope; usually left unset — see [../key-wrapping/cli.md](../key-wrapping/cli.md) |

```bash
# Verify then restore everything -- no key flag needed, even for an
# encrypted backup: the envelope's own stored key reference resolves it.
qrdb backup restore --id bkp-20260819-0300 --validate

# Restore two shards, overriding the key reference (the key file moved).
qrdb backup restore --id bkp-20260819-0300 --shards 1,2 \
     --decryption-key-file backup.key --validate
```

With `--validate`, a checksum mismatch aborts before any shard is touched.
A wrong key reference (stored or overridden), a tampered shard or manifest,
or a stored-metadata/artifact mismatch all abort the WHOLE restore with one
typed error, before any shard is imported: every target shard is
authenticated and decrypted first, and a failure anywhere imports nothing.

The result reports how many shards were restored and lists **per-shard
errors** only for an OPERATIONAL import failure after every shard has
already passed verification (the underlying store refused a particular
shard) — a narrower case than a cryptographic one, named precisely rather
than collapsed into the top-level refusal above.

## `qrdb backup delete <id>`

Delete a backup by ID. No flags.

```bash
qrdb backup delete bkp-20260812-0300
```

## Permissions

Each verb carries its own grant: `backup:create`, `backup:list`, `backup:read`,
`backup:verify`, `backup:delete`, `backup:restore`. **Restore is separate
because it overwrites shard state** — it is not implied by any of the others.

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite. Note
that the end-to-end backup/restore integration test lives in the **cluster
stage** (`make integration-test-cluster`). CI runs it as the `backup-restore`
leg of the `integration-features` job (`.github/workflows/ci.yml`), required by
`ci-gate`.
