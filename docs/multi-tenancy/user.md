# Multi-Tenancy — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-29

Running several customers on one deployment, with as much separation as each
one needs.

## Prerequisites

A reachable server, client certificates, and the `tenants:*` permissions. For
anything above `prefix` isolation you also need `storage.volume_pools`
configured on the server.

**Creating tenants and setting their plans is the platform operator's job.**
`tenant create`, `set-entitlement`, `update-quotas` and any change of isolation
type need a certificate for an identity in the `system` tenant. A tenant's own
Admin manages everything inside its tenant, can read, rename and delete it, and sees
only its own tenant in `tenant list` -- but it cannot create tenants or change
its own limits. See [api.md](api.md#who-may-call-what).

## Pick the right rung

| Mode | What the tenant gets | Cost |
|---|---|---|
| `prefix` (default) | its keys prefixed on the shared store | none — **but this is logical separation, not encryption** |
| `barrier` | its own key over its IAM data, on its own Pebble store | a volume from the pool |
| `volume` | its own filesystem root and its own Raft host; **everything** encrypted under its key, including the WAL | a volume, a host per tenant |
| `raft` | a dedicated Raft group per database, replicated across nodes | volumes, Raft groups, replicas |

**Say `prefix` out loud to anyone who assumes tenants are encrypted apart.**
It is a key prefix. Everything else on that rung shares the system barrier.

## Creating tenants

```bash
qrdb tenant create --id acme --name "Acme Corp"

qrdb tenant create --id secure-co --name "Secure Co" --isolation-type volume

qrdb tenant create --id big-co --name "Big Co" --isolation-type raft \
     --data-root /mnt/tenant-disks/big-co
```

`--data-root` pins the tenant to a specific disk instead of taking a volume from
the pool. The directory must be empty or not exist yet.

**Isolation cannot change once the tenant holds data.** Get this right at
creation, or migrate the data.

## Granting the plan

Nothing above `prefix` is available until you grant it. Entitlements are the
plan; quotas are the size.

```bash
qrdb tenant set-entitlement acme \
     --isolation-modes prefix,volume,raft \
     --allow-dedicated-raft --allow-volume-storage \
     --allowed-barrier-modes passphrase,shamir

qrdb tenant update-quotas acme \
     --max-databases 20 --max-storage-bytes 107374182400 --max-total-replicas 9
```

`-1` means unlimited; `0` means use the system default.

Entitlements are checked **before** quotas, so a tenant with no entitlement for
a tier is refused without the counters being consulted at all.

## The tenant's own barrier

Only for `barrier`, `volume` and `raft` tenants.

```bash
qrdb tenant barrier-init --id secure-co --strategy passphrase --passphrase "$PP"
qrdb tenant barrier-status --id secure-co
qrdb tenant barrier-unseal --id secure-co --key "$KEY_B64"
qrdb tenant barrier-seal   --id secure-co
```

With M-of-N:

```bash
qrdb tenant barrier-init --id secure-co --strategy shamir --threshold 3 --shares 5
```

Officers register and claim through `qrdb tenant barrier-officer ...`, mirroring
the system barrier's flow with `--tenant-id` added — see
[../shamir-custody/user.md](../shamir-custody/user.md).

**Self-managed is the default and it is the secure one.** `--barrier-auto-unseal`
at creation wraps the tenant's key under the *system* barrier so the node can
unseal without the tenant present. Convenient; strictly weaker; entitlement-gated
for that reason.

## Working as a tenant

You do not pass a tenant flag. **The tenant comes from your certificate's OU.**

```bash
qrdb ca issue --username alice --tenant-id acme --out ./alice
qrdb --cert-file alice/cert.pem --key-file alice/key.pem kv list app/
```

## Troubleshooting

**"was deleted and cannot be reused" when creating a tenant.** That id has
been used and deleted. It is retired permanently — pick another name. The
server cannot tell whether you are the same customer as the one that held the
name before, and guessing wrong would hand you their data, so it does not
guess. The message includes when the id was retired.

**"id system is reserved" when deleting a tenant.** The platform's own tenant
and the other reserved names (`default`, anything starting with `_`, anything
containing `:`) cannot be deleted. Nothing was changed when you saw that
message — no key was destroyed and no record was removed.

**"still owns N database(s)" when deleting a tenant.** Teardown is bottom-up:
drop the tenant's databases first, then delete the tenant. The error names
them. Each database's own drop reclaims that database's data, which is what
keeps the tenant's delete quick.

**A tenant delete failed part-way.** Run it again. The teardown is idempotent
and resumes; by the time anything can fail, the id is already retired, so
nothing can attach to what is left. A delete against an id that was never used
is still "not found".

**A `volume` or `raft` tenant's directory after a delete.** It is removed,
along with the key that protected it and the records that would have restored
the barrier at the next restart. If the directory is still there on the node
that ran the delete, the delete reported an error — re-run it.

**A deleted tenant's directory on ANOTHER node.** A node that was down during
the delete, or held the tenant without hosting a shard, still has it. It
removes it by itself the next time it starts. To reclaim the disk without a
restart, run the reconcile against that node:

```bash
qrdb --server node-2.db.example.com tenant reconcile-deleted
```

It removes only directories of deleted tenants and never one a live tenant
uses; anything it left alone is listed with the reason, and logged on the
node. A deleted tenant's disk that is a mount point is emptied; unmount it
yourself. If it fails for one tenant it still reclaims the others and then
reports every failure; run it again once the cause is fixed.

**"overlaps retired tenant ... run `qrdb tenant reconcile-deleted`" on
create.** The directory you asked for still belongs to a deleted tenant this
node has not cleaned up yet. Run the reconcile against that node, then create
again.

**Which tenant names can never be used again?** List them, with when each was
deleted:

```bash
qrdb tenant list-deleted
```

**"tenant has data" when changing isolation.** Isolation is fixed once data
exists. Migrate the data to a new tenant, or delete it first. Note `update` only
accepts `barrier` and `prefix` in any case.

**An entitlement error on something you set a quota for.** Quotas size a tier;
entitlements grant it. Set the entitlement first — the check runs before the
quota, so the quota you set is never even consulted.

**"volume pool not configured on barrier registry".** The `barrier`, `volume`
and `raft` tiers allocate storage from `storage.volume_pools`. Configure it on
the server.

**A hardware seal strategy was refused.** `allowed_barrier_modes` empty means
software defaults with hardware **fail-closed**. Grant it explicitly.

**Barrier commands fail on a tenant.** It is probably a `prefix` tenant, which
has no barrier of its own — it rides the system barrier.

**Storage usage looks wrong for a `volume` tenant.** A background reconciler
walks each volume tenant's `DataRoot` on an interval (default 5 minutes) to
correct drift. Give it a cycle.

**One tenant's replicas did not come back after a node restart.** They should:
placement is persisted and each node re-attaches its replicas at boot. Check the
placement table and the node's health.

**You are worried a client could claim another tenant's name.** With the tenant
service configured, it cannot — identity comes from the certificate and unknown
tenants are rejected. **Without a tenant service wired**, that rejection is
skipped and any tenant name from an authenticated client is accepted. That mode
exists for trusted single-tenant embedding only. **A production multi-tenant
deployment must configure the tenant service.**

**You want the tenant's raft WAL encrypted under its own key.** For `volume`
tenants it already is. For `raft` tenants the raft log lives in the shared host
LogDB under the *system* barrier; a dedicated per-tenant WAL is **designed but
not implemented**.
