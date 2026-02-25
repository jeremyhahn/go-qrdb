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

## go-xkms Integration

go-xkms passes its own `Authenticator` (JWT with tenant claims, adaptive auth) to go-qrdb's server constructor. go-qrdb accepts any implementation of the interface.
