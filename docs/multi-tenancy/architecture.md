# Multi-Tenancy — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Tenant isolation, from a logical key prefix up to a dedicated Raft group per
database, plus the three governance planes that decide who may have which.

## Problem

"Multi-tenant" covers a range from "one customer's keys are prefixed" to "one
customer's data is on its own volume, under its own key, on its own Raft group".
Different customers on one deployment genuinely need different points on that
range, and forcing one point makes the deployment either insecure or
unaffordable.

## Shape chosen

**An isolation ladder**, selected per tenant at creation, plus **three
orthogonal governance planes**.

A tenant's isolation mode is set on `TenantInfo.IsolationType` at creation and
defaults to `prefix`. Each rung adds stronger separation at a higher resource
and operational cost.

| Mode (`IsolationType`) | Raft | Storage | At-rest crypto | What is isolated |
|------------------------|------|---------|----------------|------------------|
| **`prefix`** (default) | Shared system shard | Shared Pebble | Shared system barrier | Logical key prefix only |
| **`barrier`** | Shared system shard | Dedicated Pebble for IAM data, from the volume pool | Per-tenant DEK (IAM data) | Key prefix + a per-tenant cryptographic barrier over the tenant's IAM data |

Each rung in detail:

| **`raft`** | Dedicated Raft group per database | State-machine data under the tenant's `DataRoot`; raft log in the shared host LogDB | Per-tenant DEK over the SM data | Each database is its own Raft shard |

### `prefix` — shared store, logical key prefix (default)

The tenant's keys are prefixed with the tenant id against the shared `system`
store on the shared Raft shard. No per-tenant barrier is applied. This is the
lowest-overhead mode and the default when no isolation type is specified. It is
logical separation, not cryptographic isolation.

### `barrier` — per-tenant cryptographic barrier for IAM data

Adds a dedicated per-tenant `TenantBarrier` (its own DEK and `{tenantID}/`
namespace in an independent backend allocated from the storage volume pool) over
the tenant's IAM data. The tenant's KV data plane still rides the shared shard
with key prefixing. Requires `storage.volume_pools` to be configured; without a
pool, registering a barrier fails with `volume pool not configured on barrier
registry`. See [seal/multi-tenant.md](../seal/multi-tenant.md) for the barrier
lifecycle, officer enrollment, and seal strategy selection.

### `volume` — dedicated filesystem volume, full engines

The tenant receives a dedicated filesystem `DataRoot` allocated from the volume
pool, hosting a **per-tenant single-node, transport-less Raft host** with the
full engine set (KV, MVCC, lease, watch, time-series). All of the tenant's data
— state machine **and** raft WAL — lives under `{DataRoot}` and is encrypted at
rest under the tenant's own DEK via a per-tenant barrier. The host is built
lazily, only after the tenant barrier is unsealed. Requires the volume pool and
the seal subsystem.

### `raft` — dedicated Raft group per database with multi-node replication

Each of the tenant's databases runs as its own Raft shard, replicated across
cluster nodes. The tenant gets a dedicated `DataRoot` from the pool and a
per-tenant barrier; the shard's state-machine data is rooted under
`{DataRoot}/databases/{db}` and encrypted under the tenant DEK on each node,
while the raft log stays in the shared host LogDB. Requires the volume pool and
the seal subsystem.

**Multi-node replication is production-ready.** A requested replication factor is

```
prefix   shared raft, shared pebble, logical key prefix      (DEFAULT)
   |
barrier  shared raft, dedicated pebble for IAM, per-tenant DEK
   |
volume   per-tenant local raft host, own FS DataRoot,
         full engines, all data under tenant DEK (incl. WAL)
   |
raft     dedicated raft group per database, SM data under
         tenant DataRoot + DEK, raft log in shared host LogDB
```

**Changing a tenant's isolation type after it holds data is rejected** with
`ErrTenantHasData` (HTTP 409). Migrate or delete the data first.

## The three governance planes

The planes are deliberately kept orthogonal. A provisioning call must pass
**all three**; do not collapse entitlements into RBAC roles.

| Plane | Controls | Enforced at |
|-------|----------|-------------|
| **RBAC permissions** | Which operation a principal may call at all | Transport middleware |
| **Numeric quotas** | How many of a resource a tenant may hold | Service layer (before delegating) |
| **Feature entitlements** | Which isolation tiers / features a tenant's plan permits | Service layer (before the quota check) |


They are **deliberately orthogonal**. A provisioning call must pass all three,
and **entitlements must not be collapsed into RBAC roles** — "may call this
operation" and "does this customer's plan include this tier" are different
questions with different owners.

Entitlements are checked **before** the numeric quota, so a plan that does not
include a tier is refused without consulting counters at all.

### Alternatives rejected

- **One isolation model.** Rejected on the problem above.
- **Entitlements as roles.** Rejected explicitly — see the orthogonality note.
- **Permissive defaults.** Rejected: the built-in entitlement defaults are
  conservative — prefix-only isolation, with dedicated raft, volume storage,
  system-managed barrier and hardware seal strategies **all denied** until an
  operator grants them.
- **Changing isolation in place.** Rejected while the tenant holds data.
- **A shared cluster-wide tenant key.** Rejected — see the per-node key design
  below.

## Scoping

The tenant comes from **the authenticated mTLS identity**, never from a request
field. The database is selected separately by `X-QRDB-Database` (or its
per-transport equivalent). Keyspace and vector namespaces are tenant-partitioned
on top of that. Detail: [scoping.md](../scoping.md).

### Every data-plane surface resolves per `(tenant, database)`

The transports never hold a data-plane servicer. Each one resolves per request
through `service.Registry`, whose accessors return the servicer for a
`(tenant, database)` pair wrapped at `tenant.ComposeNamespace`'s
`{tenant}/{db}/`:

```
KVService  MVCCService  WatchService   LeaseService     IndexService
TimeSeriesService       VectorService  TransactionService
BatchService            TableService   SequenceService  PrivilegeService
CollectionService
```

**A surface missing from that list is a surface with no tenancy.** That is not
a theoretical risk. `CollectionService` was the last accessor to be added, and
until it was, every transport served document collections from one process-wide
servicer built over the raw, pre-namespace engines: under `prefix` isolation two
tenants naming the same database and the same collection addressed the same
documents, and each could read, overwrite and deny the other's. The registry
wrapper isolated the bundle's KV and index servicers; the collection service was
copied past it, holding the engines it had been constructed with.

The general rule the miss came from: **a service inherits isolation from the
engines it HOLDS, never from the engines its neighbours in the same bundle
hold.** A bundle member that composes other engines must be REBUILT over the
wrapped ones, not copied — and a comment asserting that it "inherits their
isolation" is a claim about construction that only the construction site can
make true. The one at `applyComposite` asserted it, the construction site did
not do it, and the assertion is what kept the miss out of review.

See [../collections/architecture.md](../collections/architecture.md) for the
collection surface's key layout, and
[../collections/engineering.md](../collections/engineering.md) for why the
pre-existing unnamespaced documents are left in place rather than migrated.

### The resolver-served store is confined by construction

`tenant.StoreResolver.Resolve` returns the `kvstore.KVStore` backing a tenant's
resolver-served records (IAM — users, roles and the user-side **auth factors**
that authenticate them — invitations, permission scopes, and any
`dao.GenericDAO` a consumer builds). For a shared-prefix tenant it returns the
system store **confined to `{tenant}/`** — the tenant alone.

The confinement is a property of the store, not of the caller. That distinction
is the whole point: a `dao.GenericDAO` keys entities `{entityType}/{id}` and has
no tenant concept at all, so a resolver that returned the bare shared store gave
every prefix tenant one undifferentiated keyspace — which is precisely what
happened up to v0.8.0-alpha (#41). The IAM stores were unaffected only because
they embed tenant segments in their own keys by hand; every DAO consumer was
exposed, and `data-access.md` makes DAO/Entity the required default.

**Identity is tenant-wide; data is per-(tenant, database).** That split is why
this plane namespaces at `{tenant}/` and not at `ComposeNamespace`'s
`{tenant}/{db}/`. The resolver-served store holds users, roles, permission
scopes and invitations, and a user must authenticate whichever database a
request names. Namespacing it per database was measured to break login
outright: firstboot writes the admin user with no database bound to its
context, so it landed under `{tenant}/system/` while every request carrying a
database resolved elsewhere and found nothing. Single-database unit tests
cannot see that; the integration stack, which authenticates for real, fails
every suite.

### A credential is separated by the store it is looked up in

An auth factor's lookup index is keyed `__sys:auth/lookup/{type}/{credKeyHex}`
and carries no tenant segment — and a `(type, credential)` tuple is genuinely
not unique across a deployment, because two organizations can hold the same
certificate or enrol the same token. What separates them is which store the
lookup reaches, which is the resolver's answer for the tenant the login was
begun under.

Routing it rather than putting a tenant in that one key is the same decision
made once for the whole plane: this repository has ONE mechanism for
per-tenant IAM data. A second mechanism for one surface would mean every later
reader has to know which surface uses which, and there is no version of that
which stays true. Routing is also the better move — records relocate under a
new prefix rather than being rewritten in place.

Enrolment routes on the tenant that owns the USER id, through the identity
index described next, so a factor lands where the login that follows will look
for it rather than wherever the enrolling call's context happened to point. A
caller that may not reach the owning tenant is REFUSED, which is the one place
this plane's write path is stricter than its read path: a redirected write
would bind a credential of the caller's choosing to another tenant's user id.

### A global id needs a global answer to "which store?"

Identity ids are global: a user id is a Snowflake, a role id a UUID, both
unique across the whole deployment. Identity STORAGE is not — it is confined
per tenant, by the section above. Every transport exposes operations that name
a record by id alone (`users delete <id>`, `roles assign --role <id>`), and
those operations cannot assume the caller's tenant is the record's tenant.

Before the keyspaces were confined they could, by accident: every prefix
tenant shared one keyspace, so the two were the same store. Confinement ended
the accident, and `tenant.IdentityIndex` replaces it with a deliberate lookup —
a system-plane `id -> tenant` map in the system tenant's identity namespace, at
`__sys:identity_idx/tenant/{kind}/{id}`.

Three properties make it safe to put a cross-tenant map in the shared store:

- **It carries the id and the tenant, nothing else.** The entry lives outside
  every tenant barrier by necessity, so it may leak existence and placement and
  must leak nothing more — no username, no role name, no permissions. Those
  stay inside the tenant's store, behind the tenant's barrier.
- **The record's own store still decides.** The index answers a routing
  question only. `tenant.OwningTenant` reconciles the answer with the caller
  through `EffectiveTenant`, exactly as `CreateUser` and `ListUsers` reconcile
  a client-supplied tenant, and a sealed owning barrier still refuses with
  `ErrTenantSealed` rather than falling back to a readable store.
- **It never widens reach, and never answers a caller who may not reach the
  record.** An unindexed id routes to the caller's own tenant, which is exactly
  where it routed before the index existed. A caller in another tenant is also
  routed to its own tenant and gets an ordinary not-found — returning a
  cross-tenant denial there would confirm, to a caller who supplied only an id,
  both that the record exists and which tenant holds it.

The entry is a CLAIM, taken atomically and settled before the record is
written. Role ids are caller-supplied — `CreateRoleRequest.ID` travels from
the REST and gRPC surfaces straight into the store — so a last-writer-wins
entry would let one tenant create a role carrying another tenant's role id and
take over where that id routes. A contested id is refused
(`RoleIDExistsError`, naming the caller's own tenant), a claim is released
only by its holder, and a refused capture leaves nothing behind.

"Atomically" is a requirement, not a preference: a store that cannot settle
the claim in one operation is refused outright, because a read-then-write
claim lets two proposers both observe "absent" and both win. Every state
machine implements the conditional write it needs. A claim also succeeds only
for the call that created it — even the holder's own re-claim is refused,
since a create proceeding on a claim it did not win can have that claim
released by a concurrent delete, leaving a record no by-id operation can
reach.

The entry is written and removed by the same functions that write and remove
the record, next to the tenant and username/name indexes. A map maintained by a
sibling caller drifts from what it describes the first time another path writes
a record, and that is how every occurrence of this class in this repository
began.

`role.ResolvePermissions` is the one by-id method that stays context-routed. It
resolves the CALLER's own bindings during authorization, so a binding naming
another tenant's role must not import that role's permissions: the index is a
routing aid, not a way to widen a grant.

**There is no exempt prefix.** Every key a resolver-served store handles is
confined, the reserved `__sys:` keyspace included. That is deliberate: an
exemption keyed on a string prefix would be a SECOND tenancy mechanism
alongside the namespace, and two mechanisms drift. Concretely, an earlier draft
that exempted `__sys:` opened three escapes — a tenant id of `__sys:` produced
the namespace `__sys:/system/` so ordinary DAO keys landed reserved; a DAO
whose entity type began with `__sys:` wrote global records; and
`pkg/permission`, which keys `__sys:perm_scope/{permission}` with **no** tenant
segment of its own, had one tenant's permission scopes apply to every tenant.
Confinement closes all three by construction rather than by rule.

The IAM stores additionally embed tenant segments in their own keys
(`__sys:users/tenant/{tenantID}/…`). That is now redundant with the namespace —
the same duplicate-encoding class as two scoping functions — and is a
follow-on cleanup, not a correctness issue: the namespace is what enforces
isolation, the embedded segment merely repeats it.

**Consequence for indexes, and it is a real trade.** The identity store still
refuses a namespace-qualified index with `kvstore.ErrIndexUnsupported` rather
than serving an index nobody updates; `pkg/dao` detects the refusal and plans a
full scan, so **results stay correct and only the access path is slower**.
Isolation was chosen over the index.

The reason has changed, and only half of it is left. The engine used to decide
which indexes a write maintains by inferring an entity type from the key's FIRST
path segment, which under a namespaced key `acme/system/workitem/1` reads as
`acme` — so a namespace-qualified index could not be maintained at all. The
engine no longer infers anything: an index carries the key prefix it covers and
maintenance compares the key against it (#410, see
[../secondary-indexes/architecture.md](../secondary-indexes/architecture.md#index-key-scope--a-name-is-not-a-predicate)),
which is what #40 asked for. What still refuses is `lifecycle.kvStoreAdapter`,
whose own check accepts only the unqualified name — lifting that is #38/#40 and
is a change to the identity plane's access paths, not to this boundary.

Note the same "entity type is the leading key segment" assumption existed in
`kvstore.MemoryKVStore` and is why `EntityIndex.KeyPrefix` exists: a store must
be told which keys an index covers rather than inferring it from the entity
type. `index.IndexConfig.KeyPrefix` is that same statement one layer down, so
the Raft-backed engine and the in-memory store now scope index maintenance the
same way.

## Per-node-independent key design

For `raft` tenants replicated across nodes, the tenant root key is transported
to each replica over the **authenticated mTLS control plane using HPKE
encryption**, and each node **re-wraps it under its own system barrier**
(system-managed auto-unseal) or under the tenant's own strategy (self-managed).

There is no shared cluster-wide tenant key: a compromised node key does not
expose the tenant's key on other nodes.

Replica placement is quota-checked and spread across distinct healthy nodes by a
load-aware scheduler. Each node's `TenantShardReconciler` watches the replicated
placement table and converges its assigned replicas — starting on boot,
following placement changes, stopping on demotion. Replicas survive node
restarts through the persistent placement table and per-node replica re-attach.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Tenant records, isolation modes, provisioning | `pkg/tenant` | go-qrdb |
| Quotas, entitlements, usage counters, reconciler | `pkg/quota` | go-qrdb |
| Per-tenant barriers, `BarrierRegistry`, `TenantBarrier` | `pkg/seal` — see [../barrier-seal/](../barrier-seal/) | go-qrdb |
| Tenant root-key transport (HPKE over mTLS) | `pkg/keydelivery`, `pkg/clusterkeys` | go-qrdb |
| Replica placement and reconciliation | `pkg/placement` | go-qrdb |
| Volume allocation | `pkg/storage/pool` — see [../storage-engines/](../storage-engines/) | go-qrdb over **go-objstore** |
| Raft groups | — | **go-quicraft** |

## Trust boundaries

- **The tenant is derived from the certificate OU**, never trusted from a
  request.
- **`barrier`, `volume` and `raft` tenants hold their own DEK.** `prefix` is
  logical separation, **not cryptographic isolation** — say so plainly to
  anybody who assumes otherwise.
- **A secondary index is confined to its own tenant's keyspace by the same
  prefix.** `prefix` tenants share one state machine, so index maintenance
  compares each written key against the index's key scope — the namespace read
  back off the index name plus the prefix its creator declared. Until #410 it
  did not, and an index-name-addressed `IndexQuery` could return another
  tenant's KEY NAMES (not their values) to a caller holding `index:query` on
  its own tenant. See
  [../secondary-indexes/architecture.md](../secondary-indexes/architecture.md#index-key-scope--a-name-is-not-a-predicate).
- **Per-node key independence** is the property that makes multi-node tenant
  replication safe.
- **Provisioning-governance permissions are distinct**:
  `tenants:set_isolation`, `tenants:set_entitlements`,
  `databases:create_raft`, `databases:create_volume`.
- **`AllowedBarrierModes` restricts which seal strategies a tenant may pick**,
  with the built-in default being software strategies plus fail-close on
  hardware.

## What this feature deliberately does not do

- **No self-service registration store.** The platform-wide registration
  policy (`iam.self_service_registration`) is held by the composing host
  (go-quicbaas) — one canonical record for the whole stack (owner ruling
  2026-09-01, company doc 08 §"Registration model"). qrdb contributes only
  the gating permission vocabulary: `registration_config:read|update` and
  `tenants:approve` are reserved cross-scope permissions granted to
  Admin@system, gating the host's registration-config and pending-tenant
  approval surfaces. The pending-tenant queue is host state, not a qrdb
  entity.
- **`prefix` provides no cryptographic isolation.**
- **No in-place isolation change once data exists.**
- **No cross-tenant queries.** Nothing joins across tenants.
- **No entitlement inference from roles.**

## Companion documents

- [scoping.md](../scoping.md) — tenant and database scoping in detail.
- [seal/multi-tenant.md](../seal/multi-tenant.md) — `BarrierRegistry`,
  `TenantBarrier`, per-tenant DEK isolation and key-management modes.
- [deployment-multi-node-raft.md](../deployment-multi-node-raft.md) — deploying
  multi-node IsolationRaft tenant clusters end to end.
- [design/multi-tenant-isolation-quota.md](../design/multi-tenant-isolation-quota.md)
  — the design record, including phases whose status is not fully settled on
  paper. Read it as history and intent.
