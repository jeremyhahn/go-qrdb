# Distributed Locks — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

A distributed lock lets one process at a time into a critical section across
machines. Each acquisition comes with a **fencing token** — a number that only
goes up — and using it is what separates a safe lock from a hopeful one.

## Prerequisites

A reachable server, client certificates, an unsealed barrier, and the `locks:*`
permissions for the verbs you use.

## First run

```bash
qrdb lock trylock jobs/nightly-report --ttl 300 --output json
```

If you got a result, you hold the lock. Note `fencing_token`. When you are done:

```bash
qrdb lock release jobs/nightly-report --token 4711
```

## Common tasks

**Take the lock or give up immediately.** This is the right shape for a
scheduled job that should simply not run twice.

```bash
qrdb lock trylock jobs/nightly-report --ttl 300 || exit 0
```

**Wait for the lock.**

```bash
qrdb lock acquire jobs/nightly-report --ttl 300 --wait 60
```

**Always release, even on failure.**

```bash
KEY=jobs/nightly-report
OUT=$(qrdb lock trylock "$KEY" --ttl 300 --output json) || exit 0
TOKEN=$(echo "$OUT" | jq -r .fencing_token)
trap 'qrdb lock release "$KEY" --token "$TOKEN"' EXIT
```

**Hold a lock longer than its TTL.** Extend it on an interval comfortably inside
the TTL — a third is a reasonable rule.

```bash
while sleep 100; do qrdb lock extend "$KEY" --token "$TOKEN" || break; done &
```

If `extend` starts failing, you have lost the lock. Stop what you are doing.

**Use the fencing token where it counts.** Pass it to whatever the lock is
protecting, and have that thing remember the highest token it has accepted and
refuse anything lower:

```bash
run-the-report --fence "$TOKEN"
```

This is the step people skip, and it is the one that actually prevents
split-brain. Without it, a process that stalls past its TTL and then wakes up
will happily write over the new holder's work.

**Re-enter a lock you already hold.** With `--reentrant`, the same holder can
take the lock again; the count goes up and you must release once per
acquisition.

```bash
qrdb lock acquire cfg/rewrite --ttl 60 --holder worker-7 --reentrant
qrdb lock acquire cfg/rewrite --ttl 60 --holder worker-7 --reentrant   # count = 2
qrdb lock release cfg/rewrite --token <t2>                             # count = 1
qrdb lock release cfg/rewrite --token <t1>                             # released
```

**See who holds what.**

```bash
qrdb lock status jobs/nightly-report
qrdb lock list --prefix jobs/
```

## Troubleshooting

**`trylock` returns nothing / non-zero.** Somebody else holds the lock. That is
the answer, not an error. `qrdb lock status <key>` shows who.

**Release or extend fails with "not lock holder".** Your fencing token is stale:
your TTL expired, the lock was taken by someone else, and your token no longer
matches. Stop writing to the protected resource. Do not retry the release — the
refusal is what prevented you from releasing a lock you no longer own.

**The lock is stuck and nobody holds it.** Wait for the TTL. A crashed holder's
lock is released automatically when its lease expires; there is no manual
break-lock, deliberately.

**Your job ran twice anyway.** Almost always the fencing token is not being
checked downstream. The lock narrows the window; only the resource checking the
token closes it. Look for a stalled holder — long GC pause, descheduled
container, network partition — that outlived its TTL.

**`acquire` times out.** Raise `--wait`, or use `trylock` and skip the run. Note
that `--wait 0` falls back to the global `--timeout`.

**Locks do not show up in `qrdb kv list`.** By design — lock records live under
the internal prefix `__sys:lock/`, so they can never collide with your keys.
Use `qrdb lock list`.

**How long should the TTL be?** Long enough to cover the worst realistic pause
of your critical section, and no longer — the TTL is also how long a crashed
holder blocks everyone else. If the section can genuinely run long, use a
shorter TTL with `lock extend` on a timer instead of one enormous TTL.
