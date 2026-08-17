<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Custom Authenticators & Authorizers (Embedder Hooks)

go-qrdb ships exactly one first-contact authenticator (mTLS) and one
authorization stack (RBAC, optionally decorated with ABAC). Everything else
— OAuth 2.1 bearer validation, external policy engines, adaptive
authentication — is the embedding application's to implement and register
through the hooks below. **qrdb defines the interfaces and the transport
plumbing only; it contains no OAuth, JWT, or token-validation code and no
such dependencies.**

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

Authenticators run as a first-success-wins chain; the built-in mTLS
authenticator always runs first, so mTLS peers keep their behavior and
requests without a client certificate fall through to embedder-supplied
authenticators. The authorizer renders every permission decision on the
resulting identity.

## Registration hooks

| Layer | Hook | Use |
|---|---|---|
| `pkg/auth/wiring` | `AuthMiddlewareConfig.ExtraAuthenticators` | Append authenticators to the canonical middleware stack |
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
    node.WithAuthenticators(quicbaasOAuthAuthenticator), // embedder-implemented
    node.WithAuthorizerWrapper(quicbaasScopeAuthorizer), // embedder-implemented, optional
)
```

Without `WithBearerChallenge`, unauthenticated MCP requests proceed
identity-less into per-tool DEFAULT-DENY errors — the pre-OAuth behavior
mTLS-only deployments rely on.

## Composition with built-in semantics

Embedder hooks compose with, and never bypass, the built-in rules:

- **Default-deny:** an identity no authorizer admits is denied.
- **IdentityGated routes** (officer claims, tenant barrier bootstrap)
  authenticate but skip RBAC by design; handler-level rules still apply.
- **Sealed-window semantics:** identities authenticated while their
  tenant's IAM is unreachable are marked and constrained exactly as with
  mTLS (see `docs/permissions/`).
- A `WrapAuthorizer` decorator receives the inner RBAC[+ABAC] stack and
  chooses when to defer to it; returning nil fails node construction
  closed.
