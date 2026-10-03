# Initialization Ceremony — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-29

Command group: `qrdb ceremony`. All verbs accept the root persistent flags
documented in [../key-value/cli.md](../key-value/cli.md#global-flags).

> **`qrdb ceremony init` has been REMOVED.** Use the root `qrdb init` command —
> see [../firstboot/cli.md](../firstboot/cli.md). The verb remains registered
> only to say so.

## `qrdb init` with share holders

With `--barrier-strategy shamir --threshold M` (M >= 2), `qrdb init` names the
initial share holders:

| Flag | Type | Meaning |
|---|---|---|
| `--officers` | stringArray | a Security Officer: `username=path/to/csr.pem`; repeatable |
| `--custodians` | stringArray | a key custodian: `username=path/to/csr.pem`; repeatable |
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
[../shamir-custody/cli.md](../shamir-custody/cli.md). Holders enrolled here
have only the certificate factor (`mtls`); enrol additional factors with
`barrier officer register` and a rekey.

## `qrdb ceremony status`

Display the current state of the ceremony: `awaiting_init`, `enrolling` or
`operational`. No flags.

```bash
qrdb ceremony status --output json
```

## `qrdb ceremony sign-csr`

Issue a system-tenant certificate under SO PIN authorization: a login
certificate for an RBAC role, or, for `--role so` and `--role custodian`, a
holder certificate that carries no roles. The CA assigns the
identity (`CN=<username>`, `OU=[system, <profile>]`, no SAN); the CSR supplies
only the public key.

| Flag | Type | Meaning |
|---|---|---|
| `--csr-file` | string | path to the CSR PEM |
| `--username` | string | the user the certificate is for |
| `--role` | string | role for the signed certificate |
| `--so-pin` | string | Security Officer PIN |

```bash
qrdb ceremony sign-csr --csr-file dave.csr --username dave \
     --role operator --so-pin "$SO_PIN"
```

## Related groups

- **Root `qrdb init`** — the one-call bootstrap. [../firstboot/cli.md](../firstboot/cli.md)
- **`qrdb barrier officer …`, `quorum …`, `rekey`** — share holders.
  [../shamir-custody/cli.md](../shamir-custody/cli.md)
- **`qrdb ca …`** — post-init CA operations.
  [../certificate-authority/cli.md](../certificate-authority/cli.md)

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite.
