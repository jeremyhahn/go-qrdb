# go-qrdb roadmap

> **Status: REFERENCE** · Owner: Software Department · Last verified: 2026-10-16

## Mission

The cluster engine and database system for every project in the stack: embedded, standalone or Raft-clustered, on go-quicraft's multi-group Raft consensus and QUIC Raft transport. Its `sdk/go` and service interfaces are the base of go-xkms and go-quicbaas: the data plane, cluster formation, barrier and seal, Shamir custody, identity and the single RBAC PDP. One cluster engine and one datastore per deployment, tenants strictly isolated; boundaries in [docs/SPEC.md](docs/SPEC.md#boundaries).

## Milestones

- [Working fundamentally: Raft clustering and storage](../../milestone/28) — RC — projected 2026-10-21 16Z
- [Dev complete](../../milestone/12) — RC — projected 2026-10-22 10Z — criteria in [docs/SPEC.md](docs/SPEC.md#release-criteria)

## Complete

- **key-value** — KV, MVCC revisions, consistency levels, compaction — [docs/key-value/](docs/key-value/) — done
- **transactions** — CAS transactions, atomic batches, file import — [docs/transactions/](docs/transactions/) — done
- **guarded-swap** — `sdk.GuardedSwap` — [docs/guarded-swap/](docs/guarded-swap/) — done
- **storage-compare-and-swap** — `CompareAndSwap` on the `Storage` interface; atomic file-engine guarded writes — [#649](../../issues/649), [#632](../../issues/632) — done
- **watch** — change streams with replay — [docs/watch/](docs/watch/) — done
- **leases** — TTL leases and cascade expiry — [docs/leases/](docs/leases/) — done
- **distributed-locks** — locks with fencing tokens — [docs/distributed-locks/](docs/distributed-locks/) — done
- **coordination** — the SDK coordination surface — [docs/coordination/](docs/coordination/) — done
- **secondary-indexes** — indexes, query planner, DAO aggregations — [docs/secondary-indexes/](docs/secondary-indexes/) — done
- **numeric-index-ranges** — numeric RANGE plans order numerically — [#35](../../issues/35) — done
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
- **storage-engines** — file, memory, pebble, namespace and go-objstore backends, selected by configuration — [docs/storage-engines/](docs/storage-engines/) — done
- **volume-pools** — named volume pools — [docs/volume-pools/](docs/volume-pools/) — done
- **backup-restore** — snapshots, verification, selective restore — [docs/backup-restore/](docs/backup-restore/) — done
- **clustering** — discovery, formation, membership, rebalance — [docs/clustering/](docs/clustering/) — done
- **bounded-startup-read** — each startup wait of a restarted node bounded, retried and reported — [#668](../../issues/668), PR [#714](../../pulls/714) — done
- **clusterenroll-coverage** — `pkg/clusterenroll` coverage back to its baseline — [#652](../../issues/652) — done
- **run-modes** — standalone and Raft run modes — [docs/run-modes/](docs/run-modes/) — done
- **capabilities** — the effective runtime feature profile — [docs/capabilities/](docs/capabilities/) — done
- **configuration** — load order, validation, flag and YAML mapping — [docs/configuration/](docs/configuration/) — done
- **deployment** — host requirements — [docs/deployment/](docs/deployment/) — done
- **release** — `make release-alpha`, lockstep module tags — [docs/release/](docs/release/) — done
- **ci-actionlint** — `make lint` runs actionlint — [#636](../../issues/636) — done
- **port-reservation-package** — the shared port-reservation package — [#306](../../issues/306) — done
- **api-server** — the unified multi-protocol server — [docs/api-server/](docs/api-server/) — done
- **request-context-dpop** — DPoP proof, method and URI in the request context — [#646](../../issues/646) — done
- **replica-forwarding** — forward to the replica leader on every transport — [docs/replica-forwarding/](docs/replica-forwarding/) — done
- **barrier-seal** — the at-rest barrier, strategies, per-node keys — [docs/barrier-seal/](docs/barrier-seal/) — done
- **sealed-mode-serving** — adopt the go-quicraft release candidate, with sealed-mode serving — [#659](../../issues/659), PR [#695](../../pulls/695) — done
- **seal-pause-resume** — seal pauses every shard the node hosts; unseal resumes them — PR [#695](../../pulls/695) — done
- **shamir-custody** — Security Officers, M-of-N shares, rekey — [docs/shamir-custody/](docs/shamir-custody/) — done
- **ceremony** — the initialization ceremony and share lifecycle — [docs/ceremony/](docs/ceremony/) — done
- **firstboot** — system tenant, built-in roles, platform admin — [docs/firstboot/](docs/firstboot/) — done
- **trust-bootstrap** — the client-side CA trust command shape — [docs/trust-bootstrap/](docs/trust-bootstrap/) — done
- **certificate-authority** — client certificate issuance, export, revocation — [docs/certificate-authority/](docs/certificate-authority/) — done
- **trust-anchors** — per-tenant registered CAs — [docs/trust-anchors/](docs/trust-anchors/) — done
- **credentials** — named credentials, versioned rotation — [docs/credentials/](docs/credentials/) — done
- **key-wrapping** — the KEK provider seam, backup envelopes — [docs/key-wrapping/](docs/key-wrapping/) — done
- **tenant-dek** — the per-tenant DEK keyring — [docs/tenant-dek/](docs/tenant-dek/) — done
- **hardware-crypto-seams** — every key, signing and encryption need is a composer-supplied seam with a software default — [#912](../../issues/912) — done
- **identity-rbac** — users, roles, the RBAC PDP — [docs/identity-rbac/](docs/identity-rbac/) — done
- **identity-hydrator** — `WithIdentityHydrator`; the login Subject is the immutable user id — [#670](../../issues/670) — done
- **embedded-explicit-identity** — `Node.ServiceContext` and `Node.DelegatedContext`: an embedded call carries an explicitly presented identity, a tenant delegation verified on every call — [#873](../../issues/873) — done
- **user-password-self-scope** — the User role holds self-scoped `passwords:write` and `passwords:delete` — [docs/auth/](docs/auth/) — done
- **role-delete-cascade** — deleting a role removes it from every holder — [#697](../../issues/697) — done
- **sdk-abac-condition-parser** — sdk/go type and constructor aliases for the ABAC condition parser, so consumers validate conditions with the server's own parser — [docs/auth/](docs/auth/) — done
- **auth-factors** — the `Factor` framework, PIN custody — [docs/auth-factors/](docs/auth-factors/) — done
- **audit** — compliance audit logging and query — [docs/audit/](docs/audit/) — done
- **rate-limit** — per-permission throttling on every transport — [docs/rate-limit/](docs/rate-limit/) — done
- **attested-caller** — the attested-caller gate — [docs/attested-caller/](docs/attested-caller/) — done
- **multi-tenancy** — isolation ladder, quotas, per-tenant barriers — [docs/multi-tenancy/](docs/multi-tenancy/) — done
- **desktop-gui** — the Wails desktop application — [docs/desktop-gui/](docs/desktop-gui/) — done
- **typescript-sdk** — the `@qrdb/sdk` TypeScript client — [docs/typescript-sdk/](docs/typescript-sdk/) — done
- **protected-memory** — `RetrieveCredential` and secret DTOs; key material across the `EncryptionBarrier`; unseal shares; zeroizable entity field — [#22](../../issues/22), [#145](../../issues/145), [#79](../../issues/79), [#633](../../issues/633), PR [#681](../../pulls/681) — done
- **tenant-undo-id-reuse** — an undone tenant create leaves an undone tombstone; the ID is reusable once every serving node has acknowledged its cleanup — [#774](../../issues/774) — done
- **identity-tenant-owner** — a typed TenantOwner on transport.Identity, set by the identity hydrator from the role store; the field consumers authorize on — [docs/identity-rbac/](docs/identity-rbac/) — done
- **rbac-list-own-permissions** — RBACClient.ListOwnPermissions lists the authenticated caller's own effective permissions, authentication only, on every transport and the CLI — [docs/identity-rbac/](docs/identity-rbac/) — done
- **iam-policy-enabled-presence** — a presence-tracked enabled field on ABAC policies (absent = enabled, false = skipped), returned unchanged on every read — [docs/auth/](docs/auth/) — done
- **abac-condition-value-quoting** — RFC 8259 JSON-string quoting for ABAC condition values: a quoted value is one token, unquoted values unchanged, malformed quotes rejected — [docs/auth/](docs/auth/) — done
- **range-delete** — engine range delete for dropped databases — [#457](../../issues/457) — done
- **dead-code-triage** — unreachable symbols triaged, wired or deleted — [#350](../../issues/350) — done
- **default-roles** — shipped default roles every tenant starts with; they carry the OTA activate and confirm permissions — [#737](../../issues/737) — done
- **device-onboarding-permission** — device onboarding is a tenant-scoped permission, not a role; each tenant onboards its own devices — [#737](../../issues/737) — done
- **ota-activate-confirm-permissions** — OTA activate and confirm are tenant-scoped permissions, not roles — [#737](../../issues/737) — done
- **spiffe-trust-domain-tenant** — the tenant is the SPIFFE trust domain — [docs/auth/](docs/auth/) — done
- **embedder-seams** — `sdk/go` exposes the seams embedders need — [#626](../../issues/626), PR [#681](../../pulls/681) — done

## Now

- **cluster-verification** — the qrdb-3-node-proven gate passes on the three-node cluster; cluster suites green on main — [#668](../../issues/668), [#671](../../issues/671), [#679](../../issues/679) — blocked: the pinned port set (#671) and the rate nemesis (#679)
- **strict-tenant-isolation** — a home store per strict tenant; per-node tenant barrier init — [#683](../../issues/683) — open
- **lease-reads** — lease reads documented as linearizable under the 1 % clock-rate bound — [#678](../../issues/678), PR [#681](../../pulls/681) — open
- **auth-factor-claim** — conditional factor add; hard delete of a rolled-back add — [#655](../../issues/655), [#684](../../issues/684), PR [#681](../../pulls/681) — open
- **storage-latency-observability** — go-quicraft host metrics on /metrics, storage syncs timed, a slow-sync warning — [#742](../../issues/742) — done
- **rolling-enable-encryption** — `cluster enable-encryption` recycles one node at a time from a replicated per-node step record, behind a cluster feature gate — [#775](../../issues/775) — done
- **identity-ou-spiffe-agree** — tenant identity requires both a subject OU and a SPIFFE ID SAN that agree; no fallbacks; every issuer sets both — [#777](../../issues/777) — done
- **optional-auth-route** — a `HandleOptional` route mode on `ModuleHTTPRegistrar`: a presented credential runs the auth chain, none admits an anonymous caller, an invalid one is rejected; consumer go-quicbaas #1457 — [docs/api-server/](docs/api-server/) — done
- **arm64-release** — linux-arm64 and arm64-fips release artifacts; the integration suite once on arm64 before the RC — [#702](../../issues/702) — in progress
- **separation-of-duties** — SO separation of duties is enforced in compliance mode only; the optional SO holds in both postures — [#1128](../../issues/1128), [#1042](../../issues/1042) — in progress

## Next

- **fips-140-3** — the FIPS 140-3 job (ci.yml `fips`) green and measured; clusterenroll on the P-256 handshake — [#618](../../issues/618), [#608](../../issues/608), [#20](../../issues/20), [#118](../../issues/118) — open
- **documentation** — ROADMAP names every milestone and folder; RC spec scrub; agent spec home — [#657](../../issues/657), [#658](../../issues/658), [#634](../../issues/634) — open
- **file-backend-encryption** — file backend encrypted at rest under the node barrier — [#630](../../issues/630), PR [#681](../../pulls/681) — open
- **lease-keepalive-latency** — no heavy keepalive tail — [#275](../../issues/275), PR [#681](../../pulls/681) — open
- **verifiable-log** — an append-only Merkle-tree database type per tenant with proofs and owner-signed tree heads — PR [#729](../../pulls/729) — open

## Later

- **release-readiness** — release readiness for the public release — [#565](../../issues/565) — open
- **fsync-stall-leadership** — leadership survives a bounded follower WAL fsync stall; adopts go-quicraft's heartbeat-under-fsync seam — [docs/clustering/](docs/clustering/) — open
- **tenant-metadata-description** — `UpdateTenantRequest` and `TenantInfo` carry `Metadata` and `Description`, persisted in the tenant record, on every transport and the Go SDK; consumer go-quicbaas #1518 — [docs/multi-tenancy/api.md](docs/multi-tenancy/api.md) — done

## Future

- **barrier-passphrase-change** — a supported system-barrier passphrase change — [#631](../../issues/631) — open
- **lean-node-build** — opt-in `qrdb_*` tags for a lean embedded node — [#627](../../issues/627) — open
- **multi-model-databases** — a database as a multi-model container over one storage choice — [docs/databases/](docs/databases/) — open
- **object-storage-lsm** — the object-storage LSM segment layout — [docs/storage-engines/](docs/storage-engines/) — blocked: pending decision
- **volume-pool-tenant-moves** — tenant moves between volume pools, in phase 2 — [docs/volume-pools/](docs/volume-pools/) — open
- **sqlite-vfs** — go-qrdb as a SQLite backend through a VFS — [docs/sqlite-vfs/](docs/sqlite-vfs/) — open

## Dependencies

- Up: go-quicraft v0.22.0-alpha ([#659](../../issues/659)) — Raft consensus, QUIC Raft transport, `EpochBarrier` and the L1 strategies — clustering, barrier-seal, sealed-mode-serving.
- Up: go-enclave v0.8.0-alpha — protected-memory primitives under `pkg/secure` — credentials, barrier-seal, protected-memory.
- Up: go-objstore v0.4.0-alpha — the go-objstore storage backend and backup destinations — storage-engines, backup-restore.
- Up: go-truststrap v0.6.0-alpha — the cluster-enrollment bootstrap; the P-256 handshake ([#118](../../issues/118)) — clustering, fips-140-3.
- Up: go-codec v1.4.0 — the codecs — secondary-indexes, collections.
- standards-watch: draft-ietf-hpke-pq pinned at -05 — pkg/keydelivery (provisional: reached through Go's crypto/hpke, whose implemented revision is unconfirmed; docs/reference/README.md)
- Down: go-xkms — embeds go-qrdb and adds its services through `ServiceModule`; supplies hardware `Factor` and `SealStrategy` implementations.
- Down: go-quicbaas — exposes the database services via go-qrdb and composes go-xkms; delegates first boot through `sdk/go/node`.
