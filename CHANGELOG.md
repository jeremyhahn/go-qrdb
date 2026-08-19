# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html).

Pre-1.0, MINOR is where breaking changes live; PATCH is reserved for changes
that do not move the API. Releases before `0.2.0-alpha` predate this file and
are not reconstructed here — their history is in the git log.

## [Unreleased]

## [0.6.0-alpha] - 2026-08-18

MINOR, not PATCH: pre-1.0 this is where breakage lives, and this release moves
the derivation of persisted role and custodian identifiers, tightens the
multicast shared-secret contract, and removes `statemachine.CachedDiskBackend`.

Two housekeeping defects are closed here rather than left to rot. `VERSION` had
read `0.5.7-alpha` through both the `v0.5.8-alpha` and `v0.5.9-alpha` tags — the
unreconciled-sources-of-truth failure `release-versioning.md` §2 warns about —
and neither of those tags got a CHANGELOG section, so their content was still
sitting under `[Unreleased]`. It is folded into this entry. `v0.5.9-alpha` in
particular carried the `CachedDiskBackend` removal, a breaking change published
as a PATCH; consumers that pinned back to `v0.5.8-alpha` over it should move
forward to this release.

**Consumers on `v0.5.9-alpha` or earlier should upgrade.** The UUIDv8 migration
below is what stops `pkg/role` panicking in package init under
`GODEBUG=fips140=only`; it landed after `v0.5.9-alpha` was cut, so every tagged
release before this one still takes down any downstream binary that authorizes
through `pkg/role` in a FIPS build.

### Added

- **Protected resolution targets for `ConfigProcessor` (`pkg/secret`).** A
  config field declared as `secret.SecretValue` (or `*secret.SecretValue`)
  instead of `string` is now resolved in place inside its own memguard
  enclave, so a resolved secret reaches the consumer without ever resting in
  an unwipeable Go string. `SecretValue` gained YAML/JSON codecs that decode
  from — and re-encode to — the plain reference string, so config documents
  are unchanged and a re-serialized config emits the reference rather than
  the plaintext (a non-scalar node — and JSON `null`, which is a configuration
  mistake rather than an empty secret — fails with the new
  `SecretValueDecodeError`). `Raw` is left intact by the walk.

  The protected target is deliberately stricter than a plain `string` field:
  every non-nil target the walk reaches is either resolved or reported in
  `Unresolved`, never skipped in silence. A target holding nothing (a null or
  absent key), an inline `literal://` secret, a bare value, or a scheme no
  resolver claims is refused with the new `UnresolvableTargetError` — an
  inline secret carries the plaintext in `Raw` itself, an ordinary Go string
  that outlives the walk, so sealing it would leave a second unprotected copy
  and break the guarantee the field type advertises. A target the walk cannot
  write to (boxed by value in an unsettable interface) is refused with the new
  `UnwritableTargetError`; boxed targets it can write to are resolved through
  an addressable copy and assigned back. An optional secret is spelled as a
  `*SecretValue`, where an absent or null key leaves the pointer nil — the one
  case the walk passes over. That is also what closes the YAML null gap:
  `yaml.v3` zeroes a null destination before the unmarshaler is consulted, so
  `password:` and `password: null` never reach `UnmarshalYAML` and are refused
  at the walk instead of at decode time, where JSON rejects its `null` token.

  A protected-target failure fails the call: `Process` returns
  `ErrUnresolvedSecretURI` whenever the new `ProcessResult.ProtectedUnresolved`
  is non-empty, with or without `WithFailClosed()`, because choosing the type
  is itself the request to fail closed on that field. `Process` also destroys a
  target's existing enclave before refilling it, so a failed refresh over an
  already-resolved config leaves the field empty rather than letting a revoked
  credential keep authenticating while its backend is down.

  Additive throughout: `Process`'s signature is unchanged, the deprecated
  string path keeps its exact behaviour — including that its failures stay
  opt-in diagnostics under `WithFailClosed()` — and no existing consumer
  breaks on upgrade.
- **Redacted display paths for `SecretValue` (`pkg/secret`).** `SecretValue`
  now implements `fmt.Formatter` and `slog.LogValuer`, which `fmt` and
  `log/slog` consult ahead of `fmt.Stringer`. `%v`, `%s`, `%+v`, `fmt.Sprint`
  and structured logging of a config struct holding a resolved secret therefore
  render the redacted description instead of the plaintext. New `Redacted()`
  exposes the same description directly: it shows the reference only when a
  resolver established that the reference points at a secret rather than
  carrying one, judging by provenance rather than syntax — the registry falls
  back to `StrategyLiteral` for anything no resolver claims, so a
  reference-shaped `https://user:pw@host` or `unknown://pw` is the secret in
  fact and is redacted, as is any value not yet resolved. Only an explicit
  `String()`/`Bytes()` call still materializes plaintext, and only
  serialization still emits `Raw` verbatim — reproducing the configuration
  document is what serializing it means.
- **`WithNoPlaintextStrings()` migration ratchet (`pkg/secret`).** With this
  processor option set, a plain `string` field holding a reference is left
  untouched — its secret is never fetched — and its path is reported in the
  new `ProcessResult.PlaintextSkipped` (always a subset of `Unresolved`). A
  consumer turns it on, converts the reported fields to `SecretValue` one at
  a time, and adds `WithFailClosed()` once the list is empty to refuse any
  boot that would produce a plaintext string. Both re-exported from
  `sdk/go/secret`.
- **Raft state machines over any storage backend (run-modes phase 5).**
  New `raft.sm_storage` (`--sm-storage`) selects the storage backend the
  storage-generic state machines (`sm_type: disk`/`fast`) compose:
  `pebble` (default), `file`, or `memory`, rooted per-(shard, replica)
  under the data dir. Objstore is refused as the hot apply path with a
  pointed error — it is a snapshot/backup tier. The pebble-native SMs
  (`pebble`, `mvcc`) reject a non-pebble selection.
- **File storage backend durability.** `pkg/storage/file` writes are now
  crash-safe: Set is temp-file + fsync + atomic rename + directory fsync;
  Delete fsyncs the parent; batch commits are all-or-nothing via a
  CRC-verified commit journal published before any per-key file is
  touched and replayed at open (a corrupt journal fails the open rather
  than serving a half-applied batch). New optional `storage.Syncer` seam.
  Acceptance: raft single-node survives a hard kill + restart over
  `--sm disk --sm-storage file`, and a 3-node cluster replicates over
  file-backed SM storage (test/integration/sm_storage_file_test.go).

- **Run-modes requirements ratified** ([docs/run-modes.md](docs/run-modes.md)):
  standalone = no Raft (standard database operations + always-on security
  core over any non-raft storage backend); raft (single- or multi-node) =
  MVCC/watch/lease/lock/transactions/etcd/cluster ops. Feature availability
  follows a three-gate model (build tag > run mode > config) with an
  explicit always-compiled CORE. The daemon's `mode: standalone` and the
  capabilities upgrade land in subsequent phases of the approved plan.

- **Feature profile system (run-modes phase 2).** `pkg/capabilities` now
  seals an effective per-deployment profile at boot
  (`capabilities.Seal(cfg.EffectiveCapabilities())`): the intersection of
  build tags, run mode, and config. `Capabilities` gains `mode` plus
  `lock`/`cluster`/`backup`/`rebalance`; the proto, REST/gRPC/QUIC/MCP/
  embedded transports, SDK DTO, and `qrdb admin capabilities` all carry
  the new fields (additive wire shapes). Before a composition root seals,
  `Get` keeps the historical compile-time fallback.
- **`mode` config key** (`"standalone"`/`"raft"`, empty = raft) with
  validation: under standalone, explicitly enabling `mvcc`/`watch`/
  `lease`, `raft.join`, or a multi-member `raft.peers` list fails with
  `requires mode: raft`; standalone skips the raft identity requirements
  (only `raft.data_dir` stays mandatory). New optional `enabled` toggles
  on `mvcc`/`watch`/`lease` narrow the raft profile.
- **Boot-time build gate** `Config.ValidateCapabilities()`: a wire
  protocol enabled in config but not compiled in is refused with an error
  naming the build tag. (Between phases 2 and 3 the daemon refused
  `mode: standalone` with a pointed error; phase 3 replaced the refusal
  with the real composition branch.)
- **Typed refusal hints.** REST `ServiceNotImplementedError` and the
  gRPC/QUIC/service `ServiceNotConfiguredError` gained an optional `Hint`
  appended to the message (e.g. `requires mode: raft`).

- **`mode: standalone` in the daemon (run-modes phase 3).** `qrdbd` now
  composes the raft-free data plane (`lifecycle.NewLocalStorage`/
  `NewLocalEngine` → `backend.LocalBackend`) when `mode: standalone` (or
  `--mode standalone`) is set: no raft host, no WAL, no election — boot
  drops from seconds to milliseconds. Standard database operations plus
  the always-on security core (mTLS/RBAC/IAM, seal barrier incl. encfs,
  audit) serve as in raft mode; raft-only endpoint families (MVCC
  revisions, watch, lease, lock, etcd-style Txn, cluster/backup/
  rebalance, encryption migration) are refused on every transport with
  501/Unimplemented naming the remedy `requires mode: raft`. The SQL
  catalog keeps its internal MVCC/transaction plumbing (standard ops).
- **One canonical local composition.** The three duplicate
  storage/engine composition sites (SDK node, embedded transport, and
  now the daemon) collapse into `lifecycle.NewLocalStorage`/
  `NewLocalEngine`; the embedded `database/sql` driver gains objstore
  support as a side effect.
- **SDK standalone gating (behavior change, as announced below).**
  `NewStandaloneNode` no longer exposes the MVCC/watch/lease/transaction/
  lock service families; standalone nodes report `mode: standalone` in
  capabilities with those features false. `NewClusteredNode` is
  unchanged. `service.AdminService` gains `WithCapabilityProfile` /
  `lifecycle.Infra.Profile` so each node in a multi-node process reports
  its own truth.
- REST's `capabilitiesResponse` now carries the full profile (a phase-2
  funnel gap), and REST maps `ServiceNotImplementedError` to 501 from
  registry-resolved paths (previously 500).

- **Per-mode exemplars and integration suites (run-modes phase 4).**
  `configs/qrdb-standalone-{pebble,memory,file}.yaml` exemplars;
  `configs/qrdb-standalone-mvcc.yaml` renamed to
  `qrdb-raft-single-mvcc.yaml` (it always was a single-node RAFT config —
  the old name contradicted the ratified terminology).
  `TestStandaloneMode_StandardOpsAndRefusals_PerProtocol` drives a
  `--mode standalone` daemon over all five transports: standard ops
  green, capability truthfulness, raft-only refusals; plus per-backend
  boot coverage. The unix SDK transport's capabilities mapping gained the
  new profile fields (the last funnel site). Downstream notified:
  go-quicbaas#4.

- **Release tag lockstep check.** `make check-tag-lockstep`
  (`scripts/check-tag-lockstep.sh`) checks release-versioning.md P2: every
  root `vX.Y.Z` tag must have an `sdk/go/vX.Y.Z` tag at the same commit, and
  no `sdk/go` tag may exist without its root. The new `Release Lockstep`
  workflow runs it on version-tag pushes in both namespaces (`v*` and
  `sdk/go/v*`) — within a minute of publication, rather than days later in a
  consumer's dependency bump — and re-audits the whole tag set on every merge
  to `main`. It detects a skewed release; it
  cannot prevent one, because a tag is visible the moment it is pushed
  (go-qrdb#13 tracks enforcement). The preventive step is running the check
  before publishing both tags in one atomic push
  (`git push --atomic origin vX.Y.Z sdk/go/vX.Y.Z`). `make test-tag-lockstep` covers
  the rejection branches against throwaway repositories, since this
  repository's own tag set is permanently valid and exercises only the
  accepting branch. `v0.5.9-alpha` was tagged on the root module alone, so
  `go get` at latest resolved the root to `v0.5.9-alpha` and `sdk/go` to
  `v0.5.8-alpha` — a pair that does not compile, because the older `sdk/go`
  still aliased the symbols removed below. The tag has since been cut.
  Reported by go-xkms (#6).

### Deprecated

- **Plaintext-into-`string` secret resolution (`pkg/secret`).** Resolving a
  secret into a plain `string` config field is the plain path the stack is
  moving off; use a `SecretValue` field target, and
  `WithNoPlaintextStrings()` to find the fields that still need converting.
  `SecretValue.String()` and `SecretValue.Bytes()` are likewise deprecated in
  favour of `WithBytes`/`Open` (and `Redacted()` for anything displayed).
  Nothing is removed and the deliberate accessors keep their behaviour: the
  exported signatures stay as they are until the stack's coordinated breaking
  release, so the whole dependency chain can move in one wave rather than repo
  by repo. `String()` still returns plaintext because it is the documented way
  to hand a secret to an API that takes a string, and silently redacting it
  would break those callers at run time with no compile error; the accidental
  paths (`%v`, `fmt.Sprint`, structured logging) are redacted now, as noted
  above, so what remains deferred is only the deliberate call.

### Removed

- **`statemachine.CachedDiskBackend` and `NewCachedDiskBackend`**, with
  their `sdk/go` aliases — a breaking change to an exported API, shipped
  in `v0.5.9-alpha`. Both were a reserved placeholder: a methodless struct
  no registry ever provided, so the `cached-disk` SM backend passed config
  validation and then failed at boot. There is no replacement and nothing
  to migrate — no build that compiled against these symbols was running a
  cached disk backend. The disk-backed path is `sm_type: disk`, now
  composable over any storage backend via `raft.sm_storage`.

### Fixed — SECURITY

- **`GOFIPS140=off` was read as a FIPS request.** SO PIN hashing in
  `pkg/auth` picked its KDF with `os.Getenv("GOFIPS140") != ""`, so the one
  value that explicitly means "not FIPS" selected PBKDF2-SHA256, and
  `crypto/fips140.Enabled()` was never consulted at all. Every
  FIPS-conditional path in the tree now routes through a single predicate,
  `seal.FIPSEnabled` (re-exported from `go-quicraft/pkg/seal`): the Go
  runtime's FIPS 140-3 module state first, `GOFIPS140` as an environmental
  fallback, and `off` honoured as disabled. `pkg/seal/kdf.DefaultHasher`
  loses the same env-only test, and `pkg/pin.AutoDetectHashConfig` — which
  was blind to the environment entirely — now honours a `GOFIPS140` FIPS
  request. Stored hashes are unaffected: each record carries its own
  algorithm/version tag and verification still routes by that tag, so a
  hash written in either mode continues to verify in the other.

- **A resolver reporting success with no value is now refused
  (`pkg/secret`).** `SecretResolver` requires a non-nil `SecretValue` on
  success, but `ResolverRegistry.Resolve` did not enforce it: a resolver
  returning `(nil, nil)` panicked the plain string path on the nil
  dereference, and would have let a protected target be counted as resolved
  while holding nothing. The registry now fails such a resolver with
  `ResolutionFailedError`, so the path is recorded in `Unresolved` and
  `WithFailClosed()` refuses the boot.
- **`ConfigProcessor` no longer destroys a `SecretValue` config field.** The
  walker descended into a `SecretValue`, saw its exported `Raw` reference as
  an ordinary resolvable string field, and overwrote it with the resolved
  plaintext while leaving the enclave empty — making the documented "hold the
  secret in a `SecretValue`" pattern strictly worse than a plain `string`
  field. A `SecretValue` is now a protected leaf the walk resolves into and
  never descends through.

### Changed — protected memory

- **The live cluster DEK is held in mlocked memory, and never copied out.**
  `dek.ClusterDEKManager` kept the plaintext cluster DEK in an ordinary
  `[]byte` for the manager's whole lifetime. It is now a `*secure.Buffer`
  (mlocked, guard-paged, wiped on destroy): the key is generated straight
  into protected memory rather than being copied there, and the superseded
  buffer is destroyed on `Rotate` and on `Close`.

  **BREAKING (service seam):** `ClusterDEKManager.GetCurrentDEK() ([]byte,
  error)` is gone, and no exported API replaces it: the key is now applied
  inside the manager by `WrapKey(plaintext []byte) ([]byte, error)` and
  `UnwrapKey(wrapped []byte) ([]byte, error)`, which perform the AES-256-GCM
  key wrapping under the manager's state lock. Neither a heap-backed copy nor
  a slice aliasing protected memory crosses the API boundary, so no caller
  can retain the key past a call or mutate the live key.
  `ClusterDEKManager.CurrentVersion()` is the checked
  counterpart of `GetDEKVersion` for callers that must distinguish
  "version 0" from "no DEK". Correspondingly,
  `service.DEKServicer.GetCurrentDEK(ctx) ([]byte, uint64, error)` becomes
  `CurrentDEKVersion(ctx) (uint64, error)`: the DEK bytes never crossed that
  boundary usefully — the embedded transport already discarded them, and no
  transport exposes DEK material.

### Changed — FIPS 140-3 identifiers

- **Deterministic role and custodian identifiers moved from UUIDv5 (SHA-1) to
  RFC 9562 §5.8 UUIDv8 over SHA-256.** `crypto/sha1` panics under
  `GODEBUG=fips140=only`, and both derivations run during package
  initialization and tenant seeding, so before this change a FIPS build died
  before a single test body ran — including in any downstream binary that
  authorizes through `pkg/role`. UUIDv8 is the version reserved for
  application-defined derivations, and it is the only one available: RFC 4122
  fixes v3 to MD5 and v5 to SHA-1, so no versioned name-based UUID can carry an
  approved hash. Inputs are length-prefixed before hashing so the encoding is
  injective — unframed, tenant `a/b` with role `c` and tenant `a` with role
  `b/c` would collide on one identifier. The 36-character UUID text shape is
  unchanged, so no index key or wire format reformats.

  **These identifiers are persisted data**, so the change is versioned rather
  than a flag day: writes emit only the v8 form and reads accept both. A
  cluster seeded by an older build keeps its v5 identifiers, and code holding a
  tenant plus a role NAME must not re-derive — on such a cluster the v8 value
  names no record at all. Use `role.ResolveSystemRoleID`, which answers from
  the store's name index and falls back to the canonical value, and
  `role.SystemRoleIDs`, which returns every identifier denoting the role so
  that the SO deny-list and the separation-of-duties Owner strip still match a
  grant made before the migration. Writing a canonical identifier straight into
  a legacy cluster's user records produces a grant that resolves to no role and
  silently strips the user's permissions.

### Fixed — release integrity

- **Every previously published tag recorded an unresolvable requirement for its
  own SDK submodule.** The root `go.mod` recorded
  `github.com/jeremyhahn/go-qrdb/sdk/go v0.0.0-00010101000000-000000000000`, a
  placeholder that resolves to nothing. A `replace` directive only applies in
  the main module, so the in-repo `=> ./sdk/go` masked it for every check run
  inside this repository — local builds, CI, `go list`, module-zip inspection —
  while a downstream consumer got:

  ```
  github.com/jeremyhahn/go-qrdb/sdk/go@v0.0.0-00010101000000-000000000000:
      invalid version: unknown revision 000000000000
  ```

  `pkg/ceremony`, `pkg/api/rest` and `cmd/qrdb` import `sdk/go/transport`, so
  the failure is reachable rather than theoretical: a scratch module that does
  `go get github.com/jeremyhahn/go-qrdb@v0.5.9-alpha` and imports
  `pkg/ceremony` cannot even `go mod tidy`. Reproduced with `GOWORK=off`.

  **Why nobody hit it.** Every current consumer — go-xkms, go-quicbaas,
  go-trusted-platform — carries its own explicit
  `require github.com/jeremyhahn/go-qrdb/sdk/go` line at a real version,
  because each also imports `sdk/go/transport` directly. Their own requirement
  satisfies the import, and the root module's nonsense version is never
  consulted. The defect therefore bites a NEW consumer that reaches a root
  package without independently depending on the SDK, which is precisely what
  the mandated throwaway-consumer check constructs. Latent, not dormant.

  The reciprocal pin was stale in the same way: `sdk/go` required the root at
  `v0.4.0-alpha`, five minor versions behind.

  Both cross-module requirements now name the release they ship in
  (`v0.6.0-alpha`), which the lockstep tagging rule guarantees exists — the
  root and `sdk/go` tags are pushed atomically. The `replace` directives stay,
  because in-repo development must still build against the working tree rather
  than a published copy of its own code.

  This is the defect `release-versioning.md` §2 mandates a throwaway-consumer
  check for, and the reason that check is mandatory: no test that runs inside
  the repository can see it. It is also the same failure that condemned
  `v0.2.0-alpha`, which means it regressed and went unnoticed for many
  releases.

### Fixed — tests

- **`TestDefaultHasher_NonFIPSMode` pinned the toolchain instead of the
  selector.** It cleared `GOFIPS140` and asserted the literal Argon2id version,
  but `seal.FIPSEnabled` consults `crypto/fips140.Enabled` before falling back
  to the variable, and `GOFIPS140` selects the module at BUILD time — `t.Setenv`
  cannot retract at run time what the linker already decided. The expectation
  now routes through the `wantVersion` helper the package's other FIPS-selection
  tests already use. No coverage is dropped and nothing is skipped: the test
  asserts Argon2id on an ordinary run and PBKDF2-SHA256 under the FIPS job,
  which is the correct answer for a FIPS binary because Argon2id is not an
  approved KDF. This was the last go-qrdb-side failure under
  `GODEBUG=fips140=only`; the remainder are upstream (see
  `docs/seal/fips140-mode.md`).

## [0.5.8-alpha] - 2026-08-17

### Added

- **Live ABAC policy reload.** `pkg/auth/abac.Decorator` now supports
  `Reload(policies []*abac.Policy) error`: the new set is compiled and
  validated before an atomic pointer swap, so a failed reload leaves the
  previous policy set fully in force and `Authorize` reads lock-free under
  concurrent reloads. `abac.WithLiveReload` keeps the decorator reloadable
  even when zero policies are configured at construction. `sdk/go/node.Node`
  exposes the seam via `WithABACLiveReload` / `Node.ABACPolicyReloader()`;
  `pkg/auth/wiring.AuthorizerConfig`/`AuthMiddlewareConfig` gained an
  `Authorizer` override field so the embedded in-process transport and every
  network transport's `AuthMiddleware` share one authorizer instance, and a
  reload reaches all of them identically. `sdk/go/types_abac.go` re-exports
  `ABACPolicyReloader` and `ABACWithLiveReload`.

## [0.5.7-alpha] - 2026-08-17

### Fixed — SECURITY-ADJACENT

- **Module-registered public routes serve anonymously.** The REST
  middleware short-circuited only its hardcoded built-in allowlist before
  authentication; a module's `HandlePublic` routes (WebAuthn
  registration/login ceremonies, OIDC discovery) still demanded a peer
  certificate once `client_auth` was enabled, breaking every anonymous
  first-contact ceremony. Public registry routes now short-circuit
  pre-authentication exactly like the built-ins.

## [0.5.6-alpha] - 2026-08-17

### Added

- **Pre-resolved scope permissions** (`auth.Identity.ScopePermissions`):
  a TRUSTED in-process issuance path (a module authentication fallback
  validating a platform token) may carry the token's `scp`-derived
  `resource:action` set on the identity. The RBAC authorizer unions them
  with store-resolved role permissions — additive only, SO deny-list
  still overrides — so client-credential/app tokens whose subject has no
  user record authorize for exactly what the token was issued for. Never
  wire-deserialized; the on-behalf path constructs fresh identities and
  cannot carry them.

## [0.5.5-alpha] - 2026-08-17

### Changed

- **Built-in routes yield to module registrations.** The documented
  "modules register first and take precedence" contract is now formal:
  a built-in route whose method+pattern a capability module already
  claimed is skipped (debug-logged) instead of colliding. Module-vs-
  module duplicates still fail construction loudly.
- qrdbd auth bootstrap no longer opens an unchained `audit.log` side
  file (the decision bridge made it obsolete); a nonexistent data dir no
  longer fails auth bootstrap.

## [0.5.4-alpha] - 2026-08-16

### Added

- SDK audit-record aliases (`qrdbsdk.NodeAuditEvent` / `NodeAuditFilter` /
  `NodeAuditPage`): composing hosts emit into the node's chained audit
  sink through the SDK boundary without importing `pkg/audit`.

## [0.5.3-alpha] - 2026-08-16

### Added

- **Module authentication fallback** (`apimodule.ModuleAuthFallback`, SDK
  `qrdbsdk.ModuleAuthFallback`): a capability module whose HTTP surface
  accepts credentials qrdb's own authenticators do not understand (a
  session-token plane) may authenticate the request when the host chain
  produces no identity. The fallback identity flows through the SAME
  store-resolved enrichment and the ONE authorizer as an mTLS identity.
- **REST route-collision guard**: registering the same method+pattern
  twice (two modules, or a module and the built-in surface) fails
  construction with a descriptive panic instead of silently shadowing.

### Changed — one audit plane

- qrdbd's per-decision auth audit now rides the tamper-evident chained
  event log via the decision bridge — identical wiring to embedded nodes;
  the unchained side file is gone.
- The legacy second audit system (`audit.New`, kv/file/multi backends,
  the `audit:` config block, `DaemonAuditConfig`) is DELETED. The chained
  `AuditLogger` (Emit/Query) is the only audit sink; the `Logger`/`Entry`
  middleware contract remains as the decision-bridge interface.
- Audit wire tags are snake_case (`tenant_id`, `client_ip`,
  `request_id`, `prev_hash`), closing the documented cross-project
  serialization mismatch.

## [0.5.2-alpha] - 2026-08-16

### Changed — SECURITY

- **Authentication is now the zero value.** `AuthConfig.Enabled` became a
  pointer (`*bool`) with `IsEnabled()`: an embedder that builds a
  `DaemonConfig` literal without touching the Auth block gets an
  AUTHENTICATED unified server; only an explicit `false` disables. This
  closes a live regression where both downstream daemons (xkmsd,
  quicbaasd) served every qrdb-native plane unauthenticated because the
  plain-bool zero value silently disabled the middleware. Use
  `config.BoolPtr` / `qrdbsdk.BoolPtr` for explicit values.

### Added

- **Per-decision audit on embedded nodes** (`audit.NewDecisionLogger`):
  the auth middleware's decision trail now bridges onto the node's
  chained compliance event log — authorization decisions land in the
  same tamper-evident chain as every other audited operation, matching
  qrdbd. Closes the documented SDK-node TODO.

## [0.5.1-alpha] - 2026-08-16

### Fixed

- **SDK error exports restored**: `sdk/go/types_errors.go` was accidentally
  truncated in 0.5.0-alpha to only the new `NotLeaderError` re-export,
  dropping `QRDBError`, `ErrorCode`, the error constructors and every
  other error alias. The full surface is restored with `NotLeaderError`
  appended. 0.5.0-alpha should not be depended upon.

## [0.5.0-alpha] - 2026-08-16

### Added

- **ABAC authorization decorator** (`pkg/auth/abac`): attribute-based
  access control layered over the RBAC authorizer, porting the core of
  go-quicbaas's IAM policy engine (YAML policy schema, compiler, resource
  trie, condition parser, evaluator) into its canonical qrdb home. Hybrid
  semantics: explicit deny overrides RBAC allow, explicit allow grants
  beyond RBAC, unmatched requests delegate to RBAC. Policies are
  tenant-scoped; the reserved system tenant's policies apply platform-wide
  as guardrails. Enabled in qrdbd via `auth.abac.{enabled,policy_dir}`
  (fail-closed validation and boot) and in embedded nodes via
  `node.WithABACPolicyDir` / `node.WithABACPolicies`. SDK re-exports in
  `sdk/go/types_abac.go` (`ABACPolicy`, `DecorateAuthorizer`, ...) plus
  `qrdbsdk.DaemonAuthABACConfig`. Policies load once at construction;
  hot-reload is a documented follow-up. See `docs/auth/abac.md`.

- **Embedded-node auth enforcement** (`pkg/auth/wiring`): the SDK node
  (`NewStandaloneNode`/`NewClusteredNode`) now constructs the SAME auth
  middleware qrdbd builds — RBAC authorizer (+ABAC decoration), mTLS/SPKI
  authenticator, adaptive/enforced mode — and passes it to the unified
  multi-protocol server. Previously an embedded node served every
  permission-gated route unauthenticated. `BuildAuthMiddleware` /
  `BuildAuthorizer` are shared by `cmd/qrdbd` and the SDK node (one
  construction path, no divergence).

- **Per-RPC public affordance for module gRPC services**
  (`apimodule.GRPCPublicMethodLister`, re-exported as
  `qrdbsdk.ModuleGRPCPublicMethodLister`): modules may declare exact gRPC
  methods that bypass authentication, symmetric with REST's
  `HandlePublic`. Registered method names are validated at startup
  (typed error on malformed names). Built-in health/reflection exemptions
  now genuinely bypass authentication rather than only the permission
  lookup.

- **Module MCP tool permissions** (`apimodule.MCPToolPermissioner`): the
  unified MCP server consumes per-tool permission declarations from
  registered modules.

### Fixed

- **Sealed store crash durability (data-loss grade)**: the seal store —
  the only persisted copy of the barrier root key and epoch state — was
  opened with `SyncWrites: false` (a rationale valid only for
  Raft-replicated stores). A crash could lose the root-key write; the
  next boot silently minted a fresh root key, permanently orphaning every
  file encrypted under the lost key (`quicraft/seal: decryption failed`
  on the data-plane and raft snapshot stores). All seal/DEK stores now
  force `SyncWrites: true`; SIGKILL-subprocess regression tests prove a
  killed node reboots and serves its data (`pkg/seal/encfs`,
  `sdk/go/node`).

## [0.4.0-alpha] - 2026-08-15

### Added

- **ServiceModule MCP tool authorization** (`apimodule.MCPToolPermissioner`):
  a module that registers MCP tools may now declare each tool's required
  permission; the unified server feeds the declarations into the same MCP
  tool registry the built-in tools use, so module tool calls are authorized
  by the one middleware path instead of being unconditionally denied. A
  module tool registered without a declaration remains fail-closed.
- **SDK re-exports for module hosts**: `qrdbsdk.WithServerTLS` (the
  TLS-only transports' certificate source, required by hosts that terminate
  TLS with their own material), `qrdbsdk.NewModuleHTTPRegistrar` (capture
  registrar for module tests), `qrdbsdk.ModuleMCPToolPermissioner`, and
  `qrdbsdk.PermissionIdentityGated` (the identity-gated sentinel).

### Changed

- `github.com/jeremyhahn/go-quicraft` pinned to `v0.4.2-alpha`
  (`batch.EncodeBatch` returns `([]byte, error)`; the error is propagated
  through `pkg/batch`).

### Fixed

- `pkg/api/mcp` default-tag test builds: the shared `CredentialServicer`
  mock lived in a file constrained to the full feature-tag set while an
  untagged test file used it; the mock now lives untagged so every tag
  combination compiles.

## [0.3.0-alpha] - 2026-08-14

### Added — the unified API server seam

Phases 0-2 and phase 3 wave 1 of
`software/plans/unified-api-server-2026-08-14.md`, the work that lets a host
process expose go-qrdb's own control plane instead of reimplementing it per
transport. New exported API, so MINOR.

- `pkg/apimodule`: `ServiceModule` and `BaseModule` with `Start`/`Stop`, a
  duplicate-name check, and a startup assertion that every registered route
  carries a permission. `WithModule` fans a module out across the transports.
  Re-exported from `sdk/go`. Additive — nothing existing changes shape.
- The control plane's own admin, rebalance and user-auth-factor surfaces are
  served through the seam.
- `pkg/sql/privilege`: GRANT, REVOKE and SHOW GRANTS parsing, rewriting and
  enforcement over `service.PrivilegeService`, plus `pkg/sql/pgcatalog`.

### Changed

- REST runs on chi/v5 v5.3.0. SSE `/watch`, on-behalf-of and the cluster-peer
  exempt routes are unchanged, gated by the pre-existing watch and
  barrier-authz tests passing unmodified.
- One authorization path: `authz.proto` carries `option (auth.permission)`, a
  reflection interceptor reads it, and `Perm_*` constants replace the five
  hand-maintained permission tables. Default-deny, the `IdentityGated`
  sentinel, the public-route allowlists and on-behalf-of handling are retained.
  QUIC and MCP gain method registries with mapping guard tests and REST a route
  guard, so a handler added without a permission now fails a test instead of
  shipping unauthenticated.

### Removed

- `pkg/auth/middleware/mapping.go` and the five permission tables it fed.
- The `pkg/sql/internal/privprobe` scaffolding, superseded by `pkg/sql/privilege`.

## [0.2.1-alpha] - 2026-08-14

### Fixed

- The two in-repo modules pinned each other at `v0.1.0-alpha`, so a consumer
  taking `sdk/go v0.2.0-alpha` resolved the parent module to the release that
  still had Avro, and the build failed with `undefined: codec.Avro` — the new
  SDK pulls go-codec v1.4.0, which the old parent's `pkg/dao` cannot compile
  against. Both modules carry a local-path `replace` at each other, which
  applies only in a main module, so every in-repo build resolved through the
  working tree and nothing surfaced the stale requirement. `sdk/go` now
  requires the parent at `v0.2.1-alpha` and the root requires `sdk/go` at the
  same version.

`v0.2.0-alpha` and `sdk/go/v0.2.0-alpha` are superseded and must not be
pinned. The tags remain, per the immutability rule.

## [0.2.0-alpha] - 2026-08-14

### Removed — BREAKING: the Avro codec is gone from both modules

`github.com/hamba/avro` is archived upstream and carries reachable advisories
GO-2026-5046, GO-2026-5047 and GO-2026-5048 with no fix forthcoming and no
production-grade replacement, so Avro leaves the product stack entirely (owner
decision, 2026-08-14; the same decision produced go-codec v1.4.0). These
exported identifiers no longer exist:

- `pkg/codec`: `TypeAvro`, `AvroCodec`
- `pkg/dao`: `CodecAvro`

The `codec_avro` build tag and the `WITH_CODEC_AVRO` Makefile switch are gone
with them, as are the `.avro` and `.avsc` entries in extension-based format
detection and Avro's membership in `probeableCodecs`. A caller that selected
Avro must choose another codec; the JSON, YAML, TOML, MsgPack, BSON, CBOR and
ProtoBuf codecs are unaffected.

### Added

- `cmd/qrdb` and `cmd/qrdbd` sources, and the Wails frontend, are actually in
  the repository. They had never been committed: the root `.gitignore` carried
  unanchored `qrdb` and `qrdbd` patterns intended for the compiled binaries,
  and a gitignore pattern without a slash matches at any depth and matches
  directories, so both command trees — 222 Go files — were excluded. `HEAD` did
  not build.
- `workflow_dispatch` trigger on the CI workflow (ci-template v1.3), so a run
  lost to a runner crash or host overload can be retried without a commit.

### Changed

- `github.com/jeremyhahn/go-codec` pinned to `v1.4.0`, the Avro-free release.
- `github.com/jeremyhahn/go-quicraft` pinned to `v0.3.1-alpha`.
- `golang.org/x/crypto` to `v0.55.0` and `golang.org/x/text` to `v0.41.0` in
  the `sdk/go/gorm/conformance` module, clearing ten HIGH advisories
  (CVE-2026-39828 through CVE-2026-46597, and CVE-2026-56852).

### Fixed

- HTTP/3 listener errors were compared against `http.ErrServerClosed` with
  `!=`, and the pgsql row collector compared against `io.EOF` with `==`; a
  wrapped sentinel from either was mishandled. Both now use `errors.Is`.
- The eight registry mocks in the transport and SQL provider tests implement
  `service.Registry.PrivilegeService`, and the MVCC error stubs are referenced
  by the names they actually have. Without these no test binary in the
  affected packages compiled, which also made `gosec` exit non-zero after
  reporting no findings.
- `TestRunStart_OpenAll_DatabasesDirPermissionError` provoked its failure with
  a 0o000 directory, which root ignores; under CI it started a real node and
  hung until the test binary's deadline. It now uses a regular file where the
  directory belongs, failing with `ENOTDIR` for root and non-root alike.
- `TestNewStartCmd_UnknownSM_NoConfig` read the package directory as its data
  directory, picking up a stray committed `data/ca.pem` and failing before it
  reached the assertion it was written for.

### Security

- `cmd/qrdbd/data/` is ignored. A locally run daemon issues itself a CA, node
  and admin key there, and the directory was previously covered only as
  collateral of the unanchored `qrdbd` pattern.
- Playwright's generated `playwright-report/` and `test-results/` output no
  longer ships in the repository.

[Unreleased]: https://github.com/jeremyhahn/go-qrdb/compare/v0.6.0-alpha...HEAD
[0.6.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.5.9-alpha...v0.6.0-alpha
[0.3.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.2.1-alpha...v0.3.0-alpha
[0.2.1-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.2.0-alpha...v0.2.1-alpha
[0.2.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.1.1-alpha...v0.2.0-alpha
