# Leases — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-13

A lease is a timer with a name. Attach keys to it, keep it alive while you care
about them, and the server deletes them all when you stop.

## Prerequisites

A reachable server, client certificates, an unsealed barrier, and the `lease:*`
permissions for the verbs you use. Attaching a key also needs
`mvcc:put_with_lease`.

## First run

```bash
# Create a 60-second lease
qrdb lease grant --ttl 60
```

Note the `id` it returns. Hang a key off it:

```bash
qrdb kv put session/abc '{"user":"alice"}' --lease-id 7
```

Wait 60 seconds without renewing, and the key is gone:

```bash
qrdb kv get session/abc      # not found
```

## Common tasks

**Script it end to end.**

```bash
LEASE=$(qrdb lease grant --ttl 60 --output json | jq -r .id)
qrdb kv put session/abc '{"user":"alice"}' --lease-id "$LEASE"
qrdb kv put session/abc/meta 'ua=curl'     --lease-id "$LEASE"
```

Both keys share one lifetime. Revoke the lease and both disappear together.

**Keep it alive while your process is running.** Renew well before the TTL —
half the TTL is a good default, so one missed renewal is not fatal.

```bash
while sleep 25; do qrdb lease keep-alive "$LEASE" >/dev/null || break; done &
```

**Move a key to a new lease, or make it permanent.** Write it again. A key
belongs to the lease its latest write named, so re-writing it under another
lease moves it there, and re-writing it without `--lease-id` makes it permanent.
The old lease's revoke or expiry no longer touches it:

```bash
OLD=$(qrdb lease grant --ttl 30 --output json | jq -r .id)
qrdb kv put registry/web-1 'addr=192.0.2.5' --lease-id "$OLD"

# The service restarts and re-registers before the old lease runs out.
NEW=$(qrdb lease grant --ttl 30 --output json | jq -r .id)
qrdb kv put registry/web-1 'addr=192.0.2.6' --lease-id "$NEW"

qrdb lease revoke "$OLD"
qrdb kv get registry/web-1   # addr=192.0.2.6, still there
```

A compare-and-swap is different. It edits the value and keeps whatever lease
the key already had.

**Check how long you have left.**

```bash
qrdb lease ttl "$LEASE" --output json
```

`granted_ttl` is what you asked for; `remaining_ttl` is what is left.

**Tear down deliberately.**

```bash
qrdb lease revoke "$LEASE"
```

This deletes every attached key immediately. It is the clean-shutdown path for a
session or a service registration.

**See what is outstanding.**

```bash
qrdb lease list
```

**React when a lease expires.** The cascade delete shows up as ordinary delete
events, so a watcher learns about it the normal way:

```bash
qrdb watch session/ --prefix
```

## Troubleshooting

**Your keys vanished and you did not revoke anything.** The keep-alive stopped —
a crashed renewer, a network partition longer than the TTL, or a renewal
interval too close to the TTL. Lengthen the TTL, renew more often, or both. This
is the feature working as designed: expiry is what leases are for.

**The lease outlived its TTL after a failover.** The cluster leader renews
leases in its own memory, and every `lease.checkpoint_interval` (default `5m`)
records what each lease longer than that has left. When the leader changes, the
new leader counts every lease from that record — or from its full TTL when it
has none — plus `lease.failover_grace` (default `5s`) from the moment it takes
over. A lease renewed since its record was taken was renewed through the log,
which removed the record, so the new leader never gives a lease less than its
holder was promised; an abandoned lease can outlive its TTL by up to the grace
plus the time since its last record. It still expires, and deletes its keys,
once that has run out. Lower `lease.failover_grace` to reap an abandoned lease
sooner; keep it longer than leader election plus one renewal interval, or
holders can lose a lease they were renewing.

**`lease ttl` fails with "not leader" or `Unavailable`.** Only the leader's
countdown decides expiry, and in-memory renewals reach the leader alone, so a
node other than the leader does not answer the remaining TTL from its own
figure: it forwards the call to the leader. It fails instead when it cannot
name a leader — during an election — or on the etcd listener, which does not
forward. Retry, or send the call to another node.

**"Reading the key should have kept it alive."** It does not. Only
`lease keep-alive` renews. Renewal on access would tie expiry to traffic rather
than to whether the owner is still alive.

**Writing with `--lease-id` was refused.** The lease does not exist — it expired
or was revoked between your grant and your write. The write is refused rather
than accepted lease-less, because a key silently written without its lease would
never expire. Grant a new lease and retry.

**A key you re-wrote disappeared when an older lease expired.** On current
releases that cannot happen: re-writing a key moves it out of its old lease.
Releases before the correction kept the key in every lease any of its values had
named, so the oldest lease's expiry deleted the newest value. Upgrade.

**Leases disappeared after a node restarted.** Leases are persisted and survive
restarts and snapshot recovery, and each one restarts on a full TTL. Leases
granted by a release that did not persist them are lost on the restart into the
upgrade. Grant new ones and re-write their keys.

**`lease keep-alive` returns not-found.** The lease already expired; its keys are
already gone. Grant a new one and re-create the state.

**A lease ID from one database does not work in another.** Leases are scoped per
(tenant, database), like every other data-plane object. Check `--database`.

**You want a TTL shorter than a second.** Not available — TTLs are whole
seconds, and finer granularity would not be honoured by the expiry sweep anyway.
For sub-second coordination use [../distributed-locks/](../distributed-locks/),
which is built on leases but gives you a fencing token to reason with.
