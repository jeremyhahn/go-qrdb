# Sealed-window endpoint allowlist

While the storage barrier is sealed, the daemon serves only the
endpoints required to drive unseal. Every other request returns
`503 Service Unavailable` (REST) or `codes.Unavailable` (gRPC) with
the documented sealed envelope.

## Activation

The sealed-window middleware is installed automatically whenever
`barrier.enabled: true` in the daemon's YAML config. The probe is
`*seal.StorageBarrier`, threaded through the orchestrator via
`server.WithSealedWindowProbe(barrier)` and into both REST and gRPC
protocol servers via the per-protocol options
`rest.WithSealedWindowProbe` and `grpc.WithSealedWindowProbe`.

When `barrier.enabled: false` the option is not threaded and the
middleware is not installed; deployments remain plaintext as before.

## REST allowlist

The canonical allowlist lives in
`pkg/api/rest/middleware_sealed.go:SealedWindowAllowlist`. Entries
ending in `/` match by prefix; everything else is an exact match:

- `/v1/health`
- `/v1/barrier/status`
- `/v1/barrier/initialize`
- `/v1/barrier/unseal`
- `/v1/barrier/shamir/initialize`
- `/v1/barrier/shamir/unseal-share`
- `/v1/barrier/shamir/unseal-shares`
- `/v1/barrier/shamir/verify`
- `/v1/barrier/recovery/generate`
- `/v1/barrier/recovery/recover`
- `/v1/barrier/recovery/status`
- `/v1/init/` (prefix)
- `/v1/bootstrap/` (prefix)

## gRPC allowlist

The canonical allowlist lives in
`pkg/api/grpc/sealed_window_interceptor.go:SealedWindowAllowlist`.
Entries are method tail components — the suffix after the last `/`
in `info.FullMethod`:

- `Health`, `Status`
- `BarrierStatus`, `BarrierInitialize`, `BarrierUnseal`
- `BarrierShamir{Initialize,UnsealShare,UnsealShares,ListShares,DeleteShare,DeleteAllShares,Verify}`
- `BarrierRekey`
- `BarrierRecovery{Generate,Recover,Delete,Status}`
- `BarrierRootToken`
- `Ceremony{Initialize,Status,ClaimCertBegin,ClaimCertComplete,ClaimShare,SignCSR,EnrollExternalOfficer}`

## Response envelope

REST returns:

```http
HTTP/1.1 503 Service Unavailable
Content-Type: application/json
Retry-After: 5

{"error":"barrier sealed","code":"SEALED"}
```

gRPC returns `codes.Unavailable` with message `"barrier sealed"`.

## Auth model during the sealed window

Allowlisted endpoints flow through the middleware **before** auth in
the REST chain (`mux → auth → sealed-window → scope → CORS`) and
**after** auth in the gRPC interceptor chain. In both cases auth is
optional for allowlisted endpoints — the unseal endpoints in
particular are unauthenticated by design. This is the Vault model:
possession of the unseal credential (passphrase, Shamir share, or
hardware-bound TPM/HSM authentication context) is itself the
authorization to drive unseal. Every action is recorded in the
audit log regardless of the auth path used.

After unseal, the middleware becomes a passthrough and the regular
auth middleware takes effect for all endpoints. On re-seal (operator
calls `/v1/barrier/seal` with `barrier:seal` permission), the
sealed-window engages again automatically.

## Format-version sentinel

The seal sub-store carries a one-key sentinel
`__sys/barrier_format_version` written at first init. The daemon
calls `seal.EnsureFormatSentinel(ctx, sealBackend, sealStoreEmpty)`
before constructing the barrier; on a non-empty seal sub-store with
no sentinel the daemon refuses to start with
`seal.ErrLegacyDataDir` ("data dir was created with a
pre-barrier-refactor version; in-place upgrade is not supported").
There is no in-place upgrade from pre-encfs data dirs; operators
must migrate data through application-level export/import.

## Pre-host listener (boot-time sealed window)

With encfs enabled, the data-plane Pebble instance cannot be
opened while the barrier is sealed: every file open in the
encrypting `vfs.FS` returns `seal.ErrSealed`. The daemon therefore
restructures the boot sequence as follows:

1. `cmd/qrdbd/seal_subsystem.go:initSealSubsystem` builds the seal
   sub-store, sealing strategy, `*seal.StorageBarrier`, credential
   service, and barrier registry **before** the host. The seal
   sub-store is a separate plaintext PebbleDB instance that holds
   the wrapped barrier root key, strategy blobs, and the
   format-version sentinel — it must be readable before unseal so
   the unseal flow can find its key material.
2. The state-machine factory (`store.NewMVCCCreateFuncWith` /
   `store.NewPebbleCreateFuncWith`) is built with
   `store.WithEncryptingFS(barrier)` as its first pebble option, so
   when `initializeNode` later opens the data-plane Pebble it does
   so on top of the encfs `vfs.FS`.
3. While `barrier.IsSealed()` is true, `awaitUnseal` brings up an
   API-only `server.New(...)` configured with TLS, the seal-only
   service set produced by
   `pkg/server/lifecycle.BuildSealServicesOnly`, and the
   `server.WithSealedWindowProbe(barrier)` middleware. The probe
   keeps the canonical allowlist active so only the unseal-related
   endpoints accept requests.
4. The listener loop polls `barrier.IsSealed()` on a 250ms ticker
   (channel-based; no `time.Sleep`) and either (a) tears the
   listener down and continues the boot when the operator drives
   unseal, or (b) tears the listener down and exits cleanly on
   `SIGINT`/`SIGTERM`.
5. After unseal, the regular boot resumes: `initializeNode` opens
   the host on top of the encfs-wrapped Pebble,
   `lifecycle.BuildServices` constructs the full service graph,
   and the production multi-protocol server starts. The seal
   barrier, credential service, and barrier registry instances
   built in step 1 are threaded into `BuildServices` via `Infra`
   so the post-unseal graph reuses the same underlying seal state.

When `barrier.enabled: false` (no encfs), the boot skips steps 1-4
entirely and behaves as before.
