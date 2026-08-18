# Authentication Factors and PINs — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

A user can hold several authentication factors — a client certificate, a FIDO2
token, a PIV card — and log in with any of them.

## Prerequisites

A reachable server, client certificates, and the factor permissions. To enroll a
FIDO2 or PIV factor you need the credential bytes from the device, which you get
from the tool that talks to the device.

**Important:** the `qrdb` CLI does **not** talk to hardware. It maps a factor's
lookup key to a user. For real hardware login use `xkmsctl auth login` or
`platformctl auth login`.

## First run

Enroll your client certificate as an mTLS factor:

```bash
qrdb auth factor add --user 42 --type mtls --label "laptop" \
     --cert ./alice/cert.pem
qrdb auth factor list --user 42
```

## Common tasks

**Add a security key.**

```bash
qrdb auth factor add --user 42 --type fido2 --label "yubikey-5" \
     --credential-hex a1b2c3... --device "YubiKey 5 NFC"
```

**Add a PIV card.** `--slot` defaults to `9a`.

```bash
qrdb auth factor add --user 42 --type piv --label "badge" \
     --credential-hex d4e5f6... --reader "Generic Reader 00 00" --slot 9a
```

**Rename one** so you can tell your two tokens apart later:

```bash
qrdb auth factor relabel 7 --label "yubikey-5 (spare)"
```

**Rotate key material** — a new certificate, a re-registered credential:

```bash
qrdb auth factor rotate 7 --type mtls --label "laptop" \
     --cert ./alice/new-cert.pem
```

**Remove one.**

```bash
qrdb auth factor remove 7
```

Removing somebody's **last** factor is refused unless you pass `--force`,
because it locks them out.

**Enroll a second factor before you need it.** One token, lost, is an account
you cannot reach. Two is a bad day instead.

## PINs

PINs are set during initialization and used to authorize privileged barrier
operations. There is no separate PIN command group.

```bash
qrdb init --so-pin SOPIN --user-pin USERPIN
qrdb barrier seal --so-pin SO123 -y
qrdb barrier rekey --so-pin SO123
```

**Both PINs or neither** at initialization: supplying `--so-pin` requires
`--user-pin` and vice versa. That pair is what satisfies FIPS 140-3 and
PCI-DSS v4 dual control. Any PIN is refused entirely with
`--barrier-strategy=none`, because there is nothing to protect.

Minimum length is **6 characters**, enforced server side.

## Lockout — read this carefully

| Backend | Lockout |
|---|---|
| software (the default) | **none** — the hash strength is the protection |
| TPM 2.0 | native dictionary-attack protection |
| PKCS#11 token | the token's own policy |

The software backend has **no attempt counter**. A lockout status of "none" from
it means "this backend does not do lockout", not "you have attempts left". If
you need real lockout, the deployment must inject a hardware PIN backend.

A FIPS binary can verify a PIN hashed by a non-FIPS binary and vice versa —
the algorithm is stored with each record and resolved at verification time.

## Troubleshooting

**`auth login` succeeded but nothing is authorized.** Login resolves a factor to
a user; it does not grant permissions. The user still needs a role — see
[../identity-rbac/user.md](../identity-rbac/user.md).

**`auth login` does not touch your security key.** Correct. The qrdb CLI
resolves the `(method, lookup key)` tuple only. Use `xkmsctl auth login` or
`platformctl auth login` for the hardware round trip.

**Removing a factor was refused.** It was the user's last one. `--force`
overrides it; be sure you have another way in first.

**"PIN too short".** Six characters, minimum, enforced before any backend sees
it.

**"PIN already set".** Use the change operation rather than the set operation.

**A hardware backend says it is locked out.** The error carries a
`LockoutStatus` with the recovery time. On a TPM, `ErrHierarchyAuthMismatch`
is worse — it means the hierarchy auth came from a session whose local state was
lost, and a factory reset is required.

**One PIN was rejected on its own.** Dual control: they come in pairs at
initialization.

**Your factor enrolment data looks wrong in the store.** qrdb stores it as an
opaque blob and never parses it — the shape is entirely the factor
implementation's. That is deliberate: it means a factor cannot smuggle an
authorization decision through its own data.
