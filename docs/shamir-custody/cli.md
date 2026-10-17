# Shamir Custody — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first.

Custody verbs live under `qrdb barrier` (`officer`, `quorum`, `rekey`,
`init-shamir`, `recovery`) for the system barrier and under `qrdb tenant`
(`barrier-officer`, `barrier-quorum`, `barrier-rekey`) for a dedicated tenant
barrier. The canonical barrier verbs are documented in
[../barrier-seal/cli.md](../barrier-seal/cli.md); `qrdb init` is in
[../ceremony/cli.md](../ceremony/cli.md).

All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags), and `--output json`
or `--output yaml` where they print a result.

Private keys never leave the machine the CLI runs on. `claim` signs the claim
nonce locally; `quorum submit` answers the `mtls` factor, opens the envelope
and sends only the factor response and the opened share.

## Scope flags

Every `officer` and `quorum` verb takes:

| Flag | Meaning |
|---|---|
| `--domain` | `unseal` (default) or `recovery` — system barrier only |
| `--tenant-id` | the tenant — tenant verbs only |

## Enrolment

### `qrdb barrier officer register`

Enrol a share holder. Run by an administrator or a Security Officer, never by
the holder being enrolled. No share is produced.

| Flag | Type | Meaning |
|---|---|---|
| `--username` | string | holder username; becomes the certificate CN |
| `--role` | string | `so` (default) or `custodian` |
| `--csr-file` | path | PEM CSR supplying the holder's public key; its subject and SANs are ignored |
| `--encryption-kem` | string | HPKE suite of a separate envelope key: `dhkem-p256`, `dhkem-p384`, `dhkem-x25519`, `mlkem768-x25519` |
| `--encryption-key-file` | path | the separate envelope public key, raw, in the suite's encoding |
| `--factor` | stringArray | an additional factor: `name` or `name=<json params>`; repeatable |
| `--designated` | string | the factor fixed at enrolment for the `designated` policy (default `mtls`) |

```bash
qrdb barrier officer register --username alice --role so --csr-file alice.csr
qrdb barrier officer register --username carol --role custodian --csr-file carol.csr \
     --encryption-kem mlkem768-x25519 --encryption-key-file carol-kem.pub \
     --factor "fido2=$(cat carol-fido2-enrol.json)" --designated fido2
```

The command prints the CA-issued certificate; deliver it to the holder.

### `qrdb barrier officer list`

Holders and their claim state. Never a share.

## Distribution

### `qrdb barrier init-shamir`

Split the unseal key to the enrolled unseal-domain holders.

| Flag | Type | Meaning |
|---|---|---|
| `--threshold` | int | M |
| `--total` | int | N; must equal the number of enrolled holders |
| `--policy` | string | `designated` (default) or `any-enrolled` |
| `--so-pin`, `--user-pin` | string | optional PIN pair; each requires the other |

```bash
qrdb barrier init-shamir --threshold 3 --total 5
```

### `qrdb barrier recovery generate`

Generate the independent recovery key and split it to every enrolled
recovery-domain holder (`officer register --domain recovery`). Needs the SO
PIN.

| Flag | Type |
|---|---|
| `--threshold` | int |
| `--policy` | string |
| `--so-pin` | string |

`recovery status` and `recovery delete` report and remove the recovery key.

## Claim

### `qrdb barrier officer claim`

Claim the holder's envelope, once. The connection must present the holder's
certificate (the root `--cert-file`/`--key-file` flags).

| Flag | Type | Meaning |
|---|---|---|
| `--username` | string | the holder |
| `--key` | path | PEM private key of the holder's certificate, used to sign the nonce |
| `--out` | path | where to write the envelope (mode 0600) |

### `qrdb barrier officer claim-begin` / `claim-complete`

The same claim, split for keys that sign outside the CLI (a smart card, an
HSM). `claim-begin --username` prints the nonce and `SignMessageHex`
(`sign_message_hex` in JSON). That is the exact share-claim message to sign
([api.md](api.md#schemas)): sign those bytes, not the bare nonce.
`claim-complete --username --nonce-hex --sig-file --out` delivers the envelope.

## Quorum rounds

### `qrdb barrier quorum begin`

Issue a challenge for a holder's factor. Needed only when the factor is
answered outside the CLI; the `mtls` factor is answered by `quorum submit`
itself. A key that signs outside the CLI answers an `mtls` challenge with a
signature over the challenge message -- `qrdb/mtls-challenge/v1`, the
`challenge_id`, `method_data.purpose` and the nonce, NUL-separated
([engineering.md](engineering.md#holder-verification)) -- never over the bare
nonce.

| Flag | Type | Meaning |
|---|---|---|
| `--op` | string | `unseal`, `seal`, `root-token`, `rekey` or `recover` |
| `--username` | string | the holder |
| `--factor` | string | the factor to answer with (designated when empty) |

### `qrdb barrier quorum submit`

Submit the holder's share to the open round for `--op`.

| Flag | Type | Meaning |
|---|---|---|
| `--op` | string | as above |
| `--username` | string | the holder |
| `--envelope` | path | the claimed envelope |
| `--key` | path | the certificate key: answers `mtls` and, without `--encryption-key-file`, opens the envelope |
| `--encryption-key-file` | path | the raw private key of a separately enrolled envelope key |
| `--challenge-id` | string | a challenge answered outside the CLI |
| `--factor` | string | the factor that answered it |
| `--response-file` | path | the raw factor response |
| `--response-metadata` | string | a JSON object of response metadata |

```bash
# alice answers mtls with her certificate key
qrdb barrier quorum submit --op unseal --username alice \
     --envelope alice.env --key alice.key
# carol answers her designated FIDO2 factor
qrdb barrier quorum begin --op unseal --username carol -o json   # -> challenge
qrdb barrier quorum submit --op unseal --username carol --envelope carol.env \
     --encryption-key-file carol-kem.key \
     --challenge-id "$ID" --factor fido2 --response-file carol.assertion
```

Each submission prints the round's progress; the M-th completes the
operation (for `root-token`, it prints the token).

## Rekey and revocation

### `qrdb barrier rekey`

Open a rekey round. It completes when M current holders submit to
`--op rekey`, and then redistributes the key to every holder enrolled at that
moment — which is how a holder is added.

| Flag | Type | Meaning |
|---|---|---|
| `--domain` | string | `unseal` (default) or `recovery` |
| `--threshold` | int | M of the new share set |
| `--policy` | string | `designated` or `any-enrolled` |
| `--so-pin` | string | Security Officer PIN |

### `qrdb barrier officer revoke`

Revoke a holder: a rekey round that removes them, completed by M of the
remaining holders and refused when M would exceed N−1.

| Flag | Type | Meaning |
|---|---|---|
| `--username` | string | the holder to revoke |
| `--threshold` | int | M of the remaining share set; 0 keeps the current one |
| `--so-pin` | string | Security Officer PIN |

Every remaining holder claims a new envelope once the round completes.

## Tenant barriers

`qrdb tenant barrier-officer register|list|claim|claim-begin|claim-complete|revoke`,
`qrdb tenant barrier-quorum begin|submit` and `qrdb tenant barrier-rekey`
take `--tenant-id` and otherwise the same flags, without `--domain` and
without `--so-pin` (tenant rekey and revocation are authorized by RBAC).

## Database barriers

`qrdb database barrier-officer register|list|claim|claim-begin|claim-complete|revoke|unenrol`,
`qrdb database barrier-quorum begin|submit`, `qrdb database barrier-rekey` and
`qrdb database barrier-init-shamir` take `--database` and otherwise the same
flags, without `--domain` and `--so-pin`. `barrier-init-shamir` takes only
`--policy`: the M-of-N was fixed when the database was created. `unenrol`
removes a holder before initialization only. See
[../databases/cli.md](../databases/cli.md#share-holder-verbs-shamir-database-barrier).

## Desktop application

The desktop application's `OfficerBinding` drives the same verbs:
`RegisterHolder`, `ListHolders`, `ClaimEnvelope`, `SubmitShare`, `StartRekey`,
`RevokeHolder`. Keys passed to it are used in the application process and
wiped after the call.
