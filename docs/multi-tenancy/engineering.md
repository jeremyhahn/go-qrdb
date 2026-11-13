# Multi-Tenancy — Engineering Contract

> **Status: NORMATIVE** (the strict-isolation section is RULED, partly built;
> it names what is not built) · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

## Exported surface

`sdk/go`:

```go
func (c *Client) Tenants() transport.TenantClient
func (c *Client) WithTenant(tenantID string) (transport.KVClient, error)
```

`TenantBarrierService` is a **dedicated gRPC service**
(`qrdb.v1.TenantBarrierService`) with `InitBegin`, `InitComplete`, `Unseal`,
`Seal`, `Status`, `OfficerRegister`, `OfficerList`, `QuorumBegin`,
`QuorumSubmit`, `RekeyStart`, `OfficerRevoke`, `ShareClaimBegin`,
`ShareClaimComplete`.

Server option: `server.WithTenantService(svc tenant.TenantServicer)`. gRPC
registers `TenantBarrierService` when a tenant servicer is set; the Unix
socket server takes it through `unix.WithTenantBarrierService`. A composer
that builds its own `*grpc.Server` registers it through the SDK's
`RegisterGRPCService` with `WithRegisterTenantBarrierService(svc)`, which
registers `qrdb.v1.TenantBarrierService` beside `QRDBService`; its
tenant-scoped gate is the middleware passed with `WithRegisterAuthMiddleware`,
and without one the service runs in the no-auth posture. The Unix socket server
registers it the same way, through that one option.

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
	VolumePool        string // tenant.WithVolumePool; empty = the default pool
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

Before any write, `OnboardTenant` calls
`tenant.ValidateTenantIDTrustDomain(req.ID, clusterTrustDomain)`, which
returns a `*tenant.TenantTrustDomainError` for an ID that
`spiffe.ValidateTrustDomain` refuses (`Err` is the `*spiffe.TrustDomainError`)
or that equals the cluster trust domain (`Err` is
`tenant.ErrTenantIDIsClusterTrustDomain`). `pkg/api/errclass` classifies it as
invalid input: 400 on REST, `InvalidArgument` on gRPC, and the same class on
every other transport. The check lives in the service, not a transport, so every
transport and the CLI refuse the same IDs;
`TestTenant_CLI_TrustDomainRefusal_PerProtocol` drives the refusal through the
CLI on each.

`OnboardTenant` calls `bootstrap.ProvisionTenant` with
`AdminEnrollment: AdminEnrollCert`, `Exclusive: true` and
`Signer: &bootstrap.KeyBSigner{Certs: certProvider}`. The tenant's own Key B
CA signs a `prefix` tenant's admin CSR (`cert.CertProvider.IssueClientCert`,
through `certenroll.IssueFromCSR`). It answers with
`TenantProvisionResult.Tenant`, the record the provisioning committed, and
does not read the tenant back: once `ProvisionTenant` returns, the tenant
exists and the admin's certificate has been issued, so a read that failed
afterwards (a linearizable read timing out on a loaded cluster) would report a
created tenant as a failure and lose that one-time certificate, and the retry
would be refused as a duplicate because the provisioning is exclusive.

A pending admin's CSR (`tenant.IAMBehindTenantBarrier`) goes to
`TenantOnboardingDeps.PendingAdminSigner`, which is forwarded as
`bootstrap.Dependencies.PendingAdminSigner`. Key B cannot sign it, because a
self-managed tenant's CA and certificate records are behind a barrier that is
sealed until that admin initializes it. qrdb's node lifecycle wires
`ceremony.AdminCSRSigner` over the cluster CA (Key A), the same signer as
system first boot (CN = username, OU = [tenant, `administrator`], the
tenant's SPIFFE ID as URI SAN, client auth). The mTLS authenticator reads the tenant from the first OU, and a
cluster-CA issuer is accepted for any tenant. The first-init gate compares the
pending record's thumbprint and username. A node with no cluster CA
(bring-your-own TLS) leaves the field nil. `OnboardTenant` then passes
`Signer` as the pending signer for a system-managed barrier, whose Key B
serves from creation. For a self-managed one it passes nil, and
`ProvisionTenant` refuses the CSR before anything is written, with
`*ceremony.AdminCredentialRequiresCAError` as system first boot does
(`FailedPrecondition` on every transport). A composer
holding the CA key elsewhere (an HSM, a remote CA) injects its own
`firstboot.CertSigner`. An undone exclusive provisioning revokes the
certificate through the signer that signed it, when that signer has
`RevokeCert` (`KeyBSigner` does).

```go
// ceremony
func NewAdminCSRSigner(ca CeremonyCA, trustDomain string) (*AdminCSRSigner, error) // nil ca → *AdminCredentialRequiresCAError
func (s *AdminCSRSigner) SignAdminCSR(ctx context.Context, tenantID, subject string, csrPEM []byte) ([]byte, error)
```

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

// TenantProvisionResult carries TenantID, Tenant (the record CreateTenant
// committed; nil on a re-run), AdminUserID, AdminUsername, AdminCertPEM,
// AdminCertSerial, AdminCertThumbprint, AdminAwaitsBarrierInit,
// AdminCompletionDue, AdminRoleID and AdminPending. It never carries a
// private key.
func ProvisionTenant(ctx context.Context, cfg TenantProvisionConfig, deps Dependencies) (*TenantProvisionResult, error)

// CompletePendingTenantAdmin creates a pending admin's account once the
// tenant's identity records can be written; idempotent. With
// deps.HomeLeader set it writes only where LeadsHome(tenantID) holds, and
// returns *AdminCompletionDeferredError elsewhere.
type HomeLeader interface{ LeadsHome(tenantID string) bool }
func CompletePendingTenantAdmin(ctx context.Context, tenantID string, deps Dependencies) (bool, error)

// CompletePendingTenantAdminWithin runs CompletePendingTenantAdmin with each
// attempt bounded, retrying with backoff under an overall budget; it stops on
// tenant.ErrTenantSealed and on permanent errors. Zero bounds take defaults
// (2m budget, 10s attempt, 250ms to 5s backoff, clock.Real()).
type AdminCompletionBounds struct {
	Budget, AttemptTimeout, Delay, MaxDelay time.Duration
	Clock                                   clock.Clock
}
func CompletePendingTenantAdminWithin(ctx context.Context, tenantID string, deps Dependencies, b AdminCompletionBounds) (bool, error)

// CompletePendingTenantAdminOnce makes one CompletePendingTenantAdmin attempt
// on ctx, within b.AttemptTimeout, for a request that must not wait for the
// tenant's home group.
func CompletePendingTenantAdminOnce(ctx context.Context, tenantID string, deps Dependencies, b AdminCompletionBounds) (bool, error)
```

When `AdminCompletionDue` is set -- a system-managed barrier tenant, created
now -- `OnboardTenant` writes the success audit record (on its own detached
5s bound), answers with `result.Tenant`, and completes the admin in a
goroutine through `CompletePendingTenantAdminWithin` on a context detached
from the request. It reads nothing back after the commit.

`Dependencies` carries, for tenant provisioning,
`TrustAnchors trustanchor.Registry`, `PendingAdmins tenantadmin.Store`,
`PendingAdminSigner firstboot.CertSigner`, `BarrierAnchors FirstAdminPinner`,
`TrustDomain string`, `Homes HomeEnsurer`, `HomeLeader HomeLeader` and
`Now func() time.Time`.

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
	Create(ctx context.Context, tenantID string, p *Pending) error // never replaces a record
	Get(ctx context.Context, tenantID string) (*Pending, error) // (nil, nil) when none
	// Delete removes the record only while it is still the one read;
	// (false, nil) when it is gone or has been replaced.
	Delete(ctx context.Context, tenantID string, read *Pending) (deleted bool, err error)
}

func NewKVStore(store kvstore.KVStore) *KVStore
```

`pkg/tenant`: `WithBarrierReadyHook(h BarrierReadyHook) TenantServiceOption`,
where:

```go
type BarrierReadyHook func(ctx context.Context, tenantID string, mode AdminCompletion) error

type AdminCompletion int

const (
	AdminCompletionWithin AdminCompletion = iota // retry within the hook's own bounds
	AdminCompletionOnce                          // one attempt on the caller's context
)

var ErrTenantAdminPending = errors.New("tenant: first admin pending: the tenant's home group cannot complete it yet")
```

The service calls it inside the request, before answering.
`TenantBarrierInitWithStrategy` calls it in `AdminCompletionWithin` mode and
fails with the hook's error, the barrier staying initialized. The Shamir init
calls it in `AdminCompletionOnce` mode: it unseals only its own node, so the
tenant's home group may have no leader to complete the admin yet. The node's
hook (`pkg/server/lifecycle`) makes one `CompletePendingTenantAdminOnce`
attempt. It answers `ErrTenantAdminPending` when the barrier is sealed or this
node does not lead the home group (`*bootstrap.AdminCompletionDeferredError`).
On that error the init succeeds with `AdminPending` set and the holder
certificates in hand, and the durable pending record stays for a later trigger;
any other hook error fails the init. The service calls the hook in
`AdminCompletionWithin` mode, ignoring its error, from `TenantBarrierUnseal`, from `TenantBarrierQuorumSubmit` when the submission
completes an `OpUnseal` quorum and its cluster seal record is cleared, and from
`TenantBarrierStatus` when the barrier is initialized and unsealed. It is never
called below the threshold, for any other quorum op, or when the operation
fails. A ready barrier can raise it more than once (the unseal, then each
status read), so the hook must be idempotent; `CompletePendingTenantAdmin` is.
`pkg/auth/middleware`: `WithPendingTenantAdmins(p PendingTenantAdmins) Option`;
`pkg/auth/wiring.AuthMiddlewareConfig.PendingTenantAdmins`.

## Descriptive fields — `Description` and `Metadata`

`tenant.TenantInfo` carries `Description string` and
`Metadata map[string]string`, persisted in the tenant record
(`json:"description,omitempty"`, `json:"metadata,omitempty"`). They are set
only through `UpdateTenant`'s `tenant.TenantPatch`, and nothing in qrdb reads
them: they gate no permission, quota or isolation decision.

```go
type TenantPatch struct {
	// ...
	Description *string           // nil: unchanged; "" clears
	Metadata    map[string]string // nil: unchanged; non-nil empty clears; otherwise replaces the whole map
}

const (
	MaxDescriptionBytes   = 1024
	MaxMetadataEntries    = 64
	MaxMetadataKeyBytes   = 63
	MaxMetadataValueBytes = 256
)

type TenantFieldError struct{ Field, Reason string } // errclass.InvalidArgument
```

**Invariants.**

- **Validated before anything is written.** Both `UpdateTenant`
  implementations (`kvTenantService`, from `NewKVTenantService`, and the
  seal-enabled `TenantService`) call `validateDescriptive` first. It checks
  the byte limits, holds every key to the Kubernetes label-name grammar
  without a prefix, `[a-z0-9]([a-z0-9._-]*[a-z0-9])?` (`checkMetadataKey`, a
  byte loop, no regular expression), and refuses a description or value that
  is not valid UTF-8. A record stored before a limit tightened still decodes
  and reads unchanged; only a patch that sets the field is checked. A refused
  patch changes none of
  the tenant's fields, including `Name`.
- **Metadata replaces, never merges.** There is no per-key patch; a client
  that wants to add one entry sends the whole map. An empty map is stored as
  absent (`nil`), and the stored map is a clone of the caller's.
- **Absent and empty are distinct on every transport.** The Go SDK's
  `UpdateTenantRequest.Metadata` is `omitzero` in REST JSON, so a non-nil
  empty map reaches the server as `{}`. In protobuf, `description` is
  `optional` and `metadata` is a `TenantMetadata` message wrapping
  `map<string,string> entries`, because a bare proto map cannot tell absent
  from empty. MCP takes `metadata` as a JSON-object string and a separate
  `clear_metadata` flag, applied only when `metadata` is absent.
- **One error class.** `*TenantFieldError` maps to `errclass.InvalidArgument`:
  400 on REST, `INVALID_ARGUMENT` on gRPC and Unix, `StatusInvalidArgument` on
  QUIC, an `InvalidArgument` tool error on MCP. The embedded transport returns
  the error itself.

**Tests.**

- Each server handler (REST, gRPC, QUIC, MCP) and each SDK transport (REST,
  gRPC, Unix, MCP, embedded) runs against the real in-memory tenant service and
  checks four things: a round trip, that an absent field is unchanged, a clear,
  and an over-limit refusal.
- The QUIC SDK test checks presence on the wire.
- `TestTenant_CLI_DescriptiveFields_PerProtocol` runs the CLI against `qrdbd`
  on all five network transports.

## Isolation modes

`TenantInfo.IsolationType`, set at creation, defaulting to `prefix`:

| Mode | Raft | Storage | At-rest crypto |
|---|---|---|---|
| `prefix` | shared system shard | shared Pebble | shared system barrier |
| `barrier` | shared system shard | dedicated Pebble for IAM data, from the volume pool | per-tenant DEK over IAM data |
| `volume` | per-tenant local single-node, transport-less Raft host | dedicated directory on the tenant's volume pool (`VolumePool`, `volume.Assignment.Path`) | per-tenant DEK over **everything, including the WAL** |
| `raft` | dedicated Raft group per database | SM data under the tenant's volume directory; raft log in the shared host LogDB | per-tenant DEK over the SM data |

## Invariants

- **A resolver-served store is confined to its tenant's keyspace by
  construction.** `StoreResolver.Resolve` returns the system store wrapped in
  `kvstore.NewNamespacedKVStore` at `{tenant}/` for every shared-prefix tenant
  (including the system tenant and unknown tenants). The bare shared store
  would give every prefix tenant one keyspace. A caller must never have to
  remember to prefix.
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
  back to it, so every verb routes on the context tenant; see
  [../auth-factors/engineering.md](../auth-factors/engineering.md#the-custodian-surface-is-routed-too).
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
  whatever the isolation mode says: under `prefix` isolation its collections
  would address the same documents across tenants, and its locks would contend
  for the same name and disclose each other's holder IDs. Adding a data-plane
  surface means adding its accessor in the same change. `pkg/server/collection_namespace_test.go` asserts the accessor COUNT
  by reflection, so widening the interface without widening the coverage fails
  there rather than at a call site in another package.
- **A composite servicer is REBUILT over the wrapped engines, never copied.**
  `applyComposite` wraps each engine and then constructs the collection service
  from the wrapped pair; copying the bundle's own would carry the raw engines
  through. Isolation is a property of the engines a service holds, not of the
  struct it sits in.
- **No prefix is exempt from namespacing**, the reserved `__sys:` keyspace
  included. An exemption would be a second tenancy mechanism beside the
  namespace, and would let a crafted tenant id, a crafted DAO entity type, or
  `pkg/permission`'s tenantless scope keys reach across tenants.
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
  the record's other index writes — never by a sibling maintainer, which lets
  the write path and the read path diverge.
- **An index entry is a CLAIM, taken atomically, before the record is
  written.** Role ids are caller-supplied, so a last-writer-wins entry would
  let one tenant capture another's id and redirect every by-id operation on
  it. The claim uses `kvstore.AtomicKVStore.PutIfAbsent`, which every state
  machine implements — `PebbleStateMachine`, `MVCCEngine` and `KVEngine`
  handle `kv.OpPutIfAbsent`. A store without it is
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
  (`kvstore.AtomicKVStore.CompareAndSwap`), never a plain put.
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
  a tenant or database namespace displaces the first path segment.
- **Encrypted tenant stores still refuse index operations** (`barrier`,
  `volume`, `raft`); `pkg/dao` plans a full scan. See the architecture
  document for why this is an open design question rather than a gap.



- **Isolation type is immutable once the tenant holds data** —
  `ErrTenantHasData`, HTTP 409.
- **`prefix` is logical separation, not cryptographic isolation.**
- **`barrier`, `volume` and `raft` require `storage.volume_pools`.** Without a
  pool, registering a barrier fails with
  `volume pool not configured on barrier registry`.
- **A `dir` volume's path must be absolute and canonical.** `pkg/volume`'s
  dir provider checks it with `validation.AbsoluteCanonicalPath` before it
  opens the volume, and refuses it with a `*validation.PathError` naming the
  predicate. The path predicates live once, in `pkg/validation`, which
  `pkg/volume`, `pkg/tenant` and `pkg/seal` share.
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
  `quota.Reconciler`, which walks each volume tenant's data root
  (`quota.StorageTenant.DataRoot`), at
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
  is the defect, not the fold: `rbac.IsSystemTenant` reads the identity, so a
  boundary that folds the context and leaves `identity.TenantID` raw leaves
  unnormalized the one value the authorizer consumes.
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
  "system" — would be a change to who can authenticate, not a normalization.
  One boundary does it: `sdk/go/transport/embedded`'s RBAC surface REFUSES an
  identity with an empty `TenantID` (`transport.InvalidIdentityError`) rather
  than folding it.
- **No plaintext root key leaves a node**: a `none`, `passphrase` or `shamir`
  tenant barrier is initialized by every serving node with its own key, and
  nothing is shipped (`clusterkeys.ownKeyOnEveryNode`). For any other
  self-managed strategy the leader ships the owner-sealed barrier blob verbatim,
  HPKE-sealed to the receiving node's verified delivery key over mTLS
  (`Sender.DistributeTenantKey`). A platform-custody barrier is not shipped.
- **A tenant seal reaches every replica**, and only the tenant's own
  administrators may issue it — see
  [../barrier-seal/architecture.md](../barrier-seal/architecture.md#sealing-in-a-cluster).
- **Replica placement is quota-checked and spread across distinct healthy
  nodes**; `TenantShardReconciler` converges each node's assigned replicas and
  they survive restarts via the persistent placement table.

## Tenant teardown

`TenantService.DeleteTenant` and `kvTenantService.DeleteTenant` run the same
steps in the same order, because keyspace isolation is not a property of
encryption: a node with the seal subsystem off still has prefix tenants, and
they are exactly the tenants whose data stayed readable after a delete.

```
refuse a RESERVED id
  -> refuse if databases remain
  ---- the fence: nothing below this line is reversible -------------
  -> write the tombstone
  -> delete the metadata record
  ---- past the fence: the tenant no longer resolves for anybody ----
  -> retire the home group, close the runtimes      (seal-enabled only)
  -> destroy the barrier and its durable records   (seal-enabled only)
  -> reclaim the keyspace, the tenant DEK keyring included
  -> mark the teardown complete                     (an undo acknowledgement waits on it)
```

### Persist the retirement before anything irreversible

**That single rule places every step.** The two writes above the line destroy
nothing, so a failure in either returns with the tenant exactly as it was and
its error can be read as "nothing changed". Everything below the line is a
destruction, and by the time any of it runs the retirement is already durable.

The two other orders each fail worse. Shredding first means a transient failure
of the tombstone write returns a store error that reads as *"the delete did not
happen"* over a tenant that is still live, listed and resolvable, and whose
every byte is already permanently undecryptable: one store error, total
unrecoverable loss. Deleting first without a tombstone leaves the data readable
in the window and can strand a key nothing names. Fencing first dominates both:

| | data readable in a window? | live tenant with no key? | residue if interrupted |
|---|---|---|---|
| shred first | no | **yes, and permanent** | metadata for a keyless tenant |
| delete first, no tombstone | **yes** | no | a key nothing names |
| **fence first** | no — steps 2 and 3 make the tenant unresolvable before anything is destroyed | no | a retired id whose barrier or keyspace still exists |

The residue fencing accepts is reachable by nobody. It is removed by re-issuing
the call.

There is no DEK-shred step. The tenant DEK keyring is a record in the
tenant's own resolved store, so the fence makes it unreachable (the resolver
refuses a retired id) and the teardown removes it: a shared tenant's keyring is
under `<tenant>/`, which the keyspace reclaim covers, and an isolated tenant's
is in its home store, which is retired with the tenant before its barrier is
destroyed.

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

It also applies `ValidateTenantIDShape`, so an id carrying a path separator is
refused on the delete path too rather than composing a tombstone key outside
its own range.

`*ReservedTenantIDError` maps to **400 / `InvalidArgument`**: a client naming a
reserved id is a client error on both paths.

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

### The seams

`DeleteTenant` reaches things this package cannot touch, so they are
interfaces wired at composition time (the barrier registry is already held).
Each is optional; a composition that wires none still retires the id, which is
the security half — and which the fence commits before any is called.

| Seam | Satisfied by | Wired at |
|---|---|---|
| `tenant.DatabaseLister` | `*database.Manager` (`TenantDatabases`) | `lifecycle.BuildServices` |
| `tenant.KeyspaceReclaimer` | `*service.KeyspaceReclaimService` | `lifecycle.BuildServices` |
| `tenant.HomeRetirer` | `*service.DatabaseService` (`RetireTenantHome`) | `lifecycle.BuildServices` |
| `tenant.TenantRuntimeCloser` | `server.NewTenantRuntimeCloser` | `server.WireTenantRuntimeCloser`, through `tenant.ResidueWiring` |

**Everything that runs under the tenant's barrier stops before the barrier is
destroyed.** `RetireTenantHome` deletes the home group's placement, so every
node's reconciler closes its home replica and none restarts it; then
`CloseTenantRuntime` closes this node's runtimes synchronously. Only then does
`DestroyTenant` seal the barrier. In the other order, a home replica reopened
by the reconciler writes its Pebble manifest under a sealed barrier, and
Pebble's fatal logger exits the node. A runtime that fails to stop returns
`*TeardownError` over `*RuntimeStopError` (`Op` is `retire-home` or
`close-runtime`) and keeps the barrier; re-issuing the delete resumes there.

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
| `__registry:tenant_posture/{id}` | `BarrierRegistry.RestoreVolumeTenants` at boot, to unseal or leave sealed |
| `__sys:volumepool/assign/{id}` (`volume.Assignment`) | `RestoreVolumeTenants` at boot, and every resolution of the tenant's directory |

So a tenant that was only unregistered has its barrier back on the next
restart. `BarrierRegistry.DestroyTenant` releases the barrier and closes the
per-tenant Pebble (`ReleaseTenant`), removes the tenant's directory, and only
then drops both records (`DropTenantRecords`; the Assignment through
`volume.Allocator.Release`) — the files are released before the directory
holding them is unlinked, and a removal that fails leaves the records, so a
retry still finds the directory.

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
shard directory, applies the same guard (`RequirePathInsideLocalPools`). A dedicated `DataRoot` is
often a mount point, and rmdir on a mount point fails with `EBUSY` (or
`EXDEV`) however empty it is; a root that has been emptied and refuses its own
removal that way counts as reclaimed and is left for the operator to unmount.
Any other failure is returned as `*TenantDataRootRemoveError`. The filesystem
is reached through `seal.DirectoryRemover`, which `seal.WithDirectoryRemover`
replaces, so the failure paths are testable without a failing disk.

It returns `ErrTenantNotFound` when the tenant was not registered here at all,
which `DeleteTenant` treats as success so the delete stays re-issuable.

### Where a tenant lives

A `barrier`, `volume` or `raft` tenant's location is replicated state, so it
is recorded in terms every node resolves against its own disks and never as a
path:

| Record | Holds |
|---|---|
| `__sys:volumepool/assign/{id}` (`volume.Assignment`) | the pool name and the tenant-relative path |
| `__registry:tenant_posture/{id}` | the auto-unseal flag |
| the tenant record (`TenantInfo`) | no path |
| `placement.Replica` | no path: each node resolves its own replica's directory |

`BarrierRegistry.LocalTenantDataRoot` resolves the Assignment to **this
node's** directory on the pool; `TenantService.GetTenant` and `ListTenants`
check that it resolves, and `placementRaftInfoSource` builds a shard's
directory from it. A placement replica that carries a path
(`*placement.ReplicaDataRootError`) is refused rather than trusted: a path
names the disk of the node that wrote it, and trusted on another node it
opened, and on reclamation deleted, a directory that node did not own. There is
no migration for such records; the database is re-created.

**A refused tenant never takes the others down.** A `volume` or `raft` tenant
whose Assignment or pool does not resolve on this node is still listed:
`ListTenants` returns it beside every other tenant with `TenantInfo.Refusal`
set to a `*BarrierError` (`Op` `resolve-data-root`), and `GetTenant` reports
the same refusal; it does not fail the listing. Boot (`reattachVolumeTenants`,
`reattachRaftTenants`) skips it, and `RestoreVolumeTenants` logs and skips an
Assignment it cannot re-attach. `DeleteTenant` on a tenant whose Assignment is
missing or invalid removes no directory: it applies the same no-databases rule,
tombstones the id and deletes the record, and `DestroyTenant` releases the
barrier and drops the records without a removal; the keyspace is reclaimed as
for any tenant. The deleted-tenant reconciliation treats a refused tenant as
live.

**A tenant's location is decided before its barrier is registered.**
`volume.Allocator.Reserve` writes the Assignment in one transaction before
registration, so no registered tenant lacks a recorded location. A posture
record that fails to write is logged, and a restart then restores the tenant
self-managed (sealed).

**A `barrier` tenant is registered the way a `volume` tenant is.**
`BarrierRegistry.RegisterBarrierTenant` reserves an Assignment on the first
configured pool (`ensureGenericPoolAssignment`), opens the barrier store at
`{DataRoot}/barrier`, and writes its posture record, so `RestoreVolumeTenants`
brings it back at boot by the same path: a system-managed tenant unsealed, a
self-managed one sealed.

### Failure modes

| Step fails | State afterwards | Remedy |
|---|---|---|
| Reserved or malformed id | nothing changed — no read, no commit | the id is not deletable; there is no remedy and none is wanted |
| Database enumeration | nothing changed | fix the catalog, retry |
| Databases remain | nothing changed | drop them; the error names them |
| Tombstone write | nothing changed — tenant intact, id not retired, **keyring untouched** | retry |
| Metadata delete | id retired, record still present, keyring untouched | retry; the delete resumes from the top |
| Home retirement or runtime close | id retired, metadata gone, barrier kept | retry; both steps are idempotent |
| Barrier destroy | id retired, metadata gone, tenant and keyring unresolvable | retry; `DestroyTenant` is idempotent |
| Keyspace reclaim | id retired, metadata gone, barrier destroyed, bytes remain | retry; the reclamation is idempotent and a tombstoned tenant is still deletable |

The last three arrive wrapped in `*TeardownError`; the first five do not, and
that is the distinction the message carries.

Re-issuing `DeleteTenant` on a tenant whose metadata is already gone but whose
id is tombstoned resumes past the fence rather than reporting not-found. An
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
pass** — an unreadable live tenant's directory is unknown, and that unknown
could be any candidate below. (`ListTenants` skips such a record; that is right
for a listing and wrong for the feed of a destructive operation.) A live
tenant's claim is the directory its Assignment resolves to on this node. The
pool bases and the reserved data roots are read once too. Then, for each retired id, in order:

1. **Skip the id whole** if a record exists for it (`SkipLiveTenantRecord`) —
   a direct existence read of the id's own key, so a record that exists but
   will not decode still counts.
2. **Collect the candidates** — the directory the id's Assignment resolves
   to on this node (`BarrierRegistry.TenantDataRootCandidates`); with no
   Assignment left, the directory the tombstone's `VolumePool` places it at
   (`BarrierRegistry.PlacementDataRoot`).
   **An id already fully reclaimed is skipped silently** — no candidate exists
   on disk and `BarrierRegistry.HasTenantState` reports no barrier and no
   Assignment — so a pass does not stop runtimes or touch records for
   every retirement ever made.
3. **Stop the runtime** through the `tenant.TenantRuntimeCloser` seam —
   `server.NewTenantRuntimeCloser` closes the id's `TenantHostRegistry` host
   and every dedicated shard `RaftBackendFactory` started for it. Close, never
   purge: which directories go is this pass's decision.
4. **Release the barrier** — `BarrierRegistry.ReleaseTenant` seals and forgets
   it in memory and closes the per-tenant store. Nothing durable changes.
5. **Remove each existing candidate** with `BarrierRegistry.RemoveDataRoot`
   (contents first, so a mount-point root is reclaimed), except one that is
   not an absolute, canonical path (`SkipNotCanonical`), that is, contains or
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
   deletes the posture record and releases the Assignment. An id whose
   removal failed, or one of whose directories was skipped, never reaches
   this step, so the record that names the directory survives and the next
   pass finds it again; an undone id's acknowledgement is written only after
   this step, so it waits on the same pass, and `ReconcileUndone` reports the
   skip as `*TenantUndoPendingError` naming this node. Dropping the records
   first would orphan a directory that nothing named.
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
that read and the removal is not seen; its directory is under its own id, so it
is not a candidate of a retired one.

## Governance planes

| Plane | Controls | Enforced at |
|---|---|---|
| RBAC permissions | which operation a principal may call at all | transport middleware |
| Numeric quotas | how many of a resource a tenant may hold | service layer, before delegating |
| Feature entitlements | which tiers/features the plan permits | service layer, **before** the quota check |

**Do not collapse entitlements into RBAC roles.** They answer different
questions and are owned by different people.

Provisioning-governance permissions: `tenants:set_isolation`,
`tenants:set_status`, `tenants:set_entitlements`, `databases:create_raft`, `databases:create_volume`.

### Tenant-administration authorization

The per-route permission is checked against the CALLER's tenant, which says
nothing about the tenant the request names. The second decision lives in one
place, `pkg/auth/middleware/tenant_admin.go`, and every network transport calls
it:

| Operation | Decision |
|---|---|
| get / delete | `AuthorizeTenantTarget(ctx, id, tenants:read / tenants:delete)` |
| update | `AuthorizeTenantUpdate(ctx, id, patch)` — `tenants:update` for a descriptive field, `tenants:set_isolation` when `patch.IsolationType` is set, `tenants:set_status` when `patch.Status` is set |
| quotas / entitlements | route permission `tenants:set_quotas` / `tenants:set_entitlements` (cross-scope), re-checked against `id` |
| create | route permission `tenants:create` (cross-scope) |
| list | `FilterTenantList` — every tenant with `tenants:list:cross` against `system`, else the caller's own; any refusal other than an RBAC denial is returned, not narrowed |

The five plan permissions (`tenants:create`, `tenants:set_quotas`,
`tenants:set_entitlements`, `tenants:set_isolation`, `tenants:set_status`) are
`ScopeCross`
(`pkg/permission/tenant_provisioning_scope.go`). The built-in Admin role carries
them in every tenant; the scope rule makes them inert outside `system`.

A tenant's `Status` (`pkg/tenant/status.go`) is enforced by the auth chain, not
by each handler. `AuthMiddleware.finalizeIdentity` and `finalizeDelegated` call
`checkTenantActive` (`pkg/auth/middleware/tenant_status.go`) once the identity's
tenant is normalized, beside the account's own lifecycle gate, so every
transport refuses a principal of a suspended or disabled tenant with
`*TenantNotActiveError`, classed with `*UserDisabledError` (403 /
`PermissionDenied`). The record is read through `WithTenantStatusReader` on
every request, with no cache, so a token issued before a suspension is refused
on its next request and reactivation restores access the same way. The system
tenant is never looked up: its administrators are the principals who change
another tenant's status. A tenant with no record is admitted as the store
resolver admits it; a record with an empty or unknown status fails closed.

Each transport validates the id with `tenant.ValidateTenantIDShape` before it
authorizes, and maps `*tenant.InvalidTenantIDCharError` to 400 /
`INVALID_ARGUMENT` (MCP classifies through gRPC's mapper).

`rbac.RBACAuthorizer` resolves an identity's role permissions under the
IDENTITY's tenant, never the request's target: the role store refuses a role
outside the context tenant, so resolving under the target would refuse a
`system` operator acting on another tenant.

`tenants:delete` stays `ScopeSelf`: a tenant's Admin
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
`MaxTSMetrics`, `MaxDedicatedWALGroups`. `SystemBarrierQuota` bounds, in
bytes, an isolated tenant's use of the shared system barrier: `0` denies it,
`-1` is unlimited. The `Current*` fields beside them are the usage counters.

### Entitlements — `quota.Entitlements`

| Field | Permits |
|---|---|
| `AllowedIsolationModes` | which isolation modes the tenant may select |
| `AllowDedicatedRaft` | provisioning dedicated Raft-backed databases |
| `AllowVolumeStorage` | provisioning databases on a dedicated volume |
| `AllowSystemManagedBarrier` | opting an isolated tenant into system-managed auto-unseal |
| `AllowDedicatedWAL` | a per-tenant-key WAL for a dedicated-raft database |
| `AllowedBarrierModes` | which seal strategies an isolated tenant may pick; **empty = built-in software defaults, hardware fail-closed** |
| `AllowedVolumePools` | which volume pools a `volume` or `raft` tenant may be placed on, the first the default; **empty = none** |
| `AllowReplicatedAudit` | writing the tenant's audit events to the cluster-wide replicated trail |

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

**The built-in defaults are deliberately conservative**: 5 databases, 1 GiB,
`prefix` isolation only, and dedicated raft, volume storage, system-managed
barrier, system-barrier quota and hardware seal strategies **all denied** until
an operator grants them. Full reference:
[../configuration/](../configuration/).

## Encryption at rest

| Data | Encryption |
|------|------------|
| State-machine Pebble (shared shard) | Barrier-encrypting filesystem under the system barrier |
| Shared Raft WAL / LogDB | Encrypted at rest under the **system barrier** |
| `volume` / `raft` tenant SM data | Encrypted under the **tenant DEK** in the tenant's volume directory on its pool |
| `volume` tenant raft WAL | Encrypted under the tenant DEK (the per-tenant host barrier-wraps its own WAL) |
| `raft` tenant raft log (shared host) | Encrypted under the **system barrier**; under the **tenant's barrier** for a database created with a dedicated WAL (below) |

A `raft`-backed database created with `dedicatedWal` (REST; `DatabaseConfig.DedicatedWAL`)
writes its Raft log to a dedicated WAL under the owning tenant's barrier rather
than the shared system-barrier WAL. The request needs the `allow_dedicated_wal`
entitlement and room in the `max_dedicated_wal_groups` quota; without either it
is refused with the entitlement or quota error, never downgraded to the shared
WAL. On any other backend the flag is ignored. A strict tenant's database
requires one (§ "Strict isolation").

Only a `raft` tenant's database is served through its own Raft group, so only
there does a dedicated WAL carry the database's data. Every database of a
`volume` tenant is served from the tenant's own store, and a `barrier`
tenant's raft-backed database is served from the shared system store (a
per-database barrier route serves non-raft dedicated stores only). Under
either isolation a `raft` + `dedicatedWal` request would persist and meter a
group no KV write reaches, so it is refused with `DedicatedWALOffDataPathError`
(422) naming the store that serves the database, before anything is persisted
or metered. The refusal precedes the strict-tenant check, so a strict `volume`
or `barrier` tenant's request gets it too.

## Deployment requirement

**The tenant service must be configured in production.**

Tenant identity is sourced only from the authenticated mTLS identity, and
unknown-tenant requests are rejected by the tenant service. **If a deployment
wires the data-plane service registry without a tenant service** — a path
for single-tenant embeddings — **that
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
| Quota exceeded | `*quota.ExceededError`, before delegating | **yes** |
| Barrier tier without `storage.volume_pools` | `seal.ErrPoolNotConfigured` (`volume pool not configured on barrier registry`) | **yes** |
| Unknown tenant, tenant service configured | `*service.TenantNotFoundError`, 404 / `NotFound` | **yes** |
| Unknown tenant, **no** tenant service | **accepted** — the documented gap above | **no** |
| Tenant barrier sealed | `*service.BarrierSealedError`, 503 / `Unavailable` | **yes** |
| Deleting a RESERVED tenant id (`system`, `default`, `custodian`, `security-officer`, `_*`, `*:*`) | `*ReservedTenantIDError`, 400 / `InvalidArgument`, nothing committed | **yes** |
| Deleting a malformed tenant id | `*InvalidTenantIDCharError` / `*InvalidTenantIDError`, nothing committed | **yes** |
| Creating a tenant under a DELETED id | `*TenantDeletedError`, 409 / `AlreadyExists` | **yes** |
| Creating a tenant with no admin credential, or two | `*bootstrap.MissingAdminCredentialError` / `*bootstrap.ConflictingAdminCredentialError`, 400; nothing written | **yes** |
| Admin CSR or certificate refused | `*bootstrap.AdminCredentialRejectedError` wrapping the cause, classified as the cause; nothing written | **yes** |
| Trust anchors unparseable or not CA certificates | `*bootstrap.InvalidTrustAnchorsError`, 400; nothing written | **yes** |
| A seam the request needs is not wired (signer, binder, anchor registry, pending-admin store, delete seams for an exclusive run) | `*bootstrap.MissingProvisioningSeamError`, 501; checked before any write | **yes** |
| Exclusive provisioning failed after a write | `*bootstrap.ProvisioningUndoneError` (`Cause`, `UndoFailures`); writes undone in reverse, id retired | **yes** |
| Onboarding not finished within the service's deadline (`service.DefaultTenantOnboardingTimeout`, 60 s, on every transport; the caller's sooner deadline still applies) | the step cut short fails with `context.DeadlineExceeded`, undone as above, 504 / `DEADLINE_EXCEEDED`. The REST handler applies no bound of its own to create, as on every tenant endpoint (next row) | **yes** |
| Any other tenant operation not finished within the caller's deadline: get, list, list deleted, reconcile deleted, update, delete, quotas, entitlements, barrier init, unseal, seal and status, and every tenant barrier share-holder route | `context.DeadlineExceeded`, 504 / `DEADLINE_EXCEEDED`. Each is bounded by the caller's deadline alone (the CLI's `--timeout`), on every transport | **yes** |
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

The distinction is not cosmetic. A bare timeout reads identically whether the
shard is broken or the machine is merely busy, and under CI load the second is
common: the 20 ms poll sleep overruns by microseconds on an idle host, so a gap
of ten intervals is direct evidence of descheduling rather than of anything
raft did.

**The 15 second budget is not a tuning knob.** It already dwarfs the election's
configured cost. go-quicraft derives the election timeout as
`ElectionRTT * RTTMillisecond`, and the tenant Host is built
`WithoutTransport()`, so it carries `cluster.TransportlessRTTMillisecond`
(25 ms) and `cluster.SingleNodeElectionRTT` (5 ticks) — a **125 ms** election
budget. `RTTMillisecond` is also the engine tick interval, so it prices
continuous CPU as well as election latency; 25 ms captures 95% of the
achievable boot latency for 32% of the CPU that 5 ms costs.

Widening it to quiet a red suite would remove the tests' ability to fail for the
right reason: a genuine regression that quintupled election time would still fit
inside the larger budget, leaving tests that are red when the host is busy and
green when the code regresses. If this wait times out, read the verdict in the
error before changing the number.

### The wait reads time through a seam, and production reads the real clock

The poll loop takes its clock as a parameter (`shardLeaderClock`: `Now` and
`Sleep`, and nothing else). Production passes `realShardLeaderClock`, so every
number in the error above — elapsed, longest gap — is measured against real wall
time. That is not incidental: the starvation verdict *is* a statement about wall
time, and a wait that measured virtual time in production would report
"the shard genuinely did not elect" on a host that never let it ask.

The seam exists for the tests. They script the fake shard **by poll number**
— fail on poll 1, fail again on poll 3 — while the loop stops on a
**wall-clock budget**. Those are the same quantity only on an idle host: on a
shared CI runner a 30 ms budget at a 1 ms interval can buy two polls where an
idle host delivers thirty-two, so a scripted poll never runs. Under a virtual
clock, time advances only when the loop sleeps and by exactly the interval it
asked for, so the poll count is arithmetic rather than a bet on the scheduler.
**Do not respond to a red test here by enlarging a budget:** under that clock
a budget cannot be missed, so a failure is a real defect.

The budget is roughly 120x the election it waits on. Tightening it needs its
own evidence, separate from the argument against widening: what this wait
absorbs is host descheduling rather than election time, and the two do not
shrink together.

## Test strategy

**Integration target:** `make integration-test-multi-tenancy` (no Docker; selection in `test/integration/features.mk`). The tenant CLI suites (lifecycle, deleted-id retirement, isolation, barrier) run the real `qrdb` binary against a self-spawned `qrdbd`. Per-transport accounting, including the transports a capability does not reach, is the `multi-tenancy` row of `test/integration/COVERAGE.md`.

- **Unit**: entitlement refusals, `Max*` semantics for `-1` and `0`, the
  isolation-change refusal, and the volume-pool precondition.
- **Entitlement checks run before quota checks** (`DatabaseService.CreateForTenant`)
  — reversing them would leak information about counters to a plan with no
  access to the tier. No test pins the order.
- **The no-tenant-service path is tested explicitly** so the gap stays visible
  rather than becoming a surprise.
- **Create/delete races are driven deterministically**
  (`pkg/tenant/create_delete_race_test.go`): the store double applies the
  concurrent operation's write through the real store at the moment of the
  commit and delegates the commit itself, so the outcome is decided by the
  store under test. A restart is simulated by `RestoreVolumeTenants` after the
  losing create. Real-goroutine races assert one winner per id and no
  tombstone beside a live record.
- **Integration**: the `raft`-isolation suites —
  `isolation_raft_cluster_test.go`, `isolation_raft_dedicated_wal_test.go`,
  `isolation_raft_rebalance_test.go`, `isolation_raft_scale_test.go`,
  `isolation_raft_shard_ids_test.go` — run with the feature's other suites. CI runs them in its **full tier** — every push to
  `main`, every `workflow_dispatch`, and a pull request labelled `ci:full` —
  in one shard of the `integration-features` job
  (`make integration-test-multi-tenancy`, the files `features.mk` maps to this
  feature), each shard under its own 30-minute limit. A plain pull request push runs none of them, so a change to tenant
  isolation carries `ci:full`. `test/buildgates/feature_jobs_gate_test.go`
  fails when the job's matrix drops a feature that maps a file, when the job's
  `if:` is not exactly the full-tier condition, or when the workflow's
  top-level `on:` stops naming `push` and `pull_request`, and
  `ci_tiers_test.go` fails when the result gate stops tagging it `full` — any
  narrower condition would be excused as a tier skip on exactly the pushes to
  `main` this gate exists to cover. `feature_integration_coverage_test.go`
  fails when an `isolation_raft_*` test, or any other non-Docker integration
  test, is named by no feature, and so by no leg.

## Observability

The `Current*` counters of the tenant record's `quota.Quotas` are the live usage view; the quota
reconciler corrects `volume` tenants' storage drift on its interval. Tenant
provisioning, quota and entitlement changes are audited through
[../audit/](../audit/).

Log events (`pkg/tenant`, each with a `tenant_id` attribute):

- **Lifecycle, `Info`**: `tenant created`, `tenant updated`, `tenant retired`,
  `tenant deleted`, `tenant quotas updated`, `tenant entitlements updated`,
  `deleted-tenant reconcile`, `reclaimed deleted tenant directory`.
- **Barrier, `Info`**: `tenant barrier registered`, `tenant barrier
  initialized`, `tenant barrier init statement claimed`, `tenant barrier
  sealed`, `tenant barrier unsealed`, `tenant shamir barrier prepared`,
  `tenant shamir barrier initialized`, `tenant barrier holder quorum
  complete`.
- **Refusals and residue, `Warn` or `Error`**: `tenant refused on this node;
  its data is not opened` (`Error`), `skipping corrupt tenant metadata`,
  `deleted-tenant reconcile left a path in place`, the `tenant create lost to
  a delete` and `tenant create undone` family, and `undone tenant ... left to
  the next reconcile`.

There are no tenant-specific metrics.

## Strict isolation — engineering contract (RULED)

> **Status: partially implemented.** Design:
> [architecture.md](architecture.md#strict-tenant-isolation--design). This
> section is what an implementation must satisfy, and the
> [implementation plan](#implementation-plan) it is built in. The code has the
> isolation policies (`pkg/tenant/isolation_policy.go`), `ScopeSelf` without a
> system lift, and the home store that holds an isolated tenant's identity
> records, DEK keyring, CA and certificate records. It does not have the
> serving set, the SNI relay, per-node credential verifiers, the tenant
> bootstrap record, operator access to tenant identity, or a dedicated WAL per
> strict tenant database, so the `strict-isolation` feature gate is registered
> but not supported.
> An invariant or error below that names no symbol is a requirement not yet
> built.

### Inventory — where tenant records live today

Store key:
- **SYS** is the Raft-replicated system shard.
- **SEAL** is the node-local seal Pebble at `{dataDir}/seal`, which is not
  replicated.
- **TB** is the tenant's node-local barrier store, the `barrier` directory under
  the tenant's volume directory.
- **NS** is the namespaced data plane.

*Required* means required in the system tenant under the design.

| Record | Key (constant) | Store | Written by | Read by | Required? |
|---|---|---|---|---|---|
| Tenant record: isolation, plan, usage, volume pool, auto-unseal | `__sys:tenants/{id}` (`metadataPrefix`, `pkg/tenant/service.go`) | SYS | `TenantService.CreateTenant`, `UpdateQuotas`, `UpdateEntitlements`, usage counters | `pkg/server/rest_registry.go` (every request), `pkg/tenant/store_resolver.go`, `pkg/server/tenant_infra.go` (boot), `quota.Reconciler` | **Yes**, minus the identity-object counters |
| Tombstone | `__sys:tenants-deleted/{id}` (`tombstonePrefix`, `pkg/tenant/tombstone.go`) | SYS | `TenantService.fence` | `TenantService.CreateTenant`, `pkg/tenant/store_resolver.go`, `pkg/tenant/deleted_tenants.go` | **Yes** |
| Placement | `__sys:placement/{t}/{dbid}` (`PlacementPrefix`, `pkg/placement/store.go`); `dbid` is the opaque id in the database's catalog record, the home group's is `database.TenantHomeID` | SYS | `pkg/service/database_service.go` | `pkg/server/tenant_shard_reconciler.go`, `pkg/clusterkeys/receiver.go`, `pkg/server/raft_backend_factory.go` | **Yes**, with opaque database ids (S5) |
| Volume-pool assignment | `__sys:volumepool/assign/{id}` (`AssignmentKeyPrefix`, `pkg/volume/catalog.go`) | SYS | `pkg/volume` catalog | `pkg/volume` catalog, `seal.BarrierRegistry` | **Yes** |
| Tenant barrier posture (auto-unseal) | `__registry:tenant_posture/{id}` (`tenantBarrierPostureStorePrefix`, `pkg/seal/barrier_registry.go`) | SEAL | `BarrierRegistry.persistTenantBarrierPosture` | `loadTenantBarrierPosture`, `RestoreVolumeTenants` | Node-level; stays |
| Sealed tenant root key and its sibling records | `tenant/{id}/root-key*` (`tenantBarrierConfig`, `pkg/seal/barrier_registry.go`; `pkg/seal/unsealkey.go`, `pkg/seal/migrate.go`, `pkg/seal/recovery.go`) | TB | `StorageBarrier` | `StorageBarrier` | Node-level; becomes the per-replica blob |
| IAM of `barrier`/`volume`/`raft` tenants (users, roles, factors, lookups, invites, perm scopes) | the record's own key, un-namespaced (`tenant.HomeStoreSource`, chosen in `pkg/tenant/store_resolver.go`) | the tenant's home store: its home group in a cluster, `tenant.LocalHomes` under its barrier on a standalone node | user/role/factor/invite stores via the resolver | the same, plus the auth middleware | **No**: in the home store |
| User records and indexes | `__sys:users/…`, `__sys:user_idx/{username,email,cert_spki}/…` (`pkg/user/store.go`) | home store, or `{t}/` for a shared tenant | `pkg/user/store.go` | middleware, routing | **No**: in the home store |
| Role records and indexes | `__sys:roles/…`, `__sys:role_idx/name/…` (`pkg/role/store.go`) | home store, or `{t}/` for a shared tenant | `pkg/role/store.go` | RBAC, `pkg/auth/rbac/rbac.go` | **No**: in the home store |
| User auth factors and lookup | `__sys:auth/{factors,users,lookup,factorsets}/…` (`pkg/service/user_auth_factor_service.go`, `user_auth_factor_set.go`) | home store, or `{t}/` for a shared tenant | routed factor service | `pkg/auth/login.go` | **No**: in the home store |
| Custodian factors and challenges | `__sys:authfactor/…` (`sysEnrollmentPrefix`, `pkg/service/authfactor_service.go`) | home store, or `{t}/` for a shared tenant | routed | routed | **No**: in the home store |
| Permission-scope overrides | `__sys:perm_scope/{perm}` (`pkg/permission/tenant_scope_store.go`) | resolver (`NewTenantScopeStore`; not wired in `qrdbd`) | — | — | **No**: home store |
| Invitations | DAO entity types `invites`, `invite_redemptions` (`pkg/invite/store.go`) | resolver (wired by the SDK only, `sdk/go/types_invite.go`) | — | — | **No**: home store |
| DEK keyring | `__sys:dek/keyring` (`pkg/dek/keyring.go`) | home store, or `{t}/` for a shared tenant | `pkg/dek` | `pkg/dek` | **No**: in the home store |
| Identity index | `system/__sys:identity_idx/tenant/{kind}/{id}` (`pkg/tenant/identity_index.go`) | SYS, plaintext; none for an isolated tenant | `pkg/tenant/identity_index.go` | `tenant.OwningTenant`, user/role/factor routing | **No: removed** |
| Tenant CA key and certificate | `__sys:cert_ca/{t}/…` (`prefixCertCA`, `pkg/cert/default_provider.go`; key through `pkg/cakey`) | home store for an isolated tenant (`pkg/cert/home_routing.go`); SYS otherwise | `DefaultCertProvider.Initialize` | `DefaultCertProvider.getCAMaterial` | **No**: in the home store. The public certificate is published at `__sys:cert_ca_pub/{t}/` (`prefixCertCAPub`), which the trust path (`IsTenantIssuer`, `CACertPEMs`) reads |
| Issued certificates | `__sys:certs/{t}/{serial}` (`prefixCerts`, `pkg/cert/default_provider.go`) | home store for an isolated tenant; SYS otherwise | `DefaultCertProvider.IssueClientCert` | `TenantForSerial` | **No**: in the home store |
| Revocation markers | `__sys:certs_revoked/{serial}` (`prefixCertsRevoked`, `pkg/cert/default_provider.go`) | home store for a certificate an isolated tenant's CA issued; SYS otherwise | `RevokeCert` | `IsPeerRevoked`, `pkg/auth/mtls.go` | Tenant-issued: **No**, in the home store. Node and platform certificates: stay. |
| SO PIN commitment | `__sys:so-pin-commit/{t}` (`soPINCommitPrefix`, `pkg/auth/so_auth.go`) | SYS, unconfined root store (`cmd/qrdbd/start.go`) | `SOAuthenticator` | the same | **No: moves** to the bootstrap record |
| Tenant officer roster | `ceremony/tenants/{id}/officers/` (`pkg/ceremony/barrier_scope.go`) | TB | tenant ceremony (`pkg/ceremony`) | claim and unseal | **No: moves**, plus the unseal roster in the bootstrap record |
| Database catalog | `__sys:dbcatalog/{t}/{name}` (`pkg/database/catalog.go`, `CatalogKey`) | home store for an isolated tenant; SYS for the system and shared tenants | `Manager.Create`, `Update*`, `Drop` through `database.KVCatalog` | `Manager.OpenAll`, `Manager.ListForTenant`, `Manager.Open` | **No**: in the home store |
| Collection work journal | `__sys:collection-work/{t}/…` (`CollectionWorkPrefix`, `pkg/service/collection_types.go`) | SYS for prefix/barrier tenants | `pkg/server/collection_work_journal.go` | `pkg/server/collection_sweeper.go` | **No: moves** |
| Drop fence | `__sys:dropfence/{t}/{db}/` (`DropFencePrefix`, `pkg/service/drop_fence.go`) | the data's group; SYS for shared tenants | `pkg/service/keyspace_reclaim.go` | `DropFenceGuard` | **No: moves** with the database store |
| Reclaim index | `__reclaim:index/{job}` (`reclaimIndexPrefix`, `pkg/service/reclaim_job.go`) | SYS | `TableService` | `ReclaimSweeper` | **No: moves** |
| Locks, lease owners, row locks, sequences | `__sys:lock/`, `__sys:lease-owner/{t}/{db}/`, `__sys:rowlock/`, `__seq:`/`__usq:` (`pkg/lock/types.go`; `pkg/server/namespaced_lease.go`; `pkg/rowlock/record.go`; `pkg/service/table_types.go`, `pkg/service/sequence_service.go`) | NS: SYS for prefix/barrier tenants | data plane | data plane | **No: moves** with the database store |
| Time series, index catalog | `ts:`, `idx:metric:`, `idx:tag:`, `retention:` + `{t}/{db}/` (`pkg/timeseries/keys.go`); `idx:` (`pkg/index/internal/indexkey/indexkey.go`) | SYS for prefix/barrier tenants | data plane | data plane | **No: moves** with the database store |
| Vector namespaces | `{dataDir}/vector`, names `{t}/{db}/…` (`pkg/server/lifecycle/lifecycle.go`) | node-local, shared by all tenants | `VectorService` | the same | **No: moves** (databases binding design) |
| Encryption-migration records | `__sys:encmig/{t}/{db}[/nodes/{n}]` (`clustermig.Prefix`, `pkg/clustermig/store.go`) | SYS | `clustermig.Store` | the same | Tenant scope: **moves**. System scope: stays. |
| Audit events | `{dataDir}/audit-events.ndjson` (`audit.DefaultTrailFile`) | node-local, every tenant in one file; the replicated chain for a tenant whose audit entitlement is on | `audit.ChainFileLogger`, `service.ReplicatedLogger` | `ChainFileLogger.Query`, `ReplicatedLogger.Query` | Tenant events: the tenant's replicated trail in its home group. Platform events: the replicated platform trail in the system shard. The node-local file is the destination of neither. |
| Rate-limit buckets | key `tenant\0subject\0perm` (`pkg/auth/middleware/ratelimit.go`) | node memory | limiter | limiter | Node memory; stays |
| Metrics | `qrdb_vector_*{tenant,namespace}` (`pkg/service/vector.go`) | scrape | — | `/metrics` (authentication only, `pkg/api/rest/server.go`) | Namespace label removed |
| Tenant key delivery | HPKE envelope with root key or sealed blob (`pkg/clusterkeys/sender.go`; import `StorageBarrier.ImportSealedBlob`, `pkg/seal/import_key.go`) | in transit; re-wrapped into TB | `clusterkeys.Sender.DistributeTenantKey` | `pkg/clusterkeys/receiver.go` | **Removed.** Verifier delivery replaces it. |

### Rules that hold under every policy

1. **Only the owning tenant reaches a database.** The etcd listener
   (`pkg/server/server_etcd.go`), the SQL provider and the REST database routes
   check `database.OwnedBy`, an exact match. The system tenant is not admitted
   to another tenant's database.
2. **`ScopeSelf` admits the caller's own tenant only, for every identity.**
   The system tenant gets no lift (`pkg/auth/rbac/rbac.go`). A client
   certificate is therefore issued (`certs:issue`) only into the caller's own
   tenant.
3. **Audit is confined to the caller's tenant on every transport.** Query goes
   through `tenant.ConfineAuditFilter` and chain verification through
   `tenant.Confine`. Both refuse a `tenant_id` naming another tenant with
   `tenant.CrossTenantDeniedError`.
4. **Submitted Shamir share bytes are never persisted.**
5. **A role binding names the holder's own tenant.**
   `user.NormalizeRoleBindings` refuses any other with
   `user.RoleBindingTenantError`.
6. **Shard ids are unique across the cluster.** `shard.Claimer.ClaimShardID`
   records each id as issued in the replicated store before returning it.

### Invariants

- **The residue list is closed.** The only keys in the system store that are
  derived from a strict tenant's id, or carry its records, are the ones listed
  in [architecture.md](architecture.md#what-stays-in-the-system-tenant-and-why).
  A test over the system store must enforce it.
- **No tenant key crosses a node boundary.**
  - Nothing exports a tenant root key, a replica key, or a sealed blob meant
    for import on another node.
  - Today `clusterkeys.Sender.DistributeTenantKey` delivers a tenant root key
    or sealed blob, and `StorageBarrier.ImportSealedBlob` imports it. This
    invariant removes both.
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
  - Placement refuses a database group with a replica outside the set.
  - Growing the serving set is the tenant-scale verb.
- **A node outside the serving set never terminates a strict tenant's
  connection, when it can avoid it.**
  - A connection whose SNI names a strict tenant this node does not serve is
    relayed unterminated, inside a node-mTLS tunnel, to a serving node.
  - A connection it had to terminate (no tenant SNI, or Unix or embedded)
    and whose certificate names such a tenant is refused before any user
    lookup.
  - The relaying node never looks the user up, never forwards an on-behalf
    assertion, and never logs an authorization decision about a tenant user.
    It logs only the relay itself to the platform trail: tenant, client
    address, serving node.
- **The serving node binds SNI to the certificate.** On a relayed or a direct
  connection, a certificate that names a tenant other than the SNI tenant is
  refused.
- **A strict tenant's users authenticate only with certificates chaining to
  that tenant's own CA.** A cluster-CA certificate naming the tenant is
  refused. The SPKI binding policy is `required` for strict tenants, whatever
  `auth.mtls_binding_policy` says for the rest.
- **The policies only tighten** (`pkg/tenant/isolation_policy.go`).
  - The cluster floor is written with a compare-and-swap:
    `setClusterIsolationPolicy`, composed of `PutIfAbsent` and
    `CompareAndDelete` over `clusterPolicyBackend`.
  - The floor permits only `unset → strict | operator-access` and
    `operator-access → strict` (`clusterIsolationTransitionAllowed`).
  - An unset floor reads as `operator-access` (`getClusterIsolationPolicy`).
    Each node applies `tenant.isolation_policy` to the floor before it serves
    (`cmd/qrdbd/isolation_floor.go`); a refused change is logged and the
    stored floor stands.
  - The tenant setting permits only `inherit → strict`:
    `checkIsolationPolicyChangeFor` refuses any change away from `strict`
    with `tenant.IsolationPolicyChangeError`.
- **The floor governs isolated tenants only.** A shared
  (`quota.IsolationModePrefix`) tenant is created and updated under every
  floor, by `TenantService` and `kvTenantService` alike.
- **`ScopeSelf` has no system lift, under every policy.**
  - `RBACAuthorizer.Authorize` (`pkg/auth/rbac/rbac.go`) admits a `ScopeSelf`
    permission only on the caller's own tenant. `ScopeSelfStrict` behaves the
    same and is still a separate value.
  - `tenant.EffectiveTenant` (`pkg/tenant/context.go`) has no system-tenant
    branch: every identity, the system tenant included, acts only on its own
    tenant through it. This is the layer that routes `CreateUser`,
    `CreateRole` and factor enrolment, independent of the RBAC entry check.
  - Cross-tenant reach exists only through `ScopeCross` permissions, and they
    act on system records. Operator access to identity records in a named
    tenant whose effective policy is `operator-access` is not built: under
    every floor, cross-tenant identity reach is absent.
  - `tenants:read:cross`, `tenants:delete:cross` and `tenants:update:cross`
    (`pkg/permission/cross_scope.go`) cover reading, deleting and updating
    another tenant. `tenants:update` stays self-scoped (see its row in
    [Tenant-administration
    authorization](#tenant-administration-authorization) above).
    `AuthMiddleware.AuthorizeTenantTarget`
    (`pkg/auth/middleware/middleware.go`) tries the self-scope permission
    first and falls back to the matching cross one only when the target is
    not the caller's own tenant. A patch that both renames and re-isolates
    another tenant needs `tenants:update:cross` and `tenants:set_isolation`,
    each authorized on its own.
- **Tenant provisioning and authentication bind the context to the target
  tenant before any routed lookup.**
  - `bootstrap.ProvisionTenant` rebinds its context with
    `tenant.WithTenantID(ctx, cfg.TenantID)` before it seeds the new tenant's
    built-in roles and creates its first admin. It is reached only through
    the `ScopeCross` `tenants:create` gate. The identity in the context is a
    separate key and is untouched, so audit records name the real caller.
  - `AuthMiddleware.finalizeIdentity` binds the context to the authenticating
    identity's tenant before `lookupUser` runs, because the routed user
    lookup requires the binding.
  - For a system-managed (auto-unseal) dedicated tenant,
    `TenantService.registerDedicatedBarrier` initializes the barrier through
    the registry and does not call `notifyBarrierReady`, so no
    `tenant.BarrierReadyHook` fires. `ProvisionTenant` instead reports
    `AdminCompletionDue`, set from the created `tenant.TenantInfo`'s
    `BarrierAutoUnseal`, after `provisionPendingAdmin` has written the
    pending-admin record. `TenantOnboardingService.OnboardTenant` completes
    the admin after it answers, under its own deadline. A self-managed tenant
    completes its admin through the barrier-ready hook when its own admin
    initializes or unseals the barrier.
- **No permission reaches tenant data across tenants.** Every data-plane
  permission is `ScopeSelf` or `ScopeTenantOnly`, never `ScopeCross`. That
  covers KV, MVCC, SQL, collections, vectors, time series, locks, leases,
  watch, etcd, sequences, transactions, indexes and tenant-group backups.
  No test over the whole permission registry asserts it yet. The `ScopeSelf`
  tests in `pkg/auth/rbac` and the `CrossTenantDeniedError` tests in
  `pkg/user`, `pkg/role` and `pkg/service` cover it piecemeal.
- **Operator access is recorded twice.** An operator-access decision on a
  tenant's identity records is written to the platform trail and to the
  tenant's own trail before the operation runs. If either write fails, the
  decision is refused, whatever the audit failure policy says.
- **The first admin is bound to the creation CSR's key** (`pkg/initproof`).
  - A self-managed tenant's init is accepted only with a fresh signature,
    bound to the TLS exporter, by the key whose SPKI the pending first admin
    records.
  - No certificate, role or operator permission substitutes for that proof.
  - The challenge is single-use, expires after five minutes, and verifies
    only on the TLS 1.3 connection that asked for it. The embedded transport
    has no connection and asks no proof.
- **The tenant bootstrap record is MAC-bound to the replica key.** It is
  verified on every unseal against the home store's copy, and a mismatch
  re-seals the tenant on that node.

### Data model

`tenant.TenantInfo` (`pkg/tenant/service.go`) is the tenant's record in the
system tenant. Its fields are `ID`, `Name`, `IsolationType`, `Quotas`
(`*quota.Quotas`), `Entitlements` (`*quota.Entitlements`), `VolumePool`,
`BarrierAutoUnseal`, `CreatedAt`, `UpdatedAt`, `IsolationPolicy`
(`TenantIsolationPolicy`), `Description`, `Metadata` and `Status`. `Refusal`
is set by `ListTenants` alone and never persisted.

Strict isolation requires these additions:

- **A lifecycle**: provisioning, active, deleting.
- **A home group**: its shard id and replicas; empty for a shared tenant.
- **A pending first admin**: username and SPKI thumbprint, for a self-managed
  tenant until init.
- **Limits and counters split.** `quota.Quotas` holds each limit and its
  `Current…` counter together. The identity-object limits stay in the plan,
  because the platform sets them: `MaxUsers`, `MaxRoles`, `MaxCertificates`,
  `MaxCollections`, `MaxKeys`, `MaxTSMetrics` and `MaxVectorDims`. Their
  counters move to the home store, and the home group enforces them.
- **The home store's keyspace** is the system store's identity layout
  without the tenancy encodings: no `{tenant}/` wrapping, and no tenant
  segment in `__sys:users/tenant/{tenant}/…`. The store is the tenant.
- **Placement keyed by database id (built).** Placement is keyed
  `__sys:placement/{tenant}/{dbid}` (`placement.PlacementPrefix`). `dbid` is
  an opaque, never-reused id (128 random bits, hex; `database.NewDatabaseID`)
  allocated when the database is created and stored in its catalog record
  (`DatabaseMeta.ID`), so the name ↔ id mapping lives in the tenant's
  catalog. The home group, which has no catalog record, is placed under the
  reserved `database.TenantHomeID`. A node that holds the catalog record
  addresses the entry by id (`placement.Store.Get`); one that does not (a
  follower, the leader serving a forwarded scale) finds it by the name the
  entry's value still carries (`placement.Store.GetByName`). A drop's fence
  never resolves by name: it reads the entry under the catalog record's id
  (`placement.Store.GetBound`), which refuses an entry naming another
  database, since a dropped predecessor's entry can share the name. The key-delivery
  envelope names the database by id (`keydelivery.Meta.DatabaseID`).
  Remaining: the name in the placement value, which these readers need until
  the catalog resolves on every serving node.

### Error semantics

These conditions have a typed error today:

| Condition | Error | REST | gRPC / Unix / QUIC / MCP | Retry? |
|---|---|---|---|---|
| An isolated tenant's home store cannot be reached on this node | `*tenant.TenantHomeUnavailableError{TenantID, Err}`; matches `tenant.ErrTenantSealed` | 503 | `UNAVAILABLE` | yes |
| An identity acts in another tenant on a non-cross permission | `*rbac.PermissionDeniedError` at the RBAC check; `*tenant.CrossTenantDeniedError` at the routing layer | 403 | `PERMISSION_DENIED` | no |
| Loosening the floor or a tenant's policy, or losing a concurrent change | `*tenant.IsolationPolicyChangeError{From, To}` | 409 | `FAILED_PRECONDITION` | no |
| A CSR that does not parse | `*cert.InvalidCSRError` | 400 | `INVALID_ARGUMENT` | no |
| Init proof of possession absent, stale, from another connection or by the wrong key | `*tenant.FirstAdminProofError{TenantID, Reason, Err}` | 401 | `UNAUTHENTICATED` | with a fresh nonce |
| An isolated tenant's barrier is initialized before `per-node-barrier-keys` is activated | `*feature.NotActiveError{Feature: featuregate.PerNodeBarrierKeys}` | 503 | `UNAVAILABLE` | yes, after activation |
| A shard's dedicated WAL cannot be opened | `*database.TenantWALUnavailableError{TenantID, ShardID, Err}`; the shard does not start | 503 | `UNAVAILABLE` | after operator action |

These conditions are required and have no error type yet:

| Condition | REST | gRPC / Unix / QUIC / MCP | Retry? |
|---|---|---|---|
| A terminated connection names a strict tenant this node does not serve (Unix, embedded, or no tenant SNI); the error carries the serving nodes | 421 Misdirected Request + `X-QRDB-Serving-Nodes` | `UNAVAILABLE`, `ErrorInfo` reason `TENANT_NOT_SERVED_HERE`, with the node list | yes, with tenant SNI or on a serving node |
| Certificate tenant differs from the SNI tenant | TLS alert / 421 | `UNAUTHENTICATED` | no |
| The relay cannot reach any serving node | connection closed with a TLS alert; logged on the relay | the same | yes |
| The home group has no quorum, or the tenant is sealed on a quorum | 503 | `UNAVAILABLE` | yes |
| Operator access to identity records in a strict tenant | 403 | `PERMISSION_DENIED` | no |
| Create without a first admin | 400 | `INVALID_ARGUMENT` | no |
| Credential does not match this node's verifier | 403 | `PERMISSION_DENIED` | no |
| Bootstrap record does not match the home store after unseal; the tenant is re-sealed on the node | 503 | `UNAVAILABLE` | after operator action |
| Database replica outside the serving set | 409 | `FAILED_PRECONDITION` | no |
| A strict tenant's database requested without a dedicated WAL | 409 | `FAILED_PRECONDITION` | no |

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

> Phase 7 is delivered as the work packages in
> [../barrier-seal/architecture.md](../barrier-seal/architecture.md#work-packages).
>
> - **Isolated tenants, PN-1 to PN-10.** Phase 7's own scope, through one
>   `BarrierKeyProvider` seam and a signed init statement.
> - **Shared tenants: no package.** The shared Raft group is one more tenant
>   with its own platform-managed barrier; a shared tenant has no keys of its
>   own, and its data is readable by the platform.
> - **The system barrier, PN-12.** Planned: it folds into the same seam,
>   which deletes `node.WithSealStrategy`.
> - **Serving set, PN-13:** the tenant's replica count, a tenant quota set by
>   the system owner or a system admin through the existing quota and
>   entitlement records.
> - **Custody.** A platform-sealed key is the tenant's choice and is not
>   refused under either policy. The statement records the opt-in and the
>   barrier status shows it.
> - **Dependencies.** Phase 7 needs two parts of phase 6: the home store on a
>   dedicated WAL, and the reconciler gating each shard on an unsealed tenant
>   barrier. It does not need opaque database-id placement (S5), and it needs
>   the replicated catalog only for the database-barrier package.
> - **Gates.** The packages add no Raft opcode. They add one activation-based
>   feature, `per-node-barrier-keys` (`featuregate.PerNodeBarrierKeys`),
>   under which an isolated create, or a change to an isolated type, on a
>   cluster is refused until the feature is activated (PN-2, below).
>
> **PN-1.** Tenant barrier init resolves each node's strategy through
> `seal.BarrierKeyProvider`, injected with `server.WithBarrierKeyProvider`
> (also on `sdk/go/node`) and carried through `lifecycle.Infra` to
> `tenant.WithBarrierKeyProvider`; the default provider maps
> the `StrategyRegistry` onto it. See
> [../barrier-seal/engineering.md](../barrier-seal/engineering.md).
>
> **PN-2.** The contract it satisfies is in
> [../barrier-seal/engineering.md](../barrier-seal/engineering.md#per-node-barrier-keys-init-statement):
> `pkg/barrierinit` holds `InitStatement`, `Sign`, `Verify`, `Book` (the
> guarded claim and compare-and-set) and `Watcher`;
> `pkg/server.NewBarrierInitKV` adapts the system store to it through
> `protocol.OpGuarded`; `tenant.WithPerNodeBarrierKeysGate` is the create and
> update gate. Every node composes the `Watcher` (`startBarrierInitWatcher`
> on a clustered node, serving the tenants placed on it;
> `startLocalBarrierInitWatcher` on a standalone node, serving every tenant;
> both initialising through `TenantBarrierSelfInit`), and onboarding pins
> each tenant's first admin as its epoch-1 anchor
> (`bootstrap.Dependencies.BarrierAnchors`). Every client transport signs the
> statement (`barrierinit.SignTenantInit`) and every handler hands it to the
> tenant service (`tenant.WithInitStatement`). The `Book`
> (`tenant.WithBarrierInitBook`) and the gate are composed together on every
> node from one check, `perNodeBarrierKeysRequired`, nil on a standalone
> node; `featuregate.PerNodeBarrierKeys` is in `featuregate.Supported`.
> Pinned by `TestCheckActivated_AdoptsPerNodeBarrierKeysActivation`,
> `TestBuildServices_IsolatedTenantIsGatedOnPerNodeBarrierKeys` and
> `TestBuildServices_StandaloneIsolatedTenantIsNotGated`.
>
> **PN-3, platform custody.** An isolated tenant (`barrier`, `volume` or
> `raft`: each has a home group placed on every node) created with
> `BarrierAutoUnseal` claims the platform's statement
> (`tenant.PlatformInitSigner`, wired by `pkg/server/lifecycle` from the
> node's cluster identity), and every node of its groups self-initialises its
> own key; the create ships no key. A placed node without the barrier
> registers its shell (`TenantService.RegisterTenantShell` through
> `server.ShellRegistrar`). The tenant's IAM, keyring and invitations are
> read from its home group, so every node reads them under its own key.
> Pinned by `TestIsolationRaft_EndToEnd` (assertions b2 and g),
> `TestRegisterTenantShell_*`,
> `TestTenantShardReconciler_RegistersShellForAPlacementWithoutABarrier`
> and `TestBarrierRegistry_ImportTenant_InitializesRegisteredShell`.
> A node without a cluster identity (no CA pool, node certificate or signing
> key) refuses a platform-custody raft tenant and claims nothing for a barrier
> or volume one: `wirePlatformInit` wires no platform signer, a raft create
> then fails with `*tenant.InitStatementRequiredError` and is undone, and a
> barrier or volume create succeeds unclaimed (a standalone node composes no
> signer and needs none). Pinned by
> `TestWirePlatformInit_WiresNothingWithoutItsParts`,
> `TestCreateTenant_ARefusedPlatformClaimUndoesTheCreate` (`no platform
> signer`) and `TestCreateTenant_NoPlatformClaim` (`no platform
> signer/barrier`, `no platform signer/volume`).
> Not yet built: the serving-set check before a shell is registered (needs
> `pkg/placement/serving_set.go`, draft PR #1494).
>
> **PN-4, owner custody under `none` and `passphrase`.** Every node serving
> the barrier initialises its own key, and neither key is shipped:
> `clusterkeys.Sender.DistributeTenantKey` returns without contacting a
> follower for either strategy, which covers each delivery site that routes
> through it (database invites, observer adds and rebalance moves), and
> ships nothing for a barrier not yet initialised. A Shamir key is not
> shipped either: every serving node is armed from the claimed init
> statement's roster and check value and keys itself at its holders' first
> quorum, and `StorageBarrier.ImportSealedBlob` refuses a Shamir blob
> (`seal.ErrImportSealedBlobShamir`), which carries none of the node's
> wrapped-key and roster records and would leave the replica unable either
> to key itself or to unwrap the import. The sender reads the strategy from
> the persisted blob, so a leader still sealed after a restart ships nothing
> too.
>
> - **Serving-node discovery.** `TenantBarrierStatus` lists the nodes holding
>   a replica of the tenant's groups, each with the API endpoints its member
>   record publishes (`tenant.BarrierNode`, sourced by
>   `pkg/server/lifecycle` from the placement table and member records). It
>   reaches clients on every transport and the CLI status table. A
>   standalone node lists none; a list the node cannot read is logged, left
>   out, and reported as `NodesUnreadable` (`nodes_unreadable`) so a client
>   can tell an unknown serving set from a standalone barrier.
> - **Fan-out.** `qrdb tenant barrier-init` under `passphrase` sends the init
>   to every listed node at once, over `--server`'s transport (rest, grpc,
>   quic or mcp), and prints one result per node; each call proves the init
>   on its own connection, and the claim book accepts the re-signed
>   statement as the same init. A `none`, Shamir or hardware init goes to
>   `--server`'s node alone, the other nodes initialising from its claim.
>   `qrdb tenant barrier-unseal` fans out the same way: it reads the listed
>   nodes from `--server`'s barrier status and sends the credential unseal
>   to each of them at once, since each node's own key is sealed under that
>   credential, printing one result per node; a `BarrierFanOutError` names
>   the nodes that refused while the others unseal. A barrier served by one
>   node is unsealed on `--server`'s node. When the status reports
>   `NodesUnreadable`, both `barrier-init` (passphrase) and `barrier-unseal`
>   fail with `TenantServingNodesUnknownError` naming the tenant and send
>   nothing, rather than act on `--server`'s node alone and leave the other
>   replicas sealed.
> - **First-admin window.** Once the pending first admin's record is
>   consumed, a node initialising later admits the certificate whose
>   thumbprint was pinned for the barrier at onboarding
>   (`middleware.FirstAdminPins`); an unreadable pin fails closed.
>
> Pinned by `TestClusterEnableEncryption_TenantScope_RF3` (through the CLI,
> no key delivered and no unseal on the followers),
> `TestSendToServingNodes_*`, `TestTenantBarrierInit_FansOutToServingNodes`,
> `TestTenantBarrierUnseal_*`,
> `TestBarrierNodes_*`, `TestDistributeTenantKey_OwnKeyOnEveryNode_ShipsNothing`,
> `TestDistributeTenantKey_Uninitialized_ShipsNothing`,
> `TestDistributeTenantKey_Shamir_ShipsNothing`,
> `TestBarrierRegistry_ImportTenant_ShamirBlobLeavesTheReplicaToArmShamir` and
> `TestCheckFirstInitCertificate_*`.
>
> - **First-admin completion.** The barrier-ready hook
>   (`lifecycle.Node.completePendingTenantAdmin`) runs
>   `bootstrap.CompletePendingTenantAdminWithin` inside the init, unseal or
>   status request that raised it, before that request answers, on a context
>   detached from the request's cancellation, so the account exists when an
>   init returns. A completion error other than `tenant.ErrTenantSealed`
>   fails the init with that typed error. `Dependencies.HomeLeader` confines
>   the writes to the home group's leader; `AttachTenantHomes` supplies it
>   from `LeadsDatabase(tenant, database.TenantHomeName)`. A node without
>   replicated homes is its tenants' only writer. Elsewhere an attempt
>   returns `*bootstrap.AdminCompletionDeferredError` having written nothing,
>   and the retry keeps checking until the record is gone or the node leads.
>   `tenantadmin.Store.Delete` takes the record it read and deletes with
>   `CompareAndDelete`; a lost delete returns false, nil. The onboarding
>   path (`service.TenantOnboardingService`) stays ungated: its writes are
>   the same guarded ones. Pinned by
>   `TestCompletePendingTenantAdmin_RunsOnceOnTheHomeLeader`,
>   `_ANewLeaderFinishesADeposedOnesWork`, `_LosingTheRaceIsSuccess`,
>   `_OnlyTheHomeLeaderCompletes`, `_AFollowerThatBecomesLeaderFinishes`,
>   `_AccountExistsWhenInitReturns`, `_InitFailsWithTheCompletionError`,
>   `TestBarrierReadyHook_InitFailsWithTheHookError` and
>   `TestKVStore_DeleteLeavesARecordRecordedSinceTheRead`.

Phases 0 to 4 are built, and the list below states what each does. Phases 5
to 12, in the table after it, are the plan.

- **0. Cluster tenancy suite.** The tenant suites run as the `multi-tenancy`
  leg of CI's per-feature `integration-features` job
  (`make integration-test-multi-tenancy`).
  `test/buildgates/feature_jobs_gate_test.go` fails when the job's leg list
  and `test/integration/features.mk` differ.
- **1. Standing-rule defects.** The etcd listener serves only a caller whose
  tenant owns the database (`database.OwnedBy`); any other is refused with
  `etcd.TenantMismatchError`. Audit query and replicated-chain verification
  are confined to the caller's tenant on REST, gRPC, QUIC and MCP
  (`tenant.ConfineAuditFilter`, `tenant.Confine`), and a `tenant_id` naming
  another tenant is refused. Submitted Shamir shares are held in memory only
  and never written to a store. `user.NormalizeRoleBindings` refuses a
  binding in any tenant but the holder's own with `RoleBindingTenantError`.
- **2. Cluster-wide shard ids.** `shard.Claimer` issues every raft database's
  shard id from one replicated high-water mark with a compare-and-swap, so an
  id is issued once in the cluster's lifetime. On a host that gates
  features, a claim is refused with `*feature.NotActiveError` until
  `featuregate.ClusterShardIDs` is active.
- **3. First admin by CSR (S7).** A tenant create carries `admin_username`
  with `admin_csr_pem` or `admin_cert_pem`, on six transports and the CLI.
  System-managed and `prefix` tenants get their admin user, SPKI binding and
  certificate at create; self-managed tenants get a pending first admin,
  consumed at barrier init. Init is two calls, `init/begin` and
  `init/complete`, on REST, gRPC, QUIC, MCP and the Unix socket
  (`pkg/initproof`, `AuthMiddleware.BeginTenantBarrierInit`,
  `AuthMiddleware.AuthorizeTenantBarrierInitProof`). `begin` issues a
  single-use 32-byte nonce bound to the connection's TLS 1.3 exporter for
  five minutes; `complete` carries a signature over the tenant, the nonce and
  that exporter by the key whose SPKI the pending first admin records. The
  SDK signs with a caller-supplied `crypto.Signer`
  (`transport.ProveTenantBarrierInit`), the seam for device-held keys; the
  CLI signs with its `--key-file` key. The verifier half is one type,
  `middleware.TenantBarrierInitVerifier` (`Begin`, `Complete`, over an
  `initproof.Book`): `AuthMiddleware` runs it for every transport, holding
  the proof to the pending first admin's key, and the SDK re-exports it
  (`sdk.TenantBarrierInitVerifier`, `sdk.NewTenantBarrierInitVerifier`,
  `sdk.TenantBarrierInitKey`) for a facade that terminates the caller's TLS
  and so must run the exchange on its own connection; the facade names the
  expected key through `TenantBarrierInitKey` and authorizes the caller
  itself. The embedded transport is in process,
  has no connection to bind, and asks no proof. A replayed nonce, a nonce
  from another connection, an expired challenge and a signature by another
  key are each refused with `FirstAdminProofError`
  (`pkg/auth/middleware/barrier_bootstrap_test.go`, `pkg/initproof`,
  `sdk/go/types_tenant_barrier_init_test.go`);
  `test/integration/tenant_barrier_init_proof_cli_test.go` runs the real CLI
  on every network transport. The pending first-admin record stays in the
  system shard until phase 8.
- **4. Dedicated tenant WAL, fail-closed (S4: G2, G3, G4, G6).** See G2 to G6
  below.

| Phase | Delivers | Red test (fails before, passes after) | Depends on |
|---|---|---|---|
| **5. Isolation policy and RBAC (S4: G1 for databases)** | **Built:** the floor and the tenant policy, tighten-only (`tenant.ClusterIsolationPolicy`, `tenant.TenantIsolationPolicy`; a loosening is refused with `IsolationPolicyChangeError`), the floor set from `tenant.isolation_policy`. The floor tightens from `operator-access` to `strict` in one compare-and-swap, so a lost race is refused and the floor is never unset in between. `ScopeSelf` has no system lift, `EffectiveTenant` has no system branch, and `tenants:read:cross` and `tenants:delete:cross` exist. **Built:** the `strict-isolation` feature (`featuregate.StrictIsolation`), in `featuregate.Supported()`: a standalone node is active from the start, and a cluster once it has activated the feature cluster-wide; a mixed-version cluster never activates it. Until then the isolation-policy verbs, a strict tenant's database create and an isolated tenant's placement are refused with `feature.NotActiveError` (HTTP 503, `Unavailable`) and change nothing. Get and set of the floor, and a tenant update carrying `isolation_policy`, exist on REST, gRPC, QUIC and MCP, in the Go SDK on every transport, and in the CLI (`qrdb cluster-isolation-policy get|set`, `qrdb tenant update --isolation-policy`); an unknown policy value is `InvalidArgument` (HTTP 400). The `tenant.isolation_policy` key predates the verbs and is not behind the gate. **G1 (Built):** every database of a strict tenant (its own policy or the floor strict) is created with a dedicated WAL. A create without one is refused before any quota is reserved with `database.StrictTenantDedicatedWALRequiredError`, `FailedPrecondition` on every transport, naming `--dedicated-wal`. A strict tenant's non-raft database is refused with the same error, naming the backend, since only a raft database can carry a dedicated WAL. The `FeatureDedicatedWAL` entitlement and the `MaxDedicatedWALGroups` quota still apply. `dedicated_wal` is carried by REST, gRPC, QUIC, MCP, the Unix socket, the Go SDK and `qrdb database create --dedicated-wal`. A strict tenant never has a log under a node key. **To build:** tenant-qualified operator permissions to read and write a tenant's identity records, audited in both trails. The policy governs isolated tenants only; a shared tenant is never refused. | Under `operator-access`, the operator's read of a tenant's identity records succeeds and appears in both trails, and a `prefix` create under a strict floor succeeds. **Feature gate (Built):** `TestStrictIsolationRequired_FollowsTheHost` (`pkg/server/lifecycle`: active on a standalone host and on an activated cluster, `NotActiveError` otherwise); `TestTenantIsolation_IsolationPolicyVerbs_StandaloneActive_PerProtocol` (`test/integration`: on a standalone node the floor tightens to strict and reads back, a loosening and an unknown value are refused, and a tenant update stores its policy, on rest, grpc, quic, mcp and unix). **G1 (Built):** `TestCreateForTenant_StrictTenant_*`, `TestCreateForTenant_StrictFloor_SharedWALRefused` and `TestCreateForTenant_NonStrictTenant_SharedWALAllowed` (`pkg/service`); `TestClassify_StrictTenantDedicatedWALRequired` (`pkg/api/errclass`); `TestRaftBackendFactory_StrictTenantDatabase_SharedWALHoldsNoEntry` (`pkg/server`: the shared WAL holds no entry of the dedicated database's shard); `TestStrictTenant_DatabaseNeedsDedicatedWAL_PerProtocol` (`test/integration`: under a strict floor, refused without `--dedicated-wal` and created with it on rest, grpc, quic, mcp and unix). | 3, 4 |
| **6. Tenant home group and replicated catalog (S4: G1 for the home group)** | **Built:** the home group of every isolated tenant, its log on a dedicated WAL from its first entry (see [Remaining work](#remaining-work)). The database catalog is replicated, not node-local: nothing reads `meta.json`, and an isolated tenant's catalog is `__sys:dbcatalog/{t}/{name}` in its home store (see the record table above). Database groups are confined to the serving set, the nodes that host the tenant's home group: `placement.WithServingSet` narrows create and scale-out placement to it (`service.PlacementServingSet`), the replica admission shared by scale-out and a rebalance move refuses a node outside it, and either refusal is `placement.ServingSetError` (FailedPrecondition); a rebalance plan (`CreateRebalancePlan`) proposes no move outside it and names each skipped move with that error as its reason; a shared tenant is not confined. A rebalance never moves a tenant's home group: it spans the cluster, so the plan lists each home-group shard as skipped and a move of one is refused before any mutation, both with `service.HomeGroupMoveError` (FailedPrecondition). **Built:** placement is keyed by the opaque database id from the catalog record, `__sys:placement/{t}/{dbid}` (S5). | The catalog is replicated: `TestDatabaseOwner_OtherNodesForwardToTheOwner` creates a database through one node and `database list` on another names it. Serving-set confinement: `TestTenantServingSet_ThreeNode_CLI_PerProtocol` (create and scale-out on a three-node cluster, a scale-out past the serving set refused, every transport) and `TestRebalanceService_CreateRebalancePlan_HonoursServingSet`; the home group stays put (`TestRebalanceService_CreateRebalancePlan_NeverMovesAHomeGroup`, `TestMoveTenantGroupShard_RejectsHomeGroup`). A scan of the placement keys finds only opaque ids, never a database name (`TestStore_PlacementKeysCarryOnlyTheDatabaseID`). | 2, 5 |
| **7. Per-replica keys (S4: G10)** | The work packages above. **Built:** every serving node initializes its own key from the barrier owner's signed init statement (`featuregate.PerNodeBarrierKeys`), and the CLI sends a tenant barrier init to every serving node. **To build:** delete tenant-key transport (`StorageBarrier.ImportSealedBlob`, `clusterkeys.Sender.DistributeTenantKey`). Client fan-out for unseal and rekey. Verifier delivery through `clusterkeys`. Per-node tenant bootstrap records. Each node's dedicated WALs are keyed by **its own** replica key. Unseal is per node, against the cluster seal record. | Two replicas of one tenant must have distinct key fingerprints. One replica's key must fail to open another replica's store and WAL. A Raft snapshot catch-up across distinct keys must succeed. | 6, cluster-wide sealing record |
| **8. Move the records; delete the shared store paths** | The records [Remaining work](#remaining-work) lists as still in the system shard move into the home store; the SO PIN commitment and the unseal roster go to the bootstrap record. The CA chain is pinned per tenant, and the binding policy is `required` for strict tenants. `tenant.IdentityIndex`, `tenant.OwningTenant` and the resolver's shared-store branch are deleted. | **The residue test** (below) must pass. A cluster-CA certificate naming a strict tenant authenticates today; it must be refused. | 7 |
| **9. Layer-4 passthrough (S6)** | SNI routing and the relay tunnel for REST, gRPC, and the etcd and SQL listeners over TCP; QUIC by Initial-packet SNI and connection id. A typed not-served-here error for Unix and embedded. The SDK and CLI set SNI. On-behalf forwarding is refused for strict tenants. The routing input (SNI) is not yet decided. | A strict tenant's client that connects to a non-serving node is refused as sealed today; it must be served through the relay. On every transport, the serving node must observe the client's own certificate, the relaying node's TLS-termination counter for the connection must be zero, and an SNI/certificate mismatch must be refused. | 8, the routing input decided |
| **10. Tenant audit trail and metrics (S9)** | Every isolated tenant's audit trail is a replicated, hash-chained trail in its home group under the tenant barrier. Platform events stay in the platform trail in the system shard. **Built (cluster mode):** once `AttachTenantHomes` attaches the node's home groups, `service.SystemShardAuditResolver` routes an isolated tenant's chain and logs to its home group; a `barrier` tenant follows its recorded audit route, so a chain already on the system shard stays there. **To build (standalone):** a standalone node attaches no home group, so its tenants' chains are still in the system shard and a `volume` or `raft` tenant is refused with `DedicatedTenantAuditNotSupportedError`. The `namespace` label is removed from the `qrdb_vector_*` metrics. | A tenant event lands in the system shard today; it must land only in the tenant's trail. The platform trail must hold no tenant username, and a `/metrics` scrape must show no tenant data name. | 8 |
| **11. Tenant-only backups (S10, fallback A)** | A tenant-scoped (`ScopeTenantOnly`) backup permission for a strict tenant's groups. Platform shard backup is limited to the system shard. | A platform `backup:create` of a strict tenant's group succeeds today; it must be refused. The tenant's own backup must succeed and restore. | 6 |
| **12. Tenant WAL key rotation (S4: G7)** | Rotation of a dedicated WAL's key epochs, with per-barrier purge floors so that rotating or shredding one tenant never affects another. The floor must also cover what the tenant barrier stores outside the WAL. Until it can, the barrier pins every epoch it has written under, so the purge retires none of them; see [../barrier-seal/engineering.md](../barrier-seal/engineering.md#invariants). | A rotation on tenant A must leave tenant B's WAL readable, and A's pre-rotation epochs must be purged once no live entry needs them. | 7 |

**External dependency — G5 (go-quicraft).** In go-quicraft, one sealed WAL in a
commit batch fails the whole batch. The G4 ordering (stop and unregister a
tenant's shards before its barrier seals) keeps a sealed tenant WAL out of any
batch. When go-quicraft confines that failure to its own shard, the dependency
bump carries a regression test of the batch behaviour itself.

**G4.** `RaftBackendFactory` registers one `OnSealing` observer on
each tenant barrier it starts a shard under (`watchSealLocked`). The observer
runs inside `StorageBarrier.Seal` with the root key still live, so it is on
every seal path: a tenant's own seal, a node seal, `SealAll`. It evicts and
stops every shard the node runs for the tenant (`stopTenantShards`: host stop,
then multiplex unregister and WAL close), closed rather than purged. Another
tenant's shards, and the system shard, are untouched
(`TestRaftBackendFactory_SealTenant_StopsItsShardsOnly`). A
tenant's shards come back through the reconciler once it is unsealed.

**G6 is covered by G4:** the seal stops and evicts the tenant's shards, so no
failed shard stays latched on the host, and the reconciler starts them again
once the tenant is unsealed
(`TestRaftBackendFactory_SealTenant_ShardRestartsAfterUnseal`).

**G2 and G3.** A shard whose placement says `DedicatedWAL` and whose
WAL cannot be opened does not start: `openAndRegisterDedicatedWAL` returns
`*database.TenantWALUnavailableError{TenantID, ShardID, Err}` inside the
factory's `RaftBackendError`, and nothing falls back to the shared WAL. The
causes are no multiplex wired (`database.ErrDedicatedWALNotWired`),
no tenant barrier, a sealed barrier, or an open or register failure.
`database.NewRaftWAL` builds the multiplex on every host that has a system
barrier, single-member hosts and the SDK node included, so the guarantee
holds in every run mode. That includes a system
barrier still in plaintext mode (StrategyNone, before `EnableEncryption`):
there is one code path whether or not the barrier encrypts. The shared waldb
under a plaintext-mode barrier declares `AtRestHostRouted`. go-quicraft accepts
that beside `WithBarrier` because the one route that can hold cleartext writes
through the host's own barrier. It is checked by identity, as the default WAL
under the same barrier is. Its records encrypt from the moment
`EnableEncryption` flips that barrier. A `DedicatedWAL` shard on such a host
starts under its tenant barrier
(`TestRaftBackendFactory_DedicatedWAL_PlaintextModeHost`). Only a host with
seal disabled (no system barrier at all) has no multiplex and refuses a
`DedicatedWAL` shard with `ErrDedicatedWALNotWired`.

**Order and cut points.**
- **Now (phase 1)**, the standing rules hold: no cross-tenant database
  access, and bindings name the holder's own tenant only.
- **After phase 5**, the platform operator can no longer reach into a tenant
  through any API, and no strict tenant has a log under a node key.
- **After phase 8**, a strict tenant's records are out of the system tenant.
- **After phase 9**, every transport reaches a strict tenant from any node
  without that node terminating the session.

Each cut point is a usable release.

### The residue test

The residue test is planned, as `test/integration/tenant_residue_test.go`.
It makes the residue list enforceable rather than descriptive.

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
  non-member node returns the typed not-served-here error before any user
  lookup.
  The user-store double **fails the test if it is called**: a faithful double
  for "must not be reached".
- **SNI parsing.** ClientHello and QUIC Initial SNI parsing is tested against
  recorded handshakes, including malformed, fragmented and absent SNI.
- **Init proof of possession.** The signature must cover the nonce and the TLS
  exporter. A replay on another connection fails, and so does a signature by
  any other key. So do a replay on the same connection, a nonce presented on a
  connection whose exporter differs from the one it was issued for, and a
  challenge past its five-minute lifetime (`pkg/initproof`,
  `pkg/auth/middleware/barrier_bootstrap_test.go`).
- **The bootstrap record.** The MAC binds the record, and a tampered roster
  re-seals the tenant.
- **Credential verifiers.** A wrong passphrase on a first-contact node is
  refused before any key is sealed.

**Integration (`test/integration/`, `make integration-test-multi-tenancy`, a shard of CI's full-tier `integration-features` job):**
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
  carrying the operator's identity-read permission (phase 5) reads a named
  tenant's users. The access appears in both trails, and the same role is
  refused every data-plane verb.
- **Opt-in on an `operator-access` cluster.** A tenant that opts into strict
  refuses that permission from then on.
- **Residue.** The residue test above.

### Remaining work

Measured against the contract above:

- **Home store.** The `tenant.isolation_policy` floor
  (`QRDB_TENANT_ISOLATION_POLICY`), the per-tenant policy, and a home store for every isolated (`barrier`,
  `volume` or `raft`) tenant whatever its policy: the policy decides operator
  verbs, backups and relay, never where a record lives. The resolver routes an
  isolated tenant's users, roles, role bindings, DEK keyring and invitations
  to its home store through one seam (`tenant.HomeStoreSource`); with no home
  source attached it fails closed with `TenantHomeUnavailableError`. In a
  cluster the source is the tenant's home group (`database.TenantHomeName`,
  `lifecycle.TenantHomes`), placed on every node and started at tenant create
  before the first identity record is written; on a standalone node it is a
  store under the tenant's barrier in its data root (`tenant.LocalHomes`).
  Both offer the guarded batch the tenant CA is written with
  (`kvstore.MultiGuardedBatchKVStore`). On a standalone node the tenant
  barrier compares each guard's plaintext and hands the ciphertext it read
  down as the guard, as `CompareAndSwap` does, and `seal.StorageAdapter`
  evaluates the guards and commits the writes in one engine batch under its
  write lock; a backend without batches refuses with
  `seal.ErrGuardedBatchUnsupported` inside
  `kvstore.GuardedBatchUnsupportedError`. In a cluster the home group's log is
  a dedicated WAL under the tenant's key, so a cluster whose system barrier is
  in plaintext mode (protection `none`) cannot create an isolated tenant: the
  home group is refused with `ErrDedicatedWALNotWired` (the phase 4 limit
  above), and the create fails closed.
  Tenant deletion retires the home before the barrier is destroyed (see
  [The seams](#the-seams)). Every resolver a node serves from -- the node's
  own and the daemon's authentication resolver -- gets the same home source
  (`lifecycle.AttachTenantHomes`, `lifecycle.AttachLocalTenantHomes`); one
  left without it would fail the tenant closed on the node that had just
  written its records. The home group, like every dedicated shard, snapshots
  and compacts on the system shard's cadence (`raft.snapshot_entries`,
  `raft.compaction_overhead`), so a replica that falls behind a compacted log
  catches up by snapshot install.
- **Identity index.** An isolated tenant takes no claim in
  `system/__sys:identity_idx/`: `Record` asks the resolver whether the tenant
  is homed (`tenant.HomeLocator`, the same decision `Resolve` routes by) and
  writes nothing for one that is, so its ids are resolved in its own tenant
  only. The entries that stay are those of the system tenant and of shared
  (`prefix`) tenants, whose records are in the system shard: there the claim
  keeps a caller-supplied role id from naming two tenants' records, and the factor service's enrolment path
  refuses an id another tenant holds. Neither crosses a tenant, since
  `ScopeSelf` has no system lift; both go with the index when phase 8 deletes
  it, which waits on phase 7.
- **Tenant CA.** An isolated tenant's CA key and certificate
  (`__sys:cert_ca/{t}/{material,certificate}`), the records of the
  certificates it issues (`__sys:certs/{t}/{serial}`) and the revocation
  markers of those certificates (`__sys:certs_revoked/{serial}`) are written
  to and read from its home store. `DefaultCertProvider` routes by the node's
  resolver (`SetStoreResolver`, wired by `lifecycle.BuildServices`), asking
  `tenant.HomeLocator` the same question the resolver routes by; it
  re-resolves the home store on every signing call, so a rebuilt store is
  followed. The system store keeps only the published CA certificate,
  `__sys:cert_ca_pub/{t}/certificate` (DER), written before the CA first
  signs: a failed publish refuses the signature. Chain verification
  (`CACertPEMs`, `IsTenantIssuer`) reads only the published copy, so a serving
  node verifies an isolated tenant's client without reading its home store.
  Revocation is checked per presented certificate (`IsPeerRevoked`): a
  system-store marker revokes any certificate;
  a certificate the isolated tenant's CA signed is then checked against its
  home store, and an unavailable home fails that certificate closed. A
  certificate naming the tenant that another CA signed is never blocked on
  the home store. Serial lookups (`TenantForSerial`, `RevokeCert`,
  `IsRevoked`) search the caller's home store when the caller is an
  isolated tenant, and the system store otherwise. Tenant deletion reclaims the
  published copy with the tenant's other system-store keys.
- **Proved.** Exact-key probes of the system store for each moved family
  (`cmd/qrdbd` `TestTenantHome_*`); a node in each mode on which a `barrier`,
  a `volume` and (in a cluster) a `raft` tenant, created under an
  operator-access or a strict floor, keep their users and roles only in their
  home stores, across a restart (`TestTenantHome_RecordsLiveOnlyInTheHomeStore`);
  and a three-node cluster on which the
  strict tenant's admin authenticates on every node, before and after a full
  restart (`TestTenant_StrictHome_AuthenticatesOnEveryNode`). In the same
  test a node is stopped while the home group compacts its log past the
  node's last entry; on restart it installs the home group's snapshot and
  serves a user created while it was down.
- **Bound.** Creating an isolated tenant in a cluster waits for the home
  group to elect a leader and commit its ready marker on every node. The
  create runs under the onboarding service's own deadline
  (`service.DefaultTenantOnboardingTimeout`), the same on every transport and
  for every isolated tenant whatever its type or policy.
- **Not yet moved into the home store** (still in the system shard): the
  SO PIN commitment and the unseal roster,
  encryption migration state and journals, and the pending first-admin
  record.
- **Tenant audit trail (phase 10, cluster mode).** On a node with a Raft
  host, `AttachTenantHomes` gives `service.SystemShardAuditResolver` the
  node's home groups, and an entitled `volume` or `raft` tenant's chain and
  logs apply in its home group. A `barrier` tenant's chain follows its audit
  route: a recorded fact under `__sys:audit/route/<tenant>` on the system
  shard, written once (first writer wins) by the proposer of the tenant's
  first event after the attach. The route is `system` when the tenant's chain
  already has records on the system shard, or when
  `featuregate.VerifiableLogAudit` is not yet active on every replica, and
  `home` otherwise. A recorded route never changes and no record is moved, so
  a chain begun on the system shard stays there for writes, Query and Verify.
  A mixed-capability cluster never splits a chain: a home write while the
  gate is off is refused with `AuditHomeNotActiveError` (unavailable). The
  route is picked before proposing; apply is unchanged. Platform events about
  the tenant stay on the system tenant's trail and carry no tenant identity:
  onboarding names no admin username there (the tenant's own trail records
  it), a status change names its caller only if it is a platform principal,
  and a tenant barrier's holder conflict drops the holder's username and
  certificate thumbprint. A standalone node attaches no
  home group: there every tenant's chain is still in the system shard, and a
  `volume` or `raft` tenant's chain is refused with
  `DedicatedTenantAuditNotSupportedError`.
- **Home-group WAL (G1).** `ProvisionTenantHome` places the home
  group `DedicatedWAL`, so its log is written from the first entry to a WAL
  under the tenant's key and its snapshots use the tenant barrier. It is a
  placement, not a catalog database, so it takes no `MaxDedicatedWALGroups`
  quota. The host's shared WAL holds no entry of it
  (`TestRaftBackendFactory_HomeGroup_SharedWALHoldsNoEntry`; the same test shows a
  non-dedicated database does land there). Where a dedicated WAL cannot be
  opened the home group does not start (G2), so an isolated tenant is never
  served from a log under the node key.
- **Per-replica keys (phase 7)** are unblocked for the resolver-routed
  records of every isolated tenant. Every record still listed above keeps them
  blocked.
- **Late-joining nodes.** A node that joins after an isolated tenant was
  created receives the home placement through the reconciler. Until its
  replica has caught up (the shard's join window has closed) the node refuses
  the tenant's requests with `lifecycle.TenantHomeCatchingUpError`, a
  retryable unavailable, so callers go to the other nodes; it neither serves
  its stale replica nor proxies to the leader.
- **Transports.** Get and set of the floor and the tenant policy are not yet
  on any transport or the CLI; the floor is configuration only. Layer-4 passthrough (phase 9) is not started.
