# Initialization Ceremony — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
> Conflicts with this document are defects. Fix them here first.

The ceremony is how a brand-new system gets its PINs, a CA, a barrier and, for
M-of-N custody, its first share holders. Most operators never run it by hand —
`qrdb init` does it for you.

## Prerequisites

- A daemon that has never been initialized (`qrdb barrier status` reports
  `Initialized: false`; a sealed node refuses `qrdb ceremony status`).
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
| `restore_failed` | the node could not read its ceremony state at startup; fix the storage fault and restart it |

## Claiming as a holder

Each holder claims their own sealed envelope once, connecting with the
certificate they were issued:

```bash
qrdb --cert-file alice.crt --key-file alice.key \
     barrier officer claim --username alice --key alice.key --out alice.env
```

Everything after that — using the envelope to unseal, adding and revoking
holders — is in [../shamir-custody/user.md](../shamir-custody/user.md).

## Issuing a holder certificate

`sign-csr` issues HOLDER certificates only: `--role so` for a
Security Officer, `--role custodian` for a Shamir share holder. Neither holds
a login account.

```bash
qrdb ceremony sign-csr --csr-file dave.csr --username dave \
     --role custodian --so-pin "$SO_PIN"
```

The certificate is issued as `CN=dave`, `OU=[system, <holder profile>]` with no
Subject Alternative Name: `--username` is the identity, and the CSR contributes
only its key. Whatever Subject or SANs the CSR asks for are ignored, so this
step cannot mint a login in another tenant. Neither holder role's certificate
ever logs in as a user.

A login certificate for a user-facing account (admin, operator, user,
auditor) is a SEPARATE path: the tenant Admin issues it through the certs
CSR-sign or BYOC flow ([../certificate-authority/user.md](../certificate-authority/user.md)),
which requires an authenticated, authorized caller rather than only the SO
PIN. `sign-csr` refuses any other role with "invalid role".

`sign-csr` works only while the ceremony is `enrolling`. In that state it is
reachable on the sealed-window listener without session authentication:
there is no IAM plane yet, and the SO PIN is the only credential. In any
other state `sign-csr` is refused on every transport, whatever PIN, session
or certificate is presented. Certificates already issued keep verifying;
only minting a new one through `sign-csr` is limited to `enrolling`.

Choose an SO PIN with **at least 40 bits of entropy**, for example 8 random
characters from letters and digits, or a 12-digit random number: it is all
that protects `sign-csr` on the sealed-window listener. After 5 wrong PINs in a row the check locks for a delay that
doubles from 1 second up to 15 minutes, and the server answers `429` with
`Retry-After`. A correct PIN resets the count.

This issues a certificate, not a share. To give someone a share, enrol
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
