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

## Canonical allowlist (all transports)

The allowlist is defined ONCE, transport-neutrally, in
`pkg/api/sealedwindow` (`Table` keyed by canonical `Verb`). Each protocol
transport derives its own list from that table, so the four lists cannot
drift apart:

| Transport | Derived var | Derivation |
|---|---|---|
| REST | `pkg/api/rest.SealedWindowAllowlist` | `sealedwindow.RESTAllowlist()` |
| gRPC | `pkg/api/grpc.SealedWindowAllowlist` | `sealedwindow.GRPCAllowlist()` |
| QUIC | `pkg/api/quic.SealedWindowAllowlist` | `sealedwindow.QUICAllowlist()` |
| MCP | `pkg/api/mcp.SealedWindowAllowlist` | `sealedwindow.MCPAllowlist()` |

The canonical verb set (union of the historical per-transport lists,
minus the deliberate exclusions below):

- `health`, `status` — liveness probes.
- Barrier lifecycle: `barrier-status`, `barrier-initialize`,
  `barrier-unseal`.
- Shamir: `shamir-initialize`, `shamir-unseal-share`,
  `shamir-unseal-shares`, `shamir-verify` (REST + QUIC only — no gRPC
  RPC or MCP tool exists), `rekey`, `root-token`.
- Recovery: `recovery-generate`, `recovery-recover`, `recovery-delete`,
  `recovery-status`.
- Security Officers: `officer-register`, `officer-list`,
  `officer-claim-status`, `share-claim-begin`, `share-claim-complete`,
  `recovery-officer-register`, `recovery-share-claim-begin`,
  `recovery-share-claim-complete`.
- Ceremony: `ceremony-initialize`, `ceremony-status`,
  `ceremony-claim-cert-begin`, `ceremony-claim-cert-complete`,
  `ceremony-claim-share`, `ceremony-sign-csr`,
  `ceremony-claim-share-begin`, `ceremony-claim-share-complete`,
  `ceremony-submit-unseal-share`, `ceremony-enroll-external-officer`
  (gRPC method; on REST the same operation lives at
  `/v1/init/enroll-external-officer`, covered by the `/v1/init/`
  prefix).

REST additionally allowlists three transport-specific prefixes with no
transport-neutral verb (`sealedwindow.RESTExtra`): `/v1/init/`,
`/v1/bootstrap/`, and `/v1/cluster/` (cluster peer key-transport; the
receiver self-authorises via node-cert + placement check, and the auth
middleware also pre-exempts `/v1/cluster/` unconditionally). REST path
entries ending in `/` match by prefix; everything else is an exact
match. gRPC entries are method tails (the suffix after the last `/` in
`info.FullMethod`).

The Security Officer registration and share-claim routes are
allowlisted because first-boot Shamir initialization is reachable
only through the sealed-window listener: officers must be registered
(and able to claim their shares) before and immediately after
`InitializeShamir` runs, while the barrier is still sealed.

### Deliberate exclusions

Never allowlisted, on any transport:

- **seal** (`/v1/barrier/seal`, `BarrierSeal`, `barrier.Seal`,
  `qrdb_barrier_seal`) and **Shamir seal-vote** — sealing is only
  meaningful while the barrier is unsealed, so these are served by the
  regular (post-unseal) listener.
- **enable-encryption** — the migration requires an unsealed data
  plane.
- **Shamir share management** (`barrier.ShamirListShares` /
  `ShamirDeleteShare` / `ShamirDeleteAllShares`) — post-unseal
  administration.
- **custodian enroll/revoke** (`/v1/ceremony/enroll-custodian`,
  `/v1/ceremony/revoke-custodian`) — SO-gated governance operations
  served post-unseal.

## MySQL / PostgreSQL wire protocols

The SQL wire protocols carry no barrier/ceremony verbs, so nothing is
allowlisted there; they are fail-closed for the whole sealed window.
When `server.WithSealedWindowProbe` is threaded (the same option that
drives the other transports):

- **MySQL** refuses every new connection during the handshake with the
  typed `sql/mysql: barrier sealed` error.
- **PostgreSQL** refuses new sessions (FATAL) and rejects every query
  on surviving sessions (ERROR), both with SQLSTATE `57P03`
  (`cannot_connect_now`) and message `sql/pgsql: barrier sealed`.

Once the barrier is unsealed both gates are passthrough; on re-seal
they engage again (MySQL for new connections; established MySQL
sessions fail at the storage layer with `seal.ErrSealed`).

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

### Identity-gated claim routes after unseal

The officer share-claim routes (system, recovery, ceremony
custodian, and tenant barrier — on all four transports) keep the
Vault posture **after** unseal: they map to the
`permission.IdentityGated` sentinel instead of an RBAC permission.
The middleware requires only an authenticated mTLS identity (no
certificate → 401) and audits the decision; the binding
authorization — officer-registry membership, certificate-thumbprint
match against the enrollment record (mismatch → 403), and
nonce-signature or factor verification — is enforced at the handler
layer. This is necessary, not merely convenient: Security Officers
are registered in the ceremony registry during the sealed window,
before the Raft-backed RBAC user store exists, so they are never
RBAC users and no role grant could authorize them. The sentinel is
deliberately unregistered — it cannot be granted to any role and
never appears in the permission catalog.

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
3. While `barrier.IsSealed()` is true, `awaitUnseal` brings up the
   sealed-window listener via the shared
   `pkg/server.ServeSealedWindow(ctx, cfg, probe, opts...)`: an
   API-only `server.New(...)` configured with TLS, the seal-only
   service set produced by
   `pkg/server/lifecycle.BuildSealServicesOnly`, and the
   `server.WithSealedWindowProbe(barrier)` middleware (appended
   automatically). The probe keeps the canonical allowlist active so
   only the unseal-related endpoints accept requests. SDK embedders
   get the same listener for nodes returned in the sealed state via
   `sdk/go/node.ServeSealedWindow(ctx, n, extraOpts...)`, which serves
   the node's own seal-service instances and returns once the operator
   drives the unseal (the caller then completes the boot with
   `n.Unseal`).

   When the daemon manages its own TLS material (auto-generated or
   config-provided CA cert **and** key), `cmd/qrdbd` constructs a
   `ceremony.LocalCA` from the cluster CA bundle
   (`ceremonyCAFromBundle`) and wires it into the ceremony core via
   `lifecycle.Infra.CeremonyCA` → `CeremonyService.SetCA`. Officer
   CSRs signed during the sealed window therefore chain to the same
   CA the server trusts for mTLS client auth, so a registered
   security officer can immediately authenticate to claim their
   share. With BYO TLS certificates (no CA key available), no
   `LocalCA` is constructed and officer registration stays gated on
   the full initialization ceremony (`ErrCANotInitialized`).
   `SetCA` never overwrites a CA established by `Initialize`; the
   ceremony-established CA stays authoritative.
4. `ServeSealedWindow` polls the probe on a 250ms ticker
   (channel-based; no `time.Sleep`) and either (a) tears the listener
   down and returns nil when the operator drives unseal, or (b) tears
   the listener down and returns `*server.SealedWindowCanceledError`
   on `SIGINT`/`SIGTERM`, which the daemon treats as a clean exit.
5. After unseal, the regular boot resumes: `initializeNode` opens
   the host on top of the encfs-wrapped Pebble,
   `lifecycle.BuildServices` constructs the full service graph,
   and the production multi-protocol server starts. The seal
   barrier, credential service, and barrier registry instances
   built in step 1 are threaded into `BuildServices` via `Infra`
   so the post-unseal graph reuses the same underlying seal state.
   `awaitUnseal` returns the sealed-window `*lifecycle.Node`, and
   `start.go` extracts its ceremony core
   (`service.CeremonyServiceCore`) and threads it into
   `BuildServices` via `Infra.CeremonyCoreSvc`. The full boot
   therefore reuses the **same** `ceremony.CeremonyService`
   instance that served the sealed window, preserving its
   in-memory state: enclave-held pending shares
   (`sealedShareStore`), cache-only officer registrations made
   while the registry was sealed, pending officer certificates,
   and outstanding claim challenges. Without this reuse, shares
   sealed to officers during first-boot Shamir initialization
   would be unclaimable after the boot completes.

When `barrier.enabled: false` (no encfs), the boot skips steps 1-4
entirely and behaves as before.
