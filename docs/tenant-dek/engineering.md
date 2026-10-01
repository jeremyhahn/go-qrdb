# Tenant DEK Keyring — Engineering

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-01

## Packages

| Package | Role |
|---|---|
| `pkg/dek` | `Keyring`: versions, the stored record, the AEAD, typed errors. Depends only on `pkg/kvstore`, `pkg/crypto/aesgcm` and `pkg/secure`. |
| `pkg/service` | `DEKServicer` / `DEKService`: the one place tenant scoping is decided, shared by every transport. |
| `pkg/api/dekwire` | Keyring results to wire shapes, and the error classification every transport renders. |
| `pkg/api/{rest,grpc,quic,mcp}` | Transport handlers; `pkg/api/unix` serves the gRPC handlers over a socket. |
| `sdk/go/transport` | `DEKClient` and its request/response types. |
| `sdk/go/transport/{rest,grpc,unix,quic,mcp,embedded}` | Client implementations; gRPC and Unix share `internal/dekconv`. |
| `pkg/server/lifecycle` | Builds `Node.DEK` over `Node.StoreResolver` in both the seal-enabled and seal-disabled branches. |

## Composition

`lifecycle.BuildServices` builds the keyring wherever it builds a
`StoreResolver`, which is every composition:

```go
keyring, _ := dek.NewKeyring(node.StoreResolver)
node.DEK, _ = service.NewDEKService(keyring)
```

`cmd/qrdbd` and `sdk/go/node` pass `server.WithDEKService(svcNode.DEK)`, which
threads it to the REST, gRPC, QUIC, Unix and MCP servers; `sdk/go/node` also
passes `embedded.WithDEKService` so `node.Client().DEK()` works in process.

## The stored record

Key `__sys:dek/keyring` inside the tenant's resolved store. JSON:

```json
{"format":1,"current":3,"versions":[
  {"version":1,"created_at":"...","retired_at":"..."},
  {"version":2,"key":"<base64>","created_at":"..."},
  {"version":3,"key":"<base64>","created_at":"..."}]}
```

A retired version keeps its number and timestamps and loses its key. On read,
a record whose format is unknown, which has no versions, or whose current
version has no usable 32-byte key is refused as `ErrCorruptKeyring` rather than
used.

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

`ErrAuthenticationFailed` deliberately does not say whether the cause was
tampering, a different AAD or another tenant's ciphertext; saying which would
be an oracle.

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
