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

## Trimmed tests

| Package | Test | Change | Test that pins the path |
|---|---|---|---|
| pkg/tenant | TestCreateTenant_ConcurrentCreateAndDeleteNeverResurrect | 50 to 10 iterations; stays for the race detector | TestCreateTenant_RefusedWhenADeleteRetiresTheIdMidCreate (deterministic interleave) |
