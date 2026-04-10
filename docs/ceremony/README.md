<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Ceremony Package

The `pkg/ceremony/` package implements the initialization ceremony for go-qrdb. It manages a three-state machine that secures the system bootstrap process with M-of-N Shamir secret sharing, officer enrollment via certificate claims, and challenge-response authentication.

## Three-State Machine

```
awaiting_init  ──Initialize()──>  enrolling  ──allClaimsComplete()──>  operational
```

- **awaiting_init**: System is freshly deployed, no ceremony has been performed.
- **enrolling**: Ceremony in progress. Officers claim certificates and Shamir shares.
- **operational**: All officers have claimed their artifacts. System is ready.

## Single-Admin vs M-of-N Mode

When `threshold < 2`, the ceremony runs in **single-admin mode**: no officers are enrolled, no Shamir shares are generated, and the state transitions directly from `awaiting_init` to `operational`.

When `threshold >= 2`, the ceremony runs in **M-of-N mode**: the user PIN is split into Shamir shares (one per officer), each officer's CSR is signed to produce a certificate, and shares are held in secure memory until claimed.

## Share Lifecycle (memguard)

Shares are **never persisted to disk**. During M-of-N enrollment:

1. `Initialize()` calls `shamir.Split()` on the user PIN
2. Each share is stored in a `memguard.LockedBuffer` (mlock'd, guard-paged memory)
3. Officer connects via mTLS, calls `BeginClaimCert()` to get a challenge nonce
4. Officer signs nonce with CSR private key, calls `CompleteClaimCert()` to receive certificate
5. Officer calls `ClaimShare()` to retrieve their share bytes
6. The `LockedBuffer` is `Destroy()`'d (secure wipe) immediately upon claim
7. When all claims complete, the state transitions to `operational`

If the ceremony is abandoned (e.g., server restart), all shares are lost and the ceremony must be re-run. This is intentional: shares are secrets that belong to custodians, not the system.

## CeremonyCA Interface

The ceremony requires a narrow CA interface that decouples it from any specific CA implementation:

```go
type CeremonyCA interface {
    SignCSR(csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error)
    CACertificate() (*x509.Certificate, error)
    CABundle() ([]byte, error)
    IssueCertificate(req *IssueCertRequest) (*IssuedCert, error)
}
```

Both qrdb's `cert.CertProvider` and xkms's `ca.XKMSCA` can satisfy this via thin adapters.

## ThresholdInitializer

The `ThresholdInitializer` interface supports hardware-backed threshold ceremonies (e.g., HSM native threshold). Implementations are registered via `ThresholdRegistry` and stay in go-xkms. A `NoopThresholdInitializer` is provided for environments without hardware support.

## REST API

| Method | Path | Description |
|--------|------|-------------|
| POST | `/v1/ceremony/initialize` | Start the ceremony |
| GET | `/v1/ceremony/status` | Get ceremony state |
| POST | `/v1/ceremony/claim-cert/begin` | Begin certificate claim challenge |
| POST | `/v1/ceremony/claim-cert/complete` | Complete claim with signed nonce |
| POST | `/v1/ceremony/claim-share` | Retrieve Shamir share |
| POST | `/v1/ceremony/sign-csr` | Sign a CSR during/after ceremony |

## SDK Usage

```go
client, _ := sdk.NewClient(transport)
client.Connect(ctx)

// Initialize (single-admin)
resp, _ := client.Ceremony().CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{
    SOPin:     "so-pin-value",
    UserPin:   "user-pin-value",
    Threshold: 1,
})

// Check status
status, _ := client.Ceremony().CeremonyStatus(ctx)
// status.State == "operational"
```

# Credentials Package

The `pkg/credentials/` package manages named credentials using a configurable strategy.

## Strategies

| Strategy | Description |
|----------|-------------|
| `manual` | Credentials stored in-memory. Retrieval blocks until an operator submits the credential. |
| `barrier` | Credentials encrypted and stored via the `StorageBarrier`. |

Platform strategies (tpm2, pkcs11, cloud KMS) are extensions provided by go-xkms.

## REST API

| Method | Path | Description |
|--------|------|-------------|
| POST | `/v1/credentials/submit` | Submit a named credential |
| GET | `/v1/credentials/strategy` | Get active strategy |
| DELETE | `/v1/credentials/{name}` | Delete a credential |
