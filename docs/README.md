# QRDB Documentation

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-13
> Index of the documentation tree.

<!--
Copyright 2026 Jeremy Hahn

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

![QuicRaft DB Logo](../assets/logo.svg)

> **Start with the [Glossary](GLOSSARY.md)** — the standard terminology for run
> modes, storage backends, state machines and engines. These terms are easy to
> conflate and every other document assumes the glossary's usage.

## How this documentation is organised

**Every feature has a folder holding five documents**, and together they are
that feature's documentation:

```
docs/<feature>/
    architecture.md     design, component boundaries, data flow, rejected alternatives
    engineering.md      implementation contracts, invariants, errors, tests
    cli.md              every verb and flag, with examples
    api.md              every capability, on every transport
    user.md             task-oriented usage
```

**The feature folder is the requirements contract.** An agent picking up work on
a feature reads its folder before it reads code. Where a document contradicts
the code, the document is wrong and gets fixed — never the reverse.

## Features

### Data plane

| Feature | What it covers |
|---|---|
| [key-value](key-value/) | KV operations, MVCC revisions, consistency levels, write modes, compaction, selective restore |
| [transactions](transactions/) | compare-and-swap transactions, atomic batches, file import |
| [guarded-swap](guarded-swap/) | `sdk.GuardedSwap` — the optimistic compare-and-set loop over the transaction plane |
| [watch](watch/) | change streams, replay from a revision |
| [leases](leases/) | TTL leases and cascade expiry |
| [distributed-locks](distributed-locks/) | locks with fencing tokens, reentrancy |
| [coordination](coordination/) | the SDK coordination surface — lock/lease/CAS/watch, the acknowledged-deadline ownership model |
| [secondary-indexes](secondary-indexes/) | indexes, compound indexes, the query planner, DAO aggregations |
| [full-text-search](full-text-search/) | inverted index, analyzer, tokenizer *(build tag `qrdb_fts`)* |
| [collections](collections/) | document collections and ID strategies |
| [sql](sql/) | MySQL and PostgreSQL wire protocols, relational tables, transactions, privileges |
| [row-locking](row-locking/) | optimistic vs pessimistic SQL concurrency, `FOR UPDATE` / `FOR SHARE`, the replicated lock table, deadlock detection |
| [vectors](vectors/) | vector namespaces, k-NN and hybrid search, index maintenance *(`qrdb_vector`)* |
| [time-series](time-series/) | metrics, tags, aggregation, retention *(`qrdb_ts`)* |
| [embeddings](embeddings/) | text→vector provider proxy with caching |
| [rag](rag/) | chunking, ingestion, semantic retrieval |
| [etcd-compatibility](etcd-compatibility/) | per-database etcd v3 endpoints *(`qrdb_etcd`)* |

### Storage and operations

| Feature | What it covers |
|---|---|
| [databases](databases/) | multi-database lifecycle, per-database protocol, raft and barrier config |
| [storage-engines](storage-engines/) | the `Engine` interface, file/memory/pebble/namespace, volume pools; object-storage LSM target design *(ruled, not yet implemented)* |
| [volume-pools](volume-pools/) | *(proposed, awaiting sign-off)* named volume pools: per-node mounts, tenant placement, entitlements and capacity, management, the `VolumeProvider` seam |
| [backup-restore](backup-restore/) | shard snapshots, verification, selective restore |
| [clustering](clustering/) | peer discovery, cluster formation, membership, node admin, rebalance |
| [cluster-verification](cluster-verification/) | *(draft, build in progress)* what "Raft clustering and storage proven" means: the gate and exit criteria, workload, ledger, history, nemesis faults, the soak and its evidence bundle, `make cluster-proof` |
| [run-modes](run-modes/) | standalone vs raft, the three capability gates |
| [capabilities](capabilities/) | the effective runtime feature profile: build tags, run mode and configuration |
| [configuration](configuration/) | load order, validation, the flag/YAML mapping |
| [deployment](deployment/) | host requirements — locked memory, container and systemd caps, the listener and firewall table |
| [release](release/) | cutting a release: `make release-alpha`, VERSION as the release record, lockstep module tags, the Release workflow's post-publish validation |
| [api-server](api-server/) | the unified multi-protocol server and the `ServiceModule` seam |
| [replica-forwarding](replica-forwarding/) | a node that holds no replica of a database forwards the request to its replica leader, on the caller's behalf, on REST, gRPC, QUIC, MCP and embedded; Unix is the documented coverage gap |

### Security and identity

| Feature | What it covers |
|---|---|
| [barrier-seal](barrier-seal/) | the at-rest barrier, strategies, the strategy registry, enable-encryption |
| [shamir-custody](shamir-custody/) | Security Officers, M-of-N shares, rekey, recovery keys, root tokens |
| [ceremony](ceremony/) | the initialization ceremony state machine and share lifecycle |
| [firstboot](firstboot/) | system tenant, built-in roles, the platform admin, admin credential paths |
| [trust-bootstrap](trust-bootstrap/) | client-side CA trust establishment *(command shape only — see its docs)* |
| [certificate-authority](certificate-authority/) | client certificate issuance, export, revocation |
| [trust-anchors](trust-anchors/) | per-tenant registered CA certificates for bring-your-own certificate enrollment |
| [credentials](credentials/) | named credentials, versioned rotation, secret resolution |
| [key-wrapping](key-wrapping/) | the KEK provider seam (`keywrap.Provider`), the software implementation, the registry, backup's envelope encryption |
| [tenant-dek](tenant-dek/) | the per-tenant versioned data encryption keyring: encrypt, decrypt, rewrap, rotate, retire |
| [identity-rbac](identity-rbac/) | users, roles, the permission vocabulary, the RBAC PDP |
| [auth-factors](auth-factors/) | the `Factor` framework, the N-factor model, PIN custody |
| [audit](audit/) | compliance-grade audit logging and query |
| [rate-limit](rate-limit/) | per-permission request throttling, enforced identically across every transport |
| [attested-caller](attested-caller/) | the attested-caller gate: admit a gated operation only from a peer whose measured state is currently acceptable |
| [multi-tenancy](multi-tenancy/) | the isolation ladder, quotas, entitlements, per-tenant barriers; strict tenant isolation *(ruled, not yet implemented)* |

### Interfaces

| Feature | What it covers |
|---|---|
| [desktop-gui](desktop-gui/) | the Wails desktop application *(build tag `desktop`)* |
| [typescript-sdk](typescript-sdk/) | the `@qrdb/sdk` TypeScript client package, REST/Wails transports, typed errors, and contract tests |

## Migration state

**All 47 currently-implemented features have a `docs/<feature>/` folder with all
five files** (migrated 2026-08-19; [rate-limit](rate-limit/) and
[coordination](coordination/) added 2026-09-10;
[attested-caller](attested-caller/) added 2026-09-25;
[row-locking](row-locking/) added 2026-09-30; [tenant-dek](tenant-dek/) added
2026-10-01; [typescript-sdk](typescript-sdk/) and
[capabilities](capabilities/) added 2026-10-06). The portfolio view is derived,
not maintained — count `docs/*/architecture.md`.

The count read 34 while the tree held 35 folders: [guarded-swap](guarded-swap/)
had a complete folder and no row in the tables above. The row is now there, so
the tables and the derived count agree again.

Some legacy documents survive **as companions**, not as duplicates: they carry
deeper reference material the five-file layout points at rather than restates.
They are listed below and are linked from the feature folder that owns them.

## Companion references

| Document | Owned by |
|---|---|
| [GLOSSARY.md](GLOSSARY.md) | everything |
| [01-architecture.md](01-architecture.md) | [key-value](key-value/), [storage-engines](storage-engines/) |
| [05-consistency.md](05-consistency.md) | [key-value](key-value/) |
| [08-secondary-indexes.md](08-secondary-indexes.md), [16-query-planner.md](16-query-planner.md), [17-entity-sql-bridge.md](17-entity-sql-bridge.md) | [secondary-indexes](secondary-indexes/) |
| [12-configuration.md](12-configuration.md) | [configuration](configuration/) — the full field-by-field reference |
| [15-vectors.md](15-vectors.md) | [vectors](vectors/) |
| [18-distributed-locking.md](18-distributed-locking.md) | [distributed-locks](distributed-locks/) |
| [run-modes.md](run-modes.md) | [run-modes](run-modes/) — the ratified requirements statement |
| [bootstrap.md](bootstrap.md) | [firstboot](firstboot/) |
| [ceremony-sdk.md](ceremony-sdk.md) | [ceremony](ceremony/) |
| [scoping.md](scoping.md) | [multi-tenancy](multi-tenancy/) |
| [deployment-multi-node-raft.md](deployment-multi-node-raft.md) | [multi-tenancy](multi-tenancy/), [clustering](clustering/) |
| [embedding.md](embedding.md) | [api-server](api-server/), [run-modes](run-modes/) — embedding qrdb in a Go program |
| [databases/README.md](databases/README.md) | [databases](databases/) |
| [auth/](auth/) — README, roles, abac, layering, custom-authorizers | [identity-rbac](identity-rbac/) |
| [seal/](seal/) — README, sealed-window, compliance, multi-tenant, enabling-encryption, so-dual-factor, auth-factors, and the design records | [barrier-seal](barrier-seal/), [shamir-custody](shamir-custody/) |
| [sql/gorm.md](sql/gorm.md), [sql/upstream-gaps.md](sql/upstream-gaps.md), [sql/constraint-column-usage-duplication.md](sql/constraint-column-usage-duplication.md) | [sql](sql/) |
| [credentials/secret-resolution.md](credentials/secret-resolution.md) | [credentials](credentials/) |
| [gui/README.md](gui/README.md) | [desktop-gui](desktop-gui/) |
| [release/extension-seams.md](release/extension-seams.md) | [release](release/), [key-wrapping](key-wrapping/), [tenant-dek](tenant-dek/) — every composition seam an upstream composer can fill, its option, and the open gaps |
| [clusterenroll-noise-gap.md](clusterenroll-noise-gap.md) | open questions |
| [future/sqlite-vfs.md](future/sqlite-vfs.md) | future enhancement — proposed, **not implemented**: qrdb as a drop-in SQLite storage backend via a custom VFS |

## Testing

`make integration-test-<feature>` runs one feature's integration tests in
isolation — no Docker, one target per folder above. The selection lives in
[test/integration/features.mk](../test/integration/features.mk); the
per-feature, per-transport coverage it adds up to (and every known gap) is
[test/integration/COVERAGE.md](../test/integration/COVERAGE.md). See
`AGENTS.md`'s Testing section for the full set of test entry points, and
[release/engineering.md](release/engineering.md#test-tooling-this-feature-folder-also-owns)
for where each piece of this tooling lives.

## Module

```
github.com/jeremyhahn/go-qrdb
```
