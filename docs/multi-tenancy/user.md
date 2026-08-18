# Multi-Tenancy — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Running several customers on one deployment, with as much separation as each
one needs.

## Prerequisites

A reachable server, client certificates, and the `tenants:*` permissions. For
anything above `prefix` isolation you also need `storage.volume_pools`
configured on the server.

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
