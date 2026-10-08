# Tenant DEK Keyring — Engineering

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

## Packages

| Package | Role |
|---|---|
| `pkg/dek` | `Keyring`: versions, the stored record, the AEAD, the KEK wrap/unwrap seam, typed errors. Depends on `pkg/kvstore`, `pkg/crypto/aesgcm`, `pkg/secure` and `pkg/keywrap`. |
| `pkg/service` | `DEKServicer` / `DEKService`: the one place tenant scoping is decided, shared by every transport. |
| `pkg/api/dekwire` | Keyring results to wire shapes, and the error classification every transport renders. |
| `pkg/api/{rest,grpc,quic,mcp}` | Transport handlers; `pkg/api/unix` serves the gRPC handlers over a socket. |
| `sdk/go/transport` | `DEKClient` and its request/response types. |
| `sdk/go/transport/{rest,grpc,unix,quic,mcp,embedded}` | Client implementations; gRPC and Unix share `internal/dekconv`. |
| `pkg/server/lifecycle` | Builds `Node.DEK` over `Node.StoreResolver` in both the seal-enabled and seal-disabled branches. |

## Composition

`lifecycle.BuildServices` builds the keyring wherever it builds a
`StoreResolver`, which is every composition, passing it the SAME key-wrap
registry/provider Backup consumes (`lifecycle.Infra.
KeyWrapRegistry`/`KeyWrapProvider`) plus its own key directory
(`lifecycle.Infra.DEKKeyDir`):

```go
opts := []dek.KeyringOption{dek.WithKeyDir(infra.DEKKeyDir)}
if infra.KeyWrapRegistry != nil {
    opts = append(opts, dek.WithKeyWrapRegistry(infra.KeyWrapRegistry))
} else if infra.KeyWrapProvider != nil {
    opts = append(opts, dek.WithKeyWrapProvider(infra.KeyWrapProvider))
}
keyring, _ := dek.NewKeyring(node.StoreResolver, opts...)
node.DEK, _ = service.NewDEKService(keyring)
```

`WithKeyWrapProvider` is skipped when a shared registry is also supplied:
the registry's composer is responsible for populating it (see
[key-wrapping/architecture.md](../key-wrapping/architecture.md#the-tenant-dek-keyring)),
and registering the same provider a second time into the SAME shared
registry object (once by Backup's own init, once by the keyring's) would
collide. `cmd/qrdbd` and `sdk/go/node` pass `server.WithDEKService(svcNode.DEK)`,
which threads it to the REST, gRPC, QUIC, Unix and MCP servers; `sdk/go/node`
also passes `embedded.WithDEKService` so `node.Client().DEK()` works in
process. `server.WithDEKKeyDir`/`CryptoOptions.DEKKeyDir` forward an
embedder's key directory into `lifecycle.Infra.DEKKeyDir` up front, the
same `ResolveCryptoOptions` shape `WithKeyDir`/`CryptoOptions.KeyDir` use
for Backup.

### The engine seam: `dek.Provider`

`service.NewDEKService` takes a `dek.Provider`, not a `*dek.Keyring`. The
interface is `Status`, `Rotate`, `Retire`, `Encrypt`, `Decrypt` and `Rewrap`,
each acting on the tenant it is given, and carries operations only: no method
takes or returns a key. `dek.Keyring` is the software default and satisfies it
(compile-time asserted).

A composer replaces the engine outright with `server.WithDEKProvider(p)` or
`node.WithDEKProvider(p)`. `server.ResolveCryptoOptions` carries it into
`lifecycle.Infra.DEKProvider`, and `BuildServices` uses it in place of the
keyring it would otherwise build; `qrdbd` and `sdk/go/node` both forward it, in
the standalone and the clustered boot paths. The provider's owner closes it.
Tenant scoping stays in `service.DEKService`, above the seam, so a provider
sees only a tenant the service already resolved.

This seam is **distinct** from `keywrap.Provider`. Keywrap wraps a key a
`Keyring` holds, and an embedder that keeps the software keyring still registers
a hardware KEK through it. `dek.Provider` is for a device that holds the tenant
keys itself and runs the AEAD, the rotation and the retirement. `dektest.Device`
(`pkg/dek/dektest`) is the fake: it keeps its keys behind the interface and
counts operations, which is what the tests assert to show no key bytes cross
the seam. See [extension-seams](../release/extension-seams.md).

## The stored record

Key `__sys:dek/keyring` inside the tenant's resolved store. JSON (format
2 — a version's key is wrapped, never plaintext):

```json
{"format":2,"current":3,"versions":[
  {"version":1,"created_at":"...","retired_at":"..."},
  {"version":2,"wrapped_key":"<base64>","provider_id":"software","key_ref":"acme","created_at":"..."},
  {"version":3,"wrapped_key":"<base64>","provider_id":"software","key_ref":"acme","created_at":"..."}]}
```

A retired version keeps its number and timestamps and loses its wrapped
key, provider ID and key reference. On read, a record whose format is
unknown, which has no versions, or whose current version has no usable
wrapped key (missing wrapped bytes, provider ID or key reference) is
refused as `ErrCorruptKeyring` rather than used. A record stored in the
PRE-key-wrap format (format 1 — a plaintext key per version) is refused
as `ErrLegacyKeyringFormat` instead, checked before the generic corruption
check so the refusal names the real reason; see
[architecture.md](architecture.md#key-wrapping).

## Concurrency

Every mutation is read–modify–write with **compare-and-swap** on the record
(`PutIfAbsent` to create). The resolved store must implement
`kvstore.AtomicKVStore`; one that does not is refused (`ErrKeyringUnavailable`
wrapping `*AtomicStoreRequiredError`) rather than raced. A lost CAS retries, up
to 16 attempts; every round has a winner, so the limit is only reached under
sustained contention (`ErrConcurrentUpdate`).

The keyring holds no keys between calls: each operation reads the record,
applies the key and wipes the decoded key bytes. That keeps every node
consistent with the replicated record at the cost of one store read per call.

## Error classes

Every error matches exactly one sentinel under `errors.Is`; `dekwire.Code` maps
the class to a status once, and each transport renders that status.

| Sentinel | gRPC / QUIC / MCP | REST |
|---|---|---|
| `ErrInvalidInput` | `InvalidArgument` | 400 |
| `ErrCrossTenant` | `PermissionDenied` | 403 |
| `ErrMalformedCiphertext` | `InvalidArgument` | 400 |
| `ErrAuthenticationFailed` | `InvalidArgument` | 400 |
| `ErrUnknownVersion` | `NotFound` | 404 |
| `ErrVersionRetired` | `FailedPrecondition` | 409 |
| `ErrRetireCurrentVersion` | `FailedPrecondition` | 409 |
| `ErrKeyringUnavailable` | `Unavailable` | 503 |
| `ErrCorruptKeyring` | `DataLoss` | 500 |
| `ErrConcurrentUpdate` | `Aborted` | 409 |
| `ErrCrypto` | `Internal` | 500 |
| `ErrLegacyKeyringFormat` | `DataLoss` | 500 |
| `ErrKeyDirNotConfigured` | `Unimplemented` | 404 (route absent) |
| `ErrKeyWrapFailed` | `InvalidArgument` | 400 |

`ErrAuthenticationFailed` deliberately does not say whether the cause was
tampering, a different AAD or another tenant's ciphertext; saying which would
be an oracle. `ErrKeyWrapFailed` holds the identical discipline one layer
down, at the KEK: an unregistered provider ID, a bad key reference and a
KEK authentication failure are one answer. `ErrKeyDirNotConfigured` is
different in kind — a SERVER capability gap (no `--dek-key-dir`/
`dek.WithKeyDir` ever configured), never a caller-supplied value the
request could fix — so it is never folded into `ErrKeyWrapFailed`, the
same split `*service.BackupKeyDirNotConfiguredError` holds for backup.

## Permissions

Declared on the gRPC method option and registered for every transport:

| Permission | Scope | Duty | Built-in roles |
|---|---|---|---|
| `dek:encrypt` | self | crypto-use | Operator, User |
| `dek:decrypt` | self | crypto-use | Operator, User |
| `dek:rewrap` | self | crypto-use | Admin, Operator |
| `dek:rotate` | self | — | Admin |
| `dek:retire` | self | — | Admin |
| `dek:status` | self | — | Admin, Operator |

The crypto-use verbs are on the Security Officer deny-list (`dek:rewrap` by
resource, the other two by verb).

## Teardown

There is no DEK-shred step. The fence makes the tenant unresolvable, so the
keyring is unusable from that point; the barrier destroy makes a dedicated
tenant's record undecryptable; the keyspace reclaim deletes it.
`service.TenantKeyPrefixes` covers both `<tenant>/` and
`seal.TenantSharedNamespace(tenant)`, so a dedicated tenant's identity records
and keyring are reclaimed too — including under barrier protection `none`,
where they would otherwise survive as plaintext.

## Tests

**Integration target:** `make integration-test-tenant-dek` (no Docker; selection in `test/integration/features.mk`). It runs the keyring CLI suite per protocol and the REST cluster-replication and restart-survival suites. Per-transport accounting is the `tenant-dek` row of `test/integration/COVERAGE.md`.

| Property | Where |
|---|---|
| Versions survive a restart | `pkg/dek` (new keyring over the same stores); `test/integration/dek_test.go` (durable node restarted) |
| Rotation keeps old data decryptable | `pkg/dek`, every transport's round-trip test, both integration suites |
| Rewrap | `pkg/dek`, every transport, both integration suites |
| Cross-tenant refusal | `pkg/service`, every transport, both integration suites |
| Retiring the current version refused | `pkg/dek`, every transport, both integration suites |
| Mismatched AAD fails | `pkg/dek`, every transport, both integration suites |
| Every node of a 3-node cluster decrypts | `test/integration/dek_test.go` |
| Barrier wrapping, sealed refusal, reclaim on delete | `pkg/server/lifecycle/dek_test.go` |
| Concurrent rotations lose no version | `pkg/dek` |
| Every version's key is wrapped (never plaintext) under the configured provider/key reference, default and configured | `pkg/dek` |
| A legacy-format (pre-key-wrap) record is refused, not migrated | `pkg/dek` |
| The built-in "software" provider with no key directory configured refuses minting a key | `pkg/dek` |
| A go-xkms-shaped hardware/cloud-KMS provider double round-trips through the SAME registry seam backup uses | `pkg/dek` |
| Backup and the DEK keyring share one injected key-wrap registry/provider with no collision | `pkg/server/lifecycle` |
| Real `qrdbd` processes mint and unwrap DEK versions through the built-in "software" provider, including across a restart | `test/integration/dek_test.go` |
