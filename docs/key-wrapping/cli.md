# Key Wrapping — CLI

> **Status: NORMATIVE; lands incrementally.** Scope: go-qrdb · Last verified: 2026-10-04

Key wrapping has no command group of its own — it is reached entirely
through `qrdb backup create` / `qrdb backup restore`'s existing flags. This
document covers only the flags this feature adds or changes; every other
`qrdb backup` flag is unchanged and documented in
[backup-restore/cli.md](../backup-restore/cli.md).

## `qrdb backup create` — new and changed flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--encryption-key-file` | string | — | path to a 32-byte key file for the `"software"` key-wrap provider; `-` reads the key from stdin instead |
| `--encryption-key` | string | — | **kept, with a warning**: the 32-byte key as a flag value directly (base64) |
| `--key-provider` | string | `software` when a key is supplied | the registered `keywrap.Provider` ID to wrap this backup's DEK with |

`--encryption-key-file` and `--encryption-key` are mutually exclusive;
supplying both is refused the same way `--encryption` and no key at all is
refused. `--key-provider` is meaningful only alongside `--encryption-key-file`
(or a future provider-specific reference flag go-xkms's own CLI extension
adds) — `--encryption-key`'s inline path always uses the built-in software
provider and ignores `--key-provider` if given.

```bash
# The documented way: a key file the operator controls the permissions of.
qrdb backup create --destination /backups/daily \
     --encryption-key-file /etc/qrdb/backup.key

# Equivalent, reading the key from stdin instead of a file on disk.
cat /etc/qrdb/backup.key | qrdb backup create --destination /backups/daily \
     --encryption-key-file -

# Kept, with a warning printed to stderr naming the exposure:
#   "Warning: --encryption-key is visible in `ps` and shell history for the
#   life of this process; prefer --encryption-key-file."
qrdb backup create --destination /backups/daily \
     --encryption-key "$(base64 -w0 /etc/qrdb/backup.key)"
```

**`--encryption-key-file` is the documented way.** The file's contents never
appear in a flag value, an environment variable, or a process listing; only
its path does. See
[architecture.md's Key hygiene section](architecture.md#key-hygiene-the-key-never-rides-in-a-flag-value).

## `qrdb backup restore` — new flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--decryption-key-file` | string | — | path to the SAME 32-byte key file used at create time; `-` for stdin |
| `--decryption-key` | string | — | **kept, with the same warning** as `--encryption-key` |

```bash
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
