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

func NewSoftwareProvider() *SoftwareProvider

func (p *SoftwareProvider) ProviderID() string
func (p *SoftwareProvider) Wrap(ctx context.Context, keyFilePath string, dek []byte) ([]byte, error)
func (p *SoftwareProvider) Unwrap(ctx context.Context, keyFilePath string, wrapped []byte) ([]byte, error)
```

`pkg/service` (backup), additions over the existing surface in
[backup-restore/engineering.md](../backup-restore/engineering.md):

```go
type BackupOptions struct {
	Destination   string
	Compression   CompressionType
	Encryption    EncryptionType
	KeyProviderID string // defaults to "software" when EncryptionKey or KeyRef is set
	KeyRef        string // mutually exclusive with EncryptionKey
	EncryptionKey []byte // mutually exclusive with KeyRef; inline 32-byte KEK
	Metadata      map[string]string
	ShardIDs      []uint64
}

type BackupInfo struct {
	// ... existing fields unchanged ...
	KeyProviderID string // empty for "none" backups
}

func WithKeyWrapRegistry(r keywrap.Registry) BackupServiceOption
func WithKeyWrapProvider(p keywrap.Provider) BackupServiceOption
```

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
- **`SoftwareProvider` refuses a key file that is not exactly 32 bytes.**
  Not truncated, not padded, not hashed to size — `*InvalidKeyLengthError`
  naming the length actually found.
- **AES-256-GCM, both envelope layers, no hand-rolled crypto.** `Wrap`/
  `Unwrap` and the per-shard payload encryption both use `crypto/aes` +
  `crypto/cipher.NewGCM`, a fresh 12-byte nonce per call (`crypto/rand`),
  never a static or derived nonce.
- **AAD at the payload layer binds a shard to its backup.** The additional
  authenticated data `Open` checks is recomputed from the backup's own
  trusted metadata (ID, destination, shard ID, format version) at restore
  time, never read back from the (untrusted, until authenticated) stored
  artifact itself.
- **A GCM authentication failure, at either layer, is `*UnwrapFailedError`
  (keywrap layer) or `*BackupDecryptionFailedError` (service layer) --
  never distinguished from "tampered" vs "wrong key" in what is returned.**
  Both collapse to one typed, fail-closed outcome so a caller cannot use the
  distinction to narrow down a correct key.
- **Every key buffer is zeroed after use.** `secure.Wipe` on the DEK after
  it has sealed/opened every shard in a backup, and on `SoftwareProvider`'s
  in-memory copy of the key file's contents after each `Wrap`/`Unwrap`
  call.
- **A registered provider ID is immutable once registered; `Register`
  refuses a duplicate.** Matches `seal.StrategyRegistry`'s own rule — a
  caller wanting replacement semantics must `Unregister` first.
- **`EncryptionKey` and `KeyRef` are mutually exclusive.** Supplying both
  is refused (`*BackupInvalidEncryptionError`-shaped: a malformed request,
  not a missing-key one) before any shard is touched.
- **Restoring an unknown `KeyProviderID` is refused, not silently treated
  as unencrypted.** `*ProviderNotRegisteredError` (keywrap) surfaces as
  `*BackupDecryptionFailedError` (service) — the backup names a provider
  this node does not have registered, which is an environment/deployment
  problem, not a wrong-key one, but gets the same fail-closed typed
  refusal rather than a different one that would let a caller distinguish
  "provider missing" from "key wrong" and narrow a guess between them.

## Error semantics

`pkg/keywrap`:

| Error type | Condition | Fails closed? |
|---|---|---|
| `InvalidKeyLengthError` | key file is not exactly 32 bytes | yes |
| `ProviderAlreadyRegisteredError` | `Register` called with a duplicate ID | yes |
| `ProviderNotRegisteredError` | `LookupFactory`/`Lookup` called with an unregistered ID | yes |
| `UnwrapFailedError` | GCM authentication failed on `Unwrap` (wrong KEK or tampered `wrapped`) | **yes** |

`pkg/service` (backup), additions:

| Error type | Condition | Fails closed? |
|---|---|---|
| `BackupDecryptionFailedError` | `Unwrap` failed, or a shard/artifact's GCM `Open` failed, during restore | **yes** |

Both `UnwrapFailedError` and `BackupDecryptionFailedError` map to
`InvalidArgument` on every transport (`pkg/api/errclass`), the same class
`BackupDecryptionKeyRequiredError` already uses — a caller-supplied
credential that does not work, not a server-side failure.

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
  single-byte tamper of a stored shard's ciphertext gives the same typed
  error; an explicit `none` backup's stored bytes ARE the plaintext
  (negative control for the "not the plaintext" assertion above) and still
  warn/audit exactly as the already-shipped mechanism does.
- **Integration**: a backup created with a `fakeHardwareProvider`
  registered under a non-`"software"` ID round-trips through create,
  list (confirms `KeyProviderID` on `BackupInfo` matches), and restore —
  proving the service layer never special-cases `"software"` anywhere in
  its own logic.

## Configuration

No dedicated config section for `pkg/keywrap` itself — a `Registry` is
composed in code (`WithKeyWrapRegistry`/`WithKeyWrapProvider`), matching
`StrategyRegistry`'s own composition-not-configuration shape. The backup
surface's own flags/fields (`--encryption-key-file`, `KeyRef`) are
documented in [cli.md](cli.md) and
[backup-restore/cli.md](../backup-restore/cli.md).

## Persistence

A backup's envelope metadata (`format_version`, `provider_id`, `key_ref`,
`wrapped_dek`) is stored alongside its existing manifest fields (ID,
timestamp, checksum, ...), wherever that manifest already lives — the
in-memory registry for a non-durable backup, or the configured
`BackupSink` for a durable one. It is read back only by `RestoreBackup`
and `VerifyBackup`; `ListBackups`/`GetBackup` expose `KeyProviderID` alone
(see [architecture.md](architecture.md#backup-integration) for why the
wrapped DEK itself is not surfaced on the wire).
