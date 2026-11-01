# #1330 audit, lane tsa1330z5

## pkg/timeseries

Unit-test time before (`go test -tags "$(make -s print-build-tags)" -count=1 -cover ./pkg/timeseries/`, after ts_coverage_test.go): 3.45 s wall, coverage 94.8% (baseline 94.76).

Unit-test time after (same command, storage_coverage_test.go deleted): 0.82 s wall (test binary 0.045 s), coverage 94.8% (baseline 94.76). The build cache was warm for the after figure; the before figure may include compilation, so the wall-clock difference overstates the saving. `make test-pkg PKG=./pkg/timeseries/...`: ok.

### storage_coverage_test.go

| file | test | verdict | reason | covering test |
|---|---|---|---|---|
| storage_coverage_test.go | TestStorage_WritePoint_ValidPoint | moved | only check that WritePoint stores value and tags | storage_test.go:TestStorage_WritePoint_ValidPoint |
| storage_coverage_test.go | TestStorage_WritePoint_EmptyMetric | moved | only check that WritePoint calls DataPoint.Validate | storage_test.go:TestStorage_WritePoint_EmptyMetric |
| storage_coverage_test.go | TestStorage_WritePoint_ZeroTimestamp | removed | WritePoint delegates to Validate: dropping the call fails the empty-metric test, breaking the timestamp rule fails the Validate test | storage_test.go:TestStorage_WritePoint_EmptyMetric, TestDataPoint_Validate_ZeroTimestamp |
| storage_coverage_test.go | TestStorage_WritePoint_AfterClose | moved | only check of WritePoint's own closed guard | storage_test.go:TestStorage_WritePoint_AfterClose |
| storage_coverage_test.go | TestStorage_WritePoint_WithoutTags | removed | the untagged path is writePointInBatch, shared with Write | storage_test.go:TestStorage_Write_WithoutTags |
| storage_coverage_test.go | TestStorage_QueryRich_RawPoints | moved | only check of QueryRich's raw path (metric echoed, no AggregatedValue) | storage_test.go:TestStorage_QueryRich_RawPoints |
| storage_coverage_test.go | TestStorage_QueryRich_WithTagFilter | moved | only check that QueryRich applies req.Tags | storage_test.go:TestStorage_QueryRich_WithTagFilter |
| storage_coverage_test.go | TestStorage_QueryRich_WithAggregation | moved | only check of the scalar aggregation branch | storage_test.go:TestStorage_QueryRich_WithAggregation |
| storage_coverage_test.go | TestStorage_QueryRich_WithDownsampling | moved | only check of the Interval branch | storage_test.go:TestStorage_QueryRich_WithDownsampling |
| storage_coverage_test.go | TestStorage_QueryRich_WithGroupBy | moved | only check of the GroupBy branch | storage_test.go:TestStorage_QueryRich_WithGroupBy |
| storage_coverage_test.go | TestStorage_QueryRich_WithGroupByAndInterval | moved | only check that QueryRich passes Interval to groupAndAggregate | storage_test.go:TestStorage_QueryRich_WithGroupByAndInterval |
| storage_coverage_test.go | TestStorage_QueryRich_WithLimit | moved | only check of QueryRich's limit break | storage_test.go:TestStorage_QueryRich_WithLimit |
| storage_coverage_test.go | TestStorage_QueryRich_EmptyResult | moved | only check that an unknown metric is an empty result, not an error, through QueryRich | storage_test.go:TestStorage_QueryRich_EmptyResult |
| storage_coverage_test.go | TestStorage_QueryRich_InvalidMetric | moved | only check that QueryRich calls QueryRequest.Validate | storage_test.go:TestStorage_QueryRich_InvalidMetric |
| storage_coverage_test.go | TestStorage_QueryRich_EndBeforeStart | removed | dropping the Validate call fails the invalid-metric test; the rule itself has its own test | storage_test.go:TestStorage_QueryRich_InvalidMetric, TestQueryRequest_Validate_EndBeforeStart |
| storage_coverage_test.go | TestStorage_QueryRich_AfterClose | moved | only check of QueryRich's closed guard | storage_test.go:TestStorage_QueryRich_AfterClose |
| storage_coverage_test.go | TestStorage_QueryRich_InvalidAggFunc | removed | as EndBeforeStart | storage_test.go:TestStorage_QueryRich_InvalidMetric, TestQueryRequest_Validate_InvalidAggFunc |
| storage_coverage_test.go | TestStorage_QueryRich_PercentileAggregation | moved | only check that QueryRich passes req.Percentile through | storage_test.go:TestStorage_QueryRich_PercentileAggregation |
| storage_coverage_test.go | TestStorage_QueryRich_PercentileInvalidRange | removed | as EndBeforeStart | storage_test.go:TestStorage_QueryRich_InvalidMetric, TestQueryRequest_Validate_PercentileOutOfRange |
| storage_coverage_test.go | TestStorage_QueryRich_FirstAggregation | moved | only check that AggFirst sees points in time order end to end | storage_test.go:TestStorage_QueryRich_FirstAggregation |
| storage_coverage_test.go | TestStorage_QueryRich_LastAggregation | moved | as FirstAggregation, for AggLast | storage_test.go:TestStorage_QueryRich_LastAggregation |
| storage_coverage_test.go | TestStorage_DeleteSeries_AllPoints | moved | only check that a full delete also drops the metric index | storage_test.go:TestStorage_DeleteSeries_AllPoints |
| storage_coverage_test.go | TestStorage_DeleteSeries_TimeRange | moved, tightened | only check of a bounded delete; now asserts which points survive | storage_test.go:TestStorage_DeleteSeries_TimeRange |
| storage_coverage_test.go | TestStorage_DeleteSeries_WithTagFilter | moved | only check of the tag-filtered delete | storage_test.go:TestStorage_DeleteSeries_WithTagFilter |
| storage_coverage_test.go | TestStorage_DeleteSeries_InvalidMetric | moved | only check that DeleteSeries calls DeleteRequest.Validate | storage_test.go:TestStorage_DeleteSeries_InvalidMetric |
| storage_coverage_test.go | TestStorage_DeleteSeries_EndBeforeStart | removed | dropping the Validate call fails the invalid-metric test; the rule has its own test | storage_test.go:TestStorage_DeleteSeries_InvalidMetric, TestDeleteRequest_Validate_EndBeforeStart |
| storage_coverage_test.go | TestStorage_DeleteSeries_AfterClose | moved | only check of DeleteSeries' closed guard | storage_test.go:TestStorage_DeleteSeries_AfterClose |
| storage_coverage_test.go | TestStorage_DeleteSeries_NonExistentMetric | moved | only check that deleting an absent metric is not an error | storage_test.go:TestStorage_DeleteSeries_NonExistentMetric |
| storage_coverage_test.go | TestStorage_RetentionPolicy_SetAndGet | moved | only Set/Get round trip of all three policy fields | storage_test.go:TestStorage_RetentionPolicy_SetAndGet |
| storage_coverage_test.go | TestStorage_GetRetentionPolicy_Default | moved | only check of the zero policy for an unset metric | storage_test.go:TestStorage_GetRetentionPolicy_Default |
| storage_coverage_test.go | TestStorage_SetRetentionPolicy_Overwrite | moved | only check that a second Set replaces the first | storage_test.go:TestStorage_SetRetentionPolicy_Overwrite |
| storage_coverage_test.go | TestStorage_GetRetentionPolicy_AfterClose | moved | only check of Get's closed guard | storage_test.go:TestStorage_GetRetentionPolicy_AfterClose |
| storage_coverage_test.go | TestStorage_SetRetentionPolicy_AfterClose | moved | only check of Set's closed guard | storage_test.go:TestStorage_SetRetentionPolicy_AfterClose |
| storage_coverage_test.go | TestEncodeDecodeRetentionPolicy_RoundTrip | removed | the codec round trip of every field fails the Set/Get test when broken; the 17-byte length is an internal detail | storage_test.go:TestStorage_RetentionPolicy_SetAndGet |
| storage_coverage_test.go | TestDecodeRetentionPolicy_TooShort | removed | short policy bytes are decoded through GetRetentionPolicy | storage_test.go:TestStorage_GetRetentionPolicy_CorruptData |
| storage_coverage_test.go | TestEncodeRetentionPolicy_ZeroValue | removed | fixed-width little-endian fields have no zero special case; a field-encoding break fails the Set/Get test | storage_test.go:TestStorage_RetentionPolicy_SetAndGet |
| storage_coverage_test.go | TestStorage_ApplyRetention_DeletesPointsOlderThanCutoff | moved | only check that expired points go and later ones stay | storage_test.go:TestStorage_ApplyRetention_DeletesPointsOlderThanCutoff |
| storage_coverage_test.go | TestStorage_ApplyRetention_CutoffIsExclusive | moved | only check of the cutoff boundary | storage_test.go:TestStorage_ApplyRetention_CutoffIsExclusive |
| storage_coverage_test.go | TestStorage_ApplyRetention_NoExpiredPoints | removed | points after the cutoff survive in the cutoff test, which also fails if everything is deleted | storage_test.go:TestStorage_ApplyRetention_DeletesPointsOlderThanCutoff |
| storage_coverage_test.go | TestStorage_ApplyRetention_NonExistentMetric | moved | only check that an absent metric is not an error | storage_test.go:TestStorage_ApplyRetention_NonExistentMetric |
| storage_coverage_test.go | TestStorage_ApplyRetention_LeavesOtherMetricsAlone | moved | only check of the metric bound | storage_test.go:TestStorage_ApplyRetention_LeavesOtherMetricsAlone |
| storage_coverage_test.go | TestStorage_ApplyRetention_BoundsDeletesPerCall | moved | only check of MaxRetentionDeletesPerApply | storage_test.go:TestStorage_ApplyRetention_BoundsDeletesPerCall |
| storage_coverage_test.go | TestStorage_ApplyRetention_InvalidMetric | moved | only check of ApplyRetention's metric validation | storage_test.go:TestStorage_ApplyRetention_InvalidMetric |
| storage_coverage_test.go | TestStorage_ApplyRetention_AfterClose | moved | only check of ApplyRetention's closed guard | storage_test.go:TestStorage_ApplyRetention_AfterClose |
| storage_coverage_test.go | TestStorage_ListMetrics_ReturnsAllMetrics | moved | only check that every written metric is listed | storage_test.go:TestStorage_ListMetrics_ReturnsAllMetrics |
| storage_coverage_test.go | TestStorage_ListMetrics_WithPrefix | moved | only storage-level check of the prefix filter (upper layers use doubles) | storage_test.go:TestStorage_ListMetrics_WithPrefix |
| storage_coverage_test.go | TestStorage_ListMetrics_WithLimit | moved | only storage-level check of the limit | storage_test.go:TestStorage_ListMetrics_WithLimit |
| storage_coverage_test.go | TestStorage_ListMetrics_EmptyStorage | moved | only check of the empty listing | storage_test.go:TestStorage_ListMetrics_EmptyStorage |
| storage_coverage_test.go | TestStorage_ListMetrics_AfterClose | moved | only check of ListMetrics' closed guard | storage_test.go:TestStorage_ListMetrics_AfterClose |
| storage_coverage_test.go | TestValidationError_Error_ContainsOpAndMessage | moved | only check of ValidationError's text | errors_test.go:TestValidationError_Error_ContainsOpAndMessage |
| storage_coverage_test.go | TestValidationError_Error_EmptyFields | removed | its one assertion, the "timeseries:" prefix, is also asserted by the moved test | errors_test.go:TestValidationError_Error_ContainsOpAndMessage |
| storage_coverage_test.go | TestInvalidAggFuncError_Error_ContainsFuncID | moved | only check of InvalidAggFuncError's text | errors_test.go:TestInvalidAggFuncError_Error_ContainsFuncID |
| storage_coverage_test.go | TestInvalidAggFuncError_Error_ValidFuncID | removed | same format path with another number; the moved test fails if the id is dropped | errors_test.go:TestInvalidAggFuncError_Error_ContainsFuncID |
| storage_coverage_test.go | TestInvalidBatchPointError_Error_ContainsIndex | moved | only check of InvalidBatchPointError's text | errors_test.go:TestInvalidBatchPointError_Error_ContainsIndex |
| storage_coverage_test.go | TestInvalidBatchPointError_Unwrap_ReturnsInnerError | moved | only check of the Unwrap chain | errors_test.go:TestInvalidBatchPointError_Unwrap_ReturnsInnerError |
| storage_coverage_test.go | TestDataPoint_Validate_Valid | removed | a valid point must pass Validate for WritePoint to store it | storage_test.go:TestStorage_WritePoint_ValidPoint |
| storage_coverage_test.go | TestDataPoint_Validate_EmptyMetric | removed | WritePoint returns Validate's error unchanged, and that test asserts InvalidMetricError | storage_test.go:TestStorage_WritePoint_EmptyMetric |
| storage_coverage_test.go | TestDataPoint_Validate_ZeroTimestamp | moved | only check of the timestamp rule | types_test.go:TestDataPoint_Validate_ZeroTimestamp |
| storage_coverage_test.go | TestAggFunc_IsValid_KnownFuncs | moved | only check that min, max and count are accepted | types_test.go:TestAggFunc_IsValid_KnownFuncs |
| storage_coverage_test.go | TestAggFunc_IsValid_InvalidFuncs | moved | only check that AggNone and out-of-range ids are rejected | types_test.go:TestAggFunc_IsValid_InvalidFuncs |
| storage_coverage_test.go | TestAggFunc_String_KnownFuncs | rewritten, moved | merged with String_Unknown into one table | types_test.go:TestAggFunc_String |
| storage_coverage_test.go | TestAggFunc_String_Unknown | rewritten | now a row of the String table | types_test.go:TestAggFunc_String |
| storage_coverage_test.go | TestQueryRequest_Validate_Valid | removed | a valid request must pass Validate for QueryRich to answer | storage_test.go:TestStorage_QueryRich_RawPoints |
| storage_coverage_test.go | TestQueryRequest_Validate_EmptyMetric | removed | QueryRich returns Validate's error unchanged and that test asserts InvalidMetricError | storage_test.go:TestStorage_QueryRich_InvalidMetric |
| storage_coverage_test.go | TestQueryRequest_Validate_ZeroStartTime | moved | only check of the start-time rule | types_test.go:TestQueryRequest_Validate_ZeroStartTime |
| storage_coverage_test.go | TestQueryRequest_Validate_ZeroEndTime | moved | only check of the end-time rule | types_test.go:TestQueryRequest_Validate_ZeroEndTime |
| storage_coverage_test.go | TestQueryRequest_Validate_EndBeforeStart | moved | only check of the range rule | types_test.go:TestQueryRequest_Validate_EndBeforeStart |
| storage_coverage_test.go | TestQueryRequest_Validate_InvalidAggFunc | moved | only check of the aggregation rule | types_test.go:TestQueryRequest_Validate_InvalidAggFunc |
| storage_coverage_test.go | TestQueryRequest_Validate_PercentileOutOfRange | rewritten, moved | now checks both bounds (-5 and 150): the removed QueryRich_PercentileInvalidRange was the only check of the upper one | types_test.go:TestQueryRequest_Validate_PercentileOutOfRange |
| storage_coverage_test.go | TestWriteRequest_Validate_Valid | moved | only test reaching the success return; WriteRequest.Validate has no caller in the package | types_test.go:TestWriteRequest_Validate_Valid |
| storage_coverage_test.go | TestWriteRequest_Validate_EmptyPoints | moved | only check of the empty-batch rule | types_test.go:TestWriteRequest_Validate_EmptyPoints |
| storage_coverage_test.go | TestWriteRequest_Validate_InvalidPointInBatch | moved | only check of the batch index | types_test.go:TestWriteRequest_Validate_InvalidPointInBatch |
| storage_coverage_test.go | TestDeleteRequest_Validate_Valid | removed | same request as ZeroTimesAreAccepted; DeleteSeries of {Metric} must succeed | storage_test.go:TestStorage_DeleteSeries_AllPoints |
| storage_coverage_test.go | TestDeleteRequest_Validate_EmptyMetric | removed | DeleteSeries returns Validate's error unchanged and that test asserts InvalidMetricError | storage_test.go:TestStorage_DeleteSeries_InvalidMetric |
| storage_coverage_test.go | TestDeleteRequest_Validate_EndBeforeStart | moved | only check of the range rule | types_test.go:TestDeleteRequest_Validate_EndBeforeStart |
| storage_coverage_test.go | TestDeleteRequest_Validate_ZeroTimesAreAccepted | removed | a full delete with zero times must succeed and drop every point | storage_test.go:TestStorage_DeleteSeries_AllPoints |
| storage_coverage_test.go | TestAggFirst_ReturnFirstValue | removed | QueryRich with AggFirst over 10,20,30 dispatches to aggFirst and asserts 10 | storage_test.go:TestStorage_QueryRich_FirstAggregation |
| storage_coverage_test.go | TestAggFirst_SingleValue | removed | a one-element slice is first by definition; adds no failing case beyond the 3-value check | storage_test.go:TestStorage_QueryRich_FirstAggregation |
| storage_coverage_test.go | TestAggLast_ReturnLastValue | removed | QueryRich with AggLast over 10,20,30 dispatches to aggLast and asserts 30 | storage_test.go:TestStorage_QueryRich_LastAggregation |
| storage_coverage_test.go | TestAggLast_SingleValue | removed | a one-element slice is last by definition; adds no failing case beyond the 3-value check | storage_test.go:TestStorage_QueryRich_LastAggregation |
| storage_coverage_test.go | TestComputeAggregationWithPercentile_PercentileFunc | moved | only exact check of the percentile index arithmetic (QueryRich_PercentileAggregation asserts >= 90 only) | storage_test.go:TestComputeAggregationWithPercentile_PercentileFunc |
| storage_coverage_test.go | TestComputeAggregationWithPercentile_NonPercentileFallback | removed | QueryRich with AggAvg reaches computeAggregation through this fallback and asserts 20 | storage_test.go:TestStorage_QueryRich_WithAggregation |
| storage_coverage_test.go | TestComputeAggregationWithPercentile_EmptyValues | moved | only check of the empty guard ahead of the len-1 index | storage_test.go:TestComputeAggregationWithPercentile_EmptyValues |
| storage_coverage_test.go | TestAggregatePoints_Sum | removed | aggregatePoints is QueryRich's default path; a wrong value copy fails the avg assertion, and sum dispatch is checked directly | storage_test.go:TestStorage_QueryRich_WithAggregation, storage_test.go:TestComputeAggregation_Sum |
| storage_coverage_test.go | TestAggregatePoints_Empty | moved | only test of aggregatePoints' own empty guard (QueryRich never aggregates an empty range in any test) | storage_test.go:TestAggregatePoints_Empty |
| storage_coverage_test.go | TestAggregatePoints_PercentileWithCustomValue | removed | if aggregatePoints dropped the percentile, p95 over 1..100 would return 1 and fail the >= 90 assertion | storage_test.go:TestStorage_QueryRich_PercentileAggregation |
| storage_coverage_test.go | TestDownsample_BucketsData | moved | only exact check of bucket boundaries and per-bucket values (QueryRich_WithDownsampling asserts >= 2 buckets) | storage_test.go:TestDownsample_BucketsData |
| storage_coverage_test.go | TestDownsample_EmptyPoints | removed | tautology: without the guard the loop over no points also yields zero buckets, so the assertion cannot fail | - |
| storage_coverage_test.go | TestDownsample_ZeroInterval | moved | only check of the zero-interval guard, without which bucketing divides by zero | storage_test.go:TestDownsample_ZeroInterval |
| storage_coverage_test.go | TestGroupAndAggregate_GroupsByTag | removed | QueryRich with GroupBy asserts the same per-group sums (us 30, eu 70) | storage_test.go:TestStorage_QueryRich_WithGroupBy |
| storage_coverage_test.go | TestGroupAndAggregate_EmptyPoints | moved | only check that no points yield nil, not an empty slice | storage_test.go:TestGroupAndAggregate_EmptyPoints |
| storage_coverage_test.go | TestGroupAndAggregate_WithInterval | rewritten, moved | asserted >= 2 points, which raw undownsampled points also satisfy; now asserts nil AggregatedValue and bucket sums 30, 30 | storage_test.go:TestGroupAndAggregate_WithInterval |
| storage_coverage_test.go | TestBuildGroupKey_SingleTag | removed | a key that ignored the tag value would merge the two regions into one group | storage_test.go:TestStorage_QueryRich_WithGroupBy |
| storage_coverage_test.go | TestBuildGroupKey_MultipleTags | moved | only check of the '\|' separator | storage_test.go:TestBuildGroupKey_MultipleTags |
| storage_coverage_test.go | TestBuildGroupKey_MissingTag | moved | only check that a missing tag yields an empty segment | storage_test.go:TestBuildGroupKey_MissingTag |
| storage_coverage_test.go | TestBuildGroupKey_EmptyGroupBy | removed | tautology: no branch handles an empty groupBy, the loop simply does not run; QueryRich never calls it with one | - |
| storage_coverage_test.go | TestStorage_FullLifecycle | removed | every phase is asserted by a focused test: write, prefix list, tag-filtered aggregate, retention set/get, delete-all | storage_test.go:TestStorage_WritePoint_ValidPoint, TestStorage_ListMetrics_WithPrefix, TestStorage_QueryRich_WithTagFilter, TestStorage_QueryRich_WithAggregation, TestStorage_RetentionPolicy_SetAndGet, TestStorage_DeleteSeries_AllPoints |

## Findings fixed in place

- (a) pkg/timeseries: `SetRetentionPolicy` passed a nil policy to `encodeRetentionPolicy`, which dereferenced it and panicked. It now returns the typed `NilRetentionPolicyError` and stores nothing. The test was written first and panicked before the fix: storage_test.go:TestStorage_SetRetentionPolicy_NilPolicy, plus errors_test.go:TestNilRetentionPolicyError_Error_{ContainsMessage,HasPrefix}. Coverage holds at 94.8%.
- (b) pkg/storage/pebble: `buildIterOptions`'s `if opts.OnlyKeys` branch assigned `pebble.IterKeyTypePointsOnly`, the zero value of `KeyTypes` and already the default, so it changed nothing. The branch is deleted and a comment says why OnlyKeys has no pebble counterpart. No test referenced it. Coverage after the change is 96.1% (floor 94.39); `go vet` and gofmt are clean.
