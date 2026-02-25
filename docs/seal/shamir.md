<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Shamir Threshold Unsealing

The ExtendedBarrier supports M-of-N Shamir secret sharing for root key custody. Multiple key holders must collaborate to unseal the barrier.

## Initialization

`InitializeShamir` generates a root key, seals it, and splits the secret into N shares:

```go
result, err := barrier.InitializeShamir(ctx, seal.Credentials{})
// result.Shares: ["base64share1", ..., "base64share5"]
// result.Threshold: 3
// result.TotalShares: 5
```

Distribute shares to key holders. The barrier is unsealed after initialization.

## Dual-Mode Operation

| Mode | Strategy | Shamir Secret | Use Case |
|------|----------|---------------|----------|
| **Direct** | `StrategyShamir` | Root key itself | Multi-party root key custody |
| **Credential** | Any other strategy | Credential password | Password-based unsealing with quorum |

## Unsealing

### Stateful (one-at-a-time)

`UnsealWithShare` accumulates shares in an internal quorum accumulator with a 5-minute TTL:

```go
progress, _ := barrier.UnsealWithShare(ctx, shares[0])
// progress.Required: 3, progress.Submitted: 1, progress.Complete: false

progress, _ = barrier.UnsealWithShare(ctx, shares[2])
// progress.Submitted: 2, progress.Complete: false

progress, _ = barrier.UnsealWithShare(ctx, shares[4])
// progress.Submitted: 3, progress.Complete: true — barrier unsealed
```

Duplicate shares are rejected (`ErrShamirDuplicateShare`). The accumulator resets after TTL expiry (`ErrShamirQuorumExpired`).

### Stateless (batch)

```go
err := barrier.UnsealWithShares(ctx, shares[:3])
```

## Rekey

Generate new shares without changing the root key:

```go
result, err := barrier.Rekey(ctx, 4, 7) // new 4-of-7 split
// Old shares are deleted. New shares returned.
```

## Recovery Keys

Recovery keys are an independent Shamir split of the DEK (not root key) for disaster recovery. Shares are returned for offline storage and are NOT persisted.

```go
// Generate (barrier must be unsealed)
result, err := barrier.GenerateRecoveryKeys(ctx, 3, 5)

// Recover (barrier must be sealed)
err = barrier.RecoverWithKeys(ctx, recoveryShares[:3])
```

## Root Token Generation

`GenerateRootToken` produces a one-time admin token by proving knowledge of the master key through share reconstruction:

```go
token, err := barrier.GenerateRootToken(ctx, shares[:3])
// token.Token: hex-encoded HMAC-SHA256 value
```

## Error Reference

| Error | Condition |
|-------|-----------|
| `ErrShamirNotConfigured` | `BarrierConfig.Shamir` is nil |
| `ErrShamirThresholdInvalid` | Threshold < 2 or > total |
| `ErrShamirDuplicateShare` | Same share submitted twice |
| `ErrShamirQuorumExpired` | 5-min TTL elapsed |
| `ErrShamirQuorumIncomplete` | Not enough shares provided |
| `ErrShamirCombineFailed` | Share reconstruction failed |
| `ErrRecoveryKeysNotFound` | No recovery key metadata |
| `ErrRootTokenVerificationFailed` | Share verification failed |
