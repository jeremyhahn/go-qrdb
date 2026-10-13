# Embedding QRDB

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-13
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

QRDB can be embedded directly into Go applications via the `sdk/go/node` package. Two builder functions cover the most common deployment shapes:

| Builder | Description |
|---|---|
| `NewStandaloneNode` | Engine-direct (no Raft). Suitable for single-process apps, edge deployments, CLI tools, and unit tests. Uses `pkg/backend.LocalBackend`. |
| `NewClusteredNode` | Full Raft consensus via `go-quicraft`. Suitable for multi-node HA clusters. Uses `pkg/backend/raft.HostAdapter`. |

Both builders return a `*node.Node` that exposes:

```go
type Node struct { ... }

// Server returns the started multi-protocol server.
func (n *Node) Server() *server.Server

// Client returns an in-process SDK client connected to this node.
func (n *Node) Client() *sdk.Client

// Services returns the lifecycle.Node aggregating all service instances.
func (n *Node) Services() *lifecycle.Node

// KVService returns the node's KVServicer.
func (n *Node) KVService() service.KVServicer

// AdminService returns the node's AdminServicer.
func (n *Node) AdminService() service.AdminServicer

// Close tears down all resources in reverse order. It blocks until an
// in-flight Unseal boot or system EnableEncryption finishes, and is
// idempotent; Unseal and EnableEncryption after Close return ErrNodeClosed.
func (n *Node) Close() error
```

---

## Trust model

**The embedded transport (`n.Client()`, `n.AdminService()`, and every
service accessible through `n.Services()`) is a fully trusted, in-process
surface with no authorization of its own.** Every remote transport (REST,
gRPC, QUIC, Unix, MCP) enforces permissions in its own handler or
middleware layer — REST middleware, the gRPC interceptor reading a proto
method's permission option, QUIC's `handleIf`, MCP's `Perm` field — and
every one of those sits in front of the SAME service methods embedded
calls directly, with nothing in between. A service method like
`AdminService.AddMember` or `AdminService.MintJoinToken` performs no
permission check of its own; authorization is a property of the HANDLER
a remote transport interposes, and embedded has none.

This is intentional and correct for its actual use: an embedding Go
process calling `n.Client()` or `n.Services()` IS its own admission
control, the same way a library call is not expected to re-authenticate
its caller. The rule this implies, and the reason it is written down
here rather than left implicit:

**The desktop webview is the operator's own local UI, not an untrusted
party** -- it is the one surface this specific embedder exposes a human
operator through, on a machine that operator already controls. Ordinary
cluster admin verbs (`addMember`, `removeMember`, `setNodeCompaction`, and
their siblings) may stay wired through it: an operator clicking a button
in their own desktop app is exercising the same authority they would have
from the CLI on the same machine, and refusing those verbs there would not
add security, only friction.

**The rule that stays is narrower, and about what a response CARRIES, not
which verb it names: a secret-bearing output must never be returned
through the webview.** Today that means exactly one thing --
`admin.mintJoinToken`'s minted token, a credential good for admitting a
new node to the whole cluster (`ScopeCross`; see
[clustering/architecture.md](clustering/architecture.md)) -- is never
wired through, no matter how convenient a "mint a join token" button
would be. `sdk/typescript/src/wails.ts`'s `admin.mintJoinToken`
deliberately throws `NotImplementedError` instead of wiring a new
`AdminBinding` method for it, rather than let the interface's own
TypeScript structural typing silently reopen the gap: a token minted
there would appear in the webview's own JavaScript heap, on a surface
whose whole point is rendering content (including, on some platforms, a
loaded remote page or a plugin) that the desktop process does not fully
control -- exactly the condition a write-only credential like this one
exists to never be exposed to.

If a desktop or other local-UI surface genuinely needs to mint a join
token, the fix is a narrower, dedicated binding that keeps the token off
the webview's own JavaScript entirely (written to a file the operator
copies by hand, for instance) -- not a general exception to this rule,
and not an assumption that "the operator's own machine" extends to
whatever that webview happens to be rendering at the time.

---

## Standalone node

A standalone node is engine-direct: writes are applied immediately to an in-memory or on-disk MVCC/KV engine under a `LocalBackend`. No quicraft host is created, no `StartShard` is called, and no Raft WAL or nodehost directory is written.

### Minimal (memory backend)

```go
import (
    qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
    "github.com/jeremyhahn/go-qrdb/sdk/go/node"
)

cfg := qrdbconfig.DefaultConfig()
cfg.Raft.DataDir = ""          // empty → memory backend
cfg.Protocols.REST = false      // disable listeners if not needed
cfg.Protocols.GRPC = false

n, err := node.NewStandaloneNode(ctx, cfg, nil)
if err != nil { ... }
defer n.Close()

client := n.Client()
if err := client.Put(ctx, "key", []byte("value")); err != nil { ... }
```

### On-disk (PebbleDB backend)

```go
cfg := qrdbconfig.DefaultConfig()
cfg.Raft.DataDir = "/var/lib/myapp/data"
cfg.Protocols.REST = true
cfg.Server.AdminPort = 9000

n, err := node.NewStandaloneNode(ctx, cfg, nil)
```

### Injecting hardware crypto options

`serverOpts` is forwarded verbatim to `server.New`. Pass
`server.WithStrategyRegistry`, `server.WithPINBackend`, and
`server.WithFactorRegistry` here so hardware-backed seal strategies and
authentication factors from go-xkms are wired in at composition time.

```go
n, err := node.NewStandaloneNode(ctx, cfg,
    []server.ServerOption{
        server.WithStrategyRegistry(myStrategyRegistry),
        server.WithPINBackend(myTPMPINBackend),
        server.WithFactorRegistry(myFactorRegistry),
    },
    node.WithSealPassphrase(passphrase),
)
```

### Seal barrier (encryption at rest)

Enable the barrier in the config and supply the passphrase via `WithSealPassphrase`:

```go
cfg.Seal.Enabled  = true
cfg.Seal.Strategy = "passphrase"

n, err := node.NewStandaloneNode(ctx, cfg, nil,
    node.WithSealPassphrase([]byte("admin-secret")),
)
```

The barrier is auto-unsealed during construction. The data-plane PebbleDB is opened on top of `pkg/seal/encfs` so all on-disk data is encrypted at rest.

---

## Clustered node

A clustered node creates a `quicraft.Host`, starts the system KV shard with a subsystem-aware MVCC state machine factory (lessor + watch hub + time-series), and wraps the host with `pkg/backend/raft.HostAdapter` to satisfy `service.Host`.

### Single-node cluster (no transport)

A single-entry `members` map automatically selects `quicraft.WithoutTransport()` so no QUIC port is needed:

```go
cfg := qrdbconfig.DefaultConfig()
cfg.Raft.DataDir  = "/var/lib/myapp/raft"
cfg.Raft.ReplicaID = 1
cfg.Protocols.REST = true

n, err := node.NewClusteredNode(ctx, cfg,
    map[uint64]string{1: "127.0.0.1:63001"},
    nil,
)
```

### Multi-node cluster

```go
members := map[uint64]string{
    1: "10.0.0.1:63001",
    2: "10.0.0.2:63001",
    3: "10.0.0.3:63001",
}
cfg.Raft.RaftAddress  = "10.0.0.1:63001"
cfg.Raft.ListenAddress = "0.0.0.0:63001"
cfg.Raft.ReplicaID     = 1
// The Raft identities of the OTHER replicas; this node's own is derived
// from its certificate. node.MemberIdentityFromPEM(peerCertPEM) computes one.
cfg.Raft.MemberIdentities = "2=spki:…,3=spki:…"

n, err := node.NewClusteredNode(ctx, cfg, members, nil,
    node.WithRaftMTLS(&node.RaftMTLSConfig{CACert: caPEM, Cert: certPEM, Key: keyPEM}),
)
```

A node handed its cluster identity with `WithRaftMTLS` and no cluster CA key
of its own (neither `WithClusterCAKeyProvider` nor `WithClusterCAMaterial`)
runs on operator-supplied TLS, exactly as `qrdbd` does with `--tls-ca-file`,
`--tls-cert-file` and `--tls-key-file`: it holds no self-issued cluster CA,
and the CA its identity was issued under is its cluster CA -- the anchor that
decides which peers may forward on a caller's behalf and which certificates
authenticate for any tenant. Verbs that sign with the cluster CA (ceremony
CSR signing, officer registration, join tokens) refuse on such a node, as on a
BYO-TLS `qrdbd` node; give the node a cluster CA key to use them.

#### Injecting the cluster CA key

An embedder that keeps the cluster CA's key in an HSM, a TPM or a cloud KMS
injects it as a `cakey.Provider` -- `cakey.Adapter` wraps any signer -- and
the node signs with it, as `qrdbd` and `pkg/server` do:

```go
ca := cakey.Adapter{SignerFunc: hsmSigner, CertificateFunc: operatorCACert}
n, err := node.NewClusteredNode(ctx, cfg, members, serverOpts,
    node.WithRaftMTLS(&node.RaftMTLSConfig{CACert: caPEM, Cert: certPEM, Key: keyPEM}),
    node.WithClusterCAKeyProvider(ca), // the cluster CA (Key A): never generated, never stored
)
```

With a provider injected the node is no longer BYO-TLS: its cluster CA is the
provider's certificate, CSR signing and officer registration sign with it, and
member-side CSR join (`cluster_join.enabled`, `cluster_join.listen_host`,
`cluster_join.listen_port`) runs exactly as on `qrdbd` -- the node records its
own join identity as admitted, starts the join listener, and serves the
join-token verb from the same store. The per-tenant login CA (Key B) is
injected separately, through `server.WithCertKeyProviderFactory` in
`serverOpts`.

#### Tenant defaults

A tenant with no quotas or entitlements of its own gets the configuration's
`tenant.default_quotas` and `tenant.default_entitlements`, read exactly as
`qrdbd` reads them; `node.WithDefaultTenantEntitlements` overrides the
configured entitlements.

A multi-node Raft transport requires mTLS material (`WithRaftMTLS`) and the
member identities: go-quicraft admits a member by its identity alone, and a
founding node missing one is refused at start. See
[clustering/architecture.md](clustering/architecture.md#member-identities).
Its Raft address cannot use port 0.

### Forwarding the client's calls to a replica

A clustered node holds a replica of only some raft databases. Its embedded
client presents no credential, so for that client's call to a database the
node holds no replica of to be forwarded, configure the identity it forwards
as -- a client certificate and key the cluster CA issued for a user of the
tenant:

```go
caller, err := tls.LoadX509KeyPair(callerCertFile, callerKeyFile)
n, err := node.NewClusteredNode(ctx, cfg, members, serverOpts,
    node.WithRaftMTLS(&node.RaftMTLSConfig{CACert: caPEM, Cert: certPEM, Key: keyPEM}),
    node.WithEmbeddedCaller(caller),
)
```

Every forward authenticates that certificate as the node's QUIC listener
authenticates a client's, and the replica authorizes that identity. The node
must hold the tenant's key: a clustered embedded node takes part in the
cluster's tenant key transport as `qrdbd` does, so enable REST (the key
routes) and, when nodes share a host on distinct admin ports, set
`auth.peer_admin_urls`. Without
`WithEmbeddedCaller` such a call fails with `*placement.NotAReplicaError` and
the replica set; the node never forwards as itself. `serverOpts` must carry
the cluster CA (`server.WithCACertPool`) and the node's TLS identity, and the
replicas must serve QUIC. See
[replica-forwarding/api.md](replica-forwarding/api.md#embedded).

### Pre-bound listeners

To serve REST and gRPC on kernel-assigned ports without a reservation race,
bind the sockets and hand them over in `serverOpts`:

```go
restLn, _ := net.Listen("tcp", "127.0.0.1:0")
h3, _ := net.ListenPacket("udp", restLn.Addr().String())
grpcLn, _ := net.Listen("tcp", "127.0.0.1:0")

n, err := node.NewStandaloneNode(ctx, cfg, []server.ServerOption{
    server.WithRESTListener(restLn),
    server.WithRESTPacketConn(h3), // optional; must share restLn's host and port
    server.WithGRPCListener(grpcLn),
})
```

The node owns the sockets from then on: every server it builds — including the
one an in-process encryption migration rebuilds — serves on them, and
`Node.Close` closes them. See
[api-server/architecture.md](api-server/architecture.md#pre-bound-sockets).

---

## Daemon-only concerns

The following concerns are **not** provided by the builders and remain in `cmd/qrdbd`. They can be passed in as `server.ServerOption` or callbacks if the embedder wants them:

| Concern | How the daemon handles it |
|---|---|
| Leader forwarding | `server.WithLeaderForwarding(registry)` |
| SIGHUP TLS reload | `signal.Notify` goroutine calling `bundle.Reloader.Reload()` |
| Sealed-window listener | `lifecycle.BuildSealServicesOnly` + `awaitUnseal` before `initializeNode` |
| Prometheus gatherer | `server.WithGatherer(reg)` |
| Audit file logger | `server.WithAuditLogger(...)` |
| Auth middleware | `server.WithAuthMiddleware(...)` |

Embedders that need these features pass the corresponding `server.ServerOption` values in the `serverOpts` slice.

---

## Per-Tenant Infrastructure & Enable-Encryption

Both the daemon (`cmd/qrdbd`) and the embedded node (`sdk/go/node`) are sibling
composition roots over the shared `pkg/server` core. A new compositor,
`server.BuildTenantInfra()`, is used by both to wire the per-tenant infrastructure
(tenant host registry, Raft backend factory, encryption migrator), enabling parity
across daemon and embedded paths.

To opt in to per-tenant features on an embedded node, use the
`node.WithTenantIsolation()` builder option:

```go
n, err := node.NewStandaloneNode(ctx, cfg, nil,
    node.WithTenantIsolation(),
)
```

### Seal barrier (encryption at rest)

Enable the barrier in config and supply the passphrase via `WithSealPassphrase`:

```go
cfg.Seal.Enabled  = true
cfg.Seal.Strategy = "passphrase"

n, err := node.NewStandaloneNode(ctx, cfg, nil,
    node.WithSealPassphrase([]byte("admin-secret")),
    node.WithTenantIsolation(),
)
```

The barrier is auto-unsealed during construction. All on-disk data is encrypted
at rest via the storage barrier.

#### The Raft WAL

A sealed clustered node writes its Raft WAL under the same system barrier,
through `database.NewRaftWAL`, the code path qrdbd uses:

| Posture | WAL |
|---|---|
| Seal disabled | written `WithoutEncryption` (plaintext, like every other byte) |
| Single-member cluster, or system protection `none` | go-quicraft's own WAL `WithBarrier`; encrypts once encryption is enabled |
| Multi-member cluster whose system barrier encrypts | the multiplex: shared system-barrier WAL plus per-tenant dedicated WALs for `dedicatedWal` databases |

The WAL directory records its format in `<data-dir>/wal/.qrdb-wal-format`.
Before v0.25.1-alpha the SDK node wrote its WAL `WithoutEncryption` even when
sealed. Such a WAL is **refused, not converted**: unsealing a node over it
fails with `*database.RaftWALFormatError`. Start the node from an empty WAL
directory, and a cluster replica re-syncs from its peers.

### Tenant-scoped enable-encryption (with isolation enabled)

Migrate a tenant's barrier from plaintext or auto-unseal to a keyed strategy:

```go
import qrdbsdk "github.com/jeremyhahn/go-qrdb/sdk/go"

// scope is passed by value (qrdbsdk.BarrierScope = service.BarrierScope);
// opts is qrdbsdk.MigrationOptions (= seal.MigrationOptions).
scope := qrdbsdk.BarrierScope{
    Kind:     qrdbsdk.ScopeTenant,
    TenantID: "acme",
}

err := n.EnableEncryption(ctx, scope, qrdbsdk.MigrationOptions{
    TargetStrategy: qrdbsdk.StrategyPassphrase,
    Mechanism:      qrdbsdk.MechanismSwap,
    Availability:   qrdbsdk.AvailabilityOnline,
    Credentials:    qrdbsdk.Credentials{Secret: []byte("tenant-secret")},
})
```

Both `StrategyNone` (plaintext) and `StrategySystemBarrier` (auto-unseal) are
valid migration sources. User-credential strategies (`passphrase`, `shamir`) are
not eligible sources. The embedded node recycles the tenant's dedicated Raft host
in-process with no data loss. See
[seal/enabling-encryption.md](seal/enabling-encryption.md) for full details.

---

## Exported HostAdapter

`pkg/backend/raft.HostAdapter` wraps `*quicraft.Host` to satisfy `service.Host`. Embedders that build their own quicraft host (e.g. for custom shard topologies) can use it directly:

```go
import raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"

adapter := raftbackend.New(myQuicraftHost)
// adapter satisfies service.Host and can be passed to lifecycle.BuildServices
```

A `BootstrapAdapter` is also exported for the pre-ReadIndex bootstrap phase:

```go
boot := raftbackend.NewBootstrapAdapter(adapter)
bootKVSvc := service.NewKVService(boot, shard.ShardKV)
```

---

## Tenant quota usage tracking

`tenant.TenantServicer` includes three methods for tracking per-tenant resource usage alongside the configured limits:

```go
// Increment current usage (enforces limit; returns QuotaExceededError).
IncrementQuotaUsage(ctx, tenantID, quota.ResourceKeys, 1)

// Decrement current usage (clamped to zero, never errors on underflow).
DecrementQuotaUsage(ctx, tenantID, quota.ResourceKeys, 1)

// Read current usage without side effects.
GetQuotaUsage(ctx, tenantID, quota.ResourceKeys)
```

Usage counters are stored in `TenantInfo.Quotas` (the `Current*` fields on `quota.Quotas`) and persisted with each `UpdateQuotas` / `IncrementQuotaUsage` / `DecrementQuotaUsage` call. Enforcement happens inside `IncrementQuotaUsage`: when `current + delta > max` and `max > 0`, a `*tenant.QuotaExceededError` is returned and the counter is not written.

A `max` of `-1` means unlimited; `0` means use system default (treated as unlimited today).
