//go:build qrdb_ts

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package timeseries

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// WritePoint
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// QueryRich
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// DeleteSeries
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// GetRetentionPolicy / SetRetentionPolicy
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// ListMetrics
// ---------------------------------------------------------------------------

func TestStorage_ListMetrics_ReturnsAllMetrics(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = write(t, s, "cpu.usage", 10.0, base, nil)
	_ = write(t, s, "mem.free", 1024.0, base, nil)
	_ = write(t, s, "disk.io", 50.0, base, nil)

	metrics, err := s.ListMetrics("", 0)
	if err != nil {
		t.Fatalf("ListMetrics: unexpected error: %v", err)
	}
	if len(metrics) != 3 {
		t.Fatalf("ListMetrics: got %d metrics, want 3", len(metrics))
	}

	found := map[string]bool{}
	for _, m := range metrics {
		found[m] = true
	}
	for _, expected := range []string{"cpu.usage", "mem.free", "disk.io"} {
		if !found[expected] {
			t.Errorf("ListMetrics: missing metric %q", expected)
		}
	}
}

func TestStorage_ListMetrics_WithPrefix(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = write(t, s, "cpu.usage", 10.0, base, nil)
	_ = write(t, s, "cpu.load", 5.0, base, nil)
	_ = write(t, s, "mem.free", 1024.0, base, nil)

	metrics, err := s.ListMetrics("cpu", 0)
	if err != nil {
		t.Fatalf("ListMetrics(prefix): unexpected error: %v", err)
	}
	if len(metrics) != 2 {
		t.Errorf("ListMetrics(prefix): got %d metrics, want 2", len(metrics))
	}
	for _, m := range metrics {
		if !strings.HasPrefix(m, "cpu") {
			t.Errorf("ListMetrics(prefix): metric %q does not start with 'cpu'", m)
		}
	}
}

func TestStorage_ListMetrics_WithLimit(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = write(t, s, "cpu.usage", 10.0, base, nil)
	_ = write(t, s, "mem.free", 1024.0, base, nil)
	_ = write(t, s, "disk.io", 50.0, base, nil)

	metrics, err := s.ListMetrics("", 2)
	if err != nil {
		t.Fatalf("ListMetrics(limit): unexpected error: %v", err)
	}
	if len(metrics) != 2 {
		t.Errorf("ListMetrics(limit): got %d metrics, want 2", len(metrics))
	}
}

func TestStorage_ListMetrics_EmptyStorage(t *testing.T) {
	s := newStorage(t)

	metrics, err := s.ListMetrics("", 0)
	if err != nil {
		t.Fatalf("ListMetrics(empty): unexpected error: %v", err)
	}
	if len(metrics) != 0 {
		t.Errorf("ListMetrics(empty): got %d metrics, want 0", len(metrics))
	}
}

func TestStorage_ListMetrics_AfterClose(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()

	_, err := s.ListMetrics("", 0)
	if err == nil {
		t.Fatal("ListMetrics(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("ListMetrics(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Error types at 0% coverage
// ---------------------------------------------------------------------------

func TestValidationError_Error_ContainsOpAndMessage(t *testing.T) {
	e := &ValidationError{Op: "QueryRequest.Validate", Message: "percentile must be between 0 and 100"}
	got := e.Error()
	if !strings.Contains(got, "QueryRequest.Validate") {
		t.Errorf("Error: expected op in message, got %q", got)
	}
	if !strings.Contains(got, "percentile must be between 0 and 100") {
		t.Errorf("Error: expected message in output, got %q", got)
	}
	if !strings.HasPrefix(got, "timeseries:") {
		t.Errorf("Error: expected 'timeseries:' prefix, got %q", got)
	}
}

func TestValidationError_Error_EmptyFields(t *testing.T) {
	e := &ValidationError{}
	got := e.Error()
	if !strings.HasPrefix(got, "timeseries:") {
		t.Errorf("Error: expected 'timeseries:' prefix, got %q", got)
	}
}

func TestInvalidAggFuncError_Error_ContainsFuncID(t *testing.T) {
	e := &InvalidAggFuncError{Func: AggFunc(99)}
	got := e.Error()
	if !strings.Contains(got, "99") {
		t.Errorf("Error: expected func id '99' in message, got %q", got)
	}
	if !strings.Contains(got, "invalid aggregation function") {
		t.Errorf("Error: expected 'invalid aggregation function', got %q", got)
	}
}

func TestInvalidAggFuncError_Error_ValidFuncID(t *testing.T) {
	e := &InvalidAggFuncError{Func: AggAvg}
	got := e.Error()
	if !strings.Contains(got, "1") {
		t.Errorf("Error: expected func id '1' in message, got %q", got)
	}
}

func TestInvalidBatchPointError_Error_ContainsIndex(t *testing.T) {
	inner := &InvalidMetricError{}
	e := &InvalidBatchPointError{Index: 3, Err: inner}
	got := e.Error()
	if !strings.Contains(got, "3") {
		t.Errorf("Error: expected index '3' in message, got %q", got)
	}
	if !strings.Contains(got, "invalid data point at index") {
		t.Errorf("Error: expected 'invalid data point at index', got %q", got)
	}
}

func TestInvalidBatchPointError_Unwrap_ReturnsInnerError(t *testing.T) {
	inner := &InvalidTimestampError{}
	e := &InvalidBatchPointError{Index: 0, Err: inner}
	var ite *InvalidTimestampError
	if !errors.As(e, &ite) {
		t.Error("Unwrap: errors.As failed for inner InvalidTimestampError")
	}
}

// ---------------------------------------------------------------------------
// Types at 0% coverage: DataPoint.Validate
// ---------------------------------------------------------------------------

func TestDataPoint_Validate_Valid(t *testing.T) {
	dp := &DataPoint{
		Metric:    "cpu.usage",
		Value:     42.5,
		Timestamp: time.Now(),
	}
	if err := dp.Validate(); err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}
}

func TestDataPoint_Validate_EmptyMetric(t *testing.T) {
	dp := &DataPoint{
		Metric:    "",
		Value:     42.5,
		Timestamp: time.Now(),
	}
	err := dp.Validate()
	if err == nil {
		t.Fatal("Validate(empty metric): expected error, got nil")
	}
	var ime *InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("Validate(empty metric): expected InvalidMetricError, got %T: %v", err, err)
	}
}

func TestDataPoint_Validate_ZeroTimestamp(t *testing.T) {
	dp := &DataPoint{
		Metric:    "cpu.usage",
		Value:     42.5,
		Timestamp: time.Time{},
	}
	err := dp.Validate()
	if err == nil {
		t.Fatal("Validate(zero timestamp): expected error, got nil")
	}
	var ite *InvalidTimestampError
	if !errors.As(err, &ite) {
		t.Fatalf("Validate(zero timestamp): expected InvalidTimestampError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Types at 0% coverage: AggFunc.IsValid / AggFunc.String
// ---------------------------------------------------------------------------

func TestAggFunc_IsValid_KnownFuncs(t *testing.T) {
	validFuncs := []AggFunc{AggAvg, AggSum, AggMin, AggMax, AggCount, AggPercentile, AggFirst, AggLast}
	for _, f := range validFuncs {
		if !f.IsValid() {
			t.Errorf("IsValid(%d): expected true", f)
		}
	}
}

func TestAggFunc_IsValid_InvalidFuncs(t *testing.T) {
	invalidFuncs := []AggFunc{AggNone, AggFunc(99), AggFunc(255)}
	for _, f := range invalidFuncs {
		if f.IsValid() {
			t.Errorf("IsValid(%d): expected false", f)
		}
	}
}

func TestAggFunc_String_KnownFuncs(t *testing.T) {
	tests := []struct {
		fn   AggFunc
		want string
	}{
		{AggNone, "none"},
		{AggAvg, "avg"},
		{AggSum, "sum"},
		{AggMin, "min"},
		{AggMax, "max"},
		{AggCount, "count"},
		{AggPercentile, "percentile"},
		{AggFirst, "first"},
		{AggLast, "last"},
	}
	for _, tt := range tests {
		got := tt.fn.String()
		if got != tt.want {
			t.Errorf("String(%d): got %q, want %q", tt.fn, got, tt.want)
		}
	}
}

func TestAggFunc_String_Unknown(t *testing.T) {
	got := AggFunc(99).String()
	if got != "unknown" {
		t.Errorf("String(99): got %q, want %q", got, "unknown")
	}
}

// ---------------------------------------------------------------------------
// Types at 0% coverage: QueryRequest.Validate
// ---------------------------------------------------------------------------

func TestQueryRequest_Validate_Valid(t *testing.T) {
	qr := &QueryRequest{
		Metric:    "cpu.usage",
		StartTime: ts(2026, 3, 1, 10, 0, 0),
		EndTime:   ts(2026, 3, 1, 11, 0, 0),
	}
	if err := qr.Validate(); err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}
}

func TestQueryRequest_Validate_EmptyMetric(t *testing.T) {
	qr := &QueryRequest{
		Metric:    "",
		StartTime: time.Now(),
		EndTime:   time.Now().Add(time.Hour),
	}
	err := qr.Validate()
	if err == nil {
		t.Fatal("Validate(empty metric): expected error, got nil")
	}
	var ime *InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("Validate(empty metric): expected InvalidMetricError, got %T: %v", err, err)
	}
}

func TestQueryRequest_Validate_ZeroStartTime(t *testing.T) {
	qr := &QueryRequest{
		Metric:    "cpu.usage",
		StartTime: time.Time{},
		EndTime:   time.Now(),
	}
	err := qr.Validate()
	if err == nil {
		t.Fatal("Validate(zero start): expected error, got nil")
	}
	var ite *InvalidTimestampError
	if !errors.As(err, &ite) {
		t.Fatalf("Validate(zero start): expected InvalidTimestampError, got %T: %v", err, err)
	}
}

func TestQueryRequest_Validate_ZeroEndTime(t *testing.T) {
	qr := &QueryRequest{
		Metric:    "cpu.usage",
		StartTime: time.Now(),
		EndTime:   time.Time{},
	}
	err := qr.Validate()
	if err == nil {
		t.Fatal("Validate(zero end): expected error, got nil")
	}
	var ite *InvalidTimestampError
	if !errors.As(err, &ite) {
		t.Fatalf("Validate(zero end): expected InvalidTimestampError, got %T: %v", err, err)
	}
}

func TestQueryRequest_Validate_EndBeforeStart(t *testing.T) {
	qr := &QueryRequest{
		Metric:    "cpu.usage",
		StartTime: ts(2026, 3, 1, 12, 0, 0),
		EndTime:   ts(2026, 3, 1, 10, 0, 0),
	}
	err := qr.Validate()
	if err == nil {
		t.Fatal("Validate(end before start): expected error, got nil")
	}
	var itr *InvalidTimeRangeError
	if !errors.As(err, &itr) {
		t.Fatalf("Validate(end before start): expected InvalidTimeRangeError, got %T: %v", err, err)
	}
}

func TestQueryRequest_Validate_InvalidAggFunc(t *testing.T) {
	qr := &QueryRequest{
		Metric:      "cpu.usage",
		StartTime:   ts(2026, 3, 1, 10, 0, 0),
		EndTime:     ts(2026, 3, 1, 11, 0, 0),
		Aggregation: AggFunc(99),
	}
	err := qr.Validate()
	if err == nil {
		t.Fatal("Validate(invalid agg): expected error, got nil")
	}
	var iaf *InvalidAggFuncError
	if !errors.As(err, &iaf) {
		t.Fatalf("Validate(invalid agg): expected InvalidAggFuncError, got %T: %v", err, err)
	}
}

func TestQueryRequest_Validate_PercentileOutOfRange(t *testing.T) {
	qr := &QueryRequest{
		Metric:      "cpu.usage",
		StartTime:   ts(2026, 3, 1, 10, 0, 0),
		EndTime:     ts(2026, 3, 1, 11, 0, 0),
		Aggregation: AggPercentile,
		Percentile:  -5.0,
	}
	err := qr.Validate()
	if err == nil {
		t.Fatal("Validate(percentile<0): expected error, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate(percentile<0): expected ValidationError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Types at 0% coverage: WriteRequest.Validate
// ---------------------------------------------------------------------------

func TestWriteRequest_Validate_Valid(t *testing.T) {
	wr := &WriteRequest{
		Points: []*DataPoint{
			{Metric: "cpu.usage", Value: 42.5, Timestamp: time.Now()},
		},
	}
	if err := wr.Validate(); err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}
}

func TestWriteRequest_Validate_EmptyPoints(t *testing.T) {
	wr := &WriteRequest{Points: []*DataPoint{}}
	err := wr.Validate()
	if err == nil {
		t.Fatal("Validate(empty): expected error, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate(empty): expected ValidationError, got %T: %v", err, err)
	}
}

func TestWriteRequest_Validate_InvalidPointInBatch(t *testing.T) {
	wr := &WriteRequest{
		Points: []*DataPoint{
			{Metric: "cpu.usage", Value: 1.0, Timestamp: time.Now()},
			{Metric: "", Value: 2.0, Timestamp: time.Now()}, // invalid
		},
	}
	err := wr.Validate()
	if err == nil {
		t.Fatal("Validate(invalid point): expected error, got nil")
	}
	var ibpe *InvalidBatchPointError
	if !errors.As(err, &ibpe) {
		t.Fatalf("Validate(invalid point): expected InvalidBatchPointError, got %T: %v", err, err)
	}
	if ibpe.Index != 1 {
		t.Errorf("Validate(invalid point): index got %d, want 1", ibpe.Index)
	}
}

// ---------------------------------------------------------------------------
// Types at 0% coverage: DeleteRequest.Validate
// ---------------------------------------------------------------------------

func TestDeleteRequest_Validate_Valid(t *testing.T) {
	dr := &DeleteRequest{Metric: "cpu.usage"}
	if err := dr.Validate(); err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}
}

func TestDeleteRequest_Validate_EmptyMetric(t *testing.T) {
	dr := &DeleteRequest{Metric: ""}
	err := dr.Validate()
	if err == nil {
		t.Fatal("Validate(empty metric): expected error, got nil")
	}
	var ime *InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("Validate(empty metric): expected InvalidMetricError, got %T: %v", err, err)
	}
}

func TestDeleteRequest_Validate_EndBeforeStart(t *testing.T) {
	dr := &DeleteRequest{
		Metric:    "cpu.usage",
		StartTime: ts(2026, 3, 1, 12, 0, 0),
		EndTime:   ts(2026, 3, 1, 10, 0, 0),
	}
	err := dr.Validate()
	if err == nil {
		t.Fatal("Validate(end before start): expected error, got nil")
	}
	var itr *InvalidTimeRangeError
	if !errors.As(err, &itr) {
		t.Fatalf("Validate(end before start): expected InvalidTimeRangeError, got %T: %v", err, err)
	}
}

func TestDeleteRequest_Validate_ZeroTimesAreAccepted(t *testing.T) {
	dr := &DeleteRequest{
		Metric: "cpu.usage",
		// Zero start and end times are allowed to mean "all".
	}
	if err := dr.Validate(); err != nil {
		t.Fatalf("Validate(zero times): unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Internal helpers at 0%: encodeRetentionPolicy / decodeRetentionPolicy
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Internal helpers at 0%: aggFirst / aggLast
// ---------------------------------------------------------------------------

func TestAggFirst_ReturnFirstValue(t *testing.T) {
	result := aggFirst([]float64{10, 20, 30})
	if result != 10.0 {
		t.Errorf("aggFirst: got %f, want 10.0", result)
	}
}

func TestAggFirst_SingleValue(t *testing.T) {
	result := aggFirst([]float64{42.0})
	if result != 42.0 {
		t.Errorf("aggFirst(single): got %f, want 42.0", result)
	}
}

func TestAggLast_ReturnLastValue(t *testing.T) {
	result := aggLast([]float64{10, 20, 30})
	if result != 30.0 {
		t.Errorf("aggLast: got %f, want 30.0", result)
	}
}

func TestAggLast_SingleValue(t *testing.T) {
	result := aggLast([]float64{42.0})
	if result != 42.0 {
		t.Errorf("aggLast(single): got %f, want 42.0", result)
	}
}

// ---------------------------------------------------------------------------
// Internal helpers at 0%: computeAggregationWithPercentile
// ---------------------------------------------------------------------------

func TestComputeAggregationWithPercentile_PercentileFunc(t *testing.T) {
	values := make([]float64, 100)
	for i := range values {
		values[i] = float64(i + 1)
	}

	result := computeAggregationWithPercentile(AggPercentile, values, 50.0)
	// P50 of 1..100: index = (100-1)*0.5 = 49.5, int = 49 -> values[49] = 50
	if result != 50.0 {
		t.Errorf("computeAggregationWithPercentile(p50): got %f, want 50.0", result)
	}
}

func TestComputeAggregationWithPercentile_NonPercentileFallback(t *testing.T) {
	values := []float64{10, 20, 30}
	result := computeAggregationWithPercentile(AggAvg, values, 0)
	if result != 20.0 {
		t.Errorf("computeAggregationWithPercentile(avg fallback): got %f, want 20.0", result)
	}
}

func TestComputeAggregationWithPercentile_EmptyValues(t *testing.T) {
	result := computeAggregationWithPercentile(AggPercentile, []float64{}, 95.0)
	if result != 0 {
		t.Errorf("computeAggregationWithPercentile(empty): got %f, want 0", result)
	}
}

// ---------------------------------------------------------------------------
// Internal helpers at 0%: aggregatePoints
// ---------------------------------------------------------------------------

func TestAggregatePoints_Sum(t *testing.T) {
	points := []*DataPoint{
		{Value: 10.0},
		{Value: 20.0},
		{Value: 30.0},
	}
	result := aggregatePoints(points, AggSum, 0)
	if result != 60.0 {
		t.Errorf("aggregatePoints(sum): got %f, want 60.0", result)
	}
}

func TestAggregatePoints_Empty(t *testing.T) {
	result := aggregatePoints([]*DataPoint{}, AggSum, 0)
	if result != 0 {
		t.Errorf("aggregatePoints(empty): got %f, want 0", result)
	}
}

func TestAggregatePoints_PercentileWithCustomValue(t *testing.T) {
	points := make([]*DataPoint, 100)
	for i := range points {
		points[i] = &DataPoint{Value: float64(i + 1)}
	}
	result := aggregatePoints(points, AggPercentile, 90.0)
	// P90 of 1..100: index = (100-1)*0.9 = 89.1, int = 89 -> values[89] = 90
	if result != 90.0 {
		t.Errorf("aggregatePoints(p90): got %f, want 90.0", result)
	}
}

// ---------------------------------------------------------------------------
// Internal helpers at 0%: downsample
// ---------------------------------------------------------------------------

func TestDownsample_BucketsData(t *testing.T) {
	base := ts(2026, 3, 1, 10, 0, 0)
	points := []*DataPoint{
		{Metric: "cpu", Value: 10.0, Timestamp: base},
		{Metric: "cpu", Value: 20.0, Timestamp: base.Add(30 * time.Minute)},
		{Metric: "cpu", Value: 30.0, Timestamp: base.Add(time.Hour)},
		{Metric: "cpu", Value: 40.0, Timestamp: base.Add(90 * time.Minute)},
	}

	result := downsample(points, time.Hour, AggSum, 0)
	if len(result) != 2 {
		t.Fatalf("downsample: got %d buckets, want 2", len(result))
	}
	// First bucket: 10+20=30, second: 30+40=70.
	if result[0].Value != 30.0 {
		t.Errorf("downsample bucket[0]: got %f, want 30.0", result[0].Value)
	}
	if result[1].Value != 70.0 {
		t.Errorf("downsample bucket[1]: got %f, want 70.0", result[1].Value)
	}
}

func TestDownsample_EmptyPoints(t *testing.T) {
	result := downsample([]*DataPoint{}, time.Hour, AggAvg, 0)
	if len(result) != 0 {
		t.Errorf("downsample(empty): got %d buckets, want 0", len(result))
	}
}

func TestDownsample_ZeroInterval(t *testing.T) {
	points := []*DataPoint{{Value: 1.0}}
	result := downsample(points, 0, AggAvg, 0)
	if len(result) != 1 {
		t.Errorf("downsample(zero interval): got %d, want 1 (passthrough)", len(result))
	}
}

// ---------------------------------------------------------------------------
// Internal helpers at 0%: groupAndAggregate / buildGroupKey
// ---------------------------------------------------------------------------

func TestGroupAndAggregate_GroupsByTag(t *testing.T) {
	base := ts(2026, 3, 1, 10, 0, 0)
	points := []*DataPoint{
		{Value: 10.0, Tags: map[string]string{"host": "a"}, Timestamp: base},
		{Value: 20.0, Tags: map[string]string{"host": "b"}, Timestamp: base.Add(time.Minute)},
		{Value: 30.0, Tags: map[string]string{"host": "a"}, Timestamp: base.Add(2 * time.Minute)},
	}

	results := groupAndAggregate(points, []string{"host"}, AggSum, 0, 0)
	if len(results) != 2 {
		t.Fatalf("groupAndAggregate: got %d groups, want 2", len(results))
	}
	for _, g := range results {
		if g.AggregatedValue == nil {
			t.Error("groupAndAggregate: expected non-nil AggregatedValue")
			continue
		}
		host := g.Tags["host"]
		if host == "a" && *g.AggregatedValue != 40.0 {
			t.Errorf("groupAndAggregate(host=a): got %f, want 40.0", *g.AggregatedValue)
		}
		if host == "b" && *g.AggregatedValue != 20.0 {
			t.Errorf("groupAndAggregate(host=b): got %f, want 20.0", *g.AggregatedValue)
		}
	}
}

func TestGroupAndAggregate_EmptyPoints(t *testing.T) {
	results := groupAndAggregate([]*DataPoint{}, []string{"host"}, AggSum, 0, 0)
	if results != nil {
		t.Errorf("groupAndAggregate(empty): expected nil, got %v", results)
	}
}

func TestGroupAndAggregate_WithInterval(t *testing.T) {
	base := ts(2026, 3, 1, 10, 0, 0)
	points := []*DataPoint{
		{Metric: "cpu", Value: 10.0, Tags: map[string]string{"host": "a"}, Timestamp: base},
		{Metric: "cpu", Value: 20.0, Tags: map[string]string{"host": "a"}, Timestamp: base.Add(30 * time.Minute)},
		{Metric: "cpu", Value: 30.0, Tags: map[string]string{"host": "a"}, Timestamp: base.Add(time.Hour)},
	}

	results := groupAndAggregate(points, []string{"host"}, AggSum, time.Hour, 0)
	if len(results) != 1 {
		t.Fatalf("groupAndAggregate(interval): got %d groups, want 1", len(results))
	}
	// With interval, we get downsampled points instead of AggregatedValue.
	if len(results[0].Points) < 2 {
		t.Errorf("groupAndAggregate(interval): expected >= 2 downsampled points, got %d", len(results[0].Points))
	}
}

func TestBuildGroupKey_SingleTag(t *testing.T) {
	tags := map[string]string{"host": "srv1"}
	key := buildGroupKey(tags, []string{"host"})
	if key != "srv1" {
		t.Errorf("buildGroupKey(single): got %q, want %q", key, "srv1")
	}
}

func TestBuildGroupKey_MultipleTags(t *testing.T) {
	tags := map[string]string{"host": "srv1", "region": "us"}
	key := buildGroupKey(tags, []string{"host", "region"})
	if key != "srv1|us" {
		t.Errorf("buildGroupKey(multi): got %q, want %q", key, "srv1|us")
	}
}

func TestBuildGroupKey_MissingTag(t *testing.T) {
	tags := map[string]string{"host": "srv1"}
	key := buildGroupKey(tags, []string{"host", "region"})
	if key != "srv1|" {
		t.Errorf("buildGroupKey(missing): got %q, want %q", key, "srv1|")
	}
}

func TestBuildGroupKey_EmptyGroupBy(t *testing.T) {
	tags := map[string]string{"host": "srv1"}
	key := buildGroupKey(tags, []string{})
	if key != "" {
		t.Errorf("buildGroupKey(empty): got %q, want empty", key)
	}
}

// ---------------------------------------------------------------------------
// Full lifecycle integration: write, query, delete, verify cleanup
// ---------------------------------------------------------------------------

func TestStorage_FullLifecycle(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	// Phase 1: Write data points via WritePoint.
	for i := 0; i < 5; i++ {
		point := &DataPoint{
			Metric:    "app.requests",
			Value:     float64(i+1) * 100.0,
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Tags:      map[string]string{"env": "prod"},
		}
		if err := writePoint(t, s, point); err != nil {
			t.Fatalf("WritePoint[%d]: unexpected error: %v", i, err)
		}
	}

	// Phase 2: Verify with ListMetrics.
	metrics, err := s.ListMetrics("app", 0)
	if err != nil {
		t.Fatalf("ListMetrics: unexpected error: %v", err)
	}
	if len(metrics) != 1 || metrics[0] != "app.requests" {
		t.Fatalf("ListMetrics: got %v, want [app.requests]", metrics)
	}

	// Phase 3: QueryRich with aggregation.
	req := &QueryRequest{
		Metric:      "app.requests",
		StartTime:   base,
		EndTime:     base.Add(4 * time.Minute),
		Aggregation: AggSum,
		Tags:        map[string]string{"env": "prod"},
	}
	result, err := s.QueryRich(req)
	if err != nil {
		t.Fatalf("QueryRich: unexpected error: %v", err)
	}
	if result.AggregatedValue == nil {
		t.Fatal("QueryRich: expected non-nil AggregatedValue")
	}
	expectedSum := 100.0 + 200.0 + 300.0 + 400.0 + 500.0
	if *result.AggregatedValue != expectedSum {
		t.Errorf("QueryRich: sum got %f, want %f", *result.AggregatedValue, expectedSum)
	}

	// Phase 4: Set retention policy.
	policy := &RetentionPolicy{Duration: 7 * 24 * time.Hour, Aggregation: AggAvg}
	if err := setRetentionPolicy(t, s, "app.requests", policy); err != nil {
		t.Fatalf("SetRetentionPolicy: unexpected error: %v", err)
	}

	// Phase 5: Get retention policy.
	gotPolicy, err := s.GetRetentionPolicy("app.requests")
	if err != nil {
		t.Fatalf("GetRetentionPolicy: unexpected error: %v", err)
	}
	if gotPolicy.Duration != 7*24*time.Hour {
		t.Errorf("GetRetentionPolicy: duration got %v, want 168h", gotPolicy.Duration)
	}

	// Phase 6: Delete series.
	delReq := &DeleteRequest{Metric: "app.requests"}
	if err := deleteSeries(t, s, delReq); err != nil {
		t.Fatalf("DeleteSeries: unexpected error: %v", err)
	}

	// Phase 7: Verify deletion.
	results, err := s.Query("app.requests", base, base.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("Query after delete: unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Query after delete: got %d results, want 0", len(results))
	}
}

// ---------------------------------------------------------------------------
// QueryRich with First/Last aggregation (covers aggFirst/aggLast via
// aggregatePoints path)
// ---------------------------------------------------------------------------

