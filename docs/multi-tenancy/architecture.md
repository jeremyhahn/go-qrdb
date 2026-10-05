# Multi-Tenancy — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-29

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

## Deleting a tenant

**A deleted tenant id is retired, not released.** It can never be used again.

### Problem

A tenant id is a name the caller chose. Deleting a tenant used to remove its
metadata and its barrier registration and leave its keyspace exactly where it
was, so creating a tenant under the same id re-attached the NEW principal to
the OLD principal's keys, secondary indexes, relational tables and identity
records. The sequence needs no storage access: create `acme`, populate,
delete, create `acme` again, read.

The exposure differs by isolation mode, and was measured per mode rather than
reasoned about:

| Mode | What the second tenant got |
|---|---|
| **`prefix`** | The first tenant's data, **readable**. No cryptographic backstop exists at this rung. |
| **`barrier`** | The first tenant's ciphertext, with the key path gone. |
| **`volume`** / **`raft`** | The dedicated `DataRoot` is derived from the tenant id, so the id landed on the SAME directory over the SAME barrier store. A system-managed tenant failed with `barrier already initialized`; a self-managed tenant was created and was then permanently unusable — its barrier could neither be initialized (already is) nor unsealed (nobody holds the first tenant's credential). Data unreadable; id bricked; directory leaked. |

### Shape chosen

The question a delete is supposed to answer — "is this the same principal?" —
is one the system cannot answer about a caller-supplied string. So it does not
guess. `DeleteTenant` records the id in a **tombstone** and `CreateTenant`
refuses a tombstoned id forever.

The order of the whole operation follows one rule — **persist the retirement
before anything irreversible**:

0. **Refuse a reserved id outright**, before any read. The reserved set —
   `system`, `default`, `custodian`, `security-officer`, `_*`, `*:*` — is declared in exactly one place and
   consulted by creation and deletion alike; see "One reserved set, two
   policies" below.
1. **Refuse while the tenant still owns databases.** Teardown is bottom-up. A
   database's own drop reclaims the keyspace that database owns, so by the
   time a tenant's delete runs, what is left under it is its identity records
   and its DEK keyring.
2. **Write the tombstone.** From here the id is retired, so no later principal
   can inherit whatever the remaining steps have not yet removed.
3. **Delete the metadata record.** Steps 2 and 3 are the **fence**. Neither
   destroys anything, and together they make the tenant unresolvable: nothing
   reads it, nothing lists it, and the store resolver refuses its id outright
   rather than serving it as an unregistered prefix tenant. Because the
   tenant's DEK keyring is a record in the store the resolver hands out, the
   keyring is unusable from here on as well.
4. **Destroy the barrier**, including the durable records behind it — see
   "Unregistering a barrier is not destroying it" below. The first
   irreversible step: a dedicated-isolation tenant's data and keyring are
   encrypted under that barrier.
5. **Reclaim the keyspace**, including a dedicated-isolation tenant's
   barrier-encrypted range in the shared store, which holds its identity
   records and DEK keyring.

There is no separate DEK-shred step. The tenant DEK keyring
([tenant-dek](../tenant-dek/architecture.md)) lives in the tenant's own store,
so it dies with the tenant's barrier and keyspace and is unreachable from the
fence on.

**Why the fence is first.** The DEK used to be shredded first, and that was a
defect. The ordering was
argued from a choice of two — shred-then-delete can strand metadata for a
tenant whose key is gone, delete-then-shred can strand a key nothing names *and*
leave the data readable in the window — and the first was taken as the lesser.
Its real cost is worse than "stranded metadata": a transient failure of the
tombstone write returned an error reading *"the delete did not happen"* while
leaving a live, listed, resolvable tenant whose every byte was already
permanently undecryptable. A third ordering was not on that list and dominates
both. Fencing first leaves no readable window, because nothing resolves the
tenant after step 3, and leaves no live tenant without a key, because the
metadata is gone before the key is. What it can leave is a **retired id whose
barrier or keyspace still exists** — residue reachable by nobody, removed by
re-issuing the call.

A failure in steps 4 or 5 leaves the id retired and is reported as such: the
error names the retirement and the remedy, because "reclaim failed" read as
"nothing changed" is what makes an operator stop. Re-issuing the delete resumes
from step 4; every step past the fence is idempotent.

### One reserved set, two policies

The reserved-id rule used to be applied only at creation, so the ids this
system declares un-creatable were freely **deletable**: `DeleteTenant` took a
caller-supplied `system` and ran the whole irreversible teardown against it.
The reclamation in step 6 already refused the system tenant — *"a reclamation
that accepted it would empty the store it was asked to tidy"* — but that
guards the **last** step, and steps 2 to 4 are the committing ones. The same
judgement now sits at the front, where it prevents the commits rather than
only the destructive read.

The bottom-up database refusal is **not** a substitute for it. It returns
without refusing when no `DatabaseLister` is wired, and where one is wired it
catches the system tenant only by accident of configuration — the system
tenant happens to own the default `system` database. A guard that depends on a
database existing is not a guard.

The set is written down **once**. Creation and deletion are two call sites
with exactly one documented difference:

| Path | Honours the first-boot bypass? | Why |
|---|---|---|
| **create** | yes | bootstrap must be able to create the system tenant |
| **delete** | **no** | nothing legitimately deletes a reserved tenant, at boot or afterwards, and the operation is irreversible |

### Reclamation is synchronous

Step 1 bounds step 6. After the databases are dropped, what remains under a
tenant is its identity records — users, roles, permission scopes, invitations
— which the quota plane already limits by `MaxUsers` and `MaxRoles`. That is
strictly less work than `DROP DATABASE` already performs inline on the same
key-at-a-time path.

**A background reclaimer was considered and rejected on that measurement.** It
would add a restartable, idempotent, observable subsystem — a work queue, a
scheduler, a progress surface — in order to do less work than a call this
system already makes synchronously. The reasoning that makes an asynchronous
purge attractive ("an unbounded keyspace walk inside a synchronous API call is
a denial of service on your own control plane") is sound, and bottom-up
refusal is what removes its premise.

The reclamation scope is nonetheless the tenant's WHOLE namespace, not just
the identity residue, because the database catalog is node-local while the
keyspace is replicated: a node whose catalog under-reports would otherwise
leave a tenant's data behind with nothing left to name it.

### What a tenant owns

`service.TenantKeyPrefixes` is the declaration. Six ranges, not one:

| Range | Why it is separate |
|---|---|
| `{tenant}/` | Identity records AND every `{tenant}/{db}/` namespace, since the first contains the second. |
| `ts:{tenant}/` | Time-series points. The namespace lands INSIDE the series prefix, because it is the metric NAME the namespaced view prefixes, not the key. |
| `idx:metric:{tenant}/` | Time-series metric index, same reason. |
| `retention:{tenant}/` | Time-series retention policies, same reason. |
| `__sys:placement/{tenant}/` | A dedicated-raft database's replica assignment: keyed by tenant, OUTSIDE the namespace. The per-node reconciler watches this range, so removing an entry is what stops the shard and purges its data dir on **every** node holding a replica — which the local node's `DataRoot` removal cannot reach. |
| `__sys:collection-work/{tenant}/` | The collection work journal: one entry per collection in a transitional state, keyed by tenant OUTSIDE the namespace so the collection sweeper can find work without a database catalog. Removing the range means a deleted tenant's entries go with it instead of waiting for the sweeper. |

Plus the secondary-index catalog and the vector namespaces, which are
namespaces of their own rather than ranges of keys and are enumerated
separately.

The **system tenant is refused**: `system/` holds the bootstrap database and
every system identity record, so a reclamation that accepted it would empty
the store.

### Unregistering a barrier is not destroying it

`BarrierRegistry.UnregisterTenant` seals and forgets the IN-MEMORY barrier.
Two durable records outlive it — the `DataRoot` record and the volume-pool
slot assignment — and the boot path reads the first, so a deleted volume or
raft tenant's barrier was re-registered on the next restart at the same
directory over the same store. `BarrierRegistry.DestroyTenant` is the
operation a deletion needs: it removes both records, closes the per-tenant
store and removes the `DataRoot`.

### Every node reclaims a deleted tenant's directories

A delete destroys the `DataRoot` on the node that ran it, and the placement
removal purges dedicated shards on the nodes hosting them at that moment. A
node that was **down** during the delete, or that held the tenant's barrier
without hosting a shard, keeps the tenant's directories — and, worse, its
tenant-root record, from which the next boot would restore the retired
barrier.

**The reclaim predicate is the tombstone, and nothing else.** A missing
placement entry or a missing tenant record also describes a tenant that has
not been placed yet; reclaiming on that signal would destroy a live tenant's
data. A tombstone describes nothing but a deletion, it is replicated, and it is
permanent.

The same pass runs in two places:

- **At boot**, before any tenant barrier is restored. For each retired id the
  node releases the barrier, removes every local directory the registry
  attributes to the id — its registered `DataRoot`, the one its local record
  names, and `{poolBase}/{id}` under each local pool base — and then drops the
  durable records that would have restored the barrier. A failure is logged and does
  not fail the boot: what is reclaimed is disk, never correctness, because a
  retired id resolves for nobody whether or not its bytes remain.
- **On demand**, as the operator verb (`ReconcileDeletedTenants`, on every
  transport and `qrdb tenant reconcile-deleted`), which additionally stops
  anything the node still runs for the id — its volume host and dedicated
  shards — before removing directories. It acts on the node serving the
  request, never forwarded: each replica reclaims its own disk.

**It never removes a directory a live tenant claims.** The live set is read
strictly — an unreadable tenant record aborts the pass, because its directory
is unknown — and re-read right before each removal. A retired id that still
has a record is skipped whole. A directory that is, contains or lies
inside one a live tenant claims — on its record or in this node's registry —
is skipped and reported: an operator-supplied `DataRoot` can be reused by a
later tenant, and a stale record on a node that missed the delete would
otherwise name the new tenant's data. A directory that is, or contains, a
volume-pool base or a reserved root is skipped the same way, as is one inside
a reserved root that is not also inside a pool base, and so is a recorded
path that is not absolute and canonical — judged as recorded, before any
cleaning could turn `x/..` into its parent. Every skip is logged at `WARN`.

**One id's failure does not stop the others.** A removal that fails for one
retired id is collected and the pass goes on; the pass reports every failure
together at the end. Only a failure to read the guards themselves — the
tombstones or the live set — stops it. An id already fully reclaimed on the
node is passed over without touching anything.

**A mount-point directory is reclaimed.** A dedicated `DataRoot` is often a
mount point, which cannot itself be removed. The directory is emptied first,
and an emptied root that refuses its own removal because it is a mount counts
as reclaimed; the mount is left for the operator.

**The records go last.** The directories are removed before the records that
name them, so a removal that fails leaves the record and the next pass finds
the directory again. And a new tenant may not take a directory another
tenant's record on the node still names, which closes the window from the
create side; when that record belongs to a retired tenant the refusal says so
and names the reconcile verb.

Retired ids are also **enumerable** (`ListDeletedTenants`): a paged range read
over the tombstone prefix, so an operator can ask which ids can never be used
again, and when each was retired, instead of discovering it by a refused
create.

### The identity plane fails closed too

`tenant.StoreResolver.Resolve` deliberately falls through to the shared store
for an **unknown** tenant, which keeps lookups working for callers that never
registered metadata. A **deleted** tenant must not get that fall-through, or
its id resolves to the keyspace the deletion just reclaimed. The resolver
consults the tombstone and refuses.

### Alternatives rejected

- **Delete as a pure purge, no tombstone.** Rejected: the walk is unbounded
  without bottom-up refusal, and even a complete purge leaves the id
  available, which is the question the delete was asked.
- **Delete as a pure tombstone, no purge.** Rejected: a permanent storage leak
  is not a delete.
- **A background reclaimer.** Rejected on the measurement above.
- **Releasing the id after a retention period.** Rejected: "enough time has
  passed" is not evidence that the second `acme` is the same principal as the
  first.
- **Reclaiming a directory whose tenant has no placement entry.** Rejected: an
  unplaced live tenant looks the same, and the reclaim would destroy its data.
  The tombstone is the only predicate.
- **Reclaiming by the `DataRoot` the tombstone records.** Rejected: it is the
  deleting node's path, and an operator-supplied path may since belong to a
  live tenant. The reclaim uses what this node's registry attributes to the id
  and still checks every path against live tenants.

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
CollectionService       LockService
```

**A surface missing from that list is a surface with no tenancy.** That is not
a theoretical risk; it has happened twice, and the second time is the more
instructive one.

`CollectionService` was added after every transport had served document
collections from one process-wide servicer built over the raw, pre-namespace
engines: under `prefix` isolation two tenants naming the same database and the
same collection addressed the same documents, and each could read, overwrite
and deny the other's. The registry wrapper isolated the bundle's KV and index
servicers; the collection service was copied past it, holding the engines it
had been constructed with.

`LockService` was added after the same enumeration found one more surface of
the same shape. A lock record is `__sys:lock/{key}` with the key supplied by
the caller and no tenant segment, and the lock servicer was a process singleton
over the raw engines — so under `prefix` isolation one tenant holding `deploy`
blocked every other tenant's `deploy`, `List` enumerated foreign lock names,
`Status` returned a foreign holder record, and `LockHeldError` handed back the
foreign `HolderID` even as it correctly refused the caller. See
[../distributed-locks/architecture.md](../distributed-locks/architecture.md).

**Note what survived the "no exempt prefix" rule below, and why.** That rule
confines every key a resolver-served store handles. A process singleton is not
resolver-served, so it never enters the confined path at all: the guarantee was
conditional on being resolved, and the two misses were the two surfaces that
were not. The discriminator is therefore NOT the key shape —
`pkg/permission`'s `__sys:perm_scope/{permission}` has no tenant segment either
and is perfectly safe, because it routes through `tenant.StoreResolver`. The
question to ask of any surface is: **does the writing type route through the
resolver, or resolve through the registry's namespaced engines — or does it
hold a raw store as a process singleton?**

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
resolver-served records (IAM — users, roles, the user-side **auth factors**
that authenticate them, and the ceremony-time **custodian auth factors** —
invitations, permission scopes, and any `dao.GenericDAO` a consumer builds).
For a shared-prefix tenant it returns the system store **confined to
`{tenant}/`** — the tenant alone.

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

**Shared-prefix tenants have real, per-tenant, engine-maintained indexes.** A
DAO over a resolver-served store registers its indexes under
namespace-qualified names (`acme/workitem.State`) with the absolute key prefix
the tenant's records carry (`acme/workitem/`). The engine maintains an index
for exactly the keys under its scope, so one tenant's index can never hold —
or answer with — another tenant's rows, and a query is answered from the index
rather than a full scan.

It used to be the other way round, and isolation was chosen over the index. The
engine decided which indexes a write maintains by inferring an entity type from
the key's FIRST path segment, which under a namespaced key `acme/workitem/1`
reads as `acme`, so a namespace-qualified index could not be maintained, and
`lifecycle.kvStoreAdapter` refused every qualified name with
`kvstore.ErrIndexUnsupported` rather than serve an index nobody updated. Two
changes removed the reason. The engine no longer infers anything: an index
carries the key prefix it covers and maintenance compares the key against it,
on the Raft apply path and on `KVEngine.PutWithIndexes`/`DeleteWithIndexes`
alike (see
[../secondary-indexes/architecture.md](../secondary-indexes/architecture.md#index-key-scope--a-name-is-not-a-predicate)).
And the adapter translates the caller's absolute key prefix into the scope the
engine composes — `engine.IndexNamespace(name)` followed by the declared prefix
— so the scope maintained is exactly the prefix the keys carry, whether the
store is namespaced at `{tenant}/`, at `{tenant}/{database}/`, or not at all.
Nothing about the tenant or database is assumed; both are read off what the
caller declared. A name whose namespace the keys do **not** live under is still
refused, because the engine would maintain it over a keyspace no write reaches.

This covers the tenants whose resolver-served store is the plaintext shared
store. The store served to `barrier`, `volume` and `raft` tenants encrypts
every value under the tenant's DEK and still refuses index operations; see
"Indexes on encrypted tenant stores" below.

Note the same "entity type is the leading key segment" assumption existed in
`kvstore.MemoryKVStore` and is why `EntityIndex.KeyPrefix` exists: a store must
be told which keys an index covers rather than inferring it from the entity
type. `index.IndexConfig.KeyPrefix` is that same statement one layer down, so
the Raft-backed engine and the in-memory store now scope index maintenance the
same way.

### Indexes on encrypted tenant stores

**Not built, and not decided here.** The resolver serves `barrier`, `volume`
and `raft` tenants a store that encrypts every value under the tenant DEK
before it reaches the shared system store, and that store refuses every index
operation with `kvstore.ErrIndexUnsupported`; `pkg/dao` plans a full scan. An
index over those records would have to put indexed values into index keys in
the shared store, outside the tenant's barrier.

The tenancy model states that an isolated tenant has everything isolated — its
own barrier, its own storage location and its own engine configuration — so
the intended shape is that an isolated tenant's records, and therefore its
indexes, live in the tenant's own store rather than the shared one. Converging
the resolver-served records onto that store, and whether any index is offered
before it happens, are open design questions for the owner.

## Per-node-independent key design

For `raft` tenants replicated across nodes, the tenant root key is transported
to each replica over the **authenticated mTLS control plane using HPKE
encryption**, and each node **re-wraps it under its own system barrier**
(system-managed auto-unseal) or under the tenant's own strategy (self-managed).

The wrapping key is per node; the tenant root key is not. Every replica of a
`raft` tenant holds the **same** tenant root key and derives the same keys from
it (`pkg/seal/import_key.go`), so a node whose node key and disk are both
compromised yields a key that also decrypts that tenant's data on the other
replicas. **Correction (2026-10-01):** this section previously said a
compromised node key does not expose the tenant's key on other nodes. A key per
replica waits on where a dedicated tenant's identity records are stored: they
are encrypted with the tenant key and replicated through the system shard, so
every node serving the tenant must hold the same key today.

A tenant seal is a cluster seal record once the cluster has activated
`cluster-seal`: every node holding the tenant's barrier seals it, ends the
tenant's open streams and stops the tenant's shards. Only the tenant's own
administrators may seal or unseal it; the platform operator cannot. See
[../barrier-seal/architecture.md](../barrier-seal/architecture.md#sealing-in-a-cluster).

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
| The multi-node composition of `raft` isolation: the key plane, the key distributor, the rebalance dependencies, the replica host, the peer admin registry | `pkg/server/lifecycle` (`ComposeRaftTenantCluster`, `NewPeerAdminRegistry`), composed identically by `qrdbd` and a clustered `sdk/go/node` node | go-qrdb |
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
- **Per-node KEK independence** protects each node's at-rest copy of a
  tenant key; the tenant root key itself is shared by the tenant's replicas
  (see *Per-node-independent key design*).
- **Provisioning-governance permissions are distinct**:
  `tenants:set_isolation`, `tenants:set_entitlements`,
  `databases:create_raft`, `databases:create_volume`.
- **A tenant's plan belongs to the platform operator.** `tenants:create`,
  `tenants:set_quotas`, `tenants:set_entitlements` and `tenants:set_isolation`
  are cross-scope (`pkg/permission/tenant_provisioning_scope.go`): only a
  `system`-tenant identity can exercise them, so a tenant's Admin cannot mint
  tenants or raise its own limits. `tenants:update` stays self-scoped and covers
  only the display name.
- **The tenant a request names is authorized, not only the caller's.** Every
  network transport's entry check authorizes the route's permission against
  the caller's own tenant; the handler then re-decides against the tenant the
  request names (`AuthMiddleware.AuthorizeTenantTarget`, `AuthorizeTenantUpdate`)
  and narrows a list to the caller's own tenant unless it holds
  `tenants:list:cross` (`AuthMiddleware.FilterTenantList`). The re-check charges
  no second rate-limit token and records only a refusal.
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
