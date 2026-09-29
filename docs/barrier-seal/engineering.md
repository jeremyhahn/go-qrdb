# Barrier and Seal — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-11

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

  The rule does **not** yet reach Shamir share fields or SO/User PINs, which
  are still text strings on every transport. See
  [api.md](api.md#known-limitation--share-and-pin-fields-are-still-text-strings).

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
- **`software` is a deprecated config alias for `passphrase`**, canonicalized
  once at load (`SealConfig.CanonicalStrategy()`), so every consumer sees
  `passphrase` — and the strategy itself reports `ID() == "passphrase"`.
- **Sealed refuses everything.** `ErrBarrierSealed` for every data-path read and
  write until unsealed. Only the sealed-window allowlist is reachable.
- **Zero hardware coupling is a hard invariant.** No TPM2 code, no PKCS#11 code,
  no `pkcs11` build tag in any qrdb Go file.
- **Storage wrapping is conditional on `base`.** With a base backend set, the
  barrier transparently encrypts `Put` and decrypts `Get`; with `base` nil it is
  pure crypto (the go-quicraft shape used for the Raft WAL).
- **PIN dual control**: `--so-pin` requires `--user-pin` and vice versa
  (FIPS 140-3 / PCI-DSS v4). PINs are rejected entirely under
  `--barrier-strategy=none` with a typed `PINsNotAllowedError`.
- **Per-node key independence**: each node holds its own system barrier KEK; a
  tenant root key is re-wrapped under the *receiving* node's system barrier. A
  compromised node key does not expose tenant keys elsewhere.
- **The CA key is a plain `[]byte` (DER-encoded), not a `secure.Buffer`.**
  `DefaultCertProvider.caKey` is decrypted from its wrapped form on demand and
  held only until the next `Seal()`, which zero-fills it with `secure.Wipe`
  (a plain overwrite, not a go-enclave locked-memory enclave) and drops the
  reference. After sealing, `GenerateClientCert()` returns
  `CAKeyNotAvailableError`.
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
  comment saying so, and are covered by a regression test that fails if a
  borrow is reintroduced.
- **The rule is checked, not just written down.** `secure.GuardBorrows`-armed
  tests drive each converted path and report a nested borrow as a test failure
  naming both sites, instead of a process that stops with no diagnostic.
- **`ShareExchangeCoordinator` unseals exactly once** — `CompareAndSwap` on an
  atomic guards the handler. It is transport-agnostic, uses map-based message
  dispatch, and contains **no `time.Sleep`** (timer + select).

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

## Permission model

| Permission | Gates |
|---|---|
| `seal:bootstrap` | initialize, and root-token generation |
| `seal:unseal` / `seal:seal` | unseal / seal |
| `seal:status` | status, including recovery status |
| `seal:rekey` | rotating the unseal key and share set |
| `seal:rotate_root_key` | root key rotation |
| `seal:enable_encryption` | migrating off `none` |
| `seal:initialize_shamir` | Shamir M-of-N initialization |
| `seal:generate_recovery` / `seal:recover_with_keys` | recovery key lifecycle |
| `seal:officer_register` / `seal:officer_list` | Security Officer enrolment and listing |
| `seal:submit_seal_vote` | Shamir seal voting |
| `barrier:unseal-share` | submitting an unseal share |

Officer claim endpoints are **identity-gated** rather than permission-gated: the
claimant proves possession of the registered key by signing a nonce over mTLS.

## Configuration

```yaml
seal:
  enabled: true          # false (the default) runs with no barrier at all
  strategy: passphrase   # passphrase | none | shamir  ("software" = deprecated alias)
```

`SealConfig.CanonicalStrategy()` canonicalizes the alias at load. Per-database
and per-tenant barrier configuration lives in
[../databases/](../databases/engineering.md) and
[../multi-tenancy/](../multi-tenancy/engineering.md).

## Persistence

- The sealed root-key blob records the strategy that sealed it
  (`persistedStrategyCache`).
- The CA private key is stored wrapped at
  `__sys:cert_ca/{tenantID}/wrapped_key`, with the certificate PEM at
  `__sys:cert_ca/{tenantID}/cert_pem`.
- **Compatibility**: the sealed blob format carries a version sentinel — see
  [seal/sealed-window.md](../seal/sealed-window.md).

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

### The gate is checked against a known instance

`make test-secure-analyzer` plants a raw `memguard` import and a raw holder
field, requires both to be reported, and requires `go vet` to exit nonzero —
a diagnostic that does not fail the command does not block a merge. It also
requires the four scoped populations to stay silent through the real binary.
A clean sweep and a neutered analyzer print the same thing; only the red run
distinguishes them.

## Test strategy

- **Unit**: registry defaults and lookup, `ErrNoStrategySpecified`, the alias
  canonicalization, seal/unseal state transitions, PIN dual-control refusals,
  and `ShareExchangeCoordinator`'s exactly-once unseal under concurrency.
- **Fake strategies are injected through the same `StrategyRegistry`** used for
  hardware, so integration tests need no hardware coupling. That is the point of
  the seam.
- **No `time.Sleep`** anywhere in the coordinator or its tests.
- **Integration**: `cluster_enable_encryption_test.go` lives in the **cluster
  stage** (`make integration-test-cluster`), which is **not currently wired into
  CI**.

## Observability

Every barrier operation emits an audit event when an `AuditLogger` is
configured:

All barrier operations emit audit events when an `AuditLogger` is configured:

| Action | Resource | Outcome |
|--------|----------|---------|
| `initialize` | `barrier` | `allow` / `deny` |
| `unseal` | `barrier` | `allow` / `deny` |
| `seal` | `barrier` | `allow` |
| `rotate` | `barrier` | `allow` / `deny` |
| `rekey` | `barrier` | `allow` / `deny` |
| `recovery_generate` | `barrier` | `allow` / `deny` |
| `tenant_init` | `tenant:{id}` | `allow` / `deny` |


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
