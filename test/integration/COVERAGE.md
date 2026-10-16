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

# Integration test coverage matrix

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-13
> Accounting of integration coverage per feature and transport; derived from `test/integration/features.mk`.

CLI-driven, per-transport coverage for every `docs/<feature>/` folder, over
the six transports the unified API server hosts (`docs/api-server/`): REST,
gRPC, QUIC, MCP, Unix socket, and embedded (in-process, no network). The
owner's rule (`qrdb-cli-integration-coverage`) is that every command earns a
CLI integration test on every transport; this is the accounting against that
rule, derived from `test/integration/features.mk` and the test files it
selects. **This file lists gaps — it does not fix them.**

Transport legend: **R**=REST, **G**=gRPC, **Q**=QUIC, **M**=MCP, **U**=Unix
socket, **E**=embedded. A letter means a CLI-level integration test (the
non-Docker `test/integration` suite unless noted) drives that transport for
this feature; `—` means it does not.

## How CI runs these suites

The push-path cluster stage is `make integration-test-cluster`: every test in
the package, self-spawned, run by the `integration-cluster` job in
`.github/workflows/ci.yml` as three legs (`CLUSTER_SHARD=1/3`..`3/3`), each
selecting its share of `go test -list` by an anchored `-run` expression.
`make check-cluster-shards` proves the legs partition the package, so no test
drops out of the gate and none runs twice; `ci-gate` requires the job, so
every leg. A feature's own selection runs locally with
`make integration-test-<feature>`, and a test that `features.mk` maps to no
feature fails `make check-integration-feature-coverage`. The legs are
described in `docs/release/engineering.md` ("Cluster suite legs"). The
Docker-based `test/integration/api` suite runs as `integration-standalone`.

## The cross-cutting gap: embedded

**Every multi-transport CLI suite in `test/integration` runs over the same
shared five-protocol table** (`multiProtocols` in
`test/integration/multi_protocol_helpers_test.go`: REST, gRPC, QUIC, MCP,
Unix) **and that table does not include embedded**, because the `qrdb` CLI
itself has no `--server`/`--transport` form that selects embedded
(`cmd/qrdb/root.go`'s `schemePrefixes` has no `embedded://` entry, and
`cmd/qrdb/config.go`'s `buildClient` refuses `--transport embedded`
unconditionally). The practical effect: embedded is the one transport with
no CLI-level coverage for any feature below except
[replica-forwarding](#replica-forwarding) and [api-server](#storage-and-operations),
each of which drives embedded directly through `sdk/go/node`'s in-process
`*sdk.Client` instead of the CLI binary — forwarding because its behavior
differs for an embedded caller (no network hop to itself), api-server
because it is the one feature whose job is to prove behavioral parity
across every transport the server hosts, embedded included. auth-factors'
conditional add is driven over embedded the same way, and only that part
of the feature is. A command's
`_PerProtocol` test passing is not evidence the command works over
embedded; closing this gap for a given feature means adding a dedicated
embedded subtest the way those two files do, not widening `multiProtocols`
itself (it is, deliberately, the CLI-selectable set).

## Data plane

| Feature | R | G | Q | M | U | E | Notes |
|---|---|---|---|---|---|---|---|
| [key-value](../../docs/key-value/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `kv_cli_test.go`, `kv_restore_cli_test.go`, `batch_cli_test.go`, `batch_import_cli_test.go` are `_PerProtocol`. `kv_test.go`'s cluster-correctness suite (single/three-node, concurrent load) talks REST only. |
| [transactions](../../docs/transactions/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `tx_cli_test.go` is `_PerProtocol`. `sql_transaction_test.go` tests MySQL-wire transaction semantics (deadlock detection), a different protocol axis, not a qrdb-transport gap. |
| [guarded-swap](../../docs/guarded-swap/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `feature_gate_cluster_test.go`'s `TestCluster_FeatureGateActivatesGuardedWrites_CLI_PerProtocol` drives the post-drop guarded-write refusal over every remote transport; the three-node activation/drop-fence correctness suite itself (`TestCluster_FeatureGateActivatesGuardedWrites`) stays REST-only since the transport is not its axis. |
| [watch](../../docs/watch/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `_PerProtocol`, plus an MCP-specific no-loss replay case. |
| [leases](../../docs/leases/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `lease_cli_test.go` is `_PerProtocol`, including keep-alive past the granted TTL and expiry once renewals stop. `lease_binding_test.go`'s three-node restart-survival cases and `lease_keepalive_test.go`'s leader-failover case are REST only. etcd keep-alive and expiry are `TestEtcd_LeaseKeepAliveAndExpiry` in `etcd_test.go`. |
| [distributed-locks](../../docs/distributed-locks/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `_PerProtocol`. |
| [coordination](../../docs/coordination/) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | `TestCoordinator_SameOnEveryTransport` (`sdk/go/conformance/coordination_parity_test.go`) drives Get/Put/CompareAndSwap/AcquireLease/Watch through `Client.Coordination()` on every transport, including embedded, across the backend/mode grid. `coordination_sdk_test.go` (the `make integration-test-coordination` target) drives the same SDK `Client.Coordination()` surface over the shared multi-protocol node. Coordination is a client-side composition over leases/transactions/watch with deliberately no CLI surface (`docs/coordination/cli.md`), so this is SDK-level coverage rather than a CLI integration test. |
| [secondary-indexes](../../docs/secondary-indexes/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `index_cli_test.go` is `_PerProtocol`. |
| [full-text-search](../../docs/full-text-search/) | — | — | — | — | — | — | No CLI/SDK/transport surface exists (`docs/full-text-search/cli.md`'s documented gap), so there is no transport table to run -- the six columns stay `—` by design. `fts_test.go` drives `pkg/fts`'s `FTSIndex`, the package's upper-most abstraction, directly against every production `storage.Storage` backend (memory, file, Pebble). `WITH_QRDB_FTS` defaults off, so `features.mk`'s `INTTAGS_full_text_search := qrdb_fts` makes `make integration-test-full-text-search` compile and run it without a manual override. |
| [collections](../../docs/collections/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `_PerProtocol`. |
| [sql](../../docs/sql/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | The `qrdb sql-query` CLI subcommand (`sql_query_cli_test.go`) is `_PerProtocol`; `TestSQL_TenantScoping_CLI_PerProtocol` (`sql_query_tenant_scope_test.go`) extends the tenant-scoping proof across gRPC/QUIC/MCP/Unix too, seeding via the MySQL wire protocol once and reading back through `sql query` on every qrdb transport. The bulk of SQL correctness (`sql_test.go`, `sql_ddl_test.go`, `sql_autoincrement_test.go`, `sql_pgsql_literals_test.go`, `sql_alter_rewrite_test.go`, `sql_readonly_ddl_test.go`, `entity_bridge_test.go`) still runs over the MySQL/PgSQL *wire protocols* directly — the right axis for SQL correctness itself — and embedded remains the one documented cross-cutting gap (not in `multiProtocols`). |
| [row-locking](../../docs/row-locking/) | — | — | — | — | — | — | **Gap** on the qrdb-transport axis: `sql_row_lock_test.go` drives lock/deadlock behavior over the MySQL wire protocol only (no PgSQL, no qrdb CLI transport). |
| [vectors](../../docs/vectors/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `_PerProtocol`. |
| [time-series](../../docs/time-series/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `_PerProtocol`. |
| [embeddings](../../docs/embeddings/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `_PerProtocol`, but only the "provider not configured" refusal path — no suite drives a configured embedding provider end to end. `test/integration/api/embedding_rag_test.go` (Docker, `docker-compose.embedding.yml`, a real Ollama sidecar) is the one suite that does, and it is out of scope for this mapping. |
| [rag](../../docs/rag/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | Same shape as embeddings: `_PerProtocol` refusal path only here; the configured path is Docker-only (`embedding_rag_test.go`). |
| [etcd-compatibility](../../docs/etcd-compatibility/) | — | — | — | — | — | — | **Gap** on the qrdb-transport axis: `etcd_test.go` drives the etcd v3 wire protocol directly with a real etcd client; no qrdb CLI command exists to select here, so there is nothing to run over REST/gRPC/QUIC/MCP/Unix/embedded. |

## Storage and operations

| Feature | R | G | Q | M | U | E | Notes |
|---|---|---|---|---|---|---|---|
| [databases](../../docs/databases/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `database_lifecycle_cli_test.go` / `database_barrier_cli_test.go` are `_PerProtocol`. `multidb_scope_test.go`'s `TestMultiDB_Scope_AllTransports` drives gRPC/QUIC/MCP/Unix (no REST, no embedded) through the **SDK client directly**, not the `qrdb` CLI — real coverage, but not CLI-level. |
| [storage-engines](../../docs/storage-engines/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `admin_node_compaction_cli_test.go` is `_PerProtocol`. `sm_storage_file_test.go`'s file-backend correctness suite is REST only. |
| [volume-pools](../../docs/volume-pools/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `_PerProtocol`. |
| [backup-restore](../../docs/backup-restore/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `backup_cli_test.go`'s command-tree suite is `_PerProtocol`; the disaster-recovery scenarios in `backup_restore_test.go` are REST only. |
| [clustering](../../docs/clustering/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `admin_cluster_cli_test.go`, `join_request_cli_test.go`, `join_token_cli_test.go`, `rebalance_cli_test.go`, `drain_test.go` are `_PerProtocol`. `admin_test.go` (start/discovery/cluster-formation flag matrix) and `cluster_test.go` (health/status/leader-election/snapshot/restart) drive REST only. `cluster_crash_test.go` (SIGKILL of the leader and then a follower under concurrent writers) and `cluster_partition_test.go` (leader isolated and healed at a UDP relay in front of every Raft listener, `qrdbdharness.UDPRelay`) and `cluster_lease_test.go` (linearizable reads through a leader that is isolated, paused and isolated, or paused, while a successor overwrites the key, with `lease_read` on) drive REST only: their axis is fault recovery, not the transport. |
| [run-modes](../../docs/run-modes/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `run_modes_standalone_cli_test.go` is `_PerProtocol`. `qrdbd_cli_test.go` (completion, node identity) and `completion_cli_test.go` are local-CLI-only — no server, so the transport axis does not apply to them. |
| [capabilities](../../docs/capabilities/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `cli_multiprotocol_test.go` drives `qrdb admin capabilities` through the shared admin command tree over every remote CLI transport; embedded coverage remains in the admin/API-server suite rather than the CLI matrix. |
| [configuration](../../docs/configuration/) | — | — | — | — | — | — | **Gap.** No dedicated integration test; exercised only incidentally through other features' config fixtures. |
| [deployment](../../docs/deployment/) | — | — | — | — | — | — | **Gap.** The locked-memory budget is a host/kernel property (`RLIMIT_MEMLOCK`); no integration test exercises it. |
| [release](../../docs/release/) | — | — | — | — | — | — | **Gap**, by nature: a release is a tagging and CI process, not a runtime surface. Its own test coverage is `test/buildgates/release_workflow_test.go` and the `scripts/check-release-alpha_test.sh` / `check-tag-lockstep_test.sh` suites (see `docs/release/engineering.md`), not a CLI integration test. |
| [api-server](../../docs/api-server/) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | `test/integration/cli_multiprotocol_test.go` is the self-spawned, non-Docker `_PerProtocol` suite for `version`, `--server` flag-format parsing, and `admin health`/`status`/`peers` (including `--output json`/`yaml`) over REST/gRPC/QUIC/MCP/Unix, plus a dedicated embedded-transport subtest per command (the CLI has no `--server`/`--transport` form that selects embedded). `test/integration/api/{rest,grpc,mcp,unix}_test.go` and its own `cli_multiprotocol_test.go` remain the Docker-based suite run by `make integration-test-standalone` for CI; the `kv` command tree itself is covered by [key-value](#data-plane), not duplicated here. |
| [replica-forwarding](../../docs/replica-forwarding/) | ✓ | ✓ | ✓ | ✓ | — | ✓ | The one feature with real embedded coverage: `replica_forwarding_embedded_test.go` and the mixed-cluster case test an embedded caller specifically, because forwarding has no network hop to itself. No Unix-socket forwarding case exists. |

## Security and identity

| Feature | R | G | Q | M | U | E | Notes |
|---|---|---|---|---|---|---|---|
| [barrier-seal](../../docs/barrier-seal/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | Several `_PerProtocol` suites (`barrier_cli_test.go`, `barrier_permutations_cli_test.go`, `cluster_seal_cli_test.go`, and now `enable_encryption_cli_test.go`'s `TestCLI_BarrierEnableEncryption_SystemScope_Lifecycle_CLI_PerProtocol`, which generalizes the `barrier enable-encryption` trigger itself). `cluster_seal_test.go`, `enable_encryption_test.go`, and `cluster_enable_encryption_test.go` post directly to the REST admin API (no CLI binary involved) to assert cluster-replication/migration/restart-survival semantics already exercised at the CLI layer elsewhere; the one step that stays REST-only inside the `_PerProtocol` suites too is `barrier unseal` while sealed, since only the REST client's `Transport.Connect` tolerates a 503 as reachable (gRPC/QUIC/MCP do not), matching `TestBarrierClusterUnseal_CLI_PerProtocol`'s identical choice. `fips_kdf_test.go` tests `pkg/seal/kdf` directly as a package with no CLI or transport surface at all. |
| [shamir-custody](../../docs/shamir-custody/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `barrier_cli_test.go` includes the system-barrier Shamir lifecycle `_PerProtocol`: holder enrolment/listing, init-shamir, claim and claim-begin/claim-complete, quorum submit for seal/unseal/root-token/rekey/recovery, and recovery status/delete. `tenant_barrier_officer_cli_test.go` covers the tenant holder lifecycle `_PerProtocol`. |
| [ceremony](../../docs/ceremony/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `TestCeremony_SignCSR_CLI_PerProtocol`, `TestCLI_BarrierOfficerRegister_SealedWindow_CLI_PerProtocol`, and `TestCLI_RecoveryOfficerFlow_CLI_PerProtocol` are `_PerProtocol`; the remaining init/officer regression tests in the same file (bug-specific scenarios) drive REST/Unix only. |
| [firstboot](../../docs/firstboot/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `TestInit_CLI_EnvVarBinding_PerProtocol`, `_SingleAdmin_PerProtocol`, `_ShamirWithOfficers_PerProtocol` are `_PerProtocol`; local-flag-validation, hostname-SAN and production-boot-proof cases are REST/local only. |
| [trust-bootstrap](../../docs/trust-bootstrap/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `TestBootstrap_CLI_PerProtocol` — the command is shape-only (always refuses), so this proves the refusal is consistent across transports, not a working bootstrap. |
| [certificate-authority](../../docs/certificate-authority/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `ca_cli_test.go` and `TestClusterCA_SelfIssued_SurvivesKeyDeletionAndRestart_CLI_PerProtocol` are `_PerProtocol`. The original REST-only `TestClusterCA_SelfIssued_SurvivesKeyDeletionAndRestart` stays as a focused regression test. |
| [trust-anchors](../../docs/trust-anchors/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `tenant_trust_anchors_cli_test.go` is `_PerProtocol`: the add/list/remove lifecycle, the 32-anchor-per-tenant cap, the typed refusals (empty bundle, non-CA certificate, unknown fingerprint), cross-tenant confinement, and the BYOC admit-then-refuse guarantee (a cert is accepted while its anchor is registered, refused the instant the anchor is withdrawn). |
| [credentials](../../docs/credentials/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `_PerProtocol`. |
| [key-wrapping](../../docs/key-wrapping/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `key_wrapping_cli_test.go` is `_PerProtocol`: the encrypted-backup envelope round trip (provider recorded and shown by `backup get`, stored key reference resolving on restore, an override naming the right key accepted and the wrong key refused as a decryption failure) and the refusals (unregistered provider, path or `..` instead of a bare key name, missing and wrong-length key files). The offline boot-time restore of an encrypted backup is `backup_restore_test.go`. |
| [tenant-dek](../../docs/tenant-dek/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `dek_cli_test.go` is `_PerProtocol`. `dek_test.go`'s cluster-replication and restart-survival suites drive REST only. |
| [identity-rbac](../../docs/identity-rbac/) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | `roles_cli_test.go`, `users_cli_test.go`, `invitation_cli_test.go` are `_PerProtocol`. `identity_hydrator_test.go` is embedded only: it boots an in-process node with `node.WithIdentityHydrator` and asserts the Subject the login ceremony returns and its bearer resolves to, because the ceremony is served by the embedded transport alone and a hydrator is a Go value no spawned `qrdbd` can take. |
| [auth-factors](../../docs/auth-factors/) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | `auth_cli_test.go` is `_PerProtocol`. `auth_factor_condition_cli_test.go` drives the conditional add (one-time claim, expected version, N racing claims) through the CLI per protocol and, for **E**, through `sdk/go/node`'s in-process client; the rest of the lifecycle has no embedded coverage. `test/integration/auth/factors_test.go` additionally drives the factor-lookup path's REST/gRPC/QUIC/MCP wire encodings directly (in-process, no CLI, no Unix). |
| [audit](../../docs/audit/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `_PerProtocol`. |
| [rate-limit](../../docs/rate-limit/) | — | — | — | — | — | — | **Gap.** No integration test. |
| [attested-caller](../../docs/attested-caller/) | — | — | — | — | — | — | **Gap.** No integration test. |
| [multi-tenancy](../../docs/multi-tenancy/) | ✓ | ✓ | ✓ | ✓ | ✓ | — | `tenant_cli_test.go`, `tenant_isolation_cli_test.go`, `tenant_deleted_cli_test.go` are `_PerProtocol`. `tenant_barrier_init_proof_cli_test.go` runs `tenant barrier-init`'s proof of possession `_PerProtocol` on the five network transports (no signer, another key's signature, then the real CLI with the first admin's key); embedded asks no proof. `tenant_data_root_cli_test.go` and the `isolation_raft_*` cluster-correctness suites drive REST only. |

## Interfaces

| Feature | R | G | Q | M | U | E | Notes |
|---|---|---|---|---|---|---|---|
| [desktop-gui](../../docs/desktop-gui/) | — | — | — | — | — | — | Out of scope for this Go harness: covered by the TypeScript SDK / Playwright suite (`make test-e2e`), which drives the desktop app and web UI against a live `qrdbd` over REST. |
| [typescript-sdk](../../docs/typescript-sdk/) | — | — | — | — | — | — | Out of scope for this Go harness: covered by `npm test` in `sdk/typescript` and the frontend Playwright suite (`npm run test:e2e` / `npm run test:e2e:real`) under `cmd/qrdb/frontend`. |

## Summary

- **4 features have no integration test at all**: configuration, deployment,
  rate-limit and attested-caller, and (on the qrdb-transport axis
  specifically) etcd-compatibility and row-locking are additionally gapped
  even though their own wire protocol is tested.
- **release**, **desktop-gui**, and **typescript-sdk** are deliberately out of scope for this
  harness (process, Playwright-covered, and TypeScript/Vitest-covered, respectively), not silent gaps.
- **api-server** now has a self-spawned, non-Docker suite
  (`cli_multiprotocol_test.go`) covering all six transports for `version`,
  `--server` flag formats, and `admin health`/`status`/`peers`; the
  Docker-based `test/integration/api` suite is kept as-is for CI, and the
  `kv` command tree stays with key-value, not duplicated into api-server.
- **Embedded has no CLI-level coverage anywhere** except
  replica-forwarding's and api-server's dedicated suites — see "The
  cross-cutting gap: embedded" above.
