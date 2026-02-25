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
	"math"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// openPebble creates a PebbleDB in a temporary directory and registers
// cleanup. It fails the test immediately on error.
func openPebble(t *testing.T) *pebble.DB {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("pebble.Close: %v", err)
		}
	})
	return db
}

// newStorage creates a Storage backed by a temp PebbleDB.
func newStorage(t *testing.T) *Storage {
	t.Helper()
	db := openPebble(t)
	s, err := New(db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// ts creates a UTC time helper for test readability.
func ts(year int, month time.Month, day, hour, min, sec int) time.Time {
	return time.Date(year, month, day, hour, min, sec, 0, time.UTC)
}

// ---------------------------------------------------------------------------
// New
// ---------------------------------------------------------------------------

func TestNew_NilDB(t *testing.T) {
	_, err := New(nil)
	if err == nil {
		t.Fatal("New(nil): expected NilDBError, got nil")
	}
	var nde *NilDBError
	if !errors.As(err, &nde) {
		t.Fatalf("New(nil): expected NilDBError, got %T: %v", err, err)
	}
}

func TestNew_ValidDB(t *testing.T) {
	db := openPebble(t)
	s, err := New(db)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	if s == nil {
		t.Fatal("New: returned nil storage")
	}
}

// ---------------------------------------------------------------------------
// Write
// ---------------------------------------------------------------------------

func TestStorage_Write_SinglePoint(t *testing.T) {
	s := newStorage(t)
	timestamp := ts(2026, 2, 24, 10, 0, 0)
	err := s.Write("cpu.usage", 42.5, timestamp, map[string]string{"host": "srv1"})
	if err != nil {
		t.Fatalf("Write: unexpected error: %v", err)
	}

	// Verify by querying.
	results, err := s.Query("cpu.usage", timestamp, timestamp)
	if err != nil {
		t.Fatalf("Query: unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Query: got %d results, want 1", len(results))
	}
	if results[0].Value != 42.5 {
		t.Errorf("Query: value got %f, want 42.5", results[0].Value)
	}
	if results[0].Metric != "cpu.usage" {
		t.Errorf("Query: metric got %q, want %q", results[0].Metric, "cpu.usage")
	}
}

func TestStorage_Write_WithoutTags(t *testing.T) {
	s := newStorage(t)
	timestamp := ts(2026, 2, 24, 10, 0, 0)
	err := s.Write("mem.free", 1024.0, timestamp, nil)
	if err != nil {
		t.Fatalf("Write: unexpected error: %v", err)
	}
	results, _ := s.Query("mem.free", timestamp, timestamp)
	if len(results) != 1 {
		t.Fatalf("Query: got %d results, want 1", len(results))
	}
}

func TestStorage_Write_InvalidMetric(t *testing.T) {
	s := newStorage(t)
	err := s.Write("", 42.5, time.Now(), nil)
	if err == nil {
		t.Fatal("Write(empty metric): expected InvalidMetricError, got nil")
	}
	var ime *InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("Write(empty metric): expected InvalidMetricError, got %T: %v", err, err)
	}
}

func TestStorage_Write_ZeroTimestamp(t *testing.T) {
	s := newStorage(t)
	err := s.Write("cpu.usage", 42.5, time.Time{}, nil)
	if err == nil {
		t.Fatal("Write(zero timestamp): expected InvalidTimestampError, got nil")
	}
	var ite *InvalidTimestampError
	if !errors.As(err, &ite) {
		t.Fatalf("Write(zero timestamp): expected InvalidTimestampError, got %T: %v", err, err)
	}
}

func TestStorage_Write_AfterClose(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()
	err := s.Write("cpu.usage", 42.5, time.Now(), nil)
	if err == nil {
		t.Fatal("Write(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Write(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestStorage_Write_NegativeValue(t *testing.T) {
	s := newStorage(t)
	timestamp := ts(2026, 2, 24, 10, 0, 0)
	err := s.Write("temperature", -15.3, timestamp, nil)
	if err != nil {
		t.Fatalf("Write(negative value): unexpected error: %v", err)
	}
	results, _ := s.Query("temperature", timestamp, timestamp)
	if len(results) != 1 || results[0].Value != -15.3 {
		t.Errorf("Query: got %v, want -15.3", results)
	}
}

// ---------------------------------------------------------------------------
// WriteBatch
// ---------------------------------------------------------------------------

func TestStorage_WriteBatch_MultiplePoints(t *testing.T) {
	s := newStorage(t)
	t1 := ts(2026, 2, 24, 10, 0, 0)
	t2 := ts(2026, 2, 24, 10, 1, 0)
	t3 := ts(2026, 2, 24, 10, 2, 0)

	points := []DataPoint{
		{Metric: "cpu.usage", Value: 10.0, Timestamp: t1, Tags: map[string]string{"host": "srv1"}},
		{Metric: "cpu.usage", Value: 20.0, Timestamp: t2, Tags: map[string]string{"host": "srv1"}},
		{Metric: "cpu.usage", Value: 30.0, Timestamp: t3, Tags: map[string]string{"host": "srv1"}},
	}

	if err := s.WriteBatch(points); err != nil {
		t.Fatalf("WriteBatch: unexpected error: %v", err)
	}

	results, err := s.Query("cpu.usage", t1, t3)
	if err != nil {
		t.Fatalf("Query: unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("Query: got %d results, want 3", len(results))
	}
}

func TestStorage_WriteBatch_EmptyBatch(t *testing.T) {
	s := newStorage(t)
	err := s.WriteBatch([]DataPoint{})
	if err != nil {
		t.Fatalf("WriteBatch(empty): unexpected error: %v", err)
	}
}

func TestStorage_WriteBatch_NilBatch(t *testing.T) {
	s := newStorage(t)
	err := s.WriteBatch(nil)
	if err != nil {
		t.Fatalf("WriteBatch(nil): unexpected error: %v", err)
	}
}

func TestStorage_WriteBatch_InvalidMetricInBatch(t *testing.T) {
	s := newStorage(t)
	points := []DataPoint{
		{Metric: "cpu.usage", Value: 10.0, Timestamp: time.Now()},
		{Metric: "", Value: 20.0, Timestamp: time.Now()},
	}
	err := s.WriteBatch(points)
	if err == nil {
		t.Fatal("WriteBatch(invalid metric): expected InvalidMetricError, got nil")
	}
	var ime *InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("WriteBatch(invalid metric): expected InvalidMetricError, got %T: %v", err, err)
	}
}

func TestStorage_WriteBatch_ZeroTimestampInBatch(t *testing.T) {
	s := newStorage(t)
	points := []DataPoint{
		{Metric: "cpu.usage", Value: 10.0, Timestamp: time.Now()},
		{Metric: "cpu.usage", Value: 20.0, Timestamp: time.Time{}},
	}
	err := s.WriteBatch(points)
	if err == nil {
		t.Fatal("WriteBatch(zero timestamp): expected InvalidTimestampError, got nil")
	}
	var ite *InvalidTimestampError
	if !errors.As(err, &ite) {
		t.Fatalf("WriteBatch(zero timestamp): expected InvalidTimestampError, got %T: %v", err, err)
	}
}

func TestStorage_WriteBatch_AfterClose(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()
	points := []DataPoint{
		{Metric: "cpu.usage", Value: 10.0, Timestamp: time.Now()},
	}
	err := s.WriteBatch(points)
	if err == nil {
		t.Fatal("WriteBatch(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("WriteBatch(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Query
// ---------------------------------------------------------------------------

func TestStorage_Query_TimeRange(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 2, 24, 10, 0, 0)

	for i := 0; i < 10; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		_ = s.Write("cpu.usage", float64(i), ts, nil)
	}

	// Query subset.
	start := base.Add(2 * time.Minute)
	end := base.Add(5 * time.Minute)
	results, err := s.Query("cpu.usage", start, end)
	if err != nil {
		t.Fatalf("Query: unexpected error: %v", err)
	}
	if len(results) != 4 { // inclusive: 2, 3, 4, 5
		t.Errorf("Query: got %d results, want 4", len(results))
	}
}

func TestStorage_Query_TagFiltering(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 2, 24, 10, 0, 0)

	_ = s.Write("cpu.usage", 10.0, base, map[string]string{"host": "srv1"})
	_ = s.Write("cpu.usage", 20.0, base.Add(time.Minute), map[string]string{"host": "srv2"})
	_ = s.Write("cpu.usage", 30.0, base.Add(2*time.Minute), map[string]string{"host": "srv1"})

	results, err := s.Query("cpu.usage", base, base.Add(2*time.Minute),
		WithTags(map[string]string{"host": "srv1"}))
	if err != nil {
		t.Fatalf("Query: unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Query(tag filter): got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.Tags["host"] != "srv1" {
			t.Errorf("Query(tag filter): unexpected host tag %q", r.Tags["host"])
		}
	}
}

func TestStorage_Query_Limit(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 2, 24, 10, 0, 0)

	for i := 0; i < 10; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		_ = s.Write("cpu.usage", float64(i), ts, nil)
	}

	results, err := s.Query("cpu.usage", base, base.Add(9*time.Minute),
		WithTSLimit(3))
	if err != nil {
		t.Fatalf("Query: unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("Query(limit): got %d results, want 3", len(results))
	}
}

func TestStorage_Query_Descending(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 2, 24, 10, 0, 0)

	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		_ = s.Write("cpu.usage", float64(i), ts, nil)
	}

	results, err := s.Query("cpu.usage", base, base.Add(4*time.Minute),
		WithDescending())
	if err != nil {
		t.Fatalf("Query: unexpected error: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("Query(descending): got %d results, want 5", len(results))
	}
	// First result should have the highest value (most recent).
	if results[0].Value != 4.0 {
		t.Errorf("Query(descending): first result value got %f, want 4.0", results[0].Value)
	}
	if results[4].Value != 0.0 {
		t.Errorf("Query(descending): last result value got %f, want 0.0", results[4].Value)
	}
}

func TestStorage_Query_DescendingWithLimit(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 2, 24, 10, 0, 0)

	for i := 0; i < 10; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		_ = s.Write("cpu.usage", float64(i), ts, nil)
	}

	results, err := s.Query("cpu.usage", base, base.Add(9*time.Minute),
		WithDescending(), WithTSLimit(3))
	if err != nil {
		t.Fatalf("Query: unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("Query(descending+limit): got %d results, want 3", len(results))
	}
	// Should get the 3 most recent.
	if results[0].Value != 9.0 {
		t.Errorf("Query(descending+limit): first value got %f, want 9.0", results[0].Value)
	}
}

func TestStorage_Query_EmptyResult(t *testing.T) {
	s := newStorage(t)
	start := ts(2026, 2, 24, 10, 0, 0)
	end := ts(2026, 2, 24, 11, 0, 0)

	results, err := s.Query("cpu.usage", start, end)
	if err != nil {
		t.Fatalf("Query(empty): unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Query(empty): got %d results, want 0", len(results))
	}
}

func TestStorage_Query_InvalidMetric(t *testing.T) {
	s := newStorage(t)
	_, err := s.Query("", time.Now(), time.Now())
	if err == nil {
		t.Fatal("Query(empty metric): expected InvalidMetricError, got nil")
	}
	var ime *InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("Query(empty metric): expected InvalidMetricError, got %T: %v", err, err)
	}
}

func TestStorage_Query_ZeroTimestamps(t *testing.T) {
	s := newStorage(t)
	_, err := s.Query("cpu.usage", time.Time{}, time.Now())
	if err == nil {
		t.Fatal("Query(zero start): expected InvalidTimestampError, got nil")
	}
	var ite *InvalidTimestampError
	if !errors.As(err, &ite) {
		t.Fatalf("Query(zero start): expected InvalidTimestampError, got %T: %v", err, err)
	}
}

func TestStorage_Query_EndBeforeStart(t *testing.T) {
	s := newStorage(t)
	start := ts(2026, 2, 24, 12, 0, 0)
	end := ts(2026, 2, 24, 10, 0, 0)
	_, err := s.Query("cpu.usage", start, end)
	if err == nil {
		t.Fatal("Query(end before start): expected InvalidTimeRangeError, got nil")
	}
	var itr *InvalidTimeRangeError
	if !errors.As(err, &itr) {
		t.Fatalf("Query(end before start): expected InvalidTimeRangeError, got %T: %v", err, err)
	}
}

func TestStorage_Query_AfterClose(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()
	_, err := s.Query("cpu.usage", time.Now(), time.Now().Add(time.Hour))
	if err == nil {
		t.Fatal("Query(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Query(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Aggregate
// ---------------------------------------------------------------------------

func TestStorage_Aggregate_Avg(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 2, 24, 10, 0, 0)

	_ = s.Write("cpu.usage", 10.0, base, nil)
	_ = s.Write("cpu.usage", 20.0, base.Add(time.Minute), nil)
	_ = s.Write("cpu.usage", 30.0, base.Add(2*time.Minute), nil)

	results, err := s.Aggregate("cpu.usage", base, base.Add(2*time.Minute),
		AggAvg, time.Hour)
	if err != nil {
		t.Fatalf("Aggregate(avg): unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Aggregate(avg): got %d buckets, want 1", len(results))
	}
	if results[0].Value != 20.0 {
		t.Errorf("Aggregate(avg): got %f, want 20.0", results[0].Value)
	}
	if results[0].Count != 3 {
		t.Errorf("Aggregate(avg): count got %d, want 3", results[0].Count)
	}
}

func TestStorage_Aggregate_Sum(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 2, 24, 10, 0, 0)

	_ = s.Write("cpu.usage", 10.0, base, nil)
	_ = s.Write("cpu.usage", 20.0, base.Add(time.Minute), nil)
	_ = s.Write("cpu.usage", 30.0, base.Add(2*time.Minute), nil)

	results, err := s.Aggregate("cpu.usage", base, base.Add(2*time.Minute),
		AggSum, time.Hour)
	if err != nil {
		t.Fatalf("Aggregate(sum): unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Aggregate(sum): got %d buckets, want 1", len(results))
	}
	if results[0].Value != 60.0 {
		t.Errorf("Aggregate(sum): got %f, want 60.0", results[0].Value)
	}
}

func TestStorage_Aggregate_Min(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 2, 24, 10, 0, 0)

	_ = s.Write("cpu.usage", 50.0, base, nil)
	_ = s.Write("cpu.usage", 10.0, base.Add(time.Minute), nil)
	_ = s.Write("cpu.usage", 30.0, base.Add(2*time.Minute), nil)

	results, err := s.Aggregate("cpu.usage", base, base.Add(2*time.Minute),
		AggMin, time.Hour)
	if err != nil {
		t.Fatalf("Aggregate(min): unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Aggregate(min): got %d buckets, want 1", len(results))
	}
	if results[0].Value != 10.0 {
		t.Errorf("Aggregate(min): got %f, want 10.0", results[0].Value)
	}
}

func TestStorage_Aggregate_Max(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 2, 24, 10, 0, 0)

	_ = s.Write("cpu.usage", 10.0, base, nil)
	_ = s.Write("cpu.usage", 50.0, base.Add(time.Minute), nil)
	_ = s.Write("cpu.usage", 30.0, base.Add(2*time.Minute), nil)

	results, err := s.Aggregate("cpu.usage", base, base.Add(2*time.Minute),
		AggMax, time.Hour)
	if err != nil {
		t.Fatalf("Aggregate(max): unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Aggregate(max): got %d buckets, want 1", len(results))
	}
	if results[0].Value != 50.0 {
		t.Errorf("Aggregate(max): got %f, want 50.0", results[0].Value)
	}
}

func TestStorage_Aggregate_Count(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 2, 24, 10, 0, 0)

	_ = s.Write("cpu.usage", 10.0, base, nil)
	_ = s.Write("cpu.usage", 20.0, base.Add(time.Minute), nil)
	_ = s.Write("cpu.usage", 30.0, base.Add(2*time.Minute), nil)

	results, err := s.Aggregate("cpu.usage", base, base.Add(2*time.Minute),
		AggCount, time.Hour)
	if err != nil {
		t.Fatalf("Aggregate(count): unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Aggregate(count): got %d buckets, want 1", len(results))
	}
	if results[0].Value != 3.0 {
		t.Errorf("Aggregate(count): got %f, want 3.0", results[0].Value)
	}
}

func TestStorage_Aggregate_Percentile(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 2, 24, 10, 0, 0)

	for i := 0; i < 100; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		_ = s.Write("latency", float64(i+1), ts, nil)
	}

	results, err := s.Aggregate("latency", base, base.Add(99*time.Second),
		AggPercentile, time.Hour)
	if err != nil {
		t.Fatalf("Aggregate(percentile): unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Aggregate(percentile): got %d buckets, want 1", len(results))
	}
	// 99th percentile of 1..100 should be close to 100.
	if results[0].Value < 95.0 {
		t.Errorf("Aggregate(percentile): got %f, expected >= 95.0", results[0].Value)
	}
}

func TestStorage_Aggregate_MultipleBuckets(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 2, 24, 10, 0, 0)

	// Bucket 1: 10:00
	_ = s.Write("cpu.usage", 10.0, base, nil)
	// Bucket 2: 11:00
	_ = s.Write("cpu.usage", 20.0, base.Add(time.Hour), nil)
	// Bucket 3: 12:00
	_ = s.Write("cpu.usage", 30.0, base.Add(2*time.Hour), nil)

	results, err := s.Aggregate("cpu.usage", base, base.Add(2*time.Hour),
		AggAvg, time.Hour)
	if err != nil {
		t.Fatalf("Aggregate: unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("Aggregate: got %d buckets, want 3", len(results))
	}
	// Buckets should be chronologically ordered.
	for i := 1; i < len(results); i++ {
		if !results[i].Timestamp.After(results[i-1].Timestamp) {
			t.Errorf("Aggregate: buckets not in chronological order at index %d", i)
		}
	}
}

func TestStorage_Aggregate_EmptyData(t *testing.T) {
	s := newStorage(t)
	start := ts(2026, 2, 24, 10, 0, 0)
	end := ts(2026, 2, 24, 11, 0, 0)

	results, err := s.Aggregate("cpu.usage", start, end, AggAvg, time.Hour)
	if err != nil {
		t.Fatalf("Aggregate(empty): unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Aggregate(empty): got %d buckets, want 0", len(results))
	}
}

func TestStorage_Aggregate_InvalidMetric(t *testing.T) {
	s := newStorage(t)
	_, err := s.Aggregate("", time.Now(), time.Now().Add(time.Hour),
		AggAvg, time.Hour)
	if err == nil {
		t.Fatal("Aggregate(empty metric): expected InvalidMetricError, got nil")
	}
	var ime *InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("Aggregate(empty metric): expected InvalidMetricError, got %T: %v", err, err)
	}
}

func TestStorage_Aggregate_ZeroTimestamps(t *testing.T) {
	s := newStorage(t)
	_, err := s.Aggregate("cpu.usage", time.Time{}, time.Now(),
		AggAvg, time.Hour)
	if err == nil {
		t.Fatal("Aggregate(zero timestamp): expected InvalidTimestampError, got nil")
	}
	var ite *InvalidTimestampError
	if !errors.As(err, &ite) {
		t.Fatalf("Aggregate(zero timestamp): expected InvalidTimestampError, got %T: %v", err, err)
	}
}

func TestStorage_Aggregate_EndBeforeStart(t *testing.T) {
	s := newStorage(t)
	start := ts(2026, 2, 24, 12, 0, 0)
	end := ts(2026, 2, 24, 10, 0, 0)
	_, err := s.Aggregate("cpu.usage", start, end, AggAvg, time.Hour)
	if err == nil {
		t.Fatal("Aggregate(end before start): expected InvalidTimeRangeError, got nil")
	}
	var itr *InvalidTimeRangeError
	if !errors.As(err, &itr) {
		t.Fatalf("Aggregate(end before start): expected InvalidTimeRangeError, got %T: %v", err, err)
	}
}

func TestStorage_Aggregate_AfterClose(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()
	_, err := s.Aggregate("cpu.usage", time.Now(), time.Now().Add(time.Hour),
		AggAvg, time.Hour)
	if err == nil {
		t.Fatal("Aggregate(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Aggregate(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestStorage_Aggregate_UnknownAggFunc(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 2, 24, 10, 0, 0)
	_ = s.Write("cpu.usage", 10.0, base, nil)

	results, err := s.Aggregate("cpu.usage", base, base,
		AggFunc(99), time.Hour)
	if err != nil {
		t.Fatalf("Aggregate(unknown): unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Aggregate(unknown): got %d buckets, want 1", len(results))
	}
	// Unknown function should return 0.
	if results[0].Value != 0 {
		t.Errorf("Aggregate(unknown): got %f, want 0", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestStorage_Close_ReturnsNil(t *testing.T) {
	s := newStorage(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
}

func TestStorage_Close_SubsequentWriteFails(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()
	err := s.Write("cpu.usage", 1.0, time.Now(), nil)
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Write(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestStorage_Close_SubsequentQueryFails(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()
	_, err := s.Query("cpu.usage", time.Now(), time.Now().Add(time.Hour))
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Query(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestStorage_Close_SubsequentAggregateFails(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()
	_, err := s.Aggregate("cpu.usage", time.Now(), time.Now().Add(time.Hour),
		AggAvg, time.Hour)
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Aggregate(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestStorage_Close_SubsequentWriteBatchFails(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()
	err := s.WriteBatch([]DataPoint{
		{Metric: "cpu.usage", Value: 1.0, Timestamp: time.Now()},
	})
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("WriteBatch(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Encode/Decode round-trip
// ---------------------------------------------------------------------------

func TestEncodeDecodeDataPoint_RoundTrip(t *testing.T) {
	original := &DataPoint{
		Metric:    "cpu.usage",
		Value:     42.5,
		Timestamp: ts(2026, 2, 24, 10, 30, 0),
		Tags: map[string]string{
			"host":   "srv1",
			"region": "us-east",
		},
	}

	encoded, err := encodeDataPoint(original)
	if err != nil {
		t.Fatalf("encodeDataPoint: unexpected error: %v", err)
	}

	decoded, err := decodeDataPoint(encoded)
	if err != nil {
		t.Fatalf("decodeDataPoint: unexpected error: %v", err)
	}

	if decoded.Metric != original.Metric {
		t.Errorf("Metric: got %q, want %q", decoded.Metric, original.Metric)
	}
	if decoded.Value != original.Value {
		t.Errorf("Value: got %f, want %f", decoded.Value, original.Value)
	}
	if !decoded.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp: got %v, want %v", decoded.Timestamp, original.Timestamp)
	}
	if len(decoded.Tags) != len(original.Tags) {
		t.Fatalf("Tags: got %d tags, want %d", len(decoded.Tags), len(original.Tags))
	}
	for k, v := range original.Tags {
		if decoded.Tags[k] != v {
			t.Errorf("Tags[%q]: got %q, want %q", k, decoded.Tags[k], v)
		}
	}
}

func TestEncodeDecodeDataPoint_NoTags(t *testing.T) {
	original := &DataPoint{
		Metric:    "mem.free",
		Value:     1024.0,
		Timestamp: ts(2026, 2, 24, 10, 0, 0),
		Tags:      nil,
	}

	encoded, err := encodeDataPoint(original)
	if err != nil {
		t.Fatalf("encodeDataPoint: unexpected error: %v", err)
	}

	decoded, err := decodeDataPoint(encoded)
	if err != nil {
		t.Fatalf("decodeDataPoint: unexpected error: %v", err)
	}

	if decoded.Metric != original.Metric {
		t.Errorf("Metric: got %q, want %q", decoded.Metric, original.Metric)
	}
	if decoded.Value != original.Value {
		t.Errorf("Value: got %f, want %f", decoded.Value, original.Value)
	}
	if decoded.Tags != nil {
		t.Errorf("Tags: expected nil, got %v", decoded.Tags)
	}
}

func TestEncodeDecodeDataPoint_NegativeValue(t *testing.T) {
	original := &DataPoint{
		Metric:    "temperature",
		Value:     -273.15,
		Timestamp: ts(2026, 1, 1, 0, 0, 0),
	}

	encoded, err := encodeDataPoint(original)
	if err != nil {
		t.Fatalf("encodeDataPoint: unexpected error: %v", err)
	}

	decoded, err := decodeDataPoint(encoded)
	if err != nil {
		t.Fatalf("decodeDataPoint: unexpected error: %v", err)
	}

	if decoded.Value != original.Value {
		t.Errorf("Value: got %f, want %f", decoded.Value, original.Value)
	}
}

func TestEncodeDecodeDataPoint_SpecialFloatValues(t *testing.T) {
	original := &DataPoint{
		Metric:    "special",
		Value:     math.Inf(1),
		Timestamp: ts(2026, 1, 1, 0, 0, 0),
	}

	encoded, err := encodeDataPoint(original)
	if err != nil {
		t.Fatalf("encodeDataPoint: unexpected error: %v", err)
	}

	decoded, err := decodeDataPoint(encoded)
	if err != nil {
		t.Fatalf("decodeDataPoint: unexpected error: %v", err)
	}

	if !math.IsInf(decoded.Value, 1) {
		t.Errorf("Value: got %f, want +Inf", decoded.Value)
	}
}

func TestDecodeDataPoint_TooShort(t *testing.T) {
	_, err := decodeDataPoint([]byte{0x01, 0x02})
	if err == nil {
		t.Fatal("decodeDataPoint(too short): expected DecodeError, got nil")
	}
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("decodeDataPoint(too short): expected DecodeError, got %T: %v", err, err)
	}
}

func TestDecodeDataPoint_MetricLengthExceedsData(t *testing.T) {
	// Create data with metric length pointing beyond available bytes.
	data := make([]byte, 20)
	data[0] = 0xFF // metricLen = 255 (little-endian), way beyond data.
	data[1] = 0x00
	_, err := decodeDataPoint(data)
	if err == nil {
		t.Fatal("decodeDataPoint(bad metric len): expected DecodeError, got nil")
	}
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("decodeDataPoint(bad metric len): expected DecodeError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Key ordering (chronological sort in PebbleDB)
// ---------------------------------------------------------------------------

func TestStorage_KeyOrdering_ChronologicalSort(t *testing.T) {
	s := newStorage(t)

	// Write points out of chronological order.
	t3 := ts(2026, 2, 24, 10, 2, 0)
	t1 := ts(2026, 2, 24, 10, 0, 0)
	t2 := ts(2026, 2, 24, 10, 1, 0)

	_ = s.Write("metric", 30.0, t3, nil)
	_ = s.Write("metric", 10.0, t1, nil)
	_ = s.Write("metric", 20.0, t2, nil)

	results, err := s.Query("metric", t1, t3)
	if err != nil {
		t.Fatalf("Query: unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("Query: got %d results, want 3", len(results))
	}
	// Results should be in chronological order due to key ordering.
	if results[0].Value != 10.0 {
		t.Errorf("result[0]: got %f, want 10.0", results[0].Value)
	}
	if results[1].Value != 20.0 {
		t.Errorf("result[1]: got %f, want 20.0", results[1].Value)
	}
	if results[2].Value != 30.0 {
		t.Errorf("result[2]: got %f, want 30.0", results[2].Value)
	}
}

func TestStorage_KeyOrdering_DifferentMetricsIsolated(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 2, 24, 10, 0, 0)

	_ = s.Write("cpu.usage", 10.0, base, nil)
	_ = s.Write("mem.free", 1024.0, base, nil)
	_ = s.Write("cpu.usage", 20.0, base.Add(time.Minute), nil)

	cpuResults, err := s.Query("cpu.usage", base, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("Query(cpu): unexpected error: %v", err)
	}
	if len(cpuResults) != 2 {
		t.Errorf("Query(cpu): got %d results, want 2", len(cpuResults))
	}

	memResults, err := s.Query("mem.free", base, base)
	if err != nil {
		t.Fatalf("Query(mem): unexpected error: %v", err)
	}
	if len(memResults) != 1 {
		t.Errorf("Query(mem): got %d results, want 1", len(memResults))
	}
}

// ---------------------------------------------------------------------------
// matchesTags helper
// ---------------------------------------------------------------------------

func TestMatchesTags_AllMatch(t *testing.T) {
	dpTags := map[string]string{"host": "srv1", "region": "us-east"}
	filter := map[string]string{"host": "srv1"}
	if !matchesTags(dpTags, filter) {
		t.Error("matchesTags: expected true when all filter tags match")
	}
}

func TestMatchesTags_NoMatch(t *testing.T) {
	dpTags := map[string]string{"host": "srv1"}
	filter := map[string]string{"host": "srv2"}
	if matchesTags(dpTags, filter) {
		t.Error("matchesTags: expected false when values differ")
	}
}

func TestMatchesTags_NilDPTags(t *testing.T) {
	filter := map[string]string{"host": "srv1"}
	if matchesTags(nil, filter) {
		t.Error("matchesTags: expected false when dpTags is nil")
	}
}

func TestMatchesTags_EmptyFilter(t *testing.T) {
	dpTags := map[string]string{"host": "srv1"}
	if !matchesTags(dpTags, map[string]string{}) {
		t.Error("matchesTags: expected true when filter is empty")
	}
}

func TestMatchesTags_MissingKey(t *testing.T) {
	dpTags := map[string]string{"host": "srv1"}
	filter := map[string]string{"region": "us-east"}
	if matchesTags(dpTags, filter) {
		t.Error("matchesTags: expected false when filter key missing from dpTags")
	}
}

// ---------------------------------------------------------------------------
// reverseDataPoints helper
// ---------------------------------------------------------------------------

func TestReverseDataPoints_OddLength(t *testing.T) {
	pts := []DataPoint{
		{Value: 1}, {Value: 2}, {Value: 3},
	}
	reverseDataPoints(pts)
	if pts[0].Value != 3 || pts[1].Value != 2 || pts[2].Value != 1 {
		t.Errorf("reverseDataPoints: got [%f, %f, %f], want [3, 2, 1]",
			pts[0].Value, pts[1].Value, pts[2].Value)
	}
}

func TestReverseDataPoints_EvenLength(t *testing.T) {
	pts := []DataPoint{
		{Value: 1}, {Value: 2}, {Value: 3}, {Value: 4},
	}
	reverseDataPoints(pts)
	if pts[0].Value != 4 || pts[3].Value != 1 {
		t.Errorf("reverseDataPoints: first=%f last=%f, want first=4 last=1",
			pts[0].Value, pts[3].Value)
	}
}

func TestReverseDataPoints_Empty(t *testing.T) {
	// Should not panic.
	reverseDataPoints(nil)
	reverseDataPoints([]DataPoint{})
}

func TestReverseDataPoints_Single(t *testing.T) {
	pts := []DataPoint{{Value: 42}}
	reverseDataPoints(pts)
	if pts[0].Value != 42 {
		t.Errorf("reverseDataPoints(single): got %f, want 42", pts[0].Value)
	}
}

// ---------------------------------------------------------------------------
// computeAggregation helper
// ---------------------------------------------------------------------------

func TestComputeAggregation_Avg(t *testing.T) {
	result := computeAggregation(AggAvg, []float64{10, 20, 30})
	if result != 20.0 {
		t.Errorf("computeAggregation(avg): got %f, want 20.0", result)
	}
}

func TestComputeAggregation_Sum(t *testing.T) {
	result := computeAggregation(AggSum, []float64{10, 20, 30})
	if result != 60.0 {
		t.Errorf("computeAggregation(sum): got %f, want 60.0", result)
	}
}

func TestComputeAggregation_Min(t *testing.T) {
	result := computeAggregation(AggMin, []float64{50, 10, 30})
	if result != 10.0 {
		t.Errorf("computeAggregation(min): got %f, want 10.0", result)
	}
}

func TestComputeAggregation_Max(t *testing.T) {
	result := computeAggregation(AggMax, []float64{10, 50, 30})
	if result != 50.0 {
		t.Errorf("computeAggregation(max): got %f, want 50.0", result)
	}
}

func TestComputeAggregation_Count(t *testing.T) {
	result := computeAggregation(AggCount, []float64{10, 20, 30})
	if result != 3.0 {
		t.Errorf("computeAggregation(count): got %f, want 3.0", result)
	}
}

func TestComputeAggregation_EmptyValues(t *testing.T) {
	result := computeAggregation(AggAvg, []float64{})
	if result != 0 {
		t.Errorf("computeAggregation(empty): got %f, want 0", result)
	}
}

func TestComputeAggregation_Unknown(t *testing.T) {
	result := computeAggregation(AggFunc(99), []float64{10, 20, 30})
	if result != 0 {
		t.Errorf("computeAggregation(unknown): got %f, want 0", result)
	}
}

// ---------------------------------------------------------------------------
// hashTags helper
// ---------------------------------------------------------------------------

func TestHashTags_Deterministic(t *testing.T) {
	tags := map[string]string{"host": "srv1", "region": "us-east"}
	h1 := hashTags(tags)
	h2 := hashTags(tags)
	if h1 != h2 {
		t.Errorf("hashTags: non-deterministic: %d != %d", h1, h2)
	}
}

func TestHashTags_EmptyMap(t *testing.T) {
	h := hashTags(map[string]string{})
	if h != 0 {
		t.Errorf("hashTags(empty): got %d, want 0", h)
	}
}

func TestHashTags_NilMap(t *testing.T) {
	h := hashTags(nil)
	if h != 0 {
		t.Errorf("hashTags(nil): got %d, want 0", h)
	}
}

func TestHashTags_DifferentTagsDifferentHash(t *testing.T) {
	h1 := hashTags(map[string]string{"host": "srv1"})
	h2 := hashTags(map[string]string{"host": "srv2"})
	if h1 == h2 {
		t.Error("hashTags: expected different hashes for different tag values")
	}
}

// ---------------------------------------------------------------------------
// appendHex helper
// ---------------------------------------------------------------------------

func TestAppendHex_Zero(t *testing.T) {
	result := appendHex(nil, 0)
	if string(result) != "0000000000000000" {
		t.Errorf("appendHex(0): got %q, want 16 zeros", string(result))
	}
}

func TestAppendHex_MaxUint64(t *testing.T) {
	result := appendHex(nil, math.MaxUint64)
	if string(result) != "ffffffffffffffff" {
		t.Errorf("appendHex(max): got %q, want all f's", string(result))
	}
}

func TestAppendHex_KnownValue(t *testing.T) {
	result := appendHex(nil, 0xDEADBEEF)
	if string(result) != "00000000deadbeef" {
		t.Errorf("appendHex(0xDEADBEEF): got %q, want '00000000deadbeef'", string(result))
	}
}

func TestAppendHex_AppendsToExisting(t *testing.T) {
	dst := []byte("prefix:")
	result := appendHex(dst, 0xFF)
	if string(result) != "prefix:00000000000000ff" {
		t.Errorf("appendHex(append): got %q", string(result))
	}
}
