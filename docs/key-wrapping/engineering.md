# Key Wrapping — Engineering Contract

> **Status: NORMATIVE; lands incrementally.** Scope: go-qrdb · Last verified: 2026-10-04

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

func NewRegistry() *DefaultRegistry          // empty
func NewDefaultRegistry() *DefaultRegistry   // pre-populated with "software"

func (r *DefaultRegistry) Register(id string, factory ProviderFactory) error
func (r *DefaultRegistry) Unregister(id string)
func (r *DefaultRegistry) LookupFactory(id string) (ProviderFactory, error)
func (r *DefaultRegistry) ListAvailable() []string

// Lookup resolves and constructs in one call -- the shape every caller
// other than a registry inspector actually wants.
func (r *DefaultRegistry) Lookup(id string) (Provider, error)

type SoftwareProvider struct { /* unexported */ }

func NewSoftwareProvider(keyDir string) (*SoftwareProvider, error)

func (p *SoftwareProvider) ProviderID() string
func (p *SoftwareProvider) Wrap(ctx context.Context, keyRef string, dek []byte) ([]byte, error)
func (p *SoftwareProvider) Unwrap(ctx context.Context, keyRef string, wrapped []byte) ([]byte, error)
```

`keyRef` is a bare file NAME inside `keyDir`, opened through an `*os.Root` —
never a path (go-qrdb security review, backup-encrypt-default #B4).

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

// NewBackupService now returns an error: a WithKeyWrapProvider collision
// with an ID already registered is returned typed, never discarded
// (go-qrdb security review, backup-encrypt-default #B6).
func NewBackupService(provider ShardSnapshotProvider, opts ...BackupServiceOption) (BackupServicer, error)
```

**`EncryptionKey`/`DecryptionKey` (raw KEK bytes) are retired, not merely
deprecated** — owner ruling, see
[architecture.md](architecture.md#owner-ruling-the-inline-key-is-retired).
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
  be used as a filesystem oracle by a caller who may be remote
  (go-qrdb security review, backup-encrypt-default #B4). `*KeyDirectoryError`
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
  the latter has nothing to do with the KEK or the wrapped DEK
  (go-qrdb security review, backup-encrypt-default: conflating the two
  previously reported a tampered shard as "wrong key-encrypting key").**
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
  — the same two-error split `backup-restore/engineering.md` already
  documents, now keyed off `KeyRef` instead of a raw key byte slice.
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

`BackupDecryptionFailedError` retires `BackupDecryptionKeyRequiredError`:
restore no longer requires the caller to supply anything (the envelope's
own stored `KeyRef` resolves it), so there is nothing left for a
"decryption key required" refusal to fire on. Both `UnwrapFailedError` and
`BackupDecryptionFailedError` map to `InvalidArgument` on every transport
(`pkg/api/errclass`) — a caller-supplied credential or reference that does
not work, not a server-side failure.

## Test strategy

- **Unit, `pkg/keywrap`**: `SoftwareProvider` round-trips a DEK through a
  real 32-byte key file; refuses a 31-byte and a 33-byte file with
  `*InvalidKeyLengthError`; `Unwrap` with a different 32-byte key file
  returns `*UnwrapFailedError`; a single-bit flip in `wrapped` also
  returns `*UnwrapFailedError`, not a panic or a different decode. Each
  `Wrap`/`Unwrap` call zeroes its own in-memory key copy, proven the same
  way `pkg/cakey`/`pkg/seal`'s own wipe tests are — a swappable `wipeFn`
  indirection, not an assertion on process memory.
- **Unit, `pkg/keywrap`**: `DefaultRegistry.Register` refuses a duplicate
  ID; `LookupFactory`/`Lookup` on an unregistered ID returns
  `*ProviderNotRegisteredError` naming the registered set;
  `NewDefaultRegistry()` has exactly `"software"` registered.
- **Unit, `pkg/keywrap`**: a `fakeHardwareProvider` test double, registered
  through `Registry.Register` exactly as a real go-xkms provider would be,
  proves the registry path works for ANY `Provider`, not only
  `SoftwareProvider` — the same "prove the seam through an interface, not
  the one production type" discipline `cakey.Adapter`'s own tests hold.
- **Unit, `pkg/service` (backup)**: the three OWNER-RULING scenarios,
  re-proven under the envelope (not merely "a key was supplied"):
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
  already-shipped mechanism does.
- **Unit, `pkg/service` (backup) -- the review's own regression suite**: a
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
  than being discarded. Each of these is paired with a mutation check:
  reverting the corresponding fix makes the test fail.
- **Integration**: a backup created with a `fakeHardwareProvider`
  registered under a non-`"software"` ID round-trips through create,
  list (confirms `KeyProviderID` on `BackupInfo` matches), and restore —
  proving the service layer never special-cases `"software"` anywhere in
  its own logic.

## Configuration

The `Registry` itself is composed in code (`WithKeyWrapRegistry`/
`WithKeyWrapProvider`), matching `StrategyRegistry`'s own
composition-not-configuration shape. The one genuinely configured value is
the built-in `"software"` provider's key DIRECTORY (`WithKeyDir`/
`lifecycle.Infra.KeyDir`/`--backup-key-dir`): every key file it reads must
live inside it, by NAME, confined via `*os.Root` (go-qrdb security review,
backup-encrypt-default #B4). The backup surface's own flags/fields
(`--encryption-key-file`, `KeyRef`) are documented in [cli.md](cli.md) and
[backup-restore/cli.md](../backup-restore/cli.md).

## Persistence

A backup's envelope (`BackupEnvelope`: `format_version`, `provider_id`,
`key_ref`, `wrapped_dek`, and `sealed_manifest` -- the AUTHENTICATED
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
