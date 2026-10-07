# TypeScript SDK — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

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
response DTOs are exported from `types.ts`. Error classes are exported from
`errors.ts`.

## Authentication

Browser mode uses whatever credentials the REST deployment requires: TLS client
certificates are handled by the browser/host, and bearer or cookie material is
deployment-specific. Desktop mode delegates authentication to the Wails binding
and embedded or remote Go client path.

## Versioning

The package version is `0.1.0` in `sdk/typescript/package.json`. The package is
an in-repository dependency today; npm publication is not yet implemented.
