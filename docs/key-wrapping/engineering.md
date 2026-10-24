# Key Wrapping — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first.

## Exported surface

`pkg/keywrap`:

```go
package keywrap

type Provider interface {
	ProviderID() string
	Wrap(ctx context.Context, keyRef string, dek []byte) (wrapped []byte, err error)
	Unwrap(ctx context.Context, keyRef string, wrapped []byte) (dek []byte, err error)
}

type ProviderFactory func() (Provider, error)

type Registry interface {
	Register(id string, factory ProviderFactory) error
	Unregister(id string)
	LookupFactory(id string) (ProviderFactory, error)
	ListAvailable() []string
}

type DefaultRegistry struct { /* unexported */ }

func NewRegistry() *DefaultRegistry                             // empty
func NewDefaultRegistry(keyDir string) (*DefaultRegistry, error) // pre-populated with "software"

func (r *DefaultRegistry) Register(id string, factory ProviderFactory) error
func (r *DefaultRegistry) Unregister(id string)
func (r *DefaultRegistry) LookupFactory(id string) (ProviderFactory, error)
func (r *DefaultRegistry) ListAvailable() []string

// Lookup resolves and constructs in one call -- the shape every caller
// other than a registry inspector actually wants.
func Lookup(r Registry, id string) (Provider, error)
func (r *DefaultRegistry) Lookup(id string) (Provider, error)

type SoftwareProvider struct { /* unexported */ }

func NewSoftwareProvider(keyDir string) (*SoftwareProvider, error)

func (p *SoftwareProvider) ProviderID() string
func (p *SoftwareProvider) Wrap(ctx context.Context, keyRef string, dek []byte) ([]byte, error)
func (p *SoftwareProvider) Unwrap(ctx context.Context, keyRef string, wrapped []byte) ([]byte, error)
func (p *SoftwareProvider) Close() error
```

`keyRef` is a bare file NAME inside `keyDir`, opened through an `*os.Root` —
never a path. `Close` releases that root.

`pkg/service` (backup), additions over the existing surface in
[backup-restore/engineering.md](../backup-restore/engineering.md):

```go
type BackupOptions struct {
	Destination   string
	Compression   CompressionType
	Encryption    EncryptionType
	KeyProviderID string // defaults to "software" when KeyRef is set
	KeyRef        string // required when Encryption resolves to "aes256"
	Metadata      map[string]string
	ShardIDs      []uint64
}

type BackupInfo struct {
	// ... existing fields unchanged ...
	KeyProviderID string // empty for "none" backups
}

type RestoreOptions struct {
	// ... existing fields unchanged ...

	// DecryptionKeyRef overrides the key reference stored in the backup's
	// own envelope. Left empty, the stored reference is used -- restore
	// needs no key material supplied at all in the common case.
	DecryptionKeyRef string
}

func WithKeyWrapRegistry(r keywrap.Registry) BackupServiceOption
func WithKeyWrapProvider(p keywrap.Provider) BackupServiceOption
func WithKeyDir(dir string) BackupServiceOption
func WithAuditFailurePolicy(p audit.FailurePolicy) BackupServiceOption

// NewBackupService returns a WithKeyWrapProvider collision with an ID
// already registered as a typed error, never discarded.
func NewBackupService(provider ShardSnapshotProvider, opts ...BackupServiceOption) (BackupServicer, error)
```

**No raw KEK bytes** (see
[architecture.md](architecture.md#no-inline-key)).
There is no inline-bytes path on `BackupOptions` or `RestoreOptions`; `KeyRef`
is the only way to name a key, and it is always a provider-resolved
reference (the bare NAME of a key file inside the server's configured key
directory for `"software"`, never a path), never raw bytes.

## Invariants

- **`Provider.Wrap`/`Unwrap` never return the KEK.** Only `dek`/`wrapped`
  cross the interface boundary. A future `Provider` implementation that
  returned key material through an out-of-band channel (a debug log, an
  error string) would violate this contract even if `Wrap`/`Unwrap`'s own
  return values never did; reviewers hold implementations to the same
  "never export" discipline `cakey.Provider` holds for signing keys.
- **Every DEK is fresh random, every backup, never reused.** `crypto/rand`,
  32 bytes, generated inside `CreateBackup` and nowhere persisted
  unwrapped.
- **`SoftwareProvider` refuses a key file that is not exactly 32 bytes, or
  a `keyRef` that is not a bare file name inside its configured
  directory.** Not truncated, not padded, not hashed to size. Every such
  failure -- a rejected name, a missing file, a non-regular file, the
  wrong length -- collapses into ONE detail-free `*KeyAccessError`: the
  directory, the rejected name, the OS error, and the length actually
  read are logged on the server only, never returned, so the error cannot
  be used as a filesystem oracle by a caller who may be remote.
  `*KeyDirectoryError`
  stays separate and detailed: it fires only at construction, when the
  configured directory itself cannot be opened.
- **AES-256-GCM, both envelope layers, no hand-rolled crypto.** `Wrap`/
  `Unwrap` and the per-shard payload encryption both use `crypto/aes` +
  `crypto/cipher.NewGCM`, a fresh 12-byte nonce per call (`crypto/rand`),
  never a static or derived nonce.
- **AAD at the payload layer binds a shard to its backup.** The additional
  authenticated data `Open` checks is recomputed from the backup's own
  trusted metadata (ID, destination, shard ID, format version) at restore
  time, never read back from the (untrusted, until authenticated) stored
  artifact itself.
- **A GCM authentication failure on the DEK-wrap layer is
  `*UnwrapFailedError` (keywrap) / `*BackupDecryptionFailedError`
  (service); a failure on a SHARD's own ciphertext is
  `*BackupShardIntegrityError` -- a DIFFERENT type, deliberately, since
  the latter has nothing to do with the KEK or the wrapped DEK.**
  Neither layer distinguishes "tampered" from "wrong key" WITHIN itself --
  both collapse to one typed, fail-closed outcome per layer so a caller
  cannot use the distinction to narrow down a correct key.
- **Every key buffer is zeroed after use.** `secure.Wipe` on the DEK after
  it has sealed/opened every shard in a backup, and on `SoftwareProvider`'s
  in-memory copy of the key file's contents after each `Wrap`/`Unwrap`
  call.
- **A registered provider ID is immutable once registered; `Register`
  refuses a duplicate.** Matches `seal.StrategyRegistry`'s own rule — a
  caller wanting replacement semantics must `Unregister` first.
- **`KeyRef` empty on an `"aes256"` (defaulted or explicit) create request
  is refused.** `*BackupEncryptionRequiredError` when the default applied,
  `*BackupEncryptionKeyRequiredError` when `"aes256"` was named explicitly
  — the same two-error split `backup-restore/engineering.md` documents,
  keyed off `KeyRef`.
- **An unregistered `KeyProviderID` is refused, not silently treated as
  unencrypted, at EITHER create or restore time.**
  `*ProviderNotRegisteredError` (keywrap) surfaces as
  `*BackupDecryptionFailedError` (service) in both directions — at create,
  a `KeyProviderID` naming a provider this node does not have registered
  is a configuration mistake caught before any shard is captured; at
  restore, it means a provider the backup was created under is no longer
  registered on this node, an environment/deployment problem rather than a
  wrong-key one, but given the same fail-closed typed refusal rather than
  a distinguishable one that would let a caller narrow a guess between
  "provider missing" and "key wrong".
- **Restore needs no key material supplied at all, in the common case.**
  The backup's own envelope stores the `KeyRef` it was wrapped under, so
  `RestoreOptions.DecryptionKeyRef` left empty still resolves and unwraps
  correctly — only an override (the key file moved, or restore runs on a
  different node) needs to name anything.

## Error semantics

`pkg/keywrap`:

| Error type | Condition | Fails closed? |
|---|---|---|
| `KeyAccessError` | `keyRef` is not a bare name, or does not resolve to a regular, exactly-32-byte file inside the configured directory | yes |
| `KeyDirectoryError` | `NewSoftwareProvider`'s configured directory cannot be opened as an `*os.Root` | yes (construction-time) |
| `ProviderClosedError` | `Wrap`/`Unwrap` called after this provider's own `Close` has already run | yes |
| `ProviderAlreadyRegisteredError` | `Register` called with a duplicate ID | yes |
| `ProviderNotRegisteredError` | `LookupFactory`/`Lookup` called with an unregistered ID | yes |
| `UnwrapFailedError` | GCM authentication failed on `Unwrap` (wrong KEK or tampered `wrapped`) | **yes** |

`pkg/service` (backup), additions:

| Error type | Condition | Fails closed? |
|---|---|---|
| `BackupDecryptionFailedError` | An unregistered `KeyProviderID` (create or restore), or a `Wrap`/`Unwrap` failure (bad key reference, wrong KEK, or a tampered wrapped DEK) | **yes** |
| `BackupShardIntegrityError` | A shard's own digest or GCM `Open` failed during restore -- distinct from the KEK-layer failure above | **yes** |
| `BackupManifestInvalidError` | The authenticated manifest failed to authenticate or decode | **yes** |
| `BackupMetadataMismatchError` | The stored metadata and the artifact disagree about encryption, or the manifest's own backup ID/destination/provider does not match the metadata it was read alongside | **yes** |
| `BackupVerificationFailedError` | RestoreBackup's top-level, pre-import verification failure for a specific target shard (wraps one of the above) | **yes** |
| `BackupShardNotFoundError` | A target shard has no stored payload, or no manifest entry | **yes** |
| `BackupKeyDirNotConfiguredError` | The built-in `"software"` provider is resolved with no `WithKeyDir`/`lifecycle.Infra.KeyDir` configured | **yes** |
| `BackupEnvelopeVersionError` | The envelope names a `FormatVersion` this build does not implement | **yes** |
| `BackupManifestAlgorithmError` | The authenticated manifest names an `Algorithm` this build does not implement | **yes** |
| `BackupPlaintextRestoreNotAllowedError` | Restoring a backup whose stored state is plaintext without the caller's explicit `AllowPlaintext` opt-in | **yes** |

Restore requires the caller to supply nothing (the envelope's own stored
`KeyRef` resolves it), so there is no "decryption key required" refusal.
`UnwrapFailedError` surfaces as `BackupDecryptionFailedError`, which maps
to `InvalidArgument` on every transport (`pkg/api/errclass`) — a caller-supplied credential or reference that does
not work, not a server-side failure.

## Test strategy

**Integration target:** `make integration-test-key-wrapping` (no Docker; selection in `test/integration/features.mk`). It runs `key_wrapping_cli_test.go`, which drives `qrdb backup` per protocol: the encrypted-backup envelope round trip (provider recorded and shown by `backup get`, the stored key reference resolving on restore, a right override accepted and a wrong one refused) and the refusals (unregistered provider, a path or `..` instead of a bare key name, missing and wrong-length key files). Per-transport accounting is the `key-wrapping` row of `test/integration/COVERAGE.md`.

- **Unit, `pkg/keywrap`**: `SoftwareProvider` round-trips a DEK through a
  real 32-byte key file; refuses a 31-byte and a 33-byte file with
  `*KeyAccessError`; `Unwrap` with a different 32-byte key file
  returns `*UnwrapFailedError`; a single-bit flip in `wrapped` also
  returns `*UnwrapFailedError`, not a panic or a different decode. Each
  `Wrap`/`Unwrap` call zeroes its own in-memory key copy, proven the same
  way `pkg/cakey`/`pkg/seal`'s own wipe tests are — a swappable `wipeFn`
  indirection, not an assertion on process memory.
- **Unit, `pkg/keywrap`**: `DefaultRegistry.Register` refuses a duplicate
  ID; `LookupFactory`/`Lookup` on an unregistered ID returns
  `*ProviderNotRegisteredError` naming the requested id -- its `Error()`
  never renders the registered set, which is logged on the server only
  (`LookupFactory`'s own `slog.Error`), since `id` is reachable from a
  remote caller (`BackupOptions.KeyProviderID`) and the set is server
  configuration a prober should not learn one request at a time;
  `NewDefaultRegistry(keyDir)` has exactly `"software"` registered.
- **Unit, `pkg/keywrap`**: a `fakeHardwareProvider` test double, registered
  through `Registry.Register` exactly as a real go-xkms provider would be,
  proves the registry path works for ANY `Provider`, not only
  `SoftwareProvider` — the same "prove the seam through an interface, not
  the one production type" discipline `cakey.Adapter`'s own tests hold.
- **Unit, `pkg/service` (backup)**: the encrypt-by-default scenarios,
  proven under the envelope (not merely "a key was supplied"):
  the default with no key/provider configured is refused
  (`BackupEncryptionRequiredError`); the default with a key-file `KeyRef`
  produces a backup whose PERSISTED SHARD BYTES ARE NOT THE PLAINTEXT
  (read the sink/registry's raw stored bytes directly, bypassing
  `RestoreBackup`, and assert they do not contain the known plaintext
  fixture) and which restores correctly with the matching key file; a
  wrong key file on restore gives `*BackupDecryptionFailedError`; a
  single-byte tamper of a stored shard's ciphertext gives
  `*BackupShardIntegrityError`, wrapped in a top-level
  `*BackupVerificationFailedError`, with NOTHING imported; an explicit
  `none` backup's stored bytes ARE the plaintext (negative control for the
  "not the plaintext" assertion above) and still warn/audit exactly as the
  plaintext-override mechanism does.
- **Unit, `pkg/service` (backup), regression suite**: a
  backup whose stored metadata and envelope disagree (either direction) is
  refused with `*BackupMetadataMismatchError`; a tampered manifest is
  refused with `*BackupManifestInvalidError`; a shard dropped from storage
  after creation (present in the manifest, absent from the artifact) is
  caught by the DEFAULT restore target (no explicit `TargetShards`), not
  only an explicit one; a plaintext-override audit write failure under
  `FailClosed` refuses the backup/restore and stores nothing, while
  `FailOpen` (and `SetAuditFailurePolicy` applied after construction)
  still proceeds; a duplicate `WithKeyWrapProvider` registration returns
  `*keywrap.ProviderAlreadyRegisteredError` from `NewBackupService` rather
  than being discarded.
- **Unit, `pkg/service` (backup)**: a backup created with a
  `fakeKeyWrapProvider` registered under a non-`"software"` ID round-trips through create,
  list (confirms `KeyProviderID` on `BackupInfo` matches), and restore —
  proving the service layer never special-cases `"software"` anywhere in
  its own logic.

## Configuration

The `Registry` itself is composed in code (`WithKeyWrapRegistry`/
`WithKeyWrapProvider`), matching `StrategyRegistry`'s own
composition-not-configuration shape. The one genuinely configured value is
the built-in `"software"` provider's key DIRECTORY (`WithKeyDir`/
`lifecycle.Infra.KeyDir`/`--backup-key-dir`): every key file it reads must
live inside it, by NAME, confined via `*os.Root`. The backup surface's own flags/fields
(`--encryption-key-file`, `KeyRef`) are documented in [cli.md](cli.md) and
[backup-restore/cli.md](../backup-restore/cli.md).

## Persistence

A backup's envelope (`BackupEnvelope`, stored as JSON under its Go field
names: `FormatVersion`, `ProviderID`, `KeyRef`, `WrappedDEK`, and
`SealedManifest` -- the AUTHENTICATED
manifest described under [architecture.md's envelope
format](architecture.md#envelope-format), not to be confused with the
backup's own ordinary metadata record below) is stored SEPARATELY from
`BackupInfo` (ID, timestamp, checksum, ...), wherever that metadata record
already lives — the in-memory registry for a non-durable backup, or the
configured `BackupSink` for a durable one. The envelope is read back only
by `RestoreBackup`, `DecryptBackupShard`, and `VerifyBackup`;
`ListBackups`/`GetBackup` expose `KeyProviderID` alone
(see [architecture.md](architecture.md#backup-integration) for why the
wrapped DEK itself is not surfaced on the wire).
