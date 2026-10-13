# Multi-Tenancy — Engineering Contract

> **Status: NORMATIVE** (the strict-isolation section is RULED, not yet
> implemented) · Scope: go-qrdb · Last verified: 2026-10-06

## Exported surface

`sdk/go`:

```go
func (c *Client) Tenants() transport.TenantClient
func (c *Client) WithTenant(tenantID string) (transport.KVClient, error)
```

`TenantBarrierService` is a **dedicated gRPC service**
(`qrdb.v1.TenantBarrierService`) with `Init`, `Unseal`, `Seal`, `Status`,
`OfficerRegister`, `OfficerList`, `ShareClaimBegin`, `ShareClaimComplete`,
`QuorumBegin`, `QuorumSubmit`, `RekeyStart`, `OfficerRevoke`.

Server option: `WithTenantBarrierService(svc tenant.TenantServicer)`.

Tenant creation delegates, on every transport, to one service
(`pkg/service/tenant_onboarding.go`), wired with
`server.WithTenantOnboarding(svc service.TenantOnboardingServicer)`:

```go
type TenantOnboardingServicer interface {
	// OnboardTenant creates a tenant and, as one unit, registers its trust
	// anchors, creates its first admin with the tenant's Admin role, and
	// binds the admin's certificate. It returns the admin's certificate,
	// never a key.
	OnboardTenant(ctx context.Context, req *OnboardTenantRequest) (*OnboardTenantResult, error)
}

type OnboardTenantRequest struct {
	ID, Name          string
	IsolationType     tenant.IsolationType // empty = prefix
	BarrierAutoUnseal bool
	DataRoot          string
	AdminUsername     string // required
	AdminCSRPEM       []byte // exactly one of AdminCSRPEM and AdminCertPEM
	AdminCertPEM      []byte
	AdminChainPEM     []byte
	TrustAnchorsPEM   []byte // required with AdminCertPEM
}

type OnboardTenantResult struct {
	Tenant *tenant.TenantInfo
	Admin  TenantAdminCredential
}

type TenantAdminCredential struct {
	UserID            uint64 // zero when AwaitsBarrierInit
	Username          string
	CertPEM           []byte
	Serial            string // empty for a bring-your-own certificate
	Thumbprint        string
	AwaitsBarrierInit bool
}
```

`OnboardTenant` calls `bootstrap.ProvisionTenant` with
`AdminEnrollment: AdminEnrollCert`, `Exclusive: true` and
`Signer: &bootstrap.KeyBSigner{Certs: certProvider}` — the tenant's own Key B
CA signs the admin's CSR (`cert.CertProvider.IssueClientCert`, through
`certenroll.IssueFromCSR`).

`pkg/bootstrap`:

```go
type TenantProvisionConfig struct {
	TenantID, DisplayName string
	Isolation             tenant.IsolationType // empty = IsolationBarrier
	AdminUsername         string               // empty = "admin"
	AdminCSRPEM           []byte
	AdminCertPEM          []byte
	AdminChainPEM         []byte
	TrustAnchorsPEM       []byte
	CreateOptions         []tenant.CreateTenantOption
	Exclusive             bool
	AdminEnrollment       AdminEnrollmentMode
}

// TenantProvisionResult gains AdminCertSerial and AdminAwaitsBarrierInit;
// AdminKeyPEM is removed. AdminCertPEM, AdminChainPEM, TrustAnchorsPEM and
// Exclusive are new on the config.
func ProvisionTenant(ctx context.Context, cfg TenantProvisionConfig, deps Dependencies) (*TenantProvisionResult, error)

// CompletePendingTenantAdmin creates a pending admin's account once the
// tenant's identity records can be written; idempotent.
func CompletePendingTenantAdmin(ctx context.Context, tenantID string, deps Dependencies) (bool, error)
```

`Dependencies` gains `TrustAnchors trustanchor.Registry`,
`PendingAdmins tenantadmin.Store`, `TrustDomain string` and `Now func() time.Time`.

`pkg/tenantadmin` — the pending first admin of a tenant whose identity records
are behind its barrier:

```go
type Pending struct {
	Username   string    `json:"username"`
	Thumbprint string    `json:"thumbprint"`
	NotAfter   time.Time `json:"not_after"`
	Serial     string    `json:"serial,omitempty"`
}

type Store interface {
	Put(ctx context.Context, tenantID string, p *Pending) error
	Get(ctx context.Context, tenantID string) (*Pending, error) // (nil, nil) when none
	Delete(ctx context.Context, tenantID string) error
}

func NewKVStore(store kvstore.KVStore) *KVStore
```

`pkg/tenant`: `WithBarrierReadyHook(h BarrierReadyHook) TenantServiceOption`,
where `type BarrierReadyHook func(ctx context.Context, tenantID string)`.
The service calls it (through `notifyBarrierReady`) from
`TenantBarrierInitWithStrategy` and the Shamir init, from
`TenantBarrierUnseal`, from `TenantBarrierQuorumSubmit` when the submission
completes an `OpUnseal` quorum and its cluster seal record is cleared, and from
`TenantBarrierStatus` when the barrier is initialized and unsealed. It is never
called below the threshold, for any other quorum op, or when the operation
fails. A ready barrier can raise it more than once (the unseal, then each
status read), so the hook must be idempotent; `CompletePendingTenantAdmin` is.
`pkg/auth/middleware`: `WithPendingTenantAdmins(p PendingTenantAdmins) Option`;
`pkg/auth/wiring.AuthMiddlewareConfig.PendingTenantAdmins`.

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
  predicate.** `tenantid.IsSystem` answers true for both `tenantid.System`
  and `""`, because the unscoped keyspace and the system keyspace are the same
  keyspace — `StoreResolver`, `IdentityIndex`, `kv_service` and
  `database.manager_tenant` all ask it that question and all depend on the
  empty branch. That branch is safe only under a precondition: on an
  AUTHORIZATION path the predicate must never be handed `""`. `tenantid.Normalize`
  is the single fold that establishes it, and every boundary that binds a
  tenant calls it — `FromContext` (context), `auth/middleware.finalizeIdentity`
  (identity **and** context), `sql/provider.QRDBSession.TenantID` (SQL
  session). Open-coding `if id == "" { id = tenantid.System }` at a new boundary
  is the defect, not the fold: before #188 `auth/middleware` folded the context
  and left `identity.TenantID` raw, and `rbac.IsSystemTenant` reads the
  identity, so the one value the authorizer consumed was the one value the
  boundary did not normalize.
- **The fold maps absence onto PRIVILEGE, and says so.** Folding `""` to
  `tenantid.System` preserves `IsSystemTenant`'s answer exactly — it is a
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
- **Per-node key wrapping, shared tenant key**: the tenant root key is
  transported over authenticated mTLS with HPKE and **re-wrapped under each
  receiving node's own node barrier** (system-managed) or the tenant's strategy
  (self-managed). The wrapping is per node; the tenant root key is the same on
  every replica. **Correction (2026-10-01):** this was described as per-node key
  independence.
- **A tenant seal reaches every replica**, and only the tenant's own
  administrators may issue it — see
  [../barrier-seal/architecture.md](../barrier-seal/architecture.md#sealing-in-a-cluster).
- **Replica placement is quota-checked and spread across distinct healthy
  nodes**; `TenantShardReconciler` converges each node's assigned replicas and
  they survive restarts via the persistent placement table.

## Tenant teardown

`TenantService.DeleteTenant` and `kvTenantService.DeleteTenant` run the same
five steps in the same order, because keyspace isolation is not a property of
encryption: a node with the seal subsystem off still has prefix tenants, and
they are exactly the tenants whose data stayed readable after a delete.

```
refuse a RESERVED id
  -> refuse if databases remain
  ---- the fence: nothing below this line is reversible -------------
  -> write the tombstone
  -> delete the metadata record
  ---- past the fence: the tenant no longer resolves for anybody ----
  -> destroy the barrier and its durable records   (seal-enabled only)
  -> reclaim the keyspace, the tenant DEK keyring included
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
| **fence first** | no — steps 2 and 3 make the tenant unresolvable before anything is destroyed | no | a retired id whose barrier or keyspace still exists |

The residue fencing accepts is reachable by nobody. It is removed by re-issuing
the call.

There is no DEK-shred step any more. The tenant DEK keyring is a record in the
tenant's own resolved store, so the fence makes it unreachable (the resolver
refuses a retired id) and the barrier destroy and keyspace reclaim remove it —
the reclaim covers the dedicated tenant's `seal.TenantSharedNamespace` range,
where its identity records and keyring live, as well as `<tenant>/`.

### Every step past the fence is idempotent

That is what makes the residue a resumable state rather than a wedge, and it is
a requirement on the seams, not an accident:

| Step | Absent-resource behaviour |
|---|---|
| `BarrierRegistry.DestroyTenant` | returns `ErrTenantNotFound`, which `DeleteTenant` treats as success |
| `KeyspaceReclaimer.ReclaimTenantKeyspace` | an already-empty keyspace is success |

So re-issuing `DeleteTenant` over a partly finished teardown completes it.

### The error says which side of the fence it came from

Every failure past the fence is wrapped in `*tenant.TeardownError`, whose
message states that the tenant is retired and no longer resolvable and that the
remedy is to re-issue the call. A failure at or before the fence is **not**
wrapped in it. `errors.As` still finds `*BarrierError` and
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

`DeleteTenant` reaches three things this package cannot touch, so two are
interfaces wired at composition time (the third, the barrier registry, is
already held). Both are optional; a composition that wires neither still
retires the id, which is the security half — and which the fence commits
before either is called.

| Seam | Satisfied by | Wired at |
|---|---|---|
| `tenant.DatabaseLister` | `*database.Manager` (`TenantDatabases`) | `lifecycle.BuildServices` |
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
**inside** the `DataRoot`, then the root itself. It removes nothing that is not
strictly inside one of this node's own volume-pool paths: anything else is
refused with `*seal.DataRootOutsideLocalRootsError` before the filesystem is
touched. The path must be absolute and already clean, and it is compared
twice, both of which must pass: as given, component by component (so
`/pool10/t` is not inside `/pool1`), and with every symbolic link resolved on
both sides, so a link in the pool -- the tenant directory or any directory
above it -- that points outside cannot carry the removal there. A path that
does not exist yet resolves through its deepest existing ancestor; one whose
resolution fails otherwise is refused with the cause. `RaftBackendFactory.CloseAndPurge`, which purges a demoted replica's
shard directory, applies the same guard (`RequireInsideLocalPool`). A dedicated `DataRoot` is
often a mount point, and rmdir on a mount point fails with `EBUSY` (or
`EXDEV`) however empty it is; a root that has been emptied and refuses its own
removal that way counts as reclaimed and is left for the operator to unmount.
Any other failure is returned as `*TenantDataRootRemoveError`. The filesystem
is reached through `seal.DirectoryRemover`, which `seal.WithDirectoryRemover`
replaces, so the failure paths are testable without a failing disk.

It returns `ErrTenantNotFound` when the tenant was not registered here at all,
which `DeleteTenant` treats as success so the delete stays re-issuable.

### Where a tenant lives

A `volume` or `raft` tenant's location is replicated state, so it is recorded
in terms every node resolves against its own disks and never as a path:

| Record | Holds |
|---|---|
| `__registry:tenant_root/{id}` | the pool name, the relative slot, the auto-unseal flag |
| `__sys:volumepool/assign/{id}` | the pool and slot the cluster-wide cursor gave the tenant |
| the tenant record (`TenantInfo`) | no `DataRoot`: it is resolved on read |
| `placement.Replica` | no `DataRoot`: each node resolves its own replica's directory |

`BarrierRegistry.LocalTenantDataRoot` resolves the slot to **this node's**
candidate path and joins the tenant id; `TenantService.GetTenant` and
`ListTenants` fill `DataRoot` from it, and `placementRaftInfoSource` builds a
shard's directory from it. A record that carries a path -- a tenant record
(`*PersistedDataRootError`) or a placement replica (`*placement.ReplicaDataRootError`)
-- is refused rather than trusted: a path names the disk of the node that wrote
it, and trusted on another node it opened, and on reclamation deleted, a
directory that node did not own. There is no migration for such records; the
tenant is re-created on a named pool.

**A refused tenant never takes the others down.** `ListTenants` returns it
beside every other tenant with `TenantInfo.Refusal` set to its typed error and
no `DataRoot`; it does not fail the listing. Boot (`reattachVolumeTenants`,
`reattachRaftTenants`) and `BarrierRegistry.RestoreVolumeTenants` skip it with
an ERROR log and open nothing at the recorded path; the storage-quota
reconciler does not walk it. `DeleteTenant` on a tenant whose record names a
path removes it without touching any directory: it applies the same
no-databases rule, tombstones the id and deletes the record, releases the
barrier (`ReleaseTenant`) and drops the tenant-root record and slot assignment
(`DropTenantRecords`), and reclaims the keyspace. `DestroyTenant` -- which
removes a directory -- is not called. The deleted-tenant reconciliation treats
a live refused tenant as live: the path it names and its pool directory are
both protected roots.

**A registration that cannot record where the tenant lives fails.** The
tenant-root record is what every restart resolves the tenant from, so a
`volume` or `raft` registration that cannot write it -- including behind a
sealed system barrier -- releases the barrier and the store it opened and
returns `*seal.TenantRootPersistError`; `CreateTenant` rolls back as for any
registration failure.

### Failure modes

| Step fails | State afterwards | Remedy |
|---|---|---|
| Reserved or malformed id | nothing changed — no read, no commit | the id is not deletable; there is no remedy and none is wanted |
| Database enumeration | nothing changed | fix the catalog, retry |
| Databases remain | nothing changed | drop them; the error names them |
| Tombstone write | nothing changed — tenant intact, id not retired, **keyring untouched** | retry |
| Metadata delete | id retired, record still present, keyring untouched | retry; the delete resumes from the top |
| Barrier destroy | id retired, metadata gone, tenant and keyring unresolvable | retry; `DestroyTenant` is idempotent |
| Keyspace reclaim | id retired, metadata gone, barrier destroyed, bytes remain | retry; the reclamation is idempotent and a tombstoned tenant is still deletable |

The last two arrive wrapped in `*TeardownError`; the first five do not, and
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

**The create side cannot reopen the window.** A `volume` or `raft` tenant's
directory is `{pool path}/{tenant id}`, resolved on each node, and no caller
chooses it. A retired id is never reused — its tombstone refuses the create —
so a new tenant's directory can never be one the reconcile is entitled to
remove, and no create-time check against other tenants' records is needed.

Every step is idempotent. Failures split in two. A **guard** failure —
listing the tombstones, reading the live set (including the re-read) — aborts
the pass with `*ResidueReconcileError{TenantID, Op, Path}`, because nothing can
be judged without it. A **per-id** failure — a removal, stopping the runtime,
dropping the records — is collected and the pass continues with the next id;
the pass then returns its report together with `*ResidueReconcileFailuresError`,
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
> (per-barrier epoch tracking) first.

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

**A clustered node composes `raft` isolation across nodes through one
helper.** `lifecycle.ComposeRaftTenantCluster` builds the cluster key plane (a
receiver for the tenant keys peers deliver, served on the REST listener, and a
sender delivering this node's), hands `DatabaseService` its key distributor
and `RebalanceService` its placement table, key deliverer and barrier lookup,
and gives both the replica host they add and remove replicas through.
`qrdbd` and a clustered embedded node (`sdk/go/node`) both call it, so an
embedded node is a full member: placed as a replica it receives the tenant's
key and serves the database. It needs what a `qrdbd` node needs -- a REST
listener (the key routes), its node identity (`WithRaftMTLS`, the SPIFFE SAN
naming its Raft address; carried as `RaftTenantClusterConfig.Identity`, a
`*tls.Certificate` whose key may be a device-held `crypto.Signer`), and `auth.peer_admin_urls` when nodes
share a host on distinct admin ports. A missing piece disables what needs it,
with a warning, rather than failing the boot.

**The key plane opens deliveries through a decrypter, not a key.** Peers seal
each tenant key to the public key of the node's certificate (DHKEM-P-256), and
`keydelivery.NewReceiver` takes the `keydelivery.Decrypter` that opens them.
`RaftTenantClusterConfig.Decrypter` carries it, and an embedder supplies it
with `node.WithKeyDeliveryDecrypter`. A composer whose node key is held on a
TPM or HSM builds it with `keydelivery.NewDecrypter` over an
`ecdh.KeyExchanger` that runs the key agreement on the device. Left unset,
the node derives the decrypter from its identity key: a software ECDSA key,
as `qrdbd` has, or a device key that agrees keys itself. A signer that cannot
agree keys and no decrypter leaves the key plane off, with a warning. A
decrypter that is not for the certificate's key would fail every delivery, so
it leaves the key plane off, with a warning.

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
| Deleting a RESERVED tenant id (`system`, `default`, `custodian`, `security-officer`, `_*`, `*:*`) | `*ReservedTenantIDError`, 400 / `InvalidArgument`, nothing committed | **yes** |
| Deleting a malformed tenant id | `*InvalidTenantIDCharError` / `*InvalidTenantIDError`, nothing committed | **yes** |
| Creating a tenant under a DELETED id | `*TenantDeletedError`, 409 / `AlreadyExists` | **yes** |
| Creating a tenant with no admin credential, or two | `*bootstrap.MissingAdminCredentialError` / `*bootstrap.ConflictingAdminCredentialError`, 400; nothing written | **yes** |
| Admin CSR or certificate refused | `*bootstrap.AdminCredentialRejectedError` wrapping the cause, classified as the cause; nothing written | **yes** |
| Trust anchors unparseable or not CA certificates | `*bootstrap.InvalidTrustAnchorsError`, 400; nothing written | **yes** |
| A seam the request needs is not wired (signer, binder, anchor registry, pending-admin store, delete seams for an exclusive run) | `*bootstrap.MissingProvisioningSeamError`, 501; checked before any write | **yes** |
| Exclusive provisioning failed after a write | `*bootstrap.ProvisioningUndoneError` (`Cause`, `UndoFailures`); writes undone in reverse, id retired | **yes** |
| Trust-on-first-init with a certificate other than the pending admin's | `*middleware.FirstInitCertificateMismatchError`, 403 | **yes** |
| Trust-on-first-init under `required` with no pending admin | `*middleware.FirstInitNotOnboardedError`, 403 | **yes** |
| Pending-admin record unreadable during the first-init check | `*middleware.StoreUnavailableError`, refused | **yes** |
| Deleting a tenant that still owns databases | `*TenantHasDatabasesError`, 409 / `FailedPrecondition` | **yes** |
| Tombstone store unreadable during create | `*StoreError{Op: "tombstone-check"}` | **yes** |
| Deleted-tenant reconcile would touch a live tenant's directory | skipped and reported, never removed | **yes** |
| Deleted-tenant reconcile fails at boot | logged; the boot continues and the verb retries on demand | **n/a** — disk only |
| Id retired by a delete while the create was in flight | `*TenantDeletedError`; provisioned barrier destroyed, nothing committed | **yes** |
| Metadata store has no atomic fenced create | `ErrTenantStoreNotAtomic`, before anything is provisioned | **yes** |
| Database enumeration fails during delete | `*DatabaseListError`, nothing changed | **yes** |
| Keyspace reclaim fails during delete | `*TeardownError` wrapping `*KeyspaceReclaimError`; id already retired | **yes** |
| Resolving a DELETED tenant's keyspace | `*TenantDeletedError`, no fall-through | **yes** |
| Reclaiming the SYSTEM tenant's keyspace | `*service.ReclaimScopeError`, nothing deleted | **yes** |
| Tenant shard elects no leader in 15s | `*ShardLeaderWaitError` (wraps `ErrTenantShardNoLeader`), host build refused | **yes** |
| Tenant shard's leader elected but cannot serve a linearizable read within the same 15s (it never committed in its term) | `*ShardReadWaitError` (wraps `ErrShardNotReadReady`), host build refused. A host is never handed out before its first read can succeed -- see clustering/engineering.md | **yes** |

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

**Integration target:** `make integration-test-multi-tenancy` (no Docker; selection in `test/integration/features.mk`). The tenant CLI suites (lifecycle, deleted-id retirement, isolation, barrier) run the real `qrdb` binary against a self-spawned `qrdbd`; the multi-tenancy row of the accounting file lists which transports each capability reaches and which it does not. Per-transport accounting is the `multi-tenancy` row of `test/integration/COVERAGE.md`.

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
  the **cluster stage**. CI runs them in its **full tier** — every push to
  `main`, every `workflow_dispatch`, and a pull request labelled `ci:full` —
  in the `integration-cluster-tenancy` job (`make integration-test-cluster-tenancy`,
  selected by `CLUSTER_TENANCY_RUN` in the Makefile), and the full cluster
  stage runs in the same tier, in the `integration-cluster` job. A plain pull
  request push runs neither, so a change to tenant isolation carries
  `ci:full`. `test/buildgates/cluster_tenancy_gate_test.go` fails
  when the selection drops an `isolation_raft_*` suite, selects fewer than its
  floor of tests, or leaves the job out of the CI result gate. The test
  function count behind that floor is **build-constraint aware**: it parses
  each file's `//go:build` line with `go/build/constraint` and evaluates it
  with only the `integration` tag (plus the default GOOS/GOARCH) set, so a
  file tagged `!integration` — which never compiles into the binary
  `make integration-test-cluster-tenancy` runs — cannot inflate the count. The
  gate also asserts the workflow's top-level `on:` actually names `push` and
  `pull_request`, that the job's `if:` is exactly the full-tier condition, and
  that the result gate tags it `full` — any narrower condition would be
  excused as a tier skip on exactly the pushes to `main` this gate exists to
  cover. *(Corrected 2026-10-02: this said the stage was not wired
  into CI; it has run nightly since 2026-09-22. Corrected 2026-10-12:
  the job moved from every push to the full tier, and the gate asserts the
  full-tier `if:` where it used to forbid any `if:`.)*

## Observability

`Current*` counters on the tenant record are the live usage view; the quota
reconciler corrects `volume` tenants' storage drift on its interval. Tenant
provisioning, quota and entitlement changes are audited through
[../audit/](../audit/).

## Strict isolation — engineering contract (RULED)

> **Status: RULED — partially implemented.** The owner ruled the design on
> 2026-10-01, and S4 on 2026-10-02. Design and rulings:
> [architecture.md](architecture.md#strict-tenant-isolation--design). This
> section is what an implementation must satisfy, and the
> [implementation plan](#implementation-plan) it is built in. File and line
> references are to the tree at v0.26.0-alpha and record where things were at
> that point, not current line numbers. **Correction (2026-10-05):** phase 3's
> first-admin-by-CSR shape already shipped since, independent of this plan —
> see that phase's row. **Correction (2026-10-06):** phase 5's isolation
> policy and the `ScopeSelf` system-tenant-lift removal (the first two
> invariants under [Invariants](#invariants-2) below) shipped; see their
> "Shipped 2026-10-06" notes. Phase 5's G1 (every strict tenant's database on
> a dedicated WAL) did not ship — it depends on phase 4, which is also still
> open — so the feature gate this phase would otherwise register
> (`strict-isolation`) was deliberately not added: there is nothing yet for it
> to gate. Every other phase (0, 1, 2, 4, 6-12) remains as described.

### Inventory — where tenant records live today

Store key:
- **SYS** is the Raft-replicated system shard.
- **SEAL** is the node-local seal Pebble at `{dataDir}/seal`, which is not
  replicated.
- **TB** is the tenant's node-local barrier store: `{DataRoot}/barrier`, or
  `{poolBase}/{tenant}` for pooled `barrier` tenants.
- **NS** is the namespaced data plane.

*Required* means required in the system tenant under the design.

| Record | Key (constant) | Store | Written by | Read by | Required? |
|---|---|---|---|---|---|
| Tenant record: isolation, plan, usage, `DataRoot`, auto-unseal | `__sys:tenants/{id}` (`pkg/tenant/service.go:37`; struct `:79-100`) | SYS | `CreateTenant` `service.go:552` (commit `:742`), `UpdateQuotas` `:1261`, `UpdateEntitlements` `:1295`, usage `:1439/:1493/:1542` | `rest_registry.go:617-654` (every request), `store_resolver.go:182-226`, `tenant_infra.go:316,391` (boot), quota reconciler `cmd/qrdbd/start.go:2496` | **Yes**, minus `DataRoot` and the identity-object counters |
| Tombstone | `__sys:tenants-deleted/{id}` (`pkg/tenant/tombstone.go:60`) | SYS | `fence` `service.go:1178-1190` | create `service.go:573`, `store_resolver.go:233`, `deleted_tenants.go:76,355` | **Yes** |
| Placement | `__sys:placement/{t}/{db}` (`pkg/placement/store.go:29`) | SYS | `database_service.go:1593,945,969,1032` | `tenant_shard_reconciler.go:238,540-618`, `clusterkeys/receiver.go:238`, `raft_backend_factory.go:304` | **Yes**, with opaque database ids (S5) |
| Volume-pool slot | `__sys:volumepool/assign/{id}` (`pkg/seal/barrier_registry.go:340`) | SYS | `barrier_registry.go:467` | `:429,:1408` | **Yes** |
| Tenant root record (`DataRoot`, auto-unseal) | `__registry:tenant_root/{id}` (`barrier_registry.go:705`) | SEAL | `persistTenantRoot` `:720` | `RestoreVolumeTenants` `:768` | Node-level; stays |
| Sealed tenant root key, `.meta`, `.wrapped`, `.migration`, recovery-meta | `tenant/{id}/root-key*` (`barrier_registry.go:1139-1142`; `pkg/seal/barrier.go:345`, `unsealkey.go:77`, `migrate.go:33`, `recovery.go:21`) | TB | `StorageBarrier` | `StorageBarrier` | Node-level; becomes the per-replica blob |
| IAM of `barrier`/`volume`/`raft` tenants (users, roles, factors, lookups, invites, perm scopes) | `__tiam:{t}/…` (`pkg/seal/tenant_shared_kvstore.go:30,36`; chosen at `pkg/tenant/store_resolver.go:367-379`) | SYS, values under the shared tenant key, **key names plaintext** | user/role/factor/invite stores via the resolver | the same, plus the auth middleware `pkg/auth/middleware/middleware.go:396` | **No: moves** |
| User records and indexes | `__sys:users/…`, `__sys:user_idx/{username,email,cert_spki}/{t}/…` (`pkg/user/store.go:40-63`) | inside `__tiam:` or `{t}/` | `pkg/user/store.go` | middleware, routing | **No: moves** |
| Role records and indexes | `__sys:roles/…`, `__sys:role_idx/name/{t}/…` (`pkg/role/store.go:39-45`) | inside `__tiam:` or `{t}/` | `pkg/role/store.go` | RBAC `pkg/auth/rbac/rbac.go:720-749` | **No: moves** |
| User auth factors and lookup | `__sys:auth/{factors,users,lookup}/…` (`pkg/service/user_auth_factor_service.go:62-73`) | inside `__tiam:` or `{t}/` | routed factor service | `pkg/auth/login.go:102-150` | **No: moves** |
| Custodian factors and challenges | `__sys:authfactor/…` (`pkg/service/authfactor_service.go:30,46`) | inside `__tiam:` or `{t}/` | routed | routed | **No: moves** |
| Permission-scope overrides | `__sys:perm_scope/{perm}` (`pkg/permission/tenant_scope_store.go:31`) | resolver (not wired in `qrdbd`) | — | — | **No: moves** |
| Invitations | `invites/{id}`, `invite_redemptions/{id}` (`pkg/invite/store.go:24,27`) | resolver (wired by the SDK only, `sdk/go/types_invite.go:61`) | — | — | **No: moves** |
| DEK keyring | `__sys:dek/keyring` (`pkg/dek/keyring.go:75`) | inside `__tiam:` or `{t}/` | `pkg/dek` | `pkg/dek` | **No: moves** |
| Identity index | `system/__sys:identity_idx/tenant/{kind}/{id}` (`pkg/tenant/identity_index.go:52-67`) | SYS, plaintext | `identity_index.go:386` | `:500`, `OwningTenant` `:625-650` | **No: removed** |
| Tenant CA key (wrapped by the **writing node's** key) and CA certificate | `__sys:cert_ca/{t}/{wrapped_key,cert_pem}` (`pkg/cert/default_provider.go:48-54`) | SYS | `DefaultCertProvider.Initialize` | `loadCA` | Key moves; the public certificate stays (published) |
| Issued certificates | `__sys:certs/{t}/{serial}` (`default_provider.go:57,369`) | SYS | `GenerateClientCert` | `TenantForSerial` `:574-611` | **No: moves** |
| Revocation markers | `__sys:certs_revoked/{serial}` (`default_provider.go:60`) | SYS | `RevokeCert` | `pkg/auth/mtls.go:210` | Tenant-issued: **moves**. Node and platform certificates: stay. |
| Custodian records | `__sys:custodian…` (`pkg/custodian/service.go:36-44`) | SYS, **unconfined** root store (`cmd/qrdbd/start.go:3564`) | `custodian.Service` | the same | **No: moves** |
| SO PIN commitment | `__sys:so-pin-commit/{t}` (`pkg/auth/so_auth.go:41`) | SYS, unconfined (`start.go:3576`) | `SOAuthenticator` | the same | **No: moves** to the bootstrap record |
| Custodian groups | `custodian-groups/{id}` (`pkg/custodian/custodian_group.go:30`) | SYS, unconfined | custodian | custodian | **No: moves** |
| Submitted Shamir shares, **including share bytes** | `shares/{group}/{index}` (`pkg/custodian/share_service.go:32,90-130`) | SYS, unconfined | `ShareService` | the same | **No: never persisted** |
| Tenant officer roster | `ceremony/tenants/{id}/officers/` (`pkg/ceremony/barrier_scope.go:54-61`) | TB; **SYS before init** (`pkg/tenant/service.go:2045-2058`) | tenant ceremony | claim and unseal | **No: moves**, plus the unseal roster in the bootstrap record |
| Database catalog | `{dataDir}/databases/{name}/meta.json` (`pkg/database/manager.go:34,345`) | node-local, creating node only | `Manager.Create` | `rest_registry.go:274,373` | **No: moves** to the home store, replicated |
| Collection work journal | `__sys:collection-work/{t}/…` (`pkg/service/collection_types.go:78`) | SYS for prefix/barrier tenants | `collection_work_journal.go:65` | `collection_sweeper.go:358` | **No: moves** |
| Drop fence | `__sys:dropfence/{t}/{db}/` (`pkg/service/drop_fence.go:70`) | the data's group; SYS for shared tenants | `keyspace_reclaim.go:297` | `DropFenceGuard` | **No: moves** with the database store |
| Reclaim index | `__reclaim:index/{job}` (`pkg/service/reclaim_job.go:134`) | SYS | `TableService` | `ReclaimSweeper` | **No: moves** |
| Locks, lease owners, row locks, sequences | `__sys:lock/`, `__sys:lease-owner/{t}/{db}/`, `__sys:rowlock/`, `__seq:`/`__usq:` (`pkg/lock/types.go:23`; `pkg/server/namespaced_lease.go:31`; `pkg/rowlock/record.go:77`; `pkg/service/table_types.go:322`) | NS: SYS for prefix/barrier tenants | data plane | data plane | **No: moves** with the database store |
| Time series, index catalog | `ts:`, `idx:metric:`, `retention:` + `{t}/{db}/` (`pkg/timeseries/keys.go:40-53`); `idx:{name}` (`pkg/index/pebble_index.go:32`) | SYS for prefix/barrier tenants | data plane | data plane | **No: moves** with the database store |
| Vector namespaces | `{DataDir}/vector`, names `{t}/{db}/…` (`pkg/server/lifecycle/lifecycle.go:561-582`) | node-local, shared by all tenants | `VectorService` | the same | **No: moves** (databases binding design) |
| Encryption-migration records | `__sys:encmig/{t}/{db}[/nodes/{n}]` (`pkg/clustermig/store.go:44-52`) | SYS | `clustermig.Store` | the same | Tenant scope: **moves**. System scope: stays. |
| Audit events | `{dataDir}/audit-events.ndjson` (`cmd/qrdbd/start.go:1628-1637`) | node-local, every tenant in one file | `ChainFileLogger` | `Query` `pkg/audit/chain_file_logger.go:573` | Tenant events: **move** to the tenant's trail. Platform events: stay. |
| Rate-limit buckets | key `tenant\0subject\0perm` (`pkg/auth/middleware/ratelimit.go:60`) | node memory (not wired in `qrdbd`) | limiter | limiter | Node memory; stays |
| Metrics | `qrdb_vector_*{tenant,namespace}` (`pkg/service/vector.go:311-323`) | scrape | — | `/metrics` (authentication only, `pkg/api/rest/server.go:1521`) | Namespace label removed |
| Tenant key delivery | HPKE envelope with root key or sealed blob (`pkg/clusterkeys/sender.go:248-313`; import `pkg/seal/import_key.go:136-305`) | in transit; re-wrapped into TB | `DistributeTenantKey` (`pkg/service/database_service.go:1479`, `:388-405`) | `receiver.go:180-291` | **Removed.** Verifier delivery replaces it. |

### Defects found by the inventory (independent of the policy)

These break rules that are already owner rulings ("no cross-tenant database
access for anyone"; "role bindings name the holder's own tenant"). They are fixed
whatever the isolation policy: items 1, 3, 4 and 5 in phase 1 of the
[implementation plan](#implementation-plan), item 6 in phase 2, and item 2 in
phase 5, by removing the system lift it rides on.

1. **The etcd listener admits the system tenant into any tenant's
   database.** `pkg/server/server_etcd.go:193` accepts a caller whose tenant is
   the owner **or** `system`, then authorizes `kv:*`, which is ScopeSelf and
   lifted for system identities, against the owner.
2. **A system-tenant identity can mint a client certificate for another
   tenant's user.**
   - `certs:generate` is authorized against the body's tenant
     (`pkg/auth/middleware/cert_authz.go:62`) under ScopeSelf, which lifts the
     system tenant.
   - The minted certificate's OU selects the tenant
     (`pkg/cert/default_provider.go:322-326`).
   - An account with no SPKI binding is not bound under the default posture
     (`pkg/auth/middleware/cert_binding.go:77-88`).
3. **Audit query is unconfined.**
   - Every transport takes `tenant_id` from the request: REST
     `pkg/api/rest/handlers_audit.go:66`, gRPC `audit_server.go:58`, QUIC
     `audit_frame.go:31`, MCP `audit_tool.go:81`.
   - Nothing checks it against the caller, so a tenant Admin holding
     `audit:query` reads every tenant's events on that node.
4. **Submitted Shamir share bytes are persisted in the replicated store**
   (`pkg/custodian/share_service.go:90-130`), outside every barrier.
5. **System-tenant users may hold role bindings in any tenant**
   (`pkg/user/cert_binding.go:46-61`). This contradicts the ruling that a
   binding names the holder's own tenant.
6. **Shard ids are allocated per node** (`pkg/shard/allocator.go:23-52`,
   `cmd/qrdbd/start.go:1130`). Nothing checks uniqueness across the cluster.
   A tenant home group per tenant multiplies the groups that need ids, so
   cluster-wide allocation is a prerequisite.

### Invariants

- **The residue list is closed.** The only keys in the system store that are
  derived from a strict tenant's id, or carry its records, are the ones listed
  in [architecture.md](architecture.md#what-stays-in-the-system-tenant-and-why).
  The residue test below enforces it.
- **No tenant key crosses a node boundary.**
  - Nothing exports a tenant root key, a replica key, or a sealed blob meant
    for import on another node.
  - `StorageBarrier.ExportRootKeyContext`, `ImportRootKey`,
    `ImportSealedBlob` and the tenant-key modes of `clusterkeys` are deleted.
- **A replica key is born on its node, under a credential presented to that
  node.** A node that has never met a valid credential for the tenant holds no
  key for it, and it reports `sealed`.
- **Credentials and shares travel only from the holder's client to the
  node.**
  - No node relays a passphrase, share, unseal key or replica key to another
    node.
  - The only tenant-seal artefact `clusterkeys` carries is the credential
    verifier, and only to a node in the tenant's serving set.
- **The serving set contains every database group of the tenant.**
  - Placement refuses a database group with a replica outside the set, with
    `placement.ReplicaOutsideServingSetError`.
  - Growing the serving set is the tenant-scale verb.
- **A node outside the serving set never terminates a strict tenant's
  connection, when it can avoid it.**
  - A connection whose SNI names a strict tenant this node does not serve is
    relayed unterminated, inside a node-mTLS tunnel, to a serving node.
  - A connection it had to terminate (no tenant SNI, or Unix or embedded)
    and whose certificate names such a tenant is refused with
    `tenant.TenantNotServedHereError`, before any user lookup.
  - The relaying node never looks the user up, never forwards an on-behalf
    assertion, and never logs an authorization decision about a tenant user.
    It logs only the relay itself to the platform trail: tenant, client
    address, serving node.
- **The serving node binds SNI to the certificate.** On a relayed or a direct
  connection, a certificate that names a tenant other than the SNI tenant is
  refused with `tenant.TenantSNIMismatchError`.
- **A strict tenant's users authenticate only with certificates chaining to
  that tenant's own CA.** A cluster-CA certificate naming the tenant is
  refused. The SPKI binding policy is `required` for strict tenants, whatever
  `auth.mtls_binding_policy` says for the rest.
- **The policies only tighten. Shipped 2026-10-06**
  (`pkg/tenant/isolation_policy.go`).
  - The cluster floor and the tenant setting are each written with a
    compare-and-swap (`setClusterIsolationPolicy`, composed of
    `PutIfAbsent`/`CompareAndDelete` over `clusterPolicyBackend`;
    `checkIsolationPolicyChangeFor` for the tenant setting).
  - The floor permits only `unset → strict | operator-access` and
    `operator-access → strict` (`clusterIsolationTransitionAllowed`).
  - The tenant setting permits only `inherit → strict`.
  - Not yet true: the floor's "unset" read defaults to
    `ClusterIsolationPolicyOperatorAccess` (today's behavior) rather than to
    `strict`, because nothing yet performs the "first-boot configuration,
    recorded once" write this table's own row promises; see the slice
    correction at the top of this section.
- **Strict tenants have storage of their own. Shipped 2026-10-06**
  (`tenant.ValidateIsolationModeAgainstPolicy`). Creating a `prefix` tenant
  under a strict floor, or setting `strict` on a `prefix` tenant, is refused
  with `tenant.StrictRequiresDedicatedStorageError` before any write, in both
  `TenantService.CreateTenant`/`UpdateTenant` and the no-seal
  `kvTenantService`'s.
- **`ScopeSelf` has no system lift. Shipped 2026-10-06, and unconditionally —
  not gated by the isolation policy at all ("under every policy" in the
  ruling).**
  - The `inSystem ||` arm in `rbac.Authorize`'s `ScopeSelf` case
    (`pkg/auth/rbac/rbac.go`) is deleted. `ScopeSelfStrict` is NOT yet folded
    into `ScopeSelf` — they are now behaviorally identical, but remain
    distinct enum values pending that follow-on cleanup.
  - `tenant.EffectiveTenant` (`pkg/tenant/context.go`) lost its system-tenant
    branch the same way: every identity, including the system tenant, may
    only act on its own tenant through it. This is the layer that actually
    gated `CreateUser`/`CreateRole`/factor-enrolment routing, independent of
    the RBAC entry check.
  - Cross-tenant reach exists only through `ScopeCross` permissions. They act
    on system records or, for `tenant_identity:*` against a tenant whose
    effective policy is `operator-access`, on identity records in an
    explicitly named tenant. **Not yet shipped:** `tenant_identity:read`/
    `write` themselves — nothing wires an operator-access reach surface yet,
    so under every floor value, cross-tenant identity reach is simply absent
    rather than gated.
  - `tenants:read:cross`, `tenants:delete:cross` and `tenants:update:cross`
    (`pkg/permission/cross_scope.go`) replace the lift `tenants:read` /
    `tenants:delete` / `tenants:update` relied on; `tenants:update` itself
    stays self-scoped (see its own row in [Tenant-administration
    authorization](#tenant-administration-authorization) above), but renaming
    another tenant is ordinary platform administration like reading or
    deleting it, so `AuthMiddleware.AuthorizeTenantTarget`
    (`pkg/auth/middleware/middleware.go`) tries the self-scope permission
    first and falls back to the matching cross one only when the target
    differs from the caller's own tenant — a patch that both renames and
    re-isolates a DIFFERENT tenant is admitted for a system caller holding
    `tenants:update:cross` and `tenants:set_isolation`, each authorized on its
    own merits.
- **Three production regressions the `ScopeSelf` lift removal exposed,
  surfaced only by running the real integration suite end-to-end (unit tests
  construct already-correctly-bound contexts by hand and could not have
  caught any of these), fixed in the same change:**
  - `pkg/bootstrap.ProvisionTenant` seeded a new tenant's built-in roles and
    created its first admin using the CALLER's ctx (the system admin's),
    targeting the brand-new tenant — refused the instant the lift was gone,
    breaking `tenant create` on every transport. Fixed by rebinding
    `ctx = tenant.WithTenantID(ctx, cfg.TenantID)` once, near the top of
    `ProvisionTenant`; this is legitimately privileged (`ProvisionTenant` is
    reached only through the `ScopeCross` `tenants:create` gate), so the
    identity in ctx (a separate key) is untouched and audit trails still name
    the real caller.
  - `pkg/auth/middleware.finalizeIdentity` bound ctx's tenant to the
    authenticating identity's own (cert-derived) tenant AFTER calling
    `m.lookupUser` (the routed `GetUserByUsername`) — but that lookup itself
    now needs ctx already bound to match, so EVERY non-system-tenant mTLS
    authentication failed closed. Fixed by moving the bind to immediately
    after `identity.TenantID` normalization, before any lookup.
  - A system-managed (auto-unseal) dedicated-isolation tenant's pending admin
    (`pkg/tenantadmin`) was never completed: `registerDedicatedBarrier`'s
    auto-unseal branch initializes the barrier directly through the registry
    and never calls `notifyBarrierReady`, so nothing ever raises
    `tenant.BarrierReadyHook` for it — unlike a self-managed tenant, whose own
    admin later calls barrier-init/-unseal/-status to trigger it. Fixed in
    `ProvisionTenant`, not `registerDedicatedBarrier`: the pending-admin
    record `provisionPendingAdmin` writes does not exist yet at the point
    `CreateTenant` runs (Step 1, before the pending-admin step), so the hook
    would have fired too early even if wired there. `ProvisionTenant` now
    calls `CompletePendingTenantAdmin` inline immediately afterward, gated on
    `CreateTenant`'s returned `*tenant.TenantInfo.BarrierAutoUnseal` so a
    self-managed tenant's (or an idempotent re-run's) deferred semantics are
    unchanged.
- **No permission reaches tenant data across tenants.** Every data-plane
  permission is `ScopeSelf` or `ScopeTenantOnly`, never `ScopeCross`. That
  covers KV, MVCC, SQL, collections, vectors, time series, locks, leases,
  watch, etcd, sequences, transactions, indexes and tenant-group backups.
  **Not yet shipped:** the table-driven test over the permission registry
  that asserts it; today's assertion is indirect, via the `ScopeSelf`
  lift-removal tests in `pkg/auth/rbac` and the routing-layer
  `CrossTenantDeniedError` tests in `pkg/user`, `pkg/role` and `pkg/service`.
- **Operator access is recorded twice.** A `tenant_identity:*` decision is
  written to the platform trail and to the tenant's own trail before the
  operation runs. If either write fails, the decision is refused, whatever the
  audit failure policy says.
- **The first admin is bound to the creation CSR's key.**
  - A self-managed tenant's init is accepted only with a fresh signature,
    bound to the TLS exporter, by the key whose SPKI the pending first admin
    records.
  - No certificate, role or operator permission substitutes for that proof.
- **The tenant bootstrap record is MAC-bound to the replica key.** It is
  verified on every unseal against the home store's copy, and a mismatch
  re-seals the tenant on that node.

### Data model

`TenantInfo`, the tenant's record in the system tenant, after the change:

```go
type TenantInfo struct {
	ID, Name        string
	IsolationType   IsolationType
	BarrierMode     BarrierMode            // self-managed | system-managed
	IsolationPolicy TenantIsolationPolicy  // inherit | strict; tighten-only
	Lifecycle       TenantLifecycle        // provisioning | active | deleting
	Home            HomeGroup              // ShardID, Replicas []NodeRef; empty for prefix
	Quotas          quota.PlatformQuotas   // databases, raft groups, replicas, bytes, backups
	Entitlements    quota.Entitlements
	PendingAdmin    *PendingFirstAdmin     // {Username, SPKIThumbprint}; self-managed, until init
	CreatedAt       time.Time
}
```

- **Limits and counters are split.** The identity-object limits stay in the
  plan, because the platform sets them: `MaxUsers`, `MaxRoles`,
  `MaxCertificates`, `MaxCollections`, `MaxKeys`, `MaxTSMetrics` and
  `MaxVectorDims`. Their counters live in the home store, and the home group
  enforces them.
- **The home store's keyspace** is the identity layout that exists today,
  minus three tenancy encodings: the `__tiam:` wrapping, the `{tenant}/`
  wrapping, and the tenant segment embedded in `__sys:users/tenant/{tenant}/…`.
  The store is now the tenant.
- **Placement is keyed `__sys:placement/{tenant}/{dbid}`**, where `dbid` is an
  opaque, never-reused id allocated by the home group. The name ↔ id mapping
  lives in the tenant's catalog.

### Error semantics

| Condition | Error | REST | gRPC / Unix / QUIC / MCP | Retry? |
|---|---|---|---|---|
| A terminated connection names a strict tenant this node does not serve (Unix, embedded, or no tenant SNI) | `*tenant.TenantNotServedHereError{TenantID, ServingNodes}` | 421 Misdirected Request + `X-QRDB-Serving-Nodes` | `UNAVAILABLE`, `ErrorInfo` reason `TENANT_NOT_SERVED_HERE`, with the node list | yes, with tenant SNI or on a serving node |
| Certificate tenant differs from the SNI tenant | `*tenant.TenantSNIMismatchError` | TLS alert / 421 | `UNAUTHENTICATED` | no |
| The relay cannot reach any serving node | `*tenant.TenantRelayUnavailableError{TenantID}` | connection closed with a TLS alert; logged on the relay | the same | yes |
| The home group has no quorum, or the tenant is sealed on a quorum | `*tenant.TenantHomeUnavailableError` (wraps `ErrTenantSealed` when sealed) | 503 | `UNAVAILABLE` | yes |
| A system identity acts in another tenant on a non-cross permission | `*rbac.PermissionDeniedError` | 403 | `PERMISSION_DENIED` | no |
| `tenant_identity:*` against a strict tenant | `*tenant.IsolationPolicyError{Policy: strict}` | 403 | `PERMISSION_DENIED` | no |
| Loosening the floor or a tenant's policy | `*tenant.IsolationPolicyChangeError{From, To}` | 409 | `FAILED_PRECONDITION` | no |
| A `prefix` tenant under a strict floor, or asking for strict | `*tenant.StrictRequiresDedicatedStorageError` | 409 | `FAILED_PRECONDITION` | no |
| Create without `first_admin`, or with an unparseable CSR | `*tenant.FirstAdminRequiredError` / `*tenant.InvalidCSRError` | 400 | `INVALID_ARGUMENT` | no |
| Init proof of possession absent, stale or by the wrong key | `*tenant.FirstAdminProofError` | 401 | `UNAUTHENTICATED` | with a fresh nonce |
| Credential does not match this node's verifier | `*seal.CredentialVerifierError` | 403 | `PERMISSION_DENIED` | no |
| Bootstrap record does not match the home store after unseal | `*seal.BootstrapRecordMismatchError`; tenant re-sealed on the node | 503 | `UNAVAILABLE` | after operator action |
| Database replica outside the serving set | `*placement.ReplicaOutsideServingSetError` | 409 | `FAILED_PRECONDITION` | no |
| `strict-isolation` not yet activated | `*featuregate.FeatureNotActiveError` | 409 | `FAILED_PRECONDITION` | after rollout |
| A shard's dedicated WAL cannot be opened (G2) | `*server.TenantWALUnavailableError{TenantID, ShardID}`; the shard does not start | 503 | `UNAVAILABLE` | after operator action |
| A strict tenant's database requested without a dedicated WAL (G1) | `*database.DedicatedWALRequiredError` | 409 | `FAILED_PRECONDITION` | no |

Every row goes into `pkg/api/errclass` once. No transport maps them itself.

### Implementation plan

**What every phase satisfies:**
- It merges on its own and leaves `main` green: `make test`, `make vet-tags`,
  `make check-publication-bound`, and coverage within 1.0pp of baseline.
- It opens with a **red test**: written first, run against the unfixed tree,
  and failing for the reason the phase exists. The test goes green in the same
  phase.
- It updates this folder, and every other feature folder it touches, in the
  same commit.
- Its user-visible change ships on all six transports and the CLI together.

Cluster-wide behaviour changes ride the `strict-isolation` feature gate from
phase 5 on.

| Phase | Delivers | Red test (fails before, passes after) | Depends on |
|---|---|---|---|
| **0. Cluster tenancy suite as a merge gate** | A `make integration-test-cluster-tenancy` target that runs the multi-node tenant suites (`isolation_raft_*`, and every strict suite that follows) as a **per-push** CI job listed in the CI result gate. The suites' CI-load timeouts are fixed, by measurement, before the job turns on. The full cluster stage stays nightly. | `test/buildgates/cluster_tenancy_gate_test.go` parses `ci.yml` and asserts that the job exists, runs that target, and is a `needs:` of the result gate. A discovery floor asserts that the target selects at least N tests. Red today: the cluster stage is nightly only. | — |
| **1. Standing-rule defects** | (a) The etcd listener admits owner-tenant callers only (`pkg/server/server_etcd.go:193`). (b) Audit query is confined to the caller's tenant on six transports; `audit:read:cross` reads the platform trail. (c) Shamir share bytes are no longer persisted (`pkg/custodian/share_service.go`). (d) Role bindings name the holder's own tenant, for every holder (`pkg/user/cert_binding.go:46-61`). **All four are already being fixed by changes in flight**, as is cross-tenant certificate minting. This phase is complete once those changes merge, and each carries its own red test. | (a) A system identity holding `kv:range` reads a tenant database over etcd; it must get `PERMISSION_DENIED`. (b) A tenant Admin passes `tenant_id=<other>` on each transport; it must get only its own events. (c) After a share submit, a scan of the system store must find no share bytes. (d) A system user's binding in tenant `acme` must fail with `RoleBindingTenantError`. | 0 |
| **2. Cluster-wide shard ids** | The shard allocator claims ids through a guarded write on a replicated high-water mark in the system shard (`pkg/shard/allocator.go`), replacing the per-node counter. | Two nodes that allocate concurrently today receive the same shard id; they must receive distinct ids. | 0 |
| **3. First admin by CSR (S7)** | **Already shipped, 2026-10-03, outside this plan** — `admin_username` plus `admin_csr_pem`/`admin_cert_pem` is required at create, on six transports and the CLI (not the `first_admin {username, csr_pem}` shape this row originally proposed). System-managed and `prefix` tenants get their admin user, SPKI binding and certificate at create; self-managed tenants get the pending first admin, consumed at barrier init. Trust-on-first-init by "a certificate naming the tenant" is removed. Records still land in today's stores; phase 8 moves them. What remains for this phase: the `init`/`begin`/`complete` proof-of-possession protocol bound to the TLS exporter, needed once initialization fans out to a serving set with no tenant client certificate yet. | A certificate the system operator mints with OU=`acme` initializes `acme`'s barrier today; it must get `FirstAdminProofError`. The CSR key holder initializes, and its SPKI is reported in `barrier-status`. | 1 |
| **4. Dedicated tenant WAL, fail-closed (S4: G2, G3, G4, G6)** | The existing dedicated-WAL mechanism (`pkg/database/multiplex_logdb.go`, go-quicraft per-shard barriers) becomes a dependable guarantee. **G2:** a database whose placement or catalog says `DedicatedWAL` refuses to start when its WAL cannot be opened, with `TenantWALUnavailableError`; the silent fall-back to the shared WAL (`pkg/server/raft_backend_factory.go:904`) is deleted. **G3:** the multiplex is wired on single-node hosts and on the SDK node, so the guarantee holds in every run mode. **G4:** sealing a tenant stops and unregisters its shards from the host and the multiplex **before** its barrier seals, so no apply or commit batch meets a sealed WAL. **G6:** a shard that failed is evicted from the host and restarted when the tenant is unsealed. | **G2:** a dedicated WAL whose directory cannot be opened falls back to the shared WAL today and the shard starts; it must refuse. **G4:** sealing a tenant with a running dedicated-WAL shard leaves the shard registered today, and its next proposal meets a sealed barrier; after the change the shard is gone before the barrier seals, and a concurrent proposal on another tenant's shard in the same batch commits. **G6:** a shard failed by a sealed barrier stays latched after unseal today; it must serve again after unseal. **G3:** a single-node host and an SDK node refuse or ignore `DedicatedWAL` today; they must write the shard's log under the tenant key. | 0 |
| **5. Isolation policy and RBAC (S4: G1 for databases)** | Register the `strict-isolation` feature. Add the floor and the tenant policy (get, set, tighten-only) on six transports and the CLI. Remove the `ScopeSelf` system lift and `EffectiveTenant`'s system branch. Add `tenants:read:cross`, `tenants:delete:cross`, and `tenant_identity:read`/`write` with tenant-qualified operator verbs and double audit. Enforce the S2 refusal. **G1:** every database of a strict tenant is created with a dedicated WAL, and a request to create one without it is refused. A strict tenant never has a log under a node key. Before activation, behaviour is unchanged and the new verbs return `FeatureNotActiveError`. | Table test over every `ScopeSelf` permission: a system identity in tenant `acme` is admitted today and must be refused. That includes `certs:generate` for another tenant's OU, where the in-flight fix has not already removed it, and `users:read` by id. Under `operator-access`, `tenant_identity:read` succeeds and appears in both trails. Loosening the policy returns `IsolationPolicyChangeError`, and a `prefix` create under a strict floor returns `StrictRequiresDedicatedStorageError`. **G1:** a strict tenant's raft database is created today with its log on the shared WAL; it must be created on a dedicated WAL, and the system-shard LogDB must hold no entry of its shard. | 3, 4 |
| **6. Tenant home group and replicated catalog (S4: G1 for the home group)** | A home group per `raft` and `barrier` tenant, placed by the scheduler, started by the reconciler, and gated on the serving set. **Its log is on a dedicated WAL from its first entry (G1).** The database catalog moves into the home store. Placement is keyed by opaque database id (S5). Database groups are confined to the serving set. | A database created through node A is unknown to node B's catalog today; it must resolve on every serving node. A scan of the system shard finds the database name in placement keys today; it must find only the opaque id. The shared host LogDB must hold no home-group entry. | 2, 5 |
| **7. Per-replica keys (S4: G10)** | Delete tenant-key transport. Client fan-out for init, unseal and rekey. Verifier delivery through `clusterkeys`. Per-node tenant bootstrap records. Each node's dedicated WALs are keyed by **its own** replica key. Unseal is per node, against the cluster seal record. | Two replicas of one tenant have the same key fingerprint today (the fingerprint is exposed under a test build tag); they must differ. One replica's key must fail to open another replica's store and WAL. A Raft snapshot catch-up across distinct keys must succeed. | 6, cluster-wide sealing record |
| **8. Move the records; delete the shared store paths** | Identity, factors, invites, permission scopes, the DEK keyring, the tenant CA and issued certificates, custodian records and groups, and the officer roster move into the home store. The SO PIN commitment and the unseal roster go to the bootstrap record. The CA chain is pinned per tenant, and the binding policy is `required` for strict tenants. `__tiam:`, `tenant.IdentityIndex`, `OwningTenant` and the resolver's shared-store branch are deleted. | **The residue test** (below) fails today on usernames, emails and thumbprints in `__tiam:` key names, on `__sys:cert_ca/{t}` and `__sys:custodian…`, and on the identity index; it must pass. A cluster-CA certificate naming a strict tenant authenticates today; it must be refused. | 7 |
| **9. Layer-4 passthrough (S6)** | SNI routing and the relay tunnel for REST, gRPC, and the etcd and SQL listeners over TCP; QUIC by Initial-packet SNI and connection id. `TenantNotServedHereError` for Unix and embedded. The SDK and CLI set SNI. On-behalf forwarding is refused for strict tenants. **The routing input (SNI) awaits the owner's confirmation.** | A strict tenant's client that connects to a non-serving node is refused as sealed today; it must be served through the relay. On every transport, the serving node must observe the client's own certificate, the relaying node's TLS-termination counter for the connection must be zero, and an SNI/certificate mismatch must be refused. | 8, owner confirmation |
| **10. Tenant audit trail and metrics (S9)** | A node-local, hash-chained audit trail per tenant, under the tenant barrier, on each serving node. Tenant events go there; platform events go to the platform trail. The `namespace` label is removed from `qrdb_vector_*`. | A tenant event lands in the shared node file today; it must land only in the tenant's trail. The platform trail must hold no tenant username, and a `/metrics` scrape must show no tenant data name. | 8 |
| **11. Tenant-only backups (S10, fallback A)** | `backup:tenant` (`ScopeTenantOnly`) for a strict tenant's groups. Platform shard backup is limited to the system shard. | A platform `backup:*` of a strict tenant's group succeeds today; it must be refused. The tenant's own backup must succeed and restore. | 6 |
| **12. Tenant WAL key rotation (S4: G7)** | Rotation of a dedicated WAL's key epochs, with per-barrier purge floors so that rotating or shredding one tenant never affects another. **A later phase**, as ruled. | A rotation on tenant A must leave tenant B's WAL readable, and A's pre-rotation epochs must be purged once no live entry needs them. | 7 |

**External dependency — G5 (go-quicraft).** In go-quicraft, one sealed WAL in a
commit batch fails the whole batch. The defect is raised with go-quicraft. Until
the fix is released, the G4 ordering (stop and unregister a tenant's shards
before its barrier seals) is what keeps a sealed tenant WAL out of any batch.
Phase 4's G4 red test exercises exactly that interleaving. When the fix
lands, the dependency bump carries a regression test of the batch behaviour
itself.

**Order and cut points.**
- **After phase 1**, the owner's standing rules hold: no cross-tenant
  database access, and bindings name the holder's own tenant only.
- **After phase 5**, the platform operator can no longer reach into a tenant
  through any API, and no strict tenant has a log under a node key.
- **After phase 8**, a strict tenant's records are out of the system tenant.
- **After phase 9**, every transport reaches a strict tenant from any node
  without that node terminating the session.

Each cut point is a usable release.

### The residue test

`test/integration/tenant_residue_test.go` is the gate that makes the residue
list enforceable rather than descriptive.

1. Boot a three-node cluster under `strict`, and create one tenant of each
   isolation tier.
2. Exercise each tenant end to end: users, roles, factors, certificates,
   invites, databases, collections, locks, sequences, time series, vectors and
   a seal and unseal.
3. Scan **every key** in the system shard on every node, and every
   node-local system store.
4. Fail on any key that contains a tenant id or a tenant-chosen name (a
   username, role name or database name the test chose to be unique) unless
   its prefix is in the residue allowlist.
5. Fail on any **value** that contains one of those unique strings outside
   the allowlist.

The allowlist is a Go table beside the test, and each entry carries the reason
from architecture.md. Discovery floors assert that the scan saw at least N keys
and at least one key from each allowed prefix, because an empty scan reports
success.

### Test plan

Each phase's red test is in the plan above. In addition:

**Unit (in memory, non-blocking):**
- **The scope rule.** A system identity is refused every `ScopeSelf`
  permission in another tenant, under both policies, and so is
  `EffectiveTenant`.
- **The permission registry.** No data-plane permission is `ScopeCross`. This
  is a table test over `permission.All()`.
- **Policy transitions.** The full transition tables for the floor and the
  tenant setting, including compare-and-swap races.
- **The serving-set gate.** A terminated connection for a strict tenant on a
  non-member node returns `TenantNotServedHereError` before any user lookup.
  The user-store double **fails the test if it is called**: a faithful double
  for "must not be reached".
- **SNI parsing.** ClientHello and QUIC Initial SNI parsing is tested against
  recorded handshakes, including malformed, fragmented and absent SNI.
- **Init proof of possession.** The signature must cover the nonce and the TLS
  exporter. A replay on another connection fails, and so does a signature by
  any other key.
- **The bootstrap record.** The MAC binds the record, and a tampered roster
  re-seals the tenant.
- **Credential verifiers.** A wrong passphrase on a first-contact node is
  refused before any key is sealed.

**Integration (`test/integration/`, cluster stage, per-push from phase 0; CI's full tier since 2026-10-12):**
- **Distinct replica keys.** Every replica's key is distinct, and one
  replica's key does not open another's store.
- **Snapshot catch-up** works across distinct keys.
- **Restart.** A node restarts sealed. A client fan-out unseal restores the
  serving set, and a quorum restores service.
- **Scale-up.** A new serving node starts sealed, receives the verifier, and
  unseals at the next fan-out.
- **Passthrough.** For REST, gRPC, QUIC, and the etcd and SQL listeners, a
  client reaching a non-serving node is served through the relay. Unix and
  embedded on a non-member node return the typed error.
- **Operator refused under strict.** On every transport, a system Admin is
  refused a strict tenant's users, roles, factors, certificate minting, audit
  and data.
- **Operator access under `operator-access`.** On every transport, a role
  carrying `tenant_identity:read` reads a named tenant's users. The access
  appears in both trails, and the same role is refused every data-plane verb.
- **Opt-in on an `operator-access` cluster.** A tenant that opts into strict
  refuses `tenant_identity:read` from then on.
- **Residue.** The residue test above.
