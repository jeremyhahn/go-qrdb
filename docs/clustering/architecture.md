# Clustering — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

Covers peer discovery, cluster formation, membership, node administration and
shard rebalancing. **Raft mode only** — see
[../run-modes/](../run-modes/architecture.md).

## Problem

Turning a set of processes into a Raft cluster needs three things a consensus
library does not provide: finding the peers, deciding when there are enough of
them to start, and keeping the membership correct as nodes come and go. Leaving
those to each deployment means each one invents its own, subtly wrong.

## Shape chosen

Discovery, bootstrap orchestration and membership live **here**, above the
consensus primitives.

## Ownership

| Concern                       | Home                                  |
|-------------------------------|---------------------------------------|
| Raft primitives (`StartShard`, `GetLeaderID`, replica add/remove) | go-quicraft |
| Discovery engine (`pkg/discovery`) + bootstrap orchestration (`pkg/cluster`) | **go-qrdb** |
| SDK cluster client (`Cluster()`) | **go-qrdb** SDK |
| Service discovery / registry  | go-quicbaas (on go-qrdb lease+watch+KV)|

The discovery engine and bootstrap orchestration live in go-qrdb, the layer
that actually owns the cluster. go-quicraft is reduced to Raft primitives only.

That split is the central decision: go-quicraft is reduced to Raft primitives
(`StartShard`, `GetLeaderID`, replica add/remove) and go-qrdb owns the layer
that actually knows what a cluster is.

**Cluster discovery is not service discovery**, and not trust bootstrap.
Registering and resolving *application* service instances is built by downstream
consumers (go-quicbaas) on this store's lease + watch + KV primitives. Noise /
SPKI / DANE CA establishment is [../trust-bootstrap/](../trust-bootstrap/).

## Discovery engine

A discovery *method* satisfies a small interface:

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

Four methods ship:

| Method      | Mechanism                                                                |
|-------------|--------------------------------------------------------------------------|
| `static`    | Fixed member list from `raft.peers` (`id=address` specs).                |
| `multicast` | HMAC-SHA256-authenticated UDP multicast announcements (opt-in; requires an operator-chosen PSK of at least `discovery.MinSharedSecretLen` (16) bytes — there is no default secret). |
| `dns`       | DNS SRV lookup at `{service}.{proto}.{domain}` (default `_raft._udp`).   |
| `token`     | etcd-style registration with a shared-token HTTP discovery service.      |

Kubernetes deployments use `dns` against a headless Service — no Kubernetes-
specific method is needed.

The `Manager` chains methods with retry, returning the first non-empty result.

## Bootstrap orchestration (`pkg/cluster`)

`pkg/cluster` turns discovered peers into a running Raft shard. The reusable
entry point is transport-neutral and embeddable:

The `Manager` chains methods with retry, returning the first non-empty result.

## Bootstrap orchestration

```go
opts := cluster.Options{
    Method:      "multicast",
    NodeID:      replicaID,
    RaftAddress: raftAddr,
    MulticastSecret: secret,
}
err := cluster.BootstrapShard(ctx, node, opts, shardID, createFn, shardCfg)
```

`BootstrapShard` runs: **discover → validate addresses → add self → check
quorum → build member map → `StartShard`**. The daemon (`qrdbd`) maps its CLI
flags / YAML into `cluster.Options`; embedders (go-xkms, go-quicbaas) do the
same from their own configuration. Helpers `BuildDiscovery`, `ParsePeers`, and
`IsSingleNode` are exported for finer-grained use.

Configuration lives under the `discovery` block — see
[12-configuration.md](../12-configuration.md#discovery).

## Member identities

A replica with a Raft transport admits a member by its **node identity** and by
nothing else. The identity is `spki:` followed by the hex SHA-256 of the
member's transport-certificate public key, so it survives a certificate renewal
on the same keypair and changes only with the keypair. An address is routing
metadata; a certificate from the cluster CA is not, on its own, a claim to any
replica ID. This is go-quicraft's rule, and go-qrdb supplies the identities it
needs.

Where each identity comes from:

| Identity | Source |
|---|---|
| This replica's own | derived from the certificate it presents (`WithRaftMTLS` for the SDK node, `--tls-cert-file` for `qrdbd`) |
| Every other replica's | `raft.member_identities` (`--member-identities`), `replicaID=identity` specs the operator collects out of band |

A peer's identity is **never learned from the peer's own connection**: that
would let any holder of a cluster certificate register itself as any replica,
which is the attack identities close. The operator reads each node's identity
from the certificate the CA issued (`qrdbd node-identity --cert FILE`, or
`node.MemberIdentityFromPEM` in the SDK) before any node starts.

A founding replica must name every member of the initial membership; a joining
replica must name at least one existing member it will accept its bootstrap
from. The discovery method is irrelevant: `raft.member_identities` is keyed by
replica ID and applies to whatever membership discovery produces. A configured
entry for the local replica that disagrees with its certificate is refused
before the host is built. A single-node cluster has no transport and needs none.

### Adding replicas: identities come from the committed membership

**go-quicraft commits every member's identity in the shard's membership**
(`Membership.Identities`, read with `SyncGetShardMembership`) — seeded at
bootstrap from the founders' `MemberIdentities`, and changed only by committed
`*WithIdentity` config changes. go-qrdb keeps no identity store of its own; it
reads identities from there. Every add path — `member add` / `AddReplica` on
all six transports, rebalance, dynamic membership, the placement reconciler
admitting and promoting followers, founding a multi-replica dedicated
(IsolationRaft) database, and the admin shard start (`StartShard`, whose
members' identities are built exactly as a dedicated database's founders'
are) — resolves the identity it commits the same way:

| The replica being added | Identity committed |
|---|---|
| already a member of the target shard (a promotion, a re-add) | the one that shard committed for it |
| on a host that is a member of the system shard (every dedicated-shard replica, every rebalance target, a new system-shard replica ID at a member's address) | the one the system shard committed for that host, found by its Raft address |
| on a brand-new node no committed membership names | **none available — refused** unless the operator supplies it |

The operator may always supply the identity (the optional identity on
`member add`); if it contradicts one the cluster already committed for that
replica or host, the add is refused — for any shard, the system shard
included. Every replica the system shard places at the address is consulted:
if they carry different identities, the address does not name one host the
cluster agrees on and the add is refused as ambiguous (a conflict: `409` /
`FailedPrecondition`). An add naming a replica ID the membership already places
at a different address is refused the same way: the committed identity belongs
to that other host. A target shard this node has not loaded (a dedicated
shard being founded) contributes nothing; one that exists but cannot be read is
refused as unavailable (`503` / `Unavailable`) rather than resolved from the
host alone.

**Rebalance never moves the system shard.** Every host is a member of it — its
membership is where each host's identity is committed — so any destination the
cluster can vouch for already hosts a replica, and one that does not is a
brand-new node, which joins through `member add` with its identity. A planned
system-shard move is refused with `*service.RebalanceSystemShardMoveError`, a
move to a host that already hosts the shard with
`*service.RebalanceTargetHostsReplicaError`, and a destination without a
committed identity with `*service.MemberIdentityError`; each is logged and
counted as a failed move. A brand-new node's identity has to come
from outside the cluster for the reason above: the new node's own connection is
exactly what cannot be trusted to say which replica it is. So adding a
brand-new node to a running cluster is two steps — print its identity with
`qrdbd node-identity --cert`, and pass it to `member add`; every later add of a
replica on that host is automatic.

## Member-side CSR join (Key A, cluster CA)

This replaces `docs/clusterenroll-noise-gap.md`'s unresolved options with an
owner ruling: the cluster CA private key (Key A) is **never** distributed to
a joiner, on any transport — that part of the prior ruling stands — but a
joiner can still get a leaf certificate without it, because Key A is now
reachable (barrier-encrypted, replicated — see
[../certificate-authority/architecture.md](../certificate-authority/architecture.md#the-ca-key-provider-seam-pkgcakey))
from **any** admitted, unsealed member, not only the first node.

1. Before the joiner does anything, an admin mints it a **single-use,
   identity-bound join token**: `clusterenroll.MintJoinToken` binds the
   token to `spiffe://<trust domain>/node/<nodeID>` and an expiry, stores
   only the token's SHA-256 digest in the replicated store (never the
   plaintext), and returns the plaintext exactly once. See "Join-token
   minting" below for the admin-facing verb.
2. The joiner fetches the trust anchor (the CA **certificate** only) exactly
   as today — `clusterenroll.FetchAnchor` over Noise/SPKI/DANE/Direct,
   unchanged.
3. The joiner generates its own node key pair (the private key never leaves
   it) and builds a self-signed PKCS#10 CSR for that key.
4. It submits the CSR, with the minted token, to an admitted member's
   dedicated join listener (`clusterenroll.JoinServer`).
   `HandleJoinRequest` checks the cluster CA provider is actually ready to
   sign (`provider.Signer`) BEFORE touching the token at all: a sealed
   member refuses every join attempt with the same generic message,
   whether the presented token is valid or not, so a sealed node answers no
   differently to a prober holding a real token than to one holding
   garbage, and a legitimately minted single-use token is never burned on
   a join that could never have succeeded. Only once that check passes does
   `clusterenroll.ConsumeJoinToken` consume the token atomically
   (compare-and-delete): a second presentation of
   the same token, concurrent or not, is refused exactly as an unknown
   token is, and a token whose trust domain does not match this member's
   own, or that names a tenant path or an already-admitted identity, is
   refused the same cause-independent way (no token-validity oracle). Only
   then does the member sign the CSR with Key A
   (`clusterca.IssueLeafForKey`, reading the cluster CA through whichever
   `cakey.Provider` that member was built with: the built-in
   `SoftwareProvider` over the replicated store, or an injected HSM/TPM/KMS
   one) — **for the token's bound identity, never the joiner's
   self-reported `node_uri`** — and returns the leaf.
5. The joiner now holds a Key-A-chained leaf and can present it for Raft
   peer TLS like any other member.

**The leaf carries the bound identity's own address, never the signer's.**
`IssueLeafForKey` builds the leaf from `clustertls.IdentityLeafTemplate`, not
`clustertls.NodeLeafTemplate`'s `LocalSANs()` — the signing member's own
DNS/IP SANs never leak onto a joiner's certificate. The template does carry
one address SAN: the bound identity's own address, recovered from the SPIFFE
URI SAN itself and classified as an IP or DNS SAN, alongside that URI SAN.
This is not redundant with SPIFFE-identity verification — go-quicraft's Raft
QUIC transport dials a peer by this exact address and runs the ordinary x509
SAN check against it, with no SPIFFE-aware substitute, so a leaf without a
matching SAN could never be dialed as a raft peer at all.

**Admission attempts are rate-limited and locked out per source address**,
on both transports, through one package-level guard shared by Noise\_NK and
SPKI/Direct: a sustained-rate token bucket bounds brute-force guessing, and
`joinLockoutThreshold` consecutive bad-token attempts from one source trip a
`joinLockoutDuration` lockout independent of the rate limit. Switching
transports mid-attack gains an attacker nothing. `JoinServer` additionally
bounds concurrent in-flight Noise\_NK handshakes
(`joinServerMaxConcurrentConns`), refusing (closing the connection) rather
than queuing behind a potential flood once at capacity.

This removes `clusterenroll.Join`'s hard `*EnrollmentUnavailableError`
refusal, for a node configured with a join listener address, public key and
token (`BootstrapOptions.JoinConfigured`) — the CSR-sign exchange is what
"member-side CSR issuance" (named but not implemented in earlier releases)
turns out to be. A node that is not configured with a join listener (e.g.
one meant to be enrolled via explicit TLS files) still refuses with
`*EnrollmentUnavailableError`, unchanged.

### Join-token minting

An admin mints a token through `POST /v1/cluster/join-tokens` (REST) or the
`qrdb admin join-token mint <node-id>` CLI, both reaching
`service.AdminService.MintJoinToken` — a distinct interface
(`service.ClusterJoinTokenMinter`), not a method on the broader
`AdminServicer`, so every existing `AdminServicer` test double in this repo
continues to compile unchanged; a caller type-asserts for the capability.
Permission `cluster:join_token` is `ScopeCross`: minting a token admits a
node to the whole cluster, not to one tenant, so it is granted only to the
cluster **Admin** built-in role (owner ruling, 2026-10-06 — mirroring
`clusterAdminPermissions`'s own platform-only precedent) and, through
`permission.All()` rather than a role grant, `Owner`. Only the Admin grant
survives strict separation of duties, which is always on in a FIPS build and
strips the `Owner` role id outright — without it, no identity could mint a
join token under a FIPS build at all; see
[../seal/fips140-mode.md](../seal/fips140-mode.md#strict-separation-of-duties-and-clusterjoin_token-under-fips).
A tenant's own Admin still cannot exercise the permission: `ScopeCross`
admits only a system-tenant caller. The verb is
wired on all six transports — REST (`POST /v1/cluster/join-tokens`), gRPC
(`MintJoinToken`, permission carried as a method option on the proto RPC and
enforced the same way every other gRPC verb's permission is), QUIC
(`admin.MintJoinToken`), Unix (served by the same `qrdbServiceServer` gRPC
implementation Unix already rides), MCP (the `qrdb_mint_join_token` tool),
and the embedded transport.

**Five of those six enforce the `ScopeCross` denial; the embedded transport
does not, and this is a property of the transport, not a gap in this
verb.** `service.AdminService.MintJoinToken` — like every other
`AdminServicer` verb (`AddMember`, `RemoveMember`, `Drain`, ...) — performs
no permission check of its own; enforcement lives entirely in the
transport/handler layer: REST middleware, the gRPC interceptor reading the
proto method's permission option, QUIC's `handleIf`, MCP's `Perm` field.
The embedded transport calls the service directly, with no handler in
between, so it skips all of that by construction. An embedding process is
its own admission control — the same trust assumption every other admin
verb already makes for it. **Embedded is a fully trusted, in-process
surface with no authorization of its own**, but the rule that follows
from that is about what a response CARRIES, not which verb it names: a
secret-bearing output -- today, exactly `MintJoinToken`'s minted token,
good for admitting a new node to the whole cluster -- must never be
returned through a less-trusted local surface such as a desktop webview,
even though ordinary admin verbs with no secret in their response
(`AddMember`, `setNodeCompaction`, ...) may stay wired through it as the
operator's own local UI. See [../embedding.md](../embedding.md) for the
full statement of this rule and `sdk/typescript/src/wails.ts`'s
`admin.mintJoinToken` for the concrete instance: the desktop binding
deliberately throws `NotImplementedError` rather than wiring this one
verb through to the embedded transport its webview talks to.

**Wire transport (Noise, shipped).** The one-shot `get_ca_bundle`
request/response the Noise bootstrap `Handler` in go-truststrap dispatches
cannot carry a second method — the handler is a fixed map built once in
`NewServer`, and the connection closes after one round trip. The CSR
exchange therefore runs as qrdb's **own** protocol
(`clusterenroll.JoinServer` / `clusterenroll.JoinCSR`), on its **own
dedicated port** with its **own persisted static key**
(`{dataDir}/join-key.hex`, independent of the anchor advertiser's
`noise-key.hex` so the two listeners have independent lifecycles — a member
that is not the first node still runs a join listener, but never an anchor
advertiser). It is framed with `bootstrap.WriteFrame`/`ReadFrame` (already
exported, used unmodified) over a session built with go-truststrap's
lower-level `noiseproto.Session` (handshake + `Encrypt`/`Decrypt` of
arbitrary bytes) — a generic Noise\_NK primitive that is not specific to the
bundle-fetch protocol. go-truststrap is not modified; this package only
calls primitives it already exports.

**Wire transport (SPKI, Direct, shipped).** The same transport-agnostic
core (`clusterenroll.HandleJoinRequest`) backs a second binding: a plain
HTTPS route, `POST /v1/enroll/join` (`clusterenroll.JoinHTTPPath`), on the
node's existing admin HTTPS listener (`pkg/api/rest`, `WithClusterJoin`),
reached the same way `GET /v1/ca/bootstrap` already is -- SPKI-pinned
(`spkipin.NewPinnedTLSConfig`, the same primitive the anchor fetch uses) or
Direct (the system trust store). It carries the identical
`JoinRequest`/`JoinResponse` JSON shape the Noise transport encrypts, in the
clear body of a TLS-protected request: there is exactly one implementation
of the admission check, the CSR validation and the issuance call, called by
both bindings. The client side is `clusterenroll.JoinCSRHTTP`, selected by
`Join` whenever `BootstrapOptions.JoinHTTPURL` is set instead of
`JoinAddr`/`JoinKey`. Like `/v1/ca/bootstrap`, the route exists only on the
regular (post-unseal) server and is deliberately NOT added to the
sealed-window allowlist: the cluster CA (Key A) it signs with is only
reachable once unsealed, so there is nothing for a sealed-window binding to
serve.

**Admission is a separate concern from signing.** Reaching an existing
member and completing the handshake (Noise) or the TLS connection
(SPKI/Direct) proves nothing about whether this joiner should be issued a
certificate — that is what the join token (or the approval queue below)
decides, before the CSR is signed, not after.

## Approval queue (tokenless join)

A joiner with no minted token (`JoinRequest.Token == ""`) is not refused
outright: its CSR is queued, replicated, in `pkg/clusterenroll`'s own
namespace (`__sys:cluster_join_pending/`, keyed by the hex SHA-256 digest of
the CSR itself — a value the joiner's own keypair determines, not an admin
choice), and `HandleJoinRequest` reports `*JoinResponse.Pending` with a
`RequestID` rather than an error. `Join`'s own retry loop (`BootstrapOptions.
JoinApprovalPollInterval`, default 15s) keeps re-presenting the identical
CSR, indefinitely, until an operator decides — a node configured this way
blocks at startup exactly as a human-gated system is meant to, rather than
crash-looping the daemon every few seconds.

1. An operator lists queued requests
   (`service.JoinRequestReviewer.ListPendingJoinRequests`, `qrdb admin
   join-request list`) and decides each one:
   - **Approve** (`ApproveJoinRequest`, `qrdb admin join-request approve
     <id> --node-id <node-id>`) binds the CSR to
     `spiffe://<trust domain>/node/<node-id>` — an operator choice, never the
     joiner's self-reported `NodeURI` in the request, the identical invariant
     a minted join token's bound identity already carries. Approving does
     **not** sign anything itself: it only records the decision in the
     replicated queue. The actual `IssueLeafForKey` call happens the next
     time the joiner's own poll reaches `HandleJoinRequest`, through the
     SAME reserve → sign → promote → advertise sequence
     (`issueAndAdmit`) the token path already uses — admission (store-only)
     and signing (provider-only) stay as separate as they already are for
     the token path. `ApproveJoinRequest` refuses with the same
     `*clusterenroll.ExistingMemberError` MintJoinToken already refuses with
     when the chosen node-id's identity is already admitted or reserved.
   - **Reject** (`RejectJoinRequest`, `qrdb admin join-request reject <id>
     --reason <text>`) records a terminal refusal; the joiner's next poll
     reports `*clusterenroll.JoinRefusedError` and `Join` stops retrying.
2. A decided (approved, rejected, or delivered) entry never expires and is
   never re-decided — `ApproveJoinRequest`/`RejectJoinRequest` refuse with
   `*PendingJoinRequestNotFoundError` on an id that is anything other than a
   live, undecided entry. An **undecided** entry past
   `DefaultPendingJoinRequestTTL` (72h) is treated as absent: a retried
   submission reclaims the slot with the identical CompareAndSwap-from-the-
   observed-stale-value pattern `ReserveJoinIdentity` already uses for the
   token path's own reservation TTL, so a joiner that gave up is never
   wedged in the queue forever.
3. The queue itself is capped (`MaxPendingJoinRequests`, 256): unlike a
   minted token, a tokenless submission proves nothing about its sender at
   all, so without a cap a credential-less source could grow the replicated
   store without bound simply by generating a fresh keypair per attempt. A
   brand-new submission past the cap is refused
   (`*PendingJoinQueueFullError`); a RETRY of an existing entry is never
   refused by the cap, since it grows nothing.
4. `cluster:join_token` (ScopeCross, platform-only — the same permission
   minting a token already requires) gates `ListPendingJoinRequests`,
   `ApproveJoinRequest` and `RejectJoinRequest` on every transport that wires
   `service.JoinRequestReviewer`: reviewing a tokenless CSR is the same
   whole-cluster admission decision minting a token already is, not a
   narrower one.

Every decision, and the queue's own submit/expire/deliver events, are
logged at the `clusterenroll` level (`slog`) the same way every other
security-relevant event in this package already is (join-attempt lockouts,
signing failures); this package does not depend on `pkg/audit`, so a
deployment that wants the compliance trail to carry these events ships its
own `slog` handler wired to it.

### Admission lifecycle: lapsed renewal, retirement, CSR identity

The join-identity ledger (`__sys:cluster_join_identities/`) that blocks a
second `MintJoinToken`/`ConsumeJoinToken` for an already-admitted identity
(above) is not a single bit. Each record classifies into one of five
states (`clusterenroll.identityState`): **absent**, a live **reservation**
(in flight, see "The full sequence" above), an **active** admission, a
**lapsed** one, or **retired**.

- **Active vs. lapsed.** `PromoteJoinIdentityAdmitted` and
  `MarkJoinIdentityAdmitted` read the issued leaf's own `NotAfter` and
  serial back off the certificate and store them alongside the admission
  timestamp. A later admission check compares `NotAfter` against now:
  still in the future is **active** (blocks a fresh mint or consume,
  unchanged); already past is **lapsed**. A record written before this
  field existed (the bare RFC 3339 timestamp earlier releases wrote) has
  no recorded expiry and is treated as perpetually active — fail closed,
  never lapsing on its own.
- **Renewing a lapsed admission is not a distinct verb.** A lapsed
  identity is simply **not blocking**: `MintJoinToken`, `ConsumeJoinToken`
  and `ReserveJoinIdentity` all treat it exactly like an absent one, so an
  admin mints an ordinary token and the joiner runs the ordinary CSR
  exchange — ordinary rules, no renewal-specific wire shape, no special
  case for the signing member to get wrong.
- **Retirement (`clusterenroll.RetireJoinIdentity`) is permanent.** An
  admin who removes a member from the cluster, or who must treat an
  issued leaf as revoked, retires its node identity: the ledger record
  becomes `retired`, which blocks `MintJoinToken` (with the distinct
  `*RetiredJoinIdentityError`, since this is an authenticated admin
  action, not the anonymous join wire path) and `ConsumeJoinToken` (with
  the SAME generic refusal every other cause gets — no oracle) forever.
  Unlike a lapsed admission, a retired one is **never** reclaimed by
  `ReserveJoinIdentity`, whether or not its leaf has also expired: "a
  removed member cannot re-enrol with its old cert or identity" holds
  regardless of timing. Retiring writes unconditionally (a plain `Put`,
  not a conditional swap) over whatever state the identity is currently
  in — absent (pre-emptively banning an identity before it ever attempts
  to join), reserved, active, lapsed, or already retired (idempotent) —
  because it is a rare, deliberate admin action that must not be refused
  by a race it did not expect to find. The one accepted race: a join
  already past its reservation when the retirement lands can still
  complete, since the token's single use is already spent by the time
  `ReserveJoinIdentity` ran; an operator who needs the retirement to win
  retires the identity again afterward.
- **CSR identity validation.** Before the token is touched — the same
  ordering the proof-of-possession and key-strength checks above already
  use — `HandleJoinRequest` requires the CSR to carry the join request's
  own claimed identity (`JoinRequest.NodeURI`) as its **sole** subject
  alternative name: exactly one URI SAN, equal to `NodeURI`, and no DNS,
  IP or email SANs at all. This is the shape every legitimate joiner's
  own `buildJoinCSR` already produces. It does **not** make the claimed
  identity authoritative for signing — `IssueLeafForKey` still signs for
  the TOKEN's bound identity, resolved by `ConsumeJoinToken`, never for
  `NodeURI` or anything else the joiner asserts — it only catches a
  confused or tampered client, or a relay that altered one copy of the
  claimed identity but not the other, before either reaches the token.

### Why this does not reopen the shared-CA-key defect

`docs/clusterenroll-noise-gap.md` described a transport that silently
stripped the CA **key** out of a bundle meant to carry it. That mode is
retired, not repaired: Key A's private half is never serialized onto the
wire in any direction, by either side, in this design. What crosses the
wire is a certificate (anchor, step 1) and a CSR/leaf pair (steps 2–4),
exactly the two artifact types trust-bootstrap already treats as public.

## Enrollment-role, kept

`clusterenroll.RoleFile` (`enrollment-role`) is **not** removed. It was
never purely a function of CA key custody: `StartAdvertiser` has only ever
served the CA **certificate** (never the key, even before this change — see
`docs/clusterenroll-noise-gap.md`), so every member already caches `ca.pem`
locally today regardless of where Key A's private half lives. The role file
still encodes `RoleSingleNode` (no Raft peers at all, unrelated to key
location) and still decides whether `clusterenroll.Resolve`/`ResolveCA`
start the Noise listener **for the node that generated or cached the CA via
those paths**. What it no longer decides is whether any OTHER member may
serve: that was the "smaller, separate topology decision" this document
used to leave open, and the next section makes it.

## Every admitted, unsealed member serves the anchor

**Owner ruling (2026-10-06).** Pinning the anchor-serving role to the one
node that happened to generate the cluster CA was a single point of
failure: `cmd/qrdbd` registered `/v1/ca/bootstrap` and ran the Noise
anchor advertiser only where `clusterenroll.Resolve`'s first-node path (or
its resume) returned a non-nil `Advertiser` — every joined node, and every
node that merely loaded cached material on a later boot without being that
one node, served neither. Once that node was gone for good, `FetchAnchor`
and `Join` had nowhere left to reach, permanently, no matter how many other
members were alive and unsealed.

The fix adds no second copy of Key A and no new key transport: every member
already caches the identical certificate (`ca.pem`, `bundle.CACertPEM`)
regardless of whether it generated or joined — the paragraph above has
always been true. `cmd/qrdbd`'s `servesAnchor` (`bootstrap.go`) now gates
both surfaces on holding that certificate at all, not on which Resolve path
produced it: an explicit BYO-TLS node (`TLSBundle.ExplicitTLS`) is still
excluded, since its CA is the operator's own, never qrdb's, but a
single-real-peer deployment is not — a self-issued-CA cluster can only ever
FOUND with one real member in the first place (every founder's identity has
to be known to every other founder before any of them starts, which is only
possible for an identity a node derives from a keypair it has already
generated; the generating node's own keypair exists before anyone has
joined, and nobody else's does), so excluding a literal single-peer
deployment would make a cluster meant to grow by member-side CSR join
permanently unable to serve the first node a second member ever needs to
reach. `startMemberAnchorAdvertiser` starts the identical
`clusterenroll.StartAnchorAdvertiser` the first node's own
`Resolve`/`ResumeAdvertiser` path already calls, from the same certificate,
for any member that path did not already start one for — so a process
never runs two anchor listeners, and the enforcement that no private key is
ever served (`ValidateAnchorBundle`) is the same single check either path
goes through.

**No change to verification.** A joiner's `FetchAnchor`/`Join` still
validate exactly what they always did (`ValidateAnchorBundle`, the pinned
SPKI hash or Noise static key, the single-use join token); what changes is
only which members a joiner may point `--bootstrap-addr` /
`--bootstrap-join-url` / `--bootstrap-noise-addr` at. A deployment that
loses the node that generated Key A now continues enrolling new members
through any admitted, unsealed survivor — see
[cli.md](cli.md#trust-anchor-fetch) and
[user.md](user.md#adding-a-node-member-side-csr-join).

## Dynamic membership (opt-in)

`cluster.MembershipManager` reconciles the Raft group as nodes join and leave,
realizing the etcd-style **lease + watch** membership pattern on go-qrdb's own

## Dynamic membership — opt-in


- Each node registers a member entry (`__sys:cluster/members/{nodeID}`, in the
  system namespace so it is hidden from user KV scans) backed by a
  **lease** and refreshes it on a keepalive interval.
- A node's death stops the keepalive; the lease expires; the entry is deleted;
  a **prefix watch** surfaces the removal.
- On the **leader**, join/leave events drive `AddMember`/`RemoveMember` on the
  shard's Raft group, applied by a single serialized worker (one in-flight
  reconfiguration per replica, latest intent wins).
- On startup and after a watch re-subscribe, the manager reconciles against a
  full membership **snapshot** (`ListMembers`) so members that joined or left
  while it was not watching are converged in both directions. The snapshot is
  O(cluster size); because membership sets are small and reconnects are rare,
  this full rescan is intentional rather than a revision-resumed watch.

It is built against two narrow interfaces (`MembershipStore` for lease-backed
entries + prefix watch, and `Reconfigurer` for the Raft add/remove + leader
lookup) so it is testable and embeddable without import cycles. The in-process
`MembershipStore` is backed by the MVCC lessor and watch hub; the same lessor
and watch subsystem also backs the SDK `Lease()`/`Watch()` surface that the
service registry uses — one source of truth, two consumers.

**The member record is always published.** Every raft-mode node -- `qrdbd`
and the embeddable clustered node alike -- publishes its own lease-backed member
entry (Raft address, the API endpoints it serves per protocol, its features,
and its liveness), whether or not dynamic membership is on. Without dynamic
membership the record is published by `cluster.Advertiser` alone
(`sdk.Client.NewNodeAdvertiser`; qrdbd logs `member record published`), with
no reconfiguration loop; with it, the membership manager publishes the same
record as part of its own start. While the node barrier is sealed neither
proposes anything: the keep-alive is skipped, a first publication is retried
after the unseal, and a sealed leader holds its lease-expiry sweep and grants
every lease a full TTL when it unseals
([../barrier-seal/engineering.md](../barrier-seal/engineering.md)). An
unsealed leader still expires a sealed follower's record when its TTL passes:
a sealed node serves nothing, so it is not offered as a forwarding target.
Replica forwarding resolves the node it
forwards to from these records ([../replica-forwarding/](../replica-forwarding/)).
The endpoints a node publishes come from one derivation,
`config.(*Config).AdvertisedAPIEndpoints`: the advertise host (else the Raft
address's host) with the port of every client protocol the node serves.

**Safety:** dynamic membership is **opt-in and off by default**. Raft
reconfiguration only occurs when a `Reconfigurer` is supplied and the local node
is the leader. A **quorum guard** (`MinQuorum`) refuses to remove a replica when
doing so would drop the active membership below quorum, so a transient liveness
blip cannot destroy the cluster. Joins/leaves are **idempotent** — the manager
consults `Reconfigurer.IsMember` and skips `AddMember` for replicas already in
the group (e.g. the initial bootstrap members) and `RemoveMember` for replicas
already gone. Reconfiguration retries with capped exponential backoff.

**Enabling it.** The `qrdbd` daemon wires the manager onto an in-process
(embedded) SDK client over its own service layer. Turn it on with
`--dynamic-membership` (or `discovery.dynamic_membership: true`); the
auto-removal quorum floor comes from `--cluster-min-peers` /
`discovery.min_peers`. Embedders get the same wiring from any qrdb SDK client
via `client.NewMembershipManager(self, shardID, cfg)` (or the lower-level
`client.NewMembershipStore()` / `client.NewMembershipReconfigurer()`).

## Cluster feature gate

A command only a newer binary understands must never reach a Raft group that
still holds an older one: the older replica refuses it and writes nothing while
its peers apply it, and the group diverges. It is not enough that a group's
CURRENT replicas all support the command: go-quicraft catches a replica that
rejoins a group -- a rolled-back node, or one removed and re-added -- up by
REPLAYING the group's committed log, and an entry already in that log would
use the command. A later membership change can therefore turn a group that
looked fully upgraded a moment ago into one holding an old binary, with no way
to tell beforehand which future change will do it. `pkg/cluster/featuregate`
follows etcd's rule for exactly this reason: a command is sent only once every
node in the CLUSTER -- not just a given group's current members -- supports
it, permanently, for good. Guarded writes (`OpGuarded`, the drop fence's atomic
write) are the first feature it gates; a future opcode is one more entry in
the same list.

- **Advertisement.** Every node with a transport advertises the features its
  binary implements in a **lease-backed** record,
  `__sys:cluster/features/nodes/{replicaID}`, keyed by its system-shard replica
  ID and carrying the raft address the system shard committed for it. It is the
  same `cluster.Advertiser` and lease + watch store dynamic membership uses,
  under its own prefix, and it runs whether or not dynamic membership is on. A
  node stopped gracefully withdraws its record at once, before its host shuts
  down; one that crashes -- or is rolled back to a binary that does not
  advertise -- stops refreshing its lease, and its record expires with it.
  Under dynamic membership a node's member registration carries its features
  too (`cluster.Member.Features`), and the gate follows both.
- **Whether a write is sent guarded.** `Gate.Enabled(shardID, feature)` is
  exactly `Activated(feature)` -- one atomic load, nothing about shardID at
  all. There used to also be a per-group early-enablement path here: a group
  was treated as enabled, ahead of cluster-wide activation, whenever its
  CURRENT replicas all happened to advertise the feature. That was unsafe for
  the reason above -- a later membership change can bring in an old binary
  that the group's log already has the command in -- and it is gone. Before
  activation, every group's writes keep the documented unguarded fallback
  (see "Before activation" below), with no exception for a group that looks
  fully upgraded.
- **Per-group decision.** `Gate.decide(shardID, feature, withWitnesses)`
  reports whether every replica that applies shardID's log (voters and
  observers; witnesses are also checked when withWitnesses is set) runs a
  binary that advertises feature. A record counts only while its (replica ID,
  address) pair is a current system-shard member; a dedicated shard, whose
  replica IDs placement assigns, matches its members to records by raft
  address. It is read fresh from committed membership on every call -- there
  is no cache to go stale -- and it has exactly two callers, neither of them
  the write path above: activation (the system shard, with witnesses) and
  admission, next.
- **Admission.** A replica -- voter, observer or witness -- is added to a group
  only on a node that advertises every feature the group uses: every activated
  feature, and every feature decide finds the group's CURRENT members all
  supporting. Go-quicraft catches a new member up by replaying the group's
  log, so a replica on a node that cannot apply a feature would diverge on the
  first entry that uses it, and a proposal racing the config change could not
  be told apart from one before it; refusing the add makes both impossible.
  Every add path asks first and changes nothing when refused
  (`featuregate.UnsupportedReplicaError`): an explicit member add, a
  dynamic-membership join, a scale-up, a rebalance move, and the placement
  scheduler's candidate list for a new group. A node that is not yet a member
  is judged by its own lease-backed record, feature advertisement or member
  registration. The clearest case for why this is decide, not Enabled, is the
  SYSTEM shard before activation: decide(systemShard, f, true) -- every
  current member advertising f, witnesses included -- is the EXACT condition
  activation waits for. Admitting a non-advertising node onto the system
  shard right then would immediately make that condition false again,
  blocking activation on a node this check could have refused instead. The
  same decide call protects every other shard identically, activated or not:
  a dedicated group's current members already agreeing on a feature is
  exactly what Enabled now deliberately ignores (a later membership change
  could still undo it), and admission on the join path is where that undoing
  is refused, rather than discovered later when an unsupporting replica
  cannot apply an entry already in the group's log.
- **Activation.** Once every system-shard member -- every node, witnesses
  included, since activation enables the feature everywhere -- advertises a
  feature, it is **activated**: recorded permanently under
  `__sys:cluster/features/activated/{feature}`, and enabled for every group from
  then on with one atomic load. A node whose binary lacks an activated feature
  **refuses to start** (`featuregate.UnsupportedFeatureError`), which makes a
  downgrade impossible for every binary that has the gate. A node embedded
  through the Go SDK is built before it can read the activations, so it
  cannot refuse; it never advertises, logs the error, and reports its health
  as `degraded`. A binary from before the gate cannot refuse, so **after
  activation, no node may be started or added on an older release**, and after
  a crash, wait out the lease TTL before starting a node on an older release.
  An activation record is trusted as replicated system state: it is written
  only by a gate, with `PutIfAbsent`, once every member advertises the feature;
  anyone able to forge one can already rewrite any system record. Its
  `activated_by` field is kept for audit, not re-verified.
- **Learning of an activation.** Every other node enables the feature as soon
  as its own system-shard replica has applied the record, from local state,
  with no leader and no quorum involved. Its startup check reads the records
  linearizably, since a downgrade refusal must not miss one; after that a
  record a local read finds is final -- activation is permanent -- so waiting
  on a leader would only add delay. That delay is what matters across a
  leader change: the node taking over a group must not refuse the feature's
  writes the cluster activated long before.
- **Before activation** a group that is not enabled keeps the path every binary
  understands: for guarded writes, the write is proposed unguarded and the drop
  fence is the check made when the database is resolved.
- **One binary, no gate.** A standalone node, and a host with no transport
  (a single-node cluster, a volume tenant's host), holds only this binary: it
  uses every feature from the start. A single-node cluster with a transport
  activates at once, since its only member is itself.

## SDK `Cluster()` client

The SDK exposes cluster membership through a thin facade over the already-wired
control-plane operations — it adds no new wire RPCs:

```go
cc := client.Cluster()
members, _ := cc.Members(ctx, shardID)          // filtered from Peers()

## SDK `Cluster()` client

_ = cc.AddReplica(ctx, shardID, id, raftAddr, "") // delegates to AddMember; "" = committed identity
_ = cc.RemoveReplica(ctx, shardID, id)           // delegates to RemoveMember
_ = cc.PromoteReplica(ctx, shardID, id)          // delegates to PromoteMember
ch, _ := cc.WatchMembership(ctx, shardID)        // polls + emits deltas
for ev := range ch {
    // ev.Members, ev.Added, ev.Removed, ev.LeaderChanged
}
```

`WatchMembership` polls (rather than using a dedicated streaming RPC) so it
works over every transport, including the unary-only REST transport. The poll
cadence is `DefaultMembershipPollInterval`.

### Alternatives rejected

- **Discovery inside go-quicraft.** Rejected: consensus and cluster formation
  are different concerns with different dependency graphs, and putting
  discovery below the store meant the store could not use its own primitives to
  do it.
- **A Kubernetes-specific discovery method.** Rejected: `dns` against a headless
  Service covers it. A dedicated method would be one more thing to maintain for
  no capability.
- **A default multicast secret.** Rejected outright: multicast announcements are
  HMAC-SHA256 authenticated with an **operator-chosen** PSK and there is no
  default. A shipped default secret is not authentication.
- **Dynamic membership on by default.** Rejected: automatic Raft reconfiguration
  is exactly the machinery that can destroy a cluster during a transient
  liveness blip. It is opt-in, leader-only, and quorum-guarded.
- **Refusing to start a first node in FIPS 140-3 mode.** Rejected. The Noise NK
  enrollment listener cannot run there — its X25519 handshake is not an approved
  key-agreement scheme — but nothing about being a first node requires Noise:
  SPKI-pinned TLS, DANE and direct HTTPS all fetch the same CA bundle from
  `/v1/ca/bootstrap`, and the operator never chose a method in the first place
  (there is no advertise-method option to choose). Refusing would mean go-qrdb
  cannot form a multi-node cluster under FIPS at all, over a method a FIPS
  deployment could not have used. So Noise is excluded, loudly, and the node
  advertises the approved methods.
- **Falling back from Noise without saying so.** Rejected on both sides. A
  bootstrap method downgraded in silence is its own defect: the operator is
  running a smaller enrollment surface than the documentation describes, and the
  only other thing that ever reports it is a peer's join failing on a different
  machine. Every exclusion logs at WARN naming FIPS, and the case with nothing
  left to select — a join configured with Noise and nothing else — is refused
  outright at configuration time.
- **Compiling the Noise transport out under a `fips` build tag.** Rejected, even
  though go-truststrap offers exactly that tag. It would produce two binaries
  whose enrollment surfaces differ and whose difference is invisible in the
  artifact, while `GODEBUG=fips140` is a run-time switch. One binary that reads
  the runtime it was started in is the smaller claim.
- **A revision-resumed membership watch.** Rejected in favour of a full
  `ListMembers` snapshot on startup and after a watch re-subscribe. It is
  O(cluster size), membership sets are small, and reconnects are rare — the
  simple rescan converges in both directions.
- **A dedicated streaming RPC for `WatchMembership`.** Rejected so the facade
  works over every transport including unary-only REST; it polls at
  `DefaultMembershipPollInterval`.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Discovery methods and `Manager` | `pkg/discovery` | go-qrdb |
| Bootstrap orchestration, `MembershipManager` | `pkg/cluster` | go-qrdb |
| Member-side CSR join (anchor fetch, CSR exchange, admission) | `pkg/clusterenroll` | go-qrdb |
| Cluster CA (Key A) provider seam | `pkg/cakey`, `pkg/clustertls` — see [../certificate-authority/](../certificate-authority/) | go-qrdb |
| Node admin (health, status, peers, drain, snapshot, shards, topology) | `pkg/service` admin | go-qrdb |
| Shard rebalancing | `pkg/placement`, `pkg/service` | go-qrdb |
| Raft primitives, leader election, log | — | **go-quicraft** |
| SDK surface | `sdk/go` (`Client.Admin()`, `Client.Cluster()`, rebalance client) | go-qrdb |

## Trust boundaries

- **A Raft member is admitted by its node identity.** Holding a certificate
  from the cluster CA is not enough to speak for a replica ID; the identity for
  that ID must have been supplied by the operator (see
  [Member identities](#member-identities)).
- **Multicast discovery requires an operator PSK** and authenticates
  announcements with HMAC-SHA256. Without a secret the method does not run.
- **Membership entries live under `__sys:cluster/members/{nodeID}`**, inside the
  internal namespace, so they are hidden from user KV scans.
- **Every admin verb carries its own permission** — `admin:health`,
  `admin:status`, `admin:peers`, `admin:add_member`, `admin:remove_member`,
  `admin:snapshot`, `admin:drain`, `admin:capabilities`, `admin:start_shard`,
  `admin:shard_state`. Adding and removing replicas are separately gated from
  reading peers. Promoting an observer to a voter (`service.MemberPromoter`,
  `qrdb admin member promote`) reuses `admin:add_member`: a promotion is a
  Raft reconfiguration like an add, not a new capability. Adding a replica
  as a non-voting observer in the first place (`service.ObserverMemberAdder`,
  `qrdb admin member add-observer`) reuses the identical permission for the
  identical reason — it is how an operator reaches `member promote`'s
  success path at all: a plain `member add` always votes, so without
  `add-observer` there was no way to create the observer `member promote`
  promotes. Both are served on every transport — see
  [api.md](api.md#capability-matrix--node-administration).
- **A leader-only write forwards; it does not refuse by node identity.**
  `qrdb init`'s admin-credential step (ceremony-initialize,
  `service.CeremonyServiceWithLeaderGate`) must commit to the system shard,
  so it runs on the leader -- but a follower forwards the identical request
  rather than answering with its own refusal, the same node-parity contract
  `member add`/`add-observer`/`promote`/`remove` and `rebalance execute`
  honor (owner ruling 2026-10-06: "all the nodes are equal..."). Every
  transport forwards a leader-only refusal -- `*service.NotLeaderError`, or
  ceremony-initialize's `*service.AdminCredentialNotLeaderError` -- through
  that transport's own leader-forwarding path
  ([../replica-forwarding/](../replica-forwarding/)): REST's existing
  system-leader forwarding (`tryForwardWrite`/`forwardToLeader`), and gRPC's,
  QUIC's and MCP's own leader methods on the same
  `FrameForwarder`/`ReplicaForwarder` each already carries for the
  per-database `NotAReplicaError` path. Before this, gRPC, QUIC and MCP
  forwarded only the ceremony's refusal, so a membership change sent to a
  follower over them answered `not leader`. The Unix socket
  forwards as gRPC does -- it serves the gRPC services -- and the embedded
  transport forwards as its QUIC client transport does.
- **`GET /health` is a public route** so a load balancer can probe an
  unauthenticated node; nothing else in this feature is.
- **Rebalance `execute` physically moves shards** and is gated apart from the
  read-only `stats`/`plan`/`report`.
- **Member-side CSR join requires the operator-configured join token** (an
  operator-chosen secret, following the same no-default-secret rule as
  multicast discovery's PSK); a CSR presented without it is refused before
  Key A is ever touched.
- **The peer/Raft/enrollment transport trusts Key A only.** A certificate
  issued by Key B (a tenant login CA) is refused on this transport even
  though it is a validly-chained client certificate elsewhere — see
  [../certificate-authority/architecture.md](../certificate-authority/architecture.md#key-b-trust-on-the-client-facing-listeners).

## What this feature deliberately does not do

- **No service discovery.** Application service registries are a consumer
  concern built on lease + watch + KV.
- **No trust bootstrap.** See [../trust-bootstrap/](../trust-bootstrap/).
- **No FIPS-approved replacement for Noise NK.** Noise_NK_25519 has no approved
  DH to substitute; in FIPS 140-3 mode the method is excluded rather than
  swapped. See [../seal/fips140-mode.md](../seal/fips140-mode.md).
- **No automatic rebalancing.** Plans are generated and executed deliberately.
- **No cross-cluster federation.**
