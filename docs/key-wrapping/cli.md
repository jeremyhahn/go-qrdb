# Key Wrapping — CLI

> **Status: NORMATIVE; lands incrementally.** Scope: go-qrdb · Last verified: 2026-10-04

Key wrapping has no command group of its own — it is reached entirely
through `qrdb backup create` / `qrdb backup restore`'s existing flags. This
document covers only the flags this feature adds or changes; every other
`qrdb backup` flag is unchanged and documented in
[backup-restore/cli.md](../backup-restore/cli.md).

## `qrdbd` — the key directory config

The built-in `"software"` provider resolves every key reference inside ONE
directory the DAEMON (not the CLI) is configured with — `--backup-key-dir`
(`qrdbd start --backup-key-dir <dir>`), kept OUTSIDE `--data-dir` and
`--backup-dir` on purpose, same as every other key-custody directory in this
repository: a backup made with a key from this directory must still be
restorable after `--data-dir` is lost entirely. Resolving `"software"` with
no directory configured refuses with a typed, server-side error
(`*BackupKeyDirNotConfiguredError`) rather than falling back to an implicit
location. `--restore-decryption-key-file` (below) resolves against this
same directory during an offline, boot-time disaster-recovery restore.
`--restore-backup-id`'s offline restore mirrors `qrdb backup restore`'s
own `--allow-plaintext` opt-in as `--allow-plaintext-restore`: a
plaintext-stored backup is refused (`*BackupPlaintextRestoreNotAllowedError`)
unless the operator sets it, and the gate/settle audit record brackets the
WHOLE offline sequence (decrypt, bootstrap, stop, import, restart), not
merely the decrypt step.

## `qrdb backup create` — new and changed flags

**`--encryption-key` is retired** (owner ruling — see
[architecture.md](architecture.md#owner-ruling-the-inline-key-is-retired)):
there is no flag that takes key bytes directly any more.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--encryption-key-file` | string | — | the NAME of a key file inside the server's `--backup-key-dir`, resolved on the SERVER's own node — never a path |
| `--key-provider` | string | `software` when a key reference is supplied | the registered `keywrap.Provider` ID to wrap this backup's DEK with |

`--encryption-key-file` names a bare file inside the server's configured
key directory, not a path on the node running the backup service and not
the CLI operator's own machine — the CLI never opens it, it only forwards
the string, and the server refuses a value containing a path separator or
`".."` with a detail-free error (`*KeyAccessError`) rather than treating it
as a filesystem path (go-qrdb security review, backup-encrypt-default:
an unconstrained path let a caller probe the server's filesystem and point
the read at an arbitrarily large file). This is also why there is no `-`
(stdin) form: an earlier draft of this feature read a key's bytes from
stdin and sent them inline, which is exactly the path the owner ruling
retired.

```bash
qrdbd start --backup-key-dir /etc/qrdb/keys ...

qrdb backup create --destination /backups/daily \
     --encryption-key-file backup.key

# A non-default provider (go-xkms-registered hardware or cloud KMS):
qrdb backup create --destination /backups/daily \
     --key-provider aws-kms --encryption-key-file arn:aws:kms:...
```

**`--encryption-key-file` is the only way.** The key file's contents never
appear in a flag value, an environment variable, or a process listing on
either the CLI operator's machine or the wire; only its NAME does, and that
name never leaves the server's configured key directory. See
[architecture.md's Key hygiene section](architecture.md#key-hygiene-the-key-never-rides-in-a-flag-value).

## `qrdb backup restore` — new flags

**`--decryption-key` is retired** alongside `--encryption-key`.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--decryption-key-file` | string | — | the NAME of a key file inside the server's `--backup-key-dir` that overrides the key reference stored in the backup's own envelope (e.g. the key file moved, or restore runs on a different node); left unset, the stored reference is used |
| `--allow-plaintext` | bool | `false` | explicit opt-in required to restore a backup whose OWN stored state is plaintext; refused with a typed error (`BackupPlaintextRestoreNotAllowedError`) when unset, never inferred from the backup's own metadata (round 4 security review -- see [architecture.md](architecture.md#envelope-format)) |

```bash
# The common case: no key flag at all -- the envelope's own stored
# reference resolves automatically.
qrdb backup restore --id bkp-20261004-0300 --validate

# Only needed when the key file has moved, or restore runs elsewhere.
qrdb backup restore --id bkp-20261004-0300 \
     --decryption-key-file backup.key --validate
```

A restore naming the wrong key file, or a key file for a different
provider's key, is refused with `*BackupDecryptionFailedError` — reported
on the CLI as a plain refusal, not a stack trace, same as every other typed
service error.

## `qrdb backup get` / `qrdb backup list` — new field

Both now show `KeyProviderID` alongside the existing fields (empty for a
`none` backup):

```bash
$ qrdb backup get bkp-20261004-0300
ID:           bkp-20261004-0300
...
Encryption:   aes256
KeyProvider:  software
Checksum:     ...
```

## What this feature deliberately does not do here

- **No standalone `qrdb keywrap` command group.** There is nothing to list,
  create, or rotate at the CLI layer today — a provider is composed in
  code (`WithKeyWrapRegistry`), not configured interactively. If go-xkms's
  hardware providers eventually need operator-facing enrollment (pairing
  with a TPM, say), that command lives in go-xkms's own CLI extension, not
  here.
- **No key rotation flag on `backup create`/`restore`.** Rotating the KEK is
  an operator file-system operation BETWEEN backups -- add a new key file
  (a new NAME) inside `--backup-key-dir` and start naming it in fresh
  `--encryption-key-file` values; the old file and name stay in place so
  backups already made under it keep restoring (see architecture.md's
  rotation note). This CLI has no "re-wrap an existing backup's DEK under a
  new key" verb and no automated rotation scheduler.
