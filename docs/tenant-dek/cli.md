# Tenant DEK Keyring — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-01

Command group: `qrdb dek` — the caller tenant's data encryption keyring. All
verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags), and `--output json` or
`--output yaml` prints the full response.

| Group flag | Type | Meaning |
|---|---|---|
| `--tenant` | string | The caller's own tenant ID. Optional; any other tenant is refused. |

Ciphertexts and associated data are base64 on the command line.

## `qrdb dek status`

Show the keyring's versions — never key material.

```bash
qrdb dek status
# tenant acme: AES-256-GCM, current v2
# VERSION  STATE    CREATED               RETIRED
# 1        retired  2026-10-01T12:00:00Z  2026-10-01T13:00:00Z
# 2        current  2026-10-01T12:30:00Z
```

## `qrdb dek rotate`

Add a key version and make it current. Earlier versions keep decrypting.

```bash
qrdb dek rotate
```

## `qrdb dek retire <version>`

Destroy an old version's key. The current version is refused. Rewrap every
ciphertext under the version first: retirement is irreversible.

```bash
qrdb dek retire 1
```

## `qrdb dek encrypt`

Encrypt under the current version; prints the base64 ciphertext.

| Flag | Type |
|---|---|
| `--plaintext` | string (required) |
| `--aad` | base64 |

```bash
AAD=$(printf 'acme/db/password/v3' | base64)
CT=$(qrdb dek encrypt --plaintext "$DB_PASSWORD" --aad "$AAD")
```

## `qrdb dek decrypt <ciphertext>`

Decrypt; prints the plaintext. `--aad` must equal the value given at
encryption.

```bash
qrdb dek decrypt "$CT" --aad "$AAD"
```

## `qrdb dek rewrap <ciphertext>`

Re-encrypt under the current version without revealing the plaintext; prints
the new ciphertext.

```bash
CT=$(qrdb dek rewrap "$CT" --aad "$AAD")
```
