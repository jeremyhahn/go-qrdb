# Shamir Custody — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Custody verbs live under `qrdb barrier`, in the `officer`, `recovery` and
`root-token` sub-trees. The canonical barrier verbs themselves are documented in
[../barrier-seal/cli.md](../barrier-seal/cli.md).

The `officer`, `shamir-seal-vote` and `root-token` sub-trees are retained for
operational parity and are **not** part of the canonical CLI surface shared
across the stack's binaries.

All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

## Security Officers

Officers are registered **before** barrier initialization. After
initialization their shares are sealed to their registered certificates, and
each claims their own share through an mTLS challenge/response.

### `qrdb barrier officer register`

The officer's CSR is validated, signed by the ceremony CA, and the officer
record is persisted. Only the CSR's key is used: the certificate is issued as
`CN=<--username>`, `OU=[<tenant>, custodian]` with no SAN, whatever Subject the
CSR names. **Share bytes are NOT produced at this step** — the
barrier must be initialized (`barrier init-shamir`) for distribution to occur.

| Flag | Type | Meaning |
|---|---|---|
| `--username` | string | Security Officer username |
| `--csr-file` | string | path to a PEM-encoded CSR |
| `--authenticator-type` | string | e.g. `software`, `fido2`, `piv` |
| `--authenticator-ref` | string | opaque authenticator reference |

```bash
qrdb barrier officer register --username alice --csr-file alice.csr \
     --authenticator-type piv --authenticator-ref "slot9a"
```

### `qrdb barrier officer list`

List enrolled Security Officers for the system barrier. No flags.

### `qrdb barrier officer claim-status`

Per-officer enrolment and claim state. **Never returns share bytes.** No flags.

```bash
qrdb barrier officer claim-status --output json
```

### `qrdb barrier officer claim-begin`

Issue a challenge nonce to the named officer. The officer signs it with their
registered private key and submits the signature to `claim-complete` **within
the expiry window (5 minutes)**.

| Flag | Type |
|---|---|
| `--username` | string |

### `qrdb barrier officer claim-complete`

Verify the nonce signature and return the share bytes **exactly once**. The
signature file must contain the raw signature bytes produced by signing the
nonce from `claim-begin`.

| Flag | Type |
|---|---|
| `--username` | string |
| `--nonce-hex` | string — the nonce from `claim-begin` |
| `--sig-file` | string — path to the raw signature bytes |

```bash
NONCE=$(qrdb barrier officer claim-begin --username alice --output json | jq -r .nonce)
# sign $NONCE with alice's private key into alice.sig
qrdb barrier officer claim-complete --username alice \
     --nonce-hex "$NONCE" --sig-file alice.sig
```

The share is returned once and the server-side enclave is destroyed. There is
no second chance — store it before the terminal scrolls.

## Quorum operations

### `qrdb barrier unseal-shares`

Submit shares to unseal. See
[../barrier-seal/cli.md](../barrier-seal/cli.md#qrdb-barrier-unseal-shares).
Below-threshold submissions accumulate and report progress; they are not
errors.

### `qrdb barrier shamir-seal-vote`

Submit a share as a vote to **seal**. A Shamir barrier cannot be sealed by one
administrator — M officers must each vote. When the threshold is met all
submitted shares are wiped and the barrier seals.

| Flag | Type |
|---|---|
| `--share` | string |

### `qrdb barrier rekey`

Rotate the unseal key and the whole share set, **cryptographically revoking
every previously distributed share**. Claim history resets and every officer
must claim again. See
[../barrier-seal/cli.md](../barrier-seal/cli.md#qrdb-barrier-rekey).

### `qrdb barrier root-token`

Generate a one-time admin token by reconstructing from shares. Officers
submitting their claimed shares **is** the consent signal, and this is the only
place raw shares are legitimate request input.

| Flag | Type |
|---|---|
| `--share` | stringArray — repeatable; a share, or the claim-complete JSON envelope |

```bash
qrdb barrier root-token --share a --share b --share c
```

## Recovery keys

Recovery keys are an **independent** Shamir split of the root key, for the case
where the operational quorum is unavailable. They are never returned in the
generation response and never persisted on the server.

### `qrdb barrier recovery generate`

Requires the barrier **unsealed**, and a Security Officer PIN when a PINBackend
is configured.

| Flag | Type | Meaning |
|---|---|---|
| `--threshold` | int | minimum keys required for recovery (M) |
| `--total` | int | total recovery keys (N) |
| `--so-pin` | string | required when a PINBackend is configured |

```bash
qrdb barrier recovery generate --threshold 3 --total 5 --so-pin SO123
```

### `qrdb barrier recovery officer register` / `claim-begin` / `claim-complete`

Recovery officers are **independent from the unseal officers** and receive the
recovery shares. The flags mirror the unseal-officer verbs above
(`--username`, `--csr-file`, `--authenticator-type`, `--authenticator-ref`;
then `--nonce-hex`, `--sig-file`).

### `qrdb barrier recovery recover`

Recover the barrier by submitting M-of-N recovery keys. Requires the barrier
**sealed**.

| Flag | Type |
|---|---|
| `--share` | stringArray — repeatable; a recovery key, or the claim-complete JSON envelope |

```bash
qrdb barrier recovery recover --share key-a --share key-b --share key-c
```

### `qrdb barrier recovery status` / `delete` (operational)

`status` reports whether recovery keys are present. `delete` removes all stored
recovery key metadata. Neither takes flags.

## Per-tenant custody

Tenants with a dedicated barrier have the same officer model under
`qrdb tenant barrier-officer` — see
[../multi-tenancy/cli.md](../multi-tenancy/cli.md).

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite.
