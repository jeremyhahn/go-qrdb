# Authentication Factors and PINs — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Command group: `qrdb auth`, with a `factor` subgroup implementing the N-factor
model (one User → many AuthFactors). All verbs accept the root persistent flags
documented in [../key-value/cli.md](../key-value/cli.md#global-flags).

> **The qrdb CLI does not perform hardware verification.** It resolves a
> `(method, lookup key)` tuple to a user. For end-to-end hardware login use
> `xkmsctl auth login` or `platformctl auth login`.

## `qrdb auth login`

Authenticate the active session using one of the enrolled factors.

| Flag | Type | Meaning |
|---|---|---|
| `--method` | string | **required** — `mtls \| fido2 \| piv` |
| `--cert` | string | certificate PEM path (`--method mtls`) |
| `--credential-hex` | string | credential bytes as hex (`--method fido2\|piv`) |

The lookup key per method:

| Method | Key |
|---|---|
| `mtls` | SHA-256 of the certificate DER — supplied as `--cert <path>` |
| `fido2` | WebAuthn `credentialID` bytes — `--credential-hex` |
| `piv` | SHA-256 of the slot certificate DER — `--credential-hex` |

```bash
qrdb auth login --method mtls --cert ./alice/cert.pem
qrdb auth login --method fido2 --credential-hex a1b2c3...
```

## `qrdb auth factor add`

Enroll a new factor.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--type` | string | — | **required** — `mtls \| fido2 \| piv` |
| `--label` | string | — | **required** — human-readable label |
| `--user` | string | — | **required for add** — numeric user id |
| `--cert` | string | — | certificate PEM path (`--type mtls`) |
| `--credential-hex` | string | — | credential bytes as hex (`--type fido2\|piv`) |
| `--device` | string | — | FIDO2 device id metadata (`--type fido2`) |
| `--reader` | string | — | PC/SC reader name metadata (`--type piv`) |
| `--slot` | string | `9a` | PIV slot id metadata (`--type piv`) |
| `--created-by` | string | `self` | provenance: `self \| admin \| ceremony` |

```bash
qrdb auth factor add --user 42 --type mtls --label "laptop" --cert ./alice/cert.pem

qrdb auth factor add --user 42 --type fido2 --label "yubikey-5" \
     --credential-hex a1b2c3... --device "YubiKey 5 NFC"

qrdb auth factor add --user 42 --type piv --label "badge" \
     --credential-hex d4e5f6... --reader "Generic Reader 00 00" --slot 9a
```

## `qrdb auth factor list`

| Flag | Type | Required |
|---|---|---|
| `--user` | string | yes — numeric user id |

```bash
qrdb auth factor list --user 42 --output json
```

## `qrdb auth factor rotate <factor-id>`

Rotate an enrolled factor's key material. Takes the same flags as `add`;
`--user` is **inherited** rather than required.

```bash
qrdb auth factor rotate 7 --type mtls --label "laptop" --cert ./alice/new-cert.pem
```

## `qrdb auth factor relabel <factor-id>`

| Flag | Type | Required |
|---|---|---|
| `--label` | string | yes |

## `qrdb auth factor remove <factor-id>`

| Flag | Type | Meaning |
|---|---|---|
| `--force` | bool | allow removing the **last** factor (admin) |

```bash
qrdb auth factor remove 7
qrdb auth factor remove 7 --force     # only when you mean it
```

Removing a user's last factor is refused without `--force`, because it locks
them out.

## PINs

PINs have **no `qrdb` verb group of their own.** They are set during the
ceremony and the barrier lifecycle:

```bash
qrdb init --so-pin SOPIN --user-pin USERPIN
qrdb barrier init --barrier-strategy passphrase --passphrase '<pp>' \
                  --so-pin SO123 --user-pin USR123
qrdb barrier seal --so-pin SO123 -y
qrdb barrier rekey --so-pin SO123
qrdb barrier recovery generate --threshold 3 --total 5 --so-pin SO123
```

**Dual control**: `--so-pin` requires `--user-pin` and vice versa at
initialization. Any PIN is refused with `--barrier-strategy=none`.

Minimum PIN length is **6 characters**, enforced server side before any backend
sees it.

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite.
