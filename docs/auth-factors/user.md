# Authentication Factors and PINs — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-14

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

**Hand out a one-time enrolment.** When a user may enrol their first card
and only one — an onboarding link, a recovery token — make the add
conditional, and the server refuses every add after the first that succeeds,
however many arrive at once:

```bash
qrdb auth factor add --user 42 --type piv --label "badge" \
     --credential-hex d4e5f6... --if-no-active-type
```

**Add only if nothing changed since you looked.** `list` prints the user's
factor-set version; pass it back, and the add is refused if any factor was
added, removed, deleted or rotated in between:

```bash
qrdb auth factor list --user 42            # ... Factor set version: 3
qrdb auth factor add --user 42 --type fido2 --label "spare" \
     --credential-hex a1b2c3... --expected-version 3
```

From Go, set `IfNoActiveType` or `ExpectedFactorSetVersion` on the add
request and match a refusal with
`errors.Is(err, sdk.ErrFactorConditionFailed)`.

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
because it locks them out. A removed factor is revoked, not erased: it stays
listed, and its certificate or token can never be enrolled again.

**Undo an add that did not complete.** If you enrolled a factor as one step
of something that then failed, delete it rather than removing it, and the
same certificate or token can be enrolled again:

```bash
qrdb auth factor delete 7
qrdb auth factor add --user 42 --type mtls --label "laptop" \
     --cert ./alice/cert.pem
```

`delete` refuses a factor that has been removed, so it never brings back a
revoked credential, and it needs `--force` for somebody's last factor just as
`remove` does.

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
| software (the default) | **none in the backend itself** — the hash strength is the protection; the node's SO PIN lockout decorator fronts it (below) |
| TPM 2.0 | native dictionary-attack protection |
| PKCS#11 token | the token's own policy |

The software backend has **no attempt counter** of its own. A node fronts its
PIN backend with an SO PIN lockout that counts each caller separately: one
counter for anonymous sealed-window callers, and one for each authenticated
identity or verified client certificate. After 5 wrong SO PINs in a row on a
counter, every SO PIN check by that caller -- the right PIN included -- is
refused for a backoff that doubles from 1 s up to 15 min, on every operation
and transport. Neither anonymous guesses nor another certificate holder's
guesses lock the Security Officer out. Authenticated callers together may
present at most 64 wrong PINs in 15 minutes; past that every authenticated
SO PIN check waits for the window to end. Wait the backoff
out and present the right PIN to recover. The counters are in memory, so a
restart clears them. If you need a lockout that survives restarts,
the deployment must inject a hardware PIN backend.

A FIPS binary can verify a PIN hashed by a non-FIPS binary and vice versa —
the algorithm is stored with each record and resolved at verification time.

## Upgrading from a build without tenant-scoped factors

Earlier builds stored every factor in one keyspace with no tenant in it. The first time the
server serves an authentication after upgrading, it moves each record into the
tenant that owns its user and logs what it did.

You do not have to run anything. Two log lines matter:

- `auth-factor records migrated into their owning tenants` — informational, with
  a count. Nothing to do.
- `auth-factor migration refused a record` at **ERROR**, naming the record's key
  and its factor and user ids. The server could not establish which tenant the
  record belongs to and **will not guess**, because a credential placed in the
  wrong organization authenticates the wrong person. The record is left where it
  is and resolves for nobody, so the user it belonged to must re-enrol that
  factor — or, if the user is gone, the record can be deleted.
- `auth-factor migration could not reach every owning tenant` at **WARN**. This
  one needs no action. A tenant with its own barrier is sealed until its
  administrator unseals it, and a server that has not yet been given a tenant's
  key cannot write to that tenant either — so those records wait, and the server
  places them on its own once the tenant becomes reachable. Every other tenant
  signs in normally in the meantime.

Everything else keeps working during and after the move: only the refused
records are affected, and the server names every one of them.

## Troubleshooting

**`auth login` succeeded but nothing is authorized.** Login resolves a factor to
a user; it does not grant permissions. The user still needs a role — see
[../identity-rbac/user.md](../identity-rbac/user.md).

**`auth login` does not touch your security key.** Correct. The qrdb CLI
resolves the `(method, lookup key)` tuple only. Use `xkmsctl auth login` or
`platformctl auth login` for the hardware round trip.

**Removing a factor was refused.** It was the user's last one. `--force`
overrides it; be sure you have another way in first.

**A conditional add was refused.** The condition did not hold, and nothing
was enrolled. With `--if-no-active-type` the error names the active factor
that already holds the type; remove or revoke it first if a replacement is
intended, or use `rotate`. With `--expected-version` the user's factors
changed since your `list`: list again, check the change, and retry with the
new version.

**"factor set of user N contended" / `ABORTED`.** Many changes to one
user's factors landed while yours was committing. Nothing was enrolled; retry.

**A conditional add fails with an internal error about an atomic store.**
The node's store has no guarded writes, so it cannot decide a condition
safely and refuses rather than guessing. Unconditional adds still work.

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
