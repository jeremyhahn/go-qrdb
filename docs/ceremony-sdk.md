<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Ceremony SDK Exposure

This document describes the ceremony service as exposed through the go-qrdb SDK,
covering the protocol flow, SDK client interface, transport coverage, and REST
endpoint mapping.

## Protocol Flow

The initialization ceremony follows a linear state machine:

```
awaiting_init --> enrolling --> operational
```

### Single-Admin Mode (threshold < 2)

```
1. CeremonyInitialize   -- issues CA, TLS, and SO certs; transitions directly to operational
```

### M-of-N Mode (threshold >= 2)

```
1. CeremonyInitialize        -- issues certs, splits user PIN into Shamir shares, enters enrolling
2. CeremonyClaimCertBegin     -- officer requests a challenge nonce
3. CeremonyClaimCertComplete  -- officer submits signed nonce, receives certificate
4. CeremonyClaimShare         -- officer retrieves their Shamir share (destroyed on read)
5. CeremonySignCSR            -- officer or service submits a CSR for signing
6. CeremonyStatus             -- query current ceremony state at any point
```

Steps 2-5 repeat for each officer. Once all officers have claimed their shares,
the state transitions automatically to `operational`.

## SDK CeremonyClient Interface

Defined in `sdk/go/transport/client.go`:

```go
type CeremonyClient interface {
    CeremonyInitialize(ctx context.Context, req *CeremonyInitializeRequest) (*CeremonyInitializeResponse, error)
    CeremonyStatus(ctx context.Context) (*CeremonyStatusResponse, error)
    CeremonyClaimCertBegin(ctx context.Context, req *ClaimCertBeginRequest) (*ClaimCertBeginResponse, error)
    CeremonyClaimCertComplete(ctx context.Context, req *ClaimCertCompleteRequest) (*ClaimCertCompleteResponse, error)
    CeremonyClaimShare(ctx context.Context, req *ClaimShareRequest) (*ClaimShareResponse, error)
    CeremonySignCSR(ctx context.Context, req *SignCSRInitRequest) (*SignCSRInitResponse, error)
}
```

### Service Layer Mapping

| SDK Method                  | Service Method                      |
|-----------------------------|-------------------------------------|
| CeremonyInitialize          | CeremonyServicer.CeremonyInitialize |
| CeremonyStatus              | CeremonyServicer.CeremonyStatus     |
| CeremonyClaimCertBegin      | CeremonyServicer.CeremonyBeginClaimCert |
| CeremonyClaimCertComplete   | CeremonyServicer.CeremonyCompleteClaimCert |
| CeremonyClaimShare          | CeremonyServicer.CeremonyClaimShare |
| CeremonySignCSR             | CeremonyServicer.CeremonySignCSR    |

All six service methods are exposed. There are no gaps.

## Transport Coverage

All five transports implement `CeremonyClient` with compile-time interface assertions:

| Transport | File | Mechanism |
|-----------|------|-----------|
| Embedded  | `sdk/go/transport/embedded/transport_ceremony.go` | Direct service call via injected `CeremonyServicer` |
| REST      | `sdk/go/transport/rest/transport_ceremony.go`      | HTTP JSON via `postJSONDecode` / `NewRequestWithContext` |
| gRPC      | `sdk/go/transport/grpc/transport_ceremony.go`      | Protobuf via generated `QRDBServiceClient` stub |
| QUIC      | `sdk/go/transport/quic/transport_ceremony.go`      | Protobuf over QUIC via `t.call()` RPC dispatch |
| Unix      | `sdk/go/transport/unix/transport_ceremony.go`      | Protobuf via gRPC over Unix domain socket |

Every transport implements all six methods. There are no missing implementations.

## REST Endpoints

Registered in `pkg/api/rest/server.go` when a `CeremonyServicer` is injected:

| Method | Path                              | Handler                          |
|--------|-----------------------------------|----------------------------------|
| POST   | `/v1/ceremony/initialize`         | `handleCeremonyInitialize`       |
| GET    | `/v1/ceremony/status`             | `handleCeremonyStatus`           |
| POST   | `/v1/ceremony/claim-cert/begin`   | `handleCeremonyClaimCertBegin`   |
| POST   | `/v1/ceremony/claim-cert/complete`| `handleCeremonyClaimCertComplete`|
| POST   | `/v1/ceremony/claim-share`        | `handleCeremonyClaimShare`       |
| POST   | `/v1/ceremony/sign-csr`           | `handleCeremonySignCSR`          |

Handlers are defined in `pkg/api/rest/handlers_ceremony.go`.

## SDK Usage Example

```go
import (
    "context"
    "fmt"

    "github.com/jeremyhahn/go-qrdb/sdk/go/transport"
    "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
)

func main() {
    ctx := context.Background()

    client, err := rest.NewTransport("https://localhost:8443")
    if err != nil {
        panic(err)
    }
    defer client.Close()

    if err := client.Connect(ctx); err != nil {
        panic(err)
    }

    // 1. Initialize ceremony (M-of-N with 2 officers, threshold 2)
    initResp, err := client.CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{
        SOPin:     "so-secret",
        UserPin:   "user-secret",
        Threshold: 2,
        Officers: []transport.OfficerConfig{
            {Username: "alice", CSRPEM: aliceCSR},
            {Username: "bob", CSRPEM: bobCSR},
        },
    })
    if err != nil {
        panic(err)
    }
    fmt.Println("SPKI Pin:", initResp.SPKIPin)
    fmt.Println("State:", initResp.State)

    // 2. Each officer claims their certificate
    challenge, err := client.CeremonyClaimCertBegin(ctx, &transport.ClaimCertBeginRequest{
        Username: "alice",
    })
    if err != nil {
        panic(err)
    }

    // Officer signs the nonce with their CSR private key (application-specific)
    signature := signNonce(aliceKey, challenge.NonceHex)

    cert, err := client.CeremonyClaimCertComplete(ctx, &transport.ClaimCertCompleteRequest{
        Username:  "alice",
        NonceHex:  challenge.NonceHex,
        Signature: signature, // base64-encoded
    })
    if err != nil {
        panic(err)
    }
    fmt.Println("Officer cert:", cert.CertPEM[:40], "...")

    // 3. Officer claims their Shamir share
    share, err := client.CeremonyClaimShare(ctx, &transport.ClaimShareRequest{
        Username: "alice",
    })
    if err != nil {
        panic(err)
    }
    fmt.Println("Share claimed (store securely):", share.ShareJSON[:20], "...")

    // 4. Check ceremony status
    status, err := client.CeremonyStatus(ctx)
    if err != nil {
        panic(err)
    }
    fmt.Println("Ceremony state:", status.State)

    // 5. Sign a CSR (available during or after ceremony)
    signed, err := client.CeremonySignCSR(ctx, &transport.SignCSRInitRequest{
        Username: "alice",
        SOPin:    "so-secret",
        CSRPEM:   serviceCSR,
        Role:     "server",
    })
    if err != nil {
        panic(err)
    }
    fmt.Println("Signed cert:", signed.CertPEM[:40], "...")
}
```

## Audit Summary

**Date:** 2026-03-28

- **CeremonyClient interface**: 6 methods -- complete, matches all `CeremonyServicer` operations
- **Embedded transport**: 6/6 methods implemented
- **REST transport**: 6/6 methods implemented
- **gRPC transport**: 6/6 methods implemented
- **QUIC transport**: 6/6 methods implemented
- **Unix transport**: 6/6 methods implemented
- **REST handlers**: 6/6 endpoints registered with route map dispatch
- **REST tests**: Coverage tests exist in `pkg/api/rest/rest_coverage_test.go`
- **Gaps found**: None
