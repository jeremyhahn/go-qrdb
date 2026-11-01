# Secret-reference resolution (`pkg/secret`)

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-22
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0.
-->

The credentials service (L2) **stores** named, versioned secrets. `pkg/secret`
sits one layer above it and **resolves references to those secrets** at boot, so
a binary never embeds a secret and never hand-rolls its own injection logic. It
is a resolver, not a store: it persists nothing.

A reference is a `scheme://name` string that appears in configuration, for
example `password: "credential://db-key"`. At startup the resolver turns that
reference into its plaintext value and writes the value back into the config in
place. A value with no recognized scheme is left untouched.

## The pieces

- **`SecretResolver`** — the plug-in interface. A resolver claims a URI scheme
  (`CanResolve` / `Scheme`) and turns a reference into a `SecretValue`
  (`Resolve`).
- **`ResolverRegistry`** — ordered dispatch. Register resolvers; `Resolve` walks
  them in registration order and uses the first whose `CanResolve` matches. A
  reference that no resolver claims falls through to a literal (the value is used
  verbatim).
- **`ConfigProcessor`** — reflects over a config struct, finds the fields that
  hold a reference, and resolves each through the registry. A `SecretValue`
  field is filled in place inside its enclave; a plain `string` field has the
  plaintext written back over the reference. `WithFailClosed()` makes it return
  `ErrUnresolvedSecretURI` when any reference fails to resolve. It is opt-in;
  a production boot sets it.
- **`SecretValue`** — a resolved secret plus its `ResolvedStrategy` tag, and the
  processor's protected resolution target.

## Secrets live in a sealed enclave

A resolved `SecretValue` seals its plaintext in a go-enclave sealed enclave
(`secure.Secret`), not in a garbage-collected byte slice. AT REST the value is
AES-256-GCM ciphertext plus a wrapped data key — ordinary (unlocked) heap
bytes by design, since there is no plaintext there to protect with a page
lock. Only the moment an accessor calls `Open` or `WithBytes` does the
plaintext exist at all, and only then is it mlocked, guard-paged memory; it is
wiped and unmapped the instant that call returns.

Read a secret through `WithBytes` (or `Open`), which keeps the plaintext in
mlocked memory and wipes the working copy when the callback returns:

```go
err := sv.WithBytes(ctx, func(plaintext []byte) error {
    return useSecret(plaintext) // valid only for this call; do not retain
})
```

There is no accessor that returns the plaintext as an ordinary `[]byte` or
`string`. A caller that must hand one to an API that takes it copies it out of
the `Open` buffer with `secure.CopyOut` and wipes the copy when the call
returns. Call `Clear()` once you are done to destroy the enclave.

Every *accidental* path to the plaintext is closed. `SecretValue` implements
`fmt.Formatter` and `slog.LogValuer`, which `fmt` and `log/slog` consult ahead
of `fmt.Stringer`, so `%v`, `%s`, `%+v`, `fmt.Sprint` and a structured log of a
config struct all render the redacted description. `Redacted()` is the same
description, available directly.

It shows the reference (`credential://db-key`) only when a resolver has
established that the reference points *at* a secret rather than carrying one,
and `[redacted]` otherwise. **Provenance decides that, not syntax.** Looking
like a URI proves nothing: the registry falls back to `StrategyLiteral` for any
value no resolver claims, so `https://user:hunter2@example.test` and
`unknown://hunter2` are references in shape only and are the secret in fact. A
value whose `Strategy` is unset — one that has not been resolved yet — is
redacted for the same reason: nothing has vouched for where its `Raw` came from.

Serialization is the one place `Raw` is still emitted verbatim, because that is
what serializing a configuration *means* — `MarshalYAML`/`MarshalJSON` reproduce
the document the value was loaded from. If an operator puts a secret inline in
that document, writing the document back out writes the secret; use a reference.

Only an explicit `WithBytes` or `Open` call materializes the plaintext, and
then only in protected memory for the length of that call.

## Protected config fields

A plain `string` config field is the leak. When the `ConfigProcessor` writes a
resolved secret into one, that field is an ordinary Go string: it lives in the
heap for the lifetime of the process and cannot be wiped. The enclave protects
the secret right up to the moment it lands in the config struct, and no further.

Declare the field as a `SecretValue` instead and the plaintext never becomes a
string at all — the processor seals it into that field's own enclave:

```go
type DatabaseConfig struct {
    Host     string              `yaml:"host"`
    Password *secret.SecretValue `yaml:"password"`
}
```

The config document does not change. `SecretValue` decodes from — and
re-encodes to — the plain reference string in both YAML and JSON, so
`password: "credential://db-key"` reads the same for either field type, and a
re-serialized config emits the reference rather than the resolved value. A
non-scalar node fails with `SecretValueDecodeError`, and so does JSON `null` —
a null where a reference belongs is a configuration mistake, not an empty
secret. Express an optional secret with a `*SecretValue` field, where an absent
or null key simply leaves the pointer nil.

After `Process`, read the secret through the enclave-respecting accessor:

```go
err := cfg.Database.Password.WithBytes(ctx, func(pw []byte) error {
    return db.Connect(cfg.Database.Host, pw)
})
```

`Raw` keeps the reference the value resolved from; the processor never
overwrites it.

### A protected target must reference a secret, and it fails closed

The protected target is deliberately **stricter than a plain `string` field**.
Every non-nil target the walk reaches ends in one of two outcomes — resolved, or
reported in `Unresolved` — and never in silence:

| the target holds | outcome |
| --- | --- |
| `credential://db-key`, or any registered scheme | resolved into its enclave |
| nothing (an empty `Raw`: a null or absent key) | `UnresolvableTargetError` |
| `literal://hunter2`, or a bare `hunter2` | `UnresolvableTargetError` |
| a scheme no registered resolver claims | `UnresolvableTargetError` |
| a value the walk cannot write to (boxed in an unsettable interface) | `UnwritableTargetError` |

An **inline** secret is refused rather than sealed, and that is the point:
`literal://hunter2` carries the secret in `Raw` itself, an ordinary Go string
that outlives the walk and can never be wiped. Sealing it into the enclave would
leave a second, unprotected copy behind and quietly break the guarantee the
field type advertises. Use `literal://` in a plain `string` field, where it
promises nothing, or better, put the value in the credential store and reference
it.

**A protected-target failure fails the call, with or without
`WithFailClosed()`.** `Process` returns `ErrUnresolvedSecretURI` whenever
`ProcessResult.ProtectedUnresolved` is non-empty — choosing the type *is* the
request to fail closed on that field, and a caller who reads only the error,
which is the normal Go contract, must not be told a config is ready when a
secret it declared protected is missing or unverified. On the deprecated `string`
path a failure is a diagnostic you opt into with `WithFailClosed()`.

**A plain `string` field whose value resolved but cannot be read back fails
closed too.** The resolver's result is sealed on the way in; if it cannot be
opened again to write into the field — the process Sealer was wiped, the kernel
refused to lock the transient buffer — the field keeps its reference, the path
lands in `Unresolved`, and a `ResolvedReadError` wrapping the cause is recorded
in `Errors`. The field is never overwritten with `""` and never counted in
`Resolved`, so an unreadable secret cannot pass for one that is legitimately
empty. Whether that fails the call follows the plain-string contract: with
`WithFailClosed()` it does.

**A failed re-resolution never leaves the previous secret usable.** `Process`
destroys a target's existing enclave before refilling it, so a second walk over
an already-resolved config — a refresh after a rotation, a re-read after a
reload — leaves the field empty and errors if the new value cannot be fetched.
A revoked credential cannot go on authenticating for as long as its backend
stays down.

An **empty** target is refused for the same reason a missing password should
stop a boot: declaring a field as a `SecretValue` says a secret belongs there.
A secret that is genuinely optional is spelled as a `*SecretValue`, where an
absent or null key leaves the pointer nil and the walk passes over it.

**A plain `string` target carries no requiredness signal at all**, and that is
the second silent case. An empty string is skipped without comment, because the
walk cannot tell a required secret that went missing from an ordinary string
that is legitimately blank — the type says nothing either way, and guessing is
worse than declining to guess. Requiredness for a plain `string` field is
therefore the **consumer's** obligation, enforced in its own `Validate()`; this
package neither detects nor reports it. A field that must hold a secret belongs
in a `SecretValue`, which is the secure target regardless and the only one
whose absence is caught here rather than at first use.

This is what closes the gap YAML would otherwise leave. `yaml.v3` zeroes a null
destination before the unmarshaler is consulted, so `password:` and
`password: null` never reach `UnmarshalYAML` and cannot be rejected there; they
decode to an empty target, which the walk refuses. JSON rejects its `null` token
at decode time instead. Either way a required secret cannot be deleted or nulled
into a successful boot.

Boxed targets the walk *can* write to (an `any` struct field, an `any` map
value) are resolved through an addressable copy and assigned back.

### The boundary: requiredness only for a leaf the walk reaches

Everything above guarantees requiredness for a protected leaf the walk
**reaches**. It does not, and is not meant to, guarantee that the walk reaches
every leaf a config's author intended to be there.

A `SecretValue` nested inside a `nil` pointer or absent map entry is never
examined: the walk descends a pointer only `if !v.IsNil()`, so deleting an
entire config section — `database:` removed from the YAML rather than left
with an empty `password:` — skips the required field inside it along with the
rest of the section, and `Process` returns success under `WithFailClosed()`.
The field-type contract above (`SecretValue` means required, `*SecretValue`
means optional) says nothing about the container the field lives in, because
`pkg/secret` walks the config **value** it is handed, not the config **type**
against a schema — deciding that a section must be present at all is a
decision about the shape of the config, not about one field's secret, and
`pkg/secret` is deliberately not a config-validation framework.

**That is the consumer's job, in its own `Validate()`, the same place it
already checks that required non-secret fields are present.** A `Database`
struct whose absence should fail startup is validated the same way a
`Database.Host` of `""` would be — by the caller who knows the section is
required — not by `pkg/secret`, which cannot distinguish "this section is
optional and was omitted" from "this section is required and was deleted"
without a schema neither its input type nor its call carries.

### Migrating off `string` fields

`WithNoPlaintextStrings()` is the ratchet. With it set, the processor refuses to
write plaintext into a `string` field: the reference stays put, the secret is
never fetched, and the field's path is reported in
`ProcessResult.PlaintextSkipped` (always a subset of `Unresolved`).

```go
proc := secret.NewConfigProcessor(reg, secret.WithNoPlaintextStrings())
result, _ := proc.Process(ctx, &cfg)
// result.PlaintextSkipped is the exact list of fields still holding a reference
// because resolving it would have produced a plaintext string.
```

Turn the option on, read the list, convert those fields to `SecretValue` one at
a time, and the list shrinks to empty. Add `WithFailClosed()` once it is, and
any boot that would have produced a plaintext string fails instead.

Both options are opt-in.

## Scheme ownership across the stack

`pkg/secret` defines the contract; each layer registers the resolvers for the
schemes it owns. A new scheme is added by implementing `SecretResolver` in the
owning layer and registering it at startup, with no change to this package.

| scheme         | owner                | resolves to                                   |
| -------------- | -------------------- | --------------------------------------------- |
| `credential://`| go-qrdb              | a value from the credential store             |
| `literal://`   | go-qrdb              | the verbatim value (prefix stripped)          |
| `kms://`       | go-quicbaas          | a value sealed by a local KMS strategy        |
| `remote-kms://`| go-quicbaas          | a value from a remote KMS                     |
| `autogen://`   | go-quicbaas          | a freshly generated value                     |
| `encrypted://` | go-trusted-platform  | a value decrypted via the platform barrier    |
| `idevid://`    | go-trusted-platform  | a value bound to the device IDevID            |

`credential://<name>` resolves through the credential store. Use
`NewTenantScopedCredentialRetrieve` to route a lookup to the per-tenant store
when the request carries a tenant ID, and to the system store otherwise; a
system-scope request with no system store configured fails closed with
`ErrNilSystemCredentialService`. A tenant with no dedicated barrier, one on
the shared system barrier, fails with `seal.ErrTenantNotFound` and never
falls back to the system store, which is the same refusal the
`CredentialServicer` makes ([architecture.md](architecture.md), "Tenant
stores").

The resolver's retriever (`CredentialRetriever` / `CredentialRetrieveFunc`)
returns a protected-memory `*secure.Buffer`, not a `[]byte`. `Resolve` hands the
Buffer to `NewSecretValueFromBuffer`, which seals it into the `SecretValue`
straight from protected pages and consumes it — on success and on failure
(`*SealFailedError`). A retriever that returns a nil Buffer with no error fails
closed. See
[Protected-memory boundary](architecture.md#protected-memory-boundary).

`literal://<value>` forces a value to be treated as a verbatim literal, which is
how an operator quotes a value that would otherwise look like a reference. The
prefix is stripped during resolution. It applies to a plain `string` field only;
a protected target refuses it (above).

## Consuming it downstream

Downstream projects consume this package through the SDK re-exports in
`sdk/go/secret/` (`ResolverRegistry`, `NewConfigProcessor`,
`NewCredentialResolver`, `SecretValue`, `WithFailClosed`,
`WithNoPlaintextStrings`, …), never by importing `go-qrdb/pkg/*` directly.

## See also

- [Credentials Service](architecture.md) — the L2 store the `credential://` scheme reads from.
- Package documentation: `go doc github.com/jeremyhahn/go-qrdb/pkg/secret`.
