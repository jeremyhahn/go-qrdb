# Custom Authenticators & Authorizers (Embedder Hooks)

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-23
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

go-qrdb ships exactly one first-contact authenticator (mTLS) and one
authorization stack (RBAC, optionally decorated with ABAC). Everything else
— OAuth 2.1 bearer validation, external policy engines, adaptive
authentication — is the embedding application's to implement and register
through the hooks below. **qrdb defines the interfaces and the transport
plumbing only; it contains no OAuth, JWT, or token-validation code and
imports no such library.**

## The two interfaces (`pkg/auth`)

```go
type Authenticator interface {
    Authenticate(ctx context.Context) (*Identity, error)
    Name() string
}

type Authorizer interface {
    Authorize(ctx context.Context, identity *Identity, resource, action string) error
}
```

Authenticators run as a first-success-wins chain. Each embedder-supplied
authenticator takes an explicit position relative to the built-in mTLS
authenticator: before it (it decides every request it accepts, including one
that also presents a client certificate), after it (only requests mTLS
declines reach it), or replacing it (a client certificate alone authenticates
nobody; the transport's TLS stays in place). See
[identity-rbac/api.md](../identity-rbac/api.md#consumer-authenticators-nodewithauthenticators). The authorizer renders every permission decision on the
resulting identity.

## Registration hooks

| Layer | Hook | Use |
|---|---|---|
| `pkg/auth/wiring` | `AuthMiddlewareConfig.ExtraAuthenticators` | Place authenticators in the canonical chain (`[]auth.AuthenticatorPlacement`: before, after, or replacing mTLS) |
| `pkg/auth/wiring` | `AuthMiddlewareConfig.WrapAuthorizer` | Decorate the built RBAC[+ABAC] authorizer (compose over / defer to it) |
| `sdk/go/node` | `node.WithAuthenticators(...)` | Same, for applications embedding qrdb via the SDK node |
| `sdk/go/node` | `node.WithAuthorizerWrapper(...)` | Same; the wrapped instance is shared by the embedded transport and every network transport |
| `pkg/server` | `server.WithMCPServerOptions(...)` | Pass MCP transport options (the OAuth discovery surface below) through the multi-protocol server |

Because every transport (REST, gRPC, gRPC-over-UDS, QUIC, MCP, MySQL,
PostgreSQL) shares the one `AuthMiddleware`, a hook registered once applies
everywhere at once.

## Credential carriers per transport

An embedder authenticator resolves its credential from the request context:

- **REST and MCP (HTTP):** the raw `Authorization` header is carried via
  `auth.HTTPAuthorizationFromContext(ctx)`; mTLS state via
  `auth.TLSStateFromContext(ctx)`.
- **gRPC / gRPC-over-UDS:** read request metadata directly
  (`metadata.FromIncomingContext(ctx)`); the transport does not need qrdb
  plumbing for it.
- **QUIC (native frames):** mTLS only; the framing carries no
  bearer-credential field.

### DPoP (RFC 9449) request metadata

A sender-constrained-token authenticator also needs the `DPoP` proof and the
method and URI it is bound to (`htm`, `htu`). The context carries them as
`auth.DPoPRequestFromContext(ctx)` (SDK: `sdk.DPoPRequestFromContext`), a
`DPoPRequest{Proof, Method, URI}`; the bool is false when the request had no
`DPoP` header. qrdb only carries the data: it does not verify proofs, track
`jti` replay or issue nonces, and the authenticator order is unchanged.

- **REST and MCP (HTTP/2 and HTTP/3):** the `DPoP` header, `r.Method`, and
  `scheme://host/path` (https when the connection is TLS; no query or
  fragment, per RFC 9449 section 4.2).
- **gRPC / gRPC-over-UDS:** the `dpop` metadata, method `POST` (every gRPC
  call is one), and `scheme://:authority/<service>/<method>`.
- **Not applicable:** native QUIC frames, the embedded transport and MCP
  stdio are not HTTP requests, so DPoP is REST/gRPC-only and the context
  never carries it there.

## MCP authorization (OAuth 2.1, RFC 9728)

Per the MCP authorization specification, an MCP server acting as an OAuth
protected resource must (1) answer credential-less requests with
`401 Unauthorized` plus a `WWW-Authenticate` header whose
`resource_metadata` parameter points at (2) a public RFC 9728 Protected
Resource Metadata document, and (3) validate bearer tokens (signature,
audience, scope).

The split of responsibilities:

- **qrdb (transport plumbing only):**
  - `mcp.WithBearerChallenge(value)` — the exact `WWW-Authenticate` value
    emitted with 401 when no authenticator accepts the request.
  - `mcp.WithProtectedResourceMetadata(doc)` — serves the embedder's JSON
    document, unauthenticated, at
    `/.well-known/oauth-protected-resource`
    (`mcp.ProtectedResourceMetadataPath`).
  - Carries the `Authorization` header to the authenticator chain.
- **The embedder (e.g. go-quicbaas):** composes the challenge value,
  authors the metadata document, and implements token validation as an
  `auth.Authenticator` registered via `ExtraAuthenticators` /
  `node.WithAuthenticators`. Scope-to-permission mapping, if any, is
  implemented in a `WrapAuthorizer` decorator.

```go
node, err := node.NewStandaloneNode(ctx, cfg,
    []server.ServerOption{
        server.WithMCPServerOptions(
            mcp.WithBearerChallenge(`Bearer realm="mcp", resource_metadata="https://db.example.com/.well-known/oauth-protected-resource"`),
            mcp.WithProtectedResourceMetadata(prmJSON),
        ),
    },
    node.WithAuthenticators(sdk.ChainBeforeMTLS, quicbaasOAuthAuthenticator), // embedder-implemented
    node.WithAuthorizerWrapper(quicbaasScopeAuthorizer), // embedder-implemented, optional
)
```

Without `WithBearerChallenge`, an unauthenticated MCP request is still
refused with `401` at the HTTP layer, before the MCP layer reads it, but
without a `WWW-Authenticate` header. `initialize`, `ping` and `tools/list`
are never answered to an anonymous caller, unless a module declares public
tools (`MCPPublicToolLister`, see
[api-server/engineering.md](../api-server/engineering.md#mcp-public-tools)):
then `initialize` and `tools/list` are answered, the listing shows the
public set only, and those tools alone may be called. The challenge rides only a `401`:
a request the node could not finish checking (user store unavailable, rate
limited) is answered `503` or `429` with `Retry-After`, as REST answers it.

## Composition with built-in semantics

Embedder hooks compose with, and never bypass, the built-in rules:

- **Default-deny:** an identity no authorizer admits is denied.
- **IdentityGated routes** (officer claims, tenant barrier bootstrap)
  authenticate but skip RBAC by design; handler-level rules still apply.
- **Sealed-window semantics:** identities authenticated while their
  tenant's IAM is unreachable are marked and constrained exactly as with
  mTLS (see [identity-rbac/architecture.md](../identity-rbac/architecture.md)).
- A `WrapAuthorizer` decorator receives the inner RBAC[+ABAC] stack and
  chooses when to defer to it; returning nil fails node construction
  closed.
