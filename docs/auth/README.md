<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Pluggable Authentication

go-qrdb defines pluggable `Authenticator` and `Authorizer` interfaces in `pkg/auth/`. The default implementation extracts identity from mTLS client certificates. go-xkms can inject richer authenticators (JWT, adaptive) when using go-qrdb as a library.

## Interfaces

```go
type Identity struct {
    Subject   string
    TenantID  string
    Roles     []string
    Metadata  map[string]string
}

type Authenticator interface {
    Authenticate(ctx context.Context) (*Identity, error)
    Name() string
}

type Authorizer interface {
    Authorize(ctx context.Context, resource, action string) error
}
```

## Default mTLS Authenticator

Extracts identity from TLS peer certificates:

| Field | Source |
|-------|--------|
| `Subject` | Certificate Subject CN |
| `TenantID` | Certificate Subject OU[0] |
| `Roles` | Not populated (external RBAC) |

## Middleware Integration

The authenticator is wired into protocol middleware:

- **REST**: `middleware_auth.go` extracts TLS peer cert, calls `Authenticate()`
- **gRPC**: `interceptor_auth.go` extracts from gRPC TLS credentials
- **Unix**: No auth required (inherently local)
- **MCP**: OAuth 2.1 Bearer token validation via external mux

## RBAC Authorizer

The `pkg/auth/rbac/` package implements the `auth.Authorizer` interface with role-based access control.

```go
type RBACAuthorizer struct {
    roleStore   RoleStore
    permCache   sync.Map       // permission cache: userID -> cachedPerms
    cacheTTL    time.Duration  // default 30s
}
```

### Behavior

- Loads the user's assigned roles from the `RoleStore`
- Collects all permissions across the assigned roles (built-in roles use concrete `resource:action` grants; whole-segment wildcards like `resource:*` also match)
- Allows the request only when the requested `resource:action` is present, and denies otherwise (`PermissionDeniedError`). There is no superadmin role that skips the check — an unknown role simply grants nothing
- **SO deny-list (FIPS 140-3 AS10.03)**: an identity holding the `so` role is denied cryptographic operations regardless of any grant, and this runs before permissions are resolved. It covers the `keys:*` crypto verbs (`sign`, `encrypt`, `decrypt`, `verify`, `wrap`, `unwrap`, `derive`), hardware-token key use (`piv:use`), and any resource whose action names a cryptographic primitive (`attest`, `quote`, `mac`, `kdf`, ...) -- including permissions contributed by a higher layer. Barrier lifecycle (`seal:unseal`, `seal:seal`) and issuance (`ceremony:sign_csr`, `certs:generate`) are the SO's own duties and are not denied
- **Permission cache**: a `sync.Map` caches resolved permissions per user with a configurable TTL (default 30 seconds) to avoid repeated store lookups on every request

## Auth Middleware

The `pkg/auth/middleware/` package provides protocol-specific middleware that wires authentication and authorization into the request path.

### AuthMiddleware

`AuthMiddleware` accepts one or more `Authenticator` implementations and an `Authorizer`. It tries each authenticator in order until one succeeds.

| File | Description |
|------|-------------|
| `middleware/grpc.go` | gRPC unary + stream interceptors that extract TLS peer state and invoke the auth chain |
| `middleware/rest.go` | REST middleware wrapping the HTTP dispatch mux |
| `middleware/mapping.go` | Route-to-permission mapping: maps each HTTP route or gRPC method to a `resource:action` permission string |

### Auth Chain Flow

```
Request
  -> Extract TLS State (peer certificates)
  -> Authenticator Chain (mTLS -> next -> ...)
  -> User Lookup (UserStore.GetByUsername)
  -> Enabled Check (reject disabled users)
  -> RBAC Authorization (Authorizer.Authorize)
  -> Handler
```

If any step fails, the request is rejected with the appropriate error: **401
Unauthorized** when the caller could not be authenticated, **403 Forbidden**
when an authenticated caller's grants do not cover a route that exists, and
**405 Method Not Allowed** (with an `Allow` header) when the path is a
registered API route but the method carries no registered permission — a wrong
verb, not an authorization failure (go-qrdb#185).

The 405 is evaluated *after* authentication, so an anonymous caller gets 401
and never learns which verbs a path serves. Full mapping:
[../api-server/api.md](../api-server/api.md#common-status-mapping).

## Admin Bootstrap

Admin bootstrap is owned by [`pkg/firstboot`](../../pkg/firstboot/firstboot.go). It is the canonical place where the system tenant, built-in roles, and the platform admin user are created. The package is reusable across qrdb, xkms, quicbaas, and any future binary in the stack — dependencies are passed by interface (`TenantCreator`, `RoleCreator`, `UserCreator`, `CertProvider`).

`firstboot.Initialize` runs after `ceremony.Initialize`:

1. **TLS setup** (driven by ceremony): Generate or load CA certificate and server TLS credentials.
2. **System tenant**: Create the reserved `system` tenant via an internal-call context that bypasses the public reserved-id rejection.
3. **Built-in roles**: Register the six built-in roles (SO / Admin / Operator / User / Auditor / Custodian) returned by `role.BuiltInRoles(tenantID)` under the system tenant. Already-exists errors are tolerated so re-runs are idempotent.
4. **Admin user**: Create user `admin` in the system tenant with exactly one role grant: `Admin@system`. The system-tenant rule in `pkg/auth/rbac` promotes that single grant to cross-tenant authority.
5. **Admin client certificate**: Issue an mTLS client certificate via the supplied `CertProvider` so the CLI can authenticate immediately after first start.

Every step is idempotent and Raft-replicated; follower nodes observe the leader's writes and skip duplication on their own runs.

The function returns a `firstboot.Result` describing which artifacts were created in *this* run (the `*Created` flags reflect this run only, not the overall presence of the resource — useful for distinguishing a true first boot from a follower replay).

> See `pkg/firstboot/firstboot.go:130-204` for the canonical implementation.

## go-xkms Integration

go-xkms passes its own `Authenticator` (JWT with tenant claims, adaptive auth) to go-qrdb's server constructor. go-qrdb accepts any implementation of the interface.

## Embedder Hooks (OAuth 2.1 / custom authorizers)

The registration seams an embedding application uses to supply custom
authenticators and authorizers — including the MCP OAuth 2.1 discovery
surface (401 + `WWW-Authenticate`, RFC 9728 Protected Resource Metadata)
that go-quicbaas drives — are documented in
[custom-authorizers.md](custom-authorizers.md). qrdb defines the interfaces
and transport plumbing only; token validation is always the embedder's
implementation.
