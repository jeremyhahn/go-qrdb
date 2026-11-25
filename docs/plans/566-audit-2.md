# #566 suite audit 2: pkg/service (rw566u)

Rules as in [566-suite-audit.md](566-suite-audit.md) (ruling 273(12)): a
constructor "ReturnsNonNil" test goes when its body asserts only non-nil
and another test in the package calls the same constructor and uses the
result; an "Implements"/"InterfaceCompliance" test goes when a compile-time
guard covers it (a `var _ Iface = ...` line, or a constructor whose
declared return type is the interface), and is trimmed to a package-level
`var _` guard when none exists. No behaviour loses its only check; no
production code changes; no tests added.

| Removed or trimmed test (file:func) | Behaviour it checked; why removed | Still checked by (file:func or file:line guard) |
|---|---|---|
| backend_kv_test.go:TestNewBackendKVService_ReturnsNonNil | constructor non-nil; tautology | backend_kv_test.go:TestPut_StoresValue (newBackendKVService) |
| backend_kv_test.go:TestNewBackendKVService_ImplementsKVServicer | `*BackendKVService` is a KVServicer; guarded at compile time | backend_kv.go:32 `var _ KVServicer = (*BackendKVService)(nil)` |
| batch_service_test.go:TestNewBatchService_ReturnsNonNil | constructor non-nil; tautology | batch_service_test.go:TestNewBatchService_PreservesShardID |
| collection_service_test.go:TestCollectionService_ImplementsCollectionServicer | body was only the interface assignment; trimmed to a package-level guard | collection_service_test.go `var _ CollectionServicer = (*CollectionService)(nil)` |
| credential_service_test.go:TestNewCredentialService_InterfaceCompliance | interface conversion non-nil; guarded at compile time | credential_service.go:117 `var _ CredentialServicer = (*CredentialService)(nil)`; credential_service_test.go:TestCredentialService_CredentialStrategy_Manual |
| database_service_test.go:TestDatabaseService_ImplementsDatabaseCredentialCreator | `*DatabaseService` satisfies DatabaseCredentialCreator (directly and through a DatabaseServicer value); guarded at compile time on the concrete type | database_service.go:159 `var _ DatabaseCredentialCreator = (*DatabaseService)(nil)`, database_service.go:894 `var _ DatabaseServicer = (*DatabaseService)(nil)` |
| index_service_test.go:TestIndexService_NewIndexService_ReturnsNonNil | constructor non-nil; tautology | index_service_test.go:TestIndexService_NewIndexService_PreservesShardID |
| lease_service_test.go:TestLeaseService_NewLeaseService_ReturnsNonNil | constructor non-nil; tautology | lease_service_test.go:TestLeaseService_NewLeaseService_PreservesShardID |
| mvcc_service_test.go:TestNewMVCCKVService_ReturnsNonNil | constructor non-nil; tautology | mvcc_service_test.go:TestMVCCKVService_Put_Success |
| mvcc_service_test.go:TestNewMVCCKVService_ImplementsInterface | the constructor's declared return type is MVCCKVServicer | mvcc_service.go:204 `func NewMVCCKVService(...) MVCCKVServicer` |
| mvcc_service_test.go:TestMockKeyNotFoundErr_ImplementsInterface | tested the test's own mock; trimmed to a guard | mvcc_service_test.go `var _ keyNotFoundErr = (*mockKeyNotFoundError)(nil)` |
| mvcc_service_test.go:TestMockRevisionCompactedErr_ImplementsInterface | tested the test's own mock; trimmed to a guard | mvcc_service_test.go `var _ revisionCompactedErr = (*mockRevisionCompactedError)(nil)` |
| mvcc_service_test.go:TestMockRevisionNotFoundErr_ImplementsInterface | tested the test's own mock; trimmed to a guard | mvcc_service_test.go `var _ revisionNotFoundErr = (*mockRevisionNotFoundError)(nil)` |
| proposer_test.go:TestNewHostProposerAdapter_ReturnsNonNil | constructor non-nil; tautology | proposer_test.go:TestNewHostProposerAdapter_StoresHost |
| rebalance_move_test.go:TestRebalanceService_NewRebalanceService_WithOptions_ReturnsNonNil | constructor with options non-nil; tautology | rebalance_move_test.go:TestExecuteRebalance_DedicatedTenantShard_SuccessfulMove |
| rebalance_service_test.go:TestRebalanceService_NewRebalanceService_ReturnsNonNil | constructor non-nil; tautology | rebalance_service_test.go:TestRebalanceService_GetShardStats_ReturnsStats |
| rebalance_service_test.go:TestRebalanceService_NewRebalanceService_ImplementsInterface | the constructor's declared return type is RebalanceServicer | rebalance_service.go:228 `func NewRebalanceService(...) RebalanceServicer` |
| service_test.go:TestNewKVService_ReturnsNonNil | constructor non-nil; tautology | service_test.go:TestKVService_Put_Success |
| service_test.go:TestNewAdminService_ReturnsNonNil | constructor non-nil; tautology | service_test.go:TestAdminService_Health_OK_LeaderTrue |
| timeseries_service_test.go:TestTimeSeriesService_NewTimeSeriesService_ReturnsNonNil | constructor non-nil; tautology | timeseries_service_test.go:TestTimeSeriesService_NewTimeSeriesService_PreservesShardID |
| transaction_service_test.go:TestNewTransactionService_ReturnsNonNil | constructor non-nil; tautology | transaction_service_test.go:TestNewTransactionService_PreservesShardID |
| watch_service_test.go:TestWatchService_NewWatchService_ReturnsNonNil | constructor non-nil; tautology | watch_service_test.go:TestWatchService_Watch_DelegatesToHubWatch |
| watch_service_test.go:TestWatchService_NewWatchService_ImplementsInterface | the constructor's declared return type is WatchServicer | watch_service.go:75 `func NewWatchService(hub WatchHub) WatchServicer` |

| Kept candidate | Why it stays |
|---|---|
| none | every candidate of this audit met rule (1) or (2) |
