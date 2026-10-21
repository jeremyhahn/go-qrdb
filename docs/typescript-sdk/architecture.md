# TypeScript SDK — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

## Problem

Browser and desktop clients need the same public QRDB operations as the Go SDK
without reaching into Go packages or duplicating Wails binding details in app
views.

## Shape Chosen

The TypeScript SDK lives at `sdk/typescript/` as the `@qrdb/sdk` ESM package.
Its public barrel is `sdk/typescript/src/index.ts`, with service contracts in
`interfaces.ts`, DTOs in `types.ts`, REST/Wails transports in `rest.ts` and
`wails.ts`, and transport selection in `factory.ts`.

The desktop GUI consumes the package through `cmd/qrdb/frontend/package.json` as
`file:../../../sdk/typescript`. Browser mode uses `RestClient` over `fetch()`.
Desktop mode uses `WailsClient`, which wraps Wails-generated IPC bindings and
keeps the GUI from importing Go packages directly.

## Component Boundaries

`sdk/typescript` owns the TypeScript public API, wire-shape transforms, typed
errors, and contract tests. `cmd/qrdb/frontend` owns the Svelte app and imports
the SDK as an application dependency. The Go command and server packages own the
actual Wails bindings and REST endpoints.

## Data Flow

Browser client calls flow through `RestClient` to the REST API selected by
`baseUrl`. Desktop calls flow through `WailsClient` to Wails IPC bindings under
`cmd/qrdb/bindings_*.go`; those bindings call `sdk/go`, and embedded mode then
reaches the in-process server.

## Trust Boundaries

The SDK never stores private keys, PINs, Shamir shares, or root tokens as a
durable concern. It passes caller-supplied values to the selected transport for
one request, surfaces typed failures, and leaves certificate and key custody to
the GUI, browser runtime, operating system, or caller.

## Rejected Alternatives

Generating TypeScript directly from protobuf was rejected because the REST and
Wails surfaces are not only the gRPC schema: the GUI needs desktop-only binding
operations and REST wire-shape normalization.

Letting Svelte views call Wails bindings directly was rejected because it would
create a second public client surface and make browser mode drift from desktop
mode.

## Not In Scope

Publishing to npm is not part of this feature. The shipped consumer is the in-repository frontend using the local file dependency.
