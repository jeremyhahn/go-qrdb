# Credentials and Secret Resolution — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

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
    CredentialRetrieve(ctx context.Context, name string) ([]byte, error)
    CredentialSubmit(ctx context.Context, name string, value []byte) error
    CredentialDelete(ctx context.Context, name string) error
    CredentialStrategy() string

    // Versioned rotation (the new 5 methods added in Phase 8e)
    CredentialList(ctx context.Context) ([]string, error)
    CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error)
    CredentialRetrieveVersion(ctx context.Context, name string, version uint64) ([]byte, error)
    CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error)
    CredentialGCExpired(ctx context.Context, name string) (int, error)
}
```

`CredentialService` (`pkg/service/credential_service.go:75-145`) is the canonical implementation; it is a thin pass-through to `credentials.CredentialService`.

`CredentialService` (`pkg/service/credential_service.go`) is the canonical
implementation and a thin pass-through to `credentials.CredentialService`.

`pkg/secret` provides the secret-reference resolver SPI: `SecretValue`,
`ConfigProcessor`, and the `credential://` / `literal://` schemes.

## Strategies

| Strategy | Storage | Read semantics |
|---|---|---|
| `manual` | go-enclave sealed enclave (`secure.Secret`) | **blocks until an operator submits the value**; plaintext materialized transiently (`Open → copy → Destroy`) |
| `barrier` | encrypt-then-store through `seal.StorageBarrier` | the production default; unreadable while sealed |

`CredentialStrategy()` reports which is active. A future HSM-only strategy will
return `ErrEnumerationNotSupported` from `ListCredentials` rather than change
the interface.

## Invariants

- **Credentials never live in the user KV store.** They are distinct from the
  KEK/DEK custody in the barrier and from the Raft data plane.
- **`pkg/credentials` is the only secret-bearing store in qrdb.** There is no
  `secrets` or `kms` package; the old `secrets-envelope` was removed in the
  single-KEK/DEK refactor and was not replaced.
- **`manual` values are never raw heap bytes.** They live in enclaves;
  plaintext exists only transiently during a read; the enclave is destroyed on
  delete and on rotation.
- **A read copies under a held borrow, and refuses an unreadable enclave.**
  The copy out of protected memory runs inside a single coordinated borrow
  (`secure.CopyOut`) rather than sizing the destination and then reading it as
  two separate calls, which would leave a gap between them in which a
  concurrent destroy of that same enclave (`enclave.WipeAll`, or any other
  caller with a handle to it) could land. An enclave whose pages are already
  gone by the time the read happens yields nothing, and the read **fails
  closed** rather than returning a correctly sized run of zeros that a caller
  cannot tell from the real credential. The same applies to a resolved
  `SecretValue`: `Bytes()` answers `nil` and `String()` answers `""` when the
  material can no longer be read, and `WithBytes` holds the SAME enclave's
  borrow for its callback's entire duration rather than releasing it before
  the callback runs — a concurrent destroy blocks against that borrow instead
  of racing it.
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
  under the same lock as the current credential, so this was already true
  there; the barrier strategy was the gap (go-qrdb creddel blocker).
- **GC reaps only expired historical versions** — it never touches the current
  one.
- **Secret references resolve to protected targets, not strings.** A protected
  config field must reference a secret, and resolution **fails closed** when it
  cannot.
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
| `ListCredentials` on a future HSM-only strategy | `ErrEnumerationNotSupported` | n/a — explicit, not an empty list |
| Missing `credentials:*` permission | permission denied | **yes** |

The "blocks rather than returns empty" property is the load-bearing one for the
ceremony case: a non-resident secret that read as empty would let a boot
continue with no credential at all.

## Permission model

`credentials:submit`, `credentials:strategy`, `credentials:list`,
`credentials:store`, `credentials:retrieve`, `credentials:rotate`,
`credentials:delete`, plus the version-specific grants. **Write, rotate and
delete custody secret material and are gated separately from reads.**

## Configuration

The active strategy is a server configuration item; `credential strategy`
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

**Integration target:** `make integration-test-credentials` (no Docker; selection in `test/integration/features.mk`). It runs the credential CLI suite per protocol. Per-transport accounting is the `credentials` row of `test/integration/COVERAGE.md`.

- **Unit**: store/retrieve/submit/delete for both strategies, rotation with and
  without a grace period, versioned retrieval including `0` meaning current, GC
  reaping only expired versions, and `CredentialStrategy()`.
- **The enclave lifecycle is tested, not assumed**: destroy on delete and on
  rotation, and transient materialization on read.
- **Delete is tested against the full version history, not just the current
  value**: rotate with a nonzero grace period, delete, then assert every
  version — current and in-grace history alike — is unretrievable, on both
  strategies, across a simulated process restart (a fresh `StorageBarrier`
  unsealing the same durable backend), through `CredentialServicer`, and
  through every CLI-exposed transport (REST, gRPC, QUIC, MCP, Unix).
- **Fail-closed tests** for the protected-config-field rule and for an
  unresolvable reference at boot.
- **No `time.Sleep`** — grace-period expiry is driven through the clock seam.
- **Error tests** per function.

## Observability

Credential operations are audited through [../audit/](../audit/). **Values are
never logged or included in audit records** — only names, versions and
outcomes.
