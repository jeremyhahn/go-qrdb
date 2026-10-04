# Key Wrapping — CLI

> **Status: NORMATIVE; lands incrementally.** Scope: go-qrdb · Last verified: 2026-10-04

Key wrapping has no command group of its own — it is reached entirely
through `qrdb backup create` / `qrdb backup restore`'s existing flags. This
document covers only the flags this feature adds or changes; every other
`qrdb backup` flag is unchanged and documented in
[backup-restore/cli.md](../backup-restore/cli.md).

## `qrdb backup create` — new and changed flags

**`--encryption-key` is retired** (owner ruling — see
[architecture.md](architecture.md#owner-ruling-the-inline-key-is-retired)):
there is no flag that takes key bytes directly any more.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--encryption-key-file` | string | — | the key reference the key-wrap provider resolves on the SERVER's own node — a 32-byte key-file path for the `"software"` provider |
| `--key-provider` | string | `software` when a key reference is supplied | the registered `keywrap.Provider` ID to wrap this backup's DEK with |

`--encryption-key-file` names a path on the node running the backup
service, not the CLI operator's own machine — the CLI never opens it, it
only forwards the string. This is also why there is no `-` (stdin) form: an
earlier draft of this feature read a key's bytes from stdin and sent them
inline, which is exactly the path the owner ruling retired.

```bash
qrdb backup create --destination /backups/daily \
     --encryption-key-file /etc/qrdb/backup.key

# A non-default provider (go-xkms-registered hardware or cloud KMS):
qrdb backup create --destination /backups/daily \
     --key-provider aws-kms --encryption-key-file arn:aws:kms:...
```

**`--encryption-key-file` is the only way.** The key file's contents never
appear in a flag value, an environment variable, or a process listing on
either the CLI operator's machine or the wire; only its path does, and that
path never leaves the server. See
[architecture.md's Key hygiene section](architecture.md#key-hygiene-the-key-never-rides-in-a-flag-value).

## `qrdb backup restore` — new flags

**`--decryption-key` is retired** alongside `--encryption-key`.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--decryption-key-file` | string | — | overrides the key reference stored in the backup's own envelope (e.g. the key file moved, or restore runs on a different node); left unset, the stored reference is used |

```bash
# The common case: no key flag at all -- the envelope's own stored
# reference resolves automatically.
qrdb backup restore --id bkp-20261004-0300 --validate

# Only needed when the key file has moved, or restore runs elsewhere.
qrdb backup restore --id bkp-20261004-0300 \
     --decryption-key-file /etc/qrdb/backup.key --validate
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
- **No key rotation flag on `backup create`/`restore`.** Rotating the KEK a
  key file names is an operator file-system operation (replace the file's
  contents) between backups; this CLI has no "re-wrap an existing backup's
  DEK under a new key" verb, matching the architecture doc's own "no key
  rotation workflow" scope note.
