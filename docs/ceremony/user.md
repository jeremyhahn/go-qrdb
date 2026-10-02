# Initialization Ceremony — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

The ceremony is how a brand-new system gets a CA, a barrier and its security
officers. Most operators never run it by hand — `qrdb init` does it for you.
This guide is for when you need the pieces separately.

## Prerequisites

- A daemon that has never been initialized (`qrdb ceremony status` reports
  `awaiting_init`).
- A CSR from each officer, generated with their own private key — ideally on a
  PIV card or FIDO2 authenticator.
- The SO and User PINs you intend to set.

## The usual path

```bash
qrdb init --so-pin SOPIN --user-pin USERPIN
```

or, for M-of-N:

```bash
qrdb init --barrier-strategy shamir --shares 5 --threshold 3 \
          --so-pin SOPIN --user-pin USERPIN \
          --officers alice=alice.csr --officers bob=bob.csr \
          --officers carol=carol.csr
```

See [../firstboot/user.md](../firstboot/user.md). Note that
`qrdb ceremony init` **no longer exists** — the root `init` replaced it.

## Watching the state

```bash
qrdb ceremony status
```

| State | Meaning |
|---|---|
| `awaiting_init` | nothing has happened yet |
| `enrolling` | initialized; officers still claiming |
| `operational` | everyone has claimed; system ready |

In single-admin mode you go straight to `operational`. In M-of-N you stay in
`enrolling` until the last officer claims.

## Claiming as an officer

Two things to claim: your certificate, and your share. Both are nonce
challenges you sign with your own key.

**Your certificate:**

```bash
NONCE=$(qrdb ceremony claim-cert begin --username alice --output json | jq -r .nonce)
# sign the nonce with the key that made your CSR, base64 the signature
qrdb ceremony claim-cert complete --username alice \
     --nonce "$NONCE" --signature "$SIG_B64"
```

**Your share** — this needs an mTLS client certificate matching your enrolment
record, so do it after you have the certificate:

```bash
NONCE=$(qrdb ceremony claim-share begin --username alice --output json | jq -r .nonce)
qrdb ceremony claim-share complete --username alice \
     --nonce "$NONCE" --signature "$SIG_B64"
```

**The share comes back exactly once.** Store it before you lose the terminal.
The server destroys its copy the moment it hands it over.

## Claiming as a custodian

Custodians use an authentication factor (FIDO2, PIV, PKCS#11) instead of a bare
signature:

```bash
CH=$(qrdb ceremony claim-share-begin --custodian-id c-1 --output json | jq -r .challenge_id)
qrdb ceremony claim-share-complete --custodian-id c-1 \
     --challenge-id "$CH" --factor-response "$RESP_B64"
```

Then, at unseal time:

```bash
qrdb ceremony submit-unseal-share --custodian-id c-1 --share "$SHARE_B64"
```

Note these hyphenated verbs are the **custodian** path. The nested
`claim-share begin` / `complete` are the **officer** path. Different flows.

## Adding an officer later

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

To give a new officer a *share*, you rekey — see
[../shamir-custody/user.md](../shamir-custody/user.md).

## Troubleshooting

**"no officers".** You asked for `threshold >= 2` without naming the officers.
This fails closed rather than initializing a quorum arrangement with nobody in
it. Pass `--officers user=path/to/csr.pem` for each.

**The server restarted mid-enrolment and the shares are gone.** They are. A
pending share lives only in memory and is never written to disk — sealed as
ciphertext (`secure.Secret`) until the moment a custodian claims it, and
mlocked only for that instant — so an abandoned ceremony must be re-run. That
is deliberate: shares belong to custodians, not to the system.

**A claim was refused with "already claimed".** Delivery is once. If the share
was lost between the response and your storage, the recovery is a rekey.

**The nonce expired.** Get a new one from `begin` and sign it promptly.

**Your certificate authenticates but the share claim is refused.** The share
claim matches your mTLS certificate against the enrolment record. If you claimed
a certificate but are presenting a different one, it will not match.

**PINs.** They are hashed by the PIN backend (Argon2id, or PBKDF2 in FIPS mode)
and never stored raw. In M-of-N the User PIN *is* the secret being split, so
choosing a weak one weakens the whole arrangement.

**An officer wants to use a certificate they already hold** (corporate PKI, a
card vendor's) rather than getting one issued. That is the external-officer
path — enrol with `cert` or `pubkey` rather than `csr`. `cert` is verified
against a configured external CA pool; `pubkey` is asserted by the administrator
running the ceremony and has no chain to check.

**Nothing works and the system says it is not initialized.**
`GET /v1/config/initialized` is a public endpoint precisely so you can ask that
question without credentials.
