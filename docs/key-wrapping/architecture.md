# Key Wrapping — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first.

Envelope encryption for every data-encrypting key (DEK) go-qrdb mints, through
**one** key-encrypting-key (KEK) seam that go-xkms and quicbaas compose
hardware and cloud KMS backends into. Its consumers are backup and the
per-tenant DEK keyring ([tenant-dek](../tenant-dek/architecture.md)) — see [The tenant DEK keyring](#the-tenant-dek-keyring) below.

## Problem

Two features in this repository need the same primitive — encrypt some
bytes with a key that is itself protected, and know later which protected key
to ask for when decrypting — in a form a hardware backend can plug into:

- **Backup** encrypts every shard under AES-256 (`EncryptionAES256`). A
  restore must fail on a wrong key or a tampered artifact, which needs
  authenticated ciphertext and a record of which key protects it.
- **The tenant DEK keyring** ([tenant-dek](../tenant-dek/architecture.md))
  needs its per-version keys wrapped under a key-encrypting key that an
  operator with a TPM, an HSM, or a cloud KMS can keep there rather than in
  a software-sealed barrier.
- **Both need one seam go-xkms can compose a hardware backend into.** A
  TPM2, PKCS#11 or cloud-KMS-backed KEK has exactly one shape that matters
  here — wrap and unwrap a short byte string under a reference the caller
  already has.

`cakey.Provider` ([certificate-authority](../certificate-authority/architecture.md#the-ca-key-provider-seam-pkgcakey))
already solved the identical problem for **signing**: one small interface,
one software implementation, and a `With*` composition point go-xkms fills
with hardware. This feature is `cakey.Provider`'s shape applied to
**wrapping** instead of signing.

## Shape chosen

**One interface, `keywrap.Provider`, shaped like `cakey.Provider`: it holds
or references the KEK and never exports it.**

```go
package keywrap

// Provider wraps and unwraps a data-encrypting key (DEK) under a
// key-encrypting key (KEK) this provider holds or references. It never
// exports the KEK itself -- callers get a wrapped DEK back, never KEK
// bytes, the same discipline cakey.Provider holds for a CA signing key.
//
// Thread-safe: implementations must support concurrent Wrap/Unwrap calls
// from every caller that holds this provider (backup and the tenant DEK
// keyring).
type Provider interface {
	// ProviderID identifies this provider's kind -- "software", "tpm2",
	// "pkcs11", "aws-kms", and so on. Stored verbatim in every envelope
	// this provider wraps, so a later Unwrap resolves the SAME kind of
	// provider through the Registry (below) without guessing.
	ProviderID() string

	// Wrap encrypts dek under the KEK keyRef names, returning the bytes to
	// persist. keyRef is opaque to this package and entirely
	// provider-specific: the bare NAME of a key file inside the server's
	// own configured key directory for the software provider, an
	// HSM slot/handle, a KMS key ARN. This package never inspects it.
	Wrap(ctx context.Context, keyRef string, dek []byte) (wrapped []byte, err error)

	// Unwrap decrypts wrapped, previously returned by Wrap under the SAME
	// keyRef, back into the original dek. Returns *UnwrapFailedError when
	// the KEK does not match or wrapped was tampered with -- the provider
	// must not distinguish those two causes in what it returns, so a
	// caller cannot use this as a key-guessing oracle.
	Unwrap(ctx context.Context, keyRef string, wrapped []byte) (dek []byte, err error)
}
```

**Providers resolve through a registry keyed by provider ID**, mirroring
[`seal.StrategyRegistry`](../barrier-seal/architecture.md): go-qrdb's daemon
owns a default registry pre-populated with the one software provider it
ships; go-xkms registers hardware and cloud-KMS providers at composition
time by calling `Register`, the identical shape `StrategyRegistry` already
uses for sealing strategies.

```go
package keywrap

// ProviderFactory constructs a Provider on demand, the same lazy-construction
// shape seal.StrategyFactory uses -- a TPM2 or PKCS#11 provider may need to
// acquire a hardware handle only when first used, not at registration time.
type ProviderFactory func() (Provider, error)

// Registry is the canonical lookup table for KEK providers by ID.
type Registry interface {
	Register(id string, factory ProviderFactory) error
	Unregister(id string)
	LookupFactory(id string) (ProviderFactory, error)
	ListAvailable() []string
}
```

**qrdb ships exactly one software implementation.** `SoftwareProvider` wraps
and unwraps with an operator-supplied 32-byte key read from a key FILE (never
from a flag value or an environment variable — see
[Key hygiene](#key-hygiene-the-key-never-rides-in-a-flag-value) below), using
AES-256-GCM from the Go standard library (`crypto/aes` + `crypto/cipher`,
both FIPS-capable): a fresh random 96-bit nonce per `Wrap` call, prepended to
the ciphertext, with no additional authenticated data of its own — callers
that need AAD (backup does) supply it at the DEK-encrypts-the-payload layer,
one level up, not here. No hand-rolled cryptography anywhere in this
package.

```go
package keywrap

func NewSoftwareProvider(keyDir string) (*SoftwareProvider, error)

func (p *SoftwareProvider) ProviderID() string // "software"
func (p *SoftwareProvider) Wrap(ctx context.Context, keyRef string, dek []byte) ([]byte, error)
func (p *SoftwareProvider) Unwrap(ctx context.Context, keyRef string, wrapped []byte) ([]byte, error)
func (p *SoftwareProvider) Close() error        // releases the *os.Root
```

**Every key file this provider ever opens lives directly inside ONE
configured directory, `keyDir`, opened once at construction as an `*os.Root`**
— the same confinement `pkg/audit`'s `trailDir` already holds for the
compliance trail. `keyRef` is a **bare file name** inside that directory,
**never a path**: `Wrap`/`Unwrap` refuse a `keyRef` that is empty, `"."`,
`".."`, or contains a path separator, before ever asking the filesystem.
This closes a filesystem oracle and a denial-of-service lever:
`BackupOptions.KeyRef` / `RestoreOptions.DecryptionKeyRef` are values a
caller on a network transport supplies, and resolving them as an
unconstrained path would let that caller probe
the server's filesystem (existence, permissions, size, via the error
returned) and point the read at an arbitrarily large or infinite file (e.g.
`/dev/zero`). The read is capped at 33 bytes regardless of the named file's
real size, so "too long" costs the same bounded work as "exactly right"
without ever learning how much too long.

A `keyRef` that resolves is required to be a **regular file of exactly 32
bytes** — never truncated, padded, or hashed down to size. Every failure
(a rejected name, a missing file, a directory given as `keyRef`, the wrong
length) collapses into ONE typed, **detail-free** error, `*KeyAccessError`:
its `Error()` string names none of the directory, the rejected name, the
underlying OS error, or the length actually read, because any of that
reaching a remote caller would turn the error itself into the same
filesystem oracle the confinement closes. Full detail is written to the
server's own log via `slog` at the point of failure. `*KeyDirectoryError`
stays separate and detailed on purpose: it fires only at construction, when
`keyDir` itself cannot be opened, which is an operator configuration
mistake, never something a request triggers.

`Wrap`/`Unwrap` read the file, use it, and zero the in-memory copy
(`secure.Wipe`) before returning — the same discipline every other
software key-custody path in this repository already holds
([certificate-authority](../certificate-authority/architecture.md),
[barrier-seal](../barrier-seal/architecture.md)).

**Rotation: a new key version is a new file and a new `keyRef`, never an
in-place overwrite.** Overwriting a key file in place would retroactively
change what an already-wrapped DEK (and therefore an already-created
backup) depends on; instead, an operator adds `backup-v2.key` alongside
`backup-v1.key` and starts naming the new one in fresh backups, while a
restore of an old backup keeps asking for whichever `keyRef` it was created
under (`BackupEnvelope.KeyRef`, overridable per restore via
`RestoreOptions.DecryptionKeyRef` when the file's name on this node
differs). `pkg/keywrap`'s own tests prove a DEK wrapped under `v1` still
unwraps after `v2` exists in the same directory; `pkg/service`'s prove a
backup made under an old key reference still restores.

**The key directory is a server-side configuration value, never
caller-supplied.** `service.WithKeyDir` (service construction)
and `lifecycle.Infra.KeyDir` (composition) set it once; `cmd/qrdbd` exposes
it as `--backup-key-dir`. Resolving the built-in `"software"` provider with
no key directory configured refuses with `*BackupKeyDirNotConfiguredError`
rather than falling back to an implicit location such as the process's
current working directory.

### Alternatives rejected

- **A raw key passed as `[]byte` straight into `BackupOptions`, no
  provider seam at all.** Rejected: it has no composition point for a
  hardware backend, and it puts key bytes on the wire (see
  [No inline key](#no-inline-key)).
- **Encrypting shards directly under the KEK, no DEK.** Rejected:
  envelope encryption exists specifically so the expensive-to-protect key
  (the KEK, possibly in hardware) is invoked once per backup rather than
  once per shard/artifact, and so rotating the KEK never requires
  re-encrypting data that has not changed — only the (small) wrapped DEK
  is re-wrapped. This is the standard shape every KMS (AWS, GCP, Azure,
  Vault) already expects a caller to use, which is also why it is what
  lets go-xkms compose a *cloud* KMS here as easily as a local HSM.
- **AES key-wrap (RFC 3394) instead of AES-256-GCM for `Wrap`/`Unwrap`.**
  Considered, not rejected outright: either is "no hand-rolled crypto" and
  either satisfies this interface. AES-256-GCM is what this repository
  already uses pervasively (the barrier, backup's own DEK-to-payload
  layer), so using the same primitive for both envelope layers means one
  reviewed code path instead of two, and GCM's authentication tag is what
  turns "wrong KEK" into a typed, fail-closed `UnwrapFailedError` rather
  than silently-wrong bytes. A future provider is free to wrap under a
  different scheme internally (an HSM's own wrap operation, for instance)
  — this package's own `SoftwareProvider` is the only one bound to GCM by
  this document.
- **One option that accepts a "role" parameter, instead of distinct
  `With*` options per consumer.** Rejected, for the same reason
  `certificate-authority`'s two-key design rejected it: a single
  role-parameterized option lets a caller wire the *same* provider
  instance to two call sites that are supposed to be independently
  rotatable by mistake. See [Injection](#injection) below.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| `keywrap.Provider`, `keywrap.Registry`, `keywrap.DefaultRegistry` | `pkg/keywrap` | go-qrdb |
| `keywrap.SoftwareProvider` (AES-256-GCM, key-file backed) | `pkg/keywrap` | go-qrdb |
| Backup's envelope: DEK generation, per-shard/artifact AES-256-GCM, wrapped-DEK storage in backup metadata | `pkg/service` (backup) — see [backup-restore](../backup-restore/architecture.md) | go-qrdb |
| Hardware/cloud-KMS providers (TPM2, PKCS#11, AWS/GCP/Azure KMS, Vault) | — | **go-xkms**, registered via `WithKeyWrapProvider` / a `Registry.Register` call at composition time |
| The tenant DEK keyring's own KEK integration | `pkg/dek` — see [tenant-dek](../tenant-dek/architecture.md) | go-qrdb |

go-qrdb never imports go-xkms. The fit for a hardware or cloud-KMS provider
is verified in `pkg/keywrap`'s own tests against a same-shaped local double
— `fakeHardwareProvider`, registered through `Registry` exactly as a real
one would be — never against a real xkms type, the same discipline
`cakey.Adapter`'s tests already hold.

## Envelope format

Each backup generates **one fresh random 32-byte DEK**, with
`crypto/rand`, used for that backup only — never reused across backups,
never derived from anything persisted.

```
create:
  dek          := crypto/rand, 32 bytes
  for each shard/artifact:
      nonce    := crypto/rand, 12 bytes
      aad      := backup metadata (id, destination, shard id, format version)
      ciphertext := AES-256-GCM-Seal(dek, nonce, plaintext, aad)
      digest   := SHA-256(ciphertext)
      store nonce || ciphertext
  manifest     := { format_version, algorithm, provider_id, key_ref,
                     backup_id, destination, shards: sorted [{shard_id, digest}] }
  manifest_aad := "qrdb-backup-manifest" || backup_id || destination || format_version
  sealed_manifest := AES-256-GCM-Seal(dek, nonce2, json(manifest), manifest_aad)
  wrapped_dek  := provider.Wrap(ctx, keyRef, dek)
  secure.Wipe(dek)
  store { format_version, provider_id, key_ref, wrapped_dek, sealed_manifest } in backup metadata

restore:
  read { format_version, provider_id, key_ref, wrapped_dek, sealed_manifest } from backup metadata
  provider     := registry.Lookup(provider_id)        // refused if unregistered
  dek          := provider.Unwrap(ctx, key_ref, wrapped_dek)   // *UnwrapFailedError on mismatch/tamper
  manifest     := AES-256-GCM-Open(dek, sealed_manifest, manifest_aad)  // *BackupManifestInvalidError on failure
  assert manifest.backup_id == backup_id && manifest.destination == destination
         && manifest.provider_id == provider_id                // *BackupMetadataMismatchError otherwise
  // PASS 1 -- verify EVERY target shard before importing ANY:
  for each target shard:
      assert shard present in artifact AND in manifest.shards   // *BackupShardNotFoundError otherwise
      assert SHA-256(stored ciphertext) == manifest digest       // *BackupShardIntegrityError otherwise
      nonce, ciphertext := split(stored bytes)
      aad      := backup metadata (same fields, recomputed — not re-read from the untrusted artifact)
      plaintext := AES-256-GCM-Open(dek, nonce, ciphertext, aad)  // *BackupShardIntegrityError on failure
  // PASS 2 -- only now import every verified plaintext.
  secure.Wipe(dek)
```

**AAD binds each shard/artifact to the backup it was sealed under.** The
additional authenticated data is derived from the backup's own metadata
(its ID, destination, and the shard ID this specific piece belongs to), not
stored inside the ciphertext — so a shard swapped in from a different
backup, or between two shards of the same backup, fails authentication on
`Open` rather than decrypting into the wrong place silently.

**The manifest is an authenticated header binding the whole envelope
together, sealed under the DEK (never the KEK).** It records the format
version, the algorithm, the provider ID and key reference the DEK was
wrapped under, the backup's own ID and destination, and the EXACT sorted
set of shard IDs with a SHA-256 digest of each shard's stored ciphertext.
Restore authenticates it before touching a single shard and cross-checks
its own backup ID/destination/provider against the metadata it was read
alongside. Without it, (1) a dropped shard (removed from storage after
creation) would go undetected when the default restore target is derived
from whatever the artifact still has rather than from an authenticated
original set, and (2) a restore that decrypts and imports shard by shard
leaves every shard imported before a tampered one in place, with no
top-level error. Restore therefore runs two passes: pass 1 authenticates and
decrypts (or, for a plaintext backup, confirms the presence of) EVERY
target shard; only once ALL of them pass does pass 2 import any of them.
A verification failure anywhere returns one typed, top-level
`*BackupVerificationFailedError` and imports nothing.

**The stored metadata (`BackupInfo.Encryption`) and the artifact must
agree about whether a backup is encrypted.** If `BackupInfo.Encryption` were
the only signal restore read to decide whether to decrypt, an attacker able
to edit a backup's metadata independently of its cryptographic artifact
(unix permissions on a file-backed sink, for instance) could flip it to
`"none"`, substitute plaintext of their own choosing and recompute the
unauthenticated plaintext checksum, and restore would import the
substitution without consulting the envelope still on disk. Restore
therefore refuses with `*BackupMetadataMismatchError` whenever an
envelope is present but the metadata claims `"none"`, or the metadata
claims encryption but no envelope is stored — in EITHER direction, before
reading anything else. This cannot detect a WHOLESALE substitution (an
attacker who also deletes the envelope and manifest entirely, since
forging a new one requires the KEK, which this threat model assumes they do
not have) — that residual gap is why `storage.Storage` write access to a
backup repository is itself a privileged operation, outside this
document's scope. Restoring a backup that is genuinely plaintext (no
envelope ever existed) is unaffected: it is logged at WARN and, where an
audit logger is configured, recorded in the compliance trail, exactly as
an explicit-plaintext `CreateBackup` already was.

**`format_version` is recorded, not inferred.** A backup's metadata carries
the envelope format version it was written under, so a future format change
(a different AEAD, a different AAD shape, a different `algorithm` value)
can read an old backup correctly rather than guessing from its absence.
Version `1` is this document's format, above, with `algorithm` always
recorded as `"aes-256-gcm"`.

**`none` backups carry no envelope at all.** The `none` path is reached
only by explicit request, never by default
([backup-restore/architecture.md](../backup-restore/architecture.md)).

**A tampered shard's own ciphertext failing authentication is
`*BackupShardIntegrityError`, never `keywrap.UnwrapFailedError`.** The
latter names a KEK/wrapped-DEK failure one layer up; conflating the two
would report plain ciphertext tampering as "wrong key-encrypting key",
which misleads an operator debugging a corrupt artifact.

## Backup integration

**"Encrypted by default" means a configured KEK provider and key
reference.** `CreateBackup` refuses with `BackupEncryptionRequiredError`
when the default applies and nothing is configured; a `KeyProviderID` +
`KeyRef` pair that resolves through the registry satisfies it. There is no
raw-bytes escape hatch (see [No inline key](#no-inline-key) below): every
KEK is referenced through a provider, never passed as bytes through this
struct or over the wire.

```go
type BackupOptions struct {
	Destination string
	Compression CompressionType
	Encryption  EncryptionType // "aes256" (default) | "none"

	// KeyProviderID names the registered keywrap.Provider to wrap this
	// backup's DEK with. Defaults to "software" when KeyRef is set and
	// this is left empty.
	KeyProviderID string

	// KeyRef is the key reference the named provider resolves -- the
	// bare NAME of a key file inside the server's configured key
	// directory for "software". Required whenever Encryption
	// resolves to "aes256" (whether defaulted or named explicitly).
	KeyRef string

	Metadata map[string]string
	ShardIDs []uint64
}
```

### No inline key

No backup surface carries key bytes: the backup options and messages have
no `EncryptionKey` or `DecryptionKey` field in the Go SDK, no
`encryption_key` or `decryption_key` wire field on REST, gRPC, QUIC or
MCP, and `qrdb backup` has no `--encryption-key` or `--decryption-key`
flag. The CLI has
`--encryption-key-file` / `--decryption-key-file` and `--key-provider`.
`KeyRef` is always a reference the server resolves on its own node — the
bare NAME of a key file inside its configured key directory for
`"software"`, never a path and never bytes a client transmits. That is
also why `--encryption-key-file` has no stdin form (`-`): there is no
client-to-server channel for raw key bytes.

`BackupInfo` carries one field for this feature, safe to expose on every transport because
it names a provider, not a key: `KeyProviderID string` (empty for `none`
backups). The wrapped DEK, key reference, and format version are NOT
surfaced on `BackupInfo` — they live in the backup's own persisted manifest
(wherever shard data already lives: the in-memory registry or the
configured `BackupSink`), read back only by `RestoreBackup` and `VerifyBackup`
internally.

A GCM authentication failure on `Unwrap` (wrong KEK, or a tampered wrapped
DEK) surfaces as `*BackupDecryptionFailedError` (`InvalidArgument`) on every
transport. A shard's own `Open` failing (tampered shard ciphertext) is a
DIFFERENT typed error, `*BackupShardIntegrityError` -- see
[Envelope format](#envelope-format) above -- deliberately not the same
type: the two causes are checked at different layers (the KEK/wrapped-DEK
once per backup, each shard's own ciphertext per shard). Neither case distinguishes "wrong key" from
"tampered" WITHIN its own layer, so a caller cannot use that finer
distinction to probe for the correct key one shard at a time.

## Key hygiene: the key never rides in a flag value

**`--encryption-key-file <name>` is the only way** to supply the software
provider's 32-byte key: the CLI passes the bare file NAME straight through
as `KeyRef`, and the server's own `SoftwareProvider` resolves it inside its
one confined key directory and validates it on its own node when it
actually wraps or unwraps the DEK. The CLI itself never opens the file.

No flag accepts key bytes (see [No inline key](#no-inline-key)). A key
passed as a flag value would be visible in `ps`, in shell history and in
any process-listing tool on the host for the life of the process.

The desktop/web UI's key-reference field is a plain text input (it carries a
key file name, not a secret): the key file's bytes never pass through the
browser or the Wails bridge in either direction.

## Target: cloud-KMS references

go-qrdb registers no cloud-KMS provider, so its own documentation, error
text and help name no KMS capability. **Target, once go-xkms registers a
cloud-KMS provider:** `KeyProviderID`
names it (`"aws-kms"`, `"gcp-kms"`, `"azure-kv"`, `"vault"`, matching
whatever go-xkms calls its own registrations), and `KeyRef` carries that
service's own key identifier (an ARN, a resource name, a path) — exactly
the shape [Injection](#injection) below composes for hardware.

## Injection

An embedder registers a hardware or cloud-KMS provider at composition time:

```go
registry := keywrap.NewRegistry() // empty; NewDefaultRegistry(keyDir) pre-populates "software"
registry.Register("tpm2", func() (keywrap.Provider, error) {
	return tpm2Provider, nil // a keywrap.Provider go-xkms constructs
})
```

and wires the registry into whichever composition root needs it through
`server.WithKeyWrapRegistry(registry)`, the same `With*` composition shape
`WithStrategyRegistry` uses for sealing strategies:
`server.ResolveCryptoOptions` carries it into
`lifecycle.Infra.KeyWrapRegistry`, which `BuildServices` hands to the
Backup service. `server.WithKeyWrapProvider(p keywrap.Provider)` is the
narrower sibling for an embedder with exactly one provider and no registry
of its own: it installs the provider under its own `ProviderID()` into the
otherwise software-only default registry, and `NewBackupService` returns a
collision with an ID already registered as a typed error.

**This seam is wired on every composition root `sdk/go/node` reaches**
(`NewStandaloneNode`/`NewClusteredNode`, seal-enabled and seal-disabled
alike, via the shared `withCryptoInfra` helper) and the embedded SDK
transport, which consumes whichever `BackupServicer` `sdk/go/node` built
rather than constructing its own. `cmd/qrdbd` is `package main`, so
nothing outside it can call a `With*` option on it; it exposes the same
seam as a package-level variable, `keyWrapInjection`, read by both its live
`BuildServices` call (`start.go`) and its offline, boot-time
disaster-recovery restore path (`restoreShardFromBackup`, `restore.go`).
`restore_test.go` overrides it to prove an injected registry or provider
reaches the offline path.

**Two options** (`WithKeyWrapRegistry`/`WithKeyWrapProvider`), not one
role-parameterized option: a caller with one ad hoc provider should not
have to build a registry. Backup and the tenant DEK keyring consume the
same `lifecycle.Infra.KeyWrapRegistry`/`KeyWrapProvider` (see
[The tenant DEK keyring](#the-tenant-dek-keyring) below), while each
resolves its own independent "software" instance, confined to its own key
directory (`KeyDir` for backup, `DEKKeyDir` for the keyring), when neither
field is supplied.

## The tenant DEK keyring

[tenant-dek](../tenant-dek/architecture.md) wraps every version's key
through **this same `keywrap.Provider`/`Registry`** — one KEK seam, two
consumers, rather than a second one invented for the keyring.
`dek.NewKeyring`'s `WithKeyWrapRegistry`/`WithKeyWrapProvider` are the
identical option shapes `NewBackupService` uses; `lifecycle.BuildServices`
passes it the SAME `Infra.KeyWrapRegistry`/`KeyWrapProvider` values it
passes to Backup, so a hardware or cloud-KMS provider registered once is
available to both. The keyring's own confinement directory for the
built-in "software" provider is separate from backup's (`Infra.DEKKeyDir`,
`dek.WithKeyDir`, `qrdbd --dek-key-dir`), because the two naming domains
differ: the keyring's key reference defaults to the tenant ID itself
(`dek.WithKeyRef`, tenant-scoped and configurable), while backup's is an
operator-chosen name per backup. Each version's wrapped key, provider ID
and key reference are recorded in the keyring's own stored record (never
the plaintext key); `dek.Status` surfaces the provider ID and key
reference that wrapped each version, for audit. A stored record holding a
plaintext key per version is refused with a typed error (`*dek.LegacyKeyringFormatError`) rather than migrated
silently. See [tenant-dek/architecture.md](../tenant-dek/architecture.md)
for the full design.

**`keywrap.Provider` is the key-wrap seam, not the DEK seam.** The engine
behind the tenant DEK service is a separate interface, `dek.Provider`
(`server.WithDEKProvider`), that a composer fills when a device holds the tenant
keys and runs the encryption itself. A composer that keeps the software keyring
fills only this seam. See
[tenant-dek/engineering.md](../tenant-dek/engineering.md#the-engine-seam-dekprovider)
and [release/extension-seams.md](../release/extension-seams.md).

## Trust boundaries

- **The KEK itself never leaves the provider that holds it.** `Wrap`/
  `Unwrap` cross the interface boundary with a DEK (32 bytes, one per
  backup) and a wrapped DEK, never the KEK — a hardware provider's whole
  reason to exist is that the KEK never enters this process's memory at
  all.
- **A wrapped DEK is useless without its provider and key reference.**
  Storing it in backup metadata — including in a plaintext (`none`)
  backup's own metadata, for an `aes256` backup's shard payloads inside it
  — discloses nothing by itself.
- **The software provider's key file is the operator's to protect.** This
  package validates its length, confines every key file to one configured
  directory, and zeroes its own in-memory copy; file permissions, at-rest
  encryption of the file itself, and backup of the key file (never inside a
  qrdb backup) are the operator's responsibility, exactly as for every
  other software-custody key in this repository.
- **There is no flag-value key exposure to trade off.** No flag accepts
  key bytes — see
  [Key hygiene](#key-hygiene-the-key-never-rides-in-a-flag-value).
- **`secure.Wipe` zeroes the key buffer this package held, not every copy
  the AEAD implementation made of it.** `crypto/aes`'s software
  implementation expands a key into a round-key schedule it keeps for the
  lifetime of the `cipher.Block`, and Go's garbage collector may have
  relocated or retained copies of the original slice before `Wipe` ran; the
  standard library gives no API to zero either. This is why a hardware
  provider, whose KEK never enters this process's memory, is the stronger
  guarantee.

## What this feature deliberately does not do

- **No key derivation.** Every DEK is fresh random; nothing here derives a
  key from a passphrase (that is `pkg/seal`'s job for the barrier itself).
- **No AUTOMATED key rotation workflow for the KEK.** A new key version is a
  new file and a new `keyRef` (see the rotation note under
  [Shape chosen](#shape-chosen)), added by the operator; this seam only
  wraps and unwraps under whatever `keyRef` currently resolves to, with no
  scheduler, no expiry, and no re-wrap-on-rotation step. A backup's own
  wrapped DEK is never re-wrapped after the fact — restoring an old backup
  still asks for the KEK that wrapped it, by the `keyRef` it was wrapped
  under (or `RestoreOptions.DecryptionKeyRef`'s override).
- **No multi-recipient envelopes.** One `keyRef`, one provider, one wrapped
  DEK per backup — not an envelope multiple KEKs can independently open.
