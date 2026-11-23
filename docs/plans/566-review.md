# 566 whole-file review of the changed test files (rw566w2)

Codex round 3 on PR #1707 asked for a review of every changed test file
past the diff: the surrounding test, and the production code each changed
test drives. This is that record: one row per `*_test.go` file in
`git diff --name-only $(git merge-base HEAD main) HEAD`. A file whose only
change is a removed test points at the audit record that names the test
keeping its path (566-suite-audit.md, 566-audit-2..5.md); each name was
checked against those records, and those records are not redone here.

Checks per file: no assertion weakened; every wait (Eventually, Never,
synctest.Wait, a channel receive) observes the awaited event, not only
elapsed time; a removed test's path is kept by the test the record names.

Verdicts: ok = nothing to fix; fixed in <commit subject>.

## cmd/qrdb, cmd/qrdbd

| File | Changed tests | Caller chain checked | Verdict | Note |
|---|---|---|---|---|
| cmd/qrdbd/start_coverage_test.go | TestCertReloader_FileWatch_DebounceReloadError, TestCertReloader_FileWatch_WatcherError (sleep → recordingHandler.waitFor); TestNewKVServiceAdapter_ReturnsNonNil removed | cert_reload.go watch loop: the debounce AfterFunc logs "debounced reload failed" only after Reload returns an error; the Errors case logs "filesystem watcher error" | ok | waitFor matches the exact message the production path logs, so it passes only once that branch ran; removal in 566-audit-3 |
| cmd/qrdbd/cert_reload_test.go | TestCertReloader_FileWatch, _KeyDirChange (Eventually on the swapped pointer, then the certificate compared); _IgnoresUnrelatedFiles, _Stop_NoReloadAfterStop (Never over three debounce intervals) | CertReloader.watch → Reload → cert.Store | ok | Never over 300 ms observes at least what the old 300 ms sleep then one read did |
| cmd/qrdbd/cluster_migrate_test.go | TestRunClusterMigReconciler_Tick | runClusterMigReconciler → store.GetMigration nil after reap | ok | same condition and bound |
| cmd/qrdbd/holder_audit_test.go | TestAwaitUnseal_UnsealQuorumIsAudited | sealed window installs HolderDeps().Audit | ok | same condition and bound |
| cmd/qrdbd/isolation_floor_test.go | TestApplyIsolationFloor_ContinuesInBackground | applyIsolationFloor background retry → svc.state() | ok | same condition and bound |
| cmd/qrdbd/noise_port_test.go, noise_port_unix_test.go | TestPortHold_TakesThePortFromAHolderItCannotReach (sleep → polls >= 2) | portHold.poll increments polls only after listen() fails on a tick | ok | the counter proves the fallback ran; the not-armed check and assertPortHeld are unchanged |
| cmd/qrdbd/reclaim_sweep_test.go | TestStartReclaimSweeper_StandaloneDaemonSweepsAsTheLeader, _ClusteredFollowerStillSweeps | startReclaimSweeper → resolver.sweeps() | ok | same condition and bound |
| cmd/qrdbd/unseal_ceremony_test.go | initializeCeremony helper (retry loop → EventuallyWithT) | POST /v1/ceremony/initialize until status < 300 | ok | a request-build error now retries instead of failing at once; it cannot occur with a fixed URL and body |
| cmd/qrdbd/adapters_test.go | TestKVServiceAdapter_ImplementsKVStore removed | newKVServiceAdapter declared return type | ok | 566-audit-3 |
| cmd/qrdbd/coverage_missing_test.go | TestWebUIHandler_ImplementsHTTPHandler removed | webUIHandler declared return type | ok | 566-audit-3 |
| cmd/qrdb/bindings_test.go | four *_Interface tests removed | error types' Error() kept by the *_Message tests | ok | 566-audit-3 |
| cmd/qrdb/host_adapter_test.go | two Implements tests removed | compile-time `_ service.Host` guards | ok | 566-audit-3 |

## pkg/service

| File | Changed tests | Caller chain checked | Verdict | Note |
|---|---|---|---|---|
| pkg/service/vector_metric_index_test.go | new qrdb_vector file: the nine index-building tests moved from vector_batch_dimension, vector_index_kind, vector_list_cursor, vector_metric and vector_geometry_refusal | newNamespaceIndex, CreateNamespace, SearchSimilar, BatchUpsert, performIndexRebuild | ok | bodies moved unchanged (rw566v); the isolation test left again in rw566w2 |
| pkg/service/vector_geometry_refusal_test.go | TestVectorService_RestartRefusesStoredUnknownGeometry (untagged); TestWithClusterIsolationPolicyReader_OperatorAccess moved in | CreateNamespace: validateTenantCreate runs before the namespace-exists check and before newNamespaceIndex | fixed in test(service): run the operator-access isolation test without qrdb_vector (#566) | the test now asserts operator-access passes the gate (NamespaceExistsError) and strict refuses (TenantRequiredError) on a pre-existing namespace, so no index is built and it runs untagged |
| pkg/service/vector_batch_dimension_test.go, vector_index_kind_test.go, vector_list_cursor_test.go, vector_metric_test.go | index-building tests moved out | as above | ok | every removed name is a func in vector_metric_index_test.go |
| pkg/service/vector_restart_test.go | qrdb_vector tag added; newRestartVectorStorage moved to the untagged refusal file | PebbleVectorStorage open | ok | helper body unchanged |
| pkg/service/vector_service_test.go | TestVectorService_GetRebuildProgress_EstimatedTimeRemaining: 10 ms sleep removed | RebuildIndex async → GetRebuildProgress | ok | its assertion was already ElapsedTimeMs >= 0, which the sleep never changed; the name promises more than it checks (pre-existing, report item) |
| pkg/service/coverage_gaps2_test.go | TestWithClusterIsolationPolicyReader_OperatorAccess moved out | see vector_geometry_refusal_test.go | ok | |
| pkg/service/hard_confined_watch_test.go | TestHardConfinedWatch_WatchEntityTypeFilterGoroutineExitsOnContextDone (sleep → synctest bubble) | HardConfinedWatch.WatchEntityType forwarder: select on `out <- ev` / ctx.Done | ok | a leaked forwarder would still be blocked on the send after the second Wait, so the read gets the value and the test fails |
| pkg/service/invitation_test.go | TestRedeemInvitation_RefusesAnExpiredToken (1 ns TTL + sleep → one-minute TTL on the synctest clock, both sides of the boundary) | RedeemInvitation → invite expiry against s.now() | ok | "past the TTL" can only pass if the service reads the fake clock, so the bubble is exercised |
| pkg/service/reclaim_job_test.go | TestReclaimSweeper_SetStoreResolverIsRaceFreeWithRun (synctest bubble) | ReclaimSweeper.Run tick vs SetStoreResolver | ok | both goroutines wake at each fake millisecond, so -race sees the same interleaving |
| pkg/service/shard_snapshot_provider_test.go | TestHostShardSnapshotProvider_SnapshotShard_ContextCanceledDuringPoll (timer cancel → cancel inside the first host callback) | SnapshotShard request → poll loop ctx check | ok | still asserts ShardSnapshotError wrapping context.Canceled |
| pkg/service/fsync_stall_leadership_test.go | fsyncStallCluster.leader helper | host leader query | ok | id/term read only after the successful tick |
| pkg/service/batch_import_session_leader_test.go | waitForLeaderExcept helper | host leader query excluding one host | ok | as above |
| pkg/service/watch_entity_terminal_test.go | awaitEviction helper | watch.Hub.WatchCount reaching zero | ok | same condition and bound |
| pkg/service/mvcc_service_test.go | constructor and mock-interface tests removed; compile-time guards added | mocks satisfy the interfaces the service type-asserts | ok | 566-audit-2 |
| pkg/service/backend_kv_test.go, batch_service_test.go, collection_service_test.go, credential_service_test.go, database_service_test.go, index_service_test.go, lease_service_test.go, proposer_test.go, rebalance_move_test.go, rebalance_service_test.go, service_test.go, timeseries_service_test.go, transaction_service_test.go, watch_service_test.go | ReturnsNonNil / Implements tests removed | constructor return types or compile-time guards | ok | 566-audit-2 |

## sdk/go

| File | Changed tests | Caller chain checked | Verdict | Note |
|---|---|---|---|---|
| sdk/go/node/cluster_formation_test.go | awaitAgreedLeader, TestRunningCluster_AddReplica_Identity (two loops), advertiseJoiner, TestClusteredNode_UnsealRetriesAfterACancelledBoot_DataPlaneAccessorsCleared: sleep → ticker | node leader / membership / unseal state polled by the unchanged loop condition | ok | only the pause between polls changed; each loop still exits on its observed condition or its deadline |
| sdk/go/node/compaction_replicated_test.go | setPolicy helper | policy read-back loop | ok | as above |
| sdk/go/node/embedded_cluster_ca_test.go | retryUntil helper | f() until nil or ctx done | ok | the pause now also wakes on ctx.Done; the return still reports f's error |
| sdk/go/node/prebound_listener_test.go | TestNode_PreboundListenersSurviveMigration | h3Healthy probes across the stale HTTP/3 timer window | ok | every probe asserted, plus one after the window ends: stronger than before |
| sdk/go/node/tenant_isolation_test.go | awaitReadIndexReady helper | ReadIndex ready loop | ok | ticker only |
| sdk/go/node/tenant_undo_watch_test.go | awaitFeatureActive, TestClusteredNode_FollowersAcknowledgeAnUndoWhileServing | feature gate / undo ack loops | ok | ticker only |
| sdk/go/conformance/compaction_node_test.go, compaction_policy_test.go, coordination_parity_test.go, harness_test.go, share_holders_test.go | TestNodeKeyspaceCompaction_OnEveryTransport, TestCompactionPolicy_OnEveryTransport, assertLeaseExpiryDeletesKeys, awaitWritable, holderE2E.dial | per-transport polling loops | ok | ticker only; conditions and deadlines unchanged |
| sdk/go/coordination_test.go | blockedRenewal and the five TestLease_* deadline tests, TestLease_OwnershipEndsAtTheDeadlineNotTheNextPoll: wall-clock ranges → synctest bubble | Lease renew / Done / poll loop over fakeLease | ok | the deadlines are now asserted as exact instants (4 s, 6 s, 7 s) instead of ranges; the acknowledged-renewal test now proves the extension reaches send+TTL, where it used to check only 1 s past the renew |
| sdk/go/driver/pool_test.go | assertPoolDrained | sql.DB stats loop | ok | ticker only |
| sdk/go/driver/connector_network_test.go, stmt_test.go | interface-compliance tests removed | compile-time guards | ok | 566-audit-5 |
| sdk/go/testutil/qrdbdharness/daemon_test.go, relay_test.go | waitForTail, waitCount | daemon log tail, relay counters | ok | ticker only |
| sdk/go/transport/embedded/seal_watch_test.go, transport_watch_test.go | TestWatch_OtherTenantSealLeavesTheStream, settledGoroutineCount, goroutinesAtMost, awaitEviction | watch stream / goroutine count / hub count loops | ok | ticker only; a ticker starts no goroutine, so the goroutine counts are not disturbed |
| sdk/go/transport/mcp/transport_database_scope_test.go, transport_guarded_test.go; sdk/go/transport/quic/transport_credentials_test.go, transport_guarded_test.go; sdk/go/transport/unix/transport_guarded_test.go | Implements tests removed | compile-time guards | ok | 566-audit-5 |
