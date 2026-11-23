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

## pkg/server

| File | Changed tests | Caller chain checked | Verdict | Note |
|---|---|---|---|---|
| pkg/server/conn_observer_test.go | waitForCounts, TestConnObserver_SeesEveryListener loops → assert.Eventually then a re-read that fails with the observed counts | connection observer counters per listener | ok | the failure path re-reads and names the counts, so a timeout cannot pass |
| pkg/server/data_plane_keyspace_test.go | lease-expiry wait → clocktest fake clock, Advance past the TTL, then ExpiredLeaseIDs / Revoke called synchronously | lessor expiry against the injected clock | ok | the expiry is driven, not waited for |
| pkg/server/lifecycle/cluster_ca_test.go | backoff-cancel test → synctest bubble with an onCall counter | cluster CA read retry backoff → ctx.Done | ok | asserts one read, the call still blocked in the backoff, then an immediate return after the cancel |
| pkg/server/lifecycle/local_lease_expiry_test.go | fake clock: BlockUntil(1), Advance, Waiters()==0 after stop | local lease expiry loop timer | ok | BlockUntil observes the loop parked on its timer before the clock moves |
| pkg/server/lifecycle/trust_anchors_test.go | poll → Eventuallyf | trust anchor reload | ok | same condition and bound |
| pkg/server/namespaced_watch_leak_test.go | goroutine/watch-count waits → Eventually helpers on quietReporter returning the last sample | namespaced watch teardown | ok | callers assert the returned sample, so a timeout fails with the observed count |
| pkg/server/namespaced_watch_terminal_test.go | terminal-event wait → Eventually | namespaced watch terminal close | ok | same condition and bound |
| pkg/server/raft_backend_factory_identity_test.go | identity poll → EventuallyWithT | raft backend factory replica identity | ok | assertions inside the CollectT run on the last tick |
| pkg/server/raft_backend_factory_lock_test.go | lock-held poll → Eventually with a deferred unlock | factory open lock | ok | same condition; the unlock now runs on every exit path |
| pkg/server/raft_backend_factory_test.go | writeAndAwaitLogRange: one synchronous read, then a ticker loop; KeepsLog also asserts LastIndex >= written | raft log range after a write | ok | stronger: the kept log is checked to cover the written index |
| pkg/server/raft_backend_migrate_test.go | getWhenReadable → retryWhileReadIndexNotReady (assert.Eventually on quietReporter, 15 s, 200 ms) | get after an election until "read index not ready" clears | ok | the last attempt's error is returned and asserted by the caller; the mutex and done channel stop a late tick from running an attempt after the helper returns, so the result read by the caller is the last one |
| pkg/server/raft_promoter_identity_test.go | TestRaftPromoter_PromoteObserverCarriesItsIdentity: ctx loop → require.Eventually on Observers[2] | follower ShardMembership after promote | ok | same condition; bound is raftharness.Bound |
| pkg/server/rest_registry_test.go | TestNewRESTServiceRegistry_ReturnsNonNil removed | NewRESTServiceRegistry declared return type and its behaviour tests | ok | 566-audit-4 |
| pkg/server/served_watch_test.go, served_watch_transport_test.go, server_etcd_guard_test.go | drained, waitFor, GuardStreamEndsOnClusterSeal: deadline loops → require.Eventually | served watch queue / stream subscription count | ok | same condition, bound and message |
| pkg/server/startup_read_test.go | TestRunStartupRead_ContextEnds → synctest bubble; _ContextEndsMidAttempt cancels inside the attempt; _ReportsAnAttemptThatOutlivesItsBound returns once two progress lines are logged | RunStartupRead attempt / backoff / progress reporter | ok | ContextEnds now proves the cancel lands in the backoff (one read, still blocked, zero fake time to return); the outlive test waits on the logged lines, not a 200 ms sleep |
| pkg/server/tenant_shard_reconciler_test.go | awaitCondition → assert.Eventually on quietReporter | tenant shard reconciler convergence | ok | returns the bool; callers phrase the failure |

## Other packages

| File | Changed tests | Caller chain checked | Verdict | Note |
|---|---|---|---|---|
| pkg/api/connlimit/connlimit_test.go | TestListenerReleasesSlotOnClose, waitForAccepted: deadline loops → require.Eventuallyf | listener slot release on Close; accepted-conn count | ok | the slot check still needs an accepted, held-open second conn (read times out); waitForAccepted's message no longer prints the count, the condition is unchanged |
| pkg/api/connlimit/quic_test.go | TestQUICListenerReleasesSlotOnConnectionClose: 200 ms sleep → wait on the server conn's Context().Done, then on the counter emptying | context.AfterFunc(conn.Context(), release) | ok | stronger: the next dial now starts only after the release ran |
| pkg/api/etcd/watch_compacted_test.go | the bare-stream control split out of TestSerialWatchStream_SendsOneAtATime into TestSerialWatchStream_DetectorSeesUnserializedOverlap (synctest bubble) | serialWatchStream mutex vs overlapDetectingStream | ok | the serialised test still runs in real time (a mutex wait is not durable in a bubble); the control's overlap is now certain, not likely |
| pkg/api/grpc/enable_encryption_ctx_test.go | TestBarrierEnableEncryption_MigrationKeepsTheRequestValues: sleep + one read → assert.Never over 50 ms | migration ctx detached from the request ctx | ok | Never samples the whole window |
| pkg/api/quic/replica_forward_test.go, server_stop_stream_race_test.go | dead-target cache eviction, awaitAcceptClosing → require.Eventuallyf | forwarder.cached, srv.acceptClosing | ok | same condition and bound |
| pkg/api/rest/server_options_test.go | TestNewSPAHandler_ReturnsNonNil removed | declared return type http.Handler | ok | 566-audit-5 |
| pkg/backend/group_commit_test.go | five GroupCommit tests → synctest bubbles; NoAckBeforeItsSync also asserts fake elapsed >= groups × syncDelay; the panic test's 5 s guard is fake time | LocalBackend group commit applier / token | ok | stronger: an ack that skipped its sync would now also fail the elapsed bound |
| pkg/bootstrap/concurrent_role_seed_test.go, pkg/firstboot/role_race_cluster_test.go | waitShardReadableWithin → require.EventuallyWithT over every host, answered hosts skipped | quicraft host ShardMembership | ok | one bound over all hosts as before; the last error is reported |
| pkg/cert/watch_eviction_test.go, pkg/cluster/featuregate/featuregate_test.go, pkg/replicaforward/invalidate_test.go | pollUntil, eventually helpers → require.Eventually | as each caller | ok | same condition, bound and message |
| pkg/clienttrust/pool_test.go | HandshakeNeverWaitsOnABlockedRead, Refresh_SetDuringFlightWins, Run_RebuildsOnInvalidate → Eventually / EventuallyWithT | Pool rebuild → CACertPEMs; Run on Invalidate → VerifyPeerChain | ok | cancel is now deferred so a timeout still stops Run |
| pkg/cluster/membership_test.go | TestMembershipManager_StartOutlivesStartupCtx → EventuallyWithT GreaterOrEqual 3 puts | membership refresh loop | ok | same condition and bound |
| pkg/clusterenroll/join_test.go | TestJoin_TokenlessApprovalQueue_PollsUntilApproved → require.Eventually; a list error ends the poll and fails via require.NoError | ListPendingJoinRequests | ok | a list error still fails the test |
| pkg/credentials/retrieve_buffer_test.go, waiter_cancel_test.go | waiter-count polls → EventuallyWithT / require.Eventually; waitUntil helper deleted | manual retrieval waiter registry | ok | same exact-count condition; waitUntil had no other caller |
| pkg/dao/dynamic_entity_test.go, pkg/database/database_test.go, factory_test.go, pkg/seal/splitter_test.go, pkg/sql/provider/index_adapter_test.go, relational_ddl_test.go, relational_table_ddl_test.go, relational_table_test.go, pkg/statemachine/kv_adapter_test.go | Implements / InterfaceCompliance / NotNil tests removed or trimmed to package guards | production `var _` guards, declared return types, identity tests | ok | 566-audit-4, 566-audit-5; relational_ddl's positive control for the read-only guard is kept by readonly_database_test.go |
| pkg/database/backend_standalone_multiprocess_test.go | TestMultiProcessHelper start-file wait → EventuallyWithT | child process start signal | ok | same condition and bound |
| pkg/discovery/manager_test.go | TestManager_DiscoverCancelledMidRetry: 50 ms timer → cancel once m1 has been called | Manager.Discover retry wait → ctx.Done | ok | still asserts a manager CancelledError wrapping context.Canceled within 1 s of a 10 s interval |
| pkg/discovery/multicast_test.go | fakeTransport reads wake on arrival or a pacing ticker and count reads; MinPeersWaitsForEnough checks Discover still waiting at one peer; StoppedDuringSleep, AnnounceLoopStopsOnSignal, AnnouncementTimestampIsFresh, AnnouncementsNotRejectedAsReplay → synctest bubbles; ListenHandlesReadError and ListenLoopSetReadDeadlineError wait on read / deadline counters | MulticastDiscovery listen / announce / Discover | ok | stronger: the negative tests now prove the loop ran before asserting 0 peers, and the stop test watches 200 ms of fake time after Stop |
| pkg/discovery/token_test.go | TestTokenDiscovery_Stop: 50 ms timer → wait on a poll counter | token discovery node-list poll | ok | Stop now lands on a running poll |
| pkg/embedding/client_test.go | TestEmbed_CancelledContext handler: 500 ms sleep → block until the request ends | client request with a cancelled ctx | ok | the handler never answers, so only the client's cancel can end the call |
| pkg/engine/mvcc_engine_test.go, pkg/store/coverage_test.go, pebble_extended_test.go | Lookup_LeaseKeepAlive → EventuallyWithT then the lease ID asserted | LeaseKeepAliveQuery lookup | ok | the ID check runs on the successful attempt's result |
| pkg/firstboot/admin_bound_watch_test.go | watcher-registration and awaitNotAwaiting loops → require.Eventually(f) | watch hub WatchCount, probe state | ok | same condition and bound |
| pkg/index/extended_memory_index_test.go | TestExtendedMemoryIndex_ContextCancellation: 1 ms sleep → `<-ctx2.Done()` | ScanWithContext on a 1 ns timeout ctx | ok | waits on the expiry itself |
| pkg/rowlock/faults_test.go, harness_test.go, metrics_test.go | lost-lease, waitQueued, waitWaiting → require.Eventually(f) | keepalive lost flag, queue records, waiter gauge | ok | same condition and bound |
| pkg/rowlock/session_test.go | IdleLeaseIsRevoked → require.Eventuallyf; LeaseInUseIsNotIdle: 1.5 s sleep → lessor on a clocktest fake clock, four half-TTL steps each after a renewal that began at that instant | Session keepalive → lessor expiry (renewalLease signals after a successful KeepAlive) | ok | stronger: the lock is carried two TTLs past its grant, and every step waits for an observed renewal |
| pkg/sql/internal/sessionguard/sessionguard_test.go | waitClosed waits on a close channel; SealedSkipsTheAdmission → EventuallyWithT with a deferred Stop; StreamEndClosesOnlyItsTenantsSessions, Release_ForgetsTheSession → synctest bubbles with 20 ms fake absence windows then synctest.Wait | guard watch loops / endAll / Release | ok | the absence checks now run after every guard goroutine has settled |
| pkg/sql/mysql/idle_timeout_wire_test.go, row_lock_wire_test.go, sealed_cursor_wire_test.go, sealed_session_wire_test.go; pkg/sql/pgsql/sealed_session_wire_test.go | lock-release and seal-close loops → EventuallyWithT / assert.Eventually | wire server seal / idle / disconnect paths | ok | sealed_cursor still checks the final error with requireSealedEnd; the transaction test still rolls back before failing |
| pkg/statemachine/raft_keylength_test.go, replica_storage_cluster_test.go | propose retry loops → EventuallyWithT on quietReporter with a mutex-guarded lastError | SyncPropose across hosts | ok | the caller still fails on the last error; the guard covers a late attempt after a timeout |
| pkg/trustanchor/kv_atomic_test.go | TestKVRegistry_ConcurrentAddNeverExceedsTheCap → synctest bubble, registry clock pinned to the bundles' issue time; also asserts the cap is filled | KVRegistry.Add read-then-write | ok | stronger: every reader reads before any write, and an Add that refused everything now fails |
| pkg/volume/reporter_test.go | TestReporter_StartRetriesAFailedInitialReport → EventuallyWithT on err and raw | reporter refresh loop | ok | same condition and bound |
| test/clusterproof/nemesis/target_test.go | waitState | process state poll | ok | ticker only |
| test/clusterproof/soak/check_test.go, config_test.go, soak_test.go | Run and Main in synctest bubbles (bubbleRun, runElapsed); TestRunCleanIsProven asserts fake elapsed >= o.Duration | soak Run pacing over the fake store | ok | stronger: the clean run is proven to have paced its full duration |
| test/integration/helpers_test.go, isolation_raft_cluster_test.go, tenant_undo_reuse_cli_test.go | deadline loops → pollUntil | admin trust / status / tenant create per protocol | ok | each caller keeps its failure (or, in isolation_raft_cluster, its following assertion) as before |
