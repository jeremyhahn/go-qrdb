# Clustering — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

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

Adding a replica is `cluster.IdentityResolver` over go-quicraft's committed
memberships (`service.ResolveMemberIdentity` for a host): the target shard's
`Membership.Identities` for a replica it already names, else the system shard's
identity for the host at the replica's Raft address, else the operator's
override, else a typed refusal. An override is validated and checked against
any committed identity. A transport-less host reads nothing and commits none.
Every add then goes through go-quicraft's `*WithIdentity` API
(`service.Host.RequestAddNodeWithIdentity`, `RequestAddObserverWithIdentity`);
the identity-less forms are no longer called anywhere. The dedicated-shard
factory builds a founder's `MemberIdentities` for every placement replica the
same way (a joiner keeps whichever anchors resolve).

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

**A node's mTLS identity can be a signer, not key bytes.** `sdk/go/node`'s
`WithRaftMTLS` takes `RaftMTLSConfig` (go-quicraft's `MTLSConfig`): either the
`Cert`/`Key` PEM pair, or `Certificate`, a `*tls.Certificate` whose
`PrivateKey` is any `crypto.Signer` -- a TPM- or HSM-held key that signs on
the device. `Certificate` takes precedence, and `CACert` is required either
way. The one identity serves every consumer:

- the Raft transport presents it (go-quicraft signs handshakes through the
  signer);
- the local member identity is derived from its leaf;
- the cluster key plane's sender presents it, through
  `lifecycle.RaftTenantClusterConfig.Identity`, and its receiver publishes the
  leaf for peers to seal tenant keys to;
- the member-side cluster join reads the node certificate from it.

The key plane also needs the node's HPKE decrypter. A device key that agrees
keys itself (`ecdh.KeyExchanger`) serves as the default decrypter. Otherwise
the composer supplies one with `WithKeyDeliveryDecrypter` (see
`docs/multi-tenancy/engineering.md`); without one the key plane is off, with a
warning. `qrdbd` builds the identity from its TLS bundle's software key.
- The SDK node wraps each refusal in `*node.ConfigError{Field:
  "raft.member_identities"}`, which unwraps to the typed cause.

### Enrollment role — `pkg/clusterenroll`

**SDK surface and device-held keys.** `sdk/go/clusterenroll` re-exports the CSR
join: `StartJoinServer` (taking a `cakey.Provider`), `MintJoinToken`, `JoinCSR`
and `JoinCSRHTTP`, with their option and typed-error types. The joiner builds
the CSR itself, so its key can sit behind a `crypto.Signer` that never exports
it and the issued leaf certifies the device's public key. `Resolve`, `ResolveCA`
and `Join` are the software path: they generate the node key in process and
return PEM, the software default. A composer that enrolls through the CSR join
hands the result to `WithRaftMTLS` as a signer-backed `Certificate`. See
[release/extension-seams.md](../release/extension-seams.md).

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
`PersistMaterial` writes `ca-key.pem` (when there is one), `node-key.pem`,
`node-cert.pem` and then `ca.pem` — each atomically, the sequence not — and
`LoadFromDir` treats `ca-key.pem` as optional, since a node holding only a leaf
legitimately has none. `ca.pem` goes last, and `PersistCA` writes the key
before it too, so a visible `ca.pem` means every key written with it is already
durable: an interrupted write can no longer leave a CA certificate whose key was
lost, which `LoadFromDir` would accept as a CA that cannot sign. Anything that
watches the directory for `ca.pem` may rely on the rest of the set being in
place.

The resume's own completeness check requires **only `ca.pem`**, now that the
cluster CA (Key A) is persisted in the replicated store
(`pkg/clusterca`, `cakey.SoftwareProvider`) rather than in `ca-key.pem`: the
advertiser serves an anchor-only bundle — the CA **certificate**, never the
private key (`StartAnchorAdvertiser`'s own doc comment) — so `ca.pem` is the
one file its resumed listener cannot do without. Requiring `ca-key.pem` here
was itself a standing defect, not a safeguard: `cmd/qrdbd` deletes
`ca-key.pem` from disk once Key A's persistence to the replicated store is
confirmed (`confirmClusterCAPersisted`), which is **every** boot after the
first successful one, so a check that required it made the first node's own
second restart fail closed with `*IncompleteFirstNodeMaterialError`
forever — the opposite of what this function exists to close for every other
first-node restart case. A missing `ca.pem` still refuses with
`*IncompleteFirstNodeMaterialError`, naming the **first node's** directory
and the missing file. The node's own leaf (`node-cert.pem`, `node-key.pem`)
is not served by the advertiser either and is already required by
`LoadFromDir`, which fails closed with `*PartialMaterialError` when any of
its three are missing.

`confirmClusterCAPersisted` reads Key A's certificate back from the system
shard with a linearizable read, right after the barrier unseals. On a
restart that is exactly when a leader elected a moment earlier still refuses
such reads until it commits an entry in its own term
(`quicraft.ErrReadIndexNotReady`). The read is therefore retried, with
bounded backoff for up to 30 seconds, on that error as on a not-leader
answer (`lifecycle.AwaitClusterCACertificate`); every other error fails the
boot at once. It is a startup read (see Invariants): each attempt is bounded on
its own, so a readback that never returns fails the boot with a logged
`*server.StartupReadError` instead of hanging it.

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
ordering exists to prevent. `securefile.WriteFileAtomic` (moved out of this
package to `pkg/util/securefile` and re-exported from `sdk/go`) therefore
fsyncs the directory after the rename and does not report success until
that succeeds. Every caller gets this, so `PersistMaterial` and `PersistCA`
are crash-durable too.

**`WriteFileAtomic` refuses a pre-existing symlink at its target
(`*securefile.SymlinkRefusedError`), checked with `os.Lstat` before the
temporary file is even created — a BREAKING change from the pre-move
implementation, which called `rename(tmpFile, path)` unconditionally.**
`rename(2)` does not dereference its destination: when `path` was already
a symlink, the old code silently replaced the SYMLINK ITSELF with an
ordinary file holding the new material -- it did not write through the
symlink to whatever it pointed at, and the distinction matters for exactly
the case this refusal protects. A **Kubernetes Secret volume mount is a
symlink**: each file `kubectl` shows under the mount point is actually a
symlink into a `..data/<timestamp>` directory the kubelet atomically swaps
on rotation, not a plain file. A deployment that points `--data-dir` (or
any path `PersistMaterial`/`PersistCA`/`clusterenroll`'s key/role writes
land in) AT a Secret-mounted directory now gets `*SymlinkRefusedError` on
every boot that writes cluster material — where the pre-move code would
have silently severed the kubelet's symlink, replacing it with a plain
file the kubelet's next rotation can no longer swap. Mount a Secret for
material qrdb only ever READS (a bootstrap anchor bundle, a BYO-TLS CA);
for a directory qrdb WRITES into (the data directory itself, or any parent
of `ca.pem`/`node-cert.pem`/`node-key.pem`/`ca-key.pem`/an enrollment
key/the role file), use an `emptyDir` or a `PersistentVolumeClaim` instead,
or have an init container `cp` the Secret's contents into one before qrdb
starts.

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
`pkg/keydelivery` declines an X25519 share-holder envelope suite: a deployment that has ASKED
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

### Cluster feature gate — `pkg/cluster/featuregate`

```go
type Feature string
const GuardedWrites Feature = "guarded-writes"
const ClusterShardIDs Feature = "cluster-shard-ids"
func Supported() []Feature
func SupportedNames() []string

const AdvertisementPrefix = "__sys:cluster/features/nodes/"
const ActivationPrefix    = "__sys:cluster/features/activated/"

func New(cfg Config) (*Gate, error)
func (g *Gate) Enabled(shardID uint64, f Feature) bool
func (g *Gate) Supports(raftAddr string, f Feature) bool
func (g *Gate) AdmitReplica(shardID uint64, raftAddr string) error // *UnsupportedReplicaError
func (g *Gate) Activated(f Feature) bool
func (g *Gate) AdvertisedSelf() (Self, error)
func (g *Gate) CheckActivated(ctx context.Context, kv ActivationKV) error
func (g *Gate) Refusal() error
func (g *Gate) Start(ctx context.Context, kv ActivationKV, adverts ...cluster.MembershipStore) error
func (g *Gate) Close() error
func CheckActivated(ctx context.Context, kv ActivationKV) error
func HostMembership(h ShardMembershipReader) MembershipSource
```

- The only emission point of a gated command is `service.guardCommand`. It
  asks a Host implementing `service.FeatureGatedHost` whether
  `GuardedWrites` is enabled for the shard; `raftbackend.HostAdapter`
  implements it from `raftbackend.WithFeatureGate(gate)`. Without a gate, an
  adapter over a host with no transport answers enabled and one over a host
  with a transport answers **not** enabled -- a forgotten gate costs
  atomicity, never divergence. `raftbackend.NewFeatureGate` returns the gate
  for a host, or nil for one with no transport.
- The composer (`qrdbd`, `sdk/go/node`) builds the gate with the host, attaches
  it to every adapter over that host (the service host, the bootstrap host, and
  the IsolationRaft factory through `server.TenantInfraConfig.FeatureGate`), and
  starts it once the services exist: `client.StartFeatureGate(ctx, gate, kv,
  opts...)` checks the activations (`*featuregate.UnsupportedFeatureError`),
  advertises through `cluster.Advertiser` over
  `client.NewMembershipStoreAt(featuregate.AdvertisementPrefix)`, and starts
  the gate. `ctx` bounds the start only; the advertisement and the gate run
  until they are closed. Both composers give each start attempt a deadline
  (`server.StartupReadAttemptTimeout`, through
  `server.WhileSystemShardSettling` in `qrdbd`) and retry: a leader that has
  not yet committed in its term drops a follower's ReadIndex without
  answering it, so the activation read of an attempt with no deadline waits
  forever, and a restarted `qrdbd` never reaches its listeners. The composer passes `cluster.WithAdvertiseSealProbe` with the node
  barrier, so the advertisement proposes nothing while the barrier is sealed
  ([../barrier-seal/engineering.md](../barrier-seal/engineering.md)). `qrdbd` does this synchronously after provisioning, so a refused
  binary never serves; `sdk/go/node` does it in the background once the system
  shard has a leader, because a clustered node is built before its peers. It
  cannot refuse to start after the fact, so under a refusal it never
  advertises, logs the error, and reports itself **degraded**: the gate keeps
  the refusal (`Gate.Refusal`) and `raftbackend.HostAdapter.HealthCheck` turns
  an `ok` verdict into `degraded`, which every transport's health check
  serves. A `qrdbd` node with no gate still runs `featuregate.CheckActivated`
  at startup.
- **Admission** is `service.ReplicaAdmitter` (`AdmitReplica`), implemented by
  `raftbackend.HostAdapter` and `qrdbd`'s raft host adapter from the gate.
  `AdminService.AddMember` (every transport's member add, and dynamic
  membership's reconfigurer) asks it before `RequestAddNode`;
  `addObserverReplica` (scale-up and rebalance) asks it before the placement
  compare-and-swap; the raft host adapter asks again in `AddFollowerNode` and
  `AddObserver`; the placement scheduler drops refused candidates
  (`placement.WithReplicaFilter`, wired in `lifecycle.BuildServices`). A refusal
  is REST 409 and gRPC/QUIC `FailedPrecondition`.
- **Withdrawal.** `qrdbd` closes the advertiser and the gate explicitly in its
  signal and migration branches, before it stops the servers and the host, so
  the lease is revoked while the host can still propose. After a crash the
  record lapses only with its lease (`cluster.DefaultLeaseTTL`).
- **Caching.** `Enabled` reads membership only on a cache miss; the cache is
  invalidated by every advertisement change and by the background refresh, so
  a write does no per-call membership copy. Staleness after a membership change
  is bounded by one refresh and is safe because of admission.
- Adding a feature: add the constant to `supported`, and gate its emission
  point on `Enabled`. Nothing else in the package changes.
- **`cluster-shard-ids`** is behaviour, not a command. Every raft database
  create claims its shard ID from one replicated high-water mark
  (`shard.Claimer`, see [../databases/engineering.md](../databases/engineering.md#cluster-wide-shard-ids)),
  using the KV engine's existing compare-and-set. The claim is gated on the
  feature: on a host that gates features, every claim -- and so every raft
  database create -- is refused with `*featuregate.NotActiveError` (503 /
  `UNAVAILABLE`, retryable) until `cluster-shard-ids` is active cluster-wide,
  so no node claims while another may still allocate per node. Its activation
  also applies the downgrade rule: once every node claims, a binary that
  allocates shard IDs per node, or accepts a caller's shard ID, can neither
  start nor be admitted as a replica. *(Corrected: this said nothing is gated
  on `Enabled` before activation.)*
- Invariants: an undecidable group (membership unreadable, empty) is not
  enabled; an unknown feature is never enabled; activation counts witnesses and
  is recorded with `PutIfAbsent` -- only the node whose claim created the record
  logs "activated", the others log "observed" -- and a failed write is retried
  on the next refresh, never assumed.
- **Adopting applied activations.** `ActivationKV` has two reads: `ListAll`
  (linearizable) and `ListStale` (the local replica's applied state); neither
  may filter reserved keys, and `service.KVServicer` satisfies both for the
  `__sys:` prefix. `Start` and `CheckActivated` use `ListAll`, so the
  downgrade refusal never runs on a lagging replica. The background loop
  first adopts what `ListStale` finds -- on every advertisement event, and at
  the top of every refresh, before the refresh's linearizable reads -- and
  only then reseeds, re-checks linearizably and activates. The linearizable
  reads wait on a leader and can take seconds on a slow disk or across a
  leader change; the local read does not, so a node that is about to lead a
  group already enables what its replica has applied. A local read can lag
  but never invents a record, and a record is permanent, so what it finds is
  activated; it does not set `ActivationsLoaded`, because what it misses may
  be activated too. A failed local read -- a witness applies no command --
  falls back to the linearizable path. An applied record of a feature this
  binary lacks is a refusal (`Gate.Refusal`) like a linearizable one.
- **Joining a new node after activation.** A node is admitted only when it
  has a live advertisement. A member advertises through its own gate, once it
  is running; a joining node cannot do that before it is a member, so
  `pkg/clusterenroll`'s member-side CSR join publishes a pre-membership
  advertisement on its behalf instead. The joiner sends its own replica ID
  and claimed features (`featuregate.SupportedNames()`) in the join request;
  `HandleJoinRequest`, once it has signed and admitted the leaf, calls
  `clusterenroll.FeatureAdvertiser.AdvertiseJoiner` (implemented by
  `pkg/joinadvertise.Advertiser`, which `pkg/server/lifecycle`'s Noise_NK
  listener and `pkg/server`'s REST `/v1/enroll/join` route both wire from the
  node's own `service.LeaseServicer` and `service.MVCCKVServicer`) to grant a
  short lease (`joinadvertise.DefaultTTL`, 15 minutes) and write a
  `cluster.Member` record under `featuregate.AdvertisementPrefix` keyed by
  the claimed replica ID, with the claimed features filtered to the names
  this binary's own `featuregate.Supported()` knows. Once that record exists,
  the operator's `member add` sees a live advertisement and admits the node
  exactly as it would a long-running one. The record is a stopgap, not a
  substitute for the node's own advertisement: it lapses with its lease, and
  once the node is a member and running, its own gate replaces it. A
  `FeatureAdvertiser` failure is logged, not fatal -- the certificate
  `HandleJoinRequest` already issued is still returned, and `member add`
  simply surfaces the same refusal it always would with no advertiser
  configured at all (a state machine type with no lease support, see
  `joinadvertise.New`'s own doc comment). A node that registers through
  **dynamic membership** from a client of the running cluster, with its
  features in the registration, has never had this gap: its registration
  record is exactly the live advertisement `Supports` reads, published
  before the leader ever proposes `AddMember`.

### Node administration — `sdk/go/transport`

```go
type AdminClient interface {
	Health(ctx context.Context) (*HealthResponse, error)
	Status(ctx context.Context) (*StatusResponse, error)
	Peers(ctx context.Context) (*PeersResponse, error)
	AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error
	RemoveMember(ctx context.Context, shardID, replicaID uint64) error
	// PromoteMember promotes a committed observer to a voter, keeping its
	// committed address and identity; nothing restarts.
	PromoteMember(ctx context.Context, shardID, replicaID uint64) error
	RequestSnapshot(ctx context.Context, shardID uint64) error
	Drain(ctx context.Context) error
	Capabilities(ctx context.Context) (*CapabilitiesResponse, error)
	StartShard(ctx context.Context, req *StartShardRequest) error
	StopShard(ctx context.Context, shardID uint64) error
	GetShardState(ctx context.Context, shardID uint64) (*ShardStateResponse, error)
	GetTopology(ctx context.Context) (*TopologyResponse, error)
	// ReplicaStates reports, for every state-machine replica on the
	// answering node (or shardID's alone when non-zero), its applied index
	// and state-machine hash. It is never forwarded to a leader.
	ReplicaStates(ctx context.Context, shardID uint64) (*ReplicaStatesResponse, error)
	// RetireJoinIdentity permanently refuses any future admission for a
	// node identity -- a fresh join-token mint, or a renewal of one whose
	// admission has lapsed -- after it is removed from the cluster or its
	// certificate must be treated as revoked. Unlike MintJoinToken, its
	// response carries no secret, so it is a method here directly rather
	// than a distinct capability interface.
	RetireJoinIdentity(ctx context.Context, nodeID, reason string) error
}
```

Server-side, `PromoteMember` is `service.MemberPromoter`, a distinct
interface from `AdminServicer` (mirroring `ClusterJoinTokenMinter`'s own
reason: every `AdminServicer` test double in this repo keeps compiling
without growing a method most of them have no `Host` to serve). The real
`AdminService` implements it unconditionally, and every transport binding
type-asserts for it. `RetireJoinIdentity` is `service.JoinIdentityRetirer`,
the same distinct-interface pattern, and reads and writes the identical
join-identity ledger `ClusterJoinTokenMinter`'s `MintJoinToken` does --
both configured by the same `WithClusterJoinTokenStore` /
`SetClusterJoinTokenStore` call, so enabling member-side CSR join wires
both at once. See [architecture.md](architecture.md#admission-lifecycle-lapsed-renewal-retirement-csr-identity).

`AddObserverMember` (`qrdb admin member add-observer`) is `service.
ObserverMemberAdder`, a sibling capability interface for the identical
reason: it adds a replica as a non-voting observer through `host.
RequestAddObserverWithIdentity` (go-quicraft's own observer-add primitive)
rather than `RequestAddNodeWithIdentity`, with every other check
(`admitReplica`'s feature-gate admission, `ResolveMemberIdentity`'s
committed-identity resolution) shared verbatim with `AddMember`. It exists
because `PromoteMember`'s success path had no way to be exercised before
it: a plain `AddMember` always produces a voter, so there was never a
cluster-created observer to promote.

`ListPendingJoinRequests`/`ApproveJoinRequest`/`RejectJoinRequest` are
`service.JoinRequestReviewer`, a third capability interface over the SAME
replicated store `ClusterJoinTokenMinter` mints into
(`AdminService.clusterJoinStoreAndDomain`, the one bind both interfaces
read) — reviewing a tokenless CSR queued by `pkg/clusterenroll`'s approval
path (pending_join.go) is an admission decision, not a signing one:
`ApproveJoinRequest` calls `clusterenroll.ApproveJoinRequest`, which only
binds the queue entry to an operator-chosen identity and CompareAndSwaps
the record — it never touches a `cakey.Provider`. The actual
`IssueLeafForKey` call happens later, inside `HandleJoinRequest`, when the
joiner's own poll (`Join`'s retry loop, `JoinApprovalPollInterval`) next
presents the identical CSR and finds it approved.

`ReplicaStates` is `service.ReplicaStateReader`, the same
distinct-interface pattern. `AdminService` implements it over go-quicraft's
`Host.StateMachineHash(shardID)`, which returns the state machine's
`sm.Hasher` value together with its applied index, and lists the local
replicas from `GetNodeHostInfo(SkipLogInfo)`. The host reaches the service
through a capability interface, `service.StateMachineHasher`, that
`*raft.HostAdapter` satisfies by embedding `*quicraft.Host` and asserts at
compile time — the standalone `backend.LocalBackend` does not, which is
what makes the read `ReplicaStateUnsupportedError` in standalone mode. The
service maps go-quicraft's answers one to one:

| `StateMachineHash` answer | Unfiltered read | `shardID` filter |
|---|---|---|
| hash, index, `ok` | listed | returned |
| `ok == false` (no `sm.Hasher`) | `*StateMachineHashUnsupportedError` | the same |
| `quicraft.ErrShardNotFound` (stopped mid-read, or a witness) | left out | `*ReplicaNotHostedError` |
| any other error | `*ReplicaStateError{ShardID, Err}` | the same |

Witnesses are recognised from `ShardInfo.IsWitness` and never asked: they
hold no state machine. The read takes no lock of qrdb's own and adds no
state: it is a pure function of what go-quicraft reports at the moment it
is asked.

**Known limitation, upstream.** `StateMachineHash` loads the applied index
before it takes the state machine's read lock to compute the hash, so an
entry applied between the two pairs an older index with a newer hash. The
pair therefore is not atomic on a shard taking writes. The effect is
one-sided — two replicas can only be made to disagree, never to agree when
their state differs — so the read is documented as a quiet-shard read in
[api.md](api.md#replica-states), and the verification harness reads it only
after its workload has stopped. qrdb does not work around it:
the pair belongs to go-quicraft, which is the one place it can be read
atomically.

### SDK `Cluster()` facade

A thin facade over the already-wired control-plane operations — **it adds no new
wire RPCs**:

```go
cc := client.Cluster()
members, _ := cc.Members(ctx, shardID)        // filtered from Peers()
leader, ok, _ := cc.Leader(ctx, shardID)      // from GetShardState()
_ = cc.AddReplica(ctx, shardID, id, raftAddr, identity) // delegates to AddMember
_ = cc.RemoveReplica(ctx, shardID, id)        // delegates to RemoveMember
_ = cc.PromoteReplica(ctx, shardID, id)       // delegates to PromoteMember
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

- **A host is handed out only when its shard can serve a linearizable read.**
  A leader elected a moment ago refuses every ReadIndex
  (`quicraft.ErrReadIndexNotReady`, logged as
  `batched ReadIndex failed (commitPending)`) until it has committed an entry in
  its own term, so "a leader is known" is not "the shard can be read". Every
  path that opens or reopens a shard and then serves it waits for both, through
  one wait with one budget (15s) and the starvation diagnostics of the leader wait:
  `server.WaitForShardServing`. The paths are the per-tenant volume Host and the
  dedicated per-database raft shards (`pkg/server`, which also covers an
  encryption migration's reopen), the system shard before `qrdbd` starts its
  listeners, the system shard of every `sdk/go/node` clustered node, sealed or
  seal-disabled, with or without tenant isolation (the node's host builder
  waits before it returns, so before `BuildTenantInfra` reads the replicated
  tenant store and before the caller's first request, such as a replica add's
  committed-membership read), and the desktop embedded server. The admin
  `StartShard` operation is deliberately outside the rule: it starts a shard
  whose other members may not exist yet, and its caller decides when to read.
  So is `waitForShardLeaderKnown` (`pkg/service`), the wait the scale path,
  the rebalancer and every admin membership change make: it gates a
  membership change, which is proposed through the log and ordered after the
  new leader's first entry in its term, not a linearizable read -- a known
  leader is the whole of what that proposal needs. It does not cover a read
  made around the change: the committed-membership read that add,
  add-observer (resolving the member's identity) and promote make, and a KV
  read after a leadership move, can still meet the new leader's ReadIndex
  warm-up. Every wait honours the
  caller's ctx and returns its error, unless the wait has already finished: a
  cancellation that lands with the probe that found the shard serving still
  reports success. A clustered SDK boot that fails -- cancelled, or timed out --
  leaves the node sealed and bootable, so the next `Unseal` boots it. A timeout names its phase: `*server.ShardLeaderWaitError` when no
  leader was elected, `*server.ShardReadWaitError` (wraps
  `server.ErrShardNotReadReady`) when a leader was but never served a read --
  each reporting probe errors, a starved poller, or the genuine failure. Do not
  widen the budget to make either go away. `qrdbd`'s earlier best-effort
  ReadIndex warm-up is gone: the wait replaces it.
- **Every startup read is bounded per attempt, and a boot it fails is logged.**
  A boot-time read of the replicated store runs through
  `server.RunStartupRead`: each attempt gets its own context, ending after
  `server.StartupReadAttemptTimeout` (5s) or at the read's startup deadline,
  whichever comes first, and the read uses it. An attempt that does not return
  -- a ReadIndex handed to a leader that died, or one never answered -- is cut
  off and retried, not waited on for the rest of the boot. go-quicraft's typed
  retryable `quicraft.ErrReadIndexNotReady` (matched with `errors.Is`, classed
  Unavailable, returned while a new leader has not committed in its term) is
  retried with backoff until the startup deadline, as is any transient the read
  names itself, such as a not-leader answer; any other error fails the boot at
  once, as the read returned it. When the deadline passes, the boot fails with
  `*server.StartupReadError`, which names the read, its budget, the attempts
  made and how many were cut off, and unwraps to the last attempt's error. It
  is logged at ERROR (`startup read did not succeed before its deadline`) with
  the same fields. If the caller's context ends first, the same error carries
  that context's error as well. The reads are the cluster-CA readback
  (`lifecycle.AwaitClusterCACertificate`, 30s), and the reads made while the
  system shard settles (`server.WhileSystemShardSettling`, 15s): the
  feature-gate start (its activation check and its self-advertisement), the
  boot tenant list, the encryption migrator's entitlement and isolation-mode
  reads, and `qrdbd`'s dynamic-membership start (its registration and first
  membership snapshot). `sdk/go/node` bounds each feature-gate start attempt
  the same way and retries it in the background. `qrdbd`'s member-record
  publisher, which runs in the background after the API servers start,
  bounds each registration the same way and retries until it lands. Settling,
  for these reads, includes a not-leader answer that names no leader, and one
  that names a leader without an address: go-quicraft fails the reads and
  forwarded proposals pending at a leader change with that notice and expects
  the caller to retry. A not-leader answer that names a leader with an address
  is a routing answer and fails the read at once. `qrdbd`'s wait for the
  first admin's replication bounds each lookup the same way under its own
  deadline, and so does first boot's lookup for an existing admin, where a
  lookup cut off reads as "no admin yet" and the idempotent first-admin step
  decides. `server.WaitForShardServing` already bounds each probe (500ms).
  Database provisioning and the first-boot `Init` stay on the boot's context:
  cutting off a write that may still commit would race the retry that follows
  it. A registration does not: it re-puts the node's own lease-bound record,
  so a retry after a cut-off attempt writes the same record again, and an
  orphaned lease expires with its TTL. Nor does the deleted-tenant reconcile:
  it is a startup read (15s) with each attempt bounded, because on the boot's
  context one read that never answered held a restarted node past the
  reconcile's budget. A cut-off pass is safe to retry: every read it makes
  fails closed, and the records that name a retired tenant's directories are
  dropped last, so the retry finds whatever the cut-off pass left.
- **A startup wait says what it waits for.** While a startup read waits --
  inside an attempt or between two -- it logs `startup read still waiting` at
  WARN every `server.StartupWaitReportInterval` (5s), naming the read, the time
  elapsed and the budget, and once an attempt has failed, the attempts made
  and cut off and the last error. A step that stays on the boot's context,
  such as a provisioning write, is reported the same way while it outlasts its
  attempt's bound. The shard
  waits (`server.WaitForShardLeader`, `server.WaitForShardServing`) log
  `startup still waiting` on the same interval with what they wait for, the
  shard, the polls, the elapsed time, the budget and the last probe error, and
  the member-record publisher logs `member record still waiting to publish`.
  A restarted node that has not served `/health` after a few seconds has said
  why in its log.
- **`qrdbd` waits for the system shard's leader for as long as it runs, in
  rounds measured in time, not in a count of polls.** Before its first Raft
  write -- the auth bootstrap and first boot,
  the cluster-CA path, and database provisioning -- `qrdbd` waits until the
  system shard knows its leader (`awaitLeader` in `cmd/qrdbd`, which calls
  `server.WaitForShardLeader`: the leader phase of `WaitForShardServing` on its
  own) in rounds of `leaderWaitBudget`: `leaderWaitElections` (120) election
  timeouts, where the election timeout is `ElectionRTT` × `RTTMillisecond` of
  the host and shard configurations the daemon starts the shard with, after
  go-quicraft fills their defaults. That is 15s for a single-node daemon (the
  transport-less tuning, 5 × 25ms) and 4min at go-quicraft's defaults
  (10 × 200ms), where the wait also covers the peers' own start-up -- no
  election completes before a quorum of replicas runs. A round that runs out
  is logged with its `*server.ShardLeaderWaitError` -- naming the shard, the
  budget and the time spent, with the same starvation verdict as the
  shard-serving wait -- and the next round begins: each node is unsealed by
  its own operator, so a quorum can form long after this node is ready, and a
  node that gave up would exit and leave its peers waiting on it. Only the
  caller's ctx ends the wait, so a signal ends it at once. It used to give up
  after 30 polls on a capped backoff, about 13s of wall time whatever the
  election timeout. However long the wait, the node's ports stay held:
  `cmd/qrdbd` binds the REST, HTTP/3, gRPC, QUIC transport and MCP sockets
  once, before the sealed-window listener, and hands them on to the node's
  own server (`serverSockets` in `cmd/qrdbd/server_sockets.go`), so none is
  free between the unseal and the node serving.
- **Dynamic membership is off by default.** Raft reconfiguration occurs only
  when a `Reconfigurer` is supplied **and the local node is the leader**.
- **A first node serves enrollment on every boot, not only its first.** The
  node that generated the cluster CA owes peers a Noise NK enrollment listener
  for as long as it runs. It is started on every boot, including boots that
  load the CA from cache, and it comes back on the same persisted Noise static
  key so a peer that pinned that key still enrolls — see
  [Enrollment role](#enrollment-role).
- **Every other admitted, unsealed member serves it too, from the moment it
  starts.** `cmd/qrdbd`'s `servesAnchor`/`startMemberAnchorAdvertiser`
  (`bootstrap.go`, `start.go`) register `/v1/ca/bootstrap` and start an
  anchor-only Noise listener for any member `clusterenroll.Resolve`'s
  first-node path did not already start one for, from the same cached
  certificate — see [architecture.md](architecture.md#every-admitted-unsealed-member-serves-the-anchor).
  The node that generated Key A being gone for good no longer strands the
  rest of the cluster's enrollment surface.
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

| Permission | Gates | Scope |
|---|---|---|
| `admin:health` | node health | self |
| `admin:status` | detailed node status | self |
| `admin:peers` | shard peer membership | self |
| `admin:capabilities` | effective capability profile | self |
| `admin:drain` | drain mode | cross |
| `admin:snapshot` | requesting a shard snapshot | cross |
| `admin:add_member` / `admin:remove_member` | Raft reconfiguration — gated apart from reading peers | cross |
| `admin:start_shard` / `admin:stop_shard` / `admin:shard_state` | shard lifecycle and state | cross |
| `admin:topology` | every shard's replicas and addresses | cross |
| `admin:replica_state` | this node's replicas' applied index and state-machine hash — a diagnostic that holds each state machine's read lock while it hashes; held by no built-in role but Owner | cross |
| `admin:node_compaction` | the node keyspace's scheduled-compaction policy (see [../key-value/engineering.md](../key-value/engineering.md) "Node-keyspace compaction") | cross |
| `rebalance:stats` / `rebalance:plan` / `rebalance:report` | read-only | cross |
| `rebalance:execute` | **physically moves shards** — separate grant | cross |

**Cross** is `ScopeCross` (`pkg/permission/cluster_admin_scope.go`): only a
`system`-tenant identity may exercise it. These routes address shards by id
and name no tenant, and the admin service does no tenant check, so as
`ScopeSelf` they admitted any tenant's Admin or SO -- both built-in roles grant
add_member, remove_member, drain and snapshot in every tenant -- who could
remove replicas from the system shard and break quorum for every tenant.
`TestClusterAdminScope` proves the refusal on REST, gRPC, Unix, QUIC, MCP and
the embedded `RBACClient`. `admin:peers` stays self-scoped because the etcd
listener's `Cluster.MemberList` is a tenant-facing call authorized against the
database's owning tenant.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Standalone mode | `requires mode: raft` | **yes** |
| Discovery finds no peers | `BootstrapShard` fails at the quorum check | **yes** |
| Multi-node start with no identity for a member (founder) or no anchor (joiner) | `StartShard` refuses with `*quicraft.InvalidMemberError`; the node does not start | **yes** |
| Malformed `raft.member_identities`, or a local entry that is not the certificate's | `*MemberIdentityParseError` / `*MemberIdentityFormatError` / `*MemberIdentityMismatchError` before the host is built | **yes** |
| Adding a replica on a brand-new node (no committed membership names it) with no identity supplied | `*service.MemberIdentityError` wrapping `*cluster.MemberIdentityUnknownError`, which names the replica and `qrdbd node-identity --cert`; gRPC/MCP `FAILED_PRECONDITION`, QUIC `StatusFailedPrecondition`, REST 412 | **yes** |
| A supplied identity contradicting the one committed for that replica or host | `*service.MemberIdentityError` wrapping `*cluster.MemberIdentityConflictError`; `FAILED_PRECONDITION` / REST 409 | **yes** |
| A malformed supplied identity | `*cluster.MemberIdentityFormatError`; `INVALID_ARGUMENT` / REST 400 | **yes** |
| A shard opened or reopened whose leader is elected but never commits in its term within 15s | `*server.ShardReadWaitError` (wraps `server.ErrShardNotReadReady`); the host is not handed out. `qrdbd` refuses to start with `*SystemShardServingError` | **yes** |
| `qrdbd`'s system shard knows no leader within a round of `leaderWaitBudget` (120 election timeouts) | `*server.ShardLeaderWaitError` (wraps `server.ErrTenantShardNoLeader`), naming the shard, the budget, the time spent and whether the poller was starved, is logged and the next round begins; the wait ends only with the boot context, and a signal returns the context's error as the `wait for leader` step of `*AuthBootstrapError` or `*DatabaseProvisionError` | **yes** |
| A proposal, a linearizable/follower read, or a committed-membership read (`SyncGetShardMembership`, which resolves a new member's identity) on a node that knows no leader for the shard (mid-election, or not yet reached by the new leader) | Held by the Raft host adapter (`throughElection`, `pkg/backend/raft/leader_wait.go`): it polls `GetLeaderID` with backoff (10 ms doubling to 250 ms) and re-runs the request once a leader is known. At 10 s or the caller's deadline it returns `*raft.LeaderWaitError` wrapping the last `*quicraft.NotLeaderError`, which the service layer's translations (`translateProposeError`, `translateReadError`) still find with `errors.As` and map to `*service.NotLeaderError`: `UNAVAILABLE` / REST 503 `NOT_LEADER` (a membership read's wraps into `*cluster.MembershipReadError`, the member-identity `UNAVAILABLE` refusal). A proposal refused naming a leader, and every other error, is returned at once. Previously the refusal reached the client immediately, so a follower's write right after a leader change failed although a leader was moments away | **yes** (retry) |
| A linearizable/follower read in flight when leadership changes hands | go-quicraft fails every request the change strands (`failLeaderChangeRequests`) with a `*quicraft.NotLeaderError` naming the new leader. A `ReadIndex` is otherwise refused only while no leader is known, so for a read that refusal means "stranded", never "served elsewhere": the adapter reissues it (`readThroughLeaderChange`) within the same 10 s bound, and go-quicraft forwards the reissued `ReadIndex` to the new leader. A read has no effect to repeat; a stranded proposal may still commit through the old leader's log and is never reissued. Previously the stranded read reached callers with nowhere to forward it — a MySQL statement inside a transaction failed with 1105 | **yes** (reissue) |
| The committed membership cannot be read (no leader) — the system shard's, or a target shard that exists but is not readable yet | `*cluster.MembershipReadError`; `UNAVAILABLE` / REST 503. Only a target shard this node has not loaded (`quicraft.ErrShardNotFound`) falls through to the host's system-shard identity | **yes** (retry) |
| An add naming a replica ID the shard's committed membership places at a different address | `*cluster.MemberAddressMismatchError`, naming both addresses; `FAILED_PRECONDITION` / REST 409 | **yes** |
| An address the system shard binds to replicas with different identities | `*cluster.MemberIdentityAmbiguousError`, naming the identities; `FAILED_PRECONDITION` / REST 409 | **yes** |
| Admin `StartShard` naming a member no committed membership names (founder) | `*service.MemberIdentityError`, coded as for member add; the shard does not start. A joiner leaves the member out and keeps the anchors it can resolve | **yes** |
| Rebalance move of the system shard | `*service.RebalanceSystemShardMoveError`, logged; the move counts as failed | **yes** |
| Rebalance execute on a node that cannot read the placement records (e.g. mid-election) | `*service.RebalancePlacementReadError`; the whole plan is refused before any move, `UNAVAILABLE` / REST 503. The records are what tell a dedicated tenant shard (tenant-aware MOVE) from a generic one, so an unread index is never treated as an empty one. Previously the read error was swallowed and every dedicated move fell through to the generic voter add: refused on a non-member as a failed move behind a 200, and on a member an add that skipped the observer, key delivery and placement steps | **yes** (retry) |
| Rebalance MOVE submitted (or forwarded) to a node holding no replica of the dedicated group at all | `resolveMoveLeader` (`pkg/service/rebalance_service.go`) picks any OTHER replica, never the MOVE's own source, as the forwarding target — so the chain is bounded at 3 hops, deterministically: guessed member (never the source) → the group's actual current leader, if different → the post-transfer new leader, only if that leader was also the source. `forwardToLeader`'s `maxForwardHops` (`pkg/api/rest/server.go`) is sized for exactly this chain. Previously, guessing the source outright could need a hop `forwardToLeader`'s guard rejected as a false `*rest.LeaderForwardLoopError`, observed in practice | **yes** |
| Rebalance move to a host the shard's committed membership already names (voter, observer or witness) | `*service.RebalanceTargetHostsReplicaError`, logged; the move counts as failed. The new replica ID is past every ID the membership has used, removed ones included | **yes** |
| Rebalance move to a host with no committed identity | `*service.MemberIdentityError`, logged; the move counts as failed | **yes** |
| Founding a dedicated database whose placement names a host the system shard never committed | `*service.MemberIdentityError` from the factory; the shard does not start | **yes** |
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
| Replica states on a standalone node | `*service.ReplicaStateUnsupportedError` (`requires mode: raft`); REST 501, gRPC/MCP `UNIMPLEMENTED` | **yes** |
| Replica states naming a shard this node holds no replica of, or holds only as a witness | `*service.ReplicaNotHostedError`; REST 404, gRPC/MCP `NOT_FOUND` | **yes** |
| Replica states for a shard whose state machine cannot hash itself | `*service.StateMachineHashUnsupportedError`; REST 501, gRPC/MCP `UNIMPLEMENTED` — never a zero hash that could agree with another | **yes** |
| Replica states when the host refuses for any other reason | `*service.ReplicaStateError`, naming the shard and wrapping the host's error | **yes** |
| Missing permission | permission denied | **yes** |
| The cluster activated a feature this binary lacks | `featuregate.UnsupportedFeatureError`; `qrdbd` refuses to start, an embedded clustered node never advertises and reports health `degraded` | **yes** |
| A group holds a replica that does not advertise a feature | the feature is not used in that group (guarded writes are proposed unguarded) | **yes** |
| The feature gate cannot read membership | the feature is not enabled for that group | **yes** |

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
| `bootstrap.noise_port` | `--bootstrap-noise-port` | TCP port of the Noise trust-anchor listener, which every admitted, unsealed member runs when qrdbd manages the cluster CA — never on operator-supplied `--tls-*-file` certificates (the CA-generating node from first boot, every other member once it has joined or restarted from a cached data directory); **zero means the fixed default 63006, not an ephemeral port** |
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
  `reserveNoisePort` in `cmd/qrdbd/noise_port_test.go` for the history.
  The listener is not only the first node's: every node that serves the CA
  anchor (`startMemberAnchorAdvertiser`) opens it too, so any `runStart` test
  that gets past TLS on generated material reaches it, and a test driving
  `qrdbd start` through cobra gets 63006 from the flag default rather than
  from zero. `cmd/qrdbd`'s `TestMain` therefore holds 63006 for the whole run
  (`holdDefaultNoisePort`): a test that forgets the reservation fails on every
  run, in that test, instead of one run in three somewhere else. CI runs
  several copies of the package on one host, and a copy that exits mid-run
  must not take the guard with it, so the hold never gives up on its first
  failed listen: copies share the port through `SO_REUSEPORT`, a foreign
  holder is waited on through a connection to it, and one that refuses that
  connection is polled until it lets go.
  `TestDefaultNoisePortIsHeld` fails if the hold is ever dropped. In
  `pkg/clusterenroll`, the test of the zero-means-63006 rule reads the address
  at the `newNoiseServer` seam and listens on an ephemeral port, so no unit
  test binds 63006 at all.
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
- **Replica states are tested on every transport against one service
  double.** Each binding — REST, gRPC, Unix, QUIC, MCP, the embedded client and
  the CLI — has a test for the answer and one for each typed refusal, and the
  service has a test per row of the mapping table above against a fake
  `StateMachineHasher`. The integration test (`admin_cluster_cli_test.go`,
  per protocol) runs the real `qrdb admin replica-states` against each of three
  nodes and asserts all three replicas of a shard report one hash at one
  applied index.
- **Integration**: the 3-node Raft suites — `isolation_raft_cluster_test.go`,
  `isolation_raft_dedicated_wal_test.go`, `isolation_raft_rebalance_test.go`,
  `isolation_raft_scale_test.go`, `cluster_test.go`, `drain_test.go`,
  `cluster_enable_encryption_test.go` — live in `make integration-test-cluster`.
  **Those tests run in CI** in the `integration-features` job
  (`.github/workflows/ci.yml`), one leg per feature (`clustering`,
  `multi-tenancy`, `barrier-seal`), required by the `ci-gate` job in CI's full
  tier, so cluster behaviour is
  exercised on every push to `main`, every dispatch, and every pull request
  labelled `ci:full`.

## Observability

- `GET /health` is public; `admin status`, `admin peers` and `admin topology`
  are the operational views. `admin replica-states` is the diagnostic one:
  whether this node's replicas agree with their peers' at an equal applied
  index.
- `rebalance report` gives per-node load and names hotspots;
  `ImbalanceScore` is the single number to alert on.
- Membership changes are audited through [../audit/](../audit/).

## Integration Target

Run `make integration-test-clustering` for this feature. The per-transport accounting row is `test/integration/COVERAGE.md` under `clustering`.
