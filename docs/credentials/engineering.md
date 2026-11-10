# Credentials and Secret Resolution — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-31
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

    // Scheduled rotation policy
    CredentialSetRotationPolicy(ctx context.Context, name string, policy credentials.RotationPolicy) error
    CredentialRotationPolicy(ctx context.Context, name string) (*CredentialRotationPolicyStatus, error)
}
```

`CredentialService` (`pkg/service/credential_service.go`) is the canonical
implementation. It resolves the caller's tenant's store on every call and then
passes through to that `credentials.CredentialService`. The system tenant's
store is the node-wide service, bound to the one replicated store on the
system shard (see "One store per cluster"). A tenant's store is built over its dedicated
barrier with `credentials.CredentialService.ForBarrier`, which refuses a nil
barrier with `ErrNilBarrier` and shares the parent's logger, audit logger,
rotation value source and clock, so a tenant's scheduled rotations draw from
the same source as the system store's.
`service.WithCredentialTenantBarriers(registry)` binds the
`seal.BarrierRegistry` the tenant barriers come from. The node's lifecycle
passes it whenever the node has a registry; without it, every tenant call is
refused. The embedded transport is handed the same `CredentialServicer` as
the network transports (`embedded.WithCredentialService`).

Its
`CredentialRotateDue(ctx, now)` is the scheduler's entry point, not a wire
operation. `CredentialRotationPolicyFromWire(enabled, interval, retainVersions)`
is the one parser every transport uses to turn the wire fields into a
`credentials.RotationPolicy`. `CredentialRotationPolicyStatus` carries the
policy and `NextRotation`, and renders them for the wire with
`WireInterval()` (a Go duration string) and `WireNextRotation()` (RFC 3339 UTC,
or empty when nothing is scheduled).

`pkg/credentials` — the rotation policy and its value source:

```go
type RotationPolicy struct {
    Enabled        bool
    Interval       time.Duration
    RetainVersions int // versions kept after a scheduled rotation, the current one included
}

type RotationValueSource func(ctx context.Context, name string) (*secure.Buffer, error)

func WithRotationValueSource(src RotationValueSource) Option
```

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
| `barrier` | system records under `__sys:credentials/` (values) and `__sys:credential-meta/`, `__sys:credential-versions/`, `__sys:credential-staged/` on the system shard's replicated KV, encrypted at rest by each node's system barrier | the default; unreadable while sealed |

`CredentialStrategy(ctx)` reports the strategy of the caller's store: the
configured one for the system tenant, always `barrier` for a tenant. A strategy that cannot
enumerate returns `ErrEnumerationNotSupported` from `ListCredentials` rather
than change the interface.

## Invariants

- **Credentials never live in the user KV store.** They are system records
  under the reserved `__sys:` prefix, which user KV calls cannot reach, and
  are distinct from the KEK/DEK custody in the barrier.
- **One store per cluster.** `credentials.Store` is the seam; the service is
  built unbound (`NewCredentialService(cfg, nil, ...)`) by every composer
  (qrdbd, the SDK node, the embedded CLI server) and
  `lifecycle.buildSealServices` calls `BindStore` with the `kvSealBackend`
  over the system shard's KV once it exists. Its `PutIfAbsent` and
  `CompareAndSwap` are Raft transactions, so the rotation claim decides once
  for the cluster. Until the bind every call fails with
  `ErrStoreUnavailable` (503 / `UNAVAILABLE`). No composer may pass a
  node-local store.
- **`pkg/credentials` is the only secret-bearing store in qrdb.** There is no
  `secrets` or `kms` package.
- **A tenant's credentials live under the tenant's own barrier, never in the
  system store.** The store is resolved from the caller's tenant, never from a
  request field. A tenant on the shared system barrier is refused, not given
  the node-wide store or a prefix inside it. Names and values in one tenant are
  invisible to every other tenant and to the platform.
- **Concurrent writers of one name commit one at a time.** A `barrier`
  rotation and a stored value each land in one guarded batch
  (`PutAllIfAllUnchanged`) that applies only while the records it read are
  unchanged; the writer whose batch is refused reads again, so no rotation
  retains a value another writer already replaced and no two rotations take
  the same version (see [Scheduled rotation](#scheduled-rotation)). The store
  must offer that batch; one that does not is refused with
  `ErrStoreNotAtomic`.
- **A legacy key is read from its end.** The node-local legacy store appended
  `#meta` and `#v<n>` to a credential's name, and a name may itself hold a
  `#`. The migration reads a legacy key as the metadata or version record of
  `<name>` only when it ends in exactly `#meta` or `#v<decimal>` and the
  node's legacy store also holds `<name>`'s value (or its migration note, or
  `<name>` is the conflict being resolved); every other key is the value of
  the credential the whole key names. So `db#prod` is its own credential
  whether or not `db` exists, and each migrates, conflicts and resolves under
  its own name. A legacy name that ends in a record suffix of an existing
  credential (`db#meta` beside `db`) was already one and the same record in
  the legacy layout, and is read as that credential's record. The legacy copy
  is deleted value last, so a record left by an interrupted delete still has
  the value that makes it that credential's.
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
- **A credential rotates once per interval across the cluster, not once per
  node.** Every node runs the scheduler, and a due credential is claimed by a
  `CompareAndSwap` on its metadata record that records a claim expiring one
  lease (five minutes) ahead. Only the claim holder calls the
  `RotationValueSource`, so a source with effects outside the store runs once
  per rotation; the others see the claim and do nothing.
  See [Scheduled rotation](#scheduled-rotation).
- **A rotation that does not complete leaves the credential due.**
  `NextRotation` moves one interval past now only when the claimed rotation
  lands. A failed rotation releases its claim, so the next check retries it; a
  claim whose holder crashed lapses after its lease, so the credential is due
  again on any node and after a restart.
- **No writer of the metadata record overwrites another.** The policy, the claim, a
  rotation's version fields and the history trim are each applied by a
  `CompareAndSwap` that re-reads the record on a conflict, so a policy disabled
  or changed concurrently with a scheduled rotation is kept.
- **`retrieve-version 0` means the current version.**
- **`delete` removes the credential and its non-current versions.** Under
  `barrier` this means the current value, the metadata record, and every
  retained version record regardless of grace status — not just the ones GC
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
| Credential store not bound yet (the system shard's KV not built, or a node still sealed at first boot) | `ErrStoreUnavailable`, unavailable on every transport | **yes** — nothing is read or written |
| Protected config field with no secret reference | resolution fails | **yes** |
| Unresolvable `credential://` reference at boot | boot fails | **yes** |
| Resolved value cannot be sealed (`NewSecretValue`) | `*secret.SealFailedError`, resolution fails | **yes** |
| `ListCredentials` on a backend that cannot enumerate | `ErrEnumerationNotSupported` | n/a — explicit, not an empty list |
| Missing `credentials:*` permission | permission denied | **yes** |
| Rotation policy refused: an enabled policy with a non-positive or unparsable interval or `RetainVersions` below 1, or any policy under `manual` | `*credentials.InvalidRotationPolicyError`, invalid-argument on every transport | **yes** — nothing is stored |
| Rotation policy set or read for an unknown name | `ErrCredentialNotFound` | n/a |
| Due rotation with no `RotationValueSource` configured | `*credentials.RotationError` wrapping `ErrNoRotationValueSource`; the scheduler logs it and the credential stays due | **yes** — no rotation, no claim |
| `RotationValueSource` returns an error or a nil Buffer | `*credentials.RotationError` wrapping the cause (`ErrEmptyCredentialValue` for nil); the credential stays due | **yes** |
| Claimed rotation fails after the source (failed or contended commit batch, cancelled scheduler context) | `*credentials.RotationError` wrapping the cause; the claim is released and the credential stays due, so the next check retries, landing the staged value without calling the source again | **yes** — `NextRotation` does not move |
| Node crashes holding a rotation claim | the claim lapses after its five-minute lease; the credential is then due on every node | n/a |
| Expired-version reaping or the history trim fails after a rotation landed | `*credentials.RotationError` wrapping the cause; the rotation is not counted, `NextRotation` has moved, and the record keeps a pending cleanup that every later check retries without calling the value source | **yes** — the trimmed versions stay until the cleanup lands |
| Tenant on the shared system barrier | `*service.CapabilityUnsupportedError` (no dedicated barrier) | **yes**, never the node-wide store |
| Tenant whose dedicated barrier is sealed | `*service.BarrierSealedError` | **yes** |
| Tenant caller on a node with no barrier registry bound | `*service.CapabilityUnsupportedError` (not bound) | **yes** |

The "blocks rather than returns empty" property is the load-bearing one for the
ceremony case: a non-resident secret that read as empty would let a boot
continue with no credential at all.

## Permission model

One permission per verb, listed in the [API capability matrix](api.md#capability-matrix)
(`pkg/permission/constants.go`). **Write, rotate and delete custody secret
material and are gated separately from reads.** Setting and reading a rotation
policy both take `credentials:rotate`: a policy decides when secret material
is replaced, so it is held to the same permission as the rotation itself. Every `credentials:*`
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

`seal.credential_rotation_check_period` sets how often each node's scheduler
looks for due credentials. It is a Go duration and defaults to `1m`; a
non-positive or unparsable value fails configuration validation. A rotation
lands within one check period of its `NextRotation`.

## Scheduled rotation

`SetRotationPolicy` validates the policy, stores it in the credential's metadata
record (`credential-meta/<name>`), and schedules `NextRotation` one interval from now. A disabled policy
clears the schedule. Setting a policy emits a `rotation_policy` audit event.

`qrdbd` starts a scheduler on every node (`pkg/server`,
`credential_rotation_scheduler.go`). Every check period it calls
`CredentialRotateDue`, which sweeps the system store and then the store of
every tenant the `seal.BarrierRegistry` lists (`ListTenants`), in tenant-ID
order, each under that tenant's context, so a tenant's due policy rotates
that tenant's credential and nothing else. A tenant whose barrier is sealed,
or which was removed between the listing and the visit, is skipped; its
policies fall due at the first check after it is unsealed. A tenant whose
sweep fails is reported as a `*CredentialTenantRotationError` naming it, and
the sweep goes on to the next. In each store it walks the `barrier`
strategy's credentials and, for each one whose enabled policy is due:

1. claims the rotation with a `CompareAndSwap` on the metadata record (one Raft
   transaction on the system shard, so exactly one node wins) that records a claim
   expiring one lease after the moment it is taken, read on the service clock
   for this credential rather than at the start of the pass (see
   [Invariants](#invariants)), and stops here if another caller holds a claim
   that has not expired at that moment;
2. draws the new value from the `RotationValueSource`, unless an earlier
   attempt left a staged value (below). Steps 2 to 4 are one attempt, under a
   deadline of 30 seconds or the claim's own expiry, whichever is sooner. An
   attempt past it fails like any other, and the pass goes on to the next
   credential, so one slow source does not starve the rest; the scheduler
   sets no deadline on the pass, and only its own context ending stops one;
3. stages the new value: one guarded batch, refused unless the metadata record
   is exactly as read, writes the value under the next version's staged key
   (`credential-staged/<e>/<N+1>`) and the metadata record naming that version
   in `staged`;
4. commits the rotation in one guarded batch, refused unless the metadata
   record and the current value are both exactly as read: it writes the
   previous value under its version key, the new value as the current value,
   and the metadata record, which advances the version, clears `staged`, ends
   the claim, records the claim as a pending cleanup and moves `NextRotation`
   one interval past now, unless the policy or its schedule changed meanwhile.
   No record shows the version advanced and the interval still open, and no
   reader sees the new value without its metadata. A refused batch is re-read
   and re-applied, up to eight times, only while the fresh record still
   carries this claim and still names this staged version; otherwise the
   rotation ends with `ErrRotationClaimLost` having written nothing. If
   steps 2 to 4 failed it ends the claim instead and leaves `NextRotation`
   where it was, so the credential stays due. Ending a failed claim runs even
   after the scheduler's context is cancelled, under its own 30-second bound
   that starts once the rotation has returned; if that write fails too, the
   claim lapses;
5. deletes every staged value of a version up to the current one, runs `GCExpiredVersions`, then prunes history to `RetainVersions - 1`
   non-current versions, then clears the pending cleanup.

The staged value is the persisted evidence of a value the source already
produced. It is kept under its own `credential-staged/<e>/<n>` key, apart from the
`credential-versions/<e>/<n>` version records (`<e>` is the name in unpadded
URL-safe base64), so the deletes that spend it (step 5 and the rotation's own
post-commit delete) can never remove a version that a concurrent rotation
retained under the same number. A claimant whose metadata record names a
staged version that is still the next version, left by an attempt that failed
or crashed after step 3 and before step 4 landed, lands it from step 4 without
calling the source again, so the source runs once per interval even across
that failure. Only the version the record names is resumed: the staged keys
are never probed, so a staged value the record does not name is never landed.

Every writer of the current value commits through the same kind of batch. A
manual rotation writes the retained version, the current value and the
metadata (with `staged` cleared) in one batch guarded on the metadata record
and the current value as read, and numbers its versions again from the fresh
record when the batch is refused. `StoreCredential` and `SubmitCredential`
write the current value in a batch guarded on the metadata record; when the
record names a staged version the same batch clears `staged` and deletes the
staged value. The stored value does not advance the version, the rotation
stays due, and the next check calls the source again and retains the stored
value as the previous version. A delete removes every staged value of the
credential.

A check that finds a pending cleanup on a credential that is not due, with no
unexpired claim held by another caller, runs step 5 alone and does not call the
value source. A failed cleanup is therefore retried on every check until it
lands, not left until the next rotation. Under a policy disabled meanwhile it
reaps expired versions and does no trim.

The scheduler logs a failure and keeps running. Under `manual` it does
nothing: a manual credential has no stored value to replace and the policy is
refused.

**The value source is a seam, and `qrdbd` ships none.** Minting a credential
value means knowing what the credential is for: a database password has to be
changed at the database, an API key issued by its provider. That knowledge
belongs to the application that embeds qrdb, so it supplies the source through
`credentials.WithRotationValueSource`. Without one, every due rotation fails
with `ErrNoRotationValueSource` and the credential stays due, so a source wired
in later picks it up on its next check.

## Persistence

`barrier` credentials are system records under `__sys:credentials/` (the
values) and its sibling prefixes `__sys:credential-meta/`,
`__sys:credential-versions/` and `__sys:credential-staged/` on the system
shard's replicated KV, so every node reads the same value, policy and
version history; each node's system barrier encrypts them at rest. The
embedded transport's KV, MVCC, batch, transaction and watch services confine
each of those prefixes whole (`pkg/service/hard_confined_keys.go`,
`hardConfinedWholePrefixes`): an embedded caller with KV permission cannot
read or replace a record under any of them, and only the credential verbs
reach them.
`manual` credentials are **not persisted at all** — they live only in memory
for the life of the process, which is why a restart re-blocks retrieval until
an operator submits again.

Version history carries the version number, the creation time and the
expiry implied by the rotation's grace period. Under `barrier`, a rotation
policy and its `NextRotation` live in the same metadata record, so they persist
with the credential and are removed with it by `delete`.

## Test strategy

**Integration target:** `make integration-test-credentials` (no Docker; selection in `test/integration/features.mk`). It runs the credential CLI suite per protocol on one node, the tenant-store CLI suite per protocol, and the embedded tenant-store suite; and on a three-node cluster a credential, rotation policy, rotation and delete made through one node are read through the others, per protocol. Per-transport accounting is the `credentials` row of `test/integration/COVERAGE.md`.

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
- **Rotation policy**: validation and the `manual` refusal, scheduling and
  clearing, `RotateDue` calling the value source once when a second caller
  arrives while the first holds the claim, a crashed holder's claim lapsing,
  a failing or absent value source, a failed current-value write and a
  cancelled scheduler context each leaving the credential due (and retried by
  a restarted service), a policy changed during a rotation, the prune, the
  reaper or a rotation keeping it, and pruning to `RetainVersions`. Each SDK transport sets and reads a policy and
  maps a refused one to its invalid-argument error, and the `qrdb credential
  rotation-policy` verbs have CLI unit tests.
- **One store**: an unbound service fails every verb with
  `ErrStoreUnavailable`, `BindStore` refuses nil and replaces a bound store,
  and two services bound to one store see each other's credential and policy
  while two concurrent `RotateDue` passes rotate once and call the source once.
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
