# Authentication Factors and PINs — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

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
| `--if-no-active-type` | bool | `false` | add only if the user holds no active factor of `--type` |
| `--expected-version` | uint | — | add only if the user's factor set is still at this version, as `list` reports it |

`--if-no-active-type` and `--expected-version` make the add **conditional**,
and may be given together. The server decides the condition in the same write
that adds the factor, so of several commands racing the same claim exactly one
succeeds. A refused add exits non-zero with an error naming the active factor that
already holds the type (`--if-no-active-type`) or the current and expected
versions (`--expected-version`, checked first); nothing is enrolled. `--expected-version 0` means "the user's factors
have never changed".

A conditional add reports the version it committed: `Factor Set:    version N`
in table output, `factor_set_version` in JSON (inline beside the factor
record's own fields). An unconditional add prints the factor record alone.

```bash
qrdb auth factor add --user 42 --type mtls --label "laptop" --cert ./alice/cert.pem

qrdb auth factor add --user 42 --type fido2 --label "yubikey-5" \
     --credential-hex a1b2c3... --device "YubiKey 5 NFC"

qrdb auth factor add --user 42 --type piv --label "badge" \
     --credential-hex d4e5f6... --reader "Generic Reader 00 00" --slot 9a

# One-time claim: enrol the user's first PIV card, or nothing.
qrdb auth factor add --user 42 --type piv --label "badge" \
     --credential-hex d4e5f6... --if-no-active-type

# Add only if nothing changed since the list you read.
qrdb auth factor add --user 42 --type fido2 --label "spare" \
     --credential-hex a1b2c3... --expected-version 3
```

## `qrdb auth factor list`

| Flag | Type | Required |
|---|---|---|
| `--user` | string | yes — numeric user id |

Table output ends with `Factor set version: N`; JSON carries it as
`factor_set_version`. That is the value `add --expected-version` compares
against. It is read before the factors, so it is never newer than the list
printed with it.

```bash
qrdb auth factor list --user 42 --output json
```

## `qrdb auth factor rotate <factor-id>`

Rotate an enrolled factor's key material. Takes the same flags as `add`
except the two conditions — `--if-no-active-type` and `--expected-version` are
refused as unknown flags; `--user` is **inherited** rather than required.

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

`remove` **revokes**: the factor stays listed with `revoked_at` set, and its
credential stays claimed, so the same certificate or token can never be
enrolled again.

## `qrdb auth factor delete <factor-id>`

Delete a factor outright, so its credential can be bound again — the
rollback for an `add` whose enrolment did not complete. The factor, its
credential and SPKI claims and its place in the user's factor set go in one
atomic commit; the factor is no longer listed. Prints `OK`.

| Flag | Type | Meaning |
|---|---|---|
| `--force` | bool | allow deleting the **last** factor (admin); the user is deactivated |

```bash
qrdb auth factor add --user 42 --type mtls --label "laptop" --cert ./alice/cert.pem
qrdb auth factor delete 7 --force     # 7 was the user's only factor
qrdb auth factor add --user 42 --type mtls --label "laptop" --cert ./alice/cert.pem   # binds again
```

A **revoked** factor cannot be deleted: the command exits non-zero with a
`factor revoked` error and the credential stays claimed, so nothing undoes a
`remove`. Deleting an id that is not there — including one already deleted —
exits non-zero with a not-found error. The command needs the same permission
as `add`.

## PINs

PINs have **no `qrdb` verb group of their own.** They are set during the
ceremony and the barrier lifecycle:

```bash
qrdb init --so-pin SOPIN --user-pin USERPIN
qrdb barrier init --barrier-strategy passphrase --passphrase '<pp>' \
                  --so-pin SO123 --user-pin USR123
qrdb barrier seal --so-pin SO123 -y
qrdb barrier rekey --so-pin SO123
qrdb barrier recovery generate --threshold 3 --so-pin SO123
```

**Dual control**: `--so-pin` requires `--user-pin` and vice versa at
initialization. Any PIN is refused with `--barrier-strategy=none`.

Minimum PIN length is **6 characters**, enforced server side before any backend
sees it.
