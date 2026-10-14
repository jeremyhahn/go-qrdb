# Extension seams

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-13
> Inventory of the seams a composer replaces; the options named here are the contract.

An upstream composer (go-xkms with hardware-backed cryptography, go-quicbaas
with managed key custody) extends go-qrdb by passing enhanced implementations
through its composition options. This document inventories every capability
such a composer may need to replace or extend, names the seam and the option
that reaches it, and lists the places where a code path still bypasses the seam
or needs raw key bytes.

## Rules the inventory measures against

1. Every capability has a seam, and go-qrdb ships a software default behind it.
2. The seam is reachable from the composition surfaces, not only from internal
   packages: `pkg/server` options, `sdk/go/node` `NodeOption`s, and the embedded
   transport. It is forwarded in **both** boot paths (`qrdbd` and the SDK node).
3. Operations go to the device that holds the key. A seam that takes a raw
   private key, or returns one, is a gap: a hardware-backed implementation
   cannot satisfy it.
4. Hardware backends are never implemented in go-qrdb. The composition
   direction is one way: composers import go-qrdb.
5. Every seam ships a fake device in tests that holds its key internally and
   serves only operations, which is what proves no key bytes are needed.

## Composition surfaces

| Surface | What it does | Caveat |
| --- | --- | --- |
| `pkg/server` `With*` options | Configure `server.New`. | Services are built by `lifecycle.BuildServices` **before** `server.New`. An option that is not also forwarded into `lifecycle.Infra` does not reach the services. |
| `server.ResolveCryptoOptions` / `CryptoOptions` | Lets a caller read the resolved options and forward them into `Infra`. | Only options surfaced here can be forwarded. |
| `lifecycle.Infra` | The dependency bag consumed by `BuildServices` and `BuildSealServicesOnly`. | `qrdbd` builds it directly; `sdk/go/node` builds it from its options. |
| `sdk/go/node` `NodeOption`s | The embedder entry point. | Options the caller passes as server options come last and win over the node's own. |
| Embedded transport | In-process calls with a caller identity. | The embedder has no node-sanctioned service principal yet (see the scope note). |

## Capability inventory

**Reachable** `Y` means a composer can supply the implementation through
`sdk/go/node` and `pkg/server` and it takes effect. The last column links a
remaining gap, listed under [Open gaps](#open-gaps).

| Capability | Seam | Option | Reachable | Remaining |
| --- | --- | --- | --- | --- |
| Node mTLS identity (raft, QUIC, cluster key plane) | `RaftMTLSConfig.Certificate` (`*tls.Certificate` whose `PrivateKey` is any `crypto.Signer`); `lifecycle.RaftTenantClusterConfig.Identity` | `WithRaftMTLS` | Y | none. The Raft transport, the member identity, the key plane's sender and receiver, and the cluster join all take the node identity from the one `*tls.Certificate`; `RaftTenantClusterConfig{CertPEM, KeyPEM}` is deleted. The `Cert`/`Key` PEM pair remains the software path, and `qrdbd` builds the identity from its TLS bundle. |
| Cluster identity (CA trust, bootstrap bundle) | `cakey.CertOnlyProvider`, bootstrap bundle | `WithBootstrapCABundle`, `WithCABootstrap`, `WithClusterJoin` | Y | none for a composer: it enrolls its node however it chooses (see the next row) and hands the result to `WithRaftMTLS` as a signer-backed `Certificate`. `qrdbd`'s own joiner (`clusterenroll.Join`) generates its key in process; that is the daemon's software default, not a composition surface. |
| Cluster enrolment, CSR join | `cakey.Provider` on the server; a caller-built CSR on the joiner, so the key can sit behind a `crypto.Signer` | `StartJoinServer`, `MintJoinToken`, `JoinCSR`, `JoinCSRHTTP` in `sdk/go/clusterenroll` | Y | none |
| Cluster enrolment, convenience join | None. `Resolve`, `ResolveCA` and `Join` generate the node key in process and return PEM | `sdk/go/clusterenroll` | software path only | none: it is the default, and a composer uses the CSR join |
| Server TLS identity | `crypto.Signer` leaf key; `ceremony.CeremonyCA`, `cert.Provider`, explicit `tls.Config` | `WithServerTLSKey`, `WithCertProvider`, `WithCeremonyCA`, `WithTLS` | Y | none. `ceremony.ServerTLSConfig` takes the leaf key as a `crypto.Signer`, sends only its public half to the CA, and signs handshakes through it. Unset, each transport's leaf gets a fresh in-memory P-256 key. |
| CA signing | `cakey.Provider` (`crypto.Signer` plus certificate), `cakey.CertOnlyProvider` | `WithCABootstrap`, `Infra.ClusterCAKeyProvider` | Y | none. `ceremony.CeremonyCA.IssueCertificate` certifies a caller-held `IssueCertRequest.PublicKey`; `IssuedCert.KeyPEM` is deleted. No path returns the CA key. |
| Per-tenant signing | `cakey.ProviderFactory` | `WithCertKeyProviderFactory` | Y | none |
| Holder signing (share claims) | `ceremony.SignShareClaim(crypto.Signer, ...)` | caller supplies the signer | Y | none |
| Holder envelope decryption | `keydelivery.Decrypter` (`hpke.PrivateKey`); `keydelivery.NewDecrypter` over an `ecdh.KeyExchanger` (and a `crypto.Decapsulator` for the hybrid KEM) | caller: `ShareEnvelope.Open(keydelivery.Decrypter)` | Y | none. A device-held key opens through `keydelivery.NewDecrypter`, which sends the key agreement to the device; `OpenWithKey` remains the software path. |
| Node key delivery (HPKE receiver) | `keydelivery.NewReceiver(keydelivery.Decrypter, nodeID, ...)` | `WithKeyDeliveryDecrypter`; `RaftTenantClusterConfig.Decrypter` | Y | none. The receiver opens through the decrypter, which must be the DHKEM(P-256) key of the node's mTLS certificate; a mismatch turns the key plane off. Unset, the default composition converts the node's software key. |
| Seal strategy (system barrier) | `seal.SealingStrategy`, resolved through `seal.NewSystemStrategyRegistry` | `WithSealStrategy` (registered under its ID); `server.WithStrategyRegistry` | Y | none. Both boot paths resolve the system barrier's strategies through the registry; `server.WithSealStrategy` is deleted. |
| Seal strategy registry (system, tenant and database barriers) | `seal.StrategyRegistry` | `server.WithStrategyRegistry`, passed as a node server option; `Infra.StrategyRegistry` | Y | none. `sdk/go/node` overlays it on the system barrier's software strategies; `pkg/database` resolves per-database barriers through it, and `pkg/tenant` through the default `BarrierKeyProvider` over it (next row). |
| Tenant barrier key (per node) | `seal.BarrierKeyProvider`: asked with a `BarrierKeyRequest` (barrier, node, strategy, opaque `KeyRef`, epoch), it returns the `SealingStrategy` plus the `CredentialKind` and `Custody` it declares | `server.WithBarrierKeyProvider`, passed as a node server option; `Infra.BarrierKeyProvider` | Y | none. An injected provider wins; otherwise `seal.NewBarrierKeyProvider` resolves over the `StrategyRegistry`. qrdb never parses a `KeyRef`. The node and database barriers join the seam in later work packages. |
| Database barrier | Resolves its mode through the `StrategyRegistry` first, then the built-in passphrase, empty and shamir modes; anything else is `UnsupportedBarrierStrategyError` | `server.WithStrategyRegistry` (forwarded to `database.WithBarrierStrategyRegistry`) | Y | none |
| Seal store | `storage.Storage`, opened by the node under `<install-dir>/seal` | none, by design | node-local | see [The seal store](#the-seal-store) |
| Tenant DEK engine | `dek.Provider`; `dek.Keyring` is the software default | `WithDEKProvider` | Y | none |
| Key wrap (DEK and backup KEK) | `keywrap.Provider`, `keywrap.Registry` | `WithKeyWrapProvider`, `WithKeyWrapRegistry`, `WithKeyDir` | Y | none |
| Auth factors | `authfactors.FactorRegistry`, `Factor` | `WithFactorRegistry` | Y | none |
| PIN backend | `pin.PINBackend` | `WithPINBackend` | Y | none |
| Trust anchors | `trustanchor.Registry` | `WithTrustAnchors` | Y | none |
| Peer attestation verifier | `auth.PeerAttestationVerifier` through `middleware.PeerAttestationGate` | `WithPeerAttestationGate` | Y | none |
| Extra authenticators | the auth chain | node extra authenticators | Y | none |
| Storage backend, standalone data plane | `storage.Storage` | `WithStorageFactory` | Y | Gap E |
| Storage backend, clustered replicas | `statemachine.StorageFactory` | none | N | Gap E |
| Backup sink | `service.BackupSink` | `WithBackupSink` | Y | none |
| Audit sink | `audit.AuditLogger` | `WithAuditLogger` | Y | none |
| Replicated audit chain | `service.OptInAuditLogger` | `WithReplicatedAuditLogger`; the node's own is forwarded to every network transport | Y | none |
| Clock | `clock.Clock` | `WithClock` | Y | see [The clock](#the-clock) |
| Rate limiter, auth middleware | `ratelimit.Limiter` | `WithRateLimiter` | Y | none |
| Rate limiter, invitation redeem | `ratelimit.Limiter` | `WithInvitationRedeemLimiter` | Y | none |
| Rate limiter, holder act | `ratelimit.Limiter` | `WithHolderActLimiter` | Y | none |
| Cert provider | `cert.Provider` | `WithCertProvider` | Y | none |
| Service identity (embedder in-process calls) | none in this tree | none | N | out of scope |

## What the seams guarantee

- **`dek.Provider`** is the tenant DEK engine: `Encrypt`, `Decrypt`, `Rewrap`,
  `Rotate`, `Retire`, `Status`. `dek.Keyring` is the default and wraps each
  version under a `keywrap.Provider` KEK. A composer's provider replaces the
  engine outright, so a device that holds the tenant keys seals and opens the
  data without ever exporting them. `keywrap.Provider` stays the **key-wrap**
  seam (a KEK wraps a DEK); `dek.Provider` is the **data-encryption** seam.
  They are distinct on purpose: an embedder that keeps the software keyring
  still registers a hardware KEK through `keywrap`. See
  [tenant-dek](../tenant-dek/architecture.md#key-wrapping).
- **`service.BackupSink`** carries backup artifacts to durable storage. Without
  one, an embedded node's backups live in process memory only; `qrdbd` defaults
  to `<data-dir>/backups`.
- **`ratelimit.Limiter`** replaces the invitation-redeem and holder-act token
  buckets. The redeem limiter is asked once per source address and once per
  tenant under distinct key prefixes. The caller closes any limiter it supplies.
  See [rate-limit](../rate-limit/architecture.md).
- **`StorageFactory`** opens a standalone node's data-plane storage in place of
  the configured backend. The factory receives the data directory and the
  data-plane file system, so a storage that writes files keeps its I/O under the
  barrier. The node closes what the factory returns.
- **`keydelivery.Decrypter`** is the HPKE receive seam. The receiver and
  `ShareEnvelope.Open` open through it, so a device that performs the key
  agreement never exposes the private key. `WithClock` on the receiver anchors
  its freshness window and replay guard to the node's clock.

### The clock

`clock.Clock` is threaded through the lessor (lease time and its expiry
sweeper), the key-delivery receiver (freshness and replay window), invitation
expiry, and the token buckets behind the redeem and holder-act limiters. A nil
clock keeps the real one. Two time gates still read the wall clock directly and
are not yet behind the seam: PIN lockout and ceremony challenge expiry.

### The seal store

The seal store holds the node's own seal state, including what the seal
strategy sealed. Per-node seal keys are the one node-local state the
architecture allows, so the store is opened by the node under
`<install-dir>/seal` and is not injectable. The seam a composer uses to put the
**root key** behind a device is the seal strategy, not the store: the store
holds only what the strategy sealed.

## Verified results

These were open questions in the first inventory. Each was read and, where it
was a gap, fixed.

| Question | Result |
| --- | --- |
| How does a database barrier resolve its seal strategy? | **Gap, fixed.** It accepted only passphrase, empty and shamir and ignored the registry, so a hardware strategy a composer registered could seal a tenant barrier but not a database barrier. It now resolves through the registry first. |
| Is the replicated audit logger reachable from `sdk/go/node`? | **Gap, fixed.** The node built it and served it on the embedded transport only; REST, gRPC, QUIC, Unix and MCP answered the local chain. The node now forwards it to every network transport, and a caller's own logger still wins. |
| What attestation verifier do auth factors use? | **No gap.** `auth.PeerAttestationVerifier` is an interface reached through `middleware.PeerAttestationGate` and `WithPeerAttestationGate`; a composer supplies its own. |
| What does `sdk/go/clusterenroll` do for identity and signing? | **Partial, fixed.** The CSR join server and client sit behind a `crypto.Signer` but were not exported by the SDK; they now are. The convenience join still generates the node key in process, which is the software default. |
| Do the PIN backend, factor registry and audit logger reach `BuildSealServicesOnly` in the sealed window? | **No gap.** Both boot paths hand the same server options to `buildServices` and `buildSealServices`, and a test pins it. |

## Open gaps

- **Gap E, clustered replica storage.** `WithStorageFactory` reaches the
  standalone data plane. A clustered replica's storage is chosen by its state
  machine through a registry-level factory that node options do not reach.

The time gates named under [The clock](#the-clock) are the only other places a
composer-held time source does not yet reach.

## Scope note: service identity

A node-sanctioned service principal for an embedder's own in-process calls is
owned by the go-xkms composer. This document records that the capability is
absent in go-qrdb today and does not propose a design for it.
