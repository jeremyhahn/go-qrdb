# Key Wrapping — Architecture

> **Status: NORMATIVE; lands incrementally — see each section's own note.**
> Scope: go-qrdb · Last verified: 2026-10-04

Envelope encryption for every data-encrypting key (DEK) go-qrdb mints, through
**one** key-encrypting-key (KEK) seam that go-xkms and quicbaas compose
hardware and cloud KMS backends into. Backup is the first consumer; the
per-tenant DEK keyring ([tenant-dek](../tenant-dek/architecture.md)) is the
second, planned, and explicitly **not** touched by this lane — see
[Planned: the tenant DEK keyring](#planned-the-tenant-dek-keyring) below.

## Problem

Three features in this repository need the same primitive — encrypt some
bytes with a key that is itself protected, and know later which protected key
to ask for when decrypting — and until now none of them had it in a form a
hardware backend could plug into:

- **Backup** promised AES-256 encryption (`EncryptionAES256`) and validated a
  key's length, but never actually encrypted a shard with it: the key was
  checked and discarded, and every backup, "encrypted" or not, was written in
  plaintext and labelled otherwise. The restore path accepted any key,
  including a wrong one, because there was no ciphertext whose authentication
  could fail.
- **The tenant DEK keyring** ([tenant-dek](../tenant-dek/architecture.md))
  wraps its DEKs with the per-tenant barrier today — correct for an
  operator with no hardware KMS, and a dead end for one who has a TPM, an
  HSM, or a cloud KMS and wants the tenant keyring's *key-encrypting* key to
  live there instead of in a software-sealed barrier.
- **Neither surface agreed on a seam go-xkms could compose a hardware backend
  into.** A TPM2, PKCS#11 or cloud-KMS-backed KEK has exactly one shape that
  matters here — wrap and unwrap a short byte string under a reference the
  caller already has — and nothing in this repository named that shape before
  now.

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
// from every caller that holds this provider (backup today; the tenant
// DEK keyring is the planned second caller).
type Provider interface {
	// ProviderID identifies this provider's kind -- "software", "tpm2",
	// "pkcs11", "aws-kms", and so on. Stored verbatim in every envelope
	// this provider wraps, so a later Unwrap resolves the SAME kind of
	// provider through the Registry (below) without guessing.
	ProviderID() string

	// Wrap encrypts dek under the KEK keyRef names, returning the bytes to
	// persist. keyRef is opaque to this package and entirely
	// provider-specific: a file path for the software provider, an
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

func NewSoftwareProvider() *SoftwareProvider

func (p *SoftwareProvider) ProviderID() string // "software"
func (p *SoftwareProvider) Wrap(ctx context.Context, keyFilePath string, dek []byte) ([]byte, error)
func (p *SoftwareProvider) Unwrap(ctx context.Context, keyFilePath string, wrapped []byte) ([]byte, error)
```

`keyRef` for `SoftwareProvider` IS the key file's path. `Wrap`/`Unwrap` read
the file, validate it is **exactly 32 bytes**, use it, and zero the in-memory
copy (`secure.Wipe`) before returning — the same discipline every other
software key-custody path in this repository already holds
([certificate-authority](../certificate-authority/architecture.md),
[barrier-seal](../barrier-seal/architecture.md)). A file that is not exactly
32 bytes is refused with a typed error naming the length found, never
silently truncated, padded, or hashed down to size.

### Alternatives rejected

- **A raw key passed as `[]byte` straight into `BackupOptions`, no
  provider seam at all.** Rejected: this is the shape that shipped and
  never actually encrypted anything (the problem this feature fixes), and
  it has no composition point for a hardware backend. See
  [Key hygiene](#key-hygiene-the-key-never-rides-in-a-flag-value) for the
  narrow, explicitly-labelled exception this design keeps for operators who
  are not using a key file.
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
| The planned tenant DEK keyring integration | `pkg/tenant` — see [tenant-dek](../tenant-dek/architecture.md) | go-qrdb (not this lane) |

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
      store nonce || ciphertext
  wrapped_dek  := provider.Wrap(ctx, keyRef, dek)
  secure.Wipe(dek)
  store { format_version, provider_id, key_ref, wrapped_dek } in backup metadata

restore:
  read { format_version, provider_id, key_ref, wrapped_dek } from backup metadata
  provider     := registry.Lookup(provider_id)        // refused if unregistered
  dek          := provider.Unwrap(ctx, key_ref, wrapped_dek)   // *UnwrapFailedError on mismatch/tamper
  for each shard/artifact:
      nonce, ciphertext := split(stored bytes)
      aad      := backup metadata (same fields, recomputed — not re-read from the untrusted artifact)
      plaintext := AES-256-GCM-Open(dek, nonce, ciphertext, aad)   // *BackupDecryptionFailedError on failure
  secure.Wipe(dek)
```

**AAD binds each shard/artifact to the backup it was sealed under.** The
additional authenticated data is derived from the backup's own metadata
(its ID, destination, and the shard ID this specific piece belongs to), not
stored inside the ciphertext — so a shard swapped in from a different
backup, or between two shards of the same backup, fails authentication on
`Open` rather than decrypting into the wrong place silently.

**`format_version` is recorded, not inferred.** A backup's metadata carries
the envelope format version it was written under, so a future format change
(a different AEAD, a different AAD shape) can read an old backup correctly
rather than guessing from its absence. Version `1` is this document's
format, above.

**`none` backups carry no envelope at all** and stay exactly as readable as
they always were — this feature changes nothing about the `none` path
except, per the owner's standing ruling
([backup-restore/architecture.md](../backup-restore/architecture.md)),
that it is reached only by explicit request, never by default.

## Backup integration

**"Encrypted by default" now means a configured KEK provider or key
reference, not merely a raw key byte slice.** `CreateBackup`'s refusal when
the default applies and nothing is configured — `BackupEncryptionRequiredError`
— is unchanged in spirit and in when it fires; what changed is what
satisfies it: a `KeyProviderID` + `KeyRef` pair that resolves through the
registry, OR (see [Key hygiene](#key-hygiene-the-key-never-rides-in-a-flag-value))
an inline 32-byte key for the common case where an operator has not set up a
key file or a hardware provider.

```go
type BackupOptions struct {
	Destination   string
	Compression   CompressionType
	Encryption    EncryptionType // unchanged: "aes256" (default) | "none"

	// KeyProviderID names the registered keywrap.Provider to wrap this
	// backup's DEK with. Defaults to "software" when EncryptionKey or
	// KeyRef is set and this is left empty.
	KeyProviderID string

	// KeyRef is the key reference the named provider resolves --
	// typically a key-file path for "software". Mutually exclusive with
	// EncryptionKey.
	KeyRef string

	// EncryptionKey, when set, is used directly as 32 raw KEK bytes
	// through an unregistered, in-memory SoftwareProvider instance --
	// the inline path. Mutually exclusive with KeyRef. Deprecated in
	// favor of KeyRef pointing at a key file: see Key hygiene.
	EncryptionKey []byte

	Metadata map[string]string
	ShardIDs []uint64
}
```

**Open design question for owner review**, flagged rather than decided
silently: keeping `EncryptionKey` as an inline-bytes escape hatch alongside
the new `KeyRef` is this document's proposal for backward compatibility with
the already-shipped `--encryption-key` flag and the SDK field of the same
name; an alternative is retiring `EncryptionKey` entirely in favor of
`KeyRef` pointing at a (possibly temp) file, closing the "key rides in a
flag value" gap outright instead of merely warning about it. This document
takes the warn-and-keep position; the owner's call stands if it differs.

`BackupInfo` gains one new field, safe to expose on every transport because
it names a provider, not a key: `KeyProviderID string` (empty for `none`
backups). The wrapped DEK, key reference, and format version are NOT
surfaced on `BackupInfo` — they live in the backup's own persisted manifest
(wherever shard data already lives: the in-memory registry or the
configured `BackupSink`), read back only by `RestoreBackup` and `VerifyBackup`
internally.

A GCM authentication failure on `Unwrap` (wrong KEK) or on a shard's own
`Open` (tampered ciphertext) both surface as `*BackupDecryptionFailedError`
(`InvalidArgument`) on every transport — one typed error for both causes,
deliberately: a caller given "wrong key" versus "tampered" as distinguishable
outcomes could use that distinction to probe for the correct key one shard
at a time.

## Key hygiene: the key never rides in a flag value, by default

**`--encryption-key-file <path>` (and `-` for stdin) is the documented way**
to supply the software provider's 32-byte key: the CLI reads it once, passes
only the resulting bytes to `KeyRef`'s underlying file read (for a path) or
directly to an inline provider construction (for stdin, which has no
path to re-read), and never echoes it.

**`--encryption-key <base64>` is kept, with a warning.** A key passed as a
flag value is visible in `ps`, in shell history, and in any process-listing
tool on the host for the life of the process — a strictly worse exposure
than a file the operator controls the permissions of. The CLI prints a
warning naming this exposure when `--encryption-key` is used; it is not
refused, because an operator scripting around a secrets manager that already
injects the value as an argument has a working setup this flag should not
break.

The desktop/web UI's key field is a password-masked input (`type="password"`
/ the Svelte equivalent), not a plaintext field, regardless of which path
(inline key or key-file reference) the operator chooses.

## Target: KMS references are not re-added here

**An earlier round of this feature's own backup work promised a
"KMS key reference" in error text, SDK doc comments, the TypeScript types,
CLI help and the CHANGELOG — before this seam existed to make that promise
true.** Every one of those mentions is removed in the same change that adds
this document, not merely left stale: this seam is what makes a KMS
reference meaningful (a `KeyProviderID` naming a go-xkms-registered cloud-KMS
provider, with `KeyRef` as that KMS's own key identifier), and until a real
provider is registered, naming "KMS" anywhere in this repository's
qrdb-only documentation described a capability that did not exist.

**Target, once go-xkms registers a cloud-KMS provider:** `KeyProviderID`
names it (`"aws-kms"`, `"gcp-kms"`, `"azure-kv"`, `"vault"`, matching
whatever go-xkms calls its own registrations), and `KeyRef` carries that
service's own key identifier (an ARN, a resource name, a path) — exactly
the shape [Injection](#injection) below composes today for hardware.

## Injection

An embedder registers a hardware or cloud-KMS provider at composition time:

```go
registry := keywrap.NewDefaultRegistry() // pre-populated with "software"
registry.Register("tpm2", func() (keywrap.Provider, error) {
	return xkmsKeywrap.NewTPM2Provider(tpmHandle), nil
})
```

and wires the registry into whichever composition root needs it through
`WithKeyWrapRegistry(registry)`, the same `With*` composition shape
`WithStrategyRegistry` already uses for sealing strategies — on `qrdbd`,
`sdk/go/node`, and the embedded SDK alike. `WithKeyWrapProvider(p
keywrap.Provider)` is the narrower sibling for an embedder with exactly one
provider and no registry of its own, installing it under its own
`ProviderID()` in an otherwise-default registry.

**Two options, not one role-parameterized option**, for the same reason
`certificate-authority`'s Key A / Key B split rejected a role parameter:
backup's registry and the tenant DEK keyring's (future) registry are
independently composed, so an embedder that wants hardware backing for one
but not the other is not forced to choose for both at once by a single
shared argument.

## Planned: the tenant DEK keyring

**Not touched by this lane.** [tenant-dek](../tenant-dek/architecture.md)
wraps its per-tenant DEKs with the tenant's own `seal.EncryptionBarrier`
today. The owner has approved a `DEKProvider` seam for it that will wrap
those same DEKs through **this same `keywrap.Provider`/`Registry`**
instead — one KEK seam, two consumers, rather than a second one invented for
the keyring. That integration is out of scope here; this document records
the target so the keyring's own future design does not reinvent `Provider`'s
shape.

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
  package validates its length and zeroes its own in-memory copy; file
  permissions, at-rest encryption of the file itself, and backup of the key
  file (never inside a qrdb backup) are the operator's responsibility,
  exactly as for every other software-custody key in this repository.
- **`--encryption-key`'s flag-value exposure is a documented, warned trade-off**,
  not a silent one — see [Key hygiene](#key-hygiene-the-key-never-rides-in-a-flag-value).

## What this feature deliberately does not do

- **No key derivation.** Every DEK is fresh random; nothing here derives a
  key from a passphrase (that is `pkg/seal`'s job for the barrier itself).
- **No key rotation workflow for the KEK.** Rotating a key file's contents,
  or a hardware KEK, is the provider's and the operator's concern; this
  seam only wraps and unwraps under whatever `keyRef` currently resolves to.
  A backup's own wrapped DEK is never re-wrapped after the fact — restoring
  an old backup still asks for the KEK that wrapped it.
- **No multi-recipient envelopes.** One `keyRef`, one provider, one wrapped
  DEK per backup — not an envelope multiple KEKs can independently open.
