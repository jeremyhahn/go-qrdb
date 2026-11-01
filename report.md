# #1330 audit — lane tsa1330s

## pkg/errors

Unit-test time (`go test -tags "$(make -s print-build-tags)" -count=1 ./pkg/errors/`): before 0.015s, after 0.008s.
Coverage: before 100.0%, after 100.0%. All four filler files deleted; `make test-pkg PKG=./pkg/errors/...` passes.

| file | test | verdict | reason | covering test |
|---|---|---|---|---|
| coverage_gaps_test.go | TestNewSystemKeyspaceViolationError | moved | only check of the constructor; "empty key" subtest dropped (no branch on the key, same assertions as the main case) | errors_test.go:TestNewSystemKeyspaceViolationError |
| coverage_gaps_test.go | TestNewCrossTenantAccessError | moved | only check; "empty tenants" subtest dropped (no branch) | errors_test.go:TestNewCrossTenantAccessError |
| coverage_gaps_test.go | TestNewMissingTenantContextError | moved | only check; "empty op" subtest dropped (no branch) | errors_test.go:TestNewMissingTenantContextError |
| coverage_gaps_test.go | TestIsSystemKeyspaceViolation | moved | only check of the predicate | errors_test.go:TestIsSystemKeyspaceViolation |
| coverage_gaps_test.go | TestIsCrossTenantAccess | moved | only check of the predicate | errors_test.go:TestIsCrossTenantAccess |
| coverage_gaps_test.go | TestIsMissingTenantContext | moved | only check of the predicate | errors_test.go:TestIsMissingTenantContext |
| coverage_gaps_test.go | TestNotImplementedHelper | moved | only check of helpers.go NotImplemented; subtests merged | helpers_test.go:TestNotImplemented |
| coverage_gaps_test.go | TestErrorCode_String_RemainingCodes | moved | rows merged into the owning table | errors_test.go:TestErrorCode_String |
| coverage_gaps_test.go | TestErrorCode_HTTPStatus_RemainingCodes | moved | rows merged into the owning table | errors_test.go:TestErrorCode_HTTPStatus |
| coverage_gaps_test.go | TestErrorCode_IsRetryable_RateLimited | moved | row merged into the retryable list | errors_test.go:TestErrorCode_IsRetryable |
| coverage_gaps2_test.go | TestIsResourceExhausted_WithMatch | removed | the "resource exhausted error" row fails when the match breaks | errors_test.go:TestIsResourceExhausted |
| coverage_gaps2_test.go | TestIsResourceExhausted_WithNil | removed | duplicate of the "nil error" row | errors_test.go:TestIsResourceExhausted |
| coverage_gaps2_test.go | TestIsResourceExhausted_WithDifferentCode | removed | duplicate of the "different error code" row | errors_test.go:TestIsResourceExhausted |
| coverage_gaps2_test.go | TestIsResourceExhausted_WithStandardError | moved | only check of the non-QRDBError case: added as the "plain error" row | errors_test.go:TestIsResourceExhausted |
| coverage_gaps2_test.go | TestIsNotFound_WithStandardError | moved | only check of the plain-error case; new owning test in errors_test.go (IsNotFound lives in errors.go) | errors_test.go:TestIsNotFound |
| coverage_gaps2_test.go | TestIsCanceled_WithStandardError | moved | as above | errors_test.go:TestIsCanceled |
| coverage_gaps2_test.go | TestIsInvalidInput_WithStandardError | moved | as above | errors_test.go:TestIsInvalidInput |
| coverage_gaps2_test.go | TestWrappedMessageError_Unwrap | moved | only check of Unwrap / errors.Is traversal; merged with _Error | errors_test.go:TestWrappedMessageError |
| coverage_gaps2_test.go | TestWrappedMessageError_Error | moved | only check of Error(); merged | errors_test.go:TestWrappedMessageError |
| coverage_gaps2_test.go | TestCircuitOpen_CreatesCircuitOpenError | moved | only check of CircuitOpen | helpers_test.go:TestCircuitOpen |
| coverage_gaps2_test.go | TestIsCircuitOpen_WithMatch / _WithNil / _WithDifferentCode / _WithStandardError | moved | only checks of IsCircuitOpen; four tests merged into one | helpers_test.go:TestIsCircuitOpen |
| coverage_gaps2_test.go | TestErrorCode_String_MissingCodes | moved | duplicate rows of the table already merged from coverage_gaps_test.go, plus the new ErrCircuitOpen row merged | errors_test.go:TestErrorCode_String |
| coverage_gaps2_test.go | TestErrorCode_HTTPStatus_MissingCodes | moved | duplicate rows, plus ErrCircuitOpen and the unknown-code default (500) merged | errors_test.go:TestErrorCode_HTTPStatus |
| coverage_gaps2_test.go | containsStr, findSubstr | removed | hand-rolled strings.Contains, used only by the removed _Error test | — |
| helpers_coverage_test.go | TestIsNotFound_Helper | removed | same match / nil / other-code assertions, plus plain error, in the owning file (IsNotFound is in errors.go, not helpers.go) | errors_test.go:TestIsNotFound |
| helpers_coverage_test.go | TestIsCanceled_Helper | removed | as above | errors_test.go:TestIsCanceled |
| helpers_coverage_test.go | TestIsInvalidInput_Helper | removed | as above | errors_test.go:TestIsInvalidInput |
| errors_additional_test.go | TestWrapf | moved | only check of helpers.go Wrapf | helpers_test.go:TestWrapf |
| errors_additional_test.go | TestQRDBError_WithRetry | moved | only check of WithRetry (errors.go) | errors_test.go:TestQRDBError_WithRetry |
| errors_additional_test.go | TestQRDBError_GetContext_Nil | moved | only check of the nil-map case; folded into the owning GetContext test | errors_test.go:TestQRDBError_WithContext |
| errors_additional_test.go | TestAs | moved | only check of helpers.go As on non-QRDB and nil errors | helpers_test.go:TestAs |
| errors_additional_test.go | TestGetContext | moved | only check of helpers.go GetContext | helpers_test.go:TestGetContext |
| errors_additional_test.go | TestInternalf | moved | only check | helpers_test.go:TestInternalf |
| errors_additional_test.go | TestUnavailablef | moved | only check | helpers_test.go:TestUnavailablef |
| errors_additional_test.go | TestErrorCode_String_AllCodes | moved | rows merged: the owning table now lists every code once | errors_test.go:TestErrorCode_String |
| errors_additional_test.go | TestErrorCode_HTTPStatus_AllCodes | moved | rows merged, as above | errors_test.go:TestErrorCode_HTTPStatus |
| errors_additional_test.go | TestWrap_PreservesContext | removed | context carried through Wrap is asserted (and non-aliasing too); the wrapper's Op by TestWrap_ReclassifiesAQRDBError | wrap_reclassify_test.go:TestWrap_CarriesContextForwardWithoutAliasing |
| errors_additional_test.go | TestWrap_RetryFollowsTheCallersCode | moved | only check that an inner WithRetry(true) does not govern the wrapper and survives on the inner error; its reverse-direction tail dropped as a duplicate of TestWrap_RetryableCodeSetsRetry | wrap_reclassify_test.go:TestWrap_RetryFollowsTheCallersCode |
| errors_additional_test.go | TestQRDBError_IsWithNilUnderlying | moved | only check of Is with a nil Err: two rows added to the owning table | errors_test.go:TestQRDBError_Is |

## pkg/dao

Unit-test time (`go test -tags "$(make -s print-build-tags)" -count=1 ./pkg/dao/`): before 0.225s. Coverage before 94.6% (baseline 94.60).

| file | test | verdict | reason | covering test |
|---|---|---|---|---|
| dao_coverage_test.go | TestNewCodec_ProtoBufReturnsError | removed | CodecCreationError and the CodecType are asserted for an unavailable codec | codec_common_test.go:TestNewCodecUnsupported |
| dao_coverage_test.go | TestNewCodec_UnsupportedCodecReturnsError | removed | as above | codec_common_test.go:TestNewCodecUnsupported |
| dao_coverage_test.go | TestJsonNumberToUint64_* (4) | moved | only checks; merged into one table in the owning file, with invalid string and invalid json.Number rows | dynamic_entity_test.go:TestJsonNumberToUint64 |
| dao_coverage_test.go | TestGenericDAO_IndexName_WithoutDatabase | removed | the query returns nothing when the "<prefix>.<field>" name is wrong | generic_dao_index_test.go:TestGenericDAO_QueryByIndex_IndexNameFormat |
| dao_coverage_test.go | TestGenericDAO_IndexName_WithDatabase | moved | only check of the database-qualified name | generic_dao_index_test.go:TestGenericDAO_IndexName_WithDatabase |
| dao_coverage_test.go | TestIsNil_NilPointerReturnsTrue | removed | Save/Delete/SaveWithCondition(nil) must return IsInvalidEntity | transaction_test.go:TestTransactionNilEntity |
| dao_coverage_test.go | TestIsNil_NonPointerReturnsFalse | moved | only check of the non-pointer case | transaction_test.go:TestIsNil |
| dao_coverage_test.go | TestEvaluateCondition_CreateOnExistingTimestamped | removed | duplicate | transactional_dao_test.go:TestTransactionWithTimestampedEntity/CreateConditionWithTimestamp |
| dao_coverage_test.go | TestEvaluateCondition_ModOnExistingTimestamped | removed | duplicate; asserted nothing past commit | transactional_dao_test.go:TestTransactionWithTimestampedEntity/ModConditionWithTimestamp |
| dao_coverage_test.go | TestEvaluateCondition_CreateOnNonTimestamped | moved | only check; tautological assertion (`errors.Is(e, e)`) replaced by ConditionFailedError + Reason + record untouched | transaction_test.go:TestTransactionConditionEvaluation/CreateAndModOnNonTimestampedRefused |
| dao_coverage_test.go | TestEvaluateCondition_ModOnNonTimestamped | moved | only check; asserted only "an error"; tightened as above | transaction_test.go:TestTransactionConditionEvaluation/CreateAndModOnNonTimestampedRefused |
| dao_coverage_test.go | TestEvaluateCondition_VersionOnNonVersioned | moved | only check of the go-qrdb#59 refusal | transaction_test.go:TestTransactionConditionEvaluation/VersionOnNonVersionedRefused |
| dao_coverage_test.go | TestEvaluateCondition_VersionOnVersionedStillDecides | removed | a stale expectation (1 against stored 5) must fail, which a substituted constant would pass | transaction_test.go:TestTransactionSaveWithCondition/SaveWithVersionConditionMismatch |
| dao_coverage_test.go | TestEvaluateCondition_NewEntityConditionCreate / _NewEntityConditionMod | moved | only checks that a new entity compares as 0; asserted only a pass, now also that OpEqual 1 is refused and not written | transaction_test.go:TestTransactionConditionEvaluation/CreateAndModOnNewEntityCompareAsZero |
| dao_coverage_test.go | TestEvaluateCondition_ValueOnNonExistentEntity | moved | only tx check (unit_of_work_test.go's is the GenericDAO path); now also OpNotEqual nil refused | transaction_test.go:TestTransactionConditionEvaluation/ValueOnNewEntityComparesAgainstNil |
| dao_coverage_test.go | TestEvaluateCondition_ValueOnExistingEntity | moved | only check of OpNotEqual on stored bytes | transaction_test.go:TestTransactionConditionEvaluation/ValueOperatorsCompareStoredBytes |
| dao_coverage_test.go | TestEvaluateValueCondition_ByteComparisons | moved | asserted nothing (commit error discarded); rewritten with pass and refuse rows for >, <, >=, <= | transaction_test.go:TestTransactionConditionEvaluation/ValueOperatorsCompareStoredBytes |
| dao_coverage_test.go | TestExtractGenericDAO_HappyPath | removed | RegisterSave on a GenericDAO, committed and read back | unit_of_work_test.go:TestUnitOfWork (RegisterSave(uow, userDAO, ...) cases) |
| dao_coverage_test.go | TestExtractGenericDAO_TransactionalDAO | removed | same path, plus commit and read-back | unit_of_work_test.go:TestUnitOfWork_TransactionalDAOExtraction |
| dao_coverage_test.go | TestWithTx_FunctionReturnsError | removed | duplicate: error returned verbatim and not persisted | transactional_dao_test.go:TestWithTx/FailedTransaction |
| codec_additional_test.go | TestSupportedCodecs | moved | only check of the dao.SupportedCodecs wrapper; merged with the variants test into the codec_json-tagged file (codec_common_test.go has no tag, so it cannot assert JSON's presence) | codec_json_test.go:TestSupportedCodecs |
| codec_additional_test.go | TestIsCodecSupportedVariants | moved | only check of IsCodecSupported for unknown and empty types; merged as above | codec_json_test.go:TestSupportedCodecs |
| codec_additional_test.go | TestNewCodecAllSupportedTypes | removed | single JSON row behind a t.Skipf skip guard; Type() and round trip asserted | codec_json_test.go:TestCodecTypeJSON, codec_json_test.go:TestCodecRoundTripJSON |
| codec_additional_test.go | TestCodecWrapperMethods | removed | duplicate Marshal / Unmarshal / Type checks | codec_json_test.go:TestNewCodecJSON, codec_json_test.go:TestCodecRoundTripJSON, codec_json_test.go:TestCodecTypeJSON |
| generic_dao_additional_test.go | TestIsNotFoundErrorVariants | moved | only check of the QRDBError / other-code / plain-error cases; replaced the owning test, whose comment wrongly claimed pkg/errors could not be imported | helpers_test.go:TestIsNotFoundError |

### Remaining (slice ended at turn 190 of 200)

- generic_dao_additional_test.go, still to do:
  - TestNewDAOWithNilStore, TestNewDAOWithEmptyEntityType and TestNewDAOWithNilNewEntity: remove. They are covered by generic_dao_test.go:TestNew/NilStore, /EmptyEntityType and /NilNewEntityFactory, which also assert Field and Reason.
  - TestSaveWithMarshalError (put error), TestGetWithGenericError, TestCountWithScanError, TestPageWithScanError and TestForEachPageWithError: these are the only checks that store errors propagate. Planned: move them into generic_dao_test.go as one table test, tighten to ErrorIs, and rename the file to mock_error_kv_store_test.go so it holds only errorKVStore. page_edge_cases_test.go:TestPageScanErrorAfterCount covers only the scan error that comes after Count.
  - TestPageWithMalformedData: the only check that Page skips malformed entries. Move it and tighten to exactly one entity with ID 1.
- save_deterministic_coverage_test.go and unit_of_work_coverage_test.go: not started.
- Close pkg/dao: `make test-pkg PKG=./pkg/dao/...` and record the after-time. Coverage is now 94.7% against a baseline of 94.60.
- pkg/storage/pebble: pebble_coverage_test.go and pebble_coverage2/3/4_test.go are not started. Before: 25.837s, 96.2%.
- Final checks: `make vet`, `make lint`, `gofmt -l`.
- Unverified: dao_coverage_test.go's TestExtractGenericDAO_HappyPath row cites the unit_of_work_test.go RegisterSave(userDAO) cases (line 57). Confirm the enclosing test name.
