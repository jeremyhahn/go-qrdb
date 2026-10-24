# Initialization Ceremony — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

Command group: `qrdb ceremony`. All verbs accept the root persistent flags
documented in [../key-value/cli.md](../key-value/cli.md#global-flags).

> **There is no `qrdb ceremony init` verb.** The root `qrdb init` runs the
> ceremony — see [../firstboot/cli.md](../firstboot/cli.md).

## `qrdb init` with share holders

With `--barrier-strategy shamir --threshold M` (M >= 2), `qrdb init` names the
initial share holders:

| Flag | Type | Meaning |
|---|---|---|
| `--officers` | stringArray | a Security Officer: `username=path/to/csr.pem`; repeatable |
| `--custodians` | stringArray | a key custodian: `username=path/to/csr.pem`; repeatable |
| `--officers-file` | string | path to a JSON document naming every holder with the full shape (below); mutually exclusive with `--officers`/`--custodians` |
| `--policy` | string | factor policy of the initial share set: `designated` (default) or `any-enrolled` |
| `--shares` | int | N; must equal the number of officers plus custodians |

```bash
qrdb init --barrier-strategy shamir --shares 5 --threshold 3 \
     --so-pin "$SO_PIN" --user-pin "$USER_PIN" \
     --officers alice=alice.csr --officers bob=bob.csr \
     --custodians carol=carol.csr --custodians dave=dave.csr \
     --custodians erin=erin.csr
```

Each CSR supplies only a public key; the CA assigns the certificate identity.
The command prints each holder's certificate. Each holder then claims its
envelope with `qrdb barrier officer claim` — see
[../shamir-custody/cli.md](../shamir-custody/cli.md). `--officers`/
`--custodians` enrol only the certificate factor (`mtls`), with the
certificate key receiving the share envelope; a holder that needs a
separate envelope key, an additional factor, or a designated factor uses
`--officers-file` instead (below), rather than enrolling bare here and
rekeying those in afterward with `barrier officer register`.

### `--officers-file`: the rich holder shape

`qrdb init` is the ONLY system bootstrap, so it is also the only place
Shamir custody over the system barrier is ever established. A holder who
needs a separate share-envelope key, an additional authentication factor
(FIDO2, PIV, ...), or a designated factor has no OTHER registration path to
get one through — `--officers`/`--custodians` can only name a username and a
CSR file. `--officers-file` carries everything `barrier officer register`
takes instead, one JSON document naming every initial holder:

```json
{
  "officers": [
    { "username": "alice", "role": "so", "csr_pem": "alice.csr.pem" },
    {
      "username": "carol",
      "role": "custodian",
      "csr_pem": "carol.csr.pem",
      "encryption_kem": "dhkem-x25519",
      "encryption_key": "carol.enc.pub",
      "factors": [{ "name": "fido2", "params": { "attestation": "..." } }],
      "designated": "fido2"
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `username` | the holder's unique identifier; required |
| `role` | `so` (default, empty) or `custodian` |
| `csr_pem` | path to the holder's PEM certificate signing request; required |
| `encryption_kem` | HPKE suite of `encryption_key`: `dhkem-p256`, `dhkem-p384`, `dhkem-x25519` or `mlkem768-x25519`; omit both `encryption_kem`/`encryption_key` to have the certificate key receive the envelope instead |
| `encryption_key` | path to the holder's separate share-envelope public key, raw bytes in `encryption_kem`'s own encoding (not PEM, not base64) |
| `factors` | additional authentication factors to enrol, inline: `[{"name":...,"params":{...}}]`. `mtls` is always enrolled, from the issued certificate |
| `designated` | the factor fixed at enrolment under the designated policy; `mtls` when empty |

`csr_pem` and `encryption_key` are file paths, read at parse time, exactly
like `--admin-csr`/`--admin-cert`/`--officers` — key material is never
accepted inline on the command line or embedded in the document itself.
`factors[].params` IS inline JSON: a factor's own enrolment parameters
(an attestation object, a slot certificate, ...) are produced by that
factor's own enrolment tooling, not file references.

```bash
qrdb init --barrier-strategy shamir --shares 5 --threshold 3 \
     --so-pin "$SO_PIN" --user-pin "$USER_PIN" \
     --officers-file officers.json
```

## `qrdb ceremony status`

Display the current state of the ceremony: `awaiting_init`, `enrolling` or
`operational`. No flags.

```bash
qrdb ceremony status --output json
```

## `qrdb ceremony sign-csr`

Submit a CSR for signing while the initialization ceremony is still
`enrolling`. Once the ceremony reaches `operational`, this command is
refused on every transport — there is no post-ceremony path back to it.
**Issues HOLDER certificates only**: `--role` accepts `so` or `custodian`,
never a login role. A login certificate for a user-facing account (admin, operator,
user, auditor) is issued by the tenant Admin through the certs CSR-sign or
BYOC path ([../certificate-authority/cli.md](../certificate-authority/cli.md)),
which requires an authenticated, authorized caller — not merely the SO PIN
this command accepts before unseal.

| Flag | Type | Meaning |
|---|---|---|
| `--csr-file` | string | path to the CSR PEM |
| `--username` | string | holder username |
| `--role` | string | `so` or `custodian` |
| `--so-pin` | string | Security Officer PIN |

```bash
qrdb ceremony sign-csr --csr-file dave.csr --username dave \
     --role custodian --so-pin SO123
```

## Related groups

- **Root `qrdb init`** — the one-call bootstrap. [../firstboot/cli.md](../firstboot/cli.md)
- **`qrdb barrier officer …`, `quorum …`, `rekey`** — share holders.
  [../shamir-custody/cli.md](../shamir-custody/cli.md)
- **`qrdb ca …`** — post-init CA operations.
  [../certificate-authority/cli.md](../certificate-authority/cli.md)

## Integration tests

`make integration-test-ceremony` drives these verbs end to end
(`test/integration/officer_ceremony_cli_test.go`).
