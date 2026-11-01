# Credentials and Secret Resolution — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-27
> Conflicts with this document are defects. Fix them here first.

## Exported surface

`sdk/go`:

```go
func (c *Client) Credentials() transport.CredentialClient
```

`pkg/service` — the interface every transport handler routes through:

```go
type CredentialServicer interface {
    // Classic CRUD
    CredentialStore(ctx context.Context, name string, value []byte) error
    CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error)
    CredentialSubmit(ctx context.Context, name string, value []byte) error
    CredentialDelete(ctx context.Context, name string) error
    CredentialStrategy(ctx context.Context) (string, error)

    // Versioned rotation
    CredentialList(ctx context.Context) ([]string, error)
    CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error)
    CredentialRetrieveVersionBuffer(ctx context.Context, name string, version uint64) (*secure.Buffer, error)
    CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error)
    CredentialGCExpired(ctx context.Context, name string) (int, error)
}
```

`CredentialService` (`pkg/service/credential_service.go`) is the canonical
implementation. It resolves the caller's tenant's store on every call and then
passes through to that `credentials.CredentialService`. The system tenant's
store is the node-wide service. A tenant's store is built over its dedicated
barrier with `credentials.CredentialService.ForBarrier`, which refuses a nil
barrier with `ErrNilBarrier` and shares the parent's audit logger.
`service.WithCredentialTenantBarriers(registry)` binds the
`seal.BarrierRegistry` the tenant barriers come from. The node's lifecycle
passes it whenever the node has a registry; without it, every tenant call is
refused. The embedded transport is handed the same `CredentialServicer` as
the network transports (`embedded.WithCredentialService`).

`transport.CredentialClient` (the SDK surface) carries the same pair:
`CredentialRetrieveBuffer(ctx, name)` and
`CredentialRetrieveVersionBuffer(ctx, req)`, each returning a
`*secure.Buffer` the caller must `Destroy`. Neither it nor
`credentials.CredentialService` has a `[]byte` retrieve method.

`make check-no-deprecated-credential-retrieve`
(`test/buildgates/no_deprecated_credential_retrieve_test.go`, in CI's `lint-gates` job)
fails if any Go file reintroduces a deleted credential retrieval declaration
that returns `[]byte`, or uses the unambiguous removed store-level selectors.
The protobuf `QRDBService` methods of the same names are allowed because they
return protobuf response messages, not heap credential bytes. A test that needs
the bytes uses its package's `retrieveBytes` helper (`retrieveBytesFrom` in
`pkg/service`), which goes through the
Buffer method and copies out.

**Protected-memory handle, end to end.** Retrieval returns a go-enclave-backed
`*secure.Buffer`. The architecture document's
[Protected-memory boundary](architecture.md#protected-memory-boundary) section
is the contract for where bytes may leave it. The rules a contributor works to:

- A handler that must put the value on the wire does so through
  `service.CredentialWireValue(buf)` and nowhere else. It copies out under a
  held borrow and destroys the Buffer; the returned slice is the wire DTO.
  REST, QUIC and MCP handlers wipe it after encoding; gRPC cannot, because it
  marshals after the handler returns.
- An SDK remote transport builds its Buffer from its own wire response. REST and
  MCP decode base64
  directly into protected memory (`transport.WireValue` as the JSON field type,
  `transport.BufferFromBase64`); gRPC, Unix and QUIC move the protobuf-owned
  slice in with `transport.BufferFromWire`, which wipes it. An empty
  wire value is refused with `*secure.InvalidSizeError` — the service never
  returns an empty credential, so an empty one is a fault, not a value.
- `secret.NewSecretValueFromBuffer` seals a Buffer into a `SecretValue`
  directly from protected pages and consumes the Buffer (including on failure,
  as `*secret.SealFailedError`). `CredentialResolver` calls it; there is no
  copy through a heap slice.
- The embedded SDK transport passes the Buffer through untouched. Its Buffer
  methods are declared in the embedded authorization registry under their own
  names, mapped to the `credentials:retrieve` /
  `credentials:retrieve_version` permissions that guard the wire retrieve
  operations, so an embedded caller is authorized as a remote one is.
- A test double that wraps a `[]byte` hook must clone it before
  `secure.NewBufferFromBytes`, which wipes its source; the real service never
  returns an empty value, and a double must not either.

`pkg/secret` provides the secret-reference resolver SPI: `SecretValue`,
`ConfigProcessor`, and the `credential://` / `literal://` schemes.

## Strategies

| Strategy | Storage | Read semantics |
|---|---|---|
| `manual` | go-enclave sealed enclave (`secure.Secret`) | **blocks until an operator submits the value**; a read opens the enclave straight into a fresh `*secure.Buffer` |
| `barrier` | encrypt-then-store through `seal.StorageBarrier` | the default; unreadable while sealed |

`CredentialStrategy(ctx)` reports the strategy of the caller's store: the
configured one for the system tenant, always `barrier` for a tenant. A strategy that cannot
enumerate returns `ErrEnumerationNotSupported` from `ListCredentials` rather
than change the interface.

## Invariants

- **Credentials never live in the user KV store.** They are distinct from the
  KEK/DEK custody in the barrier and from the Raft data plane.
- **`pkg/credentials` is the only secret-bearing store in qrdb.** There is no
  `secrets` or `kms` package.
- **A tenant's credentials live under the tenant's own barrier, never in the
  system store.** The store is resolved from the caller's tenant, never from a
  request field. A tenant on the shared system barrier is refused, not given
  the node-wide store or a prefix inside it. Names and values in one tenant are
  invisible to every other tenant and to the platform.
- **Concurrent rotations of one name are not serialized.** Each reads the
  current version and writes the next; the later write wins. This is a known
  property, not a defect of tenancy; serializing them belongs to the row-lock
  work.
- **`manual` values are never raw heap bytes.** They live in enclaves;
  a read opens the enclave into a fresh `*secure.Buffer`; the enclave is
  destroyed on delete and on a rotation with no grace period.
- **A read copies under a held borrow, and refuses an unreadable enclave.**
  The copy out of protected memory runs inside a single coordinated borrow
  (`secure.CopyOut`) rather than sizing the destination and then reading it as
  two separate calls, which would leave a gap between them in which a
  concurrent destroy of that same enclave (`enclave.WipeAll`, or any other
  caller with a handle to it) could land. An enclave whose pages are already
  gone by the time the read happens yields nothing, and the read **fails
  closed** rather than returning a correctly sized run of zeros that a caller
  cannot tell from the real credential. The same applies to a resolved
  `SecretValue`: its plaintext is read only through `WithBytes` or `Open`, and
  `WithBytes` holds the SAME enclave's
  borrow for its callback's entire duration rather than releasing it before
  the callback runs — a concurrent destroy blocks against that borrow instead
  of racing it.
- **A resolved value is handed on in place.** `NewSecretValueFromBuffer` seals
  the Buffer's own locked pages, never a heap copy of them, and a `WithBytes`
  callback is the place to hand the plaintext to a KEK, barrier or verifier:
  the callback may allocate, open, borrow and destroy any other protected
  memory, because every go-enclave object carries its own lock, and the only
  nesting that deadlocks — reentering the buffer being borrowed — is out of
  its reach: the callback sees the bytes of a transient buffer, never the
  buffer. Output runs the other way round: a protected destination is
  allocated first and written in place inside the source's borrow.
- **A sealing failure is `*secret.SealFailedError`, and it fails closed rather
  than returning an empty `SecretValue`.** `NewSecretValue` seals a resolved
  value into a `secure.Secret` before returning it; if that seal fails (most
  concretely a process Sealer wiped by `enclave.WipeAll`, or the kernel
  refusing to lock the seal's key pages), a `SecretValue` whose accessors silently reported empty would be
  indistinguishable from a reference that legitimately resolves to nothing —
  a credential could vanish from configuration with no error anywhere.
  `SealFailedError` unwraps to the underlying `secure.NewSecret` failure
  (`errors.Is` against `enclave.ErrSealerWiped`, `enclave.ErrSealFailed`,
  `enclave.ErrMaterialTooLarge` or `enclave.ErrLockedMemoryUnavailable` works
  through it; never `ErrBudgetExhausted`, because sealing is not admitted
  against the process locked-memory budget), and its `Ref` field never carries the resolved
  plaintext: for a literal reference (`literal://...` or an unclaimed bare
  value) the raw reference itself IS the secret rather than a pointer to one,
  and `Ref` is redacted to `"[redacted]"` in that case — computed once at
  construction and the only form of the reference this error retains, so
  neither `Error()`, `%#v`, reflection, nor JSON encoding can recover the
  plaintext through it. For every other strategy `Ref` is the raw reference
  verbatim, since it only ever pointed at the secret rather than being it.
- **An unreadable resolved value is `*secret.ResolvedReadError`, never `""`.**
  When `ConfigProcessor.Process` resolves a plain `string` field but cannot
  read the sealed result back (`SecretValue` open or copy failure), it leaves
  the reference in the field, records the path as `Unresolved`, records a
  `ResolvedReadError` wrapping the cause, and does not count it `Resolved`.
  See [secret-resolution.md](secret-resolution.md).
- **Rotation takes a grace period.** The previous version stays retrievable for
  that long; `0` wipes it immediately.
- **`retrieve-version 0` means the current version.**
- **`delete` removes the credential and its non-current versions.** Under
  `barrier` this means the current value, the `#meta` sidecar, and every
  `#v<N>` history entry regardless of grace status — not just the ones GC
  would have reaped on its own. `DeleteCredential`'s barrier branch
  (`deleteBarrierAll`) shares the per-version deletion loop with `gcBarrier`
  (`deleteBarrierVersionKeys`) rather than duplicating it; `gcBarrier` filters
  to expired entries first, `deleteBarrierAll` passes the whole history.
  Versions are deleted before the meta sidecar, which is deleted before the
  current value, so a failure partway through never leaves a version
  reachable: a stale meta entry whose version key is already gone reads as
  expired, and a stale version key with no meta entry reads as not-found.
  Under `manual` the enclave-backed version map and meta sidecar are dropped
  under the same lock as the current credential.
- **GC reaps only expired historical versions** — it never touches the current
  one.
- **A protected config field resolves to a `SecretValue`, never a string.** It
  must reference a secret held elsewhere, so an inline `literal://` value is
  refused, and resolution **fails closed** when it cannot.
- **PINs are not credentials.** `pkg/pin` owns them, because PIN lockout is
  backend-native and credentials have no equivalent.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Retrieve under `manual` with nothing submitted | blocks until submitted | **yes** — never returns an empty value |
| Retrieve a version past its grace period | not-found | **yes** |
| Barrier sealed with the `barrier` strategy | refused | **yes** |
| Protected config field with no secret reference | resolution fails | **yes** |
| Unresolvable `credential://` reference at boot | boot fails | **yes** |
| Resolved value cannot be sealed (`NewSecretValue`) | `*secret.SealFailedError`, resolution fails | **yes** |
| `ListCredentials` on a backend that cannot enumerate | `ErrEnumerationNotSupported` | n/a — explicit, not an empty list |
| Missing `credentials:*` permission | permission denied | **yes** |
| Tenant on the shared system barrier | `*service.CapabilityUnsupportedError` (no dedicated barrier) | **yes**, never the node-wide store |
| Tenant whose dedicated barrier is sealed | `*service.BarrierSealedError` | **yes** |
| Tenant caller on a node with no barrier registry bound | `*service.CapabilityUnsupportedError` (not bound) | **yes** |

The "blocks rather than returns empty" property is the load-bearing one for the
ceremony case: a non-resident secret that read as empty would let a boot
continue with no credential at all.

## Permission model

One permission per verb, listed in the [API capability matrix](api.md#capability-matrix)
(`pkg/permission/constants.go`). **Write, rotate and delete custody secret
material and are gated separately from reads.** Every `credentials:*`
permission is tenant self-scope, declared in `pkg/permission/self_scope.go`,
so a tenant administrator can grant it inside the tenant. The grant reaches
only that tenant's store because the store is resolved from the caller's
tenant.

## Configuration

The active strategy is `seal.credential_strategy` in the server configuration
(`manual` or `barrier`; empty selects `barrier`); `credential strategy`
reports it. Secret references in config use the `credential://` and
`literal://` schemes and are processed by `ConfigProcessor` at boot. Scheme
ownership across the stack is documented in
[credentials/secret-resolution.md](../credentials/secret-resolution.md).

## Persistence

`barrier` credentials are stored encrypted through the `StorageBarrier`.
`manual` credentials are **not persisted at all** — they live only in memory
for the life of the process, which is why a restart re-blocks retrieval until
an operator submits again.

Version history carries the version number, the creation time and the
expiry implied by the rotation's grace period.

## Test strategy

**Integration target:** `make integration-test-credentials` (no Docker; selection in `test/integration/features.mk`). It runs the credential CLI suite per protocol, the tenant-store CLI suite per protocol, and the embedded tenant-store suite. Per-transport accounting is the `credentials` row of `test/integration/COVERAGE.md`.

- **Unit**: store/retrieve/submit/delete for both strategies, rotation with and
  without a grace period, versioned retrieval including `0` meaning current, GC
  reaping only expired versions, and `CredentialStrategy()`.
- **The enclave lifecycle is tested, not assumed**: destroy on delete and on
  rotation, and opening into a fresh Buffer on read.
- **Delete is tested against the full version history, not just the current
  value**: rotate with a nonzero grace period, delete, then assert every
  version — current and in-grace history alike — is unretrievable, on both
  strategies, across a simulated process restart (a fresh `StorageBarrier`
  unsealing the same durable backend), through `CredentialServicer`, and
  through every CLI-exposed transport (REST, gRPC, QUIC, MCP, Unix).
- **The Buffer path is tested at every hop**: the store (both strategies,
  borrow tracing so no read holds a borrow past its callback), the service
  (`CredentialWireValue` copies out and destroys, and fails closed on a nil or
  already-destroyed Buffer), the resolver (the retrieved Buffer is consumed and
  destroyed; a nil Buffer without an error fails closed), `BufferFromWire`
  (moves, wipes, refuses empty), and each of the six SDK transports (success,
  server error, nil request, closed transport).
- **Tenant stores are tested end to end on every transport**: two tenants
  holding the same name with different values, each reading only its own; one
  tenant's list, retrieve, rotate and delete never reaching the other's or the
  platform's; a principal without the grants refused; and a shared-barrier
  tenant refused for want of a dedicated barrier. The CLI covers REST, gRPC,
  QUIC, MCP and Unix, and the SDK client covers embedded. Unit tests cover
  the service's store resolution, `ForBarrier`, and the lifecycle option that
  binds the registry.
- **Fail-closed tests** for the protected-config-field rule and for an
  unresolvable reference at boot.
- **Error tests** per function.

## Observability

Credential operations are audited through [../audit/](../audit/). **Values are
never logged or included in audit records** — only names, versions and
outcomes.
