# Barrier and Seal — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-01

## Exported surface

`sdk/go`:

```go
func (c *Client) Barrier()   transport.BarrierClient
func (c *Client) DEK()       transport.DEKClient
func (c *Client) SealStore() transport.SealStoreClient
```

Composition-plane options (Tier 2), on the server constructor:

```go
func WithStrategyRegistry(reg seal.StrategyRegistry) ServerOption   // hardware injection
func WithSealStrategy(s seal.SealingStrategy) ServerOption          // direct injection
func WithPINBackend(b pin.PINBackend) ServerOption
```

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

For field documentation and the complete struct definition, see `pkg/seal/barrier.go:113-160`. The key fields are:
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
  share count only the daemon's init command can supply, and the daemon
  constructs it inline.
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
  write until unsealed. Only the sealed-window allowlist is reachable. That
  includes what was opened before the seal: on the SQL wires every statement of
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
  through `server.WhileSystemShardSettling`); a cluster that has activated a
  feature the binary lacks fails the boot.
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
  seal.ErrSealed)`). `Connect`, `Close` and `WithDatabase` are lifecycle, not
  requests; a database-scoped transport inherits the gate.
  `TestSealGate_RefusesEveryRequestOutsideTheAllowlist` walks the method set
  by reflection, so a request method added without the gate fails. The node's
  feature gate advertises through its own ungated in-process client, as
  `cmd/qrdbd`'s does. The per-database etcd listener, which has no
  sealed-window middleware, refuses every request and every Watch or
  LeaseKeepAlive open while the node refuses (`Server.nodeRefusal`): the node
  barrier's `*clusterseal.SealedError` when held or sealed,
  `*clusterseal.SealStateUnconfirmedError` when cut off or unconfirmed.
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
  (the go-enclave migration removed the process-wide lock that made it), but converting
  these sites to a held borrow is a separate, not-yet-landed change.
  Those sites read without a borrow deliberately, each with a
  `//secure:unborrowed injected-interface` directive the secure analyzer
  requires (rule 4 below), and are covered by a regression test that fails if
  a borrow is reintroduced.
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
> the older prose is stale; the design record is
> [seal/cluster-enable-encryption-design.md](../seal/cluster-enable-encryption-design.md).

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
| Node lease expired, or no read since start | node gate refuses; streams end with `SealStateUnconfirmedError`; keys kept | **yes** |
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
process-wide mlocking allocator. Four rules protect that boundary, and they
are enforced by a `go vet` plugin under `pkg/secure/analyzer/` that `make
secure-analyzer` runs in CI's Lint job over **every Go module in the tree**
under **every combination in the Makefile's `SUPPORTED_TAG_SETS`**.

| # | Rule | Scope |
|---|---|---|
| 1 | No raw `memguard` import anywhere (go-enclave is the protected-memory library; `pkg/secure` is its facade) | Every file, tests included |
| 2 | No `ssh.ParsePrivateKey`; use `ParsePrivateKeyWithPassphrase` on Buffer-backed bytes | Every file, tests included |
| 3 | No struct field named `Passphrase`, `Password`, `PIN`, `Secret`, `PrivateKey`, `KeyMaterial` or `RecoveryShare` typed `string` or `[]byte` | Scoped — see below |
| 4 | No `secure.Buffer.Bytes` reference (call, method value or method expression) unless a `//secure:unborrowed <kind> <reason>` directive trailing the read, or standing alone on the line above it, names why it cannot hold a borrow; `<kind>` is `injected-interface`, `allocates-protected` or `nested-enclave` | Every file except `_test.go` |

Rule 4 exists because `Bytes` returns a slice into locked pages with no lock
held, so a concurrent destroy — the shutdown sweep above all — can unmap the
pages mid-read and fault the process. `WithBytes` and `secure.CopyOut` hold
the borrow across the read. A read that cannot hold one crosses an interface a
consumer implements (the barrier's `Encrypt`, a sealing strategy, the share
distributor), which `pkg/secure` forbids reaching from inside a borrow. The
directive is written at the site and read by the analyzer; it is not an
allowlist, and nothing away from the line can silence a read. Test files are
out of scope: a test reads its buffer on the goroutine that owns it, with no
shutdown sweep running.

Both the sweep and its module coverage matter. `./...` does not cross a module
boundary, so `sdk/go` needs its own pass; and a single tag combination reports
on the files that combination compiles and says nothing about the others.

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

The four rules are about declarations and access. None of them asks what
happens to bytes once they are copied out of protected memory, and that is how
an unrecoverable plaintext copy once landed with every rule green: a
passphrase moved into a `secure.Buffer`, read back with
`append([]byte(nil), b...)` inside `WithBytes`, and never wiped. A second
analyzer in the same tool, `securecopy`, follows every such copy.

**A copy-out** is `append(x, view...)`, `copy(dst, view)` into a
non-protected destination, `bytes.Clone`/`slices.Clone` of a view,
`string(view)`, or a call to a function known to return one. A *view* is the
`[]byte` a literal `WithBytes` callback receives, a `Buffer.Bytes` result, or
a re-slice of either.

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
line above it, in the same per-site form as rule 4.

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
field, a bare `secure.Buffer.Bytes` read, an unwiped copy-out and a copy-out
whose deferred wipe precedes it, requires each to be reported, and requires `go vet` to exit nonzero —
a diagnostic that does not fail the command does not block a merge. It also
requires the four scoped populations, a directive-justified read, a read in a
test file and a correctly wiped copy-out to stay silent through the real
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
- **Cluster seal, integration** (cluster stage): a 3-node cluster in which a
  seal through one node makes the other two refuse requests and end their
  watches; each node unseals with its own credential; no key bytes appear in
  the replicated records or the Raft log; a tenant administrator's seal reaches
  every replica; a cut-off node stops serving and resumes; a restarted node
  reads the record before serving.
- **Integration**: `cluster_enable_encryption_test.go` lives in the **cluster
  stage** (`make integration-test-cluster`), which CI runs on every push in
  the `integration-cluster` job, part of the result gate. Its tenant-scope
  case also runs in the cluster tenancy job (see
  [../multi-tenancy/engineering.md](../multi-tenancy/engineering.md#test-strategy)).
  *(Corrected 2026-10-02: this said the stage was not wired into CI.
  Corrected 2026-10-05: the stage moved from the nightly to every push.)*

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
