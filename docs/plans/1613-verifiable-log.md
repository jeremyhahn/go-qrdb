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
| 8 | Barrier | GAP | The log registry sits in the system shard, under the system barrier; a barrier-isolated tenant (`BarrierTenantLogNotSupportedError`) and a barrier-enabled database (`BarrierDatabaseLogNotSupportedError`) are refused (`pkg/server/lifecycle/seams_vlog_test.go:TestBuildServices_LogServiceRefusesBarrierTenants`, `...BarrierDatabases`). §8 of the architecture says the tenant's or database's barrier encrypts the records; the code does not serve such tenants at all. |
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
b. Row 8 (barrier): not closed in this lane. Serving a barrier tenant or a
   barrier database needs the log registry and its sealed signing keys moved
   from the system shard into the tenant's (or database's) own barrier-sealed
   store, with the apply path, snapshot and restore following it — an
   architecture change (which store owns a barrier tenant's logs, and how a
   sealed barrier refuses reads), for the owner to decide. The architecture
   §8 "Barrier" bullet is corrected to state what the code does.

## Step 3

Row stays `in progress` while row 8 is a GAP; `pm/rows.md` lists it as
`in progress` under #1613. The architecture §8 "Barrier" bullet now states
what the code does. Not done here: ROADMAP.md is a protected path this lane
may not edit, so moving fsync-stall-leadership, lean-node-build and
barrier-passphrase-change to Complete (SPEC marks them shipped) is left to a
lane launched with ROADMAP.md allowed.

## Remains

- Row 8, the barrier: an owner decision on where a barrier tenant's (and a
  barrier database's) logs live, then the move with apply, snapshot, restore
  and a CLI E2E on a barrier tenant.
- The ROADMAP.md moves above.

## Out of scope

The go-xkms provider and transparency log (the seam rows on the other side);
the open question on the head signature type (architecture, Open questions).
