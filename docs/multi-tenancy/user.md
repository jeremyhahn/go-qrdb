# Multi-Tenancy — User Guide

> **Status: NORMATIVE** (the strict-isolation section is RULED, not yet
> implemented) · Scope: go-qrdb · Last verified: 2026-10-05
> Conflicts with this document are defects. Fix them here first.

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

A tenant is created **with its first admin**, or not at all. The admin keeps its
own private key: it sends you a certificate signing request, never a key, and
you never receive one back.

```bash
# the tenant's admin, on their own machine — only csr.pem leaves it:
qrdb ca csr --username alice --out ./alice

# you, the platform operator:
qrdb tenant create --id acme --name "Acme Corp" \
     --admin-username alice --admin-csr alice/csr.pem --out ./acme

qrdb tenant create --id secure-co --name "Secure Co" --isolation-type volume \
     --admin-username alice --admin-csr alice/csr.pem

qrdb tenant create --id big-co --name "Big Co" --isolation-type raft \
     --admin-username alice --admin-csr alice/csr.pem
```

A `volume` or `raft` tenant takes a directory from the volume pool. Every node
keeps that tenant in its own pool, at `{pool path}/{tenant}`: the cluster
records which pool and slot the tenant got, and each node maps that to its own
disk. `--data-root` is retired and refused -- a path names one node's disk, and
a dedicated disk is configured as a volume pool on every node instead.

A tenant created by an older release may still have a path in its record. Such
a tenant is **refused**: `tenant list` still shows it beside every other
tenant, but no node opens its data, and every other tenant is served as usual.
Delete it with `qrdb tenant delete` -- the delete removes its records and
touches no directory -- and create it again. Remove the old directory yourself
once you no longer need it.

For a `prefix` tenant the CSR is signed by the tenant's own CA. For a
`barrier`, `volume` or `raft` tenant it is signed by the cluster CA, because
the tenant's own CA sits behind a barrier that stays sealed until the admin
initializes it. Either way the certificate logs in only to this tenant. A
node that serves your own TLS certificates (`--tls-cert-file`) has no
cluster CA: it signs a `--barrier-auto-unseal` tenant's CSR with the
tenant's CA, but refuses a self-managed one's before creating anything. For
that tenant, give the admin's own certificate with `--admin-cert` and its
issuer with `--trust-anchors`. Send
the admin the certificate
(`acme/admin-cert.pem` with `--out`); with the key they already hold, it is
their login.

**If the admin's organisation has its own CA**, pass their certificate instead
of a CSR, with the CA certificates it chains to. Those become the tenant's
trust anchors ([../trust-anchors/user.md](../trust-anchors/user.md)):

```bash
qrdb tenant create --id globex --name "Globex" \
     --admin-username ops --admin-cert ops.pem --admin-chain ops-chain.pem \
     --trust-anchors globex-root.pem
```

The certificate's CN must be the admin username and it must name the new
tenant.

**Everything is checked before anything is written.** A bad CSR, anchors that
are not CA certificates, or a certificate that does not chain or names someone
else is refused with no tenant created, and you can retry with the same id. A
failure after that point is undone, but undoing a tenant deletes it, which
retires the id for good — pick a new one.

**For `barrier`, `volume` and `raft` tenants the admin's account comes later.**
Their accounts live behind the tenant's own barrier, which does not exist yet.
`tenant create` reports that the admin's account is created when the admin
runs `qrdb tenant barrier-init` — and only the certificate you just issued may
run it. A `prefix` tenant's admin can log in straight away.

**Isolation cannot change once the tenant holds data.** Get this right at
creation, or migrate the data.

## Describing a tenant

A tenant can carry a free-text description and a small map of `key=value`
metadata — an owner, an environment, a billing reference. qrdb stores them and
returns them; nothing in qrdb reads them, so they grant nothing and restrict
nothing.

```bash
qrdb tenant update acme --description "Acme production" \
     --metadata env=prod --metadata owner=platform-team
qrdb tenant get acme
```

`--metadata` sets the **whole** map: name every entry you want to keep.
Leave a flag out and that field is unchanged; `--description ""` and
`--clear-metadata` empty them. A description is at most 1024 bytes; metadata is
at most 64 entries, keys at most 128 bytes and values at most 1024 bytes. A
value over a limit is refused and nothing changes. A tenant's own Admin can
set both on its tenant.

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

Only for `barrier`, `volume` and `raft` tenants. The tenant's first admin runs
`barrier-init` with the certificate `tenant create` issued and its own key;
that also creates the admin's account. The CLI proves it holds the key: it
signs a one-time challenge from the node, tied to this TLS connection, with
the `--key-file` key. The key never leaves your machine.

```bash
qrdb --cert-file alice/cert.pem --key-file alice/key.pem \
     tenant barrier-init --id secure-co --strategy passphrase --passphrase "$PP"
qrdb tenant barrier-status --id secure-co
qrdb tenant barrier-unseal --id secure-co --key "$KEY_B64"
qrdb tenant barrier-seal   --id secure-co
```

With M-of-N, name the initial share holders inline (the barrier's own IAM
isn't readable until it exists, so standalone officer registration can't
reach it beforehand):

```bash
qrdb tenant barrier-init --id secure-co --strategy shamir --threshold 2 --shares 2 \
     --officers alice=alice.csr --custodians bob=bob.csr
```

Both officers are validated together and enrolled atomically with
initialization — a problem with either one refuses the whole call, and a
failure partway through rolls back any officer already registered, so a
corrected retry never collides with a half-finished attempt.

Later officers register and claim through `qrdb tenant barrier-officer ...`,
mirroring the system barrier's flow with `--tenant-id` added — see
[../shamir-custody/user.md](../shamir-custody/user.md).

**Self-managed is the default and it is the secure one.** `--barrier-auto-unseal`
at creation wraps the tenant's key under the *system* barrier so the node can
unseal without the tenant present. Convenient; strictly weaker; entitlement-gated
for that reason.

## Working as a tenant

You do not pass a tenant flag. **The tenant comes from your certificate's OU.**

```bash
qrdb --cert-file alice/cert.pem --key-file alice/key.pem kv list app/
```

The tenant's Admin issues further users' certificates itself, into its own
tenant only — see [../certificate-authority/user.md](../certificate-authority/user.md).

## Troubleshooting

**`tenant create` says the admin username or credential is missing.** Every
tenant is created with its first admin: pass `--admin-username` and exactly one
of `--admin-csr` or `--admin-cert`.

**`tenant create` refused the admin's certificate.** It must chain to one of the
CA certificates in `--trust-anchors` (with `--admin-chain` supplying any
intermediates), its CN must be the admin username, and it must name the new
tenant. No tenant was created; fix it and retry with the same id.

**The admin's `barrier-init` was refused.** On a tenant created with its first
admin, only the certificate `tenant create` issued or enrolled for that admin
may initialize the barrier, presented by that admin. Under
`auth.mtls_binding_policy: required`, a tenant that has no onboarded admin
cannot have its barrier initialized by any certificate.

**"barrier init proof ... refused".** The node asked for proof that you hold
the first admin's key and did not get a valid one. Pass that admin's own key
with `--key-file`, beside its certificate. Each challenge is used once and
lasts five minutes, so simply rerun the command; nothing was initialized.
"needs the first admin's key to prove possession, and no signer was given"
means no `--key-file` was given. The node needs a TLS
1.3 connection to ask for the proof.

**"was deleted and cannot be reused" when creating a tenant.** That id has
been used and deleted. It is retired permanently — pick another name. The
server cannot tell whether you are the same customer as the one that held the
name before, and guessing wrong would hand you their data, so it does not
guess. The message includes when the id was retired.

**"id system is reserved" when deleting a tenant.** The platform's own tenant
and the other reserved names (`default`, `custodian`, `security-officer`, anything starting with `_`, anything
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

## Strict isolation — RULED

> **Status: ruled — partially implemented.**
> Design: [architecture.md](architecture.md#strict-tenant-isolation--design).
> Correction (2026-10-05): creating a tenant with its first admin, and
> restricting barrier init to that admin's certificate, already shipped for
> every tenant on 2026-10-03 — see [Creating tenants](#creating-tenants).
>
> **Correction (2026-10-06):** the floor and tenant setting described below
> now exist and tighten-only as described, and nobody — the system tenant
> included — can reach another tenant's `ScopeSelf` data or IAM records any
> more, regardless of the setting. What is **not** true yet: no CLI or API
> surface can read or set either setting (the Go service API is ready for it,
> nothing is wired above it), a cluster that never touches the floor reads it
> as `operator-access` rather than the `strict` default described below
> (nothing performs the "first-boot, recorded once" write that default
> assumes), and moving a strict tenant's records into a home store of its own
> — the heart of "what a strict tenant gets" below — has not started.
>
> **Per-node barrier keys (2026-10-08).** Each node serving an isolated tenant
> initialises its own key for the tenant's barrier from a signed init
> statement; no key is carried between nodes.
>
> - **Shipped for a `raft` tenant whose key auto-unseals.** The platform
>   signs the statement and every node of the tenant's groups initialises its
>   own key, so `qrdb tenant barrier-status` on two nodes shows different key
>   fingerprints. A node added to the tenant's group shows the barrier as
>   sealed and uninitialised until it has initialised its own key.
> - **Not yet for a tenant whose admin holds the key.** Its key is still
>   delivered to the other serving nodes.
> - **A single-node cluster run without a cluster certificate** cannot create
>   a `raft` tenant whose key auto-unseals yet: the create is refused because
>   the node has nothing to sign the statement with.
>
> On a cluster, creating a `barrier`, `volume` or `raft` tenant, or changing a
> tenant to one of those types, is refused as unavailable until every node
> runs a release with the `per-node-barrier-keys` feature, and succeeds once a
> retry finds it active. A standalone node is never refused.
>
> **Choosing a type for confidentiality.** A `prefix` tenant lives in the
> shared store, under a barrier the platform manages: **its data is readable
> by the platform.** A tenant that needs its data kept from the platform
> operator chooses an isolated type (`barrier`, `volume` or `raft`). Whether
> an isolated tenant's key auto-unseals is the tenant's own choice, never
> refused; once per-node keys are complete, its barrier status shows which it
> chose.
>
> **Correction (2026-10-07):** the cluster floor can now be set at start with
> the configuration key `tenant.isolation_policy` (`strict` or
> `operator-access`; environment `QRDB_TENANT_ISOLATION_POLICY`). It is
> applied on every start and only ever tightens: a value looser than the
> stored floor is logged and ignored. Left unset, nothing is applied.
>
> ```yaml
> tenant:
>   isolation_policy: strict
> ```
>
> **Correction (2026-10-07, revised the same day):** every isolated tenant —
> `barrier`, `volume` or `raft`, under either policy — now gets a home store
> of its own. On a cluster it is a group with a replica on every node; on a
> standalone server it is a store under the tenant's own barrier. Its users,
> roles, role bindings, data-key keyring, invitations, CA and the certificates
> its CA issues live there and not in the platform's store, and its admin
> signs in through any node. The policy decides only what an operator may do,
> so a tenant may be made strict, or the floor raised to `strict`, at any
> time. Creating an isolated tenant on a cluster waits for that group to
> start, so `tenant create` can take up to 25 seconds. A node that has just
> joined answers a tenant's requests with a retryable "unavailable" until its
> copy has caught up; clients retry on another node. The tenant's custodians,
> audit trail and database catalog have not moved yet.

- The tenant keeps all of its own records in its own store: users, roles,
  factors, certificates, its CA, its database catalog, its audit trail and its
  keys.
- Those records live only on the nodes that serve the tenant. Each of those
  nodes encrypts them under a key that exists on that node alone.
- The platform keeps a short registry entry: the tenant's name, tier, plan,
  where it runs, and whether it is sealed.
- The platform operator cannot read or change any of the tenant's records.
- Nobody outside the tenant can reach its data.

A strict tenant always has storage of its own, so it must be on the `barrier`,
`volume` or `raft` tier; `prefix` is not available. The server therefore needs
`storage.volume_pools`.

**What changes on an `operator-access` cluster.** Operators who hold the
`tenant_identity` permissions can read and administer users, roles and factors
in any tenant that has not opted into strict. Every such action appears in
that tenant's own audit trail. Operators still cannot reach any tenant's
databases.

To see which policy applies:
- `qrdb cluster isolation-policy` shows the cluster floor;
- `qrdb tenant get <id>` shows a tenant's effective policy.

A policy only ever tightens.

**Bringing up a new strict tenant.** Creating with a CSR and initializing with
only the bound certificate already works today, for every tenant — see
[Creating tenants](#creating-tenants) and
[The tenant's own barrier](#the-tenants-own-barrier) above. The only new flag
is `--isolation-policy`:

```bash
# the tenant's admin, on their own machine — only csr.pem leaves it:
qrdb ca csr --username alice --out ./alice

# operator (or the self-service flow): create the tenant with that CSR
qrdb tenant create --id acme --name "Acme" --isolation-type raft \
     --admin-username alice --admin-csr alice/csr.pem --isolation-policy strict

# tenant: initialize with the certificate tenant create issued, which is the
# only one the barrier's trust-on-first-init window admits
qrdb --cert-file alice/admin-cert.pem --key-file alice/key.pem \
     tenant barrier-init --id acme --strategy passphrase --passphrase "$PP"
```

`tenant create` and `barrier-status` both report the SPKI thumbprint of the
admin bound. Compare it with your own key's thumbprint. If init is refused for
your key, the tenant was created with a different CSR: treat that tenant as
compromised and ask for a new one.

**After a restart, unseal every serving node.** `qrdb tenant barrier-unseal`
does that for you. A node that joined since the last unseal stays sealed until
the next unseal reaches it.

**Connecting through any node.** The CLI and SDK put your tenant in the TLS
server name (SNI). If you connect to a node that does not serve your tenant,
that node passes your still-encrypted connection on to one that does. Your
session is never opened on the node in between.

The Unix socket and the embedded client are the exceptions. With those you
must be on a serving node; otherwise you get "tenant is not served by this
node", together with the list of serving nodes.

**Backups.** Back up a strict tenant's databases yourself, with
`qrdb tenant backup create`. Platform backups do not include them.
