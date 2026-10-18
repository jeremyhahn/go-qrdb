# Copyright 2026 Jeremy Hahn
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# ============================================================================
# Per-feature integration test selection
# ============================================================================
#
# ONE table, mapping each docs/<feature>/ folder to the integration test
# files (relative to test/integration/) whose top-level Test functions
# belong to it. The Makefile's `integration-test-%` pattern rule reads this
# file: it extracts every `func Test...` from the mapped files and runs
# exactly those, via `go test -run`, over the self-spawned qrdbdharness
# suites in ./test/integration and ./test/integration/auth -- no Docker, no
# compose stack, each test file building its own qrdbd/qrdb binaries the way
# it always has (sdk/go/testutil/qrdbdharness).
#
# A feature below with an EMPTY value has not yet earned a non-Docker
# integration test: `make integration-test-<feature>` then runs zero tests
# and SAYS SO, rather than silently matching nothing. Two shapes of empty
# entry, each noted where it is declared:
#   - desktop-gui: covered by the TypeScript SDK / Playwright suite
#     (`make test-e2e`), which is not this Go harness.
#   - typescript-sdk: covered by `npm test` in sdk/typescript and by the
#     frontend Playwright suite, not this Go harness.
#   - coordination: deliberately has no CLI surface (docs/coordination/cli.md),
#     so there is no CLI command here to select; its conformance coverage is
#     TestCoordinator_SameOnEveryTransport in sdk/go/conformance/
#     coordination_parity_test.go, a separate module/suite outside this
#     Makefile's reach.
#   - every other empty entry is a real coverage gap. test/integration/COVERAGE.md
#     is the per-transport accounting; it lists each by name.
#
# api-server is NOT empty: cli_multiprotocol_test.go is the self-spawned
# equivalent of the Docker suite's test/integration/api/cli_multiprotocol_test.go
# for the surface a CLI-selectable transport can reach (version, --server
# flag-format parsing, admin health/status/peers and their --output
# json/yaml forms), covering REST, gRPC, QUIC, MCP and Unix via
# startMultiProtocolNode plus the embedded transport directly (the CLI has
# no --transport/--server form that selects it at all). The Docker suite
# (test/integration/api/{grpc,rest,mcp,unix,cli_multiprotocol}_test.go, run
# by `make integration-test-standalone`) stays for CI; the kv command tree
# and cluster topology/membership remain covered by the key_value and
# clustering entries below, not duplicated here. metrics_test.go scrapes a raft
# node's Prometheus /metrics (the default gatherer, the WAL fsync histogram,
# the authentication requirement). module_optional_route_test.go serves a
# service module's HandleOptional route from an in-process node over real TLS
# (anonymous, bound-certificate and refused callers).
#
# test/buildgates/feature_integration_coverage_test.go (run by
# `make check-integration-feature-coverage` in the Lint job) is the other
# reader of this file: it fails when a docs/*/architecture.md folder has no
# INTTEST_ entry here, or when a Test function anywhere in the NON-DOCKER
# packages (test/integration, test/integration/auth -- test/integration/api
# is the compose-based suite and is out of scope for this mapping) is not
# named by any entry's files.
#
# A value is a space-separated list of paths relative to test/integration/,
# e.g. "kv_cli_test.go" (the root package) or "auth/factors_test.go" (the
# test/integration/auth package). Four files declare no top-level Test
# function and belong to no feature on that account alone -- they are shared
# helpers, not suites, and nothing in them needs selecting:
# helpers_test.go, membership_helpers_test.go, multi_protocol_helpers_test.go,
# replica_forwarding_helpers_test.go.
#
# full-text-search has no CLI/SDK/transport surface at all (docs/full-text-search
# /cli.md) and its only axis is qrdb_fts, a WITH_QRDB_* tag that defaults OFF
# -- the opposite of every other tag-gated feature here. Its test
# (fts_test.go) drives pkg/fts's FTSIndex, the package's upper-most
# abstraction per this directory's own CLAUDE.md ("packages without CLI
# support" convention), directly against every production storage.Storage
# backend. Because qrdb_fts is off by default, its entry pairs with an
# optional `INTTAGS_full_text_search := qrdb_fts` (see Makefile's
# INT_FEATURE_TAGS comment) so `make integration-test-full-text-search` on
# its own compiles and runs it -- no WITH_QRDB_FTS=1 override needed. The
# INTTAGS_ prefix (not INTTEST_) is deliberate: test/buildgates/
# feature_integration_coverage_test.go's featureMapping parses every
# `^INTTEST_...` line as a feature's FILE list, and "qrdb_fts" is a build
# tag, not a file in this directory -- sharing the prefix would make that
# gate look for a file named "qrdb_fts" and fail.
#
# The variable name is INTTEST_ plus the feature folder's name with every
# `-` turned into `_` (make variable names may contain hyphens, but the
# pattern rule avoids the question entirely by computing the same
# substitution on both sides: $(INTTEST_$(subst -,_,$*))).

INTTEST_api_server          := cli_multiprotocol_test.go metrics_test.go module_optional_route_test.go
INTTEST_attested_caller     :=
INTTEST_audit               := audit_cli_test.go audit_tenant_confinement_cli_test.go audit_replicated_cluster_test.go
INTTEST_auth_factors        := auth_cli_test.go auth_factor_condition_cli_test.go auth/factors_test.go
INTTEST_backup_restore      := backup_cli_test.go backup_restore_test.go
INTTEST_barrier_seal        := barrier_cli_test.go barrier_permutations_cli_test.go cluster_seal_cli_test.go cluster_seal_test.go cluster_enable_encryption_test.go enable_encryption_cli_test.go enable_encryption_test.go tenant_barrier_sealed_window_cli_test.go tenant_barrier_restart_cli_test.go fips_kdf_test.go
INTTEST_ceremony            := officer_ceremony_cli_test.go ceremony_forwarding_test.go ceremony_follower_forward_test.go
INTTEST_certificate_authority := ca_cli_test.go ca_delegate_cli_test.go clusterca_test.go cluster_ca_anchor_spof_test.go
INTTEST_capabilities        := cli_multiprotocol_test.go
INTTEST_clustering          := admin_test.go admin_cluster_cli_test.go admin_state_hash_cli_test.go cluster_test.go drain_test.go rebalance_cli_test.go join_request_cli_test.go join_token_cli_test.go join_p256_cli_test.go leader_transfer_cli_test.go member_record_test.go dynamic_membership_pebble_test.go
# cluster-verification: the in-CI leader SIGKILL-under-load, leader
# partition and lease-read-after-leader-change tests. The soak itself
# (`make cluster-proof`) is not this harness and is never selected here.
INTTEST_cluster_verification := cluster_crash_test.go cluster_lease_test.go cluster_partition_test.go cluster_ports_test.go
INTTEST_collections         := collection_cli_test.go
INTTEST_configuration       :=
# coordination: SDK-only by design (see the note above); the per-transport
# conformance coverage also lives in sdk/go/conformance/coordination_parity_test.go.
INTTEST_coordination        := coordination_sdk_test.go
INTTEST_credentials         := credential_cli_test.go
INTTEST_databases           := database_barrier_cli_test.go database_barrier_holders_cli_test.go database_lifecycle_cli_test.go multidb_scope_test.go dao_sdk_test.go dao_cluster_test.go
INTTEST_deployment          :=
INTTEST_desktop_gui         :=
INTTEST_distributed_locks   := lock_cli_test.go
INTTEST_embeddings          := embedding_cli_test.go
INTTEST_etcd_compatibility  := etcd_test.go
INTTEST_firstboot           := init_cli_test.go
INTTEST_full_text_search    := fts_test.go
INTTAGS_full_text_search    := qrdb_fts
INTTEST_guarded_swap        := feature_gate_cluster_test.go
INTTEST_identity_rbac       := roles_cli_test.go roles_own_permissions_cli_test.go users_cli_test.go invitation_cli_test.go tenant_owner_test.go tenant_owner_cli_test.go identity_hydrator_test.go
INTTEST_key_value           := kv_cli_test.go kv_test.go kv_read_permission_test.go kv_restore_cli_test.go kv_restore_default_sm_cli_test.go batch_cli_test.go batch_import_cli_test.go batch_import_session_test.go
INTTEST_key_wrapping        := key_wrapping_cli_test.go
INTTEST_leases              := lease_binding_test.go lease_cli_test.go lease_keepalive_test.go
INTTEST_multi_tenancy       := tenant_cli_test.go tenant_data_root_cli_test.go tenant_deleted_cli_test.go tenant_isolation_cli_test.go isolation_raft_cluster_test.go isolation_raft_dedicated_wal_test.go isolation_raft_rebalance_test.go isolation_raft_scale_test.go isolation_raft_shard_ids_test.go tenant_barrier_init_proof_cli_test.go
INTTEST_rag                 := rag_cli_test.go
INTTEST_rate_limit          :=
INTTEST_release             :=
INTTEST_replica_forwarding  := replica_forwarding_embedded_test.go replica_forwarding_grpc_test.go replica_forwarding_mcp_test.go replica_forwarding_mixed_test.go replica_forwarding_quic_test.go replica_forwarding_rest_test.go replica_forwarding_test.go
INTTEST_row_locking         := sql_row_lock_test.go
INTTEST_run_modes           := run_modes_standalone_cli_test.go qrdbd_cli_test.go completion_cli_test.go
INTTEST_secondary_indexes   := index_cli_test.go dao_sdk_test.go dao_cluster_test.go
INTTEST_shamir_custody      := barrier_cli_test.go tenant_barrier_officer_cli_test.go
INTTEST_sql                 := sql_alter_rewrite_test.go sql_autoincrement_test.go sql_ddl_test.go sql_pgsql_literals_test.go sql_query_cli_test.go sql_query_tenant_scope_test.go sql_readonly_ddl_test.go sql_test.go entity_bridge_test.go
# sqlite-vfs: a future feature, not implemented; nothing to select yet.
INTTEST_sqlite_vfs          :=
INTTEST_storage_engines     := admin_node_compaction_cli_test.go sm_storage_file_test.go
INTTEST_tenant_dek          := dek_test.go dek_cli_test.go
INTTEST_time_series         := ts_cli_test.go
INTTEST_transactions        := sql_transaction_test.go tx_cli_test.go
INTTEST_trust_anchors       := tenant_trust_anchors_cli_test.go
INTTEST_trust_bootstrap     := bootstrap_cli_test.go
INTTEST_typescript_sdk      :=
INTTEST_vectors             := vector_cli_test.go
INTTEST_volume_pools        := volume_pool_cli_test.go volume_boot_cli_test.go volume_boot_degraded_cli_test.go
INTTEST_watch               := watch_cli_test.go
