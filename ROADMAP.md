# go-qrdb roadmap

> **Status: REFERENCE** · Owner: Software Department · Last verified: 2026-11-20

## Mission

The cluster engine and database system for every project in the stack: embedded, standalone or Raft-clustered, on go-quicraft's multi-group Raft consensus and QUIC Raft transport. Its `sdk/go` and service interfaces are the base of go-xkms and go-quicbaas: the data plane, cluster formation, barrier and seal, Shamir custody, identity and the single RBAC PDP. One cluster engine and one datastore per deployment, tenants strictly isolated; boundaries in [docs/SPEC.md](docs/SPEC.md#boundaries).

## Milestones

- [Working fundamentally: Raft clustering and storage](../../milestone/28) — RC — projected 2026-10-21 16Z
- [Dev complete](../../milestone/12) — RC — projected 2026-10-22 10Z — criteria in [docs/SPEC.md](docs/SPEC.md#release-criteria)

## Complete

- **key-value** — KV, MVCC revisions, consistency levels, compaction — [docs/key-value/](docs/key-value/) — done
- **transactions** — CAS transactions, atomic batches, file import — [docs/transactions/](docs/transactions/) — done
- **guarded-swap** — `sdk.GuardedSwap` — [docs/guarded-swap/](docs/guarded-swap/) — done
- **storage-compare-and-swap** — `CompareAndSwap` on the `Storage` interface; atomic file-engine guarded writes — [#649](../../issues/649), [#632](../../issues/632), [docs/guarded-swap/](docs/guarded-swap/) — done
- **watch** — change streams with replay — [docs/watch/](docs/watch/) — done
- **leases** — TTL leases and cascade expiry — [docs/leases/](docs/leases/) — done
- **distributed-locks** — locks with fencing tokens — [docs/distributed-locks/](docs/distributed-locks/) — done
- **coordination** — the SDK coordination surface — [docs/coordination/](docs/coordination/) — done
- **secondary-indexes** — indexes, query planner, DAO aggregations — [docs/secondary-indexes/](docs/secondary-indexes/) — done
- **numeric-index-ranges** — numeric RANGE plans order numerically — [#35](../../issues/35), [docs/secondary-indexes/](docs/secondary-indexes/) — done
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
- **bounded-startup-read** — each startup wait of a restarted node bounded, retried and reported — [#668](../../issues/668), PR [#714](../../pulls/714), [docs/clustering/](docs/clustering/) — done
- **clusterenroll-coverage** — `pkg/clusterenroll` coverage back to its baseline — [#652](../../issues/652), [docs/clustering/](docs/clustering/) — done
- **run-modes** — standalone and Raft run modes — [docs/run-modes/](docs/run-modes/) — done
- **capabilities** — the effective runtime feature profile — [docs/capabilities/](docs/capabilities/) — done
- **configuration** — load order, validation, flag and YAML mapping — [docs/configuration/](docs/configuration/) — done
- **deployment** — host requirements — [docs/deployment/](docs/deployment/) — done
- **release** — `make release-alpha`, lockstep module tags — [docs/release/](docs/release/) — done
- **ci-actionlint** — `make lint` runs actionlint — [#636](../../issues/636), [docs/release/](docs/release/) — done
- **port-reservation-package** — the shared port-reservation package — [#306](../../issues/306), [docs/api-server/](docs/api-server/) — done
- **api-server** — the unified multi-protocol server — [docs/api-server/](docs/api-server/) — done
- **request-context-dpop** — DPoP proof, method and URI in the request context — [#646](../../issues/646), [docs/api-server/](docs/api-server/) — done
- **replica-forwarding** — forward to the replica leader on every transport — [docs/replica-forwarding/](docs/replica-forwarding/) — done
- **barrier-seal** — the at-rest barrier, strategies, per-node keys — [docs/barrier-seal/](docs/barrier-seal/) — done
- **sealed-mode-serving** — adopt the go-quicraft release candidate, with sealed-mode serving — [#659](../../issues/659), PR [#695](../../pulls/695), [docs/barrier-seal/](docs/barrier-seal/) — done
- **seal-pause-resume** — seal pauses every shard the node hosts; unseal resumes them — PR [#695](../../pulls/695), [docs/barrier-seal/](docs/barrier-seal/) — done
- **shamir-custody** — Security Officers, M-of-N shares, rekey — [docs/shamir-custody/](docs/shamir-custody/) — done
- **ceremony** — the initialization ceremony and share lifecycle — [docs/ceremony/](docs/ceremony/) — done
- **firstboot** — system tenant, built-in roles, platform admin — [docs/firstboot/](docs/firstboot/) — done
- **trust-bootstrap** — the client-side CA trust command shape — [docs/trust-bootstrap/](docs/trust-bootstrap/) — done
- **certificate-authority** — client certificate issuance, export, revocation — [docs/certificate-authority/](docs/certificate-authority/) — done
- **trust-anchors** — per-tenant registered CAs — [docs/trust-anchors/](docs/trust-anchors/) — done
- **key-wrapping** — the KEK provider seam, backup envelopes — [docs/key-wrapping/](docs/key-wrapping/) — done
- **tenant-dek** — the per-tenant DEK keyring — [docs/tenant-dek/](docs/tenant-dek/) — done
- **hardware-crypto-seams** — every key, signing and encryption need is a composer-supplied seam with a software default — [#912](../../issues/912), [docs/barrier-seal/](docs/barrier-seal/), [docs/key-wrapping/](docs/key-wrapping/), [docs/tenant-dek/](docs/tenant-dek/), [docs/certificate-authority/](docs/certificate-authority/), [docs/clustering/](docs/clustering/), [docs/volume-pools/](docs/volume-pools/), [docs/auth-factors/](docs/auth-factors/), [docs/release/extension-seams.md](docs/release/extension-seams.md) — done
- **identity-rbac** — users, roles, the RBAC PDP — [docs/identity-rbac/](docs/identity-rbac/) — done
- **identity-hydrator** — `WithIdentityHydrator`; the login Subject is the immutable user id — [#670](../../issues/670), [docs/identity-rbac/](docs/identity-rbac/) — done
- **embedded-explicit-identity** — `Node.ServiceContext` and `Node.DelegatedContext`: an embedded call carries an explicitly presented identity, a tenant delegation verified on every call — [#873](../../issues/873), [docs/identity-rbac/](docs/identity-rbac/) — done
- **user-password-self-scope** — the User role holds self-scoped `passwords:write` and `passwords:delete` — [docs/auth/](docs/auth/) — done
- **role-delete-cascade** — deleting a role removes it from every holder — [#697](../../issues/697), [docs/identity-rbac/](docs/identity-rbac/) — done
- **sdk-abac-condition-parser** — sdk/go type and constructor aliases for the ABAC condition parser, so consumers validate conditions with the server's own parser — [docs/auth/](docs/auth/) — done
- **auth-factors** — the `Factor` framework, PIN custody — [docs/auth-factors/](docs/auth-factors/) — done
- **audit** — compliance audit logging and query — [docs/audit/](docs/audit/) — done
- **rate-limit** — per-permission throttling on every transport — [docs/rate-limit/](docs/rate-limit/) — done
- **attested-caller** — the attested-caller gate — [docs/attested-caller/](docs/attested-caller/) — done
- **multi-tenancy** — isolation ladder, quotas, per-tenant barriers — [docs/multi-tenancy/](docs/multi-tenancy/) — done
- **typescript-sdk** — the `@qrdb/sdk` TypeScript client — [docs/typescript-sdk/](docs/typescript-sdk/) — done
- **protected-memory** — `RetrieveCredential` and secret DTOs; key material across the `EncryptionBarrier`; unseal shares; zeroizable entity field — [#22](../../issues/22), [#145](../../issues/145), [#79](../../issues/79), [#633](../../issues/633), PR [#681](../../pulls/681), [docs/barrier-seal/](docs/barrier-seal/), [docs/credentials/](docs/credentials/) — done
- **tenant-undo-id-reuse** — an undone tenant create leaves an undone tombstone; the ID is reusable once every serving node has acknowledged its cleanup — [#774](../../issues/774), [docs/multi-tenancy/](docs/multi-tenancy/) — done
- **identity-tenant-owner** — a typed TenantOwner on transport.Identity, set by the identity hydrator from the role store; the field consumers authorize on — [docs/identity-rbac/](docs/identity-rbac/) — done
- **rbac-list-own-permissions** — RBACClient.ListOwnPermissions lists the authenticated caller's own effective permissions, authentication only, on every transport and the CLI — [docs/identity-rbac/](docs/identity-rbac/) — done
- **iam-policy-enabled-presence** — a presence-tracked enabled field on ABAC policies (absent = enabled, false = skipped), returned unchanged on every read — [docs/auth/](docs/auth/) — done
- **abac-condition-value-quoting** — RFC 8259 JSON-string quoting for ABAC condition values: a quoted value is one token, unquoted values unchanged, malformed quotes rejected — [docs/auth/](docs/auth/) — done
- **range-delete** — engine range delete for dropped databases — [#457](../../issues/457), [docs/key-value/](docs/key-value/) — done
- **dead-code-triage** — unreachable symbols triaged, wired or deleted — [#350](../../issues/350), [docs/release/](docs/release/) — done
- **documentation** — ROADMAP names every milestone and folder; document banners; RC spec scrub; agent spec home — [#657](../../issues/657), [#658](../../issues/658), [#635](../../issues/635), [#634](../../issues/634), [docs/README.md](docs/README.md) — done
- **default-roles** — shipped default roles every tenant starts with; they carry the OTA activate and confirm permissions — [#737](../../issues/737), [docs/identity-rbac/](docs/identity-rbac/), [docs/firstboot/](docs/firstboot/) — done
- **device-onboarding-permission** — device onboarding is a tenant-scoped permission, not a role; each tenant onboards its own devices — [#737](../../issues/737), [docs/identity-rbac/](docs/identity-rbac/) — done
- **ota-activate-confirm-permissions** — OTA activate and confirm are tenant-scoped permissions, not roles — [#737](../../issues/737), [docs/identity-rbac/](docs/identity-rbac/) — done
- **spiffe-trust-domain-tenant** — the tenant is the SPIFFE trust domain — [docs/auth/](docs/auth/) — done
- **embedder-seams** — `sdk/go` exposes the seams embedders need — [#626](../../issues/626), PR [#681](../../pulls/681), [docs/release/extension-seams.md](docs/release/extension-seams.md) — done
- **lease-reads** — lease reads documented as linearizable under the 1 % clock-rate bound — [#678](../../issues/678), PR [#681](../../pulls/681), [docs/key-value/](docs/key-value/) — done
- **auth-factor-claim** — conditional factor add; hard delete of a rolled-back add — [#655](../../issues/655), [#684](../../issues/684), PR [#681](../../pulls/681), [docs/auth-factors/](docs/auth-factors/) — done
- **storage-latency-observability** — go-quicraft host metrics on /metrics, storage syncs timed, a slow-sync warning — [#742](../../issues/742), [docs/api-server/](docs/api-server/) — done
- **rolling-enable-encryption** — `cluster enable-encryption` recycles one node at a time from a replicated per-node step record, behind a cluster feature gate — [#775](../../issues/775), [docs/barrier-seal/](docs/barrier-seal/) — done
- **identity-ou-spiffe-agree** — tenant identity requires both a subject OU and a SPIFFE ID SAN that agree; no fallbacks; every issuer sets both — [#777](../../issues/777), [docs/identity-rbac/](docs/identity-rbac/), [docs/firstboot/](docs/firstboot/) — done
- **optional-auth-route** — a `HandleOptional` route mode on `ModuleHTTPRegistrar`: a presented credential runs the auth chain, none admits an anonymous caller, an invalid one is rejected; consumer go-quicbaas #1457 — [docs/api-server/](docs/api-server/) — done
- **file-backend-encryption** — file backend encrypted at rest under the node barrier — [#630](../../issues/630), PR [#681](../../pulls/681), [docs/storage-engines/](docs/storage-engines/) — done
- **lease-keepalive-latency** — no heavy keepalive tail — [#275](../../issues/275), PR [#681](../../pulls/681), [docs/leases/](docs/leases/) — done
- **tenant-metadata-description** — `UpdateTenantRequest` and `TenantInfo` carry `Metadata` and `Description`, persisted in the tenant record, on every transport and the Go SDK; consumer go-quicbaas #1518 — [docs/multi-tenancy/api.md](docs/multi-tenancy/api.md) — done
- **batch-revision** — `transport.BatchResponse.Revision` is the one MVCC revision a batch committed at, on every transport — [#797](../../issues/797), [docs/transactions/](docs/transactions/) — done
- **sdk-embedded-index-service** — sdk/go type and constructor aliases for the index service — [#928](../../issues/928), [docs/secondary-indexes/](docs/secondary-indexes/) — done
- **sdk-compaction-policy-reexport** — sdk/go type, constant and value aliases for the compaction policy — [#927](../../issues/927), [docs/key-value/](docs/key-value/) — done
- **sdk-rbac-authorization-reexport** — sdk/go aliases for the surfaces a layer above authorizes its own requests with — [docs/identity-rbac/](docs/identity-rbac/), [docs/audit/](docs/audit/) — done
- **barrier-passphrase-change** — a supported system-barrier passphrase change on every transport, the CLI, the TypeScript SDK and `Node.ChangePassphrase` — [#631](../../issues/631), [docs/barrier-seal/](docs/barrier-seal/) — done
- **fsync-stall-leadership** — leadership survives a bounded follower WAL fsync stall; adopts go-quicraft's heartbeat-under-fsync seam — [docs/clustering/](docs/clustering/) — done
- **lean-node-build** — opt-in `qrdb_*` tags for a lean embedded node — [#627](../../issues/627), [docs/run-modes/](docs/run-modes/) — done
- **cluster-verification** — the qrdb-3-node-proven gate passes on the three-node cluster; cluster suites green on main — [#668](../../issues/668), [#671](../../issues/671), [#679](../../issues/679), [docs/cluster-verification/](docs/cluster-verification/) — done
- **fips-140-3** — the FIPS 140-3 job (ci.yml `fips`) green and measured; clusterenroll on the P-256 handshake — [#618](../../issues/618), [#608](../../issues/608), [#20](../../issues/20), [#118](../../issues/118), [docs/seal/fips140-mode.md](docs/seal/fips140-mode.md), [docs/clustering/architecture.md](docs/clustering/architecture.md#member-side-csr-join-key-a-cluster-ca) — done
- **tenant-delegation** — a tenant-controlled, revocable, audited delegation for an embedder's server-owned work in that tenant: a standing delegation policy, `Node.MintDelegation` and `Node.DelegatedContext`; consumer go-quicbaas #1814 — [#1187](../../issues/1187), [docs/identity-rbac/](docs/identity-rbac/), [docs/certificate-authority/](docs/certificate-authority/) — done
- **invitation-reissue-supersedes** — re-inviting a pending account supersedes every earlier token in one guarded write (`invite.Store.Reissue`); activation recorded apart from redemption — [#1196](../../issues/1196), [docs/identity-rbac/](docs/identity-rbac/) — done
- **role-grant-rule** — a grantor may grant only permissions it holds (`*role.GrantExceedsHolderError`) — [#1279](../../issues/1279), [docs/identity-rbac/architecture.md](docs/identity-rbac/architecture.md#the-grant-rule) — done
- **tenant-barrier-signed-init** — a two-phase signed tenant barrier init for a first admin's key that signs away from the node; consumer go-quicbaas #1814 — [#1213](../../issues/1213), PR [#1402](../../pulls/1402), [docs/multi-tenancy/api.md](docs/multi-tenancy/api.md#signed-barrier-init-a-first-admins-key-that-signs-elsewhere) — done
- **seal-quiesce** — sealing an at-rest barrier quiesces the encrypted stores under it, then refuses new work with `ErrBarrierSealed` — PR [#1403](../../pulls/1403), [docs/barrier-seal/](docs/barrier-seal/) — done
- **kv-versions** — a caller reads a key's version, the number a transaction's `version` compare reads; transaction branches answer gets and ranges inside the transaction — [#1212](../../issues/1212), [docs/key-value/](docs/key-value/) — done
- **iam-exposure** — the IAM surface go-xkms needs: device-share permissions, roles only from the bound user record, sdk re-exports of devices:onboard, HoldsSystemRole and the keys/certs/passwords/ssh/pin/piv permissions, the object-owner rule, Unix-socket peer-credential identity, a shared-password override permission, a tenant status filter; consumer go-xkms — [#1651](../../issues/1651), [docs/identity-rbac/](docs/identity-rbac/) — done
- **verifiable-log** — an append-only Merkle-tree database type per tenant with proofs and owner-signed tree heads — [#1613](../../issues/1613), PR [#1655](../../pulls/1655), [docs/databases/](docs/databases/), [docs/plans/1613-verifiable-log.md](docs/plans/1613-verifiable-log.md) — done

## Now

- **strict-tenant-isolation** — a home store per strict tenant; per-node tenant barrier init — [#683](../../issues/683), [docs/multi-tenancy/](docs/multi-tenancy/) — open
- **arm64-release** — linux-arm64 and arm64-fips release artifacts; the integration suite once on arm64 before the RC — [#702](../../issues/702), [docs/release/](docs/release/) — in progress
- **separation-of-duties** — SO separation of duties is enforced in compliance mode only; the optional SO holds in both postures — [#1128](../../issues/1128), [#1042](../../issues/1042), [docs/identity-rbac/](docs/identity-rbac/) — in progress
- **desktop-gui** — the Wails desktop application — [docs/desktop-gui/](docs/desktop-gui/) — in progress
- **webtransport-session** — WebTransport sessions (draft-ietf-webtrans-http3-16) on the REST HTTP/3 listener behind an authenticated, tenant-bound CONNECT route — [#1244](../../issues/1244), [docs/api-server/](docs/api-server/) — in progress
- **credentials** — named credentials, versioned rotation; scheduled rotation policies, their transport and CLI verbs, whole-store moves; consumer go-quicbaas — [#1201](../../issues/1201), [#1185](../../issues/1185), [docs/credentials/](docs/credentials/) — in progress

## Next


## Later

- **webtransport-upstream** — drop the patched in-tree webtransport-go copy once an upstream release on go-qrdb's quic-go carries its six fixes — [#1688](../../issues/1688), [#1244](../../issues/1244), [docs/api-server/](docs/api-server/) — open
- **release-readiness** — release readiness for the public release — [#565](../../issues/565), [docs/release/](docs/release/) — open
- **object-storage-credential-handle** — the object-storage credential crosses the seam as the go-enclave protected-memory credential handle; adopts go-objstore#16 — [#1143](../../issues/1143), [docs/storage-engines/](docs/storage-engines/) — open
- **rolling-upgrades** — mixed-version rolling upgrades between adjacent releases: wire-version negotiation, typed refusal of version gaps, a cross-release compatibility matrix (N to N+1 both ways, mid-upgrade cluster) — [#1628](../../issues/1628), PR [#1638](../../pulls/1638), [docs/rolling-upgrades/](docs/rolling-upgrades/) — in review
- **object-storage-provider** — object storage is configurable as a backend storage provider of go-qrdb's storage API, the `pkg/storage.Storage` seam; go-objstore supplies the backends (owner ruling 228(3)) — [docs/storage-engines/](docs/storage-engines/) — open
- **object-storage-provider-seam** — the provider side of the object-storage seam: go-quicbaas configures the provider per go-objstore backend; go-qrdb designs no backend of its own (owner ruling 228(2), 228(3)) — [docs/storage-engines/](docs/storage-engines/) — open

## Future

- **multi-model-databases** — a database as a multi-model container over one storage choice — [docs/databases/](docs/databases/) — open
- **object-storage-lsm** — the object-storage LSM segment layout — [docs/storage-engines/](docs/storage-engines/) — blocked: pending decision
- **volume-pool-tenant-moves** — tenant moves between volume pools, in phase 2 — [docs/volume-pools/](docs/volume-pools/) — open
- **sqlite-vfs** — go-qrdb as a SQLite backend through a VFS — [docs/sqlite-vfs/](docs/sqlite-vfs/) — open

## Dependencies

- Up: go-quicraft v0.24.2-alpha ([#944](../../issues/944)) — Raft consensus, QUIC Raft transport, `EpochBarrier` and the L1 strategies — clustering, barrier-seal, sealed-mode-serving.
- Up: go-enclave v0.10.0-alpha — protected-memory primitives under `pkg/secure` — credentials, barrier-seal, protected-memory.
- Up: go-objstore v0.7.0-alpha — the go-objstore storage backend and backup destinations — storage-engines, backup-restore.
- Up: go-truststrap v0.7.0-alpha — the cluster-enrollment bootstrap; the P-256 handshake ([#118](../../issues/118)) — clustering, fips-140-3.
- Up: go-codec v1.4.0 — the codecs — secondary-indexes, collections.
- standards-watch: draft-ietf-hpke-pq pinned at -05 — pkg/keydelivery (provisional: reached through Go's crypto/hpke, whose implemented revision is unconfirmed; docs/reference/README.md)
- standards-watch: draft-ietf-webtrans-http3 pinned at -16 — pkg/api/rest (through the patched webtransport-go v0.13.0 copy in internal/webtransport; the legacy SETTINGS it sends are recorded as deviations in docs/reference/README.md)
- Down: go-xkms — embeds go-qrdb and adds its services through `ServiceModule`; supplies hardware `Factor` and `SealStrategy` implementations.
- Down: go-quicbaas — exposes the database services via go-qrdb and composes go-xkms; delegates first boot through `sdk/go/node`.
