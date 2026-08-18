# Credentials — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Named secrets the engine itself needs, with version history and a rotation
grace period.

**This is not an application secret store.** It holds the handful of
operational and boot secrets qrdb needs for itself. Application and tenant
secrets belong to the KMS one layer up.

## Prerequisites

A reachable server, client certificates, and the `credentials:*` permissions.
Under the `barrier` strategy the barrier must be unsealed.

## Which strategy are you on?

```bash
qrdb credential strategy
```

| Strategy | What it means for you |
|---|---|
| `barrier` (production default) | values are encrypted and stored; they survive restarts; they are unreadable while the barrier is sealed |
| `manual` | values live only in memory; **a read blocks until somebody submits the value**; a restart means submitting again |

## First run

```bash
qrdb credential store smtp-password --value "$SMTP_PASS"
qrdb credential retrieve smtp-password
qrdb credential list
```

`list` shows names only. It never shows values.

## Common tasks

**Rotate without breaking your consumers.** Give the old value a grace period
so a fleet can roll over without a synchronized restart.

```bash
qrdb credential rotate smtp-password --value "$NEW" --grace-period 3600
```

For an hour, both the new and the previous value are retrievable. After that,
the old one is gone.

**Rotate immediately** — the old value is wiped as soon as the call returns:

```bash
qrdb credential rotate smtp-password --value "$NEW"
```

**Look at the history.**

```bash
qrdb credential versions smtp-password --output json
qrdb credential retrieve-version smtp-password --version 3
```

`--version 0` means the current version.

**Tidy up.** GC reaps only versions whose grace period has elapsed; the current
one is never touched.

```bash
qrdb credential gc smtp-password
```

**Remove it entirely.**

```bash
qrdb credential delete smtp-password
```

**Supply a secret that lives nowhere.** Under `manual`, a read blocks until
somebody provides the value — that is how an officer hands a boot secret to a
starting system:

```bash
qrdb credential submit ceremony-seed --value "$SEED"
```

## Referencing credentials from configuration

Configuration fields can point at a credential rather than embedding the value:

```yaml
some_secret_field: "credential://smtp-password"
```

At boot these resolve into protected in-memory values, not plain strings. A
field marked protected **must** reference a secret; if the reference cannot be
resolved, startup fails rather than continuing with nothing. `literal://` exists
for the cases where an inline value is genuinely intended.

## Troubleshooting

**A retrieve is hanging.** You are on the `manual` strategy and nobody has
submitted the value. Run `qrdb credential submit <name> --value ...` from
another session. It blocks rather than returning empty on purpose — an empty
value would let the boot continue with no credential at all.

**Everything fails with a barrier error.** The `barrier` strategy needs the
barrier unsealed. See [../barrier-seal/user.md](../barrier-seal/user.md).

**A version you wanted is gone.** Its grace period elapsed, or a rotation with
`--grace-period 0` wiped it. There is no recovery — the point of the wipe is
that the old value stops existing.

**Your credentials vanished after a restart.** You are on `manual`. Values live
only in memory for the life of the process.

**You cannot find a credential in `kv list`.** Credentials are never in the user
KV store. They are a separate store, distinct from both the data plane and the
barrier's key custody.

**You want to store application secrets here.** Do not. This is the engine's own
minimal custody. Application and tenant secrets — with per-key authorization,
key-use audit and rotation policy — are the KMS at the services layer, which
composes above this.

**You are looking for PIN management.** Different subsystem: PINs need lockout
semantics credentials do not have. See
[../auth-factors/user.md](../auth-factors/user.md).
