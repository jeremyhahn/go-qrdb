# Multi-Tenancy — Architecture

> **Status: NORMATIVE** (the strict-isolation section is RULED, not yet
> implemented) · Scope: go-qrdb · Last verified: 2026-10-05

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
| **`volume`** | Per-tenant single-node, transport-less Raft host | Dedicated filesystem `DataRoot` from the pool | Per-tenant DEK over all data, WAL included | Everything the tenant's databases hold |
| **`raft`** | Dedicated Raft group per database | State-machine data under the tenant's `DataRoot`; raft log in the shared host LogDB | Per-tenant DEK over the SM data | Each database is its own Raft shard |

*(Corrected 2026-10-01: the `volume` row was missing from this table and the
`raft` row had been split off below it.)*

Each rung in detail:

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

## Creating a tenant: onboarding with its first admin

### Problem

A tenant with no account in it is unusable, and on a dedicated tenant it is
worse: the first thing a dedicated tenant needs is its own barrier, and before
that barrier exists the barrier's trust-on-first-init window was the only way
in — admitting **any** authenticated identity of the tenant. Whoever reached it
first set the tenant's key. And the platform operator is the one party that
must not hold the tenant's credential, so it cannot simply mint one and hand it
over.

### Shape chosen

**A tenant is created with its first admin's login credential, or not at all.**
`tenant create` takes the admin's username and exactly one credential the admin
already holds the key for:

- a **certificate signing request**, which the new tenant's own login CA
  (Key B) signs; or
- a **bring-your-own certificate** from the admin's organisation's CA, which
  must chain to CA certificates registered as the tenant's trust anchors in the
  same call ([../trust-anchors/](../trust-anchors/architecture.md)), with its
  CN naming the admin and its tenant naming the new tenant.

The response carries the admin's certificate and its SPKI thumbprint — never a
private key, because none was ever made on the server.

What happens next depends on where the tenant keeps its identity records
(`tenant.IAMBehindTenantBarrier`):

| Isolation | Identity records | The admin at creation |
|---|---|---|
| `prefix` | the shared system store | account created, granted the tenant's Admin role, certificate bound — usable at once |
| `barrier`, `volume`, `raft` | behind the tenant's own barrier, which does not exist yet | certificate issued or verified and recorded as the tenant's **pending admin**; the response says `awaits_barrier_init` |

For a pending admin, the record (`pkg/tenantadmin`) lives in the tenant's
identity namespace on the replicated system store, readable while the tenant
barrier is sealed, and does two things:

1. **It is the only certificate the trust-on-first-init window admits.** A
   barrier-init request must present that SPKI, as that username; anything
   else is refused. So whoever initializes the barrier is the admin the
   operator onboarded. Under `auth.mtls_binding_policy: required`, a tenant
   with no onboarded admin admits **nobody** through the window — that posture
   admits no unbound certificate anywhere else either.
2. **It becomes the account.** The tenant service runs a barrier-ready hook
   after the barrier is initialized, after each unseal — by key material, or
   by the holder share that completes an unseal quorum (never one below the
   threshold, a seal or rekey quorum, or a submission that fails) — and on each
   status read that finds it unsealed; qrdb's composers register
   `bootstrap.CompletePendingTenantAdmin` there, which seeds the tenant's roles,
   creates the admin with the Admin role (or adopts an existing account of that
   name), binds the certificate, and deletes the record. It is idempotent, so a
   failed attempt is finished by the next trigger.

A **system-managed** barrier is initialized and unsealed by the create itself,
and nothing raises the hook for it. Its admin is completed by the create's
caller instead, **after** the response: `bootstrap.ProvisionTenant` reports
`AdminCompletionDue`, and the onboarding service answers with the tenant record
it committed, then runs `bootstrap.CompletePendingTenantAdminWithin` in the
background — each attempt bounded, retried with backoff under its own budget,
stopping on a sealed tenant or a permanent error. The account step writes
through the tenant's own shard, which can be slower than the create's request
deadline; keeping it out of the request means a create that committed always
answers success. A completion that gives up leaves the record for the hook's
next trigger. The success or failure audit record is written before the
response, on its own bound rather than the request's.

### One unit, with compensation

Every input that can be refused — the trust anchors, the CSR, the
bring-your-own certificate's chain and identity — is judged **before** the
tenant record is written. That ordering is not tidiness: a deleted tenant id is
retired permanently (see [Deleting a tenant](#deleting-a-tenant)), so undoing a
created tenant costs the caller its id. Refusing early means an ordinary
mistake never costs an id.

Only a failure after the first write — an infrastructure failure — reaches the
undo path: each write registered how to take itself back (remove the anchors,
revoke the signed certificate, delete the pending record or the account, delete
the tenant), and they run in reverse. An undo step that itself fails is
reported to the caller for an operator to finish rather than swallowed.

### Alternatives rejected

- **The server generates the admin's key and returns it.** Rejected: the
  platform operator would hold the tenant's first credential, and the response
  would be a credential in transit. The CSR path gives the operator nothing it
  could log in with.
- **Create the tenant, then issue the admin separately.** Rejected: between the
  two steps the tenant exists with no admin, and a dedicated tenant's
  first-init window admits whoever reaches it first.
- **Pre-create the dedicated tenant's admin account in the system store.**
  Rejected: a dedicated tenant's accounts live behind its barrier by design;
  writing one outside it would split the tenant's identity across two stores.

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
[Strict tenant isolation](#strict-tenant-isolation--design) rules that
convergence: an isolated tenant's records move into its own home store, where
an index is an ordinary index over the tenant's own data.

## Per-node-independent key design

> **Correction (2026-10-06): the prerequisite below is now verified, not just
> suspected.** An attempt to implement independent per-replica keys for
> system-managed tenants (`BarrierRegistry.ProvisionSystemManagedReplica`
> generating a fresh root key locally instead of importing the leader's) was
> built and run against a real 3-node cluster. A node holding a database
> replica but running an independently generated tenant key failed every
> authenticated request with `quicraft/seal: decryption failed` from
> `pkg/user/store.go`'s `GetUserByUsername`. Cause, with citations:
> `pkg/tenant/store_resolver.go`'s `resolveBarrier` routes a `raft`/`barrier`/
> `volume` tenant's IAM (users, roles, auth factors) through the **system
> shard** — replicated to every node regardless of that tenant's database
> placement — "because IAM must be resolvable on EVERY replica" (its own
> comment). `pkg/seal/tenant_shared_kvstore.go`'s `Put`/`PutIfAbsent` encrypt
> the VALUE under the tenant's root key via `TenantBarrier.Barrier().Encrypt`
> **before** the ciphertext reaches Raft, so the replicated log entry itself
> is ciphertext, not plaintext later encrypted at rest. A node whose tenant
> key differs from whichever node wrote a given identity record cannot open
> it. This is not specific to a newly added replica or to system-managed
> mode: it holds for every node of every `raft`/`barrier`/`volume` tenant
> today, because the system shard has no placement boundary and system-shard
> leadership (who writes a given entry) is independent of any node's identity.
> The change was reverted; `ExportRootKeyContext`/`ImportRootKey` are
> unchanged. **Per-replica tenant keys cannot ship — for any barrier mode —
> until tenant identity moves out of the system shard**, which is exactly
> what [Strict tenant isolation](#strict-tenant-isolation--design)'s home
> store does. That migration is the real prerequisite this section's older
> "waits on where a dedicated tenant's identity records are stored" language
> was pointing at.

For a `raft` tenant replicated across nodes, the tenant root key is
transported to each replica over the **authenticated mTLS control plane
using HPKE encryption**, and each node **re-wraps it under its own system
barrier** (system-managed auto-unseal) or under the tenant's own strategy
(self-managed).

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

Because leaving the placement stops a replica, a replica leaves its group
before it leaves the placement. A scale-down and a rebalance move both evict
the replica from Raft first, then drop it from the placement. The eviction is
a configuration change committed by a quorum of the configuration it leaves,
which still counts the departing replica's vote. In a group of two voters,
that vote is the one the removal needs. *(Corrected: the placement used to be
dropped first, so the departing node's reconciler stopped and purged its
replica before the removal committed. The surviving leader then lost quorum
and the move failed at its last step.)* A failed eviction leaves the
placement unchanged, so the replica keeps running. The placement write after
the eviction applies to the entry as it then stands, so a promotion
committed meanwhile is kept rather than refused.

## Strict tenant isolation — design

> **Status: RULED — partially implemented.** The owner ruled the design
> decisions on 2026-10-01 and S4 on 2026-10-02; they are listed at the end
> under [Rulings](#rulings). It is the requirements contract for the work
> that builds it.
>
> **Correction (2026-10-05): S7 already shipped, independent of this ruling.**
> Tenant onboarding landed on 2026-10-03 (see
> [Creating a tenant: onboarding with its first admin](#creating-a-tenant-onboarding-with-its-first-admin)
> above) and took the CSR-based first admin, the pending-admin record and the
> trust-on-first-init restriction described below for every tenant, under the
> real field names `admin_username` / `admin_csr_pem` / `admin_cert_pem` — not
> the `first_admin: {username, csr_pem}` shape this section still shows.
>
> **Correction (2026-10-06): the isolation-policy setting and the `ScopeSelf`
> system-tenant lift removal shipped.** `pkg/tenant/isolation_policy.go` has
> the cluster floor (`ClusterIsolationPolicy`: unset/`strict`/`operator-access`,
> tighten-only CAS) and the per-tenant opt-in (`TenantIsolationPolicy`:
> inherit/`strict`), wired into `CreateTenant` (S2: `StrictRequiresDedicatedStorageError`
> for a prefix tenant under an effective-strict policy) and `UpdateTenant`
> (tighten-only: `IsolationPolicyChangeError`). `tenants:read:cross`,
> `tenants:delete:cross` and `tenants:update:cross`
> ([permission.go](../../pkg/permission/cross_scope.go)) are the named
> replacements `AuthMiddleware.AuthorizeTenantTarget` falls back to — ordinary
> platform administration of another tenant's registry entry (read, delete,
> rename), not a `ScopeSelf` data/IAM operation. **Separately, and unconditionally** — this is NOT gated by the
> isolation-policy setting, it applies "under every policy" per the ruling —
> the `inSystem ||` arm is deleted from `rbac.Authorize`'s `ScopeSelf` case and
> from `tenant.EffectiveTenant`: a system-tenant identity no longer reaches any
> other tenant's `ScopeSelf` data or IAM records, in any deployment, whether or
> not the isolation-policy setting has ever been touched. The misleading
> `tenant.strict_isolation` config key (vector-namespace-only) is deleted; its
> intent is folded into the cluster floor via
> `tenant.ClusterIsolationPolicyReader`, consulted live by
> `pkg/service/vector.go`'s `validateTenantCreate`.
>
> **Two production regressions surfaced by integration testing, and fixed in
> the same change.** (1) `pkg/bootstrap.ProvisionTenant`'s own writes
> (seeding the new tenant's built-in roles, creating its first admin) used
> the CALLER's ctx, not the new tenant's — the ScopeSelf lift removal made
> every one of those refuse with a cross-tenant denial, so `tenant create`
> was broken on every transport until `ProvisionTenant` rebinds ctx to the
> tenant it is provisioning. (2) `pkg/auth/middleware.finalizeIdentity` bound
> ctx's tenant to the authenticating identity AFTER looking that identity up
> by username — the lookup itself needs ctx already bound to the identity's
> OWN tenant now that the lift is gone, so authentication for every
> non-system-tenant identity was broken until the bind moved before the
> lookup. (3) A system-managed (auto-unseal) dedicated-isolation tenant's
> pending admin was never completed: nothing else ever raises
> `tenant.BarrierReadyHook` for it (its barrier is never sealed, so its own
> admin never calls barrier-init/-unseal/-status to trigger the hook the way
> a self-managed tenant's admin does) — `ProvisionTenant` reports
> `AdminCompletionDue` whenever `CreateTenant` reports
> `BarrierAutoUnseal: true`, and `OnboardTenant` completes the account after
> it has answered, retried on its own bound rather than the request's. A
> completion that runs out of that bound leaves the pending record, which the
> tenant's next barrier unseal or status read completes.
>
> **What this slice does NOT do.** No transport or CLI exposes the
> isolation-policy get/set verbs yet (the table in
> [api.md](api.md#strict-isolation--ruled) is still the target shape, not
> current reality) — only the Go service API is ready for it. The
> `tenant_identity:read`/`write` operator-access surface, the tenant home
> group, per-replica keys, layer-4 passthrough, the dedicated-WAL guarantee
> (S4: G1-G10), the per-tenant audit trail (S9), tenant-only backups (S10), the
> `strict-isolation` cluster feature gate and the residue test are all still
> unimplemented. What remains unshipped from the rest of this design: the home
> store, per-node replica keys, the client fan-out to every node in the
> serving set, and the rest of the cluster isolation-policy ladder.
>
> Engineering detail and the phased plan:
> [engineering.md](engineering.md#strict-isolation--engineering-contract-ruled).
> API, CLI and user guide: [api.md](api.md#strict-isolation--ruled),
> [cli.md](cli.md#strict-isolation--ruled), [user.md](user.md#strict-isolation--ruled).

### Problem

A tenant on a dedicated isolation tier (`barrier`, `volume` or `raft`) is
promised isolation. Today most of what makes up that tenant lives in the system
tenant:

- **Identity.** Its users, roles, role bindings, auth factors, credential
  lookup keys, permission scopes, invitations and DEK keyring are in the
  shared, Raft-replicated system store under `__tiam:{tenant}/`. The values
  are encrypted under the tenant key, but the **key names are plaintext**, and
  they carry usernames, email addresses, certificate thumbprints, role names
  and credential lookup hashes.
- **Certificates.** Its CA private key and its issued-certificate records are
  in the system store.
- **Seal metadata.** Its custodian records, its SO PIN commitment, its
  custodian groups and its submitted Shamir shares are in the system store,
  outside every tenant barrier. So is its officer roster, before
  initialization.
- **Routing.** A global identity index in the system tenant maps every user id
  and role id to its tenant.

Two more facts make it worse:

- **Shared root key.** Every replica of a `raft` tenant holds the same tenant
  root key, delivered by `pkg/clusterkeys`. A node whose system key and disk
  are both compromised yields a key that decrypts the tenant's data on every
  other replica too.
- **Operator reach.** RBAC's `ScopeSelf` rule admits any system-tenant
  identity in any tenant. A system-tenant Admin can:
  - read and rewrite another tenant's users, roles and factors;
  - mint a CA-signed client certificate for another tenant's user, and log in
    as that user;
  - reach another tenant's database through its etcd listener.

The owner's direction is that a tenant must be fully isolated. **Every tenant
record, and every system record about the tenant, lives in the tenant's own
isolated store, to the greatest extent possible.** The only things left in the
system tenant are the ones that are absolutely required and cannot be avoided.

### Shape chosen

```
                 SYSTEM TENANT (system shard, replicated to every node)
                 +-----------------------------------------------------+
                 | tenant registry entry   tombstone   placement       |
                 | plan + platform usage   pool slots  seal record     |
                 | isolation policy        tenant CA certs (public)    |
                 +-----------------------------------------------------+
                        |  routing only: who, where, how big
                        v
   +--------------------------- tenant "acme" serving set ---------------------------+
   |   node A                     node B                     node C                  |
   |   acme replica key A         acme replica key B         acme replica key C      |
   |   (born on A, never leaves)  (born on B, never leaves)  (born on C, never leaves)|
   |   +---------------------+    +---------------------+    +---------------------+ |
   |   | acme home group     |<-->| acme home group     |<-->| acme home group     | |
   |   |  users roles factors|    |  (Raft replica)     |    |  (Raft replica)     | |
   |   |  certs CA catalog   |    |                     |    |                     | |
   |   |  DEK keyring        |    |                     |    |                     | |
   |   +---------------------+    +---------------------+    +---------------------+ |
   |   | acme db groups ...  |<-->| acme db groups ...  |<-->| acme db groups ...  | |
   |   +---------------------+    +---------------------+    +---------------------+ |
   +----------------------------------------------------------------------------------+
        Raft carries entries between replicas over node mTLS. Each node encrypts
        what it stores under ITS OWN replica key. No key crosses a node boundary.
```

1. **Each strict tenant gets a home store.** It holds every record of the
   tenant that the platform does not need in order to place, route, size or
   retire the tenant.
   - For a `raft` or `barrier` tenant, the home store is a **tenant home
     group**: a Raft group per tenant, replicated across the tenant's serving
     set.
   - For a `volume` tenant, it is the tenant's existing single-node host.
2. **Each node encrypts its copy under its own replica key.** A replica key
   is born on the node that uses it, under the tenant's credential as
   presented to that node, and never leaves that node. Raft replicates
   entries between replicas over the node mTLS channel it already uses, and
   each replica encrypts at rest locally. No tenant key is ever sent between
   nodes, whether a root key, a replica key or a derived key.
   `pkg/clusterkeys` stops carrying tenant keys.
3. **The system tenant keeps a short, enumerated list of records.** The list
   is written down below with the reason for each one, and a test enforces it
   (see [engineering.md](engineering.md#the-residue-test)).
4. **A node outside a tenant's serving set never terminates that tenant's
   connections.** It relays the still-encrypted connection, at layer 4, to a
   node that serves the tenant. The serving node terminates TLS and
   authenticates the client itself.
5. **A cluster-wide floor decides the platform operator's reach, and a tenant
   may raise it for itself.**
   - Under `strict`, the operator has no access to tenant records.
   - Under `operator-access`, named, audited, platform-only permissions reach
     tenant identity records.
   - **Tenant databases and data are never accessible across tenants, in
     either mode, by anyone.**

### Vocabulary

| Term | Meaning |
|---|---|
| **node barrier** | Each node's own system barrier. It protects that node's system-shard data, its WAL and its node-local records. |
| **tenant barrier** | The barrier over one tenant's data on one node. It is keyed by that node's **replica key**. |
| **database barrier** | An optional per-database barrier, layered on the tenant barrier ([../databases/architecture.md](../databases/architecture.md)). |
| **replica key** | A tenant root key that exists on exactly one node. Every node in a tenant's serving set has its own. |
| **serving set** | The nodes holding the tenant's home group replicas. Only they hold the tenant's keys, data or identity records. Every database group of the tenant is placed inside it. |
| **tenant home group / home store** | The per-tenant replicated group (`raft` and `barrier` tenants), or host (`volume` tenants), that holds the tenant's own records. |
| **tenant registry entry** | The tenant's record in the system tenant: what the platform needs, and nothing more. |
| **strict tenant** | A tenant whose effective isolation policy is `strict` (see [The isolation policy](#the-isolation-policy)). |

### What moves into the tenant

Every row is a per-tenant record that lives in the system store today, or is
reachable through it. The full inventory, with code locations, is in
[engineering.md](engineering.md#inventory--where-tenant-records-live-today).

| Record | Today | For a strict tenant |
|---|---|---|
| Users, and the username, email and SPKI indexes | `__tiam:{t}/__sys:users…` and `__sys:user_idx…` in the system shard; plaintext key names | home store |
| Roles, the role-name index, role bindings | `__tiam:{t}/__sys:roles…` | home store |
| User auth factors, credential lookup index | `__tiam:{t}/__sys:auth/…` | home store |
| Custodian (ceremony) auth factors and challenges | `__tiam:{t}/__sys:authfactor…` | home store |
| Invitations and redemptions | `__tiam:{t}/invites/…` | home store |
| Permission-scope overrides | `__tiam:{t}/__sys:perm_scope/…` | home store |
| DEK keyring | `__tiam:{t}/__sys:dek/keyring` | home store |
| Identity index (user and role id → tenant) | `system/__sys:identity_idx/…` | **removed** — see [Identity routing without a global index](#identity-routing-without-a-global-index) |
| Tenant CA private key and CA certificate | `__sys:cert_ca/{t}/…`, wrapped by the writing node's key | home store. Only the CA certificate, which is public, is also published to the system tenant. |
| Issued-certificate records | `__sys:certs/{t}/{serial}` | home store |
| Revocation of tenant-issued certificates | `__sys:certs_revoked/{serial}` (global) | home store, enforced by the serving replicas when they authenticate the tenant's users |
| Custodian records and indexes | `__sys:custodian…` (unconfined root store) | home store |
| SO PIN commitment | `__sys:so-pin-commit/{t}` (unconfined root store) | each node's tenant bootstrap record (below), because it gates operations on a sealed tenant |
| Custodian groups and submitted Shamir shares | `custodian-groups/{id}` and `shares/{group}/{index}`, unconfined, with **share bytes in the replicated store** | Groups go to the home store. **Share bytes are never persisted anywhere**: they are held in protected memory on the node that receives them, as the share-holder model requires. |
| Tenant officer roster | tenant barrier store; **falls back to the replicated system store before init** | Home store, plus the unseal roster in each node's tenant bootstrap record. Before init, the roster is held in memory by the init ceremony. |
| Database catalog | node-local `meta.json` on the creating node only | home store, as one replicated catalog for the tenant |
| Collection work journal, drop fence, reclaim index, locks, leases, sequences, row locks, index catalog, time series | system shard under `{t}/…` and `__sys:…/{t}/…` | the database's own store, per the databases binding design. Tenant-wide journals go to the home store. |
| Tenant-scope encryption-migration records | `__sys:encmig/{t}/{db}` | home store |
| Audit events about the tenant's identities and data | one node-local file shared by every tenant | a per-tenant, hash-chained trail, node-local on each serving node, under that node's tenant barrier (S9). Platform events about the tenant go to the platform trail. |
| Identity-object usage counters (`CurrentUsers`, `CurrentRoles`, `CurrentCertificates`, `CurrentCollections`, `CurrentKeys`, `CurrentTSMetrics`) | on the tenant registry entry | home store; the home group enforces them against the plan's limits |
| Vector namespace names in metric labels | `qrdb_vector_*{tenant, namespace}` | the namespace label is removed; a tenant's data names are not metric labels |

### What stays in the system tenant, and why

This list is closed. A new record keyed by a tenant may enter the system tenant
only by being added here with its reason, and the residue test enforces that.

| Record | Key | Why it cannot move |
|---|---|---|
| **Tenant registry entry** | `__sys:tenants/{id}` | Every node must place, route, admit or refuse a tenant before it is unsealed, and on nodes holding none of its keys. Quotas and entitlements are the platform's contract with the tenant, enforced on resources the platform owns. |
| **Tombstone** | `__sys:tenants-deleted/{id}` | It is the permanent, replicated proof that an id is retired. Every node checks it to refuse re-creation and to reclaim its own disk. A store that dies with the tenant cannot hold it. |
| **Placement** of the home group and of every database group | `__sys:placement/{tenant}/…` | Every node's reconciler needs it to start, stop and purge shards, including while the tenant is sealed. Databases are named by **opaque id**, never by name (S5). |
| **Volume-pool slot assignment** | `__sys:volumepool/assign/{id}` | Volume pools are a platform resource. The assignment must be agreed cluster-wide before the tenant has any store to hold it. |
| **Cluster seal record** for the tenant scope | `__sys:cluster/seal/tenant/{t}` (cluster-wide sealing design) | Every serving node must learn from the replicated system that the tenant is sealed, including a node that cannot reach the tenant's own group. It holds no key material, and names its principal by an opaque reference. |
| **Tenant CA certificate (public)** | `__sys:cert_ca_pub/{t}` | A serving node verifies a client's chain before it reads anything else about the client. A public certificate discloses nothing the client does not already present. |
| **Cluster isolation floor** | `__sys:cluster/isolation_policy` | Cluster-wide, and read on every authorization decision. |

What the **tenant registry entry** holds:
- id, display name, isolation type;
- barrier mode (self-managed or system-managed);
- the tenant's own isolation policy;
- lifecycle state;
- serving set (home group id and members);
- plan (quotas and entitlements);
- platform usage counters (databases, Raft groups, replicas, storage bytes,
  backups);
- for a self-managed tenant not yet initialized only, the **pending first
  admin**: the username and the SPKI thumbprint from the creation CSR. It is
  deleted by initialization (see
  [Bootstrapping and unsealing](#bootstrapping-and-unsealing)).

Two fields are dropped from today's record. The replicated `DataRoot` goes,
because it is one node's path and so is node-local state. The identity-object
counters go too; they have moved to the home store.

The following records are **node-level**, not system-tenant, and stay where
they are:

| Record | Where | Why |
|---|---|---|
| Tenant root record (`DataRoot`, barrier mode) | `__registry:tenant_root/{id}` in the node-local seal store | A node must find the tenant's sealed store on its own disk before anything is unsealed. |
| **Tenant bootstrap record** (new): this node's sealed replica-key blob, its credential verifier, the unseal roster (officer usernames and SPKI thumbprints), and the SO PIN commitment | The tenant's node-local barrier store, outside the encrypted region, next to the sealed blob as today | Unsealing needs it while everything else is sealed. Its integrity is bound to the replica key and re-verified after every unseal. |
| Rate-limit buckets | node memory | Platform self-protection. Never persisted. |
| Platform audit trail | node-local, hash-chained file | It records platform actions: tenant create and delete, plan changes, seal and unseal requests, policy changes, operator access and passthrough relays. A tenant's own events are in the tenant's trail. |

**No `prefix` tenant is strict (S2).** A `prefix` tenant has no key and no
store of its own, so it cannot hold its records anywhere but the system shard.
The consequences:

- Under a `strict` cluster floor, creating a `prefix` tenant is refused with
  `tenant.StrictRequiresDedicatedStorageError`. The same refusal applies when
  a `prefix` tenant asks for the strict policy for itself under an
  `operator-access` floor.
- A `prefix` tenant becomes strict only by changing its isolation type, which
  is refused while it holds data.
- The system tenant is the platform itself. It stays `prefix`, and the floor
  does not apply to it.

So a `strict` cluster can host tenants only where `storage.volume_pools` is
configured. Without a pool, tenant creation fails with the existing
`volume pool not configured` refusal, now raised before any record is written.

### Per-replica tenant keys

> **Status (2026-10-06): BLOCKED, verified — not implementable yet, for any
> barrier mode.** An implementation of the System-managed row below
> (`BarrierRegistry.ProvisionSystemManagedReplica` calling
> `StorageBarrier.Initialize` locally, with `ExportRootKeyContext`/
> `ImportRootKey` deleted) was built and run against a real 3-node cluster.
> Every authenticated request to a node holding an independently-keyed
> replica failed: `pkg/user/store.go`'s `GetUserByUsername` returned
> `quicraft/seal: decryption failed`. See the correction in
> [Per-node-independent key design](#per-node-independent-key-design) above
> for the root cause and citations — in short, tenant IAM rides the system
> shard's cluster-wide Raft replication as pre-encrypted ciphertext
> (`pkg/seal/tenant_shared_kvstore.go`), so every node needs the SAME tenant
> key regardless of database placement. The change was reverted; this
> section's table and the rest of its text remain the target design, not
> something to attempt again before the home store
> ([Strict tenant isolation](#strict-tenant-isolation--design)) lands. The
> Self-managed rows have the identical blocker and were never attempted for
> that reason.
>
> **Re-checked (2026-10-06) after the isolation-policy slice shipped: still
> blocked, in every mode.** That slice added the isolation-policy setting and
> removed the system tenant's `ScopeSelf` lift; it moved no record. Tenant
> identity routing does not consult the isolation policy at all:
> `defaultStoreResolver.Resolve` (`pkg/tenant/store_resolver.go`) sends every
> `barrier`/`volume`/`raft` tenant to `resolveBarrier`, which still wraps the
> **system shard** in `seal.NewTenantSharedKVStore`, so a `strict` tenant's
> users, roles, factors and DEK keyring are still Raft entries in the system
> shard, encrypted under its root key before replication. Per mode:
>
> | Tenant | Shared tenant key needed? | Why |
> |---|---|---|
> | `prefix` (and the system tenant) | not applicable | It has no tenant key: its records are in the system shard under each node's own node barrier, which is already per node. |
> | dedicated, effective policy `strict` | **yes, until its home store ships** | Its IAM is still pre-encrypted system-shard entries (above). Per-replica keys arrive with the tenant home group, whose Raft log is under each node's tenant barrier (S4); before that, a replica with its own key cannot read a record another node wrote. |
> | dedicated, effective policy `operator-access` (or unset floor) | **yes, by design** | The home store is defined for strict tenants only ([Shape chosen](#shape-chosen), item 1); a non-strict tenant's identity stays in the system shard, so every node that authenticates it must open the same ciphertext. |
>
> So `ExportRootKeyContext`, `ImportRootKey`, `ImportSealedBlob` and the
> tenant-key delivery in `pkg/clusterkeys` stay, and stay the only key path,
> until the home store lands; they are then removed for strict tenants. An
> owner decision is still needed on whether non-strict dedicated tenants keep
> a shared key permanently (the table's last row) or also get a home store.

> **Decided (owner, 2026-10-08):**
> [../decisions/tenant-barrier-per-node-init.md](../decisions/tenant-barrier-per-node-init.md).
> The table below is unchanged until the work packages that replace it land.
>
> - Init is node-local where no client credential is needed, and fanned out by
>   the client where one is (Option A).
> - A passphrase joiner gets the tenant-wide verifier over HPKE from a serving
>   peer.
> - A KMS `KeyRef` names one tenant-controlled key by default; a per-node
>   template is allowed. `KeyRef` is opaque to qrdb: qrdb ships no hardware or
>   cloud key code, only the seam an injected go-xkms strategy reads it
>   through.
> - **One mechanism for every barrier.** Every barrier's strategy is resolved
>   per barrier and per node through one `seal.BarrierKeyProvider` seam. That
>   covers the node barrier (folded in), isolated tenants and databases.
>   **Shipped for tenant barrier init (PN-1)** — see
>   [barrier-seal](../barrier-seal/architecture.md#the-barrier-key-seam--barrierkeyprovider).
>   The node and database barriers follow.
> - **Auto-unseal is the tenant's choice.** A platform-sealed key is not
>   refused under any policy. The statement's custody records the tenant's
>   opt-in, and the tenant's barrier status shows it, so the choice is
>   auditable.
> - **Shared tenants have no keys of their own.** The shared Raft group is one
>   more tenant with its own barrier, which the platform manages. **A shared
>   tenant's data is readable by the platform.** A tenant that needs
>   confidentiality from the platform chooses an isolated type.
> - **The serving set is the tenant's replica count,** a tenant quota the
>   system owner or a system admin sets through the existing quota and
>   entitlement records. A tenant admin cannot change its own.
> - **Opaque database-id placement (MT-17) is not a prerequisite.**
>
> **Shipped (PN-2):** the signed, keyless init statement
> (`pkg/barrierinit.InitStatement`), its verification, its guarded claim and
> compare-and-set under `__sys:cluster/barrier/init/<barrier>`, the watcher
> that delivers each verified epoch to a serving node, and the
> `per-node-barrier-keys` feature gate. See
> [../barrier-seal/architecture.md](../barrier-seal/architecture.md#per-node-barrier-keys)
> and [Feature gate and upgrade](#feature-gate-and-upgrade). Every node,
> standalone or clustered, composes the watcher, which initialises the node's
> own key for a tenant it serves from a claimed statement, and onboarding pins the
> tenant's first admin as the statement's anchor. Every transport's tenant
> barrier init carries the statement the owner's client signs and hands it to
> the tenant service, which claims it through the init book. The feature is
> advertised by every binary and gates both an isolated tenant's admission and
> the claim. Key delivery (`DistributeTenantKey`) still runs beside the
> watcher; each node keeps whichever key reaches it first and never replaces
> one.
> See [../barrier-seal/architecture.md](../barrier-seal/architecture.md#how-a-node-takes-part-tenant-barriers).

**A replica key is created on the node that uses it, the first time that node
holds a valid credential for the tenant, and it never leaves that node.**

| Barrier mode | How a node's replica key is wrapped | What a node needs to unseal it |
|---|---|---|
| **System-managed** | under that node's own node barrier, as today | nothing beyond its own unsealed node barrier; it auto-unseals |
| **Self-managed, passphrase** | under a KEK from Argon2id over the passphrase and **this node's** salt | the passphrase, presented to this node |
| **Self-managed, Shamir** | under an **unseal key** reconstructed from M-of-N shares (today's unseal-key indirection). The unseal key is common to the tenant; every replica key under it is independent. | a quorum of shares, presented to this node |
| **Self-managed, hardware** (injected from go-xkms) | under a key in that node's own hardware | that node's hardware, which by nature binds each node to its own device |

What this buys, measured against today:

- **One node's compromise stays on that node.** Compromising one node's disk
  and memory yields that node's replica key and nothing else; the other
  replicas' keys were never on it. Today the same compromise yields the key
  every replica shares.
- **The tenant's credential is the only common secret.** It travels from the
  credential holder's client to each node, and never from node to node.
- **`pkg/clusterkeys` stops carrying tenant keys.** `ExportRootKeyContext`,
  `ImportRootKey`, `ImportSealedBlob` and the tenant-key delivery modes are
  removed, not kept for compatibility. `clusterkeys` keeps its node-identity
  checks, and delivers a **credential verifier** to a new replica (below),
  which is not a key.
- **A system-managed tenant is strictly weaker, as today.** Its replica keys
  are wrapped under node keys, so whoever controls a node's root controls that
  node's copy. It is protected from the operator's API reach, not from the
  node's own root. The `AllowSystemManagedBarrier` entitlement keeps gating it.

**Raft snapshots and data movement.** Raft already moves state between nodes
whose node barriers have different keys; every system-shard snapshot does it
today. The sender reads through its own barrier, the bytes cross node mTLS, and
the receiver writes through its own barrier. The tenant groups use the same
path, with the tenant barrier in place of the node barrier. No ciphertext
crosses a node boundary.

**The Raft log of tenant groups — S4, ruled: under the tenant key from day
one.**
- **A strict tenant never has a log under a node key.** Every database group
  of a strict tenant, and its home group, writes its Raft log to a
  **dedicated WAL** under that node's tenant barrier from the first entry
  (G1). Volume tenants already do this on their own host. `raft` and
  `barrier` tenants use the existing dedicated-WAL mechanism: the multiplex
  LogDB (`pkg/database/multiplex_logdb.go`) over go-quicraft's per-shard
  barriers.
- **No fallback (G2).** A shard whose dedicated WAL cannot be opened does not
  start. Today it falls back silently to the shared WAL; that path is
  deleted, and the refusal is `TenantWALUnavailableError`.
- **Every run mode (G3).** The multiplex is wired on single-node hosts and on
  the SDK node, so the guarantee does not depend on how a node is deployed.
- **Seal ordering (G4).** Sealing a tenant on a node stops and unregisters
  the tenant's shards there before its barrier seals. A sealed WAL is
  therefore never part of a commit.
- **Recovery (G6).** A shard that failed is evicted from the host, and
  restarted when the tenant is unsealed.
- **Per-node keys (G10).** Each node's tenant WALs are keyed by that node's
  own replica key (see above).
- **Rotation (G7)** of a tenant WAL's key epochs is a later phase.
- **G5 is a go-quicraft defect:** one sealed WAL fails a whole commit batch.
  It is raised with go-quicraft. Until it is fixed, the G4 ordering is what
  keeps a sealed WAL out of every batch.

### Identity routing without a global index

The global id → tenant index exists so that a by-id operation from a
system-tenant caller can find the tenant that holds the id. Under this design,
by-id operations never cross tenants:

- **An id is resolved in the caller's own tenant.** A caller can only act in
  its own tenant, so the question "which store?" always has one answer.
- **Role-id capture becomes impossible by construction.** The index's claims
  exist to stop one tenant redirecting another tenant's by-id routing. With no
  shared routing table, there is nothing to redirect.
- **Under `operator-access`, an operator names the tenant explicitly**
  (`qrdb users get --tenant acme <id>`). The id is resolved in that tenant's
  store, on that tenant's serving set.

`tenant.IdentityIndex`, `tenant.OwningTenant` and the system-namespace index
records are removed.

### Connecting through a node outside the serving set — layer-4 passthrough

A node outside a tenant's serving set holds none of the tenant's records and
none of its keys, by design. It cannot authenticate the tenant's users and must
not try. The owner ruled (S6) that it relays the client's **still-encrypted**
connection to a serving node. It never redirects, and it never forwards an
on-behalf assertion.

```
client --TLS ClientHello (SNI acme.<tenant-sni-suffix>)--> node X  (not serving acme)
   X: peeks the ClientHello, does NOT terminate TLS
   X: reads tenant "acme" from the SNI; reads acme's registry entry: serving {A,B,C}
   X: opens a node-mTLS relay tunnel to A, sends {tenant, client address},
      then splices client bytes <-> tunnel bytes unchanged
   A: accepts the relayed stream only from an authenticated cluster node
   A: terminates the CLIENT's TLS itself, so proof of possession is observed HERE
   A: requires: client chain -> acme's own CA; cert tenant == SNI tenant
   A: looks the user up in acme's home store; checks binding, lifecycle,
      revocation and auth methods; authorizes; serves
```

- **The tenant is read from SNI.** That is the only routing input visible
  before TLS is terminated. Isolated tenants are addressed as
  `<tenant>.<tenancy.sni_suffix>`, so:
  - a strict tenant's id must be a valid DNS label;
  - every node's server certificate carries `*.<tenancy.sni_suffix>`.

  The SDK and CLI set the SNI from the tenant they are configured for.
- **The serving node binds the SNI to the certificate.** A client whose
  certificate names a different tenant from its SNI is refused with
  `tenant.TenantSNIMismatchError`. Otherwise a client could steer a relay to
  the wrong tenant's serving set.
- **The relay is a byte pipe inside an authenticated tunnel.** The relaying
  node never holds the client's TLS keys, and so it cannot read or alter the
  session. The node-to-node tunnel exists for three reasons:
  - the serving node accepts relayed streams only from cluster nodes;
  - the platform trail records the true client address;
  - a relay cannot be used to reach a serving node's client port from outside
    the cluster under a forged source.
- **QUIC** is relayed the same way. The relaying node reads the SNI from the
  QUIC Initial packet, whose protection keys are derived from public values,
  and routes the connection's datagrams by connection id for its lifetime.
- **Unix socket and embedded** are node-local by nature and have nothing to
  relay. On a node outside the serving set they answer with
  `tenant.TenantNotServedHereError`, naming the serving set.
- **A client that sends no tenant SNI** is terminated locally, as the system
  tenant's own clients are. If its certificate then names a strict tenant
  this node does not serve, the node refuses it with `TenantNotServedHereError`.
  It has already terminated the connection, so it cannot relay it, and it does
  not look the user up.
- **What the relay discloses.** SNI is plaintext on the wire, so a network
  observer sees which tenant a connection is for. Encrypted ClientHello would
  hide it and is a later option; it is not part of this design.

### Bootstrapping and unsealing

A self-managed tenant's identity lives inside the store that is sealed. The
credential that opens the store therefore cannot be authorized by anything
inside it. The design breaks the cycle in three places.

> **Decided (owner, 2026-10-08):**
> [../decisions/tenant-barrier-per-node-init.md](../decisions/tenant-barrier-per-node-init.md).
> The text below is unchanged until the work packages that replace it land.
> The joiner's verifier over HPKE from a serving peer is decided, and a
> platform-sealed key stays the tenant's choice under either policy. The
> design differs from the text below in five places:
>
> - **The init parameters are signed.** The client signs the init statement
>   with the same `crypto.Signer` as the init proof, so every node verifies
>   the parameters, not only the connection. The statement, its verification,
>   its guarded claim and its transport on every init are shipped; the
>   fan-out of a passphrase or Shamir init to every serving node is not yet.
> - **Shamir has no pending key.** A Shamir node holds no pending key: it
>   generates its key when a share quorum, checked against the statement's
>   unseal-key check value, first reaches it.
> - **The first admin waits for quorum.** It is written by the home group's
>   leader, with guarded writes, once a quorum exists, and not inside the
>   first node's init response.
> - **A passphrase node computes one tenant-wide verifier** from the
>   statement's salt, so the serving nodes need no verifier delivery between
>   them. Only a later joiner needs one.
> - **A node that missed a credential change** refuses the old credential, and
>   catches up with the old and new credentials together.

**1. Creation: the tenant's first admin is fixed by the requester's own CSR
(S7). Already shipped** (`admin_username` plus `admin_csr_pem` or
`admin_cert_pem`, 2026-10-03) **for every tenant, not only a strict one** —
see [Creating a tenant](#creating-a-tenant-onboarding-with-its-first-admin).
What this design adds on top:

- The platform writes the registry entry, places the home group, assigns pool
  slots, and records the pending first admin (username and CSR SPKI
  thumbprint) — the same record `tenantadmin` already keeps today, moved into
  the home store once it exists.
- **System-managed tenant.** The home group initializes at once. In the home
  store, create:
  1. creates the tenant's CA;
  2. creates the first admin user, with its SPKI binding;
  3. issues the admin certificate from the CSR.

  The certificate is returned in the create response. A certificate is
  public, and the key never left the requester. Nothing is left pending.
- **Self-managed tenant.** The home store cannot be written until the
  tenant's own credential initializes it, so the pending first admin waits in
  the registry entry until barrier init consumes it.
- **Substitution is detectable.** Whoever calls create chooses the CSR, and a
  platform that substituted its own CSR would hold the first admin. But the
  real requester then cannot complete initialization, because the proof below
  fails for its key. The init response and `barrier-status` both report the
  bound admin's SPKI thumbprint, which the requester compares with its own.

**2. Initialization: proven by the CSR key; each replica's key born on that
replica.**

- **The client proves possession of the pending first admin's key.** It uses
  the officer-claim pattern: `init/begin` returns a nonce, and `init/complete`
  carries a signature over that nonce bound to the TLS exporter of the
  connection. The connection needs no tenant client certificate, because none
  can exist yet.
  - This proof replaces trust-on-first-init. A certificate naming the tenant
    no longer opens the window, so a certificate minted by anyone else is
    useless.
  - **Shipped (2026-10-14), on REST, gRPC, the Unix socket, QUIC and MCP.**
    The nonce is single-use, lives five minutes, and verifies only against
    the exporter of the TLS 1.3 connection that asked for it; the SDK runs
    both calls on one connection and signs with a caller-supplied
    `crypto.Signer`, so a device-held key never leaves its device. Today the
    connection is still authenticated by the first admin's certificate issued
    at create, and the proof is required on top of it; the embedded
    transport, which is in process and has no connection, asks no proof.
    Dropping the certificate and fanning out (below) ship with phase 7.
- **The client sends init to every node in the serving set.** No node relays
  it to another; the SDK and CLI do this fan-out.
  - **Passphrase:** each node generates its own random replica key and seals
    it under a KEK derived from the passphrase and its own salt.
  - **Shamir:** the first node generates the unseal key and splits it to the
    enrolled officers. The response is share-free, as today. That node seals
    its replica key under the unseal key. Each other node generates its own
    replica key and holds it **pending** until the first unseal ceremony
    presents a quorum to it; it then seals the key under the reconstructed
    unseal key and unseals.
- **The first node to initialize writes into the home store**: the tenant CA,
  the first admin user and its SPKI binding, and the admin certificate issued
  from the CSR. It then deletes the pending first admin from the registry
  entry. The other nodes receive these records through Raft once they unseal.
- **Every node records a credential verifier** in its tenant bootstrap record.
  A node meeting the credential for the first time can then tell a correct one
  from a wrong one before it seals a new key under it.
  - For a passphrase, the verifier is an Argon2id tag.
  - For Shamir, it is a self-authenticating unseal key: the split secret
    carries its own MAC.
  - A replica added later gets the verifier through the existing `clusterkeys`
    HPKE delivery. That delivery is placement-gated, revocation-checked, and
    addressed to that one node.
  - A verifier is not a key, but it is an offline-guessing oracle for a
    passphrase. It therefore goes only to serving replicas, and never into the
    replicated system store. The sealed blob on every serving node is already
    the same oracle.
- System-managed tenants skip all of this. Each node generates its replica
  key and wraps it under its own node barrier when placement assigns it.

**3. Unseal: authorized by the credential and a tenant-CA certificate, never by
tenant RBAC.**

- **The sealed window stays, narrowed.** It admits, for status and unseal
  only, a caller whose certificate **chains to the tenant's own CA**, whose
  public certificate is in the system tenant. A certificate from the cluster
  CA naming the tenant is not enough. The credential itself is still the real
  gate.
- **The roster and SO PIN commitment come from the node itself.** They are
  read from the node's own tenant bootstrap record, because the home store is
  still sealed. After unseal, the node re-verifies both against the home
  store's copy. The bootstrap record is bound to the replica key by a MAC
  computed at seal time. A mismatch seals the tenant again on that node and
  raises `seal.BootstrapRecordMismatchError`.
- **Credentials travel only from the holder.** Shares and passphrases go from
  the holder's client to each serving node, and are never relayed node to
  node. A share is held in protected memory on the node that received it, and
  counted once per holder, per the unified share-holder model
  ([../shamir-custody/](../shamir-custody/)).

**The home group starts on a node only when that node's tenant barrier is
unsealed.** Until a quorum of the serving set is unsealed, the home group has no
quorum. Every request for the tenant is then refused with
`TenantHomeUnavailableError` (503, retryable), except the identity-gated
barrier endpoints.

**A system-managed tenant's key reaches every serving node at once.** The node
that creates the home group delivers the tenant key to all the other serving
nodes concurrently (`clusterkeys.Sender.DistributeTenantKey`), and reports
every failed delivery, not only the first. A node answers a delivery only after
it has imported the key and unsealed. Its unseal audit event belongs in the
tenant's own trail, which lives in the home group, so recording that event can
wait for the home group to reach quorum. If the deliveries went one node at a
time, the first node's answer would wait on deliveries still queued behind it,
and a home group needing more than one follower for quorum could never form.

### The isolation policy

**A cluster floor and a per-tenant setting (S1).**

| Setting | Where | Values | Who sets it |
|---|---|---|---|
| Cluster floor `tenancy.isolation_policy` | `__sys:cluster/isolation_policy` | `strict` (default) or `operator-access` | First-boot configuration, recorded once. It can be tightened later (`cluster:set_isolation_policy`, ScopeCross), never loosened. |
| Tenant policy | the tenant's registry entry | `strict` or `inherit` (default) | The requester at create (`isolation_policy: strict`), or later by the tenant's own Admin (`tenants:set_isolation_policy`, ScopeSelf). Tighten-only; a tenant's `strict` cannot be withdrawn. |

The **effective policy** is `strict` when either setting is `strict`. Every
authorization decision reads the effective policy of the **target** tenant.

**Why it only tightens.** A tenant that was promised strict isolation must not
lose it for data it already stores. Loosening is refused with
`tenant.IsolationPolicyChangeError`.

Every tenant can read the floor and its own effective policy
(`GET /v1/tenants/{id}` carries both), so it knows which promise it holds.

| | Effective `strict` | Effective `operator-access` |
|---|---|---|
| Where tenant records live | in the tenant's home store; the tenant must be `barrier`, `volume` or `raft` (S2) | in the home store for isolated tenants; in the system shard for `prefix` tenants |
| A system-tenant identity in this tenant | refused for every permission that is not cross-scope | the same, plus the named operator permissions below |
| `ScopeSelf` system lift | **none** | **none** |
| Operator permissions over tenant identity (`tenant_identity:read`, `tenant_identity:write`) | refused for everyone, with the policy named in the refusal | Effective only for system-tenant roles that carry them explicitly; no built-in role does. Every use is written to the platform trail **and** the tenant's own trail. |
| Tenant databases and data (KV, SQL, documents, vectors, time series, etcd, backups of tenant groups) | never, for anyone outside the tenant | **never**, for anyone outside the tenant |
| Role bindings naming another tenant | refused | refused |
| Minting a certificate for another tenant | refused. The tenant's CA key is in the tenant's store, and a cluster-CA certificate is not accepted for a strict tenant's users. | refused. `tenant_identity:write` may add, list or withdraw an SPKI binding on a tenant user, but never signs with the tenant's CA. |
| Deleting the tenant | the platform may, through `tenants:delete:cross`, audited (S8); the tenant's own Admin may, through `tenants:delete` | the same |

**How it maps to RBAC.**

- **`ScopeSelf` loses its system lift in every mode.** The system tenant is
  confined to its own tenant, like every other tenant. `ScopeSelf` becomes the
  rule that `ScopeSelfStrict` is today, and `ScopeSelfStrict` is folded into
  it.
- **`tenant.EffectiveTenant` stops honouring a requested tenant from a
  system-tenant caller.** A request naming another tenant is refused with
  `CrossTenantDeniedError`, unless the operation is one of the cross-scope
  verbs.
  - **`tenant.OwnTenant`** (`sdk.OwnTenant`) already gives a caller this exact
    rule today, ahead of this migration: it is `EffectiveTenant` with no
    system-tenant exception at all, for a caller that must enforce "no
    caller, system included, ever acts on another tenant" now rather than
    wait on the floor above (#587). It is a second function, not a
    flag on `EffectiveTenant` — qrdb's own IAM routing keeps using
    `EffectiveTenant` until this migration lands.
- **Role bindings name the holder's own tenant only.** The system-tenant
  exception in `user.NormalizeRoleBindings` is removed.
- **The platform plane** is the `ScopeCross` permissions, and the policy does
  not affect it. Those permissions act on system-tenant records only:
  registry, plan, tombstones, placement, pools, cluster membership, node
  barriers and the platform audit trail. Two verbs are new:
  - `tenants:read:cross` reads another tenant's registry entry;
  - `tenants:delete:cross` deletes another tenant.

  They replace the system lift that `tenants:read` and `tenants:delete` enjoy
  today.
- **The operator-identity plane** is `tenant_identity:read` and
  `tenant_identity:write`. Both are `ScopeCross`, and both are effective only
  when the target tenant's effective policy is `operator-access`. Each request
  names its tenant explicitly and is served on that tenant's serving set.
- **A platform seal of a tenant** (cluster-wide sealing, decision D3 there) is
  a platform-plane verb under every policy. A seal discloses nothing, and
  unseal stays with the tenant's own credential holders.

### Backups of a strict tenant (S10)

- **The rule.** The platform takes a tenant's backup only if the archive it
  produces can be opened by the tenant's credential alone, **and** the
  platform never reads the tenant's data while making it. Otherwise, backups
  of a strict tenant's groups are tenant-only (`backup:tenant`,
  `ScopeTenantOnly`).
- **What the first build ships.** The platform-made form needs each serving
  node to encrypt its snapshot stream under a key derived from the tenant's
  credential, not from its replica key; a replica key never leaves its node,
  so it cannot open an archive anywhere else. That derivation needs the
  credential present at backup time. The first build therefore ships
  **tenant-only** backups.
- **What would add the platform form.** A design that lets an unattended
  platform job produce such an archive without the credential and without
  reading the data. Until then, platform shard backup covers the system shard
  only.

### Interaction with cluster-wide sealing

The cluster-wide sealing design
([../barrier-seal/architecture.md](../barrier-seal/architecture.md)) adds a
replicated, epoch-numbered seal record per scope, enforced on every node. This
design depends on that record, and constrains it:

- **The tenant scope's serving set is this design's serving set.** It is the
  set of home group replicas, and every database group of the tenant is placed
  inside it.
- **Its unseal model must be per node.**
  - Option D1-B there (the unsealing node delivers the root-key context to the
    other replicas) moves a key between nodes. The owner rule forbids that,
    and under this design there is no common root key to deliver.
  - "Unseal the cluster" becomes one operator action through **client
    fan-out** instead: the CLI and SDK present the credential, or each
    holder's share, to every serving node.
  - Each node verifies the credential against its own verifier and unseals its
    own replica key.
- **Rekey is per node.** There is no shared root key to rotate.
  - A credential change (a new passphrase or a new share set) re-wraps each
    node's replica key, by client fan-out.
  - A replica-key rotation is node-local, and re-encrypts that node's copy
    only.
  - Old shares stop working on a node as soon as that node re-wraps. The
    cluster seal record's epoch is the fence that stops a node still on the
    old credential from serving.
- **The seal record names its principal opaquely.** A tenant username is a
  tenant record and does not go into the system store; the full detail goes
  to the tenant's own trail.
- **A joining node comes up sealed.** A node added to a tenant's serving set
  gets the verifier, not a key, and unseals at the next fan-out. For a
  system-managed tenant, it generates its key on arrival.

### Feature gate and upgrade

- **The new behaviour ships behind the cluster feature gate**
  (`pkg/cluster/featuregate`), as feature `strict-isolation`. Until every node
  advertises the feature, the strict floor and per-tenant strict are not
  applied. That covers the home groups, the passthrough relay, the removal of
  the system lift and the removal of key transport.
- **While the gate is closed, the existing behaviour holds.** A cluster still
  reaching activation is in a defined state, never half-migrated. Once the
  feature is activated, a binary without it refuses to start, which is the
  gate's existing downgrade rule.
- **No compatibility layer.** Pre-release tenants are re-created, not
  migrated. `__tiam:`, the identity index, tenant-key delivery and the shared
  resolver branch are deleted outright.

**`per-node-barrier-keys` (`featuregate.PerNodeBarrierKeys`)** is a second
activation-based feature, built like `cluster-seal`. It adds no Raft command:
the init statement is written through the existing guarded write.

- **Why it exists.** An isolated tenant's replicas each initialise their own
  key from the replicated init statement. A node whose binary does not know
  the statement would wait for a key delivery that never comes, so an
  isolated tenant is not admitted until every member advertises the feature.
- **Not yet active:** creating a `barrier`, `volume` or `raft` tenant, or
  changing an existing tenant to one of those types, is refused with
  `*featuregate.NotActiveError{Feature: per-node-barrier-keys}` (503,
  retryable after rollout) and writes nothing. A `prefix` tenant is unaffected.
  Recording an init statement is refused the same way.
- **Active:** on a clustered node, once every member advertises the feature;
  a single-member cluster activates at its first check. A standalone node
  gates nothing and is active from its first start.
- **Guarded writes.** The statement's claim and compare-and-set are guarded
  writes, so a cluster that has not activated `guarded-writes` refuses them
  with `*barrierinit.GuardedWritesUnavailableError` (503) rather than write
  them unguarded. Onboarding's first-admin anchor pin is a guarded write too.
  `guarded-writes` activates by the same rule, and every binary that knows
  `per-node-barrier-keys` knows it, so the refusal is seen only transiently
  during a rolling upgrade; nothing activates it by hand.

### Alternatives rejected

- **Keep identity in the shared store, encrypted.** Rejected for three
  reasons. It needs one key on every node, which is the property being
  removed. The key names leak usernames, emails and thumbprints whatever the
  values hold. And "readable on every node" is exactly the reach strict
  isolation forbids.
- **Encrypt the key names too, as an opaque keyed hash per key.** Rejected.
  It still needs a common key, still leaves record counts and churn visible,
  and gives up every range scan the identity stores rely on.
- **Derive each node's key from one tenant root with a per-node salt.**
  Rejected. The salts are not secret, so one compromised root yields every
  node's key; it is the shared key with an extra step.
- **Deliver the tenant key at unseal time from the unsealing node.**
  Rejected: it is key transport, which the owner has ruled out.
- **Redirect the client to a serving node.** Rejected by the S6 ruling in
  favour of passthrough. A redirect makes every client implement reconnection
  and exposes the serving set to any holder of a tenant certificate.
- **On-behalf forwarding from a non-serving node.** Rejected. The serving node
  would trust another node's claim of proof of possession, so a compromised
  non-serving node could act as any user whose public certificate it had
  seen.
- **An operator-held one-time bootstrap credential.** Rejected by the S7
  ruling. The platform would hold, even briefly, a credential that creates the
  tenant's first admin.
- **One global "strict" bit per permission.** Rejected: two tenancy mechanisms
  would drift apart. The policy is one input to the one scope rule.
- **Per-database groups only, with identity on "the first database".**
  Rejected. Identity is tenant-wide, and a tenant with no database still has
  users.

### Rulings

Owner rulings of 2026-10-01 and 2026-10-02. **FINAL.**

| # | Question | Ruling |
|---|---|---|
| **S1** | Cluster-wide only, or per tenant? | **B.** The cluster sets a floor. A tenant may opt into `strict` under an `operator-access` floor. |
| **S2** | May `strict` include `prefix` tenants? | **B.** No. Under strict, every tenant has storage of its own. |
| **S3** | What becomes of `barrier` mode? | **A.** Barrier tenants get a home group. |
| **S4** | Must a strict tenant's Raft log be under the tenant's key? | **Yes, from day one** (2026-10-02). A strict tenant never has a node-key log. Use the existing dedicated-WAL mechanism, closing the gaps the go-quicraft analysis found: G1 mandatory, G2 fail-closed, G3 every run mode, G4 seal ordering, G6 evict and restart, G10 per-node keys, and G7 rotation as a later phase. G5 is a go-quicraft defect, raised upstream. |
| **S5** | May database names appear in system records? | **B.** No. System records use opaque database ids. |
| **S6** | How does a non-serving node reach the serving set? | **B.** Layer-4 passthrough of the still-encrypted connection. Never a redirect; never an on-behalf forward. |
| **S7** | Where does a tenant's first admin come from? | **B.** From a CSR supplied at tenant create. No operator-held bootstrap credential. **Already shipped** (2026-10-03, see the correction above) — unconditionally, not gated on this ruling. |
| **S8** | May the platform delete a strict tenant? | **A.** Yes, audited. |
| **S9** | Where does the tenant audit trail live? | **B.** Node-local on each serving node, under the tenant's key. |
| **S10** | Who backs up a tenant's groups? | **B, else A.** A platform-made archive that only the tenant can open, if it can be built without platform read access; otherwise tenant-only backups. |

Standing rules that also apply:
- **No backward-compatibility code.** Pre-release tenants are re-created, not
  migrated.
- **The cluster feature gate stays.**

Plan confirmations (2026-10-02):
- A strict floor requiring volume pools is accepted; tenant creation without
  a pool fails cleanly before any write.
- Phase 0 is accepted: the cluster tenancy suites run on every push once
  their timeouts are fixed.

Two questions are still with the owner: SNI as the passthrough routing input
(phase 9), and whether a first admin may present an existing certificate in
place of a CSR (phase 3).

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
- **A module host reaches the SAME re-check qrdb's own handlers use.**
  `sdk/go/node.Node.AuthMiddleware` / `Node.Authorizer` expose the node's live
  `*middleware.AuthMiddleware`, and a `pkg/apimodule.ServiceModule`
  implementing `ModuleTenantTargetAware` receives it (as the minimal
  `TenantTargetAuthorizer` interface) before any of its `Register*` hooks
  run — see [../api-server/architecture.md](../api-server/architecture.md)
  — so `AuthorizeTenantTarget` is reachable from a module's own handler on
  any transport, not only from qrdb's built-in tenant routes (#587).
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
- [Strict tenant isolation](#strict-tenant-isolation--design) — the ruled
  design that moves every non-required tenant record out of the system tenant.
- [deployment-multi-node-raft.md](../deployment-multi-node-raft.md) — deploying
  multi-node IsolationRaft tenant clusters end to end.
