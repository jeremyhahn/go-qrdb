# Sealed-window endpoint allowlist

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-23
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

While the storage barrier is sealed, the daemon serves the unseal call, the
first-boot ceremony and one status answer, and nothing else. Every other
request -- health, node status and ceremony status included -- is refused with
the typed sealed refusal ([Response envelope](#response-envelope)):
`503 Service Unavailable` with `{"code":"SEALED"}` on REST, `codes.Unavailable`
with an `ErrorInfo` of domain `qrdb.v1` and reason `SEALED` on gRPC and Unix,
and the equivalent on QUIC, MCP and the embedded transport. Unseal progress
travels in the unseal call's own response.

The status answer is barrier status, and a sealed node's answer carries two
facts: `sealed: true` and `initialized`. It is served before the first boot
and after it, with no store read and no secret, so a client tells a node that
needs initialization (`initialized: false`) from an initialized sealed one
(`initialized: true`). The flag is read once while the node starts and cached
by the barrier (`seal.StorageBarrier.InitializedCached`), and initialization
sets it. A node held or cut off by a cluster seal, whose own barrier is
unsealed, refuses barrier status like every other call: operators read the
seal record from a serving node.

A client certificate authenticates the unseal caller at the TLS handshake,
with no store lookup, and grants nothing past the seal.

## Activation

The sealed-window gate is installed whenever `seal.enabled: true` in the
daemon's YAML config (`config.SealConfig.Enabled`). The probe is
`*seal.StorageBarrier`, threaded through the orchestrator via
`server.WithSealedWindowProbe(barrier)` and into every protocol server via
its own `WithSealedWindowProbe` option: REST, gRPC, QUIC, MCP, Unix,
PostgreSQL and MySQL.

When `seal.enabled: false` the option is not threaded, the gate is not
installed, and data is stored plaintext.

## Canonical allowlist (all transports)

The allowlist is defined ONCE, transport-neutrally, in
`pkg/api/sealedwindow` (`Table` keyed by canonical `Verb`). Each protocol
transport derives its own list from that table, so the lists cannot drift
apart:

| Transport | Derived var | Derivation |
|---|---|---|
| REST | `pkg/api/rest.SealedWindowAllowlist` | `sealedwindow.RESTAllowlist()` |
| gRPC | `pkg/api/grpc.SealedWindowAllowlist` | `sealedwindow.GRPCFullMethodAllowlist(serviceName)` |
| QUIC | `pkg/api/quic.SealedWindowAllowlist` | `sealedwindow.QUICAllowlist()` |
| MCP | `pkg/api/mcp.SealedWindowAllowlist` | `sealedwindow.MCPAllowlist()` |
| Embedded | `sdk/go/transport/embedded` `embeddedAllowlist` | `sealedwindow.EmbeddedAllowlist()` |

**Every verb on this list is anonymous on the boot-time sealed-window
listener**, which has no authentication middleware (see
[Auth model](#auth-model-during-the-sealed-window)). Each one is therefore
justified as an anonymous verb, and belongs to one of these classes:

- **The unseal call.** Credential possession, the Vault model, where holding
  the unseal credential is the authorization: `barrier-unseal`. The holder
  quorum, where the holder's factor proof against the roster the wrapped key
  is sealed under is the credential: `quorum-begin`/`quorum-submit`. The
  quorum service refuses every operation but unseal and recover while the
  barrier is sealed, and a submission reports the unseal's progress in its own
  response. The share claims that hand a holder the share it then submits:
  `share-claim-begin`/`-complete` and `recovery-share-claim-begin`/`-complete`
  (the holder's enrolled certificate). A barrier migrated to Shamir comes back
  sealed, and its holders claim their envelopes from the sealed node.
- **First boot**, trust on first use: `barrier-initialize`,
  `shamir-initialize`, `ceremony-initialize`, `officer-register`,
  `recovery-officer-register` and `ceremony-sign-csr` (SO PIN). These are
  `PreOperationalOnly`: they are served only while enrollment is open, on the
  regular listener as well as the boot-time one, and refused with the sealed
  refusal like any other call once it closes. An initialized barrier refuses
  the initialize verbs anyway, so a sealed node that holds a key never serves
  them.
- **The initialized flag**: `barrier-status`. It is `ListenerOnly`: served on
  the sealed-window path, and so while the node's own barrier is sealed, before
  the first boot and after it, and refused on the regular gate. A sealed
  barrier's answer is `sealed: true` and `initialized` and nothing else -- no
  strategy, share counts, timestamps or cluster seal state -- so it carries no
  secret and needs no store read. The regular gate refuses it on a node held or
  cut off by a cluster seal, whose barrier is unsealed; an unsealed, serving
  node answers the full status through its authenticated routes.

There is no liveness class. `health`, `status` and `ceremony-status` are
refused while sealed. `ceremony-status` stays in `sealedwindow.Table` only for
the awaiting-admin-credential gate, a state of an unsealed node, and is not on
the sealed surface (`sealedwindow.Verbs`).

**One verb is allowlisted on the regular listener only**:
`barrier-cluster-unseal` (`POST /v1/barrier/cluster-unseal`), which clears a
cluster-wide seal of the node barrier (see
[../barrier-seal/architecture.md](../barrier-seal/architecture.md#sealing-in-a-cluster)).
A node held by a cluster seal refuses everything outside the allowlist, so the
verb must pass the gate there; it is still authenticated and authorized by
`seal:unseal`. It is marked `AuthenticatedOnly` in the table, and the boot-time
listener's allowlist is derived without it, so it is never anonymous. The
regular listener's gate also closes, beyond a sealed barrier, while the node
is held by a cluster seal, has no fresh lease on the seal records, or has not
read them since it started. The verb is served only while the node's own
barrier is unsealed: with it sealed, the regular listener serves the boot-time
listener's path and refuses the verb with the sealed `503` like every other
`AuthenticatedOnly` verb (see
[Auth model](#auth-model-during-the-sealed-window)).

The full per-transport names are in `sealedwindow.Table`.

REST entries are paths, or method-qualified `METHOD /path` entries where
one path serves several verbs. `officer-register` is `POST
/v1/barrier/officers`, because `GET` on the same path is the officer list, and
`recovery-officer-register` is `POST /v1/barrier/recovery/officers`.
A path ending in `/` matches by prefix; a path with a `{name}` segment
matches one non-empty path segment in that place, which is how the claim
routes (`POST /v1/barrier/officers/{username}/claim/begin` and
`.../complete`, and their recovery forms) are listed, so that
`/v1/barrier/officers/revoke` is not admitted; anything else matches exactly
(`sealedwindow.RESTEntryMatches`). REST additionally allowlists one
transport-specific prefix with no transport-neutral verb
(`sealedwindow.RESTExtra`): `/v1/cluster/`, the cluster peer key transport.
Its receiver authorizes itself with the node certificate and a placement
check, and the auth middleware also exempts its two routes,
`/v1/cluster/keydelivery` and `/v1/cluster/nodekey`. gRPC entries are fully-qualified
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
`shamir-initialize`, `quorum-submit` and `ceremony-initialize`. Every request on the boot-time listener
holds the listener's `sealedwindow.Admission` for its whole duration. A
`Transition` request holds it exclusively and every other request holds it
shared, on all transports together. So an unsealing request waits for the
requests already in flight, none starts while it runs, and every request
admitted after it sees the barrier unsealed and is refused.

### Enrollment closes (`PreOperationalOnly`)

The first-boot verbs are `PreOperationalOnly`: `barrier-initialize`,
`shamir-initialize`, `ceremony-initialize`, `ceremony-sign-csr`,
`officer-register` and `recovery-officer-register`. Registration CA-signs any
CSR it is given and assigns a share index, so it is anonymous only while the
enrollment ceremony can still use a new officer. The listener asks
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
  officers) runs on the authenticated listener.

A listener with no ceremony service reads enrollment from the barrier alone:
it is open exactly while the barrier is uninitialized. It fails closed: an
unreadable record or barrier counts as closed. The reads run under the
request's context and a 2-second deadline (`enrollmentProbeTimeout`), so a
seal store that stops answering costs a refusal, not a request that holds the
listener's admission indefinitely. Once enrollment is closed a sealed node
refuses the first-boot verbs with the same typed sealed refusal it sends every
other call outside the unseal call (`sealedwindow.RefuseSealed`). A regular
listener applies this rule while the node barrier is sealed, because it then
serves through the boot-time listener's path (see
[Auth model](#auth-model-during-the-sealed-window)); once the barrier is
unsealed its authentication and the route's permission decide. The embedded
transport exists only once the node has unsealed, so its barrier is
initialized and enrollment closed: while sealed it refuses the first-boot
verbs.

The ceremony enforces the same rule itself. `RegisterOfficer`, which enrols
unseal and recovery holders alike, refuses with `ceremony.ErrEnrollmentClosed`
when the request is marked anonymous (`sealedwindow.Anonymous`) and enrollment is
closed. It checks this under the ceremony's lock, in the same critical
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
  sits beside the claim routes but is not on the allowlist: the claims are
  listed as one-segment patterns rather than an `/v1/barrier/officers/`
  prefix, so a sealed node answers the revocation 503 like any other
  excluded route.
- **enable-encryption** — the migration requires an unsealed data
  plane.
- **holder list** (`GET /v1/barrier/officers`, `BarrierOfficerList`,
  `barrier.OfficerList`, `qrdb_barrier_officer_list`). It is served
  post-unseal to authenticated callers.
- **recovery-key generate, delete and status**. Each one refuses while the
  barrier is sealed (`seal.ErrSealed`), so allowlisting them would add only an
  anonymous destructive surface.
- **`/v1/bootstrap/`, `/v1/init/` and `shamir-verify`**. None has a route on
  any transport. A prefix allowlisted over an empty subtree would silently
  exempt whatever is registered there next.

## MySQL / PostgreSQL wire protocols

The SQL wire protocols carry no barrier/ceremony verbs, so nothing is
allowlisted there; they are fail-closed for the whole sealed window.
When `server.WithSealedWindowProbe` is threaded (the same option that
drives the other transports):

- **MySQL** refuses every new connection during the handshake with the
  typed `sql/mysql: barrier sealed` error, and on a surviving session every
  query, prepare, execute and `COM_INIT_DB` with the same error.
- **PostgreSQL** refuses new sessions (FATAL) and rejects every Parse and
  every Bind/Execute -- including of a statement prepared before the seal --
  on surviving sessions (ERROR), both with SQLSTATE `57P03`
  (`cannot_connect_now`) and message `sql/pgsql: barrier sealed`.

Once the barrier is unsealed both gates are passthrough; on re-seal
they engage again, for new connections and open sessions alike.

The boot-time sealed-window listener does not start either protocol.

## Response envelope

Every call a sealed node does not serve gets one typed refusal, the same on
every transport (`sealedwindow.RefuseSealed`; constants
`sealedwindow.ErrorDomain`, `ReasonSealed`, `SealedMessage`). REST returns:

```http
HTTP/1.1 503 Service Unavailable
Content-Type: application/json
Retry-After: 5

{"error":"barrier sealed","code":"SEALED"}
```

| Transport | Refusal |
|---|---|
| REST | `503`, body `{"error":"barrier sealed","code":"SEALED"}` |
| gRPC and Unix | `codes.Unavailable`, message `barrier sealed`, detail `ErrorInfo{Domain: "qrdb.v1", Reason: "SEALED"}` |
| QUIC | `StatusUnavailable`, message `barrier sealed`, `wire.ErrorInfo{Domain: "qrdb.v1", Reason: "SEALED"}` |
| MCP | `UNAVAILABLE`, message `barrier sealed`, reason `SEALED` |
| Embedded | `*embedded.BarrierSealedError` (unwraps to `seal.ErrSealed`) |

The Go SDK decodes each of these into `*transport.BarrierSealedError`, tested
with `transport.IsBarrierSealed`; the CLI prints an error that begins
`transport: barrier sealed`.
Every SDK transport's `Connect`, and REST's reachability probe, count the
refusal as reachable: the node is up and sealed. `Healthy` stays false.
Readiness waits (`qrdbdharness.IsSealedRefusal`) read it the same way.

A node whose barrier is unsealed but which cannot confirm the cluster seal
state — cut off from the seal records, or not yet having read them — is not
sealed and does not answer `SEALED`. It refuses the same requests with
`503 {"code":"SEAL_STATE_UNCONFIRMED"}` and `Retry-After: 5`, or
`codes.Unavailable` with `ErrorInfo` reason `SEAL_STATE_UNCONFIRMED`; every
transport's form is in [../barrier-seal/api.md](../barrier-seal/api.md).

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

There are two listeners, and while the node barrier is sealed they serve the
same thing: a node whose barrier was re-sealed behaves exactly like a node
that booted sealed.

**The regular listener** is the one the node serves once it has booted. It
samples the node barrier on every request and picks one of two paths:

- **Node barrier sealed** (a re-seal, `/v1/barrier/seal` with `seal:seal`, or
  a quorum seal): the request takes the boot-time listener's path -- the same
  gate over the same router, built by the same code, with no authentication,
  no SO dual factor and no replica forwarding. The unseal call is served
  anonymously, barrier status answers the initialized flag, the first-boot verbs
  are served only while enrollment is open, and everything else -- `AuthenticatedOnly` verbs included -- is refused with the
  typed sealed refusal. No request reaches the user store, which the sealed
  barrier cannot read: go-quicraft refuses every read through a sealed barrier
  by design. The listener's TLS configuration does not change, so a client
  certificate is still required at the handshake wherever the listener
  requires one; it authenticates the unseal caller and grants nothing past
  the seal.
- **Node barrier unsealed**: the regular chain. On REST it runs, from the
  outside in, `canonical path → CORS → scope → sealed-window → auth → SO dual
  factor → router`; on gRPC and Unix the sealed-window interceptor runs after
  the auth interceptor. The gate here closes only for the cluster-wide reasons
  above -- a cluster seal holding the node, no fresh lease on the seal
  records, or no read of them since start -- and **an allowlisted request is
  still authenticated and authorized** by the route's permission. That is the
  path `barrier-cluster-unseal` takes on a node the cluster holds.

The dispatch keys on the node barrier alone, not on the cluster seal: a node
held by a cluster seal whose own barrier is unsealed can still read its user
store, so it stays on the authenticated path. A request admitted on the
sealed path while an unseal completes is refused by the listener gate's own
re-sample (`sealed-window listener closed: barrier unsealed`) and retried on
the regular path. Every listener of one node shares one admission
(`server.NewSealedWindowListenerProbe`), so an unseal waits for requests
already admitted anonymously on any of them.

Two surfaces stay on the regular path while sealed. MCP's enrollment endpoint
(`EnrollPath`) is public by design and is served as on an unsealed node; the
invitation it redeems lives behind the barrier, so it fails with the typed
sealed error. The embedded transport has no listener; a call through it while
the barrier is sealed returns the typed sealed error, never an internal error
or a user-store failure.

**The boot-time sealed-window listener** (`server.ServeSealedWindow`, see
[below](#pre-host-listener-boot-time-sealed-window)) has **no
authentication middleware**, because while the barrier is sealed there is
no identity plane to authenticate against. Its allowlist is anonymous. That
is the Vault model: possession of the unseal credential (passphrase, Shamir
share, or a hardware-bound authentication context) is itself the
authorization to drive unseal. Every action is recorded in the audit log.

**No authentication does not mean no certificate verification.** On both
listeners, and on every transport (REST, gRPC, QUIC, MCP, Unix), the peer's
TLS state and the listener's chain verifier (the client trust pool, set by
`server.WithPeerChainVerifier`) are bound to every request outermost, before
the sealed-window gate and independent of authentication: REST
`peerTLSMiddleware`, gRPC and Unix `middleware.PeerTLSUnaryInterceptor` /
`PeerTLSStreamInterceptor`, QUIC `withConnectionState`, MCP
`authHTTPContext`. A share claim in the sealed window therefore binds the
caller's certificate only when that chain verifies, exactly as after unseal;
a merely presented or absent certificate is refused with 401 /
`Unauthenticated` (`service.ClientCertRequiredError`). See
[Shamir custody](../shamir-custody/engineering.md).

That is why the allowlist is kept to the verbs that are justified as
anonymous, and why this listener's gate differs from the regular one: **once
the barrier is unsealed it refuses every request, allowlisted or not.** It
samples the barrier on every request, so it does not depend on the 250 ms
poll (`sealedWindowPollInterval`) that stops the listener: no route
registered on it, `DELETE /v1/barrier/recovery` and the credential routes
included, runs anonymously against an unsealed barrier. The listener does not start the MySQL or PostgreSQL wire protocols, because
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

There is no in-place upgrade from a pre-encfs data dir: the only remedy is
migrating the data in that directory through application-level
export/import. The error unwraps to `seal.ErrLegacyDataDir`, so callers
match it with `errors.Is`.

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
   `store.WithEncryptor(atrest.New(barrier))` as its first pebble option, so
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
   share. A node that holds no CA key on disk (a joined member, or
   any member once Key A is replicated) has no ceremony CA in the
   sealed window. Once the barrier unseals, every node's ceremony
   signs through a CA over the replicated Key A
   (`__sys:cluster_ca/`), so every node serves every CA operation
   from then on, with writes going through the Raft leader. With BYO
   TLS certificates there is no Key A at all: no ceremony CA is ever
   constructed, holder enrolment refuses (`ErrCANotInitialized`),
   and every verify-only holder operation is
   still served.
   `SetCA` never overwrites a CA established by `Initialize`; the
   ceremony-established CA stays authoritative.
4. `ServeSealedWindow` polls the probe on a 250ms ticker
   (channel-based; no `time.Sleep`) and either (a) tears the listener
   down and returns nil when the operator drives unseal, or (b) tears
   the listener down and returns `*server.SealedWindowCanceledError`
   on `SIGINT`/`SIGTERM`, which the daemon treats as a clean exit.
   The poll only stops the listener. Refusing requests after unseal is
   done by the listener's gate on every request, so the window between
   unseal and `Stop` serves nothing. `Stop` does not release the ports:
   `cmd/qrdbd` binds the REST, HTTP/3, gRPC, QUIC transport and MCP
   sockets before the sealed-window listener starts and hands the same sockets to the
   production server in step 5, so nothing else on the host can take a
   port while the node boots (see "Pre-bound sockets" in
   [the API server architecture](../api-server/architecture.md)).
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

When `seal.enabled: false` (no encfs), the boot skips steps 1-4.

## Mounting the sealed surface in a host

A host that runs its own listeners while qrdb's barrier is sealed (go-quicbaas,
for one) mounts qrdb's sealed surface instead of reimplementing it. There is
one implementation: qrdb's own boot-time listener is built from the same
constructors (`rest.NewSealedWindowServer`, `mcp.NewSealedWindowServer`).

```go
import sdkserver "github.com/jeremyhahn/go-qrdb/sdk/go/server"

probe := sdkserver.NewSealedWindowListenerProbe(barrier, ceremonySvc)

// REST, and HTTP/3 on the same handler.
sealed := sdkserver.NewSealedWindowHandler(barrierSvc, ceremonySvc, credentialSvc, probe)
mux.Handle("/v1/barrier/", sealed)
mux.Handle("/v1/ceremony/", sealed)
mux.Handle("/health", sealed)

// MCP.
mcpServer.AddTools(sdkserver.SealedWindowMCPTools(barrierSvc, ceremonySvc, credentialSvc, probe)...)
```

- **The whole allowlist.** The unseal call -- passphrase unseal, the holder
  quorum, officer and recovery share claims -- barrier status, and the
  first-boot verbs while enrollment is open. The MCP set is exactly the allowlisted tools the given
  services can serve.
- **One status answer, no health.** Barrier status answers `sealed: true` and
  the `initialized` flag, on REST, MCP and gRPC alike. On REST, `GET /health`
  and ceremony status answer the sealed envelope, 503 `{"code":"SEALED"}`. The
  MCP set carries no health or node status tool, and the gRPC `Health` RPC is
  refused with the typed `Unavailable`.
- **One probe for every transport.** Build it once and pass it to the REST
  handler, the MCP tools and the gRPC listener interceptors
  (`sdkserver.SealedWindowListenerUnaryInterceptor` and
  `sdkserver.SealedWindowListenerStreamInterceptor`). They then share one
  `Admission`, so a request that unseals the barrier waits for requests already
  admitted on any transport. No request admitted afterwards runs anonymously
  against the unsealed barrier.
- **The gate travels with the handlers.** It allowlists anonymously while
  sealed and closes enrollment once the ceremony is operational or the
  barrier initialized. Once
  the barrier is unsealed, every mounted route and tool refuses: REST with 503
  `SEALED_WINDOW_CLOSED`, MCP with `UNAVAILABLE`.
- **The CA for officer CSRs comes from the ceremony core.** It is set with
  `CeremonyService.SetCA`, which qrdbd does at boot through
  `lifecycle.Infra.CeremonyCA`. Without a CA, officer registration answers
  `ceremony: CA not initialized`.
- **Pinned by tests.** `sdk/go/node/sealed_window_mount_test.go` mounts both
  surfaces in a host-style mux and MCP server.
