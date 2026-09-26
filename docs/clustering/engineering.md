# Clustering — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

**Raft mode only.** In standalone every capability here refuses with
`requires mode: raft`.

## Exported surface

### Discovery — `pkg/discovery`

```go
type Method interface {
	Discover(ctx context.Context) ([]Peer, error)
	Name() string
	Stop() error
}

type Peer struct {
	NodeID  uint64
	Address string
}
```

Four shipped methods: `static`, `multicast`, `dns`, `token`.

#### Multicast sockets are built by hand, and by platform

`multicast` does not use `net.ListenMulticastUDP`. That helper sets
`SO_REUSEPORT` on Linux, which makes the kernel load-balance arriving datagrams
across the sockets bound to the group instead of delivering each datagram to all
of them — so two nodes on one host would each see half the announcements.
`pkg/discovery` therefore builds the descriptor itself: `SO_REUSEADDR` only,
bound to the group address rather than the wildcard.

Building the descriptor by hand means owning its flags. Close-on-exec is
mandatory — a discovery socket must not survive into a child process — and the
listener is also non-blocking. Where those flags come from is the one thing that
differs by platform, and it is isolated behind `newDatagramSocket`:

| File | Platforms | Mechanism |
|---|---|---|
| `socket.go` | everything except darwin | `SOCK_CLOEXEC` / `SOCK_NONBLOCK` as `socket(2)` type bits, set atomically with the descriptor |
| `socket_darwin.go` | darwin | `socket(2)` takes no such flags; both are applied afterwards with `fcntl`, the socket and its `FD_CLOEXEC` held under `ForkLock` so no concurrent `fork`+`exec` can inherit the descriptor in between |

Nothing above `newDatagramSocket` is platform-aware. Adding a platform means
adding one file, not a branch in `multicast.go`.


### Cluster formation — `pkg/cluster`

```go
type Options struct {
	Method          string
	NodeID          uint64
	RaftAddress     string
	MulticastSecret string
	// ...
}

func BootstrapShard(ctx context.Context, node ..., opts Options, shardID uint64, createFn ..., shardCfg ...) error

// Exported helpers for finer-grained use:
func BuildDiscovery(...) (...)
func ParsePeers(...)   (...)
func IsSingleNode(...) bool

// Raft member identities (see "Member identities" below):
func MemberIdentityFromCertificate(leaf *x509.Certificate) string
func MemberIdentityFromPEM(certPEM []byte) (string, error)
func ValidateMemberIdentity(s string) error
func ParseMemberIdentities(specs []string) (map[uint64]string, error)
func ResolveMemberIdentities(specs []string, selfID uint64, selfCertPEM []byte) (map[uint64]string, error)
```

`BootstrapShard` runs, in order: **discover → validate addresses → add self →
check quorum → build member map → `StartShard`.**

### Member identities

go-quicraft (0.13 and later) admits a member of a shard on a host with a
transport by its node identity alone, and refuses a founding `StartShard` whose
`Config.MemberIdentities` omits a member, or a joining one that names no anchor.
Both composers — `sdk/go/node.buildClusteredRaftConfigs` and `qrdbd`'s
`initializeNode` — build that map with `ResolveMemberIdentities` from
`raft.member_identities` and the local transport certificate, before the host is
built.

- The identity is `"spki:" + hex(sha256(leaf.RawSubjectPublicKeyInfo))`, byte
  for byte what `quicraft.Host.NodeIdentity` reports. go-quicraft does not
  export its derivation, so `MemberIdentityFromCertificate` restates it; the
  SDK and daemon formation tests assert the two agree against a live host, so a
  change on either side fails a test rather than a cluster.
- The local entry is derived, never trusted from configuration: a configured
  local entry that differs is `*MemberIdentityMismatchError`.
- A malformed spec is `*MemberIdentityParseError` (not `id=identity`, replica
  ID 0, or a replica named twice) or `*MemberIdentityFormatError` (not
  `spki:` + 64 lower-case hex digits). Both are raised before any directory is
  created.
- Coverage — every founder named, an anchor for a joiner — is go-quicraft's
  rule and is left to `StartShard`, which returns `*quicraft.InvalidMemberError`.
  Restating it here would be a second copy of the rule to drift.
- The SDK node wraps each refusal in `*node.ConfigError{Field:
  "raft.member_identities"}`, which unwraps to the typed cause.

### Enrollment role — `pkg/clusterenroll`

`clusterenroll.Resolve` / `ResolveCA` decide, on the run that first puts
cluster CA material in the data directory, which of three roles this node
holds, and record it in `{DataDir}/enrollment-role`:

| Role | Meaning | Enrollment listener |
|---|---|---|
| `first-node` | generated the cluster CA; peers enroll against it | started on **every** boot |
| `single-node` | generated its own CA, has no peers | never |
| `joined` | fetched the CA from an existing cluster | never |

**The role is recorded because it cannot be recovered from the material.** A
data directory left by a first node whose listener started and served, and one
left by a first node whose listener failed to start, are identical file for
file — measured 2026-09-03, both
`[ca-key.pem ca.pem enrollment-role node-cert.pem node-key.pem noise-key.hex]`.
So no ordering of generate/persist/start lets a later boot tell them apart;
once the process exits the two leave the same bytes. The role is written
**before** the CA material for the same reason: material present with no role
file then means exactly one thing, a data directory written before this
mechanism existed, rather than a state a fresh install can also reach through a
badly timed crash. That ordering is what makes the refusal below defensible —
if an interrupted first boot could reach the same state, the refusal would be
turning an ordinary crash into a node that will not start.

Reading the role is what makes the cached-material path able to restart the
listener. Before it, the listener was started on exactly one boot in a node's
life — the boot that generated the CA — so **every** later boot of a first
node came up healthy, reported healthy, and served no enrollment at all, with
nothing logging a problem. The failure was visible only to a peer trying to
join, from a different machine.

**A data directory with CA material and no recorded role refuses to boot**, with
a `*MissingRoleError`. It is the state a directory written before the role file
existed presents, and the node in it cannot know whether it owes the cluster an
enrollment listener. Coming up anyway and serving none is the defect itself: a
first node in that state is silently unjoinable for as long as it runs, nothing
on the node reports it, and the only symptom is a peer's join failing on another
machine — possibly weeks later, during an unrelated operation. Refusing puts the
report on the node that has the defect, at the moment it has it.

The refusal covers every such directory, not only a first node's, because the
two are not distinguishable — that is exactly what the rest of this section
measures. A `single-node` or `joined` directory written before the role file is
refused too, and the message tells its operator which value to record.

The error names the remedy rather than the state: the file to write, the three
values it accepts, and that a restart is needed. It also carries the caution
that **holding `ca-key.pem` is not evidence of having generated the CA**, since
an operator following the remedy would otherwise reproduce by hand the
derivation the next paragraph rules out.

The role is not inferred from `SingleNode` either: that is configuration, it can
differ from the value the node was first started with, and inferring a
cluster-wide enrollment decision from it at read time would be a guess wearing
the clothes of a recovery.

Nor is it derived from the material, which was measured rather than assumed.
Releases before the CA key stopped being distributed enrolled a **joining**
node by handing it the CA certificate *and the CA private key* — that is how it
minted its own leaf — and it persisted both: `[ca-key.pem ca.pem
enrollment-role node-cert.pem node-key.pem]`. This release never hands out the
key and refuses joins through enrollment (`*EnrollmentUnavailableError`, before
any dial or write), but data directories written that way still exist. "Holds
the CA key and is not single-node" therefore does not
mean "generated the CA", and acting on it would give every joined node in the
cluster the first node's role, each opening an enrollment listener nobody asked
for. The other candidate signal, `noise-key.hex`, does discriminate but is
written by the advertiser start *after* the CA material, so a node killed
between the two reads as "not a first node" — wrong on the exact case the
recorded role exists to survive.

A role file whose contents are not one of the three values is refused with
`*UnknownRoleError` rather than defaulted, because both defaults are wrong: one
opens an enrollment socket nobody asked for, the other withholds one that was.

**A recorded role is not sufficient on its own; the material has to be able to
back the listener.** "Material present" is four files written one at a time.
`PersistMaterial` writes `ca.pem`, `node-cert.pem`, `node-key.pem` and then
`ca-key.pem` — each atomically, the sequence not — and `LoadFromDir` treats
`ca-key.pem` as optional, since a node holding only a leaf legitimately has
none. A process that stopped after `node-key.pem` therefore leaves a cache that
loads cleanly with no CA private key, and a first node would advertise an
enrollment endpoint that answers and serves an unusable cert-only bundle, with
every joining node failing in `splitCABundle` — in a different process, about a
bundle it did not create.

So the resume refuses with `*IncompleteFirstNodeMaterialError`, naming the
**first node's** directory and the missing files rather than letting the
symptom surface on the joiner. The check is derived from what the advertiser
serves — `CABundle` concatenates `CACertPEM` and `CAKeyPEM` — not from the
write order, so reordering `PersistMaterial` moves nothing.

**Every path that writes CA material records the role, including `Join`.**
`Join` is exported and reachable without going through `Resolve`, and it
persists a joined node's material; the role write lives inside it rather than in
its caller, so a consumer that calls it directly cannot produce a directory that
will refuse to boot on its next start.

`ResolveCA` is not exposed to the same window: `LoadCAFromDir` requires both
`ca.pem` and `ca-key.pem` and reports "no cache" when either is missing, so an
interrupted `PersistCA` sends the next boot down the generate path. The
asymmetry is deliberate and is pinned by a test.

**Atomic visibility and crash durability are different properties, and the
ordering claim needs the second.** `rename` gives a concurrent reader the old
file or the new one and never a mix; it does not put the new *directory entry*
on stable storage. Without an `fsync` of the containing directory, a power loss
can recover a set of files in an order the writer never wrote them in — the CA
material, written later, surviving while the role entry it was supposed to
follow is gone. The node then reads as a directory written before the role
existed and comes up with no enrollment listener, which is exactly what the
ordering exists to prevent. `clustertls.WriteFileAtomic` therefore fsyncs the
directory after the rename and does not report success until that succeeds.
Every caller gets this, so `PersistMaterial` and `PersistCA` are crash-durable
too.

**There is one local-SAN implementation, and the unspecified addresses are not
in it.** `clustertls.LocalSANs` returns `localhost`, the machine hostname, the
two loopbacks, and every non-loopback unicast interface address. `0.0.0.0` and
`::` are excluded: they are *bind* addresses, and a TLS client verifies the name
it **dialed**, never the address the server bound to. `pkg/cluster` refuses to
advertise them to peers for the same reason (`AdvertisableHost`). Listing them
widens the certificate's stated identity and verifies nothing.

The test harness that mints certificates for the integration and conformance
suites — `sdk/go/testutil/qrdbdharness` — calls that same function rather than
keeping its own. It used to keep a copy, and the copy drifted: it retained the
two unspecified addresses after `clustertls` dropped them. Nothing disagreed
with the drift, because the exclusion was documented in prose and asserted by no
test, so an assertion written against the harness copy (`len(ips) >= 4`) was
copied into `cmd/qrdbd`, where it was wrong and reddened every container gate
for three weeks. Both halves of the contract are now pinned by tests in
`pkg/clustertls` — the inclusion half and the exclusion half.

The harness is not a special case in need of wildcards. Every suite that uses
it binds `qrdbd` to `0.0.0.0` and then connects to `127.0.0.1` or `localhost`;
the bind address and the verified name are different values, and only the second
one has to be a SAN.

**A consumer that keeps its own cached-material branch must resume the listener
from it.** `Resolve`'s cached path is not the only one: `qrdbd` keeps a
cached-certificate branch above its delegation to `Resolve`, so that an X.509
parse failure keeps its own typed error, and that branch returns before
`Resolve` is reached. `clusterenroll.ResumeAdvertiser` is exported for exactly
this, and the daemon's branch calls it. A resume placed only inside `Resolve`
is fully exercised by the package's own tests and does nothing at all for the
daemon, which is the only production consumer — so the assertion that pins this
lives in `cmd/qrdbd`, against `resolveTLS`, not against `Resolve`.

### FIPS 140-3 — the Noise NK method is unavailable

Noise_NK_25519_ChaChaPoly_SHA256 mandates X25519 key agreement, which is not an
approved scheme under SP 800-56A Rev. 3. Under `GODEBUG=fips140=only` the Go
Cryptographic Module refuses it outright; under `GODEBUG=fips140=on` the module
permits it and **this package still declines to use it**, for the reason
`pkg/ceremony` declines an Ed25519 external officer: a deployment that has ASKED
for FIPS mode should not be handed non-approved cryptography because the runtime
tolerates it. The predicate is therefore `crypto/fips140.Enabled()`, not
`Enforced()`, and it is read through `clusterenroll.NoiseAvailable()`.

**The ruling has two halves, and what separates them is the operator's stated
intent.**

| Situation | Behaviour |
|---|---|
| **First node** — no advertise-method option exists to choose; the operator asked to be the first node of a multi-node cluster | Noise is **excluded**. `StartAdvertiser` returns a non-nil `Advertiser` whose `NoiseEnabled()` is false, binds nothing, publishes no key and writes no `noise-key.hex`. The node advertises the approved methods. |
| **Joining node, Noise configured alongside an approved method** | Noise is **excluded** from the truststrap `MethodOrder`; SPKI, DANE or direct HTTPS carry the join. `truststrap.AutoFetch` is a fallback chain by construction, so a preference list is exactly what the operator gave. |
| **Joining node, Noise configured as the ONLY method** | **Refused**, at configuration time, with `*NoiseFIPSUnavailableError`. There is nothing to select among: the operator named Noise and this runtime cannot provide it. |

**None of the three is silent.** The two exclusions log at WARN — from
`startNoiseAdvertiser` and again from `logAdvertiserInfo` on the advertise side,
and from `buildAutoConfig` on the join side — naming FIPS, naming the method
that was dropped, and naming the approved methods to configure instead. The
refusal's message leads with FIPS. A bootstrap method downgraded without saying
so is its own defect.

**An `Advertiser` means "this node advertises enrollment", not "a Noise listener
is running."** The two were the same pointer while Noise was the only advertise
mechanism, and they are not any more: `cmd/qrdbd` registers the
`/v1/ca/bootstrap` endpoint — which is what SPKI, DANE and direct HTTPS all
fetch — on exactly the condition that produced a non-`nil` advertiser. Gating
that endpoint on a live Noise server instead would leave a FIPS first node
unreachable by *every* method rather than by one, silently, which is the shape
[Enrollment role](#enrollment-role) exists to keep out of this package. Ask
`Advertiser.NoiseEnabled()` when you need to know about the Noise listener
specifically.

**The bind host stays required in FIPS mode.** `AdvertiseOptions.validate()` is
a configuration-completeness check and does not read the runtime: a
configuration that is incomplete is incomplete in both modes, and making its
validity depend on `GODEBUG` would mean the same file passing on one host and
failing on another.

**The decision is taken at run time.** `go-truststrap` also ships a `fips` build
tag that compiles its X25519 helpers out, and taking that route would mean two
binaries whose enrollment surfaces differ and whose difference is invisible in
the artifact. One binary that reads the runtime it was started in is the smaller
claim, and it is the claim `GODEBUG` itself makes.

**What this does NOT do.** It does not make the Noise transport work under FIPS —
nothing can, short of a different handshake — and it does not decide whether the
shared-CA-key enrollment mode should be served over Noise at all, which is a
separate open question. See
[fips140-mode.md](../seal/fips140-mode.md) for the FIPS register and the
remaining gaps.

### Dynamic membership — `pkg/cluster`

`MembershipManager`, built against two narrow interfaces so it is testable and
embeddable without import cycles:

- `MembershipStore` — lease-backed entries plus a prefix watch.
- `Reconfigurer` — Raft add/remove plus leader lookup, and `IsMember`.

SDK wiring: `client.NewMembershipManager(self, shardID, cfg)`, or the
lower-level `client.NewMembershipStore()` / `client.NewMembershipReconfigurer()`.

### Node administration — `sdk/go/transport`

```go
type AdminClient interface {
	Health(ctx context.Context) (*HealthResponse, error)
	Status(ctx context.Context) (*StatusResponse, error)
	Peers(ctx context.Context) (*PeersResponse, error)
	AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr string) error
	RemoveMember(ctx context.Context, shardID, replicaID uint64) error
	RequestSnapshot(ctx context.Context, shardID uint64) error
	Drain(ctx context.Context) error
	Capabilities(ctx context.Context) (*CapabilitiesResponse, error)
	StartShard(ctx context.Context, req *StartShardRequest) error
	StopShard(ctx context.Context, shardID uint64) error
	GetShardState(ctx context.Context, shardID uint64) (*ShardStateResponse, error)
	GetTopology(ctx context.Context) (*TopologyResponse, error)
}
```

### SDK `Cluster()` facade

A thin facade over the already-wired control-plane operations — **it adds no new
wire RPCs**:

```go
cc := client.Cluster()
members, _ := cc.Members(ctx, shardID)        // filtered from Peers()
leader, ok, _ := cc.Leader(ctx, shardID)      // from GetShardState()
_ = cc.AddReplica(ctx, shardID, id, raftAddr) // delegates to AddMember
_ = cc.RemoveReplica(ctx, shardID, id)        // delegates to RemoveMember
ch, _ := cc.WatchMembership(ctx, shardID)     // polls, emits deltas
// ev.Members, ev.Added, ev.Removed, ev.LeaderChanged
```

`WatchMembership` **polls** at `DefaultMembershipPollInterval` rather than using
a streaming RPC, so it works over every transport including unary-only REST.

### Rebalance — `sdk/go/transport`

```go
type RebalanceClient interface {
	GetShardStats(ctx context.Context, shardID uint64) (*ShardStatsResponse, error)
	CreateRebalancePlan(ctx context.Context, req *RebalancePlanRequest) (*RebalancePlan, error)
	ExecuteRebalance(ctx context.Context, plan *RebalancePlan) (*RebalanceResult, error)
	GetImbalanceReport(ctx context.Context) (*ImbalanceReport, error)
}

type ShardStatsResponse struct {
	ShardID      uint64 `json:"shard_id"`
	KeyCount     uint64 `json:"key_count"`
	ByteCount    uint64 `json:"byte_count"`
	LeaderID     uint64 `json:"leader_id"`
	ReplicaCount int    `json:"replica_count"`
}
type RebalancePlanRequest struct {
	MaxMoves      int     `json:"max_moves"`      // 0 = no limit
	DryRun        bool    `json:"dry_run"`
	TargetBalance float64 `json:"target_balance"` // 0-100; 0 = perfectly balanced
}
type RebalanceMove struct {
	ShardID        uint64 `json:"shard_id"`
	FromNode       string `json:"from_node"`
	ToNode         string `json:"to_node"`
	EstimatedBytes uint64 `json:"estimated_bytes"`
}
type RebalancePlan struct {
	Moves             []RebalanceMove `json:"moves"`
	EstimatedDuration time.Duration   `json:"estimated_duration"`
	CurrentImbalance  float64         `json:"current_imbalance"`
}
type RebalanceResult struct {
	MovesCompleted int           `json:"moves_completed"`
	MovesFailed    int           `json:"moves_failed"`
	Duration       time.Duration `json:"duration"`
	FinalImbalance float64       `json:"final_imbalance"`
}
type NodeLoad struct {
	NodeID      string `json:"node_id"`
	Address     string `json:"address"`
	ShardCount  int    `json:"shard_count"`
	TotalKeys   uint64 `json:"total_keys"`
	TotalBytes  uint64 `json:"total_bytes"`
	LeaderCount int    `json:"leader_count"`
}
type ImbalanceReport struct {
	Nodes          []NodeLoad `json:"nodes"`
	ImbalanceScore float64    `json:"imbalance_score"`
	Hotspots       []string   `json:"hotspots"`
}
```

## Invariants

- **Dynamic membership is off by default.** Raft reconfiguration occurs only
  when a `Reconfigurer` is supplied **and the local node is the leader**.
- **A first node serves enrollment on every boot, not only its first.** The
  node that generated the cluster CA owes peers a Noise NK enrollment listener
  for as long as it runs. It is started on every boot, including boots that
  load the CA from cache, and it comes back on the same persisted Noise static
  key so a peer that pinned that key still enrolls — see
  [Enrollment role](#enrollment-role).
- **In FIPS 140-3 mode a first node still advertises, over the approved methods
  only.** The Noise NK listener is excluded — its X25519 handshake is not
  approved — and the node comes up serving `/v1/ca/bootstrap` for SPKI, DANE and
  direct HTTPS, saying so at WARN. A join configured with Noise as its only
  method is refused at configuration time instead. See
  [FIPS 140-3](#fips-140-3--the-noise-nk-method-is-unavailable).
- **A node whose data directory holds cluster CA material with no recorded
  enrollment role refuses to start.** The role cannot be recovered from the
  material, so the node cannot know whether it owes the cluster a listener, and
  starting anyway means being silently unjoinable with nothing on the node
  reporting it. The error names the file, the three values, and the restart.
- **The quorum guard (`MinQuorum`) refuses to remove a replica** when doing so
  would drop active membership below quorum. A transient liveness blip cannot
  destroy the cluster.
- **Join and leave are idempotent.** The manager consults
  `Reconfigurer.IsMember` and skips `AddMember` for replicas already present
  (the initial bootstrap members) and `RemoveMember` for replicas already gone.
- **One in-flight reconfiguration per replica**, applied by a single serialized
  worker; **latest intent wins**. Reconfiguration retries with capped
  exponential backoff.
- **Reconciliation is snapshot-based.** On startup and after a watch
  re-subscribe the manager reconciles against a full `ListMembers` snapshot, so
  members that joined or left while it was not watching converge in **both**
  directions. This is O(cluster size) and deliberate.
- **One implementation of the local SAN set, and it excludes `0.0.0.0` and
  `::`.** Production code and the `qrdbdharness` test CA both call
  `clustertls.LocalSANs`. A wildcard is a bind address; nothing verifies a
  certificate against one — see [Enrollment role](#enrollment-role).
- **Multicast requires an operator-chosen PSK.** There is no default secret;
  announcements are HMAC-SHA256 authenticated.
- **Member entries live at `__sys:cluster/members/{nodeID}`**, lease-backed and
  refreshed on a keepalive interval. A dead node's keepalive stops, the lease
  expires, the entry is deleted, and a prefix watch surfaces the removal.
- **The membership store shares one lessor and watch hub with the SDK
  `Lease()`/`Watch()` surface** — one source of truth, two consumers.
- **The `Cluster()` facade adds no wire RPCs.** Everything it does is a
  composition of `Peers`, `GetShardState`, `AddMember` and `RemoveMember`.
- **Rebalance `plan` with `DryRun` produces the plan without executing it**;
  `execute` takes a plan as input, so plan and apply are separable.

## Permission model

| Permission | Gates |
|---|---|
| `admin:health` | node health |
| `admin:status` | detailed node status |
| `admin:peers` | shard peer membership |
| `admin:capabilities` | effective capability profile |
| `admin:drain` | drain mode |
| `admin:snapshot` | requesting a shard snapshot |
| `admin:add_member` / `admin:remove_member` | Raft reconfiguration — gated apart from reading peers |
| `admin:start_shard` / `admin:shard_state` | shard lifecycle and state |
| `rebalance:stats` / `rebalance:plan` / `rebalance:report` | read-only |
| `rebalance:execute` | **physically moves shards** — separate grant |

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Standalone mode | `requires mode: raft` | **yes** |
| Discovery finds no peers | `BootstrapShard` fails at the quorum check | **yes** |
| Multi-node start with no identity for a member (founder) or no anchor (joiner) | `StartShard` refuses with `*quicraft.InvalidMemberError`; the node does not start | **yes** |
| Malformed `raft.member_identities`, or a local entry that is not the certificate's | `*MemberIdentityParseError` / `*MemberIdentityFormatError` / `*MemberIdentityMismatchError` before the host is built | **yes** |
| `AddMember`, rebalance or placement adding a replica on a host with a transport | refused by go-quicraft with `ErrMemberIdentityRequired`: these paths carry no identity yet | **yes** |
| Founding a dedicated (IsolationRaft) database with more than one initial replica on a host with a transport | refused by go-quicraft with `*quicraft.InvalidMemberError`: the factory can state only the local replica's identity | **yes** |
| Multicast configured without a secret | method does not run | **yes** |
| `RemoveMember` would drop below `MinQuorum` | refused by the quorum guard | **yes** |
| Any bootstrap join (bootstrap flags set), this release | `*EnrollmentUnavailableError` before the first dial; nothing is written to the data directory. The FIPS rows below apply to `FetchAnchor`, the one fetch that still dials. | **yes** |
| `/v1/ca/bootstrap` or the Noise advertiser handed key material | filtered to `CERTIFICATE` blocks (`clustertls.CertificatesOnly`, `ValidateAnchorBundle`); the CA private key is never served | **yes** |
| FIPS 140-3 mode, anchor fetch configured with Noise as its only method | `*NoiseFIPSUnavailableError` before the first dial; nothing is written to the data directory | **yes** |
| FIPS 140-3 mode, anchor fetch configured with Noise plus an approved method | Noise dropped from `MethodOrder` with a WARN naming FIPS; the approved methods carry the fetch | n/a |
| FIPS 140-3 mode, first node | advertises the approved methods with a WARN naming FIPS; no Noise listener, no `noise-key.hex` | n/a |
| `AddMember` for an existing replica | skipped (idempotent) | n/a |
| `RemoveMember` for an absent replica | skipped (idempotent) | n/a |
| Reconfiguration transient failure | retried with capped exponential backoff | — |
| Unknown shard on state/snapshot | not-found | yes |
| Missing permission | permission denied | **yes** |

## Configuration

Discovery lives under the `discovery` block — see
[../configuration/](../configuration/).

| Setting | Flag | Meaning |
|---|---|---|
| `discovery.dynamic_membership` | `--dynamic-membership` | enable the membership manager |
| `discovery.min_peers` | `--cluster-min-peers` | auto-removal quorum floor (`MinQuorum`) |
| `raft.peers` | — | `id=address` specs for the `static` method |
| `raft.member_identities` | `--member-identities` | `id=spki:<hex>` identities of the other replicas; required for a multi-node cluster |
| DNS SRV target | — | `{service}.{proto}.{domain}`, default `_raft._udp` |
| `bootstrap.noise_port` | `--bootstrap-noise-port` | TCP port of the first node's enrollment listener; **zero means the fixed default 63006, not an ephemeral port** |
| `bootstrap.noise_host` | `--bootstrap-noise-host` | bind host for that listener; falls back to `server.host` so a node confined to one interface does not expose enrollment on all of them |

Both `bootstrap.noise_*` settings are accepted and validated in FIPS 140-3 mode
and neither opens a listener there — see
[FIPS 140-3](#fips-140-3--the-noise-nk-method-is-unavailable).

## Test strategy

- **Unit**: each discovery method, `Manager` chaining and retry,
  `BootstrapShard`'s six steps including the quorum refusal, and
  `MembershipManager` against the two narrow interfaces (that is what they are
  for).
- **Formation with member identities is a unit test, not only an integration
  one.** `TestNewClusteredNode_ThreeReplicasFormAndReplicate` builds three
  replicas in one process through `NewClusteredNode`, each with its own leaf
  from one test CA, and asserts they agree on a leader and replicate a write;
  `TestInitializeNode_MemberIdentitiesFormTheShard` does the daemon's half. Both
  fail if the resolved identities are not placed on the shard config.
- **The quorum guard and the idempotence checks are the important tests** —
  they are what stops automatic reconfiguration destroying a cluster.
- **No `time.Sleep`**: lease expiry and the keepalive are driven through the
  clock seam.
- **A test that opens the enrollment listener reserves its own Noise port, at
  the host the listener will bind.** `AdvertiseOptions.NoisePort` reads zero as
  *the default 63006*, not as the `":0"` a Go reader expects, so the natural
  spelling of "give me any free port" is in fact the spelling of "give me the
  one port every other copy of this package is also asking for". CI runs
  pull-request pipelines on five runners sharing one host, so concurrent copies
  are the normal case; a fixed port there is a scheduled failure rather than a
  race. The reservation probes at `resolveNoiseHost(f)` and not at a hardcoded
  loopback, because a port proven free at one address says nothing about the
  same number at another. This has reddened CI three times — see
  `reserveNoisePort` in `cmd/qrdbd/enrollment_resume_test.go` for the history.
- **The FIPS ruling is asserted in BOTH runtimes, and nothing is skipped.**
  `clusterenroll.noiseAvailable` is an overridable variable, as `readFileFn`
  already is, so a test can assert the FIPS ruling from an ordinary runtime.
  Only `false` is safe to force everywhere: forcing `true` under
  `fips140=only` tells the package Noise may be used while the module still
  refuses the X25519 underneath it. That is why method selection lives in its
  own `buildAutoConfig`, which performs no cryptography — it is where the
  "a non-FIPS node still selects Noise" regression guard can run under
  `fips140=only` as well as under `fips140=off`.
- **Where a live Noise listener is genuinely unreachable, the test asserts the
  contract that REPLACES it rather than skipping.** A fault planted in
  `noise-key.hex` or in the listen port cannot fire when neither is touched, so
  the assertion becomes "the fixture was not reached, and the advertiser came up
  without Noise" — which is strictly stronger than a skip, because a regression
  that started reading the key file in FIPS mode fails it. `assertAdvertises`
  and `assertNoiseFaultUnreachable` in `pkg/clusterenroll`, and
  `assertAdvertisesWithoutNoise` in `cmd/qrdbd`, express this once each.
- **Integration**: the 3-node Raft suites — `isolation_raft_cluster_test.go`,
  `isolation_raft_dedicated_wal_test.go`, `isolation_raft_rebalance_test.go`,
  `isolation_raft_scale_test.go`, `cluster_test.go`, `drain_test.go`,
  `cluster_enable_encryption_test.go` — live in `make integration-test-cluster`.
  **That stage is not currently wired into CI**, so cluster behaviour is not
  exercised by CI today. This is a known and tracked gap.

## Observability

- `GET /health` is public; `admin status`, `admin peers` and `admin topology`
  are the operational views.
- `rebalance report` gives per-node load and names hotspots;
  `ImbalanceScore` is the single number to alert on.
- Membership changes are audited through [../audit/](../audit/).
