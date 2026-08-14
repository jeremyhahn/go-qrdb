<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0.
-->

# Secret-reference resolution (`pkg/secret`)

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
- **`ConfigProcessor`** — reflects over a config struct, finds string fields that
  hold a reference, resolves each through the registry, and writes the plaintext
  back in place. `WithFailClosed()` makes it return `ErrUnresolvedSecretURI` when
  any reference fails to resolve. For a production boot, that is the default you want.
- **`SecretValue`** — a resolved secret plus its `ResolvedStrategy` tag.

## Secrets live in a memguard enclave

A resolved `SecretValue` seals its plaintext in a memguard enclave (`secure.Secret`),
not in a garbage-collected byte slice. The plaintext sits in mlocked, guard-paged
memory and is decrypted only for the moment an accessor needs it.

Read a secret through `WithBytes` (or `Open`), which keeps the plaintext in
mlocked memory and wipes the working copy when the callback returns:

```go
err := sv.WithBytes(func(plaintext []byte) error {
    return useSecret(plaintext) // valid only for this call; do not retain
})
```

`Bytes()` and `String()` still exist for callers that must hand a raw `[]byte`
or `string` to an API that takes one, but they copy the secret into the
garbage-collected heap, where it cannot be wiped reliably, so reach for them
only when an interface leaves no choice. Call `Clear()` once you are done to destroy
the enclave.

One limit is worth stating plainly: when the `ConfigProcessor` resolves a secret
into a plain `string` config field, that field is an ordinary Go string and
lives in the heap for the lifetime of the process. The enclave protects the
secret through the resolution pipeline, up to the moment it lands in the config
struct. Config types that hold their secret in a `SecretValue` rather than a
bare `string` keep the enclave guarantee end to end.

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
`ErrNilSystemCredentialService`.

`literal://<value>` forces a value to be treated as a verbatim literal, which is
how an operator quotes a value that would otherwise look like a reference. The
prefix is stripped during resolution.

## Consuming it downstream

Downstream projects consume this package through the SDK re-exports in
`sdk/go/secret/` (`ResolverRegistry`, `NewConfigProcessor`,
`NewCredentialResolver`, `SecretValue`, `WithFailClosed`, …), never by importing
`go-qrdb/pkg/*` directly.

## See also

- [Credentials Service](README.md) — the L2 store the `credential://` scheme reads from.
- Package documentation: `go doc github.com/jeremyhahn/go-qrdb/pkg/secret`.
