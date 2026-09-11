# Credentials and Secret Resolution — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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
| `manual` | memguard secure enclave (`secure.Secret`) | **blocks until an operator submits the value**; plaintext materialized transiently (`Open → copy → Destroy`) |
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
  (`secure.CopyOut`) rather than sizing the destination and then reading it,
  which are two separate acquisitions of the process-wide purge lock with a gap
  between them. An enclave whose pages a purge has already taken yields
  nothing, and the read **fails closed** rather than returning a correctly
  sized run of zeros that a caller cannot tell from the real credential. The
  same applies to a resolved `SecretValue`: `Bytes()` answers `nil` and
  `String()` answers `""` when the material can no longer be read.
- **Rotation takes a grace period.** The previous version stays retrievable for
  that long; `0` wipes it immediately.
- **`retrieve-version 0` means the current version.**
- **`delete` removes the credential and its non-current versions.**
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

- **Unit**: store/retrieve/submit/delete for both strategies, rotation with and
  without a grace period, versioned retrieval including `0` meaning current, GC
  reaping only expired versions, and `CredentialStrategy()`.
- **The enclave lifecycle is tested, not assumed**: destroy on delete and on
  rotation, and transient materialization on read.
- **Fail-closed tests** for the protected-config-field rule and for an
  unresolvable reference at boot.
- **No `time.Sleep`** — grace-period expiry is driven through the clock seam.
- **Error tests** per function.

## Observability

Credential operations are audited through [../audit/](../audit/). **Values are
never logged or included in audit records** — only names, versions and
outcomes.
