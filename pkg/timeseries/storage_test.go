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
	"math"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newStorage creates a Storage backed by an in-memory storage.Storage.
func newStorage(t *testing.T) *Storage {
	t.Helper()
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })
	s, err := New(stg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// write stages and commits a single data point in its own batch -- the
// granularity a caller outside a Raft apply loop uses.
func write(t *testing.T, s *Storage, metric string, value float64, timestamp time.Time, tags map[string]string) error {
	t.Helper()
	b := s.stg.Batch()
	defer func() { _ = b.Close() }()
	if err := s.Write(b, metric, value, timestamp, tags); err != nil {
		return err
	}
	return b.Commit()
}

func TestStorage_View_ReadsThroughSnapshot(t *testing.T) {
	s := newStorage(t)
	at := time.Unix(1_700_000_000, 0).UTC()
	if err := write(t, s, "cpu", 1, at, nil); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	snap, err := s.stg.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()
	if err := write(t, s, "cpu", 2, at.Add(time.Second), nil); err != nil {
		t.Fatalf("write 2: %v", err)
	}

	view, err := s.View(snap).Query("cpu", at.Add(-time.Minute), at.Add(time.Minute))
	if err != nil {
		t.Fatalf("View.Query: %v", err)
	}
	if len(view) != 1 || view[0].Value != 1 {
		t.Fatalf("View.Query = %+v, want the one point before the snapshot", view)
	}
	live, err := s.Query("cpu", at.Add(-time.Minute), at.Add(time.Minute))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(live) != 2 {
		t.Fatalf("Query(live) = %d points, want 2", len(live))
	}
}

func TestStorage_View_SharesClosedState(t *testing.T) {
	s := newStorage(t)
	view := s.View(s.stg)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	at := time.Unix(1_700_000_000, 0).UTC()
	_, err := view.Query("cpu", at, at.Add(time.Minute))
	var closed *ClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("View.Query after Close = %v, want *ClosedError", err)
	}
}

// writeBatch stages and commits a slice of data points in one batch.
func writeBatch(t *testing.T, s *Storage, points []DataPoint) error {
	t.Helper()
	b := s.stg.Batch()
	defer func() { _ = b.Close() }()
	if err := s.WriteBatch(b, points); err != nil {
		return err
	}
	return b.Commit()
}

// writePoint stages and commits a single validated DataPoint in its own batch.
func writePoint(t *testing.T, s *Storage, point *DataPoint) error {
	t.Helper()
	b := s.stg.Batch()
	defer func() { _ = b.Close() }()
	if err := s.WritePoint(b, point); err != nil {
		return err
	}
	return b.Commit()
}

// deleteSeries stages and commits a DeleteRequest in its own batch.
func deleteSeries(t *testing.T, s *Storage, req *DeleteRequest) error {
	t.Helper()
	b := s.stg.Batch()
	defer func() { _ = b.Close() }()
	if err := s.DeleteSeries(b, req); err != nil {
		return err
	}
	return b.Commit()
}

// setRetentionPolicy stages and commits a retention policy in its own batch.
func setRetentionPolicy(t *testing.T, s *Storage, metric string, policy *RetentionPolicy) error {
	t.Helper()
	b := s.stg.Batch()
	defer func() { _ = b.Close() }()
	if err := s.SetRetentionPolicy(b, metric, policy); err != nil {
		return err
	}
	return b.Commit()
}

// applyRetention stages and commits a retention sweep in its own batch.
func applyRetention(t *testing.T, s *Storage, metric string, cutoff time.Time) error {
	t.Helper()
	b := s.stg.Batch()
	defer func() { _ = b.Close() }()
	if err := s.ApplyRetention(b, metric, cutoff); err != nil {
		return err
	}
	return b.Commit()
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
	s, err := New(memory.NewStorage())
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
	err := write(t, s, "cpu.usage", 42.5, timestamp, map[string]string{"host": "srv1"})
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
	err := write(t, s, "mem.free", 1024.0, timestamp, nil)
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
	err := write(t, s, "", 42.5, time.Now(), nil)
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
	err := write(t, s, "cpu.usage", 42.5, time.Time{}, nil)
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
	err := write(t, s, "cpu.usage", 42.5, time.Now(), nil)
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
	err := write(t, s, "temperature", -15.3, timestamp, nil)
	if err != nil {
		t.Fatalf("Write(negative value): unexpected error: %v", err)
	}
	results, _ := s.Query("temperature", timestamp, timestamp)
	if len(results) != 1 || results[0].Value != -15.3 {
		t.Errorf("Query: got %v, want -15.3", results)
	}
}

func TestStorage_WritePoint_ValidPoint(t *testing.T) {
	s := newStorage(t)
	timestamp := ts(2026, 3, 1, 12, 0, 0)

	point := &DataPoint{
		Metric:    "cpu.load",
		Value:     75.5,
		Timestamp: timestamp,
		Tags:      map[string]string{"host": "node1"},
	}

	if err := writePoint(t, s, point); err != nil {
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

// TestStorage_WritePoint_EmptyMetric checks that WritePoint validates the
// point; each field rule of DataPoint.Validate has its own test.
func TestStorage_WritePoint_EmptyMetric(t *testing.T) {
	s := newStorage(t)

	point := &DataPoint{
		Metric:    "",
		Value:     1.0,
		Timestamp: time.Now(),
	}

	err := writePoint(t, s, point)
	if err == nil {
		t.Fatal("WritePoint(empty metric): expected error, got nil")
	}
	var ime *InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("WritePoint(empty metric): expected InvalidMetricError, got %T: %v", err, err)
	}
}

func TestStorage_WritePoint_AfterClose(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()

	point := &DataPoint{
		Metric:    "cpu.load",
		Value:     1.0,
		Timestamp: time.Now(),
	}

	err := writePoint(t, s, point)
	if err == nil {
		t.Fatal("WritePoint(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("WritePoint(after close): expected ClosedError, got %T: %v", err, err)
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

	if err := writeBatch(t, s, points); err != nil {
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
	err := writeBatch(t, s, []DataPoint{})
	if err != nil {
		t.Fatalf("WriteBatch(empty): unexpected error: %v", err)
	}
}

func TestStorage_WriteBatch_NilBatch(t *testing.T) {
	s := newStorage(t)
	err := writeBatch(t, s, nil)
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
	err := writeBatch(t, s, points)
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
	err := writeBatch(t, s, points)
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
	err := writeBatch(t, s, points)
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
		_ = write(t, s, "cpu.usage", float64(i), ts, nil)
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

	_ = write(t, s, "cpu.usage", 10.0, base, map[string]string{"host": "srv1"})
	_ = write(t, s, "cpu.usage", 20.0, base.Add(time.Minute), map[string]string{"host": "srv2"})
	_ = write(t, s, "cpu.usage", 30.0, base.Add(2*time.Minute), map[string]string{"host": "srv1"})

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
		_ = write(t, s, "cpu.usage", float64(i), ts, nil)
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
		_ = write(t, s, "cpu.usage", float64(i), ts, nil)
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
		_ = write(t, s, "cpu.usage", float64(i), ts, nil)
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

func TestStorage_QueryRich_RawPoints(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	for i := 0; i < 5; i++ {
		_ = write(t, s, "req.latency", float64(i+1)*10.0, base.Add(time.Duration(i)*time.Minute), map[string]string{"host": "web1"})
	}

	req := &QueryRequest{
		Metric:    "req.latency",
		StartTime: base,
		EndTime:   base.Add(4 * time.Minute),
	}

	result, err := s.QueryRich(req)
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
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = write(t, s, "cpu.usage", 10.0, base, map[string]string{"host": "srv1"})
	_ = write(t, s, "cpu.usage", 20.0, base.Add(time.Minute), map[string]string{"host": "srv2"})
	_ = write(t, s, "cpu.usage", 30.0, base.Add(2*time.Minute), map[string]string{"host": "srv1"})

	req := &QueryRequest{
		Metric:    "cpu.usage",
		StartTime: base,
		EndTime:   base.Add(2 * time.Minute),
		Tags:      map[string]string{"host": "srv1"},
	}

	result, err := s.QueryRich(req)
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
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = write(t, s, "cpu.usage", 10.0, base, nil)
	_ = write(t, s, "cpu.usage", 20.0, base.Add(time.Minute), nil)
	_ = write(t, s, "cpu.usage", 30.0, base.Add(2*time.Minute), nil)

	req := &QueryRequest{
		Metric:      "cpu.usage",
		StartTime:   base,
		EndTime:     base.Add(2 * time.Minute),
		Aggregation: AggAvg,
	}

	result, err := s.QueryRich(req)
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

func TestStorage_QueryRich_FirstAggregation(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = write(t, s, "metric", 10.0, base, nil)
	_ = write(t, s, "metric", 20.0, base.Add(time.Minute), nil)
	_ = write(t, s, "metric", 30.0, base.Add(2*time.Minute), nil)

	req := &QueryRequest{
		Metric:      "metric",
		StartTime:   base,
		EndTime:     base.Add(2 * time.Minute),
		Aggregation: AggFirst,
	}

	result, err := s.QueryRich(req)
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
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = write(t, s, "metric", 10.0, base, nil)
	_ = write(t, s, "metric", 20.0, base.Add(time.Minute), nil)
	_ = write(t, s, "metric", 30.0, base.Add(2*time.Minute), nil)

	req := &QueryRequest{
		Metric:      "metric",
		StartTime:   base,
		EndTime:     base.Add(2 * time.Minute),
		Aggregation: AggLast,
	}

	result, err := s.QueryRich(req)
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

func TestStorage_QueryRich_PercentileAggregation(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	for i := 1; i <= 100; i++ {
		_ = write(t, s, "latency", float64(i), base.Add(time.Duration(i)*time.Second), nil)
	}

	req := &QueryRequest{
		Metric:      "latency",
		StartTime:   base,
		EndTime:     base.Add(101 * time.Second),
		Aggregation: AggPercentile,
		Percentile:  95.0,
	}

	result, err := s.QueryRich(req)
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

func TestStorage_QueryRich_WithDownsampling(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	// Write 6 points across 2 hours.
	for i := 0; i < 6; i++ {
		_ = write(t, s, "cpu.usage", float64(i+1)*10.0, base.Add(time.Duration(i)*20*time.Minute), nil)
	}

	req := &QueryRequest{
		Metric:      "cpu.usage",
		StartTime:   base,
		EndTime:     base.Add(100 * time.Minute),
		Aggregation: AggSum,
		Interval:    time.Hour,
	}

	result, err := s.QueryRich(req)
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
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = write(t, s, "cpu.usage", 10.0, base, map[string]string{"host": "srv1", "region": "us"})
	_ = write(t, s, "cpu.usage", 20.0, base.Add(time.Minute), map[string]string{"host": "srv2", "region": "us"})
	_ = write(t, s, "cpu.usage", 30.0, base.Add(2*time.Minute), map[string]string{"host": "srv1", "region": "eu"})
	_ = write(t, s, "cpu.usage", 40.0, base.Add(3*time.Minute), map[string]string{"host": "srv2", "region": "eu"})

	req := &QueryRequest{
		Metric:      "cpu.usage",
		StartTime:   base,
		EndTime:     base.Add(3 * time.Minute),
		Aggregation: AggSum,
		GroupBy:     []string{"region"},
	}

	result, err := s.QueryRich(req)
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
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = write(t, s, "cpu.usage", 10.0, base, map[string]string{"host": "srv1"})
	_ = write(t, s, "cpu.usage", 20.0, base.Add(30*time.Minute), map[string]string{"host": "srv1"})
	_ = write(t, s, "cpu.usage", 30.0, base.Add(time.Hour), map[string]string{"host": "srv1"})
	_ = write(t, s, "cpu.usage", 40.0, base, map[string]string{"host": "srv2"})

	req := &QueryRequest{
		Metric:      "cpu.usage",
		StartTime:   base,
		EndTime:     base.Add(time.Hour),
		Aggregation: AggSum,
		GroupBy:     []string{"host"},
		Interval:    time.Hour,
	}

	result, err := s.QueryRich(req)
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
	base := ts(2026, 3, 1, 10, 0, 0)

	for i := 0; i < 10; i++ {
		_ = write(t, s, "cpu.usage", float64(i), base.Add(time.Duration(i)*time.Minute), nil)
	}

	req := &QueryRequest{
		Metric:    "cpu.usage",
		StartTime: base,
		EndTime:   base.Add(9 * time.Minute),
		Limit:     3,
	}

	result, err := s.QueryRich(req)
	if err != nil {
		t.Fatalf("QueryRich(limit): unexpected error: %v", err)
	}
	if len(result.Points) != 3 {
		t.Errorf("QueryRich(limit): got %d points, want 3", len(result.Points))
	}
}

func TestStorage_QueryRich_EmptyResult(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	req := &QueryRequest{
		Metric:    "nonexistent.metric",
		StartTime: base,
		EndTime:   base.Add(time.Hour),
	}

	result, err := s.QueryRich(req)
	if err != nil {
		t.Fatalf("QueryRich(empty): unexpected error: %v", err)
	}
	if len(result.Points) != 0 {
		t.Errorf("QueryRich(empty): got %d points, want 0", len(result.Points))
	}
}

// TestStorage_QueryRich_InvalidMetric checks that QueryRich validates the
// request; each rule of QueryRequest.Validate has its own test.
func TestStorage_QueryRich_InvalidMetric(t *testing.T) {
	s := newStorage(t)

	req := &QueryRequest{
		Metric:    "",
		StartTime: time.Now(),
		EndTime:   time.Now().Add(time.Hour),
	}

	_, err := s.QueryRich(req)
	if err == nil {
		t.Fatal("QueryRich(empty metric): expected error, got nil")
	}
	var ime *InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("QueryRich(empty metric): expected InvalidMetricError, got %T: %v", err, err)
	}
}

func TestStorage_QueryRich_AfterClose(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()

	req := &QueryRequest{
		Metric:    "cpu.usage",
		StartTime: time.Now(),
		EndTime:   time.Now().Add(time.Hour),
	}

	_, err := s.QueryRich(req)
	if err == nil {
		t.Fatal("QueryRich(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("QueryRich(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestStorage_DeleteSeries_AllPoints(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = write(t, s, "cpu.usage", 10.0, base, nil)
	_ = write(t, s, "cpu.usage", 20.0, base.Add(time.Minute), nil)
	_ = write(t, s, "cpu.usage", 30.0, base.Add(2*time.Minute), nil)

	req := &DeleteRequest{Metric: "cpu.usage"}
	if err := deleteSeries(t, s, req); err != nil {
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
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = write(t, s, "cpu.usage", 10.0, base, nil)
	_ = write(t, s, "cpu.usage", 20.0, base.Add(time.Minute), nil)
	_ = write(t, s, "cpu.usage", 30.0, base.Add(2*time.Minute), nil)

	// Delete only middle point.
	req := &DeleteRequest{
		Metric:    "cpu.usage",
		StartTime: base.Add(30 * time.Second),
		EndTime:   base.Add(90 * time.Second),
	}
	if err := deleteSeries(t, s, req); err != nil {
		t.Fatalf("DeleteSeries(range): unexpected error: %v", err)
	}

	results, err := s.Query("cpu.usage", base, base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("Query after range delete: unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Query after range delete: got %d results, want 2", len(results))
	}
	if results[0].Value != 10.0 || results[1].Value != 30.0 {
		t.Errorf("Query after range delete: got values %v and %v, want 10 and 30", results[0].Value, results[1].Value)
	}
}

func TestStorage_DeleteSeries_WithTagFilter(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = write(t, s, "cpu.usage", 10.0, base, map[string]string{"host": "srv1"})
	_ = write(t, s, "cpu.usage", 20.0, base.Add(time.Minute), map[string]string{"host": "srv2"})
	_ = write(t, s, "cpu.usage", 30.0, base.Add(2*time.Minute), map[string]string{"host": "srv1"})

	req := &DeleteRequest{
		Metric: "cpu.usage",
		Tags:   map[string]string{"host": "srv1"},
	}
	if err := deleteSeries(t, s, req); err != nil {
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

// TestStorage_DeleteSeries_InvalidMetric checks that DeleteSeries validates
// the request; each rule of DeleteRequest.Validate has its own test.
func TestStorage_DeleteSeries_InvalidMetric(t *testing.T) {
	s := newStorage(t)

	req := &DeleteRequest{Metric: ""}
	err := deleteSeries(t, s, req)
	if err == nil {
		t.Fatal("DeleteSeries(empty metric): expected error, got nil")
	}
	var ime *InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("DeleteSeries(empty metric): expected InvalidMetricError, got %T: %v", err, err)
	}
}

func TestStorage_DeleteSeries_AfterClose(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()

	req := &DeleteRequest{Metric: "cpu.usage"}
	err := deleteSeries(t, s, req)
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

	req := &DeleteRequest{Metric: "nonexistent"}
	// Deleting a metric with no data should not error.
	if err := deleteSeries(t, s, req); err != nil {
		t.Fatalf("DeleteSeries(nonexistent): unexpected error: %v", err)
	}
}

func TestStorage_RetentionPolicy_SetAndGet(t *testing.T) {
	s := newStorage(t)

	policy := &RetentionPolicy{
		Duration:    7 * 24 * time.Hour,
		Resolution:  5 * time.Minute,
		Aggregation: AggAvg,
	}

	if err := setRetentionPolicy(t, s, "cpu.usage", policy); err != nil {
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
	if err := setRetentionPolicy(t, s, "cpu.usage", policy1); err != nil {
		t.Fatalf("SetRetentionPolicy(1): unexpected error: %v", err)
	}

	policy2 := &RetentionPolicy{Duration: 48 * time.Hour, Aggregation: AggMax}
	if err := setRetentionPolicy(t, s, "cpu.usage", policy2); err != nil {
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

	err := setRetentionPolicy(t, s, "cpu.usage", &RetentionPolicy{Duration: time.Hour})
	if err == nil {
		t.Fatal("SetRetentionPolicy(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("SetRetentionPolicy(after close): expected ClosedError, got %T: %v", err, err)
	}
}

func TestStorage_ApplyRetention_DeletesPointsOlderThanCutoff(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = write(t, s, "cpu.usage", 10.0, base, nil)
	_ = write(t, s, "cpu.usage", 20.0, base.Add(time.Minute), nil)
	_ = write(t, s, "cpu.usage", 30.0, base.Add(2*time.Minute), nil)

	// cutoff sits strictly between the first and second point: the first
	// is older than cutoff and expired, the other two are not.
	cutoff := base.Add(30 * time.Second)
	if err := applyRetention(t, s, "cpu.usage", cutoff); err != nil {
		t.Fatalf("ApplyRetention: unexpected error: %v", err)
	}

	results, err := s.Query("cpu.usage", base, base.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("Query after ApplyRetention: unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Query after ApplyRetention: got %d results, want 2", len(results))
	}
	for _, p := range results {
		if p.Timestamp.Before(cutoff) {
			t.Errorf("surviving point at %v is before cutoff %v", p.Timestamp, cutoff)
		}
	}
}

func TestStorage_ApplyRetention_CutoffIsExclusive(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = write(t, s, "cpu.usage", 10.0, base, nil)

	// A point exactly at cutoff is NOT older than cutoff and must survive.
	if err := applyRetention(t, s, "cpu.usage", base); err != nil {
		t.Fatalf("ApplyRetention: unexpected error: %v", err)
	}

	results, err := s.Query("cpu.usage", base.Add(-time.Minute), base.Add(time.Minute))
	if err != nil {
		t.Fatalf("Query after ApplyRetention: unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Query after ApplyRetention: got %d results, want 1 (point at cutoff must survive)", len(results))
	}
}

func TestStorage_ApplyRetention_NonExistentMetric(t *testing.T) {
	s := newStorage(t)

	if err := applyRetention(t, s, "does.not.exist", time.Now()); err != nil {
		t.Fatalf("ApplyRetention(nonexistent metric): unexpected error: %v", err)
	}
}

func TestStorage_ApplyRetention_LeavesOtherMetricsAlone(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	_ = write(t, s, "cpu.usage", 10.0, base, nil)
	_ = write(t, s, "mem.usage", 20.0, base, nil)

	if err := applyRetention(t, s, "cpu.usage", base.Add(time.Hour)); err != nil {
		t.Fatalf("ApplyRetention: unexpected error: %v", err)
	}

	results, err := s.Query("mem.usage", base.Add(-time.Minute), base.Add(time.Minute))
	if err != nil {
		t.Fatalf("Query mem.usage after ApplyRetention: unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Query mem.usage after ApplyRetention: got %d results, want 1 (untouched metric)", len(results))
	}
}

func TestStorage_ApplyRetention_BoundsDeletesPerCall(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 3, 1, 10, 0, 0)

	// One more point than the bound, all expired by the cutoff. WriteBatch
	// commits all of them in one fsync so setup stays fast.
	total := MaxRetentionDeletesPerApply + 1
	points := make([]DataPoint, total)
	for i := 0; i < total; i++ {
		points[i] = DataPoint{Metric: "cpu.usage", Value: float64(i), Timestamp: base.Add(time.Duration(i) * time.Millisecond)}
	}
	if err := writeBatch(t, s, points); err != nil {
		t.Fatalf("WriteBatch: unexpected error: %v", err)
	}

	cutoff := base.Add(time.Duration(total) * time.Millisecond)
	if err := applyRetention(t, s, "cpu.usage", cutoff); err != nil {
		t.Fatalf("ApplyRetention: unexpected error: %v", err)
	}

	results, err := s.Query("cpu.usage", base.Add(-time.Minute), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("Query after bounded ApplyRetention: unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Query after bounded ApplyRetention: got %d results, want 1 (only the bound's overflow survives this call)", len(results))
	}
}

func TestStorage_ApplyRetention_InvalidMetric(t *testing.T) {
	s := newStorage(t)

	err := applyRetention(t, s, "", time.Now())
	if err == nil {
		t.Fatal("ApplyRetention(empty metric): expected error, got nil")
	}
	var ime *InvalidMetricError
	if !errors.As(err, &ime) {
		t.Fatalf("ApplyRetention(empty metric): expected InvalidMetricError, got %T: %v", err, err)
	}
}

func TestStorage_ApplyRetention_AfterClose(t *testing.T) {
	s := newStorage(t)
	_ = s.Close()

	err := applyRetention(t, s, "cpu.usage", time.Now())
	if err == nil {
		t.Fatal("ApplyRetention(after close): expected ClosedError, got nil")
	}
	var ce *ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("ApplyRetention(after close): expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Aggregate
// ---------------------------------------------------------------------------

func TestStorage_Aggregate_Avg(t *testing.T) {
	s := newStorage(t)
	base := ts(2026, 2, 24, 10, 0, 0)

	_ = write(t, s, "cpu.usage", 10.0, base, nil)
	_ = write(t, s, "cpu.usage", 20.0, base.Add(time.Minute), nil)
	_ = write(t, s, "cpu.usage", 30.0, base.Add(2*time.Minute), nil)

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

	_ = write(t, s, "cpu.usage", 10.0, base, nil)
	_ = write(t, s, "cpu.usage", 20.0, base.Add(time.Minute), nil)
	_ = write(t, s, "cpu.usage", 30.0, base.Add(2*time.Minute), nil)

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

	_ = write(t, s, "cpu.usage", 50.0, base, nil)
	_ = write(t, s, "cpu.usage", 10.0, base.Add(time.Minute), nil)
	_ = write(t, s, "cpu.usage", 30.0, base.Add(2*time.Minute), nil)

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

	_ = write(t, s, "cpu.usage", 10.0, base, nil)
	_ = write(t, s, "cpu.usage", 50.0, base.Add(time.Minute), nil)
	_ = write(t, s, "cpu.usage", 30.0, base.Add(2*time.Minute), nil)

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

	_ = write(t, s, "cpu.usage", 10.0, base, nil)
	_ = write(t, s, "cpu.usage", 20.0, base.Add(time.Minute), nil)
	_ = write(t, s, "cpu.usage", 30.0, base.Add(2*time.Minute), nil)

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
		_ = write(t, s, "latency", float64(i+1), ts, nil)
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
	_ = write(t, s, "cpu.usage", 10.0, base, nil)
	// Bucket 2: 11:00
	_ = write(t, s, "cpu.usage", 20.0, base.Add(time.Hour), nil)
	// Bucket 3: 12:00
	_ = write(t, s, "cpu.usage", 30.0, base.Add(2*time.Hour), nil)

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
	_ = write(t, s, "cpu.usage", 10.0, base, nil)

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
	err := write(t, s, "cpu.usage", 1.0, time.Now(), nil)
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
	err := writeBatch(t, s, []DataPoint{
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

// TestDecodeDataPoint_EveryTruncationIsDecodeError cuts a tagged encoding at
// every length short of complete: each cut lands in a different field (metric,
// value, timestamp, tag count, tag key length, key, value length, value) and
// must fail as a DecodeError, never decode a partial point or panic.
func TestDecodeDataPoint_EveryTruncationIsDecodeError(t *testing.T) {
	encoded, err := encodeDataPoint(&DataPoint{
		Metric:    "cpu",
		Value:     1.5,
		Timestamp: ts(2026, 2, 24, 10, 30, 0),
		Tags:      map[string]string{"host": "srv1"},
	})
	if err != nil {
		t.Fatalf("encodeDataPoint: %v", err)
	}
	for n := range len(encoded) {
		_, err := decodeDataPoint(encoded[:n])
		var de *DecodeError
		if !errors.As(err, &de) {
			t.Errorf("decodeDataPoint(%d of %d bytes) = %v, want *DecodeError", n, len(encoded), err)
		}
	}
	if _, err := decodeDataPoint(encoded); err != nil {
		t.Fatalf("decodeDataPoint(complete) = %v, want nil", err)
	}
}

// TestStorage_GetRetentionPolicy_CorruptData verifies stored policy bytes
// too short to decode surface as a DecodeError.
func TestStorage_GetRetentionPolicy_CorruptData(t *testing.T) {
	s := newStorage(t)
	if err := s.stg.Set([]byte(KeyPrefixRetention+"corrupt.metric"), []byte{0x01, 0x02}); err != nil {
		t.Fatalf("stg.Set: %v", err)
	}

	_, err := s.GetRetentionPolicy("corrupt.metric")
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("GetRetentionPolicy = %v, want *DecodeError", err)
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

	_ = write(t, s, "metric", 30.0, t3, nil)
	_ = write(t, s, "metric", 10.0, t1, nil)
	_ = write(t, s, "metric", 20.0, t2, nil)

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

	_ = write(t, s, "cpu.usage", 10.0, base, nil)
	_ = write(t, s, "mem.free", 1024.0, base, nil)
	_ = write(t, s, "cpu.usage", 20.0, base.Add(time.Minute), nil)

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
