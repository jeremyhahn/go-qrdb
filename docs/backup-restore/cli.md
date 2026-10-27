# Backup and Restore — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

Command group: `qrdb backup` — create, list, get, verify, delete and restore
backups. All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

**Requires raft mode.** Shard backup and restore operate on shard snapshots; in
standalone mode they refuse with `requires mode: raft`.

## `qrdb backup create`

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--destination` | string | — (required) | the backup's destination label, recorded with it and bound into its authenticated data, e.g. `/backups/daily`; the bytes go to the server's backup sink (`qrdbd start --backup-dir`) |
| `--compression` | string | `none` | `none \| zstd \| snappy`; validated and recorded in the backup's metadata, not applied to shard data |
| `--encryption` | string | *(unset)* | `aes256 \| none` — unset defaults to `aes256` server-side |
| `--encryption-key-file` | string | — | **required unless `--encryption` is explicitly `none`**: the bare name of a key file inside the server's configured key directory, never a path — see [../key-wrapping/cli.md](../key-wrapping/cli.md) |
| `--key-provider` | string | *(unset: `software`)* | the registered `keywrap.Provider` to wrap this backup's DEK with — see [../key-wrapping/cli.md](../key-wrapping/cli.md) |

An unset `--encryption` is `aes256`, which requires `--encryption-key-file`.
Plaintext is written only with an explicit `--encryption none`.

```bash
qrdb backup create --destination /backups/daily --encryption-key-file backup.key

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
qrdb backup get 3f8e2a1c-7b4d-4e9a-9c61-2d5f0b8a7e43 --output json
```

## `qrdb backup verify <id>`

Verify integrity by recomputing the SHA-256 checksum and comparing it to the
stored value. No flags.

```bash
qrdb backup verify 3f8e2a1c-7b4d-4e9a-9c61-2d5f0b8a7e43 --output json
```

The result reports both the expected and the actual checksum, so a mismatch is
diagnosable rather than a bare failure.

## `qrdb backup restore`

Restore shard data from a backup. **This overwrites shard state.**

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--id` | string | — (required) | backup ID to restore from |
| `--shards` | string | all shards | comma-separated shard IDs to restore |
| `--validate` | bool | false | run integrity verification **before** restoring |
| `--decryption-key-file` | string | — | overrides the key reference stored in the backup's own envelope; usually left unset — see [../key-wrapping/cli.md](../key-wrapping/cli.md) |
| `--allow-plaintext` | bool | false | explicit opt-in to restore a backup whose stored state is plaintext; without it such a restore is refused with a typed error |

```bash
# Verify then restore everything -- no key flag needed, even for an
# encrypted backup: the envelope's own stored key reference resolves it.
qrdb backup restore --id 3f8e2a1c-7b4d-4e9a-9c61-2d5f0b8a7e43 --validate

# Restore two shards, overriding the key reference (the key file moved).
qrdb backup restore --id 3f8e2a1c-7b4d-4e9a-9c61-2d5f0b8a7e43 --shards 1,2 \
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
qrdb backup delete a91d04e6-52c3-4f0b-8e7a-6b3c9f1d2e85
```

## Permissions

Each verb carries its own grant: `backup:create`, `backup:list`, `backup:read`,
`backup:verify`, `backup:delete`, `backup:restore`. **Restore is separate
because it overwrites shard state** — it is not implied by any of the others.

## Integration tests

`make integration-test-backup-restore` runs the self-spawned CLI suites
(`test/integration/backup_cli_test.go`, `test/integration/backup_restore_test.go`);
CI runs them in the `integration-features` shard whose feature list includes
`backup-restore` (`.github/workflows/ci.yml`), required by `ci-gate`. The Docker suite's
`test/integration/api/cli_backup_test.go` runs under
`make integration-test-standalone`.
