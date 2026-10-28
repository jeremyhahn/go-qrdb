# Multi-Tenancy — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
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
| `barrier` | its own key over its IAM data, on its own store | a volume from the pool |
| `volume` | its own filesystem root and its own consensus log; **everything** encrypted under its key, including that log | a volume per tenant |
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
disk. No flag pins a path: a path names one node's disk, so a dedicated disk
is configured as a volume pool on every node instead.

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
at most 64 entries; a key is 1 to 63 lowercase letters, digits, `.`, `_` or
`-`, beginning and ending with a letter or digit, and a value is at most 256
bytes. A value over a limit or a key outside that form is refused and nothing changes. A tenant's own Admin can
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
the `--key-file` key. The key never leaves your machine. The passphrase, unseal
key and shares go only over TLS: on a connection without it, the Unix socket
included, the call is refused before anything is sent, with an error saying
the secret "is sent only over TLS; this connection is not TLS".

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

**Serving `barrier-init` from your own service.** A service built on the Go
SDK that terminates its callers' TLS itself — its own REST, gRPC or MCP
front end — runs the same two-step proof on its own connection with
`sdk.TenantBarrierInitVerifier`. `Begin(tenantID, tlsState)` issues the
one-time challenge, bound to the tenant and that connection;
`Complete(tenantID, tlsState, proof, key)` checks the caller's signature
against it, and against the key `key` names (an `sdk.ComputeSPKIThumbprint`
value), and spends the challenge. A refused proof is
`*sdk.FirstAdminProofError`, whose `Reason` says which check failed. The
verifier decides only the proof: authorizing the caller is still the
service's own job.

```go
v := sdk.NewTenantBarrierInitVerifier(nil) // one per service, shared by every connection
c, err := v.Begin(tenantID, r.TLS)          // send c.Nonce to the caller
// ... the caller signs with transport.ProveTenantBarrierInit ...
err = v.Complete(tenantID, r.TLS, proof, func() (string, error) {
	return sdk.ComputeSPKIThumbprint(firstAdminCert), nil
})
```

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

**"cannot be a SPIFFE trust domain" when creating a tenant.** The tenant ID is
the trust domain of every certificate in the tenant
(`spiffe://<tenant>/user/<username>`), so it must be one: lowercase letters,
digits, `.`, `-` and `_` only. `Acme` is refused; use `acme`. The deployment's
own trust domain (`auth.cluster_trust_domain`, `qrdb.cluster` unless set) is
refused too, because a certificate under it names no tenant. Nothing was
written, so the ID is still free to use once corrected.

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

**A hardware seal strategy was refused.** An empty `--allowed-barrier-modes` means
software defaults with hardware **fail-closed**. Grant it explicitly.

**Barrier commands fail on a tenant.** It is probably a `prefix` tenant, which
has no barrier of its own — it rides the system barrier.

**Storage usage looks wrong for a `volume` tenant.** A background reconciler
walks each volume tenant's directory on an interval (default 5 minutes) to
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

## Strict isolation

Design: [architecture.md](architecture.md#strict-tenant-isolation--design).

**Choosing a type for confidentiality.** A `prefix` tenant lives in the shared
store, under a barrier the platform manages: **its data is readable by the
platform.** A tenant that needs its data kept from the platform operator
chooses an isolated type (`barrier`, `volume` or `raft`). Whether an isolated
tenant's key auto-unseals is the tenant's own choice, never refused.

**The cluster floor.** Set it at start with the configuration key
`tenant.isolation_policy` (`strict` or `operator-access`; environment
`QRDB_TENANT_ISOLATION_POLICY`). It is applied on every start and only ever
tightens: a value looser than the stored floor is logged and ignored. Left
unset, nothing is applied. A node running in FIPS 140-3 mode raises the floor to `strict`. No
CLI verb or API endpoint reads or sets a policy.

```yaml
tenant:
  isolation_policy: strict
```

**An isolated tenant keeps its own identity.** For a `barrier`, `volume` or
`raft` tenant, under either policy, its users, roles, role bindings, data-key
keyring, invitations, CA, the certificates its CA issues and its database
catalog are kept under the tenant's own key, not with the platform's, and its
admin signs in through any node. The tenant's custodians and audit trail stay
with the platform. The policy decides only what an operator may do, so a tenant
may be made strict, or the floor raised to `strict`, at any time.

On a cluster, `tenant create` for an isolated tenant takes longer than for a
`prefix` tenant. A node that has just joined answers a tenant's requests with a
retryable "unavailable" until it has caught up; clients retry on another node.

**Each node has its own barrier key.** Every node serving an isolated tenant
holds its own key for the tenant's barrier; no key is carried between nodes.
For a `raft` tenant whose key auto-unseals, `qrdb tenant barrier-status` on two
nodes shows different key fingerprints. A node added to the tenant shows the
barrier as sealed and uninitialized until it has its key. A passphrase
`tenant barrier-init` is sent by the CLI to every serving node.

On a cluster, creating a `barrier`, `volume` or `raft` tenant, or changing a
tenant to one of those types, is refused as unavailable until every node runs a
release with the `per-node-barrier-keys` feature, and succeeds once a retry
finds it active. A standalone node is never refused.
