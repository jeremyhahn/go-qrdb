# Multi-Tenancy — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-29

## Exported surface

`sdk/go`:

```go
func (c *Client) Tenants() transport.TenantClient
func (c *Client) WithTenant(tenantID string) (transport.KVClient, error)
```

`TenantBarrierService` is a **dedicated gRPC service**
(`qrdb.v1.TenantBarrierService`) with `Init`, `Unseal`, `UnsealShare`, `Seal`,
`Status`, `OfficerRegister`, `OfficerList`, `ShareClaimBegin`,
`ShareClaimComplete`, `OfficerClaimStatus`, `ShamirSealVote`.

Server option: `WithTenantBarrierService(svc tenant.TenantServicer)`.

## Isolation modes

`TenantInfo.IsolationType`, set at creation, defaulting to `prefix`:

| Mode | Raft | Storage | At-rest crypto |
|---|---|---|---|
| `prefix` | shared system shard | shared Pebble | shared system barrier |
| `barrier` | shared system shard | dedicated Pebble for IAM data, from the volume pool | per-tenant DEK over IAM data |
| `volume` | per-tenant local single-node, transport-less Raft host | dedicated filesystem `DataRoot` from the pool | per-tenant DEK over **everything, including the WAL** |
| `raft` | dedicated Raft group per database | SM data under the tenant's `DataRoot`; raft log in the shared host LogDB | per-tenant DEK over the SM data |

## Invariants

- **A resolver-served store is confined to its tenant's keyspace by
  construction.** `StoreResolver.Resolve` returns the system store wrapped in
  `kvstore.NewNamespacedKVStore` at `{tenant}/` for every shared-prefix tenant
  (including the system tenant and unknown tenants). Returning the bare shared store gave every prefix tenant one
  keyspace — the #41 defect. A caller must never have to remember to prefix.
- **The user-side auth-factor service is routed like the user and role
  services.** `service.NewRoutingUserAuthFactorService` over the same
  `StoreResolver`; a composition with no resolver gets NO factor surface rather
  than an unrouted one. The lookup key carries no tenant and is not supposed
  to: the store is the tenant dimension, and nothing downstream re-checks it.
  Enrolment (`Add`) routes on the tenant owning the named user id and REFUSES a
  caller that may not reach it; `LookupByCredentialKey` routes on the context
  tenant, which is the tenant the login was begun under.
- **The ceremony-time custodian auth-factor service is routed the same way:**
  `service.NewRoutingAuthFactorService` over the same
  `StoreResolver`, wired by `pkg/server`'s `ensureAuthFactorService` and
  `cmd/qrdbd`'s `buildAuthFactorServiceOption`, with the same no-resolver-no-
  surface refusal. It has no owner index, unlike `Add` above: a custodian
  enrollment id is issued inside the enrolling tenant and only ever presented
  back to it, so every verb routes on the context tenant. Pre-cutover records
  at the flat `__sys:authfactor/` key are not migrated — go-qrdb is
  pre-release alpha — and are unreachable through the routed service after
  this change; see [../auth-factors/engineering.md](../auth-factors/engineering.md#the-custodian-surface-is-routed-too).
- **The resolver-served store namespaces at `{tenant}/`, not
  `{tenant}/{db}/`.** It serves identity, and identity is tenant-wide — a user
  authenticates whichever database a request names. Per-database namespacing
  here breaks login, because firstboot writes the admin user with no database
  bound. Data-plane database scoping is the transports' and SQL provider's job
  and is unaffected.
- **Every data-plane servicer a transport uses comes from
  `service.Registry`, resolved per `(tenant, database)`.** The fourteen
  accessors are the complete list of namespaced data-plane surfaces; a surface
  a transport holds as a process-wide singleton instead has NO tenancy,
  whatever the isolation mode says. `CollectionService` and then `LockService`
  were the last two to exist, and while each was missing that surface was
  shared across tenants under `prefix` isolation — collections addressed the
  same documents, locks contended for the same name and disclosed each other's
  holder IDs. Adding a data-plane surface means adding its accessor in the same
  change. `pkg/server/collection_namespace_test.go` asserts the accessor COUNT
  by reflection, so widening the interface without widening the coverage fails
  there rather than at a call site in another package.
- **A composite servicer is REBUILT over the wrapped engines, never copied.**
  `applyComposite` wraps each engine and then constructs the collection service
  from the wrapped pair; copying the bundle's own would carry the raw engines
  through. Isolation is a property of the engines a service holds, not of the
  struct it sits in.
- **No prefix is exempt from namespacing**, the reserved `__sys:` keyspace
  included. An exemption would be a second tenancy mechanism beside the
  namespace; it also let a crafted tenant id, a crafted DAO entity type, and
  `pkg/permission`'s tenantless scope keys each reach across tenants.
- **A by-id IAM operation routes to the tenant that HOLDS the id, not the
  caller's.** `tenant.IdentityIndex` maps `id -> tenant` in the system tenant's
  identity namespace; `tenant.OwningTenant` is the single rule both routers
  apply. Unindexed ids route to the caller (reach never widens), a caller who
  may not reach the owner is routed to its own tenant and gets a plain
  not-found (a cross-tenant denial would be an existence oracle), and a sealed
  owning barrier refuses with `ErrTenantSealed` — never a fallback read.
- **The index entry carries the id and the tenant ONLY.** It lives outside
  every barrier by necessity, so it may leak existence and placement and
  nothing else.
- **The index is maintained by the function that writes the record**, next to
  the record's other index writes — never by a sibling maintainer, which is how
  every write-path/read-path divergence in this repository started.
- **An index entry is a CLAIM, taken atomically, before the record is
  written.** Role ids are caller-supplied, so a last-writer-wins entry would
  let one tenant capture another's id and redirect every by-id operation on
  it. The claim uses `kvstore.AtomicKVStore.PutIfAbsent`, which every state
  machine implements — `PebbleStateMachine`, `MVCCEngine` and `KVEngine`,
  the last of which gained its handler with #41. A store without it is
  REFUSED (`tenant.ErrIdentityStoreNotAtomic`), never downgraded to
  read-then-write. Refusing before the first write is what leaves a refused
  capture with nothing behind.
- **A claim is released through a GUARDED delete**
  (`kvstore.AtomicKVStore.CompareAndDelete`), never a plain one. A guarded
  create with an unguarded release is half a primitive: two releasers both
  read holder A, the first deletes, another tenant legitimately claims the
  now-absent id, and the second delete removes THEIR claim.
- **A failed write keeps its quota reservation only when its outcome is
  UNKNOWN.** `quotaKVService` reserves bytes before the inner write and must
  decide, on an error, whether the tenant still owes them. Three cases, not
  two: a definitive REFUSAL (a guard evaluated and declined) releases; a
  definitive FAILURE releases, because it proves nothing was stored —
  validation, encoding, `NotLeader`, a state machine that applied nothing,
  a refused atomic capability; anything else RETAINS, because a Raft proposal
  can commit and then fail to deliver its response.
  The default for an unrecognised error is to retain. Both mistakes are real
  and they are not symmetric. Releasing an unknown outcome under-counts a
  value that IS stored, and the limit stops bounding anything. Retaining a
  definitive rejection leaks bytes on every invalid request — and for a
  **prefix tenant that is permanent**, because the reconciler repairs volume
  tenants from real storage and a prefix tenant's usage only ever climbs, so
  a caller sending invalid requests in a loop could exhaust the quota without
  writing a byte.
- **A claim is EDITED through a guarded swap**
  (`kvstore.AtomicKVStore.CompareAndSwap`, added in #56), never a plain put.
  It completes the set: `PutIfAbsent` takes a claim, `CompareAndDelete`
  releases one, and `CompareAndSwap` moves one from a value the caller
  observed to a new one, refusing if the record moved in between. A plain put
  over a claim discards whatever landed since the read, which is the same
  lost-update the guarded create exists to prevent, one operation later.
  An absent key is NOT created by a swap — a swap edits what is there, and
  creating on absence would let a released claim be resurrected by a caller
  holding a stale view of it.
- **Absence is `(false, nil)` for every guarded verb**, never an error. A
  backend that reports a missing key as a failure makes callers unable to
  distinguish "nothing was there" from "the store is broken", and the natural
  retry loop around a guarded verb spins on an error that will never clear.
  This holds through the encrypted wrappers too: `seal.StorageBarrier` maps
  its backend's `ErrSecretNotFound` to `(false, nil)` for both the swap and
  the delete.
- **A claim succeeds only for the call that created it.** A pre-existing entry
  is refused whatever tenant holds it, the claimant's own included: proceeding
  on a claim you did not win lets a concurrent same-tenant delete release it
  under you, leaving a record no by-id operation can reach. The cost is that a
  create dying mid-way burns its id, which is the safe direction.
- **An empty namespace prefix is an error, not a pass-through.** It would hand
  the caller the entire store root, which is the unconfined access the wrapper
  exists to prevent, delivered silently.
- **Index results crossing a namespace are dropped, not stripped.** A key
  outside the namespace has no business in its result; passing it through
  stripped would hand the caller another tenant's record.
- **A store is told which keys an index covers** (`EntityIndex.KeyPrefix`)
  rather than inferring `{entityType}/`. The inference holds only while the
  entity type is the leading key segment, which namespacing breaks — and a
  store that infers wrongly indexes nothing while reporting success.
- **Shared-prefix tenants have engine-backed, per-tenant indexes.** The
  namespaced store registers `{tenant}/{entity}.{Field}` with the absolute key
  prefix `{tenant}/{entity}/`; `lifecycle.kvStoreAdapter` hands the engine
  that prefix made relative to `engine.IndexNamespace(name)`, so the scope
  the engine maintains (`engine.ComposeIndexScope`) is exactly the prefix the
  tenant's keys carry. The same translation serves a `{tenant}/{database}/`
  namespace and a DAO built `WithDatabase`. A name whose namespace the keys do
  not live under is refused with `kvstore.ErrIndexUnsupported`.
- **Index maintenance selects by scope on every path.** The Raft apply path
  and `KVEngine.PutWithIndexes`/`DeleteWithIndexes` both keep exactly the
  indexes whose scope covers the key. Nothing parses a key for an entity type:
  a tenant or database namespace displaces the first path segment, which is why
  the name-prefix filter that did so was removed.
- **Encrypted tenant stores still refuse index operations** (`barrier`,
  `volume`, `raft`); `pkg/dao` plans a full scan. See the architecture
  document for why this is an open design question rather than a gap.



- **Isolation type is immutable once the tenant holds data** —
  `ErrTenantHasData`, HTTP 409.
- **`prefix` is logical separation, not cryptographic isolation.**
- **`barrier`, `volume` and `raft` require `storage.volume_pools`.** Without a
  pool, registering a barrier fails with
  `volume pool not configured on barrier registry`.
- **Every volume-pool path is validated before it is created.** A pool's paths
  reach `os.MkdirAll`, so `pool.BuildLocalCandidates` requires each one to be
  non-empty, absolute, canonical (`filepath.Clean(p) == p`), free of any
  symlinked component, and non-overlapping with the other paths in the same
  request. Failures are `*VolumePoolPathError`, wrapping either a
  `*validation.PathError` naming the predicate or a `*VolumePoolOverlapError`
  naming the volume it collides with.

  The symlink predicate is not redundant with the canonical one: `filepath.Clean`
  reasons about a path lexically while the syscall follows it physically, so a
  perfectly canonical `/srv/volume-a/vol` can still be a symlink elsewhere. The
  overlap predicate is separator-aware — `/data/foo2` is **not** inside
  `/data/foo` — because two volumes where one contains the other are one volume
  counted twice, and a placement strategy would report free space that does not
  exist.

  The five path predicates are shared with tenant `DataRoot` validation through
  `pkg/validation`; neither package carries its own copy.
- **A `volume` tenant's host is built lazily**, only after its barrier is
  unsealed.
- **Entitlements are checked before quotas.** A plan that does not include a
  tier is refused without consulting counters.
- **`Max*` semantics**: `-1` means unlimited, `0` means use the system default.
- **`MaxStorageBytes` meters every data-plane write surface**, collection
  documents included: the registry wraps the resolved KV, MVCC, batch,
  transaction, time-series, vector and collection servicers on every resolve,
  with the tenant's current limit. A collection document costs its body bytes
  (see [../collections/engineering.md](../collections/engineering.md#tenant-scoping)).
- **`MaxStorageBytes` for `volume` tenants is reconciled in the background** by
  a reconciler that walks each volume tenant's `DataRoot`, at
  `tenant.quota_reconcile_interval` (default `5m`), to correct drift.
- **Tenant identity is sourced only from the authenticated mTLS identity**,
  never from a header or metadata field.
- **An absent tenant is FOLDED at the boundary, never inferred at the
  predicate.** `tenant.IsSystemTenant` answers true for both `SystemTenantID`
  and `""`, because the unscoped keyspace and the system keyspace are the same
  keyspace — `StoreResolver`, `IdentityIndex`, `kv_service` and
  `database.manager_tenant` all ask it that question and all depend on the
  empty branch. That branch is safe only under a precondition: on an
  AUTHORIZATION path the predicate must never be handed `""`. `tenant.Normalize`
  is the single fold that establishes it, and every boundary that binds a
  tenant calls it — `FromContext` (context), `auth/middleware.finalizeIdentity`
  (identity **and** context), `sql/provider.QRDBSession.TenantID` (SQL
  session). Open-coding `if id == "" { id = SystemTenantID }` at a new boundary
  is the defect, not the fold: before #188 `auth/middleware` folded the context
  and left `identity.TenantID` raw, and `rbac.IsSystemTenant` reads the
  identity, so the one value the authorizer consumed was the one value the
  boundary did not normalize.
- **The fold maps absence onto PRIVILEGE, and says so.** Folding `""` to
  `SystemTenantID` preserves `IsSystemTenant`'s answer exactly — it is a
  normalization, not a policy — so a principal that authenticates with no
  tenant binding is system-scoped both before and after. `pkg/auth/mtls.go`
  mints exactly that shape: a certificate that chains to the trusted CA but
  carries neither a SPIFFE tenant SAN nor a Subject OU authenticates with
  `TenantID ""` and no error. qrdb's own issuance cannot produce one
  (`pkg/cert` refuses an empty `TenantID`; `cmd/qrdbd` issues its admin cert
  with `OU=system`), so the shape arrives only from a CA-signed certificate
  minted outside qrdb's issuance path. Making absence fail CLOSED instead —
  a distinct authorization-facing predicate for which "unknown" is not
  "system" — is a deliberate change to who can authenticate, not a
  normalization, and is tracked separately (#188). The precedent for it
  already exists at one boundary: `sdk/go/transport/embedded`'s RBAC surface
  REFUSES an identity with an empty `TenantID` (`InvalidIdentityError`) rather
  than folding it.
- **Per-node key independence**: the tenant root key is transported over
  authenticated mTLS with HPKE and **re-wrapped under each receiving node's own
  system barrier** (system-managed) or the tenant's strategy (self-managed).
- **Replica placement is quota-checked and spread across distinct healthy
  nodes**; `TenantShardReconciler` converges each node's assigned replicas and
  they survive restarts via the persistent placement table.

## Tenant teardown

`TenantService.DeleteTenant` and `kvTenantService.DeleteTenant` run the same
six steps in the same order, because keyspace isolation is not a property of
encryption: a node with the seal subsystem off still has prefix tenants, and
they are exactly the tenants whose data stayed readable after a delete.

```
refuse a RESERVED id
  -> refuse if databases remain
  ---- the fence: nothing below this line is reversible -------------
  -> write the tombstone
  -> delete the metadata record
  ---- past the fence: the tenant no longer resolves for anybody ----
  -> shred the tenant DEK
  -> destroy the barrier and its durable records   (seal-enabled only)
  -> reclaim the keyspace
```

### Persist the retirement before anything irreversible

**That single rule places every step.** The two writes above the line destroy
nothing, so a failure in either returns with the tenant exactly as it was and
its error can be read as "nothing changed". Everything below the line is a
destruction, and by the time any of it runs the retirement is already durable.

The ordering the first implementation shipped was the inverse — shred first —
and it was argued from a choice of two: shred-then-delete can strand metadata
for a tenant whose key is gone, and delete-then-shred can strand a key nothing
names *and* leave the data readable in the window. Shred-first was taken as the
lesser of those. **Its real cost was worse than either.** A transient failure of
the tombstone write returned `*StoreError{Op: "put-tombstone"}` — a message that
reads as *"the delete did not happen"* — over a tenant that was still live,
still listed and still resolvable, and whose every byte was already permanently
undecryptable. One store error, total unrecoverable loss, and a message telling
the operator to shrug.

Fencing first was not on that list of two, and it dominates both:

| | data readable in a window? | live tenant with no key? | residue if interrupted |
|---|---|---|---|
| shred first | no | **yes, and permanent** | metadata for a keyless tenant |
| delete first, no tombstone | **yes** | no | a key nothing names |
| **fence first** | no — steps 2 and 3 make the tenant unresolvable before anything is destroyed | no | a retired id whose DEK still exists |

The residue fencing accepts is reachable by nobody and is the same kind the
barrier destroy and the keyspace reclaim already leave behind. It is removed by
re-issuing the call.

### Every step past the fence is idempotent

That is what makes the residue a resumable state rather than a wedge, and it is
a requirement on the seams, not an accident:

| Step | Absent-resource behaviour |
|---|---|
| `DEKShredder.DeleteTenantDEK` | a tenant with no DEK is success; the storage delete treats not-found as success |
| `BarrierRegistry.DestroyTenant` | returns `ErrTenantNotFound`, which `DeleteTenant` treats as success |
| `KeyspaceReclaimer.ReclaimTenantKeyspace` | an already-empty keyspace is success |

So re-issuing `DeleteTenant` over a partly finished teardown completes it.

### The error says which side of the fence it came from

Every failure past the fence is wrapped in `*tenant.TeardownError`, whose
message states that the tenant is retired and no longer resolvable and that the
remedy is to re-issue the call. A failure at or before the fence is **not**
wrapped in it. `errors.As` still finds `*DEKShredError`, `*BarrierError` and
`*KeyspaceReclaimError` underneath, so a transport that maps those keeps
mapping them.

### The reserved-id guard runs first

`validateDeletableTenantID` is the front of both `DeleteTenant`
implementations, before the metadata read and before any commit. It shares
`reservedTenantIDReason` — the single declaration of the reserved set — with
`validateReservedTenantID` on the create path, and differs from it in one
respect only: the create path honours the `WithInternalCall` first-boot
bypass and the delete path has none.

It also applies `validateTenantIDShape`, so an id carrying a path separator is
refused on the delete path too rather than composing a tombstone key outside
its own range.

`*ReservedTenantIDError` maps to **400 / `InvalidArgument`**. It previously
had no mapping on any transport and fell through to 500 / `Internal`, which
reported a client naming a reserved id as a server fault — latent on the
create path, reachable on the delete path once the guard was hoisted there.

### The tombstone

- **Key:** `__sys:tenants-deleted/{id}`, holding a `tenant.Tombstone`.
- **Sibling of `__sys:tenants/`, not a child.** `ListTenants` scans the
  metadata prefix; a tombstone nested under it would have to be filtered out
  at every reader.
- **Written to the tenant metadata store**, which in a cluster is the
  replicated system shard, so the refusal survives restart and holds on every
  node.
- **A record that will not decode still retires the id.** The existence of the
  record is the fact that matters; releasing an id because of a decode failure
  is the one outcome the tombstone exists to prevent.
- **An unreadable store fails the create.** Creating a tenant is the operation
  whose whole safety property is that the id was never used; an unanswerable
  "was it?" must not be read as "no".
- **A create is atomic against a delete of the same id.** `CreateTenant`
  commits its metadata record through `kvstore.FencedCreator.PutIfAllAbsent`:
  the record is written only while the record **and** the tombstone are both
  absent, decided in one step — a single transaction with a `version == 0`
  compare per key on the replicated store. The earlier tombstone and duplicate
  reads are fast refusals before anything is provisioned; they are not the
  guarantee. Every interleaving of a create and a delete therefore ends in
  exactly one outcome: the create lands before the tombstone (and the delete,
  which removes whatever record it finds, removes it), or the create is refused.
  A retired id never has a live record beside its tombstone.
- **Two concurrent creates of one id have one winner.** The record key is its
  own fence, so the loser is refused with `ErrTenantAlreadyExists` and the
  winner's record is untouched.
- **A create that loses to a retirement destroys what it provisioned.** A
  `barrier`, `volume` or `raft` create provisions its barrier before the
  commit. When the commit is refused with `*TenantDeletedError`, the create
  calls `BarrierRegistry.DestroyTenant`, removing the durable tenant-root record
  the boot path restores from — so the retired id's barrier does not return on
  restart. A create that loses to another create, or whose outcome is unknown,
  only unregisters its in-memory barrier: the durable records may belong to the
  winner.
- **A store without the capability is refused**, with
  `ErrTenantStoreNotAtomic`, before anything is provisioned — never degraded to
  a read followed by a write. Both production stores carry it: the replicated
  seal backend and, on a seal-disabled node, the transaction-backed KV adapter.

### The three seams

`DeleteTenant` reaches four things this package cannot touch, so three are
interfaces wired at composition time (the fourth, the barrier registry, is
already held). All three are optional; a composition that wires none still
retires the id, which is the security half — and which the fence commits
before any of the three is called.

| Seam | Satisfied by | Wired at |
|---|---|---|
| `tenant.DatabaseLister` | `*database.Manager` (`TenantDatabases`) | `lifecycle.BuildServices` |
| `tenant.DEKShredder` | `dek.TenantDEKManager` (`DeleteTenantDEK`) | `lifecycle.BuildServices`, from `Infra.TenantDEKManager` |
| `tenant.KeyspaceReclaimer` | `*service.KeyspaceReclaimService` | `lifecycle.BuildServices` |

They are set through `tenant.TeardownWiring`, resolved from the
`TenantServicer` the node built, for the ordering reason
`database.Manager.SetKeyspaceReclaimer` exists: the tenant service is
constructed before the data plane it needs.

**`*service.KeyspaceReclaimService` satisfies both `database.KeyspaceReclaimer`
and `tenant.KeyspaceReclaimer`.** One walk, two scopes. A database's keyspace
and a tenant's are the same shapes of key over different scopes, so a second
implementation would be a duplicate that drifts the first time a sixth shape
is added.

### With no lister wired, the delete proceeds

That is not a fail-open. Correctness does not rest on the refusal: the
tombstone retires the id and the reclamation removes the bytes whether or not
the databases were dropped first. The refusal buys a BOUND on the reclamation
and an explicit teardown order. An enumeration that FAILS is a different
matter and aborts the delete — "I could not find out" is not "none".

Every composition `lifecycle.BuildServices` produces wires it, because the
database manager is never nil there.

### `DestroyTenant`, not `UnregisterTenant`

`UnregisterTenant` seals and forgets the in-memory barrier and leaves two
durable records in place:

| Record | Read by |
|---|---|
| `__registry:tenant_root/{id}` | `BarrierRegistry.RestoreVolumeTenants` at boot |
| `__sys:volumepool/assign/{id}` | `resolveTenantPoolBase` on the next provision |

So a deleted tenant's barrier came back on the next restart.
`BarrierRegistry.DestroyTenant` releases the barrier and closes the
per-tenant Pebble, removes the `DataRoot`, and only then deletes both records
— the files are released before the directory holding them is unlinked, and a
removal that fails leaves the records, so a retry still finds the directory.
(An earlier revision of this section listed the records before the
directory; the code removes the directory first.)

The removal is `BarrierRegistry.RemoveDataRoot`: it removes everything
**inside** the `DataRoot`, then the root itself. A dedicated `DataRoot` is
often a mount point, and rmdir on a mount point fails with `EBUSY` (or
`EXDEV`) however empty it is; a root that has been emptied and refuses its own
removal that way counts as reclaimed and is left for the operator to unmount.
Any other failure is returned as `*TenantDataRootRemoveError`. The filesystem
is reached through `seal.DirectoryRemover`, which `seal.WithDirectoryRemover`
replaces, so the failure paths are testable without a failing disk.

It returns `ErrTenantNotFound` when the tenant was not registered here at all,
which `DeleteTenant` treats as success so the delete stays re-issuable.

### Failure modes

| Step fails | State afterwards | Remedy |
|---|---|---|
| Reserved or malformed id | nothing changed — no read, no commit | the id is not deletable; there is no remedy and none is wanted |
| Database enumeration | nothing changed | fix the catalog, retry |
| Databases remain | nothing changed | drop them; the error names them |
| Tombstone write | nothing changed — tenant intact, id not retired, **DEK untouched** | retry |
| Metadata delete | id retired, record still present, DEK untouched | retry; the delete resumes from the top |
| DEK shred | id retired, metadata gone, tenant unresolvable; the key may or may not be gone | retry; `DeleteTenantDEK` is idempotent |
| Barrier destroy | id retired, metadata gone, DEK shredded | retry; `DestroyTenant` is idempotent |
| Keyspace reclaim | id retired, metadata gone, DEK shredded, bytes remain | retry; the reclamation is idempotent and a tombstoned tenant is still deletable |

The last three arrive wrapped in `*TeardownError`; the first five do not, and
that is the distinction the message carries.

Re-issuing `DeleteTenant` on a tenant whose metadata is already gone but whose
id is tombstoned resumes from the shred rather than reporting not-found. An
id that was never used is still `ErrTenantNotFound`.

## Retired tenant ids

### Listing — `ListDeletedTenants`

```go
ListDeletedTenants(ctx context.Context, pageToken string, pageSize int) (*tenant.DeletedTenantsPage, error)

type DeletedTenantsPage struct {
    Tombstones    []tenant.Tombstone
    NextPageToken string
}
```

- **A paged range read over `__sys:tenants-deleted/`**, in ascending id
  order. Both tenant services share one implementation.
- **`pageSize`**: `0` means `DefaultDeletedTenantsPageSize` (100); above
  `MaxDeletedTenantsPageSize` (1000) is served at the maximum; negative is
  `*InvalidPageRequestError{Field: "page_size"}`.
- **`pageToken`** is opaque (the last id of the previous page, base64url). A
  token that does not decode is `*InvalidPageRequestError{Field:
  "page_token"}`. A token that does decode is honoured whether or not this
  service minted it: it is only a lower bound, and grants nothing. The next
  page starts strictly after the id it names, so a retirement landing between
  pages is never listed twice.
- **Cost, stated.** Neither store interface the tenant services run on offers
  an ordered range scan bounded by a start key and a limit, so each page lists
  the KEYS of the whole tombstone prefix — O(retired ids) — and reads the
  values of at most one page. The prefix is bounded by the number of deletions
  ever made.
- **The key is authoritative for the id.** A body that will not decode is
  listed as `Tombstone{ID: id}` with a zero `DeletedAt` — the id is retired
  either way.
- Permission `tenants:list_deleted`, **cross-scope** (system tenant only).

### Reclaiming — `ReconcileDeletedTenants`

```go
ReconcileDeletedTenants(ctx context.Context) (*tenant.ReconcileReport, error)

type ReconcileReport struct {
    Inspected int
    Reclaimed []tenant.ReclaimedPath // {TenantID, Path}
    Skipped   []tenant.SkippedPath   // {TenantID, Path, Reason}
}
```

Node-local by nature. Once per pass it reads the **live set strictly**: every
tenant record is decoded, and a record that will not decode **aborts the
pass** — an unreadable live tenant has an unknown `DataRoot`, and that unknown
could be any candidate below. (`ListTenants` skips such a record; that is right
for a listing and wrong for the feed of a destructive operation.) Live claims
are each live tenant's recorded `DataRoot` plus everything the registry
attributes to it on this node. The pool bases and the reserved data roots are
read once too. Then, for each retired id, in order:

1. **Skip the id whole** if a record exists for it (`SkipLiveTenantRecord`) —
   a direct existence read of the id's own key, so a record that exists but
   will not decode still counts.
2. **Collect the candidates** — `BarrierRegistry.TenantDataRootCandidates`:
   the id's registered `DataRoot`, the one its local tenant-root record
   names, and `{poolBase}/{id}` under every local pool base. Each is returned
   **as recorded, never cleaned**, and de-duplicated on its cleaned form: a
   recorded `/srv/qrdb/x/..` cleans to `/srv/qrdb`, a different directory, so
   the path must be judged before anything cleans it.
   **An id already fully reclaimed is skipped silently** — no candidate exists
   on disk and `BarrierRegistry.HasTenantState` reports no barrier, record or
   slot assignment — so a pass does not stop runtimes or touch records for
   every retirement ever made.
3. **Stop the runtime** through the `tenant.TenantRuntimeCloser` seam —
   `server.NewTenantRuntimeCloser` closes the id's `TenantHostRegistry` host
   and every dedicated shard `RaftBackendFactory` started for it. Close, never
   purge: which directories go is this pass's decision.
4. **Release the barrier** — `BarrierRegistry.ReleaseTenant` seals and forgets
   it in memory and closes the per-tenant store. Nothing durable changes.
5. **Remove each existing candidate** with `BarrierRegistry.RemoveDataRoot`
   (contents first, so a mount-point root is reclaimed), except one that is
   not an absolute, canonical path **as recorded** (`SkipNotCanonical` — a
   relative or dotted recorded root is never resolved), that is, contains or
   lies inside a directory a live tenant claims (`SkipOverlapsLiveTenant`),
   that is or contains a pool base or a reserved data root, or that lies
   inside a reserved data root without lying strictly inside a pool base
   (`SkipOverlapsReservedRoot`). The pool-base exemption exists because a pool
   may be carved out of a reserved root, and every tenant's pool directory
   lies inside its pool base by construction. Every skipped path is logged at
   `WARN` with `tenant_id`, `path` and `reason`. **The live set is re-read,
   strictly, right before each removal**, so a tenant created on a peer since
   the pass began keeps its directory.
6. **Drop the durable records last** — `BarrierRegistry.DropTenantRecords`
   deletes the tenant-root record and the slot assignment. An id whose
   removal failed never reaches this step, so the record that names the
   directory survives and the next pass finds it again; dropping the records
   first would orphan an operator-supplied `DataRoot` that nothing named any
   more.
   `DestroyTenant` runs the same three steps in the same order —
   `ReleaseTenant`, remove the `DataRoot`, `DropTenantRecords`.

**The create side closes the window from the other end.** Registering a
`volume` or `raft` tenant at a `DataRoot` another tenant's tenant-root record
on this node still names — for instance a deleted tenant this node has not
reclaimed yet — is refused (`seal.DataRootClaimedError`, surfaced by
`CreateTenant` as `*DataRootOverlapError`, which every network transport maps
to 409 / `FAILED_PRECONDITION` and the embedded transport returns as is), so a
new tenant is never handed a directory the reconcile is entitled to remove. When the claimant is
tombstoned the error names it as a **retired tenant** and carries a `Remedy`
naming `qrdb tenant reconcile-deleted`. A registry that cannot list the
records to check returns `*seal.ClaimedRootCheckError` rather than a bare
storage error.

Every step is idempotent. Failures split in two. A **guard** failure —
listing the tombstones, reading the live set (including the re-read) — aborts
the pass with `*ResidueReconcileError{TenantID, Op, Path}`, because nothing can
be judged without it. A **per-id** failure — a removal, stopping the runtime,
dropping the records — is collected and the pass continues with the next id;
the pass then returns its report together with `*ResidueReconcileErrors`,
whose `Unwrap() []error` exposes each `*ResidueReconcileError`; the report
carries the same failures as `Failed []FailedReclaim`. Every transport asks
`tenant.ReconcileResponseError(report, err)` what to answer: nil when the
error only collects per-id failures the report already carries — the
transport then answers success with the report — and the error otherwise.
The Go SDK turns a non-empty `Failed` back into
`*transport.ReconcileDeletedTenantsError`, returned beside the response.
Re-running resumes. The seal-disabled service owns no directories and reports only
`Inspected`.

**Wiring.** `server.BuildTenantInfra` runs the pass **before**
`RestoreVolumeTenants` — nothing is running yet — logging a failure without
failing the boot (and still logging what a partly failed pass reclaimed), and then wires the runtime closer into the tenant service
(`tenant.ResidueWiring`, via `server.WireTenantRuntimeCloser`). `sdk/go/node`
re-wires it into the tenant service a system-scope recycle rebuilds.
Permission `tenants:reconcile_deleted`, **cross-scope**.

**Residual, stated.** The re-read right before each removal narrows the
window to the removal itself: a tenant whose record commits on a peer between
that read and the `RemoveAll` is not seen. Its `DataRoot` must be empty at
creation, and a create on THIS node at a path a record still names is refused,
so the exposure is an empty directory created on another node in that
instant.

## Governance planes

| Plane | Controls | Enforced at |
|---|---|---|
| RBAC permissions | which operation a principal may call at all | transport middleware |
| Numeric quotas | how many of a resource a tenant may hold | service layer, before delegating |
| Feature entitlements | which tiers/features the plan permits | service layer, **before** the quota check |

**Do not collapse entitlements into RBAC roles.** They answer different
questions and are owned by different people.

Provisioning-governance permissions: `tenants:set_isolation`,
`tenants:set_entitlements`, `databases:create_raft`, `databases:create_volume`.

### Tenant-administration authorization

The per-route permission is checked against the CALLER's tenant, which says
nothing about the tenant the request names. The second decision lives in one
place, `pkg/auth/middleware/tenant_admin.go`, and every network transport calls
it:

| Operation | Decision |
|---|---|
| get / delete | `AuthorizeTenantTarget(ctx, id, tenants:read / tenants:delete)` |
| update | `AuthorizeTenantUpdate(ctx, id, patch)` — `tenants:update`, plus `tenants:set_isolation` when `patch.IsolationType` is set |
| quotas / entitlements | route permission `tenants:set_quotas` / `tenants:set_entitlements` (cross-scope), re-checked against `id` |
| create | route permission `tenants:create` (cross-scope) |
| list | `FilterTenantList` — every tenant with `tenants:list:cross` against `system`, else the caller's own; any refusal other than an RBAC denial is returned, not narrowed |

The four plan permissions are `ScopeCross`
(`pkg/permission/tenant_provisioning_scope.go`). The built-in Admin role carries
them in every tenant; the scope rule makes them inert outside `system`.

Each transport validates the id with `tenant.ValidateTenantIDShape` before it
authorizes, and maps `*tenant.InvalidTenantIDCharError` to 400 /
`INVALID_ARGUMENT` (MCP classifies through gRPC's mapper).

`rbac.RBACAuthorizer` resolves an identity's role permissions under the
IDENTITY's tenant, never the request's target: the role store refuses a role
outside the context tenant, so a `system` operator's first decision against
another tenant used to fail whenever the permission cache was cold.

`tenants:delete` stays `ScopeSelf` (owner ruling 2026-09-29): a tenant's Admin
may delete its own tenant.

The list probe (`tenants:list:cross`) is a capability probe, not an access
decision: it runs decide-only, charging no rate-limit token and writing no
audit record, so an ordinary tenant-Admin list records no denial and does not
fail when the audit sink cannot record one.

The "tenant must be empty before an isolation change" precondition lives in
the tenant service (`pkg/tenant/isolation_change.go`), so every transport
reaches it. Both tenant services embed the check and ask a
`tenant.TenantDataProbe` whether the tenant holds data; `service.TenantDataProbe`
is the implementation (the unfiltered MVCC count where the shard has MVCC, the
KV list where it does not), and `server.New` wires it over the node's service
registry through `tenant.IsolationChangeWiring`. No probe, or a probe error,
fails the change closed. A patch naming the current isolation type changes
nothing and consults no probe.

Both barrier bootstrap fallbacks (`AuthorizeTenantBarrierInit`,
`AuthorizeTenantBarrierSealedOp`) return a `*RateLimitedError` immediately,
next to the audit-failure check: the limiter refuses before RBAC is consulted,
so its answer is not a denial the window may override.

### Quotas — `quota.Quotas`

`MaxDatabases`, `MaxRaftGroups`, `MaxReplicasPerGroup`, `MaxTotalReplicas`,
`MaxStorageBytes`, `MaxDatabaseSizeBytes`, plus `MaxCollections`, `MaxKeys`,
`MaxUsers`, `MaxRoles`, `MaxCertificates`, `MaxBackups`, `MaxVectorDims`,
`MaxTSMetrics`. Each has a matching `Current*` counter on the tenant record.

### Entitlements — `quota.Entitlements`

| Field | Permits |
|---|---|
| `AllowedIsolationModes` | which isolation modes the tenant may select |
| `AllowDedicatedRaft` | provisioning dedicated Raft-backed databases |
| `AllowVolumeStorage` | provisioning databases on a dedicated volume |
| `AllowSystemManagedBarrier` | opting an isolated tenant into system-managed auto-unseal |
| `AllowedBarrierModes` | which seal strategies an isolated tenant may pick; **empty = built-in software defaults, hardware fail-closed** |

A request for a mode or feature the plan does not grant is rejected with
`*EntitlementError`.

## Configuration

```yaml
tenant:
  default_quotas:
    max_databases: 5
    max_raft_groups: 0
    max_replicas_per_group: 3
    max_total_replicas: 3
    max_storage_bytes: 1073741824   # 1 GiB
    system_barrier_quota: 0         # Isolated tenants denied system-barrier use by default
  default_entitlements:
    allowed_isolation_modes: [prefix]
    allow_dedicated_raft: false
    allow_volume_storage: false
    allow_system_managed_barrier: false
    allowed_barrier_modes: []       # Empty = software defaults (none/passphrase/shamir); hardware fail-closed
  quota_reconcile_interval: 5m
```

These are the built-in defaults: a tenant with no explicit plan may create up to
5 databases, consume up to 1 GiB, use only shared `prefix` isolation, and use only

**The built-in defaults are deliberately conservative**: 5 databases, 1 GiB,
`prefix` isolation only, and dedicated raft, volume storage, system-managed
barrier, system-barrier quota and hardware seal strategies **all denied** until
an operator grants them. Full reference:
[../configuration/](../configuration/).

## Encryption at rest

| Data | Encryption |
|------|------------|
| State-machine Pebble (shared shard) | Barrier-encrypting filesystem under the system barrier |
| Shared Raft WAL / LogDB | Encrypted at rest under the **system barrier** (committed entries are no longer cleartext on disk) |
| `volume` / `raft` tenant SM data | Encrypted under the **tenant DEK** on the tenant's `DataRoot` |
| `volume` tenant raft WAL | Encrypted under the tenant DEK (the per-tenant host barrier-wraps its own WAL) |
| `raft` tenant raft log (shared host) | Encrypted under the **system barrier** (plaintext-at-rest gap closed) |


> **Not implemented.** A **dedicated per-tenant WAL** for `raft`-mode groups
> (Phase 4d Option C) is **designed but not built**. It would encrypt the
> tenant's raft log under its own key on the shared host rather than the shared
> system key. It requires a go-quicraft per-barrier snapshot-pool rework
> (per-barrier epoch tracking) first. Plan:
> [design/4d-per-tenant-wal-option-c.md](../design/4d-per-tenant-wal-option-c.md).

## Deployment requirement

**The tenant service must be configured in production.**

Tenant identity is sourced only from the authenticated mTLS identity, and
unknown-tenant requests are rejected by the tenant service. **If a deployment
wires the data-plane service registry without a tenant service** — a
backward-compatibility path for single-tenant embeddings — **that
unknown-tenant rejection is skipped**: any tenant name presented by an
authenticated client is accepted. It is still namespaced and barrier-checked,
but it is **not validated against a tenant registry**.

That mode is intended only for trusted single-tenant or embedded use. A
production multi-tenant deployment that omits the tenant service has a real
isolation gap.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Changing isolation with data present | `ErrTenantHasData`, 409 | **yes** |
| Isolation mode not in `AllowedIsolationModes` | `*EntitlementError` | **yes** |
| Seal strategy not in `AllowedBarrierModes` | `*EntitlementError` | **yes** — hardware is fail-closed by default |
| Quota exceeded | quota error, before delegating | **yes** |
| Barrier tier without `storage.volume_pools` | `volume pool not configured on barrier registry` | **yes** |
| Unknown tenant, tenant service configured | rejected | **yes** |
| Unknown tenant, **no** tenant service | **accepted** — the documented gap above | **no** |
| Tenant barrier sealed | tenant operations refused | **yes** |
| Deleting a RESERVED tenant id (`system`, `default`, `_*`, `*:*`) | `*ReservedTenantIDError`, 400 / `InvalidArgument`, nothing committed | **yes** |
| Deleting a malformed tenant id | `*InvalidTenantIDCharError` / `*InvalidTenantIDError`, nothing committed | **yes** |
| Creating a tenant under a DELETED id | `*TenantDeletedError`, 409 / `AlreadyExists` | **yes** |
| Deleting a tenant that still owns databases | `*TenantHasDatabasesError`, 409 / `FailedPrecondition` | **yes** |
| Tombstone store unreadable during create | `*StoreError{Op: "tombstone-check"}` | **yes** |
| Deleted-tenant reconcile would touch a live tenant's directory | skipped and reported, never removed | **yes** |
| Deleted-tenant reconcile fails at boot | logged; the boot continues and the verb retries on demand | **n/a** — disk only |
| Id retired by a delete while the create was in flight | `*TenantDeletedError`; provisioned barrier destroyed, nothing committed | **yes** |
| Metadata store has no atomic fenced create | `ErrTenantStoreNotAtomic`, before anything is provisioned | **yes** |
| Tenant DEK shred fails during delete | `*TeardownError` wrapping `*DEKShredError`; the id is already retired | **yes** |
| Database enumeration fails during delete | `*DatabaseListError`, nothing changed | **yes** |
| Keyspace reclaim fails during delete | `*TeardownError` wrapping `*KeyspaceReclaimError`; id already retired | **yes** |
| Resolving a DELETED tenant's keyspace | `*TenantDeletedError`, no fall-through | **yes** |
| Reclaiming the SYSTEM tenant's keyspace | `*service.ReclaimScopeError`, nothing deleted | **yes** |
| Tenant shard elects no leader in 15s | `*ShardLeaderWaitError` (wraps `ErrTenantShardNoLeader`), host build refused | **yes** |

### Why the no-leader error carries a measurement

Building an `IsolationVolume` tenant host starts a single-node Raft shard and
waits for it to elect itself leader before binding services; until it does,
every proposal fails with "not leader". That wait is bounded at 15 seconds.

When it expires the error does not merely say the shard did not elect. It
reports what the wait **observed** — how many times the shard was actually
asked, the real time spent, the longest gap between two consecutive polls, and
the last error `GetLeaderID` returned — and names one of three causes:

- **the shard could not be asked** — `GetLeaderID` kept failing, so that error
  is the finding and the timeout is a symptom;
- **the poller was starved** — some gap exceeded ten poll intervals, so the
  scheduler consumed the budget and the election never received the wall time
  the budget implies. This is host contention, not an election defect;
- **the shard genuinely did not elect** — the poller ran on time for the whole
  budget. This is a real failure.

The distinction is not cosmetic. The bare sentinel that preceded it read
identically whether the shard was broken or the machine was merely busy, and
under CI load the second is common: a 20 ms poll sleep overruns by microseconds
on an idle host, so ten consecutive missed intervals is direct evidence of
descheduling rather than of anything raft did.

**The 15 second budget is not a tuning knob.** It already dwarfs the election's
configured cost. go-quicraft derives the election timeout as
`ElectionRTT * RTTMillisecond`, and this shard no longer takes the upstream
defaults: the tenant Host is built `WithoutTransport()`, so it carries
`cluster.TransportlessRTTMillisecond` (25 ms) and
`cluster.SingleNodeElectionRTT` (5 ticks) — a **125 ms** election budget.
Until those existed a transport-less host inherited go-quicraft's WAN defaults,
10 * 200 ms = 2 s, and this wait was about seven times them. 25 ms rather than
go-quicraft's documented LAN range was chosen by measurement: `RTTMillisecond`
is also the engine tick interval, so it prices continuous CPU as well as
election latency, and 25 ms captures 95% of the achievable boot latency for 32%
of the CPU that 5 ms costs.

Widening it to quiet a red suite would remove the tests' ability to fail for the
right reason: a genuine regression that quintupled election time would still fit
inside the larger budget, leaving tests that are red when the host is busy and
green when the code regresses. If this wait times out, read the verdict in the
error before changing the number.

### The wait reads time through a seam, and production still reads the real clock

The poll loop takes its clock as a parameter (`shardLeaderClock`: `Now` and
`Sleep`, and nothing else). Production passes `realShardLeaderClock`, so every
number in the error above — elapsed, longest gap — is measured against real wall
time. That is not incidental: the starvation verdict *is* a statement about wall
time, and a wait that measured virtual time in production would report
"the shard genuinely did not elect" on a host that never let it ask.

The seam exists for the tests, and for a reason that is the same defect one
level up from the one the error type was built to fix. Those tests script the
fake shard **by poll number** — fail on poll 1, fail again on poll 3 — while the
loop stops on a **wall-clock budget**. Those are the same quantity only on an
idle host, and on a shared CI runner they diverged: a 30 ms budget at a 1 ms
interval bought **two** polls where an idle host delivers thirty-two, so poll 3
never ran and a test failed asserting on an error its fixture had never been
asked to produce. The message it was checking was *correct*; `budget 30ms, 2
polls, longest gap 36ms` is the starvation verdict, reported accurately about
the starvation that broke the test.

The fix is not a larger budget — that moves the flake to a busier host. Under a
virtual clock, time advances only when the loop sleeps and by exactly the
interval it asked for, so the poll count is arithmetic rather than a bet on the
scheduler. **Do not respond to a future red here by enlarging a budget:** under
that clock a budget cannot be missed, so a failure is a real defect.

The budget is now roughly 120x the election it waits on, so whether it should be
*tightened* is a live question — and a separate one from not widening it. The
argument above establishes only that widening destroys the tests' ability to
fail for the right reason. Tightening needs its own evidence, because what this
wait absorbs is host descheduling rather than election time, and the two do not
shrink together: the election budget fell by 16x, the scheduler's worst pause
did not.

## Test strategy

- **Unit**: entitlement checks running before quota checks, `Max*` semantics for
  `-1` and `0`, the isolation-change refusal, and the volume-pool precondition.
- **The entitlement-before-quota ordering is its own test** — reversing it would
  leak information about counters to a plan with no access to the tier.
- **The no-tenant-service path is tested explicitly** so the gap stays visible
  rather than becoming a surprise.
- **Create/delete races are driven deterministically**
  (`pkg/tenant/create_delete_race_test.go`): the store double applies the
  concurrent operation's write through the real store at the moment of the
  commit and delegates the commit itself, so the outcome is decided by the
  store under test. A restart is simulated by `RestoreVolumeTenants` after the
  losing create. Real-goroutine races assert one winner per id and no
  tombstone beside a live record.
- **Integration**: the multi-node tenant suites —
  `isolation_raft_cluster_test.go`, `isolation_raft_dedicated_wal_test.go`,
  `isolation_raft_rebalance_test.go`, `isolation_raft_scale_test.go` — live in
  the **cluster stage**, which is **not currently wired into CI**.

## Observability

`Current*` counters on the tenant record are the live usage view; the quota
reconciler corrects `volume` tenants' storage drift on its interval. Tenant
provisioning, quota and entitlement changes are audited through
[../audit/](../audit/).
