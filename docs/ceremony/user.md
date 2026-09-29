# Initialization Ceremony — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-29

The ceremony is how a brand-new system gets its PINs, a CA, a barrier and, for
M-of-N custody, its first share holders. Most operators never run it by hand —
`qrdb init` does it for you.

## Prerequisites

- A daemon that has never been initialized (`qrdb ceremony status` reports
  `awaiting_init`).
- For M-of-N: a CSR from each initial share holder — Security Officers and key
  custodians — generated with their own private key, ideally on a PIV card or
  security key.
- The SO and User PINs you intend to set.

## The usual path

```bash
qrdb init --so-pin "$SO_PIN" --user-pin "$USER_PIN"
```

or, for M-of-N:

```bash
qrdb init --barrier-strategy shamir --shares 5 --threshold 3 \
          --so-pin "$SO_PIN" --user-pin "$USER_PIN" \
          --officers alice=alice.csr --officers bob=bob.csr \
          --custodians carol=carol.csr --custodians dave=dave.csr \
          --custodians erin=erin.csr
```

`--shares` must equal the number of holders you name. The command prints each
holder's certificate; give each holder theirs. You cannot name yourself as a
holder. See [../firstboot/user.md](../firstboot/user.md) for the rest of what
`init` sets up.

## Watching the state

```bash
qrdb ceremony status
```

| State | Meaning |
|---|---|
| `awaiting_init` | nothing has happened yet |
| `enrolling` | custody initialized; holders still claiming |
| `operational` | every holder has claimed, or single-admin mode |

## Claiming as a holder

Each holder claims their own sealed envelope once, connecting with the
certificate they were issued:

```bash
qrdb --cert-file alice.crt --key-file alice.key \
     barrier officer claim --username alice --key alice.key --out alice.env
```

Everything after that — using the envelope to unseal, adding and revoking
holders — is in [../shamir-custody/user.md](../shamir-custody/user.md).

## Issuing a login certificate

```bash
qrdb ceremony sign-csr --csr-file dave.csr --username dave \
     --role operator --so-pin "$SO_PIN"
```

The certificate is issued as `CN=dave`, `OU=[system, <role profile>]` with no
Subject Alternative Name: `--username` is the identity, and the CSR contributes
only its key. Whatever Subject or SANs the CSR asks for are ignored, so this
step cannot mint a login in another tenant. A `--role so` certificate is an
officer credential and never logs in as a user.

Choose an SO PIN with **at least 40 bits of entropy**, for example 8 random
characters from letters and digits, or a 12-digit random number. Before
unseal, sign-csr can be reached without authentication, so the PIN is all that
protects it. After 5 wrong PINs in a row the check locks for a delay that
doubles from 1 second up to 15 minutes, and the server answers `429` with
`Retry-After`. A correct PIN resets the count.

This issues a login certificate, not a share. To give someone a share, enrol
them as a holder and rekey.

## Troubleshooting

**"M-of-N requires at least one officer".** You asked for `--threshold >= 2`
without naming any holders. Pass `--officers` and `--custodians`.

**"--shares must equal the number of --officers plus --custodians".** The key
is split to exactly the holders you name.

**The server restarted before everyone claimed.** Unclaimed envelopes live
only in memory and are gone; claimed ones are unaffected. M holders can rekey
to redistribute.

**A claim was refused with "already claimed".** Delivery is once. A lost
envelope is replaced by a rekey.

**The claim is refused for the certificate.** The claim must come over a
connection presenting the certificate you were issued.

**PIN operations fail with "no PIN backend".** SO-PIN operations refuse to run
without one; the daemon wires one by default.

**Nothing works and the system says it is not initialized.**
`GET /v1/config/initialized` is a public endpoint precisely so you can ask that
question without credentials.
