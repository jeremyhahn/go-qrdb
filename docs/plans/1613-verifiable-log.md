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
| 8 | Barrier | HOLDS | Barrier tenant HOLDS in Raft mode: its logs, entries and sealed signing keys are applied in its home group, under its own key; a sealed barrier refuses with `tenant.ErrTenantSealed` (`pkg/service/audit_shard_resolver_test.go:TestSystemShardLogResolver_RoutesBarrierTenantsToTheirHomeGroup`; `pkg/server/lifecycle/log_groups_test.go:TestLogGroups_TenantHomeGroup`; `test/integration/vlog_barrier_tenant_cli_test.go:TestVlog_CLI_BarrierTenant_PerProtocol`, every transport: sealed refused, unsealed create/append/get/prove-inclusion/prove-consistency/head, log unchanged after the unseal). Barrier database HOLDS on every backend: its logs are applied to and read from the database's own barrier backend (`pkg/service/vlog_database_store.go`), encrypted under the database's key; a sealed database refuses with `*service.BarrierSealedError`, a legacy raft barrier record with `*database.BarrierOffDataPathError` (`pkg/service/vlog_log_service_test.go:TestLogService_ServesABarrierDatabase`, `TestLogService_BarrierDatabaseLifecycle`, `TestLogService_RefusesALegacyRaftBarrierRecord`, `TestLogService_BarrierDatabaseCommitFailureRestores`; `pkg/server/lifecycle/seams_vlog_test.go:TestBuildServices_LogServiceSealsWithTheBarrierDatabase`; `test/integration/vlog_barrier_tenant_cli_test.go:TestVlog_CLI_BarrierDatabase_PerProtocol`, every transport: unsealed every verb and its proofs, no entry in plaintext in the database's data directory, sealed refused, wrong secret stays refused, unsealed log unchanged and grows, gone after drop). Barrier tenant HOLDS on a standalone node: its logs are applied to and read from its own standalone barrier store (`pkg/service/vlog_barrier_store.go`, attached by `AttachLocalTenantHomes`) (`pkg/service/vlog_barrier_store_test.go:TestLogService_ServesABarrierTenantOnItsStandaloneBarrier`, `TestLogService_SealedStandaloneBarrierTenantRefuses`; `test/integration/vlog_barrier_tenant_cli_test.go:TestVlog_CLI_BarrierTenant_Standalone_PerProtocol`). A barrier tenant's audit trail stays on the system shard, where its audit logger writes and the log service reads it (review fixes F2, F3 and the audit P1 below). The kv surface refuses a barrier database's reserved log keyspace (F1), and a dropped database's logs are reclaimed (F4). |
| 9 | Replication | HOLDS | `sdk/go/node/clustered_vlog_test.go:TestClusteredCreateFunc_AppliesVerifiableLog`, `...LogRegistryPerReplica`; `pkg/store/vlog_apply_test.go:TestStoreVerifiableLog_SnapshotMidLogMatchesAPeer` |
| 10 | Standalone and Raft modes | HOLDS | standalone: `pkg/engine/mvcc_vlog_test.go:TestMVCCEngine_VerifiableLog_DispatchesAndAnswersReads`; Raft: `pkg/store/vlog_apply_test.go:TestStoreVerifiableLog_DispatchesAndAnswersReads` |
| 11 | One apply-time opcode behind a feature gate | HOLDS | `protocol.OpVerifiableLog` (`pkg/protocol/vlog.go`), `featuregate.VerifiableLog`/`VerifiableLogKeys`; `pkg/service/vlog_log_service_test.go:TestLogService_Writes_RefusedBeforeGateActive`, `TestLogService_KeyWrites_RefusedBeforeKeyFeatureActive`; `pkg/store/vlog_apply_test.go:TestStoreAuditInstance_MixedVersionReplay` |
| 12 | Dispatched by both state-machine families | HOLDS | `statemachine.ApplyVerifiableLog` from `pkg/engine/mvcc_engine.go` and `pkg/store/pebble.go`; `TestMVCCEngine_VerifiableLog_RefusalVersusHalt`, `TestStoreVerifiableLog_RefusalVersusHalt`, `TestStoreAuditInstance_FamiliesAgree` |
| 13 | Sequence assigned at apply | HOLDS | `pkg/vlog/audit_instance_test.go:TestAuditInstance_DeterministicAcrossBatchPacking`; `pkg/store/vlog_apply_test.go:TestStoreVerifiableLog_ReopenMatchesAPeer` |
| 14 | Head signature produced outside apply, appended as a record | HOLDS | `LogService.SignHead` signs, then proposes the `Checkpoint` sub-type; `TestLogService_SignHead_RecordsTheCheckpoint`; `pkg/vlog/checkpoint_test.go` |
| 15 | `vlog.HeadSignerProvider` via `WithHeadSignerProvider` on the server and `sdk/go/node` | HOLDS | `pkg/server/seam_options_test.go`; `sdk/go/node/seams_test.go:TestWithHeadSignerProvider_ReachesTheServer`; `sdk/go/server/headsigner_test.go`; `pkg/server/lifecycle/seams_vlog_test.go:TestBuildServices_HeadSignerProviderCreatesTheLogsKeys` |
| 16 | Software provider (key as the log's sealed record) is the default | HOLDS | `pkg/vlog/signing_software_test.go:TestSoftwareHeadSignerProvider_CreateKeyIsTheSealedRecord`; `TestNewLogService_DefaultsToTheSoftwareProvider`; `TestBuildServices_HeadSignerProviderAbsentKeepsTheSoftwareProvider`; `sdk/go/node/seams_test.go:TestWithHeadSignerProvider_AbsentKeepsTheSoftwareProvider` |
| 17 | The audit trail is one log instance | HOLDS | `pkg/vlog/audit_instance_test.go:TestAuditInstance_SwitchStartsTheTreeAtS0`; `pkg/store/vlog_apply_test.go:TestStoreAuditInstance_FamiliesAgree`; `pkg/service/vlog_log_service_audit_test.go:TestLogService_ReadsTheAuditInstance` |

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
c. Row 8 (barrier database), this PR, lanes fx1655h and fx1655i.
   - `pkg/service/vlog_database_store.go`: a barrier-enabled database's logs
     are applied to and read from the database's own barrier backend, so they
     are encrypted under the database's key, sealed with it and dropped with
     it. A sealed database refuses with `BarrierSealedError` (`Unavailable`); a
     legacy raft barrier record with `database.BarrierOffDataPathError`
     (`Unprocessable`).
   - The log service's route resolves each database incarnation to its log
     target (`logTarget`: the barrier store, or `raftTarget`, the group).
   - The barrier-database refusal error of slice b and its errclass row are
     deleted.
   - CLI E2E: `TestVlog_CLI_BarrierDatabase_PerProtocol` in
     `test/integration/vlog_barrier_tenant_cli_test.go`, every transport:
     served, sealed, wrong secret, unsealed, dropped and re-created.

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
   - Barrier-database P1: served from the database's own barrier store (slice
     c; `TestLogService_ServesABarrierDatabase`,
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

## Remains

- This slice did not run the vlog CLI E2E (`TestVlog_CLI_PerProtocol`,
  `TestVlog_CLI_BarrierTenant_*`, `TestVlog_CLI_BarrierDatabase_PerProtocol`,
  `-tags=codec_json,raft,integration`) after the fence. Run it before
  promotion.

- Open, Tenant item (F3 proof half): a standalone node composes an ungated
  host (`cmd/qrdbd/start.go`), so its replicated audit logger keeps
  `OpAuditAppend` for every tenant (`docs/databases/engineering.md`, "The
  switch"): its events are written to the system shard
  (`TestBuildServices_StandaloneBarrierTenantsAuditIsWrittenToTheSystemShard`
  covers that half), but there is no verifiable audit instance to prove. The
  same holds for every tenant and on main, not a barrier gap; gating the
  standalone host is a change to `cmd/qrdbd`, outside this row's paths.
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
- The SPEC, `pm/rows.md` and ROADMAP.md moves above.

## Out of scope

The go-xkms provider and transparency log (the seam rows on the other side);
the open question on the head signature type (architecture, Open questions).
