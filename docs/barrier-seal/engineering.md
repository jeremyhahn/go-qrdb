# Barrier and Seal — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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
func NewDeferredSoftwareStrategy() *SoftwareStrategy
func NewNoneStrategy() SealingStrategy

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
- **A barrier's KDF work factor is not preserved across a strategy
  replacement, because it cannot currently be expressed.** The deferred
  constructor takes no KDF parameters, so a deferred strategy always derives
  with the library defaults for the selected algorithm; the tunable
  constructors upstream return **bound** strategies, which `ChangePassword`
  must not install. Tunability and deferred binding are disjoint today, so no
  deployment can hold a tuned work factor for a password change to discard.
  This is a latent trap for whoever adds a KDF tuning surface, not a live
  weakening: adding that surface owns adding the preservation with it, and
  depends on upstream accepting a deferred constructor that takes explicit KDF
  parameters plus an accessor for reading them back.
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
- **Key material lives in `memguard.LockedBuffer`** and is destroyed on seal.
  After sealing, `GenerateClientCert()` returns `CAKeyNotAvailableError`.
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
