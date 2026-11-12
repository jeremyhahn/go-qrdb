# Multi-Tenancy — Architecture

> **Status: NORMATIVE** (the strict-isolation section is RULED and partly
> built; each part names what is not built) · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

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

**Changing a tenant's isolation type after it holds data is rejected** with
`ErrTenantHasData` (HTTP 409). Migrate or delete the data first.

## Creating a tenant: onboarding with its first admin

### Problem

A tenant with no account in it is unusable, and on a dedicated tenant it is
worse: the first thing a dedicated tenant needs is its own barrier, and without
an onboarded admin the barrier's trust-on-first-init window is the only way
in, admitting **any** authenticated identity of the tenant. Whoever reaches it
first sets the tenant's key. And the platform operator is the one party that
must not hold the tenant's credential, so it cannot simply mint one and hand it
over.

### Shape chosen

**A tenant is created with its first admin's login credential, or not at all.**
`tenant create` takes the admin's username and exactly one credential the admin
already holds the key for:

- a **certificate signing request**, which the cluster signs. A `prefix`
  tenant's admin is signed by the tenant's own login CA (Key B). A dedicated
  tenant's (`barrier`, `volume`, `raft`) is signed by the cluster CA (Key A),
  the same CA that signs the system admin at first boot. The CN is the admin's
  username and the first OU is the tenant. Key B's records live behind that
  tenant's barrier, which is sealed until the admin initializes it, so Key B
  cannot sign at creation. A cluster-CA certificate's tenant comes from its
  OU, so it authenticates as the admin of that tenant and of no other. A node
  serving bring-your-own TLS has no cluster CA. There, a system-managed
  barrier serves from creation, so its Key B signs. A self-managed tenant's
  CSR is refused before the tenant is created, as first boot refuses one; its
  admin brings their own certificate instead. Composers that sign
  elsewhere inject their own signer; see
  [engineering.md](engineering.md#exported-surface); or
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
   name), binds the certificate, and deletes the record. The hook runs inside
   the request that raised it, so the account exists when the barrier init
   returns, and an init whose admin could not be created fails with the
   completion's error. It is idempotent, so a failed attempt is finished by
   the next trigger.

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

### The tenant ID is a trust domain

A tenant's principals are `spiffe://<tenant>/<kind>/<name>`: the tenant ID is
their SPIFFE trust domain verbatim (see
[identity-rbac/architecture.md](../identity-rbac/architecture.md#identity-mapping)).
An ID that cannot be one names a tenant no certificate could ever name, so the
CA could never issue its admin a certificate and the mTLS authenticator could
never admit one. Creation therefore refuses, before anything is written, an ID
that does not survive being written into a SPIFFE ID and parsed back unchanged
(SPIFFE-ID §2.1: lowercase letters, digits, `.`, `-` and `_`; an uppercase
letter is refused, not folded), and an ID equal to the cluster trust domain
(`auth.cluster_trust_domain`), under which a certificate names no tenant. The
refusal is `*tenant.TenantTrustDomainError`, an invalid-input error on every
transport, and it is enforced in the one create path every transport and the
CLI share, so no transport can create a tenant another refuses.

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

A tenant id is a name the caller chose. A delete that removed the tenant's
metadata and barrier registration but released the id would let a tenant
created under the same id re-attach the NEW principal to the OLD principal's
keys, secondary indexes, relational tables and identity records. The sequence
needs no storage access: create `acme`, populate, delete, create `acme` again,
read.

The exposure differs by isolation mode:

| Mode | What a second tenant under a reused id would get |
|---|---|
| **`prefix`** | The first tenant's data, **readable**. No cryptographic backstop exists at this rung. |
| **`barrier`** | The first tenant's ciphertext, with the key path gone. |
| **`volume`** / **`raft`** | The dedicated `DataRoot` is derived from the tenant id, so the id lands on the SAME directory over the SAME barrier store. A system-managed tenant fails with `barrier already initialized`; a self-managed tenant is created and is then permanently unusable: its barrier can be neither initialized (it already is) nor unsealed (nobody holds the first tenant's credential). Data unreadable; id bricked; directory leaked. |

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

**Why the fence is first.** Shred-then-delete leaves, on a transient failure
of the tombstone write, a live, listed, resolvable tenant whose every byte is
already undecryptable, behind an error that says the delete did not happen.
Delete-then-shred can strand a key nothing names *and* leaves the data
readable in the window. Fencing first leaves no readable window, because nothing resolves the
tenant after step 3, and leaves no live tenant without a key, because the
metadata is gone before the key is. What it can leave is a **retired id whose
barrier or keyspace still exists** — residue reachable by nobody, removed by
re-issuing the call.

A failure in steps 4 or 5 leaves the id retired and is reported as such: the
error names the retirement and the remedy, so an operator does not read
"reclaim failed" as "nothing changed" and stop. Re-issuing the delete resumes
from step 4; every step past the fence is idempotent.

### One reserved set, two policies

The reserved-id rule applies at deletion as well as at creation: an id this
system declares un-creatable is not **deletable** either, so `DeleteTenant`
refuses a caller-supplied `system` before any step commits. The reclamation in
step 5 refuses the system tenant too, but that guards the **last** step, and
steps 2 to 4 are the committing ones.

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

Step 1 bounds step 5. After the databases are dropped, what remains under a
tenant is its identity records — users, roles, permission scopes, invitations
— which the quota plane already limits by `MaxUsers` and `MaxRoles`. That is
strictly less work than `DROP DATABASE` already performs inline on the same
key-at-a-time path.

**A background reclaimer is rejected.** It would add a restartable,
idempotent, observable subsystem (a work queue, a scheduler, a progress
surface) to do less work than a call this system already makes synchronously.
An unbounded keyspace walk inside a synchronous API call would be a denial of
service on the control plane; bottom-up refusal is what bounds the walk.

The reclamation scope is nonetheless the tenant's WHOLE namespace, not just
the identity residue: the database catalog names the tenant's databases, but
a key outside every recorded database would otherwise be left behind with
nothing left to name it.

### What a tenant owns

`service.TenantKeyPrefixes` is the declaration. Eight ranges, not one:

| Range | Why it is separate |
|---|---|
| `{tenant}/` | Identity records AND every `{tenant}/{db}/` namespace, since the first contains the second. |
| `ts:{tenant}/` | Time-series points. The namespace lands INSIDE the series prefix, because it is the metric NAME the namespaced view prefixes, not the key. |
| `idx:metric:{tenant}/` | Time-series metric index, same reason. |
| `retention:{tenant}/` | Time-series retention policies, same reason. |
| `__sys:placement/{tenant}/` | A dedicated-raft database's replica assignment: keyed by tenant, OUTSIDE the namespace. The per-node reconciler watches this range, so removing an entry is what stops the shard and purges its data dir on **every** node holding a replica — which the local node's `DataRoot` removal cannot reach. |
| `__sys:collection-work/{tenant}/` | The collection work journal: one entry per collection in a transitional state, keyed by tenant OUTSIDE the namespace so the collection sweeper can find work without a database catalog. Removing the range means a deleted tenant's entries go with it instead of waiting for the sweeper. |
| `__sys:cert_ca/{tenant}/` | The tenant's certificate-CA key records, keyed by tenant OUTSIDE the namespace under the CA key provider's own prefix. |
| `__sys:cert_ca_pub/{tenant}/` | The tenant's certificate-CA public records, same reason. |

Plus the secondary-index catalog and the vector namespaces, which are
namespaces of their own rather than ranges of keys and are enumerated
separately.

The **system tenant is refused**: `system/` holds the bootstrap database and
every system identity record, so a reclamation that accepted it would empty
the store.

### Unregistering a barrier is not destroying it

`BarrierRegistry.UnregisterTenant` seals and forgets the IN-MEMORY barrier.
Two durable records outlive it — the `DataRoot` record and the volume-pool
slot assignment — and the boot path reads the first, so unregistering alone
would let a deleted volume or raft tenant's barrier be re-registered on the
next restart at the same directory over the same store. `BarrierRegistry.DestroyTenant` is the
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
A skipped directory keeps the records that name it, so the next pass tries it
again, and an undone id's acknowledgement waits until every directory is
removed or absent: the id is never reused over a directory still on disk.

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
- **A shared cluster-wide tenant key.** Rejected for the strategies a node can
  key itself (`none`, `passphrase`, `shamir`); see the per-node key design
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

**A surface missing from that list is a surface with no tenancy.** A process
singleton built over the raw, pre-namespace engines serves every tenant from
one keyspace: under `prefix` isolation two tenants naming the same database
address the same records. Document collections and distributed locks
([../distributed-locks/architecture.md](../distributed-locks/architecture.md))
are on the list for that reason: a lock record is `__sys:lock/{key}`, with the
key supplied by the caller and no tenant segment.

The "no exempt prefix" rule below confines every key a resolver-served store
handles. A process singleton is not resolver-served, so it never enters the
confined path at all. The discriminator is therefore NOT the key shape:
`pkg/permission`'s `__sys:perm_scope/{permission}` has no tenant segment either
and is safe, because it routes through `tenant.StoreResolver`. The question to
ask of any surface is: **does the writing type route through the resolver, or
resolve through the registry's namespaced engines, or does it hold a raw store
as a process singleton?**

**A service inherits isolation from the engines it HOLDS, never from the
engines its neighbours in the same bundle hold.** A bundle member that composes
other engines must be REBUILT over the wrapped ones (`applyComposite` in
`pkg/server/rest_registry.go`), not copied; a comment asserting that it
"inherits their isolation" is a claim only the construction site can make
true.

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
no tenant concept at all, so a resolver that returned the bare shared store
would give every prefix tenant one undifferentiated keyspace, and DAO/Entity is
the required default for data access.

**Identity is tenant-wide; data is per-(tenant, database).** That split is why
this plane namespaces at `{tenant}/` and not at `ComposeNamespace`'s
`{tenant}/{db}/`. The resolver-served store holds users, roles, permission
scopes and invitations, and a user must authenticate whichever database a
request names. Namespacing it per database breaks login: firstboot writes the
admin user with no database bound to its context, so it would land under
`{tenant}/system/` while every request carrying a database resolved elsewhere
and found nothing.

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

`tenant.IdentityIndex` answers it: a system-plane `id -> tenant` map in the
system tenant's identity namespace, at
`system/__sys:identity_idx/tenant/{kind}/{id}`.

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
  record.** An unindexed id routes to the caller's own tenant. A caller in another tenant is also
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
a record.

`role.ResolvePermissions` is the one by-id method that stays context-routed. It
resolves the CALLER's own bindings during authorization, so a binding naming
another tenant's role must not import that role's permissions: the index is a
routing aid, not a way to widen a grant.

**There is no exempt prefix.** Every key a resolver-served store handles is
confined, the reserved `__sys:` keyspace included. That is deliberate: an
exemption keyed on a string prefix would be a SECOND tenancy mechanism
alongside the namespace, and two mechanisms drift. Exempting `__sys:` would
let a DAO whose entity type began with `__sys:` write global records, and would
let `pkg/permission`, which keys `__sys:perm_scope/{permission}` with **no**
tenant segment of its own, apply one tenant's permission scopes to every
tenant. Confinement closes both by construction rather than by rule.

The IAM stores additionally embed tenant segments in their own keys
(`__sys:users/tenant/{tenantID}/…`). That is redundant with the namespace, not
a correctness issue: the namespace is what enforces isolation, the embedded
segment merely repeats it.

**Shared-prefix tenants have real, per-tenant, engine-maintained indexes.** A
DAO over a resolver-served store registers its indexes under
namespace-qualified names (`acme/workitem.State`) with the absolute key prefix
the tenant's records carry (`acme/workitem/`). The engine maintains an index
for exactly the keys under its scope, so one tenant's index can never hold —
or answer with — another tenant's rows, and a query is answered from the index
rather than a full scan.

The engine infers nothing from the key: an index carries the key prefix it covers and maintenance compares the key against it,
on the Raft apply path and on `KVEngine.PutWithIndexes`/`DeleteWithIndexes`
alike (see
[../secondary-indexes/architecture.md](../secondary-indexes/architecture.md#index-key-scope--a-name-is-not-a-predicate)).
The adapter (`lifecycle.kvStoreAdapter`) translates the caller's absolute key prefix into the scope the
engine composes — `engine.IndexNamespace(name)` followed by the declared prefix
— so the scope maintained is exactly the prefix the keys carry, whether the
store is namespaced at `{tenant}/`, at `{tenant}/{database}/`, or not at all.
Nothing about the tenant or database is assumed; both are read off what the
caller declared. A name whose namespace the keys do **not** live under is still
refused, because the engine would maintain it over a keyspace no write reaches.

This covers the tenants whose resolver-served store is the plaintext shared
store. The store served to `barrier`, `volume` and `raft` tenants encrypts
every value under the tenant's DEK and refuses index operations; see
"Indexes on encrypted tenant stores" below.

`kvstore.EntityIndex.KeyPrefix` states which keys an index covers, so no store
infers it from the entity type. `index.IndexConfig.KeyPrefix` is that same
statement one layer down, so the Raft-backed engine and `kvstore.MemoryKVStore`
scope index maintenance the same way.

### Indexes on encrypted tenant stores

**Not built.** The resolver serves `barrier`, `volume` and `raft` tenants their
home store (see [Strict tenant isolation](#strict-tenant-isolation--design)),
which encrypts every value under the tenant DEK and refuses every index
operation with `kvstore.ErrIndexUnsupported`; `pkg/dao` plans a full scan.

## Per-node-independent key design

A tenant barrier sealed with `none`, `passphrase` or `shamir` has its own key
on every node that serves it: each node initialises its own key through its
own `BarrierKeyProvider`, and no key is transported
([../barrier-seal/architecture.md](../barrier-seal/architecture.md#per-node-barrier-keys)).

Every other strategy does the same: each serving node initialises its own key
from the barrier owner's signed init statement, and a node placed later by
scale-out or a rebalance move keys itself on the init watcher's next poll
(`pkg/barrierinit`). The tenant-key transport is deleted: nothing ships a
root key or a sealed blob between nodes.

A tenant seal is a cluster seal record once the cluster has activated
`cluster-seal`: every node holding the tenant's barrier seals it, ends the
tenant's open streams and stops the tenant's shards. Only the tenant's own
administrators may seal or unseal it; the platform operator cannot. See
[../barrier-seal/architecture.md](../barrier-seal/architecture.md#sealing-in-a-cluster).

Replica placement is quota-checked and spread across distinct healthy nodes by a
load-aware scheduler. Each node's `TenantShardReconciler` watches the replicated
placement table and converges its assigned replicas — starting on boot,
following placement changes, stopping on demotion. A replica whose tenant
barrier is missing or sealed on the node is deferred, and starts the moment
that barrier is initialized or unsealed there: the barrier registry reports
each tenant barrier's initialization and unseal to the reconciler, which runs
a pass then rather than at its next poll. A tenant's home group is placed
before any node holds its barrier, so this is what brings the group up as
soon as the tenant's barrier exists. Replicas survive node
restarts through the persistent placement table and per-node replica re-attach.

Because leaving the placement stops a replica, a replica leaves its group
before it leaves the placement. A scale-down and a rebalance move both evict
the replica from Raft first, then drop it from the placement. The eviction is
a configuration change committed by a quorum of the configuration it leaves,
which still counts the departing replica's vote. In a group of two voters,
that vote is the one the removal needs: dropping the placement first would
stop and purge the departing replica before the removal committed, and the
surviving leader would lose quorum. A failed eviction leaves the
placement unchanged, so the replica keeps running. The placement write after
the eviction applies to the entry as it then stands, so a promotion
committed meanwhile is kept rather than refused.

## Strict tenant isolation — design

> **Status: ruled, partially implemented.** The design decisions are listed
> at the end under [Rulings](#rulings). It is the requirements contract for the
> work that builds it.

**What is built.**

- **Onboarding (S7)** for every tenant: the CSR-based first admin, the
  pending-admin record and the trust-on-first-init restriction, as
  [Creating a tenant](#creating-a-tenant-onboarding-with-its-first-admin)
  describes, with the request fields `admin_username`, `admin_csr_pem` and
  `admin_cert_pem`.
- **The tenant's security officer from tenant create**: the
  optional `so_username`, `so_csr_pem` / `so_cert_pem`, `so_chain_pem` and
  `so_pin`, planned beside the admin in `pkg/bootstrap/tenant_so.go`. A partial
  set or a CSR with a certificate is refused before anything is written. Under
  strict separation of duties an SO matching the admin by username or by SPKI
  thumbprint is refused too; outside it an SO matching the admin by both is the
  admin, whose account is granted `so` beside `admin`, and one matching by only
  one of the two is refused. A distinct SO is enrolled
  with the `so` role alone and its PIN is set with
  `SOAuthenticator.SetSOPIN`, one PIN per tenant, as the create's last step.
  An isolated tenant records the SO as `__sys:pending-so` beside the pending
  admin; the account is completed right after the admin's. Both postures
  enroll the SO alike otherwise. Under strict separation only an SO grants or
  revokes `so`. No tenant route is SO-gated yet: the SO permissions are
  node-barrier, cross-scope.
- **The isolation policy**, in `pkg/tenant/isolation_policy.go`: the cluster
  floor (`ClusterIsolationPolicy`: unset, `strict` or `operator-access`,
  tighten-only) and the per-tenant opt-in (`TenantIsolationPolicy`: inherit
  or `strict`), applied through `UpdateTenant`, which refuses a loosening
  with `IsolationPolicyChangeError`. The policy governs isolated tenants only.
  The cluster floor is configured with `tenant.isolation_policy`
  (`QRDB_TENANT_ISOLATION_POLICY`); no transport or CLI exposes a policy
  get/set verb, and `tenant.ClusterIsolationPolicyReader` is consulted live by
  `pkg/service/vector.go`'s `validateTenantCreate`.
- **No system-tenant lift, under every policy.** The `ScopeSelf` case of
  `rbac.RBACAuthorizer.Authorize` and `tenant.EffectiveTenant` give a system-tenant identity
  no reach into another tenant's `ScopeSelf` data or IAM records. Platform
  administration of another tenant's registry entry (read, delete, rename)
  goes through `tenants:read:cross`, `tenants:delete:cross` and
  `tenants:update:cross` (`pkg/permission/cross_scope.go`), which
  `AuthMiddleware.AuthorizeTenantTarget` checks.
- **The home store, for every isolated tenant, in both modes.** Every
  `barrier`, `volume` and `raft` tenant has a home store, whatever its
  isolation policy: the policy decides what an operator may do, never where a
  record lives. `defaultStoreResolver.Resolve` (`pkg/tenant/store_resolver.go`)
  sends every isolated tenant to its home store through one seam,
  `tenant.HomeStoreSource`, so every record served through the resolver
  (users and their indexes, roles and role bindings, user and custodian auth
  factors, invitations, permission scopes, the DEK keyring and the tenant CA)
  is written to the tenant's own store and to nothing in the system shard. A
  shared (`prefix`) tenant and the system tenant stay in the system shard.
  - **Clustered (Raft).** A tenant home group (`database.TenantHomeName`,
    `__home`): a placement-only Raft group with a ledger-issued shard, placed
    on every node of the cluster (RF = cluster size) with its log on a WAL
    under the tenant's key, and created by `bootstrap.ProvisionTenant` before
    the tenant's first identity record is written (`lifecycle.TenantHomes`).
    Every node holds a replica, so every node authenticates the tenant from
    its own copy. Every node with a system barrier runs the WAL multiplex,
    whether the barrier encrypts or is still in plaintext mode, so the home
    group's log is always on a WAL under the tenant's key and never under the
    node key. On a node that has not enabled encryption, that tenant key is
    itself plaintext until the tenant's barrier encrypts.
  - **Standalone.** A store under the tenant's own barrier, in the tenant's
    data root (`tenant.LocalHomes`): the same records, the same keys, behind
    the same seam, including the atomic guarded batch the tenant CA is
    written with.

The built home store differs from the design below in two ways:

- **Nothing is migrated.** A tenant whose records predate its home store is
  re-created, not moved.
- **The home group spans the cluster**, not a chosen serving set. A node that
  joins later receives the home placement from the reconciler, and until its
  replica has caught up it refuses the tenant's requests as retryable
  unavailable (`lifecycle.TenantHomeCatchingUpError`) rather than serve a
  stale replica or proxy, so callers go to the other nodes.

What remains is listed under
[Remaining work](engineering.md#remaining-work) in engineering.md. Engineering
detail and the phased plan:
[engineering.md](engineering.md#strict-isolation--engineering-contract-ruled).
API, CLI and user guide: [api.md](api.md#strict-isolation--ruled),
[cli.md](cli.md#strict-isolation--ruled),
[user.md](user.md#strict-isolation--ruled).

### Problem

A tenant on a dedicated isolation tier (`barrier`, `volume` or `raft`) is
promised isolation. Every record about it that lives in the system tenant
weakens that promise:

- **Identity.** In the shared, Raft-replicated system store, a tenant's users,
  roles, role bindings, auth factors, credential lookup keys, permission
  scopes, invitations and DEK keyring sit under a per-tenant prefix. The
  values are encrypted under the tenant key, but the **key names are
  plaintext**, and they carry usernames, email addresses, certificate
  thumbprints, role names and credential lookup hashes. The home store holds
  these records instead.
- **Seal metadata.** The tenant's SO PIN commitment (`__sys:so-pin-commit/`)
  is in the system store, outside every tenant barrier.
- **Routing.** A global identity index in the system tenant maps every user id
  and role id to its tenant.

Two more facts make it worse:

- **Shared root key.** For a seal strategy a node cannot key itself, every
  replica of a `raft` tenant holds the same tenant root key, delivered by
  `pkg/clusterkeys`. A node whose system key and disk are both compromised
  yields a key that decrypts the tenant's data on every other replica too.
- **Operator reach.** A system-tenant identity that reached every tenant could
  read and rewrite another tenant's users, roles and factors, mint a
  CA-signed client certificate for another tenant's user and log in as that
  user, and reach another tenant's database through its etcd listener.

**Every tenant record, and every system record about the tenant, lives in the
tenant's own isolated store, to the greatest extent possible.** The system
tenant keeps only what it cannot do without.

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

1. **Each isolated tenant gets a home store**, whatever its isolation policy.
   It holds every record of the tenant that the platform does not need in
   order to place, route, size or retire the tenant.
   - In a cluster, for a `raft`, `barrier` or `volume` tenant alike, the home
     store is a **tenant home group**: a Raft group per tenant, replicated
     across the tenant's serving set.
   - On a standalone node, it is a store under the tenant's own barrier in the
     tenant's data root.
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
| **tenant home group / home store** | The store that holds an isolated tenant's own records: in a cluster, the per-tenant replicated group (`raft`, `barrier` and `volume` tenants); on a standalone node, a store under the tenant's barrier. |
| **tenant registry entry** | The tenant's record in the system tenant: what the platform needs, and nothing more. |
| **strict tenant** | A tenant whose effective isolation policy is `strict` (see [The isolation policy](#the-isolation-policy)). |

### What moves into the tenant

Every row is a per-tenant record that a shared store would hold, or that is
reachable through the system store. The full inventory, with code locations,
is in
[engineering.md](engineering.md#inventory--where-tenant-records-live-today).

| Record | Built | For a strict tenant |
|---|---|---|
| Users, and the username, email and SPKI indexes | home store | home store |
| Roles, the role-name index, role bindings | home store | home store |
| User auth factors, credential lookup index | home store | home store |
| Custodian (ceremony) auth factors and challenges | home store | home store |
| Invitations and redemptions | home store | home store |
| Permission-scope overrides | home store | home store |
| DEK keyring | home store | home store |
| Named credentials (`credentials:*`) | the tenant's own barrier; a `prefix` tenant has none and is refused ([../credentials/architecture.md](../credentials/architecture.md#tenant-stores)) | the tenant's own barrier |
| Identity index (user and role id → tenant) | `system/__sys:identity_idx/…` | **removed**; see [Identity routing without a global index](#identity-routing-without-a-global-index) |
| Tenant CA private key and CA certificate | home store; the public CA certificate is also published at `__sys:cert_ca_pub/{t}/` | home store, with the public certificate published |
| Issued-certificate records | home store | home store |
| Revocation of tenant-issued certificates | `__sys:certs_revoked/{serial}` (global) | home store, enforced by the serving replicas when they authenticate the tenant's users |
| SO PIN commitment | `__sys:so-pin-commit/{t}` (unconfined root store) | each node's tenant bootstrap record (below), because it gates operations on a sealed tenant |
| Tenant officer roster | tenant barrier store; **falls back to the replicated system store before init** | Home store, plus the unseal roster in each node's tenant bootstrap record. Before init, the roster is held in memory by the init ceremony. |
| Database catalog | home store, one record per database under `__sys:dbcatalog/{t}/` | home store |
| Collection work journal, drop fence, reclaim index, locks, leases, sequences, row locks, index catalog, time series | system shard under `{t}/…` and `__sys:…/{t}/…` | the database's own store, per the databases binding design. Tenant-wide journals go to the home store. |
| Tenant-scope encryption-migration records | `__sys:encmig/{t}/{db}` | home store |
| Audit events about the tenant's identities and data | one node-local file shared by every tenant | a per-tenant, hash-chained trail replicated in the tenant's home group, under the tenant's key (S9). Platform events about the tenant go to the platform trail. |
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
| **Tenant CA certificate (public)** | `__sys:cert_ca_pub/{t}/certificate` (DER; the key is per tenant, under a `{t}/` segment, so tenant reclamation removes it by prefix) | A serving node verifies a client's chain before it reads anything else about the client. A public certificate discloses nothing the client does not already present. |
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
| Platform audit trail | hash-chained, replicated in the system shard (today the node-local file the audit feature still writes) | It records platform actions: tenant create and delete, plan changes, seal and unseal requests, policy changes, operator access and passthrough relays. A tenant's own events are in the tenant's trail. |

**The policy governs isolated tenants only.** A tenant is **shared** (`prefix`: prefixed keys in the shared
store under the shared barrier; in raft mode, the shared group) or
**isolated** (`barrier`, `volume`, or `raft` in raft mode only). A shared
tenant has no key and no store of its own, so its records live in the system
shard, and the strict policy has nothing to say about it. The consequences:

- The floor and a tenant's own `strict` opt-in never refuse a shared tenant:
  creating one, or asking for `strict` on one, succeeds under every floor.
- Strict-only behaviour (the strict default for isolated tenants, the home
  moves) applies to isolated tenants, behind the cluster feature gate
  described in [Feature gate and upgrade](#feature-gate-and-upgrade).
- The system tenant is the platform itself. It stays `prefix`, and the floor
  does not apply to it.

Which kind a create gets when the request names none is the default tenant
type, an admin setting.

### Per-replica tenant keys

> **Status: partially built.** The per-node init design is
> [../barrier-seal/architecture.md](../barrier-seal/architecture.md#per-node-init-design):
>
> - Init is node-local where no client credential is needed, and fanned out by
>   the client where one is.
> - A passphrase joiner gets the tenant-wide verifier over HPKE from a serving
>   peer.
> - A KMS `KeyRef` names one tenant-controlled key by default; a per-node
>   template is allowed. `KeyRef` is opaque to qrdb: qrdb ships no hardware or
>   cloud key code, only the seam an injected go-xkms strategy reads it
>   through.
> - **One mechanism for every barrier.** Every barrier's strategy is resolved
>   per barrier and per node through one `seal.BarrierKeyProvider` seam
>   ([barrier-seal](../barrier-seal/architecture.md#the-barrier-key-seam--barrierkeyprovider)).
>   Tenant barrier init uses it; the node and database barriers follow.
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
> - Opaque database-id placement is not a prerequisite.
>
> **Built.** The signed, keyless init statement
> (`pkg/barrierinit.InitStatement`), its verification, and its guarded claim
> and compare-and-set under `__sys:cluster/barrier/init/<barrier>`; the
> watcher that delivers each verified epoch to a serving node; and the
> `per-node-barrier-keys` feature gate, which every binary advertises and which
> gates both an isolated tenant's admission and the claim (see
> [../barrier-seal/architecture.md](../barrier-seal/architecture.md#per-node-barrier-keys)).
> Every node, standalone or clustered, composes the watcher, which initialises
> the node's own key for a tenant it serves from a claimed statement.
> Onboarding pins the tenant's first admin as the statement's anchor. Every
> transport's tenant barrier init carries the statement the owner's client
> signs and hands it to the tenant service, which claims it through the init
> book.
>
> An isolated tenant whose key auto-unseals (platform custody) claims a
> statement the platform signs with a node's cluster identity, which verifies
> against the cluster CA and only for a tenant that chose platform custody.
> Every node of the tenant's groups initialises its own key from it, so no key
> is carried between nodes and two nodes' key fingerprints differ. The
> embedded node (`sdk/go/node`) carries the same node identity and
> platform-init signer as `qrdbd`, composed through
> `lifecycle.ComposeRaftTenantCluster` with `lifecycle.NodeIdentity`; a
> single-member embedded cluster given no identity issues its own through
> `clusterenroll.Resolve`. The statement stays required: a node without an
> identity refuses the create with `tenant.InitStatementRequiredError`. A node
> that placement assigns to the tenant's group registers the tenant's sealed,
> keyless shell first, so status, init and unseal reach it before any key
> exists.
>
> **Not built:**
>
> - Placement does not check the tenant's signed serving set before a node
>   registers a shell.
> - A node composed without a cluster identity (a single-node cluster run
>   without transport TLS) refuses a platform-custody `raft` tenant, having no
>   signer, and claims no statement for a `barrier` or `volume` tenant, so no
>   other node of its home group initialises a key for it.
>
> See [../barrier-seal/architecture.md](../barrier-seal/architecture.md#how-a-node-takes-part-tenant-barriers).

**A replica key is created on the node that uses it, the first time that node
holds a valid credential for the tenant, and it never leaves that node.**

| Barrier mode | How a node's replica key is wrapped | What a node needs to unseal it |
|---|---|---|
| **System-managed** | under that node's own node barrier, as today | nothing beyond its own unsealed node barrier; it auto-unseals |
| **Self-managed, passphrase** | under a KEK from Argon2id over the passphrase and **this node's** salt | the passphrase, presented to this node |
| **Self-managed, Shamir** | under an **unseal key** reconstructed from M-of-N shares (today's unseal-key indirection). The unseal key is common to the tenant; every replica key under it is independent. | a quorum of shares, presented to this node |
| **Self-managed, hardware** (injected from go-xkms) | under a key in that node's own hardware | that node's hardware, which by nature binds each node to its own device |

What this buys:

- **One node's compromise stays on that node.** Compromising one node's disk
  and memory yields that node's replica key and nothing else; the other
  replicas' keys were never on it. With a shared key, the same compromise
  yields the key every replica holds.
- **The tenant's credential is the only common secret.** It travels from the
  credential holder's client to each node, and never from node to node.
- **`pkg/clusterkeys` stops carrying tenant keys.**
  The sealed-blob import and the tenant-key delivery are deleted, not kept
  for compatibility. `clusterkeys` keeps its node-identity
  checks, and delivers a **credential verifier** to a new replica (below),
  which is not a key.
- **A system-managed tenant is strictly weaker, as today.** Its replica keys
  are wrapped under node keys, so whoever controls a node's root controls that
  node's copy. It is protected from the operator's API reach, not from the
  node's own root. The `AllowSystemManagedBarrier` entitlement keeps gating it.

**Raft snapshots and data movement.** Raft already moves state between nodes
whose node barriers have different keys; every system-shard snapshot does it. The sender reads through its own barrier, the bytes cross node mTLS, and
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
  start, and never falls back to the shared WAL; the refusal is
  `database.TenantWALUnavailableError`.
- **Every run mode (G3).** The multiplex is wired on single-node hosts and on
  the SDK node, and under a system barrier whether it encrypts or is still in
  plaintext mode (protection `none`), so the guarantee does not depend on how
  a node is deployed or whether it has enabled encryption yet.
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

The design removes `tenant.IdentityIndex`, `tenant.OwningTenant` and the
system-namespace index records. An isolated tenant already keeps no index
entry; its ids are resolved in its own home store. The shared tenants'
entries go with the index itself (status in
[engineering.md](engineering.md#remaining-work)).

### Connecting through a node outside the serving set — layer-4 passthrough

**Not built.** This is the design the passthrough is built against.

A node outside a tenant's serving set holds none of the tenant's records and
none of its keys, by design. It cannot authenticate the tenant's users and must
not try. It relays (S6) the client's **still-encrypted**
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
  `<tenant>.<suffix>`, under one cluster-wide suffix, so:
  - a strict tenant's id must be a valid DNS label;
  - every node's server certificate carries `*.<suffix>`.

  The SDK and CLI set the SNI from the tenant they are configured for.
- **The serving node binds the SNI to the certificate.** A client whose
  certificate names a different tenant from its SNI is refused. Otherwise a client could steer a relay to
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
  relay. On a node outside the serving set they refuse the request, naming
  the serving set.
- **A client that sends no tenant SNI** is terminated locally, as the system
  tenant's own clients are. If its certificate then names a strict tenant
  this node does not serve, the node refuses it the same way.
  It has already terminated the connection, so it cannot relay it, and it does
  not look the user up.
- **What the relay discloses.** SNI is plaintext on the wire, so a network
  observer sees which tenant a connection is for. Encrypted ClientHello would
  hide it and is a later option; it is not part of this design.

### Bootstrapping and unsealing

A self-managed tenant's identity lives inside the store that is sealed. The
credential that opens the store therefore cannot be authorized by anything
inside it. The design breaks the cycle in three places.

> **Per-node init:**
> [../barrier-seal/architecture.md](../barrier-seal/architecture.md#per-node-init-design).
> The joiner's verifier over HPKE from a serving peer is decided, and a
> platform-sealed key stays the tenant's choice under either policy. The
> design differs from the text below in five places:
>
> - **The init parameters are signed.** The client signs the init statement
>   with the same `crypto.Signer` as the init proof, so every node verifies
>   the parameters, not only the connection. The statement, its verification,
>   its guarded claim and its transport on every init are built; the
>   fan-out of a passphrase or Shamir init to every serving node is not.
> - **Shamir has no pending key.** A Shamir node holds no pending key: it
>   generates its key when a share quorum, checked against the statement's
>   unseal-key check value, first reaches it.
> - **The first admin waits for quorum.** It is written by the home group's
>   leader, with guarded writes, once a quorum exists, and not inside the
>   first node's init response. Built: see the first-admin bullet under
>   step 2 below.
> - **A passphrase node computes one tenant-wide verifier** from the
>   statement's salt, so the serving nodes need no verifier delivery between
>   them. Only a later joiner needs one.
> - **A node that missed a credential change** refuses the old credential, and
>   catches up with the old and new credentials together.

**1. Creation: the tenant's first admin is fixed by the requester's own CSR
(S7). Built for every tenant, not only a strict one** (`admin_username` plus
`admin_csr_pem` or `admin_cert_pem`); see [Creating a tenant](#creating-a-tenant-onboarding-with-its-first-admin).
What this design adds on top:

- The platform writes the registry entry, places the home group, assigns pool
  slots, and records the pending first admin (username and CSR SPKI
  thumbprint), the same record `pkg/tenantadmin` keeps.
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
  - **Built on REST, gRPC, the Unix socket, QUIC and MCP.**
    The nonce is single-use, lives five minutes, and verifies only against
    the exporter of the TLS 1.3 connection that asked for it; the SDK runs
    both calls on one connection and signs with a caller-supplied
    `crypto.Signer`, so a device-held key never leaves its device. The
    connection is still authenticated by the first admin's certificate issued
    at create, and the proof is required on top of it; the embedded
    transport, which is in process and has no connection, asks no proof.
    Dropping the certificate, and the fan-out below, are not built.
- **The client sends init to every node in the serving set.** No node relays
  it to another; the SDK and CLI do this fan-out.
  - **Passphrase:** each node generates its own random replica key and seals
    it under a KEK derived from the passphrase and its own salt.
  - **Shamir:** the first node generates the unseal key and splits it to the
    enrolled officers. The response is share-free. That node seals
    its replica key under the unseal key. Each other node generates its own
    replica key and holds it **pending** until the first unseal ceremony
    presents a quorum to it; it then seals the key under the reconstructed
    unseal key and unseals.
- **The home group's leader writes the first admin into the home store**, once
  per tenant, after the barrier is usable: the tenant's built-in roles, then
  the first admin user with the Admin role and its SPKI binding, then the
  deletion of the pending first-admin record. The admin certificate was
  already issued from the CSR at create. Built:
  - **Off the init path.** Barrier init, unseal and a status read on an
    unsealed barrier each start the completion in the background and return.
    A node runs at most one per tenant at a time, under its own bounded
    retry, and stops it with the node.
  - **On the leader only.** A node that does not lead the home group writes
    nothing. It keeps checking within the retry budget until the record is
    gone or the node has become the leader. A new leader therefore finishes
    a record its predecessor left pending, adopting the account the
    predecessor created.
  - **Guarded writes, no new opcode.** The roles and the user are
    create-if-absent and the binding is compare-and-swap. The record is
    deleted with compare-and-delete, and only while it is still the record
    that was read. A completion that finds the record gone, or loses the
    delete, has lost a race to another completion, and reports success.

  The other nodes receive these records through Raft.
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

**3. Unseal: authorized by the credential, never by tenant RBAC.**

- **The sealed window is narrow.** A caller whose tenant's IAM store is sealed
  on the node is authenticated by its certificate and reaches only the
  identity-gated barrier endpoints (status, init, unseal) and health; every
  other permission is answered unavailable
  (`pkg/auth/middleware/middleware.go`, `IdentityIAMSealed`). A certificate
  signed by another tenant's CA is refused with
  `auth.IssuerTenantMismatchError`. The credential itself is the real gate.
- **The roster comes from the node itself.** The home store is still sealed,
  so the node reads the holder roster from its own barrier records, or, before
  its first quorum, from the armed init statement
  (`StorageBarrier.quorumRoster`).
- **Credentials travel only from the holder.** Shares and passphrases go from
  the holder's client to each serving node, and are never relayed node to
  node. A share is held in protected memory on the node that received it, and
  counted once per holder, per the unified share-holder model
  ([../shamir-custody/](../shamir-custody/)).

**The home group starts on a node only when that node's tenant barrier is
unsealed.** Until a quorum of the serving set is unsealed, the home group has no
quorum. Every request for the tenant is then refused with
`tenant.TenantHomeUnavailableError` (503, retryable), except the identity-gated
barrier endpoints.

**Every serving node holds its own key, and no key is shipped.** A platform-custody tenant's nodes each initialise their own key from
the platform's claimed init statement. A `none` barrier's nodes each initialise
from the owner's claimed statement, sent to one node. A `passphrase` barrier's
nodes each initialise from the owner's init: `qrdb tenant barrier-init` reads
the serving nodes from the barrier status, which lists every node holding a
replica of the tenant's groups with the API endpoints its member record
publishes, and sends the init to all of them at once, printing one result per
node. A node that refuses or cannot be reached is reported without stopping the
others; each node's init proves itself on its own connection, and the claim
accepts the one statement from every node. Neither key is ever shipped, and neither is a Shamir
barrier's: every serving node is armed from the claimed init statement and
keys itself at its holders' first quorum, and a replica refuses a Shamir sealed
blob, which lacks the node's own wrapped-key and roster records.

The fan-out is concurrent because a node's init completes only once its replica of the home group
has a leader, which needs a quorum of the serving nodes keyed. Sent one node at
a time, the first node's answer would wait on the nodes still queued behind
it, and a home group needing more than one other node for quorum could not
form within its startup budget.

### The isolation policy

**A cluster floor and a per-tenant setting (S1)** (`pkg/tenant/isolation_policy.go`).

| Setting | Where | Values | Who sets it |
|---|---|---|---|
| Cluster floor | `__sys:cluster/isolation_policy` | unset (read as `operator-access`), `operator-access`, `strict` | The config key `tenant.isolation_policy`, which each node applies at start through `TenantService.SetClusterIsolationPolicy`. A value the stored floor already holds is a no-op. A node in FIPS 140 mode tightens it to `strict` at start. |
| Tenant policy | the tenant's registry entry (`isolation_policy`) | `inherit` (empty, the default) or `strict` | The Go API: `tenant.WithIsolationPolicy` at create, or a `TenantPatch.IsolationPolicy` on update. No transport or CLI verb sets it. |

`tenant.EffectiveIsolationPolicy` is `strict` when either setting is `strict`.

**Both only tighten.** A tenant that was promised strict isolation must not
lose it for data it already stores. An unset floor may become either value,
`operator-access` may become `strict`, and `strict` is terminal; a tenant's
`strict` cannot be withdrawn. Loosening is refused with
`tenant.IsolationPolicyChangeError`. Every isolated tenant has a home store
whatever its policy, so tightening moves no record and is accepted at any
time.

**What the policy decides today.** Under a `strict` floor the vector service
refuses a namespace create from a caller acting as the system tenant
(`service.WithClusterIsolationPolicyReader`). Nothing else reads the policy:
authorization does not consult the effective policy.

**Not built: the rest of the design.**

- **`ScopeSelf` loses its system lift.** The system tenant would be confined
  to its own tenant like every other, and `permission.ScopeSelfStrict` would
  fold into `ScopeSelf`.
- **`tenant.EffectiveTenant` stops honouring a requested tenant from a
  system-tenant caller**, refusing with `tenant.CrossTenantDeniedError` unless
  the operation is cross-scope. `tenant.OwnTenant` (`sdk.OwnTenant`) already
  gives a caller this rule: it is `EffectiveTenant` with no system-tenant
  exception, a second function rather than a flag.
- **Role bindings name the holder's own tenant only.** The system-tenant
  exception in `user.NormalizeRoleBindings` would be removed.
- **The platform plane** stays the `ScopeCross` permissions, which act on
  system-tenant records only (registry, placement, pools, membership, node
  barriers, the platform audit trail) and are unaffected by the policy.
- **Tenant data is never reachable from outside the tenant**, under either
  policy, and a strict tenant's CA key and records stay in its home store.
- **A platform seal of a tenant** (cluster-wide sealing) is a platform-plane
  verb under every policy. A seal discloses nothing, and unseal stays with the
  tenant's own credential holders.

### Backups of a strict tenant (S10)

- **The rule.** The platform takes a tenant's backup only if the archive it
  produces can be opened by the tenant's credential alone, **and** the
  platform never reads the tenant's data while making it. Otherwise, backups
  of a strict tenant's groups are tenant-only (`permission.ScopeTenantOnly`).
- **Why the platform form is hard.** Each serving node would have to encrypt
  its snapshot stream under a key derived from the tenant's credential, not
  from its replica key: a replica key never leaves its node, so it cannot open
  an archive anywhere else. That derivation needs the credential present at
  backup time.
- **Not built.** The `backup:*` permissions are cluster-administration
  permissions (`pkg/permission/cluster_admin_scope.go`); there is no
  tenant-only backup permission.

### Interaction with cluster-wide sealing

The cluster-wide sealing design
([../barrier-seal/architecture.md](../barrier-seal/architecture.md)) adds a
replicated, epoch-numbered seal record per barrier, enforced on every node by
its seal monitor. This design depends on that record, and constrains it:

- **The tenant scope's serving set is this design's serving set.** It is the
  set of home group replicas, and every database group of the tenant is placed
  inside it.
- **Its unseal model is per node.** No key moves between nodes, and there is
  no common root key to deliver. Each node verifies the credential against its
  own verifier and unseals its own replica key. The CLI sends
  `tenant barrier-init` to every serving node; unseal and rekey are sent to one
  node at a time.
- **Rekey is per node.** There is no shared root key to rotate.
  - A credential change (a new passphrase or a new share set) re-wraps each
    node's replica key.
  - A replica-key rotation is node-local, and re-encrypts that node's copy
    only.
  - Old shares stop working on a node as soon as that node re-wraps. The
    cluster seal record's epoch is the fence that stops a node still on the
    old credential from serving.
- **A joining node comes up sealed.** A node added to a tenant's serving set
  gets the verifier, not a key, and unseals at the next fan-out. For a
  system-managed tenant, it generates its key on arrival.

### Feature gates and upgrade

**`per-node-barrier-keys` (`featuregate.PerNodeBarrierKeys`)** is an
activation-based cluster feature (`pkg/cluster/featuregate`), built like `cluster-seal`. It adds no Raft command:
the init statement is written through the existing guarded write.

- **Why it exists.** An isolated tenant's replicas each initialise their own
  key from the replicated init statement. A node whose binary does not know
  the statement would wait for a key delivery that never comes, so an
  isolated tenant is not admitted until every member advertises the feature.
- **Not yet active:** creating a `barrier`, `volume` or `raft` tenant, or
  changing an existing tenant to one of those types, is refused with
  `*feature.NotActiveError{Feature: per-node-barrier-keys}` (503,
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

**`tenant-undo` (`featuregate.TenantUndo`)** is a third activation-based
feature. It adds no Raft command: the undone tombstone and the
acknowledgements are ordinary metadata writes.

- **Why it exists.** Undoing a create that did not complete (an exclusive
  provisioning that failed part-way) is not a deletion: the id never served
  anyone, so it may be created again. A node whose binary predates the
  feature never acknowledges its cleanup, and a reuse waiting on it would
  wait forever, so the undone state is written only once every member
  advertises the feature.
- **Not yet active:** an undo retires the id exactly as a deletion does, and
  a later create of it is refused with `*TenantDeletedError` (409).
- **Active:** an undo writes a tombstone in the `undone` state carrying a
  random nonce. Each serving node, once it holds nothing of the id, records
  `__sys:tenants-undone-ack/<id>/<nonce>/<node>`, with `<node>` its node
  identity. A create of the id waits until every node on the roster, by the
  same identities, has acknowledged. It then deletes the tombstone only if it
  still holds the bytes it read, deletes that undo's acknowledgements, and
  commits. The nonce scopes the acknowledgements to one undo, so an earlier
  undo's never counts toward a later one.
- **An undo never downgrades a retirement.** The undone tombstone is a
  conditional write: it is created only where no tombstone exists, or
  swapped only for an earlier undone one still holding the bytes read. An
  undo racing a deletion of the same id that lands first loses with
  `*TenantDeletedError` (409), and the id stays retired.
- **The roster** is every member of the system shard, read from its Raft
  membership at the time of the create: voting nodes, observers and
  witnesses alike, since each serves the API and may hold the id's material.
  A standalone node's roster is itself.
- **A node acknowledges only a finished teardown.** The undo's teardown —
  barrier destruction, ceremony cleanup and reclamation of the id's records
  from the shared keyspace — ends by marking the tombstone `torn_down`, and a
  node acknowledges only a marked tombstone. A teardown that fails partway
  leaves the mark unset, so no node acknowledges and the id stays pending;
  re-issuing `tenant delete <id>` resumes the teardown, which marks the
  tombstone when it completes.
- **Each node acknowledges as the mark arrives.** The node that ran the undo
  acknowledges at the undo itself. Every node also holds a watch on the
  tombstone prefix, started by `lifecycle.StartNodeWatchers`, the one
  composition qrdbd and `sdk/go/node` share. The watch is the node's own
  work: stopping the node ends it and waits for it, before any store it reads
  through is closed. A put of an `undone` tombstone
  makes the node clean up that id and acknowledge. The mark is written after
  the tenant's record is deleted, so a node that saw the first tombstone
  write while the record still existed sees the mark after it, and a reuse
  waits on replication rather than on a timer. When the watch closes, the node
  re-establishes it with backoff and then runs a full
  `ReconcileDeletedTenants`, covering any tombstone written in the gap. A
  node that was down acknowledges in the reconcile it runs at boot. A cleanup
  that fails is left to the next reconcile, and the
  `qrdb tenant reconcile-deleted` verb runs one on demand.
- **Until every node has acknowledged**, the create is refused with
  `*TenantUndoPendingError`, naming the nodes still pending and carrying a
  retry delay of `tenant.UndoPendingRetryAfter` (5 s). Every transport maps
  it to its own Unavailable status with that delay, so an SDK caller reads
  both off `*transport.RequestError` (`StatusCode`, `RetryAfter`):

  | Transport | Status | Delay |
  |---|---|---|
  | REST | 503 | `Retry-After` header |
  | gRPC, unix | `Unavailable` (14) | `RetryInfo` detail |
  | QUIC | `StatusUnavailable` (5) | delay in the error frame |
  | MCP | `CodeUnavailable` (-32006) | `retry_after_seconds` |
  | Embedded | the error itself | `RetryAfter` |
- **A deletion is unchanged.** A deleted id is retired for good whether or
  not the feature is active.

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
  Rejected: it is key transport, which per-node keys forbid.
- **Redirect the client to a serving node.** Rejected in favour of
  passthrough (S6). A redirect makes every client implement reconnection
  and exposes the serving set to any holder of a tenant certificate.
- **On-behalf forwarding from a non-serving node.** Rejected. The serving node
  would trust another node's claim of proof of possession, so a compromised
  non-serving node could act as any user whose public certificate it had
  seen.
- **An operator-held one-time bootstrap credential.** Rejected (S7). The
  platform would hold, even briefly, a credential that creates the
  tenant's first admin.
- **One global "strict" bit per permission.** Rejected: two tenancy mechanisms
  would drift apart. The policy is one input to the one scope rule.
- **Per-database groups only, with identity on "the first database".**
  Rejected. Identity is tenant-wide, and a tenant with no database still has
  users.

### Rulings

| # | Question | Ruling |
|---|---|---|
| **S1** | Cluster-wide only, or per tenant? | **B.** The cluster sets a floor. A tenant may opt into `strict` under an `operator-access` floor. |
| **S2** | May `strict` include `prefix` tenants? | The policy applies to isolated tenants only; a shared (`prefix`) tenant is never refused by the floor. |
| **S3** | What becomes of `barrier` mode? | **A.** Barrier tenants get a home group. |
| **S4** | Must a strict tenant's Raft log be under the tenant's key? | **Yes.** A strict tenant never has a node-key log. It uses the dedicated-WAL mechanism: mandatory, failing closed, in every run mode, sealed in order, evicted and restarted with its group, under per-node keys; key rotation is a later phase. |
| **S5** | May database names appear in system records? | **B.** No. System records use opaque database ids. |
| **S6** | How does a non-serving node reach the serving set? | **B.** Layer-4 passthrough of the still-encrypted connection. Never a redirect; never an on-behalf forward. |
| **S7** | Where does a tenant's first admin come from? | **B.** From a CSR supplied at tenant create. No operator-held bootstrap credential. |
| **S8** | May the platform delete a strict tenant? | **A.** Yes, audited. |
| **S9** | Where does the tenant audit trail live? | **B.** In the tenant's home group, replicated, hash-chained, under the tenant's key: the replicated audit chain ([../audit/](../audit/), `audit.Chain`, `OpAuditAppend`). The platform trail is replicated the same way, in the system shard. No trail is node-local, because a node-local trail is lost with its node and no cluster backup restores it. |
| **S10** | Who backs up a tenant's groups? | **B, else A.** A platform-made archive that only the tenant can open, if it can be built without platform read access; otherwise tenant-only backups. |

Standing rules that also apply:
- **No backward-compatibility code.** Pre-release tenants are re-created, not
  migrated.
- **Changes ship behind the cluster feature gate** (`pkg/cluster/featuregate`).

Open question: SNI as the passthrough routing input.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Tenant records, isolation modes, provisioning | `pkg/tenant` | go-qrdb |
| Quotas, entitlements, usage counters, reconciler | `pkg/quota` | go-qrdb |
| Per-tenant barriers, `BarrierRegistry`, `TenantBarrier` | `pkg/seal` — see [../barrier-seal/](../barrier-seal/) | go-qrdb |
| Node-to-node HPKE envelope over mTLS (no tenant key is carried; verifier delivery to be built) | `pkg/keydelivery`, `pkg/clusterkeys` | go-qrdb |
| The multi-node composition of `raft` isolation: the key plane, the rebalance dependencies, the replica host, the peer admin registry | `pkg/server/lifecycle` (`ComposeRaftTenantCluster`, `NewPeerAdminRegistry`), composed identically by `qrdbd` and a clustered `sdk/go/node` node | go-qrdb |
| Replica placement and reconciliation | `pkg/placement` | go-qrdb |
| Volume allocation | `pkg/volume` — see [../volume-pools/](../volume-pools/) | go-qrdb (the seam and the `dir` provider); **go-xkms** registers its own providers |
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
  back off the index name plus the prefix its creator declared, so a caller
  holding `index:query` on its own tenant never sees another tenant's key
  names. See
  [../secondary-indexes/architecture.md](../secondary-indexes/architecture.md#index-key-scope--a-name-is-not-a-predicate).
- **Per-node keys**: each serving replica of a `none`, `passphrase` or
  `shamir` tenant barrier initializes its own key, so no two replicas share
  one; a barrier of any other self-managed strategy is shipped to followers as
  the owner-sealed blob (see *Per-node-independent key design*).
- **Provisioning-governance permissions are distinct**:
  `tenants:set_isolation`, `tenants:set_entitlements`,
  `databases:create_raft`, `databases:create_volume`.
- **A tenant's plan belongs to the platform operator.** `tenants:create`,
  `tenants:set_quotas`, `tenants:set_entitlements`, `tenants:set_isolation`
  and `tenants:set_status` are cross-scope
  (`pkg/permission/tenant_provisioning_scope.go`): only a `system`-tenant
  identity can exercise them, so a tenant's Admin cannot mint tenants or raise
  its own limits. `tenants:update` stays self-scoped and covers only the
  display name, description and metadata.
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
  any transport, not only from qrdb's built-in tenant routes.
- **`quota.Entitlements.AllowedBarrierModes` restricts which seal strategies
  a tenant may pick.** Unset, it allows the software strategies (`none`,
  `passphrase`, `shamir`); a hardware strategy must be granted.

## What this feature deliberately does not do

- **No self-service registration store.** The platform-wide registration
  policy (`iam.self_service_registration`) is held by the composing host
  (go-quicbaas), one canonical record for the whole stack. qrdb contributes
  only the gating permission vocabulary: `registration_config:read`,
  `registration_config:update` and `tenants:approve` are cross-scope
  permissions (`pkg/permission/cross_scope.go`) granted to Admin@system, gating the host's registration-config and pending-tenant
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
