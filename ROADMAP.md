# go-qrdb roadmap

> **Status: REFERENCE** · Owner: Software Department · Last verified: 2026-10-13
> The Software Department refreshes it under handbook `software/conventions/roadmap.md`; the project agent proposes changes by PR.
> Agrees with [docs/SPEC.md](docs/SPEC.md), the normative feature table: shipped = Complete, in progress = Now or Next, planned = Later, future = Future.

## Mission

The cluster engine and database system for every project in the stack: embedded, standalone or Raft-clustered. Embeds go-quicraft, the Raft library, inheriting its multi-group Raft consensus, QUIC Raft transport, `EpochBarrier` and L1 sealing strategies and re-exporting them through `sdk/go`. Owns the data plane, cluster formation, barrier and seal, Shamir custody and the ceremony, identity and the single RBAC PDP, all through `sdk/go`, which with its service interfaces is the base of go-xkms and go-quicbaas. One cluster engine and one datastore per deployment: system-level records of every layer in the system tenant, tenant data in its tenant, tenants strictly isolated. Boundaries: [docs/SPEC.md, Boundaries](docs/SPEC.md#boundaries).

## Milestones

- [Working fundamentally: Raft clustering and storage](../../milestone/28) — 5 open, 1 closed, due 2026-10-27 — closes when main is green on every on-push job for three consecutive pushes and the qrdb-3-node-proven gate has passed on the three-node cluster; order go-quicraft, go-qrdb, go-xkms, go-quicbaas — cluster-verification, sealed-mode-serving — open
- [Release candidate](../../milestone/12) — 13 open, 22 closed, due 2026-11-02 — the public release; the version is confirmed at the owner's publish approval; criteria in [docs/SPEC.md, Release criteria](docs/SPEC.md#release-criteria) — lease-reads, auth-factor-claim, protected-memory, storage-compare-and-swap, request-context-dpop, fips-140-3, documentation, identity-hydrator, release-readiness — open

## Complete

- **key-value** — KV, MVCC revisions, consistency levels, compaction — [docs/key-value/](docs/key-value/) — done
- **transactions** — CAS transactions, atomic batches, file import — [docs/transactions/](docs/transactions/) — done
- **guarded-swap** — `sdk.GuardedSwap` — [docs/guarded-swap/](docs/guarded-swap/) — done
- **watch** — change streams with replay — [docs/watch/](docs/watch/) — done
- **leases** — TTL leases and cascade expiry — [docs/leases/](docs/leases/) — done
- **distributed-locks** — locks with fencing tokens — [docs/distributed-locks/](docs/distributed-locks/) — done
- **coordination** — the SDK coordination surface — [docs/coordination/](docs/coordination/) — done
- **secondary-indexes** — indexes, query planner, DAO aggregations — [docs/secondary-indexes/](docs/secondary-indexes/) — done
- **full-text-search** — inverted index under `qrdb_fts` — [docs/full-text-search/](docs/full-text-search/) — done
- **collections** — document collections and ID strategies — [docs/collections/](docs/collections/) — done
- **sql** — MySQL and PostgreSQL wire protocols — [docs/sql/](docs/sql/) — done
- **row-locking** — SQL row locks, deadlock detection — [docs/row-locking/](docs/row-locking/) — done
- **vectors** — vector namespaces, k-NN and hybrid search — [docs/vectors/](docs/vectors/) — done
- **time-series** — metrics, aggregation, retention — [docs/time-series/](docs/time-series/) — done
- **embeddings** — text-to-vector provider proxy — [docs/embeddings/](docs/embeddings/) — done
- **rag** — chunking, ingestion, semantic retrieval — [docs/rag/](docs/rag/) — done
- **etcd-compatibility** — per-database etcd v3 endpoints — [docs/etcd-compatibility/](docs/etcd-compatibility/) — done
- **databases** — multi-database lifecycle — [docs/databases/](docs/databases/) — done
- **storage-engines** — file, memory, pebble, namespace backends — [docs/storage-engines/](docs/storage-engines/) — done
- **volume-pools** — named volume pools — [docs/volume-pools/](docs/volume-pools/) — done
- **backup-restore** — snapshots, verification, selective restore — [docs/backup-restore/](docs/backup-restore/) — done
- **clustering** — discovery, formation, membership, rebalance — [docs/clustering/](docs/clustering/) — done
- **run-modes** — standalone and Raft run modes — [docs/run-modes/](docs/run-modes/) — done
- **capabilities** — the effective runtime feature profile — [docs/capabilities/](docs/capabilities/) — done
- **configuration** — load order, validation, flag and YAML mapping — [docs/configuration/](docs/configuration/) — done
- **deployment** — host requirements — [docs/deployment/](docs/deployment/) — done
- **release** — `make release-alpha`, lockstep module tags — [docs/release/](docs/release/) — done
- **api-server** — the unified multi-protocol server — [docs/api-server/](docs/api-server/) — done
- **replica-forwarding** — forward to the replica leader on every transport — [docs/replica-forwarding/](docs/replica-forwarding/) — done
- **barrier-seal** — the at-rest barrier, strategies, per-node keys — [docs/barrier-seal/](docs/barrier-seal/) — done
- **shamir-custody** — Security Officers, M-of-N shares, rekey — [docs/shamir-custody/](docs/shamir-custody/) — done
- **ceremony** — the initialization ceremony and share lifecycle — [docs/ceremony/](docs/ceremony/) — done
- **firstboot** — system tenant, built-in roles, platform admin — [docs/firstboot/](docs/firstboot/) — done
- **trust-bootstrap** — the client-side CA trust command shape — [docs/trust-bootstrap/](docs/trust-bootstrap/) — done
- **certificate-authority** — client certificate issuance, export, revocation — [docs/certificate-authority/](docs/certificate-authority/) — done
- **trust-anchors** — per-tenant registered CAs — [docs/trust-anchors/](docs/trust-anchors/) — done
- **credentials** — named credentials, versioned rotation — [docs/credentials/](docs/credentials/) — done
- **key-wrapping** — the KEK provider seam, backup envelopes — [docs/key-wrapping/](docs/key-wrapping/) — done
- **tenant-dek** — the per-tenant DEK keyring — [docs/tenant-dek/](docs/tenant-dek/) — done
- **identity-rbac** — users, roles, the RBAC PDP — [docs/identity-rbac/](docs/identity-rbac/) — done
- **auth-factors** — the `Factor` framework, PIN custody — [docs/auth-factors/](docs/auth-factors/) — done
- **audit** — compliance audit logging and query — [docs/audit/](docs/audit/) — done
- **rate-limit** — per-permission throttling on every transport — [docs/rate-limit/](docs/rate-limit/) — done
- **attested-caller** — the attested-caller gate — [docs/attested-caller/](docs/attested-caller/) — done
- **multi-tenancy** — isolation ladder, quotas, per-tenant barriers — [docs/multi-tenancy/](docs/multi-tenancy/) — done
- **desktop-gui** — the Wails desktop application — [docs/desktop-gui/](docs/desktop-gui/) — done
- **typescript-sdk** — the `@qrdb/sdk` TypeScript client — [docs/typescript-sdk/](docs/typescript-sdk/) — done

## Now

- **cluster-verification** — the qrdb-3-node-proven gate: linearizability checker, nemesis and soak; rate nemesis; pinned port set; cluster suites green on main — [#669](../../issues/669), [#679](../../issues/679), [#671](../../issues/671), [#668](../../issues/668); PRs [#673](../../pulls/673), [#689](../../pulls/689), [#691](../../pulls/691), [#692](../../pulls/692), [#693](../../pulls/693); PR [#696](../../pulls/696), the #668 rebalance fix (refuse a plan whose placement records cannot be read): merged, done — folder arrives with #669 — in review
- **sealed-mode-serving** — adopt the go-quicraft v0.21.0 release candidate, with sealed-mode serving — [#659](../../issues/659); PR [#695](../../pulls/695) — [docs/barrier-seal/](docs/barrier-seal/) — in review
- **bounded-startup-read** — each startup read attempt bounded and a boot failure logged; go-quicraft `ErrReadIndexNotReady` retried until the startup deadline — [#668](../../issues/668); PR [#673](../../pulls/673) — [docs/clustering/](docs/clustering/) — in review
- **lease-reads** — lease reads documented as linearizable under the 1 % clock-rate bound — [#678](../../issues/678); PR [#681](../../pulls/681) — [docs/key-value/](docs/key-value/) — in review
- **auth-factor-claim** — conditional factor add; hard delete of a rolled-back add — [#655](../../issues/655), [#684](../../issues/684); PR [#681](../../pulls/681) — [docs/auth-factors/](docs/auth-factors/) — in review
- **protected-memory** — scoped borrow and constant-time `Equal`; `RetrieveCredential` and secret DTOs; key material across the `EncryptionBarrier`; unseal shares; zeroizable entity field — [#650](../../issues/650), [#22](../../issues/22), [#145](../../issues/145), [#79](../../issues/79), [#633](../../issues/633); PR [#651](../../pulls/651) — [docs/barrier-seal/](docs/barrier-seal/) — in review
- **storage-compare-and-swap** — `Storage.CompareAndSwap`; atomic file-engine guarded writes — [#649](../../issues/649), [#632](../../issues/632); PR [#651](../../pulls/651) — [docs/guarded-swap/](docs/guarded-swap/) — in review
- **request-context-dpop** — DPoP proof, method and URI in the request context — [#646](../../issues/646); PR [#651](../../pulls/651) — [docs/api-server/](docs/api-server/) — in review
- **fips-140-3** — nightly FIPS 140-3 job green and placed; clusterenroll on the P-256 handshake — [#618](../../issues/618), [#608](../../issues/608), [#20](../../issues/20), [#118](../../issues/118) — [docs/seal/fips140-mode.md](docs/seal/fips140-mode.md) — open
- **documentation** — ROADMAP names every milestone and folder; banners; RC spec scrub; agent spec home — [#657](../../issues/657), [#658](../../issues/658), [#635](../../issues/635), [#634](../../issues/634) — [docs/README.md](docs/README.md) — open

## Next

- **file-backend-encryption** — file backend encrypted at rest under the node barrier — [#630](../../issues/630); PR [#681](../../pulls/681) — [docs/storage-engines/](docs/storage-engines/) — in review
- **embedder-seams** — `sdk/go` exposes the seams embedders need — [#626](../../issues/626); PR [#681](../../pulls/681) — [docs/release/extension-seams.md](docs/release/extension-seams.md) — in review
- **strict-tenant-isolation** — each strict tenant gets a home store, partially implemented; per-node tenant barrier init — [#683](../../issues/683); PR [#651](../../pulls/651) — [docs/multi-tenancy/](docs/multi-tenancy/) — in review
- **numeric-index-ranges** — numeric RANGE plans order numerically — [#35](../../issues/35) — [docs/secondary-indexes/](docs/secondary-indexes/) — open
- **ci-actionlint** — `make lint` runs actionlint — [#636](../../issues/636); PR [#651](../../pulls/651) — [docs/release/](docs/release/) — in review
- **port-reservation-package** — decide the shared port-reservation package — [#306](../../issues/306) — no folder — open

## Later

- **identity-hydrator** — `WithIdentityHydrator`; the login Subject is the immutable user id — [#670](../../issues/670) — [docs/identity-rbac/](docs/identity-rbac/) — open
- **release-readiness** — release readiness for the public release — [#565](../../issues/565) — [docs/release/](docs/release/) — open
- **arm64-release** — linux-arm64 and arm64-fips release artifacts; the integration suite once on arm64 before the RC — [#702](../../issues/702) — [docs/release/](docs/release/) — open
- **seal-pause-resume** — seal pauses every shard the node hosts, the system shard included, through go-quicraft `Host.PauseShard` after the seal record commits and before the keys are wiped; unseal restores the keys, then `Host.ResumeShard`; `ErrShardPaused` answers Unavailable on every transport, never a shutdown — [docs/barrier-seal/](docs/barrier-seal/) — blocked: pending go-quicraft `shard-pause` (go-quicraft#353)

## Future

- **range-delete** — engine range delete for dropped databases — [#457](../../issues/457) — [docs/key-value/](docs/key-value/) — open
- **dead-code-triage** — unreachable symbols triaged, wired or deleted — [#350](../../issues/350) — [docs/release/](docs/release/) — open
- **barrier-passphrase-change** — a supported system-barrier passphrase change — [#631](../../issues/631) — [docs/barrier-seal/](docs/barrier-seal/) — open
- **lease-keepalive-latency** — no heavy keepalive tail — [#275](../../issues/275) — [docs/leases/](docs/leases/) — open
- **role-delete-cascade** — deleting a role removes it from every holder — [#697](../../issues/697) — [docs/identity-rbac/](docs/identity-rbac/) — open
- **clusterenroll-coverage** — `pkg/clusterenroll` coverage back to its baseline — [#652](../../issues/652) — [docs/clustering/](docs/clustering/) — open
- **lean-node-build** — opt-in `qrdb_*` tags for a lean embedded node — [#627](../../issues/627) — [docs/run-modes/](docs/run-modes/) — open
- **multi-model-databases** — a database as a multi-model container over one storage choice — [docs/databases/](docs/databases/) — open
- **object-storage-lsm** — the object-storage LSM segment layout — [docs/storage-engines/](docs/storage-engines/) — blocked: pending decision
- **volume-pool-tenant-moves** — tenant moves between volume pools, in phase 2 — [docs/volume-pools/](docs/volume-pools/) — open
- **sqlite-vfs** — go-qrdb as a SQLite backend through a VFS — [docs/future/sqlite-vfs.md](docs/future/sqlite-vfs.md) — open

## Dependencies

- Up: go-quicraft v0.20.0 — the Raft library go-qrdb embeds; multi-group Raft consensus and the QUIC Raft transport; the barrier interfaces and minimal barrier; `EpochBarrier`, `StrategyNone`, `StrategyPassphrase`, re-exported through `sdk/go` — clustering, barrier-seal; v0.21 is sealed-mode-serving ([#659](../../issues/659)) and also provides `ErrReadIndexNotReady` for bounded-startup-read ([#668](../../issues/668)).
- Up: go-enclave v0.8.0-alpha — the protected-memory primitives under `pkg/secure` — credentials, barrier-seal, protected-memory.
- Up: go-objstore v0.4.0-alpha — the ObjStore storage backend and backup destinations — storage-engines, backup-restore.
- Up: go-truststrap v0.6.0-alpha — the Noise bootstrap handler and session for cluster enrollment; the P-256 handshake ([#118](../../issues/118)) — clustering, fips-140-3.
- Up: go-codec v1.4.0 — the JSON, YAML, TOML, MsgPack, BSON, CBOR and ProtoBuf codecs — secondary-indexes, collections.
- Down: go-xkms — embeds go-qrdb as a library and adds hardware crypto, auth factors, KMS, CA, ACME and TPM policies as services through `ServiceModule`, so the go-qrdb client works unchanged against an xkms node; supplies hardware `Factor` and `SealStrategy` implementations through the `With*` options.
- Down: go-quicbaas — exposes the database services via go-qrdb and composes go-xkms on top, every go-qrdb seam filled by hardware crypto; delegates first boot through `sdk/go/node`.
