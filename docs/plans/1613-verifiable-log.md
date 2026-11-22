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
| 8 | Barrier | PARTIAL | Barrier tenant HOLDS in Raft mode: its logs, entries and sealed signing keys are applied in its home group, under its own key; a sealed barrier refuses with `tenant.ErrTenantSealed` (`pkg/service/audit_shard_resolver_test.go:TestSystemShardLogResolver_RoutesBarrierTenantsToTheirHomeGroup`; `pkg/server/lifecycle/log_groups_test.go:TestLogGroups_TenantHomeGroup`; `test/integration/vlog_barrier_tenant_cli_test.go:TestVlog_CLI_BarrierTenant_PerProtocol`, every transport: sealed refused, unsealed create/append/get/prove-inclusion/prove-consistency/head, log unchanged after the unseal). Barrier database HOLDS on every backend: its logs are applied to and read from the database's own barrier backend (`pkg/service/vlog_database_store.go`), encrypted under the database's key; a sealed database refuses with `*service.BarrierSealedError`, a legacy raft barrier record with `*database.BarrierOffDataPathError` (`pkg/service/vlog_log_service_test.go:TestLogService_ServesABarrierDatabase`, `TestLogService_BarrierDatabaseLifecycle`, `TestLogService_RefusesALegacyRaftBarrierRecord`, `TestLogService_BarrierDatabaseCommitFailureRestores`; `pkg/server/lifecycle/seams_vlog_test.go:TestBuildServices_LogServiceSealsWithTheBarrierDatabase`; `test/integration/vlog_barrier_tenant_cli_test.go:TestVlog_CLI_BarrierDatabase_PerProtocol`, every transport: unsealed every verb and its proofs, no entry in plaintext in the database's data directory, sealed refused, wrong secret stays refused, unsealed log unchanged and grows, gone after drop). PARTIAL for one line: a barrier tenant on a standalone node fails closed with `BarrierStoreUnavailableError`. See Remains. |
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
   - `service.NewSystemShardAuditResolver` takes a `service.LogGroupSource`,
     the one resolver of the audit logger and the log service;
     a barrier tenant routes to `TenantHomeGroup` (its `__home` dedicated
     group).
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

## Step 3

Row 8 is PARTIAL, so the SPEC row stays `in progress` and `pm/rows.md` is
unchanged: one line remains open, no barrier tenant is served on a standalone
node (`BarrierStoreUnavailableError`). ROADMAP.md is a protected path this lane may not edit, so moving
fsync-stall-leadership, lean-node-build and barrier-passphrase-change to
Complete (SPEC marks them shipped) is left to a lane launched with ROADMAP.md
allowed.

## Remains

- Row 8, standalone: barrier homes are `seal.NewTenantBarrierKVStore` KV stores
  with no log state machine, so a barrier tenant's log fails closed there with
  `BarrierStoreUnavailableError`.
- Drop of a barrier database, question for the coordinator: `Manager.drop`
  (`pkg/database/manager.go`) removes only the manager's own per-database
  directory, not a `--data-dir` the caller chose. Re-creating a barrier
  database over the old folder finds the old encrypted log and fails to
  decrypt it instead of answering not found. Whether drop may delete a
  caller-owned directory is a destructive-delete policy call; until it is
  ruled, the user guide says to re-create in a new `--data-dir`, and the CLI
  E2E does so.
- The ROADMAP.md moves above.

## Out of scope

The go-xkms provider and transparency log (the seam rows on the other side);
the open question on the head signature type (architecture, Open questions).
