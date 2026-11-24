# #1613 — SPEC row `verifiable-log`: audit against main

Objective: audit each requirement of the `verifiable-log` row in
[../SPEC.md](../SPEC.md) against main, close what is small enough, and mark
the row shipped only when every line HOLDS.

## Audit (step 1)

| # | Requirement | Verdict | Proof |
|---|---|---|---|
| 1 | Engine surface: create, append, get, proof-inclusion, proof-consistency, head | HOLDS | `pkg/service/vlog_log_service_test.go`: `TestLogService_CreateLog_ProposesTheDescriptor`, `TestLogService_Append_ReturnsTheFirstIndex`, `TestLogService_GetEntries_ReturnsThePage`, `TestLogService_ProveInclusion_CarriesBothSizes`, `TestLogService_ProveConsistency_CarriesBothSizes`, `TestLogService_Head_ReadsTheHead` |
| 2 | Sign-head hook | HOLDS | `pkg/service/vlog_log_service_test.go:TestLogService_SignHead_RecordsTheCheckpoint`; `pkg/vlog/signing_software_test.go:TestSignHead_Refusals` |
| 3 | Served on gRPC, QUIC, Unix, REST, MCP (and embedded) | HOLDS | `test/integration/vlog_cli_test.go:TestVlog_CLI_PerProtocol` (every verb over `multiProtocols`); `pkg/api/{grpc,quic,rest,mcp,unix}/*vlog*_test.go` |
| 4 | sdk/go | HOLDS | `sdk/go/transport/*/transport_vlog_test.go` over `sdk/go/transport/internal/vlogtest` round trip; `sdk/go/types_vlog.go` |
| 5 | TypeScript SDK | HOLDS | `sdk/typescript/test/vlog-contract.test.ts` (REST and Wails clients) |
| 6 | Tenant isolation | HOLDS | `pkg/service/vlog_log_service_test.go:TestLogService_CreateLog_Refusals` (cross-tenant); `test/integration/vlog_tenant_confinement_cli_test.go:TestVlog_CLI_TenantConfinement_PerProtocol` (closed by step 2a) |
| 7 | DEK | HOLDS (by design) | `docs/databases/architecture.md` §8: no second key layer; a caller encrypts entries with `DEKService` before appending, the leaf commits to the ciphertext |
| 8 | Barrier | HOLDS | Barrier tenant HOLDS in Raft mode: its logs, entries and sealed signing keys are applied in its home group, under its own key; a sealed barrier refuses with `tenant.ErrTenantSealed` (`pkg/service/audit_shard_resolver_test.go:TestSystemShardLogResolver_RoutesBarrierTenantsToTheirHomeGroup`; `pkg/server/lifecycle/log_groups_test.go:TestLogGroups_TenantHomeGroup`; `test/integration/vlog_barrier_tenant_cli_test.go:TestVlog_CLI_BarrierTenant_PerProtocol`, every transport: sealed refused, unsealed create/append/get/prove-inclusion/prove-consistency/head, log unchanged after the unseal). Barrier database HOLDS on every backend (owner ruling 1234, option A): its logs live in the group its tenant resolves to (on a standalone node, the node's local store), proposed through `barrierDatabaseTarget` (`pkg/service/vlog_database_store.go`) with each entry body and software key sealed under the database's barrier key (sub-types 8, 9 and 10, `featuregate.VerifiableLogSealed`); a sealed database refuses with `*service.BarrierSealedError`, a legacy raft barrier record with `*database.BarrierOffDataPathError` (`pkg/service/vlog_log_service_test.go:TestLogService_ServesABarrierDatabase`, `TestLogService_BarrierDatabaseLogsLiveInTheParentGroup`, `TestLogService_SealedBarrierDatabaseRefusesWhileItsGroupApplies`, `TestLogService_BarrierDatabaseAppendNeedsTheSealedFeature`, `TestLogService_BarrierDatabaseDropReclaimsItsLogsInTheParentGroup`, `TestLogService_BarrierDatabaseLifecycle`, `TestLogService_RefusesALegacyRaftBarrierRecord`; `pkg/statemachine/vlog_apply_test.go:TestApplyVerifiableLog_SealedKeyRecordsAreStoredUnopened`; `pkg/server/lifecycle/seams_vlog_test.go:TestBuildServices_LogServiceSealsWithTheBarrierDatabase`; `test/integration/vlog_barrier_tenant_cli_test.go:TestVlog_CLI_BarrierDatabase_PerProtocol` (clustered) and `TestVlog_CLI_BarrierDatabase_Standalone_PerProtocol` (standalone), every transport: unsealed every verb and its proofs, no entry in plaintext under the node's data root or the database's data directory, sealed refused, wrong secret stays refused, unsealed log unchanged and grows, gone after drop). Barrier tenant HOLDS on a standalone node: its logs are applied to and read from its own standalone barrier store (`pkg/service/vlog_barrier_store.go`, attached by `AttachLocalTenantHomes`) (`pkg/service/vlog_barrier_store_test.go:TestLogService_ServesABarrierTenantOnItsStandaloneBarrier`, `TestLogService_SealedStandaloneBarrierTenantRefuses`; `test/integration/vlog_barrier_tenant_cli_test.go:TestVlog_CLI_BarrierTenant_Standalone_PerProtocol`). A barrier tenant's audit trail stays on the system shard, where its audit logger writes and the log service reads it (review fixes F2, F3 and the audit P1 below). The kv surface refuses a barrier database's reserved log keyspace (F1), and a dropped database's logs are reclaimed (F4). |
| 9 | Replication | HOLDS | `sdk/go/node/clustered_vlog_test.go:TestClusteredCreateFunc_AppliesVerifiableLog`, `TestClusteredCreateFunc_LogRegistryPerReplica`; `pkg/store/vlog_apply_test.go:TestStoreVerifiableLog_SnapshotMidLogMatchesAPeer` |
| 10 | Standalone and Raft modes | HOLDS | standalone: `pkg/engine/mvcc_vlog_test.go:TestMVCCEngine_VerifiableLog_DispatchesAndAnswersReads`; Raft: `pkg/store/vlog_apply_test.go:TestStoreVerifiableLog_DispatchesAndAnswersReads`; the audit instance on a standalone node (rw1613w6): `test/integration/audit_cli_test.go:TestAudit_CLI_AuditInstance_Standalone_PerProtocol` |
| 11 | One apply-time opcode behind a feature gate | HOLDS | `protocol.OpVerifiableLog` (`pkg/protocol/vlog.go`), `featuregate.VerifiableLog`/`VerifiableLogKeys`; `pkg/service/vlog_log_service_test.go:TestLogService_Writes_RefusedBeforeGateActive`, `TestLogService_KeyWrites_RefusedBeforeKeyFeatureActive`; `pkg/store/vlog_apply_test.go:TestStoreAuditInstance_MixedVersionReplay` |
| 12 | Dispatched by both state-machine families | HOLDS | `statemachine.ApplyVerifiableLog` from `pkg/engine/mvcc_engine.go` and `pkg/store/pebble.go`; `TestMVCCEngine_VerifiableLog_RefusalVersusHalt`, `TestStoreVerifiableLog_RefusalVersusHalt`, `TestStoreAuditInstance_FamiliesAgree` |
| 13 | Sequence assigned at apply | HOLDS | `pkg/vlog/audit_instance_test.go:TestAuditInstance_DeterministicAcrossBatchPacking`; `pkg/store/vlog_apply_test.go:TestStoreVerifiableLog_ReopenMatchesAPeer` |
| 14 | Head signature produced outside apply, appended as a record | HOLDS | `LogService.SignHead` signs, then proposes the `Checkpoint` sub-type; `TestLogService_SignHead_RecordsTheCheckpoint`; `pkg/vlog/checkpoint_test.go` |
| 15 | `vlog.HeadSignerProvider` via `WithHeadSignerProvider` on the server and `sdk/go/node` | HOLDS | `pkg/server/seam_options_test.go:TestResolveCryptoOptions_SurfacesTheHeadSignerProvider`; `sdk/go/node/seams_test.go:TestMergeSeamOptions_FoldsNodeOptionsIntoTheServerOptions`; `sdk/go/server/headsigner_test.go:TestWithHeadSignerProvider_ReachesCryptoOptions`; `pkg/server/lifecycle/seams_vlog_test.go:TestBuildServices_HeadSignerProviderCreatesTheLogsKeys` |
| 16 | Software provider (key as the log's sealed record) is the default | HOLDS | `pkg/vlog/signing_software_test.go:TestSoftwareHeadSignerProvider_CreateKeyIsTheSealedRecord`; `TestNewLogService_DefaultsToTheSoftwareProvider`; `TestBuildServices_HeadSignerProviderAbsentKeepsTheSoftwareProvider`; `sdk/go/node/seams_test.go:TestWithHeadSignerProvider_AbsentKeepsTheSoftwareProvider` |
| 17 | The audit trail is one log instance | HOLDS | `pkg/vlog/audit_instance_test.go:TestAuditInstance_SwitchStartsTheTreeAtS0`; `pkg/store/vlog_apply_test.go:TestStoreAuditInstance_FamiliesAgree`; `pkg/service/vlog_log_service_audit_test.go:TestLogService_ReadsTheAuditInstance`. Raft mode: `test/integration/audit_cli_test.go:TestAudit_CLI_AuditInstance_PerProtocol`. Standalone (rw1613w6): `pkg/service/audit_replicated_logger_test.go:TestReplicatedLogger_Emit_UngatedHostProposesToTheAuditInstance`; `pkg/server/lifecycle/seams_vlog_test.go:TestBuildServices_StandaloneBarrierTenantsAuditIsWrittenToTheSystemShard`, `TestBuildServices_StandaloneAuditSwitchKeepsTheChainContinuous`; `test/integration/audit_cli_test.go:TestAudit_CLI_AuditInstance_Standalone_PerProtocol` |

## Slices (step 2)

a. Row 6 (done): `test/integration/vlog_tenant_confinement_cli_test.go`,
   every transport: two prefix tenants' identities; the same log name in each
   is two logs with their own heads; a log one tenant created is neither
   listed, read nor appended to by the other (the CLI carries no tenant flag:
   the caller's certificate names the tenant). Listed in `INTTEST_databases`.
b. Row 8 (barrier), lane vl1613c. No new seal domain: the logs go to the
   barrier-sealed Raft groups the node already runs, whose state machines
   carry a log registry (snapshot and restore follow the group).
   - `service.NewSystemShardLogResolver` takes a `service.LogGroupSource`;
     the log service routes a barrier tenant's logs to `TenantHomeGroup` (its
     `__home` dedicated group). The audit logger keeps
     `service.NewSystemShardAuditResolver`, which sends a barrier tenant to
     the system shard as on main, and the log service reads the reserved
     `system` database through the same resolver
     (`LogServiceConfig.AuditResolver`), so the audit trail is read where it
     is written.
   - `pkg/server/lifecycle/log_groups.go` implements the source over the
     dedicated-Raft services `AttachTenantHomes` attaches (attached lazily:
     `BuildServices` runs first). Sealed tenant barrier →
     `tenant.ErrTenantSealed`; no group → `BarrierStoreUnavailableError`
     (`Unavailable`).
   - The per-database route of the first cut (`LogShardResolver`,
     `ResolveDatabaseLogShard`, `DatabaseGroup`) is deleted (Codex round 1,
     P1 #1 and #2: a dedicated Raft database's group is not under the
     database's key, and a legacy record could reach the system shard); a
     barrier-enabled database's log is served by slice c.
   - `BarrierTenantLogNotSupportedError` and its errclass row are deleted.
   - The REST transport sends the database scope header on `/vlog` requests;
     before, `--database` never reached the server over REST.
   - CLI E2E: `test/integration/vlog_barrier_tenant_cli_test.go`, listed in
     `INTTEST_databases`.
c. Row 8 (barrier database), this PR, lanes fx1655h and fx1655i, replaced
   by rw1613w3 (owner ruling 1234, option A) and closed for standalone
   nodes by rw1613w4.
   - `pkg/service/vlog_database_store.go`: a barrier-enabled database's logs
     live in the group its tenant resolves to (`r.raft(ctx, tenantID)`; on a
     standalone node, the node's local store). `barrierDatabaseTarget` wraps
     that group's target: entry bodies and software keys are sealed under
     the database's barrier key (sub-types 8, 9, 10), so they are unreadable
     while it is sealed and reclaimed in the parent group when it is dropped.
     A sealed database refuses with `BarrierSealedError` (`Unavailable`); a
     legacy raft barrier record with `database.BarrierOffDataPathError`
     (`Unprocessable`). The first cut's per-database barrier store
     (`databaseLogStore`) is deleted.
   - The log service's route resolves each database incarnation to its log
     target (`logTarget`: `barrierDatabaseTarget` over the parent group, a
     barrier tenant's standalone store, or `raftTarget`, the group).
   - The barrier-database refusal error of slice b and its errclass row are
     deleted.
   - CLI E2E in `test/integration/vlog_barrier_tenant_cli_test.go`, every
     transport: `TestVlog_CLI_BarrierDatabase_PerProtocol` (clustered) and
     `TestVlog_CLI_BarrierDatabase_Standalone_PerProtocol` (standalone):
     served, no plaintext under the data root, sealed, wrong secret,
     unsealed, dropped and re-created.

d. Review fixes, lane fx1655n (PR #1655).
   - F1, `e17340317`: the kv surface refuses a key in, or a delete range over,
     a barrier database's reserved verifiable-log keyspace with
     `service.ReservedKeyError` (`InvalidArgument`)
     (`pkg/service/backend_kv_reserved_test.go:TestBackendKV_RefusesABarrierDatabasesLogKeyspace`,
     `TestRefuseReservedRange`).
   - F2, `5ace0aea3`: the audit logger routes a barrier tenant's audit events
     to the system shard, as on main
     (`pkg/service/audit_shard_resolver_test.go:TestSystemShardAuditResolver_RoutesBarrierTenantsToTheSystemShard`).
   - Audit P1, `b22b18c59`: the log service reads the reserved `system`
     database through the audit logger's resolver, so a barrier tenant's audit
     instance is read, proved and listed where it is written, and stays
     readable while the tenant is sealed
     (`pkg/service/vlog_log_service_audit_test.go:TestLogService_ReadsABarrierTenantsAuditInstanceWhereItsLoggerWrites`,
     `TestLogService_SealedBarrierTenantKeepsItsAuditTrailOnTheSystemShard`).
   - F3, `cefa8202a`: on a composed standalone node a barrier tenant's audit
     events are written to the system shard and the log service reaches its
     audit instance there
     (`pkg/server/lifecycle/seams_vlog_test.go:TestBuildServices_StandaloneBarrierTenantsAuditIsWrittenToTheSystemShard`).
   - F4, `849a7a35d`: dropping a database reclaims its logs, so a re-created
     name starts empty (`pkg/vlog/named_test.go:TestChain_DropDatabase_ReclaimsEveryLogOfTheDatabase`;
     `pkg/service/vlog_log_service_test.go:TestDatabaseService_DropReclaimsTheDatabasesLogs`,
     `TestLogService_DropDatabaseLogs_ProposesUntilNothingRemains`;
     `pkg/service/vlog_barrier_store_test.go:TestLogService_DropDatabaseLogsOnAStandaloneBarrierReclaimsEveryLog`).
   - Barrier-database P1: served from the parent group (or a standalone
     node's local store), sealed under the database's key (slice c;
     `TestLogService_ServesABarrierDatabase`,
     `TestLogService_BarrierDatabaseLogsLiveInTheParentGroup`,
     `TestLogService_BarrierDatabaseLifecycle`).

Migration note: a barrier tenant's audit trail is on the system shard, under
the system barrier, as on main. Moving it into the tenant's home group would
leave the instance already on the system shard behind, so it needs a
coordinated migration of the existing audit instance, not a routing change.

## Step 3

Every line of the row HOLDS. `docs/SPEC.md`, `pm/rows.md` and ROADMAP.md are
paths this lane may not edit, so marking the row shipped, and moving
fsync-stall-leadership, lean-node-build and barrier-passphrase-change to
Complete (SPEC marks them shipped), is left to a lane launched with them
allowed.

## Codex verify at 28ecaf0d9b

- F1 (system database's logs on the system shard): only the system tenant's
  audit instance (`system`/`vlog.AuditLogID`) goes to the system shard; every
  other log of a barrier tenant, in the `system` database too, goes to its
  own group or barrier store and is refused while the barrier is sealed.
- F2 (`__vlog/` outside the migration): `MigrateEncryption` stages, journals,
  verifies and commits `service.BarrierLogKeyspace` beside the home store's
  records, and the standalone log store admits each operation through the
  home-store gate (`service.LogStoreAdmission`), so the migration fences and
  drains it.
- F3 (subtype 7 under `verifiable-log`): the drop sub-type is gated behind
  its own feature, `featuregate.VerifiableLogDrop`; a database with no logs
  proposes nothing, and one whose logs sit on a group without the feature is
  refused with `LogFeatureNotActiveError` before anything is fenced.
- F4 (reclamation after the catalog release): a dropped database's logs are
  reclaimed inside the fenced teardown, before `Manager.Drop` releases the
  name; subtype 7 carries the incarnation and deletes only that
  incarnation's logs, and a failed reclamation leaves the database fenced
  and droppable on retry.
- F5 (dedicated tenants' drops): a drop needs no log work for a tenant
  without verifiable logs (volume or raft isolation), so their drops succeed.
- Codex round 2 P1 (batch-dependent reclamation): `vlog.Chain.DropDatabase`
  walks committed storage merged with the current Update's staged writes and
  deletions, so a log created or appended earlier in the same apply batch is
  reclaimed whole and a record the batch already deleted is not counted
  again; packed and separate applies leave the same table
  (`TestChain_DropDatabase_PackedBatchMatchesSeparateUpdates`,
  `TestApplyVerifiableLog_AppendAndDropInOneBatch`).
- Codex round 3 P1-2 (writes and reads across a drop and re-create): a log
  write is fenced to the database incarnation it was admitted under.
  - `logService.admit` takes the live incarnation from
    `database.Manager.GetOwned`. A dropping database is refused at once.
  - Raft groups (shared, and dedicated raft): the command is wrapped in the
    drop-fence guard (`DropFenceGuard`, `OpGuarded`). The group's apply
    compares the marker `KeyspaceReclaimService` keeps in that same group, so
    a create paused in the signer across a drop and re-create is refused with
    `*database.DroppingError`, and stores no log and no software key record.
  - Standalone barrier store (no group): the incarnation is checked under
    `s.mu`, the lock the drop's reclamation (`DropDatabaseLogs`) takes too.
  - Reads: `vlog.IncarnationRequest` answers a log whose descriptor names an
    earlier incarnation with `LogNotFoundError`, and a list leaves it out.
  - Tests: `TestLogService_CreatePausedAcrossDropAndRecreateIsRefused`,
    `TestLogService_ReadRefusesAnEarlierIncarnationsLog` and
    `TestLogService_StandaloneBarrierStoreFencesAPausedCreate`. They live in
    the `qrdb_vlog`-tagged `pkg/service/vlog_log_service_test.go`, so run them
    with that tag.
  - Residuals:
    - A barrier database's own store is dropped with its backend, and gets
      only a pre-propose incarnation check, which is not atomic with the
      apply.
    - The audit instance and default-database logs are unfenced, since they
      cannot be dropped.
    - A legacy database with an empty incarnation is guarded as "marker
      absent".
    - An external (HSM/KMS) provider keeps a key it minted for a create
      that is then refused.
    - Append apply does not compare the descriptor's incarnation itself. The
      fence keeps stale logs from arising.

## mg1655a

- Merge: main `241f4cdb39` merged into the row branch (merge commit). The
  only textual conflict was `CHANGELOG.md` (both sides' Changed lines kept).
- Codex round 4 P1-a (mixed-version descriptors): a create records its
  database's incarnation only while `featuregate.VerifiableLogDrop` (shipped
  with this PR's wire) is active on the log's group; before that the log is
  created legacy (no incarnation), so every replica persists the same
  descriptor bytes. Picked over refusing the create, so a rolling upgrade
  keeps creating logs. A key rotation of a log that records an incarnation
  is refused with `LogFeatureNotActiveError` (`verifiable-log-drop`) until
  the feature is active; a legacy log rotates as before
  (`TestLogService_IncarnationWritesWaitForDropFeature`).
- Codex round 4 P1-b (legacy logs hidden): `vlog.IncarnationRequest` treats
  a descriptor without an incarnation as a legacy log of the database that
  holds its name: reads and lists answer it, append and rotation work, and
  a drop reclaims it through `DropDatabase` as before. No lazy stamping
  (`TestIncarnationRequest_LegacyLogIsVisible`, and the service test above).
- CLI E2E: `TestVlog_CLI_PerProtocol` run unchanged and green; the CLI
  cannot create a pre-upgrade log (a fresh node has every feature active).
- Out of this lane: Codex round 4 P1-c (barrier-tenant fence marker,
  `pkg/server/store_resolver.go`) belongs to lane fx1655c.
## fx1655c

Codex round 4 P1-3 at 9a8dd39821: a barrier tenant's logs are proposed to
its home group, but `storeResolver.FenceTarget` put the drop-fence marker of
its named databases on the system store, so the home group's state machine
refused a log write in a live database as `DroppingError`. fx1655c made
`FenceTarget` resolve the home group (`database.TenantHomeName`, through
`RaftBackendFactory.Services`) for barrier isolation, not per-database, and
keep the system store on a node with no home group (its standalone barrier
store checks the incarnation is live instead). No new key scheme.
Correction (rw1613d, Codex round 5 P1 1): moving the whole marker was wrong.
Only the log fence goes to the home group (`StoreResolver.LogFenceTarget`);
the data fence stays on the system store, where the tenant's database data
is served and `sharedStoreFence` guards it.

- Tests: `TestStoreResolver_LogFenceTargetBarrierTenantUsesItsHomeGroup`
  (renamed from `TestStoreResolver_FenceTargetBarrierTenantUsesItsHomeGroup`
  in rw1613d),
  `TestLogService_BarrierTenantNamedDatabaseIsFencedInItsHomeGroup`, and the
  named-database step of `TestVlog_CLI_BarrierTenant_PerProtocol`, which
  fails with "database is being dropped" without the fix.
- Residual: a barrier database's logs go to its own sealed store, which
  checks incarnation liveness and reads no marker; its marker stays where
  the tenant's isolation puts it (no sealed-store fence target added).

## rw1613c

The barrier CLI e2e re-run after the fx1655c home-group fence
(`-tags=codec_json,raft,integration`, foreground, one target at a time), each
on rest, grpc, quic, mcp and unix:

- `TestVlog_CLI_BarrierTenant_PerProtocol` (raft tenant, home group, sealed
  refusal, every verb and proof, named-database live + drop/re-create): pass
  on all five.
- `TestVlog_CLI_BarrierTenant_Standalone_PerProtocol` (standalone barrier
  store, `FenceTarget` keeps the system store): pass on all five.
- `TestVlog_CLI_BarrierDatabase_PerProtocol` (the database's own barrier
  store, drop and re-create under a new data dir): pass on all five.

No failure, so no code or test change; `TestVlog_CLI_PerProtocol` ran green
in mg1655a.

## Remains

- CLOSED by rw1613w6, Tenant item (F3 proof half): a standalone node
  composes an ungated host, whose replicated audit logger kept
  `OpAuditAppend`, so there was no audit instance to prove. In the
  `qrdb_vlog` build `ReplicatedLogger.Emit` now writes an ungated host's
  trail as the tenant's audit instance; no `cmd/qrdbd` change was needed.
  Tests: `TestReplicatedLogger_Emit_UngatedHostProposesToTheAuditInstance`,
  `TestBuildServices_StandaloneBarrierTenantsAuditIsWrittenToTheSystemShard`,
  `TestBuildServices_StandaloneAuditSwitchKeepsTheChainContinuous`,
  `TestAudit_CLI_AuditInstance_Standalone_PerProtocol`.
- Drop of a database, decided by convention (ruling 109): dropping a database
  never deletes a directory the caller chose; it reclaims only what qrdb
  created under its own data root (the logs in the store, the manager's
  per-database directory under the node's data directory), as PostgreSQL's
  DROP TABLESPACE leaves the tablespace's directory. The drop path already
  does this: `Manager.drop` (`pkg/database/manager.go`) removes only
  `m.dbDir`, `Chain.DropDatabase` (`pkg/vlog/named.go`) deletes store
  records only, and `pkg/service/database_service.go` removes no directory,
  so no code changed. `TestDatabaseService_DropLeavesACallerChosenDataDirInPlace`
  (`pkg/service/database_drop_dir_test.go`) proves a caller-chosen
  `--data-dir` and a file in it survive the drop. Re-creating a barrier
  database therefore takes a new `--data-dir` (the user guide and the CLI
  E2E do so).
- The SPEC, `pm/rows.md` and ROADMAP.md moves above: rw1613w7 had them
  authorised (owner ruling 1234), but the lane's policy guard refused
  `docs/SPEC.md` and `ROADMAP.md` (not in its `LANE_POLICY_ALLOW`), so all
  three are left for the coordinator with the exact text in "## rw1613w7".
- CLOSED by rw1613g: an untenanted (platform) database's verifiable logs
  survived its drop (rw1613f): `Manager.reclaimLogs` (`pkg/database/manager.go`) and
  `DatabaseService.checkLogs` skip a database whose tenant ID is empty, while
  the log service keeps its logs under the `system` tenant
  (`routingTenant`). Fix: drop both empty-tenant guards and route the tenant
  through `routingTenant` in `logService.DropDatabaseLogs` and
  `CheckDropDatabaseLogs`. `pkg/database` was outside rw1613f's file map.

## rw1613d (Codex round 5 at 928f3d8a41)

- P1 1: fx1655c put a barrier tenant's whole drop-fence marker in its home
  group, so its database data, served from the system store, lost its fence.
  `FenceTarget` answers the system store again (data fence,
  `sharedStoreFence`); the new `StoreResolver.LogFenceTarget` answers the
  home group (log fence). The reclaim service marks the log marker live on
  create, tombstones it before the data fence on drop and clears it when the
  reclamation ends. Fix 67692d823, tests f31428695:
  `TestStoreResolver_FenceTargetBarrierTenantUsesTheSystemStore`,
  `TestStoreResolver_LogFenceTargetBarrierTenantUsesItsHomeGroup`,
  `TestKeyspaceReclaim_BarrierTenantFencesDataAndLogMarkers`; the CLI e2e
  `TestVlog_CLI_BarrierTenant_PerProtocol` puts and gets a kv key in the
  named database across a drop and re-create (rw1613e granted the test
  identity `kv:put`/`kv:get`).
- P1 2: `SignHead` read the descriptor and head before admission, so a
  signer paused across a drop and re-create signed the old incarnation's
  head. It now admits first, reads through the admitted group under its
  incarnation and proposes through it (refused with `DroppingError`);
  `RotateKey` re-reads the descriptor under the admitted incarnation. Fix
  b3df53741, test 90211cc83:
  `TestLogService_SignHeadPausedAcrossDropAndRecreateIsRefused`.
- P1 3: a database drop deleted every completed log's descriptor and head
  outside the step budget. Both now count against `MaxPrunePerCommand`; a
  step that cannot cover the pair leaves it for the next. Fix 7e80ce1c0,
  test 878a45233: `TestChain_DropDatabase_EmptyLogsAreBounded`.

## rw1613f

- `TestVlog_CLI_DatabaseDrop_PerProtocol` (`test/integration/vlog_cli_test.go`),
  on every transport, two cases: a shared-store database (`database create`
  with no backend flag) and a dedicated-raft one (`--backend raft
  --cluster-mode local --replicas 1`). Each creates two logs, appends, signs,
  rotates, drops and re-creates the database, then expects neither log
  listed, `head` not found, and the same log name created afresh (key
  version 1, first index 0, head size 1, a checkpoint verifying under the
  new key).
- RED on every transport (wip bd2d1fe03): the test runs as an untenanted
  identity, whose database logs the drop never reclaims (see Remains); list
  and head hide them by incarnation, but `create` of the same name is
  refused "already exists". No fix landed in this slice (fixed by rw1613g).
- docs/databases/architecture.md: the logs step of "What a drop reclaims",
  the barrier tenant's log-only fence marker, and `SignHead` admitting first.

## rw1613g

- Failing first, each RED at cd4ace6eb:
  `TestManager_Drop_ReclaimLogsOfAnUntenantedDatabase`
  (`pkg/database/manager_fence_test.go`, the reclaimer was never called),
  `TestDatabaseService_DropOfAnUntenantedDatabaseReclaimsItsLogs` (no check,
  no reclaim) and
  `TestLogService_DropDatabaseLogs_UntenantedActsOnTheSystemTenant` (the drop
  command was refused "tenant id and database required")
  (`pkg/service/vlog_log_service_test.go`).
- Fix: `Manager.reclaimLogs` and `DatabaseService.checkLogs` no longer skip
  an empty tenant; `logService.DropDatabaseLogs` and `CheckDropDatabaseLogs`
  map the tenant through `routingTenant`, so an untenanted database's logs are
  listed and deleted under the `system` tenant. Tenanted drops are unchanged.
- `TestVlog_CLI_DatabaseDrop_PerProtocol` (rw1613f) now passes on every
  transport, shared-store and dedicated-raft, with no test change; the
  rw1613f gap is closed.
- docs/databases/architecture.md: "What a drop reclaims" and the log service
  row no longer call the untenanted case open.

## rw1613h

- CI on #1655's merge with main (ee91c9028d) failed
  `TestBuildServices_ReclaimerReachesEveryShapeADatabaseOwns`: the drop of an
  untenanted memory database was refused "service: read failed: engine:
  invalid query: unsupported query type". Reproduced at the merge and at the
  row tip ab80ca2fd; green at cd4ace6eb. The cause is rw1613g, not #1705's
  go-codec move: an untenanted drop now checks and reclaims logs, and the
  test's host runs the "kv" engine (also an opt-in embedded engine), whose
  Lookup has no verifiable-log case, so `dropGroup`'s list read was refused.
- Fix: `logService.dropGroup` reads the unsupported-query-type refusal (the
  duck-typed `unsupportedQueryTypeErr` the MVCC service already uses) as "this
  group holds no log": nothing to check, reclaim or propose. Every other read
  refusal still refuses the drop.
- Tests: `TestLogService_DropDatabaseLogs_GroupWithNoLogCaseHasNoneToReclaim`
  (new); `TestBuildServices_ReclaimerReachesEveryShapeADatabaseOwns`, the
  service drop/log tests, the lifecycle package and
  `TestVlog_CLI_DatabaseDrop_PerProtocol` pass on the full tag set.

## rw1613p and rw1613w1 (CI red and Codex round 2 at b50e2f2b95)

- CI red at b50e2f2b95 ("Unit [make coverage-leg service]", "Unit [make
  test-tags]"): `TestKeyspaceReclaim_BarrierTenantFencesDataAndLogMarkers`.
  Keeping the log tombstone at reclaim dropped the only step that reported
  a stale drop superseded when no data was left to refuse it. Fix
  (f6700ef94, `pkg/service/keyspace_reclaim.go`): the reclaim reads the
  home-group log marker without removing it; absent or this drop's
  tombstone passes, a successor's live marker or another drop's tombstone
  is `DropSupersededError`.
- Codex round 2 P1 (`vlog_barrier_store.go` proposeFenced): `CreateLog`
  keeps its admitted standalone barrier store across `createKey`; an
  encryption migration that commits meanwhile
  (`BarrierRegistry.ReplaceTenantBarrier`) leaves the old barrier unsealed,
  so the create wrote descriptor and head under the obsolete key. Fix: the
  store keeps its `LogBarrierStoreSource` and, after admission and under
  its lock, refuses with `ErrLogStoreReplaced` (class Unavailable; the
  caller retries) when the source no longer holds its barrier, dropping
  the chain; the route already rebuilds the store on the new barrier.
- Tests: `TestLogService_StandaloneBarrierCreateAcrossMigrationRefusesTheReplacedBarrier`
  (new; failed before the fix with the create succeeding through the old
  barrier); `pausingSigners` pauses only its first `CreateKey`;
  pkg/service on the default and the four test-tags sets, pkg/api/errclass,
  `TestVlog_CLI_DatabaseDrop_PerProtocol` and
  `TestVlog_CLI_BarrierTenant_Standalone_PerProtocol` pass.

## rw1613w2 (Codex round 1 at 9428bf8de4)

The three P1s were fixed in b50e2f2b9; rw1613w2 gives each a test.

- P1-a, a pre-upgrade barrier tenant's database had no log marker in its
  home group, so every log write was refused `DroppingError` and its
  drop failed. Fix: `LogFenceInitializer`
  (`KeyspaceReclaimService.InitDatabaseLogFence`, wired in
  `lifecycle.BuildServices`) writes the live incarnation at the first
  log admission, never over a present marker, once per incarnation;
  `fenceLogMarker` tombstones an absent log marker on drop. Tests:
  `TestKeyspaceReclaim_PreUpgradeBarrierTenantLogMarker`,
  `TestLogService_PreUpgradeDatabaseLogFenceIsInitializedAtFirstLogWrite`.
  With `fenceMarker` in place of `fenceLogMarker` the reclaim test fails:
  the drop is refused `DropSupersededError`.
- P1-b, the reclamation cleared the log tombstone, readmitting a write
  paused under the empty incarnation. Fix: the tombstone stays until the
  next create claims it (`ownLogTombstone` reads it, never removes it).
  Tests: `TestKeyspaceReclaim_BarrierTenantFencesDataAndLogMarkers`
  (asserts the tombstone after the reclamation; it failed at
  9428bf8de4), and the pre-upgrade test above.
- P1-c, admission looked the database up twice and a failed second
  lookup admitted the write unfenced. Fix: `logRoute.resolve` returns
  the database its one lookup found and `admit` fences to its
  incarnation. Test: `TestLogService_AdmitFencesToItsOneDatabaseLookup`.
- docs/databases/architecture.md: the log tombstone outlives the
  reclamation; the pre-upgrade marker initialization.

## rw1613w3 (owner ruling 1234, option A)

A barrier-enabled database's logs move from its own barrier store into
the Raft group its tenant resolves to (`r.raft(ctx, tenantID)`).

- `barrierDatabaseTarget` (`pkg/service/vlog_database_store.go`) wraps
  the parent group's target. It holds the database's quiesce lease
  across each propose and refuses with `BarrierSealedError` while the
  database is sealed.
- What it proposes:
  - An append goes as sub-type 8 (`AppendSealed`). Each leaf hash is
    computed over the plaintext and each body is sealed with
    `BarrierBackend.SealValue`.
  - A software key's PKCS#8 is sealed and goes as the new sub-type 9
    (create) or 10 (rotate).
  - Apply stores the sealed records unopened (`vlog.CreateWithSealedKey`,
    `vlog.RotateSealedKey`). Sub-types 1 and 5 match the record against a
    parsed key, so they cannot carry a sealed one.
  - Reads open bodies and keys under the database's key.
  - Sealed writes need `featuregate.VerifiableLogSealed`. This includes
    a keyless create.
- `databaseLogStore` and the per-database store cache are deleted. A
  drop reclaims the logs in the parent group.
- Tests:
  - `TestLogService_BarrierDatabaseLogsLiveInTheParentGroup` failed
    before the change: the logs were in the database's backend.
  - `TestLogService_SealedBarrierDatabaseRefusesWhileItsGroupApplies`,
    `TestLogService_BarrierDatabaseAppendNeedsTheSealedFeature`,
    `TestLogService_BarrierDatabaseDropReclaimsItsLogsInTheParentGroup`
    and `TestApplyVerifiableLog_SealedKeyRecordsAreStoredUnopened` were
    written after the code.
  - `TestBuildServices_LogServiceSealsWithTheBarrierDatabase` now asserts
    that the system shard holds the records, with no entry in plaintext.
  - The CLI e2e (`TestVlog_CLI_BarrierDatabase_PerProtocol`) scans the
    node's data root as well as the database's directory.
- Step 9: `pkg/server/lifecycle/tenant_home.go` routes only barrier
  *tenants* (`service.BarrierLogKeyspace`), so it needs no change.
- Step 10: a database barrier's rekey (`DatabaseBarrierRekeyStart`,
  `seal.RekeyParams`) re-wraps the root key under a fresh unseal key.
  The root key never rotates and no stored value is re-encrypted
  (`pkg/seal/unsealkey.go`), so sealed log records stay readable.
- Open: there is no re-encryption of sealed log records on root-key
  compromise, because the barrier offers none for any value.

## rw1613w4 (option A on a standalone node; proof list against the tip)

- Standalone result: `TestVlog_CLI_BarrierDatabase_Standalone_PerProtocol`
  (`test/integration/vlog_barrier_tenant_cli_test.go`) boots a standalone
  node (`startStandaloneModeNode`, pebble) and runs the clustered test's body
  on every transport: create a barrier database, unsealed
  create/append/get/prove-inclusion/prove-consistency/head/sign-head, no
  entry in plaintext under the node's data root or the database's directory,
  sealed refused, wrong secret refused, unsealed log unchanged and grows,
  gone after drop. It passed first: `logRoute.resolve` wraps
  `r.raft(ctx, tenantID)`, and on a standalone node the resolver answers the
  node's local store, so `barrierDatabaseTarget` seals the logs there with
  no second store type. No code change, no CHANGELOG line.
- Stale text fixed: audit row 8 (parent group / local store, the rw1613w3
  tests; the deleted `TestLogService_BarrierDatabaseCommitFailureRestores`
  removed), slice c, the review-fix barrier-database bullet;
  `docs/audit/engineering.md` lists sub-types 1 to 10 for
  `pkg/protocol/vlog.go`; `docs/databases/architecture.md` and `user.md` name
  the standalone case and drop "its own store". The fx1655c and Codex-verify
  residuals above that mention the database's own store are history,
  superseded by rw1613w3. Left: `docs/databases/api.md` (the "own barrier"
  sentence of the log section) is outside this lane's paths.
- Proof list: every test named in rows 1 to 17 was found on the tip, except
  row 15's `TestWithHeadSignerProvider_ReachesTheServer`, now named by the
  tests that prove it (`TestResolveCryptoOptions_SurfacesTheHeadSignerProvider`,
  `TestMergeSeamOptions_FoldsNodeOptionsIntoTheServerOptions`,
  `TestWithHeadSignerProvider_ReachesCryptoOptions`), and row 9's elided
  `TestClusteredCreateFunc_LogRegistryPerReplica` written out.
- Commands, each passing: `go test -count=1 -tags=codec_json,raft,qrdb_vlog
  -run '^(...)$'` over the named tests of `./pkg/service/` (22),
  `./pkg/vlog/`, `./pkg/server/lifecycle/`, `./pkg/store/`, `./pkg/engine/`,
  `./pkg/statemachine/`, `./pkg/server/`, and in `sdk/go` `./node/` and
  `./server/` (23 between them). Without `qrdb_vlog` the `seams_vlog_test.go`
  tests compile out, so run them with it. Integration
  (`-tags=codec_json,raft,integration`): the six barrier, drop and per-verb
  CLI tests and `TestVlog_CLI_TenantConfinement_PerProtocol`, every transport.
- `./pkg/service/` whole: green on the Makefile's `BUILD_TAGS` (`codec_json
  raft qrdb_ts qrdb_vector qrdb_vlog qrdb_sql qrdb_mysql qrdb_pgsql qrdb_etcd
  qrdb_embedded`). With `-tags=codec_json,raft` alone the vector tests fail
  "vector support not compiled" (they need `qrdb_vector`); this lane changed
  no Go code in the package, so that is the tag set, not a regression.

## rw1613w5 (Codex round 5 at 06a8907fc0)

- P1-1, a failed create strands the home-group log marker:
  `KeyspaceReclaimService.UnfenceDatabaseKeyspace` marked a barrier tenant's
  home-group log marker live, then claimed the data marker (system store or
  per-database store); a failure there released only the data marker, and
  the create's undo left the log marker live under an incarnation no catalog
  holds, refusing every later create of the name. Fix: the log marker is read
  before it is marked live; when the data claim (`unfenceDataMarker`:
  `claimReclaimStore`, `clearMarker` or the system `markLive`) fails,
  `releaseLogClaim` swaps it from the incarnation back to its prior value, or
  deletes it when it was absent, under `database.DropRollbackContext`; a
  failed release is a `MarkerReleaseError`. A marker that already held the
  incarnation is left. Test `TestUnfence_FailedDataClaimReleasesTheLogMarker`
  (fresh name, after a drop, already live): failed before the fix (marker
  still present / `"inc-a"` instead of the tombstone), passes after, and the
  retry with a new incarnation holds both markers.
- P1-2, the per-incarnation log-store cache outlives a drop: its cache part
  closed with rw1613w3's deletion of `databaseLogStore` and its stores map.
  The residue, `logRoute.inited` (one entry per incarnation, never removed),
  is now emptied for (tenant, name) by `logService.DropDatabaseLogs` once
  the drop succeeds (`logRoute.forgetFences`); no new cache. Test
  `TestLogService_DropForgetsTheLogFenceIncarnation`: failed before the fix
  (one entry after the drop), passes after.
- `docs/databases/api.md`: the "database's own barrier store" paragraph now
  says the parent group or the standalone node's local store, sealed under
  the database's barrier key. `architecture.md` describes no create rollback
  of the markers and is unchanged; CHANGELOG lists no unreleased fixes.
- Commands, each passing: `gofmt -l pkg/service` (empty);
  `go test -count=1 -tags=codec_json,raft,qrdb_vlog -run
  '^(TestUnfence_|TestKeyspaceReclaim_|TestDatabaseCreate_|TestLogService_)'
  ./pkg/service/`; without `qrdb_vlog` the `TestLogService_` tests compile
  out (`vlog_log_service*.go` are `//go:build qrdb_vlog`). Integration
  (`-tags=codec_json,raft,integration,qrdb_vlog`): the four barrier-database,
  barrier-tenant and drop CLI tests, every transport.

## rw1613w6 (row finish)

- Slice 1, lint: gocritic `nilValReturn` at
  `pkg/service/vlog_database_store.go` (`barrierDatabaseTarget.read`, the
  `*vlog.SigningKey` case returned `got` inside `if got == nil`). The typed
  nil is kept, now written `(*vlog.SigningKey)(nil)`: a bare `nil` would turn
  into an untyped nil in the `any` result and change what
  `logRead[*vlog.SigningKey]`'s assertion sees. Test
  `TestLogService_BarrierDatabaseReadOfAMissingKeyIsNil` (no key and a
  `vlog.KeyNotFoundError` for a missing version) passes before and after,
  showing the behavior did not change.
- Slice 2, the audit trail on a standalone node: the daemon wiring
  (`cmd/qrdbd`) was not the gap. `ReplicatedLogger.Emit` wrote the audit
  instance only on a `FeatureGatedHost` with `VerifiableLogAudit` on, and a
  standalone node's `LocalBackend` gates nothing, so it kept `OpAuditAppend`.
  Now an ungated host writes the instance when the binary carries
  `qrdb_vlog` (`ungatedHostAppliesAuditInstance`, set by the tagged
  `vlog_database_store.go`); a build without `qrdb_vlog` keeps
  `OpAuditAppend`. A gated host still follows its gate. Tests and
  fail-before results:
  - `TestReplicatedLogger_Emit_UngatedHostProposesToTheAuditInstance`:
    "op = 79, want OpVerifiableLog (82)" before, passes after;
    `TestReplicatedLogger_Emit_AuditInstanceNeedsItsOwnGate` case "ungated
    host" now wants op 82 in a `qrdb_vlog` build and 79 without it.
  - `TestBuildServices_StandaloneBarrierTenantsAuditIsWrittenToTheSystemShard`
    now also reads the instance's head: "log vault/system/audit not found"
    before, passes after.
  - `TestBuildServices_StandaloneAuditSwitchKeepsTheChainContinuous`: an
    existing standalone trail (two `OpAuditAppend` records, no instance),
    then two events through the logger. The instance holds the two new
    events, the query sees all four, and the tenant chain verifies from
    genesis. Passes. It is a new proof, so it has no fail-before; the switch
    starts the tree at S0 over the existing trail, and the chain stays
    whole.
  - `TestAudit_CLI_AuditInstance_Standalone_PerProtocol` (and
    `TestAudit_CLI_AuditInstance_PerProtocol` through the same helper, now
    with `audit query --replicated` and `audit verify --replicated`):
    "vlog head on auditlog-solo/system/audit = size 0" on rest, grpc, quic
    and mcp before, passes after.
- Slice 3, every Codex finding from the twelve CHANGES_REQUESTED reviews,
  checked against the tip: all CLOSED; no code change was needed.

  | Head | Finding | CLOSED by | Proving test |
  | --- | --- | --- | --- |
  | d5b15794bc | #1 logs sealed under the database's key | rw1613w3 | `TestLogService_BarrierDatabaseLogsLiveInTheParentGroup`, `TestBuildServices_LogServiceSealsWithTheBarrierDatabase` |
  | d5b15794bc | #2 no supported barrier database | rw1613w3, rw1613w4 | `TestLogService_ServesABarrierDatabase`, `TestVlog_CLI_BarrierDatabase_PerProtocol`, `TestVlog_CLI_BarrierDatabase_Standalone_PerProtocol` |
  | d5b15794bc | #3 sealed signing key in the e2e | rw1613w3 | `TestVlog_CLI_BarrierTenant_PerProtocol` (sign-head before seal and after unseal), `TestApplyVerifiableLog_SealedKeyRecordsAreStoredUnopened` |
  | 9ce21b4ba2 | owner approval of the SPEC change | owner ruling 1234 option A (record only) | none; SPEC row 158 is the coordinator's |
  | ba20bd3d7e | complete barrier-database support | rw1613w3, rw1613w4 | as d5b15794bc #2 |
  | ba20bd3d7e | audit writer and log reader in one group | audit row 8 (audit P1), rw1613w6 | `TestSystemShardLogResolver_RoutesBarrierTenantsToTheirHomeGroup`, `TestBuildServices_StandaloneBarrierTenantsAuditIsWrittenToTheSystemShard` |
  | 28ecaf0d9b | F1 restrict the routing exception | Codex verify at 28ecaf0d9b | `TestLogService_BarrierTenantsSystemDatabaseLogsStayBehindItsBarrier` |
  | 28ecaf0d9b | F3 gate the drop sub-type | Codex verify at 28ecaf0d9b | `TestLogService_DropDatabaseLogs_GateOff`, `TestGate_VerifiableLogDropActivation` |
  | 28ecaf0d9b | reclamation sees writes staged in the batch | Codex verify at 28ecaf0d9b | `TestChain_DropDatabase_PackedBatchMatchesSeparateUpdates`, `TestApplyVerifiableLog_AppendAndDropInOneBatch` |
  | 28ecaf0d9b | F4 name fenced until reclamation | Codex verify at 28ecaf0d9b | `TestManager_Drop_FencesTheStoreBeforeReclaiming`, `TestManager_Drop_ReclaimFailureRemovesTheStoreFence`, `TestDropFence_FailedReclamationRollsTheFenceBack`, `TestDatabaseService_DropReclaimsTheDatabasesLogs` |
  | 3ad7bfadef | staged writes (named.go) | Codex verify at 28ecaf0d9b | as the batch tests above |
  | 3ad7bfadef | fence admitted writes | mg1655a | `TestLogService_CreatePausedAcrossDropAndRecreateIsRefused`, `TestLogService_ReadRefusesAnEarlierIncarnationsLog`, `TestLogService_StandaloneBarrierStoreFencesAPausedCreate` |
  | 9a8dd39821 | writes wait for the drop feature; legacy log; home-group fence | mg1655a, fx1655c | `TestLogService_IncarnationWritesWaitForDropFeature`, `TestIncarnationRequest_LegacyLogIsVisible`, `TestStoreResolver_LogFenceTargetBarrierTenantUsesItsHomeGroup`, `TestLogService_BarrierTenantNamedDatabaseIsFencedInItsHomeGroup` |
  | 928f3d8a41 | barrier-tenant fence target; paused sign-head; bounded empty drop | rw1613d | `TestStoreResolver_FenceTargetBarrierTenantUsesTheSystemStore`, `TestKeyspaceReclaim_BarrierTenantFencesDataAndLogMarkers`, `TestLogService_SignHeadPausedAcrossDropAndRecreateIsRefused`, `TestChain_DropDatabase_EmptyLogsAreBounded` |
  | 9428bf8de4 | pre-upgrade markers; one database lookup | rw1613w2 | `TestKeyspaceReclaim_PreUpgradeBarrierTenantLogMarker`, `TestLogService_PreUpgradeDatabaseLogFenceIsInitializedAtFirstLogWrite`, `TestLogService_AdmitFencesToItsOneDatabaseLookup` |
  | b50e2f2b95 | create across a barrier migration | rw1613p, rw1613w1 | `TestLogService_StandaloneBarrierCreateAcrossMigrationRefusesTheReplacedBarrier` |
  | fdeca8a053, 06a8907fc0 #1 | failed create strands the log marker | rw1613w5 | `TestUnfence_FailedDataClaimReleasesTheLogMarker` |
  | d2b8ad1d61, 06a8907fc0 #2 | store cache outlives a drop | rw1613w3, rw1613w5 | `TestLogService_DropForgetsTheLogFenceIncarnation` |

- Left for the coordinator: the SPEC row 158 status, `pm/rows.md` and the
  ROADMAP line 122 "awaits the owner's decision" text (row text this lane
  may not edit); `docs/audit/architecture.md` (outside this lane's file map)
  still says a host that gates nothing keeps `OpAuditAppend`, and now needs
  the same standalone sentence as `docs/databases/architecture.md` §7.
- Commands, each passing: `gofmt -l pkg/service pkg/server/lifecycle
  test/integration` (empty); `go test -count=1 -tags=codec_json,raft,qrdb_vlog
  -run '^(TestReplicatedLogger_|TestLogService_|TestUnfence_|TestKeyspaceReclaim_|TestSystemShardLogResolver_)'
  ./pkg/service/`; `TestReplicatedLogger_` without `qrdb_vlog`;
  `-run '^(TestBuildServices_|TestLogGroups_)' ./pkg/server/lifecycle/`; the
  Slice 3 proving tests by package (service, server, database, vlog,
  statemachine, lifecycle). Integration
  (`-tags=codec_json,raft,integration,qrdb_vlog`): the audit-instance (Raft
  and standalone), audit verify and query, and vlog barrier-database,
  barrier-tenant, drop and per-protocol CLI tests, every transport.

## rw1613w7

Docs only, the row's closing lane.

- `docs/audit/architecture.md`, "The audit instance": the clause "a host
  that gates nothing keeps `OpAuditAppend`" is replaced by the tip's rule
  (`docs/databases/architecture.md` §7): a standalone node's system shard
  gates nothing, so in a `qrdb_vlog` build its trail is the audit instance
  from the first event the binary writes, continuous with the
  `OpAuditAppend` records an earlier binary left; a build without
  `qrdb_vlog` keeps `OpAuditAppend`.
- Sweep: `git grep -n -i -E 'gates nothing|keeps .?OpAuditAppend|own sealed
  store|awaits the owner|owner.s decision|pending the owner|not yet
  served|in progress' -- docs ':!docs/plans' ':!docs/reference'`, plus a
  grep for verifiable-log next to in progress/pending/not yet. Verdicts:
  `docs/audit/architecture.md:432` stale, fixed; `docs/databases/architecture.md`
  1374/1378 and `docs/databases/engineering.md:449` are the tip's rule,
  correct; `docs/databases/api.md:493` (the `verifiable-log-keys` gate not yet
  active on every replica) is a mixed-version error, correct;
  `docs/SPEC.md:158` is the row itself (below); every other hit (README
  volume-pools and multi-tenancy, other SPEC rows, attested-caller,
  barrier-seal, clustering, distributed-locks, multi-tenancy, secondary
  indexes, time-series, storage-engines, sqlite-vfs, decisions, and the
  "in progress" operations of other features) is another feature, not
  this row. No stale hit outside this lane's files.
- Row text, authorised by owner ruling 1234 option A, NOT applied: the
  lane's policy guard refused `docs/SPEC.md` and `ROADMAP.md` as protected
  paths (they were not in the lane's `LANE_POLICY_ALLOW`); the `pm/rows.md`
  removal was undone so the three edits land together. For the coordinator:
  - `docs/SPEC.md:158`: `| verifiable-log | in progress |` becomes
    `| verifiable-log | shipped |`, and "with the tenant's isolation, DEK,
    barrier and replication;" becomes "with the tenant's isolation, DEK,
    barrier and replication (a barrier-enabled database's logs live in its
    tenant's Raft group, or the node's local store when standalone, each
    entry body and software signing key sealed under the database's barrier
    key; a barrier-isolated tenant's logs live in its home group);"; nothing
    else in the row changes.
  - `ROADMAP.md:122`: the line leaves "## Now" and is the last line of
    "## Complete" (after kv-versions), in that section's form: its text
    unchanged up to the issue link, the PR link changed from #729 to #1655,
    the docs and plan links kept, and the tail "in progress: serving ...
    where their logs live" replaced by "done". "## Now" keeps four lines.
  - `pm/rows.md:20`: the line `| verifiable-log | #1613 | in progress | 0 |
    none |` is removed.
- Serena's language server rewrote `go.sum` (171 added lines) during the
  lane; it was restored, never committed.
- Commands: `make check-publication-bound`; the Acceptance greps
  (`gates nothing keeps` in `docs/audit` prints nothing; the SPEC, ROADMAP
  and `pm/rows.md` greps still show the unapplied row text above).
- Nothing left for the row's lanes; after the coordinator applies the three
  row edits, next is the row PR's one final Codex review.

## Out of scope

The go-xkms provider and transparency log (the seam rows on the other side);
the open question on the head signature type (architecture, Open questions).
