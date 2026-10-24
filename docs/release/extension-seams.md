# Extension seams

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-21
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
| Embedded transport | In-process calls with a caller identity. | The node-sanctioned service principal acts in the system tenant only (see the scope note). |
| `sdk/go` re-exports | The seam types an embedder implements (`sdk.CAKeyProvider`, `sdk.CAKeyProviderFactory`, `sdk.ClientCertProvider`, `sdk.VolumeKeyProvider`, ...) and the SDK constructors over them. | An embedder imports only `sdk/go/...`; every row below marked `Y` is reachable that way, and `test/buildgates/sdk_external_composer_test.go` builds an embedder in a module of its own to prove it. |

## Capability inventory

**Reachable** `Y` means a composer can supply the implementation through
`sdk/go/node` and `pkg/server` and it takes effect. The last column links a
remaining gap, listed under [Open gaps](#open-gaps).

| Capability | Seam | Option | Reachable | Remaining |
| --- | --- | --- | --- | --- |
| Node mTLS identity (raft, QUIC, cluster key plane) | `RaftMTLSConfig.Certificate` (`*tls.Certificate` whose `PrivateKey` is any `crypto.Signer`); `lifecycle.RaftTenantClusterConfig.Identity` | `WithRaftMTLS` | Y | none. The Raft transport, the member identity, the key plane's sender and receiver, and the cluster join all take the node identity from the one `*tls.Certificate`. The `Cert`/`Key` PEM pair remains the software path, and `qrdbd` builds the identity from its TLS bundle. |
| Cluster identity (CA trust, bootstrap bundle) | `cakey.CertOnlyProvider`, bootstrap bundle | `WithBootstrapCABundle`, `WithCABootstrap`, `WithClusterJoin` | Y | none for a composer: it enrolls its node however it chooses (see the next row) and hands the result to `WithRaftMTLS` as a signer-backed `Certificate`. `qrdbd`'s own joiner (`clusterenroll.Join`) generates its key in process; that is the daemon's software default, not a composition surface. |
| Cluster enrolment, CSR join | `cakey.Provider` on the server; a caller-built CSR on the joiner, so the key can sit behind a `crypto.Signer` | `StartJoinServer`, `MintJoinToken`, `JoinCSR`, `JoinCSRHTTP` in `sdk/go/clusterenroll` | Y | none |
| Cluster enrolment, convenience join | None. `Resolve`, `ResolveCA` and `Join` generate the node key in process and return PEM | `sdk/go/clusterenroll` | software path only | none: it is the default, and a composer uses the CSR join |
| Server TLS identity | `crypto.Signer` leaf key; `ceremony.CeremonyCA`, `cert.CertProvider`, explicit `tls.Config` | `WithServerTLSKey`, `WithCertProvider`, `WithCeremonyCA`, `WithTLS` | Y | none. `ceremony.ServerTLSConfig` takes the leaf key as a `crypto.Signer`, sends only its public half to the CA, and signs handshakes through it. Unset, each transport's leaf gets a fresh in-memory P-256 key. |
| CA signing | `cakey.Provider` (`crypto.Signer` plus certificate), `cakey.CertOnlyProvider`; in the SDK `sdk.CAKeyProvider`, `sdk.CAKeyAdapter`, `sdk.CAKeyCertOnlyProvider` | `node.WithClusterCAKeyProvider`, `WithCABootstrap`, `Infra.ClusterCAKeyProvider` | Y | none. `ceremony.CeremonyCA.IssueCertificate` certifies a caller-held `IssueCertRequest.PublicKey`. No path returns the CA key. |
| Per-tenant signing (login CA Key B) | `cakey.ProviderFactory` (`sdk.CAKeyProviderFactory`) | `WithCertKeyProviderFactory` on `pkg/server` and `sdk/go/node`, forwarded in both boot paths | Y | none. Mutually exclusive with `WithCertProvider`, which replaces the login CA the factory keys: a node given both fails to build with `CertProviderConflictError`. |
| Holder signing (share claims) | `ceremony.SignShareClaim(crypto.Signer, ...)` | caller supplies the signer | Y | none |
| Holder envelope decryption | `keydelivery.Decrypter` (`hpke.PrivateKey`); `keydelivery.NewDecrypter` over an `ecdh.KeyExchanger` (and a `crypto.Decapsulator` for the hybrid KEM) | caller: `ShareEnvelope.Open(keydelivery.Decrypter)` | Y | none. A device-held key opens through `keydelivery.NewDecrypter`, which sends the key agreement to the device; `OpenWithKey` remains the software path. |
| Node key delivery (HPKE receiver) | `keydelivery.NewReceiver(keydelivery.Decrypter, nodeID, ...)` | `WithKeyDeliveryDecrypter`; `RaftTenantClusterConfig.Decrypter` | Y | none. The receiver opens through the decrypter, which must be the DHKEM(P-256) key of the node's mTLS certificate; a mismatch turns the key plane off. Unset, the default composition converts the node's software key. |
| Seal strategy (system barrier) | `seal.SealingStrategy`, resolved through `seal.NewSystemStrategyRegistry` | `node.WithSealStrategy` (registered under its ID); `server.WithStrategyRegistry` | Y | none. Both boot paths resolve the system barrier's strategies through the registry. |
| Seal strategy registry (system, tenant and database barriers) | `seal.StrategyRegistry` | `server.WithStrategyRegistry`, passed as a node server option; `Infra.StrategyRegistry` | Y | none. `sdk/go/node` overlays it on the system barrier's software strategies; `pkg/database` resolves per-database barriers through it, and `pkg/tenant` through the default `BarrierKeyProvider` over it (next row). |
| Tenant barrier key (per node) | `seal.BarrierKeyProvider`: asked with a `BarrierKeyRequest` (barrier, node, strategy, opaque `KeyRef`, epoch), it returns the `SealingStrategy` plus the `CredentialKind` and `Custody` it declares | `server.WithBarrierKeyProvider`, passed as a node server option; `Infra.BarrierKeyProvider` | Y | none. An injected provider wins; otherwise `seal.NewBarrierKeyProvider` resolves over the `StrategyRegistry`. qrdb never parses a `KeyRef`. |
| Database barrier | Resolves its mode through the `StrategyRegistry` first, then the built-in passphrase, empty and shamir modes; anything else is `UnsupportedBarrierStrategyError` | `server.WithStrategyRegistry` (forwarded to `database.WithBarrierStrategyRegistry`) | Y | none |
| Seal store | `storage.Storage`, opened by the node under `<install-dir>/seal` | none, by design | node-local | see [The seal store](#the-seal-store) |
| Tenant DEK engine | `dek.Provider`; `dek.Keyring` is the software default | `WithDEKProvider` | Y | none |
| Key wrap (DEK and backup KEK) | `keywrap.Provider`, `keywrap.Registry` | `WithKeyWrapProvider`, `WithKeyWrapRegistry`, `WithKeyDir` | Y | none |
| Auth factors | `authfactors.FactorRegistry`, `Factor` | `WithFactorRegistry` | Y | none |
| PIN backend | `pin.PINBackend` | `WithPINBackend` | Y | none |
| Trust anchors | `trustanchor.Registry` | `WithTrustAnchors` | Y | none |
| Peer attestation verifier | `auth.PeerAttestationVerifier` through `middleware.PeerAttestationGate` | `WithPeerAttestationGate` | Y | none |
| Extra authenticators | the auth chain (`auth.Authenticator` at an `auth.ChainPosition`) | `node.WithAuthenticators` | Y | none |
| Storage backend, standalone data plane | `storage.Storage` | `WithStorageFactory` | Y | none |
| Storage backend, clustered replicas | `node.ReplicaStorageFactory` | `node.WithReplicaStorageFactory` | Y, `disk` and `fast` | none. The MVCC store and the state machines that carry their own storage refuse the option with `statemachine.ReplicaStorageNotApplicableError`. |
| Backup sink | `service.BackupSink` | `WithBackupSink` | Y | none |
| Audit sink | `audit.AuditLogger` | `WithAuditLogger` | Y | none |
| Replicated audit chain | `service.OptInAuditLogger` | `WithReplicatedAuditLogger`; the node's own is forwarded to every network transport | Y | none |
| Clock | `clock.Clock` | `WithClock` | Y | see [The clock](#the-clock) |
| Rate limiter, auth middleware | `ratelimit.Limiter` | `WithRateLimiter` | Y | none |
| Rate limiter, invitation redeem | `ratelimit.Limiter` | `WithInvitationRedeemLimiter` | Y | none |
| Rate limiter, holder act | `ratelimit.Limiter` | `WithHolderActLimiter` | Y | none |
| Login CA (client certificates) | `cert.CertProvider` (`sdk.ClientCertProvider`); `clienttrust.CertSource` (`sdk.ClientCertSource`) for the client-trust pool | `WithCertProvider` on `pkg/server` and `sdk/go/node`, forwarded into `Infra.CertProvider` in both boot paths | Y | none. It replaces the built-in login CA for every transport. A provider that is not also a `ClientCertSource` issues certificates no node trusts. |
| Volume key custody | `volume.KeyProvider` (`sdk.VolumeKeyProvider`) | `node.WithVolumeKeyProvider` | Y | none |
| Volume backend | `volume.Provider` (`sdk.VolumeProvider`) | `node.WithVolumeProvider` | Y | none |
| DAOs over a named database | `dao.Factory` (`sdk.DAOFactory`) partitioned by database name | `sdk.NewDatabaseDAO`, `sdk.NewDatabaseDAOTransactional`, `sdk.NewClientDAOFactory` | Y | none. A name the factory cannot resolve is refused, never served from the default store. |
| Tenant SPIFFE identity in issued certificates | `spiffe.ID.URI` (`sdk.SpiffeID.URI`) | the composer's own CA calls it | Y | none. It builds only a SAN `TenantFromCert` reads back as the same identity. |
| Authorization of a service module's RPCs | the `(qrdb.v1.permission)` method option, `qrdb/v1/permission.proto` under `sdk/go/proto` | the module's own `.proto` imports it | Y | none |
| Service identity (embedder in-process calls) | `node.WithServicePrincipal`, `Node.ServiceContext` | `sdk/go/node` | Y, system tenant | see the scope note |
| Service identity inside a tenant (tenant-issued delegation) | `sdk.DelegationGrant`, `sdk.DelegationFromCertificate`, `sdk.DelegationCheckLifetime`; issued by `CertClient.IssueDelegation` | `node.WithServicePrincipal` names the one principal a node accepts delegations for | Y | none. Only the tenant's own CA issues one; see the scope note. |

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
- **`ReplicaStorageFactory`** opens the storage of each clustered replica of a
  `disk` or `fast` state machine in place of `raft.sm_storage`. The factory
  receives the shard ID, the replica ID, the replica's own directory
  (`<data-dir>/sm/shard-<S>-replica-<R>`) and the data-plane file system: the
  barrier's when the seal barrier is enabled, nil otherwise. Each call returns
  storage owned by that one replica, and the node closes it with the replica.
  A factory error fails the replica's open with
  `statemachine.ReplicaStorageOpenError`, so the shard does not start. The
  clustered MVCC store (`raft.sm_type` empty, `pebble` or `mvcc`) opens its own
  Pebble database, and it and the state machines that carry their own storage
  refuse the option with `statemachine.ReplicaStorageNotApplicableError`
  rather than ignore it. `WithStorageFactory` is unchanged and still reaches
  only the standalone data plane.
- **`keydelivery.Decrypter`** is the HPKE receive seam. The receiver and
  `ShareEnvelope.Open` open through it, so a device that performs the key
  agreement never exposes the private key. `WithClock` on the receiver anchors
  its freshness window and replay guard to the node's clock.

### The clock

`clock.Clock` is threaded through the lessor (lease time and its expiry
sweeper), the key-delivery receiver (freshness and replay window), invitation
expiry, the token buckets behind the redeem and holder-act limiters, the SO PIN
lockout over the node's seal store (`pin.WithLockoutClock`: its backoffs,
aggregate window and stale-counter eviction), and a share holder's claim
challenge, which is issued and expires on it (`CeremonyService.SetClock`,
carried to every tenant and database holder authority through
`HolderDeps.Clock`). A nil clock keeps the real one. A PIN backend the composer
injects is decorated with the lockout where it is handed over, so a composer
that wants its own clock on that lockout decorates it first with
`pin.NewLockoutBackend(b, pin.WithLockoutClock(c))`; the first decoration of a
backend is the one every later wrapper shares. Certificate validity and
enrolment stamps are records, not gates, and stay on the wall clock.

### The seal store

The seal store holds the node's own seal state, including what the seal
strategy sealed. Per-node seal keys are the one node-local state the
architecture allows, so the store is opened by the node under
`<install-dir>/seal` and is not injectable. The seam a composer uses to put the
**root key** behind a device is the seal strategy, not the store: the store
holds only what the strategy sealed.

## Open gaps

None.

## Scope note: service identity

`node.WithServicePrincipal` sanctions one principal for the embedder's own
in-process work. Its identity is `qrdb-service:<name>` in the **system
tenant**, its permissions are exactly those registered for it, and no network
transport accepts it on its own say-so. See
[api-server engineering](../api-server/engineering.md#service-principal).

It cannot act in an isolated tenant on the platform's authority, and that is
deliberate. The system owner and the platform never read tenant data, so no
platform-side setting may let a service principal act as a tenant. It acts
inside a tenant only under a delegation the **tenant** issues. That
certificate is the one mechanism: there is no tenant-scoped `ServiceContext`,
no option that turns delegation on or off or changes its scope, and no
fallback. A delegation is:

- a short-lived X.509 client certificate from the tenant's own login CA (which
  may be hardware-held through `WithCertKeyProviderFactory`), at most 24 hours;
- naming the principal and the delegated permissions in a **critical**
  extension, so a verifier that does not understand it rejects it;
- issued only through the tenant's own `certs:delegate` path — the system
  tenant cannot issue one, and neither can a delegated caller;
- verified by the existing mTLS chain on every transport, re-verified when
  forwarded to the leader, and audited to the tenant's trail on every use;
- holding the delegated scope intersected with the permissions registered
  under the principal's name;
- withdrawn early through ordinary revocation by serial.

An embedder reads a grant with `sdk.DelegationFromCertificate` and
`sdk.DelegationCheckLifetime` without importing anything outside `sdk/go`. See
[identity-rbac](../identity-rbac/architecture.md#tenant-issued-delegations) and
[certificate-authority](../certificate-authority/architecture.md#delegations-to-a-service-principal).

The extension is named `tp-delegation`, object identifier
`1.3.6.1.4.1.29377.101.24`.
