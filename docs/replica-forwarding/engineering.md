# Replica Forwarding — Engineering

> **Status: SHIPPED — REST, gRPC, Unix socket, QUIC, MCP and embedded.** ·
> Scope: go-qrdb · Last verified: 2026-10-05
>
> Rulings 6 (member records on every raft node), 7 (the leader cache) and 8
> (the embedded caller) are in [architecture.md](architecture.md#rulings).

## Exported surface

### `pkg/placement`

```go
type NotAReplicaError struct {
	TenantID string
	Database string
	ShardID  uint64
	NodeID   string
	Replicas []Replica
	// LeaderReplicaID is the placement's bootstrap leader -- a hint, not a
	// fact: Raft may have elected another replica since. Zero when unknown.
	LeaderReplicaID uint64
}
```

### `pkg/replicaforward`

```go
// Locator resolves targets from the refusal, the member records and the
// leader cache, and runs the bounded retry.
func NewLocator(members MemberSource, cache *LeaderCache, selfRaftAddr string, logger *slog.Logger) *Locator
func (l *Locator) Forward(ctx context.Context, refusal *placement.NotAReplicaError, proto cluster.APIProtocol, send SendFunc) (Target, error)
func (l *Locator) Cache() *LeaderCache

// SendFunc forwards to one target over one transport and classifies the
// answer; an error is a failure after the request may have reached t.
type SendFunc func(ctx context.Context, t Target) (Result, error)
type Target struct{ ReplicaID uint64; RaftAddr, Endpoint string }
type Result struct{ Outcome Outcome; LeaderRaftAddr string }
type Outcome uint8 // Served, NotLeader, NotAReplica, Unreachable

// LeaderCache: shard ID -> the replica the last forward reached and its member
// endpoints. Reads are lock-free (a copy-on-write map behind an
// atomic.Pointer); writes serialize.
func NewLeaderCache() *LeaderCache
func (c *LeaderCache) Get(shardID uint64) (CachedLeader, bool)
func (c *LeaderCache) Put(shardID uint64, leader CachedLeader)
func (c *LeaderCache) Evict(shardID uint64)
func (c *LeaderCache) EvictDatabase(tenantID, database string) // a placement event names the database
func (c *LeaderCache) EvictMember(raftAddr string)              // every entry pointing at the member
func (c *LeaderCache) Clear()

// RunInvalidation keeps a cache honest from the placement and member feeds
// until ctx ends; Start builds a Locator with it running.
func RunInvalidation(ctx context.Context, cache *LeaderCache, placements PlacementWatcher, members MemberWatcher, logger *slog.Logger)
func Start(ctx context.Context, members Members, placements PlacementWatcher, selfRaftAddr string, logger *slog.Logger) *Locator

// Config is one listener's forwarding: the Locator, the pool a target's
// certificate is verified against, and the bound on one forward.
type Config struct {
	Locator *Locator
	RootCAs *x509.CertPool
	Timeout time.Duration // zero is DefaultTimeout (10s)
}

// The structured refusal, carried beside the message on every transport.
const ErrorDomain = "qrdb.v1"
const ReasonNotAReplica, ReasonNotLeader = "NOT_A_REPLICA", "NOT_LEADER"
func Refusal(err error) (reason string, md map[string]string, ok bool)
func NotAReplicaMetadata(e *placement.NotAReplicaError) map[string]string
func ParseNotAReplica(md map[string]string) (*placement.NotAReplicaError, error)
func NotLeaderMetadata(leaderRaftAddr string) map[string]string
func LeaderRaftAddr(md map[string]string) string

const MetadataForwardedTo = "x-qrdb-forwarded-to" // on a relayed answer
const MetadataServedBy = "x-qrdb-served-by"       // gRPC: the target's own answer
```

### `pkg/auth/forward`

The assertion's `Method`/`Path`/`BodySHA256` fields keep their names and
serialization. A non-HTTP binding stores `"<protocol>:<method>"` in `Method`
and an empty `Path`, so a REST assertion is byte-identical to one made before
bindings were generalized.

```go
type Binding struct{ Method, Path, BodySHA256 string }
func HTTPBinding(method, requestURI string, body []byte) Binding
func RPCBinding(protocol, method string, payload []byte) Binding
func VerifyBinding(assertionB64, sigB64 string, peer *x509.Certificate, trustDomain string, now time.Time, b Binding) (*CallerAssertion, error)

// SignCaller signs that caller made the bound request, as the node nodeID
// holding key; a holder caller's assertion carries its profile. No caller or
// no node identity is *ForwardUnsignedError.
func SignCaller(caller *auth.Identity, b Binding, key crypto.Signer, nodeID string, now time.Time) (assertionB64, sigB64 string, err error)

// NodeIdentity is the signing key and SPIFFE node id of a listener's TLS
// identity certificate.
func NodeIdentity(cfg *tls.Config) (crypto.Signer, string)

const MetadataAssertion, MetadataSignature = "x-qrdb-on-behalf-assertion", "x-qrdb-on-behalf-sig"
```

### `pkg/auth/middleware`

```go
func (m *AuthMiddleware) AuthenticateHTTP(ctx context.Context, w http.ResponseWriter, r *http.Request) (context.Context, *auth.Identity, error) // MCP
func (m *AuthMiddleware) AuthenticateQUICOnBehalf(ctx context.Context, method string, payload []byte, assertion, sig string) (context.Context, error)
func GRPCOnBehalfAssertion(ctx context.Context) (assertion, sig string, ok bool)
func IsOnBehalf(identity *auth.Identity) bool
```

gRPC (and the Unix socket) verify in the unary auth interceptor, binding the
full method and the request message serialized deterministically.

### Transports

| Option | Package |
|---|---|
| `WithReplicaForwarding(replicaforward.Config)` | `pkg/api/rest`, `pkg/api/grpc`, `pkg/api/quic`, `pkg/api/mcp`, `pkg/api/unix` |
| `NewReplicaForwarder(cfg, nodeTLS) *ReplicaForwarder` | `pkg/api/grpc` (the Unix socket installs it) |
| `server.WithReplicaForwarding(l *replicaforward.Locator, timeout)` | `pkg/server`: every listener, verifying targets against `WithCACertPool` |
| `wire.MethodOnBehalf`, `wire.OnBehalf`, `wire.ErrorPayload.Info` | `pkg/api/quic/wire` |
| `wire.ErrorPayload.Reason`, `.Metadata` | `pkg/api/mcp/wire` |
| `NewFrameForwarder(cfg, nodeTLS) *FrameForwarder`, `(*FrameForwarder).Forward(ctx, caller, method, database, payload, refusal)` | `pkg/api/quic`: the QUIC listener's forwarder, and the embedded client's |

`cmd/qrdbd` and `sdk/go/node` compose forwarding on every raft-mode node with
a placement table: `replicaforward.Start` over the node's membership store and
placement table, handed to `server.WithReplicaForwarding`.

### Embedded

```go
// sdk/go/node
func WithEmbeddedCaller(cert tls.Certificate) NodeOption

// sdk/go/transport/embedded
type ReplicaForwarder interface {
	Forward(ctx context.Context, caller *auth.Identity, method, database string, payload []byte,
		refusal *placement.NotAReplicaError) (status uint16, body []byte, forwarded bool)
}
func WithReplicaForwarding(forwarder ReplicaForwarder, caller auth.Authenticator) Option

// sdk/go/transport/quic
type FrameSender func(ctx context.Context, method, database string, payload []byte) (status uint16, body []byte, err error)
func NewFrameTransport(database string, send FrameSender) *Transport

// pkg/server
func (s *Server) ReplicaForwarder() (*quic.FrameForwarder, error)
func (s *Server) AuthenticateCertificate(ctx context.Context, chain []*x509.Certificate) (*auth.Identity, error)
type EmbeddedForwardingStoppedError struct{}
type CallerCertificateError struct{ Reason string; Err error }
type CallerAuthUnavailableError struct{}
```

`WithDatabase` on an embedded transport with forwarding returns a
`forwardingScope`: the scoped transport, embedded, with every unary method of
the data-plane client interfaces overridden by a delegate that makes the call
on the node and, when the node refused it with `*placement.NotAReplicaError`,
makes it again on a `quic.NewFrameTransport` whose sender is the node's
`FrameForwarder`, for the identity the configured caller authenticates. The
delegates are generated (`replica_forward_methods.go`, by `internal/scopegen
-forward-o`; `make check-generated` fails on drift), so a client interface
that grows a method is forwarded once regenerated, and a stream -- a reader,
a channel or a callback in its signature -- is never overridden and so is
refused.

`sdk/go/node` installs the option on a clustered node with forwarding on and
a caller configured: the forwarder is the current server's
`ReplicaForwarder()`, built on first use from the QUIC TLS identity and the
forwarding configuration and closed with the server; the caller authenticates
the configured chain through the current server's `AuthenticateCertificate`
on every forward.

### Cache invalidation hooks

| Event | Hook | Effect |
|---|---|---|
| a forward served | `Locator.Forward` | `Put` the target, with its member endpoints |
| a target answered `NotLeader`, `NotAReplica`, was unreachable, or failed after the send | `Locator.Forward` | `Evict(shard)`; the retry that serves is `Put` |
| a placement written or deleted | `RunInvalidation`, watching `__sys:placement/` on every node | `EvictDatabase` |
| a member record changed or expired | `RunInvalidation`, watching `cluster.MembershipPrefix` on every node | `EvictMember(raft address)` |

Both watches re-subscribe 500 ms after a feed ends. While a feed is down the
cache is cleared rather than trusted. The placement table's feed is
`lifecycle.Node.PlacementChanges`, a placement store over the system KV
whose `Watch` reads the node's watch service. It is a feed only: every write
goes through `Node.PlacementStore`, which keeps no feed, so the tenant shard
reconciler converges on its poll exactly as before. (Giving the reconciler
the feed made it react inside a rebalance MOVE's own steps, and the MOVE
failed. Its watch path stays unused until that interplay is designed.)

## Invariants

1. **At most two targets answer, and nothing is forwarded twice.** An ingress
   request carrying an assertion is never forwarded (the hop marker). A
   target that answers `NotLeader` or `NotAReplica` spends the one retry; an
   unreachable one (the request never left this node) is skipped, each
   replica at most once.
2. **Authorization twice, as the caller both times.** Ingress authorizes
   before resolving services; the target authorizes the asserted caller with
   roles it resolves itself. A request with no authenticated caller is not
   forwarded: a node identity alone authorizes nothing.
3. **The answer is the target's.** Status, body and metadata are relayed
   unchanged, except hop-by-hop headers and the caller's credentials, which a
   forward does not carry. The only thing added is the replica that served it
   (`x-qrdb-forwarded-to`), where the transport has metadata.
4. **Fail to the refusal, never to the local catalog.** Every failure to find
   a target ends in the ingress node's own `NotAReplicaError`. Nothing falls
   back to serving locally.
5. **Streams refuse.** gRPC and Unix streaming methods have no forwarding
   interceptor; REST's `GET /watch` is excluded by route; QUIC's
   `watch.Watch`/`watch.WatchPrefix` are dispatched before the forwarding
   point. MCP has no streams.
6. **Idempotence is the caller's.** A forward that failed after the request
   may have reached the target -- a reset mid-answer, a timeout -- returns
   `*replicaforward.SendError` (or, on gRPC, the transport's status) and is
   never retried. Unreachable means provably unsent: gRPC's connection never
   became ready, REST's and MCP's request headers were never written, QUIC's
   stream never opened.
7. **Forwarding needs a CA pool.** A target's listener certificate is
   verified against `server.WithCACertPool`; a server without one forwards
   nothing.

## Error semantics

| Error | When | Class |
|---|---|---|
| `*placement.NotAReplicaError` | no target served: none reachable, every one refused, forwarding off, no caller, or a stream | `Unavailable` (503), unchanged |
| `*replicaforward.NoTargetServedError` | the Locator found no target that served | internal to the forwarding layer, which answers the refusal |
| `*replicaforward.MemberLookupError` | the member records could not be read | as above |
| `*replicaforward.SendError` | the forward failed after it may have reached a target | `Unavailable` (`DeadlineExceeded` when the caller's deadline passed) |
| `*forward.ForwardUnsignedError` | no authenticated caller, or no node signing identity | logged; the caller gets the refusal |
| `*replicaforward.RefusalDecodeError` | refusal metadata that does not decode | the refusal is answered as is |
| the target's own error | anything the target answers | relayed verbatim |

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `replica_forwarding.enabled` | `true` | forward on a raft-mode node with a placement table; `false` refuses with the replica set |
| `replica_forwarding.timeout` | `10s` | bound on one forward, connection included; a connection gets half of it, so an unanswered handshake leaves time for the retry |

There is no setting for the hop limit or the retry count. Both are part of
the contract.

## Persistence

The leader cache is in memory, per node, and holds addresses only. Nothing
about it is persisted or replicated. The feature reads the placement table
and the member records (`__sys:cluster/members/{node}`), both replicated;
every raft node publishes its member record under a lease (ruling 6).

## Test strategy

**Unit:**

- `pkg/replicaforward`: target ordering, skipping self and non-advertising
  members, the retry choice for each answer, unreachable replicas not
  spending the retry, at most two answers, the refusal codec, the metrics.
- `pkg/auth/forward`: binding round trips per protocol, every binding field
  tamper-checked, REST assertions byte-identical, `SignCaller`,
  `NodeIdentity`.
- `pkg/auth/middleware`: on-behalf verification for gRPC, QUIC and the HTTP
  path MCP uses.
- `sdk/go/transport/embedded`: a refused write is sent as the request frame
  a QUIC client sends, for the configured caller; the replica's answer and its
  error status come back; no caller, a caller that does not authenticate, one
  of another tenant, no replica serving, another refusal and a served call
  are all the node's own result; re-scoping keeps forwarding.
  `internal/scopegen`: the committed delegates are current, streams are not
  forwarded, every forwardable method exists on the QUIC transport.
- `sdk/go/transport/quic`: a frame transport hands over the exact frame and
  maps the answer; a sender error is the call's; a watch is refused.
- `pkg/server`: `AuthenticateCertificate` through the mTLS authenticator and
  through a composer's authenticator reading the verified chain; another CA's
  certificate, no chain, no CA pool and no chain of authenticators refused.
  `ReplicaForwarder` built once, refused after stop.
- `sdk/go/node`: `WithEmbeddedCaller` refuses an empty, unparseable, keyless
  or mismatched certificate at construction; the option is installed only on
  a clustered node with forwarding on and a caller.
- Each transport's forwarding layer, against fake or real target listeners:
  forward and relay as the caller, the hop marker, retry on `NotLeader`,
  unreachable to the next replica, no retry after a send, the target's error
  relayed, every replica refusing, no caller, streams refused. QUIC also: a
  dead target's cached connection closes within a keep-alive.

**Cache (unit, `pkg/replicaforward`):** a hit skips the member lookup; a
`NotLeader` updates the entry; a transport error evicts it; a placement event
evicts the database; a member event evicts the entries pointing at it; a feed
outage clears the cache. The placement table's feed is tested in
`pkg/server/lifecycle` over a real watch hub.

**Integration (`test/integration/replica_forwarding_*_test.go`), one command
set over every network transport -- REST, gRPC, QUIC, MCP and the Unix socket:**

- three nodes, a single-replica database, so two nodes hold no replica:
  - writes and reads through each non-replica reach the replica, as the
    caller, and the answer names it;
  - a caller without the permission, or from another tenant, is refused;
  - a watch stream is refused with the replica set (MCP's bounded
    `qrdb_watch` is forwarded);
  - with the replica down, the non-replica answers the refusal naming it,
    never a local answer;
- four nodes, a three-replica database: the leader is killed, and the next
  request forwarded through the non-replica is served by a surviving replica
  and the new leader holds it.

The same suite on a BYO-TLS cluster lands with the Key B lane (decision D2).

The embedded multi-node case -- an embedded node holding no replica forwards
as its configured caller, a caller without the permission is refused by the
replica, and a node with no caller is refused with `NotAReplicaError` -- needs
an embedded node that holds a raft tenant's key without having created it.
An embedded clustered node does not yet compose the tenant key transport, the
follower admission and the rebalance dependencies `qrdbd` composes, so that
case lands with them; until then the embedded path is proven by the unit
tests above.

## Observability

| Log event | Level |
|---|---|
| `replicaforward: forwarded` (`tenant`, `database`, `protocol`, `target`, `attempt`) | DEBUG |
| `replicaforward: no target` (`tenant`, `database`, `protocol`) | WARN |
| `replicaforward: not forwarded` (no caller or no node identity) | WARN |
| `replicaforward: embedded call not forwarded: …` (the configured caller did not authenticate, or belongs to another tenant; the node's forwarder could not be built) | WARN |
| `replicaforward: invalidation feed down; leader cache cleared` | WARN |
| `replica forwarding off: no CA pool …` (once per server) | WARN |

| Metric | Meaning |
|---|---|
| `qrdb_replica_forward_total{protocol,outcome}` | outcome is `ok`, `retried_ok`, `refused` or `error` |
| `qrdb_replica_forward_duration_seconds{protocol}` | time per forward |
