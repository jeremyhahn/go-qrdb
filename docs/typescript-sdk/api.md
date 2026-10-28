# TypeScript SDK — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

## Capability Matrix

| Capability | Embedded | Unix socket | REST | gRPC | QUIC | MCP |
|---|---|---|---|---|---|---|
| Browser client | — | — | `RestClient` over `fetch()` | not implemented here — not a TypeScript transport | not implemented here — not a TypeScript transport | not implemented here — not a TypeScript transport |
| Desktop client | `WailsClient` through Go bindings and `sdk/go` | inherited from the Go binding/server path | inherited from the Go binding/server path | inherited from the Go binding/server path | inherited from the Go binding/server path | inherited from the Go binding/server path |

The TypeScript SDK does not implement native gRPC, QUIC, MCP, or Unix-socket
clients. Those transports are available to the desktop path only because Wails
calls Go bindings that use the Go SDK and server machinery.

## Public Entry Points

`getClient(config?: ClientConfig): Promise<Client>` returns the Wails client in
desktop mode and the REST client otherwise.

`isDesktop(): boolean` reports whether the Wails runtime is present.

`Client` is the service facade exported from `interfaces.ts`. Request and
response DTOs are exported from `types.ts`; their shapes are the server's, as
documented in each feature's `api.md`. Error classes are exported from
`errors.ts`; how a server status maps to each is the table in
[engineering.md](engineering.md#error-semantics).

The SDK enforces no `scp` permission itself: every call is authorized by the
server it reaches, as the identity the client authenticated as.

## Authentication

Browser mode uses whatever credentials the REST deployment requires: TLS client
certificates are handled by the browser/host, and bearer or cookie material is
deployment-specific. Desktop mode delegates authentication to the Wails binding
and embedded or remote Go client path.

### Secret-bearing calls

A call whose request body carries a secret is refused before any network I/O
unless the client's base URL is `https://`; it fails with `ConfigError` and
nothing is sent. The request is sent with `redirect: "error"`, so a 3xx
response fails the call and the secret is never re-sent to another location.
This is stricter than the Go SDK, whose `transport.GuardBarrierSecret` accepts
a redirect to the same host. The guarded calls are `seal.initialize`,
`initializeShamir`, `unseal`, `changePassphrase`, `seal` with an SO PIN and
`enableEncryption`; `ceremony.initialize` and `signCSR`; `officers.startRekey`
and `revokeOfficer`; `tenants.barrierInit` and `barrierUnseal`;
`database.create` and `updateRaft` when they carry a barrier secret, discovery
token or multicast shared secret; `certs.export`; `invitations.redeem`; and
`credentials.submit`, `store` and `rotate`. A call whose optional secret is
absent is not guarded. `sdk/typescript/test/secret-transport.test.ts` holds
each one to all three outcomes.

## Versioning

The package version is `0.1.0` in `sdk/typescript/package.json`. The package is
consumed from this repository and is not published to npm.
