# Barrier and Seal — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-14

## Exported surface

`sdk/go`:

```go
func (c *Client) Barrier()   transport.BarrierClient
func (c *Client) DEK()       transport.DEKClient
func (c *Client) SealStore() transport.SealStoreClient
```

Composition-plane options (Tier 2), on the server constructor:

```go
func WithStrategyRegistry(reg seal.StrategyRegistry) ServerOption   // hardware injection, system and tenant barriers
func WithBarrierKeyProvider(p seal.BarrierKeyProvider) ServerOption // tenant barrier key seam; wins over the registry
func WithPINBackend(b pin.PINBackend) ServerOption
```

and on the embedded node (`sdk/go/node`):

```go
func WithSealStrategy(strategy seal.SealingStrategy) NodeOption     // a pre-built strategy, registered under its ID
```

The system barrier resolves its strategies through a registry, the same seam
tenant barriers use:

```go
// pkg/seal
func NewSystemStrategyRegistry(composer StrategyRegistry, threshold, total int) (*DefaultStrategyRegistry, error)
func ResolveSystemStrategies(registry StrategyRegistry, configured, persisted StrategyID) ([]SealingStrategy, error)
```

`NewSystemStrategyRegistry` holds qrdb's software strategies — passphrase
(deferred), none, and shamir with the configured threshold and total —
overlaid by every strategy the composer's registry holds, which wins on a
shared name. `sdk/go/node` passes the `WithStrategyRegistry` registry as the
composer and registers a `WithSealStrategy` strategy under its own ID; that
strategy is selected unless `seal.strategy` names `shamir` or `none`. A
composer that would rather select by name registers its strategy under the
name `seal.strategy` selects. `qrdbd` composes no hardware strategy and passes
no composer. `ResolveSystemStrategies` builds the selected strategy, which must
resolve, then the one the persisted root key names when it differs (the
`enable-encryption` restart path), best effort. **Correction (2026-10-07):**
`pkg/server` also carried a `WithSealStrategy`, surfaced by
`ResolveCryptoOptions` and consumed by nothing; it is deleted, and the system
barrier no longer selects its strategy with a `switch` that bypassed the
registry.

The SDK node builders forward `WithStrategyRegistry` and
`WithBarrierKeyProvider` into `lifecycle.Infra` before `BuildServices`, because
the tenant service that consumes them is built there.

`pkg/seal`:

```go
type StrategyRegistry interface {
	Register(name StrategyID, factory StrategyFactory) error
	Unregister(name StrategyID)
	LookupFactory(name StrategyID) (StrategyFactory, error)
	ListAvailable() []StrategyID
}
type StrategyFactory func() (SealingStrategy, error)

func NewDefaultStrategyRegistry() StrategyRegistry

// The per-node barrier key seam (architecture.md, "The barrier key seam").
type BarrierKeyProvider interface {
	BarrierStrategy(ctx context.Context, req BarrierKeyRequest) (BarrierKeyStrategy, error)
}
type BarrierKeyRequest struct {
	Barrier    string // clusterseal grammar: "node", "tenant/<t>", "database/<t>/<db>"
	NodeID     string
	StrategyID StrategyID
	KeyRef     string // opaque; never parsed by go-qrdb
	Epoch      uint64
}
type BarrierKeyStrategy struct {
	Strategy   SealingStrategy
	Credential CredentialKind // CredentialNone | CredentialPassphrase | CredentialShares
	Custody    Custody        // CustodyNone | CustodyOwner | CustodyPlatform
}
func NewBarrierKeyProvider(reg StrategyRegistry) BarrierKeyProvider // nil reg: NewDefaultStrategyRegistry()

func NewSoftwareStrategy(passphrase []byte) (*SoftwareStrategy, error)
func NewSoftwareStrategyWithParams(passphrase []byte, time, memory uint32, threads uint8) (*SoftwareStrategy, error) // Argon2id, refused under FIPS
func NewSoftwareStrategyFIPS(passphrase []byte, iterations int) (*SoftwareStrategy, error)                        // PBKDF2-SHA256
func NewDeferredSoftwareStrategy() *SoftwareStrategy
func NewDeferredSoftwareStrategyWithParams(version KDFVersion, params KDFParams) (*SoftwareStrategy, error)
func NewNoneStrategy() SealingStrategy

type KDFVersion = quicraftseal.KDFVersion // KDFArgon2id, KDFPbkdf2SHA256
type KDFParams  = quicraftseal.KDFParams  // read off a strategy with KDFVersion() / KDFParams()

func (b *StorageBarrier) OnUnsealed(fn func(context.Context)) // runs fn on every transition to unsealed
func NewCredentials(secret []byte) (Credentials, error)  // consumes (wipes) secret
func CredentialsFor(secret []byte) (Credentials, error)  // borrows secret; empty -> zero Credentials
func MustCredentials(secret []byte) Credentials           // fixtures

func FIPSEnabled() bool // the stack's single FIPS 140 predicate
```

`FIPSEnabled` is re-exported from `go-quicraft/pkg/seal`. It reads the Go
runtime's FIPS 140-3 module state (`crypto/fips140.Enabled`) first and falls
back to the `GOFIPS140` environment variable for deployments that signal FIPS
intent that way, honouring the explicit value `off` as **disabled**. Every
FIPS-conditional path in qrdb routes through it — SO PIN hashing (`pkg/auth`),
PIN hash configuration (`pkg/pin.AutoDetectHashConfig`) and KDF selection
(`pkg/seal/kdf.DefaultHasher`) — so one environment yields one answer across
the whole process.

Strategy IDs: `seal.StrategyPassphrase`, `seal.StrategyShamir`,
`seal.StrategyNone`, `seal.StrategySystemBarrier`.

`StorageBarrier` (`pkg/seal/barrier.go`) key fields:

For field documentation and the complete struct definition, see the `StorageBarrier` struct in `pkg/seal/barrier.go`. The key fields are:
- `state` (atomic): 0 = sealed, 1 = unsealed
- `activeStrategy` (atomic.Value): StrategyID that sealed the root key
- `persistedStrategyCache` (atomic.Value): cached strategy ID from sealed root key blob
- `accumulator` (atomic.Value): Shamir share accumulator for quorum unsealing
- `initializedAt` (atomic.Value): initialization timestamp
- `strategies` (map, RW-locked): all registered strategies
- `strategiesMu`, `accumulatorMu` (sync.RWMutex, sync.Mutex): synchronization for concurrent access

`pkg/clusterseal` — cluster-wide sealing (see
[architecture.md](architecture.md#sealing-in-a-cluster)):

```go
type Barrier string                         // "node", "tenant/<id>", "database/<tenant>/<db>"
func NodeBarrier() Barrier
func TenantBarrier(tenantID string) Barrier
func DatabaseBarrier(tenantID, database string) Barrier
func ParseBarrier(s string) (Barrier, error)

type Principal struct{ Name, Tenant string } // who wrote a record
func PrincipalFrom(ctx context.Context) Principal

type Record struct {                        // the replicated record; no key material
	Barrier         Barrier
	Epoch           uint64
	Sealed          bool
	Principal       string
	PrincipalTenant string
	Node            string                  // the writing node's raft address
	At              time.Time
}

type Local string                           // serving | sealed | held | cut_off | unconfirmed
type Status struct { Active bool; Record Record; Local Local }
func (s Status) Report() *Report            // the wire form every transport returns
func ReportOf(sealer Sealer, b Barrier) *Report // never nil; inactive when sealer is nil
func ActiveIn(sealer Sealer) bool

type Enforcer interface {                   // composition: seals one barrier on this node
	SealNode(ctx context.Context) error
	SealTenant(ctx context.Context, tenantID string) error
	SealDatabase(ctx context.Context, tenantID, database string) error
	NodeUnsealed() bool
	TenantUnsealed(tenantID string) bool
	DatabaseUnsealed(tenantID, database string) bool
}

type Sealer interface {                     // what the barrier services call
	Active() bool
	Seal(ctx context.Context, b Barrier, by Principal) (Record, error)
	Clear(ctx context.Context, b Barrier, by Principal) (Record, error)
	Status(b Barrier) Status
	Refuses(b Barrier) bool                 // the barrier's record says sealed here
}

type Config struct {
	KV           KV                         // the replicated system store
	Gate         ActivationReader           // nil: never active (standalone, or no transport)
	Enforcer     Enforcer
	Self         string                     // this node's replica ID; it acknowledges under it
	Node         string                     // its raft address, written into records; defaults to Self
	Members      func(ctx context.Context) ([]string, error) // system-shard replica IDs
	Lease        time.Duration              // default 15s
	PollInterval time.Duration              // default 1s; at most a third of Lease
	AckTimeout   time.Duration              // default 5s
	Clock        Clock
	Logger       *slog.Logger
	Streams      *Streams                   // nil builds one; a Deferred composer passes Deferred.Streams()
}

func NewMonitor(cfg Config) (*Monitor, error)    // implements Sealer; Run polls, Refusing gates
func (m *Monitor) Run(ctx context.Context)
func (m *Monitor) Refusing() bool                // node gate: lease expired, unconfirmed, or node held
func (m *Monitor) Streams() *Streams             // the stream-ending registry the transports resolve through

type Deferred struct{ /* ... */ }               // a Sealer bound to its Monitor after the services exist
func NewDeferred() *Deferred
func (d *Deferred) Set(m *Monitor)               // until Set, every seal is node-local
func (d *Deferred) Streams() *Streams
func (s *Streams) SubscribeServing(tenantID, database string, refuses func() error) (*Subscription, error) // subscribe, THEN ask refuses
func (s *Streams) GuardWatcher(tenantID, database string, refuses func() error, open func() (watch.Watcher, error)) (watch.Watcher, error) // SubscribeServing, then open; a seal ends it; Close releases
```

`pkg/server` composes it:

```go
type ClusterSealParts struct {               // what a composer hands NewClusterSealMonitor
	KV          service.KVServicer
	Gate        *featuregate.Gate
	Barriers    *seal.BarrierRegistry
	Shards      *RaftBackendFactory          // tenant raft shards, stopped before a tenant seal
	Hosts       *TenantHostRegistry          // tenant volume hosts, likewise
	Databases   service.DatabaseServicer     // SealDatabaseLocal seals a database here
	Membership  featuregate.ShardMembershipReader
	SystemShard uint64
	ReplicaID   uint64
	RaftAddress string
	Timing      config.ClusterSealTiming
	Streams     *clusterseal.Streams
	Logger      *slog.Logger
}
func NewClusterSealMonitor(p ClusterSealParts) (*clusterseal.Monitor, error)
func WithClusterSeal(cs ClusterSeal) ServerOption
```

`Monitor` is constructed by the composer (`cmd/qrdbd`, `sdk/go/node`) over the
system KV and the feature gate, and handed to the barrier, tenant and database
services and to the server, which wraps the node's sealed-window probe with
`Refusing` and every resolved watch with `Streams`.

## Invariants

- **A barrier credential is a wipeable byte slice up to the service boundary,
  and protected memory from there on.** The transport request field, the
  protobuf field and the service-layer parameter are all `[]byte`, never
  `string`, because a Go string is immutable and therefore cannot be zeroized.
  `seal.Credentials.Secret` is a `*enclave.SealedEnclave` (go-enclave, as of
  go-quicraft v0.11.0-alpha): the service copies the borrowed slice into a
  sealed enclave with `seal.CredentialsFor`, and the strategy that needs the
  passphrase opens that enclave itself for exactly as long as its KDF runs.
  Nothing between the decoder and the KDF holds the plaintext as a `[]byte`.

  The ownership rule that makes the types useful:

  - **The callee borrows the slice and retains nothing.** `BarrierServicer`
    and `TenantServicer` read it for the duration of the call, build a
    credential from a copy, and destroy that credential when the call returns.
    `InitializeShamir` opens the credential into a working copy of its own
    that `SealAndDistribute` wipes. Nothing keeps a reference past the call,
    which is what makes the caller free to wipe.
  - **The owner of the slice is whoever decoded it, and it wipes on every exit
    path.** Each protocol handler `defer secure.Wipe`s the slice its decoder
    produced, so a rejected unseal leaves no plaintext behind either.
  - **The owner of a credential is whoever built it, and it destroys it.**
    `creds.Secret.Destroy()` (nil-safe) once the seal or unseal it was built
    for has returned. Nothing downstream destroys it, because a credential
    passed through two layers would otherwise be released by the first to
    finish with it.
  - **The exception is `enable-encryption`, and it is deliberate.** That
    migration runs in a goroutine that OUTLIVES the request, so ownership of
    the credential passes to the goroutine and it is destroyed there; the
    decoded request slice is wiped by the handler the moment the credential
    exists. A handler-level destroy would pull the credential out from under a
    running migration.
  - **A test double must therefore snapshot the plaintext DURING the call**
    (`pkg/seal/sealtest.Snapshot`). A double that stores the credential is
    holding a borrow, and it will find a destroyed enclave once the handler
    returns — correctly.
  - **A wipe does not reach the encoders.** It destroys the slice and its
    aliases, not the transient copy serialization makes — base64 in a REST
    body, protobuf's out-of-place marshalling, the MCP JSON-RPC body. Those go
    unreferenced and are collected, not zeroized, and nothing at this layer can
    change that: serializing means copying and the wire format owns the copy.
    The guarantee is that the caller's copy and the daemon's decoded copy are
    destroyable on demand, not that no plaintext exists anywhere.

  MCP is the one transport that cannot honour this end to end: tool arguments
  are JSON strings, so `pkg/api/mcp.secretArg` and
  `sdk/go/transport/mcp.secretArg` are the single documented conversion points
  on each side. The residue is one string per call and goes no further.

  The rule does **not** yet reach SO/User PINs, which are still text strings
  on every transport. See
  [api.md](api.md#known-limitation--share-and-pin-fields-are-still-text-strings).
  Quorum submissions carry the share as bytes and every transport wipes it.

- **The default registry holds exactly two strategies**: `StrategyPassphrase`
  (via `NewDeferredSoftwareStrategy`, so the passphrase arrives at
  Initialize/Unseal time rather than at construction) and `StrategyNone`.
  **`StrategyShamir` is deliberately NOT in it** — it needs a threshold and
  share count only the node's configuration can supply, so
  `NewSystemStrategyRegistry` adds it, with those parameters, for the system
  barrier.
- **The two software constructors have OPPOSITE credential semantics, and the
  difference is invisible in the type.** Both return a `SoftwareStrategy`.
  `NewSoftwareStrategy` binds its passphrase at construction and **ignores the
  credentials passed to `SealRootKey`/`UnsealRootKey`** — an unseal with the
  wrong secret returns the root key and a nil error. `NewDeferredSoftwareStrategy`
  derives from `Credentials.Secret` on every call and refuses a wrong secret.
  Only the deferred constructor authenticates the caller that unseals the
  barrier, which is why the default registry and `pkg/database` use it and why
  `ChangePassword` installs one.

  Two consequences worth stating separately, because each has already cost an
  incident:

  - **Replacing a registered strategy can silently change whether the barrier
    authenticates anyone**, with no signature, type or error to notice. A
    passphrase-rotation feature built on a bound strategy converts a barrier
    that checks credentials into one that accepts any passphrase for the
    lifetime of the process.
  - **A wrong-credential assertion cannot be written against a bound fixture,
    and a success-only assertion proves nothing against either.** These are two
    different failures and conflating them leads to deleting the wrong test.
    Against a bound strategy a *rejection* assertion goes **red** — the strategy
    accepts the wrong secret — so the test fails over its own fixture rather
    than over the code it meant to cover; the rejection test is the right test,
    and every test in this repo that asserts a wrong secret is refused must
    build its fixture with the deferred constructor. The **vacuous** shape is
    the opposite one: a test asserting only that the root key comes back passes
    with either constructor, which is how a whole suite stayed green over a
    barrier that checked nothing.
- **A barrier's KDF work factor is preserved across a password change, and
  that is correctness, not hardening.** A sealed root-key blob records the KDF
  *version* byte and never the params, so a blob sealed at a non-default factor
  can only be unsealed by a strategy carrying that same factor — the live
  strategy object is the only place the number exists. `ChangePassword` reads
  the version and params off the strategy it is replacing (`KDFVersion()`,
  `KDFParams()`) and builds the replacement with
  `NewDeferredSoftwareStrategyWithParams`, which is deferred (it still
  authenticates the caller) and tuned (it still opens the blob). The
  no-argument deferred constructor reset the factor to the library defaults on
  every password change, silently: the blob it wrote could be opened only by a
  default-factor strategy, so a restart that constructed the deployment's
  tuned strategy from its configuration could not unseal it — whichever
  direction the tuning went — and a deployment tuned above the defaults was
  also weakened on the credential that wraps the root key. The version
  travels with the params
  so the FIPS leg cannot be got wrong: a PBKDF2 strategy stays PBKDF2 and an
  Argon2id one stays Argon2id, and a version the build or runtime policy
  refuses is refused by the constructor. A strategy of another type registered
  under `StrategyPassphrase` has no parameters to preserve and is refused with
  `ErrPassphraseStrategyNotSoftware` rather than re-sealed under defaults.
- **Multiple registered strategies require an explicit choice.**
  `ErrNoStrategySpecified` otherwise. Exactly one registered is selected
  unconditionally.
- **Sealed refuses everything.** `ErrBarrierSealed` for every data-path read and
  write until unsealed. Only the sealed-window allowlist -- the unseal call,
  barrier status, and the first-boot verbs while enrollment is open -- is
  reachable. Barrier status is `ListenerOnly`: the sealed path answers it
  with `sealed` and `initialized` alone, the flag the barrier caches at boot
  (`seal.StorageBarrier.InitializedCached`, primed by
  `server.NewSealedWindowListenerProbe`), so it reads no store and discloses
  no strategy, share count or seal record; the regular gate of a held or
  cut-off node refuses it. Health, node status and ceremony status are
  refused with the typed sealed refusal (`sealedwindow.RefuseSealed`: domain `qrdb.v1`, reason
  `SEALED`), which clients read as up-and-sealed. The quorum likewise runs
  only the unseal and recovery rounds while sealed (`QuorumOp.RunsSealed`):
  a root token minted while sealed would grant past the seal. The allowlist
  is served anonymously on every listener: pkg/server hands each regular listener the
  node's one listener probe (`WithSealedWindowDispatch` on REST, gRPC, Unix,
  QUIC and MCP), keyed on the node barrier alone, and while it reports sealed
  a request takes the boot-time listener's chain instead of authentication,
  so nothing reads the user store behind the sealed barrier
  ([seal/sealed-window.md](../seal/sealed-window.md#auth-model-during-the-sealed-window)).
  Sealed refusal also includes what was opened before the seal: on the SQL wires every statement of
  an open session, every Execute of a statement prepared while the node served
  (PostgreSQL `57P03`) and MySQL `COM_INIT_DB`, and the session itself, whose
  connection the seal closes (a MySQL server-side cursor and an open
  transaction end with it); on every transport every open watch, which the
  seal ends.
- **Sealing the node barrier seals the whole node.** Every system-barrier seal
  -- `BarrierSeal`, a completed share-holder seal quorum (`service.WithBarrierRegistry`
  wires the registry into `BarrierService`), and `SealEnforcer.SealNode` for a
  cluster record -- and the node's own shutdown and failed-boot paths, go
  through the one whole-node seal, `service.SealWholeNode`: every database
  barrier (`database.Manager.SealBarrierDatabases`, which seals the database
  barrier manager's own registry through `DatabaseBarrierManager.SealAll` --
  `BarrierRegistry.SealTenants` -- and marks the databases sealed), then every
  tenant barrier and the node barrier (`BarrierRegistry.SealAll`), then every
  stream ends. There is no second node-seal path to drift from it. Each
  registry is an `OnUnsealed` observer of the node barrier and unseals, when it
  unseals, only the system-managed (`StrategySystemBarrier`) barriers its last
  node seal moved from unsealed to sealed; no database barrier is
  system-managed today, so a passphrase or Shamir database stays sealed for its
  owner or quorum. The database registry's gate
  (`DatabaseBarrierManager.SetUnsealGate`, wired by lifecycle) vetoes a
  database whose record is sealed or whose owner this node cannot name;
  `DatabaseBarrierManager.UnsealRefused` (`BarrierRegistry.TenantUnsealRefused`
  for tenants) reports the gate's verdict for one barrier.
  Closing the database manager (`database.Manager.Close`) wipes every
  database barrier before it closes the databases, so no close -- shutdown,
  a failed boot, a data-plane teardown -- leaves a database key in memory; it
  is the one owner of that wipe, and every path that drops the manager closes
  it. `SealTenant` drops a tenant from that set, and
  `WithTenantUnsealGate` (wired by lifecycle to the cluster sealer's `Refuses`)
  vetoes one whose record is sealed or unreadable. One registry mutex
  serializes `SealAll` against the observer and against the registry's own
  tenant seal and unseal (`SealTenant`, `UnsealTenant`,
  `SubmitTenantHolderShare` for an unseal), each of which does its bookkeeping and the
  barrier transition as one step: a node seal never finds a tenant mid-change
  and records the wrong side of it. A failed re-unseal stays pending
  (`PendingTenantUnseals`, `DatabaseBarrierManager.PendingUnseals`) until the
  next node unseal or the barrier's own unseal. The database barrier manager's
  registry keeps the same discipline: an owner seal or a record's enforcement
  (`SealAuthorized`) drops a database from the set, and an owner or quorum
  unseal (`UnsealDatabase`, `DatabaseBarrierManager.SubmitHolderShare`) is
  ordered against the node seal. A self-managed tenant is never unsealed for it. A cut-off seals
  nothing.
- **A seal quorum seals through its caller.** The share-holder quorum engine
  knows nothing of the cluster: `StorageBarrier.SubmitHolderShareWith` takes
  the seal a completed `OpSeal` round runs, and the `OpSeal` finisher only
  calls it (`SubmitHolderShare` passes `Seal`). Each service passes its own
  cluster seal -- `BarrierService.seal` for the node barrier,
  `TenantService.sealTenant` (through `BarrierRegistry.SubmitTenantHolderShare`)
  for a tenant, `DatabaseService.sealOwnedDatabase` for a database -- so the
  record is written before any key is wiped, and a record refused outright
  (`SealEpochConflictError`) wipes nothing. A tenant's unseal submission runs
  under the registry's mutex, ordered against the node seal; a seal
  submission does not, because its seal takes that mutex itself. A Shamir
  database barrier refuses a single caller's seal
  (`DatabaseBarrierManager.RequiresQuorum`), while the authorized seal a
  quorum or a seal record drives (`SealAuthorized`) seals it on every node.
- **Zero hardware coupling is a hard invariant.** No TPM2 code, no PKCS#11 code,
  no `pkcs11` build tag in any qrdb Go file.
- **Storage wrapping is conditional on `base`.** With a base backend set, the
  barrier transparently encrypts `Put` and decrypts `Get`; with `base` nil it is
  pure crypto (the go-quicraft shape used for the Raft WAL).
- **PIN dual control**: `--so-pin` requires `--user-pin` and vice versa
  (FIPS 140-3 / PCI-DSS v4). PINs are rejected entirely under
  `--barrier-strategy=none` with a typed `PINsNotAllowedError`.
- **Per-node key independence holds for the node barrier only.** Each node
  holds its own node-barrier key. A tenant root key is re-wrapped under the
  *receiving* node's node barrier, but the root key itself is the same on every
  replica of a raft tenant, so a node whose key and disk are both compromised
  exposes the tenant key used on the other replicas. **Correction
  (2026-10-01):** this line previously said a compromised node key does not
  expose tenant keys elsewhere.
- **A seal record carries no key material.** `clusterseal.Record` has no field
  that can hold a key, a share, a credential or an envelope, and a unit test
  pins its encoded field set.
- **A record is written only by a guarded write.** Create-if-absent for the
  first epoch, compare-and-swap against the exact bytes read for every later
  one; the epoch only increases. A lost race is `SealEpochConflictError`,
  except that a seal racing another seal, or a clear racing another clear, is
  idempotent: the loser re-reads, finds the state it wanted, and succeeds.
- **The receiving node writes the record before it seals itself.** If the write
  cannot be confirmed, the node still seals itself — sealing is the safe
  direction — and returns `RecordNotConfirmedError`.
- **Enforcement order is fixed:** close the gate, end streams, stop the
  barrier's shards, wipe the key. It is idempotent and keyed by epoch, so the
  monitor never enforces one epoch twice.
- **Every node seals on a seal, even one it missed.** Seal and clear records
  alternate, one epoch apart. A node that finds a clear record two or more
  epochs past the last it applied missed the seal in between -- it was cut
  off, or the seal and the clear fell between two reads -- and wipes the key
  then, with no acknowledgement round; its owner unseals it as every other
  node was unsealed. The first read after a boot is exempt: that boot's keys
  came from its own unseal.
- **A node-barrier wipe waits for every system-shard member's acknowledgement,
  bounded by `seal.cluster_ack_timeout`.** The gate is closed and streams are
  ended before the wait.
- **A node serves a barrier only when its record is clear and it has unsealed
  the barrier itself.** A tenant or database unseal that cannot write its clear
  record seals the barrier again before it returns.
- **No tenant runtime starts before this node has read its record.** A
  system-managed tenant's barrier is unsealed at boot, before the monitor
  exists -- the monitor seals through the tenant registries, which are built
  first. So a node that installs a monitor starts no tenant runtime at boot
  (`TenantInfraConfig.ClusterSealer` set): the tenant shard reconciler, which
  the composer starts only after `Deferred.Set`, and first access both consult
  the record and start the runtime then. The reconciler holds a dedicated
  shard while its tenant's record or its database's record is sealed. Until
  its first read the monitor refuses every barrier.
- **No lease, no service.** With `cluster-seal` active, the node gate refuses
  every request outside the sealed-window allowlist until the monitor's first
  successful read, and again whenever `seal.cluster_lease` passes without one.
  Keys stay in memory; a successful read resumes service with no re-unseal.
  A node whose feature gate has not yet read the cluster's activations
  (`featuregate.Gate.ActivationsLoaded`) refuses the same way: it cannot yet
  tell an inactive feature from one it has not learned of. Both composers
  read them before they serve: `cmd/qrdbd` before its listeners start,
  `sdk/go/node` in `startClusterSeal` before it installs the monitor (both
  through `server.WhileSystemShardSettling`, a startup read whose attempts
  are each bounded by `server.StartupReadAttemptTimeout`, so a read stranded
  on a gone leader is retried rather than held); a cluster that has activated a feature the
  binary lacks fails the boot.
- **Activation opens no window.** `Monitor.poll` reads and publishes the
  records, and renews the lease, before `cluster-seal` is activated too; it
  enforces nothing and cuts nothing off until it is. No record can exist
  before activation, so the read is accurate, and the lease is already fresh
  when the feature turns on: a cluster with no seal record never refuses for
  the activation. The first read made while active is the one that applies
  the records. The first-read-after-boot exemption (a node barrier found
  sealed is held, not wiped) covers only the monitor's first read made once
  the gate has loaded the activations: a node that was reading before
  activation unsealed before any seal, so a node seal it reads afterwards is
  enforced. A monitor with no gate can never activate and reads nothing.
- **One gate, every transport.** REST, gRPC, Unix, QUIC, MCP and the
  in-process embedded transport derive their sealed-window allowlists from
  `sealedwindow.Table` (the embedded column is `Names.Embedded`, its derived set
  `sealedwindow.EmbeddedAllowlist`) and decide with `sealedwindow.Decide`. The
  embedded transport takes the node's probe through `embedded.WithSealProbe`
  (`server.NodeSealProbe`: the node barrier, plus the cluster seal when there
  is one), gates every request method before reading its arguments, and
  refuses with `*embedded.BarrierSealedError` (`errors.Is(err,
  seal.ErrSealed)`). The embedded transport exists only once the node has
  unsealed, so enrollment is closed and it refuses the first-boot verbs
  (`sealedwindow.EmbeddedPreOperationalOnly`) while sealed; it needs no
  enrollment probe. Every gate samples its probe with `sealedwindow.Sample`:
  a probe that implements `sealedwindow.StateProbe` -- `server.NodeSealProbe`
  does -- reports a cut-off or unconfirmed node apart from a sealed one, and
  `Decide` answers it `RefuseUnconfirmed`, which each transport maps to its
  `SEAL_STATE_UNCONFIRMED` form (the embedded one to
  `*clusterseal.SealStateUnconfirmedError`), never to its sealed refusal. An
  unconfirmed node is not sealed, but like a held node it answers no status
  question: barrier status, a `ListenerOnly` verb, is refused on its regular
  listener as unconfirmed, and only the unseal call is served. `Connect`, `Close` and `WithDatabase` are lifecycle, not
  requests; a database-scoped transport inherits the gate.
  `TestSealGate_RefusesEveryRequestOutsideTheAllowlist` walks the method set
  by reflection, so a request method added without the gate fails. The node's
  feature gate advertises through its own ungated in-process client, as
  `cmd/qrdbd`'s does; what keeps those writes off a sealed log is the next
  rule, not the gate. The per-database etcd listener, which has no
  sealed-window middleware, refuses every request and every Watch or
  LeaseKeepAlive open while the node refuses (`Server.nodeRefusal`): the node
  barrier's `*clusterseal.SealedError` when held or sealed,
  `*clusterseal.SealStateUnconfirmedError` when cut off or unconfirmed.
- **The node proposes nothing on its own behalf while its barrier is sealed.**
  A sealed node barrier means the node's encrypted log cannot take an entry,
  and an entry proposed into it halts the shard. The node's own background
  writers -- the feature-gate advertisement, the member record, the dynamic
  membership manager, the volume-pool node report, and the lease-expiry
  sweep -- run through ungated
  in-process clients, so each consults the node barrier itself (both
  composers pass it in: `cmd/qrdbd` the `StorageBarrier`, `sdk/go/node` the
  `Node`). A `cluster.Advertiser` composed with `WithAdvertiseSealProbe`
  skips its keep-alive while sealed, refuses a registration with
  `*cluster.NodeSealedError` (the publishers retry it after the unseal), and
  leaves its record to its lease rather than withdrawing it; a
  `MembershipManager` composed with `WithMembershipSealProbe` does the same.
  A `volume.Reporter` composed with `WithReporterSealProbe` skips its
  five-second refresh while sealed and refuses `Report` with
  `*volume.NodeSealedError`; its report rides the held lease and is renewed by
  the first refresh after the unseal.
  `Lessor.RunExpiry` composed with `WithExpiryHold` proposes no revoke while
  sealed, and on the unseal gives every lease a full TTL before it sweeps
  again, so no record is revoked for keep-alives the sealed node could not
  take. The probe is the node barrier alone, not `server.NodeSealProbe`: a
  node whose cluster-seal lease has lapsed still holds its keys and its log
  still takes entries, and its records must stay alive for it to be found
  again. The check and the proposal are
  not atomic, so a seal landing between them still sends one entry to the
  sealed log; that the log refuses such an entry without halting the shard is
  the consensus library's side of the contract.
- **Until `cluster-seal` is activated, sealing is node-local** and every
  status reports `active: false`. No record is written and none is enforced;
  the monitor still reads them, to hold a fresh lease when the feature turns
  on.
- **Seal authority does not cross tenants.** Node barrier: `system`-tenant
  identities only. Tenant barrier: that tenant's identities only — the
  `system` tenant is refused. Database barrier: the owning tenant only.
- **The CA key is never a plain `[]byte` or `*ecdsa.PrivateKey`; it is reached
  only through an opaque, barrier-checked `crypto.Signer`.** Both Key A and
  Key B are `cakey.SoftwareProvider` instances now; neither `pkg/clusterca`
  nor `pkg/cert` holds key material in a field of its own. `Signer` cannot be
  type-asserted back to the concrete key and re-checks the barrier's sealed
  state and the provider's own `Seal` generation on every `Sign` call, so a
  `Signer` obtained before a `Seal()` refuses afterward even though the
  caller is still holding it. The stored record itself carries no additional
  barrier encryption (the owner's CA-at-rest ruling) — the barrier only gates
  whether this process will touch the key at all. The decrypted key is held
  in memory only until the next `Seal()`, which zero-fills it with
  `secure.Wipe` (a plain overwrite, not a go-enclave locked-memory enclave)
  and drops the reference. After sealing, every `Signer`/`Certificate` call
  returns `BarrierSealedError`.
- **Reads of protected key material hold the borrow across the read**, not just
  across the accessor. A tenant DEK served from cache and the recovery
  strategy's root key are both copied inside one coordinated borrow
  (`secure.CopyOut`), and both **fail closed** when the enclave yields nothing
  rather than handing back zeros of the right length.
- **A read whose material crosses into an injected interface is the exception,
  and is marked as one.** `EncryptionBarrier.Encrypt`, `SealingStrategy` unseal
  and the ceremony's barrier unseal are supplied by consumers, and any
  conforming implementation may allocate protected memory of its own. Under
  go-enclave that allocation no longer deadlocks a borrow held across the call
  (the go-enclave migration removed the process-wide lock that made it), but
  `pkg/secure` still forbids reaching consumer code from inside a borrow. Those
  sites take a heap copy with `secure.CopyOut` and wipe it as soon as the call
  returns — go-enclave offers no accessor that reads without a borrow — and are
  covered by a regression test that fails if a borrow is held across the call.
- **The rule is checked, not just written down.** `secure.GuardBorrows`-armed
  tests drive each converted path and report a nested borrow as a test failure
  naming both sites, instead of a process that stops with no diagnostic.
- **Nodes never exchange shares.** Custody is per node; a Shamir barrier opens
  only through its own authenticated holder quorum.

## Dual-mode operation

### Dual-Mode Operation

| Mode | Strategy Type | Encrypt/Decrypt | Key Rotation |
|------|--------------|-----------------|--------------|
| **Software / Shamir** | `SoftwareStrategy`, `ShamirStrategy` | Epoch-based HKDF DEKs via embedded barrier | `Rotate()` generates new epoch DEK |
| **None** | `NoneStrategy` | Passthrough (no encryption) | n/a |
| **Hardware** (xkms-registered) | TPM2, PKCS#11, cloud KMS, Vault | Delegates to `BarrierEncryptor` from strategy | Hardware manages key lifecycle |


## `enable-encryption` — moving off `none`

`barrier enable-encryption` migrates a barrier initialized with strategy `none`
to a keyed strategy, **re-encrypting at-rest data in place**.

| Flag / field | Values | Default |
|---|---|---|
| `--strategy` | `passphrase`, `shamir` | required |
| `--mechanism` | `swap`, `in-place` | `swap` |
| `--availability` | `quiesced`, `online` | `quiesced` |
| `--tenant` | tenant ID | system barrier |

The migration runs **asynchronously on the server**: a system-scope migration
recycles the node's data plane in process, so the call returns once the
migration has *started*. Poll `barrier status` until the strategy changes from
`none` to the chosen strategy.

The migration has no time budget: every transport runs it on a context with no
deadline or cancellation, and it derives the new key once when it mints the
target barrier and again when the reopened barrier unseals, so with a
passphrase its wall time follows the host's load. A poller bounds the wait by
its own deadline, never by a fixed window. A migration that fails after it was
accepted reopens the data plane under the source strategy and logs the error;
`barrier status` then reports the strategy still `none`, the same answer it
gave before the migration began, so a remote poller cannot tell a failed
migration from an unfinished one and only its deadline ends that wait. An
embedder holding the `Node` can: the recycle publishes a new `Node.Services()`
graph whether the migration committed or fell back, so a graph other than the
one read before the call means the migration is over, and the strategy it then
reports is the outcome.

The recycle stops every background worker of the old data plane before it
closes that plane's storage — the lease and reclaim sweepers and the
volume-pool reporter, whose refresh proposes through the old MVCC and lease
services — and the rebuild starts new ones over the new services. A worker
left running proposes into closed storage; the engine then fails the apply
with `storage is closed` and reports that the lessor could not be rebuilt.

On a cluster, a `qrdbd` node that leads the system shard hands that
leadership to a peer before it stops anything (`handOffLeadership`,
`cmd/qrdbd/leader_handoff.go`), and closes its Raft host only once another
replica leads or `leaderHandoffTimeout` (5s) passes. It picks the voter it has
heard from within the election timeout that holds the most of its log; when it
has heard from none -- the peers are down for their own migration -- it skips
the transfer. A leader that closed without handing off left every peer's
system-shard reads forwarded to a replica that no longer answered until they
elected one themselves, and those reads are the TLS handshake's trust
resolution and each request's revocation check: the peers stopped serving
authenticated requests for that window. A handoff that fails or times out is
logged at WARN and the recycle proceeds; it costs only the election it was
meant to spare.

A tenant on the **shared** system barrier cannot change its encryption; the
server rejects the request. `system-barrier` and `none` are the two valid
enable-encryption source strategies.

The data-plane backend does not decide whether a node can migrate: the migrated
root key is written to the node's **seal store**, a dedicated store the seal
subsystem opens beside the data plane whatever that backend is, so a node on
the memory backend migrates exactly as one on Pebble does. A node that cannot
migrate at all — seal not enabled, not yet unsealed, no seal store, or (for
the embedded node) boot parameters not retained — refuses **before** it tears
anything down, with the same refusal every transport gives when no migrator is
wired: `service.ErrMigrationProviderUnavailable`, `501` / `Unimplemented`. The
embedded node's `MigrationNotSupportedError` carries the reason and matches
that sentinel through `errors.Is`. A failure after the node has accepted the
migration is a different error, `MigrationReopenError`, and is not reported as
"not available".

### A tenant-scope migration right after a system-scope recycle

`server.scopedEncryptionMigrator` routes a `ScopeTenant` request through two
reads against the live tenant service before it picks a mechanism: the
entitlement gate (`CheckBarrierMode`, governing which strategies the tenant
may migrate to) and the isolation lookup (`isRaftTenant`'s `GetTenant`,
routing `IsolationRaft` tenants to the per-shard mechanism instead of the
per-tenant volume Host). Both are linearizable reads against the tenant
service's backing raft host.

A system-scope migration's recycle (`nodeMigrationProvider.Reopen` →
`completeStandaloneBoot`) closes and reopens that host, so its first leader
election afterward has a brief `quicraft.ErrReadIndexNotReady` warm-up window
before its first entry of the term commits. A tenant-scope migration that
lands in that window — immediately after a system-scope one, as a client
polling `barrier status` to completion and then issuing the tenant request
would — must not be refused or misrouted because of it. Both reads retry
through `whenReadIndexReady` (`pkg/server/tenant_residue.go`), the same
helper the tenant-reconcile and list-tenants paths use and `ImportCommit`'s
`syncReadRetrying` mirrors: only `quicraft.ErrReadIndexNotReady` is retried,
up to a 15-second deadline, every other error returns immediately.

### In a cluster, every node supplies its own credential

The replicated `__sys:encmig/` intent record carries the target strategy, the
mechanism, the Shamir M/N, the owner set and the generation — **and no
credential**, not even an encrypted one. Each owning node obtains its own
unlock credential locally:

| Node | Credential source |
|---|---|
| The node that accepted the request (the leader) | the request's passphrase, held in protected memory for this node's own recycle and destroyed after it; never written anywhere |
| Every other owner | the file `<seal.migration_credentials_dir>/system` (node barrier) or `<seal.migration_credentials_dir>/tenant-<id>` (a tenant barrier), read when its recycle starts and wiped after use |
| Any owner, Shamir target | none: each node mints its own key and seals a fresh share set to its own enrolled officers |

An owner whose credential file is absent records phase `awaiting-credential`
and retries on every reconciler pass; it is not counted as a failed attempt.
The directory must be a real directory (not a symlink) owned by the daemon's
user and writable by no one else. It is opened once, `O_RDONLY|O_DIRECTORY|
O_NOFOLLOW|O_CLOEXEC`, and vetted on that descriptor; the file is opened
relative to it (`openat`), so a directory moved away and replaced at the same
path after the vetting is never the one read. The file is opened `O_RDONLY|
O_NOFOLLOW|O_NONBLOCK|O_CLOEXEC` and checked on the open descriptor: a regular file,
mode `0600` or stricter, owned by the daemon's user, non-empty and at most
64 KiB. Anything else is refused (`clustermig.MigrationCredentialFileError`,
naming the directory or the file); off unix, where the file cannot be opened
without following a symlink, every credential file is refused. The trimmed
newline is wiped with the rest of the buffer; a refused file also
leaves the owner in `awaiting-credential`, with the reason as the status
detail, so fixing the file is enough. One trailing newline is not part of the
credential. A tenant ID that is not a plain file name is refused.

```go
// pkg/clustermig
type CredentialSource interface {
	Credential(ctx context.Context, m *ClusterMigration) ([]byte, error) // any error: awaiting-credential
	Release(m *ClusterMigration)                                           // the generation completed or was abandoned
}
type LocalCredentials struct{ /* ... */ }                                // the source every node runs
func NewLocalCredentials(dir string) *LocalCredentials                   // dir = seal.migration_credentials_dir
func (l *LocalCredentials) Hold(m *ClusterMigration, credential []byte) error // the accepting node's request credential, copied to protected memory
const PhaseAwaitingCredential seal.MigrationPhase = "awaiting-credential"
var ErrCredentialAwaited error
func NewCoordinator(store *Store, owners OwnerResolver, isLeader func() bool, nowUnix func() int64) *Coordinator
func (c *Coordinator) Request(ctx context.Context, tenantID, database string, opts seal.MigrationOptions, ropts ...RequestOption) (*ClusterMigration, error)
type Prepare func(m *ClusterMigration) (undo func(), err error)
func WithPrepare(p Prepare) RequestOption // the accepting node Holds its credential before the record is written
```

Earlier releases sealed the credential to each owner with HPKE and stored the
envelopes in the record; that path is gone, together with the per-node
migration receiver.

**An indeterminate record write leaves the accepting node awaiting its
credential.** The accepting node holds the request's credential through
`WithPrepare` before the record is written, and the prepare's undo releases
it -- destroying the protected copy -- when `PutMigration` returns an error.
A write can fail and still commit: a proposal that times out, or whose
leader is lost after it was appended, may be applied afterwards. The undo has
run by then, so the record exists while this node holds no credential for its
generation. That node reports `awaiting-credential` like an owner without its
file, and is satisfied the same way -- the credential file in
`seal.migration_credentials_dir`, or a new request, whose generation
supersedes the stranded one. No other node is affected, and nothing proceeds
on a credential this node no longer holds. The request's caller saw an error
and, retrying, issues exactly that new request.

> **Correction to `seal/README.md`.** That document states the strategy is fixed
> at initialization and that moving off `none` is "a planned backup/restore
> migration into a freshly-keyed instance". `barrier enable-encryption` has
> since shipped and performs an in-place re-encryption. The code is correct and
> the older prose is stale.

## Per-node barrier keys: init statement

The design is [architecture.md](architecture.md#per-node-barrier-keys). The
shipped surface, `pkg/barrierinit`:

```go
type InitStatement struct {
	Barrier           clusterseal.Barrier
	Epoch             uint64 // 1, then +1 per re-key or strategy change
	StrategyID        seal.StrategyID
	KeyRef            string // opaque; read only by an injected strategy
	Threshold         int    // Shamir only
	RosterHash        []byte // Shamir only
	VerifierSalt      []byte // passphrase only
	UnsealKeyCheck    []byte // Shamir only
	SignerSPKI        []byte // DER SubjectPublicKeyInfo of the signing key
	SignerCertificate []byte // DER; epoch 2 and later
	Signature         []byte
}
func (s InitStatement) Message() []byte
func Sign(signer crypto.Signer, s InitStatement) (InitStatement, error)
func Verify(s InitStatement, a Anchors) error

type Anchors struct {
	FirstAdmin string         // user.PublicKeySPKIThumbprint of the pending first admin
	CA         *x509.CertPool // the owner's CA, pinned in the system tenant
	At         time.Time      // certificate validity time; zero is now
}

const Prefix = "__sys:cluster/barrier/init/"
type KV interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	PutIf(ctx context.Context, key string, expected, value []byte) (bool, error)
	Scan(ctx context.Context, prefix string) (map[string][]byte, error)
}
func NewBook(kv KV, required func() error) *Book // nil gates nothing
func (b *Book) Claim(ctx context.Context, s InitStatement, a Anchors) error   // epoch 1
func (b *Book) Advance(ctx context.Context, s InitStatement, a Anchors) error // epoch n+1
func (b *Book) Versions(ctx context.Context, barrier clusterseal.Barrier) ([]InitStatement, error)

// Serves func(ctx, clusterseal.Barrier) (bool, error): an error is retried on the next poll
func NewWatcher(cfg WatcherConfig) (*Watcher, error) // KV, Anchors, Serves, Handle required
func (w *Watcher) Run(ctx context.Context) error
func (w *Watcher) Poll(ctx context.Context) error

// The wire form a client sends beside an init request.
const MaxEncodedLength = 16 * MaxFieldLength
func (s InitStatement) Marshal() ([]byte, error)
func Parse(raw []byte) (InitStatement, error) // *InitStatementError (ReasonMalformed); no signature check
type Expect struct{ Barrier clusterseal.Barrier; Epoch uint64; StrategyID seal.StrategyID; KeyRef string }
func (s InitStatement) Matches(e Expect) error // ReasonMismatch

// The epoch-1 anchor: the first admin's SPKI thumbprint, pinned at onboarding.
func NewPins(kv KV) *Pins
func (p *Pins) PinFirstAdmin(ctx context.Context, b clusterseal.Barrier, thumbprint string) error // *AnchorConflictError for another admin
func (p *Pins) FirstAdmin(ctx context.Context, b clusterseal.Barrier) (string, error)
func (p *Pins) Anchors(ctx context.Context, b clusterseal.Barrier) (Anchors, error)

// The tenant service's claim of a statement before an init.
type AnchorSource func(ctx context.Context, b clusterseal.Barrier) (Anchors, error)
func NewClaimer(book *Book, anchors AnchorSource) *Claimer
func (c *Claimer) ClaimTenantInit(ctx context.Context, tenantID string, strategyID seal.StrategyID, keyRef string, raw []byte) error
```

The pins live under `__sys:cluster/barrier/anchor/<barrier>` through the same
`KV`, pinned with a guarded create-if-absent: a retry naming the same admin
succeeds, another admin gets `*AnchorConflictError` (`AlreadyExists`).
`bootstrap.Dependencies.BarrierAnchors` (`bootstrap.FirstAdminPinner`) is where
onboarding pins the pending first admin of an isolated tenant whose identity
records are behind its barrier.

`pkg/tenant` cannot import `pkg/barrierinit` (`barrierinit` imports `user`,
which imports `tenant`), so the tenant seam carries the encoded statement:
`tenant.BarrierInitBook` is the `ClaimTenantInit` method,
`tenant.WithBarrierInitBook` injects it and `tenant.WithInitStatement` passes
a request's statement to `TenantBarrierInitWithStrategy`. With a book
injected, an init claims the statement before this node registers or
initializes anything, serialised per tenant; an IsolationRaft init with no
statement is refused with `*tenant.InitStatementRequiredError`
(`InvalidArgument`). `TenantBarrierSelfInit` is the watcher's delivery: it
resolves the strategy through the `BarrierKeyProvider`, initializes this
node's key when the resolved strategy needs no credential, treats an
already-initialized barrier as done, and leaves a Shamir barrier or one that
needs a credential for its owner's own init or unseal.

`pkg/server/lifecycle` composes `BarrierInit` (`newBarrierInit`): the system
store adapter and the pins. Onboarding pins through it, and
a `Watcher` runs on every node with `TenantBarrierSelfInit` as its `Handle`.
On a clustered node `startBarrierInitWatcher` starts it with `Serves` "a
tenant barrier placed on this node" (an unreadable placement table is an
error, retried); on a standalone node `BuildServices` starts it through
`startLocalBarrierInitWatcher`, serving every tenant barrier, since the node
is the only one.

`pkg/server.NewBarrierInitKV(service.KVServicer) barrierinit.KV` is the store
adapter: `PutIf` is a `service.WriteGuard` comparing the key's value to
`expected` (an absent key compares equal to the empty value), proposed as
`protocol.OpGuarded`; a refused guard is `(false, nil)`.

The activation check `NewBook` and `tenant.WithPerNodeBarrierKeysGate` take
returns the refusal itself: the composer builds the
`*featuregate.NotActiveError`, so neither package imports `pkg/cluster` and a
lean standalone build does not compile cluster code through them.

`featuregate.PerNodeBarrierKeys` is in the supported set, so every binary
advertises it and adopts its activation record. `pkg/server/lifecycle` builds
the check once per node (`perNodeBarrierKeysRequired`): nil on a host that is
not a `service.FeatureGatedHost` — a standalone node, which gates nothing —
and otherwise a function that returns
`*featuregate.NotActiveError{Feature: per-node-barrier-keys}` until
`FeatureEnabled(0, PerNodeBarrierKeys)` holds cluster-wide. The same check is
passed to `tenant.WithPerNodeBarrierKeysGate` and to the book, so neither an
isolated tenant nor a statement is admitted before every member can
self-initialise. Pinned by
`TestCheckActivated_AdoptsPerNodeBarrierKeysActivation`,
`TestGate_PerNodeBarrierKeysInactiveWithoutRecord`,
`TestPerNodeBarrierKeysRequired_FollowsTheGate`,
`TestPerNodeBarrierKeysRequired_NilOnAHostThatGatesNothing`,
`TestBarrierInitClaimer_RecordsNothingBeforeActivation`,
`TestBuildServices_IsolatedTenantIsGatedOnPerNodeBarrierKeys` and
`TestBuildServices_StandaloneIsolatedTenantIsNotGated`.

**Invariants.**

- `Message` is injective: a fixed context string, then every field but
  `Signature` length-prefixed (uint32 big-endian) or fixed-width (uint64
  big-endian). Changing any covered field invalidates the signature.
- `Verify` checks, in order: every field present and within
  `MaxFieldLength` (16 KiB); the SPKI parses; the signer is the anchor's
  (epoch 1: its thumbprint equals `Anchors.FirstAdmin`; later: the
  certificate chains to `Anchors.CA` for client authentication at
  `Anchors.At` and its SPKI is byte-equal to `SignerSPKI`); the signature
  verifies (`mtls.VerifyNonceSignature`: ECDSA and RSA over SHA-256, Ed25519).
- `Claim` and `Advance` verify before they write, so an unverified statement
  is never stored. Both are idempotent for the stored statement, compared by
  `Message` alone: an ECDSA signer signs the same statement differently on
  every call, so a retry that re-signs it still matches, and both sides have
  already verified.
- A record decodes only to the barrier's dense epochs from 1; anything else is
  `*CorruptRecordError` and is never delivered.
- The watcher holds a per-barrier high-water mark of delivered epochs and
  calls `Handle` only for a newer epoch that verified. `Handle` must be
  idempotent.
- No custody refusal: a statement naming a platform-sealed strategy verifies
  like any other.

### Composition into a node

```go
// pkg/barrierinit
const AnchorPrefix = "__sys:cluster/barrier/anchor/"
func NewPins(kv KV) *Pins
func (p *Pins) PinFirstAdmin(ctx context.Context, b clusterseal.Barrier, thumbprint string) error
func (p *Pins) FirstAdmin(ctx context.Context, b clusterseal.Barrier) (string, error)
func (p *Pins) Anchors(ctx context.Context, b clusterseal.Barrier) (Anchors, error) // an AnchorSource

func NewClaimer(book *Book, anchors AnchorSource) *Claimer
func (c *Claimer) ClaimTenantInit(ctx context.Context, tenantID string, strategyID seal.StrategyID, keyRef string, raw []byte) error
// TenantInit returns the latest claimed statement for tenantID's barrier,
// verified against its anchors; *NoInitStatementError when none is recorded.
func (c *Claimer) TenantInit(ctx context.Context, tenantID string) (InitStatement, error)

// SignTenantInit is what every client transport calls: the encoded epoch-1
// statement for tenantID's barrier, or nil for a nil signer.
func SignTenantInit(signer crypto.Signer, tenantID, strategy string) ([]byte, error)

// pkg/tenant
func WithBarrierInitBook(book BarrierInitBook) TenantServiceOption // *barrierinit.Claimer
func WithInitStatement(raw []byte) TenantBarrierInitOption
func (s *TenantService) TenantBarrierSelfInit(ctx context.Context, tenantID string, strategyID seal.StrategyID, keyRef string) error
// BarrierInitBook.TenantInit reports the strategy, key reference and epoch of
// a barrier's verified statement (found=false when none is recorded); a tenant
// barrier unseal resolves exactly that strategy through the key provider.
type BarrierInitTerms struct { StrategyID seal.StrategyID; KeyRef string; Epoch uint64 }

// pkg/seal
// UnsealWithStrategy opens the root key with strategy alone, never the
// barrier's registrations; a strategy whose ID is not the sealed one is
// *UnsealStrategyMismatchError (errors.Is ErrStrategyMismatch).
func (b *StorageBarrier) UnsealWithStrategy(ctx context.Context, strategy SealingStrategy, creds Credentials) error
func (r *BarrierRegistry) UnsealTenantWithStrategy(ctx context.Context, tenantID string, strategy SealingStrategy, creds Credentials) error

// pkg/bootstrap
type FirstAdminPinner interface { // *barrierinit.Pins
	PinFirstAdmin(ctx context.Context, b clusterseal.Barrier, thumbprint string) error
}
// Dependencies.BarrierAnchors, service.TenantOnboardingDeps.BarrierAnchors
```

`pkg/server/lifecycle` builds one `BarrierInit` per node over the system store
(`Node.BarrierInit`, nil exactly when `Node.Tenant` is): onboarding pins
through its `Pins`, and `ComposeRaftTenantCluster` — on a
node with dedicated Raft groups — starts a `Watcher` whose `Serves` asks the
placement table whether a group of the barrier's tenant has a replica on this
node (`placement.Placement.ReplicaForNode`, keyed by Raft address) and whose
`Handle` dispatches on `clusterseal.Kind`; only `KindTenant` is handled, any
other is `*lifecycle.UnsupportedBarrierKindError` and retried. On a
standalone node (`*backend.LocalBackend` host) `BuildServices` starts the same
`Watcher` with `Serves` true for every tenant barrier. The watcher is stopped
with the node. The tenant service is composed with the book as its
`Claimer` — `tenant.WithBarrierInitBook(barrierInit.claimer(required))` —
beside `tenant.WithPerNodeBarrierKeysGate(required)`, both taking the same
`perNodeBarrierKeysRequired(host)` check, so a statement a transport carries
reaches `WithInitStatement` and is claimed before the node generates its own
key.

**Invariants.**

- The anchor is pinned before the tenant create returns, and has no undo: an
  undone provisioning deletes the tenant, whose ID is retired.
- A tenant barrier init with a statement claims it before this node generates
  a key. The statement must name exactly the barrier, epoch 1, the strategy
  and the key reference the request resolved (`ReasonMismatch` otherwise),
  then verify against the pinned anchor; a refused claim leaves nothing
  initialised.
- `IsolationRaft` without a statement is `*tenant.InitStatementRequiredError`
  when a book is composed; other isolation types may init without one.
- `TenantBarrierSelfInit` initialises only a strategy with
  `seal.CredentialNone`; Shamir and credential-bearing strategies return nil
  untouched. An already-initialised barrier is nil, so redelivery is
  harmless. It serialises with the owner's init on the same per-tenant lock,
  and it runs no barrier-ready hook.
- The pending-admin completion (the tenant service's barrier-ready hook,
  `bootstrap.CompletePendingTenantAdmin` composed in `pkg/server/lifecycle`) runs
  as the caller on `context.WithoutCancel` of the caller's context with its own
  15-second deadline, so its replicated writes are not cut short by the
  request's.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Barrier sealed | `ErrBarrierSealed` on every data-path read and write | **yes** |
| Several strategies registered, none named | `ErrNoStrategySpecified` | **yes** |
| Unknown strategy name | registry lookup error | **yes** |
| `--so-pin` without `--user-pin` (or vice versa) | refused — dual control | **yes** |
| Any PIN with `--barrier-strategy=none` | `PINsNotAllowedError` | **yes** |
| CA operation while sealed | `CAKeyNotAvailableError` | **yes** |
| `enable-encryption` on a tenant using the shared system barrier | rejected | **yes** |
| `enable-encryption` on a node that cannot migrate (see above) | refused before any teardown, `ErrMigrationProviderUnavailable` | **yes** |
| tenant `enable-encryption` while a system migration is rebuilding the data plane | `DataPlaneRecyclingError`, `503` / `Unavailable`; retry | **yes** |
| `enable-encryption` queued on an embedded node when `Close` begins | refused, not queued: `service.ErrNodeClosed`, `503` / `Unavailable` | **yes** |
| Wrong passphrase on unseal | refused; barrier stays sealed | **yes** |
| Seal record not confirmed | the receiving node is sealed; `RecordNotConfirmedError` | **yes** |
| Tenant or database unseal whose clear record cannot be written | the barrier is sealed again; `RecordNotConfirmedError` | **yes** |
| Two seal/clear requests race | the loser re-reads; opposite intent → `SealEpochConflictError` | **yes** |
| Cluster unseal on a node that has not unsealed itself | `ErrSealed` | **yes** |
| Cluster unseal before `cluster-seal` is activated | `ErrInactive` | **yes** |
| Init statement malformed, unanchored, by a key other than the owner's, or with a bad signature | `*barrierinit.InitStatementError{Reason}`; nothing stored or delivered | **yes** |
| Two epoch-1 claims race | one is stored; the other `*BarrierAlreadyInitializedError` | **yes** |
| Statement epoch does not follow the recorded one, or lost a race | `*EpochConflictError{Epoch, Current}` | **yes** |
| Init record does not decode to the barrier's dense epochs | `*barrierinit.CorruptRecordError`; that barrier is skipped, the rest delivered | **yes** |
| Init record write before `guarded-writes` is active | `*GuardedWritesUnavailableError`, never an unguarded write | **yes** |
| Statement names another barrier, epoch, strategy or key reference than the init it came with | `*InitStatementError{Reason: ReasonMismatch}`; nothing claimed or initialised | **yes** |
| Raft-isolated tenant barrier init without a statement, on a tenant service composed with an init book | `*tenant.InitStatementRequiredError`; nothing initialised | **yes** |
| First admin pinned again with another thumbprint | `*barrierinit.AnchorConflictError`; the pin is unchanged | **yes** |
| Tenant barrier unseal after a restart whose root key is sealed with another strategy than its statement names | `*seal.UnsealStrategyMismatchError`; stays sealed, no same-ID system strategy tried | **yes** |
| Raft-isolated tenant barrier unseal with no recorded statement | `*tenant.InitStatementMissingError`; stays sealed | **yes** |
| Statement record for a barrier level the node cannot yet self-initialise | `*lifecycle.UnsupportedBarrierKindError`; retried on the next poll | **yes** |
| Isolated tenant create or update, or a statement recorded, before `per-node-barrier-keys` is active | `*featuregate.NotActiveError{Feature: per-node-barrier-keys}`; nothing written. A standalone node never raises it | **yes** |
| Node lease expired, or no read since start | node gate refuses as `SEAL_STATE_UNCONFIRMED`, never `SEALED` (`sealedwindow.StateProbe`, `TestClusterSeal_LapsedLeaseIsUnconfirmedNotSealed`); streams end with `SealStateUnconfirmedError`; keys kept | **yes** |
| Enforcement step fails on a node (shard close, key wipe) | logged and retried on the next poll; the gate stays closed | **yes** |
| Platform operator seals or unseals a tenant's barrier | `403` (`tenant_barrier:*` are tenant-only) and `TenantScopeError` in the service | **yes** |
| A tenant seals another tenant's database, or the `system` tenant a tenant's | refused as not found (`databases:*` seal permissions are owner-only) | **yes** |
| Migration owner without its credential file | phase `awaiting-credential`, retried | **yes** |
| Request's record write fails but commits (timeout, lost leader) | the accepting node's held credential was released by the undo; that node reports `awaiting-credential` until its file is placed or a new request supersedes the generation | **yes** |

## Permission model

| Permission | Gates | Scope |
|---|---|---|
| `seal:bootstrap` | initialize, and root-token generation | cross |
| `seal:unseal` / `seal:seal` | unseal / seal | cross |
| `seal:status` | status, including recovery status | self |
| `seal:rekey` | rotating the unseal key and share set | cross |
| `seal:rotate_root_key` | root key rotation | cross |
| `seal:enable_encryption` | migrating off `none` (node or a named tenant) | cross |
| `seal:initialize_shamir` | Shamir M-of-N initialization | cross |
| `seal:generate_recovery` | recovery key generation and deletion | cross |
| `seal:officer_register` / `seal:officer_list` | share holder enrolment (unseal and recovery domains) and listing | cross |
| `seal:officer_revoke` | share holder revocation (a quorum-gated rekey) | cross |

**Cross** is `ScopeCross` (`pkg/permission/node_barrier_scope.go`): only a
`system`-tenant identity may exercise it. These routes name no tenant, and
`ScopeSelf` compares the caller's tenant with the request's -- for a request
naming none, with itself -- so as `ScopeSelf` they admitted any tenant's holder.
That let a tenant's SO register holders into the node barrier's unseal and
recovery domains; since every rekey, revocation and recovery-key generation
sends a share to every registered holder, M planted holders could
reconstruct the node unseal key. The six-transport proof is
`TestNodeOfficerScope` (REST, gRPC, Unix, QUIC, MCP, and the embedded
`RBACClient`); the per-permission proof over the real built-in roles is
`TestNodeBarrier_TenantRoleHoldersAreDenied` in `pkg/auth/rbac`, and
`TestNodeBarrierRoutes_TenantHolderRefused` in `pkg/api/rest` pins each
lifecycle route to its permission. The boot-time sealed-window listener runs
no RBAC, so anonymous pre-operational holder registration is unaffected
(`TestSealedWindow_OfficerEnrollmentOpenBeforeInitialization`).

Share claims and quorum submissions are **identity-gated** rather than
permission-gated: a claim proves possession of the enrolled certificate key
over the holder's own connection, and a submission carries a factor proof
checked against the roster. Unseal, seal, root token, recovery and rekey
completion on a Shamir barrier are quorum operations, not permissions. See
[../shamir-custody/api.md](../shamir-custody/api.md).

`seal:unseal` also gates **cluster unseal** (`/v1/barrier/cluster-unseal`),
which is on the sealed-window allowlist.

**Tenant and database barriers.** The `tenant_barrier:*` permissions are
`ScopeTenantOnly`: the `system` tenant is refused for another tenant's barrier,
and `TenantService` repeats the refusal for its seal and unseal
(`enforceTenantOwner`, no system-tenant lift). The tenant holder quorum keeps
`enforceTenantScope` (the system tenant is admitted) for quorum begin and
submit: every share is verified against the tenant's holder roster
(`HolderVerifier.VerifyHolder`), so the system tenant can open a round but only
the tenant's holders can complete one. `databases:seal`, `databases:unseal` and `databases:barrier_status` are
`ScopeSelfStrict`, and `DatabaseService` resolves the database through the
caller's own tenant for these three operations, the `system` tenant included.
**Correction (2026-10-01):** these three were `ScopeSelf`, which the
system-tenant rule lifts across tenants, and `DatabaseService` let the `system`
tenant act on any tenant's database.

## Configuration

```yaml
seal:
  enabled: true          # false (the default) runs with no barrier at all
  strategy: passphrase   # passphrase | none | shamir
```

Cluster sealing and cluster enable-encryption add:

```yaml
seal:
  cluster_lease: 15s           # serve only while the seal records were read within this
  cluster_poll_interval: 1s    # how often the monitor reads the records; at most a third of the lease
  cluster_ack_timeout: 5s      # node-barrier seal: how long to wait for every node's acknowledgement
  migration_credentials_dir: "" # this node's own enable-encryption credentials (see above)
```

All three durations must be positive; `cluster_lease` at most 60s, because a
cut-off node serves until its lease runs out; `cluster_poll_interval` at most
a third of `cluster_lease`; and `cluster_ack_timeout` at most half of it. A
config that breaks any of these is refused at load with a
`config.ValidationError` naming the field.

Per-database and per-tenant barrier configuration lives in
[../databases/](../databases/engineering.md) and
[../multi-tenancy/](../multi-tenancy/engineering.md).

## Persistence

- The sealed root-key blob records the strategy that sealed it
  (`persistedStrategyCache`).
- Each tenant's Key B CA (key and certificate together, as one JSON record
  with no additional barrier encryption of its own — the owner's
  CA-at-rest ruling) is stored at `__sys:cert_ca/{tenantID}/material`.
- **Compatibility**: the sealed blob format carries a version sentinel — see
  [seal/sealed-window.md](../seal/sealed-window.md).
- **Cluster seal records** live in the replicated system store under
  `__sys:cluster/seal/records/<barrier>`, and node-barrier acknowledgements
  under `__sys:cluster/seal/acks/<node>`. Records are never deleted: a barrier's
  epoch must keep increasing across database re-creation. Creating a database
  clears the record a dropped predecessor of the same name and owner left
  sealed, so the new database starts from a clear record and its first seal
  takes the next epoch; if that clear cannot be written the create is rolled
  back. Another tenant's record for the same name is a different barrier and is
  never touched. A tenant ID is retired on delete and never reissued, so a
  tenant record has no successor.
- **A database seal record seals only its owner's database.** The node
  enforcer seals the database the record names for the tenant it names; a
  database another tenant holds under that name is left alone. A database with
  no barrier of its own is never recorded sealed.

### A per-tenant barrier has durable records beyond the sealed blob

Two records outlive a `BarrierRegistry.UnregisterTenant`, and they exist so
that a node can bring a dedicated-isolation tenant back up after a restart:

| Record | Written by | Read by |
|---|---|---|
| `__registry:tenant_root/{id}` | `RegisterVolumeTenant` | `RestoreVolumeTenants` at boot |
| `__sys:volumepool/assign/{id}` | `recordSlotAssignment` | `resolveTenantPoolBase` on the next provision |

**`UnregisterTenant` is therefore not a delete.** It seals and forgets the
in-memory barrier and leaves both records in place, so a tenant "removed" that
way is re-registered at the next boot, at the same `DataRoot`, over the same
on-disk store. `BarrierRegistry.DestroyTenant` is the operation a tenant
deletion needs: it releases the barrier and closes the per-tenant Pebble
(`ReleaseTenant`), removes the `DataRoot`, and only then removes both records
(`DropTenantRecords`), **in that order** — the files are released before the
directory holding them is unlinked, and a removal that fails leaves the
records, so a re-issue still finds the directory. (This section previously
listed the records before the directory; the code removes the directory
first.)

The `DataRoot` is removed by `RemoveDataRoot`, contents first and then the
root. A dedicated `DataRoot` is often a mount point, whose rmdir fails with
`EBUSY` or `EXDEV` however empty it is; an emptied root that fails that way
counts as removed, and the mount is left to the operator. The filesystem is
reached through `DirectoryRemover` (`WithDirectoryRemover`).

`DestroyTenant` is idempotent; a tenant that was never registered on this node
returns `ErrTenantNotFound`, which the tenant teardown treats as success. Its
failures are typed — `*TenantStoreCloseError`, `*TenantRecordDeleteError`,
`*TenantDataRootRemoveError` — and each stops the sequence rather than being
logged, because leaving either record behind resurrects the barrier, and
dropping them before the directory is gone would orphan it.

See [../multi-tenancy/architecture.md](../multi-tenancy/architecture.md) for
where it sits in the tenant teardown order.

## Reading a passphrase from standard input

`secure.StdinProvider` (re-exported from `sdk/go/secure`) reads one line
straight into an mlocked buffer, never through a `bufio.Reader`: that would
stage the passphrase in heap memory nothing wipes, and it would consume bytes
past the newline that belong to the stream's next reader. The rest of the
stream is always left where it was.

The number of `read(2)` calls depends on the stream. When standard input is a
regular file — redirected from one — the line is read in bulk and the bytes
past the newline are sought back, so a line costs one or two reads; the offset
the seek reports must be exactly one byte past the newline, or the read fails.
Anything else — a pipe, a terminal or another character device, even one that
accepts a seek — is read one byte per call, because it cannot give bytes back
and any larger read may take bytes past the newline.
Lines longer than `MaxStdinPassphraseLen` (1024 bytes) are refused, not
truncated, on either path. A stream that answers 100 reads in a row with no
bytes and no error is broken, and the read fails with `io.ErrNoProgress`, as
`bufio` does, rather than spinning.

## Secure-memory rules, and what enforces them

Key material in this repository is held through `pkg/secure`, a facade over a
process-wide mlocking allocator. Three rules protect that boundary, and they
are enforced by a `go vet` plugin under `pkg/secure/analyzer/` that `make
secure-analyzer` runs in CI's Lint job over **every Go module in the tree**
under **every combination in the Makefile's `SUPPORTED_TAG_SETS`**.

| # | Rule | Scope |
|---|---|---|
| 1 | No raw `memguard` import anywhere (go-enclave is the protected-memory library; `pkg/secure` is its facade) | Every file, tests included |
| 2 | No `ssh.ParsePrivateKey`; use `ParsePrivateKeyWithPassphrase` on Buffer-backed bytes | Every file, tests included |
| 3 | No struct field named `Passphrase`, `Password`, `PIN`, `Secret`, `PrivateKey`, `KeyMaterial` or `RecoveryShare` typed `string` or `[]byte` | Scoped — see below |

There is no rule about reading a `secure.Buffer` without a borrow, because it
cannot be written: go-enclave scopes every borrow to a callback, so
`WithBytes` and `secure.CopyOut` are the only ways to read a buffer and the
compiler rejects anything else. A read whose material must cross an interface
a consumer implements takes a `CopyOut` copy, which the lifetime analyzer
below then requires to be wiped.

Both the sweep and its module coverage matter. `./...` does not cross a module
boundary, so `sdk/go` needs its own pass; and a single tag combination reports
on the files that combination compiles and says nothing about the others.

### Borrowing, liveness, comparison and the empty value

`secure.Buffer` (locked working copy) and `secure.Secret` (sealed at rest)
share one vocabulary, and `sdk/go/secure` re-exports all of it:

| Need | `Buffer` | `Secret` |
|---|---|---|
| Read the bytes for one call, never holding them | `WithBytes(fn)` | `WithBytes(ctx, fn)`: opens, calls `fn`, destroys the opened Buffer |
| Tell a live value from a destroyed one | `IsAlive()` | `IsAlive()` |
| Compare without leaking where the contents differ | `Equal(*Buffer)`, `EqualBytes([]byte)` | open it, then the Buffer's |
| Hold a value of zero bytes | `NewEmptyBuffer()` | `NewEmptySecret()` |

- **The borrow is scoped.** The callback's slice is a view of locked pages;
  it must not outlive the call, and nothing inside it may call back into
  `pkg/secure` (the borrow guard reports it). A caller whose signature forces a
  `[]byte` takes a `CopyOut` copy and wipes it.
- **`IsAlive` is the liveness test, not `Size`.** A destroyed value and a live
  empty one both have a `Size` of 0. `IsAlive` is true from construction until
  `Destroy` and false for nil, zero-value and destroyed values, and for a
  Buffer whose pages a shutdown `WipeAll` took. A live `Secret` can still be
  unopenable after that sweep wipes the process sealer: liveness is about the
  value, not about whether `Open` will succeed.
- **Comparisons are constant time.** Both use `crypto/subtle`, with every
  borrow held across the comparison, so neither operand leaves locked memory.
  The timing reveals whether the lengths differ and nothing else. A nil,
  destroyed or zero-value operand returns `*NilBufferError` rather than
  `false`, so "not equal" and "nothing to compare" stay distinct.
- **The empty value is asked for by name.** `NewBuffer`, `NewBufferFromBytes`
  and `NewSecret` still refuse a zero length with `*InvalidSizeError`, so a
  size computation that came out zero cannot quietly produce one. The empty
  value maps no pages, admits nothing against the locked-memory budget and
  cannot fail. Its borrow hands the callback an empty slice;
  `NewSecretFromBuffer` turns an empty Buffer into an empty Secret; `Open` of
  an empty Secret returns an empty Buffer. `CopyOut` refuses it with
  `*EmptyBorrowError`, as it refuses every empty borrow: a `[]byte`-returning
  API cannot tell "empty on purpose" from "the pages are gone", so a caller
  that accepts an empty value reads it through `WithBytes`.

### Why rule 3 is scoped

Rule 3's remedy is `*secure.Secret` / `*secure.Buffer`, and that remedy only
exists where **this code chooses the representation**. Where another mechanism
materialises the plaintext into memory this code did not allocate, the field
type sits downstream of the copy that matters. Four clauses follow, and the
argument for each is written out in `pkg/secure/analyzer/scope.go`, which is
the document of record:

- **Generated files** — the generator rewrites them, so they can carry neither
  a hand-edit nor an inline suppression, and the wire type has no mapping to a
  Buffer in any case.
- **Fields carrying a codec struct tag** — by the time a handler sees
  `req.Passphrase` the decoder has already allocated the plaintext. A custom
  unmarshaler receives those same bytes and copies them; it does not prevent
  them.
- **Command-line flag targets** — the value is in `os.Args` before `main`
  runs, unwipeable and world-readable through the process's own command line.
- **`_test.go` files** — a fixture holds a literal from its own source, which
  is in the binary's read-only data before the field exists. Rules 1 and 2
  still apply in tests, and that asymmetry is deliberate: rule 1 is a
  containment rule about a process-wide effect, rule 3 is a representation
  rule about a value's lifetime in a long-running process.

**At the codec boundary the standard is stated, not left silent**: a
sensitive-named field is declared `[]byte`, never `string`, and every handler
that reads one wipes it before returning. `string` is immutable in Go and
cannot be wiped; `[]byte` can, and the wipe is the control. The analyzer does
not yet enforce the `[]byte` half — converting the remaining `string` fields
is a wire-format break, since JSON renders `[]byte` as base64 — and that gap
is tracked rather than assumed away.

### Lifetimes: where a copy of protected bytes ends

The three rules are about declarations and access. None of them asks what
happens to bytes once they are copied out of protected memory, and that is how
an unrecoverable plaintext copy once landed with every rule green: a
passphrase moved into a `secure.Buffer`, read back with
`append([]byte(nil), b...)` inside `WithBytes`, and never wiped. A second
analyzer in the same tool, `securecopy`, follows every such copy.

**A copy-out** is `append(x, view...)`, `copy(dst, view)` into a
non-protected destination, `bytes.Clone`/`slices.Clone` of a view,
`string(view)`, or a call to a function known to return one. A *view* is the
`[]byte` a literal `WithBytes` callback receives — the callback is the call's
last argument, so `Secret.WithBytes(ctx, fn)` counts as `Buffer.WithBytes(fn)`
does — or a re-slice of one.

**The obligation ends** when the variable holding the copy is:

- **wiped or consumed** after the copy: passed to `secure.Wipe`, the builtin
  `clear`, or any function that hands its parameter on to one. go-enclave's
  `Wipe` and `NewSealed` are the roots, and consumers are inferred from them
  across packages, so `secure.NewBufferFromBytes` and `seal.NewCredentials`
  count with no annotation. A contract inference cannot see (an interface
  method, for example) is declared with `//secure:consumes` in its doc
  comment.
- **returned**, which makes the function a copy-returner and moves the
  obligation to every caller, across packages.

Anything else is reported. `defer secure.Wipe(x)` written *before* the copy
is reported too: a deferred call evaluates `x` when the defer statement runs,
so it wipes whatever `x` held then. `defer func() { secure.Wipe(x) }()` reads
`x` at exit and counts wherever it is written. A copy that has to outlive every
wipe carries `//secure:unwiped <reason>` trailing it, or standing alone on the
line above it; the directive is per site and visible in review, not an
allowlist.

**What it cannot see**, which stays a review obligation:

- a wipe that only some paths reach (the analysis is not path-sensitive; a
  `defer` right after the copy removes the question);
- a wipe through a second variable (reported, a false positive in the safe
  direction), and an `append` that reallocates the copy after it was made
  (not seen);
- what a callee does with a view it is handed. A view passed to a function
  is not a copy-out, and inside that function the parameter is an ordinary
  `[]byte`;
- copies returned through an interface method or a function value. Facts
  attach to concrete functions, so the obligation is not traced past dynamic
  dispatch. That is where credential retrieval leaves the analysis: the
  service interface the transports call;
- callbacks passed to `WithBytes` by variable rather than as a literal;
- copies the compiler or runtime makes, and plaintext that never lived in
  protected memory (a decoded request field, for example).

Measured when the analyzer landed: 24 copy-outs across both modules. 19 are
returned to a caller that owns them, 3 are wiped or consumed, and 2 carry
`//secure:unwiped`: the per-tenant credential retrieval returned through a
function value to the resolver that wipes it, and the officer share claim
returned to its transport response.

### The gate is checked against a known instance

`make test-secure-analyzer` plants a raw `memguard` import, a raw holder
field, an unwiped copy-out and a copy-out
whose deferred wipe precedes it, requires each to be reported, and requires `go vet` to exit nonzero —
a diagnostic that does not fail the command does not block a merge. It also
requires the four scoped populations and a correctly wiped copy-out to stay silent through the real
binary.
A clean sweep and a neutered analyzer print the same thing; only the red run
distinguishes them.

## Test strategy

- **Unit**: registry defaults and lookup, `ErrNoStrategySpecified`, the alias
  canonicalization, seal/unseal state transitions, PIN dual-control refusals,
  and the refusal of credential initialization for a Shamir barrier.
- **Fake strategies are injected through the same `StrategyRegistry`** used for
  hardware, so integration tests need no hardware coupling. That is the point of
  the seam.
- **No `time.Sleep`** anywhere in the coordinator or its tests.
- **Cluster seal, unit**: `pkg/clusterseal` drives the monitor with an
  injected clock and an in-memory guarded KV: guarded writes and their races,
  epoch-keyed idempotent enforcement, the acknowledgement wait and its timeout,
  lease expiry and resumption with keys kept, restart, held, and the inactive
  gate. Each transport proves the cluster-unseal route, the `cluster_seal`
  status object and a watch ended by a seal.
- **Init statement, unit**: `pkg/barrierinit` signs with real ECDSA keys and a
  real test CA. A statement signed by another key is refused with
  `InitStatementError`; every covered field changes the signature; of two
  concurrent claims exactly one is stored; the compare-and-set refuses a
  skipped, repeated or raced epoch; the watcher delivers each epoch once on an
  injected clock and never an unverified one. `pkg/server` runs the store
  adapter against a real `KVService` over a `LocalBackend`, with and without
  guarded writes enabled. `pkg/tenant` proves that an isolated create on a
  three-member gate is refused with `NotActiveError` before activation and
  admitted after.
- **Cluster seal, integration** (cluster stage): a 3-node cluster in which a
  seal through one node makes the other two refuse requests and end their
  watches; each node unseals with its own credential; no key bytes appear in
  the replicated records or the Raft log; a tenant administrator's seal reaches
  every replica; a cut-off node stops serving and resumes; a restarted node
  reads the record before serving.
- **Integration**: `cluster_enable_encryption_test.go` lives in the **cluster
  stage** (`make integration-test-cluster`), which CI runs in its full tier
  (every push to `main`, every dispatch, and a `ci:full` pull request) as the
  `barrier-seal` leg of the `integration-features` job
  (`make integration-test-barrier-seal`), part of the result gate, tenant-scope
  case included.
  *(Corrected 2026-10-02: this said the stage was not wired into CI.
  Corrected 2026-10-05: the stage moved from the nightly to every push.
  Corrected 2026-10-12: every push to `main`, not every PR push.)*
- **Feature target**: `make integration-test-barrier-seal` runs this feature's
  self-spawned suites (`test/integration/features.mk`, no Docker). The
  per-transport accounting, including the embedded-transport gap, is the
  `barrier-seal` row of `test/integration/COVERAGE.md`.

## Observability

Every barrier operation records an event on the node's compliance audit
trail. A barrier's `BarrierConfig.AuditLogger` is
`seal.NewComplianceAuditLogger(trail, tenant, name)`, the adapter onto the
one trail (`pkg/audit`): the daemon and the SDK node give it to the system
barrier and, through `seal.WithAuditTrail`, to every tenant barrier their
registry builds; the database barrier manager
(`database.WithBarrierAuditTrail`) to every database barrier; and the
desktop's embedded server to both when its trail is enabled. Each record is
keyed by the tenant that owns the barrier -- the system tenant for the system
barrier, the tenant for a tenant barrier, the database's tenant for a
database barrier -- and names the barrier in the attribute `barrier`
(`system`, `tenant:<id>`, `database:<tenant>/<name>`). Only allow-listed
details reach the trail (`domain`, `op`, `strategy`, `threshold`, `from`,
`to`, `rolled_back`, `submitted`, `required`, and the holder record
conflict's `scope`, `username`, `cached_cert`); no key material is among
them, and any other detail is dropped. The records are post-hoc: a trail
that refuses one is counted on
`qrdb_audit_write_failures_total{component="barrier"}` and logged, and the
operation's outcome stands.

| Action | Recorded when |
|--------|---------------|
| `initialize` | the barrier is initialized, or the initialization refused |
| `unseal` | the barrier is unsealed, by a credential or a completed quorum, or the unseal refused |
| `seal` | the barrier is sealed |
| `quorum-complete` | a holder quorum's round completes and its operation runs, or the reconstructed key is refused |
| `distribute` | a domain's key is split to its holders |
| `change_password` | a passphrase barrier's passphrase is changed |
| `enable_encryption` | a barrier is migrated to a keyed strategy |
| `import-root-key` / `import-sealed-blob` | a root key or sealed blob is imported |
| `holder-record-conflict` | a holder cached while sealed conflicts with a persisted holder |

The outcome is `success` for an allowed operation, `failure` for one that
failed with an error and `denied` for any other refusal. The error's text is
never recorded -- it is free text from a sealing strategy, an injected KMS,
HSM or TPM strategy's among them -- and stays on the operational log. **One emitter per event:** a
share submission and a rekey opening are the holder engine's records
(`holder-quorum-submit`, `holder-rekey-start`; see
[../shamir-custody/engineering.md](../shamir-custody/engineering.md)), and the
barrier records no copy of them. *(Corrected: this table listed `rotate`,
`rekey`, `recovery_generate` and `tenant_init`, which no barrier emits, and
said events were emitted "when an AuditLogger is configured" -- no composer
configured one, so none reached any trail.)*


With strategy `none`, every one of those events is marked `strategy: none`, so
the absence of encryption is permanently visible in the trail rather than
inferable.

### Seal enabled, storage not encrypted

Enabling seal does not by itself encrypt every byte the node writes. The
barrier's encrypted filesystem wraps Pebble's file system. On a raft node it
covers every state machine that persists anything -- `mvcc` and `pebble`, and
`disk` and `fast` over pebble storage -- and a state machine whose storage
would persist plaintext (`disk` or `fast` over `file`) is refused at start
(see [../storage-engines/](../storage-engines/engineering.md)), so a raft node reports no
gap. On a standalone node it covers the `pebble` backend, not the `file` or
`objstore` backend.

Such a node still runs. It says so in two places, both computed once at boot
from the configuration (`config.Config.EffectiveCapabilities`):

- **A startup WARNING** per gap, logged by every composition root (the daemon
  and `sdk/go/node`) before the node serves traffic:

  ```
  seal is enabled but data is stored unencrypted at rest  where="standalone backend \"file\" stores its data without the seal barrier's at-rest encryption; use backend \"pebble\""
  ```

- **The capability report**: `UnencryptedAtRest` (`unencryptedAtRest` in JSON,
  `unencrypted_at_rest` in the protobuf) lists the same gaps on REST, gRPC,
  Unix, QUIC, MCP and the embedded transport, and `qrdb admin capabilities`
  prints one `unencryptedAtRest` row per gap. It is empty when seal is off or
  everything on disk is encrypted.

The report judges the node's own store, because that is where every
database's data lives. A database is a namespace over the node's store
whatever `backend` its catalog record names: the store the database manager
opens for a `file` database holds no data unless the database carries a
per-database barrier, and then the barrier encrypts its values (not its keys)
separately. A configured database after the first, or one created at runtime,
therefore adds no gap of its own; on a standalone node the first configured
database's backend *is* the node's store. `objstore` is a node-store backend
only: the database manager refuses to open a database with it
(`database.InvalidBackendError`).

### `seal.enabled: false` startup warning

`seal.enabled: false` is not a barrier operation — there is no barrier
instance to audit — so it is not in the table above. `lifecycle.BuildServices`
is the single construction path `cmd/qrdbd`, the embedded `sdk/go/node`
composer, and the Wails GUI's `cmd/qrdb` composer all call on every
successful boot, sealed or not, so it is where the warning is logged exactly
once per node start:

```
qrdb/seal: seal disabled; at-rest data is stored in plaintext
```

The condition is `!infra.SealEnabled && infra.SealBarrier == nil`, not a bare
nil check on `SealBarrier`: `SealEnabled` is the composer's own seal.enabled
config decision, read and passed in BEFORE it attempted to build a barrier.
A barrier that was *requested* but failed to construct (a bad passphrase, a
corrupt seal store, an invalid strategy config) also leaves `SealBarrier` nil,
and that is a distinct failure the composer is responsible for reporting
through its own error path — not an operator's decision to run without
encryption, and this warning must not misattribute it as one. The Wails GUI
composer (`cmd/qrdb/bindings_server.go`) has no "no barrier at all" state of
its own — even its default mode constructs a real `none`-strategy barrier —
so it passes `SealEnabled: true` unconditionally and this warning never fires
for it; a failed construction there is reported by its own "seal barrier
unavailable" WARN instead. The warning is logged only after `BuildServices`'s
own construction has fully succeeded, so it never fires for a node whose
service graph failed to build there — it says nothing about a later failure
elsewhere in boot (`server.New`, transport listen, etc.), which
`BuildServices` cannot observe. Requiring `SealBarrier` to also be nil means
an older call site that supplies a real barrier without yet setting
`SealEnabled` is not wrongly accused of running one it doesn't have.

The wording mirrors go-quicraft's own `StrategyNone` WARN
(`quicraft/seal: barrier performs NO encryption; at-rest data is stored in
plaintext`) so an operator sees the same fact whether a do-nothing barrier
exists or none exists at all; it does not fire when a barrier of any kind is
present, including under `StrategyNone`, whose own WARN is unchanged and
owned by go-quicraft.

**The `hint` deliberately does not name a procedure to run in place.**
`enable-encryption` only migrates an *existing* barrier whose current
strategy is credential-free (`none`, or a tenant's `system-barrier`-wrapped
root key — see [../seal/enabling-encryption.md](../seal/enabling-encryption.md)),
by re-encoding files that are already in qrdb's encrypted-file format
(`pkg/seal/encfs`). A `seal.enabled: false` deployment's files were never
written in that format, so `enable-encryption` does nothing for it, and
simply setting `seal.enabled: true` and restarting does **not** encrypt
existing data either: `encfs.FS.Open` passes a pre-existing plaintext file
through unmodified (no magic header to decrypt), but `OpenReadWrite` on a
non-empty file without the header returns `ErrInvalidHeader` — the first
write-mode reopen of old data under the flipped config can wedge the boot,
and nothing re-encodes the files that do stay readable. There is no
supported in-place path here; the `hint` instead points at provisioning a new
node/tenant with `seal.enabled: true` and moving data into it through
backup/restore or an application-level re-seed (see
[user.md](user.md#running-with-no-barrier-at-all)).
