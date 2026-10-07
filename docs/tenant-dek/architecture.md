# Tenant DEK Keyring — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

A per-tenant, versioned keyring of AES-256-GCM data encryption keys. A caller
encrypts and decrypts its own data through the server; the keys never leave
it.

## Problem

An application layered on qrdb (a secrets manager, a parameter store) must keep
its records encrypted under a key that belongs to the tenant. That needs four
properties that an at-rest barrier alone does not give:

- **The key never leaves the server.** The caller hands over plaintext and gets
  back ciphertext, and the reverse.
- **Rotation keeps old data readable.** A new key version must not strand every
  record written under the previous one.
- **Context binding.** A ciphertext moved from one record to another, or from
  one tenant to another, must fail to decrypt.
- **One key per tenant, scoped by identity.** A caller reaches its own tenant's
  keys and no other's.

## Shape chosen

One keyring per tenant, held as **one record in the tenant's own resolved
store** — the store `tenant.StoreResolver` returns for that tenant, the same one
that holds its users and roles.

```
caller ─► transport ─► service.DEKService ─► dek.Keyring ─► StoreResolver.Resolve(tenant)
                       (tenant scoping)      (versions,       │
                                              AEAD)           ├─ prefix tenant:    "<tenant>/__sys:dek/keyring"
                                                              │                    in the replicated shared store
                                                              └─ dedicated tenant: "__tiam:<tenant>/__sys:dek/keyring"
                                                                                   in the replicated shared store,
                                                                                   encrypted under the tenant's barrier
```

Riding the resolver gives the keyring every property the tenant's identity
already has, with no second mechanism:

| Property | How the keyring gets it |
|---|---|
| Wrapped by the tenant's barrier | A dedicated-isolation tenant's resolved store encrypts every value with that tenant's barrier. A prefix tenant has no barrier of its own; its record is protected at rest by the system barrier. |
| Replicated | The resolved store is the system shard's replicated KV. Every node reads the same keyring, so any node decrypts what any node encrypted, and a rotation or retirement on one node is in force on all of them at the next call. |
| Survives restart | It is a durable KV record, not process memory. |
| Fails closed when sealed | The resolver refuses a sealed tenant; the keyring answers `ErrKeyringUnavailable`. |
| Dies with the tenant | The teardown fence makes the tenant unresolvable, then the barrier destroy and keyspace reclaim remove the record. |

### Application keys are not barrier epochs

The keyring's keys are independent random 256-bit keys, not the barrier's
storage epochs. Rotating or retiring a keyring version never touches the
tenant's stored data, and rotating the barrier never invalidates a keyring
ciphertext. Reusing the barrier epochs was considered and rejected: retiring an
old epoch would also strand any of the tenant's stored data still under it.

### Versions

- **Encrypt** seals under the current version. The first Encrypt (or Rotate)
  creates version 1.
- **Rotate** adds a version and makes it current. Earlier versions keep
  decrypting.
- **Rewrap** moves a ciphertext to the current version without returning its
  plaintext.
- **Retire** destroys an old version's key. The current version is refused.
  Retirement is irreversible, and the keyring cannot know where ciphertexts are
  stored, so the caller rewraps everything under a version before retiring it.

Updates use compare-and-swap on the record, so concurrent rotations on
different nodes each add a version; none overwrites another.

### Key wrapping

Each version's own key is itself a data-encrypting key (DEK), wrapped
under a key-encrypting key (KEK) through
[pkg/keywrap](../key-wrapping/architecture.md)'s `Provider`/`Registry`
seam — the same seam [backup](../backup-restore/architecture.md) uses.
`dek.NewKeyring` resolves the built-in `"software"` provider by default,
confined to an operator-configured key directory (`dek.WithKeyDir`,
`lifecycle.Infra.DEKKeyDir`, `qrdbd --dek-key-dir`);
`dek.WithKeyWrapRegistry`/`WithKeyWrapProvider` install a
go-xkms-registered hardware or cloud-KMS provider instead — the SAME
registry/provider Backup consumes
(`lifecycle.Infra.KeyWrapRegistry`/`KeyWrapProvider`), so an embedder
registers a hardware KEK once and both consumers resolve it.

**Each tenant's key reference is tenant-scoped and configurable.**
`dek.WithKeyRef` computes it; the default is the tenant ID itself, which
gives every tenant a distinct KEK reference under the configured provider
with no further composition required. For the built-in `"software"`
provider this means one 32-byte key file per tenant, named after the
tenant's ID, inside the configured key directory.

**The stored record never carries a version's plaintext key** — only its
wrapped bytes, the provider ID that wrapped it, and the key reference it
was wrapped under. `Status` surfaces the provider ID and key reference for
each version (never the key itself), so an operator can audit which KEK
protects which version. Changing the active provider or key reference
(`WithProviderID`, `WithKeyRef`) affects only NEW versions; an existing
version keeps the provider and reference it was wrapped under, and
`Rewrap` moves a ciphertext's key to the current version, wrapped under
the CURRENT provider and reference.

**There is no plaintext escape hatch.** Unlike backup, which may create an
explicitly plaintext backup, every tenant DEK keyring version is always
wrapped under a KEK: `Encrypt`/`Rotate` refuse with a typed, server-side
error (`*dek.KeyDirNotConfiguredError`) when the built-in `"software"`
provider is resolved but no key directory was ever configured, rather than
falling back to an unwrapped key or an implicit location. An unregistered
provider ID, a bad key reference, or a KEK authentication failure
(wrong KEK, or a tampered wrapped key) collapse into one answer,
`*dek.KeyWrapFailedError` — deliberately not distinguished further, the
same discipline `*AuthenticationFailedError` holds one layer up.

**A record written before this integration existed stored a version's key
in plaintext.** Such a record is refused with a typed error,
`*dek.LegacyKeyringFormatError`, on every verb that reads it — never
treated as merely corrupt, and never migrated to the new format silently.

### Ciphertext

Opaque to callers. Internally:

```
"QDEK" (4) | format 0x01 (1) | key version, big-endian (8) | nonce (12) | ciphertext | tag (16)
```

The GCM additional data is the 13-byte header, the length-prefixed tenant ID
and the caller's associated data. Changing the version, the tenant or the AAD
fails authentication.

### Tenant scoping

Every operation acts on the **caller's** tenant: the tenant authentication
bound to the request, or the system tenant for an unbound in-process caller. A
request naming any other tenant is refused with `*dek.CrossTenantError`
(403 / `PermissionDenied`) — including from the system tenant. The permission
model's system-tenant rule lifts self-scope permissions across tenants; the
keyring service deliberately does not.

### Barrier protection `none`

A barrier configured with `none` stores data in plaintext, and so it stores the
keyring record: the keyring follows the barrier's at-rest protection exactly as
identity records do. Encrypt and decrypt still work; what is absent is at-rest
protection of the keys themselves. This matches qrdb's existing `none`
semantics — a deliberate operator choice for dev/test or OS-level full-disk
encryption, refused by go-xkms's `--require-encryption` gate — rather than
adding a refusal only this feature would make. Operators that need at-rest
protection select a keyed barrier strategy; enabling encryption later migrates
the keyring record with the rest of the tenant's records.

## What it replaced

Before this feature, `pkg/dek` held a cluster DEK in process memory only — never
initialised by the daemon, never persisted, discarded on rotation — and tenant
DEKs wrapped by it in a node-local Pebble directory. A restart or a second node
could not read them, no transport served them, and `DEKClient` returned only
metadata. A third, unused tenant key manager (`seal.TenantKeyManager`) also
existed. All three are gone; this keyring is the one tenant DEK in the stack.

## Alternatives rejected

- **Expose the barrier's epoch AEAD directly.** Least code, but retiring a
  version would endanger the tenant's stored data, and `none` and hardware
  barriers cannot rotate.
- **Persist and replicate the old cluster-DEK hierarchy.** A second key
  hierarchy beside the barriers, with its own rotation and recovery story, for
  no property the barrier-wrapped record lacks.
- **Cache keys in memory per node.** A node would keep using a version another
  node had just retired. Each call reads the replicated record instead.

## Related

- [engineering.md](engineering.md) · [api.md](api.md) · [cli.md](cli.md) · [user.md](user.md)
- [multi-tenancy](../multi-tenancy/architecture.md) — isolation modes and tenant teardown
- [barrier-seal](../barrier-seal/architecture.md) — the barrier that wraps the keyring's STORED RECORD
- [key-wrapping](../key-wrapping/architecture.md) — the KEK seam that wraps each version's KEY
