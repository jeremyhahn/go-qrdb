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

**Every verb on this list is anonymous on the boot-time sealed-window
listener**, which has no authentication middleware (see
[Auth model](#auth-model-during-the-sealed-window)). Each one is therefore
justified as an anonymous verb, and belongs to one of these classes:

- **Liveness**: `health`, `status`, `barrier-status`.
- **Credential possession**, the Vault model, where holding the unseal
  credential is the authorization: `barrier-unseal`.
- **First-boot trust on first use**: `barrier-initialize`,
  `shamir-initialize`, `ceremony-initialize`, `officer-register`,
  `recovery-officer-register`. Registration is admitted only while the
  enrolment ceremony is pre-operational (`PreOperationalOnly`).
- **Handshakes whose binding authorization the handler enforces**:
  `share-claim-begin`/`-complete` and
  `recovery-share-claim-begin`/`-complete` (the holder's enrolled
  certificate), `quorum-begin`/`quorum-submit` (the holder's factor proof
  against the roster the wrapped key is sealed under; every quorum operation
  -- unseal, seal, root-token, rekey completion, recover -- is a submission,
  so these are how a sealed node is unsealed), and `ceremony-sign-csr` (SO
  PIN). `ceremony-status` reports the ceremony state.

The full per-transport names are in `sealedwindow.Table`.

REST entries are paths, or method-qualified `METHOD /path` entries where
one path serves several verbs. `officer-register` is `POST
/v1/barrier/officers`, because `GET` on the same path is the officer list, and
`recovery-officer-register` is `POST /v1/barrier/recovery/officers`.
A path ending in `/` matches by prefix, and anything else matches exactly
(`sealedwindow.RESTEntryMatches`). REST additionally allowlists one
transport-specific prefix with no transport-neutral verb
(`sealedwindow.RESTExtra`): `/v1/cluster/`, the cluster peer key transport.
Its receiver authorizes itself with the node certificate and a placement
check, and the auth middleware also pre-exempts `/v1/cluster/`
unconditionally. gRPC entries are fully-qualified
`/qrdb.v1.QRDBService/<Method>` paths, so a method of the same name on
another service is not admitted.

The share-holder registration and claim routes are allowlisted because
first-boot Shamir initialization is reachable only through the
sealed-window listener: holders must be enrolled (and able to claim their
envelopes) before and immediately after `InitializeShamir` runs, while the
barrier is still sealed. The quorum routes are allowlisted because they are
the only way a sealed Shamir barrier opens. A holder authorizes itself on
each of them — certificate, nonce signature and factor proof against the
roster — so being reachable while sealed grants nothing by itself.

### No request runs across the unseal (`Transition`, `Admission`)

The gate checks the barrier when a request arrives. Without more than that, a
request admitted while sealed could still be running when another request
unseals the barrier, and would finish anonymously against an unsealed
barrier. The verbs that can take the barrier out of the sealed state are
marked `Transition`: `barrier-initialize`, `barrier-unseal`,
`shamir-initialize`, `shamir-unseal-share`, `shamir-unseal-shares`,
`recovery-recover`, `ceremony-initialize` and
`ceremony-submit-unseal-share`. Every request on the boot-time listener
holds the listener's `sealedwindow.Admission` for its whole duration. A
`Transition` request holds it exclusively and every other request holds it
shared, on all transports together. So an unsealing request waits for the
requests already in flight, none starts while it runs, and every request
admitted after it sees the barrier unsealed and is refused.

### Officer enrollment closes (`PreOperationalOnly`)

`officer-register` and `recovery-officer-register` are the two
`PreOperationalOnly` verbs. Registration CA-signs any CSR it is given and
assigns a share index, so it is anonymous on the boot-time listener only while
the enrollment ceremony can still use a new officer. The listener asks
`ceremony.CeremonyService.EnrollmentOpen` on every request, and enrollment
closes as soon as any of these is true:

- the ceremony is operational in this process;
- the barrier records an operational ceremony. The ceremony writes that record
  (`seal.StorageBarrier.MarkCeremonyOperational`) to the barrier's plaintext
  store when it reaches operational. The store is readable while sealed, so
  the record survives a restart while sealed, when the ceremony's in-memory
  state starts over;
- the barrier is initialized (`seal.StorageBarrier.Initialized`). Shares are
  distributed at initialization, and every later enrollment (rekey, recovery
  officers) runs on the authenticated listener. This also covers a node whose
  ceremony completed before the record existed.

It fails closed: an unreadable record, or a listener with no ceremony
service, counts as closed. The reads run under the request's context and a
2-second deadline (`enrollmentProbeTimeout`), so a seal store that stops
answering costs a refusal, not a request that holds the listener's
admission indefinitely. Once enrollment is closed the listener answers
registration with `403` and `{"code":"ENROLLMENT_CLOSED"}` on REST,
`PERMISSION_DENIED` on gRPC and Unix, `StatusPermissionDenied` on QUIC and
`PERMISSION_DENIED` on MCP. There is no `Retry-After`, because waiting does
not reopen enrollment. A regular listener never applies this rule: its
authentication and the route's `seal:officer_register` permission decide.

The ceremony enforces the same rule itself. `RegisterOfficer` and
`RegisterRecoveryOfficer` refuse with `ceremony.ErrEnrollmentClosed` when the
request is marked anonymous (`sealedwindow.Anonymous`) and enrollment is
closed. They check this under the ceremony's lock, in the same critical
section as the registration. The gate samples enrollment before the request
runs; this check holds even if enrollment closes in between, and covers any
path that reaches the ceremony anonymously. The error classifies as
permission denied on every transport.

### Deliberate exclusions

Never allowlisted, on any transport:

- **seal** (`/v1/barrier/seal`, `BarrierSeal`, `barrier.Seal`,
  `qrdb_barrier_seal`) — sealing is only meaningful while the barrier is
  unsealed, so it is served by the regular (post-unseal) listener. (A
  Shamir barrier seals through `quorum-submit` with op `seal`.)
- **rekey start and holder revocation** (`BarrierRekeyStart`,
  `BarrierOfficerRevoke` and their QUIC and MCP forms) — governance
  operations served post-unseal. On REST, `/v1/barrier/officers/revoke`
  falls under the `/v1/barrier/officers/` claim prefix and so reaches its
  handler while sealed; it is still gated by `seal:officer_revoke` and the SO
  PIN, and a rekey of a sealed barrier is refused with `ErrSealed`.
- **enable-encryption** — the migration requires an unsealed data
  plane.
- **holder list** (`GET /v1/barrier/officers`, `BarrierOfficerList`,
  `barrier.OfficerList`, `qrdb_barrier_officer_list`). It is served
  post-unseal to authenticated callers.
- **recovery-key generate, delete and status**. Each one refuses while the
  barrier is sealed (`seal.ErrSealed`), so allowlisting them added only an
  anonymous destructive surface. Nothing is lost by refusing them at the
  gate.
- **`/v1/bootstrap/`, `/v1/init/` and `shamir-verify`**. None has a route on
  any transport. A prefix allowlisted over an empty subtree would silently
  exempt whatever is registered there next.

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

The boot-time sealed-window listener does not start either protocol.

## Response envelope

REST returns:

```http
HTTP/1.1 503 Service Unavailable
Content-Type: application/json
Retry-After: 5

{"error":"barrier sealed","code":"SEALED"}
```

gRPC returns `codes.Unavailable` with message `"barrier sealed"`.

Once its barrier is unsealed, the boot-time sealed-window listener refuses
every request with a distinct envelope:

```http
HTTP/1.1 503 Service Unavailable
Content-Type: application/json
Retry-After: 5

{"error":"sealed-window listener closed: barrier unsealed","code":"SEALED_WINDOW_CLOSED"}
```

gRPC and Unix return `codes.Unavailable`, QUIC returns `StatusUnavailable`,
and MCP returns `UNAVAILABLE` for a tool call, all with the message
`sealed-window listener closed: barrier unsealed`. MCP also answers any HTTP
request to its endpoint with 503 before the MCP layer reads it. The client
retries, and the regular listener answers once the boot completes.

## Auth model during the sealed window

There are two listeners, and they differ in exactly one respect.

**The regular listener** is the one the node serves once it has booted.
When its barrier is re-sealed, its gate refuses non-allowlisted requests.
Every transport runs both the gate and authentication. On REST the chain, from
the outside in, is `canonical path → CORS → scope → sealed-window → auth →
SO dual factor → router`, and on gRPC and Unix the sealed-window
interceptor runs after the auth interceptor. **An allowlisted request is
therefore still authenticated and authorized** by the route's permission
on this listener. Nothing on it is anonymous because it is allowlisted.
After unseal the gate passes everything to authentication. On re-seal
(`/v1/barrier/seal` with `seal:seal`) it engages again.

**The boot-time sealed-window listener** (`server.ServeSealedWindow`, see
[below](#pre-host-listener-boot-time-sealed-window)) has **no
authentication middleware**, because while the barrier is sealed there is
no identity plane to authenticate against. Its allowlist is anonymous. That
is the Vault model: possession of the unseal credential (passphrase, Shamir
share, or a hardware-bound authentication context) is itself the
authorization to drive unseal. Every action is recorded in the audit log.

That is why the allowlist is kept to the verbs that are justified as
anonymous, and why this listener's gate differs from the regular one: **once
the barrier is unsealed it refuses every request, allowlisted or not.** It
samples the barrier on every request, so it does not depend on the 250 ms
poll that stops the listener. Before this, the gate became a pass-through at
unseal, and until the poll noticed and `Stop` completed, every route
registered on the listener ran anonymously against an unsealed barrier. That
included `DELETE /v1/barrier/recovery` and the credential routes. The
listener does not start the MySQL or PostgreSQL wire protocols, because
they carry no sealed-window verb.

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
no sentinel the daemon refuses to start.

The refusal is reported as `LegacyDataDirError`, which names the data
directory the operator has to act on and states what was detected:

```
cli: refusing to start on legacy data dir /var/lib/qrdb (pre-encfs
format detected): qrdb/seal: data dir was created with a
pre-barrier-refactor version; in-place upgrade is not supported
```

The directory is part of the message because this refusal is not
something the daemon can resolve on its own and not something a
generic "seal barrier initialization failed" tells an operator how to
act on: there is no in-place upgrade from pre-encfs data dirs, so the
only remedy is migrating data through application-level export/import,
and the operator needs to know which directory to migrate. The error
unwraps to `seal.ErrLegacyDataDir`, so callers matching the sentinel
with `errors.Is` are unaffected.

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
   boot-time listener's gate on every transport (appended
   automatically, over any `WithSealedWindowProbe` in the options). The
   gate keeps the canonical allowlist active so only the unseal-related
   endpoints accept requests while sealed, and refuses everything once
   unsealed. SDK embedders
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
   The poll only stops the listener. Refusing requests after unseal is
   done by the listener's gate on every request, so the window between
   unseal and `Stop` serves nothing.
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

## Mounting the sealed surface in a host

A host that runs its own listeners while qrdb's barrier is sealed (go-quicbaas,
for one) mounts qrdb's sealed surface instead of reimplementing it. There is
one implementation: qrdb's own boot-time listener is built from the same
constructors (`rest.NewSealedWindowServer`, `mcp.NewSealedWindowServer`).

```go
probe := sdk.NewSealedWindowListenerProbe(barrier, ceremonySvc)

// REST, and HTTP/3 on the same handler.
sealed := sdk.NewSealedWindowHandler(barrierSvc, ceremonySvc, credentialSvc, probe)
mux.Handle("/v1/barrier/", sealed)
mux.Handle("/v1/ceremony/", sealed)
mux.Handle("/health", sealed)

// MCP.
mcpServer.AddTools(sdk.SealedWindowMCPTools(barrierSvc, ceremonySvc, credentialSvc, probe)...)
```

- **The whole allowlist.** Barrier status, initialize and unseal, Shamir and
  recovery unseal, officer registration, officer and recovery share claims,
  claim status, the ceremony verbs and the root token. The MCP set is exactly
  the allowlisted tools the given services can serve.
- **Health.** On REST, `GET /health` answers the sealed envelope, 503
  `{"code":"SEALED"}`. The `qrdb_health` tool answers a health status of
  `sealed`, as the gRPC `Health` RPC does.
- **One probe for every transport.** Build it once and pass it to the REST
  handler, the MCP tools and the gRPC listener interceptors
  (`sdk.SealedWindowListenerUnaryInterceptor` and
  `sdk.SealedWindowListenerStreamInterceptor`). They then share one
  `Admission`, so a request that unseals the barrier waits for requests already
  admitted on any transport. No request admitted afterwards runs anonymously
  against the unsealed barrier.
- **The gate travels with the handlers.** It allowlists anonymously while
  sealed and closes officer enrollment once the ceremony is operational. Once
  the barrier is unsealed, every mounted route and tool refuses: REST with 503
  `SEALED_WINDOW_CLOSED`, MCP with `UNAVAILABLE`.
- **The CA for officer CSRs comes from the ceremony core.** It is set with
  `CeremonyService.SetCA`, which qrdbd does at boot through
  `lifecycle.Infra.CeremonyCA`. Without a CA, officer registration answers
  `ceremony: CA not initialized`.
- **Pinned by tests.** `sdk/go/node/sealed_window_mount_test.go` mounts both
  surfaces in a host-style mux and MCP server.
