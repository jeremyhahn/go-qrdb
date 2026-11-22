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
| 8 | Barrier | PARTIAL | Barrier tenant HOLDS in Raft mode: its logs, entries and sealed signing keys are applied in its home group, under its own key; a sealed barrier refuses with `tenant.ErrTenantSealed` (`pkg/service/audit_shard_resolver_test.go:TestSystemShardLogResolver_RoutesBarrierTenantsToTheirHomeGroup`; `pkg/server/lifecycle/log_groups_test.go:TestLogGroups_TenantHomeGroup`; `test/integration/vlog_barrier_tenant_cli_test.go:TestVlog_CLI_BarrierTenant_PerProtocol`, every transport: sealed refused, unsealed create/append/get/prove-inclusion/prove-consistency/head, log unchanged after the unseal). Barrier database: every log call is refused with `BarrierDatabaseLogNotSupportedError` (`Unimplemented`) before any key, proposal or read, on every backend, sealed or not, because the log has no store under the database's key (`pkg/service/vlog_log_service_test.go:TestLogService_RefusesABarrierDatabase`; `pkg/api/errclass/database_barrier_test.go:TestClassify_BarrierDatabaseLogNotSupported`; `test/integration/vlog_barrier_tenant_cli_test.go:TestVlog_CLI_BarrierDatabase_PerProtocol`, every transport). A barrier tenant on a standalone node fails closed with `BarrierStoreUnavailableError`. See Remains. |
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
   - A barrier-enabled database is refused in the log service's route with
     `BarrierDatabaseLogNotSupportedError` (`Unimplemented`) before any key,
     proposal or read, whatever its backend (Codex round 1, P1 #1 and #2: a
     dedicated Raft database's group is not under the database's key, and a
     legacy record could reach the system shard). The per-database route
     (`LogShardResolver`, `ResolveDatabaseLogShard`, `DatabaseGroup`) is
     deleted.
   - `BarrierTenantLogNotSupportedError` and its errclass row are deleted.
   - The REST transport sends the database scope header on `/vlog` requests;
     before, `--database` never reached the server over REST.
   - CLI E2E: `test/integration/vlog_barrier_tenant_cli_test.go`, listed in
     `INTTEST_databases`.

## Step 3

Row 8 is PARTIAL, so the SPEC row stays `in progress` and `pm/rows.md` is
unchanged: no log is served in a barrier-enabled database, and no barrier
tenant is served on a standalone node. ROADMAP.md is a protected path this lane may not edit, so moving
fsync-stall-leadership, lean-node-build and barrier-passphrase-change to
Complete (SPEC marks them shipped) is left to a lane launched with ROADMAP.md
allowed.

## Remains

- Row 8, open question for the owner: where does a barrier-enabled database's
  log live? The database barrier encrypts that database's KV records, but the
  log registry is state-machine state applied in a group, and no group a
  database's log could be applied in (the system shard, a tenant's home group,
  or a dedicated Raft database's own group) is under the database's key.
  Putting the registry under the database barrier needs either a group sealed
  by the database's barrier or apply-time encryption with the database key, an
  architecture call (Codex round 1, P1 #2, deferred to the owner). Until the
  owner rules, every log call in a barrier-enabled database fails closed with
  `BarrierDatabaseLogNotSupportedError` (`Unimplemented`); this row implements
  no barrier-database routing.
- Row 8, standalone: barrier homes are `seal.NewTenantBarrierKVStore` KV stores
  with no log state machine, so a barrier tenant's log fails closed there; the
  same question (registry in a KV home) applies.
- The ROADMAP.md moves above.

## Out of scope

The go-xkms provider and transparency log (the seam rows on the other side);
the open question on the head signature type (architecture, Open questions).
