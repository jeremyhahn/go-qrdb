# Desktop GUI — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

**The GUI adds no server API.** That is the design: both of its transports reach
the same surfaces every other client uses.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Everything the GUI does (browser path) | the ordinary REST API **+ CORS** | — | — | — | — | the same permissions as any REST client |
| Everything the GUI does (desktop path) | — | — | — | — | Wails IPC → Go bindings → `sdk/go` → embedded server | the same permissions |
| A GUI-only endpoint | **none exists** | — | — | — | — | n/a |

**There is deliberately no privileged GUI surface.** A GUI user can do exactly
what a CLI user holding the same certificate can do, and nothing more. If you
are looking for a GUI-specific route, there isn't one — that is the answer, not
a gap in this document.

The only server-side accommodation is **CORS** on the REST API, so a browser
can reach it.

## The two client paths

| Path | Chain |
|---|---|
| Desktop | Svelte → `@qrdb/sdk` `WailsClient` → Wails IPC → Go bindings (`cmd/qrdb/bindings_*.go`) → `sdk/go` → embedded `qrdbd` |
| Browser | Svelte → `@qrdb/sdk` `RestClient` → `fetch()` + JSON → `pkg/api/rest` (+ CORS) → `qrdbd` |

`getClient()` auto-detects the runtime and returns the right one. Frontend code
does not branch.

## `@qrdb/sdk`

The TypeScript package mirrors the Go SDK's shape: a client interface, per-area
service interfaces, a factory, two transport implementations, watch streaming,
and a typed error hierarchy — transport, service, vector, management and feature
errors, so a frontend can tell "the server refused" from "the connection
failed".

Full reference: [typescript-sdk/](../typescript-sdk/).

## Authentication

**mTLS at the TLS handshake; no login endpoint, no password field, no token.**
The identity shown in the GUI header is the client certificate's Subject CN.

## Capability gating

Views follow the server's reported capabilities
(`GET /capabilities` — see [../api-server/api.md](../api-server/api.md)). A
capability the server does not have produces no view, rather than a view that
errors on use.

## Versioning

Not applicable — this feature introduces no routes. The npm package is versioned
independently of the server; see
[typescript-sdk/](../typescript-sdk/).
