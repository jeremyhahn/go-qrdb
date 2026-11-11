# Credentials — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-11-01
> Conflicts with this document are defects. Fix them here first.

Named secrets the engine itself needs, with version history and a rotation
grace period.

**This is not an application secret store.** It holds the handful of
operational and boot secrets qrdb needs for itself, and each tenant has its own
store for the same kind of secret. Application secrets belong to the KMS one
layer up.

## Prerequisites

A reachable server, client certificates, and the `credentials:*` permissions.
Under the `barrier` strategy the barrier must be unsealed.

## Which strategy are you on?

```bash
qrdb credential strategy
```

| Strategy | What it means for you |
|---|---|
| `barrier` (the default) | values are encrypted and stored once for the whole cluster, so any node returns the same value, policy and versions; they survive restarts; they are unreadable while the barrier is sealed |
| `manual` | values live only in memory; **a read blocks until somebody submits the value**; a restart means submitting again |

## First run

```bash
qrdb credential store smtp-password --value "$SMTP_PASS"
qrdb credential retrieve smtp-password
qrdb credential list
```

`list` shows names only. It never shows values.

`retrieve` prints the value on standard output, so it is in your terminal
scrollback or your pipe from that point; that is the one place the value leaves
protected memory on your side. Inside a Go program, use the SDK's
`CredentialRetrieveBuffer` and `Destroy` the returned buffer when you are done
with it.

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

**Rotate on a schedule.** Give the credential a rotation policy and the
cluster rotates it for you, once per interval — not once per node:

```bash
qrdb credential rotation-policy set smtp-password --enabled --interval 720h --retain-versions 3
qrdb credential rotation-policy get smtp-password
```

`get` shows `enabled`, `interval`, `retain-versions` and the next scheduled
rotation. `--retain-versions 3` keeps the current value and two predecessors;
older versions are pruned after each scheduled rotation. Run
`rotation-policy set <name>` with no flags to stop scheduled rotation. Each
node checks for due credentials every `seal.credential_rotation_check_period`
(default `1m`), so a rotation lands within one check period of its due time.
Setting or reading a policy needs the `credentials:rotate` permission.

The new value comes from a rotation value source that the application embedding
qrdb supplies. **`qrdbd` ships none**: on a stock daemon you can set a policy,
but every due rotation fails and is logged until a source is wired in. Keep
rotating by hand there.

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

**Remove it entirely.** This wipes the current value and the *entire* version
history — including a version still inside its grace period — not just the
value `retrieve` was showing you.

```bash
qrdb credential delete smtp-password
```

**Supply a secret that lives nowhere.** Under `manual`, a read blocks until
somebody provides the value — that is how an officer hands a boot secret to a
starting system:

```bash
qrdb credential submit ceremony-seed --value "$SEED"
```

**Settle what the upgrade could not merge.** Older releases kept a copy of
each credential on every node. On its first start after the upgrade, each
node moves its copies into the cluster's one store: the first node to move a
credential wins, and a later node whose copy is identical just drops its own.
A node whose copy differs keeps it and reports a migration conflict (a
warning in its log and a failed `migrate` audit event). Ask each node in turn:

```bash
qrdb credential migration-conflicts list
```

Each line names a credential and which parts of the node's copy differ from
the cluster's: the current value, the version history or the rotation policy.
For each one, decide which value is right, then on that node either import
the node's value as the credential's new version (the cluster's value stays
as the previous version) or discard the node's copy:

```bash
qrdb credential migration-conflicts resolve smtp-password --resolution import
qrdb credential migration-conflicts resolve api-token --resolution discard
```

Either way the node's old copy is deleted, so the conflict is gone from the
list. Importing carries over only the current value; the node's old version
history and policy are dropped, and the audit event names what was dropped.
Listing needs `credentials:list`; resolving needs `credentials:rotate`.

## Credentials in a tenant

Every tenant has its own credential store. Sign in as a principal of the tenant
and the same commands work on that tenant's store, never on another tenant's
or on the platform's:

```bash
qrdb credential store smtp-password --value "$ACME_SMTP_PASS"
qrdb credential list
```

The same name can hold a different value in every tenant. A tenant
administrator can grant the `credentials:*` permissions to roles inside the
tenant. A tenant's store always uses the `barrier` strategy, over the tenant's
own barrier, so the tenant's barrier must be unsealed.

A tenant needs a dedicated barrier to have a store: onboard it with `barrier`,
`volume` or `raft` isolation. A tenant on `prefix` isolation shares the system
barrier, and its credential calls are refused.

## Referencing credentials from configuration

An application that embeds qrdb can point a field of its own configuration at a
credential rather than embedding the value; `qrdbd`'s own configuration has no
such field:

```yaml
smtp_password: "credential://smtp-password"
```

At the application's boot these resolve into protected in-memory values, not plain strings. A
field marked protected **must** reference a secret; if the reference cannot be
resolved, startup fails rather than continuing with nothing. `literal://` gives
an inline value to a field that is not protected; a protected field refuses it.

## Troubleshooting

**A retrieve is hanging.** You are on the `manual` strategy and nobody has
submitted the value. Run `qrdb credential submit <name> --value ...` from
another session. It blocks rather than returning empty on purpose — an empty
value would let the boot continue with no credential at all.

**`rotation-policy set` is refused with invalid argument.** An enabled policy
needs a positive `--interval` written as a duration (`720h`, not `30d`) and
`--retain-versions` of at least 1. The `manual` strategy refuses every policy:
the server cannot mint a value an officer has to submit.

**A scheduled rotation never happens.** The server log shows
`credential rotation scheduler: rotation failed` with
`credentials: no rotation value source configured`. The server has no rotation
value source; `qrdbd` ships none. Rotate by hand with `qrdb credential rotate`.
Until a rotation succeeds, the credential stays due and is retried every check
period.

**`migration-conflicts list` shows nothing on a node that logged a
conflict.** You are talking to a different node: the list is per node. Point
the CLI at the node that logged it. If the node's own store was still sealed
when it started, the migration ran again on the first credential operation or
list call after that.

**Everything fails with a barrier error.** The `barrier` strategy needs the
barrier unsealed. See [../barrier-seal/user.md](../barrier-seal/user.md).

**A tenant's credential call fails with "no dedicated barrier".** The tenant is
on `prefix` isolation, on the shared system barrier. Credentials need a tenant
with its own barrier; there is no tenant prefix inside the platform's store.

**A tenant's credential call fails with a sealed-barrier error.** The tenant's
own barrier is sealed. Unseal it; the platform's barrier being unsealed is not
enough.

**A version you wanted is gone.** Its grace period elapsed, or a rotation with
`--grace-period 0` wiped it. There is no recovery — the point of the wipe is
that the old value stops existing.

**Your credentials vanished after a restart.** You are on `manual`. Values live
only in memory for the life of the process.

**You cannot find a credential in `qrdb kv list`.** Credentials are never in the user
KV store. They are a separate store, distinct from both the data plane and the
barrier's key custody.

**You want to store application secrets here.** Do not. This is the engine's own
minimal custody. Application secrets — with per-key authorization,
key-use audit and rotation policy — are the KMS at the services layer, which
composes above this.

**You are looking for PIN management.** Different subsystem: PINs need lockout
semantics credentials do not have. See
[../auth-factors/user.md](../auth-factors/user.md).
