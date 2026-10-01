# Initialization Ceremony — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Command group: `qrdb ceremony`. All verbs accept the root persistent flags
documented in [../key-value/cli.md](../key-value/cli.md#global-flags).

> **`qrdb ceremony init` has been REMOVED.** Use the root `qrdb init` command —
> see [../firstboot/cli.md](../firstboot/cli.md). The verb remains registered
> only to say so.

## `qrdb ceremony status`

Display the current state of the initialization ceremony: `awaiting_init`,
`enrolling` or `operational`. No flags.

```bash
qrdb ceremony status --output json
```

## `qrdb ceremony sign-csr`

Submit a CSR for signing during or after the ceremony.

| Flag | Type | Meaning |
|---|---|---|
| `--csr-file` | string | path to the CSR PEM |
| `--username` | string | officer username |
| `--role` | string | role for the signed certificate |
| `--so-pin` | string | Security Officer PIN |

```bash
qrdb ceremony sign-csr --csr-file dave.csr --username dave \
     --role operator --so-pin SO123
```

## Certificate claim — `qrdb ceremony claim-cert`

Two steps. An officer proves possession of the private key matching their CSR
by signing a nonce.

### `claim-cert begin`

| Flag | Type |
|---|---|
| `--username` | string |

Returns a nonce for the officer to sign.

### `claim-cert complete`

| Flag | Type | Meaning |
|---|---|---|
| `--username` | string | officer username |
| `--nonce` | string | challenge nonce hex from `begin` |
| `--signature` | string | base64-encoded signature of the nonce |

Returns the issued certificate.

```bash
NONCE=$(qrdb ceremony claim-cert begin --username alice --output json | jq -r .nonce)
qrdb ceremony claim-cert complete --username alice \
     --nonce "$NONCE" --signature "$SIG_B64"
```

## Share claim — `qrdb ceremony claim-share`

Claim the officer's Shamir share over a nonce challenge/response. **Requires an
mTLS client certificate matching the officer's enrolment record.**

### `claim-share begin`

| Flag | Type |
|---|---|
| `--username` | string |

Returns a nonce the officer must sign with the private key matching their
enrolled certificate.

### `claim-share complete`

| Flag | Type | Meaning |
|---|---|---|
| `--username` | string | officer username |
| `--nonce` | string | nonce hex from `begin` |
| `--signature` | string | base64 signature over the **raw nonce bytes** |

**Returns the Shamir share exactly once.**

## Factor-verified custodian claim

The custodian path uses an authentication factor rather than a bare signature.

### `qrdb ceremony claim-share-begin`

Issue an auth-factor challenge for a custodian prior to releasing their share.

| Flag | Type |
|---|---|
| `--custodian-id` | string |

### `qrdb ceremony claim-share-complete`

Verify the response and release the custodian's share record.

| Flag | Type |
|---|---|
| `--custodian-id` | string |
| `--challenge-id` | string — from `claim-share-begin` |
| `--factor-response` | string — base64-encoded factor response |

### `qrdb ceremony submit-unseal-share`

Submit a custodian's share toward the unseal quorum; the barrier unseals once
the threshold is reached.

| Flag | Type | Meaning |
|---|---|---|
| `--custodian-id` | string | custodian identifier |
| `--share` | string | base64-encoded Shamir share |
| `--challenge-id` | string | from `claim-share-begin` |
| `--factor-response` | string | base64 factor response — **optional** |

```bash
qrdb ceremony submit-unseal-share --custodian-id c-1 --share "$SHARE_B64"
```

Note the two spellings in this group: the hyphenated
`claim-share-begin`/`claim-share-complete` verbs are the **custodian,
factor-verified** path; the nested `claim-share begin`/`complete` are the
**officer, signature-verified** path. They are different flows, not aliases.

The factor challenge/response here is verified through `pkg/ceremony`'s own
`CeremonyExtension.FactorVerifier` seam (`CeremonyService.ClaimShareChallenge`
/ `VerifyClaimAndDeliver`) — a **different** seam from the
`service.AuthFactorServicer` the daemon exposes at `/v1/authfactors/*` and
documents in
[../auth-factors/engineering.md](../auth-factors/engineering.md#the-custodian-surface-is-routed-too).
No composer in this repository calls `SetExtension` to supply that extension
today, so `claim-share-begin` and `claim-share-complete` return
`ErrNotImplemented` until one does; the tenant-routing described there applies
to the separate `/v1/authfactors/*` surface, not to this one.

## Related groups

- **Root `qrdb init`** — the one-call bootstrap. [../firstboot/cli.md](../firstboot/cli.md)
- **`qrdb barrier officer …`** — Security Officer registration and claim.
  [../shamir-custody/cli.md](../shamir-custody/cli.md)
- **`qrdb ca …`** — post-init CA operations.
  [../certificate-authority/cli.md](../certificate-authority/cli.md)

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite. The
custodian flow (`claim-share-begin` → `claim-share-complete` →
`submit-unseal-share`) is exposed with full parity across REST, gRPC, QUIC, MCP,
Unix and embedded.
