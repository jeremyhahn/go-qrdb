# #566 suite audit (folded #1331)

Rules (ruling 273(12)): one path per behaviour at the uppermost layer that
reaches it; remove duplicates, tests of the standard library or a
dependency, tautologies; replace sleeps standing in for synchronisation.
No behaviour loses its only test; the coverage gate stays met.

## Package order (largest test time first)

| Order | CI leg | Package | Test files / lines |
|---|---|---|---|
| 1 | coverage tenant-lifecycle, tenant | pkg/tenant | 78 / 22k |
| 2 | coverage seal-barrier, seal | pkg/seal | 94 / 24k |
| 3 | coverage server-raft, server | pkg/server | 177 / 48k |
| 4 | test-tags | remaining packages by test time | |

## Removed tests

| Package | Removed test | Reason | Test that keeps the path |
|---|---|---|---|
| pkg/tenant | TestKVTenantBarrierSeal_NotEnabled2 | duplicate | TestKVTenantBarrierSeal_NotEnabled |
| pkg/tenant | TestKVTenantBarrierStatus_NotEnabled2 | duplicate | TestKVTenantBarrierStatus_NotEnabled |
| pkg/tenant | TestNestedContextsInnerTenantWins | tests context.WithValue shadowing (stdlib) | TestWithTenantIDAndFromContextRoundTrip |
| pkg/tenant | TestWithTenantIDPreservesParentValues | tests context.WithValue chaining (stdlib) | TestWithTenantIDAndFromContextRoundTrip |
| pkg/tenant | TestFromContextWithCancelledContext | tests that cancellation keeps values (stdlib) | TestWithTenantIDAndFromContextRoundTrip |
| pkg/tenant | TestIncrementQuotaUsage_Success | duplicate | TestTenantService_IncrementQuotaUsage_Success |
| pkg/tenant | TestIncrementQuotaUsage_ExceedsLimit | duplicate | TestTenantService_IncrementQuotaUsage_AtLimit, _QuotaExceeded |
| pkg/tenant | TestIncrementQuotaUsage_UnlimitedLimit | duplicate | TestTenantService_IncrementQuotaUsage_UnlimitedLimit |
| pkg/tenant | TestIncrementQuotaUsage_ZeroDelta | duplicate | TestTenantService_IncrementQuotaUsage_ZeroDelta |
| pkg/tenant | TestIncrementQuotaUsage_EmptyTenantID | duplicate | TestTenantService_IncrementQuotaUsage_EmptyID |
| pkg/tenant | TestDecrementQuotaUsage_Success | duplicate | TestTenantService_DecrementQuotaUsage_Success |
| pkg/tenant | TestDecrementQuotaUsage_ClampToZero | duplicate | TestTenantService_DecrementQuotaUsage_ClampToZero |
| pkg/tenant | TestDecrementQuotaUsage_ZeroDelta | duplicate (weaker assertion) | TestTenantService_DecrementQuotaUsage_ZeroDelta |
| pkg/tenant | TestDecrementQuotaUsage_EmptyTenantID | duplicate | TestTenantService_DecrementQuotaUsage_EmptyID |
| pkg/tenant | TestGetQuotaUsage_Success | duplicate | TestTenantService_GetQuotaUsage_Success |
| pkg/tenant | TestGetQuotaUsage_NoQuotasSetReturnsZero | duplicate | TestTenantService_GetQuotaUsage_NilQuotas |
| pkg/tenant | TestGetQuotaUsage_EmptyTenantID | duplicate | TestTenantService_GetQuotaUsage_EmptyID |
| pkg/tenant | TestGetQuotaUsage_TenantNotFound | duplicate | TestTenantService_GetQuotaUsage_NotFound |
| pkg/tenant | TestQuotaExceededError_ImplementsError | tautology (compile-time assignment) | TestQuotaExceededError_Error |
| pkg/tenant | TestInvalidTenantIDErrorMessage | duplicate (substring of the exact format) | TestInvalidTenantIDErrorFormat |
| pkg/tenant | TestInvalidTenantIDErrorEmptyTenantID | duplicate of the format path | TestInvalidTenantIDErrorFormat |
| pkg/tenant | TestInvalidTenantIDErrorImplementsErrorInterface | tautology | TestInvalidTenantIDErrorFormat |
| pkg/tenant | TestInvalidTenantIDCharError_NonEmpty | weaker duplicate | TestInvalidTenantIDCharError_ContainsHex |
| pkg/tenant | TestInvalidTenantIDCharError_ImplementsError | tautology | TestInvalidTenantIDCharError_ContainsHex |
| pkg/tenant | TestTenantStoreError_ReadTransient_TrueWhenSetByNewStoreError | duplicate | TestNewStoreError_NonDecryptCauseIsTransient |
| pkg/tenant | TestIsTransientCause_NonDecryptCause | lower layer of newStoreError (its only caller) | TestNewStoreError_NonDecryptCauseIsTransient |
| pkg/tenant | TestIsTransientCause_DecryptCause | lower layer of newStoreError (its only caller) | TestNewStoreError_DecryptCauseIsNotTransient |
| pkg/tenant | TestValidateReservedTenantID_System | lower layer of CreateTenant | TestTenantService_CreateRejectsSystemID |
| pkg/tenant | TestValidateReservedTenantID_LegacyDefault | lower layer of CreateTenant | TestTenantService_CreateRejectsLegacyDefaultID |
| pkg/seal | TestBarrier_Unseal_Success | subset of the seal/unseal round trip (its sealed/unsealed asserts moved there) | TestBarrier_UnsealThenDataRoundTrip |
| pkg/seal | TestBarrier_PutGet_RoundTrip | Put/Get path repeated by the seal/unseal round trip | TestBarrier_UnsealThenDataRoundTrip |
| pkg/seal | TestBarrier_Seal_Success | init, seal, sealed-gate path repeated | TestBarrier_Seal_ZeroesDEK, TestBarrier_Get_Sealed |
| pkg/seal | TestBarrier_Encryptor_Success (Put/Get tail only; the test stays) | duplicate Put/Get round trip | TestBarrier_UnsealThenDataRoundTrip |
| pkg/server | TestWithCertEnrollment_NonNil | option-setter assignment, lower layer of the served call | TestMintDelegation_ServerHandsTheRequestToTheService |
| pkg/server | TestWithDatabaseManager_NonNil | option-setter assignment, lower layer of the served call | TestSealedDatabase_RefusedOverSQLWire |
| pkg/server | TestWithStrategyRegistry_Applied | duplicate (same field asserted; the extra lookup tests pkg/seal) | TestWithAllInjections_Compose |
| pkg/server | TestWithFactorRegistry_Applied | duplicate (same field asserted; the extra List tests pkg/authfactors) | TestWithAllInjections_Compose |
| pkg/server | TestWithTrustAnchors_Applied | lower layer of the exported crypto options | TestResolveCryptoOptions_SurfacesTrustAnchors |
| pkg/server | TestWithTrustAnchors_UnsetIsNil | lower layer of the exported crypto options | TestResolveCryptoOptions_TrustAnchorsNilWhenAbsent |
| pkg/lease | TestNotFoundErrorWithLargeID | duplicate with another ID | TestNotFoundErrorMessage |
| pkg/lease | TestNotFoundErrorImplementsError | tautology (compile-time assignment) | TestNotFoundErrorMessage |
| pkg/lease | TestExistsErrorWithDifferentID | duplicate with another ID | TestExistsErrorMessage |
| pkg/lease | TestExistsErrorImplementsError | tautology (compile-time assignment) | TestExistsErrorMessage |
| pkg/lease | TestInvalidTTLErrorWithZero | weaker duplicate (contains "0") | TestInvalidTTLErrorMessage |
| pkg/lease | TestInvalidTTLErrorImplementsError | tautology (compile-time assignment) | TestInvalidTTLErrorMessage |
| pkg/lease | TestLessorClosedErrorImplementsError | tautology (compile-time assignment) | TestLessorClosedErrorMessage |
| pkg/lock | TestSentinelErrors_NonNil | tautology (errors.New package vars) | TestSentinelErrors_DistinctMessages |
| pkg/lock | TestLockHeldError_ImplementsError | tautology (compile-time assignment) | TestLockHeldError_ContainsKey |
| pkg/lock | TestNotLockHolderError_ImplementsError | tautology (compile-time assignment) | TestNotLockHolderError_ContainsKey |
| pkg/lock | TestLockNotFoundError_ImplementsError | tautology (compile-time assignment) | TestLockNotFoundError_ContainsKey |
| pkg/lock | TestLockNotFoundError_EmptyKey | weaker duplicate (non-empty message) | TestLockNotFoundError_ContainsPrefix |
| pkg/shard | TestShardAlreadyRegisteredErrorMessage, _WithLargeID | substring checks of the exact message | TestErrorMessagePrefixes/ShardAlreadyRegistered |
| pkg/shard | TestShardNotRegisteredErrorMessage, _WithZeroID | substring checks of the exact message | TestErrorMessagePrefixes/ShardNotRegistered |
| pkg/shard | TestInvalidShardIDErrorMessage, _WithLargeID | substring checks of the exact message | TestErrorMessagePrefixes/InvalidShardID |
| pkg/shard | TestShardAlreadyRegisteredErrorImplementsError, TestShardNotRegisteredErrorImplementsError, TestInvalidShardIDErrorImplementsError | tautology (compile-time assignment) | TestErrorMessagePrefixes |
| pkg/consistency | TestUnknownLevelErrorImplementsError | tautology (compile-time assignment) | TestUnknownLevelErrorMessage |
| pkg/consistency | TestInvalidLevelErrorWithNegativeLevel | duplicate with another value | TestInvalidLevelErrorMessage |
| pkg/consistency | TestInvalidLevelErrorImplementsError | tautology (compile-time assignment) | TestInvalidLevelErrorMessage |
| pkg/consistency | TestInvalidTimeoutErrorWithLargeNegative | duplicate with another value | TestInvalidTimeoutErrorMessage |
| pkg/consistency | TestInvalidTimeoutErrorImplementsError | tautology (compile-time assignment) | TestInvalidTimeoutErrorMessage |
| pkg/quota | TestExceededErrorImplementsError | tautology (compile-time assignment) | TestExceededErrorMessageContainsTenantID |
| pkg/quota | TestInvalidResourceErrorImplementsError | tautology (compile-time assignment) | TestInvalidResourceErrorMessageContainsResourceID |
| pkg/quota | TestUnregisteredCounterErrorImplementsError | tautology (compile-time assignment) | TestUnregisteredCounterErrorMessageContainsResource |
| pkg/quota | TestErrNilCheckerIsNotNil | tautology (errors.New package var) | TestErrNilCheckerMessageContainsQuota |
| pkg/backend | TestClusterOperationError_ImplementsError | tautology (non-empty message) | TestClusterOperationError_Message |
| pkg/backend | TestUnsupportedOperationError_ImplementsError | tautology (non-empty message) | TestUnsupportedOperationError_Message |
| pkg/backend | TestBackendClosedError_ImplementsError | tautology (non-empty message) | TestBackendClosedError_Message |
| pkg/backend | TestLookupTypeError_ImplementsError | tautology (non-empty message) | TestLookupTypeError_Message |
| pkg/watch | TestHubClosedError_ImplementsErrorInterface | tautology (non-empty message) | TestHubClosedError_Error_ReturnsExpectedMessage |
| pkg/watch | TestHubClosedError_NotNil | tautology (compile-time assignment) | TestHubClosedError_Error_ReturnsExpectedMessage |
| pkg/watch | TestHubClosedError_ErrorsAs | tests errors.As on the concrete pointer (stdlib) | TestHubClosedError_Error_ReturnsExpectedMessage |
| pkg/embedding | TestInvalidInputError_ImplementsError | tautology (no assertion) | TestInvalidInputError_Error |
| pkg/embedding | TestEmptyResponseError_ImplementsError | tautology (no assertion) | TestEmptyResponseError_Error |
| pkg/embedding | TestRetryExhaustedError_As | tests errors.As on the concrete pointer (stdlib) | TestRetryExhaustedError_Error |
| pkg/embedding | TestHTTPError_As | tests errors.As on the concrete pointer (stdlib) | TestHTTPError_Error |
| pkg/embedding | TestCacheCapacityError_Zero | duplicate with another value | TestCacheCapacityError_Error |
| pkg/embedding | TestProviderAlreadyRegisteredError_As | tests errors.As on the concrete pointer (stdlib) | TestProviderAlreadyRegisteredError_Error |
| pkg/embedding | TestProviderNotFoundError_As | tests errors.As on the concrete pointer (stdlib) | TestProviderNotFoundError_Error |
| pkg/messaging | TestDeltaNilInputError_ImplementsErrorInterface | tautology (non-empty message) | TestDeltaNilInputError_Error_IncludesParameter |
| pkg/messaging | TestDeltaTypeMismatchError_ImplementsErrorInterface | tautology (non-empty message) | TestDeltaTypeMismatchError_Error_WithPath |
| pkg/messaging | TestDeltaMaxDepthExceededError_ImplementsErrorInterface | tautology (non-empty message) | TestDeltaMaxDepthExceededError_Error_IncludesPath |
| pkg/messaging | TestDeltaDeserializationError_ImplementsErrorInterface | tautology (non-empty message) | TestDeltaDeserializationError_Error_IncludesCause |
| pkg/messaging | TestDeltaDiffError_ImplementsErrorInterface | tautology (non-empty message) | TestDeltaDiffError_Error_IncludesCause |
| pkg/service | TestInvalidEntityOpTypeError_ImplementsError | tautology (compile-time assignment) | TestInvalidEntityOpTypeError_Message |
| pkg/service | TestBatchEntityEmptyOpsError_ImplementsError | tautology (compile-time assignment) | TestBatchEntityEmptyOpsError_Message |
| pkg/service | TestVectorNotFoundErrorImplementsError | tests errors.As on the concrete pointer (stdlib) | TestVectorNotFoundError |
| pkg/service | TestNamespaceNotFoundErrorImplementsError | tests errors.As on the concrete pointer (stdlib) | TestNamespaceNotFoundError |
| pkg/service | TestNamespaceExistsErrorImplementsError | tests errors.As on the concrete pointer (stdlib) | TestNamespaceExistsError |
| pkg/service | TestDimensionMismatchErrorImplementsError | tests errors.As on the concrete pointer (stdlib) | TestDimensionMismatchError |
| pkg/service | TestInvalidVectorErrorImplementsError | tests errors.As on the concrete pointer (stdlib) | TestInvalidVectorError |
| pkg/service | TestInvalidNamespaceNameErrorImplementsError | tests errors.As on the concrete pointer (stdlib) | TestInvalidNamespaceNameError |
| pkg/audit | TestAggregateLogger_ImplementsAuditLogger | tautology (no assertion: `var _ = NewAggregateLogger()`) | TestAggregateLogger_ImplementsVerifier |
| pkg/sql/engine | TestUnsupportedSyntaxError_ImplementsError | weaker duplicate of the message check | TestUnsupportedSyntaxError_Error |
| pkg/api/quic/wire | TestMethodTooLongErrorImplementsError, TestPayloadTooLargeErrorImplementsError, TestReadErrorImplementsError, TestWriteErrorImplementsError | tautology (compile-time assignment) | the matching TestXxxErrorMessage |
| pkg/api/quic/wire | TestMethodTooLongErrorWithDifferentValues | duplicate with other values | TestMethodTooLongErrorMessage |
| pkg/api/quic/wire | TestPayloadTooLargeErrorWithDifferentValues | duplicate with other values | TestPayloadTooLargeErrorMessage |
| pkg/api/quic/wire | TestReadErrorUnwrap | lower layer of errors.Is through Unwrap | TestReadErrorUnwrapWithErrorsIs |
| pkg/api/quic/wire | TestWriteErrorUnwrap | lower layer of errors.Is through Unwrap | TestWriteErrorUnwrapWithErrorsIs |
| pkg/api/unix | TestWithLock_Applied, TestWithTenantService_Applied, TestWithAuthFactorService_Applied, TestWithAuditLogger_Applied | tautology (With…(nil) on a field NewServer leaves nil) | TestStartRegistersEveryWiredService, TestStartInstallsTheAuthMiddlewareAndAuditService |
| pkg/api/unix | TestWithEncryptionMigrator_Applied, TestWithTenantBarrierService_Applied, TestWithSQLService_Applied, TestWithSealedWindowProbe_Applied, TestWithPINBackend_Nil | tautology (nil option on a nil default) | the matching TestWithXxx_NonNil / _Applied |
| pkg/api/unix | TestWithEmbedding_Nil, TestWithRAGService_Nil, TestWithBackupService_Nil, TestWithRebalanceService_Nil | tautology (nil option on a nil default) | the matching TestWithXxx_Applied |
| pkg/api/unix | TestWithAllNewOptions_StartStop | Start with only nil options, duplicate of the plain Start path | TestStartRegistersEveryWiredService |
| pkg/api/unix | TestStart_WithTenantBarrierService_Registered | duplicate (same option in the every-service Start) | TestStartRegistersEveryWiredService |
| pkg/api/unix | TestWithUserService_Applied, TestWithRoleService_Applied, TestWithCertProvider_Applied, TestWithCertEnrollment_NilIsNil, TestWithAuthMiddleware_Applied | tautology (nil option on a nil default) | TestStart_WithAllOptionalServices, TestWithCertEnrollment_Applied, TestStartInstallsTheAuthMiddlewareAndAuditService |
| pkg/api/unix | TestWithBarrierService_Applied, TestWithCeremonyService_Applied, TestWithCredentialService_Applied, TestWithCollectionService_Applied, TestWithMVCC_Applied, TestWithTransaction_Applied, TestWithBatch_Applied, TestWithWatch_Applied, TestWithLease_Applied, TestWithIndex_Applied, TestWithTimeSeries_Applied, TestWithVector_Applied, TestWithDatabaseService_Applied | tautology (nil option on a nil default) | TestStart_WithNonNilOptionalServices, TestStartRegistersEveryWiredService |
| pkg/api/grpc | TestWithLock_Nil, TestWithPINBackend_Nil, TestWithAuthFactorService_ServerOption, TestWithUserAuthFactorService_Nil, TestWithTenantService_ServerOption | tautology (nil option on a nil default) | TestWithLock_ServerOption, TestWithPINBackend_ServerOption, TestWithUserAuthFactorService_ServerOption |
| pkg/api/rest | TestWithCollectionService_NilOption | tautology (nil option on a nil default) | TestWithCollectionService_Option |
| cmd/qrdb | TestSQLExecutionError_ImplementsError | tautology (compile-time assignment) | TestSQLExecutionError_Error |
| pkg/sql/provider | TestCollectionTable_Implements{Insertable,Deletable,Updatable,Replaceable}Table, TestCollectionWriter_ImplementsRowReplacer | duplicate of the package-level `var _ sqle.X` guards in collection_table.go | the compile-time guards; the collection INSERT/DELETE/UPDATE/REPLACE tests |
| pkg/sql/provider | TestEntityTable_Implements{Table,InsertableTable,DeletableTable,UpdatableTable,ReplaceableTable,IndexAddressableTable,IndexedTable}, TestEntityWriter_ImplementsRow{Inserter,Deleter,Updater,Replacer} | duplicate of the guards in entity_table.go | the compile-time guards |
| pkg/sql/provider | TestKVTable_Implements{Insertable,Deletable,Updatable,Replaceable}Table, TestKVWriter_ImplementsRowReplacer | duplicate of the guards in kv_table.go | the compile-time guards |
| pkg/sql/provider | TestProvider_ImplementsDatabaseProvider, TestProvider_ImplementsMutableDatabaseProvider, TestDatabase_ImplementsDatabase | duplicate of the guards in provider.go | the compile-time guards |
| pkg/sql/provider | TestQRDBSession_ImplementsTransactionSession | duplicate of the guard in session.go | the compile-time guard |
| pkg/sql/provider | TestMetricsTable_ImplementsTable, TestNamespacesTable_ImplementsTable, TestDatapointsTable_ImplementsTable, TestVectorsTable_ImplementsTable | duplicate of the guards in metrics_table.go, namespaces_table.go, timeseries_table.go, vector_table.go | the compile-time guards |
| sdk/go/transport/quic | TestTransport_Implements{Transport,KVClient,AdminClient,MVCCClient,TransactionClient,BatchClient,LeaseClient,UserClient,RoleClient,CertClient}Interface | duplicate of the package-level guards in transport.go, transport_auth.go, transport_mvcc.go | the compile-time guards |
| pkg/database | TestBarrierBackend_InterfaceCompliance, TestStandaloneBackend_InterfaceCompliance, TestBackendStorageAdapter_InterfaceCompliance | duplicate of the package-level guards in backend_barrier.go, backend_standalone.go, backend_storage_adapter.go | the compile-time guards |
| pkg/engine | TestKVEngine_ImplementsEngine, TestMVCCEngine_ImplementsEngine | duplicate of the package-level guards in kv_engine.go, mvcc_engine.go | the compile-time guards |
| pkg/index | TestMemoryIndex_ImplementsIndex, TestMemoryIndex_ImplementsIndex_ViaNewMemoryIndex, TestStoragePebbleIndex_ImplementsIndex, TestStoragePebbleIndex_ImplementsIndexWithContext | duplicate of the package-level guards in memory_index.go, storage_pebble_index.go | the compile-time guards |
| pkg/index/pebbleindex | TestIndex_ImplementsIndex, TestIndex_ImplementsIndex_ViaConstructor | duplicate of the package-level guard in index.go | the compile-time guard |
| pkg/store | TestMVCCStateMachine_Implements{DiskStateMachine,Hasher}, TestPebbleStateMachine_Implements{DiskStateMachine,Hasher,NALookup} | duplicate of the package-level guards in mvcc.go, pebble.go | the compile-time guards |

## Removed tests and what still covers them

One row per test function this branch deletes (`git diff origin/main...HEAD -- '*_test.go'`, 200 functions). The last column names the retained test (file:func) or the production compile-time guard (file:line) that still checks the behaviour. Every row has a retained check, so no test was restored.

### pkg/api

| Removed test | Behaviour it checked; why removed | Still checked by |
|---|---|---|
| `pkg/api/grpc/grpc_coverage2_test.go:TestWithLock_Nil` | `WithLock(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/grpc/grpc_coverage2_test.go:TestWithLock_ServerOption` |
| `pkg/api/grpc/grpc_coverage2_test.go:TestWithPINBackend_Nil` | `WithPINBackend(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/grpc/grpc_coverage2_test.go:TestWithPINBackend_ServerOption` |
| `pkg/api/grpc/grpc_coverage2_test.go:TestWithAuthFactorService_ServerOption` | `WithAuthFactorService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/grpc/authfactor_server_test.go:TestServer_WithAuthFactorService_ServesEnroll`, `pkg/api/grpc/service_lock_test.go:TestServerOption_WithAuthFactorService` |
| `pkg/api/grpc/grpc_coverage2_test.go:TestWithUserAuthFactorService_Nil` | `WithUserAuthFactorService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/grpc/grpc_coverage2_test.go:TestWithUserAuthFactorService_ServerOption` |
| `pkg/api/grpc/grpc_coverage2_test.go:TestWithTenantService_ServerOption` | `WithTenantService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/grpc/service_lock_test.go:TestRegisterService_WithTenantService` |
| `pkg/api/quic/wire/wire_test.go:TestMethodTooLongErrorWithDifferentValues` | method too long error with different values; duplicate with other values | `pkg/api/quic/wire/wire_test.go:TestMethodTooLongErrorMessage` |
| `pkg/api/quic/wire/wire_test.go:TestMethodTooLongErrorImplementsError` | `MethodTooLongError` satisfies `error`; tautology (compile-time assignment) | `pkg/api/quic/wire/wire_test.go:TestMethodTooLongErrorMessage` |
| `pkg/api/quic/wire/wire_test.go:TestPayloadTooLargeErrorWithDifferentValues` | payload too large error with different values; duplicate with other values | `pkg/api/quic/wire/wire_test.go:TestPayloadTooLargeErrorMessage` |
| `pkg/api/quic/wire/wire_test.go:TestPayloadTooLargeErrorImplementsError` | `PayloadTooLargeError` satisfies `error`; tautology (compile-time assignment) | `pkg/api/quic/wire/wire_test.go:TestPayloadTooLargeErrorMessage` |
| `pkg/api/quic/wire/wire_test.go:TestReadErrorUnwrap` | read error unwrap; lower layer of errors.Is through Unwrap | `pkg/api/quic/wire/wire_test.go:TestReadErrorUnwrapWithErrorsIs` |
| `pkg/api/quic/wire/wire_test.go:TestReadErrorImplementsError` | `ReadError` satisfies `error`; tautology (compile-time assignment) | `pkg/api/quic/wire/wire_test.go:TestReadErrorMessage` |
| `pkg/api/quic/wire/wire_test.go:TestWriteErrorUnwrap` | write error unwrap; lower layer of errors.Is through Unwrap | `pkg/api/quic/wire/wire_test.go:TestWriteErrorUnwrapWithErrorsIs` |
| `pkg/api/quic/wire/wire_test.go:TestWriteErrorImplementsError` | `WriteError` satisfies `error`; tautology (compile-time assignment) | `pkg/api/quic/wire/wire_test.go:TestWriteErrorMessage` |
| `pkg/api/rest/handlers_collection_test.go:TestWithCollectionService_NilOption` | `WithCollectionService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/rest/handlers_collection_test.go:TestWithCollectionService_Option` |
| `pkg/api/unix/options_embedding_test.go:TestWithEmbedding_Nil` | `WithEmbedding(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/options_embedding_test.go:TestWithEmbedding_Applied` |
| `pkg/api/unix/options_embedding_test.go:TestWithRAGService_Nil` | `WithRAGService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/options_embedding_test.go:TestWithRAGService_Applied` |
| `pkg/api/unix/options_embedding_test.go:TestWithBackupService_Nil` | `WithBackupService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/options_embedding_test.go:TestWithBackupService_Applied` |
| `pkg/api/unix/options_embedding_test.go:TestWithRebalanceService_Nil` | `WithRebalanceService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/options_embedding_test.go:TestWithRebalanceService_Applied` |
| `pkg/api/unix/options_sql_sealed_test.go:TestWithSQLService_Applied` | `WithSQLService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/options_sql_sealed_test.go:TestWithSQLService_NonNil` |
| `pkg/api/unix/options_sql_sealed_test.go:TestWithSealedWindowProbe_Applied` | `WithSealedWindowProbe(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/options_sql_sealed_test.go:TestWithSealedWindowProbe_NonNil` |
| `pkg/api/unix/options_test.go:TestWithLock_Applied` | `WithLock(nil)` leaves a nil field nil; tautology (With…(nil) on a field NewServer leaves nil), the setter is checked with a real value by the retained test | `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/options_test.go:TestWithTenantService_Applied` | `WithTenantService(nil)` leaves a nil field nil; tautology (With…(nil) on a field NewServer leaves nil), the setter is checked with a real value by the retained test | `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/options_test.go:TestWithEncryptionMigrator_Applied` | `WithEncryptionMigrator(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/options_test.go:TestWithEncryptionMigrator_NonNil` |
| `pkg/api/unix/options_test.go:TestWithAuthFactorService_Applied` | `WithAuthFactorService(nil)` leaves a nil field nil; tautology (With…(nil) on a field NewServer leaves nil), the setter is checked with a real value by the retained test | `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/options_test.go:TestWithTenantBarrierService_Applied` | `WithTenantBarrierService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/options_test.go:TestWithTenantBarrierService_NonNil` |
| `pkg/api/unix/options_test.go:TestWithAuditLogger_Applied` | `WithAuditLogger(nil)` leaves a nil field nil; tautology (With…(nil) on a field NewServer leaves nil), the setter is checked with a real value by the retained test | `pkg/api/unix/options_wiring_test.go:TestStartInstallsTheAuthMiddlewareAndAuditService` |
| `pkg/api/unix/options_test.go:TestWithAllNewOptions_StartStop` | `WithAllNewOptions_StartStop(nil)` leaves a nil field nil; Start with only nil options, duplicate of the plain Start path, the setter is checked with a real value by the retained test | `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/options_test.go:TestStart_WithTenantBarrierService_Registered` | `Start`: with tenant barrier service, registered; duplicate (same option in the every-service Start) | `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/server_pin_test.go:TestWithPINBackend_Nil` | `WithPINBackend(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_pin_test.go:TestWithPINBackend_Applied` |
| `pkg/api/unix/server_test.go:TestWithUserService_Applied` | `WithUserService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithAllOptionalServices` |
| `pkg/api/unix/server_test.go:TestWithRoleService_Applied` | `WithRoleService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithAllOptionalServices` |
| `pkg/api/unix/server_test.go:TestWithCertProvider_Applied` | `WithCertProvider(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithAllOptionalServices` |
| `pkg/api/unix/server_test.go:TestWithCertEnrollment_NilIsNil` | `WithCertEnrollment(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestWithCertEnrollment_Applied` |
| `pkg/api/unix/server_test.go:TestWithAuthMiddleware_Applied` | `WithAuthMiddleware(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/options_wiring_test.go:TestStartInstallsTheAuthMiddlewareAndAuditService` |
| `pkg/api/unix/server_test.go:TestWithBarrierService_Applied` | `WithBarrierService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithNonNilOptionalServices`, `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/server_test.go:TestWithCeremonyService_Applied` | `WithCeremonyService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithNonNilOptionalServices`, `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/server_test.go:TestWithCredentialService_Applied` | `WithCredentialService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithNonNilOptionalServices`, `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/server_test.go:TestWithCollectionService_Applied` | `WithCollectionService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithNonNilOptionalServices`, `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/server_test.go:TestWithMVCC_Applied` | `WithMVCC(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithNonNilOptionalServices`, `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/server_test.go:TestWithTransaction_Applied` | `WithTransaction(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithNonNilOptionalServices`, `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/server_test.go:TestWithBatch_Applied` | `WithBatch(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithNonNilOptionalServices`, `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/server_test.go:TestWithWatch_Applied` | `WithWatch(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithNonNilOptionalServices`, `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/server_test.go:TestWithLease_Applied` | `WithLease(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithNonNilOptionalServices`, `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/server_test.go:TestWithIndex_Applied` | `WithIndex(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithNonNilOptionalServices`, `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/server_test.go:TestWithTimeSeries_Applied` | `WithTimeSeries(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithNonNilOptionalServices`, `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/server_test.go:TestWithVector_Applied` | `WithVector(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithNonNilOptionalServices`, `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |
| `pkg/api/unix/server_test.go:TestWithDatabaseService_Applied` | `WithDatabaseService(nil)` leaves a nil field nil; tautology (nil option on a nil default), the setter is checked with a real value by the retained test | `pkg/api/unix/server_test.go:TestStart_WithNonNilOptionalServices`, `pkg/api/unix/options_wiring_test.go:TestStartRegistersEveryWiredService` |

### pkg/database

| Removed test | Behaviour it checked; why removed | Still checked by |
|---|---|---|
| `pkg/database/backend_barrier_test.go:TestBarrierBackend_InterfaceCompliance` | `*BarrierBackend` satisfies `Backend`; duplicate of the guard | `pkg/database/backend_barrier.go:73` `var _ Backend = (*BarrierBackend)(nil)` |
| `pkg/database/backend_standalone_test.go:TestStandaloneBackend_InterfaceCompliance` | `*StandaloneBackend` satisfies `Backend`; duplicate of the guard | `pkg/database/backend_standalone.go:25` `var _ Backend = (*StandaloneBackend)(nil)` |
| `pkg/database/backend_storage_adapter_test.go:TestBackendStorageAdapter_InterfaceCompliance` | `*BackendStorageAdapter` satisfies `seal.StorageBackend`; duplicate of the guard | `pkg/database/backend_storage_adapter.go:38` `var _ seal.StorageBackend = (*BackendStorageAdapter)(nil)` |

### pkg/engine

| Removed test | Behaviour it checked; why removed | Still checked by |
|---|---|---|
| `pkg/engine/kv_engine_test.go:TestKVEngine_ImplementsEngine` | `*KVEngine` satisfies `Engine`; duplicate of the guard | `pkg/engine/kv_engine.go:40` `var _ Engine = (*KVEngine)(nil)` |
| `pkg/engine/mvcc_engine_test.go:TestMVCCEngine_ImplementsEngine` | `*MVCCEngine` satisfies `Engine`; duplicate of the guard | `pkg/engine/mvcc_engine.go:2840` `var _ Engine = (*MVCCEngine)(nil)` |

### pkg/index

| Removed test | Behaviour it checked; why removed | Still checked by |
|---|---|---|
| `pkg/index/memory_index_test.go:TestMemoryIndex_ImplementsIndex` | `*MemoryIndex` satisfies `Index`; duplicate of the guard | `pkg/index/memory_index.go:297` `var _ Index = (*MemoryIndex)(nil)` |
| `pkg/index/memory_index_test.go:TestMemoryIndex_ImplementsIndex_ViaNewMemoryIndex` | `*MemoryIndex` satisfies `Index`; duplicate of the guard | `pkg/index/memory_index.go:297` `var _ Index = (*MemoryIndex)(nil)` |
| `pkg/index/pebbleindex/index_test.go:TestIndex_ImplementsIndex` | `*Index` satisfies `index.Index`; duplicate of the guard | `pkg/index/pebbleindex/index.go:564` `var _ index.Index = (*Index)(nil)` |
| `pkg/index/pebbleindex/index_test.go:TestIndex_ImplementsIndex_ViaConstructor` | `*Index` satisfies `index.Index`; duplicate of the guard | `pkg/index/pebbleindex/index.go:564` `var _ index.Index = (*Index)(nil)` |
| `pkg/index/storage_pebble_index_test.go:TestStoragePebbleIndex_ImplementsIndex` | `*StoragePebbleIndex` satisfies `Index`; duplicate of the guard | `pkg/index/storage_pebble_index.go:933` `var _ Index = (*StoragePebbleIndex)(nil)` |
| `pkg/index/storage_pebble_index_test.go:TestStoragePebbleIndex_ImplementsIndexWithContext` | `*StoragePebbleIndex` satisfies `IndexWithContext`; duplicate of the guard | `pkg/index/storage_pebble_index.go:934` `var _ IndexWithContext = (*StoragePebbleIndex)(nil)` |

### pkg/server

| Removed test | Behaviour it checked; why removed | Still checked by |
|---|---|---|
| `pkg/server/server_test.go:TestWithCertEnrollment_NonNil` | `WithCertEnrollment`: non nil; option-setter assignment, lower layer of the served call | `pkg/server/embedded_mint_delegation_test.go:TestMintDelegation_ServerHandsTheRequestToTheService` |
| `pkg/server/server_test.go:TestWithDatabaseManager_NonNil` | `WithDatabaseManager`: non nil; option-setter assignment, lower layer of the served call | `pkg/server/server_sealed_database_sql_test.go:TestSealedDatabase_RefusedOverSQLWire` |
| `pkg/server/server_with_options_test.go:TestWithStrategyRegistry_Applied` | `WithStrategyRegistry`: applied; duplicate (same field asserted; the extra lookup tests pkg/seal) | `pkg/server/server_with_options_test.go:TestWithAllInjections_Compose` |
| `pkg/server/server_with_options_test.go:TestWithFactorRegistry_Applied` | `WithFactorRegistry`: applied; duplicate (same field asserted; the extra List tests pkg/authfactors) | `pkg/server/server_with_options_test.go:TestWithAllInjections_Compose` |
| `pkg/server/server_with_options_test.go:TestWithTrustAnchors_Applied` | `WithTrustAnchors`: applied; lower layer of the exported crypto options | `pkg/server/server_with_options_test.go:TestResolveCryptoOptions_SurfacesTrustAnchors` |
| `pkg/server/server_with_options_test.go:TestWithTrustAnchors_UnsetIsNil` | `WithTrustAnchors`: unset is nil; lower layer of the exported crypto options | `pkg/server/server_with_options_test.go:TestResolveCryptoOptions_TrustAnchorsNilWhenAbsent` |

### pkg/sql/provider

| Removed test | Behaviour it checked; why removed | Still checked by |
|---|---|---|
| `pkg/sql/provider/collection_table_test.go:TestCollectionTable_ImplementsInsertableTable` | `*CollectionTable` satisfies `sqle.InsertableTable`; duplicate of the guard | `pkg/sql/provider/collection_table.go:37` `var _ sqle.InsertableTable = (*CollectionTable)(nil)` |
| `pkg/sql/provider/collection_table_test.go:TestCollectionTable_ImplementsDeletableTable` | `*CollectionTable` satisfies `sqle.DeletableTable`; duplicate of the guard | `pkg/sql/provider/collection_table.go:38` `var _ sqle.DeletableTable = (*CollectionTable)(nil)` |
| `pkg/sql/provider/collection_table_test.go:TestCollectionTable_ImplementsUpdatableTable` | `*CollectionTable` satisfies `sqle.UpdatableTable`; duplicate of the guard | `pkg/sql/provider/collection_table.go:40` `var _ sqle.UpdatableTable = (*CollectionTable)(nil)` |
| `pkg/sql/provider/collection_table_test.go:TestCollectionTable_ImplementsReplaceableTable` | `*CollectionTable` satisfies `sqle.ReplaceableTable`; duplicate of the guard | `pkg/sql/provider/collection_table.go:39` `var _ sqle.ReplaceableTable = (*CollectionTable)(nil)` |
| `pkg/sql/provider/collection_table_test.go:TestCollectionWriter_ImplementsRowReplacer` | `*collectionWriter` satisfies `sqle.RowReplacer`; duplicate of the guard | `pkg/sql/provider/collection_table.go:457` `var _ sqle.RowReplacer = (*collectionWriter)(nil)` |
| `pkg/sql/provider/entity_table_test.go:TestEntityTable_ImplementsTable` | `*EntityTable` satisfies `sqle.Table`; duplicate of the guard | `pkg/sql/provider/entity_table.go:36` `var _ sqle.Table = (*EntityTable)(nil)` |
| `pkg/sql/provider/entity_table_test.go:TestEntityTable_ImplementsInsertableTable` | `*EntityTable` satisfies `sqle.InsertableTable`; duplicate of the guard | `pkg/sql/provider/entity_table.go:37` `var _ sqle.InsertableTable = (*EntityTable)(nil)` |
| `pkg/sql/provider/entity_table_test.go:TestEntityTable_ImplementsDeletableTable` | `*EntityTable` satisfies `sqle.DeletableTable`; duplicate of the guard | `pkg/sql/provider/entity_table.go:38` `var _ sqle.DeletableTable = (*EntityTable)(nil)` |
| `pkg/sql/provider/entity_table_test.go:TestEntityTable_ImplementsUpdatableTable` | `*EntityTable` satisfies `sqle.UpdatableTable`; duplicate of the guard | `pkg/sql/provider/entity_table.go:40` `var _ sqle.UpdatableTable = (*EntityTable)(nil)` |
| `pkg/sql/provider/entity_table_test.go:TestEntityTable_ImplementsReplaceableTable` | `*EntityTable` satisfies `sqle.ReplaceableTable`; duplicate of the guard | `pkg/sql/provider/entity_table.go:39` `var _ sqle.ReplaceableTable = (*EntityTable)(nil)` |
| `pkg/sql/provider/entity_table_test.go:TestEntityTable_ImplementsIndexAddressableTable` | `*EntityTable` satisfies `sqle.IndexAddressableTable`; duplicate of the guard | `pkg/sql/provider/entity_table.go:41` `var _ sqle.IndexAddressableTable = (*EntityTable)(nil)` |
| `pkg/sql/provider/entity_table_test.go:TestEntityTable_ImplementsIndexedTable` | `*EntityTable` satisfies `sqle.IndexedTable`; duplicate of the guard | `pkg/sql/provider/entity_table.go:42` `var _ sqle.IndexedTable = (*EntityTable)(nil)` |
| `pkg/sql/provider/entity_table_test.go:TestEntityWriter_ImplementsRowInserter` | `*entityWriter` satisfies `sqle.RowInserter`; duplicate of the guard | `pkg/sql/provider/entity_table.go:459` `var _ sqle.RowInserter = (*entityWriter)(nil)` |
| `pkg/sql/provider/entity_table_test.go:TestEntityWriter_ImplementsRowDeleter` | `*entityWriter` satisfies `sqle.RowDeleter`; duplicate of the guard | `pkg/sql/provider/entity_table.go:460` `var _ sqle.RowDeleter = (*entityWriter)(nil)` |
| `pkg/sql/provider/entity_table_test.go:TestEntityWriter_ImplementsRowUpdater` | `*entityWriter` satisfies `sqle.RowUpdater`; duplicate of the guard | `pkg/sql/provider/entity_table.go:461` `var _ sqle.RowUpdater = (*entityWriter)(nil)` |
| `pkg/sql/provider/entity_table_test.go:TestEntityWriter_ImplementsRowReplacer` | `*entityWriter` satisfies `sqle.RowReplacer`; duplicate of the guard | `pkg/sql/provider/entity_table.go:462` `var _ sqle.RowReplacer = (*entityWriter)(nil)` |
| `pkg/sql/provider/kv_table_test.go:TestKVTable_ImplementsInsertableTable` | `*KVTable` satisfies `sqle.InsertableTable`; duplicate of the guard | `pkg/sql/provider/kv_table.go:56` `var _ sqle.InsertableTable = (*KVTable)(nil)` |
| `pkg/sql/provider/kv_table_test.go:TestKVTable_ImplementsDeletableTable` | `*KVTable` satisfies `sqle.DeletableTable`; duplicate of the guard | `pkg/sql/provider/kv_table.go:57` `var _ sqle.DeletableTable = (*KVTable)(nil)` |
| `pkg/sql/provider/kv_table_test.go:TestKVTable_ImplementsUpdatableTable` | `*KVTable` satisfies `sqle.UpdatableTable`; duplicate of the guard | `pkg/sql/provider/kv_table.go:59` `var _ sqle.UpdatableTable = (*KVTable)(nil)` |
| `pkg/sql/provider/kv_table_test.go:TestKVTable_ImplementsReplaceableTable` | `*KVTable` satisfies `sqle.ReplaceableTable`; duplicate of the guard | `pkg/sql/provider/kv_table.go:58` `var _ sqle.ReplaceableTable = (*KVTable)(nil)` |
| `pkg/sql/provider/kv_table_test.go:TestKVWriter_ImplementsRowReplacer` | `*kvWriter` satisfies `sqle.RowReplacer`; duplicate of the guard | `pkg/sql/provider/kv_table.go:298` `var _ sqle.RowReplacer = (*kvWriter)(nil)` |
| `pkg/sql/provider/metrics_table_test.go:TestMetricsTable_ImplementsTable` | `*MetricsTable` satisfies `sqle.Table`; duplicate of the guard | `pkg/sql/provider/metrics_table.go:51` `var _ sqle.Table = (*MetricsTable)(nil)` |
| `pkg/sql/provider/namespaces_table_test.go:TestNamespacesTable_ImplementsTable` | `*NamespacesTable` satisfies `sqle.Table`; duplicate of the guard | `pkg/sql/provider/namespaces_table.go:57` `var _ sqle.Table = (*NamespacesTable)(nil)` |
| `pkg/sql/provider/provider_test.go:TestProvider_ImplementsDatabaseProvider` | `*Provider` satisfies `sqle.DatabaseProvider`; duplicate of the guard | `pkg/sql/provider/provider.go:40` `var _ sqle.DatabaseProvider = (*Provider)(nil)` |
| `pkg/sql/provider/provider_test.go:TestDatabase_ImplementsDatabase` | `*Database` satisfies `sqle.Database`; duplicate of the guard | `pkg/sql/provider/provider.go:42` `var _ sqle.Database = (*Database)(nil)` |
| `pkg/sql/provider/provider_test.go:TestProvider_ImplementsMutableDatabaseProvider` | `*Provider` satisfies `sqle.MutableDatabaseProvider`; duplicate of the guard | `pkg/sql/provider/provider.go:41` `var _ sqle.MutableDatabaseProvider = (*Provider)(nil)` |
| `pkg/sql/provider/session_transaction_test.go:TestQRDBSession_ImplementsTransactionSession` | `*QRDBSession` satisfies `sqle.TransactionSession`; duplicate of the guard | `pkg/sql/provider/session.go:106` `var _ sqle.TransactionSession = (*QRDBSession)(nil)` |
| `pkg/sql/provider/timeseries_table_test.go:TestDatapointsTable_ImplementsTable` | `*DatapointsTable` satisfies `sqle.Table`; duplicate of the guard | `pkg/sql/provider/timeseries_table.go:58` `var _ sqle.Table = (*DatapointsTable)(nil)` |
| `pkg/sql/provider/vector_table_test.go:TestVectorsTable_ImplementsTable` | `*VectorsTable` satisfies `sqle.Table`; duplicate of the guard | `pkg/sql/provider/vector_table.go:51` `var _ sqle.Table = (*VectorsTable)(nil)` |

### pkg/store

| Removed test | Behaviour it checked; why removed | Still checked by |
|---|---|---|
| `pkg/store/mvcc_test.go:TestMVCCStateMachine_ImplementsDiskStateMachine` | `*MVCCStateMachine` satisfies `sm.DiskStateMachine`; duplicate of the guard | `pkg/store/mvcc.go:967` `var _ sm.DiskStateMachine = (*MVCCStateMachine)(nil)` |
| `pkg/store/mvcc_test.go:TestMVCCStateMachine_ImplementsHasher` | `*MVCCStateMachine` satisfies `sm.Hasher`; duplicate of the guard | `pkg/store/mvcc.go:968` `var _ sm.Hasher = (*MVCCStateMachine)(nil)` |
| `pkg/store/store_test.go:TestPebbleStateMachine_ImplementsDiskStateMachine` | `*PebbleStateMachine` satisfies `sm.DiskStateMachine`; duplicate of the guard | `pkg/store/pebble.go:2885` `var _ sm.DiskStateMachine = (*PebbleStateMachine)(nil)` |
| `pkg/store/store_test.go:TestPebbleStateMachine_ImplementsHasher` | `*PebbleStateMachine` satisfies `sm.Hasher`; duplicate of the guard | `pkg/store/pebble.go:2886` `var _ sm.Hasher = (*PebbleStateMachine)(nil)` |
| `pkg/store/store_test.go:TestPebbleStateMachine_ImplementsNALookup` | `*PebbleStateMachine` satisfies `sm.NALookup`; duplicate of the guard | `pkg/store/pebble.go:2887` `var _ sm.NALookup = (*PebbleStateMachine)(nil)` |

### pkg/tenant

| Removed test | Behaviour it checked; why removed | Still checked by |
|---|---|---|
| `pkg/tenant/context_test.go:TestNestedContextsInnerTenantWins` | nested contexts inner tenant wins; tests context.WithValue shadowing (stdlib) | `pkg/tenant/context_test.go:TestWithTenantIDAndFromContextRoundTrip` |
| `pkg/tenant/context_test.go:TestWithTenantIDPreservesParentValues` | with tenant idpreserves parent values; tests context.WithValue chaining (stdlib) | `pkg/tenant/context_test.go:TestWithTenantIDAndFromContextRoundTrip` |
| `pkg/tenant/context_test.go:TestFromContextWithCancelledContext` | from context with cancelled context; tests that cancellation keeps values (stdlib) | `pkg/tenant/context_test.go:TestWithTenantIDAndFromContextRoundTrip` |
| `pkg/tenant/errors_test.go:TestInvalidTenantIDErrorMessage` | invalid tenant iderror message; duplicate (substring of the exact format) | `pkg/tenant/errors_test.go:TestInvalidTenantIDErrorFormat` |
| `pkg/tenant/errors_test.go:TestInvalidTenantIDErrorEmptyTenantID` | invalid tenant iderror empty tenant id; duplicate of the format path | `pkg/tenant/errors_test.go:TestInvalidTenantIDErrorFormat` |
| `pkg/tenant/errors_test.go:TestInvalidTenantIDErrorImplementsErrorInterface` | `InvalidTenantIDError` satisfies `error`; tautology | `pkg/tenant/errors_test.go:TestInvalidTenantIDErrorFormat` |
| `pkg/tenant/errors_test.go:TestInvalidTenantIDCharError_NonEmpty` | `InvalidTenantIDCharError`: non empty; weaker duplicate | `pkg/tenant/errors_test.go:TestInvalidTenantIDCharError_ContainsHex` |
| `pkg/tenant/errors_test.go:TestInvalidTenantIDCharError_ImplementsError` | `InvalidTenantIDCharError` satisfies `error`; tautology | `pkg/tenant/errors_test.go:TestInvalidTenantIDCharError_ContainsHex` |
| `pkg/tenant/errors_test.go:TestTenantStoreError_ReadTransient_TrueWhenSetByNewStoreError` | `TenantStoreError`: read transient, true when set by new store error; duplicate | `pkg/tenant/errors_test.go:TestNewStoreError_NonDecryptCauseIsTransient` |
| `pkg/tenant/errors_test.go:TestIsTransientCause_NonDecryptCause` | `IsTransientCause`: non decrypt cause; lower layer of newStoreError (its only caller) | `pkg/tenant/errors_test.go:TestNewStoreError_NonDecryptCauseIsTransient` |
| `pkg/tenant/errors_test.go:TestIsTransientCause_DecryptCause` | `IsTransientCause`: decrypt cause; lower layer of newStoreError (its only caller) | `pkg/tenant/errors_test.go:TestNewStoreError_DecryptCauseIsNotTransient` |
| `pkg/tenant/kv_service_test.go:TestKVTenantBarrierSeal_NotEnabled2` | `KVTenantBarrierSeal`: not enabled2; duplicate | `pkg/tenant/quota_barrier_test.go:TestKVTenantBarrierSeal_NotEnabled` |
| `pkg/tenant/kv_service_test.go:TestKVTenantBarrierStatus_NotEnabled2` | `KVTenantBarrierStatus`: not enabled2; duplicate | `pkg/tenant/quota_barrier_test.go:TestKVTenantBarrierStatus_NotEnabled` |
| `pkg/tenant/quota_barrier_test.go:TestQuotaExceededError_ImplementsError` | `QuotaExceededError` satisfies `error`; tautology (compile-time assignment) | `pkg/tenant/quota_barrier_test.go:TestQuotaExceededError_Error` |
| `pkg/tenant/reserved_test.go:TestValidateReservedTenantID_System` | `ValidateReservedTenantID`: system; lower layer of CreateTenant | `pkg/tenant/service_test.go:TestTenantService_CreateRejectsSystemID` |
| `pkg/tenant/reserved_test.go:TestValidateReservedTenantID_LegacyDefault` | `ValidateReservedTenantID`: legacy default; lower layer of CreateTenant | `pkg/tenant/service_test.go:TestTenantService_CreateRejectsLegacyDefaultID` |
| `pkg/tenant/update_tenant_test.go:TestIncrementQuotaUsage_Success` | `IncrementQuotaUsage`: success; duplicate | `pkg/tenant/quota_barrier_test.go:TestTenantService_IncrementQuotaUsage_Success` |
| `pkg/tenant/update_tenant_test.go:TestIncrementQuotaUsage_ExceedsLimit` | `IncrementQuotaUsage`: exceeds limit; duplicate | `pkg/tenant/quota_barrier_test.go:TestTenantService_IncrementQuotaUsage_AtLimit`, `pkg/tenant/quota_barrier_test.go:TestTenantService_IncrementQuotaUsage_QuotaExceeded` |
| `pkg/tenant/update_tenant_test.go:TestIncrementQuotaUsage_UnlimitedLimit` | `IncrementQuotaUsage`: unlimited limit; duplicate | `pkg/tenant/quota_barrier_test.go:TestTenantService_IncrementQuotaUsage_UnlimitedLimit` |
| `pkg/tenant/update_tenant_test.go:TestIncrementQuotaUsage_ZeroDelta` | `IncrementQuotaUsage`: zero delta; duplicate | `pkg/tenant/quota_barrier_test.go:TestTenantService_IncrementQuotaUsage_ZeroDelta` |
| `pkg/tenant/update_tenant_test.go:TestIncrementQuotaUsage_EmptyTenantID` | `IncrementQuotaUsage`: empty tenant id; duplicate | `pkg/tenant/quota_barrier_test.go:TestTenantService_IncrementQuotaUsage_EmptyID` |
| `pkg/tenant/update_tenant_test.go:TestDecrementQuotaUsage_Success` | `DecrementQuotaUsage`: success; duplicate | `pkg/tenant/quota_barrier_test.go:TestTenantService_DecrementQuotaUsage_Success` |
| `pkg/tenant/update_tenant_test.go:TestDecrementQuotaUsage_ClampToZero` | `DecrementQuotaUsage`: clamp to zero; duplicate | `pkg/tenant/quota_barrier_test.go:TestTenantService_DecrementQuotaUsage_ClampToZero` |
| `pkg/tenant/update_tenant_test.go:TestDecrementQuotaUsage_ZeroDelta` | `DecrementQuotaUsage`: zero delta; duplicate (weaker assertion) | `pkg/tenant/quota_barrier_test.go:TestTenantService_DecrementQuotaUsage_ZeroDelta` |
| `pkg/tenant/update_tenant_test.go:TestDecrementQuotaUsage_EmptyTenantID` | `DecrementQuotaUsage`: empty tenant id; duplicate | `pkg/tenant/quota_barrier_test.go:TestTenantService_DecrementQuotaUsage_EmptyID` |
| `pkg/tenant/update_tenant_test.go:TestGetQuotaUsage_Success` | `GetQuotaUsage`: success; duplicate | `pkg/tenant/quota_barrier_test.go:TestTenantService_GetQuotaUsage_Success` |
| `pkg/tenant/update_tenant_test.go:TestGetQuotaUsage_NoQuotasSetReturnsZero` | `GetQuotaUsage`: no quotas set returns zero; duplicate | `pkg/tenant/quota_barrier_test.go:TestTenantService_GetQuotaUsage_NilQuotas` |
| `pkg/tenant/update_tenant_test.go:TestGetQuotaUsage_EmptyTenantID` | `GetQuotaUsage`: empty tenant id; duplicate | `pkg/tenant/quota_barrier_test.go:TestTenantService_GetQuotaUsage_EmptyID` |
| `pkg/tenant/update_tenant_test.go:TestGetQuotaUsage_TenantNotFound` | `GetQuotaUsage`: tenant not found; duplicate | `pkg/tenant/quota_barrier_test.go:TestTenantService_GetQuotaUsage_NotFound` |

### sdk/go/transport/quic

| Removed test | Behaviour it checked; why removed | Still checked by |
|---|---|---|
| `sdk/go/transport/quic/transport_test.go:TestTransport_ImplementsTransportInterface` | `*Transport` satisfies `transport.Transport`; duplicate of the guard | `sdk/go/transport/quic/transport.go:41` `var _ transport.Transport = (*Transport)(nil)` |
| `sdk/go/transport/quic/transport_test.go:TestTransport_ImplementsKVClientInterface` | `*Transport` satisfies `transport.KVClient`; duplicate of the guard | `sdk/go/transport/quic/transport.go:42` `var _ transport.KVClient = (*Transport)(nil)` |
| `sdk/go/transport/quic/transport_test.go:TestTransport_ImplementsAdminClientInterface` | `*Transport` satisfies `transport.AdminClient`; duplicate of the guard | `sdk/go/transport/quic/transport.go:43` `var _ transport.AdminClient = (*Transport)(nil)` |
| `sdk/go/transport/quic/transport_test.go:TestTransport_ImplementsMVCCClientInterface` | `*Transport` satisfies `transport.MVCCClient`; duplicate of the guard | `sdk/go/transport/quic/transport_mvcc.go:29` `var _ transport.MVCCClient = (*Transport)(nil)` |
| `sdk/go/transport/quic/transport_test.go:TestTransport_ImplementsTransactionClientInterface` | `*Transport` satisfies `transport.TransactionClient`; duplicate of the guard | `sdk/go/transport/quic/transport_mvcc.go:30` `var _ transport.TransactionClient = (*Transport)(nil)` |
| `sdk/go/transport/quic/transport_test.go:TestTransport_ImplementsBatchClientInterface` | `*Transport` satisfies `transport.BatchClient`; duplicate of the guard | `sdk/go/transport/quic/transport_mvcc.go:31` `var _ transport.BatchClient = (*Transport)(nil)` |
| `sdk/go/transport/quic/transport_test.go:TestTransport_ImplementsLeaseClientInterface` | `*Transport` satisfies `transport.LeaseClient`; duplicate of the guard | `sdk/go/transport/quic/transport_mvcc.go:32` `var _ transport.LeaseClient = (*Transport)(nil)` |
| `sdk/go/transport/quic/transport_test.go:TestTransport_ImplementsUserClientInterface` | `*Transport` satisfies `transport.UserClient`; duplicate of the guard | `sdk/go/transport/quic/transport_auth.go:25` `var _ transport.UserClient = (*Transport)(nil)` |
| `sdk/go/transport/quic/transport_test.go:TestTransport_ImplementsRoleClientInterface` | `*Transport` satisfies `transport.RoleClient`; duplicate of the guard | `sdk/go/transport/quic/transport_auth.go:26` `var _ transport.RoleClient = (*Transport)(nil)` |
| `sdk/go/transport/quic/transport_test.go:TestTransport_ImplementsCertClientInterface` | `*Transport` satisfies `transport.CertClient`; duplicate of the guard | `sdk/go/transport/quic/transport_auth.go:27` `var _ transport.CertClient = (*Transport)(nil)` |

### Other packages

| Removed test | Behaviour it checked; why removed | Still checked by |
|---|---|---|
| `cmd/qrdb/bindings_sql_test.go:TestSQLExecutionError_ImplementsError` | `SQLExecutionError` satisfies `error`; tautology (compile-time assignment) | `cmd/qrdb/bindings_sql_test.go:TestSQLExecutionError_Error` |
| `pkg/audit/audit_logger_test.go:TestAggregateLogger_ImplementsAuditLogger` | `AggregateLogger`: implements audit logger; tautology (no assertion: `var _ = NewAggregateLogger()`) | `pkg/audit/verify_test.go:TestAggregateLogger_ImplementsVerifier` |
| `pkg/backend/errors_test.go:TestClusterOperationError_ImplementsError` | `ClusterOperationError` satisfies `error`; tautology (non-empty message) | `pkg/backend/errors_test.go:TestClusterOperationError_Message` |
| `pkg/backend/errors_test.go:TestUnsupportedOperationError_ImplementsError` | `UnsupportedOperationError` satisfies `error`; tautology (non-empty message) | `pkg/backend/errors_test.go:TestUnsupportedOperationError_Message` |
| `pkg/backend/errors_test.go:TestBackendClosedError_ImplementsError` | `BackendClosedError` satisfies `error`; tautology (non-empty message) | `pkg/backend/errors_test.go:TestBackendClosedError_Message` |
| `pkg/backend/errors_test.go:TestLookupTypeError_ImplementsError` | `LookupTypeError` satisfies `error`; tautology (non-empty message) | `pkg/backend/errors_test.go:TestLookupTypeError_Message` |
| `pkg/consistency/errors_test.go:TestUnknownLevelErrorImplementsError` | `UnknownLevelError` satisfies `error`; tautology (compile-time assignment) | `pkg/consistency/errors_test.go:TestUnknownLevelErrorMessage` |
| `pkg/consistency/errors_test.go:TestInvalidLevelErrorWithNegativeLevel` | invalid level error with negative level; duplicate with another value | `pkg/consistency/errors_test.go:TestInvalidLevelErrorMessage` |
| `pkg/consistency/errors_test.go:TestInvalidLevelErrorImplementsError` | `InvalidLevelError` satisfies `error`; tautology (compile-time assignment) | `pkg/consistency/errors_test.go:TestInvalidLevelErrorMessage` |
| `pkg/consistency/errors_test.go:TestInvalidTimeoutErrorWithLargeNegative` | invalid timeout error with large negative; duplicate with another value | `pkg/consistency/errors_test.go:TestInvalidTimeoutErrorMessage` |
| `pkg/consistency/errors_test.go:TestInvalidTimeoutErrorImplementsError` | `InvalidTimeoutError` satisfies `error`; tautology (compile-time assignment) | `pkg/consistency/errors_test.go:TestInvalidTimeoutErrorMessage` |
| `pkg/embedding/errors_test.go:TestInvalidInputError_ImplementsError` | `InvalidInputError` satisfies `error`; tautology (no assertion) | `pkg/embedding/errors_test.go:TestInvalidInputError_Error` |
| `pkg/embedding/errors_test.go:TestEmptyResponseError_ImplementsError` | `EmptyResponseError` satisfies `error`; tautology (no assertion) | `pkg/embedding/errors_test.go:TestEmptyResponseError_Error` |
| `pkg/embedding/errors_test.go:TestRetryExhaustedError_As` | `errors.As` matches `*RetryExhaustedError`; tests errors.As on the concrete pointer (stdlib) | `pkg/embedding/errors_test.go:TestRetryExhaustedError_Error` |
| `pkg/embedding/errors_test.go:TestHTTPError_As` | `errors.As` matches `*HTTPError`; tests errors.As on the concrete pointer (stdlib) | `pkg/embedding/errors_test.go:TestHTTPError_Error` |
| `pkg/embedding/errors_test.go:TestCacheCapacityError_Zero` | `CacheCapacityError`: zero; duplicate with another value | `pkg/embedding/errors_test.go:TestCacheCapacityError_Error` |
| `pkg/embedding/errors_test.go:TestProviderAlreadyRegisteredError_As` | `errors.As` matches `*ProviderAlreadyRegisteredError`; tests errors.As on the concrete pointer (stdlib) | `pkg/embedding/errors_test.go:TestProviderAlreadyRegisteredError_Error` |
| `pkg/embedding/errors_test.go:TestProviderNotFoundError_As` | `errors.As` matches `*ProviderNotFoundError`; tests errors.As on the concrete pointer (stdlib) | `pkg/embedding/errors_test.go:TestProviderNotFoundError_Error` |
| `pkg/lease/errors_test.go:TestNotFoundErrorWithLargeID` | not found error with large id; duplicate with another ID | `pkg/lease/errors_test.go:TestNotFoundErrorMessage` |
| `pkg/lease/errors_test.go:TestNotFoundErrorImplementsError` | `NotFoundError` satisfies `error`; tautology (compile-time assignment) | `pkg/lease/errors_test.go:TestNotFoundErrorMessage` |
| `pkg/lease/errors_test.go:TestExistsErrorWithDifferentID` | exists error with different id; duplicate with another ID | `pkg/lease/errors_test.go:TestExistsErrorMessage` |
| `pkg/lease/errors_test.go:TestExistsErrorImplementsError` | `ExistsError` satisfies `error`; tautology (compile-time assignment) | `pkg/lease/errors_test.go:TestExistsErrorMessage` |
| `pkg/lease/errors_test.go:TestInvalidTTLErrorWithZero` | invalid ttlerror with zero; weaker duplicate (contains "0") | `pkg/lease/errors_test.go:TestInvalidTTLErrorMessage` |
| `pkg/lease/errors_test.go:TestInvalidTTLErrorImplementsError` | `InvalidTTLError` satisfies `error`; tautology (compile-time assignment) | `pkg/lease/errors_test.go:TestInvalidTTLErrorMessage` |
| `pkg/lease/errors_test.go:TestLessorClosedErrorImplementsError` | `LessorClosedError` satisfies `error`; tautology (compile-time assignment) | `pkg/lease/errors_test.go:TestLessorClosedErrorMessage` |
| `pkg/lock/errors_test.go:TestSentinelErrors_NonNil` | `SentinelErrors`: non nil; tautology (errors.New package vars) | `pkg/lock/errors_test.go:TestSentinelErrors_DistinctMessages` |
| `pkg/lock/errors_test.go:TestLockHeldError_ImplementsError` | `LockHeldError` satisfies `error`; tautology (compile-time assignment) | `pkg/lock/errors_test.go:TestLockHeldError_ContainsKey` |
| `pkg/lock/errors_test.go:TestNotLockHolderError_ImplementsError` | `NotLockHolderError` satisfies `error`; tautology (compile-time assignment) | `pkg/lock/errors_test.go:TestNotLockHolderError_ContainsKey` |
| `pkg/lock/errors_test.go:TestLockNotFoundError_ImplementsError` | `LockNotFoundError` satisfies `error`; tautology (compile-time assignment) | `pkg/lock/errors_test.go:TestLockNotFoundError_ContainsKey` |
| `pkg/lock/errors_test.go:TestLockNotFoundError_EmptyKey` | `LockNotFoundError`: empty key; weaker duplicate (non-empty message) | `pkg/lock/errors_test.go:TestLockNotFoundError_ContainsPrefix` |
| `pkg/messaging/errors_test.go:TestDeltaNilInputError_ImplementsErrorInterface` | `DeltaNilInputError` satisfies `error`; tautology (non-empty message) | `pkg/messaging/errors_test.go:TestDeltaNilInputError_Error_IncludesParameter` |
| `pkg/messaging/errors_test.go:TestDeltaTypeMismatchError_ImplementsErrorInterface` | `DeltaTypeMismatchError` satisfies `error`; tautology (non-empty message) | `pkg/messaging/errors_test.go:TestDeltaTypeMismatchError_Error_WithPath` |
| `pkg/messaging/errors_test.go:TestDeltaMaxDepthExceededError_ImplementsErrorInterface` | `DeltaMaxDepthExceededError` satisfies `error`; tautology (non-empty message) | `pkg/messaging/errors_test.go:TestDeltaMaxDepthExceededError_Error_IncludesPath` |
| `pkg/messaging/errors_test.go:TestDeltaDeserializationError_ImplementsErrorInterface` | `DeltaDeserializationError` satisfies `error`; tautology (non-empty message) | `pkg/messaging/errors_test.go:TestDeltaDeserializationError_Error_IncludesCause` |
| `pkg/messaging/errors_test.go:TestDeltaDiffError_ImplementsErrorInterface` | `DeltaDiffError` satisfies `error`; tautology (non-empty message) | `pkg/messaging/errors_test.go:TestDeltaDiffError_Error_IncludesCause` |
| `pkg/quota/errors_test.go:TestExceededErrorImplementsError` | `ExceededError` satisfies `error`; tautology (compile-time assignment) | `pkg/quota/errors_test.go:TestExceededErrorMessageContainsTenantID` |
| `pkg/quota/errors_test.go:TestInvalidResourceErrorImplementsError` | `InvalidResourceError` satisfies `error`; tautology (compile-time assignment) | `pkg/quota/errors_test.go:TestInvalidResourceErrorMessageContainsResourceID` |
| `pkg/quota/errors_test.go:TestUnregisteredCounterErrorImplementsError` | `UnregisteredCounterError` satisfies `error`; tautology (compile-time assignment) | `pkg/quota/errors_test.go:TestUnregisteredCounterErrorMessageContainsResource` |
| `pkg/quota/errors_test.go:TestErrNilCheckerIsNotNil` | err nil checker is not nil; tautology (errors.New package var) | `pkg/quota/errors_test.go:TestErrNilCheckerMessageContainsQuota` |
| `pkg/seal/barrier_test.go:TestBarrier_Unseal_Success` | `Barrier`: unseal, success; subset of the seal/unseal round trip (its sealed/unsealed asserts moved there) | `pkg/seal/barrier_test.go:TestBarrier_UnsealThenDataRoundTrip` |
| `pkg/seal/barrier_test.go:TestBarrier_Seal_Success` | `Barrier`: seal, success; init, seal, sealed-gate path repeated | `pkg/seal/barrier_test.go:TestBarrier_Seal_ZeroesDEK`, `pkg/seal/barrier_test.go:TestBarrier_Get_Sealed` |
| `pkg/seal/barrier_test.go:TestBarrier_PutGet_RoundTrip` | `Barrier`: put get, round trip; Put/Get path repeated by the seal/unseal round trip | `pkg/seal/barrier_test.go:TestBarrier_UnsealThenDataRoundTrip` |
| `pkg/service/entity_errors_test.go:TestInvalidEntityOpTypeError_ImplementsError` | `InvalidEntityOpTypeError` satisfies `error`; tautology (compile-time assignment) | `pkg/service/entity_errors_test.go:TestInvalidEntityOpTypeError_Message` |
| `pkg/service/entity_errors_test.go:TestBatchEntityEmptyOpsError_ImplementsError` | `BatchEntityEmptyOpsError` satisfies `error`; tautology (compile-time assignment) | `pkg/service/entity_errors_test.go:TestBatchEntityEmptyOpsError_Message` |
| `pkg/service/vector_errors_test.go:TestVectorNotFoundErrorImplementsError` | `VectorNotFoundError` satisfies `error`; tests errors.As on the concrete pointer (stdlib) | `pkg/service/vector_errors_test.go:TestVectorNotFoundError` |
| `pkg/service/vector_errors_test.go:TestNamespaceNotFoundErrorImplementsError` | `NamespaceNotFoundError` satisfies `error`; tests errors.As on the concrete pointer (stdlib) | `pkg/service/vector_errors_test.go:TestNamespaceNotFoundError` |
| `pkg/service/vector_errors_test.go:TestNamespaceExistsErrorImplementsError` | `NamespaceExistsError` satisfies `error`; tests errors.As on the concrete pointer (stdlib) | `pkg/service/vector_errors_test.go:TestNamespaceExistsError` |
| `pkg/service/vector_errors_test.go:TestDimensionMismatchErrorImplementsError` | `DimensionMismatchError` satisfies `error`; tests errors.As on the concrete pointer (stdlib) | `pkg/service/vector_errors_test.go:TestDimensionMismatchError` |
| `pkg/service/vector_errors_test.go:TestInvalidVectorErrorImplementsError` | `InvalidVectorError` satisfies `error`; tests errors.As on the concrete pointer (stdlib) | `pkg/service/vector_errors_test.go:TestInvalidVectorError` |
| `pkg/service/vector_errors_test.go:TestInvalidNamespaceNameErrorImplementsError` | `InvalidNamespaceNameError` satisfies `error`; tests errors.As on the concrete pointer (stdlib) | `pkg/service/vector_errors_test.go:TestInvalidNamespaceNameError` |
| `pkg/shard/errors_test.go:TestShardAlreadyRegisteredErrorMessage` | shard already registered error message; substring checks of the exact message | `pkg/shard/errors_test.go:TestErrorMessagePrefixes/ShardAlreadyRegistered` |
| `pkg/shard/errors_test.go:TestShardAlreadyRegisteredErrorImplementsError` | `ShardAlreadyRegisteredError` satisfies `error`; tautology (compile-time assignment) | `pkg/shard/errors_test.go:TestErrorMessagePrefixes` |
| `pkg/shard/errors_test.go:TestShardNotRegisteredErrorMessage` | shard not registered error message; substring checks of the exact message | `pkg/shard/errors_test.go:TestErrorMessagePrefixes/ShardNotRegistered` |
| `pkg/shard/errors_test.go:TestShardNotRegisteredErrorImplementsError` | `ShardNotRegisteredError` satisfies `error`; tautology (compile-time assignment) | `pkg/shard/errors_test.go:TestErrorMessagePrefixes` |
| `pkg/shard/errors_test.go:TestInvalidShardIDErrorMessage` | invalid shard iderror message; substring checks of the exact message | `pkg/shard/errors_test.go:TestErrorMessagePrefixes/InvalidShardID` |
| `pkg/shard/errors_test.go:TestInvalidShardIDErrorImplementsError` | `InvalidShardIDError` satisfies `error`; tautology (compile-time assignment) | `pkg/shard/errors_test.go:TestErrorMessagePrefixes` |
| `pkg/sql/engine/errors_test.go:TestUnsupportedSyntaxError_ImplementsError` | `UnsupportedSyntaxError` satisfies `error`; weaker duplicate of the message check | `pkg/sql/engine/errors_test.go:TestUnsupportedSyntaxError_Error` |
| `pkg/watch/errors_test.go:TestHubClosedError_ImplementsErrorInterface` | `HubClosedError` satisfies `error`; tautology (non-empty message) | `pkg/watch/errors_test.go:TestHubClosedError_Error_ReturnsExpectedMessage` |
| `pkg/watch/errors_test.go:TestHubClosedError_ErrorsAs` | `errors.As` matches `*HubClosedError`; tests errors.As on the concrete pointer (stdlib) | `pkg/watch/errors_test.go:TestHubClosedError_Error_ReturnsExpectedMessage` |
| `pkg/watch/errors_test.go:TestHubClosedError_NotNil` | `HubClosedError`: not nil; tautology (compile-time assignment) | `pkg/watch/errors_test.go:TestHubClosedError_Error_ReturnsExpectedMessage` |
| `pkg/shard/errors_test.go:TestShardAlreadyRegisteredErrorWithLargeID` | shard already registered error with large id; duplicate with another ID (substring check of the exact message) | `pkg/shard/errors_test.go:TestErrorMessagePrefixes/ShardAlreadyRegistered` |
| `pkg/shard/errors_test.go:TestShardNotRegisteredErrorWithZeroID` | shard not registered error with zero id; duplicate with another ID (substring check of the exact message) | `pkg/shard/errors_test.go:TestErrorMessagePrefixes/ShardNotRegistered` |
| `pkg/shard/errors_test.go:TestInvalidShardIDErrorWithLargeID` | invalid shard iderror with large id; duplicate with another ID (substring check of the exact message) | `pkg/shard/errors_test.go:TestErrorMessagePrefixes/InvalidShardID` |

## Trimmed tests

| Package | Test | Change | Test that pins the path |
|---|---|---|---|
| pkg/tenant | TestCreateTenant_ConcurrentCreateAndDeleteNeverResurrect | 50 to 10 iterations; stays for the race detector | TestCreateTenant_RefusedWhenADeleteRetiresTheIdMidCreate (deterministic interleave) |
| pkg/server | TestBuildDataPlaneServices_ImportSession_LeaseExpiryReclaims | wall-clock wait replaced by a fake clock (the Lessor's clock advances past the TTL) | itself |
| pkg/server/lifecycle | TestStartLocalLeaseExpiry_StopWaitsAndStaysStopped | wall-clock wait replaced by a fake clock (BlockUntil before stop, no waiters after, Advance past TTL and sweeps) | itself |
| pkg/server | TestRunStartupRead_ContextEnds | wall-clock wait replaced by a channel-free order: the read func cancels on its first call | itself |
| pkg/server | TestRunStartupRead_ContextEndsMidAttempt | wall-clock wait replaced by a channel-free order: the read func cancels while in flight | itself |
| pkg/server | TestRunStartupRead_ReportsAnAttemptThatOutlivesItsBound | wall-clock wait replaced by the attempt returning once the log holds its second progress line (bounded deadline) | itself |
| pkg/server/lifecycle | TestAwaitClusterCACertificate_CtxCanceledDuringBackoff | wall-clock wait replaced by the provider's first Certificate call canceling ctx before the backoff | itself |
| pkg/service | TestHostShardSnapshotProvider_SnapshotShard_ContextCanceledDuringPoll | wall-clock wait replaced by the first host poll callback canceling ctx | itself |
| pkg/rowlock | TestSession_LeaseInUseIsNotIdle | wall-clock wait replaced by a fake clock (half-TTL steps, each after a renewal, to two TTLs past the grant) | itself |
| pkg/api/connlimit | TestQUICListenerReleasesSlotOnConnectionClose | wall-clock wait replaced by the server conn's context ending and the counter emptying (bounded deadline) | itself |
| pkg/index | TestExtendedMemoryIndex_ContextCancellation | wall-clock wait replaced by waiting on the timed-out context's Done | itself |
| pkg/embedding | TestEmbed_CancelledContext | wall-clock wait replaced by the handler returning when the request context ends | itself |
| pkg/service | TestVectorService_GetRebuildProgress_EstimatedTimeRemaining | wall-clock wait removed (the only assertion, ElapsedTimeMs >= 0, needs no elapsed time) | itself |
| pkg/service | TestHardConfinedWatch_WatchEntityTypeFilterGoroutineExitsOnContextDone | wall-clock wait replaced by the forwarder draining the capacity-1 event channel before cancel (bounded deadline) | itself |
| cmd/qrdbd | TestCertReloader_FileWatch_DebounceReloadError | wall-clock wait replaced by a recording slog handler receiving "debounced reload failed" (bounded deadline) | itself |
| cmd/qrdbd | TestCertReloader_FileWatch_WatcherError | wall-clock wait replaced by a recording slog handler receiving "filesystem watcher error" (bounded deadline) | itself |
| pkg/rowlock | TestKeepalive_RevokedLease_MarksTheOwnerLost | wall-clock wait loop replaced by require.Eventually on the lease's lost flag | itself |
| pkg/rowlock | waitWaiting (helper) | wall-clock wait loop replaced by require.Eventuallyf on the waiter gauge | its callers |
| pkg/rowlock | waitQueued (helper) | wall-clock wait loop replaced by require.Eventuallyf on the queue depth | its callers |
| pkg/sql/internal/sessionguard | closeCounter / waitClosed (helpers) | wall-clock wait loop replaced by a channel closed on the first close (bounded deadline) | their callers |
| pkg/firstboot | TestWatchForAdminBound_RealHubObservesAddCertBinding | wall-clock wait loop replaced by require.Eventually on the hub's watch count | itself |
| pkg/firstboot | TestWatchForAdminBound_ContextCanceledReturnsNil | wall-clock wait loop replaced by require.Eventually on the hub's watch count | itself |
| pkg/firstboot | TestWatchForAdminBound_TransientLookupFailureRetries | wall-clock wait loop replaced by require.Eventually on the hub's watch count | itself |
| pkg/firstboot | TestThreeNodeCluster_FollowersLeaveAwaitingViaWatch_NoRestart | wall-clock wait loop replaced by require.Eventually on the hub's watch count reaching 3 | itself |
| pkg/firstboot | TestThreeNodeCluster_LeaderChange_RemainingNodesLeaveAwaitingViaWatch | wall-clock wait loop replaced by require.Eventually on the hub's watch count reaching 3 | itself |
| pkg/firstboot | awaitNotAwaiting (helper) | wall-clock wait loop replaced by require.Eventuallyf on the probe leaving awaiting | its callers |
| pkg/server | waitForCounts (helper) | wall-clock wait loop replaced by assert.Eventually on every transport's open/close counts | its callers |
| pkg/server | TestModuleConnObserver_SeesEveryListener | two wall-clock wait loops (raw TLS dial balance, QUIC/HTTP3 balance) replaced by assert.Eventually on the per-transport counts | itself |
| pkg/api/connlimit | waitForAccepted (helper) | wall-clock wait loop replaced by require.Eventuallyf on the accepted-connection count | its callers |

## Folded refs

| Ref | Fold | Result |
|---|---|---|
| fold-src/sealed-ceremony-ca-row-1 | merged (--no-ff) | adds sdk/go/node/sealed_ceremony_ca_test.go and docs/plans/1.md, updates sealed_window_mount_test.go; merged cleanly against current main, Sealed tests in sdk/go/node pass |
| fold-src/sealed-ceremony-ca-wip | merged with -s ours | superseded by the sealed-ceremony-ca row-1 ref (same test file; its go.sum change is noise) |
| fold-src/ci-bigdisk-docker | merged with -s ours | superseded by ruling 617 (#1582, ci.yml jobs on the self-hosted pools) |

## Sleep sweep 2 (rw566e)

Package order 4: cmd/qrdbd, pkg/volume, pkg/clienttrust and sdk/go test files the other 566 lanes do not touch. Line numbers are the launch base's.

| File | Line | Replaced by / kept and why |
|---|---|---|
| cmd/qrdbd/cert_reload_test.go | 344 | require.Eventually on the loaded certificate changing, then the content assert |
| cmd/qrdbd/cert_reload_test.go | 365 | assert.Never over three debounce intervals: an unrelated file write never swaps the certificate |
| cmd/qrdbd/cert_reload_test.go | 478 | assert.Never over three debounce intervals: no reload after Stop |
| cmd/qrdbd/cert_reload_test.go | 592 | require.Eventually on the loaded certificate changing (key in a separate directory) |
| cmd/qrdbd/unseal_ceremony_test.go | 467, 476 | ticker wait between retries of the deadline-bounded initialize poll; superseded at the merge by sweep 4's require.EventuallyWithTf |
| cmd/qrdbd/reclaim_sweep_test.go | 116, 140 | require.Eventually on the resolver having recorded a sweep |
| pkg/volume/reporter_seal_test.go | 168, 175, 202 | kept: synctest fake-clock advance; the report interval is the thing under test, synctest.Wait follows |
| pkg/clienttrust/pool_test.go | 1285, 1449, 2440 | kept in sweep 2: already deadline-bounded polls with t.Fatal on timeout; replaced in sweep 6 by testify polls |
| sdk/go/transport/embedded/transport_watch_test.go | 106, 121 | ticker wait in the deadline-bounded goroutine-count helpers (no testify: Eventually's goroutine would skew the count) |
| sdk/go/transport/embedded/transport_watch_test.go | 286 | ticker wait in awaitEviction's deadline-bounded poll on the hub's watcher count |
| sdk/go/transport/coalescing_test.go | 261, 273 | kept: synctest fake-clock advance to either side of the batch window, which is the thing under test |
| sdk/go/node/tenant_undo_watch_test.go | 42, 101 | ticker wait in the deadline-bounded feature-gate and undo-acknowledgement polls |
| sdk/go/node/cluster_formation_test.go | 219, 610, 688, 726, 1246 | ticker wait in the deadline-bounded leader, gate-settle, joiner-read, admit and lease-reap polls (bodies call t.Fatalf, so not Eventually) |
| sdk/go/coordination_test.go | 1035 | kept: positions the renewal at the halfway mark of the 4 s lease lifetime; the wall-clock deadline is the thing under test |
| sdk/go/coordination_test.go | 1046 | ticker wait in the deadline-bounded poll for the keepalive reaching the fake |

## Sleep sweep 3 (rw566f)

Package order 3 (pkg/server, pkg/server/lifecycle) and 4 (pkg/sql/mysql): every
time.Sleep in the files below either became a bounded testify poll on the same
condition, or stays because the sleep is what the test measures. Helpers that
return their last observation (and let the caller judge it) poll with
assert.Eventually and a quiet reporter, so the deadline does not add a second
failure.

| File | Line | Replaced by / kept and why |
|---|---|---|
| pkg/server/namespaced_watch_leak_test.go | 76 | settledNamespacedGoroutines: assert.Eventually on two equal consecutive goroutine counts (5s, 10ms), last sample returned |
| pkg/server/namespaced_watch_leak_test.go | 91 | namespacedGoroutinesAtMost: assert.Eventually on the count falling to the bound, last sample returned to the leak assertion |
| pkg/server/namespaced_watch_terminal_test.go | 79 | awaitHubEviction: require.Eventually on hub.WatchCount() == 0 (30s) |
| pkg/server/prebound_quic_test.go | 112 | kept: paces the test's own dials across the stale-timer window, which is the behaviour under test |
| pkg/server/raft_backend_factory_identity_test.go | 185 | require.EventuallyWithT on some founder accepting a write (raftharness.Bound), last Put error reported |
| pkg/server/raft_backend_factory_lock_test.go | 235 | awaitStarting: require.Eventuallyf on the start being in f.starting |
| pkg/server/raft_backend_factory_lock_test.go | 521 | require.Eventually on the seal listing the barrier in f.sealing (5s) |
| pkg/server/raft_backend_factory_test.go | 317 | writeAndAwaitLogRange: assert.Eventually on FirstIndex > 1 or a LogRange error, last range returned |
| pkg/server/raft_backend_factory_test.go | 617 | RestoreShardOnline retry: retryWhileReadIndexNotReady |
| pkg/server/raft_backend_migrate_test.go | 49 | getWhenReadable: new shared retryWhileReadIndexNotReady (assert.Eventually, 15s, 200ms, attempts serialised, last error returned) |
| pkg/server/raft_promoter_identity_test.go | 72 | require.Eventually on observer 2 in the follower's membership (raftharness.Bound) |
| pkg/server/served_watch_test.go | 138 | drained: require.Eventually on len(ch) == 0 (5s) |
| pkg/server/served_watch_transport_test.go | 193 | waitFor: require.Eventually on cond (5s) |
| pkg/server/server_etcd_guard_test.go | 167 | require.Eventually on streams.Len() == 0 (5s) |
| pkg/server/tenant_shard_reconciler_test.go | 331 | awaitCondition: assert.Eventually on cond (reconcileConvergeBound), bool to the caller |
| pkg/server/system_shard_settling_test.go | 196, 198, 248 | kept: inside synctest.Test, the sleeps advance the fake clock |
| pkg/server/lifecycle/trust_anchors_test.go | 245 | waitTrusted: require.Eventuallyf on VerifyPeerChain succeeding |
| pkg/sql/mysql/idle_timeout_wire_test.go | 107 | kept: the active connection's traffic below the idle bound is what the test proves keeps it open |
| pkg/sql/mysql/idle_timeout_wire_test.go | 142 | require.EventuallyWithT on the NOWAIT lock succeeding, last error reported |
| pkg/sql/mysql/row_lock_wire_test.go | 186 | require.EventuallyWithT on the NOWAIT lock succeeding, last error reported |
| pkg/sql/mysql/sealed_cursor_wire_test.go | 258 | require.EventuallyWithT on a fetch failing, then requireSealedEnd on that error |
| pkg/sql/mysql/sealed_session_wire_test.go | 105 | require.EventuallyWithT on the ping reading as a closed connection |
| pkg/sql/mysql/sealed_session_wire_test.go | 162 | assert.Eventually on the statement being refused; rollback then FailNow on timeout |

## Sleep sweep 4 (rw566g)

Package order 4 (cmd/qrdbd): every time.Sleep left in the cmd/qrdbd test files
below either became a bounded testify poll on the same condition, or stays
because the elapsed time is what the test proves or the test can reach no
observable ready state without a production change. Line numbers are those
before the sweep. The tip's lint finding is fixed in the same slice:
pkg/rowlock/harness_test.go imports are grouped as goimports with the
go-qrdb local prefix writes them (testify in its own group).

| File | Line | Replaced by / kept and why |
|---|---|---|
| cmd/qrdbd/cert_reload_test.go | 345 | TestCertReloader_FileWatch: require.Eventuallyf on r.cert.Load() != origLoaded (2s, 20ms), certificate equality asserted after |
| cmd/qrdbd/cert_reload_test.go | 366 | sweep 2's assert.Never over three debounce intervals (no reload from an unrelated file) replaces the sleep kept here, at the merge of the two sweeps |
| cmd/qrdbd/cert_reload_test.go | 479 | sweep 2's assert.Never over three debounce intervals (no reload after Stop) replaces the sleep kept here, at the merge of the two sweeps |
| cmd/qrdbd/cert_reload_test.go | 593 | TestCertReloader_FileWatch_KeyDirChange: require.Eventuallyf on r.cert.Load() != origLoaded (2s, 20ms), certificate equality asserted after |
| cmd/qrdbd/cluster_migrate_test.go | 503 | require.Eventuallyf on GetMigration returning nil (5s, 20ms) |
| cmd/qrdbd/coverage_target_test.go | 150 | kept: the sealed-window listener binds port 0 and awaitUnseal exposes no address, hook or state for its first tick, so the test has no observable ready state without a production change |
| cmd/qrdbd/fullstart_integration_test.go | 187 | kept: a poll on the admin port accepting never succeeded within 15s (the daemon logs no "starting sealed-window listener" before the SIGTERM), so no observable ready state is reachable; the 2s window stays |
| cmd/qrdbd/holder_audit_test.go | 256 | require.Eventuallyf on svc.HolderDeps().Audit != nil (readinessTimeout, 10ms) |
| cmd/qrdbd/isolation_floor_test.go | 153 | require.Eventuallyf on the strict floor being applied (5s, 5ms) |
| cmd/qrdbd/noise_port_unix_test.go | 51 | kept: 4 retry intervals so the guard must fall back on polling; the elapsed window is what the test proves |
| cmd/qrdbd/reclaim_sweep_test.go | 117 | require.Eventuallyf on len(resolver.sweeps()) > 0 (5s, 1ms) |
| cmd/qrdbd/reclaim_sweep_test.go | 141 | require.Eventuallyf on len(resolver.sweeps()) > 0 (5s, 1ms) |
| cmd/qrdbd/reset_signal_race_test.go | 108 | kept: lets an in-flight SIGTERM land before the helper process exits; the window is what the race test proves |
| cmd/qrdbd/signal_dispatch_test.go | 95 | kept: 2 retry intervals after stop prove no later signal outlives the daemon |
| cmd/qrdbd/unseal_ceremony_test.go | 468, 477 | initializeCeremony: require.EventuallyWithTf over the same POST (readinessTimeout, signalRetryInterval), last error or status reported |

## Sleep sweep 5 (rw566h)

Package order 5 (pkg/service, pkg/volume, pkg/credentials): every time.Sleep
left in the test files below either became a bounded testify poll on the same
condition, or stays because the elapsed time is what the test proves. Line
numbers are those before the sweep. Helpers that return a value capture it
inside the poll condition and return it after; signatures and callers are
unchanged.

| File | Line | Replaced by / kept and why |
|---|---|---|
| pkg/service/batch_import_session_leader_test.go | 98 | waitForLeaderExcept: require.Eventuallyf on a host reporting a known, non-excluded leader (raftharness.Bound, 20ms), host index and replica id captured in the condition |
| pkg/service/fsync_stall_leadership_test.go | 142 | (*fsyncStallCluster).leader: require.Eventuallyf on agreedLeader() != 0 (raftharness.Bound, 10ms), id and term captured in the condition |
| pkg/service/invitation_test.go | 461 | kept: the token's expiry is judged by the invitation store's Lookup against the wall clock; InvitationDeps.Now only feeds bring-your-own certificate verification, so 1ms past a 1ns TTL is the only way to reach an expired token without a production change |
| pkg/service/reclaim_job_test.go | 786 | kept: paces the SetStoreResolver churn against Run for the 100ms context; the interleaving is what -race proves, there is no condition to wait on |
| pkg/service/watch_entity_terminal_test.go | 67 | awaitEviction: require.EventuallyWithT on hub.WatchCount() == 0 (30s, 1ms), the last count reported |
| pkg/volume/reporter_seal_test.go | 169, 176 | kept: inside synctest.Test the sleeps advance the fake clock past the report intervals the test counts refreshes over |
| pkg/volume/reporter_seal_test.go | 203 | kept: inside synctest.Test the sleep advances the fake clock two report intervals |
| pkg/volume/reporter_test.go | 261 | TestReporter_StartRetriesAFailedInitialReport: require.EventuallyWithT over the same mvcc.Get (5s, 5ms), the last raw and err reported |
| pkg/credentials/retrieve_buffer_test.go | 110 | require.EventuallyWithT on len(svc.waiters["late"]) == waiters read under waitersMu (5s, 1ms), the last count reported |
| pkg/credentials/waiter_cancel_test.go | 87 | waitUntil deleted; its one caller in TestRetrieveManual_CancelDeregistersWaiter is require.Eventually on the same condition (2s, 1ms) with the same failure message |

## Sleep sweep 6 (rw566i)

Package order 6 (pkg/clienttrust, pkg/cert, pkg/cluster, pkg/cluster/featuregate,
pkg/clusterenroll, pkg/replicaforward, pkg/database, pkg/engine, pkg/store,
pkg/sql/internal/sessionguard, pkg/bootstrap): every deadline-loop time.Sleep
poll in the test files below became a bounded testify poll on the same
condition with the same bound and tick, the last error or observation
reported. Line numbers are those before the sweep. Helpers keep their
signature and callers; a value found inside the condition is captured and
used after. The sleeps named at the end of the table stay, with the reason.

| File | Line | Replaced by / kept and why |
|---|---|---|
| pkg/clienttrust/pool_test.go | 1286 | require.Eventually on atomic.LoadInt32(&prov.blockingCalls) != 0 (2s, 1ms), same failure message |
| pkg/clienttrust/pool_test.go | 1450 | require.Eventually on atomic.LoadInt32(&prov.blockingCalls) != 0 (2s, 1ms), same failure message |
| pkg/clienttrust/pool_test.go | 2441 | TestPool_Run_RebuildsOnInvalidate: require.EventuallyWithT on VerifyPeerChain returning nil (poolCacheTTL/2, 5ms), the last error reported; cancel() deferred so a failed poll still stops Run |
| pkg/cert/watch_eviction_test.go | 188 | pollUntil: require.Eventually(check, 2s, 1ms) with the same message |
| pkg/cluster/featuregate/featuregate_test.go | 262 | eventually: require.Eventually(cond, 2s, 5ms, msg) |
| pkg/cluster/membership_test.go | 715 | require.EventuallyWithT on store.putCallCount() >= 3 (2s, 5ms), the last count reported |
| pkg/clusterenroll/join_test.go | 411 | require.Eventually on ListPendingJoinRequests returning one entry (5s, 10ms), requestID captured; a list error ends the poll at once and fails on require.NoError |
| pkg/replicaforward/invalidate_test.go | 54 | eventually: require.Eventually(cond, 5s, 5ms, msg) |
| pkg/database/backend_standalone_multiprocess_test.go | 109 | require.EventuallyWithT on os.Stat(startFile) succeeding (30s, 1ms), the last error reported |
| pkg/engine/mvcc_engine_test.go | 1354 | require.EventuallyWithT on Lookup succeeding (5s, 1ms), the last error reported; the result is captured and the lease ID asserted once after |
| pkg/store/coverage_test.go | 1302 | require.EventuallyWithT on Lookup succeeding (5s, 1ms), the last error reported; the lease ID asserted once after |
| pkg/store/pebble_extended_test.go | 134 | require.EventuallyWithT on Lookup succeeding (5s, 1ms), the last error reported; the lease ID asserted once after |
| pkg/sql/internal/sessionguard/sessionguard_test.go | 121 | TestWatchAdmission_SealedSkipsTheAdmission: require.EventuallyWithT on byAdmission and byProbe >= 3 (5s, 1ms), both last counts reported; g.Stop() (idempotent) deferred before the poll |
| pkg/bootstrap/concurrent_role_seed_test.go | 344 | waitShardReadableWithin: one require.EventuallyWithT over every host's SyncGetShardMembership (same bound, 10ms), each attempt keeps its shardReadyPollAttempt timeout, a host that answered is not asked again, the last error reported |
| pkg/sql/internal/sessionguard/sessionguard_test.go | 180, 206 | kept: a fixed 20ms window after the stream ends proves no further callback fires; there is no condition to wait on |
| pkg/backend/group_commit_test.go | 70 | kept: injected store latency that lets commits group, not a wait |
| pkg/trustanchor/kv_atomic_test.go | 43 | kept: injected store latency, not a wait |
| pkg/statemachine/raft_keylength_test.go | 86 | kept in this sweep: backoff between proposals while the raft cluster settles; left to a pkg/statemachine sweep |
| pkg/statemachine/replica_storage_cluster_test.go | 109 | kept in this sweep: backoff between attempts while the raft cluster settles; left to a pkg/statemachine sweep |
| pkg/api/*, pkg/sql/mysql, pkg/sql/pgsql wire tests | - | kept in this sweep: outside sweep 6's packages (pkg/sql/mysql is in the sweep 3 table) |
| pkg/firstboot, pkg/rowlock | - | kept in this sweep: outside sweep 6's packages (pkg/rowlock is in an earlier sweep table) |
| pkg/watch, pkg/lock, pkg/credentials/versioned_test.go | - | kept: time.Sleep appears only in comments |
