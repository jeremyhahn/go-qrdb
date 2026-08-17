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
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// NewWithDir
// ---------------------------------------------------------------------------

func TestNewWithDir_WritesAndReadsDataPoint(t *testing.T) {
	dir := t.TempDir()
	s, err := NewWithDir(dir)
	if err != nil {
		t.Fatalf("NewWithDir(%q): unexpected error: %v", dir, err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: unexpected error: %v", err)
		}
	}()

	timestamp := ts(2026, 3, 1, 12, 0, 0)
	if err := s.Write("cpu.usage", 42.5, timestamp, map[string]string{"host": "node-1"}); err != nil {
		t.Fatalf("Write: unexpected error: %v", err)
	}

	points, err := s.Query("cpu.usage", timestamp, timestamp)
	if err != nil {
		t.Fatalf("Query: unexpected error: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("Query: expected 1 point, got %d", len(points))
	}
	if points[0].Value != 42.5 {
		t.Errorf("Query: expected value 42.5, got %f", points[0].Value)
	}
	if points[0].Metric != "cpu.usage" {
		t.Errorf("Query: expected metric 'cpu.usage', got %q", points[0].Metric)
	}
}

func TestNewWithDir_EmptyDir(t *testing.T) {
	_, err := NewWithDir("")
	if err == nil {
		t.Fatal("NewWithDir(''): expected EmptyDirError, got nil")
	}
	var ede *EmptyDirError
	if !errors.As(err, &ede) {
		t.Fatalf("NewWithDir(''): expected *EmptyDirError, got %T: %v", err, err)
	}
}

func TestNewWithDir_InvalidDir(t *testing.T) {
	_, err := NewWithDir("/dev/null/not-a-directory")
	if err == nil {
		t.Fatal("NewWithDir(invalid): expected OpenDirError, got nil")
	}
	var ode *OpenDirError
	if !errors.As(err, &ode) {
		t.Fatalf("NewWithDir(invalid): expected *OpenDirError, got %T: %v", err, err)
	}
	if ode.Dir != "/dev/null/not-a-directory" {
		t.Errorf("OpenDirError.Dir: got %q, want %q", ode.Dir, "/dev/null/not-a-directory")
	}
	if ode.Unwrap() == nil {
		t.Error("OpenDirError.Unwrap: expected non-nil underlying error")
	}
}

func TestNewWithDir_Close_ClosesPebbleDB(t *testing.T) {
	dir := t.TempDir()
	s, err := NewWithDir(dir)
	if err != nil {
		t.Fatalf("NewWithDir(%q): unexpected error: %v", dir, err)
	}

	// Write a data point to confirm the DB is functional.
	timestamp := ts(2026, 3, 1, 12, 0, 0)
	if err := s.Write("test.metric", 1.0, timestamp, nil); err != nil {
		t.Fatalf("Write: unexpected error: %v", err)
	}

	// Close should close the underlying PebbleDB.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}

	// After Close, attempting to re-open the same directory should succeed,
	// proving the lock was released (PebbleDB was closed).
	s2, err := NewWithDir(dir)
	if err != nil {
		t.Fatalf("NewWithDir (after close): expected success, got %v", err)
	}
	defer func() {
		if err := s2.Close(); err != nil {
			t.Errorf("Close(s2): unexpected error: %v", err)
		}
	}()

	// Verify the data persisted.
	points, err := s2.Query("test.metric", timestamp, timestamp)
	if err != nil {
		t.Fatalf("Query(s2): unexpected error: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("Query(s2): expected 1 point, got %d", len(points))
	}
}

func TestNewWithDir_DoubleClose_ReturnsClosedError(t *testing.T) {
	dir := t.TempDir()
	s, err := NewWithDir(dir)
	if err != nil {
		t.Fatalf("NewWithDir(%q): unexpected error: %v", dir, err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close (first): unexpected error: %v", err)
	}

	err = s.Close()
	if err == nil {
		t.Fatal("Close (second): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Close (second): expected *ClosedError, got %T: %v", err, err)
	}
}

func TestNewWithDir_OwnsDB_True(t *testing.T) {
	dir := t.TempDir()
	s, err := NewWithDir(dir)
	if err != nil {
		t.Fatalf("NewWithDir(%q): unexpected error: %v", dir, err)
	}
	defer func() { _ = s.Close() }()

	if !s.ownsDB {
		t.Error("NewWithDir: expected ownsDB to be true")
	}
}

func TestNew_OwnsDB_False(t *testing.T) {
	db := openPebble(t)
	s, err := New(db)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	if s.ownsDB {
		t.Error("New: expected ownsDB to be false")
	}
}

// ---------------------------------------------------------------------------
// Storage.DB accessor
// ---------------------------------------------------------------------------

func TestStorage_DB_ReturnsUnderlyingPebbleDB(t *testing.T) {
	db := openPebble(t)
	s, err := New(db)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	if s.DB() != db {
		t.Error("DB: returned a different *pebble.DB than was provided to New")
	}
}

func TestStorage_DB_NotNil(t *testing.T) {
	s := newStorage(t)
	if s.DB() == nil {
		t.Error("DB: returned nil for a valid Storage")
	}
}

// ---------------------------------------------------------------------------
// WritePoint
// ---------------------------------------------------------------------------

func TestStorage_WritePoint_ValidPoint(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	timestamp := ts(2026, 3, 1, 12, 0, 0)

	point := &DataPoint{
		Metric:    "cpu.load",
		Value:     75.5,
		Timestamp: timestamp,
		Tags:      map[string]string{"host": "node1"},
	}

	if err := s.WritePoint(ctx, point); err != nil {
		t.Fatalf("WritePoint: unexpected error: %v", err)
	}

	results, err := s.Query("cpu.load", timestamp, timestamp)
	if err != nil {
		t.Fatalf("Query: unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Query: got %d results, want 1", len(results))
	}
	if results[0].Value != 75.5 {
		t.Errorf("Query: value got %f, want 75.5", results[0].Value)
	}
	if results[0].Tags["host"] != "node1" {
		t.Errorf("Query: tag host got %q, want %q", results[0].Tags["host"], "node1")
	}
}

func TestStorage_WritePoint_EmptyMetric(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()

	point := &DataPoint{
		Metric:    "",
		Value:     1.0,
		Timestamp: time.Now(),
	}

	err := s.WritePoint(ctx, point)
	if err == nil {
		t.Fatal("WritePoint(empty metric): expected error, got nil")
	}
	var ime *InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("WritePoint(empty metric): expected InvalidMetricError, got %T: %v", err, err)
	}
}

func TestStorage_WritePoint_ZeroTimestamp(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()

	point := &DataPoint{
		Metric:    "cpu.load",
		Value:     1.0,
		Timestamp: time.Time{},
	}

	err := s.WritePoint(ctx, point)
	if err == nil {
		t.Fatal("WritePoint(zero timestamp): expected error, got nil")
	}
	var ite *InvalidTimestampError
	if !errors.As(err, &ite) {
		t.Fatalf("WritePoint(zero timestamp): expected InvalidTimestampError, got %T: %v", err, err)
	}
}

func TestStorage_WritePoint_AfterClose(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()
	ctx := context.Background()

	point := &DataPoint{
		Metric:    "cpu.load",
		Value:     1.0,
		Timestamp: time.Now(),
	}

	err := s.WritePoint(ctx, point)
	if err == nil {
		t.Fatal("WritePoint(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("WritePoint(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestStorage_WritePoint_WithoutTags(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	timestamp := ts(2026, 3, 1, 12, 0, 0)

	point := &DataPoint{
		Metric:    "mem.used",
		Value:     2048.0,
		Timestamp: timestamp,
	}

	if err := s.WritePoint(ctx, point); err != nil {
		t.Fatalf("WritePoint(no tags): unexpected error: %v", err)
	}

	results, err := s.Query("mem.used", timestamp, timestamp)
	if err != nil {
		t.Fatalf("Query: unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Query: got %d results, want 1", len(results))
	}
}

// ---------------------------------------------------------------------------
// QueryRich
// ---------------------------------------------------------------------------

func TestStorage_QueryRich_RawPoints(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	base := ts(2026, 3, 1, 10, 0, 0)

	for i := 0; i < 5; i++ {
		_ = s.Write("req.latency", float64(i+1)*10.0, base.Add(time.Duration(i)*time.Minute), map[string]string{"host": "web1"})
	}

	req := &QueryRequest{
		Metric:    "req.latency",
		StartTime: base,
		EndTime:   base.Add(4 * time.Minute),
	}

	result, err := s.QueryRich(ctx, req)
	if err != nil {
		t.Fatalf("QueryRich: unexpected error: %v", err)
	}
	if result.Metric != "req.latency" {
		t.Errorf("QueryRich: metric got %q, want %q", result.Metric, "req.latency")
	}
	if len(result.Points) != 5 {
		t.Errorf("QueryRich: got %d points, want 5", len(result.Points))
	}
	if result.AggregatedValue != nil {
		t.Error("QueryRich: expected nil AggregatedValue for raw query")
	}
}

func TestStorage_QueryRich_WithTagFilter(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = s.Write("cpu.usage", 10.0, base, map[string]string{"host": "srv1"})
	_ = s.Write("cpu.usage", 20.0, base.Add(time.Minute), map[string]string{"host": "srv2"})
	_ = s.Write("cpu.usage", 30.0, base.Add(2*time.Minute), map[string]string{"host": "srv1"})

	req := &QueryRequest{
		Metric:    "cpu.usage",
		StartTime: base,
		EndTime:   base.Add(2 * time.Minute),
		Tags:      map[string]string{"host": "srv1"},
	}

	result, err := s.QueryRich(ctx, req)
	if err != nil {
		t.Fatalf("QueryRich(tags): unexpected error: %v", err)
	}
	if len(result.Points) != 2 {
		t.Errorf("QueryRich(tags): got %d points, want 2", len(result.Points))
	}
	for _, p := range result.Points {
		if p.Tags["host"] != "srv1" {
			t.Errorf("QueryRich(tags): point has host=%q, want srv1", p.Tags["host"])
		}
	}
}

func TestStorage_QueryRich_WithAggregation(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = s.Write("cpu.usage", 10.0, base, nil)
	_ = s.Write("cpu.usage", 20.0, base.Add(time.Minute), nil)
	_ = s.Write("cpu.usage", 30.0, base.Add(2*time.Minute), nil)

	req := &QueryRequest{
		Metric:      "cpu.usage",
		StartTime:   base,
		EndTime:     base.Add(2 * time.Minute),
		Aggregation: AggAvg,
	}

	result, err := s.QueryRich(ctx, req)
	if err != nil {
		t.Fatalf("QueryRich(agg): unexpected error: %v", err)
	}
	if result.AggregatedValue == nil {
		t.Fatal("QueryRich(agg): expected non-nil AggregatedValue")
	}
	if *result.AggregatedValue != 20.0 {
		t.Errorf("QueryRich(agg): got %f, want 20.0", *result.AggregatedValue)
	}
}

func TestStorage_QueryRich_WithDownsampling(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	base := ts(2026, 3, 1, 10, 0, 0)

	// Write 6 points across 2 hours.
	for i := 0; i < 6; i++ {
		_ = s.Write("cpu.usage", float64(i+1)*10.0, base.Add(time.Duration(i)*20*time.Minute), nil)
	}

	req := &QueryRequest{
		Metric:      "cpu.usage",
		StartTime:   base,
		EndTime:     base.Add(100 * time.Minute),
		Aggregation: AggSum,
		Interval:    time.Hour,
	}

	result, err := s.QueryRich(ctx, req)
	if err != nil {
		t.Fatalf("QueryRich(downsample): unexpected error: %v", err)
	}
	// With hour-sized buckets over 100 minutes, we should get 2 buckets.
	if len(result.Points) < 2 {
		t.Errorf("QueryRich(downsample): got %d buckets, want >= 2", len(result.Points))
	}
	// AggregatedValue should be nil when downsampling is used.
	if result.AggregatedValue != nil {
		t.Error("QueryRich(downsample): expected nil AggregatedValue for downsampled query")
	}
}

func TestStorage_QueryRich_WithGroupBy(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = s.Write("cpu.usage", 10.0, base, map[string]string{"host": "srv1", "region": "us"})
	_ = s.Write("cpu.usage", 20.0, base.Add(time.Minute), map[string]string{"host": "srv2", "region": "us"})
	_ = s.Write("cpu.usage", 30.0, base.Add(2*time.Minute), map[string]string{"host": "srv1", "region": "eu"})
	_ = s.Write("cpu.usage", 40.0, base.Add(3*time.Minute), map[string]string{"host": "srv2", "region": "eu"})

	req := &QueryRequest{
		Metric:      "cpu.usage",
		StartTime:   base,
		EndTime:     base.Add(3 * time.Minute),
		Aggregation: AggSum,
		GroupBy:     []string{"region"},
	}

	result, err := s.QueryRich(ctx, req)
	if err != nil {
		t.Fatalf("QueryRich(groupBy): unexpected error: %v", err)
	}
	if len(result.GroupedResults) != 2 {
		t.Fatalf("QueryRich(groupBy): got %d groups, want 2", len(result.GroupedResults))
	}
	// Verify each group has aggregated values.
	for _, group := range result.GroupedResults {
		if group.AggregatedValue == nil {
			t.Error("QueryRich(groupBy): expected non-nil AggregatedValue per group")
		}
		region := group.Tags["region"]
		if region == "us" && *group.AggregatedValue != 30.0 {
			t.Errorf("QueryRich(groupBy): US sum got %f, want 30.0", *group.AggregatedValue)
		}
		if region == "eu" && *group.AggregatedValue != 70.0 {
			t.Errorf("QueryRich(groupBy): EU sum got %f, want 70.0", *group.AggregatedValue)
		}
	}
}

func TestStorage_QueryRich_WithGroupByAndInterval(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = s.Write("cpu.usage", 10.0, base, map[string]string{"host": "srv1"})
	_ = s.Write("cpu.usage", 20.0, base.Add(30*time.Minute), map[string]string{"host": "srv1"})
	_ = s.Write("cpu.usage", 30.0, base.Add(time.Hour), map[string]string{"host": "srv1"})
	_ = s.Write("cpu.usage", 40.0, base, map[string]string{"host": "srv2"})

	req := &QueryRequest{
		Metric:      "cpu.usage",
		StartTime:   base,
		EndTime:     base.Add(time.Hour),
		Aggregation: AggSum,
		GroupBy:     []string{"host"},
		Interval:    time.Hour,
	}

	result, err := s.QueryRich(ctx, req)
	if err != nil {
		t.Fatalf("QueryRich(groupBy+interval): unexpected error: %v", err)
	}
	if len(result.GroupedResults) != 2 {
		t.Fatalf("QueryRich(groupBy+interval): got %d groups, want 2", len(result.GroupedResults))
	}
	// With interval set, each group should have downsampled Points, not AggregatedValue.
	for _, group := range result.GroupedResults {
		if len(group.Points) == 0 {
			t.Error("QueryRich(groupBy+interval): expected non-empty Points per group")
		}
	}
}

func TestStorage_QueryRich_WithLimit(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	base := ts(2026, 3, 1, 10, 0, 0)

	for i := 0; i < 10; i++ {
		_ = s.Write("cpu.usage", float64(i), base.Add(time.Duration(i)*time.Minute), nil)
	}

	req := &QueryRequest{
		Metric:    "cpu.usage",
		StartTime: base,
		EndTime:   base.Add(9 * time.Minute),
		Limit:     3,
	}

	result, err := s.QueryRich(ctx, req)
	if err != nil {
		t.Fatalf("QueryRich(limit): unexpected error: %v", err)
	}
	if len(result.Points) != 3 {
		t.Errorf("QueryRich(limit): got %d points, want 3", len(result.Points))
	}
}

func TestStorage_QueryRich_EmptyResult(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	base := ts(2026, 3, 1, 10, 0, 0)

	req := &QueryRequest{
		Metric:    "nonexistent.metric",
		StartTime: base,
		EndTime:   base.Add(time.Hour),
	}

	result, err := s.QueryRich(ctx, req)
	if err != nil {
		t.Fatalf("QueryRich(empty): unexpected error: %v", err)
	}
	if len(result.Points) != 0 {
		t.Errorf("QueryRich(empty): got %d points, want 0", len(result.Points))
	}
}

func TestStorage_QueryRich_InvalidMetric(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()

	req := &QueryRequest{
		Metric:    "",
		StartTime: time.Now(),
		EndTime:   time.Now().Add(time.Hour),
	}

	_, err := s.QueryRich(ctx, req)
	if err == nil {
		t.Fatal("QueryRich(empty metric): expected error, got nil")
	}
	var ime *InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("QueryRich(empty metric): expected InvalidMetricError, got %T: %v", err, err)
	}
}

func TestStorage_QueryRich_EndBeforeStart(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()

	req := &QueryRequest{
		Metric:    "cpu.usage",
		StartTime: ts(2026, 3, 1, 12, 0, 0),
		EndTime:   ts(2026, 3, 1, 10, 0, 0),
	}

	_, err := s.QueryRich(ctx, req)
	if err == nil {
		t.Fatal("QueryRich(end before start): expected error, got nil")
	}
	var itr *InvalidTimeRangeError
	if !errors.As(err, &itr) {
		t.Fatalf("QueryRich(end before start): expected InvalidTimeRangeError, got %T: %v", err, err)
	}
}

func TestStorage_QueryRich_AfterClose(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()
	ctx := context.Background()

	req := &QueryRequest{
		Metric:    "cpu.usage",
		StartTime: time.Now(),
		EndTime:   time.Now().Add(time.Hour),
	}

	_, err := s.QueryRich(ctx, req)
	if err == nil {
		t.Fatal("QueryRich(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("QueryRich(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestStorage_QueryRich_InvalidAggFunc(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()

	req := &QueryRequest{
		Metric:      "cpu.usage",
		StartTime:   ts(2026, 3, 1, 10, 0, 0),
		EndTime:     ts(2026, 3, 1, 11, 0, 0),
		Aggregation: AggFunc(99),
	}

	_, err := s.QueryRich(ctx, req)
	if err == nil {
		t.Fatal("QueryRich(invalid agg): expected error, got nil")
	}
	var iaf *InvalidAggFuncError
	if !errors.As(err, &iaf) {
		t.Fatalf("QueryRich(invalid agg): expected InvalidAggFuncError, got %T: %v", err, err)
	}
}

func TestStorage_QueryRich_PercentileAggregation(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	base := ts(2026, 3, 1, 10, 0, 0)

	for i := 1; i <= 100; i++ {
		_ = s.Write("latency", float64(i), base.Add(time.Duration(i)*time.Second), nil)
	}

	req := &QueryRequest{
		Metric:      "latency",
		StartTime:   base,
		EndTime:     base.Add(101 * time.Second),
		Aggregation: AggPercentile,
		Percentile:  95.0,
	}

	result, err := s.QueryRich(ctx, req)
	if err != nil {
		t.Fatalf("QueryRich(percentile): unexpected error: %v", err)
	}
	if result.AggregatedValue == nil {
		t.Fatal("QueryRich(percentile): expected non-nil AggregatedValue")
	}
	if *result.AggregatedValue < 90.0 {
		t.Errorf("QueryRich(percentile): p95 got %f, expected >= 90.0", *result.AggregatedValue)
	}
}

func TestStorage_QueryRich_PercentileInvalidRange(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()

	req := &QueryRequest{
		Metric:      "latency",
		StartTime:   ts(2026, 3, 1, 10, 0, 0),
		EndTime:     ts(2026, 3, 1, 11, 0, 0),
		Aggregation: AggPercentile,
		Percentile:  150.0,
	}

	_, err := s.QueryRich(ctx, req)
	if err == nil {
		t.Fatal("QueryRich(percentile>100): expected error, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("QueryRich(percentile>100): expected ValidationError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DeleteSeries
// ---------------------------------------------------------------------------

func TestStorage_DeleteSeries_AllPoints(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = s.Write("cpu.usage", 10.0, base, nil)
	_ = s.Write("cpu.usage", 20.0, base.Add(time.Minute), nil)
	_ = s.Write("cpu.usage", 30.0, base.Add(2*time.Minute), nil)

	req := &DeleteRequest{Metric: "cpu.usage"}
	if err := s.DeleteSeries(ctx, req); err != nil {
		t.Fatalf("DeleteSeries: unexpected error: %v", err)
	}

	results, err := s.Query("cpu.usage", base, base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("Query after delete: unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Query after delete: got %d results, want 0", len(results))
	}

	// Metric index should also be cleaned.
	metrics, err := s.ListMetrics("", 0)
	if err != nil {
		t.Fatalf("ListMetrics after delete: unexpected error: %v", err)
	}
	for _, m := range metrics {
		if m == "cpu.usage" {
			t.Error("ListMetrics after full delete: metric index not cleaned")
		}
	}
}

func TestStorage_DeleteSeries_TimeRange(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = s.Write("cpu.usage", 10.0, base, nil)
	_ = s.Write("cpu.usage", 20.0, base.Add(time.Minute), nil)
	_ = s.Write("cpu.usage", 30.0, base.Add(2*time.Minute), nil)

	// Delete only middle point.
	req := &DeleteRequest{
		Metric:    "cpu.usage",
		StartTime: base.Add(30 * time.Second),
		EndTime:   base.Add(90 * time.Second),
	}
	if err := s.DeleteSeries(ctx, req); err != nil {
		t.Fatalf("DeleteSeries(range): unexpected error: %v", err)
	}

	results, err := s.Query("cpu.usage", base, base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("Query after range delete: unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Query after range delete: got %d results, want 2", len(results))
	}
}

func TestStorage_DeleteSeries_WithTagFilter(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = s.Write("cpu.usage", 10.0, base, map[string]string{"host": "srv1"})
	_ = s.Write("cpu.usage", 20.0, base.Add(time.Minute), map[string]string{"host": "srv2"})
	_ = s.Write("cpu.usage", 30.0, base.Add(2*time.Minute), map[string]string{"host": "srv1"})

	req := &DeleteRequest{
		Metric: "cpu.usage",
		Tags:   map[string]string{"host": "srv1"},
	}
	if err := s.DeleteSeries(ctx, req); err != nil {
		t.Fatalf("DeleteSeries(tags): unexpected error: %v", err)
	}

	results, err := s.Query("cpu.usage", base, base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("Query after tag delete: unexpected error: %v", err)
	}
	// Only srv2 should remain.
	if len(results) != 1 {
		t.Fatalf("Query after tag delete: got %d results, want 1", len(results))
	}
	if results[0].Tags["host"] != "srv2" {
		t.Errorf("Query after tag delete: remaining point has host=%q, want srv2", results[0].Tags["host"])
	}
}

func TestStorage_DeleteSeries_InvalidMetric(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()

	req := &DeleteRequest{Metric: ""}
	err := s.DeleteSeries(ctx, req)
	if err == nil {
		t.Fatal("DeleteSeries(empty metric): expected error, got nil")
	}
	var ime *InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("DeleteSeries(empty metric): expected InvalidMetricError, got %T: %v", err, err)
	}
}

func TestStorage_DeleteSeries_EndBeforeStart(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()

	req := &DeleteRequest{
		Metric:    "cpu.usage",
		StartTime: ts(2026, 3, 1, 12, 0, 0),
		EndTime:   ts(2026, 3, 1, 10, 0, 0),
	}

	err := s.DeleteSeries(ctx, req)
	if err == nil {
		t.Fatal("DeleteSeries(end before start): expected error, got nil")
	}
	var itr *InvalidTimeRangeError
	if !errors.As(err, &itr) {
		t.Fatalf("DeleteSeries(end before start): expected InvalidTimeRangeError, got %T: %v", err, err)
	}
}

func TestStorage_DeleteSeries_AfterClose(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()
	ctx := context.Background()

	req := &DeleteRequest{Metric: "cpu.usage"}
	err := s.DeleteSeries(ctx, req)
	if err == nil {
		t.Fatal("DeleteSeries(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("DeleteSeries(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestStorage_DeleteSeries_NonExistentMetric(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()

	req := &DeleteRequest{Metric: "nonexistent"}
	// Deleting a metric with no data should not error.
	if err := s.DeleteSeries(ctx, req); err != nil {
		t.Fatalf("DeleteSeries(nonexistent): unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GetRetentionPolicy / SetRetentionPolicy
// ---------------------------------------------------------------------------

func TestStorage_RetentionPolicy_SetAndGet(t *testing.T) {
	s := newStorage(t)

	policy := &RetentionPolicy{
		Duration:    7 * 24 * time.Hour,
		Resolution:  5 * time.Minute,
		Aggregation: AggAvg,
	}

	if err := s.SetRetentionPolicy("cpu.usage", policy); err != nil {
		t.Fatalf("SetRetentionPolicy: unexpected error: %v", err)
	}

	got, err := s.GetRetentionPolicy("cpu.usage")
	if err != nil {
		t.Fatalf("GetRetentionPolicy: unexpected error: %v", err)
	}
	if got.Duration != policy.Duration {
		t.Errorf("Duration: got %v, want %v", got.Duration, policy.Duration)
	}
	if got.Resolution != policy.Resolution {
		t.Errorf("Resolution: got %v, want %v", got.Resolution, policy.Resolution)
	}
	if got.Aggregation != policy.Aggregation {
		t.Errorf("Aggregation: got %v, want %v", got.Aggregation, policy.Aggregation)
	}
}

func TestStorage_GetRetentionPolicy_Default(t *testing.T) {
	s := newStorage(t)

	got, err := s.GetRetentionPolicy("nonexistent.metric")
	if err != nil {
		t.Fatalf("GetRetentionPolicy(default): unexpected error: %v", err)
	}
	if got.Duration != 0 {
		t.Errorf("Duration: got %v, want 0", got.Duration)
	}
	if got.Resolution != 0 {
		t.Errorf("Resolution: got %v, want 0", got.Resolution)
	}
	if got.Aggregation != AggNone {
		t.Errorf("Aggregation: got %v, want AggNone", got.Aggregation)
	}
}

func TestStorage_SetRetentionPolicy_Overwrite(t *testing.T) {
	s := newStorage(t)

	policy1 := &RetentionPolicy{Duration: 24 * time.Hour, Aggregation: AggAvg}
	if err := s.SetRetentionPolicy("cpu.usage", policy1); err != nil {
		t.Fatalf("SetRetentionPolicy(1): unexpected error: %v", err)
	}

	policy2 := &RetentionPolicy{Duration: 48 * time.Hour, Aggregation: AggMax}
	if err := s.SetRetentionPolicy("cpu.usage", policy2); err != nil {
		t.Fatalf("SetRetentionPolicy(2): unexpected error: %v", err)
	}

	got, err := s.GetRetentionPolicy("cpu.usage")
	if err != nil {
		t.Fatalf("GetRetentionPolicy: unexpected error: %v", err)
	}
	if got.Duration != 48*time.Hour {
		t.Errorf("Duration: got %v, want 48h", got.Duration)
	}
	if got.Aggregation != AggMax {
		t.Errorf("Aggregation: got %v, want AggMax", got.Aggregation)
	}
}

func TestStorage_GetRetentionPolicy_AfterClose(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()

	_, err := s.GetRetentionPolicy("cpu.usage")
	if err == nil {
		t.Fatal("GetRetentionPolicy(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("GetRetentionPolicy(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestStorage_SetRetentionPolicy_AfterClose(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()

	err := s.SetRetentionPolicy("cpu.usage", &RetentionPolicy{Duration: time.Hour})
	if err == nil {
		t.Fatal("SetRetentionPolicy(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("SetRetentionPolicy(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ListMetrics
// ---------------------------------------------------------------------------

func TestStorage_ListMetrics_ReturnsAllMetrics(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = s.Write("cpu.usage", 10.0, base, nil)
	_ = s.Write("mem.free", 1024.0, base, nil)
	_ = s.Write("disk.io", 50.0, base, nil)

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

	_ = s.Write("cpu.usage", 10.0, base, nil)
	_ = s.Write("cpu.load", 5.0, base, nil)
	_ = s.Write("mem.free", 1024.0, base, nil)

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

	_ = s.Write("cpu.usage", 10.0, base, nil)
	_ = s.Write("mem.free", 1024.0, base, nil)
	_ = s.Write("disk.io", 50.0, base, nil)

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

func TestEncodeDecodeRetentionPolicy_RoundTrip(t *testing.T) {
	original := &RetentionPolicy{
		Duration:    30 * 24 * time.Hour,
		Resolution:  10 * time.Minute,
		Aggregation: AggSum,
	}

	encoded, err := encodeRetentionPolicy(original)
	if err != nil {
		t.Fatalf("encodeRetentionPolicy: unexpected error: %v", err)
	}
	if len(encoded) != 17 {
		t.Fatalf("encodeRetentionPolicy: got %d bytes, want 17", len(encoded))
	}

	decoded, err := decodeRetentionPolicy(encoded)
	if err != nil {
		t.Fatalf("decodeRetentionPolicy: unexpected error: %v", err)
	}
	if decoded.Duration != original.Duration {
		t.Errorf("Duration: got %v, want %v", decoded.Duration, original.Duration)
	}
	if decoded.Resolution != original.Resolution {
		t.Errorf("Resolution: got %v, want %v", decoded.Resolution, original.Resolution)
	}
	if decoded.Aggregation != original.Aggregation {
		t.Errorf("Aggregation: got %v, want %v", decoded.Aggregation, original.Aggregation)
	}
}

func TestDecodeRetentionPolicy_TooShort(t *testing.T) {
	_, err := decodeRetentionPolicy([]byte{0x01, 0x02, 0x03})
	if err == nil {
		t.Fatal("decodeRetentionPolicy(too short): expected error, got nil")
	}
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("decodeRetentionPolicy(too short): expected DecodeError, got %T: %v", err, err)
	}
}

func TestEncodeRetentionPolicy_ZeroValue(t *testing.T) {
	original := &RetentionPolicy{}
	encoded, err := encodeRetentionPolicy(original)
	if err != nil {
		t.Fatalf("encodeRetentionPolicy(zero): unexpected error: %v", err)
	}
	decoded, err := decodeRetentionPolicy(encoded)
	if err != nil {
		t.Fatalf("decodeRetentionPolicy(zero): unexpected error: %v", err)
	}
	if decoded.Duration != 0 || decoded.Resolution != 0 || decoded.Aggregation != AggNone {
		t.Errorf("Expected zero-value policy, got %+v", decoded)
	}
}

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
	ctx := context.Background()
	base := ts(2026, 3, 1, 10, 0, 0)

	// Phase 1: Write data points via WritePoint.
	for i := 0; i < 5; i++ {
		point := &DataPoint{
			Metric:    "app.requests",
			Value:     float64(i+1) * 100.0,
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Tags:      map[string]string{"env": "prod"},
		}
		if err := s.WritePoint(ctx, point); err != nil {
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
	result, err := s.QueryRich(ctx, req)
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
	if err := s.SetRetentionPolicy("app.requests", policy); err != nil {
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
	if err := s.DeleteSeries(ctx, delReq); err != nil {
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

func TestStorage_QueryRich_FirstAggregation(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = s.Write("metric", 10.0, base, nil)
	_ = s.Write("metric", 20.0, base.Add(time.Minute), nil)
	_ = s.Write("metric", 30.0, base.Add(2*time.Minute), nil)

	req := &QueryRequest{
		Metric:      "metric",
		StartTime:   base,
		EndTime:     base.Add(2 * time.Minute),
		Aggregation: AggFirst,
	}

	result, err := s.QueryRich(ctx, req)
	if err != nil {
		t.Fatalf("QueryRich(first): unexpected error: %v", err)
	}
	if result.AggregatedValue == nil {
		t.Fatal("QueryRich(first): expected non-nil AggregatedValue")
	}
	if *result.AggregatedValue != 10.0 {
		t.Errorf("QueryRich(first): got %f, want 10.0", *result.AggregatedValue)
	}
}

func TestStorage_QueryRich_LastAggregation(t *testing.T) {
	s := newStorage(t)
	ctx := context.Background()
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = s.Write("metric", 10.0, base, nil)
	_ = s.Write("metric", 20.0, base.Add(time.Minute), nil)
	_ = s.Write("metric", 30.0, base.Add(2*time.Minute), nil)

	req := &QueryRequest{
		Metric:      "metric",
		StartTime:   base,
		EndTime:     base.Add(2 * time.Minute),
		Aggregation: AggLast,
	}

	result, err := s.QueryRich(ctx, req)
	if err != nil {
		t.Fatalf("QueryRich(last): unexpected error: %v", err)
	}
	if result.AggregatedValue == nil {
		t.Fatal("QueryRich(last): expected non-nil AggregatedValue")
	}
	if *result.AggregatedValue != 30.0 {
		t.Errorf("QueryRich(last): got %f, want 30.0", *result.AggregatedValue)
	}
}
