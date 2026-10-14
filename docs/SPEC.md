# go-qrdb specification

> **Status: NORMATIVE** · Scope: go-qrdb
>
> **Rules of this file.** This project carries `docs/SPEC.md` (what it is, and
> every feature with its requirements and status) and
> [`ROADMAP.md`](../ROADMAP.md) (what is complete and what remains). The two
> agree. A feature listed here, in any status, is never deleted: not its docs,
> its code or its issues. Removing or re-scoping a feature needs the owner's
> change to this file. The feature folders `docs/<feature>/` stay the detail
> behind each row. The owner approves this file and ROADMAP.md before they merge.

## Purpose

go-qrdb (QuicRaft DB) is the cluster engine and database system for every
project in the stack. The stack is one product in layers, each project a
library and framework for the one above, inheriting all of its functionality
and re-exporting every lower service through its own SDK. go-qrdb embeds
go-quicraft, the Raft library, and builds on it a distributed key-value store
and the database services over it, run embedded, standalone or Raft-clustered.
Its SDK, `sdk/go`, and its service interfaces are the base of everything above:

- go-xkms adds hardware crypto, auth factors, KMS, CA, ACME and TPM policies as
  services on top of go-qrdb through the `ServiceModule` abstraction, so the
  go-qrdb client works unchanged against an xkms node;
- go-quicbaas, the application-layer framework, exposes the database services
  via go-qrdb and composes go-xkms on top with the hardware crypto
  implementations passed in, so every go-qrdb seam is filled by hardware
  crypto.

Terms: [GLOSSARY.md](GLOSSARY.md).

## Boundaries

go-qrdb embeds go-quicraft as a library and inherits from it:

- multi-group Raft consensus and the QUIC Raft transport, through
  `quicraft.Host`, on which every shard of the store runs;
- the `EpochBarrier`, the L1 sealing strategies `StrategyNone` and
  `StrategyPassphrase`, the software strategy, its KDF and AEAD primitives,
  and Shamir secret sharing.

go-qrdb re-exports those through `sdk/go`: `EpochBarrier`,
`EpochBarrierConfig`, `DefaultEpochBarrierConfig`, `StrategyNone`,
`StrategyPassphrase`, `SealingStrategy`, `Credentials`, `SoftwareStrategy`
and the KDF and AEAD types (`types_seal.go`, `types_seal_strategy.go`); the
Shamir share types (`types_shamir.go`); `ErrDecryptionFailed`; and
`RaftMTLSConfig` (`sdk/go/node`).

go-qrdb owns:

- the distributed key-value store and the database services over it, on the
  inherited Raft consensus;
- `pkg/seal`: `StorageBarrier` over go-quicraft's `EpochBarrier`,
  `BarrierRegistry` and `TenantBarrier` for per-tenant isolation, the
  `StrategyRegistry` that go-xkms hardware strategies are injected into, the
  Shamir application subsystem (quorum, recovery, rekey, root token) and
  `StrategyShamir`;
- `pkg/pin`: the `PINBackend` interface and one software backend, in memory or
  over a storage; callers pass `PINBackend` handles, never raw PIN bytes;
- `pkg/credentials`: versioned named-credential rotation;
- `pkg/ceremony`: the Shamir M-of-N initialization ceremony, share enrollment
  and CSR signing, all through `PINBackend`;
- `pkg/authfactors`: the factor interfaces, the factor registry and the mTLS
  factor;
- `pkg/firstboot`: the canonical first-boot orchestration that go-xkms and
  go-quicbaas delegate to through `sdk/go/node`;
- the `ServiceModule` abstraction through which a layer above adds its
  services;
- per-shard composition through `WithStrategyRegistry`, `WithSealStrategy`,
  `WithPINBackend` and `WithFactorRegistry`.

One cluster engine and one datastore per deployment. A project above embeds
go-qrdb as a library, never as a second product: no second database, cluster or
engine, and no disjoint store or service for the same thing. System-level
records of every layer live in the system tenant and tenant data in its tenant;
tenants are strictly isolated, and there is no shared cross-tenant client. A
feature a layer above needs from go-qrdb is a seam row in both SPECs and a
re-export in that layer's SDK, never a parallel implementation; the layer above
adds services and never wraps, filters or re-models go-qrdb's under its own
names.

go-qrdb never:

- ships TPM 2.0, PKCS#11, FIDO2, PIV or cloud-KMS driver code: go-xkms
  implements those and passes them in as `Factor` or `SealStrategy`
  implementations through the `With*` options;
- holds WebAuthn relying-party ceremony state, an ACME server, a DANE publisher
  or trust-bootstrap dispatch: go-xkms;
- implements the application layer and its composition (go-quicbaas) or Raft
  primitives (go-quicraft);
- copies code from go-xkms or imports it (composition is one-way), or keeps a
  compatibility shim or transitional wrapper.

## Features

Status is one of **shipped** (on main and documented), **in progress** (an open
pull request carries it, or its issue is labelled `task:in-progress`),
**planned** (an open issue in a milestone, not started) or **future** (an open
issue with no milestone, not started, or a design the docs record as not yet
implemented). The detail column links the documentation and issues behind the
row.

### Data plane

| Feature | Status | Requirements | Detail |
|---|---|---|---|
| key-value | shipped | KV operations, MVCC revisions, consistency levels, write modes, compaction, selective restore. | [key-value/](key-value/), [01-architecture.md](01-architecture.md), [05-consistency.md](05-consistency.md) |
| transactions | shipped | Compare-and-swap transactions, atomic batches, file import. | [transactions/](transactions/) |
| guarded-swap | shipped | `sdk.GuardedSwap`, the optimistic compare-and-set loop over the transaction plane. | [guarded-swap/](guarded-swap/) |
| watch | shipped | Change streams with replay from a revision. | [watch/](watch/) |
| leases | shipped | TTL leases and cascade expiry. | [leases/](leases/) |
| distributed-locks | shipped | Locks with fencing tokens and reentrancy. | [distributed-locks/](distributed-locks/), [18-distributed-locking.md](18-distributed-locking.md) |
| coordination | shipped | The SDK coordination surface (lock, lease, CAS, watch) and the acknowledged-deadline ownership model. | [coordination/](coordination/) |
| secondary-indexes | shipped | Indexes, compound indexes, the query planner, DAO aggregations. | [secondary-indexes/](secondary-indexes/), [08-secondary-indexes.md](08-secondary-indexes.md), [16-query-planner.md](16-query-planner.md), [17-entity-sql-bridge.md](17-entity-sql-bridge.md) |
| full-text-search | shipped | Inverted index, analyzer, tokenizer, under build tag `qrdb_fts`. | [full-text-search/](full-text-search/) |
| collections | shipped | Document collections and ID strategies. | [collections/](collections/) |
| sql | shipped | MySQL and PostgreSQL wire protocols, relational tables, transactions, privileges, under `qrdb_sql`, `qrdb_mysql`, `qrdb_pgsql`. | [sql/](sql/) |
| row-locking | shipped | Optimistic or pessimistic SQL concurrency, `FOR UPDATE` / `FOR SHARE`, the replicated lock table, deadlock detection. | [row-locking/](row-locking/) |
| vectors | shipped | Vector namespaces, k-NN and hybrid search, index maintenance, under `qrdb_vector`. | [vectors/](vectors/), [15-vectors.md](15-vectors.md) |
| time-series | shipped | Metrics, tags, aggregation, retention, under `qrdb_ts`. | [time-series/](time-series/) |
| embeddings | shipped | A text-to-vector provider proxy with caching. | [embeddings/](embeddings/) |
| rag | shipped | Chunking, ingestion, semantic retrieval. | [rag/](rag/) |
| etcd-compatibility | shipped | Per-database etcd v3 endpoints, under `qrdb_etcd`. | [etcd-compatibility/](etcd-compatibility/) |

### Storage and operations

| Feature | Status | Requirements | Detail |
|---|---|---|---|
| databases | shipped | Multi-database lifecycle, per-database protocol, Raft and barrier configuration. | [databases/](databases/) |
| storage-engines | shipped | The `Engine` interface over the file, memory, pebble and namespace backends. | [storage-engines/](storage-engines/) |
| volume-pools | shipped | Named volume pools: per-node mounts, tenant placement, entitlements and capacity, management, the `VolumeProvider` seam. | [volume-pools/](volume-pools/) |
| backup-restore | shipped | Shard snapshots, verification, selective restore. | [backup-restore/](backup-restore/) |
| clustering | shipped | Peer discovery, cluster formation, membership, node admin, rebalance. | [clustering/](clustering/), [deployment-multi-node-raft.md](deployment-multi-node-raft.md) |
| run-modes | shipped | Standalone and Raft run modes, the three capability gates. | [run-modes/](run-modes/), [run-modes.md](run-modes.md), [embedding.md](embedding.md) |
| capabilities | shipped | The effective runtime feature profile from build tags, run mode and configuration. | [capabilities/](capabilities/) |
| configuration | shipped | Load order, validation, the flag and YAML mapping. | [configuration/](configuration/), [12-configuration.md](12-configuration.md) |
| deployment | shipped | Host requirements: locked memory, container and systemd capabilities. | [deployment/](deployment/) |
| release | shipped | `make release-alpha`, VERSION as the release record, lockstep module tags, post-publish validation. | [release/](release/) |
| api-server | shipped | The unified multi-protocol server and the `ServiceModule` seam. | [api-server/](api-server/), [embedding.md](embedding.md) |
| replica-forwarding | shipped | A node holding no replica of a database forwards the request to its replica leader on the caller's behalf, on REST, gRPC, Unix socket, QUIC, MCP and embedded. | [replica-forwarding/](replica-forwarding/) |

### Security and identity

| Feature | Status | Requirements | Detail |
|---|---|---|---|
| barrier-seal | shipped | The at-rest barrier, strategies, the strategy registry, enable-encryption, the per-node key architecture. | [barrier-seal/](barrier-seal/), [seal/](seal/) |
| shamir-custody | shipped | Security Officers, M-of-N shares, rekey, recovery keys, root tokens. | [shamir-custody/](shamir-custody/), [seal/](seal/) |
| ceremony | shipped | The initialization ceremony state machine and the share lifecycle. | [ceremony/](ceremony/), [ceremony-sdk.md](ceremony-sdk.md) |
| firstboot | shipped | The system tenant, built-in roles, the platform admin, the admin credential paths. | [firstboot/](firstboot/), [bootstrap.md](bootstrap.md) |
| trust-bootstrap | shipped | The client-side CA trust command shape; every mode returns `PendingBootstrapBackendError` and the TOFU implementation is go-xkms's. | [trust-bootstrap/](trust-bootstrap/) |
| certificate-authority | shipped | Client certificate issuance, export and revocation, signing through a `crypto.Signer`. | [certificate-authority/](certificate-authority/) |
| trust-anchors | shipped | Per-tenant registered CA certificates for bring-your-own-certificate enrollment. | [trust-anchors/](trust-anchors/) |
| credentials | shipped | Named credentials, versioned rotation, secret resolution. | [credentials/](credentials/) |
| key-wrapping | shipped | The KEK provider seam (`keywrap.Provider`), its software implementation, the registry, backup envelope encryption. | [key-wrapping/](key-wrapping/) |
| tenant-dek | shipped | The per-tenant versioned DEK keyring: encrypt, decrypt, rewrap, rotate, retire. | [tenant-dek/](tenant-dek/) |
| identity-rbac | shipped | Users, roles, the permission vocabulary, the RBAC PDP. | [identity-rbac/](identity-rbac/), [auth/](auth/) |
| auth-factors | shipped | The `Factor` framework, the N-factor model, PIN custody. | [auth-factors/](auth-factors/) |
| audit | shipped | Compliance-grade audit logging and query. | [audit/](audit/) |
| rate-limit | shipped | Per-permission request throttling, enforced identically on every transport. | [rate-limit/](rate-limit/) |
| attested-caller | shipped | Admit a gated operation only from a peer whose measured state is currently acceptable. | [attested-caller/](attested-caller/) |
| multi-tenancy | shipped | The isolation ladder, quotas, entitlements, per-tenant barriers. | [multi-tenancy/](multi-tenancy/), [scoping.md](scoping.md) |

### Clients

| Feature | Status | Requirements | Detail |
|---|---|---|---|
| desktop-gui | shipped | The Wails desktop application, under build tag `desktop`. | [desktop-gui/](desktop-gui/), [gui/README.md](gui/README.md) |
| typescript-sdk | shipped | The `@qrdb/sdk` TypeScript client: REST and Wails transports, typed errors, contract tests. | [typescript-sdk/](typescript-sdk/) |

### Remaining work

| Feature | Status | Requirements | Detail |
|---|---|---|---|
| cluster-verification | in progress | The qrdb-3-node-proven gate: a linearizability checker, nemesis and soak harness; a clock nemesis that perturbs rate, with lease reads on; a pinned port set for the three-node cluster; cluster suites green on main. | #669, #679, #671, #668 |
| sealed-mode-serving | in progress | Adopt the go-quicraft v0.21.0 release candidate, with sealed-mode serving. | #659, [barrier-seal/](barrier-seal/) |
| bounded-startup-read | in progress | Each startup read attempt is bounded and a boot failure is logged; go-quicraft's typed retryable `ErrReadIndexNotReady` (errors.Is-matchable, classed Unavailable, returned while the leader has not committed the read index) is retried until the startup deadline. The consuming side of the seam with go-quicraft. | #668, [clustering/](clustering/) |
| lease-reads | in progress | Lease reads are documented as linearizable under the 1 % clock-rate bound, not as a stale-read window. | #678, [key-value/](key-value/) |
| auth-factor-claim | in progress | A conditional factor-add, one-time-claim primitive; a hard delete or rollback reinstate, so a rolled-back add frees its SPKI and credential indexes. | #655, #684, [auth-factors/](auth-factors/) |
| protected-memory | in progress | Scoped borrow, liveness, constant-time `Equal` and a live empty value, re-exported from `sdk/go`; `RetrieveCredential`'s return type and secret-bearing DTOs; protected key material across the `EncryptionBarrier`; unseal shares out of ordinary heap memory; a zeroizable secret-bytes entity field. | #650, #22, #145, #79, #633, [barrier-seal/](barrier-seal/), [credentials/](credentials/) |
| storage-compare-and-swap | in progress | `CompareAndSwap` on the `Storage` interface; file-engine guarded writes atomic across processes, or shared opens refused. | #649, #632, [guarded-swap/](guarded-swap/) |
| request-context-dpop | in progress | The request context carries the DPoP proof, HTTP method and URI to authenticators. | #646, [api-server/](api-server/) |
| fips-140-3 | in progress | The nightly FIPS 140-3 job is green and measured, its placement pending decision; clusterenroll adopts go-truststrap's P-256 handshake to clear the X25519 blocker. | #618, #608, #20, #118, [seal/fips140-mode.md](seal/fips140-mode.md), [clusterenroll-noise-gap.md](clusterenroll-noise-gap.md) |
| documentation | in progress | ROADMAP.md lists every milestone and names the folder behind each item; document banners; a spec-review scrub of docs/ for the release candidate; where the agent spec lives. | #657, #658, #635, #634, [README.md](README.md) |
| file-backend-encryption | in progress | The file backend encrypts data at rest under the node barrier, as pebble does. | #630, [storage-engines/](storage-engines/) |
| embedder-seams | in progress | `sdk/go` exposes the seams embedders need: cakey, cert-key factory, volume keys, DAO-from-factory, permission proto, SPIFFE. | #626, [release/extension-seams.md](release/extension-seams.md) |
| strict-tenant-isolation | in progress | Each strict tenant gets a home store holding every record the platform does not need to place, route, size or retire it, partially implemented; per-node tenant barrier init. | #683, [multi-tenancy/](multi-tenancy/) |
| numeric-index-ranges | in progress | A RANGE plan over a numerically indexed field orders numerically. | #35, [secondary-indexes/](secondary-indexes/) |
| ci-actionlint | in progress | `make lint` runs actionlint. | #636, [release/](release/) |
| port-reservation-package | in progress | Decide the shared port-reservation package, now that production code is a caller. | #306 |
| identity-hydrator | planned | `sdk/go/node` exposes `WithIdentityHydrator`, so the login Subject is the immutable user id, not the username. | #670, [identity-rbac/](identity-rbac/) |
| release-readiness | planned | Release readiness for the public release. | #565, [release/](release/) |
| arm64-release | planned | linux-arm64 and linux-arm64 FIPS artifacts from the release workflow; the integration suite run once on arm64 before the RC. | #702, [release/](release/) |
| range-delete | future | An engine range delete, so reclaiming a dropped database is not one proposal per key. | #457, [key-value/](key-value/) |
| dead-code-triage | future | Triage the unreachable symbols; wiring is an answer as well as deletion. | #350, [release/](release/) |
| barrier-passphrase-change | future | A supported passphrase change for the system barrier, for embedders. | #631, [barrier-seal/](barrier-seal/) |
| lease-keepalive-latency | future | The lease keepalive round trip loses its heavy tail. | #275, [leases/](leases/) |
| role-delete-cascade | future | Deleting a role removes its id from every holder. | #697, [identity-rbac/](identity-rbac/) |
| clusterenroll-coverage | future | `pkg/clusterenroll` coverage restored to its pre-Go-1.27.2 baseline. | #652, [clustering/](clustering/) |
| lean-node-build | future | Opt-in `qrdb_*` tags, so an embedder links no Raft, pebble, servers, cluster, vector or time-series code. | #627, [run-modes/](run-modes/) |
| multi-model-databases | future | A database is an independently configured multi-model container inside a tenant, with one storage choice that every engine it serves is built over. | [databases/](databases/), [vectors/](vectors/), [time-series/](time-series/) |
| object-storage-lsm | future | The object-storage LSM segment layout, proposed and pending decision. | [storage-engines/](storage-engines/) |
| volume-pool-tenant-moves | future | Tenant moves between volume pools, in phase 2. | [volume-pools/](volume-pools/) |
| sqlite-vfs | future | go-qrdb as a SQLite storage backend through a VFS. | [future/sqlite-vfs.md](future/sqlite-vfs.md) |

## Interfaces

- Go SDK: [sdk/go](../sdk/go/); embedded node [sdk/go/node](../sdk/go/node/), `database/sql` driver [sdk/go/driver](../sdk/go/driver/), GORM [sdk/go/gorm](../sdk/go/gorm/).
- TypeScript SDK: [sdk/typescript](../sdk/typescript/), [typescript-sdk/](typescript-sdk/).
- `qrdb` CLI: [cmd/qrdb](../cmd/qrdb/); each feature's `cli.md`.
- `qrdbd` daemon: [cmd/qrdbd](../cmd/qrdbd/), [configuration/](configuration/).
- REST over HTTP/2 and HTTP/3: [pkg/api/rest](../pkg/api/rest/), [sdk/go/transport/rest](../sdk/go/transport/rest/), [api-server/](api-server/).
- gRPC: [pkg/api/grpc](../pkg/api/grpc/), [sdk/go/transport/grpc](../sdk/go/transport/grpc/).
- QUIC: [pkg/api/quic](../pkg/api/quic/), [sdk/go/transport/quic](../sdk/go/transport/quic/).
- MCP: [pkg/api/mcp](../pkg/api/mcp/), [sdk/go/transport/mcp](../sdk/go/transport/mcp/).
- Unix socket: [pkg/api/unix](../pkg/api/unix/), [sdk/go/transport/unix](../sdk/go/transport/unix/).
- Embedded, in process: [sdk/go/transport/embedded](../sdk/go/transport/embedded/), [embedding.md](embedding.md).
- SQL core, `qrdb_sql`: [pkg/sql](../pkg/sql/), [sql/](sql/).
- MySQL wire protocol, `qrdb_mysql`: [pkg/sql/mysql](../pkg/sql/mysql/), [sql/](sql/).
- PostgreSQL wire protocol, `qrdb_pgsql`: [pkg/sql/pgsql](../pkg/sql/pgsql/), [sql/](sql/).
- etcd v3 protocol, `qrdb_etcd`: [pkg/api/etcd](../pkg/api/etcd/), [etcd-compatibility/](etcd-compatibility/).

## Release criteria

The [Release candidate](../../milestone/12) milestone is the public release. It closes when:

1. every issue in the milestone is closed: #20, #22, #118, #145, #565, #646,
   #649, #650, #655, #657, #658, #670, #678; and
2. the owner approves the publication, which is when the version is confirmed.
